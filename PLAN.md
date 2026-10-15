# Leasorix – Plan für Claude Code

> Selbst gehosteter DHCP-Server mit dem Funktionsumfang eines Windows-DHCP-Servers,
> Docker/Compose-Deployment, Web-UI und Hochverfügbarkeit (mehrere Instanzen im selben Netz).

## 0. Arbeitsanweisung an Claude Code

- Arbeite die Phasen strikt der Reihe nach ab. Jede Phase hat Akzeptanzkriterien, erst wenn sie erfüllt sind, geht es weiter.
- Nach jeder Phase: Tests laufen lassen, `git commit` mit sinnvoller Message, kurze Notiz in `docs/PROGRESS.md`.
- Keine Features erfinden, die nicht im Plan stehen. Bei Unklarheit die konservative Variante wählen und in `docs/DECISIONS.md` festhalten.
- Alles, was Netzwerk-Raw-Sockets braucht, muss in Docker per `network_mode: host` oder macvlan funktionieren (siehe Phase 8).
- Teste niemals gegen das echte Produktivnetz. Entwicklung und Tests laufen in isolierten Docker-Netzen / Linux Network Namespaces.

## 1. Zielbild

| Bereich | Ziel |
|---|---|
| Protokoll | DHCPv4 vollständig (DISCOVER/OFFER/REQUEST/ACK/NAK/DECLINE/RELEASE/INFORM), später DHCPv6 |
| Verwaltung | Web-UI + REST-API, alles auch per Config-Import/Export |
| HA | Active/Active (Load Balance) und Hot Standby, mehrere Instanzen pro Netz |
| Betrieb | Ein Container-Image, Docker Compose, Healthchecks, Prometheus-Metriken |
| Persistenz | Leases und Konfiguration überleben Neustart und Container-Update |

Nicht-Ziele (vorerst): Active-Directory-Integration/Autorisierung, Windows-kompatibles Failover-Wire-Protokoll (wir bauen ein eigenes).

## 2. Tech-Stack (Vorschlag, Begründung in docs/DECISIONS.md festhalten)

- **Sprache:** Go (1.23+). Ein statisches Binary, gute Netzwerk-Libs, kleines Docker-Image.
- **DHCP-Paketebene:** `github.com/insomniacslk/dhcp` (dhcpv4/dhcpv6 Parsing), eigener Raw-Socket-/UDP-Server mit Interface-Binding (`SO_BINDTODEVICE`), damit Broadcast-Antworten auf dem richtigen Interface rausgehen.
- **Storage:** SQLite (WAL-Modus) via `modernc.org/sqlite` (kein CGO). Migrationen mit `golang-migrate` oder `goose`.
- **HA-Replikation:** Eigenes leichtgewichtiges Peer-Protokoll über mTLS-gesichertes gRPC (Details Phase 6).
- **API:** REST (OpenAPI-Spec zuerst schreiben), Router `chi`, Auth per Session-Cookie + API-Tokens.
- **Web-UI:** Vite + React + TypeScript + Tailwind, wird ins Go-Binary eingebettet (`go:embed`). Alternative: htmx + Templ, falls schlanker gewünscht.
- **Observability:** `slog` (JSON), Prometheus `/metrics`, `/healthz` und `/readyz`.
- **Tests:** `go test`, Integrationstests mit Docker/netns, Last-Tests mit `dhcpperf` (bzw. eigenem Go-Client).

## 3. Repo-Struktur

```
leasorix/
├── cmd/leasorixd/        # main
├── internal/
│   ├── dhcp4/              # Protokoll-Engine, State Machine, Option-Handling
│   ├── dhcp6/              # (Phase 10)
│   ├── scope/              # Scopes, Pools, Exclusions, Reservations, Policies
│   ├── lease/              # Lease-Store, Konfliktprüfung, Expiry-Worker
│   ├── ha/                 # Peer-Replikation, Failover-Logik
│   ├── dnsupdate/          # RFC-2136-Updates (Phase 9)
│   ├── api/                # REST-Handler + OpenAPI
│   ├── auth/               # Benutzer, Rollen, Tokens
│   ├── store/              # SQLite, Migrationen
│   └── metrics/
├── web/                    # Frontend (Vite/React)
├── deploy/
│   ├── Dockerfile
│   ├── docker-compose.yml
│   ├── docker-compose.ha.yml
│   └── examples/
├── docs/                   # PROGRESS.md, DECISIONS.md, ARCHITECTURE.md
├── test/                   # Integrations-/Lasttests, netns-Skripte
└── PLAN.md
```

## 4. Feature-Matrix (Windows-DHCP-Parität)

| Windows-DHCP-Feature | Leasorix | Phase |
|---|---|---|
| IPv4-Scopes (Range, Subnetz, Lease-Dauer) | ja | 2 |
| Exclusion Ranges | ja | 2 |
| Reservierungen (MAC/Client-ID → IP) | ja | 2 |
| Server-/Scope-/Reservierungs-Optionen (Gateway, DNS, Domain, NTP, …) | ja, mit Vererbung | 3 |
| Vendor Classes / User Classes | ja | 3 |
| Policies (Bedingungen auf MAC, Vendor Class, Option 82 → eigener Pool/Optionen) | ja | 3 |
| Superscopes / Multicast-Scopes | Superscope ja, Multicast optional | 7 |
| DHCP-Relay (giaddr), mehrere Subnetze pro Server | ja | 2 |
| Konflikterkennung (ICMP/ARP-Probe vor OFFER) | ja | 2 |
| Allow/Deny-Filter (MAC-Filter, Wildcards) | ja | 3 |
| BOOTP-Unterstützung | optional | 7 |
| PXE/Boot-Optionen (66, 67, 60, 43, Option 82 lesen) | ja | 3 |
| Lease-Verwaltung (anzeigen, löschen, in Reservierung umwandeln) | ja | 5 |
| Dynamische DNS-Updates + Name Protection | ja | 9 |
| Failover: Load Balance (Prozent-Split) und Hot Standby (Reserve-%) | ja, eigenes Protokoll | 6 |
| Audit-Log | ja | 4 |
| Statistiken (Auslastung je Scope) | ja | 4 |
| Rollenmodell (Admin / Operator / Read-only) | ja | 5 |
| Import/Export der Konfiguration (YAML/JSON, `netsh dhcp dump`-Import optional) | ja | 7 |
| DHCPv6 (Stateful, Prefix Delegation optional) | ja | 10 |

## 5. Phasen

### Phase 1 – Fundament
- Go-Modul, Linter (`golangci-lint`), CI-Skript (`make test lint build`).
- Config-Datei (YAML) + Env-Overrides, strukturierte Logs, Graceful Shutdown.
- SQLite-Store mit Migrationen: Tabellen `scopes`, `exclusions`, `reservations`, `options`, `leases`, `audit_log`, `users`.
- `/healthz`, `/readyz`, `/metrics` als Skeleton.

**Akzeptanz:** `make build` erzeugt Binary, `leasorixd --config x.yml` startet, Migrationen laufen idempotent, Unit-Tests grün.

### Phase 2 – DHCPv4-Kern
- Server-Loop pro Interface, Broadcast- und Unicast-/Relay-Handling (giaddr, Option 82 durchreichen).
- Vollständige State Machine: DISCOVER→OFFER, REQUEST (SELECTING/INIT-REBOOT/RENEWING/REBINDING)→ACK/NAK, DECLINE (IP sperren), RELEASE, INFORM.
- IP-Vergabe: Reservierung > bestehende Lease > freie IP aus Pool (Exclusions beachten), Lease-Zeiten T1/T2.
- Konflikterkennung per ICMP-Ping (konfigurierbar, an/aus, Timeout).
- Expiry-Worker (abgelaufene Leases freigeben, Grace-Period).
- Mehrere Scopes, Zuordnung per Interface-IP bzw. giaddr.

**Akzeptanz:** Integrationstest im netns mit `dhclient`/`udhcpc` und Go-Testclient: Lease erhalten, erneuern, freigeben, Decline, Reservierung wird bevorzugt, Relay-Szenario funktioniert, keine doppelten IPs bei 1000 parallelen Clients.

### Phase 3 – Optionen, Klassen, Policies, Filter
- Option-Vererbung: Server → Scope → Klasse/Policy → Reservierung (spezifischste gewinnt).
- Typed Options (IP, IP-Liste, String, Int, Bytes, Hex), Standardoptionen 1, 3, 6, 12, 15, 42, 43, 51, 60, 66, 67, 119, 121, 150 usw.
- Vendor/User Classes, Policies mit Bedingungen (UND/ODER, Wildcards, Option-82-Felder).
- MAC Allow/Deny-Listen.

**Akzeptanz:** Tabellengetriebene Tests für Option-Auflösung; PXE-Client bekommt Boot-Optionen, gesperrte MAC bekommt keine Antwort.

### Phase 4 – Audit, Statistiken, Metriken
- Audit-Log für alle Konfigurationsänderungen und Lease-Events (wer, was, wann).
- Statistiken je Scope: Pool-Größe, genutzt, frei, reserviert, Auslastung %.
- Prometheus-Metriken: Pakete pro Typ, Leases aktiv, Latenz, Fehler, HA-Status.
- Log-Rotation bzw. Retention-Einstellung.

**Akzeptanz:** Metriken erscheinen in `/metrics`, Audit-Einträge entstehen bei jeder Mutation.

### Phase 5 – REST-API, Auth, Web-UI
- **OpenAPI-Spec zuerst**, daraus Typen generieren.
- Auth: lokale Benutzer (argon2id), Sessions, API-Tokens, Rollen Admin/Operator/Read-only, Erststart-Setup (Admin anlegen). Optional OIDC später.
- Endpunkte für Scopes, Exclusions, Reservierungen, Optionen, Policies, Leases, Statistik, Audit, Benutzer, Einstellungen.
- Web-UI-Seiten:
  1. Dashboard (Auslastung, Pakete/s, HA-Status, letzte Events)
  2. Scopes (Liste, Detail, Wizard zum Anlegen, Exclusions, Optionen)
  3. Reservierungen (inline anlegen, CSV-Import)
  4. Leases (Suche/Filter, „in Reservierung umwandeln“, Lease löschen)
  5. Policies / Klassen / Filter
  6. HA / Cluster-Status und Failover-Aktionen
  7. Audit-Log
  8. Benutzer & Einstellungen
- Validierung im Backend UND Frontend (Überlappung von Scopes, IP im Subnetz, Exclusion in Range usw.).
- Dark/Light-Mode, responsive.

**Akzeptanz:** Kompletter Workflow nur über die UI möglich (Scope anlegen → Client bekommt Lease → in UI sichtbar). API-Tests und ein paar Playwright-E2E-Tests grün.

### Phase 6 – Hochverfügbarkeit / Mehrere Instanzen
Ziel: Mehrere Leasorix-Instanzen im selben Netz, ohne doppelte IPs und mit Ausfallsicherheit.

**Modi (pro Scope oder pro Relationship konfigurierbar):**
1. **Load Balance (Active/Active):** Beide antworten. Aufteilung deterministisch per Hash(MAC) → Partner (Standard 50/50, Prozent einstellbar). Kein „Offer-Delay-Rennen“.
2. **Hot Standby:** Ein Primary antwortet, Standby hält Reserve-Anteil (Standard 5 %) des freien Pools für Notfälle und übernimmt nach Ausfall.

**Replikation:**
- Peer-Verbindung per gRPC mit mTLS (Zertifikate per UI/CLI generierbar oder extern bereitgestellt) oder alternativ Shared-Secret + TLS.
- Konfiguration: Single-Writer-Prinzip – Änderungen laufen über den Primary (oder konfigurierten Config-Leader) und werden an Partner repliziert (Versionsnummer/Logical Clock). Partner im Read-only-Modus für Konfig, wenn er nicht Leader ist, mit klarer UI-Anzeige.
- Leases: Jede Lease-Änderung wird synchron repliziert, bevor das ACK rausgeht (konfigurierbar: `sync` Standard / `async` für Performance).
- Zustandsmaschine pro Relationship: `NORMAL`, `COMMUNICATION_INTERRUPTED`, `PARTNER_DOWN`, `RECOVER`, `RECOVER_DONE`. Heartbeats, Timeouts konfigurierbar.
- Bei `COMMUNICATION_INTERRUPTED`: weiter antworten nur mit eigenem Pool-Anteil, bestehende Clients erneuern (verlängern max. MCLT = Maximum Client Lead Time, Standard 1 h).
- Bei `PARTNER_DOWN` (manuell bestätigt oder nach Auto-Timeout): Übernahme des gesamten Pools nach Ablauf von MCLT.
- Resync nach Rückkehr: Lease-DB-Abgleich per Version/Timestamp, Konflikte deterministisch lösen (höherer Lease-Ende-Zeitpunkt + Peer-ID als Tiebreaker).
- **Split-Brain-Schutz:** Beim Reconnect zuerst Abgleich, dann erst normale Vergabe; Konflikterkennung per ICMP bleibt als zusätzliches Sicherheitsnetz aktiv.
- **N>2 Instanzen:** Erst Paare unterstützen, danach optional Cluster-Mode mit Raft (`hashicorp/raft`) als Alternative für 3+ Knoten, in `docs/DECISIONS.md` begründen und erst nach Abschluss des Paar-Modus angehen.

**Fallback-Optionen zusätzlich (einfach, für Notfälle):**
- „Split Scope 80/20“-Vorlage: zwei unabhängige Server mit getrennten Pool-Hälften ohne Replikation (klassischer Fallback), per Wizard in der UI erzeugbar.
- Notfall-Export/Import der Lease-DB per UI/CLI.

**Akzeptanz (netns/Compose-Tests):**
- Normalbetrieb: Beide Server liefern, keine Duplikate über 5000 Leases.
- Primary hart killen: Standby übernimmt, Clients erneuern ohne Unterbrechung.
- Netzwerk-Partition zwischen Servern: Beide bleiben im Rahmen ihres Anteils korrekt, nach Heilung konsistente DB, keine doppelten IPs.
- Konfig-Änderung am Leader erscheint binnen Sekunden am Partner.

### Phase 7 – Komfort & Kompatibilität
- Superscopes, optional BOOTP und Multicast-Scopes.
- Config-Export/-Import (YAML/JSON), Backup/Restore per UI und CLI (`leasorix backup/restore`).
- CLI `leasorixctl` (Scopes, Leases, Status) auf Basis der REST-API.
- Optional: Importer für Windows-`netsh dhcp server dump`-Ausgabe oder ISC-dhcpd.conf.

### Phase 8 – Docker & Compose
- Multi-Stage-Dockerfile (Node-Build fürs UI → Go-Build → `gcr.io/distroless/static` oder `alpine`), Non-root-User wo möglich, Capabilities `NET_BIND_SERVICE`, `NET_RAW` (Ping), `NET_ADMIN` nur falls wirklich nötig.
- **Netzwerk-Hinweis:** DHCP braucht Layer-2-Broadcast. Zwei unterstützte Varianten dokumentieren und testen:
  1. `network_mode: host` (einfachste Variante, Interface per Config binden)
  2. `macvlan`-Netzwerk mit fester IP je Container (Beispiel-Compose mitliefern)
  Bridge-Netzwerk ohne Relay funktioniert **nicht** zuverlässig, klar in der Doku erwähnen.
- Volumes: `/data` (SQLite, Zertifikate), Config per Bind-Mount oder Env.
- Healthcheck im Image, `restart: unless-stopped`.
- `docker-compose.yml` (Single) und `docker-compose.ha.yml` (zwei Instanzen mit macvlan, Beispiel-Zertifikatssetup).
- Image-Tags, `make docker`, optional GitHub Actions für Build & Push (GHCR).

**Akzeptanz:** `docker compose up -d` startet, UI erreichbar, Client im Test-VLAN bekommt Lease; HA-Compose mit zwei Containern funktioniert.

### Phase 9 – DNS-Integration
- RFC-2136-Updates (A/PTR) mit TSIG, konfigurierbar pro Scope: nie / bei Client-Wunsch / immer.
- Name Protection (kein Überschreiben von Namen anderer MACs), Cleanup bei Lease-Ablauf.
- Test gegen BIND/PowerDNS im Compose.

### Phase 10 – DHCPv6 (optional)
- Stateful DHCPv6 (SOLICIT/ADVERTISE/REQUEST/RENEW/REBIND/RELEASE/DECLINE/INFORMATION-REQUEST), IA_NA, DUID-basierte Reservierungen.
- Prefix Delegation optional.
- Gleiche Scope-/Policy-/UI-Konzepte wiederverwenden.

## 6. Sicherheit

- Default-Bind der Web-UI nur auf Management-Interface/localhost, TLS mit selbstsigniertem Zertifikat oder eigenem Zertifikat.
- Passwörter nur gehasht, Rate-Limit am Login, CSRF-Schutz, strikte Security-Header.
- DHCP-Rogue-Hinweis in der Doku (DHCP Snooping am Switch empfehlen).
- Peer-Kommunikation ausschließlich verschlüsselt und authentifiziert.
- Secrets nie ins Log, `govulncheck` und Dependency-Scan in der CI.

## 7. Teststrategie

- Unit: Option-Auflösung, Pool-Allokation, State Machine, HA-Zustandsautomat (mit simulierter Zeit).
- Integration: netns-Skripte mit echten DHCP-Clients, Relay-Setup mit `dnsmasq --dhcp-relay` oder `dhcrelay`.
- Chaos/HA: Container killen, Netzwerk per `tc`/`iptables` trennen, Zeit verschieben.
- Last: mindestens 5000 Clients, Lease-Rate und Latenz-Messung dokumentieren.
- Fuzzing: `go test -fuzz` auf dem Paket-Parser.

## 8. Definition of Done (Gesamtprojekt)

- [ ] Alle Phasen 1–9 abgeschlossen, Phase 10 optional
- [ ] Doku: README (Quickstart mit Compose), `docs/ARCHITECTURE.md`, Admin-Handbuch, HA-Handbuch, Netzwerk-/Docker-Hinweise
- [ ] Alle Tests und Linter grün, `govulncheck` ohne Findings
- [ ] Zwei-Knoten-HA-Test dokumentiert und reproduzierbar (`make test-ha`)
- [ ] Release-Image mit Version-Tag gebaut

## 9. Startbefehl für Claude Code
