package panel

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// MetricsSnapshot holds all data needed to render /metrics in one DB round-trip.
type MetricsSnapshot struct {
	CollectedAt time.Time
	Nodes       []MetricsNode
}

type MetricsNode struct {
	Node            Node
	LatestHeartbeat *NodeHeartbeat
	RoutesTotal     int
	RoutesEnabled   int
	DesiredRevision *int64
	ActualRevision  *int64
	ConfigState     string
	LastError       string
	// from heartbeat metrics
	ConnectionsCurrent  uint64
	BackendsHealthy     int
	BackendsDegraded    int
	BackendsUnavailable int
	HAProxyServiceState string
	// traffic this calendar month
	TrafficBytesIn  int64
	TrafficBytesOut int64
}

// configStateLister is implemented by stores that can load many node config
// states in one query.
type configStateLister interface {
	ListConfigStates(context.Context, []string) (map[string]NodeConfigState, error)
}

// CollectMetrics gathers a MetricsSnapshot from existing store data.
// It calls the same internal helpers the dashboard already uses, so no
// new queries are needed beyond what the dashboard endpoint already executes.
func CollectMetrics(ctx context.Context, store Store) (MetricsSnapshot, error) {
	overview, err := store.GetDashboardOverview(ctx, "", "1h")
	if err != nil {
		return MetricsSnapshot{}, err
	}
	snap := MetricsSnapshot{CollectedAt: time.Now().UTC()}
	// Batch config state lookups when the store supports it: one query for
	// all nodes instead of one GetConfigState round-trip per node.
	var configStates map[string]NodeConfigState
	if lister, ok := store.(configStateLister); ok {
		ids := make([]string, len(overview.Nodes))
		for i, dn := range overview.Nodes {
			ids[i] = dn.Node.ID
		}
		configStates, err = lister.ListConfigStates(ctx, ids)
		if err != nil {
			return MetricsSnapshot{}, err
		}
	}
	for _, dn := range overview.Nodes {
		mn := MetricsNode{
			Node:            dn.Node,
			RoutesTotal:     dn.RoutesTotal,
			RoutesEnabled:   dn.RoutesEnabled,
			TrafficBytesIn:  dn.TrafficBytesIn,
			TrafficBytesOut: dn.TrafficBytesOut,
		}
		if dn.LatestHeartbeat != nil {
			mn.LatestHeartbeat = dn.LatestHeartbeat
		}
		// Config state
		var cs NodeConfigState
		var found bool
		if configStates != nil {
			cs, found = configStates[dn.Node.ID]
		} else {
			var stateErr error
			cs, stateErr = store.GetConfigState(ctx, dn.Node.ID)
			found = stateErr == nil
		}
		if found {
			mn.DesiredRevision = cs.DesiredRevision
			mn.ActualRevision = cs.ActualRevision
			mn.ConfigState = cs.State
			mn.LastError = cs.LastError
		}
		// Runtime metrics from heartbeat
		if dn.LatestHeartbeat != nil && dn.LatestHeartbeat.Metrics != nil {
			runtime, _ := dn.LatestHeartbeat.Metrics["haproxy_runtime"].(map[string]any)
			if runtime != nil {
				if v, ok := runtime["connections_current"]; ok {
					if f, ok := v.(float64); ok {
						mn.ConnectionsCurrent = uint64(f)
					}
				}
				health := dashboardRuntimeHealth(runtime)
				mn.BackendsHealthy = health.Healthy
				mn.BackendsDegraded = health.Degraded
				mn.BackendsUnavailable = health.Unavailable
			}
			if svc, ok := dn.LatestHeartbeat.Metrics["haproxy_service"].(map[string]any); ok {
				mn.HAProxyServiceState, _ = svc["active_state"].(string)
			}
		}
		snap.Nodes = append(snap.Nodes, mn)
	}
	return snap, nil
}

// RenderPrometheusMetrics writes a Prometheus text format response.
// Format is hand-written (no external dependency).
func RenderPrometheusMetrics(w http.ResponseWriter, snap MetricsSnapshot) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	b := &strings.Builder{}

	writeHelp := func(name, typ, help string) {
		fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
	}

	writeHelp("nodeflow_node_up", "gauge", "1 if node is online or degraded, 0 if offline")
	for _, n := range snap.Nodes {
		up := 0
		if n.Node.Status == "online" || n.Node.Status == "degraded" {
			up = 1
		}
		fmt.Fprintf(b, "nodeflow_node_up{node=%q,node_name=%q} %d\n", n.Node.ID, n.Node.Name, up)
	}

	writeHelp("nodeflow_node_last_seen_seconds", "gauge", "Unix timestamp of the most recent agent heartbeat, or 0 if never seen")
	for _, n := range snap.Nodes {
		val := "0"
		if n.Node.LastSeen != nil {
			val = fmt.Sprintf("%d", n.Node.LastSeen.Unix())
		}
		fmt.Fprintf(b, "nodeflow_node_last_seen_seconds{node=%q,node_name=%q} %s\n", n.Node.ID, n.Node.Name, val)
	}

	writeHelp("nodeflow_node_routes_total", "gauge", "Total routes on the node")
	for _, n := range snap.Nodes {
		fmt.Fprintf(b, "nodeflow_node_routes_total{node=%q,node_name=%q} %d\n", n.Node.ID, n.Node.Name, n.RoutesTotal)
	}

	writeHelp("nodeflow_node_routes_enabled", "gauge", "Enabled routes on the node")
	for _, n := range snap.Nodes {
		fmt.Fprintf(b, "nodeflow_node_routes_enabled{node=%q,node_name=%q} %d\n", n.Node.ID, n.Node.Name, n.RoutesEnabled)
	}

	writeHelp("nodeflow_node_connections_current", "gauge", "Current TCP connections reported by HAProxy")
	for _, n := range snap.Nodes {
		fmt.Fprintf(b, "nodeflow_node_connections_current{node=%q,node_name=%q} %d\n", n.Node.ID, n.Node.Name, n.ConnectionsCurrent)
	}

	writeHelp("nodeflow_node_backends_healthy", "gauge", "Number of healthy HAProxy backend servers")
	for _, n := range snap.Nodes {
		fmt.Fprintf(b, "nodeflow_node_backends_healthy{node=%q,node_name=%q} %d\n", n.Node.ID, n.Node.Name, n.BackendsHealthy)
	}

	writeHelp("nodeflow_node_backends_degraded", "gauge", "Number of degraded HAProxy backend servers")
	for _, n := range snap.Nodes {
		fmt.Fprintf(b, "nodeflow_node_backends_degraded{node=%q,node_name=%q} %d\n", n.Node.ID, n.Node.Name, n.BackendsDegraded)
	}

	writeHelp("nodeflow_node_backends_unavailable", "gauge", "Number of unavailable HAProxy backend servers")
	for _, n := range snap.Nodes {
		fmt.Fprintf(b, "nodeflow_node_backends_unavailable{node=%q,node_name=%q} %d\n", n.Node.ID, n.Node.Name, n.BackendsUnavailable)
	}

	writeHelp("nodeflow_node_traffic_bytes_in_month", "gauge", "Bytes received this calendar month")
	for _, n := range snap.Nodes {
		fmt.Fprintf(b, "nodeflow_node_traffic_bytes_in_month{node=%q,node_name=%q} %d\n", n.Node.ID, n.Node.Name, n.TrafficBytesIn)
	}

	writeHelp("nodeflow_node_traffic_bytes_out_month", "gauge", "Bytes sent this calendar month")
	for _, n := range snap.Nodes {
		fmt.Fprintf(b, "nodeflow_node_traffic_bytes_out_month{node=%q,node_name=%q} %d\n", n.Node.ID, n.Node.Name, n.TrafficBytesOut)
	}

	writeHelp("nodeflow_node_desired_revision", "gauge", "Desired config revision assigned to the node (-1 if none)")
	for _, n := range snap.Nodes {
		val := int64(-1)
		if n.DesiredRevision != nil {
			val = *n.DesiredRevision
		}
		fmt.Fprintf(b, "nodeflow_node_desired_revision{node=%q,node_name=%q} %d\n", n.Node.ID, n.Node.Name, val)
	}

	writeHelp("nodeflow_node_actual_revision", "gauge", "Actual config revision running on the node (-1 if none)")
	for _, n := range snap.Nodes {
		val := int64(-1)
		if n.ActualRevision != nil {
			val = *n.ActualRevision
		}
		fmt.Fprintf(b, "nodeflow_node_actual_revision{node=%q,node_name=%q} %d\n", n.Node.ID, n.Node.Name, val)
	}

	writeHelp("nodeflow_node_config_in_sync", "gauge", "1 if desired_revision == actual_revision (config fully applied)")
	for _, n := range snap.Nodes {
		inSync := 0
		if n.ConfigState == "in_sync" {
			inSync = 1
		}
		fmt.Fprintf(b, "nodeflow_node_config_in_sync{node=%q,node_name=%q} %d\n", n.Node.ID, n.Node.Name, inSync)
	}

	fmt.Fprint(w, b.String())
}

// metricsAuthMiddleware returns 401/403 when the request does not carry a
// valid bearer token or (optionally) does not originate from an allowed CIDR.
// If no allowCIDRs are configured, source IP is not checked.
func metricsAuthMiddleware(bearerToken string, allowCIDRs []string, next http.HandlerFunc) http.HandlerFunc {
	var nets []*net.IPNet
	for _, cidr := range allowCIDRs {
		if ip := net.ParseIP(cidr); ip != nil {
			var bits int
			if ip.To4() != nil {
				bits = 32
			} else {
				bits = 128
			}
			_, ipnet, _ := net.ParseCIDR(ip.String() + fmt.Sprintf("/%d", bits))
			nets = append(nets, ipnet)
		} else if _, ipnet, err := net.ParseCIDR(cidr); err == nil {
			nets = append(nets, ipnet)
		}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if len(nets) > 0 {
			clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)
			ip := net.ParseIP(clientIP)
			allowed := false
			for _, n := range nets {
				if ip != nil && n.Contains(ip) {
					allowed = true
					break
				}
			}
			if !allowed {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		auth := r.Header.Get("Authorization")
		token := strings.TrimPrefix(auth, "Bearer ")
		if auth == "" || !strings.HasPrefix(auth, "Bearer ") || bearerToken == "" ||
			subtle.ConstantTimeCompare([]byte(token), []byte(bearerToken)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="nodeflow-metrics"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}
