package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type serviceControlRunner struct {
	mu           sync.Mutex
	state        string
	bootState    string
	restartState string
	commands     []string
	failAction   string
}

func (r *serviceControlRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands = append(r.commands, name+" "+strings.Join(args, " "))
	if len(args) > 0 && args[0] == "show" {
		return []byte(r.state + "\n"), nil
	}
	if len(args) > 0 && args[0] == "is-enabled" {
		if r.bootState == "" {
			r.bootState = "enabled"
		}
		return []byte(r.bootState + "\n"), nil
	}
	if len(args) > 0 && args[0] == r.failAction {
		return []byte("permission denied"), errors.New("exit 1")
	}
	if len(args) > 0 && args[0] == "start" {
		r.state = "active"
	}
	if len(args) > 0 && args[0] == "restart" {
		r.state = r.restartState
		if r.state == "" {
			r.state = "active"
		}
	}
	if len(args) > 0 && args[0] == "stop" {
		r.state = "inactive"
	}
	if len(args) > 0 && args[0] == "enable" {
		r.bootState = "enabled"
	}
	if len(args) > 0 && args[0] == "disable" {
		r.bootState = "disabled"
	}
	return nil, nil
}

func TestHAProxyServiceControllerStopAndStart(t *testing.T) {
	runner := &serviceControlRunner{state: "active"}
	manager := &ConfigManager{}
	controller := &HAProxyServiceController{Runner: runner, Manager: manager, ServiceName: "haproxy.service"}

	require.NoError(t, controller.Reconcile(context.Background(), HAProxyServiceAssignment{Generation: 1, Enabled: false}))
	snapshot := controller.Snapshot(context.Background())
	assert.Equal(t, "inactive", snapshot.State)
	assert.Equal(t, int64(1), snapshot.Generation)

	require.NoError(t, controller.Reconcile(context.Background(), HAProxyServiceAssignment{Generation: 2, Enabled: true}))
	snapshot = controller.Snapshot(context.Background())
	assert.Equal(t, "active", snapshot.State)
	assert.Equal(t, int64(2), snapshot.Generation)
	assert.Contains(t, runner.commands, "systemctl stop haproxy.service")
	assert.Contains(t, runner.commands, "systemctl disable haproxy.service")
	assert.Contains(t, runner.commands, "systemctl start haproxy.service")
	assert.Contains(t, runner.commands, "systemctl enable haproxy.service")
}

func TestHAProxyServiceControllerDoesNotAcknowledgeFailure(t *testing.T) {
	runner := &serviceControlRunner{state: "active", failAction: "stop"}
	controller := &HAProxyServiceController{Runner: runner, Manager: &ConfigManager{}, ServiceName: "haproxy.service"}

	err := controller.Reconcile(context.Background(), HAProxyServiceAssignment{Generation: 1, Enabled: false})
	require.Error(t, err)
	snapshot := controller.Snapshot(context.Background())
	assert.Equal(t, int64(0), snapshot.Generation)
	assert.Contains(t, snapshot.LastError, "systemctl stop failed")
}

func TestHAProxyServiceControllerIgnoresBootPersistenceFailure(t *testing.T) {
	runner := &serviceControlRunner{state: "inactive", bootState: "disabled", failAction: "enable"}
	controller := &HAProxyServiceController{Runner: runner, Manager: &ConfigManager{}, ServiceName: "haproxy.service"}

	require.NoError(t, controller.Reconcile(context.Background(), HAProxyServiceAssignment{Generation: 7, Enabled: true}))
	snapshot := controller.Snapshot(context.Background())
	assert.Equal(t, "active", snapshot.State)
	assert.Equal(t, int64(7), snapshot.Generation)
	assert.Empty(t, snapshot.LastError)
	assert.Contains(t, runner.commands, "systemctl start haproxy.service")
	assert.Contains(t, runner.commands, "systemctl enable haproxy.service")
}

func TestHAProxyServiceControllerHardRestartsOncePerGeneration(t *testing.T) {
	runner := &serviceControlRunner{state: "active"}
	controller := &HAProxyServiceController{Runner: runner, Manager: &ConfigManager{}, ServiceName: "haproxy.service"}

	require.NoError(t, controller.Reconcile(context.Background(), HAProxyServiceAssignment{Generation: 1, Enabled: true}))
	require.NoError(t, controller.Reconcile(context.Background(), HAProxyServiceAssignment{Generation: 1, Enabled: true, RestartGeneration: 3}))
	// The Panel keeps this assignment in heartbeat responses until it observes
	// the acknowledgement. It must not produce another restart meanwhile.
	require.NoError(t, controller.Reconcile(context.Background(), HAProxyServiceAssignment{Generation: 1, Enabled: true, RestartGeneration: 3}))

	snapshot := controller.Snapshot(context.Background())
	assert.Equal(t, int64(1), snapshot.Generation)
	assert.Equal(t, int64(3), snapshot.RestartGeneration)
	assert.Equal(t, "active", snapshot.State)
	restarts := 0
	for _, command := range runner.commands {
		if command == "systemctl restart haproxy.service" {
			restarts++
		}
	}
	assert.Equal(t, 1, restarts, "restart must be acknowledged exactly once")
}

func TestHAProxyServiceControllerAllowsRestartWithoutServiceGeneration(t *testing.T) {
	runner := &serviceControlRunner{state: "active"}
	controller := &HAProxyServiceController{Runner: runner, Manager: &ConfigManager{}, ServiceName: "haproxy.service"}

	require.NoError(t, controller.Reconcile(context.Background(), HAProxyServiceAssignment{Enabled: true, RestartGeneration: 1}))
	snapshot := controller.Snapshot(context.Background())
	assert.Zero(t, snapshot.Generation)
	assert.Equal(t, int64(1), snapshot.RestartGeneration)
	assert.Contains(t, runner.commands, "systemctl restart haproxy.service")
}

func TestHAProxyServiceControllerRejectsHardRestartWhenDisabled(t *testing.T) {
	runner := &serviceControlRunner{state: "inactive"}
	controller := &HAProxyServiceController{Runner: runner, Manager: &ConfigManager{}, ServiceName: "haproxy.service"}

	require.NoError(t, controller.Reconcile(context.Background(), HAProxyServiceAssignment{Generation: 1, Enabled: false}))
	err := controller.Reconcile(context.Background(), HAProxyServiceAssignment{Generation: 1, Enabled: false, RestartGeneration: 1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disabled")
	snapshot := controller.Snapshot(context.Background())
	assert.Equal(t, int64(0), snapshot.RestartGeneration)
	assert.Contains(t, snapshot.LastError, "disabled")
	assert.NotContains(t, runner.commands, "systemctl restart haproxy.service")
}

func TestHAProxyServiceControllerDoesNotAcknowledgeFailedHardRestart(t *testing.T) {
	runner := &serviceControlRunner{state: "active", failAction: "restart"}
	controller := &HAProxyServiceController{Runner: runner, Manager: &ConfigManager{}, ServiceName: "haproxy.service"}

	require.NoError(t, controller.Reconcile(context.Background(), HAProxyServiceAssignment{Generation: 1, Enabled: true}))
	err := controller.Reconcile(context.Background(), HAProxyServiceAssignment{Generation: 1, Enabled: true, RestartGeneration: 1})
	require.Error(t, err)
	snapshot := controller.Snapshot(context.Background())
	assert.Equal(t, int64(0), snapshot.RestartGeneration)
	assert.Contains(t, snapshot.LastError, "systemctl restart failed")
}

func TestHAProxyServiceControllerRequiresActiveAfterHardRestart(t *testing.T) {
	runner := &serviceControlRunner{state: "active", restartState: "failed"}
	controller := &HAProxyServiceController{Runner: runner, Manager: &ConfigManager{}, ServiceName: "haproxy.service"}

	require.NoError(t, controller.Reconcile(context.Background(), HAProxyServiceAssignment{Generation: 1, Enabled: true}))
	err := controller.Reconcile(context.Background(), HAProxyServiceAssignment{Generation: 1, Enabled: true, RestartGeneration: 1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not become active after hard restart")
	assert.Equal(t, int64(0), controller.Snapshot(context.Background()).RestartGeneration)
}

func TestHAProxyServiceAssignmentAcceptsOlderPanelJSON(t *testing.T) {
	var assignment HAProxyServiceAssignment
	require.NoError(t, json.Unmarshal([]byte(`{"generation":4,"enabled":true}`), &assignment))
	assert.Equal(t, int64(4), assignment.Generation)
	assert.True(t, assignment.Enabled)
	assert.Zero(t, assignment.RestartGeneration)
}

func countCommands(runner *serviceControlRunner, command string) int {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	count := 0
	for _, got := range runner.commands {
		if got == command {
			count++
		}
	}
	return count
}

func TestHAProxyServiceControllerSnapshotCachesActiveState(t *testing.T) {
	const show = "systemctl show --property=ActiveState --value haproxy.service"
	now := time.Unix(1700000000, 0)
	runner := &serviceControlRunner{state: "active"}
	manager := &ConfigManager{}
	controller := &HAProxyServiceController{Runner: runner, Manager: manager, ServiceName: "haproxy.service",
		StateRefreshInterval: time.Minute, Now: func() time.Time { return now }}

	for i := 0; i < 5; i++ {
		assert.Equal(t, "active", controller.Snapshot(context.Background()).State)
	}
	assert.Equal(t, 1, countCommands(runner, show), "idle snapshots must reuse cached state")

	// Explicit invalidation re-reads.
	runner.mu.Lock()
	runner.state = "failed"
	runner.mu.Unlock()
	controller.InvalidateState()
	assert.Equal(t, "failed", controller.Snapshot(context.Background()).State)
	assert.Equal(t, 2, countCommands(runner, show))

	// Config apply/rollback bumps the manager epoch.
	runner.mu.Lock()
	runner.state = "active"
	runner.mu.Unlock()
	manager.mu.Lock()
	manager.markMutated()
	manager.mu.Unlock()
	assert.Equal(t, "active", controller.Snapshot(context.Background()).State)
	assert.Equal(t, 3, countCommands(runner, show))

	// TTL expiry re-reads.
	now = now.Add(time.Minute)
	assert.Equal(t, "active", controller.Snapshot(context.Background()).State)
	assert.Equal(t, 4, countCommands(runner, show))

	// Reconcile verifies the new state and seeds the cache with it.
	require.NoError(t, controller.Reconcile(context.Background(), HAProxyServiceAssignment{Generation: 1, Enabled: false}))
	afterReconcile := countCommands(runner, show)
	assert.Equal(t, "inactive", controller.Snapshot(context.Background()).State)
	assert.Equal(t, afterReconcile, countCommands(runner, show))
}

func TestHAProxyServiceControllerSnapshotDoesNotCacheErrors(t *testing.T) {
	runner := &failingShowRunner{}
	controller := &HAProxyServiceController{Runner: runner, Manager: &ConfigManager{}, ServiceName: "haproxy.service"}
	assert.Equal(t, "unknown", controller.Snapshot(context.Background()).State)
	assert.Equal(t, "unknown", controller.Snapshot(context.Background()).State)
	assert.Equal(t, 2, runner.calls)
}

func TestHAProxyServiceControllerNegativeRefreshDisablesCache(t *testing.T) {
	runner := &serviceControlRunner{state: "active"}
	controller := &HAProxyServiceController{Runner: runner, Manager: &ConfigManager{}, ServiceName: "haproxy.service", StateRefreshInterval: -1}
	controller.Snapshot(context.Background())
	controller.Snapshot(context.Background())
	assert.Equal(t, 2, countCommands(runner, "systemctl show --property=ActiveState --value haproxy.service"))
}

type failingShowRunner struct{ calls int }

func (r *failingShowRunner) Run(context.Context, string, ...string) ([]byte, error) {
	r.calls++
	return nil, errors.New("exit 1")
}
