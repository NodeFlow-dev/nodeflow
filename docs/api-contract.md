# API contract

Status: endpoints below exist in the current code. Deferred hardening is listed
at the end and is not an implemented contract.

Current responses use `Content-Type: application/json`; persisted identifiers are UUIDs and timestamps are UTC RFC 3339. Panel errors use:

```json
{"error":{"code":"stable_code","message":"safe message"}}
```

Agent endpoints use the same stable error envelope. Correlation IDs remain target
hardening. Secrets, private keys, SSH credentials and raw command output must
never appear in responses or logs.

## Current MVP: Panel API

`GET /healthz` is unauthenticated. `/api/v1/*` accepts the administrator bearer
token or the browser's HttpOnly session. Mutating browser calls are
same-origin-only. `/agent/v1/*` requires TLS 1.3 client authentication plus an
active enrollment bearer token bound to the SHA-256 fingerprint of that exact
leaf certificate. A migrated legacy token with a NULL fingerprint is accepted
once and atomically bound to the verified leaf before subsequent use.

| Method | Endpoint | Purpose |
|---|---|---|
| `GET, POST` | `/api/v1/nodes` | List/create nodes. |
| `GET, PUT, DELETE` | `/api/v1/nodes/{node_id}` | Read/update/delete one node. |
| `POST` | `/api/v1/bootstrap/host-key` | Scan one public SSH host key before authentication. |
| `POST` | `/api/v1/bootstrap` | Start a bounded asynchronous pinned-key SSH install from an uploaded signed Agent release; optional `release_id`, otherwise newest compatible release; returns `202`. |
| `GET` | `/api/v1/bootstrap/{job_id}` | Read allow-listed live stage and terminal bootstrap status; never returns SSH credentials. |
| `POST` | `/api/v1/nodes/{node_id}/reinstall` | Full pinned-key SSH reinstall of an existing node without replacing its id or routes; returns `202` and the normal bootstrap job id. |
| `POST` | `/api/v1/nodes/{node_id}/rotate-credentials` | Pinned-key reinstall of certificate/token, then revoke superseded bearer credentials. |
| `GET` | `/api/v1/nodes/{node_id}/audit?limit=20` | Read recent sanitized privileged actions for one node. |
| `GET` | `/api/v1/nodes/{node_id}/traffic?month=YYYY-MM` | Read reset-safe monthly node/backend/route traffic accounting. |
| `GET, PUT` | `/api/v1/nodes/{node_id}/firewall` | Read/update `off`, `observe` or locally gated `apply` UFW policy. |
| `GET, POST` | `/api/v1/nodes/{node_id}/routes` | List routes or save a new disabled draft. |
| `GET, PUT, PATCH, DELETE` | `/api/v1/nodes/{node_id}/routes/{route_id}` | Read/update/delete one route; `PATCH` toggles only `enabled`; active-state changes auto-publish HAProxy config. |
| `POST` | `/api/v1/nodes/{node_id}/render-config` | Deterministically preview a complete HAProxy config from enabled routes. |
| `GET` | `/api/v1/dns/resolve?host={hostname}` | Resolve a DNS hostname from the Panel host so the route editor can offer a `preferred_ip`; see "DNS resolve helper". |
| `POST` | `/api/v1/nodes/{node_id}/enrollment-tokens` | Issue a bearer token; plaintext is returned once, hash is stored. |
| `GET, POST` | `/api/v1/nodes/{node_id}/config-revisions` | List/create immutable, monotonically numbered HAProxy configurations. |
| `POST` | `/api/v1/nodes/{node_id}/config-revisions/from-routes` | Render enabled routes and create an immutable revision without assigning it. |
| `GET` | `/api/v1/nodes/{node_id}/config-revisions/{revision}` | Read one immutable configuration revision. |
| `GET` | `/api/v1/nodes/{node_id}/config-state` | Read desired/actual revision and convergence state. |
| `PUT` | `/api/v1/nodes/{node_id}/desired-revision` | Assign any existing revision as desired state. |
| `GET, POST` | `/api/v1/agent-releases` | List or stream/upload and sign an immutable Agent release. |
| `DELETE` | `/api/v1/agent-releases/{release_id}` | Delete only an unused release; installed, assigned and rollback-required releases return `409 release_in_use`. |
| `GET` | `/api/v1/agent-releases/signing-key` | Read Ed25519 public key/fingerprint metadata. |
| `GET, PUT` | `/api/v1/nodes/{node_id}/agent-update` | Read update state or explicitly assign a newer release. |
| `POST` | `/api/v1/nodes/{node_id}/agent-update/rollback` | Clone a verified older artifact into a new signed sequence and assign that safe rollback. |
| `POST` | `/agent/v1/heartbeat` | mTLS+token heartbeat; returns explicitly assigned config/quota/firewall/update work. |
| `POST` | `/agent/v1/config-report` | Append an apply result and update actual/convergence state. |
| `GET` | `/agent/v1/updates/{sequence}/artifact` | Stream only the release assigned to this authenticated node. |
| `POST` | `/agent/v1/credential-renewals` | Create or replay one pending mTLS certificate/bearer candidate. |
| `POST` | `/agent/v1/credential-renewals/{renewal_id}/confirm` | Atomically activate the authenticated candidate and revoke every predecessor. |

| `GET` | `/metrics` | Prometheus text metrics (opt-in, bearer token required — see `PANEL_METRICS_ENABLED`). |

### DNS resolve helper

`GET /api/v1/dns/resolve?host=<hostname>` is an admin-authenticated read-only
helper for the route editor. It resolves the name with the Panel host's system
resolver (not the Node's `nf_dns` resolvers, so results may differ from what
HAProxy sees on the Node) and returns:

```json
{"host":"example.com","addresses":["192.0.2.1","2001:db8::1"],"resolved_at":"2026-09-23T12:00:00Z"}
```

`host` is normalised with the same rules as route target hostnames (lowercase,
trailing dot removed). `addresses` lists IPv4 first, then IPv6, each group
sorted and deduplicated (IPv4-mapped IPv6 collapses to IPv4), at most 64
entries. An IP literal, empty or otherwise invalid hostname returns
`400 invalid_host`. The lookup times out after 3 seconds; NXDOMAIN, timeout or
other resolver failures still return `200` with `"addresses":[]` and a short
`"error"` (`no such host`, `lookup timed out`, `lookup failed`). At most 10
lookups run concurrently per Panel process; excess requests get
`429 rate_limited`. Nothing is persisted and the endpoint is not audited.

### Existing-node reinstall

`POST /api/v1/nodes/{node_id}/reinstall` accepts the same write-only SSH fields
as bootstrap: `ssh_port`, `username`, `auth_mode`, password or private-key
credentials, `sudo_mode`, optional `sudo_password`, and the exact scanned
`host_key_sha256` plus `host_key_algorithm`. Node name/address, Agent port and
firewall ceiling are inherited from the existing node. The SSH port may be
overridden for this connection when the server-side SSH service moved.

The response is the normal polling-safe `202` job. Poll
`GET /api/v1/bootstrap/{job_id}` until `installed` or `failed`; only allow-listed
stages are exposed. The job never creates or replaces the node row and never
modifies routes. It installs a candidate bearer/mTLS identity into a unique
root-only rollback slot, waits until that exact bearer authenticates, then
revokes predecessors and removes the backup. Installer, heartbeat or database
failure restores the previous credential files before the candidate bearer is
revoked. If SSH rollback itself cannot be confirmed, the candidate is retained
instead of risking an Agent outage and the job terminates at stage `rollback`.
A successful completion writes the `node.reinstall` audit action.

Rollback is an identity/trust-state transaction, not an OS snapshot: it restores
the Agent and updater environment, signed-update trust root, mTLS files,
credential state and prior Agent active state. It does not downgrade installed
packages or binaries, restore systemd unit definitions, or undo HAProxy/UFW
side effects already completed by the installer.

### Agent release assignment CAS

Forward assignment is compare-and-set against the update state last read by the
operator. Both sequence fields are required nonnegative integers; use `0` when
there is no installed or desired release:

```json
{
  "release_id":"33333333-3333-4333-8333-333333333333",
  "expected_actual_sequence":11,
  "expected_desired_sequence":11
}
```

Panel compares both values atomically before the existing newer-sequence and
node-platform guards. A stale value returns `409 update_state_changed`; reload
`GET /api/v1/nodes/{node_id}/agent-update` before choosing another release.

Rollback also carries the state that authorized the confirmation:

```json
{
  "target_release_id":"22222222-2222-4222-8222-222222222222",
  "expected_actual_sequence":11,
  "expected_desired_sequence":11
}
```

`expected_actual_sequence` must be positive for rollback. Panel verifies that
the target is an older release for the installed platform, re-verifies its
artifact, clones it into a new signed sequence, then assigns that clone with the
same actual/desired CAS. An installed-sequence precheck failure remains
`409 actual_sequence_changed`; a desired-state or final assignment race returns
`409 update_state_changed`.

### Agent credential renewal

The Agent generates a new Ed25519 private key and bearer token locally. The
private key and raw next token never appear in the start request. Start auth is
the current verified certificate plus its active raw bearer:

```json
{
  "renewal_id":"33333333-3333-4333-8333-333333333333",
  "csr_pem":"-----BEGIN CERTIFICATE REQUEST-----\n...\n-----END CERTIFICATE REQUEST-----\n",
  "next_token_sha256":"64-lowercase-hex",
  "next_token_prefix":"nfe_12345678"
}
```

`renewal_id` is a canonical UUIDv4. The PEM field is limited to 8192 bytes,
must contain exactly one signed Ed25519 CSR and may not contain trailing data.
Panel ignores the CSR subject, SANs and extensions: the issued certificate CN
is always the authenticated node UUID and has only client-auth usage. The next
token hash is globally unique and cannot equal the current token hash.

A new request returns `201`; an exact replay returns `200` with the stored
certificate and identical metadata:

```json
{
  "renewal_id":"33333333-3333-4333-8333-333333333333",
  "certificate_pem":"-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----\n",
  "certificate_sha256":"64-lowercase-hex",
  "serial":"123456789",
  "not_before":"2026-07-13T12:00:00Z",
  "not_after":"2028-10-15T12:00:00Z",
  "confirm_by":"2026-07-14T12:00:00Z"
}
```

The candidate is stored with `activated_at=NULL` and cannot authenticate
heartbeat, config-report or artifact requests. One node may have only one
unconfirmed candidate. A bound credential is eligible when its certificate or
bearer expires within 45 days; a legacy unbound credential is eligible once.
Issuance is limited to one attempt per node per minute and confirmation is due
within 24 hours.

Confirmation has no JSON body. It authenticates with the candidate certificate
and the raw next token in `Authorization: Bearer ...`. One PostgreSQL
transaction activates the candidate, records the audit event and revokes all
other bearer rows for the node. A repeated confirmation with the active
candidate is idempotent. Until commit, the predecessor remains fully active;
any database failure rolls back activation and revocation together.

Stable renewal failures include `401 unauthorized`, `409 renewal_not_due`,
`409 renewal_in_progress`, `409 idempotency_conflict`, `409 renewal_expired`,
`422 invalid_csr`, `429 renewal_rate_limited`, `503 ca_expires_soon` and
`503 credential_issuer_unavailable`. `renewal_expired` is terminal for that
candidate: Agent atomically discards its local pending state and creates a new
candidate on a later renewal check.

### Route builder contract

Routes are scoped to one node and one listener. `listener_ip` is either `"*"`
or a canonical IPv4/IPv6 address; `listener_port` is `1..65535`. A non-fallback
route has one to 64 unique DNS SNI names. Only one fallback route may exist for
the same `(node_id, listener_ip, listener_port)`, and a fallback has no SNI.

TCP target example:

```json
{
  "listener_ip": "*",
  "listener_port": 443,
  "snis": ["vpn.example.com", "cdn.example.com"],
  "fallback": false,
  "target_type": "tcp",
  "target_host": "192.0.2.20",
  "target_port": 10443,
  "dns_pool": false,
  "proxy_protocol": "v2",
  "quota_bytes": 1099511627776,
  "quota_action": "block_new",
  "enabled": false,
  "custom_fragment": "  timeout connect 5s\n"
}
```

Unix target/fallback example:

```json
{
  "listener_ip": "127.0.0.1",
  "listener_port": 443,
  "snis": [],
  "fallback": true,
  "target_type": "unix",
  "unix_socket_path": "/dev/shm/xray.sock",
  "proxy_protocol": "v1",
  "quota_bytes": null,
  "quota_action": "observe",
  "enabled": false,
  "custom_fragment": ""
}
```

`proxy_protocol` accepts `none`, `v1` or `v2`. A TCP target requires a valid
DNS/IP `target_host` and port. A DNS name whose last label is all-numeric (or
`0x` hexadecimal) is rejected: strings such as `1.2.3`, `010.0.0.1`,
`300.1.1.1` or `0x7f.0.0.1` are malformed IPv4 literals, not host names (RFC
3696 §2). The same rule applies to `servers[].host`. A Unix target requires a canonical absolute
Linux path of at most 107 bytes and forbids TCP target fields. `quota_bytes` is
either `null` (unlimited) or a positive integer. `quota_action` is `observe`
or `block_new`; `block_new` requires a positive quota.

For a DNS target, `dns_pool:true` switches the backend from one resolved
address to a source-IP-sticky pool of all healthy A/AAAA answers. New
revisions use 32 internal `server-template` slots, `balance source` and
consistent hashing. Health checks are forced on; unavailable addresses are
removed from new connection selection. Distribution is approximate across
distinct source IPs, not strict round-robin. Existing TCP sessions are not
migrated after backend failure. Runtime `block_new` quota enforcement is not
supported for DNS pools; observe-only accounting remains available.

**Multi-server backends and balance mode** (`1.1.0+`): A route may carry an
explicit `servers` array (max 16 entries). Each entry has `position` (1..16,
defaults to array order), `name` (1–58 characters of `A-Z a-z 0-9 _ -`, not
ending in `_pref`), `target_type` (`tcp` or `unix`), `host`, `port`,
`unix_socket_path`, `backup`, `dns_pool` and `preferred_ip`.

`servers` is the source of truth whenever it is sent. The route target fields
are derived from the first primary server and route-level `dns_pool` is
ignored (it is legacy-only for routes without servers). A single server
without `preferred_ip` is stored as a plain single-target route (servers
cleared), so it renders the canonical `nf_srv_<id>` server byte-identically to
a route saved through `target_host`/`target_port`, and runtime `block_new`
keeps working.

`balance_mode` (`pool` or `failover`):
- absent: explicit `servers[].backup` flags are kept as sent (1.1.0-rc
  compatibility). Any backup flag makes the route `failover`; otherwise `pool`.
- `pool`: all servers are active; `backup` flags are cleared. Client
  distribution is selected by `balance_algorithm` and `sticky_mode` (below).
- `failover`: position order is priority. The first server is active, every
  later server gets `backup`. `balance_algorithm`, `sticky_mode` and their
  settings are accepted, validated and stored empty; `sticky_enabled` is
  forced false. No balance directives are emitted.
At least one server must be active.

### Client distribution (pool mode, migration 000052)

Two orthogonal settings. `balance_algorithm` picks the server for a **new**
client; `sticky_mode` decides whether a returning client keeps its server.

`balance_algorithm`:

| Value | Rendered | Weights | Notes |
| --- | --- | --- | --- |
| `roundrobin` (default) | nothing (HAProxy default) | dynamic | Servers take new clients in turn. |
| `static-rr` | `balance static-rr` | static | Runtime weight changes and `slowstart` have no effect. |
| `random` | `balance random(<balance_random_draws>)` | dynamic | `balance_random_draws` 1..5 (default 2): HAProxy draws that many random servers and takes the least loaded; 1 is pure random. |
| `leastconn` | `balance leastconn` | dynamic | Fewest active connections (connections/weight). Not Xray `leastLoad`. |
| `leastping` | nothing + agent annotations | dynamic | Xray `leastLoad`/`leastPing` analogue, see below. Requires Node Agent 1.1.0. |

`sticky_mode`:

| Value | Rendered | Behaviour |
| --- | --- | --- |
| `none` | only the algorithm | Every new connection is balanced by `balance_algorithm`. |
| `source` | `balance source` (or `balance hash src,ipmask(32,<sticky_ipv6_prefix>)` when the prefix is 32..127) + `hash-type <consistent\|map-based> sdbm avalanche` [+ `hash-balance-factor <n>`] | Stateless hash of the client address; `balance_algorithm` is ignored. `sticky_hash` `''` = consistent (only the moved servers' clients move), `map-based` = more even, but almost all clients move when the server set changes, and weights are static. `sticky_hash_balance_factor` 0 (off) or 101..1000 (UI: number input in %) caps a server at that % of the average load; consistent only. `sticky_ipv6_prefix` 0 = full address (`balance source`, byte-identical to 1.0.8). `client_ipv6=false` always renders plain `balance source`. |
| `source_table` | algorithm line, `stick-table type ipv6 size <sticky_table_entries> expire <sticky_ttl> peers nf_peers`, `stick on src,ipmask(32,<sticky_ipv6_prefix>)`, `option redispatch` | Remembered client: the algorithm picks the first server, the client then stays on it until `sticky_ttl` of inactivity, even if DNS answers or servers change. IPv4 keys are the full address (stored IPv4-mapped); IPv6 keys are grouped by `sticky_ipv6_prefix` (0 ⇒ 64; UI number input /32../128). `redispatch` moves a client off a dead server. This is the rc-era «table size + expire» stickiness. `client_ipv6=false` renders `stick-table type ip …` with `stick on src`. |

`sticky_ttl` (`source_table` only): HAProxy time `1m`..`7d`, default `1h`. The
UI takes a number with a minutes/hours unit plus quick picks (1m … 24h); a
stored `d` value (for example `7d`) is shown in hours. `sticky_table_entries`:
`''` (default `100k`) or `<n>k` with n 1..10000 or `<n>m` with n 1..10
(HAProxy `k`/`m` suffixes, migration 000053; the earlier `10k`/`100k`/`1m`
render unchanged). Measured on HAProxy 3.4.2 an IPv6 entry with `server_id`
costs about 226 bytes of RSS: ≈2 MB, ≈22 MB and ≈220 MB at 10k/100k/1m full
fill; the UI shows the estimate for the entered size. The size is an upper
limit, not a preallocation: entries are allocated as clients arrive, and when
the table is full HAProxy evicts the least recently used entry (that client is
simply balanced again). A `type ip` table (IPv4-only key) measured the same
≈228 B/entry as `type ipv6`, so memory is not a reason to switch: routes keep
`type ipv6` by default (IPv4 clients are stored IPv4-mapped). The route editor calls the field «Макс.
клиентов в памяти»: «Авто» sends `''` (renders `100k` = 102 400 clients), a
manual value is a plain client count rounded up to whole `k` (or `m` for exact
multiples of 1024²).

`client_ipv6` (migration 000054, default `true`): whether the route's client
tables accept IPv6 keys. `true` is the historical render (byte-identical for
every existing route). `false` is for nodes without IPv6 and makes every
client table of the route IPv4-only:

| Table | `client_ipv6=true` | `client_ipv6=false` |
| --- | --- | --- |
| `source_table` | `stick-table type ipv6 …`, `stick on src,ipmask(32,<sticky_ipv6_prefix\|64>)` | `stick-table type ip …`, `stick on src` |
| `source` | `balance source` or `balance hash src,ipmask(32,<prefix>)` | `balance source` |
| HAProxy bandwidth limiter | `stick-table type ipv6 size 1m …`, `filter bwlim-* … key src,ipmask(32,64)` | `stick-table type ip size 1m …`, `key src` |

With `client_ipv6=false` a sent `sticky_ipv6_prefix` is ignored and stored as
0. Bind lines do not change: a `*` listener renders `bind :<port>`, which
HAProxy opens on IPv4 only (`0.0.0.0`); IPv6 clients only reach listeners bound
to an IPv6 address (`::` is dual-stack). A `type ip` table cannot hold a
native IPv6 source: the key conversion fails, so such a connection gets no
stick entry (it is still balanced) and is not bandwidth-limited; only
IPv4-mapped/compatible IPv6 sources are converted (verified on HAProxy 3.4.2:
`::1` is stored as `0.0.0.1`). The editor shows it as «IPv6-клиенты» Вкл/Выкл in
«Распределение клиентов» (source/source_table), with the «Подсеть
IPv6-клиента» prefix field inactive while off. When any enabled route uses `source_table`, `global` gets
`localpeer nf_local` and a local `peers nf_peers` section
(`bind unix@/run/haproxy/nf_peers.sock`, `server nf_local`), so table entries
are handed over to the new process on reload (verified with a master-worker
reload on 3.4.2). The client address is the one HAProxy sees, i.e. the PROXY
protocol source when `accept_proxy_from` accepts the header.

Defaults when fields are absent: `sticky_mode` is `source` if
`sticky_enabled:true` or any server is a DNS pool (the 1.0.8 behaviour),
otherwise `none`. `balance_algorithm` is `leastconn` for `source_table`
(rc12 behaviour), otherwise `roundrobin`. Legacy `sticky_mode` input values
map as `leastconn` ⇒ `balance_algorithm:leastconn` + `none`, `roundrobin` ⇒
`roundrobin` + `none` (an explicit `balance_algorithm` wins). `sticky_mode:"sni"`
was removed (it balanced whole domains rather than clients) and returns
`400 validation_error` "sticky_mode sni was removed: …"; migration 000052
converts stored `sni` rows to `source`, `leastconn`/`roundrobin` rows to the
two-field form, and gives rc12 `source_table` rows `balance_algorithm:leastconn`.
Responses include every field and, for compatibility, `sticky_enabled` (true
iff `sticky_mode` is `source` or `source_table`). Routes that only use the
pre-000052 shapes render byte-identically and keep their fingerprints (the new
fields are hashed only when they express something the old fields could not);
the only render change is ` peers nf_peers` and the peers section for
`source_table` routes.

Single-target routes (no `servers[]`) keep the pre-000052 behaviour:
`sticky_mode` is derived as before and `balance_algorithm` is stored `''`.

**Common pool settings.** `slowstart` (`''` or HAProxy time `1s`..`10m`; UI
off/30s/60s/120s) is appended to every `server`/`server-template` line; it
requires `health_check` and, per HAProxy, only applies after a server was seen
down (never at HAProxy start), ramping its weight and connection limit
linearly. It has no effect with static weights (`static-rr`, `map-based`).
Per-server `weight` (1..256, 0/absent = HAProxy default 1, not rendered) is
rendered as ` weight <n>`: roundrobin, static-rr, random and both hash types
distribute proportionally to it, leastconn compares connections/weight,
`source_table` uses it for the first choice only.

**leastping (Xray `leastLoad`/`leastPing` analogue).** HAProxy has no latency
balancing, so the renderer emits annotations the Node Agent reads:

```
    # nf-weights backend=<be> algo=leastping tolerance=0.20 tolerance_ms=0
    # nf-weight server=<name> base=<weight or 1> cost=<cost>
    # nf-weight template=<name>_ slots=32 base=<w> cost=<c> ips=<ip>=<w>,…
```

Every 5 s the Agent reads `show stat` (`check_duration` of the last health
check) and `show servers state`, computes an effective latency =
`check_duration` × `cost` (per-server `cost` 0/absent = 1, up to 100; higher
cost = less preferred) and sets runtime weights with
`set server <be>/<srv> weight <w>`: servers within `leastping_tolerance`
(ratio 0..1, default 0.2, i.e. +20 % of the fastest) or within
`leastping_tolerance_ms` (0..1000, absolute floor) of the fastest get their
full base weight, slower ones base × fastest/latency (minimum 1). Base
weights are scaled by min(100, 256/max base) so ratios survive rounding. Down
or unchecked servers are ignored when finding the fastest. Only changed
weights are sent (with a 10 % jitter deadband), under the Agent's config
lock; a reload restores configured weights and the next pass reapplies them.
`leastping` forces `health_check:true`. With `sticky_mode:source` the
algorithm is inert and no annotations are emitted.

**Per-IP weights in a DNS pool.** A `dns_pool` server may carry
`ip_weights: [{"ip","weight","cost"}]` (max 32, unique normalised IPs;
`weight` 0 = the server's weight or 1..256; `cost` optional, 0/absent = the
server's cost or 0.01..100, rounded to 2 decimals; each entry sets a weight,
a cost or both). Template slots receive addresses only at runtime, so the Agent maps
slots to addresses through `show servers state` and sets the listed weight on
the matching slot (other slots keep the server's base weight), reapplying
when DNS changes or after a reload. With `leastping` a per-IP `cost` replaces
the server cost for the slot holding that address (effective latency =
check duration × cost); it is rendered as a separate `ipcosts=<ip>=<c>,…`
key on the template annotation, which Agents that predate it ignore (they
use the server cost). Cost-only entries are inert without leastping and are
then left out of the annotations. Without leastping the annotations use
`algo=static` and cover only templates with a per-IP weight. `ip_weights` need dynamic
weights: per-IP weights are rejected with `static-rr` and with `sticky_mode:source` +
`sticky_hash:map-based`; with the consistent source hash runtime weights
reshape the hash ring (HAProxy: consistent hashing is dynamic). For plain IP
pools use per-server `weight` (static config, no Agent needed).

**leastconn costs and tolerance («Меньше соединений»).** HAProxy
`balance leastconn` sends a new connection to the server with the lowest
connections/weight ratio (server weights are honoured and dynamic). NodeFlow
builds on that:

- **Cost** (`servers[].cost`, `ip_weights[].cost`, 0.01..100, 0/absent = 1):
  a connection to a server of cost *c* counts *c* times. Rendered statically,
  no Agent needed: every server line gets ` weight <base/cost × K>` with
  `base` = `weight` or 1 and K = min(100, 256 / max(base/cost)) over the
  backend (clamped 1..256), so a server of cost 2 receives half as many
  clients as an equal server of cost 1. Nothing changes (byte-identical
  output) when no server or address of the backend sets a cost. For a DNS
  pool the per-IP values are pinned by the existing `algo=static`
  annotation (`ips=<ip>=<base/cost × K>`, Agent ≥ 1.1.0).
- **Tolerance** (`balance_tolerance`, ratio 0..1, rounded to 2 decimals;
  alias of `leastping_tolerance`, same stored column; default 0 = plain
  `balance leastconn`): servers whose effective load (scur + 1) × cost / base
  is at most the least loaded × (1 + tolerance) count as equally loaded and
  share new clients by base weight and cost instead of all going to the
  single least loaded server. With a tolerance > 0 the backend renders no
  `balance` line (weighted round-robin, like `leastping`) plus

  ```
      # nf-weights backend=<be> algo=leastconn tolerance=0.20 tolerance_ms=0
      # nf-weight server=<name> base=<weight or 1> cost=<cost>
      # nf-weight template=<name>_ slots=32 base=<w> cost=<c> ips=… ipcosts=…
  ```

  Every 5 s the Agent reads `scur` from `show stat`, keeps the equally
  loaded group at its full base/cost × K weight and sets every other up
  server to weight 1 (not 0: it still serves if the group fails between
  passes) until the group catches up. Hysteresis: a server already in the
  group leaves only above best × (1 + tolerance × 1.5); only changed weights
  are sent, and a reload is followed by a reapply. Down and unresolved
  servers are not compared. The static base/cost weights stay on the server
  lines and act until the first pass or if the Agent stops.
  `leastping_tolerance_ms` is latency-only and stored 0 for leastconn. With
  `sticky_mode:source` the algorithm and both settings are inert; with
  `source_table` they choose the first server of a new client.

**PROXY protocol from hostnames.** When the `accept_proxy_from` union of a
listener contains hostnames (and does not already cover every source), the
renderer emits, instead of the plain `if { src … }` form:

```
    # accept PROXY protocol header from trusted sources only
    # nf-pp-trusted file=/etc/haproxy/nodeflow/pp-trusted-<frontend>.acl domains=a.example.com,b.example.com
    acl nf_pp_trusted src -f /etc/haproxy/nodeflow/pp-trusted-<frontend>.acl
    tcp-request connection expect-proxy layer4 if { src <static IP/CIDRs> } || nf_pp_trusted
```

(`if nf_pp_trusted` when there are no static entries). Listeners without
hostnames render byte-identically to before; the renderer version does not
change. The Agent owns the ACL file: before every `haproxy -c` (validate,
apply, rollback) it resolves the hostnames (A+AAAA, all addresses) and writes
the file atomically — an empty file when nothing resolved yet, so nobody is
trusted through the hostname until it does. Every 30 s it re-resolves; only
when the address set changed it rewrites the file and swaps the loaded ACL
through the runtime API (`prepare acl` / `add acl @<ver>` / `commit acl
@<ver>`, atomic) without a reload. An unchanged set sends nothing to the
stats socket. A failed lookup keeps the last known addresses of the name
(logged once per state change); files of removed listeners are deleted.

**Agent gates.** A leastconn `balance_tolerance` > 0 (pool mode, sticky not
`source`, servers[] backend) needs Node Agent ≥ 1.1.1 (a 1.1.0 Agent rejects
`algo=leastconn`): `422 leastconn_tolerance_requires_agent_1_1_1` on save
and on publish. Effective `leastping` (pool mode, sticky not `source`) and
any `ip_weights` need Node Agent ≥ 1.1.0 (the runtime weight controller,
`NODE_AGENT_WEIGHTS=apply|off`, default `apply`). Panel returns `422`
`leastping_requires_agent_1_1` / `ip_weights_requires_agent_1_1` on save and
on publish, exactly like the kernel shaper gate below.

Per-server `dns_pool:true` (DNS hostname only): the server is rendered as a
32-slot `server-template` with `resolvers nf_dns`. Static and DNS-pool servers
may be mixed. Health checks are forced on for the route when any server is a
DNS pool. `preferred_ip` (IPv4/IPv6, stored normalised) is valid only on a
DNS-pool server that is the first server of a `failover` route; a single
DNS-pool server with `preferred_ip` and no `balance_mode` is treated as
`failover`. Panel then emits `server <name>_pref <preferred_ip>:<port>` as the
active server and the template as backup.

Failover semantics of `preferred_ip`: the preferred address is the only active
server of the backend and receives all traffic while its health check passes.
The DNS-pool `server-template` behind it is rendered with `backup`, and the
backend gets `option allbackups`, so once the preferred server is marked down
(3 failed checks at 5s interval) every healthy resolved address of the pool
takes traffic at the same time (roundrobin over all of them), not only the
first backup. As soon as the preferred server passes 2 checks again, HAProxy
sends new connections to it only; established connections on pool servers are
not moved. The preferred address does not have to be one of the resolved
addresses; if it is, that address simply appears both as `_pref` and as a
template slot. The UI helper `GET /api/v1/dns/resolve` only suggests
candidates, Panel does not re-check the stored `preferred_ip` against DNS.

HAProxy has only two tiers (active/backup). When a DNS pool is a reserve (a
backup template, or the template behind `preferred_ip`), `option allbackups`
is emitted so the load is spread over all resolved addresses. Because that
option activates every backup at once, a DNS-pool reserve must be the only
reserve of a failover route (`422`/`400 validation_error` otherwise).
Server names that collide with template slots (`<pool name>_<digits>`) are
rejected. `custom_fragment` cannot override `balance`, `hash-type`, `server`,
`server-template` or `default-server` when any server is a DNS pool.

`quota_action:block_new` is rejected for multi-server routes and DNS pools:
runtime enforcement disables the single `nf_srv_<id>` server, which such
backends do not have. Their runtime names carry `multi_server:true` (or
`dns_pool:true`) and are skipped by quota enforcement; observe-only
accounting remains available.

The legacy `sticky_table_size` and `sticky_expire` fields (removed from the
schema by migration 000030) are still accepted for compatibility and ignored.

`custom_fragment` is the compatibility field used by the advanced
«manual backend directives» editor. It is limited to 8192 bytes and is rendered
only inside the generated backend of that route. CRLF is normalized to LF,
leading indentation is normalized to four spaces, surrounding blank lines are
removed and internal blank runs are collapsed. Top-level HAProxy section
headers, control characters, HAProxy backslash escapes and lines longer than
512 bytes are rejected. This is not a full HAProxy config editor: it cannot
replace or extend `global`, `defaults`, `frontend`, `listen`, `resolvers` or any
other section.

Panel validation establishes the route-level boundary. The Node Agent still
runs `haproxy -c` against the complete generated configuration before atomic
replacement/reload. If validation or reload fails, the previous actual config
remains active and the route reports failed deployment.

For backward compatibility, an old request with `hostname`, `target_host` and
`target_port` is accepted as one SNI on listener `*:443`, TCP target and
`proxy_protocol: "none"`. Responses retain `hostname` as the first SNI and
retain `target_host`/`target_port`; fallback and Unix routes return empty/zero
legacy values. When both `hostname` and `snis` are sent, `hostname` must equal
the first normalized SNI. `PUT` remains a full replacement and requires
`enabled`, with one exception for the 1.1.0 fields: when a key is absent from
the body (or `null`), the stored value is kept instead of being reset:

- `servers` absent: stored servers and their `backup` flags are kept (an
  explicit `balance_mode` in the same body still applies to them). The target
  fields are then derived from the stored servers. `"servers": []` clears
  them.
- `accept_proxy_from`, `shaper_mode`, `sticky_ttl` absent: stored value.
- `sticky_mode` and `sticky_enabled` both absent: stored `sticky_mode`.
- `balance_algorithm`, `balance_random_draws`, `leastping_tolerance`,
  `leastping_tolerance_ms`, `sticky_hash`, `sticky_hash_balance_factor`,
  `sticky_table_entries`, `sticky_ipv6_prefix`, `slowstart`, `client_ipv6`
  absent: stored value. Per-server `weight`, `cost` and `ip_weights` inside an explicit
  `servers[]` are taken as sent.

A `PUT` whose normalised spec is identical to a settled route (`active` and
in sync, or a `draft`/`disabled` route that is not deployed) returns `200`
with the unchanged route and `version`, and publishes no revision (no
HAProxy reload). `POST` always persists `enabled:false`, even if an older
client sends `true`: creating a route never changes HAProxy. Enabling is an
explicit `PUT` or `PATCH`.

**Enable/disable only** (`1.1.0+`):

```
PATCH /api/v1/nodes/{node_id}/routes/{route_id}
{"enabled": false, "expected_version": 4}
```

`enabled` is required; `expected_version` is optional (positive integer,
`409 stale_route_version` when stale). No other key is accepted
(`400 invalid_json`). Every other field, including `servers`,
`balance_mode`, `accept_proxy_from`, `sticky_mode` and `shaper_mode`, is kept
exactly as stored. The response is `200` with the full route, the same shape
as `GET` (including `servers`). The publish rules are the same as for `PUT`.

`POST`, `PUT` and `PATCH` responses, and the `202` body of a pending
`DELETE`, contain the complete route including `snis` and `servers`, exactly
as `GET /routes/{route_id}` returns it.

Every route response also contains:

```json
{
  "version": 4,
  "enabled": true,
  "deployed": true,
  "deployment_state": "active",
  "deployment_error": "",
  "desired_revision": 12,
  "applied_revision": 12,
  "desired_fingerprint": "64-lowercase-hex-characters",
  "deployed_fingerprint": "64-lowercase-hex-characters",
  "delete_pending": false
}
```

`enabled` is operator intent; `deployed` is actual HAProxy membership.
`deployment_state` is `draft`, `pending`, `active`, `disabled`, `failed` or
`deleting`. Fingerprints distinguish an edited active desired spec from the old
spec still running while apply is pending/failed. Revision numbers are an
internal convergence aid; normal UI does not expose revision controls.

An edit of a disabled, non-deployed route only updates Panel. Enabling,
disabling or editing an enabled route atomically saves intent, renders the full
node config, appends a hidden immutable revision and assigns it. The previous
actual config remains active until Agent validation/reload succeeds. Failed
apply reports set per-route `deployment_state:"failed"` and a stable bounded
`deployment_error`; Panel does not keep reissuing that failed revision. The
next edit/toggle retries through a new immutable revision. Disabling/deleting
the final active route applies a deterministic valid HAProxy base config with
no listeners.

Deleting a draft/already-disabled route returns `204`. Deleting a route that is
active or belongs to an outstanding operation returns `202` with
`delete_pending:true`; its row is removed only after Agent reports that removal
revision (or a later superseding revision that also omits it) as actual. A
verified heartbeat performs the same finalization when the report was lost.

For optimistic concurrency, a client should echo the current `version` as
`expected_version` in a `PUT` JSON body, or use
`DELETE .../{route_id}?expected_version=4`. It is optional for compatibility.
A stale value returns `409 stale_route_version`; successful intent mutations
increment `version`. Deployment-only Agent updates do not increment it.

Duplicate SNI or fallback conflicts return HTTP `409 already_exists`. Other
invalid or unsafe route shapes return HTTP `400 validation_error`.

**Kernel shaper and Agent version.** `shaper_mode:"kernel"` with a client
limit is enforced by the Node Agent (nftables), which Agent 1.1.0 introduced.
Panel reads the Agent version from the node's latest heartbeat (semver
`MAJOR.MINOR.PATCH`; a pre-release such as `1.1.0-rc1`, an unparsable or an
unknown version counts as older). When it is below `1.1.0`, Panel returns
`422` with code `kernel_shaper_requires_agent_1_1`:

- on `POST`/`PUT` that saves a kernel-shaped route with a limit;
- on any mutation that would publish a revision, and on `render-config` /
  `config-revisions/from-routes`, while an enabled kernel-shaped route exists
  (for example after an Agent downgrade). The previous config stays
  assigned; the limit is never silently dropped. Disabling that route (or
  switching it to `shaper_mode:"haproxy"`) is still accepted.

Kernel mode without any client limit renders nothing and needs no Agent
support.

### HAProxy renderer contract

`POST /api/v1/nodes/{node_id}/render-config` has no request body. It returns the
complete config, SHA-256, renderer version, counts, warnings and stable runtime
names. Disabled routes are excluded. An empty enabled route set returns
`422 no_enabled_routes`; a defensively detected inconsistent stored route set
returns `422 invalid_route_set`.

The current renderer rejects wildcard/specific bind overlaps, caps enabled routes at
1024 and emits one ACL per SNI route, with up to 32 SNI values per `acl` line
(one sample fetch per line instead of per value; a line holds at most 64 words). It groups routes by
listener IP/port and emits TCP listeners with TLS ClientHello inspection only
when SNI routing is present, exact case-insensitive SNI ACLs and one optional
`default_backend` for the listener fallback. TCP and Unix-socket targets,
`send-proxy` and `send-proxy-v2` are supported. Each generated backend is named
`nf_be_<route_uuid_without_hyphens>`; HAProxy runtime statistics can therefore
be mapped back to route IDs without fuzzy matching. The generated global
section exposes the administrative stats socket at
`/run/haproxy/admin.sock`.

Non-empty `custom_fragment` values are emitted as canonical manual directives
inside only the owning route's `nf_be_<route_uuid_without_hyphens>` backend.
Render responses expose `manual_backend_routes`; immutable metadata records
`custom_fragment_policy:"route_backend_directives"`. The warning explicitly
states that manual directives are active and must pass Agent-side HAProxy
validation. Renderer versions v1-v15 remain readable for existing immutable
revisions; new revisions use `haproxy-tcp-sni-v16`.

`observe` quotas are metadata and warnings only. `block_new` is emitted in
immutable runtime metadata. Panel derives monthly usage from reset-safe
counters and Agent disables the exact generated server via HAProxy Runtime API
after the limit. This prevents new backend connections without killing current
sessions. A config reload resets runtime server state; Agent intentionally
forgets its cache and re-applies the active policy on the next heartbeat.

### Traffic accounting contract

`GET /api/v1/nodes/{node_id}/traffic?month=YYYY-MM` defaults to the current UTC
month when `month` is omitted. HAProxy cumulative `bytes_in`/`bytes_out`
counters are converted to monotonic deltas. A lower counter is treated as an
HAProxy restart/reset and the new value is added; an identical heartbeat adds
zero. The first observed snapshot accounts the traffic accumulated since the
current HAProxy process started. A delta is attributed to the UTC month in
which Panel received the heartbeat.

The response contains node totals, every observed HAProxy `backend_key`, and
one quota status per configured route. Renderer backend names follow
`nf_be_<route_uuid_without_hyphens>`, so known backends include `route_id`.
Unknown/custom HAProxy backends remain visible by `backend_key`. A route with no
observed backend row returns `observed: false` and zero usage rather than hiding
the missing mapping.

`used_bytes` is `bytes_in + bytes_out`. Route `limit_bytes` and policy come from
the immutable actual revision, not a mutable draft; `applied` makes that explicit.
`reached` reports the counter threshold. `block_requested`
shows desired maintenance and `blocked` is the Agent-reported actual Runtime
API state. `enforcement` is true when at least one route uses `block_new`.

To persist the same deterministic result, call:

```http
POST /api/v1/nodes/{node_id}/config-revisions/from-routes
Content-Type: application/json

{"note":"operator note, optional"}
```

The created immutable revision metadata includes `source`, `renderer`, counts,
`custom_fragment_policy`, `quota_enforcement`, and
`route_backends: {route_uuid: backend_name}` and
`route_fingerprints: {route_uuid: desired_spec_sha256}`. This endpoint never changes
`desired_revision`; use `PUT .../desired-revision` as a separate explicit step.

## Current MVP: Agent API (default `127.0.0.1:4200`)

All endpoints except health require `Authorization: Bearer $NODE_AGENT_TOKEN`.

| Method | Endpoint | Purpose |
|---|---|---|
| `GET` | `/v1/health` | Loopback liveness plus Agent version. |
| `GET` | `/v1/info` | Agent/HAProxy version and managed path. |
| `GET` | `/v1/stats` | Host statistics. |
| `POST` | `/v1/config/validate` | Validate `{ "config": "..." }` with HAProxy. |
| `POST` | `/v1/config/apply` | Validate, back up and atomically replace the managed config. |
| `POST` | `/v1/config/rollback` | Restore the newest retained known-good backup. |
| `POST` | `/v1/update/verify` | Diagnostic verification of an already-staged signed artifact. |

This API remains loopback-only. The process fails closed on a non-loopback bind
unless `NODE_AGENT_ALLOW_REMOTE_LISTEN=true` is set explicitly. That override is
intended only for isolated diagnostics because this local API is plaintext.
Normal reconciliation does not use it: Agent initiates mTLS requests to the
dedicated Panel listener on TCP 4200.

`/v1/update/verify` obeys
`NODE_AGENT_SELF_UPDATE_MODE=off|verify-only|apply`. It runs only the verifier;
actual download/activation is driven by a heartbeat assignment and the separate
updater systemd service.

Every current Agent heartbeat also reports the locally observed configuration
state when available:

```json
{
  "actual_revision": 42,
  "config_sha256": "64-lowercase-hex-characters"
}
```

Panel verifies the pair against its immutable revision ledger. A missing,
unknown or checksum-mismatched state marks the node `drifted` and reissues the
explicit desired revision even when the last stored apply report said
`in_sync`. Older Agents that omit both fields remain compatible. An unknown
local revision in an intermediate apply report is retained as bounded audit
metadata rather than violating the revision foreign key.

When no work is assigned, heartbeat returns:

```json
{"status":"accepted","node_id":"uuid"}
```

When an already assigned desired revision differs from actual, the same response also includes:

```json
{
  "assignment": {
    "revision": 42,
    "config": "global\n  daemon\n",
    "sha256": "64-lowercase-hex-characters"
  }
}
```

An in-sync active renderer revision may also return `quota_assignment` and
`firewall_assignment`. An explicitly assigned newer Agent release may return
`update_assignment` with signed version/platform/SHA/size/sequence/path fields.
Quota entries contain only deterministic
`nf_be_<uuid>/nf_srv_<uuid>` objects. Firewall ports come only from immutable
actual-revision metadata. Panel mode `apply` cannot exceed the Agent's local
firewall ceiling. Agent never enables absent/inactive UFW. New-node SSH
bootstrap may install and enable it only after explicit opt-in and after adding
the separate `nodeflow-ssh` safeguard for the real server-side sshd port.
When bootstrap is explicitly submitted with `allow_firewall_apply:true`, the
new node starts in Panel mode `apply` (existing nodes are unchanged). After a
route revision becomes actual, its HAProxy TCP listener ports are therefore
opened automatically with exact `nodeflow`-tagged UFW rules; disabling
or deleting the last route on a port removes only that tagged stale rule.

The Agent treats Panel's integer revision as decimal string `"42"` in its local revision marker. The marker is published only after a successful HAProxy reload, and idempotency requires both the marker and managed-config SHA-256 to match. Configurations are limited to 524288 bytes; the ten newest known-good backups are retained. Assignment checksum mismatch, validation and activation failures produce bounded reports with stable error codes only; raw HAProxy/systemd output is never reported.

## Deferred hardening

Bootstrap request credentials remain write-only and ephemeral:

```json
{
  "host":"192.0.2.10",
  "port":22,
  "user":"root",
  "auth":{"type":"password","password":"ephemeral"},
  "host_key_fingerprint":"SHA256:..."
}
```

Current bootstrap rejects missing host-key pins, runs as a bounded asynchronous
job and manual rotation revokes old bearers after a fresh certificate is
installed. Remaining work includes durable/cancellable jobs, certificate
revocation/CRL, signing-key rotation, correlation IDs and general
`Idempotency-Key` support outside the dedicated renewal contract.

Future asynchronous apply responses may use:

```json
{
  "node_id":"uuid",
  "desired_revision":42,
  "actual_revision":42,
  "state":"applied",
  "rollback":{"attempted":false,"succeeded":null}
}
```

Target semantics use `200` for an already-satisfied idempotent request, `202` for asynchronous work, `409` for revision conflicts and `422` for validation failure. Diagnostics must be bounded and sanitized.

## 1.1.0 route field additions

New optional fields on route create/update (all backward compatible):

| Field | Type | Default | Description |
|---|---|---|---|
| `accept_proxy_from` | `[]string` | `[]` | IPv4/IPv6 addresses, CIDRs or hostnames that may send a PROXY protocol header to this listener. A hostname (RFC 1123, ≤ 253 chars, lower-cased, trailing dot dropped; no wildcards, URLs or ports; at most 32 per route) means every A/AAAA address of the name (DNS pool), resolved and kept current by Node Agent ≥ 1.1.3 — see "PROXY protocol from hostnames" below; otherwise `422 pp_trusted_domains_requires_agent_1_1_3` on save and on publish. Renderer emits `tcp-request connection expect-proxy layer4 if { src … }` per frontend (union across routes). Only the literal `0.0.0.0/0` and `::/0` count as "all": any other prefix of length 0 (`10.0.0.0/0`, `2001:db8::/0`) and IPv4-mapped IPv6 prefixes (`::ffff:0:0/96`, `::ffff:10.0.0.0/104`) are rejected with `400 validation_error` (write the IPv4 CIDR instead). The sentinel `["0.0.0.0/0","::/0"]` (order-insensitive; either entry alone is also valid) means "expect PROXY from all sources": when the listener union covers every address family the bind receives (`0.0.0.0/0` for `*` and IPv4 binds, `::/0` for a specific IPv6 bind, both for `::`), the renderer emits the unconditional `tcp-request connection expect-proxy layer4`, and direct clients without a PROXY header are then rejected on that listener. Otherwise the conditional form is kept byte-identically. |
| `servers` | `[]object` | `[]` | Explicit multi-server backend (max 16). Each entry: `position`, `name`, `target_type`, `host`/`port` or `unix_socket_path`, `backup`, `dns_pool`, `preferred_ip`. Empty = legacy single-target mode. See "Multi-server backends and balance mode". |
| `balance_mode` | `string` | derived | `pool` or `failover`. Absent: keeps explicit `backup` flags (any backup ⇒ `failover`). |
| `sticky_enabled` | `bool` | `false` | Legacy client-IP affinity. Used only to derive `sticky_mode` when that is absent; in responses true iff `sticky_mode` is `source` or `source_table`. |
| `balance_algorithm` | `string` | derived | `roundrobin`, `static-rr`, `random`, `leastconn` or `leastping` (pool mode). Absent: `leastconn` for `source_table`, else `roundrobin`. |
| `balance_random_draws` | `int` | `2` | `random` only: 1..5. |
| `leastping_tolerance` | `number` | `0.2` | Relative tolerance 0..1 for `leastping` (latency, default 0.2) and `leastconn` (connection load, default 0). 1.1.0 name of `balance_tolerance`; both are returned. |
| `balance_tolerance` | `number` | see left | Alias of `leastping_tolerance`; when both are sent they must be equal. `leastconn` > 0 needs Agent ≥ 1.1.1. |
| `leastping_tolerance_ms` | `int` | `0` | `leastping` only: absolute tolerance floor, 0..1000 ms. |
| `sticky_mode` | `string` | derived | `none`, `source` or `source_table` (pool mode). Legacy `leastconn`/`roundrobin` accepted and mapped; `sni` rejected. Absent: `source` if `sticky_enabled` or any DNS pool, else `none`. Stored `''` in failover. |
| `sticky_ttl` | `string` | `1h` | `source_table` only: stick-table expiry, HAProxy time `1m`..`7d` (UI presets up to `24h`). |
| `sticky_hash` | `string` | `''` | `source` only: `''` (consistent) or `map-based`. |
| `sticky_hash_balance_factor` | `int` | `0` | `source` + consistent only: 0 or 101..1000. |
| `sticky_table_entries` | `string` | `100k` | `source_table` only: `<n>k` (1..10000) or `<n>m` (1..10). |
| `sticky_ipv6_prefix` | `int` | `0` | `source`/`source_table`: 0 or 32..128. Forced to 0 when `client_ipv6` is `false`. |
| `client_ipv6` | `bool` | `true` | `false`: IPv4-only client tables (`type ip`, key `src`) for the stick-table, source hash and HAProxy bandwidth limiter. |
| `slowstart` | `string` | `''` | HAProxy time `1s`..`10m`; requires `health_check`. |
| `servers[].weight` | `int` | `0` | 0 (HAProxy default) or 1..256. |
| `servers[].cost` | `number` | `0` | 0 (=1) .. 100. `leastping`: latency multiplier; `leastconn`: connection multiplier (rendered as weight base/cost × K). |
| `servers[].ip_weights` | `[]object` | `[]` | DNS-pool servers only: `[{"ip","weight","cost"}]`, max 32, Agent-applied; `weight` 0 = server weight, `cost` (leastping/leastconn) 0 = server cost. |
| `sticky_table_size` | `string` | — | Deprecated (migration 000030). Accepted and ignored. |
| `sticky_expire` | `string` | — | Deprecated (migration 000030). Accepted and ignored. |

### match_mode deprecation (F6)

`match_mode: "fallback"` is the new canonical value for all non-SNI routes.
The values `any_tcp` and `destination_ip` remain accepted on input and are silently normalized to `fallback`.
The Panel API always returns `"fallback"` for such routes.
The wildcard vs concrete-IP distinction is expressed solely by `listener_ip`.

### /metrics endpoint (F5)

Enabled by `PANEL_METRICS_ENABLED=true`. Requires `Authorization: Bearer <PANEL_METRICS_TOKEN>`.
Optional CIDR-based allowlist via `PANEL_METRICS_ALLOW_CIDRS` (comma-separated).

Exposed metrics (all labelled `node="<node_id>"`):

```
nodeflow_node_up
nodeflow_node_last_seen_seconds
nodeflow_node_routes_total
nodeflow_node_routes_enabled
nodeflow_node_connections_current
nodeflow_node_backends_healthy / _degraded / _unavailable
nodeflow_node_traffic_bytes_in_month
nodeflow_node_traffic_bytes_out_month
nodeflow_node_desired_revision
nodeflow_node_actual_revision
nodeflow_node_config_in_sync
```

Example Prometheus scrape config:

```yaml
scrape_configs:
  - job_name: nodeflow
    static_configs:
      - targets: ['panel.example.com:8080']
    metrics_path: /metrics
    authorization:
      credentials: '<PANEL_METRICS_TOKEN>'
```

Example alert rules:

```yaml
groups:
  - name: nodeflow
    rules:
      - alert: NodeFlowNodeDown
        expr: nodeflow_node_up == 0
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "NodeFlow node {{ $labels.node }} is offline"

      - alert: NodeFlowConfigDrift
        expr: nodeflow_node_config_in_sync == 0
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "Node {{ $labels.node }} config not in sync for 10+ min"
```
