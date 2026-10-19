# Installation

Every release is published ready to run – nothing has to be built on the
server:

| Way | What you get |
|---|---|
| [Docker image](#docker) (recommended) | `ghcr.io/meister119/leasorix` for linux/amd64 and linux/arm64, plus Compose files pinned to the release |
| [Binaries](#binaries-with-systemd) | `leasorixd` (web UI embedded) and `leasorixctl` as static Linux binaries for amd64 and arm64, with an example configuration and a systemd unit |
| [From source](../README.md#building) | `make build` (Go and Node.js needed) |

Releases: [github.com/meister119/leasorix/releases](https://github.com/meister119/leasorix/releases).

## Versions and image tags

| Image tag | Meaning |
|---|---|
| `1.2.3` | exactly this release – recommended for servers |
| `1.2` | newest patch release of 1.2 |
| `latest` | newest release (pre-releases such as `1.3.0-rc.1` never become `latest`) |
| `edge` | current state of `main`, between releases |

Database migrations run automatically when a newer version starts. Going
back to an older version is only possible by restoring a backup taken
before the update.

## Access to a private repository

While the repository or the container package is private, images and
release files need GitHub credentials:

- **Make the image public** (owner, once): GitHub → *Packages* →
  `leasorix` → *Package settings* → *Change visibility* → *Public*.
  `docker pull` then works without login, even if the repository itself
  stays private.
- **Or log in on the server** with a personal access token (classic) with
  the scope `read:packages`:
  ```sh
  echo "$TOKEN" | docker login ghcr.io -u <github-user> --password-stdin
  ```
- **Release files** of a private repository: download them in the browser
  or with the [GitHub CLI](https://cli.github.com/) (`gh auth login`), as
  shown below. For a public repository, plain URLs work as well:
  `https://github.com/meister119/leasorix/releases/latest/download/<file>`.

## Docker

DHCP needs layer 2 access to the LAN, so the container runs in the host
network (or with its own address via macvlan, see [DOCKER.md](DOCKER.md)).

```sh
mkdir leasorix && cd leasorix
gh release download -R meister119/leasorix -p docker-compose.yml   # newest release
cat > .env <<'EOF'
LEASORIX_DHCP4_INTERFACES=eth0
LEASORIX_HTTP_LISTEN=192.168.1.10:8067
EOF
docker compose up -d
docker compose logs leasorix | grep setup_token     # one-time setup token
```

- `.env` next to the Compose file is read by every `docker compose`
  command, so the settings survive updates and restarts.
  `LEASORIX_DHCP4_INTERFACES` is the LAN interface of the host,
  `LEASORIX_HTTP_LISTEN` the address of the web UI (default: localhost
  only).
- The Compose file of a release pins the image to that version. Variants:
  `docker-compose.macvlan.yml` (own IP in the LAN) and
  `docker-compose.ha.yml` (HA pair) – same procedure, see
  [DOCKER.md](DOCKER.md).
- Then open `https://192.168.1.10:8067/` (self-signed certificate at
  first), create the first administrator with the setup token and set up a
  scope.

Without Compose, the same as one command:

```sh
docker run -d --name leasorix --network host --restart unless-stopped \
  --read-only --tmpfs /tmp --security-opt no-new-privileges:true \
  --cap-drop ALL --cap-add NET_BIND_SERVICE --cap-add NET_RAW \
  -e LEASORIX_DHCP4_INTERFACES=eth0 -e LEASORIX_HTTP_LISTEN=192.168.1.10:8067 \
  -v leasorix-data:/data ghcr.io/meister119/leasorix:latest
```

### Updating (Docker)

Add the new version to `.env` once (later only change the number):
`LEASORIX_IMAGE=ghcr.io/meister119/leasorix:1.2.4` – it overrides the
image of the Compose file. Then:

```sh
docker compose exec leasorix leasorixd backup --out /data/before-update.db
docker compose pull && docker compose up -d
```

With `LEASORIX_IMAGE=ghcr.io/meister119/leasorix:latest`, these two
commands alone update to the newest release.

## Binaries with systemd

```sh
VERSION=1.2.3 ARCH=amd64          # or arm64
gh release download "v$VERSION" -R meister119/leasorix \
  -p "leasorix_${VERSION}_linux_${ARCH}.tar.gz" -p SHA256SUMS
sha256sum -c SHA256SUMS --ignore-missing
tar -xzf "leasorix_${VERSION}_linux_${ARCH}.tar.gz"
cd "leasorix_${VERSION}_linux_${ARCH}"

sudo useradd --system --no-create-home --shell /usr/sbin/nologin leasorix
sudo install -m 0755 leasorixd leasorixctl /usr/local/bin/
sudo install -D -m 0640 -g leasorix leasorix.yml /etc/leasorix/leasorix.yml
sudo install -m 0644 leasorixd.service /etc/systemd/system/
```

Set at least `dhcp4.interfaces` and `http.listen` in
`/etc/leasorix/leasorix.yml` (all keys are explained in the file), then:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now leasorixd
sudo journalctl -u leasorixd | grep setup_token    # one-time setup token
```

The unit (`deploy/systemd/leasorixd.service`):

- runs `leasorixd` as the unprivileged user `leasorix` with only
  `CAP_NET_BIND_SERVICE` (port 67/547) and `CAP_NET_RAW` (replies to
  clients without an address, ICMP conflict detection);
- creates the data directory `/var/lib/leasorix` (`data_dir` of the
  example configuration) and `/var/log/leasorix`; the rest of the file
  system is read-only for the service (`ProtectSystem=strict`). For another
  `data_dir`, add it with `ReadWritePaths=`;
- restarts the server after a crash and stops it cleanly with SIGTERM.

Settings can also be overridden with environment variables in a drop-in
(`sudo systemctl edit leasorixd`):

```ini
[Service]
Environment=LEASORIX_DHCP4_INTERFACES=eth0,eth1
```

HA certificates (see [HA.md](HA.md)) must be readable by the service
user, e.g. in `/etc/leasorix/ha` with
`sudo chown root:leasorix /etc/leasorix/ha/*.pem && sudo chmod 0640 /etc/leasorix/ha/*.pem`.

Offline commands run as the service user, so the files keep the right
owner:

```sh
sudo -u leasorix leasorixd backup --config /etc/leasorix/leasorix.yml --out /var/lib/leasorix/backup.db
sudo systemctl stop leasorixd
sudo -u leasorix leasorixd restore --config /etc/leasorix/leasorix.yml /var/lib/leasorix/backup.db
sudo systemctl start leasorixd
```

Inbound ports: `67/udp` (DHCP), `547/udp` (DHCPv6, if enabled),
`8067/tcp` (web UI, API, metrics – management network only), `8068/tcp`
(HA, only between the two servers).

### Updating (binaries)

Take a backup (see above), download and unpack the new archive as during
the installation, then:

```sh
sudo install -m 0755 leasorixd leasorixctl /usr/local/bin/
sudo systemctl restart leasorixd
leasorixd version
```

### HA pairs

Both servers should run the same version. Update them one after the
other: the partner serves alone in the meantime; wait until the HA status
shows `NORMAL` again before updating the second server.

## Publishing a release (maintainers)

```sh
git switch main && git pull
git tag -a v1.2.3 -m "Leasorix 1.2.3"
git push origin v1.2.3
```

The workflow `.github/workflows/release.yml` then builds the image for
amd64 and arm64 (tags `1.2.3`, `1.2`, `latest`), the binaries
(`make dist`) and creates the GitHub release with all files, `SHA256SUMS`
and release notes generated from the merged pull requests. A tag with a
suffix (`v1.3.0-rc.1`) becomes a pre-release and leaves `latest`
unchanged. `make ui dist` builds the same files locally in `dist/`.
