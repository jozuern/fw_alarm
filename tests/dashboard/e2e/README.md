# Browser-Szenarien (Chrome, Ende-zu-Ende)

Die Szenarien unter `tests/dashboard/e2e/scenarios/` fahren die Oberfläche in
einem Headless-Chrome durch (Screenshots, Eingaben, Anfragen). Ihr Dashboard
und die Bark-Attrappe startet `run.mjs` selbst über denselben Prüfstand wie die
Server-Tests (`tests/backend/harness.mjs`): eigener freier Port, leerer
Datenordner im Temp-Ordner, am Ende wird alles entfernt. Eine Sandbox von Hand
gibt es nicht mehr. Welche Fassung des Servers läuft (heute PHP, später Go),
bestimmt die Umgebungsvariable `FW_BACKEND`, wie bei den Server-Tests.

```bash
node tests/dashboard/e2e/run.mjs smoke                  # ein Szenario
node tests/dashboard/e2e/run.mjs overview --out shots   # Screenshots dorthin (sonst: Temp-Ordner fw-e2e-shots)
```

Szenarien: `smoke shell overview recipients log settings switch`. Voraussetzung:
Node 24, `php` (8.3) und Chrome. Chrome läuft ohne Fenster (headless).
Anmeldung, Sitzungen und die Dashboard-Aktionen ohne Oberfläche prüfen die
Server-Tests in `tests/backend/` (siehe README dort). Die Unit-Tests laufen
ohne Prüfstand: `node --test "tests/dashboard/*.test.mjs"` (die Ordner-Form
scheitert unter Node 24).

Zugänge: Admin `admin` / `test`, Lese-Benutzer `leser` / `test`, Maschinen-Token
`box-token`, Status-Key `statuskey0001`. `offline_after_seconds` ist 30. Die
Bark-Attrappe antwortet nach Key-Präfix: `fail` = HTTP 500, `bad` = 400,
`slow` = 4 s Verzögerung.

Von Hand ausprobieren: `node tests/backend/serve.mjs [--port 8787]` startet
dasselbe Dashboard samt Attrappe, gibt die Adresse aus und läuft bis Strg+C.

`tests/dashboard/e2e/sandbox.mjs` hält die Hilfen der Szenarien (Status-Push der
simulierten Box, Lauf des Wächters, Bark-Anfragen, Server anhalten/starten) als
dünne Hüllen um den Prüfstand. Zustand herstellen oder lesen geht nur über die
Absichts-Helfer `backend.state.*` und `backend.storage.*` (jeder Aufruf mit
`await`), nie über Dateien im Datenordner.

Stolpersteine in den Szenarien: Ein `page.goto()`, das nur den `#`-Teil der
aktuellen Adresse ändert (z.B. nach dem Login von `index.php` auf
`index.php#/empfaenger`), löst kein `load` aus und hängt – vorher
`about:blank` laden. Jeder Lauf beginnt mit leeren Daten: was ein Szenario
braucht (z.B. einen langen Verlauf), legt es selbst an. Unter Git Bash
schreibt `MSYS_NO_PATHCONV=1` Argumente um, die mit `/` beginnen, nicht um;
Chrome und PHP bekommen Pfade als `C:/...`.
