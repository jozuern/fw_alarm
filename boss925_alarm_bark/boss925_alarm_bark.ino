/*
  BOSS-925 Alarm-Wächter -> Bark (iOS Critical Alerts)
  ----------------------------------------------------------------------------
  Liest den potenzialfreien Relaiskontakt eines Swissphone-Ladegeräts
  (z.B. LGRA-Expert, DIN-Buchse Pin 1 + 3). Der Kontakt schließt, wenn der
  eingesteckte BOSS 925 eine Alarmierung empfängt. Der ESP32 erkennt das und
  sendet über Bark einen Critical Alert an alle Empfänger (umgeht Stumm-
  schaltung und "Nicht stören" auf iOS).

    - ALARM     -> Critical Alert an alle Keys der Empfängerliste (im
                   NAS-Dashboard gepflegt; BARK_KEYS_ALARM ist die Startliste).
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
      bis alle versorgt sind oder ALARM_RETRY_GIVEUP_MS erreicht ist. Lehnt
      Bark einen Key dauerhaft ab (HTTP 400/401/403/404), wird er sofort
      aufgegeben und du wirst gewarnt.
    - Neustart-fest: Offene Zustellung, Wiederbewaffnung, Cooldown und der
      Alarmzähler liegen zusätzlich im RTC-Speicher (mit Prüfsumme). Nach
      einem Watchdog-/Absturz-/Brownout-Neustart geht die Zustellung weiter,
      und ein noch geschlossener Kontakt alarmiert NICHT erneut. Nur beim
      Einschalten (Power-on) beginnt alles von vorn.
    - Klemm-Warnung: Bleibt der Kontakt ungewöhnlich lange geschlossen,
      bekommst du einen Status-Hinweis (solange sind keine neuen Alarme
      erkennbar).
    - Warnungen an dich (Zustellung unvollständig, Kontakt klemmt, Liste
      übernommen ...) werden wiederholt, bis Bark sie angenommen hat.

  Diese Lösung ist eine ZUSATZ-Alarmierung, KEIN Ersatz für den Melder.

  Alle einstellbaren Werte stehen in config.h (Kopie von config.example.h).
  Externe Libraries: keine (WiFi/HTTPClient/WiFiClientSecure/Zeit/Watchdog/
  Preferences sind alle im ESP32-Arduino-Core enthalten).
  ----------------------------------------------------------------------------
*/

#include <stddef.h>      // offsetof() für die RTC-Prüfsumme
#include <WiFi.h>
#include <HTTPClient.h>
#include <WiFiClientSecure.h>
#include <time.h>
#include <esp_task_wdt.h>
#include <esp_timer.h>   // 64-Bit-Uptime (millis() liefe nach 49,7 Tagen über)
#include <esp_system.h>  // esp_reset_reason() für die Neustart-Diagnose im Dashboard
#include <esp_attr.h>    // RTC_NOINIT_ATTR: Alarmzustand übersteht Watchdog-/Absturz-Neustarts
#include <Preferences.h> // NVS-Speicher für die vom Dashboard verwaltete Empfängerliste

#include "config.h"   // <-- DEINE WERTE. Falls dieser Include fehlschlägt:
                      //     config.example.h nach config.h kopieren!

// ======================= FALLBACKS FÜR NEUERE WERTE =========================
// config.h ist gitignored und gehört dem Nutzer. Damit eine ÄLTERE config.h
// ohne diese Einträge weiter kompiliert, gelten hier die Standardwerte aus
// config.example.h (Bedeutung siehe dort).
#ifndef WIFI_RECONNECT_MS
#define WIFI_RECONNECT_MS          (60UL*1000UL)
#endif
#ifndef STATUS_RETRY_MS
#define STATUS_RETRY_MS            (5UL*60UL*1000UL)
#endif
#ifndef REMOTE_ACK_MAX_TRIES
#define REMOTE_ACK_MAX_TRIES       10
#endif
#ifndef KEY_SYNC_REJECT_BACKOFF_MS
#define KEY_SYNC_REJECT_BACKOFF_MS (60UL*1000UL)
#endif
#ifndef WDT_TIMEOUT_S
#define WDT_TIMEOUT_S              60
#endif

// Anzahl der Alarm-Keys automatisch aus dem Array in config.h berechnen.
static const int BARK_KEYS_ALARM_COUNT =
    sizeof(BARK_KEYS_ALARM) / sizeof(BARK_KEYS_ALARM[0]);

// Maximal verwaltbare Alarm-Empfänger (Dashboard-Liste + NVS-Speicher).
static const int MAX_ALARM_KEYS = 10;

// ============================ ZEITLIMITS / WATCHDOG =========================
// Der Core lässt den TLS-Handshake ohne eigenes Limit bis zu 120 s laufen -
// länger als der Watchdog. Deshalb bekommt JEDE HTTPS-Anfrage ein
// Handshake-Limit, und der Watchdog wird so bemessen, dass ein einzelner
// Versuch im ungünstigsten Fall sicher darunter bleibt.
static const unsigned long BARK_HTTP_TIMEOUT_MS = 8000UL;
static const unsigned long BARK_TLS_TIMEOUT_S   = BARK_HTTP_TIMEOUT_MS / 1000UL;
// NAS: REMOTE_HTTP_TIMEOUT_MS aufgerundet auf Sekunden, mindestens 4 s (der
// Handshake ESP32<->Synology braucht typischerweise 1-3 s).
static const unsigned long REMOTE_TIMEOUT_S     = (REMOTE_HTTP_TIMEOUT_MS + 999UL) / 1000UL;
static const unsigned long REMOTE_TLS_TIMEOUT_S = REMOTE_TIMEOUT_S < 4UL ? 4UL : REMOTE_TIMEOUT_S;
// Ungünstigster Fall EINES Versuchs in Sekunden: DNS (lwIP gibt nach ~15 s
// auf) + Verbindungsaufbau + TLS-Handshake + Senden + Antwort-Kopf (beim NAS
// zusätzlich der Antworttext). Jede Phase ist einzeln durch ihr Limit begrenzt.
static const unsigned long DNS_WORST_S            = 15UL;
static const unsigned long BARK_ATTEMPT_WORST_S   = DNS_WORST_S + 4UL * BARK_TLS_TIMEOUT_S;
static const unsigned long REMOTE_ATTEMPT_WORST_S = DNS_WORST_S + 4UL * REMOTE_TIMEOUT_S
                                                  + REMOTE_TLS_TIMEOUT_S;
static const unsigned long ATTEMPT_WORST_S = BARK_ATTEMPT_WORST_S > REMOTE_ATTEMPT_WORST_S
                                           ? BARK_ATTEMPT_WORST_S : REMOTE_ATTEMPT_WORST_S;
// Watchdog: mindestens 60 s und immer >= ungünstigster Versuch + 10 s Reserve.
// Ein kleinerer WDT_TIMEOUT_S aus einer älteren config.h (früher 30 s) wird
// angehoben - sonst könnte ein einzelner langsamer Handshake einen Neustart
// mitten in der Alarm-Zustellung auslösen. Gefüttert wird vor und nach jedem
// Versuch und zwischen den Empfängern.
static const unsigned long WDT_MIN_TIMEOUT_S = (ATTEMPT_WORST_S + 10UL) > 60UL
                                             ? (ATTEMPT_WORST_S + 10UL) : 60UL;
static const unsigned long WDT_EFFECTIVE_TIMEOUT_S =
    ((unsigned long)WDT_TIMEOUT_S) > WDT_MIN_TIMEOUT_S ? (unsigned long)WDT_TIMEOUT_S
                                                       : WDT_MIN_TIMEOUT_S;

// sendBark(): Pseudo-HTTP-Code "kein WLAN" (zählt wie ein Transportfehler < 0).
static const int BARK_CODE_NO_WIFI = -100;

// Pegel-Abfrage: Liegen zwei Proben weiter auseinander (loop() steckte in
// einer blockierenden Anfrage), kann die Abfrage selbst nicht belegen, dass
// der Kontakt dazwischen durchgehend geschlossen war. Normale Durchläufe
// dauern ~20-90 ms (ohne NTP-Zeit wartet getLocalTime() bis zu 50 ms).
static const unsigned long LEVEL_SAMPLE_GAP_MS = (DEBOUNCE_MS / 2UL) < 100UL ? 100UL
                                                                             : (DEBOUNCE_MS / 2UL);
// Startphase: So lange läuft nach setup() NUR die Relais-Erkennung (plus eine
// offene Zustellung) - kein NAS-Request, keine Start-Meldung. Ein Kontakt,
// der beim Neustart schon geschlossen ist, wird so zuerst alarmiert.
static const unsigned long STARTUP_DETECT_MS = DEBOUNCE_MS + 200UL;
// Abstand, in dem laufende Zeiten (Cooldown, Nachsende-Frist) in den
// RTC-Speicher geschrieben werden.
static const unsigned long RTC_SAVE_INTERVAL_MS = 1000UL;

// Firmware-Kennung fürs Dashboard: FW_BUILD_MARKER aus config.h plus
// automatisch der Kompilier-Zeitpunkt - ein vergessener Marker-Wechsel lässt
// sich so trotzdem einem Build zuordnen.
static const char* FW_BUILD_INFO = FW_BUILD_MARKER " " __DATE__ " " __TIME__;

// Version des Arduino-ESP32-Cores (z.B. "2.0.17") für Status-Push und Log.
#define FW_STRINGIFY2(x) #x
#define FW_STRINGIFY(x)  FW_STRINGIFY2(x)
#if defined(ESP_ARDUINO_VERSION_MAJOR) && defined(ESP_ARDUINO_VERSION_MINOR) && defined(ESP_ARDUINO_VERSION_PATCH)
static const char* CORE_VERSION = FW_STRINGIFY(ESP_ARDUINO_VERSION_MAJOR) "."
                                  FW_STRINGIFY(ESP_ARDUINO_VERSION_MINOR) "."
                                  FW_STRINGIFY(ESP_ARDUINO_VERSION_PATCH);
#else
static const char* CORE_VERSION = "unbekannt";
#endif

// ============================ INTERNE VARIABLEN =============================

// --- Alarm-Erkennung -------------------------------------------------------
unsigned long activeSince        = 0;     // seit wann ist der Kontakt aktiv (Pegel-Abfrage)?
unsigned long lastLevelSample    = 0;     // Zeitpunkt der letzten Pegel-Probe
unsigned long lastAlarm          = 0;     // Zeitpunkt des letzten RELAIS-Alarms
// Cooldown als eigener Merker: Der frühere Vergleich "lastAlarm != 0 &&
// now - lastAlarm < COOLDOWN_MS" kippte nach 49,7 Tagen ohne Alarm (millis()-
// Überlauf) und sperrte dann 5 min lang jeden neuen Alarm.
bool          cooldownActive     = false;
bool          waitingForRelease  = false; // nach Alarm: erst wieder scharf, wenn Kontakt offen war
unsigned long openSince          = 0;     // seit wann ist der Kontakt (wieder) offen?
bool          stuckWarned        = false; // "Kontakt klemmt" erkannt (Status-Feld stuck_warned)
bool          stuckNoticeDelivered = false; // ... und die Warnung ist bei Bark angekommen

// --- Nachsende-Puffer (Alarm überlebt WLAN-Ausfall) -------------------------
bool          alarmPending    = false; // es gibt noch nicht zugestellte Empfänger
// Der offene Alarm stammt vom DEMO-ALARM-Befehl des Dashboards (nicht vom
// Relais). So ein Alarm rührt Cooldown/Wiederbewaffnung NICHT an, und ein
// echter Relaisalarm hat jederzeit Vorrang.
bool          pendingIsRemote = false;
String        pendingStamp;            // Zeitstempel der ERKENNUNG ("[..] " oder "")
String        pendingBody;             // Alarm-Text inkl. Zeitstempel der ERKENNUNG
unsigned long pendingSince   = 0;      // wann wurde der Alarm erkannt?
int           pendingVolume  = ALARM_VOLUME;  // Lautstärke, bei der ERKENNUNG festgelegt
unsigned long lastSendRound  = 0;      // wann lief die letzte Sende-Runde?
bool          firstRoundDone = false;  // erste Runde läuft sofort, ohne Wartezeit
int           roundStartIdx  = 0;      // nach Abbruch: nächste Runde beginnt hier
bool          keyDone[MAX_ALARM_KEYS];
bool          keyFailed[MAX_ALARM_KEYS]; // von Bark dauerhaft abgelehnt (4xx) - aufgegeben
// Fortschritt der letzten Zustellung, beim Alarm festgehalten: Ein späterer
// Listen-Sync verschiebt so nicht die Anzeige "erreicht/gesamt".
int           deliveryTotal  = 0;
int           deliveryDone   = 0;
bool          alarmGaveUp    = false;  // letzte Zustellung endete, ohne alle zu erreichen
uint32_t      alarmSeq       = 0;      // zählt JEDEN Alarm (auch textgleiche) fürs NAS
uint8_t       alarmSource    = 0;      // 0 = noch keiner, 1 = relay, 2 = remote
uint32_t      pendingKeyHash = 0;      // Prüfsumme der Empfängerliste beim Alarm

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
bool          keyStoreOpen     = false; // NVS geöffnet? Sonst wird nichts gespeichert.

// --- Vom Pin-Interrupt gesetzter Alarm-Merker --------------------------------
// Der Interrupt misst die Impulsdauer selbst. War der Kontakt mindestens
// DEBOUNCE_MS geschlossen, wird der Alarm "eingerastet" - auch wenn loop()
// gerade in einer blockierenden HTTPS-Sendung steckt.
portMUX_TYPE           isrMux       = portMUX_INITIALIZER_UNLOCKED;
volatile bool          isrLatched   = false;
volatile unsigned long isrLatchedAt = 0;  // Beginn des eingerasteten Impulses (millis)
volatile unsigned long isrSince1    = 0;  // Beginn der laufenden Schließung (0 = offen)
#if RELAY_PIN2_ENABLED
volatile unsigned long isrSince2    = 0;
#endif

// --- Heartbeat ---------------------------------------------------------------
unsigned long lastHeartbeatMs  = 0;   // für Heartbeat OHNE NTP (millis-Abstand)
int           lastHeartbeatDay = -1;  // für Heartbeat MIT NTP (1x pro Kalendertag)
unsigned long lastHeartbeatTry = 0;   // letzter VERSUCH (für Wiederholung nach Fehler)
String        lastHeartbeatInfo = "nie";
bool          lastHeartbeatOk   = false;

// --- WLAN --------------------------------------------------------------------
unsigned long lastWifiBegin     = 0;  // letzter (harter) Verbindungsaufbau per WiFi.begin()
unsigned long wifiLostAt        = 0;  // seit wann ist das WLAN weg?
bool          lastWifiConnected = false;

// --- Start / Watchdog ----------------------------------------------------------
unsigned long setupDoneMs         = 0;
bool          startupDone         = false; // Startphase (nur Erkennung) vorbei?
bool          startMessageDone    = false; // Start-Meldung schon versucht?
bool          wdtActive           = false;
bool          restoredActiveState = false; // offener Alarmzustand aus dem RTC übernommen?

// --- NAS-Fernüberwachung -----------------------------------------------------
unsigned long lastRemoteStatusPush    = 0;
unsigned long lastRemoteStatusAttempt = 0;
unsigned long lastRemotePoll          = 0;
unsigned long lastRemoteError         = 0;  // Backoff für Poll/ACK/Key-Sync
unsigned long lastStatusError         = 0;  // eigener Backoff für den Status-Push
unsigned long lastRemoteAckTry        = 0;
bool          remoteStatusDirty       = true;
unsigned long lastRemoteCommandId     = 0;
bool          remoteAckPending        = false;
int           remoteAckFails          = 0;  // Fehlversuche der offenen ACK
bool          nasAuthWarned           = false;  // 403-Warnung schon verschickt?
unsigned long remoteAckId             = 0;
String        remoteAckResult;
String        remoteAckMessage;
String        lastAlarmInfo           = "nie";
unsigned long keySyncRejectedAt       = 0;  // letzte verworfene Liste (eigener Backoff)
bool          keySyncRejectNotified   = false;

// --- Warnungen an den Owner (BARK_KEY_STATUS) ----------------------------------
// Jede Warnung bleibt "offen", bis Bark sie angenommen hat (wie beim
// Heartbeat), und wird nach STATUS_RETRY_MS erneut versucht. Gesendet wird
// nie während einer offenen Alarm-Zustellung (Alarm hat Vorrang) und
// höchstens eine pro loop()-Durchlauf. Reihenfolge = Priorität.
static const int NOTICE_NO_KEYS       = 0;  // keine Empfänger (Boot oder Alarm)
static const int NOTICE_KEY_REJECTED  = 1;  // Bark lehnt einen Key dauerhaft ab
static const int NOTICE_GAVEUP        = 2;  // Nachsenden aufgegeben
static const int NOTICE_STUCK         = 3;  // Kontakt klemmt?
static const int NOTICE_RELEASED      = 4;  // Kontakt wieder offen (nach Klemm-Warnung)
static const int NOTICE_KEYS_UPDATED  = 5;  // Empfängerliste übernommen
static const int NOTICE_SYNC_REJECTED = 6;  // Empfängerliste vom NAS verworfen
static const int NOTICE_WDT           = 7;  // Watchdog nicht aktiv
static const int NOTICE_COUNT         = 8;
bool          noticePending[NOTICE_COUNT];
String        noticeTitle[NOTICE_COUNT];
String        noticeBody[NOTICE_COUNT];
unsigned long lastNoticeFail = 0;   // nach einem Fehlschlag pausieren ALLE Warnungen
String        rejectedKeysText;     // gesammelte abgelehnte Keys (maskiert)
static const char* NO_KEYS_BOOT_TITLE = "Keine Alarm-Empfaenger";

// --- Alarmzustand im RTC-Speicher ----------------------------------------------
// Überlebt jeden Neustart außer dem Einschalten (Power-on). Ohne diesen Puffer
// gingen nach einem Watchdog-/Absturz-/Brownout-Neustart offene Zustellungen
// verloren, und ein noch geschlossener Kontakt alarmierte nach JEDEM Neustart
// erneut (eine Absturz-Schleife wäre ein Dauerfeuer). Nur einfache Typen -
// der Speicher wird beim Start nicht initialisiert; Magic, Größe und
// Prüfsumme erkennen Müll und ein anderes Layout (andere Firmware).
struct RtcAlarmState {
  uint32_t magic;                // RTC_STATE_MAGIC
  uint32_t size;                 // sizeof(RtcAlarmState)
  uint32_t alarmSeq;
  uint32_t pendingElapsedMs;     // ms seit Erkennung des offenen Alarms (beim Sichern)
  uint32_t relayAlarmElapsedMs;  // ms seit dem letzten RELAIS-Alarm (Cooldown/Klemm-Prüfung)
  uint32_t keyListHash;          // Prüfsumme der Empfängerliste beim Alarm
  uint16_t keyDoneMask;          // Bit i: Key i hat den Alarm bekommen
  uint16_t keyFailedMask;        // Bit i: Key i wurde von Bark dauerhaft abgelehnt
  uint8_t  alarmPending;
  uint8_t  pendingIsRemote;
  uint8_t  waitingForRelease;
  uint8_t  cooldownActive;
  uint8_t  stuckWarned;
  uint8_t  stuckNoticeDelivered;
  uint8_t  alarmGaveUp;
  uint8_t  alarmSource;
  uint8_t  pendingVolume;
  uint8_t  deliveryTotal;
  uint8_t  deliveryDone;
  uint8_t  reserved;
  char     stamp[32];            // Zeitstempel der Erkennung, z.B. "[15.11.2026 14:23] "
  uint32_t checksum;             // FNV-1a über alle Felder davor
};
RTC_NOINIT_ATTR RtcAlarmState rtcState;
static const uint32_t RTC_STATE_MAGIC = 0x46574131UL;  // "FWA1"
bool          rtcReady    = false;  // erst nach restoreRtcState() schreiben
unsigned long lastRtcSave = 0;

// Vorab-Deklarationen (damit die Reihenfolge im File egal ist).
void feedWatchdog();
void processPendingAlarm();
void requestRemoteStatusPush();
void handleRemoteMonitor();
bool alarmBlockedNow();
void saveRtcState();
void queueNotice(int id, const String& title, const String& body);

// ============================ WLAN ==========================================

// Verbindungsaufbau anstoßen (nicht blockierend) und den Zeitpunkt merken.
void wifiBegin() {
  WiFi.mode(WIFI_STA);
  WiFi.setAutoReconnect(true);          // Core soll selbst nachverbinden
  WiFi.begin(WIFI_SSID, WIFI_PASSWORD);
  lastWifiBegin = millis();
  if (lastWifiBegin == 0) lastWifiBegin = 1;
}

// WLAN-Verlust einmalig vermerken (egal, wer ihn zuerst bemerkt: die
// loop()-Pflege oder ein Sendeversuch). Ab hier läuft die Frist bis zum
// harten Neuaufbau.
void noteWifiLost() {
  if (!lastWifiConnected) return;
  lastWifiConnected = false;
  wifiLostAt = millis();
  if (wifiLostAt == 0) wifiLostAt = 1;
  Serial.println("WLAN weg - der Core verbindet selbst neu (AutoReconnect).");
}

// Darf die Firmware die Verbindung hart neu aufbauen? Erst, wenn das WLAN
// schon WIFI_RECONNECT_MS weg ist UND der letzte eigene Anstoß so lange her
// ist. Vorher arbeitet der AutoReconnect des Cores - ein WiFi.begin()
// mittendrin bricht dessen laufenden Verbindungsaufbau nur ab.
bool wifiHardReconnectDue() {
  if (lastWifiBegin == 0) return true;   // noch nie angestoßen
  unsigned long now = millis();
  bool lostLongEnough = (wifiLostAt == 0) || (now - wifiLostAt >= WIFI_RECONNECT_MS);
  return lostLongEnough && (now - lastWifiBegin >= WIFI_RECONNECT_MS);
}

// Zeitlich begrenztes Warten auf die Verbindung (blockierend, max ~15 s).
// Wird beim Start und vor jedem Senden benutzt. Stößt nur dann selbst neu an,
// wenn kein Verbindungsaufbau mehr läuft (siehe wifiHardReconnectDue()).
bool connectWiFi(unsigned long timeoutMs = 15000) {
  if (WiFi.status() == WL_CONNECTED) return true;
  noteWifiLost();
  if (wifiHardReconnectDue()) {
    if (lastWifiBegin != 0) WiFi.disconnect();
    wifiBegin();
  }
  unsigned long start = millis();
  while (WiFi.status() != WL_CONNECTED && millis() - start < timeoutMs) {
    delay(250);
    feedWatchdog();                     // Watchdog auch beim Warten füttern
  }
  return WiFi.status() == WL_CONNECTED;
}

// Nicht-blockierende Pflege im loop(). Früher riss sie alle 10 s per
// disconnect()/begin() den gerade laufenden Verbindungsaufbau ab. Jetzt
// verbindet zuerst der Core selbst neu; hart neu aufgebaut wird erst nach
// WIFI_RECONNECT_MS ohne Verbindung (und dann höchstens in diesem Abstand).
void maintainWiFi() {
  if (WiFi.status() == WL_CONNECTED) {
    if (!lastWifiConnected) {
      lastWifiConnected = true;
      Serial.println("WLAN wieder verbunden.");
      requestRemoteStatusPush();
    }
    return;
  }
  noteWifiLost();
  if (!wifiHardReconnectDue()) return;
  Serial.println("WLAN weiterhin weg -> Verbindung wird neu aufgebaut...");
  WiFi.disconnect();
  wifiBegin();
}

// ============================ WATCHDOG ======================================

// Der Hardware-Watchdog startet den ESP32 neu, falls die Firmware hängt
// (z.B. in einer Endlosschleife). Wir müssen ihn regelmäßig "füttern".
void feedWatchdog() {
#if WDT_ENABLED
  esp_task_wdt_reset();
#endif
}

// Gibt true zurück, wenn der Watchdog aktiv ist (bzw. bewusst abgeschaltet).
bool setupWatchdog() {
#if WDT_ENABLED
  // Die Watchdog-API hat sich zwischen ESP32-Core 2.x und 3.x geändert.
  #if ESP_ARDUINO_VERSION_MAJOR >= 3
    esp_task_wdt_config_t cfg = {
      .timeout_ms     = (uint32_t)(WDT_EFFECTIVE_TIMEOUT_S * 1000UL),
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
      return false;
    }
  #else
    // Core 2.x: init() konfiguriert einen schon laufenden Watchdog um.
    esp_err_t err = esp_task_wdt_init(WDT_EFFECTIVE_TIMEOUT_S, true);
    if (err != ESP_OK) {
      Serial.println("WARNUNG: Watchdog liess sich nicht konfigurieren!");
      return false;
    }
  #endif
  // aktuellen Task (loop) überwachen; INVALID_ARG = schon angemeldet.
  esp_err_t addErr = esp_task_wdt_add(NULL);
  if (addErr != ESP_OK && addErr != ESP_ERR_INVALID_ARG) {
    Serial.println("WARNUNG: loop-Task liess sich nicht beim Watchdog anmelden!");
    return false;
  }
  if (WDT_EFFECTIVE_TIMEOUT_S != (unsigned long)WDT_TIMEOUT_S) {
    Serial.printf("Hinweis: WDT_TIMEOUT_S=%d aus config.h ist zu knapp fuer einen "
                  "HTTPS-Versuch - verwendet werden %lu s.\n",
                  (int)WDT_TIMEOUT_S, WDT_EFFECTIVE_TIMEOUT_S);
  }
  Serial.printf("Watchdog aktiv (%lu s).\n", WDT_EFFECTIVE_TIMEOUT_S);
  return true;
#else
  return true;
#endif
}

// ============================ ZEIT / NTP ====================================

void setupTime() {
#if NTP_ENABLED
  configTime(0, 0, NTP_SERVER_1, NTP_SERVER_2);  // Roh-Zeit (UTC) holen
  setenv("TZ", NTP_TZ, 1);                        // Zeitzone setzen
  tzset();
  // kurz auf eine plausible Zeit warten (max ~5 s). Ein offener oder gerade
  // eintreffender Alarm wartet NICHT auf die Uhrzeit: dann sofort weiter
  // (die Zeit kommt im Hintergrund; ohne Zeit gilt die volle Lautstärke).
  struct tm t;
  unsigned long start = millis();
  while (!getLocalTime(&t, 200) && millis() - start < 5000) {
    feedWatchdog();
    if (alarmPending || isrLatched || (!waitingForRelease && relayActive())) break;
  }
  if (getLocalTime(&t, 50)) {
    Serial.printf("Zeit synchronisiert: %02d:%02d:%02d\n",
                  t.tm_hour, t.tm_min, t.tm_sec);
  } else {
    Serial.println("Zeit (NTP) noch nicht verfügbar - läuft im Hintergrund.");
  }
#endif
}

// "[09.06.2026 14:23] " aus einer Ortszeit.
String stampFromTm(const struct tm& t) {
  char buf[64];   // großzügig: snprintf kann die int-Felder nicht begrenzen
  snprintf(buf, sizeof(buf), "[%02d.%02d.%04d %02d:%02d] ",
           t.tm_mday, t.tm_mon + 1, t.tm_year + 1900, t.tm_hour, t.tm_min);
  return String(buf);
}

// Liefert "[09.06.2026 14:23] " oder "" falls keine Zeit vorhanden.
String timePrefix() {
#if NTP_ENABLED
  struct tm t;
  if (getLocalTime(&t, 50)) return stampFromTm(t);
#endif
  return "";
}

// Liegt die Ortszeit t im Zeitfenster des wöchentlichen ILS-Probealarms?
bool inProbeAlarmWindow(const struct tm& t) {
  if (PROBE_WEEKDAY >= 0 && t.tm_wday != PROBE_WEEKDAY) return false;
  int minutes = t.tm_hour * 60 + t.tm_min;
  return minutes >= PROBE_WINDOW_START_MIN && minutes <= PROBE_WINDOW_END_MIN;
}

// Zeitstempel und Lautstärke eines Alarms, der vor msAgo ms erkannt wurde
// (0 = jetzt; > 0 bei einem eingerasteten Impuls, der erst nach einer
// blockierenden Sendung verarbeitet wird). Beides wird EINMAL bei der
// Erkennung festgelegt. Ohne gültige NTP-Zeit (getLocalTime() schlägt fehl)
// gibt es keinen Zeitstempel und bewusst die VOLLE Lautstärke: ein echter
// Alarm darf nie wegen einer fehlenden Uhrzeit leiser ankommen.
void alarmTimingFor(unsigned long msAgo, String& stamp, int& volume) {
  stamp  = "";
  volume = ALARM_VOLUME;
  struct tm t;
  if (!getLocalTime(&t, 50)) return;
  if (msAgo >= 1000UL) {
    time_t then = time(nullptr) - (time_t)(msAgo / 1000UL);
    if (localtime_r(&then, &t) == nullptr) return;
  }
#if NTP_ENABLED
  stamp = stampFromTm(t);
#endif
  if (inProbeAlarmWindow(t)) volume = PROBE_ALARM_VOLUME;
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

// Bark-Keys sind Geheimnisse (wer sie kennt, kann das iPhone alarmieren):
// im seriellen Log und in Warnungen nur die ersten 4 Zeichen zeigen.
String maskKey(const char* key) {
  String k = (key != nullptr) ? String(key) : String();
  if (k.length() <= 4) return "****";
  return k.substring(0, 4) + "...";
}

// Sendet eine Bark-Nachricht an EINEN Device-Key, mit bis zu BARK_MAX_TRIES
// Versuchen. Gibt true zurück, wenn der Server mit HTTP 200 antwortet.
// Bei HTTP 4xx (z.B. falscher Key) wird NICHT wiederholt - das kann eine
// Wiederholung nicht beheben.
// httpCode (darf nullptr sein) bekommt den Code des LETZTEN Versuchs:
// < 0 = Transportfehler (kein WLAN/DNS/TLS/Timeout), sonst HTTP-Status.
// level: "critical" (laut, umgeht Stumm/DND), "active", "timeSensitive", "passive".
bool sendBark(const char* key, const String& title, const String& body,
              const char* level, const char* sound, int volume, bool repeat, int* httpCode) {
  if (httpCode != nullptr) *httpCode = 0;
  if (key == nullptr || strlen(key) == 0) return false;

  if (!connectWiFi(15000)) {
    Serial.println("Kein WLAN - Nachricht nicht gesendet.");
    if (httpCode != nullptr) *httpCode = BARK_CODE_NO_WIFI;
    return false;
  }

  String form = "title="  + urlEncode(title)
              + "&body="  + urlEncode(body)
              + "&sound=" + urlEncode(sound)
              + "&level=" + urlEncode(level)
              + "&volume=" + String(volume);
  if (repeat) form += "&call=1";

  String url = String(BARK_HOST) + "/" + key;
  String masked = maskKey(key);

  for (int attempt = 1; attempt <= BARK_MAX_TRIES; attempt++) {
    feedWatchdog();         // jeder Versuch startet mit vollem Watchdog-Polster
    WiFiClientSecure client;
    client.setInsecure();   // ohne Zertifikatsprüfung (einfach & robust)
    // Ohne dieses Limit liefe ein hängender Handshake bis zu 120 s (Core-
    // Standard) - länger als der Watchdog. Angabe in SEKUNDEN.
    client.setHandshakeTimeout(BARK_TLS_TIMEOUT_S);
    HTTPClient http;
    http.setConnectTimeout(BARK_HTTP_TIMEOUT_MS);
    http.setTimeout(BARK_HTTP_TIMEOUT_MS);
    http.begin(client, url);
    http.addHeader("Content-Type", "application/x-www-form-urlencoded");

    int code = http.POST(form);
    http.end();
    feedWatchdog();
    if (httpCode != nullptr) *httpCode = code;

    Serial.printf("Bark POST (%s) Versuch %d/%d -> HTTP %d\n",
                  masked.c_str(), attempt, BARK_MAX_TRIES, code);

    if (code == 200) return true;             // Erfolg
    if (code >= 400 && code < 500) {          // Client-Fehler: Key/Anfrage falsch
      Serial.printf("HTTP %d - Wiederholen zwecklos, Key in der Dashboard-Liste/config.h pruefen!\n", code);
      return false;
    }
    if (attempt < BARK_MAX_TRIES) delay(500); // kurz warten, dann erneut
  }
  Serial.printf("Bark an %s endgültig fehlgeschlagen.\n", masked.c_str());
  return false;
}

// Leiser Status-/Heartbeat-Ping nur an dich. Gibt true bei Erfolg zurück.
// WICHTIG: title/body landen in der Bark-Notification -> NUR ASCII (ae/ue/oe),
// da Bark dort keine Umlaute korrekt darstellt. Gilt auch fuer ALARM_TITLE/_BODY.
bool sendStatus(const String& title, const String& body) {
  return sendBark(BARK_KEY_STATUS, title, timePrefix() + body,
                  "passive", STATUS_SOUND, STATUS_VOLUME, false, nullptr);
}

// ============================ WARNUNGEN AN DEN OWNER ========================
// Früher gingen "Zustellung unvollstaendig", "Kontakt klemmt?" und
// "Empfaengerliste aktualisiert" genau EINMAL raus - scheiterte dieser eine
// Versuch (WLAN-Schluckauf), erfuhr niemand davon. Jetzt bleiben sie offen,
// bis Bark sie angenommen hat. Der Zeitstempel im Text ist der des
// EREIGNISSES, nicht der des (vielleicht späteren) Versands.
// Bark-Texte: NUR ASCII (ae/oe/ue)!

void queueNotice(int id, const String& title, const String& body) {
  if (id < 0 || id >= NOTICE_COUNT) return;
  if (strlen(BARK_KEY_STATUS) == 0) return;   // kein Status-Key -> keine Warnungen
  noticePending[id] = true;
  noticeTitle[id]   = title;
  noticeBody[id]    = timePrefix() + body;
}

void cancelNotice(int id) {
  if (id >= 0 && id < NOTICE_COUNT) noticePending[id] = false;
}

// Nach einer zugestellten Warnung: zugehörige Merker nachziehen.
void onNoticeDelivered(int id) {
  if (id == NOTICE_STUCK) {
    stuckNoticeDelivered = true;   // erst jetzt gibt es auch ein "wieder offen"
    saveRtcState();
  } else if (id == NOTICE_KEY_REJECTED) {
    rejectedKeysText = "";
  }
}

// Ein Versuch für EINE Warnung. Erledigt nur bei Erfolg - oder wenn Bark den
// Status-Key dauerhaft ablehnt (400/401/403/404): Das behebt keine
// Wiederholung, sie wäre nur alle STATUS_RETRY_MS ein nutzloser TLS-Request.
bool trySendNotice(int id) {
  if (id < 0 || id >= NOTICE_COUNT || !noticePending[id]) return true;
  int code = 0;
  bool ok = sendBark(BARK_KEY_STATUS, noticeTitle[id], noticeBody[id],
                     "passive", STATUS_SOUND, STATUS_VOLUME, false, &code);
  if (ok) {
    noticePending[id] = false;
    lastNoticeFail = 0;
    onNoticeDelivered(id);
  } else if (code == 400 || code == 401 || code == 403 || code == 404) {
    noticePending[id] = false;
    Serial.println("Warnung verworfen: Bark lehnt BARK_KEY_STATUS ab - Key in config.h pruefen!");
  } else {
    lastNoticeFail = millis();
    if (lastNoticeFail == 0) lastNoticeFail = 1;
    Serial.println("Warnung an den Owner nicht zugestellt - neuer Versuch spaeter.");
  }
  return ok;
}

// Aus loop(): höchstens EINE Warnung pro Durchlauf, nie während eines offenen
// Alarms. Nach einem Fehlschlag pausieren alle (Bark/WLAN ist dann meist für
// alle weg) - sonst blockierten mehrere Warnungen hintereinander die Schleife.
void processOwnerNotices() {
  if (alarmPending) return;
  if (lastNoticeFail != 0 && millis() - lastNoticeFail < STATUS_RETRY_MS) return;
  for (int id = 0; id < NOTICE_COUNT; id++) {
    if (!noticePending[id]) continue;
    trySendNotice(id);
    return;
  }
}

// ============================ ALARMZUSTAND IM RTC ===========================

uint32_t fnv1a(const uint8_t* data, size_t len, uint32_t h) {
  for (size_t i = 0; i < len; i++) {
    h ^= data[i];
    h *= 16777619UL;
  }
  return h;
}

// Prüfsumme der aktuellen Empfängerliste: Passt nach einem Neustart die Liste
// aus dem NVS nicht mehr zu der beim Alarm, gelten die keyDone-Bits nicht.
uint32_t keyListHash() {
  uint32_t h = 2166136261UL;
  for (int i = 0; i < alarmKeyCount; i++) {
    h = fnv1a((const uint8_t*)alarmKeys[i].c_str(), alarmKeys[i].length(), h);
    h = fnv1a((const uint8_t*)"\n", 1, h);
  }
  return h;
}

uint32_t rtcChecksumOf(const RtcAlarmState& s) {
  return fnv1a((const uint8_t*)&s, offsetof(RtcAlarmState, checksum), 2166136261UL);
}

// Schreibt den aktuellen Alarmzustand in den RTC-Speicher. Wird bei jeder
// Änderung der Felder und während laufender Fristen periodisch aufgerufen
// (Zeiten werden als "vergangen seit" gesichert, weil millis() nach einem
// Neustart wieder bei 0 beginnt).
void saveRtcState() {
  if (!rtcReady) return;   // vor restoreRtcState() nichts überschreiben
  unsigned long now = millis();
  RtcAlarmState s;
  memset(&s, 0, sizeof(s));   // definierte Füllbytes -> stabile Prüfsumme
  s.magic                = RTC_STATE_MAGIC;
  s.size                 = sizeof(RtcAlarmState);
  s.alarmSeq             = alarmSeq;
  s.pendingElapsedMs     = alarmPending ? (uint32_t)(now - pendingSince) : 0;
  s.relayAlarmElapsedMs  = (waitingForRelease || cooldownActive) ? (uint32_t)(now - lastAlarm) : 0;
  s.keyListHash          = pendingKeyHash;
  for (int i = 0; i < alarmKeyCount && i < MAX_ALARM_KEYS; i++) {
    if (keyDone[i])   s.keyDoneMask   |= (uint16_t)(1u << i);
    if (keyFailed[i]) s.keyFailedMask |= (uint16_t)(1u << i);
  }
  s.alarmPending         = alarmPending ? 1 : 0;
  s.pendingIsRemote      = pendingIsRemote ? 1 : 0;
  s.waitingForRelease    = waitingForRelease ? 1 : 0;
  s.cooldownActive       = cooldownActive ? 1 : 0;
  s.stuckWarned          = stuckWarned ? 1 : 0;
  s.stuckNoticeDelivered = stuckNoticeDelivered ? 1 : 0;
  s.alarmGaveUp          = alarmGaveUp ? 1 : 0;
  s.alarmSource          = alarmSource;
  s.pendingVolume        = (uint8_t)constrain(pendingVolume, 0, 10);
  s.deliveryTotal        = (uint8_t)constrain(deliveryTotal, 0, MAX_ALARM_KEYS);
  s.deliveryDone         = (uint8_t)constrain(deliveryDone, 0, MAX_ALARM_KEYS);
  strlcpy(s.stamp, pendingStamp.c_str(), sizeof(s.stamp));
  s.checksum             = rtcChecksumOf(s);
  memcpy((void*)&rtcState, &s, sizeof(s));
  lastRtcSave = now;
}

// Nach dem Start (nach loadAlarmKeys()): Zustand aus dem RTC übernehmen -
// aber nur nach einem NICHT-Power-on-Neustart und mit gültiger Prüfsumme.
// Beim Einschalten ist der RTC-Speicher Zufall; dann beginnt alles von vorn.
void restoreRtcState() {
  esp_reset_reason_t reason = esp_reset_reason();
  RtcAlarmState s;
  memcpy(&s, (const void*)&rtcState, sizeof(s));
  bool valid = reason != ESP_RST_POWERON
            && s.magic == RTC_STATE_MAGIC
            && s.size == sizeof(RtcAlarmState)
            && s.checksum == rtcChecksumOf(s)
            && s.alarmPending <= 1 && s.pendingIsRemote <= 1
            && s.waitingForRelease <= 1 && s.cooldownActive <= 1
            && s.stuckWarned <= 1 && s.stuckNoticeDelivered <= 1
            && s.alarmGaveUp <= 1 && s.alarmSource <= 2
            && s.pendingVolume <= 10 && s.deliveryTotal <= MAX_ALARM_KEYS
            && s.deliveryDone <= s.deliveryTotal;
  rtcReady = true;
  if (!valid) {
    if (reason != ESP_RST_POWERON) {
      Serial.println("RTC-Alarmzustand ungueltig oder fremd - beginne ohne Altzustand.");
    }
    saveRtcState();   // definierten Anfangszustand schreiben
    return;
  }

  unsigned long now = millis();
  s.stamp[sizeof(s.stamp) - 1] = '\0';
  alarmSeq      = s.alarmSeq;
  alarmSource   = s.alarmSource;
  alarmGaveUp   = s.alarmGaveUp != 0;
  deliveryTotal = s.deliveryTotal;
  deliveryDone  = s.deliveryDone;
  pendingVolume = s.pendingVolume;
  pendingStamp  = String(s.stamp);
  if (alarmSource != 0) lastAlarmInfo = pendingStamp + ALARM_BODY;

  waitingForRelease    = s.waitingForRelease != 0;
  cooldownActive       = s.cooldownActive != 0;
  stuckWarned          = s.stuckWarned != 0;
  stuckNoticeDelivered = s.stuckNoticeDelivered != 0;
  if (waitingForRelease || cooldownActive) lastAlarm = now - s.relayAlarmElapsedMs;

  if (s.alarmPending) {
    alarmPending    = true;
    pendingIsRemote = s.pendingIsRemote != 0;
    pendingSince    = now - s.pendingElapsedMs;
    pendingBody     = pendingStamp + ALARM_BODY;
    pendingKeyHash  = s.keyListHash;
    bool sameList = (s.keyListHash == keyListHash()) && (deliveryTotal == alarmKeyCount);
    for (int i = 0; i < MAX_ALARM_KEYS; i++) {
      keyDone[i]   = sameList && (s.keyDoneMask   & (1u << i)) != 0;
      keyFailed[i] = sameList && (s.keyFailedMask & (1u << i)) != 0;
    }
    if (!sameList && pendingIsRemote) {
      // DEMO-Alarm, aber die Liste ist eine andere (z.B. NVS verworfen ->
      // volle config.h-Liste): verwerfen - ein Demo-Alarm darf nie an
      // Empfänger gehen, für die er nicht gedacht war.
      alarmPending    = false;
      pendingIsRemote = false;
      alarmGaveUp     = (deliveryDone < deliveryTotal);
      Serial.println("RTC: Empfaengerliste geaendert - offener DEMO-Alarm verworfen.");
    } else if (!sameList) {
      // Relaisalarm, Liste hat sich geändert (z.B. NVS-Stand verworfen):
      // lieber doppelt als gar nicht - alle der aktuellen Liste bekommen ihn.
      deliveryTotal  = alarmKeyCount;
      deliveryDone   = 0;
      pendingKeyHash = keyListHash();
      Serial.println("RTC: Empfaengerliste seit dem Alarm geaendert - sende an alle erneut.");
    }
    if (alarmPending) {
      // Nachsendung: sofort fällig und als "(verspaetet zugestellt)" markiert.
      firstRoundDone = true;
      lastSendRound  = now - ALARM_RETRY_MS;
      roundStartIdx  = 0;
    }
  }
  // War die Klemm-Warnung vor dem Neustart noch nicht zugestellt: erneut.
  if (stuckWarned && !stuckNoticeDelivered && waitingForRelease) {
    queueNotice(NOTICE_STUCK, "Kontakt klemmt?",
                "Relaiskontakt ist seit dem Alarm dauerhaft geschlossen. "
                "Melder/Lader pruefen - bis dahin sind keine neuen Alarme erkennbar!");
  }
  restoredActiveState = alarmPending || waitingForRelease || cooldownActive;
  Serial.printf("RTC-Alarmzustand uebernommen (Neustart-Grund %s): Alarm offen=%d, "
                "wartet auf Oeffnen=%d, Cooldown=%d, Alarmzaehler=%lu.\n",
                resetReasonToken(), alarmPending ? 1 : 0, waitingForRelease ? 1 : 0,
                cooldownActive ? 1 : 0, (unsigned long)alarmSeq);
  saveRtcState();
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

// Schreibreihenfolge so, dass ein Stromausfall mittendrin nie eine halbe
// Liste unter einer gültigen Version hinterlässt: erst v=0 (= "nicht
// synchronisiert"), dann die Einträge, dann n, ZULETZT die echte Version.
// Bricht es ab, bleibt v=0 -> beim nächsten Start Startliste aus config.h und
// erzwungener Abgleich mit dem Dashboard. Gibt true zurück, wenn alles
// geschrieben wurde.
bool saveAlarmKeys() {
  if (!keyStoreOpen) return false;
  bool ok = keyStore.putULong("v", 0) != 0;
  for (int i = 0; ok && i < alarmKeyCount; i++) {
    ok = keyStore.putString(("k" + String(i)).c_str(), alarmKeys[i]) != 0
      && keyStore.putUChar(("m" + String(i)).c_str(), alarmKeyMuted[i] ? 1 : 0) != 0
      && keyStore.putUChar(("mv" + String(i)).c_str(), alarmKeyMuteVolume[i]) != 0;
  }
  ok = ok && keyStore.putInt("n", alarmKeyCount) != 0;
  ok = ok && keyStore.putULong("v", alarmKeysVersion) != 0;
  return ok;
}

void loadAlarmKeys() {
  keyStoreOpen = keyStore.begin("fwalarm", false);
  if (!keyStoreOpen) {
    Serial.println("WARNUNG: NVS nicht nutzbar - Empfaengerliste wird nicht gespeichert.");
  }
  // Gültige Einträge eines verworfenen NVS-Stands - nur Reserve, falls auch
  // die config.h-Liste leer ist (lieber ein Teil der Empfänger als keiner).
  String  nvsKeys[MAX_ALARM_KEYS];
  bool    nvsMuted[MAX_ALARM_KEYS];
  uint8_t nvsMuteVol[MAX_ALARM_KEYS];
  int     nvsValid = 0;

  int n = keyStoreOpen ? keyStore.getInt("n", -1) : -1;
  if (n != -1) {
    unsigned long v = keyStore.getULong("v", 0);
    bool ok = (n >= 1 && n <= MAX_ALARM_KEYS && v >= 1);
    int scan = (n < 0) ? 0 : (n > MAX_ALARM_KEYS ? MAX_ALARM_KEYS : n);
    for (int i = 0; i < scan; i++) {
      String k = keyStore.getString(("k" + String(i)).c_str(), "");
      // Fehlt das Stumm-Flag (NVS-Stand einer alten Firmware): laut = sicher.
      bool m = keyStore.getUChar(("m" + String(i)).c_str(), 0) != 0;
      // Fehlende Stumm-Lautstärke (alter NVS-Stand): 0 = lautlos, das alte
      // Verhalten - so ändert das Update nichts an bestehenden Stummschaltungen.
      uint8_t mv = keyStore.getUChar(("mv" + String(i)).c_str(), 0);
      if (!validAlarmKey(k)) { ok = false; continue; }
      nvsKeys[nvsValid]    = k;
      nvsMuted[nvsValid]   = m;
      nvsMuteVol[nvsValid] = mv > 10 ? 10 : mv;
      nvsValid++;
    }
    if (ok && nvsValid == n) {
      alarmKeyCount    = n;
      alarmKeysVersion = v;
      for (int i = 0; i < n; i++) {
        alarmKeys[i]          = nvsKeys[i];
        alarmKeyMuted[i]      = nvsMuted[i];
        alarmKeyMuteVolume[i] = nvsMuteVol[i];
      }
      Serial.printf("Empfaengerliste aus NVS geladen: %d Keys (Version %lu, %d stumm).\n",
                    alarmKeyCount, alarmKeysVersion, mutedKeyCount());
      return;
    }
    // Unvollständig/ungültig (z.B. Stromausfall beim Schreiben): Version 0
    // erzwingt beim nächsten Poll einen neuen Abgleich mit dem Dashboard.
    Serial.println("WARNUNG: Empfaengerliste im NVS unvollstaendig/ungueltig - "
                   "nutze config.h-Liste bis zum naechsten Abgleich.");
  }
  // Allererster Boot (noch nie synchronisiert) oder verworfener NVS-Stand:
  // Startliste aus config.h, Version 0.
  alarmKeysVersion = 0;
  alarmKeyCount = 0;
  for (int i = 0; i < BARK_KEYS_ALARM_COUNT && alarmKeyCount < MAX_ALARM_KEYS; i++) {
    if (BARK_KEYS_ALARM[i] != nullptr && strlen(BARK_KEYS_ALARM[i]) > 0) {
      alarmKeyMuted[alarmKeyCount] = false;   // Startliste: immer laut
      alarmKeyMuteVolume[alarmKeyCount] = 0;
      alarmKeys[alarmKeyCount++] = String(BARK_KEYS_ALARM[i]);
    }
  }
  if (alarmKeyCount == 0 && nvsValid > 0) {
    for (int i = 0; i < nvsValid; i++) {
      alarmKeys[i]          = nvsKeys[i];
      alarmKeyMuted[i]      = nvsMuted[i];
      alarmKeyMuteVolume[i] = nvsMuteVol[i];
    }
    alarmKeyCount = nvsValid;
    Serial.printf("config.h-Liste leer - nutze %d gueltige Keys aus dem NVS-Rest.\n", nvsValid);
    return;
  }
  Serial.printf("Empfaengerliste aus config.h uebernommen: %d Keys.\n", alarmKeyCount);
}

// ============================ EINGANG (RELAIS) ==============================

// true, wenn der Eingang pin aktiv ist (= Alarm-Kontakt geschlossen).
bool pinActive(int pin) {
  int v = digitalRead(pin);
  return ACTIVE_LOW ? (v == LOW) : (v == HIGH);
}

// true, wenn (mindestens) ein Eingang aktiv ist (= Alarm-Kontakt geschlossen).
bool relayActive() {
  bool a = pinActive(RELAY_PIN);
#if RELAY_PIN2_ENABLED
  a = a || pinActive(RELAY_PIN2);
#endif
  return a;
}

// Pin-Interrupt: läuft bei JEDER Flanke (schließen UND öffnen). Misst, wie
// lange der Kontakt geschlossen war. War es mindestens DEBOUNCE_MS, wird der
// Alarm eingerastet (isrLatched). So geht ein kurzer Impuls nicht verloren,
// selbst wenn loop() gerade blockiert (z.B. HTTPS-Sendung des Heartbeats).
// Kurze Störspitzen (< DEBOUNCE_MS) rasten dagegen NICHT ein.
// isrLatchedAt merkt sich den Beginn des (ersten) eingerasteten Impulses,
// damit der Alarm-Text den Zeitpunkt der Erkennung trägt, nicht den der
// späteren Verarbeitung.
void IRAM_ATTR relayEdge(int pin, volatile unsigned long* since) {
  int v = digitalRead(pin);
  bool active = ACTIVE_LOW ? (v == LOW) : (v == HIGH);
  unsigned long now = millis();
  portENTER_CRITICAL_ISR(&isrMux);
  if (active) {
    if (*since == 0) *since = now;              // Beginn der Schließung merken
  } else {
    if (*since != 0 && now - *since >= DEBOUNCE_MS) {
      if (!isrLatched) isrLatchedAt = *since;   // erster eingerasteter Impuls zählt
      isrLatched = true;                        // lang genug zu -> einrasten
    }
    *since = 0;
  }
  portEXIT_CRITICAL_ISR(&isrMux);
}

void IRAM_ATTR relayIsr1() { relayEdge(RELAY_PIN, &isrSince1); }
#if RELAY_PIN2_ENABLED
void IRAM_ATTR relayIsr2() { relayEdge(RELAY_PIN2, &isrSince2); }
#endif

// Ist ein Eingang laut Interrupt seit mindestens DEBOUNCE_MS ununterbrochen
// geschlossen (und jetzt noch aktiv)? Der Interrupt sieht jede Flanke - auch
// während loop() blockiert. Damit gilt die Entprellung auch über blockierende
// Aufrufe hinweg, ohne dass zwei Störspitzen mit einer langen Sendung
// dazwischen wie EIN langer Impuls aussehen.
bool isrClosedLongEnough() {
  unsigned long s1 = isrSince1;   // ZUERST lesen, dann die Uhr: t >= s1
  unsigned long t  = millis();
  if (s1 != 0 && t - s1 >= DEBOUNCE_MS && pinActive(RELAY_PIN)) return true;
#if RELAY_PIN2_ENABLED
  unsigned long s2 = isrSince2;
  t = millis();
  if (s2 != 0 && t - s2 >= DEBOUNCE_MS && pinActive(RELAY_PIN2)) return true;
#endif
  return false;
}

// ============================ ALARM-VERSAND =================================

// Lehnt Bark den Key dauerhaft ab? 400/401/403/404 behebt keine Wiederholung
// (falscher/gelöschter Key). Andere 4xx (z.B. 429 "zu viele Anfragen") und
// 5xx bleiben für die nächste Runde offen.
bool isPermanentRejection(int code) {
  return code == 400 || code == 401 || code == 403 || code == 404;
}

// Empfänger, die den offenen Alarm noch bekommen sollen.
int openKeyCount() {
  int open = 0;
  for (int i = 0; i < alarmKeyCount; i++) {
    if (!keyDone[i] && !keyFailed[i]) open++;
  }
  return open;
}

// Abgelehnten Key für die Owner-Warnung sammeln (nur maskiert!).
void noteRejectedKey(const String& key, int code) {
  if (!noticePending[NOTICE_KEY_REJECTED]) rejectedKeysText = "";
  String entry = maskKey(key.c_str()) + " (HTTP " + String(code) + ")";
  if (rejectedKeysText.indexOf(entry) >= 0) return;   // schon in der offenen Warnung
  if (rejectedKeysText.length() > 0) rejectedKeysText += ", ";
  rejectedKeysText += entry;
  queueNotice(NOTICE_KEY_REJECTED, "Alarm-Empfaenger abgelehnt",
              "Bark lehnt Alarm-Empfaenger ab: " + rejectedKeysText
              + ". Sie bekommen diesen Alarm NICHT - Key in der Dashboard-Liste/config.h pruefen!");
}

// Nachsende-Frist abgelaufen: aufgeben und den Owner warnen (mit Wiederholung).
void giveUpAlarm() {
  alarmPending    = false;
  pendingIsRemote = false;
  alarmGaveUp     = true;
  Serial.println("Alarm-Nachsenden AUFGEGEBEN (Zeitlimit erreicht).");
  String when = pendingStamp;
  when.trim();
  String text = "Ein Alarm konnte nicht an alle Empfaenger zugestellt werden! Erreicht: "
              + String(deliveryDone) + " von " + String(deliveryTotal);
  if (when.length() > 0) text += ", erkannt " + when;
  text += ".";
  queueNotice(NOTICE_GAVEUP, "Alarm-Zustellung unvollstaendig", text);
  saveRtcState();
  requestRemoteStatusPush();
}

// Alarm ohne einen einzigen Empfänger: NICHT als "zugestellt" werten.
void failNoRecipients() {
  alarmPending    = false;
  pendingIsRemote = false;
  alarmGaveUp     = true;
  Serial.println("ALARM, aber KEIN Alarm-Empfaenger in der Liste - niemand wurde alarmiert!");
  queueNotice(NOTICE_NO_KEYS, "ALARM ohne Empfaenger",
              "Ein Alarm wurde erkannt, aber die Empfaengerliste der Box ist LEER - "
              "niemand wurde alarmiert! Dashboard-Liste/config.h pruefen.");
  saveRtcState();
  requestRemoteStatusPush();
}

// Sendet den offenen Alarm an alle Empfänger, die ihn noch NICHT bekommen
// haben. Jeder Key wird einzeln behandelt - ein fehlerhafter Key blockiert die
// anderen NICHT. Wird aus loop() wiederholt aufgerufen, bis alle versorgt sind:
// War WLAN/Internet im Alarm-Moment weg, geht der Alarm NICHT verloren, sondern
// wird alle ALARM_RETRY_MS nachgesendet (max. bis ALARM_RETRY_GIVEUP_MS).
void processPendingAlarm() {
  if (!alarmPending) return;

  unsigned long now = millis();
  if (firstRoundDone) {
    // Zwischen zwei Nachsende-Runden warten (nicht blockierend).
    if (now - lastSendRound < ALARM_RETRY_MS) return;
    // Irgendwann aufgeben - eine Stunden alte Alarm-Meldung hilft niemandem.
    if (now - pendingSince >= ALARM_RETRY_GIVEUP_MS) {
      giveUpAlarm();
      return;
    }
  }
  if (alarmKeyCount == 0) {   // z.B. nach Neustart mit verlorener Liste
    failNoRecipients();
    return;
  }

  lastSendRound = now;
  bool isRetry = firstRoundDone;
  firstRoundDone = true;
  saveRtcState();

  if (!connectWiFi(15000)) {
    Serial.println("Kein WLAN - Alarm wird nachgesendet, sobald Verbindung da ist.");
    return;
  }

  // Zeitstempel im Text ist der der ERKENNUNG - bei Nachsendung zusätzlich
  // kennzeichnen, damit der Empfänger die Verzögerung erkennt.
  String body = pendingBody;
  if (isRetry) body += " (verspaetet zugestellt)";

  int  ok = 0, rejectedNow = 0, transportFailsInRow = 0;
  bool aborted = false, deadlineHit = false, sentOne = false;
  int  count = alarmKeyCount;
  int  start = (roundStartIdx > 0 && roundStartIdx < count) ? roundStartIdx : 0;
  roundStartIdx = 0;
  for (int n = 0; n < count; n++) {
    int i = (start + n) % count;
    if (keyDone[i] || keyFailed[i]) continue;   // hat ihn schon / aufgegeben
    // Frist auch MITTEN in der Runde prüfen (eine Runde kann bei Netzfehlern
    // Minuten dauern).
    if (millis() - pendingSince >= ALARM_RETRY_GIVEUP_MS) {
      deadlineHit = true;
      break;
    }
    if (sentOne) delay(150);   // kurze Pause zwischen den Empfängern
    sentOne = true;
    feedWatchdog();
    // Arbeitsmodus: gleicher Critical Alert, aber mit der im Dashboard
    // eingestellten Stumm-Lautstärke (0 = lautlos; durchbricht trotzdem
    // Stummschalter/Fokus) und ohne call=1 (das wuerde den Ton wiederholen).
    int code = 0;
    bool good = alarmKeyMuted[i]
      ? sendBark(alarmKeys[i].c_str(), ALARM_TITLE, body,
                 "critical", ALARM_SOUND, alarmKeyMuteVolume[i], false, &code)
      : sendBark(alarmKeys[i].c_str(), ALARM_TITLE, body,
                 "critical", ALARM_SOUND, pendingVolume, ALARM_CALL, &code);
    feedWatchdog();
    if (good) {
      keyDone[i] = true;
      deliveryDone++;
      ok++;
      transportFailsInRow = 0;
      saveRtcState();
      continue;
    }
    if (isPermanentRejection(code)) {
      // Dauerhaft abgelehnt: keine weiteren Nachsende-Runden für diesen Key,
      // Owner warnen - die übrigen Keys laufen normal weiter.
      keyFailed[i] = true;
      rejectedNow++;
      transportFailsInRow = 0;
      noteRejectedKey(alarmKeys[i], code);
      saveRtcState();
      continue;
    }
    if (code < 0) {
      // Transportfehler (kein WLAN/DNS/TLS/Timeout) betreffen nicht den Key,
      // sondern die Verbindung. Zwei in Folge -> Runde abbrechen, statt jeden
      // weiteren Key minutenlang durch die Timeouts zu schicken. Die nächste
      // Runde (nach ALARM_RETRY_MS) beginnt beim nächsten, hier nicht mehr
      // versuchten Key.
      if (++transportFailsInRow >= 2) {
        aborted = true;
        roundStartIdx = (i + 1) % count;
        break;
      }
    } else {
      transportFailsInRow = 0;   // 5xx/andere 4xx: Server erreichbar, pro Key isoliert
    }
  }
  int open = openKeyCount();
  Serial.printf("Alarm-Runde: %d neu zugestellt, %d abgelehnt, %d noch offen%s.\n",
                ok, rejectedNow, open,
                aborted ? " (Runde nach Netzfehlern abgebrochen)" : "");

  if (open == 0) {
    alarmPending    = false;
    pendingIsRemote = false;
    // Dauerhaft abgelehnte Keys: Die Box sendet nicht mehr nach, aber nicht
    // alle wurden erreicht.
    alarmGaveUp = (deliveryDone < deliveryTotal);
    Serial.println(alarmGaveUp
                   ? "Alarm-Zustellung beendet - abgelehnte Empfaenger NICHT erreicht."
                   : "Alarm an alle Empfaenger zugestellt.");
  } else if (deadlineHit) {
    giveUpAlarm();
  }
  saveRtcState();
  requestRemoteStatusPush();

  // Abgelehnte Keys sofort melden (EIN Versuch direkt nach der Runde - die
  // übrigen Empfänger sind in dieser Runde schon bedient). Scheitert er,
  // wiederholt processOwnerNotices() ihn, sobald kein Alarm mehr offen ist.
  if (rejectedNow > 0) trySendNotice(NOTICE_KEY_REJECTED);
}

// Neuer Alarm erkannt: Zustand setzen und sofort die erste Sende-Runde starten.
// remote = DEMO-ALARM-Befehl des Dashboards: gleiche Zustellung (Nachsende-
// Puffer, keyDone[]), aber OHNE Cooldown/Wiederbewaffnung - sonst würde ein
// echter Relaisalarm in den nächsten COOLDOWN_MS verworfen.
// stamp/volume wurden bei der ERKENNUNG festgelegt (alarmTimingFor()).
void startAlarm(bool remote, const String& stamp, int volume) {
  Serial.println(remote
                 ? ">>> ALARM (Dashboard-Befehl) - sende Bark Critical Alert <<<"
                 : ">>> ALARM erkannt - sende Bark Critical Alert an alle <<<");

  alarmSeq++;                  // jeder Alarm zählt, auch ein textgleicher
  alarmSource     = remote ? 2 : 1;
  alarmPending    = true;
  pendingIsRemote = remote;
  pendingStamp    = stamp;
  pendingBody     = stamp + ALARM_BODY;   // Zeitpunkt der Erkennung
  lastAlarmInfo   = pendingBody;
  pendingSince    = millis();
  // Lautstärke einmal bei der ERKENNUNG festlegen, nicht pro Sende-Runde: Ein
  // Alarm um 19:04, der erst um 19:20 nachgesendet wird, bleibt ein Probealarm.
  pendingVolume   = volume;
  if (pendingVolume != ALARM_VOLUME) {
    Serial.printf("Probealarm-Fenster: Lautstaerke %d statt %d.\n",
                  pendingVolume, ALARM_VOLUME);
  }
  firstRoundDone  = false;
  roundStartIdx   = 0;
  alarmGaveUp     = false;
  deliveryTotal   = alarmKeyCount;
  deliveryDone    = 0;
  for (int i = 0; i < MAX_ALARM_KEYS; i++) {
    keyDone[i]   = false;
    keyFailed[i] = false;
  }
  pendingKeyHash = keyListHash();

  if (!remote) {
    lastAlarm            = millis();
    cooldownActive       = true;
    waitingForRelease    = true;   // neuer Alarm erst, wenn Kontakt wieder offen war
    openSince            = 0;
    stuckWarned          = false;
    stuckNoticeDelivered = false;
    cancelNotice(NOTICE_STUCK);
    cancelNotice(NOTICE_RELEASED);
    activeSince          = 0;
  }

  if (alarmKeyCount == 0) {
    failNoRecipients();
    return;
  }
  saveRtcState();              // VOR dem Senden: ein Absturz darin verliert nichts
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

// Sperrt NEUE Relaisalarme (Wiederbewaffnung bzw. Cooldown nach einem
// Relaisalarm). Ein DEMO-Alarm setzt beides nicht.
bool alarmBlockedNow() {
  return waitingForRelease || cooldownActive;
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

  feedWatchdog();
  WiFiClientSecure client;
  client.setInsecure();  // wie Bark: robust gegen CA-/Uhrzeit-Probleme auf ESP32
  // Handshake-Limit wie die übrigen NAS-Timeouts (in Sekunden, mind. 4 s) -
  // sonst hinge ein halb erreichbares NAS bis zu 120 s im Handshake.
  client.setHandshakeTimeout(REMOTE_TLS_TIMEOUT_S);
  HTTPClient http;
  http.setConnectTimeout(REMOTE_HTTP_TIMEOUT_MS);
  http.setTimeout(REMOTE_HTTP_TIMEOUT_MS);
  if (!http.begin(client, remoteEndpoint(endpoint))) {
    return false;
  }
  http.addHeader("Content-Type", "application/x-www-form-urlencoded");

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

// "erreicht/gesamt" der letzten Zustellung - mit den beim Alarm
// festgehaltenen Zahlen, damit ein späterer Listen-Sync die Anzeige nicht
// verschiebt. Vor dem ersten Alarm wie bisher "0/<Listengröße>".
String keyProgressText() {
  if (alarmSource == 0) return "0/" + String(alarmKeyCount);
  return String(deliveryDone) + "/" + String(deliveryTotal);
}

// Empfängerliste vom NAS verworfen: eigener Backoff (sonst zöge jeder Poll
// einen zweiten TLS-Request nach sich) und EINE Warnung an den Owner, bis
// wieder eine Liste angenommen wurde.
void rejectKeySync(const char* reason) {
  keySyncRejectedAt = millis();
  if (keySyncRejectedAt == 0) keySyncRejectedAt = 1;
  Serial.printf("Key-Sync verworfen: %s\n", reason);
  if (!keySyncRejectNotified) {
    keySyncRejectNotified = true;
    queueNotice(NOTICE_SYNC_REJECTED, "Empfaengerliste verworfen",
                String("Die Box hat die Empfaengerliste vom NAS verworfen (") + reason
                + "). Sie alarmiert weiter mit ihrer bisherigen Liste ("
                + String(alarmKeyCount) + " Keys, Version " + String(alarmKeysVersion)
                + "). Dashboard/NAS pruefen.");
  }
}

// Holt die komplette Empfängerliste vom NAS (api/keys.php) und übernimmt sie
// nur, wenn sie plausibel ist. Wird aufgerufen, sobald die im Poll gemeldete
// keys_version von der lokalen abweicht. Bewusst NIE während eines offenen
// RELAIS-Alarms - mitten in einer Zustellung wird die Liste nicht
// ausgetauscht. Ein offener DEMO-Alarm hält den Abgleich dagegen nicht auf
// (z.B. Demo -> LIVE); ändert sich dabei die Empfängerliste, wird er
// verworfen, weil seine keyDone[]-Zuordnung zur alten Liste gehört.
void syncAlarmKeys() {
  if (alarmPending && !pendingIsRemote) return;
  if (keySyncRejectedAt != 0 && millis() - keySyncRejectedAt < KEY_SYNC_REJECT_BACKOFF_MS) return;

  String nonce = String(millis(), HEX) + "-k" + String(alarmKeysVersion, HEX);
  String form = remoteBaseForm() + "&nonce=" + urlEncode(nonce);
  String response;
  if (!remotePostForm("keys.php", form, &response)) {
    lastRemoteError = millis();
    return;
  }
  if (lineValue(response, "ok") != "1") {
    rejectKeySync("Antwort ohne ok=1");
    return;
  }
  if (lineValue(response, "nonce") != nonce) {
    rejectKeySync("falsche Nonce");
    return;
  }

  long versionValue = lineValue(response, "version").toInt();
  if (versionValue < 1) {
    // Version 0 hieße "keine Liste gepflegt" - die würde der Poll gar nicht
    // anfordern; eine fehlende/kaputte Zeile ebenso wenig übernehmen.
    rejectKeySync("Version fehlt");
    return;
  }
  unsigned long version = (unsigned long)versionValue;
  int count = lineValue(response, "count").toInt();
  if (count < 1 || count > MAX_ALARM_KEYS) {
    rejectKeySync("unplausible Empfaenger-Anzahl");
    return;
  }

  String fresh[MAX_ALARM_KEYS];
  bool freshMuted[MAX_ALARM_KEYS];
  uint8_t freshMuteVol[MAX_ALARM_KEYS];
  for (int i = 0; i < count; i++) {
    fresh[i] = lineValue(response, ("key" + String(i)).c_str());
    if (!validAlarmKey(fresh[i])) {
      rejectKeySync("ungueltiger Key in der Antwort");
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

  if (alarmPending && pendingIsRemote && keySetChanged) {
    // Sonst ginge der Rest eines Demo-Alarms an die NEUE (z.B. volle) Liste.
    alarmPending    = false;
    pendingIsRemote = false;
    alarmGaveUp     = (deliveryDone < deliveryTotal);
    Serial.println("Key-Sync: offener DEMO-Alarm verworfen (Empfaengerliste geaendert).");
  }

  for (int i = 0; i < count; i++) {
    alarmKeys[i]           = fresh[i];
    alarmKeyMuted[i]       = freshMuted[i];
    alarmKeyMuteVolume[i]  = freshMuteVol[i];
  }
  alarmKeyCount    = count;
  alarmKeysVersion = version;
  if (!saveAlarmKeys()) {
    // Im RAM gilt die neue Liste trotzdem; nach einem Neustart erzwingt der
    // unvollständige NVS-Stand (v=0) einen neuen Abgleich.
    Serial.println("WARNUNG: Empfaengerliste konnte nicht (vollstaendig) im NVS gespeichert werden.");
  }
  keySyncRejectedAt     = 0;
  keySyncRejectNotified = false;
  cancelNotice(NOTICE_SYNC_REJECTED);
  // Die Boot-Warnung "keine Empfänger" ist mit einer Liste erledigt (die
  // Warnung über einen Alarm OHNE Empfänger bleibt dagegen offen).
  if (noticePending[NOTICE_NO_KEYS] && noticeTitle[NOTICE_NO_KEYS] == NO_KEYS_BOOT_TITLE) {
    cancelNotice(NOTICE_NO_KEYS);
  }
  saveRtcState();
  Serial.printf("Empfaengerliste synchronisiert: %d Keys (Version %lu, %d stumm).\n",
                count, version, mutedKeyCount());
  if (keySetChanged) {
    // Leise Bestätigung an den Owner: Änderungen an der Alarmierungs-Liste
    // selbst sollen nicht unbemerkt passieren können. Mit Wiederholung; eine
    // spätere Übernahme ersetzt eine noch nicht zugestellte Meldung.
    queueNotice(NOTICE_KEYS_UPDATED, "Empfaengerliste aktualisiert",
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
  // Eigener Backoff: Ein scheiternder Poll/ACK legt den Status-Push (das
  // Lebenszeichen fürs Dashboard und den Offline-Wächter) nicht mehr lahm.
  if (lastStatusError != 0 && now - lastStatusError < REMOTE_ERROR_BACKOFF_MS) return;

  lastRemoteStatusAttempt = now;

  String form = remoteBaseForm()
    + "&fw_build=" + urlEncode(FW_BUILD_INFO)
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
    + "&cooldown=" + boolField(cooldownActive)
    + "&waiting_for_release=" + boolField(waitingForRelease)
    + "&stuck_warned=" + boolField(stuckWarned)
    + "&ack_pending=" + boolField(remoteAckPending)
    // Zusätzliche Diagnose-Felder (das NAS wertet sie aus, wenn vorhanden):
    + "&core=" + urlEncode(CORE_VERSION)
    + "&alarm_seq=" + String((unsigned long)alarmSeq)
    + "&max_alloc_heap=" + String((unsigned long)ESP.getMaxAllocHeap())
    + "&alarm_gaveup=" + boolField(alarmGaveUp);
  // Quelle des letzten Alarms - erst, wenn es einen gab.
  if (alarmSource != 0) {
    form += "&alarm_source=";
    form += (alarmSource == 2) ? "remote" : "relay";
  }

  if (remotePostForm("status.php", form)) {
    lastRemoteStatusPush = now;
    remoteStatusDirty = false;
    lastStatusError = 0;
  } else {
    lastStatusError = now;
  }
}

void queueRemoteAck(unsigned long id, const String& result, const String& message) {
  remoteAckPending = true;
  remoteAckId = id;
  remoteAckResult = result;
  remoteAckMessage = message;
  remoteAckFails = 0;
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
    remoteAckFails = 0;
    lastRemoteError = 0;
    requestRemoteStatusPush();
  } else {
    lastRemoteError = now;
    // Eine dauerhaft scheiternde ACK legte früher Poll und Key-Sync für immer
    // still (gepollt wird erst nach der ACK). Nach REMOTE_ACK_MAX_TRIES wird
    // sie verworfen - das Dashboard zeigt dann ehrlich "ausgeliefert,
    // Bestätigung fehlt". lastRemoteCommandId bleibt gemerkt: kein Replay.
    if (++remoteAckFails >= REMOTE_ACK_MAX_TRIES) {
      Serial.printf("ACK fuer NAS-Befehl #%lu nach %d Fehlversuchen verworfen.\n",
                    remoteAckId, remoteAckFails);
      remoteAckPending = false;
      remoteAckFails = 0;
      requestRemoteStatusPush();
    }
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

  // ALARM = Demo-Werkzeug des Dashboards ("DEMO-ALARM über die Box", nur im
  // Demo-Modus, serverseitig gesichert): testet die komplette Kette Poll ->
  // Zustellung -> Bark. Der manuelle REAL ALARM läuft dagegen DIREKT vom NAS
  // zu Bark. Der Demo-Alarm nutzt dieselbe Zustellung wie ein Relaisalarm
  // (Nachsende-Puffer + keyDone[]), setzt aber KEINEN Cooldown und keine
  // Wiederbewaffnung - ein echter Relaisalarm danach muss sofort durchgehen
  // und hat während der Demo-Zustellung Vorrang.
  if (type == "ALARM") {
    if (alarmPending) {
      queueRemoteAck(id, "skipped_busy", pendingIsRemote
                     ? "demo alarm delivery still in progress"
                     : "relay alarm delivery in progress");
      return;
    }
    String stamp;
    int volume = ALARM_VOLUME;
    alarmTimingFor(0, stamp, volume);
    bool haveRecipients = alarmKeyCount > 0;
    startAlarm(true, stamp, volume);
    queueRemoteAck(id, haveRecipients ? "executed" : "no_recipients",
                   haveRecipients ? "remote alarm started (no cooldown)"
                                  : "no alarm recipients on the box");
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

// Kleiner Check beim Start: Pins, Key-Anzahl, WLAN.
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

// ============================ RELAIS-LOGIK ==================================

// Wiederbewaffnung, Klemm-Prüfung, Cooldown-Gate und Erkennung. Läuft in
// jedem loop()-Durchlauf als ERSTES nach der WLAN-Pflege - vor Heartbeat und
// NAS-Anfragen, damit ein Alarm nie hinter Nebenlogik wartet.
void handleRelayInput() {
  unsigned long now = millis();
  bool active = relayActive();

  // Cooldown abgelaufen? (Merker statt Zeitvergleich, siehe cooldownActive.)
  if (cooldownActive && now - lastAlarm >= COOLDOWN_MS) {
    cooldownActive = false;
    saveRtcState();
    requestRemoteStatusPush();
  }

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
        // Klemm-Warnung zurücksetzen (früher blieb stuckWarned bis zum
        // nächsten Alarm stehen). Wurde sie zugestellt, leise Entwarnung;
        // war sie noch nicht raus, ist sie jetzt hinfällig.
        if (stuckWarned) {
          if (stuckNoticeDelivered) {
            queueNotice(NOTICE_RELEASED, "Kontakt wieder offen",
                        "Relaiskontakt ist wieder offen - Alarm-Erkennung wieder scharf.");
          }
          cancelNotice(NOTICE_STUCK);
        }
        stuckWarned = false;
        stuckNoticeDelivered = false;
        saveRtcState();
        requestRemoteStatusPush();
      }
    } else {
      openSince = 0;
      // Kontakt klemmt? Solange er zu ist, sind KEINE neuen Alarme erkennbar.
      // Leise warnen (mit Wiederholung, bis zugestellt), damit du am Lader
      // nachschauen kannst.
      if (!stuckWarned && now - lastAlarm >= STUCK_CONTACT_WARN_MS) {
        stuckWarned = true;
        Serial.println("WARNUNG: Kontakt dauerhaft geschlossen (klemmt?).");
        cancelNotice(NOTICE_RELEASED);
        queueNotice(NOTICE_STUCK, "Kontakt klemmt?",
                    "Relaiskontakt ist seit dem Alarm dauerhaft geschlossen. "
                    "Melder/Lader pruefen - bis dahin sind keine neuen Alarme erkennbar!");
        saveRtcState();
        requestRemoteStatusPush();
      }
    }
  }

  // Vom Interrupt eingerasteten Impuls abholen (Merker dabei zurücksetzen).
  portENTER_CRITICAL(&isrMux);
  bool latched = isrLatched;
  unsigned long latchedAt = isrLatchedAt;
  isrLatched = false;
  portEXIT_CRITICAL(&isrMux);

  // Lag seit der letzten Probe eine blockierende Anfrage? Dann belegt die
  // Pegel-Abfrage NICHT, dass der Kontakt dazwischen zu war (zwei Störspitzen
  // vor/nach einem 1-4-s-NAS-Request sähen sonst wie ein langer Impuls aus).
  bool sampleGap = (lastLevelSample != 0 && now - lastLevelSample > LEVEL_SAMPLE_GAP_MS);
  lastLevelSample = now;

  if (alarmBlockedNow()) {
    // Empfänger wurden gerade erst alarmiert: neue Auslösungen (auch ein
    // eingerasteter Impuls) werden in dieser Phase bewusst verworfen.
    activeSince = 0;
    return;
  }

  // DEBOUNCE_MS gilt in beiden Pfaden: Pegel-Abfrage (eigene, lückenlose
  // Proben ODER laut Interrupt ununterbrochen geschlossen) und ISR-Latch.
  bool levelTrigger = false;
  if (active) {
    if (activeSince == 0 || sampleGap) activeSince = now;   // Beginn (neu) merken
    if (now - activeSince >= DEBOUNCE_MS || isrClosedLongEnough()) levelTrigger = true;
  } else {
    activeSince = 0;                                         // Kontakt offen -> Reset
  }
  if (!latched && !levelTrigger) return;

  // Ein eingerasteter Impuls (kam während einer Blockade) trägt den Zeitpunkt
  // seines Beginns - nicht den der Verarbeitung nach der Sendung.
  unsigned long msAgo = latched ? (millis() - latchedAt) : 0;
  if (alarmPending && pendingIsRemote) {
    Serial.println("Relaisalarm hat Vorrang - offener DEMO-Alarm wird verworfen.");
  }
  String stamp;
  int volume = ALARM_VOLUME;
  alarmTimingFor(msAgo, stamp, volume);
  startAlarm(false, stamp, volume);
}

// ============================ SETUP / LOOP =================================

// Start-Meldung leise nur an dich - erst NACH der Startphase und nie vor
// einer offenen Zustellung, damit ein Alarm nach einem Neustart nicht hinter
// ihr wartet.
void sendStartMessage() {
  startMessageDone = true;
  String body = "System gestartet und ueberwacht den Relaiskontakt.";
  if (restoredActiveState) {
    body += " Neustart (" + String(resetReasonToken()) + "): Alarm-Zustand wurde uebernommen.";
  }
  bool startMsgOk = sendStatus("Alarm-Waechter online", body);
#if HEARTBEAT_ENABLED && NTP_ENABLED
  // Start nach der Heartbeat-Stunde: Die (erfolgreiche) Start-Meldung zählt als
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

void setup() {
  Serial.begin(115200);
  delay(300);
  Serial.println("\nBOSS-925 Alarm-Wächter startet...");
  Serial.printf("Firmware %s, Arduino-ESP32-Core %s, Neustart-Grund: %s\n",
                FW_BUILD_INFO, CORE_VERSION, resetReasonToken());

  // Eingänge konfigurieren. ACTIVE_LOW (Kontakt gegen GND): interner Pull-up,
  // LOW = Alarm. ACTIVE_LOW false (Kontakt gegen 3,3 V): interner Pull-down,
  // HIGH = Alarm - mit Pull-up läge der Pin dann dauerhaft auf HIGH (= Dauer-
  // alarm). GPIO27/25/26/32/33 können beides, GPIO34-39 keins von beiden.
  // Zusätzlich Pin-Interrupts: fangen auch Impulse, während loop() blockiert.
  // Ist der Kontakt SCHON beim Start geschlossen, Messung sofort beginnen -
  // so rastet auch ein Impuls ein, der noch während setup() endet.
  pinMode(RELAY_PIN, ACTIVE_LOW ? INPUT_PULLUP : INPUT_PULLDOWN);
  if (pinActive(RELAY_PIN)) isrSince1 = millis();
  attachInterrupt(digitalPinToInterrupt(RELAY_PIN), relayIsr1, CHANGE);
#if RELAY_PIN2_ENABLED
  pinMode(RELAY_PIN2, ACTIVE_LOW ? INPUT_PULLUP : INPUT_PULLDOWN);
  if (pinActive(RELAY_PIN2)) isrSince2 = millis();
  attachInterrupt(digitalPinToInterrupt(RELAY_PIN2), relayIsr2, CHANGE);
#endif

  loadAlarmKeys();   // Empfängerliste: NVS (Dashboard-Stand) oder config.h-Start
  restoreRtcState(); // offener Alarm/Wiederbewaffnung nach Watchdog-/Absturz-Neustart
  wdtActive = setupWatchdog();
  connectWiFi(15000);
  lastWifiConnected = (WiFi.status() == WL_CONNECTED);
  setupTime();
  selfTest();

  if (alarmKeyCount == 0) {
    queueNotice(NOTICE_NO_KEYS, NO_KEYS_BOOT_TITLE,
                "Die Empfaengerliste der Box ist LEER - ein Alarm erreicht niemanden! "
                "Dashboard-Liste/config.h pruefen.");
  }
#if WDT_ENABLED
  if (!wdtActive) {
    queueNotice(NOTICE_WDT, "Watchdog nicht aktiv",
                "Der Hardware-Watchdog liess sich nicht einrichten - haengt die "
                "Firmware, startet die Box NICHT von selbst neu.");
  }
#endif

  lastHeartbeatMs = millis();
  setupDoneMs = millis();
  requestRemoteStatusPush();
}

void loop() {
  feedWatchdog();
  maintainWiFi();          // nicht-blockierend
  handleRelayInput();      // Erkennung zuerst (startet ggf. sofort die 1. Runde)
  processPendingAlarm();   // offene Alarm-Zustellungen nachholen

  // Laufende Fristen (Cooldown, Nachsende-Frist) regelmäßig sichern.
  if ((alarmPending || waitingForRelease || cooldownActive)
      && millis() - lastRtcSave >= RTC_SAVE_INTERVAL_MS) {
    saveRtcState();
  }

  // Startphase: erst die Erkennung eine Entprell-Zeit lang laufen lassen,
  // bevor Start-Meldung und NAS-Requests (beide blockierend) dran sind.
  if (!startupDone) {
    if (millis() - setupDoneMs < STARTUP_DETECT_MS) {
      delay(20);
      return;
    }
    startupDone = true;
  }
  if (!startMessageDone && !alarmPending) sendStartMessage();

  handleHeartbeat();
  handleRemoteMonitor();   // NAS ACK/Poll/Status (kurze Timeouts + Backoff)
  processOwnerNotices();   // offene Warnungen an dich (mit Wiederholung)

  delay(20);
}
