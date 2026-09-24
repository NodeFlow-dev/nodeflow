package panel

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderHAProxyConfigGoldenAndDeterministic(t *testing.T) {
	quota := int64(1000)
	routes := []Route{
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
		{
			ID: "disabled\nbackend injected", Enabled: false, ListenerIP: "\n", ListenerPort: -1,
			TargetHost: "127.0.0.1\nserver injected", CustomFragment: "backend injected",
		},
	}

	got, err := RenderHAProxyConfig(routes)
	require.NoError(t, err)
	want, err := os.ReadFile("testdata/haproxy_render.golden.cfg")
	require.NoError(t, err)
	assert.Equal(t, string(want), got.Config)
	assert.Len(t, got.SHA256, 64)
	// Without node facts the output stays byte-identical v20 (golden unchanged).
	assert.Equal(t, olderV20HAProxyRenderer, got.Renderer)
	assert.Equal(t, 3, got.EnabledRoutes)
	assert.Equal(t, 2, got.Listeners)
	assert.Equal(t, []int{443, 8443}, got.ListenerPorts)
	assert.Equal(t, 1, got.QuotaMetadataRoutes)
	assert.Equal(t, 1, got.ManualBackendRoutes)
	assert.Equal(t, []string{
		"observe-only traffic quotas do not block HAProxy connections",
		"manual backend directives are rendered and must pass HAProxy validation before apply",
	}, got.Warnings)
	assert.Equal(t, "nf_be_111111111111", got.RouteBackends["11111111-1111-4111-8111-111111111111"])
	assert.NotContains(t, got.Config, "backend injected")
	assert.Contains(t, got.Config, "    timeout connect 3s\n")
	assert.NotContains(t, got.Config, "\n    daemon\n")

	reversed := slices.Clone(routes)
	slices.Reverse(reversed)
	again, err := RenderHAProxyConfig(reversed)
	require.NoError(t, err)
	assert.Equal(t, got.Config, again.Config)
	assert.Equal(t, got.SHA256, again.SHA256)
	assert.Equal(t, got.RuntimeNames, again.RuntimeNames)
	assert.Equal(t, got.RouteBackends, again.RouteBackends)
}

func TestRenderHAProxyConfigScopesManualDirectivesToOneBackend(t *testing.T) {
	first := fallbackRoute("11111111-1111-4111-8111-111111111111")
	first.ListenerPort = 443
	first.CustomFragment = "\t timeout connect 17s\r\n\toption redispatch\r\n"
	second := fallbackRoute("22222222-2222-4222-8222-222222222222")
	second.ListenerPort = 8443

	got, err := RenderHAProxyConfig([]Route{second, first})
	require.NoError(t, err)
	firstStart := strings.Index(got.Config, "\nbackend "+RouteBackendKey(first.ID)+"\n")
	secondStart := strings.Index(got.Config, "\nbackend "+RouteBackendKey(second.ID)+"\n")
	require.GreaterOrEqual(t, firstStart, 0)
	require.Greater(t, secondStart, firstStart)
	assert.Contains(t, got.Config[firstStart:secondStart], "    timeout connect 17s\n    option redispatch\n")
	assert.NotContains(t, got.Config[secondStart:], "timeout connect 17s")
	assert.Equal(t, 1, got.ManualBackendRoutes)

	canonical := first
	canonical.CustomFragment = "    timeout connect 17s\n    option redispatch"
	again, err := RenderHAProxyConfig([]Route{canonical, second})
	require.NoError(t, err)
	assert.Equal(t, got.Config, again.Config)
	assert.Equal(t, got.SHA256, again.SHA256)
	assert.Equal(t, got.RouteFingerprints, again.RouteFingerprints)

	withoutManual := canonical
	withoutManual.CustomFragment = ""
	plain, err := RenderHAProxyConfig([]Route{withoutManual, second})
	require.NoError(t, err)
	assert.NotEqual(t, got.RouteFingerprints[first.ID], plain.RouteFingerprints[first.ID])
}

func TestRenderHAProxyConfigRejectsManualSectionAndEscapedInjection(t *testing.T) {
	for _, fragment := range []string{
		"backend injected\n    server attacker 192.0.2.1:443",
		"    frontend injected",
		"    timeout connect 5s\\\n    backend injected",
	} {
		route := fallbackRoute(testRouteID)
		route.CustomFragment = fragment
		_, err := RenderHAProxyConfig([]Route{route})
		var invalid *RouteSetError
		require.ErrorAs(t, err, &invalid)
		assert.Contains(t, err.Error(), "custom_fragment")
	}
}

func TestSupportedHAProxyRendererVersionsIncludeV1ThroughV21(t *testing.T) {
	assert.Equal(t, []string{
		"haproxy-tcp-sni-v21",
		"haproxy-tcp-sni-v20",
		"haproxy-tcp-sni-v19",
		"haproxy-tcp-sni-v18",
		"haproxy-tcp-sni-v17",
		"haproxy-tcp-sni-v16",
		"haproxy-tcp-sni-v15",
		"haproxy-tcp-sni-v14",
		"haproxy-tcp-sni-v13",
		"haproxy-tcp-sni-v12",
		"haproxy-tcp-sni-v11",
		"haproxy-tcp-sni-v10",
		"haproxy-tcp-sni-v9",
		"haproxy-tcp-sni-v8",
		"haproxy-tcp-sni-v7",
		"haproxy-tcp-sni-v6",
		"haproxy-tcp-sni-v5",
		"haproxy-tcp-sni-v4",
		"haproxy-tcp-sni-v3",
		"haproxy-tcp-sni-v2",
		"haproxy-tcp-sni-v1",
	}, supportedHAProxyRenderers())
}

func TestRenderHAProxyConfigRendersStickyDNSPool(t *testing.T) {
	route := Route{
		ID: testRouteID, Name: "germany pool", MatchMode: "sni",
		ListenerIP: "*", ListenerPort: 443, SNIs: []string{"vpn.example.com"}, Hostname: "vpn.example.com",
		TargetType: "tcp", TargetHost: "de-pool.example.com", TargetPort: 443, DNSPool: true,
		HealthCheck: false, ProxyProtocol: "v2", QuotaAction: "observe", Enabled: true,
	}

	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	assert.Contains(t, got.Config, "    balance source\n")
	assert.Contains(t, got.Config, "    hash-type consistent sdbm avalanche\n")
	assert.Contains(t, got.Config, "    option tcp-check\n")
	assert.Contains(t, got.Config, "    server-template "+routeServerTemplatePrefix(route.ID)+" 32 de-pool.example.com:443 check inter 5s fall 3 rise 2 resolvers nf_dns init-addr last,none resolve-opts prevent-dup-ip hash-key addr send-proxy-v2 check-send-proxy\n")
	assert.NotContains(t, got.Config, "    server "+RouteServerKey(route.ID)+" ")
	require.Len(t, got.RuntimeNames, 1)
	assert.True(t, got.RuntimeNames[0].DNSPool)
	assert.Equal(t, routeServerTemplatePrefix(route.ID)+"1", got.RuntimeNames[0].Server)
}

func TestRenderedDNSPoolPassesHAProxyValidation(t *testing.T) {
	binary := os.Getenv("NODEFLOW_TEST_HAPROXY_BINARY")
	if binary == "" {
		t.Skip("NODEFLOW_TEST_HAPROXY_BINARY is not set")
	}
	route := Route{
		ID: testRouteID, Name: "germany pool", MatchMode: "any_tcp", ListenerIP: "*", ListenerPort: 18443,
		Fallback: true, TargetType: "tcp", TargetHost: "de-pool.example.com", TargetPort: 443,
		DNSPool: true, ProxyProtocol: "none", QuotaAction: "observe", Enabled: true,
	}
	result, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	config := strings.ReplaceAll(result.Config, "    user haproxy\n    group haproxy\n", "")
	path := t.TempDir() + "/haproxy.cfg"
	require.NoError(t, os.WriteFile(path, []byte(config), 0o600))
	output, err := exec.Command(binary, "-c", "-f", path).CombinedOutput()
	require.NoError(t, err, string(output))
}

func TestRenderHAProxyConfigRejectsInvalidDNSPoolTargets(t *testing.T) {
	for _, route := range []Route{
		{ID: testRouteID, Name: "ip", MatchMode: "any_tcp", ListenerIP: "*", ListenerPort: 443, Fallback: true, TargetType: "tcp", TargetHost: "192.0.2.10", TargetPort: 443, DNSPool: true, Enabled: true},
		{ID: testRouteID, Name: "unix", MatchMode: "any_tcp", ListenerIP: "*", ListenerPort: 443, Fallback: true, TargetType: "unix", UnixSocketPath: "/run/xray.sock", DNSPool: true, Enabled: true},
	} {
		_, err := RenderHAProxyConfig([]Route{route})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dns_pool")
	}
}

func TestRenderHAProxyConfigRendersPerClientIPBandwidthLimits(t *testing.T) {
	download := int64(100)
	upload := int64(25)
	sni := Route{
		ID: "11111111-1111-4111-8111-111111111111", Name: "limited SNI", MatchMode: "sni",
		ListenerIP: "*", ListenerPort: 443, SNIs: []string{"vpn.example.com"}, Hostname: "vpn.example.com",
		TargetType: "tcp", TargetHost: "192.0.2.10", TargetPort: 443, ProxyProtocol: "none", Enabled: true,
		ClientDownloadMbps: &download, ClientUploadMbps: &upload,
	}
	fallback := fallbackRoute("22222222-2222-4222-8222-222222222222")
	fallback.ListenerPort = 443
	fallback.ClientDownloadMbps = &download

	got, err := RenderHAProxyConfig([]Route{fallback, sni})
	require.NoError(t, err)

	assert.Contains(t, got.Config, "filter bwlim-out nf_bw_download_111111111111 limit 12500000 key src,ipmask(32,64) table nf_bw_download_table_111111111111 min-size 2896")
	assert.Contains(t, got.Config, "filter bwlim-in nf_bw_upload_111111111111 limit 3125000 key src,ipmask(32,64) table nf_bw_upload_table_111111111111 min-size 2896")
	assert.Contains(t, got.Config, "tcp-request content set-bandwidth-limit nf_bw_download_111111111111 if nf_sni_111111111111")
	assert.Contains(t, got.Config, "tcp-request content set-bandwidth-limit nf_bw_upload_111111111111 if nf_sni_111111111111")
	assert.Contains(t, got.Config, "tcp-request content set-bandwidth-limit nf_bw_download_222222222222 if !nf_sni_111111111111")
	assert.Contains(t, got.Config, "backend nf_bw_download_table_111111111111\n    stick-table type ipv6 size 1m expire 1h store bytes_out_rate(1s)")
	assert.Contains(t, got.Config, "backend nf_bw_upload_table_111111111111\n    stick-table type ipv6 size 1m expire 1h store bytes_in_rate(1s)")
	assert.Contains(t, got.Config, "backend nf_bw_download_table_222222222222\n    stick-table type ipv6 size 1m expire 1h store bytes_out_rate(1s)")
	assert.Less(t,
		strings.Index(got.Config, "acl nf_sni_111111111111 req.ssl_sni -i vpn.example.com"),
		strings.Index(got.Config, "tcp-request content set-bandwidth-limit nf_bw_download_111111111111 if nf_sni_111111111111"),
		"HAProxy 3.4 rejects a rule that references an ACL declared later",
	)
	assert.Less(t,
		strings.Index(got.Config, "tcp-request inspect-delay 5s"),
		strings.Index(got.Config, "tcp-request content set-bandwidth-limit nf_bw_download_111111111111 if nf_sni_111111111111"),
		"SNI must be inspected before a bandwidth limit selects the SNI route",
	)
	assert.Less(t,
		strings.Index(got.Config, "tcp-request content set-bandwidth-limit nf_bw_download_222222222222 if !nf_sni_111111111111"),
		strings.Index(got.Config, "tcp-request content accept if { req.ssl_hello_type 1 }"),
		"accept stops content-rule evaluation, so limits must precede it",
	)
	assert.Equal(t, 1, strings.Count(got.Config, "acl nf_sni_111111111111 req.ssl_sni -i vpn.example.com"))
	assert.Equal(t, 1, strings.Count(got.Config, "tcp-request content accept if { req.ssl_hello_type 1 }"))
}

func TestRenderHAProxyConfigRejectsInvalidClientBandwidthLimit(t *testing.T) {
	zero := int64(0)
	route := fallbackRoute(testRouteID)
	route.ClientDownloadMbps = &zero

	_, err := RenderHAProxyConfig([]Route{route})
	var invalid *RouteSetError
	require.ErrorAs(t, err, &invalid)
	assert.Contains(t, err.Error(), "client_download_mbps: must be positive")
}

func TestRenderHAProxyConfigUsesAutomaticCapacityAndConditionalFullSplice(t *testing.T) {
	got, err := RenderHAProxyConfig([]Route{fallbackRoute(testRouteID)})
	require.NoError(t, err)

	assert.NotContains(t, got.Config, "maxconn 50000")
	assert.NotContains(t, got.Config, "\n    maxconn ")
	assert.Contains(t, got.Config, "    .if enabled(SPLICE)\n        option splice-request\n        option splice-response\n    .endif\n")
	assert.NotContains(t, got.Config, "option splice-auto")
	assert.Contains(t, got.Config, "    option clitcpka\n    option srvtcpka\n")
	assert.Contains(t, got.Config, "    clitcpka-idle 300s\n    clitcpka-intvl 30s\n    clitcpka-cnt 3\n")
	assert.Contains(t, got.Config, "    srvtcpka-idle 300s\n    srvtcpka-intvl 30s\n    srvtcpka-cnt 3\n")
	assert.Contains(t, got.Config, "    timeout client 15m\n    timeout server 15m\n")
	assert.NotContains(t, got.Config, "    timeout client 24h\n")
}

func TestRenderHAProxyConfigUsesSystemAndFallbackResolversForDomainTargets(t *testing.T) {
	route := Route{
		ID: testRouteID, Name: "domain backend", MatchMode: "any_tcp",
		ListenerIP: "*", ListenerPort: 443, Fallback: true,
		TargetType: "tcp", TargetHost: "origin.example.com", TargetPort: 443,
		ProxyProtocol: "none", Enabled: true,
	}

	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	assert.Contains(t, got.Config, "resolvers nf_dns\n    nameserver adguard_local 127.0.0.1:53\n")
	assert.Contains(t, got.Config, "    nameserver adguard_local 127.0.0.1:53\n")
	assert.Contains(t, got.Config, "    nameserver systemd_resolved 127.0.0.53:53\n")
	assert.Contains(t, got.Config, "    nameserver cloudflare 1.1.1.1:53\n")
	assert.Contains(t, got.Config, "    nameserver google 8.8.8.8:53\n")
	assert.Contains(t, got.Config, "    nameserver quad9 9.9.9.9:53\n")
	assert.Contains(t, got.Config, "origin.example.com:443 resolvers nf_dns init-addr last,none")
	assert.NotContains(t, got.Config, "parse-resolv-conf")
	assert.NotContains(t, got.Config, "init-addr last,libc")
}

func TestRenderHAProxyConfigOmitsResolverForIPAndUnixTargets(t *testing.T) {
	ipRoute := Route{
		ID: "11111111-1111-4111-8111-111111111111", Name: "IP backend", MatchMode: "any_tcp",
		ListenerIP: "*", ListenerPort: 443, Fallback: true,
		TargetType: "tcp", TargetHost: "192.0.2.10", TargetPort: 443,
		ProxyProtocol: "none", Enabled: true,
	}
	unixRoute := Route{
		ID: "22222222-2222-4222-8222-222222222222", Name: "socket backend", MatchMode: "any_tcp",
		ListenerIP: "*", ListenerPort: 8443, Fallback: true,
		TargetType: "unix", UnixSocketPath: "/dev/shm/xray.sock",
		ProxyProtocol: "none", Enabled: true,
	}

	got, err := RenderHAProxyConfig([]Route{ipRoute, unixRoute})
	require.NoError(t, err)
	assert.NotContains(t, got.Config, "resolvers nf_dns")
	assert.NotContains(t, got.Config, "nameserver adguard_local")
	assert.NotContains(t, got.Config, "parse-resolv-conf")
}

func TestRenderHAProxyConfigPersistsMatchModeAndCoalescesListener(t *testing.T) {
	sniOne := Route{
		ID: "11111111-1111-4111-8111-111111111111", Name: "api", MatchMode: "sni",
		ListenerIP: "192.0.2.10", ListenerPort: 443, SNIs: []string{"api.example.com"}, Hostname: "api.example.com",
		TargetType: "tcp", TargetHost: "198.51.100.10", TargetPort: 443, HealthCheck: true, ProxyProtocol: "none", Enabled: true,
	}
	sniTwo := sniOne
	sniTwo.ID, sniTwo.Name, sniTwo.SNIs, sniTwo.Hostname = "22222222-2222-4222-8222-222222222222", "cdn", []string{"cdn.example.com"}, "cdn.example.com"
	destination := sniOne
	destination.ID, destination.Name, destination.MatchMode = "33333333-3333-4333-8333-333333333333", "IP ingress", "destination_ip"
	destination.SNIs, destination.Hostname, destination.Fallback = nil, "", true

	got, err := RenderHAProxyConfig([]Route{destination, sniTwo, sniOne})
	require.NoError(t, err)
	assert.Equal(t, 1, got.Listeners)
	assert.Equal(t, 3, got.EnabledRoutes)
	assert.Equal(t, 1, strings.Count(got.Config, "\nfrontend "))
	assert.Equal(t, 1, strings.Count(got.Config, "    bind 192.0.2.10:443\n"))
	assert.Contains(t, got.Config, "    default_backend "+RouteBackendKey(destination.ID)+"\n")
}

func TestRenderHAProxyConfigCanDisableHealthCheck(t *testing.T) {
	route := fallbackRoute(testRouteID)
	route.MatchMode = "any_tcp"
	route.Name = "raw TCP"
	route.HealthCheck = false

	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	assert.NotContains(t, got.Config, "option tcp-check")
	assert.NotContains(t, got.Config, " check inter ")
	assert.Contains(t, got.Config, "    server "+RouteServerKey(route.ID)+" /dev/shm/xray.sock\n")
}

func TestRenderHAProxyConfigRejectsNoEnabledRoutes(t *testing.T) {
	_, err := RenderHAProxyConfig(nil)
	assert.ErrorIs(t, err, ErrNoEnabledRoutes)
	_, err = RenderHAProxyConfig([]Route{{ID: testRouteID, Enabled: false}})
	assert.ErrorIs(t, err, ErrNoEnabledRoutes)
}

func TestLifecycleRendererAllowsDeterministicEmptyConfig(t *testing.T) {
	first, err := renderHAProxyConfigForLifecycle(nil, NodeRenderFacts{})
	require.NoError(t, err)
	second, err := renderHAProxyConfigForLifecycle([]Route{{ID: testRouteID, Enabled: false}}, NodeRenderFacts{})
	require.NoError(t, err)

	assert.Equal(t, first.Config, second.Config)
	assert.Equal(t, first.SHA256, second.SHA256)
	assert.Zero(t, first.EnabledRoutes)
	assert.Zero(t, first.Listeners)
	assert.Contains(t, first.Config, "global\n")
	assert.Contains(t, first.Config, "defaults\n")
	assert.NotContains(t, first.Config, "\n    daemon\n")
	assert.NotContains(t, first.Config, "\nfrontend ")
	assert.NotContains(t, first.Config, "\nbackend ")
	assert.Len(t, first.SHA256, 64)
}

func TestRenderHAProxyConfigMarksRuntimeQuota(t *testing.T) {
	quota := int64(1024)
	route := fallbackRoute(testRouteID)
	route.QuotaBytes = &quota
	route.QuotaAction = "block_new"
	route.QuotaPeriod = "monthly_from_creation"
	route.CreatedAt = time.Date(2026, 1, 31, 10, 15, 0, 0, time.FixedZone("source", 3*60*60))

	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	assert.Equal(t, 1, got.QuotaRuntimeRoutes)
	assert.Zero(t, got.QuotaMetadataRoutes)
	assert.Contains(t, got.Config, "quota=1 KiB enforcement=runtime-block-new")
	assert.Contains(t, got.Warnings, "quota enforcement blocks new backend connections through the HAProxy Runtime API")
	require.Len(t, got.RuntimeNames, 1)
	assert.Equal(t, "block_new", got.RuntimeNames[0].QuotaAction)
	assert.Equal(t, int64(1024), *got.RuntimeNames[0].QuotaBytes)
	assert.Equal(t, "monthly_from_creation", got.RuntimeNames[0].QuotaPeriod)
	require.NotNil(t, got.RuntimeNames[0].QuotaAnchorAt)
	assert.Equal(t, route.CreatedAt.UTC(), *got.RuntimeNames[0].QuotaAnchorAt)
}

func TestRenderHAProxyConfigDefendsPersistedRouteBoundary(t *testing.T) {
	base := Route{
		ID: testRouteID, ListenerIP: "*", ListenerPort: 443,
		SNIs: []string{"vpn.example.com"}, Hostname: "vpn.example.com",
		TargetType: "tcp", TargetHost: "192.0.2.20", TargetPort: 443,
		ProxyProtocol: "none", Enabled: true,
	}
	tests := []struct {
		name   string
		routes []Route
	}{
		{
			name: "target injection",
			routes: []Route{func() Route {
				route := base
				route.TargetHost = "192.0.2.20\nserver injected"
				return route
			}()},
		},
		{
			name: "duplicate fallback",
			routes: []Route{
				fallbackRoute("11111111-1111-4111-8111-111111111111"),
				fallbackRoute("22222222-2222-4222-8222-222222222222"),
			},
		},
		{
			name: "duplicate listener SNI",
			routes: []Route{
				base,
				func() Route {
					route := base
					route.ID = "33333333-3333-4333-8333-333333333333"
					return route
				}(),
			},
		},
		{name: "invalid id", routes: []Route{func() Route { route := base; route.ID = "../../bad"; return route }()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := RenderHAProxyConfig(tt.routes)
			var invalid *RouteSetError
			require.ErrorAs(t, err, &invalid)
			assert.False(t, strings.ContainsAny(invalid.Error(), "\r\n"))
		})
	}
}

func TestRenderHAProxyConfigRejectsDuplicateIDs(t *testing.T) {
	route := fallbackRoute(testRouteID)
	_, err := RenderHAProxyConfig([]Route{route, route})
	var invalid *RouteSetError
	require.True(t, errors.As(err, &invalid))
	assert.Contains(t, err.Error(), "duplicate route id")
}

func TestRenderHAProxyConfigRejectsOverlappingWildcardBinds(t *testing.T) {
	first := fallbackRoute("11111111-1111-4111-8111-111111111111")
	second := fallbackRoute("22222222-2222-4222-8222-222222222222")
	second.ListenerIP = "192.0.2.10"
	_, err := RenderHAProxyConfig([]Route{first, second})
	var invalid *RouteSetError
	require.ErrorAs(t, err, &invalid)
	assert.Contains(t, err.Error(), "overlap")
}

func TestRenderHAProxyConfigDoesNotDelayFallbackOnlyTCP(t *testing.T) {
	got, err := RenderHAProxyConfig([]Route{fallbackRoute(testRouteID)})
	require.NoError(t, err)
	assert.NotContains(t, got.Config, "inspect-delay")
	assert.NotContains(t, got.Config, "req.ssl_hello_type")
}

func TestRenderHAProxyConfigRendersBoundedSNIACLLines(t *testing.T) {
	route := Route{
		ID: testRouteID, ListenerIP: "*", ListenerPort: 443,
		SNIs: []string{"one.example.com", "two.example.com"}, Hostname: "one.example.com",
		TargetType: "tcp", TargetHost: "192.0.2.20", TargetPort: 443,
		ProxyProtocol: "none", Enabled: true,
	}
	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	assert.Contains(t, got.Config, "    acl nf_sni_"+routeRuntimeID(testRouteID)+" req.ssl_sni -i one.example.com two.example.com\n")
	assert.Equal(t, 1, strings.Count(got.Config, " req.ssl_sni -i "))

	// MaxRouteSNIs values split into bounded lines (HAProxy caps a line at 64 words).
	route.SNIs = make([]string, MaxRouteSNIs)
	for i := range route.SNIs {
		route.SNIs[i] = fmt.Sprintf("h%02d.example.com", i)
	}
	route.Hostname = route.SNIs[0]
	got, err = RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	lines := 0
	for _, line := range strings.Split(got.Config, "\n") {
		if strings.Contains(line, " req.ssl_sni -i ") {
			lines++
			assert.LessOrEqual(t, len(strings.Fields(line)), 64)
		}
	}
	assert.Equal(t, 2, lines)
	assert.Contains(t, got.Config, " h31.example.com\n    acl nf_sni_"+routeRuntimeID(testRouteID)+" req.ssl_sni -i h32.example.com ")
}

func TestRenderHAProxyConfigEnforcesRuntimeRouteLimit(t *testing.T) {
	routes := make([]Route, 0, MaxRenderedRoutes+1)
	for index := 0; index <= MaxRenderedRoutes; index++ {
		route := fallbackRoute(fmt.Sprintf("%08x-1111-4111-8111-111111111111", index))
		route.ListenerPort = 10000 + index
		routes = append(routes, route)
	}
	_, err := RenderHAProxyConfig(routes)
	var invalid *RouteSetError
	require.ErrorAs(t, err, &invalid)
	assert.Contains(t, err.Error(), "exceeds")
}

func fallbackRoute(id string) Route {
	return Route{
		ID: id, ListenerIP: "*", ListenerPort: 443, Fallback: true,
		TargetType: "unix", UnixSocketPath: "/dev/shm/xray.sock",
		ProxyProtocol: "none", Enabled: true,
	}
}
