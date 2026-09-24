package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentHealthURLUsesConfiguredLoopbackListener(t *testing.T) {
	t.Setenv("NODE_UPDATER_HEALTH_URL", "")
	t.Setenv("NODE_AGENT_LISTEN", "127.0.0.1:4317")
	got, err := agentHealthURL()
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:4317/v1/health", got)
}

func TestAgentHealthURLAcceptsExplicitIPv6Loopback(t *testing.T) {
	t.Setenv("NODE_UPDATER_HEALTH_URL", "http://[::1]:4200/v1/health")
	got, err := agentHealthURL()
	require.NoError(t, err)
	assert.Equal(t, "http://[::1]:4200/v1/health", got)
}

func TestAgentHealthURLRejectsRemoteOrUnexpectedURL(t *testing.T) {
	for _, value := range []string{
		"https://127.0.0.1:4200/v1/health",
		"http://192.0.2.10:4200/v1/health",
		"http://127.0.0.1:4200/other",
		"http://127.0.0.1:4200/v1/health?redirect=http://example.com",
	} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("NODE_UPDATER_HEALTH_URL", value)
			_, err := agentHealthURL()
			assert.Error(t, err)
		})
	}
}

func TestUpdaterPublicKeyPrefersDedicatedEnvironment(t *testing.T) {
	t.Setenv("NODE_UPDATER_PUBLIC_KEY", "new-key")
	t.Setenv("NODE_AGENT_UPDATE_PUBLIC_KEY", "legacy-key")
	assert.Equal(t, "new-key", updaterPublicKey())
}

func TestUpdaterPublicKeyFallsBackForLegacyInstall(t *testing.T) {
	t.Setenv("NODE_UPDATER_PUBLIC_KEY", "")
	t.Setenv("NODE_AGENT_UPDATE_PUBLIC_KEY", "legacy-key")
	assert.Equal(t, "legacy-key", updaterPublicKey())
}
