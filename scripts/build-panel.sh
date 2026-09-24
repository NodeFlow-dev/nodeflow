#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
output=${1:-"$root/panel-api"}
staging=""
panel_version=$(sed -n 's/^var panelVersion = "\([^"]*\)"$/\1/p' "$root/cmd/panel-api/main.go")
[ -n "$panel_version" ] || { echo "Panel version is not declared" >&2; exit 1; }

cleanup() {
  if [ -n "$staging" ] && [ -d "$staging" ]; then
    rm -rf -- "$staging"
  fi
}
trap cleanup EXIT HUP INT TERM

cd "$root/frontend"
if [ "${NODEFLOW_SKIP_NPM_CI:-0}" != "1" ]; then
  npm ci --no-audit --no-fund
fi
npm run build

test -s dist/index.html
grep -q 'id="root"' dist/index.html

cd "$root"
staging=$(mktemp -d internal/panel/.web_dist.XXXXXX)
cp -a frontend/dist/. "$staging/"
test -s "$staging/index.html"
test ! -e "$staging/app.js"

rm -rf internal/panel/web_dist
mv "$staging" internal/panel/web_dist
staging=""

CGO_ENABLED=${CGO_ENABLED:-0} go build -buildvcs=false -trimpath \
  -ldflags="-s -w -X main.panelVersion=$panel_version" \
  -o "$output" ./cmd/panel-api
printf 'panel=%s frontend=React\n' "$output"
