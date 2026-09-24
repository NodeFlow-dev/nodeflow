package agent

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const weightsTestConfig = `backend nf_be_a
    mode tcp
    # nf-weights backend=nf_be_a algo=leastping tolerance=0.20 tolerance_ms=0
    # nf-weight server=s1 base=1 cost=0.00
    # nf-weight server=s2 base=2 cost=1.50
    # nf-weight template=dp_ slots=3 base=1 cost=0.00 ips=2001:db8::1=5,192.0.2.7=10
    option tcp-check

backend nf_be_b
    # nf-weights backend=nf_be_b algo=static tolerance=0.00 tolerance_ms=0
    # nf-weight template=pool_ slots=2 base=3 cost=0.00 ips=192.0.2.9=40
`

const serversStateHeader = "1\n# be_id be_name srv_id srv_name srv_addr srv_op_state srv_admin_state srv_uweight srv_iweight srv_time_since_last_change srv_check_status srv_check_result srv_check_health srv_check_state srv_agent_state bk_f_forced_id srv_f_forced_id srv_fqdn srv_port srvrecord srv_use_ssl srv_check_port srv_check_addr srv_agent_addr srv_agent_port\n"

func TestParseWeightAnnotations(t *testing.T) {
	backends, err := ParseWeightAnnotations([]byte(weightsTestConfig))
	require.NoError(t, err)
	require.Len(t, backends, 2)
	a := backends[0]
	assert.Equal(t, "nf_be_a", a.Backend)
	assert.Equal(t, "leastping", a.Algo)
	assert.InDelta(t, 0.2, a.Tolerance, 1e-9)
	require.Len(t, a.Units, 3)
	assert.Equal(t, WeightUnit{Server: "s2", Base: 2, Cost: 1.5}, a.Units[1])
	assert.Equal(t, "dp_", a.Units[2].Template)
	assert.Equal(t, 3, a.Units[2].Slots)
	assert.Equal(t, map[string]int{"2001:db8::1": 5, "192.0.2.7": 10}, a.Units[2].IPs)
	assert.Equal(t, "static", backends[1].Algo)

	none, err := ParseWeightAnnotations([]byte("backend x\n    mode tcp\n"))
	require.NoError(t, err)
	assert.Empty(t, none)

	for _, bad := range []string{
		"# nf-weight server=s1 base=1\n",
		"# nf-weights backend=b algo=fast\n",
		"# nf-weights backend=b algo=static tolerance=2\n",
		"# nf-weights backend=b algo=static\n# nf-weight server=s1 base=0\n",
		"# nf-weights backend=b algo=static\n# nf-weight server=s1 template=t_ slots=1 base=1\n",
		"# nf-weights backend=b algo=static\n# nf-weight template=t_ slots=2 base=1 ips=bad=3\n",
		"# nf-weights backend=b algo=static\n# nf-weight template=t_ slots=2 base=1 ips=192.0.2.1=999\n",
		"# nf-weights backend=b algo=static\n# nf-weight server=s1 base=1 ips=192.0.2.1=3\n",
		"# nf-weights backend=b;x algo=static\n",
	} {
		_, err := ParseWeightAnnotations([]byte(bad))
		assert.Error(t, err, bad)
	}
}

func TestParseShowServersState(t *testing.T) {
	raw := serversStateHeader +
		"7 nf_be_a 1 s1 127.0.0.1 2 0 20 20 3 6 3 4 6 0 0 0 - 9478 - 0 0 - - 0\n" +
		"7 nf_be_a 3 dp_1 - 0 32 5 5 3 1 0 0 14 0 0 0 localhost 9478 - 0 0 - - 0\n" +
		"7 nf_be_a 4 dp_2 2001:0db8:0:0::1 2 0 1 1 3 6 3 4 6 0 0 0 pool 443 - 0 0 - - 0\n"
	state, err := parseShowServersState([]byte(raw))
	require.NoError(t, err)
	assert.Equal(t, RuntimeServerState{Name: "s1", Addr: "127.0.0.1", OpState: 2, UWeight: 20}, state["s1"])
	assert.Equal(t, "", state["dp_1"].Addr)
	assert.Equal(t, 32, state["dp_1"].Admin)
	assert.Equal(t, "2001:db8::1", state["dp_2"].Addr)

	_, err = parseShowServersState([]byte("Unknown command\n"))
	assert.Error(t, err)
	_, err = parseShowServersState([]byte("1\n7 b 1 s1 127.0.0.1 2 0 1 1\n"))
	assert.Error(t, err)
}

func up(addr string, weight int) RuntimeServerState {
	return RuntimeServerState{Addr: addr, OpState: 2, UWeight: weight}
}

func TestDesiredWeightsStaticIPWeights(t *testing.T) {
	backends, err := ParseWeightAnnotations([]byte(weightsTestConfig))
	require.NoError(t, err)
	state := map[string]RuntimeServerState{
		"pool_1": up("192.0.2.9", 3),
		"pool_2": up("192.0.2.10", 3),
	}
	desired := DesiredWeights(backends[1], state, nil)
	assert.Equal(t, map[string]int{"pool_1": 40, "pool_2": 3}, desired)
}

func TestDesiredWeightsLeastPing(t *testing.T) {
	backend := WeightBackend{Backend: "be", Algo: "leastping", Tolerance: 0.2, Units: []WeightUnit{
		{Server: "fast", Base: 1},
		{Server: "near", Base: 1},
		{Server: "slow", Base: 1},
		{Server: "costly", Base: 1, Cost: 3},
		{Server: "down", Base: 1},
		{Server: "unchecked", Base: 1},
	}}
	state := map[string]RuntimeServerState{
		"fast": up("192.0.2.1", 1), "near": up("192.0.2.2", 1), "slow": up("192.0.2.3", 1),
		"costly": up("192.0.2.4", 1), "down": {Addr: "192.0.2.5", OpState: 0, UWeight: 1},
		"unchecked": up("192.0.2.6", 1),
	}
	latency := map[string]ServerLatency{
		"fast": {CheckOK: true, DurationMS: 10}, "near": {CheckOK: true, DurationMS: 12},
		"slow": {CheckOK: true, DurationMS: 40}, "costly": {CheckOK: true, DurationMS: 10},
		"down": {CheckOK: true, DurationMS: 1},
	}
	desired := DesiredWeights(backend, state, latency)
	// Scale K = min(100, 256/1) = 100.
	assert.Equal(t, 100, desired["fast"])
	assert.Equal(t, 100, desired["near"], "12ms is within +20% of 10ms")
	assert.Equal(t, 25, desired["slow"], "10/40 of full weight")
	assert.Equal(t, 33, desired["costly"], "10ms x cost 3 = 30ms")
	assert.Equal(t, 100, desired["down"], "down servers keep the scaled base and never define best")
	assert.Equal(t, 100, desired["unchecked"])

	// Absolute floor: 40ms is within best+30ms.
	backend.ToleranceMS = 30
	assert.Equal(t, 100, DesiredWeights(backend, state, latency)["slow"])
}

func TestDesiredWeightsLeastPingBaseAndIPWeights(t *testing.T) {
	backend := WeightBackend{Backend: "be", Algo: "leastping", Tolerance: 0.2, Units: []WeightUnit{
		{Server: "a", Base: 2},
		{Template: "dp_", Slots: 2, Base: 1, IPs: map[string]int{"192.0.2.8": 4}},
	}}
	state := map[string]RuntimeServerState{"a": up("192.0.2.1", 2), "dp_1": up("192.0.2.8", 1), "dp_2": up("192.0.2.9", 1)}
	latency := map[string]ServerLatency{
		"a": {CheckOK: true, DurationMS: 5}, "dp_1": {CheckOK: true, DurationMS: 5}, "dp_2": {CheckOK: true, DurationMS: 50},
	}
	desired := DesiredWeights(backend, state, latency)
	// max base 4 => K = 64.
	assert.Equal(t, map[string]int{"a": 128, "dp_1": 256, "dp_2": 6}, desired)
}

func TestParseWeightAnnotationsIPCosts(t *testing.T) {
	config := "# nf-weights backend=be algo=leastping tolerance=0.20 tolerance_ms=0\n" +
		"# nf-weight template=dp_ slots=4 base=10 cost=1.50 ips=192.0.2.6=40 ipcosts=192.0.2.5=3.00,2001:0db8::7=0.50\n"
	backends, err := ParseWeightAnnotations([]byte(config))
	require.NoError(t, err)
	unit := backends[0].Units[0]
	assert.Equal(t, map[string]int{"192.0.2.6": 40}, unit.IPs)
	assert.Equal(t, map[string]float64{"192.0.2.5": 3, "2001:db8::7": 0.5}, unit.IPCosts)

	for _, bad := range []string{
		"# nf-weights backend=b algo=leastping\n# nf-weight server=s1 base=1 ipcosts=192.0.2.1=2\n",
		"# nf-weights backend=b algo=leastping\n# nf-weight template=t_ slots=2 base=1 ipcosts=192.0.2.1=0\n",
		"# nf-weights backend=b algo=leastping\n# nf-weight template=t_ slots=2 base=1 ipcosts=192.0.2.1=101\n",
		"# nf-weights backend=b algo=leastping\n# nf-weight template=t_ slots=2 base=1 ipcosts=bad=2\n",
		"# nf-weights backend=b algo=leastping\n# nf-weight template=t_ slots=2 base=1 ipcosts=192.0.2.1=NaN\n",
	} {
		_, err := ParseWeightAnnotations([]byte(bad))
		assert.Error(t, err, bad)
	}
}

func TestDesiredWeightsLeastPingIPCosts(t *testing.T) {
	// Three slots, same measured latency; the per-IP cost decides.
	backend := WeightBackend{Backend: "be", Algo: "leastping", Tolerance: 0.2, Units: []WeightUnit{
		{Template: "dp_", Slots: 3, Base: 1, Cost: 1.5, IPCosts: map[string]float64{"192.0.2.1": 1, "192.0.2.3": 4}},
	}}
	state := map[string]RuntimeServerState{"dp_1": up("192.0.2.1", 1), "dp_2": up("192.0.2.2", 1), "dp_3": up("192.0.2.3", 1)}
	latency := map[string]ServerLatency{
		"dp_1": {CheckOK: true, DurationMS: 10}, "dp_2": {CheckOK: true, DurationMS: 10}, "dp_3": {CheckOK: true, DurationMS: 10},
	}
	desired := DesiredWeights(backend, state, latency)
	// Effective: dp_1 10x1 = 10 (best), dp_2 10x1.5 = 15, dp_3 10x4 = 40.
	assert.Equal(t, map[string]int{"dp_1": 100, "dp_2": 67, "dp_3": 25}, desired)

	// The cost follows the address, not the slot: after DNS moves
	// 192.0.2.3 into dp_1 the expensive cost moves with it.
	state["dp_1"], state["dp_3"] = up("192.0.2.3", 1), up("192.0.2.1", 1)
	desired = DesiredWeights(backend, state, latency)
	assert.Equal(t, map[string]int{"dp_1": 25, "dp_2": 67, "dp_3": 100}, desired)

	// static algo ignores costs entirely.
	backend.Algo = "static"
	assert.Equal(t, map[string]int{"dp_1": 1, "dp_2": 1, "dp_3": 1}, DesiredWeights(backend, state, latency))
}

func TestParseWeightAnnotationsIgnoresUnknownKeys(t *testing.T) {
	// Forward compatibility contract the ipcosts= key relies on: an Agent
	// skips annotation keys it does not know instead of failing the backend.
	backends, err := ParseWeightAnnotations([]byte("# nf-weights backend=be algo=static future=1\n# nf-weight template=t_ slots=1 base=2 later=x\n"))
	require.NoError(t, err)
	assert.Equal(t, 2, backends[0].Units[0].Base)
}

func TestPlanWeightChangesIdempotent(t *testing.T) {
	backend := WeightBackend{Backend: "be", Algo: "static"}
	state := map[string]RuntimeServerState{"s1": up("192.0.2.1", 5), "s2": up("192.0.2.2", 1)}
	changes := PlanWeightChanges(backend, state, map[string]int{"s1": 5, "s2": 7, "gone": 3})
	assert.Equal(t, []WeightChange{{Backend: "be", Server: "s2", Weight: 7}}, changes)
	state["s2"] = up("192.0.2.2", 7)
	assert.Empty(t, PlanWeightChanges(backend, state, map[string]int{"s1": 5, "s2": 7}))

	// leastping deadband: 95 vs 100 is jitter, 50 vs 100 is not.
	lp := WeightBackend{Backend: "be", Algo: "leastping"}
	state = map[string]RuntimeServerState{"s1": up("192.0.2.1", 95), "s2": up("192.0.2.2", 50)}
	changes = PlanWeightChanges(lp, state, map[string]int{"s1": 100, "s2": 100})
	assert.Equal(t, []WeightChange{{Backend: "be", Server: "s2", Weight: 100}}, changes)
}

type fakeWeightRuntime struct {
	states map[string]string
	stat   string
	sent   []WeightChange
}

func (f *fakeWeightRuntime) ShowServersState(_ context.Context, backend string) ([]byte, error) {
	return []byte(f.states[backend]), nil
}

func (f *fakeWeightRuntime) ShowStat(context.Context) ([]byte, error) { return []byte(f.stat), nil }

func (f *fakeWeightRuntime) SetServerWeight(_ context.Context, backend, server string, weight int) error {
	f.sent = append(f.sent, WeightChange{Backend: backend, Server: server, Weight: weight})
	// Reflect the change in the next state read.
	f.states[backend] = replaceUWeight(f.states[backend], server, weight)
	return nil
}

func replaceUWeight(raw, server string, weight int) string {
	state, _ := parseShowServersState([]byte(raw))
	out := serversStateHeader
	for _, name := range []string{"s1", "s2", "dp_1", "dp_2", "dp_3", "pool_1", "pool_2"} {
		srv, ok := state[name]
		if !ok {
			continue
		}
		if name == server {
			srv.UWeight = weight
		}
		addr := srv.Addr
		if addr == "" {
			addr = "-"
		}
		out += "7 b 1 " + name + " " + addr + " " + itoa(srv.OpState) + " " + itoa(srv.Admin) + " " + itoa(srv.UWeight) + " 1 3 6 3 4 6 0 0 0 - 443 - 0 0 - - 0\n"
	}
	return out
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestWeightControllerReconcileIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodeflow.cfg")
	require.NoError(t, os.WriteFile(path, []byte(weightsTestConfig), 0o600))
	runtime := &fakeWeightRuntime{
		states: map[string]string{
			"nf_be_a": replaceUWeight(serversStateHeader+
				"7 b 1 s1 192.0.2.1 2 0 1 1 3 6 3 4 6 0 0 0 - 443 - 0 0 - - 0\n"+
				"7 b 1 s2 192.0.2.2 2 0 2 2 3 6 3 4 6 0 0 0 - 443 - 0 0 - - 0\n"+
				"7 b 1 dp_1 192.0.2.7 2 0 1 1 3 6 3 4 6 0 0 0 - 443 - 0 0 - - 0\n"+
				"7 b 1 dp_2 - 0 32 1 1 3 6 3 4 6 0 0 0 - 443 - 0 0 - - 0\n"+
				"7 b 1 dp_3 - 0 32 1 1 3 6 3 4 6 0 0 0 - 443 - 0 0 - - 0\n", "", 0),
			"nf_be_b": serversStateHeader +
				"7 b 1 pool_1 192.0.2.9 2 0 3 3 3 6 3 4 6 0 0 0 - 443 - 0 0 - - 0\n" +
				"7 b 1 pool_2 192.0.2.10 2 0 3 3 3 6 3 4 6 0 0 0 - 443 - 0 0 - - 0\n",
		},
		stat: "# pxname,svname,qcur,qmax,scur,smax,slim,stot,bin,bout,dreq,dresp,ereq,econ,eresp,wretr,wredis,status,weight,act,bck,chkfail,chkdown,lastchg,downtime,qlimit,pid,iid,sid,throttle,lbtot,tracked,type,rate,rate_lim,rate_max,check_status,check_code,check_duration\n" +
			"nf_be_a,s1,0,0,0,0,,0,0,0,,0,,0,0,0,0,UP,1,1,0,0,0,1,0,,1,1,1,,0,,2,0,,0,L4OK,,10\n" +
			"nf_be_a,s2,0,0,0,0,,0,0,0,,0,,0,0,0,0,UP,2,1,0,0,0,1,0,,1,1,2,,0,,2,0,,0,L4OK,,10\n" +
			"nf_be_a,dp_1,0,0,0,0,,0,0,0,,0,,0,0,0,0,UP,1,1,0,0,0,1,0,,1,1,3,,0,,2,0,,0,L4OK,,10\n",
	}
	controller := &WeightController{Runtime: runtime, Manager: &ConfigManager{ManagedConfig: path}, Mode: WeightsModeApply}
	sent, err := controller.Reconcile(context.Background())
	require.NoError(t, err)
	// nf_be_a: max base 10 (dp_1 ip weight) => K = 25. s1: 10ms, s2: 10ms x 1.5 = 15ms
	// (> 12ms tolerance) => 2*25*10/15 = 33; dp_1 = 10*25 = 250; dp_2/3 unresolved: untouched.
	assert.ElementsMatch(t, []WeightChange{
		{Backend: "nf_be_a", Server: "dp_1", Weight: 250},
		{Backend: "nf_be_a", Server: "s1", Weight: 25},
		{Backend: "nf_be_a", Server: "s2", Weight: 33},
		{Backend: "nf_be_b", Server: "pool_1", Weight: 40},
	}, sent)
	again, err := controller.Reconcile(context.Background())
	require.NoError(t, err)
	assert.Empty(t, again, "second pass must not send anything")

	controller.Mode = WeightsModeOff
	off, err := controller.Reconcile(context.Background())
	require.NoError(t, err)
	assert.Empty(t, off)
}

// --- algo=leastconn ---------------------------------------------------------

func TestParseWeightAnnotationsLeastConn(t *testing.T) {
	backends, err := ParseWeightAnnotations([]byte("# nf-weights backend=be algo=leastconn tolerance=0.25 tolerance_ms=0\n# nf-weight server=s1 base=2 cost=1.50\n"))
	require.NoError(t, err)
	require.Len(t, backends, 1)
	assert.Equal(t, "leastconn", backends[0].Algo)
	assert.Equal(t, 0.25, backends[0].Tolerance)
	assert.Equal(t, 1.5, backends[0].Units[0].Cost)
}

func leastConnUnits(backend WeightBackend) []runtimeServerUnit {
	return expandUnits(backend, nil)
}

func TestLeastConnWeightsCostAndTolerance(t *testing.T) {
	backend := WeightBackend{Backend: "be", Algo: "leastconn", Tolerance: 0.2, Units: []WeightUnit{
		{Server: "s1", Base: 1},
		{Server: "s2", Base: 1, Cost: 2},
		{Server: "s3", Base: 2},
	}}
	state := map[string]RuntimeServerState{"s1": up("192.0.2.1", 1), "s2": up("192.0.2.2", 1), "s3": up("192.0.2.3", 1)}
	// share base/cost: s1 1, s2 0.5, s3 2 => K = min(100, 128) = 100.
	// Idle: loads (0+1)*cost/base = s1 1, s2 2, s3 0.5. best 0.5: only s3
	// is within 20%; s1/s2 are paused at 1.
	desired, equal := LeastConnWeights(backend, leastConnUnits(backend), state, map[string]uint64{}, nil)
	assert.Equal(t, map[string]int{"s1": 1, "s2": 1, "s3": 200}, desired)
	assert.Equal(t, map[string]bool{"s3": true}, equal)

	// s3 with 1 conn: load 1 = s1 (1) => s1 and s3 equal, s2 (2) still out.
	desired, equal = LeastConnWeights(backend, leastConnUnits(backend), state, map[string]uint64{"s3": 1}, equal)
	assert.Equal(t, map[string]int{"s1": 100, "s2": 1, "s3": 200}, desired)
	assert.Equal(t, map[string]bool{"s1": true, "s3": true}, equal)

	// Cost honoured: s2 (cost 2) with 0 conns has load 2; s1 with 1 conn
	// has load 2 and s3 with 3 conns load 2 => all equal, shared by
	// base/cost.
	desired, _ = LeastConnWeights(backend, leastConnUnits(backend), state, map[string]uint64{"s1": 1, "s3": 3}, nil)
	assert.Equal(t, map[string]int{"s1": 100, "s2": 50, "s3": 200}, desired)
}

func TestLeastConnWeightsHysteresis(t *testing.T) {
	backend := WeightBackend{Backend: "be", Algo: "leastconn", Tolerance: 0.2, Units: []WeightUnit{
		{Server: "a", Base: 1}, {Server: "b", Base: 1},
	}}
	state := map[string]RuntimeServerState{"a": up("192.0.2.1", 1), "b": up("192.0.2.2", 1)}
	// a 9 conns (load 10), b 10 conns (load 11): 10% apart, both equal.
	_, equal := LeastConnWeights(backend, leastConnUnits(backend), state, map[string]uint64{"a": 9, "b": 10}, nil)
	assert.True(t, equal["b"])
	// b grows to 12 (load 13 = 30% above a): outside 20% but inside the
	// hysteresis band 20% x 1.5 = 30% => stays in the group.
	desired, equal := LeastConnWeights(backend, leastConnUnits(backend), state, map[string]uint64{"a": 9, "b": 12}, equal)
	assert.True(t, equal["b"])
	assert.Equal(t, 100, desired["b"])
	// Without previous membership the same loads leave b out.
	desired, equal2 := LeastConnWeights(backend, leastConnUnits(backend), state, map[string]uint64{"a": 9, "b": 12}, nil)
	assert.False(t, equal2["b"])
	assert.Equal(t, 1, desired["b"])
	// Beyond the hysteresis band b drops out.
	_, equal = LeastConnWeights(backend, leastConnUnits(backend), state, map[string]uint64{"a": 9, "b": 14}, equal)
	assert.False(t, equal["b"])
}

func TestLeastConnWeightsZeroToleranceAndDownServers(t *testing.T) {
	backend := WeightBackend{Backend: "be", Algo: "leastconn", Units: []WeightUnit{
		{Server: "a", Base: 1}, {Server: "b", Base: 1}, {Server: "c", Base: 1},
	}}
	down := up("192.0.2.3", 1)
	down.OpState = 0
	state := map[string]RuntimeServerState{"a": up("192.0.2.1", 1), "b": up("192.0.2.2", 1), "c": down}
	// Tolerance 0: only exact ties share; c is down and keeps its share
	// weight (not compared, even with 0 connections).
	desired, equal := LeastConnWeights(backend, leastConnUnits(backend), state, map[string]uint64{"a": 2, "b": 3}, nil)
	assert.Equal(t, map[string]int{"a": 100, "b": 1, "c": 100}, desired)
	assert.Equal(t, map[string]bool{"a": true}, equal)
}

func TestLeastConnPerIPCostTemplate(t *testing.T) {
	backend := WeightBackend{Backend: "be", Algo: "leastconn", Tolerance: 0.1, Units: []WeightUnit{
		{Template: "dp_", Slots: 2, Base: 1, IPCosts: map[string]float64{"192.0.2.2": 3}},
	}}
	state := map[string]RuntimeServerState{"dp_1": up("192.0.2.1", 1), "dp_2": up("192.0.2.2", 1)}
	units := expandUnits(backend, state)
	// dp_2 costs 3: with 2 conns on dp_1 (load 3) and 0 on dp_2 (load 3)
	// they are equal and share 3:1.
	desired, _ := LeastConnWeights(backend, units, state, map[string]uint64{"dp_1": 2}, nil)
	assert.Equal(t, map[string]int{"dp_1": 100, "dp_2": 33}, desired)
}

func TestWeightControllerLeastConnIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodeflow.cfg")
	config := "backend nf_be_a\n    # nf-weights backend=nf_be_a algo=leastconn tolerance=0.20 tolerance_ms=0\n    # nf-weight server=s1 base=1 cost=0.00\n    # nf-weight server=s2 base=1 cost=2.00\n"
	require.NoError(t, os.WriteFile(path, []byte(config), 0o600))
	header := "# pxname,svname,qcur,qmax,scur,smax,slim,stot,bin,bout,dreq,dresp,ereq,econ,eresp,wretr,wredis,status,weight,act,bck,chkfail,chkdown,lastchg,downtime,qlimit,pid,iid,sid,throttle,lbtot,tracked,type,rate,rate_lim,rate_max,check_status,check_code,check_duration\n"
	stat := func(s1, s2 int) string {
		return header +
			"nf_be_a,s1,0,0," + itoa(s1) + ",0,,0,0,0,,0,,0,0,0,0,UP,1,1,0,0,0,1,0,,1,1,1,,0,,2,0,,0,L4OK,,10\n" +
			"nf_be_a,s2,0,0," + itoa(s2) + ",0,,0,0,0,,0,,0,0,0,0,UP,1,1,0,0,0,1,0,,1,1,2,,0,,2,0,,0,L4OK,,10\n"
	}
	runtime := &fakeWeightRuntime{
		states: map[string]string{"nf_be_a": serversStateHeader +
			"7 b 1 s1 192.0.2.1 2 0 1 1 3 6 3 4 6 0 0 0 - 443 - 0 0 - - 0\n" +
			"7 b 1 s2 192.0.2.2 2 0 1 1 3 6 3 4 6 0 0 0 - 443 - 0 0 - - 0\n"},
		stat: stat(0, 0),
	}
	controller := &WeightController{Runtime: runtime, Manager: &ConfigManager{ManagedConfig: path}, Mode: WeightsModeApply}
	// Idle: s1 load 1, s2 load 2 => s1 full (share 1 => 100), s2 paused.
	sent, err := controller.Reconcile(context.Background())
	require.NoError(t, err)
	assert.ElementsMatch(t, []WeightChange{{Backend: "nf_be_a", Server: "s1", Weight: 100}}, sent)
	again, err := controller.Reconcile(context.Background())
	require.NoError(t, err)
	assert.Empty(t, again)
	// s1 has 1 connection: load 2 = s2 => both equal, s2 gets share 50.
	runtime.stat = stat(1, 0)
	sent, err = controller.Reconcile(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []WeightChange{{Backend: "nf_be_a", Server: "s2", Weight: 50}}, sent)
}
