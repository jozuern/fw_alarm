# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Was dieses Projekt ist

ESP32-Firmware (Arduino-Framework, Board `esp32dev`) für eine inoffizielle
Feuerwehr-**Zusatz**alarmierung. Der ESP32 überwacht den potenzialfreien
Relaiskontakt eines Swissphone-Laders (DIN Pin 1→GND, Pin 3→GPIO27,
`INPUT_PULLUP`, LOW = Alarm). Schließt der Kontakt, sendet die Box über den Dienst
**Bark** einen iOS Critical Alert an alle Empfänger. Es wird **kein Funk/POCSAG
dekodiert** – einziger Eingang ist der trockene Relaiskontakt.

Kernprinzip im README betont und einzuhalten: **Zusatz, kein Ersatz** für den Melder.

## Wo was läuft (seit dem Umzug 11/2026)

- **Dashboard** (`nas_dashboard/`, PHP, samt Daten): auf der Oracle-VM
  (Always Free, Frankfurt, 1 OCPU / 1 GB + Auslagerungsdatei), erreichbar nur
  über den Cloudflare Tunnel als `https://alarm.tunero.dev/`. Nginx hört nur
  auf `127.0.0.1:8080`, PHP-FPM-Pool `fw-alarm` als Benutzer `fwalarm`,
  Webroot `/srv/fw-alarm/app`, Daten `/srv/fw-alarm/data`, `config.php` nur
  auf der VM. Einrichtung, Deploy, Prüfungen, Betrieb: `server/README.md`.
- **NAS (zu Hause)**: hält keine Daten des Dashboards mehr. Es ist (a) die
  **Brücke** (`nas_bridge/`): reicht die Aufrufe der Box (bis sie neu
  geflasht ist) und alte Kurzbefehl-Links serverseitig an die VM weiter, und
  (b) der **Außen-Wächter**: führt `cloud_watcher/fw-alarm-watch.sh` als
  DSM-Aufgabe aus und prüft die VM von außen.
- **Box**: unverändert; sie ruft bis zum Flashen weiter die alte NAS-Adresse
  auf. Ihre Bark-Alarme hängen an nichts davon.
- **Namen im Code** sind historisch geblieben (`nas_dashboard/`, `nas_web`,
  `nas_alarm`, `NAS_ALIVE_URL`, Kommentare mit „NAS“): Wo unten „NAS“ den Ort
  des Dashboards meint, ist heute der Server gemeint. **Sichtbare** Texte
  sagen „Server“; „NAS“ steht dort nur noch für den Außen-Wächter
  (`tests/dashboard/wording.test.mjs` prüft das).
- **Deploy**: `git push origin main`, dann `bash server/deploy.sh HEAD`
  (sichert zuerst, prüft danach jede Datei auf dem Server gegen Git) und
  `bash server/check.sh public https://alarm.tunero.dev`. Die Brücke wird per
  SMB auf das NAS kopiert (`nas_bridge/README.md`).
- **Sicherung**: Empfängerliste (Bark-Keys, Kurzbefehl-Tokens), Verlauf und
  `config.php` gibt es nur auf der VM. `server/backup.sh` holt sie als ein
  Archiv auf den PC (`~/fw-alarm-backups`, die letzten 30); `deploy.sh` ruft
  es VOR jeder Änderung auf dem Server auf und spielt auch dann ein, wenn die
  Sicherung scheitert (Warnung, Rückgabe 3). Die Archive enthalten
  Geheimnisse: nie im Repo (das Skript bricht dann ab), in keinem
  Cloud-Ordner. Ein Archiv gilt erst nach der Prüfung als Sicherung (lesbar,
  Empfängerliste und `config.php` enthalten) – eine gescheiterte löscht keine
  älteren. Zurückspielen: `server/README.md`. Tests:
  `node --test "tests/server/*.test.mjs"` (ssh als Attrappe).
- **Das Repo ist öffentlich**: keine echten Adressen (NAS, VM) in
  versionierten Dateien, nur Platzhalter (`dein-name.synology.me`, `<NAS>`,
  `<IP der VM>`); die VM-Adresse für `deploy.sh` steht in der nicht
  versionierten `server/deploy.local`.

## Go-Fassung (im Bau)

Der Server des Dashboards wird als EIN Go-Programm `fw-alarm` neu gebaut –
innen neu, außen gleich. Entwurf:
`docs/superpowers/specs/2026-11-17-go-server-neubau-design.md`, Plan:
`docs/superpowers/plans/2026-11-17-go-server-neubau.md`. **Auf dem Server
läuft bis zum Umschalten weiter die PHP-Fassung** (`nas_dashboard/`, alles
oben und unten Beschriebene); nichts an der Go-Fassung darf ihr Verhalten
ändern.
- Aufbau: Modul `fwalarm` (`go.mod`, Go-Version festgeschrieben);
  `cmd/fw-alarm/` verteilt nur auf die Unterbefehle (einzige Stelle mit
  `os.Exit`), alles andere liegt in `internal/` (`clock`, `bg`, `textde`,
  `config`, `model`, `store`, `bark`, `core`, `web`, `control`, `app`,
  weitere folgen laut Plan). Abhängigkeiten nur
  Standardbibliothek plus `golang.org/x/crypto`, `x/sys`, `x/term` und
  `github.com/BurntSushi/toml`, mitgeliefert in `vendor/` (gebaut wird mit
  `-mod=vendor`, ohne CGO). `nas_dashboard/webassets.go` bettet die
  Oberfläche ein; `server/deploy.sh` spielt diese Datei nicht ein.
- Konfiguration: `config.toml`, Vorlage `server/go/config.example.toml` (sie
  ist genau die Ausgabe von `config.Write` – Erklärungen nur dort ändern,
  dann `FW_WRITE_EXAMPLE=1 go test ./internal/config -run
  TestExampleIsWriteOutput`). Dieselben Schlüssel wie `config.php` ohne
  `data_dir`/`health_check_url`, dazu `state_dir`, `listen`,
  `control_socket`, `check_interval_seconds` und die Probe-Sperre
  `send_guard_keys`/`send_guard_until`; fehlt ein Schlüssel, gilt der Wert,
  den der PHP-Code dafür nimmt; eine Datei ohne einen einzigen Wert ist
  fehlerhaft, und `config.Write` schreibt die Probe-Sperre als Erstes
  (`send_guard_keys`, `timezone`, `send_guard_until`). Im
  laufenden Server prüft ein Hintergrund-Prüfer die Datei alle 2 s; eine
  geänderte Fassung – gültig oder fehlerhaft – gilt bzw. gilt als fehlerhaft
  erst, wenn sie 2 s später unverändert ist (eine halb geschriebene Datei
  wird so nie übernommen; `reload` urteilt sofort). Fehlerhaft: Es gilt die
  letzte gültige Fassung. Nach jeder in Kraft gesetzten Fassung liegt eine
  Kopie als `config.last-good.toml` im Datenordner (0600) – mit ihr startet
  er, wenn `config.toml` nicht ladbar ist. `state_dir`, `listen` und
  `control_socket` gelten erst nach einem Neustart. Befehle bisher:
  `fw-alarm version`, `fw-alarm check-config [--config <datei>]`,
  `fw-alarm hash-password` (Argon2id), `fw-alarm serve` und die Befehle an
  den laufenden Server über den Steuer-Socket `control_socket`: `health`
  (`running=1`, `version=`, `state=`, `save_ok=`, `config_ok=`, `hint=`;
  Rückgabe 0 nur gesund), `check`, `flush`, `reload`, `stop` (kehrt zurück,
  wenn der Server fort ist) – alle mit `[--config <datei>] [--last-good
  <datei>]`.
- Prüfen auf dem PC: `bash tools/check-go.sh` (nur Go: gofmt,
  `go mod tidy -diff`, `go vet` für PC und `linux/arm64`, staticcheck,
  govulncheck, `go test` ohne Race-Detektor) und `bash tools/test-go.sh [dateien…]` (baut
  `bin/fw-alarm` wie das Produkt – ohne CGO, `-trimpath` – und lässt
  Server-Tests aus `tests/backend/` gegen Go laufen; ohne Angabe die Dateien
  aus `tools/go-green.txt`). Go ist auf dem PC pro Benutzer installiert, die
  Skripte finden es selbst. staticcheck ohne ST1005 (`staticcheck.conf`:
  deutsche Fehlertexte beginnen mit Hauptwörtern). Kommentare deutsch mit
  Umlauten, Bezeichner englisch.
- **GitHub prüft jeden Push auf `main`** und einmal pro Woche
  (`.github/workflows/ci.yml`): Job „go“ – gofmt, `go mod tidy -diff`,
  `go vet`, staticcheck, govulncheck, `go test -race` (läuft auf dem PC
  mangels C-Compiler nicht) und Bau für `linux/arm64` und `windows/amd64`;
  Job „server-tests“ – `bash tools/test-go.sh` (die Server-Tests aus
  `tools/go-green.txt` gegen Go); Job „node“ – die Node-Tests unter
  `tests/dashboard/`, `tests/server/` und `tests/watcher/` (die Server-Tests
  unter `tests/backend/` gegen PHP noch nicht). Nur
  Leserechte, keine Geheimnisse, kein Einspielen; die Aktionen sind an feste
  Stände gebunden.
- Zeilenenden (`.gitattributes`): Go-Dateien, `tools/*.sh`, `.github/` und
  alle eingebetteten Text-Dateien (`assets/app/`, `app.css`, `favicon.svg`,
  `manifest.json`) LF; `vendor/`, `testdata/`, `tests/backend/fixtures/`
  und die Bilder unter `assets/` bytegenau (`assets/vendor/` war es schon).

## Build / Flash / Monitor

PlatformIO (CLI-Weg):
```bash
pio run                 # kompilieren
pio run -t upload       # auf den ESP32 flashen
pio device monitor      # serielle Ausgabe, 115200 Baud
```
`platformio.ini` setzt `src_dir = boss925_alarm_bark`, damit der Ordnername exakt
zur `.ino` passt – dadurch lässt sich dasselbe Projekt **ohne Änderung auch in der
Arduino IDE** öffnen (Board: „ESP32 Dev Module"). Für die Firmware gibt es
keine Tests und kein Lint (die Tests der Server-Seite stehen unter „Architektur“).
Die Plattform ist gepinnt (`espressif32@7.0.1` = Arduino-ESP32-Core 2.0.17,
damit getestet); Änderungen trotzdem so schreiben, dass sie auch mit Core 3.x
bauen (Versions-Weichen über `ESP_ARDUINO_VERSION_MAJOR`).

## Architektur

Alles liegt in `boss925_alarm_bark/`:
- `boss925_alarm_bark.ino` — die gesamte Firmware-Logik.
- `config.h` — **alle** vom Nutzer editierbaren Werte (Secrets). Per `.gitignore`
  ausgeschlossen; existiert lokal als Kopie von `config.example.h`.
- `config.example.h` — Vorlage mit Platzhaltern, im Git.

Der `.ino`-Code liest **nichts** fest verdrahtet; jeder konfigurierbare Wert kommt
als `#define` (bzw. das Array `BARK_KEYS_ALARM[]`) aus `config.h`. Beim Ändern von
Verhalten/Defaults daher **immer beide** Config-Dateien synchron halten.
`config.h` gehört dem Nutzer (gitignored): Jeder NEUE Wert bekommt im `.ino`
einen `#ifndef`-Fallback (Block „FALLBACKS FÜR NEUERE WERTE“), damit eine
ältere `config.h` weiter kompiliert; geänderte Mindestwerte greifen im `.ino`
(z.B. `WDT_EFFECTIVE_TIMEOUT_S` = max(`WDT_TIMEOUT_S`, Minimum)).
Einzige Laufzeit-Ausnahme: Die aktiven Alarm-Empfänger (`alarmKeys[]`) kommen
nach dem ersten Dashboard-Sync aus dem NVS, nicht mehr aus `config.h`
(siehe Empfängerlisten-Bullet unten).

Zusätzlich gibt es `nas_dashboard/`: die PHP-Dateien des Dashboards (Nginx +
PHP 8.3, kein Docker, keine DB; bis 11/2026 auf Synology Web Station, seitdem
auf der Oracle-VM – siehe „Wo was läuft“). Das Dashboard ist ein optionales
Rendezvous-System für Monitoring und manuelle Befehle. Der ESP32 bleibt
outbound-only: Status-Push nach oben, Command-Polling nach oben. Bark muss auch
bei NAS-Ausfall unverändert unabhängig funktionieren. Die Oberfläche ist
Preact/htm ohne Build-Schritt und ohne npm (siehe „Frontend des Dashboards“
unter „Bewusste Entscheidungen“).

**Server-Tests** (`tests/backend/`, README dort): Die Regeln der Server-Seite –
Box-Protokoll (`api/{status,poll,ack,keys}.php`), `api/alive.php`,
Kurzbefehl-Link, Wächter, Alarm-Versand – prüft
`node --test "tests/backend/*.test.mjs"` von außen: Jede Testdatei startet ein
eigenes Dashboard (`php -S`, leerer Datenordner im Temp-Ordner, freier Port)
und eine Bark-Attrappe im Testprozess (`harness.mjs`), spricht nur HTTP und
sagt über `backend.state`/`backend.storage`, welchen gespeicherten Zustand
sie braucht (die Dateien von PHP, `backend.data`, nur in `php-*.test.mjs`;
der Datenordner von Go, `backend.files`, nur in `go-*.test.mjs`).
Sie sind der Maßstab für die
geplante Go-Fassung (Treiber über `FW_BACKEND`: `harness-php.mjs` bzw.
`harness-go.mjs`, der hält den Server zum Ändern des Zustands an und startet
ihn neu; `bash tools/test-go.sh`; Design – Neubau innen, außen
gleich – in `docs/superpowers/specs/2026-11-17-go-server-neubau-design.md`,
Plan daneben unter `plans/`): nur Beobachtbares prüfen, nie interne Namen. Eine neue oder geänderte Server-Regel bekommt dort ihren Test –
für die alte UND die neue Firmware (`box().oldStatus()`).

Und `cloud_watcher/`: der **Außen-Wächter** – ein Bash-Skript, das ein
ANDERER Rechner als der Server alle 5 min ausführt. Seit dem Umzug ist das
das NAS (DSM-Aufgabe als root; bis 11/2026 war es umgekehrt: eine Cloud-VM
mit systemd-Timer überwachte das NAS – daher Ordnername, `NAS_ALIVE_URL` und
die Formulierungen „VM“/„NAS“ im Rest dieses Absatzes, der die Regeln
beschreibt). Wie das überwachte Ziel in den Meldungen heißt, steht in
`TARGET_NAME` (hier `Alarm-Server`). **Als root läuft das Skript nur, wenn
Skript, Konfiguration, ihre Ordner und der Zustandsordner root gehören und
für niemanden sonst schreibbar sind** (es führt seine Konfiguration aus):
auf dem NAS deshalb in `/volume1/@fw_alarm_watch`, NIE in den Freigaben
`web`/`web_packages` – dort darf der Web-Benutzer `http` schreiben.
Es ruft alle 5 min `api/alive.php` über die ÖFFENTLICHE Adresse des Dashboards
auf und meldet per Bark an den Status-Key, wenn es von außen nicht erreichbar
ist (3 Fehlschläge in Folge) oder der Wächter `check_offline.php` dort
> 45 min nicht lief. Gegenrichtung: `alive.php` merkt sich jeden Aufruf
(`watcher_seen.json`), der Cron-Wächter meldet eine verstummte VM (Art
`outside_watcher`, nur vorhanden, wenn `watcher_token_sha256` gesetzt ist –
`outside_watcher_configured()`; schweigt, solange der Schreib-Selbsttest mit
`blocks_pushes` scheitert – dann kann `alive.php` nichts speichern, und die
Schuld läge beim NAS). Die drei Ausfall-Abläufe im Cron-Wächter (NAS-Dashboard,
Box, Außen-Wächter) folgen denselben Regeln, sind aber bewusst getrennt
geblieben (eigene Zustandsschlüssel; „Box OFFLINE“ ist die wichtigste
Meldung, der NAS-Abschnitt läuft trotzdem davor, weil der Box-Text vom
Selbsttest abhängt) – Regeländerungen in allen drei Abschnitten nachziehen.
Nach einer Datenordner-Störung zählt die Stille der VM erst ab dem Ende der
Störung (`watcher_unknown_at`). Lässt sich `watchdog_state.json` nicht
schreiben, hält der Wächter seinen Zustand in einer Ersatz-Ablage im
Temp-Ordner (`watchdog_fallback_state_path()`), sonst kämen „DEFEKT“ nie und
„OFFLINE“ alle 15 min. Regeln: eigenes Token (`watcher_token_sha256`,
nur POST), NIE das Maschinen-Token der Box (die VM darf weder Status fälschen
noch Befehle abholen); `alive.php` bleibt schlicht und lauffähig mit älteren
Ständen von `common.php` (nur `load_config`/`data_path`/`read_json_file`/
`write_json_file`/`text_response`), schreibt best effort und meldet `ok=1`
auch bei klemmendem Datenordner (dafür gibt es `nas_web`); antwortet 404,
wenn `watcher_token_sha256` gleich `machine_token_sha256` ist. Die VM schickt
`bark_failed_at` mit (ihre eigenen Bark-Meldungen scheitern → Dashboard
warnt), `alive.php` liefert zusätzlich `cron_configured` (bark_key_status
gesetzt) und `nas_send_failed` (letzte Admin-Meldung des NAS gescheitert) –
das Skript muss mit älteren `alive.php`-Ständen ohne diese Zeilen
weiterlaufen. Kann die VM ihren Zustand nicht speichern, ruft sie
`alive.php` gar nicht erst auf (dann greift die Gegenüberwachung).
Bark-Texte des Außen-Wächters ASCII; Meldungen gelten nur bei angenommener
Zustellung als erledigt. Die Oracle-VM (heute der Server des Dashboards)
bleibt bewusst im Always-Free-Kontingent (kein Pay-as-you-go). Oracle stoppt
VMs, deren CPU-, Netz- UND Speicherauslastung 7 Tage unter 20 % bleibt –
deshalb hat sie nur 1 GB Speicher (ca. 40 % belegt; mit 2 GB waren es 21 %);
einen Ausfall meldet der Außen-Wächter. Skript, Units und Vorlage unter
`cloud_watcher/` sowie alles unter `server/` und `nas_bridge/` sind LF
(`.gitattributes`). Tests des Skripts:
`node --test "tests/watcher/*.test.mjs"` (curl/flock/id/stat als Attrappen).

**Die Brücke (`nas_bridge/`)** auf dem NAS reicht genau
`api/{status,poll,ack,keys,mute}.php` per PHP-cURL an die VM weiter (festes
Ziel in `bridge_config.php`, kein offener Proxy, keine Daten, keine
Geheimnisse). Regeln, die bleiben müssen: nur die in `lib/bridge.php`
aufgezählten Kopfzeilen gehen in beide Richtungen (nie Cookies; fehlende
`Accept`/`User-Agent` werden NICHT ergänzt – `mute.php` unterscheidet Browser,
Kurzbefehl und Link-Vorschau daran); Zeitlimits 3 s/3,5 s (unter den 4 s der
Box); die Brücke antwortet **nie selbst mit 403** und reicht ein 403 nur
weiter, wenn es von der App stammt (Rumpf `FORBIDDEN`) – jedes andere 403
(Cloudflare, Nginx) wird 502, denn die Box wertet 403 als „Token abgelehnt“
und warnt per Bark; Wartung („Halt“) allein über das Vorhandensein von
`bridge_hold.php` (dann 503, im Halt abgewiesene Mute-Aufrufe landen ohne
Token im `held_log` und werden nachgeholt); `index.php`/`classic.php` leiten
mit 302 (nicht 301) um. Die Brücke setzt `X-FW-Bridge: 1`: Nginx protokolliert
das (`bridge=1`), und `api/status.php` merkt es sich als `via_bridge` – daran
hängt der Text von „Box OFFLINE“ (siehe „Meldungen an den Admin“). Tests:
`node --test "tests/bridge/*.test.mjs"`. Nach dem Flashen der Box entfallen
die vier Box-Pfade, `api/mute.php` bleibt für alte Links.

Ablauf in `loop()`: `feedWatchdog()` → `maintainWiFi()` (nicht-blockierend; erst
der AutoReconnect des Cores, harter Neuaufbau frühestens nach
`WIFI_RECONNECT_MS`) → `handleRelayInput()` (Cooldown-Ende,
Wiederbewaffnungs-/Klemm-Logik, Gate, Erkennung per Pegel-Abfrage **plus**
Interrupt-Latch → `startAlarm()`) → `processPendingAlarm()` (Nachsende-Runden)
→ periodische RTC-Sicherung → [Startphase `STARTUP_DETECT_MS` nach `setup()`:
bis hierher, sonst nichts] → Start-Meldung (einmalig, nicht während
`alarmPending`) → `handleHeartbeat()` → `handleRemoteMonitor()` (NAS
ACK/Poll/Status, kurze Timeouts + Backoff) → `processOwnerNotices()`. Die
Erkennung läuft so immer VOR Nebenlogik, auch im ersten Durchlauf nach einem
Neustart. Alarm geht als `level=critical` an alle Keys,
Status/Heartbeat/Test als `level=passive` nur an `BARK_KEY_STATUS`.

Wichtige Eigenschaften, die beim Refactoring erhalten bleiben müssen:
- **Pro Key isoliert senden** mit Retry (`BARK_MAX_TRIES`); ein toter Key darf die
  anderen nicht blockieren. `sendBark()` gibt `bool` zurück (Code des letzten
  Versuchs über `int* httpCode`, < 0 = Transportfehler); bei HTTP 4xx wird
  **nicht** wiederholt (Client-Fehler). `sendStatus()` gibt ebenfalls `bool` zurück.
  400/401/403/404 = dauerhaft abgelehnt – aber NUR, wenn Bark in derselben
  Runde mindestens einen anderen Empfänger angenommen hat (Beweis, dass Bark
  selbst funktioniert): Dann ist der Key für diesen Alarm endgültig gescheitert
  (`keyFailed[]`, keine Nachsende-Runden mehr), Owner wird sofort gewarnt (Key
  nur maskiert, `maskKey()` – auch im seriellen Log). Lehnt Bark ALLE ab
  (Störung, Firewall), bleiben die Keys offen und werden in jeder Runde erneut
  versucht – sonst ginge ein echter Alarm nach der ersten Runde nie wieder
  raus. Die übrigen Keys laufen weiter. Zwei Transportfehler in Folge brechen eine Runde ab (die
  nächste beginnt beim nächsten Key); 5xx/andere 4xx bleiben pro Key isoliert.
  Jede HTTPS-Anfrage hat ein Handshake-Limit (`setHandshakeTimeout()`, Bark 8 s,
  NAS = `REMOTE_HTTP_TIMEOUT_MS`, mind. 4 s) – der Core-Standard (120 s) läge
  über dem Watchdog.
- **Nachsende-Puffer**: `startAlarm()` merkt den Alarm (`alarmPending`, `keyDone[]`,
  Zeitstempel der Erkennung im Text); `processPendingAlarm()` versucht alle
  `ALARM_RETRY_MS` erneut die noch offenen Keys, bis alle versorgt sind oder
  `ALARM_RETRY_GIVEUP_MS` erreicht ist (auch mitten in einer Runde geprüft;
  dann Status-Warnung an den Owner und Status-Feld `alarm_gaveup=1` bis zum
  nächsten Alarm). Nachgesendete Alarme bekommen den Zusatz „(verspaetet
  zugestellt)". Ein eingerasteter Impuls trägt den Zeitpunkt seines Beginns
  (`isrLatchedAt`), nicht den der späteren Verarbeitung. „erreicht/gesamt“
  (`key_progress`) kommt aus beim Alarm festgehaltenen Zahlen
  (`deliveryDone`/`deliveryTotal`); ein Alarm ohne Empfänger gilt NICHT als
  zugestellt (Owner-Warnung, auch schon beim Boot).
- **Alarmzustand übersteht Neustarts**: `RtcAlarmState` (`RTC_NOINIT_ATTR`,
  Magic + Größe + FNV-Prüfsumme) hält Wiederbewaffnung, Cooldown, offene
  Zustellung (Masken, Lautstärke, Zeitstempel, vergangene Zeiten), Klemm-Status,
  `alarm_seq` und Quelle; `saveRtcState()` bei jeder Änderung und periodisch.
  `restoreRtcState()` übernimmt ihn nur nach einem NICHT-Power-on-Reset mit
  gültiger Prüfsumme (sonst verwerfen) und setzt die Zustellung als Nachsendung
  fort; passt die Empfängerliste nicht mehr (Listen-Prüfsumme), geht ein
  Relaisalarm an alle erneut, ein Demo-Alarm wird verworfen.
- **Echte Flankenerkennung**: Nach einem Alarm erst wieder scharf, wenn der Kontakt
  `REARM_OPEN_MS` stabil offen war (`waitingForRelease`) — ein dauerhaft
  geschlossener Kontakt darf NICHT periodisch neu alarmieren (auch nicht nach
  einem Watchdog-/Absturz-Neustart, siehe RTC). Zusätzlich
  **Cooldown** `COOLDOWN_MS` als Mindestabstand (Merker `cooldownActive`, kein
  millis-Vergleich mit `lastAlarm != 0` – der kippte nach 49,7 Tagen). Bleibt der Kontakt
  `STUCK_CONTACT_WARN_MS` geschlossen → „Kontakt klemmt?"-Statuswarnung (mit
  Wiederholung); beim Wiederbewaffnen wird `stuckWarned` zurückgesetzt und –
  nur wenn die Warnung zugestellt war – leise „Kontakt wieder offen“ gemeldet.
- **Interrupt-Latch** (`relayEdge`, `IRAM_ATTR`): misst die Impulsdauer per
  Pin-Interrupt und rastet Impulse ≥ `DEBOUNCE_MS` ein (`isrLatched`), damit kein
  Alarm verloren geht, während `loop()` in einer blockierenden HTTPS-Sendung steckt.
- **Entprellung** `DEBOUNCE_MS` gilt in beiden Pfaden (Pegel-Abfrage und ISR),
  auch über blockierende Aufrufe hinweg: Die Pegel-Abfrage verwirft
  `activeSince` nach einer Probenlücke > `LEVEL_SAMPLE_GAP_MS`; über eine
  Lücke hinweg zählt nur, was der Interrupt sieht (`isrClosedLongEnough()`:
  Kontakt seit ≥ `DEBOUNCE_MS` ununterbrochen geschlossen und jetzt noch aktiv).
  `ACTIVE_LOW false` schaltet `INPUT_PULLDOWN` statt `INPUT_PULLUP`.
  Heartbeat/WLAN laufen auch während Cooldown/Wiederbewaffnung weiter.
- **Warnungen an den Owner** (`queueNotice()`/`processOwnerNotices()`): gelten
  erst bei angenommener Zustellung als erledigt, Wiederholung nach
  `STATUS_RETRY_MS`, nie während `alarmPending` (Ausnahme: abgelehnter Key –
  ein Versuch direkt nach der Runde), höchstens eine pro `loop()`, nach einem
  Fehlschlag Pause für alle. Zeitstempel = Ereignis. Auch bei 400/403/404 auf
  den Status-Key wird eine Warnung NICHT verworfen (kann eine Bark-Störung
  sein), nur später wiederholt. Die Start-Meldung kommt erst nach der
  Startphase und nie vor einer offenen Zustellung.
- **Heartbeat** mit NTP = 1×/Kalendertag ab `HEARTBEAT_HOUR`; ohne NTP Fallback auf
  millis-Abstand (`HEARTBEAT_MS`). Als erledigt markiert wird **nur bei Erfolg**;
  nach Fehlversuch neuer Versuch nach `HEARTBEAT_RETRY_MS`. Solange ein Alarm
  offen ist (`alarmPending`), pausiert der Heartbeat (Alarm hat Vorrang).
- **NAS-Monitoring ist Nebenlogik**: `REMOTE_*` sendet Status per
  `application/x-www-form-urlencoded` an `api/status.php`, pollt `api/poll.php`
  und quittiert `api/ack.php`. Keine JSON-/HTTP-Zusatzbibliothek in der Firmware.
  NAS-Fehler/TLS/Timeouts sind nicht fatal, nutzen kurze Timeouts und Backoff und
  dürfen die Bark-Alarmierung nicht verhindern. Der Status-Push hat einen
  eigenen Backoff (`lastStatusError`), Poll/ACK/Key-Sync teilen sich
  `lastRemoteError`; eine ACK wird nach `REMOTE_ACK_MAX_TRIES` Fehlversuchen
  verworfen (`lastRemoteCommandId` bleibt – kein Replay).
- **Manueller REAL ALARM läuft DIREKT vom NAS zu Bark** (`bark_send_alarm_all()`
  in `nas_dashboard/lib/common.php`, PHP-cURL, `level=critical`, an alle Keys
  PARALLEL über `bark_post_many()` (curl_multi), pro Key isoliert mit Retry,
  HTTP 4xx ohne Retry, `ignore_user_abort`; Titel/Text gehen unverändert raus
  wie in config.php eingetragen — `bark_ascii()` sichert nur die
  Statusmeldungen mit Nutzereingaben ab). Der Alarmpfad braucht KEIN
  Schreibrecht: überfällige Stummschaltungen rechnet er beim Lesen als laut
  (`mute_is_stale()`), weggeschrieben wird erst NACH dem Versand und nur
  best effort (`with_lock_soft()`). Vorher/nachher landen `nas_alarm_started`/
  `nas_alarm` im Verlauf (beide mit `demo`=bool; Alt-Einträge ohne das Feld
  erkennt man am Text „DEMO-MODUS“). `run_nas_broadcast()` in
  `dashboard_api.php` nimmt EINE Momentaufnahme von Liste und Demo-Zustand,
  prüft damit `expect_demo` und sendet mit genau ihr (`$snapshot` bis
  `alarm_recipients()`); die Antwort trägt `started_at`. cURL-Fehlerursachen
  (DNS/TLS/Timeout) stehen als `err` im Ergebnis und im PHP-Fehlerlog. Bewusste Entscheidung: Die manuelle
  Alarmierung braucht man gerade dann, wenn die ESP32-Kette klemmt — sie darf
  deshalb nicht am Poll des ESP32 hängen. `ALARM`-Commands legt das
  Dashboard nur noch als **Demo-Werkzeug** an (Button „Demo-Alarm über
  die Box"): serverseitig gesichert (`demo_alarm_block_reason()`) — nur bei
  aktivem Demo-Modus UND nur, wenn der letzte Status-Push des ESP32 die
  aktuelle `keys_version` bestätigt, genau EINEN Empfänger (`keys_count=1`)
  meldet und jünger als `offline_after_seconds` ist (sonst könnte der
  Testalarm noch an die volle Liste gehen). Geprüft beim Anlegen (auch erneut
  unter dem `command`-Lock) UND beim Ausliefern in `poll.php`: Hat sich der
  Demo-Stand seit dem Anlegen geändert (`demo_changed_at` im Befehl), wird der
  Befehl als `cancelled` verworfen. REAL ALARM, ENTWARNUNG und DEMO-ALARM
  senden `expect_demo` mit (`'0'` = LIVE, sonst `key_id`) – Pflichtfeld;
  fehlt es oder weicht der Modus ab, antwortet das NAS mit 409
  `mode_changed` und sendet NICHTS.
  **Nachsendung nach einem Teilausfall**: REAL ALARM und ENTWARNUNG nehmen
  optional `only_ids="3,5"` an (`requested_only_ids()` → `bark_broadcast()`);
  die Auswahl kann die effektive Empfängerliste nur VERKLEINERN (Demo-Modus
  bleibt beim Test-Empfänger, gelöschte ids fallen heraus). Ein vorhandenes,
  aber unbrauchbares Feld wird mit 400 abgelehnt, eine leere Schnittmenge
  sendet nichts — NIE ersatzweise an alle. Dafür tragen die Sende-Ergebnisse
  und `effective_alarm_entries()` die `id` (`api/keys.php` gibt sie nicht aus);
  im Verlauf steht `resend` = Anzahl. Eine Nachsendung trägt wie in der
  Firmware die Zeit des URSPRÜNGLICHEN Versands (das Dashboard schickt
  `orig_at` = `started_at` der ersten Antwort, angenommen bis 24 h; sonst
  jüngster `*_started` ohne `resend` im Verlauf, aber nur bis 6 h alt,
  sonst jetzt) plus „(verspaetet zugestellt)“
  (`resend_stamped_body()`); das Dashboard behält die Liste der nicht
  Erreichten (`unreached` im Store, gemerkt in `localStorage`) sichtbar, bis
  ein neueres Ergebnis sie ersetzt oder der Admin „Ausblenden“ klickt. Der `ALARM`-Zweig in der Firmware
  läuft durch dieselbe Zustellung wie ein Relaisalarm
  (`startAlarm(true, …)`/`processPendingAlarm()`, Merker `pendingIsRemote`),
  setzt aber KEINEN Cooldown und keine Wiederbewaffnung (sonst würde ein echter
  Relaisalarm danach `COOLDOWN_MS` lang verworfen); ein Relaisalarm während der
  Demo-Zustellung hat Vorrang und ersetzt sie. Läuft schon eine Zustellung,
  antwortet die Box `skipped_busy`. Der Status-Push meldet `alarm_source`
  (`relay`|`remote`) und den Zähler `alarm_seq`.
  **TEST** läuft dagegen absichtlich über den ESP32 (Poll → `sendStatus()` an
  `BARK_KEY_STATUS`) — er testet die halbe Kette mit — und darf die
  Alarm-Zustandsmaschine nicht verändern. **ENTWARNUNG** (`false_alarm` /
  `bark_send_false_alarm_all()` → `bark_false_alarm_fields()`): ebenfalls
  **Critical Alert** (ASCII-Texte) an die effektive Empfängerliste, direkt vom
  NAS — muss durchdringen, sonst fährt jemand umsonst los. Unterschied zum
  Alarm: kurzer Standardton (`bark_false_alarm_sound`, Default `glass`) statt
  `alarm_fw` und **kein** `call=1`. Stummschaltungen (Arbeitsmodus) gelten
  wie beim Alarm (überfällige rechnet die Leseseite schon vorher als laut,
  `expire_stale_mutes()` schreibt sie nach dem Versand weg). Im Demo-Modus geht
  auch sie nur an den Test-Empfänger.
- **Empfängerliste wird im Dashboard gepflegt** (eine Quelle für beide
  Alarmwege): versioniert in `alarm_keys.json(.php)` im `data_dir`, verwaltet
  über `dashboard_api.php` (`keys_list`/`keys_add`/`keys_delete`, dazu
  `keys_test` = Test-Push an EINEN Empfänger über `bark_send_test()`: normale
  Mitteilung `level=active` mit dem Alarmton, bewusst KEIN Critical Alert und
  ohne Lock/Versions-Bump — prüft Key und Ton ohne Demo-Modus; das Dashboard
  fragt vorher nach, weil dabei der Alarmton spielt; letzter
  Empfänger nicht löschbar, max. 10 = `MAX_ALARM_KEYS`, Key-Format validiert).
  Alle Schreiber laufen über `mutate_alarm_keys()` (Lock, Lesen, Ändern,
  `backup_then_write()`); Versionen nur über `bump_keys_version()` — monoton
  über eine Hochwassermarke (`alarm_keys_version.json`), damit ein `.bak`-Restore
  die Version nie auf eine Nummer zurücksetzt, die der ESP32 mit anderem Inhalt
  kennt. `read_json_file()` unterscheidet `missing`/`corrupt`;
  `read_alarm_keys_state()` fällt bei kaputter Datei auf die `.bak` zurück und
  liefert `null`, wenn beide unlesbar sind — Schreiber brechen dann ab, statt
  mit leerer Liste neu anzufangen. `backup_then_write()` sichert nur einen
  lesbaren Stand. `effective_alarm_entries()` bereinigt (gültige Keys, keine
  Duplikate, max. 10) — genau das, was die Firmware akzeptiert. `poll.php` liefert
  `keys_version=N` mit; weicht sie ab, holt der ESP32 die Liste über
  `api/keys.php` (Plain Text: `version=`, `count=`, `key0=`…, Nonce gespiegelt)
  und persistiert sie per `Preferences` im NVS (`syncAlarmKeys()`), damit sie
  Reboot/NAS-Ausfall übersteht. Sync **nie** während eines offenen
  RELAIS-Alarms; ein offener Demo-Alarm hält ihn nicht auf, wird aber
  verworfen, wenn sich das Key-Set ändert (seine `keyDone[]` gehören zur alten
  Liste – sonst ginge sein Rest z.B. an die volle LIVE-Liste). Leere oder
  unplausible Listen (`ok`≠1, falsche Nonce, `version` < 1, `count`, ungültiger
  Key) werden verworfen – mit eigenem Backoff (`KEY_SYNC_REJECT_BACKOFF_MS`)
  und EINER Owner-Warnung bis zur nächsten angenommenen Liste. NVS-Schreib-
  reihenfolge in `saveAlarmKeys()`: `v=0` → Einträge → `n` → echte Version
  (Rückgaben geprüft); `loadAlarmKeys()` prüft `n`, `v` ≥ 1 und jeden Key per
  `validAlarmKey()` – sonst config.h-Liste mit Version 0 (erzwingt Sync). Eine
  Übernahme wird nur dann (als Owner-Warnung mit Wiederholung) an den Owner
  gemeldet, wenn sich das **Key-Set** geändert hat
  (Anzahl oder Keys, inkl. Demo-Wechsel) — reine Stumm-Wechsel (Arbeitsmodus)
  laufen bewusst still, sonst käme bei jedem Kurzbefehl-Aufruf der Kollegen
  ein Push. `BARK_KEYS_ALARM` (config.h) und `bark_keys_alarm`
  (config.php) sind nur noch Start-/Fallback-Listen: die Firmware nutzt ihre
  nur bis zum ersten Sync (Version 0), das NAS seine, solange die EFFEKTIVE
  Liste leer ist (auch bei Version > 0, z.B. alle Keys ungültig oder Liste
  samt `.bak` unlesbar; entdoppelt).
  Robustheit der Liste: `read_alarm_keys_state()` wertet auch eine falsche
  Struktur (`"keys": false`) als kaputt und greift auch bei FEHLENDER Datei auf
  eine vorhandene `.bak` zurück. `bump_keys_version()` nimmt zusätzlich die
  zuletzt von der Box gemeldete `keys_version` ins Maximum (nach Verlust des
  `data_dir` liefe die Version sonst auf eine Nummer, die die Box mit anderem
  Inhalt kennt); neue ids vergibt `allocate_key_id()` ebenfalls über eine
  Hochwassermarke (`next_id` in `alarm_keys_version.json`). `api/keys.php`
  liest Liste und Demo-Zustand unter dem `keys`-Lock (ohne Schreibrecht
  lockfrei) und dann in dieser Reihenfolge: erst Liste/Version, dann Demo –
  passend zur Schreibreihenfolge von `demo_set`, NICHT tauschen.
  `keys_list` liefert `muted` effektiv (überfällig = laut) plus
  `mute_overdue`: Die Rückschaltung ist fällig, aber nicht gespeichert – die
  BOX alarmiert dann weiter stumm, denn sie holt die Liste nur bei neuer
  Version (das Dashboard zeigt die Zeile orange als „Evtl. stumm“). Ist die Liste samt `.bak`
  unlesbar, antwortet `keys_list` mit `code=keys_corrupt`.
  Weitere Empfänger-Aktionen (alle Admin+CSRF, über `mutate_alarm_keys()`,
  Verlauf per `log_key_event()` nach dem Lock): `keys_rename` (Name wie bei
  `keys_add`; KEIN Versions-Bump, die Box kennt den Namen nicht; ist es der
  aktive Demo-Empfänger, schreibt sie `label` in `demo_mode.json` mit
  `backup_then_write()` – im selben `keys`-Lock, vor `alarm_keys.json`, wie
  `demo_set`; Ereignis `key_renamed`), `keys_replace_key` (neuer Bark-Key für
  denselben Empfänger, Name/Stumm-Zustand/`mute_token`/Telefon bleiben; bumpt
  die Version; für den AKTIVEN Demo-Empfänger abgelehnt, weil `demo_mode.json`
  den Key kopiert hält; Duplikat-Prüfung wie `keys_add`; `key_replaced` mit
  beiden Keys nur maskiert) und `keys_set_phone` (Telefonnummer `''` = löschen,
  sonst `/^[0-9+\-\/() ]{3,30}$/`; KEIN Bump; `key_phone_changed` ohne Nummer)
  sowie `keys_new_mute_token` (neuer Geheim-Token = neue Kurzbefehl-Links, die
  bisherigen gelten sofort nicht mehr – der Ausweg, wenn ein Link in falsche
  Hände geraten ist; Stumm-Zustand bleibt; KEIN Bump; der Token steht weder in
  der Antwort noch im Verlauf, Ereignis `key_token_renewed`; der alte
  `key=`-Link hängt am Bark-Key und wird nur durch `keys_replace_key`
  entwertet).
  `keys_add` nimmt optional `phone` (gleiche Prüfung; ungültig = nichts
  angelegt). Die Telefonnummer ist reine Dashboard-Anzeige: `keys_list` liefert
  sie nur der Admin-Rolle, NIE `api/keys.php`, Bark-Texte oder der Verlauf.
- **Demo-Modus (Dashboard, nur Admin)**: leitet beide Alarmwege auf genau einen
  Test-Empfänger aus der gepflegten Liste um — ohne Firmware-Änderung, über
  denselben Empfängerlisten-Mechanismus: `demo_mode.json` im `data_dir` hält den
  Zustand, `effective_alarm_entries()` in `common.php` liefert im Demo-Modus nur
  den Demo-Key (genutzt von `api/keys.php` UND `bark_send_alarm_all()`), jeder
  Moduswechsel erhöht die `keys_version` → ESP32 holt die Liste beim nächsten
  Poll. `demo_set` schreibt erst `demo_mode.json`, dann `alarm_keys.json`
  (diese Reihenfolge ist gegenüber dem lockfreien Leser `api/keys.php`
  selbstheilend — NICHT tauschen); scheitert der zweite Schritt, wird der
  Demo-Zustand zurückgerollt. Ausschalten ist idempotent und erhöht immer die
  Version (Reparaturweg); scheitert auch das Zurückrollen, antwortet
  `demo_set` laut mit `code=inconsistent`. Ist `demo_mode.json` kaputt oder
  verschwunden (obwohl eine `.bak` existiert), rechnet das NAS LIVE, die Box
  bliebe aber still auf der Demo-Liste – `repair_broken_demo_mode()` (in
  poll.php, keys.php und im Cron) schreibt dann LIVE fest und erhöht die
  Version (Verlauf: `demo_repaired`); die `.bak` hilft dort nicht, sie hält
  den VORIGEN Modus. Schutzregeln: Demo-Empfänger muss aus der Liste stammen (beim
  Zurückschalten existiert so garantiert eine nicht-leere Liste, die der ESP32
  akzeptiert), der aktive Demo-Empfänger ist nicht löschbar, das Dashboard zeigt
  auf jeder Seite ein oranges Demo-Band mit Sync-Status (`demo_sync_ok` bzw.
  Vergleich ESP32-`keys_version` vs. NAS-Version). Der Alarm selbst ist im Demo-Modus bewusst zeichengenau
  identisch mit einem echten (gleicher Text/Ton/Level, kein Demo-Zusatz) —
  nur die Empfängerliste ist kürzer.
- **Arbeitsmodus (Stummschaltung pro Empfänger)**: `muted`/`muted_at`/
  `mute_volume` pro Eintrag in `alarm_keys.json`; stumme Keys bekommen
  weiterhin denselben Critical Alert, aber mit ihrer **Stumm-Lautstärke**
  (`mute_volume` 0–10, Default 0 = lautlos) und ohne `call=1` — durchbricht
  trotzdem Stummschalter/Fokus (beide Alarmwege: `bark_alarm_fields()` mit
  `$muted`/`$muteVolume` und die Firmware über `alarmKeyMuted[]`/
  `alarmKeyMuteVolume[]`). Umschalten per öffentlichem GET/POST-Endpoint
  `api/mute.php?t=<GEHEIM_TOKEN>&state=on|off` (Token `mute_token` pro
  Eintrag, im Dashboard kopierbar und nur an die Admin-Rolle ausgeliefert;
  Alt-Form `key=<BARK_KEY>` bleibt gültig; Bremse gegen Durchprobieren über
  einen persistenten Zähler pro Absender (`throttle_*`, Bucket `mute`, KEIN
  `usleep`) — gezählt und gebremst werden NUR Fehlversuche, ein gültiger
  Token funktioniert immer (sonst könnte jemand hinter derselben Adresse die
  Links aller Kollegen sperren); idempotent; ruft ein BROWSER den Link auf
  (`Sec-Fetch-Dest: document` bzw. `Accept: text/html`), antwortet
  `mute_answer()` mit einer kleinen lesbaren HTML-Seite (ohne Token/Key,
  Stylesheet des Dashboards) — Kurzbefehle und Skripte bekommen UNVERÄNDERT
  den Text `ok=1`/`muted=…`; HEAD-Anfragen und bekannte Link-Vorschau-Abrufer
  (iMessage/WhatsApp/… User-Agents) schalten bewusst NICHTS um;
  bewusst KEIN `name=`-Parameter — Namen wären erratbar) oder per
  Dashboard-Aktion `keys_mute` (Admin+CSRF); Stumm-Lautstärke über
  `keys_set_mute_volume` (Admin+CSRF). Jeder Wechsel erhöht die
  `keys_version`; `api/keys.php` liefert `muted{i}=0|1`- und
  `mutevol{i}=0..10`-Zeilen mit (NIE die Tokens), die Firmware persistiert
  sie im NVS (`m{i}`/`mv{i}`, fehlend = laut bzw. 0 — alte Firmware/altes
  NAS bleiben so in sicheren Modi). `ensure_mute_tokens()` rüstet
  Bestandseinträgen Tokens nach (Lazy, ohne Versions-Bump, nimmt selbst den
  `keys`-Lock). Sicherheitsnetz `expire_stale_mutes()` (`mute_max_seconds`,
  Default 12 h, 0 = aus; der wirksame Wert kommt aus `app_settings.json` – im
  Dashboard einstellbar über `settings_list`/`settings_set` (nur Admin), Stufen
  2/4/6/8/12/16/24 h plus der config.php-Wert, Lock `settings` – und fällt
  sonst auf `config.php` zurück; EINE Stelle: `mute_max_seconds()`): läuft in poll.php (damit es den Relaisweg auch OHNE
  Cron erreicht), mute.php, keys.php, keys_list, nach `bark_send_alarm_all()`
  und im Cron-Wächter (dort auch ohne `bark_key_status`); die Leseseite
  rechnet überfällige Stummschaltungen schon vorher als laut
  (`mute_is_stale()` — EINE Definition für beide). Nimmt selbst den
  `keys`-Lock (NIE aus einem gehaltenen `keys`-Lock aufrufen; `$soft=true` =
  nicht fatal); die Info-Pushs laufen erst NACH der Antwort
  (`defer_after_response()` + `fastcgi_finish_request()`), nie davor — der
  ESP32 hat nur 4 s Timeout. Der Wächter erinnert zusätzlich am Status-Key (Standard max. 1×/Tag),
  solange jemand stumm ist (`mute_reminded_at` in `watchdog_state.json`).
  Beim **manuellen** Umschalten geht bewusst KEINE Bestätigung mehr an den
  betroffenen Key (Nutzerentscheidung, störte nur); nur die **automatische**
  Rückschaltung durch `expire_stale_mutes()` meldet sich weiterhin
  (ASCII-Texte!).
- **Dauerhaft stumm (`mute_permanent`)**: Pro Eintrag abschaltbares
  Sicherheitsnetz für längere Abwesenheiten. Ist das Flag gesetzt, überspringt
  `expire_stale_mutes()` den Eintrag (`mute_is_permanent()`) — die
  Stummschaltung läuft dann NIE von selbst aus. Bewusst **reine NAS-Logik**:
  Die Firmware kennt weiterhin nur `muted`/`mutevol`, `api/keys.php` liefert
  das Flag NICHT aus, und es gibt **keine** Firmware-Änderung dafür. Deshalb
  bumpt `keys_set_mute_permanent` (Admin+CSRF) die `keys_version` nur, wenn
  sich dabei auch `muted` ändert — sonst löst es einen sinnlosen Key-Sync aus.
  Einschalten setzt immer zugleich `muted=true` (kein Zustand „permanent, aber
  laut"). Beenden geht über `keys_set_mute_permanent`, `keys_mute` (Laut) und
  `api/mute.php?state=off` — alle drei löschen das Flag, weil „laut" immer die
  sichere Richtung ist. Diese Regel steht EINMAL in `apply_mute_change()`
  (common.php), über die alle Stumm/Laut-Schalter laufen (Dashboard,
  Kurzbefehl-Link, automatische Rückschaltung); sie meldet zurück, ob eine
  Dauer-Stummschaltung endete (→ zusätzlicher `key_mute_permanent`-Eintrag). Der öffentliche Kurzbefehl-Link kann es bewusst **nur
  beenden, nie setzen**. `keys_list` liefert `mute_permanent` mit und lässt
  `mute_until` dann weg (es gibt keine Endzeit); das Dashboard zeigt eine
  eigene rote Pille „Dauerhaft“. Der Cron-Wächter zählt dauerhaft Stumme getrennt und
  erinnert für sie nur 1×/Woche (`permanent_mute_reminded_at`) statt 1×/Tag —
  eine bewusste Dauereinstellung darf nicht täglich pingen. (Beides sind
  Standardwerte — die Abstände stellt der Admin im Dashboard ein, siehe
  nächster Punkt.)
- **Meldungen an den Admin (einstellbar)**: Was das NAS an `bark_key_status`
  schickt und wie, stellt der Admin im Dashboard ein (Einstellungen →
  „Meldungen an mich“; `notify_list`/`notify_set`/`notify_test` in `dashboard_api.php`, alle
  nur Admin; gespeichert in `notify_settings.json` unter dem `notify`-Lock).
  EINE Quelle für Arten und Möglichkeiten: `admin_notify_types()` und
  `admin_notify_global_specs()` in `common.php`. Jeder Wert ist eine Auswahl
  aus festen `choices`; `load_notify_settings()` liefert immer einen
  vollständigen, geprüften Stand (Unbekanntes fällt auf den Standard),
  `notify_set` lehnt unbekannte Werte ab und speichert NUR ausdrücklich
  Gesetztes — alles andere folgt weiter den Standards bzw. `config.php`
  (`watchdog_offline_after_seconds`, `demo_reminder_after_seconds`). Pro Art:
  ein/aus, Dringlichkeit (Bark-Level passive/active/timeSensitive/critical),
  Verhalten in der Ruhezeit und Artspezifisches (Offline-Schwelle,
  Wiederholungen, „wieder da“-Meldung …); global: Ruhezeit (kappt die
  Dringlichkeit auf leise, außer bei Arten mit `ignore_quiet`), Ton,
  Lautstärke kritischer Meldungen und ein Stellvertreter (`extra_id` aus der
  Empfängerliste, best effort). ALLE Admin-Meldungen des NAS laufen über
  `admin_notify_send()` (Rückgabe `sent`/`failed`/`off`, Texte per
  `bark_ascii()`); `bark_send_status()` bleibt nur für die Info an einen
  BETROFFENEN Empfänger (automatische Laut-Rückschaltung). Standard = Verhalten
  von vorher: die alten Arten sind an und leise; NEUE Arten (`login_success`,
  `mute_changed`, `mute_expired`, `box_restart`, `box_health`,
  `weekly_report`) sind AUS. Im Wächter gilt eine abgeschaltete alte Art als
  erledigt (`$notify()` liefert true: Einmal-Logik und Verlaufseintrag laufen
  weiter, Ausgabe `OHNE_PUSH …`); die neuen Arten wertet er abgeschaltet gar
  nicht erst aus. `box_offline` und `nas_web` sind sicherheitsrelevant
  (`important`): Das Dashboard fragt vor dem Abschalten nach und zeigt den
  Zustand als Hinweis (`watchdog.offline_alert`/`web_alert` im Status).
  Meldungen aus Web-Anfragen (Login, Kurzbefehl-Link, Status-Push der Box)
  gehen per `defer_after_response()` erst NACH der Antwort raus — beim Login
  vorher `session_write_close()`, sonst wartet die Weiterleitung auf Bark.
  `box_restart` erkennt `api/status.php` an der gesunkenen `uptime_s` derselben
  `device_id` ODER an einem nach hinten gesprungenen Startzeitpunkt
  (`seen_at − uptime_s`, fängt Absturz-Schleifen mit einem Push pro Start) –
  Letzteres nur, wenn `uptime_s` nicht länger ist als der Abstand zum vorigen
  Push (sonst täuscht eine NTP-Korrektur der NAS-Uhr bei einer seit Wochen
  laufenden Box einen Neustart vor).
  Regeln, die beim Umbau bleiben müssen: (1) `admin_notify_send()` liefert
  `off` NUR bei abgeschalteter Art oder leerem Status-Key — ein eingetragener,
  aber ungültiger Key ist `failed` (sonst hakt der Wächter einen Ausfall still
  als gemeldet ab), eine unbekannte Art ebenfalls. Hat nur der Stellvertreter
  angenommen, ist das `sent` (kein Wiederholen alle 15 min an ihn) mit
  `$used['primary_failed']` – der Wächter führt den Status-Key dann trotzdem
  als gestört (`last_send_failed_at`). (2) Die Standards aus `config.php` gelten SEKUNDENGENAU
  (Auswahl-Schlüssel sind Sekunden: `offline_after_s`, `after_s`), nichts wird
  gerundet. (3) „Wieder online“ gibt es erst nach einem NEUEN Status-Push
  (`offline_seen_at`), nicht schon, weil die Schwelle hochgesetzt wurde.
  (4) War eine fällige Meldung abgeschaltet (`offline_push_pending`/
  `web_push_pending`), wird sie nach dem Wiedereinschalten sofort nachgeholt;
  ohne zugestellte Ausfall-Meldung gibt es kein „wieder da“. (5) Die
  Erinnerung „weiterhin OFFLINE“ nutzt denselben Text wie die erste Meldung
  (inkl. Server-Diagnose bei `blocks_pushes`). Der Text kommt aus
  `box_offline_notice_text()` (common.php): Solange der letzte Status der
  Box über die Brücke kam (`via_bridge`, also bis zum Flashen), nennt er das
  NAS zu Hause als möglichen Grund – und als wahrscheinlichen, wenn auch der
  Außen-Wächter (er läuft auf demselben NAS) seit > 9 min schweigt; dann
  entfällt der Satz „Relaisalarm wuerde NICHT ausgeloest“, denn die Box
  alarmiert weiter direkt über Bark. Der Text von „Dashboard DEFEKT“ kommt
  aus `web_fail_notice_text()`: Hinweis je nach Ursache (Datenordner →
  `chown`, Webserver → Dienste prüfen). (6) Die Lese-Rolle erfährt nichts
  über diese Einstellungen: kein `notify_changed` in `log_view`, kein
  `offline_alert`/`web_alert`/`outside_alert` im Status (dort liest
  `important_notify_flags()` nur diese Schalter, nicht die ganze Tabelle).
  (7) `heartbeat_ok=0` zählt
  nur als Fehler, wenn `last_heartbeat` nicht `nie` ist — Schwellen für
  `box_health` stehen doppelt (`cron/check_offline.php` und
  `assets/app/logic.js`), beide Stellen anpassen.
  Der Kurzbefehl-Link leitet Browser nach dem Umschalten per 303 auf
  `mute.php?done=…` um (ohne Token, schaltet nichts), damit Neu-Laden nicht
  erneut schaltet. NICHT einstellbar ist, was die Firmware selbst sendet
  (Heartbeat, Start-Meldung, „Kontakt klemmt?“, Listen-Übernahme, TEST).
- **Command-Protokoll** ist Plain Text (`ok=1`, `nonce=...`, `id=...`,
  `type=...`). Der ESP32 schickt pro Poll eine Nonce, die das NAS spiegeln muss.
  Das NAS vergibt monotone IDs, markiert einen gepollten Befehl sofort persistent
  als `delivered` und sendet ihn nicht erneut; der ESP32 ACKt danach. Diese
  at-most-once-Auslieferung verhindert Replay-Doppelalarme bei NAS-Reboot oder
  verlorenen alten Antworten. ACK-Verlust führt zu Dashboard-Status `delivered`,
  nicht zu heimlichem Neuauslösen. `poll.php` und `ack.php` sind bei einem
  gesperrten Datenordner NICHT fatal (`with_lock_soft`): Poll antwortet ohne
  Befehl (`type=NONE`, aber mit `keys_version`), ACK antwortet `OK` und
  protokolliert nur – ein HTTP 500 schickte die Box in ihren Fehler-Backoff,
  in dem sie auch keinen Status mehr pusht. Eine wiederholte, gleiche ACK
  wird nur bestätigt (kein zweites `acked` im Verlauf). Ein nicht abgeholter `pending`-Befehl
  verfällt nach `pending_command_ttl_seconds` (Default 300 s) als `expired` und
  ist im Dashboard abbrechbar (`cancelled`) — ein Befehl an einen toten ESP32
  darf nicht Stunden später noch ausgeführt werden.
- **Probealarm-Fenster dämpft nur die Lautstärke, unterdrückt nichts**: Der
  wöchentliche ILS-Probealarm löst weiterhin bewusst den vollen Alarm aus (testet
  die ganze Kette wöchentlich mit) — er geht nur leiser raus. Erkennt die Firmware
  einen Alarm im Fenster (`inProbeAlarmWindow()`: `PROBE_WEEKDAY`,
  `PROBE_WINDOW_START_MIN`…`PROBE_WINDOW_END_MIN`, alles aus `config.h`), sendet sie
  `PROBE_ALARM_VOLUME` (5) statt `ALARM_VOLUME` (10) — sonst zeichengenau identisch
  (gleicher Text/Ton, weiterhin `level=critical`, weiterhin `call=1`). Das Fenster
  darf **niemals** dazu führen, dass ein Alarm ausbleibt. Sicherungen, die bleiben
  müssen: ohne NTP-Zeit (`getLocalTime()` schlägt fehl) gilt die volle Lautstärke;
  außerhalb von `PROBE_WEEKDAY` ebenso; die Lautstärke wird **einmal bei der
  Erkennung** in `pendingVolume` festgehalten (wie `pendingBody`; beides in
  `alarmTimingFor()` – bei einem eingerasteten Impuls für den Zeitpunkt seines
  Beginns), damit Nachsende-Runden nach Fensterende nicht plötzlich laut
  werden (auch nicht nach einem Neustart: `pendingVolume` liegt im RTC). Stumme Empfänger
  (Arbeitsmodus) bleiben unabhängig davon bei ihrer Stumm-Lautstärke. Der manuelle
  Dashboard-Alarm (`bark_send_alarm_all()`) kennt das Fenster bewusst **nicht** —
  ein Mensch löst nie den Probealarm aus.
- Bark-Versand: simple `application/x-www-form-urlencoded`-POST mit manuellem
  `urlEncode()` — **keine JSON-/HTTP-Bibliothek** hinzufügen. Nur ESP32-Core-Libs
  (`WiFi`, `HTTPClient`, `WiFiClientSecure`, `time.h`, `esp_task_wdt`,
  `esp_timer`, `esp_system`, `esp_attr`, `Preferences`).
- Watchdog-Init ist core-versionsabhängig (`ESP_ARDUINO_VERSION_MAJOR >= 3` vs. 2.x)
  — beide Zweige beibehalten. Im 3.x-Zweig ist der Task-Watchdog ab Werk schon
  initialisiert: liefert `esp_task_wdt_init()` `ESP_ERR_INVALID_STATE`, muss
  `esp_task_wdt_reconfigure()` folgen, sonst bleiben die Default-Werte (5 s, kein
  Reset) aktiv. Scheitert die Einrichtung, warnt die Box den Owner. Wirksam ist
  `WDT_EFFECTIVE_TIMEOUT_S` = max(`WDT_TIMEOUT_S`, 60 s bzw. ungünstigster
  HTTPS-Versuch + 10 s) – ein Versuch (DNS ~15 s + Verbindung + Handshake +
  Senden + Antwort) muss sicher darunter bleiben; gefüttert wird vor/nach jedem
  Versuch und zwischen den Keys.

## Bewusste Entscheidungen (nicht „wegoptimieren")

- TLS via `client.setInsecure()` (keine Zertifikatsprüfung) — gewollt, einfach/robust.
  Gilt auch für das NAS; Authentifizierung läuft über ein langes Maschinen-Token.
- OTA ist **nicht** eingebaut, nur als README-TODO dokumentiert.
- **Cloudflare vor dem Dashboard** (Zone `tunero.dev`, Einstellungen müssen so
  bleiben): Bot Fight Mode AUS (lässt sich im Free-Plan nicht pro Adresse
  ausnehmen), Mindest-TLS 1.2 (die Box kann nur 1.2), „Always Use HTTPS“ an;
  für `alarm.tunero.dev` eine Konfigurationsregel (Browser Integrity Check,
  E-Mail-Verschleierung, Rocket Loader, Under-Attack, RUM aus) und eine
  Cache-Regel (Cache umgehen) – sonst blockt oder verändert Cloudflare die
  Antworten für Box, NAS-Brücke und Kurzbefehle. Bewusst KEIN Cloudflare
  Access davor (Mail-Code wäre im Notfall Reibung). Cloudflare beendet TLS
  und sieht Maschinen-Token, Bark-Keys und Stumm-Tokens im Klartext – in
  Kauf genommen; das Cloudflare-Konto braucht deshalb zweistufige Anmeldung.
  Lokal verwalteter Tunnel (`cloudflared`, Konfiguration
  `server/cloudflared/config.yml.example`). `server/check.sh public` prüft,
  dass ein Aufruf wie von der Box (TLS 1.2, kein `Accept`) und einer ohne
  `Accept` die Antwort der APP bekommen.
- **Auf der VM nichts über `/tmp`**: Was root ausführt (`server/setup.sh`,
  `check.sh`) oder was Geheimnisse enthält, wird in einen Ordner im
  Home-Verzeichnis kopiert (`~/fw-server`, Rechte 700 für Geheimnisse) –
  `/tmp` ließe einen anderen lokalen Benutzer Dateien unterschieben oder
  mitlesen. `setup.sh` verweigert den Start aus `/tmp`, `deploy.sh` nutzt
  `mktemp -d`.
- Alarm-Sound `alarm_fw` ist ein **eigener importierter** Bark-Ton; muss auf jedem
  Empfänger-iPhone vorhanden sein, sonst Standardton.
- Synology-Dashboard: separate Auth für Maschine (`REMOTE_MACHINE_TOKEN`, im PHP
  nur SHA-256-Hash, **nur per POST**, Hash-Vergleich case-/whitespace-tolerant)
  und Mensch (Dashboard-Login mit Passwort-Hash + CSRF, `sleep(1)` plus
  Sperrfenster pro Absender (IPv4 einzeln, IPv6 als /64) nach zu vielen
  Fehlversuchen als Brute-Force-Bremse — der Versuch wird VOR
  `password_verify` atomar gezählt (`throttle_hit()`), Login-POSTs von fremden
  Seiten (maßgeblich ist allein `Sec-Fetch-Site`: erlaubt sind `same-origin`,
  `none` und ein fehlender Header; `cross-site` UND `same-site` werden
  abgelehnt – unter `alarm.tunero.dev` wäre `same-site` jede andere Seite auf
  tunero.dev — bewusst KEIN Origin/Host-Vergleich,
  der hinter einem Host-umschreibenden Proxy jeden Login sperren würde) werden
  vorher abgewiesen; alle
  Throttle-Locks sind nicht fatal, damit ein fehlendes Schreibrecht den Login
  nicht sperrt; höchstens 500 gezählte Absender pro Bremse, darüber werden
  NEUE nicht gezählt – bewusst kein Sammel-Zähler, den ein Angreifer füllen
  und so den Admin aussperren könnte), Session-Idle-Timeout 12 h
  (`session.gc_maxlifetime` wird dafür gesetzt, sonst räumt PHP nach 24 min
  ab; `use_strict_mode`; Cookie-Pfad = Dashboard-Ordner; das Sitzungs-Cookie
  ist ein DAUERHAFTES 12-h-Cookie, bei jeder angemeldeten Anfrage per
  `refresh_dashboard_cookie()` erneuert – iOS wirft Cookies ohne Ablaufdatum
  weg, sobald die Home-Bildschirm-App geschlossen wird; die Eigenschaften
  kommen aus `dashboard_cookie_options()`; Logout löscht auch
  das Cookie; die Status-Antwort liefert das aktuelle CSRF-Token mit, damit
  ein alter Tab nach Neu-Login nicht am veralteten Token scheitert;
  eine Sitzung gibt es NUR für Angemeldete: `index.php` und
  `dashboard_api.php` setzen sie über `resume_dashboard_session()` fort (nur
  wenn ein Sitzungs-Cookie mitkommt; eine nicht angemeldete Sitzung wird
  sofort verworfen, ohne Lösch-Cookie – das überschriebe bei einer
  verspäteten Anfrage das Cookie einer Neuanmeldung), angelegt wird sie erst
  bei erfolgreicher Anmeldung – anonyme Aufrufe bekommen weder Cookie noch
  Sitzungsdatei (sonst füllte jeder Scanner den Sitzungsordner); die Sitzung
  ist an die Zugangsdaten gebunden (`auth_stamp` =
  `dashboard_auth_stamp()`, HMAC über Rolle+Benutzer mit dem Passwort-Hash
  aus config.php): Passwort ändern meldet ALLE Sitzungen dieses Kontos ab –
  der einzige Weg, ein verlorenes, angemeldetes Handy auszusperren, denn
  eine pollende Sitzung läuft sonst nie ab),
  `session_write_close()` direkt
  nach der Auth in `dashboard_api.php` (ein laufender REAL ALARM blockiert so
  keine anderen Requests derselben Sitzung), ändernde Aktionen nur per POST,
  Sicherheits-Header/CSP zentral oben in `common.php` (dazu
  `Cache-Control: no-store` für JEDE PHP-Antwort – ein von einem Proxy/CDN
  zwischengespeichertes `ok=1` des Kurzbefehl-Links hieße, dass nichts
  geschaltet wurde), `commands.log`-Rotation
  ab 5 MB, Assets mit `?v=filemtime()`-Cache-Busting). Hinter einem
  Reverse-Proxy/Tunnel (geplant: Cloudflare Tunnel) sieht PHP nur die Adresse
  des Proxys und kein HTTPS: Dafür gibt es `trusted_proxies` +
  `proxy_client_ip_header` in config.php (beide leer = wie bisher nur
  `REMOTE_ADDR`). NUR bei Anfragen von einem dieser Proxys zählen
  `X-Forwarded-Proto` (`request_is_https()` → Secure-Cookie, HSTS) und die
  Adress-Kopfzeile (`login_client_ip()`, bei `X-Forwarded-For` der LETZTE
  Eintrag) – alle Bremsen pro Absender laufen über `client_throttle_key()`.
  Nie eine Forwarded-Kopfzeile ohne diese Prüfung auswerten (sonst sucht sich
  jeder Besucher seine Adresse aus). `api/health.php` lehnt Aufrufe mit
  Proxy-Kopfzeilen ab (ein Tunnel auf dem NAS käme sonst als 127.0.0.1 durch).
  Bark-Antworten werden nicht eingelesen (`bark_post_many()` verwirft den
  Inhalt, es zählt nur der HTTP-Code; keine Weiterleitungen). Zusätzlich optionaler
  Lese-Benutzer (`dashboard_readonly_user`/`..._password_hash`, Rolle in
  `$_SESSION['role']`, fehlende Rolle = `readonly`, fail-closed): sieht
  Status + Empfängerliste, alle ändernden/auslösenden
  Aktionen werden **serverseitig** per `require_dashboard_admin()` geblockt
  (UI-Ausblendung ist nur Komfort). Die Zugangsregeln ALLER Aktionen stehen
  an einer Stelle: `DASHBOARD_ACTIONS` oben in `dashboard_api.php` (`post`/
  `csrf`/`admin`, jeweils ausdrücklich); geprüft wird zentral vor jedem
  Handler (Methode 405 → CSRF 403 `csrf` → Rolle 403 `readonly`), die
  Handler prüfen das NICHT noch einmal. Eine Aktion ohne Eintrag ist gesperrt
  (GET 405, POST 400) – neue Aktion = neuer Eintrag. Das Verlaufs-Panel liest die letzten
  `commands.log`-Zeilen per Tail-Read (`log_view` in `dashboard_api.php`;
  optional `filter=alarm|keys|system` über `log_event_category()`/
  `read_command_log_filtered()` — sonst verdrängen die täglichen Stumm-Wechsel
  der Kurzbefehle die Alarme aus dem Fenster; `filter=alarm_core` = nur die
  Versand-Ereignisse für „Letzter Alarm“ auf der Übersicht, ohne die
  Befehls-Ereignisse der Demo-Alarme; was die Lese-Rolle nicht sehen darf
  (`notify_changed`) wird VOR dem Abzählen aussortiert; gefilterte Ergebnisse
  liegen in `log_cache.json`, solange sich `commands.log`/`.1` nicht ändern
  (Größe+mtime); ein `late_ack` trägt den Befehlstyp aus `recent_types` in
  `command_state.json`) und
  liefert **nur serverseitig whitelistete Felder** aus (Keys stehen im Log nur
  maskiert). EINE Tabelle für alle Verlaufs-Ereignisse: `log_event_types()` in
  `common.php` (Kategorie, `core` = Versand-Ereignis, `admin` = nur für den
  Admin, freigegebene Felder mit Typ/Pfad/Standard); daraus folgen
  `log_event_category()`, die Vorprüfung `log_line_may_match()`,
  `log_events_hidden()` und die Ausgabe `log_event_view()`. Ein neues Ereignis
  braucht dort einen Eintrag und in `assets/app/logic.js` einen Text (`describeLogEntry`)
  – sonst taucht es nicht auf. Einträge zu einem Empfänger schreibt
  `log_key_event()` (Key immer maskiert). Dauerhaft im Verlauf landen auch: Relaisalarme (`relay_alarm` —
  `api/status.php` loggt, wenn sich `last_alarm` (oder der Zähler
  `alarm_seq` – sendet erst die Firmware ab Stand 11/2026 (main), ebenso
  `alarm_source`/`core`/`max_alloc_heap`/`alarm_gaveup`; die Box im Betrieb
  kann noch einen älteren Stand ohne diese Felder haben, das NAS muss mit
  beiden auskommen) gegenüber dem zuletzt gemerkten Stand DIESER Box
  ändert — gemerkt pro `device_id` in `alarm_seen.json`, damit ein zweites
  Board mit demselben Token nichts unterdrückt; Wechsel zu „nie" =
  ESP32-Reboot, kein Alarm; `source=remote` = Demo-Alarm, wenn die Box den
  ALARM-Befehl kurz zuvor (≤ 300 s) mit `executed` quittiert hat ODER er vor
  ≤ 60 s ausgeliefert und noch nicht quittiert ist (die Box pusht ihren Status
  VOR der ACK – das ist der Normalfall) — sonst `relay`; die Fenster setzen
  `COOLDOWN_MS` ≥ 300 s voraus. Ein VERALTETER Push (traf früher ein als der
  gespeicherte, kleinere Laufzeit, < 60 s Versatz – passiert, wenn das NAS
  hängt) wird nur bestätigt, nicht verarbeitet (sonst Status-Rücksprung,
  Doppel-`relay_alarm`, falscher Neustart); ein Neustart zählt nur, wenn die
  Laufzeit nicht länger ist als der Abstand zum vorigen Push. Optional
  `box_device_id` in config.php (= `REMOTE_DEVICE_ID` der echten Box): Dann
  zählen nur deren Pushs für Status/Wächter/Demo-Sicherung, nur sie bekommt
  Befehle; andere Boards mit dem Token werden angenommen, aber nur in
  `devices_seen.json` geführt (`device_hint()` im Status: fremdes Board bzw.
  ohne die Einstellung „mehrere Boards melden sich“, sobald sich in den
  letzten 7 Tagen mehr als eins gemeldet hat – mit ausdrücklichem „ACHTUNG …
  ist sie AUSGEFALLEN“, wenn eins davon schweigt, während ein anderes
  weiterpusht; ein Hinweis nur über gerade aktive Boards verschwände genau
  dann, wenn die echte Box tot ist)) und
  Wächter-Ereignisse (`box_offline`/`box_recovered`, gleiche Einmal-Semantik
  wie die Bark-Meldung), außerdem `box_restart` (nur nach
  Absturz/Watchdog/Brownout), `key_test`, `notify_changed` und
  `watcher_silent`/`watcher_recovered` (Außen-Wächter verstummt/wieder da). Der ESP32-Status-Push enthält als Diagnose-Felder
  zusätzlich `reset_reason` (Token, im Frontend übersetzt und bei
  Watchdog/Panic/Brownout orange markiert), `free_heap` (Warnfarbe < 50 kB),
  `keys_muted`, `core` (Core-Version), `alarm_seq`, `alarm_source`,
  `max_alloc_heap` und `alarm_gaveup`; `fw_build` = `FW_BUILD_MARKER` plus
  Kompilier-Zeitpunkt. Wie das Frontend diese Werte zeigt, steht unten im
  Punkt „Frontend des Dashboards“. Die CSP kommt ohne `unsafe-inline` aus
  (keine `style=`-Attribute in PHP-Seiten und keine Inline-Skripte einführen –
  Startwerte gehen als meta-Tags an das Frontend). Empfängerliste/Demo-Zustand
  werden vor jedem Schreiben als `*.bak`-Guard-Datei gesichert
  (`backup_then_write()`). Optionaler Offline-Wächter: `cron/check_offline.php`
  (auf der VM: systemd-Timer `fw-alarm-cron.timer` alle 15 min als Benutzer
  `fwalarm`; Nginx sperrt `/cron/` von außen. Auf dem NAS war es eine
  DSM-Aufgabe als root, CLI oder POST mit Maschinen-Token. Folge auf der VM:
  Wächter und Web-Benutzer sind derselbe – kann `fwalarm` den Datenordner
  nicht beschreiben, scheitert auch der Wächter an seiner Sperre und meldet
  nichts; dann greift der Außen-Wächter nach 45 min, siehe
  `server/README.md` „Bekannte Grenzen“) meldet
  per `admin_notify_send()` (ASCII-Texte! Dringlichkeit, Abstände und
  Ein/Aus laut „Meldungen an den Admin“, Standard level=passive) einmalig
  Offline/Recovery an `bark_key_status`, erinnert an einen zu lange aktiven
  Demo-Modus (`demo_reminder_after_seconds`, danach max. 1×/Tag) und schreibt
  bei jedem Lauf `last_run_at` in `watchdog_state.json` — das Dashboard warnt
  daraus, wenn die DSM-Aufgabe fehlt oder hängt. Weil der Wächter als root
  läuft, prüft er die Schreibrechte des Web-Benutzers über einen Selbsttest:
  `web_health_check()` ruft `api/health.php` per `https://127.0.0.1/…` auf
  (`health_check_url`; antwortet NUR auf Loopback ohne Proxy-Kopfzeilen, sonst 404), das als `http`
  `data_dir_write_check()` ausführt (Lock, Schreiben, Zurücklesen, Löschen).
  Ab 2 Fehlschlägen in Folge Bark „Dashboard DEFEKT“, Erinnerung alle
  24 h, einmal „wieder OK“; Verlaufs-Events `nas_web_failed`/
  `nas_web_recovered`. Scheitert der Selbsttest, nennt die „Box OFFLINE“-
  Meldung das NAS als wahrscheinliche Ursache (Störung 10/2026: Box lief,
  `http` hatte nach einem Update nur noch Leserecht auf `web_packages`).
  `with_lock()` zeigt dann statt „Fehler.“ einen lesbaren Hinweis (ohne
  Pfad und ohne Benutzernamen – die Meldung erreicht auch nicht angemeldete
  Aufrufer; Pfad nur per `error_log`, der Befehl zum Beheben nur in der
  Bark-Meldung an den Admin; JSON für `dashboard_api.php` über die
  Konstante `FW_ALARM_JSON_API`). Was nur best effort ist (Mute-Ablauf im
  Alarmpfad, Login-Throttle, Verlauf, Poll/ACK der Box, Kurzbefehl-Link),
  nutzt `with_lock_soft()` statt des fatalen `with_lock()`. Lock-Reihenfolge
  (kein Zyklus erlaubt): `keys`→`log`, `command`→`log`, `watchdog`→`log`;
  `settings` (`app_settings.json`, `settings_set`) wird allein genommen –
  der Verlaufseintrag folgt erst nach seiner Freigabe; `log` ist immer innen, `expire_stale_mutes()`/`ensure_mute_tokens()`/
  `repair_broken_demo_mode()` nehmen `keys` selbst (nie aus einem gehaltenen
  `keys`-Lock), kein Bark-Versand unter einem Web-Lock. Legt der root-Cron Dateien neu an, übergibt
  `fix_owner()` sie an Besitzer/Gruppe des `data_dir` — aber NUR, wenn der
  Ordner einem Nicht-root-Benutzer gehört, und OHNE je die Rechte-Bits
  anzufassen (kein `chmod`: auf Synology kommt der Zugriff von `http` meist
  über File-Station-ACLs, die ein chmod aushebeln könnte — die Funktion darf
  Zugriff nie wegnehmen). `data_dir_write_check()` prüft zusätzlich (Schritt
  `perm`), ob die Dateien beschreibbar sind, die das Dashboard an Ort und
  Stelle beschreibt (`inplace_data_files()`: Verlauf, Lock-Dateien, `*.bak`) —
  bewusst NICHT alle Dateien, denn die übrigen ersetzt es per `rename()`.
  Reihenfolge: ERST der Ordner (lock/write/read/delete), DANN werden ALLE
  betroffenen In-Place-Dateien gesammelt (`files=` in health.php) – sonst
  meldete ein flächiger Rechteverlust nur „perm: commands.log“.
  Bei `perm` nennt die DEFEKT-Meldung die Dateien, und „Box OFFLINE“ gibt dem
  NAS nur dann die Schuld, wenn die Störung die Status-Pushs wirklich
  blockiert (`blocks_pushes`: Ordner-Schritt gescheitert, Web Station weg/5xx
  oder `status.lock`/`command.lock` betroffen – `data_files_block_pushes()`);
  ein falsch eingerichteter Selbsttest (3xx/4xx ohne `step=`) blockiert
  nichts. Die Wirkung im DEFEKT-Text hängt an `blocks_pushes`, nicht am Schritt. Derselbe Key aktiviert die
  Meldung bei ausgelöster Login-IP-Sperre. Ohne `bark_key_status` in
  config.php sind alle Meldewege inaktiv. Keine Geheimnisse in
  clientseitigem JavaScript. Laufzeitdaten (`data_dir`) bei Nginx möglichst
  außerhalb des Webroots ablegen; zusätzlich heißen alle Laufzeitdateien `*.php`
  mit Guard-Zeile (`DATA_FILE_GUARD`), damit Nginx sie nie als statische Datei
  ausliefert — Web Station ignoriert `.htaccess`. Diese Namens-/Guard-Logik
  steckt zentral in `data_path()`/`write_json_file()`/`read_json_file()`.
- **Frontend des Dashboards** (neu seit 11/2026, Spec
  `docs/superpowers/specs/2026-11-15-dashboard-redesign-design.md`). Dateien:
  `index.php` (Login/Logout-Logik, dann Anmeldeseite bzw. App-Hülle – beides
  aus `lib/shell.php`: `render_login_page()`/`render_app_page()`, gemeinsamer
  Kopf `render_head_tags()`); `assets/vendor/htm-preact-3.1.1.umd.js`
  (Preact + Hooks + htm, feste Version, LICENSE-Dateien daneben, in
  `.gitattributes` `-text`, läuft nie außerhalb des Browsers);
  `assets/app/{logic,api,store,ui,view-overview,view-recipients,view-log,view-settings,main}.js`
  als klassische Skripte mit gemeinsamem Namensraum `window.FW` – bewusst
  KEINE ES-Module (Importe trügen kein `?v=`, Import-Maps wären
  Inline-Skripte); feste Reihenfolge in `app_script_paths()`, alle `defer`,
  jedes Tag mit `?v=filemtime`. `assets/app/theme-boot.js` setzt synchron im
  `<head>` VOR dem Stylesheet das Design aus `localStorage` (`fwAlarm.theme`),
  sonst blitzt bei „Hell“ Schwarz auf. `assets/app.css` ist das einzige
  Stylesheet (Tokens oben; Hell-Block und „System“-Block identisch halten).
  `logic.js` = reine Logik ohne DOM/Preact (Urteil, Hinweise, Prüfzeilen,
  Verlaufs-Texte `describeLogEntry`, Bestätigungsfragen, nicht Erreichte),
  `store.js` = zentraler Zustand, Poller und Aktionen – die Ansichten lesen nur
  daraus. Startwerte kommen als meta-Tags (`csrf`, `role`, `user`,
  `public-base`, `mute-max`). Home-Bildschirm-App: `manifest.json` +
  `assets/icons/` (+ `apple-touch-icon.png`) + Apple-meta-Tags
  (`black-translucent`), bewusst KEIN Service Worker (das Notfall-Werkzeug
  lädt immer frisch). Die alte Oberfläche ist entfernt: `classic.php` leitet
  nur noch mit 302 auf `index.php` um (alte Lesezeichen; ohne Sitzung und
  ohne Cookie), `assets/classic/` gibt es nicht mehr. Die Kurzbefehl-Seite
  (`mute_page()` in `api/mute.php`) nutzt `app.css` mit dem Rahmen der
  Anmeldeseite.
  Aufbau (bewusst so, nicht „aufräumen“): vier Bereiche Übersicht/Empfänger/
  Verlauf/Einstellungen – unter 900 px Tab-Leiste unten, ab 900 px
  Seitenleiste; Adressen per Hash (`#/uebersicht`, `#/empfaenger/3`,
  `#/verlauf?filter=alarm`, `#/einstellungen/meldungen/box_offline` …), damit
  Zurück und Neuladen gehen. Die Status-Karte der Übersicht zeigt das Urteil
  (`verdict()`), Rangfolge: `auth` (abgemeldet) > `nas` (NAS nicht
  erreichbar/Störung/Status veraltet/beschäftigt – orange, ROT, wenn der
  letzte bekannte Stand „Box offline“ war) > `loading` (noch kein Status) >
  `offline` > `alarm` (läuft, rot pulsierend) > `stuck` (Kontakt klemmt) >
  `standing` („Alarm ausgelöst“: Zustellung durch, Kontakt noch geschlossen –
  rot, ruhig, nie grün) > `demo` > `hints` („Bereit – N Hinweise“, Farbe =
  schlimmster Hinweis) > `ok`. Hinweise (`collectHints()`, abschließend): rot
  = letzte Zustellung der Box unvollständig, Empfängerliste beschädigt;
  orange = Offline- bzw. Außen-Wächter mit Problem, WLAN getrennt oder
  ≤ −80 dBm, Heartbeat gescheitert, Neustart durch Watchdog/Absturz/Brownout
  (nur am ersten Tag), wenig Speicher, Box hat die Liste seit > 60 s nicht
  übernommen, ALLE stumm, `device_hint`, Rückschaltung überfällig. KEIN
  Hinweis: einzelne (auch dauerhaft) Stumme und ein Alarm der letzten Stunde –
  beides steht in der Prüfliste. Bei NAS-Störung keine Hinweise aus alten
  Daten. Farb-Regel überall: rot = offline/kaputt/nicht zugestellt, orange =
  unregelmäßig, grau = gerade unbekannt (veraltete Werte sehen nie normal
  aus); Farbe nie allein (Symbol ✓/!/✕ und Text). Die Gruppe „Auslösen“
  (Alarm an alle, Entwarnung, Test, im Demo-Modus Demo-Alarm über die Box)
  steht normal unter der Prüfliste und rückt bei ROTEM Urteil direkt unter die
  Status-Karte – der Alarm-Knopf muss bei „Box offline“ auch auf kleinen
  iPhones ohne Scrollen sichtbar sein. Die Lese-Rolle hat keine Gruppe
  „Auslösen“ (nur den Satz, einen Admin zu verständigen). Badge am Eintrag
  Übersicht und Status-Symbol im Seitentitel, solange das Urteil nicht grün
  ist; im Demo-Modus ein oranges Band auf jeder Seite. Das Ergebnis von Alarm/
  Entwarnung steht direkt unter „Auslösen“, nennt die nicht Erreichten zuerst
  (mit „Anrufen“, wenn eine Telefonnummer hinterlegt ist) und bietet die
  Nachsendung nur an sie an. Rückmeldungen stehen bei der auslösenden Aktion
  (`ui.Msg`: Farbe UND Zeichen ✓/✕/!, Erfolg verschwindet von selbst, Fehler
  bleiben). Sichtbare Texte sagen „die Box“ statt „ESP32“.
  Sicherheitsregeln im Frontend (nicht abschwächen): Bestätigungen nur über
  `store.openConfirm()`/`confirmAlarm()`/`confirmResend()` – Frage und
  `expect_demo` aus EINER Momentaufnahme, Frist 10–25 s je nach Länge der
  Frage (gegen die Uhrzeit gerechnet; nach Ablauf löst Bestätigen nichts
  aus), geschlossen bei verstecktem Tab und bei Moduswechsel, Schließen und
  Auslösen nur für genau ihre id. Alles, was einen Alarm sendet (Alarm an
  alle, Entwarnung, Demo-Alarm über die Box, Nachsendung), braucht „Gedrückt
  halten zum Senden“ (1,5 s, `ui.HoldButton`); andere Aktionen mit Folgen ein
  Sheet mit rotem Bestätigen-Knopf, Fokus startet auf „Abbrechen“. Nach einer
  unklaren Antwort gibt es KEINE Nachsendung (auch nicht über den Hinweis
  nach einem Moduswechsel). Die nicht Erreichten merkt sich der Store in
  `localStorage` (`fwAlarm.lastUnreached`, max. 12 h – iOS verwirft
  `sessionStorage` mit der Home-Bildschirm-App); angezeigt wird
  `unreached.entries` (zusammengeführt), nie nur das frische `failed`.
  Abmelden von Hand löscht beides vom Gerät (`store.forgetDevice()`: die
  Namen und den Merker „Versand ohne Ergebnis“ – sonst läse sie der Nächste
  an diesem Browser); läuft die Sitzung nur ab (401), bleiben sie bewusst
  stehen. Design und Browser-Hinweis bleiben immer.
  Scheitert ein Speichern, erhöht der Store `state.rev[slice]` – Bedienelemente
  mit eigenem Zustand sind darauf geschlüsselt und springen zurück.
  Alarm an alle, Entwarnung und die Nachsendung sperrt NUR `alarmInFlight`
  (andere laufende Aktionen sperren sie nicht); „Demo-Alarm über die Box“
  hängt zusätzlich am Zähler laufender Aktionen (`busyCount`), weil er sich
  die Befehlsschlange der Box mit TEST teilt. Der Halten-Knopf nennt die
  Aktion („Halten: Alarm senden“, „… Entwarnung senden“, „… Demo-Alarm
  senden“, „… erneut senden“; 19 px fett, Beschreibung „Zum Senden gedrückt
  halten“). Beim Öffnen einer Alarm-Frage holt der Store die Empfängerliste
  neu (die offene Frage bleibt ihre Momentaufnahme); bei beschädigter Liste
  nennt sie die Not-Liste aus `config.php`.
  **Versand ohne Ergebnis**: `sendNasAlarm()` setzt beim Start den Merker
  `fwAlarm.sendPending` (localStorage, max. 12 h), löscht ihn bei jedem
  eindeutigen Ergebnis (Antwort mit Ergebnis, sicher NICHT gesendet) und bei
  „Ausblenden“ – nach „Antwort unklar“ bleibt er stehen. Steht er beim
  Laden noch da (unklar, oder iOS hat die Seite während des Versands
  verworfen), zeigt die Übersicht „Letzter Versand ohne Ergebnis … – erst
  Verlauf prüfen“ OHNE Nachsende-Angebot. „Im Verlauf prüfen“
  (`store.verifyInLog()`) öffnet „Alarme“ ohne Suche und lädt IMMER frisch
  (kein gemerkter Stand, der wie „nichts gesendet“ aussähe); eine Adresse
  mit `?filter=` leert die Suche ebenfalls. „Ausblenden“ fragt nach,
  solange nicht Erreichte gemerkt sind (`store.dismissResult()`).
  **Unsicherer Stand sieht nie frisch aus**: Solange keine frische
  NAS-Antwort da ist (Status-Fehler oder 15 s ohne Antwort) bzw. nach der
  Abmeldung mit stehen gebliebenem Alarm-Ergebnis, steht auf JEDER Seite das
  Band `logic.unsureBand()` („Keine Verbindung zum Server – angezeigte Werte
  können veraltet sein (Stand HH:MM:SS)“, rot mit „letzter bekannter Stand
  …: Box OFFLINE“) über dem Demo-Band; auf der Übersicht ersetzt es die
  Problemzeile der Status-Karte. Dann sind auch alle Prüfzeilen grau; ein
  gescheiterter Abruf von „Letzter Alarm“ macht die Zeile grau mit
  „(nicht aktualisiert)“, ein gescheiterter Verlauf-Abruf zeigt über den
  gemerkten Einträgen „Verlauf konnte nicht aktualisiert werden – Stand von
  HH:MM“. Der Wechsel in den Verlauf lädt sofort (auch am PC). `pageshow`/
  `focus` bei sichtbarer Seite starten angehaltene Abfragen wieder (der
  Sekundentakt für „veraltet“ hängt daran). Abgemeldet mit Ergebnis:
  „Neu anmelden“ in Status- und Ergebnis-Karte (erst nach dem Versand).
  **Nie ohne Ausweg**: `#app` enthält in `lib/shell.php` eine statische
  Ersatzanzeige („wird geladen…“, nach 2 s „Neu laden“), die `main.js` erst
  vor dem ersten Zeichnen leert; fehlt
  ein Baustein (Preact, Store, UI, Übersicht), zeigt `main.js` statt der App
  eine Meldung mit demselben Link (ohne Preact gebaut). Fehlergrenzen
  (`ui.Boundary`/`ui.Fallback`, immer mit „Neu laden“): um die ganze App,
  um jede Ansicht und auf der Übersicht um jeden Teil – stürzt z.B. die
  Prüfliste ab, bleibt „Auslösen“ bedienbar.
  „Automatisch wieder laut nach“ fragt beim
  Verkürzen nach, wenn dadurch Stumme sofort laut würden
  (`logic.muteShortenImpact()`, Regel wie `mute_is_stale()`).
  Eine überfällige, nicht gespeicherte
  Rückschaltung (`mute_overdue`) erscheint orange als „Evtl. stumm“, nie als
  grünes „Laut“. Live-Aktualisierungen ändern keine offene Bestätigung, kein
  offenes Sheet, keinen gezogenen Regler und keine Eingabe.
  Abfragen (`store.js`): Status 5 s, Empfänger 30 s, Verlauf 30 s, Alarm-Info
  60 s, Meldungen/Einstellungen 60 s (Verlauf nur auf seiner Seite und der
  PC-Übersicht mit „Letzte Ereignisse“, Meldungen/Einstellungen nur auf ihrer
  Seite); Poller mit „läuft noch“-Schutz; ohne erfolgreiche
  Antwort seit 15 s gilt der Stand als veraltet (Urteil `nas`). Tab versteckt:
  alle Abfragen aus, jede offene Bestätigung zu; bei Rückkehr sofort
  aktualisieren. Ausnahme „Hinweis im Browser bei Störung“ (pro Gerät, nur wo
  der Browser Benachrichtigungen kann): Dann läuft bei verstecktem Tab nur die
  Status-Abfrage alle 60 s weiter und meldet Übergänge (Box offline, Alarm
  läuft/ausgelöst, NAS nicht erreichbar ab dem 3. Fehlschlag in Folge,
  Zustellung unvollständig, einmal „wieder in Ordnung“). HTTP 401 →
  „Abgemeldet“ und Neuladen zur Anmeldung – außer während ein Alarm-Versand
  läuft (dann bleibt das Ergebnis lesbar). CSRF-Token aus jeder
  Status-Antwort (auch ins Abmelde-Formular); nach einem CSRF-Fehler kein
  automatischer Neuversuch. Design (Dunkel/Hell/System) und Browser-Hinweis
  pro Gerät in `localStorage`, jeder Zugriff in try/catch.
  Tests: `node --test "tests/dashboard/*.test.mjs"` (Logik, Anfragen, Store,
  Kontrast, ortsneutrale Texte samt der Wächter-Texte aus PHP – die
  Ordner-Form `node --test tests/dashboard/` scheitert unter Node 24);
  Ende-zu-Ende: `node tests/dashboard/e2e/run.mjs <name>` startet das
  Dashboard über denselben Prüfstand wie die Server-Tests
  (`tests/backend/harness.mjs`, `FW_BACKEND` wählt die Fassung; leerer
  Datenordner pro Lauf, README in `tests/dashboard/e2e/`; Szenarien in
  `tests/dashboard/e2e/scenarios/`, Headless Chrome über CDP; von Hand:
  `node tests/backend/serve.mjs`). Neue Farbklassen gehören in `pairs()` von
  `tests/dashboard/contrast.test.mjs` (WCAG AA in beiden Designs).
- `REMOTE_HTTP_TIMEOUT_MS` nicht wieder unter ~4 s drücken: Der TLS-Handshake
  ESP32↔Synology braucht typischerweise 1–3 s; zu knappe Timeouts führen zu
  Dauer-Backoff und einem flappenden ONLINE/OFFLINE-Status.

## Konventionen

- Kommentare und Nutzertexte auf **Deutsch mit echten Umlauten (ä/ö/ü/ß)**, UTF-8 –
  auch in Git-Commit-Nachrichten. **Ausnahme:** Strings, die als `title`/`body` an
  Bark gehen (`ALARM_TITLE`, `ALARM_BODY`, die `sendStatus()`-Texte), müssen **ASCII
  (ae/ue/oe)** sein, weil die Bark-Notification keine Umlaute korrekt darstellt.
- Editierbare Werte in den Config-Dateien klar oben markiert lassen
  (`>>> HIER STEHEN ALLE WERTE … <<<`).
- Secrets niemals in `config.example.h` oder die `.ino` schreiben.
