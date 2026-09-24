package panel

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderConfigPreviewEndpoint(t *testing.T) {
	f := &fakeStore{routes: []Route{fallbackRoute(testRouteID)}}
	w := request(t, handler(f), http.MethodPost, "/api/v1/nodes/"+testNodeID+"/render-config", "", testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"node_id":"`+testNodeID+`"`)
	// No node facts: byte-identical v20 output.
	assert.Contains(t, w.Body.String(), `"renderer":"`+olderV20HAProxyRenderer+`"`)
	assert.Contains(t, w.Body.String(), `backend `+RouteBackendKey(testRouteID)+`\n`)
	assert.Contains(t, w.Body.String(), `"route_backends":{"`+testRouteID+`":"`+RouteBackendKey(testRouteID)+`"}`)

	w = request(t, handler(f), http.MethodGet, "/api/v1/nodes/"+testNodeID+"/render-config", "", testAdminToken)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestRenderConfigPreviewRejectsNoEnabledRoutes(t *testing.T) {
	f := &fakeStore{routes: []Route{{ID: testRouteID, Enabled: false}}}
	w := request(t, handler(f), http.MethodPost, "/api/v1/nodes/"+testNodeID+"/render-config", "", testAdminToken)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	assert.JSONEq(t, `{"error":{"code":"no_enabled_routes","message":"node has no enabled routes to render"}}`, w.Body.String())
}

func TestCreateConfigRevisionFromRoutesDoesNotAssignDesired(t *testing.T) {
	f := &fakeStore{routes: []Route{fallbackRoute(testRouteID)}}
	w := request(t, handler(f), http.MethodPost, "/api/v1/nodes/"+testNodeID+"/config-revisions/from-routes", `{"note":"operator preview"}`, testAdminToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	assert.Equal(t, "operator preview", f.createdConfig.Note)
	assert.Contains(t, f.createdConfig.Config, "stats socket /run/haproxy/admin.sock")
	assert.Equal(t, "routes", f.createdConfig.Metadata["source"])
	assert.Equal(t, olderV20HAProxyRenderer, f.createdConfig.Metadata["renderer"])
	backends, ok := f.createdConfig.Metadata["route_backends"].(map[string]string)
	require.True(t, ok)
	assert.Equal(t, RouteBackendKey(testRouteID), backends[testRouteID])
	assert.Zero(t, f.desiredRevision, "creating an immutable revision must not assign desired state")

	f.createdConfig = configRevisionInput{}
	w = request(t, handler(f), http.MethodPost, "/api/v1/nodes/"+testNodeID+"/config-revisions/from-routes", `{}`, testAdminToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	assert.True(t, strings.HasPrefix(f.createdConfig.Note, "Собрано: маршрутов 1"))
}

func TestCreateConfigRevisionFromRoutesValidatesInputAndRouteSet(t *testing.T) {
	f := &fakeStore{routes: []Route{fallbackRoute(testRouteID)}}
	w := request(t, handler(f), http.MethodPost, "/api/v1/nodes/"+testNodeID+"/config-revisions/from-routes", `{"note":"ok","assign":true}`, testAdminToken)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	w = request(t, handler(f), http.MethodPost, "/api/v1/nodes/"+testNodeID+"/config-revisions/from-routes", `{"note":"`+strings.Repeat("x", 501)+`"}`, testAdminToken)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	f.routes = nil
	w = request(t, handler(f), http.MethodPost, "/api/v1/nodes/"+testNodeID+"/config-revisions/from-routes", `{}`, testAdminToken)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Empty(t, f.createdConfig.Config)
}
