package panel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderHAProxyConfigCheckSendProxy(t *testing.T) {
	// health_check=true + proxy_protocol=v2  → check-send-proxy must appear
	routeV2 := Route{
		ID: testRouteID, Name: "proxied v2", MatchMode: "any_tcp",
		ListenerIP: "*", ListenerPort: 443, Fallback: true,
		TargetType: "tcp", TargetHost: "192.0.2.10", TargetPort: 443,
		HealthCheck: true, ProxyProtocol: "v2", Enabled: true,
	}
	got, err := RenderHAProxyConfig([]Route{routeV2})
	require.NoError(t, err)
	assert.Contains(t, got.Config, " send-proxy-v2 check-send-proxy\n",
		"v2 with health check must append check-send-proxy after send-proxy-v2")
}

func TestRenderHAProxyConfigCheckSendProxyV1(t *testing.T) {
	routeV1 := Route{
		ID: testRouteID, Name: "proxied v1", MatchMode: "any_tcp",
		ListenerIP: "*", ListenerPort: 443, Fallback: true,
		TargetType: "unix", UnixSocketPath: "/dev/shm/xray.sock",
		HealthCheck: true, ProxyProtocol: "v1", Enabled: true,
	}
	got, err := RenderHAProxyConfig([]Route{routeV1})
	require.NoError(t, err)
	assert.Contains(t, got.Config, " send-proxy check-send-proxy\n",
		"v1 with health check must append check-send-proxy after send-proxy")
}

func TestRenderHAProxyConfigNoCheckSendProxyWhenNoHealthCheck(t *testing.T) {
	route := Route{
		ID: testRouteID, Name: "no check", MatchMode: "any_tcp",
		ListenerIP: "*", ListenerPort: 443, Fallback: true,
		TargetType: "unix", UnixSocketPath: "/dev/shm/xray.sock",
		HealthCheck: false, ProxyProtocol: "v2", Enabled: true,
	}
	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	assert.NotContains(t, got.Config, "check-send-proxy",
		"no health check means check-send-proxy must not be emitted")
}

func TestRenderHAProxyConfigNoCheckSendProxyWhenProxyProtocolNone(t *testing.T) {
	route := fallbackRoute(testRouteID)
	route.HealthCheck = true
	// ProxyProtocol defaults to "none" in fallbackRoute
	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	assert.NotContains(t, got.Config, "check-send-proxy",
		"proxy_protocol=none must not emit check-send-proxy")
}
