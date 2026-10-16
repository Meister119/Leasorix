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
  gRPC   <---> |  ha: Failover, Replikation ---+               |
  (peer)        +---------------------------------------------+
```

Ein Prozess, ein statisches Binary. Komponenten laufen als Goroutinen unter
einem gemeinsamen `errgroup`; fällt eine Komponente aus oder kommt
`SIGTERM`, wird der Kontext abgebrochen und alle fahren geordnet herunter.

## Pakete

| Paket | Aufgabe |
|---|---|
| `cmd/leasorixd` | Daemon: Flags, Config, Logger, Signal-Handling; Offline-Kommandos (`ha certs`, `leases`, `backup`, `restore`, `healthcheck`) |
| `cmd/leasorixctl` | REST-CLI auf Basis des generierten API-Clients |
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
| `internal/auth` | Benutzer, Rollen, Sessions, API-Tokens, Setup |
| `internal/audit` | Audit-Log (Schreiben im Tx, Abfrage, Purge) |
| `internal/dhcpopt` | Options-Katalog und typisierte Werte |
| `internal/api/gen` | aus OpenAPI generierter Code (Server-Interface, Modelle, Client) |
| `internal/configio` | Konfigurations-Export/-Import (`leasorix-config/v1`), netsh-Importer |
| `internal/backup` | Backup (VACUUM INTO) und logischer Restore |
| `web` | React-UI, per `go:embed` eingebettet |
| `internal/ha` | HA-Partnerschaft: Zustandsautomat, Pool-Aufteilung, gRPC-Peer-Protokoll (`pb`), Lease-/Config-Replikation, Zertifikate |
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
              scope.Store.Snapshot()  <-- Link-Auswahl (giaddr/82.5/118/Interface-IP,
                                                        + Superscope-Mitglieder)
                                                        |  ha.Node.Accept (wer antwortet)
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
- **BOOTP**: Anfragen ohne DHCP-Nachrichtentyp laufen durch denselben Pfad
  (Offer + Request in einem Schritt) und erhalten eine BOOTREPLY.
- **Expiry-Worker**: alle `expiry_interval` → `active`→`expired`,
  Löschen nach Grace-Period bzw. Decline-Zeit, abgelaufene Angebote räumen.

## Management-API und Web-UI

```
Browser ──HTTPS──> api.Server (chi)
                     ├─ /healthz /readyz /metrics
                     ├─ /api/v1/*  ── authenticate (Cookie | Bearer)
                     │                └─ authorize (RBAC-Tabelle, CSRF, Origin, Content-Type)
                     │                   └─ gen.ServerInterface (aus openapi.yaml generiert)
                     │                      └─ handlers → scope.Store / lease.Manager / auth.Service / audit.Log
                     └─ /*  ── eingebettete SPA (web/dist, Build-Tag ui), Fallback index.html
```

- **Spec first:** `internal/api/openapi.yaml` ist die Quelle für Go-Typen und
  Router (`internal/api/gen`) sowie für die TypeScript-Typen der UI
  (`web/src/api/schema.d.ts`).
- **Audit-Actor:** Die Authentifizierungs-Middleware legt Benutzer und
  Client-IP in den Context; alle Domänenoperationen schreiben ihn in ihre
  Audit-Einträge.
- **UI:** React + TanStack Query; jede Seite liest über den typisierten
  Client, Mutationen invalidieren die betroffenen Queries. Rollen steuern,
  welche Aktionen angezeigt werden – die Durchsetzung passiert im Backend.

## Persistenz

SQLite im WAL-Modus unter `<data_dir>/leasorix.db`. Schreibzugriffe laufen
über einen Pool mit genau einer Verbindung (`BEGIN IMMEDIATE`), Lesezugriffe
über einen separaten Read-only-Pool. Das Schema wird über eingebettete
goose-Migrationen (`internal/store/migrations`) beim Start aktualisiert.

Migrationen: 00001 Grundschema, 00002 Policies/Optionen/Filter, 00003
Sessions/API-Tokens, 00004 HA (Lease-Zeitstempel in ms + `origin`,
`ha_state`, Trigger-gepflegter `config_version`-Zähler), 00005 Superscopes
und BOOTP.

`store.ReplaceTables` ersetzt eine Gruppe von Tabellen transaktional mit
abgeschalteten Fremdschlüsseln und anschließendem `foreign_key_check`; es
ist die gemeinsame Basis für HA-Config-Sync, Konfigurations-Import und
Restore. Danach ordnet `store.RemapLeases` jede Lease dem Scope zu, dessen
Subnetz ihre Adresse enthält (IDs können sich ändern). Ein `flock` auf
`<data_dir>/leasorixd.lock` verhindert parallele Prozesse auf derselben
Datenbank.

## Hochverfügbarkeit

Details und Bedienung: [HA.md](HA.md); Entscheidungen D-043 bis D-052.

```
  Server A (primary, Config-Leader)            Server B (secondary)
  +--------------------------------+   gRPC   +--------------------------------+
  | dhcp4 --Accept()--> ha.Node     |  mTLS /  | ha.Node <--Accept()-- dhcp4     |
  |   |                  |  ^       |  Secret  |  ^   |                  |      |
  |   v                  |  |       | <------> |  |   |                  v      |
  | lease.Manager <------+  |       | Heartbeat|  |   +--> lease.Manager        |
  |   CanAllocate/LeaseLimit|       | PushLeases  |       ApplyRemote          |
  |   Replicate ----------->+       | DumpLeases  |                            |
  | scope.Store  --config_version-->| PushConfig -+-> applyConfig -> scope.Store|
  +--------------------------------+          +--------------------------------+
```

- **Hot Path ohne Sperren:** `ha.Node` veröffentlicht Zustand und
  Übernahmezeit atomar; `Accept` (wer antwortet), `CanAllocate` (eigener
  Pool-Anteil) und `LeaseLimit` (MCLT) lesen nur diesen Schnappschuss.
- **Lease-Replikation:** der Lease-Manager trägt Änderungen in eine Outbox
  ein; ein Sender bündelt sie zu `PushLeases`. Bei `sync` wartet der Request
  auf die Bestätigung, bevor er lokal speichert und das ACK rausgeht.
  Empfangene Änderungen laufen durch `lease.Wins` (deterministisches
  Zusammenführen), ebenso der Abgleich per `DumpLeases`.
- **Zustandsautomat** (`internal/ha/machine.go`) ist rein ereignisgetrieben
  und mit simulierter Zeit getestet; `node.go` verbindet ihn mit gRPC,
  Persistenz (`ha_state`), Audit und Metriken.
- **Konfiguration:** Trigger erhöhen `config_version`; der Leader schickt
  bei Änderung einen Snapshot aller Konfigurationstabellen, der Partner
  ersetzt seine Tabellen in einer Transaktion und lädt den Scope-Snapshot
  neu. Die API des Partners lehnt Konfigurationsänderungen ab.

## Deployment (Container)

Details: [DOCKER.md](DOCKER.md); Entscheidungen D-060 bis D-063.

```
 deploy/Dockerfile
   ui     (Node, $BUILDPLATFORM)  npm ci + vite build ------------+
   build  (Go,   $BUILDPLATFORM)  go build -tags ui (cross) <-----+
                                  setcap-Helfer: File-Capabilities
   final  distroless/static:nonroot  /usr/local/bin/{leasorixd,leasorixctl}
                                     /etc/leasorix/leasorix.yml, VOLUME /data
                                     HEALTHCHECK leasorixd healthcheck

 Compose:  host  ── network_mode: host, DHCP auf Host-Interfaces
           macvlan ─ eigene MAC/IP im LAN (Parent-Interface/VLAN)
           ha    ── certs (Einmal-Dienst) -> dhcp-a + dhcp-b im macvlan-Netz
```

- Ein Container = ein Server-Prozess; Konfiguration über die Datei im Image
  plus `LEASORIX_*`-Variablen, Zustand ausschließlich im Volume `/data`.
- Läuft als UID 65532 mit read-only Root-FS; die einzigen Rechte sind die
  File-Capabilities `NET_BIND_SERVICE` und `NET_RAW` des Binaries.
