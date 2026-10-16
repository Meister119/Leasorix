# Docker & Compose

Leasorix runs as one container per server. The image contains `leasorixd`
(with the embedded web UI) and `leasorixctl`, is based on
`gcr.io/distroless/static-debian12:nonroot` (no shell, no package manager)
and runs as the unprivileged user `nonroot` (UID/GID 65532).

| File | Network | Use case |
|---|---|---|
| `deploy/docker-compose.yml` | `network_mode: host` | one server, simplest option |
| `deploy/docker-compose.macvlan.yml` | macvlan | one server with its own IP/MAC on the LAN |
| `deploy/docker-compose.ha.yml` | macvlan | two servers as an HA pair (including certificates) |

Image: `ghcr.io/meister119/leasorix:<version>` or `:latest` (see
[Building the image](#building-the-image) for a local build).

## Why not a bridge network?

DHCP clients without an address send `DHCPDISCOVER` as a broadcast into
their layer 2 segment. A container on the Docker bridge network (NAT) does
not see these broadcasts and does not know the LAN's interfaces — **without
a relay it does not receive a single request**. Leasorix therefore needs
direct L2 access: host network or macvlan. A bridge network with a published
port `67/udp` behind a DHCP relay (`ip helper-address` on the router) is not
tested and is not supported.

## Quick start (host network)

```sh
cd deploy
export LEASORIX_DHCP4_INTERFACES=eth0               # host interface(s), comma-separated
export LEASORIX_HTTP_LISTEN=192.168.1.10:8067       # management address (default: localhost only)
docker compose up -d
docker compose logs leasorix | grep setup_token     # one-time setup token
```

Then open `https://192.168.1.10:8067/` (the certificate is self-signed at
first), use the setup token to create the first administrator, and set up a
scope. Instead of reading the token from the log, you can preset it:
`LEASORIX_SETUP_TOKEN=…` (only relevant on the first start).

UDP port 67 must be allowed inbound on the host, and no other DHCP server
(e.g., libvirt's dnsmasq) may listen on the same interfaces.

## Configuration

- **File in the image:** `/etc/leasorix/leasorix.yml` (data directory
  `/data`, JSON logs, UI on `0.0.0.0:8067` with a self-signed
  certificate, DHCP off until interfaces are set).
- **Environment variables** override any value:
  `LEASORIX_<SECTION>_<KEY>`, e.g., `LEASORIX_DHCP4_INTERFACES=eth0`,
  `LEASORIX_LOG_LEVEL=debug`, `LEASORIX_HTTP_TLS_MODE=files`.
- **Custom file:** bind-mount it over the file in the image (prepared as a
  comment in the Compose files):
  `- ./leasorix.yml:/etc/leasorix/leasorix.yml:ro`. All keys:
  `deploy/examples/leasorix.yml`.
- **Data:** volume `/data` (database, self-signed TLS certificate, lock
  file). A bind mount used instead of a volume must be owned by UID 65532:
  `sudo chown 65532:65532 ./data`.
- **Ports:** `67/udp` (DHCP), `547/udp` (DHCPv6, if enabled),
  `8067/tcp` (UI, REST API, metrics), `8068/tcp` (HA peer connection).
- **DHCPv6:** set `LEASORIX_DHCP6_INTERFACES=eth0`. With the host network,
  IPv6 on the host interface is sufficient. The macvlan Compose files are
  IPv4-only; for DHCPv6, give the network `enable_ipv6: true` and an IPv6
  subnet (`ipam.config`) and assign the container a static IPv6 address in
  the scope's prefix. DHCPv6 needs no additional capabilities.

The DHCP configuration itself (scopes, options, reservations …) is stored
in the database and is managed via the UI, the API, or `leasorixctl`.

## Privileges and hardening

`leasorixd` needs exactly two capabilities:

| Capability | Purpose |
|---|---|
| `NET_BIND_SERVICE` | bind port 67 as non-root |
| `NET_RAW` | `AF_PACKET` replies to clients without an address (L2 unicast), ICMP conflict detection |

`NET_ADMIN` is not required. Because Docker does not pass capabilities on
to a non-root process, the binary carries them as **file capabilities**
(`cap_net_bind_service,cap_net_raw=ep`). The Compose files add further
hardening:

```yaml
read_only: true                          # read-only root file system
tmpfs: [/tmp]                            # SQLite temp files
security_opt: ["no-new-privileges:true"] # compatible with the file capabilities
cap_drop: [ALL]
cap_add: [NET_BIND_SERVICE, NET_RAW]
```

Important: both capabilities must remain in the container's capability set
— even for one-off commands such as `ha certs`. If they are missing, the
kernel refuses to start the binary with `exec /usr/local/bin/leasorixd:
operation not permitted`. Docker's default set includes both; they only
need to be added back when using `cap_drop: [ALL]` or `--cap-drop ALL`.

Running the container as root is neither necessary nor useful: with
`cap_drop: [ALL]`, root lacks the permission to write to `/data`, which is
owned by UID 65532.

## Host network (`docker-compose.yml`)

- `LEASORIX_DHCP4_INTERFACES`: names of the host interfaces on which DHCP
  is answered. Additional subnets are reached via DHCP relays (giaddr) —
  one interface is enough for that.
- `LEASORIX_HTTP_LISTEN`: default `127.0.0.1:8067`, so that the UI is not
  accidentally reachable from the entire network; for external access,
  enter a management address.
- HA with the host network on two hosts: see
  [HA on two Docker hosts](#ha-on-two-docker-hosts).

## macvlan (`docker-compose.macvlan.yml`)

The container gets its own MAC and IP address on the LAN and answers DHCP
on its `eth0`:

```sh
cd deploy
LEASORIX_PARENT=eth0 LEASORIX_SUBNET=192.168.1.0/24 \
LEASORIX_GATEWAY=192.168.1.1 LEASORIX_IP=192.168.1.5 \
  docker compose -f docker-compose.macvlan.yml up -d
```

- `LEASORIX_IP` lies outside the DHCP pool (or create it as an exclusion).
  `ip_range` (/32) prevents Docker from assigning addresses from the pool to
  other containers.
- **VLAN:** specify `eth0.20` as the parent — Docker creates the 802.1Q
  subinterface itself.
- **The Docker host itself cannot reach macvlan containers** (a property
  of macvlan). So open the UI and API from another machine, or create a
  macvlan "shim" interface on the host:

  ```sh
  ip link add lx-shim link eth0 type macvlan mode bridge
  ip addr add 192.168.1.4/32 dev lx-shim
  ip link set lx-shim up
  ip route add 192.168.1.5/32 dev lx-shim
  ```

- In virtual machines, the hypervisor must allow foreign MAC addresses on
  the parent interface (VMware: *Promiscuous Mode* and *Forged
  Transmits*, Hyper-V: *MAC address spoofing*). Wi-Fi interfaces are not
  suitable as a parent.

## High availability (`docker-compose.ha.yml`)

Two containers, `dhcp-a` (primary, config leader) and `dhcp-b`
(secondary), on the same macvlan network. On the first start, a one-shot
service `certs` generates the CA and node certificates for mTLS in the
`ha-certs` volume (`leasorixd ha certs --if-missing`); then both servers
start.

```sh
cd deploy
LEASORIX_PARENT=eth0 LEASORIX_SUBNET=192.168.1.0/24 LEASORIX_GATEWAY=192.168.1.1 \
LEASORIX_A_IP=192.168.1.5 LEASORIX_B_IP=192.168.1.6 \
  docker compose -f docker-compose.ha.yml up -d
docker compose -f docker-compose.ha.yml logs dhcp-a | grep setup_token
```

- Setup and configuration happen on `dhcp-a`; `dhcp-b` takes over the
  configuration and users automatically and is read-only.
- `LEASORIX_HA_MODE`: `load-balance` (default) or `hot-standby`;
  `LEASORIX_HA_AUTO_PARTNER_DOWN` (e.g., `1h`, default `0s` = manual
  only). For further parameters (`LEASORIX_HA_MCLT`, …), see
  [HA.md](HA.md).
- Both containers on **one** host protect against software failures and
  are ideal for trying things out; real redundancy requires two hosts.

### HA on two Docker hosts

Generate the certificates once and distribute them:

```sh
mkdir ha-certs && sudo chown 65532:65532 ha-certs
docker run --rm --network none -v "$PWD/ha-certs:/certs" \
  ghcr.io/meister119/leasorix:latest ha certs --out /certs dhcp-a dhcp-b
# copy to host B: ca.pem, dhcp-b.pem, dhcp-b-key.pem (owner 65532, key 0600)
```

Each host then runs `docker-compose.yml` (or the macvlan variant) with an
additional certificate mount `- ./ha-certs:/certs:ro` and the HA
variables, shown here for host A (192.168.1.10):

```yaml
    environment:
      LEASORIX_HA_NODE_ID: dhcp-a          # host B: dhcp-b
      LEASORIX_HA_ROLE: primary            # host B: secondary
      LEASORIX_HA_PEER_ID: dhcp-b          # host B: dhcp-a
      LEASORIX_HA_PEER_ADDRESS: 192.168.1.11:8068
      LEASORIX_HA_TLS_MODE: mtls
      LEASORIX_HA_TLS_CA_FILE: /certs/ca.pem
      LEASORIX_HA_TLS_CERT_FILE: /certs/dhcp-a.pem
      LEASORIX_HA_TLS_KEY_FILE: /certs/dhcp-a-key.pem
```

TCP port 8068 must be open between the hosts.

## Health check and restart

The image contains a `HEALTHCHECK`: `leasorixd healthcheck` queries
`/readyz` on the local server (address from the configuration, wildcard →
`127.0.0.1`). `docker compose ps` shows `healthy` as soon as the database
and the DHCP listener are ready. All services have `restart: unless-stopped`.

## Operations

```sh
docker compose logs -f leasorix                                   # logs (JSON)
docker compose exec leasorix leasorixd backup --out /data/backup.db
docker compose cp leasorix:/data/backup.db ./leasorix-backup.db   # copy the backup to the host
docker compose exec -e LEASORIX_TOKEN=lxt_… leasorix \
  leasorixctl --url https://127.0.0.1:8067 --insecure status      # CLI inside the container
docker compose pull && docker compose up -d                       # update (migration runs on start)
```

The `--url` of `leasorixctl` must match the listen address (with
`LEASORIX_HTTP_LISTEN=192.168.1.10:8067`, use that address). Restoring
works while running via the UI, the API, or `leasorixctl restore`; offline,
with the service stopped, via
`docker compose run --rm leasorix restore /data/backup.db`. Taking a backup
before an update is recommended.

## Building the image

```sh
make docker                            # leasorix:<version> and leasorix:latest
LEASORIX_IMAGE=leasorix:latest docker compose -f deploy/docker-compose.yml up -d
```

- Multi-stage: Node builds the UI, Go builds both binaries (static,
  `CGO_ENABLED=0`) and sets the file capabilities; the runtime image is
  distroless.
- Base images can be swapped via build arguments, e.g., for a registry
  mirror: `--build-arg NODE_IMAGE=mirror.gcr.io/library/node:22-bookworm-slim
  --build-arg GO_IMAGE=mirror.gcr.io/library/golang:1.27-bookworm`
  (`RUNTIME_IMAGE` works the same way).
- Behind a TLS-inspecting proxy, pass the proxy's CA as a build secret:
  `--secret id=ca,src=proxy-ca.pem` (used only for `npm ci` and
  `go mod download`; it does not end up in the image).
- Additional options via `DOCKER_BUILD_ARGS`, e.g.,
  `make docker DOCKER_BUILD_ARGS="--network host --secret id=ca,src=ca.pem"`.
- Multi-arch without emulation (the binaries are cross-compiled):
  `docker buildx build -f deploy/Dockerfile --platform linux/amd64,linux/arm64 .`
- Releases: for a tag `v1.2.3`, `.github/workflows/release.yml` builds the
  images for amd64/arm64 and publishes them as `1.2.3`, `1.2`, and
  `latest` to the GitHub Container Registry; commits to `main` are
  published as `edge`.

## Tests

`sudo make test-docker` tests the image and all three Compose files in
isolated networks (a veth pair + network namespace as the client LAN, never
the real network):

1. **Host network:** the container becomes `healthy`, runs as 65532 with a
   read-only root FS and exactly `NET_BIND_SERVICE`+`NET_RAW`, the UI is
   reachable, setup + scope via the API, a real DHCP client (`udhcpc`) gets
   an address, backup via `docker exec`, `leasorixctl` in the image.
2. **macvlan:** a client gets an address from the container with its own IP.
3. **HA:** both containers `NORMAL`, admin and scope replicated from the
   leader to the partner, the partner rejects configuration changes, clients
   are distributed across both servers, the lease is visible on the partner;
   after `docker kill dhcp-a`, a client of `dhcp-a` keeps its address on the
   partner.

Requirements: root, Docker with the Compose plugin, `iproute2`, `udhcpc`,
`curl`; image via `IMAGE=…` (default `leasorix:latest`).

## Troubleshooting

| Message / symptom | Cause |
|---|---|
| `exec /usr/local/bin/leasorixd: operation not permitted` | `NET_BIND_SERVICE`/`NET_RAW` missing (e.g., `cap_drop: [ALL]` without `cap_add`) |
| `open /data/leasorixd.lock: permission denied` | bind mount not owned by UID 65532 (`chown 65532:65532`) |
| `data directory … is in use by another leasorixd process` | two containers on the same volume |
| `no dhcp4.interfaces configured; DHCPv4 service is disabled` | `LEASORIX_DHCP4_INTERFACES` missing |
| Clients get no response (bridge network) | not supported; use host network or macvlan |
| Clients get no response (macvlan in a VM) | hypervisor drops foreign MACs (Promiscuous/Forged Transmits) |
| UI not reachable from the Docker host (macvlan) | macvlan isolation; create a shim interface |
