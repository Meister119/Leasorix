# Leasorix

Selbst gehosteter DHCP-Server mit dem Funktionsumfang eines Windows-DHCP-Servers,
Docker/Compose-Deployment, Web-UI und Hochverfügbarkeit.

> Status: in Entwicklung – siehe [docs/PROGRESS.md](docs/PROGRESS.md) und [PLAN.md](PLAN.md).

## Bauen

Voraussetzungen: Go ≥ 1.26 (die Toolchain wird über `go.mod` automatisch
geladen), optional `golangci-lint` v2 und `govulncheck`.

```sh
make build        # bin/leasorixd
make test         # Unit-Tests mit Race-Detector
make lint         # golangci-lint
make ci           # lint + test + build + vuln
```

## Starten

```sh
./bin/leasorixd --config deploy/examples/leasorix.yml
```

Jeder Config-Wert lässt sich per Umgebungsvariable überschreiben, z. B.
`LEASORIX_HTTP_LISTEN=10.0.0.5:8067` oder `LEASORIX_DATA_DIR=/data`.

Endpunkte des Management-Listeners (Default `127.0.0.1:8067`):

| Pfad | Zweck |
|---|---|
| `/healthz` | Liveness |
| `/readyz` | Readiness (Datenbank, später DHCP-Listener, HA) |
| `/metrics` | Prometheus-Metriken |

## Dokumentation

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
- [docs/DECISIONS.md](docs/DECISIONS.md)
- [docs/PROGRESS.md](docs/PROGRESS.md)
