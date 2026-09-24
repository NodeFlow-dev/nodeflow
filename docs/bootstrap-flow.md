# Node bootstrap flow

Status: implemented as a bounded asynchronous job with pinned SSH host keys,
per-node mTLS identity, Agent/updater installation and immediate outbound
heartbeat.

## Operator flow

1. In **Ноды → Добавить ноду**, enter name, IP, SSH port and user. Choose SSH
   password or a private key (with an optional passphrase), then choose root,
   passwordless sudo, sudo password or automatic privilege detection.
2. Panel performs a host-key scan without authenticating and returns the public
   algorithm/fingerprint.
3. Operator verifies/pins that `SHA256:` fingerprint, checks the explicit
   confirmation and submits installation.
4. Panel returns a polling-safe job id. Polling exposes only allow-listed live
   stages (`create_node`, credentials, SSH connect/upload/privilege/install`) and
   a recoverable terminal status. The bounded worker creates the node row plus
   an 825-day bearer credential. Only its hash is persisted; the first verified
   Agent request binds that row to the exact certificate leaf fingerprint.
5. Panel issues an Ed25519 client key/certificate whose CN is the new node UUID.
6. Panel connects using the selected SSH authentication and the exact pinned
   host key. A non-root account is elevated only through the selected sudo mode.
7. After OS/architecture preflight it verifies and uploads the explicitly
   selected signed Agent release, or the newest compatible release when
   `release_id` is omitted. Bootstrap is rejected until a release is uploaded.
   The updater helper may still come from the Panel image.
8. On Ubuntu 24.04 (noble) and 26.04 (resolute) it converges HAProxy to the
   newest 3.4 build from the official HAProxy Performance repository
   (`haproxy-awslc`), validating the existing config with the candidate binary
   first and never downgrading a newer HAProxy; on Debian it installs the
   distribution `haproxy` package only when HAProxy is missing. Other Ubuntu
   releases are rejected. It then writes root-only Agent env/TLS
   files and hardened systemd units.
9. Agent starts on `127.0.0.1:4200` and immediately initiates an mTLS heartbeat
   to `PANEL_AGENT_PUBLIC_URL` (normally `https://panel:4200`).
10. Normal operation is pull-based and no longer uses SSH.

## Credential rules

- Bootstrap SSH/sudo passwords, SSH private keys and passphrases are decoded
  only into the bounded worker request and SSH client memory. Job records
  contain only id, state, safe stage, timestamps and the installed node id.
- That SSH authentication material is never written to PostgreSQL, filesystem,
  traces, audit data or API responses. Remote stdout/stderr and
  server-controlled SSH error text are discarded because either may echo
  sensitive environment data.
- Host-key mismatch aborts before authentication.
- Certificate private key travels only through the pinned SSH session and is
  installed as root mode `0600`.
- **Обновить доступ** repeats the pinned-SSH install with a new certificate and
  bearer, verifies Agent startup, then revokes every superseded bearer. A failed
  install revokes the unused new bearer and leaves the current one operational.
- Normal Agent renewal does not use SSH. It stages an Agent-generated Ed25519
  key/CSR and next bearer hash, verifies the returned node-owned certificate,
  then confirms with the candidate pair. Panel activates it and revokes all
  predecessors in one transaction; the old pair remains valid until commit.
- On bootstrap failure Panel removes the new database node. A host may still
  contain partial package/filesystem changes; inspect it before retrying.

## Installed layout

```text
/usr/local/bin/nodeflow-node-agent
/usr/local/libexec/nodeflow-node-updater
/etc/nodeflow/node-agent.env
/etc/nodeflow/tls/{ca.crt,node.crt,node.key}
/etc/systemd/system/nodeflow-node-agent.service
/etc/systemd/system/nodeflow-node-updater.service
/var/lib/nodeflow/credentials/state.json
/var/lib/nodeflow/updates/
```

Agent's unit permits writes only to HAProxy configuration, its atomic credential
state and its update state. New Panel bootstraps enable renewal in `apply` mode;
manual standalone installs default to `observe` until explicitly enabled.
Updater's separate unit permits the atomic Agent-binary swap and rollback.

## Firewall

No inbound firewall rule is required for normal control traffic: Agent connects
outbound to Panel TCP 4200. HAProxy listener ports are managed separately by the
revision-derived UFW policy. The Agent-local API must remain loopback-only.

On Debian/Ubuntu, a new-node bootstrap installs the UFW package without changing
firewall state. If the operator explicitly allows firewall apply, bootstrap:

1. derives the actual server-side sshd port from `SSH_CONNECTION` (the form's
   SSH port is only a validated fallback);
2. adds `allow <sshd-port>/tcp` with comment `nodeflow-ssh`;
3. enables UFW only after that safeguard succeeds;
4. starts Agent with local firewall ceiling `apply` and the minimal systemd
   UFW write/capability allowance.

The SSH tag differs deliberately from the exact `nodeflow` listener tag,
so route reconciliation never adopts or removes it. Port 4200 is never opened.
Without explicit opt-in the node remains `observe` and bootstrap does not enable
UFW.

For an existing node, inspect the plan without mutation first:

```bash
sudo ./scripts/prepare-node-firewall.sh --dry-run --ssh-port 22
```

`--apply` installs/enables UFW and can block inbound services which lack an
allow rule. Review the node's real sshd port and every required inbound service
before using it; preparing UFW alone does not raise Agent or Panel policy from
`observe` to `apply`.

## Recovery

- Wrong password/host key: correct the form and retry after inspecting partial
  changes.
- Panel unreachable from node: verify `PANEL_AGENT_PUBLIC_URL`, TCP 4200 and
  server SAN/CA; do not expose node-local 4200.
- Agent fails after install: restore the newest directory in
  `/var/backups/nodeflow-node/` or rerun the controlled installer.
- Lost CA/signing key: restore `pki/` from offline backup before enrolling or
  publishing releases.
