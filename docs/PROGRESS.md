# Fortschritt

Kurze Notiz pro abgeschlossener Phase (siehe PLAN.md). Entscheidungen stehen
in [DECISIONS.md](DECISIONS.md).

| Phase | Status |
|---|---|
| 1 – Fundament | ✅ abgeschlossen |
| 2 – DHCPv4-Kern | ✅ abgeschlossen |
| 3 – Optionen, Klassen, Policies, Filter | ✅ abgeschlossen |
| 4 – Audit, Statistiken, Metriken | offen |
| 5 – REST-API, Auth, Web-UI | offen |
| 6 – Hochverfügbarkeit | offen |
| 7 – Komfort & Kompatibilität | offen |
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
