package panel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const poolServers = `"servers":[{"name":"a","host":"192.0.2.1","port":443},{"name":"b","host":"192.0.2.2","port":443}]`

func TestStickyModeDerivedWhenAbsent(t *testing.T) {
	cases := []struct {
		name, payload, want string
	}{
		{"plain pool", `{"name":"p","match_mode":"fallback","balance_mode":"pool",` + poolServers + `}`, StickyModeNone},
		{"sticky_enabled", `{"name":"p","match_mode":"fallback","balance_mode":"pool","sticky_enabled":true,` + poolServers + `}`, StickyModeSource},
		{"legacy dns pool", `{"name":"p","match_mode":"fallback","target_host":"pool.example.com","target_port":443,"dns_pool":true}`, StickyModeSource},
		{"single dns-pool server", `{"name":"p","match_mode":"fallback","balance_mode":"pool","servers":[{"name":"dp","host":"pool.example.com","port":443,"dns_pool":true}]}`, StickyModeSource},
		{"single target", `{"name":"p","match_mode":"fallback","target_host":"192.0.2.1","target_port":443}`, StickyModeRoundRobin},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := validatePayload(t, tc.payload)
			require.NoError(t, err)
			assert.Equal(t, tc.want, spec.StickyMode)
			assert.Equal(t, tc.want == StickyModeSource, spec.StickyEnabled)
		})
	}
	// A vanilla pool route (balance_algorithm and sticky_mode both absent)
	// resolves balance_algorithm to the explicit "roundrobin" default.
	spec, err := validatePayload(t, `{"name":"p","match_mode":"fallback","balance_mode":"pool",`+poolServers+`}`)
	require.NoError(t, err)
	assert.Equal(t, BalanceAlgorithmRoundRobin, spec.BalanceAlgorithm)
	// Single-target routes never carry an algorithm: nothing to balance between.
	spec, err = validatePayload(t, `{"name":"p","match_mode":"fallback","target_host":"192.0.2.1","target_port":443}`)
	require.NoError(t, err)
	assert.Equal(t, "", spec.BalanceAlgorithm)
}

func TestStickyModeValidation(t *testing.T) {
	_, err := validatePayload(t, `{"name":"p","match_mode":"fallback","sticky_mode":"sni",`+poolServers+`}`)
	require.ErrorContains(t, err, stickyModeSNIError)
	_, err = validatePayload(t, `{"name":"p","match_mode":"fallback","sticky_mode":"hash",`+poolServers+`}`)
	require.ErrorContains(t, err, "sticky_mode must be")
	for _, ttl := range []string{"30s", "8d", "0m", "1x", "01h", "-1h", "1.5h"} {
		_, err = validatePayload(t, `{"name":"p","match_mode":"fallback","sticky_mode":"source_table","sticky_ttl":"`+ttl+`",`+poolServers+`}`)
		require.ErrorContains(t, err, "sticky_ttl", ttl)
	}
	for _, ttl := range []string{"1m", "15m", "1h", "6h", "24h", "7d", "60s"} {
		spec, err := validatePayload(t, `{"name":"p","match_mode":"fallback","sticky_mode":"source_table","sticky_ttl":"`+ttl+`",`+poolServers+`}`)
		require.NoError(t, err, ttl)
		assert.Equal(t, ttl, spec.StickyTTL)
		assert.True(t, spec.StickyEnabled)
	}
	spec, err := validatePayload(t, `{"name":"p","match_mode":"fallback","sticky_mode":"source_table",`+poolServers+`}`)
	require.NoError(t, err)
	assert.Equal(t, "1h", spec.StickyTTL)
	assert.Equal(t, BalanceAlgorithmLeastConn, spec.BalanceAlgorithm, "legacy source_table default")
	// sticky_ttl is kept only for source_table. Legacy sticky_mode=leastconn
	// maps into sticky_mode=none + balance_algorithm=leastconn.
	spec, err = validatePayload(t, `{"name":"p","match_mode":"fallback","sticky_mode":"leastconn","sticky_ttl":"6h",`+poolServers+`}`)
	require.NoError(t, err)
	assert.Equal(t, "", spec.StickyTTL)
	assert.False(t, spec.StickyEnabled)
	assert.Equal(t, StickyModeNone, spec.StickyMode)
	assert.Equal(t, BalanceAlgorithmLeastConn, spec.BalanceAlgorithm)
	// Legacy sticky_mode=roundrobin maps into sticky_mode=none + balance_algorithm=roundrobin.
	spec, err = validatePayload(t, `{"name":"p","match_mode":"fallback","sticky_mode":"roundrobin",`+poolServers+`}`)
	require.NoError(t, err)
	assert.Equal(t, StickyModeNone, spec.StickyMode)
	assert.Equal(t, BalanceAlgorithmRoundRobin, spec.BalanceAlgorithm)
	// balance_algorithm wins over the sticky_mode legacy mapping when present.
	spec, err = validatePayload(t, `{"name":"p","match_mode":"fallback","sticky_mode":"leastconn","balance_algorithm":"static-rr",`+poolServers+`}`)
	require.NoError(t, err)
	assert.Equal(t, StickyModeNone, spec.StickyMode)
	assert.Equal(t, BalanceAlgorithmStaticRR, spec.BalanceAlgorithm)
	// Failover accepts but ignores sticky_mode and balance_algorithm.
	spec, err = validatePayload(t, `{"name":"p","match_mode":"fallback","balance_mode":"failover","sticky_mode":"leastconn",`+poolServers+`}`)
	require.NoError(t, err)
	assert.Equal(t, "", spec.StickyMode)
	assert.Equal(t, "", spec.BalanceAlgorithm)
	assert.False(t, spec.StickyEnabled)
}

func TestRenderStickyModes(t *testing.T) {
	cases := []struct {
		name, payload string
		want          string
		absent        []string
	}{
		{"source", `{"name":"p","match_mode":"fallback","sticky_mode":"source",` + poolServers + `}`,
			"    mode tcp\n    balance source\n    hash-type consistent sdbm avalanche\n    option tcp-check\n", nil},
		{"source_table", `{"name":"p","match_mode":"fallback","sticky_mode":"source_table","sticky_ttl":"6h",` + poolServers + `}`,
			"    mode tcp\n    balance leastconn\n    stick-table type ipv6 size 1m expire 6h peers nf_peers\n    stick on src,ipmask(32,64)\n    option redispatch\n    option tcp-check\n", []string{"balance source"}},
		{"leastconn", `{"name":"p","match_mode":"fallback","sticky_mode":"leastconn",` + poolServers + `}`,
			"    mode tcp\n    balance leastconn\n    option tcp-check\n", []string{"stick", "hash-type"}},
		{"roundrobin", `{"name":"p","match_mode":"fallback","sticky_mode":"roundrobin",` + poolServers + `}`,
			"    mode tcp\n    option tcp-check\n", []string{"balance", "stick"}},
		{"roundrobin dns pool", `{"name":"p","match_mode":"fallback","sticky_mode":"roundrobin","servers":[{"name":"dp","host":"pool.example.com","port":443,"dns_pool":true},{"name":"b","host":"192.0.2.2","port":443}]}`,
			"    mode tcp\n    option tcp-check\n", []string{"balance", "stick"}},
		{"failover", `{"name":"p","match_mode":"fallback","balance_mode":"failover","sticky_mode":"source_table",` + poolServers + `}`,
			"    mode tcp\n    option tcp-check\n", []string{"balance", "stick"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, got := renderPayload(t, tc.payload)
			backend := backendSection(got.Config)
			assert.Contains(t, backend, tc.want)
			for _, s := range tc.absent {
				assert.NotContains(t, backend, s)
			}
		})
	}
}

// sticky_mode sni is removed (migration 000052): the render path is gone and
// the API rejects it on input (see TestStickyModeValidation). Only a row
// that somehow still carries the legacy 'sni' value in storage (the CHECK
// keeps it for safety) can reach the renderer; it must render nothing, never
// crash.
func TestRenderStickyModeSNIRemoved(t *testing.T) {
	route := Route{
		ID: testRouteID, Name: "legacy-sni", MatchMode: "sni", SNIs: []string{"a.example.com"}, ListenerIP: "*", ListenerPort: 443,
		TargetType: "tcp", TargetHost: "192.0.2.1", TargetPort: 443, HealthCheck: true, ProxyProtocol: "none", Enabled: true,
		BalanceMode: "pool", StickyMode: StickyModeSNI,
		Servers: []RouteServer{
			{Position: 1, Name: "a", TargetType: "tcp", Host: "192.0.2.1", Port: 443},
			{Position: 2, Name: "b", TargetType: "tcp", Host: "192.0.2.2", Port: 443},
		},
	}
	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	backend := backendSection(got.Config)
	assert.NotContains(t, backend, "balance")
	assert.NotContains(t, backend, "req.ssl_sni")
}

// Rows stored before migration 000051 have sticky_mode ”. They render as
// before, except a servers[] DNS pool is source-hash sticky like in 1.0.8.
func TestRenderEmptyStickyModeCompat(t *testing.T) {
	base := Route{
		ID: testRouteID, Name: "legacy", MatchMode: "fallback", ListenerIP: "*", ListenerPort: 443, Fallback: true,
		TargetType: "tcp", TargetHost: "pool.example.com", TargetPort: 443, HealthCheck: true,
		ProxyProtocol: "none", Enabled: true, BalanceMode: "pool",
	}
	dnsServer := RouteServer{Position: 1, Name: "dp", TargetType: "tcp", Host: "pool.example.com", Port: 443, DNSPool: true}
	static := RouteServer{Position: 2, Name: "st", TargetType: "tcp", Host: "192.0.2.2", Port: 443}

	withServers := base
	withServers.Servers = []RouteServer{dnsServer, static}
	got, err := RenderHAProxyConfig([]Route{withServers})
	require.NoError(t, err)
	assert.Contains(t, got.Config, "    mode tcp\n    balance source\n    hash-type consistent sdbm avalanche\n")

	failover := withServers
	failover.BalanceMode = "failover"
	failover.Servers = []RouteServer{dnsServer, {Position: 2, Name: "st", TargetType: "tcp", Host: "192.0.2.2", Port: 443, Backup: true}}
	got, err = RenderHAProxyConfig([]Route{failover})
	require.NoError(t, err)
	assert.NotContains(t, got.Config, "balance")

	plain := base
	plain.TargetHost = "192.0.2.1"
	plain.Servers = []RouteServer{{Position: 1, Name: "a", TargetType: "tcp", Host: "192.0.2.1", Port: 443}, static}
	got, err = RenderHAProxyConfig([]Route{plain})
	require.NoError(t, err)
	assert.NotContains(t, got.Config, "balance")

	plain.StickyEnabled = true
	got, err = RenderHAProxyConfig([]Route{plain})
	require.NoError(t, err)
	assert.Contains(t, got.Config, "    balance source\n")
}

// Existing routes have sticky_mode ” (or the backfilled value implied by
// sticky_enabled). Their fingerprints must not change; the derived value the
// API now stores for an unchanged route must hash identically too.
func TestStickyModeKeepsExistingFingerprints(t *testing.T) {
	base := RouteSpec{
		Name: "test", ListenerIP: "*", ListenerPort: 443, MatchMode: "fallback",
		Fallback: true, TargetType: "tcp", TargetHost: "192.0.2.1", TargetPort: 443,
		HealthCheck: true, ProxyProtocol: "none", QuotaAction: "observe", QuotaPeriod: "calendar_month", Enabled: true,
		Servers: []RouteServerSpec{{Position: 1, Name: "a", TargetType: "tcp", Host: "192.0.2.1", Port: 443}, {Position: 2, Name: "b", TargetType: "tcp", Host: "192.0.2.2", Port: 443}},
	}
	derived := base
	derived.StickyMode = StickyModeRoundRobin
	assert.Equal(t, routeSpecFingerprint(base), routeSpecFingerprint(derived))

	sticky := base
	sticky.StickyEnabled = true
	stickyDerived := sticky
	stickyDerived.StickyMode = StickyModeSource
	assert.Equal(t, routeSpecFingerprint(sticky), routeSpecFingerprint(stickyDerived))

	dns := base
	dns.Servers = []RouteServerSpec{{Position: 1, Name: "dp", TargetType: "tcp", Host: "pool.example.com", Port: 443, DNSPool: true}, base.Servers[1]}
	dnsDerived := dns
	dnsDerived.StickyMode = StickyModeSource
	assert.Equal(t, routeSpecFingerprint(dns), routeSpecFingerprint(dnsDerived))

	for _, mode := range []string{StickyModeLeastConn, StickyModeSNI} {
		changed := base
		changed.StickyMode = mode
		assert.NotEqual(t, routeSpecFingerprint(base), routeSpecFingerprint(changed), mode)
	}
	table := base
	table.StickyEnabled = true
	table.StickyMode, table.StickyTTL = StickyModeSourceTable, "1h"
	table6h := table
	table6h.StickyTTL = "6h"
	assert.NotEqual(t, routeSpecFingerprint(sticky), routeSpecFingerprint(table))
	assert.NotEqual(t, routeSpecFingerprint(table), routeSpecFingerprint(table6h))
}
