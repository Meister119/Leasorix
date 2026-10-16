# Leasorix

Selbst gehosteter DHCP-Server mit dem Funktionsumfang eines Windows-DHCP-Servers,
Docker/Compose-Deployment, Web-UI und Hochverfügbarkeit.

> Status: in Entwicklung – siehe [docs/PROGRESS.md](docs/PROGRESS.md) und [PLAN.md](PLAN.md).

## Schnellstart mit Docker Compose

DHCP braucht Layer-2-Zugang zum LAN, deshalb läuft der Container im
Host-Netzwerk (oder mit eigener Adresse per macvlan – ein Bridge-Netz ohne
Relay funktioniert nicht):

```sh
git clone https://github.com/meister119/leasorix && cd leasorix/deploy
export LEASORIX_DHCP4_INTERFACES=eth0             # LAN-Interface des Hosts
export LEASORIX_HTTP_LISTEN=192.168.1.10:8067     # Adresse für die Web-UI
docker compose up -d                              # Image: ghcr.io/meister119/leasorix
docker compose logs leasorix | grep setup_token   # einmaliges Setup-Token
```

`https://192.168.1.10:8067/` öffnen, mit dem Token den ersten Administrator
anlegen, Scope einrichten – fertig. Lokal gebautes Image statt Registry:
`make docker` und `LEASORIX_IMAGE=leasorix:latest docker compose up -d`.

| Compose-Datei | Variante |
|---|---|
| `deploy/docker-compose.yml` | ein Server, Host-Netzwerk |
| `deploy/docker-compose.macvlan.yml` | ein Server mit eigener IP im LAN |
| `deploy/docker-compose.ha.yml` | HA-Paar (zwei Container, mTLS-Zertifikate automatisch) |

Details, Rechte, macvlan-Besonderheiten und Fehlersuche:
[docs/DOCKER.md](docs/DOCKER.md).

## Bauen

Voraussetzungen: Go ≥ 1.26 (die Toolchain wird über `go.mod` automatisch
geladen), Node.js ≥ 22 für die Web-UI, optional `golangci-lint` v2 und
`govulncheck`.

```sh
make build        # Web-UI bauen + bin/leasorixd mit eingebetteter UI
make build-noui   # nur das Binary (ohne Node.js), UI zeigt einen Hinweis
make test         # Go-Unit-Tests mit Race-Detector
make ui-test      # Frontend: Typecheck + Vitest
make lint         # golangci-lint
make test-integration  # DHCP-Ende-zu-Ende in Network Namespaces (root)
make test-ha      # Zwei-Knoten-HA-Tests (5000 Clients, Failover, Partition; root)
make test-dns     # DNS-Updates gegen BIND und PowerDNS (root, Docker)
make test-e2e     # Playwright gegen die echte UI + echten DHCP-Client (root)
make docker       # Container-Image leasorix:<version> und :latest
make test-docker  # Image + Compose-Dateien (host, macvlan, HA) testen (root)
make generate     # API- und Peer-Protokoll-Code neu erzeugen (protoc 3.21)
make ci           # lint + test + ui-test + build + vuln
```

## Starten

```sh
./bin/leasorixd --config deploy/examples/leasorix.yml
```

Jeder Config-Wert lässt sich per Umgebungsvariable überschreiben, z. B.
`LEASORIX_HTTP_LISTEN=10.0.0.5:8067` oder `LEASORIX_DATA_DIR=/data`.

Beim ersten Start steht im Log ein einmaliges Setup-Token
(`setup_token=…`). Damit legt man unter `https://127.0.0.1:8067/` den ersten
Administrator an (das Zertifikat ist anfangs selbstsigniert).

Endpunkte des Management-Listeners (Default `https://127.0.0.1:8067`):

| Pfad | Zweck |
|---|---|
| `/healthz` | Liveness |
| `/readyz` | Readiness (Datenbank, DHCP-Listener) |
| `/metrics` | Prometheus-Metriken |
| `/api/v1` | REST-API (Spec: `internal/api/openapi.yaml`) |
| `/` | Web-UI |

## Hochverfügbarkeit

Zwei Server teilen sich die Scopes (Load Balance oder Hot Standby),
replizieren Leases und Konfiguration über eine mTLS-gesicherte
gRPC-Verbindung und übernehmen füreinander. Einrichtung und Betrieb:
[docs/HA.md](docs/HA.md).

Weitere Kommandos:

```sh
leasorixd ha certs --out ./ha-certs dhcp-a dhcp-b   # mTLS-Zertifikate erzeugen
leasorixd leases export --config … --out leases.json   # Notfall-Export
leasorixd leases import --config … leases.json          # Import (Dienst gestoppt)
leasorixd backup --config … --out backup.db             # Backup (auch im Betrieb)
leasorixd restore --config … backup.db                  # Restore (Dienst gestoppt)
```

## DNS-Updates

Leasorix trägt Clients per RFC 2136 (mit TSIG) als A- und PTR-Records ein –
pro Scope nie, auf Anforderung des Clients oder immer, mit Namensschutz
(DHCID) und Entfernen beim Lease-Ende. Einrichtung unter *DNS-Updates* in der
Web-UI: [docs/ADMIN.md](docs/ADMIN.md#11-dns-updates).

## Verwaltung per Kommandozeile

`make build` erzeugt zusätzlich `bin/leasorixctl`, das den Server über die
REST-API steuert (API-Token unter *Benutzer & Einstellungen* anlegen):

```sh
export LEASORIX_URL=https://127.0.0.1:8067 LEASORIX_TOKEN=lxt_… LEASORIX_INSECURE=1
leasorixctl status
leasorixctl scopes list
leasorixctl config export -f config.yaml
leasorixctl config import --netsh dhcp-dump.txt --dry-run   # Umzug von Windows
```

## Dokumentation

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
- [docs/ADMIN.md](docs/ADMIN.md) – Admin-Handbuch
- [docs/HA.md](docs/HA.md) – HA-Handbuch
- [docs/DOCKER.md](docs/DOCKER.md) – Docker, Compose, Netzwerk-Hinweise
- [docs/DECISIONS.md](docs/DECISIONS.md)
- [docs/PROGRESS.md](docs/PROGRESS.md)
