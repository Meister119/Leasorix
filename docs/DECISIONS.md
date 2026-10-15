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
