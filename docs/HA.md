# HA guide

Two Leasorix servers on the same network share the scopes, replicate
leases and configuration, and take over for each other when one fails.

## Modes

| Mode | Who answers in normal operation | Free addresses |
|---|---|---|
| `load-balance` | Primary for `load_balance_percent` of the clients (hash of the MAC), secondary for the rest | Split in the same ratio |
| `hot-standby` | Only the primary | Standby keeps `standby_reserve_percent` (default 5%) as a reserve |

Each free address is owned by exactly one server. A server assigns new
leases only from its own share; existing leases are renewed by either server.

## Setup

With Docker: `deploy/docker-compose.ha.yml` starts a ready-made pair
including certificates; running it on two hosts is described in
[DOCKER.md](DOCKER.md#ha-on-two-docker-hosts). Without containers:

1. **Certificates** (mTLS, recommended): in the UI under
   *High availability → Generate mTLS certificates* or via the CLI
   ```sh
   leasorixd ha certs --out ./ha-certs dhcp-a dhcp-b
   ```
   Copy `ca.pem`, `<node>.pem` and `<node>-key.pem` to each node
   (transfer keys only over secure channels). Alternatively, use
   `tls.mode: secret` with a shared secret (≥ 16 characters, preferably ≥ 32,
   via `shared_secret_file`).
2. **Configuration** on both servers (the values of `mode`, the percentages,
   `mclt` and `config_leader` must match, otherwise the servers will not
   connect):
   ```yaml
   # Server A                              # Server B: node_id dhcp-b,
   ha:                                     # role secondary, peer dhcp-a
     mode: load-balance                    # or hot-standby
     node_id: dhcp-a
     role: primary
     listen: "10.99.0.1:8068"
     peer: { id: dhcp-b, address: "10.99.0.2:8068" }
     # load_balance_percent: 50
     # standby_reserve_percent: 5
     # replication: sync                   # or async
     # mclt: 1h
     # auto_partner_down: 0                # 0 = manual only
     tls:
       mode: mtls
       ca_file: /etc/leasorix/ha/ca.pem
       cert_file: /etc/leasorix/ha/dhcp-a.pem
       key_file: /etc/leasorix/ha/dhcp-a-key.pem
   ```
   All values can also be set via environment variables
   (`LEASORIX_HA_PEER_ADDRESS`, `LEASORIX_HA_TLS_SHARED_SECRET_FILE`, …).
3. **Start**: first set up the config leader (default: the primary)
   (setup token, scopes), then start the partner. The partner takes over the
   configuration and users automatically; afterwards, signing in works on
   both servers with the same credentials.
4. Open the peer port (default 8068/TCP) only between the two servers. A
   dedicated link (VLAN/cable) for replication is good, but not required.

A server with an HA configuration only starts assigning addresses once it
has synchronized with its partner. If only one server is put into
operation, the partner failure must be confirmed once (see below) – or
enable HA only when both servers are ready.

## States

| State | Meaning | DHCP |
|---|---|---|
| `NORMAL` | Partner connected, everything synchronized | Split according to the mode; leases are replicated to the partner before the ACK |
| `COMMUNICATION_INTERRUPTED` | Partner unreachable (failure or network partition) | Answers all clients; new addresses only from its own share, lease time at most MCLT |
| `PARTNER_DOWN` | Partner confirmed as down | All clients; after the MCLT has elapsed, the entire pool |
| `RECOVER` | Synchronization with the partner in progress (after startup or reconnect) | No replies |
| `RECOVER_DONE` | Own synchronization finished, waiting for the partner | No replies |

Synchronization pulls the partner's complete lease table and merges it
(the newer change wins; for true conflicts, the later lease end wins, then
the higher node ID). It typically takes less than a second; clients retry
their requests anyway.

## Operation

**A server fails.** After `communication_timeout` (default 10 s), the
other server goes to `COMMUNICATION_INTERRUPTED`: existing clients continue
to be renewed (by at most the MCLT), and new clients get addresses from its
own share (in hot standby, from the reserve).

**Confirm partner down.** If the partner is definitely gone for good (not
just a disrupted connection!), use *Confirm partner down* in the UI or
`POST /api/v1/ha/partner-down` (admin). Once the MCLT has elapsed, the
server uses the entire pool. With `auto_partner_down: 30m`, this happens
automatically after that time – caution: in a pure network partition
between the servers, both would then use the entire pool; ICMP conflict
detection remains active as a safety net.

**Return.** The returning server starts in `RECOVER`, synchronizes, and
both go to `NORMAL`. A server that was gone for longer than
`auto_partner_down` always waits for the partner.

**Changing the configuration.** Only on the config leader; the partner
shows a banner and rejects changes (API: 409). Changes reach the partner
within seconds (< 1 s in testing). If the leader is down for an extended
period, set `config_leader` on the remaining server to that server's role
and restart it; when bringing the old leader back, align both
configurations again.

**Maintenance / updates.** Restart the servers one at a time. A freshly
restarted server synchronizes with the running partner immediately.

**DNS updates.** DNS zones and TSIG keys are replicated like the rest of
the configuration. Each server registers the leases it assigns or renews;
if the partner takes over a client, it updates that client's records (same
DHCID, no conflict). The registration list
(*DNS updates → Registrations*) always shows the state of the local
server.

## DHCPv6

The DHCPv6 configuration (IPv6 scopes, exclusions, reservations, options)
is replicated by the config leader just like the IPv4 configuration.
DHCPv6 **bindings**, however, are not replicated:

- Clients are split by a hash of the DUID, addresses and delegated
  prefixes by a hash of the address – in the same ratio as with IPv4 (load
  balance, or hot standby with a reserve). Each server assigns only from
  its own share, so no duplicate bindings occur even without replication.
- Messages carrying a server's server ID (REQUEST, RENEW, RELEASE,
  DECLINE) are answered only by that server.
- If the partner is unreachable (`COMMUNICATION_INTERRUPTED`,
  `PARTNER_DOWN`), a server answers all clients, but even then assigns only
  from its own share (taking over the entire pool after the MCLT, as with
  IPv4, does not apply because the server does not know the partner's
  bindings). Clients of the failed server receive a new address or a new
  prefix from this share at REBIND; the old binding is ended with a
  lifetime of 0. For delegated prefixes, this means the prefixes behind the
  router change.
- In hot standby, the standby owns only the reserve (`standby_reserve_percent`)
  – more than enough with a /64 as the range; for small ranges or
  delegation pools, choose a larger value if necessary.

## Fallback: split scope (without replication)

For two independent servers without an HA partnership: open the scope →
*Split scope…* → choose the share (typically 80/20) → *Split*. This server
excludes the second server's range; load the downloaded file on the
second server under *Scopes → Import…* (there, the first server's part is
excluded). API: `POST /scopes/{id}/split`,
`POST /scopes/import`.

## Emergency: back up and restore the lease database

- UI: *High availability → Emergency* or *Leases → Export*; import is
  admin-only.
- API: `GET /api/v1/leases/export`, `POST /api/v1/leases/import`.
- CLI: `leasorixd leases export --config … [--out file.json]` (also while
  the server is running), `leasorixd leases import --config … file.json`
  (only with the service stopped).

The import merges the records with the existing data and can be
repeated. Records outside all scopes are skipped.

## Monitoring

Prometheus (`/metrics`):

| Metric | Meaning |
|---|---|
| `leasorix_ha_state{state}` | 1 for the current state |
| `leasorix_ha_partner_connected` | Heartbeats succeeding |
| `leasorix_ha_lease_updates_total{direction}` | Replicated lease changes |
| `leasorix_ha_replication_errors_total` | Undelivered update packets |
| `leasorix_ha_replication_duration_seconds` | Duration of the updates |
| `leasorix_ha_lease_conflicts_total` | Resolved conflicts |
| `leasorix_ha_resyncs_total{result}` | Synchronizations |
| `leasorix_ha_config_version` | Configuration version |
| `leasorix_ha_certificate_expiry_timestamp_seconds` | Expiry of the node certificate |

Recommended alerts: state ≠ NORMAL for longer than a few minutes,
`replication_errors_total` increasing, certificate expiring in < 30 days.
State changes are also recorded in the audit log (`ha.state`,
`ha.partner_down`, `ha.config_applied`, `lease.ha_conflict`).

## Troubleshooting

| Message (UI/log) | Cause |
|---|---|
| `partner reports node id …` / `both servers are configured as primary` / `… differs` | The settings of the two servers do not match |
| `peer certificate does not belong to …` | The certificate does not belong to the configured `peer.id` |
| `peer authentication failed` | Wrong shared secret or node ID |
| `connection refused` / `network is unreachable` | Partner is off, port 8068 is blocked or `peer.address` is wrong |
| Server stays in `RECOVER` | Partner never reached (first start) – start the partner or confirm partner down |

## Testing

`make test-ha` (as root) starts two real `leasorixd` instances in network
namespaces and checks: 5,000 clients in load balance without duplicate
addresses, a hard failure of the primary in hot standby (renewal by the
standby, takeover, synchronization on return), a network partition with
subsequent merge, and configuration replication.

## Limitations

- Exactly two servers per partnership and one partnership per server
  (all scopes); no Raft cluster.
- The clocks of both servers should be kept in sync via NTP (lease times
  and the ordering of changes are based on timestamps).
- Admin deletions made during a network partition can reappear during
  synchronization.
- DHCPv6 bindings are not replicated; if a server fails, its DHCPv6
  clients change their address (see [DHCPv6](#dhcpv6)).
