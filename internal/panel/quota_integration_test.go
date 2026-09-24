package panel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPGStoreQuotaTrafficSkipsStaleRevisionSnapshot(t *testing.T) {
	databaseURL := os.Getenv("NODEFLOW_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NODEFLOW_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	defer pool.Close()
	var migrated bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version='000015')`).Scan(&migrated))
	require.True(t, migrated, "database must include migration 000015")

	store := NewPGStore(pool)
	node, err := store.CreateNode(ctx, fmt.Sprintf("quota-stale-%d", time.Now().UnixNano()), "198.18.0.2", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.DeleteNode(context.Background(), node.ID) })
	token := fmt.Sprintf("quota-stale-token-%d", time.Now().UnixNano())
	sum := sha256.Sum256([]byte(token))
	_, err = store.CreateEnrollmentToken(ctx, node.ID, hex.EncodeToString(sum[:]), "quota-test", time.Now().Add(time.Hour))
	require.NoError(t, err)

	route, err := store.CreateRoute(ctx, node.ID, RouteSpec{
		ListenerIP: "*", ListenerPort: 18446, SNIs: []string{"quota.example"}, Hostname: "quota.example",
		TargetType: "tcp", TargetHost: "192.0.2.10", TargetPort: 443,
		ProxyProtocol: "none", QuotaAction: "observe", QuotaPeriod: "hourly",
	})
	require.NoError(t, err)
	enable := routeAsSpec(route, true)
	enable.ExpectedVersion = revision(route.Version)
	route, err = store.UpdateRoute(ctx, node.ID, route.ID, enable)
	require.NoError(t, err)
	require.NotNil(t, route.DesiredRevision)
	revisionOne := *route.DesiredRevision
	configOne, err := store.GetConfigRevision(ctx, node.ID, revisionOne)
	require.NoError(t, err)
	_, err = store.IngestApplyReport(ctx, token, ApplyReport{Revision: revisionOne, State: "applied"})
	require.NoError(t, err)

	backend := RouteBackendKey(route.ID)
	firstInstanceID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	secondInstanceID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	firstStartedAt := time.Now().UTC().Truncate(time.Microsecond)
	secondStartedAt := firstStartedAt.Add(time.Second)
	_, err = store.IngestHeartbeat(ctx, token, quotaOrderedHeartbeat(
		firstInstanceID, firstStartedAt, 1, revisionOne, configOne.SHA256, backend, 100,
	))
	require.NoError(t, err)

	edit := routeAsSpec(route, true)
	edit.TargetPort = 8443
	edit.ExpectedVersion = revision(route.Version)
	route, err = store.UpdateRoute(ctx, node.ID, route.ID, edit)
	require.NoError(t, err)
	require.NotNil(t, route.DesiredRevision)
	revisionTwo := *route.DesiredRevision
	configTwo, err := store.GetConfigRevision(ctx, node.ID, revisionTwo)
	require.NoError(t, err)
	_, err = store.IngestApplyReport(ctx, token, ApplyReport{Revision: revisionTwo, State: "applied"})
	require.NoError(t, err)
	_, err = store.IngestHeartbeat(ctx, token, quotaOrderedHeartbeat(
		firstInstanceID, firstStartedAt, 2, revisionTwo, configTwo.SHA256, backend, 120,
	))
	require.NoError(t, err)

	// Same-revision out-of-order sample must not be treated as a reset.
	_, err = store.IngestHeartbeat(ctx, token, quotaOrderedHeartbeat(
		firstInstanceID, firstStartedAt, 1, revisionTwo, configTwo.SHA256, backend, 50,
	))
	require.NoError(t, err)

	// A restarted Agent begins a new ordered stream. A delayed sample from the
	// older process is rejected even when its sequence and counter are larger.
	_, err = store.IngestHeartbeat(ctx, token, quotaOrderedHeartbeat(
		secondInstanceID, secondStartedAt, 1, revisionTwo, configTwo.SHA256, backend, 130,
	))
	require.NoError(t, err)
	var acceptedLastSeen time.Time
	require.NoError(t, pool.QueryRow(ctx, `SELECT last_seen_at FROM nodes WHERE id=$1`, node.ID).Scan(&acceptedLastSeen))
	stale := quotaOrderedHeartbeat(firstInstanceID, firstStartedAt, 3, revisionOne, configOne.SHA256, backend, 200)
	stale.Status = "error"
	_, err = store.IngestHeartbeat(ctx, token, stale)
	require.NoError(t, err)

	var accumulated, hourlyUsage, dailyUsage, latestRaw, actualRevision, latestSequence int64
	var latestInstanceID, nodeStatus string
	var finalLastSeen time.Time
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT accumulated_bytes_in FROM traffic_counter_state
		WHERE node_id=$1 AND scope='backend' AND proxy_name=$2`, node.ID, backend).Scan(&accumulated))
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT COALESCE(sum(bytes_in),0) FROM traffic_quota_usage
		WHERE node_id=$1 AND proxy_name=$2 AND period='hourly'`, node.ID, backend).Scan(&hourlyUsage))
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT COALESCE(bytes_in+bytes_out,0) FROM traffic_daily
		WHERE node_id=$1 AND day=(clock_timestamp() AT TIME ZONE 'UTC')::date`, node.ID).Scan(&dailyUsage))
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT (metrics->'haproxy_runtime'->'backends'->($2::text)->>'bytes_in')::bigint
		FROM node_heartbeats WHERE node_id=$1`, node.ID, backend).Scan(&latestRaw))
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT actual_revision FROM node_config_state WHERE node_id=$1`, node.ID).Scan(&actualRevision))
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT traffic_instance_id::text,traffic_sample_seq
		FROM node_heartbeats WHERE node_id=$1`, node.ID).Scan(&latestInstanceID, &latestSequence))
	require.NoError(t, pool.QueryRow(ctx, `SELECT status,last_seen_at FROM nodes WHERE id=$1`, node.ID).
		Scan(&nodeStatus, &finalLastSeen))
	assert.Equal(t, int64(130), accumulated)
	assert.Equal(t, int64(130), hourlyUsage)
	assert.Equal(t, int64(130), dailyUsage)
	detail, err := store.GetNodeOperationalDetail(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, detail.TrafficDailyObservedDays)
	require.NotNil(t, detail.TrafficDailyAverageUsed)
	assert.Equal(t, 130.0, *detail.TrafficDailyAverageUsed)
	assert.Equal(t, int64(130), latestRaw)
	assert.Equal(t, revisionTwo, actualRevision)
	assert.Equal(t, secondInstanceID, latestInstanceID)
	assert.Equal(t, int64(1), latestSequence)
	assert.Equal(t, "online", nodeStatus)
	assert.Equal(t, acceptedLastSeen, finalLastSeen)
}

func TestPGStoreQuotaTrafficSplitsMonotonicDeltaAcrossHourlyWindows(t *testing.T) {
	databaseURL := os.Getenv("NODEFLOW_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NODEFLOW_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err)
	defer pool.Close()

	store := NewPGStore(pool)
	node, err := store.CreateNode(ctx, fmt.Sprintf("quota-split-%d", time.Now().UnixNano()), "198.18.0.3", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.DeleteNode(context.Background(), node.ID) })
	token := fmt.Sprintf("quota-split-token-%d", time.Now().UnixNano())
	sum := sha256.Sum256([]byte(token))
	_, err = store.CreateEnrollmentToken(ctx, node.ID, hex.EncodeToString(sum[:]), "quota-split-test", time.Now().Add(time.Hour))
	require.NoError(t, err)

	route, err := store.CreateRoute(ctx, node.ID, RouteSpec{
		ListenerIP: "*", ListenerPort: 18447, SNIs: []string{"quota-split.example"}, Hostname: "quota-split.example",
		TargetType: "tcp", TargetHost: "192.0.2.11", TargetPort: 443,
		ProxyProtocol: "none", QuotaAction: "observe", QuotaPeriod: "hourly",
	})
	require.NoError(t, err)
	enable := routeAsSpec(route, true)
	enable.ExpectedVersion = revision(route.Version)
	route, err = store.UpdateRoute(ctx, node.ID, route.ID, enable)
	require.NoError(t, err)
	require.NotNil(t, route.DesiredRevision)
	actualRevision := *route.DesiredRevision
	config, err := store.GetConfigRevision(ctx, node.ID, actualRevision)
	require.NoError(t, err)
	_, err = store.IngestApplyReport(ctx, token, ApplyReport{Revision: actualRevision, State: "applied"})
	require.NoError(t, err)

	backend := RouteBackendKey(route.ID)
	instanceID := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	startedAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	_, err = store.IngestHeartbeat(ctx, token, quotaOrderedHeartbeat(
		instanceID, startedAt, 1, actualRevision, config.SHA256, backend, 100,
	))
	require.NoError(t, err)

	var databaseNow time.Time
	require.NoError(t, pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow))
	intervalStart := databaseNow.UTC().Add(-90 * time.Minute).Truncate(time.Microsecond)
	_, err = pool.Exec(ctx, `
		UPDATE traffic_counter_state SET updated_at=$3
		WHERE node_id=$1 AND scope='backend' AND proxy_name=$2`, node.ID, backend, intervalStart)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DELETE FROM traffic_quota_usage WHERE node_id=$1 AND proxy_name=$2`, node.ID, backend)
	require.NoError(t, err)

	const deltaBytes = int64(1_000_000)
	_, err = store.IngestHeartbeat(ctx, token, quotaOrderedHeartbeat(
		instanceID, startedAt, 2, actualRevision, config.SHA256, backend, 100+deltaBytes,
	))
	require.NoError(t, err)
	var observedAt time.Time
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT updated_at FROM traffic_counter_state
		WHERE node_id=$1 AND scope='backend' AND proxy_name=$2`, node.ID, backend).Scan(&observedAt))
	expected, err := splitQuotaDelta("hourly", time.Time{}, intervalStart, observedAt, trafficCounter{BytesIn: deltaBytes})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(expected), 2)

	rows, err := pool.Query(ctx, `
		SELECT window_start,window_end,bytes_in,bytes_out
		FROM traffic_quota_usage
		WHERE node_id=$1 AND proxy_name=$2 AND period='hourly'
		ORDER BY window_start`, node.ID, backend)
	require.NoError(t, err)
	defer rows.Close()
	actual := make([]quotaWindowDelta, 0, len(expected))
	for rows.Next() {
		var part quotaWindowDelta
		part.Window.Period = "hourly"
		require.NoError(t, rows.Scan(&part.Window.Start, &part.Window.End, &part.Delta.BytesIn, &part.Delta.BytesOut))
		actual = append(actual, part)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, expected, actual)
	var total int64
	for _, part := range actual {
		total += part.Delta.BytesIn
	}
	assert.Equal(t, deltaBytes, total)
}

func quotaOrderedHeartbeat(instanceID string, startedAt time.Time, sequence, revision int64, sha, backend string, bytesIn int64) Heartbeat {
	heartbeat := orderedHeartbeat(instanceID, startedAt, sequence)
	heartbeat.Version = "quota-test"
	heartbeat.Status = "online"
	heartbeat.Metrics = quotaTestMetrics(backend, bytesIn)
	heartbeat.ActualRevision = &revision
	heartbeat.ConfigSHA256 = sha
	return heartbeat
}

func quotaTestMetrics(backend string, bytesIn int64) map[string]any {
	return map[string]any{"haproxy_runtime": map[string]any{
		"bytes_in": bytesIn, "bytes_out": int64(0),
		"backends": map[string]any{
			backend: map[string]any{"bytes_in": bytesIn, "bytes_out": int64(0)},
		},
	}}
}
