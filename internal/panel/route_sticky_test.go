package panel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderHAProxyConfigStickySessions(t *testing.T) {
	route := Route{
		ID: testRouteID, Name: "sticky", MatchMode: "any_tcp",
		ListenerIP: "*", ListenerPort: 443, Fallback: true,
		TargetType: "unix", UnixSocketPath: "/dev/shm/xray.sock",
		HealthCheck: true, ProxyProtocol: "none",
		StickyEnabled: true, BalanceMode: "pool",
		Enabled: true,
	}
	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	assert.Contains(t, got.Config, "    balance source\n")
	assert.Contains(t, got.Config, "    hash-type consistent sdbm avalanche\n")
	// No stick-table or stick on src in new stateless consistent-hash design.
	assert.NotContains(t, got.Config, "stick-table")
	assert.NotContains(t, got.Config, "stick on src")
}

func TestRenderHAProxyConfigStickyFailoverModeNoHash(t *testing.T) {
	route := Route{
		ID: testRouteID, Name: "failover sticky off", MatchMode: "any_tcp",
		ListenerIP: "*", ListenerPort: 443, Fallback: true,
		TargetType: "unix", UnixSocketPath: "/dev/shm/xray.sock",
		HealthCheck: false, ProxyProtocol: "none",
		StickyEnabled: true, BalanceMode: "failover",
		Enabled: true,
	}
	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	// failover mode: no balance source, sticky flag ignored.
	assert.NotContains(t, got.Config, "balance source")
	assert.NotContains(t, got.Config, "hash-type")
}

func TestRenderHAProxyConfigNoStickyWhenDisabled(t *testing.T) {
	route := fallbackRoute(testRouteID)
	route.StickyEnabled = false
	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	assert.NotContains(t, got.Config, "balance source")
	assert.NotContains(t, got.Config, "hash-type")
}

func TestStickyChangesRouteFingerprint(t *testing.T) {
	base := RouteSpec{
		Name: "test", ListenerIP: "*", ListenerPort: 443, MatchMode: "any_tcp",
		Fallback: true, TargetType: "unix", UnixSocketPath: "/dev/shm/xray.sock",
		HealthCheck: true, ProxyProtocol: "none", QuotaAction: "observe", QuotaPeriod: "calendar_month", Enabled: true,
	}
	sticky := base
	sticky.StickyEnabled = true
	sticky.BalanceMode = "pool"
	assert.NotEqual(t, routeSpecFingerprint(base), routeSpecFingerprint(sticky))
}
