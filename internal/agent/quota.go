package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxQuotaBackendPolicies  = 1024
	quotaActionObserve       = "observe"
	quotaActionBlockNew      = "block_new"
	quotaBackendPrefix       = "nf_be_"
	quotaServerPrefix        = "nf_srv_"
	legacyQuotaBackendPrefix = "bc_be_"
	legacyQuotaServerPrefix  = "bc_srv_"
)

// QuotaBackendPolicy describes the desired runtime state for one renderer-
// owned HAProxy server. Disabling a server stops new connections while
// existing connections are left intact by HAProxy.
type QuotaBackendPolicy struct {
	RouteID     string    `json:"route_id"`
	Backend     string    `json:"backend"`
	Server      string    `json:"server"`
	Action      string    `json:"action"`
	Block       bool      `json:"block"`
	UsedBytes   int64     `json:"used_bytes"`
	LimitBytes  *int64    `json:"limit_bytes,omitempty"`
	QuotaPeriod string    `json:"quota_period,omitempty"`
	WindowStart time.Time `json:"window_start,omitempty"`
	WindowEnd   time.Time `json:"window_end,omitempty"`
}

type QuotaAssignment struct {
	Month    string               `json:"month"`
	Policies []QuotaBackendPolicy `json:"policies"`
}

type HAProxyQuotaController interface {
	SetServerMaintenance(context.Context, string, string, bool) error
}

// QuotaReconciler applies only renderer-derived backend/server names. The
// applied map is an observed local state cache, not desired state: entries are
// published only after HAProxy accepted the corresponding runtime command.
type QuotaReconciler struct {
	Controller HAProxyQuotaController
	Manager    *ConfigManager

	mu      sync.Mutex
	applied map[string]bool
}

func (q *QuotaReconciler) Reconcile(ctx context.Context, assignment QuotaAssignment) error {
	if q == nil || q.Controller == nil || q.Manager == nil {
		return errors.New("quota reconciler is not configured")
	}
	policies, err := validateQuotaAssignment(assignment)
	if err != nil {
		return err
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	if q.applied == nil {
		q.applied = make(map[string]bool, len(policies))
	}

	err = q.Manager.RunSerialized(func() error {
		for _, policy := range policies {
			key := quotaRuntimeKey(policy.Backend, policy.Server)
			// Runtime observations invalidate this cache after an external reload
			// or operator change. Unchanged state therefore needs no per-route CLI
			// command on every heartbeat.
			if actual, known := q.applied[key]; known && actual == policy.Block {
				continue
			}
			if err := q.Controller.SetServerMaintenance(ctx, policy.Backend, policy.Server, policy.Block); err != nil {
				// A failed command leaves runtime state unknown, especially after an
				// external HAProxy restart. Do not keep publishing a stale claim.
				delete(q.applied, key)
				return fmt.Errorf("set quota runtime state for route %s: %w", policy.RouteID, err)
			}
			q.applied[key] = policy.Block
		}
		return nil
	})
	if err != nil {
		return err
	}

	// A successful complete assignment is authoritative. Removed entries refer
	// to servers no longer present in the active renderer revision and must not
	// remain in the observed-state snapshot.
	desired := make(map[string]struct{}, len(policies))
	for _, policy := range policies {
		desired[quotaRuntimeKey(policy.Backend, policy.Server)] = struct{}{}
	}
	for key := range q.applied {
		if _, exists := desired[key]; !exists {
			delete(q.applied, key)
		}
	}
	return nil
}

func normalizeAndValidateQuotaWindow(month string, policy *QuotaBackendPolicy) error {
	if policy.QuotaPeriod == "" && policy.WindowStart.IsZero() && policy.WindowEnd.IsZero() {
		start, err := time.Parse("2006-01", month)
		if err != nil {
			return errors.New("invalid quota assignment month")
		}
		policy.QuotaPeriod = "calendar_month"
		policy.WindowStart = start.UTC()
		policy.WindowEnd = start.AddDate(0, 1, 0).UTC()
	}
	if !validAgentQuotaPeriod(policy.QuotaPeriod) || policy.WindowStart.IsZero() || policy.WindowEnd.IsZero() {
		return errors.New("invalid quota policy window")
	}
	_, startOffset := policy.WindowStart.Zone()
	_, endOffset := policy.WindowEnd.Zone()
	if startOffset != 0 || endOffset != 0 || !policy.WindowEnd.After(policy.WindowStart) {
		return errors.New("invalid quota policy window")
	}
	start, end := policy.WindowStart.UTC(), policy.WindowEnd.UTC()
	switch policy.QuotaPeriod {
	case "hourly":
		if !start.Equal(start.Truncate(time.Hour)) || !end.Equal(start.Add(time.Hour)) {
			return errors.New("invalid hourly quota window")
		}
	case "daily":
		midnight := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
		if !start.Equal(midnight) || !end.Equal(start.AddDate(0, 0, 1)) {
			return errors.New("invalid daily quota window")
		}
	case "calendar_month":
		monthStart := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC)
		if !start.Equal(monthStart) || !end.Equal(start.AddDate(0, 1, 0)) {
			return errors.New("invalid calendar-month quota window")
		}
	case "monthly_from_creation":
		if duration := end.Sub(start); duration < 28*24*time.Hour || duration > 31*24*time.Hour {
			return errors.New("invalid anniversary-month quota window")
		}
	}
	policy.WindowStart, policy.WindowEnd = start, end
	return nil
}

func validAgentQuotaPeriod(value string) bool {
	switch value {
	case "hourly", "daily", "calendar_month", "monthly_from_creation":
		return true
	default:
		return false
	}
}

// ObserveRuntime compares the last successfully applied policy with the
// read-only HAProxy stats already collected for the heartbeat. Missing or
// mismatched servers are forgotten so the next authoritative assignment
// repairs the state. This detects reloads without polling HAProxy a second
// time or issuing one Runtime API command per route every heartbeat.
func (q *QuotaReconciler) ObserveRuntime(stats HAProxyRuntimeStats) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for key, expectedMaintenance := range q.applied {
		backend, server, ok := strings.Cut(key, "/")
		if !ok {
			delete(q.applied, key)
			continue
		}
		servers, ok := stats.Servers[backend]
		if !ok {
			delete(q.applied, key)
			continue
		}
		observed, ok := servers[server]
		if !ok || runtimeServerInMaintenance(observed.Status) != expectedMaintenance {
			delete(q.applied, key)
		}
	}
}

func runtimeServerInMaintenance(status string) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(status)), "MAINT")
}

func (q *QuotaReconciler) Snapshot() map[string]bool {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.applied) == 0 {
		return nil
	}
	result := make(map[string]bool, len(q.applied))
	for key, blocked := range q.applied {
		result[key] = blocked
	}
	return result
}

// Reset forgets runtime state after any config apply attempt. A successful
// HAProxy reload recreates servers in their config-default state; a failed
// apply may also have reloaded a rollback. The next authoritative assignment
// must therefore issue the runtime commands again.
func (q *QuotaReconciler) Reset() {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.applied = nil
}

func validateQuotaAssignment(assignment QuotaAssignment) ([]QuotaBackendPolicy, error) {
	month, err := time.Parse("2006-01", assignment.Month)
	if err != nil || month.Format("2006-01") != assignment.Month {
		return nil, errors.New("invalid quota assignment month")
	}
	if len(assignment.Policies) > maxQuotaBackendPolicies {
		return nil, errors.New("quota assignment exceeds policy limit")
	}

	policies := append([]QuotaBackendPolicy(nil), assignment.Policies...)
	seenRoutes := make(map[string]struct{}, len(policies))
	seenRuntime := make(map[string]struct{}, len(policies))
	for index := range policies {
		policy := &policies[index]
		if err := normalizeAndValidateQuotaWindow(assignment.Month, policy); err != nil {
			return nil, err
		}
		if !validCanonicalUUID(policy.RouteID) {
			return nil, errors.New("invalid quota route ID")
		}
		if !quotaRuntimeObjectsMatchRoute(policy.RouteID, policy.Backend, policy.Server) {
			return nil, errors.New("quota runtime object does not match route ID")
		}
		if policy.Action != quotaActionObserve && policy.Action != quotaActionBlockNew {
			return nil, errors.New("invalid quota action")
		}
		if policy.UsedBytes < 0 || (policy.LimitBytes != nil && *policy.LimitBytes <= 0) {
			return nil, errors.New("invalid quota byte counters")
		}
		if policy.Action == quotaActionBlockNew && policy.LimitBytes == nil {
			return nil, errors.New("block_new quota action requires a limit")
		}
		shouldBlock := policy.Action == quotaActionBlockNew && policy.LimitBytes != nil && policy.UsedBytes >= *policy.LimitBytes
		if policy.Block != shouldBlock {
			return nil, errors.New("quota block state does not match usage and limit")
		}
		if _, exists := seenRoutes[policy.RouteID]; exists {
			return nil, errors.New("duplicate quota route policy")
		}
		seenRoutes[policy.RouteID] = struct{}{}
		key := quotaRuntimeKey(policy.Backend, policy.Server)
		if _, exists := seenRuntime[key]; exists {
			return nil, errors.New("duplicate quota runtime policy")
		}
		seenRuntime[key] = struct{}{}
	}

	sort.Slice(policies, func(i, j int) bool {
		if policies[i].Backend != policies[j].Backend {
			return policies[i].Backend < policies[j].Backend
		}
		return policies[i].Server < policies[j].Server
	})
	return policies, nil
}

func quotaRuntimeObjectsMatchRoute(routeID, backend, server string) bool {
	compactID := strings.ReplaceAll(routeID, "-", "")
	shortID := compactID
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}
	return (backend == quotaBackendPrefix+shortID && server == quotaServerPrefix+shortID) ||
		(backend == quotaBackendPrefix+compactID && server == quotaServerPrefix+compactID) ||
		(backend == legacyQuotaBackendPrefix+compactID && server == legacyQuotaServerPrefix+compactID)
}

func validCanonicalUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for index := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		char := value[index]
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func quotaRuntimeKey(backend, server string) string {
	return backend + "/" + server
}
