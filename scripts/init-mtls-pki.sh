#!/bin/sh
set -eu

panel_host=${1:?usage: init-mtls-pki.sh panel-ip-or-dns [project-directory]}
project_dir=${2:-.}

case "$panel_host" in
  *[!A-Za-z0-9:.-]*|'')
    echo "panel host must be an IP address or DNS name" >&2
    exit 2
    ;;
esac

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root so private keys can be owned by the Panel container group" >&2
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

pki_dir=$project_dir/pki
tls_dir=$project_dir/tls
for path in "$pki_dir/ca.key" "$pki_dir/ca.crt" "$tls_dir/server.key" "$tls_dir/server.crt"; do
  if [ -e "$path" ]; then
    echo "refusing to overwrite existing PKI: $path" >&2
    exit 1
  fi
done

umask 077
install -d -m 0750 -o root -g "$runtime_group" "$pki_dir" "$tls_dir"
work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT HUP INT TERM

openssl genpkey -algorithm ED25519 -out "$pki_dir/ca.key"
openssl req -new -x509 -key "$pki_dir/ca.key" -days 3650 \
  -subj "/O=NodeFlow/CN=NodeFlow Agent CA" \
  -addext "basicConstraints=critical,CA:TRUE,pathlen:0" \
  -addext "keyUsage=critical,keyCertSign,cRLSign" \
  -out "$pki_dir/ca.crt"

openssl genpkey -algorithm ED25519 -out "$tls_dir/server.key"
openssl req -new -key "$tls_dir/server.key" \
  -subj "/O=NodeFlow/CN=$panel_host" \
  -out "$work_dir/server.csr"

case "$panel_host" in
  *:*) subject_alt_name="IP:$panel_host" ;;
  *[!0-9.]*) subject_alt_name="DNS:$panel_host" ;;
  *) subject_alt_name="IP:$panel_host" ;;
esac
cat > "$work_dir/server.ext" <<EOF
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=serverAuth
subjectAltName=$subject_alt_name
EOF

openssl x509 -req -in "$work_dir/server.csr" \
  -CA "$pki_dir/ca.crt" -CAkey "$pki_dir/ca.key" -CAcreateserial \
  -days 825 -extfile "$work_dir/server.ext" \
  -out "$tls_dir/server.crt"
rm -f "$pki_dir/ca.srl"

chown root:"$runtime_group" "$pki_dir/ca.key" "$pki_dir/ca.crt" "$tls_dir/server.key" "$tls_dir/server.crt"
chmod 0440 "$pki_dir/ca.key" "$tls_dir/server.key"
chmod 0444 "$pki_dir/ca.crt" "$tls_dir/server.crt"

openssl verify -CAfile "$pki_dir/ca.crt" "$tls_dir/server.crt"
openssl x509 -in "$pki_dir/ca.crt" -noout -fingerprint -sha256 -subject -enddate
openssl x509 -in "$tls_dir/server.crt" -noout -fingerprint -sha256 -subject -enddate
"$project_dir/scripts/init-update-signing-key.sh" "$project_dir"
