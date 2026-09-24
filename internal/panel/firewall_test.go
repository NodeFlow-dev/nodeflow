package panel

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeFirewallMode(t *testing.T) {
	mode, ok := normalizeFirewallMode("unknown")
	assert.False(t, ok)
	assert.Equal(t, "observe", mode)
	for _, want := range []string{"off", "observe", "apply"} {
		mode, ok = normalizeFirewallMode(want)
		assert.True(t, ok)
		assert.Equal(t, want, mode)
	}
}

func TestInitialFirewallModeRequiresExplicitBootstrapOptIn(t *testing.T) {
	assert.Equal(t, "observe", initialFirewallMode(nil))
	assert.Equal(t, "observe", initialFirewallMode(map[string]any{"firewall_apply_allowed": false}))
	assert.Equal(t, "observe", initialFirewallMode(map[string]any{"firewall_apply_allowed": "true"}))
	assert.Equal(t, "apply", initialFirewallMode(map[string]any{"firewall_apply_allowed": true}))
}
