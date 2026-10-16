# HA-Handbuch

Zwei Leasorix-Server im selben Netz teilen sich die Scopes, replizieren
Leases und Konfiguration und übernehmen füreinander, wenn einer ausfällt.
Hintergründe zu den Entscheidungen: [DECISIONS.md](DECISIONS.md) D-043 bis
D-052.

## Modi

| Modus | Wer antwortet im Normalbetrieb | Freie Adressen |
|---|---|---|
| `load-balance` | Primary für `load_balance_percent` der Clients (Hash der MAC), Secondary für den Rest | im selben Verhältnis geteilt |
| `hot-standby` | nur der Primary | Standby hält `standby_reserve_percent` (Default 5 %) als Reserve |

Jede freie Adresse gehört fest einem Server. Neue Leases vergibt ein Server
nur aus seinem Anteil; bestehende Leases verlängert jeder.

## Einrichtung

Mit Docker: `deploy/docker-compose.ha.yml` startet ein fertiges Paar
inklusive Zertifikaten, die Verteilung auf zwei Hosts beschreibt
[DOCKER.md](DOCKER.md#ha-auf-zwei-docker-hosts). Ohne Container:

1. **Zertifikate** (mTLS, empfohlen): in der UI unter
   *Hochverfügbarkeit → mTLS-Zertifikate erzeugen* oder per CLI
   ```sh
   leasorixd ha certs --out ./ha-certs dhcp-a dhcp-b
   ```
   Auf jeden Knoten `ca.pem`, `<node>.pem` und `<node>-key.pem` kopieren
   (Schlüssel nur über sichere Wege). Alternativ `tls.mode: secret` mit einem
   gemeinsamen Geheimnis (≥ 16 Zeichen, besser ≥ 32, per
   `shared_secret_file`).
2. **Konfiguration** auf beiden Servern (Werte für `mode`, Prozente, `mclt`
   und `config_leader` müssen übereinstimmen, sonst verbinden sie sich nicht):
   ```yaml
   # Server A                              # Server B: node_id dhcp-b,
   ha:                                     # role secondary, peer dhcp-a
     mode: load-balance                    # bzw. hot-standby
     node_id: dhcp-a
     role: primary
     listen: "10.99.0.1:8068"
     peer: { id: dhcp-b, address: "10.99.0.2:8068" }
     # load_balance_percent: 50
     # standby_reserve_percent: 5
     # replication: sync                   # oder async
     # mclt: 1h
     # auto_partner_down: 0                # 0 = nur manuell
     tls:
       mode: mtls
       ca_file: /etc/leasorix/ha/ca.pem
       cert_file: /etc/leasorix/ha/dhcp-a.pem
       key_file: /etc/leasorix/ha/dhcp-a-key.pem
   ```
   Alle Werte lassen sich auch per Umgebungsvariable setzen
   (`LEASORIX_HA_PEER_ADDRESS`, `LEASORIX_HA_TLS_SHARED_SECRET_FILE`, …).
3. **Starten**: zuerst den Config-Leader (Default: Primary) einrichten
   (Setup-Token, Scopes), dann den Partner starten. Der Partner übernimmt
   Konfiguration und Benutzer automatisch; die Anmeldung funktioniert danach
   auf beiden Servern mit denselben Zugangsdaten.
4. Den Peer-Port (Default 8068/TCP) nur zwischen den beiden Servern
   freigeben. Eine eigene Verbindung (VLAN/Kabel) für die Replikation ist
   gut, aber nicht nötig.

Ein Server mit HA-Konfiguration vergibt erst Adressen, wenn er sich mit dem
Partner abgeglichen hat. Wird nur ein Server in Betrieb genommen, muss der
Partner-Ausfall einmal bestätigt werden (siehe unten) – oder HA erst
aktivieren, wenn beide bereitstehen.

## Zustände

| Zustand | Bedeutung | DHCP |
|---|---|---|
| `NORMAL` | Partner verbunden, alles abgeglichen | Aufteilung laut Modus; Leases werden vor dem ACK zum Partner repliziert |
| `COMMUNICATION_INTERRUPTED` | Partner nicht erreichbar (Ausfall oder Netztrennung) | beantwortet alle Clients, neue Adressen nur aus dem eigenen Anteil, Lease-Zeit höchstens MCLT |
| `PARTNER_DOWN` | Partner als ausgefallen bestätigt | alle Clients; nach Ablauf der MCLT der ganze Pool |
| `RECOVER` | Abgleich mit dem Partner läuft (nach Start oder Wiederverbindung) | keine Antworten |
| `RECOVER_DONE` | eigener Abgleich fertig, wartet auf den Partner | keine Antworten |

Der Abgleich zieht die komplette Lease-Tabelle des Partners und führt sie
zusammen (neuere Änderung gewinnt; bei echten Konflikten das spätere
Lease-Ende, dann die höhere Node-ID). Er dauert typischerweise unter einer
Sekunde; Clients wiederholen ihre Anfragen ohnehin.

## Betrieb

**Ein Server fällt aus.** Der andere geht nach `communication_timeout`
(Default 10 s) in `COMMUNICATION_INTERRUPTED`: bestehende Clients werden
weiter verlängert (höchstens um die MCLT), neue Clients bekommen Adressen aus
dem eigenen Anteil (im Hot Standby aus der Reserve).

**Partner-Ausfall bestätigen.** Ist der Partner sicher dauerhaft weg (nicht
nur die Verbindung gestört!), in der UI *Partner-Ausfall bestätigen* oder
`POST /api/v1/ha/partner-down` (Admin). Nach Ablauf der MCLT nutzt der
Server den ganzen Pool. Mit `auto_partner_down: 30m` geschieht das
automatisch nach dieser Zeit – Vorsicht: bei einer reinen Netztrennung
zwischen den Servern würden dann beide den ganzen Pool nutzen; die
ICMP-Konflikterkennung bleibt als Sicherheitsnetz aktiv.

**Rückkehr.** Der zurückkommende Server startet in `RECOVER`, gleicht ab und
beide gehen nach `NORMAL`. Ein Server, der länger als `auto_partner_down`
weg war, wartet in jedem Fall auf den Partner.

**Konfiguration ändern.** Nur auf dem Config-Leader; der Partner zeigt ein
Banner und lehnt Änderungen ab (API: 409). Änderungen sind nach Sekunden
(im Test < 1 s) auf dem Partner. Fällt der Leader länger aus, auf dem
verbliebenen Server `config_leader` auf dessen Rolle umstellen und neu
starten; beim Zurückholen des alten Leaders beide Konfigurationen wieder
angleichen.

**Wartung / Updates.** Server nacheinander neu starten. Ein neu gestarteter
Server gleicht sofort mit dem laufenden Partner ab.

## Fallback: Split Scope (ohne Replikation)

Für zwei unabhängige Server ohne HA-Partnerschaft: Scope öffnen →
*Split-Scope…* → Anteil wählen (üblich 80/20) → *Aufteilen*. Dieser Server
schließt den Bereich des zweiten aus; die heruntergeladene Datei auf dem
zweiten Server unter *Scopes → Importieren…* einspielen (dort ist der Teil
des ersten Servers ausgeschlossen). API: `POST /scopes/{id}/split`,
`POST /scopes/import`.

## Notfall: Lease-Datenbank sichern und einspielen

- UI: *Hochverfügbarkeit → Notfall* bzw. *Leases → Exportieren*; Import nur
  für Admins.
- API: `GET /api/v1/leases/export`, `POST /api/v1/leases/import`.
- CLI: `leasorixd leases export --config … [--out datei.json]` (auch im
  laufenden Betrieb), `leasorixd leases import --config … datei.json` (nur bei
  gestopptem Dienst).

Der Import führt die Datensätze mit dem Bestand zusammen und ist
wiederholbar. Datensätze außerhalb aller Scopes werden übersprungen.

## Überwachung

Prometheus (`/metrics`):

| Metrik | Bedeutung |
|---|---|
| `leasorix_ha_state{state}` | 1 für den aktuellen Zustand |
| `leasorix_ha_partner_connected` | Heartbeats erfolgreich |
| `leasorix_ha_lease_updates_total{direction}` | replizierte Lease-Änderungen |
| `leasorix_ha_replication_errors_total` | nicht zugestellte Update-Pakete |
| `leasorix_ha_replication_duration_seconds` | Laufzeit der Updates |
| `leasorix_ha_lease_conflicts_total` | aufgelöste Konflikte |
| `leasorix_ha_resyncs_total{result}` | Abgleiche |
| `leasorix_ha_config_version` | Konfigurationsversion |
| `leasorix_ha_certificate_expiry_timestamp_seconds` | Ablauf des Knotenzertifikats |

Empfohlene Alarme: Zustand ≠ NORMAL länger als einige Minuten,
`replication_errors_total` steigt, Zertifikat läuft in < 30 Tagen ab.
Zustandswechsel stehen zusätzlich im Audit-Log (`ha.state`,
`ha.partner_down`, `ha.config_applied`, `lease.ha_conflict`).

## Fehlersuche

| Meldung (UI/Log) | Ursache |
|---|---|
| `partner reports node id …` / `both servers are configured as primary` / `… differs` | Einstellungen der beiden Server passen nicht zusammen |
| `peer certificate does not belong to …` | Zertifikat gehört nicht zur konfigurierten `peer.id` |
| `peer authentication failed` | Shared Secret oder Node-ID falsch |
| `connection refused` / `network is unreachable` | Partner aus, Port 8068 gesperrt oder falsche `peer.address` |
| Server bleibt in `RECOVER` | Partner nie erreicht (erster Start) – Partner starten oder Ausfall bestätigen |

## Test

`make test-ha` (root) startet zwei echte `leasorixd` in Network Namespaces
und prüft: 5000 Clients im Load Balance ohne doppelte Adressen, harten
Ausfall des Primary im Hot Standby (Verlängerung beim Standby, Übernahme,
Abgleich bei Rückkehr), Netztrennung mit anschließender Zusammenführung und
die Konfigurations-Replikation.

## Grenzen

- Genau zwei Server pro Partnerschaft und eine Partnerschaft pro Server
  (alle Scopes); kein Raft-Cluster (D-052).
- Die Uhren beider Server sollten per NTP synchron sein (Lease-Zeiten und
  die Reihenfolge von Änderungen basieren auf Zeitstempeln).
- Admin-Löschungen während einer Netztrennung können beim Abgleich
  zurückkommen (D-048).
