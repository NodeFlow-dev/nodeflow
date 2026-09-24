package panel

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"
)

const dashboardOfflineAfter = 45 * time.Second

func (a *API) dashboardOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	query := r.URL.Query()
	for key := range query {
		if key != "range" && key != "node_id" {
			writeError(w, http.StatusBadRequest, "validation_error", "only range and node_id query parameters are supported")
			return
		}
	}
	ranges, supplied := query["range"]
	if !supplied || len(ranges) != 1 {
		writeError(w, http.StatusBadRequest, "validation_error", "range must be one of 1m, 5m, 1h, 24h, 7d, 30d")
		return
	}
	if _, ok := parseTrafficHistoryRange(ranges[0]); !ok {
		writeError(w, http.StatusBadRequest, "validation_error", "range must be one of 1m, 5m, 1h, 24h, 7d, 30d")
		return
	}
	nodeID := ""
	if values, supplied := query["node_id"]; supplied {
		if len(values) != 1 || !validID(values[0]) {
			writeError(w, http.StatusBadRequest, "validation_error", "node_id must be one valid UUID")
			return
		}
		nodeID = strings.ToLower(values[0])
	}
	overview, err := a.store.GetDashboardOverview(r.Context(), nodeID, ranges[0])
	respondStore(w, overview, err, http.StatusOK)
}

func (s *PGStore) GetDashboardOverview(ctx context.Context, nodeID, rangeValue string) (DashboardOverview, error) {
	spec, ok := parseTrafficHistoryRange(rangeValue)
	if !ok {
		return DashboardOverview{}, errors.New("invalid dashboard range")
	}
	result := DashboardOverview{
		Range:          rangeValue,
		SelectedNodeID: nodeID,
		Nodes:          make([]DashboardNode, 0),
		TrafficHistory: TrafficHistory{Range: rangeValue, BucketSeconds: int64(spec.bucket / time.Second), Samples: make([]TrafficHistorySample, 0)},
		TopRoutes:      make([]DashboardRoute, 0),
	}
	selectedFound, err := s.loadDashboardNodes(ctx, &result)
	if err != nil {
		return DashboardOverview{}, err
	}
	if nodeID != "" && !selectedFound {
		return DashboardOverview{}, ErrNotFound
	}
	if err = s.loadDashboardHistory(ctx, &result, spec); err != nil {
		return DashboardOverview{}, err
	}
	if err = s.loadDashboardTopRoutes(ctx, &result, spec); err != nil {
		return DashboardOverview{}, err
	}
	return result, nil
}

func (s *PGStore) loadDashboardNodes(ctx context.Context, result *DashboardOverview) (bool, error) {
	rows, err := s.pool.Query(ctx, `
		WITH current_month AS (
			SELECT date_trunc('month',clock_timestamp() AT TIME ZONE 'UTC')::date AS month
		)
		SELECT n.id::text,n.name,host(n.address),n.status,n.metadata,n.last_seen_at,n.created_at,n.updated_at,n.sort_order,
		       heartbeat.agent_version,heartbeat.status,heartbeat.metrics,heartbeat.routes_ok,
		       heartbeat.traffic_instance_id::text,heartbeat.traffic_instance_started_at,
		       heartbeat.traffic_sample_seq,heartbeat.received_at,
		       COALESCE(route_count.total,0),COALESCE(route_count.enabled,0),
		       current_month.month,
		       COALESCE(traffic.bytes_in,0),COALESCE(traffic.bytes_out,0),
		       COALESCE(traffic.bytes_in,0)+COALESCE(traffic.bytes_out,0),
		       traffic.node_id IS NOT NULL,
		       rate.rx_bytes_per_second*8,
		       rate.tx_bytes_per_second*8,
		       rate.sampled_at
		FROM nodes AS n
		CROSS JOIN current_month
		LEFT JOIN node_heartbeats AS heartbeat ON heartbeat.node_id=n.id
		LEFT JOIN LATERAL (
			SELECT count(*) AS total,count(*) FILTER (WHERE enabled) AS enabled
			FROM routes WHERE node_id=n.id
		) AS route_count ON true
		LEFT JOIN traffic_monthly AS traffic
		  ON traffic.node_id=n.id AND traffic.month=current_month.month
		 AND traffic.scope='node' AND traffic.proxy_name=''
		LEFT JOIN LATERAL (
			SELECT sampled_at,rx_bytes_per_second,tx_bytes_per_second
			FROM node_traffic_rate_samples
			WHERE node_id=n.id
			ORDER BY sampled_at DESC
			LIMIT 1
		) AS rate ON true
		ORDER BY n.sort_order,n.id`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	now := time.Now().UTC()
	selectedFound := result.SelectedNodeID == ""
	for rows.Next() {
		var item DashboardNode
		var metadata []byte
		var heartbeatAgentVersion, heartbeatStatus *string
		var heartbeatMetrics []byte
		var heartbeatRoutesOK *bool
		var heartbeatInstanceID *string
		var heartbeatInstanceStartedAt *time.Time
		var heartbeatSampleSequence *int64
		var heartbeatReceivedAt *time.Time
		var month time.Time
		if err = rows.Scan(
			&item.Node.ID, &item.Node.Name, &item.Node.Address, &item.Node.Status, &metadata,
			&item.Node.LastSeen, &item.Node.CreatedAt, &item.Node.UpdatedAt, &item.Node.SortOrder,
			&heartbeatAgentVersion, &heartbeatStatus, &heartbeatMetrics, &heartbeatRoutesOK,
			&heartbeatInstanceID, &heartbeatInstanceStartedAt, &heartbeatSampleSequence, &heartbeatReceivedAt,
			&item.RoutesTotal, &item.RoutesEnabled, &month,
			&item.TrafficBytesIn, &item.TrafficBytesOut, &item.TrafficUsed, &item.TrafficObserved,
			&item.RXBitsPerSecond, &item.TXBitsPerSecond, &item.RateSampledAt,
		); err != nil {
			return false, err
		}
		if err = json.Unmarshal(metadata, &item.Node.Metadata); err != nil {
			return false, err
		}
		item.TrafficMonth = month.UTC().Format("2006-01")
		if heartbeatReceivedAt != nil {
			heartbeat := NodeHeartbeat{
				RoutesOK:                 heartbeatRoutesOK,
				TrafficInstanceStartedAt: heartbeatInstanceStartedAt,
				TrafficSampleSeq:         heartbeatSampleSequence,
				ReceivedAt:               heartbeatReceivedAt.UTC(),
			}
			if heartbeatAgentVersion != nil {
				heartbeat.AgentVersion = *heartbeatAgentVersion
			}
			if heartbeatStatus != nil {
				heartbeat.Status = *heartbeatStatus
			}
			if heartbeatInstanceID != nil {
				heartbeat.TrafficInstanceID = *heartbeatInstanceID
			}
			if len(heartbeatMetrics) != 0 {
				if err = json.Unmarshal(heartbeatMetrics, &heartbeat.Metrics); err != nil {
					return false, err
				}
			}
			item.LatestHeartbeat = &heartbeat
		}
		connections, health := dashboardHeartbeatTotals(item.LatestHeartbeat)
		item.Node.Status = dashboardNodeStatus(item.Node, item.LatestHeartbeat, health, now)
		dashboardNormalizeCurrentRate(&item, heartbeatReceivedAt)
		result.Totals.NodesTotal++
		result.Totals.RoutesTotal += item.RoutesTotal
		result.Totals.TrafficMonthBytes += item.TrafficUsed
		switch item.Node.Status {
		case "online":
			result.Totals.NodesOnline++
		case "degraded":
			result.Totals.NodesDegraded++
		default:
			result.Totals.NodesOffline++
		}
		if item.Node.Status == "offline" {
			result.Totals.BackendsUnavailable += health.Healthy + health.Degraded + health.Unavailable
		} else {
			result.Totals.ConnectionsCurrent = dashboardSaturatingAdd(result.Totals.ConnectionsCurrent, connections)
			result.Totals.BackendsHealthy += health.Healthy
			result.Totals.BackendsDegraded += health.Degraded
			result.Totals.BackendsUnavailable += health.Unavailable
		}
		if item.Node.ID == result.SelectedNodeID {
			selectedFound = true
		}
		result.Nodes = append(result.Nodes, item)
	}
	if err = rows.Err(); err != nil {
		return false, err
	}
	result.Totals.RXBitsPerSecond, result.Totals.TXBitsPerSecond, result.Totals.CurrentRateComplete = dashboardCurrentRateTotals(result.Nodes)
	return selectedFound, nil
}

func (s *PGStore) loadDashboardHistory(ctx context.Context, result *DashboardOverview, spec trafficHistoryRange) error {
	var selected any
	if result.SelectedNodeID != "" {
		selected = result.SelectedNodeID
	}
	observedAt := time.Now().UTC()
	rows, err := s.pool.Query(ctx, `
		WITH per_node_bucket AS (
			SELECT node_id,
			       date_bin($2::interval,sampled_at,TIMESTAMPTZ '1970-01-01 00:00:00+00') AS bucket_at,
			       avg(rx_bytes_per_second)*8 AS rx_bps,
			       avg(tx_bytes_per_second)*8 AS tx_bps,
			       avg(cpu_percent) AS cpu_percent,
			       avg(memory_percent) AS memory_percent
			FROM node_traffic_rate_samples
			WHERE sampled_at >= $3::timestamptz-$4::interval
			  AND sampled_at <= $3::timestamptz
			  AND ($1::uuid IS NULL OR node_id=$1::uuid)
			GROUP BY node_id,bucket_at
		)
		SELECT bucket_at,sum(rx_bps),sum(tx_bps),avg(cpu_percent),avg(memory_percent)
		FROM per_node_bucket
		GROUP BY bucket_at
		ORDER BY bucket_at`, selected, postgresInterval(spec.bucket), observedAt, postgresInterval(spec.window))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var sample TrafficHistorySample
		if err = rows.Scan(&sample.Timestamp, &sample.RXBPS, &sample.TXBPS, &sample.CPUPercent, &sample.MemoryPercent); err != nil {
			return err
		}
		result.TrafficHistory.Samples = append(result.TrafficHistory.Samples, sample)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	result.TrafficHistory.Samples = fillTrafficHistoryGaps(spec, observedAt, result.TrafficHistory.Samples)
	return nil
}

func (s *PGStore) loadDashboardTopRoutes(ctx context.Context, result *DashboardOverview, spec trafficHistoryRange) error {
	var selected any
	if result.SelectedNodeID != "" {
		selected = result.SelectedNodeID
	}
	rows, err := s.pool.Query(ctx, `
		WITH current_month AS (
			SELECT date_trunc('month',clock_timestamp() AT TIME ZONE 'UTC')::date AS month
		), route_rates AS (
			SELECT route_id,node_id,
			       avg(rx_bytes_per_second)*8 AS rx_bps,
			       avg(tx_bytes_per_second)*8 AS tx_bps
			FROM route_traffic_rate_samples
			WHERE sampled_at >= clock_timestamp()-$2::interval
			  AND ($1::uuid IS NULL OR node_id=$1::uuid)
			GROUP BY route_id,node_id
		), total_rate AS (
			SELECT COALESCE(sum(rx_bps+tx_bps),0) AS bps FROM route_rates
		)
		SELECT route.id::text,route.node_id::text,node.name,
		       route.name,
		       route.listener_ip,route.listener_port,
		       NULL::text[],
		       route.fallback,
		       COALESCE(traffic.bytes_in,0),COALESCE(traffic.bytes_out,0),
		       COALESCE(traffic.bytes_in,0)+COALESCE(traffic.bytes_out,0) AS used_bytes,
		       rate.rx_bps,rate.tx_bps,rate.rx_bps+rate.tx_bps,
		       CASE WHEN total.bps>0 THEN (rate.rx_bps+rate.tx_bps)*100/total.bps ELSE 0 END
		FROM routes AS route
		JOIN nodes AS node ON node.id=route.node_id
		JOIN route_rates AS rate ON rate.route_id=route.id AND rate.node_id=route.node_id
		CROSS JOIN current_month
		CROSS JOIN total_rate AS total
		LEFT JOIN traffic_monthly AS traffic
		  ON traffic.node_id=route.node_id AND traffic.month=current_month.month
		 AND traffic.scope='backend'
		 AND traffic.proxy_name=('nf_be_' || replace(lower(route.id::text),'-',''))
		ORDER BY rate.rx_bps+rate.tx_bps DESC,route.created_at DESC
		LIMIT 5`, selected, postgresInterval(spec.window))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var route DashboardRoute
		if err = rows.Scan(
			&route.RouteID, &route.NodeID, &route.NodeName, &route.Name,
			&route.ListenerIP, &route.ListenerPort, &route.SNIs, &route.Fallback,
			&route.BytesIn, &route.BytesOut, &route.UsedBytes,
			&route.RXBitsPerSecond, &route.TXBitsPerSecond, &route.BitsPerSecond, &route.SharePercent,
		); err != nil {
			return err
		}
		route.SNIs = []string{}
		result.TopRoutes = append(result.TopRoutes, route)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	rows.Close()
	return s.loadDashboardTopRouteSNIs(ctx, result.TopRoutes)
}

type dashboardBackendHealth struct {
	Healthy     int
	Degraded    int
	Unavailable int
}

func dashboardHeartbeatTotals(heartbeat *NodeHeartbeat) (uint64, dashboardBackendHealth) {
	if heartbeat == nil || heartbeat.Metrics == nil {
		return 0, dashboardBackendHealth{}
	}
	runtime, ok := heartbeat.Metrics["haproxy_runtime"].(map[string]any)
	if !ok {
		return 0, dashboardBackendHealth{}
	}
	connections := dashboardUint(runtime["connections_current"])
	health := dashboardRuntimeHealth(runtime)
	return connections, health
}

func dashboardRuntimeHealth(runtime map[string]any) dashboardBackendHealth {
	var health dashboardBackendHealth
	servers, serversOK := runtime["servers"].(map[string]any)
	serverCount := 0
	if serversOK {
		for _, rawBackend := range servers {
			backend, ok := rawBackend.(map[string]any)
			if !ok {
				continue
			}
			for _, rawServer := range backend {
				server, ok := rawServer.(map[string]any)
				if !ok || serverCount >= maxTrafficBackends {
					continue
				}
				if rawAddress, reported := server["address"]; reported {
					if strings.TrimSpace(dashboardStatusString(rawAddress)) == "" {
						continue
					}
				}
				if dashboardAddBackendStatus(&health, dashboardStatusString(server["status"])) {
					serverCount++
				}
			}
		}
	}
	if serverCount > 0 {
		return health
	}
	backends, ok := runtime["backends"].(map[string]any)
	if !ok {
		return health
	}
	count := 0
	for _, rawBackend := range backends {
		backend, ok := rawBackend.(map[string]any)
		if !ok || count >= maxTrafficBackends {
			continue
		}
		if dashboardAddBackendStatus(&health, dashboardStatusString(backend["status"])) {
			count++
		}
	}
	return health
}

func dashboardAddBackendStatus(health *dashboardBackendHealth, status string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(status))
	if normalized == "MAINT (RESOLUTION)" {
		return false
	}
	switch normalized {
	case "UP", "OPEN", "READY":
		health.Healthy++
	case "DOWN", "MAINT", "STOPPED":
		health.Unavailable++
	default:
		health.Degraded++
	}
	return true
}

func dashboardStatusString(value any) string {
	status, _ := value.(string)
	return strings.TrimSpace(status)
}

func dashboardUint(value any) uint64 {
	number, ok := nonNegativeFiniteFloat(value)
	if !ok || number > math.MaxUint64 {
		return 0
	}
	return uint64(number)
}

func dashboardSaturatingAdd(left, right uint64) uint64 {
	if math.MaxUint64-left < right {
		return math.MaxUint64
	}
	return left + right
}

func dashboardCurrentRateTotals(nodes []DashboardNode) (*float64, *float64, bool) {
	var rxBitsPerSecond, txBitsPerSecond float64
	observedNodes := 0
	for _, node := range nodes {
		if node.Node.Status == "offline" {
			continue
		}
		observedNodes++
		if node.RXBitsPerSecond == nil || node.TXBitsPerSecond == nil {
			return nil, nil, false
		}
		rxBitsPerSecond += *node.RXBitsPerSecond
		txBitsPerSecond += *node.TXBitsPerSecond
	}
	if observedNodes == 0 {
		return nil, nil, false
	}
	return &rxBitsPerSecond, &txBitsPerSecond, true
}

func dashboardNormalizeCurrentRate(node *DashboardNode, heartbeatReceivedAt *time.Time) {
	if heartbeatReceivedAt != nil && node.RateSampledAt != nil && node.RateSampledAt.Equal(heartbeatReceivedAt.UTC()) {
		return
	}
	node.RXBitsPerSecond = nil
	node.TXBitsPerSecond = nil
	node.RateSampledAt = nil
}

func dashboardNodeStatus(node Node, heartbeat *NodeHeartbeat, health dashboardBackendHealth, now time.Time) string {
	if node.LastSeen == nil || now.Sub(node.LastSeen.UTC()) > dashboardOfflineAfter || heartbeat == nil || node.Status == "offline" || node.Status == "pending" {
		return "offline"
	}
	if node.Status == "error" || (heartbeat.Status != "" && heartbeat.Status != "online" && heartbeat.Status != "ok") ||
		(heartbeat.RoutesOK != nil && !*heartbeat.RoutesOK) || health.Degraded > 0 || health.Unavailable > 0 {
		return "degraded"
	}
	return "online"
}

// loadDashboardTopRouteSNIs fills SNIs for the top routes with one batched
// query instead of a correlated subquery per result row.
func (s *PGStore) loadDashboardTopRouteSNIs(ctx context.Context, routes []DashboardRoute) error {
	if len(routes) == 0 {
		return nil
	}
	ids := make([]string, len(routes))
	index := make(map[string]int, len(routes))
	for i, route := range routes {
		ids[i] = route.RouteID
		index[route.RouteID] = i
	}
	rows, err := s.pool.Query(ctx, `
		SELECT route_id::text,sni FROM route_snis
		WHERE route_id = ANY($1::uuid[])
		ORDER BY route_id,position`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var routeID, sni string
		if err = rows.Scan(&routeID, &sni); err != nil {
			return err
		}
		if i, ok := index[routeID]; ok {
			routes[i].SNIs = append(routes[i].SNIs, sni)
		}
	}
	return rows.Err()
}
