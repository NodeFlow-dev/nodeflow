package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for QA report 07 (1.1.0-rc11).

func qaStoredFailoverRoute() Route {
	download := int64(5)
	return Route{
		ID: testRouteID, NodeID: testNodeID, Name: "qa-toggle", Version: 3,
		ListenerIP: "*", ListenerPort: 9450, MatchMode: "fallback", Fallback: true, SNIs: []string{},
		TargetType: "tcp", TargetHost: "127.0.0.1", TargetPort: 9903,
		HealthCheck: true, ProxyProtocol: "none", AcceptProxyFrom: []string{"10.0.0.0/8"},
		QuotaAction: "observe", QuotaPeriod: "calendar_month", ClientDownloadMbps: &download,
		Enabled: true, Deployed: true, DeploymentState: "active",
		BalanceMode: "failover", ShaperMode: ShaperModeKernel, StickyMode: "",
		Servers: []RouteServer{
			{Position: 1, Name: "p", TargetType: "tcp", Host: "127.0.0.1", Port: 9903},
			{Position: 2, Name: "r", TargetType: "unix", UnixSocketPath: "/dev/shm/nf-test.sock", Backup: true},
		},
	}
}

// B1: the exact pre-fix NodeDetailPage.routeInput body (no servers,
// balance_mode, accept_proxy_from, sticky or shaper fields) must keep them.
func TestQARouteTogglePutKeepsAbsentFields(t *testing.T) {
	f := &fakeStore{routes: []Route{qaStoredFailoverRoute()}}
	body := `{"expected_version":3,"name":"qa-toggle","match_mode":"fallback","hostname":"",
		"listener_ip":"*","listener_port":9450,"snis":[],"fallback":true,"target_type":"tcp",
		"target_host":"127.0.0.1","target_port":9903,"dns_pool":false,"unix_socket_path":"",
		"health_check":true,"proxy_protocol":"none","quota_bytes":null,"quota_action":"observe",
		"quota_period":"calendar_month","client_upload_mbps":null,"client_download_mbps":5,
		"enabled":false,"custom_fragment":""}`
	w := request(t, handler(f), http.MethodPut, "/api/v1/nodes/"+testNodeID+"/routes/"+testRouteID, body, testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	spec := f.updatedRoute
	require.Len(t, spec.Servers, 2)
	assert.False(t, spec.Servers[0].Backup)
	assert.True(t, spec.Servers[1].Backup)
	assert.Equal(t, "failover", spec.BalanceMode)
	assert.Equal(t, []string{"10.0.0.0/8"}, spec.AcceptProxyFrom)
	assert.Equal(t, ShaperModeKernel, spec.ShaperMode)
	assert.False(t, spec.Enabled)
}

// Explicit values, including empty arrays, still replace the stored ones.
func TestQARouteTogglePutExplicitEmptyClears(t *testing.T) {
	f := &fakeStore{routes: []Route{qaStoredFailoverRoute()}}
	body := `{"name":"qa-toggle","match_mode":"fallback","listener_port":9450,"fallback":true,
		"target_type":"tcp","target_host":"127.0.0.1","target_port":9903,"enabled":true,
		"servers":[],"accept_proxy_from":[],"shaper_mode":"haproxy","sticky_mode":"roundrobin"}`
	w := request(t, handler(f), http.MethodPut, "/api/v1/nodes/"+testNodeID+"/routes/"+testRouteID, body, testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Empty(t, f.updatedRoute.Servers)
	assert.Empty(t, f.updatedRoute.AcceptProxyFrom)
	assert.Equal(t, ShaperModeHAProxy, f.updatedRoute.ShaperMode)
}

// Absent servers keep the stored backup flags verbatim, even a layout that
// is not "first primary, rest backup".
func TestQARoutePutAbsentServersKeepsExplicitBackupLayout(t *testing.T) {
	stored := qaStoredFailoverRoute()
	stored.ShaperMode = ShaperModeHAProxy
	stored.Servers = []RouteServer{
		{Position: 1, Name: "dead1", TargetType: "tcp", Host: "192.0.2.1", Port: 443},
		{Position: 2, Name: "dead2", TargetType: "tcp", Host: "192.0.2.2", Port: 443},
		{Position: 3, Name: "dr", TargetType: "tcp", Host: "192.0.2.3", Port: 443, Backup: true},
	}
	f := &fakeStore{routes: []Route{stored}}
	body := `{"name":"renamed","match_mode":"fallback","listener_port":9450,"fallback":true,
		"target_type":"tcp","target_host":"192.0.2.1","target_port":443,"enabled":true}`
	w := request(t, handler(f), http.MethodPut, "/api/v1/nodes/"+testNodeID+"/routes/"+testRouteID, body, testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, f.updatedRoute.Servers, 3)
	assert.Equal(t, []bool{false, false, true}, []bool{f.updatedRoute.Servers[0].Backup, f.updatedRoute.Servers[1].Backup, f.updatedRoute.Servers[2].Backup})
}

// B1 contract: PATCH {enabled, expected_version} toggles only enabled and
// returns the full route including servers.
func TestQARoutePatchEnabledContract(t *testing.T) {
	f := &fakeStore{routes: []Route{qaStoredFailoverRoute()}}
	h := handler(f)
	path := "/api/v1/nodes/" + testNodeID + "/routes/" + testRouteID
	w := request(t, h, http.MethodPatch, path, `{"enabled":false,"expected_version":3}`, testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var got Route
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.False(t, got.Enabled)
	require.Len(t, got.Servers, 2)
	assert.True(t, got.Servers[1].Backup)
	assert.Equal(t, []string{"10.0.0.0/8"}, got.AcceptProxyFrom)
	assert.Equal(t, ShaperModeKernel, got.ShaperMode)
	assert.Equal(t, "failover", got.BalanceMode)
	assert.Equal(t, routeServersToSpec(qaStoredFailoverRoute().Servers), f.updatedRoute.Servers)

	w = request(t, h, http.MethodPatch, path, `{"enabled":true,"expected_version":2}`, testAdminToken)
	assert.Equal(t, http.StatusConflict, w.Code)
	w = request(t, h, http.MethodPatch, path, `{"expected_version":3}`, testAdminToken)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	w = request(t, h, http.MethodPatch, path, `{"enabled":true,"name":"x"}`, testAdminToken)
	assert.Equal(t, http.StatusBadRequest, w.Code, "only enabled/expected_version are accepted")
	w = request(t, h, http.MethodPatch, path, `{"enabled":true,"expected_version":0}`, testAdminToken)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// H1: version gate.
func TestQAAgentVersionKernelShaperGate(t *testing.T) {
	for version, want := range map[string]bool{
		"1.1.0": true, "1.1.1": true, "1.2.0": true, "2.0.0": true, "v1.1.0": true, "1.1.0+build.5": true,
		"1.0.5": false, "1.0.99": false, "1.1.0-rc11": false, "0.9.9": false,
		"": false, "integration": false, "1.1": false, "1.1.x": false,
	} {
		assert.Equal(t, want, agentSupportsKernelShaper(version), version)
	}
}

func TestQAKernelShaperRejectedOnOldAgentAtRender(t *testing.T) {
	download := int64(1)
	route := fallbackRoute(testRouteID)
	route.ClientDownloadMbps = &download
	route.ShaperMode = ShaperModeKernel
	for _, version := range []string{"1.0.5", ""} {
		err := checkKernelShaperRoutes([]Route{route}, version)
		var kernelErr *KernelShaperUnsupportedError
		require.ErrorAs(t, err, &kernelErr, version)
		status, code, _ := renderErrorResponse(err)
		assert.Equal(t, 422, status)
		assert.Equal(t, "kernel_shaper_requires_agent_1_1", code)
	}
	assert.NoError(t, checkKernelShaperRoutes([]Route{route}, "1.1.0"))
	// Kernel mode without limits and disabled routes need no Agent support.
	noLimit := fallbackRoute(testRouteID)
	noLimit.ShaperMode = ShaperModeKernel
	disabled := route
	disabled.Enabled = false
	assert.NoError(t, checkKernelShaperRoutes([]Route{noLimit, disabled}, "1.0.5"))

	f := &fakeStore{nodes: []Node{{ID: testNodeID}}, routes: []Route{route}, agentVersion: "1.0.5"}
	w := request(t, handler(f), http.MethodPost, "/api/v1/nodes/"+testNodeID+"/render-config", `{}`, testAdminToken)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"code":"kernel_shaper_requires_agent_1_1"`)
	f.agentVersion = "1.1.0"
	w = request(t, handler(f), http.MethodPost, "/api/v1/nodes/"+testNodeID+"/render-config", `{}`, testAdminToken)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

func TestQAKernelShaperStoreErrorResponse(t *testing.T) {
	w := httptest.NewRecorder()
	respondStore(w, nil, &KernelShaperUnsupportedError{AgentVersion: "1.0.5"}, 200)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Contains(t, w.Body.String(), `"code":"kernel_shaper_requires_agent_1_1"`)
	assert.Contains(t, w.Body.String(), "1.0.5")
}

// M2: numeric pseudo-IPv4 hosts are not DNS names.
func TestQANumericHostsRejected(t *testing.T) {
	for _, host := range []string{"010.0.0.1", "1.2.3", "300.1.1.1", "1.2.3.4.5", "0x7f.0.0.1", "127.1", "1", "a.b.123", "example.0x1f"} {
		_, ok := normalizeTargetHost(host)
		assert.False(t, ok, host)
	}
	for _, host := range []string{"1.2.3.4", "::1", "example.com", "123.example.com", "a1.b2", "0xdead.example", "xn--80ak6aa92e.com", "1e100.net"} {
		_, ok := normalizeTargetHost(host)
		assert.True(t, ok, host)
	}
	f := &fakeStore{}
	for _, body := range []string{
		`{"listener_port":9451,"match_mode":"fallback","target_type":"tcp","target_host":"010.0.0.1","target_port":80}`,
		`{"listener_port":9451,"match_mode":"fallback","servers":[{"name":"a","host":"1.2.3","port":80},{"name":"b","host":"192.0.2.1","port":80}]}`,
	} {
		w := request(t, handler(f), http.MethodPost, "/api/v1/nodes/"+testNodeID+"/routes", body, testAdminToken)
		assert.Equal(t, http.StatusBadRequest, w.Code, body)
	}
}

// L1: only the literal 0.0.0.0/0 and ::/0 mean "all".
func TestQAAcceptProxyFromZeroPrefixes(t *testing.T) {
	for _, value := range []string{"::ffff:0:0/96", "::ffff:0.0.0.0/96", "::ffff:10.0.0.0/104", "1.2.3.4/0", "10.0.0.0/0", "2001:db8::/0"} {
		_, err := validateAcceptProxyFrom([]string{value})
		assert.Error(t, err, value)
	}
	got, err := validateAcceptProxyFrom([]string{"0.0.0.0/0", "::/0", "10.1.2.3/8", "2001:db8::1/32", "192.0.2.1"})
	require.NoError(t, err)
	assert.Equal(t, []string{"0.0.0.0/0", "10.0.0.0/8", "192.0.2.1", "2001:db8::/32", "::/0"}, got)
	assert.False(t, acceptProxyCoversListener("*", []string{"10.0.0.0/8"}))
}

// L2: an identical PUT on a settled route is a no-op.
func TestQARouteUpdateNoop(t *testing.T) {
	stored := qaStoredFailoverRoute()
	spec := routeAsSpec(stored, true)
	normalizeRouteSpecDefaults(&spec)
	stored.DesiredFingerprint = routeSpecFingerprint(spec)
	stored.DeployedFingerprint = stored.DesiredFingerprint
	assert.True(t, routeUpdateIsNoop(stored, spec))

	changed := spec
	changed.TargetPort = 9904
	assert.False(t, routeUpdateIsNoop(stored, changed))
	disabled := routeAsSpec(stored, false)
	assert.False(t, routeUpdateIsNoop(stored, disabled))
	failed := stored
	failed.DeploymentState = "failed"
	assert.False(t, routeUpdateIsNoop(failed, spec), "a repeated save is the retry of a failed route")
	pending := stored
	pending.DeployedFingerprint = "other"
	assert.False(t, routeUpdateIsNoop(pending, spec))
	sticky := spec
	sticky.StickyTTL = "2h"
	assert.False(t, routeUpdateIsNoop(stored, sticky))
}

// L4: hashed assets are immutable; the shell stays no-store.
func TestQAHashedAssetCaching(t *testing.T) {
	h := NewHandler(&fakeStore{}, Config{AdminToken: testAdminToken})
	index := httptest.NewRecorder()
	h.ServeHTTP(index, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusOK, index.Code)
	assert.Equal(t, "no-store", index.Header().Get("Cache-Control"))
	asset := ""
	for _, line := range strings.Split(index.Body.String(), `"`) {
		if strings.HasPrefix(line, "/assets/") && strings.HasSuffix(line, ".js") {
			asset = line
			break
		}
	}
	require.NotEmpty(t, asset)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, asset, nil))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "public, max-age=31536000, immutable", w.Header().Get("Cache-Control"))

	assert.True(t, hashedAssetPath("assets/index-C4b8adjg.js"))
	assert.True(t, hashedAssetPath("assets/index-DyWCGJ-L.js"))
	assert.True(t, hashedAssetPath("assets/RouteEditorPage-BhkBjYMj.css"))
	assert.False(t, hashedAssetPath("index.html"))
	assert.False(t, hashedAssetPath("assets/logo.svg"))
	assert.False(t, hashedAssetPath("assets/sub/x-C4b8adjg.js"))
}

func TestQAMetricsTokenCompare(t *testing.T) {
	token := strings.Repeat("m", 40)
	next := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	h := metricsAuthMiddleware(token, nil, next)
	for header, want := range map[string]int{
		"Bearer " + token:        http.StatusOK,
		"Bearer " + token + "x":  http.StatusUnauthorized,
		"Bearer " + token[:39]:   http.StatusUnauthorized,
		"Bearer ":                http.StatusUnauthorized,
		"":                       http.StatusUnauthorized,
		"Basic " + token:         http.StatusUnauthorized,
		"bearer " + token + "  ": http.StatusUnauthorized,
	} {
		r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		w := httptest.NewRecorder()
		h(w, r)
		assert.Equal(t, want, w.Code, header)
	}
}
