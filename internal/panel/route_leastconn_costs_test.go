package panel

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// leastconn connection costs (static weights) and tolerance (Agent 1.1.1
// runtime weights).

func TestLeastConnWithoutCostOrToleranceRendersUnchanged(t *testing.T) {
	_, got := renderPayload(t, `{"name":"p","match_mode":"fallback","balance_algorithm":"leastconn",
		"servers":[{"name":"a","host":"192.0.2.1","port":443,"weight":3},{"name":"b","host":"192.0.2.2","port":443}]}`)
	backend := backendSection(got.Config)
	assert.Contains(t, backend, "    balance leastconn\n")
	assert.Contains(t, backend, "    server a 192.0.2.1:443 weight 3 check")
	assert.Contains(t, backend, "    server b 192.0.2.2:443 check")
	assert.NotContains(t, backend, "nf-weight")
}

func TestLeastConnCostRendersStaticWeights(t *testing.T) {
	// base/cost: a = 1/2 = 0.5, b = 1/1 = 1, c = 2/0.5 = 4. K = min(100,
	// 256/4) = 64: a 32, b 64, c 256.
	_, got := renderPayload(t, `{"name":"p","match_mode":"fallback","balance_algorithm":"leastconn",
		"servers":[{"name":"a","host":"192.0.2.1","port":443,"cost":2},{"name":"b","host":"192.0.2.2","port":443},
		{"name":"c","host":"192.0.2.3","port":443,"weight":2,"cost":0.5}]}`)
	backend := backendSection(got.Config)
	assert.Contains(t, backend, "    balance leastconn\n")
	assert.Contains(t, backend, "    server a 192.0.2.1:443 weight 32 check")
	assert.Contains(t, backend, "    server b 192.0.2.2:443 weight 64 check")
	assert.Contains(t, backend, "    server c 192.0.2.3:443 weight 256 check")
	assert.NotContains(t, backend, "nf-weight", "static costs need no Agent")
}

func TestLeastConnCostInertUnderStickySource(t *testing.T) {
	_, got := renderPayload(t, `{"name":"p","match_mode":"fallback","balance_algorithm":"leastconn","sticky_mode":"source",
		"servers":[{"name":"a","host":"192.0.2.1","port":443,"cost":2},{"name":"b","host":"192.0.2.2","port":443}]}`)
	backend := backendSection(got.Config)
	assert.Contains(t, backend, "    server a 192.0.2.1:443 check")
}

func TestLeastConnPerIPCostUsesStaticAnnotation(t *testing.T) {
	// DNS pool: server weight 10 cost 2 (share 5); 192.0.2.5 cost 4 (share
	// 2.5); 192.0.2.6 weight 40 (share 20, cost inherited 2). Plain b share
	// 1. K = min(100, 256/20) = 12.8.
	payload := `{"name":"p","match_mode":"fallback","balance_algorithm":"leastconn","sticky_mode":"none",
		"servers":[{"name":"dp","host":"pool.example.com","port":443,"dns_pool":true,"weight":10,"cost":2,
			"ip_weights":[{"ip":"192.0.2.5","cost":4},{"ip":"192.0.2.6","weight":40}]},
			{"name":"b","host":"192.0.2.2","port":443}]}`
	_, got := renderPayload(t, payload)
	backend := backendSection(got.Config)
	assert.Contains(t, backend, "    balance leastconn\n")
	assert.Contains(t, backend, "# nf-weights backend=nf_be_"+routeRuntimeID(reviewRouteID)+" algo=static tolerance=0.00 tolerance_ms=0\n")
	assert.Contains(t, backend, "# nf-weight template=dp_ slots=32 base=64 cost=0.00 ips=192.0.2.5=32,192.0.2.6=256\n")
	assert.Contains(t, backend, "    server-template dp_ 32 pool.example.com:443 weight 64 ")
	assert.Contains(t, backend, "    server b 192.0.2.2:443 weight 13 check")
	assert.NotContains(t, backend, "server=b")
}

func TestLeastConnToleranceIsAgentManaged(t *testing.T) {
	payload := `{"name":"p","match_mode":"fallback","balance_mode":"pool","balance_algorithm":"leastconn","balance_tolerance":0.2,
		"servers":[{"name":"a","host":"192.0.2.1","port":443,"cost":2},{"name":"b","host":"192.0.2.2","port":443,"weight":3}]}`
	spec, got := renderPayload(t, payload)
	assert.True(t, specNeedsLeastConnAgent(spec))
	backend := backendSection(got.Config)
	assert.NotContains(t, backend, "balance ", "weighted round-robin follows the Agent weights")
	assert.Contains(t, backend, "# nf-weights backend=nf_be_"+routeRuntimeID(reviewRouteID)+" algo=leastconn tolerance=0.20 tolerance_ms=0\n")
	assert.Contains(t, backend, "# nf-weight server=a base=1 cost=2.00\n")
	assert.Contains(t, backend, "# nf-weight server=b base=3 cost=0.00\n")
	// Static base/cost weights stay on the server lines: they apply before
	// the first Agent pass and whenever the Agent is not running. base/cost:
	// a 0.5, b 3; K = min(100, 256/3) = 85.33: a 43, b 256.
	assert.Contains(t, backend, "    server a 192.0.2.1:443 weight 43 check")
	assert.Contains(t, backend, "    server b 192.0.2.2:443 weight 256 check")
}

func TestLeastConnToleranceUnderSourceTable(t *testing.T) {
	_, got := renderPayload(t, `{"name":"p","match_mode":"fallback","balance_algorithm":"leastconn","sticky_mode":"source_table","balance_tolerance":0.3,`+poolServers+`}`)
	backend := backendSection(got.Config)
	assert.NotContains(t, backend, "balance leastconn")
	assert.Contains(t, backend, "stick on src")
	assert.Contains(t, backend, "algo=leastconn tolerance=0.30")
}

func TestLeastConnToleranceAgentGateAtPublish(t *testing.T) {
	route := fallbackRoute(testRouteID)
	route.BalanceMode = "pool"
	route.BalanceAlgorithm = BalanceAlgorithmLeastConn
	route.StickyMode = StickyModeNone
	route.LeastPingTolerance = 0.2
	route.Servers = []RouteServer{
		{Position: 1, Name: "a", TargetType: "tcp", Host: "192.0.2.1", Port: 443},
		{Position: 2, Name: "b", TargetType: "tcp", Host: "192.0.2.2", Port: 443},
	}
	for _, version := range []string{"1.1.0", "1.1.1-rc1", ""} {
		err := checkLeastConnAgentRoutes([]Route{route}, version)
		var gateErr *LeastConnToleranceUnsupportedError
		require.ErrorAs(t, err, &gateErr, version)
		status, code, _ := renderErrorResponse(err)
		assert.Equal(t, 422, status)
		assert.Equal(t, "leastconn_tolerance_requires_agent_1_1_1", code)
	}
	assert.NoError(t, checkLeastConnAgentRoutes([]Route{route}, "1.1.1"))
	noTolerance := route
	noTolerance.LeastPingTolerance = 0
	assert.NoError(t, checkLeastConnAgentRoutes([]Route{noTolerance}, "1.0.5"), "costs alone are static")

	f := &fakeStore{nodes: []Node{{ID: testNodeID}}, routes: []Route{route}, agentVersion: "1.1.0"}
	w := request(t, handler(f), http.MethodPost, "/api/v1/nodes/"+testNodeID+"/render-config", `{}`, testAdminToken)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"code":"leastconn_tolerance_requires_agent_1_1_1"`)
	f.agentVersion = "1.1.1"
	w = request(t, handler(f), http.MethodPost, "/api/v1/nodes/"+testNodeID+"/render-config", `{}`, testAdminToken)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

func TestLeastConnFingerprintUnchangedWithoutTolerance(t *testing.T) {
	spec, err := validatePayload(t, `{"name":"p","match_mode":"fallback","balance_algorithm":"leastconn",`+poolServers+`}`)
	require.NoError(t, err)
	withZero, err := validatePayload(t, `{"name":"p","match_mode":"fallback","balance_algorithm":"leastconn","balance_tolerance":0,`+poolServers+`}`)
	require.NoError(t, err)
	assert.Equal(t, routeSpecFingerprint(spec), routeSpecFingerprint(withZero))
	withTolerance, err := validatePayload(t, `{"name":"p","match_mode":"fallback","balance_algorithm":"leastconn","balance_tolerance":0.2,`+poolServers+`}`)
	require.NoError(t, err)
	assert.NotEqual(t, routeSpecFingerprint(spec), routeSpecFingerprint(withTolerance))
}
