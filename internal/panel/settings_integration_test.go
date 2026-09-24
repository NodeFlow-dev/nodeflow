package panel

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPanelSettingsPersistenceAndGatedAuditRetention(t *testing.T) {
	databaseURL := os.Getenv("NODEFLOW_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NODEFLOW_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	defer pool.Close()
	store := NewPGStore(pool)

	original, err := store.GetPanelSettings(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = store.UpdatePanelSettings(cleanupCtx, original)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM audit_log WHERE action LIKE 'settings.integration.%'`)
	})

	updated, err := store.UpdatePanelSettings(ctx, PanelSettings{
		Theme:                    "system",
		Accent:                   "#22B8CF",
		InactivityTimeoutMinutes: 45,
		MaxSessions:              7,
		AuditRetentionDays:       7,
	})
	require.NoError(t, err)
	assert.Equal(t, "system", updated.Theme)
	assert.Equal(t, "#22B8CF", updated.Accent)
	assert.Equal(t, 45, updated.InactivityTimeoutMinutes)
	assert.Equal(t, 7, updated.MaxSessions)
	assert.Equal(t, 7, updated.AuditRetentionDays)

	loaded, err := store.GetPanelSettings(ctx)
	require.NoError(t, err)
	assert.Equal(t, updated, loaded)

	_, err = pool.Exec(ctx, `
		UPDATE panel_settings SET next_audit_cleanup_at=clock_timestamp() WHERE singleton=true;
		INSERT INTO audit_log(actor_type,action,resource_type,details,created_at)
		VALUES
		  ('test','settings.integration.expired','test','{}',clock_timestamp()-interval '8 days'),
		  ('test','settings.integration.recent','test','{}',clock_timestamp()-interval '1 day')`)
	require.NoError(t, err)
	require.NoError(t, store.AppendAudit(ctx, AuditEvent{
		ActorType: "test", Action: "settings.integration.trigger", ResourceType: "test", Details: map[string]any{},
	}))
	var expiredCount, recentCount int
	// Retention is independent from append: a cleanup failure can no longer
	// roll back or suppress the audit insert itself.
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='settings.integration.expired'`).Scan(&expiredCount))
	assert.Equal(t, 1, expiredCount)
	require.NoError(t, store.CleanupAudit(ctx))

	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='settings.integration.expired'`).Scan(&expiredCount))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action IN ('settings.integration.recent','settings.integration.trigger')`).Scan(&recentCount))
	assert.Zero(t, expiredCount)
	assert.Equal(t, 2, recentCount)

	// The first cleanup advances the persisted gate by six hours. An expired row
	// inserted during that window remains until a later gated sweep.
	_, err = pool.Exec(ctx, `
		INSERT INTO audit_log(actor_type,action,resource_type,details,created_at)
		VALUES ('test','settings.integration.gated','test','{}',clock_timestamp()-interval '8 days')`)
	require.NoError(t, err)
	require.NoError(t, store.AppendAudit(ctx, AuditEvent{
		ActorType: "test", Action: "settings.integration.second", ResourceType: "test", Details: map[string]any{},
	}))
	require.NoError(t, store.CleanupAudit(ctx))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='settings.integration.gated'`).Scan(&expiredCount))
	assert.Equal(t, 1, expiredCount)
}
