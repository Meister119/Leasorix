# Admin-Handbuch

Betrieb eines Leasorix-Servers. Hochverfügbarkeit hat ein eigenes Handbuch:
[HA.md](HA.md). Entscheidungen und Hintergründe: [DECISIONS.md](DECISIONS.md).

## 1. Start und Bootstrap-Konfiguration

`leasorixd --config /etc/leasorix/leasorix.yml` startet den Server. Die Datei
enthält nur Prozess-Einstellungen (Listener, Datenverzeichnis, Interfaces,
Logging, HA); die eigentliche DHCP-Konfiguration liegt in der Datenbank und
wird über Web-UI, REST-API oder `leasorixctl` gepflegt. Ein kommentiertes
Beispiel mit allen Schlüsseln: [`deploy/examples/leasorix.yml`](../deploy/examples/leasorix.yml).

Jeder Schlüssel lässt sich per Umgebungsvariable überschreiben:
`LEASORIX_<ABSCHNITT>_<SCHLÜSSEL>`, z. B. `LEASORIX_HTTP_LISTEN=10.0.0.5:8067`,
`LEASORIX_DHCP4_INTERFACES=eth0,eth1`, `LEASORIX_HA_PEER_ADDRESS=…`.

| Schlüssel | Default | Bedeutung |
|---|---|---|
| `data_dir` | `/var/lib/leasorix` | Datenbank, Zertifikate, Lock-Datei |
| `http.listen` | `127.0.0.1:8067` | Management (UI, API, `/metrics`) – nur Management-Netz |
| `http.tls.mode` | `self-signed` | `self-signed`, `files` (eigenes Zertifikat) oder `off` |
| `dhcp4.interfaces` | – | Interfaces, auf denen DHCP bedient wird (leer = aus) |
| `dhcp4.conflict_detection` | an, 500 ms | Ping vor jeder neuen Adresse |
| `dhcp4.bootp_lease_time` | `720h` | Lease-Dauer für BOOTP-Clients |
| `audit.retention` | `2160h` (90 Tage) | Aufbewahrung des Audit-Logs |
| `auth.*` | | Sitzungsdauer, Login-Sperre, Setup-Token |
| `ha.*` | aus | siehe [HA.md](HA.md) |

Ein Datenverzeichnis kann nur von einem Prozess genutzt werden
(`leasorixd.lock`).

**Erster Start:** Im Log erscheint ein einmaliges Setup-Token
(`setup_token=…`); damit legt man im Browser den ersten Administrator an.
Das Zertifikat ist anfangs selbstsigniert (`<data_dir>/tls`), für den
Dauerbetrieb ein eigenes Zertifikat (`http.tls.mode: files`) hinterlegen.

## 2. Scopes

Ein Scope ist ein IPv4-Subnetz mit dynamischem Bereich, Lease-Dauer und
Optionen. Welcher Scope einen Client bedient, ergibt sich aus der Adresse
des Interfaces bzw. bei Relays aus `giaddr` (oder Option 118 / 82.5).

- **Ausschlüsse** nehmen Adressen aus der dynamischen Vergabe (z. B. für
  statisch konfigurierte Geräte).
- **Reservierungen** binden eine Adresse fest an MAC oder Client-ID; sie
  haben Vorrang und dürfen außerhalb des dynamischen Bereichs liegen. Import
  per CSV unter *Reservierungen → CSV-Import*.
- **Superscopes** fassen Scopes zusammen, die dasselbe physische Netz
  versorgen (mehrere IP-Netze auf einem Segment, „Multinet“). Clients auf
  dem Link eines Mitglieds können Adressen aus allen Mitgliedern bekommen –
  der Router muss alle Netze auf dem Segment bedienen. Anlegen unter
  *Scopes → Superscopes*, Zuordnung im Scope unter *Einstellungen*.
- **BOOTP:** Clients mit Reservierung erhalten immer eine BOOTP-Antwort.
  Dynamisches BOOTP (Adressen aus dem Pool) wird pro Scope mit „BOOTP-Clients
  dynamisch bedienen“ eingeschaltet; die Lease-Dauer ist
  `dhcp4.bootp_lease_time`. BOOTP-Antworten enthalten die wichtigsten
  Optionen (64-Byte-Vendor-Bereich) sowie Bootserver/-datei aus Option 66/67.
- **Split-Scope:** siehe [HA.md](HA.md#fallback-split-scope-ohne-replikation).

## 3. Optionen, Klassen, Policies, Filter

Optionen werden vererbt: Server < Scope < Server-Policy < Scope-Policy <
Reservierung (die spezifischere Ebene gewinnt). Fordert ein Client eine
Parameterliste an (Option 55), wird genau diese in seiner Reihenfolge
geliefert. Eigene Optionscodes lassen sich unter *Benutzer & Einstellungen →
Server-Optionen* definieren.

Policies (wie unter Windows) prüfen Bedingungen auf MAC, Client-ID,
Hostname, Vendor-/User-Class, definierte Klassen und Option 82 (Platzhalter
`*`/`?`, Hex mit `0x…`) und liefern eigene Optionen, eine eigene Lease-Dauer
und – auf Scope-Ebene – eigene Adressbereiche. MAC-Filter (Allow/Deny,
Platzhalter) werden unter *Policies & Filter → Filter* gepflegt und
aktiviert.

## 4. Leases

*Leases* zeigt alle Datensätze mit Suche und Filter. „Löschen“ gibt eine
Adresse sofort frei, „In Reservierung umwandeln“ übernimmt IP, MAC und
Hostname. Abgelaufene und freigegebene Leases bleiben `expired_grace`
(Default 4 h) erhalten, damit derselbe Client seine Adresse wiederbekommt.
Antwortet eine Adresse auf den Ping vor der Vergabe, wird sie für
`decline_time` gesperrt (Zustand „abgelehnt“).

## 5. Benutzer, Rollen, API-Tokens

| Rolle | darf |
|---|---|
| `readonly` | alles lesen (außer Benutzerverwaltung), Konfiguration exportieren |
| `operator` | zusätzlich DHCP-Konfiguration und Leases ändern |
| `admin` | zusätzlich Benutzer, Einstellungen, Import, Backup/Restore, HA-Aktionen |

API-Tokens (`lxt_…`) werden unter *Mein Konto & API-Tokens* erzeugt; ihre
Rolle ist höchstens die des Benutzers. Sie werden als
`Authorization: Bearer lxt_…` gesendet und für `leasorixctl` gebraucht.

## 6. Audit-Log und Überwachung

Jede Änderung (UI, API, DHCP-Ereignisse, HA) steht im Audit-Log mit Akteur
und Vorher/Nachher. Prometheus-Metriken unter `/metrics`
(`leasorix_dhcp4_*`, `leasorix_lease_*`, `leasorix_scope_*`, `leasorix_leases`,
`leasorix_ha_*`), Health unter `/healthz` und `/readyz`.

## 7. Konfiguration exportieren und importieren

*Benutzer & Einstellungen → Sicherung & Import* oder
`leasorixctl config export|import`. Das Format `leasorix-config/v1` (YAML oder
JSON) enthält Einstellungen, eigene Optionsdefinitionen, Klassen,
Server-Optionen und -Policies, MAC-Filter, Superscopes und alle Scopes mit
Ausschlüssen, Reservierungen, Optionen und Policies. Bezüge laufen über
Namen, nicht über Datenbank-IDs; Benutzer und API-Tokens sind nicht
enthalten.

```yaml
format: leasorix-config/v1
server_options:
  - {code: 6, value: "10.0.0.53,10.0.0.54"}
superscopes:
  - {name: Gebäude A}
scopes:
  - scope: {name: LAN, subnet: 10.0.0.0/24, start: 10.0.0.100, end: 10.0.0.200,
            lease_time: 86400, enabled: true, superscope: Gebäude A}
    exclusions: [{start: 10.0.0.100, end: 10.0.0.109}]
    reservations: [{ip: 10.0.0.20, mac: "00:15:5d:0a:0b:0c", hostname: printer}]
    options: [{code: 3, value: 10.0.0.1}]
    policies: []
```

Import-Modi:

- **Ergänzen** (`merge`): fügt hinzu; bereits vorhandene Objekte (gleicher
  Scope-Name/überlappendes Subnetz, Klassenname, …) sind Konflikte.
  Server-Optionen und Einstellungen aus der Datei überschreiben.
- **Ersetzen** (`replace`): ersetzt die gesamte DHCP-Konfiguration; Leases
  in weiterhin vorhandenen Subnetzen bleiben erhalten.
- **Nur prüfen** (Dry-Run): validiert gegen die aktuelle Konfiguration ohne
  Änderung.

Jeder Import wird vollständig geprüft, bevor etwas übernommen wird – bei
einem Fehler bleibt alles unverändert. Auf einem HA-Partner ohne
Config-Leader-Rolle ist der Import gesperrt.

## 8. Umzug von einem Windows-DHCP-Server

Auf dem Windows-Server `netsh dhcp server dump > dhcp.txt` ausführen und die
Datei unter *Sicherung & Import → Windows-DHCP übernehmen* (zuerst mit
„Nur prüfen“) oder per `leasorixctl config import --netsh dhcp.txt`
einspielen. Übernommen werden Scopes (Name, Bereich, Status, Lease-Dauer aus
Option 51, BOOTP-Freigabe), Ausschlüsse, Reservierungen, Optionswerte auf
Server-, Scope- und Reservierungsebene, eigene Optionsdefinitionen,
Klassen, Superscopes und MAC-Filter. Nicht übernommene Zeilen werden mit
Grund aufgelistet – typischerweise klassenspezifische Optionswerte (in
Leasorix als Policy anlegen), DNS-Update-Einstellungen, Multicast-Scopes und
DHCPv6. Aktive Leases sind im Dump nicht enthalten; Clients erneuern ihre
Adresse beim nächsten Renew (Reservierungen bleiben ohnehin gleich).

## 9. Backup und Wiederherstellung

Ein Backup ist eine vollständige, konsistente Kopie der Datenbank (auch im
laufenden Betrieb) – inklusive Passwort-Hashes, also sicher aufbewahren.

- UI: *Sicherung & Import → Backup herunterladen / Wiederherstellen…*
- Server-CLI: `leasorixd backup --config … --out backup.db` (auch im
  laufenden Betrieb), `leasorixd restore --config … backup.db` (bei gestopptem
  Dienst)
- API/CLI: `GET /api/v1/system/backup`, `POST /api/v1/system/restore`,
  `leasorixctl backup FILE`, `leasorixctl restore FILE --yes`

Die Wiederherstellung übernimmt Konfiguration, Benutzer, API-Tokens und
Leases; das Audit-Log des Servers bleibt erhalten und bekommt einen Eintrag
`system.restore`. Alle Sitzungen enden. Backups älterer Versionen werden
automatisch migriert, Backups einer neueren Version abgelehnt. Für den
reinen Lease-Notfall gibt es zusätzlich den Lease-Export/-Import (siehe
[HA.md](HA.md#notfall-lease-datenbank-sichern-und-einspielen)).

## 10. Kommandozeile `leasorixctl`

`leasorixctl` steuert einen Server über die REST-API:

```sh
export LEASORIX_URL=https://dhcp01:8067 LEASORIX_TOKEN=lxt_…
export LEASORIX_CA=/etc/leasorix/tls/cert.pem   # oder LEASORIX_INSECURE=1
leasorixctl status
leasorixctl scopes list
leasorixctl leases list --scope LAN -q printer
leasorixctl ha status
leasorixctl config export -f config.yaml
leasorixctl -o json leases list            # JSON statt Tabelle
```

`leasorixctl` ohne Argumente zeigt alle Befehle. Exit-Code 0 = Erfolg,
1 = Fehler (Meldung des Servers wird ausgegeben), 2 = Aufruffehler.

## 11. Sicherheit

- Den Management-Listener nur im Management-Netz (oder auf localhost hinter
  einem Reverse Proxy) erreichbar machen.
- **Rogue-DHCP:** Ein fremder DHCP-Server im Netz kann Clients falsche
  Adressen und Gateways geben. Auf den Switches **DHCP Snooping** aktivieren
  und nur die Ports der Leasorix-Server (bzw. der Relays) als vertrauenswürdig
  markieren.
- Backups und HA-Schlüssel sind vertraulich; API-Tokens mit der kleinsten
  nötigen Rolle anlegen und ein Ablaufdatum setzen.
