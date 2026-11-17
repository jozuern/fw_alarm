# Der Dashboard-Server in Go — Design

Stand: 16.11.2026. Plan dazu: `docs/superpowers/plans/2026-11-16-go-server.md`.
Betrifft nur die Server-Seite des Dashboards (`nas_dashboard/` auf der
Oracle-VM). Firmware, NAS-Brücke und Außen-Wächter bleiben, wie sie sind.

## 1. Absicht und Warum

Der Server des Dashboards (heute PHP 8.3 hinter Nginx und PHP-FPM, dazu ein
Wächter-Skript per Timer) wird durch **ein** Programm in Go ersetzt:
`fw-alarm`. Von außen ändert sich nichts – dieselben Adressen, dieselben
Antworten, dieselben Dateien im Datenordner, dieselbe Oberfläche.

Gründe des Nutzers: Sorge vor PHP-Lücken, „alte Sprache“. Was die Umstellung
tatsächlich bringt (und woran sie sich messen lässt):

- **Weniger bewegliche Teile.** Ein Dienst statt Nginx + PHP-FPM-Pool +
  Wächter-Skript + deren Konfiguration; nichts im Webroot, das ein
  Interpreter ausführen könnte.
- **Fehler fallen vor dem Einspielen auf.** Der Übersetzer prüft Typen und
  Namen; heute gibt es für 6 700 Zeilen PHP weder eine statische Prüfung noch
  Unit-Tests.
- **Regeln werden direkt testbar** (z.B. „überfällig stumm zählt als laut“),
  auch die, die sich von außen nicht auslösen lassen (Speichern scheitert,
  zwei Anfragen gleichzeitig).

Was sie NICHT bringt: neue Funktionen. Passkeys folgen danach, in Go
(Entscheidung des Nutzers vom 16.11.2026).

## 2. Ziele, Nicht-Ziele, Anti-Ziele

**Ziele**

1. `fw-alarm` besteht `node --test "tests/backend/*.test.mjs"` und alle
   Szenarien unter `tests/dashboard/e2e/` – dieselben Tests wie die
   PHP-Fassung, unverändert.
2. Die Go-Fassung arbeitet auf dem **vorhandenen Datenordner** ohne
   Umwandlung, und die PHP-Fassung kann danach wieder darauf arbeiten
   (Rückweg ohne Datenverlust).
3. Box (alte UND neue Firmware), NAS-Brücke, Außen-Wächter und die
   Kurzbefehl-Links der Kollegen merken von der Umstellung nichts.
4. Umschalten und Zurückschalten dauern Sekunden und brauchen keinen Eingriff
   an Box, NAS oder Cloudflare.
5. Am Ende laufen auf der VM weder PHP noch Nginx.

**Nicht-Ziele**

- Keine Änderung an Oberfläche (`assets/`), Firmware, `nas_bridge/`,
  `cloud_watcher/`.
- Kein neues Datenformat, keine Datenbank, keine Umbenennung der
  Laufzeitdateien oder Adressen („Namen sind historisch“ gilt weiter).
- `classic.php` (die eingefrorene alte Oberfläche) wird nicht übertragen; sie
  entfällt vorher (siehe §12).
- Passkeys, Docker, CI-Dienste, npm.

**Anti-Ziele** (so wäre es gescheitert, auch wenn es „läuft“)

- Ein Alarm geht wegen der Umstellung nicht, doppelt oder an die falschen
  Empfänger raus – auch nicht in den Tests auf der VM.
- Ein Kollege braucht einen neuen Kurzbefehl-Link.
- Die Box bekommt vom Server ein 403 (sie warnte dann „Token abgelehnt“).
- Eine der „muss so bleiben“-Regeln aus `CLAUDE.md` geht still verloren, weil
  kein Test sie hielt.
- Die VM fällt unter die 20-%-Grenze von Oracle und wird als unbenutzt
  gestoppt.

## 3. Ausgangslage

| Teil | Heute |
|---|---|
| Anwendung | `nas_dashboard/`: 17 PHP-Dateien, 6 728 Zeilen (`lib/common.php` 3 156 mit 123 Funktionen, `dashboard_api.php` 1 267, `cron/check_offline.php` 571) |
| Oberfläche | `nas_dashboard/assets/` (Preact/htm, 5 883 Zeilen JS, ein Stylesheet), kein Build-Schritt |
| Server | Oracle-VM, Ubuntu 24.04 **aarch64**, 1 GB; Nginx `127.0.0.1:8080` → PHP-FPM-Pool `fw-alarm` (Benutzer `fwalarm`); `cloudflared` → `http://127.0.0.1:8080`; Timer `fw-alarm-cron` alle 15 min |
| Daten | `/srv/fw-alarm/data`: JSON-Dateien `<name>.php` mit Guard-Zeile, `commands.log.php`, Lock-Dateien; Sitzungen als PHP-Dateien in `/srv/fw-alarm/sessions` |
| Konfiguration | `/srv/fw-alarm/app/config.php` (PHP-Array, nur auf der VM) |
| Tests | `tests/backend/` (Server von außen, neu), `tests/dashboard/` (Logik + Chrome-Szenarien), `tests/bridge/`, `tests/watcher/`, `tests/server/` |
| Entwicklungs-PC | Windows, PHP 8.3 und Node 24 vorhanden, **Go noch nicht installiert** |

## 4. Was die Umstellung prägt (Fakten mit Folgen)

1. **Die Adressen enden auf `.php` und bleiben so.** Die Box ruft
   `…/api/status.php` usw. (bis zum Flashen über die Brücke), in den
   Kurzbefehlen der Kollegen steht `…/api/mute.php?t=…`. Go bedient genau
   diese Pfade.
2. **Die Brücke wertet Antworten aus:** Ein 403 reicht sie nur weiter, wenn
   der Rumpf genau `FORBIDDEN` ist; ihre Zeitlimits sind 3 s/3,5 s, die der
   Box 4 s. Antworten an die Box müssen deshalb weiter sofort kommen;
   Bark-Meldungen gehen erst nach der Antwort raus.
3. **Die Box im Betrieb hat alte Firmware** (ohne `alarm_seq`, `alarm_source`
   …) und wird irgendwann geflasht – vor oder nach dieser Umstellung
   (offen, §13). Der Server muss mit beiden Ständen und mit/ohne Brücke
   (`X-FW-Bridge`) richtig arbeiten. Die Tests prüfen beides.
4. **PHP ist beim Lesen nachsichtig, Go nicht.** In den Dateien steht, was
   PHP über Monate geschrieben hat: leere Listen `[]`, wo ein Objekt gemeint
   ist; Wahrheitswerte als `true`, `1` oder `"1"`; Zahlen als Zeichenketten.
   Go muss so nachsichtig lesen wie PHP (`!empty()`, `(int)`), sonst
   scheitert es erst an den echten Daten.
5. **PHP schreibt unbekannte Felder zurück, typisierte Go-Strukturen
   verlören sie.** Ein Eintrag der Empfängerliste trägt Felder, die der Code
   nur teilweise kennt (`added_at`, künftig mehr). Go behält beim Ändern
   alles, was es nicht kennt.
6. **PHP bedient im Test nur eine Anfrage zugleich, Go viele.** Regeln über
   Gleichzeitigkeit (zwei Polls, zwei Anmeldeversuche, Alarm während eines
   Listen-Wechsels) wurden nie Ende-zu-Ende geprüft. In Go sind sie der
   wahrscheinlichste Ort für neue Fehler.
7. **Sitzungen lassen sich nicht übernehmen** (PHP-eigenes Format). Beim
   Umschalten melden sich alle einmal neu an – auch die Home-Bildschirm-App.
8. **Oracle stoppt VMs, deren CPU, Netz UND Speicher 7 Tage unter 20 %
   liegen.** Heute ca. 40 % Speicher. Go braucht weniger als Nginx +
   PHP-FPM – wie viel, wird vor dem Umschalten auf der VM gemessen (§12).
9. **Nach einer Antwort weiterarbeiten** ist in PHP ein Sonderweg
   (`fastcgi_finish_request`), in Go der Normalfall – aber ein Neustart des
   Dienstes darf laufende Sendungen nicht abschneiden.
10. **Der Wächter prüft heute den Server von nebenan** (eigener Prozess,
    ruft `api/health.php` über HTTP). Liefe er im Server-Prozess mit, fiele
    er mit ihm aus.

## 5. Ansätze

**A – Alles auf einmal ersetzen, ohne Rückweg.** Go schreibt ein eigenes,
„sauberes“ Datenformat, Daten werden einmal umgewandelt. Einfachster Code,
aber kein Zurück nach dem ersten geschriebenen Datensatz, und die
Umwandlung ist ein eigener Fehlerherd. Verworfen: widerspricht Ziel 2.

**B – Stück für Stück hinter Nginx (einzelne Adressen wandern nach Go).**
Jeder Schritt klein und einzeln rückholbar. Dafür laufen PHP und Go wochenlang
gleichzeitig auf denselben Dateien, Anmeldung und Dashboard-API müssten
trotzdem gemeinsam wandern (eine Sitzung), und jede Zwischenstufe wäre ein
eigener, nie wiederkehrender Zustand, den man testen müsste. Verworfen: mehr
Zustände als Nutzen.

**C – Gleiches Verhalten, gleicher Datenordner, ein Umschalten, PHP bleibt
als Rückweg liegen (gewählt).** Go wird vollständig neben der PHP-Fassung
entwickelt und gegen dieselben Tests geprüft. Auf der VM wird nur umgestellt,
wohin Nginx die Anfragen reicht und was der Wächter-Timer startet; beides
zurückzudrehen stellt die PHP-Fassung wieder her. Erst nach zwei ruhigen
Wochen verschwinden Nginx und PHP. Preis: Go muss die Eigenheiten der
PHP-Dateien mittragen (§4.4, §4.5) und die Sperrdateien genauso benutzen.

## 6. Aufbau der Go-Fassung

Ein Go-Modul im Repo-Wurzelordner (`go.mod`, Modulname `fwalarm`), ein
Programm `cmd/fw-alarm` mit vier Aufrufen:

| Aufruf | Zweck |
|---|---|
| `fw-alarm serve` | der HTTP-Server (Dauerbetrieb, systemd) |
| `fw-alarm watchdog` | EIN Lauf des Wächters, gibt die bekannte Zeile aus (`WEB_OK \| OK \| …`) – vom bestehenden Timer gestartet |
| `fw-alarm check-config` | prüft die Konfiguration, nennt Fehler ohne Geheimnisse |
| `fw-alarm version` | Git-Stand, mit dem gebaut wurde |

Der Wächter bleibt ein **eigener Prozess** (§4.10): Hängt der Server, läuft
der Wächter weiter und meldet „Dashboard DEFEKT“. Beide Prozesse teilen sich
die Dateien über dieselben Sperrdateien wie heute (`flock` auf
`<name>.lock.php`).

Pakete unter `internal/` (jedes mit eigenen Unit-Tests):

| Paket | Inhalt (entspricht in PHP) |
|---|---|
| `phpval` | nachsichtiges Lesen wie PHP: `Int`, `Str`, `Truthy`, Kürzen auf Bytes |
| `store` | Datenordner: Guard-Dateien lesen/atomar schreiben, `.bak`, Sperren, Verlauf anhängen/rotieren/rückwärts lesen, Schreib-Selbsttest |
| `config` | Konfiguration laden, prüfen, bei Änderung neu laden |
| `bark` | Versand an mehrere Keys gleichzeitig mit Runden, ASCII-Umsetzung, Felder für Alarm/Entwarnung/Test |
| `keys` | Empfängerliste: Zustand, Sicherung, Versionen/ids, effektive Liste, Stumm-Regeln, Demo-Modus, Reparatur |
| `notify` | Arten und Einstellungen der Admin-Meldungen, Ruhezeit, Versand |
| `logview` | Tabelle der Verlaufs-Ereignisse, Filter, Ausgabe pro Rolle |
| `command` | Befehle an die Box (anlegen, ausliefern, quittieren, verfallen) |
| `web` | HTTP-Rahmen: Kopfzeilen, Formulare, Proxy/Absender, Bremse, Sitzung, Anmeldung, CSRF, Seiten-Hüllen, statische Dateien |
| `box` | `api/status.php`, `poll.php`, `ack.php`, `keys.php` |
| `mutelink` | `api/mute.php` samt Browser-Seiten |
| `dashapi` | `dashboard_api.php`: Tabelle der Aktionen und alle Handler |
| `watch` | Wächter-Lauf, `api/health.php`, `api/alive.php` |

Abhängigkeiten: Standardbibliothek plus `golang.org/x/crypto` (bcrypt für
die vorhandenen Passwort-Hashes). Mitgeliefert im Repo (`vendor/`), damit
ein Bau kein Netz braucht. Go-Version: die zur Umsetzung aktuelle stabile,
mindestens 1.23, in `go.mod` festgeschrieben. Zeitzonen-Daten sind
eingebaut (`time/tzdata`), `CGO_ENABLED=0`.

Die Oberfläche bleibt in `nas_dashboard/assets/` und wird in das Programm
eingebettet (`embed`): eine Datei `nas_dashboard/webassets.go` (Paket
`webassets`). Der Versions-Parameter `?v=` der Skripte ist dann ein
Kurz-Hash des Inhalts statt der Dateizeit.

## 7. Gleichheit mit der PHP-Fassung

Maßgeblich sind die Tests. Was sie nicht zeichengenau festhalten, regelt
diese Liste.

**Adressen.** `/` und `/index.php`, `/dashboard_api.php`,
`/api/{status,poll,ack,keys,mute,alive,health}.php`, `/manifest.json`,
`/assets/…`. `/classic.php` leitet mit 302 auf `index.php` (alte
Lesezeichen, die Brücke verweist noch dorthin). Alles andere: 404 – auch
`/lib/`, `/cron/`, `/data/`, `/config*`.

**Kopfzeilen.** Jede dynamische Antwort trägt die Kopfzeilen aus
`lib/common.php` (CSP ohne `unsafe-inline`, `X-Frame-Options: DENY`,
`Cache-Control: no-store`, `nosniff`, Referrer-/Permissions-Policy,
`X-Robots-Tag`), HSTS nur bei HTTPS laut vertrautem Proxy. Statische
Dateien: `nosniff`, richtiger Typ.

**Formulare wie PHP.** Felder aus `application/x-www-form-urlencoded` UND
`multipart/form-data` (Test-Werkzeuge senden so); bei doppelten Feldern
zählt das LETZTE; das Maschinen-Token zählt nur aus dem Rumpf, nie aus der
Adresse; Rumpf höchstens 64 kB. Nicht übernommen wird PHPs Umschreiben von
Feldnamen (`a.b` → `a_b`, `x[]`): Die Firmware sendet nur einfache Namen.

**Daten.** Dieselben Dateinamen, dieselbe Guard-Zeile, dieselben Felder.
Lesen nachsichtig (§4.4), Schreiben kanonisch (echte Wahrheitswerte und
Zahlen, UTF-8 statt `ä`, Reihenfolge der Schlüssel beliebig) – beides
liest PHP problemlos. Unbekannte Felder bleiben erhalten (§4.5). Wo PHP sich
auf die Reihenfolge eines Objekts verließ (`devices_seen`, `alarm_seen`,
`recent_types`), sortiert Go ausdrücklich. Nicht mehr geschrieben wird
`log_cache.json` (nur eine Beschleunigung für das NAS).

**Texte.** Alle Meldungen, Bark-Texte (ASCII) und Seiten wortgleich. Was
PHP, Nginx oder `config.php` beim Namen nennt („PHP-cURL fehlt“,
`systemctl status nginx php8.3-fpm`, `php8.3-curl` und die rund ein Dutzend
Hinweise „… in config.php …“ in Meldungen und Oberfläche), ändert sich erst
beim Aufräumen (§12, Schritt 5) – bis dahin meint „config.php“ in einer
Meldung die Datei `/etc/fw-alarm/config.json`.

**Sperre nicht zu öffnen.** Wo PHP hart abbricht (`with_lock()`: alle
Dashboard-Aktionen, der Status-Push), antwortet Go genauso: HTTP 500, für
die Dashboard-API als JSON mit `code: storage`, sonst als Klartext – immer
derselbe Satz ohne Pfad und ohne Benutzernamen. Wo PHP nachsichtig ist
(`with_lock_soft()`: Poll, Quittung, Kurzbefehl-Link, Alarm-Versand,
Bremse, Verlauf), bleibt Go es auch.

**Statische Dateien.** Inhaltstypen aus einer festen Tabelle im Programm
(wie Nginx sie sendete: `.js` → `application/javascript`, ohne `charset`),
nie aus dem Betriebssystem – unter Windows wären sie sonst andere als auf
der VM.

**Zeit und Zahlen.** Zeitzone aus der Konfiguration (Standard
Europe/Berlin), Formate wie `date('[d.m.Y H:i] ')`; Runden kaufmännisch wie
`round()`; Kürzen auf Bytes mit Ersatzzeichen für angeschnittene Umlaute.

**Nicht übernommen:** `fix_owner()` (der Wächter läuft nicht mehr als root),
der HTTP-Aufruf des Wächters (`POST /cron/check_offline.php`), `classic.php`.

## 8. Sitzungen und Anmeldung

Gleiche Regeln wie heute (Cookie `boss925_dashboard`, `HttpOnly`,
`SameSite=Strict`, `Secure` laut Proxy, 12 h, bei jeder angemeldeten Anfrage
erneuert; Sitzung nur für Angemeldete; an die Zugangsdaten gebunden; neue ID
bei jeder Anmeldung; Abmelden löscht Sitzung und Cookie; `Sec-Fetch-Site`;
Bremse pro Absender vor der Passwortprüfung; `sleep(1)` nach Fehlversuch).

Neu ist nur die Ablage: eine Datei pro Sitzung in `/srv/fw-alarm/sessions`,
benannt nach dem SHA-256 der Sitzungs-ID (wer den Ordner lesen kann, hat
damit keine verwendbare ID), Inhalt JSON (`user`, `role`, `csrf`,
`auth_stamp`, `last_seen`). Sitzungen überstehen so einen Neustart des
Dienstes – das Handy bleibt nach einem Update angemeldet. Abgelaufene
Dateien räumt der Server stündlich weg. `last_seen` wird höchstens einmal
pro Minute geschrieben.

Passwort-Hashes: bcrypt (`$2y$`, `$2a$`, `$2b$`), wie `password_hash(…,
PASSWORD_DEFAULT)` sie erzeugt. Ein anderes Format lehnt `check-config` ab.

## 9. Konfiguration

`/etc/fw-alarm/config.json` (root:fwalarm 0640), dieselben Schlüssel wie
`config.php`, dazu zwei neue: `session_dir` (Ordner der Sitzungsdateien) und
`listen` (Adresse, falls systemd keinen Port übergibt). Format: JSON; ganze
Zeilen, die mit `//` beginnen, sind Kommentare – so behält die Vorlage
`server/config.example.json` ihre Erklärungen. Unbekannte Schlüssel sind ein
Fehler (Tippfehler fallen auf). Die Umwandlung der vorhandenen `config.php`
erledigt einmalig ein Befehl auf der VM (PHP ist dann noch da).

Fehlt ein Schlüssel, gilt der Standard, den der **PHP-Code** dafür nimmt
(das `?? …` im Quelltext) – nicht der Wert der Vorlage. Die echte Datei
entsteht ja aus der echten `config.php`, und der dürfen neuere Schlüssel
fehlen; das Verhalten soll sich beim Umschalten nicht ändern (Beispiel:
ohne `bark_alarm_title` heißt der Alarm `FEUERWEHR-ALARM`, nicht wie in der
Vorlage `FEUERWEHR-ALARM (manuell)`). Einzige Ausnahme: `health_check_url`
zeigt ohne Eintrag auf die eigene Lausch-Adresse. Werte liest Go so
nachsichtig wie PHP (`"180"` als Zahl, `1` als ja).

Solange PHP als Rückweg liegt (§12, Schritt 4 bis 5), wird **jede Änderung
in beiden Dateien** gemacht – sonst gälte nach einem Zurückschalten z.B.
wieder ein altes Passwort.

Wie heute wirkt eine Änderung ohne Neustart: Der Server liest die Datei neu,
sobald sich ihre Änderungszeit oder Größe geändert hat (geprüft bei jeder
Anfrage – ein Dateisystem-Aufruf). Ist sie
fehlerhaft, gilt die letzte gute weiter und der Fehler steht im Journal; der
Wächter (startet jedes Mal frisch) gibt dann `KONFIGURATION FEHLERHAFT …`
aus und endet mit Fehler. Im Runbook steht deshalb: nach jeder Änderung
`sudo fw-alarm check-config`.

`health_check_url` bleibt (der Wächter prüft den Server weiter über HTTP).

## 10. Betrieb auf der VM

**Dienste.** `fw-alarm.socket` hält den Lausch-Port, `fw-alarm.service`
(Benutzer `fwalarm`, `Restart=always`, nur Datenordner und Sitzungsordner
beschreibbar) bedient ihn – so geht bei einem Neustart des Dienstes keine
Anfrage verloren. Beim Beenden lässt der Server laufende Anfragen und
ausstehende Bark-Meldungen bis zu 40 s zu Ende laufen (wie heute
`process_control_timeout`). `fw-alarm-cron.service` startet `fw-alarm
watchdog` statt PHP; der Timer bleibt.

**Ports.** Während PHP als Rückweg liegt: Go auf `127.0.0.1:8081`, Nginx
(`127.0.0.1:8080`) reicht alles dorthin durch. Nach dem Aufräumen: Go
selbst auf `127.0.0.1:8080`, Nginx entfällt, `cloudflared` bleibt
unverändert.

**Protokolle.** Nach `journald` (`journalctl -u fw-alarm`): Zugriffe im
heutigen Format – Zeit, Methode, **Pfad ohne Query**, Status, Dauer,
`bridge=1`, User-Agent – und Fehlermeldungen. Nie ein Token, nie ein
Klartext-Key.

**Einspielen.** `server/deploy.sh <rev>` baut auf dem PC für
`linux/arm64` aus genau diesem Git-Stand (sauberer Auszug, `-trimpath`),
sichert wie bisher zuerst die Daten, lädt das Programm in einen Ordner im
Home der VM (nie `/tmp`), vergleicht die Prüfsumme auf der VM mit der des
PCs, tauscht `/usr/local/bin/fw-alarm` in einem Schritt und startet den
Dienst neu. Ausgabe am Ende: `deployed <sha>: fw-alarm sha256 <…> ok`.
`fw-alarm version` auf der VM nennt denselben Stand.

**Sicherung.** `server/backup.sh` holt zusätzlich `config.json` (solange
es `config.php` gibt, beide).

## 11. Prüfungen vor dem Umschalten

1. **Server-Tests**: `FW_BACKEND=go node --test "tests/backend/*.test.mjs"`
   – alle grün, ohne dass eine Testdatei dafür geändert wurde (Ausnahmen
   nennt der Plan einzeln).
2. **Oberflächen-Szenarien** unter `tests/dashboard/e2e/` gegen Go (die
   Sandbox bekommt dafür denselben Treiber-Schalter).
3. **Go-Unit-Tests** für das, was von außen nicht auslösbar ist: Speichern
   scheitert (kein Befehl ohne gespeicherten Zustand; Alarm ohne
   Schreibrecht; Bremse und Verlauf nicht fatal), Gleichzeitigkeit (§4.6),
   alte Datenformen (§4.4), Sommer-/Winterzeit.
4. **Paritätslauf**: PHP und Go nebeneinander auf Kopien desselben
   Datenordners, dieselbe Folge von Anfragen, Antworten nach Normalisierung
   (Zeiten, Reihenfolge der Schlüssel) gleich. Einmal mit einem künstlichen
   Datenordner im Repo, einmal **auf dem PC mit einer Kopie der echten
   Daten** aus der jüngsten Sicherung (Bark zeigt dabei auf die Attrappe;
   die Kopie liegt in einem Ordner nur für den Nutzer und wird danach
   gelöscht).
5. **Sicherheits-Durchsicht** in einer eigenen Sitzung (wie beim Umzug).
6. **Staging auf der VM**: Go auf Port 8082 mit einer Kopie des
   Datenordners und einem Bark-Ziel, das nichts annimmt; `server/check.sh
   local 8082` dagegen; Speicherbedarf messen (§4.8).

## 12. Ablauf und Rückweg

1. **Vorarbeiten ohne Wirkung auf den Betrieb:** `classic.php` entfernen
   (eigener kleiner Schritt, noch in PHP), Sandbox treiberfähig machen, Go
   auf dem PC einrichten.
2. **Go-Fassung bauen**, bis §11.1–4 erfüllt sind. Der Betrieb merkt nichts.
3. **Staging** (§11.6) und Messung. Liegt der erwartete Speicherbedarf der
   VM unter 25 %, wird NICHT umgeschaltet, sondern mit dem Nutzer
   entschieden (Abschnitt 4, Punkt 8).
4. **Umschalten:** `config.json` aus `config.php` erzeugen und prüfen,
   Dienst starten, Nginx auf Go umstellen, Wächter-Timer auf Go umstellen.
   Danach nur harmlose Prüfungen (§14). Alle melden sich neu an.
   **Rückweg**, solange Schritt 5 nicht gelaufen ist: die zwei Dateien
   zurücktauschen (`nginx -s reload`, Timer-Unit) – Sekunden, keine Daten
   gehen verloren; alle melden sich erneut an.
5. **Aufräumen, frühestens 14 Tage nach Schritt 4 ohne Störung:** Go
   übernimmt Port 8080, Nginx und PHP werden entfernt, die PHP-Dateien
   verlassen das Repo, Texte/Doku/Wortlaut-Tests werden angepasst, der
   Paritätslauf entfällt. Ab hier gibt es keinen Rückweg zur PHP-Fassung
   mehr – nur noch `git revert` und ein altes Programm.

## 13. Reihenfolge mit Flashen und Passkeys

- **Flashen der Box: davor oder danach, beides geht.** Der Server arbeitet
  mit beiden Firmware-Ständen und mit oder ohne Brücke. Einzige Regel:
  zwischen Flashen und Schritt 4 liegen mindestens drei ruhige Tage, damit
  eine Störung einer der beiden Änderungen zuzuordnen ist. Der Abbau der
  Box-Pfade in der Brücke (Aufgabe 12 des Umzugsplans) ist davon unabhängig.
- **Passkeys erst nach Schritt 5.** Sie kämen nur in Go; solange PHP als
  Rückweg liegt, dürfte es sie nicht geben. Der vorhandene Plan
  (`2026-11-16-passkey-login.md`) wird dann für Go neu gefasst.

## 14. Sicherheit gegen versehentlichen Alarm

- Auf dem PC und im Staging zeigt `bark_host` immer auf eine Attrappe; echte
  Bark-Keys kommen dort nur in der Kopie der echten Daten vor (§11.4) und
  werden nie an `api.day.app` gesendet. Der Paritätslauf mit echten Daten
  bricht ab, wenn `bark_host` nicht auf `127.0.0.1` zeigt.
- Auf dem Live-System nach dem Umschalten: nie „Alarm an alle“,
  Entwarnung, Demo-Alarm oder `keys_test`. Erlaubt sind Status-Pushs (die
  Box sendet sie selbst), der TEST-Befehl, Vorschau-/HEAD-Aufrufe des
  Kurzbefehl-Links, der EIGENE Kurzbefehl-Link des Nutzers und ein Lauf des
  Wächters.
- Der erste echte „Alarm an alle“ über Go ist bewusst ein **Demo-Alarm an
  den Nutzer selbst** (Demo-Modus, er ist der Test-Empfänger), gemeinsam mit
  ihm, bevor Schritt 5 beginnt.

## 15. Was dem Umsetzenden überlassen bleibt

Innere Aufteilung der Pakete, Namen privater Funktionen, die Form der
Fehlerwerte, ob Vorlagen `html/template` oder Zeichenketten sind (die
Ausgabe muss gleich sein), Details der systemd-Härtung, wie der Paritätslauf
Antworten normalisiert.

## 16. Festlegungen, die ich getroffen habe (bitte prüfen)

1. Ansatz C: gleicher Datenordner, ein Umschalten, PHP 14 Tage als Rückweg.
2. Der Wächter bleibt ein eigener Prozess am Timer.
3. `classic.php` wird nicht übertragen, sondern vorher entfernt.
4. Konfiguration als `config.json` unter `/etc/fw-alarm/`, mit
   `//`-Kommentarzeilen, Änderungen wirken ohne Neustart.
5. Beim Umschalten (und bei einem Rückweg) melden sich alle neu an.
6. Am Ende ohne Nginx: `cloudflared` spricht direkt mit Go.
7. Gebaut wird auf dem PC (Go wird dort installiert), nicht auf der VM und
   nicht bei GitHub.
8. Eine Fremdbibliothek: `golang.org/x/crypto`, im Repo mitgeliefert.
9. Der Ordner `nas_dashboard/` behält seinen Namen (nur noch Oberfläche).
10. Passkeys erst nach dem Aufräumen.
