# Synology-Setup für das BOSS-925 Dashboard

## 1. Pakete installieren

Installiere im Synology Paket-Zentrum:

- **Web Station**
- **PHP 8.3**

Docker wird nicht benötigt.
Nginx als Back-end ist in Ordnung; Apache ist nicht erforderlich.

Prüfe im PHP-Profil (Web Station → Skriptsprache-Einstellungen → PHP-Profil
bearbeiten → Erweiterungen), dass **curl** aktiviert ist – der manuelle
REAL ALARM sendet damit direkt vom NAS an Bark.

## 2. Dateien ablegen

Kopiere den Inhalt von `nas_dashboard/` in den Web-Ordner der Synology, zum Beispiel:

```text
/volume1/web/fw_alarm/
```

Kopiere danach:

```text
config.example.php -> config.php
```

`config.php` bleibt auf der Synology und enthält deine echten Geheimnisse
(Token-Hash, Passwort-Hash und die Bark-Keys für den NAS-Direktalarm).

### Datenordner (`data_dir`)

Der Datenordner muss für den **PHP-Prozess beschreibbar** sein. Auf DSM:
File Station → Rechtsklick auf den Ordner → Eigenschaften → Berechtigung → dem
Benutzer, unter dem PHP läuft (bzw. der Gruppe `http`), Lesen/Schreiben geben
und auf Unterordner anwenden. Ob es funktioniert, siehst du daran, dass nach dem
ersten Status-Push des ESP32 eine `latest_status.json.php` erscheint; vorher
antworten die APIs mit HTTP 500.

**Lege den Ordner UNBEDINGT außerhalb des Web-Roots ab** (also NICHT unter
`/volume1/web/…`). Hier liegen die Bark-Keys im **Klartext**. Ein Pfad im
ausgelieferten Baum ist nur durch die Guard-Zeile (unten) geschützt – eine
einzige, zerbrechliche Schicht. Bewährt auf dieser Installation:
`/volume1/web_packages/fw_alarm_data` (per URL nicht erreichbar, PHP darf
schreiben). Alternativ eine eigene DSM-Freigabe außerhalb von `/volume1/web`.
Beachte ggf. `open_basedir` im PHP-Profil.

Alle Laufzeitdateien heißen zusätzlich absichtlich `*.php` und beginnen mit einer
Guard-Zeile (`<?php http_response_code(404); exit; ?>`): Nginx liefert sie
dadurch **nie** als statische Datei aus – wichtig, weil Web Station (Nginx)
`.htaccess`-Dateien ignoriert. Das ist die **zweite** Schicht (Verteidigung in
der Tiefe), ersetzt aber NICHT den Ordner außerhalb des Web-Roots.

## 3. Geheimnisse erzeugen

Maschinen-Token für den ESP32:

```bash
php -r "echo bin2hex(random_bytes(32)), PHP_EOL;"
php -r "echo hash('sha256', 'HIER_DEN_TOKEN_EINFUEGEN'), PHP_EOL;"
```

Den Klartext-Token trägst du in `boss925_alarm_bark/config.h` als
`REMOTE_MACHINE_TOKEN` ein. In `nas_dashboard/config.php` kommt nur der
SHA-256-Hash. **Klartext und Hash müssen zueinander passen** – wenn du den
Klartext verlierst, erzeuge beides neu.

Dashboard-Passwort:

```bash
php -r "echo password_hash('DEIN_DASHBOARD_PASSWORT', PASSWORD_DEFAULT), PHP_EOL;"
```

Den Hash trägst du in `dashboard_password_hash` ein.

Optionaler **Lese-Benutzer** (z. B. für Kameraden, die nur den Status sehen
sollen): In `config.php` `dashboard_readonly_user` und
`dashboard_readonly_password_hash` setzen (Hash genauso mit `password_hash()`
erzeugen). Dieser Benutzer sieht Status und Empfängerliste (Keys maskiert),
kann aber weder TEST noch REAL ALARM auslösen und die Liste nicht ändern –
die Sperre greift serverseitig, nicht nur im Browser. Beide Werte leer = Login aus.

Bark-Keys der Alarm-Empfänger: **im Dashboard pflegen** (Panel
„Alarm-Empfänger"). Die Liste ist versioniert, gilt für beide Alarmwege und
wird vom ESP32 automatisch übernommen und im NVS gespeichert.
`bark_keys_alarm` in `config.php` und `BARK_KEYS_ALARM` in `config.h` sind nur
Start-/Fallback-Listen, solange die Dashboard-Liste leer ist.

## 4. HTTPS und Portweiterleitung

Nutze Synology DDNS oder eine eigene Domain, zum Beispiel:

```text
dein-name.synology.me
```

Erstelle in DSM ein Let's-Encrypt-Zertifikat für diesen Hostnamen und weise es Web
Station zu. Leite an deinem Router genau **TCP-Port 443** von außen auf die
Synology weiter.

Dashboard-URL (extern bzw. im LAN):

```text
https://dein-name.synology.me/fw_alarm/
https://192.168.1.50/fw_alarm/
```

ESP32-API-Basis-URL für `REMOTE_BASE_URL` – im LAN am robustesten die lokale
IP (funktioniert dank `setInsecure()` trotz Zertifikat-Mismatch und auch ohne
Internet):

```text
https://192.168.1.50/fw_alarm/api
```

## 5. Offline-Wächter (optional, empfohlen)

Der tägliche Heartbeat kommt vom ESP32 selbst – eine tote Box schweigt einfach,
und ohne offenes Dashboard merkt das niemand. `cron/check_offline.php` schließt
die Lücke: Es prüft das Alter des letzten Status-Pushs und schickt dir **einmal**
eine leise Bark-Meldung, wenn die Box zu lange schweigt (Default: 10 Minuten,
`watchdog_offline_after_seconds`), und **einmal**, wenn sie wieder da ist.

Einrichtung:

1. In `config.php` deinen Status-Bark-Key eintragen (derselbe wie
   `BARK_KEY_STATUS` in `config.h`):

   ```php
   'bark_key_status' => 'DEIN_BARK_KEY',
   ```

2. DSM → **Systemsteuerung → Aufgabenplaner → Erstellen → Geplante Aufgabe →
   Benutzerdefiniertes Skript**:
   - Benutzer: `root` (oder ein Benutzer mit Leserecht auf den Web-Ordner)
   - Zeitplan: täglich, **alle 15 Minuten** wiederholen
   - Skript:

     ```bash
     php -f /volume1/web/fw_alarm/cron/check_offline.php
     ```

     Falls `php` nicht gefunden wird, den vollen Pfad des PHP-8.3-Pakets
     verwenden (z. B. `/usr/local/bin/php83` – per SSH mit `which php83`
     prüfbar).

3. Aufgabe einmal manuell ausführen („Ausführen") und das Ergebnis ansehen
   (Aufgabenplaner → Aktion → „Ergebnis anzeigen"): `OK` bzw. `NO_DATA` ist
   gut; `DISABLED` heißt, der Key aus Schritt 1 fehlt noch.

Das Skript ist auch per HTTPS aufrufbar (POST mit dem Maschinen-Token wie bei
`api/status.php`) – anonyme Web-Aufrufe weist es mit 403 ab. Derselbe
`bark_key_status` aktiviert außerdem eine Meldung, wenn eine IP wegen zu
vieler Login-Fehlversuche gesperrt wurde.

Der Wächter übernimmt noch zwei weitere Aufgaben:

- **Demo-Modus-Erinnerung**: Bleibt der Demo-Modus länger als
  `demo_reminder_after_seconds` (Default 4 h) aktiv, erinnert er leise per
  Bark daran (danach max. 1× pro Tag) – im Demo-Modus erreichen echte Alarme
  nur den Test-Empfänger.
- **Selbstüberwachung**: Jeder Lauf wird protokolliert; das Dashboard zeigt
  „Offline-Wächter: zuletzt gelaufen vor X min" und warnt, wenn die Aufgabe
  nie oder seit mehr als 35 Minuten nicht gelaufen ist – also mehr als zwei
  verpasste 15-Minuten-Läufe (z. B. weil sie im Aufgabenplaner deaktiviert
  wurde).

### Backup der Laufzeitdaten

Empfängerliste, Demo-Zustand und Verlauf leben im `data_dir`
(außerhalb des Web-Roots, z. B. `/volume1/web_packages/fw_alarm_data`).
Nimm den Ordner in dein NAS-Backup auf
(z. B. Hyper Backup), wenn du eins hast. Zusätzlich hält das Dashboard vor
jeder Änderung der Empfängerliste die Vorversion als `*.bak.php`-Datei fest –
Wiederherstellen = Datei umbenennen.

## 6. Härtung

- Wähle ein langes Maschinen-Token und ein separates starkes Dashboard-Passwort.
- Aktiviere DSM-Firewall und Auto-Block für wiederholte Fehlversuche (schützt
  DSM; das Dashboard-Login bremst Brute-Force zusätzlich mit `sleep(1)` **und**
  sperrt eine IP nach zu vielen Fehlversuchen für ein Zeitfenster –
  `login_max_failures` / `login_lockout_window_seconds` in `config.php`).
- Eingebaut sind außerdem: Sicherheits-Header (u. a. Content-Security-Policy,
  `X-Frame-Options`, HSTS bei HTTPS, zentral in `lib/common.php`), ein
  Session-Ablauf nach 12 h Inaktivität, Rotation von `commands.log` ab 5 MB
  sowie `index.php`-Platzhalter gegen Verzeichnislisting in `api/`, `lib/`,
  `data/` und im `data_dir`.
- Lösche nach der Einrichtung `config.example.php` und `SETUP.md` aus dem
  Webroot auf dem NAS – sie verraten Angreifern nur die Struktur (das Original
  bleibt ja im Git-Repo).
- Halte DSM, Web Station und PHP aktuell.
- Gib keine PHP-Fehler öffentlich aus. Die Dateien selbst senden nur knappe
  Fehlermeldungen.
- `config.php` ist nur so lange geschützt, wie PHP ausgeführt wird – wenn PHP
  im Web-Station-Profil deaktiviert wird, wäre der Quelltext (inkl. Bark-Keys)
  herunterladbar. Nach Änderungen an Web Station kurz prüfen, dass
  `https://…/fw_alarm/config.php` eine leere Seite liefert.

## Design

Der ESP32 sendet Status per Formular-POST an `api/status.php` und pollt
`api/poll.php` auf Befehle (alle 10 s, 4 s Timeout, 30 s Backoff bei Fehlern).
Die Poll-Antwort ist ein kleines Textprotokoll:

```text
ok=1
nonce=esp32nonce
id=17
type=TEST
```

Ohne Befehl kommt `id=0` und `type=NONE`. Die `nonce` wird vom ESP32 im Poll
mitgeschickt und vom NAS gespiegelt; Antworten mit falscher Nonce werden
verworfen, damit alte Poll-Antworten nicht versehentlich erneut wirken. Ein neuer
Befehl liegt als `pending` in `command_state.json.php`. Beim Poll wird er unter
Dateisperre sofort als `delivered` markiert und genau einmal ausgeliefert; der
ESP32 quittiert danach `api/ack.php`. Alte, quittierte oder bereits ausgelieferte
Befehle werden nicht erneut gesendet, auch nicht nach NAS-Neustart. Diese
Entscheidung schützt vor Replay-Doppelalarmen. Wenn die ACK verloren geht, wird
nicht heimlich erneut ausgelöst; das Dashboard zeigt den offenen Zustand, und ein
neuer manueller Befehl bekommt eine neue monotone ID. Ein `pending`-Befehl, den
der ESP32 nicht innerhalb `pending_command_ttl_seconds` (Default 300 s) abholt,
verfällt automatisch als `expired` und lässt sich im Dashboard auch aktiv
abbrechen.

Über das Command-Protokoll läuft nur noch **TEST** (leise Bark-Meldung vom ESP32
an den Status-Empfänger – testet WLAN, NAS-Anbindung und Bark-Versand der Box).
Der manuelle **REAL ALARM** wird direkt vom NAS per cURL an Bark gesendet
(`level=critical`, pro Key isoliert mit Retry, HTTP 4xx ohne Retry): Manuelle
Alarmierung muss gerade dann funktionieren, wenn die ESP32-Kette klemmt.

Die **Empfängerliste** liegt versioniert in `alarm_keys.json.php` im `data_dir`
und wird im Dashboard verwaltet. `poll.php` meldet dem ESP32 bei jedem Poll die
aktuelle `keys_version`; weicht sie von seiner ab, holt er die Liste über
`api/keys.php` (gleiches Text-Protokoll, Token-Auth, gespiegelte Nonce) und
speichert sie im NVS-Flash. Leere oder unplausible Listen verwirft er, während
eines offenen Alarms synchronisiert er nicht, und jede Übernahme bestätigt er
per Statusmeldung an den Owner.

Der **Demo-Modus** (Panel „Betriebsmodus", nur Admin) leitet ALLE Alarme –
Relaisalarm über den ESP32 und REAL ALARM vom NAS – auf genau einen
Test-Empfänger aus der Liste um. Er nutzt denselben Mechanismus: `demo_mode.json.php`
im `data_dir` merkt sich den Zustand, `api/keys.php` liefert im Demo-Modus nur
den Demo-Key, und jeder Moduswechsel erhöht die `keys_version`, sodass der ESP32
die passende Liste beim nächsten Poll übernimmt (Firmware unverändert). Das
Dashboard zeigt ein Warn-Banner, solange Demo aktiv ist, inklusive der Info, ob
der ESP32 die Demo-Liste schon übernommen hat – **vorher nicht testen**, sonst
alarmiert ein Relaistest noch alle. Der aktive Demo-Empfänger ist nicht löschbar,
und der Demo-Empfänger muss aus der gepflegten Liste stammen (dadurch gibt es
beim Zurückschalten garantiert wieder eine nicht-leere Liste). Nach dem Testen
zurück auf LIVE schalten – das Banner erinnert daran.

TLS läuft über HTTPS mit Let's Encrypt auf der Synology. Auf dem ESP32 wird wie
beim bestehenden Bark-Pfad `setInsecure()` verwendet: Das ist auf Mikrocontrollern
robuster gegen Root-CA-/Uhrzeit-Probleme. Die Vertraulichkeit hängt dadurch stärker
am schwer erratbaren Maschinen-Token (nur per POST akzeptiert); das Dashboard
selbst nutzt eine separate Session-Anmeldung, Passwort-Hash und CSRF-Schutz.
