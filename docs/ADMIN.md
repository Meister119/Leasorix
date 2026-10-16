# Admin-Handbuch

Betrieb eines Leasorix-Servers. Hochverfügbarkeit hat ein eigenes Handbuch:
[HA.md](HA.md), Container-Betrieb [DOCKER.md](DOCKER.md). Entscheidungen und
Hintergründe: [DECISIONS.md](DECISIONS.md).

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
| `dhcp6.interfaces` | – | Interfaces für DHCPv6 (leer = aus), siehe [Abschnitt 12](#12-dhcpv6) |
| `dhcp6.decline_time` | `24h` | Sperre für Adressen, die ein Client per DECLINE meldet |
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
`leasorix_ha_*`, `leasorix_dns_*`), Health unter `/healthz` und `/readyz`.

## 7. Konfiguration exportieren und importieren

*Benutzer & Einstellungen → Sicherung & Import* oder
`leasorixctl config export|import`. Das Format `leasorix-config/v1` (YAML oder
JSON) enthält Einstellungen, eigene Optionsdefinitionen, Klassen,
Server-Optionen und -Policies, MAC-Filter, Superscopes, DNS-Zonen und alle
Scopes mit Ausschlüssen, Reservierungen, Optionen, Policies und
DNS-Einstellungen, außerdem die DHCPv6-Konfiguration (`server_options6`,
`scopes6`, siehe [Abschnitt 12](#12-dhcpv6)). Bezüge laufen über Namen, nicht über Datenbank-IDs;
Benutzer, API-Tokens und TSIG-Schlüssel (Geheimnisse) sind nicht enthalten –
eine Zone mit Schlüssel lässt sich nur importieren, wenn ein Schlüssel
gleichen Namens auf dem Zielserver existiert.

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
Klassen, Superscopes, MAC-Filter und die DNS-Update-Einstellungen
(`set dnsconfig` auf Server- und Scope-Ebene, siehe
[Abschnitt 11](#11-dns-updates)). DNS-Zonen und TSIG-Schlüssel stehen nicht im
Dump und müssen in Leasorix angelegt werden. Nicht übernommene Zeilen werden
mit Grund aufgelistet – typischerweise klassenspezifische Optionswerte (in
Leasorix als Policy anlegen), Multicast-Scopes und DHCPv6. Aktive Leases sind im Dump nicht enthalten; Clients erneuern ihre
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
Leases (auch DHCPv6-Bindungen); das Audit-Log des Servers bleibt erhalten und bekommt einen Eintrag
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
leasorixctl dns registrations
leasorixctl v6 leases -q 0003000102    # DHCPv6-Bindungen
leasorixctl config export -f config.yaml
leasorixctl -o json leases list            # JSON statt Tabelle
```

`leasorixctl` ohne Argumente zeigt alle Befehle. Exit-Code 0 = Erfolg,
1 = Fehler (Meldung des Servers wird ausgegeben), 2 = Aufruffehler.

## 11. DNS-Updates

Leasorix trägt Clients per RFC 2136 als A- und PTR-Record in DNS ein (wie
die DNS-Registerkarte eines Windows-DHCP-Scopes). Einrichtung:

1. **TSIG-Schlüssel** (*DNS-Updates → TSIG-Schlüssel*, nur Admins): Name und
   Algorithmus (Standard `hmac-sha256`); das Geheimnis wird zufällig erzeugt
   oder aus `tsig-keygen` übernommen. Nach dem Speichern zeigt die UI einmalig
   die passende Konfiguration für BIND und PowerDNS.
2. **Zonen** (*DNS-Updates → Zonen*): Vorwärtszone (z. B. `example.lan`) und
   Reverse-Zone (z. B. `1.168.192.in-addr.arpa`) jeweils mit primärem Server
   (`IP` oder `host:port`) und Schlüssel. Für jeden Namen gilt die
   spezifischste passende Zone.
3. **Scope** (*Scope → DNS*):

| Einstellung | Bedeutung |
|---|---|
| Records registrieren | **Nie**; **auf Anforderung des Clients** (Option 81: A und PTR, bzw. nur PTR, wenn der Client seinen A-Record selbst einträgt; Clients ohne Option 81 werden nicht registriert); **immer** (A und PTR für jeden Client mit Namen aus Option 81 oder 12) |
| Domäne | Name = erstes Label des Client-Namens + diese Domäne; leer = Domänenname aus Option 15 |
| TTL | 0 = ein Drittel der Lease-Dauer (1 min … 1 Tag) |
| Records entfernen, wenn der Lease endet | Release, Ablauf oder Löschen entfernen A und PTR (Standard: an) |
| Namensschutz (DHCID) | Ein Name, der einem anderen Client gehört oder manuell angelegt wurde, wird nie überschrieben (RFC 4701/4703, Standard: an) |

Der DHCP-Server wartet nie auf DNS: Updates laufen im Hintergrund mit
Wiederholungen (30 s, dann verdoppelt bis 1 h). *DNS-Updates →
Registrierungen* bzw. `leasorixctl dns registrations` zeigt pro Adresse den
eingetragenen Namen und den Zustand (`registriert`, `ausstehend`, `Konflikt`,
`Fehler` mit Meldung). Konflikte durch den Namensschutz stehen zusätzlich im
Audit-Log (`dns.conflict`) und werden bei der nächsten Verlängerung des
Leases erneut versucht. Metriken: `leasorix_dns_updates_total`,
`leasorix_dns_registrations`.

Der Client kann nur einen Namen in der Domäne seines Scopes registrieren –
ein mitgesendeter vollqualifizierter Name in einer anderen Zone wird auf die
Scope-Domäne umgeschrieben (und so in der Antwort-Option 81 mitgeteilt).

**DNS-Server:**

- *BIND:* Schlüssel per `key "…" { … };` einbinden und in jeder Zone
  `update-policy { grant <schlüssel> zonesub ANY; };` setzen.
- *PowerDNS:* `dnsupdate=yes`, `allow-dnsupdate-from=<Leasorix-Adresse>`,
  Schlüssel mit `pdnsutil import-tsig-key` anlegen und pro Zone
  `pdnsutil set-meta <zone> TSIG-ALLOW-DNSUPDATE <schlüssel>`.
- *Windows-DNS* unterstützt kein TSIG, sondern nur GSS-TSIG (Kerberos). Mit
  „nicht sicheren und sicheren“ dynamischen Updates kann die Zone ohne
  Schlüssel eingetragen werden (Updates dann nur über die Quelladresse
  geschützt).

Bei Hochverfügbarkeit registriert jeder Server die Leases, die er vergibt;
übernimmt der Partner einen Client, aktualisiert er dessen Records (gleiche
DHCID, daher kein Konflikt). Die Registrierungsliste ist pro Server.

## 12. DHCPv6

Leasorix bedient IPv6-Clients zustandsbehaftet nach RFC 8415: Adressen
(IA_NA) und Präfix-Delegation (IA_PD) für Router, dazu Optionen für
zustandslose Clients (INFORMATION-REQUEST). Die Konfiguration ist von IPv4
getrennt (Seite *IPv6 (DHCPv6)*, API unter `/api/v1/v6/…`,
`leasorixctl v6 …`), folgt aber denselben Konzepten.

**Aktivieren:** in der Bootstrap-Konfiguration

```yaml
dhcp6:
  interfaces: [eth0]      # Interfaces mit IPv6-Adresse im Präfix eines Scopes
```

Pro Interface öffnet Leasorix UDP-Port 547 und tritt der Multicast-Gruppe
`ff02::1:2` bei. Die Server-DUID (DUID-LLT aus der MAC des ersten
Interfaces) wird beim ersten Start erzeugt und bleibt danach gleich
(*Benutzer & Einstellungen → System*). Im Container braucht DHCPv6 den
Host- oder macvlan-Modus mit IPv6 wie DHCPv4.

**Router-Advertisements:** Ob Clients DHCPv6 überhaupt nutzen, steuert der
Router: das *Managed*-Flag (M=1) im Router-Advertisement für Adressen, das
*Other*-Flag (O=1) nur für Optionen. Das Default-Gateway kommt bei IPv6 immer
aus dem Router-Advertisement, nie von DHCPv6.

**Scopes** (*IPv6 (DHCPv6) → Neuer IPv6-Scope*):

| Feld | Bedeutung |
|---|---|
| Präfix | Präfix des Links (z. B. `2001:db8:1::/64`); bestimmt, welche Clients der Scope bedient – über die Adresse des Interfaces oder bei Relays über die *link-address* |
| Start-/Endadresse | Bereich für die Adressvergabe; leer = keine Adressen (nur Präfix-Delegation) |
| Bevorzugte / gültige Lebensdauer | in Sekunden; T1/T2 = 50 %/80 % der bevorzugten Lebensdauer |
| Delegations-Pool, Länge | z. B. `2001:db8:100::/40` mit Länge 56 → 65 536 delegierbare /56 für Router; darf das Link-Präfix nicht überlappen |
| Server-Präferenz | Option 7 im ADVERTISE (0–255); bei mehreren Servern wählt der Client den höchsten |
| Rapid Commit | SOLICIT mit Rapid-Commit wird direkt mit REPLY beantwortet (2 statt 4 Nachrichten) |

Adressen werden pro Client deterministisch gewählt (gleicher Client – gleiche
Adresse, solange sie frei ist); große Bereiche wie ein ganzes /64 sind
möglich. **Ausschlüsse** nehmen Adressbereiche aus der Vergabe.
**Reservierungen** binden eine Adresse an die DUID des Clients, optional nur
für eine IAID (leer = jede IA des Clients); die Adresse muss im Präfix
liegen, nicht unbedingt im Bereich. Die DUID steht im Lease bzw. im Log des
Clients (z. B. `/var/lib/dhcp/dhclient6.leases`, Windows: `ipconfig /all`
„DHCPv6-Client-DUID“).

**Optionen** gibt es auf Server-, Scope- und Reservierungsebene (die
spezifischere gewinnt): u. a. DNS-Server (23), Domänen-Suchliste (24), SNTP
(31), NTP (56), Boot-Datei-URL (59), AFTR-Name (64). Eigene Optionen mit
Code und Typ sind möglich; Optionen, die das Protokoll selbst setzt (IA,
Status, Präferenz, Rapid Commit, Relay-Optionen …), sind gesperrt. Ohne
Option-Request des Clients werden alle konfigurierten Optionen gesendet.

**Leases** (*IPv6 → Leases*, `leasorixctl v6 leases`) zeigen Adressen und
delegierte Präfixe mit DUID, IAID, Hostname (aus Option 39) und Laufzeit;
Operatoren können Bindungen löschen. Abgelaufene oder freigegebene Bindungen
bleiben `dhcp6.expired_grace` (4 h) für denselben Client reserviert; per
DECLINE gemeldete Adressen sind `dhcp6.decline_time` gesperrt.

**Relays:** Ein DHCPv6-Relay (RELAY-FORW, auch mehrstufig) muss an eine
Adresse eines der `dhcp6.interfaces` senden. Der Scope ergibt sich aus der
*link-address* des innersten Relays; die Antwort geht als RELAY-REPL an das
Relay zurück (Interface-ID wird zurückgegeben). Direkt per Unicast gesendete
Anfragen beantwortet Leasorix mit `UseMulticast` (es bietet keine
Server-Unicast-Option an).

**MAC-Filter** gelten auch für DHCPv6, sofern die MAC des Clients bekannt
ist (DUID-LL/-LLT mit Ethernet-Adresse oder Option 79 vom Relay).

**Hochverfügbarkeit:** Die DHCPv6-Konfiguration wird wie die IPv4-
Konfiguration vom Config-Leader repliziert. DHCPv6-Bindungen werden **nicht**
repliziert: Die Clients (nach DUID) und die Adressen/Präfixe werden wie bei
IPv4 nach dem HA-Modus aufgeteilt, und jeder Server vergibt nur aus seinem
Anteil. Ist ein Server ausgefallen, bedient der Partner alle Clients – aber
weiterhin nur aus seinem Anteil; Clients des ausgefallenen
Servers bekommen beim nächsten Rebind eine neue Adresse (die alte mit
Lebensdauer 0). Die Bereiche sollten daher großzügig sein (bei /64 kein
Thema). Details: [HA.md](HA.md#dhcpv6).

**Nicht umgesetzt:** DNS-Updates (AAAA) für DHCPv6, Policies/Klassen für
IPv6, Lease-Export/-Import für DHCPv6 (Backup enthält die Bindungen),
Reconfigure, Server-Unicast-Option, Übernahme von Windows-DHCPv6-Scopes aus
dem netsh-Dump.

Metriken: `leasorix_dhcp6_packets_received_total`, `…_packets_sent_total`,
`…_packets_dropped_total` (nach Grund) und `…_lease_events_total`.

## 13. Sicherheit

- Den Management-Listener nur im Management-Netz (oder auf localhost hinter
  einem Reverse Proxy) erreichbar machen.
- **Rogue-DHCP:** Ein fremder DHCP-Server im Netz kann Clients falsche
  Adressen und Gateways geben. Auf den Switches **DHCP Snooping** aktivieren
  und nur die Ports der Leasorix-Server (bzw. der Relays) als vertrauenswürdig
  markieren.
- Backups und HA-Schlüssel sind vertraulich; API-Tokens mit der kleinsten
  nötigen Rolle anlegen und ein Ablaufdatum setzen.
- TSIG-Schlüssel liegen in der Datenbank (für die Signatur nötig), werden
  von der API nie ausgegeben und sind nur für Admins änderbar. DNS-Updates
  ohne Schlüssel nur in vertrauenswürdigen Netzen verwenden.
