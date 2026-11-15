/*
  config.example.h  ->  KOPIERE diese Datei nach "config.h" und trage deine
  echten Werte ein. config.h ist per .gitignore vom Versionskontroll-System
  ausgeschlossen, damit deine WLAN- und Bark-Zugangsdaten NICHT im Git landen.

  In der Arduino IDE:  Datei im selben Ordner als "config.h" speichern.
  Mit PlatformIO:      cp boss925_alarm_bark/config.example.h boss925_alarm_bark/config.h

  ===========================================================================
  >>>>>>>>>>  HIER STEHEN ALLE WERTE, DIE DU EDITIEREN MUSST/KANNST  <<<<<<<<<
  ===========================================================================
*/
#pragma once

// ===========================================================================
//  1) WLAN  (PFLICHT)
// ===========================================================================
#define WIFI_SSID       "DEIN_WLAN_NAME"
#define WIFI_PASSWORD   "DEIN_WLAN_PASSWORT"

// Bricht das WLAN ab, verbindet der ESP32-Core zuerst selbst neu
// (AutoReconnect). Erst wenn so lange keine Verbindung zustande kam, baut die
// Firmware sie hart neu auf (nicht blockierend, danach höchstens in diesem
// Abstand). Fehlt der Wert in einer älteren config.h, gilt 60 s.
#define WIFI_RECONNECT_MS   (60UL*1000UL)

// ===========================================================================
//  2) BARK  (PFLICHT)
// ===========================================================================
// Öffentlicher Server, oder dein selbst gehosteter (OHNE abschließenden "/").
#define BARK_HOST       "https://api.day.app"

// Device-Keys der Alarm-Empfänger (bis zu 10). Jede Person sieht ihren Key
// in der Bark-App. Leere Einträge ("") werden übersprungen.
// HINWEIS: Das ist nur die STARTLISTE für den allerersten Boot. Sobald im
// NAS-Dashboard eine Empfängerliste gepflegt wird, übernimmt der ESP32 diese
// per Sync und speichert sie im NVS-Flash - die Dashboard-Liste ist dann
// führend und diese Einträge hier werden ignoriert.
// WICHTIG: Pro Gerät die iOS-Funktion "Kritische Hinweise" erlauben
//          (siehe README), sonst kommt der Alarm nur als normale Mitteilung.
static const char* BARK_KEYS_ALARM[] = {
  "KEY_PERSON_1",
  "KEY_PERSON_2",
  // "KEY_PERSON_3",
  // ... bis zu 10
};

// Dein eigener Device-Key für Status/Heartbeat (NUR du, leise).
#define BARK_KEY_STATUS "DEIN_EIGENER_KEY"

// ===========================================================================
//  3) ALARM-INHALT  (optional anpassbar)
// ===========================================================================
// ACHTUNG: Bark zeigt in der Notification KEINE Umlaute -> hier bewusst
// ae/ue/oe statt ä/ü/ö verwenden (gilt nur fuer an Bark gesendete Texte).
#define ALARM_TITLE     "FEUERWEHR-ALARM"
#define ALARM_BODY      "Melder hat ausgeloest!"
#define ALARM_SOUND     "alarm_fw"  // Bark-Soundname (hier ein eigener importierter)
#define ALARM_VOLUME    10          // 0-10, gilt nur bei level=critical
#define ALARM_CALL      true        // call=1 -> Ton wiederholt sich ~30 s

// --- Wöchentlicher Probealarm der Leitstelle (ca. 19 Uhr) ------------------
// Alarme, die INNERHALB dieses Zeitfensters erkannt werden, gehen mit
// PROBE_ALARM_VOLUME statt ALARM_VOLUME raus - sonst unverändert (gleicher
// Text, gleicher Ton, weiterhin level=critical). Ohne NTP-Zeit oder an einem
// anderen Wochentag gilt immer die volle Lautstärke: im Zweifel lieber zu
// laut als zu leise.
#define PROBE_WEEKDAY          3    // 0=So, 1=Mo ... 6=Sa; -1 = jeden Tag
#define PROBE_WINDOW_START_MIN (18*60 + 55)   // 18:55 (Minuten seit Mitternacht)
#define PROBE_WINDOW_END_MIN   (19*60 +  5)   // 19:05 (einschließlich)
#define PROBE_ALARM_VOLUME     5    // 0-10, Lautstärke im Probealarm-Fenster

// Status-/Heartbeat-Ton (leise, level=passive)
#define STATUS_SOUND    "minuet"
#define STATUS_VOLUME   3

// ===========================================================================
//  4) NAS-DASHBOARD / FERNÜBERWACHUNG  (optional, aber empfohlen)
// ===========================================================================
// Die Box bleibt outbound-only: Status wird zum NAS gepusht, Kommandos werden
// gepollt. Basis-URL OHNE abschließenden Slash, zeigt auf den api-Ordner.
// TIPP: Wenn ESP32 und NAS im selben LAN sind, die lokale IP des NAS nutzen
// (z.B. "https://192.168.1.50/fw_alarm/api") - funktioniert dank
// setInsecure() trotz Zertifikat-Mismatch und auch ohne Internet/Hairpin-NAT.
#define REMOTE_MONITOR_ENABLED       true
#define REMOTE_BASE_URL              "https://dein-name.synology.me/fw_alarm/api"
#define REMOTE_MACHINE_TOKEN         "LANGER_ZUFAELLIGER_TOKEN_AUS_CONFIG_PHP"
#define REMOTE_DEVICE_ID             "boss925-01"
// Frei wählbare Kennung fürs Dashboard; die Firmware hängt automatisch Datum
// und Uhrzeit des Kompilierens an.
#define FW_BUILD_MARKER              "boss925-monitor-2026-11-15"

// Status alle 60 s: schnell genug für "lebt noch", aber sparsam für NAS/ESP32.
// Ereignisse (Alarm, WLAN-Reconnect, Heartbeat-Ergebnis) werden zusätzlich
// zeitnah geschickt, aber per MIN_EVENT_GAP entprellt.
#define REMOTE_STATUS_INTERVAL_MS     (60UL*1000UL)
#define REMOTE_STATUS_MIN_EVENT_GAP_MS 5000UL

// Poll alle 10 s: Jeder Poll ist ein voller TLS-Handshake, der loop() 1-2 s
// blockiert (Relais-Impulse fängt derweil das ISR-Latch). Über den Poll laufen
// nur der TEST-Befehl und im Demo-Modus der DEMO-ALARM - der manuelle REAL
// ALARM geht direkt vom NAS an Bark. Fehler bekommen Backoff (Status-Push
// getrennt von Poll/ACK), damit ein unerreichbares NAS nicht bremst.
#define REMOTE_POLL_INTERVAL_MS       (10UL*1000UL)
#define REMOTE_ERROR_BACKOFF_MS       (30UL*1000UL)
// Nicht zu knapp: TLS-Handshake ESP32<->Synology dauert typischerweise 1-3 s.
// Bei 1,5 s würde fast jeder Request abbrechen und im Backoff landen.
// Gilt auch als Limit für den TLS-Handshake (aufgerundet, mindestens 4 s).
#define REMOTE_HTTP_TIMEOUT_MS        4000UL
// Kommt die Quittung (ACK) eines Dashboard-Befehls so oft hintereinander nicht
// beim NAS an, wird sie verworfen - sonst bliebe das Polling (und damit der
// Abgleich der Empfängerliste) dauerhaft stehen. Das Dashboard zeigt den
// Befehl dann als "ausgeliefert, Bestätigung fehlt"; er wird NICHT erneut
// ausgeführt. Fehlt der Wert in einer älteren config.h, gilt 10.
#define REMOTE_ACK_MAX_TRIES          10
// Verwirft die Box eine Empfängerliste vom NAS (unplausible Antwort), fragt
// sie frühestens nach dieser Pause erneut nach (und meldet das EINMAL an
// BARK_KEY_STATUS). Fehlt der Wert in einer älteren config.h, gilt 60 s.
#define KEY_SYNC_REJECT_BACKOFF_MS    (60UL*1000UL)

// ===========================================================================
//  5) RELAIS-EINGANG  (Pin & Polarität)
// ===========================================================================
// Pin MUSS einen internen Pull-up (bei ACTIVE_LOW false: Pull-down) können:
// NICHT GPIO34-39 verwenden (die haben keins von beiden)!
// Gut: GPIO27, 25, 26, 32, 33. (GPIO27 ist kein Strapping-Pin -> ideal.)
#define RELAY_PIN       27
// true  = Kontakt gegen GND -> LOW = Alarm (interner Pull-up, Standard).
// false = Kontakt gegen 3,3 V -> HIGH = Alarm; die Firmware schaltet dann den
//         internen Pull-down ein (die Pins oben können beides).
#define ACTIVE_LOW      true

// Optionaler 2. Eingang als Fallback/Parallel-Kontakt (z.B. zweite Lötbrücke
// oder Reserve-Verdrahtung). Löst aus, sobald EINER der beiden Pins aktiv ist.
#define RELAY_PIN2_ENABLED  false
#define RELAY_PIN2          26

// ===========================================================================
//  6) TIMINGS  (Logik)
// ===========================================================================
#define DEBOUNCE_MS     300UL                   // Kontakt muss so lange aktiv sein
#define COOLDOWN_MS     (5UL*60UL*1000UL)       // 5 min Sperre nach einem Alarm
#define BARK_MAX_TRIES  3                       // 1. Versuch + 2 Wiederholungen

// Nach einem Alarm muss der Kontakt erst so lange stabil OFFEN gewesen sein,
// bevor ein NEUER Alarm ausgelöst werden kann (verhindert Dauerfeuer, wenn der
// Kontakt lange geschlossen bleibt, z.B. bis jemand den Melder quittiert).
#define REARM_OPEN_MS   2000UL                  // 2 s stabil offen = wieder scharf

// Bleibt der Kontakt nach einem Alarm SO lange dauerhaft geschlossen, geht
// einmalig eine leise Warnung an BARK_KEY_STATUS ("Kontakt klemmt?"). Öffnet
// er danach wieder, folgt eine leise Entwarnung ("Kontakt wieder offen").
#define STUCK_CONTACT_WARN_MS  (30UL*60UL*1000UL)   // 30 min

// Nachsenden: War WLAN/Internet im Alarm-Moment weg, wird der Alarm gemerkt
// und regelmäßig erneut versucht (pro Empfänger, bis alle versorgt sind).
// Lehnt Bark einen Key dauerhaft ab (HTTP 400/401/403/404), wird er sofort
// aufgegeben und BARK_KEY_STATUS gewarnt (Key nur maskiert).
#define ALARM_RETRY_MS         (60UL*1000UL)        // Abstand zwischen Nachsende-Runden
#define ALARM_RETRY_GIVEUP_MS  (15UL*60UL*1000UL)   // danach aufgeben (Meldung zu alt)

// Warnungen an dich (Zustellung unvollständig, Kontakt klemmt, Liste
// übernommen ...) gelten erst als erledigt, wenn Bark sie angenommen hat.
// Schlägt der Versand fehl, neuer Versuch nach dieser Wartezeit (nie während
// einer laufenden Alarm-Zustellung). Fehlt der Wert in einer älteren
// config.h, gilt 5 min.
#define STATUS_RETRY_MS        (5UL*60UL*1000UL)

// ===========================================================================
//  7) HEARTBEAT  ("ich lebe noch"-Ping nur an dich)
// ===========================================================================
#define HEARTBEAT_ENABLED   true
// Wenn NTP aktiv: täglich zu dieser Stunde (0-23, lokale Zeit).
#define HEARTBEAT_HOUR      8
// Fallback ohne NTP: fester Abstand (alle 24 h ab Start).
#define HEARTBEAT_MS        (24UL*60UL*60UL*1000UL)
// Schlägt ein Heartbeat-Versand fehl (z.B. WLAN-Schluckauf), wird er nach
// dieser Wartezeit erneut versucht - nicht erst am nächsten Tag.
#define HEARTBEAT_RETRY_MS  (5UL*60UL*1000UL)   // 5 min

// Hinweis: Der wöchentliche Probealarm der ILS löst ABSICHTLICH den vollen
// Alarm an alle aus - so wird die komplette Kette (Melder -> Relais -> ESP32
// -> WLAN -> Bark -> iPhones) jede Woche automatisch mitgetestet.

// ===========================================================================
//  8) NTP-ZEIT  (Zeitstempel in Nachrichten, feste Heartbeat-Uhrzeit)
// ===========================================================================
#define NTP_ENABLED     true
#define NTP_SERVER_1    "pool.ntp.org"
#define NTP_SERVER_2    "time.nist.gov"
// POSIX-TZ-String. Mitteleuropa mit Sommerzeit:
#define NTP_TZ          "CET-1CEST,M3.5.0,M10.5.0/3"

// ===========================================================================
//  9) HARDWARE-WATCHDOG  (Reset, falls die Firmware hängt)
// ===========================================================================
// Mindestens 60 s: Ein einzelner HTTPS-Versuch (DNS + Verbindung + TLS-
// Handshake + Antwort) kann im ungünstigsten Fall knapp 50 s dauern. Kleinere
// Werte (früher 30) hebt die Firmware automatisch auf dieses Minimum an.
// Nach einem Watchdog-Neustart setzt die Box eine offene Alarm-Zustellung
// fort (Zustand im RTC-Speicher).
#define WDT_ENABLED     true
#define WDT_TIMEOUT_S   60          // Sekunden ohne "Futter" -> Reboot
