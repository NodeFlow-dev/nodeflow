package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Runtime weight controller.
//
// The Panel renderer annotates backends whose server weights must be managed
// at runtime (balance_algorithm=leastping, leastconn with a tolerance, or
// DNS-pool ip_weights):
//
//	# nf-weights backend=<be> algo=<leastping|leastconn|static> tolerance=<0.20> tolerance_ms=<n>
//	# nf-weight server=<name> base=<w> cost=<c>
//	# nf-weight template=<prefix> slots=<n> base=<w> cost=<c> ips=<ip>=<w>,<ip>=<w> ipcosts=<ip>=<c>,<ip>=<c>
//
// ips= and ipcosts= are optional. ipcosts= (leastping/leastconn) replaces the
// template cost for the slot currently holding that address; Agents that
// predate it ignore the unknown key and use the template cost.
// algo=leastconn needs Agent 1.1.1: a 1.1.0
// Agent rejects the unknown algo, so the Panel gates it by Agent version.
//
// Every pass reads the HAProxy runtime state of each annotated backend,
// computes the desired user weight of every runtime server and sends
// "set server <be>/<srv> weight <w>" only for servers whose current user
// weight differs. A reload restores the configured weights; the next pass
// reapplies the desired ones. Passes run under the ConfigManager lock so they
// never race a reload.

const (
	WeightsModeApply = "apply"
	WeightsModeOff   = "off"

	weightsBackendAnnotation = "# nf-weights "
	weightsServerAnnotation  = "# nf-weight "
	maxRuntimeWeight         = 256
	maxWeightScale           = 100
	maxWeightTemplateSlots   = 256
	// leastpingDeadbandPercent suppresses weight churn caused by small
	// health-check latency jitter: a leastping weight is only rewritten when
	// it moves by more than this share of the desired value.
	leastpingDeadbandPercent = 10
	// leastconnHysteresis widens the tolerance band a server must leave
	// before it drops out of the "equally loaded" group it already belongs
	// to (band = tolerance x (1 + leastconnHysteresis)), so a server hovering
	// at the edge does not flap between full and reduced weight every pass.
	leastconnHysteresis = 0.5
)

// WeightBackend is one annotated backend.
type WeightBackend struct {
	Backend     string
	Algo        string // leastping | leastconn | static
	Tolerance   float64
	ToleranceMS float64
	Units       []WeightUnit
}

// WeightUnit is one annotated server or server-template.
type WeightUnit struct {
	Server   string // static server name ("" for a template)
	Template string // template prefix, slots are <Template><1..Slots>
	Slots    int
	Base     int
	Cost     float64
	IPs      map[string]int     // canonical IP -> weight (templates only)
	IPCosts  map[string]float64 // canonical IP -> leastping/leastconn cost (templates only)
}

// ParseWeightAnnotations extracts the runtime weight annotations of a
// rendered HAProxy configuration.
func ParseWeightAnnotations(config []byte) ([]WeightBackend, error) {
	var out []WeightBackend
	scanner := bufio.NewScanner(bytes.NewReader(config))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, weightsBackendAnnotation):
			fields, err := annotationFields(strings.TrimPrefix(line, weightsBackendAnnotation))
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNo, err)
			}
			backend := WeightBackend{Backend: fields["backend"], Algo: fields["algo"]}
			if !validRuntimeObjectName(backend.Backend) {
				return nil, fmt.Errorf("line %d: nf-weights requires a valid backend", lineNo)
			}
			if backend.Algo != "leastping" && backend.Algo != "leastconn" && backend.Algo != "static" {
				return nil, fmt.Errorf("line %d: nf-weights algo must be leastping, leastconn or static", lineNo)
			}
			if backend.Tolerance, err = annotationFloat(fields, "tolerance", 0, 1); err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNo, err)
			}
			if backend.ToleranceMS, err = annotationFloat(fields, "tolerance_ms", 0, 1000); err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNo, err)
			}
			out = append(out, backend)
		case strings.HasPrefix(line, weightsServerAnnotation):
			if len(out) == 0 {
				return nil, fmt.Errorf("line %d: nf-weight before nf-weights", lineNo)
			}
			unit, err := parseWeightUnit(strings.TrimPrefix(line, weightsServerAnnotation))
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNo, err)
			}
			last := &out[len(out)-1]
			last.Units = append(last.Units, unit)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func parseWeightUnit(text string) (WeightUnit, error) {
	fields, err := annotationFields(text)
	if err != nil {
		return WeightUnit{}, err
	}
	unit := WeightUnit{Server: fields["server"], Template: fields["template"]}
	switch {
	case unit.Server != "" && unit.Template == "":
		if !validRuntimeObjectName(unit.Server) {
			return WeightUnit{}, errors.New("nf-weight server name is invalid")
		}
	case unit.Template != "" && unit.Server == "":
		if !validRuntimeObjectName(unit.Template) {
			return WeightUnit{}, errors.New("nf-weight template prefix is invalid")
		}
		slots, convErr := strconv.Atoi(fields["slots"])
		if convErr != nil || slots < 1 || slots > maxWeightTemplateSlots {
			return WeightUnit{}, errors.New("nf-weight template requires slots 1..256")
		}
		unit.Slots = slots
	default:
		return WeightUnit{}, errors.New("nf-weight requires exactly one of server or template")
	}
	base, convErr := strconv.Atoi(fields["base"])
	if convErr != nil || base < 1 || base > maxRuntimeWeight {
		return WeightUnit{}, errors.New("nf-weight base must be 1..256")
	}
	unit.Base = base
	if unit.Cost, err = annotationFloat(fields, "cost", 0, 100); err != nil {
		return WeightUnit{}, err
	}
	if raw := fields["ips"]; raw != "" {
		if unit.Template == "" {
			return WeightUnit{}, errors.New("nf-weight ips is only valid for templates")
		}
		unit.IPs = make(map[string]int)
		for _, item := range strings.Split(raw, ",") {
			sep := strings.LastIndexByte(item, '=')
			if sep <= 0 {
				return WeightUnit{}, fmt.Errorf("nf-weight ips entry %q is malformed", item)
			}
			ip := net.ParseIP(item[:sep])
			weight, convErr := strconv.Atoi(item[sep+1:])
			if ip == nil || convErr != nil || weight < 1 || weight > maxRuntimeWeight {
				return WeightUnit{}, fmt.Errorf("nf-weight ips entry %q is malformed", item)
			}
			unit.IPs[ip.String()] = weight
		}
	}
	if raw := fields["ipcosts"]; raw != "" {
		if unit.Template == "" {
			return WeightUnit{}, errors.New("nf-weight ipcosts is only valid for templates")
		}
		unit.IPCosts = make(map[string]float64)
		for _, item := range strings.Split(raw, ",") {
			sep := strings.LastIndexByte(item, '=')
			if sep <= 0 {
				return WeightUnit{}, fmt.Errorf("nf-weight ipcosts entry %q is malformed", item)
			}
			ip := net.ParseIP(item[:sep])
			cost, convErr := strconv.ParseFloat(item[sep+1:], 64)
			if ip == nil || convErr != nil || math.IsNaN(cost) || cost <= 0 || cost > 100 {
				return WeightUnit{}, fmt.Errorf("nf-weight ipcosts entry %q is malformed", item)
			}
			unit.IPCosts[ip.String()] = cost
		}
	}
	return unit, nil
}

// annotationFields splits "k=v k=v" (first '=' separates key and value).
func annotationFields(text string) (map[string]string, error) {
	fields := make(map[string]string)
	for _, token := range strings.Fields(text) {
		key, value, ok := strings.Cut(token, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("malformed annotation field %q", token)
		}
		if _, dup := fields[key]; dup {
			return nil, fmt.Errorf("duplicate annotation field %q", key)
		}
		fields[key] = value
	}
	return fields, nil
}

func annotationFloat(fields map[string]string, key string, minimum, maximum float64) (float64, error) {
	raw, ok := fields[key]
	if !ok || raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || value < minimum || value > maximum {
		return 0, fmt.Errorf("annotation %s must be between %g and %g", key, minimum, maximum)
	}
	return value, nil
}

// RuntimeServerState is one row of "show servers state <backend>".
type RuntimeServerState struct {
	Name    string
	Addr    string // canonical IP, "" when unresolved
	OpState int    // 2 = running
	Admin   int
	UWeight int
}

// parseShowServersState parses the "show servers state" response (format
// version line, "# " header, space-separated rows).
func parseShowServersState(response []byte) (map[string]RuntimeServerState, error) {
	lines := strings.Split(strings.TrimSpace(string(response)), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "1" {
		return nil, errors.New("unsupported show servers state format")
	}
	index := map[string]int{}
	out := make(map[string]RuntimeServerState)
	for _, raw := range lines[1:] {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			for i, name := range strings.Fields(strings.TrimPrefix(line, "#")) {
				index[name] = i
			}
			continue
		}
		if len(index) == 0 {
			return nil, errors.New("show servers state row before header")
		}
		fields := strings.Fields(line)
		get := func(name string) (string, bool) {
			i, ok := index[name]
			if !ok || i >= len(fields) {
				return "", false
			}
			return fields[i], true
		}
		name, ok := get("srv_name")
		if !ok {
			return nil, errors.New("show servers state missing srv_name")
		}
		state := RuntimeServerState{Name: name}
		if addr, ok := get("srv_addr"); ok {
			if ip := net.ParseIP(addr); ip != nil {
				state.Addr = ip.String()
			}
		}
		for key, target := range map[string]*int{"srv_op_state": &state.OpState, "srv_admin_state": &state.Admin, "srv_uweight": &state.UWeight} {
			value, ok := get(key)
			if !ok {
				return nil, fmt.Errorf("show servers state missing %s", key)
			}
			n, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("show servers state %s is not a number", key)
			}
			*target = n
		}
		out[name] = state
	}
	return out, nil
}

// ServerLatency is the health-check view of one runtime server.
type ServerLatency struct {
	CheckOK    bool
	DurationMS uint64
}

// WeightChange is one runtime command to send.
type WeightChange struct {
	Backend string
	Server  string
	Weight  int
}

type runtimeServerUnit struct {
	name string
	base int
	cost float64
}

// expandUnits lists the runtime servers of a backend with their base weight
// and cost (ips/ipcosts resolved against the current slot address).
func expandUnits(backend WeightBackend, state map[string]RuntimeServerState) []runtimeServerUnit {
	var out []runtimeServerUnit
	for _, unit := range backend.Units {
		cost := unit.Cost
		if cost <= 0 {
			cost = 1
		}
		if unit.Server != "" {
			out = append(out, runtimeServerUnit{name: unit.Server, base: unit.Base, cost: cost})
			continue
		}
		for slot := 1; slot <= unit.Slots; slot++ {
			name := unit.Template + strconv.Itoa(slot)
			srv, ok := state[name]
			if !ok || srv.Addr == "" {
				// Unassigned slot (MAINT resolution): nothing to weigh. When
				// DNS assigns an address the next pass picks it up.
				continue
			}
			base := unit.Base
			if weight, ok := unit.IPs[srv.Addr]; ok {
				base = weight
			}
			slotCost := cost
			if ipCost, ok := unit.IPCosts[srv.Addr]; ok {
				slotCost = ipCost
			}
			out = append(out, runtimeServerUnit{name: name, base: base, cost: slotCost})
		}
	}
	return out
}

// DesiredWeights computes the desired user weight of every runtime server of
// an annotated backend.
//
// static: the base weight (ip_weights override the template base).
// leastping: effective latency = check_duration (ms, at least 1) x cost,
// where a template slot uses its address's ipcosts entry when present.
// Servers whose effective latency is within tolerance of the fastest
// (<= best x (1+tolerance) or <= best + tolerance_ms) keep their full scaled
// base weight; slower ones get it scaled by best/latency. Base weights are
// first multiplied by K = min(100, 256/max base) so the ratios survive
// integer rounding. Servers that are down or have no completed check keep
// their scaled base weight (HAProxy does not select them anyway).
func DesiredWeights(backend WeightBackend, state map[string]RuntimeServerState, latency map[string]ServerLatency) map[string]int {
	units := expandUnits(backend, state)
	desired := make(map[string]int, len(units))
	if backend.Algo == "leastconn" {
		desired, _ = LeastConnWeights(backend, units, state, nil, nil)
		return desired
	}
	if backend.Algo != "leastping" {
		for _, unit := range units {
			desired[unit.name] = clampWeight(float64(unit.base))
		}
		return desired
	}
	maxBase := 1
	for _, unit := range units {
		if unit.base > maxBase {
			maxBase = unit.base
		}
	}
	scale := maxRuntimeWeight / maxBase
	if scale > maxWeightScale {
		scale = maxWeightScale
	}
	if scale < 1 {
		scale = 1
	}
	effective := make(map[string]float64, len(units))
	best := math.Inf(1)
	for _, unit := range units {
		srv, ok := state[unit.name]
		lat, measured := latency[unit.name]
		if !ok || srv.OpState != 2 || !measured || !lat.CheckOK {
			continue
		}
		duration := float64(lat.DurationMS)
		if duration < 1 {
			duration = 1
		}
		eff := duration * unit.cost
		effective[unit.name] = eff
		if eff < best {
			best = eff
		}
	}
	for _, unit := range units {
		scaled := float64(unit.base * scale)
		eff, ok := effective[unit.name]
		if !ok || eff <= best*(1+backend.Tolerance) || eff <= best+backend.ToleranceMS {
			desired[unit.name] = clampWeight(scaled)
			continue
		}
		desired[unit.name] = clampWeight(scaled * best / eff)
	}
	return desired
}

// LeastConnWeights computes the desired user weights of an algo=leastconn
// backend from the live connection count of every server, and returns the
// new "equally loaded" group. The Panel renders such a backend as weighted
// round-robin, so the weights set here decide where new clients go.
//
// Each server's share weight is base/cost, scaled by one factor
// K = min(100, 256/max(base/cost)) so the ratios survive integer rounding.
// Its effective load is (scur+1) x cost / base (+1 keeps an idle backend
// comparable). Servers whose load is within tolerance of the least loaded
// one (load <= best x (1+tolerance)) are equally loaded: they keep their full
// share weight and split new clients by weight and cost. Every other
// compared server is paused at weight 1 (not 0, so it still serves if the
// whole group fails between passes) until the group catches up; this is the
// "least connections" part. A server already in the group stays there until
// its load exceeds best x (1 + tolerance x (1+leastconnHysteresis)), so a
// server hovering at the edge does not flap. Down and unassigned servers are
// not compared and keep their share weight (HAProxy does not pick them).
// Backup servers never occur: pool mode, the only mode with an algorithm,
// has no backups.
func LeastConnWeights(backend WeightBackend, units []runtimeServerUnit, state map[string]RuntimeServerState, conns map[string]uint64, previous map[string]bool) (map[string]int, map[string]bool) {
	desired := make(map[string]int, len(units))
	equal := make(map[string]bool, len(units))
	share := func(unit runtimeServerUnit) float64 {
		return float64(unit.base) / unit.cost
	}
	maxShare := 0.0
	for _, unit := range units {
		maxShare = math.Max(maxShare, share(unit))
	}
	if maxShare <= 0 {
		return desired, equal
	}
	scale := math.Min(maxWeightScale, maxRuntimeWeight/maxShare)
	loads := make(map[string]float64, len(units))
	best := math.Inf(1)
	for _, unit := range units {
		srv, ok := state[unit.name]
		if !ok || srv.OpState != 2 {
			continue
		}
		load := float64(conns[unit.name]+1) * unit.cost / float64(unit.base)
		loads[unit.name] = load
		best = math.Min(best, load)
	}
	for _, unit := range units {
		full := clampWeight(share(unit) * scale)
		load, compared := loads[unit.name]
		if !compared {
			desired[unit.name] = full
			continue
		}
		band := backend.Tolerance
		if previous[unit.name] {
			band *= 1 + leastconnHysteresis
		}
		// A tiny epsilon keeps float rounding from splitting exact ties.
		if load <= best*(1+band)+1e-9 {
			equal[unit.name] = true
			desired[unit.name] = full
			continue
		}
		desired[unit.name] = 1
	}
	return desired, equal
}

func clampWeight(value float64) int {
	weight := int(math.Round(value))
	if weight < 1 {
		return 1
	}
	if weight > maxRuntimeWeight {
		return maxRuntimeWeight
	}
	return weight
}

// PlanWeightChanges returns only the commands needed to reach the desired
// weights. For leastping a change within the jitter deadband is skipped
// (leastconn weights only switch between two fixed values per server and
// get their stability from the group hysteresis instead).
func PlanWeightChanges(backend WeightBackend, state map[string]RuntimeServerState, desired map[string]int) []WeightChange {
	names := make([]string, 0, len(desired))
	for name := range desired {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []WeightChange
	for _, name := range names {
		srv, ok := state[name]
		if !ok {
			continue // not in this HAProxy generation (yet)
		}
		want := desired[name]
		if srv.UWeight == want {
			continue
		}
		if backend.Algo == "leastping" {
			diff := srv.UWeight - want
			if diff < 0 {
				diff = -diff
			}
			if diff*100 <= want*leastpingDeadbandPercent && srv.UWeight > 1 && want > 1 {
				continue
			}
		}
		out = append(out, WeightChange{Backend: backend.Backend, Server: name, Weight: want})
	}
	return out
}

// weightRuntime is the HAProxy runtime surface the controller needs.
type weightRuntime interface {
	ShowServersState(ctx context.Context, backend string) ([]byte, error)
	ShowStat(ctx context.Context) ([]byte, error)
	SetServerWeight(ctx context.Context, backend, server string, weight int) error
}

// latencyFromShowStat extracts health-check results per backend/server.
func latencyFromShowStat(response []byte) (map[string]map[string]ServerLatency, error) {
	stats, err := parseHAProxyShowStat(response)
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[string]ServerLatency, len(stats.Servers))
	for backend, servers := range stats.Servers {
		row := make(map[string]ServerLatency, len(servers))
		for name, srv := range servers {
			status := strings.TrimSpace(strings.TrimPrefix(srv.CheckStatus, "* "))
			row[name] = ServerLatency{
				CheckOK:    strings.HasSuffix(status, "OK") && strings.HasPrefix(status, "L"),
				DurationMS: srv.CheckDurationMS,
			}
		}
		out[backend] = row
	}
	return out, nil
}

// connectionsFromShowStat extracts the current session count (scur) per
// backend/server.
func connectionsFromShowStat(response []byte) (map[string]map[string]uint64, error) {
	stats, err := parseHAProxyShowStat(response)
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[string]uint64, len(stats.Servers))
	for backend, servers := range stats.Servers {
		row := make(map[string]uint64, len(servers))
		for name, srv := range servers {
			row[name] = srv.SessionsCurrent
		}
		out[backend] = row
	}
	return out, nil
}

// WeightController applies the runtime weights of the active managed config.
type WeightController struct {
	Runtime weightRuntime
	Manager *ConfigManager
	Mode    string
	Logger  *log.Logger

	mu       sync.Mutex
	lastSum  [32]byte
	backends []WeightBackend
	parsed   bool
	// equal is the leastconn "equally loaded" group of the previous pass per
	// backend (hysteresis input).
	equal map[string]map[string]bool
}

// NewWeightControllerFromEnv reads NODE_AGENT_WEIGHTS (apply|off, default apply).
func NewWeightControllerFromEnv(runtime weightRuntime, manager *ConfigManager) *WeightController {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("NODE_AGENT_WEIGHTS")))
	switch mode {
	case "", "on", "auto", "apply":
		mode = WeightsModeApply
	default:
		mode = WeightsModeOff
	}
	return &WeightController{Runtime: runtime, Manager: manager, Mode: mode}
}

// Reconcile runs one pass and returns the commands it sent.
func (c *WeightController) Reconcile(ctx context.Context) ([]WeightChange, error) {
	if c == nil || c.Runtime == nil || c.Manager == nil {
		return nil, errors.New("weight controller is not configured")
	}
	if c.Mode == WeightsModeOff {
		return nil, nil
	}
	config, _, err := c.Manager.managedConfigSnapshot()
	if err != nil {
		return nil, fmt.Errorf("read managed config: %w", err)
	}
	backends, err := c.annotations(config)
	if err != nil || len(backends) == 0 {
		return nil, err
	}
	var sent []WeightChange
	err = c.Manager.RunSerialized(func() error {
		// The config may have changed between the snapshot and the lock.
		current, readErr := os.ReadFile(c.Manager.ManagedConfig)
		if readErr != nil || sha256.Sum256(current) != sha256.Sum256(config) {
			return nil
		}
		var latency map[string]map[string]ServerLatency
		var conns map[string]map[string]uint64
		var stat []byte
		readStat := func() error {
			if stat != nil {
				return nil
			}
			var statErr error
			stat, statErr = c.Runtime.ShowStat(ctx)
			return statErr
		}
		for _, backend := range backends {
			if backend.Algo == "leastping" && latency == nil {
				if statErr := readStat(); statErr != nil {
					return statErr
				}
				var statErr error
				if latency, statErr = latencyFromShowStat(stat); statErr != nil {
					return statErr
				}
			}
			if backend.Algo == "leastconn" && conns == nil {
				if statErr := readStat(); statErr != nil {
					return statErr
				}
				var statErr error
				if conns, statErr = connectionsFromShowStat(stat); statErr != nil {
					return statErr
				}
			}
			raw, stateErr := c.Runtime.ShowServersState(ctx, backend.Backend)
			if stateErr != nil {
				return stateErr
			}
			state, stateErr := parseShowServersState(raw)
			if stateErr != nil {
				return fmt.Errorf("backend %s: %w", backend.Backend, stateErr)
			}
			var desired map[string]int
			if backend.Algo == "leastconn" {
				var equal map[string]bool
				desired, equal = LeastConnWeights(backend, expandUnits(backend, state), state, conns[backend.Backend], c.previousEqual(backend.Backend))
				c.setEqual(backend.Backend, equal)
			} else {
				desired = DesiredWeights(backend, state, latency[backend.Backend])
			}
			changes := PlanWeightChanges(backend, state, desired)
			for _, change := range changes {
				if setErr := c.Runtime.SetServerWeight(ctx, change.Backend, change.Server, change.Weight); setErr != nil {
					return fmt.Errorf("set server %s/%s weight %d: %w", change.Backend, change.Server, change.Weight, setErr)
				}
				sent = append(sent, change)
			}
		}
		return nil
	})
	return sent, err
}

func (c *WeightController) previousEqual(backend string) map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.equal[backend]
}

func (c *WeightController) setEqual(backend string, equal map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.equal == nil {
		c.equal = make(map[string]map[string]bool)
	}
	c.equal[backend] = equal
}

func (c *WeightController) annotations(config []byte) ([]WeightBackend, error) {
	sum := sha256.Sum256(config)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.parsed && sum == c.lastSum {
		return c.backends, nil
	}
	backends, err := ParseWeightAnnotations(config)
	if err != nil {
		return nil, err
	}
	c.backends, c.lastSum, c.parsed = backends, sum, true
	return backends, nil
}

// Run reconciles periodically until ctx is cancelled. Errors are logged once
// per distinct message.
func (c *WeightController) Run(ctx context.Context, interval time.Duration) {
	if c == nil || c.Mode == WeightsModeOff {
		return
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	logger := c.Logger
	if logger == nil {
		logger = log.Default()
	}
	lastErr := ""
	tick := func() {
		runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		sent, err := c.Reconcile(runCtx)
		cancel()
		if err != nil {
			if msg := err.Error(); msg != lastErr && ctx.Err() == nil {
				logger.Printf("runtime weight reconciliation failed: %s", msg)
				lastErr = msg
			}
			return
		}
		if lastErr != "" {
			logger.Printf("runtime weight reconciliation recovered")
			lastErr = ""
		}
		if len(sent) > 0 {
			logger.Printf("runtime weights applied: %d change(s)", len(sent))
		}
	}
	tick()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
}
