#!/bin/sh
set -eu

panel_host=${1:-}
public_url=${2:-}
agent_bind_addr=${3:-${NODEFLOW_AGENT_BIND_ADDR:-0.0.0.0}}
panel_port=${NODEFLOW_PANEL_PORT:-8080}
root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)

if [ -z "$panel_host" ] || [ "$#" -gt 3 ]; then
  echo "usage: sudo ./scripts/install-panel.sh PANEL_IP_OR_DNS [PUBLIC_HTTPS_URL] [AGENT_BIND_IP]" >&2
  exit 2
fi
case "$panel_host" in
  *[!A-Za-z0-9:.-]*) echo "panel host must be an IP address or DNS name" >&2; exit 2 ;;
esac
case "$agent_bind_addr" in
  *[!A-Fa-f0-9:.]*) echo "Agent bind address must be an IP address" >&2; exit 2 ;;
esac
case "$panel_port" in
  ''|*[!0-9]*) echo "NODEFLOW_PANEL_PORT must be a TCP port number" >&2; exit 2 ;;
esac
[ "$panel_port" -ge 1 ] && [ "$panel_port" -le 65535 ] \
  || { echo "NODEFLOW_PANEL_PORT must be between 1 and 65535" >&2; exit 2; }
[ "$panel_port" -ne 4200 ] \
  || { echo "NODEFLOW_PANEL_PORT must not conflict with the Agent mTLS port 4200" >&2; exit 2; }
if [ -z "$public_url" ]; then
  case "$panel_host" in
    *:*) public_url="https://[$panel_host]"; agent_url="https://[$panel_host]:4200" ;;
    *) public_url="https://$panel_host"; agent_url="https://$panel_host:4200" ;;
  esac
else
  case "$public_url" in
    https://*) ;;
    *) echo "PUBLIC_HTTPS_URL must start with https://" >&2; exit 2 ;;
  esac
  case "$panel_host" in
    *:*) agent_url="https://[$panel_host]:4200" ;;
    *) agent_url="https://$panel_host:4200" ;;
  esac
fi

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root: PKI files must be readable only by the Panel container group" >&2
  exit 2
fi
command -v docker >/dev/null 2>&1 || { echo "Docker Engine with Compose plugin is required" >&2; exit 1; }
docker compose version >/dev/null 2>&1 || { echo "Docker Compose plugin is required" >&2; exit 1; }
command -v openssl >/dev/null 2>&1 || { echo "OpenSSL 3 is required" >&2; exit 1; }

cd "$root"
if [ -e .env ]; then
  echo "refusing to overwrite $root/.env; use docker compose for an existing installation" >&2
  exit 1
fi
for path in pki tls; do
  if [ -e "$path" ]; then
    echo "refusing a mixed new install: $root/$path already exists" >&2
    exit 1
  fi
done

umask 077
postgres_password=$(openssl rand -hex 24)
admin_token=$(openssl rand -hex 32)
cat > .env <<EOF
POSTGRES_DB=nodeflow
POSTGRES_USER=nodeflow
POSTGRES_PASSWORD=$postgres_password
PANEL_ADMIN_TOKEN=$admin_token
PANEL_PORT=$panel_port
PANEL_BIND_ADDR=127.0.0.1
ALLOW_INSECURE_HTTP=false
PANEL_PUBLIC_URL=$public_url
DATABASE_MAX_CONNS=10
PANEL_AGENT_PUBLIC_URL=$agent_url
PANEL_AGENT_TLS_LISTEN_ADDR=:4200
PANEL_AGENT_TLS_BIND_ADDR=$agent_bind_addr
PANEL_AGENT_TLS_PORT=4200
PANEL_AGENT_TLS_CERT_FILE=/tls/server.crt
PANEL_AGENT_TLS_KEY_FILE=/tls/server.key
PANEL_AGENT_TLS_CLIENT_CA_FILE=/pki/ca.crt
PANEL_AGENT_TLS_ISSUER_KEY_FILE=/pki/ca.key
PANEL_REQUIRE_AGENT_MTLS=true
PANEL_UPDATE_SIGNING_KEY_FILE=/pki/update-signing.key
EOF
chmod 0600 .env

rollback_new_install() {
  rm -f .env
  rm -rf pki tls
}
trap 'rollback_new_install' HUP INT TERM
if ! ./scripts/init-mtls-pki.sh "$panel_host" "$root"; then
  rollback_new_install
  exit 1
fi
if ! docker compose config -q || ! docker compose run --rm --no-deps panel-exposure-guard; then
  rollback_new_install
  exit 1
fi

# From this point the generated trust roots may already be consumed by running
# containers. Preserve them on interruption so a retry cannot orphan nodes.
trap - HUP INT TERM
docker compose up -d --build

attempt=0
until docker compose exec -T postgres sh -c \
  "wget -qO- http://panel-api:8080/healthz | grep -q '\"status\":\"ok\"'" >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 60 ]; then
    echo "Panel did not become healthy; inspect: docker compose logs panel-api migrate" >&2
    exit 1
  fi
  sleep 1
done

printf '%s\n' \
  "NodeFlow Panel is healthy." \
  "Browser upstream: http://127.0.0.1:$panel_port (loopback only)." \
  "Public URL: $public_url" \
  "Agent mTLS endpoint: $agent_url" \
  "Admin token is stored only in $root/.env; it was not printed." \
  "Next: configure HTTPS with docs/install/reverse-proxy/Caddyfile.example or nginx.conf.example."
