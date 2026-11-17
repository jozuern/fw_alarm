# Server-Tests (Maßstab für jede Fassung des Dashboard-Servers)

Diese Tests prüfen die Server-Seite von außen: was die Box, der Außen-Wächter,
ein Kurzbefehl und das Dashboard über HTTP sehen, was bei Bark ankommt und was
im Datenordner steht. Sie kennen keine PHP-Funktion beim Namen – dieselben
Tests müssen auch eine künftige Fassung in einer anderen Sprache bestehen
(geplant: Go). Wer eine Regel der Server-Seite ändert oder ergänzt, schreibt
den Test dazu hier.

```bash
node --test "tests/backend/*.test.mjs"            # alles (ca. 1 min)
node --test "tests/backend/box-poll-ack.test.mjs" # eine Datei
```

Voraussetzung: Node 24 und `php` (8.3) im Pfad. Kein Chrome, kein npm, keine
Sandbox von Hand: Jede Testdatei startet über `harness.mjs` ihr eigenes
Dashboard (`php -S` auf einem freien Port, leerer Datenordner im Temp-Ordner)
und eine Bark-Attrappe im Testprozess; am Ende wird alles entfernt
(`FW_KEEP=1` lässt den Ordner zum Nachsehen liegen).

| Datei | Was sie festhält |
|---|---|
| `box-status.test.mjs` | Status-Push: Token, gespeicherte Felder, Relaisalarm und Neustart im Verlauf, veraltete Pushs, fremde Boards |
| `box-poll-ack.test.mjs` | Befehle: Klartext-Antwort, Auslieferung höchstens einmal, Quittung, Verfall, Nummern, Sicherungen des Demo-Alarms, Aufräumen im Poll |
| `box-keys.test.mjs` | Empfängerliste für die Box: Format, Stumm, Demo, Bereinigung, Sicherung, Versionen und ids |
| `alive.test.mjs` | Lebenszeichen für den Außen-Wächter |
| `mute-link.test.mjs` | Kurzbefehl-Link: Text und Browser-Seite, Vorschauen, Bremse, dauerhaft stumm |
| `watchdog.test.mjs` | Wächter: Box offline/wieder da, Selbsttest, Erinnerungen, Außen-Wächter, wie Meldungen rausgehen |
| `alarm-send.test.mjs` | Alarm, Entwarnung und Test-Push direkt an Bark: Felder, Empfänger, bestätigter Modus, Teilausfall, Nachsendung |

## Regeln für neue Tests

- **Nur Beobachtbares prüfen:** HTTP-Antwort, Bark-Attrappe (`backend.bark`),
  Antworten der Dashboard-API (`session()`), Dateien im Datenordner
  (`backend.data`). Nie interne Namen der Implementierung.
- **Keine echte Wartezeit:** „Seit 20 Minuten offline“ entsteht durch einen
  geänderten Zeitstempel in der Laufzeitdatei (`backend.data.edit(...)`),
  nicht durch Warten.
- **Speichern scheitern lassen:** `backend.data.block('name')` legt an die
  Stelle der Datei einen Ordner – das wirkt auf jedem System (ein
  Schreibschutz per `chmod` wirkt unter Windows und als root nicht).
- **Meldungen nach der Antwort:** Der Server verschickt manches erst, nachdem
  er geantwortet hat – dafür `waitFor(...)`, für „es darf nichts kommen“
  `settle()`.
- **Jeder Test für sich:** `beforeEach` setzt Datenordner, Konfiguration und
  Attrappe zurück (`backend.reset()`).
- **Alte und neue Firmware:** `box().status()` sendet die Felder des Stands
  auf `main`, `box().oldStatus()` die der Box im Betrieb (ohne `alarm_seq`,
  `alarm_source`, `core`, `max_alloc_heap`, `alarm_gaveup`).
- Der Test-Server von PHP bedient unter Windows nur eine Anfrage zugleich:
  Regeln über gleichzeitige Anfragen lassen sich hier nicht prüfen.

## Eine andere Server-Fassung prüfen

`FW_BACKEND` wählt den Treiber in `harness.mjs` (heute nur `php`). Ein Treiber
liefert `base`, `dataDir`, `configure()`, `reset()`, `runWatchdog()`, `stop()`;
alles andere (Attrappe, Helfer, Tests) bleibt gleich. `FW_BACKEND_SRC=<Ordner>`
prüft eine Kopie von `nas_dashboard/` statt des Repo-Stands – z.B. eine
absichtlich verfälschte, um zu sehen, dass die Tests anschlagen.
