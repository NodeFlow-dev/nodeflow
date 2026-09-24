package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPGStoreDashboardOverview(t *testing.T) {
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
	require.NoError(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version='000016')`).Scan(&migrated))
	require.True(t, migrated, "database must include migration 000016")

	store := NewPGStore(pool)
	seed := time.Now().UnixNano()
	third := 1 + int(seed%253)
	fourth := 1 + int((seed/253)%252)
	nodeOne, err := store.CreateNode(ctx, fmt.Sprintf("dashboard-one-%d", seed), fmt.Sprintf("198.19.%d.%d", third, fourth), map[string]any{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.DeleteNode(context.Background(), nodeOne.ID) })
	nodeTwo, err := store.CreateNode(ctx, fmt.Sprintf("dashboard-two-%d", seed), fmt.Sprintf("198.19.%d.%d", third, fourth+1), map[string]any{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.DeleteNode(context.Background(), nodeTwo.ID) })

	metricsOne := dashboardIntegrationMetrics(9, 100, 50, "UP", "DOWN")
	metricsTwo := dashboardIntegrationMetrics(4, 50, 25, "UP")
	heartbeatOneAt := insertDashboardHeartbeat(t, ctx, pool, nodeOne.ID, metricsOne)
	heartbeatTwoAt := insertDashboardHeartbeat(t, ctx, pool, nodeTwo.ID, metricsTwo)

	route, err := store.CreateRoute(ctx, nodeOne.ID, RouteSpec{
		ListenerIP: "*", ListenerPort: 443,
		SNIs: []string{"api.dashboard.example"}, Hostname: "api.dashboard.example",
		TargetType: "tcp", TargetHost: "192.0.2.80", TargetPort: 443,
		ProxyProtocol: "none", QuotaAction: "observe", QuotaPeriod: "calendar_month",
	})
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE routes SET enabled=true WHERE id=$1`, route.ID)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO traffic_monthly(node_id,month,scope,proxy_name,bytes_in,bytes_out,updated_at)
		VALUES
			($1,date_trunc('month',clock_timestamp() AT TIME ZONE 'UTC')::date,'node','',1000,2000,clock_timestamp()),
			($2,date_trunc('month',clock_timestamp() AT TIME ZONE 'UTC')::date,'node','',300,700,clock_timestamp()),
			($1,date_trunc('month',clock_timestamp() AT TIME ZONE 'UTC')::date,'backend',$3,300,400,clock_timestamp())`,
		nodeOne.ID, nodeTwo.ID, RouteBackendKey(route.ID))
	require.NoError(t, err)

	bucket := time.Now().UTC().Truncate(15 * time.Second).Add(-30 * time.Second)
	_, err = pool.Exec(ctx, `
		INSERT INTO node_traffic_rate_samples(node_id,sampled_at,rx_bytes_per_second,tx_bytes_per_second,cpu_percent,memory_percent)
		VALUES
			($1,$3,100,50,10,20),($1,$3+interval '2 seconds',300,150,30,40),
			($2,$3,50,25,20,30),($2,$3+interval '2 seconds',150,75,40,50),
				($1,$4,125,75,25,35),($2,$5,0,0,30,40)`,
		nodeOne.ID, nodeTwo.ID, bucket.Add(2*time.Second), heartbeatOneAt, heartbeatTwoAt)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO route_traffic_rate_samples(node_id,route_id,sampled_at,rx_bytes_per_second,tx_bytes_per_second)
		VALUES ($1,$2,$3,100,50),($1,$2,$3+interval '2 seconds',300,150),
		       ($1,$2,$3-interval '2 minutes',1000000,1000000)`,
		nodeOne.ID, route.ID, bucket.Add(2*time.Second))
	require.NoError(t, err)

	selected, err := store.GetDashboardOverview(ctx, nodeOne.ID, "1m")
	require.NoError(t, err)
	assert.Equal(t, nodeOne.ID, selected.SelectedNodeID)
	assert.Len(t, selected.TopRoutes, 1)
	assert.Equal(t, nodeOne.ID, selected.TopRoutes[0].NodeID)
	assert.Equal(t, nodeOne.Name, selected.TopRoutes[0].NodeName)
	assert.Equal(t, route.ID, selected.TopRoutes[0].RouteID)
	assert.Equal(t, route.Name, selected.TopRoutes[0].Name)
	assert.Equal(t, int64(300), selected.TopRoutes[0].BytesIn)
	assert.Equal(t, int64(400), selected.TopRoutes[0].BytesOut)
	routeHistory, err := store.GetRouteTrafficHistory(ctx, nodeOne.ID, route.ID, "1m")
	require.NoError(t, err)
	require.Len(t, routeHistory.Samples, 5)
	assert.Equal(t, int64(15), routeHistory.BucketSeconds)
	routeSample := dashboardSampleAt(t, routeHistory.Samples, bucket)
	require.NotNil(t, routeSample.RXBPS)
	require.NotNil(t, routeSample.TXBPS)
	assert.InDelta(t, 1600.0, *routeSample.RXBPS, 0.01)
	assert.InDelta(t, 800.0, *routeSample.TXBPS, 0.01)
	_, err = store.GetRouteTrafficHistory(ctx, nodeTwo.ID, route.ID, "1m")
	assert.ErrorIs(t, err, ErrNotFound, "a route must not be readable through another node")
	assert.Equal(t, int64(700), selected.TopRoutes[0].UsedBytes)
	assert.Equal(t, 1600.0, selected.TopRoutes[0].RXBitsPerSecond)
	assert.Equal(t, 800.0, selected.TopRoutes[0].TXBitsPerSecond)
	assert.Equal(t, 2400.0, selected.TopRoutes[0].BitsPerSecond)
	assert.Equal(t, 100.0, selected.TopRoutes[0].SharePercent)

	selectedNode := findDashboardNode(t, selected.Nodes, nodeOne.ID)
	assert.Equal(t, 1, selectedNode.RoutesTotal)
	assert.Equal(t, 1, selectedNode.RoutesEnabled)
	assert.Equal(t, int64(3000), selectedNode.TrafficUsed)
	assert.True(t, selectedNode.TrafficObserved)
	require.NotNil(t, selectedNode.RXBitsPerSecond)
	require.NotNil(t, selectedNode.TXBitsPerSecond)
	assert.Equal(t, 1000.0, *selectedNode.RXBitsPerSecond)
	assert.Equal(t, 600.0, *selectedNode.TXBitsPerSecond)
	require.NotNil(t, selectedNode.RateSampledAt)
	assert.Equal(t, heartbeatOneAt, *selectedNode.RateSampledAt)
	zeroNode := findDashboardNode(t, selected.Nodes, nodeTwo.ID)
	require.NotNil(t, zeroNode.RXBitsPerSecond)
	require.NotNil(t, zeroNode.TXBitsPerSecond)
	assert.Zero(t, *zeroNode.RXBitsPerSecond, "an observed current zero must not become null")
	assert.Zero(t, *zeroNode.TXBitsPerSecond, "an observed current zero must not become null")
	require.NotNil(t, zeroNode.RateSampledAt)
	assert.Equal(t, heartbeatTwoAt, *zeroNode.RateSampledAt)
	assert.Equal(t, "degraded", selectedNode.Node.Status)
	assert.GreaterOrEqual(t, selected.Totals.NodesTotal, 2)
	assert.GreaterOrEqual(t, selected.Totals.RoutesTotal, 1)
	assert.GreaterOrEqual(t, selected.Totals.ConnectionsCurrent, uint64(13))
	assert.GreaterOrEqual(t, selected.Totals.TrafficMonthBytes, int64(4000))
	assert.GreaterOrEqual(t, selected.Totals.BackendsHealthy, 2)
	assert.GreaterOrEqual(t, selected.Totals.BackendsUnavailable, 1)

	selectedSample := dashboardSampleAt(t, selected.TrafficHistory.Samples, bucket)
	require.NotNil(t, selectedSample.RXBPS)
	require.NotNil(t, selectedSample.TXBPS)
	assert.Equal(t, 1600.0, *selectedSample.RXBPS)
	assert.Equal(t, 800.0, *selectedSample.TXBPS)
	require.NotNil(t, selectedSample.CPUPercent)
	require.NotNil(t, selectedSample.MemoryPercent)
	assert.Equal(t, 20.0, *selectedSample.CPUPercent)
	assert.Equal(t, 30.0, *selectedSample.MemoryPercent)

	global, err := store.GetDashboardOverview(ctx, "", "1m")
	require.NoError(t, err)
	globalSample := dashboardSampleAt(t, global.TrafficHistory.Samples, bucket)
	require.NotNil(t, globalSample.RXBPS)
	require.NotNil(t, globalSample.TXBPS)
	assert.GreaterOrEqual(t, *globalSample.RXBPS, 2400.0, "global history must average each node inside the bucket, then sum the node rates")
	assert.GreaterOrEqual(t, *globalSample.TXBPS, 1200.0, "global history must average each node inside the bucket, then sum the node rates")

	_, err = pool.Exec(ctx, `
			UPDATE node_traffic_rate_samples
			SET sampled_at=$3
			WHERE node_id=$1 AND sampled_at=$2`, nodeTwo.ID, heartbeatTwoAt, heartbeatTwoAt.Add(-time.Millisecond))
	require.NoError(t, err)
	incomplete, err := store.GetDashboardOverview(ctx, "", "1m")
	require.NoError(t, err)
	mismatchedNode := findDashboardNode(t, incomplete.Nodes, nodeTwo.ID)
	assert.Nil(t, mismatchedNode.RXBitsPerSecond)
	assert.Nil(t, mismatchedNode.TXBitsPerSecond)
	assert.Nil(t, mismatchedNode.RateSampledAt)
	assert.False(t, incomplete.Totals.CurrentRateComplete)
	assert.Nil(t, incomplete.Totals.RXBitsPerSecond)
	assert.Nil(t, incomplete.Totals.TXBitsPerSecond)

	_, err = store.GetDashboardOverview(ctx, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "1m")
	assert.True(t, errors.Is(err, ErrNotFound), err)
}

func dashboardIntegrationMetrics(connections uint64, rxBytes, txBytes float64, statuses ...string) map[string]any {
	servers := make(map[string]any, len(statuses))
	for index, status := range statuses {
		servers[fmt.Sprintf("backend-%d", index)] = map[string]any{
			fmt.Sprintf("server-%d", index): map[string]any{"status": status},
		}
	}
	return map[string]any{
		"network_bytes_per_second": map[string]any{"ens3_rx": rxBytes, "ens3_tx": txBytes},
		"haproxy_runtime": map[string]any{
			"connections_current": connections,
			"servers":             servers,
		},
	}
}

func insertDashboardHeartbeat(t *testing.T, ctx context.Context, pool *pgxpool.Pool, nodeID string, metrics map[string]any) time.Time {
	t.Helper()
	encoded, err := json.Marshal(metrics)
	require.NoError(t, err)
	observedAt := time.Now().UTC()
	_, err = pool.Exec(ctx, `UPDATE nodes SET status='online',last_seen_at=$2,updated_at=$2 WHERE id=$1`, nodeID, observedAt)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO node_heartbeats(node_id,agent_version,status,metrics,routes_ok,received_at)
		VALUES($1,'dashboard-test','online',$2,true,$3)`, nodeID, string(encoded), observedAt)
	require.NoError(t, err)
	return observedAt
}

func findDashboardNode(t *testing.T, nodes []DashboardNode, nodeID string) DashboardNode {
	t.Helper()
	for _, node := range nodes {
		if node.Node.ID == nodeID {
			return node
		}
	}
	require.FailNow(t, "dashboard node not found", nodeID)
	return DashboardNode{}
}

func dashboardSampleAt(t *testing.T, samples []TrafficHistorySample, bucket time.Time) TrafficHistorySample {
	t.Helper()
	for _, sample := range samples {
		if sample.Timestamp.Equal(bucket) {
			return sample
		}
	}
	require.FailNow(t, "dashboard traffic bucket not found", bucket.String())
	return TrafficHistorySample{}
}
