package panel

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGzipCompressesLargeJSON(t *testing.T) {
	body := `{"data":"` + strings.Repeat("a", 4096) + `"}`
	handler := gzipUIResponses(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
	assert.Contains(t, rec.Header().Values("Vary"), "Accept-Encoding")
	assert.Less(t, rec.Body.Len(), len(body)/10)
	reader, err := gzip.NewReader(rec.Body)
	require.NoError(t, err)
	decoded, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, body, string(decoded))
}

func TestGzipSkipsSmallAndIneligibleResponses(t *testing.T) {
	large := strings.Repeat("x", 4096)
	cases := []struct {
		name        string
		path        string
		accept      string
		contentType string
		status      int
		body        string
	}{
		{"small json", "/api/v1/x", "gzip", "application/json", 200, `{"ok":true}`},
		{"no accept", "/api/v1/x", "", "application/json", 200, large},
		{"q zero", "/api/v1/x", "gzip;q=0", "application/json", 200, large},
		{"binary", "/api/v1/x", "gzip", "application/octet-stream", 200, large},
		{"agent path", "/agent/v1/heartbeat", "gzip", "application/json", 200, large},
		{"auth path", "/auth/session", "gzip", "application/json", 200, large},
		{"error status small", "/api/v1/x", "gzip", "application/json", 404, `{"error":{}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := gzipUIResponses(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.accept != "" {
				req.Header.Set("Accept-Encoding", tc.accept)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			assert.Equal(t, tc.status, rec.Code)
			assert.Empty(t, rec.Header().Get("Content-Encoding"))
			assert.Equal(t, tc.body, rec.Body.String())
		})
	}
}

func TestGzipEmbeddedWebAssets(t *testing.T) {
	handler := NewHandler(&fakeStore{}, Config{AdminToken: strings.Repeat("a", 32)})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	if rec.Header().Get("Content-Encoding") == "gzip" {
		reader, err := gzip.NewReader(rec.Body)
		require.NoError(t, err)
		decoded, err := io.ReadAll(reader)
		require.NoError(t, err)
		assert.Contains(t, strings.ToLower(string(decoded)), "<html")
	} else {
		assert.Less(t, rec.Body.Len(), gzipMinBytes, "only small documents may be served uncompressed")
	}
	// Security headers still apply.
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
}

func TestLoadDataRetentionPolicy(t *testing.T) {
	policy, err := LoadDataRetentionPolicy()
	require.NoError(t, err)
	assert.Equal(t, DefaultDataRetentionPolicy(), policy)

	t.Setenv("PANEL_RETENTION_INTERVAL", "30m")
	t.Setenv("PANEL_RETENTION_QUOTA_USAGE_DAYS", "0")
	t.Setenv("PANEL_RETENTION_APPLY_REPORTS_DAYS", "30")
	t.Setenv("PANEL_RETENTION_TRAFFIC_MONTHLY_MONTHS", "12")
	policy, err = LoadDataRetentionPolicy()
	require.NoError(t, err)
	assert.Equal(t, 30*time.Minute, policy.Interval)
	assert.Zero(t, policy.QuotaUsageRetentionDays)
	assert.Equal(t, 30, policy.ApplyReportRetentionDays)
	assert.Equal(t, 12, policy.TrafficMonthlyRetentionMons)

	t.Setenv("PANEL_RETENTION_TRAFFIC_MONTHLY_MONTHS", "1")
	_, err = LoadDataRetentionPolicy()
	assert.Error(t, err, "keeping less than the previous month would erase live reporting")
	t.Setenv("PANEL_RETENTION_TRAFFIC_MONTHLY_MONTHS", "12")
	t.Setenv("PANEL_RETENTION_INTERVAL", "5s")
	_, err = LoadDataRetentionPolicy()
	assert.Error(t, err)
}

func TestDataRetentionTargetsNeverTouchCurrentWindows(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	targets := dataRetentionTargets(now, DefaultDataRetentionPolicy())
	byTable := map[string]retentionTarget{}
	for _, target := range targets {
		byTable[target.table] = target
	}
	require.Len(t, byTable, 5)
	assert.Equal(t, now.Add(-trafficRateRetention), byTable["node_traffic_rate_samples"].cutoff)
	assert.Equal(t, now.AddDate(0, 0, -93), byTable["traffic_quota_usage"].cutoff)
	assert.Equal(t, "window_end < $1", byTable["traffic_quota_usage"].where)
	assert.Equal(t, time.Date(2024, 9, 1, 0, 0, 0, 0, time.UTC), byTable["traffic_monthly"].cutoff)

	disabled := dataRetentionTargets(now, DataRetentionPolicy{})
	assert.Len(t, disabled, 2, "rate samples are always bounded; optional tables are opt-out")
}

type countingConfigStateStore struct {
	fakeStore
	single int
	batch  int
}

func (s *countingConfigStateStore) GetConfigState(context.Context, string) (NodeConfigState, error) {
	s.single++
	return NodeConfigState{}, ErrNotFound
}

func (s *countingConfigStateStore) ListConfigStates(_ context.Context, ids []string) (map[string]NodeConfigState, error) {
	s.batch++
	desired := int64(7)
	return map[string]NodeConfigState{ids[0]: {NodeID: ids[0], DesiredRevision: &desired, State: "in_sync"}}, nil
}

func TestCollectMetricsBatchesConfigState(t *testing.T) {
	store := &countingConfigStateStore{}
	store.dashboardOverview = DashboardOverview{Nodes: []DashboardNode{
		{Node: Node{ID: "11111111-1111-4111-8111-111111111111"}},
		{Node: Node{ID: "22222222-2222-4222-8222-222222222222"}},
		{Node: Node{ID: "33333333-3333-4333-8333-333333333333"}},
	}}
	snap, err := CollectMetrics(context.Background(), store)
	require.NoError(t, err)
	assert.Equal(t, 1, store.batch)
	assert.Zero(t, store.single)
	require.Len(t, snap.Nodes, 3)
	assert.Equal(t, "in_sync", snap.Nodes[0].ConfigState)
	require.NotNil(t, snap.Nodes[0].DesiredRevision)
	assert.Empty(t, snap.Nodes[1].ConfigState)
}
