package panel

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ipv4OnlyGoldenRoutes covers every client table a route can render with
// client_ipv6=false: the source_table stick-table, the source hash (with a
// stale prefix that must be ignored) and the HAProxy bandwidth limiter.
func ipv4OnlyGoldenRoutes() []Route {
	servers := []RouteServer{
		{Position: 0, Name: "a", TargetType: "tcp", Host: "192.0.2.1", Port: 443},
		{Position: 1, Name: "b", TargetType: "tcp", Host: "192.0.2.2", Port: 443},
	}
	download, upload := int64(100), int64(20)
	return []Route{
		{
			ID: "88888888-8888-4888-8888-888888888888", ListenerIP: "*", ListenerPort: 443, MatchMode: "sni",
			SNIs: []string{"table.example.com"}, Hostname: "table.example.com", TargetType: "tcp",
			TargetHost: "192.0.2.1", TargetPort: 443, ProxyProtocol: "none", Enabled: true, HealthCheck: true,
			BalanceMode: "pool", StickyMode: StickyModeSourceTable, StickyEnabled: true, StickyTTL: "1h",
			StickyTableEntries: "100k", Servers: servers, ClientIPv6: boolPointer(false),
		},
		{
			ID: "99999999-9999-4999-8999-999999999999", ListenerIP: "*", ListenerPort: 443, MatchMode: "sni",
			SNIs: []string{"hash.example.com"}, Hostname: "hash.example.com", TargetType: "tcp",
			TargetHost: "192.0.2.1", TargetPort: 443, ProxyProtocol: "none", Enabled: true, HealthCheck: true,
			BalanceMode: "pool", StickyMode: StickyModeSource, StickyEnabled: true, StickyIPv6Prefix: 56,
			Servers: servers, ClientIPv6: boolPointer(false),
		},
		{
			ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", ListenerIP: "*", ListenerPort: 443, Fallback: true,
			MatchMode: "fallback", TargetType: "tcp", TargetHost: "192.0.2.3", TargetPort: 443,
			ProxyProtocol: "none", Enabled: true, ShaperMode: ShaperModeHAProxy,
			ClientDownloadMbps: &download, ClientUploadMbps: &upload, ClientIPv6: boolPointer(false),
		},
	}
}

func TestRenderHAProxyConfigIPv4OnlyGolden(t *testing.T) {
	got, err := RenderHAProxyConfig(ipv4OnlyGoldenRoutes())
	require.NoError(t, err)
	if os.Getenv("NODEFLOW_UPDATE_GOLDEN") != "" {
		require.NoError(t, os.WriteFile("testdata/haproxy_render_ipv4_only.golden.cfg", []byte(got.Config), 0o644))
	}
	want, err := os.ReadFile("testdata/haproxy_render_ipv4_only.golden.cfg")
	require.NoError(t, err)
	assert.Equal(t, string(want), got.Config)
	assert.NotContains(t, got.Config, "ipv6")
	assert.NotContains(t, got.Config, "ipmask")
	assert.Contains(t, got.Config, "    stick-table type ip size 100k expire 1h peers nf_peers\n    stick on src\n")
	assert.Contains(t, got.Config, "    balance source\n")
	assert.Contains(t, got.Config, " key src table nf_bw_download_table_")
	assert.Contains(t, got.Config, "    stick-table type ip size 1m expire 1h store bytes_out_rate(1s)\n")
	assert.Contains(t, got.Config, "    stick-table type ip size 1m expire 1h store bytes_in_rate(1s)\n")
}

func TestClientIPv6DefaultKeepsRender(t *testing.T) {
	routes := ipv4OnlyGoldenRoutes()
	for i := range routes {
		routes[i].ClientIPv6 = nil
	}
	implicit, err := RenderHAProxyConfig(routes)
	require.NoError(t, err)
	for i := range routes {
		routes[i].ClientIPv6 = boolPointer(true)
	}
	explicit, err := RenderHAProxyConfig(routes)
	require.NoError(t, err)
	assert.Equal(t, implicit.Config, explicit.Config)
	assert.Contains(t, explicit.Config, "    stick-table type ipv6 size 100k expire 1h peers nf_peers\n    stick on src,ipmask(32,64)\n")
	assert.Contains(t, explicit.Config, "    balance hash src,ipmask(32,56)\n")
	assert.Contains(t, explicit.Config, " key src,ipmask(32,64) table nf_bw_download_table_")
	assert.Contains(t, explicit.Config, "    stick-table type ipv6 size 1m expire 1h store bytes_out_rate(1s)\n")
}

func TestClientIPv6Validation(t *testing.T) {
	base := `{"name":"p","match_mode":"fallback","sticky_mode":"source_table","sticky_ipv6_prefix":48,` + poolServers

	spec, err := validatePayload(t, base+`}`)
	require.NoError(t, err)
	assert.False(t, spec.ClientIPv4Only, "absent client_ipv6 means true")
	assert.Equal(t, 48, spec.StickyIPv6Prefix)

	spec, err = validatePayload(t, base+`,"client_ipv6":true}`)
	require.NoError(t, err)
	assert.False(t, spec.ClientIPv4Only)
	assert.Equal(t, 48, spec.StickyIPv6Prefix)

	spec, err = validatePayload(t, base+`,"client_ipv6":false}`)
	require.NoError(t, err)
	assert.True(t, spec.ClientIPv4Only)
	assert.Zero(t, spec.StickyIPv6Prefix, "the IPv6 prefix is cleared for IPv4-only routes")

	// Single-target routes keep the flag too (it drives the bwlim tables).
	spec, err = validatePayload(t, `{"name":"s","match_mode":"fallback","target_host":"192.0.2.1","target_port":443,"client_download_mbps":10,"client_ipv6":false}`)
	require.NoError(t, err)
	assert.True(t, spec.ClientIPv4Only)
}

func TestClientIPv6Fingerprint(t *testing.T) {
	spec, err := validatePayload(t, `{"name":"p","match_mode":"fallback","sticky_mode":"source_table",`+poolServers+`}`)
	require.NoError(t, err)
	explicitTrue, err := validatePayload(t, `{"name":"p","match_mode":"fallback","sticky_mode":"source_table","client_ipv6":true,`+poolServers+`}`)
	require.NoError(t, err)
	off, err := validatePayload(t, `{"name":"p","match_mode":"fallback","sticky_mode":"source_table","client_ipv6":false,`+poolServers+`}`)
	require.NoError(t, err)
	assert.Equal(t, routeSpecFingerprint(spec), routeSpecFingerprint(explicitTrue), "default keeps existing fingerprints")
	assert.NotEqual(t, routeSpecFingerprint(spec), routeSpecFingerprint(off))
	assert.False(t, strings.Contains(routeSpecFingerprint(spec), "client"))

	assert.True(t, routeAsSpec(specToRoute(off), true).ClientIPv4Only)
	assert.False(t, routeAsSpec(specToRoute(spec), true).ClientIPv4Only)
}

func TestClientIPv6PartialPutKeepsStoredValue(t *testing.T) {
	stored := Route{ClientIPv6: boolPointer(false)}
	var in routeInput
	mergeAbsentRouteFields(&in, map[string]bool{}, stored)
	require.NotNil(t, in.ClientIPv6)
	assert.False(t, *in.ClientIPv6)

	in = routeInput{ClientIPv6: boolPointer(true)}
	mergeAbsentRouteFields(&in, map[string]bool{"client_ipv6": true}, stored)
	assert.True(t, *in.ClientIPv6)

	assert.True(t, routeInputOmitsStoredFields(map[string]bool{
		"servers": true, "accept_proxy_from": true, "shaper_mode": true, "sticky_mode": true, "sticky_ttl": true,
		"balance_algorithm": true, "balance_random_draws": true, "leastping_tolerance": true, "leastping_tolerance_ms": true,
		"sticky_hash": true, "sticky_hash_balance_factor": true, "sticky_table_entries": true, "sticky_ipv6_prefix": true,
		"slowstart": true,
	}), "client_ipv6 absent must merge the stored value")
}
