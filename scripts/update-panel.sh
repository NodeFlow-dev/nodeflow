#!/bin/sh
set -eu

source_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
target=${1:-/opt/nodeflow}

[ "$(id -u)" -eq 0 ] || { echo "run as root: sudo ./scripts/update-panel.sh /opt/nodeflow" >&2; exit 1; }
case "$target" in /*) ;; *) echo "target must be an absolute path" >&2; exit 2 ;; esac
[ "$source_dir" != "$target" ] || { echo "extract the new archive outside $target" >&2; exit 2; }

for command in docker flock rsync tar sha256sum mktemp df awk; do
  command -v "$command" >/dev/null 2>&1 || { echo "$command is required" >&2; exit 1; }
done
for file in compose.yaml Dockerfile.panel scripts/migrate.sh; do
  [ -f "$source_dir/$file" ] || { echo "new Panel source is missing $file" >&2; exit 1; }
done
[ -f "$target/.env" ] || { echo "missing live configuration: $target/.env" >&2; exit 1; }

exec 9>/run/nodeflow-panel-update.lock
flock -n 9 || { echo "another NodeFlow Panel update is running" >&2; exit 75; }

available_kib=$(df -Pk "$target" | awk 'NR == 2 { print $4 }')
[ "${available_kib:-0}" -ge 1048576 ] || { echo "at least 1 GiB free space is required" >&2; exit 1; }

backup_dir=/var/backups/nodeflow
install -d -m 0700 "$backup_dir"
stamp=$(date -u +%Y%m%dT%H%M%SZ)
source_backup="$backup_dir/panel-source-$stamp.tar.gz"
db_partial=""
rollback_dir=""
old_image_id=""
old_image_name=""
update_started=0

cleanup() {
  [ -z "$db_partial" ] || rm -f -- "$db_partial"
  [ -z "$rollback_dir" ] || rm -rf -- "$rollback_dir"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

cd "$target"
if grep -Eq '^(POSTGRES_PASSWORD=replace-with-a-long-random-password|PANEL_ADMIN_TOKEN=replace-with-at-least-32-random-characters)$' .env; then
  echo "refusing update with example secrets in $target/.env" >&2
  exit 1
fi

old_container=$(docker compose ps -q panel-api 2>/dev/null || true)
if [ -n "$old_container" ]; then
  old_image_id=$(docker inspect -f '{{.Image}}' "$old_container")
  old_image_name=$(docker inspect -f '{{.Config.Image}}' "$old_container")
fi

# The source backup is root-only. Runtime secrets are deliberately excluded:
# the updater never replaces .env, tls/ or pki/ in the first place.
tar -czf "$source_backup" \
  --exclude='./.env' --exclude='./tls' --exclude='./pki' \
  --exclude='./frontend/node_modules' --exclude='./frontend/dist' \
  --exclude='./internal/panel/web_dist' --exclude='./release' \
  --exclude='./*.dump' --exclude='./*.backup' .
chmod 0600 "$source_backup"
source_sha=$(sha256sum "$source_backup" | awk '{print $1}')
printf 'source_backup=%s sha256=%s\n' "$source_backup" "$source_sha"

# Validate the incoming compose and exposure policy against the live env before
# touching the checkout.
docker compose --project-name nodeflow-update-preflight \
  --project-directory "$source_dir" --env-file "$target/.env" \
  -f "$source_dir/compose.yaml" config -q
docker compose --project-name nodeflow-update-preflight \
  --project-directory "$source_dir" --env-file "$target/.env" \
  -f "$source_dir/compose.yaml" run --rm --no-deps panel-exposure-guard

docker compose up -d postgres release-init
db_partial=$(mktemp "$backup_dir/pre-update-$stamp.XXXXXX.partial")
db_backup=${db_partial%.partial}.dump
docker compose exec -T postgres sh -c \
  'exec pg_dump -Fc -U "$POSTGRES_USER" "$POSTGRES_DB"' > "$db_partial"
chmod 0600 "$db_partial"
docker compose exec -T postgres pg_restore --list < "$db_partial" >/dev/null
mv -f "$db_partial" "$db_backup"
db_partial=""
db_sha=$(sha256sum "$db_backup" | awk '{print $1}')
printf 'database_backup=%s sha256=%s\n' "$db_backup" "$db_sha"

restore_previous() {
  reason=$1
  echo "Panel update failed: $reason" >&2
  rollback_dir=$(mktemp -d "$backup_dir/.source-rollback.XXXXXX")
  tar -xzf "$source_backup" -C "$rollback_dir"
  rsync -a --delete-delay --delay-updates \
    --exclude=/.env --exclude=/tls/ --exclude=/pki/ \
    --exclude=/frontend/node_modules/ --exclude=/frontend/dist/ \
    --exclude=/internal/panel/web_dist/ \
    "$rollback_dir/" "$target/"
  if [ -n "$old_image_id" ] && [ -n "$old_image_name" ]; then
    docker tag "$old_image_id" "$old_image_name" || true
    (cd "$target" && docker compose up -d --no-deps --force-recreate panel-api) || true
  fi
  echo "source restored; database dump retained at $db_backup" >&2
  echo "database migrations are not rolled back automatically" >&2
  exit 1
}

update_started=1
rsync -a --delete-delay --delay-updates \
  --exclude=/.env --exclude=/tls/ --exclude=/pki/ \
  --exclude=/frontend/node_modules/ --exclude=/frontend/dist/ \
  --exclude=/internal/panel/web_dist/ \
  "$source_dir/" "$target/"

cd "$target"
docker compose config -q || restore_previous "compose validation"
docker compose run --rm --no-deps panel-exposure-guard || restore_previous "exposure policy"
docker compose build panel-api || restore_previous "image build"
docker compose run --rm migrate || restore_previous "database migration"
docker compose up -d --no-deps panel-api || restore_previous "container start"

health_ok() {
  docker compose exec -T postgres sh -c \
    "wget -qO- http://panel-api:8080/healthz | grep -q '\"status\":\"ok\"'" >/dev/null 2>&1
}

react_ok() {
  index=$(docker compose exec -T postgres wget -qO- http://panel-api:8080/) || return 1
  printf '%s\n' "$index" | grep -q 'id="root"' || return 1
  asset=$(printf '%s\n' "$index" | sed -n 's|.*src="/\([^"?]*\.js\)[^"]*".*|\1|p' | head -n 1)
  case "$asset" in assets/*.js) ;; *) return 1 ;; esac
  docker compose exec -T postgres wget -q --spider "http://panel-api:8080/$asset"
}

new_container=$(docker compose ps -q panel-api 2>/dev/null || true)
[ -n "$new_container" ] || restore_previous "container is absent"
attempt=0
stable=0
while [ "$stable" -lt 4 ]; do
  attempt=$((attempt + 1))
  current=$(docker compose ps -q panel-api 2>/dev/null || true)
  if [ "$current" = "$new_container" ] && health_ok && react_ok && \
     [ "$(docker inspect -f '{{.State.Status}}' "$new_container")" = running ] && \
     [ "$(docker inspect -f '{{.RestartCount}}' "$new_container")" = 0 ]; then
    stable=$((stable + 1))
  else
    stable=0
  fi
  [ "$attempt" -lt 30 ] || restore_previous "health/stability gate"
  sleep 1
done

find "$backup_dir" -maxdepth 1 -type f -name 'pre-update-*.dump' -printf '%T@ %p\n' \
  | sort -nr | awk 'NR > 10 { sub(/^[^ ]+ /, ""); print }' \
  | while IFS= read -r old; do rm -f -- "$old"; done
find "$backup_dir" -maxdepth 1 -type f -name 'panel-source-*.tar.gz' -printf '%T@ %p\n' \
  | sort -nr | awk 'NR > 10 { sub(/^[^ ]+ /, ""); print }' \
  | while IFS= read -r old; do rm -f -- "$old"; done

printf 'panel_update=ok health=ok react_asset=ok restart_count=0\n'
