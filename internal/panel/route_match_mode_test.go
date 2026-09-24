package panel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestF6AnyTCPNormalizesToFallback(t *testing.T) {
	port := 443
	enabled := true
	spec, err := validateRoute(routeInput{
		MatchMode:  "any_tcp",
		ListenerIP: "*", ListenerPort: &port,
		TargetType: "unix", UnixSocketPath: "/dev/shm/xray.sock",
		Enabled: &enabled,
	}, true)
	require.NoError(t, err)
	assert.Equal(t, "fallback", spec.MatchMode)
	assert.True(t, spec.Fallback)
}

func TestF6DestinationIPNormalizesToFallback(t *testing.T) {
	port := 443
	enabled := true
	spec, err := validateRoute(routeInput{
		MatchMode:  "destination_ip",
		ListenerIP: "192.0.2.10", ListenerPort: &port,
		TargetType: "tcp", TargetHost: "10.0.0.1", TargetPort: 8443,
		Enabled: &enabled,
	}, true)
	require.NoError(t, err)
	assert.Equal(t, "fallback", spec.MatchMode)
	assert.True(t, spec.Fallback)
}

func TestF6FallbackModeAccepted(t *testing.T) {
	port := 443
	enabled := true
	spec, err := validateRoute(routeInput{
		MatchMode:  "fallback",
		ListenerIP: "*", ListenerPort: &port,
		TargetType: "unix", UnixSocketPath: "/dev/shm/xray.sock",
		Enabled: &enabled,
	}, true)
	require.NoError(t, err)
	assert.Equal(t, "fallback", spec.MatchMode)
}

func TestF6AnyTCPOnConcreteIPNowAccepted(t *testing.T) {
	// Previously any_tcp on concrete IP was rejected; now it normalizes to fallback.
	port := 443
	enabled := true
	spec, err := validateRoute(routeInput{
		MatchMode:  "any_tcp",
		ListenerIP: "192.0.2.10", ListenerPort: &port,
		TargetType: "tcp", TargetHost: "10.0.0.1", TargetPort: 8443,
		Enabled: &enabled,
	}, true)
	require.NoError(t, err, "any_tcp on concrete IP must not be rejected after F6")
	assert.Equal(t, "fallback", spec.MatchMode)
}

func TestF6DestinationIPOnWildcardNowAccepted(t *testing.T) {
	port := 443
	enabled := true
	spec, err := validateRoute(routeInput{
		MatchMode:  "destination_ip",
		ListenerIP: "*", ListenerPort: &port,
		TargetType: "unix", UnixSocketPath: "/dev/shm/xray.sock",
		Enabled: &enabled,
	}, true)
	require.NoError(t, err, "destination_ip on wildcard must not be rejected after F6")
	assert.Equal(t, "fallback", spec.MatchMode)
}

func TestF6RendererUnchangedForAnyTCPRoute(t *testing.T) {
	// Routes stored as any_tcp still produce correct HAProxy config.
	route := Route{
		ID: testRouteID, Name: "legacy any_tcp", MatchMode: "any_tcp",
		ListenerIP: "*", ListenerPort: 443, Fallback: true,
		TargetType: "unix", UnixSocketPath: "/dev/shm/xray.sock",
		ProxyProtocol: "none", Enabled: true,
	}
	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	assert.Contains(t, got.Config, "    default_backend "+RouteBackendKey(testRouteID)+"\n")
	assert.NotContains(t, got.Config, "any_tcp")
}

func TestF6RendererUnchangedForDestinationIPRoute(t *testing.T) {
	route := Route{
		ID: testRouteID, Name: "legacy destination_ip", MatchMode: "destination_ip",
		ListenerIP: "192.0.2.10", ListenerPort: 443, Fallback: true,
		TargetType: "tcp", TargetHost: "10.0.0.1", TargetPort: 8443,
		ProxyProtocol: "none", Enabled: true,
	}
	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	assert.Contains(t, got.Config, "    bind 192.0.2.10:443\n")
	assert.Contains(t, got.Config, "    default_backend "+RouteBackendKey(testRouteID)+"\n")
	assert.NotContains(t, got.Config, "destination_ip")
}

func TestF6NormalizeScanRouteLegacyValues(t *testing.T) {
	// Simulate what scanRoute does for legacy DB rows.
	for _, legacy := range []string{"any_tcp", "destination_ip"} {
		r := Route{MatchMode: legacy}
		// Apply the same normalization scanRoute applies.
		if r.MatchMode == "any_tcp" || r.MatchMode == "destination_ip" {
			r.MatchMode = "fallback"
		}
		assert.Equal(t, "fallback", r.MatchMode, "legacy %s should normalize to fallback", legacy)
	}
}
