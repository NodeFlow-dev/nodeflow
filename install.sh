#!/bin/sh
set -eu

# Interactive all-in-one installer for a new NodeFlow Panel host.
# Optional automation inputs: NODEFLOW_DOMAIN and NODEFLOW_AUTH_MODE
# (cookie or none). When no local source/install kit is available, the newest
# published GitHub Release is downloaded and verified automatically.

install_root=${NODEFLOW_INSTALL_ROOT:-/opt/nodeflow}
caddyfile=${NODEFLOW_CADDYFILE:-/etc/caddy/Caddyfile}
caddy_conf_dir=${NODEFLOW_CADDY_CONF_DIR:-/etc/caddy/conf.d}
caddy_snippet=$caddy_conf_dir/nodeflow-panel.caddy
credentials_name=nodeflow-credentials.txt
stage_dir=
download_dir=
release_tag=local
release_asset=local-source

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
  exec sudo --preserve-env=NODEFLOW_DOMAIN,NODEFLOW_AUTH_MODE "$0" "$@"
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
  apt_install ca-certificates curl jq openssl rsync
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
  while ! valid_domain "$domain"; do
    if [ -n "$domain" ]; then
      say "Invalid domain. Use a DNS name without scheme, port or path."
    fi
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

find_payload() {
  installer_path=${NODEFLOW_SCRIPT_PATH:-$0}
  script_dir=$(CDPATH= cd -- "$(dirname -- "$installer_path")" && pwd)
  for source_candidate in "$script_dir" "$script_dir/.."; do
    if [ -f "$source_candidate/compose.yaml" ] \
      && [ -x "$source_candidate/scripts/install-panel.sh" ]; then
      payload_kind=source
      payload_path=$(CDPATH= cd -- "$source_candidate" && pwd)
      release_asset=local-source
      return
    fi
  done

  for candidate in \
    "$script_dir/01-PANEL/nodeflow-panel-source.tar.gz" \
    "$script_dir/nodeflow-panel-source.tar.gz" \
    "$script_dir/../01-PANEL/nodeflow-panel-source.tar.gz" \
    "$PWD/01-PANEL/nodeflow-panel-source.tar.gz" \
    "$PWD/nodeflow-panel-source.tar.gz"
  do
    if [ -f "$candidate" ]; then
      payload_kind=archive
      payload_path=$candidate
      release_asset=bundled-source
      return
    fi
  done
  download_latest_payload
}

download_latest_payload() {
  github_repository=${NODEFLOW_GITHUB_REPOSITORY:-NodeFlow-dev/nodeflow}
  case "$github_repository" in
    */*) ;;
    *) die "invalid NODEFLOW_GITHUB_REPOSITORY" ;;
  esac
  release_api=${NODEFLOW_RELEASE_API_URL:-https://api.github.com/repos/$github_repository/releases/latest}
  download_dir=$(mktemp -d /tmp/nodeflow-release.XXXXXX)
  chmod 0700 "$download_dir"
  release_json=$download_dir/release.json

  say "Resolving the latest published NodeFlow release from GitHub..."
  curl -fsSL --retry 3 --connect-timeout 10 \
    -H 'Accept: application/vnd.github+json' \
    -H 'X-GitHub-Api-Version: 2022-11-28' \
    -H 'User-Agent: NodeFlow-installer' \
    "$release_api" -o "$release_json" \
    || die "cannot read the latest GitHub release"

  release_tag=$(jq -er '.tag_name | select(type == "string" and length > 0)' "$release_json") \
    || die "GitHub release has no tag_name"
  asset_count=$(jq '[.assets[] | select(.name | test("^NodeFlow-Panel-.+-Agent-.+-install-kit\\.tar\\.gz$"))] | length' "$release_json")
  [ "$asset_count" -eq 1 ] || die "expected exactly one install-kit asset in $release_tag; found $asset_count"
  release_asset=$(jq -er '.assets[] | select(.name | test("^NodeFlow-Panel-.+-Agent-.+-install-kit\\.tar\\.gz$")) | .name' "$release_json")
  asset_url=$(jq -er --arg name "$release_asset" '.assets[] | select(.name == $name) | .browser_download_url' "$release_json")
  asset_digest=$(jq -r --arg name "$release_asset" '.assets[] | select(.name == $name) | (.digest // "")' "$release_json")
  checksum_name=$release_asset.sha256

  outer_archive=$download_dir/$release_asset
  curl -fsSL --retry 3 --connect-timeout 10 -H 'User-Agent: NodeFlow-installer' \
    "$asset_url" -o "$outer_archive" || die "cannot download $release_asset"

  expected_sha=
  case "$asset_digest" in
    sha256:*) expected_sha=$(printf '%s' "${asset_digest#sha256:}" | tr 'A-F' 'a-f') ;;
  esac
  checksum_count=$(jq --arg name "$checksum_name" '[.assets[] | select(.name == $name)] | length' "$release_json")
  if [ "$checksum_count" -eq 1 ]; then
    checksum_url=$(jq -er --arg name "$checksum_name" '.assets[] | select(.name == $name) | .browser_download_url' "$release_json")
    checksum_file=$download_dir/$checksum_name
    curl -fsSL --retry 3 --connect-timeout 10 -H 'User-Agent: NodeFlow-installer' \
      "$checksum_url" -o "$checksum_file" || die "cannot download $checksum_name"
    checksum_sha=$(awk 'NF { print $1; exit }' "$checksum_file" | tr 'A-F' 'a-f')
    if [ -n "$expected_sha" ] && [ "$checksum_sha" != "$expected_sha" ]; then
      die "GitHub digest and $checksum_name disagree"
    fi
    expected_sha=$checksum_sha
  elif [ "$checksum_count" -ne 0 ]; then
    die "release $release_tag contains duplicate $checksum_name assets"
  fi
  case "$expected_sha" in
    ''|*[!0-9A-Fa-f]*) die "release $release_tag has no valid SHA-256 for $release_asset" ;;
  esac
  [ "${#expected_sha}" -eq 64 ] || die "invalid SHA-256 length for $release_asset"
  actual_sha=$(sha256sum "$outer_archive" | awk '{ print $1 }')
  [ "$actual_sha" = "$expected_sha" ] || die "SHA-256 mismatch for $release_asset"
  archive_is_safe "$outer_archive" || die "unsafe path found in install-kit archive"

  kit_unpack=$download_dir/unpacked
  install -d -m 0700 "$kit_unpack"
  tar -xzf "$outer_archive" -C "$kit_unpack"
  checksum_list=$(find "$kit_unpack" -mindepth 2 -maxdepth 2 -type f -name SHA256SUMS -print)
  [ "$(printf '%s\n' "$checksum_list" | awk 'NF { count++ } END { print count+0 }')" -eq 1 ] \
    || die "install kit must contain exactly one SHA256SUMS"
  kit_root=$(dirname -- "$checksum_list")
  (cd "$kit_root" && sha256sum -c SHA256SUMS >/dev/null) \
    || die "an internal install-kit checksum failed"
  payload_path=$kit_root/01-PANEL/nodeflow-panel-source.tar.gz
  [ -f "$payload_path" ] || die "install kit does not contain Panel source"
  payload_kind=archive
  say "Verified NodeFlow $release_tag: $release_asset"
}

archive_is_safe() {
  ! tar -tzf "$1" | awk '
    /^\// { bad = 1 }
    /(^|\/)\.\.($|\/)/ { bad = 1 }
    END { exit bad ? 0 : 1 }
  '
}

prepare_source() {
  [ "$install_root" = /opt/nodeflow ] \
    || [ "${NODEFLOW_ALLOW_TEST_PATHS:-}" = 1 ] \
    || die "refusing non-standard install path without NODEFLOW_ALLOW_TEST_PATHS=1"
  [ ! -e "$install_root" ] || die "$install_root already exists; refusing to overwrite an installation"

  install -d -m 0755 "$(dirname -- "$install_root")"
  stage_dir=$(mktemp -d "$(dirname -- "$install_root")/.nodeflow-install.XXXXXX")
  chmod 0750 "$stage_dir"

  if [ "$payload_kind" = source ]; then
    rsync -a \
      --exclude='.git/' \
      --exclude='.env' \
      --exclude='pki/' \
      --exclude='tls/' \
      --exclude='release/' \
      --exclude='dist/' \
      --exclude='frontend/node_modules/' \
      --exclude='frontend/dist/' \
      --exclude='internal/panel/web_dist/' \
      "$payload_path/" "$stage_dir/"
  else
    archive_is_safe "$payload_path" || die "unsafe path found in source archive"
    tar -xzf "$payload_path" -C "$stage_dir"
  fi

  [ -f "$stage_dir/compose.yaml" ] || die "payload does not contain compose.yaml"
  [ -x "$stage_dir/scripts/install-panel.sh" ] || die "payload does not contain executable scripts/install-panel.sh"
  mv -- "$stage_dir" "$install_root"
  stage_dir=
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

  local_https_code=$(curl -ksS --max-time 15 --resolve "$domain:443:127.0.0.1" \
    -o /dev/null -w '%{http_code}' "https://$domain/") \
    || die "local Caddy HTTPS request failed"
  if [ "$auth_mode" = cookie ]; then
    [ "$local_https_code" = 403 ] \
      || die "Caddy cookie gate returned HTTP $local_https_code instead of 403 without a cookie"
  else
    case "$local_https_code" in
      200|301|302|303|307|308) ;;
      *) die "Caddy returned unexpected HTTP $local_https_code" ;;
    esac
  fi

  if ! curl -sS --max-time 15 -o /dev/null "https://$domain/"; then
    say "WARNING: public HTTPS is not verified yet; check DNS and inbound ports 80/443."
  fi
}

main() {
  require_root "$@"
  [ "$#" -eq 0 ] || die "this installer takes no positional arguments"
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
  find_payload
  install_caddy
  install_docker
  prepare_source

  say "Installing NodeFlow Panel in $install_root..."
  (cd "$install_root" && ./scripts/install-panel.sh "$domain" "https://$domain" 0.0.0.0)
  configure_caddy
  verify_installation
  write_credentials

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
