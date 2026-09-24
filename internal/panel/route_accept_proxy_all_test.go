package panel

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func acceptProxyAllRoute(id, sni, listenerIP string, from []string) Route {
	return Route{
		ID: id, Name: sni, MatchMode: "sni",
		ListenerIP: listenerIP, ListenerPort: 443, SNIs: []string{sni}, Hostname: sni,
		TargetType: "unix", UnixSocketPath: "/dev/shm/xray.sock",
		ProxyProtocol: "none", AcceptProxyFrom: from, Enabled: true,
	}
}

func TestValidateAcceptProxyFromAcceptsAllSentinel(t *testing.T) {
	for _, in := range [][]string{
		{"0.0.0.0/0", "::/0"},
		{"::/0", "0.0.0.0/0"},
		{"0.0.0.0/0"},
		{"::/0"},
	} {
		out, err := validateAcceptProxyFrom(in)
		require.NoError(t, err, in)
		assert.ElementsMatch(t, in, out)
	}
}

func TestRenderAcceptProxyFromAllIsUnconditional(t *testing.T) {
	cases := []struct {
		name       string
		listenerIP string
		from       []string
		all        bool
	}{
		{"wildcard both", "*", []string{"0.0.0.0/0", "::/0"}, true},
		{"wildcard reversed", "*", []string{"::/0", "0.0.0.0/0"}, true},
		{"wildcard v4 only", "*", []string{"0.0.0.0/0"}, true},
		{"wildcard v6 only", "*", []string{"::/0"}, false},
		{"ipv4 bind", "192.0.2.1", []string{"0.0.0.0/0"}, true},
		{"ipv6 bind v6", "2001:db8::10", []string{"::/0"}, true},
		{"ipv6 bind v4 only", "2001:db8::10", []string{"0.0.0.0/0"}, false},
		{"dual-stack bind both", "::", []string{"0.0.0.0/0", "::/0"}, true},
		{"dual-stack bind v4 only", "::", []string{"0.0.0.0/0"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			route := acceptProxyAllRoute(testRouteID, "vpn.example.com", tc.listenerIP, tc.from)
			got, err := RenderHAProxyConfig([]Route{route})
			require.NoError(t, err)
			assert.Equal(t, 1, strings.Count(got.Config, "expect-proxy layer4"))
			if tc.all {
				assert.Contains(t, got.Config, "    # accept PROXY protocol header from all sources\n    tcp-request connection expect-proxy layer4\n")
				assert.NotContains(t, got.Config, "expect-proxy layer4 if")
			} else {
				assert.Contains(t, got.Config, "    tcp-request connection expect-proxy layer4 if { src ")
			}
		})
	}
}

func TestRenderAcceptProxyFromUnionWithAllIsUnconditional(t *testing.T) {
	all := acceptProxyAllRoute("11111111-1111-4111-8111-111111111111", "a.example.com", "*", []string{"0.0.0.0/0", "::/0"})
	list := acceptProxyAllRoute("22222222-2222-4222-8222-222222222222", "b.example.com", "*", []string{"10.0.0.1", "192.0.2.0/24"})
	got, err := RenderHAProxyConfig([]Route{all, list})
	require.NoError(t, err)
	assert.Contains(t, got.Config, "    tcp-request connection expect-proxy layer4\n")
	assert.NotContains(t, got.Config, "if { src")
	assert.Equal(t, 1, strings.Count(got.Config, "expect-proxy layer4"))

	// Without the "all" route the list keeps the conditional form.
	got, err = RenderHAProxyConfig([]Route{list})
	require.NoError(t, err)
	assert.Contains(t, got.Config, "    tcp-request connection expect-proxy layer4 if { src 10.0.0.1 192.0.2.0/24 }\n")
}

func TestAcceptProxyFromAllDoesNotChangeExistingFingerprints(t *testing.T) {
	// Pinned hash of a route spec that predates the sentinel: the renderer
	// change must not touch fingerprints of existing routes.
	spec := RouteSpec{
		Name: "test", ListenerIP: "*", ListenerPort: 443, MatchMode: "sni",
		SNIs: []string{"vpn.example.com"}, TargetType: "unix", UnixSocketPath: "/dev/shm/xray.sock",
		HealthCheck: true, ProxyProtocol: "none", QuotaAction: "observe", QuotaPeriod: "calendar_month", Enabled: true,
		AcceptProxyFrom: []string{"192.0.2.1"},
	}
	before := "bf83be6d481216117c75c1743c89ace14402d6480e4b1ae4171fbcbff99221c2"
	route := acceptProxyAllRoute(testRouteID, "vpn.example.com", "*", []string{"192.0.2.1"})
	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	assert.Contains(t, got.Config, "expect-proxy layer4 if { src 192.0.2.1 }")
	assert.Equal(t, before, routeSpecFingerprint(spec))
}
