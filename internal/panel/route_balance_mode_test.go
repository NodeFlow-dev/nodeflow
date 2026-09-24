package panel

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRenderBalanceModePoolStaticStickyUsesConsistentHash verifies that pool mode
// with sticky emits balance source + hash-type and no stick-table.
func TestRenderBalanceModePoolStaticStickyUsesConsistentHash(t *testing.T) {
	route := Route{
		ID: testRouteID, Name: "pool-sticky", MatchMode: "any_tcp",
		ListenerIP: "*", ListenerPort: 443, Fallback: true,
		TargetType: "tcp", TargetHost: "192.0.2.1", TargetPort: 443,
		HealthCheck: true, ProxyProtocol: "none", Enabled: true,
		StickyEnabled: true, BalanceMode: "pool",
		Servers: []RouteServer{
			{ID: "s1", RouteID: testRouteID, Position: 1, Name: "a", TargetType: "tcp", Host: "192.0.2.1", Port: 443},
			{ID: "s2", RouteID: testRouteID, Position: 2, Name: "b", TargetType: "tcp", Host: "192.0.2.2", Port: 443},
		},
	}
	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	assert.Contains(t, got.Config, "    balance source\n")
	assert.Contains(t, got.Config, "    hash-type consistent sdbm avalanche\n")
	assert.NotContains(t, got.Config, "stick-table")
	assert.NotContains(t, got.Config, "stick on src")
	assert.Contains(t, got.Config, "    server a 192.0.2.1:443 check inter 5s fall 3 rise 2\n")
	assert.Contains(t, got.Config, "    server b 192.0.2.2:443 check inter 5s fall 3 rise 2\n")
	// Pool mode: no backup keyword.
	assert.NotContains(t, got.Config, " backup\n")
}

// TestRenderBalanceModeFailoverStaticOrder verifies failover mode:
// position > 1 servers get backup, no balance line.
func TestRenderBalanceModeFailoverStaticOrder(t *testing.T) {
	route := Route{
		ID: testRouteID, Name: "failover-static", MatchMode: "any_tcp",
		ListenerIP: "*", ListenerPort: 443, Fallback: true,
		TargetType: "tcp", TargetHost: "192.0.2.1", TargetPort: 443,
		HealthCheck: true, ProxyProtocol: "none", Enabled: true,
		StickyEnabled: false, BalanceMode: "failover",
		Servers: []RouteServer{
			{ID: "s1", RouteID: testRouteID, Position: 1, Name: "primary", TargetType: "tcp", Host: "192.0.2.1", Port: 443, Backup: false},
			{ID: "s2", RouteID: testRouteID, Position: 2, Name: "secondary", TargetType: "tcp", Host: "192.0.2.2", Port: 443, Backup: true},
			{ID: "s3", RouteID: testRouteID, Position: 3, Name: "tertiary", TargetType: "tcp", Host: "192.0.2.3", Port: 443, Backup: true},
		},
	}
	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	assert.NotContains(t, got.Config, "balance source")
	assert.NotContains(t, got.Config, "hash-type")
	assert.Contains(t, got.Config, "    server primary 192.0.2.1:443 check inter 5s fall 3 rise 2\n")
	assert.Contains(t, got.Config, "    server secondary 192.0.2.2:443 check inter 5s fall 3 rise 2 backup\n")
	assert.Contains(t, got.Config, "    server tertiary 192.0.2.3:443 check inter 5s fall 3 rise 2 backup\n")
}

// TestRenderPoolMixedStaticAndDNSTemplate verifies that a pool route with one
// static server and one dns_pool server renders both correctly.
func TestRenderPoolMixedStaticAndDNSTemplate(t *testing.T) {
	route := Route{
		ID: testRouteID, Name: "pool-mixed", MatchMode: "any_tcp",
		ListenerIP: "*", ListenerPort: 443, Fallback: true,
		TargetType: "tcp", TargetHost: "192.0.2.1", TargetPort: 443,
		HealthCheck: true, ProxyProtocol: "none", Enabled: true,
		StickyEnabled: false, BalanceMode: "pool",
		Servers: []RouteServer{
			{ID: "s1", RouteID: testRouteID, Position: 1, Name: "static-a", TargetType: "tcp", Host: "192.0.2.1", Port: 443},
			{ID: "s2", RouteID: testRouteID, Position: 2, Name: "dnspool", TargetType: "tcp", Host: "pool.example.com", Port: 443, DNSPool: true},
		},
	}
	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	assert.Contains(t, got.Config, "    server static-a 192.0.2.1:443 check inter 5s fall 3 rise 2\n")
	assert.Contains(t, got.Config, "    server-template dnspool_ 32 pool.example.com:443 check inter 5s fall 3 rise 2 resolvers nf_dns init-addr last,none resolve-opts prevent-dup-ip hash-key addr\n")
}

// TestRenderFailoverDNSTemplateAsBackupWithAllbackups verifies that a failover route
// with a dns_pool server at position 2 emits backup on template + option allbackups.
func TestRenderFailoverDNSTemplateAsBackupWithAllbackups(t *testing.T) {
	route := Route{
		ID: testRouteID, Name: "failover-dns-backup", MatchMode: "any_tcp",
		ListenerIP: "*", ListenerPort: 443, Fallback: true,
		TargetType: "tcp", TargetHost: "192.0.2.1", TargetPort: 443,
		HealthCheck: true, ProxyProtocol: "none", Enabled: true,
		StickyEnabled: false, BalanceMode: "failover",
		Servers: []RouteServer{
			{ID: "s1", RouteID: testRouteID, Position: 1, Name: "primary", TargetType: "tcp", Host: "192.0.2.1", Port: 443, Backup: false},
			{ID: "s2", RouteID: testRouteID, Position: 2, Name: "dnspool", TargetType: "tcp", Host: "pool.example.com", Port: 443, Backup: true, DNSPool: true},
		},
	}
	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	assert.Contains(t, got.Config, "    option allbackups\n")
	assert.Contains(t, got.Config, "    server primary 192.0.2.1:443 check inter 5s fall 3 rise 2\n")
	assert.Contains(t, got.Config, "    server-template dnspool_ 32 pool.example.com:443 check inter 5s fall 3 rise 2 resolvers nf_dns init-addr last,none resolve-opts prevent-dup-ip hash-key addr backup\n")
}

// TestRenderFailoverDNSFirstWithPreferredIP verifies that when a dns_pool server
// at position 1 has preferred_ip set, a static pref server is emitted primary
// and the template is emitted as backup, plus option allbackups.
func TestRenderFailoverDNSFirstWithPreferredIP(t *testing.T) {
	route := Route{
		ID: testRouteID, Name: "failover-dns-pref", MatchMode: "any_tcp",
		ListenerIP: "*", ListenerPort: 443, Fallback: true,
		TargetType: "tcp", TargetHost: "vpn.example.com", TargetPort: 443,
		HealthCheck: true, ProxyProtocol: "none", Enabled: true,
		StickyEnabled: false, BalanceMode: "failover",
		Servers: []RouteServer{
			{ID: "s1", RouteID: testRouteID, Position: 1, Name: "pool", TargetType: "tcp", Host: "vpn.example.com", Port: 443, Backup: false, DNSPool: true, PreferredIP: "198.51.100.10"},
		},
	}
	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	// Static preferred server — primary, no backup.
	assert.Contains(t, got.Config, "    server pool_pref 198.51.100.10:443 check inter 5s fall 3 rise 2\n")
	// Template as backup.
	assert.Contains(t, got.Config, "    server-template pool_ 32 vpn.example.com:443 check inter 5s fall 3 rise 2 resolvers nf_dns init-addr last,none resolve-opts prevent-dup-ip hash-key addr backup\n")
	// All resolved addresses share load after failover, and the preferred
	// server is the only active one.
	assert.Contains(t, got.Config, "    option allbackups\n")
	assert.Equal(t, 1, strings.Count(got.Config, "    server pool_pref "))
	assert.NotContains(t, got.Config, "_pref 198.51.100.10:443 check inter 5s fall 3 rise 2 backup")
}

// TestRenderLegacyDNSPoolRouteGoldenCompatible verifies that a legacy route with
// routes.dns_pool=true (no servers array) renders identically to v17 except for
// the renderer line — ensuring zero byte-change on existing routes.
func TestRenderLegacyDNSPoolRouteGoldenCompatible(t *testing.T) {
	route := Route{
		ID: testRouteID, Name: "legacy pool", MatchMode: "sni",
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
}

// TestRenderEmitsResolversForHostnameInExtraServer is a v19 regression test:
// the legacy target is an IP, but a later server is a hostname that references
// nf_dns. HAProxy 3.4 rejects the config ("unable to find required resolvers
// 'nf_dns'") unless the resolvers section is emitted.
func TestRenderEmitsResolversForHostnameInExtraServer(t *testing.T) {
	route := Route{
		ID: testRouteID, Name: "failover-ip-then-dns", MatchMode: "any_tcp",
		ListenerIP: "*", ListenerPort: 443, Fallback: true,
		TargetType: "tcp", TargetHost: "192.0.2.1", TargetPort: 443,
		HealthCheck: true, ProxyProtocol: "none", Enabled: true, BalanceMode: "failover",
		Servers: []RouteServer{
			{ID: "s1", RouteID: testRouteID, Position: 1, Name: "primary", TargetType: "tcp", Host: "192.0.2.1", Port: 443},
			{ID: "s2", RouteID: testRouteID, Position: 2, Name: "backup", TargetType: "tcp", Host: "backup.example.com", Port: 443, Backup: true},
		},
	}
	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	assert.Contains(t, got.Config, "backup.example.com:443 check inter 5s fall 3 rise 2 resolvers nf_dns")
	assert.Contains(t, got.Config, "\nresolvers nf_dns\n")
}

// TestRenderBracketsIPv6PreferredIP: v19 renders preferred_ip IPv6 literals in
// brackets, matching renderTarget/renderServerSpecTarget. HAProxy 3.4 happens to
// split an unbracketed "2001:db8::9:8443" on the last colon, so this is
// consistency hardening, not a proven misparse.
func TestRenderBracketsIPv6PreferredIP(t *testing.T) {
	route := Route{
		ID: testRouteID, Name: "failover-dns-pref6", MatchMode: "any_tcp",
		ListenerIP: "*", ListenerPort: 443, Fallback: true,
		TargetType: "tcp", TargetHost: "vpn.example.com", TargetPort: 8443,
		HealthCheck: true, ProxyProtocol: "none", Enabled: true, BalanceMode: "failover",
		Servers: []RouteServer{
			{ID: "s1", RouteID: testRouteID, Position: 1, Name: "pool", TargetType: "tcp", Host: "vpn.example.com", Port: 8443, DNSPool: true, PreferredIP: "2001:db8::9"},
		},
	}
	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	assert.Contains(t, got.Config, "    server pool_pref [2001:db8::9]:8443 check inter 5s fall 3 rise 2\n")
}
