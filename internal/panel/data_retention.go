package panel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Data retention for time-series and ledger tables that grow with every
// heartbeat or apply report. Deletes run outside the heartbeat transaction in
// small ctid batches so no single statement holds locks for long.
const (
	defaultDataRetentionInterval    = 10 * time.Minute
	defaultDataRetentionTimeout     = 2 * time.Minute
	dataRetentionBatchSize          = 5000
	dataRetentionMaxBatchesPerTable = 40

	defaultQuotaUsageRetentionDays     = 93
	defaultApplyReportRetentionDays    = 90
	defaultTrafficMonthlyRetentionMons = 24

	minQuotaUsageRetentionDays     = 7
	minApplyReportRetentionDays    = 1
	minTrafficMonthlyRetentionMons = 2
)

// DataRetentionPolicy configures the background cleaner. A zero value for an
// optional table disables its cleanup. Rate samples are always kept for
// trafficRateRetention because the 30d history view depends on them.
type DataRetentionPolicy struct {
	Interval                    time.Duration
	QuotaUsageRetentionDays     int
	ApplyReportRetentionDays    int
	TrafficMonthlyRetentionMons int
}

// DefaultDataRetentionPolicy returns conservative defaults.
func DefaultDataRetentionPolicy() DataRetentionPolicy {
	return DataRetentionPolicy{
		Interval:                    defaultDataRetentionInterval,
		QuotaUsageRetentionDays:     defaultQuotaUsageRetentionDays,
		ApplyReportRetentionDays:    defaultApplyReportRetentionDays,
		TrafficMonthlyRetentionMons: defaultTrafficMonthlyRetentionMons,
	}
}

// LoadDataRetentionPolicy reads optional overrides from the environment:
//
//	PANEL_RETENTION_INTERVAL              Go duration, >= 1m (default 10m)
//	PANEL_RETENTION_QUOTA_USAGE_DAYS      0 disables, else >= 7 (default 93)
//	PANEL_RETENTION_APPLY_REPORTS_DAYS    0 disables, else >= 1 (default 90)
//	PANEL_RETENTION_TRAFFIC_MONTHLY_MONTHS 0 disables, else >= 2 (default 24)
func LoadDataRetentionPolicy() (DataRetentionPolicy, error) {
	policy := DefaultDataRetentionPolicy()
	if raw := strings.TrimSpace(os.Getenv("PANEL_RETENTION_INTERVAL")); raw != "" {
		value, err := time.ParseDuration(raw)
		if err != nil || value < time.Minute {
			return DataRetentionPolicy{}, fmt.Errorf("PANEL_RETENTION_INTERVAL must be a duration of at least 1m")
		}
		policy.Interval = value
	}
	var err error
	if policy.QuotaUsageRetentionDays, err = retentionEnvInt("PANEL_RETENTION_QUOTA_USAGE_DAYS", policy.QuotaUsageRetentionDays, minQuotaUsageRetentionDays); err != nil {
		return DataRetentionPolicy{}, err
	}
	if policy.ApplyReportRetentionDays, err = retentionEnvInt("PANEL_RETENTION_APPLY_REPORTS_DAYS", policy.ApplyReportRetentionDays, minApplyReportRetentionDays); err != nil {
		return DataRetentionPolicy{}, err
	}
	if policy.TrafficMonthlyRetentionMons, err = retentionEnvInt("PANEL_RETENTION_TRAFFIC_MONTHLY_MONTHS", policy.TrafficMonthlyRetentionMons, minTrafficMonthlyRetentionMons); err != nil {
		return DataRetentionPolicy{}, err
	}
	return policy, nil
}

func retentionEnvInt(name string, fallback, minimum int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 || (value > 0 && value < minimum) || value > 100000 {
		return 0, fmt.Errorf("%s must be 0 (disabled) or an integer >= %d", name, minimum)
	}
	return value, nil
}

type dataRetentionStore interface {
	CleanupRetention(context.Context, DataRetentionPolicy) (DataRetentionResult, error)
}

// DataRetentionResult reports rows removed by one sweep.
type DataRetentionResult struct {
	Ran     bool
	Deleted map[string]int64
	Backlog bool
}

// RunDataRetentionCleaner sweeps once at startup and then on every interval
// until ctx is canceled. Sweeps never overlap and each has its own deadline.
func RunDataRetentionCleaner(ctx context.Context, store dataRetentionStore, policy DataRetentionPolicy) {
	runDataRetentionCleaner(ctx, store, policy, defaultDataRetentionTimeout)
}

func runDataRetentionCleaner(ctx context.Context, store dataRetentionStore, policy DataRetentionPolicy, timeout time.Duration) {
	if policy.Interval <= 0 {
		policy.Interval = defaultDataRetentionInterval
	}
	if timeout <= 0 {
		timeout = defaultDataRetentionTimeout
	}
	sweep := func() {
		sweepCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		result, err := store.CleanupRetention(sweepCtx, policy)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("data retention cleanup failed", "error", err)
			}
			return
		}
		for table, rows := range result.Deleted {
			if rows > 0 {
				slog.Info("data retention cleanup", "table", table, "deleted", rows)
			}
		}
	}
	sweep()
	ticker := time.NewTicker(policy.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}

type retentionTarget struct {
	table string
	// where selects expired rows; $1 is the cutoff parameter.
	where  string
	cutoff any
}

func dataRetentionTargets(now time.Time, policy DataRetentionPolicy) []retentionTarget {
	now = now.UTC()
	rateCutoff := now.Add(-trafficRateRetention)
	targets := []retentionTarget{
		{table: "node_traffic_rate_samples", where: "sampled_at < $1", cutoff: rateCutoff},
		{table: "route_traffic_rate_samples", where: "sampled_at < $1", cutoff: rateCutoff},
	}
	if policy.QuotaUsageRetentionDays > 0 {
		// Only windows that ended long ago; current enforcement windows always
		// have window_end in the future.
		targets = append(targets, retentionTarget{
			table: "traffic_quota_usage", where: "window_end < $1",
			cutoff: now.AddDate(0, 0, -policy.QuotaUsageRetentionDays),
		})
	}
	if policy.ApplyReportRetentionDays > 0 {
		targets = append(targets, retentionTarget{
			table: "config_apply_reports", where: "received_at < $1",
			cutoff: now.AddDate(0, 0, -policy.ApplyReportRetentionDays),
		})
	}
	if policy.TrafficMonthlyRetentionMons > 0 {
		currentMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		targets = append(targets, retentionTarget{
			table: "traffic_monthly", where: "month < $1",
			cutoff: currentMonth.AddDate(0, -policy.TrafficMonthlyRetentionMons, 0),
		})
	}
	return targets
}

// CleanupRetention is gated by traffic_rate_retention_state so that multiple
// Panel replicas perform at most one sweep per interval. Each batch is its own
// short autocommit statement; heartbeat transactions are never involved.
func (s *PGStore) CleanupRetention(ctx context.Context, policy DataRetentionPolicy) (DataRetentionResult, error) {
	if policy.Interval <= 0 {
		policy.Interval = defaultDataRetentionInterval
	}
	result := DataRetentionResult{Deleted: map[string]int64{}}
	// Claim the gate slightly before the interval elapses so tick jitter does
	// not skip every other sweep.
	gate := policy.Interval - policy.Interval/10
	var now time.Time
	err := s.pool.QueryRow(ctx, `
		UPDATE traffic_rate_retention_state
		SET last_cleanup_at=clock_timestamp()
		WHERE singleton=true AND last_cleanup_at < clock_timestamp() - ($1 * interval '1 second')
		RETURNING last_cleanup_at`, int64(gate/time.Second)).Scan(&now)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.Ran = true
	for _, target := range dataRetentionTargets(now, policy) {
		deleted, backlog, err := s.deleteExpiredBatches(ctx, target)
		result.Deleted[target.table] = deleted
		if err != nil {
			return result, fmt.Errorf("retention %s: %w", target.table, err)
		}
		result.Backlog = result.Backlog || backlog
	}
	if result.Backlog {
		// Reopen the gate so the next tick continues draining.
		if _, err = s.pool.Exec(ctx, `
			UPDATE traffic_rate_retention_state SET last_cleanup_at='-infinity'::timestamptz
			WHERE singleton=true`); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (s *PGStore) deleteExpiredBatches(ctx context.Context, target retentionTarget) (int64, bool, error) {
	// ctid batching bounds each statement; the inner SELECT uses the table's
	// retention index on the filtered column.
	query := `DELETE FROM ` + target.table + ` WHERE ctid = ANY(ARRAY(
		SELECT ctid FROM ` + target.table + ` WHERE ` + target.where + ` LIMIT $2))`
	var total int64
	for batch := 0; batch < dataRetentionMaxBatchesPerTable; batch++ {
		tag, err := s.pool.Exec(ctx, query, target.cutoff, dataRetentionBatchSize)
		if err != nil {
			return total, false, err
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < dataRetentionBatchSize {
			return total, false, nil
		}
	}
	return total, true, nil
}
