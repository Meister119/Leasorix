# Progress

A short note per completed phase (see PLAN.md). Decisions are recorded in
[DECISIONS.md](DECISIONS.md).

| Phase | Status |
|---|---|
| 1 – Foundation | ✅ done |
| 2 – DHCPv4 core | ✅ done |
| 3 – Options, classes, policies, filters | ✅ done |
| 4 – Audit, statistics, metrics | ✅ done |
| 5 – REST API, auth, web UI | ✅ done |
| 6 – High availability | ✅ done |
| 7 – Convenience & compatibility | ✅ done |
| 8 – Docker & Compose | ✅ done |
| 9 – DNS integration | ✅ done |
| 10 – DHCPv6 (optional) | ✅ done |

---

## Phase 1 – Foundation

**Implemented**
- Go module `github.com/meister119/leasorix`, repo layout as described in
  PLAN.md (packages for later phases as documented placeholders).
- `Makefile` with `build`, `test` (race detector), `lint`, `vuln`, `ci`;
  `.golangci.yml` (v2); GitHub Actions workflow `ci.yml`.
- Bootstrap config: YAML (`--config` or `LEASORIX_CONFIG`) + env overrides
  `LEASORIX_<SECTION>_<KEY>`, strict validation (unknown keys = error).
  Example: `deploy/examples/leasorix.yml`.
- Structured logs via `slog` (JSON or text, configurable level).
- Graceful shutdown on `SIGINT`/`SIGTERM` (`signal.NotifyContext` +
  `errgroup`, HTTP shutdown with timeout, the DB is closed cleanly).
- SQLite store (modernc, WAL, FULL sync, writer/reader pool), embedded
  goose migrations. Tables: `scopes`, `exclusions`, `reservations`,
  `options`, `leases`, `audit_log`, `users`.
- HTTP management server (chi) with `/healthz` (liveness), `/readyz`
  (readiness checks, currently: database) and `/metrics` (dedicated
  Prometheus registry with Go/process collectors and `leasorix_build_info`).

**Acceptance**
- `make build` produces `bin/leasorixd` ✅
- `leasorixd --config x.yml` starts, endpoints respond, `SIGTERM` → exit 0 ✅
- Migrations are idempotent (test `TestMigrateIdempotent` + second start
  without re-running migrations) ✅
- Unit tests green (`make test`), `make lint` without findings ✅

**Open points / notes**
- `govulncheck` runs in CI. In the current development environment the
  network policy blocks `vuln.go.dev`, so it cannot be run locally.

## Phase 2 – DHCPv4 core

**Implemented**
- `internal/scope`: scopes, exclusions, reservations with validation
  (overlap, range within the subnet, exclusion within the range, unique
  MAC/client ID/IP) and an immutable in-memory snapshot for the engine.
- `internal/lease`: lease manager (in-memory index, address selection
  reservation > offer > existing lease > option 50 > free > released >
  expired), offer hold time, release/decline, expiry worker with
  grace period, group-commit writer (ACK only after a durable commit),
  ICMP conflict detection with ARP self-check.
- `internal/dhcp4`: state machine per RFC 2131 (DISCOVER→OFFER, REQUEST in
  SELECTING/INIT-REBOOT/RENEWING/REBINDING → ACK/NAK, DECLINE, RELEASE,
  INFORM), reply addressing per RFC 2131 4.1 (relay, ciaddr, broadcast,
  L2 unicast via AF_PACKET), relay support (giaddr, option 82 echo,
  link selection 82.5, subnet selection 118, server ID override 82.11),
  multiple scopes per server, scope selection by interface IP or giaddr.
- Server: one socket per interface (`SO_BINDTODEVICE`, `IP_PKTINFO`),
  bounded concurrency, automatic rebinding, readiness check `dhcp4`.
- Config section `dhcp4` (interfaces, timings, conflict detection).
- Test infrastructure: `test/netns` (namespaces, veth, `setns`),
  `test/dhcpclient` (Go client simulator for many parallel clients),
  `test/integration` (`make test-integration`), fuzz test `make fuzz`.
- CI: additional job for the netns integration tests.

**Acceptance** (`make test-integration`, all green)
- dhclient: obtains a lease, unicast renew extends the lease, INIT-REBOOT
  after a restart gets the same address, release ✅
- udhcpc: lease with mask/lease time/server ID, hostname + client ID
  stored, release ✅
- Go client: exclusions, reservation preferred, decline blocks the address,
  release, NAK for a foreign address and the wrong network, INFORM, leases
  survive a restart ✅
- Conflict detection: an address in use is skipped and marked as `declined`
  ✅
- Relay (dnsmasq `--dhcp-relay`): a client in the remote subnet gets a lease
  from the matching scope, server ID = the address facing the relay ✅
- 1000 parallel clients: 1000 unique addresses, 1000 persisted
  active leases, no duplicates (≈ 1.5–3 s incl. conflict ping) ✅
- Unit tests (race detector) and lint green; fuzzing ~1.2 million
  executions without findings.

**Notes**
- DHCP options other than subnet mask, lease time, T1/T2 and server ID
  follow in phase 3 (router, DNS, … from the `options` table).

## Phase 3 – Options, classes, policies, filters

**Implemented**
- `internal/dhcpopt`: option catalog (including 3, 6, 12, 15, 42, 43, 60,
  66, 67, 119, 121, 150, 249, 252) and typed values (IP, IP list, string,
  uint8/16/32, int32, bool, hex, domain list RFC 1035, routes RFC 3442) with
  validation, canonical text form and wire encoding. Custom option
  definitions for additional codes.
- Migration 00002: `classes`, `policies`, `policy_conditions`,
  `policy_ranges`, `option_definitions`, `mac_filters`, `settings`;
  `options` extended with a policy owner (down migration tested).
- `internal/scope`: CRUD for option values (server/scope/policy/
  reservation), classes, policies (incl. conditions, ranges,
  lease duration), MAC filters and filter toggles; the snapshot contains
  everything pre-encoded. Evaluation: wildcard matching, policy evaluation
  (AND/OR, `eq`/`ne`, option 82, user class per RFC 3004), range
  calculation, option inheritance, filter decision.
- Lease manager: allocation via `Candidate` (policy ranges, the policy's
  lease duration), cursor per range.
- DHCP handler: MAC filter before any processing, policies per scope,
  options according to the Parameter Request List, PXE header fields, size
  limit per option 57; options for INFORM as well.

**Acceptance**
- Table-driven tests of option resolution (`TestOptionResolution`:
  server < scope < server policy < scope policy < reservation, processing
  order, OR, `ne`, disabled policy, option 82 via hex) ✅
- A PXE client gets boot options: udhcpc with `-V PXEClient…` receives
  `tftp`, `bootfile`, `siaddr`, `boot_file`; a PXE-style Go client (options
  60/93/97, PRL) receives 66/67 + header fields; a regular client does not ✅
- A blocked MAC gets no reply, other clients are served ✅
- Standard options (router, DNS, domain, NTP) reach udhcpc ✅
- Unit tests, integration tests and lint green.

## Phase 4 – Audit, statistics, metrics

**Implemented**
- `internal/audit`: entries (time, actor, source, action, object, details
  as JSON), actor via `context`, queries with filters (time range, actor,
  action prefix, object) and keyset pagination, purge.
- All config mutations (scopes, exclusions, reservations, options,
  option definitions, classes, policies, filters, filter toggles) atomically
  write an audit entry with before/after.
- Lease events (assign, renew (optional), release, decline, conflict,
  expire) in the audit log via the group commit.
- Scope statistics (`lease.Manager.Stats`): total, excluded, usable,
  in use, reserved, blocked, free, utilization %, leases per state.
- Prometheus: `leasorix_dhcp4_packets_received_total{type}`,
  `…_packets_sent_total{type}`, `…_packets_dropped_total{reason}`,
  `…_errors_total{kind}`, `…_request_duration_seconds{type}`,
  `leasorix_lease_events_total{event}`, `leasorix_lease_commit_duration_seconds`,
  `leasorix_lease_commit_batch_size`, `leasorix_scope_addresses{kind}`,
  `leasorix_scope_utilization_ratio`, `leasorix_leases{state}`.
- Retention: `audit.retention` + purge job; optional log file with
  size-based rotation (`log.file`, `max_size_mb`, `max_backups`).

**Acceptance**
- Audit entries are created for every mutation: `TestEveryMutationIsAudited`
  checks all 22 mutation types (exactly one entry, actor/source from the
  context), rejected operations write nothing ✅
- Metrics appear in `/metrics`: app test (scope gauges) and
  integration test after real DHCP traffic (packet counters, latency,
  lease events, utilization 0.1 with 1/10 in use) ✅
- Lease events end up in the audit log with actor `dhcp` and source `s0` ✅

**Notes**
- HA status as a metric comes with phase 6 (HA does not exist yet).
- Audit queries via API/UI follow in phase 5.

## Phase 5 – REST API, auth, web UI

**Implemented**
- OpenAPI spec (`internal/api/openapi.yaml`, ~50 operations) → generated
  Go types + chi server interface and TS types; CI checks they are up to
  date.
- `internal/auth`: users (argon2id), server-side sessions with
  idle/absolute timeout, CSRF token, API tokens capped by role,
  setup token for the first start, login rate limit, protection of the last
  admin, audit of all auth events. Migration 00003 (`sessions`,
  `api_tokens`).
- REST API `/api/v1` for setup/login/logout/me/password, tokens, scopes
  (+statistics, exclusions), reservations (+CSV import), options
  (server/scope/policy/reservation) and definitions, classes, policies,
  MAC filters, leases (search/filter/paging, delete, convert to a
  reservation), statistics, audit (filters, keyset paging), users,
  settings, system info, HA status (complete from phase 6 on).
- Middleware: authentication (cookie/bearer), central RBAC table,
  CSRF, origin and content type checks, body limits, security headers/CSP.
- HTTPS: self-signed (default), custom files, or off.
- Web UI (German, light/dark, responsive): Sign in & Initial setup,
  Dashboard (utilization, packets/s, HA, recent events), Scopes (list,
  wizard, detail view with Overview/Exclusions/Options/Reservations/
  Policies/Leases), Reservations (inline creation, editing with
  options, CSV import), Leases (search/filter/paging, convert, delete),
  Policies & filters (classes, filters, policy editor with conditions,
  ranges, lease duration, options), High availability, Audit log, Users &
  settings (account, API tokens, users, server options, option
  definitions, system).
- Embedded in the binary (`make build`, tag `ui`); `make build-noui`
  without Node.js.

**Acceptance**
- Complete workflow through the UI only (Playwright, `make test-e2e`):
  Initial setup → scope via the wizard (incl. frontend validation) →
  a real `udhcpc` in the client namespace gets a lease → lease visible in
  the UI → converted to a reservation → in the audit log (admin / dhcp) ✅
- More E2E tests: wrong password, a Read-only user sees no editing
  actions, dark mode persists — 5/5 green ✅
- Go API tests: setup, login + rate limit, logout, CSRF/origin/content type,
  roles + API tokens, validation errors with fields, CRUD for all objects,
  leases, CSV import, statistics, headers, RBAC table ✅
- Frontend unit tests (Vitest) for the validation, typecheck ✅

## Phase 6 – High availability

**Implemented**
- `internal/ha`: partnership of two servers in load balance mode
  (hash(MAC), configurable percentage, default 50/50) or hot standby mode
  (reserve default 5%); fixed split of the free addresses; state machine
  NORMAL / COMMUNICATION_INTERRUPTED / PARTNER_DOWN / RECOVER / RECOVER_DONE
  with heartbeats, timeouts, MCLT (default 1 h), automatic partner down
  (optional), manual partner down confirmation and split-brain protection
  (reconcile first, then allocate).
- Peer protocol over gRPC/Protobuf, secured with mTLS (certificates via
  the UI or `leasorixd ha certs`) or TLS + a channel-bound shared secret.
- Lease replication: synchronous before the ACK (default) or asynchronous,
  batched; reconciliation of the complete table after reconnecting, with
  deterministic conflict resolution (newer change wins; for true
  conflicts the later end, then the node ID). Migration 00004.
- Configuration replication following the single-writer principle:
  trigger-maintained version number, snapshot from the config leader,
  partner is read-only (API 409, banner in the UI); users/API tokens are
  replicated as well.
- DHCP handler: responsibility per state (directed renewals are always
  answered); lease manager: own pool share, MCLT limit,
  shortening when the partner has not acknowledged.
- API/UI: HA status (state, partner, mode, configuration, errors,
  certificate expiry), confirm partner down, certificate generator,
  emergency export/import of the lease DB (also via CLI), split scope
  wizard (80/20) with scope export/import (`leasorix-scope/v1`).
- Metrics `leasorix_ha_*`, audit entries for state changes,
  partner down, config takeover and conflicts.
- `flock` on the data directory (no double start, safe offline import).
- Docs: [HA.md](HA.md) (HA guide), DECISIONS D-043…D-052, ARCHITECTURE.

**Acceptance** (`make test-ha`, two real `leasorixd` in network namespaces,
mTLS with certificates generated via CLI)
- Normal operation: 5000 clients, both servers serve (≈ 2500/2500), no
  duplicate address, both databases identical; ≈ 300 leases/s incl.
  ICMP conflict check and synchronous replication ✅
- Primary killed with SIGKILL: the standby already has all leases at ACK
  time (synchronous), renews all 40 clients without interruption
  (lease ≤ MCLT), serves new clients from the reserve, enters PARTNER_DOWN
  after the automatic timeout; the returning primary reconciles, both
  NORMAL, tables identical ✅
- Network partition between the servers: both allocate only from their
  share (lease ≤ MCLT), after healing consistent databases without
  duplicate addresses ✅
- A configuration change on the leader is visible on the partner after
  ≈ 0.2 s; the partner rejects changes; the replicated admin can sign in on
  the partner ✅
- Unit tests: state machine with simulated time (9 scenarios),
  address split, two nodes in one process (replication, partition, takeover
  after MCLT, wrong secret, mTLS incl. a foreign node certificate),
  config snapshot round trip, merge rules, API (read-only, partner down,
  certificates, export/import, split); Playwright E2E 6/6 ✅

**Open points / notes**
- No Raft/cluster mode for more than two nodes (optional per the plan,
  D-052).
- The HA Compose variant was implemented in phase 8 (`deploy/docker-compose.ha.yml`).

## Phase 7 – Convenience & compatibility

**Implemented**
- **Superscopes** (multinet): migration 00005, CRUD in API/UI, assignment
  in the scope, the DHCP handler includes all members of the link;
  part of HA replication and export/import.
- **BOOTP** (optional): replies for reserved BOOTP clients, dynamic
  BOOTP per scope, separate lease duration, 64-byte vendor area, boot
  server/boot file in the header.
- **Configuration export/import** (YAML/JSON, `leasorix-config/v1`) in
  the UI, API and `leasorixctl`: Merge/Replace modes, dry run, full
  validation against a temporary database, all-or-nothing, leases
  are kept on Replace (matched by address).
- **Importer for `netsh dhcp server dump`** (Windows): scopes, ranges,
  exclusions, reservations, option values, option definitions, classes,
  superscopes, MAC filters; warnings for lines that were not imported;
  UTF-16.
- **Backup/restore** via UI, API, `leasorixd backup|restore` and
  `leasorixctl backup|restore`: consistent database copy while running,
  logical restore without a restart, incl. migration of older backups.
- **`leasorixctl`**: REST CLI (generated client) for status, scopes,
  leases, HA, configuration, backup; table or JSON output.
- Multicast scopes (MADCAP) deliberately not implemented (optional, D-055).
- Docs: [ADMIN.md](ADMIN.md) (admin guide), DECISIONS D-053…D-059.

**Tests**
- Unit: superscope store, snapshot, handler (superscope expansion,
  BOOTP replies), bundle/export-import round trip (YAML and JSON
  identical), merge conflict without partial changes, dry run, Replace
  keeping leases, netsh import (UTF-8 and UTF-16), backup/restore incl.
  foreign/newer files, API (roles, content types, session end after
  restore), `leasorixctl` against a real in-process server.
- Integration (netns, real packets): multinet via a superscope (a third
  client gets an address from the second subnet with that subnet's router),
  BOOTP with reservation and lease duration, no dynamic BOOTP unless
  enabled ✅
- Playwright E2E 7/7 (new: create/assign a superscope, YAML export,
  netsh dry run with warnings, backup download) ✅

## Phase 8 – Docker & Compose

**Implemented**
- **Image** (`deploy/Dockerfile`): multi-stage Node → Go → distroless
  `static-debian12:nonroot`, about 50 MB, runs as UID 65532; file
  capabilities `NET_BIND_SERVICE` + `NET_RAW` (no `NET_ADMIN`), set
  by a dedicated Go helper; `HEALTHCHECK` via `leasorixd healthcheck`;
  `/data` volume, config in the image + `LEASORIX_*` env or bind mount;
  `leasorixctl` included; cross-build for amd64/arm64 without emulation;
  mirror build arguments and an optional proxy CA secret.
- **Compose:** `docker-compose.yml` (host network),
  `docker-compose.macvlan.yml`, `docker-compose.ha.yml` (two containers on
  the macvlan network, a one-shot service generates the mTLS certificates).
  Hardened with `cap_drop: [ALL]`, read-only root FS, `no-new-privileges`,
  `restart: unless-stopped`.
- `make docker` (tags `<version>` and `latest`), `make test-docker`.
- **CI:** job `docker` (build the image + Compose tests);
  `release.yml` publishes multi-arch images to the GitHub Container
  Registry (version tag, `latest`, `edge`).
- `leasorixd version`; unknown arguments now abort with an error.
- Docs: [DOCKER.md](DOCKER.md) (variants, why no bridge network,
  permissions, macvlan shim/VLAN/VM notes, HA on one or two hosts,
  operations, build, troubleshooting), README quick start with Compose,
  DECISIONS D-060…D-063.

**Tests** (`sudo make test-docker`, isolated veth/netns networks)
- Host network: `docker compose up -d` → `healthy`, UID 65532, read-only,
  `CapEff` = exactly the two capabilities, UI reachable, setup + scope via
  the API, a real DHCP client (`udhcpc`) gets an address, backup via
  `docker exec`, `leasorixctl` in the image ✅
- macvlan: a client gets an address from the container with its own IP ✅
- HA Compose with two containers: both `NORMAL`, admin + scope replicated
  from the leader to the partner, partner read-only, clients distributed
  across both servers, lease visible on the partner; after
  `docker kill dhcp-a` the client of `dhcp-a` gets its address from the
  partner ✅
- arm64 cross-build verified (AArch64 binary, capabilities in the layer);
  offline restore in the read-only container verified ✅

**Open points / notes**
- Publishing to GHCR is only implemented as a workflow; a push was not
  possible from the development environment and happens with the first
  version tag on GitHub.
- Bridge network + DHCP relay is neither tested nor supported (D-062).

## Phase 9 – DNS integration

**Implemented**
- **RFC 2136 updates** (A, PTR, DHCID) with **TSIG** (HMAC-SHA256 and
  others, `miekg/dns`), zones with a primary server and an optional key,
  longest matching zone per name; asynchronous updater with retries and
  persistent registration state (migration 00006).
- **Per scope:** never / on client request (option 81 per RFC 4702)
  / always; domain (fallback: option 15), TTL, remove records when the
  lease ends, name protection. Option 81 is answered in OFFER/ACK
  (S/O/N bits).
- **Name protection** with DHCID (RFC 4701/4703): foreign and manually
  created names are never overwritten; conflicts show up in the log, audit
  (`dns.conflict`), status and metrics.
- **Cleanup** on release, expiry and deletion (also after a restart) via a
  periodic reconciliation with the lease table.
- API (`/dns/keys`, `/dns/zones`, `/dns/registrations`, `dns` on the
  scope), web UI (*DNS updates* page with a key generator and snippets for
  BIND/PowerDNS, registration status; scope tab *DNS*), `leasorixctl dns
  zones|registrations`, configuration export/import (zones, scope
  settings; deliberately no keys), netsh `set dnsconfig`, HA sync
  and backup of keys/zones. Metrics `leasorix_dns_*`.
- Fix from phase 6: shortened leases (unacknowledged synchronous
  replication) are replicated afterwards (D-068).
- Docs: [ADMIN.md](ADMIN.md#11-dns-updates), DECISIONS D-064…D-068.

**Tests**
- Unit: option 81 parser/reply (incl. fuzzing), decision matrix,
  DHCID against the examples from RFC 4701, TTL, updater with simulated DNS
  (registration, renewal without traffic, rename, release, expiry,
  conflict without an endless loop, backoff, restart with cleanup, keeping
  records, missing zone), handler (option 81 in OFFER/ACK), API (roles,
  secret never returned, validation, read-only partner), config round trip
  with zones, missing key, netsh mapping, `leasorixctl`.
- Integration (`make test-dns`, part of `make test-integration`): BIND 9.20
  and PowerDNS 4.9 via Compose (`test/dns`) on a macvlan network attached
  to the server namespace; real DHCP clients register with TSIG
  (option 81 or hostname only), A/PTR/DHCID are verified, name protection
  against a manual record and a foreign client, removal after release and
  after expiry – against both servers ✅
- Playwright E2E 8/8 (new: key with snippet, zone, scope setting,
  registration of a real client) ✅; all 17 integration tests ✅

**Open points / notes**
- No GSS-TSIG (Windows DNS with "secure only" updates); Windows DNS only
  with unsigned updates (D-067).
- No AAAA records (a candidate with DHCPv6, phase 10).

## Phase 10 – DHCPv6 (optional)

**Implemented**
- **Stateful DHCPv6 per RFC 8415:** SOLICIT/ADVERTISE, REQUEST, RENEW,
  REBIND, RELEASE, DECLINE, CONFIRM, INFORMATION-REQUEST; Rapid Commit,
  server preference, server ID rules, `UseMulticast` for unicast,
  status codes (NoAddrsAvail, NoPrefixAvail, NoBinding, NotOnLink).
- **IA_NA** (addresses) and **IA_PD** (prefix delegation for routers) with
  deterministic allocation even in large ranges (a whole /64, D-071),
  client hints, offer hold time, grace time for the previous client,
  blocking on DECLINE, expiry; persistent bindings (`leases6`).
- **Scopes** with prefix, address range, lifetimes, delegation pool,
  preference, Rapid Commit; exclusions; **reservations by DUID**
  (optionally IAID); options at server/scope/reservation level
  (catalog: DNS 23/24, SIP, NIS, SNTP, NTP 56, boot URL 59, AFTR 64 …,
  custom codes with a type). Separate data model (migration 00007, D-070).
- **Server:** one socket per interface (`[::]:547`, `ff02::1:2`), relays
  including multi-hop (link-address, interface ID, RFC 6939 client MAC),
  persistent server DUID, MAC filter for clients with a known MAC, metrics
  `leasorix_dhcp6_*`, audit (`scope6.*`, `exclusion6.*`, `reservation6.*`,
  `option6.*`, `lease6.delete`; binding events optional, as for IPv4).
- **HA:** configuration is replicated (config sync, read-only partner);
  bindings are not – clients and addresses are split by hash, each
  server allocates only from its share (D-073).
- REST API `/api/v1/v6/…` (scopes, exclusions, reservations, options,
  bindings, statistics), web UI page *IPv6 (DHCPv6)* (scopes with a detail
  view, reservations, options, leases, server options),
  `leasorixctl v6 scopes|leases`, configuration export/import
  (`scopes6`, `server_options6`), backup/restore incl. bindings, bootstrap
  section `dhcp6`, `make fuzz` extended with `FuzzHandle6`.
- HA fix (IPv4): changes that could not be sent during a short interruption
  were lost if the partner had already completed its reconciliation
  (sporadically failing HA test under load). Now every unsent change forces
  another reconciliation (addendum to D-068, unit test of the state
  machine).
- Docs: [ADMIN.md](ADMIN.md#12-dhcpv6), [HA.md](HA.md#dhcpv6),
  ARCHITECTURE, DOCKER, DECISIONS D-069…D-073.

**Tests**
- Unit: 128-bit arithmetic and bucket distribution, scope validation,
  reservations/options/snapshot, allocator (stability per client,
  exhaustion, exclusions, reservations, hints, PD, /64 range,
  HA share, expiry, scope removal), handler (complete exchange,
  renew/rebind, release, Rapid Commit, decline, information-request, relay,
  filter, exhausted scope) and fuzzing of the packet path, complementary
  HA split, API (roles, read-only partner, validation), config
  round trip, bootstrap config, `leasorixctl`.
- Playwright E2E 9/9 (new: IPv6 scope with prefix delegation,
  server-side validation, option, reservation, export) ✅
- Integration: `test/integration/dhcp6_test.go` (part of
  `make test-integration`) with ISC `dhclient -6` (address, prefix
  delegation, release), busybox `udhcpc6`, Go client (Solicit/Request/
  Renew/Information-Request) and a relay (RELAY-FORW with interface ID).
- All unit tests (race detector), golangci-lint (also with build tag
  `integration`) and the 17 existing integration tests incl. HA and DNS
  (BIND/PowerDNS) ✅; the DHCPv6 integration test is skipped here (see
  below).
- CI workflow: `busybox` installed for `udhcpc6`, manual start via
  `workflow_dispatch` possible. First CI run of the integration tests on
  GitHub runners: Ubuntu passes `rp_filter=2` on to new namespaces (which
  dropped broadcast replies to the Go test client) and uses AppArmor to
  restrict `/sbin/dhclient` to system scripts – test namespaces disable
  `rp_filter`, dhclient runs from a copy. Reproduced and verified locally
  with `rp_filter=2`. In the same run the Go client and relay tests for
  DHCPv6 passed for real for the first time.

**Open points / notes**
- The kernel of this development environment is booted with
  `ipv6.disable=1`; the DHCPv6 integration test skips itself here. In CI
  (GitHub runner, run of 16.10.2026 on `c0ff4bc`) it passed fully green –
  dhclient -6 with prefix delegation and release, udhcpc6, Go client,
  relay – as did all other jobs (lint, unit, govulncheck, npm audit,
  integration, E2E, Docker).
- Not implemented (D-069): DNS updates for DHCPv6 (AAAA), policies/classes
  for IPv6, Reconfigure, server unicast, Leasequery, DHCPv6 lease export,
  netsh import of DHCPv6 scopes.
- HA: when a server fails, its DHCPv6 clients change their address
  (D-073).

## Addendum – English by default, German UI

**Implemented**
- README and all documents translated to English; code, API, CLI, logs,
  URLs and paths were already English.
- Web UI: English by default, German selectable in the header and on the
  sign-in page (stored per browser). Translation layer `web/src/i18n` with
  per-module text tables checked by the compiler (D-074).
- README with feature overview, screenshots (`make screenshots`, demo data
  in `test/screenshots/demo.yaml`) and an architecture diagram.


## Addendum – Ready-made releases

**Implemented**
- `.github/workflows/release.yml`: on a version tag the image (amd64,
  arm64; `1.2.3`, `1.2`, `latest`) and a GitHub release with the binaries,
  Compose files pinned to the version and `SHA256SUMS`; release notes are
  generated from the merged pull requests (D-075). Pull requests that touch
  the release build everything without publishing.
- `make dist`: release archives `leasorix_<version>_linux_<arch>.tar.gz`
  with `leasorixd` (UI embedded), `leasorixctl`, the example configuration
  and the systemd unit `deploy/systemd/leasorixd.service`.
- `docs/INSTALL.md`: installation from a release with Docker (no checkout,
  settings in `.env`) or as binaries with systemd, updates, image tags,
  access to a private repository, publishing a release. README and
  DOCKER.md point to it.

**Tested**
- `make dist` for amd64 and arm64, checksums, Compose files of the release
  validated with `docker compose config`; release step simulated.
- systemd unit in a container with systemd as PID 1: the installation steps
  of INSTALL.md, service runs as `leasorix` with exactly the two
  capabilities, `/readyz` ok, a DHCP client (udhcpc over a veth pair) gets a
  lease, backup and restore as the service user.
