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

// Device-Keys ALLER Alarm-Empfänger (bis zu 10). Jede Person sieht ihren Key
// in der Bark-App. Leere Einträge ("") werden übersprungen.
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
//  4) RELAIS-EINGANG  (Pin & Polarität)
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
//  5) TIMINGS  (Logik)
// ===========================================================================
#define DEBOUNCE_MS     300UL                   // Kontakt muss so lange aktiv sein
#define COOLDOWN_MS     (5UL*60UL*1000UL)       // 5 min Sperre nach einem Alarm
#define BARK_MAX_TRIES  3                        // 1. Versuch + 2 Wiederholungen

// ===========================================================================
//  6) HEARTBEAT  ("ich lebe noch"-Ping nur an dich)
// ===========================================================================
#define HEARTBEAT_ENABLED   true
// Wenn NTP aktiv: täglich zu dieser Stunde (0-23, lokale Zeit).
#define HEARTBEAT_HOUR      8
// Fallback ohne NTP: fester Abstand (alle 24 h ab Start).
#define HEARTBEAT_MS        (24UL*60UL*60UL*1000UL)

// ===========================================================================
//  7) NTP-ZEIT  (Zeitstempel in Nachrichten, feste Heartbeat-Uhrzeit)
// ===========================================================================
#define NTP_ENABLED     true
#define NTP_SERVER_1    "pool.ntp.org"
#define NTP_SERVER_2    "time.nist.gov"
// POSIX-TZ-String. Mitteleuropa mit Sommerzeit:
#define NTP_TZ          "CET-1CEST,M3.5.0,M10.5.0/3"

// ===========================================================================
//  8) HARDWARE-WATCHDOG  (Reset, falls die Firmware hängt)
// ===========================================================================
#define WDT_ENABLED     true
#define WDT_TIMEOUT_S   30          // Sekunden ohne "Futter" -> Reboot
