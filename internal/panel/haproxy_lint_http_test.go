package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const lintCleanConfig = "global\n    daemon\ndefaults\n    mode tcp\n    timeout connect 5s\nfrontend fe\n    bind :443\n    bind [::]:8443\n    default_backend be\nbackend be\n    server s1 192.0.2.1:443\n"

func TestConfigLintEndpoint(t *testing.T) {
	f := &fakeStore{}
	h := handler(f)
	body, err := json.Marshal(map[string]string{"config": lintCleanConfig})
	require.NoError(t, err)
	w := request(t, h, http.MethodPost, "/api/v1/nodes/"+testNodeID+"/config-lint", string(body), testAdminToken)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var got struct {
		Valid            bool          `json:"valid"`
		Issues           []LintIssue   `json:"issues"`
		ListenerTCPPorts []int         `json:"listener_tcp_ports"`
		Sections         []LintSection `json:"sections"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.True(t, got.Valid)
	assert.NotNil(t, got.Issues)
	assert.Empty(t, got.Issues)
	assert.Equal(t, []int{443, 8443}, got.ListenerTCPPorts)
	assert.Equal(t, LintSection{Type: "frontend", Name: "fe", Line: 6}, got.Sections[2])
	assert.Empty(t, f.auditEvents, "lint is read-only and not audited")

	w = request(t, h, http.MethodPost, "/api/v1/nodes/"+testNodeID+"/config-lint", `{"config":"global\n    timeout x\nfrontend\n"}`, testAdminToken)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"valid":false`)
	assert.Contains(t, w.Body.String(), `"end_column"`)

	w = request(t, h, http.MethodGet, "/api/v1/nodes/"+testNodeID+"/config-lint", "", testAdminToken)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	w = request(t, h, http.MethodPost, "/api/v1/nodes/"+testNodeID+"/config-lint", string(body), "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	big, _ := json.Marshal(map[string]string{"config": strings.Repeat("#", MaxManagedConfigBytes+1)})
	w = request(t, h, http.MethodPost, "/api/v1/nodes/"+testNodeID+"/config-lint", string(big), testAdminToken)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	f.getNodeHook = func(context.Context, string) (Node, error) { return Node{}, ErrNotFound }
	w = request(t, h, http.MethodPost, "/api/v1/nodes/"+testNodeID+"/config-lint", string(body), testAdminToken)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestAdvancedEditorRevisionIsLintedAndCarriesFirewallPlan(t *testing.T) {
	f := &fakeStore{}
	h := handler(f)
	path := "/api/v1/nodes/" + testNodeID + "/config-revisions"
	broken := "global\n    daemon\nfrontend fe\n    bind :70000\n    default_backend missing\n"
	payload := func(cfg string, force bool) string {
		raw, err := json.Marshal(map[string]any{"config": cfg, "note": "manual", "metadata": map[string]any{"source": "advanced_editor"}, "force": force})
		require.NoError(t, err)
		return string(raw)
	}
	w := request(t, h, http.MethodPost, path, payload(broken, false), testAdminToken)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	var rejected struct {
		Error  map[string]string `json:"error"`
		Issues []LintIssue       `json:"issues"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rejected))
	assert.Equal(t, "config_lint_failed", rejected.Error["code"])
	assert.NotEmpty(t, rejected.Error["message"])
	require.Len(t, rejected.Issues, 2)
	assert.Empty(t, f.createdConfig.Config, "rejected revision must not be stored")

	w = request(t, h, http.MethodPost, path, payload(broken, true), testAdminToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	assert.Equal(t, "manual", f.createdConfig.Metadata["renderer"])
	assert.Equal(t, map[string]any{"errors": 2, "warnings": 0}, f.createdConfig.Metadata["lint"])
	assert.Equal(t, []int{}, f.createdConfig.Metadata["listener_tcp_ports"])

	generated, err := os.ReadFile("testdata/haproxy_render.golden.cfg")
	require.NoError(t, err)
	w = request(t, h, http.MethodPost, path, payload(string(generated), false), testAdminToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	assert.True(t, strings.HasPrefix(f.createdConfig.Config, "# Edited manually in NodeFlow advanced editor.\n# renderer:"))
	assert.NotContains(t, f.createdConfig.Config, "Do not edit")
	assert.Equal(t, []int{443, 8443}, f.createdConfig.Metadata["listener_tcp_ports"])
	assert.Equal(t, "advanced_editor", f.createdConfig.Metadata["source"])

	noLF := strings.TrimRight(string(generated), "\n")
	w = request(t, h, http.MethodPost, path, payload(noLF, false), testAdminToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	assert.True(t, strings.HasSuffix(f.createdConfig.Config, "\n"), "manual revision must end with LF")
	assert.Equal(t, map[string]any{"errors": 0, "warnings": 0}, f.createdConfig.Metadata["lint"])

	// Non-editor revisions keep the legacy contract (no lint, no force).
	w = request(t, h, http.MethodPost, path, `{"config":"not haproxy","metadata":{"source":"routes"}}`, testAdminToken)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	assert.Nil(t, f.createdConfig.Metadata["lint"])
	w = request(t, h, http.MethodPost, path, `{"config":"x","force":true}`, testAdminToken)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func int64p(v int64) *int64 { return &v }

// Lab regression: firewall mode apply + a manual desired revision used to
// fail every heartbeat with "desired firewall listener plan is incomplete".
func TestFirewallAssignmentAcceptsManualRevisionPlan(t *testing.T) {
	lint := LintHAProxyConfig(lintCleanConfig)
	ports, err := json.Marshal(lint.ListenerTCPPorts)
	require.NoError(t, err)
	row := firewallPlanRow{
		ActualRevision: int64p(4), ActualPorts: []byte(`[443]`), ActualComplete: true,
		DesiredRevision: int64p(5), DesiredPorts: ports, DesiredComplete: true,
	}
	assignment, err := resolveFirewallAssignment(testNodeID, "apply", true, row)
	require.NoError(t, err)
	assert.Equal(t, "apply", assignment.Mode)
	assert.True(t, assignment.Transition)
	assert.Equal(t, []int{443}, assignment.TCPPorts)
	assert.Equal(t, []int{443, 8443}, assignment.DesiredTCPPorts)
}

func TestFirewallAssignmentDegradesForLegacyManualRevision(t *testing.T) {
	row := firewallPlanRow{
		ActualRevision: int64p(4), ActualPorts: []byte(`[443]`), ActualComplete: true,
		DesiredRevision: int64p(5), DesiredComplete: false,
	}
	assignment, err := resolveFirewallAssignment(testNodeID, "apply", true, row)
	require.NoError(t, err, "heartbeat must not fail")
	assert.Equal(t, "observe", assignment.Mode, "no prune while the plan is unknown")
	assert.False(t, assignment.Transition)
	assert.Empty(t, assignment.DesiredTCPPorts)
	assert.Equal(t, []int{443}, assignment.TCPPorts)

	// Without a pending config the active plan still governs in apply mode.
	assignment, err = resolveFirewallAssignment(testNodeID, "apply", false, row)
	require.NoError(t, err)
	assert.Equal(t, "apply", assignment.Mode)
	assert.Equal(t, []int{443}, assignment.TCPPorts)

	// A legacy manual revision that is already active keeps observe.
	row = firewallPlanRow{ActualRevision: int64p(5), ActualComplete: false}
	assignment, err = resolveFirewallAssignment(testNodeID, "apply", false, row)
	require.NoError(t, err)
	assert.Equal(t, "observe", assignment.Mode)
	assert.False(t, assignment.ActivePlanComplete)
}
