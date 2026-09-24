#!/bin/sh
set -eu

bind_addr=${PANEL_BIND_ADDR:-127.0.0.1}
allow_insecure=$(printf '%s' "${ALLOW_INSECURE_HTTP:-false}" | tr '[:upper:]' '[:lower:]')

case "$bind_addr" in
  127.0.0.1|'[::1]'|::1)
    exit 0
    ;;
esac

case "$allow_insecure" in
  true|1|yes)
    printf 'WARNING: NodeFlow browser HTTP is explicitly published on %s\n' "$bind_addr" >&2
    exit 0
    ;;
esac

cat >&2 <<EOF
Refusing to publish the NodeFlow browser endpoint as plain HTTP on $bind_addr.
Keep PANEL_BIND_ADDR=127.0.0.1 and use an SSH tunnel or HTTPS reverse proxy.
For a deliberate insecure lab-only publication set ALLOW_INSECURE_HTTP=true.
EOF
exit 1
