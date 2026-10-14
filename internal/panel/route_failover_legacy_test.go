package panel

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// legacyFailoverStickyRoutes are failover rows as migrations 000041, 000051
// and 000052 could leave them: sticky values stored although the pre-2.2.0
// renderer ignored stickiness for failover backends.
func legacyFailoverStickyRoutes(t *testing.T) map[string]Route {
	t.Helper()
	build := func(payload string, mutate func(*Route)) Route {
		spec, err := validatePayload(t, payload)
		require.NoError(t, err)
		route := specToRoute(spec)
		route.BalanceMode = "failover"
		route.Servers[len(route.Servers)-1].Backup = true
		mutate(&route)
		return route
	}
	static := `{"name":"legacy-failover","match_mode":"fallback","listener_port":19443,"servers":[{"name":"primary","host":"192.0.2.10","port":443},{"name":"reserve","host":"192.0.2.20","port":443}]}`
	dns := `{"name":"legacy-failover","match_mode":"fallback","listener_port":19443,"servers":[{"name":"primary","host":"pool.example.com","port":443,"dns_pool":true},{"name":"reserve","host":"192.0.2.20","port":443}]}`
	return map[string]Route{
		// 000051: sticky_mode='source' WHERE sticky_enabled, any balance_mode.
		"source": build(static, func(r *Route) {
			r.StickyEnabled, r.StickyMode, r.StickyTTL, r.BalanceAlgorithm = true, StickyModeSource, "", ""
		}),
		// 000051 + 000052 on a DNS-pool failover: source and a stored algorithm.
		"source-leastconn-dns": build(dns, func(r *Route) {
			r.StickyEnabled, r.StickyMode, r.StickyTTL, r.BalanceAlgorithm = true, StickyModeSource, "", BalanceAlgorithmLeastConn
		}),
		// rc API source_table + 000052 backfill balance_algorithm='leastconn'.
		"source-table-leastconn": build(dns, func(r *Route) {
			r.StickyEnabled, r.StickyMode, r.StickyTTL, r.BalanceAlgorithm = true, StickyModeSourceTable, "24h", BalanceAlgorithmLeastConn
		}),
	}
}

// applyMigration000057 mirrors migrations/000057_failover_legacy_distribution.up.sql.
func applyMigration000057(route *Route) {
	if route.BalanceMode != "failover" || legacyDNSPoolRoute(renderRoute{DNSPool: route.DNSPool, Servers: routeServersForRender(routeServersToSpec(route.Servers))}) {
		return
	}
	switch route.StickyMode {
	case StickyModeSource, StickyModeSourceTable, StickyModeLeastConn:
		route.StickyMode = ""
	}
}

// TestLegacyFailoverStickyRenderMatchesV210 compares the render of legacy
// failover rows after migration 000057 with the v2.1.0 render captured as
// golden files (NODEFLOW_WRITE_LEGACY_FAILOVER_GOLDEN=1 rewrites them on the
// v2.1.0 tree, where the migration step is a no-op).
func TestLegacyFailoverStickyRenderMatchesV210(t *testing.T) {
	for name, route := range legacyFailoverStickyRoutes(t) {
		t.Run(name, func(t *testing.T) {
			golden := filepath.Join("testdata", "legacy_failover_"+name+".golden.cfg")
			if os.Getenv("NODEFLOW_WRITE_LEGACY_FAILOVER_GOLDEN") != "" {
				got, err := RenderHAProxyConfig([]Route{route})
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(golden, []byte(got.Config), 0o644))
				return
			}
			want, err := os.ReadFile(golden)
			require.NoError(t, err)
			before := routeSpecFingerprint(routeAsSpec(route, true))
			applyMigration000057(&route)
			got, err := RenderHAProxyConfig([]Route{route})
			require.NoError(t, err)
			require.Equal(t, string(want), got.Config)
			require.NotContains(t, got.Config, "stick-table")
			require.NotContains(t, got.Config, "balance source")
			if name != "source-table-leastconn" {
				// 000051 rows hashed sticky_mode=source as implied by
				// sticky_enabled, so the fingerprint is unchanged too.
				require.Equal(t, before, routeSpecFingerprint(routeAsSpec(route, true)))
			}
		})
	}
}
