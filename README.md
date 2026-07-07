# BOSS-925 Alarm-Wächter → Bark (iOS Critical Alerts)

Ein kleines ESP32-Projekt, das den **potenzialfreien Relaiskontakt eines
Swissphone-Ladegeräts** überwacht. Steckt ein **BOSS 925** im Ladegerät und
empfängt eine Alarmierung, schließt der Kontakt für einige Sekunden. Der ESP32
erkennt das und schickt über den Dienst **Bark** einen **lauten Critical Alert**
auf die iPhones aller Empfänger – **auch wenn das Handy stumm ist oder „Nicht
stören" aktiv ist.**

> ⚠️ **WICHTIG – nur ZUSATZ, kein Ersatz!**
> Diese Box ist eine **zusätzliche** Alarmierung. Sie ersetzt **NICHT** den
> Funkmelder. Verlasse dich für die Einsatzalarmierung immer auf den offiziellen
> Melder. Diese Lösung kann ausfallen (WLAN weg, Strom weg, Internet weg,
> Bark-Server-Störung, iPhone-Einstellung falsch). Sie ist ein nettes
> „Sicherheitsnetz", mehr nicht.

---

## Inhaltsverzeichnis
1. [Wie funktioniert das Ganze? (in einfachen Worten)](#1-wie-funktioniert-das-ganze)
2. [Teileliste (was du kaufen musst)](#2-teileliste)
3. [Verkabelung](#3-verkabelung)
4. [Bark auf dem iPhone einrichten](#4-bark-auf-dem-iphone-einrichten)
5. [Software vorbereiten: `config.h`](#5-software-vorbereiten-configh)
6. [Flashen – Variante A: Arduino IDE (einfachster Einstieg)](#6-flashen--variante-a-arduino-ide)
7. [Flashen – Variante B: PlatformIO (Kommandozeile)](#7-flashen--variante-b-platformio)
8. [Testen](#8-testen)
9. [Was die Einstellungen bedeuten](#9-was-die-einstellungen-bedeuten)
10. [Heartbeat: der „ich lebe noch"-Ping](#10-heartbeat-der-ich-lebe-noch-ping)
11. [NAS-Dashboard: Fernüberwachung und manueller Test](#11-nas-dashboard-fernüberwachung-und-manueller-test)
12. [Troubleshooting (wenn etwas nicht klappt)](#12-troubleshooting)
13. [Sicherheit & offene Punkte (TODO)](#13-sicherheit--offene-punkte-todo)

---

## 1. Wie funktioniert das Ganze?

Stell dir eine Kette aus vier Gliedern vor:

```
 [BOSS-925 im Lader]  -->  [Relaiskontakt im Lader]  -->  [ESP32]  -->  [Bark / iPhones]
   empfängt Alarm          schließt 2 Drähte           merkt das      lauter Alarm-Ton
```

1. **Der Melder** (BOSS 925) steckt im Ladegerät und empfängt wie gewohnt seinen
   Funkalarm.
2. **Das Ladegerät** hat einen eingebauten **Relaiskontakt**. Das ist im Grunde
   ein **Schalter**, der bei Alarm kurz **zwei Drähte verbindet** (er „schließt").
   „Potenzialfrei" / „trockener Kontakt" heißt: Da kommt **kein Strom** raus, es
   ist nur ein Schalter ohne eigene Spannung – ungefährlich für den ESP32.
3. **Der ESP32** (ein kleiner WLAN-Mikrocontroller) hat einen seiner Pins so
   eingestellt, dass er merkt, wenn dieser Schalter schließt.
4. Sobald der Kontakt **mindestens 0,3 Sekunden** geschlossen ist, schickt der
   ESP32 über WLAN/Internet eine Nachricht an den **Bark-Dienst**, und der löst
   auf den iPhones den lauten Alarm aus.

Mehr „Magie" steckt nicht dahinter. Es wird **kein Funk dekodiert** – der ESP32
„hört" nur, ob der Schalter im Lader auf oder zu ist.

Optional gibt es zusätzlich ein kleines **NAS-Dashboard**. Die Box bleibt dabei
weiterhin **outbound-only**: Sie pusht Status zum NAS und pollt dort auf manuelle
Befehle. Das Dashboard ist nur Überwachung und manueller Test/Trigger; der
eigentliche Bark-Alarmweg funktioniert unabhängig davon weiter.

---

## 2. Teileliste

| Teil | Beschreibung | ca. Preis |
|------|--------------|-----------|
| **ESP32 DevKit (WROOM-32)** | Der Mikrocontroller mit WLAN. „DevKit" = fertige Platine mit USB-Buchse. | 6–12 € |
| **USB-Kabel + 5 V/1 A Netzteil** | Stromversorgung (z. B. altes Handy-Ladegerät). | vorhanden / 5 € |
| **5-poliger DIN-Stecker, 180°, männlich** | Passt in die DIN-Buchse des Swissphone-Laders (LGRA-Familie, z. B. beim BOSS-925). Bestellbegriff: „DIN-Stecker 5-polig 180° Lötversion". | 1–3 € |
| **2 Dupont-/Litzendrähte** | Verbinden DIN-Stecker mit dem ESP32. | Cent-Beträge |
| optional: kleines Gehäuse | Damit alles geschützt ist. | 5–10 € |

> Du brauchst **keinen** Lötkolben zwingend, aber ein klein wenig Löten am
> DIN-Stecker macht die Verbindung zuverlässiger. Wer gar nicht löten will, nimmt
> einen **5-poligen DIN-Stecker mit Schraubklemmen** (~5 €, Bestellbegriff
> „DIN-Stecker 5-polig Schraubklemme") und klemmt die Drähte nur fest.
>
> ⚠️ **Anderer Lader?** Der **5-polige DIN** gilt für die **LGRA-Familie** (LGRA /
> LGRA-Expert / LGRA Pro), in der der BOSS-925 steckt. **Neuere s.QUAD-Lader** nutzen
> dagegen einen **6-poligen Mini-DIN** (klein, wie ein alter PS/2-Stecker) – dann
> brauchst du den passenden 6-pol-Mini-DIN-Stecker. Der Relaiskontakt liegt aber auch
> dort auf **Pin 1 + Pin 3**, die Verdrahtung unten bleibt also gleich.

---

## 3. Verkabelung

Der Lader (Swissphone **LGRA-Familie**, in der der BOSS-925 steckt) gibt den
Relaiskontakt an der **5-poligen DIN-Buchse** auf **Pin 1 und Pin 3** aus. Diese
beiden Pins werden bei Alarm intern verbunden. Der Kontakt schließt, sobald
**Melder im Lader steckt + Netzstecker dran + Alarm empfangen** – je nach
Einstellung für ca. 10 s oder bis zur Quittierung am Melder.

**So verdrahtest du es:**

| DIN-Pin (Lader) | geht an ESP32 |
|-----------------|---------------|
| **Pin 1** | **GND** (Minus / Masse) |
| **Pin 3** | **GPIO27** |

```
   Swissphone-Lader (DIN-Buchse)             ESP32 DevKit
   ┌───────────────────────────┐
   │  Pin 1  ──────────────────┼──────────►  GND
   │  Pin 3  ──────────────────┼──────────►  GPIO27
   └───────────────────────────┘
        (potenzialfreier Kontakt,
         schließt bei Alarm)
```

> 🔧 **Welche zwei Stifte nimmst du?** Welcher der beiden „Pin 1" und welcher
> „Pin 3" heißt, ist **egal** – es ist ein potenzialfreier Relaiskontakt, kein Plus/
> Minus. Du musst nur die **richtigen zwei von fünf** Stiften erwischen, denn an der
> Buchse liegen auch **Pin 2 = Masse** und **Pin 4 = Versorgungsspannung** (da darf
> der ESP32 **nicht** dran!). **Sicherer Weg:** Multimeter auf
> **Durchgangsprüfung (Piepton)**, am Lader einen **Probealarm** auslösen – die zwei
> Pins, zwischen denen es genau dann piept, sind dein Relaiskontakt. Diese beiden an
> GND und GPIO27 (Reihenfolge beliebig) – fertig.

**Warum funktioniert das ohne weitere Bauteile?**
Der ESP32-Pin ist im Programm als `INPUT_PULLUP` eingestellt. Das bedeutet: Ein
winziger interner Widerstand zieht den Pin im Ruhezustand auf **HIGH** (Plus).
Schließt der Relaiskontakt, wird GPIO27 mit GND verbunden und damit auf **LOW**
(Minus) gezogen. **LOW = Alarm.** Genau das wertet die Firmware aus
(`ACTIVE_LOW = true`).

> 🔌 **Strom:** Den ESP32 einfach per USB ans 5-V-Netzteil. Fertig.
>
> ⚠️ **Welche Pins gehen NICHT?** GPIO34–39 können **keinen** internen Pull-up und
> dürfen hier nicht verwendet werden. **GPIO27** ist ideal (kein „Strapping-Pin",
> der beim Booten stört). Andere gute Pins: 25, 26, 32, 33. Den Pin kannst du in
> `config.h` über `RELAY_PIN` ändern.
>
> 🔎 **Lötbrücke im Lader:** Je nach Einstellung im Ladegerät schließt der Kontakt
> entweder **ca. 10 s** oder **bis zur Quittierung am Melder**. Beides ist für
> dieses Projekt in Ordnung – die Firmware reagiert auf die steigende Flanke und
> hat danach einen 5-Minuten-Cooldown.

---

## 4. Bark auf dem iPhone einrichten

**Bark** ist eine kostenlose App + Dienst, mit dem man von „irgendwo" eine
Push-Nachricht an ein bestimmtes iPhone schicken kann. Jedes iPhone bekommt einen
eigenen geheimen **Device-Key** (eine zufällige Zeichenfolge). Wer den Key kennt,
kann diesem iPhone eine Nachricht schicken.

**Schritt für Schritt – auf JEDEM Empfänger-iPhone:**

1. **App installieren:** „**Bark – Customed Notifications**" aus dem App Store.
2. **App öffnen.** Sie zeigt oben eine Beispiel-URL an, z. B.
   `https://api.day.app/AbCdEf123456/...`. Der Teil **`AbCdEf123456`** ist der
   **Device-Key** dieses iPhones. Diesen Key brauchst du gleich für `config.h`.
   (Über den Kopier-Button bekommst du ihn sauber.)
3. **„Kritische Hinweise" erlauben (GANZ WICHTIG!):**
   Nur damit kommt der Alarm auch durch, wenn das iPhone **stumm** ist oder
   **„Nicht stören"** an ist.
   - iPhone-**Einstellungen** → **Mitteilungen** → in der App-Liste **Bark** suchen.
   - Den Schalter **„Kritische Hinweise"** (engl. *Critical Alerts*) **einschalten.**
   - Falls iOS beim ersten kritischen Alarm nachfragt: **erlauben**.
4. **Testen direkt in der App:** Bark hat einen „Test"-Knopf, der eine
   Beispiel-Nachricht schickt. Kommt sie an, funktioniert die App-Seite.

> Wiederhole das für **jede Person**, die alarmiert werden soll. Sammle alle
> Device-Keys – sie kommen gleich in die `config.h`.

### 4a. Alarm-Ton `alarm_fw` auf JEDEM iPhone installieren (PFLICHT)

Dieses Projekt benutzt als Alarmton den eigenen, langen Ton **`alarm_fw`**
(eingestellt in `config.h` über `ALARM_SOUND`). Damit der Alarm bei jedem
Empfänger **laut und ~30 Sekunden lang** klingelt, muss die Tondatei in der
**Bark-App auf JEDEM Empfänger-iPhone** importiert sein. Fehlt sie, spielt Bark
nur einen kurzen Standardton ab.

Die fertige Datei liegt im Projekt: **`alarm_fw.mp3`** (im Wurzelverzeichnis,
auch im GitHub-Repo).

**So bekommst du sie auf jedes iPhone und in Bark:**

1. **Datei aufs iPhone bringen** – ein Weg genügt:
   - Im **Safari** auf dem iPhone die Datei im GitHub-Repo öffnen
     (`alarm_fw.mp3` → „Download"), **oder**
   - per **AirDrop** / **iMessage** / **E-Mail** an das iPhone schicken.
2. **In Bark importieren:** Die Datei antippen → **Teilen-Symbol** (Quadrat mit
   Pfeil nach oben) → in der App-Liste **„Bark"** wählen bzw. **„In Bark öffnen / In
   Bark importieren"**. Alternativ in der Bark-App unten **„Settings/Einstellungen"
   → „Sounds"** → Ton hinzufügen.
3. **Prüfen:** In Bark unter **Sounds** sollte **`alarm_fw`** jetzt in der Liste
   stehen (antippen = Vorhören).

> ⏱️ **Wichtig:** iOS erlaubt für Mitteilungstöne **maximal 30 Sekunden** Länge.
> `alarm_fw.mp3` hält das ein. Eigene Töne also nie länger als 30 s machen.
>
> 💡 **Alternative ohne Import:** Wer sich den Import sparen will, trägt in `config.h`
> einen der **eingebauten** Bark-Töne ein (z. B. `multiwayinvitation` – ein langer
> Klingelton, oder `alarm`, `bell`). Diese sind auf jedem iPhone sofort verfügbar,
> klingen aber weniger nach „Sirene".

> **Eigener Server (optional):** Bark kann man auch selbst hosten. Dann trägst du
> in `config.h` bei `BARK_HOST` deine eigene Adresse ein (ohne `/` am Ende). Für
> den Anfang ist der öffentliche Server `https://api.day.app` völlig ausreichend.

---

## 5. Software vorbereiten: `config.h`

Alle Werte, die **du** anpassen musst, stehen in **einer einzigen Datei**:
`boss925_alarm_bark/config.h`.

Diese Datei ist absichtlich **nicht** im Projekt enthalten (sie enthält ja deine
Passwörter). Es gibt stattdessen eine **Vorlage** namens `config.example.h`.

**So legst du deine `config.h` an:**
- Kopiere `config.example.h` und nenne die Kopie `config.h` (im selben Ordner).
  - PlatformIO/Terminal: `cp boss925_alarm_bark/config.example.h boss925_alarm_bark/config.h`
  - In der Arduino IDE die Datei einfach im selben Ordner als `config.h` speichern.
- Danach die Platzhalter in `config.h` durch deine echten Werte ersetzen (WLAN,
  Bark-Keys usw.). `config.h` ist per `.gitignore` ausgeschlossen und landet
  **nicht** im Git – deine Zugangsdaten bleiben lokal.

**Was du mindestens ausfüllen musst:**

```c
#define WIFI_SSID       "DEIN_WLAN_NAME"      // Name deines WLANs
#define WIFI_PASSWORD   "DEIN_WLAN_PASSWORT"  // WLAN-Passwort

static const char* BARK_KEYS_ALARM[] = {
  "KEY_PERSON_1",     // Device-Key von iPhone 1
  "KEY_PERSON_2",     // Device-Key von iPhone 2  (Komma nicht vergessen!)
  // ... bis zu 10
};

#define BARK_KEY_STATUS "DEIN_EIGENER_KEY"     // nur DEIN iPhone (leise Status-Pings)
```

> 📋 `BARK_KEYS_ALARM` ist seit der Dashboard-Empfängerverwaltung nur noch die
> **Startliste** für den allerersten Boot. Sobald im NAS-Dashboard Empfänger
> gepflegt sind, übernimmt der ESP32 diese Liste automatisch und dauerhaft
> (siehe Abschnitt 11).

> 💡 Die geschweiften Klammern `{ }` und die Anführungszeichen `" "` müssen genau
> so stehen bleiben. Jeder Key kommt in `"..."`, danach ein **Komma**. Zeilen mit
> `//` am Anfang sind „auskommentiert" = ausgeschaltet.
>
> 🔐 **Sicherheit:** Behandle die Bark-Keys wie Passwörter – wer einen Key kennt,
> kann diesem iPhone Nachrichten schicken. Trage sie nur in deine lokale `config.h`
> (bzw. ins NAS-Dashboard) ein und teile sie nicht öffentlich. Landet ein Key doch
> einmal in falschen Händen, erstelle in der Bark-App einen neuen und ersetze ihn.

**Optional für das NAS-Dashboard:**

```c
#define REMOTE_MONITOR_ENABLED true
#define REMOTE_BASE_URL        "https://dein-name.synology.me/fw_alarm/api"
#define REMOTE_MACHINE_TOKEN   "DEIN_LANGER_ZUFAELLIGER_TOKEN"
#define REMOTE_DEVICE_ID       "boss925-01"
#define FW_BUILD_MARKER        "boss925-monitor-2026-07-07"
```

`REMOTE_MACHINE_TOKEN` ist ein anderes Geheimnis als deine Bark-Keys. Auf der
Synology steht davon nur der SHA-256-Hash in `nas_dashboard/config.php`.

---

## 6. Flashen – Variante A: Arduino IDE

„Flashen" = das Programm auf den ESP32 übertragen.

1. **Arduino IDE installieren** (Version 2.x) von arduino.cc.
2. **ESP32-Unterstützung hinzufügen:**
   - **Datei → Einstellungen** → Feld „Zusätzliche Boardverwalter-URLs":
     `https://espressif.github.io/arduino-esp32/package_esp32_index.json`
   - **Werkzeuge → Board → Boardverwalter…** → nach „**esp32**" suchen → von
     Espressif **installieren**.
3. **Projekt öffnen:** Doppelklick auf
   `boss925_alarm_bark/boss925_alarm_bark.ino`. Die IDE öffnet automatisch auch
   die `config.h` als Tab.
4. **Board wählen:** **Werkzeuge → Board → ESP32 Arduino → „ESP32 Dev Module".**
5. **Port wählen:** ESP32 per USB anstecken. **Werkzeuge → Port** → den neuen
   COM-Port (Windows) bzw. `/dev/cu.*` (Mac) auswählen.
   - *Windows erkennt nichts?* Dann fehlt der USB-Treiber (CP210x oder CH340) –
     siehe [Troubleshooting](#11-troubleshooting).
6. **Hochladen:** Auf den **Pfeil-Knopf (→)** oben links klicken. Beim ersten Mal
   muss man manchmal beim Erscheinen von „Connecting…" kurz den **BOOT**-Taster
   am ESP32 gedrückt halten.
7. **Serielle Ausgabe ansehen:** **Werkzeuge → Serieller Monitor**, rechts unten
   **115200 Baud** einstellen. Du solltest den Selbsttest-Text sehen.

---

## 7. Flashen – Variante B: PlatformIO

Für alle, die lieber die Kommandozeile nutzen (oder VS Code).

1. **VS Code** installieren, darin die Erweiterung **„PlatformIO IDE"**.
2. Projektordner `FW_Alarm` in VS Code öffnen.
3. ESP32 per USB anstecken.
4. Im Terminal (im Projektordner):

```bash
pio run                 # kompilieren (Test, ob alles baut)
pio run -t upload       # auf den ESP32 flashen
pio device monitor      # serielle Ausgabe ansehen (115200 Baud)
```

PlatformIO lädt die ESP32-Toolchain beim ersten `pio run` automatisch herunter.
Externe Libraries sind **keine** nötig.

---

## 8. Testen

**A) Ohne Lader – „Schalter von Hand simulieren":**
Verbinde kurz mit einem Stück Draht **GPIO27 mit GND** (das macht genau das, was
der Relaiskontakt tut). Halte die Verbindung ca. **1 Sekunde**.
→ Im seriellen Monitor erscheint `>>> ALARM erkannt …`, und auf den iPhones
sollte der laute Alarm losgehen.

**B) Mit Lader:**
Manche Swissphone-Lader haben einen **Test-/Probealarm**. Wenn der Melder
korrekt steckt und der Lader auslöst, schließt der Kontakt und der ESP32
alarmiert.

**Was du beim Start sehen solltest (serieller Monitor):**
```
BOSS-925 Alarm-Wächter startet...
Watchdog aktiv (30 s).
Zeit synchronisiert: 14:23:05
=== SELBSTTEST ===
Eingang Pin 27, ACTIVE_LOW=ja, aktueller Stand: offen (Ruhe)
Alarm-Empfänger: 2 (Listen-Version 0, 0 = config.h-Startliste)
WLAN: verbunden
IP-Adresse: 192.168.x.x
=== SELBSTTEST ENDE ===
```
Außerdem kommt auf **dein** iPhone eine leise Nachricht „Alarm-Wächter online".

---

## 9. Was die Einstellungen bedeuten

Alle in `config.h`:

| Einstellung | Bedeutung |
|-------------|-----------|
| `WIFI_SSID` / `WIFI_PASSWORD` | Dein WLAN-Zugang. |
| `BARK_HOST` | Bark-Server. Standard `https://api.day.app`. Eigener Server möglich. |
| `BARK_KEYS_ALARM[]` | Startliste der Empfänger-Keys für den allerersten Boot; danach ist die im NAS-Dashboard gepflegte Liste führend (Abschnitt 11). |
| `BARK_KEY_STATUS` | Nur dein Key für leise Status-/Heartbeat-Pings. |
| `ALARM_TITLE` / `ALARM_BODY` | Überschrift und Text des Alarms. |
| `ALARM_SOUND` | Bark-Soundname (z. B. `alarm`, `alarm_fw`, `bell`). |
| `ALARM_VOLUME` | 0–10, nur bei kritischem Alarm wirksam. |
| `ALARM_CALL` | `true` = Ton wiederholt sich ~30 s (`call=1`). |
| `RELAY_PIN` | Eingangs-Pin (Standard 27). |
| `ACTIVE_LOW` | `true` = Kontakt gegen GND, LOW = Alarm. |
| `RELAY_PIN2_ENABLED` / `RELAY_PIN2` | Optionaler zweiter Eingang als Reserve. |
| `DEBOUNCE_MS` | Wie lange der Kontakt mind. zu sein muss (Standard 300 ms). |
| `COOLDOWN_MS` | Mindestabstand zwischen zwei Alarmen (Standard 5 min). |
| `REARM_OPEN_MS` | Kontakt muss nach einem Alarm erst so lange **offen** gewesen sein, bevor ein neuer Alarm möglich ist (Standard 2 s). Verhindert Dauerfeuer bei dauerhaft geschlossenem Kontakt. |
| `STUCK_CONTACT_WARN_MS` | Bleibt der Kontakt nach einem Alarm so lange zu (Standard 30 min), bekommst du eine leise „Kontakt klemmt?"-Warnung. |
| `BARK_MAX_TRIES` | Sende-Versuche pro Key bei Netzfehler (Standard 3). |
| `ALARM_RETRY_MS` / `ALARM_RETRY_GIVEUP_MS` | Nachsenden: War WLAN/Internet beim Alarm weg, wird jede Minute erneut versucht, bis alle Empfänger versorgt sind – nach 15 min wird aufgegeben (und du bekommst eine Warnung). |
| `HEARTBEAT_*` | Täglicher „ich lebe noch"-Ping (siehe unten), inkl. Wiederholung nach Fehlversuch (`HEARTBEAT_RETRY_MS`). |
| `NTP_*` | Holt die Uhrzeit aus dem Internet (für Zeitstempel & Heartbeat-Uhrzeit). |
| `REMOTE_*` | Optionales NAS-Dashboard: Status-Push, Command-Polling, kurze Timeouts und Backoff. |
| `FW_BUILD_MARKER` | Frei wählbare Kennung, die im Dashboard angezeigt wird (z. B. Datum/Version des Flashs). |
| `WDT_*` | Hardware-Watchdog: startet den ESP32 neu, falls er hängt. |

---

## 10. Heartbeat: der „ich lebe noch"-Ping

Einmal am Tag (Standard: **8:00 Uhr**, einstellbar über `HEARTBEAT_HOUR`) schickt
die Box **nur an dich** eine leise Nachricht „System läuft". Damit weißt du, dass
die Box **Strom hat, im WLAN ist und das Internet erreicht.**

> 📭 **Kommt der tägliche Ping NICHT?** → Geh zur Box und prüfe:
> Strom da? WLAN/Router okay? Internet okay? Im Zweifel ESP32 kurz vom Strom
> trennen und wieder anstecken.

**Noch sicherer (optionaler externer „Totmann-Schalter"):**
Der Heartbeat sagt dir nur etwas, wenn die Box **noch funktioniert**. Fällt sie
komplett aus, kommt logischerweise **gar nichts** – und du müsstest das aktive
Fehlen bemerken. Profis lösen das mit einem **„Dead Man's Switch"-Monitor**: ein
externer Dienst, der **dich** alarmiert, wenn der erwartete Ping **ausbleibt**.

> ✅ **Schon eingebaut, wenn du das NAS-Dashboard nutzt:** Der Offline-Wächter
> `cron/check_offline.php` (per DSM-Aufgabenplaner alle paar Minuten) meldet dir
> **einmal** leise per Bark, wenn die Box zu lange keinen Status mehr gepusht hat –
> und **einmal**, wenn sie wieder da ist. Für echte Unabhängigkeit vom eigenen
> Netz kannst du zusätzlich einen externen Dienst wie **healthchecks.io** nutzen
> (siehe [TODO](#13-sicherheit--offene-punkte-todo)).

### 10a. Wöchentlicher Probealarm der ILS = automatischer Wochentest

Ein **Probealarm der Leitstelle** (z. B. Mittwoch ~19 Uhr) alarmiert deinen Melder
genauso wie ein echter Einsatz – der Relaiskontakt im Lader schließt also auch
dabei, und die Box sendet **den vollen lauten Alarm an alle**.

Das ist **Absicht**: Der wöchentliche Probealarm testet damit automatisch die
**komplette Kette** (Melder → Relais → ESP32 → WLAN → Bark → iPhones) – ein viel
stärkerer Nachweis als der reine Heartbeat. Kommt am Probealarm-Tag der laute
Alarm **nicht**, weißt du sofort, dass etwas in der Kette klemmt.

Alle Empfänger sollten also wissen: **Der Alarm zur wöchentlichen Probezeit ist
der Test.**

---

## 11. NAS-Dashboard: Fernüberwachung und manueller Test

Im Ordner `nas_dashboard/` liegt ein kleines PHP-Dashboard für Synology Web
Station. Es braucht keine Datenbank und kein Docker; Nginx + PHP 8.3 reichen.

Funktionen:

- Status-Push vom ESP32 an `api/status.php`: WLAN, RSSI, IP, Uptime, Kontaktstand,
  ISR-Latch, offener Alarm samt Empfänger-Fortschritt, Heartbeat-Ergebnis,
  Cooldown/Wiederbewaffnung, Klemm-Warnung und Firmware-Kennung.
- Command-Polling über `api/poll.php`: `TEST` läuft über den ESP32 (wartet auf
  dessen Poll) und sendet eine leise Bark-Nachricht nur an `BARK_KEY_STATUS` –
  damit wird die halbe Kette (WLAN, NAS-Anbindung, Bark-Versand vom ESP32)
  mitgetestet.
- `REAL ALARM` wird dagegen **direkt vom NAS** per Bark Critical Alert an alle
  Empfänger gesendet – bewusst ohne Umweg über den ESP32, denn manuell
  alarmieren muss man vor allem dann, wenn die ESP32-Kette gerade klemmt.
- **Empfängerverwaltung im Dashboard**: Bark-Keys lassen sich im Panel
  „Alarm-Empfänger" hinzufügen und löschen. Die Liste ist versioniert und gilt
  für beide Alarmwege; der ESP32 sieht die Version bei jedem Poll, holt sich
  Änderungen automatisch über `api/keys.php` und speichert sie dauerhaft im
  NVS-Flash (übersteht Reboot und NAS-Ausfall). Jede Übernahme bestätigt er mit
  einer leisen Statusmeldung. `BARK_KEYS_ALARM` in `config.h` ist nur noch die
  Startliste für den allerersten Boot; sobald die Dashboard-Liste gepflegt ist,
  ist sie führend. Der letzte Empfänger lässt sich nicht löschen.
- Dashboard mit Login, Offline-Anzeige, zwei Buttons (`TEST`, `REAL ALARM` mit
  zusätzlicher Browser-Bestätigung und Ergebnis pro Empfänger) und einem
  Abbrechen-Knopf für noch nicht abgeholte Befehle.
- **Demo-Modus** (Panel „Betriebsmodus", nur Admin): leitet **alle** Alarme –
  Relaisalarm über den ESP32 und `REAL ALARM` vom NAS – auf genau **einen**
  Test-Empfänger aus der gepflegten Liste um, damit du gefahrlos üben kannst.
  Der Alarm ist dabei zeichengenau identisch mit einem echten (gleicher
  Text/Ton/Level), nur die Empfängerliste ist kürzer. Ein Warn-Banner zeigt, ob
  der ESP32 die Demo-Liste schon übernommen hat – **vorher nicht testen**.
- **Optionaler Lese-Benutzer** (`dashboard_readonly_user` in `config.php`): sieht
  Status und Empfängerliste (Keys maskiert), kann aber weder `TEST` noch
  `REAL ALARM` auslösen und die Liste nicht ändern – die Sperre greift
  serverseitig, nicht nur im Browser.
- **Offline-Wächter** (`cron/check_offline.php`, DSM-Aufgabenplaner): meldet leise
  per Bark, wenn die Box zu lange keinen Status mehr pusht (und wenn sie wieder
  da ist). Details in `nas_dashboard/SETUP.md`.

Die Dashboard-URL sieht typischerweise so aus:

```text
https://dein-name.synology.me/fw_alarm/
```

Die ESP32-API-Basis-URL für `config.h`:

```text
https://dein-name.synology.me/fw_alarm/api
```

Am Router muss nur **TCP-Port 443** zur Synology weitergeleitet werden. Details
stehen in `nas_dashboard/SETUP.md`.

### Design

Das Command-Protokoll ist bewusst kein JSON, sondern ein kleines Textformat:

```text
ok=1
nonce=esp32nonce
id=17
type=TEST
```

Jeder Befehl bekommt auf dem NAS eine monotone ID. Der ESP32 schickt pro Poll eine
Nonce, die das NAS spiegeln muss; alte Antworten mit falscher Nonce werden
verworfen. Beim Poll wird ein `pending` Befehl unter Dateisperre sofort als
`delivered` markiert; der ESP32 führt nur IDs aus, die größer als die zuletzt im
laufenden Boot gesehene ID sind, und schickt danach eine ACK. Alte oder bereits
ausgelieferte Kommandos werden nicht erneut gesendet. Diese Entscheidung schützt
vor Replay-Doppelalarmen; wenn eine ACK verloren geht, zeigt das Dashboard den
Zustand an, aber das NAS feuert denselben Befehl nicht heimlich noch einmal.
Holt der ESP32 einen `pending`-Befehl nicht innerhalb von 5 Minuten ab
(einstellbar über `pending_command_ttl_seconds`), verfällt er automatisch als
`expired` – er darf nicht Stunden später noch „nachschlagen". Über das
Dashboard lässt er sich auch manuell abbrechen.

Status wird alle 60 Sekunden und zusätzlich bei wichtigen Ereignissen gepusht
(Alarm, WLAN wieder da, Heartbeat-Ergebnis). Polling läuft alle 10 Sekunden; bei
Fehlern gibt es 30 Sekunden Backoff und 4 Sekunden HTTP-Timeout (der
TLS-Handshake ESP32↔Synology braucht typischerweise 1–3 s, knappere Timeouts
würden dauernd fehlschlagen). So bleibt der manuelle TEST flott, ohne bei
NAS-Ausfall die Relais-Erkennung oder den Bark-Alarmweg zu blockieren. Das
Dashboard markiert die Box nach 180 Sekunden ohne Status-Push als offline.

Die Laufzeitdateien (Status, Befehle, Log) speichert das NAS als `*.php` mit
einer Guard-Zeile am Anfang: Nginx liefert sie dadurch nie als statische Datei
aus, sondern übergibt sie an PHP, das sofort mit 404 abbricht. Das ist nötig,
weil Synology Web Station Nginx nutzt und `.htaccess`-Regeln ignoriert.

TLS läuft über HTTPS/Let's Encrypt auf der Synology. Der ESP32 nutzt wie beim
Bark-Pfad `setInsecure()`, weil das auf Mikrocontrollern robuster gegen
Root-CA-/Uhrzeit-Probleme ist. Authentifiziert wird trotzdem: ESP32-Endpunkte
prüfen ein langes Maschinen-Token (nur per POST), das Dashboard nutzt ein
separates Login mit Passwort-Hash und CSRF-Schutz.

---

## 12. Troubleshooting

| Problem | Mögliche Ursache & Lösung |
|---------|---------------------------|
| **Windows zeigt keinen COM-Port** | USB-Seriell-Treiber fehlt. Schau auf den Chip neben der USB-Buchse: **CP2102** → Silicon-Labs-CP210x-Treiber installieren; **CH340** → CH340-Treiber. Anderes/„Daten"-USB-Kabel probieren (manche sind nur Ladekabel!). |
| **Upload bricht bei „Connecting…" ab** | Beim Erscheinen von „Connecting…" den **BOOT**-Taster am ESP32 gedrückt halten, bis der Upload startet. `upload_speed` ggf. auf `115200` senken. |
| **`config.h: No such file`** | Du hast noch keine `config.h`. Kopiere `config.example.h` → `config.h`. |
| **WLAN verbindet nicht** | SSID/Passwort prüfen (Groß/Klein!). ESP32 kann **nur 2,4-GHz-WLAN**, **kein** reines 5-GHz. Ggf. im Router ein 2,4-GHz-Netz aktivieren. |
| **Bark-POST liefert nicht HTTP 200** | Im Monitor erscheint z. B. `HTTP 400/404`. → Device-Key falsch/Tippfehler. `HTTP -1`/`-11` → kein Internet/Timeout. |
| **Dashboard bleibt OFFLINE** | Prüfe `REMOTE_BASE_URL`, `REMOTE_MACHINE_TOKEN`, Synology-HTTPS-Zertifikat, Portweiterleitung TCP 443 und Schreibrechte für `data_dir` in `nas_dashboard/config.php`. |
| **Dashboard-Befehl bleibt „delivered"** | Der ESP32 hat den Befehl vom NAS abgeholt, aber die ACK kam nicht zurück. Aus Sicherheitsgründen wird dieselbe ID nicht erneut ausgeliefert; bei Bedarf neuen Befehl senden. |
| **Dashboard-Befehl wird „expired"** | Der ESP32 hat innerhalb der TTL (Standard 5 min) nicht gepollt – Box offline? Absichtliches Verhalten: Ein liegengebliebener Befehl darf nicht Stunden später noch ausgeführt werden. |
| **REAL ALARM meldet „PHP-cURL fehlt"** | In DSM → Web Station → PHP-Profil bearbeiten → Erweiterung `curl` aktivieren. |
| **Nachricht kommt, aber leise / kein Alarm bei Stumm** | „**Kritische Hinweise**" für Bark im iPhone **nicht** aktiviert (siehe Abschnitt 4). Das ist der häufigste Fehler! |
| **Alarm löst „grundlos" aus** | Eingang schwingt/prellt. `DEBOUNCE_MS` erhöhen (z. B. 500). Verkabelung/Masseverbindung prüfen. |
| **Ständige Neustarts** | Watchdog schlägt an, weil etwas blockiert (oft schlechtes WLAN). `WDT_TIMEOUT_S` testweise erhöhen und Monitor beobachten. |

---

## 13. Sicherheit & offene Punkte (TODO)

**Bereits umgesetzt:**
- ✅ Secrets (`config.h`) per `.gitignore` aus Git herausgehalten.
- ✅ Pro Key einzeln senden, 1–2 Wiederholungen bei Fehler, ein toter Key
  blockiert die anderen nicht.
- ✅ **Nachsenden bei Netzausfall:** War WLAN/Internet im Alarm-Moment weg, wird
  der Alarm gemerkt und bis 15 min lang jede Minute nachgesendet (pro Empfänger,
  mit Zeitstempel der Erkennung und Hinweis „verspaetet zugestellt").
- ✅ **Kein Dauerfeuer:** Nach einem Alarm muss der Kontakt erst wieder öffnen,
  bevor ein neuer Alarm ausgelöst wird; klemmt er länger, kommt eine Warnung.
- ✅ **Pin-Interrupt-Latch:** Auch kurze Alarm-Impulse, die während einer
  laufenden Sendung eintreffen, gehen nicht verloren.
- ✅ WLAN-Auto-Reconnect (nicht-blockierend).
- ✅ Selbsttest beim Start.
- ✅ Hardware-Watchdog (Auto-Reboot bei Hänger; funktioniert auch mit
  ESP32-Core 3.x korrekt).
- ✅ NTP-Zeitstempel in Nachrichten + feste Heartbeat-Uhrzeit; Heartbeat wird
  bei Fehlversuch nach 5 min wiederholt statt erst am nächsten Tag.
- ✅ Optionaler zweiter Eingangs-Pin (`RELAY_PIN2_ENABLED`).
- ✅ Optionales Synology-Dashboard mit Status-Push, Command-Polling, separatem
  Maschinen-Token, Dashboard-Login und genau-einmal-Auslieferung pro Command-ID;
  Befehle verfallen per TTL und sind abbrechbar.
- ✅ Manueller `REAL ALARM` direkt vom NAS an Bark – unabhängig vom ESP32, als
  Rückfallebene, wenn dessen Kette klemmt.
- ✅ Empfängerverwaltung im Dashboard (versioniert, für beide Alarmwege, vom ESP32
  automatisch in den NVS-Flash übernommen) plus **Demo-Modus** und optionaler
  **Lese-Benutzer**.
- ✅ **NAS-Offline-Wächter** (`cron/check_offline.php`): meldet leise per Bark,
  wenn die Box zu lange schweigt bzw. wieder da ist (Dead-Man's-Switch auf dem NAS).

**Noch offen / bewusst nicht eingebaut (TODO):**
- 🔧 **OTA-Update (drahtlos flashen):** Auf Wunsch nicht eingebaut (kleinere
  Angriffsfläche). Wer es nachrüsten will: `ArduinoOTA`-Bibliothek (im Core),
  `ArduinoOTA.begin()` in `setup()` und `ArduinoOTA.handle()` in `loop()`,
  unbedingt mit Passwort. Dann kann man die Box ohne USB-Kabel neu flashen.
- 🔧 **Externer Dead-Man's-Switch fürs Heartbeat-Monitoring:** Der NAS-Offline-Wächter
  oben deckt das schon ab, hängt aber am selben Heim-Netz/NAS. Für echte
  Unabhängigkeit empfiehlt sich zusätzlich ein **externer** Dienst, der **dich**
  alarmiert, wenn der erwartete Ping ausbleibt. Zwei einfache Wege:
  1. Einen kostenlosen Dienst wie **healthchecks.io** nutzen: Dort einen „Check"
     mit erwarteter Periode (z. B. täglich) anlegen, dessen Ping-URL die Box
     zusätzlich zum Bark-Heartbeat aufruft. Bleibt der Ping aus, alarmiert dich
     healthchecks.io aktiv.
  2. Falls du einen **eigenen Bark-Server** betreibst: serverseitig prüfen, ob
     der tägliche Status-Ping ankam, und sonst Alarm schlagen.
- 🔧 **TLS-Zertifikatsprüfung:** Aktuell `setInsecure()` (keine Prüfung) – einfach
  und robust gegen abgelaufene Root-Zertifikate, theoretisch aber MITM-anfällig.
  Wer es härten will, kann das Root-CA-Zertifikat des Bark-Servers einpinnen.

---

### Lizenz / Haftung
Veröffentlicht unter der **MIT-Lizenz** (siehe [`LICENSE`](LICENSE)) – du darfst das
Projekt frei nutzen, anpassen und weitergeben. **Ohne Gewähr**, Nutzung auf eigenes
Risiko. Nochmals: **Zusatz, kein Ersatz** für die offizielle Melder-Alarmierung.

Das Repository enthält **keine Geheimnisse** – WLAN-Zugang, Bark-Keys, Tokens und
Passwort-Hashes stehen ausschließlich in den lokalen, per `.gitignore`
ausgeschlossenen Dateien `config.h` bzw. `config.php`. Lege dir diese aus den
mitgelieferten `*.example.*`-Vorlagen an.
