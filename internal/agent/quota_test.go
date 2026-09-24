package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	quotaRouteA = "11111111-1111-4111-8111-111111111111"
	quotaRouteB = "22222222-2222-4222-8222-222222222222"
)

type quotaControllerCall struct {
	backend string
	server  string
	block   bool
}

type fakeQuotaController struct {
	mu            sync.Mutex
	calls         []quotaControllerCall
	failRemaining int
	called        chan struct{}
}

func (c *fakeQuotaController) SetServerMaintenance(_ context.Context, backend, server string, block bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, quotaControllerCall{backend: backend, server: server, block: block})
	if c.called != nil {
		select {
		case c.called <- struct{}{}:
		default:
		}
	}
	if c.failRemaining > 0 {
		c.failRemaining--
		return errors.New("runtime command failed")
	}
	return nil
}

func (c *fakeQuotaController) snapshotCalls() []quotaControllerCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]quotaControllerCall(nil), c.calls...)
}

func quotaPolicy(routeID, action string, block bool, used int64, limit *int64) QuotaBackendPolicy {
	compact := strings.ReplaceAll(routeID, "-", "")
	return QuotaBackendPolicy{
		RouteID: routeID, Backend: quotaBackendPrefix + compact, Server: quotaServerPrefix + compact,
		Action: action, Block: block, UsedBytes: used, LimitBytes: limit,
	}
}

func int64Pointer(value int64) *int64 { return &value }

func TestQuotaReconcilerCachesMatchingStateAndRepairsRuntimeDrift(t *testing.T) {
	controller := &fakeQuotaController{}
	reconciler := &QuotaReconciler{Controller: controller, Manager: &ConfigManager{}}
	limit := int64(10)
	assignment := QuotaAssignment{Month: "2026-07", Policies: []QuotaBackendPolicy{
		quotaPolicy(quotaRouteB, quotaActionBlockNew, true, 10, &limit),
		quotaPolicy(quotaRouteA, quotaActionObserve, false, 50, &limit),
	}}

	if err := reconciler.Reconcile(context.Background(), assignment); err != nil {
		t.Fatal(err)
	}
	calls := controller.snapshotCalls()
	if len(calls) != 2 || calls[0].backend != quotaBackendPrefix+strings.ReplaceAll(quotaRouteA, "-", "") || calls[0].block || !calls[1].block {
		t.Fatalf("calls=%+v", calls)
	}
	if err := reconciler.Reconcile(context.Background(), assignment); err != nil {
		t.Fatal(err)
	}
	if calls = controller.snapshotCalls(); len(calls) != 2 {
		t.Fatalf("unchanged policies caused redundant Runtime API calls: %+v", calls)
	}

	blocked := assignment.Policies[0]
	ready := assignment.Policies[1]
	reconciler.ObserveRuntime(HAProxyRuntimeStats{Servers: map[string]map[string]HAProxyServerStats{
		blocked.Backend: {blocked.Server: {Status: "UP"}},
		ready.Backend:   {ready.Server: {Status: "UP"}},
	}})
	if err := reconciler.Reconcile(context.Background(), assignment); err != nil {
		t.Fatal(err)
	}
	if calls = controller.snapshotCalls(); len(calls) != 3 || !calls[2].block || calls[2].backend != blocked.Backend {
		t.Fatalf("runtime drift was not repaired exactly once: %+v", calls)
	}

	assignment.Policies[0] = quotaPolicy(quotaRouteB, quotaActionBlockNew, false, 9, &limit)
	if err := reconciler.Reconcile(context.Background(), assignment); err != nil {
		t.Fatal(err)
	}
	calls = controller.snapshotCalls()
	if len(calls) != 4 || calls[3].backend != quotaBackendPrefix+strings.ReplaceAll(quotaRouteB, "-", "") || calls[3].block {
		t.Fatalf("unblock calls=%+v", calls)
	}

	snapshot := reconciler.Snapshot()
	if len(snapshot) != 2 || snapshot[quotaRuntimeKey(assignment.Policies[0].Backend, assignment.Policies[0].Server)] {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	snapshot["mutated"] = true
	if _, leaked := reconciler.Snapshot()["mutated"]; leaked {
		t.Fatal("Snapshot returned internal map")
	}
}

func TestQuotaReconcilerRetriesFailedCommand(t *testing.T) {
	controller := &fakeQuotaController{failRemaining: 1}
	reconciler := &QuotaReconciler{Controller: controller, Manager: &ConfigManager{}}
	assignment := QuotaAssignment{Month: "2026-07", Policies: []QuotaBackendPolicy{
		quotaPolicy(quotaRouteA, quotaActionBlockNew, true, 11, int64Pointer(10)),
	}}

	if err := reconciler.Reconcile(context.Background(), assignment); err == nil {
		t.Fatal("expected runtime command failure")
	}
	if snapshot := reconciler.Snapshot(); len(snapshot) != 0 {
		t.Fatalf("failed command published as applied: %+v", snapshot)
	}
	if err := reconciler.Reconcile(context.Background(), assignment); err != nil {
		t.Fatal(err)
	}
	if calls := controller.snapshotCalls(); len(calls) != 2 {
		t.Fatalf("failed command was not retried: %+v", calls)
	}
}

func TestQuotaReconcilerFailedBlockedReassertDoesNotPublishStaleState(t *testing.T) {
	controller := &fakeQuotaController{}
	reconciler := &QuotaReconciler{Controller: controller, Manager: &ConfigManager{}}
	assignment := QuotaAssignment{Month: "2026-07", Policies: []QuotaBackendPolicy{
		quotaPolicy(quotaRouteA, quotaActionBlockNew, true, 11, int64Pointer(10)),
	}}
	if err := reconciler.Reconcile(context.Background(), assignment); err != nil {
		t.Fatal(err)
	}
	controller.mu.Lock()
	controller.failRemaining = 1
	controller.mu.Unlock()
	policy := assignment.Policies[0]
	reconciler.ObserveRuntime(HAProxyRuntimeStats{Servers: map[string]map[string]HAProxyServerStats{
		policy.Backend: {policy.Server: {Status: "UP"}},
	}})
	if err := reconciler.Reconcile(context.Background(), assignment); err == nil {
		t.Fatal("expected blocked-state reassertion failure")
	}
	if snapshot := reconciler.Snapshot(); len(snapshot) != 0 {
		t.Fatalf("failed reassertion retained stale runtime state: %+v", snapshot)
	}
}

func TestQuotaRuntimeObservationAcceptsMaintenanceVariants(t *testing.T) {
	for _, status := range []string{"MAINT", "MAINT (agent)", " maint(via bc) "} {
		if !runtimeServerInMaintenance(status) {
			t.Fatalf("status %q was not recognized as maintenance", status)
		}
	}
	for _, status := range []string{"", "UP", "DOWN"} {
		if runtimeServerInMaintenance(status) {
			t.Fatalf("status %q was incorrectly recognized as maintenance", status)
		}
	}
}

func TestQuotaReconcilerPrunesObservedStateAfterCompleteAssignment(t *testing.T) {
	controller := &fakeQuotaController{}
	reconciler := &QuotaReconciler{Controller: controller, Manager: &ConfigManager{}}
	limit := int64(10)
	first := QuotaAssignment{Month: "2026-07", Policies: []QuotaBackendPolicy{
		quotaPolicy(quotaRouteA, quotaActionObserve, false, 1, &limit),
		quotaPolicy(quotaRouteB, quotaActionBlockNew, true, 10, &limit),
	}}
	if err := reconciler.Reconcile(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background(), QuotaAssignment{Month: "2026-07", Policies: first.Policies[:1]}); err != nil {
		t.Fatal(err)
	}
	if snapshot := reconciler.Snapshot(); len(snapshot) != 1 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestQuotaReconcilerResetForgetsReloadedRuntimeState(t *testing.T) {
	controller := &fakeQuotaController{}
	reconciler := &QuotaReconciler{Controller: controller, Manager: &ConfigManager{}}
	assignment := QuotaAssignment{Month: "2026-07", Policies: []QuotaBackendPolicy{
		quotaPolicy(quotaRouteA, quotaActionBlockNew, true, 10, int64Pointer(10)),
	}}
	requireNoError := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	requireNoError(reconciler.Reconcile(context.Background(), assignment))
	reconciler.Reset()
	if snapshot := reconciler.Snapshot(); len(snapshot) != 0 {
		t.Fatalf("snapshot after reset=%+v", snapshot)
	}
	requireNoError(reconciler.Reconcile(context.Background(), assignment))
	if calls := controller.snapshotCalls(); len(calls) != 2 {
		t.Fatalf("runtime state was not reissued after reload: %+v", calls)
	}
}

func TestQuotaAssignmentValidationRejectsUnsafePolicies(t *testing.T) {
	valid := quotaPolicy(quotaRouteA, quotaActionBlockNew, true, 10, int64Pointer(10))
	tests := map[string]QuotaAssignment{
		"month": {Month: "2026-7", Policies: []QuotaBackendPolicy{valid}},
		"uppercase UUID": {Month: "2026-07", Policies: []QuotaBackendPolicy{func() QuotaBackendPolicy {
			p := quotaPolicy("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", quotaActionBlockNew, true, 10, int64Pointer(10))
			p.RouteID = strings.ToUpper(p.RouteID)
			return p
		}()}},
		"mismatched backend":    {Month: "2026-07", Policies: []QuotaBackendPolicy{func() QuotaBackendPolicy { p := valid; p.Backend = "nf_be_other"; return p }()}},
		"invalid action":        {Month: "2026-07", Policies: []QuotaBackendPolicy{func() QuotaBackendPolicy { p := valid; p.Action = "drop"; return p }()}},
		"observe block":         {Month: "2026-07", Policies: []QuotaBackendPolicy{quotaPolicy(quotaRouteA, quotaActionObserve, true, 10, int64Pointer(10))}},
		"before limit":          {Month: "2026-07", Policies: []QuotaBackendPolicy{quotaPolicy(quotaRouteA, quotaActionBlockNew, true, 9, int64Pointer(10))}},
		"reached without block": {Month: "2026-07", Policies: []QuotaBackendPolicy{quotaPolicy(quotaRouteA, quotaActionBlockNew, false, 10, int64Pointer(10))}},
		"missing limit":         {Month: "2026-07", Policies: []QuotaBackendPolicy{quotaPolicy(quotaRouteA, quotaActionBlockNew, false, 0, nil)}},
		"negative used":         {Month: "2026-07", Policies: []QuotaBackendPolicy{quotaPolicy(quotaRouteA, quotaActionObserve, false, -1, nil)}},
		"duplicate route":       {Month: "2026-07", Policies: []QuotaBackendPolicy{valid, valid}},
	}
	for name, assignment := range tests {
		t.Run(name, func(t *testing.T) {
			controller := &fakeQuotaController{}
			reconciler := &QuotaReconciler{Controller: controller, Manager: &ConfigManager{}}
			if err := reconciler.Reconcile(context.Background(), assignment); err == nil {
				t.Fatal("expected validation error")
			}
			if calls := controller.snapshotCalls(); len(calls) != 0 {
				t.Fatalf("validation happened after runtime command: %+v", calls)
			}
		})
	}
}

func TestQuotaAssignmentAcceptsCurrentAndLegacyRendererRuntimeObjects(t *testing.T) {
	compact := strings.ReplaceAll(quotaRouteA, "-", "")
	for name, policy := range map[string]QuotaBackendPolicy{
		"short nodeflow": {
			RouteID: quotaRouteA, Backend: quotaBackendPrefix + compact[:12], Server: quotaServerPrefix + compact[:12],
			Action: quotaActionObserve,
		},
		"full nodeflow": quotaPolicy(quotaRouteA, quotaActionObserve, false, 0, nil),
		"legacy bridge control": {
			RouteID: quotaRouteA, Backend: legacyQuotaBackendPrefix + compact, Server: legacyQuotaServerPrefix + compact,
			Action: quotaActionObserve,
		},
	} {
		t.Run(name, func(t *testing.T) {
			reconciler := &QuotaReconciler{Controller: &fakeQuotaController{}, Manager: &ConfigManager{}}
			if err := reconciler.Reconcile(context.Background(), QuotaAssignment{Month: "2026-07", Policies: []QuotaBackendPolicy{policy}}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestQuotaAssignmentAcceptsMixedWindowsAndNormalizesLegacy(t *testing.T) {
	hourly := quotaPolicy(quotaRouteA, quotaActionObserve, false, 7, nil)
	hourly.QuotaPeriod = "hourly"
	hourly.WindowStart = time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	hourly.WindowEnd = hourly.WindowStart.Add(time.Hour)

	legacy := quotaPolicy(quotaRouteB, quotaActionObserve, false, 9, nil)
	policies, err := validateQuotaAssignment(QuotaAssignment{
		Month: "2026-07", Policies: []QuotaBackendPolicy{legacy, hourly},
	})
	require.NoError(t, err)
	require.Len(t, policies, 2)
	byRoute := map[string]QuotaBackendPolicy{}
	for _, policy := range policies {
		byRoute[policy.RouteID] = policy
	}
	assert.Equal(t, "hourly", byRoute[quotaRouteA].QuotaPeriod)
	assert.Equal(t, hourly.WindowStart, byRoute[quotaRouteA].WindowStart)
	assert.Equal(t, "calendar_month", byRoute[quotaRouteB].QuotaPeriod)
	assert.Equal(t, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), byRoute[quotaRouteB].WindowStart)
	assert.Equal(t, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), byRoute[quotaRouteB].WindowEnd)
}

func TestQuotaAssignmentRejectsInvalidExplicitWindows(t *testing.T) {
	valid := quotaPolicy(quotaRouteA, quotaActionObserve, false, 0, nil)
	valid.QuotaPeriod = "daily"
	valid.WindowStart = time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	valid.WindowEnd = valid.WindowStart.AddDate(0, 0, 1)
	tests := []QuotaBackendPolicy{
		func() QuotaBackendPolicy { p := valid; p.QuotaPeriod = "weekly"; return p }(),
		func() QuotaBackendPolicy { p := valid; p.WindowEnd = p.WindowStart; return p }(),
		func() QuotaBackendPolicy { p := valid; p.WindowStart = p.WindowStart.Add(time.Hour); return p }(),
		func() QuotaBackendPolicy {
			p := valid
			p.WindowStart = time.Date(2026, 7, 12, 0, 0, 0, 0, time.FixedZone("UTC+3", 3*60*60))
			p.WindowEnd = p.WindowStart.AddDate(0, 0, 1)
			return p
		}(),
	}
	for _, policy := range tests {
		_, err := validateQuotaAssignment(QuotaAssignment{Month: "2026-07", Policies: []QuotaBackendPolicy{policy}})
		assert.Error(t, err)
	}
}

func TestQuotaReconcilerSerializesWithConfigManager(t *testing.T) {
	manager := &ConfigManager{}
	controller := &fakeQuotaController{called: make(chan struct{}, 1)}
	reconciler := &QuotaReconciler{Controller: controller, Manager: manager}
	managerEntered := make(chan struct{})
	managerRelease := make(chan struct{})
	managerDone := make(chan error, 1)
	go func() {
		managerDone <- manager.RunSerialized(func() error {
			close(managerEntered)
			<-managerRelease
			return nil
		})
	}()
	<-managerEntered

	reconcileDone := make(chan error, 1)
	go func() {
		reconcileDone <- reconciler.Reconcile(context.Background(), QuotaAssignment{Month: "2026-07", Policies: []QuotaBackendPolicy{
			quotaPolicy(quotaRouteA, quotaActionObserve, false, 0, nil),
		}})
	}()
	select {
	case <-controller.called:
		t.Fatal("quota runtime command bypassed config manager lock")
	case <-time.After(25 * time.Millisecond):
	}
	close(managerRelease)
	if err := <-managerDone; err != nil {
		t.Fatal(err)
	}
	if err := <-reconcileDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-controller.called:
	case <-time.After(time.Second):
		t.Fatal("quota runtime command was not issued")
	}
}
