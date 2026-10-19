# Architecture and implementation decisions

Format: short ADR entries. New decisions are appended at the bottom; old ones
are not rewritten but, where necessary, superseded by a new entry
("supersedes D-xxx").

---

## D-001 – Go as the language, minimum version 1.26, toolchain 1.27.2

**Context:** PLAN.md proposes Go 1.23+. The current versions of the
required libraries (`golang.org/x/sys`, `modernc.org/sqlite`,
`prometheus/client_golang`, …) require at least Go 1.26.

**Decision:** `go 1.26.0` as the minimum version in `go.mod`,
`toolchain go1.27.2` (latest stable version at project start) for builds.
Go remains the language: a static binary without CGO, good networking libraries,
a small image.

**Consequence:** golangci-lint must be built with Go ≥ 1.26 (CI uses
v2.14.0). The Docker build stage uses `golang:1.27`.

## D-002 – Module path

`github.com/meister119/leasorix` (matches the GitHub repo). Renaming
is possible at any time via `go mod edit -module` + search/replace.

## D-003 – Bootstrap configuration (YAML/env) vs. DHCP configuration (DB)

**Decision:** The YAML file (`--config`) and the `LEASORIX_*` variables
contain only process-level settings (data directory, listen addresses,
logging, interfaces, later HA peers/TLS). Scopes, reservations, options,
policies, etc. live exclusively in the database and are managed via the
REST API/UI or import/export (phase 7).

**Rationale:** A single "source of truth" for the DHCP configuration is a
prerequisite for config replication in HA mode (phase 6,
single-writer principle) and for the audit log. Two competing sources
(file and UI) would create conflicts.

**Env override details:** Name = `LEASORIX_` + YAML path in uppercase,
levels joined with `_` (`http.listen` → `LEASORIX_HTTP_LISTEN`).
String lists are comma-separated, durations use Go format (`10s`, `1h`). Unknown
YAML keys are an error (typos should not be silently
ignored).

## D-004 – SQLite: modernc, WAL, synchronous=FULL, separate pools

**Decision:**
- Driver `modernc.org/sqlite` (pure Go, no CGO → static binary).
- `journal_mode=WAL`, `foreign_keys=ON`, `busy_timeout=10s`.
- `synchronous=FULL`: A lease confirmed by an ACK must survive a power
  failure; otherwise a crash risks duplicate assignment. Performance is
  measured in phase 2; if necessary, group commit is added rather than
  lowering durability.
- Two connection pools: a writer with exactly one connection and
  `BEGIN IMMEDIATE` (all writes serialized within the process, no
  `SQLITE_BUSY` deadlocks), and a reader pool with `query_only=1`.

## D-005 – Migrations with goose (embedded SQL files)

`github.com/pressly/goose/v3` via the `Provider` API (no global
state), migrations as `.sql` files embedded in the binary via `go:embed`. Migrations run
on every start and are idempotent. Few dependencies, down migrations
possible.

## D-006 – IPv4 addresses as INTEGER in the database

**Decision:** IPv4 addresses are stored as `INTEGER` (uint32 value);
the API returns strings.

**Rationale:** Range checks ("IP in the pool?", overlap of scopes and
exclusions), sorting and statistics can be expressed directly in SQL.
Drawback: harder to read in the raw DB. DHCPv6 (phase 10) gets
its own tables with 16-byte BLOBs.

## D-007 – Management listener on localhost only by default

`http.listen` defaults to `127.0.0.1:8067` (PLAN.md section 6). For
external access, a management IP must be set explicitly. One
listener serves the UI, REST API, `/healthz`, `/readyz` and `/metrics`.

## D-008 – No trust in `X-Forwarded-For`

The chi middleware `RealIP` is not used: it accepts
`X-Forwarded-For` unchecked and would make the login rate limit (phase 5) and the
audit log (client IP) spoofable. If a reverse proxy is to be
supported, an explicit list of trusted proxies will be added.

## D-009 – YAML library `go.yaml.in/yaml/v3`

Official successor of the archived `gopkg.in/yaml.v3` (same API,
maintained by the YAML org).

## D-010 – Linter profile

golangci-lint v2 with `standard` + bodyclose, errorlint, gosec, misspell,
noctx, nolintlint, unconvert, copyloopvar, durationcheck, nilerr.
`gosec G115` (integer conversion) is disabled because IPv4 arithmetic
constantly converts between `uint32`/`int64`; value ranges are checked during
validation. In tests, `gosec` and `noctx` are off.

---

## D-011 – DHCPv4 socket model

**Decision:** One UDP socket per configured interface on
`0.0.0.0:67` with `SO_BINDTODEVICE`, `SO_BROADCAST`, `SO_REUSEADDR` and
`IP_PKTINFO` (destination address → server identifier for unicast/relay;
source address when sending = server identifier). Replies to clients without
an address and without the broadcast flag go via an `AF_PACKET` datagram socket
(protocol 0, receives nothing) directly to `chaddr`/`yiaddr` (RFC 2131 4.1).
If that is not possible (no `CAP_NET_RAW`, no Ethernet), the server falls back
to broadcast – which RFC 2131 explicitly allows.

**Consequence:** Relays must be reachable via one of the configured interfaces
(no socket sees packets on other interfaces).
If an interface is missing at startup, binding is retried every 5 s; until then,
`/readyz` reports "not listening".

## D-012 – Client identity and reservation matching

A client is identified by option 61 (client identifier), or, without
option 61, by `chaddr` (RFC 2131 4.2). The same MAC with different
client IDs counts as different clients (e.g. PXE vs. operating system).
Reservations match on the client ID first, then on the MAC.

## D-013 – Lease states, times and defaults

| State | Meaning |
|---|---|
| `active` | bound until `expires_at` |
| `expired` | expired, held for the same client for `expired_grace` |
| `released` | released by the client, address immediately available again, record kept for affinity |
| `declined` | DECLINE or conflict, blocked until `expires_at` |

- `expired_grace` = 4 h (Windows parity).
- `decline_time` = 24 h (Kea default "decline-probation-period"). Windows
  blocks "BAD_ADDRESS" indefinitely; 24 h is the compromise between
  safety and pool loss, configurable.
- Offers (OFFER) are kept in memory only (`offer_timeout` 60 s),
  not persisted: after a restart they are worthless anyway.
- Lease time 0 = unlimited (`0xffffffff`, without T1/T2). T1 = 50%, T2 = 87.5%.
- There is exactly one active binding per client and scope; older ones are set
  to `released` when a new address is ACKed.

## D-014 – Order of address selection

Reservation > pending offer > the client's existing lease
(active before most recently used) > requested address (option 50) > never-used
address (cursor, round-robin) > released address (oldest first) >
expired address of other clients (oldest first, even within the
grace period). The last stage applies only when the pool is exhausted – the
grace period is a preference, not a hard block, so that a full pool
does not starve because of past leases.

## D-015 – INIT-REBOOT/RENEW without a local record

RFC 2131 recommends silence if the server has no record of the client.
Instead, Leasorix confirms (ACK) if the address is valid, free and
not reserved, and creates the lease. NAK if it belongs to another
client, is reserved/excluded or is on the wrong network.
Silence only if no scope exists at all for the link network.

**Rationale:** After data loss or a restart without a record,
"silence" would let the client keep using its address unnoticed while
we consider it free → duplicates. With an ACK, the binding is recorded.
In phase 6 (HA), this is extended with the partner's pool responsibility.

## D-016 – Reservations

- A reserved address must be within the subnet (not the network/broadcast address),
  but not necessarily in the dynamic range; it is never assigned dynamically,
  not even within an exclusion.
- If a client has a reservation, it gets only that address.
  If the address is currently assigned to another client or responds to the
  conflict ping, the reserved client gets **no** offer (warning in the
  log) instead of a dynamic substitute address. The other client receives a NAK
  on its next renew.

## D-017 – Scope rules

Subnets must not overlap, prefix /8 to /30, the subnet of an
existing scope is immutable (as in Windows), exclusions must lie within the
range and must not overlap, lease time ≥ 10 s or 0.
A disabled scope no longer makes offers and answers renewals
with a NAK so that clients look for a new address.

## D-018 – Link selection for relays

Priority: option 118 (Subnet Selection, RFC 3011) > relay sub-option 5
(Link Selection, RFC 3527) > `giaddr`. Relay sub-option 11 (Server
Identifier Override, RFC 5107) is used as the server ID. Option 82 is
returned unchanged (RFC 3046), as is option 61 (RFC 6842).

## D-019 – Conflict detection

On by default (500 ms, 1 attempt) – unlike Windows (off by default), because
a duplicate is more costly than the delay on the first offer. Only
addresses that the client does not already actively hold are checked; an
offer that has already been checked is not pinged again. If the address responds, the
ARP cache is checked: if the reply comes from the requesting client itself, it is
not a conflict. Conflicts are stored as `declined` (for `decline_time`).
A raw ICMP socket is preferred, with a fallback to an unprivileged
ICMP socket; otherwise the feature is disabled with a warning.

## D-020 – Lease persistence: group commit

An ACK is sent only once the lease is durably stored in SQLite. All
writes go through a writer goroutine that batches concurrent
requests into one transaction (one `fsync` for many leases).
Decisions are made in memory under a mutex; memory may only be ahead of the
disk in the safe direction (the address already counts as
in use). If a batch fails, the requests are retried individually.

## D-021 – BOOTP ignored for now

Packets without a DHCP message type (BOOTP) are dropped in phase 2; BOOTP is
optional in phase 7.

## D-022 – Integration tests with network namespaces

The tests under `test/integration` (build tag `integration`, root required)
set up their own namespaces + veth pairs per test. The Go test client creates
its sockets via `setns` on a locked OS thread directly in the
client namespace; the same technique serves as `DialContext` for HTTP checks
into the server namespace. Note: busybox `udhcpc -q -R` does not send a RELEASE
(it exits before BOUND) – the test therefore terminates udhcpc with SIGTERM.

---

## D-023 – Option model

- Option values are stored typed (`ip`, `ip-list`, `string`,
  `uint8/16/32`, `int32`, `bool`, `hex`, `domain-list`, `routes`) – in
  canonical text form; the wire encoding is produced when the
  snapshot is built. This keeps DB contents readable and makes export/import (phase 7) trivial.
- Built-in catalog in `internal/dhcpopt` (RFC 2132 + 3397/3442/4833/8910,
  Cisco 150, Microsoft 249/252). Further codes only via custom
  option definitions (code + name + type), e.g. for 224–254.
- Server-managed codes are not configurable: 1 (from the subnet),
  51 (lease time from scope/policy), 54, 58/59 (from the lease time), as well as the
  purely client-side or relay options 50, 53, 55, 57, 61, 82, 118.
- Option 43 is maintained as `hex` (vendor-specific, encapsulated).

## D-024 – Classes take effect via policies (no class-qualified values)

Besides policies, Windows also has option values that are bound directly to a
vendor/user class. Leasorix models classes as named
definitions (type vendor = option 60, user = option 77, wildcard patterns)
that are referenced in policy conditions (`field: class`). Options for
a class = a policy with a condition on the class + option values on the
policy. This is more expressive (can be combined with MAC/option 82) and
avoids a second, competing inheritance axis.

**Inheritance (most specific wins):** Server → scope → server policies →
scope policies → reservation. Within one level, the policy with
the lowest processing order wins.

## D-025 – Policy semantics

- Conditions on `mac`, `client_id`, `hostname`, `vendor_class`,
  `user_class`, `class`, `relay_circuit_id`, `relay_remote_id`,
  `relay_subscriber_id`; operator `eq`/`ne`; combined with AND or OR at the
  top level (no nested groups – conservative, covers the
  Windows cases).
- Wildcards `*` and `?`, case-insensitive comparison. For binary fields
  (option 82, client ID), a value with the prefix `0x` is compared against the
  hex representation.
- Option 77 is parsed per RFC 3004; if the content is not in valid
  RFC 3004 format (Windows clients), it counts as a single entry.
- **IP ranges** only for scope policies. Addresses in a policy range
  go exclusively to matching clients; non-matching clients get
  the rest of the scope range. If the range of the matching policy (or policies) is
  exhausted, there is **no** fallback to the general range
  (the separation is usually intended, e.g. VoIP).
- Lease duration: the first matching policy with a duration set (scope policies
  before server policies, each by processing order), otherwise the scope.
- At least one condition is mandatory; referenced classes must
  exist, a class in use cannot be deleted, and a
  rename is carried over into the conditions.

## D-026 – Option delivery

- If the client sends option 55, only the requested options are delivered (in
  the order of the list), otherwise all configured ones.
- Options 66/67 (or 150) are additionally mirrored into the BOOTP header fields
  `sname`/`file`/`siaddr`, because many PXE ROMs read only those.
- If the reply exceeds the maximum message size (option 57, otherwise
  576 bytes), options are removed from the end of the request list and a
  warning is logged. Option overload (52) is not used.

## D-027 – MAC filters

Allow list and deny list with wildcards, both disabled by default
(switch in `settings`). Deny wins over allow. If the allow list is active,
only listed MACs are served. Filtered clients are silently ignored for all
message types (logged at info level).

---

## D-028 – Audit log: atomic with the change

- Configuration changes write their audit entry **in the same
  transaction** as the change (`scope.Store.rec`). So there are neither
  changes without an entry nor entries without a change; rejected operations
  leave nothing behind. Details contain `before`/`after`.
- Lease events run as additional ops through the group commit of the
  lease writer – same atomicity, no additional fsync.
- Who: actor + source from the `context` (`audit.WithActor`). The API
  (phase 5) sets the user and client IP there; DHCP events run as
  `dhcp` with source `<interface> [via <giaddr>]`, the expiry worker as
  `system`.
- Lease renewals are **not** audited by default
  (`audit.lease_renewals: false`): with 5000 clients and a 1 h lease time, that would be
  ~10,000 entries per hour. Assignment, release, decline, conflict and
  expiry are logged.
- Retention `audit.retention` (default 90 days, 0 = unlimited), purge
  at startup and every 6 h in batches of 10,000 rows (short write locks).

## D-029 – Scope statistics

Computed from the in-memory lease index (O(leases + reservations), not
O(pool size)): `usable` = range − exclusions; `in_use` = active leases in the
usable range; `reserved` = unused reservations in the usable
range; `declined` = blocked addresses; `available` = the rest; utilization =
(usable − available) / usable. Policy ranges are not reported
separately.

## D-030 – Metrics

Dedicated registry (no global state). Packet counters per message type
(received/sent), drops with a reason (`filtered`, `no_scope`,
`no_address`, `other_server`, `not_ours`, `invalid`, `malformed`, `bootp`,
…), internal errors (`persist`, `send`), processing duration as a histogram,
lease events, commit duration and batch size of the group commit. Scope and
lease gauges are computed only at scrape time (custom collector), so that the
DHCP path does not pay for statistics. HA status follows with phase 6.

## D-031 – Logging target and rotation

Default stderr (container standard; rotation is handled by Docker/journald).
Optional `log.file` with built-in size-based rotation (`max_size_mb`,
`max_backups`) without external dependencies; every log record ends up
complete in a single file.

---

## D-032 – OpenAPI first, code generated

- Specification `internal/api/openapi.yaml` (OpenAPI **3.0.3**, because
  `oapi-codegen` supports 3.1 only partially).
- Go: `oapi-codegen` v2.8 (pinned as a `go tool` in `go.mod`) generates models
  and the chi `ServerInterface` (`internal/api/gen`). The handlers
  implement the interface; the compiler thus enforces that every
  endpoint in the spec exists.
- TypeScript: `openapi-typescript` generates `web/src/api/schema.d.ts`; the
  client is `openapi-fetch` – paths, parameters and bodies are typed.
- Generated code is checked in; CI verifies via `git diff` that it matches the
  spec (`make generate`).

## D-033 – Authentication and sessions

- Passwords: argon2id with RFC 9106 parameters (t=3, m=64 MiB, p=4) in
  PHC format; the parameters are stored in the hash and can be increased later.
  Minimum length 10 characters. Unknown users are checked against a
  dummy hash (no timing oracle).
- Server-side sessions: cookie `leasorix_session` (32 random bytes), stored in the
  DB only as SHA-256. Cookie `HttpOnly`, `SameSite=Strict`, `Secure` as soon as
  TLS is active. Absolute lifetime 12 h, inactivity 1 h (configurable).
  Password changes, deactivation and role changes end sessions.
- CSRF: synchronizer token per session, mandatory header `X-CSRF-Token` on
  all non-GET requests with cookie auth; additionally an Origin check and a
  mandatory `Content-Type: application/json` (forms cannot send
  JSON requests without a preflight).
- API tokens `lxt_…` (only the SHA-256 is stored, shown once), optionally
  with an expiry; role ≤ the user's role, effective role = minimum of the
  token role and the current user role. No CSRF for bearer tokens.
- The last active admin cannot be deleted, deactivated or demoted;
  users cannot delete their own account.
- OIDC is "optional, later" in the plan and not implemented yet.

## D-034 – First start only with a setup token

As long as no user exists, the first admin can only be created with a
one-time setup token that is written to the log at startup
(`setup_token=…`) or preset via `auth.setup_token`. Without this
protection, anyone who reaches the UI before the operator could take over the
instance. Failed attempts count toward the rate limit.

## D-035 – Role model

| Role | Allowed to |
|---|---|
| `readonly` | read everything except user management; own password, own (read-only) tokens |
| `operator` | additionally change the DHCP configuration (scopes, reservations, options, policies, classes, filter entries) and delete/convert leases |
| `admin` | additionally users and `PUT /settings` (including enabling the MAC filter lists) |

The check happens centrally in a middleware based on method + path (one
table, tested), not scattered across the handlers.

## D-036 – Login rate limit

Max. 10 failed attempts per 15 min per client IP **and** per username
(sliding window, in memory), then `429` with `Retry-After`.
Failed attempts are recorded in the audit log (`auth.login_failed`). The lock per
username theoretically allows an account to be locked out
briefly – accepted deliberately (management UI on the internal network, short
window, an admin can keep working via an API token).

## D-037 – TLS on by default (self-signed)

`http.tls.mode: self-signed` is the default: on first start, an
ECDSA P-256 certificate (825 days, SANs localhost/hostname/listen IP) is written to
`<data_dir>/tls` (key 0600). Alternatively `files` (own
certificate) or `off` (e.g. behind a reverse proxy). HSTS only with TLS.
TLS handshake errors (browsers before the certificate is accepted, scanners)
are logged at debug level only.

## D-038 – Strict security headers

CSP without `unsafe-inline` (`script-src 'self'; style-src 'self'`), hence
no inline script: theme detection lives in `/theme.js`. In addition,
`X-Frame-Options: DENY`, `frame-ancestors 'none'`, `nosniff`,
`Referrer-Policy: no-referrer`, COOP/CORP, `Cache-Control: no-store` for the
API. `X-Forwarded-For` is not evaluated (D-008). `/metrics` is reachable without
signing in (Prometheus scrape) – protected via the bind address.

## D-039 – Web UI

- Vite 8 + React 19 + TypeScript + Tailwind 4, TanStack Query, React Router 7.
  **TypeScript 5.9** instead of 7.0: the native 7.x line is new, while 5.9 is proven with
  the entire tooling (conservative choice).
- UI language: German (like the PLAN and the users); i18n can be added
  later.
- Dark/light: class strategy, default from `prefers-color-scheme`, the choice
  is remembered in `localStorage`.
- Embedding via `go:embed` behind the build tag `ui` (`make build`). Without the tag,
  `go build ./...` builds without Node.js and serves a notice page – so
  no build artifact has to be checked in.
- Validation on both sides: `web/src/lib/validate.ts` mirrors the backend rules
  (prefix /8–/30, host bits, range within the subnet, overlap, MAC pattern)
  for immediate feedback; the backend check is authoritative.
- The scope wizard suggests range/router only as placeholders and
  fills them into empty fields only on "Next" (no overwriting while
  typing).

## D-040 – Reservation import (CSV)

All-or-nothing in one transaction; errors are reported with the line number.
Mandatory columns `scope` (name or ID) and `ip`, plus `mac`
and/or `client_id`; delimiter `,` or `;` (detected automatically), a BOM
is tolerated. Each imported row creates an audit entry.

## D-041 – Lease actions via API/UI

"Delete lease" removes the record immediately (address free again, audit
`lease.delete` with the user). "Convert to reservation" takes over the IP,
MAC and hostname (client ID only if no MAC is known).

## D-042 – End-to-end tests

Playwright against the real server in a network namespace (management
veth to the root namespace, DHCP veth to the client namespace); the test starts
`udhcpc` in the client namespace (`test/e2e/run.sh`, `make test-e2e`). Locally,
the preinstalled Chromium is used; in CI, it is installed via
`playwright install`.

## D-043 – HA peer protocol: gRPC with Protobuf

Custom protocol (`internal/ha/pb/ha.proto`) over gRPC, as planned.
Four RPCs: `Heartbeat` (status of both sides, also serves as a
connection check), `PushLeases` (lease changes), `DumpLeases`
(stream of the entire lease table for reconciliation), `PushConfig`
(configuration snapshot). Each server is both server and client;
the connection uses a fixed peer address (`ha.peer.address`). Generated
code is checked in (`make generate`; protoc 3.21 as in Ubuntu 24.04,
`protoc-gen-go`/`protoc-gen-go-grpc` as a `go tool` with a pinned version), and
CI checks that it is up to date. Messages up to 64 MiB, backoff at most one
heartbeat interval, so that the connection comes back quickly after a
partition.

## D-044 – Securing the peer connection

- **mtls** (default): shared CA; the node certificate carries the node ID
  as a DNS name. The client verifies the server against `peer.id`, and the server
  verifies the client the same way via `VerifyConnection` – a valid certificate from the same
  CA for a different node is rejected. TLS ≥ 1.3. Certificates via the
  UI (`POST /ha/certificates`) or CLI (`leasorixd ha certs`): ECDSA P-256,
  CA 10 years, nodes 5 years; the CA key is not stored
  (new nodes = new bundle). Expiry is exposed as a metric, in the UI and as a log warning
  30 days in advance.
- **secret**: TLS 1.3 with throwaway certificates, followed – before the
  first gRPC byte – by a mutual proof
  `HMAC-SHA256(secret, label|role|node ID|TLS exporter)`. The exporter value
  is bound to the TLS session; a man-in-the-middle has two sessions
  and can neither forge nor relay the proof. Secret ≥ 16
  characters, preferably from `shared_secret_file`.

## D-045 – Splitting clients and addresses

- Load balance: the primary is responsible for a client if
  `SHA-256(MAC) mod 100 < load_balance_percent`, otherwise the secondary
  (Hash(MAC) per the plan; deterministic, no offer race).
- Every free address belongs to exactly one server: the share is determined by a
  Weyl sequence `frac(ip · φ⁻¹)` instead of a hash – contiguous ranges are
  thus split almost exactly in the desired ratio, and the assignment
  does not depend on the configuration (exclusions/reservations change
  nothing, no redistribution on config changes).
- Load balance: address share = client share. Hot standby: the standby
  owns `standby_reserve_percent` (default 5%) of the addresses as a reserve,
  the primary owns the rest.
- New addresses are assigned only from the server's own share (also for
  option 50 / INIT-REBOOT). A client's existing active bindings
  may always be renewed, regardless of who owns the address.
- Conservative: a static reserve instead of dynamic pool rebalancing as in
  ISC failover. Consequence: in hot standby, the primary cannot use the
  reserve.

## D-046 – State machine

States per the plan: NORMAL, COMMUNICATION_INTERRUPTED (CI), PARTNER_DOWN
(PD), RECOVER, RECOVER_DONE. "Connected" means: the server's own heartbeats are
answered **and** the partner's heartbeats arrive (both within
`communication_timeout`) and the settings match.

- Always start in RECOVER (no DHCP service), unless the server was in PD.
  If the lease table was current before the restart (NORMAL/CI/RECOVER_DONE)
  and the partner is not reachable after `communication_timeout`, the server goes
  to CI – but only if the downtime was shorter than `auto_partner_down`
  (otherwise the partner might have taken over the entire pool in the meantime).
  A server without a complete table waits for the partner or a
  manual partner-down confirmation.
- RECOVER and RECOVER_DONE do not answer DHCP requests ("reconcile first,
  then assign"). Reconciliation typically takes less than a second.
- Reconciliation: each server pulls the partner's complete table
  (`DumpLeases`) and merges it (D-048). For the reconciliation to count, the
  partner remembers the session ID and epoch: the epoch increases on every
  connection loss, the session on every restart. NORMAL only once
  both sides have reconciled against each other's current state.
- CI → PD only by an operator (`POST /ha/partner-down`, only while disconnected)
  or after `auto_partner_down` (default 0 = off, conservative: a
  network split must not automatically lead to double use of the pool).
- PD remains active after the partner returns until the partner reports RECOVER_DONE.
  If both are in PD (partition + auto partner down), the secondary
  falls back to RECOVER. NORMAL with the partner in PD → RECOVER.
- After a failed synchronous lease update, the server immediately goes
  to CI (new epoch) and reconciles again after the next heartbeat.
- Persisted (`ha_state`, local): state, PD time, "last alive"
  (every 10 s).
- `/readyz` does not depend on the HA state (a waiting partner should not
  be restarted by an orchestrator); the state is exposed in
  `leasorix_ha_state` and in the UI.

## D-047 – Lease times, MCLT and replication

- NORMAL + `replication: sync` (default): a new/renewed binding is
  first replicated to the partner, then stored durably on the local server, then ACKed.
  If the partner does not confirm within `replication_timeout`, the
  lease is stored and assigned shortened to MCLT (and the server goes to
  CI). Replication is batched (like the group commit).
- CI: new and renewed leases at most MCLT (default 1 h).
- PD: full lease time; addresses from the partner's share only from
  the PD time + MCLT. The returning partner reconciles before any
  assignment (D-046), so no limit is necessary.
- `async`: full lease time, replication in the background. Weaker
  guarantee (after a crash, the most recent changes may be missing; ICMP
  conflict detection remains as a safety net) – intended only for performance.
- Releases, declines, conflict blocks and admin deletions are replicated without
  waiting (they act in the safe direction). Each server handles expiry/purge
  itself; these are not replicated.

## D-048 – Merging lease tables

`leases.updated_at` is now in milliseconds, `leases.origin` names the
node of the last change (migration 00004). For an address, the winner is
determined as follows (`lease.Wins`, identical on both sides):

1. Records of the same client: the newer change (then the later end,
   then the higher node ID).
2. Different clients, only one binds the address (active or
   declined and not expired): the binding one.
3. Both bind (a real conflict, e.g. after a split brain): the later
   lease end, then the higher node ID (plan). Conflicts are logged, audited as
   `lease.ha_conflict` and counted.
4. Neither binds: the newer change.

Limitation: admin deletions are replicated but not stored as a
tombstone; a lease deleted during a partition can come back
from the partner during reconciliation (harmless: it expires normally).
The scope of a received record is determined by its address.

## D-049 – Configuration replication (single writer)

- The leader is configured statically (`ha.config_leader`, default `primary`) and
  must be the same on both servers; two leaders = mismatch, no
  connection (both then continue to operate safely in CI).
- A version counter (`config_version`) is incremented by an SQLite trigger on every
  change to a configuration table – so no write path is missed.
  The leader checks it on every heartbeat (and immediately after
  scope changes), creates a snapshot when it has changed (JSON of all
  configuration tables from one read transaction, SHA-256) and sends
  it as long as the partner reports a different hash.
- Replicated: scopes, exclusions, reservations, options,
  definitions, classes, policies, MAC filters, settings as well as users
  and API tokens (signing in is possible on both servers). Local only:
  leases, audit log, sessions and HA state. Token usage does not count as a
  change.
- Applied on the partner in one transaction with foreign keys
  disabled (otherwise replacing the scopes would cascade-delete the
  leases), followed by `foreign_key_check`; leases outside the new scopes
  and sessions of changed/deleted users are removed. Afterwards, the
  partner pulls the leases again (scope IDs may have changed). The
  partner's lease reconciliation starts only once its configuration matches the
  leader's state.
- Read-only partner: the API rejects configuration changes with 409 and
  a pointer to the leader (including password/tokens, since they are replicated);
  lease actions, login/logout and HA actions remain allowed. The UI shows a banner
  and hides edit actions. If the leader is down for an extended period,
  the partner is made the leader via the configuration file.

## D-050 – Split scope and scope bundle

Fallback without replication ("split scope 80/20") as in Windows, via
exclusions: this server keeps the first n% of the dynamic range
and excludes the rest; for the second server, a
scope bundle is created with the local part as an exclusion. Existing
exclusions are taken into account (only gaps are filled in). The
bundle format `leasorix-scope/v1` (scope, exclusions, reservations with
options, scope options, scope policies; references by content instead of IDs)
also serves as a building block for the configuration export in phase 7. Import
is all-or-nothing (on errors, the newly created scope is removed
again). While an HA partnership is active, the wizard is not offered.

## D-051 – Emergency export/import of the lease database

Format `leasorix-leases/v1` (JSON). Import = merge per D-048 (idempotent);
via the API, changed records are replicated to the partner. CLI:
`leasorixd leases export` reads directly from the database (even while the
service is running), `leasorixd leases import` only with the service stopped –
for this, `leasorixd` now holds an exclusive lock (`flock`) on
`<data_dir>/leasorixd.lock`; this also prevents two daemons from running on
the same data directory.

## D-052 – No Raft / no clusters with more than two nodes

The plan mentions a Raft cluster mode as an optional later alternative,
only after the pair mode. It is not implemented: the pair mode covers
Windows failover parity, and a Raft log for leases would tie every assignment to
a quorum (more latency, a third node required). If needed, it would be a
separate project after phase 9.

## D-053 – Superscopes

Separate table `superscope_members` (a scope belongs to at most one
superscope; deletion on either side cascades cleanly, no
foreign-key column in `scopes`, so that the down migration works without
rebuilding the table). The DHCP handler determines the link's scopes as before
(interface addresses or giaddr/118/82.5) and appends the remaining members
of their superscopes (order: the matched scope first, then by
network address). An address of the interface continues to serve as the server identifier.
Like all configuration tables, superscopes are replicated via HA and
are part of export/import (referenced by name).

## D-054 – BOOTP

Optional per the plan, implemented as in Windows: clients with a reservation
always get a BOOTREPLY; dynamic BOOTP from the pool only for
scopes with `bootp`. The lease duration is global, `dhcp4.bootp_lease_time` (default 30
days, as in Windows); the lease is stored and replicated normally and
expires (BOOTP clients do not renew). Replies contain no DHCP options
(53, 51, 54 …); the 64-byte vendor area (RFC 951/1497) is filled by priority
(mask, router, DNS, hostname, domain, …), and options 66/67 end up in
`sname`/`file`/`siaddr`. Policies and HA responsibility apply as for DHCP.

## D-055 – No multicast scopes (MADCAP)

Multicast scopes are optional per the plan. MADCAP (RFC 2730) is a separate
protocol (UDP 2535) with very little adoption; not implemented. The
netsh importer reports such lines as not imported.

## D-056 – Configuration export/import

- Format `leasorix-config/v1`, YAML (default) or JSON with identical
  keys; references by name instead of IDs, so that files can be transferred
  between servers. The scope entries are scope bundles (D-050).
- Users and API tokens are not included (secrets); backup/restore
  covers those.
- Import via a temporary database: copy the current configuration into it
  (`merge`) or start empty (`replace`), apply the document via the normal
  store methods (all validations), then transfer the result into the real database
  in one transaction. This makes the import
  all-or-nothing without implementing every validation twice. One
  audit entry `config.import` instead of thousands of individual entries.
- Afterwards, leases are assigned to the scope whose subnet contains their address
  (IDs may change); this now applies generally, also in the
  lease manager (`ScopesChanged`) and in HA config sync.
- A dry run validates against the current state without making changes. Import only for
  admins; blocked on an HA partner (read-only).

## D-057 – Importer for `netsh dhcp server dump`

The plan mentions netsh or ISC dhcpd.conf as an optional importer; netsh was
chosen (Windows parity is the project's goal, and people switching
typically come from Windows). The converter produces a
`leasorix-config/v1` document that is checked like any import. UTF-16
(PowerShell redirection) is detected. Standard option definitions and the
built-in Windows classes are skipped; option values are validated against
the option type and, on errors, omitted with a warning instead of aborting the
import. Lines that are not imported (class-specific values,
DNS updates, multicast, DHCPv6, server settings without a counterpart)
are reported with their line number or in summary form.

## D-058 – Backup and restore

- Backup = a complete SQLite copy via `VACUUM INTO` over a separate
  connection (consistent, does not block writers). Contains
  password hashes → admins only.
- Restore is logical: the uploaded backup is written to a temporary file,
  checked (SQLite header, Leasorix tables, schema version –
  newer versions are rejected), stripped of triggers/views (a
  crafted file must not execute anything when read), migrated to the current
  schema, and configuration, users, API tokens and leases are taken over in one
  transaction. Audit log and HA state remain local; all
  sessions end. Afterwards, the scope snapshot and lease manager are
  reloaded – no restart needed.
- The plan mentions `leasorix backup/restore`; implemented as
  `leasorixd backup|restore` (the daemon binary owns the database;
  `restore` runs offline with the data directory lock) and `leasorixctl
  backup|restore` via the API.

## D-059 – `leasorixctl`

Separate binary based on the Go client generated from the OpenAPI spec
(`client: true` in the oapi-codegen config). Authentication only via API token
(no sessions/CSRF), configuration via flags or `LEASORIX_URL`,
`LEASORIX_TOKEN`, `LEASORIX_CA`/`LEASORIX_INSECURE`, output as a table
or JSON (`-o json`). Destructive commands (`ha partner-down`, `restore`)
require `--yes`.

## D-060 – Container image

- Multi-stage build: UI with Node, both binaries with Go (static,
  `CGO_ENABLED=0`, `-trimpath`), runtime image
  `gcr.io/distroless/static-debian12:nonroot` (no shell, no
  package manager, approx. 50 MB). Alpine would have been the alternative; distroless
  has the smaller attack surface and is sufficient because nothing in the image needs a
  shell.
- The build stages run on `$BUILDPLATFORM` and compile for
  `$TARGETARCH` – multi-arch (amd64/arm64) without QEMU.
- The health check is a subcommand (`leasorixd healthcheck`, queries
  `/readyz`), because distroless has no `curl`. `leasorixctl` is included in the
  image.
- Base images are build arguments (registry mirror); a proxy CA can be
  passed as an optional BuildKit secret and does not end up in the image.
- Unknown arguments to `leasorixd` are now an error instead of
  silently starting the server (`leasorixd version` used to
  start the server); `version` exists in addition to `--version`.

## D-061 – Privileges via file capabilities, nonroot

Docker does not pass capabilities on to a non-root user (no
ambient capabilities). So that `leasorixd` can bind port 67 as UID 65532 and
use `AF_PACKET`/ICMP, the binary gets the file capabilities
`cap_net_bind_service,cap_net_raw=ep`. They are set by a small
Go helper (`deploy/docker/setcap`, writes `security.capability` directly),
so that the build stage needs neither `libcap` nor package sources. `NET_ADMIN`
is not required. Consequences (verified, documented in DOCKER.md):

- Both capabilities must remain in the container's bounding set; otherwise
  the kernel refuses `execve` – even for one-off commands.
- `no-new-privileges` is compatible: the kernel then limits file capabilities
  to the existing permitted set, which Docker fills from `cap_add`.
- Root in the container is not an alternative: with `cap_drop: [ALL]`, root
  lacks write access to `/data` (UID 65532).

## D-062 – Compose variants and defaults

- Three files instead of profiles: `docker-compose.yml` (host network, the
  simplest working variant), `docker-compose.macvlan.yml` and
  `docker-compose.ha.yml` (macvlan, two containers). A bridge network is
  not offered: without L2 access, broadcast DISCOVERs do not reach the
  container; bridge + relay is not tested and is therefore not
  documented as supported.
- Secure defaults: `cap_drop: [ALL]` + the two required capabilities,
  `read_only` with `tmpfs: /tmp`, `no-new-privileges`, `restart:
  unless-stopped`. In host mode, the UI listens only on
  `127.0.0.1` by default – the management address must be set deliberately.
- The HA compose file creates the mTLS certificates with a one-shot service
  (`ha certs --if-missing`) in a shared volume; both servers wait for it
  via `service_completed_successfully`. Running both containers on one
  host is intended as a test/starter variant; distributing them across two
  hosts is documented.
- The test (`make test-docker`) uses veth pairs and network namespaces as the
  client LAN, never the real network (PLAN rule).

## D-063 – Image publishing

`release.yml` builds with Buildx for `linux/amd64` and `linux/arm64` and
publishes to the GitHub Container Registry using the `GITHUB_TOKEN`
(`ghcr.io/<owner>/leasorix`): tags `v1.2.3` → `1.2.3`, `1.2`, `latest`;
`main` → `edge`; pull requests only build. No Docker Hub (would need a separate
account/secret, rate limits). The CI workflow additionally has a
job that builds the image and runs `make test-docker`.

## D-057 (Addendum, phase 9) – netsh `set dnsconfig`

The netsh importer now imports `set dnsconfig Enable Update Lookup
NonDyn` at server and scope level (the server setting applies to scopes without
their own setting): `Enable=0` → never, `Update=1` or `NonDyn=1` → always, otherwise
"on client request"; `Lookup` → "Remove records when the lease ends".
`NonDyn` (updates also for clients without option 81) has no direct
counterpart in Leasorix; "always" is the closest mapping. Zones and
keys are not included in the dump (warning).

## D-064 – DNS updates: architecture

- Library `github.com/miekg/dns` for RFC 2136 messages and TSIG instead of
  a custom DNS encoding (widely used, stable, supports all
  TSIG algorithms and the DHCID type).
- The DHCP path never waits for DNS: on ACK, the handler only computes the
  decision (name, A/PTR, option 81 in the reply) and hands it to the
  updater (`Bound`, memory only). A background worker sends the updates
  (up to 4 in parallel) with retries (30 s, doubling up to 1 h).
- State per address in `dns_registrations` (desired / actually
  registered, separately for A with DHCID and PTR). This way, pending
  deletions survive a restart, and renames remove the old name
  first. The table is local (not replicated, not in the export).
- Lease end (release, expiry, deletion, scope removed, restore) is detected by a
  periodic reconciliation (every 15 s) against the lease table instead of hooks at
  every point in the lease manager – one mechanism for all cases, including
  leases that expired during downtime. Deletion attempts are given up after 12
  failures (logged).
- Zones and keys live in the scope snapshot; HA config sync and restore
  reload them without a separate mechanism.

## D-065 – DNS updates: names and modes

- Modes as in Windows: never / on client request / always. "On client
  request" follows RFC 4702 (option 81: S=1 → A+PTR, S=0 → PTR only, N=1 →
  nothing); without option 81, there is no registration. "Always" registers A+PTR for
  every client with a name (option 81 or 12) and sets the O bit in the
  reply if this overrides the client's request.
- The name is always the first (sanitized) label of the client name plus the
  scope domain (fallback: option 15). Fully qualified client names in
  other zones are not accepted – a client must not be able to register itself in
  arbitrary zones for which the server has write permission.
- Option 81 is answered in OFFER and ACK (RCODE1/2 = 255, encoding as
  used by the client), even in "never" mode (then with the N bit). As a result, option 81 is
  "managed" and no longer configurable as an option value. If option 12 is missing,
  the lease takes the name from option 81.
- New scopes default to "never" (Windows: "on client request"), so that
  updating to this version or creating a zone does not trigger updates in
  existing scopes. The TTL is automatically one third of the lease duration
  (1 min … 1 day).
- BOOTP clients are not registered.

## D-066 – Name protection with DHCID

Name protection per RFC 4701/4703 (DHCID record with a SHA-256 over the
client identifier and the name; client ID, DUID per RFC 4361 or
hardware address): a name is only created if it is free, or
updated if it carries the client's own DHCID; manually created names and
names of other clients are left untouched. Deletion likewise only with a
matching DHCID; PTR records are only deleted if they still point to the
client's own name. Default **on** (Windows: off) – the safe option;
can be turned off per scope (Leasorix then replaces existing A records). A
conflict is logged, audited (`dns.conflict`) and retried only at the next
renewal of the lease, not in a loop. Because the
DHCID depends only on the client, both HA servers can update the same name.

## D-067 – TSIG keys

- The secret is stored in plain text in the database – this is required for the
  HMAC computation (unlike passwords, which are only stored hashed). The
  API never returns it (write-only), only admins can change it, and the audit entry
  omits the secret.
- Like users, keys are not part of the configuration export (zones
  reference them by name), but they are replicated via HA and included in the
  backup.
- Algorithms: HMAC-SHA256 (default), -SHA384/-512/-224 as well as -SHA1/-MD5
  for older servers; minimum length 16 bytes. Zones without a key are
  possible (unsigned updates), because Windows DNS does not support TSIG;
  GSS-TSIG (Kerberos) is not implemented.

## D-068 – HA fix: re-replicate shortened leases

Found during the phase 9 test run (a sporadically failing unit test). If the
partner does not confirm a synchronous replication in time, the assigning
server shortens the lease to MCLT – but the partner may have stored the long version
anyway. Until now, that version remained there (same change timestamp,
the longer duration wins), and the tables diverged. Now the
shortened version gets a strictly newer timestamp and is re-replicated
(asynchronously, via resync if that fails), so that both servers know the same
duration.

**Addendum (phase 10):** Under load, the error still occurred sporadically.
Cause: after the timeout, the server marks the connection as disrupted
(new epoch); the partner then pulls the table – possibly
before the shortened version has been stored. The subsequent re-replication
was discarded ("not connected, the partner pulls everything anyway"). Now
a server also increments its epoch when it cannot send a change because there
is no connection; the partner pulls the table again, and the
return to NORMAL waits until it has reconciled the current epoch. This
closes the window for all changes made during an interruption, not
just for shortened leases.


## D-069 – DHCPv6: feature set

Phase 10 implements stateful DHCPv6 per RFC 8415: SOLICIT/ADVERTISE,
REQUEST, RENEW, REBIND, RELEASE, DECLINE, CONFIRM, INFORMATION-REQUEST,
Rapid Commit, server preference, IA_NA and – because the plan lists it as
optional and it comes almost for free with the same allocator – IA_PD
(prefix delegation). Relays are supported, including multiple relay hops
(RELAY-FORW/-REPL, Interface-ID, client link-layer address per RFC 6939).
Message encoding with `github.com/insomniacslk/dhcp/dhcpv6` (the same
library as for DHCPv4).

Deliberately **not** implemented (no invented features, effort without a
requirement): Reconfigure (RFC 8415 sec. 18.2.11, requires authentication),
the Server Unicast option (clients must use multicast; direct unicasts
are answered with `UseMulticast`), Leasequery, DNS updates (AAAA/PTR in
`ip6.arpa`), policies/classes for IPv6, lease export/import for DHCPv6 and
importing DHCPv6 scopes from the netsh dump. Leasorix does not send router
advertisements – that remains the router's job.

## D-070 – Separate data model for IPv6

IPv6 gets its own tables (`scopes6`, `exclusions6`, `reservations6`,
`options6`, `leases6`) and packages (`scope6`, `lease6`, `dhcp6`,
`dhcpopt6`, `ip6`) instead of a generalization of the IPv4 model: the
IPv4 path uses `uint32` throughout (bitmaps, range statistics,
policies, superscopes, BOOTP, HA replication), and a rework would have put the
tested core at risk. The concepts stay the same (scope, exclusion,
reservation, options at server/scope/reservation level, snapshot,
audit, config version), as do the UI structure and API style (`/api/v1/v6/…`).

- Addresses and prefixes are stored as canonical text (SQLite
  has no 128-bit integers); range checks run in Go.
- Reservations apply to a DUID (hex, normalized) and optionally an
  IAID (`-1` = any IA) – the DHCPv6 counterpart of the MAC.
- The configuration tables are part of HA config sync, the configuration
  export (`scopes6`, `server_options6`), replace import and backup; the
  bindings are included in the backup.

## D-071 – Address assignment in large ranges

A /64 has 2^64 addresses – bitmaps or a linear search as with IPv4
are not an option. The bindings are held in memory (map by prefix, persisted
before the reply); a new address is chosen deterministically from the DUID and
IAID: ranges of up to 65,536 addresses or prefixes are searched starting from a
client-specific starting point (never-used ones first), larger ones via
64 client-specific hash candidates. As a result, the same client tends to get the
same address even after its binding has expired or after data loss, and addresses
are not predictably sequential. Precedence: reservation →
existing binding or offer → the client's request (IA hint) → previous
binding → search. ADVERTISE holds the address for the client for `offer_timeout`;
expired/released bindings stay reserved for the
previous client for `expired_grace`, and addresses reported via DECLINE are blocked
for `decline_time`.

## D-072 – Server DUID, lifetimes, options

- The server DUID (DUID-LLT of the first configured interface, otherwise
  DUID-LL with a random address) is generated once and stored locally
  (`local_state`, not replicated): it stays stable when hardware is replaced,
  and HA partners have different DUIDs, as RFC 8415 requires.
- Lifetimes in replies are the remaining time of the binding, at most the
  configured values; T1/T2 are 50%/80% of the preferred lifetime.
  Addresses the client should no longer get (scope changed,
  binding gone) are returned in RENEW/REBIND with a lifetime of 0.
- Options are sent according to the client's Option Request; without an
  Option Request, all configured options are sent. Protocol options (IA, status,
  server ID, preference, Rapid Commit, relay options …) are not
  configurable.
- The hostname of a binding comes from option 39 (Client FQDN, first label);
  it is used for display only.

## D-073 – High availability for DHCPv6 without binding replication

The HA protocol (phase 6) replicates IPv4 leases; a second
replication path for IPv6 would be a substantial extension. Instead:

- Clients are split by a hash of the DUID, addresses and
  delegated prefixes by a hash of the address, in the ratio of the
  HA mode. Each server assigns only from its own share – even if the
  partner is unreachable or has been confirmed as down. This way,
  duplicate bindings never occur, even without replication.
- The price: if a server fails, its clients receive a new
  address or prefix from the partner on REBIND. With IPv6's large ranges
  and multiple addresses per interface, this is acceptable; with delegated
  prefixes, the network behind the router changes. Documented in HA.md.
- The configuration (scopes, reservations, options) is replicated
  by the config leader, as with IPv4.

## D-074 – English by default, German in the UI

Supersedes the language point of D-039. Everything in the repository is
English: README, documentation, code, API, CLI, logs, URLs and paths. The web
UI is English by default and can be switched to German.

- Own small translation layer (`web/src/i18n`) instead of a library such as
  i18next: two languages, no plural rules beyond simple variants, no runtime
  loading. Each module declares its texts next to the code with
  `messages({ en, de })`; the compiler checks that the German table has
  exactly the keys of the English one, so a missing translation is a build
  error.
- The language is chosen in the header (and on the sign-in page) and stored
  per browser (`localStorage`, like the color scheme); `<html lang>` follows
  it. Changing the language remounts the app below the providers, so texts
  outside components (tables, labels) are re-evaluated without extra
  plumbing.
- Texts from the server (validation messages, option names of the catalog,
  audit actions) stay English: they are part of the API and the logs.
- Dates and numbers are formatted with the locale of the chosen language.

## D-075 – Ready-made releases: image, binaries, systemd

Leasorix is installed from a release; building on the server is not
needed.

- A version tag `v1.2.3` publishes the multi-arch image (amd64, arm64) as
  `1.2.3`, `1.2` and `latest`, and a GitHub release with the binaries,
  the Compose files and `SHA256SUMS`. The release is only created after the
  image has been pushed, so the pull command in the notes always works.
  Tags with a suffix become pre-releases and do not move `latest`.
- The archives are built by `make dist` (the same target locally and in
  CI): per platform `leasorixd` with the embedded UI, `leasorixctl`, the
  example configuration and a systemd unit. Only Linux amd64/arm64 – the
  same platforms as the image; DHCP raw sockets are Linux-only anyway.
- The Compose files of a release are pinned to its version and have no
  build section, so a downloaded file never falls back to a build that
  cannot work without the sources. In the repository they keep the build
  section and `latest`.
- systemd unit with a static system user `leasorix` instead of
  `DynamicUser=`: HA certificates and keys in `/etc/leasorix` can be given
  to a fixed group, and offline commands (`backup`, `restore`) run as the
  same user, so file ownership stays correct. Hardened like the container:
  only `CAP_NET_BIND_SERVICE` and `CAP_NET_RAW`, `NoNewPrivileges`,
  `ProtectSystem=strict` with `/var/lib/leasorix` as the only writable
  data path.
- Visibility of the image follows GitHub: while the repository or the
  package is private, `docker pull` needs a login (`read:packages`); the
  owner can make the package public independently of the repository
  (INSTALL.md).
