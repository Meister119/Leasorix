# Admin guide

Operating a Leasorix server. High availability has its own guide:
[HA.md](HA.md), container operation [DOCKER.md](DOCKER.md). Decisions and
background: [DECISIONS.md](DECISIONS.md).

## 1. Startup and bootstrap configuration

`leasorixd --config /etc/leasorix/leasorix.yml` starts the server. The file
contains only process settings (listeners, data directory, interfaces,
logging, HA); the actual DHCP configuration lives in the database and is
managed through the web UI, the REST API or `leasorixctl`. An annotated
example with all keys: [`deploy/examples/leasorix.yml`](../deploy/examples/leasorix.yml).

Every key can be overridden with an environment variable:
`LEASORIX_<SECTION>_<KEY>`, e.g. `LEASORIX_HTTP_LISTEN=10.0.0.5:8067`,
`LEASORIX_DHCP4_INTERFACES=eth0,eth1`, `LEASORIX_HA_PEER_ADDRESS=…`.

| Key | Default | Meaning |
|---|---|---|
| `data_dir` | `/var/lib/leasorix` | Database, certificates, lock file |
| `http.listen` | `127.0.0.1:8067` | Management (UI, API, `/metrics`) – management network only |
| `http.tls.mode` | `self-signed` | `self-signed`, `files` (your own certificate) or `off` |
| `dhcp4.interfaces` | – | Interfaces on which DHCP is served (empty = off) |
| `dhcp4.conflict_detection` | on, 500 ms | Ping before every new address |
| `dhcp4.bootp_lease_time` | `720h` | Lease duration for BOOTP clients |
| `dhcp6.interfaces` | – | Interfaces for DHCPv6 (empty = off), see [section 12](#12-dhcpv6) |
| `dhcp6.decline_time` | `24h` | Block period for addresses that a client reports via DECLINE |
| `audit.retention` | `2160h` (90 days) | Retention period of the audit log |
| `auth.*` | | Session duration, login lockout, setup token |
| `ha.*` | off | see [HA.md](HA.md) |

A data directory can only be used by one process at a time
(`leasorixd.lock`).

**First start:** A one-time setup token appears in the log
(`setup_token=…`); use it in the browser to create the first administrator.
The certificate is self-signed at first (`<data_dir>/tls`); for long-term
operation, provide your own certificate (`http.tls.mode: files`).

## 2. Scopes

A scope is an IPv4 subnet with a dynamic range, lease duration and
options. Which scope serves a client is determined by the address of the
interface or, for relays, by `giaddr` (or option 118 / 82.5).

- **Exclusions** remove addresses from dynamic assignment (e.g. for
  statically configured devices).
- **Reservations** permanently bind an address to a MAC or client ID; they
  take precedence and may lie outside the dynamic range. CSV import under
  *Reservations → CSV import*.
- **Superscopes** group scopes that serve the same physical network
  (multiple IP networks on one segment, "multinet"). Clients on the link of
  one member can get addresses from all members – the router must serve all
  networks on the segment. Create them under *Scopes → Superscopes*; assign a
  scope in the scope's *Settings*.
- **BOOTP:** Clients with a reservation always receive a BOOTP reply.
  Dynamic BOOTP (addresses from the pool) is enabled per scope with "Serve
  BOOTP clients dynamically"; the lease duration is
  `dhcp4.bootp_lease_time`. BOOTP replies contain the most important
  options (64-byte vendor area) as well as the boot server/file from option 66/67.
- **Split scope:** see [HA.md](HA.md#fallback-split-scope-without-replication).

## 3. Options, classes, policies, filters

Options are inherited: server < scope < server policy < scope policy <
reservation (the more specific level wins). If a client requests a
parameter list (option 55), exactly that list is delivered, in the client's
order. Custom option codes can be defined under *Users & settings →
Server options*.

Policies (as in Windows) evaluate conditions on MAC, client ID, hostname,
vendor/user class, defined classes and option 82 (wildcards `*`/`?`, hex
with `0x…`) and deliver their own options, their own lease duration and –
at scope level – their own address ranges. MAC filters (allow/deny,
wildcards) are managed and enabled under *Policies & filters → MAC filters*.

## 4. Leases

*Leases* shows all records with search and filters. "Delete" releases an
address immediately; "Convert to reservation" takes over IP, MAC and
hostname. Expired and released leases are kept for `expired_grace`
(default 4 h) so that the same client gets its address back. If an address
answers the ping before assignment, it is blocked for `decline_time`
(state "declined").

## 5. Users, roles, API tokens

| Role | Can |
|---|---|
| `readonly` | read everything (except user management), export the configuration |
| `operator` | additionally change the DHCP configuration and leases |
| `admin` | additionally manage users, settings, import, backup/restore, HA actions |

API tokens (`lxt_…`) are created under *My account & API tokens*; their
role is at most that of the user. They are sent as
`Authorization: Bearer lxt_…` and are required for `leasorixctl`.

## 6. Audit log and monitoring

Every change (UI, API, DHCP events, HA) is recorded in the audit log with
the actor and the before/after values. Prometheus metrics at `/metrics`
(`leasorix_dhcp4_*`, `leasorix_lease_*`, `leasorix_scope_*`, `leasorix_leases`,
`leasorix_ha_*`, `leasorix_dns_*`), health at `/healthz` and `/readyz`.

## 7. Export and import the configuration

*Users & settings → Backup & import* or
`leasorixctl config export|import`. The `leasorix-config/v1` format (YAML or
JSON) contains settings, custom option definitions, classes, server options
and server policies, MAC filters, superscopes, DNS zones and all scopes with
their exclusions, reservations, options, policies and DNS settings, plus
the DHCPv6 configuration (`server_options6`, `scopes6`, see
[section 12](#12-dhcpv6)). References use names, not database IDs; users,
API tokens and TSIG keys (secrets) are not included – a zone with a key can
only be imported if a key with the same name exists on the target server.

```yaml
format: leasorix-config/v1
server_options:
  - {code: 6, value: "10.0.0.53,10.0.0.54"}
superscopes:
  - {name: Building A}
scopes:
  - scope: {name: LAN, subnet: 10.0.0.0/24, start: 10.0.0.100, end: 10.0.0.200,
            lease_time: 86400, enabled: true, superscope: Building A}
    exclusions: [{start: 10.0.0.100, end: 10.0.0.109}]
    reservations: [{ip: 10.0.0.20, mac: "00:15:5d:0a:0b:0c", hostname: printer}]
    options: [{code: 3, value: 10.0.0.1}]
    policies: []
```

Import modes:

- **Merge** (`merge`): adds objects; objects that already exist (same
  scope name/overlapping subnet, class name, …) are conflicts. Server
  options and settings from the file overwrite the existing ones.
- **Replace** (`replace`): replaces the entire DHCP configuration; leases
  in subnets that still exist are kept.
- **Dry run**: validates against the current configuration without
  changing anything.

Every import is fully validated before anything is applied – if there is an
error, nothing changes. On an HA partner without the config leader role,
import is blocked.

## 8. Migrating from a Windows DHCP server

On the Windows server, run `netsh dhcp server dump > dhcp.txt` and load the
file under *Backup & import → Import from Windows DHCP* (first with
"Dry run") or with `leasorixctl config import --netsh dhcp.txt`. The import
covers scopes (name, range, state, lease duration from option 51, BOOTP
enablement), exclusions, reservations, option values at server, scope and
reservation level, custom option definitions, classes, superscopes, MAC
filters and the DNS update settings (`set dnsconfig` at server and scope
level, see [section 11](#11-dns-updates)). DNS zones and TSIG keys are not
in the dump and must be created in Leasorix. Lines that are not imported
are listed with a reason – typically class-specific option values (create
them as a policy in Leasorix), multicast scopes and DHCPv6. Active leases
are not included in the dump; clients renew their address at the next
renew (reservations stay the same anyway).

## 9. Backup and restore

A backup is a complete, consistent copy of the database (taken even while
the server is running) – including password hashes, so store it securely.

- UI: *Backup & import → Download backup / Restore…*
- Server CLI: `leasorixd backup --config … --out backup.db` (also while the
  server is running), `leasorixd restore --config … backup.db` (with the
  service stopped)
- API/CLI: `GET /api/v1/system/backup`, `POST /api/v1/system/restore`,
  `leasorixctl backup FILE`, `leasorixctl restore FILE --yes`

A restore brings back the configuration, users, API tokens and leases
(including DHCPv6 bindings); the server's audit log is kept and gets a
`system.restore` entry. All sessions end. Backups from older versions are
migrated automatically; backups from a newer version are rejected. For a
lease-only emergency there is also the lease export/import (see
[HA.md](HA.md#emergency-back-up-and-restore-the-lease-database)).

## 10. Command-line tool `leasorixctl`

`leasorixctl` controls a server through the REST API:

```sh
export LEASORIX_URL=https://dhcp01:8067 LEASORIX_TOKEN=lxt_…
export LEASORIX_CA=/etc/leasorix/tls/cert.pem   # or LEASORIX_INSECURE=1
leasorixctl status
leasorixctl scopes list
leasorixctl leases list --scope LAN -q printer
leasorixctl ha status
leasorixctl dns registrations
leasorixctl v6 leases -q 0003000102    # DHCPv6 bindings
leasorixctl config export -f config.yaml
leasorixctl -o json leases list            # JSON instead of a table
```

`leasorixctl` without arguments lists all commands. Exit code 0 = success,
1 = error (the server's message is printed), 2 = usage error.

## 11. DNS updates

Leasorix registers clients in DNS as A and PTR records via RFC 2136 (like
the DNS tab of a Windows DHCP scope). Setup:

1. **TSIG keys** (*DNS updates → TSIG keys*, admins only): name and
   algorithm (default `hmac-sha256`); the secret is generated randomly or
   taken from `tsig-keygen`. After saving, the UI shows the matching
   configuration for BIND and PowerDNS once.
2. **Zones** (*DNS updates → Zones*): forward zone (e.g. `example.lan`) and
   reverse zone (e.g. `1.168.192.in-addr.arpa`), each with a primary server
   (`IP` or `host:port`) and a key. For each name, the most specific
   matching zone applies.
3. **Scope** (*Scope → DNS*):

| Setting | Meaning |
|---|---|
| Register records | **Never**; **on client request** (option 81: A and PTR, or only PTR if the client registers its A record itself; clients without option 81 are not registered); **always** (A and PTR for every client with a name from option 81 or 12) |
| Domain | Name = first label of the client name + this domain; empty = domain name from option 15 |
| TTL | 0 = one third of the lease duration (1 min … 1 day) |
| Remove records when the lease ends | Release, expiry or deletion removes A and PTR (default: on) |
| Name protection (DHCID) | A name that belongs to another client or was created manually is never overwritten (RFC 4701/4703, default: on) |

The DHCP server never waits for DNS: updates run in the background with
retries (30 s, then doubling up to 1 h). *DNS updates →
Registrations* or `leasorixctl dns registrations` shows the registered name
and the state for each address (`registered`, `pending`, `conflict`,
`error` with a message). Conflicts caused by name protection are also
recorded in the audit log (`dns.conflict`) and are retried at the next
renewal of the lease. Metrics: `leasorix_dns_updates_total`,
`leasorix_dns_registrations`.

A client can only register a name in its scope's domain – a fully
qualified name in another zone sent by the client is rewritten to the
scope domain (and reported back to the client in option 81 of the reply).

**DNS servers:**

- *BIND:* include the key with `key "…" { … };` and set
  `update-policy { grant <key> zonesub ANY; };` in each zone.
- *PowerDNS:* `dnsupdate=yes`, `allow-dnsupdate-from=<Leasorix address>`,
  create the key with `pdnsutil import-tsig-key` and, per zone, run
  `pdnsutil set-meta <zone> TSIG-ALLOW-DNSUPDATE <key>`.
- *Windows DNS* does not support TSIG, only GSS-TSIG (Kerberos). With
  "Nonsecure and secure" dynamic updates, the zone can be added without a
  key (updates are then protected only by the source address).

With high availability, each server registers the leases it assigns; if
the partner takes over a client, it updates that client's records (same
DHCID, so no conflict). The registration list is per server.

## 12. DHCPv6

Leasorix serves IPv6 clients statefully per RFC 8415: addresses (IA_NA)
and prefix delegation (IA_PD) for routers, plus options for stateless
clients (INFORMATION-REQUEST). The configuration is separate from IPv4
(page *IPv6 (DHCPv6)*, API under `/api/v1/v6/…`, `leasorixctl v6 …`) but
follows the same concepts.

**Enabling:** in the bootstrap configuration

```yaml
dhcp6:
  interfaces: [eth0]      # interfaces with an IPv6 address in a scope's prefix
```

On each interface, Leasorix opens UDP port 547 and joins the multicast
group `ff02::1:2`. The server DUID (DUID-LLT from the MAC of the first
interface) is generated at first start and stays the same afterwards
(*Users & settings → System*). In a container, DHCPv6 needs host or macvlan
mode with IPv6, just like DHCPv4.

**Router advertisements:** Whether clients use DHCPv6 at all is controlled
by the router: the *Managed* flag (M=1) in the router advertisement for
addresses, the *Other* flag (O=1) for options only. With IPv6, the default
gateway always comes from the router advertisement, never from DHCPv6.

**Scopes** (*IPv6 (DHCPv6) → New IPv6 scope*):

| Field | Meaning |
|---|---|
| Prefix | Prefix of the link (e.g. `2001:db8:1::/64`); determines which clients the scope serves – via the address of the interface or, for relays, via the *link-address* |
| Start/end address | Range for address assignment; empty = no addresses (prefix delegation only) |
| Preferred / valid lifetime | In seconds; T1/T2 = 50%/80% of the preferred lifetime |
| Delegation pool, length | E.g. `2001:db8:100::/40` with length 56 → 65,536 delegable /56 prefixes for routers; must not overlap the link prefix |
| Server preference | Option 7 in ADVERTISE (0–255); with multiple servers, the client picks the highest |
| Rapid Commit | A SOLICIT with Rapid Commit is answered directly with a REPLY (2 instead of 4 messages) |

Addresses are chosen deterministically per client (same client – same
address, as long as it is free); large ranges such as an entire /64 are
possible. **Exclusions** remove address ranges from assignment.
**Reservations** bind an address to the client's DUID, optionally only for
one IAID (empty = any IA of the client); the address must be within the
prefix, but not necessarily within the range. The DUID can be found in the
lease or in the client's log (e.g. `/var/lib/dhcp/dhclient6.leases`;
Windows: `ipconfig /all`, "DHCPv6 Client DUID").

**Options** exist at server, scope and reservation level (the more
specific wins), including DNS servers (23), domain search list (24), SNTP
(31), NTP (56), boot file URL (59) and AFTR name (64). Custom options with
code and type are possible; options that the protocol sets itself (IA,
status, preference, Rapid Commit, relay options …) are blocked. If the
client sends no option request, all configured options are sent.

**Leases** (*IPv6 (DHCPv6) → Leases*, `leasorixctl v6 leases`) show
addresses and delegated prefixes with DUID, IAID, hostname (from option 39)
and lifetime; operators can delete bindings. Expired or released bindings
stay reserved for the same client for `dhcp6.expired_grace` (4 h);
addresses reported via DECLINE are blocked for `dhcp6.decline_time`.

**Relays:** A DHCPv6 relay (RELAY-FORW, including multi-hop) must send to
an address of one of the `dhcp6.interfaces`. The scope is determined by the
*link-address* of the innermost relay; the reply goes back to the relay as
RELAY-REPL (the Interface-ID is returned). Requests sent directly via
unicast are answered with `UseMulticast` (Leasorix does not offer the
server unicast option).

**MAC filters** also apply to DHCPv6 if the client's MAC is known
(DUID-LL/-LLT with an Ethernet address, or option 79 from the relay).

**High availability:** The DHCPv6 configuration is replicated by the
config leader just like the IPv4 configuration. DHCPv6 bindings are
**not** replicated: the clients (by DUID) and the addresses/prefixes are
split according to the HA mode as with IPv4, and each server assigns only
from its own share. If a server fails, the partner serves all clients – but
still only from its own share; clients of the failed server get a new
address at the next rebind (the old one with a lifetime of 0). The ranges
should therefore be generous (not an issue with a /64). Details:
[HA.md](HA.md#dhcpv6).

**Not implemented:** DNS updates (AAAA) for DHCPv6, policies/classes for
IPv6, lease export/import for DHCPv6 (backups include the bindings),
Reconfigure, the server unicast option, importing Windows DHCPv6 scopes
from the netsh dump.

Metrics: `leasorix_dhcp6_packets_received_total`, `…_packets_sent_total`,
`…_packets_dropped_total` (by reason) and `…_lease_events_total`.

## 13. Security

- Make the management listener reachable only from the management network
  (or only on localhost behind a reverse proxy).
- **Rogue DHCP:** A foreign DHCP server on the network can hand out wrong
  addresses and gateways to clients. Enable **DHCP snooping** on the
  switches and mark only the ports of the Leasorix servers (or of the
  relays) as trusted.
- Backups and HA keys are confidential; create API tokens with the smallest
  necessary role and set an expiration date.
- TSIG keys are stored in the database (they are needed for signing), are
  never returned by the API and can only be changed by admins. Use DNS
  updates without a key only in trusted networks.
