package panel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This test is intentionally opt-in because unit-test hosts do not always
// provide PostgreSQL. scripts/test-migrations.sh supplies the schema gate;
// CI/live validation can point this test at a disposable migrated database.
func TestPGStoreRouteLifecycleIntegration(t *testing.T) {
	databaseURL := os.Getenv("NODEFLOW_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NODEFLOW_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, pool.Ping(ctx))
	var migrated bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version='000016')`).Scan(&migrated))
	require.True(t, migrated, "database must include migration 000016")

	store := NewPGStore(pool)
	name := fmt.Sprintf("route-lifecycle-%d", time.Now().UnixNano())
	node, err := store.CreateNode(ctx, name, "198.18.0.1", map[string]any{"firewall_apply_allowed": true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.DeleteNode(context.Background(), node.ID) })
	policy, err := store.GetFirewallPolicy(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, "apply", policy.Mode)
	token := "route-lifecycle-integration-token"
	sum := sha256.Sum256([]byte(token))
	_, err = store.CreateEnrollmentToken(ctx, node.ID, hex.EncodeToString(sum[:]), "integration", time.Now().Add(time.Hour))
	require.NoError(t, err)

	base := RouteSpec{
		ListenerIP: "*", ListenerPort: 443, SNIs: []string{"a.example"}, Hostname: "a.example",
		TargetType: "tcp", TargetHost: "192.0.2.10", TargetPort: 443,
		ProxyProtocol: "none", QuotaAction: "observe", Enabled: true,
	}
	route, err := store.CreateRoute(ctx, node.ID, base)
	require.NoError(t, err)
	assert.False(t, route.Enabled)
	assert.False(t, route.Deployed)
	assert.Equal(t, "draft", route.DeploymentState)
	assert.Equal(t, int64(1), route.Version)
	assert.Nil(t, route.DesiredRevision)
	var revisionCount int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM config_revisions WHERE node_id=$1`, node.ID).Scan(&revisionCount))
	assert.Zero(t, revisionCount, "draft creation must not create or assign config")

	enable := routeAsSpec(route, true)
	enable.ExpectedVersion = revision(route.Version)
	route, err = store.UpdateRoute(ctx, node.ID, route.ID, enable)
	require.NoError(t, err)
	require.NotNil(t, route.DesiredRevision)
	firstRevision := *route.DesiredRevision
	assert.Equal(t, "pending", route.DeploymentState)
	assert.False(t, route.Deployed)
	assert.NotEqual(t, route.DesiredFingerprint, route.DeployedFingerprint)
	firstConfig, err := store.GetConfigRevision(ctx, node.ID, firstRevision)
	require.NoError(t, err)
	state, err := store.IngestApplyReport(ctx, token, ApplyReport{Revision: firstRevision, State: "applied"})
	require.NoError(t, err)
	assert.Equal(t, "in_sync", state.State)
	route, err = store.GetRoute(ctx, node.ID, route.ID)
	require.NoError(t, err)
	assert.Equal(t, "active", route.DeploymentState)
	assert.True(t, route.Deployed)
	assert.Equal(t, route.DesiredFingerprint, route.DeployedFingerprint)

	edit := routeAsSpec(route, true)
	edit.TargetPort = 8443
	edit.ExpectedVersion = revision(route.Version)
	route, err = store.UpdateRoute(ctx, node.ID, route.ID, edit)
	require.NoError(t, err)
	failedRevision := *route.DesiredRevision
	assert.True(t, route.Deployed, "old actual route remains active while edited desired spec is pending")
	assert.NotEqual(t, route.DesiredFingerprint, route.DeployedFingerprint)
	_, err = store.IngestApplyReport(ctx, token, ApplyReport{
		Revision: failedRevision, State: "failed", ActualRevision: &firstRevision,
		Error: "validation_failed", RollbackAttempted: true, RollbackSucceeded: boolPointer(true),
	})
	require.NoError(t, err)
	route, err = store.GetRoute(ctx, node.ID, route.ID)
	require.NoError(t, err)
	assert.Equal(t, "failed", route.DeploymentState)
	assert.Equal(t, "validation_failed", route.DeploymentError)
	assert.True(t, route.Deployed)
	heartbeat, err := store.IngestHeartbeat(ctx, token, Heartbeat{
		Version: "integration", Status: "online", Metrics: map[string]any{},
		ActualRevision: &firstRevision, ConfigSHA256: firstConfig.SHA256,
	})
	require.NoError(t, err)
	assert.Nil(t, heartbeat.Assignment, "failed revision must not be reissued until a new mutation")

	retry := routeAsSpec(route, true)
	retry.ExpectedVersion = revision(route.Version)
	route, err = store.UpdateRoute(ctx, node.ID, route.ID, retry)
	require.NoError(t, err)
	retryRevision := *route.DesiredRevision
	assert.Greater(t, retryRevision, failedRevision)
	_, err = store.IngestApplyReport(ctx, token, ApplyReport{Revision: retryRevision, State: "applied"})
	require.NoError(t, err)
	route, err = store.GetRoute(ctx, node.ID, route.ID)
	require.NoError(t, err)
	assert.Equal(t, "active", route.DeploymentState)
	assert.Equal(t, route.DesiredFingerprint, route.DeployedFingerprint)

	disable := routeAsSpec(route, false)
	disable.ExpectedVersion = revision(route.Version)
	route, err = store.UpdateRoute(ctx, node.ID, route.ID, disable)
	require.NoError(t, err)
	disableFailedRevision := *route.DesiredRevision
	_, err = store.IngestApplyReport(ctx, token, ApplyReport{
		Revision: disableFailedRevision, State: "failed", ActualRevision: &retryRevision,
		Error: "reload_failed", RollbackAttempted: true, RollbackSucceeded: boolPointer(true),
	})
	require.NoError(t, err)
	route, err = store.GetRoute(ctx, node.ID, route.ID)
	require.NoError(t, err)
	assert.False(t, route.Enabled)
	assert.True(t, route.Deployed)
	assert.Equal(t, "failed", route.DeploymentState)
	disableRetry := routeAsSpec(route, false)
	disableRetry.ExpectedVersion = revision(route.Version)
	route, err = store.UpdateRoute(ctx, node.ID, route.ID, disableRetry)
	require.NoError(t, err)
	disableRevision := *route.DesiredRevision
	zeroConfig, err := store.GetConfigRevision(ctx, node.ID, disableRevision)
	require.NoError(t, err)
	assert.NotContains(t, zeroConfig.Config, "\nfrontend ", "last-route disable must publish a valid zero-listener base config")
	_, err = store.IngestApplyReport(ctx, token, ApplyReport{Revision: disableRevision, State: "applied"})
	require.NoError(t, err)
	route, err = store.GetRoute(ctx, node.ID, route.ID)
	require.NoError(t, err)
	assert.Equal(t, "disabled", route.DeploymentState)
	assert.False(t, route.Deployed)
	deleted, err := store.DeleteRoute(ctx, node.ID, route.ID, revision(route.Version))
	require.NoError(t, err)
	assert.False(t, deleted.Pending)
	_, err = store.GetRoute(ctx, node.ID, route.ID)
	assert.ErrorIs(t, err, ErrNotFound)

	// Optimistic concurrency: both callers observed version 1, but the node lock
	// and version check permit only one intent mutation.
	concurrentSpec := base
	concurrentSpec.SNIs, concurrentSpec.Hostname = []string{"c.example"}, "c.example"
	concurrent, err := store.CreateRoute(ctx, node.ID, concurrentSpec)
	require.NoError(t, err)
	var wait sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for port := 9001; port <= 9002; port++ {
		wait.Add(1)
		go func(port int) {
			defer wait.Done()
			spec := routeAsSpec(concurrent, false)
			spec.TargetPort = port
			spec.ExpectedVersion = revision(concurrent.Version)
			_, updateErr := store.UpdateRoute(context.Background(), node.ID, concurrent.ID, spec)
			errorsSeen <- updateErr
		}(port)
	}
	wait.Wait()
	close(errorsSeen)
	successes, conflicts := 0, 0
	for updateErr := range errorsSeen {
		switch {
		case updateErr == nil:
			successes++
		case errors.Is(updateErr, ErrRouteVersionConflict):
			conflicts++
		default:
			require.NoError(t, updateErr)
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, conflicts)

	// Conflicting disabled drafts are valid control-plane state. Only enabling
	// the second route is rejected, while the node lock keeps the check and
	// immutable revision creation in one serialized transaction.
	draftSpec := base
	draftSpec.ListenerPort = 10443
	draftSpec.SNIs, draftSpec.Hostname = []string{"duplicate.example"}, "duplicate.example"
	firstDraft, err := store.CreateRoute(ctx, node.ID, draftSpec)
	require.NoError(t, err)
	secondDraft, err := store.CreateRoute(ctx, node.ID, draftSpec)
	require.NoError(t, err, "overlapping disabled drafts must not be blocked by database uniqueness")
	firstEnable := routeAsSpec(firstDraft, true)
	firstEnable.ExpectedVersion = revision(firstDraft.Version)
	firstDraft, err = store.UpdateRoute(ctx, node.ID, firstDraft.ID, firstEnable)
	require.NoError(t, err)
	require.NotNil(t, firstDraft.DesiredRevision)
	secondEnable := routeAsSpec(secondDraft, true)
	secondEnable.ExpectedVersion = revision(secondDraft.Version)
	_, err = store.UpdateRoute(ctx, node.ID, secondDraft.ID, secondEnable)
	var routeSetErr *RouteSetError
	require.ErrorAs(t, err, &routeSetErr)
	secondDraft, err = store.GetRoute(ctx, node.ID, secondDraft.ID)
	require.NoError(t, err)
	assert.False(t, secondDraft.Enabled)
	assert.Equal(t, int64(1), secondDraft.Version, "failed enable must roll back the intent mutation")

	// Pending delete survives a skipped revision and is finalized by a verified
	// heartbeat for a later desired config that also omits the route.
	aSpec := base
	aSpec.SNIs, aSpec.Hostname = []string{"delete.example"}, "delete.example"
	aSpec.ListenerPort = 444
	a, err := store.CreateRoute(ctx, node.ID, aSpec)
	require.NoError(t, err)
	aEnable := routeAsSpec(a, true)
	aEnable.ExpectedVersion = revision(a.Version)
	a, err = store.UpdateRoute(ctx, node.ID, a.ID, aEnable)
	require.NoError(t, err)
	aActiveRevision := *a.DesiredRevision
	_, err = store.IngestApplyReport(ctx, token, ApplyReport{Revision: aActiveRevision, State: "applied"})
	require.NoError(t, err)
	a, err = store.GetRoute(ctx, node.ID, a.ID)
	require.NoError(t, err)
	deletion, err := store.DeleteRoute(ctx, node.ID, a.ID, revision(a.Version))
	require.NoError(t, err)
	require.True(t, deletion.Pending)
	deleteRevision := *deletion.Route.DesiredRevision

	bSpec := base
	bSpec.SNIs, bSpec.Hostname = []string{"b.example"}, "b.example"
	bSpec.ListenerPort = 445
	b, err := store.CreateRoute(ctx, node.ID, bSpec)
	require.NoError(t, err)
	bEnable := routeAsSpec(b, true)
	bEnable.ExpectedVersion = revision(b.Version)
	b, err = store.UpdateRoute(ctx, node.ID, b.ID, bEnable)
	require.NoError(t, err)
	laterRevision := *b.DesiredRevision
	assert.Greater(t, laterRevision, deleteRevision)
	laterConfig, err := store.GetConfigRevision(ctx, node.ID, laterRevision)
	require.NoError(t, err)
	_, err = store.IngestHeartbeat(ctx, token, Heartbeat{
		Version: "integration", Status: "online", Metrics: map[string]any{},
		ActualRevision: &laterRevision, ConfigSHA256: laterConfig.SHA256,
	})
	require.NoError(t, err)
	_, err = store.GetRoute(ctx, node.ID, a.ID)
	assert.ErrorIs(t, err, ErrNotFound)

	// An older duplicate report cannot regress actual revision or route state.
	b, err = store.GetRoute(ctx, node.ID, b.ID)
	require.NoError(t, err)
	bEdit := routeAsSpec(b, true)
	bEdit.TargetPort = 9443
	bEdit.ExpectedVersion = revision(b.Version)
	b, err = store.UpdateRoute(ctx, node.ID, b.ID, bEdit)
	require.NoError(t, err)
	newerRevision := *b.DesiredRevision
	_, err = store.IngestApplyReport(ctx, token, ApplyReport{Revision: newerRevision, State: "applied"})
	require.NoError(t, err)
	_, err = store.IngestApplyReport(ctx, token, ApplyReport{Revision: laterRevision, State: "applied"})
	require.NoError(t, err)
	configState, err := store.GetConfigState(ctx, node.ID)
	require.NoError(t, err)
	require.NotNil(t, configState.ActualRevision)
	assert.Equal(t, newerRevision, *configState.ActualRevision)
	b, err = store.GetRoute(ctx, node.ID, b.ID)
	require.NoError(t, err)
	assert.Equal(t, newerRevision, *b.AppliedRevision)
	assert.Equal(t, "active", b.DeploymentState)

	// Failed active delete remains visible and a second DELETE publishes a new
	// revision instead of stranding delete_pending.
	failedDelete, err := store.DeleteRoute(ctx, node.ID, b.ID, revision(b.Version))
	require.NoError(t, err)
	failedDeleteRevision := *failedDelete.Route.DesiredRevision
	_, err = store.IngestApplyReport(ctx, token, ApplyReport{
		Revision: failedDeleteRevision, State: "failed", ActualRevision: &newerRevision,
		Error: "reload_failed", RollbackAttempted: true, RollbackSucceeded: boolPointer(true),
	})
	require.NoError(t, err)
	b, err = store.GetRoute(ctx, node.ID, b.ID)
	require.NoError(t, err)
	assert.True(t, b.DeletePending)
	assert.True(t, b.Deployed)
	assert.Equal(t, "failed", b.DeploymentState)
	retriedDelete, err := store.DeleteRoute(ctx, node.ID, b.ID, revision(b.Version))
	require.NoError(t, err)
	require.True(t, retriedDelete.Pending)
	assert.Greater(t, *retriedDelete.Route.DesiredRevision, failedDeleteRevision)
	_, err = store.IngestApplyReport(ctx, token, ApplyReport{Revision: *retriedDelete.Route.DesiredRevision, State: "applied"})
	require.NoError(t, err)
	_, err = store.GetRoute(ctx, node.ID, b.ID)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestPGStoreRouteRevisionRefreshesAllDesiredFingerprints(t *testing.T) {
	databaseURL := os.Getenv("NODEFLOW_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NODEFLOW_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, pool.Ping(ctx))
	var migrated bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version='000018')`).Scan(&migrated))
	require.True(t, migrated, "database must include migration 000018")

	store := NewPGStore(pool)
	node, err := store.CreateNode(ctx, fmt.Sprintf("fingerprint-refresh-%d", time.Now().UnixNano()), "198.18.0.2", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.DeleteNode(context.Background(), node.ID) })

	firstDraft, err := store.CreateRoute(ctx, node.ID, RouteSpec{
		ListenerIP: "*", ListenerPort: 14443, SNIs: []string{"first.example"}, Hostname: "first.example",
		TargetType: "tcp", TargetHost: "192.0.2.10", TargetPort: 443,
		ProxyProtocol: "none", QuotaAction: "observe",
	})
	require.NoError(t, err)
	firstSpec := routeAsSpec(firstDraft, true)
	firstSpec.ExpectedVersion = revision(firstDraft.Version)
	first, err := store.UpdateRoute(ctx, node.ID, firstDraft.ID, firstSpec)
	require.NoError(t, err)
	expectedFingerprint := routeSpecFingerprint(routeAsSpec(first, true))
	assert.Equal(t, expectedFingerprint, first.DesiredFingerprint)

	_, err = pool.Exec(ctx, `UPDATE routes SET desired_fingerprint=repeat('0',64) WHERE id=$1`, first.ID)
	require.NoError(t, err)
	first, err = store.GetRoute(ctx, node.ID, first.ID)
	require.NoError(t, err)
	assert.NotEqual(t, expectedFingerprint, first.DesiredFingerprint, "fixture must start stale")

	secondDraft, err := store.CreateRoute(ctx, node.ID, RouteSpec{
		ListenerIP: "*", ListenerPort: 24443, SNIs: []string{"second.example"}, Hostname: "second.example",
		TargetType: "tcp", TargetHost: "192.0.2.11", TargetPort: 443,
		ProxyProtocol: "none", QuotaAction: "observe",
	})
	require.NoError(t, err)
	secondSpec := routeAsSpec(secondDraft, true)
	secondSpec.ExpectedVersion = revision(secondDraft.Version)
	second, err := store.UpdateRoute(ctx, node.ID, secondDraft.ID, secondSpec)
	require.NoError(t, err)
	require.NotNil(t, second.DesiredRevision)

	first, err = store.GetRoute(ctx, node.ID, first.ID)
	require.NoError(t, err)
	require.NotNil(t, first.DesiredRevision)
	assert.Equal(t, *second.DesiredRevision, *first.DesiredRevision)
	assert.Equal(t, expectedFingerprint, first.DesiredFingerprint)

	revisionRecord, err := store.GetConfigRevision(ctx, node.ID, *second.DesiredRevision)
	require.NoError(t, err)
	fingerprints, ok := revisionRecord.Metadata["route_fingerprints"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, expectedFingerprint, fingerprints[first.ID])
}

func boolPointer(value bool) *bool { return &value }
