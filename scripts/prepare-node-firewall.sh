#!/bin/sh
set -eu

apply=false
ssh_port=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --apply)
      apply=true
      ;;
    --dry-run)
      apply=false
      ;;
    --ssh-port)
      shift
      ssh_port=${1:-}
      ;;
    -h|--help)
      echo "usage: prepare-node-firewall.sh [--dry-run|--apply] [--ssh-port PORT]"
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      exit 2
      ;;
  esac
  shift
done

if [ -z "$ssh_port" ]; then
  ssh_port=${SSH_CONNECTION##* }
fi
case "$ssh_port" in
  ''|*[!0-9]*)
    echo "cannot safely determine sshd port; pass --ssh-port PORT" >&2
    exit 2
    ;;
esac
if [ "$ssh_port" -lt 1 ] || [ "$ssh_port" -gt 65535 ]; then
  echo "ssh port must be between 1 and 65535" >&2
  exit 2
fi

if [ "$apply" != true ]; then
  echo "DRY-RUN: install ufw when missing"
  echo "DRY-RUN: ufw allow $ssh_port/tcp comment nodeflow-ssh"
  echo "DRY-RUN: enable ufw only after the SSH safeguard exists"
  echo "DRY-RUN: listener rules remain owned by Node Agent with exact comment nodeflow"
  exit 0
fi

if [ "$(id -u)" -ne 0 ]; then
  echo "--apply must run as root" >&2
  exit 1
fi
if ! command -v ufw >/dev/null 2>&1; then
  if ! command -v apt-get >/dev/null 2>&1; then
    echo "ufw is missing and automatic installation is supported only with apt-get" >&2
    exit 1
  fi
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq ufw
fi

# This rule is intentionally not tagged `nodeflow`: the Agent must never
# adopt or remove the SSH safeguard while listener routes are reconciled.
ufw allow "$ssh_port/tcp" comment nodeflow-ssh
if ! ufw status | grep -qx 'Status: active'; then
  ufw --force enable
fi
ufw status | grep -qx 'Status: active'
echo "UFW active; SSH $ssh_port/tcp safeguarded. Listener ports await Agent assignment."
