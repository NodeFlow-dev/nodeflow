#!/bin/sh
set -eu

# Builds release/NodeFlow-Panel-<V>-Agent-<V>-install-kit.tar.gz: the
# installers, the release compose file, helper scripts, the Node Agent systemd
# unit and the installation docs. No binaries and no source tree: the Panel is
# the image ghcr.io/nodeflow-dev/nodeflow-panel:<V>, the Node Agent binaries
# are separate release assets.
#
# Usage: scripts/build-install-kit.sh [VERSION] [--replace]

root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
agent_version=${1:-$(sed -n 's/^var version = "\([^"]*\)"$/\1/p' "$root/cmd/node-agent/main.go")}
replace=${2:-}
panel_version=$(sed -n 's/^var panelVersion = "\([^"]*\)"$/\1/p' "$root/cmd/panel-api/main.go")

for version in "$panel_version" "$agent_version"; do
  case "$version" in
    ''|*[!A-Za-z0-9._-]*) echo "invalid version: $version" >&2; exit 2 ;;
  esac
done

grep -Fxq "var version = \"$agent_version\"" "$root/cmd/node-agent/main.go" || {
  echo "cmd/node-agent/main.go does not declare $agent_version" >&2
  exit 1
}
# install.sh started from the kit takes the Panel version from this default.
grep -Fq "ghcr.io/nodeflow-dev/nodeflow-panel:\${NODEFLOW_VERSION:-$panel_version}" "$root/compose.release.yaml" || {
  echo "compose.release.yaml does not default to Panel $panel_version" >&2
  exit 1
}
grep -Fq "$agent_version" "$root/docs/install/index.html" || {
  echo "documentation does not reference Agent $agent_version: docs/install/index.html" >&2
  exit 1
}

release_root="$root/release"
kit_name="NodeFlow-Panel-$panel_version-Agent-$agent_version-install-kit"
kit="$release_root/$kit_name"
outer="$release_root/$kit_name.tar.gz"
# release/ is git-ignored, so it does not exist in a fresh (CI) checkout.
install -d "$release_root"
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

stage="$tmp/$kit_name"
install -d "$stage/scripts" "$stage/configs/systemd" "$stage/docs"
install -m 0755 "$root/install.sh" "$stage/install.sh"
for script in install-node.sh prepare-node-firewall.sh init-mtls-pki.sh init-update-signing-key.sh; do
  install -m 0755 "$root/scripts/$script" "$stage/scripts/$script"
done
for file in compose.release.yaml nodeflow.env.example README.md CHANGELOG.md LICENSE; do
  install -m 0644 "$root/$file" "$stage/$file"
done
install -m 0644 "$root/configs/systemd/nodeflow-node-agent.service" "$stage/configs/systemd/"
cp -R "$root/docs/install" "$stage/docs/install"
find "$stage/docs" -type d -exec chmod 0755 {} +
find "$stage/docs" -type f -exec chmod 0644 {} +

(cd "$stage" && find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS)
(cd "$tmp" && tar --sort=name --mtime='@0' --owner=0 --group=0 --numeric-owner -cf - "$kit_name" | gzip -n > "$outer")
mv "$stage" "$kit"
(cd "$release_root" && sha256sum "$kit_name.tar.gz" > "$kit_name.tar.gz.sha256")

(cd "$kit" && sha256sum -c --quiet SHA256SUMS)
(cd "$release_root" && sha256sum -c --quiet "$kit_name.tar.gz.sha256")
printf 'kit=%s\narchive=%s\n' "$kit" "$outer"
