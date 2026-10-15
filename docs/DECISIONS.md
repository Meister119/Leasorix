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
