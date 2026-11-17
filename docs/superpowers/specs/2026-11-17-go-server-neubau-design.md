# Der Dashboard-Server neu in Go — Design (Neubau statt Übertragung)

Stand: 17.11.2026. Ersetzt `2026-11-16-go-server-design.md` (Übertragung der
PHP-Fassung auf demselben Datenordner). Betrifft nur die Server-Seite des
Dashboards (`nas_dashboard/` auf der Oracle-VM). Firmware, NAS-Brücke und
Außen-Wächter bleiben, wie sie sind.

## 1. Absicht und Warum

Der Server des Dashboards (heute PHP 8.3 hinter Nginx und PHP-FPM, dazu ein
Wächter-Skript per Timer) wird durch **ein** Programm in Go ersetzt:
`fw-alarm`. Von außen ändert sich nichts – dieselben Adressen, dieselben
Antworten, dieselbe Oberfläche, dieselben Regeln. **Innen wird neu gebaut**,
nicht die PHP-Fassung nachgebaut.

Worte des Nutzers (17.11.2026):

- „The goal of this migration is to have a repo that uses best practices all
  across, using modern languages und frameworks, and being fully bulletproof
  (also important that the security is 100% good)“
- „it is NOT a priority to keep php for 14 days; if ignoring the current php
  structure and doing it from scratch benefits the project, do that“

Der erste Entwurf (16.11.) hielt den Datenordner der PHP-Fassung
zeichengenau bei, damit man 14 Tage lang ohne Datenverlust zurückschalten
kann. Der Preis: nachsichtiges Lesen „wie PHP“, Daten als ungetypte
Schlüssel-Wert-Bäume, Sperrdateien zwischen zwei Prozessen, Sicherungs- und
Reihenfolge-Regeln für mehrere Dateien – also PHP, in Go geschrieben. Mit
der neuen Vorgabe entfällt der Grund dafür.

## 2. Ziele, Nicht-Ziele, Anti-Ziele

**Ziele**

1. Ein Programm ersetzt Nginx, PHP-FPM und das Wächter-Skript.
2. Alles, woran andere Geräte und Menschen hängen, bleibt gleich (§4).
3. Ein Alarm und die Antworten an die Box hängen **nie an der Festplatte**:
   Gelesen wird ausschließlich aus dem Arbeitsspeicher.
4. Innen streng und getypt: feste Datentypen, strenges Einlesen, jede Regel
   als reine Funktion einzeln prüfbar; keine Sperrdateien, keine
   Reihenfolge-Regeln zwischen Dateien.
5. Die vorhandenen Daten werden **einmal** übernommen (§9) – ohne dass ein
   Kollege einen neuen Kurzbefehl-Link braucht und ohne Geister-Meldungen
   beim Umschalten.
6. Sicherheit nach Stand der Technik (§10); wenige, kleine Fremdbibliotheken.
7. Am Ende laufen auf der VM weder PHP noch Nginx, und im Repo liegt kein
   PHP mehr.

**Nicht-Ziele**

- Keine neue Oberfläche: `assets/` bleibt (nur Texte, die `config.php` oder
  die alte Ansicht nennen, ändern sich).
- Keine Änderung an Firmware, `nas_bridge/`, `cloud_watcher/` **in diesem
  Entwurf**. Die Firmware darf seit dem 17.11. geändert werden (geflasht
  werden kann erst in ca. 4 Wochen); sie bekommt danach einen eigenen,
  kleinen Entwurf (§12a).
- Keine neuen Funktionen (Passkeys folgen danach, in Go).
- Keine Datenbank, kein Docker, kein npm, kein Web-Framework.
- Keine neuen Adressen: Die Pfade behalten ihr `.php` (§4).

**Anti-Ziele** (so wäre es gescheitert, auch wenn es „läuft“)

- Ein Alarm geht wegen der Umstellung nicht, doppelt oder an die falschen
  Empfänger – auch nicht in Tests auf der VM.
- Ein Kollege braucht einen neuen Kurzbefehl-Link.
- Die Box bekommt vom Server ein 403 (sie warnte dann „Token abgelehnt“).
- Beim Umschalten entstehen Geister: ein Relaisalarm im Verlauf, der keiner
  war; eine zweite „Box OFFLINE“-Meldung; ein Schwung „wieder laut“-Pushs;
  ein Befehl, den die Box ein zweites Mal ausführt.
- Eine Regel aus `CLAUDE.md` geht still verloren, weil kein Test sie hielt.
- Die VM fällt unter die 20-%-Grenze von Oracle und wird gestoppt.

## 3. Ausgangslage

| Teil | Heute |
|---|---|
| Anwendung | `nas_dashboard/`: ca. 6 700 Zeilen PHP (`lib/common.php` 3 156, `dashboard_api.php` 1 267, `cron/check_offline.php` 571, `api/*.php`) |
| Oberfläche | `nas_dashboard/assets/` (Preact/htm, klassische Skripte, ein Stylesheet), kein Build-Schritt |
| Server | Oracle-VM, Ubuntu 24.04 aarch64, 1 GB; `cloudflared` → Nginx `127.0.0.1:8080` → PHP-FPM (Benutzer `fwalarm`); Timer `fw-alarm-cron` alle 15 min |
| Daten | `/srv/fw-alarm/data`: JSON-Dateien `<name>.php` mit Guard-Zeile, Verlauf `commands.log.php`, Sperrdateien; `config.php` nur auf der VM |
| Box | ruft alle 10 s `api/poll.php`, alle 60 s `api/status.php` (Zeitlimit 4 s), bis zum Flashen über die NAS-Brücke (Zeitlimits 3 s/3,5 s) |
| Tests | `tests/backend/` (Server von außen, 368 Fälle), `tests/dashboard/` (Logik + 7 Chrome-Szenarien), `tests/bridge/`, `tests/watcher/`, `tests/server/` |
| PC | Windows, PHP 8.3, Node 24; Go wird installiert |

Rund ein Drittel der Server-Tests greift heute direkt in die Dateien der
PHP-Fassung (`backend.data.read/edit/write/corrupt/block`, ca. 100 Stellen,
vor allem `alarm_keys.json`, `latest_status.json`, `demo_mode.json`, `.bak`).

## 4. Was von außen gleich bleiben muss

Diese Liste ist die Grenze des Neubaus. Alles andere ist frei.

1. **Adressen:** `/` und `/index.php`, `/dashboard_api.php`,
   `/api/{status,poll,ack,keys,mute,alive}.php`, `/manifest.json`,
   `/assets/…`. `/classic.php` leitet mit 302 auf `index.php`. Alles andere
   ist 404 – auch `/api/health.php` (entfällt, §6.6).
2. **Box-Protokoll** für alte UND neue Firmware, mit und ohne Brücke
   (`X-FW-Bridge` → `via_bridge`): Klartext-Zeilen, Nonce gespiegelt, Token
   nur im POST-Rumpf, falsches Token = Status 403 mit Rumpf genau
   `FORBIDDEN`, Befehle höchstens einmal, monotone Nummern. Antworten an die
   Box warten nie auf Bark.
3. **Kurzbefehl-Links:** `api/mute.php?t=<Token>&state=on|off` und die
   Alt-Form `key=`; dieselben Tokens wie heute; Text-Antwort für Kurzbefehle,
   Seite für Browser, Vorschau-Abrufer schalten nichts.
4. **Außen-Wächter:** `api/alive.php` mit eigenem Token; Antwortzeilen
   `ok`, `nonce`, `stored`, `box_age_s`, `cron_age_s`, `cron_configured`,
   `nas_send_failed` in ihrer heutigen Bedeutung.
5. **Schnittstelle der Oberfläche:** `dashboard_api.php` mit denselben
   Aktionen, Feldern, Fehlercodes und Zugangsregeln (`post`/`csrf`/`admin`),
   Anmeldung über `index.php`, dieselben Seiten-Hüllen und meta-Tags.
6. **Die Regeln aus `CLAUDE.md`** zu Alarm, Entwarnung, Nachsendung,
   Demo-Modus, Arbeitsmodus (stumm, dauerhaft stumm, Rückschaltung),
   Empfängerliste (Versionen und ids nie wiederverwenden), Befehlen,
   Meldungen an den Admin, Verlauf (Ereignisse, Felder, Rollen), Anmeldung
   und Sitzungen, Bremsen, Proxy-Vertrauen. Abschnitt 7 nennt die Regeln,
   die entfallen oder sich ändern – alle anderen gelten unverändert.
7. **Texte:** alle Meldungen, Bark-Texte (ASCII) und Seiten wortgleich.
   Ausnahme: Texte, die PHP, Nginx oder `config.php` nennen, sagen von Anfang
   an das Richtige (`config.toml`, `systemctl status fw-alarm`).
8. **Verlauf und Ereignisnamen** (`nas_alarm`, `nas_web_failed` …) bleiben,
   auch wo „nas“ historisch ist – die Oberfläche und alte Einträge hängen
   daran.

## 5. Ansätze

**A – Getreue Übertragung (Entwurf vom 16.11.).** Gleicher Datenordner, PHP
als Rückweg ohne Datenverlust. Sicherster Rückweg, schlechtester Code:
ungetypte Daten, „lesen wie PHP“, Sperrdateien, ein Viertel der Aufgaben nur
für die Verträglichkeit. Verworfen: widerspricht dem Ziel des Nutzers.

**B – Innen neu, einfache Dateien (gewählt).** Ein Prozess besitzt seinen
Zustand im Arbeitsspeicher und sichert ihn als wenige, streng geprüfte
JSON-Dateien; der Wächter läuft im selben Prozess jede Minute. Vorhandene
Daten werden einmal übernommen. Preis: die Übernahme ist ein eigener,
einmaliger Fehlerherd (§9, §11), die Tests müssen aufhören, Dateinamen zu
kennen (§11), und ein Zurückschalten auf PHP verliert, was seit dem
Umschalten geändert wurde (§12).

**C – Innen neu mit Datenbank (SQLite).** Echte Transaktionen und
Abfragen. Verworfen: für zehn Empfänger, eine Box und einen Verlauf von
wenigen MB mehr Maschinerie als Nutzen; die reine Go-Fassung von SQLite ist
eine sehr große Fremdbibliothek (mehr zu pflegen und zu prüfen), die andere
braucht einen C-Übersetzer für arm64; im Notfall lässt sich eine
Datenbankdatei nicht mit einem Texteditor ansehen oder reparieren.

Gewählt von mir (der Nutzer hat das Wie ausdrücklich überlassen): **B**.

## 6. Aufbau

### 6.1 Ein Programm, mehrere Aufrufe

Go-Modul im Repo-Wurzelordner (Modulname `fwalarm`), ein Programm
`cmd/fw-alarm`:

| Aufruf | Zweck |
|---|---|
| `fw-alarm serve` | der Server (Dauerbetrieb unter systemd) |
| `fw-alarm check` | bittet den LAUFENDEN Server um einen vollen Prüf-Lauf jetzt und gibt dessen Zeile aus (`WEB_OK \| OK \| …`) |
| `fw-alarm health` | fragt den laufenden Server nach seinem Zustand (läuft, Stand, Speicher in Ordnung, Konfiguration gültig); Rückgabe 0/1 für Skripte |
| `fw-alarm flush` | wartet, bis alles Ausstehende auf der Platte liegt (vor einer Sicherung; der Prüfstand liest danach die Dateien) |
| `fw-alarm stop` | beendet den laufenden Server geordnet – derselbe Weg wie SIGTERM (unter Windows gibt es kein SIGTERM; der Prüfstand braucht das) |
| `fw-alarm check-config` | prüft die Konfiguration, nennt Fehler ohne Geheimnisse |
| `fw-alarm check-state` | prüft, ob DIESES Programm den Zustand im Datenordner lesen kann (vor jedem Einspielen, §13) |
| `fw-alarm hash-password` | fragt ein Passwort ab (ohne Echo) und gibt den Hash für die Konfiguration aus |
| `fw-alarm report-failure` | von systemd nach einem Absturz gestartet: eine Bark-Meldung an den Admin (§6.7) |
| `fw-alarm import-php` | einmalige Übernahme der PHP-Daten (§9); wird beim Aufräumen entfernt |
| `fw-alarm version` | Git-Stand des Baus |

### 6.2 Zustand im Arbeitsspeicher, ein Schreiber

- Der gesamte Zustand ist ein **unveränderlicher, getypter Schnappschuss**
  hinter einem atomaren Zeiger. Leser (Alarm, Poll, Status, Oberfläche)
  nehmen den Zeiger und warten auf nichts – auch nicht auf einen hängenden
  Schreiber oder eine hängende Platte.
- Geändert wird an EINER Stelle: `Update(func(entwurf) …)`. Ein Schreiber
  zur Zeit: Kopie ziehen → ändern → Invarianten prüfen → sichern → Zeiger
  umsetzen → Verlaufs-Einträge anhängen. Es gibt keine Sperrdateien und
  keine Reihenfolge-Regeln; eine Änderung über mehrere Teile (Demo-Modus
  und Listen-Version) ist von selbst unteilbar.
- **Kein Bark-Versand innerhalb einer Änderung** – erst danach.
- Zwei Klassen von Daten, **jede mit eigenem Halter** – eine hängende
  Platte in der einen darf die andere nicht aufhalten:
  - **Erst sichern, dann gelten** („fest“): Empfängerliste samt Versionen
    und ids, Demo-Modus, Einstellungen, Meldungs-Einstellungen, Befehle
    (anlegen, ausliefern, quittieren, abbrechen, verfallen). Scheitert das
    Sichern, gilt die Änderung nicht, und der Aufrufer bekommt den bekannten
    Fehler (`code: storage`, derselbe Satz wie heute). **Jede solche
    Änderung hat eine Frist:** 1,5 s für Aufrufe der Box, 5 s für die
    Oberfläche. Läuft sie ab, antwortet der Server, als wäre das Sichern
    gescheitert (die Box bekommt trotzdem 200, siehe unten).
  - **Gilt sofort im Speicher, gesichert wird nebenher** („flüchtig“):
    letzter Status der Box, gesehene Boards und Alarme, Lebenszeichen des
    Außen-Wächters, Merker des Wächters (gemeldet/erinnert), Bremsen-Zähler
    und **Sitzungen** (anlegen, `last_seen`, abmelden). Ein eigener
    Hintergrund-Schreiber sichert den jeweils neuesten Stand (höchstens ein
    Schreibvorgang zur Zeit, der neueste gewinnt); **keine Anfrage wartet
    darauf**, ein Fehlschlag hält nichts auf. Auch der Verlauf wird so
    angehängt.
- Daraus folgt für die Box (Zeitlimit 4 s, Brücke 3 s):
  - Status-Push und `alive.php` fassen nur Flüchtiges an: Antwort sofort,
    immer 200.
  - Poll mit einem wartenden Befehl: „ausgeliefert“ wird gesichert; nur wenn
    das **innerhalb der Frist gelingt**, trägt die Antwort den Befehl. Sonst
    200 `type=NONE` mit `keys_version`. Gelingt das Sichern erst nach der
    Frist, gilt der Befehl als ausgeliefert und wird nie wieder gesendet
    (in der Oberfläche „ausgeliefert, nicht quittiert“) – höchstens einmal,
    auch über einen Neustart.
  - Quittung: Antwort immer `OK`; lässt sie sich nicht sichern, bleibt der
    Befehl als „ausgeliefert“ stehen.
- **Anmelden geht immer**, auch wenn nichts gesichert werden kann und auch
  im Notbetrieb (§6.3) – sonst käme in genau dieser Lage niemand an den
  Knopf „Alarm an alle“ (Störung 10/2026). Eine Sitzung, die nicht gesichert
  werden konnte, gilt bis zum nächsten Neustart. Bewusst in Kauf genommen:
  Scheitert das Sichern einer Abmeldung, lebt diese Sitzung nach einem
  Neustart wieder auf (der Browser hat ihr Cookie da schon gelöscht).
- Faustregel für alles Weitere: **Ein Neustart verliert nichts** (ein
  geordnetes Beenden schreibt alles Ausstehende), **eine kaputte oder
  hängende Platte stoppt weder Alarm noch Box noch Anmeldung** (gelesen wird
  aus dem Speicher).

### 6.3 Dateien

Ordner `/var/lib/fw-alarm` (von systemd als `StateDirectory` angelegt,
Benutzer `fwalarm`, Rechte 0700):

| Datei | Inhalt |
|---|---|
| `state.json` | die „festen“ Daten, ein Dokument mit Schema-Nummer |
| `state.prev.json` | der Stand vor dem letzten Sichern |
| `runtime.json` | die „flüchtigen“ Daten |
| `sessions.json` | Sitzungen, Schlüssel = SHA-256 der Sitzungs-ID |
| `history.jsonl` | der Verlauf, eine JSON-Zeile pro Ereignis, Felder wie heute (`event`, `log_at` …); ab 5 MB nach `history.1.jsonl` |

- Sichern: Datei daneben schreiben, `fsync`, umbenennen, Ordner `fsync`.
- Einlesen streng: unbekannte Felder und falsche Typen sind ein Fehler.
  Jede Datei trägt `schema`; ein älteres Schema hebt das Programm beim
  Start an (und legt vorher einmal `state.schema-<n>.json` ab).
- `state.json` nicht lesbar → `state.prev.json`. Beides nicht lesbar (oder
  Schema neuer als das Programm): Der Server **startet trotzdem** im
  Notbetrieb – Alarm an die Not-Liste aus der Konfiguration
  (`bark_keys_alarm`), `keys_list` antwortet `code=keys_corrupt` (die
  Oberfläche kennt das), jede Änderung an den festen Daten wird abgelehnt,
  der Admin wird gewarnt. Anmelden und Auslösen gehen weiter. Der Box meldet
  Poll wie heute `keys_version=0` – sie behält dann die Liste, die sie
  gespeichert hat. Die kaputte Datei wird nie überschrieben.
- `runtime.json`/`sessions.json` nicht lesbar → leer anfangen und warnen
  (Box gilt als „noch nie gesehen“, alle melden sich neu an).

### 6.4 HTTP

Standardbibliothek (`net/http` mit Methoden-Mustern), kein Framework.
Zeitlimits (Kopfzeilen 10 s, Lesen 30 s, Schreiben 150 s), Rumpf höchstens
64 kB, Sicherheits-Kopfzeilen an jeder dynamischen Antwort wie heute,
`Cache-Control: no-store`. Formularfelder aus urlencoded UND multipart, bei
doppelten Feldern zählt das letzte, die Reihenfolge der Felder bleibt
erhalten (der Status-Push behält die ersten 40). Statische Dateien aus dem
eingebetteten `assets/` mit fester Typ-Tabelle; der Versions-Parameter `?v=`
ist ein Inhalts-Hash als Dezimalzahl (die Szenarien verlangen Ziffern).
Seiten über `html/template`. Zugriffsprotokoll nach journald: Zeit, Methode,
**Pfad ohne Query**, Status, Dauer, `bridge=0|1`, User-Agent – nie ein Token
oder Key.

Der Server lauscht auf einem von systemd übergebenen Port
(`fw-alarm.socket`, `127.0.0.1:8080`): Bei einem Neustart des Dienstes geht
keine Anfrage verloren. Beim Beenden laufen offene Anfragen, ein laufender
„Alarm an alle“ und ausstehende Bark-Meldungen bis zu 40 s zu Ende.

### 6.5 Prüf-Lauf im selben Prozess (ersetzt den Timer)

Zwei Takte:

- **Voller Lauf alle 15 min** (`check_interval_seconds`, Standard 900) und
  bei `fw-alarm check`: dieselben Abschnitte und Regeln wie heute
  `cron/check_offline.php` – Speicher-Selbsttest → Box offline/wieder da →
  Außen-Wächter verstummt → Erinnerungen (Demo, Stumme täglich, dauerhaft
  Stumme wöchentlich) → Box-Gesundheit → Wochenbericht; dazu überfällige
  Stummschaltungen zurückschalten, verfallene Befehle abräumen, abgelaufene
  Sitzungen löschen. Die drei Ausfall-Abläufe bleiben getrennt (eigene
  Merker), wie `CLAUDE.md` es verlangt. Regeln, die Läufe ZÄHLEN („ab 2
  Fehlschlägen in Folge“ bei „Dashboard DEFEKT“ und bei der Box-Gesundheit),
  zählen volle Läufe – wie heute also rund 15–30 min, nicht 2 min.
- **Jede Minute dazwischen nur „Box offline/wieder da“** – die wichtigste
  Meldung kommt damit höchstens 1 min nach der eingestellten Schwelle statt
  bis zu 15 min danach. Sonst ändert sich an keinem Zeitpunkt etwas.

Weiter gilt:

- Die Zeile eines vollen Laufs (`WEB_OK | OK | …`, dieselben Wörter wie
  heute) geht ins Journal und an `fw-alarm check`.
- `cron_age_s` in `alive.php` = Alter des letzten abgeschlossenen vollen
  Laufs. Hängt er, meldet es der Außen-Wächter wie heute.
- `check_interval_seconds = 0` schaltet die selbsttätigen Läufe (beide
  Takte) ab – nur für Tests, die jeden Lauf einzeln auslösen. `check-config`
  meldet das als Problem, die Oberfläche zeigt den Wächter dann als stehend.
- **Keine Geister nach einer Auszeit des Servers:** Der Server schreibt jede
  Minute ein Lebenszeichen in `runtime.json`. War er beim Start länger als
  90 s weg, zählen „Box schweigt“ und „Außen-Wächter schweigt“ erst ab dem
  Wiederanlauf – solange er aus war, konnte ihn ja niemand erreichen.
- Überfällige Stummschaltungen rechnet die Leseseite weiter sofort als laut
  (eine Definition); zurückgeschaltet wird im Prüf-Lauf UND wie heute vor
  den Antworten von Poll, `api/keys.php`, Kurzbefehl-Link und `keys_list`.

### 6.6 Steuer-Zugang nur lokal

`fw-alarm check`, `health`, `flush` und `stop` sprechen den Server über einen
Unix-Socket an (`/run/fw-alarm/control.sock`, Rechte 0600, Benutzer
`fwalarm`; Pfad einstellbar). Zugang hat, wer die Datei öffnen darf – der
öffentliche Port kennt keine internen Adressen mehr. `api/health.php` und
`health_check_url` entfallen damit.

### 6.7 Wenn der Server selbst ausfällt

Ohne eigenen Wächter-Prozess braucht es andere Netze:

1. **systemd startet neu** (`Restart=always`, ohne Obergrenze, mit
   wachsendem Abstand).
2. **systemd-Watchdog:** Alle 30 s ruft das Programm den eigenen Port auf
   (eine Anfrage wie von außen, Zeitlimit 5 s) – unabhängig vom Prüf-Lauf
   und von Bark, damit ein langsamer Bark-Versand nie wie ein Hänger
   aussieht. Nur wenn sie antwortet, meldet es sich bei systemd; bleibt das
   3 min aus, beendet systemd den hängenden Prozess und startet neu.
3. **Meldung nach Absturz:** `ExecStopPost` startet `fw-alarm
   report-failure`. Endete der Dienst nicht sauber, geht EINE Bark-Meldung
   an den Status-Empfänger („Dashboard-Dienst abgestuerzt, Neustart
   laeuft“), höchstens eine pro 30 min (Merker im Datenordner). Liest nur
   die Konfiguration, braucht den Server nicht.
4. **Außen-Wächter auf dem NAS** – unverändert: nicht erreichbar oder
   Prüf-Lauf zu alt.

### 6.8 Konfiguration

`/etc/fw-alarm/config.toml` (root:fwalarm 0640), TOML mit Kommentaren,
dieselben Schlüsselnamen wie `config.php` (die Texte der Oberfläche nennen
sie). Neu: `state_dir`, `listen` (nur ohne systemd-Socket),
`control_socket`, `check_interval_seconds`. Entfallen: `data_dir`,
`health_check_url`. Unbekannte Schlüssel sind ein Fehler. Fehlt ein
Schlüssel, gilt der Wert, den der PHP-Code heute dafür nimmt (das `?? …`),
nicht der Wert der alten Vorlage – das Verhalten soll sich beim Umschalten
nicht ändern.

Eine Änderung wirkt ohne Neustart: Der Server prüft bei jedem Zugriff
Änderungszeit und Größe der Datei und liest sie neu. Ist sie fehlerhaft,
gilt die letzte gute weiter (Fehler im Journal und als Hinweis in
`fw-alarm health`). Nach jeder Änderung: `fw-alarm check-config`.

Passwörter: Neue Hashes erzeugt `fw-alarm hash-password` als **Argon2id**;
die vorhandenen bcrypt-Hashes (`$2y$`) gelten weiter. Höchstens zwei
Passwortprüfungen laufen gleichzeitig (Argon2id braucht Speicher; 1-GB-VM).

### 6.9 Pakete (Vorschlag; Feinschnitt macht der Plan)

| Paket | Inhalt |
|---|---|
| `config` | TOML laden, prüfen, bei Änderung neu laden |
| `model` | die Datentypen und die **reinen Regeln**: effektive Liste, stumm/überfällig/dauerhaft, Versionen und ids, Demo-Schutzregeln, Lebenslauf eines Befehls, Auswertung eines Status-Pushs (Relaisalarm, Neustart, veralteter Push, fremdes Board) |
| `store` | Schnappschuss, `Update`, Sichern/Laden, Verlauf, Speicher-Selbsttest |
| `bark` | Versand an viele Keys in Runden, ASCII, Feldsätze |
| `notify` | Arten und Einstellungen der Admin-Meldungen, Ruhezeit, Versand |
| `watch` | Prüf-Lauf, Zeitgeber, Texte, Wochenbericht, systemd-Watchdog |
| `web` | HTTP-Rahmen, Sitzungen, Anmeldung, Bremsen, Seiten, statische Dateien |
| `box`, `mutelink`, `dashapi`, `alive` | die Handler |
| `control` | Unix-Socket: Server und Aufrufer |
| `logview` | Tabelle der Verlaufs-Ereignisse, Filter, Ausgabe pro Rolle |
| `phpimport` | die einmalige Übernahme (§9), wird danach gelöscht |

Abhängigkeiten: Standardbibliothek, `golang.org/x/crypto` (Argon2id,
bcrypt) und `github.com/BurntSushi/toml` (ohne eigene Abhängigkeiten).
Beide im Repo mitgeliefert (`vendor/`). `CGO_ENABLED=0`, Zeitzonen-Daten
eingebaut, Go-Version in `go.mod` festgeschrieben.

## 7. Regeln aus CLAUDE.md, die entfallen oder sich ändern

Alles, was hier NICHT steht, gilt unverändert.

| Heute (PHP) | Neu |
|---|---|
| Dateien `<name>.php` mit Guard-Zeile, `data_path()`, `fix_owner()` | entfällt (Dateien liegen außerhalb jedes Webroots, es gibt keinen) |
| `mutate_alarm_keys()`, `backup_then_write()`, `.bak`-Rückfall | ein Dokument, unteilbar gesichert, `state.prev.json` als Rückfall, sonst Notbetrieb (§6.3) |
| Versionen/ids über Hochwassermarken, auch die von der Box gemeldete Version | **bleibt als Regel**, liegt in `state.json` |
| `demo_set`: Schreibreihenfolge, Zurückrollen, `code=inconsistent`, `repair_broken_demo_mode()`, Lesereihenfolge in `api/keys.php`, Ereignis `demo_repaired` | entfällt – Demo-Modus und Listen-Version ändern sich in einem Schritt; der Fall kann nicht mehr entstehen. `demo_repaired` bleibt in der Tabelle der Verlaufs-Ereignisse, damit alte Einträge weiter angezeigt werden |
| Sperren und ihre Reihenfolge, `with_lock()`/`with_lock_soft()`, „nie unter gehaltener `keys`-Sperre“ | entfällt (ein Schreiber); es bleibt: kein Bark-Versand innerhalb einer Änderung |
| Status-Push: HTTP 500, wenn der Datenordner klemmt | Status-Push gilt im Speicher und wird mit 200 bestätigt |
| Poll bei klemmendem Datenordner: je nach Ursache 500 oder `type=NONE` | immer 200 `type=NONE` mit `keys_version`; ein Befehl wird nur ausgeliefert, wenn „ausgeliefert“ innerhalb der Frist gesichert ist (§6.2) |
| `api/health.php`, `health_check_url`, Schritte `lock/write/read/delete/perm`, `inplace_data_files()`, `blocks_pushes` | Speicher-Selbsttest im vollen Prüf-Lauf (schreiben, zurücklesen, löschen im Datenordner; dazu das Ergebnis des letzten Sicherns). „Dashboard DEFEKT“ ab 2 vollen Läufen in Folge, Erinnerung alle 24 h, einmal „wieder OK“ – Ereignisse `nas_web_failed`/`nas_web_recovered` bleiben. Der Text (ASCII) sagt: was scheitert (Schritt), was weiterläuft (Alarm, Status der Box, Anmeldung), was nicht (Änderungen werden nicht gespeichert, Befehle an die Box nicht ausgeliefert), wo nachsehen (`journalctl -u fw-alarm`, Platz und Rechte von `/var/lib/fw-alarm`) |
| „Box OFFLINE“ gibt dem Server die Schuld, wenn `blocks_pushes` | dieser Zweig entfällt (Pushs kommen auch bei kaputter Platte an); der Zweig „NAS zu Hause“ (`via_bridge`) bleibt |
| `watcher_unknown_at`, Ersatz-Ablage des Wächter-Zustands im Temp-Ordner, „`alive.php` lauffähig mit alten `common.php`-Ständen“ | entfällt (Merker liegen im Speicher) |
| Wächter als eigener Prozess am Timer alle 15 min, als root auf dem NAS | voller Prüf-Lauf im Server alle 15 min, „Box offline/wieder da“ jede Minute (§6.5); Ausfall des Servers: §6.7 |
| Stille von Box und Außen-Wächter zählt ab dem letzten Lebenszeichen | nach einer Auszeit des Servers (> 90 s) erst ab dem Wiederanlauf (§6.5) |
| `log_cache.json` | entfällt |
| `defer_after_response()`/`fastcgi_finish_request()` | Arbeit nach der Antwort läuft nebenher; beim Beenden wird darauf gewartet |
| PHP-Sitzungen (`gc_maxlifetime`, `use_strict_mode`) | eigene Ablage; alle Regeln der Sitzung bleiben |
| Texte mit „config.php“, „PHP-cURL“, `systemctl status nginx php8.3-fpm` | nennen `config.toml` bzw. `systemctl status fw-alarm` |
| `classic.php`, `assets/classic/` | entfällt vorher (Aufgabe 3 des alten Plans) |

## 8. Sitzungen und Anmeldung

Regeln wie heute: Cookie `boss925_dashboard` (`HttpOnly`, `SameSite=Strict`,
`Secure` laut vertrautem Proxy, 12 h, bei jeder angemeldeten Anfrage
erneuert); Sitzung nur für Angemeldete; an die Zugangsdaten gebunden
(Passwort ändern meldet alle Sitzungen dieses Kontos ab); neue ID bei jeder
Anmeldung; Abmelden löscht Sitzung und Cookie; `Sec-Fetch-Site`; Bremse pro
Absender VOR der Passwortprüfung; genau eine Passwortprüfung pro Versuch,
auch für unbekannte Benutzer; 1 s Pause nach einem Fehlversuch.

Sitzungs-ID: 32 Zufallsbytes (`crypto/rand`). Abgelegt wird nur ihr
SHA-256. Sitzungen überstehen einen Neustart – das Handy bleibt nach einem
Update angemeldet. `last_seen` höchstens einmal pro Minute.

## 9. Übernahme der vorhandenen Daten

`fw-alarm import-php --data <alter Datenordner> --config-json <Datei>
--out-state <neuer Ordner> --out-config <config.toml>`. Die `config.php`
wird vorher auf der VM mit PHP nach JSON ausgegeben (PHP ist dann noch da).

- Nur hier wird nachsichtig gelesen (`[]` statt Objekt, `1`/`"1"`/`true`,
  Zahlen als Zeichenketten, kaputte Einträge) – im Paket `phpimport`, das
  nach dem Umschalten gelöscht wird.
- Übernommen wird, was Verhalten trägt:
  - Empfänger mit `id`, Key, Name, Telefon, Stumm-Zustand samt Zeitpunkt,
    Lautstärke, „dauerhaft“, **Kurzbefehl-Token**;
  - Listen-Version, Hochwassermarken für Version und ids;
  - Demo-Modus samt Zeitpunkt;
  - Einstellungen und Meldungs-Einstellungen;
  - Befehle: nächste Nummer, laufender Befehl, zuletzt gesehene Typen (sonst
    führte die Box einen Befehl mit bekannter Nummer nicht aus oder einen
    alten erneut);
  - letzter Status, gesehene Alarme und Boards (sonst schriebe der erste
    Push nach dem Umschalten einen Geister-Relaisalarm oder -Neustart);
  - Merker des Wächters (sonst kämen „OFFLINE“/Erinnerungen ein zweites
    Mal), Lebenszeichen des Außen-Wächters;
  - der Verlauf (beide Dateien), Zeile für Zeile.
- Nicht übernommen: Sitzungen (alle melden sich einmal neu an), Bremsen,
  `log_cache`, Sperrdateien, `.bak`.
- Schreibt nie in den alten Ordner. Weigert sich, einen vorhandenen neuen
  Zustand zu überschreiben (außer `--force`). Gibt eine Zusammenfassung aus
  (Anzahlen; was verworfen wurde und warum) – ohne Keys und Tokens.
- Ist die alte Liste samt `.bak` unlesbar, bricht die Übernahme ab.
- Ist `demo_mode.json` kaputt oder fehlt sie trotz `.bak`, übernimmt sie
  LIVE **und erhöht die Listen-Version um eins** (die Box könnte noch die
  Demo-Liste halten und muss neu abholen) – mit Hinweis in der
  Zusammenfassung.
- Sie setzt das Lebenszeichen des Servers auf „jetzt“, damit der Start
  danach nicht als Auszeit zählt.
- Die Ersatz-Ablage des PHP-Wächters im Temp-Ordner und `watcher_unknown_at`
  liest sie nicht: Umgeschaltet wird nur aus einem gesunden Zustand (§12),
  in dem beide leer sind.

## 10. Sicherheit

Hundert Prozent gibt es nicht; das Ziel ist, dass jeder bekannte Weg
geschlossen oder bewusst in Kauf genommen und aufgeschrieben ist.

**Wovor geschützt wird:** Scanner und Angreifer aus dem Internet (über den
Cloudflare-Tunnel); Durchprobieren von Passwörtern und Kurzbefehl-Tokens;
ein weitergegebener Kurzbefehl-Link; ein verlorenes, angemeldetes Handy;
fremde Seiten, die im Browser des Admins Anfragen auslösen; ein anderer
lokaler Benutzer auf der VM; Fehler in Fremdbibliotheken.

**Maßnahmen**

- Geheimnisse (Maschinen- und Wächter-Token, Passwörter, Sitzungs-IDs,
  CSRF-Tokens, Kurzbefehl-Tokens) werden in konstanter Zeit verglichen,
  kommen aus `crypto/rand` und stehen nie in einem Protokoll, einer
  Fehlermeldung, einer Weiterleitung oder einer Adresse, die der Server
  baut. Bark-Keys nur maskiert.
- Jede Anfrage hat Größen- und Zeitlimit. Kein Dateipfad entsteht aus
  einer Eingabe. HTML nur über `html/template`. CSP ohne `unsafe-inline`.
- Ändernde Aktionen nur per POST mit CSRF-Token; Anmeldung mit
  `Sec-Fetch-Site`-Regel; Rollen serverseitig an EINER Tabelle.
- Kopfzeilen eines Proxys zählen nur von vertrauten Absendern.
- Dienst unter systemd eingesperrt: eigener Benutzer, kein Zugriff außer
  auf Daten-, Konfigurations- und Laufzeitordner, keine neuen Rechte, keine
  Capabilities, eingeschränkte Systemaufrufe und Adressfamilien,
  Speicher-Obergrenze, `UMask=0077`.
- Bau: nachvollziehbar aus einem Git-Stand (`-trimpath`), auf dem PC;
  Einspielen vergleicht die Prüfsumme auf der VM mit der des PCs; nichts
  über `/tmp`.
- Prüfungen bei jeder Aufgabe: `gofmt`, `go vet`, `staticcheck`,
  `govulncheck`; Fuzz-Tests für alles, was Eingaben von außen zerlegt
  (Formulare, Konfiguration, Zustandsdateien, Verlaufszeilen, die Übernahme).
- Vor dem Umschalten: Sicherheits-Durchsicht in einer eigenen Sitzung.

**Bewusst in Kauf genommen (außerhalb dieser Umstellung):** Die Box prüft
keine TLS-Zertifikate (Firmware); Cloudflare beendet TLS und sieht Tokens
und Keys; Bark sieht Alarmtexte und Geräte-Keys; Anmeldung nur mit Passwort,
bis Passkeys folgen; Kurzbefehl-Links sind Geheimnisse in einer Adresse; wer
das Maschinen-Token hat, kann die Empfänger-Keys abrufen (die Box braucht
sie).

## 11. Prüfungen

1. **Tests kennen keine Dateien mehr.** Der Prüfstand bekommt Helfer, die
   sagen, WAS sie wollen („Empfänger 3 ist seit 13 h stumm“, „letzter Status
   ist 200 s alt“, „Sichern scheitert“), und jeder Treiber weiß, WO das
   liegt. Zuerst werden die Tests so umformuliert und müssen gegen PHP grün
   bleiben (Beweis, dass die Umformulierung nichts verändert hat); dann
   bekommt Go seinen Treiber. Der Go-Treiber ändert Zustand, indem er den
   Server geordnet anhält (`fw-alarm stop`), die Dateien ändert und auf
   demselben Port neu startet (mit Wiederholung, bis der Port frei ist) –
   das geht, weil ein Neustart nichts verliert, und es prüft genau das mit.
   Zum Lesen ruft er vorher `fw-alarm flush`. Keine Test-Haken im Programm:
   Getestet wird, was ausgeliefert wird. Auch Sitzungen spricht der
   Prüfstand über einen Helfer an („diese Sitzung ist seit 13 h untätig“),
   nicht über einen Ordner.
2. **Was nur PHPs Ablage prüft** (`.bak`, kaputte Datei, Sperre als Ordner,
   `health.php`) wandert in eine Datei, die nur gegen PHP läuft und mit PHP
   verschwindet. Für jede dieser Regeln nennt §7 das Gegenstück; das
   Gegenstück bekommt einen Go-Test. Ebenso die zwei Fälle in
   `tests/dashboard/wording.test.mjs`, die PHP-Quelltext ausführen: Ihre
   Texte prüft ein Go-Test; die Wortlaut-Regeln („Server“ statt „NAS“)
   lesen zusätzlich die Go-Quellen.
3. **Go allein:** viele Anfragen zugleich (20 Polls → genau einer bekommt
   den Befehl; 30 falsche Anmeldungen bei Grenze 8 → höchstens 8
   Passwortprüfungen; 10 `keys_add` → 10 ids, Version +10); Sichern
   scheitert (Alarm geht raus, Status wird angenommen, Poll liefert keinen
   Befehl aus, Änderungen werden abgelehnt, der Admin wird gewarnt);
   Neustart mitten in „Alarm an alle“ (alle Empfänger werden versorgt, das
   Ergebnis steht im Verlauf); Neustart verliert nichts; kaputte
   Zustandsdatei → Rückfall, dann Notbetrieb; Sommer-/Winterzeit; ein
   Tippfehler in `config.toml` (letzte gute gilt weiter).
4. **Übernahme:** ein künstlicher PHP-Datenordner im Repo mit allem, was
   PHP über die Monate geschrieben hat (alte Formen, kaputte Einträge) →
   erwarteter neuer Zustand. Dann der **Gleichlauf:** PHP auf einer Kopie
   und Go auf der Übernahme derselben Kopie bekommen dieselben Anfragen;
   verglichen werden die Antworten und was an die Bark-Attrappe ging
   (Empfänger, Lautstärke, `call`). Einmal mit dem künstlichen Ordner,
   einmal **auf dem PC mit einer Kopie der echten Daten** (Bark zeigt auf
   die Attrappe; die Kopie liegt außerhalb des Repos und wird danach
   gelöscht).
5. Die sieben Chrome-Szenarien und `tests/dashboard/*.test.mjs` gegen Go.
6. Sicherheits-Durchsicht (eigene Sitzung).
7. **Probe auf der VM** vor dem Umschalten: Go auf einem zweiten Port mit
   einer Übernahme der echten Daten und einem Bark-Ziel, das nichts annimmt;
   Speicherbedarf messen. Läge die VM danach unter 25 % Speicher, wird nicht
   umgeschaltet, sondern mit dem Nutzer entschieden (Oracle stoppt VMs, die
   bei CPU, Netz UND Speicher 7 Tage unter 20 % bleiben).

## 12. Ablauf und Rückweg

1. **Vorarbeiten** (laufen, unabhängig von diesem Entwurf): Tests für die
   Dashboard-API gegen PHP, Chrome-Szenarien über den Prüfstand,
   `classic.php` entfernen.
2. **Tests von Dateien lösen** (§11.1, gegen PHP grün).
3. **Go bauen**, bis §11.1–5 erfüllt sind. Der Betrieb merkt nichts.
4. **Sicherheits-Durchsicht, Probe auf der VM, Messung.**
5. **Umschalten** (mit dem Nutzer, ca. 30 min; nicht in den 3 Tagen vor oder
   nach einem Flashen der Box). Nur aus einem gesunden Zustand: die letzte
   Zeile des PHP-Wächters beginnt mit `WEB_OK` und enthält kein
   `STATE_WRITE_FAILED`, der Demo-Modus ist aus, kein Alarm läuft. Dann:
   Sicherung; Nginx und PHP-FPM anhalten;
   Übernahme laufen lassen; `fw-alarm` auf Port 8080 starten; den alten
   Timer abschalten. Die Box wiederholt ihre Aufrufe von selbst. Danach nur
   harmlose Prüfungen (Status der Box, TEST-Befehl, eigener Kurzbefehl-Link,
   ein Prüf-Lauf); alle melden sich neu an. Dann, gemeinsam: Demo-Modus auf
   den Nutzer selbst, **ein Alarm und eine Entwarnung nur an ihn**, zurück
   auf LIVE.
   **Rückweg:** `fw-alarm` anhalten, Nginx/PHP-FPM und Timer wieder starten
   – Sekunden. PHP arbeitet dann auf seinen alten, unberührten Daten weiter;
   was seit dem Umschalten geändert wurde (Stumm-Zustände, neue Empfänger,
   Verlauf), fehlt dort. Die Box holt sich die Liste von selbst neu.
6. **Aufräumen, nach 3 ruhigen Tagen:** PHP und Nginx von der VM, alte
   Daten nach einer letzten Sicherung löschen; im Repo: alle PHP-Dateien,
   der PHP-Treiber der Tests, die PHP-only-Tests, `phpimport` und der
   Gleichlauf gehen; `nas_dashboard/` wird zu `web/` (nur noch Oberfläche);
   `CLAUDE.md`, READMEs und Runbook werden neu geschrieben. Danach: Passkeys.

## 12a. Danach: die Firmware (eigener Entwurf, nicht Teil dieses Plans)

Der Nutzer hat am 17.11. Änderungen an der Firmware freigegeben; flashen
kann er in ca. 4 Wochen (etwa Mitte Dezember 2026). Das Umschalten des
Servers wartet NICHT darauf – der neue Server bedient die heutige Firmware
über die Brücke unverändert. Für den Flash vorgesehen, mit eigenem kurzem
Entwurf vorab (es ist das Alarmgerät, und ein Flash muss sitzen):

- Die Box prüft die Zertifikate von Server und Bark (heute nicht – die
  größte verbleibende Sicherheitslücke). Offen und Sache des Nutzers: was
  die Box tut, wenn eine Prüfung beim ALARM-Versand scheitert.
- Die Box ruft den Server direkt auf sauberen Adressen. Danach entfallen die
  Box-Pfade der Brücke, die Sonderfälle für die alte Firmware und die
  `.php`-Adressen der Box. Die Kurzbefehl-Links behalten ihre Adresse für
  immer.

## 13. Betrieb

- Units: `fw-alarm.socket` (Port 8080), `fw-alarm.service` (Benutzer
  `fwalarm`, `StateDirectory`, `RuntimeDirectory`, `ConfigurationDirectory`,
  Härtung laut §10, `WatchdogSec=180`, `ExecStopPost` laut §6.7).
  `cloudflared` bleibt unverändert.
- `server/deploy.sh <rev>`: baut auf dem PC für `linux/arm64` aus genau
  diesem Git-Stand, sichert zuerst, lädt in einen Ordner im Home der VM,
  vergleicht die Prüfsumme, lässt das NEUE Programm `check-config` und
  `check-state` gegen die echten Dateien laufen (scheitert eines, bleibt das
  alte Programm – so landet auch ein Zurückgehen auf einen älteren Stand
  nie im Notbetrieb), tauscht das Programm in einem Schritt, startet neu,
  fragt `fw-alarm health` und `fw-alarm version`. Letzte Zeile:
  `deployed <sha>: fw-alarm sha256 <…> ok`.
- `server/backup.sh` holt `/var/lib/fw-alarm` und
  `/etc/fw-alarm/config.toml`; ein Archiv zählt erst nach der Prüfung
  (lesbar, Empfängerliste und Konfiguration enthalten). Zurückspielen =
  Dienst anhalten, Ordner ersetzen, starten.
- `server/check.sh` prüft von innen (`fw-alarm health`, Rechte der Ordner,
  Stand = eingespielter Stand) und von außen (wie die Box: TLS 1.2, ohne
  `Accept`).
- Das Runbook (`server/README.md`) in einfachen Worten: was Pflicht ist, was
  freiwillig, genaue Befehle, Tabelle „Meldung → wo nachsehen“.

## 14. Was dem Umsetzenden überlassen bleibt

Feinschnitt der Pakete und Namen; Form der Fehlerwerte; die genaue Liste
der Prüfstand-Helfer (abgeleitet aus den rund 100 heutigen Stellen); Details
der systemd-Härtung; wie der Gleichlauf Antworten normalisiert; ob
versionierte statische Dateien länger gecacht werden dürfen (heutiges
Verhalten ist die Untergrenze).

## 15. Festlegungen, die ich getroffen habe (bitte prüfen)

1. Neubau innen (Ansatz B); außen bleibt alles gleich (§4).
2. Ablage als wenige JSON-Dateien, keine Datenbank.
3. Der Wächter läuft im Server statt als eigener Dienst am Timer: voller
   Lauf weiter alle 15 min, „Box offline/wieder da“ jede Minute – diese eine
   Meldung kommt dadurch früher, sonst ändert sich kein Zeitpunkt. Fällt der
   Server aus, greifen systemd, eine Absturz-Meldung und der Außen-Wächter
   (§6.7).
4. Rückweg auf PHP nur noch als Notbremse mit Verlust der Änderungen seit
   dem Umschalten; aufgeräumt wird nach 3 ruhigen Tagen statt nach 14.
5. Konfiguration als `/etc/fw-alarm/config.toml`, Daten unter
   `/var/lib/fw-alarm`.
6. Vorhandene Passwörter gelten weiter; neue erzeugt `fw-alarm
   hash-password`.
7. Zwei Fremdbibliotheken (`x/crypto`, `toml`), im Repo mitgeliefert.
8. `nas_dashboard/` heißt am Ende `web/`.
9. Gebaut wird auf dem PC; beim Umschalten melden sich alle einmal neu an;
   Passkeys erst nach dem Aufräumen (unverändert).
10. Die Oberfläche bleibt, wie sie ist (kein TypeScript, kein Build-Schritt).
11. Anmelden, Alarm und die Box laufen auch dann weiter, wenn der Server
    nichts speichern kann; nur Änderungen (Empfänger, Stummschaltung,
    Einstellungen, Befehle an die Box) werden dann abgelehnt.
12. Die Firmware ist ein eigener Schritt danach, passend zum Flash in ca.
    4 Wochen (§12a): Zertifikate prüfen, saubere Adressen.
