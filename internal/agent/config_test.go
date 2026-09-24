package agent

import (
	"testing"
	"time"
)

func TestConfigFromEnvHAProxyStatsDefaults(t *testing.T) {
	t.Setenv("NODE_AGENT_LISTEN", "")
	t.Setenv("NODE_AGENT_ALLOW_REMOTE_LISTEN", "")
	t.Setenv("NODE_AGENT_HAPROXY_STATS_SOCKET", "")
	t.Setenv("NODE_AGENT_HAPROXY_STATS_TIMEOUT", "")
	cfg := ConfigFromEnv()
	if cfg.ListenAddr != "127.0.0.1:4200" {
		t.Fatalf("listen=%q", cfg.ListenAddr)
	}
	if cfg.AllowRemoteListen {
		t.Fatal("remote listen must be disabled by default")
	}
	if err := cfg.ValidateListenAddress(); err != nil {
		t.Fatalf("default listen address rejected: %v", err)
	}
	if cfg.HAProxyStatsSocket != "/run/haproxy/admin.sock" {
		t.Fatalf("socket=%q", cfg.HAProxyStatsSocket)
	}
	if cfg.HAProxyStatsTimeout != 2*time.Second {
		t.Fatalf("timeout=%s", cfg.HAProxyStatsTimeout)
	}
}

func TestConfigValidateListenAddressRequiresExplicitRemoteOptIn(t *testing.T) {
	tests := []struct {
		name        string
		address     string
		allowRemote bool
		wantError   bool
	}{
		{name: "IPv4 loopback", address: "127.0.0.1:4200"},
		{name: "IPv6 loopback", address: "[::1]:4200"},
		{name: "wildcard rejected", address: ":4200", wantError: true},
		{name: "IPv4 wildcard rejected", address: "0.0.0.0:4200", wantError: true},
		{name: "remote IP rejected", address: "192.0.2.10:4200", wantError: true},
		{name: "hostname rejected", address: "node.example:4200", wantError: true},
		{name: "remote explicitly allowed", address: "192.0.2.10:4200", allowRemote: true},
		{name: "wildcard explicitly allowed", address: ":4200", allowRemote: true},
		{name: "invalid port", address: "127.0.0.1:70000", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := (Config{ListenAddr: test.address, AllowRemoteListen: test.allowRemote}).ValidateListenAddress()
			if test.wantError && err == nil {
				t.Fatal("expected validation error")
			}
			if !test.wantError && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

func TestConfigFromEnvRemoteListenRequiresLiteralTrue(t *testing.T) {
	t.Setenv("NODE_AGENT_ALLOW_REMOTE_LISTEN", "yes")
	if ConfigFromEnv().AllowRemoteListen {
		t.Fatal("non-literal opt-in must remain disabled")
	}
	t.Setenv("NODE_AGENT_ALLOW_REMOTE_LISTEN", "true")
	if !ConfigFromEnv().AllowRemoteListen {
		t.Fatal("literal true must enable remote listen")
	}
}

func TestConfigFromEnvSelfUpdateDefaultsOff(t *testing.T) {
	t.Setenv("NODE_AGENT_SELF_UPDATE_MODE", "")
	if mode := ConfigFromEnv().SelfUpdateMode; mode != UpdateModeOff {
		t.Fatalf("mode=%q", mode)
	}
	t.Setenv("NODE_AGENT_SELF_UPDATE_MODE", "verify-only")
	t.Setenv("NODE_AGENT_UPDATE_SEQUENCE", "12")
	cfg := ConfigFromEnv()
	if cfg.SelfUpdateMode != UpdateModeVerifyOnly || cfg.UpdateSequence != 12 {
		t.Fatalf("config=%+v", cfg)
	}
}

func TestConfigFromEnvHAProxyStatsOverrides(t *testing.T) {
	t.Setenv("NODE_AGENT_HAPROXY_STATS_SOCKET", "/run/custom/haproxy.sock")
	t.Setenv("NODE_AGENT_HAPROXY_STATS_TIMEOUT", "750ms")
	cfg := ConfigFromEnv()
	if cfg.HAProxyStatsSocket != "/run/custom/haproxy.sock" {
		t.Fatalf("socket=%q", cfg.HAProxyStatsSocket)
	}
	if cfg.HAProxyStatsTimeout != 750*time.Millisecond {
		t.Fatalf("timeout=%s", cfg.HAProxyStatsTimeout)
	}
}

func TestConfigFromEnvFirewallModeDefaultsToObserve(t *testing.T) {
	t.Setenv("NODE_AGENT_FIREWALL_MODE", "")
	if mode := ConfigFromEnv().FirewallMode; mode != FirewallModeObserve {
		t.Fatalf("mode=%q", mode)
	}
}

func TestConfigFromEnvFirewallModeAcceptsOnlyKnownValues(t *testing.T) {
	for _, mode := range []string{FirewallModeOff, FirewallModeObserve, FirewallModeApply} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("NODE_AGENT_FIREWALL_MODE", mode)
			if got := ConfigFromEnv().FirewallMode; got != mode {
				t.Fatalf("mode=%q want=%q", got, mode)
			}
		})
	}

	t.Run("invalid falls back safely", func(t *testing.T) {
		t.Setenv("NODE_AGENT_FIREWALL_MODE", "enable")
		if got := ConfigFromEnv().FirewallMode; got != FirewallModeObserve {
			t.Fatalf("mode=%q", got)
		}
	})
}

func TestConfigFromEnvCredentialRenewalDefaultsAndOverrides(t *testing.T) {
	t.Setenv("NODE_AGENT_CREDENTIAL_RENEWAL_MODE", "")
	t.Setenv("NODE_AGENT_CREDENTIAL_STATE_DIR", "")
	t.Setenv("NODE_AGENT_CREDENTIAL_RENEW_BEFORE", "")
	cfg := ConfigFromEnv()
	if cfg.CredentialMode != CredentialRenewalObserve {
		t.Fatalf("mode=%q", cfg.CredentialMode)
	}
	if cfg.CredentialStateDir != "/var/lib/nodeflow/credentials" {
		t.Fatalf("state dir=%q", cfg.CredentialStateDir)
	}
	if cfg.CredentialRenewBefore != 45*24*time.Hour {
		t.Fatalf("renew before=%s", cfg.CredentialRenewBefore)
	}

	t.Setenv("NODE_AGENT_CREDENTIAL_RENEWAL_MODE", "apply")
	t.Setenv("NODE_AGENT_CREDENTIAL_STATE_DIR", "/run/test-credentials")
	t.Setenv("NODE_AGENT_CREDENTIAL_RENEW_BEFORE", "720h")
	cfg = ConfigFromEnv()
	if cfg.CredentialMode != CredentialRenewalApply || cfg.CredentialStateDir != "/run/test-credentials" || cfg.CredentialRenewBefore != 30*24*time.Hour {
		t.Fatalf("config=%+v", cfg)
	}

	t.Setenv("NODE_AGENT_CREDENTIAL_RENEWAL_MODE", "unsafe")
	if mode := ConfigFromEnv().CredentialMode; mode != CredentialRenewalObserve {
		t.Fatalf("invalid mode must fail safe, got %q", mode)
	}
}
