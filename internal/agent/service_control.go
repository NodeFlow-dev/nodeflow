package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// defaultServiceStateRefresh bounds how long a cached systemd ActiveState is
// reused by heartbeat snapshots when nothing indicates it has changed.
const defaultServiceStateRefresh = time.Minute

type HAProxyServiceAssignment struct {
	Generation        int64 `json:"generation"`
	Enabled           bool  `json:"enabled"`
	RestartGeneration int64 `json:"restart_generation,omitempty"`
}

type HAProxyServiceSnapshot struct {
	State             string
	Generation        int64
	RestartGeneration int64
	LastError         string
}

type HAProxyServiceController struct {
	Runner      Runner
	Manager     *ConfigManager
	ServiceName string
	// StateRefreshInterval caps the age of the cached ActiveState used by
	// Snapshot. Zero selects defaultServiceStateRefresh; negative disables
	// caching so every Snapshot forks systemctl.
	StateRefreshInterval time.Duration
	Now                  func() time.Time

	mu                sync.Mutex
	generation        int64
	restartGeneration int64
	lastError         string

	cachedState   string
	cachedAt      time.Time
	cachedEpoch   uint64
	cachedStateOK bool
}

// Snapshot reports the service state. systemctl is forked only when the
// cached ActiveState is missing, expired, invalidated by Reconcile or
// InvalidateState, or when the config manager applied/rolled back a revision
// since the state was read.
func (c *HAProxyServiceController) Snapshot(ctx context.Context) HAProxyServiceSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.cachedServiceState(ctx)
	return HAProxyServiceSnapshot{State: state, Generation: c.generation, RestartGeneration: c.restartGeneration, LastError: c.lastError}
}

// InvalidateState forces the next Snapshot to re-read systemd state. Callers
// use it when an external signal (for example an unreachable runtime socket)
// suggests the process may have changed state.
func (c *HAProxyServiceController) InvalidateState() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cachedStateOK = false
}

func (c *HAProxyServiceController) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *HAProxyServiceController) cachedServiceState(ctx context.Context) string {
	refresh := c.StateRefreshInterval
	if refresh == 0 {
		refresh = defaultServiceStateRefresh
	}
	now := c.now()
	epoch := c.Manager.MutationEpoch()
	if refresh > 0 && c.cachedStateOK && epoch == c.cachedEpoch {
		if age := now.Sub(c.cachedAt); age >= 0 && age < refresh {
			return c.cachedState
		}
	}
	state, err := c.readState(ctx)
	if err != nil {
		c.cachedStateOK = false
		return "unknown"
	}
	c.rememberState(state, epoch, now)
	return state
}

func (c *HAProxyServiceController) rememberState(state string, epoch uint64, now time.Time) {
	c.cachedState, c.cachedEpoch, c.cachedAt, c.cachedStateOK = state, epoch, now, true
}

func (c *HAProxyServiceController) Reconcile(ctx context.Context, assignment HAProxyServiceAssignment) error {
	if assignment.Generation < 0 {
		return errors.New("invalid HAProxy service control generation")
	}
	if assignment.RestartGeneration < 0 {
		return errors.New("invalid HAProxy restart generation")
	}
	if assignment.Generation == 0 && assignment.RestartGeneration == 0 {
		return errors.New("empty HAProxy service control assignment")
	}
	if c.Runner == nil || c.Manager == nil || strings.TrimSpace(c.ServiceName) == "" {
		return errors.New("HAProxy service controller is not configured")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if assignment.Generation < c.generation {
		return nil
	}
	stateChangeNeeded := assignment.Generation > c.generation
	restartNeeded := assignment.RestartGeneration > c.restartGeneration
	if !stateChangeNeeded && !restartNeeded {
		return nil
	}
	if restartNeeded && !assignment.Enabled {
		err := errors.New("cannot hard restart disabled HAProxy service")
		c.lastError = boundedServiceError(err.Error())
		return err
	}
	// Any systemctl start/stop/restart below changes the process state; drop
	// the cached value first so a failed reconcile cannot keep reporting it.
	c.cachedStateOK = false
	var verifiedState string
	err := c.Manager.RunSerialized(func() error {
		if stateChangeNeeded {
			action := "stop"
			if assignment.Enabled {
				action = "start"
			}
			if output, runErr := c.Runner.Run(ctx, "systemctl", action, c.ServiceName); runErr != nil {
				return fmt.Errorf("systemctl %s failed: %s", action, strings.TrimSpace(string(output)))
			}

			// Runtime state is authoritative for the operator action. Some HAProxy
			// packages ship both a native systemd unit and /etc/init.d/haproxy;
			// systemctl enable/disable then delegates to systemd-sysv-install and may
			// fail even though start/stop works. Keep boot persistence best-effort so
			// that a packaging compatibility error cannot block config reconciliation.
			c.reconcileBootState(ctx, assignment.Enabled)

			state, stateErr := c.readState(ctx)
			if stateErr != nil {
				return stateErr
			}
			if assignment.Enabled && state != "active" && state != "reloading" {
				return fmt.Errorf("HAProxy service did not become active: %s", state)
			}
			if !assignment.Enabled && state != "inactive" {
				return fmt.Errorf("HAProxy service did not stop: %s", state)
			}
			verifiedState = state
		}
		if restartNeeded {
			if output, runErr := c.Runner.Run(ctx, "systemctl", "restart", c.ServiceName); runErr != nil {
				return fmt.Errorf("systemctl restart failed: %s", strings.TrimSpace(string(output)))
			}
			state, stateErr := c.readState(ctx)
			if stateErr != nil {
				return stateErr
			}
			if state != "active" {
				return fmt.Errorf("HAProxy service did not become active after hard restart: %s", state)
			}
			verifiedState = state
		}
		return nil
	})
	if err != nil {
		c.lastError = boundedServiceError(err.Error())
		return err
	}
	if stateChangeNeeded {
		c.generation = assignment.Generation
	}
	if restartNeeded {
		c.restartGeneration = assignment.RestartGeneration
	}
	c.lastError = ""
	if verifiedState != "" {
		// State was just read under the manager lock; reuse it for the next
		// heartbeat instead of forking systemctl again.
		c.rememberState(verifiedState, c.Manager.MutationEpoch(), c.now())
	}
	return nil
}

func (c *HAProxyServiceController) reconcileBootState(ctx context.Context, enabled bool) {
	output, err := c.Runner.Run(ctx, "systemctl", "is-enabled", c.ServiceName)
	current := strings.ToLower(strings.TrimSpace(string(output)))
	if err == nil {
		if enabled && (current == "enabled" || current == "static" || current == "indirect" || current == "generated") {
			return
		}
		if !enabled && (current == "disabled" || current == "masked") {
			return
		}
	}
	action := "disable"
	if enabled {
		action = "enable"
	}
	_, _ = c.Runner.Run(ctx, "systemctl", action, c.ServiceName)
}

func (c *HAProxyServiceController) readState(ctx context.Context) (string, error) {
	output, err := c.Runner.Run(ctx, "systemctl", "show", "--property=ActiveState", "--value", c.ServiceName)
	if err != nil {
		return "unknown", fmt.Errorf("read HAProxy service state: %w", err)
	}
	state := strings.ToLower(strings.TrimSpace(string(output)))
	switch state {
	case "active", "reloading", "inactive", "failed", "activating", "deactivating":
		return state, nil
	default:
		return "unknown", nil
	}
}

func boundedServiceError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 200 {
		return value[:200]
	}
	return value
}
