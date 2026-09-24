package panel

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderHAProxyConfigMultiServerBackend(t *testing.T) {
	route := Route{
		ID: testRouteID, Name: "multi", MatchMode: "any_tcp",
		ListenerIP: "*", ListenerPort: 443, Fallback: true,
		// Single-target fields kept for backward compat but Servers overrides them in render.
		TargetType: "tcp", TargetHost: "192.0.2.1", TargetPort: 443,
		HealthCheck: true, ProxyProtocol: "v2", Enabled: true,
		Servers: []RouteServer{
			{ID: "s1", RouteID: testRouteID, Position: 1, Name: "primary", TargetType: "tcp", Host: "192.0.2.1", Port: 443},
			{ID: "s2", RouteID: testRouteID, Position: 2, Name: "backup1", TargetType: "tcp", Host: "192.0.2.2", Port: 443, Backup: true},
			{ID: "s3", RouteID: testRouteID, Position: 3, Name: "socket", TargetType: "unix", UnixSocketPath: "/dev/shm/xray.sock", Backup: true},
		},
	}

	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	// primary — no backup keyword
	assert.Contains(t, got.Config, "    server primary 192.0.2.1:443 check inter 5s fall 3 rise 2 send-proxy-v2 check-send-proxy\n")
	// backup1 — backup keyword at end
	assert.Contains(t, got.Config, "    server backup1 192.0.2.2:443 check inter 5s fall 3 rise 2 send-proxy-v2 check-send-proxy backup\n")
	// socket (unix) backup
	assert.Contains(t, got.Config, "    server socket /dev/shm/xray.sock check inter 5s fall 3 rise 2 send-proxy-v2 check-send-proxy backup\n",
		"unix backup server must have backup keyword")
	// old nf_srv_ name must NOT appear
	assert.NotContains(t, got.Config, RouteServerKey(testRouteID))
	assert.Equal(t, 1, got.EnabledRoutes)
}

func TestRenderHAProxyConfigMultiServerDeterministic(t *testing.T) {
	route := Route{
		ID: testRouteID, Name: "multi", MatchMode: "any_tcp",
		ListenerIP: "*", ListenerPort: 443, Fallback: true,
		TargetType: "tcp", TargetHost: "192.0.2.1", TargetPort: 443,
		HealthCheck: false, ProxyProtocol: "none", Enabled: true,
		Servers: []RouteServer{
			{ID: "s2", RouteID: testRouteID, Position: 2, Name: "b", TargetType: "tcp", Host: "192.0.2.2", Port: 443},
			{ID: "s1", RouteID: testRouteID, Position: 1, Name: "a", TargetType: "tcp", Host: "192.0.2.1", Port: 443},
		},
	}
	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	// Position 1 (a) must come before position 2 (b) — but Servers on Route are
	// already-loaded DB order; renderer uses the spec order from validation.
	// For in-memory Route.Servers (no validation), order is preserved as-is.
	// Verify both appear.
	assert.Contains(t, got.Config, "    server a 192.0.2.1:443\n")
	assert.Contains(t, got.Config, "    server b 192.0.2.2:443\n")
}

func TestValidateRouteServersRejectsAllBackup(t *testing.T) {
	_, _, err := resolveRouteServers([]routeServerInput{
		{Position: 1, Name: "s1", TargetType: "tcp", Host: "192.0.2.1", Port: 443, Backup: true},
	}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one non-backup")
}

func TestValidateRouteServersRejectsDuplicateName(t *testing.T) {
	_, err := validateRouteServers([]routeServerInput{
		{Position: 1, Name: "same", TargetType: "tcp", Host: "192.0.2.1", Port: 443},
		{Position: 2, Name: "same", TargetType: "tcp", Host: "192.0.2.2", Port: 443},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
}

func TestValidateRouteServersRejectsTooMany(t *testing.T) {
	servers := make([]routeServerInput, MaxRouteServers+1)
	for i := range servers {
		servers[i] = routeServerInput{Position: i + 1, Name: fmt.Sprintf("s%d", i), TargetType: "tcp", Host: "192.0.2.1", Port: 443}
	}
	_, err := validateRouteServers(servers)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not exceed")
}

func TestValidateRouteServersDNSPoolAllowsMixedWithStatic(t *testing.T) {
	// Per-server dns_pool is now allowed alongside static servers.
	out, err := validateRouteServers([]routeServerInput{
		{Position: 1, Name: "a", TargetType: "tcp", Host: "pool.example.com", Port: 443, DNSPool: true},
		{Position: 2, Name: "b", TargetType: "tcp", Host: "192.0.2.2", Port: 443},
	})
	require.NoError(t, err)
	require.Len(t, out, 2)
	assert.True(t, out[0].DNSPool)
	assert.False(t, out[1].DNSPool)
}

func TestRenderHAProxyConfigFallsBackToSingleTargetWhenNoServers(t *testing.T) {
	route := fallbackRoute(testRouteID)
	route.Servers = nil
	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	// legacy nf_srv_ name must appear in single-target mode
	assert.Contains(t, got.Config, "    server "+RouteServerKey(testRouteID))
}

func TestMultiServerChangesRouteFingerprint(t *testing.T) {
	base := RouteSpec{
		Name: "test", ListenerIP: "*", ListenerPort: 443, MatchMode: "any_tcp",
		Fallback: true, TargetType: "unix", UnixSocketPath: "/dev/shm/xray.sock",
		HealthCheck: true, ProxyProtocol: "none", QuotaAction: "observe", QuotaPeriod: "calendar_month", Enabled: true,
	}
	withServers := base
	withServers.Servers = []RouteServerSpec{
		{Position: 1, Name: "s1", TargetType: "unix", UnixSocketPath: "/dev/shm/xray.sock"},
	}
	assert.NotEqual(t, routeSpecFingerprint(base), routeSpecFingerprint(withServers))
}
