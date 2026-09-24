#!/bin/sh
set -eu

target=${1:?usage: deploy-panel.sh user@host:/absolute/path}
root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
identity=${NODEFLOW_SSH_IDENTITY:-$HOME/.ssh/id_ed25519}
host=${target%%:*}
path=${target#*:}
min_free_mb=${NODEFLOW_DEPLOY_MIN_FREE_MB:-3072}
staging=""

case "$path" in
  /) echo "deploy path must not be /" >&2; exit 2 ;;
  /*) ;;
  *) echo "deploy path must be absolute" >&2; exit 2 ;;
esac
path=${path%/}
case "$path" in
  *[!A-Za-z0-9._/-]*|*'/../'*|*'/./'*|*/..|*/.)
    echo "deploy path contains unsupported characters or traversal" >&2
    exit 2
    ;;
esac

case "$min_free_mb" in
  ''|*[!0-9]*) echo "NODEFLOW_DEPLOY_MIN_FREE_MB must be an integer" >&2; exit 2 ;;
esac

free_mb=$(ssh -F /dev/null -i "$identity" -o IdentitiesOnly=yes "$host" sh -s -- "$path" <<'PREFLIGHT'
set -eu
path=$1
test -d "$path" || { echo "missing deploy path: $path" >&2; exit 1; }
df -Pk "$path" | awk 'NR == 2 { print int($4 / 1024) }'
PREFLIGHT
)
case "$free_mb" in
  ''|*[!0-9]*) echo "could not determine free space for $target" >&2; exit 1 ;;
esac
if [ "$free_mb" -lt "$min_free_mb" ]; then
  echo "refusing deploy: ${free_mb} MiB free on $host, ${min_free_mb} MiB required" >&2
  exit 1
fi

staging=$(ssh -F /dev/null -i "$identity" -o IdentitiesOnly=yes "$host" sh -s -- "$path" <<'STAGING'
set -eu
path=$1
parent=$(dirname -- "$path")
base=$(basename -- "$path")
umask 077
staging=$(mktemp -d "$parent/.${base}.deploy.XXXXXX")
chmod 0700 "$staging"
printf '%s\n' "$staging"
STAGING
)

cleanup_staging() {
  [ -n "$staging" ] || return 0
  ssh -F /dev/null -i "$identity" -o IdentitiesOnly=yes "$host" sh -s -- "$path" "$staging" <<'CLEANUP' >/dev/null 2>&1 || true
set -eu
path=$1
staging=$2
parent=$(dirname -- "$path")
base=$(basename -- "$path")
case "$staging" in
  "$parent"/."$base".deploy.*) ;;
  *) exit 1 ;;
esac
rm -rf -- "$staging"
CLEANUP
}
trap cleanup_staging EXIT
trap 'exit 1' HUP INT TERM

rsync -az --delete \
  --exclude=.env \
  --exclude=tls/ \
  --exclude=pki/ \
  --exclude='*.dump' \
  --exclude='*.backup' \
  --exclude=/.impeccable/ \
  --exclude=/frontend/node_modules/ \
  --exclude=/frontend/dist/ \
  --exclude=/internal/panel/web_dist/ \
  --exclude=/panel-api \
  --exclude=/node-agent \
  --exclude=/node-updater \
  -e "ssh -F /dev/null -i $identity -o IdentitiesOnly=yes" \
  "$root/" "$host:$staging/"
ssh -F /dev/null -i "$identity" -o IdentitiesOnly=yes "$host" sh -s -- "$path" "$staging" <<'REMOTE'
set -eu
path=$1
staging=$2

parent=$(dirname -- "$path")
base=$(basename -- "$path")
case "$staging" in
  "$parent"/."$base".deploy.*) ;;
  *) echo "invalid deploy staging path" >&2; exit 1 ;;
esac
test -d "$staging" || { echo "missing deploy staging directory" >&2; exit 1; }

command -v flock >/dev/null 2>&1 || { echo "flock is required for serialized deploys" >&2; exit 1; }
command -v rsync >/dev/null 2>&1 || { echo "rsync is required for staged deploys" >&2; exit 1; }
exec 9>/run/nodeflow-panel-deploy.lock
if ! flock -n 9; then
  echo "another NodeFlow Panel deploy is already running" >&2
  exit 75
fi

partial=""
cleanup() {
  if [ -n "$partial" ] && [ -f "$partial" ]; then rm -f -- "$partial"; fi
  if [ -n "$staging" ] && [ -d "$staging" ]; then rm -rf -- "$staging"; fi
}
trap cleanup EXIT
trap 'cleanup; exit 1' HUP INT TERM

test -f "$staging/compose.yaml" || { echo "staged compose.yaml is missing" >&2; exit 1; }
test -f "$staging/Dockerfile.panel" || { echo "staged Dockerfile.panel is missing" >&2; exit 1; }
test -f "$staging/scripts/migrate.sh" || { echo "staged migration runner is missing" >&2; exit 1; }
test -f "$path/.env" || { echo "missing $path/.env" >&2; exit 1; }

# Validate the staged exposure policy against the live environment before any
# source file is replaced. A rejected legacy 0.0.0.0 bind must leave the
# current checkout and running Panel untouched.
docker compose \
  --project-name nodeflow-deploy-preflight \
  --project-directory "$staging" \
  --env-file "$path/.env" \
  -f "$staging/compose.yaml" config -q
docker compose \
  --project-name nodeflow-deploy-preflight \
  --project-directory "$staging" \
  --env-file "$path/.env" \
  -f "$staging/compose.yaml" run --rm --no-deps panel-exposure-guard

# Source updates become visible only while the deploy lock is held. Delayed
# updates/deletes avoid exposing a mixture of old and new migration/source files.
rsync -a --delete-delay --delay-updates \
  --exclude=/.env \
  --exclude=/tls/ \
  --exclude=/pki/ \
  --exclude='*.dump' \
  --exclude='*.backup' \
  --exclude=/.impeccable/ \
  --exclude=/frontend/node_modules/ \
  --exclude=/frontend/dist/ \
  --exclude=/internal/panel/web_dist/ \
  --exclude=/panel-api \
  --exclude=/node-agent \
  --exclude=/node-updater \
  "$staging/" "$path/"
rm -rf -- "$staging"
staging=""

cd "$path"
umask 077

test -f .env || { echo "missing $path/.env" >&2; exit 1; }
if grep -Eq '^(POSTGRES_PASSWORD=replace-with-a-long-random-password|PANEL_ADMIN_TOKEN=replace-with-at-least-32-random-characters)$' .env; then
  echo "refusing deploy with .env.example placeholder secrets" >&2
  exit 1
fi
docker compose config -q
docker compose run --rm --no-deps panel-exposure-guard

old_container=$(docker compose ps -q panel-api 2>/dev/null || true)
old_image_id=""
old_image_name=""
if [ -n "$old_container" ]; then
  old_image_id=$(docker inspect -f '{{.Image}}' "$old_container")
  old_image_name=$(docker inspect -f '{{.Config.Image}}' "$old_container")
fi

# Build failure cannot leave the database half-migrated.
docker compose build panel-api
docker compose up -d postgres release-init

backup_dir=/var/backups/nodeflow
install -d -m 0700 "$backup_dir"
stamp=$(date -u +%Y%m%dT%H%M%SZ)
partial=$(mktemp "$backup_dir/pre-deploy-$stamp.XXXXXX.partial")
backup=${partial%.partial}.dump
docker compose exec -T postgres sh -c \
  'exec pg_dump -Fc -U "$POSTGRES_USER" "$POSTGRES_DB"' > "$partial"
chmod 0600 "$partial"
docker compose exec -T postgres pg_restore --list < "$partial" >/dev/null
mv -f "$partial" "$backup"
partial=""
backup_sha=$(sha256sum "$backup" | cut -d' ' -f1)
printf 'database_backup=%s sha256=%s\n' "$backup" "$backup_sha"

docker compose run --rm migrate
docker compose up -d --no-deps panel-api

health_ok() {
  docker compose exec -T postgres sh -c \
    "wget -qO- http://panel-api:8080/healthz | grep -q '\"status\":\"ok\"'" >/dev/null 2>&1
}

react_ok() {
  index=$(docker compose exec -T postgres wget -qO- http://panel-api:8080/) || return 1
  printf '%s\n' "$index" | grep -q 'id="root"' || return 1
  asset=$(printf '%s\n' "$index" | sed -n 's|.*src="/\([^"?]*\.js\)[^"]*".*|\1|p' | head -n 1)
  case "$asset" in
    assets/*.js) ;;
    *) return 1 ;;
  esac
  docker compose exec -T postgres wget -q --spider "http://panel-api:8080/$asset"
}

new_container=$(docker compose ps -q panel-api 2>/dev/null || true)

container_stable() {
  current_container=$(docker compose ps -q panel-api 2>/dev/null || true)
  [ "$current_container" = "$new_container" ] || return 1
  [ "$(docker inspect -f '{{.State.Status}}' "$new_container")" = "running" ] || return 1
  [ "$(docker inspect -f '{{.RestartCount}}' "$new_container")" = "0" ]
}

restore_previous_image() {
  echo "new Panel failed stability gate; restoring previous application image" >&2
  if [ -n "$old_image_id" ] && [ -n "$old_image_name" ]; then
    docker tag "$old_image_id" "$old_image_name"
    docker compose up -d --no-deps --force-recreate panel-api
    sleep 2
    if health_ok; then echo "previous Panel image restored" >&2; else echo "previous Panel image restore is unhealthy" >&2; fi
  fi
  echo "database dump retained at $backup" >&2
  exit 1
}

if [ -z "$new_container" ]; then restore_previous_image; fi

attempt=0
stable_checks=0
while [ "$stable_checks" -lt 6 ]; do
  attempt=$((attempt + 1))
  if health_ok && react_ok && container_stable; then
    stable_checks=$((stable_checks + 1))
  else
    stable_checks=0
  fi
  if [ "$attempt" -ge 40 ]; then restore_previous_image; fi
  sleep 1
done

# Keep the newest ten validated pre-deploy dumps. Retention runs only after the
# new application passed its health gate, so a failed deploy never deletes its
# recovery point.
find "$backup_dir" -maxdepth 1 -type f -name 'pre-deploy-*.dump' -printf '%T@ %p\n' \
  | sort -nr \
  | awk 'NR > 10 { sub(/^[^ ]+ /, ""); print }' \
  | while IFS= read -r old_backup; do rm -f -- "$old_backup"; done

printf 'panel_health=ok migration=ok stable_checks=%s react_asset=ok restart_count=0\n' "$stable_checks"
REMOTE
staging=""
trap - EXIT HUP INT TERM
