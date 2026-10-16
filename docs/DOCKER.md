# Docker & Compose

Leasorix läuft als ein Container pro Server. Das Image enthält `leasorixd`
(mit eingebetteter Web-UI) und `leasorixctl`, basiert auf
`gcr.io/distroless/static-debian12:nonroot` (keine Shell, kein
Paketmanager) und läuft als unprivilegierter Benutzer `nonroot` (UID/GID
65532).

| Datei | Netzwerk | Einsatz |
|---|---|---|
| `deploy/docker-compose.yml` | `network_mode: host` | ein Server, einfachste Variante |
| `deploy/docker-compose.macvlan.yml` | macvlan | ein Server mit eigener IP/MAC im LAN |
| `deploy/docker-compose.ha.yml` | macvlan | zwei Server als HA-Paar (inkl. Zertifikaten) |

Image: `ghcr.io/meister119/leasorix:<version>` bzw. `:latest` (siehe
[Image bauen](#image-bauen) für einen lokalen Build).

## Warum kein Bridge-Netz?

DHCP-Clients ohne Adresse senden `DHCPDISCOVER` als Broadcast in ihr
Layer-2-Segment. Ein Container im Docker-Bridge-Netz (NAT) sieht diese
Broadcasts nicht und kennt die Interfaces des LANs nicht – **ohne Relay
bekommt er keine einzige Anfrage**. Leasorix braucht deshalb direkten
L2-Zugang: Host-Netzwerk oder macvlan. Ein Bridge-Netz mit veröffentlichtem
Port `67/udp` hinter einem DHCP-Relay (`ip helper-address` auf dem Router)
ist nicht getestet und wird nicht unterstützt.

## Schnellstart (Host-Netzwerk)

```sh
cd deploy
export LEASORIX_DHCP4_INTERFACES=eth0               # Interface(s) des Hosts, kommagetrennt
export LEASORIX_HTTP_LISTEN=192.168.1.10:8067       # Management-Adresse (Default: nur localhost)
docker compose up -d
docker compose logs leasorix | grep setup_token     # einmaliges Setup-Token
```

Dann `https://192.168.1.10:8067/` öffnen (Zertifikat zunächst
selbstsigniert), mit dem Setup-Token den ersten Administrator anlegen und
einen Scope einrichten. Statt das Token aus dem Log zu lesen, kann man es
vorgeben: `LEASORIX_SETUP_TOKEN=…` (nur beim ersten Start relevant).

Auf dem Host muss UDP-Port 67 eingehend erlaubt sein, und kein anderer
DHCP-Server (z. B. dnsmasq von libvirt) darf auf denselben Interfaces
lauschen.

## Konfiguration

- **Datei im Image:** `/etc/leasorix/leasorix.yml` (Datenverzeichnis
  `/data`, JSON-Logs, UI auf `0.0.0.0:8067` mit selbstsigniertem
  Zertifikat, DHCP aus, bis Interfaces gesetzt sind).
- **Umgebungsvariablen** überschreiben jeden Wert:
  `LEASORIX_<SECTION>_<KEY>`, z. B. `LEASORIX_DHCP4_INTERFACES=eth0`,
  `LEASORIX_LOG_LEVEL=debug`, `LEASORIX_HTTP_TLS_MODE=files`.
- **Eigene Datei:** per Bind-Mount über die Datei im Image legen (in den
  Compose-Dateien als Kommentar vorbereitet):
  `- ./leasorix.yml:/etc/leasorix/leasorix.yml:ro`. Alle Schlüssel:
  `deploy/examples/leasorix.yml`.
- **Daten:** Volume `/data` (Datenbank, selbstsigniertes TLS-Zertifikat,
  Lock-Datei). Ein Bind-Mount statt Volume muss UID 65532 gehören:
  `sudo chown 65532:65532 ./data`.
- **Ports:** `67/udp` (DHCP), `547/udp` (DHCPv6, falls aktiviert),
  `8067/tcp` (UI, REST-API, Metriken), `8068/tcp` (HA-Peer-Verbindung).
- **DHCPv6:** `LEASORIX_DHCP6_INTERFACES=eth0` setzen. Im Host-Netzwerk
  genügt IPv6 auf dem Host-Interface. Die macvlan-Compose-Dateien sind
  IPv4-only; für DHCPv6 dem Netz `enable_ipv6: true` und ein IPv6-Subnetz
  (`ipam.config`) mitgeben und dem Container eine feste IPv6-Adresse im
  Präfix des Scopes zuweisen. Weitere Capabilities braucht DHCPv6 nicht.

Die DHCP-Konfiguration selbst (Scopes, Optionen, Reservierungen …) liegt in
der Datenbank und wird über UI, API oder `leasorixctl` gepflegt.

## Rechte und Härtung

`leasorixd` braucht genau zwei Capabilities:

| Capability | wofür |
|---|---|
| `NET_BIND_SERVICE` | Port 67 als nicht-root binden |
| `NET_RAW` | `AF_PACKET`-Antworten an Clients ohne Adresse (L2-Unicast), ICMP-Konfliktprüfung |

`NET_ADMIN` ist nicht nötig. Weil Docker einem nicht-root-Prozess keine
Capabilities vererbt, trägt das Binary sie als **File-Capabilities**
(`cap_net_bind_service,cap_net_raw=ep`). Die Compose-Dateien härten
zusätzlich:

```yaml
read_only: true                          # Root-Dateisystem schreibgeschützt
tmpfs: [/tmp]                            # Temp-Dateien von SQLite
security_opt: ["no-new-privileges:true"] # verträgt sich mit den File-Capabilities
cap_drop: [ALL]
cap_add: [NET_BIND_SERVICE, NET_RAW]
```

Wichtig: Beide Capabilities müssen im Capability-Set des Containers bleiben
– auch bei Einmal-Kommandos wie `ha certs`. Fehlen sie, verweigert der
Kernel den Start mit `exec /usr/local/bin/leasorixd: operation not
permitted`. Der Default von Docker enthält beide; nur bei `cap_drop: [ALL]`
bzw. `--cap-drop ALL` müssen sie wieder hinzugefügt werden.

Den Container als root zu starten ist weder nötig noch sinnvoll: mit
`cap_drop: [ALL]` fehlt root das Recht, in das `/data` von UID 65532 zu
schreiben.

## Host-Netzwerk (`docker-compose.yml`)

- `LEASORIX_DHCP4_INTERFACES`: Interface-Namen des Hosts, auf denen DHCP
  beantwortet wird. Weitere Subnetze erreicht man über DHCP-Relays
  (giaddr) – dafür genügt ein Interface.
- `LEASORIX_HTTP_LISTEN`: Default `127.0.0.1:8067`, damit die UI nicht
  versehentlich im ganzen Netz erreichbar ist; für den Zugriff von außen
  eine Management-Adresse eintragen.
- HA mit Host-Netzwerk auf zwei Hosts: siehe
  [HA auf zwei Docker-Hosts](#ha-auf-zwei-docker-hosts).

## macvlan (`docker-compose.macvlan.yml`)

Der Container bekommt eine eigene MAC- und IP-Adresse im LAN und beantwortet
DHCP auf seinem `eth0`:

```sh
cd deploy
LEASORIX_PARENT=eth0 LEASORIX_SUBNET=192.168.1.0/24 \
LEASORIX_GATEWAY=192.168.1.1 LEASORIX_IP=192.168.1.5 \
  docker compose -f docker-compose.macvlan.yml up -d
```

- `LEASORIX_IP` liegt außerhalb des DHCP-Pools (bzw. als Ausschluss
  anlegen). `ip_range` (/32) verhindert, dass Docker weiteren Containern
  Adressen aus dem Pool gibt.
- **VLAN:** als Parent `eth0.20` angeben – Docker legt das
  802.1Q-Subinterface selbst an.
- **Der Docker-Host selbst erreicht macvlan-Container nicht** (Eigenschaft
  von macvlan). UI und API also von einem anderen Rechner aufrufen oder auf
  dem Host ein macvlan-„Shim“-Interface anlegen:

  ```sh
  ip link add lx-shim link eth0 type macvlan mode bridge
  ip addr add 192.168.1.4/32 dev lx-shim
  ip link set lx-shim up
  ip route add 192.168.1.5/32 dev lx-shim
  ```

- In virtuellen Maschinen muss der Hypervisor fremde MAC-Adressen am
  Parent-Interface zulassen (VMware: *Promiscuous Mode* und *Forged
  Transmits*, Hyper-V: *MAC-Adress-Spoofing*). WLAN-Interfaces eignen sich
  nicht als Parent.

## Hochverfügbarkeit (`docker-compose.ha.yml`)

Zwei Container `dhcp-a` (primary, Config-Leader) und `dhcp-b` (secondary)
im selben macvlan-Netz. Ein Einmal-Dienst `certs` erzeugt beim ersten Start
CA und Knotenzertifikate für mTLS im Volume `ha-certs`
(`leasorixd ha certs --if-missing`); danach starten beide Server.

```sh
cd deploy
LEASORIX_PARENT=eth0 LEASORIX_SUBNET=192.168.1.0/24 LEASORIX_GATEWAY=192.168.1.1 \
LEASORIX_A_IP=192.168.1.5 LEASORIX_B_IP=192.168.1.6 \
  docker compose -f docker-compose.ha.yml up -d
docker compose -f docker-compose.ha.yml logs dhcp-a | grep setup_token
```

- Setup und Konfiguration auf `dhcp-a`; `dhcp-b` übernimmt Konfiguration
  und Benutzer automatisch und ist read-only.
- `LEASORIX_HA_MODE`: `load-balance` (Default) oder `hot-standby`;
  `LEASORIX_HA_AUTO_PARTNER_DOWN` (z. B. `1h`, Default `0s` = nur
  manuell). Weitere Parameter (`LEASORIX_HA_MCLT`, …) siehe
  [HA.md](HA.md).
- Beide Container auf **einem** Host schützen vor Software-Ausfällen und
  sind ideal zum Ausprobieren; echte Redundanz braucht zwei Hosts.

### HA auf zwei Docker-Hosts

Zertifikate einmal erzeugen und verteilen:

```sh
mkdir ha-certs && sudo chown 65532:65532 ha-certs
docker run --rm --network none -v "$PWD/ha-certs:/certs" \
  ghcr.io/meister119/leasorix:latest ha certs --out /certs dhcp-a dhcp-b
# auf Host B kopieren: ca.pem, dhcp-b.pem, dhcp-b-key.pem (Besitzer 65532, Key 0600)
```

Auf jedem Host läuft dann `docker-compose.yml` (oder die macvlan-Variante)
mit zusätzlichem Zertifikats-Mount `- ./ha-certs:/certs:ro` und den
HA-Variablen, hier für Host A (192.168.1.10):

```yaml
    environment:
      LEASORIX_HA_NODE_ID: dhcp-a          # Host B: dhcp-b
      LEASORIX_HA_ROLE: primary            # Host B: secondary
      LEASORIX_HA_PEER_ID: dhcp-b          # Host B: dhcp-a
      LEASORIX_HA_PEER_ADDRESS: 192.168.1.11:8068
      LEASORIX_HA_TLS_MODE: mtls
      LEASORIX_HA_TLS_CA_FILE: /certs/ca.pem
      LEASORIX_HA_TLS_CERT_FILE: /certs/dhcp-a.pem
      LEASORIX_HA_TLS_KEY_FILE: /certs/dhcp-a-key.pem
```

TCP-Port 8068 muss zwischen den Hosts offen sein.

## Healthcheck und Neustart

Das Image enthält einen `HEALTHCHECK`: `leasorixd healthcheck` fragt
`/readyz` des lokalen Servers ab (Adresse aus der Konfiguration, Wildcard →
`127.0.0.1`). `docker compose ps` zeigt `healthy`, sobald Datenbank und
DHCP-Listener bereit sind. Alle Dienste haben `restart: unless-stopped`.

## Betrieb

```sh
docker compose logs -f leasorix                                   # Logs (JSON)
docker compose exec leasorix leasorixd backup --out /data/backup.db
docker compose cp leasorix:/data/backup.db ./leasorix-backup.db   # Backup sichern
docker compose exec -e LEASORIX_TOKEN=lxt_… leasorix \
  leasorixctl --url https://127.0.0.1:8067 --insecure status      # CLI im Container
docker compose pull && docker compose up -d                       # Update (Migration läuft beim Start)
```

`--url` von `leasorixctl` muss zur Listen-Adresse passen (bei
`LEASORIX_HTTP_LISTEN=192.168.1.10:8067` also diese). Wiederherstellen geht
im laufenden Betrieb über UI, API oder `leasorixctl restore`; offline mit
gestopptem Dienst über
`docker compose run --rm leasorix restore /data/backup.db`. Vor einem
Update empfiehlt sich ein Backup.

## Image bauen

```sh
make docker                            # leasorix:<version> und leasorix:latest
LEASORIX_IMAGE=leasorix:latest docker compose -f deploy/docker-compose.yml up -d
```

- Multi-Stage: Node baut die UI, Go baut beide Binaries (statisch,
  `CGO_ENABLED=0`) und setzt die File-Capabilities, das Laufzeit-Image ist
  distroless.
- Basis-Images per Build-Argument austauschbar, z. B. für einen
  Registry-Mirror: `--build-arg NODE_IMAGE=mirror.gcr.io/library/node:22-bookworm-slim
  --build-arg GO_IMAGE=mirror.gcr.io/library/golang:1.27-bookworm`
  (`RUNTIME_IMAGE` analog).
- Hinter einem TLS-inspizierenden Proxy dessen CA als Build-Secret
  übergeben: `--secret id=ca,src=proxy-ca.pem` (wird nur für `npm ci` und
  `go mod download` verwendet, landet nicht im Image).
- Zusätzliche Optionen über `DOCKER_BUILD_ARGS`, z. B.
  `make docker DOCKER_BUILD_ARGS="--network host --secret id=ca,src=ca.pem"`.
- Multi-Arch ohne Emulation (die Binaries werden cross-kompiliert):
  `docker buildx build -f deploy/Dockerfile --platform linux/amd64,linux/arm64 .`
- Releases: `.github/workflows/release.yml` baut bei einem Tag `v1.2.3` die
  Images für amd64/arm64 und veröffentlicht sie als `1.2.3`, `1.2` und
  `latest` in der GitHub Container Registry, Commits auf `main` als `edge`.

## Tests

`sudo make test-docker` prüft das Image und alle drei Compose-Dateien in
isolierten Netzen (veth-Paar + Network Namespace als Client-LAN, nie das
echte Netz):

1. **Host-Netzwerk:** Container wird `healthy`, läuft als 65532 mit
   read-only Root-FS und genau `NET_BIND_SERVICE`+`NET_RAW`, UI erreichbar,
   Setup + Scope per API, ein echter DHCP-Client (`udhcpc`) bekommt eine
   Adresse, Backup per `docker exec`, `leasorixctl` im Image.
2. **macvlan:** Client bekommt eine Adresse vom Container mit eigener IP.
3. **HA:** beide Container `NORMAL`, Admin und Scope vom Leader auf den
   Partner repliziert, Partner lehnt Konfigurationsänderungen ab, Clients
   werden auf beide Server verteilt, Lease auf dem Partner sichtbar; nach
   `docker kill dhcp-a` behält ein Client von `dhcp-a` seine Adresse beim
   Partner.

Voraussetzungen: root, Docker mit Compose-Plugin, `iproute2`, `udhcpc`,
`curl`; Image über `IMAGE=…` (Default `leasorix:latest`).

## Fehlersuche

| Meldung / Symptom | Ursache |
|---|---|
| `exec /usr/local/bin/leasorixd: operation not permitted` | `NET_BIND_SERVICE`/`NET_RAW` fehlen (z. B. `cap_drop: [ALL]` ohne `cap_add`) |
| `open /data/leasorixd.lock: permission denied` | Bind-Mount gehört nicht UID 65532 (`chown 65532:65532`) |
| `data directory … is in use by another leasorixd process` | zwei Container auf demselben Volume |
| `no dhcp4.interfaces configured; DHCPv4 service is disabled` | `LEASORIX_DHCP4_INTERFACES` fehlt |
| Clients bekommen keine Antwort (Bridge-Netz) | nicht unterstützt, Host-Netzwerk oder macvlan nutzen |
| Clients bekommen keine Antwort (macvlan in VM) | Hypervisor verwirft fremde MACs (Promiscuous/Forged Transmits) |
| UI vom Docker-Host aus nicht erreichbar (macvlan) | macvlan-Isolation, Shim-Interface anlegen |
