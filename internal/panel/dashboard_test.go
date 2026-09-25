package panel

import (
	"encoding/json"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDashboardOverviewHTTP(t *testing.T) {
	stamp := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	f := &fakeStore{dashboardOverview: DashboardOverview{
		Range:          "24h",
		SelectedNodeID: testNodeID,
		Nodes: []DashboardNode{{
			Node:            Node{ID: testNodeID, Name: "dev-node-01", Address: "192.0.2.10", Status: "online", Metadata: map[string]any{}},
			RoutesTotal:     2,
			RoutesEnabled:   1,
			TrafficMonth:    "2026-07",
			TrafficBytesIn:  100,
			TrafficBytesOut: 200,
			TrafficUsed:     300,
			TrafficObserved: true,
			RXBitsPerSecond: float64Ref(800),
			TXBitsPerSecond: float64Ref(400),
		}},
		TrafficHistory: TrafficHistory{Range: "24h", BucketSeconds: 300, Samples: []TrafficHistorySample{{Timestamp: stamp, RXBPS: float64Ref(800), TXBPS: float64Ref(400)}}},
		TopRoutes: []DashboardRoute{{
			RouteID: testRouteID, NodeID: testNodeID, NodeName: "dev-node-01", Name: "api.internal",
			ListenerIP: "*", ListenerPort: 443, SNIs: []string{"api.example.com"},
			BytesIn: 30, BytesOut: 40, UsedBytes: 70,
			RXBitsPerSecond: 240, TXBitsPerSecond: 320, BitsPerSecond: 560, SharePercent: 100,
		}},
		Totals: DashboardOverviewTotal{
			NodesTotal: 1, NodesOnline: 1, RoutesTotal: 2, TrafficMonthBytes: 300,
			RXBitsPerSecond: float64Ref(800), TXBitsPerSecond: float64Ref(400), CurrentRateComplete: true,
		},
	}}

	w := request(t, handler(f), http.MethodGet, "/api/v1/overview?range=24h&node_id="+testNodeID, "", testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, testNodeID, f.dashboardNodeID)
	assert.Equal(t, "24h", f.dashboardRange)
	assert.JSONEq(t, `{
		"range":"24h",
		"selected_node_id":"11111111-1111-4111-8111-111111111111",
		"nodes":[{
			"node":{"id":"11111111-1111-4111-8111-111111111111","name":"dev-node-01","address":"192.0.2.10","status":"online","metadata":{},"created_at":"0001-01-01T00:00:00Z","updated_at":"0001-01-01T00:00:00Z","haproxy_logs":true},
			"routes_total":2,"routes_enabled":1,"traffic_month":"2026-07","traffic_bytes_in":100,"traffic_bytes_out":200,"traffic_used_bytes":300,"traffic_observed":true,
			"rx_bits_per_second":800,"tx_bits_per_second":400
		}],
		"traffic_history":{"range":"24h","bucket_seconds":300,"samples":[{"timestamp":"2026-07-13T12:00:00Z","rx_bps":800,"tx_bps":400}]},
		"top_routes":[{"route_id":"22222222-2222-4222-8222-222222222222","node_id":"11111111-1111-4111-8111-111111111111","node_name":"dev-node-01","name":"api.internal","listener_ip":"*","listener_port":443,"snis":["api.example.com"],"fallback":false,"bytes_in":30,"bytes_out":40,"used_bytes":70,"rx_bits_per_second":240,"tx_bits_per_second":320,"bits_per_second":560,"share_percent":100}],
			"totals":{"nodes_total":1,"nodes_online":1,"nodes_degraded":0,"nodes_offline":0,"routes_total":2,"connections_current":0,"rx_bits_per_second":800,"tx_bits_per_second":400,"current_rate_complete":true,"traffic_month_bytes":300,"backends_healthy":0,"backends_degraded":0,"backends_unavailable":0}
		}`, w.Body.String())
}

func TestDashboardOverviewRateJSONContract(t *testing.T) {
	sampledAt := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	f := &fakeStore{dashboardOverview: DashboardOverview{
		Range: "1m",
		Nodes: []DashboardNode{
			{Node: Node{ID: testNodeID}, RXBitsPerSecond: nil, TXBitsPerSecond: nil},
			{Node: Node{ID: testRouteID}, RXBitsPerSecond: float64Ref(0), TXBitsPerSecond: float64Ref(0), RateSampledAt: &sampledAt},
		},
		TrafficHistory: TrafficHistory{Range: "1m", Samples: []TrafficHistorySample{}},
		TopRoutes:      []DashboardRoute{},
		Totals:         DashboardOverviewTotal{},
	}}

	w := request(t, handler(f), http.MethodGet, "/api/v1/overview?range=1m", "", testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var payload map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	nodes := payload["nodes"].([]any)
	unobserved := nodes[0].(map[string]any)
	observedZero := nodes[1].(map[string]any)
	assert.Contains(t, unobserved, "rx_bits_per_second")
	assert.Contains(t, unobserved, "tx_bits_per_second")
	assert.Nil(t, unobserved["rx_bits_per_second"])
	assert.Nil(t, unobserved["tx_bits_per_second"])
	assert.NotContains(t, unobserved, "rate_sampled_at")
	assert.Equal(t, float64(0), observedZero["rx_bits_per_second"])
	assert.Equal(t, float64(0), observedZero["tx_bits_per_second"])
	assert.Equal(t, "2026-07-13T12:00:00Z", observedZero["rate_sampled_at"])
	totals := payload["totals"].(map[string]any)
	assert.Contains(t, totals, "rx_bits_per_second")
	assert.Contains(t, totals, "tx_bits_per_second")
	assert.Nil(t, totals["rx_bits_per_second"])
	assert.Nil(t, totals["tx_bits_per_second"])
	assert.Equal(t, false, totals["current_rate_complete"])
}

func TestDashboardOverviewAcceptsSupportedRanges(t *testing.T) {
	for _, rangeValue := range []string{"1m", "5m", "1h", "24h", "7d", "30d"} {
		t.Run(rangeValue, func(t *testing.T) {
			f := &fakeStore{dashboardOverview: DashboardOverview{Nodes: []DashboardNode{}, TopRoutes: []DashboardRoute{}, TrafficHistory: TrafficHistory{Samples: []TrafficHistorySample{}}}}
			w := request(t, handler(f), http.MethodGet, "/api/v1/overview?range="+rangeValue, "", testAdminToken)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assert.Empty(t, f.dashboardNodeID)
			assert.Equal(t, rangeValue, f.dashboardRange)
		})
	}
}

func TestDashboardOverviewValidationAuthAndMethod(t *testing.T) {
	h := handler(&fakeStore{})
	for _, path := range []string{
		"/api/v1/overview",
		"/api/v1/overview?range=1d",
		"/api/v1/overview?range=1h&range=24h",
		"/api/v1/overview?range=1h&node_id=bad",
		"/api/v1/overview?range=1h&node_id=" + testNodeID + "&node_id=" + testNodeID,
		"/api/v1/overview?range=1h&extra=true",
	} {
		w := request(t, h, http.MethodGet, path, "", testAdminToken)
		assert.Equal(t, http.StatusBadRequest, w.Code, path+": "+w.Body.String())
	}

	w := request(t, h, http.MethodGet, "/api/v1/overview?range=1h", "", "")
	assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	w = request(t, h, http.MethodPost, "/api/v1/overview?range=1h", `{}`, testAdminToken)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code, w.Body.String())
}

func TestDashboardOverviewReturnsNotFoundForUnknownSelectedNode(t *testing.T) {
	f := &fakeStore{dashboardOverviewErr: ErrNotFound}
	w := request(t, handler(f), http.MethodGet, "/api/v1/overview?range=7d&node_id="+testNodeID, "", testAdminToken)
	assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
}

func TestDashboardHeartbeatTotals(t *testing.T) {
	heartbeat := &NodeHeartbeat{Metrics: map[string]any{
		"network_bytes_per_second": map[string]any{
			"ens3_rx": 100.0,
			"wg0_rx":  50.0,
			"ens3_tx": 25.0,
			"wg0_tx":  5.0,
		},
		"haproxy_runtime": map[string]any{
			"connections_current": 12.0,
			"servers": map[string]any{
				"backend-1": map[string]any{"server-1": map[string]any{"status": "UP"}},
				"backend-2": map[string]any{"server-2": map[string]any{"status": "DOWN"}},
				"backend-3": map[string]any{"server-3": map[string]any{"status": "NOLB"}},
			},
		},
	}}

	connections, health := dashboardHeartbeatTotals(heartbeat)
	assert.Equal(t, uint64(12), connections)
	assert.Equal(t, dashboardBackendHealth{Healthy: 1, Degraded: 1, Unavailable: 1}, health)
}

func TestDashboardRuntimeHealthFallsBackToBackendStatus(t *testing.T) {
	health := dashboardRuntimeHealth(map[string]any{"backends": map[string]any{
		"one": map[string]any{"status": "OPEN"},
		"two": map[string]any{"status": "MAINT"},
	}})
	assert.Equal(t, dashboardBackendHealth{Healthy: 1, Unavailable: 1}, health)
}

func TestDashboardRuntimeHealthIgnoresUnassignedDNSPoolSlots(t *testing.T) {
	health := dashboardRuntimeHealth(map[string]any{"servers": map[string]any{
		"dns-pool": map[string]any{
			"server-1": map[string]any{"status": "UP"},
			"server-2": map[string]any{"status": "UP"},
			"server-3": map[string]any{"status": "MAINT (resolution)"},
		},
	}})
	assert.Equal(t, dashboardBackendHealth{Healthy: 2}, health)
}

func TestDashboardRuntimeHealthFallsBackWhenDNSPoolHasNoResolvedSlots(t *testing.T) {
	health := dashboardRuntimeHealth(map[string]any{
		"servers": map[string]any{
			"dns-pool": map[string]any{
				"server-1": map[string]any{"status": "MAINT (resolution)"},
				"server-2": map[string]any{"status": "MAINT (resolution)"},
			},
		},
		"backends": map[string]any{
			"dns-pool": map[string]any{"status": "DOWN"},
		},
	})
	assert.Equal(t, dashboardBackendHealth{Unavailable: 1}, health)
}

func TestDashboardNodeStatus(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-10 * time.Second)
	stale := now.Add(-time.Minute)
	routesOK := true
	routesBroken := false

	assert.Equal(t, "online", dashboardNodeStatus(
		Node{Status: "online", LastSeen: &fresh},
		&NodeHeartbeat{Status: "online", RoutesOK: &routesOK},
		dashboardBackendHealth{Healthy: 1}, now,
	))
	assert.Equal(t, "offline", dashboardNodeStatus(
		Node{Status: "online", LastSeen: &stale},
		&NodeHeartbeat{Status: "online", RoutesOK: &routesOK},
		dashboardBackendHealth{Healthy: 1}, now,
	))
	assert.Equal(t, "degraded", dashboardNodeStatus(
		Node{Status: "online", LastSeen: &fresh},
		&NodeHeartbeat{Status: "online", RoutesOK: &routesBroken},
		dashboardBackendHealth{Healthy: 1}, now,
	))
	assert.Equal(t, "degraded", dashboardNodeStatus(
		Node{Status: "online", LastSeen: &fresh},
		&NodeHeartbeat{Status: "online", RoutesOK: &routesOK},
		dashboardBackendHealth{Healthy: 1, Unavailable: 1}, now,
	))
}

func TestDashboardNumericBounds(t *testing.T) {
	assert.Equal(t, uint64(0), dashboardUint(-1))
	assert.Equal(t, uint64(0), dashboardUint(math.Inf(1)))
	assert.Equal(t, uint64(42), dashboardUint(42.0))
	assert.Equal(t, uint64(math.MaxUint64), dashboardSaturatingAdd(math.MaxUint64-1, 2))
}

func TestDashboardCurrentRateTotals(t *testing.T) {
	t.Run("complete including observed zero", func(t *testing.T) {
		rx, tx, complete := dashboardCurrentRateTotals([]DashboardNode{
			{Node: Node{Status: "online"}, RXBitsPerSecond: float64Ref(0), TXBitsPerSecond: float64Ref(0)},
			{Node: Node{Status: "degraded"}, RXBitsPerSecond: float64Ref(800), TXBitsPerSecond: float64Ref(400)},
			{Node: Node{Status: "offline"}},
		})
		require.True(t, complete)
		require.NotNil(t, rx)
		require.NotNil(t, tx)
		assert.Equal(t, 800.0, *rx)
		assert.Equal(t, 400.0, *tx)
	})

	t.Run("incomplete active node", func(t *testing.T) {
		rx, tx, complete := dashboardCurrentRateTotals([]DashboardNode{
			{Node: Node{Status: "online"}, RXBitsPerSecond: float64Ref(0), TXBitsPerSecond: float64Ref(0)},
			{Node: Node{Status: "degraded"}},
		})
		assert.False(t, complete)
		assert.Nil(t, rx)
		assert.Nil(t, tx)
	})

	t.Run("no active nodes", func(t *testing.T) {
		rx, tx, complete := dashboardCurrentRateTotals([]DashboardNode{{Node: Node{Status: "offline"}}})
		assert.False(t, complete)
		assert.Nil(t, rx)
		assert.Nil(t, tx)
	})
}

func TestDashboardNormalizeCurrentRate(t *testing.T) {
	heartbeatAt := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)

	t.Run("matched observed zero", func(t *testing.T) {
		sampledAt := heartbeatAt
		heartbeatReceivedAt := heartbeatAt
		node := DashboardNode{
			RXBitsPerSecond: float64Ref(0),
			TXBitsPerSecond: float64Ref(0),
			RateSampledAt:   &sampledAt,
		}
		dashboardNormalizeCurrentRate(&node, &heartbeatReceivedAt)
		require.NotNil(t, node.RXBitsPerSecond)
		require.NotNil(t, node.TXBitsPerSecond)
		assert.Zero(t, *node.RXBitsPerSecond)
		assert.Zero(t, *node.TXBitsPerSecond)
		assert.Equal(t, heartbeatAt, *node.RateSampledAt)
	})

	t.Run("mismatched sample", func(t *testing.T) {
		sampledAt := heartbeatAt.Add(-time.Second)
		heartbeatReceivedAt := heartbeatAt
		node := DashboardNode{
			RXBitsPerSecond: float64Ref(800),
			TXBitsPerSecond: float64Ref(400),
			RateSampledAt:   &sampledAt,
		}
		dashboardNormalizeCurrentRate(&node, &heartbeatReceivedAt)
		assert.Nil(t, node.RXBitsPerSecond)
		assert.Nil(t, node.TXBitsPerSecond)
		assert.Nil(t, node.RateSampledAt)
	})
}
