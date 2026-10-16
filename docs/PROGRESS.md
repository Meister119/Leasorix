# Fortschritt

Kurze Notiz pro abgeschlossener Phase (siehe PLAN.md). Entscheidungen stehen
in [DECISIONS.md](DECISIONS.md).

| Phase | Status |
|---|---|
| 1 – Fundament | ✅ abgeschlossen |
| 2 – DHCPv4-Kern | ✅ abgeschlossen |
| 3 – Optionen, Klassen, Policies, Filter | ✅ abgeschlossen |
| 4 – Audit, Statistiken, Metriken | ✅ abgeschlossen |
| 5 – REST-API, Auth, Web-UI | ✅ abgeschlossen |
| 6 – Hochverfügbarkeit | ✅ abgeschlossen |
| 7 – Komfort & Kompatibilität | ✅ abgeschlossen |
| 8 – Docker & Compose | offen |
| 9 – DNS-Integration | offen |
| 10 – DHCPv6 (optional) | offen |

---

## Phase 1 – Fundament

**Umgesetzt**
- Go-Modul `github.com/meister119/leasorix`, Repo-Struktur laut PLAN.md
  (Pakete der späteren Phasen als dokumentierte Platzhalter).
- `Makefile` mit `build`, `test` (Race-Detector), `lint`, `vuln`, `ci`;
  `.golangci.yml` (v2); GitHub-Actions-Workflow `ci.yml`.
- Bootstrap-Config: YAML (`--config` bzw. `LEASORIX_CONFIG`) + Env-Overrides
  `LEASORIX_<SECTION>_<KEY>`, strikte Validierung (unbekannte Keys = Fehler).
  Beispiel: `deploy/examples/leasorix.yml`.
- Strukturierte Logs via `slog` (JSON oder Text, Level konfigurierbar).
- Graceful Shutdown über `SIGINT`/`SIGTERM` (`signal.NotifyContext` +
  `errgroup`, HTTP-Shutdown mit Timeout, DB wird sauber geschlossen).
- SQLite-Store (modernc, WAL, FULL sync, Writer-/Reader-Pool), goose-
  Migrationen eingebettet. Tabellen: `scopes`, `exclusions`, `reservations`,
  `options`, `leases`, `audit_log`, `users`.
- HTTP-Management-Server (chi) mit `/healthz` (Liveness), `/readyz`
  (Readiness-Checks, aktuell: Datenbank) und `/metrics` (eigene
  Prometheus-Registry mit Go-/Prozess-Collector und `leasorix_build_info`).

**Akzeptanz**
- `make build` erzeugt `bin/leasorixd` ✅
- `leasorixd --config x.yml` startet, Endpunkte antworten, `SIGTERM` → Exit 0 ✅
- Migrationen idempotent (Test `TestMigrateIdempotent` + zweiter Start ohne
  erneute Migration) ✅
- Unit-Tests grün (`make test`), `make lint` ohne Findings ✅

**Offene Punkte / Hinweise**
- `govulncheck` läuft in der CI. In der aktuellen Entwicklungsumgebung blockt
  die Netzwerk-Policy `vuln.go.dev`, daher lokal nicht ausführbar.

## Phase 2 – DHCPv4-Kern

**Umgesetzt**
- `internal/scope`: Scopes, Exclusions, Reservierungen mit Validierung
  (Überlappung, Bereich im Subnetz, Exclusion im Bereich, eindeutige
  MAC/Client-ID/IP) und unveränderlichem In-Memory-Snapshot für die Engine.
- `internal/lease`: Lease-Manager (In-Memory-Index, Adressauswahl
  Reservierung > Angebot > bestehender Lease > Option 50 > frei > released >
  expired), Offer-Haltezeit, Release/Decline, Expiry-Worker mit
  Grace-Period, Group-Commit-Writer (ACK erst nach durablem Commit),
  ICMP-Konflikterkennung mit ARP-Selbstprüfung.
- `internal/dhcp4`: State Machine nach RFC 2131 (DISCOVER→OFFER, REQUEST in
  SELECTING/INIT-REBOOT/RENEWING/REBINDING → ACK/NAK, DECLINE, RELEASE,
  INFORM), Antwortadressierung nach RFC 2131 4.1 (Relay, ciaddr, Broadcast,
  L2-Unicast per AF_PACKET), Relay-Unterstützung (giaddr, Option 82 echo,
  Link-Selection 82.5, Subnet-Selection 118, Server-ID-Override 82.11),
  mehrere Scopes pro Server, Scope-Zuordnung per Interface-IP bzw. giaddr.
- Server: ein Socket pro Interface (`SO_BINDTODEVICE`, `IP_PKTINFO`),
  begrenzte Parallelität, automatisches Neu-Binden, Readiness-Check `dhcp4`.
- Config-Sektion `dhcp4` (Interfaces, Zeiten, Konflikterkennung).
- Test-Infrastruktur: `test/netns` (Namespaces, veth, `setns`),
  `test/dhcpclient` (Go-Client-Simulator für viele parallele Clients),
  `test/integration` (`make test-integration`), Fuzz-Test `make fuzz`.
- CI: zusätzlicher Job für die netns-Integrationstests.

**Akzeptanz** (`make test-integration`, alle grün)
- dhclient: Lease erhalten, Unicast-Renew verlängert den Lease, INIT-REBOOT
  nach Neustart bekommt dieselbe Adresse, Release ✅
- udhcpc: Lease mit Maske/Lease-Zeit/Server-ID, Hostname + Client-ID
  gespeichert, Release ✅
- Go-Client: Exclusions, Reservierung bevorzugt, Decline sperrt Adresse,
  Release, NAK für fremde Adresse und falsches Netz, INFORM, Leases
  überleben Neustart ✅
- Konflikterkennung: belegte Adresse wird übersprungen und als `declined`
  markiert ✅
- Relay (dnsmasq `--dhcp-relay`): Client im entfernten Subnetz bekommt Lease
  aus dem passenden Scope, Server-ID = Adresse Richtung Relay ✅
- 1000 parallele Clients: 1000 eindeutige Adressen, 1000 persistierte
  aktive Leases, keine Duplikate (≈ 1,5–3 s inkl. Konflikt-Ping) ✅
- Unit-Tests (Race-Detector) und Lint grün; Fuzzing ~1,2 Mio. Ausführungen
  ohne Fund.

**Hinweise**
- DHCP-Optionen außer Subnetzmaske, Lease-Zeit, T1/T2 und Server-ID folgen
  in Phase 3 (Router, DNS, … aus der `options`-Tabelle).

## Phase 3 – Optionen, Klassen, Policies, Filter

**Umgesetzt**
- `internal/dhcpopt`: Options-Katalog (u. a. 3, 6, 12, 15, 42, 43, 60, 66,
  67, 119, 121, 150, 249, 252) und typisierte Werte (IP, IP-Liste, String,
  uint8/16/32, int32, bool, Hex, Domain-Liste RFC 1035, Routen RFC 3442) mit
  Validierung, kanonischer Textform und Wire-Kodierung. Eigene
  Optionsdefinitionen für weitere Codes.
- Migration 00002: `classes`, `policies`, `policy_conditions`,
  `policy_ranges`, `option_definitions`, `mac_filters`, `settings`;
  `options` um Policy-Owner erweitert (Down-Migration getestet).
- `internal/scope`: CRUD für Optionswerte (Server/Scope/Policy/
  Reservierung), Klassen, Policies (inkl. Bedingungen, Bereichen,
  Lease-Dauer), MAC-Filter und Filter-Schalter; Snapshot enthält alles
  vorkodiert. Auswertung: Wildcard-Matching, Policy-Evaluierung (UND/ODER,
  `eq`/`ne`, Option 82, User-Class nach RFC 3004), Bereichsberechnung,
  Options-Vererbung, Filterentscheidung.
- Lease-Manager: Allokation über `Candidate` (Policy-Bereiche, Lease-Dauer
  der Policy), Cursor pro Bereich.
- DHCP-Handler: MAC-Filter vor jeder Verarbeitung, Policies pro Scope,
  Optionen nach Parameter Request List, PXE-Header-Felder, Größenlimit nach
  Option 57; Optionen auch bei INFORM.

**Akzeptanz**
- Tabellengetriebene Tests der Options-Auflösung (`TestOptionResolution`:
  Server < Scope < Server-Policy < Scope-Policy < Reservierung, Processing
  Order, ODER, `ne`, deaktivierte Policy, Option 82 per Hex) ✅
- PXE-Client bekommt Boot-Optionen: udhcpc mit `-V PXEClient…` erhält
  `tftp`, `bootfile`, `siaddr`, `boot_file`; Go-Client im PXE-Stil (Option
  60/93/97, PRL) erhält 66/67 + Header-Felder; normaler Client nicht ✅
- Gesperrte MAC bekommt keine Antwort, andere Clients werden bedient ✅
- Standardoptionen (Router, DNS, Domain, NTP) kommen bei udhcpc an ✅
- Unit-, Integrationstests und Lint grün.

## Phase 4 – Audit, Statistiken, Metriken

**Umgesetzt**
- `internal/audit`: Einträge (Zeit, Actor, Quelle, Aktion, Objekt, Details
  als JSON), Actor über `context`, Abfrage mit Filtern (Zeitraum, Actor,
  Aktions-Präfix, Objekt) und Keyset-Pagination, Purge.
- Alle Konfig-Mutationen (Scopes, Exclusions, Reservierungen, Optionen,
  Optionsdefinitionen, Klassen, Policies, Filter, Filter-Schalter) schreiben
  atomar einen Audit-Eintrag mit before/after.
- Lease-Events (assign, renew optional, release, decline, conflict, expire)
  im Audit-Log über den Group-Commit.
- Scope-Statistik (`lease.Manager.Stats`): Gesamt, ausgeschlossen, nutzbar,
  belegt, reserviert, gesperrt, frei, Auslastung %, Leases je Zustand.
- Prometheus: `leasorix_dhcp4_packets_received_total{type}`,
  `…_packets_sent_total{type}`, `…_packets_dropped_total{reason}`,
  `…_errors_total{kind}`, `…_request_duration_seconds{type}`,
  `leasorix_lease_events_total{event}`, `leasorix_lease_commit_duration_seconds`,
  `leasorix_lease_commit_batch_size`, `leasorix_scope_addresses{kind}`,
  `leasorix_scope_utilization_ratio`, `leasorix_leases{state}`.
- Retention: `audit.retention` + Purge-Job; optionale Logdatei mit
  Größenrotation (`log.file`, `max_size_mb`, `max_backups`).

**Akzeptanz**
- Audit-Einträge entstehen bei jeder Mutation: `TestEveryMutationIsAudited`
  prüft alle 22 Mutationsarten (genau ein Eintrag, Actor/Quelle aus dem
  Context), abgelehnte Operationen schreiben nichts ✅
- Metriken erscheinen in `/metrics`: App-Test (Scope-Gauges) und
  Integrationstest nach echtem DHCP-Verkehr (Paketzähler, Latenz,
  Lease-Events, Auslastung 0.1 bei 1/10 belegt) ✅
- Lease-Events landen mit Actor `dhcp` und Quelle `s0` im Audit-Log ✅

**Hinweise**
- Der HA-Status als Metrik kommt mit Phase 6 (HA existiert noch nicht).
- Audit-Abfrage per API/UI folgt in Phase 5.

## Phase 5 – REST-API, Auth, Web-UI

**Umgesetzt**
- OpenAPI-Spec (`internal/api/openapi.yaml`, ~50 Operationen) → generierte
  Go-Typen + chi-Server-Interface und TS-Typen; CI prüft Aktualität.
- `internal/auth`: Benutzer (argon2id), serverseitige Sessions mit
  Idle-/Absolut-Timeout, CSRF-Token, API-Tokens mit Rollen-Deckelung,
  Setup-Token für den Erststart, Login-Rate-Limit, Schutz des letzten Admins,
  Audit aller Auth-Ereignisse. Migration 00003 (`sessions`, `api_tokens`).
- REST-API `/api/v1` für Setup/Login/Logout/Me/Passwort, Tokens, Scopes
  (+Statistik, Exclusions), Reservierungen (+CSV-Import), Optionen
  (Server/Scope/Policy/Reservierung) und Definitionen, Klassen, Policies,
  MAC-Filter, Leases (Suche/Filter/Paging, löschen, in Reservierung
  umwandeln), Statistik, Audit (Filter, Keyset-Paging), Benutzer,
  Einstellungen, System-Info, HA-Status (ab Phase 6 vollständig).
- Middleware: Authentifizierung (Cookie/Bearer), zentrale RBAC-Tabelle,
  CSRF, Origin- und Content-Type-Prüfung, Body-Limits, Security-Header/CSP.
- HTTPS: selbstsigniert (Default), eigene Dateien oder aus.
- Web-UI (Deutsch, hell/dunkel, responsive): Login & Ersteinrichtung,
  Dashboard (Auslastung, Pakete/s, HA, letzte Ereignisse), Scopes (Liste,
  Assistent, Detail mit Übersicht/Ausschlüssen/Optionen/Reservierungen/
  Policies/Leases), Reservierungen (inline anlegen, Bearbeiten mit
  Optionen, CSV-Import), Leases (Suche/Filter/Paging, umwandeln, löschen),
  Policies/Klassen/Filter (Policy-Editor mit Bedingungen, Bereichen,
  Lease-Dauer, Optionen), Hochverfügbarkeit, Audit-Log, Benutzer &
  Einstellungen (Konto, API-Tokens, Benutzer, Server-Optionen,
  Optionsdefinitionen, System).
- Einbettung ins Binary (`make build`, Tag `ui`); `make build-noui` ohne
  Node.js.

**Akzeptanz**
- Kompletter Workflow nur über die UI (Playwright, `make test-e2e`):
  Ersteinrichtung → Scope per Assistent (inkl. Frontend-Validierung) →
  echter `udhcpc` im Client-Namespace bekommt eine Lease → Lease in der UI
  sichtbar → in Reservierung umgewandelt → im Audit-Log (admin / dhcp) ✅
- Weitere E2E-Tests: falsches Passwort, Read-only-Benutzer sieht keine
  Änderungsaktionen, Dark-Mode bleibt erhalten — 5/5 grün ✅
- Go-API-Tests: Setup, Login + Rate-Limit, Logout, CSRF/Origin/Content-Type,
  Rollen + API-Tokens, Validierungsfehler mit Feldern, CRUD aller Objekte,
  Leases, CSV-Import, Statistik, Header, RBAC-Tabelle ✅
- Frontend-Unit-Tests (Vitest) für die Validierung, Typecheck ✅

## Phase 6 – Hochverfügbarkeit

**Umgesetzt**
- `internal/ha`: Partnerschaft zweier Server in den Modi Load Balance
  (Hash(MAC), Prozent einstellbar, Default 50/50) und Hot Standby (Reserve
  Default 5 %); feste Aufteilung der freien Adressen; Zustandsautomat
  NORMAL / COMMUNICATION_INTERRUPTED / PARTNER_DOWN / RECOVER / RECOVER_DONE
  mit Heartbeats, Timeouts, MCLT (Default 1 h), Auto-Partner-Down
  (optional), manueller Partner-Down-Bestätigung und Split-Brain-Schutz
  (erst Abgleich, dann Vergabe).
- Peer-Protokoll über gRPC/Protobuf, abgesichert per mTLS (Zertifikate per
  UI oder `leasorixd ha certs`) oder TLS + kanalgebundenem Shared Secret.
- Lease-Replikation: synchron vor dem ACK (Default) oder asynchron,
  gebündelt; Abgleich der kompletten Tabelle nach Wiederverbindung mit
  deterministischer Konfliktauflösung (neuere Änderung; bei echten
  Konflikten späteres Ende, dann Node-ID). Migration 00004.
- Konfigurations-Replikation nach dem Single-Writer-Prinzip:
  Trigger-gepflegte Versionsnummer, Snapshot vom Config-Leader, Partner
  read-only (API 409, Banner in der UI); Benutzer/API-Tokens werden
  mitrepliziert.
- DHCP-Handler: Zuständigkeit je Zustand (gerichtete Renewals werden immer
  beantwortet), Lease-Manager: eigener Pool-Anteil, MCLT-Begrenzung,
  Kürzung bei fehlender Partner-Bestätigung.
- API/UI: HA-Status (Zustand, Partner, Modus, Konfiguration, Fehler,
  Zertifikatsablauf), Partner-Ausfall bestätigen, Zertifikats-Generator,
  Notfall-Export/-Import der Lease-DB (auch per CLI), Split-Scope-Wizard
  (80/20) mit Scope-Export/-Import (`leasorix-scope/v1`).
- Metriken `leasorix_ha_*`, Audit-Einträge für Zustandswechsel,
  Partner-Down, Config-Übernahme und Konflikte.
- `flock` auf das Datenverzeichnis (kein Doppelstart, sicherer Offline-Import).
- Doku: [HA.md](HA.md) (HA-Handbuch), DECISIONS D-043…D-052, ARCHITECTURE.

**Akzeptanz** (`make test-ha`, zwei echte `leasorixd` in Network Namespaces,
mTLS mit per CLI erzeugten Zertifikaten)
- Normalbetrieb: 5000 Clients, beide Server liefern (≈ 2500/2500), keine
  doppelte Adresse, beide Datenbanken identisch; ≈ 300 Leases/s inkl.
  ICMP-Konfliktprüfung und synchroner Replikation ✅
- Primary per SIGKILL beendet: der Standby hat alle Leases schon beim ACK
  (synchron), verlängert alle 40 Clients ohne Unterbrechung (Lease ≤ MCLT),
  bedient neue Clients aus der Reserve, geht nach Auto-Timeout in
  PARTNER_DOWN; der zurückkehrende Primary gleicht ab, beide NORMAL,
  Tabellen identisch ✅
- Netzwerk-Partition zwischen den Servern: beide vergeben nur aus ihrem
  Anteil (Lease ≤ MCLT), nach der Heilung konsistente Datenbanken ohne
  doppelte Adressen ✅
- Konfigurationsänderung am Leader nach ≈ 0,2 s am Partner sichtbar; der
  Partner lehnt Änderungen ab; der replizierte Admin kann sich am Partner
  anmelden ✅
- Unit-Tests: Zustandsautomat mit simulierter Zeit (9 Szenarien),
  Aufteilung, zwei Nodes im Prozess (Replikation, Partition, Takeover nach
  MCLT, falsches Secret, mTLS inkl. fremdem Knotenzertifikat),
  Config-Snapshot-Roundtrip, Merge-Regeln, API (Read-only, Partner-Down,
  Zertifikate, Export/Import, Split); Playwright-E2E 6/6 ✅

**Offene Punkte / Hinweise**
- Kein Raft-/Cluster-Modus für mehr als zwei Knoten (optional laut Plan,
  D-052).
- Die HA-Compose-Variante folgt in Phase 8.

## Phase 7 – Komfort & Kompatibilität

**Umgesetzt**
- **Superscopes** (Multinet): Migration 00005, CRUD in API/UI, Zuordnung
  im Scope, der DHCP-Handler bezieht alle Mitglieder des Links ein;
  Teil von HA-Replikation und Export/Import.
- **BOOTP** (optional): Antworten für reservierte BOOTP-Clients, dynamisches
  BOOTP pro Scope, eigene Lease-Dauer, 64-Byte-Vendor-Bereich, Bootserver/
  -datei im Header.
- **Konfigurations-Export/-Import** (YAML/JSON, `leasorix-config/v1`) in
  UI, API und `leasorixctl`: Modi Ergänzen/Ersetzen, Dry-Run, vollständige
  Validierung über eine temporäre Datenbank, alles-oder-nichts, Leases
  bleiben bei Ersetzen erhalten (Zuordnung per Adresse).
- **Importer für `netsh dhcp server dump`** (Windows): Scopes, Bereiche,
  Ausschlüsse, Reservierungen, Optionswerte, Optionsdefinitionen, Klassen,
  Superscopes, MAC-Filter; Warnungen für nicht übernommene Zeilen; UTF-16.
- **Backup/Restore** per UI, API, `leasorixd backup|restore` und
  `leasorixctl backup|restore`: konsistente Datenbank-Kopie im laufenden
  Betrieb, logischer Restore ohne Neustart inkl. Migration älterer Backups.
- **`leasorixctl`**: REST-CLI (generierter Client) für Status, Scopes,
  Leases, HA, Konfiguration, Backup; Tabellen- oder JSON-Ausgabe.
- Multicast-Scopes (MADCAP) bewusst nicht umgesetzt (optional, D-055).
- Doku: [ADMIN.md](ADMIN.md) (Admin-Handbuch), DECISIONS D-053…D-059.

**Tests**
- Unit: Superscope-Store, Snapshot, Handler (Superscope-Erweiterung,
  BOOTP-Antworten), Bundle/Export-Import-Roundtrip (YAML und JSON
  identisch), Merge-Konflikt ohne Teiländerung, Dry-Run, Replace mit
  Lease-Erhalt, netsh-Import (UTF-8 und UTF-16), Backup/Restore inkl.
  fremder/neuerer Dateien, API (Rollen, Content-Types, Session-Ende nach
  Restore), `leasorixctl` gegen einen echten In-Process-Server.
- Integration (netns, echte Pakete): Multinet über Superscope (dritter
  Client bekommt eine Adresse des zweiten Subnetzes mit dessen Router),
  BOOTP mit Reservierung und Lease-Dauer, kein dynamisches BOOTP ohne
  Freigabe ✅
- Playwright-E2E 7/7 (neu: Superscope anlegen/zuordnen, YAML-Export,
  netsh-Dry-Run mit Warnungen, Backup-Download) ✅
