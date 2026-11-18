# Server-Tests (Maßstab für jede Fassung des Dashboard-Servers)

Diese Tests prüfen die Server-Seite von außen: was die Box, der Außen-Wächter,
ein Kurzbefehl und das Dashboard über HTTP sehen, was bei Bark ankommt und was
der Server gespeichert hat. Sie kennen keine PHP-Funktion und keine Datei beim
Namen – dieselben Tests muss auch die Go-Fassung bestehen, die gerade
entsteht (Treiber `harness-go.mjs`, siehe unten). Wer eine Regel der
Server-Seite ändert oder ergänzt, schreibt den Test dazu hier.

```bash
node --test "tests/backend/*.test.mjs"            # alles gegen PHP (ca. 1 min)
node --test "tests/backend/box-poll-ack.test.mjs" # eine Datei
bash tools/test-go.sh                             # gegen Go: die Dateien aus tools/go-green.txt
bash tools/test-go.sh tests/backend/session.test.mjs   # gegen Go: genau diese Datei
```

Voraussetzung: Node 24 und `php` (8.3) im Pfad, für Go zusätzlich Go (das
Skript findet die Installation pro Benutzer selbst). Kein Chrome, kein npm,
keine Sandbox von Hand: Jede Testdatei startet über `harness.mjs` ihr eigenes
Dashboard (`php -S` bzw. `fw-alarm serve` auf einem freien Port, leerer
Datenordner im Temp-Ordner) und eine Bark-Attrappe im Testprozess; am Ende
wird alles entfernt (`FW_KEEP=1` lässt den Ordner zum Nachsehen liegen).
Von Hand: `node tests/backend/serve.mjs [--port 8787]` startet dasselbe Dashboard
und gibt die Adresse aus (bis Strg+C). Dieselbe Fassung (`FW_BACKEND`) fahren
die Chrome-Szenarien (`tests/dashboard/e2e/run.mjs`, README unter `tests/dashboard/e2e/`).

| Datei | Was sie festhält |
|---|---|
| `box-status.test.mjs` | Status-Push: Token, gespeicherte Felder, Relaisalarm und Neustart im Verlauf, veraltete Pushs, fremde Boards |
| `box-poll-ack.test.mjs` | Befehle: Klartext-Antwort, Auslieferung höchstens einmal, Quittung, Verfall, Nummern, Sicherungen des Demo-Alarms, Aufräumen im Poll |
| `box-keys.test.mjs` | Empfängerliste für die Box: Format, Stumm, Demo, Grenzen der Liste, Versionen und ids |
| `alive.test.mjs` | Lebenszeichen für den Außen-Wächter |
| `mute-link.test.mjs` | Kurzbefehl-Link: Text und Browser-Seite, Vorschauen, Bremse, dauerhaft stumm |
| `watchdog.test.mjs` | Wächter: Box offline/wieder da, Selbsttest, Erinnerungen, Außen-Wächter, wie Meldungen rausgehen |
| `alarm-send.test.mjs` | Alarm, Entwarnung und Test-Push direkt an Bark: Felder, Empfänger, bestätigter Modus, Teilausfall, Nachsendung |
| `session.test.mjs` | Anmeldung und Sitzung: Cookie, keine Sitzung für Unbekannte, `Sec-Fetch-Site`, Passwortwechsel, Abmelden, `classic.php`, Bremse gegen Durchprobieren, hinter einem Proxy |
| `dashboard-access.test.mjs` | Zugangsregeln aller Dashboard-Aktionen (Anmeldung, Methode, CSRF als Kopfzeile oder Feld, Rolle), Felder der Status-Antwort |
| `dashboard-keys.test.mjs` | Empfänger pflegen: umbenennen, Key ersetzen, Telefonnummer, neue Kurzbefehl-Links, Datenordner nicht beschreibbar |
| `dashboard-settings.test.mjs` | Einstellung „Automatisch wieder laut nach“: Stufen, Wert aus der Konfiguration, Verlauf |
| `dashboard-notify.test.mjs` | Meldungen an den Admin: Liste, Einstellen, Probe-Meldung, Meldungen bei der Anmeldung |
| `dashboard-log.test.mjs` | Verlauf im Dashboard: freigegebene Felder, Filter, Menge, rotierte Datei |
| `php-storage.test.mjs` | Nur PHP (unter einer anderen Fassung übersprungen): was allein an PHPs Ablage hängt (Sperren, kaputte Dateien, `.bak`, `health_check_url`, Wächter als eigener Prozess) und was die Go-Fassung bewusst anders regelt – die Fälle stehen unverändert so da wie in ihrer Herkunftsdatei |
| `php-health.test.mjs` | Nur PHP: Selbsttest des Datenordners (`api/health.php`): Antwort, Störungen, über einen Proxy 404 |
| `go-box.test.mjs` | Nur Go: Box-Protokoll, wo die Go-Fassung bewusst anders regelt – eine höhere Listen-Version der Box hebt die des Servers sofort darüber (nur die eingestellte Box) |
| `go-storage.test.mjs` | Nur Go: die Gegenstücke zu `php-storage.test.mjs` – was gilt, wenn nichts gesichert werden kann (der Status-Push gilt im Speicher und wird mit 200 bestätigt; der Poll antwortet 200 `type=NONE` und liefert einen Befehl erst aus, wenn „ausgeliefert“ gesichert ist) |
| `go-session.test.mjs` | Nur Go: keine gespeicherte Sitzung für Unbekannte (`sessions.json` des Servers), welche Antwort welches Cookie setzt – Sitzung und Geräte-Ausweis `boss925_device` (Sitzung zuerst), Abmelden löscht nur die Sitzung |
| `go-driver.test.mjs` | Nur Go: der Treiber selbst – Starten, Anhalten, hartes Beenden, zweiter Server auf demselben Ordner, Konfiguration und der Wächter, der nie ein fremdes `bark_host` schreibt, Neustarts ohne tote Verbindungen, jeder Helfer schreibt, was der Server annimmt, die Prüfung nach jedem Start, Sichern scheitern lassen |

## Regeln für neue Tests

- **Nur Beobachtbares prüfen:** HTTP-Antwort, Bark-Attrappe (`backend.bark`),
  Antworten der Dashboard-API (`session()`), gespeicherter Zustand über
  `backend.state` (Liste der Helfer und ihr Vertrag oben in `harness.mjs`,
  „ABSICHTS-HELFER“). Nie interne Namen der Implementierung, nie eine Datei
  des Servers.
- **Sagen, was man will – jeder Helfer mit `await`:** „Seit 20 Minuten
  offline“ entsteht durch `await backend.state.ageBoxStatus(1200)`, nicht
  durch Warten; Merker des Wächters über `ageMark`/`setMarks`, Stummschaltungen
  über `ageMute`, der laufende Befehl über `ageCommand`/`setCommand`, die
  Bremse am Kurzbefehl-Link über `ageMuteThrottle`, Verlaufs-Einträge über
  `ageLog` (als einziger relativ: „um s älter“). Herstellen: `seedRecipients`
  (nur gültige Einträge), `seedLog`, `setWatcher`. Lesen, wie gespeichert:
  `recipients()`, `recipient(id)`, `listVersion()`, `demo()`, `command()`,
  `log()`, `muteFailures()`, `notifyStored()` …
- **Speichern scheitern lassen:** `backend.storage.fail('selftest' | 'durable'
  | 'history' | 'volatile')` und `repair(...)`; was das bei PHP heißt, steht
  über `PHP_STORAGE` in `harness-php.mjs`, bei Go oben in `harness-go.mjs`.
- **Langer Verlauf:** `backend.state.seedLog([...])` legt Einträge roh in den
  Verlauf (`{ rotated: true }` = die ältere Datei) – statt hunderter Aktionen.
- **Texte mit dem Namen der Konfigurationsdatei** bauen die Erwartung aus
  `backend.words.configFile` (`config.php` bzw. `config.toml`).
- **Nur eine Fassung:** `php-*.test.mjs` prüfen, was allein an PHPs Ablage
  hängt, und fassen als einzige die Dateien von PHP an (`backend.data`);
  `go-*.test.mjs` prüfen, was sich nur die Go-Fassung fragen lässt, und
  fassen als einzige ihren Datenordner roh an (`backend.files`, dazu
  `hardKill()`, `writeRawConfig()`, `health()`, `cli()`). Eine
  solche Datei hängt alles an EIN `describe` mit
  `{ skip: BACKEND_KIND !== 'php' && 'nur PHP' }` (bzw. `'go'`). (Die
  Chrome-Szenarien unter `tests/dashboard/e2e/` nutzen `backend.data` noch,
  bis sie umgestellt sind.)
- **Abweichung nach Plan:** Wird ein gemeinsamer Fall unter Go rot, weil der
  Entwurf (`docs/superpowers/specs/2026-11-17-go-server-neubau-design.md`,
  §7) die Regel ändert, wird er NICHT verbogen: Er zieht unverändert und als
  Ganzes nach `php-*.test.mjs`, sein Gegenstück mit allem, was weiter gilt,
  kommt nach `go-*.test.mjs`, und die Tabelle „Rule → test“ im Plan bekommt
  eine Zeile. Eine Abweichung, die der Entwurf nicht nennt, ist ein Befund –
  anhalten und melden.
- **Anmelden:** `session(backend, READER)` für die API, `rawLogin(...)` für die
  rohe Antwort des Anmeldeformulars (Weiterleitung, Cookie, Fehlerseite).
- **Meldungen nach der Antwort:** Der Server verschickt manches erst, nachdem
  er geantwortet hat – dafür `waitFor(...)`, für „es darf nichts kommen“
  `settle()`.
- **Jeder Test für sich:** `beforeEach` setzt Datenordner, Konfiguration und
  Attrappe zurück (`backend.reset()`).
- **Alte und neue Firmware:** `box().status()` sendet die Felder des Stands
  auf `main`, `box().oldStatus()` die der Box im Betrieb (ohne `alarm_seq`,
  `alarm_source`, `core`, `max_alloc_heap`, `alarm_gaveup`).
- Der Test-Server von PHP bedient unter Windows nur eine Anfrage zugleich:
  Regeln über gleichzeitige Anfragen lassen sich hier nicht prüfen. Er
  antwortet außerdem erst, wenn auch die Arbeit „nach der Antwort“ erledigt
  ist (`fastcgi_finish_request()` gibt es nur unter PHP-FPM).
- Unter PHP übersprungen (`backend.kind === 'php'`, Hinweis in der Ausgabe):
  „länger als 12 Std ohne Anfrage“ (`session.test.mjs`, die Sitzungen von PHP
  liegen außerhalb des Prüfstands) und „die Antwort auf die Anmeldung wartet
  nicht auf Bark“ (`dashboard-notify.test.mjs`, siehe oben).

## Eine andere Server-Fassung prüfen

`FW_BACKEND` wählt den Treiber (im Test als `BACKEND_KIND`): `php` (Standard,
`harness-php.mjs`) oder `go` (`harness-go.mjs`, Programm aus `FW_GO_BIN`).
`harness.mjs` lädt ihn und hält alles Gemeinsame: Prüfstand, Bark-Attrappe,
die Helfer der Tests und die Prüfungen der Eingaben, die für jeden Treiber
gleich sind (dieselbe falsche Eingabe, derselbe Fehler). Ein Treiber liefert
`kind`, `base`, `dataDir`, `words`, `configure()`, `reset()`,
`runWatchdog()`, `startServer()`/`stopServer()`, `serverLog()`, `stop()`
und jeden Absichts-Helfer unter `state` und `storage`; `startBackend()`
prüft all das gleich nach dem Start des Treibers und bricht ab, wenn etwas
fehlt. Alles andere (Attrappe, übrige Helfer, Tests) bleibt gleich.

**Go:** `bash tools/test-go.sh [dateien…]` baut `bin/fw-alarm` (ohne CGO, mit
`vendor/`) und lässt die Dateien mit `FW_BACKEND=go` laufen; ohne Angabe die
aus `tools/go-green.txt` (die unter Go schon ganz grünen – so auch GitHub,
Job „server-tests“). Der Treiber startet `fw-alarm serve` auf einem freien
Port aus 20000–29999 mit eigenem `config.toml`, Datenordner und
Steuer-Socket im Temp-Ordner. Zustand ändert er, indem er den Server
geordnet anhält (`fw-alarm stop`), die Dateien des Datenordners ändert und
neu startet; gelesen wird nach `fw-alarm flush`. Nach jedem Start prüft er,
dass der Server gesund ist und nichts abgelehnt hat – sonst wirft er mit
der Ausgabe des Servers (Einzelheiten oben in `harness-go.mjs`). Keine
Konfiguration, die er schreibt, zeigt je auf einen fremden Bark-Server: Jeder
Konfigurationstext, gültig oder kaputt, trägt genau eine Zeile
`bark_host = "http://127.0.0.1:<port>"` (oder `bark_host = ""`), sonst
nirgends `bark_host` vor einem `=` und nie `"""`/`'''` – wer eine kaputte
Datei braucht, schreibt die Zeile plus seinen Unsinn. Dateinamen in
`backend.files` gibt es nur in einer Schreibweise (klein, `a-z 0-9 . _ -`,
kein Punkt am Ende). `backend.cli()` nimmt einen eigenen Schalter für
Konfiguration oder Kopie (jede Schreibweise) nur bei `check-config` an. Ein Lauf
mit `reset()` vor jedem Fall dauert dadurch etwa eine halbe Sekunde pro Fall
länger als unter PHP.

`FW_BACKEND_SRC=<Ordner>` prüft unter PHP eine Kopie von `nas_dashboard/`
statt des Repo-Stands – z.B. eine absichtlich verfälschte, um zu sehen, dass
die Tests anschlagen.
