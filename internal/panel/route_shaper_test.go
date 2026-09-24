package panel

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKernelShaperModeOmitsBwlimAndEmitsAnnotation(t *testing.T) {
	download := int64(100)
	upload := int64(25)
	route := fallbackRoute(testRouteID)
	route.ListenerPort = 8443
	route.ClientDownloadMbps = &download
	route.ClientUploadMbps = &upload
	route.ShaperMode = ShaperModeKernel

	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	assert.NotContains(t, got.Config, "bwlim")
	assert.NotContains(t, got.Config, "set-bandwidth-limit")
	assert.NotContains(t, got.Config, "stick-table")
	assert.Contains(t, got.Config, "option splice-request")
	assert.Contains(t, got.Config, "    # nf-kernel-shaper listen=* port=8443 download_bps=12500000 upload_bps=3125000\n")
}

func TestHAProxyShaperModeKeepsBwlim(t *testing.T) {
	download := int64(10)
	route := fallbackRoute(testRouteID)
	route.ClientDownloadMbps = &download
	for _, mode := range []string{"", ShaperModeHAProxy} {
		route.ShaperMode = mode
		got, err := RenderHAProxyConfig([]Route{route})
		require.NoError(t, err)
		assert.Contains(t, got.Config, "filter bwlim-out")
		assert.NotContains(t, got.Config, "nf-kernel-shaper")
	}
}

func TestKernelShaperModeWithoutLimitsEmitsNothing(t *testing.T) {
	route := fallbackRoute(testRouteID)
	route.ShaperMode = ShaperModeKernel
	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	assert.NotContains(t, got.Config, "nf-kernel-shaper")
	assert.NotContains(t, got.Config, "bwlim")
}

func TestKernelShaperListenerValidation(t *testing.T) {
	ten, twenty := int64(10), int64(20)
	sni := Route{
		ID: "11111111-1111-4111-8111-111111111111", Name: "sni", MatchMode: "sni",
		ListenerIP: "*", ListenerPort: 443, SNIs: []string{"vpn.example.com"}, Hostname: "vpn.example.com",
		TargetType: "tcp", TargetHost: "192.0.2.10", TargetPort: 443, ProxyProtocol: "none", Enabled: true,
		ClientDownloadMbps: &ten, ShaperMode: ShaperModeKernel,
	}
	fallback := fallbackRoute("22222222-2222-4222-8222-222222222222")
	fallback.ListenerPort = 443

	// Same limits on a shared listener: allowed, annotation de-duplicated by the Agent.
	fallback.ClientDownloadMbps = &ten
	fallback.ShaperMode = ShaperModeKernel
	got, err := RenderHAProxyConfig([]Route{fallback, sni})
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(got.Config, "nf-kernel-shaper listen=* port=443 download_bps=1250000 upload_bps=0"))

	// Different limits: kernel cannot tell SNI routes apart.
	fallback.ClientDownloadMbps = &twenty
	_, err = RenderHAProxyConfig([]Route{fallback, sni})
	var invalid *RouteSetError
	require.ErrorAs(t, err, &invalid)
	assert.Contains(t, err.Error(), "different client limits")

	// Mixing kernel and haproxy shaping on one listener.
	fallback.ClientDownloadMbps = &ten
	fallback.ShaperMode = ShaperModeHAProxy
	_, err = RenderHAProxyConfig([]Route{fallback, sni})
	require.ErrorAs(t, err, &invalid)
	assert.Contains(t, err.Error(), "mixes shaper_mode")

	// Unlimited haproxy-mode route next to a kernel-shaped route is fine.
	fallback.ClientDownloadMbps = nil
	got, err = RenderHAProxyConfig([]Route{fallback, sni})
	require.NoError(t, err)
	assert.NotContains(t, got.Config, "bwlim")
}

func TestNormalizeShaperMode(t *testing.T) {
	for in, want := range map[string]string{"": "haproxy", "haproxy": "haproxy", " Kernel ": "kernel"} {
		got, err := normalizeShaperMode(in)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
	_, err := normalizeShaperMode("tc")
	assert.Error(t, err)
}

func TestShaperModeFingerprintBackwardCompatible(t *testing.T) {
	spec := RouteSpec{Name: "x", ListenerIP: "*", ListenerPort: 443, MatchMode: "fallback"}
	legacy := routeSpecFingerprint(spec)
	spec.ShaperMode = ShaperModeHAProxy
	assert.Equal(t, legacy, routeSpecFingerprint(spec), "default mode must not change existing fingerprints")
	spec.ShaperMode = ShaperModeKernel
	assert.NotEqual(t, legacy, routeSpecFingerprint(spec))
}
