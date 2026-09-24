# Architecture

## Components

- **Panel API/UI**: one Go binary in a read-only container. It serves the
  browser on `:8080` and a separate TLS 1.3 client-certificate listener on
  `:4200`.
- **PostgreSQL**: nodes, routes, latest heartbeat, traffic counters, immutable
  HAProxy revisions, firewall policies, audit events and Agent release/update state.
- **Node Agent**: native root systemd service. It renders no UI and is not in
  the data path. Its local API listens on `127.0.0.1:4200`. Lower CPU/IO
  scheduling weights and `Nice=10` keep HAProxy preferred under contention.
- **Node updater**: a separate oneshot systemd service/binary. It can replace
  and restore Agent even when a candidate Agent cannot start.
- **HAProxy**: native node data plane. Client traffic never traverses Panel or
  Agent.

## Trust boundaries

Browser administration uses an HttpOnly, SameSite=Strict session derived from
the administrator token. Mutating cookie-authenticated calls require a
same-origin request.

Every Agent call to Panel uses:

1. TLS 1.3 with a client certificate issued by the private Agent CA;
2. a certificate CN equal to the node UUID;
3. an enrollment bearer token stored only as SHA-256 in PostgreSQL;
4. a database predicate binding that active token to the SHA-256 fingerprint of
   the exact verified leaf certificate before heartbeat/config/update state can
   change. Legacy NULL fingerprints are bound on first verified use.

The old plain HTTP Agent endpoints return `mtls_required` when
`PANEL_REQUIRE_AGENT_MTLS=true`. A client certificate alone cannot download an
artifact: the release must also be explicitly assigned to that node and the
bearer token must still be valid.

## Agent credential renewal

```text
current cert + current bearer
  -> Ed25519 CSR + next bearer hash
  -> Panel-issued pending certificate/token row
  -> candidate cert + raw next bearer confirms
  -> one DB commit activates candidate + revokes predecessors
```

Panel owns certificate identity: CSR subject/SAN/extensions are discarded and
CN is set from the currently authenticated node UUID. The pending pair is
usable only on its confirmation endpoint. Exact `renewal_id` replays return the
stored certificate; a different payload conflicts. Per-node row locking, one
pending candidate, a one-minute issuance interval and a 24-hour confirmation
deadline bound concurrency and abuse. The old pair remains active until the
confirmation transaction commits, so a failed write cannot strand the node.

## HAProxy configuration flow

```text
route CRUD
  -> deterministic preview
  -> immutable revision
  -> explicit desired assignment
  -> Agent heartbeat receives assignment
  -> checksum + haproxy -c
  -> atomic file replace + graceful reload
  -> bounded report + observed revision/SHA
```

Preview, revision creation and desired assignment remain distinct operator
actions. Panel treats the Agent's observed revision/SHA as authoritative: an
unknown or mismatched pair becomes `drifted` and the desired revision is sent
again. Ten newest known-good HAProxy backups are retained.

Generated TCP defaults use a 15-minute idle client/server timeout with TCP
keepalive for long-lived VPN tunnels. The global section deliberately sets no
`maxconn`: HAProxy 3.4 derives it from the file-descriptor limit (the Ubuntu
`haproxy.service` has `LimitNOFILE=524288`, giving `Maxconn: 262129` in
`show info`). A fixed value near that limit makes HAProxy refuse to start
("Cannot raise FD limit"), so capacity is raised via the unit's
`LimitNOFILE`, not the config. These values are part of an
immutable renderer revision, not silently patched into a running configuration.
Domain targets use a generated `resolvers` section that queries the node-local
AdGuard Home listener at `127.0.0.1:53`. The renderer does not read
`/etc/resolv.conf` or fall back to libc, so HAProxy can refresh target addresses
at runtime without depending on the host resolver file. IP and Unix-socket
targets do not emit an unused resolver section.

## Traffic, quotas and firewall

Agent reads HAProxy Runtime API counters every heartbeat and samples NIC deltas
for current byte rates. Unchanged quota state is verified from that same stats
snapshot, so the Agent does not issue one Runtime API mutation per route on
every heartbeat; missing or drifted state is repaired by the next assignment.
Panel stores only the latest heartbeat but maintains
reset-safe monthly deltas for nodes, backends and routes. A counter decrease
starts a new epoch instead of subtracting traffic.

`block_new` disables the exact immutable backend/server through Runtime API;
existing sessions remain open. The next successful config reload resets and
reconciles runtime policy.

Firewall plans contain only TCP listener ports from the actual immutable
revision. The effective mode is the lower of Panel policy and the node-local
ceiling. Apply diffs only rules with the exact `nodeflow` comment, adds
missing desired ports and deletes stale tagged rules by numbered rule. It never
touches untagged operator rules, the separately tagged bootstrap SSH safeguard,
deny/outbound rules, or port 4200. When the last applied route leaves a shared
listener port, the now-stale exact-tagged allow is removed; another route on the
same port keeps the single deduplicated allow.
An explicitly firewall-enabled bootstrap creates the node policy in `apply`,
so successful route activation automatically opens the new listener port.
Without that bootstrap opt-in, a new node remains in `observe`.
Agent itself never installs or enables UFW. Debian/Ubuntu new-node bootstrap
installs the package while inactive. Only explicit firewall opt-in prepares the
actual server-side SSH port with comment `nodeflow-ssh` and then enables
UFW. Existing nodes are never migrated to apply implicitly.
The Agent systemd sandbox grants optional write access only to `/etc/ufw` (when
that directory exists) plus `CAP_NET_ADMIN`; all other `/etc` paths remain
read-only.

## Agent release flow

```text
binary upload -> Panel SHA-256 + Ed25519 signature -> immutable release
  -> explicit node assignment -> heartbeat manifest
  -> mTLS artifact download -> Agent signature/hash/size/platform/sequence check
  -> pending.json -> systemd updater helper
  -> stop Agent only -> backup -> atomic replace -> start + version health check
  -> installed OR restore prior binary/state and report rolled_back
```

HAProxy is never restarted by Agent updates. Release sequence is monotonic and
persisted on the node; lower or equal sequences cannot be installed.

## Persistence

- PostgreSQL volume: control-plane state and traffic ledger.
- `agent_releases` volume: immutable signed artifacts.
- `pki/`: Agent CA plus Agent-release signing key; excluded from rsync deletion
  and Docker build context.
- Node `/var/lib/nodeflow/updates`: Agent-writable staged artifacts and
  pending/result JSON.
- Node `/var/lib/nodeflow-updater`: updater-only anti-rollback state and
  retained rollback binaries; read-only inside the Agent service sandbox.
- Node `/var/backups/nodeflow-node`: operator/bootstrap migration backups.

## Known boundaries

- Browser-facing `:8080` is bound to host loopback in the lab profile and is
  reached through an SSH tunnel or host HTTPS reverse proxy. Compose and the
  deploy script reject non-loopback plain-HTTP publication unless the operator
  explicitly sets the lab-only `ALLOW_INSECURE_HTTP=true` escape hatch.
- Bootstrap runs as a bounded in-memory asynchronous job. `POST /api/v1/bootstrap`
  returns `202` immediately; polling reports allow-listed live stages and keeps a
  terminal result for 15 minutes. Jobs are not durable across a Panel restart
  and cannot be cancelled through the API yet.
- Manual per-node certificate/token rotation over pinned SSH and the automatic
  candidate/confirm protocol are implemented. A CRL/OCSP layer, Agent-CA
  rollover and dual-key release-signing rotation are not implemented yet.
- Multi-Panel high availability and an external secret manager are outside the
  current single-Panel scope.
