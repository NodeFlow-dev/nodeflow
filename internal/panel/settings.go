package panel

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var accentColorPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

func defaultPanelSettings() PanelSettings {
	return PanelSettings{
		Theme:                    "rose",
		Accent:                   "#C27087",
		InactivityTimeoutMinutes: 30,
		MaxSessions:              5,
		AuditRetentionDays:       90,
	}
}

// strictPanelSettings is the fail-closed startup policy. It is deliberately
// stricter than the operator defaults so a temporary database/read failure
// cannot silently weaken an already configured session policy.
func strictPanelSettings() PanelSettings {
	settings := defaultPanelSettings()
	settings.InactivityTimeoutMinutes = 5
	settings.MaxSessions = 1
	return settings
}

func validatePanelSettings(settings PanelSettings) error {
	if settings.Theme != "dark" && settings.Theme != "green" && settings.Theme != "rose" &&
		settings.Theme != "cyan" && settings.Theme != "amber" && settings.Theme != "system" {
		return fmt.Errorf("theme must be dark, green, rose, cyan, amber or system")
	}
	if !accentColorPattern.MatchString(settings.Accent) {
		return fmt.Errorf("accent must be a #RRGGBB color")
	}
	if settings.InactivityTimeoutMinutes < 5 || settings.InactivityTimeoutMinutes > 1440 {
		return fmt.Errorf("session_timeout_minutes must be between 5 and 1440")
	}
	if settings.MaxSessions < 1 || settings.MaxSessions > 100 {
		return fmt.Errorf("max_sessions must be between 1 and 100")
	}
	if settings.AuditRetentionDays < 7 || settings.AuditRetentionDays > 3650 {
		return fmt.Errorf("audit_retention_days must be between 7 and 3650")
	}
	return nil
}

func configuredURLPort(rawURL string) int {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return 0
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return 0
	}
	if rawPort := u.Port(); rawPort != "" {
		port, parseErr := strconv.Atoi(rawPort)
		if parseErr == nil && port >= 1 && port <= 65535 {
			return port
		}
		return 0
	}
	switch scheme {
	case "http":
		return 80
	case "https":
		return 443
	default:
		return 0
	}
}

func configuredPort(address string) int {
	address = strings.TrimSpace(address)
	if address == "" {
		return 0
	}
	_, rawPort, err := net.SplitHostPort(address)
	if err != nil {
		if port, parseErr := strconv.Atoi(address); parseErr == nil && port >= 1 && port <= 65535 {
			return port
		}
		return 0
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 1 || port > 65535 {
		return 0
	}
	return port
}

func firstConfiguredPort(preferred, fallback int) int {
	if preferred != 0 {
		return preferred
	}
	return fallback
}
