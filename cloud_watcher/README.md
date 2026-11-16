# Außen-Wächter

Der Wächter auf dem Server des Dashboards (`nas_dashboard/cron/check_offline.php`)
meldet, wenn die Box schweigt. Fällt aber der **Server selbst** aus – gestoppt,
Tunnel oder Internet gestört, Zertifikat, Webserver –, fällt er mit aus und
meldet nichts. Dann geht auch der manuelle REAL ALARM über das Dashboard nicht.

Der Außen-Wächter schließt diese Lücke von **außen**: Ein *anderer* Rechner
ruft alle 5 Minuten `api/alive.php` über die öffentliche Adresse des
Dashboards auf und meldet per Bark an den Status-Key.

> **Wer überwacht wen?** Bis 11/2026 lief das Dashboard auf dem NAS und eine
> Cloud-VM überwachte es (daher der Ordnername `cloud_watcher/` und die
> Variable `NAS_ALIVE_URL`). Seit dem Umzug ist es umgekehrt: Das Dashboard
> läuft auf der VM (`server/README.md`), und das **NAS zu Hause** führt
> dieses Skript als DSM-Aufgabe aus. Wie das überwachte Ziel in den
> Meldungen heißt, steht in `TARGET_NAME` (hier: `Alarm-Server`).

| Meldung (mit `TARGET_NAME="Alarm-Server"`) | Wann | Danach |
|---|---|---|
| `Alarm-Server NICHT ERREICHBAR` | nach 3 Fehlschlägen in Folge (ca. 10–15 min) | Erinnerung `… weiterhin nicht erreichbar` alle 24 h, einmal `… wieder erreichbar` (mit Dauer und letztem Fehler) |
| `Alarm-Server-Waechter laeuft nicht` | `check_offline.php` lief dort > 45 min nicht (dann würde ein Ausfall der Box nicht gemeldet) | Erinnerung alle 24 h, einmal `…-Waechter laeuft wieder` |
| `Alarm-Server-Waechter meldet nichts` | in `config.php` fehlt `bark_key_status` – dann meldet der Wächter dort bewusst gar nichts | wie oben |
| `Alarm-Server kann Bark nicht erreichen` | die letzte Meldung des Wächters dort an Bark ist gescheitert und seitdem ging keine durch (3 Läufe in Folge) – dann geht auch der manuelle REAL ALARM nicht raus | Erinnerung alle 24 h, einmal `… erreicht Bark wieder` |
| `Aussen-Waechter kann wieder melden` | eine eigene Meldung kam nicht bei Bark an, hat sich aber erledigt, bevor ein neuer Versuch durchging | – |

Eine Meldung gilt erst als erledigt, wenn Bark sie angenommen hat (HTTP 200) –
sonst versucht es der nächste Lauf erneut. Scheitert der eigene Versand,
erfährt das Dashboard es beim nächsten Aufruf (`bark_failed_at`) und warnt in
der Zeile „Außen-Wächter (NAS)“: „… kann aber nicht melden“.

**Einstellungen:** Für den Außen-Wächter gelten nur `BARK_KEY` und
`BARK_LEVEL` (alle Meldungen gleich dringend) sowie die Schwellen in seiner
Konfiguration. Ruhezeit, Stellvertreter, Ton und Dringlichkeit aus dem
Dashboard („Meldungen an mich“) gelten **nicht** für ihn – er liest die
Einstellungen des Servers nicht (er soll ja gerade dann melden, wenn der
Server weg ist).

**Gegenseitig:** `alive.php` merkt sich jeden Aufruf. Verstummt der
Außen-Wächter – NAS aus, Strom oder Internet zu Hause weg, DSM-Aufgabe
abgeschaltet –, meldet das der Wächter auf dem Server („Aussen-Waechter meldet
sich nicht“, einstellbar im Dashboard). Das Dashboard zeigt den Zustand unter
„Box & Wartung“ als Zeile „Außen-Wächter (NAS)“.

Bark-Alarme der Box laufen davon völlig unabhängig. Der Außen-Wächter ist –
wie das ganze Projekt – ein **Zusatz, kein Ersatz**.

## Sicherheit: als root nur aus einem Ordner, der root gehört

Als DSM-Aufgabe läuft das Skript als **root**, und es *führt seine
Konfiguration aus* (`. fw-alarm-watch.conf`). Liegen Skript, Konfiguration
oder der Zustandsordner dort, wo ein anderer Benutzer schreiben darf, hat
dieser Benutzer damit root. Auf dem Synology-NAS darf der Web-Benutzer `http`
in den Freigaben `web` und `web_packages` schreiben – auch in neu angelegten
Unterordnern.

Deshalb:

- Skript, Konfiguration und Zustand liegen in einem Ordner **außerhalb der
  Freigaben**, der nur root gehört: `/volume1/@fw_alarm_watch` (Rechte 700).
- Das Skript prüft das selbst: Läuft es als root und gehört eine dieser
  Dateien oder einer der Ordner nicht root (oder ist für Gruppe/Andere
  schreibbar), bricht es mit „Unsichere Rechte: …“ ab, **bevor** es die
  Konfiguration liest (Rückgabewert 2).

## Einrichtung auf dem NAS (DSM-Aufgabe)

Voraussetzungen auf dem NAS (bei DSM 7 vorhanden): `bash` ≥ 4.3, `curl` ≥ 7.71,
`flock`.

1. **Token:** Der Server kennt nur den Hash (`watcher_token_sha256` in
   `config.php`). Neues Paar erzeugen – Zeile 1 = Klartext für den Wächter,
   Zeile 2 = Hash für `config.php`:
   ```bash
   php -r '$t=bin2hex(random_bytes(32)); echo $t, PHP_EOL, hash("sha256", $t), PHP_EOL;'
   ```
   Eigenes Token, **nicht** das Maschinen-Token der Box (derselbe Hash wie
   `machine_token_sha256` wird mit 404 abgelehnt).
2. **Konfiguration:** `fw-alarm-watch.conf.example` kopieren und ausfüllen
   (`TARGET_NAME`, `NAS_ALIVE_URL` = Adresse von `api/alive.php` des
   Dashboards, `WATCHER_TOKEN`, `BARK_KEY`). LF-Zeilenenden – unter Windows
   bearbeitet: `sed -i 's/\r$//' fw-alarm-watch.conf`.
3. **Bereitlegen:** Skript und Konfiguration per SMB in einen
   Zwischenordner kopieren, z. B. `\\<NAS>\web_packages\fw_alarm_watch_stage\`,
   und die Prüfsumme des Skripts notieren (`sha256sum fw-alarm-watch.sh`).
   Der Zwischenordner ist für den Web-Benutzer lesbar – deshalb gleich
   weiter mit Schritt 4.
4. **Einmalige DSM-Aufgabe als root** (Systemsteuerung → Aufgabenplaner →
   Erstellen → Geplante Aufgabe → Benutzerdefiniertes Skript; anlegen, einmal
   „Ausführen“, danach löschen):
   ```sh
   set -e
   S=/volume1/web_packages/fw_alarm_watch_stage
   D=/volume1/@fw_alarm_watch
   echo "<sha256 des Skripts>  $S/fw-alarm-watch.sh" | sha256sum -c -
   mkdir -p $D/state
   cp $S/fw-alarm-watch.sh $S/fw-alarm-watch.conf $D/
   chown -R root:root $D
   chmod 700 $D $D/state $D/fw-alarm-watch.sh
   chmod 600 $D/fw-alarm-watch.conf
   rm -rf $S
   ```
   Die Prüfsumme stellt sicher, dass genau das Skript aus dem Repo
   installiert wird. Ist der Zwischenordner danach verschwunden, lief alles
   durch.
5. **Wiederkehrende DSM-Aufgabe als root**, täglich, alle 5 Minuten:
   ```sh
   FW_WATCH_CONF=/volume1/@fw_alarm_watch/fw-alarm-watch.conf STATE_DIRECTORY=/volume1/@fw_alarm_watch/state /bin/bash /volume1/@fw_alarm_watch/fw-alarm-watch.sh > /volume1/@fw_alarm_watch/last-run.log 2>&1
   ```
   Einmal von Hand ausführen. Ob es läuft, sieht man ohne das NAS:
   Im Dashboard unter „Box & Wartung“ steht „Außen-Wächter (NAS): meldet sich
   (zuletzt …)“, und im Zugriffsprotokoll des Servers erscheint alle 5 Minuten
   `POST /api/alive.php` mit Status 200. Scheitert ein Lauf, zeigt DSM den
   Grund unter „Aktion → Ergebnis anzeigen“ (dafür in den Einstellungen des
   Aufgabenplaners die Ausgabe speichern lassen).

**Aktualisieren:** neues Skript wie in Schritt 3–4 bereitlegen und die
einmalige Aufgabe erneut ausführen (ohne die Zeilen für die Konfiguration,
wenn sie bleiben soll).

## Alternative: Linux-VM mit systemd

So lief es bis 11/2026 (VM überwacht NAS); der Weg taugt für jeden Linux-Rechner
mit systemd. Hier läuft das Skript als eigener Benutzer `fwwatch`, nicht als
root – die Rechteprüfung oben entfällt dann.

| Datei | Ziel |
|---|---|
| `fw-alarm-watch.sh` | `/usr/local/bin/fw-alarm-watch` (755, root) |
| `fw-alarm-watch.service`, `fw-alarm-watch.timer` | `/etc/systemd/system/` |
| `fw-alarm-watch.conf.example` | Vorlage für `/etc/fw-alarm-watch.conf` (640, root:fwwatch) – **enthält Geheimnisse, nie ins Git** |

Die Dateien in einen eigenen Ordner im Home-Verzeichnis kopieren (nicht nach
`/tmp` – die Konfiguration enthält Token und Bark-Key), dann:

```bash
sed -i 's/\r$//' fw-alarm-watch.sh fw-alarm-watch.service fw-alarm-watch.timer fw-alarm-watch.conf
sudo useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin fwwatch
sudo install -m 755 fw-alarm-watch.sh /usr/local/bin/fw-alarm-watch
sudo install -m 644 fw-alarm-watch.service fw-alarm-watch.timer /etc/systemd/system/
sudo install -m 640 -o root -g fwwatch fw-alarm-watch.conf /etc/fw-alarm-watch.conf
rm -f fw-alarm-watch.conf                                   # enthält Token + Bark-Key
sudo install -d -m 700 -o fwwatch -g fwwatch /var/lib/fw-alarm-watch
sudo systemctl daemon-reload
sudo -u fwwatch /usr/local/bin/fw-alarm-watch --test        # Probe-Meldung an Bark
sudo systemctl start fw-alarm-watch.service                 # ein echter Prüflauf
journalctl -u fw-alarm-watch.service -n 5                   # muss „OK http=200 …“ zeigen
sudo systemctl enable --now fw-alarm-watch.timer
```

## Betrieb

- **Rückgabewerte:** `0` = Lauf erledigt (auch wenn das Ziel nicht erreichbar
  war – dann steht `FAIL n: …` in der Ausgabe), `1` = Zustand nicht
  speicherbar bzw. Probe-Meldung gescheitert, `2` = Konfiguration ungültig
  (z. B. `http://` statt `https://`, Zahl mit Buchstaben, unbekanntes
  `BARK_LEVEL`, ungültiger `TARGET_NAME`, Windows-Zeilenenden) oder unsichere
  Rechte. Bei `1` und `2` ruft das Skript das Ziel gar nicht erst auf: Ein
  Wächter, der nicht sauber arbeiten kann, soll dort als verstummt auffallen
  („Aussen-Waechter meldet sich nicht“), statt sich als gesund zu melden.
- **Geheimnisse:** Token und Bark-Key gibt das Skript per `curl -K -` über
  stdin weiter, nicht als Argument – sie tauchen also nicht in `ps` auf. Eine
  Sperre (`state/lock`) verhindert, dass zwei Läufe gleichzeitig arbeiten.
- **Probe-Meldung:** `… fw-alarm-watch.sh --test` schickt eine Meldung an den
  Status-Key (mit denselben Umgebungsvariablen wie in der Aufgabe).
- **Kommt „Aussen-Waechter meldet sich nicht“:** NAS zu Hause prüfen – Strom
  und Internet da? Läuft die DSM-Aufgabe `fw-alarm-watch` (Aufgabenplaner,
  „Ergebnis anzeigen“)?
- **Token wechseln:** neues Paar erzeugen, Hash in `config.php` des Servers,
  Klartext in die Konfiguration des Wächters – beides zusammen, sonst meldet
  er „HTTP 403“.
- **Abschalten:** die DSM-Aufgabe deaktivieren und `watcher_token_sha256` in
  `config.php` leeren (dann antwortet `alive.php` 404 und der Wächter auf dem
  Server prüft den Außen-Wächter nicht mehr).

## Tests

```bash
node --test "tests/watcher/*.test.mjs"
```

Prüft die Meldungstexte mit `TARGET_NAME`, die Prüfung der Konfiguration und
die Rechteprüfung als root (curl, flock, id und stat sind dabei Attrappen –
es gibt keinen Netzzugriff).
