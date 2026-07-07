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
#define FW_BUILD_MARKER              "boss925-monitor-2026-07-06"

// Status alle 60 s: schnell genug für "lebt noch", aber sparsam für NAS/ESP32.
// Ereignisse (Alarm, WLAN-Reconnect, Heartbeat-Ergebnis) werden zusätzlich
// zeitnah geschickt, aber per MIN_EVENT_GAP entprellt.
#define REMOTE_STATUS_INTERVAL_MS     (60UL*1000UL)
#define REMOTE_STATUS_MIN_EVENT_GAP_MS 5000UL

// Poll alle 10 s: Jeder Poll ist ein voller TLS-Handshake, der loop() 1-2 s
// blockiert (Relais-Impulse fängt derweil das ISR-Latch). Über den Poll läuft
// nur noch der TEST-Befehl - der manuelle REAL ALARM geht direkt vom NAS an
// Bark. Fehler bekommen Backoff, damit ein unerreichbares NAS nicht bremst.
#define REMOTE_POLL_INTERVAL_MS       (10UL*1000UL)
#define REMOTE_ERROR_BACKOFF_MS       (30UL*1000UL)
// Nicht zu knapp: TLS-Handshake ESP32<->Synology dauert typischerweise 1-3 s.
// Bei 1,5 s würde fast jeder Request abbrechen und im Backoff landen.
#define REMOTE_HTTP_TIMEOUT_MS        4000UL

// ===========================================================================
//  5) RELAIS-EINGANG  (Pin & Polarität)
// ===========================================================================
// Pin MUSS einen internen Pull-up können: NICHT GPIO34-39 verwenden!
// Gut: GPIO27, 25, 26, 32, 33. (GPIO27 ist kein Strapping-Pin -> ideal.)
#define RELAY_PIN       27
#define ACTIVE_LOW      true        // Kontakt gegen GND -> LOW = Alarm

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
// einmalig eine leise Warnung an BARK_KEY_STATUS ("Kontakt klemmt?").
#define STUCK_CONTACT_WARN_MS  (30UL*60UL*1000UL)   // 30 min

// Nachsenden: War WLAN/Internet im Alarm-Moment weg, wird der Alarm gemerkt
// und regelmäßig erneut versucht (pro Empfänger, bis alle versorgt sind).
#define ALARM_RETRY_MS         (60UL*1000UL)        // Abstand zwischen Nachsende-Runden
#define ALARM_RETRY_GIVEUP_MS  (15UL*60UL*1000UL)   // danach aufgeben (Meldung zu alt)

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
#define WDT_ENABLED     true
#define WDT_TIMEOUT_S   30          // Sekunden ohne "Futter" -> Reboot
