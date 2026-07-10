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
#include <esp_timer.h>   // 64-Bit-Uptime (millis() liefe nach 49,7 Tagen über)
#include <esp_system.h>  // esp_reset_reason() für die Neustart-Diagnose im Dashboard
#include <Preferences.h> // NVS-Speicher für die vom Dashboard verwaltete Empfängerliste

#include "config.h"   // <-- DEINE WERTE. Falls dieser Include fehlschlägt:
                      //     config.example.h nach config.h kopieren!

// Anzahl der Alarm-Keys automatisch aus dem Array in config.h berechnen.
static const int BARK_KEYS_ALARM_COUNT =
    sizeof(BARK_KEYS_ALARM) / sizeof(BARK_KEYS_ALARM[0]);

// Maximal verwaltbare Alarm-Empfänger (Dashboard-Liste + NVS-Speicher).
static const int MAX_ALARM_KEYS = 10;

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
int           pendingVolume  = ALARM_VOLUME;  // Lautstärke, bei der ERKENNUNG festgelegt
unsigned long lastSendRound  = 0;     // wann lief die letzte Sende-Runde?
bool          firstRoundDone = false; // erste Runde läuft sofort, ohne Wartezeit
bool          keyDone[MAX_ALARM_KEYS];

// --- Laufzeit-Empfängerliste (vom NAS-Dashboard verwaltet) -------------------
// Wird im NVS-Flash persistiert und überlebt so Reboot und NAS-Ausfall.
// BARK_KEYS_ALARM aus config.h ist nur noch die Startliste für den allerersten
// Boot (Version 0). Sobald das Dashboard eine Liste pflegt (Version >= 1),
// ist die Dashboard-Liste führend und ersetzt die config.h-Liste komplett.
String        alarmKeys[MAX_ALARM_KEYS];
// Arbeitsmodus pro Empfänger (kommt vom Dashboard/api/keys.php mit): stumm
// geschaltete Keys bekommen den Critical Alert mit ihrer eingestellten
// Stumm-Lautstärke (0 = lautlos) - weiterhin sichtbar; sie bleiben ganz
// normal in der Zustell-Liste.
bool          alarmKeyMuted[MAX_ALARM_KEYS];
// Stumm-Lautstärke pro Empfänger (0-10, wird im Dashboard eingestellt):
// gilt NUR, solange der Key stumm ist. 0 = komplett lautlos (Standard).
uint8_t       alarmKeyMuteVolume[MAX_ALARM_KEYS];
int           alarmKeyCount    = 0;
unsigned long alarmKeysVersion = 0;
Preferences   keyStore;

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
String        lastHeartbeatInfo = "nie";
bool          lastHeartbeatOk   = false;

// --- WLAN --------------------------------------------------------------------
unsigned long lastWifiTry = 0;        // letzter (nicht-blockierender) Reconnect
bool          lastWifiConnected = false;

// --- NAS-Fernüberwachung -----------------------------------------------------
unsigned long lastRemoteStatusPush    = 0;
unsigned long lastRemoteStatusAttempt = 0;
unsigned long lastRemotePoll          = 0;
unsigned long lastRemoteError         = 0;
unsigned long lastRemoteAckTry        = 0;
bool          remoteStatusDirty       = true;
unsigned long lastRemoteCommandId     = 0;
bool          remoteAckPending        = false;
bool          nasAuthWarned           = false;  // 403-Warnung schon verschickt?
unsigned long remoteAckId             = 0;
String        remoteAckResult;
String        remoteAckMessage;
String        lastAlarmInfo           = "nie";

// Vorab-Deklarationen (damit die Reihenfolge im File egal ist).
void feedWatchdog();
void processPendingAlarm();
void requestRemoteStatusPush();
void handleRemoteMonitor();
bool alarmBlockedNow();

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
  if (WiFi.status() == WL_CONNECTED) {
    if (!lastWifiConnected) {
      lastWifiConnected = true;
      Serial.println("WLAN wieder verbunden.");
      requestRemoteStatusPush();
    }
    return;
  }
  lastWifiConnected = false;
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

// ============================ EMPFÄNGERLISTE ================================
// Die Alarm-Empfänger werden im NAS-Dashboard gepflegt und hier per Poll
// synchronisiert (siehe syncAlarmKeys() weiter unten). Persistiert im NVS,
// damit die Liste Reboot und NAS-Ausfall übersteht.

// Plausibilitätsprüfung: Bark-Keys sind kurze alphanumerische Tokens.
bool validAlarmKey(const String& k) {
  if (k.length() < 5 || k.length() > 64) return false;
  for (size_t i = 0; i < k.length(); i++) {
    char c = k[i];
    if (!isalnum((unsigned char)c) && c != '_' && c != '-') return false;
  }
  return true;
}

// Anzahl der aktuell stummgeschalteten Empfänger (für Log-Ausgaben).
int mutedKeyCount() {
  int muted = 0;
  for (int i = 0; i < alarmKeyCount; i++) {
    if (alarmKeyMuted[i]) muted++;
  }
  return muted;
}

void saveAlarmKeys() {
  keyStore.putInt("n", alarmKeyCount);
  keyStore.putULong("v", alarmKeysVersion);
  for (int i = 0; i < alarmKeyCount; i++) {
    keyStore.putString(("k" + String(i)).c_str(), alarmKeys[i]);
    keyStore.putUChar(("m" + String(i)).c_str(), alarmKeyMuted[i] ? 1 : 0);
    keyStore.putUChar(("mv" + String(i)).c_str(), alarmKeyMuteVolume[i]);
  }
}

void loadAlarmKeys() {
  keyStore.begin("fwalarm", false);
  int n = keyStore.getInt("n", -1);
  if (n >= 1 && n <= MAX_ALARM_KEYS) {
    alarmKeyCount    = n;
    alarmKeysVersion = keyStore.getULong("v", 0);
    for (int i = 0; i < alarmKeyCount; i++) {
      alarmKeys[i] = keyStore.getString(("k" + String(i)).c_str(), "");
      // Fehlt das Stumm-Flag (NVS-Stand einer alten Firmware): laut = sicher.
      alarmKeyMuted[i] = keyStore.getUChar(("m" + String(i)).c_str(), 0) != 0;
      // Fehlende Stumm-Lautstärke (alter NVS-Stand): 0 = lautlos, das alte
      // Verhalten - so ändert das Update nichts an bestehenden Stummschaltungen.
      uint8_t mv = keyStore.getUChar(("mv" + String(i)).c_str(), 0);
      alarmKeyMuteVolume[i] = mv > 10 ? 10 : mv;
    }
    Serial.printf("Empfaengerliste aus NVS geladen: %d Keys (Version %lu, %d stumm).\n",
                  alarmKeyCount, alarmKeysVersion, mutedKeyCount());
    return;
  }
  // Allererster Boot (noch nie synchronisiert): Startliste aus config.h.
  alarmKeyCount = 0;
  for (int i = 0; i < BARK_KEYS_ALARM_COUNT && alarmKeyCount < MAX_ALARM_KEYS; i++) {
    if (BARK_KEYS_ALARM[i] != nullptr && strlen(BARK_KEYS_ALARM[i]) > 0) {
      alarmKeyMuted[alarmKeyCount] = false;   // Startliste: immer laut
      alarmKeyMuteVolume[alarmKeyCount] = 0;
      alarmKeys[alarmKeyCount++] = String(BARK_KEYS_ALARM[i]);
    }
  }
  Serial.printf("Empfaengerliste aus config.h uebernommen: %d Keys.\n", alarmKeyCount);
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

// Liegt JETZT das Zeitfenster des wöchentlichen ILS-Probealarms? Ohne gültige
// NTP-Zeit lautet die Antwort bewusst "nein": ein echter Alarm darf nie wegen
// einer fehlenden Uhrzeit leiser ankommen.
bool inProbeAlarmWindow() {
  struct tm t;
  if (!getLocalTime(&t, 50)) return false;
  if (PROBE_WEEKDAY >= 0 && t.tm_wday != PROBE_WEEKDAY) return false;
  int minutes = t.tm_hour * 60 + t.tm_min;
  return minutes >= PROBE_WINDOW_START_MIN && minutes <= PROBE_WINDOW_END_MIN;
}

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
      requestRemoteStatusPush();
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
  for (int i = 0; i < alarmKeyCount; i++) {
    if (keyDone[i]) continue;   // hat den Alarm schon bekommen
    // Arbeitsmodus: gleicher Critical Alert, aber mit der im Dashboard
    // eingestellten Stumm-Lautstärke (0 = lautlos; durchbricht trotzdem
    // Stummschalter/Fokus) und ohne call=1 (das wuerde den Ton wiederholen).
    bool good = alarmKeyMuted[i]
      ? sendBark(alarmKeys[i].c_str(), ALARM_TITLE, body,
                 "critical", ALARM_SOUND, alarmKeyMuteVolume[i], false)
      : sendBark(alarmKeys[i].c_str(), ALARM_TITLE, body,
                 "critical", ALARM_SOUND, pendingVolume, ALARM_CALL);
    if (good) { keyDone[i] = true; ok++; }
    else      { fail++; }
    delay(150);   // kurze Pause zwischen den Empfängern
  }
  Serial.printf("Alarm-Runde: %d neu zugestellt, %d noch offen.\n", ok, fail);
  requestRemoteStatusPush();

  if (fail == 0) {
    alarmPending = false;
    Serial.println("Alarm an alle Empfaenger zugestellt.");
    requestRemoteStatusPush();
  }
}

// Neuer Alarm erkannt: Zustand setzen und sofort die erste Sende-Runde starten.
void startAlarm() {
  Serial.println(">>> ALARM erkannt - sende Bark Critical Alert an alle <<<");

  alarmPending   = true;
  pendingBody    = timePrefix() + ALARM_BODY;   // Zeitpunkt der Erkennung
  lastAlarmInfo  = pendingBody;
  pendingSince   = millis();
  // Lautstärke einmal bei der ERKENNUNG festlegen, nicht pro Sende-Runde: Ein
  // Alarm um 19:04, der erst um 19:20 nachgesendet wird, bleibt ein Probealarm.
  pendingVolume  = inProbeAlarmWindow() ? PROBE_ALARM_VOLUME : ALARM_VOLUME;
  if (pendingVolume != ALARM_VOLUME) {
    Serial.printf("Probealarm-Fenster: Lautstaerke %d statt %d.\n",
                  pendingVolume, ALARM_VOLUME);
  }
  firstRoundDone = false;
  for (int i = 0; i < alarmKeyCount; i++) {
    keyDone[i] = false;
  }

  lastAlarm         = millis();
  waitingForRelease = true;    // neuer Alarm erst, wenn Kontakt wieder offen war
  openSince         = 0;
  stuckWarned       = false;
  activeSince       = 0;

  processPendingAlarm();       // erste Runde sofort senden
  requestRemoteStatusPush();
}

// ============================ NAS-FERNÜBERWACHUNG ==========================

// Die NAS-Anbindung ist bewusst "best effort": kurze Timeouts, kein eigener
// WLAN-Reconnect und Backoff bei Fehlern. Ein nicht erreichbares NAS darf die
// Bark-Alarmierung und die Relais-Erkennung nicht zur Geisel nehmen.
bool remoteConfigured() {
#if REMOTE_MONITOR_ENABLED
  return strlen(REMOTE_BASE_URL) > 0 && strlen(REMOTE_MACHINE_TOKEN) > 0;
#else
  return false;
#endif
}

bool remoteReady() {
  return remoteConfigured() && WiFi.status() == WL_CONNECTED;
}

bool alarmBlockedNow() {
  unsigned long now = millis();
  bool inCooldown = (lastAlarm != 0 && now - lastAlarm < COOLDOWN_MS);
  return waitingForRelease || inCooldown;
}

String boolField(bool value) {
  return value ? "1" : "0";
}

// Grund des letzten Neustarts als kurzes Token für das Dashboard (dort wird
// es übersetzt und eingefärbt). Watchdog/Panic/Brownout sind Warnzeichen -
// bisher sah man im Dashboard nur "Uptime plötzlich klein" und musste raten.
const char* resetReasonToken() {
  switch (esp_reset_reason()) {
    case ESP_RST_POWERON:   return "poweron";
    case ESP_RST_SW:        return "software";
    case ESP_RST_EXT:       return "external";
    case ESP_RST_PANIC:     return "panic";
    case ESP_RST_TASK_WDT:  return "task_wdt";
    case ESP_RST_INT_WDT:   return "int_wdt";
    case ESP_RST_WDT:       return "wdt";
    case ESP_RST_BROWNOUT:  return "brownout";
    case ESP_RST_DEEPSLEEP: return "deepsleep";
    default:                return "unknown";
  }
}

String remoteEndpoint(const char* fileName) {
  String base = String(REMOTE_BASE_URL);
  if (base.endsWith("/")) base.remove(base.length() - 1);
  return base + "/" + fileName;
}

void requestRemoteStatusPush() {
  remoteStatusDirty = true;
}

String lineValue(const String& payload, const char* key) {
  String prefix = String(key) + "=";
  int pos = 0;
  while (pos < (int)payload.length()) {
    int end = payload.indexOf('\n', pos);
    if (end < 0) end = payload.length();
    String line = payload.substring(pos, end);
    line.trim();
    if (line.startsWith(prefix)) return line.substring(prefix.length());
    pos = end + 1;
  }
  return "";
}

// Einmalige Bark-Warnung, wenn das NAS das Maschinen-Token ablehnt (HTTP 403).
// Ohne diese Meldung fällt ein falscher Token-Hash in der config.php erst auf,
// wenn man zufällig aufs Dashboard schaut (Status bleibt dort still OFFLINE).
// Nach der nächsten erfolgreichen NAS-Antwort wird die Warnung wieder scharf,
// damit ein späteres erneutes Auftreten wieder gemeldet wird.
void warnNasTokenRejected() {
  if (nasAuthWarned) return;
  if (alarmPending) return;  // Alarmzustellung hat Vorrang vor Diagnose-Meldungen
  if (sendStatus("NAS lehnt Token ab",
                 "NAS antwortet mit HTTP 403: Maschinen-Token passt nicht zum "
                 "Hash in config.php auf dem NAS. Dashboard-Status wird nicht "
                 "aktualisiert; Bark-Alarmierung laeuft unabhaengig weiter.")) {
    nasAuthWarned = true;
  }
}

bool remotePostForm(const char* endpoint, const String& form, String* response = nullptr) {
  if (!remoteReady()) return false;

  WiFiClientSecure client;
  client.setInsecure();  // wie Bark: robust gegen CA-/Uhrzeit-Probleme auf ESP32
  HTTPClient http;
  http.setConnectTimeout(REMOTE_HTTP_TIMEOUT_MS);
  http.setTimeout(REMOTE_HTTP_TIMEOUT_MS);
  if (!http.begin(client, remoteEndpoint(endpoint))) {
    return false;
  }
  http.addHeader("Content-Type", "application/x-www-form-urlencoded");

  feedWatchdog();
  int code = http.POST(form);
  if (response != nullptr && code == 200) {
    *response = http.getString();
  }
  http.end();
  feedWatchdog();

  if (code != 200) {
    Serial.printf("NAS %s -> HTTP %d\n", endpoint, code);
  }
  if (code == 200) {
    nasAuthWarned = false;  // NAS akzeptiert wieder -> Warnung neu scharf
  } else if (code == 403) {
    warnNasTokenRejected();
  }
  return code == 200;
}

String remoteBaseForm() {
  return "token=" + urlEncode(REMOTE_MACHINE_TOKEN)
       + "&device_id=" + urlEncode(REMOTE_DEVICE_ID);
}

String keyProgressText() {
  int done = 0;
  for (int i = 0; i < alarmKeyCount; i++) {
    if (keyDone[i]) done++;
  }
  return String(done) + "/" + String(alarmKeyCount);
}

// Holt die komplette Empfängerliste vom NAS (api/keys.php) und übernimmt sie
// nur, wenn sie plausibel ist. Wird aufgerufen, sobald die im Poll gemeldete
// keys_version von der lokalen abweicht. Bewusst NIE während eines offenen
// Alarms - mitten in einer Zustellung wird die Liste nicht ausgetauscht.
void syncAlarmKeys() {
  if (alarmPending) return;

  String nonce = String(millis(), HEX) + "-k" + String(alarmKeysVersion, HEX);
  String form = remoteBaseForm() + "&nonce=" + urlEncode(nonce);
  String response;
  if (!remotePostForm("keys.php", form, &response)) {
    lastRemoteError = millis();
    return;
  }
  if (lineValue(response, "ok") != "1") return;
  if (lineValue(response, "nonce") != nonce) {
    Serial.println("Key-Sync: Antwort mit falscher Nonce verworfen.");
    return;
  }

  unsigned long version = (unsigned long)lineValue(response, "version").toInt();
  int count = lineValue(response, "count").toInt();
  if (count < 1 || count > MAX_ALARM_KEYS) {
    Serial.println("Key-Sync verworfen: unplausible Empfaenger-Anzahl.");
    return;
  }

  String fresh[MAX_ALARM_KEYS];
  bool freshMuted[MAX_ALARM_KEYS];
  uint8_t freshMuteVol[MAX_ALARM_KEYS];
  for (int i = 0; i < count; i++) {
    fresh[i] = lineValue(response, ("key" + String(i)).c_str());
    if (!validAlarmKey(fresh[i])) {
      Serial.println("Key-Sync verworfen: ungueltiger Key in der Antwort.");
      return;
    }
    // Arbeitsmodus-Flag pro Key. Fehlt die Zeile (altes NAS ohne
    // Stumm-Funktion), liefert lineValue "" -> laut = sichere Richtung.
    freshMuted[i] = (lineValue(response, ("muted" + String(i)).c_str()) == "1");
    // Stumm-Lautstärke pro Key (nur wirksam, solange der Key stumm ist).
    // Fehlende Zeile (altes NAS): "" -> toInt()=0 -> lautlos = altes Verhalten.
    int mv = lineValue(response, ("mutevol" + String(i)).c_str()).toInt();
    if (mv < 0)  mv = 0;
    if (mv > 10) mv = 10;
    freshMuteVol[i] = (uint8_t)mv;
  }

  // Nur bei einer Änderung an den EMPFÄNGERN selbst (Anzahl oder Keys) den
  // Owner benachrichtigen. Reine Stumm-Wechsel (Arbeitsmodus) lösen KEINEN
  // Push aus - die kommen bei jedem Kurzbefehl-Aufruf der Kollegen und würden
  // den Owner zuspammen; sichtbar bleiben sie im Dashboard (Badge + Verlauf).
  bool keySetChanged = (count != alarmKeyCount);
  for (int i = 0; !keySetChanged && i < count; i++) {
    if (fresh[i] != alarmKeys[i]) keySetChanged = true;
  }

  for (int i = 0; i < count; i++) {
    alarmKeys[i]           = fresh[i];
    alarmKeyMuted[i]       = freshMuted[i];
    alarmKeyMuteVolume[i]  = freshMuteVol[i];
  }
  alarmKeyCount    = count;
  alarmKeysVersion = version;
  saveAlarmKeys();
  Serial.printf("Empfaengerliste synchronisiert: %d Keys (Version %lu, %d stumm).\n",
                count, version, mutedKeyCount());
  if (keySetChanged) {
    // Leise Bestätigung an den Owner: Änderungen an der Alarmierungs-Liste
    // selbst sollen nicht unbemerkt passieren können.
    sendStatus("Empfaengerliste aktualisiert",
               "Alarm-Empfaenger vom Dashboard uebernommen: "
               + String(count) + " Keys (Version " + String(version)
               + ", " + String(mutedKeyCount()) + " stumm).");
  }
  requestRemoteStatusPush();
}

void pushRemoteStatusIfDue() {
  if (!remoteConfigured()) return;

  unsigned long now = millis();
  bool intervalDue = (lastRemoteStatusPush == 0
                      || now - lastRemoteStatusPush >= REMOTE_STATUS_INTERVAL_MS);
  bool eventDue = (remoteStatusDirty
                   && now - lastRemoteStatusAttempt >= REMOTE_STATUS_MIN_EVENT_GAP_MS);
  if (!intervalDue && !eventDue) return;
  if (!remoteReady()) return;
  if (lastRemoteError != 0 && now - lastRemoteError < REMOTE_ERROR_BACKOFF_MS) return;

  lastRemoteStatusAttempt = now;

  String form = remoteBaseForm()
    + "&fw_build=" + urlEncode(FW_BUILD_MARKER)
    + "&uptime_s=" + String((unsigned long)(esp_timer_get_time() / 1000000ULL))
    + "&wifi=" + urlEncode(WiFi.status() == WL_CONNECTED ? "connected" : "down")
    + "&rssi=" + String(WiFi.RSSI())
    + "&ip=" + urlEncode(WiFi.localIP().toString())
    + "&relay=" + urlEncode(relayActive() ? "closed" : "open")
    + "&isr_latched=" + boolField(isrLatched)
    + "&alarm_pending=" + boolField(alarmPending)
    + "&key_progress=" + urlEncode(keyProgressText())
    + "&keys_count=" + String(alarmKeyCount)
    + "&keys_muted=" + String(mutedKeyCount())
    + "&keys_version=" + String(alarmKeysVersion)
    + "&reset_reason=" + String(resetReasonToken())
    + "&free_heap=" + String((unsigned long)ESP.getFreeHeap())
    + "&last_alarm=" + urlEncode(lastAlarmInfo)
    + "&last_heartbeat=" + urlEncode(lastHeartbeatInfo)
    + "&heartbeat_ok=" + boolField(lastHeartbeatOk)
    + "&cooldown=" + boolField(lastAlarm != 0 && now - lastAlarm < COOLDOWN_MS)
    + "&waiting_for_release=" + boolField(waitingForRelease)
    + "&stuck_warned=" + boolField(stuckWarned)
    + "&ack_pending=" + boolField(remoteAckPending);

  if (remotePostForm("status.php", form)) {
    lastRemoteStatusPush = now;
    remoteStatusDirty = false;
    lastRemoteError = 0;
  } else {
    lastRemoteError = now;
  }
}

void queueRemoteAck(unsigned long id, const String& result, const String& message) {
  remoteAckPending = true;
  remoteAckId = id;
  remoteAckResult = result;
  remoteAckMessage = message;
  lastRemoteAckTry = 0;
  requestRemoteStatusPush();
}

void processRemoteAck() {
  if (!remoteAckPending || !remoteReady()) return;
  unsigned long now = millis();
  if (lastRemoteAckTry != 0 && now - lastRemoteAckTry < REMOTE_POLL_INTERVAL_MS) return;
  if (lastRemoteError != 0 && now - lastRemoteError < REMOTE_ERROR_BACKOFF_MS) return;

  lastRemoteAckTry = now;
  String form = remoteBaseForm()
    + "&id=" + String(remoteAckId)
    + "&result=" + urlEncode(remoteAckResult)
    + "&message=" + urlEncode(remoteAckMessage);
  if (remotePostForm("ack.php", form)) {
    remoteAckPending = false;
    lastRemoteError = 0;
    requestRemoteStatusPush();
  } else {
    lastRemoteError = now;
  }
}

void executeRemoteCommand(unsigned long id, const String& type) {
  if (id == 0) return;
  if (id <= lastRemoteCommandId) {
    queueRemoteAck(id, "duplicate_ignored", "command id already handled in this boot");
    return;
  }

  // ID vor der Aktion merken: Falls danach eine ACK verloren geht, verhindert
  // der ESP32 im laufenden Boot trotzdem Replay-Doppelalarme.
  lastRemoteCommandId = id;

  // Hinweis: Der manuelle REAL ALARM laeuft inzwischen DIREKT vom NAS zu Bark
  // (Dashboard sendet keinen ALARM-Befehl mehr an den ESP32). Dieser Zweig
  // bleibt als Protokoll-Absicherung erhalten und nutzt weiterhin denselben
  // Pfad wie ein Relaisalarm.
  if (type == "ALARM") {
    if (alarmBlockedNow()) {
      queueRemoteAck(id, "skipped_busy", "alarm state machine blocked by cooldown or rearm");
      return;
    }
    startAlarm();  // identischer Pfad wie Relais: Nachsende-Puffer + keyDone[]
    queueRemoteAck(id, "executed", "real alarm started");
    return;
  }

  if (type == "TEST") {
    bool ok = sendStatus("Alarm-Waechter Test",
                         "Manueller Test ueber NAS-Dashboard.");
    queueRemoteAck(id, ok ? "executed" : "status_failed",
                   ok ? "test status sent via Bark"
                      : "bark status send FAILED - check BARK_KEY_STATUS/WLAN");
    return;
  }

  queueRemoteAck(id, "unknown_command", "unsupported command type");
}

void pollRemoteCommandIfDue() {
  if (!remoteConfigured() || !remoteReady()) return;
  if (remoteAckPending) return;  // erst ACK sauber nachziehen, dann neu pollen

  unsigned long now = millis();
  if (now - lastRemotePoll < REMOTE_POLL_INTERVAL_MS) return;
  if (lastRemoteError != 0 && now - lastRemoteError < REMOTE_ERROR_BACKOFF_MS) return;
  lastRemotePoll = now;

  String response;
  String nonce = String(now, HEX) + "-" + String(lastRemoteCommandId, HEX);
  String form = remoteBaseForm()
    + "&last_command_id=" + String(lastRemoteCommandId)
    + "&nonce=" + urlEncode(nonce);
  if (!remotePostForm("poll.php", form, &response)) {
    lastRemoteError = now;
    return;
  }
  lastRemoteError = 0;

  if (lineValue(response, "nonce") != nonce) {
    Serial.println("NAS-Antwort mit falscher Poll-Nonce verworfen.");
    lastRemoteError = now;
    return;
  }

  unsigned long id = (unsigned long)lineValue(response, "id").toInt();
  String type = lineValue(response, "type");
  if (id != 0 && type != "NONE" && type.length() > 0) {
    Serial.printf("NAS-Befehl empfangen: #%lu %s\n", id, type.c_str());
    executeRemoteCommand(id, type);
  }

  // Empfängerliste nachziehen, wenn das Dashboard eine andere Version meldet.
  // keys_version=0 heißt: auf dem NAS wird (noch) keine Liste gepflegt.
  unsigned long remoteKeysVersion =
      (unsigned long)lineValue(response, "keys_version").toInt();
  if (remoteKeysVersion != 0 && remoteKeysVersion != alarmKeysVersion) {
    syncAlarmKeys();
  }
}

void handleRemoteMonitor() {
  processRemoteAck();
  pollRemoteCommandIfDue();
  pushRemoteStatusIfDue();
}

// ============================ SELBSTTEST ====================================

// Kleiner Check beim Start: Pins, Key-Anzahl, WLAN, eine Status-Meldung.
void selfTest() {
  Serial.println("\n=== SELBSTTEST ===");
  Serial.printf("Eingang Pin %d, ACTIVE_LOW=%s, aktueller Stand: %s\n",
                RELAY_PIN, ACTIVE_LOW ? "ja" : "nein",
                relayActive() ? "AKTIV (Kontakt zu)" : "offen (Ruhe)");

  Serial.printf("Alarm-Empfänger: %d (Listen-Version %lu, 0 = config.h-Startliste)\n",
                alarmKeyCount, alarmKeysVersion);
  if (alarmKeyCount == 0) {
    Serial.println("WARNUNG: KEIN Alarm-Empfänger! config.h bzw. Dashboard-Liste prüfen.");
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
          lastHeartbeatInfo = timePrefix() + "OK";
          lastHeartbeatOk = true;
        } else {
          Serial.println("Heartbeat fehlgeschlagen - naechster Versuch spaeter.");
          lastHeartbeatInfo = timePrefix() + "FEHLER";
          lastHeartbeatOk = false;
        }
        requestRemoteStatusPush();
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
      lastHeartbeatInfo = timePrefix() + "OK";
      lastHeartbeatOk = true;
    } else {
      Serial.println("Heartbeat fehlgeschlagen - naechster Versuch spaeter.");
      lastHeartbeatInfo = timePrefix() + "FEHLER";
      lastHeartbeatOk = false;
    }
    requestRemoteStatusPush();
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

  loadAlarmKeys();   // Empfängerliste: NVS (Dashboard-Stand) oder config.h-Start
  setupWatchdog();
  connectWiFi();
  lastWifiConnected = (WiFi.status() == WL_CONNECTED);
  setupTime();
  selfTest();

  lastHeartbeatMs = millis();
  // Start-Meldung leise nur an dich.
  bool startMsgOk = sendStatus("Alarm-Waechter online",
                               "System gestartet und ueberwacht den Relaiskontakt.");
#if HEARTBEAT_ENABLED && NTP_ENABLED
  // Boot nach der Heartbeat-Stunde: Die (erfolgreiche) Start-Meldung zählt als
  // heutiges Lebenszeichen - sonst kämen zwei Meldungen direkt hintereinander.
  struct tm bootTime;
  if (startMsgOk && getLocalTime(&bootTime, 50) && bootTime.tm_hour >= HEARTBEAT_HOUR) {
    lastHeartbeatDay  = bootTime.tm_yday;
    lastHeartbeatInfo = timePrefix() + "OK (Start-Meldung)";
    lastHeartbeatOk   = true;
  }
#else
  (void)startMsgOk;   // nur fuer den Heartbeat-Zweig gebraucht
#endif
  requestRemoteStatusPush();
}

void loop() {
  feedWatchdog();
  maintainWiFi();
  processPendingAlarm();   // offene Alarm-Zustellungen zuerst nachholen
  handleHeartbeat();
  handleRemoteMonitor();

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
        requestRemoteStatusPush();
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
        requestRemoteStatusPush();
      }
    }
  }

  // Vom Interrupt eingerasteten Impuls abholen (Merker dabei zurücksetzen).
  bool latched = isrLatched;
  if (latched) isrLatched = false;

  if (alarmBlockedNow()) {
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
