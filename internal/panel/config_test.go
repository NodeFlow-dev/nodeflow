package panel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setRequiredPanelEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://nodeflow:test@postgres/nodeflow")
	t.Setenv("PANEL_ADMIN_TOKEN", "0123456789abcdef0123456789abcdef")
	for _, name := range []string{
		"PANEL_PUBLIC_URL", "PANEL_AGENT_PUBLIC_URL", "PANEL_AGENT_TLS_LISTEN_ADDR",
		"PANEL_AGENT_TLS_CERT_FILE", "PANEL_AGENT_TLS_KEY_FILE", "PANEL_AGENT_TLS_CLIENT_CA_FILE",
		"PANEL_AGENT_TLS_ISSUER_KEY_FILE", "PANEL_AGENT_TLS_SERVER_NAME", "PANEL_REQUIRE_AGENT_MTLS",
	} {
		t.Setenv(name, "")
	}
	// Unit tests that do not construct a TLS listener opt out explicitly.
	// Production configuration is fail-closed when this variable is omitted.
	t.Setenv("PANEL_REQUIRE_AGENT_MTLS", "false")
}

func TestLoadConfigDefaultsToRequiringAgentMTLS(t *testing.T) {
	setRequiredPanelEnvironment(t)
	t.Setenv("PANEL_REQUIRE_AGENT_MTLS", "")

	_, err := LoadConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PANEL_REQUIRE_AGENT_MTLS requires PANEL_AGENT_TLS_LISTEN_ADDR")
}

func TestLoadConfigRequiresCompleteMTLSBootstrapConfiguration(t *testing.T) {
	setRequiredPanelEnvironment(t)
	t.Setenv("PANEL_AGENT_PUBLIC_URL", "https://panel.test:8443")
	t.Setenv("PANEL_AGENT_TLS_LISTEN_ADDR", ":8443")
	t.Setenv("PANEL_AGENT_TLS_CERT_FILE", "/tls/server.crt")
	t.Setenv("PANEL_AGENT_TLS_KEY_FILE", "/tls/server.key")
	t.Setenv("PANEL_AGENT_TLS_CLIENT_CA_FILE", "/pki/ca.crt")
	t.Setenv("PANEL_AGENT_TLS_ISSUER_KEY_FILE", "/pki/ca.key")
	t.Setenv("PANEL_AGENT_TLS_SERVER_NAME", "panel.test")
	t.Setenv("PANEL_REQUIRE_AGENT_MTLS", "true")

	config, err := LoadConfig()
	require.NoError(t, err)
	assert.True(t, config.RequireAgentMTLS)
	assert.Equal(t, "https://panel.test:8443", config.AgentPublicURL)

	t.Setenv("PANEL_AGENT_PUBLIC_URL", "http://panel.test:8080")
	_, err = LoadConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "https")
}

func TestLoadConfigRejectsMTLSRequirementWithoutIssuer(t *testing.T) {
	setRequiredPanelEnvironment(t)
	t.Setenv("PANEL_AGENT_PUBLIC_URL", "https://panel.test:8443")
	t.Setenv("PANEL_AGENT_TLS_LISTEN_ADDR", ":8443")
	t.Setenv("PANEL_AGENT_TLS_CERT_FILE", "/tls/server.crt")
	t.Setenv("PANEL_AGENT_TLS_KEY_FILE", "/tls/server.key")
	t.Setenv("PANEL_AGENT_TLS_CLIENT_CA_FILE", "/pki/ca.crt")
	t.Setenv("PANEL_REQUIRE_AGENT_MTLS", "true")

	_, err := LoadConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ISSUER_KEY")
}

func TestLoadConfigRejectsExampleAdminToken(t *testing.T) {
	setRequiredPanelEnvironment(t)
	t.Setenv("PANEL_ADMIN_TOKEN", "replace-with-at-least-32-random-characters")

	_, err := LoadConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "placeholder")
}
