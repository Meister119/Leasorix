# Leasorix

Selbst gehosteter DHCP-Server mit dem Funktionsumfang eines Windows-DHCP-Servers,
Docker/Compose-Deployment, Web-UI und Hochverfügbarkeit.

> Status: in Entwicklung – siehe [docs/PROGRESS.md](docs/PROGRESS.md) und [PLAN.md](PLAN.md).

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
make test-e2e     # Playwright gegen die echte UI + echten DHCP-Client (root)
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
```

## Dokumentation

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
- [docs/HA.md](docs/HA.md) – HA-Handbuch
- [docs/DECISIONS.md](docs/DECISIONS.md)
- [docs/PROGRESS.md](docs/PROGRESS.md)
