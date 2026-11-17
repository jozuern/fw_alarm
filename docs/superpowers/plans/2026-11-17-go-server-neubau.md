# Go Server (Neubau) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the dashboard server (PHP 8.3 behind Nginx and PHP-FPM, plus the watchdog script on a timer) by one Go program `fw-alarm` that is built new inside and behaves the same outside — proven by the black-box tests in `tests/backend/` — and switch the Oracle VM over after a one-time import of the existing data.

**Architecture:** One process owns its state in memory as immutable typed snapshots behind atomic pointers. One writer at a time changes the durable part (`state.json`, saved and read back before it counts); volatile data, sessions and the history are written behind the answer. Alarm, box and login only ever read memory, so a dead disk stops none of them. The checker runs inside the server (full run every 15 min, "box offline" every minute); a local control socket serves `check`, `health`, `flush`, `reload`, `stop`. The frontend under `nas_dashboard/assets/` is embedded unchanged.

**Tech Stack:** Go 1.27 (pinned in `go.mod`, `CGO_ENABLED=0`, `windows/amd64` for local tests, `linux/arm64` for the VM), standard library plus four vendored modules: `golang.org/x/crypto` (Argon2id, bcrypt), `golang.org/x/sys`, `golang.org/x/term`, `github.com/BurntSushi/toml`. Tests: Go unit and fuzz tests, Node 24 `node --test` for the black-box suite, headless Chrome over CDP for the browser scenarios, PHP 8.3 only as the reference until Task 30. CI: GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-11-17-go-server-neubau-design.md` (German; § numbers below refer to it). Read it and `CLAUDE.md` before any task. `CLAUDE.md` lists the rules that must survive; spec §7 lists the ones that change or go; the tests are how both are checked. The PHP source under `nas_dashboard/` is the reference for every text and every rule the spec does not change — read the PHP function a task names before writing its Go counterpart.

## Global Constraints

- **No accidental alarm:** on the PC and in the VM trial `bark_host` always points at a mock on `127.0.0.1` (trial: a closed port). On the live system never trigger "Alarm an alle", Entwarnung, Demo-Alarm or `keys_test` except the one joint demo alarm in Task 28. Helpers never touch the VM, never push, never send anything outward; the controller does that.
- **Untouched:** `boss925_alarm_bark/`, `platformio.ini`, `nas_bridge/`, `cloud_watcher/` (wording of the outside watcher only in Task 30). The frontend `nas_dashboard/assets/` changes only where a task names the file.
- **Addresses** (spec §4.1): `/`, `/index.php`, `/dashboard_api.php`, `/api/{status,poll,ack,keys,mute,alive}.php`, `/manifest.json`, `/assets/…`; `/classic.php` → 302 `index.php`; everything else 404, also `/api/health.php`. Every address decides about the method itself (a GET on `api/poll.php` is 403 `FORBIDDEN`, never 405). The server never redirects a non-canonical path.
- **Box protocol:** plain-text lines, nonce mirrored, token only from the POST body, wrong token = status 403 with body exactly `FORBIDDEN` and for nothing else, commands at most once, numbers monotone. Answers to the box never wait for Bark and never for the disk beyond the deadline.
- **Deadlines for durable changes, per request, queue included** (spec §6.2): box (poll, ack, `api/keys.php`) 1.5 s, shortcut link 2 s, dashboard 5 s. Not begun at the deadline = dropped; finished late = it counts.
- **No Bark request inside a change** (`Store.Update` callbacks are pure: no network, no clock reads other than the `now` passed in, no file access).
- **Reads never touch the disk:** no request handler opens a file — state, configuration, history tail and assets all come from memory.
- **Strict inside:** typed documents, unknown fields and wrong types are errors, every file carries `schema`. Only `history.jsonl`, the rescue copy and the import read leniently.
- **Texts:** every message, Bark text (ASCII: ae/oe/ue/ss) and page is copied verbatim from the PHP source. Exceptions are exactly the texts this plan spells out (spec §7): texts naming PHP, Nginx or `config.php` say `config.toml` / `systemctl status fw-alarm`; texts describing behavior that §7 changes.
- **Event names and fields of the history stay** (`nas_alarm`, `nas_web_failed` …), also where "nas" is historical.
- **Secrets** (machine and watcher token, passwords, session ids, CSRF tokens, mute tokens) are compared in constant time, come from `crypto/rand`, and never appear in a log line, an error text, a redirect or any URL the server builds. Bark keys only masked. Every request has a size limit (64 kB body) and time limits. No file path is built from request input. HTML only through `html/template`. No `unsafe-inline`.
- **Dependencies:** standard library and the four modules above, vendored (`go mod vendor`, builds use `-mod=vendor`). No CGO in the product. Development tools (`staticcheck`, `govulncheck`) are installed with `go install`, never module dependencies. No test hooks in the program: what is tested from outside is what ships.
- **Code style:** identifiers in English, comments in German with real umlauts, UTF-8. `gofmt` clean. No error dropped without a comment saying why. Everything the program starts on the side (per-recipient send, check run, writers, work after the answer) recovers from a panic, logs it without request data and ends for itself; the process exits only in `main`.
- **Checks in every Go task:** `bash tools/check-go.sh` (gofmt, `go vet`, `staticcheck`, `govulncheck`, `go test ./...`, with `-race` when the PC has a C compiler — see Task 4 Step 0) and the black-box files of the table "Which test file turns green when". Everything that parses outside input gets a Go fuzz test; its seed corpus runs in every `go test`, `go test -fuzz` runs 30 s once in the task that creates it.
- **Tests are the contract.** A shared test file under `tests/backend/` or `tests/dashboard/` is not edited to make Go pass, except where a task names the edit. **Divergence protocol:** when a shared case is red on Go because spec §6/§7 changes the rule, do not bend it — move the case unchanged to a `php-*.test.mjs` file, write its Go twin into a `go-*.test.mjs` file, and add a row to "Rule → test" below. A divergence the spec does not name is a finding: stop and report.
- **Old and new firmware:** every server rule is tested with `box().status()` and `box().oldStatus()`.
- **The repo is public:** no real host names or addresses in tracked files. Copies of live data or configuration live only in a folder with mode 0700 outside the repo and outside any cloud folder, and are deleted after use.
- **On the VM** nothing root executes and nothing secret passes through `/tmp`; use a folder in the home directory (0700). Oracle stays Always Free.
- **Line endings:** `*.go`, `go.mod`, `go.sum`, `tools/*.sh`, `.github/**`, everything under `server/` are LF (`.gitattributes`); `vendor/**` is `-text`.
- **Git:** commits directly on `main`, one per task (plus fix commits), German messages with umlauts, ending with the attribution lines of the session reminder. **Push only before a deploy** (first push: Task 27), `git push origin main` by name only, after a scan for real addresses.
- **Docs stay current:** a task that makes a sentence in `CLAUDE.md`, a README or the spec untrue corrects it in the same commit. Unused files go.
- **Spacing:** the switch (Task 28) not within 3 days of a flash of the box; cleanup (Task 30) after 3 quiet days; passkeys only after Task 30.

## Review Focus

Failure modes the spec implies but states no test for, most likely first. Each is pinned to the task that owns the code.

1. **Bytes that are not valid UTF-8 arrive in a field the server stores** (a status field of the box, a recipient name, a user agent). Go writes them as U+FFFD, the read-back before the rename then differs from memory, and every later save would be refused — no change could be stored again. Expected: input is made valid UTF-8 where it enters (`web.ParseForm`), and saving keeps working. Tests: Task 9 `TestFormMakesValidUTF8`, Task 7 `TestSaveSurvivesOddStrings`.
2. **The disk is full or a write is torn:** a half-written `history.jsonl` line, a truncated `state.json.tmp`. Expected: the good `state.json` stays, the next history append starts on a fresh line, readers skip the torn line, the admin gets "Dashboard DEFEKT". Tests: Task 6 `TestSaveKeepsGoodFileOnShortWrite`, Task 7 `TestAppendAfterTornLine`.
3. **The state folder cannot be written at start** (rights lost after an update — the incident of 10/2026 — or a read-only file system): not even `server.lock` can be created. Expected: the server still starts, serves alarm, box and login from what it could read, and reports the fault; it refuses to start only when ANOTHER process holds the lock. Test: Task 6 `TestOpenOnUnwritableDir`.
4. **The wall clock jumps** (NTP correction, VM paused and resumed): backwards by hours or forwards by a day. Expected: the schedule follows elapsed time, not wall time; a marker that lies in the future counts as "now" instead of muting a reminder for hours; no burst of catch-up runs. Tests: Task 17 `TestClockJumps`, Task 9 `TestSessionClockBackwards`.
5. **Odd strings in the old `config.php`** (quotes, backslashes, umlauts, a line break in `bark_alarm_body`) travel through the import into `config.toml`. Expected: the written file loads again to exactly the same values, or the import stops naming the key — never a silently changed alarm text. Test: Task 20 `TestConfigRoundTripOddStrings`.

---

## File Structure

| Path | Responsibility |
|---|---|
| `go.mod`, `go.sum`, `vendor/` | module `fwalarm`, pinned Go version, vendored modules |
| `cmd/fw-alarm/main.go` | subcommand dispatch and flags only; the one place that calls `os.Exit` |
| `internal/clock/` | `Clock` (real and fake): now, timers, sleep |
| `internal/bg/` | `Group`: work beside requests — panic recovery, waited for at shutdown |
| `internal/textde/` | `Stamp` (`[16.11.2026 10:05] `), `DurationLabel`, `OutageDuration` |
| `internal/config/` | `Config`, TOML loading and checking, `Provider` (background reload, last good copy), `Write` |
| `internal/model/` | the documents (`State`, `Runtime`, `Sessions`, `Rescue`, `Entry`) and the pure rules: effective list, mute rules, versions and ids, rewind, demo protection, command life cycle, evaluation of a status push |
| `internal/store/` | file layer `FS`, save with read-back, `server.lock`, start table, `Update` with deadlines, volatile holders, history with outbox, rescue copy, self-test |
| `internal/bark/` | sending to many keys in rounds, ASCII, field sets, masking |
| `internal/notify/` | kinds and settings of admin messages, quiet hours, sending |
| `internal/logview/` | table of history events, filters, output per role |
| `internal/core/` | `Core`: what every handler needs (config, store, Bark, clock, background group, logger) |
| `internal/web/` | route table, forms, security headers, proxy trust, brakes, sessions, login, pages, static files |
| `internal/box/` | `api/status.php`, `poll.php`, `ack.php`, `keys.php` |
| `internal/mutelink/` | `api/mute.php` |
| `internal/dashapi/` | `dashboard_api.php`: action table, all actions |
| `internal/alive/` | `api/alive.php` |
| `internal/watch/` | full run, minute tick, schedule, texts, weekly report, messages about the server's own faults, systemd watchdog |
| `internal/control/` | control socket: server and client |
| `internal/app/` | wiring and life cycle of `serve`; the other subcommands |
| `internal/phpimport/` | one-time import of the PHP data (removed in Task 30) |
| `nas_dashboard/webassets.go` | `package webassets`: embeds `assets/` and `manifest.json` |
| `tools/check-go.sh`, `tools/test-go.sh` | the Go checks; the black-box suite against Go |
| `tests/backend/harness.mjs` | + intent helpers, + Go driver |
| `tests/backend/php-*.test.mjs` | rules of PHP's storage only; run on PHP only; go with PHP |
| `tests/backend/go-*.test.mjs` | what only the Go server can be asked; run on Go only |
| `tests/backend/parity.mjs`, `tests/backend/fixtures/php-data/` | PHP and Go side by side on the same imported data (removed in Task 30) |
| `.github/workflows/ci.yml` | checks on every push and weekly |
| `server/go/` | unit, ballast unit, config template, `setup.sh`, `deploy.sh`, `backup.sh`, `check.sh`, `cutover.sh`, `rollback.sh` for the Go server; moves up to `server/` in Task 30 |

Import direction (no cycles; `go build ./...` proves it): `clock`, `bg`, `textde`, `model` import nothing of ours → `config` → `store` (model, clock) → `bark` (config, textde) → `core` (config, store, bark, clock, bg) → `notify`, `logview` → `web` → `box`, `mutelink`, `dashapi`, `alive`, `watch`, `control` → `app` → `cmd/fw-alarm`. `phpimport` imports `model`, `store`, `config` only.

## Shared vocabulary

Names every task uses. A task's Interfaces block adds to these, never renames them.

```go
// internal/clock
type Clock interface {
    Now() time.Time
    After(d time.Duration) <-chan time.Time
    Sleep(d time.Duration)
}
func Real() Clock
type Fake struct{ /* … */ }                  // nur für Tests
func NewFake(start time.Time) *Fake
func (f *Fake) Advance(d time.Duration)      // löst fällige Zeitgeber aus
func (f *Fake) Set(t time.Time)              // Sprung der Wanduhr, Zeitgeber bleiben

// internal/bg
func New(log *slog.Logger) *Group
func (g *Group) Go(name string, fn func())           // fängt Laufzeitfehler ab, Journal ohne Anfragedaten
func (g *Group) Wait(timeout time.Duration) bool     // true = alles fertig

// internal/core
type Core struct {
    Cfg     func() *config.Config    // aktueller Stand aus dem Speicher
    CfgOK   func() bool              // false: config.toml fehlerhaft, die letzte gute gilt
    Store   *store.Store
    Bark    *bark.Client
    Clock   clock.Clock
    Bg      *bg.Group
    Log     *slog.Logger
    Version string
}

// internal/store
var ErrEmergency, ErrStorage, ErrLate, ErrDropped, ErrInvalid error
func (s *Store) State() *model.State                     // Schnappschuss, nie nil
func (s *Store) Emergency() bool
func (s *Store) Update(ctx context.Context, fn func(d *model.State) error) error
func (s *Store) Runtime() *model.Runtime
func (s *Store) UpdateRuntime(fn func(d *model.Runtime))
func (s *Store) Sessions() *model.Sessions
func (s *Store) UpdateSessions(fn func(d *model.Sessions))
func (s *Store) Log(e model.Entry)                                   // Eintrag ohne eigene Änderung
func (s *Store) LogAndWait(e model.Entry, max time.Duration) bool    // true = steht auf der Platte
func (s *Store) History(limit int, keep func(model.Entry) bool) []model.Entry   // neueste zuerst

// internal/web
type BudgetKind int                                   // BudgetBox 1,5 s | BudgetLink 2 s | BudgetUI 5 s
func Budget(k BudgetKind) (context.Context, context.CancelFunc)   // unabhängig vom Abbruch des Aufrufers
```

Inside an `Update` callback a history entry is added with `d.Log(entry)` (on `*model.State` and `*model.Runtime` alike); it travels in that document's `outbox`.

## File formats (state folder)

Field names are the PHP names wherever a field carries over — the import and the test bench rely on it. Times are Unix seconds.

`state.json` (durable):

```json
{
  "schema": 1,
  "keys_version": 12,
  "next_id": 14,
  "recipients": [
    { "id": 3, "key": "…", "label": "Anna", "phone": "", "added_at": 0,
      "muted": false, "muted_at": 0, "mute_volume": 0, "mute_permanent": false, "mute_token": "…" }
  ],
  "demo": { "enabled": false, "key_id": 0, "key": "", "label": "", "changed_at": 0, "by": "" },
  "settings": { "mute_max_seconds": 0 },
  "notify": { "global": {}, "types": {} },
  "commands": { "next_id": 1, "current": null, "recent_types": [ { "id": 7, "type": "TEST" } ] },
  "outbox": []
}
```

`keys_version` and `next_id` are their own high-water marks: inside one document they never go down, and when the document itself goes back the rewind rule applies (Task 5). `settings.mute_max_seconds` 0 = the value of the configuration. `notify` holds only what the admin set explicitly, each value as the choice key in text form. `commands.current` carries the fields PHP's `command_state.json` has (`id`, `type`, `state`, `created_at`, `created_by`, `delivered_at`, `acked_at`, `result`, `message`, `demo_changed_at` …); `recent_types` at most 20, ordered by id.

`runtime.json` (volatile):

```json
{
  "schema": 1,
  "lifebeat_at": 0,
  "box": null,
  "alarms_seen": {},
  "devices": {},
  "watcher": null,
  "watch": {},
  "throttle": { "login": {}, "mute": {} },
  "outbox": []
}
```

`box` = `{ "seen_at", "via_bridge", "remote_addr", "fields": [["device_id","fw-box-1"], …] }` — the fields as an ordered list of pairs (first 40, as pushed). `alarms_seen[device_id]` and `devices[device_id]` as PHP's `alarm_seen.json` / `devices_seen.json`. `watcher` = `{ "seen_at", "bark_failed_at" }`. `watch` holds the markers of the checker under PHP's names from `watchdog_state.json` (`last_run_at`, `offline_notified`, `offline_since`, `offline_seen_at`, `offline_reminded_at`, `offline_push_pending`, `web_fail_count`, `web_fail_since`, `web_fail_notified_at`, `web_push_pending`, `watcher_watch_since`, `watcher_silent_seen_at`, `watcher_notified_at`, `watcher_push_pending`, `demo_reminded_at`, `demo_reminded_for`, `mute_reminded_at`, `permanent_mute_reminded_at`, `health_bad_runs`, `health_notified_at`, `weekly_report_at`, `last_send_ok_at`, `last_send_failed_at`) plus new ones: `last_run_line`, `retry_after` (map kind → time), `emergency_notified_at`, `config_broken_notified_at`. `throttle.<bucket>[who]` = `{ "count", "first_at" }`.

`sessions.json`: `{ "schema": 1, "sessions": { "<sha256 hex of the session id>": { "user", "role", "csrf", "auth_stamp", "created_at", "last_seen" } } }`.

`history.jsonl`: one JSON object per line, the fields PHP writes (`event`, `log_at`, …) plus `uid` (16 random bytes, hex). Rotation above 5 MB to `history.1.jsonl`.

`recipients.rescue.json`: `{ "written_at": 0, "demo_key_id": 0, "recipients": [ { "id", "key", "label", "muted", "muted_at", "mute_volume", "mute_permanent" } ] }`.

Temporary names are fixed: `state.json.tmp`, `runtime.json.tmp`, `sessions.json.tmp`, `recipients.rescue.json.tmp`, `selftest.tmp`. The test bench makes a save fail by putting a non-empty folder at such a name.

## Texts this plan fixes

Bark texts (ASCII). Each body is preceded by the time stamp `textde.Stamp` the way PHP's checker does it for its own messages.

| Occasion | Title | Body |
|---|---|---|
| self-test fails in 2 full runs | `Dashboard DEFEKT` | `Speichern scheitert (Schritt: <step>). Weiter laufen: Alarm an alle, Status der Box, Anmeldung. NICHT mehr: Aenderungen (Empfaenger, Stumm/Laut, Einstellungen) werden nicht gespeichert, Befehle an die Box nicht ausgeliefert. Nachsehen: journalctl -u fw-alarm, Platz und Rechte von /var/lib/fw-alarm.` |
| emergency mode | `Dashboard NOTBETRIEB` | `Empfaengerliste nicht lesbar. Ein Alarm ginge jetzt an <n> Empfaenger (<Not-Kopie des Servers\|Not-Liste der Konfiguration>). Die Box behaelt ihre gespeicherte Liste<, zuletzt war Demo-Modus: evtl. nur den Demo-Empfaenger>. Ausweg: Sicherung zurueckspielen (server/README.md).` |
| rewind | `Dashboard: Stand zurueckgesetzt` | `Gestartet aus <der Vorfassung (state.prev.json)\|einer zurueckgespielten Sicherung\|einer aelteren Fassung des Formats\|einer erneuten Uebernahme>. Die letzte Aenderung kann fehlen - bitte Empfaenger und Stumm/Laut pruefen.` |
| `runtime.json` unreadable | `Dashboard: Laufzeitdaten verworfen` | `runtime.json war nicht lesbar. Die Box gilt bis zu ihrem naechsten Status als nie gesehen.` |
| `sessions.json` unreadable | `Dashboard: Laufzeitdaten verworfen` | `sessions.json war nicht lesbar. Alle melden sich neu an.` |
| configuration broken | `Dashboard: Konfiguration fehlerhaft` | `config.toml ist fehlerhaft - es gilt die letzte gueltige Fassung, alle Sitzungen wurden beendet. Den Fehler nennt: sudo fw-alarm check-config` |
| interrupted send | `Dashboard: Versand unterbrochen` | `<Ein Alarm\|Eine Entwarnung> von <HH:MM> lief beim Neustart des Servers - Ergebnis unbekannt. Bitte den Verlauf pruefen und ggf. erneut senden. Der Server sendet nicht von selbst nach.` |
| crash (`report-failure`) | `Dashboard-Dienst abgestuerzt` | `systemd startet ihn neu. Nachsehen: journalctl -u fw-alarm -n 50` |

Other fixed texts: storage failure `Fehler: Datenordner nicht beschreibbar (Rechte des Web-Benutzers auf dem Server prüfen).` (as today); deadline passed `Fehler: Speichern dauert zu lange – bitte den angezeigten Stand prüfen.`; login queue full (status 503) `Gerade zu viele Anmeldungen gleichzeitig – bitte in ein paar Sekunden noch einmal versuchen.`; hint of the kind `nas_web` in the settings `(Änderungen werden dann nicht gespeichert, Befehle an die Box nicht ausgeliefert)`. History texts in `assets/app/logic.js` (`describeLogEntry`): `state_rewound` → `Server: Stand zurückgesetzt – die letzte Änderung kann fehlen`; `send_interrupted` → `Versand unterbrochen (Neustart des Servers) – Ergebnis unbekannt`.

## Which test file turns green when

`bash tools/test-go.sh <files>` must pass after the task named; a file green once stays green.

| After task | Files |
|---|---|
| 9 | `session`, `dashboard-access` |
| 10 | + `dashboard-log` |
| 11 | + `dashboard-notify` |
| 12 | + `dashboard-settings`, `dashboard-keys`; `box-keys` except cases that poll (name the still-red cases in the commit message) |
| 13 | + `box-status` |
| 14 | + `box-poll-ack`, `box-keys` complete |
| 15 | + `mute-link` |
| 16 | + `alarm-send` |
| 17 | + `alive`, `watchdog` |
| 18 | + every `go-*.test.mjs` |
| 22 | the seven browser scenarios, `tests/dashboard/*.test.mjs` |

## Rule → test (everything spec §7 changes or removes)

"PHP only" names where today's case goes in Tasks 1–2; "Go" names the test that holds the new rule. Tasks add rows here when the divergence protocol applies.

| Rule today (PHP) | PHP only | Go |
|---|---|---|
| guard files, `data_path`, `fix_owner` | — | Task 8 `TestOnlyTableAddressesAnswer` (state files are reachable by no address) |
| `.bak` fallback of the list | `php-storage`: the cases of "Kaputte Liste: Rückgriff auf die Sicherung", "nur die Liste kaputt: es gilt die Sicherung" | Task 6 `TestStartTable`; Task 18 `go-restart`: "kaputte Zustandsdatei: Rückfall mit Rücksprung, dann Notbetrieb" |
| version/ids never reused after a fallback | `php-storage`: "nach dem Rückgriff … NEUE Version und id" | Task 5 `TestRewind`; Task 18 `go-restart`: "nach dem Rückfall springen Version, ids und Befehlsnummer um 1000" |
| box-reported higher version counts at the next change | `php-storage`: "nach Verlust des Datenordners zählt die Version weiter …" | Task 13 `go-box`: "meldet die Box eine höhere Listen-Version, hebt der Server seine sofort darüber" |
| hand-spoiled list: valid, unique, first 10 are alarmed | `php-storage`: the three "von Hand verdorbene Liste"/"nur aus ungültigen Keys" cases, `box-keys` "Bereinigung" | Task 5 `TestInvalidListCannotBeSaved`; Task 6 `TestRescueReadIsLenient` |
| config list as the only net | `php-storage`: "Liste UND Sicherung unlesbar: ebenfalls die Not-Liste" | Task 16 `go-storage`: "Notbetrieb: Alarm geht an die Not-Kopie, ohne sie an die Not-Liste" |
| `demo_set` order, rollback, `inconsistent`, `repair_broken_demo_mode` | `php-storage`: demo file corrupt/removed (poll, checker) | Task 12 `TestDemoSetIsOneChange`; Task 20 `TestImportBrokenDemoBecomesLive` |
| lock files and their order | `php-storage`: per-lock cases that do not fit `storage.fail('durable')` | Task 7 `TestOneWriterAtATime` |
| status push: 500 when the folder is stuck | `php-storage`: `status.lock` / `latest_status.json` blocked | Task 13 `go-storage`: "Status wird angenommen, wenn nichts gesichert werden kann" |
| poll with a stuck folder: 500 or `type=NONE` | `php-storage` | Task 14 `TestPollDeliversOnlyWhenSaved`, `go-storage`: "Poll liefert ohne Speichern keinen Befehl aus" |
| one sentence for storage errors | — | Task 7 `TestUpdateDeadlines`; Task 12 `TestStorageSentences` |
| `api/health.php`, steps, `inplace_data_files`, `blocks_pushes` | `php-health` (the whole of today's `health.test.mjs`), DEFEKT text cases | Task 7 `TestSelfTest`; Task 17 `TestDefektText`, `go-watch`: "Dashboard DEFEKT ab dem zweiten vollen Lauf"; Task 8 `/api/health.php` → 404 |
| hint of the kind `nas_web` | `php-storage`: the `notify_list` hint | Task 11 `TestNasWebHint` |
| "Box OFFLINE" blames the server when `blocks_pushes` | `php-storage`: the checker cases that break the folder and expect that text | Task 17 `TestOfflineTextNeverBlamesServer`, `TestFirstOfflineWaitsForWatcher` |
| `watcher_unknown_at`, spare state in the temp folder | `php-storage`: `STATE_WRITE_FAILED` and `watcher_unknown_at` cases | accepted loss (spec §7); Task 17 `TestMarkersSurviveRestart` |
| checker as its own process every 15 min | — | Task 17 `TestScheduleFirstFullRun`, `TestMinuteTickOnlyBoxOffline`, `TestRunsNeverOverlap` |
| failed message retried by the next run | — | Task 17 `TestRetryNotBefore15Min` |
| silence counts from the last sign of life | — | Task 17 `TestAwayRule`, `go-watch`: "nach einer Auszeit des Servers keine Geister-Meldung" |
| `log_cache.json` | — | nothing (gone) |
| `defer_after_response` | — | Task 18 `TestShutdownWaitsForBackground` |
| PHP sessions | the idle case stays skipped on PHP | Task 9: `session` "länger als 12 Std ohne Anfrage" runs on Go |
| typo in `config.php` breaks every request | — | Task 8 `TestProviderKeepsLastGood`; Task 18 `go-config`: "Tippfehler: letzte gute gilt, Sitzungen enden, Start mit der Kopie" |
| trusted proxies by address only | — | Task 9 `TestUnixListenerCountsAsTunnel` |
| texts naming `config.php` | cases use `backend.words.configFile` | same cases |
| five new admin messages, two new history events | — | Task 17 `TestOwnFaultMessages`; Task 19 `tests/dashboard/logic-log.test.mjs` |

---

## Phase A — Tests off the files (PHP stays green)

Today about 125 places in the tests reach into PHP's data files (`backend.data.read/edit/write/corrupt/block`). After this phase a test says WHAT it wants and each driver knows WHERE that lies. The proof that the rewording changed nothing is that everything stays green on PHP and still catches three planted faults.

### Task 1: Intent helpers in the harness; box, alive and checker tests

**Files:**
- Modify: `tests/backend/harness.mjs`, `tests/backend/box-status.test.mjs`, `box-poll-ack.test.mjs`, `box-keys.test.mjs`, `alive.test.mjs`, `watchdog.test.mjs`
- Create: `tests/backend/php-storage.test.mjs`

**Interfaces:**
- Produces on `backend` (all `async`; implemented here for the PHP driver, in Task 8 for Go). Seconds are "that long ago"; a negative number lies in the future.

```js
backend.state = {
  // Zeit verschieben
  ageBoxStatus(seconds), ageDevice(deviceId, seconds),
  ageMute(idOrAll, seconds),            // 'all' = alle, die gerade stumm sind
  ageDemo(seconds),
  ageCommand(which, seconds),           // which: 'created' | 'delivered'
  ageWatcher(seconds),
  ageMark(name, seconds),               // Merker des Wächters, Namen wie in "File formats"
  ageMuteThrottle(seconds),
  ageLog(seconds, match),               // passende Verlaufs-Einträge älter machen (log_at)
  ageSession(cookie, seconds),          // PHP: wirft (Sitzungen liegen außerhalb des Prüfstands)
  // Herstellen
  setWatcher({ seenAgo, barkFailedAt = 0 }),
  setMark(name, value),
  setCommand(patch),                    // Felder des laufenden Befehls
  seedLog(entries, { rotated = false } = {}),
  seedRecipients(entries, { version, nextId }),   // nur gültige Einträge
  // Lesen: wie gespeichert, nicht wie angezeigt
  boxStatus(), recipients(), recipient(id), listVersion(), demo(), command(),
  watcher(), marks(), log(), muteFailures(), notifyStored(),
};
backend.storage = { fail(what), repair(what) };   // what: 'selftest' | 'durable' | 'history' | 'volatile'
backend.words = { configFile: 'config.php' };     // Go: 'config.toml'
```

- `boxStatus()` returns the pushed fields as an object plus `seen_at`, `via_bridge` (or `null`); `recipients()` returns `{ version, keys: [...] }`; `watcher()` `{ seen_at, bark_failed_at }` or `null`; `marks()` an object of the markers; `log()` the raw entries, oldest first.
- PHP mapping of `storage.fail` (starting point; narrow it where a shared case needs it, and write the final mapping into the comment above it): `selftest` → folder at `health_probe.json`; `history` → folder at `commands.log`; `durable` → copy the list to `alarm_keys.json.bak`, then folders at `alarm_keys.json`, `keys.lock`, `command.lock`, `notify.lock`, `settings.lock`; `volatile` → folders at `latest_status.json`, `watcher_seen.json`.
- `backend.data` stays, but only `php-*.test.mjs` files may use it.

**Classification of every place** (this is the task): (a) it shifts a time, seeds or reads stored state → the helper above; (b) it breaks storage and the case is green on PHP with `storage.fail(what)` → that; (c) it corrupts, removes or blocks a particular PHP file, reads a `.bak`, or asserts something spec §7 changes → the case moves UNCHANGED to `php-storage.test.mjs` (whole file `{ skip: backend.kind !== 'php' }`), and the report lists it with the §7 row it belongs to. Never weaken an assertion to fit a helper; a case that fits none goes to (c).

- [ ] **Step 1:** Record the baseline: `node --test "tests/backend/*.test.mjs"` → 366 pass, 2 skipped. Save the list of case names (`--test-reporter=tap`) to the scratch folder.
- [ ] **Step 2:** Implement `backend.state`, `backend.storage`, `backend.words` for the PHP driver in `harness.mjs`.
- [ ] **Step 3:** Rework the five files. Local helpers of the files (`ageStatus`, `editState`, `watcherSeen`, `ageMutes`, `storedKey`, `breakDataDir`, `stored`, `editCommand`, `seen`) become thin calls of the new helpers or disappear. The case in `watchdog.test.mjs` that sets `watcher_unknown_at` and the ones expecting `STATE_WRITE_FAILED` go to `php-storage.test.mjs`.
- [ ] **Step 4:** `node --test "tests/backend/*.test.mjs"` → every case name of the baseline is still there (in its file or in `php-storage`), same pass/skip counts. `grep -n "backend\.data\." tests/backend/{box-status,box-poll-ack,box-keys,alive,watchdog}.test.mjs` prints nothing.
- [ ] **Step 5:** Commit — `Tests: Box-, Lebenszeichen- und Wächter-Tests sagen, was sie wollen, statt Dateien anzufassen`.

### Task 2: The remaining server tests off the files; deferred items; proof

**Files:**
- Modify: `tests/backend/alarm-send.test.mjs`, `mute-link.test.mjs`, `dashboard-access.test.mjs`, `dashboard-keys.test.mjs`, `dashboard-log.test.mjs`, `dashboard-notify.test.mjs`, `session.test.mjs`, `php-storage.test.mjs`, `tests/backend/README.md`
- Rename: `tests/backend/health.test.mjs` → `php-health.test.mjs` (whole file PHP only)

**Interfaces:** Consumes Task 1's helpers; adds none.

- [ ] **Step 1:** Rework the files by the classification of Task 1. Cases asserting a text that names the configuration file build the expectation from `backend.words.configFile`.
- [ ] **Step 2: Deferred items of the earlier reviews** (each is a case or a fix in the named file):
  - `dashboard-notify.test.mjs` `loginTexts()`: `waitFor` the expected number of messages, then `settle()` for "no more" (a server may answer before it sends).
  - `session.test.mjs`: the idle case calls `backend.state.ageSession(cookie, 13 * 3600)` instead of reading a sessions folder; `sessionFiles()` goes; still skipped on PHP.
  - `session.test.mjs`: `/classic.php` answers 302 with `Location: index.php` (or an absolute address ending in `/index.php`) and no cookie.
  - `session.test.mjs`: with `trusted_proxies` set to an address that does NOT match, `X-Forwarded-For` is ignored (the brake counts the real sender).
  - `dashboard-log.test.mjs`: seed a `cancelled` entry without reason and a `relay_alarm` with explicit `source`; assert their output.
- [ ] **Step 3: Proof that the tests still bite.** Copy `nas_dashboard/` to a temp folder and plant one fault at a time; run with `FW_BACKEND_SRC=<copy>`; each must turn at least one reworked case red: (1) `mute_is_stale()` returns false always; (2) `api/status.php` never writes `relay_alarm`; (3) `api/poll.php` does not store `delivered` (the command is handed out twice). Put the three red case names into the report.
- [ ] **Step 4:** `node --test "tests/backend/*.test.mjs"` → all pass, the two known skips. `grep -ln "backend\.data\." tests/backend/*.test.mjs` prints only `php-storage.test.mjs` and `php-health.test.mjs`.
- [ ] **Step 5:** `tests/backend/README.md`: replace the rules about `backend.data` by the helpers, name the `php-*`/`go-*` convention and the divergence protocol. Commit — `Tests: Server-Tests kennen keine Dateien mehr – was nur PHPs Ablage prüft, steht in php-*.test.mjs`.

### Task 3: Browser scenarios off the files

**Files:**
- Modify: `tests/dashboard/e2e/sandbox.mjs`, `scenarios/overview.mjs`, `recipients.mjs`, `settings.mjs`, `log.mjs`, `tests/dashboard/e2e/README.md`

**Interfaces:** Consumes `backend.state`. `sandbox.mjs` loses `readData`/`editData`; `setStatusAge(backend, seconds)` calls `backend.state.ageBoxStatus`.

- [ ] **Step 1:** Replace every `readData`/`editData`/`backend.data` use: stored recipients → `backend.state.recipients()`; `last_run_at = 0` → `backend.state.setMark('last_run_at', 0)`; mute age → `ageMute`; seeded history → `seedLog`. Remove what only served a shared, hand-started sandbox, because every run starts empty: the restore logic for "existing recipients" in `overview.mjs`, `recipients.mjs`, `settings.mjs`, and `backupFile`/`restoreFile` in `settings.mjs` (raw save and restore of data files) — where a scenario needs a value set back between its own steps, it uses the dashboard action for it.
- [ ] **Step 2:** `node tests/dashboard/e2e/run.mjs <name>` for `smoke shell overview recipients log settings switch` → `OK` seven times. `grep -rn "\.data\.\|readData\|editData" tests/dashboard/e2e` prints nothing.
- [ ] **Step 3:** Commit — `Tests: Chrome-Szenarien nutzen dieselben Helfer wie die Server-Tests`.

---

## Phase B — The Go server (the live system notices nothing)

Every task here: write the named tests first and see them fail; implement; `bash tools/check-go.sh`; the black-box files of its row; commit. A task is not done while a file of an earlier row is red. Where a task says "as PHP `<function>`", that function in `nas_dashboard/` is the source for the rule and every text.

### Task 4: Module, tools, configuration

**Files:**
- Create: `go.mod`, `go.sum`, `vendor/`, `cmd/fw-alarm/main.go`, `internal/clock/clock.go` (+ test), `internal/bg/bg.go` (+ test), `internal/textde/textde.go` (+ test), `internal/config/{config.go,load.go,write.go}` (+ tests), `server/go/config.example.toml`, `tools/check-go.sh`, `tools/test-go.sh`, `nas_dashboard/webassets.go`
- Modify: `.gitattributes`, `.gitignore` (`/bin/`), `server/deploy.sh` (add `webassets.go` to the skip list and the rsync excludes, so the Go file never lands in the PHP webroot), `tests/server/` case for that, `CLAUDE.md` (new section "Go-Fassung (im Bau)": layout, commands, that PHP is still what runs)

**Interfaces:**
- Produces: `clock`, `bg`, `textde` as in "Shared vocabulary" (`textde.Stamp(t time.Time, loc *time.Location) string`, `DurationLabel(seconds int) string` as PHP `notify_duration_label`, `OutageDuration(minutes int) string` as PHP `outage_duration_text`).
- Produces `config`:

```go
type Config struct { /* ein Feld je Schlüssel, Namen s.u. */ }
func Load(path string) (*Config, error)        // unbekannter Schlüssel = Fehler mit seinem Namen
func Parse(data []byte) (*Config, error)
func (c *Config) Problems() []string           // ohne Geheimnisse; leer = in Ordnung
func (c *Config) Location() *time.Location
func (c *Config) AuthStamp(role string) string // HMAC über Rolle+Benutzer mit dem Passwort-Hash (PHP: dashboard_auth_stamp)
func Write(c *Config) []byte                   // TOML mit Kommentaren; Parse(Write(c)) ergibt c
func HashPassword(pw string) (string, error)   // Argon2id: 19 MiB, 2 Durchläufe, 1 Faden
func VerifyPassword(hash, pw string) bool      // $argon2id$ und bcrypt ($2y$, $2a$, $2b$)
```

- Keys = the names in `nas_dashboard/config.example.php` minus `data_dir` and `health_check_url`, plus `state_dir` (default `/var/lib/fw-alarm`), `listen` (default `127.0.0.1:8080`; or `unix:<path>`), `control_socket` (default `/run/fw-alarm/control.sock`), `check_interval_seconds` (default 900; 0 = no automatic runs, a problem for `check-config`).
- **A missing key gets the value the PHP CODE uses for it** (the `?? …` in the source), not the value of the example file: `offline_after_seconds` 180, `watchdog_offline_after_seconds` 600, `demo_reminder_after_seconds` 14400, `mute_max_seconds` 43200, `delivered_command_lock_seconds` 120, `pending_command_ttl_seconds` 300, `login_max_failures` 8 (never below 1), `login_lockout_window_seconds` 900 (never below 60), `bark_host` `https://api.day.app`, `bark_alarm_title` `FEUERWEHR-ALARM`, `bark_alarm_body` `Manuelle Alarmierung ueber das Dashboard!`, `bark_alarm_sound` `alarm_fw`, `bark_alarm_volume` 10, `bark_alarm_call` true, `bark_max_tries` 3 (never below 1), `bark_false_alarm_sound` `glass`, `bark_false_alarm_volume` = the alarm volume, `timezone` `Europe/Berlin`. Before relying on this list, search every `$config['…'] ??` under `nas_dashboard/` and add what is missing.
- Produces — command line: `fw-alarm version` (string from `-ldflags "-X main.version=<sha>"`, default `dev`); `fw-alarm check-config [--config <file>]` → exit 0 and `Konfiguration in Ordnung.`, or exit 1 and one line per problem; `fw-alarm hash-password` (reads without echo through `x/term`, twice, prints the hash). `--config` defaults to `/etc/fw-alarm/config.toml` for every subcommand.
- Produces: `tools/check-go.sh` (prints one line per check, exit 1 on the first failure: `gofmt -l` prints nothing; `go vet ./...`; `staticcheck ./...`; `govulncheck ./...`; `go test ./...` — with `-race` when `go env CGO_ENABLED` can be 1 and `gcc` is on the path); `tools/test-go.sh [files…]` (builds `bin/fw-alarm` with `-mod=vendor`, then `FW_BACKEND=go FW_GO_BIN=<abs> node --test <files or "tests/backend/*.test.mjs">`).

- [ ] **Step 0 (controller, not a helper):** `go install honnef.co/go/tools/cmd/staticcheck@latest` and `golang.org/x/vuln/cmd/govulncheck@latest`. Race detector on the PC: look for a C compiler (`gcc`, MSYS2 `pacman`); if none, install a portable mingw-w64 toolchain into `%LOCALAPPDATA%\Programs` (zip, checksum verified, no installer, no pop-up) and check `go test -race` on a two-line data race. If it cannot be made to work: write that into the ledger — the race detector then runs only in CI (Task 23) at the first push.
- [ ] **Step 1: Tests.** `TestDefaultsMatchPHPCode` (the list above); `TestExampleFileLoads` (`server/go/config.example.toml` with the placeholders filled loads without a problem and sets every key); `TestUnknownKeyIsError`; `TestProblems` (relative `state_dir`, unknown time zone, hash that is neither bcrypt nor Argon2id, `watcher_token_sha256` equal to `machine_token_sha256`, `check_interval_seconds` 0 → one line each, and no line contains a hash, token or key); `TestHashNormalised` (upper case and spaces in the token hashes accepted); `TestWriteRoundTrip`; `TestVerifyPassword` (the bcrypt hash of `test` from `harness.mjs`; a fresh Argon2id hash; a wrong password); `FuzzParse`; `TestStamp` (`2026-07-01T10:00:00Z` → `[01.07.2026 12:00] `, `2026-12-01T10:00:00Z` → `[01.12.2026 11:00] `); `TestOutageDuration` (119 → `119 min`, 120 → `2 Std`); `TestFakeClock`; `TestGroupRecoversPanic`, `TestGroupWait`.
- [ ] **Step 2:** Implement. `go mod init fwalarm`, the four modules, `go mod vendor`. `time/tzdata` is imported in `cmd/fw-alarm`. The example file carries every key with its explanation as a comment (taken from `config.example.php`, shortened where it described Synology) and keeps the banner `>>> HIER STEHEN ALLE WERTE … <<<`.
- [ ] **Step 3:** `bash tools/check-go.sh` → ok. `go run ./cmd/fw-alarm check-config --config server/go/config.example.toml` → exit 1 naming the placeholders. `node --test "tests/backend/*.test.mjs" "tests/server/*.test.mjs"` (PHP) → still all pass. Commit — `Go: Modul, Prüfwerkzeuge und die Konfiguration als config.toml`.

### Task 5: `model` — the documents and the rules of the recipient list

**Files:** Create `internal/model/{state.go,runtime.go,sessions.go,rescue.go,entry.go,clone.go,validate.go,recipients.go,mute.go,demo.go,rewind.go}` + tests.

**Interfaces — produces:**

```go
const MaxRecipients = 10
const Schema = 1
type State struct { /* wie "File formats" */ }
type Recipient struct{ ID int; Key, Label, Phone string; AddedAt int64; Muted bool; MutedAt int64; MuteVolume int; MutePermanent bool; MuteToken string }
type Demo struct{ Enabled bool; KeyID int; Key, Label string; ChangedAt int64; By string }
type Command struct{ /* Felder wie PHP */ }
type Commands struct{ NextID int; Current *Command; RecentTypes []RecentType }
type Runtime struct { /* wie "File formats" */ }
type BoxStatus struct{ SeenAt int64; ViaBridge, RemoteAddr string; Fields Fields }
type Fields []Field                      // geordnete Paare; JSON: Liste von Paaren
type Field struct{ Name, Value string }
func (f Fields) Get(name string) string
type Sessions struct{ Schema int; Sessions map[string]Session }
type Rescue struct{ WrittenAt int64; DemoKeyID int; Recipients []RescueRecipient }
type Entry map[string]any                // eine Verlaufszeile
func NewEntry(event string, at time.Time, fields map[string]any) Entry   // setzt event, log_at, uid
func (e Entry) Str(k string) string; func (e Entry) Int(k string) int64; func (e Entry) Bool(k string) bool   // nachsichtig
const EvNasAlarm = "nas_alarm" /* … eine Konstante je Ereignis aus PHP log_event_types() */
const EvStateRewound, EvSendInterrupted = "state_rewound", "send_interrupted"
var AllEvents []string

func (s *State) Clone() *State; func (r *Runtime) Clone() *Runtime; func (s *Sessions) Clone() *Sessions
func (s *State) Validate() error         // Invarianten: s.u.
func (s *State) Log(e Entry); func (r *Runtime) Log(e Entry)   // an die outbox, höchstens 500 (älteste fallen weg)
func DecodeState(data []byte) (*State, error)      // streng: unbekannte Felder, falsche Typen, falsches Schema = Fehler
func DecodeRuntime(data []byte) (*Runtime, error); func DecodeSessions(data []byte) (*Sessions, error)
func DecodeRescueLenient(data []byte) Rescue        // gültige, einmalige Keys, höchstens 10

func ValidKey(k string) bool                        // PHP: valid_bark_key
func ValidLabel(s string) bool; func NormalizePhone(s string) (string, bool)   // PHP: normalize_phone
func NewMuteToken() string; func ValidMuteToken(t string) bool
type Target struct{ ID int; Key, Label string; Muted bool; MuteVolume int }
func (s *State) Effective(now time.Time, muteMax int) []Target      // PHP: effective_alarm_entries – im Demo-Modus nur der Demo-Empfänger; überfällig Stumme zählen als laut
func MuteIsStale(r Recipient, now time.Time, muteMax int) bool      // PHP: mute_is_stale (dauerhaft = nie)
func (s *State) MuteMax(cfgSeconds int) int                         // PHP: mute_max_seconds
func ApplyMute(r *Recipient, muted bool, now time.Time) (permanentEnded bool)   // PHP: apply_mute_change
func (s *State) ExpireStaleMutes(now time.Time, muteMax int) []Recipient        // schaltet zurück, bumpt, protokolliert; liefert die Betroffenen
func (s *State) Bump(boxVersion int) int            // keys_version = max(eigene, boxVersion) + 1
func (s *State) AllocateID() int
func (s *State) Find(id int) *Recipient
func (s *State) DemoBlockReason(box *BoxStatus, now time.Time, offlineAfter int) string   // PHP: demo_alarm_block_reason
type RewindSource string                            // "prev" | "restore" | "schema" | "import"
func (s *State) Rewind(src RewindSource, now time.Time) // +1000 auf keys_version, next_id, commands.next_id; offener Befehl → cancelled; Eintrag state_rewound
```

`Validate` holds: at most 10 recipients; every key valid and unique; ids unique, > 0 and < `next_id`; every `mute_token` valid and unique; `mute_permanent` only with `muted`; `mute_volume` 0–10; demo enabled only with a `key_id` that is in the list and `key` equal to that recipient's key; `commands.current.id` < `commands.next_id`; `recent_types` at most 20.

- [ ] **Step 1: Tests.** `TestCloneSharesNothing` (walk original and clone by reflection: no slice, map or pointer reachable from one is reachable from the other — fails when a field is added without its copy); `TestDraftDoesNotTouchSnapshot` (change every part of a clone, the original marshals to the same bytes as before); `TestStrictDecode` (unknown field, string where a number belongs, schema 2 → error naming the field); `TestInvalidListCannotBeSaved` (table over every `Validate` invariant); `TestEffective` (demo → one target; overdue mute → loud without changing the state; permanent mute never overdue; mute volume kept); `TestExpireStaleMutes` (flag and time cleared, version +1 once, one `mute_expired`-style entry per recipient as PHP `expire_stale_mutes` writes it; nothing stale → nothing changes, no bump); `TestApplyMute` (loud ends permanent and reports it); `TestBump` (table: own 5, box 0 → 6; own 5, box 41 → 42; own 41, box 5 → 42); `TestAllocateID`; `TestRewind` (+1000 three times; `pending` and `delivered` command → `cancelled`; `acked` untouched; entry `state_rewound` with `source`); `TestRescueLenient` (non-objects, short and duplicate keys, 12 entries → the first 10 valid unique ones; garbage → empty); `TestFieldsKeepOrder` (JSON round trip keeps the order of 40 pairs); `TestOutboxCap` (501st entry drops the oldest); `FuzzDecodeState` (decode → encode → decode is stable).
- [ ] **Step 2:** Implement. One constant per event of PHP `log_event_types()`; entries are built only through `NewEntry`.
- [ ] **Step 3:** `bash tools/check-go.sh`. Commit — `Go: Datentypen des Zustands und die Regeln der Empfängerliste als reine Funktionen`.

### Task 6: `store` I — files, saving, the start table

**Files:** Create `internal/store/{fs.go,fs_unix.go,fs_windows.go,save.go,lock_unix.go,lock_windows.go,open.go,rescue.go,migrate.go}` + tests, `internal/store/storetest/faultfs.go` (a fake `FS` for tests of this and later tasks).

**Interfaces — produces:**

```go
type FS interface {
    ReadFile(name string) ([]byte, error)
    WriteFileSync(name string, data []byte) error   // anlegen/kürzen, schreiben, fsync, schließen (0600)
    AppendSync(name string, data []byte) error      // öffnen, anhängen, fsync, schließen
    Rename(from, to string) error                   // Windows: bis zu 1 s wiederholen, wenn ein Scanner die Datei hält
    Link(from, to string) error
    Remove(name string) error
    SyncDir(dir string) error                       // Windows: nichts
    Stat(name string) (fs.FileInfo, error)
    MkdirAll(dir string) error                      // 0700
}
func OSFS() FS
// storetest
type FaultFS struct{ /* … */ }                       // im Speicher; je Datei: Fail(name, err), Hang(name) → release(), ShortWrite(name, n)
type StartMode int                                   // StartNormal | StartFresh | StartFallback | StartEmergency
type StartInfo struct {
    Mode StartMode
    Rewound bool; RewindSource model.RewindSource
    RuntimeDiscarded, SessionsDiscarded bool
    LockUnavailable bool                             // Sperre ließ sich nicht anlegen (Ordner nicht beschreibbar)
    LastLifebeat time.Time                           // null = unbekannt
}
var ErrLocked = errors.New("ein anderes fw-alarm arbeitet schon in diesem Datenordner")
func Open(dir string, fsys FS, clk clock.Clock, log *slog.Logger) (*Store, StartInfo, error)   // Fehler nur ErrLocked
func (s *Store) Close(ctx context.Context) error
func (s *Store) Rescue() (targets []model.Target, source string)   // source: "rescue" | "" (nicht lesbar)
func CheckState(dir string, fsys FS) (*model.State, error)          // verlangt eine lesbare state.json; kein Rückfall
```

**Saving a document** (`state.json`, `runtime.json`, `sessions.json`, the rescue copy): marshal → `WriteFileSync(<file>.tmp)` → read the temp file back with the strict decoder and compare with what was meant (re-marshalled bytes equal) → for `state.json` only: `Remove(state.prev.json)`, `Link(state.json, state.prev.json)` (copy when linking fails) → `Rename(<file>.tmp, <file>)` → `SyncDir`. Any failing step = the save failed; `state.json` is never missing in between. A leftover `*.tmp` at start is removed if it is a file and ignored if it is not.

**Start table** (spec §6.3): `state.json` readable → normal. Both missing → fresh, empty state. `state.json` missing or unreadable and `state.prev.json` readable → the unreadable file is moved to `state.corrupt-<unix time>.json`, start on the previous one, `Rewind("prev")`, save. `state.json` unreadable (or its schema newer than `model.Schema`) and no readable previous → **emergency**: nothing durable is ever written, the broken files stay as they are. An older schema is lifted by the functions in `migrate.go` (`var migrations = map[int]func([]byte) ([]byte, error){}` — empty today) after the original was kept once as `state.schema-<n>.json`. `runtime.json` / `sessions.json` unreadable → moved aside as `<name>.corrupt-<time>.json`, start empty, flag in `StartInfo`. The lock: `server.lock`, exclusive (`flock` / `LockFileEx`), held until `Close`; held by another process → `ErrLocked`; cannot be created at all → `LockUnavailable`, start anyway (Review Focus 3).

- [ ] **Step 1: Tests** (with `FaultFS` unless a real folder is named): `TestSaveReadBack` (a decoder mismatch planted through `FaultFS` → save fails, old file untouched); `TestSaveKeepsGoodFileOnShortWrite` (Review Focus 2: temp file truncated to half → save fails, `state.json` still the old one, strict-readable); `TestPrevIsStateBefore` (after two saves `state.prev.json` holds the first); `TestStateNeverMissing` (fail every single FS call of a save in turn: after each, `CheckState` or the start table still finds a readable state); `TestStartTable` (the four rows; in the fallback row: file moved aside, +1000, `StartInfo.Rewound`); `TestSchemaNewerIsEmergency`; `TestMigrationKeepsOriginal` (with an injected 0→1 function); `TestEmergencyWritesNothing` (no FS write call for durable files after an emergency start); `TestRuntimeDiscarded`; `TestRescueReadIsLenient`, `TestRescueDemoOnly` (a valid demo recipient in the copy → only that one); `TestLockSecondOpen` (real temp folder: second `Open` → `ErrLocked`; after `Close` it works); `TestOpenOnUnwritableDir` (Review Focus 3: every write fails → `Open` succeeds with `LockUnavailable`, the state it read is served); `TestTmpIsFolder` (real temp folder: a non-empty folder at `state.json.tmp` → `Open` works, a save fails, removing the folder repairs it — on Windows and Linux); `TestRenameRetryWindows` (build tag: target held open by a reader for 300 ms → rename succeeds).
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** `bash tools/check-go.sh`. Commit — `Go: Ablage – sichern mit Zurücklesen, ein Programm pro Datenordner, Rückfall und Notbetrieb`.

### Task 7: `store` II — one writer, deadlines, volatile data, history

**Files:** Create `internal/store/{durable.go,volatile.go,history.go,selftest.go,flush.go}` + tests.

**Interfaces — produces** the `Store` methods of "Shared vocabulary", plus:

```go
type SelfTest struct{ OK bool; Step string }           // Step: ok | write | read | delete | save
func (s *Store) SelfTest() SelfTest                    // selftest.tmp schreiben, zurücklesen, löschen; "save" = letztes Sichern scheiterte
func (s *Store) SaveHealth() (durableOK, volatileOK bool)
func (s *Store) Flush(ctx context.Context) error       // wartet, bis alles Ausstehende auf der Platte liegt
func (s *Store) Lifebeat()                             // lifebeat_at = jetzt (flüchtig)
```

**`Update` exactly** (spec §6.2): callers queue for the one writer slot and give up when `ctx` ends → `ErrDropped`, nothing happened. Holding the slot: clone the snapshot → drop outbox entries the history writer reported as appended → `fn(draft)` (an error from `fn` is returned as it is, nothing saved) → `Validate` (failure → `ErrInvalid`) → save in a goroutine; the caller waits for the save or for `ctx`. Save done in time: swap the pointer, hand new outbox entries to the history writer, refresh the rescue copy when recipients or demo changed (beside, a failure holds nothing), release the slot, return nil. Save failed: `ErrStorage`, nothing changed. `ctx` ends while saving: return `ErrLate` at once — the save runs on; if it succeeds the pointer is swapped and the entries are written just as above, and only then the slot is released. In emergency mode: `ErrEmergency` immediately. `fn` is called at most once.

**Volatile holders** (`runtime.json`, `sessions.json`): `UpdateRuntime`/`UpdateSessions` clone, apply, swap the pointer under a mutex and return — never an error, never a wait. One writer goroutine per file saves the newest snapshot right after a change (one save at a time; changes arriving meanwhile are folded into the next save).

**History:** an entry is appended to `history.jsonl` only after the document carrying it in its outbox is saved. The writer opens, appends, syncs and closes per batch; if the file does not end in a line break it writes one first (Review Focus 2); above 5 MB it rotates to `history.1.jsonl` first. At start: entries found in an outbox whose `uid` is not among the last 500 lines are appended. `History(limit, keep)` never opens a file: the raw lines of `history.jsonl` and `history.1.jsonl` (together at most about 10 MB) are read once at start and kept current by the writer; a call scans them backwards, parses line by line, skips broken lines, stops at `limit`, and puts everything not yet appended in front — newest first. (The time of a resend comes from here, so the alarm path stays off the disk.) `LogAndWait` puts the entry into the volatile outbox and waits until it is on disk or `max` has passed.

- [ ] **Step 1: Tests** (`FaultFS`, `clock.Fake` where time matters): `TestUpdateDeadlines` (a: save hangs past the deadline → `ErrLate`, release the hang → snapshot shows the change and the history has the entry; b: a second `Update` queued behind the hanging one → `ErrDropped` at its deadline and its `fn` never ran; c: save fails → `ErrStorage`, snapshot unchanged, no history line); `TestOneWriterAtATime` (100 goroutines each add 1 to a counter in the state → 100, `go test -race` clean); `TestReadersNeverWait` (with a hanging save, `State()`, `Runtime()`, `History()` return within 10 ms); `TestVolatileNeverBlocks` (hanging disk: 1 000 `UpdateRuntime` calls return; after release one save holds the newest value); `TestVolatileSaveFailureIsSilent`; `TestHistoryAfterSaveOnly` (entry appears in the file only after its document is saved; a failed save leaves no line); `TestNoDuplicateAfterRestart` (kill between "document saved" and "line appended", and between "line appended" and "outbox pruned": after reopening, the line is there exactly once); `TestHistoryReadsPending` (an entry not yet appended is already in `History()`); `TestAppendAfterTornLine`; `TestRotation`; `TestLogAndWait` (on disk → true; hanging disk → false after 300 ms, and the entry still arrives later); `TestSelfTest` (ok; folder at `selftest.tmp` → `write`; last durable save failed → `save`); `TestRescueFollowsChanges` (recipient added → the copy has it; copy unwritable → the update still succeeds); `TestSaveSurvivesOddStrings` (Review Focus 1: a label with U+FFFD, quotes, `<`, U+2028 saves and reads back equal); `TestFlush`; `TestCloseWritesEverything` (after `Close` a fresh `Open` sees every volatile change).
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** `bash tools/check-go.sh`; `go test -fuzz=FuzzHistoryLine -fuzztime=30s ./internal/store` (a fuzz test for the lenient line reader). Commit — `Go: ein Schreiber mit Fristen, Flüchtiges nebenher, Verlauf über die Outbox`.

### Task 8: The server's frame — life cycle, control socket, configuration at run time, Go driver

**Files:**
- Create: `internal/core/core.go`, `internal/config/provider.go` (+ test), `internal/control/{server.go,client.go}` (+ test), `internal/web/{routes.go,respond.go,static.go}` (+ tests), `internal/app/{serve.go,commands.go}` (+ test)
- Modify: `cmd/fw-alarm/main.go`, `tests/backend/harness.mjs` (driver `go`), `tests/backend/README.md`

**Interfaces — produces:**

```go
// config
type Provider struct{ /* … */ }
func NewProvider(path, lastGoodPath string, clk clock.Clock, log *slog.Logger) (*Provider, error)   // Start: config.toml, sonst die Kopie; beides nicht → Fehler
func (p *Provider) Get() *Config          // aus dem Speicher
func (p *Provider) OK() bool              // false = Datei fehlerhaft, letzte gute gilt
func (p *Provider) Problem() string       // ohne Geheimnisse
func (p *Provider) Reload() error         // sofort; nil = neue Fassung gilt
func (p *Provider) Run(ctx context.Context)             // alle 2 s Änderungszeit, Größe, Dateinummer prüfen
func (p *Provider) OnChange(fn func(ok bool))            // nach jedem Wechsel gut↔fehlerhaft und jeder neuen Fassung

// web
type Handler func(w http.ResponseWriter, r *http.Request)
func NewRouter(c *core.Core) *Router
func (rt *Router) Handle(path string, h Handler)        // genauer Pfad, jede Methode
func Text(w http.ResponseWriter, status int, body string)   // PHP: text_response
func JSON(w http.ResponseWriter, status int, v any)          // PHP: json_response

// control: HTTP über den Unix-Socket (Rechte 0600)
type Hooks struct {
    Check  func(ctx context.Context) string   // voller Prüf-Lauf, seine Zeile
    Health func() Health
    Flush  func(ctx context.Context) error
    Reload func() error
    Stop   func()
}
type Health struct{ Version, State string; SaveOK, ConfigOK bool; Hints []string }   // State: normal | fallback | emergency
func Serve(ctx context.Context, socketPath string, h Hooks, log *slog.Logger) error
func Call(socketPath, command string, timeout time.Duration) (output string, ok bool, err error)   // command: check | health | flush | reload | stop

// app
func Serve(ctx context.Context, configPath, version string) error   // kehrt nach geordnetem Beenden zurück
```

- Command line added here: `fw-alarm serve`, `check`, `health`, `flush`, `reload`, `stop` (each `[--config <file>]`; the last five find the socket through `control_socket`). `health` prints `running=1`, `version=…`, `state=…`, `save_ok=0|1`, `config_ok=0|1`, one `hint=…` line per hint, and exits 0 only for `state=normal`, `save_ok=1`, `config_ok=1`; a server that cannot be reached → `running=0`, exit 1. Until Task 17 `check` answers `NOCH_NICHT_GEBAUT`.
- `listen`: `host:port` or `unix:<path>` (socket 0660, removed first if it is a stale socket). The server listens itself.
- `http.Server`: header timeout 10 s, read 30 s, write 150 s, idle 120 s, header size 64 kB. Paths with repeated slashes are merged before the table is consulted; no redirect, ever. Not in the table → 404 with an empty body.
- Static files from `webassets.FS`: `/manifest.json`, `/assets/<file>` — content type from a FIXED table (`.css` `text/css`, `.js` `application/javascript`, `.json` `application/json`, `.svg` `image/svg+xml`, `.png` `image/png`, `.ico` `image/x-icon`; no `charset`), `X-Content-Type-Options: nosniff`, cache headers as Nginx sends them today (`server/nginx/fw-alarm.conf`), folder listings 404. `web.AssetVersion(path) string` = first 8 bytes of the SHA-256 of the embedded file as an unsigned number modulo 10^10, ten digits (the scenarios demand digits).
- The last-good copy `config.last-good.toml` (0600) is written into the state folder after every valid load.
- **Harness driver `go`** (`FW_BACKEND=go`, binary from `FW_GO_BIN`): folder `<root>/state`, file `<root>/config.toml` (the base values of the PHP driver, plus `state_dir`, `listen = "127.0.0.1:<port>"`, `control_socket = "<root>/control.sock"`, `check_interval_seconds = 0`), a small TOML writer for strings, numbers, booleans and lists of strings. `startServer()` starts `serve` and waits for `GET /manifest.json`, retrying while the port is still busy; `stopServer()` runs `fw-alarm stop` and waits for the process to end (kill after 10 s and fail loudly); `hardKill()` kills without warning (new in the driver contract; the PHP driver throws); `configure()` rewrites the file and runs `fw-alarm reload`; `reset()` stops, empties the state folder, writes the base configuration, starts; `runWatchdog()` runs `fw-alarm check` and resolves with its output; `kind: 'go'`; `words.configFile = 'config.toml'`. `backend.state` writers: stop → change the JSON files of "File formats" → start. Readers: `fw-alarm flush`, then read. `storage.fail(what)`: non-empty folder at `selftest.tmp` (`selftest`), `state.json.tmp` (`durable`), `history.jsonl` moved aside and a folder in its place (`history`), `runtime.json.tmp` and `sessions.json.tmp` (`volatile`); `repair` undoes it. `backend.files` = raw access to the state folder (`read`, `write`, `corrupt`, `remove`, `exists` by file name) — for `go-*.test.mjs` only, as `backend.data` is for `php-*.test.mjs`. After a restart no request may fail because of a kept connection: `request()` sends `connection: close` (for both drivers).

- [ ] **Step 1: Go tests.** `TestProviderReloadsOnChange`; `TestProviderKeepsLastGood` (broken file → `Get()` still the previous one, `OK()` false, `Problem()` set and free of secrets; repaired → new values, `OK()` true); `TestProviderStartsFromCopy` (unreadable `config.toml` + a copy → starts, `OK()` false; neither → error); `TestProviderNeverReadsInGet` (count FS calls through an injected reader: 1 000 `Get()` → 0 reads); `TestControlRoundTrip` (every command; socket file mode 0600 on Linux); `TestControlStopEndsServe`; `TestOnlyTableAddressesAnswer` (`/api/health.php`, `/config.toml`, `/state.json`, `/lib/common.php`, `/data/x`, `/.git/config`, `/assets/` → 404; `//api//poll.php` reaches the poll handler without a redirect); `TestStaticTypes`; `TestAssetVersionIsDigits`; `TestServeSecondInstanceAborts` (same state folder → error naming the reason, exit code 1 in `main`); `TestSlowClient` (headers unfinished after 10 s → closed).
- [ ] **Step 2:** Implement server and driver.
- [ ] **Step 3:** `bash tools/test-go.sh tests/backend/session.test.mjs` → the server starts and stops for every case, the cases FAIL with 404 — no hang, no leftover process (`tasklist | grep fw-alarm` prints nothing). Create `tests/backend/go-driver.test.mjs` (Go only) with the driver's own cases: after `reset()` the server answers `GET /manifest.json` and `fw-alarm health` exits 0; `stopServer()` then `startServer()` answers on the same port; `configure()` with a broken value → `health` prints `config_ok=0`, a repaired one → `config_ok=1`; `hardKill()` then `startServer()` works; `storage.fail('durable')` leaves a non-empty folder at `state.json.tmp` and `repair` removes it; a second `serve` on the same folder exits non-zero.
- [ ] **Step 4:** `bash tools/check-go.sh`. Commit — `Go: Server-Gerüst mit Steuer-Zugang, Konfiguration zur Laufzeit und dem Treiber im Prüfstand`.

### Task 9: `web` — forms, headers, proxy trust, brakes, sessions, login, pages

**Files:** Create `internal/web/{form.go,headers.go,proxy.go,throttle.go,session.go,verify.go,login.go,pages.go}` + tests, `internal/web/templates/{login.html,app.html}`, `internal/web/testdata/*.html`, `internal/dashapi/{actions.go,status.go}` + tests; modify `internal/app/serve.go`.

**Interfaces — produces:**

```go
// web
type Form struct{ /* Paare in der Reihenfolge des Rumpfs */ }
func ParseForm(w http.ResponseWriter, r *http.Request) (*Form, bool)   // false: schon beantwortet (413). urlencoded + multipart, NUR Rumpf, höchstens 64 kB
func (f *Form) Get(name string) string          // letzter Wert; "" wenn er fehlt
func (f *Form) Has(name string) bool
func (f *Form) Each(fn func(name, value string) bool)   // jeder Name einmal, Reihenfolge des ersten Auftretens, letzter Wert
func ClientIP(c *core.Core, r *http.Request) string      // PHP: login_client_ip
func ThrottleKey(ip string) string                       // PHP: client_throttle_key (IPv6 als /64)
func IsHTTPS(c *core.Core, r *http.Request) bool         // PHP: request_is_https
func FromTunnel(c *core.Core, r *http.Request) bool      // vertrauter Proxy ODER Verbindung über den Unix-Socket
type Hit struct{ Blocked bool; Count int }
func ThrottleHit(c *core.Core, bucket, who string, max int, window time.Duration) Hit   // PHP: throttle_hit; höchstens 500 Absender
func ThrottleClear(c *core.Core, bucket, who string)
func MachineAuth(c *core.Core, f *Form) bool             // Token aus dem Rumpf; Aufrufer antwortet 403 "FORBIDDEN"
type Session struct{ User, Role, CSRF string }
func Resume(c *core.Core, w http.ResponseWriter, r *http.Request) (*Session, bool)   // PHP: resume_dashboard_session + refresh_dashboard_cookie
func Begin(c *core.Core, w http.ResponseWriter, r *http.Request, user, role string) *Session
func End(c *core.Core, w http.ResponseWriter, r *http.Request)
func EndAll(c *core.Core)                                // Konfiguration fehlerhaft
func StorageError(w http.ResponseWriter, err error, jsonAPI bool)   // ErrStorage/ErrEmergency → bekannter Satz; ErrLate/ErrDropped → "dauert zu lange"; 500, code "storage"
var OnLogin func(c *core.Core, role, user, ip string)    // gesetzt in Aufgabe 11
var OnLockout func(c *core.Core, ip string, windowSeconds int)

// dashapi
type Action struct{ Post, CSRF, Admin bool; Handle func(x *Ctx) }
type Ctx struct{ Core *core.Core; W http.ResponseWriter; R *http.Request; Form *web.Form; Sess *web.Session }
func Register(name string, a Action)         // die Tabelle mit allen 23 Aktionen steht HIER; spätere Aufgaben setzen nur Handle
func Handler(c *core.Core) web.Handler       // 401 → 405 → 400 → 403 csrf → 403 readonly → Handle (PHP: DASHBOARD_ACTIONS)
func StatusPart(name string, fn func(x *Ctx) any)
```

- `web.Budget` of "Shared vocabulary" is implemented here.
- Form details (spec §6.4): a repeated name keeps its first position and its last value; names with brackets (`a[]`, `a[b]`) are skipped; a body of another content type has no fields; every name and value is passed through `strings.ToValidUTF8(…, "�")` (Review Focus 1).
- Security headers on every dynamic answer, verbatim from the top of `nas_dashboard/lib/common.php` (`X-Frame-Options`, the `Content-Security-Policy` string, `Cache-Control: no-store`, `X-Content-Type-Options`, `Referrer-Policy`, `Permissions-Policy`, `X-Robots-Tag`; `Strict-Transport-Security` only when `IsHTTPS`).
- Sessions (spec §8): id = 32 bytes from `crypto/rand`, stored under its SHA-256; cookie `boss925_dashboard` with the attributes of PHP `dashboard_cookie_options()`, path `/`, 12 h, renewed on every authenticated request; bound to `Config.AuthStamp(role)`; `last_seen` written at most once per minute; idle more than 43 200 s → gone; a forged id creates nothing and sets no cookie; logout removes session and cookie. A `last_seen` in the future counts as now (Review Focus 4). When the provider reports a broken file → `EndAll` (wired with `Provider.OnChange` in `app`).
- Login (PHP `index.php`): `Sec-Fetch-Site` rule → brake blocked? (429) → password slot → count the attempt (`ThrottleHit`, before the check) → exactly ONE `config.VerifyPassword` per attempt, also for unknown users (against a fixed dummy hash) → failure: 1 s pause outside the slot → success: clear the counter, new id. Slots: at most two checks at once, at most one per sender, waiters served in turn per sender, at most 5 s → status 503 with the fixed sentence; such an attempt is not counted. Login works in emergency mode and when nothing can be saved.
- Pages: `html/template`; login page and app shell byte-equal to PHP's `render_login_page()` / `render_app_page()` except `?v=`. Capture the golden files once with `php -r` from `nas_dashboard/lib/shell.php` (login with error and kept user name; shell for admin and reader) into `internal/web/testdata/`; meta tags `csrf`, `role`, `user`, `public-base`, `mute-max` as there.
- `status` delivers here: `ok`, `status` (the last push without `remote_addr`), `seen_age_seconds`, `offline` (with the 600 s floor while `alarm_pending` is `1`), `timezone`, `csrf`, `server_time`; the keys `command`, `demo`, `nas_keys_version`, `demo_sync_ok`, `device_hint`, `watchdog` are present with empty values until Tasks 12, 13, 14, 17 register them. An action without `Handle` answers 501 `{"ok":false,"message":"Noch nicht gebaut."}`.

- [ ] **Step 1: Go tests.** `TestFormOrderAndLastValue` (60 fields, urlencoded and multipart); `TestFormSkipsBrackets`; `TestFormOtherContentType`; `TestFormTooLarge` (413); `TestFormMakesValidUTF8`; `FuzzParseForm`; `TestSecurityHeaders`; `TestClientIP` (untrusted sender ignores headers; trusted + `X-Forwarded-For: a, b` → `b`; invalid candidate → the sender; `trusted_proxies` set but not matching → headers ignored); `TestUnixListenerCountsAsTunnel`; `TestThrottleKey` (`::ffff:1.2.3.4` → `1.2.3.4`; a v6 address → its /64); `TestThrottleCap` (501st new sender neither counted nor blocked); `TestSessionRules` (forged id; idle; changed password hash; `last_seen` once per minute; new id at every login); `TestSessionClockBackwards` (Review Focus 4: clock set back 2 h → the session is still valid and does not live longer than 12 h after the next request); `TestLoginOneVerifyPerAttempt` (count through an injected verifier: one for admin, reader, unknown user); `TestLoginSlots` (three senders, 10 attempts each with a verifier that takes 100 ms: never more than two at once, never two of one sender, order fair per sender; a waiter past 5 s → 503 and not counted); `TestLoginSleepOutsideSlot`; `TestLoginWorksWhenNothingSaves` (`FaultFS` failing everything → 302 and a usable session); `TestPagesEqualPHP` (golden files, `?v=<digits>` normalised on both sides); `TestStorageError` (both sentences, JSON and text form, no path in either); `TestActionTable` (the 23 names with the three flags of `DASHBOARD_ACTIONS`).
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** `bash tools/test-go.sh tests/backend/session.test.mjs tests/backend/dashboard-access.test.mjs` → pass, including the idle case that PHP skips. `bash tools/check-go.sh`. Commit — `Go: HTTP-Rahmen, Anmeldung und Sitzungen, Seiten-Hüllen`.

### Task 10: History for the dashboard (`logview`, `log_view`)

**Files:** Create `internal/logview/{events.go,view.go}` + tests, `internal/dashapi/log.go`.

**Interfaces — produces:**

```go
type Field struct{ Name, Type, Path string; Optional bool; Default any }   // Type: str | int | bool
type Event struct{ Cat string; Core, Admin bool; Fields []Field }           // Cat: alarm | keys | system | command
var Events map[string]Event                       // PHP: log_event_types + state_rewound (system, Admin) + send_interrupted (alarm, Core)
func InFilter(e model.Entry, filter string) bool  // all | alarm | alarm_core | keys | system
func View(e model.Entry, isAdmin bool) (map[string]any, bool)   // PHP: log_event_view
func Read(c *core.Core, limit int, filter string, isAdmin bool) []map[string]any   // was die Rolle nicht sehen darf, fällt VOR dem Abzählen weg
```

- [ ] **Step 1: Go tests.** `TestEventTableComplete` (every name in `model.AllEvents` has a row and the reverse; `demo_repaired` is still there for old entries; `relay_alarm.source` defaults to `relay`; `notify_changed` and `setting_changed` admin only; the send events are `Core`); `TestViewTypes` (`"minutes":"7"` → 7; missing optional field absent; unknown event not shown; `results` never shown); `TestOldEntriesWithoutUID` (a PHP line without `uid` and without `demo` is shown).
- [ ] **Step 2:** Implement; register `log_view` (limit clamped to 1…100, unknown filter = `all`, a `late_ack` takes its type from `commands.recent_types`).
- [ ] **Step 3:** `bash tools/test-go.sh tests/backend/dashboard-log.test.mjs` → pass. `bash tools/check-go.sh`. Commit — `Go: Verlauf – Ereignis-Tabelle, Filter, Ausgabe je Rolle`.

### Task 11: `bark` and `notify`

**Files:** Create `internal/bark/{client.go,ascii.go,fields.go}` + tests, `internal/notify/{types.go,settings.go,send.go,login.go}` + tests, `internal/dashapi/notify.go`; modify `internal/app/serve.go` (set `web.OnLogin`, `web.OnLockout`).

**Interfaces — produces:**

```go
// bark
type Job struct{ Key string; Fields map[string]string }
type Result struct{ OK bool; HTTP int; Err string }
func New(cfg func() *config.Config) *Client
func (c *Client) PostMany(ctx context.Context, jobs []Job, tries int) []Result   // PHP: bark_post_many
func ASCII(s string) string                    // PHP: bark_ascii
func MaskKey(k string) string                  // PHP: mask_bark_key
func AlarmFields(cfg *config.Config, body string, muted bool, muteVolume int) map[string]string   // PHP: bark_alarm_fields
func FalseAlarmFields(cfg *config.Config, title, body string, muted bool, muteVolume int) map[string]string
func TestFields(cfg *config.Config, now time.Time) map[string]string             // PHP: bark_send_test

// notify
func Types(c *core.Core) []TypeDef             // PHP: admin_notify_types (Reihenfolge wie dort)
func GlobalSpecs(c *core.Core) []OptionDef     // PHP: admin_notify_global_specs
func Settings(c *core.Core) Resolved           // PHP: load_notify_settings – vollständig, geprüft, Unbekanntes = Standard
func Enabled(c *core.Core, kind string) bool
func ImportantFlags(c *core.Core) (boxOffline, nasWeb, outsideWatcher bool)
func InQuietHours(global map[string]string, now time.Time) bool
type Used struct{ Level string; QuietCapped, PrimaryFailed bool }
func Send(ctx context.Context, c *core.Core, kind, title, body string, force bool) (result string, used Used)   // "sent" | "failed" | "off"; PHP: admin_notify_send
func SendTo(ctx context.Context, c *core.Core, key, title, body string) bool      // Info an einen BETROFFENEN (PHP: bark_send_status)
func OutsideWatcherConfigured(cfg *config.Config) bool
func CheckSetting(c *core.Core, kind, key, value string) error                     // für notify_set und die Übernahme
```

- Bark client: connect 5 s, whole request 10 s, no redirects followed, response body never read (closed at once), `POST <bark_host>/<key>` urlencoded, 4xx never retried, 500 ms between rounds, every open job of a round at the same time, `bark_host` read anew for every send. Each job runs under `recover`.
- The hint of the kind `nas_web` is the new text of "Texts this plan fixes".

- [ ] **Step 1: Go tests.** `TestASCII` (the whole table of `bark_ascii`, line breaks kept, tab → space, other non-ASCII and invalid UTF-8 → `?`); `TestPostManyRounds` (against `httptest`: 500 → `tries` attempts with ≥ 500 ms between rounds; 400 → one attempt; cut connection → retried, `Err` set; a 2 s slow key does not delay the others; a 302 is not followed; a 50 MB answer does not grow memory); `TestPostManyRecovers`; `TestAlarmFields` (loud: `level=critical`, sound, `volume=10`, `call=1`; muted with volume 3: `volume=3`, no `call`; `bark_alarm_call=false`: no `call`); `TestFalseAlarmFields` (never `call`; volume falls back to the alarm volume); `TestQuietHoursAcrossMidnightAndDST` (22→7: 23:30 and 06:59 true, 07:00 false; equal hours never quiet; on 2026-03-29 and 2026-10-25 the local hour decides); `TestSendResults` (no status key → `off`; kind off → `off`; invalid status key → `failed`; unknown kind → `failed`; status key fails and the deputy accepts → `sent` with `PrimaryFailed`; deputy equal to the status key not sent twice); `TestSettingsDefaults` (config values to the second; unknown stored value → default); `TestNasWebHint`; `TestLoginNoticesAfterTheAnswer` (both run through `Core.Bg`).
- [ ] **Step 2:** Implement; register `notify_list`, `notify_set` (stores only what is set explicitly; unknown kind/key/value rejected, nothing stored), `notify_test`.
- [ ] **Step 3:** `bash tools/test-go.sh tests/backend/dashboard-notify.test.mjs` → pass, including the case "302 never waits for Bark" that PHP skips. `bash tools/check-go.sh`. Commit — `Go: Bark-Versand und die einstellbaren Meldungen an den Admin`.

### Task 12: Recipients, mute, demo mode, settings, `api/keys.php`

**Files:** Create `internal/dashapi/{keys.go,mute.go,demo.go,settings.go,expire.go}` + tests, `internal/box/{keys.go,respond.go}` + tests.

**Interfaces — produces:**

```go
// dashapi
func ExpireStaleMutes(ctx context.Context, c *core.Core) error   // Update + danach (über Core.Bg) die Info "wieder laut" an Betroffene und die Meldung mute_expired
// box
func Nonce(raw string) string                 // erste 80 Zeichen, dann nur A-Z a-z 0-9 _ -
func KeysVersion(c *core.Core) int            // 0 im Notbetrieb
```

- Actions: `keys_list` (overdue mutes go back to loud first — a failure there changes nothing about the answer; effective `muted`, `mute_overdue`, `mute_until`, phone and `mute_token` only for the admin; emergency → `code=keys_corrupt`), `keys_add`, `keys_delete`, `keys_rename`, `keys_replace_key`, `keys_set_phone`, `keys_new_mute_token`, `keys_mute`, `keys_set_mute_permanent`, `keys_set_mute_volume`, `demo_set`, `settings_list`, `settings_set` — rules, messages and history entries as the PHP handlers in `dashboard_api.php`; which of them raise `keys_version` exactly as `CLAUDE.md` says. Each is ONE `Store.Update` with `web.Budget(web.BudgetUI)`; a failure answers `web.StorageError`. `demo_set` changes demo state and version in the same update — the code `inconsistent` no longer exists.
- Status parts: `demo`, `nas_keys_version`, `demo_sync_ok`.
- `api/keys.php`: machine auth → `ExpireStaleMutes` within `BudgetBox` (a failure changes nothing about the answer) → plain text `ok`, `nonce`, `version`, `count`, `key<i>`, `muted<i>`, `mutevol<i>` from ONE snapshot; emergency → `version=0`, `count=0`.

- [ ] **Step 1: Go tests.** `TestDemoSetIsOneChange` (with `FaultFS` failing the save: neither demo state nor version changed; succeeding: both in one snapshot); `TestStorageSentences` (`ErrStorage` → the known sentence, `ErrLate` → "dauert zu lange", both status 500 and `code: storage`); `TestKeysAnswerFromOneSnapshot` (a `demo_set` racing 1 000 `api/keys.php` calls: every answer is either the full list under the old version or the demo list under the new one); `TestKeysListEmergency`; `TestPermanentBumpsOnlyWithMuteChange`; `TestRenameDemoRecipientUpdatesLabel`; `TestReplaceKeyOfDemoRecipientRejected`.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** `bash tools/test-go.sh tests/backend/dashboard-keys.test.mjs tests/backend/dashboard-settings.test.mjs tests/backend/box-keys.test.mjs` → pass except `box-keys` cases that poll. Extend `go-driver.test.mjs`: `storage.fail('durable')` → `keys_add` answers 500 `code: storage`, `fw-alarm health` says `save_ok=0`; after `repair` and a successful change `save_ok=1`. `bash tools/check-go.sh`. Commit — `Go: Empfängerliste, Arbeitsmodus, Demo-Modus und die Liste für die Box`.

### Task 13: Status push

**Files:** Create `internal/model/status.go` + test, `internal/box/{status.go,devices.go}` + tests, `tests/backend/go-box.test.mjs`, `tests/backend/go-storage.test.mjs`.

**Interfaces — produces:**

```go
// model (BoxStatus und Commands gibt es seit Aufgabe 5)
type PushVerdict struct {
    Stale      bool          // veralteter Push: nur bestätigen
    RelayAlarm *Entry        // relay_alarm mit source relay|remote, sonst nil
    Restart    *RestartInfo  // Neustart erkannt (für box_restart), sonst nil
    Foreign    bool          // anderes Board als box_device_id
}
func EvaluatePush(prev *Runtime, cmds Commands, f Fields, viaBridge bool, remote string, now time.Time, boxDeviceID string) (next func(d *Runtime), v PushVerdict)
func DeviceHint(rt *Runtime, boxDeviceID string, now time.Time) string   // PHP: device_hint, die drei Sätze wortgleich
```

- The push keeps the first 40 fields, 300 bytes each, key characters as PHP `clipped_post_fields`, never the token. Rules as `nas_dashboard/api/status.php`: stale push (arrived earlier than the stored one, smaller uptime, < 60 s apart) → acknowledged only; restart (`uptime_s` fell, or the start moment jumped back, only when the uptime is not longer than the gap to the previous push); relay alarm when `last_alarm`/`alarm_seq` of THIS device changed (per `device_id`, 10 newest), `source=remote` when the box acked an ALARM command ≤ 300 s ago or one was delivered ≤ 60 s ago and is not acked; `box_device_id` set → only that board counts, others only in `devices`.
- The handler touches volatile data only: answer 200 at once (`ok=1`-style text as PHP), always. After the answer (`Core.Bg`): the message `box_restart`; and when the pushed `keys_version` is higher than the server's → one `Update` that sets `keys_version` to the pushed value + 1 (so the box fetches the list again).
- Status part `device_hint`.

- [ ] **Step 1: Go tests.** `TestEvaluatePush` (table: first push; relay alarm by `last_alarm`; by `alarm_seq`; change to `nie` = reboot, no alarm; old firmware without `alarm_seq`; `remote` by acked command; `remote` by delivered command; stale push; restart by uptime; restart by start moment; no restart when the uptime exceeds the gap; foreign board); `TestClippedFields`; `TestDeviceHintTexts` (three sentences verbatim); `TestStatusNeverTouchesDurable` (`FaultFS` hanging on `state.json.tmp` → answer within 50 ms).
- [ ] **Step 2:** Implement.
- [ ] **Step 3: Go-only black-box cases.** `go-box.test.mjs`: "meldet die Box eine höhere Listen-Version, hebt der Server seine sofort darüber" (push `keys_version: 41` → `waitFor` `listVersion()` 42 → the next poll reports 42). `go-storage.test.mjs`: "Status wird angenommen, wenn nichts gesichert werden kann" (`storage.fail('volatile')` and `('durable')` → 200, the dashboard shows the push). Both files `{ skip: backend.kind !== 'go' }`.
- [ ] **Step 4:** `bash tools/test-go.sh tests/backend/box-status.test.mjs tests/backend/go-box.test.mjs tests/backend/go-storage.test.mjs` → pass. `bash tools/check-go.sh`. Commit — `Go: Status-Push der Box mit Relaisalarm, Neustart und fremden Boards`.

### Task 14: Commands (`poll`, `ack`, `enqueue`, `cancel`)

**Files:** Create `internal/model/command.go` + test, `internal/box/{poll.go,ack.go}` + tests, `internal/dashapi/command.go`.

**Interfaces — produces:**

```go
// model – reine Regeln, alle auf dem Entwurf einer Änderung
func (s *State) Enqueue(typ, by string, now time.Time, lockSeconds int) (Command, string)   // "" = angelegt, sonst der Ablehnungs-Satz
func (s *State) ExpirePending(now time.Time, ttl int) bool                                    // pending zu alt → expired
func (s *State) Deliver(now time.Time) *Command           // pending → delivered; nil, wenn nichts wartet
func (s *State) Ack(id int, result, message string, now time.Time) AckKind   // AckFirst | AckRepeat | AckLate | AckUnknown
func (s *State) Cancel(by string, now time.Time) (Command, bool)
func (s *State) DemoAlarmStillSafe(cmd Command) bool      // PHP: demo_alarm_still_safe
func LastAlarmSource(cmds Commands, now time.Time) string // "relay" | "remote" (PHP: last_alarm_source)
```

- Poll (spec §6.2), one `web.Budget(web.BudgetBox)` for everything: machine auth (else 403 `FORBIDDEN`) → overdue mutes back to loud (only when the snapshot shows one) → for the expected board: one `Update` that expires an old pending command, cancels an ALARM command whose demo state changed, and marks the waiting one `delivered`. **Only when that update returned nil does the answer carry the command.** Every other outcome: 200 `type=NONE`. The answer always carries `keys_version` (0 in emergency mode). A poll that finds nothing to change saves nothing.
- Ack: always `OK`. A repeated equal ack is only confirmed; a late one writes `late_ack` with the type from `recent_types`; an ack that cannot be saved leaves the command `delivered`.
- `enqueue` and `cancel` as the PHP handlers (ALARM only as the demo tool, secured by `DemoBlockReason` when created and again when delivered); status part `command` — a pending command past its TTL is shown as `expired` and the expiry is saved beside the answer.

- [ ] **Step 1: Go tests.** `TestCommandLifeCycle` (table over every transition and its history entry); `TestPollDeliversOnlyWhenSaved` (`FaultFS`: save hangs → answer `type=NONE` after about 1.5 s; after the release the command is `delivered` and no later poll carries it; save fails → the command still waits and the next poll delivers it); `TestTwentyPollsOneCommand`; `TestPollWritesNothingWhenIdle` (1 000 polls → no save of `state.json`); `TestAckAlwaysOK` (failing disk); `TestRecentTypes` (after 25 commands the 20 highest ids); `TestDemoAlarmCancelledAfterModeChange`.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** `go-storage.test.mjs`: "Poll liefert ohne Speichern keinen Befehl aus" (`enqueue` → `storage.fail('durable')` → poll answers 200 `type=NONE` with `keys_version` → `repair` → the next poll carries the command). `bash tools/test-go.sh tests/backend/box-poll-ack.test.mjs tests/backend/box-keys.test.mjs tests/backend/go-storage.test.mjs` → pass. `bash tools/check-go.sh`. Commit — `Go: Befehle an die Box – höchstens einmal ausliefern, quittieren, verfallen`.

### Task 15: Shortcut link (`api/mute.php`)

**Files:** Create `internal/mutelink/{handler.go,detect.go,page.go}` + tests, `internal/mutelink/templates/mute.html`, `internal/mutelink/testdata/*.html`.

- Behavior as `nas_dashboard/api/mute.php`: `t=<token>` and the old form `key=<Bark key>`; `state=on|off`; text answer for shortcuts and scripts, page for browsers (`Sec-Fetch-Dest: document` or `Accept: text/html`, GET only) with a 303 to `mute.php?done=…` that carries no token; HEAD and the known preview fetchers switch nothing; only failures are counted against the brake (bucket `mute`, limits as PHP), a valid token always works; the link can end "dauerhaft" but never set it; all through `model.ApplyMute` in ONE `Update` with `web.Budget(web.BudgetLink)`; overdue mutes go back to loud first; history entry and the optional message `mute_changed` after the answer. Storage failure: the answers of PHP `mute_storage_failed`.

- [ ] **Step 1: Go tests.** `TestPreviewDetection` (methods other than GET/POST; `Sec-Purpose`, `Purpose`, `X-Purpose`, `X-Moz` with `prefetch`/`prerender`/`preview`; every user-agent fragment of the expression in `api/mute.php`, any case; `Shortcuts/1 CFNetwork/1410 Darwin/22.6.0` is NOT a preview); `TestWantsHTML`; `TestPageEqualsPHP` (golden files from `mute_page()` for `ok`/`stale`/`crit`, `?v=` normalised); `TestRedirectQuery`; `TestTokenNowhere` (neither in the redirect, the page, the history nor the access log line); `FuzzMuteQuery`.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** `bash tools/test-go.sh tests/backend/mute-link.test.mjs` → pass. `bash tools/check-go.sh`. Commit — `Go: Kurzbefehl-Link samt Browser-Seiten, Vorschau-Sperre und Bremse`.

### Task 16: Alarm, Entwarnung, test push

**Files:** Create `internal/dashapi/{broadcast.go,alarm.go,keystest.go}` + tests; modify `tests/backend/go-storage.test.mjs`.

**Interfaces — produces:** actions `alarm`, `false_alarm`, `keys_test`;

```go
type Recipients struct{ Targets []model.Target; Demo, UsedFallback bool; Source string }   // Source: list | rescue | config
func RecipientsNow(c *core.Core) Recipients   // EINE Momentaufnahme: Liste und Demo-Zustand; Notbetrieb → Not-Kopie, sonst Not-Liste der Konfiguration
```

- Order of a send, as PHP `run_nas_broadcast()`: one snapshot → `expect_demo` (missing or different → 409 `mode_changed`, nothing sent) → `only_ids` (present but unusable → 400; empty intersection → nothing sent; it can only narrow) → `Store.LogAndWait(<event>_started, 300 ms)` → `Bark.PostMany` with a context that ignores the caller going away → overdue mutes back to loud → `Store.Log(<event>)` with `results`, `demo`, `resend`, and `started_uid` = the `uid` of the started entry → answer with `started_at`. Fields, texts and the stamp of a resend (`orig_at` up to 24 h; else the newest `*_started` without `resend` up to 6 h; else now; suffix ` (verspaetet zugestellt)`) as PHP `bark_broadcast()`/`resend_stamped_body()`. The fallback sentence names `config.toml`.
- Nothing in this path needs a save to succeed.

- [ ] **Step 1: Go tests.** `TestSnapshotUsedForCheckAndSend` (a `demo_set` between check and send does not change who gets it); `TestBroadcastWhenNothingSaves` (`FaultFS` failing everything: sent, answered; the history lines are simply missing); `TestStartedOnDiskBeforeFirstSend` (the append of `*_started` precedes the first HTTP request; with a hanging disk the first request leaves after about 300 ms); `TestBroadcastSurvivesClientDisconnect` (cancel the request after the first recipient answered, the mock delays the second by 1 s → both served, result logged); `TestOnlyIDsNeverWidens` (demo mode + `only_ids` of another recipient → nothing sent); `TestResendStamp`; `TestEmergencyRecipients` (rescue copy → those; rescue copy with a demo recipient → only him; no copy → the list of the configuration; neither → nothing sent and the answer says why); `TestNoUnbuiltActionLeft` (every name of the action table has a `Handle`).
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** `go-storage.test.mjs`: "der Alarm geht raus, wenn nichts gesichert werden kann" (`fail('durable')`, `('volatile')`, `('history')` together); "Notbetrieb: Alarm geht an die Not-Kopie, ohne sie an die Not-Liste" (two recipients → stop → `backend.files.corrupt('state.json')` and `('state.prev.json')` → start → alarm reaches both from the copy; remove the copy, restart → the key of `bark_keys_alarm`). `bash tools/test-go.sh` (whole suite) → `alarm-send` green, nothing earlier red. `bash tools/check-go.sh`. Commit — `Go: Alarm an alle, Entwarnung und Test-Push direkt an Bark`.

### Task 17: The checker inside the server and `api/alive.php`

**Files:** Create `internal/watch/{watcher.go,schedule.go,selfcheck.go,offline.go,outside.go,reminders.go,health.go,report.go,ownfaults.go,texts.go}` + tests, `internal/watch/testdata/*.txt`, `internal/alive/alive.go` + test, `tests/backend/go-watch.test.mjs`; modify `internal/app/serve.go`.

**Interfaces — produces:**

```go
func New(c *core.Core) *Watcher
func (w *Watcher) RunFull(ctx context.Context) string     // die Zeile ("WEB_OK | OK | …"); läuft schon einer, wartet der Aufruf und bekommt dessen Zeile
func (w *Watcher) RunMinute(ctx context.Context)          // nur "Box offline / wieder da"
func (w *Watcher) Run(ctx context.Context)                // Zeitplan, blockiert bis ctx endet
func (w *Watcher) OnStart(info store.StartInfo)           // Meldungen zu Rücksprung, verworfenen Laufzeitdaten, Notbetrieb
func (w *Watcher) OnConfig(ok bool)                       // fehlerhaft → Meldung sofort
func CronAge(c *core.Core, startedAt time.Time) int64     // für alive.php
```

- **Schedule** (spec §6.5): a sign of life (`Store.Lifebeat`) every minute, whatever `check_interval_seconds` says, and once more at shutdown; first full run 60 s after the start, then every `check_interval_seconds`; every minute in between `RunMinute`; `check_interval_seconds = 0` → no automatic run of either kind. Elapsed time comes from `Clock.After`, never from comparing wall-clock values (Review Focus 4).
- **Full run** = the sections of `nas_dashboard/cron/check_offline.php` in its order, each with its own markers (the three outage flows stay separate): storage self-test → box offline / back → outside watcher silent → reminders (demo, mutes daily, permanent mutes weekly) → box health → weekly report; plus overdue mutes back to loud, expired commands, expired sessions. Output tokens verbatim (`WEB_OK`, `NOTIFIED_OFFLINE`, `OHNE_PUSH (im Dashboard abgeschaltet): …` …). The line goes to the journal, to `fw-alarm check` and into `watch.last_run_line`. Rules that COUNT runs count full runs only.
- **Changed rules** (spec §6.5, §7): a message whose send failed is tried again no sooner than 15 min later (`watch.retry_after[kind]`), also in the minute tick. The first "Box OFFLINE" — only while the last status came through the bridge AND the outside watcher is configured — goes out when the threshold is reached and the outside watcher either called within the last 390 s or has been silent for more than 540 s. After a start with a last sign of life older than 90 s (or none), silence of box and outside watcher counts from the start. A marker in the future counts as now. `cron_age_s` = age of the last finished full run; before the first one after a start: the last run from `runtime.json`; without one: the time since the start. The "Box OFFLINE" text has no branch that blames the server; the branch "NAS zu Hause" stays. Storage self-test failing in 2 full runs in a row → "Dashboard DEFEKT" (new text), reminder every 24 h, once "wieder OK"; events `nas_web_failed`/`nas_web_recovered` as today.
- **The server's own faults** (kind `nas_web`, texts fixed above): emergency mode at once and every 24 h; rewind once; discarded `runtime.json`/`sessions.json` once; broken configuration at once and every 24 h.
- `api/alive.php` as the PHP file: POST only, the watcher's own token, 404 when it equals the machine token, lines `ok`, `nonce`, `stored`, `box_age_s`, `cron_age_s`, `cron_configured`, `nas_send_failed`; it touches volatile data only.
- Status part `watchdog` as PHP builds it in `dashboard_api.php` (without `offline_alert`/`web_alert`/`outside_alert` for the reader); with `check_interval_seconds = 0` the checker shows as standing.

- [ ] **Step 1: Go tests** (`clock.Fake`, `FaultFS`, Bark against `httptest`). `TestScheduleFirstFullRun` (nothing before 60 s; one at 60 s; the next at 60 s + 900 s); `TestMinuteTickOnlyBoxOffline` (between full runs a reminder that is due is NOT sent, "Box OFFLINE" is); `TestRunsNeverOverlap` (a second request during a running one gets the same line, the sections ran once); `TestLifebeatWithoutChecks` (`check_interval_seconds = 0`: sign of life every minute, no run); `TestRetryNotBefore15Min` (Bark down: over 60 simulated minutes at most 5 requests for "Box OFFLINE"); `TestFirstOfflineWaitsForWatcher` (table: watcher called 300 s ago → sent, text blames the box; 450 s ago → not yet; 600 s ago → sent, text names the NAS; not through the bridge → sent at once); `TestOfflineTextNeverBlamesServer`; `TestAwayRule` (last sign of life 600 s old, box status 1 200 s old → no "OFFLINE" at the first run; it comes once the threshold has passed since the start); `TestMarkersSurviveRestart`; `TestClockJumps` (wall clock back 3 h: no second "OFFLINE", reminders not suppressed for 3 h; forward 26 h: one run, one reminder, not 100); `TestDefektText` (the fixed text with the step; ASCII); `TestNoticeTexts` (golden files captured from PHP `box_offline_notice_text` for the cases that stay, and `weekly_report_text`); `TestWeeklyReportCounts` (demo sends, resends and `relay_alarm` with source `remote` not counted); `TestOwnFaultMessages` (each of the five occasions: title, body, kind `nas_web`, the 24 h rhythm where it applies); `TestCronAge` (the three cases); `TestAliveAnswer`.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** `bash tools/test-go.sh tests/backend/watchdog.test.mjs tests/backend/alive.test.mjs`. Cases that are red because of a changed rule above go through the divergence protocol; expect them around: DEFEKT texts, "Box OFFLINE" with a broken folder, the first "OFFLINE" through the bridge with a watcher seen 6.5–9 min ago, `cron_age_s` before the first run. `go-watch.test.mjs`: "Dashboard DEFEKT ab dem zweiten vollen Lauf, danach einmal wieder OK" (`storage.fail('selftest')`); "nach einer Auszeit des Servers keine Geister-Meldung" (stop → set `lifebeat_at` 300 s and the box status 1 200 s back through `backend.files` → start → `check` → no "OFFLINE").
- [ ] **Step 4:** `bash tools/check-go.sh`. Commit — `Go: Prüf-Lauf im Server – voller Lauf, Minutentakt, Lebenszeichen für den Außen-Wächter`.

### Task 18: When things go wrong — shutdown, crashes, many requests at once

**Files:** Create `internal/app/{shutdown.go,accesslog.go,sdnotify.go,probe.go,failure.go,interrupted.go}` + tests, `tests/backend/go-concurrency.test.mjs`, `go-restart.test.mjs`, `go-config.test.mjs`; modify `internal/app/serve.go`, `cmd/fw-alarm/main.go`, `tests/backend/README.md`.

**Interfaces — produces:** command `fw-alarm report-failure [--config <file>]`.

- **Shutdown** (SIGTERM, interrupt, `fw-alarm stop`): stop accepting → open requests, a running "Alarm an alle" and everything in `Core.Bg` finish, 40 s at most → last sign of life → `Store.Close`. An orderly shutdown loses nothing.
- **Panics:** a handler that panics answers 500 and logs the panic value and stack, never fields of the request.
- **Access log** to stdout: `<RFC 3339> "<METHOD> <path without query>" <status> <ms>ms bridge=<0|1> "<user agent>"`, through a queue of 1 000 lines that drops lines rather than holding a request; the self-probe is left out.
- **systemd** (no library; a no-op when `NOTIFY_SOCKET` is unset): `READY=1` once the listener is open; every 30 s a `GET /index.php` on the own `listen` address with a 5 s limit, and `WATCHDOG=1` only when it answered.
- **`report-failure`:** when `SERVICE_RESULT` is not `success` → unless the marker `crash-reported` next to the control socket is younger than 30 min → ONE Bark request (5 s limit) to `bark_key_status` with `level=active` and the fixed crash text; touch the marker. Reads `config.toml`, else the last-good copy; never needs the server.
- **Interrupted send:** at start, a `*_started` entry of the last 15 min that has a `uid` and no result entry naming it as `started_uid` → entry `send_interrupted` (`kind`, `started_at`, `demo`) and the message "Dashboard: Versand unterbrochen". Never a resend.

- [ ] **Step 1: Go tests.** `TestShutdownWaitsForBroadcast` (alarm to a recipient delayed by 2 s, then stop → the process ends after the recipient was served and the result is logged); `TestShutdownWaitsForBackground`; `TestShutdownLimit` (a background job that never ends → exit after 40 s of fake time); `TestHandlerPanicIs500` (the log line holds no form value); `TestAccessLogHasNoQuery` (`GET /api/mute.php?t=SECRET&state=on` → the line shows the path, not `SECRET`; `bridge=1` with `X-FW-Bridge`); `TestAccessLogDropsInsteadOfBlocking`; `TestSdNotify` (against a unixgram socket: `READY=1`, then `WATCHDOG=1` only while the probe answers; skip on Windows); `TestReportFailure` (success → nothing; failure → one request with the fixed text; a second one within 30 min → nothing; unreadable config and a last-good copy → still sent); `TestInterruptedSend` (found / not found when a result names it / never for entries without `uid`).
- [ ] **Step 2:** Implement.
- [ ] **Step 3: Go-only black-box suites** (whole files `{ skip: backend.kind !== 'go' }`):
  - `go-concurrency.test.mjs`: 20 polls at once with one waiting command → exactly one answer carries it, all are 200; 30 wrong logins at once with `login_max_failures: 8` → at most 8 answers "Login fehlgeschlagen.", the rest 429 or 503; 10 `keys_add` at once → 10 entries, 10 distinct ids, version + 10, distinct `mute_token`s; 10 shortcut-link calls `state=on` for one recipient → one `key_muted` entry, version + 1; a status push every 20 ms during an "Alarm an alle" to a `slow…` recipient → every push answers 200 within 1 s; 200 `status` requests at once → all 200.
  - `go-restart.test.mjs`: a restart loses nothing (session still valid, box status, markers, a delivered command, the history); stop during "Alarm an alle" to a `slow…` recipient → he is served and the result is in the history; `hardKill()` after a poll delivered a command → after the start no poll carries it again; `hardKill()` after the push of a relay alarm → exactly one `relay_alarm`; `hardKill()` during "Alarm an alle" → the started entry is there, after the start `send_interrupted` plus the message, nothing sent again; a second `serve` on the same folder exits with a failure and the sentence about the other program; "kaputte Zustandsdatei: Rückfall mit Rücksprung, dann Notbetrieb" (`state.json` corrupt → start: recipients of the previous version, `keys_version` + 1000, a new recipient gets an id ≥ old `next_id` + 1000, the message "Stand zurueckgesetzt"; then both files corrupt → `keys_list` answers `keys_corrupt`, poll reports `keys_version=0`, login works, `keys_add` is refused, the message "Dashboard NOTBETRIEB", `fw-alarm health` exits 1 with `state=emergency`).
  - `go-config.test.mjs`: a typo in `config.toml` → the old password still logs in, every earlier session answers 401, the message "Konfiguration fehlerhaft", `health` says `config_ok=0`; a restart with the broken file starts from the copy; the repaired file → `config_ok=1`.
- [ ] **Step 4:** `bash tools/test-go.sh` → everything passes; five times in a row without a flake (`for i in 1 2 3 4 5; do bash tools/test-go.sh || break; done`). `bash tools/check-go.sh`. Commit — `Go: geordnetes Beenden, Absturz-Meldung, Watchdog – und die Proben mit vielen Anfragen, Neustart und hartem Abbruch`.

### Task 19: The remaining commands and the two texts of the frontend

**Files:** Create `internal/app/{editconfig.go,checkstate.go,statebump.go}` + tests; modify `cmd/fw-alarm/main.go`, `internal/app/serve.go` (hints of `health`), `nas_dashboard/assets/app/logic.js`, `nas_dashboard/assets/app/view-settings.js`, `tests/dashboard/logic-log.test.mjs`, `tests/dashboard/logic-status.test.mjs`.

**Interfaces — produces:**
- `fw-alarm edit-config [--config <file>]`: copy next to the file (0600) → `$VISUAL`, else `$EDITOR`, else `nano` → check; on problems list them and ask `(b)earbeiten oder (v)erwerfen?` → replace in one rename, owner and mode kept → `reload` through the control socket when the server runs → print the result.
- `fw-alarm check-state [--config <file>] [--list]`: exit 0 and `Zustand lesbar: <n> Empfänger, Listen-Version <v>.` only for a readable `state.json` (the previous version does not count); `--list`: one line per recipient — id, name, masked key, `stumm`/`dauerhaft` — never a key or token.
- `fw-alarm state-bump [--config <file>]`: takes `server.lock` (a running server → the sentence about the other program, exit 1), applies `Rewind("restore")`, saves, prints the new numbers.
- Hints of `health`: the emergency list of the configuration holds fewer keys than the recipient list; `check_interval_seconds` is 0; the lock could not be created.
- Frontend: `describeLogEntry` gets the two texts fixed above. The two visible texts that name `config.php` (`logic.js` "nicht konfiguriert (bark_key_status in config.php setzen)", `view-settings.js` "In config.php ist kein Status-Empfänger …") say `in der Konfiguration des Servers` instead — true for PHP and Go; Task 30 makes it `config.toml`.

- [ ] **Step 1: Tests.** `TestEditConfig` (with a fake editor script: valid change → replaced, mode and owner kept; broken change + "v" → file untouched; never a temp file left); `TestCheckState` (readable; only the previous version readable → exit 1; `--list` output holds no key and no token); `TestStateBump` (+1000, open command cancelled; refuses while a server holds the lock); in `tests/dashboard/logic-log.test.mjs` the two new events; adjust the two assertions on the changed sentences.
- [ ] **Step 2:** Implement. `node --test "tests/dashboard/*.test.mjs"` → pass; `node --test "tests/backend/*.test.mjs"` (PHP) → pass; `bash tools/test-go.sh` → pass; `bash tools/check-go.sh`.
- [ ] **Step 3:** Commit — `Go: Konfiguration sicher ändern, Zustand prüfen und anheben; zwei neue Verlaufs-Texte in der Oberfläche`.

### Task 20: One-time import of the PHP data

**Files:** Create `internal/phpimport/{read.go,recipients.go,runtime.go,config.go,run.go}` + tests, `tests/backend/fixtures/php-data/` (an artificial PHP data folder plus `config.json`), `internal/phpimport/testdata/expected/`; modify `cmd/fw-alarm/main.go`.

**Interfaces — produces:** `fw-alarm import-php --data <old folder> --config-json <file> --out-state <new folder> --out-config <config.toml> [--force] [--drop-recipient <id>]… [--drop-config-key <name>]… [--preflight]`.

- Everything of spec §9. In short: lenient reading only here; recipients with id, key, name, phone, mute state and time, volume, "dauerhaft", **mute token** — an entry that cannot be taken over one to one (invalid or duplicate key, invalid token, an 11th entry, a demo recipient that is not in the list) stops the import before anything is written, naming the entry without key and token; only `--drop-recipient <id>` lets one go. An entry without a token gets a new one (nothing existed that could break) and is named in the summary. Taken over as well: list version + 1, id high-water mark (the maximum of list and `alarm_keys_version.json`), demo mode, settings, notify settings (checked with `notify.CheckSetting`; an unknown value falls to the default and is named), commands (next id, current, recent types), last status, seen alarms and boards, markers of the checker, the outside watcher's sign of life, both history files line by line (each line gets a `uid`). Not taken over: sessions, brakes, `log_cache`, lock files, `.bak`.
- Configuration: every key of the JSON file must be known (`data_dir` and `health_check_url` are dropped silently, any other unknown key stops the import unless `--drop-config-key` names it); `bark_keys_alarm` becomes the keys of the imported recipient list; `state_dir`, `listen`, `control_socket` get their defaults; the result is written with `config.Write` and must load again to the same values.
- Holds `server.lock` of the new folder while it runs (a running server there → stop with the sentence about the other program). Never writes into the old folder. Refuses an existing new state unless `--force` (then `Rewind("import")`). List and `.bak` both unreadable → stop. `demo_mode.json` broken or missing despite a `.bak` → LIVE, named in the summary. Sets the sign of life to now and writes the rescue copy. Ends by reading its own result with `store.CheckState` and prints a summary: counts, what was adjusted and why — no key, no token.
- `--preflight` only reads and prints what forbids a switch (spec §12.5): demo mode on; a command pending or delivered and not acked; `alarm_pending=1` in the last status; a recipient whose mute is overdue. Exit 1 if any.

- [ ] **Step 1: The artificial folder** from what PHP wrote in practice: `alarm_keys.json` with entries of every era (no `mute_token`, no `muted_at`, `muted: 1`, `"mute_volume": "3"`, `mute_permanent` missing), `alarm_keys_version.json` above the list's numbers, `notify_settings.json` with `"global": []`, `login_throttle.json` as `[]`, `devices_seen.json` with numeric-looking ids, `command_state.json` with a delivered command and `recent_types`, `watchdog_state.json` with every key the checker ever wrote, `commands.log` and `commands.log.1` with every event plus shapes from before 11/2026 (no `demo`, a text starting `DEMO-MODUS`) and one broken line. Variant folders for the refusals.
- [ ] **Step 2: Tests.** `TestImportGolden` (folder → the expected `state.json`/`runtime.json`, compared after removing `uid`s and the sign of life); `TestImportRefusesLoss` (one case per reason; nothing written); `TestDropRecipient`; `TestImportNeverWritesOldFolder` (checksums before and after); `TestImportRefusesExistingState`, `TestForceRewinds`; `TestImportBrokenDemoBecomesLive`; `TestVersionPlusOne`; `TestEmergencyListRefreshed`; `TestHistoryLineByLine` (count equal, order kept, the broken line skipped and counted in the summary); `TestSummaryHasNoSecrets`; `TestConfigRoundTripOddStrings` (Review Focus 5: quotes, backslashes, umlauts, a line break, `#` and `=` in `bark_alarm_body`/`bark_alarm_title` → same values after `Write` and `Parse`); `TestUnknownConfigKeyStops`; `TestPreflight`; `FuzzLenientList`.
- [ ] **Step 3:** Implement. `bash tools/check-go.sh`. Commit — `Go: einmalige Übernahme der PHP-Daten – verliert nichts stillschweigend`.

### Task 21: Parity — PHP and Go side by side on the same data

**Files:** Create `tests/backend/parity.mjs`; modify `tests/backend/harness.mjs` (both drivers accept `options.seed`), `tests/backend/README.md`.

**Interfaces — produces:**
- `startBackend({ kind, seed })`: `kind` overrides `FW_BACKEND`; `seed` = `{ data: <PHP data folder>, configJson: <file> }`. PHP driver: copies the folder into its data folder and merges the JSON into its configuration. Go driver: runs `import-php` on the copy into its state folder and uses the written `config.toml`. Both replace, alike: `bark_host` (their own mock), the token hashes and the dashboard accounts by the test values — and print the names of the replaced keys.
- `node tests/backend/parity.mjs [--data <folder> --config-json <file>]` (default: the fixture of Task 20): starts both, sends the same request list to both, compares per request the answer (status, body) and what each server sent to its Bark mock (`key`, `fields`; the time stamp in `body` masked), prints a diff per difference, exit 1 on any. It refuses to start unless both effective `bark_host` values are `http://127.0.0.1:<own mock port>`. Normalisation: drop `server_time`, `csrf`, `seen_age_seconds`, cookie values; sort object keys; mask `?v=`. `ALLOWED` in the script lists deliberate differences, each with its spec § (the version after the import is one higher; texts naming the configuration file; what §7 changes).

- [ ] **Step 1:** Request list: login as admin and reader; `status`, `keys_list`, `log_view` with all five filters and limits 1/25/100, `notify_list`, `settings_list`; box `poll`, `keys`, `status` (old and new firmware), `ack`; `alive`; shortcut link (text, browser, preview, unknown token); a form with odd field names (`a[]`, `a[b]`, a name with a dot, a name with a space) on the status push; a checker run in four prepared states; `alarm` and `false_alarm` in LIVE and in demo mode with one failing recipient; `keys_test`; a resend with `only_ids`.
- [ ] **Step 2:** `node tests/backend/parity.mjs` → no difference outside `ALLOWED`. Every difference is decided: Go wrong → fix Go (with a test in the owning package); a PHP quirk the spec gives up → `ALLOWED` with its §.
- [ ] **Step 3 (controller): the real data, on the PC only.** Take the newest archive in `~/fw-alarm-backups` (no contact with the VM), extract it into a fresh folder with mode 0700 in the job's temp folder, write `config.json` from its `config.php` with `php -r`, run `node tests/backend/parity.mjs --data … --config-json …`. Expected: no difference outside `ALLOWED`, and the import's summary names no refusal. Then delete the folder and confirm it is gone. Nothing from it is ever committed; what the import had to adjust goes into the ledger without names, keys or tokens.
- [ ] **Step 4:** Commit — `Go: Gleichlauf mit der PHP-Fassung auf übernommenen Daten ohne Abweichung`.

### Task 22: Browser scenarios, dashboard tests and wording on Go

**Files:** Modify `tests/dashboard/wording.test.mjs`, `tests/dashboard/e2e/README.md`; scenarios only through the divergence protocol.

- [ ] **Step 1:** `bash tools/test-go.sh` builds; then `FW_BACKEND=go FW_GO_BIN=<abs> node tests/dashboard/e2e/run.mjs <name>` for `smoke shell overview recipients log settings switch` → `OK` seven times. A failure is a difference between PHP and Go: fix Go, not the scenario.
- [ ] **Step 2:** `wording.test.mjs`: the place-neutral rules ("Server" in visible texts, "NAS" only for the outside watcher, "DSM" only there) also read the Go sources (`internal/**/*.go` and the templates). The two tests that execute PHP functions stay until Task 30. `node --test "tests/dashboard/*.test.mjs"` → pass.
- [ ] **Step 3 (controller):** compare the screenshots the scenarios write for PHP and for Go (overview, recipients, log, settings, login) — same pages. Anything that differs is fixed in Go or goes to the user.
- [ ] **Step 4:** Commit — `Go: Oberfläche läuft unverändert – alle Chrome-Szenarien grün`.

### Task 23: Checks on GitHub

**Files:** Create `.github/workflows/ci.yml`; modify `tools/check-go.sh` (add `GOOS=linux GOARCH=arm64 go vet ./...` and `go build` — the Linux-only files at least compile on the PC), `README.md` (one sentence), `CLAUDE.md`.

- Triggers: push to `main`, weekly (`cron: '17 4 * * 1'`), `workflow_dispatch`. `permissions: contents: read`. `concurrency` per ref, cancelling older runs. No secrets, no deploy, no artifact upload.
- Jobs on `ubuntu-24.04`: **go** — `gofmt -l` prints nothing, `go vet ./...`, `staticcheck ./...`, `govulncheck ./...`, `go test -race -mod=vendor ./...`, `go build` for `linux/arm64` and `windows/amd64`; **server-tests** — Node 24, `bash tools/test-go.sh`; **dashboard** — `node --test "tests/dashboard/*.test.mjs" "tests/server/*.test.mjs" "tests/watcher/*.test.mjs"`.
- Every action pinned by its full commit hash with the version as a comment (resolve with `gh api repos/<owner>/<repo>/git/ref/tags/<tag>`); Go from `go.mod` (`go-version-file`); `staticcheck` and `govulncheck` at fixed versions.
- CI is the first place the Linux-only files (`*_unix.go`) run and — unless Task 4 Step 0 succeeded — the only place the race detector runs. Its first run happens with the first push (Task 27); a green run is a condition for the trial and for the switch.

- [ ] **Step 1:** Write the workflow. It cannot run before the first push, so check what can be checked: every `run:` line of the three jobs is executed once by hand in Git Bash (except `-race` when the PC has no C compiler), and every pinned hash is looked up, not typed from memory.
- [ ] **Step 2:** Commit — `CI: GitHub prüft jeden Push – Go-Prüfungen mit Race-Detektor, Server-Tests gegen Go`.

### Task 24: Server files for the Go server

**Files:**
- Create: `server/go/systemd/fw-alarm.service`, `server/go/systemd/fw-alarm-ballast.service`, `server/go/setup.sh`, `server/go/deploy.sh`, `server/go/backup.sh`, `server/go/check.sh`, `server/go/cutover.sh`, `server/go/rollback.sh`, `server/go/cloudflared/config.yml.example`, `tests/server/go-deploy.test.mjs`, `go-backup.test.mjs`, `go-cutover.test.mjs`
- Modify: `server/README.md` (new part "Go-Fassung")

Every script finds its own folder through `$(dirname "$0")` (the folder moves in Task 30), takes the VM's address from `FW_DEPLOY_HOST` or `server/deploy.local` as today, and uses a folder in the home directory on the VM, never `/tmp`.

`fw-alarm.service` (the trial in Task 27 confirms it with `systemd-analyze security`; a directive the trial proves wrong is corrected there):

```ini
[Unit]
Description=FW-Alarm Dashboard (fw-alarm serve)
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=notify
NotifyAccess=main
User=fwalarm
Group=fwalarm
ExecStart=/usr/local/bin/fw-alarm serve --config /etc/fw-alarm/config.toml
ExecStopPost=/usr/local/bin/fw-alarm report-failure --config /etc/fw-alarm/config.toml
Restart=always
RestartSec=1
RestartSteps=6
RestartMaxDelaySec=60
WatchdogSec=180
TimeoutStopSec=60
StateDirectory=fw-alarm
StateDirectoryMode=0700
RuntimeDirectory=fw-alarm
RuntimeDirectoryMode=0750
RuntimeDirectoryPreserve=yes
ConfigurationDirectory=fw-alarm
MemoryMax=256M
UMask=0077
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native
SystemCallFilter=@system-service
SystemCallErrorNumber=EPERM
CapabilityBoundingSet=
AmbientCapabilities=

[Install]
WantedBy=multi-user.target
```

`fw-alarm-ballast.service`: holds a fixed amount of memory (`Environment=BALLAST_MB=64`; a small script that allocates and touches it, then sleeps), `MemoryMax` a little above; installed, not enabled.

- `setup.sh` (on the VM as root, refuses to start from `/tmp`): installs both units and `/etc/fw-alarm` (root:fwalarm 0750), enables and starts nothing, touches neither Nginx nor PHP. With `--trial`: additionally a unit `fw-alarm-trial.service` — the same file with `fw-alarm-trial` as state, runtime and configuration folder.
- `deploy.sh <rev>` (spec §13): rev must be on `origin/main` → build from `git archive <rev>` in a temp folder with `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -mod=vendor -trimpath -buildvcs=false -ldflags "-s -w -X main.version=<sha>"` → `sha256sum` → `backup.sh` first (a failure: continue, exit 3 at the end) → upload into a fresh folder in the home directory → compare the checksum there → install as root-owned `/usr/local/bin/fw-alarm.new` (0755) → the NEW program runs `check-config` and `check-state` against the real files as user `fwalarm` (one fails → remove `.new`, exit 1; both skipped with `--install-only`, for the time before the switch) → keep the old program as `fw-alarm.prev` → `mv -f` in one step → `systemctl restart fw-alarm` → `fw-alarm health` and `fw-alarm version`; when `health` does not report a healthy server within 20 s: put `fw-alarm.prev` back, restart, exit 1. Last line: `deployed <sha>: fw-alarm sha256 <64 hex> ok`.
- `backup.sh`: on the VM `fw-alarm flush` and `fw-alarm check-state`, then fetch `/var/lib/fw-alarm` and `/etc/fw-alarm/config.toml` as one archive to `~/fw-alarm-backups` (the newest 30 kept; refuses a target inside the repo); an archive counts only after the check on the PC (readable, `state.json` strict-readable by a local `fw-alarm check-state` when the binary is there, `config.toml` present); a failed one deletes no older one.
- `check.sh local|public`: inside — `fw-alarm health`, owner and mode of `/var/lib/fw-alarm` (fwalarm, 0700), `/etc/fw-alarm/config.toml` (root:fwalarm 0640), `fw-alarm version` = deployed revision, unit active; outside — like the box (TLS 1.2, no `Accept`), without `Accept`, `/classic.php` 302, `/api/health.php` 404, static file types.
- `cutover.sh preflight|stop-timer|switch|stage <dir>` (on the VM as root): `preflight` — the last line of the PHP checker starts with `WEB_OK` and has no `STATE_WRITE_FAILED`, and `fw-alarm import-php --preflight` passes; `stop-timer` — stop and disable `fw-alarm-cron.timer`, wait for a running `fw-alarm-cron.service`; `switch` — stop Nginx and PHP-FPM → write `config.php` as JSON into a 0700 folder in root's home → `import-php` into `/var/lib/fw-alarm` and `/etc/fw-alarm/config.toml` (owners and modes as above) → `check-state` → delete the JSON file → `systemctl enable --now fw-alarm` → `fw-alarm health`. If any step before the start of `fw-alarm` fails: start Nginx and PHP-FPM again, say so, exit 1. `stage <dir>` — the same export and import into `<dir>` without touching any service (the trial).
- `rollback.sh` (spec §12.5, the evening of the switch only): `fw-alarm check-state --list` into a file in the home directory → `systemctl disable --now fw-alarm` → the port is free (`ss -ltn`) → start Nginx and PHP-FPM → wait (up to 5 min) for a status push of the box and a call of the outside watcher in the Nginx log → only then enable and start the timer → print what is left to do by hand (compare recipients and mute states with the saved list).
- `cloudflared/config.yml.example`: the variant with `service: unix:/run/fw-alarm/http.sock` for Task 30.

- [ ] **Step 1: Tests with doubles** for `ssh`, `go`, `sha256sum`, `systemctl`, `php`, `fw-alarm` (pattern of `tests/server/backup.test.mjs`). `go-deploy`: refuses a rev that is not on `origin/main`; a checksum mismatch leaves the old program; a failing `check-state` of the new program leaves the old one; an unhealthy server after the restart brings `fw-alarm.prev` back; exit 3 when the backup fails; never `/tmp`; the last line. `go-backup`: `flush` and `check-state` run before the fetch; an archive without `state.json` is no backup and deletes none; target inside the repo refused. `go-cutover`: order of `switch`; a failing import restarts Nginx and PHP-FPM and never starts `fw-alarm`; the JSON export is deleted in every outcome; `stage` calls no `systemctl`; `rollback` starts the timer only after both waits.
- [ ] **Step 2:** Implement; `bash -n` on every script; `node --test "tests/server/*.test.mjs"` → pass.
- [ ] **Step 3:** `server/README.md`, part "Go-Fassung", in plain words, required and optional told apart, exact commands: services and paths; changing the configuration (`sudo fw-alarm edit-config`); new password (`fw-alarm hash-password`); deploy, backup, **restoring a backup** (the six steps of spec §13 with `state-bump`); the switch and the way back (the commands of Task 28); a table "Meldung → wo nachsehen" with the new messages; what `fw-alarm health` says.
- [ ] **Step 4:** Commit — `Server: Dienst, Einspielen, Sichern, Prüfen und Umschalten für die Go-Fassung`.

### Task 25: Review of the whole and report

- [ ] **Step 1:** Everything once more from a clean state: `bash tools/check-go.sh`; `node --test "tests/backend/*.test.mjs"` (PHP); `bash tools/test-go.sh`; the seven scenarios on both; `node --test "tests/dashboard/*.test.mjs" "tests/server/*.test.mjs" "tests/bridge/*.test.mjs" "tests/watcher/*.test.mjs"`; `node tests/backend/parity.mjs`.
- [ ] **Step 2:** Walk "Rule → test": every row names a test that exists and passes; every case in `php-*.test.mjs` has a row. Walk `CLAUDE.md`: every rule spec §7 does not list is held by a shared test that is green on Go — list any rule without one and write its test.
- [ ] **Step 3:** Whole-branch review by a fresh reviewer on the most capable model, from commit `a5e5833` (the groundwork of 2026-11-17 included). Work its findings in.
- [ ] **Step 4:** `CLAUDE.md` ("Go-Fassung (im Bau)" → what exists, how to test it, what is still PHP), `tests/backend/README.md`, the spec (anything the build decided differently, with the reason), the memory notes. Report to the user: what is built, what the three gates below need from them.

---

## Phase C — Gates, trial, switch, cleanup (each needs the user)

### Task 26: Security review — the user's own session

- [ ] **Step 1:** Assign the user the task to start the review session (it waits for their "go"). Scope to hand over: sessions and cookie, login brake and password slots, proxy trust, machine and watcher token, the shortcut link (previews, brake, no token in redirect or log), static files (no way out of the embedded tree), size and time limits, what access log and journal can hold, the control socket, `report-failure`, the unit's confinement, `deploy.sh`/`cutover.sh` (no `/tmp`, checksum before install), the import (reads hostile-looking data), the CI workflow (pinned, read-only).
- [ ] **Step 2:** Fix every finding with a test in the style of the owning task; rerun Task 25 Step 1; one commit per finding.

### Task 27: Trial on the VM and the memory calculation — needs the user's go (it touches the live machine, not the live service)

- [ ] **Step 1:** Scan the commits for real addresses; `git push origin main`; wait for the CI run: green is the condition to go on.
- [ ] **Step 2:** Copy `server/go/` to `~/fw-server-go` on the VM; `sudo bash ~/fw-server-go/setup.sh --trial`; `bash server/go/deploy.sh --install-only HEAD` → the last line `deployed <sha>: …`.
- [ ] **Step 3:** `sudo bash ~/fw-server-go/cutover.sh stage /var/lib/fw-alarm-trial` (import of the real data, read only on the PHP side). In the trial configuration set `listen = "127.0.0.1:8082"`, `bark_host = "http://127.0.0.1:9"` (a closed port: nothing can leave), `control_socket = "/run/fw-alarm-trial/control.sock"`; `fw-alarm check-config`, `check-state --list` → the recipients as in the live dashboard.
- [ ] **Step 4:** What the PC cannot show: `systemctl start|stop|restart fw-alarm-trial` and `fw-alarm health`; `systemd-analyze security fw-alarm-trial`; the watchdog (`kill -STOP` the process → systemd ends and restarts it within about 3 min); the crash message (`kill -SEGV` → `report-failure` ran once, the marker exists, a second crash within 30 min sends nothing — seen in the journal, the request itself goes to the closed port); the Unix socket (`listen = "unix:/run/fw-alarm-trial/http.sock"`, `curl --unix-socket`, `cloudflared tunnel ingress validate` on the example file, which user `cloudflared` runs as); the way back as a dry run (stop → nothing listens on 8082); memory: an Argon2id hash for the reader in the trial configuration, 30 wrong logins at once → `MemoryPeak` below 256 M, no kill; a look at the dashboard through an SSH tunnel with the real account — trigger nothing that sends.
- [ ] **Step 5: Oracle's 20 % rule** (spec §11.11): share of used memory `(total − available) / total` from `free -m`, compared once with the console; calculation for after the cleanup: today's value − Nginx − PHP-FPM + `fw-alarm`. Try the ballast unit once. **Below 25 %: the ballast unit is part of the switch.** Numbers into `server/README.md`.
- [ ] **Step 6:** Remove the trial: stop and remove the trial unit, `/var/lib/fw-alarm-trial`, `/etc/fw-alarm-trial`, `/run/fw-alarm-trial`; nothing listens on 8082; the live checks (`bash server/check.sh public …`) still pass. Findings → fixes with tests → Step 1 again where code changed.

### Task 28: The switch — date and 30 minutes together with the user

Preconditions: Tasks 1–27 done; CI green on the revision to deploy; no flash of the box in the last 3 days and none planned for the next 3; not at the start or end of a working day; the user is there.

- [ ] **Step 1 (PC):** `bash server/backup.sh` (the PHP one) → ok. `bash server/go/deploy.sh --install-only HEAD`.
- [ ] **Step 2 (VM):** `sudo bash ~/fw-server-go/cutover.sh preflight` → passes (checker line `WEB_OK`, demo off, no alarm running, no open command, nobody "Evtl. stumm"). Otherwise: not today.
- [ ] **Step 3:** `cutover.sh stop-timer`.
- [ ] **Step 4 (NAS):** put the bridge on hold (`bridge_hold.php`, `nas_bridge/README.md`).
- [ ] **Step 5 (VM):** `cutover.sh switch` — from here until its last line there is no dashboard and no manual alarm (expected 3–5 min); the box alarms by itself as always. Then `sudo bash ~/fw-server-go/check.sh local` and (PC) `bash server/go/check.sh public https://alarm.tunero.dev` → only `PASS`.
- [ ] **Step 6 (NAS):** lift the hold; replay the mute calls it noted. Look into the journal of `cloudflared` for failed `api/mute.php` calls during the gap and follow up.
- [ ] **Step 7: Harmless checks only:** the box shows online (journal: `POST /api/status.php` 200 `bridge=1`), the TEST command arrives, the user's own shortcut link on and off, `fw-alarm check` prints a `WEB_OK` line; everyone logs in again. Then together: demo mode on the user, **one alarm and one Entwarnung to them alone**, back to LIVE.
- [ ] **Step 8:** If a check fails and cannot be fixed on the spot: `sudo bash ~/fw-server-go/rollback.sh` — this evening only.
- [ ] **Step 9:** `CLAUDE.md` ("Wo was läuft": Go serves; deploy = `server/go/deploy.sh`), `server/README.md`, memory notes. Commit, push.

### Task 29: Three quiet days

- [ ] Daily: `fw-alarm health`; the journal has no status 5xx (`journalctl -u fw-alarm --since yesterday | grep -c '" 5[0-9][0-9] '` is 0); the check lines hold no `SEND_FAILED`/`WEB_FAIL`; the NAS watcher's log shows `cron_age` below 900; the share of used memory (ballast on when below 25 %); after a restart of the VM: `systemctl is-active fw-alarm cloudflared`. The first morning: the box's daily heartbeat arrived and the user is still logged in on the iPhone. A fault found now is fixed in Go and deployed with `server/go/deploy.sh`.

### Task 30: Cleanup — PHP and Nginx leave (the user confirms the deletions)

**Files:**
- Delete: every `*.php` under `nas_dashboard/` and its `SETUP.md`, `data/`; `server/nginx/`, `server/php/`, `server/logrotate/`, `server/systemd/`, `server/setup.sh`, `deploy.sh`, `backup.sh`, `check.sh`; `tests/backend/php-*.test.mjs`, the PHP driver in `harness.mjs`, `tests/backend/parity.mjs`, `tests/backend/fixtures/php-data/` (it stays as input of nothing: `internal/phpimport/` goes too), `tests/server/backup.test.mjs` and the PHP deploy case; `import-php` in `main.go`; the two PHP-executing tests in `wording.test.mjs`
- Move: `server/go/*` → `server/`; `nas_dashboard/` → `web/` (`webassets.go`, every path in tests and docs)
- Modify: `tests/backend/harness.mjs` (default driver `go`), `tests/dashboard/wording.test.mjs` (Go sources only), the two frontend texts → `config.toml`, `cloud_watcher/` texts that name `config.php`, `check_offline.php` or the timer (wording only; `tests/watcher/` with them), `.github/workflows/ci.yml` (paths), `CLAUDE.md` (rewritten: "Wo was läuft", "Architektur", test commands — the rules stay), `README.md`, `server/README.md`, `tests/backend/README.md`, `nas_bridge/README.md` (wording only), spec status line

- [ ] **Step 1 (repo):** make the changes; `bash tools/check-go.sh`, `bash tools/test-go.sh`, the seven scenarios, all `node --test` suites → pass; `grep -rn "config\.php\|php-fpm\|nginx" --include='*.go' --include='*.js' --include='*.sh' --include='*.md' .` leaves only history. Commit, push, CI green, `bash server/deploy.sh HEAD`.
- [ ] **Step 2 (VM): the Unix socket** — its own step with its own check: `listen = "unix:/run/fw-alarm/http.sock"` through `fw-alarm edit-config`, the socket variant of `cloudflared`'s `config.yml` (the old file kept next to it), restart both, `bash server/check.sh public …` → only `PASS`; the way back is the old `config.yml` and the old `listen`.
- [ ] **Step 3 (VM):** a last backup of the old data; list exactly what goes and that it cannot be undone — the user confirms; then `apt-get purge` Nginx and PHP, remove `/srv/fw-alarm`, the old timer units, the Nginx and PHP logs.
- [ ] **Step 4:** memory share measured and recorded; fresh `bash server/backup.sh`; memory notes; the passkey plan is next (rewritten for Go before it starts).

