package bootstrap

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

type Request struct {
	Name                 string                              `json:"name"`
	Address              string                              `json:"address"`
	SSHPort              int                                 `json:"ssh_port"`
	Username             string                              `json:"username"`
	AuthMode             AuthMode                            `json:"auth_mode"`
	Password             string                              `json:"password"`
	PrivateKey           string                              `json:"private_key"`
	PrivateKeyPassphrase string                              `json:"private_key_passphrase"`
	SudoMode             SudoMode                            `json:"sudo_mode"`
	SudoPassword         string                              `json:"sudo_password"`
	AgentPort            int                                 `json:"agent_port"`
	HostKeySHA256        string                              `json:"host_key_sha256"`
	HostKeyAlgorithm     string                              `json:"host_key_algorithm"`
	AllowFirewallApply   bool                                `json:"allow_firewall_apply"`
	ReleaseID            string                              `json:"release_id,omitempty"`
	NodeID               string                              `json:"-"`
	EnrollmentToken      string                              `json:"-"`
	CredentialRotation   bool                                `json:"-"`
	Reinstall            bool                                `json:"-"`
	ReinstallBackupPath  string                              `json:"-"`
	UseStoredSSHPort     bool                                `json:"-"`
	OnReleaseSelected    func(context.Context, string) error `json:"-"`
}

type AuthMode string

const (
	AuthModePassword   AuthMode = "password"
	AuthModePrivateKey AuthMode = "private_key"
)

type SudoMode string

const (
	SudoModeAuto         SudoMode = "auto"
	SudoModeRoot         SudoMode = "root"
	SudoModePassword     SudoMode = "password"
	SudoModePasswordless SudoMode = "passwordless"
)

func (r *Request) Validate() error {
	r.Name, r.Address, r.Username = strings.TrimSpace(r.Name), strings.TrimSpace(r.Address), strings.TrimSpace(r.Username)
	r.HostKeySHA256 = strings.TrimSpace(r.HostKeySHA256)
	r.AuthMode = AuthMode(strings.TrimSpace(string(r.AuthMode)))
	r.SudoMode = SudoMode(strings.TrimSpace(string(r.SudoMode)))
	if r.AuthMode == "" {
		r.AuthMode = AuthModePassword
	}
	if r.SudoMode == "" {
		r.SudoMode = SudoModeAuto
	}
	if r.SSHPort == 0 {
		r.SSHPort = 22
	}
	if r.AgentPort == 0 {
		r.AgentPort = 4200
	}
	if r.HostKeyAlgorithm == "" {
		r.HostKeyAlgorithm = ssh.KeyAlgoED25519
	}
	if r.Name == "" || net.ParseIP(r.Address) == nil || r.Username == "" || r.HostKeySHA256 == "" {
		return errors.New("name, valid IP address, username and host_key_sha256 are required")
	}
	if err := r.validateAuthentication(); err != nil {
		return err
	}
	if err := r.validateSudo(); err != nil {
		return err
	}
	if r.SSHPort < 1 || r.SSHPort > 65535 || r.AgentPort < 1 || r.AgentPort > 65535 {
		return errors.New("ssh_port and agent_port must be between 1 and 65535")
	}
	if !strings.HasPrefix(r.HostKeySHA256, "SHA256:") {
		return errors.New("host_key_sha256 must use SHA256: OpenSSH fingerprint format")
	}
	if !validHostKeyAlgorithm(r.HostKeyAlgorithm) {
		return errors.New("host_key_algorithm must be ssh-ed25519, ecdsa-sha2-nistp256 or rsa-sha2-256")
	}
	if r.Reinstall && !validReinstallBackupPath(r.ReinstallBackupPath) {
		return errors.New("reinstall backup path is invalid")
	}
	digest, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(r.HostKeySHA256, "SHA256:"))
	if err != nil || len(digest) != sha256.Size {
		return errors.New("host_key_sha256 must contain a valid SHA-256 fingerprint")
	}
	return nil
}

func (r *Request) validateAuthentication() error {
	if strings.ContainsAny(r.Password, "\r\n") {
		return errors.New("password must not contain line breaks")
	}
	switch r.AuthMode {
	case AuthModePassword:
		if r.Password == "" {
			return errors.New("password is required when auth_mode is password")
		}
		if r.PrivateKey != "" || r.PrivateKeyPassphrase != "" {
			return errors.New("private_key fields require auth_mode private_key")
		}
	case AuthModePrivateKey:
		if r.PrivateKey == "" {
			return errors.New("private_key is required when auth_mode is private_key")
		}
		if r.Password != "" {
			return errors.New("password cannot be combined with auth_mode private_key; use sudo_password for sudo")
		}
		if _, err := parsePrivateKeySigner(r.PrivateKey, r.PrivateKeyPassphrase); err != nil {
			return errors.New("private_key is invalid or its passphrase is incorrect")
		}
	default:
		return errors.New("auth_mode must be password or private_key")
	}
	return nil
}

func (r *Request) validateSudo() error {
	if strings.ContainsAny(r.SudoPassword, "\r\n") {
		return errors.New("sudo_password must not contain line breaks")
	}
	switch r.SudoMode {
	case SudoModeAuto:
		return nil
	case SudoModeRoot, SudoModePasswordless:
		if r.SudoPassword != "" {
			return errors.New("sudo_password is only valid with sudo_mode auto or password")
		}
	case SudoModePassword:
		if r.effectiveSudoPassword() == "" {
			return errors.New("sudo_password is required for sudo_mode password")
		}
	default:
		return errors.New("sudo_mode must be auto, root, password or passwordless")
	}
	return nil
}

func (r *Request) ClearSecrets() {
	r.Password = ""
	r.PrivateKey = ""
	r.PrivateKeyPassphrase = ""
	r.SudoPassword = ""
	r.EnrollmentToken = ""
}

func (r Request) effectiveSudoPassword() string {
	if r.SudoPassword != "" {
		return r.SudoPassword
	}
	if r.AuthMode == AuthModePassword {
		return r.Password
	}
	return ""
}

type Installer interface {
	Install(context.Context, Request) error
}

type ReleaseSource interface {
	OpenBootstrapRelease(context.Context, string, string, string) (BootstrapRelease, error)
}

type BootstrapRelease struct {
	ID       string
	Version  string
	Sequence uint64
	SHA256   string
	Content  io.ReadCloser
}

type progressContextKey struct{}

// WithProgress attaches a polling-safe stage callback to a bootstrap context.
// Callers must still treat stage values as untrusted and allow-list them before
// exposing them through an API.
func WithProgress(ctx context.Context, report func(string)) context.Context {
	if report == nil {
		return ctx
	}
	return context.WithValue(ctx, progressContextKey{}, report)
}

// ReportProgress publishes only a static bootstrap stage identifier. It is a
// no-op for installers used outside the asynchronous Panel job runner.
func ReportProgress(ctx context.Context, stage string) {
	if report, ok := ctx.Value(progressContextKey{}).(func(string)); ok {
		report(stage)
	}
}

// CredentialRotationFinalizer lets Panel roll back the root-only credential
// backup when the newly installed identity cannot authenticate back to Panel.
type CredentialRotationFinalizer interface {
	RollbackCredentials(context.Context, Request) error
	FinalizeCredentials(context.Context, Request) error
}

type StageError struct {
	Stage          string
	DiagnosticCode string
	ExitCode       int
}

func (e *StageError) Error() string { return "bootstrap failed at " + e.Stage }
func stage(name string) error       { return &StageError{Stage: name} }

func stageRun(name string, err error) error {
	exitCode := 0
	var exitError *ssh.ExitError
	if errors.As(err, &exitError) {
		exitCode = exitError.ExitStatus()
	}
	diagnostic := "remote_command_failed"
	if name == "install" {
		diagnostic = installDiagnosticForExit(exitCode)
	}
	return &StageError{Stage: name, DiagnosticCode: diagnostic, ExitCode: exitCode}
}

func installDiagnosticForExit(exitCode int) string {
	switch exitCode {
	case 71:
		return "haproxy_release_unavailable"
	case 72:
		return "haproxy_dependency_prepare_failed"
	case 73:
		return "haproxy_candidate_libraries_missing"
	case 74:
		return "haproxy_candidate_runtime_failed"
	case 75:
		return "haproxy_config_validation_failed"
	default:
		return "remote_install_failed"
	}
}

type SSHInstaller struct {
	UpdaterBinaryPath string
	PanelURL          string
	Releases          ReleaseSource
	Issuer            IdentityIssuer
	UpdatePublicKey   string
	Timeout           time.Duration
	Dial              func(context.Context, string, *ssh.ClientConfig) (*ssh.Client, error)
}

func NewSSHInstaller(panelURL string) *SSHInstaller {
	return &SSHInstaller{PanelURL: strings.TrimRight(strings.TrimSpace(panelURL), "/"), Timeout: 30 * time.Second}
}

func (*SSHInstaller) RequiresAgentRelease() bool { return true }

func (i *SSHInstaller) Install(ctx context.Context, r Request) error {
	defer r.ClearSecrets()
	ReportProgress(ctx, "configuration")
	if i.Releases == nil || (i.UpdatePublicKey != "" && i.UpdaterBinaryPath == "") || (!strings.HasPrefix(i.PanelURL, "http://") && !strings.HasPrefix(i.PanelURL, "https://")) || strings.ContainsAny(i.PanelURL, "\r\n") || (r.Reinstall && !validReinstallBackupPath(r.ReinstallBackupPath)) {
		return stage("configuration")
	}
	if r.CredentialRotation {
		return i.rotateCredentials(ctx, r)
	}
	var err error
	var updaterBinary *os.File
	if i.UpdatePublicKey != "" {
		ReportProgress(ctx, "updater_binary")
		updaterBinary, err = os.Open(i.UpdaterBinaryPath)
		if err != nil {
			return stage("updater_binary")
		}
		defer updaterBinary.Close()
	}
	identity := NodeTLSIdentity{}
	if i.Issuer != nil {
		ReportProgress(ctx, "identity")
		identity, err = i.Issuer.Issue(r.NodeID)
		if err != nil {
			return stage("identity")
		}
	}

	ReportProgress(ctx, "authentication")
	cfg, err := i.clientConfig(r)
	if err != nil {
		return stage("authentication")
	}
	addr := net.JoinHostPort(r.Address, strconv.Itoa(r.SSHPort))
	dial := i.Dial
	if dial == nil {
		dial = dialSSH
	}
	ReportProgress(ctx, "connect")
	client, err := dial(ctx, addr, cfg)
	if err != nil {
		// A hostile SSH server controls disconnect text and could reflect submitted
		// credentials into the client error. Keep both logs and API responses generic.
		slog.Error("SSH bootstrap connection failed", "address", addr, "user", r.Username)
		return stage("connect")
	}
	defer client.Close()
	stopCloseOnCancel := context.AfterFunc(ctx, func() { _ = client.Close() })
	defer stopCloseOnCancel()
	ReportProgress(ctx, "preflight")
	root, distro, operatingSystem, architecture, err := inspect(client)
	if err != nil {
		return stage("preflight")
	}
	ReportProgress(ctx, "release")
	release, err := i.Releases.OpenBootstrapRelease(ctx, r.ReleaseID, operatingSystem, architecture)
	if err != nil || release.Content == nil {
		return stage("release")
	}
	defer release.Content.Close()
	if r.OnReleaseSelected != nil {
		if err = r.OnReleaseSelected(ctx, release.ID); err != nil {
			return stage("release_assignment")
		}
	}

	ReportProgress(ctx, "prepare")
	tmp, err := randomRemotePath()
	if err != nil {
		return stage("prepare")
	}
	defer func() { _ = run(client, "rm -f -- "+tmp, nil) }()
	ReportProgress(ctx, "upload")
	if err := run(client, "umask 077; cat > "+tmp+" && chmod 0755 "+tmp, release.Content); err != nil {
		return stage("upload")
	}
	updaterTmp := ""
	if updaterBinary != nil {
		updaterTmp, err = randomRemotePath()
		if err != nil {
			return stage("prepare")
		}
		defer func() { _ = run(client, "rm -f -- "+updaterTmp, nil) }()
		ReportProgress(ctx, "updater_upload")
		if err := run(client, "umask 077; cat > "+updaterTmp+" && chmod 0755 "+updaterTmp, updaterBinary); err != nil {
			return stage("updater_upload")
		}
	}

	ReportProgress(ctx, "privilege")
	privilege, err := resolvePrivilege(r, root)
	if err != nil || verifyPrivilege(client, privilege) != nil {
		return stage("privilege")
	}
	script := installScriptWithRelease(tmp, updaterTmp, r.AgentPort, r.EnrollmentToken, i.PanelURL, distro, identity, i.UpdatePublicKey, r.AllowFirewallApply, release, r.SSHPort)
	if r.Reinstall {
		script = credentialSafeReinstallScript(script, r.ReinstallBackupPath)
	}
	var stdin io.Reader = strings.NewReader(script)
	// SSH_CONNECTION carries the server-side sshd port. Prefer it over the
	// externally supplied port so a NAT/port-forwarded bootstrap cannot prepare
	// the wrong UFW rule and lock out the session that is performing the install.
	command := `NODEFLOW_SSH_PORT=${SSH_CONNECTION##* }; export NODEFLOW_SSH_PORT; sh -s`
	if privilege.Mode == SudoModePassword {
		command = `NODEFLOW_SSH_PORT=${SSH_CONNECTION##* }; export NODEFLOW_SSH_PORT; sudo -S -p '' env NODEFLOW_SSH_PORT="$NODEFLOW_SSH_PORT" sh -s`
		stdin = io.MultiReader(strings.NewReader(privilege.Password+"\n"), stdin)
	} else if privilege.Mode == SudoModePasswordless {
		command = `NODEFLOW_SSH_PORT=${SSH_CONNECTION##* }; export NODEFLOW_SSH_PORT; sudo -n env NODEFLOW_SSH_PORT="$NODEFLOW_SSH_PORT" sh -s`
	}
	ReportProgress(ctx, "install")
	if err := run(client, command, stdin); err != nil {
		return stageRun("install", err)
	}
	return nil
}

const credentialBackupPath = "/var/backups/nodeflow-node/credential-rollback"
const reinstallCredentialBackupPrefix = "/var/backups/nodeflow-node/reinstall-credential-rollback-"

func NewReinstallBackupPath() (string, error) {
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return reinstallCredentialBackupPrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

func validReinstallBackupPath(path string) bool {
	suffix := strings.TrimPrefix(path, reinstallCredentialBackupPrefix)
	if suffix == path || len(suffix) != 24 {
		return false
	}
	for _, character := range suffix {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func (i *SSHInstaller) rotateCredentials(ctx context.Context, r Request) error {
	identity := NodeTLSIdentity{}
	var err error
	if i.Issuer != nil {
		identity, err = i.Issuer.Issue(r.NodeID)
		if err != nil {
			return stage("identity")
		}
	}
	return i.runCredentialScript(ctx, r, credentialRotationScript(r.EnrollmentToken, i.PanelURL, identity), "rotate")
}

func (i *SSHInstaller) RollbackCredentials(ctx context.Context, r Request) error {
	if r.Reinstall {
		if !validReinstallBackupPath(r.ReinstallBackupPath) {
			return stage("configuration")
		}
		return i.runCredentialScript(ctx, r, credentialReinstallRollbackScript(r.ReinstallBackupPath), "rollback")
	}
	return i.runCredentialScript(ctx, r, credentialRollbackScript(), "rollback")
}

func (i *SSHInstaller) FinalizeCredentials(ctx context.Context, r Request) error {
	backupPath := credentialBackupPath
	finalizeScript := "set -eu\nrm -rf -- " + backupPath + "\n"
	if r.Reinstall {
		if !validReinstallBackupPath(r.ReinstallBackupPath) {
			return stage("configuration")
		}
		backupPath = r.ReinstallBackupPath
		finalizeScript = credentialReinstallFinalizeScript(backupPath)
	}
	return i.runCredentialScript(ctx, r, finalizeScript, "finalize")
}

func credentialReinstallFinalizeScript(backupPath string) string {
	return `set -eu
backup=` + backupPath + `
systemctl stop bridge-control-node-updater.path >/dev/null 2>&1 || true
systemctl stop bridge-control-node-updater.service >/dev/null 2>&1 || true
systemctl stop bridge-control-node-agent.service >/dev/null 2>&1 || true
systemctl disable bridge-control-node-updater.path >/dev/null 2>&1 || true
systemctl disable bridge-control-node-agent.service >/dev/null 2>&1 || true
rm -f /etc/systemd/system/bridge-control-node-agent.service
rm -f /etc/systemd/system/bridge-control-node-updater.service
rm -f /etc/systemd/system/bridge-control-node-updater.path
rm -f /usr/local/bin/bridge-control-node-agent
rm -f /usr/local/libexec/bridge-control-node-updater
rm -rf /etc/bridge-control
rm -rf /var/lib/bridge-control
rm -rf /var/lib/bridge-control-updater
systemctl daemon-reload
rm -rf -- "$backup"
`
}

func (i *SSHInstaller) runCredentialScript(ctx context.Context, r Request, script, failureStage string) error {
	defer r.ClearSecrets()
	cfg, err := i.clientConfig(r)
	if err != nil {
		return stage("authentication")
	}
	addr := net.JoinHostPort(r.Address, strconv.Itoa(r.SSHPort))
	dial := i.Dial
	if dial == nil {
		dial = dialSSH
	}
	client, err := dial(ctx, addr, cfg)
	if err != nil {
		return stage("connect")
	}
	defer client.Close()
	stopCloseOnCancel := context.AfterFunc(ctx, func() { _ = client.Close() })
	defer stopCloseOnCancel()
	root, _, _, _, err := inspect(client)
	if err != nil {
		return stage("privilege")
	}
	privilege, err := resolvePrivilege(r, root)
	if err != nil || verifyPrivilege(client, privilege) != nil {
		return stage("privilege")
	}
	command := "sh -s"
	var stdin io.Reader = strings.NewReader(script)
	if privilege.Mode == SudoModePassword {
		command = "sudo -S -p '' sh -s"
		stdin = io.MultiReader(strings.NewReader(privilege.Password+"\n"), stdin)
	} else if privilege.Mode == SudoModePasswordless {
		command = "sudo -n sh -s"
	}
	if err = run(client, command, stdin); err != nil {
		return stageRun(failureStage, err)
	}
	return nil
}

func credentialRotationScript(token, panelURL string, identity NodeTLSIdentity) string {
	tlsUpdate := ""
	tlsEnvironment := ""
	if len(identity.CACertificatePEM) != 0 || len(identity.CertificatePEM) != 0 || len(identity.PrivateKeyPEM) != 0 {
		tlsUpdate = fmt.Sprintf(`install -d -m 0750 /etc/nodeflow/tls
cat > /etc/nodeflow/tls/ca.crt.new <<'NODEFLOW_CA_CERT'
%sNODEFLOW_CA_CERT
cat > /etc/nodeflow/tls/node.crt.new <<'NODEFLOW_NODE_CERT'
%sNODEFLOW_NODE_CERT
cat > /etc/nodeflow/tls/node.key.new <<'NODEFLOW_NODE_KEY'
%sNODEFLOW_NODE_KEY
chmod 0644 /etc/nodeflow/tls/ca.crt.new /etc/nodeflow/tls/node.crt.new
chmod 0600 /etc/nodeflow/tls/node.key.new
mv -f /etc/nodeflow/tls/ca.crt.new /etc/nodeflow/tls/ca.crt
mv -f /etc/nodeflow/tls/node.crt.new /etc/nodeflow/tls/node.crt
mv -f /etc/nodeflow/tls/node.key.new /etc/nodeflow/tls/node.key
`, identity.CACertificatePEM, identity.CertificatePEM, identity.PrivateKeyPEM)
		tlsEnvironment = `NODE_AGENT_PANEL_TLS_CA=/etc/nodeflow/tls/ca.crt
NODE_AGENT_PANEL_TLS_CERT=/etc/nodeflow/tls/node.crt
NODE_AGENT_PANEL_TLS_KEY=/etc/nodeflow/tls/node.key
`
		if identity.ServerName != "" {
			tlsEnvironment += "NODE_AGENT_PANEL_TLS_SERVER_NAME=" + identity.ServerName + "\n"
		}
	}
	return fmt.Sprintf(`set -eu
backup=%s
test -f /etc/nodeflow/node-agent.env
install -d -m 0700 /var/backups/nodeflow-node
mkdir "$backup"
chmod 0700 "$backup"
cp -a /etc/nodeflow/node-agent.env "$backup/node-agent.env"
if [ -d /etc/nodeflow/tls ]; then cp -a /etc/nodeflow/tls "$backup/tls"; fi
if [ -d /var/lib/nodeflow/credentials ]; then
  cp -a /var/lib/nodeflow/credentials "$backup/credentials"
  : > "$backup/had-credentials"
fi
committed=0
restore_credentials() {
  set +e
  systemctl stop nodeflow-node-agent.service
  install -m 0600 "$backup/node-agent.env" /etc/nodeflow/node-agent.env
  rm -rf /etc/nodeflow/tls
  if [ -d "$backup/tls" ]; then cp -a "$backup/tls" /etc/nodeflow/tls; fi
  rm -rf /var/lib/nodeflow/credentials
  if [ -f "$backup/had-credentials" ]; then cp -a "$backup/credentials" /var/lib/nodeflow/credentials; fi
  systemctl restart nodeflow-node-agent.service
  rm -rf "$backup"
}
cleanup() {
  status=$?
  trap - EXIT HUP INT TERM
  if [ "$committed" -ne 1 ]; then restore_credentials; fi
  exit "$status"
}
trap cleanup EXIT HUP INT TERM
systemctl stop nodeflow-node-agent.service
awk '!/^NODE_AGENT_TOKEN=|^NODE_AGENT_PANEL_URL=|^NODE_AGENT_PANEL_TLS_CA=|^NODE_AGENT_PANEL_TLS_CERT=|^NODE_AGENT_PANEL_TLS_KEY=|^NODE_AGENT_PANEL_TLS_SERVER_NAME=|^NODE_AGENT_CREDENTIAL_RENEWAL_MODE=|^NODE_AGENT_CREDENTIAL_STATE_DIR=/' /etc/nodeflow/node-agent.env > /etc/nodeflow/node-agent.env.new
cat >> /etc/nodeflow/node-agent.env.new <<'NODEFLOW_CREDENTIALS'
NODE_AGENT_TOKEN=%s
NODE_AGENT_PANEL_URL=%s
%sNODE_AGENT_CREDENTIAL_RENEWAL_MODE=apply
NODE_AGENT_CREDENTIAL_STATE_DIR=/var/lib/nodeflow/credentials
NODEFLOW_CREDENTIALS
chmod 0600 /etc/nodeflow/node-agent.env.new
mv -f /etc/nodeflow/node-agent.env.new /etc/nodeflow/node-agent.env
%srm -rf /var/lib/nodeflow/credentials
install -d -m 0700 /var/lib/nodeflow/credentials
systemctl restart nodeflow-node-agent.service
sleep 2
systemctl is-active --quiet nodeflow-node-agent.service
committed=1
`, credentialBackupPath, token, panelURL, tlsEnvironment, tlsUpdate)
}

func credentialRollbackScript() string {
	return `set -eu
backup=` + credentialBackupPath + `
test -f "$backup/node-agent.env"
systemctl stop nodeflow-node-agent.service
install -m 0600 "$backup/node-agent.env" /etc/nodeflow/node-agent.env
rm -rf /etc/nodeflow/tls
if [ -d "$backup/tls" ]; then cp -a "$backup/tls" /etc/nodeflow/tls; fi
rm -rf /var/lib/nodeflow/credentials
if [ -f "$backup/had-credentials" ]; then cp -a "$backup/credentials" /var/lib/nodeflow/credentials; fi
systemctl restart nodeflow-node-agent.service
sleep 2
systemctl is-active --quiet nodeflow-node-agent.service
rm -rf "$backup"
`
}

// credentialSafeReinstallScript wraps the full installer with a backup of the
// Agent/updater binaries, units, environments, TLS identity and trusted updater
// state. On any shell failure the previous installation is restored before the
// SSH command exits; after success the backup remains available for Panel's
// explicit heartbeat rollback/finalize step.
func credentialSafeReinstallScript(installScript, backupPath string) string {
	return fmt.Sprintf(`set -eu
backup=%s
install -d -m 0700 /var/backups/nodeflow-node
mkdir "$backup"
chmod 0700 "$backup"
if [ -f /usr/local/bin/nodeflow-node-agent ]; then
  cp -a /usr/local/bin/nodeflow-node-agent "$backup/node-agent"
  : > "$backup/had-node-agent"
fi
if [ -f /usr/local/libexec/nodeflow-node-updater ]; then
  cp -a /usr/local/libexec/nodeflow-node-updater "$backup/node-updater"
  : > "$backup/had-node-updater"
fi
if [ -f /etc/nodeflow/node-agent.env ]; then
  cp -a /etc/nodeflow/node-agent.env "$backup/node-agent.env"
  : > "$backup/had-node-agent-env"
fi
if [ -f /etc/nodeflow/node-updater.env ]; then
  cp -a /etc/nodeflow/node-updater.env "$backup/node-updater.env"
  : > "$backup/had-node-updater-env"
fi
if [ -d /etc/nodeflow/tls ]; then
  cp -a /etc/nodeflow/tls "$backup/tls"
  : > "$backup/had-tls"
fi
if [ -d /var/lib/nodeflow/credentials ]; then
  cp -a /var/lib/nodeflow/credentials "$backup/credentials"
  : > "$backup/had-credentials"
fi
if [ -d /var/lib/nodeflow-updater ]; then
  cp -a /var/lib/nodeflow-updater "$backup/updater-state"
  : > "$backup/had-updater-state"
fi
if [ -f /etc/systemd/system/nodeflow-node-agent.service ]; then
  cp -a /etc/systemd/system/nodeflow-node-agent.service "$backup/node-agent.service"
  : > "$backup/had-node-agent-unit"
fi
if [ -f /etc/systemd/system/nodeflow-node-updater.service ]; then
  cp -a /etc/systemd/system/nodeflow-node-updater.service "$backup/node-updater.service"
  : > "$backup/had-node-updater-unit"
fi
if [ -f /etc/systemd/system/nodeflow-node-updater.path ]; then
  cp -a /etc/systemd/system/nodeflow-node-updater.path "$backup/node-updater.path"
  : > "$backup/had-node-updater-path"
fi
if systemctl is-active --quiet nodeflow-node-agent.service; then
  : > "$backup/was-active"
fi
if systemctl is-enabled --quiet nodeflow-node-agent.service; then
  : > "$backup/was-enabled"
fi
if systemctl is-active --quiet nodeflow-node-updater.path; then
  : > "$backup/updater-path-was-active"
fi
if systemctl is-enabled --quiet nodeflow-node-updater.path; then
  : > "$backup/updater-path-was-enabled"
fi
if systemctl is-active --quiet bridge-control-node-agent.service; then
  : > "$backup/legacy-agent-was-active"
fi
if systemctl is-enabled --quiet bridge-control-node-agent.service; then
  : > "$backup/legacy-agent-was-enabled"
fi
if systemctl is-active --quiet bridge-control-node-updater.path; then
  : > "$backup/legacy-updater-path-was-active"
fi
if systemctl is-enabled --quiet bridge-control-node-updater.path; then
  : > "$backup/legacy-updater-path-was-enabled"
fi
install -d -m 0700 "$backup/old-packages"
if [ -d /etc/haproxy ]; then
  tar -C / -czf "$backup/haproxy-config.tar.gz" etc/haproxy
  : > "$backup/had-haproxy-config"
fi
if [ -f /etc/apt/sources.list.d/haproxy-performance-ha34.list ]; then
  cp -a /etc/apt/sources.list.d/haproxy-performance-ha34.list "$backup/haproxy-performance-ha34.list"
  : > "$backup/had-haproxy-repository"
fi
if [ -f /usr/share/keyrings/HAPROXY-key-community.asc ]; then
  cp -a /usr/share/keyrings/HAPROXY-key-community.asc "$backup/HAPROXY-key-community.asc"
  : > "$backup/had-haproxy-repository-key"
fi
if dpkg-query -W -f='${Status}' haproxy-awslc 2>/dev/null | grep -q 'ok installed'; then
  : > "$backup/had-haproxy-awslc-package"
fi
for package in haproxy haproxy-awslc libssl-awslc ufw; do
  if ! dpkg-query -W -f='${Status}' "$package" 2>/dev/null | grep -q 'ok installed'; then
    continue
  fi
  if [ "$package" = libssl-awslc ] && [ ! -f "$backup/had-haproxy-awslc-package" ]; then
    continue
  fi
  version=$(dpkg-query -W -f='${Version}' "$package")
  printf '%%s=%%s\n' "$package" "$version" >> "$backup/old-package-versions"
  (cd "$backup/old-packages" && apt-get download "$package=$version" >/dev/null)
  find "$backup/old-packages" -maxdepth 1 -type f -name "${package}_*.deb" -print -quit | grep -q .
  case "$package" in
    haproxy|haproxy-awslc) : > "$backup/had-haproxy-package" ;;
    ufw) : > "$backup/had-ufw-package" ;;
  esac
done
if systemctl is-active --quiet haproxy.service; then : > "$backup/haproxy-was-active"; fi
if systemctl is-enabled --quiet haproxy.service; then : > "$backup/haproxy-was-enabled"; fi
if [ -d /etc/ufw ]; then
  tar -C / -czf "$backup/ufw-config.tar.gz" etc/ufw
  : > "$backup/had-ufw-config"
fi
if [ -f /etc/default/ufw ]; then
  cp -a /etc/default/ufw "$backup/default-ufw"
  : > "$backup/had-default-ufw"
fi
if command -v ufw >/dev/null 2>&1 && ufw status | grep -qx 'Status: active'; then
  : > "$backup/ufw-was-active"
fi
if systemctl is-enabled --quiet ufw.service; then : > "$backup/ufw-was-enabled"; fi
cat > "$backup/restore-infrastructure.sh" <<'NODEFLOW_RESTORE_INFRASTRUCTURE'
#!/bin/sh
set -u
backup=${NODEFLOW_REINSTALL_BACKUP:?}
failed=0
export DEBIAN_FRONTEND=noninteractive

if command -v ufw >/dev/null 2>&1; then
  ufw --force disable >/dev/null 2>&1 || failed=1
fi
if command -v systemctl >/dev/null 2>&1; then
  if systemctl is-active --quiet haproxy.service; then systemctl stop haproxy.service >/dev/null 2>&1 || failed=1; fi
fi

rm -f /etc/apt/sources.list.d/haproxy-performance-ha34.list
if [ -f "$backup/had-haproxy-repository" ]; then
  install -d -m 0755 /etc/apt/sources.list.d
  cp -a "$backup/haproxy-performance-ha34.list" /etc/apt/sources.list.d/haproxy-performance-ha34.list || failed=1
fi
rm -f /usr/share/keyrings/HAPROXY-key-community.asc
if [ -f "$backup/had-haproxy-repository-key" ]; then
  install -d -m 0755 /usr/share/keyrings
  cp -a "$backup/HAPROXY-key-community.asc" /usr/share/keyrings/HAPROXY-key-community.asc || failed=1
fi

apt-get update -qq || failed=1
haproxy_remove_packages="haproxy-awslc haproxy"
if [ -f "$backup/had-haproxy-awslc-package" ]; then
  haproxy_remove_packages="$haproxy_remove_packages libssl-awslc"
fi
apt-get remove -y -qq $haproxy_remove_packages >/dev/null 2>&1 || failed=1
haproxy_debs=$(find "$backup/old-packages" -maxdepth 1 -type f \( -name 'haproxy_*.deb' -o -name 'haproxy-awslc_*.deb' -o -name 'libssl-awslc_*.deb' \) -print)
if [ -n "$haproxy_debs" ]; then
  apt-get install -y -qq --allow-downgrades -o Dpkg::Options::=--force-confold $haproxy_debs || dpkg -i $haproxy_debs || failed=1
fi
rm -rf /etc/haproxy
if [ -f "$backup/had-haproxy-config" ]; then
  tar -C / -xzf "$backup/haproxy-config.tar.gz" || failed=1
fi

if [ -f "$backup/had-ufw-package" ]; then
  apt-get remove -y -qq ufw >/dev/null 2>&1 || failed=1
  ufw_deb=$(find "$backup/old-packages" -maxdepth 1 -type f -name 'ufw_*.deb' -print -quit)
  if [ -n "$ufw_deb" ]; then
    apt-get install -y -qq --allow-downgrades -o Dpkg::Options::=--force-confold -o Dpkg::Options::=--force-confmiss "$ufw_deb" || dpkg --force-confmiss -i "$ufw_deb" || failed=1
  else
    failed=1
  fi
else
  # An initially absent UFW must return to dpkg state "not installed", not
  # the residual-config (rc) state that suppresses conffile recreation later.
  apt-get purge -y -qq ufw >/dev/null 2>&1 || failed=1
fi
rm -rf /etc/ufw
if [ -f "$backup/had-ufw-config" ]; then
  tar -C / -xzf "$backup/ufw-config.tar.gz" || failed=1
fi
rm -f /etc/default/ufw
if [ -f "$backup/had-default-ufw" ]; then
  install -d -m 0755 /etc/default
  cp -a "$backup/default-ufw" /etc/default/ufw || failed=1
fi

if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || failed=1
  if [ -f "$backup/haproxy-was-enabled" ]; then systemctl enable haproxy.service >/dev/null 2>&1 || failed=1; else systemctl disable haproxy.service >/dev/null 2>&1 || true; fi
  if [ -f "$backup/haproxy-was-active" ]; then systemctl restart haproxy.service || failed=1; else systemctl stop haproxy.service >/dev/null 2>&1 || true; fi
  if [ -f "$backup/ufw-was-enabled" ] && [ -f "$backup/had-ufw-package" ]; then systemctl enable ufw.service >/dev/null 2>&1 || failed=1; else systemctl disable ufw.service >/dev/null 2>&1 || true; fi
fi
if [ -f "$backup/ufw-was-active" ]; then
  if command -v ufw >/dev/null 2>&1; then ufw --force enable >/dev/null 2>&1 || failed=1; else failed=1; fi
elif command -v ufw >/dev/null 2>&1; then
  ufw --force disable >/dev/null 2>&1 || failed=1
fi
exit "$failed"
NODEFLOW_RESTORE_INFRASTRUCTURE
chmod 0700 "$backup/restore-infrastructure.sh"
: > "$backup/ready"
committed=0
restore_installation() {
  set +e
  systemctl stop nodeflow-node-updater.path >/dev/null 2>&1 || true
  systemctl stop nodeflow-node-updater.service >/dev/null 2>&1 || true
  systemctl stop nodeflow-node-agent.service >/dev/null 2>&1 || true
  if [ -f "$backup/had-node-agent" ]; then
    cp -a "$backup/node-agent" /usr/local/bin/nodeflow-node-agent.rollback
    mv -f /usr/local/bin/nodeflow-node-agent.rollback /usr/local/bin/nodeflow-node-agent
  else
    rm -f /usr/local/bin/nodeflow-node-agent
  fi
  if [ -f "$backup/had-node-updater" ]; then
    install -d -m 0755 /usr/local/libexec
    cp -a "$backup/node-updater" /usr/local/libexec/nodeflow-node-updater.rollback
    mv -f /usr/local/libexec/nodeflow-node-updater.rollback /usr/local/libexec/nodeflow-node-updater
  else
    rm -f /usr/local/libexec/nodeflow-node-updater
  fi
  if [ -f "$backup/had-node-agent-env" ]; then
    install -m 0600 "$backup/node-agent.env" /etc/nodeflow/node-agent.env
  else
    rm -f /etc/nodeflow/node-agent.env
  fi
  if [ -f "$backup/had-node-updater-env" ]; then
    install -m 0600 "$backup/node-updater.env" /etc/nodeflow/node-updater.env
  else
    rm -f /etc/nodeflow/node-updater.env
  fi
  rm -rf /etc/nodeflow/tls
  if [ -f "$backup/had-tls" ]; then cp -a "$backup/tls" /etc/nodeflow/tls; fi
  rm -rf /var/lib/nodeflow/credentials
  if [ -f "$backup/had-credentials" ]; then cp -a "$backup/credentials" /var/lib/nodeflow/credentials; fi
  rm -rf /var/lib/nodeflow-updater
  if [ -f "$backup/had-updater-state" ]; then cp -a "$backup/updater-state" /var/lib/nodeflow-updater; fi
  if [ -f "$backup/had-node-agent-unit" ]; then
    cp -a "$backup/node-agent.service" /etc/systemd/system/nodeflow-node-agent.service
  else
    rm -f /etc/systemd/system/nodeflow-node-agent.service
  fi
  if [ -f "$backup/had-node-updater-unit" ]; then
    cp -a "$backup/node-updater.service" /etc/systemd/system/nodeflow-node-updater.service
  else
    rm -f /etc/systemd/system/nodeflow-node-updater.service
  fi
  if [ -f "$backup/had-node-updater-path" ]; then
    cp -a "$backup/node-updater.path" /etc/systemd/system/nodeflow-node-updater.path
  else
    rm -f /etc/systemd/system/nodeflow-node-updater.path
  fi
  infrastructure_restore_failed=0
  if ! NODEFLOW_REINSTALL_BACKUP="$backup" "$backup/restore-infrastructure.sh"; then
    echo "failed to restore previous HAProxy/UFW installation; rollback backup retained at $backup" >&2
    infrastructure_restore_failed=1
  fi
  systemctl daemon-reload
  if [ -f "$backup/was-enabled" ]; then systemctl enable nodeflow-node-agent.service >/dev/null 2>&1; else systemctl disable nodeflow-node-agent.service >/dev/null 2>&1 || true; fi
  if [ -f "$backup/updater-path-was-enabled" ]; then systemctl enable nodeflow-node-updater.path >/dev/null 2>&1; else systemctl disable nodeflow-node-updater.path >/dev/null 2>&1 || true; fi
  if [ -f "$backup/was-active" ] && [ -f "$backup/had-node-agent-env" ]; then
    systemctl restart nodeflow-node-agent.service
  fi
  if [ -f "$backup/updater-path-was-active" ] && [ -f "$backup/had-node-updater-path" ]; then
    systemctl start nodeflow-node-updater.path
  fi
  if [ -f "$backup/legacy-agent-was-enabled" ]; then systemctl enable bridge-control-node-agent.service >/dev/null 2>&1; fi
  if [ -f "$backup/legacy-updater-path-was-enabled" ]; then systemctl enable bridge-control-node-updater.path >/dev/null 2>&1; fi
  if [ -f "$backup/legacy-agent-was-active" ]; then systemctl restart bridge-control-node-agent.service; fi
  if [ -f "$backup/legacy-updater-path-was-active" ]; then systemctl start bridge-control-node-updater.path; fi
  return "$infrastructure_restore_failed"
}
cleanup() {
  status=$?
  trap - EXIT HUP INT TERM
  if [ "$committed" -ne 1 ]; then restore_installation; fi
  if [ -n "${nodeflow_haproxy_txn:-}" ]; then rm -rf "$nodeflow_haproxy_txn"; fi
  exit "$status"
}
trap cleanup EXIT HUP INT TERM
systemctl stop nodeflow-node-updater.path >/dev/null 2>&1 || true
systemctl stop nodeflow-node-updater.service >/dev/null 2>&1 || true
systemctl stop nodeflow-node-agent.service >/dev/null 2>&1 || true
systemctl stop bridge-control-node-updater.path >/dev/null 2>&1 || true
systemctl stop bridge-control-node-updater.service >/dev/null 2>&1 || true
systemctl stop bridge-control-node-agent.service >/dev/null 2>&1 || true
rm -rf /var/lib/nodeflow/credentials
install -d -m 0700 /var/lib/nodeflow/credentials
export NODEFLOW_REINSTALL_BACKUP="$backup"
%s
committed=1
`, backupPath, installScript)
}

func credentialReinstallRollbackScript(backupPath string) string {
	return `set -u
backup=` + backupPath + `
if [ ! -d "$backup" ]; then
  exit 0
fi
if [ ! -f "$backup/ready" ]; then
  rm -rf "$backup"
  exit 0
fi
rollback_failed=0
systemctl stop nodeflow-node-updater.path >/dev/null 2>&1 || true
systemctl stop nodeflow-node-updater.service >/dev/null 2>&1 || true
systemctl stop nodeflow-node-agent.service >/dev/null 2>&1 || true
if [ -f "$backup/had-node-agent" ]; then
  cp -a "$backup/node-agent" /usr/local/bin/nodeflow-node-agent.rollback || rollback_failed=1
  mv -f /usr/local/bin/nodeflow-node-agent.rollback /usr/local/bin/nodeflow-node-agent || rollback_failed=1
else
  rm -f /usr/local/bin/nodeflow-node-agent || rollback_failed=1
fi
if [ -f "$backup/had-node-updater" ]; then
  install -d -m 0755 /usr/local/libexec || rollback_failed=1
  cp -a "$backup/node-updater" /usr/local/libexec/nodeflow-node-updater.rollback || rollback_failed=1
  mv -f /usr/local/libexec/nodeflow-node-updater.rollback /usr/local/libexec/nodeflow-node-updater || rollback_failed=1
else
  rm -f /usr/local/libexec/nodeflow-node-updater || rollback_failed=1
fi
if [ -f "$backup/had-node-agent-env" ]; then
  install -m 0600 "$backup/node-agent.env" /etc/nodeflow/node-agent.env || rollback_failed=1
else
  rm -f /etc/nodeflow/node-agent.env || rollback_failed=1
fi
if [ -f "$backup/had-node-updater-env" ]; then
  install -m 0600 "$backup/node-updater.env" /etc/nodeflow/node-updater.env || rollback_failed=1
else
  rm -f /etc/nodeflow/node-updater.env || rollback_failed=1
fi
rm -rf /etc/nodeflow/tls || rollback_failed=1
if [ -f "$backup/had-tls" ]; then cp -a "$backup/tls" /etc/nodeflow/tls || rollback_failed=1; fi
rm -rf /var/lib/nodeflow/credentials || rollback_failed=1
if [ -f "$backup/had-credentials" ]; then cp -a "$backup/credentials" /var/lib/nodeflow/credentials || rollback_failed=1; fi
rm -rf /var/lib/nodeflow-updater || rollback_failed=1
if [ -f "$backup/had-updater-state" ]; then cp -a "$backup/updater-state" /var/lib/nodeflow-updater || rollback_failed=1; fi
if [ -f "$backup/had-node-agent-unit" ]; then
  cp -a "$backup/node-agent.service" /etc/systemd/system/nodeflow-node-agent.service || rollback_failed=1
else
  rm -f /etc/systemd/system/nodeflow-node-agent.service || rollback_failed=1
fi
if [ -f "$backup/had-node-updater-unit" ]; then
  cp -a "$backup/node-updater.service" /etc/systemd/system/nodeflow-node-updater.service || rollback_failed=1
else
  rm -f /etc/systemd/system/nodeflow-node-updater.service || rollback_failed=1
fi
if [ -f "$backup/had-node-updater-path" ]; then
  cp -a "$backup/node-updater.path" /etc/systemd/system/nodeflow-node-updater.path || rollback_failed=1
else
  rm -f /etc/systemd/system/nodeflow-node-updater.path || rollback_failed=1
fi
if ! NODEFLOW_REINSTALL_BACKUP="$backup" "$backup/restore-infrastructure.sh"; then rollback_failed=1; fi
systemctl daemon-reload || rollback_failed=1
if [ -f "$backup/was-enabled" ]; then systemctl enable nodeflow-node-agent.service >/dev/null 2>&1 || rollback_failed=1; else systemctl disable nodeflow-node-agent.service >/dev/null 2>&1 || true; fi
if [ -f "$backup/updater-path-was-enabled" ]; then systemctl enable nodeflow-node-updater.path >/dev/null 2>&1 || rollback_failed=1; else systemctl disable nodeflow-node-updater.path >/dev/null 2>&1 || true; fi
if [ -f "$backup/was-active" ] && [ -f "$backup/had-node-agent-env" ]; then
  systemctl restart nodeflow-node-agent.service || rollback_failed=1
  sleep 2
  systemctl is-active --quiet nodeflow-node-agent.service || rollback_failed=1
fi
if [ -f "$backup/updater-path-was-active" ] && [ -f "$backup/had-node-updater-path" ]; then
  systemctl start nodeflow-node-updater.path || rollback_failed=1
fi
if [ -f "$backup/legacy-agent-was-enabled" ]; then systemctl enable bridge-control-node-agent.service >/dev/null 2>&1 || rollback_failed=1; fi
if [ -f "$backup/legacy-updater-path-was-enabled" ]; then systemctl enable bridge-control-node-updater.path >/dev/null 2>&1 || rollback_failed=1; fi
if [ -f "$backup/legacy-agent-was-active" ]; then
  systemctl restart bridge-control-node-agent.service || rollback_failed=1
  sleep 2
  systemctl is-active --quiet bridge-control-node-agent.service || rollback_failed=1
fi
if [ -f "$backup/legacy-updater-path-was-active" ]; then systemctl start bridge-control-node-updater.path || rollback_failed=1; fi
if [ "$rollback_failed" -ne 0 ]; then
  echo "rollback was incomplete; backup retained at $backup" >&2
  exit 1
fi
rm -rf "$backup"
`
}

func (i *SSHInstaller) clientConfig(r Request) (*ssh.ClientConfig, error) {
	authMode := r.AuthMode
	if authMode == "" {
		authMode = AuthModePassword
	}
	var method ssh.AuthMethod
	switch authMode {
	case AuthModePassword:
		if r.Password == "" {
			return nil, errors.New("missing SSH password")
		}
		method = ssh.Password(r.Password)
	case AuthModePrivateKey:
		signer, err := parsePrivateKeySigner(r.PrivateKey, r.PrivateKeyPassphrase)
		if err != nil {
			return nil, errors.New("invalid SSH private key credentials")
		}
		method = ssh.PublicKeys(signer)
	default:
		return nil, errors.New("unsupported SSH authentication mode")
	}
	return &ssh.ClientConfig{
		User:              r.Username,
		Auth:              []ssh.AuthMethod{method},
		HostKeyCallback:   pinnedHostKey(r.HostKeySHA256),
		Timeout:           i.Timeout,
		HostKeyAlgorithms: []string{r.HostKeyAlgorithm},
	}, nil
}

func parsePrivateKeySigner(privateKey, passphrase string) (ssh.Signer, error) {
	keyBytes := []byte(privateKey)
	defer clearBytes(keyBytes)
	if passphrase == "" {
		return ssh.ParsePrivateKey(keyBytes)
	}
	passphraseBytes := []byte(passphrase)
	defer clearBytes(passphraseBytes)
	return ssh.ParsePrivateKeyWithPassphrase(keyBytes, passphraseBytes)
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

type privilegePlan struct {
	Mode     SudoMode
	Password string
}

func resolvePrivilege(r Request, root bool) (privilegePlan, error) {
	mode := r.SudoMode
	if mode == "" {
		mode = SudoModeAuto
	}
	if root {
		if mode != SudoModeAuto && mode != SudoModeRoot {
			return privilegePlan{}, errors.New("sudo_mode requires a non-root account")
		}
		return privilegePlan{Mode: SudoModeRoot}, nil
	}
	if mode == SudoModeRoot {
		return privilegePlan{}, errors.New("SSH account is not root")
	}
	if mode == SudoModeAuto {
		if r.effectiveSudoPassword() == "" {
			mode = SudoModePasswordless
		} else {
			mode = SudoModePassword
		}
	}
	switch mode {
	case SudoModePassword:
		password := r.effectiveSudoPassword()
		if password == "" {
			return privilegePlan{}, errors.New("sudo password is required")
		}
		return privilegePlan{Mode: mode, Password: password}, nil
	case SudoModePasswordless:
		return privilegePlan{Mode: mode}, nil
	default:
		return privilegePlan{}, errors.New("unsupported sudo mode")
	}
}

func verifyPrivilege(client *ssh.Client, privilege privilegePlan) error {
	switch privilege.Mode {
	case SudoModeRoot:
		return nil
	case SudoModePassword:
		return run(client, "sudo -S -p '' true", strings.NewReader(privilege.Password+"\n"))
	case SudoModePasswordless:
		return run(client, "sudo -n true", nil)
	default:
		return errors.New("invalid privilege mode")
	}
}

func pinnedHostKey(want string) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		got := ssh.FingerprintSHA256(key)
		if len(got) != len(want) || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			return errors.New("host key mismatch")
		}
		return nil
	}
}

func dialSSH(ctx context.Context, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	d := net.Dialer{Timeout: cfg.Timeout}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	stopCloseOnCancel := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stopCloseOnCancel()
	deadline := time.Time{}
	if cfg.Timeout > 0 {
		deadline = time.Now().Add(cfg.Timeout)
	}
	if contextDeadline, ok := ctx.Deadline(); ok && (deadline.IsZero() || contextDeadline.Before(deadline)) {
		deadline = contextDeadline
	}
	if !deadline.IsZero() {
		_ = c.SetDeadline(deadline)
	}
	cc, chans, reqs, err := ssh.NewClientConn(c, addr, cfg)
	if err != nil {
		c.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// The socket deadline mirrors the context deadline and can fire a few
		// microseconds before ctx.Err() is set; report it as the context error.
		if contextDeadline, ok := ctx.Deadline(); ok && deadline.Equal(contextDeadline) && errors.Is(err, os.ErrDeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}
		return nil, err
	}
	_ = c.SetDeadline(time.Time{})
	return ssh.NewClient(cc, chans, reqs), nil
}

func run(c *ssh.Client, command string, stdin io.Reader) error {
	s, err := c.NewSession()
	if err != nil {
		return err
	}
	defer s.Close()
	s.Stdin = stdin
	// Deliberately discard remote output: it may echo environment or credentials.
	return s.Run(command)
}

func inspect(c *ssh.Client) (bool, string, string, string, error) {
	s, err := c.NewSession()
	if err != nil {
		return false, "", "", "", err
	}
	defer s.Close()
	b, err := s.Output(`sh -c 'id -u; . /etc/os-release 2>/dev/null; printf "%s\n" "${ID:-unknown}"; uname -s; uname -m'`)
	if err != nil {
		return false, "", "", "", err
	}
	parts := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(parts) != 4 {
		return false, "", "", "", errors.New("invalid inspection")
	}
	root := strings.TrimSpace(parts[0]) == "0"
	distro := strings.TrimSpace(parts[1])
	operatingSystem := strings.ToLower(strings.TrimSpace(parts[2]))
	architecture := strings.ToLower(strings.TrimSpace(parts[3]))
	if operatingSystem != "linux" {
		return false, "", "", "", errors.New("unsupported operating system")
	}
	switch architecture {
	case "x86_64", "amd64":
		architecture = "amd64"
	case "aarch64", "arm64":
		architecture = "arm64"
	default:
		return false, "", "", "", errors.New("unsupported architecture")
	}
	return root, distro, operatingSystem, architecture, nil
}

func randomRemotePath() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "/tmp/nodeflow-node-agent-" + base64.RawURLEncoding.EncodeToString(b), nil
}

func installScript(tmp string, port int, token, panelURL, distro string) string {
	return installScriptWithIdentity(tmp, port, token, panelURL, distro, NodeTLSIdentity{})
}

func installScriptWithIdentity(tmp string, port int, token, panelURL, distro string, identity NodeTLSIdentity) string {
	return installScriptWithOptions(tmp, "", port, token, panelURL, distro, identity, "", false)
}

func installScriptWithOptions(tmp, updaterTmp string, port int, token, panelURL, distro string, identity NodeTLSIdentity, updatePublicKey string, allowFirewallApply bool, sshPorts ...int) string {
	return installScriptWithRelease(tmp, updaterTmp, port, token, panelURL, distro, identity, updatePublicKey, allowFirewallApply, BootstrapRelease{}, sshPorts...)
}

func installScriptWithRelease(tmp, updaterTmp string, port int, token, panelURL, distro string, identity NodeTLSIdentity, updatePublicKey string, allowFirewallApply bool, release BootstrapRelease, sshPorts ...int) string {
	apt := ""
	if distro == "ubuntu" {
		// Bootstrap converges supported Ubuntu nodes to the newest official HAProxy
		// Performance 3.4 package (AWS-LC). The candidate binary validates the
		// existing config before the package switch. Repository, package, config and
		// service state are restored if any later step fails. A newer manually
		// installed HAProxy is never downgraded. UFW activation remains a separate
		// explicit opt-in below.
		apt = `nodeflow_package_installed() {
  dpkg-query -W -f='${Status}' "$1" 2>/dev/null | grep -q 'ok installed'
}

export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq --no-upgrade ca-certificates curl

. /etc/os-release
if [ "${ID:-}" != ubuntu ]; then
  echo "HAProxy Performance bootstrap requires Ubuntu" >&2
  exit 1
fi
case "${VERSION_CODENAME:-}" in
  noble|resolute) ;;
  *)
    echo "unsupported Ubuntu codename for HAProxy Performance 3.4: ${VERSION_CODENAME:-unknown}" >&2
    exit 1
    ;;
esac

nodeflow_haproxy_repo=/etc/apt/sources.list.d/haproxy-performance-ha34.list
nodeflow_haproxy_key=/usr/share/keyrings/HAPROXY-key-community.asc
nodeflow_haproxy_txn=$(mktemp -d)
chmod 0700 "$nodeflow_haproxy_txn"
if [ -z "${NODEFLOW_REINSTALL_BACKUP:-}" ]; then
  trap 'rm -rf "$nodeflow_haproxy_txn"' EXIT HUP INT TERM
fi
install -d -m 0700 "$nodeflow_haproxy_txn/old-packages"

if [ -f "$nodeflow_haproxy_repo" ]; then
  cp -a "$nodeflow_haproxy_repo" "$nodeflow_haproxy_txn/repository.list"
  : > "$nodeflow_haproxy_txn/had-repository"
fi
if [ -f "$nodeflow_haproxy_key" ]; then
  cp -a "$nodeflow_haproxy_key" "$nodeflow_haproxy_txn/repository-key.asc"
  : > "$nodeflow_haproxy_txn/had-repository-key"
fi
if [ -d /etc/haproxy ]; then
  tar -C / -czf "$nodeflow_haproxy_txn/haproxy-config.tar.gz" etc/haproxy
  : > "$nodeflow_haproxy_txn/had-config"
fi
if command -v systemctl >/dev/null 2>&1; then
  if systemctl is-active --quiet haproxy.service; then : > "$nodeflow_haproxy_txn/was-active"; fi
  if systemctl is-enabled --quiet haproxy.service; then : > "$nodeflow_haproxy_txn/was-enabled"; fi
fi

nodeflow_haproxy_committed=0
nodeflow_haproxy_repository_changed=0
nodeflow_haproxy_package_changed=0

nodeflow_restore_haproxy_repository() {
  if [ "$nodeflow_haproxy_repository_changed" -ne 1 ]; then return; fi
  rm -f "$nodeflow_haproxy_repo.new" "$nodeflow_haproxy_key.new"
  if [ -f "$nodeflow_haproxy_txn/had-repository" ]; then
    cp -a "$nodeflow_haproxy_txn/repository.list" "$nodeflow_haproxy_repo"
  else
    rm -f "$nodeflow_haproxy_repo"
  fi
  if [ -f "$nodeflow_haproxy_txn/had-repository-key" ]; then
    cp -a "$nodeflow_haproxy_txn/repository-key.asc" "$nodeflow_haproxy_key"
  else
    rm -f "$nodeflow_haproxy_key"
  fi
  nodeflow_haproxy_repository_changed=0
}

nodeflow_rollback_haproxy() {
  set +e
  nodeflow_restore_haproxy_repository
  apt-get update -qq
  if [ "$nodeflow_haproxy_package_changed" -eq 1 ]; then
    nodeflow_haproxy_remove_packages="haproxy-awslc haproxy"
    if [ -f "$nodeflow_haproxy_txn/had-haproxy-awslc" ]; then
      nodeflow_haproxy_remove_packages="$nodeflow_haproxy_remove_packages libssl-awslc"
    fi
    apt-get remove -y -qq $nodeflow_haproxy_remove_packages >/dev/null 2>&1 || true
    nodeflow_haproxy_old_debs=$(find "$nodeflow_haproxy_txn/old-packages" -maxdepth 1 -type f -name '*.deb' -print)
    if [ -n "$nodeflow_haproxy_old_debs" ]; then
      apt-get install -y -qq --allow-downgrades -o Dpkg::Options::=--force-confold $nodeflow_haproxy_old_debs || dpkg -i $nodeflow_haproxy_old_debs
    fi
    rm -rf /etc/haproxy
    if [ -f "$nodeflow_haproxy_txn/had-config" ]; then
      tar -C / -xzf "$nodeflow_haproxy_txn/haproxy-config.tar.gz"
    fi
    if command -v systemctl >/dev/null 2>&1; then
      systemctl daemon-reload
      if [ -f "$nodeflow_haproxy_txn/was-enabled" ]; then systemctl enable haproxy.service >/dev/null 2>&1; else systemctl disable haproxy.service >/dev/null 2>&1 || true; fi
      if [ -f "$nodeflow_haproxy_txn/was-active" ]; then systemctl restart haproxy.service; else systemctl stop haproxy.service >/dev/null 2>&1 || true; fi
    fi
  fi
}

nodeflow_haproxy_cleanup() {
  nodeflow_haproxy_status=$?
  trap - EXIT HUP INT TERM
  if [ "$nodeflow_haproxy_committed" -ne 1 ]; then nodeflow_rollback_haproxy; fi
  rm -rf "$nodeflow_haproxy_txn"
  exit "$nodeflow_haproxy_status"
}
if [ -z "${NODEFLOW_REINSTALL_BACKUP:-}" ]; then
  trap 'exit 1' HUP INT TERM
  trap nodeflow_haproxy_cleanup EXIT
fi

install -d -m 0755 /usr/share/keyrings /etc/apt/sources.list.d
nodeflow_haproxy_repository_changed=1
curl -fsSL --connect-timeout 5 --max-time 30 https://pks.haproxy.com/linux/community/RPM-GPG-KEY-HAProxy > "$nodeflow_haproxy_key.new"
chmod 0644 "$nodeflow_haproxy_key.new"
mv -f "$nodeflow_haproxy_key.new" "$nodeflow_haproxy_key"
printf '%s\n' "deb [signed-by=$nodeflow_haproxy_key] https://www.haproxy.com/download/haproxy/performance/ubuntu/ha34 ${VERSION_CODENAME} main" > "$nodeflow_haproxy_repo.new"
chmod 0644 "$nodeflow_haproxy_repo.new"
mv -f "$nodeflow_haproxy_repo.new" "$nodeflow_haproxy_repo"
apt-get update -qq

# Add the target repository before creating rollback artifacts. A node may
# retain libssl-awslc after haproxy-awslc was removed; without this repository
# APT knows the installed version but has no source from which to download the
# exact rollback package and exits with code 100.
: > "$nodeflow_haproxy_txn/old-package-versions"
if nodeflow_package_installed haproxy-awslc; then
  : > "$nodeflow_haproxy_txn/had-haproxy-awslc"
fi
for nodeflow_haproxy_old_package in haproxy haproxy-awslc libssl-awslc; do
  if ! nodeflow_package_installed "$nodeflow_haproxy_old_package"; then
    continue
  fi
  if [ "$nodeflow_haproxy_old_package" = libssl-awslc ] && [ ! -f "$nodeflow_haproxy_txn/had-haproxy-awslc" ]; then
    continue
  fi
  case "$nodeflow_haproxy_old_package" in haproxy|haproxy-awslc) : > "$nodeflow_haproxy_txn/had-managed-haproxy" ;; esac
  nodeflow_haproxy_old_version=$(dpkg-query -W -f='${Version}' "$nodeflow_haproxy_old_package")
  printf '%s=%s\n' "$nodeflow_haproxy_old_package" "$nodeflow_haproxy_old_version" >> "$nodeflow_haproxy_txn/old-package-versions"
  (cd "$nodeflow_haproxy_txn/old-packages" && apt-get download "$nodeflow_haproxy_old_package=$nodeflow_haproxy_old_version" >/dev/null)
  find "$nodeflow_haproxy_txn/old-packages" -maxdepth 1 -type f -name "${nodeflow_haproxy_old_package}_*.deb" -print -quit | grep -q .
done

nodeflow_haproxy_target_version=$(apt-cache madison haproxy-awslc | awk '$3 ~ /^([0-9]+:)?3[.]4([.-]|$)/ { print $3; exit }')
if [ -z "$nodeflow_haproxy_target_version" ]; then
  echo "official HAProxy Performance repository has no haproxy-awslc 3.4 candidate" >&2
  exit 71
fi

install -d -m 0755 "$nodeflow_haproxy_txn/candidate"
(cd "$nodeflow_haproxy_txn/candidate" && apt-get download "haproxy-awslc=$nodeflow_haproxy_target_version" >/dev/null)
nodeflow_haproxy_candidate_deb=$(find "$nodeflow_haproxy_txn/candidate" -maxdepth 1 -type f -name 'haproxy-awslc_*.deb' -print -quit)
test -n "$nodeflow_haproxy_candidate_deb"
dpkg-deb -x "$nodeflow_haproxy_candidate_deb" "$nodeflow_haproxy_txn/candidate/root"
nodeflow_haproxy_candidate_binary="$nodeflow_haproxy_txn/candidate/root/usr/sbin/haproxy"
test -x "$nodeflow_haproxy_candidate_binary"
# Validate the candidate without changing the installed package set. Fresh
# nodes do not yet have all of haproxy-awslc's runtime dependencies (notably
# Lua and OpenTracing), so download the complete missing dependency closure and
# expose the extracted libraries only to the candidate process.
nodeflow_haproxy_candidate_depends=$(dpkg-deb -f "$nodeflow_haproxy_candidate_deb" Depends)
test -n "$nodeflow_haproxy_candidate_depends"
install -d -m 0755 "$nodeflow_haproxy_txn/candidate/dependencies/partial"
apt-get satisfy -y -qq --download-only --no-install-recommends \
  -o Dir::Cache::archives="$nodeflow_haproxy_txn/candidate/dependencies/" \
  "$nodeflow_haproxy_candidate_depends" || exit 72
find "$nodeflow_haproxy_txn/candidate/dependencies" -maxdepth 1 -type f -name '*.deb' -exec dpkg-deb -x '{}' "$nodeflow_haproxy_txn/candidate/root" ';'
nodeflow_haproxy_candidate_lib=$(find "$nodeflow_haproxy_txn/candidate/root" \( -type f -o -type l \) -name '*.so*' -printf '%h\n' | sort -u | paste -sd: -)
nodeflow_run_haproxy_candidate() {
  if [ -n "$nodeflow_haproxy_candidate_lib" ]; then
    LD_LIBRARY_PATH="$nodeflow_haproxy_candidate_lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}" "$nodeflow_haproxy_candidate_binary" "$@"
  else
    "$nodeflow_haproxy_candidate_binary" "$@"
  fi
}
nodeflow_haproxy_candidate_runtime=$(nodeflow_run_haproxy_candidate -v | awk '/^HAProxy version / { print $3; exit }') || exit 74
test -n "$nodeflow_haproxy_candidate_runtime" || exit 74

nodeflow_haproxy_current_runtime=
if command -v haproxy >/dev/null 2>&1; then
  nodeflow_haproxy_current_runtime=$(haproxy -v | awk '/^HAProxy version / { print $3; exit }')
  test -n "$nodeflow_haproxy_current_runtime"
fi
if [ -f /etc/haproxy/haproxy.cfg ]; then
  if command -v haproxy >/dev/null 2>&1; then haproxy -c -f /etc/haproxy/haproxy.cfg; fi
fi

if [ -n "$nodeflow_haproxy_current_runtime" ] && dpkg --compare-versions "$nodeflow_haproxy_current_runtime" ge "$nodeflow_haproxy_candidate_runtime"; then
  nodeflow_restore_haproxy_repository
  apt-get update -qq
  nodeflow_haproxy_committed=1
else
  if [ -f /etc/haproxy/haproxy.cfg ]; then
    nodeflow_run_haproxy_candidate -c -f /etc/haproxy/haproxy.cfg || exit 75
  fi
  if [ -n "$nodeflow_haproxy_current_runtime" ] && [ ! -f "$nodeflow_haproxy_txn/had-managed-haproxy" ]; then
    echo "refusing to replace an unmanaged HAProxy installation without a package rollback artifact" >&2
    exit 1
  fi
  nodeflow_haproxy_installed_awslc=
  if nodeflow_package_installed haproxy-awslc; then
    nodeflow_haproxy_installed_awslc=$(dpkg-query -W -f='${Version}' haproxy-awslc)
  fi
  if [ -z "$nodeflow_haproxy_installed_awslc" ] || dpkg --compare-versions "$nodeflow_haproxy_installed_awslc" lt "$nodeflow_haproxy_target_version"; then
    if [ -f "$nodeflow_haproxy_txn/had-config" ]; then
      install -d -m 0700 /var/backups/nodeflow
      cp "$nodeflow_haproxy_txn/haproxy-config.tar.gz" "/var/backups/nodeflow/haproxy-config-$(date +%Y%m%d%H%M%S)-$$.tar.gz"
    fi
    nodeflow_haproxy_package_changed=1
    nodeflow_haproxy_conflict_remove=
    if nodeflow_package_installed haproxy && ! nodeflow_package_installed haproxy-awslc; then
      nodeflow_haproxy_conflict_remove=haproxy-
    fi
    apt-get install -y -qq -o Dpkg::Options::=--force-confold \
      $nodeflow_haproxy_conflict_remove "haproxy-awslc=$nodeflow_haproxy_target_version"
  fi
  haproxy -c -f /etc/haproxy/haproxy.cfg
  if command -v systemctl >/dev/null 2>&1; then
    if systemctl is-active --quiet haproxy.service; then
      systemctl reload haproxy.service || systemctl restart haproxy.service
    else
      systemctl restart haproxy.service
    fi
    systemctl is-active --quiet haproxy.service
  fi
  nodeflow_haproxy_committed=1
fi
if [ -z "${NODEFLOW_REINSTALL_BACKUP:-}" ]; then
  trap - EXIT HUP INT TERM
fi
rm -rf "$nodeflow_haproxy_txn"

if ! nodeflow_package_installed ufw || [ ! -f /etc/default/ufw ] || [ ! -f /etc/ufw/ufw.conf ]; then
  # --force-confmiss is required after an interrupted first install/rollback:
  # dpkg otherwise keeps deleted UFW conffiles deleted even with --reinstall.
  apt-get install -y -qq --reinstall -o Dpkg::Options::=--force-confold -o Dpkg::Options::=--force-confmiss ufw
fi
test -f /etc/default/ufw
test -f /etc/ufw/ufw.conf
command -v ufw >/dev/null 2>&1
ufw status >/dev/null
`
	} else if distro == "debian" {
		apt = `nodeflow_missing_packages=
for nodeflow_package in haproxy ufw; do
  if ! dpkg-query -W -f='${Status}' "$nodeflow_package" 2>/dev/null | grep -q 'ok installed'; then
    nodeflow_missing_packages="$nodeflow_missing_packages $nodeflow_package"
  fi
done
if [ -n "$nodeflow_missing_packages" ]; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq --no-upgrade $nodeflow_missing_packages
fi
if [ ! -f /etc/default/ufw ] || [ ! -f /etc/ufw/ufw.conf ]; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq --reinstall -o Dpkg::Options::=--force-confold -o Dpkg::Options::=--force-confmiss ufw
fi
test -f /etc/default/ufw
test -f /etc/ufw/ufw.conf
command -v ufw >/dev/null 2>&1
ufw status >/dev/null
`
	}
	tlsFiles := ""
	tlsEnvironment := ""
	if len(identity.CACertificatePEM) != 0 || len(identity.CertificatePEM) != 0 || len(identity.PrivateKeyPEM) != 0 {
		tlsFiles = fmt.Sprintf(`install -d -m 0750 /etc/nodeflow/tls
cat > /etc/nodeflow/tls/ca.crt <<'NODEFLOW_CA_CERT'
%sNODEFLOW_CA_CERT
cat > /etc/nodeflow/tls/node.crt <<'NODEFLOW_NODE_CERT'
%sNODEFLOW_NODE_CERT
cat > /etc/nodeflow/tls/node.key <<'NODEFLOW_NODE_KEY'
%sNODEFLOW_NODE_KEY
chmod 0644 /etc/nodeflow/tls/ca.crt /etc/nodeflow/tls/node.crt
chmod 0600 /etc/nodeflow/tls/node.key
`, identity.CACertificatePEM, identity.CertificatePEM, identity.PrivateKeyPEM)
		tlsEnvironment = `NODE_AGENT_PANEL_TLS_CA=/etc/nodeflow/tls/ca.crt
NODE_AGENT_PANEL_TLS_CERT=/etc/nodeflow/tls/node.crt
NODE_AGENT_PANEL_TLS_KEY=/etc/nodeflow/tls/node.key
`
		if identity.ServerName != "" {
			tlsEnvironment += "NODE_AGENT_PANEL_TLS_SERVER_NAME=" + identity.ServerName + "\n"
		}
	}
	updateEnvironment := "NODE_AGENT_SELF_UPDATE_MODE=off\n"
	initialUpdateState := ""
	updateDirectories := "install -d -m 0700 /var/lib/nodeflow /var/lib/nodeflow/updates /var/lib/nodeflow/credentials /var/lib/nodeflow-updater\n"
	updaterInstall := ""
	updaterUnit := ""
	updaterActivation := ""
	updaterEnvironment := ""
	capabilityBoundingSet := ""
	restrictAddressFamilies := "AF_UNIX AF_INET AF_INET6"
	ufwWritePath := ""
	firewallBootstrap := ""
	if updatePublicKey != "" {
		updateEnvironment = "NODE_AGENT_SELF_UPDATE_MODE=apply\nNODE_AGENT_UPDATE_PUBLIC_KEY=" + updatePublicKey + "\n"
		if release.Sequence > 0 {
			updateEnvironment += fmt.Sprintf("NODE_AGENT_UPDATE_SEQUENCE=%d\n", release.Sequence)
			initialUpdateState = fmt.Sprintf(`cat > /var/lib/nodeflow-updater/state.json <<'EOF'
{"version":%q,"sequence":%d,"sha256":%q,"installed_at":%q}
EOF
chmod 0600 /var/lib/nodeflow-updater/state.json
`, release.Version, release.Sequence, release.SHA256, time.Now().UTC().Format(time.RFC3339Nano))
		}
		updaterEnvironment = fmt.Sprintf(`umask 077
cat > /etc/nodeflow/node-updater.env <<'EOF'
NODE_UPDATER_PUBLIC_KEY=%s
NODE_UPDATER_STAGING_DIR=/var/lib/nodeflow/updates
NODE_UPDATER_PENDING_FILE=/var/lib/nodeflow/updates/pending.json
NODE_UPDATER_STATE_FILE=/var/lib/nodeflow-updater/state.json
NODE_UPDATER_RESULT_FILE=/var/lib/nodeflow/updates/result.json
NODE_UPDATER_ACTIVATION_FILE=/var/lib/nodeflow-updater/activation.json
NODE_UPDATER_LOCK_FILE=/run/nodeflow-node-updater/lock
NODE_UPDATER_HEALTH_URL=http://127.0.0.1:%d/v1/health
EOF
chmod 0600 /etc/nodeflow/node-updater.env
`, updatePublicKey, port)
		updaterInstall = "install -d -m 0755 /usr/local/libexec\ninstall -m 0755 " + updaterTmp + " /usr/local/libexec/nodeflow-node-updater.new\nmv -f /usr/local/libexec/nodeflow-node-updater.new /usr/local/libexec/nodeflow-node-updater\n"
		updaterUnit = `cat > /etc/systemd/system/nodeflow-node-updater.service <<'EOF'
[Unit]
Description=NodeFlow Node Agent Update Activator
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
EnvironmentFile=/etc/nodeflow/node-updater.env
ExecStart=/usr/local/libexec/nodeflow-node-updater
User=root
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
LockPersonality=true
RestrictSUIDSGID=true
RestrictNamespaces=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
MemoryDenyWriteExecute=true
PrivateDevices=true
CapabilityBoundingSet=
RuntimeDirectory=nodeflow-node-updater
RuntimeDirectoryMode=0700
ReadWritePaths=/usr/local/bin /var/lib/nodeflow/updates /var/lib/nodeflow-updater
EOF
cat > /etc/systemd/system/nodeflow-node-updater.path <<'EOF'
[Unit]
Description=Watch for pending NodeFlow Agent updates

[Path]
PathExists=/var/lib/nodeflow/updates/pending.json
PathExists=/var/lib/nodeflow-updater/activation.json
Unit=nodeflow-node-updater.service
TriggerLimitIntervalSec=60
TriggerLimitBurst=3

[Install]
WantedBy=multi-user.target
EOF
`
		updaterActivation = "systemctl enable --now nodeflow-node-updater.path\n"
	}
	firewallMode := "observe"
	if allowFirewallApply {
		fallbackSSHPort := 22
		if len(sshPorts) > 0 && sshPorts[0] >= 1 && sshPorts[0] <= 65535 {
			fallbackSSHPort = sshPorts[0]
		}
		firewallMode = "apply"
		capabilityBoundingSet = "CAP_NET_ADMIN CAP_NET_RAW"
		restrictAddressFamilies += " AF_NETLINK"
		ufwWritePath = " -/etc/ufw"
		firewallBootstrap = fmt.Sprintf(`nodeflow_ssh_port=${NODEFLOW_SSH_PORT:-%d}
case "$nodeflow_ssh_port" in
  ''|*[!0-9]*) echo "cannot safely determine the SSH server port for UFW" >&2; exit 1 ;;
esac
if [ "$nodeflow_ssh_port" -lt 1 ] || [ "$nodeflow_ssh_port" -gt 65535 ]; then
  echo "invalid SSH server port for UFW" >&2
  exit 1
fi
command -v ufw >/dev/null 2>&1 || { echo "ufw is required for firewall apply mode" >&2; exit 1; }
test -f /etc/default/ufw
test -f /etc/ufw/ufw.conf
ufw status >/dev/null
# This bootstrap-only tag is deliberately different from the exact
# nodeflow listener tag managed by Node Agent.
ufw allow "$nodeflow_ssh_port/tcp" comment nodeflow-ssh
if ! ufw status | grep -qx 'Status: active'; then
  ufw --force enable
fi
ufw status | grep -qx 'Status: active'
`, fallbackSSHPort)
	}
	return fmt.Sprintf(`set -eu
	%sinstall -m 0755 %s /usr/local/bin/nodeflow-node-agent.new
	mv -f /usr/local/bin/nodeflow-node-agent.new /usr/local/bin/nodeflow-node-agent
%s
install -d -m 0750 /etc/nodeflow
%s
%s
umask 077
cat > /etc/nodeflow/node-agent.env <<'EOF'
NODE_AGENT_LISTEN=127.0.0.1:%d
NODE_AGENT_ALLOW_REMOTE_LISTEN=false
NODE_AGENT_TOKEN=%s
NODE_AGENT_PANEL_URL=%s
%sNODE_AGENT_HEARTBEAT_INTERVAL=15s
NODE_AGENT_RECONCILE_TIMEOUT=45s
NODE_AGENT_CREDENTIAL_RENEWAL_MODE=apply
NODE_AGENT_CREDENTIAL_STATE_DIR=/var/lib/nodeflow/credentials
NODE_AGENT_FIREWALL_MODE=%s
	%sNODE_AGENT_HAPROXY_CONFIG=/etc/haproxy/haproxy.cfg
EOF
	%s
	%s
cat > /etc/systemd/system/nodeflow-node-agent.service <<'EOF'
[Unit]
Description=NodeFlow Node Agent
After=network-online.target haproxy.service
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/nodeflow/node-agent.env
ExecStart=/usr/local/bin/nodeflow-node-agent
Restart=on-failure
RestartSec=3s
User=root
UMask=0027
Nice=10
CPUWeight=20
IOWeight=20
TasksMax=64
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
LockPersonality=true
RestrictSUIDSGID=true
RestrictNamespaces=true
RestrictAddressFamilies=%s
MemoryDenyWriteExecute=true
PrivateDevices=true
CapabilityBoundingSet=%s
ReadWritePaths=/etc/haproxy /var/lib/nodeflow/updates /var/lib/nodeflow/credentials -/etc/sysctl.d -/proc/sys/fs/pipe-max-size -/proc/sys/fs/pipe-user-pages-soft%s

[Install]
WantedBy=multi-user.target
EOF
%s
	chmod 0600 /etc/nodeflow/node-agent.env
		systemctl daemon-reload
		systemctl enable nodeflow-node-agent.service
	%s
			systemctl restart nodeflow-node-agent.service
		sleep 2
		systemctl is-active --quiet nodeflow-node-agent.service
	%s
	rm -f -- %s
	`, apt, tmp, updaterInstall, tlsFiles, updateDirectories, port, token, panelURL, tlsEnvironment, firewallMode, updateEnvironment, initialUpdateState, updaterEnvironment, restrictAddressFamilies, capabilityBoundingSet, ufwWritePath, updaterUnit, firewallBootstrap, updaterActivation, tmp)
}
