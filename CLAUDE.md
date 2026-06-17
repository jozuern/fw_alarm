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
Reconnect) → `handleHeartbeat()` → Cooldown-Gate → Entprellte Flankenerkennung am
Relais-Pin → `sendAlarmToAll()`. Alarm geht als `level=critical` an alle Keys,
Status/Heartbeat als `level=passive` nur an `BARK_KEY_STATUS`.

Wichtige Eigenschaften, die beim Refactoring erhalten bleiben müssen:
- **Pro Key isoliert senden** mit Retry (`BARK_MAX_TRIES`); ein toter Key darf die
  anderen nicht blockieren. `sendBark()` gibt `bool` zurück.
- **Entprellung** `DEBOUNCE_MS`, **Cooldown** `COOLDOWN_MS` (eine Sende-Runde pro
  Alarm). Heartbeat/WLAN laufen auch während des Cooldowns weiter.
- **Heartbeat** mit NTP = 1×/Kalendertag ab `HEARTBEAT_HOUR`; ohne NTP Fallback auf
  millis-Abstand (`HEARTBEAT_MS`).
- **Probealarm-Fenster** (`TESTALARM_*`): wöchentlicher ILS-Test (z.B. Mi 18:55–19:10)
  löst denselben Kontakt aus; in diesem Fenster KEIN Broadcast, nur leiser Status an
  den Owner. `inTestWindow()` gibt ohne gültige NTP-Zeit `false` zurück (fail-safe:
  im Zweifel normal alarmieren).
- Bark-Versand: simple `application/x-www-form-urlencoded`-POST mit manuellem
  `urlEncode()` — **keine JSON-/HTTP-Bibliothek** hinzufügen. Nur ESP32-Core-Libs
  (`WiFi`, `HTTPClient`, `WiFiClientSecure`, `time.h`, `esp_task_wdt`).
- Watchdog-Init ist core-versionsabhängig (`ESP_ARDUINO_VERSION_MAJOR >= 3` vs. 2.x)
  — beide Zweige beibehalten.

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
