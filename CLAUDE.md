# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Was dieses Projekt ist

ESP32-Firmware (Arduino-Framework, Board `esp32dev`) für eine inoffizielle
Feuerwehr-**Zusatz**alarmierung. Der ESP32 überwacht den potenzialfreien
Relaiskontakt eines Swissphone-Laders (DIN Pin 1→GND, Pin 3→GPIO27,
`INPUT_PULLUP`, LOW = Alarm). Schließt der Kontakt, sendet die Box über den Dienst
**Bark** einen iOS Critical Alert an alle Empfänger. Es wird **kein Funk/POCSAG
dekodiert** – einziger Eingang ist der trockene Relaiskontakt.

Kernprinzip im README betont und einzuhalten: **Zusatz, kein Ersatz** für den Melder.

## Build / Flash / Monitor

PlatformIO (CLI-Weg):
```bash
pio run                 # kompilieren
pio run -t upload       # auf den ESP32 flashen
pio device monitor      # serielle Ausgabe, 115200 Baud
```
`platformio.ini` setzt `src_dir = boss925_alarm_bark`, damit der Ordnername exakt
zur `.ino` passt – dadurch lässt sich dasselbe Projekt **ohne Änderung auch in der
Arduino IDE** öffnen (Board: „ESP32 Dev Module"). Es gibt keine Tests und kein Lint.

## Architektur

Alles liegt in `boss925_alarm_bark/`:
- `boss925_alarm_bark.ino` — die gesamte Firmware-Logik.
- `config.h` — **alle** vom Nutzer editierbaren Werte (Secrets). Per `.gitignore`
  ausgeschlossen; existiert lokal als Kopie von `config.example.h`.
- `config.example.h` — Vorlage mit Platzhaltern, im Git.

Der `.ino`-Code liest **nichts** fest verdrahtet; jeder konfigurierbare Wert kommt
als `#define` (bzw. das Array `BARK_KEYS_ALARM[]`) aus `config.h`. Beim Ändern von
Verhalten/Defaults daher **immer beide** Config-Dateien synchron halten.
Einzige Laufzeit-Ausnahme: Die aktiven Alarm-Empfänger (`alarmKeys[]`) kommen
nach dem ersten Dashboard-Sync aus dem NVS, nicht mehr aus `config.h`
(siehe Empfängerlisten-Bullet unten).

Zusätzlich gibt es `nas_dashboard/`: PHP-Dateien für Synology Web Station
(Nginx + PHP 8.3, kein Docker, keine DB). Das Dashboard ist ein optionales
Rendezvous-System für Monitoring und manuelle Befehle. Der ESP32 bleibt
outbound-only: Status-Push nach oben, Command-Polling nach oben. Bark muss auch
bei NAS-Ausfall unverändert unabhängig funktionieren.

Ablauf in `loop()`: `feedWatchdog()` → `maintainWiFi()` (nicht-blockierender
Reconnect) → `processPendingAlarm()` (Nachsende-Runden) → `handleHeartbeat()` →
`handleRemoteMonitor()` (NAS ACK/Poll/Status, kurze Timeouts + Backoff) →
Wiederbewaffnungs-/Cooldown-Gate → Erkennung (Pegel-Abfrage im loop **plus**
Interrupt-Latch) → `startAlarm()`. Alarm geht als `level=critical` an alle Keys,
Status/Heartbeat/Test als `level=passive` nur an `BARK_KEY_STATUS`.

Wichtige Eigenschaften, die beim Refactoring erhalten bleiben müssen:
- **Pro Key isoliert senden** mit Retry (`BARK_MAX_TRIES`); ein toter Key darf die
  anderen nicht blockieren. `sendBark()` gibt `bool` zurück; bei HTTP 4xx wird
  **nicht** wiederholt (Client-Fehler). `sendStatus()` gibt ebenfalls `bool` zurück.
- **Nachsende-Puffer**: `startAlarm()` merkt den Alarm (`alarmPending`, `keyDone[]`,
  Zeitstempel der Erkennung im Text); `processPendingAlarm()` versucht alle
  `ALARM_RETRY_MS` erneut die noch offenen Keys, bis alle versorgt sind oder
  `ALARM_RETRY_GIVEUP_MS` erreicht ist (dann Status-Warnung an den Owner).
  Nachgesendete Alarme bekommen den Zusatz „(verspaetet zugestellt)".
- **Echte Flankenerkennung**: Nach einem Alarm erst wieder scharf, wenn der Kontakt
  `REARM_OPEN_MS` stabil offen war (`waitingForRelease`) — ein dauerhaft
  geschlossener Kontakt darf NICHT periodisch neu alarmieren. Zusätzlich
  **Cooldown** `COOLDOWN_MS` als Mindestabstand. Bleibt der Kontakt
  `STUCK_CONTACT_WARN_MS` geschlossen → einmalige „Kontakt klemmt?"-Statuswarnung.
- **Interrupt-Latch** (`relayEdge`, `IRAM_ATTR`): misst die Impulsdauer per
  Pin-Interrupt und rastet Impulse ≥ `DEBOUNCE_MS` ein (`isrLatched`), damit kein
  Alarm verloren geht, während `loop()` in einer blockierenden HTTPS-Sendung steckt.
- **Entprellung** `DEBOUNCE_MS` gilt in beiden Pfaden (Pegel-Abfrage und ISR).
  Heartbeat/WLAN laufen auch während Cooldown/Wiederbewaffnung weiter.
- **Heartbeat** mit NTP = 1×/Kalendertag ab `HEARTBEAT_HOUR`; ohne NTP Fallback auf
  millis-Abstand (`HEARTBEAT_MS`). Als erledigt markiert wird **nur bei Erfolg**;
  nach Fehlversuch neuer Versuch nach `HEARTBEAT_RETRY_MS`. Solange ein Alarm
  offen ist (`alarmPending`), pausiert der Heartbeat (Alarm hat Vorrang).
- **NAS-Monitoring ist Nebenlogik**: `REMOTE_*` sendet Status per
  `application/x-www-form-urlencoded` an `api/status.php`, pollt `api/poll.php`
  und quittiert `api/ack.php`. Keine JSON-/HTTP-Zusatzbibliothek in der Firmware.
  NAS-Fehler/TLS/Timeouts sind nicht fatal, nutzen kurze Timeouts und Backoff und
  dürfen die Bark-Alarmierung nicht verhindern.
- **Manueller REAL ALARM läuft DIREKT vom NAS zu Bark** (`bark_send_alarm_all()`
  in `nas_dashboard/lib/common.php`, PHP-cURL, `level=critical`, pro Key isoliert
  mit Retry, HTTP 4xx ohne Retry). Bewusste Entscheidung: Die manuelle
  Alarmierung braucht man gerade dann, wenn die ESP32-Kette klemmt — sie darf
  deshalb nicht am Poll des ESP32 hängen. Das Dashboard legt keine
  `ALARM`-Commands mehr an; der `ALARM`-Zweig in der Firmware bleibt als
  Protokoll-Absicherung erhalten und läuft weiterhin durch dieselbe
  `startAlarm()`/`processPendingAlarm()`-Logik wie ein Relaisalarm.
  **TEST** läuft dagegen absichtlich über den ESP32 (Poll → `sendStatus()` an
  `BARK_KEY_STATUS`) — er testet die halbe Kette mit — und darf die
  Alarm-Zustandsmaschine nicht verändern.
- **Empfängerliste wird im Dashboard gepflegt** (eine Quelle für beide
  Alarmwege): versioniert in `alarm_keys.json(.php)` im `data_dir`, verwaltet
  über `dashboard_api.php` (`keys_list`/`keys_add`/`keys_delete`; letzter
  Empfänger nicht löschbar, max. 10, Key-Format validiert). `poll.php` liefert
  `keys_version=N` mit; weicht sie ab, holt der ESP32 die Liste über
  `api/keys.php` (Plain Text: `version=`, `count=`, `key0=`…, Nonce gespiegelt)
  und persistiert sie per `Preferences` im NVS (`syncAlarmKeys()`), damit sie
  Reboot/NAS-Ausfall übersteht. Sync **nie** während `alarmPending`; leere oder
  unplausible Listen werden verworfen; jede Übernahme wird per `sendStatus()`
  an den Owner gemeldet. `BARK_KEYS_ALARM` (config.h) und `bark_keys_alarm`
  (config.php) sind nur noch Start-/Fallback-Listen, solange die
  Dashboard-Liste leer ist (Version 0).
- **Demo-Modus (Dashboard, nur Admin)**: leitet beide Alarmwege auf genau einen
  Test-Empfänger aus der gepflegten Liste um — ohne Firmware-Änderung, über
  denselben Empfängerlisten-Mechanismus: `demo_mode.json` im `data_dir` hält den
  Zustand, `effective_alarm_keys()` in `common.php` liefert im Demo-Modus nur
  den Demo-Key (genutzt von `api/keys.php` UND `bark_send_alarm_all()`), jeder
  Moduswechsel erhöht die `keys_version` → ESP32 holt die Liste beim nächsten
  Poll. Schutzregeln: Demo-Empfänger muss aus der Liste stammen (beim
  Zurückschalten existiert so garantiert eine nicht-leere Liste, die der ESP32
  akzeptiert), der aktive Demo-Empfänger ist nicht löschbar, das Dashboard zeigt
  ein Warn-Banner mit Sync-Status (Vergleich ESP32-`keys_version` vs.
  NAS-Version). NAS-Direktalarm bekommt im Demo-Modus den Body-Zusatz
  `[DEMO-MODUS]`.
- **Command-Protokoll** ist Plain Text (`ok=1`, `nonce=...`, `id=...`,
  `type=...`). Der ESP32 schickt pro Poll eine Nonce, die das NAS spiegeln muss.
  Das NAS vergibt monotone IDs, markiert einen gepollten Befehl sofort persistent
  als `delivered` und sendet ihn nicht erneut; der ESP32 ACKt danach. Diese
  at-most-once-Auslieferung verhindert Replay-Doppelalarme bei NAS-Reboot oder
  verlorenen alten Antworten. ACK-Verlust führt zu Dashboard-Status `delivered`,
  nicht zu heimlichem Neuauslösen. Ein nicht abgeholter `pending`-Befehl
  verfällt nach `pending_command_ttl_seconds` (Default 300 s) als `expired` und
  ist im Dashboard abbrechbar (`cancelled`) — ein Befehl an einen toten ESP32
  darf nicht Stunden später noch ausgeführt werden.
- **Kein Probealarm-Fenster mehr**: Der wöchentliche ILS-Probealarm löst bewusst
  den vollen Alarm aus (testet die ganze Kette wöchentlich mit). Nicht wieder
  einbauen, außer der Nutzer wünscht es.
- Bark-Versand: simple `application/x-www-form-urlencoded`-POST mit manuellem
  `urlEncode()` — **keine JSON-/HTTP-Bibliothek** hinzufügen. Nur ESP32-Core-Libs
  (`WiFi`, `HTTPClient`, `WiFiClientSecure`, `time.h`, `esp_task_wdt`,
  `esp_timer`, `Preferences`).
- Watchdog-Init ist core-versionsabhängig (`ESP_ARDUINO_VERSION_MAJOR >= 3` vs. 2.x)
  — beide Zweige beibehalten. Im 3.x-Zweig ist der Task-Watchdog ab Werk schon
  initialisiert: liefert `esp_task_wdt_init()` `ESP_ERR_INVALID_STATE`, muss
  `esp_task_wdt_reconfigure()` folgen, sonst bleiben die Default-Werte (5 s, kein
  Reset) aktiv.

## Bewusste Entscheidungen (nicht „wegoptimieren")

- TLS via `client.setInsecure()` (keine Zertifikatsprüfung) — gewollt, einfach/robust.
  Gilt auch für das NAS; Authentifizierung läuft über ein langes Maschinen-Token.
- OTA ist **nicht** eingebaut, nur als README-TODO dokumentiert.
- Alarm-Sound `alarm_fw` ist ein **eigener importierter** Bark-Ton; muss auf jedem
  Empfänger-iPhone vorhanden sein, sonst Standardton.
- Synology-Dashboard: separate Auth für Maschine (`REMOTE_MACHINE_TOKEN`, im PHP
  nur SHA-256-Hash, **nur per POST**) und Mensch (Dashboard-Login mit
  Passwort-Hash + CSRF, `sleep(1)` plus IP-Sperrfenster nach zu vielen
  Fehlversuchen als Brute-Force-Bremse, Session-Idle-Timeout 12 h,
  Sicherheits-Header/CSP zentral oben in `common.php`, `commands.log`-Rotation
  ab 5 MB, Assets mit `?v=filemtime()`-Cache-Busting). Zusätzlich optionaler
  Lese-Benutzer (`dashboard_readonly_user`/`..._password_hash`, Rolle in
  `$_SESSION['role']`): sieht Status + Empfängerliste, alle ändernden/auslösenden
  Aktionen werden **serverseitig** per `require_dashboard_admin()` geblockt
  (UI-Ausblendung ist nur Komfort). Keine Geheimnisse in
  clientseitigem JavaScript. Laufzeitdaten (`data_dir`) bei Nginx möglichst
  außerhalb des Webroots ablegen; zusätzlich heißen alle Laufzeitdateien `*.php`
  mit Guard-Zeile (`DATA_FILE_GUARD`), damit Nginx sie nie als statische Datei
  ausliefert — Web Station ignoriert `.htaccess`. Diese Namens-/Guard-Logik
  steckt zentral in `data_path()`/`write_json_file()`/`read_json_file()`.
- `REMOTE_HTTP_TIMEOUT_MS` nicht wieder unter ~4 s drücken: Der TLS-Handshake
  ESP32↔Synology braucht typischerweise 1–3 s; zu knappe Timeouts führen zu
  Dauer-Backoff und einem flappenden ONLINE/OFFLINE-Status.

## Konventionen

- Kommentare und Nutzertexte auf **Deutsch mit echten Umlauten (ä/ö/ü/ß)**, UTF-8 –
  auch in Git-Commit-Nachrichten. **Ausnahme:** Strings, die als `title`/`body` an
  Bark gehen (`ALARM_TITLE`, `ALARM_BODY`, die `sendStatus()`-Texte), müssen **ASCII
  (ae/ue/oe)** sein, weil die Bark-Notification keine Umlaute korrekt darstellt.
- Editierbare Werte in den Config-Dateien klar oben markiert lassen
  (`>>> HIER STEHEN ALLE WERTE … <<<`).
- Secrets niemals in `config.example.h` oder die `.ino` schreiben.
