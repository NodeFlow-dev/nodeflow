#!/usr/bin/env bash
set -euo pipefail

# Manual Node Agent installation (development and recovery). Production nodes
# are enrolled from the Panel («Ноды → Добавить ноду»), which installs the
# signed Agent, the updater and per-node mTLS over SSH.
#
# The Agent binary comes from the GitHub Release (verified against the
# release SHA256SUMS), from NODE_AGENT_BINARY, or — only with
# NODEFLOW_AGENT_SOURCE=build — is compiled from this source tree with Go.
# This script works standalone (downloaded as a release asset) or from a
# checkout; missing helper files are fetched from the same release.

if [[ ${EUID} -ne 0 ]]; then
  echo "run as root" >&2
  exit 1
fi

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_dir=$(cd "${script_dir}/.." && pwd)
token=${NODE_AGENT_TOKEN:-}
if [[ -z ${token} ]]; then
  echo "NODE_AGENT_TOKEN is required" >&2
  exit 1
fi
command -v haproxy >/dev/null || { echo "haproxy is required" >&2; exit 1; }

version=${NODEFLOW_VERSION:-2.0.0}
version=${version#v}
[[ ${version} =~ ^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.+-]+)?$ ]] || { echo "invalid NODEFLOW_VERSION" >&2; exit 2; }
repository=${NODEFLOW_GITHUB_REPOSITORY:-NodeFlow-dev/nodeflow}
[[ ${repository} =~ ^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$ ]] || { echo "invalid NODEFLOW_GITHUB_REPOSITORY" >&2; exit 2; }
release_base_url=${NODEFLOW_RELEASE_BASE_URL:-https://github.com/${repository}/releases/download/v${version}}
case $(uname -m) in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

work_dir=$(mktemp -d /tmp/nodeflow-node-install.XXXXXX)
trap 'rm -rf "${work_dir}"' EXIT

sums_ready=0
fetch_release_file() {
  local name=$1 expected actual
  command -v curl >/dev/null || { echo "curl is required to download ${name}" >&2; exit 1; }
  if [[ ${sums_ready} -eq 0 ]]; then
    curl -fsSL --retry 3 --connect-timeout 10 "${release_base_url}/SHA256SUMS" -o "${work_dir}/SHA256SUMS" \
      || { echo "cannot download SHA256SUMS of v${version}" >&2; exit 1; }
    sums_ready=1
  fi
  curl -fsSL --retry 3 --connect-timeout 10 "${release_base_url}/${name}" -o "${work_dir}/${name}" \
    || { echo "cannot download ${name} of v${version}" >&2; exit 1; }
  expected=$(awk -v n="${name}" '$2 == n || $2 == "*" n { print $1 }' "${work_dir}/SHA256SUMS")
  [[ ${expected} =~ ^[0-9a-fA-F]{64}$ ]] || { echo "SHA256SUMS does not list ${name} exactly once" >&2; exit 1; }
  actual=$(sha256sum "${work_dir}/${name}" | awk '{ print $1 }')
  [[ ${actual} == "${expected,,}" ]] || { echo "SHA-256 mismatch for ${name}" >&2; exit 1; }
}

# helper_file RELATIVE_PATH ASSET_NAME: the checkout copy when present,
# otherwise the verified release asset.
helper_file() {
  if [[ -f ${repo_dir}/$1 ]]; then
    printf '%s\n' "${repo_dir}/$1"
  else
    fetch_release_file "$2" >&2
    chmod 0755 "${work_dir}/$2"
    printf '%s\n' "${work_dir}/$2"
  fi
}

unit_file=$(helper_file configs/systemd/nodeflow-node-agent.service nodeflow-node-agent.service)

firewall_mode=${NODE_AGENT_FIREWALL_MODE:-observe}
case ${firewall_mode} in
  off|observe) ;;
  apply)
    firewall_script=$(helper_file scripts/prepare-node-firewall.sh prepare-node-firewall.sh)
    if [[ -n ${NODEFLOW_SSH_PORT:-} ]]; then
      "${firewall_script}" --apply --ssh-port "${NODEFLOW_SSH_PORT}"
    else
      "${firewall_script}" --apply
    fi
    ;;
  *)
    echo "NODE_AGENT_FIREWALL_MODE must be off, observe or apply" >&2
    exit 2
    ;;
esac

if [[ -n ${NODE_AGENT_BINARY:-} ]]; then
  install -m 0755 "${NODE_AGENT_BINARY}" /usr/local/bin/nodeflow-node-agent.new
elif [[ ${NODEFLOW_AGENT_SOURCE:-release} == build ]]; then
  command -v go >/dev/null || { echo "go is required for NODEFLOW_AGENT_SOURCE=build" >&2; exit 1; }
  go -C "${repo_dir}" build -trimpath -ldflags "-s -w" -o /usr/local/bin/nodeflow-node-agent.new ./cmd/node-agent
else
  agent_asset=nodeflow-node-agent-${version}-linux-${arch}
  fetch_release_file "${agent_asset}"
  install -m 0755 "${work_dir}/${agent_asset}" /usr/local/bin/nodeflow-node-agent.new
fi
/usr/local/bin/nodeflow-node-agent.new -version >/dev/null \
  || { rm -f /usr/local/bin/nodeflow-node-agent.new; echo "Node Agent binary does not run on this host" >&2; exit 1; }
mv -f /usr/local/bin/nodeflow-node-agent.new /usr/local/bin/nodeflow-node-agent
install -d -m 0750 /etc/nodeflow /etc/haproxy
install -d -m 0700 /var/lib/nodeflow /var/lib/nodeflow/updates /var/lib/nodeflow/credentials /var/lib/nodeflow-updater
if [[ ! -e /etc/nodeflow/node-agent.env ]]; then
  install -m 0600 /dev/null /etc/nodeflow/node-agent.env
  {
	printf 'NODE_AGENT_LISTEN=%s\n' "${NODE_AGENT_LISTEN:-127.0.0.1:4200}"
	printf 'NODE_AGENT_ALLOW_REMOTE_LISTEN=%s\n' "${NODE_AGENT_ALLOW_REMOTE_LISTEN:-false}"
    printf 'NODE_AGENT_TOKEN=%s\n' "${token}"
    printf 'NODE_AGENT_HAPROXY_CONFIG=%s\n' "${NODE_AGENT_HAPROXY_CONFIG:-/etc/haproxy/haproxy.cfg}"
    printf 'NODE_AGENT_HAPROXY_BINARY=%s\n' "$(command -v haproxy)"
    printf 'NODE_AGENT_HAPROXY_SERVICE=%s\n' "${NODE_AGENT_HAPROXY_SERVICE:-haproxy.service}"
	printf 'NODE_AGENT_HAPROXY_STATS_SOCKET=%s\n' "${NODE_AGENT_HAPROXY_STATS_SOCKET:-/run/haproxy/admin.sock}"
	printf 'NODE_AGENT_HAPROXY_STATS_TIMEOUT=%s\n' "${NODE_AGENT_HAPROXY_STATS_TIMEOUT:-2s}"
	printf 'NODE_AGENT_FIREWALL_MODE=%s\n' "${firewall_mode}"
	printf 'NODE_AGENT_SELF_UPDATE_MODE=%s\n' "${NODE_AGENT_SELF_UPDATE_MODE:-off}"
	printf 'NODE_AGENT_UPDATE_STAGING_DIR=%s\n' "${NODE_AGENT_UPDATE_STAGING_DIR:-/var/lib/nodeflow/updates}"
	printf 'NODE_AGENT_UPDATE_PUBLIC_KEY=%s\n' "${NODE_AGENT_UPDATE_PUBLIC_KEY:-}"
	printf 'NODE_AGENT_PANEL_URL=%s\n' "${NODE_AGENT_PANEL_URL:-}"
	printf 'NODE_AGENT_PANEL_TLS_CA=%s\n' "${NODE_AGENT_PANEL_TLS_CA:-}"
	printf 'NODE_AGENT_PANEL_TLS_CERT=%s\n' "${NODE_AGENT_PANEL_TLS_CERT:-}"
	printf 'NODE_AGENT_PANEL_TLS_KEY=%s\n' "${NODE_AGENT_PANEL_TLS_KEY:-}"
	printf 'NODE_AGENT_PANEL_TLS_SERVER_NAME=%s\n' "${NODE_AGENT_PANEL_TLS_SERVER_NAME:-}"
	printf 'NODE_AGENT_CREDENTIAL_RENEWAL_MODE=%s\n' "${NODE_AGENT_CREDENTIAL_RENEWAL_MODE:-observe}"
	printf 'NODE_AGENT_CREDENTIAL_STATE_DIR=%s\n' "${NODE_AGENT_CREDENTIAL_STATE_DIR:-/var/lib/nodeflow/credentials}"
	printf 'NODE_AGENT_HEARTBEAT_INTERVAL=%s\n' "${NODE_AGENT_HEARTBEAT_INTERVAL:-15s}"
	printf 'NODE_AGENT_RECONCILE_TIMEOUT=%s\n' "${NODE_AGENT_RECONCILE_TIMEOUT:-45s}"
  } > /etc/nodeflow/node-agent.env
fi
if [[ ${firewall_mode} == apply ]]; then
  sed \
    -e 's/^CapabilityBoundingSet=$/CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW/' \
    -e 's|^ReadWritePaths=/etc/haproxy /var/lib/nodeflow/updates /var/lib/nodeflow/credentials -/etc/sysctl.d -/proc/sys/fs/pipe-max-size -/proc/sys/fs/pipe-user-pages-soft$|ReadWritePaths=/etc/haproxy /var/lib/nodeflow/updates /var/lib/nodeflow/credentials -/etc/sysctl.d -/proc/sys/fs/pipe-max-size -/proc/sys/fs/pipe-user-pages-soft -/etc/ufw|' \
    "${unit_file}" > /etc/systemd/system/nodeflow-node-agent.service.new
  chmod 0644 /etc/systemd/system/nodeflow-node-agent.service.new
  mv -f /etc/systemd/system/nodeflow-node-agent.service.new /etc/systemd/system/nodeflow-node-agent.service
else
  install -m 0644 "${unit_file}" /etc/systemd/system/nodeflow-node-agent.service
fi
systemctl daemon-reload
systemctl enable nodeflow-node-agent.service
systemctl restart nodeflow-node-agent.service
sleep 2
systemctl is-active --quiet nodeflow-node-agent.service

echo "installed: nodeflow-node-agent.service"
echo "IMPORTANT: the installer did not modify /etc/haproxy/haproxy.cfg or its systemd command."
echo "Node Agent will validate, back up and manage /etc/haproxy/haproxy.cfg by default."
echo "For production enrollment, prefer the Panel SSH bootstrap: it installs per-node mTLS and the signed updater atomically."
