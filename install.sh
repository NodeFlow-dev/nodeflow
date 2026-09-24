#!/bin/sh
set -eu

# Interactive all-in-one installer and upgrader for a NodeFlow Panel host.
#
# Fresh install: installs Docker and Caddy, generates secrets, mTLS PKI and the
# update-signing key, pulls the prebuilt Panel image
# ghcr.io/nodeflow-dev/nodeflow-panel:<version> through the release compose
# file, publishes Caddy HTTPS in front of 127.0.0.1:8080 and saves the login
# credentials. Nothing is cloned or built on the host.
#
# Re-run on an installed host (/opt/nodeflow/.env exists): upgrade. The
# database is dumped with pg_dump first, .env, tls/, pki/ and the Caddy
# snippet are kept, compose.yaml is replaced by the release compose file and
# the new image is pulled and started (it applies the migrations itself).
# 1.0.x installs made from the source tree are switched to the image.
#
# Optional automation inputs: NODEFLOW_DOMAIN and NODEFLOW_AUTH_MODE (cookie or
# none) for a fresh install, NODEFLOW_VERSION (for example 2.0.0; default: the
# newest published GitHub Release), NODEFLOW_GITHUB_REPOSITORY.

install_root=${NODEFLOW_INSTALL_ROOT:-/opt/nodeflow}
caddyfile=${NODEFLOW_CADDYFILE:-/etc/caddy/Caddyfile}
caddy_conf_dir=${NODEFLOW_CADDY_CONF_DIR:-/etc/caddy/conf.d}
caddy_snippet=$caddy_conf_dir/nodeflow-panel.caddy
backup_dir=${NODEFLOW_BACKUP_DIR:-/var/backups/nodeflow}
credentials_name=nodeflow-credentials.txt
panel_image_repository=ghcr.io/nodeflow-dev/nodeflow-panel
stage_dir=
download_dir=
release_tag=
release_version=
release_asset=compose.release.yaml
runtime_gid=65532

say() {
  printf '%s\n' "$*"
}

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

cleanup() {
  if [ -n "$stage_dir" ] && [ -d "$stage_dir" ]; then
    rm -rf -- "$stage_dir"
  fi
  if [ -n "$download_dir" ] && [ -d "$download_dir" ]; then
    rm -rf -- "$download_dir"
  fi
}
trap cleanup EXIT HUP INT TERM

require_root() {
  if [ "$(id -u)" -eq 0 ]; then
    return
  fi
  command -v sudo >/dev/null 2>&1 || die "run this script as root or install sudo"
  case "$0" in
    sh|bash|-|/dev/fd/*|/proc/*) die "for a streamed installer use: curl -fsSL URL | sudo sh" ;;
  esac
  exec sudo --preserve-env=NODEFLOW_DOMAIN,NODEFLOW_AUTH_MODE,NODEFLOW_VERSION,NODEFLOW_GITHUB_REPOSITORY "$0" "$@"
}

apt_install() {
  DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "$@"
}

install_base_packages() {
  [ -r /etc/os-release ] || die "cannot identify the operating system"
  . /etc/os-release
  case "${ID:-}" in
    ubuntu|debian) ;;
    *) die "automatic installation supports Ubuntu or Debian only" ;;
  esac
  command -v apt-get >/dev/null 2>&1 || die "apt-get is required"
  say "Installing required packages..."
  apt-get update
  apt_install ca-certificates curl jq openssl
}

install_caddy() {
  if command -v caddy >/dev/null 2>&1; then
    return
  fi

  if apt-cache show caddy >/dev/null 2>&1; then
    apt_install caddy
  else
    say "Adding the official Caddy package repository..."
    apt_install debian-keyring debian-archive-keyring apt-transport-https gnupg
    install -d -m 0755 /usr/share/keyrings
    curl -1fsSL https://dl.cloudsmith.io/public/caddy/stable/gpg.key \
      | gpg --dearmor --yes -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
    curl -1fsSL https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt \
      -o /etc/apt/sources.list.d/caddy-stable.list
    chmod 0644 /usr/share/keyrings/caddy-stable-archive-keyring.gpg \
      /etc/apt/sources.list.d/caddy-stable.list
    apt-get update
    apt_install caddy
  fi

  command -v caddy >/dev/null 2>&1 || die "Caddy installation failed"
}

install_docker() {
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    systemctl enable --now docker >/dev/null
    return
  fi

  if apt-cache show docker.io >/dev/null 2>&1 \
    && apt-cache show docker-compose-v2 >/dev/null 2>&1; then
    apt_install docker.io docker-compose-v2
  else
    say "Adding the official Docker package repository..."
    apt_install gnupg
    . /etc/os-release
    case "${ID:-}" in
      ubuntu|debian) ;;
      *) die "automatic Docker installation supports Ubuntu or Debian only" ;;
    esac
    docker_codename=${VERSION_CODENAME:-}
    [ -n "$docker_codename" ] || die "cannot determine the distribution codename"
    install -d -m 0755 /etc/apt/keyrings
    curl -fsSL "https://download.docker.com/linux/$ID/gpg" \
      -o /etc/apt/keyrings/docker.asc
    chmod 0644 /etc/apt/keyrings/docker.asc
    docker_arch=$(dpkg --print-architecture)
    printf '%s\n' \
      "deb [arch=$docker_arch signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/$ID $docker_codename stable" \
      > /etc/apt/sources.list.d/docker.list
    apt-get update
    apt_install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  fi

  command -v docker >/dev/null 2>&1 || die "Docker Engine installation failed"
  docker compose version >/dev/null 2>&1 || die "Docker Compose plugin installation failed"
  systemctl enable --now docker >/dev/null
  systemctl is-active --quiet docker || die "Docker service is not active"
}

valid_domain() {
  domain_to_check=$1
  case "$domain_to_check" in
    ''|*[!A-Za-z0-9.-]*|.*|*.|*-|-*|*..*) return 1 ;;
  esac
  case "$domain_to_check" in
    *.*) ;;
    *) return 1 ;;
  esac
  [ "${#domain_to_check}" -le 253 ] || return 1
  printf '%s\n' "$domain_to_check" | awk -F. '
    {
      for (i = 1; i <= NF; i++) {
        if (length($i) < 1 || length($i) > 63) exit 1
        if (length($i) == 1 && $i !~ /^[A-Za-z0-9]$/) exit 1
        if (length($i) > 1 && $i !~ /^[A-Za-z0-9][A-Za-z0-9-]*[A-Za-z0-9]$/) exit 1
      }
    }
  '
}

valid_ipv4() {
  printf '%s\n' "$1" | awk -F. '
    NF != 4 { exit 1 }
    {
      for (i = 1; i <= 4; i++) {
        if ($i !~ /^[0-9]+$/ || $i < 0 || $i > 255) exit 1
      }
    }
  '
}

valid_ipv6() {
  case "$1" in
    *:*) printf '%s\n' "$1" | grep -Eq '^[0-9A-Fa-f:.]+$' ;;
    *) return 1 ;;
  esac
}

resolve_domain_addresses() {
  {
    getent ahostsv4 "$domain" 2>/dev/null || true
    getent ahostsv6 "$domain" 2>/dev/null || true
  } | awk '{ print $1 }' | sort -u
}

detect_public_addresses() {
  public_ipv4=$(curl -4fsS --noproxy '*' --connect-timeout 5 --max-time 10 \
    https://api.ipify.org 2>/dev/null || true)
  public_ipv6=$(curl -6fsS --noproxy '*' --connect-timeout 5 --max-time 10 \
    https://api64.ipify.org 2>/dev/null || true)

  valid_ipv4 "$public_ipv4" || public_ipv4=
  valid_ipv6 "$public_ipv6" || public_ipv6=
  [ -n "$public_ipv4" ] || [ -n "$public_ipv6" ] \
    || die "cannot determine this server's public IP address"
}

domain_points_to_this_server() {
  resolved_addresses=$(resolve_domain_addresses)
  [ -n "$resolved_addresses" ] || return 1

  if [ -n "$public_ipv4" ] \
    && printf '%s\n' "$resolved_addresses" | grep -Fqx "$public_ipv4"; then
    matched_public_ip=$public_ipv4
    return 0
  fi

  if [ -n "$public_ipv6" ]; then
    normalized_public_ipv6=$(getent ahostsv6 "$public_ipv6" 2>/dev/null \
      | awk 'NR == 1 { print $1 }')
    [ -n "$normalized_public_ipv6" ] || normalized_public_ipv6=$public_ipv6
    if printf '%s\n' "$resolved_addresses" | grep -Fqx "$normalized_public_ipv6"; then
      matched_public_ip=$normalized_public_ipv6
      return 0
    fi
  fi
  return 1
}

prompt_input() {
  prompt_text=$1
  if [ -r /dev/tty ]; then
    printf '%s' "$prompt_text" > /dev/tty
    IFS= read -r prompt_value < /dev/tty || die "interactive input is required"
  else
    printf '%s' "$prompt_text"
    IFS= read -r prompt_value || die "interactive input is required"
  fi
}

read_domain() {
  domain=${NODEFLOW_DOMAIN:-}
  domain_from_environment=0
  [ -z "${NODEFLOW_DOMAIN:-}" ] || domain_from_environment=1
  detect_public_addresses

  while :; do
    while ! valid_domain "$domain"; do
      if [ -n "$domain" ]; then
        say "Invalid domain. Use a DNS name without scheme, port or path."
      fi
      [ "$domain_from_environment" -eq 0 ] \
        || die "NODEFLOW_DOMAIN is invalid"
      prompt_input 'NodeFlow Panel domain (for example panel.example.com): '
      domain=$prompt_value
    done

    if domain_points_to_this_server; then
      say "DNS check passed: $domain points to this server ($matched_public_ip)."
      return
    fi

    say "Domain DNS check failed."
    say "Domain addresses: ${resolved_addresses:-none}"
    say "This server public IPv4: ${public_ipv4:-unavailable}"
    say "This server public IPv6: ${public_ipv6:-unavailable}"
    [ "$domain_from_environment" -eq 0 ] \
      || die "NODEFLOW_DOMAIN does not point to this server"
    domain=
    prompt_input 'NodeFlow Panel domain (for example panel.example.com): '
    domain=$prompt_value
  done
}

normalise_auth_mode() {
  case "$1" in
    1|cookie|caddy-cookie|caddy_cookie) printf '%s\n' cookie ;;
    2|none|no-cookie|no_cookie) printf '%s\n' none ;;
    *) return 1 ;;
  esac
}

read_auth_mode() {
  auth_input=${NODEFLOW_AUTH_MODE:-}
  while :; do
    if auth_mode=$(normalise_auth_mode "$auth_input"); then
      return
    fi
    if [ -n "$auth_input" ]; then
      say "Unknown authorization mode: $auth_input"
    fi
    say "External Caddy authorization:"
    say "  1) Cookie gate (activation link sets a protected cookie)"
    say "  2) None (Panel admin token remains required)"
    prompt_input 'Choose 1 or 2: '
    auth_input=$prompt_value
  done
}


# --- Release resolution and download --------------------------------------

github_repository() {
  repository=${NODEFLOW_GITHUB_REPOSITORY:-NodeFlow-dev/nodeflow}
  case "$repository" in
    */*) ;;
    *) die "invalid NODEFLOW_GITHUB_REPOSITORY" ;;
  esac
  case "$repository" in
    *[!A-Za-z0-9._/-]*) die "invalid NODEFLOW_GITHUB_REPOSITORY" ;;
  esac
}

valid_version() {
  case "$1" in
    ''|*[!0-9A-Za-z.+-]*) return 1 ;;
  esac
  printf '%s\n' "$1" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.+-]+)?$'
}

resolve_release() {
  github_repository
  download_dir=$(mktemp -d /tmp/nodeflow-release.XXXXXX)
  chmod 0700 "$download_dir"

  if [ -n "${NODEFLOW_VERSION:-}" ]; then
    release_version=${NODEFLOW_VERSION#v}
    valid_version "$release_version" || die "NODEFLOW_VERSION is not a MAJOR.MINOR.PATCH version"
    release_tag=v$release_version
  else
    release_api=${NODEFLOW_RELEASE_API_URL:-https://api.github.com/repos/$repository/releases/latest}
    say "Resolving the latest published NodeFlow release from GitHub..."
    curl -fsSL --retry 3 --connect-timeout 10 \
      -H 'Accept: application/vnd.github+json' \
      -H 'X-GitHub-Api-Version: 2022-11-28' \
      -H 'User-Agent: NodeFlow-installer' \
      "$release_api" -o "$download_dir/release.json" \
      || die "cannot read the latest GitHub release"
    release_tag=$(jq -er '.tag_name | select(type == "string" and length > 0)' "$download_dir/release.json") \
      || die "GitHub release has no tag_name"
    release_version=${release_tag#v}
    valid_version "$release_version" || die "latest release tag $release_tag is not a version"
  fi
  release_base_url=${NODEFLOW_RELEASE_BASE_URL:-https://github.com/$repository/releases/download/$release_tag}
  panel_image=$panel_image_repository:$release_version
}

download_asset() {
  curl -fsSL --retry 3 --connect-timeout 10 -H 'User-Agent: NodeFlow-installer' \
    "$release_base_url/$1" -o "$download_dir/$1" || die "cannot download $1 from $release_tag"
}

# verify_asset NAME: the file must be listed exactly once in the release
# SHA256SUMS and match it.
verify_asset() {
  expected_sha=$(awk -v name="$1" '$2 == name || $2 == "*" name { print $1 }' "$download_dir/SHA256SUMS")
  [ "$(printf '%s\n' "$expected_sha" | awk 'NF { count++ } END { print count+0 }')" -eq 1 ] \
    || die "SHA256SUMS of $release_tag must list $1 exactly once"
  case "$expected_sha" in
    *[!0-9A-Fa-f]*) die "invalid SHA-256 for $1" ;;
  esac
  [ "${#expected_sha}" -eq 64 ] || die "invalid SHA-256 length for $1"
  actual_sha=$(sha256sum "$download_dir/$1" | awk '{ print $1 }')
  [ "$actual_sha" = "$(printf '%s' "$expected_sha" | tr 'A-F' 'a-f')" ] || die "SHA-256 mismatch for $1"
}

download_release_files() {
  download_asset SHA256SUMS
  download_asset compose.release.yaml
  verify_asset compose.release.yaml
  grep -Fq "$panel_image_repository:" "$download_dir/compose.release.yaml" \
    || die "compose.release.yaml of $release_tag does not use $panel_image_repository"
  release_asset=$panel_image
  say "Verified NodeFlow $release_tag: compose.release.yaml (image $panel_image)"
}

# --- Fresh installation ---------------------------------------------------

runtime_group_name() {
  runtime_group=$(getent group "$runtime_gid" | awk -F: 'NR == 1 { print $1 }')
  if [ -z "$runtime_group" ]; then
    command -v groupadd >/dev/null 2>&1 || die "groupadd is required to create the Panel runtime group"
    if getent group nodeflow-runtime >/dev/null 2>&1; then
      die "nodeflow-runtime exists with a different GID; expected $runtime_gid"
    fi
    groupadd --gid "$runtime_gid" nodeflow-runtime
    runtime_group=nodeflow-runtime
  fi
}

# generate_pki DIR HOST: Agent CA, Panel mTLS server certificate for HOST and
# the Ed25519 Agent update-signing key, readable only by the container group.
generate_pki() {
  pki_root=$1
  pki_host=$2
  pki_dir=$pki_root/pki
  tls_dir=$pki_root/tls
  runtime_group_name
  (
    umask 077
    install -d -m 0750 -o root -g "$runtime_group" "$pki_dir" "$tls_dir"
    openssl genpkey -algorithm ED25519 -out "$pki_dir/ca.key"
    openssl req -new -x509 -key "$pki_dir/ca.key" -days 3650 \
      -subj "/O=NodeFlow/CN=NodeFlow Agent CA" \
      -addext "basicConstraints=critical,CA:TRUE,pathlen:0" \
      -addext "keyUsage=critical,keyCertSign,cRLSign" \
      -out "$pki_dir/ca.crt"
    openssl genpkey -algorithm ED25519 -out "$tls_dir/server.key"
    openssl req -new -key "$tls_dir/server.key" -subj "/O=NodeFlow/CN=$pki_host" \
      -out "$pki_root/server.csr"
    printf '%s\n' \
      "basicConstraints=critical,CA:FALSE" \
      "keyUsage=critical,digitalSignature" \
      "extendedKeyUsage=serverAuth" \
      "subjectAltName=DNS:$pki_host" > "$pki_root/server.ext"
    openssl x509 -req -in "$pki_root/server.csr" \
      -CA "$pki_dir/ca.crt" -CAkey "$pki_dir/ca.key" -CAcreateserial \
      -days 825 -extfile "$pki_root/server.ext" -out "$tls_dir/server.crt" 2>/dev/null
    rm -f "$pki_root/server.csr" "$pki_root/server.ext" "$pki_dir/ca.srl"
    openssl genpkey -algorithm ED25519 -out "$pki_dir/update-signing.key"
    openssl pkey -in "$pki_dir/update-signing.key" -pubout -out "$pki_dir/update-signing.pub"
  ) || die "cannot generate the Panel PKI"
  chown root:"$runtime_group" "$pki_dir/ca.key" "$pki_dir/ca.crt" "$tls_dir/server.key" \
    "$tls_dir/server.crt" "$pki_dir/update-signing.key" "$pki_dir/update-signing.pub"
  chmod 0440 "$pki_dir/ca.key" "$tls_dir/server.key" "$pki_dir/update-signing.key"
  chmod 0444 "$pki_dir/ca.crt" "$tls_dir/server.crt" "$pki_dir/update-signing.pub"
  openssl verify -CAfile "$pki_dir/ca.crt" "$tls_dir/server.crt" >/dev/null \
    || die "generated Panel certificate does not verify"
}

# write_env DIR: secrets and settings for compose.release.yaml. The browser
# upstream stays on 127.0.0.1:8080 for Caddy; Agent mTLS listens on 4200.
write_env() {
  (
    umask 077
    cat > "$1/.env" <<EOF
NODEFLOW_VERSION=$release_version
POSTGRES_DB=nodeflow
POSTGRES_USER=nodeflow
POSTGRES_PASSWORD=$(openssl rand -hex 24)
PANEL_ADMIN_TOKEN=$(openssl rand -hex 32)
PANEL_PORT=8080
PANEL_BIND_ADDR=127.0.0.1
ALLOW_INSECURE_HTTP=false
PANEL_PUBLIC_URL=https://$domain
DATABASE_MAX_CONNS=10
PANEL_AGENT_PUBLIC_URL=https://$domain:4200
PANEL_AGENT_TLS_LISTEN_ADDR=:4200
PANEL_AGENT_TLS_BIND_ADDR=0.0.0.0
PANEL_AGENT_TLS_PORT=4200
PANEL_AGENT_TLS_CERT_FILE=/tls/server.crt
PANEL_AGENT_TLS_KEY_FILE=/tls/server.key
PANEL_AGENT_TLS_CLIENT_CA_FILE=/pki/ca.crt
PANEL_AGENT_TLS_ISSUER_KEY_FILE=/pki/ca.key
PANEL_REQUIRE_AGENT_MTLS=true
PANEL_UPDATE_SIGNING_KEY_FILE=/pki/update-signing.key
EOF
  )
  chmod 0600 "$1/.env"
}

prepare_install_root() {
  [ "$install_root" = /opt/nodeflow ] \
    || [ "${NODEFLOW_ALLOW_TEST_PATHS:-}" = 1 ] \
    || die "refusing non-standard install path without NODEFLOW_ALLOW_TEST_PATHS=1"
  [ ! -e "$install_root" ] || die "$install_root already exists without .env; refusing to overwrite it"

  install -d -m 0755 "$(dirname -- "$install_root")"
  stage_dir=$(mktemp -d "$(dirname -- "$install_root")/.nodeflow-install.XXXXXX")
  chmod 0750 "$stage_dir"
  install -m 0644 "$download_dir/compose.release.yaml" "$stage_dir/compose.yaml"
  write_env "$stage_dir"
  generate_pki "$stage_dir" "$domain"
  mv -- "$stage_dir" "$install_root"
  stage_dir=
}

compose() {
  (cd "$install_root" && docker compose "$@")
}

wait_for_panel() {
  panel_port=$(sed -n 's/^PANEL_PORT=//p' "$install_root/.env" | tail -n 1)
  panel_port=${panel_port:-8080}
  attempt=0
  until health_response=$(curl -fsS --max-time 5 "http://127.0.0.1:$panel_port/healthz" 2>/dev/null) \
    && printf '%s\n' "$health_response" | grep -q '"status":"ok"'; do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 90 ]; then
      say "Panel did not become healthy; inspect: cd $install_root && docker compose logs panel-api migrate"
      return 1
    fi
    sleep 2
  done
  running_version=$(printf '%s\n' "$health_response" | jq -r '.version // ""')
  [ "$running_version" = "$release_version" ] \
    || { say "Panel reports version ${running_version:-unknown}, expected $release_version"; return 1; }
}

start_panel() {
  say "Pulling $panel_image and PostgreSQL..."
  compose config -q || die "compose.yaml or .env is invalid"
  compose pull || die "cannot pull the NodeFlow images (is ghcr.io reachable?)"
  compose up -d --remove-orphans || die "docker compose up failed"
  wait_for_panel || die "NodeFlow Panel $release_version did not start"
}

# --- Node Agent releases --------------------------------------------------

# publish_agent_releases: downloads the signed-release inputs (Node Agent
# binaries for linux/amd64 and linux/arm64), verifies them against SHA256SUMS
# and uploads them to the Panel, which signs them with its own Ed25519 key.
# Already published versions are skipped. Failures only warn: the binaries can
# be uploaded later in «Настройки → Node Agent».
publish_agent_releases() {
  admin_token=$(sed -n 's/^PANEL_ADMIN_TOKEN=//p' "$install_root/.env" | tail -n 1)
  panel_port=$(sed -n 's/^PANEL_PORT=//p' "$install_root/.env" | tail -n 1)
  panel_api=http://127.0.0.1:${panel_port:-8080}/api/v1/agent-releases
  existing=$(curl -fsS --max-time 10 -H "Authorization: Bearer $admin_token" "$panel_api" 2>/dev/null) \
    || { say "WARNING: cannot list Agent releases; upload Node Agent $release_version manually."; return 0; }
  for arch in amd64 arm64; do
    if printf '%s\n' "$existing" | jq -e --arg v "$release_version" --arg a "$arch" \
      '(if type == "array" then . else (.items // .releases // []) end) | any(.version == $v and .os == "linux" and .arch == $a)' >/dev/null 2>&1; then
      continue
    fi
    agent_asset=nodeflow-node-agent-$release_version-linux-$arch
    if ! curl -fsSL --retry 3 --connect-timeout 10 -H 'User-Agent: NodeFlow-installer' \
      "$release_base_url/$agent_asset" -o "$download_dir/$agent_asset"; then
      say "WARNING: cannot download $agent_asset; upload it manually in Settings -> Node Agent."
      continue
    fi
    (verify_asset "$agent_asset") || { say "WARNING: $agent_asset failed SHA-256 verification; skipped."; continue; }
    if curl -fsS --max-time 120 -X POST -H "Authorization: Bearer $admin_token" \
      -H 'Content-Type: application/octet-stream' --data-binary "@$download_dir/$agent_asset" \
      "$panel_api?version=$release_version&os=linux&arch=$arch" -o /dev/null; then
      say "Published signed Node Agent $release_version for linux/$arch."
    else
      say "WARNING: Panel rejected $agent_asset; upload it manually in Settings -> Node Agent."
    fi
  done
}

# --- Upgrade --------------------------------------------------------------

env_value() {
  sed -n "s/^$1=//p" "$install_root/.env" | tail -n 1
}

set_env_value() {
  env_tmp=$(mktemp "$install_root/.env.XXXXXX")
  chmod 0600 "$env_tmp"
  awk -v key="$1" -v value="$2" '
    BEGIN { done = 0 }
    index($0, key "=") == 1 { if (!done) print key "=" value; done = 1; next }
    { print }
    END { if (!done) print key "=" value }
  ' "$install_root/.env" > "$env_tmp"
  mv -f -- "$env_tmp" "$install_root/.env"
}

# Entries of a 1.0.x source-tree install that the image-based layout no
# longer uses. They are archived into the backup and removed after a
# successful upgrade; .env, tls/ and pki/ are never touched.
legacy_source_entries='.dockerignore .env.example CHANGELOG.md Dockerfile.panel cmd docs frontend go.mod go.sum install.sh internal migrations scripts'

upgrade_panel() {
  [ -f "$install_root/compose.yaml" ] || die "$install_root/.env exists but compose.yaml is missing"
  for path in tls/server.crt tls/server.key pki/ca.crt pki/ca.key; do
    [ -e "$install_root/$path" ] || die "$install_root/$path is missing; refusing to upgrade"
  done
  legacy_layout=0
  if grep -q 'Dockerfile.panel' "$install_root/compose.yaml"; then
    legacy_layout=1
  fi
  previous_version=$(env_value NODEFLOW_VERSION)
  say "Upgrading NodeFlow Panel in $install_root to $release_version (previous: ${previous_version:-source build})..."

  timestamp=$(date -u +%Y%m%dT%H%M%SZ)
  install -d -m 0700 "$backup_dir"
  compose up -d postgres || die "cannot start PostgreSQL for the backup"
  attempt=0
  # shellcheck disable=SC2016 # expanded by the shell inside the container
  until compose exec -T postgres sh -c 'pg_isready -q -U "$POSTGRES_USER" -d "$POSTGRES_DB"' >/dev/null 2>&1; do
    attempt=$((attempt + 1))
    [ "$attempt" -lt 60 ] || die "PostgreSQL did not become ready"
    sleep 1
  done
  db_backup=$backup_dir/nodeflow-db-$timestamp.dump
  # shellcheck disable=SC2016 # expanded by the shell inside the container
  (umask 077 && compose exec -T postgres sh -c 'pg_dump -Fc -U "$POSTGRES_USER" -d "$POSTGRES_DB"' > "$db_backup.partial") \
    || { rm -f "$db_backup.partial"; die "pg_dump failed; nothing was changed"; }
  compose exec -T postgres pg_restore --list < "$db_backup.partial" >/dev/null \
    || { rm -f "$db_backup.partial"; die "database dump is not readable; nothing was changed"; }
  mv -f -- "$db_backup.partial" "$db_backup"
  say "Database backup: $db_backup"

  config_backup=$backup_dir/nodeflow-config-$timestamp.tar.gz
  (cd "$install_root" && umask 077 && tar -czf "$config_backup" --exclude=./frontend/node_modules .) \
    || die "cannot back up $install_root"
  say "Configuration backup (including .env, tls/, pki/): $config_backup"

  cp -a -- "$install_root/compose.yaml" "$download_dir/compose.previous.yaml"
  install -m 0644 "$download_dir/compose.release.yaml" "$install_root/compose.yaml"
  cp -a -- "$install_root/.env" "$download_dir/env.previous"
  set_env_value NODEFLOW_VERSION "$release_version"

  if ! (compose config -q && compose pull && compose up -d --remove-orphans && wait_for_panel); then
    say "Upgrade failed; restoring the previous compose.yaml and .env."
    cp -a -- "$download_dir/compose.previous.yaml" "$install_root/compose.yaml"
    cp -a -- "$download_dir/env.previous" "$install_root/.env"
    compose up -d --remove-orphans >/dev/null 2>&1 || true
    die "upgrade to $release_version failed; database dump retained at $db_backup (migrations are not rolled back automatically)"
  fi

  if [ "$legacy_layout" -eq 1 ]; then
    for entry in $legacy_source_entries; do
      rm -rf -- "${install_root:?}/$entry"
    done
    say "Removed the 1.0.x source tree (archived in $config_backup); the Panel now runs $panel_image."
  fi
}

write_caddy_snippet() {
  install -d -m 0750 -o root -g caddy "$caddy_conf_dir"
  [ ! -e "$caddy_snippet" ] || die "$caddy_snippet already exists; refusing to overwrite it"

  snippet_tmp=$(mktemp "$caddy_conf_dir/.nodeflow-panel.XXXXXX")
  if [ "$auth_mode" = cookie ]; then
    bootstrap_key=$(openssl rand -hex 32)
    cookie_value=$(openssl rand -hex 32)
    cat > "$snippet_tmp" <<EOF
$domain {
    encode zstd gzip

    @nodeflow_bootstrap {
        path /__nodeflow_activate
        query key=$bootstrap_key
    }
    handle @nodeflow_bootstrap {
        route {
            header Set-Cookie "nodeflow_access=$cookie_value; Path=/; Max-Age=31536000; HttpOnly; Secure; SameSite=Strict"
            redir * / 303
        }
    }

    @nodeflow_authorized header_regexp Cookie "(^|;[ ]*)nodeflow_access=$cookie_value(;|$)"
    handle @nodeflow_authorized {
        request_body {
            max_size 70MB
        }
        reverse_proxy 127.0.0.1:8080 {
            header_up Host {host}
            header_up X-Real-IP {remote_host}
            header_up X-Forwarded-Proto https
        }
    }

    handle {
        respond "Forbidden" 403
    }
}
EOF
    activation_url="https://$domain/__nodeflow_activate?key=$bootstrap_key"
  else
    cat > "$snippet_tmp" <<EOF
$domain {
    encode zstd gzip

    request_body {
        max_size 70MB
    }
    reverse_proxy 127.0.0.1:8080 {
        header_up Host {host}
        header_up X-Real-IP {remote_host}
        header_up X-Forwarded-Proto https
    }
}
EOF
    activation_url=
  fi
  chown root:caddy "$snippet_tmp"
  chmod 0640 "$snippet_tmp"
  mv -- "$snippet_tmp" "$caddy_snippet"
}

configure_caddy() {
  [ -f "$caddyfile" ] || die "Caddyfile not found: $caddyfile"
  getent group caddy >/dev/null 2>&1 || die "Caddy system group was not created"
  timestamp=$(date -u +%Y%m%dT%H%M%SZ)
  caddy_backup="$caddyfile.nodeflow-backup.$timestamp"
  cp -a -- "$caddyfile" "$caddy_backup"
  caddy_main_changed=0

  write_caddy_snippet
  caddy_import="import $caddy_conf_dir/*.caddy"
  if ! grep -Fqx "$caddy_import" "$caddyfile"; then
    printf '\n# Managed NodeFlow site snippets\n%s\n' "$caddy_import" >> "$caddyfile"
    caddy_main_changed=1
  fi

  if ! caddy validate --config "$caddyfile" --adapter caddyfile; then
    rm -f -- "$caddy_snippet"
    if [ "$caddy_main_changed" -eq 1 ]; then
      cp -a -- "$caddy_backup" "$caddyfile"
    fi
    die "Caddy validation failed; previous Caddyfile restored"
  fi

  systemctl enable caddy >/dev/null
  if systemctl is-active --quiet caddy; then
    if ! systemctl reload caddy; then
      rm -f -- "$caddy_snippet"
      cp -a -- "$caddy_backup" "$caddyfile"
      caddy validate --config "$caddyfile" --adapter caddyfile >/dev/null 2>&1 || true
      systemctl reload caddy >/dev/null 2>&1 || true
      die "Caddy reload failed; previous configuration restored"
    fi
  elif ! systemctl start caddy; then
    rm -f -- "$caddy_snippet"
    cp -a -- "$caddy_backup" "$caddyfile"
    die "Caddy failed to start; previous configuration restored"
  fi
  systemctl is-active --quiet caddy || die "Caddy service is not active"
}

invoking_user_and_home() {
  if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != root ]; then
    invoking_user=$SUDO_USER
  else
    invoking_user=root
  fi
  invoking_home=$(getent passwd "$invoking_user" | awk -F: 'NR == 1 { print $6 }')
  [ -n "$invoking_home" ] && [ -d "$invoking_home" ] \
    || die "cannot determine home directory for $invoking_user"
  invoking_uid=$(id -u "$invoking_user")
  invoking_gid=$(id -g "$invoking_user")
  credentials_file=$invoking_home/$credentials_name
}

write_credentials() {
  admin_token=$(sed -n 's/^PANEL_ADMIN_TOKEN=//p' "$install_root/.env")
  [ -n "$admin_token" ] || die "PANEL_ADMIN_TOKEN is missing from $install_root/.env"

  credentials_tmp=$(mktemp "$invoking_home/.nodeflow-credentials.XXXXXX")
  {
    printf '%s\n' \
      "NodeFlow Panel credentials" \
      "Release: $release_tag ($release_asset)" \
      "Panel URL: https://$domain" \
      "Panel admin token: $admin_token" \
      "Agent mTLS endpoint: https://$domain:4200"
    if [ "$auth_mode" = cookie ]; then
      printf '%s\n' \
        "Caddy authorization: cookie gate" \
        "Cookie activation URL: $activation_url"
    else
      printf '%s\n' "Caddy authorization: none"
    fi
  } > "$credentials_tmp"
  chmod 0600 "$credentials_tmp"
  chown "$invoking_uid:$invoking_gid" "$credentials_tmp"
  mv -f -- "$credentials_tmp" "$credentials_file"
}

verify_installation() {
  health_response=$(curl -fsS --max-time 10 http://127.0.0.1:8080/healthz) \
    || die "direct Panel health check failed"
  printf '%s\n' "$health_response" | grep -q '"status":"ok"' \
    || die "Panel health endpoint did not report status ok"
  installed_panel_version=$(printf '%s\n' "$health_response" | jq -er '.version | select(type == "string" and length > 0)') \
    || die "Panel health endpoint did not report its version"
  if [ "$release_tag" = local ]; then
    release_tag=$installed_panel_version
  fi
  systemctl is-active --quiet caddy || die "Caddy is not active"

  if ! getent ahosts "$domain" >/dev/null 2>&1; then
    say "WARNING: $domain does not resolve yet; Caddy cannot obtain a public TLS certificate."
    say "WARNING: create the DNS record, allow ports 80/443, then verify: curl -I https://$domain/"
    return 0
  fi

  https_ready=0
  https_attempt=1
  while [ "$https_attempt" -le 12 ]; do
    if local_https_code=$(curl -ksS --noproxy '*' --max-time 10 \
      --resolve "$domain:443:127.0.0.1" -o /dev/null -w '%{http_code}' "https://$domain/"); then
      if [ "$auth_mode" = cookie ]; then
        [ "$local_https_code" = 403 ] && https_ready=1
      else
        case "$local_https_code" in
          200|301|302|303|307|308) https_ready=1 ;;
        esac
      fi
    fi
    [ "$https_ready" -eq 0 ] || break
    https_attempt=$((https_attempt + 1))
    [ "$https_attempt" -le 12 ] && sleep 5
  done

  if [ "$https_ready" -eq 0 ]; then
    say "WARNING: Caddy HTTPS is not ready yet; certificate issuance may still be in progress."
    say "WARNING: inspect it with: journalctl -u caddy -n 100 --no-pager"
    return 0
  fi

  if ! curl -sS --max-time 15 -o /dev/null "https://$domain/"; then
    say "WARNING: public HTTPS is not verified yet; check DNS and inbound ports 80/443."
  else
    say "Public HTTPS is ready."
  fi
}

main() {
  require_root "$@"
  [ "$#" -eq 0 ] || die "this installer takes no positional arguments"

  if [ -f "$install_root/.env" ]; then
    install_base_packages
    install_docker
    resolve_release
    download_release_files
    upgrade_panel
    publish_agent_releases
    domain=$(env_value PANEL_PUBLIC_URL | sed -e 's|^https\{0,1\}://||' -e 's|[:/].*$||')
    say ""
    say "NodeFlow Panel upgraded to $release_version ($panel_image)."
    say "Kept: $install_root/.env, tls/, pki/ and ${caddy_snippet}."
    if [ -e "$caddy_snippet" ] && command -v caddy >/dev/null 2>&1; then
      say "Caddy continues to serve https://$domain -> 127.0.0.1:$(env_value PANEL_PORT)."
    fi
    say "Node Agents update from the Panel: Settings -> Node Agent, then assign $release_version to the nodes."
    return
  fi

  invoking_user_and_home
  read_domain
  read_auth_mode

  [ ! -e "$install_root" ] || die "$install_root already exists; refusing to overwrite an installation"
  [ ! -e "$caddy_snippet" ] || die "$caddy_snippet already exists; refusing to overwrite it"
  [ ! -e "$credentials_file" ] || die "$credentials_file already exists; refusing to overwrite credentials"
  say "WARNING: DNS for $domain must point to this server."
  say "WARNING: ports 80/tcp and 443/tcp are required for Caddy; 4200/tcp is required for Agent mTLS."
  say "This script does not change the firewall."

  install_base_packages
  resolve_release
  download_release_files
  install_caddy
  install_docker
  prepare_install_root

  say "Installing NodeFlow Panel $release_version in $install_root..."
  start_panel
  publish_agent_releases
  configure_caddy
  write_credentials
  verify_installation

  say ""
  say "NodeFlow Panel installation completed."
  say "Credentials (also saved with mode 0600 in $credentials_file):"
  say ""
  cat "$credentials_file"
  say ""
  say "Caddy backup: $caddy_backup"
  say "No firewall rules were changed."
}

if [ "${NODEFLOW_TEST_ONLY:-}" != 1 ]; then
  main "$@"
fi
