# Signed Node Agent updates

Status: download, verification, explicit assignment, atomic activation and
external rollback are implemented.

## Trust model

`pki/update-signing.key` is an Ed25519 private key available read-only to Panel.
Bootstrap pins its raw 32-byte public key in the node environment. The signed
canonical manifest contains:

```json
{
  "schema":"nodeflow-agent-update/v1",
  "version":"0.3.1",
  "os":"linux",
  "arch":"amd64",
  "sha256":"64-lowercase-hex",
  "size":7504034,
  "sequence":12,
  "artifact_path":"00000000000000000012-linux-amd64-deadbeefdeadbeef.bin"
}
```

Signature, SHA-256, exact size, platform, safe path and monotonic sequence all
must pass. Artifacts are limited to 64 MiB. Directory components and final
files are opened with no-symlink semantics; state/pending/result files use
same-directory fsync + atomic rename.

## Panel workflow

In **Настройки → Node Agent**, upload a binary with version/platform
(«Загрузить релиз» → «Загрузить и подписать»). The installer (`install.sh`)
does this automatically for the release's linux/amd64 and linux/arm64 Agents.
Panel streams it to the persistent release volume, computes SHA-256, reserves a
global sequence, signs the canonical manifest and creates an immutable DB row.

Then explicitly assign it to a node: select the node in **Настройки → Node
Agent** and press **«Обновить до <версия>»**, or use **«Обновить»** on the node
page. Panel never broadcasts a
new release automatically. A release is returned only to the assigned node and
its artifact endpoint requires both that node's mTLS identity and bearer token.

API equivalents:

```text
POST /api/v1/agent-releases?version=0.3.1&os=linux&arch=amd64
Content-Type: application/octet-stream

PUT /api/v1/nodes/{node_uuid}/agent-update
{"release_id":"release_uuid","expected_actual_sequence":11,"expected_desired_sequence":11}

POST /api/v1/nodes/{node_uuid}/agent-update/rollback
{"target_release_id":"older_release_uuid","expected_actual_sequence":11,"expected_desired_sequence":11}
```

Read `GET /api/v1/nodes/{node_uuid}/agent-update` immediately before either
mutation. Send its `actual_sequence` and the current `desired_release.sequence`
(`0` when absent). Panel compares both values atomically. A stale forward or
desired-state request returns `409 update_state_changed`; reload before choosing
a release again. Rollback still requires a positive installed sequence and
clones the verified older artifact into a new signed sequence.

## Node activation

1. Heartbeat receives the signed manifest.
2. Agent downloads from `/agent/v1/updates/{sequence}/artifact` over its shared
   mTLS client and writes a unique root-only staging file.
3. Agent verifies the manifest/artifact, writes `pending.json`, and starts
   `nodeflow-node-updater.service` asynchronously.
4. Updater backs up the running Agent, stops only Agent, independently verifies
   Ed25519, platform, sequence and artifact again, atomically replaces the
   binary, persists sequence state and starts it.
5. Local `/v1/health` must return the exact expected version.
6. Success writes `installed`; failure restores prior binary/state and writes
   `rolled_back`. The restored Agent reports that result to Panel immediately.
7. An enabled systemd path unit watches both `pending.json` and the trusted
   activation journal. After a crash or reboot, the updater restores the prior
   binary/state before clearing the interrupted transaction.

HAProxy is independent and keeps serving existing/new traffic throughout an
Agent update.

Self-update replaces only the Agent binary. The systemd unit
`nodeflow-node-agent.service` is written by bootstrap (or `install-node.sh`)
and is **not** changed by an update. Agent 2.0.0 additionally needs write
access to the kernel pipe limits; on nodes installed by 1.0.x add it once
(or reinstall the Agent from the node menu):

```bash
sudo install -d /etc/systemd/system/nodeflow-node-agent.service.d
printf '[Service]\nReadWritePaths=-/etc/sysctl.d -/proc/sys/fs/pipe-max-size -/proc/sys/fs/pipe-user-pages-soft\n' \
  | sudo tee /etc/systemd/system/nodeflow-node-agent.service.d/20-kernel-pipes.conf
sudo systemctl daemon-reload && sudo systemctl restart nodeflow-node-agent
```

Without it the Agent works normally but reports the limits as untuned, and
Panel keeps rendering 256 KiB splice pipes.

## States

`idle → pending → downloading → verified → activating → installed`

Failures end in `failed`/`rolled_back`. The same failed sequence is terminal and
cannot be assigned again. Upload the artifact again to create a new signed
sequence; a manual safe rollback instead clones a verified older artifact into
a new sequence. Once sequence `N` is installed, API and Agent reject every
release with sequence `<= N`.

## Files and recovery

```text
/var/lib/nodeflow/updates/pending.json
/var/lib/nodeflow/updates/result.json
/var/lib/nodeflow-updater/state.json
/var/lib/nodeflow-updater/activation.json
/var/lib/nodeflow-updater/backups/
```

`state.json` is the anti-rollback anchor. The Agent sandbox can read but cannot
write `/var/lib/nodeflow-updater`; only the external updater owns it. Do
not delete it casually. If both
candidate and automatic rollback fail, restore the latest backup binary/state,
run `systemctl daemon-reload`, then restart only
`nodeflow-node-agent.service`.

## Key rotation boundary

The current manifest supports one pinned signing key. Rotation therefore needs
a controlled Agent rollout carrying the new public key (or future dual-key
support). Never replace the private key while old nodes still trust only its
public half. Keep offline backups and record its SHA-256 fingerprint.
