package panel

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConfiguredURLPort(t *testing.T) {
	tests := map[string]int{
		"https://panel.example":       443,
		"http://panel.example":        80,
		"https://panel.example:8443":  8443,
		"http://127.0.0.1:8080/path":  8080,
		"":                            0,
		"not a URL":                   0,
		"ftp://panel.example:2121":    0,
		"https://panel.example:70000": 0,
	}
	for rawURL, expected := range tests {
		assert.Equal(t, expected, configuredURLPort(rawURL), rawURL)
	}
}

func TestPanelSettingsAccentRequiresFullHexColor(t *testing.T) {
	settings := defaultPanelSettings()
	assert.Equal(t, "rose", settings.Theme)
	assert.Equal(t, "#C27087", settings.Accent)
	assert.NoError(t, validatePanelSettings(settings))
	settings.Accent = "#aBc123"
	assert.NoError(t, validatePanelSettings(settings))
	settings.Accent = "emerald"
	assert.EqualError(t, validatePanelSettings(settings), "accent must be a #RRGGBB color")
}

func TestStrictPanelSettingsUseMinimumValidSessionPolicy(t *testing.T) {
	settings := strictPanelSettings()
	assert.NoError(t, validatePanelSettings(settings))
	assert.Equal(t, 5, settings.InactivityTimeoutMinutes)
	assert.Equal(t, 1, settings.MaxSessions)
}
