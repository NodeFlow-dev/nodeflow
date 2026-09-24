#!/bin/sh
set -eu

node_id=${1:?usage: issue-node-certificate.sh node-uuid output-directory [project-directory]}
output_dir=${2:?usage: issue-node-certificate.sh node-uuid output-directory [project-directory]}
project_dir=${3:-.}

if ! printf '%s\n' "$node_id" | grep -Eq '^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$'; then
  echo "node identity must be a UUID" >&2
  exit 2
fi

ca_certificate=$project_dir/pki/ca.crt
ca_key=$project_dir/pki/ca.key
if [ ! -r "$ca_certificate" ] || [ ! -r "$ca_key" ]; then
  echo "Agent CA is unavailable; run init-mtls-pki.sh first" >&2
  exit 1
fi
if [ -e "$output_dir/node.key" ] || [ -e "$output_dir/node.crt" ] || [ -e "$output_dir/ca.crt" ]; then
  echo "refusing to overwrite an existing node identity" >&2
  exit 1
fi

umask 077
install -d -m 0700 "$output_dir"
work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT HUP INT TERM

openssl genpkey -algorithm ED25519 -out "$output_dir/node.key"
openssl req -new -key "$output_dir/node.key" \
  -subj "/O=NodeFlow Node/CN=$node_id" \
  -out "$work_dir/node.csr"
cat > "$work_dir/node.ext" <<EOF
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=clientAuth
EOF
openssl x509 -req -in "$work_dir/node.csr" \
  -CA "$ca_certificate" -CAkey "$ca_key" -CAcreateserial \
  -days 825 -extfile "$work_dir/node.ext" \
  -out "$output_dir/node.crt"
rm -f "$project_dir/pki/ca.srl"
cp "$ca_certificate" "$output_dir/ca.crt"
chmod 0600 "$output_dir/node.key"
chmod 0644 "$output_dir/node.crt" "$output_dir/ca.crt"

openssl verify -CAfile "$output_dir/ca.crt" -purpose sslclient "$output_dir/node.crt"
openssl x509 -in "$output_dir/node.crt" -noout -fingerprint -sha256 -subject -enddate
