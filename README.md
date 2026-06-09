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
11. [Troubleshooting (wenn etwas nicht klappt)](#11-troubleshooting)
12. [Sicherheit & offene Punkte (TODO)](#12-sicherheit--offene-punkte-todo)

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

---

## 2. Teileliste

| Teil | Beschreibung | ca. Preis |
|------|--------------|-----------|
| **ESP32 DevKit (WROOM-32)** | Der Mikrocontroller mit WLAN. „DevKit" = fertige Platine mit USB-Buchse. | 6–12 € |
| **USB-Kabel + 5 V/1 A Netzteil** | Stromversorgung (z. B. altes Handy-Ladegerät). | vorhanden / 5 € |
| **5-poliger DIN-Stecker (männlich)** | Passt in die DIN-Buchse des Swissphone-Laders. | 2–4 € |
| **2 Dupont-/Litzendrähte** | Verbinden DIN-Stecker mit dem ESP32. | Cent-Beträge |
| optional: kleines Gehäuse | Damit alles geschützt ist. | 5–10 € |

> Du brauchst **keinen** Lötkolben zwingend, aber ein klein wenig Löten am
> DIN-Stecker macht die Verbindung zuverlässiger. Stecken/Klemmen geht zur Not auch.

---

## 3. Verkabelung

Der Lader (Swissphone **LGRA-Expert**) gibt den Relaiskontakt an der
**5-poligen DIN-Buchse** auf **Pin 1 und Pin 3** aus. Diese beiden Pins werden
bei Alarm intern verbunden.

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
- **Einfachste Variante:** In diesem Projekt liegt bereits eine `config.h` mit
  deinem bisherigen Bark-Key vorbereitet. Du musst nur noch **WLAN-Name und
  -Passwort** eintragen und ggf. weitere Empfänger ergänzen.
- **Von Null:** Kopiere `config.example.h` und nenne die Kopie `config.h`
  (im selben Ordner).
  - PlatformIO/Terminal: `cp boss925_alarm_bark/config.example.h boss925_alarm_bark/config.h`

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

> 💡 Die geschweiften Klammern `{ }` und die Anführungszeichen `" "` müssen genau
> so stehen bleiben. Jeder Key kommt in `"..."`, danach ein **Komma**. Zeilen mit
> `//` am Anfang sind „auskommentiert" = ausgeschaltet.
>
> 🔐 **Hinweis:** In der mitgelieferten `config.h` steckt bereits ein echter
> Bark-Key aus deinem ursprünglichen Sketch. Falls dieser Key jemals öffentlich
> geteilt wurde, erstelle in der Bark-App einen neuen und ersetze ihn.

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
BOSS-925 Alarm-Waechter startet...
Watchdog aktiv (30 s).
Zeit synchronisiert: 14:23:05
=== SELBSTTEST ===
Eingang Pin 27, ACTIVE_LOW=ja, aktueller Stand: offen (Ruhe)
Alarm-Empfaenger (gueltige Keys): 2
WLAN: verbunden
IP-Adresse: 192.168.x.x
=== SELBSTTEST ENDE ===
```
Außerdem kommt auf **dein** iPhone eine leise Nachricht „Alarm-Waechter online".

---

## 9. Was die Einstellungen bedeuten

Alle in `config.h`:

| Einstellung | Bedeutung |
|-------------|-----------|
| `WIFI_SSID` / `WIFI_PASSWORD` | Dein WLAN-Zugang. |
| `BARK_HOST` | Bark-Server. Standard `https://api.day.app`. Eigener Server möglich. |
| `BARK_KEYS_ALARM[]` | Liste aller Empfänger-Keys (Alarm an alle). |
| `BARK_KEY_STATUS` | Nur dein Key für leise Status-/Heartbeat-Pings. |
| `ALARM_TITLE` / `ALARM_BODY` | Überschrift und Text des Alarms. |
| `ALARM_SOUND` | Bark-Soundname (z. B. `alarm`, `alarm_fw`, `bell`). |
| `ALARM_VOLUME` | 0–10, nur bei kritischem Alarm wirksam. |
| `ALARM_CALL` | `true` = Ton wiederholt sich ~30 s (`call=1`). |
| `RELAY_PIN` | Eingangs-Pin (Standard 27). |
| `ACTIVE_LOW` | `true` = Kontakt gegen GND, LOW = Alarm. |
| `RELAY_PIN2_ENABLED` / `RELAY_PIN2` | Optionaler zweiter Eingang als Reserve. |
| `DEBOUNCE_MS` | Wie lange der Kontakt mind. zu sein muss (Standard 300 ms). |
| `COOLDOWN_MS` | Sperre nach einem Alarm (Standard 5 min), verhindert Dauerfeuer. |
| `BARK_MAX_TRIES` | Sende-Versuche pro Key bei Netzfehler (Standard 3). |
| `HEARTBEAT_*` | Täglicher „ich lebe noch"-Ping (siehe unten). |
| `NTP_*` | Holt die Uhrzeit aus dem Internet (für Zeitstempel & Heartbeat-Uhrzeit). |
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
Siehe [TODO](#12-sicherheit--offene-punkte-todo).

---

## 11. Troubleshooting

| Problem | Mögliche Ursache & Lösung |
|---------|---------------------------|
| **Windows zeigt keinen COM-Port** | USB-Seriell-Treiber fehlt. Schau auf den Chip neben der USB-Buchse: **CP2102** → Silicon-Labs-CP210x-Treiber installieren; **CH340** → CH340-Treiber. Anderes/„Daten"-USB-Kabel probieren (manche sind nur Ladekabel!). |
| **Upload bricht bei „Connecting…" ab** | Beim Erscheinen von „Connecting…" den **BOOT**-Taster am ESP32 gedrückt halten, bis der Upload startet. `upload_speed` ggf. auf `115200` senken. |
| **`config.h: No such file`** | Du hast noch keine `config.h`. Kopiere `config.example.h` → `config.h`. |
| **WLAN verbindet nicht** | SSID/Passwort prüfen (Groß/Klein!). ESP32 kann **nur 2,4-GHz-WLAN**, **kein** reines 5-GHz. Ggf. im Router ein 2,4-GHz-Netz aktivieren. |
| **Bark-POST liefert nicht HTTP 200** | Im Monitor erscheint z. B. `HTTP 400/404`. → Device-Key falsch/Tippfehler. `HTTP -1`/`-11` → kein Internet/Timeout. |
| **Nachricht kommt, aber leise / kein Alarm bei Stumm** | „**Kritische Hinweise**" für Bark im iPhone **nicht** aktiviert (siehe Abschnitt 4). Das ist der häufigste Fehler! |
| **Alarm löst „grundlos" aus** | Eingang schwingt/prellt. `DEBOUNCE_MS` erhöhen (z. B. 500). Verkabelung/Masseverbindung prüfen. |
| **Ständige Neustarts** | Watchdog schlägt an, weil etwas blockiert (oft schlechtes WLAN). `WDT_TIMEOUT_S` testweise erhöhen und Monitor beobachten. |

---

## 12. Sicherheit & offene Punkte (TODO)

**Bereits umgesetzt:**
- ✅ Secrets (`config.h`) per `.gitignore` aus Git herausgehalten.
- ✅ Pro Key einzeln senden, 1–2 Wiederholungen bei Fehler, ein toter Key
  blockiert die anderen nicht.
- ✅ WLAN-Auto-Reconnect (nicht-blockierend).
- ✅ Selbsttest beim Start.
- ✅ Hardware-Watchdog (Auto-Reboot bei Hänger).
- ✅ NTP-Zeitstempel in Nachrichten + feste Heartbeat-Uhrzeit.
- ✅ Optionaler zweiter Eingangs-Pin (`RELAY_PIN2_ENABLED`).

**Noch offen / bewusst nicht eingebaut (TODO):**
- 🔧 **OTA-Update (drahtlos flashen):** Auf Wunsch nicht eingebaut (kleinere
  Angriffsfläche). Wer es nachrüsten will: `ArduinoOTA`-Bibliothek (im Core),
  `ArduinoOTA.begin()` in `setup()` und `ArduinoOTA.handle()` in `loop()`,
  unbedingt mit Passwort. Dann kann man die Box ohne USB-Kabel neu flashen.
- 🔧 **Externer Dead-Man's-Switch fürs Heartbeat-Monitoring:** Empfohlen, damit
  ein **Ausfall** der Box auffällt. Zwei einfache Wege:
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
Privates Hobby-Projekt. **Ohne Gewähr.** Nochmals: **Zusatz, kein Ersatz** für die
offizielle Melder-Alarmierung.
