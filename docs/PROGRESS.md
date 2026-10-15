# Fortschritt

Kurze Notiz pro abgeschlossener Phase (siehe PLAN.md). Entscheidungen stehen
in [DECISIONS.md](DECISIONS.md).

| Phase | Status |
|---|---|
| 1 – Fundament | ✅ abgeschlossen |
| 2 – DHCPv4-Kern | offen |
| 3 – Optionen, Klassen, Policies, Filter | offen |
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
