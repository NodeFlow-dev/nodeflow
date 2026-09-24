package panel

import (
	"context"
	"log/slog"
	"time"
)

const (
	defaultAuditCleanupPollInterval = time.Hour
	defaultAuditCleanupTimeout      = 30 * time.Second
	auditCleanupBatchSize           = int64(5000)
	auditCleanupBacklogRetry        = time.Minute
)

type auditRetentionStore interface {
	CleanupAudit(context.Context) error
}

// RunAuditRetentionCleaner performs one startup sweep and then polls at a
// bounded interval. Every sweep has its own deadline, runs synchronously (no
// overlap), and still relies on the persisted PostgreSQL gate for replicas.
// It returns only after ctx is canceled.
func RunAuditRetentionCleaner(ctx context.Context, store auditRetentionStore) {
	runAuditRetentionCleaner(ctx, store, defaultAuditCleanupPollInterval, defaultAuditCleanupTimeout)
}

func runAuditRetentionCleaner(ctx context.Context, store auditRetentionStore, interval, timeout time.Duration) {
	if interval <= 0 {
		interval = defaultAuditCleanupPollInterval
	}
	if timeout <= 0 {
		timeout = defaultAuditCleanupTimeout
	}
	sweep := func() {
		sweepCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if err := store.CleanupAudit(sweepCtx); err != nil && ctx.Err() == nil {
			slog.Warn("audit retention cleanup failed", "error", err)
		}
	}

	sweep()
	ticker := time.NewTicker(interval)
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
