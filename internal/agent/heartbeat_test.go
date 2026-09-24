package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRepeatedFailureLoggingSuppressesSpamAndReportsRecovery(t *testing.T) {
	var output bytes.Buffer
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)
	sender := HeartbeatSender{FailureLogPeriod: time.Minute, Now: func() time.Time { return now }}
	logger := log.New(&output, "", 0)
	failure := errors.New("panel unavailable")

	sender.logFailure(logger, "heartbeat", failure)
	sender.logFailure(logger, "heartbeat", failure)
	sender.logFailure(logger, "heartbeat", failure)
	if got := strings.Count(output.String(), "heartbeat failed"); got != 1 {
		t.Fatalf("first window logged %d failures: %s", got, output.String())
	}

	now = now.Add(time.Minute)
	sender.logFailure(logger, "heartbeat", failure)
	if !strings.Contains(output.String(), "suppressed 2 repeats") {
		t.Fatalf("suppression summary missing: %s", output.String())
	}
	sender.logFailure(logger, "heartbeat", failure)
	sender.clearFailure(logger, "heartbeat")
	if !strings.Contains(output.String(), "recovered after suppressing 1 repeated errors") {
		t.Fatalf("recovery summary missing: %s", output.String())
	}
}

type heartbeatUpdateProcessor struct {
	called chan UpdateManifest
}

func (p *heartbeatUpdateProcessor) Process(_ context.Context, manifest UpdateManifest) (UpdateVerification, error) {
	p.called <- manifest
	return UpdateVerification{Status: UpdateVerificationVerified, Sequence: manifest.Sequence}, nil
}

func (*heartbeatUpdateProcessor) Snapshot() *UpdateVerification { return nil }

type fakeHAProxyRuntimeCollector struct {
	stats HAProxyRuntimeStats
	err   error
}

func (c fakeHAProxyRuntimeCollector) Collect(context.Context) (HAProxyRuntimeStats, error) {
	return c.stats, c.err
}

func TestHeartbeatSend(t *testing.T) {
	var got Heartbeat
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/v1/heartbeat" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	sender := HeartbeatSender{URL: server.URL, Token: "secret", Version: "1.2.3", Client: server.Client()}
	if err := sender.Send(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.Version != "1.2.3" || got.Status != "online" || !got.RoutesOK {
		t.Fatalf("heartbeat=%+v", got)
	}
	if !validCanonicalUUID(got.TrafficInstanceID) || got.TrafficInstanceStartedAt.IsZero() || got.TrafficSampleSeq != 1 {
		t.Fatalf("traffic order=%+v", got)
	}
	_, offset := got.TrafficInstanceStartedAt.Zone()
	if offset != 0 {
		t.Fatalf("traffic instance start is not UTC: %v", got.TrafficInstanceStartedAt)
	}
	first := got
	if err := sender.Send(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.TrafficInstanceID != first.TrafficInstanceID ||
		!got.TrafficInstanceStartedAt.Equal(first.TrafficInstanceStartedAt) || got.TrafficSampleSeq != 2 {
		t.Fatalf("traffic order did not advance: first=%+v second=%+v", first, got)
	}
}

func TestHeartbeatReportsHAProxyRestartGeneration(t *testing.T) {
	var got Heartbeat
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	runner := &serviceControlRunner{state: "active"}
	controller := &HAProxyServiceController{Runner: runner, Manager: &ConfigManager{}, ServiceName: "haproxy.service"}
	if err := controller.Reconcile(context.Background(), HAProxyServiceAssignment{Generation: 1, Enabled: true, RestartGeneration: 2}); err != nil {
		t.Fatal(err)
	}
	sender := HeartbeatSender{URL: server.URL, Token: "secret", Version: "1.2.3", Client: server.Client(), ServiceControl: controller}
	if err := sender.Send(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.HAProxyRestartGeneration != 2 {
		t.Fatalf("restart generation=%d", got.HAProxyRestartGeneration)
	}
}

func TestHeartbeatIncludesCachedProcessNames(t *testing.T) {
	var got Heartbeat
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	root := t.TempDir()
	writeProcessCmdline(t, root, "101", "/usr/sbin/haproxy")
	writeProcessCmdline(t, root, "102", "/usr/local/bin/nodeflow-node-agent")
	writeProcessCmdline(t, root, "103", "/usr/sbin/haproxy")
	sender := HeartbeatSender{
		URL:       server.URL,
		Token:     "secret",
		Version:   "test",
		Client:    server.Client(),
		Processes: &ProcessSampler{ProcRoot: root},
	}
	if err := sender.Send(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.Metrics.ProcessCount == nil || *got.Metrics.ProcessCount <= 0 {
		t.Fatalf("process count missing: %+v", got.Metrics.ProcessCount)
	}
	if !reflect.DeepEqual(got.Metrics.ProcessNames, []string{"haproxy", "nodeflow-node-agent"}) {
		t.Fatalf("process names=%v", got.Metrics.ProcessNames)
	}
}

func TestHeartbeatIncludesHAProxyRuntimeStats(t *testing.T) {
	var got Heartbeat
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	runtimeStats := emptyHAProxyRuntimeStats()
	runtimeStats.ConnectionsCurrent = 4
	runtimeStats.ConnectionsTotal = 99
	sender := HeartbeatSender{
		URL:            server.URL,
		Token:          "secret",
		Version:        "test",
		Client:         server.Client(),
		HAProxyRuntime: fakeHAProxyRuntimeCollector{stats: runtimeStats},
	}
	if err := sender.Send(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !got.Metrics.HAProxyStatsAvailable || got.Metrics.HAProxyRuntime == nil {
		t.Fatalf("metrics=%+v", got.Metrics)
	}
	if got.Metrics.HAProxyRuntime.ConnectionsCurrent != 4 || got.Metrics.HAProxyRuntime.ConnectionsTotal != 99 {
		t.Fatalf("runtime=%+v", got.Metrics.HAProxyRuntime)
	}
}

func TestHeartbeatInvalidatesStaleQuotaCacheFromRuntimeStats(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	controller := &fakeQuotaController{}
	quota := &QuotaReconciler{Controller: controller, Manager: &ConfigManager{}}
	policy := quotaPolicy(quotaRouteA, quotaActionBlockNew, true, 10, int64Pointer(10))
	if err := quota.Reconcile(context.Background(), QuotaAssignment{Month: "2026-07", Policies: []QuotaBackendPolicy{policy}}); err != nil {
		t.Fatal(err)
	}
	runtimeStats := emptyHAProxyRuntimeStats()
	runtimeStats.Servers[policy.Backend] = map[string]HAProxyServerStats{policy.Server: {Status: "UP"}}
	sender := HeartbeatSender{
		URL: server.URL, Token: "secret", Version: "test", Client: server.Client(),
		HAProxyRuntime: fakeHAProxyRuntimeCollector{stats: runtimeStats}, Quota: quota,
	}
	if err := sender.Send(context.Background()); err != nil {
		t.Fatal(err)
	}
	if snapshot := quota.Snapshot(); len(snapshot) != 0 {
		t.Fatalf("stale quota state survived Runtime API observation: %+v", snapshot)
	}
}

func TestHeartbeatContinuesWhenHAProxyRuntimeUnavailable(t *testing.T) {
	var got Heartbeat
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	sender := HeartbeatSender{
		URL:            server.URL,
		Token:          "secret",
		Version:        "test",
		Client:         server.Client(),
		HAProxyRuntime: fakeHAProxyRuntimeCollector{err: errors.New("socket unavailable")},
	}
	if err := sender.Send(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.Metrics.HAProxyStatsAvailable || got.Metrics.HAProxyRuntime != nil {
		t.Fatalf("metrics=%+v", got.Metrics)
	}
	if got.RoutesOK {
		t.Fatal("routes_ok must be false when the HAProxy Runtime API is unavailable")
	}
	if got.Metrics.CPUCount == 0 || got.Metrics.MemoryTotal == 0 {
		t.Fatalf("system metrics were not sent: %+v", got.Metrics)
	}
}

func TestHeartbeatIncludesObservedConfigState(t *testing.T) {
	var got Heartbeat
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "haproxy.cfg")
	manager := &ConfigManager{ManagedConfig: path}
	if err := os.WriteFile(path, []byte("global\n"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.revisionPath(), []byte("7\n"), 0640); err != nil {
		t.Fatal(err)
	}
	sender := HeartbeatSender{
		URL: server.URL, Token: "secret", Version: "test", Client: server.Client(),
		Reconciler: &Reconciler{Manager: manager},
	}
	if err := sender.Send(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.ActualRevision == nil || *got.ActualRevision != 7 || len(got.ConfigSHA256) != 64 {
		t.Fatalf("observed heartbeat=%+v", got)
	}
}

func TestHeartbeatRunSendsImmediately(t *testing.T) {
	received := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case received <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sender := &HeartbeatSender{URL: server.URL, Token: "secret", Version: "test", Client: server.Client()}
	go sender.Run(ctx, time.Hour)
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("immediate heartbeat not received")
	}
}

func TestHeartbeatRejectsNon2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", http.StatusUnauthorized) }))
	defer server.Close()
	err := (&HeartbeatSender{URL: server.URL, Token: "secret", Version: "test", Client: server.Client()}).Send(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHeartbeatPullsAssignedRevision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"accepted","node_id":"node","assignment":{"revision":42,"config":"global\n","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`))
	}))
	defer server.Close()
	got, err := (&HeartbeatSender{URL: server.URL, Token: "secret", Version: "test", Client: server.Client()}).send(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Assignment == nil || got.Assignment.Revision != 42 || got.Assignment.Config != "global\n" {
		t.Fatalf("assignment=%+v", got)
	}
}

func TestHeartbeatPullsQuotaAssignment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"accepted","quota_assignment":{"month":"2026-07","policies":[{"route_id":"22222222-2222-4222-8222-222222222222","backend":"nf_be_22222222222242228222222222222222","server":"nf_srv_22222222222242228222222222222222","action":"block_new","block":true,"used_bytes":11,"limit_bytes":10}]}}`))
	}))
	defer server.Close()
	got, err := (&HeartbeatSender{URL: server.URL, Token: "secret", Version: "test", Client: server.Client()}).send(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.QuotaAssignment == nil || len(got.QuotaAssignment.Policies) != 1 || !got.QuotaAssignment.Policies[0].Block {
		t.Fatalf("quota assignment=%+v", got.QuotaAssignment)
	}
}

func TestHeartbeatRunReconcilesAssignment(t *testing.T) {
	applied := make(chan struct{}, 1)
	a := assignment(42, "global\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent/v1/heartbeat":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "accepted", "node_id": "node", "assignment": a})
		case "/agent/v1/config-report":
			var report ConfigReport
			_ = json.NewDecoder(r.Body).Decode(&report)
			if report.State == "applied" {
				select {
				case applied <- struct{}{}:
				default:
				}
			}
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	manager := &ConfigManager{Runner: &fakeRunner{}, ManagedConfig: filepath.Join(t.TempDir(), "nodeflow.cfg"), HAProxyBinary: "haproxy", ServiceName: "haproxy.service"}
	reconciler := &Reconciler{Manager: manager, Reporter: ConfigReporter{URL: server.URL, Token: "secret", Client: server.Client()}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sender := &HeartbeatSender{URL: server.URL, Token: "secret", Version: "test", Client: server.Client(), Reconciler: reconciler}
	go sender.Run(ctx, time.Hour)
	select {
	case <-applied:
	case <-time.After(time.Second):
		t.Fatal("assignment was not reconciled")
	}
}

func TestHeartbeatSuccessfulConfigApplySkipsStaleQuotaButContinuesFirewallAndUpdate(t *testing.T) {
	quotaController, firewall, updater, cancel := runHeartbeatAssignmentWorkflow(t, false, false)
	defer cancel()

	select {
	case manifest := <-updater.called:
		if manifest.Sequence != 9 {
			t.Fatalf("update sequence=%d", manifest.Sequence)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("update assignment was starved by config assignment")
	}
	if calls := quotaController.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("stale quota was reconciled after successful config apply: %+v", calls)
	}
	if status := firewall.Snapshot(); status == nil || status.EffectiveMode != FirewallModeOff {
		t.Fatalf("firewall assignment was not reconciled: %+v", status)
	}
}

func TestHeartbeatFailedConfigApplyStillReconcilesQuotaFirewallAndUpdate(t *testing.T) {
	quotaController, firewall, updater, cancel := runHeartbeatAssignmentWorkflow(t, true, false)
	defer cancel()

	select {
	case <-updater.called:
	case <-time.After(2 * time.Second):
		t.Fatal("update assignment was starved by failed config assignment")
	}
	calls := quotaController.snapshotCalls()
	if len(calls) != 1 || calls[0].block {
		t.Fatalf("quota assignment was not restored after failed config apply: %+v", calls)
	}
	if status := firewall.Snapshot(); status == nil || status.EffectiveMode != FirewallModeOff {
		t.Fatalf("firewall assignment was not reconciled: %+v", status)
	}
}

func TestHeartbeatAppliedConfigWithReportFailureStillSkipsStaleQuota(t *testing.T) {
	quotaController, _, updater, cancel := runHeartbeatAssignmentWorkflow(t, false, true)
	defer cancel()

	select {
	case <-updater.called:
	case <-time.After(2 * time.Second):
		t.Fatal("update assignment was starved after config report failure")
	}
	if calls := quotaController.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("stale quota was reconciled after HAProxy apply succeeded: %+v", calls)
	}
}

type firewallTransitionRunner struct {
	mu              sync.Mutex
	calls           [][]string
	ports           map[int]struct{}
	failFirstReload bool
	reloads         int
}

func (r *firewallTransitionRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string{name}, args...))
	if name == "systemctl" {
		r.reloads++
		if r.failFirstReload && r.reloads == 1 {
			return []byte("reload failed"), errors.New("reload failed")
		}
		return []byte("ok"), nil
	}
	if name != "ufw" {
		return []byte("ok"), nil
	}
	if reflect.DeepEqual(args, []string{"status", "numbered"}) {
		ports := r.sortedPortsLocked()
		var status strings.Builder
		status.WriteString("Status: active\n")
		for index, port := range ports {
			fmt.Fprintf(&status, "[ %d] %d/tcp ALLOW IN Anywhere # nodeflow\n", index+1, port)
		}
		return []byte(status.String()), nil
	}
	if len(args) == 4 && args[0] == "allow" && args[2] == "comment" && args[3] == firewallRuleComment {
		port, err := strconv.Atoi(strings.TrimSuffix(args[1], "/tcp"))
		if err != nil {
			return nil, err
		}
		r.ports[port] = struct{}{}
		return []byte("Rule added"), nil
	}
	if len(args) == 3 && args[0] == "--force" && args[1] == "delete" {
		number, err := strconv.Atoi(args[2])
		if err != nil {
			return nil, err
		}
		ports := r.sortedPortsLocked()
		if number < 1 || number > len(ports) {
			return nil, errors.New("unknown UFW rule")
		}
		delete(r.ports, ports[number-1])
		return []byte("Rule deleted"), nil
	}
	return nil, errors.New("unexpected UFW command")
}

func (r *firewallTransitionRunner) sortedPortsLocked() []int {
	ports := make([]int, 0, len(r.ports))
	for port := range r.ports {
		ports = append(ports, port)
	}
	sort.Ints(ports)
	return ports
}

func (r *firewallTransitionRunner) snapshot() ([][]string, []int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	calls := make([][]string, len(r.calls))
	for index := range r.calls {
		calls[index] = append([]string(nil), r.calls[index]...)
	}
	return calls, r.sortedPortsLocked()
}

func TestHeartbeatFirewallTransitionOrdersPreOpenApplyAndPrune(t *testing.T) {
	for _, test := range []struct {
		name            string
		failFirstReload bool
		activeComplete  bool
		wantPorts       []int
		wantDeletedPort string
	}{
		{name: "successful activation prunes old listener", activeComplete: true, wantPorts: []int{10065}, wantDeletedPort: "1"},
		{name: "failed activation restores active listener set", failFirstReload: true, activeComplete: true, wantPorts: []int{443}, wantDeletedPort: "2"},
		{name: "failed activation preserves unknown legacy listeners", failFirstReload: true, wantPorts: []int{443, 10065}},
	} {
		t.Run(test.name, func(t *testing.T) {
			configAssignment := assignment(42, "global\n")
			updater := &heartbeatUpdateProcessor{called: make(chan UpdateManifest, 1)}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/agent/v1/heartbeat":
					_ = json.NewEncoder(w).Encode(HeartbeatResponse{
						Assignment: &configAssignment,
						FirewallAssignment: &FirewallAssignment{
							Mode: FirewallModeApply, TCPPorts: []int{443},
							DesiredTCPPorts: []int{10065}, Transition: true, ActivePlanComplete: test.activeComplete,
						},
						UpdateAssignment: &UpdateManifest{Sequence: 10},
					})
				case "/agent/v1/config-report":
					w.WriteHeader(http.StatusAccepted)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			runner := &firewallTransitionRunner{
				ports: map[int]struct{}{443: {}}, failFirstReload: test.failFirstReload,
			}
			manager := &ConfigManager{
				Runner: runner, ManagedConfig: filepath.Join(t.TempDir(), "nodeflow.cfg"),
				HAProxyBinary: "haproxy", ServiceName: "haproxy.service",
			}
			sender := &HeartbeatSender{
				URL: server.URL, Token: "secret", Version: "test", Client: server.Client(),
				Reconciler: &Reconciler{Manager: manager, Reporter: ConfigReporter{URL: server.URL, Token: "secret", Client: server.Client()}},
				Firewall:   &FirewallReconciler{Config: FirewallConfig{Mode: FirewallModeApply}, Runner: runner},
				Updater:    updater, Logger: log.New(io.Discard, "", 0),
			}
			ctx, cancel := context.WithCancel(context.Background())
			go sender.Run(ctx, time.Hour)
			select {
			case <-updater.called:
			case <-time.After(2 * time.Second):
				cancel()
				t.Fatal("firewall transition did not finish")
			}
			cancel()

			calls, ports := runner.snapshot()
			if !reflect.DeepEqual(ports, test.wantPorts) {
				t.Fatalf("managed ports=%v want=%v calls=%v", ports, test.wantPorts, calls)
			}
			allowIndex, firstReloadIndex, lastReloadIndex, deleteIndex := -1, -1, -1, -1
			for index, call := range calls {
				switch {
				case reflect.DeepEqual(call, []string{"ufw", "allow", "10065/tcp", "comment", firewallRuleComment}):
					allowIndex = index
				case len(call) >= 2 && call[0] == "systemctl" && (call[1] == "reload" || call[1] == "reload-or-restart"):
					if firstReloadIndex == -1 {
						firstReloadIndex = index
					}
					lastReloadIndex = index
				case test.wantDeletedPort != "" && reflect.DeepEqual(call, []string{"ufw", "--force", "delete", test.wantDeletedPort}):
					deleteIndex = index
				}
			}
			ordered := allowIndex >= 0 && firstReloadIndex >= 0 && allowIndex < firstReloadIndex
			if test.wantDeletedPort != "" {
				ordered = ordered && deleteIndex >= 0 && lastReloadIndex < deleteIndex
			} else {
				for _, call := range calls {
					if len(call) >= 3 && call[0] == "ufw" && call[1] == "--force" && call[2] == "delete" {
						ordered = false
					}
				}
			}
			if !ordered {
				t.Fatalf("unsafe transition ordering: calls=%v", calls)
			}
		})
	}
}

func TestFirewallTransitionAssignmentsUseSortedUnion(t *testing.T) {
	preOpen, active, desired, err := firewallTransitionAssignments(FirewallAssignment{
		Mode: FirewallModeApply, TCPPorts: []int{8443, 443, 443},
		DesiredTCPPorts: []int{10065, 8443}, Transition: true, ActivePlanComplete: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(preOpen.TCPPorts, []int{443, 8443, 10065}) ||
		!reflect.DeepEqual(active.TCPPorts, []int{443, 8443}) ||
		!reflect.DeepEqual(desired.TCPPorts, []int{8443, 10065}) {
		t.Fatalf("pre=%+v active=%+v desired=%+v", preOpen, active, desired)
	}
}

func runHeartbeatAssignmentWorkflow(t *testing.T, failValidation, failReports bool) (*fakeQuotaController, *FirewallReconciler, *heartbeatUpdateProcessor, context.CancelFunc) {
	t.Helper()
	configAssignment := assignment(42, "global\n")
	limit := int64(10)
	quotaAssignment := QuotaAssignment{Month: "2026-07", Policies: []QuotaBackendPolicy{
		quotaPolicy(quotaRouteA, quotaActionObserve, false, 1, &limit),
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent/v1/heartbeat":
			_ = json.NewEncoder(w).Encode(HeartbeatResponse{
				Assignment:         &configAssignment,
				QuotaAssignment:    &quotaAssignment,
				FirewallAssignment: &FirewallAssignment{Mode: FirewallModeOff},
				UpdateAssignment:   &UpdateManifest{Sequence: 9},
			})
		case "/agent/v1/config-report":
			if failReports {
				w.WriteHeader(http.StatusInternalServerError)
			} else {
				w.WriteHeader(http.StatusAccepted)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	runner := &fakeRunner{failValidate: failValidation}
	manager := &ConfigManager{
		Runner: runner, ManagedConfig: filepath.Join(t.TempDir(), "nodeflow.cfg"),
		HAProxyBinary: "haproxy", ServiceName: "haproxy.service",
	}
	reconciler := &Reconciler{Manager: manager, Reporter: ConfigReporter{URL: server.URL, Token: "secret", Client: server.Client()}}
	quotaController := &fakeQuotaController{}
	quota := &QuotaReconciler{Controller: quotaController, Manager: manager}
	firewall := &FirewallReconciler{Config: FirewallConfig{Mode: FirewallModeOff}, Runner: runner}
	updater := &heartbeatUpdateProcessor{called: make(chan UpdateManifest, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sender := &HeartbeatSender{
		URL: server.URL, Token: "secret", Version: "test", Client: server.Client(),
		Reconciler: reconciler, Quota: quota, Firewall: firewall, Updater: updater,
		Logger: log.New(io.Discard, "", 0),
	}
	go sender.Run(ctx, time.Hour)
	return quotaController, firewall, updater, cancel
}

type sequenceHAProxyRuntimeCollector struct {
	mu          sync.Mutex
	generations []string
	errs        []error
}

func (c *sequenceHAProxyRuntimeCollector) Collect(context.Context) (HAProxyRuntimeStats, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	stats := emptyHAProxyRuntimeStats()
	stats.CounterGeneration, c.generations = c.generations[0], c.generations[1:]
	err := c.errs[0]
	c.errs = c.errs[1:]
	return stats, err
}

func TestHeartbeatRereadsServiceStateWhenRuntimeGenerationChanges(t *testing.T) {
	const show = "systemctl show --property=ActiveState --value haproxy.service"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	runner := &serviceControlRunner{state: "active"}
	controller := &HAProxyServiceController{Runner: runner, Manager: &ConfigManager{}, ServiceName: "haproxy.service", StateRefreshInterval: time.Hour}
	collector := &sequenceHAProxyRuntimeCollector{
		generations: []string{"1:10:0", "1:10:0", "1:10:0", "1:10:1", "", "2:20:0"},
		errs:        []error{nil, nil, nil, nil, errors.New("socket unavailable"), nil},
	}
	sender := HeartbeatSender{URL: server.URL, Token: "secret", Version: "1.2.3", Client: server.Client(), ServiceControl: controller, HAProxyRuntime: collector}
	want := []int{1, 1, 1, 2, 3, 4}
	for i, expected := range want {
		if err := sender.Send(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := countCommands(runner, show); got != expected {
			t.Fatalf("heartbeat %d: systemctl show calls=%d want %d", i, got, expected)
		}
	}
}

func TestJitteredIntervalStaysWithinTenPercent(t *testing.T) {
	interval := 15 * time.Second
	if got := jitteredInterval(interval, 0); got != 13500*time.Millisecond {
		t.Fatalf("lower bound=%s", got)
	}
	if got := jitteredInterval(interval, 0.5); got != interval {
		t.Fatalf("midpoint=%s", got)
	}
	if got := jitteredInterval(interval, 0.999999); got >= 16500*time.Millisecond || got < 16499*time.Millisecond {
		t.Fatalf("upper bound=%s", got)
	}
	if got := jitteredInterval(time.Nanosecond, 0); got != time.Nanosecond {
		t.Fatalf("tiny interval=%s", got)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("non-positive interval must panic like time.NewTicker")
		}
	}()
	jitteredInterval(0, 0.5)
}
