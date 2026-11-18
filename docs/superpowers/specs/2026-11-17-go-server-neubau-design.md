# Der Dashboard-Server neu in Go — Design (Neubau statt Übertragung)

Stand: 17.11.2026 – vom Nutzer freigegeben, nach einer unabhängigen
Durchsicht überarbeitet (§16); die Durchsicht des Plans hat einzelne Punkte
nachgeschärft (§17). Ersetzt den ersten Entwurf vom 16.11. (getreue
Übertragung der PHP-Fassung auf demselben Datenordner; er steht samt seinem
Plan in der Git-Historie). Plan dazu:
`docs/superpowers/plans/2026-11-17-go-server-neubau.md`. Betrifft nur die
Server-Seite des Dashboards (`nas_dashboard/` auf der Oracle-VM). Firmware,
NAS-Brücke und Außen-Wächter bleiben, wie sie sind.

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
   Gelesen wird ausschließlich aus dem Arbeitsspeicher – auch die
   Konfiguration (§6.8).
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

- Keine neue Oberfläche: `assets/` bleibt (es ändern sich nur Texte, die
  `config.php` nennen, und es kommen zwei Verlaufs-Texte dazu, §7).
- Keine Änderung an Firmware, `nas_bridge/`, `cloud_watcher/` **in diesem
  Entwurf**. Ausnahme: Beim Aufräumen (§12.6) werden die Texte des
  Außen-Wächters berichtigt, die `config.php`, `check_offline.php` und den
  Timer nennen. Die Firmware darf seit dem 17.11. geändert werden (geflasht
  werden kann erst in ca. 4 Wochen); sie bekommt danach einen eigenen,
  kleinen Entwurf (§12a).
- Keine neuen Funktionen (Passkeys folgen danach, in Go). Einzige Ausnahme:
  die Probe-Sperre (§10) – sie dient allein dazu, dass beim Umschalten kein
  Alarm an jemand anderen als den Admin gehen kann.
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
| PC | Windows, PHP 8.3, Node 24, Go 1.27 (ohne C-Übersetzer, also ohne Race-Detektor) |

Rund ein Drittel der Server-Tests greift heute direkt in die Dateien der
PHP-Fassung (`backend.data.read/edit/write/corrupt/block`, rund 125 Stellen,
vor allem `alarm_keys.json`, `latest_status.json`, `demo_mode.json`, `.bak`).

## 4. Was von außen gleich bleiben muss

Diese Liste ist die Grenze des Neubaus. Alles andere ist frei.

1. **Adressen:** `/` und `/index.php`, `/dashboard_api.php`,
   `/api/{status,poll,ack,keys,mute,alive}.php`, `/manifest.json`,
   `/assets/…`. `/classic.php` leitet mit 302 auf `index.php`. Alles andere
   ist 404 – auch `/api/health.php` (entfällt, §6.6). Jede Adresse
   antwortet für jede Methode so wie heute (§6.4).
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
   `nas_send_failed` in ihrer heutigen Bedeutung. Einzige Abweichung:
   `stored` meldet, ob das LETZTE Sichern der flüchtigen Daten gelang (der
   Aufruf selbst wartet auf kein Sichern mehr) – der Außen-Wächter schreibt
   den Wert nur in sein Protokoll.
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
   an das Richtige (`config.toml`, `systemctl status fw-alarm`) – ebenso
   Texte, die ein Verhalten beschreiben, das §7 ändert. Neue Meldungen und
   Verlaufs-Ereignisse: §7.
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
| `fw-alarm health` | fragt den laufenden Server nach seinem Zustand (läuft, Stand, Speichern in Ordnung, Konfiguration gültig, Notbetrieb ja/nein, Hinweise); Rückgabe 0/1 für Skripte |
| `fw-alarm flush` | wartet, bis alles Ausstehende auf der Platte liegt (vor einer Sicherung; der Prüfstand liest danach die Dateien) |
| `fw-alarm reload` | liest die Konfiguration sofort neu und meldet das Ergebnis (§6.8) |
| `fw-alarm stop` | beendet den laufenden Server geordnet – derselbe Weg wie SIGTERM (unter Windows gibt es kein SIGTERM; der Prüfstand braucht das). Für den Prüfstand und Läufe von Hand; unter systemd `systemctl stop fw-alarm` (sonst startet systemd ihn gleich wieder) |
| `fw-alarm check-config` | prüft die Konfiguration, nennt Fehler ohne Geheimnisse |
| `fw-alarm edit-config` | ändert die Konfiguration sicher: Kopie im Editor, Prüfung, Ersetzen in einem Schritt, `reload` (§6.8) |
| `fw-alarm check-state` | prüft, ob DIESES Programm den Zustand im Datenordner lesen kann. Verlangt eine lesbare `state.json` – der Rückfall auf die Vorfassung zählt nicht (vor jedem Einspielen, §13). Mit `--list`: die Empfänger zum Vergleichen (id, Name, Key maskiert, stumm/dauerhaft), ohne Keys und Tokens |
| `fw-alarm state-bump` | nur bei gestopptem Server: wendet die Rücksprung-Regel an (§6.3) – nach jedem Zurückspielen einer Sicherung |
| `fw-alarm hash-password` | fragt ein Passwort ab (ohne Echo) und gibt den Hash für die Konfiguration aus |
| `fw-alarm report-failure` | von systemd nach einem Absturz gestartet: eine Bark-Meldung an den Admin (§6.7) |
| `fw-alarm import-php` | einmalige Übernahme der PHP-Daten (§9); wird beim Aufräumen entfernt |
| `fw-alarm version` | Git-Stand des Baus |

**Ein Programm pro Datenordner:** `serve`, `state-bump` und `import-php`
halten eine Sperre auf `server.lock` im Datenordner (vom Betriebssystem
gehalten, endet mit dem Prozess). Ein zweiter Start bricht mit einer klaren
Meldung ab – der Aufbau unten setzt voraus, dass genau ein Prozess schreibt.
(Das ist die einzige Sperrdatei; sie regelt nichts zwischen Anfragen.)

### 6.2 Zustand im Arbeitsspeicher, ein Schreiber

- Der gesamte Zustand ist ein **unveränderlicher, getypter Schnappschuss**
  hinter einem atomaren Zeiger. Leser (Alarm, Poll, Status, Oberfläche)
  nehmen den Zeiger und warten auf nichts – auch nicht auf einen hängenden
  Schreiber oder eine hängende Platte.
- „Unveränderlich“ ist in Go nur eine Abmachung. Deshalb als Regel: Jede
  Änderung arbeitet auf einer **tiefen Kopie** – keine Liste und keine
  Tabelle wird zwischen Entwurf und veröffentlichtem Schnappschuss geteilt.
  Ein Test ändert jeden Teil eines Entwurfs und vergleicht den alten
  Schnappschuss; der Race-Detektor läuft bei jedem Push (§10, §11).
- Geändert wird an EINER Stelle: `Update(func(entwurf) …)`. Ein Schreiber
  zur Zeit: Kopie ziehen → ändern → Invarianten prüfen → sichern → Zeiger
  umsetzen. Es gibt keine Reihenfolge-Regeln zwischen Dateien; eine Änderung
  über mehrere Teile (Demo-Modus und Listen-Version) ist von selbst
  unteilbar.
- **Kein Bark-Versand innerhalb einer Änderung** – erst danach.
- Zwei Klassen von Daten, **jede mit eigenem Halter** – eine hängende
  Platte in der einen darf die andere nicht aufhalten:
  - **Erst sichern, dann gelten** („fest“): Empfängerliste samt Versionen
    und ids, Demo-Modus, Einstellungen, Meldungs-Einstellungen, Befehle
    (anlegen, ausliefern, quittieren, abbrechen, verfallen). Scheitert das
    Sichern, gilt die Änderung nicht, und der Aufrufer bekommt den bekannten
    Fehler (`code: storage`, derselbe Satz wie heute).
  - **Gilt sofort im Speicher, gesichert wird nebenher** („flüchtig“):
    letzter Status der Box, gesehene Boards und Alarme, Lebenszeichen des
    Außen-Wächters, Merker des Wächters (gemeldet/erinnert), Bremsen-Zähler
    und **Sitzungen** (anlegen, `last_seen`, abmelden). Ein eigener
    Hintergrund-Schreiber sichert sofort nach einer Änderung den jeweils
    neuesten Stand (höchstens ein Schreibvorgang zur Zeit, Wartendes wird
    zusammengefasst, der neueste gewinnt); **keine Anfrage wartet darauf**,
    ein Fehlschlag hält nichts auf.
- **Fristen für feste Änderungen gelten pro Anfrage** – nicht pro Änderung –
  und schließen das Warten in der Schlange ein: **1,5 s** für Aufrufe der Box
  (Poll, Quittung, `api/keys.php`), **2 s** für den Kurzbefehl-Link (alte
  Links laufen über die Brücke, die nach 3,5 s aufgibt), **5 s** für die
  Oberfläche. Im Einzelnen:
  - Eine Änderung, die bei Ablauf der Frist noch nicht begonnen hat, wird
    **verworfen**. Nur die eine, die gerade schreibt, kann später noch fertig
    werden (ein hängendes `fsync` lässt sich nicht abbrechen).
  - Wird sie nach der Frist doch noch fertig, **gilt sie** (Zeiger umgesetzt,
    Verlauf geschrieben). Der Aufrufer hat dann schon eine Antwort: die Box
    wie unten beschrieben; die Oberfläche `code: storage` mit dem Satz
    „Fehler: Speichern dauert zu lange – bitte den angezeigten Stand prüfen.“
    (nicht „nicht beschreibbar“ – das wäre dann falsch). Scheitert das
    Sichern wirklich, bleibt es beim bekannten Satz.
- **Der Verlauf reist mit der Änderung, zu der er gehört.** Jeder Eintrag
  trägt eine zufällige Kennung (`uid`) und steht zuerst in einer `outbox` im
  selben Dokument, das die Änderung sichert (`state.json` bzw.
  `runtime.json`; Einträge ohne eigene Änderung – z.B. `key_test` – laufen
  über die `outbox` der flüchtigen Daten). Ein eigener Schreiber hängt einen
  Eintrag erst an `history.jsonl` an, wenn sein Dokument gesichert ist; beim
  nächsten Sichern fällt er aus der `outbox`. Nach einem Neustart hängt der
  Server an, was in einer `outbox` steht und unter den letzten 500 Zeilen des
  Verlaufs fehlt. So gibt es weder „gesichert, aber nicht im Verlauf“ noch
  eine doppelte Zeile. Die `outbox` fasst höchstens 500 Einträge (darüber
  fallen die ältesten weg, mit einer Zeile im Journal). **Leser** (`log_view`,
  „Letzter Alarm“, die Zeit einer Nachsendung) lesen über den Speicher: die
  Datei plus alles, was noch nicht angehängt ist.
- **„Alarm gestartet“ steht auf der Platte, bevor die erste Anfrage an Bark
  geht** (`nas_alarm_started`, `false_alarm_started`) – darauf wird höchstens
  300 ms gewartet, eine tote Platte hält den Alarm nicht auf. Sonst führte der
  Hinweis der Oberfläche „erst Verlauf prüfen“ nach einem Absturz in die Irre.
- Daraus folgt für die Box (Zeitlimit 4 s, Brücke 3 s):
  - Status-Push und `alive.php` fassen nur Flüchtiges an: Antwort sofort,
    immer 200.
  - Poll: Erst werden überfällige Stummschaltungen zurückgeschaltet, dann
    ein wartender Befehl als „ausgeliefert“ gesichert – beides zusammen in
    der einen Frist von 1,5 s. Nur wenn „ausgeliefert“ **innerhalb der Frist
    gesichert ist**, trägt die Antwort den Befehl. Sonst 200 `type=NONE` mit
    `keys_version`. Wurde die Änderung verworfen, wartet der Befehl weiter.
    Wurde sie erst nach der Frist fertig, gilt er als ausgeliefert und wird
    nie wieder gesendet (in der Oberfläche „ausgeliefert, nicht quittiert“) –
    höchstens einmal, auch über einen Neustart.
  - Quittung: Antwort immer `OK`; lässt sie sich nicht sichern, bleibt der
    Befehl als „ausgeliefert“ stehen.
- **Anmelden geht immer**, auch wenn nichts gesichert werden kann und auch
  im Notbetrieb (§6.3) – sonst käme in genau dieser Lage niemand an den
  Knopf „Alarm an alle“ (Störung 10/2026). Eine Sitzung, die nicht gesichert
  werden konnte, gilt bis zum nächsten Neustart. Bewusst in Kauf genommen:
  Scheitert das Sichern einer Abmeldung, lebt diese Sitzung nach einem
  Neustart wieder auf (der Browser hat ihr Cookie da schon gelöscht).
- **Ein Fehler in einem Teil reißt nicht alles mit.** Anders als bei PHP
  teilen sich alle Anfragen einen Prozess. Deshalb fängt alles, was das
  Programm nebenher startet (Versand je Empfänger, Prüf-Lauf, Schreiber,
  Arbeit nach der Antwort), einen Laufzeitfehler ab, schreibt ihn ohne Daten
  der Anfrage ins Journal und endet für sich; beendet wird das Programm nur
  in `main`.
- Faustregel für alles Weitere: **Ein geordnetes Beenden verliert nichts**
  (es schreibt alles Ausstehende). **Ein harter Abbruch** (Speichergrenze,
  Watchdog, Ausfall der VM) verliert höchstens Flüchtiges der letzten
  Sekunde – nie eine bestätigte Änderung, nie einen ausgelieferten Befehl,
  nie die Zeile „Alarm gestartet“. **Eine kaputte oder hängende Platte stoppt
  weder Alarm noch Box noch Anmeldung** (gelesen wird aus dem Speicher).

### 6.3 Dateien

Ordner `/var/lib/fw-alarm` (von systemd als `StateDirectory` angelegt,
Benutzer `fwalarm`, Rechte 0700):

| Datei | Inhalt |
|---|---|
| `state.json` | die „festen“ Daten, ein Dokument mit Schema-Nummer |
| `state.prev.json` | der Stand vor dem letzten Sichern |
| `recipients.rescue.json` | Not-Kopie nur für den Alarm: Keys, Namen, Stumm-Zustand, Demo-Empfänger. Nach jeder Änderung an Liste oder Demo-Modus neu geschrieben (ein Fehlschlag hält nichts auf); gelesen NUR im Notbetrieb |
| `runtime.json` | die „flüchtigen“ Daten |
| `sessions.json` | Sitzungen, Schlüssel = SHA-256 der Sitzungs-ID |
| `history.jsonl` | der Verlauf, eine JSON-Zeile pro Ereignis, Felder wie heute (`event`, `log_at` …) plus `uid`; ab 5 MB nach `history.1.jsonl` |
| `config.last-good.toml` | Kopie der zuletzt gültigen Konfiguration (§6.8) |
| `server.lock` | ein Programm pro Datenordner (§6.1) |

- **Sichern:** Datei mit festem Namen daneben schreiben (`<datei>.tmp`),
  `fsync`, **das Geschriebene mit dem strengen Leser zurücklesen und
  vergleichen** (stimmt es nicht überein, gilt die Änderung nicht – die gute
  Datei ist dann noch da), umbenennen, Ordner `fsync`. Bei `state.json` wird
  vor dem Umbenennen die bisherige Datei als `state.prev.json` festgehalten
  (Verweis auf dieselbe Datei, notfalls Kopie) – aber nur, wenn es eine
  `state.json` gibt: Beim allerersten Sichern und beim Sichern direkt nach
  einem Rückfall bleibt eine vorhandene `state.prev.json` unangetastet, bis
  die neue `state.json` liegt. Zu keinem Zeitpunkt fehlen beide. Unter
  Windows entfällt das `fsync` des Ordners, und Umbenennen, Löschen und
  Verweisen werden bis zu 1 s wiederholt, wenn ein Virenscanner die Datei
  hält. Der Verlauf wird je Schub geöffnet, angehängt,
  gesichert und geschlossen.
- **Einlesen streng:** unbekannte Felder und falsche Typen sind ein Fehler.
  Jede Datei trägt `schema`; ein älteres Schema hebt das Programm beim
  Start an (und legt vorher einmal `state.schema-<n>.json` ab). Ausnahme
  Verlauf: `history.jsonl` wird nachsichtig gelesen – alten Einträgen fehlen
  Felder, eine kaputte Zeile wird übersprungen, nie ein Fehler.
- **Was beim Start gilt:**

  | `state.json` | `state.prev.json` | Folge |
  |---|---|---|
  | lesbar | egal | normaler Start |
  | fehlt | fehlt, und im Ordner liegt keine Spur früherer Nutzung (Not-Kopie, Verlauf, Laufzeitdaten, beiseitegelegte Dateien) | Neuanfang mit leerem Zustand (erste Inbetriebnahme, Prüfstand) |
  | fehlt oder nicht lesbar | lesbar | **Rückfall:** die unlesbare Datei wird als `state.corrupt-<Zeit>.json` beiseitegelegt, der Server startet auf der Vorfassung und wendet die Rücksprung-Regel an |
  | nicht lesbar, oder Schema neuer als das Programm, oder fehlt trotz solcher Spuren | fehlt oder nicht lesbar | **Notbetrieb** |

- **Rücksprung-Regel.** Sie gilt für jeden Start, der nicht auf der zuletzt
  selbst geschriebenen `state.json` aufsetzt: Rückfall auf die Vorfassung,
  `fw-alarm state-bump` nach dem Zurückspielen einer Sicherung (auch nach
  einem Rückweg über ein Schema: älteres Programm, `state.schema-<n>.json`
  als `state.json`, dann `state-bump`), erneute Übernahme. Dann könnte die Box (oder ein Browser) Nummern kennen, die der
  Server gleich noch einmal vergäbe – mit anderem Inhalt. Deshalb:
  Listen-Version, id-Zähler und Befehlsnummer springen um **1000** nach oben;
  ein wartender oder ausgelieferter, nicht quittierter Befehl wird
  abgebrochen (sonst führte ihn die Box ein zweites Mal aus); es entsteht der
  Verlaufs-Eintrag `state_rewound`; der Admin wird gewarnt (§7) – mit dem
  Hinweis, dass die letzte Änderung fehlen kann (z.B. eine Laut-Schaltung).
- Unabhängig davon: Meldet ein Status-Push der Box eine `keys_version`, die
  höher ist als die des Servers, hebt der Server seine Version nach der
  Antwort darüber an – wodurch der Rückstand auch entstand, die Box holt die
  Liste dann neu.
- **Notbetrieb** ist ein Zustand, kein einzelnes Ereignis. Der Server
  **startet trotzdem**:
  - Alarm und Entwarnung gehen an die Not-Kopie `recipients.rescue.json`.
    Sie wird EINMAL beim Start nachsichtig gelesen und danach aus dem
    Speicher bedient: die gültigen, einmaligen Keys, höchstens
    10 – lieber die gültigen Einträge als keiner; steht dort ein gültiger
    Demo-Empfänger, geht der Alarm nur an ihn. Ist auch sie nicht lesbar,
    gilt die Not-Liste der Konfiguration (`bark_keys_alarm`).
  - `keys_list` antwortet `code=keys_corrupt` (die Oberfläche kennt das),
    jede Änderung an den festen Daten wird abgelehnt, nichts Festes wird
    geschrieben – die kaputten Dateien bleiben unberührt.
  - Anmelden und Auslösen gehen weiter. Der Box meldet Poll wie heute
    `keys_version=0` – sie behält dann die Liste, die sie gespeichert hat.
  - Der Admin wird sofort und danach alle 24 h gewarnt (§7); `fw-alarm
    health` meldet den Zustand. Heraus führt nur das Zurückspielen einer
    Sicherung (§13).
- `runtime.json`/`sessions.json` nicht lesbar → die Datei beiseitelegen, leer
  anfangen und den Admin warnen (Box gilt als „noch nie gesehen“, alle melden
  sich neu an).
- `fw-alarm health` nennt als Hinweis, wenn die Not-Liste der Konfiguration
  weniger Keys enthält als die Empfängerliste.

### 6.4 HTTP

Standardbibliothek (`net/http`), kein Framework. Eine eigene kleine Tabelle
der Adressen: genaue Pfade, und **jede Adresse entscheidet selbst über die
Methode** – wie heute PHP (ein GET auf `api/poll.php` ist 403 `FORBIDDEN`,
kein 405; HEAD auf den Kurzbefehl-Link schaltet nichts). Mehrfache
Schrägstriche werden wie bei Nginx zusammengezogen, der Server leitet dafür
nie um (eine Umleitung machte aus dem POST der Box ein GET).

Zeitlimits: Kopfzeilen 10 s, Lesen 30 s, Schreiben 150 s, **Leerlauf 120 s**
(`cloudflared` hält Verbindungen bis zu 90 s). Rumpf höchstens 64 kB,
Sicherheits-Kopfzeilen an jeder dynamischen Antwort wie heute,
`Cache-Control: no-store`. Formularfelder aus urlencoded UND multipart, bei
doppelten Feldern zählt das letzte, die Reihenfolge der Felder bleibt
erhalten (der Status-Push behält die ersten 40). Feldnamen mit Klammern
(`a[]`, `a[b]`) ergäben in PHP eine Liste – solche Felder werden wie dort
übergangen; ein Rumpf mit anderem Inhaltstyp hat keine Felder. Statische
Dateien aus dem eingebetteten `assets/` mit fester Typ-Tabelle; der
Versions-Parameter `?v=` ist ein Inhalts-Hash als Dezimalzahl (die Szenarien
verlangen Ziffern). Seiten über `html/template`.

Zugriffsprotokoll nach journald: Zeit, Methode, **Pfad ohne Query**, Status,
Dauer, `bridge=0|1`, User-Agent – nie ein Token oder Key. Es läuft über eine
begrenzte Warteschlange, die im Zweifel Zeilen verwirft statt eine Anfrage
aufzuhalten; die Selbst-Probe (§6.7) steht nicht darin.

**Der Server lauscht selbst** (keine Übergabe des Ports durch systemd – damit
hieße „Dienst anhalten“ nicht „aus“, und der Rückweg von §12 scheiterte):
`listen = "127.0.0.1:8080"` oder `listen = "unix:/run/fw-alarm/http.sock"`.
Beim Umschalten lauscht er auf dem TCP-Port wie heute Nginx (`cloudflared`
bleibt unverändert, der Rückweg ebenso), beim Aufräumen zieht er auf den
Unix-Socket um (§10, §12.6). Ein Neustart des Dienstes dauert rund eine
Sekunde; ein Aufruf der Box genau in dieser Sekunde scheitert, und sie
wartet 30 s bis zum nächsten – das verkraftet sie (in der Firmware
nachgesehen). Beim Beenden laufen offene Anfragen, ein laufender „Alarm an
alle“ und ausstehende Bark-Meldungen bis zu 40 s zu Ende.

### 6.5 Prüf-Lauf im selben Prozess (ersetzt den Timer)

Zwei Takte:

- **Voller Lauf** 60 s nach dem Start, danach alle 15 min
  (`check_interval_seconds`, Standard 900), und bei `fw-alarm check`:
  dieselben Abschnitte und Regeln wie heute `cron/check_offline.php` –
  Speicher-Selbsttest → Box offline/wieder da → Außen-Wächter verstummt →
  Erinnerungen (Demo, Stumme täglich, dauerhaft Stumme wöchentlich) →
  Box-Gesundheit → Wochenbericht; dazu überfällige Stummschaltungen
  zurückschalten, verfallene Befehle abräumen, abgelaufene Sitzungen löschen.
  Die drei Ausfall-Abläufe bleiben getrennt (eigene Merker), wie `CLAUDE.md`
  es verlangt. Regeln, die Läufe ZÄHLEN („ab 2 Fehlschlägen in Folge“ bei
  „Dashboard DEFEKT“ und bei der Box-Gesundheit), zählen volle Läufe – wie
  heute also rund 15–30 min, nicht 2 min; ein Lauf über `fw-alarm check`
  zählt mit (wie heute ein Lauf des Timers von Hand). Läufe überschneiden
  sich nie: Eine zweite Anforderung wartet auf den laufenden und bekommt
  dessen Zeile.
- **Jede Minute dazwischen nur „Box offline/wieder da“** – die wichtigste
  Meldung kommt damit rund 1 min nach der eingestellten Schwelle statt bis
  zu 15 min danach.

Weiter gilt:

- **Nach einem gescheiterten Versand** wird dieselbe Meldung frühestens
  15 min später erneut versucht – auch im Minutentakt (sonst gingen bei
  gestörtem Bark bis zu 1 440 Anfragen am Tag hinaus, von der Adresse, die
  auch der Alarm nutzt).
- **Die erste „Box OFFLINE“ und ihr Text.** Solange die Box über die Brücke
  meldet und der Außen-Wächter eingerichtet ist, hängt der Text davon ab, ob
  auch der Außen-Wächter schweigt (dann ist wahrscheinlich das NAS zu Hause
  die Ursache). Mit dem Minutentakt käme die Meldung oft, bevor sich das
  beurteilen lässt. Deshalb geht sie in diesem Fall erst hinaus, wenn die
  Schwelle erreicht ist UND der Außen-Wächter entweder in den letzten
  6,5 min aufgerufen hat (er lebt → es liegt an der Box) oder seit mehr als
  9 min schweigt (→ das NAS) – das verzögert sie um höchstens 3 min: Wurde
  der Außen-Wächter noch nie gesehen, geht sie sofort hinaus, und in jedem
  Fall 3 min nach der Schwelle (dann mit dem neutralen Text).
- Die Zeile eines vollen Laufs (`WEB_OK | OK | …`, dieselben Wörter wie
  heute) geht ins Journal und an `fw-alarm check`.
- `cron_age_s` in `alive.php` = Alter des letzten abgeschlossenen vollen
  Laufs. Hängt er, meldet es der Außen-Wächter wie heute. Solange nach einem
  Start noch keiner fertig ist, gilt der letzte Lauf aus `runtime.json`;
  fehlt auch der, die Zeit seit dem Start (nie „noch nie“ – das löste beim
  Außen-Wächter eine Geister-Meldung aus).
- `check_interval_seconds = 0` schaltet die selbsttätigen Läufe (beide
  Takte) ab – nur für Tests, die jeden Lauf einzeln auslösen. `check-config`
  meldet das als Problem, die Oberfläche zeigt den Wächter dann als stehend.
- **Keine Geister nach einer Auszeit des Servers:** Der Server schreibt jede
  Minute ein Lebenszeichen in `runtime.json` – unabhängig von
  `check_interval_seconds` – und noch einmal beim geordneten Beenden. War er
  beim Start länger als 90 s weg, zählen „Box schweigt“ und „Außen-Wächter
  schweigt“ erst ab dem Wiederanlauf – solange er aus war, konnte ihn ja
  niemand erreichen. Fehlt das Lebenszeichen (erste Inbetriebnahme,
  verlorene `runtime.json`, frisch übernommene Daten), zählt die Stille
  ebenfalls ab dem Start. Dieser Zeitpunkt wird gemerkt und gilt auch nach
  einem weiteren Neustart kurz darauf.
- Überfällige Stummschaltungen rechnet die Leseseite weiter sofort als laut
  (eine Definition); zurückgeschaltet wird im vollen Lauf UND wie heute vor
  den Antworten von Poll, `api/keys.php`, Kurzbefehl-Link und `keys_list`
  sowie nach einem Versand von Alarm oder Entwarnung.

### 6.6 Steuer-Zugang nur lokal

`fw-alarm check`, `health`, `flush`, `reload` und `stop` sprechen den Server
über einen Unix-Socket an (`/run/fw-alarm/control.sock`, Rechte 0600,
Benutzer `fwalarm`; Pfad einstellbar). Zugang hat, wer die Datei öffnen darf
– der öffentliche Port kennt keine internen Adressen mehr. `api/health.php`
und `health_check_url` entfallen damit.

### 6.7 Wenn der Server selbst ausfällt

Ohne eigenen Wächter-Prozess braucht es andere Netze:

1. **systemd startet neu** (`Restart=always`, ohne Obergrenze, mit
   wachsendem Abstand von 1 s bis 60 s).
2. **systemd-Watchdog:** Alle 30 s ruft das Programm die eigene Adresse aus
   `listen` auf (`GET /index.php`, eine Anfrage wie von außen, Zeitlimit 5 s)
   – unabhängig vom Prüf-Lauf und von Bark, damit ein langsamer Bark-Versand
   nie wie ein Hänger aussieht. Nur wenn sie antwortet, meldet es sich bei
   systemd; bleibt das 3 min aus, beendet systemd den hängenden Prozess und
   startet neu (`WatchdogSec=180`, `TimeoutStopSec=60`).
3. **Meldung nach Absturz:** `ExecStopPost` startet `fw-alarm
   report-failure`. Endete der Dienst nicht sauber, geht EINE Bark-Meldung
   an den Status-Empfänger („Dashboard-Dienst abgestuerzt, systemd startet
   ihn neu“), höchstens eine pro 30 min. Der Merker dafür liegt in
   `/run/fw-alarm` (bleibt über Neustarts des Dienstes erhalten und hängt
   nicht am Datenordner – sonst käme bei kaputtem Datenordner bei jedem
   Neustart eine Meldung). Ein Versuch, höchstens 5 s – der Neustart wartet
   darauf. Liest die Konfiguration (notfalls `config.last-good.toml`),
   braucht den Server nicht.
4. **Unterbrochener Versand:** Findet der Server beim Start im Verlauf ein
   „Alarm gestartet“ der letzten 15 min ohne Ergebnis, schreibt er den
   Eintrag `send_interrupted` und warnt den Admin (§7). Er sendet **nie von
   selbst nach** – ob noch einmal alarmiert wird, entscheidet ein Mensch.
5. **Außen-Wächter auf dem NAS** – unverändert: nicht erreichbar oder
   Prüf-Lauf zu alt.

### 6.8 Konfiguration

`/etc/fw-alarm/config.toml` (root:fwalarm 0640), TOML mit Kommentaren,
dieselben Schlüsselnamen wie `config.php` (die Texte der Oberfläche nennen
sie). Neu: `state_dir`, `listen`, `control_socket`,
`check_interval_seconds`, dazu die Probe-Sperre `send_guard_keys` und
`send_guard_until` (§10). Entfallen: `data_dir`, `health_check_url`.
Unbekannte Schlüssel sind ein Fehler. „Fehlerhaft“ ist eine Datei nur, wenn
sie sich nicht lesen lässt, einen Syntaxfehler, einen unbekannten Schlüssel,
einen Wert vom falschen Typ, eine unbekannte Zeitzone, eine unzulässige
Probe-Sperre (§10) oder einen `bark_host` mit Abfrage, Fragment oder
Zugangsdaten hat. Fehlerhaft ist sie auch, wenn `listen` ein Unix-Socket ist
und `proxy_client_ip_header` fehlt oder unbrauchbar ist – dort gilt jede
Anfrage als vom Tunnel, ohne Absender-Adresse teilten sich alle Besucher
eine Login-Bremse, und ein Fremder könnte die Anmeldung sperren (im
laufenden Server zählt dafür das `listen` vom Start). Alles andere
(ein Hash in falscher Form, `check_interval_seconds = 0`, ein `bark_host` mit
Pfad oder ohne `http(s)://` …) sind Hinweise:
`check-config` nennt sie, der Server arbeitet mit der Datei. Ein Hinweis ist
auch ein gesetztes `trusted_proxies` ohne brauchbares
`proxy_client_ip_header` (alle Besucher über den Proxy zählen dann als ein
Absender – dieselbe Lücke hatte PHP). Fehlt ein
Schlüssel, gilt der Wert,
den der PHP-Code heute dafür nimmt (das `?? …`), nicht der Wert der alten
Vorlage – das Verhalten soll sich beim Umschalten nicht ändern.

**Anfragen fassen die Datei nie an** (auch die Konfiguration kommt aus dem
Speicher): Ein Hintergrund-Prüfer sieht alle 2 s nach (Änderungszeit, Größe,
Dateinummer) und setzt bei einer gültigen neuen Fassung einen Zeiger um;
`fw-alarm reload` tut dasselbe sofort und meldet das Ergebnis. Eine
geänderte Datei – gültig oder fehlerhaft – beurteilt der Prüfer erst, wenn
sie bei der nächsten Prüfung unverändert ist: Ein Editor, der gerade
schreibt, löst nichts aus, auch nicht mit einem halben Stand, der gültiges
TOML mit weniger Werten ist (es gälten die Standards – ohne Probe-Sperre).
Eine Datei, die keinen einzigen Wert setzt, ist fehlerhaft; die Probe-Sperre
steht in der geschriebenen Datei als Erstes (`send_guard_keys`, `timezone`,
`send_guard_until`) – ein abgeschnittener Anfang hält sie samt Zeitzone oder
ist fehlerhaft.

**Ist die Datei fehlerhaft**, gilt die letzte gute weiter – aber nicht
stillschweigend (ein Passwort mit Tippfehler zu ändern, darf nicht so
aussehen, als hätte es geklappt):

- Der Admin wird gewarnt (§7), mit Erinnerung alle 24 h; `fw-alarm health`
  nennt den Fehler ohne Geheimnisse.
- **Alle Sitzungen enden.** Anmelden geht mit den Passwörtern der letzten
  guten Fassung weiter („Anmelden geht immer“). So ist ein verlorenes,
  angemeldetes Handy auch dann draußen, wenn die Passwort-Änderung einen
  Tippfehler hatte.
- Nach jeder gültig geladenen Fassung legt der Server eine Kopie als
  `config.last-good.toml` in den Datenordner (0600). Ist `config.toml` beim
  **Start** nicht lesbar, startet er mit dieser Kopie und warnt wie oben –
  ein Neustart der VM Tage nach einem Tippfehler legt das Dashboard nicht
  lahm. Gibt es keine Kopie, scheitert der Start (`check-config` nennt den
  Grund).

Der sichere Weg, die Datei zu ändern, ist `sudo fw-alarm edit-config`: Kopie
anlegen, im Editor öffnen, prüfen (bei einem Fehler: noch einmal bearbeiten
oder verwerfen), in einem Schritt ersetzen (Besitzer und Rechte bleiben),
`reload`.

Passwörter: Neue Hashes erzeugt `fw-alarm hash-password` als **Argon2id**
(19 MiB, 2 Durchläufe, 1 Faden – der Mindestwert der OWASP-Empfehlung, passend
zur 1-GB-VM); die vorhandenen bcrypt-Hashes (`$2y$`) gelten weiter.
Passwortprüfungen kosten Rechenzeit und Speicher, deshalb:

- höchstens zwei gleichzeitig, höchstens eine je Absender;
- wer warten muss, wartet der Reihe nach je Absender (ein Absender mit
  vielen Versuchen drängt die anderen nicht zurück), höchstens 5 s. Danach
  antwortet die Anmeldeseite mit Status 503 und dem Satz „Gerade zu viele
  Anmeldungen gleichzeitig – bitte in ein paar Sekunden noch einmal
  versuchen.“; ein solcher Versuch zählt nicht als Fehlversuch (er wurde nie
  geprüft);
- die Pause von 1 s nach einem Fehlversuch liegt außerhalb der zwei Plätze.

Die Speicher-Obergrenze des Dienstes (`MemoryMax=256M`) ist darauf
abgestimmt: Grundbedarf plus zwei Prüfungen plus Reserve; die Probe auf der
VM (§11.7) bestätigt sie mit dem Fall „30 falsche Anmeldungen zugleich“.

### 6.9 Pakete (Vorschlag; Feinschnitt macht der Plan)

| Paket | Inhalt |
|---|---|
| `config` | TOML laden, prüfen, im Hintergrund neu laden, letzte gute Fassung |
| `model` | die Datentypen und die **reinen Regeln**: effektive Liste, stumm/überfällig/dauerhaft, Versionen und ids, Rücksprung-Regel, Demo-Schutzregeln, Lebenslauf eines Befehls, Auswertung eines Status-Pushs (Relaisalarm, Neustart, veralteter Push, fremdes Board) |
| `store` | Schnappschuss, `Update` mit Fristen, Sichern/Laden über eine austauschbare Datei-Schicht (für Tests mit hängender Platte), `outbox` und Verlauf, Not-Kopie, Speicher-Selbsttest, Sperre |
| `bark` | Versand an viele Keys in Runden, ASCII, Feldsätze |
| `notify` | Arten und Einstellungen der Admin-Meldungen, Ruhezeit, Versand |
| `watch` | Prüf-Lauf, Zeitgeber, Texte, Wochenbericht, systemd-Watchdog |
| `web` | HTTP-Rahmen, Sitzungen, Anmeldung, Bremsen, Seiten, statische Dateien |
| `box`, `mutelink`, `dashapi`, `alive` | die Handler |
| `control` | Unix-Socket: Server und Aufrufer |
| `logview` | Tabelle der Verlaufs-Ereignisse, Filter, Ausgabe pro Rolle |
| `phpimport` | die einmalige Übernahme (§9), wird danach gelöscht |

Abhängigkeiten: Standardbibliothek; `golang.org/x/crypto` (Argon2id, bcrypt)
samt dem davon benötigten `golang.org/x/sys`; `golang.org/x/term` (Passwort
ohne Echo lesen); `github.com/BurntSushi/toml` (ohne eigene Abhängigkeiten).
Vier Module, drei davon vom Go-Team, alle im Repo mitgeliefert (`vendor/`).
`CGO_ENABLED=0`, Zeitzonen-Daten eingebaut, Go-Version in `go.mod`
festgeschrieben. Die Oberfläche wird eingebettet; das verlangt eine kleine
Go-Datei im Ordner der Oberfläche selbst (`nas_dashboard/`, später `web/`).
Gebaut wird aus `git archive <Stand>`, nie aus dem Arbeitsverzeichnis
(Zeilenenden des PCs änderten sonst Bytes und damit `?v=`).

## 7. Regeln aus CLAUDE.md, die entfallen oder sich ändern

Alles, was hier NICHT steht, gilt unverändert.

| Heute (PHP) | Neu |
|---|---|
| Dateien `<name>.php` mit Guard-Zeile, `data_path()`, `fix_owner()` | entfällt (Dateien liegen außerhalb jedes Webroots, es gibt keinen) |
| `mutate_alarm_keys()`, `backup_then_write()`, `.bak`-Rückfall | ein Dokument, unteilbar gesichert und vor dem Umbenennen zurückgelesen; `state.prev.json` als Rückfall mit Rücksprung-Regel, sonst Notbetrieb (§6.3) |
| Versionen/ids über Hochwassermarken in einer eigenen Datei, auch die von der Box gemeldete Version | **bleibt als Regel**, liegt in `state.json`. Springt der Stand zurück (Rückfall, Sicherung, Schema-Rückweg), greift die Rücksprung-Regel (§6.3); eine von der Box gemeldete höhere Version hebt die des Servers sofort an, nicht erst bei der nächsten Änderung |
| Eine von Hand verdorbene Liste: die gültigen, einmaligen, ersten 10 Einträge werden alarmiert | Eine Liste mit ungültigen Einträgen lässt sich nicht speichern. Eine von Hand verdorbene `state.json` ist „nicht lesbar“ → Rückfall bzw. Notbetrieb; dort gilt die Not-Kopie, nachsichtig je Eintrag gelesen („lieber die gültigen als keiner“) |
| Not-Liste `bark_keys_alarm` als einziges Netz, wenn die Liste samt Sicherung unlesbar ist | davor liegt die Not-Kopie `recipients.rescue.json`, die der Server selbst aktuell hält; die Übernahme (§9) trägt die heutige Liste zusätzlich in die Not-Liste der Konfiguration ein |
| `demo_set`: Schreibreihenfolge, Zurückrollen, `code=inconsistent`, `repair_broken_demo_mode()`, Lesereihenfolge in `api/keys.php`, Ereignis `demo_repaired` | entfällt – Demo-Modus und Listen-Version ändern sich in einem Schritt; der Fall kann nicht mehr entstehen. `demo_repaired` bleibt in der Tabelle der Verlaufs-Ereignisse, damit alte Einträge weiter angezeigt werden |
| Sperren und ihre Reihenfolge, `with_lock()`/`with_lock_soft()`, „nie unter gehaltener `keys`-Sperre“ | entfällt (ein Schreiber); es bleibt: kein Bark-Versand innerhalb einer Änderung |
| Status-Push: HTTP 500, wenn der Datenordner klemmt | Status-Push gilt im Speicher und wird mit 200 bestätigt |
| Poll bei klemmendem Datenordner: je nach Ursache 500 oder `type=NONE` | immer 200 `type=NONE` mit `keys_version`; ein Befehl wird nur ausgeliefert, wenn „ausgeliefert“ innerhalb der Frist gesichert ist (§6.2) |
| Fehler beim Speichern: ein Satz („Datenordner nicht beschreibbar …“) | zwei Sätze unter demselben `code: storage`: der bekannte, wenn das Sichern scheitert; „Fehler: Speichern dauert zu lange – bitte den angezeigten Stand prüfen.“, wenn die Frist abläuft (die Änderung kann dann noch wirksam werden) |
| `api/health.php`, `health_check_url`, Schritte `lock/write/read/delete/perm`, `inplace_data_files()`, `blocks_pushes` | Speicher-Selbsttest im vollen Prüf-Lauf (schreiben, zurücklesen, löschen im Datenordner; dazu das Ergebnis des letzten Sicherns). „Dashboard DEFEKT“ ab 2 vollen Läufen in Folge, Erinnerung alle 24 h, einmal „wieder OK“ – Ereignisse `nas_web_failed`/`nas_web_recovered` bleiben. Der Text (ASCII) sagt: was scheitert (Schritt), was weiterläuft (Alarm, Status der Box, Anmeldung), was nicht (Änderungen werden nicht gespeichert, Befehle an die Box nicht ausgeliefert), wo nachsehen (`journalctl -u fw-alarm`, Platz und Rechte von `/var/lib/fw-alarm`) |
| Hinweis der Meldungsart `nas_web` in den Einstellungen: „(Login und Status der Box scheitern dann)“ | nennt, was wirklich ausfällt: „(Änderungen werden dann nicht gespeichert, Befehle an die Box nicht ausgeliefert)“ |
| „Box OFFLINE“ gibt dem Server die Schuld, wenn `blocks_pushes` | dieser Zweig entfällt (Pushs kommen auch bei kaputter Platte an); der Zweig „NAS zu Hause“ (`via_bridge`) bleibt, die erste Meldung wartet höchstens 3 min, bis er sich beurteilen lässt (§6.5) |
| `watcher_unknown_at`, Ersatz-Ablage des Wächter-Zustands im Temp-Ordner, „`alive.php` lauffähig mit alten `common.php`-Ständen“ | entfällt (Merker liegen im Speicher). Bewusst in Kauf genommen: Startet der Server neu, WÄHREND er nichts sichern kann, kann eine schon gesendete Meldung („Box OFFLINE“, Erinnerung) ein zweites Mal kommen |
| Wächter als eigener Prozess am Timer alle 15 min, als root auf dem NAS | voller Prüf-Lauf im Server 60 s nach dem Start und dann alle 15 min, „Box offline/wieder da“ jede Minute (§6.5); Ausfall des Servers: §6.7 |
| Eine gescheiterte Meldung versucht der nächste Lauf (nach 15 min) erneut | unverändert frühestens nach 15 min – auch für „Box offline/wieder da“ im Minutentakt |
| Stille von Box und Außen-Wächter zählt ab dem letzten Lebenszeichen | nach einer Auszeit des Servers (> 90 s) erst ab dem Wiederanlauf (§6.5) |
| `log_cache.json` | entfällt |
| `defer_after_response()`/`fastcgi_finish_request()` | Arbeit nach der Antwort läuft nebenher; beim Beenden wird darauf gewartet |
| PHP-Sitzungen (`gc_maxlifetime`, `use_strict_mode`) | eigene Ablage; alle Regeln der Sitzung bleiben |
| Tippfehler in `config.php`: jede Anfrage scheitert sofort (unübersehbar) | letzte gute Fassung gilt weiter, alle Sitzungen enden, der Admin wird gewarnt; beim Start notfalls die Kopie `config.last-good.toml` (§6.8) |
| Vertraute Proxys: nur über die Adresse (`trusted_proxies`) | zusätzlich: Verbindungen über den Unix-Socket des Servers gelten als vom Tunnel (§10) |
| Texte mit „config.php“, „PHP-cURL“, `systemctl status nginx php8.3-fpm` | nennen `config.toml` bzw. `systemctl status fw-alarm` |
| `classic.php`, `assets/classic/` | ist erledigt (17.11.2026): `classic.php` leitet nur noch um |

**Neue Meldungen an den Admin.** Der Go-Server kennt Lagen, die es in PHP
nicht gab. Sie laufen über dieselbe Stelle wie alle Admin-Meldungen und
gehören zur vorhandenen Art `nas_web` („Dashboard defekt / wieder in
Ordnung“, sicherheitsrelevant) – deren Einstellungen (Dringlichkeit,
Ruhezeit, Stellvertreter) gelten also auch für sie. Texte ASCII:

| Anlass | Titel | Wiederholung |
|---|---|---|
| Notbetrieb (§6.3) | `Dashboard NOTBETRIEB` – Empfängerliste nicht lesbar; an wie viele Empfänger ein Alarm jetzt ginge und woher (Not-Kopie/Not-Liste); die Box behält ihre gespeicherte Liste (war zuletzt Demo-Modus: evtl. nur den Demo-Empfänger); Ausweg: Sicherung zurückspielen (`server/README.md`) | sofort, dann alle 24 h |
| Rücksprung (§6.3) | `Dashboard: Stand zurueckgesetzt` – woraus gestartet wurde; die letzte Änderung kann fehlen (Stumm/Laut, Empfänger prüfen) | einmal (vorgemerkt, bis Bark sie angenommen hat) |
| `runtime.json`/`sessions.json` nicht lesbar | `Dashboard: Laufzeitdaten verworfen` – alle melden sich neu an, die Box gilt bis zum nächsten Status als nie gesehen | einmal (vorgemerkt wie oben) |
| Konfiguration fehlerhaft (§6.8) | `Dashboard: Konfiguration fehlerhaft` – es gilt die letzte gültige, alle Sitzungen wurden beendet, `fw-alarm check-config` nennt den Fehler | sofort, dann alle 24 h |
| unterbrochener Versand (§6.7) | `Dashboard: Versand unterbrochen` – ein Alarm/eine Entwarnung lief beim Neustart, Ergebnis unbekannt, Verlauf prüfen und ggf. erneut senden | einmal (vorgemerkt wie oben) |

Bewusst AUSSERHALB dieser Tabelle und nicht einstellbar: die Meldung nach
einem Absturz (§6.7.3) – sie muss ohne den Server auskommen (Dringlichkeit
fest „normal“ mit Ton).

**Neue Verlaufs-Ereignisse:** `state_rewound` (Kategorie System, nur Admin)
und `send_interrupted` (Kategorie Alarm, Versand-Ereignis). Sie brauchen je
einen Text in `assets/app/logic.js` (`describeLogEntry`) – die einzige
Änderung an der Logik der Oberfläche.

## 8. Sitzungen und Anmeldung

Regeln wie heute: Cookie `boss925_dashboard` (`HttpOnly`, `SameSite=Strict`,
`Secure` laut vertrautem Proxy, 12 h, bei jeder angemeldeten Anfrage
erneuert); Sitzung nur für Angemeldete; an die Zugangsdaten gebunden
(Passwort ändern meldet alle Sitzungen dieses Kontos ab); neue ID bei jeder
Anmeldung; Abmelden löscht Sitzung und Cookie; `Sec-Fetch-Site`; Bremse pro
Absender VOR der Passwortprüfung; genau eine Passwortprüfung pro Versuch,
auch für unbekannte Benutzer (gegen den Hash des Admins, Ergebnis verworfen –
ein fester Wegwerf-Hash verriete über die Antwortzeit, ob es den Namen
gibt); 1 s Pause nach einem Fehlversuch.

Sitzungs-ID: 32 Zufallsbytes (`crypto/rand`). Abgelegt wird nur ihr
SHA-256. Sitzungen überstehen einen Neustart – das Handy bleibt nach einem
Update angemeldet. `last_seen` höchstens einmal pro Minute. Neu: Ist die
Konfiguration fehlerhaft, enden alle Sitzungen (§6.8); wie viele
Passwortprüfungen zugleich laufen und wie gewartet wird, regelt §6.8.

## 9. Übernahme der vorhandenen Daten

`fw-alarm import-php --data <alter Datenordner> --config-json <Datei>
--out-state <neuer Ordner> --out-config <config.toml>`. Die `config.php`
wird vorher auf der VM mit PHP nach JSON ausgegeben (PHP ist dann noch da) –
in einen Ordner im Home-Verzeichnis (0700), nie nach `/tmp`, und danach
gelöscht.

- Nur hier wird nachsichtig gelesen (`[]` statt Objekt, `1`/`"1"`/`true`,
  Zahlen als Zeichenketten) – im Paket `phpimport`, das nach dem Umschalten
  gelöscht wird.
- **Die Übernahme verliert nichts stillschweigend.** Lässt sich ein
  Empfänger, sein Key, sein Kurzbefehl-Token, sein Stumm-Zustand oder der
  Demo-Empfänger nicht eins zu eins übernehmen, bricht sie ab, ohne etwas zu
  schreiben, und nennt den Eintrag (ohne Key und Token). Weglassen geht nur
  mit einem ausdrücklichen Schalter je Fall (`--drop-recipient <id>`).
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
- **Die Listen-Version steigt um eins über alles, was es je gab** (das
  Größte aus Liste, Hochwassermarke und der zuletzt von der Box gemeldeten
  Version, plus eins) – die Box holt die Liste damit auf jeden Fall einmal
  vom neuen Server, welche kleine Abweichung die Bereinigung auch gebracht
  hat. (Die Firmware übernimmt eine Liste mit
  denselben Keys still.)
- **Die Not-Liste wird aufgefrischt:** In die neue `config.toml` schreibt die
  Übernahme als `bark_keys_alarm` die Keys der heutigen Empfängerliste (die
  alte Not-Liste in `config.php` ist seit Monaten nicht gepflegt und kürzer
  als die Liste). Die Not-Kopie `recipients.rescue.json` legt sie ebenfalls
  an.
- Schreibt nie in den alten Ordner. Weigert sich, einen vorhandenen neuen
  Zustand zu überschreiben (außer `--force`; dann gilt die Rücksprung-Regel).
  Am Ende liest sie ihr eigenes Ergebnis mit dem strengen Leser
  (`check-state`) und gibt eine Zusammenfassung aus (Anzahlen; was
  angepasst wurde und warum) – ohne Keys und Tokens.
- Ist die alte Liste samt `.bak` unlesbar, bricht die Übernahme ab.
- Ist `demo_mode.json` kaputt oder fehlt sie trotz `.bak`, übernimmt sie
  LIVE (die Box könnte noch die Demo-Liste halten; die erhöhte Version lässt
  sie neu abholen) – mit Hinweis in der Zusammenfassung.
- Sie setzt KEIN Lebenszeichen des Servers: Der erste Start zählt damit als
  „nach einer Auszeit“, die Stille von Box und Außen-Wächter zählt erst ab
  ihm (§6.5). Beim Umschalten ist die Box durch den Halt der Brücke
  minutenlang abgeschnitten – daraus darf kein „Box OFFLINE“ werden.
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

**Kein Test erreicht ein echtes Handy** (Vorgabe des Nutzers vom 17.11.2026:
„under no circumstance can a real alarm trigger to ANYONE but me“). Vier
Regeln, jede für sich ausreichend:

1. **Echte Bark-Keys gibt es nur auf dem laufenden Server.** Auf dem PC und
   bei GitHub sind die Keys erfunden, und Bark ist eine Attrappe – der
   Prüfstand schreibt keine Konfiguration, deren `bark_host` nicht genau
   die Attrappe (`http://127.0.0.1:<Port>`) oder leer ist, auch keine
   absichtlich kaputte (der Treiber weist sie ab, bevor ein Byte auf der
   Platte liegt). In jeder
   KOPIE echter Daten (Gleichlauf auf dem PC, Probe auf der VM) werden vor
   dem ersten Start alle Bark-Keys durch erfundene ersetzt; der Schritt
   prüft selbst, dass keiner übrig ist, und es gibt keinen Schalter, ihn zu
   überspringen.
2. Die Probe auf der VM kann Bark zusätzlich gar nicht erreichen
   (geschlossener Port, von der Unit erzwungen, dazu eine Netz-Regel der
   Unit – vor dem Einlesen echter Daten auf der VM ausprobiert).
3. **Probe-Sperre:** Solange in der Konfiguration `send_guard_keys` und ein
   `send_guard_until` in der Zukunft stehen, sendet der Server an KEINEN
   anderen Key – durchgesetzt in der einen Funktion, durch die jeder
   Versand geht (Alarm, Entwarnung, Test-Push, Infos, Meldungen), also
   unabhängig davon, was Empfängerliste und Demo-Modus gerade ergeben. Der
   neue Server startet beim Umschalten mit dieser Sperre auf den Key des
   Admins (90 min). Damit eine vergessene oder vertippte Sperre nie einen
   echten Alarm von Hand blockiert: Sie endet von selbst, eine Zeit mehr als
   3 Stunden voraus macht die Konfiguration fehlerhaft (wird nicht
   angenommen), `fw-alarm health` und die Antwort eines Alarms nennen sie.
   Die Box alarmiert unabhängig davon immer selbst.
4. Auf dem laufenden System wird nichts ausgelöst, was sendet – außer dem
   einen gemeinsamen Demo-Alarm beim Umschalten, und der erst, nachdem der
   Server selbst gemeldet hat, wen ein Alarm jetzt erreichte (`fw-alarm
   health`: Demo-Modus, genau ein Empfänger, der Nutzer).

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
- Anmeldung: Bremse je Absender, begrenzte und gerecht verteilte
  Passwortprüfungen (§6.8) – ein Angreifer mit vielen Adressen hält den
  Admin nicht vom Anmelden ab.
- Der Bark-Versand folgt keinen Weiterleitungen und liest keine Antwort ein
  (die Standardbibliothek täte beides von sich aus).
- Kopfzeilen eines Proxys zählen nur von vertrauten Absendern. **Nach dem
  Aufräumen lauscht der Server nur noch auf einem Unix-Socket**
  (`/run/fw-alarm/http.sock`), den außer ihm nur der Benutzer von
  `cloudflared` öffnen darf; nur was dort ankommt, gilt als „vom Tunnel“.
  Ein anderer lokaler Benutzer kann sich dann nicht mehr als Tunnel ausgeben
  (und sich so seine Absender-Adresse aussuchen). Zwischen Umschalten und
  Aufräumen bleibt es bei `127.0.0.1:8080` wie heute – für diese Tage in
  Kauf genommen, damit Umschalten und Rückweg einfach bleiben.
- Dienst unter systemd eingesperrt: eigener Benutzer, kein Zugriff außer
  auf Daten-, Konfigurations- und Laufzeitordner, keine neuen Rechte, keine
  Capabilities, eingeschränkte Systemaufrufe und Adressfamilien,
  Speicher-Obergrenze, `UMask=0077`.
- Ein Programm pro Datenordner (§6.1).
- Bau: nachvollziehbar aus einem Git-Stand (`-trimpath`), auf dem PC;
  Einspielen vergleicht die Prüfsumme auf der VM mit der des PCs; nichts
  über `/tmp`.
- Prüfungen bei jeder Aufgabe: `gofmt`, `go vet`, `staticcheck`,
  `govulncheck`; Fuzz-Tests für alles, was Eingaben von außen zerlegt
  (Formulare, Konfiguration, Zustandsdateien – auch „schreiben, zurücklesen,
  gleich“ –, Verlaufszeilen, die Übernahme).
- **Bei jedem Push prüft GitHub mit** – und gepusht wird während des Baus
  nach jeder fertigen Aufgabe (Entscheidung des Nutzers vom 17.11.2026; ein
  Push spielt nichts ein) – (Actions; an feste Stände gebunden, nur
  Leserechte, keine Geheimnisse, kein Einspielen von dort): dieselben
  Prüfungen, dazu `go test -race` (der Race-Detektor läuft auf dem
  Windows-PC nicht) und die Server-Tests gegen Go. Ein grüner Lauf ist
  Bedingung für das Umschalten.
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
   demselben Port neu startet (mit Wiederholung, bis der Port frei ist; nach
   jedem Neustart öffnet der Prüfstand frische Verbindungen) – das geht,
   weil ein Neustart nichts verliert, und es prüft genau das mit. Zum Lesen
   ruft er vorher `fw-alarm flush`. Keine Test-Haken im Programm: Getestet
   wird, was ausgeliefert wird. Auch Sitzungen spricht der Prüfstand über
   einen Helfer an („diese Sitzung ist seit 13 h untätig“), nicht über einen
   Ordner.
2. **„Sichern scheitert“ ohne Haken:** Der Treiber belegt den festen Namen
   der Zwischendatei (`state.json.tmp`, `runtime.json.tmp`,
   `sessions.json.tmp`) mit einem nicht leeren Ordner bzw. legt an die Stelle
   von `history.jsonl` einen Ordner – das wirkt unter Windows wie unter Linux
   und lässt sich im laufenden Betrieb setzen und wieder aufheben.
3. **Was sich von außen nicht herstellen lässt, prüfen Go-Tests mit einer
   untergeschobenen Datei-Schicht:** eine hängende Platte, Fristen, die
   Schlange (verworfen, wenn nicht begonnen; gültig, wenn verspätet fertig),
   der Zeitpunkt des ersten Laufs, der Minutentakt und die Wiederholung nach
   gescheitertem Versand (mit einer gestellten Uhr). Hier gilt „getestet
   wird, was ausgeliefert wird“ ausdrücklich nicht.
4. **Was nur PHPs Ablage prüft** (`.bak`, kaputte Datei, Sperre als Ordner,
   `health.php`) wandert in eine Datei, die nur gegen PHP läuft und mit PHP
   verschwindet. Für jede dieser Regeln nennt §7 das Gegenstück; der Plan
   führt eine Tabelle „Regel → Test“ für alles, was §7 ändert oder streicht.
   Ebenso die zwei Fälle in `tests/dashboard/wording.test.mjs`, die
   PHP-Quelltext ausführen: Ihre Texte prüft ein Go-Test; die
   Wortlaut-Regeln („Server“ statt „NAS“) lesen zusätzlich die Go-Quellen.
5. **Go allein, von außen:** viele Anfragen zugleich (20 Polls → genau einer
   bekommt den Befehl; 30 falsche Anmeldungen bei Grenze 8 → höchstens 8
   Passwortprüfungen; 10 `keys_add` → 10 ids, Version +10); Sichern
   scheitert (Alarm geht raus, Status wird angenommen, Poll liefert keinen
   Befehl aus, Änderungen werden abgelehnt, der Admin wird gewarnt);
   Neustart mitten in „Alarm an alle“ (alle Empfänger werden versorgt, das
   Ergebnis steht im Verlauf); Neustart verliert nichts; kaputte
   Zustandsdatei → Rückfall mit Rücksprung, dann Notbetrieb mit Not-Kopie;
   zwei Server auf einem Datenordner (der zweite bricht ab);
   Sommer-/Winterzeit; ein Tippfehler in `config.toml` (letzte gute gilt
   weiter, Sitzungen enden, Start mit der Kopie).
6. **Harter Abbruch** (der Prüfstand beendet den Prozess ohne Vorwarnung):
   nach einem ausgelieferten Befehl → keine zweite Auslieferung; nach dem
   Push eines Relaisalarms → genau ein `relay_alarm`; während „Alarm an
   alle“ → „gestartet“ steht im Verlauf, nach dem Neustart
   `send_interrupted` und die Warnung, kein selbsttätiges Nachsenden.
7. **Übernahme:** ein künstlicher PHP-Datenordner im Repo mit allem, was
   PHP über die Monate geschrieben hat (alte Formen, kaputte Einträge) →
   erwarteter neuer Zustand bzw. Abbruch mit Namen des Eintrags. Dann der
   **Gleichlauf:** PHP auf einer Kopie und Go auf der Übernahme derselben
   Kopie bekommen dieselben Anfragen; verglichen werden die Antworten und
   was an die Bark-Attrappe ging (Empfänger, Lautstärke, `call`) – auch
   Formulare mit ungewöhnlichen Feldnamen. Einmal mit dem künstlichen
   Ordner, einmal **auf dem PC mit einer Kopie der echten Daten** (vorher
   werden alle Bark-Keys in der Kopie durch erfundene ersetzt, Bark zeigt
   auf die Attrappe; die Kopie liegt außerhalb des Repos und wird danach
   gelöscht).
8. Die sieben Chrome-Szenarien und `tests/dashboard/*.test.mjs` gegen Go.
9. Sicherheits-Durchsicht (eigene Sitzung).
10. **Probe auf der VM** vor dem Umschalten: Go auf einem zweiten Port mit
    einer Übernahme der echten Daten – mit ersetzten Bark-Keys (§10) – und
    einem Bark-Ziel, das nichts annimmt – erzwungen, nicht nur eingetragen: Die Probe-Unit darf nur
    Adressen des eigenen Rechners erreichen und startet nicht, wenn in
    ihrer Konfiguration etwas anderes als der geschlossene Port steht. Dort wird geprüft, was der PC nicht kann: die Unit (Start,
    Stopp, Neustart), der Watchdog (ein angehaltener Prozess wird nach 3 min
    beendet und neu gestartet), die Meldung nach einem Absturz, der
    Unix-Socket samt `cloudflared`-Aufruf darüber, der Rückweg als
    Trockenübung (Dienst stoppen → der Port ist frei), die
    Speicher-Obergrenze mit 30 gleichzeitigen falschen Anmeldungen.
11. **Speicher und Oracles 20-%-Regel.** Oracle stoppt VMs, die bei CPU,
    Netz UND Speicher 7 Tage unter 20 % bleiben; hier hält allein der
    Speicher die VM. Gemessen wird der Anteil belegten Speichers
    ((gesamt − verfügbar) / gesamt), einmal abgeglichen mit der Anzeige in
    der Oracle-Konsole: vor dem Umschalten als Rechnung (heutiger Wert
    minus Nginx und PHP-FPM plus `fw-alarm`), nach dem Umschalten und nach
    dem Aufräumen als Messung, dazu täglich in den ruhigen Tagen. Das
    Gegenmittel liegt vorher bereit und wird in der Probe ausprobiert: eine
    kleine Unit, die eine feste Menge Speicher belegt hält
    (`fw-alarm-ballast.service`, Größe einstellbar, zunächst aus).
    Eingeschaltet wird sie, wenn der Wert unter 25 % läge.

## 12. Ablauf und Rückweg

1. **Vorarbeiten** – erledigt (17.11.2026): Tests für die Dashboard-API
   gegen PHP, Chrome-Szenarien über den Prüfstand, `classic.php` entfernt.
2. **Tests von Dateien lösen** (§11.1, gegen PHP grün).
3. **Go bauen**, bis §11.1–8 erfüllt sind. Der Betrieb merkt nichts.
4. **Sicherheits-Durchsicht, Probe auf der VM, Rechnung zum Speicher.**
5. **Umschalten** (mit dem Nutzer, ca. 30 min; nicht in den 3 Tagen vor oder
   nach einem Flashen der Box; zu einer Tageszeit, zu der wenige
   Kurzbefehle laufen – also nicht zu Arbeitsbeginn oder Feierabend).

   Nur aus einem gesunden Zustand: die letzte Zeile des PHP-Wächters beginnt
   mit `WEB_OK` und enthält kein `STATE_WRITE_FAILED`; der Demo-Modus ist
   aus; kein Alarm läuft; kein Befehl wartet oder ist ausgeliefert und nicht
   quittiert; niemand steht als „Evtl. stumm“ in der Liste.

   Reihenfolge:
   1. Sicherung (`server/backup.sh`).
   2. Den alten Timer anhalten und abschalten, einen laufenden Lauf
      abwarten – sonst feuert er mitten im Umschalten (zweite „wieder
      laut“-Meldungen, „Box OFFLINE“ aus Daten, die niemand mehr füttert).
   3. Die Brücke auf dem NAS in den Halt setzen (`bridge_hold.php`): Die Box
      und alte Kurzbefehl-Links bekommen 503, abgewiesene Stumm/Laut-Aufrufe
      werden dort notiert.
   4. Nginx und PHP-FPM anhalten, abschalten und maskieren (die VM startet
      für Sicherheits-Updates von selbst neu – PHP darf danach nicht wieder
      auf dem Port sitzen und aus eingefrorenen Daten antworten).
      **Von hier bis Schritt 6 gibt es kein
      Dashboard und keinen Alarm von Hand** (erwartet: 3–5 min); die Box
      alarmiert wie immer selbst.
   5. `config.php` nach JSON ausgeben, Übernahme laufen lassen,
      `check-state`.
   6. `fw-alarm` auf `127.0.0.1:8080` starten; `fw-alarm health`,
      `server/check.sh`.
   7. Den Halt der Brücke aufheben – sofort, sobald `fw-alarm health` gut
      meldet, die längeren Prüfungen folgen danach; die dort notierten
      Stumm/Laut-Aufrufe nachholen. Für neue Links, die direkt am Server ankommen: im Journal
      von `cloudflared` nachsehen, ob in der Lücke ein Aufruf von
      `api/mute.php` scheiterte, und nachfassen.
   8. Nur harmlose Prüfungen (Status der Box, TEST-Befehl, eigener
      Kurzbefehl-Link, ein Prüf-Lauf); alle melden sich neu an. Der Server
      läuft dabei unter der Probe-Sperre (§10): Er kann an niemanden außer
      dem Admin senden. Dann, gemeinsam: Demo-Modus auf den Nutzer selbst;
      `fw-alarm health` muss „Demo-Modus, genau ein Empfänger: der Nutzer“
      melden – erst dann **ein Alarm und eine Entwarnung nur an ihn**,
      zurück auf LIVE. Als Letztes wird die Probe-Sperre bewusst aufgehoben
      (sonst endet sie 90 min nach dem Umschalten von selbst).

   **Rückweg – nur am Abend des Umschaltens.** Danach wird nach vorn
   repariert: Je länger Go läuft, desto mehr fehlt in den alten Daten, und
   desto mehr Geister-Meldungen brächte der Rückweg. Schritte:
   `fw-alarm check-state --list` ausgeben und aufheben; `systemctl stop
   fw-alarm` (der Port ist dann wirklich frei); Nginx und PHP-FPM wieder freigeben
   und starten;
   den ersten Status-Push der Box und einen Aufruf des Außen-Wächters
   abwarten; ERST DANN den alten Timer wieder starten (sonst meldet sein
   erster Lauf „Box OFFLINE“ und „Aussen-Waechter meldet sich nicht“ aus
   alten Zeitstempeln); Empfänger und Stumm-Zustände mit der aufgehobenen
   Liste vergleichen und im Dashboard von Hand nachziehen (wer nach dem
   Umschalten auf „laut“ gestellt hat, wäre in den alten Daten wieder stumm).
   PHP arbeitet auf seinen alten, unberührten Daten weiter; die Box holt
   sich die Liste von selbst neu. Ein späteres zweites Umschalten braucht
   `import-php --force`.
6. **Aufräumen, nach 3 ruhigen Tagen:** PHP und Nginx von der VM, alte
   Daten nach einer letzten Sicherung löschen; `cloudflared` und der Server
   ziehen auf den Unix-Socket um (ein eigener kleiner Schritt mit eigener
   Prüfung; zurück geht es mit der vorigen `config.yml`); im Repo: alle
   PHP-Dateien, der PHP-Treiber der Tests, die PHP-only-Tests, `phpimport`
   und der Gleichlauf gehen; `nas_dashboard/` wird zu `web/` (nur noch
   Oberfläche); die Texte des Außen-Wächters, die `config.php`,
   `check_offline.php` und den Timer nennen, werden berichtigt; `CLAUDE.md`,
   READMEs und Runbook werden neu geschrieben. Danach: Passkeys.

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
- Der Text der Box „Hash in config.php auf dem NAS“ (bei abgelehntem Token)
  nennt dann die richtige Datei.

## 13. Betrieb

- Unit: `fw-alarm.service` (Benutzer `fwalarm`, `StateDirectory`,
  `RuntimeDirectory` mit `RuntimeDirectoryPreserve=yes`,
  `ConfigurationDirectory`, Härtung laut §10, `Restart=always`,
  `WatchdogSec=180`, `TimeoutStopSec=60`, `MemoryMax=256M`, `ExecStopPost`
  laut §6.7). Keine Socket-Unit: `systemctl stop fw-alarm` heißt „aus“.
  `cloudflared` bleibt bis zum Aufräumen unverändert.
- `server/deploy.sh <rev>`: baut auf dem PC für `linux/arm64` aus genau
  diesem Git-Stand (`git archive`), sichert zuerst, lädt in einen Ordner im
  Home der VM, vergleicht die Prüfsumme, legt das neue Programm als
  root-eigene Datei neben das alte (ausführbar für `fwalarm`, nicht im Home
  des Admins), lässt das NEUE Programm `check-config` und `check-state`
  gegen die echten Dateien laufen (scheitert eines, bleibt das alte Programm
  – so landet auch ein Zurückgehen auf einen älteren Stand nie im
  Notbetrieb), hebt das alte Programm auf, tauscht in einem Schritt, startet
  neu, fragt `fw-alarm health` und `fw-alarm version`. Meldet `health`
  danach keinen gesunden Server, legt das Skript das alte Programm von
  selbst zurück und startet erneut – hinter dem Server steht kein zweiter
  mehr. Letzte Zeile: `deployed <sha>: fw-alarm sha256 <…> ok`. Vor dem
  Umschalten (es gibt noch keinen Dienst und keinen Zustand) legt
  `--install-only` nur das Programm ab: ohne Sicherung, ohne Prüfungen, ohne
  Neustart. Die alten Skripte `server/deploy.sh` und `server/backup.sh`
  weigern sich, sobald `fw-alarm` läuft.
- `server/backup.sh` ruft `fw-alarm flush` und `check-state` auf der VM und
  holt dann `/var/lib/fw-alarm` und `/etc/fw-alarm/config.toml`; ein Archiv
  zählt erst nach der Prüfung (lesbar, Empfängerliste und Konfiguration
  enthalten). Es enthält auch die Sitzungen.
- **Zurückspielen einer Sicherung:** `systemctl stop fw-alarm`; den
  heutigen Ordner beiseitelegen (nicht löschen); die Sicherung an seine
  Stelle (Besitzer `fwalarm`, 0700); `fw-alarm check-state`;
  `fw-alarm state-bump` (Rücksprung-Regel – sonst kennte die Box Nummern
  mit anderem Inhalt); `systemctl start fw-alarm`; `fw-alarm health`. Danach
  Empfänger und Stumm-Zustände ansehen: Was seit der Sicherung geändert
  wurde, fehlt.
- `server/check.sh` prüft von innen (`fw-alarm health`, Rechte der Ordner,
  Stand = eingespielter Stand, Nginx und PHP-FPM weder aktiv noch
  eingeschaltet, auf dem Port lauscht `fw-alarm`) und von außen (wie die Box: TLS 1.2, ohne
  `Accept`).
- Das Runbook (`server/README.md`) in einfachen Worten: was Pflicht ist, was
  freiwillig, genaue Befehle, Tabelle „Meldung → wo nachsehen“.

## 14. Was dem Umsetzenden überlassen bleibt

Feinschnitt der Pakete und Namen; Form der Fehlerwerte; die genaue Liste
der Prüfstand-Helfer (abgeleitet aus den rund 125 heutigen Stellen); Details
der systemd-Härtung; wie der Gleichlauf Antworten normalisiert; ob
versionierte statische Dateien länger gecacht werden dürfen (heutiges
Verhalten ist die Untergrenze).

## 15. Festlegungen, die ich getroffen habe

Der Nutzer hat den Entwurf am 17.11.2026 freigegeben, mit der Auflage einer
unabhängigen Durchsicht (§16). Punkte 1–12 sind die freigegebenen; wo die
Durchsicht einen davon verändert hat, steht es dabei. 13–17 kamen durch sie
hinzu.

1. Neubau innen (Ansatz B); außen bleibt alles gleich (§4).
2. Ablage als wenige JSON-Dateien, keine Datenbank.
3. Der Wächter läuft im Server statt als eigener Dienst am Timer: voller
   Lauf weiter alle 15 min, „Box offline/wieder da“ jede Minute – diese eine
   Meldung kommt dadurch früher, sonst ändert sich kein Zeitpunkt. Fällt der
   Server aus, greifen systemd, eine Absturz-Meldung und der Außen-Wächter
   (§6.7).
4. Rückweg auf PHP nur noch als Notbremse mit Verlust der Änderungen seit
   dem Umschalten – **geändert:** nur am Abend des Umschaltens, mit fester
   Schrittfolge (§12.5); danach wird nach vorn repariert. Aufgeräumt wird
   nach 3 ruhigen Tagen statt nach 14.
5. Konfiguration als `/etc/fw-alarm/config.toml`, Daten unter
   `/var/lib/fw-alarm`.
6. Vorhandene Passwörter gelten weiter; neue erzeugt `fw-alarm
   hash-password`.
7. Fremdbibliotheken, im Repo mitgeliefert – **geändert:** vier Module statt
   zwei (`x/crypto` braucht `x/sys`, das Passwort ohne Echo `x/term`; dazu
   `toml`). Drei davon stammen vom Go-Team.
8. `nas_dashboard/` heißt am Ende `web/`.
9. Gebaut wird auf dem PC; beim Umschalten melden sich alle einmal neu an;
   Passkeys erst nach dem Aufräumen (unverändert).
10. Die Oberfläche bleibt, wie sie ist (kein TypeScript, kein Build-Schritt)
    – **ergänzt:** zwei neue Verlaufs-Texte in `logic.js` (§7).
11. Anmelden, Alarm und die Box laufen auch dann weiter, wenn der Server
    nichts speichern kann; nur Änderungen (Empfänger, Stummschaltung,
    Einstellungen, Befehle an die Box) werden dann abgelehnt.
12. Die Firmware ist ein eigener Schritt danach, passend zum Flash in ca.
    4 Wochen (§12a): Zertifikate prüfen, saubere Adressen.
13. Der Server lauscht selbst (kein von systemd übergebener Port); nach dem
    Aufräumen nur noch auf einem Unix-Socket für den Tunnel.
14. Ist die Konfiguration fehlerhaft, enden alle Sitzungen, und der Admin
    wird gewarnt; die letzte gültige Fassung gilt weiter – auch über einen
    Neustart.
15. Der Server hält eine eigene Not-Kopie der Empfängerliste für den Alarm
    aktuell; die Not-Liste der Konfiguration wird beim Umschalten
    aufgefrischt.
16. Fünf neue Meldungen an den Admin für Störungen des Servers selbst – alle
    unter der vorhandenen Art „Dashboard defekt“ (§7).
17. GitHub prüft bei jedem Push mit (§10) – vom Nutzer mir überlassen
    („you decide whats best“, 17.11.2026).
18. Probe-Sperre und ersetzte Bark-Keys in jeder Kopie echter Daten (§10) –
    auf die Vorgabe des Nutzers vom 17.11.2026, dass bei Proben unter keinen
    Umständen jemand anderes als er alarmiert werden kann.

## 16. Durchsicht vom 17.11.2026

Eine unabhängige Durchsicht (auf Wunsch des Nutzers, vor dem Plan) hat den
Ansatz bestätigt und die Angaben zu Box, Brücke und Außen-Wächter gegen
deren Quelltext geprüft. Sie fand zwei Fehler und fünfzehn offene Punkte;
alle sind oben eingearbeitet:

| Befund | Wo gelöst |
|---|---|
| Mit einem von systemd übergebenen Port hieße „Dienst anhalten“ nicht „aus“ – Rückweg und Zurückspielen wären gescheitert | §6.4, §12.5, §13: der Server lauscht selbst |
| Tippfehler in der Konfiguration ließe alte Passwörter und Sitzungen still gelten und würde beim nächsten Neustart zum Ausfall | §6.8 |
| Fristen beim Sichern: verspätet fertig, Schlange, Budget je Anfrage | §6.2 |
| Stand springt zurück (Rückfall, Sicherung): Versionen, ids, Befehle | §6.3 Rücksprung-Regel, §13 |
| Ein Fehler irgendwo im Zustand träfe den Alarm; veraltete Not-Liste | §6.3 zurücklesen vor dem Umbenennen, Not-Kopie; §9 |
| Verlauf und Flüchtiges bei hartem Abbruch; „Alarm gestartet“ | §6.2 `outbox`, §6.7.4, §11.6 |
| Ein Prozess für alles; „unveränderlich“ nur Abmachung | §6.2 tiefe Kopie, Fehler abfangen; §10 Race-Detektor |
| Prüf-Lauf: erster Lauf, Lebenszeichen, Wiederholung, Text der ersten „Box OFFLINE“ | §6.5 |
| Reihenfolge und Lücken beim Umschalten; Rückweg nicht harmlos | §12.5 |
| Übernahme durfte Einträge „verwerfen“ | §9 |
| Konfiguration wurde bei jeder Anfrage auf der Platte geprüft | §6.8 |
| Zwei Passwortprüfungen zugleich als Hebel gegen den Admin | §6.8 |
| Lokaler Benutzer kann sich als Tunnel ausgeben | §10 Unix-Socket |
| Wie Tests Störungen herstellen; Regeln ohne Test | §11.2, §11.3, §11.6, §11.10 |
| Messung für Oracles 20-%-Regel | §11.11 |
| Neue Meldungen außerhalb der Regeln; Texte, die nicht mehr stimmen | §7, §12.6, §12a |

## 17. Durchsicht des Plans vom 17.11.2026

Vor dem ersten Go-Code hat eine zweite unabhängige Durchsicht den
Umsetzungsplan gegen diesen Entwurf und den Quelltext geprüft (2 Fehler,
16 offene Punkte). Was davon den Entwurf selbst berührt, ist oben
eingearbeitet:

| Befund | Wo gelöst |
|---|---|
| Nginx und PHP-FPM wären nach dem Umschalten nur angehalten gewesen – nach einem Neustart der VM hätte PHP wieder aus eingefrorenen Daten geantwortet | §12.5 Schritt 4, §13 `check.sh` |
| Das Sichern hätte beim ersten Sichern nach einem Rückfall die einzige gute Datei gelöscht | §6.3 Sichern, Start-Tabelle |
| Anmeldung unbekannter Benutzer gegen einen festen Wegwerf-Hash | §8 |
| Probe auf der VM: geschlossenes Bark-Ziel nur von Hand eingetragen | §11.10 |
| Geister-„Box OFFLINE“ beim Umschalten (Halt der Brücke, Lebenszeichen der Übernahme) | §9, §6.5, §12.5 Schritt 7 |
| Erste „Box OFFLINE“ über die Brücke: Außen-Wächter nie gesehen; Obergrenze 3 min | §6.5 |
| Einmalige Meldungen gingen bei gescheitertem Versand verloren | §7 |
| Übernahme: Listen-Version ohne Hochwassermarke und Version der Box | §9 |
| „Fehlerhaft“ und „Hinweis“ bei der Konfiguration nicht getrennt; halb geschriebene Datei | §6.8 |
| `stored` in `alive.php` | §4.4 |
| GitHub prüfte erst vor der Probe auf der VM | §10 (Push nach jeder Aufgabe) |
| `--install-only`, alte Skripte nach dem Umschalten | §13 |

Nach der Durchsicht, auf Vorgabe des Nutzers am selben Abend hinzugekommen:
§10 „Kein Test erreicht ein echtes Handy“ (ersetzte Bark-Keys in jeder Kopie
echter Daten, Probe-Sperre beim Umschalten, Abfrage „wen erreichte ein Alarm
jetzt“ vor dem gemeinsamen Demo-Alarm).

