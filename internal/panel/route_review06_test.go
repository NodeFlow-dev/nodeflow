package panel

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for review 06 (commit 702ff0c). Every case goes through
// the real API path: JSON payload -> routeInput -> validateRoute -> Route ->
// RenderHAProxyConfig, so the UI payload shape and validation are covered.

const reviewRouteID = "33333333-3333-4333-8333-3333333333a1"

func decodeRouteInput(t *testing.T, payload string) routeInput {
	t.Helper()
	var in routeInput
	dec := json.NewDecoder(strings.NewReader(payload))
	dec.DisallowUnknownFields() // same as the HTTP decode helper
	require.NoError(t, dec.Decode(&in))
	return in
}

func validatePayload(t *testing.T, payload string) (RouteSpec, error) {
	t.Helper()
	return validateRoute(decodeRouteInput(t, payload), false)
}

// specToRoute mirrors what Store persists and reads back for a spec.
func specToRoute(spec RouteSpec) Route {
	route := Route{
		ID: reviewRouteID, Name: spec.Name, ListenerIP: spec.ListenerIP, ListenerPort: spec.ListenerPort,
		MatchMode: spec.MatchMode, SNIs: spec.SNIs, Fallback: spec.Fallback, Hostname: spec.Hostname,
		TargetType: spec.TargetType, TargetHost: spec.TargetHost, TargetPort: spec.TargetPort, DNSPool: spec.DNSPool,
		UnixSocketPath: spec.UnixSocketPath, HealthCheck: spec.HealthCheck, ProxyProtocol: spec.ProxyProtocol,
		AcceptProxyFrom: spec.AcceptProxyFrom, QuotaBytes: spec.QuotaBytes, QuotaAction: spec.QuotaAction,
		QuotaPeriod: spec.QuotaPeriod, ClientUploadMbps: spec.ClientUploadMbps, ClientDownloadMbps: spec.ClientDownloadMbps,
		Enabled: true, CustomFragment: spec.CustomFragment, StickyEnabled: spec.StickyEnabled,
		BalanceMode: spec.BalanceMode, ShaperMode: spec.ShaperMode,
		StickyMode: spec.StickyMode, StickyTTL: spec.StickyTTL,
		BalanceAlgorithm: spec.BalanceAlgorithm, BalanceRandomDraws: spec.BalanceRandomDraws,
		LeastPingTolerance: spec.LeastPingTolerance, LeastPingToleranceMS: spec.LeastPingToleranceMS,
		StickyHash: spec.StickyHash, StickyHashBalanceFactor: spec.StickyHashBalanceFactor,
		StickyTableEntries: spec.StickyTableEntries, StickyIPv6Prefix: spec.StickyIPv6Prefix,
		Slowstart: spec.Slowstart, ClientIPv6: boolPointer(!spec.ClientIPv4Only),
	}
	for _, s := range spec.Servers {
		route.Servers = append(route.Servers, RouteServer{
			RouteID: reviewRouteID, Position: s.Position, Name: s.Name, TargetType: s.TargetType,
			Host: s.Host, Port: s.Port, UnixSocketPath: s.UnixSocketPath, Backup: s.Backup,
			DNSPool: s.DNSPool, PreferredIP: s.PreferredIP,
			Weight: s.Weight, Cost: s.Cost, IPWeights: s.IPWeights,
		})
	}
	return route
}

func renderPayload(t *testing.T, payload string) (RouteSpec, HAProxyRenderResult) {
	t.Helper()
	spec, err := validatePayload(t, payload)
	require.NoError(t, err)
	got, err := RenderHAProxyConfig([]Route{specToRoute(spec)})
	require.NoError(t, err)
	return spec, got
}

func backendSection(config string) string {
	i := strings.Index(config, "\nbackend nf_be_")
	if i < 0 {
		return ""
	}
	return config[i:]
}

// B1 (already fixed on this branch): resolvers must exist when only a later
// server is a hostname.
func TestReviewB1ResolversForDNSServerAfterIP(t *testing.T) {
	_, got := renderPayload(t, `{"name":"b1","match_mode":"fallback","listener_port":10003,"balance_mode":"pool",
		"servers":[{"name":"st","host":"192.0.2.1","port":443},{"name":"dp","host":"example.com","port":443,"dns_pool":true}]}`)
	assert.Contains(t, got.Config, "\nresolvers nf_dns\n")
}

// B2: route-level dns_pool sent with servers[] must not select the legacy
// single-template render that drops every other server.
func TestReviewB2RouteDNSPoolWithServersKeepsAllServers(t *testing.T) {
	spec, got := renderPayload(t, `{"name":"b2","match_mode":"fallback","listener_port":10008,"dns_pool":true,
		"balance_mode":"pool","servers":[{"name":"dp","host":"example.com","port":443,"dns_pool":true},
		{"name":"st","host":"192.0.2.2","port":443}]}`)
	assert.False(t, spec.DNSPool, "route-level dns_pool is legacy-only")
	backend := backendSection(got.Config)
	assert.Contains(t, backend, "    server-template dp_ 32 example.com:443 ")
	assert.Contains(t, backend, "    server st 192.0.2.2:443 ")
	assert.NotContains(t, backend, "nf_srv_")
	// A DNS pool without an explicit sticky_mode is source-hash sticky, as
	// the 1.0.8 route-level DNS pool always was.
	assert.Equal(t, StickyModeSource, spec.StickyMode)
	assert.Contains(t, backend, "    balance source\n    hash-type consistent sdbm avalanche\n")

	// First server an IP, later one a DNS pool: previously rejected with
	// "dns_pool requires a DNS target_host".
	_, err := validatePayload(t, `{"name":"b2b","match_mode":"fallback","dns_pool":true,"balance_mode":"failover",
		"servers":[{"name":"p","host":"192.0.2.1","port":443},{"name":"dp","host":"example.com","port":443,"dns_pool":true}]}`)
	require.NoError(t, err)
}

// B2 defence in depth: a stored row with route-level dns_pool and several
// servers (written by 702ff0c) renders from servers[].
func TestReviewB2RendererIgnoresRouteDNSPoolForMultiServer(t *testing.T) {
	route := specToRoute(RouteSpec{Name: "stored", ListenerIP: "*", ListenerPort: 443, MatchMode: "fallback", Fallback: true,
		TargetType: "tcp", TargetHost: "example.com", TargetPort: 443, DNSPool: true, HealthCheck: true, ProxyProtocol: "none",
		QuotaAction: "observe", QuotaPeriod: "calendar_month", BalanceMode: "pool",
		Servers: []RouteServerSpec{
			{Position: 1, Name: "dp", TargetType: "tcp", Host: "example.com", Port: 443, DNSPool: true},
			{Position: 2, Name: "st", TargetType: "tcp", Host: "192.0.2.2", Port: 443},
		}})
	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	assert.Contains(t, got.Config, "    server st 192.0.2.2:443 ")
	assert.NotContains(t, got.Config, "server-template nf_srv_")
}

// B4: the editor always sends one synthetic server. It must render the
// legacy nf_srv_<id> server so quota block_new keeps working.
func TestReviewB4SingleServerKeepsLegacyRuntimeName(t *testing.T) {
	spec, got := renderPayload(t, `{"name":"b4","match_mode":"fallback","listener_port":10011,"balance_mode":"pool",
		"quota_bytes":1073741824,"quota_action":"block_new",
		"servers":[{"name":"target","host":"192.0.2.1","port":443}]}`)
	assert.Empty(t, spec.Servers)
	assert.Equal(t, "192.0.2.1", spec.TargetHost)
	server := RouteServerKey(reviewRouteID)
	assert.Contains(t, got.Config, "    server "+server+" 192.0.2.1:443 check inter 5s fall 3 rise 2\n")
	assert.NotContains(t, got.Config, "server target")
	require.Len(t, got.RuntimeNames, 1)
	assert.Equal(t, server, got.RuntimeNames[0].Server)
	assert.False(t, got.RuntimeNames[0].DNSPool)
	assert.False(t, got.RuntimeNames[0].MultiServer)

	// Byte-identical to the same route saved through legacy target fields.
	legacy, err := validatePayload(t, `{"name":"b4","match_mode":"fallback","listener_port":10011,
		"quota_bytes":1073741824,"quota_action":"block_new","target_host":"192.0.2.1","target_port":443}`)
	require.NoError(t, err)
	legacyRender, err := RenderHAProxyConfig([]Route{specToRoute(legacy)})
	require.NoError(t, err)
	assert.Equal(t, legacyRender.Config, got.Config)
	assert.Equal(t, routeSpecFingerprint(legacy), routeSpecFingerprint(spec))
}

func TestReviewB4BlockNewRejectedWithDNSPoolAndMultiServer(t *testing.T) {
	_, err := validatePayload(t, `{"name":"b4","match_mode":"fallback","quota_bytes":1,"quota_action":"block_new",
		"servers":[{"name":"dp","host":"example.com","port":443,"dns_pool":true}]}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "block_new is not supported with dns_pool")

	_, err = validatePayload(t, `{"name":"b4","match_mode":"fallback","quota_bytes":1,"quota_action":"block_new",
		"balance_mode":"failover","servers":[{"name":"a","host":"192.0.2.1","port":443},{"name":"b","host":"192.0.2.2","port":443}]}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "block_new is not supported with multiple servers")
}

// Multi-server runtime names are flagged so the quota assignment skips a
// server name that does not exist in the backend.
func TestReviewB4MultiServerRuntimeNamesFlagged(t *testing.T) {
	_, got := renderPayload(t, `{"name":"ms","match_mode":"fallback","balance_mode":"pool",
		"servers":[{"name":"a","host":"192.0.2.1","port":443},{"name":"b","host":"192.0.2.2","port":443}]}`)
	require.Len(t, got.RuntimeNames, 1)
	assert.True(t, got.RuntimeNames[0].MultiServer)
	encoded, err := json.Marshal(got.RuntimeNames[0])
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"multi_server":true`)
}

// B5: rc-era clients send backup flags without balance_mode.
func TestReviewB5ExplicitBackupHonouredWithoutBalanceMode(t *testing.T) {
	spec, got := renderPayload(t, `{"name":"b5","match_mode":"fallback",
		"servers":[{"name":"p1","host":"192.0.2.1","port":443},{"name":"p2","host":"192.0.2.2","port":443},
		{"name":"b","host":"192.0.2.3","port":443,"backup":true}]}`)
	assert.Equal(t, "failover", spec.BalanceMode)
	require.Len(t, spec.Servers, 3)
	assert.False(t, spec.Servers[0].Backup)
	assert.False(t, spec.Servers[1].Backup)
	assert.True(t, spec.Servers[2].Backup)
	assert.Contains(t, got.Config, "    server b 192.0.2.3:443 check inter 5s fall 3 rise 2 backup\n")
	assert.Contains(t, got.Config, "    server p2 192.0.2.2:443 check inter 5s fall 3 rise 2\n")
}

func TestReviewB5LegacyStickyFieldsAcceptedAndIgnored(t *testing.T) {
	spec, err := validatePayload(t, `{"name":"b5","match_mode":"fallback","target_host":"192.0.2.1","target_port":443,
		"sticky_enabled":true,"sticky_table_size":"256k","sticky_expire":"1h"}`)
	require.NoError(t, err)
	assert.True(t, spec.StickyEnabled)

	// Full HTTP path: decode uses DisallowUnknownFields.
	f := &fakeStore{}
	w := request(t, handler(f), http.MethodPost, "/api/v1/nodes/"+testNodeID+"/routes",
		`{"name":"legacy-sticky","match_mode":"fallback","target_host":"192.0.2.1","target_port":443,"sticky_table_size":"1m","sticky_expire":"30m"}`, testAdminToken)
	assert.Equal(t, http.StatusCreated, w.Code, w.Body.String())
}

// H1: fingerprints of routes created before 702ff0c must not change. The
// hashes below were computed by routeSpecFingerprint at 702ff0c^ (357818c).
func TestReviewH1FingerprintGoldenPre702ff0c(t *testing.T) {
	quota := int64(1 << 30)
	upload := int64(50)
	base := RouteSpec{
		Name: "golden", ListenerIP: "*", ListenerPort: 443, MatchMode: "sni", SNIs: []string{"a.example.com"},
		TargetType: "tcp", TargetHost: "192.0.2.1", TargetPort: 443, HealthCheck: true, ProxyProtocol: "v2",
		AcceptProxyFrom: []string{}, QuotaBytes: &quota, QuotaAction: "block_new", QuotaPeriod: "calendar_month", Enabled: true,
		BalanceMode: "pool", ShaperMode: ShaperModeHAProxy,
	}
	assert.Equal(t, "9fd0fc9924d274a1c38787029c482c6ee6f0f9f997326722e0d5b4c8d62ab393", routeSpecFingerprint(base))

	withServers := base
	withServers.QuotaAction = "observe"
	withServers.BalanceMode = "failover"
	withServers.Servers = []RouteServerSpec{
		{Position: 1, Name: "p", TargetType: "tcp", Host: "192.0.2.1", Port: 443},
		{Position: 2, Name: "b", TargetType: "tcp", Host: "192.0.2.2", Port: 443, Backup: true},
	}
	assert.Equal(t, "739d3d21b737fc12b1c6c9e711aac79638ce3a14448be58f5d9784032e5481e6", routeSpecFingerprint(withServers))

	rate := base
	rate.ClientUploadMbps = &upload
	assert.Equal(t, "275a0cf09c3809c4d9793bae23373626aa241c711ad758f56ce330072cb80543", routeSpecFingerprint(rate))

	// Ledger (publish-time) fingerprint of the same route matches too.
	route := specToRoute(base)
	route.ID = "33333333-3333-4333-8333-333333333301"
	route.Hostname = "a.example.com"
	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	assert.Equal(t, "9fd0fc9924d274a1c38787029c482c6ee6f0f9f997326722e0d5b4c8d62ab393", got.RouteFingerprints[route.ID])
}

// H2: the ledger fingerprint equals the write-time fingerprint and reacts
// to server-only changes (list, dns_pool, preferred_ip, order).
func TestReviewH2LedgerFingerprintIncludesServers(t *testing.T) {
	payload := `{"name":"h2","match_mode":"fallback","balance_mode":"failover","client_upload_mbps":10,
		"servers":[{"name":"dp","host":"example.com","port":443,"dns_pool":true,"preferred_ip":"192.0.2.10"}]}`
	spec, got := renderPayload(t, payload)
	spec.Enabled = true
	assert.Equal(t, routeSpecFingerprint(spec), got.RouteFingerprints[reviewRouteID])

	changed := specToRoute(spec)
	changed.Servers[0].PreferredIP = "192.0.2.11"
	other, err := RenderHAProxyConfig([]Route{changed})
	require.NoError(t, err)
	assert.NotEqual(t, got.RouteFingerprints[reviewRouteID], other.RouteFingerprints[reviewRouteID])

	spec2, got2 := renderPayload(t, `{"name":"h2","match_mode":"fallback","balance_mode":"pool",
		"servers":[{"name":"a","host":"192.0.2.1","port":443},{"name":"b","host":"192.0.2.2","port":443}]}`)
	spec2.Enabled = true
	assert.Equal(t, routeSpecFingerprint(spec2), got2.RouteFingerprints[reviewRouteID])
	swapped := specToRoute(spec2)
	swapped.Servers[0].Position, swapped.Servers[1].Position = 2, 1
	got3, err := RenderHAProxyConfig([]Route{swapped})
	require.NoError(t, err)
	assert.NotEqual(t, got2.RouteFingerprints[reviewRouteID], got3.RouteFingerprints[reviewRouteID])
}

// H3: preferred_ip is only valid for the first server in failover mode.
func TestReviewH3PreferredIPOnlyFirstFailover(t *testing.T) {
	cases := map[string]string{
		"pool": `{"name":"h3","match_mode":"fallback","balance_mode":"pool",
			"servers":[{"name":"dp","host":"example.com","port":443,"dns_pool":true,"preferred_ip":"2001:db8::10"}]}`,
		"second": `{"name":"h3","match_mode":"fallback","balance_mode":"failover",
			"servers":[{"name":"p","host":"192.0.2.1","port":443},{"name":"dp","host":"example.com","port":443,"dns_pool":true,"preferred_ip":"192.0.2.10"}]}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := validatePayload(t, payload)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "preferred_ip is only valid for the first server in failover mode")
		})
	}
	// A single DNS pool with preferred_ip and no balance_mode is failover.
	spec, got := renderPayload(t, `{"name":"h3","match_mode":"fallback",
		"servers":[{"name":"dp","host":"example.com","port":443,"dns_pool":true,"preferred_ip":"2001:0db8::0010"}]}`)
	assert.Equal(t, "failover", spec.BalanceMode)
	assert.Equal(t, "2001:db8::10", spec.Servers[0].PreferredIP, "L1: normalised")
	assert.Contains(t, got.Config, "    server dp_pref [2001:db8::10]:443 check")
}

// H4: a DNS-pool reserve gets option allbackups (also behind preferred_ip)
// and must be the only reserve, otherwise priority order would be lost.
func TestReviewH4AllbackupsSemantics(t *testing.T) {
	_, got := renderPayload(t, `{"name":"h4","match_mode":"fallback","balance_mode":"failover",
		"servers":[{"name":"dp","host":"example.com","port":443,"dns_pool":true,"preferred_ip":"192.0.2.10"}]}`)
	backend := backendSection(got.Config)
	assert.Contains(t, backend, "    option allbackups\n")
	assert.Contains(t, backend, "    server dp_pref 192.0.2.10:443 check inter 5s fall 3 rise 2\n")
	assert.Contains(t, backend, "resolve-opts prevent-dup-ip hash-key addr backup\n")

	_, got = renderPayload(t, `{"name":"h4","match_mode":"fallback","balance_mode":"failover",
		"servers":[{"name":"p","host":"192.0.2.1","port":443},{"name":"dp","host":"example.com","port":443,"dns_pool":true}]}`)
	assert.Contains(t, got.Config, "    option allbackups\n")

	// Static-only failover keeps ordered single-backup behaviour.
	_, got = renderPayload(t, `{"name":"h4","match_mode":"fallback","balance_mode":"failover",
		"servers":[{"name":"p","host":"192.0.2.1","port":443},{"name":"s","host":"192.0.2.2","port":443},{"name":"t","host":"192.0.2.3","port":443}]}`)
	assert.NotContains(t, got.Config, "allbackups")

	for name, payload := range map[string]string{
		"pref+static": `{"name":"h4","match_mode":"fallback","balance_mode":"failover",
			"servers":[{"name":"dp","host":"example.com","port":443,"dns_pool":true,"preferred_ip":"192.0.2.10"},{"name":"s","host":"192.0.2.2","port":443}]}`,
		"dns+static reserves": `{"name":"h4","match_mode":"fallback","balance_mode":"failover",
			"servers":[{"name":"p","host":"192.0.2.1","port":443},{"name":"dp","host":"example.com","port":443,"dns_pool":true},{"name":"s","host":"192.0.2.2","port":443}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := validatePayload(t, payload)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "DNS-pool reserve must be the only reserve")
		})
	}
}

// H5: failover backup flags come from the sorted index, not Position>1.
func TestReviewH5FailoverBackupFromSortedIndex(t *testing.T) {
	spec, got := renderPayload(t, `{"name":"h5","match_mode":"fallback","balance_mode":"failover",
		"servers":[{"position":3,"name":"c","host":"192.0.2.3","port":443},{"position":2,"name":"b","host":"192.0.2.2","port":443}]}`)
	require.Len(t, spec.Servers, 2)
	assert.Equal(t, "b", spec.Servers[0].Name)
	assert.False(t, spec.Servers[0].Backup)
	assert.True(t, spec.Servers[1].Backup)
	assert.Contains(t, got.Config, "    server b 192.0.2.2:443 check inter 5s fall 3 rise 2\n")
	assert.Contains(t, got.Config, "    server c 192.0.2.3:443 check inter 5s fall 3 rise 2 backup\n")
}

// M1: DNS-pool servers force health checks, like the legacy route pool.
func TestReviewM1DNSPoolServerForcesHealthCheck(t *testing.T) {
	spec, got := renderPayload(t, `{"name":"m1","match_mode":"fallback","balance_mode":"failover","health_check":false,
		"proxy_protocol":"v1","servers":[{"name":"p","host":"192.0.2.1","port":443},{"name":"dp","host":"example.com","port":443,"dns_pool":true}]}`)
	assert.True(t, spec.HealthCheck)
	backend := backendSection(got.Config)
	assert.Contains(t, backend, "    option tcp-check\n")
	assert.Contains(t, backend, "    server p 192.0.2.1:443 check inter 5s fall 3 rise 2 send-proxy check-send-proxy\n")
}

// M2/M4: server names are single HAProxy tokens and cannot collide with
// generated template slot names.
func TestReviewM2M4ServerNames(t *testing.T) {
	for name, server := range map[string]string{
		"space":     `{"name":"a 10.0.0.1:22 source 0.0.0.0","host":"192.0.2.1","port":443}`,
		"dot":       `{"name":"a.b","host":"192.0.2.1","port":443}`,
		"pref":      `{"name":"x_pref","host":"192.0.2.1","port":443}`,
		"too long":  `{"name":"` + strings.Repeat("a", 59) + `","host":"192.0.2.1","port":443}`,
		"collision": `{"name":"x","host":"example.com","port":443,"dns_pool":true},{"name":"x_1","host":"192.0.2.1","port":443}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := validatePayload(t, `{"name":"m2","match_mode":"fallback","balance_mode":"pool","servers":[`+server+`,{"name":"zz","host":"192.0.2.9","port":443}]}`)
			require.Error(t, err)
		})
	}
	_, err := validatePayload(t, `{"name":"m2","match_mode":"fallback","balance_mode":"pool",
		"servers":[{"name":"x","host":"example.com","port":443,"dns_pool":true},{"name":"x_a","host":"192.0.2.1","port":443}]}`)
	require.NoError(t, err)
}

// M3: the custom_fragment guard covers per-server DNS pools.
func TestReviewM3FragmentConflictPerServerDNSPool(t *testing.T) {
	_, err := validatePayload(t, `{"name":"m3","match_mode":"fallback","balance_mode":"pool","custom_fragment":"balance roundrobin",
		"servers":[{"name":"p","host":"192.0.2.1","port":443},{"name":"dp","host":"example.com","port":443,"dns_pool":true}]}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "custom_fragment cannot override DNS pool")
}

func TestReviewUnknownBalanceModeRejected(t *testing.T) {
	_, err := validatePayload(t, `{"name":"x","match_mode":"fallback","target_host":"192.0.2.1","target_port":443,"balance_mode":"random"}`)
	require.Error(t, err)
}
