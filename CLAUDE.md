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

## Architektur (eine Quelle, drei Dateien)

Alles liegt in `boss925_alarm_bark/`:
- `boss925_alarm_bark.ino` — die gesamte Firmware-Logik.
- `config.h` — **alle** vom Nutzer editierbaren Werte (Secrets). Per `.gitignore`
  ausgeschlossen; existiert lokal als Kopie von `config.example.h`.
- `config.example.h` — Vorlage mit Platzhaltern, im Git.

Der `.ino`-Code liest **nichts** fest verdrahtet; jeder konfigurierbare Wert kommt
als `#define` (bzw. das Array `BARK_KEYS_ALARM[]`) aus `config.h`. Beim Ändern von
Verhalten/Defaults daher **immer beide** Config-Dateien synchron halten.

Ablauf in `loop()`: `feedWatchdog()` → `maintainWiFi()` (nicht-blockierender
Reconnect) → `processPendingAlarm()` (Nachsende-Runden) → `handleHeartbeat()` →
Wiederbewaffnungs-/Cooldown-Gate → Erkennung (Pegel-Abfrage im loop **plus**
Interrupt-Latch) → `startAlarm()`. Alarm geht als `level=critical` an alle Keys,
Status/Heartbeat als `level=passive` nur an `BARK_KEY_STATUS`.

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
- **Kein Probealarm-Fenster mehr**: Der wöchentliche ILS-Probealarm löst bewusst
  den vollen Alarm aus (testet die ganze Kette wöchentlich mit). Nicht wieder
  einbauen, außer der Nutzer wünscht es.
- Bark-Versand: simple `application/x-www-form-urlencoded`-POST mit manuellem
  `urlEncode()` — **keine JSON-/HTTP-Bibliothek** hinzufügen. Nur ESP32-Core-Libs
  (`WiFi`, `HTTPClient`, `WiFiClientSecure`, `time.h`, `esp_task_wdt`).
- Watchdog-Init ist core-versionsabhängig (`ESP_ARDUINO_VERSION_MAJOR >= 3` vs. 2.x)
  — beide Zweige beibehalten. Im 3.x-Zweig ist der Task-Watchdog ab Werk schon
  initialisiert: liefert `esp_task_wdt_init()` `ESP_ERR_INVALID_STATE`, muss
  `esp_task_wdt_reconfigure()` folgen, sonst bleiben die Default-Werte (5 s, kein
  Reset) aktiv.

## Bewusste Entscheidungen (nicht „wegoptimieren")

- TLS via `client.setInsecure()` (keine Zertifikatsprüfung) — gewollt, einfach/robust.
- OTA ist **nicht** eingebaut, nur als README-TODO dokumentiert.
- Alarm-Sound `alarm_fw` ist ein **eigener importierter** Bark-Ton; muss auf jedem
  Empfänger-iPhone vorhanden sein, sonst Standardton.

## Konventionen

- Kommentare und Nutzertexte auf **Deutsch mit echten Umlauten (ä/ö/ü/ß)**, UTF-8 –
  auch in Git-Commit-Nachrichten. **Ausnahme:** Strings, die als `title`/`body` an
  Bark gehen (`ALARM_TITLE`, `ALARM_BODY`, die `sendStatus()`-Texte), müssen **ASCII
  (ae/ue/oe)** sein, weil die Bark-Notification keine Umlaute korrekt darstellt.
- Editierbare Werte in den Config-Dateien klar oben markiert lassen
  (`>>> HIER STEHEN ALLE WERTE … <<<`).
- Secrets niemals in `config.example.h` oder die `.ino` schreiben.
