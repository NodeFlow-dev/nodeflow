package panel

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// haproxyLogsGoldenRoutes are the routes of haproxy_render.golden.cfg.
func haproxyLogsGoldenRoutes() []Route {
	quota := int64(1000)
	return []Route{
		{
			ID: "33333333-3333-4333-8333-333333333333", ListenerIP: "2001:db8::10", ListenerPort: 8443,
			SNIs: []string{"edge.example.com"}, Hostname: "edge.example.com", TargetType: "tcp",
			TargetHost: "2001:db8::20", TargetPort: 443, ProxyProtocol: "none", Enabled: true,
		},
		{
			ID: "22222222-2222-4222-8222-222222222222", ListenerIP: "*", ListenerPort: 443,
			Fallback: true, TargetType: "unix", UnixSocketPath: "/dev/shm/xray.sock",
			ProxyProtocol: "v1", Enabled: true,
		},
		{
			ID: "11111111-1111-4111-8111-111111111111", ListenerIP: "*", ListenerPort: 443,
			SNIs: []string{"vpn.example.com", "cdn.example.com"}, Hostname: "vpn.example.com",
			TargetType: "tcp", TargetHost: "origin.example.com", TargetPort: 10443,
			ProxyProtocol: "v2", QuotaBytes: &quota, Enabled: true,
			CustomFragment: "  timeout connect 3s\n",
		},
	}
}

var haproxyLogDirectives = []string{
	"    log /dev/log local0\n",
	"    log /dev/log local1 notice\n",
	"    log global\n",
	"    option tcplog\n",
	"    option dontlognull\n",
}

// Default (setting absent / on) must stay byte-identical to the existing
// golden files, renderer header and revision metadata included.
func TestRenderHAProxyLogsDefaultUnchanged(t *testing.T) {
	want, err := os.ReadFile("testdata/haproxy_render.golden.cfg")
	require.NoError(t, err)
	for name, facts := range map[string]NodeRenderFacts{
		"zero facts":       {},
		"explicit enabled": {HAProxyLogsDisabled: false, MemoryBytes: 8 * gib},
	} {
		got, err := RenderHAProxyConfigForNode(haproxyLogsGoldenRoutes(), facts)
		require.NoError(t, err, name)
		assert.Equal(t, string(want), got.Config, name)
		assert.Equal(t, olderV20HAProxyRenderer, got.Renderer, name)
		assert.True(t, got.HAProxyLogs, name)
		_, recorded := renderMetadata(got)["haproxy_logs"]
		assert.False(t, recorded, "default revisions keep their metadata shape")
	}
	tuned, err := os.ReadFile("testdata/haproxy_render_tuned.golden.cfg")
	require.NoError(t, err)
	got, err := RenderHAProxyConfigForNode(tunedGoldenRoutes(), NodeRenderFacts{KernelPipesTuned: true, MemoryBytes: 4 * gib})
	require.NoError(t, err)
	assert.Equal(t, string(tuned), got.Config)
}

func TestRenderHAProxyLogsDisabled(t *testing.T) {
	on, err := RenderHAProxyConfigForNode(haproxyLogsGoldenRoutes(), NodeRenderFacts{})
	require.NoError(t, err)
	off, err := RenderHAProxyConfigForNode(haproxyLogsGoldenRoutes(), NodeRenderFacts{HAProxyLogsDisabled: true})
	require.NoError(t, err)

	want, err := os.ReadFile("testdata/haproxy_render_nologs.golden.cfg")
	require.NoError(t, err)
	assert.Equal(t, string(want), off.Config)
	assert.False(t, off.HAProxyLogs)
	assert.Equal(t, HAProxyRendererVersion, off.Renderer)
	assert.NotEqual(t, on.SHA256, off.SHA256)
	assert.Equal(t, false, renderMetadata(off)["haproxy_logs"])
	for _, directive := range haproxyLogDirectives {
		assert.NotContains(t, off.Config, directive)
		assert.Contains(t, on.Config, directive)
	}
	assert.NotContains(t, off.Config, "tcplog")
	assert.NotContains(t, off.Config, "\n    log ")

	// Only the log directives and the renderer header differ.
	expected := strings.Replace(on.Config, "# renderer: "+olderV20HAProxyRenderer+"\n", "# renderer: "+HAProxyRendererVersion+"\n", 1)
	for _, directive := range haproxyLogDirectives {
		expected = strings.ReplaceAll(expected, directive, "")
	}
	assert.Equal(t, expected, off.Config)
	// Routing metadata is unaffected.
	assert.Equal(t, on.RouteFingerprints, off.RouteFingerprints)
	assert.Equal(t, on.RuntimeNames, off.RuntimeNames)

	// Route-free lifecycle config (last route removed) also drops logging.
	empty, err := renderHAProxyConfigForLifecycle(nil, NodeRenderFacts{HAProxyLogsDisabled: true})
	require.NoError(t, err)
	assert.NotContains(t, empty.Config, "log")
}

// haproxy -c on both modes when a binary is available (HAPROXY_BIN or PATH).
func TestRenderHAProxyLogsValidatesWithHAProxy(t *testing.T) {
	bin := os.Getenv("HAPROXY_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("haproxy"); err != nil {
			t.Skip("haproxy binary not available")
		}
	}
	dir := t.TempDir()
	for name, facts := range map[string]NodeRenderFacts{"on": {}, "off": {HAProxyLogsDisabled: true}} {
		// TCP targets only: haproxy -c resolves nothing and needs no socket.
		routes := []Route{{
			ID: "11111111-1111-4111-8111-111111111111", ListenerIP: "127.0.0.1", ListenerPort: 18443,
			SNIs: []string{"vpn.example.com"}, Hostname: "vpn.example.com", TargetType: "tcp",
			TargetHost: "192.0.2.10", TargetPort: 443, ProxyProtocol: "none", Enabled: true,
		}, {
			ID: "22222222-2222-4222-8222-222222222222", ListenerIP: "127.0.0.1", ListenerPort: 18443,
			Fallback: true, TargetType: "tcp", TargetHost: "192.0.2.11", TargetPort: 443, ProxyProtocol: "none", Enabled: true,
		}}
		got, err := RenderHAProxyConfigForNode(routes, facts)
		require.NoError(t, err)
		// user/group/stats socket need root and /run/haproxy; strip them for -c.
		config := strings.NewReplacer(
			"    stats socket /run/haproxy/admin.sock mode 660 level admin expose-fd listeners\n", "",
			"    user haproxy\n", "", "    group haproxy\n", "",
		).Replace(got.Config)
		path := filepath.Join(dir, name+".cfg")
		require.NoError(t, os.WriteFile(path, []byte(config), 0o600))
		out, err := exec.Command(bin, "-c", "-f", path).CombinedOutput()
		require.NoError(t, err, "%s: %s", name, out)
		t.Logf("haproxy -c (%s): %s", name, strings.TrimSpace(string(out)))
	}
}

func TestMergeNodeMetadataKeepsPanelOwnedSettings(t *testing.T) {
	previous := map[string]any{"haproxy_logs": false, "region": "eu"}
	merged := mergeNodeMetadata(previous, map[string]any{"agent_port": float64(4200)})
	assert.Equal(t, map[string]any{"agent_port": float64(4200), "haproxy_logs": false}, merged)
	merged = mergeNodeMetadata(previous, map[string]any{"haproxy_logs": true})
	assert.Equal(t, map[string]any{"haproxy_logs": true}, merged)
	assert.Equal(t, map[string]any{}, mergeNodeMetadata(nil, map[string]any{}))
	assert.True(t, haproxyLogsEnabled(nil))
	assert.True(t, haproxyLogsEnabled(map[string]any{"haproxy_logs": "false"}))
	assert.False(t, haproxyLogsEnabled(map[string]any{"haproxy_logs": false}))
}

type nodeUpdateRecorder struct {
	*fakeStore
	updated  map[string]any
	created  map[string]any
	previous map[string]any
}

func (r *nodeUpdateRecorder) UpdateNode(_ context.Context, id, name, address string, metadata map[string]any) (Node, error) {
	r.updated = metadata
	merged := mergeNodeMetadata(r.previous, metadata)
	return Node{ID: id, Name: name, Address: address, Metadata: merged}, nil
}

func (r *nodeUpdateRecorder) CreateNode(_ context.Context, name, address string, metadata map[string]any) (Node, error) {
	r.created = metadata
	return Node{ID: testNodeID, Name: name, Address: address, Metadata: metadata}, nil
}

func (r *nodeUpdateRecorder) GetNode(_ context.Context, id string) (Node, error) {
	return Node{ID: id, Name: "edge-1", Address: "192.0.2.10", Metadata: r.previous}, nil
}

func TestNodeHAProxyLogsAPI(t *testing.T) {
	rec := &nodeUpdateRecorder{fakeStore: &fakeStore{}, previous: map[string]any{"haproxy_logs": false}}
	h := NewHandler(rec, Config{AdminToken: testAdminToken})
	decodeNode := func(body string) map[string]any {
		t.Helper()
		var out map[string]any
		require.NoError(t, json.Unmarshal([]byte(body), &out))
		return out
	}

	// GET exposes the effective value.
	w := request(t, h, "GET", "/api/v1/nodes/"+testNodeID, "", testAdminToken)
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Equal(t, false, decodeNode(w.Body.String())["haproxy_logs"])

	// Old client: metadata without the key, no top-level field -> stored value kept.
	w = request(t, h, "PUT", "/api/v1/nodes/"+testNodeID, `{"name":"edge-1","address":"192.0.2.10","metadata":{"agent_port":4200}}`, testAdminToken)
	require.Equal(t, 200, w.Code, w.Body.String())
	_, sent := rec.updated["haproxy_logs"]
	assert.False(t, sent, "handler must not inject a default")
	assert.Equal(t, false, decodeNode(w.Body.String())["haproxy_logs"])

	// Top-level field switches it.
	w = request(t, h, "PUT", "/api/v1/nodes/"+testNodeID, `{"name":"edge-1","address":"192.0.2.10","metadata":{},"haproxy_logs":true}`, testAdminToken)
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Equal(t, true, rec.updated["haproxy_logs"])
	assert.Equal(t, true, decodeNode(w.Body.String())["haproxy_logs"])

	// Validation.
	for _, body := range []string{
		`{"name":"edge-1","address":"192.0.2.10","haproxy_logs":"no"}`,
		`{"name":"edge-1","address":"192.0.2.10","metadata":{"haproxy_logs":"false"}}`,
		`{"name":"edge-1","address":"192.0.2.10","metadata":{"haproxy_logs":true},"haproxy_logs":false}`,
	} {
		w = request(t, h, "PUT", "/api/v1/nodes/"+testNodeID, body, testAdminToken)
		assert.Equal(t, 400, w.Code, body+" -> "+w.Body.String())
	}

	// Create: omitted = default on (key absent), explicit off stored.
	w = request(t, h, "POST", "/api/v1/nodes", `{"name":"edge-2","address":"192.0.2.11"}`, testAdminToken)
	require.Equal(t, 201, w.Code, w.Body.String())
	_, sent = rec.created["haproxy_logs"]
	assert.False(t, sent)
	assert.Equal(t, true, decodeNode(w.Body.String())["haproxy_logs"])
	w = request(t, h, "POST", "/api/v1/nodes", `{"name":"edge-2","address":"192.0.2.11","haproxy_logs":false}`, testAdminToken)
	require.Equal(t, 201, w.Code, w.Body.String())
	assert.Equal(t, false, rec.created["haproxy_logs"])
	assert.Equal(t, false, decodeNode(w.Body.String())["haproxy_logs"])
}
