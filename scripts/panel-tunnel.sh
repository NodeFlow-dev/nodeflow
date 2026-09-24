#!/bin/sh
set -eu

panel_host=${PANEL_SSH_HOST:?set PANEL_SSH_HOST=user@panel-host}
identity=${NODEFLOW_SSH_IDENTITY:-$HOME/.ssh/id_ed25519}
local_port=${PANEL_LOCAL_PORT:-8765}
remote_port=${PANEL_REMOTE_PORT:-8080}

case "$local_port:$remote_port" in
  *[!0-9:]*|:*|*:) echo "invalid tunnel port" >&2; exit 2 ;;
esac

exec ssh -NT \
  -i "$identity" \
  -o IdentitiesOnly=yes \
  -o ExitOnForwardFailure=yes \
  -o ServerAliveInterval=30 \
  -o ServerAliveCountMax=3 \
  -L "127.0.0.1:$local_port:127.0.0.1:$remote_port" \
  "$panel_host"
