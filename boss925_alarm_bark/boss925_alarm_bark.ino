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

  Robustheit:
    - Flankenerkennung: Nach einem Alarm muss der Kontakt erst wieder ÖFFNEN
      (REARM_OPEN_MS), bevor ein neuer Alarm möglich ist. Ein dauerhaft
      geschlossener Kontakt löst also NICHT alle paar Minuten erneut aus.
    - Interrupt-Latch: Auch kurze Alarm-Impulse, die genau während einer
      blockierenden Sendung (Heartbeat/HTTPS) kommen, werden per Pin-Interrupt
      "eingerastet" und danach ausgelöst.
    - Nachsende-Puffer: Ist WLAN/Internet im Alarm-Moment ausgefallen, wird der
      Alarm gemerkt und alle ALARM_RETRY_MS erneut versucht (pro Empfänger),
      bis alle versorgt sind oder ALARM_RETRY_GIVEUP_MS erreicht ist.
    - Klemm-Warnung: Bleibt der Kontakt ungewöhnlich lange geschlossen,
      bekommst du einen Status-Hinweis (solange sind keine neuen Alarme
      erkennbar).

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

// --- Alarm-Erkennung -------------------------------------------------------
unsigned long activeSince        = 0;     // seit wann ist der Kontakt aktiv?
unsigned long lastAlarm          = 0;     // Zeitpunkt des letzten Alarms (Cooldown)
bool          waitingForRelease  = false; // nach Alarm: erst wieder scharf, wenn Kontakt offen war
unsigned long openSince          = 0;     // seit wann ist der Kontakt (wieder) offen?
bool          stuckWarned        = false; // Klemm-Warnung schon verschickt?

// --- Nachsende-Puffer (Alarm überlebt WLAN-Ausfall) -------------------------
bool          alarmPending   = false; // es gibt noch nicht zugestellte Empfänger
String        pendingBody;            // Alarm-Text inkl. Zeitstempel der ERKENNUNG
unsigned long pendingSince   = 0;     // wann wurde der Alarm erkannt?
unsigned long lastSendRound  = 0;     // wann lief die letzte Sende-Runde?
bool          firstRoundDone = false; // erste Runde läuft sofort, ohne Wartezeit
bool          keyDone[BARK_KEYS_ALARM_COUNT > 0 ? BARK_KEYS_ALARM_COUNT : 1];

// --- Vom Pin-Interrupt gesetzter Alarm-Merker --------------------------------
// Der Interrupt misst die Impulsdauer selbst. War der Kontakt mindestens
// DEBOUNCE_MS geschlossen, wird der Alarm "eingerastet" - auch wenn loop()
// gerade in einer blockierenden HTTPS-Sendung steckt.
volatile bool          isrLatched = false;
volatile unsigned long isrSince1  = 0;
#if RELAY_PIN2_ENABLED
volatile unsigned long isrSince2  = 0;
#endif

// --- Heartbeat ---------------------------------------------------------------
unsigned long lastHeartbeatMs  = 0;   // für Heartbeat OHNE NTP (millis-Abstand)
int           lastHeartbeatDay = -1;  // für Heartbeat MIT NTP (1x pro Kalendertag)
unsigned long lastHeartbeatTry = 0;   // letzter VERSUCH (für Wiederholung nach Fehler)

// --- WLAN --------------------------------------------------------------------
unsigned long lastWifiTry = 0;        // letzter (nicht-blockierender) Reconnect

// Vorab-Deklarationen (damit die Reihenfolge im File egal ist).
void feedWatchdog();
void processPendingAlarm();

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
    // Beim Core 3.x läuft der Task-Watchdog ab Werk bereits (nur ~5 s, ohne
    // Neustart). Ein zweites init() schlägt dann still fehl - in dem Fall die
    // laufende Instanz auf unsere Werte umkonfigurieren.
    esp_err_t err = esp_task_wdt_init(&cfg);
    if (err == ESP_ERR_INVALID_STATE) {
      err = esp_task_wdt_reconfigure(&cfg);
    }
    if (err != ESP_OK) {
      Serial.println("WARNUNG: Watchdog liess sich nicht konfigurieren!");
      return;
    }
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
  if (getLocalTime(&t, 50)) {
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
// Bei HTTP 4xx (z.B. falscher Key) wird NICHT wiederholt - das kann eine
// Wiederholung nicht beheben.
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
    if (code >= 400 && code < 500) {          // Client-Fehler: Key/Anfrage falsch
      Serial.printf("HTTP %d - Wiederholen zwecklos, Key in config.h pruefen!\n", code);
      return false;
    }
    if (attempt < BARK_MAX_TRIES) delay(500); // kurz warten, dann erneut
  }
  Serial.printf("Bark an %s endgültig fehlgeschlagen.\n", key);
  return false;
}

// Leiser Status-/Heartbeat-Ping nur an dich. Gibt true bei Erfolg zurück.
// WICHTIG: title/body landen in der Bark-Notification -> NUR ASCII (ae/ue/oe),
// da Bark dort keine Umlaute korrekt darstellt. Gilt auch fuer ALARM_TITLE/_BODY.
bool sendStatus(const String& title, const String& body) {
  return sendBark(BARK_KEY_STATUS, title, timePrefix() + body,
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

// Pin-Interrupt: läuft bei JEDER Flanke (schließen UND öffnen). Misst, wie
// lange der Kontakt geschlossen war. War es mindestens DEBOUNCE_MS, wird der
// Alarm eingerastet (isrLatched). So geht ein kurzer Impuls nicht verloren,
// selbst wenn loop() gerade blockiert (z.B. HTTPS-Sendung des Heartbeats).
// Kurze Störspitzen (< DEBOUNCE_MS) rasten dagegen NICHT ein.
void IRAM_ATTR relayEdge(int pin, volatile unsigned long* since) {
  int v = digitalRead(pin);
  bool active = ACTIVE_LOW ? (v == LOW) : (v == HIGH);
  unsigned long now = millis();
  if (active) {
    if (*since == 0) *since = now;              // Beginn der Schließung merken
  } else {
    if (*since != 0 && now - *since >= DEBOUNCE_MS) {
      isrLatched = true;                        // lang genug zu -> einrasten
    }
    *since = 0;
  }
}

void IRAM_ATTR relayIsr1() { relayEdge(RELAY_PIN, &isrSince1); }
#if RELAY_PIN2_ENABLED
void IRAM_ATTR relayIsr2() { relayEdge(RELAY_PIN2, &isrSince2); }
#endif

// ============================ ALARM-VERSAND =================================

// Sendet den offenen Alarm an alle Empfänger, die ihn noch NICHT bekommen
// haben. Jeder Key wird einzeln behandelt - ein fehlerhafter Key blockiert die
// anderen NICHT. Wird aus loop() wiederholt aufgerufen, bis alle versorgt sind:
// War WLAN/Internet im Alarm-Moment weg, geht der Alarm NICHT verloren, sondern
// wird alle ALARM_RETRY_MS nachgesendet (max. bis ALARM_RETRY_GIVEUP_MS).
void processPendingAlarm() {
  if (!alarmPending) return;

  if (firstRoundDone) {
    // Zwischen zwei Nachsende-Runden warten (nicht blockierend).
    if (millis() - lastSendRound < ALARM_RETRY_MS) return;
    // Irgendwann aufgeben - eine Stunden alte Alarm-Meldung hilft niemandem.
    if (millis() - pendingSince >= ALARM_RETRY_GIVEUP_MS) {
      alarmPending = false;
      Serial.println("Alarm-Nachsenden AUFGEGEBEN (Zeitlimit erreicht).");
      sendStatus("Alarm-Zustellung unvollstaendig",
                 "Ein Alarm konnte nicht an alle Empfaenger zugestellt werden!");
      return;
    }
  }

  lastSendRound = millis();
  bool isRetry = firstRoundDone;
  firstRoundDone = true;

  if (!connectWiFi()) {
    Serial.println("Kein WLAN - Alarm wird nachgesendet, sobald Verbindung da ist.");
    return;
  }

  // Zeitstempel im Text ist der der ERKENNUNG - bei Nachsendung zusätzlich
  // kennzeichnen, damit der Empfänger die Verzögerung erkennt.
  String body = pendingBody;
  if (isRetry) body += " (verspaetet zugestellt)";

  int ok = 0, fail = 0;
  for (int i = 0; i < BARK_KEYS_ALARM_COUNT; i++) {
    if (keyDone[i]) continue;   // hat den Alarm schon bekommen (oder Key leer)
    bool good = sendBark(BARK_KEYS_ALARM[i], ALARM_TITLE, body,
                         "critical", ALARM_SOUND, ALARM_VOLUME, ALARM_CALL);
    if (good) { keyDone[i] = true; ok++; }
    else      { fail++; }
    delay(150);   // kurze Pause zwischen den Empfängern
  }
  Serial.printf("Alarm-Runde: %d neu zugestellt, %d noch offen.\n", ok, fail);

  if (fail == 0) {
    alarmPending = false;
    Serial.println("Alarm an alle Empfaenger zugestellt.");
  }
}

// Neuer Alarm erkannt: Zustand setzen und sofort die erste Sende-Runde starten.
void startAlarm() {
  Serial.println(">>> ALARM erkannt - sende Bark Critical Alert an alle <<<");

  alarmPending   = true;
  pendingBody    = timePrefix() + ALARM_BODY;   // Zeitpunkt der Erkennung
  pendingSince   = millis();
  firstRoundDone = false;
  for (int i = 0; i < BARK_KEYS_ALARM_COUNT; i++) {
    // Leere Einträge gelten sofort als erledigt (werden nie angeschrieben).
    keyDone[i] = (BARK_KEYS_ALARM[i] == nullptr || strlen(BARK_KEYS_ALARM[i]) == 0);
  }

  lastAlarm         = millis();
  waitingForRelease = true;    // neuer Alarm erst, wenn Kontakt wieder offen war
  openSince         = 0;
  stuckWarned       = false;
  activeSince       = 0;

  processPendingAlarm();       // erste Runde sofort senden
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
  // Ein noch offener Alarm hat IMMER Vorrang vor dem Heartbeat.
  if (alarmPending) return;
  // Nach einem Fehlversuch erst nach HEARTBEAT_RETRY_MS erneut versuchen
  // (sonst würde jede Schleifenrunde eine blockierende Sendung starten).
  if (lastHeartbeatTry != 0 && millis() - lastHeartbeatTry < HEARTBEAT_RETRY_MS) return;

  #if NTP_ENABLED
    // Mit NTP: genau 1x pro Kalendertag, ab HEARTBEAT_HOUR Uhr.
    struct tm t;
    if (getLocalTime(&t, 50)) {
      if (t.tm_hour >= HEARTBEAT_HOUR && t.tm_yday != lastHeartbeatDay) {
        lastHeartbeatTry = millis();
        if (sendStatus("Alarm-Waechter aktiv", "Tages-Check: System laeuft.")) {
          lastHeartbeatDay = t.tm_yday;   // erst nach ERFOLG als erledigt markieren
        } else {
          Serial.println("Heartbeat fehlgeschlagen - naechster Versuch spaeter.");
        }
      }
      return;
    }
    // Falls Zeit (noch) fehlt: auf millis-Abstand zurückfallen.
  #endif
  // Ohne NTP: fester Abstand seit Start/letztem erfolgreichen Heartbeat.
  if (millis() - lastHeartbeatMs >= HEARTBEAT_MS) {
    lastHeartbeatTry = millis();
    if (sendStatus("Alarm-Waechter aktiv", "Tages-Check: System laeuft.")) {  // Bark-Text: ASCII!
      lastHeartbeatMs = millis();         // erst nach ERFOLG als erledigt markieren
    } else {
      Serial.println("Heartbeat fehlgeschlagen - naechster Versuch spaeter.");
    }
  }
#endif
}

// ============================ SETUP / LOOP =================================

void setup() {
  Serial.begin(115200);
  delay(300);
  Serial.println("\nBOSS-925 Alarm-Wächter startet...");

  // Eingänge konfigurieren (interner Pull-up -> Kontakt zieht auf GND/LOW).
  // Zusätzlich Pin-Interrupts: fangen auch Impulse, während loop() blockiert.
  // Ist der Kontakt SCHON beim Start geschlossen, Messung sofort beginnen -
  // so rastet auch ein Impuls ein, der noch während setup() endet.
  pinMode(RELAY_PIN, INPUT_PULLUP);
  if (digitalRead(RELAY_PIN) == (ACTIVE_LOW ? LOW : HIGH)) isrSince1 = millis();
  attachInterrupt(digitalPinToInterrupt(RELAY_PIN), relayIsr1, CHANGE);
#if RELAY_PIN2_ENABLED
  pinMode(RELAY_PIN2, INPUT_PULLUP);
  if (digitalRead(RELAY_PIN2) == (ACTIVE_LOW ? LOW : HIGH)) isrSince2 = millis();
  attachInterrupt(digitalPinToInterrupt(RELAY_PIN2), relayIsr2, CHANGE);
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
  processPendingAlarm();   // offene Alarm-Zustellungen zuerst nachholen
  handleHeartbeat();

  unsigned long now = millis();
  bool active = relayActive();

  // Nach einem Alarm erst wieder scharf schalten, wenn der Kontakt mindestens
  // REARM_OPEN_MS stabil OFFEN war. Ein dauerhaft geschlossener Kontakt (z.B.
  // Melder alarmiert weiter, niemand quittiert) feuert so NICHT alle paar
  // Minuten neue Critical Alerts an alle.
  if (waitingForRelease) {
    if (!active) {
      if (openSince == 0) openSince = now;
      if (now - openSince >= REARM_OPEN_MS) {
        waitingForRelease = false;
        openSince = 0;
        Serial.println("Kontakt wieder offen - Alarm-Erkennung wieder scharf.");
      }
    } else {
      openSince = 0;
      // Kontakt klemmt? Solange er zu ist, sind KEINE neuen Alarme erkennbar.
      // Einmalig leise warnen, damit du am Lader nachschauen kannst.
      if (!stuckWarned && now - lastAlarm >= STUCK_CONTACT_WARN_MS) {
        stuckWarned = true;
        Serial.println("WARNUNG: Kontakt dauerhaft geschlossen (klemmt?).");
        sendStatus("Kontakt klemmt?",
                   "Relaiskontakt ist seit dem Alarm dauerhaft geschlossen. "
                   "Melder/Lader pruefen - bis dahin sind keine neuen Alarme erkennbar!");
      }
    }
  }

  // Vom Interrupt eingerasteten Impuls abholen (Merker dabei zurücksetzen).
  bool latched = isrLatched;
  if (latched) isrLatched = false;

  bool inCooldown = (lastAlarm != 0 && now - lastAlarm < COOLDOWN_MS);

  if (waitingForRelease || inCooldown) {
    // Empfänger wurden gerade erst alarmiert: neue Auslösungen (auch ein
    // eingerasteter Impuls) werden in dieser Phase bewusst verworfen.
    activeSince = 0;
    delay(20);
    return;
  }

  bool trigger = latched;   // Impuls, der während einer Blockade kam
  if (active) {
    if (activeSince == 0) activeSince = now;          // Beginn merken
    if (now - activeSince >= DEBOUNCE_MS) trigger = true;  // lange genug aktiv?
  } else {
    activeSince = 0;                                  // Kontakt offen -> Reset
  }

  if (trigger) startAlarm();

  delay(20);
}
