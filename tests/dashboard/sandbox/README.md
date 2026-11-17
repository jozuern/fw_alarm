# Dashboard-Test-Sandbox

Lokale Kopie des NAS-Dashboards (`php -S`), eine Bark-Attrappe, eine simulierte
Box und ein kleiner Chrome-Client für Ende-zu-Ende-Prüfungen mit Screenshots.
Ein Arbeitsordner `S` (beliebig, z.B. im Temp-Ordner):

```bash
S=/c/Temp/fw-sandbox
bash tests/dashboard/sandbox/setup.sh "$S"      # Kopie anlegen/auffrischen (data/ + config.php bleiben)
bash tests/dashboard/sandbox/start.sh "$S"      # Dashboard :8787 + Bark-Attrappe :8789 (optional: site|mock|all)
bash tests/dashboard/sandbox/box.sh "$S" relay=closed alarm_seq=1   # Status-Push der Box; --age 120 = zuletzt vor 120 s
SANDBOX_DIR="$S" node tests/dashboard/e2e/run.mjs smoke --out "$S/shots"
bash tests/dashboard/sandbox/stop.sh "$S"       # (optional: site|mock|all)
```

Zugänge: Admin `admin` / `test`, Lese-Benutzer `leser` / `test`, Maschinen-Token
`box-token`, Status-Key `statuskey0001`. `offline_after_seconds` ist 30.
Die Bark-Attrappe schreibt jede Anfrage als JSON-Zeile nach `$S/bark.log`;
Key-Präfix `fail` = HTTP 500, `bad` = 400, `slow` = 4 s Verzögerung.

Szenarien (`tests/dashboard/e2e/scenarios/`, je `run.mjs <name>`; vor jedem
Lauf nach Code-Änderungen `setup.sh` erneut, die Sandbox nutzt eine Kopie):
`smoke shell overview recipients log settings switch`. Anmeldung, Sitzungen
und die Dashboard-Aktionen ohne Oberfläche prüfen die Server-Tests in
`tests/backend/` (ohne Chrome, siehe README dort).
`index.php` ist die neue Oberfläche (Anmeldeseite bzw. App), `classic.php` die
eingefrorene alte (`switch` prüft beide). Die Unit-Tests laufen ohne Sandbox:
`node --test "tests/dashboard/*.test.mjs"` (die Ordner-Form scheitert unter Node 24).

Stolpersteine in den Szenarien: Ein `page.goto()`, das nur den `#`-Teil der
aktuellen Adresse ändert (z.B. nach dem Login von `index.php` auf
`index.php#/empfaenger`), löst kein `load` aus und hängt – vorher
`about:blank` laden. Daten der Sandbox bleiben zwischen den Läufen erhalten;
jedes Szenario legt eigene Empfänger mit eindeutigen Keys an und räumt sie
wieder ab.

Stolpersteine unter Git Bash: Argumente, die mit `/` beginnen, schreibt Git Bash
für native Programme um (dann `MSYS_NO_PATHCONV=1` voranstellen); PHP und Chrome
bekommen Pfade als `C:/...`. `data_dir` steht in der Sandbox-`config.php` in der
Laufwerks-relativen Form `/Users/...` (ein `C:\`-Pfad wird abgelehnt). Das echte
`nas_dashboard/config.php` wird nie angefasst.
