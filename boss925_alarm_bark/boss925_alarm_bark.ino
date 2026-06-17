/*
  BOSS-925 Alarm-Wächter -> Bark (iOS Critical Alerts)
  ----------------------------------------------------------------------------
  Liest den potenzialfreien Relaiskontakt eines Swissphone-Ladegeräts
  (z.B. LGRA-Expert, DIN-Buchse Pin 1 + 3). Der Kontakt schließt, wenn der
  eingesteckte BOSS 925 eine Alarmierung empfängt. Der ESP32 erkennt das und
  sendet über Bark einen Critical Alert an alle Empfänger (umgeht Stumm-
  schaltung und "Nicht stören" auf iOS).

    - ALARM     -> Critical Alert an alle Keys in BARK_KEYS_ALARM.
    - START + täglicher HEARTBEAT -> leise nur an dich (BARK_KEY_STATUS).

  Diese Lösung ist eine ZUSATZ-Alarmierung, KEIN Ersatz für den Melder.

  Alle einstellbaren Werte stehen in config.h (Kopie von config.example.h).
  Externe Libraries: keine (WiFi/HTTPClient/WiFiClientSecure/Zeit/Watchdog sind
  alle im ESP32-Arduino-Core enthalten).
  ----------------------------------------------------------------------------
*/

#include <WiFi.h>
#include <HTTPClient.h>
#include <WiFiClientSecure.h>
#include <time.h>
#include <esp_task_wdt.h>

#include "config.h"   // <-- DEINE WERTE. Falls dieser Include fehlschlägt:
                      //     config.example.h nach config.h kopieren!

// Anzahl der Alarm-Keys automatisch aus dem Array in config.h berechnen.
static const int BARK_KEYS_ALARM_COUNT =
    sizeof(BARK_KEYS_ALARM) / sizeof(BARK_KEYS_ALARM[0]);

// ============================ INTERNE VARIABLEN =============================

unsigned long activeSince      = 0;   // seit wann ist der Kontakt aktiv?
unsigned long lastAlarm        = 0;   // Zeitpunkt des letzten Alarms (Cooldown)
unsigned long lastHeartbeatMs  = 0;   // für Heartbeat OHNE NTP (millis-Abstand)
int           lastHeartbeatDay = -1;  // für Heartbeat MIT NTP (1x pro Kalendertag)
unsigned long lastWifiTry      = 0;   // letzter (nicht-blockierender) Reconnect

// Vorab-Deklarationen (damit die Reihenfolge im File egal ist).
void feedWatchdog();

// ============================ WLAN ==========================================

// Einmaliger, zeitlich begrenzter Verbindungsversuch (blockierend, max ~15 s).
// Wird beim Start und vor jedem Senden benutzt.
bool connectWiFi(unsigned long timeoutMs = 15000) {
  if (WiFi.status() == WL_CONNECTED) return true;
  WiFi.mode(WIFI_STA);
  WiFi.setAutoReconnect(true);          // Core soll selbst nachverbinden
  WiFi.begin(WIFI_SSID, WIFI_PASSWORD);
  unsigned long start = millis();
  while (WiFi.status() != WL_CONNECTED && millis() - start < timeoutMs) {
    delay(250);
    feedWatchdog();                     // Watchdog auch beim Warten füttern
  }
  return WiFi.status() == WL_CONNECTED;
}

// Nicht-blockierende Pflege im loop(): alle 10 s einen Reconnect anstoßen,
// falls die Verbindung weg ist. Blockiert NICHT die Alarm-Erkennung.
void maintainWiFi() {
  if (WiFi.status() == WL_CONNECTED) return;
  if (millis() - lastWifiTry < 10000) return;
  lastWifiTry = millis();
  Serial.println("WLAN weg -> versuche neu zu verbinden...");
  WiFi.disconnect();
  WiFi.begin(WIFI_SSID, WIFI_PASSWORD);
}

// ============================ WATCHDOG ======================================

// Der Hardware-Watchdog startet den ESP32 neu, falls die Firmware hängt
// (z.B. in einer Endlosschleife). Wir müssen ihn regelmäßig "füttern".
void feedWatchdog() {
#if WDT_ENABLED
  esp_task_wdt_reset();
#endif
}

void setupWatchdog() {
#if WDT_ENABLED
  // Die Watchdog-API hat sich zwischen ESP32-Core 2.x und 3.x geändert.
  #if ESP_ARDUINO_VERSION_MAJOR >= 3
    esp_task_wdt_config_t cfg = {
      .timeout_ms     = WDT_TIMEOUT_S * 1000,
      .idle_core_mask = 0,
      .trigger_panic  = true
    };
    esp_task_wdt_init(&cfg);
  #else
    esp_task_wdt_init(WDT_TIMEOUT_S, true);
  #endif
  esp_task_wdt_add(NULL);   // aktuellen Task (loop) überwachen
  Serial.printf("Watchdog aktiv (%d s).\n", WDT_TIMEOUT_S);
#endif
}

// ============================ ZEIT / NTP ====================================

void setupTime() {
#if NTP_ENABLED
  configTime(0, 0, NTP_SERVER_1, NTP_SERVER_2);  // Roh-Zeit (UTC) holen
  setenv("TZ", NTP_TZ, 1);                        // Zeitzone setzen
  tzset();
  // kurz auf eine plausible Zeit warten (max ~5 s)
  struct tm t;
  unsigned long start = millis();
  while (!getLocalTime(&t, 200) && millis() - start < 5000) {
    feedWatchdog();
  }
  if (getLocalTime(&t)) {
    Serial.printf("Zeit synchronisiert: %02d:%02d:%02d\n",
                  t.tm_hour, t.tm_min, t.tm_sec);
  } else {
    Serial.println("Zeit (NTP) noch nicht verfügbar - läuft im Hintergrund.");
  }
#endif
}

// Liefert "[09.06.2026 14:23] " oder "" falls keine Zeit vorhanden.
String timePrefix() {
#if NTP_ENABLED
  struct tm t;
  if (getLocalTime(&t, 50)) {
    char buf[32];
    snprintf(buf, sizeof(buf), "[%02d.%02d.%04d %02d:%02d] ",
             t.tm_mday, t.tm_mon + 1, t.tm_year + 1900, t.tm_hour, t.tm_min);
    return String(buf);
  }
#endif
  return "";
}

// ============================ BARK-VERSAND ==================================

// Minimales URL-Encoding für Formularfelder (Leerzeichen, Umlaute, ...).
String urlEncode(const String &s) {
  String out;
  const char *hex = "0123456789ABCDEF";
  for (size_t i = 0; i < s.length(); i++) {
    char c = s[i];
    if (isalnum((unsigned char)c) || c == '-' || c == '_' || c == '.' || c == '~') {
      out += c;
    } else {
      out += '%';
      out += hex[(c >> 4) & 0xF];
      out += hex[c & 0xF];
    }
  }
  return out;
}

// Sendet eine Bark-Nachricht an EINEN Device-Key, mit bis zu BARK_MAX_TRIES
// Versuchen. Gibt true zurück, wenn der Server mit HTTP 200 antwortet.
// level: "critical" (laut, umgeht Stumm/DND), "active", "timeSensitive", "passive".
bool sendBark(const char* key, const String& title, const String& body,
              const char* level, const char* sound, int volume, bool repeat) {
  if (key == nullptr || strlen(key) == 0) return false;

  if (!connectWiFi()) {
    Serial.println("Kein WLAN - Nachricht nicht gesendet.");
    return false;
  }

  String form = "title="  + urlEncode(title)
              + "&body="  + urlEncode(body)
              + "&sound=" + urlEncode(sound)
              + "&level=" + urlEncode(level)
              + "&volume=" + String(volume);
  if (repeat) form += "&call=1";

  String url = String(BARK_HOST) + "/" + key;

  for (int attempt = 1; attempt <= BARK_MAX_TRIES; attempt++) {
    WiFiClientSecure client;
    client.setInsecure();   // ohne Zertifikatsprüfung (einfach & robust)
    HTTPClient http;
    http.setConnectTimeout(8000);
    http.setTimeout(8000);
    http.begin(client, url);
    http.addHeader("Content-Type", "application/x-www-form-urlencoded");

    int code = http.POST(form);
    http.end();
    feedWatchdog();

    Serial.printf("Bark POST (%s) Versuch %d/%d -> HTTP %d\n",
                  key, attempt, BARK_MAX_TRIES, code);

    if (code == 200) return true;             // Erfolg
    if (attempt < BARK_MAX_TRIES) delay(500); // kurz warten, dann erneut
  }
  Serial.printf("Bark an %s endgültig fehlgeschlagen.\n", key);
  return false;
}

// Sendet den Alarm als Critical Alert an ALLE Empfänger. Jeder Key wird
// einzeln behandelt - ein fehlerhafter Key blockiert die anderen NICHT.
void sendAlarmToAll() {
  String body = timePrefix() + ALARM_BODY;
  int ok = 0, fail = 0, skipped = 0;
  for (int i = 0; i < BARK_KEYS_ALARM_COUNT; i++) {
    if (BARK_KEYS_ALARM[i] == nullptr || strlen(BARK_KEYS_ALARM[i]) == 0) {
      skipped++;
      continue;
    }
    bool good = sendBark(BARK_KEYS_ALARM[i], ALARM_TITLE, body,
                         "critical", ALARM_SOUND, ALARM_VOLUME, ALARM_CALL);
    if (good) ok++; else fail++;
    delay(150);   // kurze Pause zwischen den Empfängern
  }
  Serial.printf("Alarm gesendet: %d ok, %d fehlgeschlagen, %d leer.\n",
                ok, fail, skipped);
}

// Leiser Status-/Heartbeat-Ping nur an dich.
// WICHTIG: title/body landen in der Bark-Notification -> NUR ASCII (ae/ue/oe),
// da Bark dort keine Umlaute korrekt darstellt. Gilt auch fuer ALARM_TITLE/_BODY.
void sendStatus(const String& title, const String& body) {
  sendBark(BARK_KEY_STATUS, title, timePrefix() + body,
           "passive", STATUS_SOUND, STATUS_VOLUME, false);
}

// ============================ EINGANG (RELAIS) ==============================

// true, wenn (mindestens) ein Eingang aktiv ist (= Alarm-Kontakt geschlossen).
bool relayActive() {
  int v1 = digitalRead(RELAY_PIN);
  bool a1 = ACTIVE_LOW ? (v1 == LOW) : (v1 == HIGH);
#if RELAY_PIN2_ENABLED
  int v2 = digitalRead(RELAY_PIN2);
  bool a2 = ACTIVE_LOW ? (v2 == LOW) : (v2 == HIGH);
  return a1 || a2;
#else
  return a1;
#endif
}

// ============================ SELBSTTEST ====================================

// Kleiner Check beim Start: Pins, Key-Anzahl, WLAN, eine Status-Meldung.
void selfTest() {
  Serial.println("\n=== SELBSTTEST ===");
  Serial.printf("Eingang Pin %d, ACTIVE_LOW=%s, aktueller Stand: %s\n",
                RELAY_PIN, ACTIVE_LOW ? "ja" : "nein",
                relayActive() ? "AKTIV (Kontakt zu)" : "offen (Ruhe)");

  int valid = 0;
  for (int i = 0; i < BARK_KEYS_ALARM_COUNT; i++) {
    if (BARK_KEYS_ALARM[i] && strlen(BARK_KEYS_ALARM[i]) > 0) valid++;
  }
  Serial.printf("Alarm-Empfänger (gültige Keys): %d\n", valid);
  if (valid == 0) {
    Serial.println("WARNUNG: KEIN gültiger Alarm-Key! Bitte config.h prüfen.");
  }

  Serial.printf("WLAN: %s\n",
                WiFi.status() == WL_CONNECTED ? "verbunden" : "NICHT verbunden");
  if (WiFi.status() == WL_CONNECTED) {
    Serial.print("IP-Adresse: ");
    Serial.println(WiFi.localIP());
  }
  Serial.println("=== SELBSTTEST ENDE ===\n");
}

// ============================ HEARTBEAT-LOGIK ===============================

void handleHeartbeat() {
#if HEARTBEAT_ENABLED
  #if NTP_ENABLED
    // Mit NTP: genau 1x pro Kalendertag, ab HEARTBEAT_HOUR Uhr.
    struct tm t;
    if (getLocalTime(&t, 50)) {
      if (t.tm_hour >= HEARTBEAT_HOUR && t.tm_yday != lastHeartbeatDay) {
        sendStatus("Alarm-Waechter aktiv", "Tages-Check: System laeuft.");
        lastHeartbeatDay = t.tm_yday;
      }
      return;
    }
    // Falls Zeit (noch) fehlt: auf millis-Abstand zurückfallen.
  #endif
  // Ohne NTP: fester Abstand seit Start/letztem Heartbeat.
  if (millis() - lastHeartbeatMs >= HEARTBEAT_MS) {
    sendStatus("Alarm-Waechter aktiv", "Tages-Check: System laeuft.");  // Bark-Text: ASCII!
    lastHeartbeatMs = millis();
  }
#endif
}

// ============================ PROBEALARM-FENSTER ===========================

// true, wenn JETZT das wöchentliche ILS-Probealarm-Fenster ist (z.B. Mi 18:55-19:10).
// Ohne gültige NTP-Zeit -> false (sicherheitshalber lieber normal alarmieren).
bool inTestWindow() {
#if (TESTALARM_SUPPRESS && NTP_ENABLED)
  struct tm t;
  if (!getLocalTime(&t, 50)) return false;        // keine Zeit -> NICHT unterdrücken
  if (t.tm_wday != TESTALARM_WDAY) return false;  // falscher Wochentag
  int minuteOfDay = t.tm_hour * 60 + t.tm_min;
  return (minuteOfDay >= TESTALARM_START_MIN && minuteOfDay <= TESTALARM_END_MIN);
#else
  return false;
#endif
}

// ============================ SETUP / LOOP =================================

void setup() {
  Serial.begin(115200);
  delay(300);
  Serial.println("\nBOSS-925 Alarm-Wächter startet...");

  // Eingänge konfigurieren (interner Pull-up -> Kontakt zieht auf GND/LOW).
  pinMode(RELAY_PIN, INPUT_PULLUP);
#if RELAY_PIN2_ENABLED
  pinMode(RELAY_PIN2, INPUT_PULLUP);
#endif

  setupWatchdog();
  connectWiFi();
  setupTime();
  selfTest();

  lastHeartbeatMs = millis();
  // Start-Meldung leise nur an dich (ersetzt nicht den täglichen Heartbeat).
  sendStatus("Alarm-Waechter online",
             "System gestartet und ueberwacht den Relaiskontakt.");
}

void loop() {
  feedWatchdog();
  maintainWiFi();
  handleHeartbeat();

  unsigned long now = millis();

  // Cooldown nach einem Alarm: Eingang ignorieren (aber Heartbeat/WLAN laufen).
  if (lastAlarm != 0 && now - lastAlarm < COOLDOWN_MS) {
    delay(20);
    return;
  }

  if (relayActive()) {
    if (activeSince == 0) activeSince = now;          // Beginn merken
    if (now - activeSince >= DEBOUNCE_MS) {           // lange genug aktiv?
      if (inTestWindow()) {
        // Wöchentlicher ILS-Probealarm: KEIN lauter Alarm an alle, nur leiser
        // Funktionsnachweis an dich. Bestätigt die ganze Kette Melder->Bark.
        Serial.println(">>> PROBEALARM-Fenster - nur leiser Status an dich <<<");
        sendStatus("Probealarm erkannt",
                   "Woechentlicher ILS-Test - Kette Melder->Bark funktioniert.");
      } else {
        Serial.println(">>> ALARM erkannt - sende Bark Critical Alert <<<");
        sendAlarmToAll();
      }
      lastAlarm = now;
      activeSince = 0;
    }
  } else {
    activeSince = 0;                                  // Kontakt offen -> Reset
  }

  delay(20);
}
