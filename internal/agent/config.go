package agent

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr            string
	AllowRemoteListen     bool
	Token                 string
	ManagedConfig         string
	HAProxyBinary         string
	ServiceName           string
	HAProxyStatsSocket    string
	HAProxyStatsTimeout   time.Duration
	FirewallMode          string
	SelfUpdateMode        UpdateMode
	UpdateStagingDir      string
	UpdatePublicKey       string
	UpdateSequence        uint64
	UpdateStateFile       string
	UpdatePendingFile     string
	UpdateResultFile      string
	UpdateHelperService   string
	PanelURL              string
	PanelTLSCA            string
	PanelTLSCert          string
	PanelTLSKey           string
	PanelTLSServerName    string
	CredentialMode        CredentialRenewalMode
	CredentialStateDir    string
	CredentialRenewBefore time.Duration
	HeartbeatInterval     time.Duration
	ReconcileTimeout      time.Duration
}

func ConfigFromEnv() Config {
	stagingDirectory := env("NODE_AGENT_UPDATE_STAGING_DIR", "/var/lib/nodeflow/updates")
	return Config{
		ListenAddr:            env("NODE_AGENT_LISTEN", "127.0.0.1:4200"),
		AllowRemoteListen:     explicitTrueEnv("NODE_AGENT_ALLOW_REMOTE_LISTEN"),
		Token:                 strings.TrimSpace(os.Getenv("NODE_AGENT_TOKEN")),
		ManagedConfig:         env("NODE_AGENT_HAPROXY_CONFIG", "/etc/haproxy/haproxy.cfg"),
		HAProxyBinary:         env("NODE_AGENT_HAPROXY_BINARY", "haproxy"),
		ServiceName:           env("NODE_AGENT_HAPROXY_SERVICE", "haproxy.service"),
		HAProxyStatsSocket:    env("NODE_AGENT_HAPROXY_STATS_SOCKET", "/run/haproxy/admin.sock"),
		HAProxyStatsTimeout:   durationEnv("NODE_AGENT_HAPROXY_STATS_TIMEOUT", 2*time.Second),
		FirewallMode:          firewallModeEnv("NODE_AGENT_FIREWALL_MODE"),
		SelfUpdateMode:        updateModeEnv("NODE_AGENT_SELF_UPDATE_MODE"),
		UpdateStagingDir:      stagingDirectory,
		UpdatePublicKey:       strings.TrimSpace(os.Getenv("NODE_AGENT_UPDATE_PUBLIC_KEY")),
		UpdateSequence:        uint64Env("NODE_AGENT_UPDATE_SEQUENCE"),
		UpdateStateFile:       env("NODE_AGENT_UPDATE_STATE_FILE", "/var/lib/nodeflow-updater/state.json"),
		UpdatePendingFile:     env("NODE_AGENT_UPDATE_PENDING_FILE", stagingDirectory+"/pending.json"),
		UpdateResultFile:      env("NODE_AGENT_UPDATE_RESULT_FILE", stagingDirectory+"/result.json"),
		UpdateHelperService:   env("NODE_AGENT_UPDATE_HELPER_SERVICE", "nodeflow-node-updater.service"),
		PanelURL:              strings.TrimRight(strings.TrimSpace(os.Getenv("NODE_AGENT_PANEL_URL")), "/"),
		PanelTLSCA:            strings.TrimSpace(os.Getenv("NODE_AGENT_PANEL_TLS_CA")),
		PanelTLSCert:          strings.TrimSpace(os.Getenv("NODE_AGENT_PANEL_TLS_CERT")),
		PanelTLSKey:           strings.TrimSpace(os.Getenv("NODE_AGENT_PANEL_TLS_KEY")),
		PanelTLSServerName:    strings.TrimSpace(os.Getenv("NODE_AGENT_PANEL_TLS_SERVER_NAME")),
		CredentialMode:        credentialRenewalModeEnv("NODE_AGENT_CREDENTIAL_RENEWAL_MODE"),
		CredentialStateDir:    env("NODE_AGENT_CREDENTIAL_STATE_DIR", "/var/lib/nodeflow/credentials"),
		CredentialRenewBefore: durationEnv("NODE_AGENT_CREDENTIAL_RENEW_BEFORE", 45*24*time.Hour),
		HeartbeatInterval:     durationEnv("NODE_AGENT_HEARTBEAT_INTERVAL", 15*time.Second),
		ReconcileTimeout:      durationEnv("NODE_AGENT_RECONCILE_TIMEOUT", 45*time.Second),
	}
}

func credentialRenewalModeEnv(key string) CredentialRenewalMode {
	mode, err := NormalizeCredentialRenewalMode(os.Getenv(key))
	if err != nil {
		return CredentialRenewalObserve
	}
	return mode
}

// ValidateListenAddress keeps the privileged local management API on loopback.
// A non-loopback or wildcard bind requires an explicit operator opt-in.
func (c Config) ValidateListenAddress() error {
	address := strings.TrimSpace(c.ListenAddr)
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("must be a valid host:port address: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if c.AllowRemoteListen {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("must bind a loopback IP unless NODE_AGENT_ALLOW_REMOTE_LISTEN=true")
	}
	return nil
}

func explicitTrueEnv(key string) bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(key)), "true")
}

func updateModeEnv(key string) UpdateMode {
	switch UpdateMode(strings.TrimSpace(os.Getenv(key))) {
	case UpdateModeVerifyOnly:
		return UpdateModeVerifyOnly
	case UpdateModeApply:
		return UpdateModeApply
	default:
		return UpdateModeOff
	}
}

func uint64Env(key string) uint64 {
	value, err := strconv.ParseUint(strings.TrimSpace(os.Getenv(key)), 10, 64)
	if err != nil {
		return 0
	}
	return value
}

func firewallModeEnv(key string) string {
	mode, err := normalizeLocalFirewallMode(os.Getenv(key))
	if err != nil {
		return FirewallModeObserve
	}
	return mode
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
