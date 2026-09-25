package bootstrap

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

type staticReleaseSource struct{}

func (staticReleaseSource) OpenBootstrapRelease(context.Context, string, string, string) (BootstrapRelease, error) {
	return BootstrapRelease{ID: "release", Version: "test", Sequence: 1, SHA256: strings.Repeat("0", 64), Content: io.NopCloser(strings.NewReader("agent"))}, nil
}

func TestRequestValidationDefaults(t *testing.T) {
	r := Request{Name: " node ", Address: "192.0.2.1", Username: "root", Password: "secret", HostKeySHA256: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}
	require.NoError(t, r.Validate())
	assert.Equal(t, 22, r.SSHPort)
	assert.Equal(t, 4200, r.AgentPort)
	assert.Equal(t, ssh.KeyAlgoED25519, r.HostKeyAlgorithm)
	assert.Equal(t, AuthModePassword, r.AuthMode)
	assert.Equal(t, SudoModeAuto, r.SudoMode)
	assert.Equal(t, "node", r.Name)
}

func TestRequestValidationSupportsPrivateKeysAndPassphrases(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	plainBlock, err := ssh.MarshalPrivateKey(private, "nodeflow-test")
	require.NoError(t, err)
	encryptedBlock, err := ssh.MarshalPrivateKeyWithPassphrase(private, "nodeflow-test", []byte("key-passphrase"))
	require.NoError(t, err)

	base := Request{
		Name:             "node",
		Address:          "192.0.2.1",
		Username:         "ubuntu",
		AuthMode:         AuthModePrivateKey,
		HostKeySHA256:    "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		SudoMode:         SudoModePasswordless,
		HostKeyAlgorithm: ssh.KeyAlgoED25519,
	}
	plain := base
	plain.PrivateKey = string(pem.EncodeToMemory(plainBlock))
	require.NoError(t, plain.Validate())

	encrypted := base
	encrypted.PrivateKey = string(pem.EncodeToMemory(encryptedBlock))
	encrypted.PrivateKeyPassphrase = "key-passphrase"
	require.NoError(t, encrypted.Validate())
	encrypted.PrivateKeyPassphrase = "wrong"
	assert.EqualError(t, encrypted.Validate(), "private_key is invalid or its passphrase is incorrect")
}

func TestRequestValidationRejectsAmbiguousAuthenticationAndSudo(t *testing.T) {
	fingerprint := "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	tests := []Request{
		{Name: "node", Address: "192.0.2.1", Username: "root", AuthMode: "unknown", Password: "secret", HostKeySHA256: fingerprint},
		{Name: "node", Address: "192.0.2.1", Username: "root", AuthMode: AuthModePassword, Password: "secret\ninjected", HostKeySHA256: fingerprint},
		{Name: "node", Address: "192.0.2.1", Username: "root", AuthMode: AuthModePrivateKey, Password: "ambiguous", PrivateKey: "invalid", HostKeySHA256: fingerprint},
		{Name: "node", Address: "192.0.2.1", Username: "root", AuthMode: AuthModePassword, Password: "secret", PrivateKeyPassphrase: "unused", HostKeySHA256: fingerprint},
		{Name: "node", Address: "192.0.2.1", Username: "ubuntu", AuthMode: AuthModePassword, Password: "secret", SudoMode: SudoModePasswordless, SudoPassword: "ambiguous", HostKeySHA256: fingerprint},
		{Name: "node", Address: "192.0.2.1", Username: "ubuntu", AuthMode: AuthModePassword, Password: "secret", SudoMode: SudoModePassword, SudoPassword: "sudo\ninjected", HostKeySHA256: fingerprint},
		{Name: "node", Address: "192.0.2.1", Username: "ubuntu", AuthMode: AuthModePrivateKey, PrivateKey: "invalid", SudoMode: SudoModePassword, HostKeySHA256: fingerprint},
	}
	for _, request := range tests {
		assert.Error(t, request.Validate())
	}
}

func TestResolvePrivilegeModes(t *testing.T) {
	root, err := resolvePrivilege(Request{SudoMode: SudoModeAuto}, true)
	require.NoError(t, err)
	assert.Equal(t, SudoModeRoot, root.Mode)

	password, err := resolvePrivilege(Request{AuthMode: AuthModePassword, Password: "ssh-and-sudo", SudoMode: SudoModeAuto}, false)
	require.NoError(t, err)
	assert.Equal(t, SudoModePassword, password.Mode)
	assert.Equal(t, "ssh-and-sudo", password.Password)

	passwordless, err := resolvePrivilege(Request{AuthMode: AuthModePrivateKey, SudoMode: SudoModeAuto}, false)
	require.NoError(t, err)
	assert.Equal(t, SudoModePasswordless, passwordless.Mode)

	separatePassword, err := resolvePrivilege(Request{AuthMode: AuthModePrivateKey, SudoMode: SudoModePassword, SudoPassword: "sudo-only"}, false)
	require.NoError(t, err)
	assert.Equal(t, "sudo-only", separatePassword.Password)

	_, err = resolvePrivilege(Request{SudoMode: SudoModeRoot}, false)
	assert.Error(t, err)
	_, err = resolvePrivilege(Request{SudoMode: SudoModePasswordless}, true)
	assert.Error(t, err)
}

func TestRequestClearSecrets(t *testing.T) {
	request := Request{Password: "ssh", PrivateKey: "key", PrivateKeyPassphrase: "key-pass", SudoPassword: "sudo", EnrollmentToken: "token"}
	request.ClearSecrets()
	assert.Empty(t, request.Password)
	assert.Empty(t, request.PrivateKey)
	assert.Empty(t, request.PrivateKeyPassphrase)
	assert.Empty(t, request.SudoPassword)
	assert.Empty(t, request.EnrollmentToken)
}

func TestBootstrapProgressContext(t *testing.T) {
	var stages []string
	ctx := WithProgress(context.Background(), func(stage string) {
		stages = append(stages, stage)
	})
	ReportProgress(ctx, "connect")
	ReportProgress(context.Background(), "ignored")
	assert.Equal(t, []string{"connect"}, stages)
}

func TestRequestValidationRejectsMissingFingerprintAndBadPorts(t *testing.T) {
	base := Request{Name: "node", Address: "192.0.2.1", Username: "root", Password: "secret"}
	assert.Error(t, base.Validate())
	base.HostKeySHA256, base.SSHPort = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", 70000
	assert.Error(t, base.Validate())
}

func TestPinnedHostKey(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	key, err := ssh.NewPublicKey(private.Public())
	require.NoError(t, err)
	want := ssh.FingerprintSHA256(key)
	assert.NoError(t, pinnedHostKey(want)("host", &net.TCPAddr{}, key))
	assert.Error(t, pinnedHostKey("SHA256:not-it")("host", &net.TCPAddr{}, key))
}

func TestDialSSHHonorsContextDuringHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	serverDone := make(chan struct{})
	defer close(serverDone)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		<-serverDone
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = dialSSH(ctx, listener.Addr().String(), &ssh.ClientConfig{
		User:            "nodeflow-test",
		Timeout:         5 * time.Second,
		HostKeyCallback: func(string, net.Addr, ssh.PublicKey) error { return nil },
	})
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(started), time.Second)
}

func TestInstallDoesNotLogServerControlledSSHError(t *testing.T) {
	const secret = "ssh-secret-that-must-not-be-logged"
	request := Request{
		Name: "node", Address: "192.0.2.1", Username: "root", Password: secret,
		HostKeySHA256: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	}
	require.NoError(t, request.Validate())

	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previousLogger)
	installer := NewSSHInstaller("https://panel.test:4200")
	installer.Releases = staticReleaseSource{}
	installer.Dial = func(context.Context, string, *ssh.ClientConfig) (*ssh.Client, error) {
		return nil, errors.New("server reflected " + secret)
	}

	assert.EqualError(t, installer.Install(context.Background(), request), "bootstrap failed at connect")
	assert.NotContains(t, logs.String(), secret)
}

func TestInstallScriptAptOnlyOnDebianFamily(t *testing.T) {
	ubuntu := installScript("/tmp/agent", 4200, "nfe_secret", "http://panel:8080", "ubuntu")
	assert.Contains(t, ubuntu, "nodeflow_package_installed()")
	assert.Contains(t, ubuntu, "noble|resolute) ;;")
	assert.Contains(t, ubuntu, "unsupported Ubuntu codename for HAProxy Performance 3.4")
	assert.Contains(t, ubuntu, "https://pks.haproxy.com/linux/community/RPM-GPG-KEY-HAProxy")
	assert.Contains(t, ubuntu, "https://www.haproxy.com/download/haproxy/performance/ubuntu/ha34 ${VERSION_CODENAME} main")
	assert.Contains(t, ubuntu, "apt-cache madison haproxy-awslc")
	assert.Contains(t, ubuntu, "apt-get download \"$nodeflow_haproxy_old_package=$nodeflow_haproxy_old_version\"")
	assert.Contains(t, ubuntu, "apt-get download \"haproxy-awslc=$nodeflow_haproxy_target_version\"")
	assert.Contains(t, ubuntu, `dpkg-deb -f "$nodeflow_haproxy_candidate_deb" Depends`)
	assert.Contains(t, ubuntu, `"$nodeflow_haproxy_candidate_depends" || exit 72`)
	assert.Contains(t, ubuntu, "apt-get satisfy -y -qq --download-only --no-install-recommends")
	assert.Contains(t, ubuntu, `-o Dir::Cache::archives="$nodeflow_haproxy_txn/candidate/dependencies/"`)
	assert.Contains(t, ubuntu, `-name '*.deb' -exec dpkg-deb -x`)
	assert.Contains(t, ubuntu, `-name '*.so*' -printf '%h\n' | sort -u | paste -sd:`)
	assert.Contains(t, ubuntu, `nodeflow_run_haproxy_candidate() {`)
	assert.Contains(t, ubuntu, `if [ -n "$nodeflow_haproxy_candidate_lib" ]; then`)
	assert.Contains(t, ubuntu, `nodeflow_haproxy_candidate_runtime=$(nodeflow_run_haproxy_candidate -v`)
	assert.Contains(t, ubuntu, `test -n "$nodeflow_haproxy_candidate_runtime" || exit 74`)
	assert.Contains(t, ubuntu, `nodeflow_run_haproxy_candidate -c -f /etc/haproxy/haproxy.cfg`)
	assert.Contains(t, ubuntu, "dpkg --compare-versions \"$nodeflow_haproxy_current_runtime\" ge \"$nodeflow_haproxy_candidate_runtime\"")
	assert.Contains(t, ubuntu, "haproxy-config-$(date +%Y%m%d%H%M%S)-$$.tar.gz")
	assert.Contains(t, ubuntu, "nodeflow_haproxy_conflict_remove=haproxy-")
	assert.Contains(t, ubuntu, `$nodeflow_haproxy_conflict_remove "haproxy-awslc=$nodeflow_haproxy_target_version"`)
	assert.Contains(t, ubuntu, "nodeflow_rollback_haproxy()")
	assert.Contains(t, ubuntu, `if [ -z "${NODEFLOW_REINSTALL_BACKUP:-}" ]; then`)
	assert.Contains(t, ubuntu, `if [ "$nodeflow_haproxy_old_package" = libssl-awslc ] && [ ! -f "$nodeflow_haproxy_txn/had-haproxy-awslc" ]; then`)
	assert.Contains(t, ubuntu, `nodeflow_haproxy_remove_packages="$nodeflow_haproxy_remove_packages libssl-awslc"`)
	assert.Contains(t, ubuntu, "apt-get remove -y -qq $nodeflow_haproxy_remove_packages")
	assert.Contains(t, ubuntu, "apt-get install -y -qq --allow-downgrades")
	assert.Contains(t, ubuntu, `cp -a "$nodeflow_haproxy_txn/repository.list" "$nodeflow_haproxy_repo"`)
	assert.NotContains(t, ubuntu, "ppa:vbernat")
	assert.NotContains(t, ubuntu, `apt-get install -y -qq "haproxy=$nodeflow_haproxy_target_version"`)
	assert.Contains(t, ubuntu, "haproxy -c -f /etc/haproxy/haproxy.cfg")
	assert.Contains(t, ubuntu, "apt-get install -y -qq --reinstall -o Dpkg::Options::=--force-confold -o Dpkg::Options::=--force-confmiss ufw")
	assert.Contains(t, ubuntu, "test -f /etc/default/ufw")
	assert.Contains(t, ubuntu, "ufw status >/dev/null")
	dependencyDownload := strings.Index(ubuntu, "apt-get satisfy -y -qq --download-only --no-install-recommends")
	candidateRun := strings.Index(ubuntu, `nodeflow_haproxy_candidate_runtime=$(nodeflow_run_haproxy_candidate -v`)
	repositoryInstall := strings.Index(ubuntu, `mv -f "$nodeflow_haproxy_repo.new" "$nodeflow_haproxy_repo"`)
	rollbackPackageDownload := strings.Index(ubuntu, `apt-get download "$nodeflow_haproxy_old_package=$nodeflow_haproxy_old_version"`)
	require.NotEqual(t, -1, dependencyDownload)
	require.NotEqual(t, -1, candidateRun)
	require.NotEqual(t, -1, repositoryInstall)
	require.NotEqual(t, -1, rollbackPackageDownload)
	assert.Less(t, dependencyDownload, candidateRun)
	assert.Less(t, repositoryInstall, rollbackPackageDownload, "the target repository must be available before downloading orphaned rollback packages")
	assert.Contains(t, ubuntu, "NODE_AGENT_TOKEN=nfe_secret")
	assert.Contains(t, ubuntu, "NODE_AGENT_PANEL_URL=http://panel:8080")
	assert.Contains(t, ubuntu, "NODE_AGENT_LISTEN=127.0.0.1:4200")
	assert.Contains(t, ubuntu, "ProtectSystem=strict")
	assert.Contains(t, ubuntu, "ReadWritePaths=/etc/haproxy /var/lib/nodeflow/updates")
	assert.NotContains(t, ubuntu, "ReadWritePaths=/etc/haproxy /var/lib/nodeflow/updates -/etc/ufw")
	assert.Contains(t, ubuntu, "CapabilityBoundingSet=\n")
	assert.Contains(t, ubuntu, "UMask=0027")
	assert.Contains(t, ubuntu, "Nice=10")
	assert.Contains(t, ubuntu, "CPUWeight=20")
	assert.Contains(t, ubuntu, "IOWeight=20")
	assert.Contains(t, ubuntu, "TasksMax=64")
	assert.Contains(t, ubuntu, "mv -f /usr/local/bin/nodeflow-node-agent.new")
	assert.Contains(t, ubuntu, "systemctl restart nodeflow-node-agent.service")
	assert.Contains(t, ubuntu, "systemctl is-active --quiet nodeflow-node-agent.service")
	assert.NotContains(t, ubuntu, "ufw --force enable")
	assert.NotContains(t, ubuntu, "comment nodeflow-ssh")

	debian := installScript("/tmp/agent", 4200, "nfe_secret", "http://panel:8080", "debian")
	assert.Contains(t, debian, "for nodeflow_package in haproxy ufw")
	assert.Contains(t, debian, "apt-get install -y -qq --no-upgrade $nodeflow_missing_packages")
	assert.Contains(t, debian, "Dpkg::Options::=--force-confmiss ufw")
	alpine := installScript("/tmp/agent", 4200, "nfe_secret", "http://panel:8080", "alpine")
	assert.False(t, strings.Contains(alpine, "apt-get"))
}

func TestInstallDiagnosticForExitIsAllowListed(t *testing.T) {
	assert.Equal(t, "haproxy_release_unavailable", installDiagnosticForExit(71))
	assert.Equal(t, "haproxy_dependency_prepare_failed", installDiagnosticForExit(72))
	assert.Equal(t, "haproxy_candidate_libraries_missing", installDiagnosticForExit(73))
	assert.Equal(t, "haproxy_candidate_runtime_failed", installDiagnosticForExit(74))
	assert.Equal(t, "haproxy_config_validation_failed", installDiagnosticForExit(75))
	assert.Equal(t, "remote_install_failed", installDiagnosticForExit(1))
}

func TestInstallScriptPreparesSSHBeforeExplicitUFWActivation(t *testing.T) {
	script := installScriptWithOptions("/tmp/agent", "", 4200, "nfe_secret", "https://panel.test:4200", "ubuntu", NodeTLSIdentity{}, "", true, 2222)
	sshAllow := `ufw allow "$nodeflow_ssh_port/tcp" comment nodeflow-ssh`
	enable := "ufw --force enable"

	assert.Contains(t, script, "nodeflow_ssh_port=${NODEFLOW_SSH_PORT:-2222}")
	assert.Contains(t, script, sshAllow)
	assert.Contains(t, script, enable)
	assert.Less(t, strings.Index(script, sshAllow), strings.Index(script, enable))
	assert.Less(t, strings.Index(script, sshAllow), strings.Index(script, "systemctl restart nodeflow-node-agent.service"))
	assert.NotContains(t, script, "ufw allow \"$nodeflow_ssh_port/tcp\" comment nodeflow\n")
	assert.Contains(t, script, "NODE_AGENT_FIREWALL_MODE=apply")
	assert.Contains(t, script, "CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW")
	assert.Contains(t, script, "RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK")
	assert.Contains(t, script, "ReadWritePaths=/etc/haproxy /var/lib/nodeflow/updates /var/lib/nodeflow/credentials -/etc/sysctl.d -/proc/sys/fs/pipe-max-size -/proc/sys/fs/pipe-user-pages-soft -/etc/ufw")
	assert.Contains(t, script, "NODE_AGENT_CREDENTIAL_RENEWAL_MODE=apply")
	assert.Contains(t, script, "NODE_AGENT_CREDENTIAL_STATE_DIR=/var/lib/nodeflow/credentials")
}

func TestPrepareNodeFirewallDryRunIsNonMutating(t *testing.T) {
	command := exec.Command("../../scripts/prepare-node-firewall.sh", "--dry-run", "--ssh-port", "2222")
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	text := string(output)
	assert.Contains(t, text, "DRY-RUN: ufw allow 2222/tcp comment nodeflow-ssh")
	assert.Contains(t, text, "listener rules remain owned by Node Agent with exact comment nodeflow")
}

func TestGeneratedInstallScriptsHaveValidShellSyntax(t *testing.T) {
	for _, updatePublicKey := range []string{"", "ZmFrZS1wdWJsaWMta2V5"} {
		for _, allowFirewallApply := range []bool{false, true} {
			updaterPath := ""
			if updatePublicKey != "" {
				updaterPath = "/tmp/updater"
			}
			script := installScriptWithOptions("/tmp/agent", updaterPath, 4200, "nfe_secret", "https://panel.test:4200", "ubuntu", NodeTLSIdentity{}, updatePublicKey, allowFirewallApply, 2222)
			command := exec.Command("sh", "-n")
			command.Stdin = strings.NewReader(script)
			output, err := command.CombinedOutput()
			require.NoError(t, err, "update_key=%t allow_firewall_apply=%v: %s", updatePublicKey != "", allowFirewallApply, output)
		}
	}
	backupPath, err := NewReinstallBackupPath()
	require.NoError(t, err)
	wrapped := credentialSafeReinstallScript(
		installScriptWithOptions("/tmp/agent", "/tmp/updater", 4200, "nfe_secret", "https://panel.test:4200", "ubuntu", NodeTLSIdentity{}, "ZmFrZS1wdWJsaWMta2V5", true, 2222),
		backupPath,
	)
	command := exec.Command("sh", "-n")
	command.Stdin = strings.NewReader(wrapped)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
}

func TestCredentialRotationRebuildsCanonicalStateAndCanRestoreIt(t *testing.T) {
	script := credentialRotationScript("nfe_next", "https://panel.test:4200", NodeTLSIdentity{
		CACertificatePEM: []byte("CA-CERT\n"), CertificatePEM: []byte("NODE-CERT\n"), PrivateKeyPEM: []byte("NODE-KEY\n"),
	})
	assert.Contains(t, script, `cp -a /var/lib/nodeflow/credentials "$backup/credentials"`)
	assert.Contains(t, script, "systemctl stop nodeflow-node-agent.service")
	assert.Contains(t, script, "NODE_AGENT_CREDENTIAL_RENEWAL_MODE=apply")
	assert.Contains(t, script, "NODE_AGENT_CREDENTIAL_STATE_DIR=/var/lib/nodeflow/credentials")
	assert.Contains(t, script, "rm -rf /var/lib/nodeflow/credentials")
	assert.Contains(t, script, "install -d -m 0700 /var/lib/nodeflow/credentials")
	assert.Contains(t, script, `if [ -f "$backup/had-credentials" ]; then cp -a "$backup/credentials" /var/lib/nodeflow/credentials; fi`)
	command := exec.Command("sh", "-n")
	command.Stdin = strings.NewReader(script)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))

	rollback := credentialRollbackScript()
	assert.Contains(t, rollback, "systemctl stop nodeflow-node-agent.service")
	assert.Contains(t, rollback, "rm -rf /var/lib/nodeflow/credentials")
	assert.Contains(t, rollback, `cp -a "$backup/credentials" /var/lib/nodeflow/credentials`)
	command = exec.Command("sh", "-n")
	command.Stdin = strings.NewReader(rollback)
	output, err = command.CombinedOutput()
	require.NoError(t, err, string(output))
}

func TestReinstallWrapsFullInstallerWithCredentialRollback(t *testing.T) {
	backupPath, err := NewReinstallBackupPath()
	require.NoError(t, err)
	wrapped := credentialSafeReinstallScript("printf '%s\\n' reinstall-body", backupPath)
	assert.Contains(t, wrapped, "backup="+backupPath)
	assert.Contains(t, wrapped, `cp -a /usr/local/bin/nodeflow-node-agent "$backup/node-agent"`)
	assert.Contains(t, wrapped, `cp -a /usr/local/libexec/nodeflow-node-updater "$backup/node-updater"`)
	assert.Contains(t, wrapped, `cp -a /etc/nodeflow/node-agent.env "$backup/node-agent.env"`)
	assert.Contains(t, wrapped, `cp -a /etc/nodeflow/node-updater.env "$backup/node-updater.env"`)
	assert.Contains(t, wrapped, `cp -a /var/lib/nodeflow/credentials "$backup/credentials"`)
	assert.Contains(t, wrapped, `cp -a /var/lib/nodeflow-updater "$backup/updater-state"`)
	assert.Contains(t, wrapped, `cp -a /etc/systemd/system/nodeflow-node-agent.service "$backup/node-agent.service"`)
	assert.Contains(t, wrapped, `cp -a /etc/systemd/system/nodeflow-node-updater.service "$backup/node-updater.service"`)
	assert.Contains(t, wrapped, `cp -a /etc/systemd/system/nodeflow-node-updater.path "$backup/node-updater.path"`)
	assert.Contains(t, wrapped, `tar -C / -czf "$backup/haproxy-config.tar.gz" etc/haproxy`)
	assert.Contains(t, wrapped, `cp -a /etc/apt/sources.list.d/haproxy-performance-ha34.list "$backup/haproxy-performance-ha34.list"`)
	assert.Contains(t, wrapped, `cp -a /usr/share/keyrings/HAPROXY-key-community.asc "$backup/HAPROXY-key-community.asc"`)
	assert.Contains(t, wrapped, `for package in haproxy haproxy-awslc libssl-awslc ufw; do`)
	assert.Contains(t, wrapped, `apt-get download "$package=$version"`)
	assert.Contains(t, wrapped, `tar -C / -czf "$backup/ufw-config.tar.gz" etc/ufw`)
	assert.Contains(t, wrapped, `ufw status | grep -qx 'Status: active'`)
	assert.Contains(t, wrapped, `cat > "$backup/restore-infrastructure.sh"`)
	assert.Contains(t, wrapped, `if [ "$package" = libssl-awslc ] && [ ! -f "$backup/had-haproxy-awslc-package" ]; then`)
	assert.Contains(t, wrapped, `haproxy_remove_packages="$haproxy_remove_packages libssl-awslc"`)
	assert.Contains(t, wrapped, `apt-get remove -y -qq $haproxy_remove_packages`)
	assert.Contains(t, wrapped, `apt-get install -y -qq --allow-downgrades -o Dpkg::Options::=--force-confold $haproxy_debs`)
	assert.Contains(t, wrapped, `apt-get remove -y -qq ufw`)
	assert.Contains(t, wrapped, `apt-get purge -y -qq ufw`)
	assert.Contains(t, wrapped, `Dpkg::Options::=--force-confmiss`)
	assert.Contains(t, wrapped, `ufw --force disable`)
	assert.Contains(t, wrapped, `ufw --force enable`)
	assert.Contains(t, wrapped, `: > "$backup/ready"`)
	assert.Contains(t, wrapped, `trap cleanup EXIT HUP INT TERM`)
	assert.Contains(t, wrapped, `if [ "$committed" -ne 1 ]; then restore_installation; fi`)
	assert.Contains(t, wrapped, `systemctl stop nodeflow-node-agent.service >/dev/null 2>&1 || true`)
	assert.Contains(t, wrapped, `: > "$backup/legacy-agent-was-active"`)
	assert.Contains(t, wrapped, `: > "$backup/legacy-updater-path-was-enabled"`)
	assert.Contains(t, wrapped, `systemctl stop bridge-control-node-agent.service >/dev/null 2>&1 || true`)
	assert.Contains(t, wrapped, `systemctl restart bridge-control-node-agent.service`)
	assert.Contains(t, wrapped, `rm -rf /var/lib/nodeflow/credentials`)
	assert.Contains(t, wrapped, `install -d -m 0700 /var/lib/nodeflow/credentials`)
	assert.Less(t, strings.Index(wrapped, `cp -a /var/lib/nodeflow/credentials "$backup/credentials"`), strings.LastIndex(wrapped, `rm -rf /var/lib/nodeflow/credentials`))
	assert.Less(t, strings.LastIndex(wrapped, `rm -rf /var/lib/nodeflow/credentials`), strings.Index(wrapped, "printf '%s\\n' reinstall-body"))
	assert.Contains(t, wrapped, `mv -f /usr/local/bin/nodeflow-node-agent.rollback /usr/local/bin/nodeflow-node-agent`)
	assert.Contains(t, wrapped, `mv -f /usr/local/libexec/nodeflow-node-updater.rollback /usr/local/libexec/nodeflow-node-updater`)
	assert.Contains(t, wrapped, `systemctl daemon-reload`)
	assert.Contains(t, wrapped, "printf '%s\\n' reinstall-body")
	assert.Contains(t, wrapped, "committed=1")
	command := exec.Command("sh", "-n")
	command.Stdin = strings.NewReader(wrapped)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))

	rollback := credentialReinstallRollbackScript(backupPath)
	assert.Contains(t, rollback, "backup="+backupPath)
	assert.Contains(t, rollback, `if [ ! -d "$backup" ]; then`)
	assert.Contains(t, rollback, `if [ ! -f "$backup/ready" ]; then`)
	assert.Contains(t, rollback, `cp -a "$backup/node-agent" /usr/local/bin/nodeflow-node-agent.rollback`)
	assert.Contains(t, rollback, `cp -a "$backup/node-updater" /usr/local/libexec/nodeflow-node-updater.rollback`)
	assert.Contains(t, rollback, `install -m 0600 "$backup/node-agent.env" /etc/nodeflow/node-agent.env`)
	assert.Contains(t, rollback, `cp -a "$backup/node-agent.service" /etc/systemd/system/nodeflow-node-agent.service`)
	assert.Contains(t, rollback, `cp -a "$backup/updater-state" /var/lib/nodeflow-updater`)
	assert.Contains(t, rollback, `install -m 0600 "$backup/node-updater.env" /etc/nodeflow/node-updater.env`)
	assert.Contains(t, rollback, `rm -rf /var/lib/nodeflow/credentials`)
	assert.Contains(t, rollback, `NODEFLOW_REINSTALL_BACKUP="$backup" "$backup/restore-infrastructure.sh"`)
	assert.Contains(t, rollback, `if ! NODEFLOW_REINSTALL_BACKUP="$backup" "$backup/restore-infrastructure.sh"; then rollback_failed=1; fi`)
	assert.Contains(t, rollback, `rollback was incomplete; backup retained at $backup`)
	assert.Contains(t, rollback, `systemctl restart bridge-control-node-agent.service || rollback_failed=1`)
	assert.Contains(t, rollback, `rm -rf "$backup"`)
	assert.Less(t, strings.Index(rollback, `NODEFLOW_REINSTALL_BACKUP="$backup" "$backup/restore-infrastructure.sh"`), strings.LastIndex(rollback, `rm -rf "$backup"`))
	command = exec.Command("sh", "-n")
	command.Stdin = strings.NewReader(rollback)
	output, err = command.CombinedOutput()
	require.NoError(t, err, string(output))

	finalize := credentialReinstallFinalizeScript(backupPath)
	assert.Contains(t, finalize, `systemctl stop bridge-control-node-agent.service`)
	assert.Contains(t, finalize, `rm -f /usr/local/bin/bridge-control-node-agent`)
	assert.Contains(t, finalize, `rm -rf /etc/bridge-control`)
	assert.Contains(t, finalize, `rm -rf -- "$backup"`)
	command = exec.Command("sh", "-n")
	command.Stdin = strings.NewReader(finalize)
	output, err = command.CombinedOutput()
	require.NoError(t, err, string(output))
}

func TestExplicitReinstallRollbackRestartsAgentAfterInfrastructureFailure(t *testing.T) {
	root := t.TempDir()
	backupPath := filepath.Join(root, "backup")
	require.NoError(t, os.MkdirAll(backupPath, 0o700))
	for _, marker := range []string{"ready", "had-node-agent-env", "was-active", "was-enabled"} {
		require.NoError(t, os.WriteFile(filepath.Join(backupPath, marker), nil, 0o600))
	}
	require.NoError(t, os.WriteFile(filepath.Join(backupPath, "node-agent.env"), []byte("OLD=1\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(backupPath, "restore-infrastructure.sh"), []byte("#!/bin/sh\nexit 1\n"), 0o700))

	liveRoot := filepath.Join(root, "live")
	for _, dir := range []string{
		filepath.Join(liveRoot, "usr/local/bin"),
		filepath.Join(liveRoot, "usr/local/libexec"),
		filepath.Join(liveRoot, "etc/nodeflow"),
		filepath.Join(liveRoot, "etc/systemd/system"),
		filepath.Join(liveRoot, "var/lib/nodeflow"),
		filepath.Join(liveRoot, "var/lib/nodeflow-updater"),
	} {
		require.NoError(t, os.MkdirAll(dir, 0o700))
	}
	fakeBin := filepath.Join(root, "bin")
	require.NoError(t, os.MkdirAll(fakeBin, 0o700))
	systemctlLog := filepath.Join(root, "systemctl.log")
	require.NoError(t, os.WriteFile(filepath.Join(fakeBin, "systemctl"), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$SYSTEMCTL_LOG\"\nexit 0\n"), 0o700))

	rollback := credentialReinstallRollbackScript(backupPath)
	rollback = strings.ReplaceAll(rollback, "/usr/local/bin", filepath.Join(liveRoot, "usr/local/bin"))
	rollback = strings.ReplaceAll(rollback, "/usr/local/libexec", filepath.Join(liveRoot, "usr/local/libexec"))
	rollback = strings.ReplaceAll(rollback, "/etc/nodeflow", filepath.Join(liveRoot, "etc/nodeflow"))
	rollback = strings.ReplaceAll(rollback, "/etc/systemd/system", filepath.Join(liveRoot, "etc/systemd/system"))
	rollback = strings.ReplaceAll(rollback, "/var/lib/nodeflow", filepath.Join(liveRoot, "var/lib/nodeflow"))
	rollback = strings.ReplaceAll(rollback, "sleep 2", ":")
	command := exec.Command("sh", "-c", rollback)
	command.Env = append(os.Environ(), "PATH="+fakeBin+":/usr/bin:/bin", "SYSTEMCTL_LOG="+systemctlLog)
	output, err := command.CombinedOutput()
	require.Error(t, err, string(output))
	assert.Contains(t, string(output), "rollback was incomplete")
	logContents, readErr := os.ReadFile(systemctlLog)
	require.NoError(t, readErr)
	assert.Contains(t, string(logContents), "restart nodeflow-node-agent.service")
	assert.DirExists(t, backupPath, "failed infrastructure restore must retain the retryable backup")
}

func TestReinstallBackupIsMarkedReadyOnlyAfterCompleteSnapshot(t *testing.T) {
	backupPath, err := NewReinstallBackupPath()
	require.NoError(t, err)
	wrapped := credentialSafeReinstallScript("true", backupPath)
	ready := strings.Index(wrapped, `: > "$backup/ready"`)
	require.NotEqual(t, -1, ready)
	for _, snapshotStep := range []string{
		`cp -a /usr/local/bin/nodeflow-node-agent "$backup/node-agent"`,
		`cp -a /usr/local/libexec/nodeflow-node-updater "$backup/node-updater"`,
		`cp -a /etc/nodeflow/node-agent.env "$backup/node-agent.env"`,
		`cp -a /etc/nodeflow/node-updater.env "$backup/node-updater.env"`,
		`cp -a /etc/nodeflow/tls "$backup/tls"`,
		`cp -a /var/lib/nodeflow/credentials "$backup/credentials"`,
		`cp -a /var/lib/nodeflow-updater "$backup/updater-state"`,
		`cp -a /etc/systemd/system/nodeflow-node-agent.service "$backup/node-agent.service"`,
		`cp -a /etc/systemd/system/nodeflow-node-updater.service "$backup/node-updater.service"`,
		`cp -a /etc/systemd/system/nodeflow-node-updater.path "$backup/node-updater.path"`,
		`tar -C / -czf "$backup/haproxy-config.tar.gz" etc/haproxy`,
		`apt-get download "$package=$version"`,
		`tar -C / -czf "$backup/ufw-config.tar.gz" etc/ufw`,
		`cat > "$backup/restore-infrastructure.sh"`,
		`: > "$backup/was-active"`,
	} {
		position := strings.Index(wrapped, snapshotStep)
		require.NotEqual(t, -1, position, snapshotStep)
		assert.Less(t, position, ready, snapshotStep)
	}
	assert.Less(t, ready, strings.Index(wrapped, "trap cleanup EXIT HUP INT TERM"))
}

func TestReinstallRollbackDiscardsPartialBackupWithoutMutatingLiveState(t *testing.T) {
	root := t.TempDir()
	backupPath := filepath.Join(root, "partial-backup")
	require.NoError(t, os.MkdirAll(backupPath, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(backupPath, "node-agent.env"), []byte("partial"), 0o600))

	liveRoot := filepath.Join(root, "live")
	for path, contents := range map[string]string{
		filepath.Join(liveRoot, "etc/nodeflow/node-agent.env"):        "live-agent",
		filepath.Join(liveRoot, "etc/nodeflow/node-updater.env"):      "live-updater",
		filepath.Join(liveRoot, "etc/nodeflow/tls/node.crt"):          "live-cert",
		filepath.Join(liveRoot, "var/lib/nodeflow/credentials/state"): "live-credentials",
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	}

	fakeBin := filepath.Join(root, "bin")
	require.NoError(t, os.MkdirAll(fakeBin, 0o700))
	systemctlLog := filepath.Join(root, "systemctl-called")
	require.NoError(t, os.WriteFile(filepath.Join(fakeBin, "systemctl"), []byte("#!/bin/sh\n: > \"$SYSTEMCTL_LOG\"\nexit 97\n"), 0o700))

	rollback := credentialReinstallRollbackScript(backupPath)
	rollback = strings.ReplaceAll(rollback, "/etc/nodeflow", filepath.Join(liveRoot, "etc/nodeflow"))
	rollback = strings.ReplaceAll(rollback, "/var/lib/nodeflow", filepath.Join(liveRoot, "var/lib/nodeflow"))
	command := exec.Command("sh", "-c", rollback)
	command.Env = append(os.Environ(), "PATH="+fakeBin+":/usr/bin:/bin", "SYSTEMCTL_LOG="+systemctlLog)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	assert.NoFileExists(t, systemctlLog, "partial backup must be rejected before stopping the Agent")
	assert.NoDirExists(t, backupPath)
	for path, contents := range map[string]string{
		filepath.Join(liveRoot, "etc/nodeflow/node-agent.env"):        "live-agent",
		filepath.Join(liveRoot, "etc/nodeflow/node-updater.env"):      "live-updater",
		filepath.Join(liveRoot, "etc/nodeflow/tls/node.crt"):          "live-cert",
		filepath.Join(liveRoot, "var/lib/nodeflow/credentials/state"): "live-credentials",
	} {
		actual, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		assert.Equal(t, contents, string(actual), path)
	}
}

func TestReinstallRequestSelectsTransactionalFullInstall(t *testing.T) {
	backupPath, err := NewReinstallBackupPath()
	require.NoError(t, err)
	request := Request{
		Name: "node", Address: "192.0.2.1", Username: "root", Password: "secret",
		HostKeySHA256: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Reinstall: true, ReinstallBackupPath: backupPath,
	}
	require.NoError(t, request.Validate())
	installer := NewSSHInstaller("https://panel.test:4200")
	installer.Releases = staticReleaseSource{}
	installer.Dial = func(context.Context, string, *ssh.ClientConfig) (*ssh.Client, error) {
		return nil, errors.New("stop before remote install")
	}

	assert.EqualError(t, installer.Install(context.Background(), request), "bootstrap failed at connect")
	assert.True(t, request.Reinstall)
}

func TestReinstallBackupPathIsUniqueAndShellSafe(t *testing.T) {
	first, err := NewReinstallBackupPath()
	require.NoError(t, err)
	second, err := NewReinstallBackupPath()
	require.NoError(t, err)
	assert.NotEqual(t, first, second)
	assert.True(t, validReinstallBackupPath(first))
	assert.False(t, validReinstallBackupPath("/tmp/operator-controlled"))
	assert.False(t, validReinstallBackupPath(reinstallCredentialBackupPrefix+"../../unsafe"))
}

func TestManualInstallerGrantsFirewallAccessOnlyForApplyMode(t *testing.T) {
	contents, err := os.ReadFile("../../scripts/install-node.sh")
	require.NoError(t, err)
	script := string(contents)
	assert.Contains(t, script, `firewall_script=$(firewall_helper)`)
	assert.Contains(t, script, `"${firewall_script}" --apply`)
	assert.Contains(t, script, "CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW")
	assert.Contains(t, script, "ReadWritePaths=/etc/haproxy /var/lib/nodeflow/updates /var/lib/nodeflow/credentials -/etc/sysctl.d -/proc/sys/fs/pipe-max-size -/proc/sys/fs/pipe-user-pages-soft -/etc/ufw")
}

func TestInstallScriptWritesMTLSIdentityAndEnvironment(t *testing.T) {
	script := installScriptWithIdentity("/tmp/agent", 4200, "nfe_secret", "https://panel.test:8443", "ubuntu", NodeTLSIdentity{
		CACertificatePEM: []byte("CA-CERT\n"),
		CertificatePEM:   []byte("NODE-CERT\n"),
		PrivateKeyPEM:    []byte("NODE-KEY\n"),
		ServerName:       "panel.test",
	})
	assert.Contains(t, script, "NODE_AGENT_PANEL_URL=https://panel.test:8443")
	assert.Contains(t, script, "NODE_AGENT_PANEL_TLS_CA=/etc/nodeflow/tls/ca.crt")
	assert.Contains(t, script, "NODE_AGENT_PANEL_TLS_CERT=/etc/nodeflow/tls/node.crt")
	assert.Contains(t, script, "NODE_AGENT_PANEL_TLS_KEY=/etc/nodeflow/tls/node.key")
	assert.Contains(t, script, "NODE_AGENT_PANEL_TLS_SERVER_NAME=panel.test")
	assert.Contains(t, script, "chmod 0600 /etc/nodeflow/tls/node.key")
	assert.Contains(t, script, "NODE-KEY")
}

func TestInstallScriptEnablesSignedUpdaterOnlyWithReleaseKey(t *testing.T) {
	script := installScriptWithOptions("/tmp/agent", "/tmp/updater", 4200, "nfe_secret", "https://panel.test:4200", "ubuntu", NodeTLSIdentity{}, "ZmFrZS1wdWJsaWMta2V5", true)
	assert.Contains(t, script, "NODE_AGENT_SELF_UPDATE_MODE=apply")
	assert.Contains(t, script, "NODE_AGENT_UPDATE_PUBLIC_KEY=ZmFrZS1wdWJsaWMta2V5")
	assert.Contains(t, script, "NODE_UPDATER_PUBLIC_KEY=ZmFrZS1wdWJsaWMta2V5")
	assert.Contains(t, script, "NODE_UPDATER_HEALTH_URL=http://127.0.0.1:4200/v1/health")
	assert.Contains(t, script, "EnvironmentFile=/etc/nodeflow/node-updater.env")
	assert.Contains(t, script, "chmod 0600 /etc/nodeflow/node-updater.env")
	assert.Contains(t, script, "nodeflow-node-updater.service")
	assert.Contains(t, script, "nodeflow-node-updater.path")
	assert.Contains(t, script, "PathExists=/var/lib/nodeflow-updater/activation.json")
	assert.Contains(t, script, "systemctl enable --now nodeflow-node-updater.path")
	assert.Contains(t, script, "install -m 0755 /tmp/updater /usr/local/libexec/nodeflow-node-updater.new")
	assert.Contains(t, script, "mv -f /usr/local/libexec/nodeflow-node-updater.new")
	assert.Contains(t, script, "ReadWritePaths=/etc/haproxy /var/lib/nodeflow/updates /var/lib/nodeflow/credentials -/etc/sysctl.d -/proc/sys/fs/pipe-max-size -/proc/sys/fs/pipe-user-pages-soft -/etc/ufw")
	assert.Contains(t, script, "NODE_AGENT_FIREWALL_MODE=apply")
	assert.Contains(t, script, "CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW")
}

func TestInstallScriptSeedsSelectedSignedReleaseState(t *testing.T) {
	release := BootstrapRelease{ID: "release", Version: "0.5.0", Sequence: 17, SHA256: strings.Repeat("a", 64)}
	script := installScriptWithRelease("/tmp/agent", "/tmp/updater", 4200, "nfe_secret", "https://panel.test:4200", "ubuntu", NodeTLSIdentity{}, "ZmFrZS1wdWJsaWMta2V5", false, release, 22)
	assert.Contains(t, script, "NODE_AGENT_UPDATE_SEQUENCE=17")
	assert.Contains(t, script, `"version":"0.5.0","sequence":17,"sha256":"`+strings.Repeat("a", 64)+`","installed_at":`)
	assert.Contains(t, script, "/var/lib/nodeflow-updater/state.json")
}

func TestUpdaterMigrationKeepsAgentAndUpdaterEnvironmentsSeparated(t *testing.T) {
	contents, err := os.ReadFile("../../scripts/install-node-updater.sh")
	require.NoError(t, err)
	script := string(contents)
	agentMarker := "cat > /etc/systemd/system/nodeflow-node-agent.service"
	updaterMarker := "cat > /etc/systemd/system/nodeflow-node-updater.service"
	agentStart := strings.Index(script, agentMarker)
	updaterStart := strings.Index(script, updaterMarker)
	require.GreaterOrEqual(t, agentStart, 0)
	require.Greater(t, updaterStart, agentStart)
	agentUnit := script[agentStart:updaterStart]
	updaterUnit := script[updaterStart:]
	assert.Contains(t, agentUnit, "EnvironmentFile=/etc/nodeflow/node-agent.env")
	assert.NotContains(t, agentUnit, "EnvironmentFile=/etc/nodeflow/node-updater.env")
	assert.Contains(t, updaterUnit, "EnvironmentFile=/etc/nodeflow/node-updater.env")
	assert.NotContains(t, updaterUnit, "EnvironmentFile=/etc/nodeflow/node-agent.env")
}

// nodes.name CHECK (length(name) BETWEEN 1 AND 200): a longer name used to
// pass Validate and fail the bootstrap job later at create_node.
func TestRequestValidationRejectsNodeNamesTheDatabaseRejects(t *testing.T) {
	fingerprint := "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	long := Request{Name: strings.Repeat("я", 201), Address: "192.0.2.1", Username: "root", Password: "secret", HostKeySHA256: fingerprint}
	require.EqualError(t, long.Validate(), "name must not exceed 200 characters")
	limit := Request{Name: strings.Repeat("я", 200), Address: "192.0.2.1", Username: "root", Password: "secret", HostKeySHA256: fingerprint}
	require.NoError(t, limit.Validate())
}
