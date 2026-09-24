package panel

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateAcceptProxyFromNormalizes(t *testing.T) {
	out, err := validateAcceptProxyFrom([]string{
		"192.0.2.1",
		"2001:db8::1",
		"10.0.0.0/8",
		// duplicate — should be deduplicated
		"192.0.2.1",
		// normalizable CIDR (host bits set)
		"10.0.0.5/8",
	})
	require.NoError(t, err)
	// normalised CIDRs collapse duplicates; 10.0.0.5/8 → 10.0.0.0/8 == "10.0.0.0/8"
	assert.NotContains(t, out, "10.0.0.5/8")
	assert.Contains(t, out, "10.0.0.0/8")
	assert.Contains(t, out, "192.0.2.1")
	assert.Contains(t, out, "2001:db8::1")
	// sorted
	for i := 1; i < len(out); i++ {
		assert.LessOrEqual(t, out[i-1], out[i])
	}
}

func TestValidateAcceptProxyFromRejectsInvalid(t *testing.T) {
	for _, entry := range []string{"not_an_ip", "*.example.com", "http://example.com", "example.com:443", "1.2.3", "-bad.example.com", "a..example.com", strings.Repeat("a", 64) + ".com"} {
		_, err := validateAcceptProxyFrom([]string{entry})
		require.Error(t, err, entry)
		assert.Contains(t, err.Error(), "not a valid IP address, CIDR or hostname", entry)
	}
}

func TestValidateAcceptProxyFromRejectsControlChars(t *testing.T) {
	// control char in the middle, not trimmed by TrimSpace
	_, err := validateAcceptProxyFrom([]string{"192.0.2\x011"})
	require.Error(t, err)
}

func TestValidateAcceptProxyFromRejectsTooMany(t *testing.T) {
	entries := make([]string, MaxAcceptProxyFromEntries+1)
	for i := range entries {
		entries[i] = "192.0.2.1"
	}
	_, err := validateAcceptProxyFrom(entries)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accept_proxy_from must not exceed")
}

func TestRenderHAProxyConfigEmitsExpectProxyACL(t *testing.T) {
	route := Route{
		ID: testRouteID, Name: "proxied sni", MatchMode: "sni",
		ListenerIP: "*", ListenerPort: 443, SNIs: []string{"vpn.example.com"}, Hostname: "vpn.example.com",
		TargetType: "unix", UnixSocketPath: "/dev/shm/xray.sock",
		ProxyProtocol:   "none",
		AcceptProxyFrom: []string{"192.0.2.10", "10.0.0.0/8"},
		Enabled:         true,
	}

	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	assert.Contains(t, got.Config, "    # accept PROXY protocol header from trusted sources only\n")
	assert.Contains(t, got.Config, "    tcp-request connection expect-proxy layer4 if { src ")
	assert.Contains(t, got.Config, "10.0.0.0/8")
	assert.Contains(t, got.Config, "192.0.2.10")
	// must appear BEFORE tcp-request inspect-delay (if any) and BEFORE acl lines
	expectPos := strings.Index(got.Config, "expect-proxy layer4")
	aclPos := strings.Index(got.Config, "acl nf_sni_")
	assert.Less(t, expectPos, aclPos, "expect-proxy must precede acl lines")
}

func TestRenderHAProxyConfigNoExpectProxyWhenEmpty(t *testing.T) {
	route := fallbackRoute(testRouteID)
	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	assert.NotContains(t, got.Config, "expect-proxy")
}

func TestRenderHAProxyConfigUnionsAcceptProxyFromAcrossRoutes(t *testing.T) {
	r1 := Route{
		ID: "11111111-1111-4111-8111-111111111111", Name: "r1", MatchMode: "sni",
		ListenerIP: "*", ListenerPort: 443, SNIs: []string{"a.example.com"}, Hostname: "a.example.com",
		TargetType: "unix", UnixSocketPath: "/dev/shm/xray.sock",
		ProxyProtocol: "none", AcceptProxyFrom: []string{"10.0.0.1"},
		Enabled: true,
	}
	r2 := Route{
		ID: "22222222-2222-4222-8222-222222222222", Name: "r2", MatchMode: "sni",
		ListenerIP: "*", ListenerPort: 443, SNIs: []string{"b.example.com"}, Hostname: "b.example.com",
		TargetType: "unix", UnixSocketPath: "/dev/shm/xray.sock",
		ProxyProtocol: "none", AcceptProxyFrom: []string{"10.0.0.2"},
		Enabled: true,
	}
	got, err := RenderHAProxyConfig([]Route{r1, r2})
	require.NoError(t, err)
	assert.Contains(t, got.Config, "10.0.0.1")
	assert.Contains(t, got.Config, "10.0.0.2")
	// only one expect-proxy line per listener
	assert.Equal(t, 1, strings.Count(got.Config, "expect-proxy layer4"))
}

func TestAcceptProxyFromChangesRouteFingerprint(t *testing.T) {
	base := RouteSpec{
		Name: "test", ListenerIP: "*", ListenerPort: 443, MatchMode: "sni",
		SNIs: []string{"vpn.example.com"}, TargetType: "unix", UnixSocketPath: "/dev/shm/xray.sock",
		HealthCheck: true, ProxyProtocol: "none", QuotaAction: "observe", QuotaPeriod: "calendar_month", Enabled: true,
	}
	withProxy := base
	withProxy.AcceptProxyFrom = []string{"192.0.2.1"}
	assert.NotEqual(t, routeSpecFingerprint(base), routeSpecFingerprint(withProxy))
}
