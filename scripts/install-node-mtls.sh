#!/bin/sh
set -eu

node_id=${1:?usage: install-node-mtls.sh node-uuid panel-url ca-cert node-cert node-key agent-binary}
panel_url=${2:?usage: install-node-mtls.sh node-uuid panel-url ca-cert node-cert node-key agent-binary}
ca_source=${3:?usage: install-node-mtls.sh node-uuid panel-url ca-cert node-cert node-key agent-binary}
certificate_source=${4:?usage: install-node-mtls.sh node-uuid panel-url ca-cert node-cert node-key agent-binary}
key_source=${5:?usage: install-node-mtls.sh node-uuid panel-url ca-cert node-cert node-key agent-binary}
binary_source=${6:?usage: install-node-mtls.sh node-uuid panel-url ca-cert node-cert node-key agent-binary}

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root" >&2
  exit 2
fi
case "$panel_url" in
  https://*[!A-Za-z0-9:./_-]*)
    echo "Panel URL contains unsupported characters" >&2
    exit 2
    ;;
  https://*) ;;
  *)
    echo "Panel URL must use https" >&2
    exit 2
    ;;
esac

for path in "$ca_source" "$certificate_source" "$key_source" "$binary_source"; do
  if [ ! -f "$path" ]; then
    echo "staged input is missing: $path" >&2
    exit 1
  fi
done

openssl verify -CAfile "$ca_source" -purpose sslclient "$certificate_source" >/dev/null
certificate_node_id=$(openssl x509 -in "$certificate_source" -noout -subject -nameopt RFC2253 | sed -n 's/.*CN=\([^,]*\).*/\1/p')
if [ "$certificate_node_id" != "$node_id" ]; then
  echo "node certificate identity mismatch" >&2
  exit 1
fi

backup_root=/var/backups/nodeflow-node
install -d -m 0700 "$backup_root"
backup_dir=$(mktemp -d "$backup_root/pre-mtls-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX")
chmod 0700 "$backup_dir"
cp -a /usr/local/bin/nodeflow-node-agent "$backup_dir/node-agent"
cp -a /etc/nodeflow/node-agent.env "$backup_dir/node-agent.env"
if [ -d /etc/nodeflow/tls ]; then
  cp -a /etc/nodeflow/tls "$backup_dir/tls"
  : > "$backup_dir/had-tls"
fi
if [ -d /var/lib/nodeflow/credentials ]; then
  cp -a /var/lib/nodeflow/credentials "$backup_dir/credentials"
  : > "$backup_dir/had-credentials"
fi
if [ -f /etc/systemd/system/nodeflow-node-agent.service.d/credentials.conf ]; then
  cp -a /etc/systemd/system/nodeflow-node-agent.service.d/credentials.conf "$backup_dir/credentials.conf"
  : > "$backup_dir/had-credentials-dropin"
fi

rollback() {
  set +e
  rollback_failed=0
  install -m 0755 "$backup_dir/node-agent" /usr/local/bin/nodeflow-node-agent.rollback || rollback_failed=1
  mv -f /usr/local/bin/nodeflow-node-agent.rollback /usr/local/bin/nodeflow-node-agent || rollback_failed=1
  cp -a "$backup_dir/node-agent.env" /etc/nodeflow/node-agent.env || rollback_failed=1
  rm -rf /etc/nodeflow/tls || rollback_failed=1
  if [ -f "$backup_dir/had-tls" ]; then
    cp -a "$backup_dir/tls" /etc/nodeflow/tls || rollback_failed=1
  fi
  rm -rf /var/lib/nodeflow/credentials || rollback_failed=1
  if [ -f "$backup_dir/had-credentials" ]; then
    cp -a "$backup_dir/credentials" /var/lib/nodeflow/credentials || rollback_failed=1
  fi
  if [ -f "$backup_dir/had-credentials-dropin" ]; then
    install -d -m 0755 /etc/systemd/system/nodeflow-node-agent.service.d || rollback_failed=1
    cp -a "$backup_dir/credentials.conf" /etc/systemd/system/nodeflow-node-agent.service.d/credentials.conf || rollback_failed=1
  else
    rm -f /etc/systemd/system/nodeflow-node-agent.service.d/credentials.conf || rollback_failed=1
  fi
  systemctl daemon-reload || rollback_failed=1
  systemctl restart nodeflow-node-agent.service || rollback_failed=1
  if [ "$rollback_failed" -ne 0 ]; then
    echo "rollback was incomplete; backup retained at $backup_dir" >&2
    return 1
  fi
}

completed=false
trap 'exit 1' HUP INT TERM
trap 'if [ "$completed" != true ]; then rollback; fi' EXIT

install -d -m 0750 /etc/nodeflow/tls
install -m 0644 "$ca_source" /etc/nodeflow/tls/ca.crt
install -m 0644 "$certificate_source" /etc/nodeflow/tls/node.crt
install -m 0600 "$key_source" /etc/nodeflow/tls/node.key

systemctl stop nodeflow-node-agent.service
rm -rf /var/lib/nodeflow/credentials
install -d -m 0700 /var/lib/nodeflow/credentials
install -d -m 0755 /etc/systemd/system/nodeflow-node-agent.service.d
cat > /etc/systemd/system/nodeflow-node-agent.service.d/credentials.conf <<'EOF'
[Service]
ReadWritePaths=/var/lib/nodeflow/credentials
EOF

environment_file=/etc/nodeflow/node-agent.env
environment_tmp=$(mktemp /etc/nodeflow/node-agent.env.XXXXXX)
awk '!/^(NODE_AGENT_PANEL_URL|NODE_AGENT_PANEL_TLS_CA|NODE_AGENT_PANEL_TLS_CERT|NODE_AGENT_PANEL_TLS_KEY|NODE_AGENT_PANEL_TLS_SERVER_NAME|NODE_AGENT_CREDENTIAL_RENEWAL_MODE|NODE_AGENT_CREDENTIAL_STATE_DIR)=/' "$environment_file" > "$environment_tmp"
printf '%s\n' \
  "NODE_AGENT_PANEL_URL=$panel_url" \
  'NODE_AGENT_PANEL_TLS_CA=/etc/nodeflow/tls/ca.crt' \
  'NODE_AGENT_PANEL_TLS_CERT=/etc/nodeflow/tls/node.crt' \
  'NODE_AGENT_PANEL_TLS_KEY=/etc/nodeflow/tls/node.key' \
  'NODE_AGENT_CREDENTIAL_RENEWAL_MODE=apply' \
  'NODE_AGENT_CREDENTIAL_STATE_DIR=/var/lib/nodeflow/credentials' >> "$environment_tmp"
chown --reference="$environment_file" "$environment_tmp"
chmod --reference="$environment_file" "$environment_tmp"
mv -f "$environment_tmp" "$environment_file"

install -m 0755 "$binary_source" /usr/local/bin/nodeflow-node-agent.new
mv -f /usr/local/bin/nodeflow-node-agent.new /usr/local/bin/nodeflow-node-agent
systemctl daemon-reload
systemctl restart nodeflow-node-agent.service
sleep 2
systemctl is-active --quiet nodeflow-node-agent.service

completed=true
trap - EXIT HUP INT TERM
rm -f "$ca_source" "$certificate_source" "$key_source" "$binary_source"
printf 'backup=%s\n' "$backup_dir"
printf 'agent_sha256='
sha256sum /usr/local/bin/nodeflow-node-agent | cut -d' ' -f1
