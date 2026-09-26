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

func TestHAProxySettingsValidation(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{"threads":257}`, `{"threads":1.5}`, `{"max_connections":-1}`, `{"timeout_client":"1s\nfrontend injected"}`, `{"timeout_server":"0s"}`, `{"unknown":true}`} {
		t.Run(raw, func(t *testing.T) {
			var value any
			require.NoError(t, json.Unmarshal([]byte(raw), &value))
			_, err := parseHAProxySettings(value)
			require.Error(t, err)
		})
	}
}

func TestHAProxySettingsRenderAndDefaultCompatibility(t *testing.T) {
	route := fallbackRoute(testRouteID)
	baseline, err := RenderHAProxyConfig([]Route{route})
	require.NoError(t, err)
	defaults, err := RenderHAProxyConfigForNode([]Route{route}, NodeRenderFacts{HAProxySettings: HAProxySettings{}})
	require.NoError(t, err)
	require.Equal(t, baseline, defaults)
	tuned, err := RenderHAProxyConfigForNode([]Route{route}, NodeRenderFacts{HAProxySettings: HAProxySettings{MaxConnections: 50000, Threads: 4, TimeoutConnect: "10s", TimeoutClient: "1h", TimeoutServer: "30m"}})
	require.NoError(t, err)
	for _, directive := range []string{"maxconn 50000", "nbthread 4", "timeout connect 10s", "timeout client 1h", "timeout server 30m"} {
		assert.Contains(t, tuned.Config, directive)
	}
	assert.NotEqual(t, baseline.SHA256, tuned.SHA256)
	assert.NotContains(t, tuned.Config, "timeout connect 5s")
	assert.Equal(t, 1, strings.Count(tuned.Config, "timeout client"))
	assert.Equal(t, baseline.RuntimeNames, tuned.RuntimeNames)
}

func TestHAProxySettingsHTTPValidationAndPreservation(t *testing.T) {
	f := &fakeStore{}
	w := request(t, handler(f), http.MethodPut, "/api/v1/nodes/"+testNodeID, `{"name":"node","address":"192.0.2.1","metadata":{"haproxy_settings":{"threads":-1}}}`, testAdminToken)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	previous := map[string]any{nodeHAProxySettingsKey: map[string]any{"threads": float64(4)}}
	merged := mergeNodeMetadata(previous, map[string]any{"agent_port": float64(4200)})
	require.Equal(t, previous[nodeHAProxySettingsKey], merged[nodeHAProxySettingsKey])
	reset := mergeNodeMetadata(previous, map[string]any{nodeHAProxySettingsKey: map[string]any{}})
	settings, err := settingsFromMetadata(reset)
	require.NoError(t, err)
	assert.Equal(t, HAProxySettings{}, settings)
}

func TestAdvancedConfigurationConflictResponse(t *testing.T) {
	w := httptest.NewRecorder()
	// Use the common store response mapper, shared by every route mutation.
	respondStore(w, nil, ErrAdvancedConfig, http.StatusOK)
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "advanced_config_active")
}

func TestResumeGeneratedConfigEndpoint(t *testing.T) {
	f := &fakeStore{}
	h := handler(f)
	path := "/api/v1/nodes/" + testNodeID + "/generated-config"
	w := request(t, h, http.MethodPost, path, "", "")
	require.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Zero(t, f.desiredRevision)
	w = request(t, h, http.MethodGet, path, "", testAdminToken)
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
	w = request(t, h, http.MethodPost, path, "", testAdminToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	assert.EqualValues(t, 1, f.desiredRevision)
	require.NotEmpty(t, f.auditEvents)
	assert.Equal(t, "revision.resume_generated", f.auditEvents[len(f.auditEvents)-1].Action)
}
