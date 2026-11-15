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

Stolpersteine unter Git Bash: Argumente, die mit `/` beginnen, schreibt Git Bash
für native Programme um (dann `MSYS_NO_PATHCONV=1` voranstellen); PHP und Chrome
bekommen Pfade als `C:/...`. `data_dir` steht in der Sandbox-`config.php` in der
Laufwerks-relativen Form `/Users/...` (ein `C:\`-Pfad wird abgelehnt). Das echte
`nas_dashboard/config.php` wird nie angefasst.
