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

func TestPGStoreTrafficHistoryHeartbeatPersistenceAndRetention(t *testing.T) {
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
	node, err := store.CreateNode(ctx, fmt.Sprintf("traffic-history-%d", time.Now().UnixNano()), "198.18.0.13", map[string]any{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.DeleteNode(context.Background(), node.ID) })
	token := fmt.Sprintf("traffic-history-token-%d", time.Now().UnixNano())
	sum := sha256.Sum256([]byte(token))
	_, err = store.CreateEnrollmentToken(ctx, node.ID, hex.EncodeToString(sum[:]), "history-test", time.Now().Add(time.Hour))
	require.NoError(t, err)

	instanceID := "33333333-3333-4333-8333-333333333333"
	startedAt := time.Now().UTC().Add(-time.Minute)
	heartbeat := orderedHeartbeat(instanceID, startedAt, 1)
	heartbeat.Version = "history-test"
	heartbeat.Status = "online"
	heartbeat.Metrics = map[string]any{
		"cpu_percent": 25.0, "memory_total_bytes": 1000.0, "memory_available_bytes": 600.0,
		"network_bytes_per_second": map[string]any{"ens3_rx": 999999.0, "ens3_tx": 999999.0},
		"haproxy_runtime":          map[string]any{"counter_generation": "4312:1783930000:7", "bytes_in": 1000.0, "bytes_out": 500.0},
	}
	_, err = store.IngestHeartbeat(ctx, token, heartbeat)
	require.NoError(t, err)

	var sampleCount int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM node_traffic_rate_samples WHERE node_id=$1`, node.ID).Scan(&sampleCount))
	assert.Zero(t, sampleCount, "the first HAProxy counter is a baseline, not an instantaneous rate")
	_, err = pool.Exec(ctx, `UPDATE traffic_counter_state SET updated_at=clock_timestamp()-interval '10 seconds' WHERE node_id=$1 AND scope='node'`, node.ID)
	require.NoError(t, err)
	sequence := int64(2)
	heartbeat.TrafficSampleSeq = &sequence
	heartbeat.Metrics = map[string]any{
		"cpu_percent": 25.0, "memory_total_bytes": 1000.0, "memory_available_bytes": 600.0,
		"network_bytes_per_second": map[string]any{"ens3_rx": 999999.0, "ens3_tx": 999999.0},
		"haproxy_runtime":          map[string]any{"counter_generation": "4312:1783930000:7", "bytes_in": 2000.0, "bytes_out": 1000.0},
	}
	_, err = store.IngestHeartbeat(ctx, token, heartbeat)
	require.NoError(t, err)

	history, err := store.GetTrafficHistory(ctx, node.ID, "1h")
	require.NoError(t, err)
	require.Len(t, history.Samples, 61)
	assert.Equal(t, int64(60), history.BucketSeconds)
	observedSample := firstObservedTrafficSample(t, history.Samples)
	require.NotNil(t, observedSample.RXBPS)
	require.NotNil(t, observedSample.TXBPS)
	assert.InDelta(t, 800.0, *observedSample.RXBPS, 25.0)
	assert.InDelta(t, 400.0, *observedSample.TXBPS, 25.0)
	require.NotNil(t, observedSample.CPUPercent)
	require.NotNil(t, observedSample.MemoryPercent)
	assert.Equal(t, 25.0, *observedSample.CPUPercent)
	assert.Equal(t, 40.0, *observedSample.MemoryPercent)
	detail, err := store.GetNodeOperationalDetail(ctx, node.ID)
	require.NoError(t, err)
	require.NotNil(t, detail.RXBitsPerSecond)
	require.NotNil(t, detail.TXBitsPerSecond)
	require.NotNil(t, detail.RateSampledAt)
	assert.InDelta(t, 800.0, *detail.RXBitsPerSecond, 25.0)
	assert.InDelta(t, 400.0, *detail.TXBitsPerSecond, 25.0)
	require.NotNil(t, detail.MetricsSummary)
	assert.Equal(t, int64(1), detail.MetricsSummary.SampleCount)
	require.NotNil(t, detail.MetricsSummary.CPUPercent)
	assert.Equal(t, 25.0, *detail.MetricsSummary.CPUPercent.Average)
	assert.Equal(t, int64(1), detail.MetricsSummary.CPUPercent.SampleCount)
	require.NotNil(t, detail.MetricsSummary.CPUPercent.ObservedFrom)
	require.NotNil(t, detail.MetricsSummary.CPUPercent.ObservedTo)

	legacy := orderedHeartbeat(instanceID, startedAt, 3)
	legacy.Version = "history-test"
	legacy.Status = "online"
	legacy.Metrics = map[string]any{}
	_, err = store.IngestHeartbeat(ctx, token, legacy)
	require.NoError(t, err)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM node_traffic_rate_samples WHERE node_id=$1`, node.ID).Scan(&sampleCount))
	assert.Equal(t, 1, sampleCount, "missing rate metrics must not create a zero sample")

	_, err = pool.Exec(ctx, `
		INSERT INTO node_traffic_rate_samples(node_id,sampled_at,rx_bytes_per_second,tx_bytes_per_second)
		VALUES($1,clock_timestamp()-interval '32 days',1,1)`, node.ID)
	require.NoError(t, err)
	sequence = int64(4)
	heartbeat.TrafficSampleSeq = &sequence
	heartbeat.Metrics = map[string]any{
		"network_bytes_per_second": map[string]any{"ens3_rx": 999999.0, "ens3_tx": 999999.0},
		"haproxy_runtime":          map[string]any{"counter_generation": "4312:1783930000:7", "bytes_in": 3000.0, "bytes_out": 1500.0},
		"cpu_percent":              50.0, "memory_total_bytes": 1000.0, "memory_available_bytes": 500.0,
	}
	_, err = store.IngestHeartbeat(ctx, token, heartbeat)
	require.NoError(t, err)
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM node_traffic_rate_samples
		WHERE node_id=$1 AND sampled_at < clock_timestamp()-interval '31 days'`, node.ID).Scan(&sampleCount))
	assert.Equal(t, 1, sampleCount, "heartbeats no longer prune history inside their transaction")
	_, err = pool.Exec(ctx, `UPDATE traffic_rate_retention_state SET last_cleanup_at='-infinity'`)
	require.NoError(t, err)
	retention, err := store.CleanupRetention(ctx, DefaultDataRetentionPolicy())
	require.NoError(t, err)
	assert.True(t, retention.Ran)
	assert.GreaterOrEqual(t, retention.Deleted["node_traffic_rate_samples"], int64(1))
	retention, err = store.CleanupRetention(ctx, DefaultDataRetentionPolicy())
	require.NoError(t, err)
	assert.False(t, retention.Ran, "the persisted gate prevents back-to-back sweeps")
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM node_traffic_rate_samples
		WHERE node_id=$1 AND sampled_at < clock_timestamp()-interval '31 days'`, node.ID).Scan(&sampleCount))
	assert.Zero(t, sampleCount)
}

func firstObservedTrafficSample(t *testing.T, samples []TrafficHistorySample) TrafficHistorySample {
	t.Helper()
	for _, sample := range samples {
		if sample.RXBPS != nil && sample.TXBPS != nil {
			return sample
		}
	}
	require.FailNow(t, "observed traffic sample not found")
	return TrafficHistorySample{}
}
