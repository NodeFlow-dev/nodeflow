#!/bin/sh
set -eu

# Executes install.sh end to end against stubbed system tools: apt-get,
# systemctl, docker compose, curl (GitHub release, ipify, Panel API), getent,
# groupadd, chown and sudo are fakes that record their calls; openssl, jq, tar
# and the POSIX utilities are real. Scenarios: fresh install in both
# authorization modes (piped through stdin), idempotent re-run upgrade,
# upgrade of a 1.0.x source-tree layout, rollback when the new Panel does not
# start, a failed fresh install, release digest failures (GitHub asset
# digests, fail closed), install-kit mode, downgrade refusal, the sudo re-exec
# and the missing-terminal prompt; plus scripts/install-node.sh release
# download and its embedded systemd unit.
#
# TEST_SHELL selects the shell that runs install.sh (default: sh). A real
# Caddy binary in NODEFLOW_TEST_CADDY is used for `caddy validate/adapt`.

root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
test_shell=${TEST_SHELL:-sh}
real_caddy=${NODEFLOW_TEST_CADDY:-}
work=$(mktemp -d /tmp/nodeflow-test-install.XXXXXX)
trap 'rm -rf "$work"' EXIT
trap 'exit 1' HUP INT TERM

bin=$work/bin
stubs=$work/stubs
tools=$work/tools
rel=$work/release
mkdir -p "$bin" "$stubs" "$tools" "$rel"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  [ ! -f "$work/out" ] || sed 's/^/  | /' "$work/out" >&2
  exit 1
}
pass() { printf 'ok - %s\n' "$*"; }

# --- Real tools the installer may use (nothing else is on PATH) -----------
for tool in awk basename cat chmod cmp cp cut date dirname env gzip grep head \
  find jq ln ls mkdir mktemp mv od openssl rm sed sha256sum sort tail tar tee touch \
  tr uname wc bash "$test_shell"; do
  path=$(command -v "$tool") || fail "missing host tool: $tool"
  ln -sf "$path" "$tools/$(basename -- "$tool")"
done
ln -sf "$(command -v "$test_shell")" "$tools/sh"
real_install=$(command -v install)
real_id=$(command -v id)
# setsid detaches the installer from the controlling terminal, as over ssh -T.
setsid=$(command -v setsid) || fail "setsid is required"

# --- Stubs ------------------------------------------------------------------
stub() {
  cat > "$stubs/$1"
  chmod 0755 "$stubs/$1"
}

stub id <<EOF
#!/bin/sh
case "\$1" in
  -u) if [ "\$#" -eq 1 ]; then echo "\${FAKE_UID:-0}"; else echo 0; fi ;;
  -g) echo 0 ;;
  *) exec "$real_id" "\$@" ;;
esac
EOF
stub install <<EOF
#!/bin/sh
# Ownership needs root; the stub keeps modes and drops -o/-g.
n=\$#; i=0
while [ "\$i" -lt "\$n" ]; do
  a=\$1; shift; i=\$((i + 1))
  case "\$a" in
    -o|-g) shift; i=\$((i + 1)) ;;
    *) set -- "\$@" "\$a" ;;
  esac
done
exec "$real_install" "\$@"
EOF
stub chown <<'EOF'
#!/bin/sh
echo "chown $*" >> "$NF_STATE/calls"
EOF
stub groupadd <<'EOF'
#!/bin/sh
echo "groupadd $*" >> "$NF_STATE/calls"; touch "$NF_STATE/group_runtime"
EOF
stub sleep <<'EOF'
#!/bin/sh
exit 0
EOF
stub gpg <<'EOF'
#!/bin/sh
cat > /dev/null
EOF
stub dpkg <<'EOF'
#!/bin/sh
echo amd64
EOF
stub apt-cache <<'EOF'
#!/bin/sh
exit 0
EOF
stub apt-get <<'EOF'
#!/bin/sh
echo "apt-get $*" >> "$NF_STATE/calls"
for p in "$@"; do
  case "$p" in
    caddy) ln -sf "$NF_STUBS/caddy" "$NF_BIN/caddy"; touch "$NF_STATE/group_caddy" ;;
    docker.io) ln -sf "$NF_STUBS/docker" "$NF_BIN/docker" ;;
  esac
done
EOF
stub systemctl <<'EOF'
#!/bin/sh
echo "systemctl $*" >> "$NF_STATE/calls"
case "$1" in
  enable) [ "$2" != --now ] || touch "$NF_STATE/active_$3" ;;
  start) touch "$NF_STATE/active_$2" ;;
  is-active) [ -f "$NF_STATE/active_$3" ] ;;
esac
EOF
stub getent <<'EOF'
#!/bin/sh
case "$1:$2" in
  ahostsv4:panel.example.test|ahosts:panel.example.test) echo "203.0.113.10 STREAM $2" ;;
  group:caddy) [ -f "$NF_STATE/group_caddy" ] && echo "caddy:x:990:" ;;
  group:65532|group:nodeflow-runtime) [ -f "$NF_STATE/group_runtime" ] && echo "nodeflow-runtime:x:65532:" ;;
  passwd:root) echo "root:x:0:0:root:$NF_HOME:/bin/sh" ;;
  *) exit 2 ;;
esac
EOF
stub sudo <<'EOF'
#!/bin/sh
printf '%s\n' "$@" > "$NF_STATE/sudo_argv"
case "$1" in --preserve-env=*) shift ;; esac
FAKE_UID=0 exec "$@"
EOF
if [ -n "$real_caddy" ]; then
  ln -sf "$real_caddy" "$stubs/caddy"
else
  stub caddy <<'EOF'
#!/bin/sh
[ "$1" = validate ] && [ -f "$3" ]
EOF
fi
stub docker <<'EOF'
#!/bin/sh
[ "$1" = compose ] || exit 1
shift
echo "compose $*" >> "$NF_STATE/calls"
case "$1" in
  version) echo "Docker Compose version v2" ;;
  config)
    [ -f compose.yaml ] && [ -f .env ] || exit 1
    grep -q 'dockerfile: Dockerfile.panel' compose.yaml \
      || grep -q 'ghcr.io/nodeflow-dev/nodeflow-panel:${NODEFLOW_VERSION' compose.yaml ;;
  pull) [ -z "${FAIL_PULL:-}" ] ;;
  up)
    if [ "$*" = "up -d postgres" ]; then exit 0; fi
    if grep -q 'dockerfile: Dockerfile.panel' compose.yaml; then v=1.0.8
    else v=$(sed -n 's/^NODEFLOW_VERSION=//p' .env); fi
    if [ "${FAIL_UP_VERSION:-}" = "$v" ]; then rm -f "$NF_STATE/panel_version"; exit 1; fi
    echo "$v" > "$NF_STATE/panel_version" ;;
  exec)
    case "$*" in
      *pg_isready*) exit 0 ;;
      *pg_dump*) [ -z "${FAIL_PGDUMP:-}" ] && printf 'PGDMP-fake-dump' ;;
      *pg_restore*) [ "$(head -c 5)" = PGDMP ] ;;
      *) exit 1 ;;
    esac ;;
esac
EOF
stub curl <<'EOF'
#!/bin/sh
out=; url=; w=; method=GET; data=; auth=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out=$2; shift ;;
    -w) w=$2; shift ;;
    -X) method=$2; shift ;;
    --data-binary) data=${2#@}; shift ;;
    -H) case "$2" in Authorization:*) auth=$2 ;; esac; shift ;;
    --connect-timeout|--max-time|--retry|--resolve|--noproxy) shift ;;
    http://*|https://*) url=$1 ;;
  esac
  shift
done
echo "curl $method $url" >> "$NF_STATE/calls"
emit() { if [ -n "$out" ] && [ "$out" != /dev/null ]; then cat > "$out"; else cat; fi; }
token=$(sed -n 's/^PANEL_ADMIN_TOKEN=//p' "$NF_ROOT/.env" 2>/dev/null)
case "$url" in
  https://api.ipify.org) printf '203.0.113.10' ;;
  https://api64.ipify.org) exit 6 ;;
  https://api.github.com/repos/NodeFlow-dev/nodeflow/releases/latest)
    [ -f "$NF_RELEASE/release.json" ] || exit 22
    emit < "$NF_RELEASE/release.json" ;;
  https://api.github.com/repos/NodeFlow-dev/nodeflow/releases/tags/*)
    [ -f "$NF_RELEASE/release.json" ] || exit 22
    [ "$(jq -r .tag_name "$NF_RELEASE/release.json")" = "${url##*/}" ] || exit 22
    emit < "$NF_RELEASE/release.json" ;;
  https://release.test/*)
    [ -f "$NF_RELEASE/${url##*/}" ] || exit 22
    emit < "$NF_RELEASE/${url##*/}" ;;
  http://127.0.0.1:8080/healthz)
    [ -f "$NF_STATE/panel_version" ] || exit 7
    printf '{"status":"ok","version":"%s"}\n' "$(cat "$NF_STATE/panel_version")" | emit ;;
  http://127.0.0.1:8080/api/v1/agent-releases*)
    [ "$auth" = "Authorization: Bearer $token" ] || exit 22
    [ -f "$NF_STATE/agent_releases" ] || echo '[]' > "$NF_STATE/agent_releases"
    if [ "$method" = POST ]; then
      [ -s "$data" ] || exit 22
      q=${url#*\?}
      v=$(printf '%s\n' "$q" | sed 's/.*version=\([^&]*\).*/\1/')
      a=$(printf '%s\n' "$q" | sed 's/.*arch=\([^&]*\).*/\1/')
      jq --arg v "$v" --arg a "$a" '. + [{version: $v, os: "linux", arch: $a}]' \
        "$NF_STATE/agent_releases" > "$NF_STATE/agent_releases.new"
      mv "$NF_STATE/agent_releases.new" "$NF_STATE/agent_releases"
      echo '{}' | emit
    else
      emit < "$NF_STATE/agent_releases"
    fi ;;
  https://panel.example.test/)
    code=200
    grep -q nodeflow_authorized "$NF_CONF/nodeflow-panel.caddy" && code=403
    [ -z "$w" ] || printf '%s' "$code" ;;
  https://dl.cloudsmith.io/*|https://download.docker.com/*) echo stub | emit ;;
  *) exit 6 ;;
esac
EOF

# --- Release fixture --------------------------------------------------------
# $rel holds what the fake GitHub serves: release.json (GitHub REST release
# object with assets[].digest = "sha256:<hex>") and the three assets: the
# install kit and the Node Agent binaries. LAYOUT=legacy puts the compose
# file where the 2.0.0 kit had it (01-PANEL/).
expected_compose=$work/expected-compose.yaml
make_release() {
  version=$1
  kit=NodeFlow-Panel-$version-Agent-$version-install-kit
  rm -rf "$rel" "$work/kit-src" && mkdir -p "$rel" "$work/kit-src/$kit"
  sed "s/NODEFLOW_VERSION:-2\.0\.0/NODEFLOW_VERSION:-$version/" \
    "$root/compose.release.yaml" > "$expected_compose"
  if [ "${LAYOUT:-}" = legacy ]; then
    mkdir -p "$work/kit-src/$kit/01-PANEL"
    cp "$expected_compose" "$work/kit-src/$kit/01-PANEL/compose.release.yaml"
  else
    cp "$expected_compose" "$work/kit-src/$kit/compose.release.yaml"
  fi
  cp "$root/install.sh" "$work/kit-src/$kit/install.sh"
  (cd "$work/kit-src" && tar -czf "$rel/$kit.tar.gz" "$kit")
  for arch in amd64 arm64; do
    printf 'fake agent %s %s\n' "$version" "$arch" > "$rel/nodeflow-node-agent-$version-linux-$arch"
  done
  release_json
}

# release_json: release.json with the current digests of every asset in $rel.
release_json() {
  for f in "$rel"/*; do
    name=${f##*/}
    [ "$name" != release.json ] || continue
    jq -n --arg n "$name" --arg d "sha256:$(sha256sum "$f" | cut -d' ' -f1)" \
      '{name: $n, digest: $d, browser_download_url: ("https://release.test/" + $n)}'
  done | jq -s --arg t "v$version" '{tag_name: $t, assets: .}' > "$rel/release.json.new"
  mv "$rel/release.json.new" "$rel/release.json"
}

# edit_release JQ: rewrites release.json.
edit_release() {
  jq "$1" "$rel/release.json" > "$rel/release.json.new" && mv "$rel/release.json.new" "$rel/release.json"
}

# --- Per-scenario host ------------------------------------------------------
new_host() {
  host=$work/host
  rm -rf "$host" "$bin" && mkdir -p "$host/state" "$host/home" "$host/etc/caddy" "$bin"
  for s in "$stubs"/*; do
    case "${s##*/}" in caddy|docker) ;; *) ln -s "$s" "$bin/${s##*/}" ;; esac
  done
  printf 'ID=debian\nVERSION_CODENAME=bookworm\n' > "$host/os-release"
  printf ':80 {\n\trespond "default"\n}\n' > "$host/etc/caddy/Caddyfile"
  NF_STATE=$host/state NF_HOME=$host/home NF_ROOT=$host/opt/nodeflow
  NF_CONF=$host/etc/caddy/conf.d NF_BIN=$bin NF_STUBS=$stubs NF_RELEASE=$rel
  export NF_STATE NF_HOME NF_ROOT NF_CONF NF_BIN NF_STUBS NF_RELEASE
}

# host_with_tools: Docker and Caddy already installed (upgrade hosts).
host_with_tools() {
  new_host
  ln -sf "$stubs/docker" "$bin/docker"
  ln -sf "$stubs/caddy" "$bin/caddy"
  touch "$NF_STATE/group_caddy" "$NF_STATE/group_runtime" "$NF_STATE/active_caddy"
}

# run_installer [VAR=value...]: pipes install.sh into the shell like
# `curl ... | sudo sh`, detached from any terminal. $launch overrides the
# command (then it must be set back to empty).
launch=
run_installer() {
  if [ -z "$launch" ]; then
    set -- "$@" "$setsid" "$tools/sh" -c "exec '$tools/sh' < '$root/install.sh'"
  else
    set -- "$@" "$script_bin" -qec "$launch" /dev/null
  fi
  env -i PATH="$bin:$tools" HOME="$NF_HOME" NF_STATE="$NF_STATE" NF_HOME="$NF_HOME" \
    NF_ROOT="$NF_ROOT" NF_CONF="$NF_CONF" NF_BIN="$NF_BIN" NF_STUBS="$NF_STUBS" \
    NF_RELEASE="$NF_RELEASE" \
    NODEFLOW_ALLOW_TEST_PATHS=1 NODEFLOW_INSTALL_ROOT="$NF_ROOT" \
    NODEFLOW_CADDYFILE="$host/etc/caddy/Caddyfile" NODEFLOW_CADDY_CONF_DIR="$NF_CONF" \
    NODEFLOW_BACKUP_DIR="$host/backups" NODEFLOW_OS_RELEASE="$host/os-release" \
    NODEFLOW_RELEASE_BASE_URL=https://release.test \
    "$@" > "$work/out" 2>&1
}

expect_ok() { run_installer "$@" || fail "installer failed ($*)"; }
expect_fail() {
  pattern=$1; shift
  if run_installer "$@"; then fail "installer succeeded, expected: $pattern"; fi
  grep -Fq -- "$pattern" "$work/out" || fail "expected error: $pattern"
}
env_of() { sed -n "s/^$1=//p" "$NF_ROOT/.env"; }
digest() { (cd "$NF_ROOT" && cat .env tls/* pki/* "$NF_CONF/nodeflow-panel.caddy" | sha256sum); }

check_caddy() {
  mode=$1
  snippet=$NF_CONF/nodeflow-panel.caddy
  grep -Fxq "import $NF_CONF/*.caddy" "$host/etc/caddy/Caddyfile" || fail "Caddyfile import missing"
  grep -q 'reverse_proxy 127.0.0.1:8080' "$snippet" || fail "snippet upstream"
  [ -n "$real_caddy" ] || return 0
  "$real_caddy" adapt --config "$host/etc/caddy/Caddyfile" --adapter caddyfile \
    > "$work/caddy.json" 2>/dev/null || fail "caddy adapt ($mode)"
  jq -e '[.. | objects | select(.handler? == "reverse_proxy") | .upstreams[].dial] == ["127.0.0.1:8080"]' \
    "$work/caddy.json" >/dev/null || fail "caddy upstream is not only 127.0.0.1:8080 ($mode)"
  jq -e '[.. | objects | .host? // empty | .[]] | index("panel.example.test") != null' \
    "$work/caddy.json" >/dev/null || fail "caddy site host ($mode)"
  if [ "$mode" = cookie ]; then
    jq -e '[.. | objects | select(.handler? == "static_response") | .status_code] | map(tostring) | index("403") != null' \
      "$work/caddy.json" >/dev/null || fail "cookie gate has no 403 fallback"
  fi
}

check_fresh() {
  mode=$1
  [ "$(cat "$NF_STATE/panel_version")" = 2.0.0 ] || fail "panel not started"
  cmp -s "$NF_ROOT/compose.yaml" "$expected_compose" || fail "compose.yaml is not the release file"
  [ "$(env_of NODEFLOW_VERSION)" = 2.0.0 ] || fail "NODEFLOW_VERSION"
  [ "$(env_of PANEL_BIND_ADDR)" = 127.0.0.1 ] || fail "browser port is not loopback-only"
  [ "$(env_of PANEL_AGENT_TLS_BIND_ADDR)" = 0.0.0.0 ] || fail "Agent mTLS port not published"
  [ "$(env_of PANEL_PUBLIC_URL)" = https://panel.example.test ] || fail "PANEL_PUBLIC_URL"
  [ -z "$(find "$NF_ROOT/.env" -perm /0077)" ] || fail ".env mode"
  openssl verify -CAfile "$NF_ROOT/pki/ca.crt" "$NF_ROOT/tls/server.crt" >/dev/null || fail "mTLS cert"
  openssl x509 -in "$NF_ROOT/tls/server.crt" -noout -ext subjectAltName | grep -q 'DNS:panel.example.test' \
    || fail "mTLS SAN"
  [ -f "$NF_ROOT/pki/update-signing.key" ] || fail "update-signing key"
  [ ! -e "$NF_ROOT/.install-incomplete" ] || fail "incomplete marker left behind"
  [ "$(jq length "$NF_STATE/agent_releases")" = 2 ] || fail "Agent releases not published"
  creds=$NF_HOME/nodeflow-credentials.txt
  grep -q "Panel admin token: $(env_of PANEL_ADMIN_TOKEN)" "$creds" || fail "credentials token"
  if [ "$mode" = cookie ]; then
    grep -q 'Cookie activation URL: https://panel.example.test/__nodeflow_activate?key=' "$creds" \
      || fail "activation URL"
  else
    ! grep -q 'Cookie activation URL' "$creds" || fail "activation URL in none mode"
  fi
  check_caddy "$mode"
  if [ -n "$real_compose" ]; then
    (cd "$NF_ROOT" && $real_compose config --format json) > "$work/compose.json" 2>/dev/null \
      || fail "docker compose config"
    jq -e '[.services["panel-api"].ports[] | "\(.host_ip):\(.published)"] | sort == ["0.0.0.0:4200", "127.0.0.1:8080"]' \
      "$work/compose.json" >/dev/null || fail "published ports differ from 127.0.0.1:8080 + 0.0.0.0:4200"
  fi
}

real_compose=
if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
  real_compose="$(command -v docker) compose"
elif command -v docker-compose >/dev/null 2>&1; then
  real_compose=$(command -v docker-compose)
fi

make_release 2.0.0

# 1. Fresh install, cookie mode, Docker and Caddy installed from apt.
new_host
expect_ok NODEFLOW_DOMAIN=panel.example.test NODEFLOW_AUTH_MODE=cookie
check_fresh cookie
grep -q 'apt-get install -y --no-install-recommends caddy' "$NF_STATE/calls" || fail "Caddy not installed"
grep -q 'apt-get install -y --no-install-recommends docker.io docker-compose-v2' "$NF_STATE/calls" \
  || fail "Docker not installed"
! grep -q 'compose build' "$NF_STATE/calls" || fail "installer built an image"
pass "fresh install (cookie), piped via stdin"

# 2. Re-run on the same host: 2.0.0 -> 2.0.0 upgrade keeps everything.
before=$(digest)
cp "$NF_HOME/nodeflow-credentials.txt" "$work/creds"
expect_ok
[ "$(digest)" = "$before" ] || fail "re-run changed .env, tls/, pki/ or the Caddy snippet"
cmp -s "$work/creds" "$NF_HOME/nodeflow-credentials.txt" || fail "re-run changed credentials"
ls "$host/backups"/nodeflow-db-*.dump >/dev/null 2>&1 || fail "no pg_dump backup"
[ "$(grep -c 'compose pull' "$NF_STATE/calls")" -eq 2 ] || fail "re-run did not pull"
[ "$(grep -c 'POST http://127.0.0.1:8080/api/v1/agent-releases' "$NF_STATE/calls")" -eq 2 ] \
  || fail "re-run published Agent releases again"
grep -q 'NodeFlow Panel upgraded to 2.0.0' "$work/out" || fail "upgrade message"
pass "re-run is an idempotent upgrade"

# 3. Downgrade is refused before anything changes.
sed -i 's/^NODEFLOW_VERSION=.*/NODEFLOW_VERSION=2.1.0/' "$NF_ROOT/.env"
expect_fail 'refusing to downgrade NodeFlow Panel 2.1.0 to 2.0.0' NODEFLOW_VERSION=2.0.0
pass "downgrade refused"

# 4. Fresh install, no Caddy gate.
new_host
expect_ok NODEFLOW_DOMAIN=panel.example.test NODEFLOW_AUTH_MODE=none
check_fresh none
pass "fresh install (none)"

# 5. Piped without a terminal and without answers: clean error, no reads
#    from the script on stdin.
new_host
expect_fail 'no terminal; set NODEFLOW_DOMAIN and NODEFLOW_AUTH_MODE'
[ ! -e "$NF_ROOT" ] || fail "install root created without answers"
pass "missing terminal is reported"

# 5b. Interactive answers on the terminal while the script itself arrives on
#     stdin (curl | sudo sh): an invalid domain is re-asked, then the menu.
script_bin=$(command -v script || true)
if [ -n "$script_bin" ]; then
  new_host
  printf 'bad_domain\npanel.example.test\n9\n2\n' > "$work/answers"
  launch="cat '$root/install.sh' | '$tools/sh'"
  run_installer < "$work/answers" || fail "interactive piped install failed"
  launch=
  grep -q 'Invalid domain' "$work/out" || fail "invalid domain not re-asked"
  grep -q 'Unknown authorization mode: 9' "$work/out" || fail "invalid mode not re-asked"
  check_fresh none
  pass "interactive prompts via /dev/tty while piped"
fi

# 6. Non-root: re-exec through sudo with the automation variables preserved.
new_host
tmp_script=$work/install-copy.sh
cp "$root/install.sh" "$tmp_script"
chmod 0644 "$tmp_script"
env -i PATH="$bin:$tools" HOME="$NF_HOME" NF_STATE="$NF_STATE" NF_HOME="$NF_HOME" \
  NF_ROOT="$NF_ROOT" NF_CONF="$NF_CONF" NF_BIN="$NF_BIN" NF_STUBS="$NF_STUBS" NF_RELEASE="$NF_RELEASE" \
  FAKE_UID=1000 NODEFLOW_ALLOW_TEST_PATHS=1 NODEFLOW_INSTALL_ROOT="$NF_ROOT" \
  NODEFLOW_CADDYFILE="$host/etc/caddy/Caddyfile" NODEFLOW_CADDY_CONF_DIR="$NF_CONF" \
  NODEFLOW_BACKUP_DIR="$host/backups" NODEFLOW_OS_RELEASE="$host/os-release" \
  NODEFLOW_RELEASE_BASE_URL=https://release.test \
  NODEFLOW_DOMAIN=panel.example.test NODEFLOW_AUTH_MODE=none \
  "$setsid" "$tools/sh" "$tmp_script" > "$work/out" 2>&1 || fail "sudo re-exec install failed"
[ "$(sed -n 2p "$NF_STATE/sudo_argv")" = sh ] || fail "sudo must run the script through sh"
[ "$(sed -n 3p "$NF_STATE/sudo_argv")" = "$tmp_script" ] || fail "sudo script path"
sed -n 1p "$NF_STATE/sudo_argv" | grep -q 'NODEFLOW_DOMAIN,NODEFLOW_AUTH_MODE,NODEFLOW_VERSION' \
  || fail "sudo does not preserve the automation variables"
pass "non-root re-exec via sudo sh"
# Piped as non-root: refused with the right hint.
if run_installer FAKE_UID=1000; then fail "piped non-root run succeeded"; fi
grep -q 'curl -fsSL URL | sudo sh' "$work/out" || fail "piped non-root hint"
pass "piped non-root run refused"

# 7. Upgrade of a 1.0.x source-tree install made by the old install.sh.
make_legacy() {
  host_with_tools
  mkdir -p "$NF_ROOT/tls" "$NF_ROOT/pki" "$NF_CONF" "$host/backups"
  cp "$root/compose.yaml" "$NF_ROOT/compose.yaml"
  for d in cmd docs frontend internal migrations scripts; do
    mkdir -p "$NF_ROOT/$d" && echo legacy > "$NF_ROOT/$d/file"
  done
  for f in .dockerignore .env.example CHANGELOG.md Dockerfile.panel go.mod go.sum install.sh; do
    echo legacy > "$NF_ROOT/$f"
  done
  cat > "$NF_ROOT/.env" <<ENV
POSTGRES_DB=nodeflow
POSTGRES_USER=nodeflow
POSTGRES_PASSWORD=legacy-password
PANEL_ADMIN_TOKEN=legacy-admin-token-0123456789abcdef0123456789
PANEL_PORT=8080
PANEL_BIND_ADDR=127.0.0.1
ALLOW_INSECURE_HTTP=false
PANEL_PUBLIC_URL=https://panel.example.test
DATABASE_MAX_CONNS=10
PANEL_AGENT_PUBLIC_URL=https://panel.example.test:4200
PANEL_AGENT_TLS_LISTEN_ADDR=:4200
PANEL_AGENT_TLS_BIND_ADDR=0.0.0.0
PANEL_AGENT_TLS_PORT=4200
PANEL_AGENT_TLS_CERT_FILE=/tls/server.crt
PANEL_AGENT_TLS_KEY_FILE=/tls/server.key
PANEL_AGENT_TLS_CLIENT_CA_FILE=/pki/ca.crt
PANEL_AGENT_TLS_ISSUER_KEY_FILE=/pki/ca.key
PANEL_REQUIRE_AGENT_MTLS=true
PANEL_UPDATE_SIGNING_KEY_FILE=/pki/update-signing.key
ENV
  chmod 0600 "$NF_ROOT/.env"
  for f in tls/server.crt tls/server.key pki/ca.crt pki/ca.key pki/update-signing.key pki/update-signing.pub; do
    echo "legacy $f" > "$NF_ROOT/$f"
  done
  printf 'panel.example.test {\n\treverse_proxy 127.0.0.1:8080\n}\n' > "$NF_CONF/nodeflow-panel.caddy"
  printf '\n# Managed NodeFlow site snippets\nimport %s/*.caddy\n' "$NF_CONF" >> "$host/etc/caddy/Caddyfile"
  echo 1.0.8 > "$NF_STATE/panel_version"
}

make_legacy
before=$(digest)
cp "$NF_ROOT/.env" "$work/legacy.env"
expect_ok
[ "$(cat "$NF_STATE/panel_version")" = 2.0.0 ] || fail "legacy upgrade did not start 2.0.0"
cmp -s "$NF_ROOT/compose.yaml" "$expected_compose" || fail "legacy compose.yaml not replaced"
[ "$(env_of NODEFLOW_VERSION)" = 2.0.0 ] || fail "legacy NODEFLOW_VERSION"
grep -v '^NODEFLOW_VERSION=' "$NF_ROOT/.env" | cmp -s - "$work/legacy.env" || fail "legacy .env secrets changed"
for e in cmd docs frontend internal migrations scripts Dockerfile.panel go.mod install.sh; do
  [ ! -e "$NF_ROOT/$e" ] || fail "legacy $e not removed"
done
for f in tls/server.key pki/ca.key pki/update-signing.key; do
  grep -qx "legacy $f" "$NF_ROOT/$f" || fail "legacy $f changed"
done
tar -tzf "$host"/backups/nodeflow-config-*.tar.gz | grep -qx './.env' || fail "config backup lacks .env"
tar -tzf "$host"/backups/nodeflow-config-*.tar.gz | grep -qx './cmd/file' || fail "config backup lacks source"
[ "$before" != "$(digest)" ] || fail "digest unexpectedly identical (NODEFLOW_VERSION added)"
pass "1.0.x source-tree install upgraded in place"

# 8. Rollback: the new Panel does not start -> previous compose.yaml/.env.
make_legacy
cp "$NF_ROOT/compose.yaml" "$work/legacy.compose"
cp "$NF_ROOT/.env" "$work/legacy.env"
expect_fail 'upgrade to 2.0.0 failed; database dump retained' FAIL_UP_VERSION=2.0.0
cmp -s "$NF_ROOT/compose.yaml" "$work/legacy.compose" || fail "compose.yaml not restored"
cmp -s "$NF_ROOT/.env" "$work/legacy.env" || fail ".env not restored"
[ -f "$NF_ROOT/Dockerfile.panel" ] || fail "source tree removed after a failed upgrade"
[ "$(cat "$NF_STATE/panel_version")" = 1.0.8 ] || fail "previous Panel not restarted"
pass "failed upgrade rolls back"

# 9. pg_dump failure stops the upgrade before any change.
make_legacy
cp "$NF_ROOT/compose.yaml" "$work/legacy.compose"
expect_fail 'pg_dump failed; nothing was changed' FAIL_PGDUMP=1
cmp -s "$NF_ROOT/compose.yaml" "$work/legacy.compose" || fail "compose changed after pg_dump failure"
pass "pg_dump failure aborts"

# 10. GitHub asset digests fail closed.
kit_asset=NodeFlow-Panel-2.0.0-Agent-2.0.0-install-kit.tar.gz
fresh='NODEFLOW_DOMAIN=panel.example.test NODEFLOW_AUTH_MODE=none'
new_host
# shellcheck disable=SC2086 # $fresh is a list of VAR=value words
check_no_install() {
  expect_fail "$@" $fresh
  [ ! -e "$NF_ROOT" ] || fail "install root created after: $1"
}
printf 'tampered' >> "$rel/$kit_asset"
check_no_install "SHA-256 mismatch for $kit_asset"
make_release 2.0.0
edit_release "del(.assets[] | select(.name == \"$kit_asset\"))"
check_no_install "release v2.0.0 has no asset $kit_asset"
make_release 2.0.0
edit_release "[.assets[] | select(.name == \"$kit_asset\")] as \$k | .assets += \$k"
check_no_install "lists asset $kit_asset more than once"
make_release 2.0.0
edit_release "(.assets[] | select(.name == \"$kit_asset\") | .digest) = null"
check_no_install "release v2.0.0 has no SHA-256 digest for $kit_asset"
make_release 2.0.0
edit_release "(.assets[] | select(.name == \"$kit_asset\") | .digest) = \"sha1:$(printf '0%.0s' $(seq 1 40))\""
check_no_install "release v2.0.0 has no SHA-256 digest for $kit_asset"
make_release 2.0.0
rm "$rel/$kit_asset"
check_no_install "cannot download $kit_asset from v2.0.0"
make_release 2.0.0
check_no_install 'cannot read GitHub release v2.0.1' NODEFLOW_VERSION=2.0.1
make_release 2.0.0
rm "$rel/release.json"
check_no_install 'cannot read the latest GitHub release'
make_release 2.0.0
echo tampered >> "$rel/nodeflow-node-agent-2.0.0-linux-arm64"
expect_ok NODEFLOW_DOMAIN=panel.example.test NODEFLOW_AUTH_MODE=none
grep -q 'SHA-256 mismatch for nodeflow-node-agent-2.0.0-linux-arm64' "$work/out" || fail "Agent digest error"
grep -q 'WARNING: nodeflow-node-agent-2.0.0-linux-arm64 was not downloaded and verified' "$work/out" || fail "Agent digest warning"
[ "$(jq -r '[.[].arch] | join(",")' "$NF_STATE/agent_releases")" = amd64 ] || fail "tampered Agent uploaded"
new_host
make_release 2.0.0
edit_release 'del(.assets[] | select(.name == "nodeflow-node-agent-2.0.0-linux-amd64"))'
expect_ok NODEFLOW_DOMAIN=panel.example.test NODEFLOW_AUTH_MODE=none
grep -q 'release v2.0.0 has no asset nodeflow-node-agent-2.0.0-linux-amd64' "$work/out" || fail "missing Agent asset not reported"
[ "$(jq -r '[.[].arch] | join(",")' "$NF_STATE/agent_releases")" = arm64 ] || fail "missing Agent asset uploaded"
make_release 2.0.0
pass "GitHub asset digests fail closed"

# 10b. A pinned version reads /releases/tags/v<version>; the 2.0.0 kit layout
#      (compose file in 01-PANEL/) is still understood.
new_host
LAYOUT=legacy make_release 2.0.0
expect_ok NODEFLOW_DOMAIN=panel.example.test NODEFLOW_AUTH_MODE=none NODEFLOW_VERSION=v2.0.0
check_fresh none
grep -q 'curl GET https://api.github.com/repos/NodeFlow-dev/nodeflow/releases/tags/v2.0.0' "$NF_STATE/calls" \
  || fail "pinned version did not read the tag release"
! grep -q 'releases/latest' "$NF_STATE/calls" || fail "pinned version read the latest release"
make_release 2.0.0
pass "pinned version and 2.0.0 kit layout"

# 10c. install.sh started from an extracted install kit uses the kit's
#      compose file and does not download the kit again.
new_host
kit_dir=$work/kit-run/NodeFlow-Panel-2.0.0-Agent-2.0.0-install-kit
rm -rf "$work/kit-run" && mkdir -p "$kit_dir"
cp "$root/install.sh" "$kit_dir/install.sh"
cp "$expected_compose" "$kit_dir/compose.release.yaml"
(cd "$kit_dir" && sha256sum ./compose.release.yaml ./install.sh > SHA256SUMS)
launch_kit() {
  env -i PATH="$bin:$tools" HOME="$NF_HOME" NF_STATE="$NF_STATE" NF_HOME="$NF_HOME" \
    NF_ROOT="$NF_ROOT" NF_CONF="$NF_CONF" NF_BIN="$NF_BIN" NF_STUBS="$NF_STUBS" NF_RELEASE="$NF_RELEASE" \
    NODEFLOW_ALLOW_TEST_PATHS=1 NODEFLOW_INSTALL_ROOT="$NF_ROOT" \
    NODEFLOW_CADDYFILE="$host/etc/caddy/Caddyfile" NODEFLOW_CADDY_CONF_DIR="$NF_CONF" \
    NODEFLOW_BACKUP_DIR="$host/backups" NODEFLOW_OS_RELEASE="$host/os-release" \
    NODEFLOW_RELEASE_BASE_URL=https://release.test \
    NODEFLOW_DOMAIN=panel.example.test NODEFLOW_AUTH_MODE=none "$@" \
    "$setsid" "$tools/sh" "$kit_dir/install.sh" > "$work/out" 2>&1
}
launch_kit || fail "install from the kit failed"
check_fresh none
grep -q "compose.release.yaml from install kit $kit_dir" "$work/out" || fail "kit compose not used"
! grep -q "curl GET https://release.test/$kit_asset" "$NF_STATE/calls" || fail "kit mode downloaded the kit"
new_host
if launch_kit NODEFLOW_VERSION=2.0.1; then fail "kit accepted another NODEFLOW_VERSION"; fi
grep -q 'differs from the install kit version 2.0.0' "$work/out" || fail "kit version mismatch message"
printf '# tampered\n' >> "$kit_dir/compose.release.yaml"
if launch_kit; then fail "tampered kit accepted"; fi
grep -q 'install kit files do not match its SHA256SUMS' "$work/out" || fail "tampered kit message"
[ ! -e "$NF_ROOT" ] || fail "install root created from a tampered kit"
pass "install from an extracted kit"

# 11. A failed fresh install is not later mistaken for an upgrade.
new_host
expect_fail 'cannot pull the NodeFlow images' NODEFLOW_DOMAIN=panel.example.test NODEFLOW_AUTH_MODE=none FAIL_PULL=1
[ -e "$NF_ROOT/.install-incomplete" ] || fail "incomplete marker missing"
expect_fail 'the previous installation in' NODEFLOW_DOMAIN=panel.example.test NODEFLOW_AUTH_MODE=none
! grep -q 'Upgrading NodeFlow' "$work/out" || fail "incomplete install treated as upgrade"
pass "failed fresh install is detected on re-run"

# 12. Invalid automation input.
new_host
expect_fail 'NODEFLOW_DOMAIN is invalid' NODEFLOW_DOMAIN=https://panel.example.test NODEFLOW_AUTH_MODE=none
expect_fail 'NODEFLOW_DOMAIN does not point to this server' NODEFLOW_DOMAIN=other.example.test NODEFLOW_AUTH_MODE=none
expect_fail 'NODEFLOW_VERSION is not a MAJOR.MINOR.PATCH version' NODEFLOW_DOMAIN=panel.example.test NODEFLOW_AUTH_MODE=none NODEFLOW_VERSION=latest
pass "invalid inputs rejected"

# 13. scripts/install-node.sh: embedded unit equals the repository unit, the
#     Agent asset is verified against its GitHub digest.
node_driver() {
  env -i PATH="$bin:$tools" NF_STATE="$NF_STATE" NF_RELEASE="$NF_RELEASE" \
    NODEFLOW_RELEASE_BASE_URL=https://release.test "$@" \
    bash -c 'NODEFLOW_TEST_ONLY=1; . "$0"; work_dir=$(mktemp -d); trap "rm -rf \"\$work_dir\"" EXIT
      version=; resolve_release; fetch_release_asset "nodeflow-node-agent-$version-linux-amd64"
      cat "$work_dir/nodeflow-node-agent-$version-linux-amd64"' "$root/scripts/install-node.sh" > "$work/out" 2>&1
}
new_host
bash -c 'NODEFLOW_TEST_ONLY=1; . "$0"; write_agent_unit "$1"' \
  "$root/scripts/install-node.sh" "$work/embedded.service" || fail "cannot render the embedded unit"
cmp -s "$work/embedded.service" "$root/configs/systemd/nodeflow-node-agent.service" \
  || fail "install-node.sh unit differs from configs/systemd/nodeflow-node-agent.service"
node_driver || fail "install-node.sh latest release download"
grep -qx 'fake agent 2.0.0 amd64' "$work/out" || fail "install-node.sh downloaded the wrong Agent"
node_driver NODEFLOW_VERSION=2.0.0 || fail "install-node.sh pinned release download"
grep -q 'curl GET https://api.github.com/repos/NodeFlow-dev/nodeflow/releases/tags/v2.0.0' "$NF_STATE/calls" \
  || fail "install-node.sh pinned version did not read the tag release"
echo tampered >> "$rel/nodeflow-node-agent-2.0.0-linux-amd64"
if node_driver; then fail "install-node.sh accepted a tampered Agent"; fi
grep -q 'SHA-256 mismatch for nodeflow-node-agent-2.0.0-linux-amd64' "$work/out" || fail "install-node.sh mismatch message"
make_release 2.0.0
edit_release 'del(.assets[] | select(.name == "nodeflow-node-agent-2.0.0-linux-amd64"))'
if node_driver; then fail "install-node.sh accepted a missing Agent asset"; fi
grep -q 'release v2.0.0 has no asset nodeflow-node-agent-2.0.0-linux-amd64' "$work/out" || fail "install-node.sh missing asset message"
make_release 2.0.0
edit_release '(.assets[].digest) = null'
if node_driver; then fail "install-node.sh accepted an asset without digest"; fi
grep -q 'has no SHA-256 digest' "$work/out" || fail "install-node.sh missing digest message"
make_release 2.0.0
if node_driver NODEFLOW_VERSION=2.0.1; then fail "install-node.sh accepted a missing release"; fi
grep -q 'cannot read the GitHub release 2.0.1' "$work/out" || fail "install-node.sh missing release message"
pass "install-node.sh: embedded unit, Agent digest verification"

caddy_kind=stub; [ -z "$real_caddy" ] || caddy_kind=real
compose_kind=skipped; [ -z "$real_compose" ] || compose_kind=real
printf 'test_install=ok shell=%s caddy=%s compose=%s\n' "$test_shell" "$caddy_kind" "$compose_kind"
