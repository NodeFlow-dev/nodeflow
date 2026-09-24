#!/bin/sh
set -eu

public_key=${1:?usage: install-node-updater.sh ed25519-public-key agent-binary updater-binary}
agent_source=${2:?usage: install-node-updater.sh ed25519-public-key agent-binary updater-binary}
updater_source=${3:?usage: install-node-updater.sh ed25519-public-key agent-binary updater-binary}

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root" >&2
  exit 2
fi
case "$public_key" in
  *[!A-Za-z0-9+/=]*|'')
    echo "invalid update public key encoding" >&2
    exit 2
    ;;
esac
for path in "$agent_source" "$updater_source"; do
  if [ ! -f "$path" ]; then
    echo "staged binary is missing: $path" >&2
    exit 1
  fi
done

install -d -m 0700 /var/backups/nodeflow-node
backup_dir=$(mktemp -d /var/backups/nodeflow-node/pre-updater-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX)
chmod 0700 "$backup_dir"
cp -a /usr/local/bin/nodeflow-node-agent "$backup_dir/node-agent"
cp -a /etc/nodeflow/node-agent.env "$backup_dir/node-agent.env"
cp -a /etc/systemd/system/nodeflow-node-agent.service "$backup_dir/node-agent.service"
if [ -f /usr/local/libexec/nodeflow-node-updater ]; then
  cp -a /usr/local/libexec/nodeflow-node-updater "$backup_dir/node-updater"
  : > "$backup_dir/had-updater"
fi
if [ -f /etc/systemd/system/nodeflow-node-updater.service ]; then
  cp -a /etc/systemd/system/nodeflow-node-updater.service "$backup_dir/node-updater.service"
  : > "$backup_dir/had-updater-unit"
fi
if [ -f /etc/systemd/system/nodeflow-node-updater.path ]; then
  cp -a /etc/systemd/system/nodeflow-node-updater.path "$backup_dir/node-updater.path"
  : > "$backup_dir/had-updater-path"
fi
if [ -f /etc/nodeflow/node-updater.env ]; then
  cp -a /etc/nodeflow/node-updater.env "$backup_dir/node-updater.env"
  : > "$backup_dir/had-updater-env"
fi
if [ -f /var/lib/nodeflow-updater/state.json ]; then
  cp -a /var/lib/nodeflow-updater/state.json "$backup_dir/updater-state.json"
  : > "$backup_dir/had-updater-state"
fi
if systemctl is-enabled --quiet nodeflow-node-updater.path; then
  : > "$backup_dir/updater-path-was-enabled"
fi
if systemctl is-active --quiet nodeflow-node-updater.path; then
  : > "$backup_dir/updater-path-was-active"
fi

rollback() {
  set +e
  rollback_failed=0
  install -m 0755 "$backup_dir/node-agent" /usr/local/bin/nodeflow-node-agent.rollback || rollback_failed=1
  mv -f /usr/local/bin/nodeflow-node-agent.rollback /usr/local/bin/nodeflow-node-agent || rollback_failed=1
  cp -a "$backup_dir/node-agent.env" /etc/nodeflow/node-agent.env || rollback_failed=1
  cp -a "$backup_dir/node-agent.service" /etc/systemd/system/nodeflow-node-agent.service || rollback_failed=1
  if [ -f "$backup_dir/had-updater" ]; then
    install -d -m 0755 /usr/local/libexec || rollback_failed=1
    cp -a "$backup_dir/node-updater" /usr/local/libexec/nodeflow-node-updater || rollback_failed=1
  else
    rm -f /usr/local/libexec/nodeflow-node-updater || rollback_failed=1
  fi
  if [ -f "$backup_dir/had-updater-unit" ]; then
    cp -a "$backup_dir/node-updater.service" /etc/systemd/system/nodeflow-node-updater.service || rollback_failed=1
  else
    rm -f /etc/systemd/system/nodeflow-node-updater.service || rollback_failed=1
  fi
  if [ -f "$backup_dir/had-updater-path" ]; then
    cp -a "$backup_dir/node-updater.path" /etc/systemd/system/nodeflow-node-updater.path || rollback_failed=1
  else
    systemctl disable --now nodeflow-node-updater.path >/dev/null 2>&1 || true
    rm -f /etc/systemd/system/nodeflow-node-updater.path || rollback_failed=1
  fi
  if [ -f "$backup_dir/had-updater-env" ]; then
    cp -a "$backup_dir/node-updater.env" /etc/nodeflow/node-updater.env || rollback_failed=1
  else
    rm -f /etc/nodeflow/node-updater.env || rollback_failed=1
  fi
  if [ -f "$backup_dir/had-updater-state" ]; then
    install -d -m 0700 /var/lib/nodeflow-updater || rollback_failed=1
    cp -a "$backup_dir/updater-state.json" /var/lib/nodeflow-updater/state.json || rollback_failed=1
  else
    rm -f /var/lib/nodeflow-updater/state.json || rollback_failed=1
  fi
  systemctl daemon-reload || rollback_failed=1
  if [ -f "$backup_dir/updater-path-was-enabled" ]; then
    systemctl enable nodeflow-node-updater.path >/dev/null 2>&1 || rollback_failed=1
  else
    systemctl disable nodeflow-node-updater.path >/dev/null 2>&1 || true
  fi
  if [ -f "$backup_dir/updater-path-was-active" ]; then
    systemctl start nodeflow-node-updater.path || rollback_failed=1
  else
    systemctl stop nodeflow-node-updater.path >/dev/null 2>&1 || true
  fi
  systemctl restart nodeflow-node-agent.service || true
  if [ "$rollback_failed" -ne 0 ]; then
    echo "rollback was incomplete; backup retained at $backup_dir" >&2
    return 1
  fi
}

completed=false
trap 'exit 1' HUP INT TERM
trap 'if [ "$completed" != true ]; then rollback; fi' EXIT

install -d -m 0700 /var/lib/nodeflow /var/lib/nodeflow/updates /var/lib/nodeflow/credentials /var/lib/nodeflow-updater
if [ -f /var/lib/nodeflow/updates/state.json ] && [ ! -e /var/lib/nodeflow-updater/state.json ]; then
  install -m 0600 /var/lib/nodeflow/updates/state.json /var/lib/nodeflow-updater/state.json
fi
install -d -m 0755 /usr/local/libexec
install -m 0755 "$updater_source" /usr/local/libexec/nodeflow-node-updater.new
mv -f /usr/local/libexec/nodeflow-node-updater.new /usr/local/libexec/nodeflow-node-updater

environment_file=/etc/nodeflow/node-agent.env
environment_tmp=$(mktemp /etc/nodeflow/node-agent.env.XXXXXX)
awk '!/^(NODE_AGENT_SELF_UPDATE_MODE|NODE_AGENT_UPDATE_PUBLIC_KEY|NODE_AGENT_UPDATE_STAGING_DIR|NODE_AGENT_UPDATE_STATE_FILE|NODE_AGENT_UPDATE_PENDING_FILE|NODE_AGENT_UPDATE_RESULT_FILE|NODE_AGENT_UPDATE_HELPER_SERVICE|NODE_AGENT_CREDENTIAL_RENEWAL_MODE|NODE_AGENT_CREDENTIAL_STATE_DIR)=/' "$environment_file" > "$environment_tmp"
printf '%s\n' \
  'NODE_AGENT_SELF_UPDATE_MODE=apply' \
  "NODE_AGENT_UPDATE_PUBLIC_KEY=$public_key" \
  'NODE_AGENT_UPDATE_STAGING_DIR=/var/lib/nodeflow/updates' \
  'NODE_AGENT_UPDATE_STATE_FILE=/var/lib/nodeflow-updater/state.json' \
  'NODE_AGENT_UPDATE_PENDING_FILE=/var/lib/nodeflow/updates/pending.json' \
  'NODE_AGENT_UPDATE_RESULT_FILE=/var/lib/nodeflow/updates/result.json' \
  'NODE_AGENT_UPDATE_HELPER_SERVICE=nodeflow-node-updater.service' \
  'NODE_AGENT_CREDENTIAL_RENEWAL_MODE=apply' \
  'NODE_AGENT_CREDENTIAL_STATE_DIR=/var/lib/nodeflow/credentials' >> "$environment_tmp"
chown --reference="$environment_file" "$environment_tmp"
chmod --reference="$environment_file" "$environment_tmp"
mv -f "$environment_tmp" "$environment_file"

agent_listen=$(awk -F= '$1 == "NODE_AGENT_LISTEN" { value=$2 } END { print value }' "$environment_file")
case "$agent_listen" in
  127.0.0.1:[0-9]*|'[::1]:'[0-9]*) ;;
  *) agent_listen=127.0.0.1:4200 ;;
esac
updater_environment=/etc/nodeflow/node-updater.env
updater_environment_tmp=$(mktemp /etc/nodeflow/node-updater.env.XXXXXX)
cat > "$updater_environment_tmp" <<EOF
NODE_UPDATER_PUBLIC_KEY=$public_key
NODE_UPDATER_STAGING_DIR=/var/lib/nodeflow/updates
NODE_UPDATER_PENDING_FILE=/var/lib/nodeflow/updates/pending.json
NODE_UPDATER_STATE_FILE=/var/lib/nodeflow-updater/state.json
NODE_UPDATER_RESULT_FILE=/var/lib/nodeflow/updates/result.json
NODE_UPDATER_ACTIVATION_FILE=/var/lib/nodeflow-updater/activation.json
NODE_UPDATER_LOCK_FILE=/run/nodeflow-node-updater/lock
NODE_UPDATER_HEALTH_URL=http://$agent_listen/v1/health
EOF
chmod 0600 "$updater_environment_tmp"
mv -f "$updater_environment_tmp" "$updater_environment"

firewall_mode=$(awk -F= '$1 == "NODE_AGENT_FIREWALL_MODE" { value=$2 } END { print value }' "$environment_file")
capability_bounding_set=""
ufw_write_path=""
if [ "$firewall_mode" = apply ]; then
  capability_bounding_set="CAP_NET_ADMIN CAP_NET_RAW"
  ufw_write_path=" -/etc/ufw"
fi

cat > /etc/systemd/system/nodeflow-node-agent.service <<EOF
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
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
MemoryDenyWriteExecute=true
PrivateDevices=true
CapabilityBoundingSet=$capability_bounding_set
ReadWritePaths=/etc/haproxy /var/lib/nodeflow/updates /var/lib/nodeflow/credentials -/etc/sysctl.d -/proc/sys/fs/pipe-max-size -/proc/sys/fs/pipe-user-pages-soft$ufw_write_path

[Install]
WantedBy=multi-user.target
EOF
cat > /etc/systemd/system/nodeflow-node-updater.service <<'EOF'
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

install -m 0755 "$agent_source" /usr/local/bin/nodeflow-node-agent.new
mv -f /usr/local/bin/nodeflow-node-agent.new /usr/local/bin/nodeflow-node-agent
systemctl daemon-reload
systemctl restart nodeflow-node-agent.service
sleep 2
systemctl is-active --quiet nodeflow-node-agent.service
systemctl enable --now nodeflow-node-updater.path

completed=true
trap - EXIT HUP INT TERM
rm -f "$agent_source" "$updater_source"
printf 'backup=%s\n' "$backup_dir"
printf 'agent_sha256='
sha256sum /usr/local/bin/nodeflow-node-agent | cut -d' ' -f1
printf 'updater_sha256='
sha256sum /usr/local/libexec/nodeflow-node-updater | cut -d' ' -f1
