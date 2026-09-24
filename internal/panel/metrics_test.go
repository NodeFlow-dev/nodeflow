package panel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetricsHandlerRequiresBearer(t *testing.T) {
	h := metricsAuthMiddleware("secret-token-32characters-long-x", nil, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/metrics", nil)
	h(w, r)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestMetricsHandlerRejectsWrongToken(t *testing.T) {
	token := "secret-token-32characters-long-x"
	h := metricsAuthMiddleware(token, nil, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/metrics", nil)
	r.Header.Set("Authorization", "Bearer wrong-token")
	h(w, r)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestMetricsHandlerAcceptsValidToken(t *testing.T) {
	token := "secret-token-32characters-long-x"
	h := metricsAuthMiddleware(token, nil, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/metrics", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	h(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestMetricsHandlerRejectsByIPWhenAllowlistSet(t *testing.T) {
	token := "secret-token-32characters-long-x"
	h := metricsAuthMiddleware(token, []string{"10.0.0.0/8"}, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/metrics", nil)
	r.RemoteAddr = "198.51.100.1:12345"
	r.Header.Set("Authorization", "Bearer "+token)
	h(w, r)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestMetricsHandlerAllowsByIPWhenInCIDR(t *testing.T) {
	token := "secret-token-32characters-long-x"
	h := metricsAuthMiddleware(token, []string{"10.0.0.0/8"}, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/metrics", nil)
	r.RemoteAddr = "10.1.2.3:12345"
	r.Header.Set("Authorization", "Bearer "+token)
	h(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestRenderPrometheusMetricsFormat(t *testing.T) {
	lastSeen := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	desired := int64(5)
	actual := int64(5)
	snap := MetricsSnapshot{
		CollectedAt: time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC),
		Nodes: []MetricsNode{
			{
				Node: Node{
					ID:       "11111111-1111-4111-8111-111111111111",
					Name:     "eu-node",
					Status:   "online",
					LastSeen: &lastSeen,
				},
				RoutesTotal:        3,
				RoutesEnabled:      2,
				DesiredRevision:    &desired,
				ActualRevision:     &actual,
				ConfigState:        "in_sync",
				ConnectionsCurrent: 42,
				BackendsHealthy:    2,
				TrafficBytesIn:     1000,
				TrafficBytesOut:    2000,
			},
		},
	}
	w := httptest.NewRecorder()
	RenderPrometheusMetrics(w, snap)
	body := w.Body.String()

	assert.Contains(t, body, "# HELP nodeflow_node_up")
	assert.Contains(t, body, "# TYPE nodeflow_node_up gauge")
	assert.Contains(t, body, `nodeflow_node_up{node="11111111-1111-4111-8111-111111111111",node_name="eu-node"} 1`)
	assert.Contains(t, body, `nodeflow_node_routes_total{node="11111111-1111-4111-8111-111111111111",node_name="eu-node"} 3`)
	assert.Contains(t, body, `nodeflow_node_routes_enabled{node="11111111-1111-4111-8111-111111111111",node_name="eu-node"} 2`)
	assert.Contains(t, body, `nodeflow_node_connections_current{node="11111111-1111-4111-8111-111111111111",node_name="eu-node"} 42`)
	assert.Contains(t, body, `nodeflow_node_desired_revision{node="11111111-1111-4111-8111-111111111111",node_name="eu-node"} 5`)
	assert.Contains(t, body, `nodeflow_node_actual_revision{node="11111111-1111-4111-8111-111111111111",node_name="eu-node"} 5`)
	assert.Contains(t, body, `nodeflow_node_config_in_sync{node="11111111-1111-4111-8111-111111111111",node_name="eu-node"} 1`)
	assert.Contains(t, body, `nodeflow_node_traffic_bytes_in_month{node="11111111-1111-4111-8111-111111111111",node_name="eu-node"} 1000`)
	assert.Contains(t, body, `nodeflow_node_traffic_bytes_out_month{node="11111111-1111-4111-8111-111111111111",node_name="eu-node"} 2000`)
	// No client-IP labels
	assert.NotContains(t, body, "client_ip")
	// No explicit sample timestamps: Prometheus stamps scrape time itself,
	// and explicit stale timestamps break staleness handling.
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "nodeflow_") {
			fields := strings.Fields(line)
			require.Len(t, fields, 2, "metric line must be `name{labels} value`: %s", line)
		}
	}
	assert.Equal(t, "text/plain; version=0.0.4; charset=utf-8", w.Header().Get("Content-Type"))
}

func TestRenderPrometheusMetricsOfflineNode(t *testing.T) {
	snap := MetricsSnapshot{
		CollectedAt: time.Now(),
		Nodes: []MetricsNode{
			{
				Node:        Node{ID: "22222222-2222-4222-8222-222222222222", Status: "offline"},
				ConfigState: "pending",
			},
		},
	}
	w := httptest.NewRecorder()
	RenderPrometheusMetrics(w, snap)
	body := w.Body.String()
	assert.Contains(t, body, `nodeflow_node_up{node="22222222-2222-4222-8222-222222222222",node_name=""} 0`)
	assert.Contains(t, body, `nodeflow_node_config_in_sync{node="22222222-2222-4222-8222-222222222222",node_name=""} 0`)
	assert.Contains(t, body, `nodeflow_node_desired_revision{node="22222222-2222-4222-8222-222222222222",node_name=""} -1`)
}

func TestMetricsRouteDisabledByDefault(t *testing.T) {
	f := &fakeStore{routes: []Route{fallbackRoute(testRouteID)}}
	h := NewHandler(f, Config{
		AdminToken:     "admin-token-32chars-xxxxxxxxxxxx",
		MetricsEnabled: false,
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/metrics", nil)
	r.Header.Set("Authorization", "Bearer secret-token-32chars-xxxxx")
	h.ServeHTTP(w, r)
	// When disabled, /metrics is not registered as a metrics handler;
	// it falls through to the SPA or returns non-metrics content.
	assert.NotContains(t, w.Body.String(), "nodeflow_node_up",
		"nodeflow metrics must not be served when MetricsEnabled=false")
}

func TestMetricsRouteEnabledWithToken(t *testing.T) {
	token := "test-metrics-token-32chars-xxxxx"
	f := &fakeStore{routes: []Route{fallbackRoute(testRouteID)}}
	h := NewHandler(f, Config{
		AdminToken:         "admin-token-32chars-xxxxxxxxxxxx",
		MetricsEnabled:     true,
		MetricsBearerToken: token,
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/metrics", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "nodeflow_node_up")
}
