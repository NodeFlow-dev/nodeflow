#!/bin/sh
set -eu

project_dir=${1:-.}
pki_dir=$project_dir/pki
private_key=$pki_dir/update-signing.key
public_key=$pki_dir/update-signing.pub

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root so the signing key can be owned by the Panel container group" >&2
  exit 2
fi

runtime_gid=65532
runtime_group=$(getent group "$runtime_gid" | awk -F: 'NR == 1 { print $1 }')
if [ -z "$runtime_group" ]; then
  command -v groupadd >/dev/null 2>&1 \
    || { echo "groupadd is required to create the Panel runtime group" >&2; exit 1; }
  if getent group nodeflow-runtime >/dev/null 2>&1; then
    echo "nodeflow-runtime exists with a different GID; expected $runtime_gid" >&2
    exit 1
  fi
  groupadd --gid "$runtime_gid" nodeflow-runtime
  runtime_group=nodeflow-runtime
fi
if [ -e "$private_key" ] || [ -e "$public_key" ]; then
  if [ ! -f "$private_key" ] || [ ! -f "$public_key" ]; then
    echo "incomplete update-signing keypair; refusing to overwrite it" >&2
    exit 1
  fi
  temporary_public=$(mktemp)
  trap 'rm -f "$temporary_public"' EXIT HUP INT TERM
  openssl pkey -in "$private_key" -pubout -out "$temporary_public"
  if ! cmp -s "$temporary_public" "$public_key"; then
    echo "update-signing public and private keys do not match" >&2
    exit 1
  fi
else
  umask 077
  install -d -m 0750 -o root -g "$runtime_group" "$pki_dir"
  openssl genpkey -algorithm ED25519 -out "$private_key"
  openssl pkey -in "$private_key" -pubout -out "$public_key"
fi

chown root:"$runtime_group" "$private_key" "$public_key"
chmod 0440 "$private_key"
chmod 0444 "$public_key"
printf 'update_public_key_sha256='
openssl pkey -pubin -in "$public_key" -outform DER | sha256sum | cut -d' ' -f1
