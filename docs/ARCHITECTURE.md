# Architecture

> Living document — extended with each phase.

## Overview

```
                +---------------------------------------------+
                |                 leasorixd                    |
                |                                              |
  UDP 67  ----> |  dhcp4 (Phase 2)  --->  lease / scope        |
                |                              |               |
  HTTP   ----> |  api (chi): UI, REST,         v               |
  (mgmt)        |  /healthz /readyz /metrics  store (SQLite)   |
                |                              ^               |
  gRPC   <---> |  ha: failover, replication ---+               |
  (peer)        +---------------------------------------------+
```

One process, one static binary. Components run as goroutines under a
shared `errgroup`; if a component fails or `SIGTERM` arrives, the context
is canceled and all components shut down in an orderly fashion.

## Packages

| Package | Purpose |
|---|---|
| `cmd/leasorixd` | Daemon: flags, config, logger, signal handling; offline commands (`ha certs`, `leases`, `backup`, `restore`, `healthcheck`) |
| `cmd/leasorixctl` | REST CLI based on the generated API client |
| `internal/app` | Wiring of all components, lifecycle |
| `internal/config` | Bootstrap configuration (YAML + env) |
| `internal/logging` | `slog` logger (JSON/text) |
| `internal/store` | SQLite access, migrations, transactions |
| `internal/health` | Liveness/readiness with registrable checks |
| `internal/metrics` | Prometheus registry |
| `internal/api` | HTTP management server |
| `internal/dhcp4` | DHCPv4 engine (Phase 2) |
| `internal/scope` | Scopes, pools, exclusions, reservations, policies |
| `internal/lease` | Lease store, allocation, conflict detection, expiry |
| `internal/auth` | Users, roles, sessions, API tokens, setup |
| `internal/audit` | Audit log (write within the transaction, query, purge) |
| `internal/dhcpopt` | Option catalog and typed values |
| `internal/api/gen` | Code generated from OpenAPI (server interface, models, client) |
| `internal/configio` | Configuration export/import (`leasorix-config/v1`), netsh importer |
| `internal/backup` | Backup (VACUUM INTO) and logical restore |
| `web` | React UI, embedded via `go:embed` |
| `internal/ha` | HA partnership: state machine, pool split, gRPC peer protocol (`pb`), lease/config replication, certificates |
| `internal/dnsupdate` | DNS updates: option 81 (RFC 4702), DHCID (RFC 4701), RFC 2136 client with TSIG, updater worker |
| `internal/dhcp6` | DHCPv6 engine (RFC 8415): handler, per-interface sockets, server DUID |
| `internal/scope6` | IPv6 scopes, exclusions, reservations (DUID/IAID), options, snapshot |
| `internal/lease6` | DHCPv6 bindings (IA_NA, IA_PD): allocation, expiry, persistence |
| `internal/dhcpopt6` | DHCPv6 option catalog and typed values |
| `internal/ip6` | 128-bit arithmetic for ranges and prefixes |

## Configuration

Two levels (see DECISIONS D-003):

1. **Bootstrap** — YAML file + `LEASORIX_*` env: listeners, data directory,
   logging, interfaces, HA peers.
2. **DHCP configuration** — in the database, managed via API/UI.

## DHCPv4 path

```
socket(s0) --> Server.readLoop --> worker (max N) --> Handler.Handle
                                                        |  per-client lock
                                                        v
            scope.Store.Snapshot()  <-- link selection (giaddr/82.5/118/interface IP,
                                                        + superscope members)
                                                        |  ha.Node.Accept (who responds)
                                                        |
                                    lease.Manager.Offer/Request/Release/Decline
                                         |  (mutex, in-memory index)
                                         |-- ICMPProber (conflict ping, outside the lock)
                                         `-- DBStore.Apply --> group-commit writer --> SQLite
                                                        |
Response (destination per RFC 2131 4.1) <---------------+
   |-- UDP to giaddr:67 / ciaddr:68 / 255.255.255.255:68
   `-- L2 unicast to chaddr (AF_PACKET), fallback broadcast
```

- **Scopes** are held as an immutable snapshot (`atomic.Pointer`); every
  change via `scope.Store` rebuilds it and notifies listeners (e.g., the
  lease manager when scopes are deleted).
- **Leases**: the lease manager is the authority. Decisions are made in
  memory, persistence goes through the group-commit writer; an ACK is sent
  only after a successful commit.
- **BOOTP**: requests without a DHCP message type go through the same path
  (offer + request in one step) and receive a BOOTREPLY.
- **Expiry worker**: every `expiry_interval` → `active`→`expired`,
  deletion after the grace period or decline time, cleanup of expired offers.

## Management API and web UI

```
Browser ──HTTPS──> api.Server (chi)
                     ├─ /healthz /readyz /metrics
                     ├─ /api/v1/*  ── authenticate (Cookie | Bearer)
                     │                └─ authorize (RBAC table, CSRF, Origin, Content-Type)
                     │                   └─ gen.ServerInterface (generated from openapi.yaml)
                     │                      └─ handlers → scope.Store / lease.Manager / auth.Service / audit.Log
                     └─ /*  ── embedded SPA (web/dist, build tag ui), fallback index.html
```

- **Spec first:** `internal/api/openapi.yaml` is the source for the Go types
  and router (`internal/api/gen`) as well as for the UI's TypeScript types
  (`web/src/api/schema.d.ts`).
- **Audit actor:** the authentication middleware puts the user and client
  IP into the context; all domain operations write it into their audit
  entries.
- **UI:** React + TanStack Query; every page reads via the typed client,
  mutations invalidate the affected queries. Roles control which actions
  are shown — enforcement happens in the backend.

## Persistence

SQLite in WAL mode at `<data_dir>/leasorix.db`. Writes go through a pool
with exactly one connection (`BEGIN IMMEDIATE`), reads through a separate
read-only pool. The schema is updated at startup via embedded goose
migrations (`internal/store/migrations`).

Migrations: 00001 base schema, 00002 policies/options/filters, 00003
sessions/API tokens, 00004 HA (lease timestamps in ms + `origin`,
`ha_state`, trigger-maintained `config_version` counter), 00005 superscopes
and BOOTP.

`store.ReplaceTables` replaces a group of tables transactionally with
foreign keys disabled, followed by a `foreign_key_check`; it is the common
basis for HA config sync, configuration import, and restore. Afterwards,
`store.RemapLeases` assigns each lease to the scope whose subnet contains
its address (IDs may change). A `flock` on `<data_dir>/leasorixd.lock`
prevents parallel processes on the same database.

## High availability

Details and operation: [HA.md](HA.md); decisions D-043 to D-052.

```
  Server A (primary, config leader)            Server B (secondary)
  +--------------------------------+   gRPC   +--------------------------------+
  | dhcp4 --Accept()--> ha.Node     |  mTLS /  | ha.Node <--Accept()-- dhcp4     |
  |   |                  |  ^       |  Secret  |  ^   |                  |      |
  |   v                  |  |       | <------> |  |   |                  v      |
  | lease.Manager <------+  |       | Heartbeat|  |   +--> lease.Manager        |
  |   CanAllocate/LeaseLimit|       | PushLeases  |       ApplyRemote          |
  |   Replicate ----------->+       | DumpLeases  |                            |
  | scope.Store  --config_version-->| PushConfig -+-> applyConfig -> scope.Store|
  +--------------------------------+          +--------------------------------+
```

- **Lock-free hot path:** `ha.Node` publishes the state and takeover time
  atomically; `Accept` (who responds), `CanAllocate` (own pool share), and
  `LeaseLimit` (MCLT) only read this snapshot.
- **Lease replication:** the lease manager adds changes to an outbox; a
  sender batches them into `PushLeases`. With `sync`, the request waits for
  the acknowledgment before it stores locally and the ACK goes out.
  Received changes go through `lease.Wins` (deterministic merging), as does
  reconciliation via `DumpLeases`.
- **State machine** (`internal/ha/machine.go`) is purely event-driven and
  tested with simulated time; `node.go` connects it to gRPC, persistence
  (`ha_state`), audit, and metrics.
- **Configuration:** triggers increment `config_version`; on a change, the
  leader sends a snapshot of all configuration tables, and the partner
  replaces its tables in one transaction and reloads the scope snapshot.
  The partner's API rejects configuration changes.

## Deployment (containers)

Details: [DOCKER.md](DOCKER.md); decisions D-060 to D-063.

```
 deploy/Dockerfile
   ui     (Node, $BUILDPLATFORM)  npm ci + vite build ------------+
   build  (Go,   $BUILDPLATFORM)  go build -tags ui (cross) <-----+
                                  setcap helper: file capabilities
   final  distroless/static:nonroot  /usr/local/bin/{leasorixd,leasorixctl}
                                     /etc/leasorix/leasorix.yml, VOLUME /data
                                     HEALTHCHECK leasorixd healthcheck

 Compose:  host  ── network_mode: host, DHCP on host interfaces
           macvlan ─ own MAC/IP on the LAN (parent interface/VLAN)
           ha    ── certs (one-shot service) -> dhcp-a + dhcp-b on the macvlan network
```

- One container = one server process; configuration via the file in the
  image plus `LEASORIX_*` variables, state exclusively in the `/data`
  volume.
- Runs as UID 65532 with a read-only root FS; the only privileges are the
  binary's file capabilities `NET_BIND_SERVICE` and `NET_RAW`.

## DNS updates

Details: [ADMIN.md](ADMIN.md#11-dns-updates); decisions D-064 to D-067.

```
 dhcp4.Handler (ACK) ── dnsupdate.Decide(scope.DNS, option 81, option 12, option 15)
        │                      └─> response option 81 (S/O/N)
        └─ Updater.Bound(lease, decision)          (memory only, no I/O)

 dnsupdate.Updater (goroutine)
   ├─ reconcile every 15 s: lease ended? → desired state = nothing
   ├─ due registrations → Client.Add/Remove{Forward,Reverse}
   │        └─ miekg/dns: UPDATE + prerequisites (DHCID) + TSIG → zone from snapshot
   └─ dns_registrations (desired / registered, retry, error)
```

- Zones and keys are configuration (snapshot, HA-replicated); registrations
  are local state of each server.
- Name protection is pure DNS logic (prerequisites on the DHCID record);
  Leasorix does not need to know anything about records it does not own.

## DHCPv6

Details: [ADMIN.md](ADMIN.md#12-dhcpv6); decisions D-069 to D-073.

```
 UDP [::]:547 per interface (SO_BINDTODEVICE, ff02::1:2)
        │
 dhcp6.Server ── worker ──> dhcp6.Handler
                              ├─ server ID rules, unicast → UseMulticast
                              ├─ MAC filter (DUID-LL/-LLT, option 79), HA: Accept(DUID)
                              ├─ scope: relay's link-address or interface addresses
                              │        └─ scope6.Snapshot (atomic, OnChange)
                              └─ lease6.Manager.Assign/Extend/Release/Decline
                                       ├─ in-memory table + leases6 (SQLite)
                                       └─ HA: CanAllocate6 (own share)
```

- Separate from the IPv4 path (own tables and packages, D-070), but
  following the same pattern: configuration in the snapshot, bindings in
  memory and persisted before the response, expiry goroutine, audit,
  metrics (`leasorix_dhcp6_*`).
- Addresses are searched deterministically from DUID/IAID (scan up to
  65,536, otherwise hash candidates), so that even a /64 works as a range
  (D-071).
- HA: configuration is replicated, bindings are not; each server allocates
  only from its own share (D-073).
