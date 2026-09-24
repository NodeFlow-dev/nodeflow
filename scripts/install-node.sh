#!/usr/bin/env bash
set -euo pipefail

# Manual Node Agent installation (development and recovery). Production nodes
# are enrolled from the Panel («Ноды → Добавить ноду»), which installs the
# signed Agent, the updater and per-node mTLS over SSH.
#
# The Agent binary comes from the GitHub Release asset
# nodeflow-node-agent-<version>-linux-<arch>, verified against the SHA-256
# digest GitHub publishes for the asset; from NODE_AGENT_BINARY; or — only
# with NODEFLOW_AGENT_SOURCE=build — is compiled from this source tree with Go.
# The systemd unit is embedded below (CI keeps it byte-identical to
# configs/systemd/nodeflow-node-agent.service). The script works standalone
# (curl from raw.githubusercontent.com), from the install kit or from a
# checkout. NODEFLOW_VERSION pins the release (default: the latest one).

agent_unit_name=nodeflow-node-agent.service

# write_agent_unit FILE: the Node Agent systemd unit.
write_agent_unit() {
  cat > "$1" <<'NODEFLOW_AGENT_UNIT'
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
CapabilityBoundingSet=
ReadWritePaths=/etc/haproxy /var/lib/nodeflow/updates /var/lib/nodeflow/credentials -/etc/sysctl.d -/proc/sys/fs/pipe-max-size -/proc/sys/fs/pipe-user-pages-soft

[Install]
WantedBy=multi-user.target
NODEFLOW_AGENT_UNIT
}

die() {
  echo "$*" >&2
  exit 1
}

# resolve_release: reads the GitHub release object (tag_name and assets[] with
# name and digest) for NODEFLOW_VERSION or the latest release.
resolve_release() {
  repository=${NODEFLOW_GITHUB_REPOSITORY:-NodeFlow-dev/nodeflow}
  [[ ${repository} =~ ^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$ ]] || { echo "invalid NODEFLOW_GITHUB_REPOSITORY" >&2; exit 2; }
  command -v curl >/dev/null || die "curl is required to download the Node Agent"
  if command -v jq >/dev/null; then
    json_tool=jq
  elif command -v python3 >/dev/null; then
    json_tool=python3
  else
    die "jq or python3 is required to read the GitHub release"
  fi
  local api
  if [[ -n ${NODEFLOW_VERSION:-} ]]; then
    version=${NODEFLOW_VERSION#v}
    [[ ${version} =~ ^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.+-]+)?$ ]] || { echo "invalid NODEFLOW_VERSION" >&2; exit 2; }
    api=${NODEFLOW_RELEASE_API_URL:-https://api.github.com/repos/${repository}/releases/tags/v${version}}
  else
    api=${NODEFLOW_RELEASE_API_URL:-https://api.github.com/repos/${repository}/releases/latest}
  fi
  curl -fsSL --retry 3 --connect-timeout 10 \
    -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2022-11-28' \
    -H 'User-Agent: NodeFlow-installer' "${api}" -o "${work_dir}/release.json" \
    || die "cannot read the GitHub release ${NODEFLOW_VERSION:-latest}"
  local tag
  tag=$(json_query tag) || die "GitHub release has no tag_name"
  if [[ -n ${NODEFLOW_VERSION:-} ]]; then
    [[ ${tag} == "v${version}" ]] || die "GitHub returned release ${tag} instead of v${version}"
  else
    version=${tag#v}
    [[ ${version} =~ ^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.+-]+)?$ ]] || die "latest release tag ${tag} is not a version"
  fi
  release_base_url=${NODEFLOW_RELEASE_BASE_URL:-https://github.com/${repository}/releases/download/v${version}}
}

# json_query tag | json_query digest NAME: prints the release tag, or the
# digest of the only asset called NAME ("count:N" when it is not unique).
json_query() {
  if [[ ${json_tool} == jq ]]; then
    if [[ $1 == tag ]]; then
      jq -er '.tag_name | select(type == "string" and length > 0)' "${work_dir}/release.json"
    else
      jq -r --arg n "$2" '[.assets[]? | select(.name == $n)] | if length == 1 then (.[0].digest // "") else "count:\(length)" end' \
        "${work_dir}/release.json"
    fi
  else
    python3 - "$1" "${2:-}" "${work_dir}/release.json" <<'PY'
import json, sys
kind, name, path = sys.argv[1:4]
with open(path, encoding="utf-8") as f:
    release = json.load(f)
if kind == "tag":
    tag = release.get("tag_name")
    if not isinstance(tag, str) or not tag:
        sys.exit(1)
    print(tag)
else:
    found = [a for a in release.get("assets") or [] if a.get("name") == name]
    print((found[0].get("digest") or "") if len(found) == 1 else "count:%d" % len(found))
PY
  fi
}

# fetch_release_asset NAME: downloads the asset into $work_dir and checks it
# against the SHA-256 digest GitHub publishes for it; fails closed.
fetch_release_asset() {
  local name=$1 digest expected actual
  digest=$(json_query digest "${name}") || die "cannot parse the GitHub release v${version}"
  case ${digest} in
    count:0) die "release v${version} has no asset ${name}" ;;
    count:*) die "release v${version} lists asset ${name} more than once" ;;
  esac
  expected=${digest#sha256:}
  [[ ${digest} == sha256:* && ${expected} =~ ^[0-9a-fA-F]{64}$ ]] \
    || die "release v${version} has no SHA-256 digest for ${name}"
  curl -fsSL --retry 3 --connect-timeout 10 -H 'User-Agent: NodeFlow-installer' \
    "${release_base_url}/${name}" -o "${work_dir}/${name}" \
    || die "cannot download ${name} of v${version}"
  actual=$(sha256sum "${work_dir}/${name}" | awk '{ print $1 }')
  [[ ${actual} == "${expected,,}" ]] || { rm -f "${work_dir}/${name}"; die "SHA-256 mismatch for ${name}"; }
}

# firewall_helper: prepare-node-firewall.sh from the checkout or install kit
# next to this script, otherwise from the verified install kit of the release.
firewall_helper() {
  if [[ -f ${repo_dir}/scripts/prepare-node-firewall.sh ]]; then
    printf '%s\n' "${repo_dir}/scripts/prepare-node-firewall.sh"
    return
  fi
  [[ -n ${version} ]] || resolve_release >&2
  local kit=NodeFlow-Panel-${version}-Agent-${version}-install-kit
  fetch_release_asset "${kit}.tar.gz" >&2
  tar -xzf "${work_dir}/${kit}.tar.gz" -C "${work_dir}" "${kit}/scripts/prepare-node-firewall.sh" \
    || die "${kit}.tar.gz does not contain scripts/prepare-node-firewall.sh"
  chmod 0755 "${work_dir}/${kit}/scripts/prepare-node-firewall.sh"
  printf '%s\n' "${work_dir}/${kit}/scripts/prepare-node-firewall.sh"
}

main() {
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

  case $(uname -m) in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
  esac

  work_dir=$(mktemp -d /tmp/nodeflow-node-install.XXXXXX)
  trap 'rm -rf "${work_dir}"' EXIT
  version=
  unit_file=${work_dir}/${agent_unit_name}
  write_agent_unit "${unit_file}"

  firewall_mode=${NODE_AGENT_FIREWALL_MODE:-observe}
  case ${firewall_mode} in
    off|observe) ;;
    apply)
      firewall_script=$(firewall_helper)
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
    [[ -n ${version} ]] || resolve_release
    agent_asset=nodeflow-node-agent-${version}-linux-${arch}
    fetch_release_asset "${agent_asset}"
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
}

# NODEFLOW_TEST_ONLY=1 only defines the functions (scripts/test-install.sh).
if [[ ${NODEFLOW_TEST_ONLY:-} != 1 ]]; then
  main "$@"
fi
