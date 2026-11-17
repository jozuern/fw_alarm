# Server-Tests (Maßstab für jede Fassung des Dashboard-Servers)

Diese Tests prüfen die Server-Seite von außen: was die Box, der Außen-Wächter,
ein Kurzbefehl und das Dashboard über HTTP sehen, was bei Bark ankommt und was
der Server gespeichert hat. Sie kennen keine PHP-Funktion und keine Datei beim
Namen – dieselben Tests müssen auch eine künftige Fassung in einer anderen
Sprache bestehen (geplant: Go). Wer eine Regel der Server-Seite ändert oder
ergänzt, schreibt den Test dazu hier.

```bash
node --test "tests/backend/*.test.mjs"            # alles (ca. 1 min)
node --test "tests/backend/box-poll-ack.test.mjs" # eine Datei
```

Voraussetzung: Node 24 und `php` (8.3) im Pfad. Kein Chrome, kein npm, keine
Sandbox von Hand: Jede Testdatei startet über `harness.mjs` ihr eigenes
Dashboard (`php -S` auf einem freien Port, leerer Datenordner im Temp-Ordner)
und eine Bark-Attrappe im Testprozess; am Ende wird alles entfernt
(`FW_KEEP=1` lässt den Ordner zum Nachsehen liegen).
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
  über `PHP_STORAGE` in `harness.mjs`.
- **Langer Verlauf:** `backend.state.seedLog([...])` legt Einträge roh in den
  Verlauf (`{ rotated: true }` = die ältere Datei) – statt hunderter Aktionen.
- **Texte mit dem Namen der Konfigurationsdatei** bauen die Erwartung aus
  `backend.words.configFile` (`config.php` bzw. `config.toml`).
- **Nur eine Fassung:** `php-*.test.mjs` prüfen, was allein an PHPs Ablage
  hängt, und fassen als einzige die Dateien von PHP an (`backend.data`);
  `go-*.test.mjs` prüfen, was sich nur die Go-Fassung fragen lässt. Eine
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

`FW_BACKEND` wählt den Treiber in `harness.mjs` (heute nur `php`; im Test als
`BACKEND_KIND`). Ein Treiber liefert `kind`, `base`, `dataDir`, `configure()`,
`reset()`, `runWatchdog()`, `stop()` und die Absichts-Helfer `state`, `storage`
und `words`; ihre Eingaben prüft `startBackend()` für jeden Treiber gleich
(dieselbe falsche Eingabe, derselbe Fehler). Alles andere (Attrappe, übrige
Helfer, Tests) bleibt gleich. `FW_BACKEND_SRC=<Ordner>`
prüft eine Kopie von `nas_dashboard/` statt des Repo-Stands – z.B. eine
absichtlich verfälschte, um zu sehen, dass die Tests anschlagen.
