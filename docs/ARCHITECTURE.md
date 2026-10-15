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

## DHCPv4-Pfad

```
socket(s0) --> Server.readLoop --> worker (max N) --> Handler.Handle
                                                        |  per-Client-Lock
                                                        v
              scope.Store.Snapshot()  <-- Link-Auswahl (giaddr/82.5/118/Interface-IP)
                                                        |
                                    lease.Manager.Offer/Request/Release/Decline
                                         |  (Mutex, In-Memory-Index)
                                         |-- ICMPProber (Konflikt-Ping, außerhalb des Locks)
                                         `-- DBStore.Apply --> Group-Commit-Writer --> SQLite
                                                        |
Response (Ziel nach RFC 2131 4.1) <---------------------+
   |-- UDP an giaddr:67 / ciaddr:68 / 255.255.255.255:68
   `-- L2-Unicast an chaddr (AF_PACKET), Fallback Broadcast
```

- **Scopes** liegen als unveränderlicher Snapshot vor (`atomic.Pointer`),
  jede Änderung über `scope.Store` baut ihn neu und benachrichtigt Listener
  (z. B. den Lease-Manager bei gelöschten Scopes).
- **Leases**: Der Lease-Manager ist die Autorität. Entscheidungen erfolgen im
  Speicher, Persistenz über den Group-Commit-Writer; ein ACK wird erst nach
  erfolgreichem Commit gesendet.
- **Expiry-Worker**: alle `expiry_interval` → `active`→`expired`,
  Löschen nach Grace-Period bzw. Decline-Zeit, abgelaufene Angebote räumen.

## Persistenz

SQLite im WAL-Modus unter `<data_dir>/leasorix.db`. Schreibzugriffe laufen
über einen Pool mit genau einer Verbindung (`BEGIN IMMEDIATE`), Lesezugriffe
über einen separaten Read-only-Pool. Das Schema wird über eingebettete
goose-Migrationen (`internal/store/migrations`) beim Start aktualisiert.
