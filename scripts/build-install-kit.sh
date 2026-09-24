#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
agent_version=${1:-$(sed -n 's/^var version = "\([^"]*\)"$/\1/p' "$root/cmd/node-agent/main.go")}
replace=${2:-}
panel_version=$(sed -n 's/^var panelVersion = "\([^"]*\)"$/\1/p' "$root/cmd/panel-api/main.go")

for version in "$panel_version" "$agent_version"; do
  case "$version" in
    ''|*[!A-Za-z0-9._-]*) echo "invalid version: $version" >&2; exit 2 ;;
  esac
done

expected="var version = \"$agent_version\""
grep -Fxq "$expected" "$root/cmd/node-agent/main.go" || {
  echo "cmd/node-agent/main.go does not declare $agent_version" >&2
  exit 1
}

for doc in "$root/docs/install/index.html" "$root/docs/install/README-NODE-AGENT.txt"; do
  grep -Fq "$agent_version" "$doc" || {
    echo "documentation does not reference Agent $agent_version: $doc" >&2
    exit 1
  }
done

release_root="$root/release"
kit_name="NodeFlow-Panel-$panel_version-Agent-$agent_version-install-kit"
kit="$release_root/$kit_name"
outer="$release_root/$kit_name.tar.gz"
tmp=$(mktemp -d "$release_root/.${kit_name}.XXXXXX")
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

if [ -e "$kit" ] || [ -e "$outer" ] || [ -e "$outer.sha256" ]; then
  if [ "$replace" != "--replace" ]; then
    echo "release already exists: $kit_name (pass --replace to rebuild this generated version)" >&2
    exit 1
  fi
  case "$kit" in "$release_root"/NodeFlow-*-install-kit) ;; *) echo "unsafe release path" >&2; exit 1 ;; esac
  rm -rf -- "$kit"
  rm -f -- "$outer" "$outer.sha256"
fi

install -d "$tmp/$kit_name/01-PANEL/reverse-proxy" "$tmp/$kit_name/02-NODE-AGENT-UPLOAD"
cp "$root/install.sh" "$tmp/$kit_name/INSTALL-NODEFLOW.sh"
chmod 0755 "$tmp/$kit_name/INSTALL-NODEFLOW.sh"
cp "$root/docs/install/index.html" "$tmp/$kit_name/00-START-HERE.html"
cp "$root/docs/install/README-FIRST.txt" "$tmp/$kit_name/README-FIRST.txt"
cp "$root/docs/install/README-PANEL.txt" "$tmp/$kit_name/01-PANEL/README-PANEL.txt"
cp "$root/docs/install/README-NODE-AGENT.txt" "$tmp/$kit_name/02-NODE-AGENT-UPLOAD/README-NODE-AGENT.txt"
cp "$root"/docs/install/reverse-proxy/* "$tmp/$kit_name/01-PANEL/reverse-proxy/"
cp "$root/compose.release.yaml" "$tmp/$kit_name/01-PANEL/compose.release.yaml"
cp "$root/nodeflow.env.example" "$tmp/$kit_name/01-PANEL/nodeflow.env.example"

agent_name="nodeflow-node-agent-$agent_version-linux-amd64"
agent_output="$tmp/$kit_name/02-NODE-AGENT-UPLOAD/$agent_name"
if [ -n "${NODEFLOW_AGENT_ARTIFACT:-}" ]; then
  [ -f "$NODEFLOW_AGENT_ARTIFACT" ] || {
    echo "Node Agent artifact not found: $NODEFLOW_AGENT_ARTIFACT" >&2
    exit 1
  }
  cp "$NODEFLOW_AGENT_ARTIFACT" "$agent_output"
  chmod 0755 "$agent_output"
else
  (cd "$root" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath -ldflags='-s -w' -o "$agent_output" ./cmd/node-agent)
fi
(cd "$tmp/$kit_name/02-NODE-AGENT-UPLOAD" && sha256sum "$agent_name" > "$agent_name.sha256")
"$agent_output" -version | grep -Fxq "$agent_version"

source_paths='.dockerignore .env.example CHANGELOG.md Dockerfile.panel install.sh cmd/panel-api cmd/node-updater compose.yaml docs/install frontend go.mod go.sum internal migrations scripts/check-panel-exposure.sh scripts/init-mtls-pki.sh scripts/init-update-signing-key.sh scripts/install-panel.sh scripts/migrate.sh scripts/update-panel.sh'
(cd "$root" && tar --sort=name --mtime='@0' --owner=0 --group=0 --numeric-owner \
  --exclude='frontend/node_modules' --exclude='frontend/dist' \
  --exclude='internal/panel/web_dist' --exclude='*/testdata' --exclude='*_test.go' \
  -cf - $source_paths | gzip -n > "$tmp/$kit_name/01-PANEL/nodeflow-panel-source.tar.gz")

(cd "$tmp/$kit_name" && find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS)
(cd "$tmp" && tar --sort=name --mtime='@0' --owner=0 --group=0 --numeric-owner -cf - "$kit_name" | gzip -n > "$outer")
mv "$tmp/$kit_name" "$kit"
(cd "$release_root" && sha256sum "$kit_name.tar.gz" > "$kit_name.tar.gz.sha256")

(cd "$kit" && sha256sum -c SHA256SUMS >/dev/null)
(cd "$release_root" && sha256sum -c "$kit_name.tar.gz.sha256" >/dev/null)
printf 'kit=%s\narchive=%s\n' "$kit" "$outer"
