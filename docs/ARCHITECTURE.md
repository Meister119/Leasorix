# Architektur

> Lebendes Dokument – wird mit jeder Phase ergänzt.

## Überblick

```
                +---------------------------------------------+
                |                 leasorixd                    |
                |                                              |
  UDP 67  ----> |  dhcp4 (Phase 2)  --->  lease / scope        |
                |                              |               |
  HTTP   ----> |  api (chi): UI, REST,         v               |
  (mgmt)        |  /healthz /readyz /metrics  store (SQLite)   |
                |                              ^               |
  gRPC   <---> |  ha (Phase 6)  ---------------+               |
  (peer)        +---------------------------------------------+
```

Ein Prozess, ein statisches Binary. Komponenten laufen als Goroutinen unter
einem gemeinsamen `errgroup`; fällt eine Komponente aus oder kommt
`SIGTERM`, wird der Kontext abgebrochen und alle fahren geordnet herunter.

## Pakete

| Paket | Aufgabe |
|---|---|
| `cmd/leasorixd` | Einstiegspunkt: Flags, Config laden, Logger, Signal-Handling |
| `internal/app` | Verdrahtung aller Komponenten, Lifecycle |
| `internal/config` | Bootstrap-Konfiguration (YAML + Env) |
| `internal/logging` | `slog`-Logger (JSON/Text) |
| `internal/store` | SQLite-Zugriff, Migrationen, Transaktionen |
| `internal/health` | Liveness/Readiness mit registrierbaren Checks |
| `internal/metrics` | Prometheus-Registry |
| `internal/api` | HTTP-Management-Server |
| `internal/dhcp4` | DHCPv4-Engine (Phase 2) |
| `internal/scope` | Scopes, Pools, Exclusions, Reservierungen, Policies |
| `internal/lease` | Lease-Store, Allokation, Konfliktprüfung, Expiry |
| `internal/auth` | Benutzer, Rollen, Sessions, Tokens (Phase 5) |
| `internal/ha` | Peer-Replikation und Failover (Phase 6) |
| `internal/dnsupdate` | RFC-2136-Updates (Phase 9) |
| `internal/dhcp6` | DHCPv6 (Phase 10) |

## Konfiguration

Zwei Ebenen (siehe DECISIONS D-003):

1. **Bootstrap** – YAML-Datei + `LEASORIX_*`-Env: Listener, Datenverzeichnis,
   Logging, Interfaces, HA-Peers.
2. **DHCP-Konfiguration** – in der Datenbank, verwaltet über API/UI.

## Persistenz

SQLite im WAL-Modus unter `<data_dir>/leasorix.db`. Schreibzugriffe laufen
über einen Pool mit genau einer Verbindung (`BEGIN IMMEDIATE`), Lesezugriffe
über einen separaten Read-only-Pool. Das Schema wird über eingebettete
goose-Migrationen (`internal/store/migrations`) beim Start aktualisiert.
