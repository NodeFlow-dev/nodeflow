package panel

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const gib = int64(1) << 30

func TestNodeRenderFactsFromMetrics(t *testing.T) {
	cases := []struct {
		name    string
		metrics map[string]any
		want    NodeRenderFacts
	}{
		{"empty", map[string]any{}, NodeRenderFacts{}},
		{"memory bucketed to 256 MiB", map[string]any{"memory_total_bytes": float64(2063208448)}, NodeRenderFacts{MemoryBytes: 7 * 256 << 20}},
		{"tuned", map[string]any{"kernel_pipes": map[string]any{"tuned": true, "pipe_max_size": float64(1048576), "pipe_user_pages_soft": float64(0)}}, NodeRenderFacts{KernelPipesTuned: true}},
		{"tuned flag but soft limit", map[string]any{"kernel_pipes": map[string]any{"tuned": true, "pipe_max_size": float64(1048576), "pipe_user_pages_soft": float64(16384)}}, NodeRenderFacts{}},
		{"tuned flag but small max", map[string]any{"kernel_pipes": map[string]any{"tuned": true, "pipe_max_size": float64(65536), "pipe_user_pages_soft": float64(0)}}, NodeRenderFacts{}},
		{"not tuned", map[string]any{"kernel_pipes": map[string]any{"tuned": false, "pipe_max_size": float64(1048576), "pipe_user_pages_soft": float64(0)}}, NodeRenderFacts{}},
		{"malformed", map[string]any{"memory_total_bytes": "lots", "kernel_pipes": "yes"}, NodeRenderFacts{}},
		{"negative memory", map[string]any{"memory_total_bytes": float64(-1)}, NodeRenderFacts{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, nodeRenderFactsFromMetrics(tc.metrics))
		})
	}
}

// The heartbeat handler decodes with UseNumber, so live metrics carry
// json.Number, not float64. Regression: facts were silently "unknown" and the
// node-facts republish never fired on real nodes.
func TestNodeRenderFactsFromUseNumberHeartbeat(t *testing.T) {
	dec := json.NewDecoder(strings.NewReader(`{"memory_total_bytes":2063208448,"kernel_pipes":{"tuned":true,"pipe_max_size":1048576,"pipe_user_pages_soft":0,"managed":true}}`))
	dec.UseNumber()
	var metrics map[string]any
	require.NoError(t, dec.Decode(&metrics))
	assert.Equal(t, NodeRenderFacts{}, nodeRenderFactsFromMetrics(metrics), "raw json.Number values are not understood")
	assert.Equal(t, NodeRenderFacts{MemoryBytes: 7 * 256 << 20, KernelPipesTuned: true}, nodeRenderFactsFromMetrics(jsonNormalizedMetrics(metrics)))
}

func TestAutoStickyTableEntries(t *testing.T) {
	cases := []struct {
		memory int64
		tables int
		want   string
	}{
		{0, 1, "1m"},             // unknown RAM
		{1 * gib, 1, "229k"},     // 1 GiB * 5 % / 228 B = 235 469 entries
		{2 * gib, 1, "459k"},     // 470 938
		{2 * gib, 2, "229k"},     // shared by two auto tables
		{4 * gib, 1, "919k"},     // 941 876
		{8 * gib, 4, "459k"},     //
		{16 * gib, 0, "3679k"},   // N < 1 counts as 1
		{256 << 20, 1, "100k"},   // clamped up to 100k
		{512 * gib, 1, "10m"},    // clamped down to 10m
		{1 * gib, 1000, "100k"},  // many tables still get 100k each
		{64 * gib, 1, "10m"},     // 15 069 000 > 10m (10 485 760)
		{48 * gib, 1, "10m"},     // 11 301 752 > 10m
		{40 * gib, 1, "9198k"},   // 9 418 524 entries = 9197.8k
		{3 * gib / 2, 1, "344k"}, // 1.5 GiB
	}
	for _, tc := range cases {
		got := NodeRenderFacts{MemoryBytes: tc.memory}.AutoStickyTableEntries(tc.tables)
		assert.Equal(t, tc.want, got, "memory=%d tables=%d", tc.memory, tc.tables)
		assert.True(t, validStickyTableEntries(got), got)
	}
}

func TestPipeSizeFromFacts(t *testing.T) {
	assert.Equal(t, 262144, NodeRenderFacts{}.PipeSize())
	assert.Equal(t, 1048576, NodeRenderFacts{KernelPipesTuned: true}.PipeSize())
}

func tunedGoldenRoutes() []Route {
	servers := []RouteServer{
		{Position: 0, Name: "a", TargetType: "tcp", Host: "192.0.2.1", Port: 443},
		{Position: 1, Name: "b", TargetType: "tcp", Host: "192.0.2.2", Port: 443},
	}
	return []Route{
		{
			ID: "44444444-4444-4444-8444-444444444444", ListenerIP: "*", ListenerPort: 443, MatchMode: "sni",
			SNIs: []string{"auto.example.com"}, Hostname: "auto.example.com", TargetType: "tcp",
			TargetHost: "192.0.2.1", TargetPort: 443, ProxyProtocol: "none", Enabled: true, HealthCheck: true,
			BalanceMode: "pool", StickyMode: StickyModeSourceTable, StickyEnabled: true, StickyTTL: "1h", Servers: servers,
		},
		{
			ID: "55555555-5555-4555-8555-555555555555", ListenerIP: "*", ListenerPort: 443, MatchMode: "sni",
			SNIs: []string{"auto2.example.com"}, Hostname: "auto2.example.com", TargetType: "tcp",
			TargetHost: "192.0.2.1", TargetPort: 443, ProxyProtocol: "none", Enabled: true, HealthCheck: true,
			BalanceMode: "pool", StickyMode: StickyModeSourceTable, StickyEnabled: true, StickyTTL: "6h", Servers: servers,
		},
		{
			ID: "66666666-6666-4666-8666-666666666666", ListenerIP: "*", ListenerPort: 443, MatchMode: "sni",
			SNIs: []string{"fixed.example.com"}, Hostname: "fixed.example.com", TargetType: "tcp",
			TargetHost: "192.0.2.1", TargetPort: 443, ProxyProtocol: "none", Enabled: true, HealthCheck: true,
			BalanceMode: "pool", StickyMode: StickyModeSourceTable, StickyEnabled: true, StickyTTL: "1h",
			StickyTableEntries: "50k", Servers: servers,
		},
		{
			// Disabled auto routes do not share the RAM budget.
			ID: "77777777-7777-4777-8777-777777777777", ListenerIP: "*", ListenerPort: 443, MatchMode: "sni",
			SNIs: []string{"off.example.com"}, Hostname: "off.example.com", TargetType: "tcp",
			TargetHost: "192.0.2.1", TargetPort: 443, ProxyProtocol: "none", Enabled: false, HealthCheck: true,
			BalanceMode: "pool", StickyMode: StickyModeSourceTable, StickyEnabled: true, Servers: servers,
		},
	}
}

func TestRenderHAProxyConfigTunedNodeGolden(t *testing.T) {
	facts := NodeRenderFacts{KernelPipesTuned: true, MemoryBytes: 4 * gib}
	got, err := RenderHAProxyConfigForNode(tunedGoldenRoutes(), facts)
	require.NoError(t, err)
	want, err := os.ReadFile("testdata/haproxy_render_tuned.golden.cfg")
	require.NoError(t, err)
	assert.Equal(t, string(want), got.Config)
	assert.Equal(t, HAProxyRendererVersion, got.Renderer)
	assert.Equal(t, 1048576, got.PipeSize)
	assert.Equal(t, "459k", got.AutoStickyTableEntries) // 4 GiB shared by two auto tables
	assert.Contains(t, got.Config, "# renderer: haproxy-tcp-sni-v21\n")
	assert.Contains(t, got.Config, "    tune.pipesize 1048576\n")
	assert.Equal(t, 2, strings.Count(got.Config, "stick-table type ipv6 size 459k "))
	assert.Contains(t, got.Config, "stick-table type ipv6 size 50k expire 1h")
	meta := renderMetadata(got)
	assert.Equal(t, 1048576, meta["pipe_size"])
	assert.Equal(t, "459k", meta["auto_sticky_table_entries"])
}

func TestRenderHAProxyConfigNodeFactsVariants(t *testing.T) {
	routes := tunedGoldenRoutes()
	// Auto tables without known RAM: 1m, 256 KiB pipes, still v21.
	got, err := RenderHAProxyConfigForNode(routes, NodeRenderFacts{})
	require.NoError(t, err)
	assert.Equal(t, HAProxyRendererVersion, got.Renderer)
	assert.Contains(t, got.Config, "    tune.pipesize 262144\n")
	assert.Equal(t, 2, strings.Count(got.Config, "stick-table type ipv6 size 1m "))

	// No auto table and untuned kernel: v20 byte-identical, whatever the RAM.
	fixed := []Route{routes[2]}
	plain, err := RenderHAProxyConfigForNode(fixed, NodeRenderFacts{MemoryBytes: 8 * gib})
	require.NoError(t, err)
	legacy, err := RenderHAProxyConfig(fixed)
	require.NoError(t, err)
	assert.Equal(t, legacy.Config, plain.Config)
	assert.Equal(t, olderV20HAProxyRenderer, plain.Renderer)
	assert.Contains(t, plain.Config, "# renderer: haproxy-tcp-sni-v20\n")
	assert.Empty(t, plain.AutoStickyTableEntries)

	// Tuned kernel only: v21 with 1 MiB pipes, otherwise identical.
	tuned, err := RenderHAProxyConfigForNode(fixed, NodeRenderFacts{KernelPipesTuned: true})
	require.NoError(t, err)
	assert.Equal(t, HAProxyRendererVersion, tuned.Renderer)
	expected := strings.Replace(strings.Replace(legacy.Config, "haproxy-tcp-sni-v20", "haproxy-tcp-sni-v21", 1), "tune.pipesize 262144", "tune.pipesize 1048576", 1)
	assert.Equal(t, expected, tuned.Config)
}

func TestAnnotateStickyTableEffective(t *testing.T) {
	routes := tunedGoldenRoutes()
	routes = append(routes, Route{ID: "88888888-8888-4888-8888-888888888888", Enabled: true, BalanceMode: "pool", StickyMode: StickyModeSource, StickyEnabled: true})
	annotateStickyTableEffective(routes, NodeRenderFacts{MemoryBytes: 4 * gib})
	assert.Equal(t, "459k", routes[0].StickyTableEntriesEffective)
	assert.Equal(t, "459k", routes[1].StickyTableEntriesEffective)
	assert.Equal(t, "50k", routes[2].StickyTableEntriesEffective)
	// A disabled auto route shows its size once enabled (3 auto tables).
	assert.Equal(t, "306k", routes[3].StickyTableEntriesEffective)
	assert.Empty(t, routes[4].StickyTableEntriesEffective)

	annotateStickyTableEffective(routes, NodeRenderFacts{})
	assert.Equal(t, "1m", routes[0].StickyTableEntriesEffective)
}

func TestRouteGetExposesStickyTableEffective(t *testing.T) {
	routes := tunedGoldenRoutes()
	f := &fakeStore{routes: routes, renderFacts: NodeRenderFacts{MemoryBytes: 4 * gib}}
	w := request(t, handler(f), http.MethodGet, "/api/v1/nodes/"+testNodeID+"/routes/"+routes[0].ID, "", testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"sticky_table_entries":""`)
	assert.Contains(t, w.Body.String(), `"sticky_table_entries_effective":"459k"`)

	w = request(t, handler(f), http.MethodGet, "/api/v1/nodes/"+testNodeID+"/routes", "", testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, 2, strings.Count(w.Body.String(), `"sticky_table_entries_effective":"459k"`))
	assert.Contains(t, w.Body.String(), `"sticky_table_entries_effective":"50k"`)
}
