package panel

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateAcceptProxyFromAcceptsHostnames(t *testing.T) {
	out, err := validateAcceptProxyFrom([]string{" Edge.Example.COM. ", "10.0.0.0/8", "edge.example.com", "192.0.2.1", "localhost"})
	require.NoError(t, err)
	assert.Equal(t, []string{"10.0.0.0/8", "192.0.2.1", "edge.example.com", "localhost"}, out)
}

func TestValidateAcceptProxyFromLimitsHostnames(t *testing.T) {
	entries := make([]string, 0, MaxAcceptProxyFromDomains+1)
	for i := 0; i < MaxAcceptProxyFromDomains; i++ {
		entries = append(entries, fmt.Sprintf("h%d.example.com", i))
	}
	_, err := validateAcceptProxyFrom(append(entries, "h0.example.com", "192.0.2.1"))
	require.NoError(t, err, "duplicates do not count twice")
	_, err = validateAcceptProxyFrom(append(entries, "extra.example.com"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "more than 32 hostnames")
}

func TestRenderAcceptProxyFromHostnamesUsesAgentACLFile(t *testing.T) {
	route := acceptProxyAllRoute(testRouteID, "vpn.example.com", "*", []string{"192.0.2.10", "edge.example.com", "10.0.0.0/8", "b.example.net"})
	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	fe := frontendRuntimeName("*", 443)
	file := "/etc/haproxy/nodeflow/pp-trusted-" + fe + ".acl"
	assert.Contains(t, got.Config, "    # accept PROXY protocol header from trusted sources only\n"+
		"    # nf-pp-trusted file="+file+" domains=b.example.net,edge.example.com\n"+
		"    acl nf_pp_trusted src -f "+file+"\n"+
		"    tcp-request connection expect-proxy layer4 if { src 10.0.0.0/8 192.0.2.10 } || nf_pp_trusted\n")
	assert.Equal(t, 1, strings.Count(got.Config, "expect-proxy layer4"))

	onlyDomain := acceptProxyAllRoute(testRouteID, "vpn.example.com", "*", []string{"edge.example.com"})
	got, err = RenderHAProxyConfig([]Route{onlyDomain})
	require.NoError(t, err)
	assert.Contains(t, got.Config, "    tcp-request connection expect-proxy layer4 if nf_pp_trusted\n")
	assert.Equal(t, olderV20HAProxyRenderer, got.Renderer, "no renderer bump: the Agent gate protects older nodes")

	// "Everyone" still wins over hostnames on the same listener.
	all := acceptProxyAllRoute(testRouteID, "vpn.example.com", "*", []string{"0.0.0.0/0", "edge.example.com"})
	got, err = RenderHAProxyConfig([]Route{all})
	require.NoError(t, err)
	assert.Contains(t, got.Config, "    tcp-request connection expect-proxy layer4\n")
	assert.NotContains(t, got.Config, "nf-pp-trusted")
}

func TestRenderAcceptProxyFromWithoutHostnamesUnchanged(t *testing.T) {
	route := acceptProxyAllRoute(testRouteID, "vpn.example.com", "*", []string{"192.0.2.10", "10.0.0.0/8"})
	got, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	assert.Contains(t, got.Config, "    # accept PROXY protocol header from trusted sources only\n"+
		"    tcp-request connection expect-proxy layer4 if { src 10.0.0.0/8 192.0.2.10 }\n")
	assert.NotContains(t, got.Config, "nf_pp_trusted")
}

func TestPPTrustedDomainsAgentGate(t *testing.T) {
	route := acceptProxyAllRoute(testNodeID, "vpn.example.com", "*", []string{"edge.example.com"})
	route.ID = testRouteID
	for _, version := range []string{"1.1.2", "1.1.3-rc1", ""} {
		err := checkPPTrustedDomainsAgentRoutes([]Route{route}, version)
		var gateErr *PPTrustedDomainsUnsupportedError
		require.ErrorAs(t, err, &gateErr, version)
		status, code, _ := renderErrorResponse(err)
		assert.Equal(t, 422, status)
		assert.Equal(t, "pp_trusted_domains_requires_agent_1_1_3", code)
	}
	assert.NoError(t, checkPPTrustedDomainsAgentRoutes([]Route{route}, "1.1.3"))
	ipOnly := route
	ipOnly.AcceptProxyFrom = []string{"192.0.2.1", "10.0.0.0/8", "2001:db8::/32"}
	assert.NoError(t, checkPPTrustedDomainsAgentRoutes([]Route{ipOnly}, "1.0.0"))
	disabled := route
	disabled.Enabled = false
	assert.NoError(t, checkPPTrustedDomainsAgentRoutes([]Route{disabled}, "1.0.0"))

	f := &fakeStore{nodes: []Node{{ID: testNodeID}}, routes: []Route{route}, agentVersion: "1.1.2"}
	w := request(t, handler(f), http.MethodPost, "/api/v1/nodes/"+testNodeID+"/render-config", `{}`, testAdminToken)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"code":"pp_trusted_domains_requires_agent_1_1_3"`)
	f.agentVersion = "1.1.3"
	w = request(t, handler(f), http.MethodPost, "/api/v1/nodes/"+testNodeID+"/render-config", `{}`, testAdminToken)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

// TestRenderedPPTrustedDomainsPassHAProxyValidation checks the rendered
// hostname form with a real HAProxy (opt-in). The ACL path is redirected to
// a temporary file, as the Agent would have created it.
func TestRenderedPPTrustedDomainsPassHAProxyValidation(t *testing.T) {
	binary := os.Getenv("NODEFLOW_TEST_HAPROXY_BINARY")
	if binary == "" {
		t.Skip("NODEFLOW_TEST_HAPROXY_BINARY is not set")
	}
	route := acceptProxyAllRoute(testRouteID, "vpn.example.com", "*", []string{"192.0.2.10", "edge.example.com"})
	route.ListenerPort = 18443
	result, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(dir+"/trusted.acl", []byte("127.0.0.1\n::1\n"), 0o600))
	config := strings.ReplaceAll(result.Config, "    user haproxy\n    group haproxy\n", "")
	config = strings.ReplaceAll(config, ppTrustedACLFile(frontendRuntimeName("*", 18443)), dir+"/trusted.acl")
	path := dir + "/haproxy.cfg"
	require.NoError(t, os.WriteFile(path, []byte(config), 0o600))
	output, err := exec.Command(binary, "-c", "-f", path).CombinedOutput()
	require.NoError(t, err, string(output))
}
