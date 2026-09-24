#!/bin/sh
set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

sh -n install.sh scripts/check-panel-exposure.sh scripts/export-public.sh scripts/install-panel.sh scripts/deploy-panel.sh
grep -q 'NODEFLOW_PANEL_PORT' scripts/install-panel.sh
grep -q 'groupadd --gid "$runtime_gid" nodeflow-runtime' scripts/init-mtls-pki.sh
grep -q 'chown root:"$runtime_group"' scripts/init-mtls-pki.sh
grep -q 'groupadd --gid "$runtime_gid" nodeflow-runtime' scripts/init-update-signing-key.sh
grep -q 'chown root:"$runtime_group"' scripts/init-update-signing-key.sh

PANEL_BIND_ADDR=127.0.0.1 ALLOW_INSECURE_HTTP=false ./scripts/check-panel-exposure.sh
if PANEL_BIND_ADDR=0.0.0.0 ALLOW_INSECURE_HTTP=false ./scripts/check-panel-exposure.sh >/dev/null 2>&1; then
  echo "exposure guard accepted unsafe plain HTTP without opt-in" >&2
  exit 1
fi
PANEL_BIND_ADDR=0.0.0.0 ALLOW_INSECURE_HTTP=true ./scripts/check-panel-exposure.sh >/dev/null 2>&1

grep -q 'proxy_pass http://127.0.0.1:8080;' docs/install/reverse-proxy/nginx.conf.example
grep -q 'reverse_proxy 127.0.0.1:8080' docs/install/reverse-proxy/Caddyfile.example
grep -q 'INSTALL-NODEFLOW.sh' scripts/build-install-kit.sh
grep -q '"$root/install.sh"' scripts/build-install-kit.sh
grep -q 'nodeflow-credentials.txt' install.sh
grep -q 'NODEFLOW_AUTH_MODE' install.sh
grep -q 'releases/latest' install.sh
grep -q 'DNS check passed:' install.sh
grep -q 'api.ipify.org' install.sh
grep -q 'api64.ipify.org' install.sh
grep -q 'verify_asset compose.release.yaml' install.sh
grep -q 'ghcr.io/nodeflow-dev/nodeflow-panel' install.sh
grep -q 'pg_dump -Fc' install.sh
! grep -q 'docker compose build\|go build\|git clone' install.sh
grep -q 'ghcr.io/nodeflow-dev/nodeflow-panel:${NODEFLOW_VERSION:-' compose.release.yaml
grep -q 'command: \["migrate"\]' compose.release.yaml
grep -q 'query key=' install.sh
grep -q 'header_regexp Cookie' install.sh
grep -q 'redir \* / 303' install.sh
grep -q 'does not resolve yet; Caddy cannot obtain a public TLS certificate' install.sh
grep -q 'write_credentials' install.sh
! grep -q 'die "local Caddy HTTPS request failed"' install.sh
grep -q 'raw.githubusercontent.com/NodeFlow-dev/nodeflow/main/install.sh' README.md
grep -q 'ALLOW_INSECURE_HTTP=' docs/install/index.html
grep -q '>false<' docs/install/index.html

if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
  env_file=$(mktemp)
  trap 'rm -f "$env_file"' EXIT HUP INT TERM
  sed \
    -e 's/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=test-only-password/' \
    -e 's/^PANEL_ADMIN_TOKEN=.*/PANEL_ADMIN_TOKEN=test-only-admin-token-32-characters/' \
    .env.example > "$env_file"
  docker compose --env-file "$env_file" config -q
  docker compose -f compose.release.yaml --env-file "$env_file" config -q
fi

printf 'install_bundle=ok exposure_guard=ok\n'
