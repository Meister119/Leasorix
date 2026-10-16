# Architektur- und Implementierungsentscheidungen

Format: kurze ADR-Einträge. Neue Entscheidungen werden unten angehängt, alte
nicht umgeschrieben, sondern ggf. durch einen neuen Eintrag ersetzt
("ersetzt D-xxx").

---

## D-001 – Sprache Go, Mindestversion 1.26, Toolchain 1.27.2

**Kontext:** PLAN.md schlägt Go 1.23+ vor. Die aktuellen Versionen der
benötigten Bibliotheken (`golang.org/x/sys`, `modernc.org/sqlite`,
`prometheus/client_golang`, …) verlangen mindestens Go 1.26.

**Entscheidung:** `go 1.26.0` als Mindestversion in `go.mod`,
`toolchain go1.27.2` (neueste stabile Version zum Projektstart) für Builds.
Go bleibt die Sprache: ein statisches Binary ohne CGO, gute Netzwerk-Libs,
kleines Image.

**Konsequenz:** golangci-lint muss mit Go ≥ 1.26 gebaut sein (CI nutzt
v2.14.0). Docker-Build-Stage nutzt `golang:1.27`.

## D-002 – Modulpfad

`github.com/meister119/leasorix` (entspricht dem GitHub-Repo). Umbenennung
wäre per `go mod edit -module` + Suchen/Ersetzen jederzeit möglich.

## D-003 – Bootstrap-Konfiguration (YAML/Env) vs. DHCP-Konfiguration (DB)

**Entscheidung:** Die YAML-Datei (`--config`) und `LEASORIX_*`-Variablen
enthalten nur prozessnahe Einstellungen (Datenverzeichnis, Listen-Adressen,
Logging, Interfaces, später HA-Peers/TLS). Scopes, Reservierungen, Optionen,
Policies usw. liegen ausschließlich in der Datenbank und werden über
REST-API/UI bzw. Import/Export (Phase 7) verwaltet.

**Begründung:** Eine einzige "Source of Truth" für DHCP-Konfiguration ist
Voraussetzung für die Konfig-Replikation im HA-Modus (Phase 6,
Single-Writer-Prinzip) und für das Audit-Log. Zwei konkurrierende Quellen
(Datei und UI) würden Konflikte erzeugen.

**Details Env-Overrides:** Name = `LEASORIX_` + YAML-Pfad in Großbuchstaben,
Ebenen mit `_` verbunden (`http.listen` → `LEASORIX_HTTP_LISTEN`).
String-Listen kommagetrennt, Dauern im Go-Format (`10s`, `1h`). Unbekannte
YAML-Schlüssel sind ein Fehler (Tippfehler sollen nicht still ignoriert
werden).

## D-004 – SQLite: modernc, WAL, synchronous=FULL, getrennte Pools

**Entscheidung:**
- Treiber `modernc.org/sqlite` (pure Go, kein CGO → statisches Binary).
- `journal_mode=WAL`, `foreign_keys=ON`, `busy_timeout=10s`.
- `synchronous=FULL`: Ein per ACK bestätigter Lease muss einen Stromausfall
  überleben, sonst droht nach einem Crash doppelte Vergabe. Performance wird
  in Phase 2 gemessen; falls nötig wird Group-Commit ergänzt, nicht die
  Durability gesenkt.
- Zwei Connection-Pools: Writer mit genau einer Verbindung und
  `BEGIN IMMEDIATE` (alle Schreibzugriffe prozessintern serialisiert, keine
  `SQLITE_BUSY`-Deadlocks), Reader-Pool mit `query_only=1`.

## D-005 – Migrationen mit goose (eingebettete SQL-Dateien)

`github.com/pressly/goose/v3` über die `Provider`-API (kein globaler
Zustand), Migrationen als `.sql` per `go:embed` im Binary. Migrationen laufen
bei jedem Start und sind idempotent. Wenige Abhängigkeiten, Down-Migrationen
möglich.

## D-006 – IPv4-Adressen als INTEGER in der Datenbank

**Entscheidung:** IPv4-Adressen werden als `INTEGER` (uint32-Wert)
gespeichert, die API liefert Strings.

**Begründung:** Bereichsprüfungen ("IP im Pool?", Überlappung von Scopes und
Exclusions), Sortierung und Statistiken lassen sich direkt in SQL ausdrücken.
Nachteil: in der rohen DB schlechter lesbar. DHCPv6 (Phase 10) bekommt
eigene Tabellen mit 16-Byte-BLOBs.

## D-007 – Management-Listener standardmäßig nur auf localhost

`http.listen` ist per Default `127.0.0.1:8067` (PLAN.md Abschnitt 6). Für
Zugriff von außen muss explizit eine Management-IP gesetzt werden. Ein
Listener bedient UI, REST-API, `/healthz`, `/readyz` und `/metrics`.

## D-008 – Kein `X-Forwarded-For`-Vertrauen

Die chi-Middleware `RealIP` wird nicht verwendet: sie übernimmt
`X-Forwarded-For` ungeprüft und würde das Login-Rate-Limit (Phase 5) und das
Audit-Log (Client-IP) fälschbar machen. Falls ein Reverse Proxy unterstützt
werden soll, kommt eine explizite Liste vertrauenswürdiger Proxys dazu.

## D-009 – YAML-Bibliothek `go.yaml.in/yaml/v3`

Offizieller Nachfolger des archivierten `gopkg.in/yaml.v3` (gleiche API,
gepflegt vom YAML-Org).

## D-010 – Linter-Profil

golangci-lint v2 mit `standard` + bodyclose, errorlint, gosec, misspell,
noctx, nolintlint, unconvert, copyloopvar, durationcheck, nilerr.
`gosec G115` (Integer-Konvertierung) ist deaktiviert, weil IPv4-Arithmetik
ständig zwischen `uint32`/`int64` konvertiert; die Wertebereiche werden in der
Validierung geprüft. In Tests sind `gosec` und `noctx` aus.

---

## D-011 – Socket-Modell DHCPv4

**Entscheidung:** Pro konfiguriertem Interface ein UDP-Socket auf
`0.0.0.0:67` mit `SO_BINDTODEVICE`, `SO_BROADCAST`, `SO_REUSEADDR` und
`IP_PKTINFO` (Zieladresse → Server-Identifier bei Unicast/Relay;
Quelladresse beim Senden = Server-Identifier). Antworten an Clients ohne
Adresse und ohne Broadcast-Flag gehen per `AF_PACKET`-Datagram-Socket
(Protokoll 0, empfängt nichts) direkt an `chaddr`/`yiaddr` (RFC 2131 4.1).
Ist das nicht möglich (kein `CAP_NET_RAW`, kein Ethernet), wird auf Broadcast
zurückgefallen – das erlaubt RFC 2131 ausdrücklich.

**Konsequenz:** Relays müssen über eines der konfigurierten Interfaces
erreichbar sein (Pakete auf anderen Interfaces sieht kein Socket).
Fehlt ein Interface beim Start, wird alle 5 s erneut gebunden; `/readyz`
meldet bis dahin "not listening".

## D-012 – Client-Identität und Reservierungs-Matching

Ein Client wird über Option 61 (Client Identifier) identifiziert, ohne
Option 61 über `chaddr` (RFC 2131 4.2). Gleiche MAC mit verschiedener
Client-ID gilt als verschiedene Clients (z. B. PXE vs. Betriebssystem).
Reservierungen matchen zuerst über die Client-ID, dann über die MAC.

## D-013 – Lease-Zustände, Zeiten und Defaults

| Zustand | Bedeutung |
|---|---|
| `active` | gebunden bis `expires_at` |
| `expired` | abgelaufen, wird `expired_grace` lang für denselben Client vorgehalten |
| `released` | vom Client freigegeben, Adresse sofort wieder vergebbar, Datensatz für Affinität |
| `declined` | DECLINE oder Konflikt, gesperrt bis `expires_at` |

- `expired_grace` = 4 h (Windows-Parität).
- `decline_time` = 24 h (Kea-Default "decline-probation-period"). Windows
  sperrt "BAD_ADDRESS" unbefristet; 24 h ist der Kompromiss zwischen
  Sicherheit und Pool-Verlust, konfigurierbar.
- Angebote (OFFER) werden nur im Speicher gehalten (`offer_timeout` 60 s),
  nicht persistiert: nach einem Neustart sind sie ohnehin wertlos.
- Lease-Zeit 0 = unbegrenzt (`0xffffffff`, ohne T1/T2). T1 = 50 %, T2 = 87,5 %.
- Pro Client und Scope gibt es genau eine aktive Bindung; ältere werden beim
  ACK einer neuen Adresse auf `released` gesetzt.

## D-014 – Reihenfolge der Adressauswahl

Reservierung > laufendes Angebot > bestehender Lease des Clients
(aktiv vor zuletzt genutzt) > angefragte Adresse (Option 50) > nie benutzte
Adresse (Cursor, Round-Robin) > freigegebene Adresse (älteste zuerst) >
abgelaufene Adresse anderer Clients (älteste zuerst, auch innerhalb der
Grace-Period). Die letzte Stufe greift nur bei erschöpftem Pool – die
Grace-Period ist eine Präferenz, kein harter Block, damit ein voller Pool
nicht an vergangenen Leases verhungert.

## D-015 – INIT-REBOOT/RENEW ohne eigenen Datensatz

RFC 2131 empfiehlt Schweigen, wenn der Server keinen Datensatz zum Client
hat. Leasorix bestätigt stattdessen (ACK), wenn die Adresse gültig, frei und
nicht reserviert ist, und legt den Lease an. NAK, wenn sie einem anderen
Client gehört, reserviert/ausgeschlossen ist oder im falschen Netz liegt.
Schweigen nur, wenn für das Link-Netz gar kein Scope existiert.

**Begründung:** Nach Datenverlust oder einem Neustart ohne Datensatz würde
"Schweigen" den Client seine Adresse unbemerkt weiterbenutzen lassen, während
wir sie als frei betrachten → Duplikate. Mit ACK wird die Bindung erfasst.
In Phase 6 (HA) wird das um die Pool-Zuständigkeit des Partners ergänzt.

## D-016 – Reservierungen

- Reservierte Adresse muss im Subnetz liegen (nicht Netz-/Broadcast-Adresse),
  nicht zwingend im dynamischen Bereich; sie wird nie dynamisch vergeben,
  auch nicht innerhalb einer Exclusion.
- Hat ein Client eine Reservierung, bekommt er ausschließlich diese Adresse.
  Ist sie gerade an einen anderen Client vergeben oder antwortet sie auf den
  Konflikt-Ping, bekommt der reservierte Client **kein** Angebot (Warnung im
  Log) statt einer dynamischen Ersatzadresse. Der andere Client erhält beim
  nächsten Renew ein NAK.

## D-017 – Scope-Regeln

Subnetze dürfen sich nicht überlappen, Präfix /8 bis /30, das Subnetz eines
bestehenden Scopes ist unveränderlich (wie bei Windows), Exclusions müssen im
Bereich liegen und dürfen sich nicht überlappen, Lease-Zeit ≥ 10 s oder 0.
Ein deaktivierter Scope macht keine Angebote mehr und beantwortet Renewals
mit NAK, damit Clients sich umorientieren.

## D-018 – Link-Auswahl bei Relays

Priorität: Option 118 (Subnet Selection, RFC 3011) > Relay-Sub-Option 5
(Link Selection, RFC 3527) > `giaddr`. Relay-Sub-Option 11 (Server
Identifier Override, RFC 5107) wird als Server-ID verwendet. Option 82 wird
unverändert zurückgegeben (RFC 3046), Option 61 ebenfalls (RFC 6842).

## D-019 – Konflikterkennung

Standardmäßig an (500 ms, 1 Versuch) – anders als Windows (Default aus), da
ein Duplikat teurer ist als die Verzögerung beim ersten Angebot. Geprüft
werden nur Adressen, die der Client nicht bereits aktiv hält; ein bereits
geprüftes Angebot wird nicht erneut gepingt. Antwortet die Adresse, wird der
ARP-Cache geprüft: kommt die Antwort vom anfragenden Client selbst, ist es
kein Konflikt. Konflikte werden als `declined` (für `decline_time`)
gespeichert. Raw-ICMP-Socket bevorzugt, Fallback auf unprivilegierten
ICMP-Socket, sonst wird die Funktion mit Warnung deaktiviert.

## D-020 – Persistenz der Leases: Group Commit

Ein ACK wird erst gesendet, wenn der Lease durable in SQLite steht. Alle
Schreibvorgänge laufen über eine Writer-Goroutine, die gleichzeitige
Anfragen zu einer Transaktion bündelt (ein `fsync` für viele Leases).
Entscheidungen fallen im Speicher unter einem Mutex; der Speicher darf dem
Datenträger nur in die sichere Richtung voraus sein (Adresse gilt schon als
belegt). Schlägt ein Batch fehl, werden die Anfragen einzeln wiederholt.

## D-021 – BOOTP vorerst ignoriert

Pakete ohne DHCP-Message-Type (BOOTP) werden in Phase 2 verworfen; BOOTP ist
in Phase 7 optional.

## D-022 – Integrationstests mit Network Namespaces

Die Tests unter `test/integration` (Build-Tag `integration`, root nötig)
bauen pro Test eigene Namespaces + veth-Paare. Der Go-Testclient erzeugt
seine Sockets per `setns` auf einem gesperrten OS-Thread direkt im
Client-Namespace; dieselbe Technik dient als `DialContext` für HTTP-Checks
in den Server-Namespace. Hinweis: busybox `udhcpc -q -R` sendet kein RELEASE
(beendet sich vor BOUND) – der Test beendet udhcpc daher per SIGTERM.

---

## D-023 – Optionsmodell

- Optionswerte werden typisiert gespeichert (`ip`, `ip-list`, `string`,
  `uint8/16/32`, `int32`, `bool`, `hex`, `domain-list`, `routes`) – in
  kanonischer Textform; die Wire-Kodierung entsteht beim Aufbau des
  Snapshots. Damit sind DB-Inhalte lesbar und Export/Import (Phase 7) trivial.
- Built-in-Katalog in `internal/dhcpopt` (RFC 2132 + 3397/3442/4833/8910,
  Cisco 150, Microsoft 249/252). Weitere Codes nur über eigene
  Optionsdefinitionen (Code + Name + Typ), z. B. für 224–254.
- Vom Server verwaltete Codes sind nicht konfigurierbar: 1 (aus dem Subnetz),
  51 (Lease-Zeit von Scope/Policy), 54, 58/59 (aus der Lease-Zeit), sowie die
  rein clientseitigen bzw. Relay-Optionen 50, 53, 55, 57, 61, 82, 118.
- Option 43 wird als `hex` gepflegt (herstellerspezifisch gekapselt).

## D-024 – Klassen wirken über Policies (keine klassen-qualifizierten Werte)

Windows kennt neben Policies auch Optionswerte, die direkt an eine
Vendor-/User-Class gebunden sind. Leasorix bildet Klassen als benannte
Definitionen (Typ vendor = Option 60, user = Option 77, Wildcard-Muster) ab,
die in Policy-Bedingungen referenziert werden (`field: class`). Optionen für
eine Klasse = Policy mit Bedingung auf die Klasse + Optionswerte an der
Policy. Das ist ausdrucksstärker (kombinierbar mit MAC/Option 82) und
vermeidet eine zweite, konkurrierende Vererbungsachse.

**Vererbung (spezifischste gewinnt):** Server → Scope → Server-Policies →
Scope-Policies → Reservierung. Innerhalb einer Ebene gewinnt die Policy mit
der niedrigsten Processing Order.

## D-025 – Policy-Semantik

- Bedingungen auf `mac`, `client_id`, `hostname`, `vendor_class`,
  `user_class`, `class`, `relay_circuit_id`, `relay_remote_id`,
  `relay_subscriber_id`; Operator `eq`/`ne`; Verknüpfung UND oder ODER auf
  oberster Ebene (keine verschachtelten Gruppen – konservativ, deckt die
  Windows-Fälle ab).
- Wildcards `*` und `?`, Vergleich case-insensitive. Für binäre Felder
  (Option 82, Client-ID) vergleicht ein Wert mit Präfix `0x` gegen die
  Hex-Darstellung.
- Option 77 wird nach RFC 3004 zerlegt; ist der Inhalt kein gültiges
  RFC-3004-Format (Windows-Clients), gilt er als ein einzelner Eintrag.
- **IP-Bereiche** nur für Scope-Policies. Adressen in einem Policy-Bereich
  bekommen ausschließlich passende Clients; nicht passende Clients bekommen
  den Rest des Scope-Bereichs. Ist der Bereich der passenden Policy(s)
  erschöpft, gibt es **kein** Ausweichen in den allgemeinen Bereich
  (die Trennung ist meist gewollt, z. B. VoIP).
- Lease-Dauer: erste passende Policy mit gesetzter Dauer (Scope-Policies
  vor Server-Policies, jeweils nach Processing Order), sonst Scope.
- Mindestens eine Bedingung ist Pflicht; referenzierte Klassen müssen
  existieren, eine verwendete Klasse kann nicht gelöscht werden, eine
  Umbenennung wird in die Bedingungen übernommen.

## D-026 – Auslieferung der Optionen

- Sendet der Client Option 55, werden nur angeforderte Optionen geliefert (in
  der Reihenfolge der Liste), sonst alle konfigurierten.
- Optionen 66/67 (bzw. 150) werden zusätzlich in die BOOTP-Header-Felder
  `sname`/`file`/`siaddr` gespiegelt, weil viele PXE-ROMs nur diese lesen.
- Überschreitet die Antwort die maximale Nachrichtengröße (Option 57, sonst
  576 Byte), werden Optionen vom Ende der Request-Liste entfernt und eine
  Warnung geloggt. Option Overload (52) wird nicht verwendet.

## D-027 – MAC-Filter

Allow- und Deny-Liste mit Wildcards, beide standardmäßig deaktiviert
(Schalter in `settings`). Deny gewinnt vor Allow. Ist die Allow-Liste aktiv,
werden nur gelistete MACs bedient. Gefilterte Clients werden für alle
Nachrichtentypen stillschweigend ignoriert (Log auf Info-Level).

---

## D-028 – Audit-Log: atomar mit der Änderung

- Konfigurationsänderungen schreiben ihren Audit-Eintrag **in derselben
  Transaktion** wie die Änderung (`scope.Store.rec`). Es gibt also weder
  Änderungen ohne Eintrag noch Einträge ohne Änderung; abgelehnte Operationen
  hinterlassen nichts. Details enthalten `before`/`after`.
- Lease-Events laufen als zusätzliche Ops durch den Group-Commit des
  Lease-Writers – gleiche Atomarität, kein zusätzlicher fsync.
- Wer: Actor + Quelle aus dem `context` (`audit.WithActor`). Die API
  (Phase 5) setzt dort Benutzer und Client-IP; DHCP-Ereignisse laufen als
  `dhcp` mit Quelle `<interface> [via <giaddr>]`, der Expiry-Worker als
  `system`.
- Lease-Renewals werden standardmäßig **nicht** auditiert
  (`audit.lease_renewals: false`): bei 5000 Clients und 1 h Lease-Zeit wären
  das ~10 000 Einträge pro Stunde. Zuweisung, Release, Decline, Konflikt und
  Ablauf werden protokolliert.
- Aufbewahrung `audit.retention` (Default 90 Tage, 0 = unbegrenzt), Purge
  beim Start und alle 6 h in Batches à 10 000 Zeilen (kurze Schreibsperren).

## D-029 – Scope-Statistik

Berechnet aus dem In-Memory-Lease-Index (O(Leases + Reservierungen), nicht
O(Poolgröße)): `usable` = Bereich − Exclusions; `in_use` = aktive Leases im
nutzbaren Bereich; `reserved` = ungenutzte Reservierungen im nutzbaren
Bereich; `declined` = gesperrte Adressen; `available` = Rest; Auslastung =
(usable − available) / usable. Policy-Bereiche werden nicht gesondert
ausgewiesen.

## D-030 – Metriken

Eigene Registry (kein globaler Zustand). Paketzähler je Nachrichtentyp
(empfangen/gesendet), Drops mit Grund (`filtered`, `no_scope`,
`no_address`, `other_server`, `not_ours`, `invalid`, `malformed`, `bootp`,
…), interne Fehler (`persist`, `send`), Verarbeitungsdauer als Histogramm,
Lease-Events, Commit-Dauer und Batch-Größe des Group-Commits. Scope- und
Lease-Gauges werden erst beim Scrape berechnet (Custom Collector), damit der
DHCP-Pfad nicht für Statistik bezahlt. Der HA-Status folgt mit Phase 6.

## D-031 – Logging-Ziel und Rotation

Default stderr (Container-Standard, Rotation übernimmt Docker/journald).
Optional `log.file` mit eingebauter Größenrotation (`max_size_mb`,
`max_backups`) ohne externe Abhängigkeit; jeder Log-Datensatz landet
vollständig in einer Datei.

---

## D-032 – OpenAPI zuerst, Code generiert

- Spezifikation `internal/api/openapi.yaml` (OpenAPI **3.0.3**, weil
  `oapi-codegen` 3.1 nur teilweise unterstützt).
- Go: `oapi-codegen` v2.8 (als `go tool` in `go.mod` gepinnt) erzeugt Modelle
  und das chi-`ServerInterface` (`internal/api/gen`). Die Handler
  implementieren das Interface; der Compiler erzwingt damit, dass jeder
  Endpunkt der Spec existiert.
- TypeScript: `openapi-typescript` erzeugt `web/src/api/schema.d.ts`, der
  Client ist `openapi-fetch` – Pfade, Parameter und Bodies sind typisiert.
- Generierter Code wird eingecheckt; die CI prüft per `git diff`, dass er zur
  Spec passt (`make generate`).

## D-033 – Authentifizierung und Sessions

- Passwörter: argon2id mit RFC-9106-Parametern (t=3, m=64 MiB, p=4) im
  PHC-Format; Parameter stehen im Hash und können später erhöht werden.
  Mindestlänge 10 Zeichen. Unbekannte Benutzer werden gegen einen
  Dummy-Hash geprüft (kein Timing-Orakel).
- Sessions serverseitig: Cookie `leasorix_session` (32 Byte Zufall), in der
  DB nur als SHA-256. Cookie `HttpOnly`, `SameSite=Strict`, `Secure` sobald
  TLS aktiv ist. Absolute Laufzeit 12 h, Inaktivität 1 h (konfigurierbar).
  Passwortwechsel, Deaktivierung und Rollenwechsel beenden Sessions.
- CSRF: Synchronizer-Token pro Session, Pflicht-Header `X-CSRF-Token` bei
  allen nicht-GET-Requests mit Cookie-Auth; zusätzlich Origin-Prüfung und
  Pflicht-`Content-Type: application/json` (Formulare können keine
  JSON-Requests ohne Preflight senden).
- API-Tokens `lxt_…` (nur SHA-256 gespeichert, einmalig angezeigt), optional
  mit Ablauf; Rolle ≤ Rolle des Benutzers, effektive Rolle = Minimum aus
  Token- und aktueller Benutzerrolle. Kein CSRF für Bearer-Tokens.
- Der letzte aktive Admin kann nicht gelöscht, deaktiviert oder herabgestuft
  werden; das eigene Konto kann man nicht löschen.
- OIDC ist im Plan "optional später" und noch nicht umgesetzt.

## D-034 – Erststart nur mit Setup-Token

Solange kein Benutzer existiert, kann der erste Admin nur mit einem
einmaligen Setup-Token angelegt werden, das beim Start im Log steht
(`setup_token=…`) oder per `auth.setup_token` vorgegeben wird. Ohne diesen
Schutz könnte jeder, der die UI vor dem Betreiber erreicht, die Instanz
übernehmen. Fehlversuche zählen ins Rate-Limit.

## D-035 – Rollenmodell

| Rolle | darf |
|---|---|
| `readonly` | alles lesen außer Benutzerverwaltung; eigenes Passwort, eigene (read-only) Tokens |
| `operator` | zusätzlich DHCP-Konfiguration ändern (Scopes, Reservierungen, Optionen, Policies, Klassen, Filter-Einträge) und Leases löschen/umwandeln |
| `admin` | zusätzlich Benutzer und `PUT /settings` (u. a. Aktivierung der MAC-Filterlisten) |

Die Prüfung erfolgt zentral in einer Middleware anhand Methode + Pfad (eine
Tabelle, getestet), nicht verstreut in den Handlern.

## D-036 – Login-Rate-Limit

Max. 10 Fehlversuche je 15 min pro Client-IP **und** pro Benutzername
(gleitendes Fenster, im Speicher), danach `429` mit `Retry-After`.
Fehlversuche landen im Audit-Log (`auth.login_failed`). Die Sperre pro
Benutzername ermöglicht theoretisch ein kurzzeitiges Aussperren eines
Kontos – bewusst in Kauf genommen (Management-UI im internen Netz, kurzes
Fenster, Admin kann per API-Token weiterarbeiten).

## D-037 – TLS standardmäßig an (selbstsigniert)

`http.tls.mode: self-signed` ist Default: beim ersten Start wird ein
ECDSA-P-256-Zertifikat (825 Tage, SANs localhost/Hostname/Listen-IP) nach
`<data_dir>/tls` geschrieben (Key 0600). Alternativ `files` (eigenes
Zertifikat) oder `off` (z. B. hinter einem Reverse Proxy). HSTS nur mit TLS.
TLS-Handshake-Fehler (Browser vor dem Akzeptieren des Zertifikats, Scanner)
werden nur auf Debug geloggt.

## D-038 – Strikte Security-Header

CSP ohne `unsafe-inline` (`script-src 'self'; style-src 'self'`), deshalb
kein Inline-Skript: die Theme-Erkennung liegt in `/theme.js`. Dazu
`X-Frame-Options: DENY`, `frame-ancestors 'none'`, `nosniff`,
`Referrer-Policy: no-referrer`, COOP/CORP, `Cache-Control: no-store` für die
API. `X-Forwarded-For` wird nicht ausgewertet (D-008). `/metrics` ist ohne
Login erreichbar (Prometheus-Scrape) – Schutz über die Bind-Adresse.

## D-039 – Web-UI

- Vite 8 + React 19 + TypeScript + Tailwind 4, TanStack Query, React Router 7.
  **TypeScript 5.9** statt 7.0: die native 7er-Linie ist neu, 5.9 ist mit dem
  gesamten Tooling erprobt (konservative Wahl).
- Sprache der Oberfläche: Deutsch (wie PLAN und Nutzer); i18n kann später
  ergänzt werden.
- Dark/Light: Class-Strategie, Default aus `prefers-color-scheme`, Auswahl
  wird in `localStorage` gemerkt.
- Einbettung über `go:embed` hinter Build-Tag `ui` (`make build`). Ohne Tag
  baut `go build ./...` ohne Node.js und liefert eine Hinweisseite – so muss
  kein Build-Artefakt eingecheckt werden.
- Validierung doppelt: `web/src/lib/validate.ts` spiegelt die Backend-Regeln
  (Präfix /8–/30, Host-Bits, Bereich im Subnetz, Überlappung, MAC-Muster)
  für sofortiges Feedback; das Backend prüft verbindlich.
- Der Scope-Assistent schlägt Bereich/Router nur als Platzhalter vor und
  übernimmt sie erst bei „Weiter“ in leere Felder (kein Überschreiben beim
  Tippen).

## D-040 – Reservierungs-Import (CSV)

Alles-oder-nichts in einer Transaktion; Fehler werden mit Zeilennummer
gemeldet. Pflichtspalten `scope` (Name oder ID) und `ip`, dazu `mac`
und/oder `client_id`; Trennzeichen `,` oder `;` (automatisch erkannt), BOM
wird toleriert. Jede importierte Zeile erzeugt einen Audit-Eintrag.

## D-041 – Lease-Aktionen per API/UI

„Lease löschen“ entfernt den Datensatz sofort (Adresse wieder frei, Audit
`lease.delete` mit dem Benutzer). „In Reservierung umwandeln“ übernimmt IP,
MAC und Hostname (Client-ID nur, wenn keine MAC bekannt ist).

## D-042 – End-to-End-Tests

Playwright gegen den echten Server in einem Network Namespace (Management-
veth zum Root-Namespace, DHCP-veth zum Client-Namespace), der Test startet
`udhcpc` im Client-Namespace (`test/e2e/run.sh`, `make test-e2e`). Lokal
wird das vorinstallierte Chromium verwendet, in der CI per
`playwright install`.

## D-043 – HA-Peer-Protokoll: gRPC mit Protobuf

Eigenes Protokoll (`internal/ha/pb/ha.proto`) über gRPC, wie im Plan
vorgesehen. Vier RPCs: `Heartbeat` (Status beider Seiten, auch
Verbindungsprüfung), `PushLeases` (Lease-Änderungen), `DumpLeases`
(Stream der ganzen Lease-Tabelle für den Abgleich), `PushConfig`
(Konfigurations-Snapshot). Jeder Server ist zugleich Server und Client;
Verbindung über eine feste Peer-Adresse (`ha.peer.address`). Generierter
Code wird eingecheckt (`make generate`; protoc 3.21 wie in Ubuntu 24.04,
`protoc-gen-go`/`protoc-gen-go-grpc` als `go tool` mit fester Version), die
CI prüft die Aktualität. Nachrichten bis 64 MiB, Backoff max. ein
Heartbeat-Intervall, damit die Verbindung nach einer Partition schnell
wiederkommt.

## D-044 – Absicherung der Peer-Verbindung

- **mtls** (Default): gemeinsame CA, das Knotenzertifikat trägt die Node-ID
  als DNS-Name. Der Client prüft den Server gegen `peer.id`, der Server den
  Client per `VerifyConnection` ebenso – ein gültiges Zertifikat derselben
  CA für einen anderen Knoten wird abgewiesen. TLS ≥ 1.3. Zertifikate per
  UI (`POST /ha/certificates`) oder CLI (`leasorixd ha certs`): ECDSA P-256,
  CA 10 Jahre, Knoten 5 Jahre; der CA-Schlüssel wird nicht gespeichert
  (neue Knoten = neues Bündel). Ablauf als Metrik, im UI und als Log-Warnung
  30 Tage vorher.
- **secret**: TLS 1.3 mit Wegwerf-Zertifikaten, danach – noch vor dem
  ersten gRPC-Byte – ein gegenseitiger Nachweis
  `HMAC-SHA256(secret, Label|Rolle|Node-ID|TLS-Exporter)`. Der Exporter-Wert
  ist an die TLS-Sitzung gebunden; ein Man-in-the-Middle hat zwei Sitzungen
  und kann den Nachweis weder fälschen noch weiterreichen. Secret ≥ 16
  Zeichen, bevorzugt aus `shared_secret_file`.

## D-045 – Aufteilung von Clients und Adressen

- Load Balance: zuständig für einen Client ist der Primary, wenn
  `SHA-256(MAC) mod 100 < load_balance_percent`, sonst der Secondary
  (Hash(MAC) laut Plan; deterministisch, kein Offer-Rennen).
- Jede freie Adresse gehört genau einem Server: Anteil per
  Weyl-Folge `frac(ip · φ⁻¹)` statt Hash – zusammenhängende Bereiche werden
  dadurch fast exakt im gewünschten Verhältnis geteilt, und die Zuordnung
  hängt nicht von der Konfiguration ab (Ausschlüsse/Reservierungen ändern
  nichts, kein Umverteilen bei Config-Änderungen).
- Load Balance: Adressanteil = Client-Anteil. Hot Standby: der Standby
  besitzt `standby_reserve_percent` (Default 5 %) der Adressen als Reserve,
  der Primary den Rest.
- Neue Adressen werden nur aus dem eigenen Anteil vergeben (auch bei
  Option 50 / INIT-REBOOT). Bestehende aktive Bindungen eines Clients
  dürfen immer verlängert werden, egal wem die Adresse gehört.
- Konservativ: statische Reserve statt dynamischem Pool-Rebalancing wie bei
  ISC-Failover. Folge: im Hot Standby kann der Primary die Reserve nicht
  nutzen.

## D-046 – Zustandsautomat

Zustände laut Plan: NORMAL, COMMUNICATION_INTERRUPTED (CI), PARTNER_DOWN
(PD), RECOVER, RECOVER_DONE. „Verbunden“ heißt: eigene Heartbeats werden
beantwortet **und** Heartbeats des Partners kommen an (beides innerhalb
`communication_timeout`) und die Einstellungen passen zusammen.

- Start immer in RECOVER (kein DHCP-Dienst), außer der Server war in PD.
  War die Lease-Tabelle vor dem Neustart aktuell (NORMAL/CI/RECOVER_DONE)
  und ist der Partner nach `communication_timeout` nicht erreichbar, geht er
  nach CI – nur wenn die Ausfallzeit kürzer als `auto_partner_down` war
  (sonst könnte der Partner inzwischen den ganzen Pool übernommen haben).
  Ein Server ohne vollständige Tabelle wartet auf den Partner oder eine
  manuelle Partner-Down-Bestätigung.
- RECOVER und RECOVER_DONE beantworten keine DHCP-Anfragen („erst Abgleich,
  dann Vergabe“). Der Abgleich dauert typisch unter einer Sekunde.
- Abgleich: jeder Server zieht die komplette Tabelle des Partners
  (`DumpLeases`) und merged sie (D-048). Damit er gilt, merkt sich der
  Partner Session-ID und Epoche: die Epoche steigt bei jedem
  Verbindungsverlust, die Session bei jedem Neustart. NORMAL erst, wenn
  beide Seiten gegen den jeweils aktuellen Stand abgeglichen haben.
- CI → PD nur per Operator (`POST /ha/partner-down`, nur ohne Verbindung)
  oder nach `auto_partner_down` (Default 0 = aus, konservativ: eine
  Netztrennung darf nicht automatisch zu doppelter Pool-Nutzung führen).
- PD bleibt bei Rückkehr des Partners aktiv, bis dieser RECOVER_DONE meldet.
  Sind beide in PD (Partition + Auto-Partner-Down), weicht der Secondary
  nach RECOVER aus. NORMAL bei Partner in PD → RECOVER.
- Nach einem fehlgeschlagenen synchronen Lease-Update geht der Server sofort
  nach CI (neue Epoche) und gleicht nach dem nächsten Heartbeat neu ab.
- Persistiert (`ha_state`, lokal): Zustand, PD-Zeitpunkt, „zuletzt lebendig“
  (alle 10 s).
- `/readyz` hängt nicht vom HA-Zustand ab (ein wartender Partner soll nicht
  von einem Orchestrator neu gestartet werden); der Zustand steht in
  `leasorix_ha_state` und in der UI.

## D-047 – Lease-Zeiten, MCLT und Replikation

- NORMAL + `replication: sync` (Default): eine neue/verlängerte Bindung wird
  erst zum Partner repliziert, dann lokal durabel gespeichert, dann ACK.
  Bestätigt der Partner nicht innerhalb `replication_timeout`, wird die
  Lease auf MCLT gekürzt gespeichert und vergeben (und der Server geht nach
  CI). Replikation läuft gebündelt (wie der Group-Commit).
- CI: neue und verlängerte Leases höchstens MCLT (Default 1 h).
- PD: volle Lease-Zeit; Adressen aus dem Partner-Anteil erst ab
  PD-Zeitpunkt + MCLT. Der zurückkehrende Partner gleicht vor jeder Vergabe
  ab (D-046), deshalb ist keine Begrenzung nötig.
- `async`: volle Lease-Zeit, Replikation im Hintergrund. Schwächere
  Garantie (bei einem Absturz können die letzten Änderungen fehlen; die
  ICMP-Konflikterkennung bleibt als Netz) – nur für Performance gedacht.
- Release, Decline, Konfliktsperren und Admin-Löschungen werden ohne Warten
  repliziert (sie wirken in die sichere Richtung). Ablauf/Purge macht jeder
  Server selbst, sie werden nicht repliziert.

## D-048 – Zusammenführen von Lease-Tabellen

`leases.updated_at` ist jetzt in Millisekunden, `leases.origin` nennt den
Knoten der letzten Änderung (Migration 00004). Für eine Adresse gewinnt
(`lease.Wins`, auf beiden Seiten identisch):

1. Datensätze desselben Clients: die neuere Änderung (dann späteres Ende,
   dann höhere Node-ID).
2. Verschiedene Clients, nur einer bindet die Adresse (aktiv oder
   abgelehnt und nicht abgelaufen): der bindende.
3. Beide binden (echter Konflikt, z. B. nach Split Brain): späteres
   Lease-Ende, dann höhere Node-ID (Plan). Konflikte werden geloggt, als
   `lease.ha_conflict` auditiert und gezählt.
4. Keiner bindet: die neuere Änderung.

Einschränkung: Admin-Löschungen werden repliziert, aber nicht als
Grabstein gespeichert; eine während einer Partition gelöschte Lease kann
beim Abgleich vom Partner zurückkommen (harmlos: sie läuft regulär aus).
Der Scope eines empfangenen Datensatzes wird über die Adresse bestimmt.

## D-049 – Konfigurations-Replikation (Single Writer)

- Leader ist fest konfiguriert (`ha.config_leader`, Default `primary`) und
  muss auf beiden Servern gleich sein; zwei Leader = Mismatch, keine
  Verbindung (beide arbeiten dann sicher in CI weiter).
- Ein Versionszähler (`config_version`) wird per SQLite-Trigger bei jeder
  Änderung einer Konfigurationstabelle erhöht – so entgeht kein Schreibpfad.
  Der Leader prüft ihn bei jedem Heartbeat (und sofort nach
  Scope-Änderungen), erzeugt bei Änderung einen Snapshot (JSON aller
  Konfigurationstabellen aus einer Lesetransaktion, SHA-256) und schickt
  ihn, solange der Partner einen anderen Hash meldet.
- Repliziert werden Scopes, Ausschlüsse, Reservierungen, Optionen,
  Definitionen, Klassen, Policies, MAC-Filter, Einstellungen sowie Benutzer
  und API-Tokens (Anmeldung an beiden Servern möglich). Lokal bleiben
  Leases, Audit-Log, Sessions und HA-Zustand. Token-Nutzung zählt nicht als
  Änderung.
- Übernahme auf dem Partner in einer Transaktion mit abgeschalteten
  Foreign Keys (sonst würde das Ersetzen der Scopes die Leases kaskadierend
  löschen), danach `foreign_key_check`; Leases außerhalb der neuen Scopes
  und Sessions geänderter/gelöschter Benutzer werden entfernt. Danach zieht
  der Partner die Leases neu (Scope-IDs können sich geändert haben). Der
  Lease-Abgleich des Partners startet erst, wenn seine Konfiguration dem
  Stand des Leaders entspricht.
- Partner read-only: die API lehnt Konfigurationsänderungen mit 409 und
  Hinweis auf den Leader ab (auch Passwort/Tokens, da repliziert); erlaubt
  bleiben Lease-Aktionen, Login/Logout, HA-Aktionen. Die UI zeigt ein Banner
  und blendet Bearbeiten-Aktionen aus. Fällt der Leader länger aus, wird
  der Partner per Konfigurationsdatei zum Leader gemacht.

## D-050 – Split Scope und Scope-Bundle

Fallback ohne Replikation („Split Scope 80/20“) wie bei Windows über
Ausschlüsse: dieser Server behält die ersten n % des dynamischen Bereichs
und schließt den Rest aus; für den zweiten Server entsteht ein
Scope-Bundle mit dem lokalen Teil als Ausschluss. Bereits vorhandene
Ausschlüsse werden berücksichtigt (nur Lücken werden ergänzt). Das
Bundle-Format `leasorix-scope/v1` (Scope, Ausschlüsse, Reservierungen mit
Optionen, Scope-Optionen, Scope-Policies; Bezüge über Inhalte statt IDs)
dient auch als Baustein für den Konfigurations-Export in Phase 7. Import
ist alles-oder-nichts (bei Fehlern wird der angelegte Scope wieder
entfernt). Bei aktiver HA-Partnerschaft wird der Wizard nicht angeboten.

## D-051 – Notfall-Export/-Import der Lease-Datenbank

Format `leasorix-leases/v1` (JSON). Import = Merge nach D-048 (idempotent),
über die API werden geänderte Datensätze zum Partner repliziert. CLI:
`leasorixd leases export` liest direkt aus der Datenbank (auch im
laufenden Betrieb), `leasorixd leases import` nur bei gestopptem Dienst –
dafür hält `leasorixd` jetzt einen exklusiven Lock (`flock`) auf
`<data_dir>/leasorixd.lock`; das verhindert auch zwei Daemons auf
demselben Datenverzeichnis.

## D-052 – Kein Raft / keine Cluster mit mehr als zwei Knoten

Der Plan nennt einen Raft-Cluster-Modus als optionale spätere Alternative,
erst nach dem Paar-Modus. Er ist nicht umgesetzt: der Paar-Modus deckt die
Windows-Failover-Parität ab, ein Raft-Log für Leases würde jede Vergabe an
ein Quorum binden (mehr Latenz, ein dritter Knoten nötig). Bei Bedarf als
eigenes Vorhaben nach Phase 9.

## D-053 – Superscopes

Eigene Tabelle `superscope_members` (Scope gehört höchstens einem
Superscope; Löschen auf beiden Seiten kaskadiert sauber, keine Spalte mit
Fremdschlüssel in `scopes`, damit die Down-Migration ohne Tabellenneubau
auskommt). Der DHCP-Handler ermittelt die Scopes des Links wie bisher
(Interface-Adressen bzw. giaddr/118/82.5) und hängt die übrigen Mitglieder
ihrer Superscopes an (Reihenfolge: gefundener Scope zuerst, dann nach
Netzadresse). Als Server-Identifier dient weiter eine Adresse des Interfaces.
Superscopes werden wie alle Konfigurationstabellen per HA repliziert und
sind Teil von Export/Import (Bezug über den Namen).

## D-054 – BOOTP

Optional laut Plan, umgesetzt wie bei Windows: Clients mit Reservierung
bekommen immer eine BOOTREPLY, dynamisches BOOTP aus dem Pool nur bei
Scopes mit `bootp`. Lease-Dauer global `dhcp4.bootp_lease_time` (Default 30
Tage wie Windows); der Lease wird normal gespeichert, repliziert und läuft
ab (BOOTP-Clients erneuern nicht). Antworten enthalten keine DHCP-Optionen
(53, 51, 54 …); der 64-Byte-Vendor-Bereich (RFC 951/1497) wird nach Priorität
gefüllt (Maske, Router, DNS, Hostname, Domäne, …), Option 66/67 landen in
`sname`/`file`/`siaddr`. Policies und HA-Zuständigkeit gelten wie bei DHCP.

## D-055 – Keine Multicast-Scopes (MADCAP)

Multicast-Scopes sind laut Plan optional. MADCAP (RFC 2730) ist ein eigenes
Protokoll (UDP 2535) mit sehr geringer Verbreitung; nicht umgesetzt. Der
netsh-Importer meldet solche Zeilen als nicht übernommen.

## D-056 – Konfigurations-Export/-Import

- Format `leasorix-config/v1`, YAML (Default) oder JSON mit identischen
  Schlüsseln; Bezüge über Namen statt IDs, damit Dateien zwischen Servern
  übertragbar sind. Die Scope-Einträge sind Scope-Bundles (D-050).
- Benutzer und API-Tokens gehören nicht dazu (Geheimnisse); dafür gibt es
  Backup/Restore.
- Import über eine temporäre Datenbank: aktuelle Konfiguration hineinkopieren
  (`merge`) oder leer starten (`replace`), Dokument über die normalen
  Store-Methoden anwenden (alle Validierungen), Ergebnis in einer
  Transaktion in die echte Datenbank übernehmen. So ist der Import
  alles-oder-nichts, ohne jede Validierung doppelt zu implementieren. Ein
  Audit-Eintrag `config.import` statt Tausender Einzeleinträge.
- Leases werden danach dem Scope zugeordnet, dessen Subnetz ihre Adresse
  enthält (IDs können sich ändern); das gilt jetzt allgemein auch im
  Lease-Manager (`ScopesChanged`) und beim HA-Config-Sync.
- Dry-Run validiert gegen den aktuellen Stand ohne Änderung. Import nur für
  Admins, auf einem HA-Partner gesperrt (read-only).

## D-057 – Importer für `netsh dhcp server dump`

Der Plan nennt netsh oder ISC-dhcpd.conf als optionalen Importer; gewählt
wurde netsh (Windows-Parität ist das Ziel des Projekts, Umsteiger kommen
typischerweise von Windows). Der Konverter erzeugt ein
`leasorix-config/v1`-Dokument, das wie jeder Import geprüft wird. UTF-16
(PowerShell-Umleitung) wird erkannt. Standard-Optionsdefinitionen und die
eingebauten Windows-Klassen werden übersprungen; Optionswerte werden gegen
den Optionstyp validiert und bei Fehlern mit Warnung ausgelassen statt den
Import abzubrechen. Nicht übernommene Zeilen (klassenspezifische Werte,
DNS-Updates, Multicast, DHCPv6, Server-Einstellungen ohne Gegenstück)
werden mit Zeilennummer bzw. zusammengefasst gemeldet.

## D-058 – Backup und Restore

- Backup = vollständige SQLite-Kopie per `VACUUM INTO` über eine eigene
  Verbindung (konsistent, blockiert keine Schreiber). Enthält
  Passwort-Hashes → nur Admins.
- Restore ist logisch: das hochgeladene Backup wird in eine temporäre Datei
  geschrieben, geprüft (SQLite-Header, Leasorix-Tabellen, Schemaversion –
  neuere Versionen werden abgelehnt), Trigger/Views daraus entfernt (eine
  präparierte Datei soll beim Lesen nichts ausführen), auf das aktuelle
  Schema migriert und Konfiguration, Benutzer, API-Tokens und Leases in einer
  Transaktion übernommen. Audit-Log und HA-Zustand bleiben lokal, alle
  Sitzungen enden. Danach werden Scope-Snapshot und Lease-Manager neu
  geladen – kein Neustart nötig.
- Der Plan nennt `leasorix backup/restore`; umgesetzt als
  `leasorixd backup|restore` (das Daemon-Binary besitzt die Datenbank;
  `restore` offline mit Datenverzeichnis-Lock) und `leasorixctl
  backup|restore` über die API.

## D-059 – `leasorixctl`

Eigenes Binary auf Basis des aus der OpenAPI-Spec generierten Go-Clients
(`client: true` im oapi-codegen-Config). Authentifizierung nur per API-Token
(keine Sessions/CSRF), Konfiguration über Flags oder `LEASORIX_URL`,
`LEASORIX_TOKEN`, `LEASORIX_CA`/`LEASORIX_INSECURE`, Ausgabe als Tabelle
oder JSON (`-o json`). Destruktive Befehle (`ha partner-down`, `restore`)
verlangen `--yes`.

## D-060 – Container-Image

- Multi-Stage-Build: UI mit Node, beide Binaries mit Go (statisch,
  `CGO_ENABLED=0`, `-trimpath`), Laufzeit-Image
  `gcr.io/distroless/static-debian12:nonroot` (keine Shell, kein
  Paketmanager, ca. 50 MB). Alpine wäre die Alternative gewesen; distroless
  hat die kleinere Angriffsfläche und reicht, weil nichts im Image eine Shell
  braucht.
- Die Build-Stages laufen auf `$BUILDPLATFORM` und kompilieren für
  `$TARGETARCH` – Multi-Arch (amd64/arm64) ohne QEMU.
- Der Healthcheck ist ein Unterkommando (`leasorixd healthcheck`, fragt
  `/readyz`), weil distroless kein `curl` hat. `leasorixctl` ist im Image
  enthalten.
- Basis-Images sind Build-Argumente (Registry-Mirror), eine Proxy-CA kann als
  optionales BuildKit-Secret übergeben werden und landet nicht im Image.
- Unbekannte Argumente an `leasorixd` sind jetzt ein Fehler, statt
  stillschweigend den Server zu starten (`leasorixd version` war vorher
  ein Serverstart); `version` gibt es zusätzlich zu `--version`.

## D-061 – Rechte über File-Capabilities, nonroot

Docker vererbt einem nicht-root-Benutzer keine Capabilities (keine
Ambient-Capabilities). Damit `leasorixd` als UID 65532 Port 67 binden und
`AF_PACKET`/ICMP nutzen kann, bekommt das Binary die File-Capabilities
`cap_net_bind_service,cap_net_raw=ep`. Gesetzt werden sie von einem kleinen
Go-Helfer (`deploy/docker/setcap`, schreibt `security.capability` direkt),
damit die Build-Stage weder `libcap` noch Paketquellen braucht. `NET_ADMIN`
ist nicht nötig. Folgen (verifiziert, in DOCKER.md dokumentiert):

- Beide Capabilities müssen im Bounding-Set des Containers bleiben, sonst
  verweigert der Kernel `execve` – auch für Einmal-Kommandos.
- `no-new-privileges` ist verträglich: der Kernel begrenzt File-Capabilities
  dann auf das bisherige Permitted-Set, das Docker aus `cap_add` füllt.
- Root im Container ist keine Alternative: mit `cap_drop: [ALL]` fehlt root
  der Schreibzugriff auf `/data` (UID 65532).

## D-062 – Compose-Varianten und Defaults

- Drei Dateien statt Profilen: `docker-compose.yml` (Host-Netzwerk, die
  einfachste funktionierende Variante), `docker-compose.macvlan.yml` und
  `docker-compose.ha.yml` (macvlan, zwei Container). Ein Bridge-Netz wird
  nicht angeboten: ohne L2-Zugang erreichen Broadcast-DISCOVERs den
  Container nicht; Bridge + Relay ist nicht getestet und daher nicht
  dokumentiert als unterstützt.
- Sichere Defaults: `cap_drop: [ALL]` + die zwei nötigen Capabilities,
  `read_only` mit `tmpfs: /tmp`, `no-new-privileges`, `restart:
  unless-stopped`. Im Host-Modus lauscht die UI per Default nur auf
  `127.0.0.1` – die Management-Adresse muss bewusst gesetzt werden.
- Das HA-Compose erzeugt die mTLS-Zertifikate mit einem Einmal-Dienst
  (`ha certs --if-missing`) in einem gemeinsamen Volume; beide Server warten
  per `service_completed_successfully` darauf. Beide Container auf einem
  Host sind als Test-/Einstiegsvariante gedacht, die Verteilung auf zwei
  Hosts ist dokumentiert.
- Der Test (`make test-docker`) nutzt veth-Paare und Network Namespaces als
  Client-LAN, nie das echte Netz (PLAN-Regel).

## D-063 – Image-Veröffentlichung

`release.yml` baut mit Buildx für `linux/amd64` und `linux/arm64` und
veröffentlicht mit dem `GITHUB_TOKEN` in der GitHub Container Registry
(`ghcr.io/<owner>/leasorix`): Tags `v1.2.3` → `1.2.3`, `1.2`, `latest`;
`main` → `edge`; Pull Requests bauen nur. Kein Docker Hub (eigener
Account/Secret nötig, Rate-Limits). Der CI-Workflow hat zusätzlich einen
Job, der das Image baut und `make test-docker` ausführt.

## D-057 (Nachtrag Phase 9) – netsh `set dnsconfig`

Der netsh-Importer übernimmt jetzt `set dnsconfig Enable Update Lookup
NonDyn` auf Server- und Scope-Ebene (Server-Einstellung gilt für Scopes ohne
eigene): `Enable=0` → nie, `Update=1` oder `NonDyn=1` → immer, sonst „auf
Anforderung des Clients“; `Lookup` → Records beim Lease-Ende entfernen.
`NonDyn` (Updates auch für Clients ohne Option 81) hat in Leasorix kein
eigenes Gegenstück; „immer“ ist die nächstliegende Abbildung. Zonen und
Schlüssel stehen nicht im Dump (Warnung).

## D-064 – DNS-Updates: Architektur

- Bibliothek `github.com/miekg/dns` für RFC-2136-Nachrichten und TSIG statt
  eigener DNS-Kodierung (verbreitet, stabil, unterstützt alle
  TSIG-Algorithmen und den DHCID-Typ).
- Der DHCP-Pfad wartet nie auf DNS: Beim ACK berechnet der Handler nur die
  Entscheidung (Name, A/PTR, Antwort-Option 81) und übergibt sie dem
  Updater (`Bound`, nur Speicher). Ein Hintergrund-Worker sendet die Updates
  (bis zu 4 parallel) mit Wiederholungen (30 s, verdoppelnd bis 1 h).
- Zustand pro Adresse in `dns_registrations` (gewollt / tatsächlich
  eingetragen, getrennt nach A mit DHCID und PTR). Damit überleben
  ausstehende Löschungen einen Neustart, und Umbenennungen entfernen erst den
  alten Namen. Die Tabelle ist lokal (nicht repliziert, nicht im Export).
- Lease-Ende (Release, Ablauf, Löschen, Scope entfernt, Restore) erkennt ein
  periodischer Abgleich (alle 15 s) gegen die Lease-Tabelle statt Hooks an
  jeder Stelle des Lease-Managers – ein Mechanismus für alle Fälle, auch für
  während einer Downtime abgelaufene Leases. Löschversuche werden nach 12
  Fehlschlägen aufgegeben (geloggt).
- Zonen und Schlüssel liegen im Scope-Snapshot; HA-Config-Sync und Restore
  laden sie ohne eigenen Mechanismus neu.

## D-065 – DNS-Updates: Namen und Modi

- Modi wie Windows: nie / auf Anforderung des Clients / immer. „Auf
  Anforderung“ folgt RFC 4702 (Option 81: S=1 → A+PTR, S=0 → nur PTR, N=1 →
  nichts); ohne Option 81 keine Registrierung. „Immer“ registriert A+PTR für
  jeden Client mit Namen (Option 81 oder 12) und setzt in der Antwort das
  O-Bit, wenn es den Wunsch des Clients übersteuert.
- Der Name ist immer das erste (bereinigte) Label des Client-Namens plus die
  Scope-Domäne (Fallback: Option 15). Vollqualifizierte Client-Namen in
  anderen Zonen werden nicht übernommen – ein Client soll sich nicht in
  beliebige Zonen eintragen können, für die der Server Schreibrechte hat.
- Option 81 wird in OFFER und ACK beantwortet (RCODE1/2 = 255, Kodierung wie
  vom Client), auch im Modus „nie“ (dann mit N-Bit). Option 81 ist dadurch
  „managed“ und nicht mehr als Optionswert konfigurierbar. Fehlt Option 12,
  übernimmt die Lease den Namen aus Option 81.
- Neue Scopes haben „nie“ als Standard (Windows: „auf Anforderung“), damit
  ein Update auf diese Version oder das Anlegen einer Zone keine Updates in
  bestehenden Scopes auslöst. TTL automatisch ein Drittel der Lease-Dauer
  (1 min … 1 Tag).
- BOOTP-Clients werden nicht registriert.

## D-066 – Namensschutz mit DHCID

Name Protection nach RFC 4701/4703 (DHCID-Record mit SHA-256 über
Client-Kennung und Namen; Client-ID, DUID nach RFC 4361 oder
Hardware-Adresse): Ein Name wird nur angelegt, wenn er frei ist, oder
aktualisiert, wenn er die eigene DHCID trägt; manuell angelegte Namen und
Namen anderer Clients bleiben unangetastet. Löschen ebenfalls nur mit
passender DHCID; PTR-Records werden nur gelöscht, wenn sie noch auf den
eigenen Namen zeigen. Standard **an** (Windows: aus) – die sichere Variante;
pro Scope abschaltbar (dann ersetzt Leasorix vorhandene A-Records). Ein
Konflikt wird geloggt, auditiert (`dns.conflict`) und erst bei der nächsten
Verlängerung des Leases erneut versucht, nicht in einer Schleife. Weil die
DHCID nur vom Client abhängt, können beide HA-Server denselben Namen
aktualisieren.

## D-067 – TSIG-Schlüssel

- Das Geheimnis wird im Klartext in der Datenbank gespeichert – für die
  HMAC-Berechnung nötig (anders als Passwörter, die nur gehasht werden). Die
  API gibt es nie aus (nur schreiben), Änderungen nur durch Admins, Audit
  ohne Geheimnis.
- Schlüssel sind wie Benutzer nicht Teil des Konfigurations-Exports (Zonen
  verweisen per Name darauf), werden aber per HA repliziert und sind im
  Backup enthalten.
- Algorithmen: HMAC-SHA256 (Standard), -SHA384/-512/-224 sowie -SHA1/-MD5
  für ältere Server; Mindestlänge 16 Byte. Zonen ohne Schlüssel sind
  möglich (unsignierte Updates), weil Windows-DNS kein TSIG beherrscht;
  GSS-TSIG (Kerberos) ist nicht umgesetzt.

## D-068 – Korrektur HA: gekürzte Leases nachreplizieren

Gefunden beim Phase-9-Testlauf (sporadisch roter Unit-Test). Bestätigt der
Partner eine synchrone Replikation nicht rechtzeitig, kürzt der vergebende
Server den Lease auf MCLT – der Partner kann die lange Fassung aber trotzdem
gespeichert haben. Bisher blieb sie dort stehen (gleicher Änderungsstempel,
längere Laufzeit gewinnt), die Tabellen divergierten. Jetzt bekommt die
gekürzte Fassung einen strikt neueren Stempel und wird nachrepliziert
(asynchron, bei Ausfall per Resync), sodass beide Server dieselbe Laufzeit
kennen.

**Nachtrag (Phase 10):** Unter Last trat der Fehler weiter sporadisch auf.
Ursache: Nach dem Timeout markiert der Server die Verbindung als gestört
(neue Epoch); der Partner zieht daraufhin die Tabelle – unter Umständen,
bevor die gekürzte Fassung gespeichert ist. Die anschließende Nachreplikation
wurde verworfen („nicht verbunden, der Partner zieht ohnehin alles“). Jetzt
erhöht ein Server seine Epoch auch dann, wenn er eine Änderung mangels
Verbindung nicht senden kann; der Partner zieht die Tabelle erneut, und der
Rückweg nach NORMAL wartet, bis er die aktuelle Epoch abgeglichen hat. Das
schließt das Fenster für alle Änderungen während einer Unterbrechung, nicht
nur für gekürzte Leases.


## D-069 – DHCPv6: Umfang

Phase 10 setzt stateful DHCPv6 nach RFC 8415 um: SOLICIT/ADVERTISE,
REQUEST, RENEW, REBIND, RELEASE, DECLINE, CONFIRM, INFORMATION-REQUEST,
Rapid Commit, Server-Präferenz, IA_NA und – weil es im Plan als optional
genannt ist und sich mit demselben Allokator ergibt – IA_PD
(Präfix-Delegation). Relays werden auch mehrstufig unterstützt
(RELAY-FORW/-REPL, Interface-ID, Client-Link-Layer-Adresse nach RFC 6939).
Nachrichten-Kodierung mit `github.com/insomniacslk/dhcp/dhcpv6` (dieselbe
Bibliothek wie für DHCPv4).

Bewusst **nicht** umgesetzt (keine erfundenen Features, Aufwand ohne
Anforderung): Reconfigure (RFC 8415 Kap. 18.2.11, braucht Authentisierung),
Server-Unicast-Option (Clients müssen Multicast nutzen; direkte Unicasts
werden mit `UseMulticast` beantwortet), Leasequery, DNS-Updates (AAAA/PTR in
`ip6.arpa`), Policies/Klassen für IPv6, Lease-Export/-Import für DHCPv6 und
die Übernahme von DHCPv6-Scopes aus dem netsh-Dump. Router-Advertisements
sendet Leasorix nicht – das bleibt Aufgabe des Routers.

## D-070 – Eigenes Datenmodell für IPv6

IPv6 bekommt eigene Tabellen (`scopes6`, `exclusions6`, `reservations6`,
`options6`, `leases6`) und Pakete (`scope6`, `lease6`, `dhcp6`,
`dhcpopt6`, `ip6`) statt einer Verallgemeinerung des IPv4-Modells: Der
IPv4-Pfad rechnet durchgängig mit `uint32` (Bitmaps, Bereichsstatistik,
Policies, Superscopes, BOOTP, HA-Replikation), und ein Umbau hätte den
getesteten Kern riskiert. Die Konzepte bleiben gleich (Scope, Ausschluss,
Reservierung, Optionen auf Server-/Scope-/Reservierungsebene, Snapshot,
Audit, Config-Version), ebenso UI-Aufbau und API-Stil (`/api/v1/v6/…`).

- Adressen und Präfixe werden als kanonischer Text gespeichert (SQLite
  kennt keine 128-Bit-Zahlen); Bereichsprüfungen laufen in Go.
- Reservierungen gelten für eine DUID (Hex, normalisiert) und optional eine
  IAID (`-1` = jede IA) – das DHCPv6-Gegenstück zur MAC.
- Die Konfigurationstabellen sind Teil von HA-Config-Sync, Konfigurations-
  Export (`scopes6`, `server_options6`), Replace-Import und Backup; die
  Bindungen sind im Backup enthalten.

## D-071 – Adressvergabe in großen Bereichen

Ein /64 hat 2^64 Adressen – Bitmaps oder lineares Suchen wie bei IPv4
scheiden aus. Die Bindungen liegen im Speicher (Map nach Präfix, persistiert
vor der Antwort); eine neue Adresse wird deterministisch aus der DUID und
IAID gewählt: Bereiche bis 65 536 Adressen bzw. Präfixe werden ab einem
client-spezifischen Startpunkt durchsucht (zuerst nie benutzte), größere mit
64 client-spezifischen Hash-Kandidaten. Derselbe Client bekommt dadurch auch
nach Ablauf der Bindung oder nach einem Datenverlust tendenziell dieselbe
Adresse, und Adressen sind nicht vorhersagbar fortlaufend. Vorrang: Reservierung →
bestehende Bindung bzw. Angebot → Wunsch des Clients (IA-Hint) → frühere
Bindung → Suche. ADVERTISE hält die Adresse `offer_timeout` für den Client;
abgelaufene/freigegebene Bindungen bleiben `expired_grace` für den
bisherigen Client reserviert, per DECLINE gemeldete Adressen `decline_time`
gesperrt.

## D-072 – Server-DUID, Lebensdauern, Optionen

- Die Server-DUID (DUID-LLT des ersten konfigurierten Interfaces, sonst
  DUID-LL mit Zufallsadresse) wird einmal erzeugt und lokal gespeichert
  (`local_state`, nicht repliziert): Sie bleibt bei Hardwaretausch stabil,
  und HA-Partner haben verschiedene DUIDs, wie es RFC 8415 verlangt.
- Lebensdauern in Antworten sind die Restlaufzeit der Bindung, höchstens die
  konfigurierten Werte; T1/T2 sind 50 %/80 % der bevorzugten Lebensdauer.
  Adressen, die der Client nicht mehr bekommen soll (Scope geändert,
  Bindung weg), werden in RENEW/REBIND mit Lebensdauer 0 zurückgegeben.
- Optionen werden nach dem Option-Request des Clients gesendet; ohne
  Option-Request alle konfigurierten. Protokoll-Optionen (IA, Status,
  Server-ID, Präferenz, Rapid Commit, Relay-Optionen …) sind nicht
  konfigurierbar.
- Der Hostname einer Bindung kommt aus Option 39 (Client FQDN, erstes Label);
  er dient nur der Anzeige.

## D-073 – Hochverfügbarkeit für DHCPv6 ohne Bindungs-Replikation

Das HA-Protokoll (Phase 6) repliziert IPv4-Leases; eine zweite
Replikationsstrecke für IPv6 wäre ein erheblicher Ausbau. Stattdessen:

- Clients werden nach einem Hash der DUID aufgeteilt, Adressen und
  delegierte Präfixe nach einem Hash der Adresse, im Verhältnis des
  HA-Modus. Jeder Server vergibt nur aus seinem Anteil – auch wenn der
  Partner nicht erreichbar oder als ausgefallen bestätigt ist. So entstehen
  ohne Replikation nie doppelte Bindungen.
- Preis: Fällt ein Server aus, erhalten seine Clients beim REBIND eine neue
  Adresse bzw. ein neues Präfix vom Partner. Bei IPv6 mit großen Bereichen
  und mehreren Adressen pro Interface ist das vertretbar; bei delegierten
  Präfixen ändert sich das Netz hinter dem Router. Dokumentiert in HA.md.
- Die Konfiguration (Scopes, Reservierungen, Optionen) wird wie bei IPv4
  vom Config-Leader repliziert.
