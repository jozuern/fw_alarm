# Go Server (Neubau) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the dashboard server (PHP 8.3 behind Nginx and PHP-FPM, plus the watchdog script on a timer) by one Go program `fw-alarm` that is built new inside and behaves the same outside — proven by the black-box tests in `tests/backend/` — and switch the Oracle VM over after a one-time import of the existing data.

**Architecture:** One process owns its state in memory as immutable typed snapshots behind atomic pointers. One writer at a time changes the durable part (`state.json`, saved and read back before it counts); volatile data, sessions and the history are written behind the answer. Alarm, box and login only ever read memory, so a dead disk stops none of them. The checker runs inside the server (full run every 15 min, "box offline" every minute); a local control socket serves `check`, `health`, `flush`, `reload`, `stop`. The frontend under `nas_dashboard/assets/` is embedded unchanged.

**Tech Stack:** Go 1.27 (pinned in `go.mod`, `CGO_ENABLED=0`, `windows/amd64` for local tests, `linux/arm64` for the VM), standard library plus four vendored modules: `golang.org/x/crypto` (Argon2id, bcrypt), `golang.org/x/sys`, `golang.org/x/term`, `github.com/BurntSushi/toml`. Tests: Go unit and fuzz tests, Node 24 `node --test` for the black-box suite, headless Chrome over CDP for the browser scenarios, PHP 8.3 only as the reference until Task 37. CI: GitHub Actions from Task 4 on.

**Spec:** `docs/superpowers/specs/2026-11-17-go-server-neubau-design.md` (German; § numbers below refer to it; its §17 lists what the review of this plan changed). Read it and `CLAUDE.md` before any task. `CLAUDE.md` lists the rules that must survive; spec §7 lists the ones that change or go; the tests are how both are checked. The PHP source under `nas_dashboard/` is the reference for every text and every rule the spec does not change — read the PHP function a task names before writing its Go counterpart.

## Global Constraints

- **No test can reach a real phone** (user, 2026-11-17: "under no circumstance can a real alarm trigger to ANYONE but me"). Four rules, each enough on its own: (1) **Real Bark keys exist only on the live server.** On the PC and on GitHub the keys are invented and `bark_host` is a mock on `127.0.0.1`; every COPY of live data (parity run on the PC, trial on the VM) has all Bark keys replaced by invented ones before any server starts on it, and the replacing step proves that none is left. (2) The trial on the VM additionally cannot reach Bark at all (closed port, enforced by the unit, plus a network rule of the unit). (3) **The live Go server starts under the send lock** (`send_guard_*`, Task 8): for the first 90 minutes it can send to no key but the admin's own — whatever the rest of the program decides. (4) On the live system nothing that sends is ever triggered except the one joint demo alarm in Task 35, and only after the server itself has confirmed that an alarm would reach exactly one person: the user. Helpers never touch the VM or the NAS, never push, and send nothing outward except what the Go tools fetch themselves (`go mod` downloads, the vulnerability database of `govulncheck`); the controller does everything else outward.
- **Untouched:** `boss925_alarm_bark/`, `platformio.ini`, `nas_bridge/`, `cloud_watcher/` (wording of the outside watcher only in Task 37). The frontend `nas_dashboard/assets/` changes only where a task names the file.
- **Addresses** (spec §4.1): `/`, `/index.php`, `/dashboard_api.php`, `/api/{status,poll,ack,keys,mute,alive}.php`, `/manifest.json`, `/assets/…`; `/classic.php` → 302 `index.php`; everything else 404, also `/api/health.php`. Every address decides about the method itself (a GET on `api/poll.php` is 403 `FORBIDDEN`, never 405). The server never redirects a non-canonical path.
- **Box protocol:** plain-text lines, nonce mirrored, token only from the POST body, wrong token = status 403 with body exactly `FORBIDDEN` and for nothing else, commands at most once, numbers monotone. Answers to the box and to the shortcut link never wait for Bark, and for the disk never beyond the deadline.
- **Deadlines for durable changes, per request, queue included** (spec §6.2): box (poll, ack, `api/keys.php`) 1.5 s, shortcut link 2 s, dashboard 5 s. Not begun at the deadline = dropped; finished late = it counts. A durable change made beside a request (after the answer, in the checker) has the 5 s budget too — none waits without bound.
- **No Bark request inside a change** (`Store.Update` callbacks are pure: no network, no file access, no clock other than the `now` passed in) and no slow work while the writer slot is held.
- **Reads never touch the disk:** no request handler opens a file — state, configuration, history and assets all come from memory.
- **Strict inside:** typed documents; unknown fields, missing fields, wrong types, `null` where a value belongs, a key in other case, a duplicate key and data after the document are errors; every file carries `schema`. (So a field added later needs a schema bump and a migration.) Only `history.jsonl`, the rescue copy and the import read leniently.
- **Texts:** every message, Bark text (ASCII: ae/oe/ue/ss) and page is copied verbatim from the PHP source. Exceptions are exactly the texts this plan spells out (spec §7): texts naming PHP, Nginx or `config.php` say `config.toml` / `systemctl status fw-alarm`; texts describing behavior that §7 changes.
- **Event names and fields of the history stay** (`nas_alarm`, `nas_web_failed` …), also where "nas" is historical.
- **Secrets** (machine and watcher token, passwords, session ids, CSRF tokens, mute tokens) are compared in constant time, come from `crypto/rand`, and never appear in a log line, an error text, a redirect or any URL the server builds. Bark keys only masked. Every request has a size limit (64 kB body) and time limits. No file path is built from request input. HTML only through `html/template`. No `unsafe-inline`.
- **Dependencies:** standard library and the four modules above, vendored (`go mod vendor`, builds use `-mod=vendor`; `golang.org/x/sys` alone is about 9 MB — accepted, spec §15.7). No CGO in the product. Development tools (`staticcheck`, `govulncheck`) are installed with `go install`, never module dependencies. No test hooks in the program: what is tested from outside is what ships.
- **Code style:** identifiers in English, comments in German with real umlauts, UTF-8. `gofmt` clean. No error dropped without a comment saying why. Everything the program starts on the side (per-recipient send, check run, writers, work after the answer) recovers from a panic, logs it without request data and ends for itself; the process exits only in `main`.
- **Checks in every Go task:** `bash tools/check-go.sh` (gofmt, `go vet` for the PC and for `linux/arm64`, `staticcheck`, `govulncheck`, `go test ./...`) and `bash tools/test-go.sh` (the black-box files that are green so far, see "Which test file turns green when"). Everything that parses outside input gets a Go fuzz test; its seed corpus runs in every `go test`, `go test -fuzz` runs 30 s once in the task that creates it. **The race detector and every Linux-only file run on GitHub**, after the push that follows each task: a red run is fixed before the next task starts.
- **Tests are the contract.** A shared test file under `tests/backend/` or `tests/dashboard/` is not edited to make Go pass, except where a task names the edit. **Divergence protocol:** when a shared case is red on Go because spec §6/§7 changes the rule, do not bend it — move the case unchanged to a `php-*.test.mjs` file, write its Go twin into a `go-*.test.mjs` file, and add a row to "Rule → test" below. A case moves as a whole even when only one of its assertions diverges — so **the Go twin carries every assertion of the moved case that still holds**, and replaces only the diverging one. A divergence the spec does not name is a finding: stop and report.
- **Old and new firmware:** every server rule is tested with `box().status()` and `box().oldStatus()`.
- **The repo is public:** no real host names or addresses in tracked files. Copies of live data or configuration live only in a folder with mode 0700 outside the repo and outside any cloud folder, and are deleted after use.
- **On the VM** nothing root executes and nothing secret passes through `/tmp`; use a folder in the home directory (0700). Oracle stays Always Free.
- **Line endings** (`.gitattributes`, Task 4): LF for `*.go`, `go.mod`, `go.sum`, `tools/*.sh`, `.github/**`, `server/**`, `internal/**/templates/**`, `nas_dashboard/assets/app/**`, `nas_dashboard/assets/app.css`, `nas_dashboard/manifest.json`; bytes untouched (`-text`) for `vendor/**`, `**/testdata/**`, `tests/backend/fixtures/**`, `nas_dashboard/assets/vendor/*`. A golden file compares bytes — it must be the same on the PC and on GitHub.
- **Git:** commits directly on `main`, one per task (plus fix commits), German messages with umlauts, ending with the attribution lines of the session reminder. **After each finished task the controller pushes** (`git push origin main` by name only, after the scan for real addresses) — decided by the user on 2026-11-17 so that GitHub checks every task. A push deploys nothing.
- **Docs stay current:** a task that makes a sentence in `CLAUDE.md`, a README or the spec untrue corrects it in the same commit. Unused files go.
- **Spacing:** the switch (Task 35) not within 3 days of a flash of the box; cleanup (Task 37) after 3 quiet days; passkeys only after Task 37.

## Review Focus

Failure modes the spec implies but states no test for, most likely first. Each is pinned to the task that owns the code.

1. **The VM reboots between the switch and the cleanup** (security updates restart it at 04:30). Nginx and PHP-FPM are still installed; if they start, PHP answers again on data frozen at the switch — an alarm to an old recipient list. Expected: both are disabled and masked at the switch, and the checks fail loudly if either runs or if anything but `fw-alarm` holds the port. Tests: Task 31 `go-cutover` "switch disables and masks", Task 30 `check.sh` case.
2. **The first save after a fallback start, or the first save ever:** `state.json` does not exist at that moment. Expected: the previous version is never removed unless a readable `state.json` exists; after any single failing step a restart finds the state again and never calls it a new installation. Tests: Task 6 `TestStateNeverMissing` (three starting points), `TestFreshOnlyWithoutTraces`.
3. **Bark cannot be reached when the server starts** (after a crash or reboot DNS may not be up). The one-time messages about the server's own faults arise exactly then. Expected: they stay pending and go out once, at a later attempt. Test: Task 23 `TestPendingMessagesSurviveFailedSend`.
4. **The configuration checker meets a half-written `config.toml`** (a plain editor writes in place). Expected: no session ends and no warning goes out for a file that is fine two seconds later. Test: Task 9 `TestProviderIgnoresHalfWrittenFile`.
5. **The state folder cannot be written at start** (rights lost after an update — the incident of 10/2026): not even `server.lock` can be created. Expected: the server still starts, serves alarm, box and login from what it could read, and reports the fault; it refuses to start only when ANOTHER process holds the lock. Test: Task 6 `TestOpenOnUnwritableDir`.

Further failure modes with a named test in their task: a torn history line and a full disk (Task 7), bytes that are not UTF-8 (Tasks 7, 11, 17), a jumping wall clock (Tasks 12, 21), a send lock that is forgotten or mistyped (Tasks 4, 8), odd strings on the way from `config.php` to `config.toml` (Tasks 4, 27).

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
| `internal/core/` | `Core`: what every handler needs (config, store, Bark, clock, background group, logger) |
| `internal/notify/` | kinds and settings of admin messages, quiet hours, sending |
| `internal/mutes/` | overdue mutes back to loud, with the messages that follow — called by box, link, dashboard and checker |
| `internal/logview/` | table of history events, filters, output per role |
| `internal/web/` | route table, forms, security headers, proxy trust, brakes, sessions, login, pages, static files |
| `internal/box/` | `api/status.php`, `poll.php`, `ack.php`, `keys.php` |
| `internal/mutelink/` | `api/mute.php` |
| `internal/dashapi/` | `dashboard_api.php`: action table, all actions |
| `internal/alive/` | `api/alive.php` |
| `internal/watch/` | full run, minute tick, schedule, texts, weekly report, messages about the server's own faults |
| `internal/control/` | control socket: server and client |
| `internal/app/` | wiring and life cycle of `serve`; the other subcommands; systemd |
| `internal/phpimport/` | one-time import of the PHP data (removed in Task 37) |
| `nas_dashboard/webassets.go` | `package webassets`: embeds `assets/` and `manifest.json` |
| `tools/check-go.sh`, `tools/test-go.sh`, `tools/go-green.txt` | the Go checks; the black-box suite against Go; the files that are green so far |
| `.github/workflows/ci.yml` | checks on every push and weekly |
| `tests/backend/harness.mjs` | + intent helpers, + Go driver |
| `tests/backend/php-*.test.mjs` | rules of PHP's storage only; run on PHP only; go with PHP |
| `tests/backend/go-*.test.mjs` | what only the Go server can be asked; run on Go only |
| `tests/backend/parity.mjs`, `tests/backend/fixtures/php-data/` | PHP and Go side by side on the same imported data (removed in Task 37) |
| `server/go/` | unit, ballast unit, config template, `setup.sh`, `deploy.sh`, `backup.sh`, `check.sh`, `cutover.sh`, `rollback.sh` for the Go server; moves up to `server/` in Task 37 |

Import direction (no cycles; `go build ./...` proves it): `clock`, `bg`, `textde`, `model` import nothing of ours → `config` → `store` (model, clock) → `bark` (config, textde) → `core` (config, store, bark, clock, bg) → `notify`, `logview` → `mutes` → `web` → `box`, `mutelink`, `dashapi`, `alive`, `watch`, `control` → `app` → `cmd/fw-alarm`. Among the handler packages: `box`, `mutelink`, `alive` and `watch` may import `dashapi` (to register parts of the `status` answer, and `watch` to ask `RecipientsNow`); `dashapi` imports none of them. `phpimport` imports `model`, `store`, `config`, `notify` only.

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

// internal/mutes
func Expire(ctx context.Context, c *core.Core) error   // überfällige Stummschaltungen zurück auf laut; Meldungen danach über Core.Bg
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

`keys_version` and `next_id` are their own high-water marks: inside one document they never go down, and when the document itself goes back the rewind rule applies (Task 5). `settings.mute_max_seconds` 0 = the value of the configuration. `notify` holds only what the admin set explicitly, each value as the choice key in text form. `commands.current` carries exactly the fields PHP writes in `api/poll.php`, `api/ack.php` and `dashboard_api.php`: `id`, `type`, `state`, `created_at`, `created_by`, `demo_changed_at`, `delivered_at`, `delivered_to`, `acked_at`, `acked_by`, `ack_result`, `ack_message`, `cancelled_at`, `cancelled_by`, `cancel_reason`, `expired_at` (Task 5 confirms the list against those files). `recent_types` at most 20, ordered by id.

`runtime.json` (volatile):

```json
{
  "schema": 1,
  "lifebeat_at": 0,
  "silence_from": 0,
  "box": null,
  "alarms_seen": {},
  "devices": {},
  "watcher": null,
  "watch": {},
  "throttle": { "login": {}, "mute": {} },
  "outbox": []
}
```

`box` = `{ "seen_at", "via_bridge", "remote_addr", "fields": [["device_id","fw-box-1"], …] }` — the fields as an ordered list of pairs (first 40, as pushed). `alarms_seen[device_id]` and `devices[device_id]` as PHP's `alarm_seen.json` / `devices_seen.json`. `watcher` = `{ "seen_at", "bark_failed_at" }`. `silence_from`: set at a start after an absence (Task 21); silence of box and outside watcher counts from the later of their last sign and this. `watch` holds the markers of the checker under PHP's names from `watchdog_state.json` (`last_run_at`, `offline_notified`, `offline_since`, `offline_seen_at`, `offline_reminded_at`, `offline_push_pending`, `web_fail_count`, `web_fail_since`, `web_fail_notified_at`, `web_push_pending`, `watcher_watch_since`, `watcher_silent_seen_at`, `watcher_notified_at`, `watcher_push_pending`, `demo_reminded_at`, `demo_reminded_for`, `mute_reminded_at`, `permanent_mute_reminded_at`, `health_bad_runs`, `health_notified_at`, `weekly_report_at`, `last_send_ok_at`, `last_send_failed_at`) plus new ones: `last_run_line`, `retry_after` (map kind → time), `emergency_notified_at`, `config_broken_notified_at`, `pending` (list of one-time messages still to send: `{ "id", "title", "body", "since" }`). `throttle.<bucket>[who]` = `{ "count", "first_at" }`.

`sessions.json`: `{ "schema": 1, "sessions": { "<sha256 hex of the session id>": { "user", "role", "csrf", "auth_stamp", "created_at", "last_seen" } } }`.

`history.jsonl`: one JSON object per line, the fields PHP writes (`event`, `log_at`, …) plus `uid` (16 random bytes, hex). Rotation above 5 MB to `history.1.jsonl`.

`recipients.rescue.json`: `{ "schema": 1, "written_at": 0, "demo_key_id": 0, "recipients": [ { "id", "key", "label", "muted", "muted_at", "mute_volume", "mute_permanent" } ] }`.

Temporary names are fixed: `state.json.tmp`, `runtime.json.tmp`, `sessions.json.tmp`, `recipients.rescue.json.tmp`, `selftest.tmp`. The test bench makes a save fail by putting a non-empty folder at such a name.

## Texts this plan fixes

Bark texts (ASCII). Each body is preceded by the time stamp `textde.Stamp` the way PHP's checker does it for its own messages.

| Occasion | Title | Body |
|---|---|---|
| self-test fails in 2 full runs | `Dashboard DEFEKT` | `Speichern scheitert (Schritt: <step>). Weiter laufen: Alarm an alle, Status der Box, Anmeldung. NICHT mehr: Aenderungen (Empfaenger, Stumm/Laut, Einstellungen) werden nicht gespeichert, Befehle an die Box nicht ausgeliefert. Nachsehen: journalctl -u fw-alarm, Platz und Rechte von /var/lib/fw-alarm.` |
| emergency mode | `Dashboard NOTBETRIEB` | `Empfaengerliste nicht lesbar. Ein Alarm ginge jetzt an <n> Empfaenger (<Not-Kopie des Servers\|Not-Liste der Konfiguration>). Die Box behaelt ihre gespeicherte Liste<, zuletzt war Demo-Modus: evtl. nur den Demo-Empfaenger>. Ausweg: Sicherung zurueckspielen (server/README.md).` |
| rewind | `Dashboard: Stand zurueckgesetzt` | `Gestartet aus <der Vorfassung (state.prev.json)\|einer zurueckgespielten Sicherung\|einer erneuten Uebernahme>. Die letzte Aenderung kann fehlen - bitte Empfaenger und Stumm/Laut pruefen.` |
| `runtime.json` unreadable | `Dashboard: Laufzeitdaten verworfen` | `runtime.json war nicht lesbar. Die Box gilt bis zu ihrem naechsten Status als nie gesehen.` |
| `sessions.json` unreadable | `Dashboard: Laufzeitdaten verworfen` | `sessions.json war nicht lesbar. Alle melden sich neu an.` |
| configuration broken | `Dashboard: Konfiguration fehlerhaft` | `config.toml ist fehlerhaft - es gilt die letzte gueltige Fassung, alle Sitzungen wurden beendet. Den Fehler nennt: sudo fw-alarm check-config` |
| interrupted send | `Dashboard: Versand unterbrochen` | `<Ein Alarm\|Eine Entwarnung> von <HH:MM> lief beim Neustart des Servers - Ergebnis unbekannt. Bitte den Verlauf pruefen und ggf. erneut senden. Der Server sendet nicht von selbst nach.` |
| crash (`report-failure`) | `Dashboard-Dienst abgestuerzt` | `systemd startet ihn neu. Nachsehen: journalctl -u fw-alarm -n 50` |

Answers of the dashboard API:

| Occasion | Text |
|---|---|
| saving fails (`ErrStorage`) | `Fehler: Datenordner nicht beschreibbar (Rechte des Web-Benutzers auf dem Server prüfen).` (as today) |
| deadline passed (`ErrLate`, `ErrDropped`) | `Fehler: Speichern dauert zu lange – bitte den angezeigten Stand prüfen.` |
| a change in emergency mode (`ErrEmergency`) | `Fehler: Notbetrieb – die Empfängerliste ist nicht lesbar, Änderungen sind gerade nicht möglich. Ausweg: Sicherung zurückspielen (server/README.md).` |
| `keys_list` in emergency mode (`code: keys_corrupt`) | `Empfängerliste nicht lesbar (Notbetrieb). Ein Alarm geht jetzt an die Not-Kopie des Servers bzw. die Not-Liste aus config.toml. Ausweg: Sicherung zurückspielen (server/README.md).` |
| login queue full (status 503) | `Gerade zu viele Anmeldungen gleichzeitig – bitte in ein paar Sekunden noch einmal versuchen.` |
| alarm or Entwarnung while the send lock holds some recipients back (put in front of the usual message) | `Probe-Sperre aktiv bis <HH:MM> – gesendet wurde nur an freigegebene Keys. ` |
| per recipient held back by the send lock (`err` of the result) | `Probe-Sperre: nicht gesendet` |
| hint of the kind `nas_web` in the settings | `(Änderungen werden dann nicht gespeichert, Befehle an die Box nicht ausgeliefert)` |
| reason of a command the rewind rule cancels (`cancel_reason`, shown as "… abgebrochen – …") | `Stand des Servers zurückgesetzt – nicht ausgeliefert.` |

Frontend (`assets/app/logic.js`, changed in Task 26; true for PHP and Go alike): `describeLogEntry` for `state_rewound` → `Server: Stand zurückgesetzt – die letzte Änderung kann fehlen`, for `send_interrupted` → `Versand unterbrochen (Neustart des Servers) – Ergebnis unbekannt`. The texts that say "Wächter-Timer" say "Wächter" instead: `noch nie gelaufen – läuft der Wächter auf dem Server?`, `seit <Dauer> nicht gelaufen – Wächter auf dem Server prüfen!`, and in the two tips `Der Wächter auf dem Server prüft alle 15 Minuten …` / `… oder wenn der Wächter dort nicht mehr läuft`. The two texts naming `config.php` say `in der Konfiguration des Servers` (Task 37: `config.toml`).

## Which test file turns green when

`tools/go-green.txt` lists the `tests/backend/` files that are completely green on Go, one per line; `bash tools/test-go.sh` without arguments runs exactly those (CI does the same), with arguments the named files. A task adds its files to the list in its own commit; a file on the list stays green. Task 25 removes the list — from then on the script runs everything.

| After task | On the list | Partly green (name the still-red cases in the commit message) |
|---|---|---|
| 10 | `go-driver` | |
| 13 | + `session`, `go-session` | `dashboard-access` except the case about `offline_alert`/`web_alert` (→ 21) |
| 14 | + `dashboard-log` | |
| 15 | + `dashboard-notify` | |
| 16 | + `dashboard-settings` | `dashboard-keys` except its `enqueue` case (→ 18) and its shortcut-link case (→ 19); `box-keys` except cases that poll (→ 18) |
| 17 | + `box-status`, `go-box`, `go-storage` | |
| 18 | + `box-poll-ack`, `box-keys` | |
| 19 | + `mute-link`, `dashboard-keys` | |
| 20 | + `alarm-send`, `go-guard` | |
| 21 | + `alive`, `dashboard-access` | |
| 22 | + `watchdog`, `go-watch` | |
| 25 | everything (the list goes) | |
| 29 | the seven browser scenarios, `tests/dashboard/*.test.mjs` | |

## Rule → test (everything spec §7 changes or removes)

"PHP only" names where today's case goes in Tasks 1–2; "Go" names the test that holds the new rule. Tasks add rows here when the divergence protocol applies.

| Rule today (PHP) | PHP only | Go |
|---|---|---|
| guard files, `data_path`, `fix_owner` | — | Task 9 `TestOnlyTableAddressesAnswer` (state files are reachable by no address) |
| `.bak` fallback of the list | `php-storage`: the cases of "Kaputte Liste: Rückgriff auf die Sicherung", "nur die Liste kaputt: es gilt die Sicherung" | Task 6 `TestStartTable`; Task 25 `go-restart`: "kaputte Zustandsdatei: Rückfall mit Rücksprung, dann Notbetrieb" |
| version/ids never reused after a fallback | `php-storage`: "nach dem Rückgriff … NEUE Version und id" | Task 5 `TestRewind`; Task 25 `go-restart` (same case: +1000) |
| box-reported higher version counts at the next change | `php-storage`: "nach Verlust des Datenordners zählt die Version weiter …" | Task 17 `go-box`: "meldet die Box eine höhere Listen-Version, hebt der Server seine sofort darüber" |
| hand-spoiled list: valid, unique, first 10 are alarmed | `php-storage`: the "von Hand verdorbene Liste"/"nur aus ungültigen Keys" cases, `box-keys` "Bereinigung" and "ein unplausibler Demo-Key alarmiert lieber alle als niemanden" (a demo file spoiled by hand) | Task 5 `TestInvalidListCannotBeSaved`; Task 6 `TestRescueReadIsLenient` |
| config list as the only net | `php-storage`: "Liste UND Sicherung unlesbar: ebenfalls die Not-Liste" | Task 20 `TestEmergencyRecipients`, `go-storage`: "Notbetrieb: Alarm geht an die Not-Kopie, ohne sie an die Not-Liste" |
| list and backup unreadable: `keys_list`, poll, `api/keys.php`, the shortcut link (500, not counted) | `php-storage`: those cases of `box-keys` and `mute-link` | Task 16 `TestKeysListEmergency`; Task 19 `TestLinkInEmergencyMode`; Task 25 `go-restart` |
| `demo_set` order, rollback, `inconsistent`, `repair_broken_demo_mode` | `php-storage`: demo file corrupt/removed (poll, checker) | Task 16 `TestDemoSetIsOneChange`; Task 27 `TestImportBrokenDemoBecomesLive` |
| lock files and their order | `php-storage`: per-lock cases that do not fit `storage.fail('durable')` | Task 7 `TestOneWriterAtATime` |
| status push: 500 when the folder is stuck | `php-storage`: `status.lock` / `latest_status.json` blocked | Task 17 `go-storage`: "Status wird angenommen, wenn nichts gesichert werden kann" |
| poll with a stuck folder: 500 or `type=NONE` | `php-storage` | Task 18 `TestPollDeliversOnlyWhenSaved`, `go-storage`: "Poll liefert ohne Speichern keinen Befehl aus" |
| `alive.php`: `stored=0` on the call whose save failed | `php-storage`: `alive` "Speichern scheitert" | Task 21 `go-storage`: "`stored` meldet das letzte Sichern" (`stored` = result of the last save of the volatile data, so 0 appears from the call AFTER the failed save) |
| one sentence for storage errors | — | Task 7 `TestUpdateDeadlines`; Task 16 `TestStorageSentences` |
| `api/health.php`, steps, `inplace_data_files`, `blocks_pushes`, `health_check_url` | `php-health` (the whole of today's `health.test.mjs`); `php-storage`: the DEFEKT text cases and the `health_check_url` cases of `watchdog` | Task 7 `TestSelfTest`; Task 22 `TestDefektText`, `go-watch`: "Dashboard DEFEKT ab dem zweiten vollen Lauf"; Task 9 `/api/health.php` → 404 |
| hint of the kind `nas_web` | `php-storage`: the `notify_list` hint | Task 15 `TestNasWebHint` |
| "Box OFFLINE" blames the server when `blocks_pushes` | `php-storage`: the checker cases that break the folder and expect that text | Task 22 `TestOfflineTextNeverBlamesServer`, `TestFirstOfflineWaitsForWatcher` |
| the first "Box OFFLINE" through the bridge goes out at the threshold, whatever the outside watcher's last call | `php-storage`: "über die Brücke, der Außen-Wächter meldet sich aber noch: es bleibt bei der Warnung vor dem Ausfall der Box" (watcher seen 500 s ago) | Task 22 `TestFirstOfflineWaitsForWatcher` (row "450 s ago → not yet") |
| `watcher_unknown_at`, spare state in the temp folder | `php-storage`: the `watcher_unknown_at` case | accepted loss (spec §7); Task 21 `TestMarkersSurviveRestart` |
| checker as its own process every 15 min; `cron_age_s=-1` before the first run | `php-storage`: the `cron_age_s=-1` cases of `alive`, among them "sieben Zeilen …" (the whole answer and its headers) | Task 21 `TestScheduleFirstFullRun`, `TestMinuteTickOnlyBoxOffline`, `TestRunsNeverOverlap`, `TestCronAge`; `go-box`: "alive.php: sieben Zeilen" (the same lines and headers, `cron_age_s` ≥ 0), "cron_age_s: vor dem ersten Lauf die Zeit seit dem Start, danach sein Alter", "cron_configured=0 ohne bark_key_status (leer und nur Leerzeichen)" |
| failed message retried by the very next run | `php-storage`: the two retry cases of `watchdog` ("nach SEND_FAILED sendet der nächste Lauf") | Task 22 `TestRetryNotBefore15Min`, `go-watch`: "nach gescheitertem Versand frühestens 15 min später" (with everything else of the moved cases: `SEND_FAILED`, nothing counts as reported, no history entry) and "ein unbrauchbarer Status-Key ist ein Fehlschlag (SEND_FAILED), kein 'abgeschaltet'" |
| silence counts from the last sign of life | — | Task 21 `TestAwayRule`, `go-watch`: "nach einer Auszeit des Servers keine Geister-Meldung" |
| `log_cache.json` | — | nothing (gone) |
| `defer_after_response` | — | Task 24 `TestShutdownWaitsForBackground`; Task 25 `go-concurrency`: answers never wait for Bark |
| PHP sessions | the idle case stays skipped on PHP | Task 13: `session` "länger als 12 Std ohne Anfrage" runs on Go |
| typo in `config.php` breaks every request | — | Task 9 `TestProviderKeepsLastGood`; Task 25 `go-config` |
| trusted proxies by address only | — | Task 11 `TestUnixListenerCountsAsTunnel` |
| texts naming `config.php` | cases use `backend.words.configFile` | same cases |
| five new admin messages, two new history events | — | Task 23 `TestOwnFaultMessages`; Task 26 `tests/dashboard/logic-log.test.mjs` |

---

## Phase A — Tests off the files (PHP stays green)

Today about 125 places in the tests reach into PHP's data files (`backend.data.read/edit/write/corrupt/block`). After this phase a test says WHAT it wants and each driver knows WHERE that lies. The proof that the rewording changed nothing is that everything stays green on PHP and still catches planted faults.

### Task 1: Intent helpers in the harness; box, alive and checker tests

**Files:**
- Modify: `tests/backend/harness.mjs`, `tests/backend/box-status.test.mjs`, `box-poll-ack.test.mjs`, `box-keys.test.mjs`, `alive.test.mjs`, `watchdog.test.mjs`
- Create: `tests/backend/php-storage.test.mjs`

**Interfaces:**
- Produces in `harness.mjs`: `export const BACKEND_KIND = process.env.FW_BACKEND || 'php'` — a whole file is limited to one kind with `describe('…', { skip: BACKEND_KIND !== 'php' && 'nur PHP' }, …)` (`backend` does not exist yet when a file is defined).
- Produces on `backend` (all `async`; implemented here for the PHP driver, in Task 10 for Go). Seconds are "that long ago"; a negative number lies in the future.

```js
backend.state = {
  // Zeit verschieben
  ageBoxStatus(seconds), ageDevice(deviceId, seconds),
  ageMute(idOrAll, seconds),            // 'all' = alle, die gerade stumm sind
  ageDemo(seconds),
  ageCommand(which, seconds),           // which: 'created' | 'delivered'
  ageWatcher(seconds),
  ageMark(name, seconds),               // Merker des Wächters, Namen wie in "File formats"
  ageRetry(kind, seconds),              // nur Go (PHP: wirft): Sperrfrist nach gescheitertem Versand
  ageMuteThrottle(seconds),
  ageLog(seconds, match),               // passende Verlaufs-Einträge älter machen (log_at)
  ageSession(cookie, seconds),          // PHP: wirft (Sitzungen liegen außerhalb des Prüfstands)
  // Herstellen
  setWatcher({ seenAgo, barkFailedAt = 0 }),
  setMarks(marks),                      // mehrere Merker in einem Zug; null löscht einen
  setCommand(patch),                    // Felder des laufenden Befehls
  seedLog(entries, { rotated = false } = {}),     // Einträge wie gegeben, ohne uid
  seedRecipients(entries, { version, nextId }),   // nur gültige Einträge
  // Lesen: wie gespeichert, nicht wie angezeigt
  boxStatus(), recipients(), recipient(id), listVersion(), demo(), command(),
  watcher(), marks(), log(), muteFailures(), notifyStored(),
};
backend.storage = { fail(what), repair(what) };   // what: 'selftest' | 'durable' | 'history' | 'volatile'
backend.words = { configFile: 'config.php' };     // Go: 'config.toml'
```

- `boxStatus()` returns the pushed fields as an object plus `seen_at`, `via_bridge` (or `null`); `recipients()` returns `{ version, keys: [...] }` (on PHP from the `.bak` while `storage.fail('durable')` is active); `watcher()` `{ seen_at, bark_failed_at }` or `null`; `marks()` an object of the markers; `log()` the raw entries, oldest first.
- PHP mapping of `storage.fail` (checked against `with_lock`, `with_lock_soft`, `read_alarm_keys_state`; write the final mapping into the comment above it): `selftest` → folder at `health_probe.json`; `history` → folders at `commands.log` and `log.lock`; `durable` → copy the list to `alarm_keys.json.bak`, then folders at `alarm_keys.json`, `keys.lock`, `command.lock`, `notify.lock`, `settings.lock`; `volatile` → folders at `latest_status.json`, `watcher_seen.json`.
- **Every helper really yields before it touches a file** (`await new Promise(setImmediate)` first): a call without `await` then fails on PHP too — on Go it would race with a restart.
- `backend.data` stays, but only `php-*.test.mjs` files may use it.

**Classification of every place** (this is the task), in this order of precedence: (c) the case corrupts, removes or blocks a particular PHP file, reads a `.bak`, sets `health_check_url`, or asserts something spec §7 changes (see "Rule → test") → it moves UNCHANGED to `php-storage.test.mjs`, and the report lists it with the §7 row it belongs to; (a) it shifts a time, seeds or reads stored state → the helper above; (b) it breaks storage and is green on PHP with `storage.fail(what)` → that. Never weaken an assertion to fit a helper; a case that fits none goes to (c).

- [ ] **Step 1:** Record the baseline: `node --test "tests/backend/*.test.mjs"` → 366 pass, 2 skipped. Save the list of case names (`--test-reporter=tap`) to the scratch folder.
- [ ] **Step 2:** Implement `BACKEND_KIND`, `backend.state`, `backend.storage`, `backend.words` for the PHP driver.
- [ ] **Step 3:** Rework the five files. Their local helpers (`ageStatus`, `editState`, `watcherSeen`, `ageMutes`, `storedKey`, `breakDataDir`, `stored`, `editCommand`, `seen`) become thin `async` calls of the new helpers or disappear; every call site gets its `await`. To `php-storage.test.mjs` go, among others: the `watcher_unknown_at` case, the `health_check_url` cases (`watchdog.test.mjs` "Selbsttest falsch eingerichtet …"), the cases asserting the DEFEKT text or a "Box OFFLINE" text that blames the server, the two cases where the run right after `SEND_FAILED` sends again, the `cron_age_s=-1` and `stored=0` cases of `alive`.
- [ ] **Step 4:** `node --test "tests/backend/*.test.mjs"` → every case name of the baseline is still there (in its file or in `php-storage`), same pass/skip counts. `grep -n "backend\.data\." tests/backend/{box-status,box-poll-ack,box-keys,alive,watchdog}.test.mjs` prints nothing. No helper call without `await`: `grep -nE "(backend\.(state|storage)\.\w+|\b(ageStatus|ageMutes|editState|watcherSeen|editCommand|breakDataDir|repairDataDir)\b)\(" <the five files> | grep -v await | grep -v "=>"` prints nothing (wrappers defined with `=>` are checked by eye).
- [ ] **Step 5:** Commit — `Tests: Box-, Lebenszeichen- und Wächter-Tests sagen, was sie wollen, statt Dateien anzufassen`.

### Task 2: The remaining server tests off the files; new shared cases; proof

**Files:**
- Modify: `tests/backend/alarm-send.test.mjs`, `mute-link.test.mjs`, `dashboard-access.test.mjs`, `dashboard-keys.test.mjs`, `dashboard-log.test.mjs`, `dashboard-notify.test.mjs`, `session.test.mjs`, `box-keys.test.mjs`, `php-storage.test.mjs`, `tests/backend/README.md`, `CLAUDE.md` (the sentence in "Server-Tests" that says the tests reach the data folder through `backend.data`)
- Rename: `tests/backend/health.test.mjs` → `php-health.test.mjs` (whole file PHP only)

**Interfaces:** Consumes Task 1's helpers; adds none.

- [ ] **Step 1:** Rework the files by the classification of Task 1 (the `mute-link` cases with list and backup unreadable go to `php-storage`). Cases asserting a text that names the configuration file build the expectation from `backend.words.configFile`. Where a case only checks that a message holds no path, it uses `backend.dataDir` as today (the Go driver provides it).
- [ ] **Step 2: Cases the earlier reviews asked for** (each must be green on PHP):
  - `dashboard-notify` `loginTexts()`: `waitFor` the expected number of messages, then `settle()` for "no more" (a server may answer before it sends).
  - `session`: the idle case calls `backend.state.ageSession(cookie, IDLE_MAX + 60)` (12 h and one minute — the limit is 12 h, not 13) instead of reading a sessions folder; `sessionFiles()` goes; still skipped on PHP.
  - `session`: `/classic.php` answers 302 with `Location: index.php` (or an absolute address ending in `/index.php`) and no cookie.
  - `session`: with `trusted_proxies` set to an address that does NOT match, `X-Forwarded-For` is ignored (the brake counts the real sender).
  - `dashboard-log`: seed a `cancelled` entry without reason and a `relay_alarm` with explicit `source`; assert their output.
  - `dashboard-access`: the CSRF token is accepted as form field `csrf` as well as in the header (`dashboard_api.php:35`).
  - `box-keys`: switching the demo mode off while it is off succeeds and still raises the list version (the repair path of `CLAUDE.md`).
- [ ] **Step 3: Proof that the tests still bite.** Copy `nas_dashboard/` to a temp folder and plant one fault at a time; run with `FW_BACKEND_SRC=<copy>`; each must turn at least one reworked case red: (1) `mute_is_stale()` returns false always; (2) `api/status.php` never writes `relay_alarm`; (3) `api/poll.php` does not store `delivered`; (4) the checker never sends the demo reminder; (5) `api/alive.php` never stores the call. Put the five red case names into the report.
- [ ] **Step 4:** `node --test "tests/backend/*.test.mjs"` → all pass, the two known skips. `grep -ln "backend\.data\." tests/backend/*.test.mjs` prints only `php-storage.test.mjs` and `php-health.test.mjs`. The `await` search of Task 1 Step 4 prints nothing for the shared files (inside `php-storage.test.mjs` the synchronous `backend.data` wrappers of moved cases show up — expected).
- [ ] **Step 5:** `tests/backend/README.md`: replace the rules about `backend.data` by the helpers, name the `php-*`/`go-*` convention and the divergence protocol. Commit — `Tests: Server-Tests kennen keine Dateien mehr – was nur PHPs Ablage prüft, steht in php-*.test.mjs`.

### Task 3: Browser scenarios off the files

**Files:**
- Modify: `tests/dashboard/e2e/sandbox.mjs`, `scenarios/overview.mjs`, `recipients.mjs`, `settings.mjs`, `log.mjs`, `tests/dashboard/e2e/README.md`

**Interfaces:** Consumes `backend.state`. `sandbox.mjs` loses `readData`/`editData`; `setStatusAge(backend, seconds)` becomes `async` and calls `backend.state.ageBoxStatus`.

- [ ] **Step 1:** Replace every `readData`/`editData`/`backend.data` use: stored recipients → `backend.state.recipients()`; `last_run_at = 0` → `backend.state.setMarks({ last_run_at: 0 })`; mute age → `ageMute`; seeded history → `seedLog`. Remove what only served a shared, hand-started sandbox, because every run starts empty: the restore logic for "existing recipients" in `overview.mjs`, `recipients.mjs`, `settings.mjs`, and `backupFile`/`restoreFile` in `settings.mjs` — where a scenario needs a value set back between its own steps, it uses the dashboard action for it. Every call of a helper that became `async` gets its `await` (`setStatusAge` is called without one today, e.g. `overview.mjs:382`, `:405`, `settings.mjs:434`, `:474`).
- [ ] **Step 2:** `node tests/dashboard/e2e/run.mjs <name>` for `smoke shell overview recipients log settings switch` → `OK` seven times. `grep -rn "\.data\.\|readData\|editData" tests/dashboard/e2e` prints nothing; `grep -rnE "\b(setStatusAge|backend\.state\.\w+)\(" tests/dashboard/e2e | grep -v await | grep -v "export"` prints nothing.
- [ ] **Step 3:** Commit — `Tests: Chrome-Szenarien nutzen dieselben Helfer wie die Server-Tests`.

---

## Phase B — The Go server (the live system notices nothing)

Every task here: write the named tests first and see them fail; implement; `bash tools/check-go.sh`; `bash tools/test-go.sh`; commit. Where a task says "as PHP `<function>`", that function in `nas_dashboard/` is the source for the rule and every text. After each task the controller pushes and looks at the run on GitHub.

### Task 4: Module, tools, configuration, checks on GitHub

**Files:**
- Create: `go.mod`, `go.sum`, `vendor/`, `cmd/fw-alarm/main.go`, `internal/clock/clock.go` (+ test), `internal/bg/bg.go` (+ test), `internal/textde/textde.go` (+ test), `internal/config/{config.go,load.go,write.go,password.go}` (+ tests), `server/go/config.example.toml`, `tools/check-go.sh`, `tools/test-go.sh`, `tools/go-green.txt` (empty), `nas_dashboard/webassets.go`, `.github/workflows/ci.yml`
- Modify: `.gitattributes` (the lines of Global Constraints "Line endings"), `.gitignore` (`/bin/`), `server/deploy.sh` (add `webassets.go` to the skip list and the rsync excludes, so the Go file never lands in the PHP webroot) and its case in `tests/server/`, `README.md` (one sentence on the checks), `CLAUDE.md` (new section "Go-Fassung (im Bau)": layout, commands, that PHP is still what runs)

**Interfaces:**
- Produces: `clock`, `bg`, `textde` as in "Shared vocabulary" (`textde.Stamp(t time.Time, loc *time.Location) string`, `DurationLabel(seconds int) string` as PHP `notify_duration_label`, `OutageDuration(minutes int) string` as PHP `outage_duration_text`).
- Produces `config`:

```go
type Config struct { /* ein Feld je Schlüssel, Namen s.u. */ }
func Load(path string) (*Config, error)        // Fehler = "fehlerhaft" (s.u.)
func Parse(data []byte) (*Config, error)
func (c *Config) Warnings() []string           // ohne Geheimnisse; leer = nichts zu melden
func (c *Config) Location() *time.Location
func (c *Config) AuthStamp(role string) string // HMAC über Rolle+Benutzer mit dem Passwort-Hash (PHP: dashboard_auth_stamp)
func Write(c *Config) []byte                   // TOML mit Kommentaren; Parse(Write(c)) ergibt c
func HashPassword(pw string) (string, error)   // Argon2id: 19 MiB, 2 Durchläufe, 1 Faden
func VerifyPassword(hash, pw string) bool      // $argon2id$ und bcrypt ($2y$, $2a$, $2b$)
```

- Keys = the names in `nas_dashboard/config.example.php` minus `data_dir`, plus `state_dir` (default `/var/lib/fw-alarm`), `listen` (default `127.0.0.1:8080`; or `unix:<path>`), `control_socket` (default `/run/fw-alarm/control.sock`), `check_interval_seconds` (default 900; 0 = no automatic runs), and the send lock: `send_guard_keys` (list of Bark keys, default empty) with `send_guard_until` (local time `YYYY-MM-DD HH:MM` in `timezone`, default empty).
- **"Broken" and "warning" are two things.** A file is BROKEN — `Parse` returns an error; in the running server the last good version applies and all sessions end (Task 9) — only when it cannot be read, has a syntax error, an unknown key, a value of the wrong type, or an unknown `timezone`. Everything else is a WARNING the server runs with: a token hash that is not 64 hex characters, a password hash that is neither bcrypt nor Argon2id, `watcher_token_sha256` equal to `machine_token_sha256`, a relative `state_dir`, `check_interval_seconds` 0, an invalid entry in `bark_keys_alarm`, an unusable `bark_key_status`. (Shared tests need such files to load and take effect.)
- **Send lock in the configuration:** it is in force while `send_guard_keys` is not empty and `send_guard_until` lies in the future. The file is BROKEN when only one of the two is set, when the time does not parse, when a key is not a valid Bark key, or when the time lies more than 3 hours after the moment of loading — a lock can be mistyped into next year and then silently block a real manual alarm; such a file is never accepted. A lock that is in force is a WARNING (`Hinweis: Probe-Sperre aktiv bis <HH:MM> – der Server sendet nur an <n> Key(s)`); one that has run out is ignored.
- Produces also: `func (c *Config) SendGuard(now time.Time) (keys []string, until time.Time, active bool)`.
- **A missing key gets the value the PHP CODE uses for it** (the `?? …` in the source), not the value of the example file; a key that is present but empty stays empty: `offline_after_seconds` 180, `watchdog_offline_after_seconds` 600, `demo_reminder_after_seconds` 14400, `mute_max_seconds` 43200, `delivered_command_lock_seconds` 120, `pending_command_ttl_seconds` 300, `login_max_failures` 8 (never below 1), `login_lockout_window_seconds` 900 (never below 60), `bark_host` `https://api.day.app`, `bark_alarm_title` `FEUERWEHR-ALARM`, `bark_alarm_body` `Manuelle Alarmierung ueber das Dashboard!`, `bark_alarm_sound` `alarm_fw`, `bark_alarm_volume` 10, `bark_alarm_call` true, `bark_max_tries` 3 (never below 1), `bark_false_alarm_sound` `glass`, `bark_false_alarm_volume` = the alarm volume, `timezone` `Europe/Berlin`. Before relying on this list, search every `$config['…'] ??` under `nas_dashboard/` and add what is missing.
- Produces — command line: `fw-alarm version` (string from `-ldflags "-X main.version=<sha>"`, default `dev`); `fw-alarm check-config [--config <file>]` → broken: exit 1 and `Fehler: <reason>`; otherwise exit 0, one line `Hinweis: <warning>` per warning, then `Konfiguration in Ordnung.`; never a secret value. `fw-alarm hash-password` (reads without echo through `x/term`, twice, prints the hash). `--config` defaults to `/etc/fw-alarm/config.toml` for every subcommand.
- Produces: `tools/check-go.sh` (one line per check, exit 1 on the first failure: `gofmt -l cmd internal nas_dashboard` prints nothing; `go vet ./...`; `GOOS=linux GOARCH=arm64 go vet ./...`; `staticcheck ./...`; `govulncheck ./...`; `go test -mod=vendor ./...`); `tools/test-go.sh [files…]` (builds `bin/fw-alarm` with `-mod=vendor`; without arguments: the files of `tools/go-green.txt`, nothing to do when it is empty; then `FW_BACKEND=go FW_GO_BIN=<abs> node --test <files>`).
- `.github/workflows/ci.yml`: triggers push to `main`, weekly (`cron: '17 4 * * 1'`), `workflow_dispatch`; `permissions: contents: read`; `concurrency` per ref, cancelling older runs; no secrets, no deploy, no artifact. Jobs on `ubuntu-24.04`: **go** — `gofmt -l` prints nothing, `go vet ./...`, `staticcheck ./...`, `govulncheck ./...`, `go test -race -mod=vendor ./...`, `go build` for `linux/arm64` and `windows/amd64`; **node** — Node 24, `node --test "tests/dashboard/*.test.mjs" "tests/server/*.test.mjs" "tests/watcher/*.test.mjs"`. Actions pinned by commit with the version as a comment: `actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1` (v7.0.1), `actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e` (v7.0.0, `go-version-file: go.mod`), `actions/setup-node@949feb2413d6458794dcd2491c4babbbce0c15c1` (v7.1.0). `staticcheck` 2026.2.2 and `govulncheck` v1.9.0 installed with `go install …@<version>`. The job **node** needs `php` on the path (two wording tests execute PHP functions until Task 37): it prints `php --version` first and installs `php-cli` with `apt-get` when the runner has none.

- [ ] **Step 1: Tests.** `TestDefaultsMatchPHPCode` (the list above); `TestPresentButEmptyStaysEmpty`; `TestExampleFileLoads` (`server/go/config.example.toml` loads, names every key, and its placeholders come out as warnings); `TestBroken` (unknown key → error naming it; wrong type; syntax error; unknown time zone); `TestWarningsStillLoad` (each warning of the list above: the file loads, the value applies, the warning line holds no hash, token or key); `TestSendGuardConfig` (unset → inactive; both set and 30 min ahead → active with those keys; run out → inactive, no error; only one of the two, an unparsable time, an invalid key, 3 h 1 min ahead → broken; the warning names no key); `TestHashNormalised` (upper case and spaces in the token hashes accepted); `TestWriteRoundTrip`; `TestWriteOddStrings` (quotes, backslashes, umlauts, a line break, `#` and `=` in `bark_alarm_title`/`bark_alarm_body`: `Parse(Write(c))` gives the same values); `FuzzWriteParse`; `FuzzParse`; `TestVerifyPassword` (the bcrypt hash of `test` from `harness.mjs`; a fresh Argon2id hash; a wrong password); `TestStamp` (`2026-07-01T10:00:00Z` → `[01.07.2026 12:00] `, `2026-12-01T10:00:00Z` → `[01.12.2026 11:00] `); `TestOutageDuration` (119 → `119 min`, 120 → `2 Std`); `TestFakeClock`; `TestGroupRecoversPanic`, `TestGroupWait`.
- [ ] **Step 2:** Implement. `go mod init fwalarm`, the four modules, `go mod vendor`. `time/tzdata` is imported in `cmd/fw-alarm`. The example file carries every key with its explanation as a comment (taken from `config.example.php`, shortened where it described Synology) and keeps the banner `>>> HIER STEHEN ALLE WERTE … <<<`. After changing `.gitattributes`: `git add --renormalize .`, and check the newly LF-pinned asset files out again so the working tree matches the repository.
- [ ] **Step 3:** `bash tools/check-go.sh` → ok. `go run ./cmd/fw-alarm check-config --config server/go/config.example.toml` → exit 0 with the placeholder warnings. `node --test "tests/backend/*.test.mjs" "tests/server/*.test.mjs" "tests/dashboard/*.test.mjs"` (PHP) → still all pass. Run every `run:` line of the workflow once by hand in Git Bash (without `-race`). Commit — `Go: Modul, Prüfwerkzeuge, Konfiguration als config.toml – und GitHub prüft ab jetzt jeden Push`.

### Task 5: `model` — the documents and the rules of the recipient list

**Files:** Create `internal/model/{state.go,runtime.go,sessions.go,rescue.go,entry.go,clone.go,decode.go,validate.go,recipients.go,mute.go,demo.go,rewind.go}` + tests.

**Interfaces — produces:**

```go
const MaxRecipients = 10
const Schema = 1
type State struct { /* wie "File formats" */ }
type Recipient struct{ ID int; Key, Label, Phone string; AddedAt int64; Muted bool; MutedAt int64; MuteVolume int; MutePermanent bool; MuteToken string }
type Demo struct{ Enabled bool; KeyID int; Key, Label string; ChangedAt int64; By string }
type Command struct{ /* die Felder aus "File formats" */ }
type RecentType struct{ ID int; Type string }
type Commands struct{ NextID int; Current *Command; RecentTypes []RecentType }
type Runtime struct { /* wie "File formats" */ }
type BoxStatus struct{ SeenAt int64; ViaBridge, RemoteAddr string; Fields Fields }
type Fields []Field                      // geordnete Paare; JSON: Liste von Paaren
type Field struct{ Name, Value string }
func (f Fields) Get(name string) string
type PendingNotice struct{ ID, Title, Body string; Since int64 }
type Sessions struct{ Schema int; Sessions map[string]Session }
type Rescue struct{ Schema int; WrittenAt int64; DemoKeyID int; Recipients []RescueRecipient }
type Entry map[string]any                // eine Verlaufszeile
func NewEntry(event string, at time.Time, fields map[string]any) Entry   // setzt event, log_at, uid
func (e Entry) Str(k string) string; func (e Entry) Int(k string) int64; func (e Entry) Bool(k string) bool   // nachsichtig
const EvNasAlarm = "nas_alarm" /* … eine Konstante je Ereignis aus PHP log_event_types() */
const EvStateRewound, EvSendInterrupted = "state_rewound", "send_interrupted"
var AllEvents []string

func (s *State) Clone() *State; func (r *Runtime) Clone() *Runtime; func (s *Sessions) Clone() *Sessions
func (s *State) Validate() error
func (s *State) Log(e Entry); func (r *Runtime) Log(e Entry)   // an die outbox, höchstens 500 (älteste fallen weg)
func DecodeState(data []byte) (*State, error)      // streng (s.u.)
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
func (s *State) ExpireStaleMutes(now time.Time, muteMax int, boxVersion int) []Recipient   // schaltet zurück, bumpt (Bump(boxVersion)), protokolliert; liefert die Betroffenen
func MaskKey(key string) string                     // PHP: mask_bark_key – die EINE Fassung; bark.MaskKey reicht nur weiter
func (s *State) Bump(boxVersion int) int            // keys_version = max(eigene, boxVersion) + 1
func (s *State) AllocateID() int
func (s *State) Find(id int) *Recipient
func (s *State) DemoBlockReason(box *BoxStatus, now time.Time, offlineAfter int) string   // PHP: demo_alarm_block_reason
type RewindSource string                            // "prev" | "restore" | "import"
func (s *State) Rewind(src RewindSource, now time.Time) // +1000 auf keys_version, next_id, commands.next_id; offener Befehl → cancelled (Grund: RewindCancelReason) mit eigenem Eintrag `cancelled`; danach Eintrag state_rewound
```

- **Strict decoding** means more than rejecting unknown fields — each of these is an error naming the field: an unknown field; a missing field (also a missing `schema`); a wrong type; `null` where a list, an object or a string belongs; a key written in another case than the document's (`"LABEL"`); a key that appears twice; anything after the document; a `schema` other than `model.Schema`.
- `Validate` holds: at most 10 recipients; every key valid and unique; ids unique, > 0 and < `next_id`; every `mute_token` valid and unique; `mute_permanent` only with `muted`; `mute_volume` 0–10; demo enabled only with a `key_id` that is in the list and `key` equal to that recipient's key; `commands.current.id` < `commands.next_id`; `recent_types` at most 20, ordered by id, ids unique.

- [ ] **Step 1: Tests.** `TestCloneSharesNothing` (walk original and clone by reflection: no slice, map or pointer reachable from one is reachable from the other — fails when a field is added without its copy); `TestDraftDoesNotTouchSnapshot` (change every part of a clone, the original marshals to the same bytes as before); `TestStrictDecode` (one row per reason above, for `State`, `Runtime` and `Sessions`); `TestInvalidListCannotBeSaved` (table over every `Validate` invariant); `TestCommandFieldsMatchPHP` (the JSON names of `Command` are exactly the list in "File formats"); `TestEffective` (demo → one target; overdue mute → loud without changing the state; permanent mute never overdue; mute volume kept); `TestExpireStaleMutes` (flag and time cleared, version +1 once, one history entry per recipient as PHP `expire_stale_mutes` writes it; nothing stale → nothing changes, no bump); `TestApplyMute` (loud ends permanent and reports it); `TestBump` (table: own 5, box 0 → 6; own 5, box 41 → 42; own 41, box 5 → 42); `TestAllocateID`; `TestRewind` (+1000 three times; `pending` and `delivered` command → `cancelled`; `acked` untouched; entry `state_rewound` with `source`); `TestRescueLenient` (non-objects, short and duplicate keys, 12 entries → the first 10 valid unique ones; garbage → empty); `TestFieldsKeepOrder` (JSON round trip keeps the order of 40 pairs); `TestOutboxCap` (501st entry drops the oldest); `FuzzDecodeState` (decode → encode → decode is stable).
- [ ] **Step 2:** Implement. One constant per event of PHP `log_event_types()`; entries are built only through `NewEntry`.
- [ ] **Step 3:** `bash tools/check-go.sh`. Commit — `Go: Datentypen des Zustands und die Regeln der Empfängerliste als reine Funktionen`.

### Task 6: `store` I — files, saving, the start table

**Files:** Create `internal/store/{fs.go,fs_unix.go,fs_windows.go,save.go,open.go,rescue.go,migrate.go}` + tests, `internal/store/storetest/faultfs.go` (a fake `FS` for tests of this and later tasks).

**Interfaces — produces:**

```go
type FS interface {
    ReadFile(name string) ([]byte, error)
    WriteFileSync(name string, data []byte) error   // anlegen/kürzen, schreiben, fsync, schließen (0600)
    AppendSync(name string, data []byte) error      // öffnen, anhängen, fsync, schließen
    Rename(from, to string) error
    Link(from, to string) error
    Remove(name string) error                       // eine fehlende Datei ist kein Fehler
    SyncDir(dir string) error                       // Windows: nichts
    Stat(name string) (fs.FileInfo, error)
    SameFile(a, b string) bool
    MkdirAll(dir string) error                      // 0700
    ReadDir(dir string) ([]string, error)           // Namen im Ordner (für die Spuren früherer Nutzung)
    Lock(name string) (release func() error, err error)   // ausschließend (flock / LockFileEx); ErrLocked, wenn ein anderer sie hält
}
func OSFS() FS            // Windows: Rename, Remove und Link bis zu 1 s wiederholen, wenn ein anderer die Datei hält
// storetest
type FaultFS struct{ /* … */ }                       // im Speicher; je Datei: Fail(name, err), Hang(name) → release(), ShortWrite(name, n), FailCall(n)
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
func (s *Store) Rescue() (model.Rescue, bool)                        // die EINMAL beim Notbetrieb-Start nachsichtig gelesene Kopie, aus dem Speicher; false: keine lesbare Kopie. Die Regeln stehen im Modell: Rescue.Targets(now, muteMax) (targets, demo, ok)
func CheckState(dir string, fsys FS) (*model.State, error)          // verlangt eine lesbare state.json; kein Rückfall
```

**Saving a document** (`state.json`, `runtime.json`, `sessions.json`, the rescue copy):

1. marshal → `WriteFileSync(<file>.tmp)` → read the temp file back with the strict decoder and compare (re-marshalled bytes equal to what was meant); a mismatch = the save failed, the temp file is removed.
2. For `state.json` only, and **only when `state.json` exists at this moment**: `Remove(state.prev.json)`, then `Link(state.json, state.prev.json)` (a copy when linking fails). When `state.json` does not exist (the first save ever; the save right after a fallback start) this step is skipped and an existing `state.prev.json` is left alone — it stays the net until the save after.
3. `Rename(<file>.tmp, <file>)` → `SyncDir`.

Any failing step = the save failed. At no point are `state.json` and `state.prev.json` both missing or both unreadable if one was readable before. A leftover `*.tmp` at start is removed if it is a file and ignored if it is not. If at start `state.json` and `state.prev.json` are the same file (`SameFile` — a save died between link and rename), the previous one is replaced by a copy, so an edit in place cannot damage both.

**Start table** (spec §6.3):

| `state.json` | `state.prev.json` | Result |
|---|---|---|
| readable | any | normal |
| missing | missing, and the folder holds no trace of earlier use (none of `recipients.rescue.json`, `history.jsonl`, `runtime.json`, `sessions.json`, `state.corrupt-*`) | fresh, empty state |
| missing or unreadable | readable | the unreadable file is moved to `state.corrupt-<unix time>.json`, start on the previous one, `Rewind("prev")`, save |
| unreadable, or missing although traces exist | missing or unreadable | **emergency**: nothing durable is ever written, the files stay as they are |
| schema newer than `model.Schema` | any (also readable) | **emergency** — an older program never falls back behind data a newer one wrote |

`model.DecodeState` also runs `Validate` (Task 5): a document that breaks an invariant counts as unreadable, exactly like broken JSON — but a `model.SchemaError` is looked at first (newer → emergency, older → lift). An older schema is lifted by the functions in `migrate.go` (one table per document — `state`, `runtime`, `sessions` — of the form `map[int]func([]byte) ([]byte, error){}`, all empty today; a volatile document whose older schema has no function is discarded like an unreadable one) after the original was kept once as `state.schema-<n>.json`. `runtime.json` / `sessions.json` unreadable → moved aside as `<name>.corrupt-<time>.json`, start empty, flag in `StartInfo`. The lock is `server.lock`, held until `Close`: held by another process → `ErrLocked`; cannot be created at all → `LockUnavailable` and start anyway (Review Focus 5). A fresh start saves the empty state at once (otherwise the first `runtime.json` would count as a trace at the next start). `state.json` becomes the previous version only when it exists AND is the readable file the server works on. On an emergency start the rescue copy is read ONCE, leniently, and kept in memory — the alarm never reads a file.

- [ ] **Step 1: Tests** (with `FaultFS` unless a real folder is named): `TestSaveReadBack` (a mismatch planted through `FaultFS` → save fails, old file untouched); `TestSaveKeepsGoodFileOnShortWrite` (temp file truncated to half → save fails, `state.json` still the old one); `TestPrevIsStateBefore` (after two saves `state.prev.json` holds the first); `TestFirstSaveEver` (no file yet → works, no previous one afterwards); `TestSaveAfterFallback` (start on the previous version: the save keeps `state.prev.json` until `state.json` is in place); `TestStateNeverMissing` (Review Focus 2 — for each of three starting points: normal, fresh, fallback — fail every single FS call of a save in turn: a reopen afterwards finds a readable state and never `StartFresh` when a state existed before); `TestFreshOnlyWithoutTraces` (both files missing but `history.jsonl` there → emergency); `TestStartTable` (the four rows; in the fallback row: file moved aside, +1000, `StartInfo.Rewound`); `TestSameFileRepaired`; `TestSchemaNewerIsEmergency`; `TestMigrationKeepsOriginal` (with an injected 0→1 function); `TestEmergencyWritesNothing` (no FS write call for durable files after an emergency start); `TestRuntimeDiscarded`; `TestRescueReadIsLenient`, `TestRescueDemoOnly` (a valid demo recipient in the copy → only that one), `TestRescueWithoutValidKey` (→ `ok=false`); `TestLockSecondOpen` (real temp folder: second `Open` → `ErrLocked`; after `Close` it works); `TestOpenOnUnwritableDir` (every write and `Lock` fail → `Open` succeeds with `LockUnavailable`, the state it read is served); `TestTmpIsFolder` (real temp folder: a non-empty folder at `state.json.tmp` → `Open` works, a save fails, removing the folder repairs it — on Windows and Linux); `TestRetryWhileHeldWindows` (build tag: target held open by a reader for 300 ms → `Rename`, `Remove` and `Link` succeed).
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** `bash tools/check-go.sh`. Commit — `Go: Ablage – sichern mit Zurücklesen, ein Programm pro Datenordner, Rückfall und Notbetrieb`.

### Task 7: `store` II — one writer, deadlines, volatile data, history

**Files:** Create `internal/store/{durable.go,volatile.go,history.go,selftest.go,flush.go}` + tests.

**Interfaces — produces** the `Store` methods of "Shared vocabulary", plus:

```go
type SelfTest struct{ OK bool; Step string }           // Step: ok | write | read | delete | save
func (s *Store) SelfTest() SelfTest                    // selftest.tmp schreiben, zurücklesen, löschen; "save" = das letzte Sichern eines Dokuments, der Not-Kopie oder des Verlaufs scheiterte
func (s *Store) Resave(ctx context.Context)            // sichert die unveränderten Dokumente noch einmal (der Prüf-Lauf ruft das, solange "save" gemeldet wird)
type SaveHealth struct{ Durable, Volatile, Sessions, History, Rescue bool }
func (s *Store) SaveHealth() SaveHealth                // true = letztes Sichern gelang
func (s *Store) Flush(ctx context.Context) error       // wartet, bis alles Ausstehende auf der Platte liegt
func (s *Store) Lifebeat()                             // lifebeat_at = jetzt (flüchtig)
```

**`Update` exactly** (spec §6.2): callers queue for the one writer slot and give up when `ctx` ends → `ErrDropped`, nothing happened. Holding the slot: clone the snapshot → drop outbox entries the history writer reported as appended → `fn(draft)` (an error from `fn` is returned as it is, nothing saved) → `Validate` (failure → `ErrInvalid`) → save in a goroutine; the caller waits for the save or for `ctx`. Save done in time: swap the pointer, hand new outbox entries to the history writer, refresh the rescue copy when recipients or demo changed (beside; a failure holds nothing but shows in `SaveHealth`), release the slot, return nil. Save failed: `ErrStorage`, nothing changed. `ctx` ends while saving: return `ErrLate` at once — the save runs on; if it succeeds the pointer is swapped and the entries are written just as above, and only then the slot is released. In emergency mode: `ErrEmergency` immediately. `fn` is called at most once.

**Volatile holders** (`runtime.json`, `sessions.json`): `UpdateRuntime`/`UpdateSessions` clone, apply, swap the pointer under a mutex and return — never an error, never a wait. One writer goroutine per file saves the newest snapshot right after a change (one save at a time; changes arriving meanwhile are folded into the next save).

**History:** an entry is appended to `history.jsonl` only after the document carrying it in its outbox is saved. The writer opens, appends, syncs and closes per batch; if the file does not end in a line break it writes one first; above 5 MB it rotates to `history.1.jsonl` first. After it appended entries of the durable outbox, the store removes them with a follow-up save of its own (beside, 5 s budget, at most one waiting, a failure ignored) — an entry never stays in an outbox for long. At start: an outbox entry whose `uid` is in none of the lines held in memory is queued for the writer — `Open` itself never waits on `history.jsonl`. Every outbox entry carries a `uid` (`Validate` rejects one without; `Store.Log` gives a missing one a fresh `uid` and writes a journal line); old history lines without `uid` are ignored when uids are compared. The queue between a saved document and the history writer has no cap of its own: an entry that fell out of its outbox leaves the queue with it. The memory view of the history holds at most about 12 MB (when rotation keeps failing, the oldest lines leave the view, not the disk). `History(limit, keep)` never opens a file: the raw lines of `history.jsonl` and `history.1.jsonl` (together at most about 10 MB) are read once at start and kept current by the writer; a call scans them backwards, parses line by line, skips broken lines, stops at `limit`, and puts everything not yet appended in front — newest first. A history that cannot be read at start (e.g. a folder in its place) = empty in memory and `SaveHealth().History` false. `LogAndWait` puts the entry into the volatile outbox and waits until it is on disk or `max` has passed.

- [ ] **Step 1: Tests** (`FaultFS`, `clock.Fake` where time matters): `TestUpdateDeadlines` (a: save hangs past the deadline → `ErrLate`, release the hang → snapshot shows the change and the history has the entry; b: a second `Update` queued behind the hanging one → `ErrDropped` at its deadline and its `fn` never ran; c: save fails → `ErrStorage`, snapshot unchanged, no history line); `TestOneWriterAtATime` (100 goroutines each add 1 to a counter in the state → 100); `TestReadersNeverWait` (with a hanging save, `State()`, `Runtime()`, `History()` return within 10 ms); `TestVolatileNeverBlocks` (hanging disk: 1 000 `UpdateRuntime` calls return; after release one save holds the newest value); `TestVolatileSaveFailureIsSilent`; `TestHistoryAfterSaveOnly` (entry appears in the file only after its document is saved; a failed save leaves no line); `TestNoDuplicateAfterRestart` (kill between "document saved" and "line appended", and between "line appended" and "outbox pruned": after reopening, the line is there exactly once); `TestOutboxPrunedByFollowUpSave`; `TestHistoryReadsPending` (an entry not yet appended is already in `History()`); `TestAppendAfterTornLine`; `TestRotation`; `TestHistoryUnreadableAtStart`; `TestLogAndWait` (on disk → true; hanging disk → false after 300 ms, and the entry still arrives later); `TestSelfTest` (ok; folder at `selftest.tmp` → `write`; last durable save failed → `save`; after `Resave` succeeded → ok again); `TestRescueFollowsChanges` (recipient added → the copy has it; copy unwritable → the update still succeeds and `SaveHealth().Rescue` is false); `TestSaveSurvivesOddStrings` (a label holding a raw `0xFF` byte, quotes, `<`, U+2028: the save succeeds and a reopen reads it); `TestFlush`; `TestCloseWritesEverything` (after `Close` a fresh `Open` sees every volatile change); `FuzzHistoryLine`.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** `bash tools/check-go.sh`; `go test -fuzz=FuzzHistoryLine -fuzztime=30s ./internal/store`. Commit — `Go: ein Schreiber mit Fristen, Flüchtiges nebenher, Verlauf über die Outbox`.

### Task 8: `bark` — sending

**Files:** Create `internal/bark/{client.go,ascii.go,fields.go}` + tests.

**Interfaces — produces:**

```go
type Job struct{ Key string; Fields map[string]string }
type Result struct{ OK bool; HTTP int; Err string }
func New(cfg func() *config.Config) *Client
func (c *Client) PostMany(ctx context.Context, jobs []Job, tries int) []Result   // PHP: bark_post_many
func ASCII(s string) string                    // PHP: bark_ascii
func MaskKey(k string) string                  // reicht an model.MaskKey weiter (eine Fassung)
func AlarmFields(cfg *config.Config, body string, muted bool, muteVolume int) map[string]string   // PHP: bark_alarm_fields
func FalseAlarmFields(cfg *config.Config, title, body string, muted bool, muteVolume int) map[string]string
func TestFields(cfg *config.Config, now time.Time) map[string]string             // PHP: bark_send_test
```

- Client: connect 5 s, whole request 10 s, no redirects followed, response body never read (closed at once), `POST <bark_host>/<key>` urlencoded, only status 200 counts, 4xx never retried, 500 ms between rounds, every open job of a round at the same time, `bark_host` read anew for every send. Each job runs under `recover`.
- **Send lock** — enforced here, in the one function every send of the server passes (`PostMany`), so that no mistake above it can get past: while `cfg().SendGuard(now)` is active, a job whose key is not in the list is NOT sent — no connection is opened for it — and its result is `Result{OK: false, HTTP: 0, Err: "Probe-Sperre: nicht gesendet"}`. Jobs to listed keys go out as always. Without an active lock the code path is not taken at all. `func (c *Client) Guard() (until time.Time, active bool)` tells callers that it is on.

- [ ] **Step 1: Tests.** `TestASCII` (the whole table of `bark_ascii`, line breaks kept, tab → space, other non-ASCII and invalid UTF-8 → `?`); `TestPostManyRounds` (against `httptest`: 500 → `tries` attempts with ≥ 500 ms between rounds; 400 → one attempt; cut connection → retried, `Err` set; a 2 s slow key does not delay the others; a 302 is not followed; a 50 MB answer does not grow memory); `TestPostManyRecovers`; `TestAlarmFields` (loud: `level=critical`, sound, `volume=10`, `call=1`; muted with volume 3: `volume=3`, no `call`; `bark_alarm_call=false`: no `call`); `TestFalseAlarmFields` (never `call`; volume falls back to the alarm volume); `TestMaskKey`; `TestSendGuardBlocksEveryOtherKey` (lock on one key, jobs to that key and to five others: the mock sees exactly one request, the five results carry the fixed `Err`, no retry rounds for them); `TestSendGuardUnsetOrExpired` (no lock, and a lock that ran out one second ago: all six go out); `TestSendGuardReadPerSend` (the lock is switched on between two `PostMany` calls through a new configuration: the second call obeys it).
- [ ] **Step 2:** Implement. `bash tools/check-go.sh`. Commit — `Go: Bark-Versand in Runden, ASCII und Feldsätze`.

### Task 9: The server's frame — life cycle, control socket, configuration at run time, static files

**Files:**
- Create: `internal/core/core.go`, `internal/config/provider.go` (+ test), `internal/control/{server.go,client.go}` (+ test), `internal/web/{routes.go,respond.go,static.go}` (+ tests), `internal/app/{serve.go,commands.go}` (+ test)
- Modify: `cmd/fw-alarm/main.go`

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
func AssetVersion(path string) string                        // zehn Ziffern aus dem Inhalts-Hash

// control: HTTP über den Unix-Socket (Rechte 0600)
type Hooks struct {
    Check  func(ctx context.Context) string   // voller Prüf-Lauf, seine Zeile
    Health func() Health
    Flush  func(ctx context.Context) error
    Reload func() error
    Stop   func()
}
type Health struct{ Version, State string; SaveOK, ConfigOK bool; Hints []string }   // State: normal | emergency (ein Start aus der Vorfassung ist ein Hinweis, kein Zustand)
func Serve(ctx context.Context, socketPath string, h Hooks, log *slog.Logger) error
func Call(socketPath, command string, timeout time.Duration) (output string, ok bool, err error)   // command: check | health | flush | reload | stop

// app
func Serve(ctx context.Context, configPath, version string) error   // kehrt nach geordnetem Beenden zurück
```

- Command line added here: `fw-alarm serve`, `check`, `health`, `flush`, `reload`, `stop` (each `[--config <file>]`; the last five find the socket through `control_socket`). `health` prints `running=1`, `version=…`, `state=…`, `save_ok=0|1`, `config_ok=0|1`, one `hint=…` line per hint, and exits 0 only for `state=normal`, `save_ok=1`, `config_ok=1`; a server that cannot be reached → `running=0`, exit 1. Until Task 21 `check` answers `NOCH_NICHT_GEBAUT`. `save_ok` = all of `Store.SaveHealth()`.
- **The provider judges a file broken only when it is unchanged at the next check** (same size and modification time 2 s later) — a half-written file from an editor is never acted on (Review Focus 4). `Reload()` judges at once. `state_dir`, `listen` and `control_socket` cannot change in a running server: a new value there is ignored until the restart and named as a hint of `health`. The last-good copy `config.last-good.toml` (0600) is written into the state folder after every valid load.
- `listen`: `host:port` or `unix:<path>` (socket mode 0600; a stale socket file is removed first). The server listens itself. Order at start: configuration → `store.Open` (the lock) → remove a stale control socket → control socket → listener.
- `http.Server`: header timeout 10 s, read 30 s, write 150 s, idle 120 s, header size 64 kB. Paths with repeated slashes are merged before the table is consulted; no redirect, ever. Not in the table → 404 with an empty body.
- Static files from `webassets.FS`: `/manifest.json`, `/assets/<file>` — content type from a FIXED table (`.css` `text/css`, `.js` `application/javascript`, `.json` `application/json`, `.svg` `image/svg+xml`, `.png` `image/png`, `.txt` `text/plain`; no `charset`), `X-Content-Type-Options: nosniff`, `ETag: "<AssetVersion>"` with 304 on a matching `If-None-Match`, folder listings 404. `AssetVersion` = first 8 bytes of the SHA-256 of the embedded file as an unsigned number modulo 10^10, ten digits (the scenarios demand digits).

- [ ] **Step 1: Go tests.** `TestProviderReloadsOnChange`; `TestProviderKeepsLastGood` (broken file, unchanged for two checks → `Get()` still the previous one, `OK()` false, `Problem()` set and free of secrets; repaired → new values, `OK()` true); `TestProviderIgnoresHalfWrittenFile` (broken at one check, valid at the next → `OK()` never false, `OnChange(false)` never called); `TestProviderWarningsApply` (a file with warnings loads and applies); `TestProviderStartsFromCopy` (unreadable `config.toml` + a copy → starts, `OK()` false; neither → error); `TestProviderNeverReadsInGet` (1 000 `Get()` → 0 reads through an injected reader); `TestControlRoundTrip` (every command; socket file mode 0600 on Linux); `TestControlStopEndsServe`; `TestStaleControlSocket` (a left-over socket file does not stop the start); `TestOnlyTableAddressesAnswer` (`/api/health.php`, `/config.toml`, `/state.json`, `/lib/common.php`, `/data/x`, `/.git/config`, `/assets/` → 404; `//api//poll.php` reaches the handler registered for `/api/poll.php` without a redirect); `TestStaticTypes` (every file under `nas_dashboard/assets` has a type in the table); `TestStaticETag`; `TestAssetVersionIsDigits`; `TestServeSecondInstanceAborts` (same state folder → error naming the reason, exit code 1 in `main`); `TestSlowClient` (headers unfinished after 10 s → closed).
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** `bash tools/check-go.sh`. By hand: `fw-alarm serve` with a temp configuration, `GET /manifest.json` → 200, `fw-alarm health` → exit 0, `fw-alarm stop` → the process ends. Commit — `Go: Server-Gerüst mit Steuer-Zugang, Konfiguration zur Laufzeit und statischen Dateien`.

### Task 10: The Go driver in the test bench

**Files:**
- Modify: `tests/backend/harness.mjs` (it keeps the bench, the Bark mock, the helpers for tests and the checks every driver shares; the PHP driver moves unchanged to `tests/backend/harness-php.mjs`, the new one goes to `tests/backend/harness-go.mjs`), `tests/backend/README.md`, `tools/go-green.txt`, `.github/workflows/ci.yml` (job **server-tests**: Go and Node 24, `bash tools/test-go.sh`)
- Create: `tests/backend/go-driver.test.mjs`

**Interfaces — produces** (driver contract for `FW_BACKEND=go`, binary from `FW_GO_BIN`):
- Folder `<root>/state`, file `<root>/config.toml`: the base values of the PHP driver, plus `state_dir`, `listen = "127.0.0.1:<port>"`, `control_socket = "<root>/control.sock"`, `check_interval_seconds = 0`; a small TOML writer for strings, numbers, booleans and lists of strings. The port is picked at random from 20000–29999 and probed (not from the dynamic range: the server is down hundreds of times per file while test connections take dynamic ports).
- `kind: 'go'`, `base`, `dataDir` (= the state folder), `words.configFile = 'config.toml'`.
- `startServer()` starts `serve` and waits for `GET /manifest.json`, retrying while the port is still busy; `stopServer()` runs `fw-alarm stop` and waits for the process to end (kill after 10 s and fail loudly); `hardKill()` kills without warning (the PHP driver throws); `configure(overrides)` rewrites the file and runs `fw-alarm reload`; `writeRawConfig(text)` writes the file as given and waits 5 s for the checker (for "a typo in `config.toml`"); `reset()` stops, empties the state folder, writes the base configuration, starts; `runWatchdog()` runs `fw-alarm check` and resolves with its output.
- `backend.state` writers: stop → change the JSON files of "File formats" → start. Readers: `fw-alarm flush`, then read. `seedLog` appends the entries exactly as given — no `uid`, no `log_at` of its own.
- `storage.fail(what)`: non-empty folder at `selftest.tmp` (`selftest`), `state.json.tmp` (`durable`), `history.jsonl` moved aside and a folder in its place (`history`), `runtime.json.tmp` and `sessions.json.tmp` (`volatile`); `repair` undoes it.
- `backend.files` = raw access to the state folder (`read`, `write`, `corrupt`, `remove`, `exists` by file name) — for `go-*.test.mjs` only, as `backend.data` is for `php-*.test.mjs`.
- The contract is the one the PHP driver implements in `harness.mjs` (Task 1) — read its comments and keep every detail: an `age*` helper throws when the thing to age is not stored yet; marker names and command fields are checked against the fixed lists there; `seedRecipients` takes only valid entries; `boxStatus()` returns the pushed fields as an object with `seen_at` and `via_bridge` as the text `'0'`/`'1'`, without `remote_addr`; `recipients()` returns `{ version, keys }` with PHP's field names. Complete the contract comment where the review of Task 2 found it thin, and implement accordingly: `ageLog` without a matching entry does nothing; `ageRetry(kind, seconds)` takes the kinds of admin messages (`box_offline`, `nas_web`, `outside_watcher` …); `ageSession(cookie, seconds)` takes the cookie as `name=wert`.
- Documents are complete or unreadable (Task 5: a missing field is an error): the driver changes what the server itself wrote, and every entry it adds (a recipient, a session, a command) carries every field of its type.
- In `runtime.json`, `watch` and `throttle` are typed (Task 5): the driver writes exactly the fields `internal/model` defines — an unknown marker name would make the server discard the file as unreadable; `go-driver.test.mjs` gets a case that sets every marker name of the helper list once and finds the server healthy afterwards.
- After a restart no request may fail because of a kept connection: `request()` sends `connection: close` (for both drivers).

- [ ] **Step 1:** `go-driver.test.mjs` (whole file Go only): after `reset()` the server answers `GET /manifest.json` and `fw-alarm health` exits 0; `stopServer()` then `startServer()` answers on the same port; `configure()` changes a value the server then uses (`health` after a broken `writeRawConfig` prints `config_ok=0`, after a good `configure()` `config_ok=1`); `hardKill()` then `startServer()` works; `storage.fail('durable')` leaves a non-empty folder at `state.json.tmp` and `repair` removes it; a second `serve` on the same folder exits non-zero; 50 `request()` calls across 10 restarts never fail on a dead connection.
- [ ] **Step 2:** Implement the driver. `bash tools/test-go.sh tests/backend/go-driver.test.mjs` → pass. `bash tools/test-go.sh tests/backend/session.test.mjs` → the server starts and stops for every case, the cases FAIL with 404 — no hang, no leftover process (`tasklist | grep fw-alarm` prints nothing).
- [ ] **Step 3:** Put `tests/backend/go-driver.test.mjs` on `tools/go-green.txt`; `node --test "tests/backend/*.test.mjs"` (PHP) → still all pass. Commit — `Tests: Treiber für den Go-Server im Prüfstand – anhalten, ändern, starten`.

### Task 11: `web` I — forms, headers, proxy trust, brakes

**Files:** Create `internal/web/{form.go,headers.go,proxy.go,throttle.go,machine.go,budget.go}` + tests.

**Interfaces — produces** (`web.Budget` of "Shared vocabulary" is implemented here):

```go
type Form struct{ /* Paare in der Reihenfolge des Rumpfs */ }
func ParseForm(w http.ResponseWriter, r *http.Request) (*Form, bool)   // false: schon beantwortet (413). urlencoded + multipart, NUR Rumpf, höchstens 64 kB
func (f *Form) Get(name string) string          // letzter Wert; "" wenn er fehlt
func (f *Form) Has(name string) bool
func (f *Form) Each(fn func(name, value string) bool)   // jeder Name einmal, Reihenfolge des ersten Auftretens, letzter Wert
func ClipBytes(s string, n int) string                   // höchstens n Bytes, nie mitten in einem Zeichen
func SecurityHeaders(c *core.Core, next Handler) Handler
func ClientIP(c *core.Core, r *http.Request) string      // PHP: login_client_ip
func ThrottleKey(ip string) string                       // PHP: client_throttle_key (IPv6 als /64)
func IsHTTPS(c *core.Core, r *http.Request) bool         // PHP: request_is_https
func FromTunnel(c *core.Core, r *http.Request) bool      // vertrauter Proxy ODER Verbindung über den Unix-Socket
type Hit struct{ Blocked bool; Count int }
func ThrottleHit(c *core.Core, bucket, who string, max int, window time.Duration) Hit   // PHP: throttle_hit; höchstens 500 Absender
func ThrottleClear(c *core.Core, bucket, who string)
func MachineAuth(c *core.Core, f *Form) bool             // Token aus dem Rumpf; Aufrufer antwortet 403 "FORBIDDEN"
```

- Form details (spec §6.4): a repeated name keeps its first position and its last value; names with brackets (`a[]`, `a[b]`) are skipped; a body of another content type has no fields; every name and value is passed through `strings.ToValidUTF8(…, "�")`.
- Security headers on every dynamic answer, verbatim from the top of `nas_dashboard/lib/common.php` (`X-Frame-Options`, the `Content-Security-Policy` string, `Cache-Control: no-store`, `X-Content-Type-Options`, `Referrer-Policy`, `Permissions-Policy`, `X-Robots-Tag`; `Strict-Transport-Security` only when `IsHTTPS`).
- Brake counters live in `Runtime.Throttle` (volatile): a hit never waits for the disk.

- [ ] **Step 1: Go tests.** `TestFormOrderAndLastValue` (60 fields, urlencoded and multipart); `TestFormSkipsBrackets`; `TestFormOtherContentType`; `TestFormTooLarge` (413); `TestFormMakesValidUTF8`; `TestClipBytes` (`ClipBytes("ää", 3)` = `"ä"`); `FuzzParseForm`; `TestSecurityHeaders`; `TestClientIP` (untrusted sender ignores headers; trusted + `X-Forwarded-For: a, b` → `b`; invalid candidate → the sender; `trusted_proxies` set but not matching → headers ignored); `TestUnixListenerCountsAsTunnel`; `TestThrottleKey` (`::ffff:1.2.3.4` → `1.2.3.4`; a v6 address → its /64); `TestThrottleWindow`; `TestThrottleCap` (501st new sender neither counted nor blocked); `TestMachineAuth` (token only from the body, constant-time compare, upper-case hash in the configuration accepted).
- [ ] **Step 2:** Implement. `bash tools/check-go.sh`; `go test -fuzz=FuzzParseForm -fuzztime=30s ./internal/web`. Commit — `Go: Formulare, Sicherheits-Kopfzeilen, Proxy-Vertrauen und Bremsen`.

### Task 12: `web` II — sessions, password slots, login

**Files:** Create `internal/web/{session.go,verify.go,login.go}` + tests; modify `internal/app/serve.go` (`Provider.OnChange` → `EndAll`).

**Interfaces — produces:**

```go
type Session struct{ User, Role, CSRF string }
func Resume(c *core.Core, w http.ResponseWriter, r *http.Request) (*Session, bool)   // PHP: resume_dashboard_session + refresh_dashboard_cookie
func Begin(c *core.Core, w http.ResponseWriter, r *http.Request, user, role string) *Session
func End(c *core.Core, w http.ResponseWriter, r *http.Request)
func EndAll(c *core.Core)                                // Konfiguration fehlerhaft
func SweepSessions(c *core.Core)                         // abgelaufene löschen (der Prüf-Lauf ruft das)
type LoginResult int                                     // LoginOK | LoginFailed | LoginBlocked (429) | LoginForbidden (403) | LoginBusy (503)
func Login(c *core.Core, w http.ResponseWriter, r *http.Request, f *Form) (LoginResult, string)   // string = Benutzername für die Fehlerseite
var OnLogin func(c *core.Core, role, user, ip string)    // gesetzt in Aufgabe 15
var OnLockout func(c *core.Core, ip string, windowSeconds int)
```

- Sessions (spec §8): id = 32 bytes from `crypto/rand`, stored under its SHA-256; cookie `boss925_dashboard` with the attributes of PHP `dashboard_cookie_options()`, path `/`, 12 h, renewed on every authenticated request; bound to `Config.AuthStamp(role)`; `last_seen` written at most once per minute; idle more than 43 200 s → gone; a forged id creates nothing and sets no cookie; logout removes session and cookie. A `last_seen` in the future counts as now. A broken configuration ends all sessions.
- Login (PHP `index.php`): `Sec-Fetch-Site` rule → brake blocked? (429) → password slot → count the attempt (`ThrottleHit`, before the check) → exactly ONE `config.VerifyPassword` per attempt → failure: 1 s pause outside the slot → success: clear the counter, new id. **An unknown user is checked against the admin's configured hash and the result discarded** (as `index.php:78-83`: a fixed dummy hash of another cost would reveal by its timing whether the name exists). Slots: at most two checks at once, at most one per sender, waiters served in turn per sender, at most 5 s → `LoginBusy`; such an attempt is not counted. Login works in emergency mode and when nothing can be saved.

- [ ] **Step 1: Go tests.** `TestSessionRules` (forged id; idle; changed password hash ends only that account's sessions; `last_seen` once per minute; new id at every login; logout); `TestSessionClockBackwards` (clock set back 2 h → the session is still valid and does not live longer than 12 h after the next request); `TestEndAll`; `TestSweepSessions`; `TestLoginOneVerifyPerAttempt` (through an injected verifier that records the hash it got: one call each for admin, reader and an unknown user — and for the unknown user it is the admin's hash); `TestLoginSecFetchSite` (`cross-site`, `same-site` → forbidden; `same-origin`, `none`, absent → allowed); `TestLoginCountsBeforeCheck`; `TestLoginSlots` (three senders, 10 attempts each with a verifier that takes 100 ms: never more than two at once, never two of one sender, order fair per sender; a waiter past 5 s → `LoginBusy` and not counted); `TestLoginSleepOutsideSlot`; `TestLoginWorksWhenNothingSaves` (`FaultFS` failing everything → `LoginOK` and a usable session); `TestLoginWorksInEmergencyMode`.
- [ ] **Step 2:** Implement. `bash tools/check-go.sh`. Commit — `Go: Sitzungen, begrenzte Passwortprüfungen und die Anmeldung`.

### Task 13: Pages, action table, `status`

**Files:** Create `internal/web/{pages.go,errors.go}` + tests, `internal/web/templates/{login.html,app.html}`, `internal/web/testdata/*.html`, `internal/dashapi/{actions.go,status.go}` + tests; modify `internal/app/serve.go` (routes `/`, `/index.php`, `/classic.php`, `/dashboard_api.php`), `tools/go-green.txt`.

**Interfaces — produces:**

```go
// web
func IndexHandler(c *core.Core) Handler      // GET: Anmeldeseite bzw. App-Hülle; POST action=login / action=logout (PHP: index.php)
func ClassicHandler() Handler                // 302 Location: index.php, kein Cookie
func StorageError(w http.ResponseWriter, err error, jsonAPI bool)   // Satz laut "Texts this plan fixes"; ErrStorage/ErrLate/ErrDropped: Status 500, code "storage"; ErrEmergency: wie PHP bei unlesbarer Liste (ok=false mit dem Notbetrieb-Satz)
// dashapi
type Action struct{ Post, CSRF, Admin bool; Handle func(x *Ctx) }
type Ctx struct{ Core *core.Core; W http.ResponseWriter; R *http.Request; Form *web.Form; Sess *web.Session }
func Register(name string, a Action)         // die Tabelle mit allen 23 Aktionen steht HIER; spätere Aufgaben setzen nur Handle
func Handler(c *core.Core) web.Handler       // 401 → 405 → 400 → 403 csrf → 403 readonly → Handle (PHP: DASHBOARD_ACTIONS)
func StatusPart(name string, fn func(x *Ctx) any)
```

- Pages through `html/template`: login page (with error and kept user name; status 503 with the fixed sentence for `LoginBusy`, 429 and 403 as PHP) and app shell as PHP's `render_login_page()` / `render_app_page()`; meta tags `csrf`, `role`, `user`, `public-base`, `mute-max` as there. Capture the golden files once with `php -r` from `nas_dashboard/lib/shell.php` (login plain, login with error and kept user name, shell for admin and reader) into `internal/web/testdata/`. Equal bytes are required for these golden inputs after `?v=<digits>` is normalised; `html/template` escapes quotes and `+` differently from PHP (`&#39;` vs `&#039;`) — that difference is allowed and the golden inputs avoid such characters.
- The CSRF token is accepted from the header `X-CSRF-Token` and from the form field `csrf`.
- `status` delivers here: `ok`, `status` (the last push without `remote_addr`), `seen_age_seconds`, `offline` (with the 600 s floor while `alarm_pending` is `1`), `timezone`, `csrf`, `server_time`; the keys `command`, `demo`, `nas_keys_version`, `demo_sync_ok`, `device_hint`, `watchdog` are present with empty values until Tasks 16, 17, 18, 21 register them. An action without `Handle` answers 501 `{"ok":false,"message":"Noch nicht gebaut."}`.

- [ ] **Step 1: Go tests.** `TestPagesEqualPHP` (the four golden files); `TestLoginPageStatuses` (failed 200, blocked 429, forbidden 403, busy 503 with the sentence); `TestClassicRedirect`; `TestStorageError` (each error value: sentence, status, JSON and text form, no path in any); `TestActionTable` (the 23 names with the three flags of `DASHBOARD_ACTIONS`); `TestAccessOrder` (401 → 405 → 400 → 403 `csrf` → 403 `readonly`); `TestCSRFFromHeaderAndField`; `TestStatusKeySet` (exactly the keys of PHP's answer).
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** `bash tools/test-go.sh tests/backend/session.test.mjs tests/backend/dashboard-access.test.mjs` → `session` passes, including the idle case that PHP skips; `dashboard-access` passes except the case about `offline_alert`/`web_alert`. New file `go-session.test.mjs` (Go only): "ein anonymer Aufruf legt keine Sitzung an" — after `GET /index.php`, `GET /dashboard_api.php?action=status`, a wrong login and a request with a forged cookie, `backend.files.read('sessions.json')` holds no session (the check `session.test.mjs` could only make for PHP's own folder). `session` and `go-session` go on `tools/go-green.txt`. `bash tools/check-go.sh`. Commit — `Go: Seiten-Hüllen, Zugangsregeln aller Aktionen und die Status-Antwort`.

### Task 14: History for the dashboard (`logview`, `log_view`)

**Files:** Create `internal/logview/{events.go,view.go}` + tests, `internal/dashapi/log.go`; modify `tools/go-green.txt`.

**Interfaces — produces:**

```go
type Field struct{ Name, Type, Path string; Optional bool; Default any }   // Type: str | int | bool
type Event struct{ Cat string; Core, Admin bool; Fields []Field }           // Cat: alarm | keys | system | command
var Events map[string]Event                       // PHP: log_event_types + state_rewound (system, Admin) + send_interrupted (alarm, Core)
func InFilter(e model.Entry, filter string) bool  // all | alarm | alarm_core | keys | system
func View(e model.Entry, isAdmin bool) (map[string]any, bool)   // PHP: log_event_view
func Read(c *core.Core, limit int, filter string, isAdmin bool) []map[string]any   // was die Rolle nicht sehen darf, fällt VOR dem Abzählen weg
```

- [ ] **Step 1: Go tests.** `TestEventTableComplete` (every name in `model.AllEvents` has a row and the reverse; `demo_repaired` is still there for old entries; `relay_alarm.source` defaults to `relay`; `notify_changed` and `setting_changed` admin only; the send events are `Core`); `TestViewTypes` (`"minutes":"7"` → 7; missing optional field absent; unknown event not shown; `results` never shown); `TestOldEntriesWithoutUID` (a PHP line without `uid` and without `demo` is shown); `TestHiddenBeforeCounting`.
- [ ] **Step 2:** Implement; register `log_view` (limit clamped to 1…100, unknown filter = `all`).
- [ ] **Step 3:** `bash tools/test-go.sh tests/backend/dashboard-log.test.mjs` → pass; on the list. `bash tools/check-go.sh`. Commit — `Go: Verlauf – Ereignis-Tabelle, Filter, Ausgabe je Rolle`.

### Task 15: `notify` — messages to the admin

**Files:** Create `internal/notify/{types.go,settings.go,send.go,login.go}` + tests, `internal/dashapi/notify.go`; modify `internal/app/serve.go` (set `web.OnLogin`, `web.OnLockout`), `tools/go-green.txt`.

**Interfaces — produces:**

```go
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
func CheckSetting(cfg *config.Config, kind, key, value string) error               // für notify_set und die Übernahme
```

- Rules as `CLAUDE.md` "Meldungen an den Admin": `off` only for a switched-off kind or an empty status key; an invalid status key and an unknown kind are `failed`; the deputy alone accepting is `sent` with `PrimaryFailed`; defaults follow the configuration to the second; quiet hours cap the urgency except for kinds with `ignore_quiet`. The hint of the kind `nas_web` is the new text of "Texts this plan fixes".

- [ ] **Step 1: Go tests.** `TestQuietHoursAcrossMidnightAndDST` (22→7: 23:30 and 06:59 true, 07:00 false; equal hours never quiet; on 2026-03-29 and 2026-10-25 the local hour decides); `TestSendResults` (no status key → `off`; kind off → `off`; invalid status key → `failed`; unknown kind → `failed`; status key fails and the deputy accepts → `sent` with `PrimaryFailed`; deputy equal to the status key not sent twice); `TestSettingsDefaults` (config values to the second; unknown stored value → default); `TestCheckSetting`; `TestNasWebHint`; `TestLoginNoticesAfterTheAnswer` (both run through `Core.Bg`).
- [ ] **Step 2:** Implement; register `notify_list`, `notify_set` (stores only what is set explicitly; unknown kind/key/value rejected, nothing stored), `notify_test`.
- [ ] **Step 3:** `bash tools/test-go.sh tests/backend/dashboard-notify.test.mjs` → pass, including the case "302 never waits for Bark" that PHP skips; on the list. `bash tools/check-go.sh`. Commit — `Go: die einstellbaren Meldungen an den Admin`.

### Task 16: Recipients, mute, demo mode, settings, `api/keys.php`

**Files:** Create `internal/mutes/mutes.go` + test, `internal/dashapi/{keys.go,mute.go,demo.go,settings.go}` + tests, `internal/box/{keys.go,respond.go}` + tests; modify `tools/go-green.txt`.

**Interfaces — produces:** `mutes.Expire` of "Shared vocabulary" (one `Update` with `model.ExpireStaleMutes`, only when the snapshot shows an overdue mute; afterwards, through `Core.Bg`, the info "wieder laut" to those affected and the optional message `mute_expired`), and

```go
// box
func Nonce(raw string) string                 // erste 80 Zeichen, dann nur A-Z a-z 0-9 _ -
func KeysVersion(c *core.Core) int            // 0 im Notbetrieb
```

- Actions: `keys_list` (`mutes.Expire` first — a failure there changes nothing about the answer; effective `muted`, `mute_overdue`, `mute_until`, phone and `mute_token` only for the admin; emergency → `code=keys_corrupt` with the fixed text), `keys_add`, `keys_delete`, `keys_rename`, `keys_replace_key`, `keys_set_phone`, `keys_new_mute_token`, `keys_mute`, `keys_set_mute_permanent`, `keys_set_mute_volume`, `demo_set`, `settings_list`, `settings_set` — rules, messages and history entries as the PHP handlers in `dashboard_api.php`; which of them raise `keys_version` exactly as `CLAUDE.md` says (`demo_set` off while off still raises it). **Every bump passes the `keys_version` of the box's last status push** (`State.Bump(boxVersion)`, `ExpireStaleMutes(…, boxVersion)`; 0 when no status is known) — as PHP's `bump_keys_version`: the lift after a status push (Task 17) runs beside the answer and may fail, so each bump protects itself. Each is ONE `Store.Update` with `web.Budget(web.BudgetUI)`; a failure answers `web.StorageError`. `demo_set` changes demo state and version in the same update — the code `inconsistent` no longer exists.
- Status parts: `demo`, `nas_keys_version`, `demo_sync_ok`.
- `api/keys.php`: machine auth → `mutes.Expire` within `BudgetBox` (a failure changes nothing about the answer) → plain text `ok`, `nonce`, `version`, `count`, `key<i>`, `muted<i>`, `mutevol<i>` from ONE snapshot; emergency → `version=0`, `count=0`.

- [ ] **Step 1: Go tests.** `TestExpireOnlyWhenStale` (nothing overdue → no save); `TestExpireMessagesAfterUpdate`; `TestDemoSetIsOneChange` (with `FaultFS` failing the save: neither demo state nor version changed; succeeding: both in one snapshot); `TestDemoOffIsIdempotentAndBumps`; `TestStorageSentences` (`ErrStorage` → the known sentence, `ErrLate` → "dauert zu lange", both status 500 and `code: storage`; `ErrEmergency` → the emergency sentence); `TestKeysAnswerFromOneSnapshot` (a `demo_set` racing 1 000 `api/keys.php` calls: every answer is either the full list under the old version or the demo list under the new one); `TestKeysListEmergency`; `TestPermanentBumpsOnlyWithMuteChange`; `TestRenameDemoRecipientUpdatesLabel`; `TestReplaceKeyOfDemoRecipientRejected`.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** `bash tools/test-go.sh tests/backend/dashboard-keys.test.mjs tests/backend/dashboard-settings.test.mjs tests/backend/box-keys.test.mjs` → `dashboard-settings` passes (on the list); the other two pass except the cases the table names. Extend `go-driver.test.mjs`: `storage.fail('durable')` → `keys_add` answers 500 `code: storage` and `fw-alarm health` says `save_ok=0`; after `repair` and a successful change `save_ok=1`. `bash tools/check-go.sh`. Commit — `Go: Empfängerliste, Arbeitsmodus, Demo-Modus und die Liste für die Box`.

### Task 17: Status push

**Files:** Create `internal/model/status.go` + test, `internal/box/{status.go,devices.go}` + tests, `tests/backend/go-box.test.mjs`, `tests/backend/go-storage.test.mjs`; modify `tools/go-green.txt`.

**Interfaces — produces:**

```go
// model (BoxStatus und Commands gibt es seit Aufgabe 5)
func LastAlarmSource(cmds Commands, sent string, now time.Time) string   // "relay" | "remote" (PHP: last_alarm_source); sent = alarm_source, das die neue Firmware selbst meldet
type PushVerdict struct {
    Stale      bool          // veralteter Push: nur bestätigen
    RelayAlarm *Entry        // relay_alarm mit source relay|remote, sonst nil
    Restart    *RestartInfo  // Neustart erkannt (für box_restart), sonst nil
    Foreign    bool          // anderes Board als box_device_id
}
func EvaluatePush(prev *Runtime, cmds Commands, f Fields, viaBridge bool, remote string, now time.Time, boxDeviceID string) (next func(d *Runtime), v PushVerdict)
func DeviceHint(rt *Runtime, boxDeviceID string, now time.Time) string   // PHP: device_hint
```

- The push keeps the first 40 fields, each value cut to 300 bytes with `web.ClipBytes` (never inside a character), key characters as PHP `clipped_post_fields`, never the token. Rules as `nas_dashboard/api/status.php`: stale push (arrived earlier than the stored one, smaller uptime, < 60 s apart) → acknowledged only; restart (`uptime_s` fell, or the start moment jumped back, only when the uptime is not longer than the gap to the previous push); relay alarm when `last_alarm`/`alarm_seq` of THIS device changed (per `device_id`, the 10 newest; `devices` the 10 newest). Source of the alarm: what the firmware reports in `alarm_source` when it does; otherwise `remote` when the box acked an ALARM command as `executed` within 300 s of its delivery, or one was delivered ≤ 60 s ago and is not acked; else `relay`. `box_device_id` set → only that board counts, others only in `devices`.
- `DeviceHint`: the variants of PHP `device_hint()` (`common.php:1041-1071`) word for word, except that `config.php` reads `config.toml`.
- The handler touches volatile data only: answer 200 at once (the text PHP sends), always. After the answer (`Core.Bg`): the message `box_restart`; and when the EXPECTED board pushes a `keys_version` higher than the server's and below 2^31 → one `Update` (5 s budget) that sets `keys_version` to the pushed value + 1, so the box fetches the list again.
- Status part `device_hint`.

- [ ] **Step 1: Go tests.** `TestEvaluatePush` (table: first push; relay alarm by `last_alarm`; by `alarm_seq`; change to `nie` = reboot, no alarm; old firmware without `alarm_seq`; source reported by the firmware; `remote` by acked command; `remote` by delivered command; an ack as `skipped_busy` is not `remote`; stale push; restart by uptime; restart by start moment; no restart when the uptime exceeds the gap; foreign board; the caps of 10 boards and 10 seen alarms); `TestClippedFields` (40 fields; 300 bytes; a cut that would split `ä` ends before it; the token never stored); `TestDeviceHintTexts`; `TestVersionLiftOnlyExpectedBoardAndPlausible`; `TestStatusNeverTouchesDurable` (`FaultFS` hanging on `state.json.tmp` → answer within 50 ms).
- [ ] **Step 2:** Implement.
- [ ] **Step 3: Go-only black-box cases** (both files Go only). `go-box.test.mjs`: "meldet die Box eine höhere Listen-Version, hebt der Server seine sofort darüber" (push `keys_version: 41` → `waitFor` `listVersion()` 42). `go-storage.test.mjs`: "Status wird angenommen, wenn nichts gesichert werden kann" (`storage.fail('volatile')` and `('durable')` → 200, the dashboard shows the push).
- [ ] **Step 4:** `bash tools/test-go.sh tests/backend/box-status.test.mjs tests/backend/go-box.test.mjs tests/backend/go-storage.test.mjs` → pass; all three on the list. `bash tools/check-go.sh`. Commit — `Go: Status-Push der Box mit Relaisalarm, Neustart und fremden Boards`.

### Task 18: Commands (`poll`, `ack`, `enqueue`, `cancel`)

**Files:** Create `internal/model/command.go` + test, `internal/box/{poll.go,ack.go}` + tests, `internal/dashapi/command.go`; modify `tests/backend/go-storage.test.mjs`, `tools/go-green.txt`.

**Interfaces — produces:**

```go
// model – reine Regeln, alle auf dem Entwurf einer Änderung
func (s *State) Enqueue(typ, by string, now time.Time, lockSeconds int) (Command, string)   // "" = angelegt, sonst der Ablehnungs-Satz
func (s *State) ExpirePending(now time.Time, ttl int) bool                                    // pending zu alt → expired
func (s *State) NoteBoxCommandID(last int) bool           // PHP api/poll.php:61-77: next_id über die Nummer der Box heben, wartenden Befehl neu nummerieren; true = etwas geändert
func (s *State) Deliver(to string, now time.Time) *Command           // pending → delivered; nil, wenn nichts wartet
func (s *State) Ack(id int, by, result, message string, now time.Time) AckKind   // AckFirst | AckRepeat | AckLate | AckUnknown
func (s *State) Cancel(by, reason string, now time.Time) (Command, bool)
func (s *State) DemoAlarmStillSafe(cmd Command) bool      // PHP: demo_alarm_still_safe
```

- Poll (spec §6.2), one `web.Budget(web.BudgetBox)` for everything: machine auth (else 403 `FORBIDDEN`) → `mutes.Expire` → for the expected board: one `Update` that applies `NoteBoxCommandID`, expires an old pending command, cancels an ALARM command whose demo state changed, and marks the waiting one `delivered`. **Only when that update returned nil does the answer carry the command.** Every other outcome: 200 `type=NONE`. The answer always carries `keys_version` (0 in emergency mode). A poll for which none of these steps changes anything saves nothing.
- Ack: always `OK`. A repeated equal ack is only confirmed; a late one writes `late_ack` carrying the type from `recent_types`; an ack that cannot be saved leaves the command `delivered`. `ack_result` cut to 80 and `ack_message` to 200 bytes with `web.ClipBytes`.
- `enqueue` and `cancel` as the PHP handlers (ALARM only as the demo tool, secured by `DemoBlockReason` when created and again when delivered); status part `command` — a pending command past its TTL is shown as `expired`, and the expiry is saved beside the answer (5 s budget).

- [ ] **Step 1: Go tests.** `TestCommandLifeCycle` (table over every transition, its fields and its history entry); `TestNoteBoxCommandID` (the cases of `box-poll-ack.test.mjs` "Nummern"); `TestPollDeliversOnlyWhenSaved` (`FaultFS`: save hangs → answer `type=NONE` after about 1.5 s; after the release the command is `delivered` and no later poll carries it; save fails → the command still waits and the next poll delivers it); `TestTwentyPollsOneCommand`; `TestPollWritesNothingWhenIdle` (1 000 polls with the same `last_command_id` → no save of `state.json`); `TestAckAlwaysOK` (failing disk); `TestLateAckCarriesType`; `TestRecentTypes` (after 25 commands the 20 highest ids); `TestDemoAlarmCancelledAfterModeChange`.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** `go-storage.test.mjs`: "Poll liefert ohne Speichern keinen Befehl aus" (`enqueue` → `storage.fail('durable')` → poll answers 200 `type=NONE` with `keys_version` → `repair` → the next poll carries the command). `bash tools/test-go.sh tests/backend/box-poll-ack.test.mjs tests/backend/box-keys.test.mjs tests/backend/go-storage.test.mjs` → pass; the first two on the list. `bash tools/check-go.sh`. Commit — `Go: Befehle an die Box – höchstens einmal ausliefern, quittieren, verfallen`.

### Task 19: Shortcut link (`api/mute.php`)

**Files:** Create `internal/mutelink/{handler.go,detect.go,page.go}` + tests, `internal/mutelink/templates/mute.html`, `internal/mutelink/testdata/*.html`; modify `tests/backend/go-storage.test.mjs`, `tools/go-green.txt`.

- Behavior as `nas_dashboard/api/mute.php`: `t=<token>` and the old form `key=<Bark key>`; `state=on|off`; text answer for shortcuts and scripts, page for browsers (`Sec-Fetch-Dest: document` or `Accept: text/html`, GET only) with a 303 to `mute.php?done=…` that carries no token; HEAD and the known preview fetchers switch nothing; only failures are counted against the brake (bucket `mute`, limits as PHP), a valid token always works; the link can end "dauerhaft" but never set it; all through `model.ApplyMute` in ONE `Update` with `web.Budget(web.BudgetLink)`; `mutes.Expire` first; history entry and the optional message `mute_changed` after the answer (`Core.Bg`). Storage failure — the save fails, the deadline passes, or the server is in emergency mode: the answers of PHP `mute_storage_failed` (status 500), and **nothing is counted against the brake** (in emergency mode the token cannot be looked up; that must not look like a wrong token).

- [ ] **Step 1: Go tests.** `TestPreviewDetection` (methods other than GET/POST; `Sec-Purpose`, `Purpose`, `X-Purpose`, `X-Moz` with `prefetch`/`prerender`/`preview`; every user-agent fragment of the expression in `api/mute.php`, any case; `Shortcuts/1 CFNetwork/1410 Darwin/22.6.0` is NOT a preview); `TestWantsHTML`; `TestPageEqualsPHP` (golden files from `mute_page()` for `ok`/`stale`/`crit`, `?v=` normalised); `TestRedirectQuery`; `TestTokenNowhere` (neither in the redirect, the page nor the history); `TestLinkInEmergencyMode` (500 with the storage answer, the brake counter unchanged); `TestLinkDeadline` (hanging save → the storage answer after about 2 s; after the release the change counts); `FuzzMuteQuery`.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** `go-storage.test.mjs`: "Kurzbefehl-Link ohne Speichern: 500, nichts gezählt" — and in emergency mode the twin of the `mute-link` case in `php-storage.test.mjs` ("Liste samt Sicherung unlesbar …") with everything that still holds: five calls, each 500 with `ok=0\nerror=speichern fehlgeschlagen\n` and `no-store`; the browser call 500 with the page "Nicht gespeichert"; `muteFailures()` stays 0. `bash tools/test-go.sh tests/backend/mute-link.test.mjs tests/backend/dashboard-keys.test.mjs tests/backend/go-storage.test.mjs` → pass; the first two on the list. `bash tools/check-go.sh`. Commit — `Go: Kurzbefehl-Link samt Browser-Seiten, Vorschau-Sperre und Bremse`.

### Task 20: Alarm, Entwarnung, test push

**Files:** Create `internal/dashapi/{broadcast.go,alarm.go,keystest.go}` + tests; modify `tests/backend/go-storage.test.mjs`, `tools/go-green.txt`.

**Interfaces — produces:** actions `alarm`, `false_alarm`, `keys_test`;

```go
type Recipients struct{ Targets []model.Target; Demo, UsedFallback bool; Source string }   // Source: list | rescue | config
func RecipientsNow(c *core.Core) Recipients   // EINE Momentaufnahme: Liste und Demo-Zustand; Notbetrieb → Store.Rescue() und model.Rescue.Targets(now, mute_max der Konfiguration) (überfällig Stumme zählen auch dort als laut), sonst Not-Liste der Konfiguration
```

- Order of a send, as PHP `run_nas_broadcast()`: one snapshot → `expect_demo` (missing or different → 409 `mode_changed`, nothing sent) → `only_ids` (present but unusable → 400; empty intersection → nothing sent; it can only narrow) → `Store.LogAndWait(<event>_started, 300 ms)` → `Bark.PostMany` with a context that ignores the caller going away → `mutes.Expire` (5 s budget of its own) → `Store.Log(<event>)` with `results`, `demo`, `resend`, and `started_uid` = the `uid` of the started entry → answer with `started_at`. Fields, texts and the stamp of a resend (`orig_at` up to 24 h; else the newest `*_started` without `resend` up to 6 h; else now; suffix ` (verspaetet zugestellt)`) as PHP `bark_broadcast()`/`resend_stamped_body()`. The fallback sentence names `config.toml`.
- Nothing in this path needs a save to succeed, and nothing in it opens a file.
- While the send lock holds back at least one recipient of a send, the answer's `message` starts with the fixed sentence `Probe-Sperre aktiv bis <HH:MM> – gesendet wurde nur an freigegebene Keys. `; the held-back recipients appear in `results` as not reached with the fixed `err`. A resend offered for them works once the lock is off.

- [ ] **Step 1: Go tests.** `TestSnapshotUsedForCheckAndSend` (a `demo_set` between check and send does not change who gets it); `TestBroadcastWhenNothingSaves` (`FaultFS` failing everything: sent, answered; the history lines are simply missing); `TestStartedOnDiskBeforeFirstSend` (the append of `*_started` precedes the first HTTP request; with a hanging disk the first request leaves after about 300 ms); `TestBroadcastSurvivesClientDisconnect` (cancel the request after the first recipient answered, the mock delays the second by 1 s → both served, result logged); `TestOnlyIDsNeverWidens` (demo mode + `only_ids` of another recipient → nothing sent); `TestResendStamp`; `TestEmergencyRecipients` (rescue copy → those; rescue copy with a demo recipient → only him; a copy without a valid key → the list of the configuration; no copy → the list of the configuration; neither → nothing sent and the answer says why); `TestNoUnbuiltActionLeft` (every name of the action table has a `Handle`).
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** `go-storage.test.mjs`: "der Alarm geht raus, wenn nichts gesichert werden kann" (`fail('durable')`, `('volatile')`, `('history')` together); "Notbetrieb: Alarm geht an die Not-Kopie, ohne sie an die Not-Liste" (two recipients → stop → `backend.files.corrupt('state.json')` and `('state.prev.json')` → start → alarm reaches both from the copy; remove the copy, restart → the key of `bark_keys_alarm`, and the answer carries the fallback sentence with `backend.words.configFile`); in each: status 200, `ok`, exactly one request per recipient with the alarm fields and the stamped body. "Notbetrieb mit von Hand verdorbener Not-Kopie: die gültigen, einmaligen, ersten 10" (the twin of the `alarm-send` case of that name in `php-storage.test.mjs`: the same spoiled entries written into `recipients.rescue.json` → 10 requests in list order, result ids in that order, message `10/10 Empfänger erreicht.`). New file `go-guard.test.mjs` (Go only): "Probe-Sperre: Alarm an alle erreicht nur den freigegebenen Key" (three recipients, lock on one of them for 30 min: the mock sees one request, the answer starts with the lock sentence and lists two as not reached; the same for Entwarnung, for `keys_test` to another recipient and for the info \"wieder laut\" to another recipient); "abgelaufene Probe-Sperre: alles geht wieder" (`send_guard_until` one minute ago); "eine Probe-Sperre von mehr als 3 Stunden wird nicht angenommen" (`writeRawConfig` → `config_ok=0`, the earlier configuration keeps applying). `bash tools/test-go.sh tests/backend/alarm-send.test.mjs tests/backend/go-storage.test.mjs tests/backend/go-guard.test.mjs` → pass; `alarm-send` and `go-guard` on the list; then `bash tools/test-go.sh`. `bash tools/check-go.sh`. Commit — `Go: Alarm an alle, Entwarnung und Test-Push direkt an Bark`.

### Task 21: The checker I — schedule, sign of life, `api/alive.php`

**Files:** Create `internal/watch/{watcher.go,schedule.go,away.go,statuspart.go}` + tests, `internal/alive/alive.go` + test; modify `internal/app/serve.go`, `tests/backend/go-storage.test.mjs`, `tools/go-green.txt`.

**Interfaces — produces:**

```go
func New(c *core.Core, startedAt time.Time) *Watcher
type Section func(ctx context.Context, w *Watcher, now time.Time) (token string)   // ein Abschnitt des vollen Laufs
func (w *Watcher) AddFull(s Section)                      // Aufgaben 22 und 23 hängen ihre Abschnitte an
func (w *Watcher) AddMinute(s Section)
func (w *Watcher) RunFull(ctx context.Context) string     // die Zeile, mit " | " verbunden; läuft schon einer, wartet der Aufruf und bekommt dessen Zeile
func (w *Watcher) RunMinute(ctx context.Context)
func (w *Watcher) Run(ctx context.Context)                // Zeitplan, blockiert bis ctx endet
func (w *Watcher) ApplyStart(info store.StartInfo)        // Auszeit-Regel: setzt silence_from
func (w *Watcher) BoxSilentSince(now time.Time) (since time.Time, known bool)       // spätere von letztem Status und silence_from
func (w *Watcher) WatcherSilentSince(now time.Time) (since time.Time, known bool)
func (w *Watcher) Notify(ctx context.Context, kind, title, body string, now time.Time) string   // notify.Send + Sperrfrist: "sent" | "failed" | "off" | "wait"
func CronAge(c *core.Core, startedAt, now time.Time) int64
```

- **Schedule** (spec §6.5): a sign of life (`Store.Lifebeat`) every minute, whatever `check_interval_seconds` says, and once more at shutdown; first full run 60 s after the start, then every `check_interval_seconds`; every minute in between `RunMinute`; `check_interval_seconds = 0` → no automatic run of either kind. Elapsed time comes from `Clock.After`, never from comparing wall-clock values. Runs never overlap.
- **Away rule:** at a start whose last sign of life is older than 90 s — or missing — `runtime.silence_from` is set to the start time; silence of box and outside watcher counts from the later of their last sign and `silence_from` (it stays in `runtime.json`, so a second restart does not undo it). A marker or time stamp in the future counts as now.
- **`Notify`** is the one way the checker sends: after a failed send the same kind is not tried again before 15 min have passed (`watch.retry_after[kind]`, result `wait`) — also in the minute tick; it records `last_send_ok_at` / `last_send_failed_at`.
- `CronAge` = age of the last finished full run; before the first one after a start: the last run from `runtime.json`; without one: the time since the start (never −1).
- `api/alive.php` as the PHP file: POST only, the watcher's own token, 404 when it equals the machine token, lines `ok`, `nonce`, `stored`, `box_age_s`, `cron_age_s`, `cron_configured`, `nas_send_failed`; it touches volatile data only and never waits. `stored` = `1` unless the last save of the volatile data failed.
- Status part `watchdog` as PHP builds it in `dashboard_api.php` (without `offline_alert`/`web_alert`/`outside_alert` for the reader); with `check_interval_seconds = 0` and no run for more than 35 min the frontend shows the checker as standing — nothing special on the server.
- Until Task 22 the full run holds only the housekeeping sections added here (`mutes.Expire`, expired commands, `web.SweepSessions`), and its line is just their tokens; it records `last_run_at` like every full run.

- [ ] **Step 1: Go tests** (`clock.Fake`). `TestScheduleFirstFullRun` (nothing before 60 s; one at 60 s; the next at 60 s + 900 s); `TestMinuteTick` (a minute section runs every minute, a full section only in full runs); `TestRunsNeverOverlap` (a second request during a running one gets the same line, the sections ran once); `TestLifebeatWithoutChecks` (`check_interval_seconds = 0`: sign of life every minute, no run); `TestAwayRule` (last sign of life 600 s old, box status 1 200 s old → `BoxSilentSince` is the start; after a second restart 30 s later still the first start; last sign of life 30 s old → the status time); `TestFutureMarkersCountAsNow`; `TestClockJumps` (wall clock back 3 h or forward 26 h: one run per interval of elapsed time, never a burst); `TestNotifyRetryNotBefore15Min` (Bark down: over 60 simulated minutes of minute ticks at most 5 requests for one kind); `TestMarkersSurviveRestart`; `TestCronAge` (the three cases); `TestAliveAnswer` (fields; equal tokens → 404; GET → as PHP); `TestAliveStoredFollowsLastSave`; `TestWatchdogStatusPartPerRole`.
- [ ] **Step 2:** Implement; `fw-alarm check` now returns the line.
- [ ] **Step 3:** `bash tools/test-go.sh tests/backend/alive.test.mjs tests/backend/dashboard-access.test.mjs` → pass; both on the list. `go-box.test.mjs`, the twins of the three `alive` cases in `php-storage.test.mjs` with everything that still holds: "alive.php: sieben Zeilen" (same lines and headers, `cron_age_s` ≥ 0); "cron_age_s: vor dem ersten Lauf die Zeit seit dem Start, danach sein Alter"; "cron_configured=0 ohne bark_key_status (leer und nur Leerzeichen)". `go-storage.test.mjs`: "`stored` meldet das letzte Sichern" (`storage.fail('volatile')` → `waitFor` a call that answers `stored=0`; after `repair` → `stored=1` again). `bash tools/check-go.sh`. Commit — `Go: Prüf-Lauf im Server – Zeitplan, Lebenszeichen und die Antwort für den Außen-Wächter`.

### Task 22: The checker II — the sections of the full run

**Files:** Create `internal/watch/{selfcheck.go,offline.go,outside.go,reminders.go,health.go,report.go,texts.go}` + tests, `internal/watch/testdata/*.txt`, `tests/backend/go-watch.test.mjs`; modify `internal/app/serve.go`, `tools/go-green.txt`.

**Interfaces:** Consumes `Watcher.AddFull`, `AddMinute`, `Notify`, `BoxSilentSince`, `WatcherSilentSince` of Task 21; `Store.SelfTest`, `Store.Resave`. Produces the sections, registered in `app` in this order.

- **Full run** = the sections of `nas_dashboard/cron/check_offline.php` in its order, each with its own markers (the three outage flows stay separate, as `CLAUDE.md` demands): storage self-test → box offline / back → outside watcher silent → reminders (demo, mutes daily, permanent mutes weekly) → box health → weekly report; then the housekeeping of Task 21. Output tokens verbatim (`WEB_OK`, `NOTIFIED_OFFLINE`, `OHNE_PUSH (im Dashboard abgeschaltet): …` …). The line goes to the journal, to `fw-alarm check` and into `watch.last_run_line`. Rules that COUNT runs count full runs only. Every rule of `CLAUDE.md` "Meldungen an den Admin" (1)–(7) holds unchanged unless listed below.
- **The minute tick** runs only "box offline / back".
- **Storage self-test:** `Store.SelfTest()` — it has no context and does real disk calls, so the section runs it beside (through `Core.Bg`) and waits at most 10 s for its answer; no answer in time = the step `write` failed (a hanging disk must not hang the check run). While it reports `save`, `Store.Resave` first and test again (a single failed save must not look like a broken disk for hours). Failing in 2 full runs in a row → "Dashboard DEFEKT" with the fixed text, reminder every 24 h, once "wieder OK" (PHP's text); events `nas_web_failed`/`nas_web_recovered` as today.
- **"Box OFFLINE"** (text as PHP `box_offline_notice_text` without the branch that blames the server; the branch "NAS zu Hause" stays). The first message — only while the last status came through the bridge AND the outside watcher is configured — goes out when the threshold is reached and one of these holds: the outside watcher called within the last 390 s (text: it is the box); it has been silent for more than 540 s (text names the NAS); it was never seen (neutral text, at once); or 180 s have passed since the threshold (neutral text) — the wait never exceeds 3 min. In every other situation it goes out at the threshold.
- **Changed timing:** a failed message is retried through `Notify`'s 15-min rule instead of "the next run".

- [ ] **Step 1: Go tests** (`clock.Fake`, `FaultFS`, Bark against `httptest`). `TestSectionOrderAndTokens` (the line of a quiet run and of a run with every condition met, compared with lines captured from PHP for the same prepared states); `TestMinuteTickOnlyBoxOffline` (between full runs a due reminder is NOT sent, "Box OFFLINE" is); `TestDefektAfterTwoFullRuns`, `TestDefektText` (the fixed text with the step; ASCII), `TestDefektResavesFirst`; `TestFirstOfflineWaitsForWatcher` (table: watcher called 300 s ago → sent, the box; 450 s ago → not yet; 450 s ago and 181 s past the threshold → sent, neutral; 600 s ago → sent, the NAS; never seen → sent at once, neutral; not through the bridge → sent at once; watcher not configured → sent at once); `TestOfflineTextNeverBlamesServer`; `TestRetryNotBefore15Min` (for "Box OFFLINE" and for a reminder); `TestOfflineRules` (the numbered rules (3), (4), (5) of `CLAUDE.md`: recovered only after a NEW push; a message that was switched off is caught up after switching on; the reminder uses the first text); `TestNoticeTexts` (golden files captured from PHP `box_offline_notice_text` for the cases that stay, and `weekly_report_text`); `TestWeeklyReportCounts` (demo sends, resends and `relay_alarm` with source `remote` not counted); `TestBoxHealthThresholds` (as `cron/check_offline.php`; `heartbeat_ok=0` counts only when `last_heartbeat` is not `nie`).
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** `bash tools/test-go.sh tests/backend/watchdog.test.mjs`. A case that is red because of a rule this task changes went to `php-storage` in Task 1 already; one that was missed goes through the divergence protocol now. `go-watch.test.mjs` (Go only): "Dashboard DEFEKT ab dem zweiten vollen Lauf, danach einmal wieder OK" (`storage.fail('selftest')`); "ein unbrauchbarer Status-Key ist ein Fehlschlag (SEND_FAILED), kein 'abgeschaltet'" (the twin of the case of that name in `php-storage.test.mjs`); "nach gescheitertem Versand frühestens 15 min später" (the twin of "nimmt Bark die Meldung nicht an …": Bark answers 500 → `check` → the `SEND_FAILED` token, nothing counts as reported, no `box_offline` entry; `check` again → no second request; `backend.state.ageRetry('box_offline', 901)` → `check` → sent); "nach einer Auszeit des Servers keine Geister-Meldung" (stop → set `lifebeat_at` 300 s and the box status 1 200 s back through `backend.files` → start → `check` → no "OFFLINE"). Both files on the list.
- [ ] **Step 4:** `bash tools/check-go.sh`. Commit — `Go: Prüf-Lauf – Selbsttest, Box offline, Außen-Wächter, Erinnerungen, Wochenbericht`.

### Task 23: The checker III — the server's own faults

**Files:** Create `internal/watch/ownfaults.go` + test; modify `internal/app/serve.go` (`Watcher.OnStart`, `Provider.OnChange` → `Watcher.OnConfig`), `tests/backend/go-watch.test.mjs`.

**Interfaces — produces:**

```go
func (w *Watcher) OnStart(info store.StartInfo)           // Rücksprung, verworfene Laufzeitdaten, Notbetrieb: Meldung vormerken bzw. senden
func (w *Watcher) OnConfig(ok bool)                       // fehlerhaft → Meldung sofort
func (w *Watcher) Queue(id, title, body string)           // einmalige Meldung vormerken (watch.pending) und gleich versuchen
```

- All five occasions use the kind `nas_web` and the fixed texts. **One-time messages** (rewind, discarded `runtime.json`, discarded `sessions.json`, and — from Task 24 — interrupted send) are written to `watch.pending` first and removed only when Bark accepted them; a section of the minute tick retries what is pending under the 15-min rule. A switched-off kind (`off`) removes them — as everywhere, a switched-off message counts as done. **Repeating messages** (emergency mode, broken configuration): at once, then every 24 h while the state lasts (`emergency_notified_at`, `config_broken_notified_at`); a failed send is retried under the 15-min rule.
- The emergency text says how many recipients an alarm would reach now and from where (`dashapi.RecipientsNow`), and mentions the demo recipient when the rescue copy holds one.

- [ ] **Step 1: Go tests.** `TestOwnFaultMessages` (each of the five occasions: title, body, kind `nas_web`); `TestPendingMessagesSurviveFailedSend` (Review Focus 3: Bark down at start → the rewind message stays in `watch.pending`, survives a restart, goes out at the first attempt after 15 min, exactly once); `TestPendingDroppedWhenKindOff`; `TestEmergencyReminder24h`; `TestConfigBrokenReminder24h` (and nothing once the file is good again).
- [ ] **Step 2:** Implement. `go-watch.test.mjs`: "verworfene Laufzeitdaten: eine Meldung" (`backend.files.corrupt('runtime.json')` while stopped → start → the message arrives once; a second restart sends nothing).
- [ ] **Step 3:** `bash tools/test-go.sh`; `bash tools/check-go.sh`. Commit — `Go: Meldungen über Störungen des Servers selbst – vorgemerkt, bis Bark sie annimmt`.

### Task 24: When things go wrong — shutdown, crashes, systemd

**Files:** Create `internal/app/{shutdown.go,accesslog.go,recover.go,sdnotify.go,probe.go,failure.go,interrupted.go}` + tests; modify `internal/app/serve.go`, `cmd/fw-alarm/main.go`.

**Interfaces — produces:** command `fw-alarm report-failure [--config <file>]`.

- **Shutdown** (SIGTERM, interrupt, `fw-alarm stop`): stop accepting → open requests, a running "Alarm an alle" and everything in `Core.Bg` finish, 40 s at most → last sign of life → `Store.Close`. An orderly shutdown loses nothing.
- **Panics:** a handler that panics answers 500 and logs the panic value and stack, never fields of the request.
- **Access log** to stdout: `<RFC 3339> "<METHOD> <path without query>" <status> <ms>ms bridge=<0|1> "<user agent>"`, through a queue of 1 000 lines that drops lines rather than holding a request; the self-probe is left out.
- **systemd** (no library; a no-op when `NOTIFY_SOCKET` is unset): `READY=1` once the listener is open; every 30 s a `GET /index.php` on the own `listen` address with a 5 s limit, and `WATCHDOG=1` only when it answered.
- **`report-failure`:** when `SERVICE_RESULT` is not `success` → unless the marker `crash-reported` next to the control socket is younger than 30 min → ONE Bark request (5 s limit) to `bark_key_status` with `level=active` and the fixed crash text; touch the marker. Reads `config.toml`, else the last-good copy; never needs the server.
- **Interrupted send:** at start, a `*_started` entry of the last 15 min that has a `uid` and no result entry naming it as `started_uid` → entry `send_interrupted` (`kind`, `started_at`, `demo`) and `Watcher.Queue` with the text "Dashboard: Versand unterbrochen". Never a resend.

- [ ] **Step 1: Go tests.** `TestShutdownWaitsForBroadcast` (alarm to a recipient delayed by 2 s, then stop → the process ends after the recipient was served and the result is logged); `TestShutdownWaitsForBackground`; `TestShutdownLimit` (a background job that never ends → exit after 40 s of fake time); `TestHandlerPanicIs500` (the log line holds no form value); `TestAccessLogHasNoQuery` (`GET /api/mute.php?t=SECRET&state=on` → the line shows the path, not `SECRET`; `bridge=1` with `X-FW-Bridge`); `TestAccessLogDropsInsteadOfBlocking`; `TestSdNotify` (against a unixgram socket: `READY=1`, then `WATCHDOG=1` only while the probe answers; build tag: not on Windows); `TestReportFailure` (success → nothing; failure → one request with the fixed text; a second one within 30 min → nothing; unreadable config and a last-good copy → still sent); `TestInterruptedSend` (found; not found when a result names it; never for entries without `uid`).
- [ ] **Step 2:** Implement. `bash tools/test-go.sh`; `bash tools/check-go.sh`. Commit — `Go: geordnetes Beenden, Absturz-Meldung und Watchdog`.

### Task 25: Proofs from outside — many requests, restarts, hard kills

**Files:** Create `tests/backend/go-concurrency.test.mjs`, `go-restart.test.mjs`, `go-config.test.mjs`; modify `tests/backend/README.md`, `tools/test-go.sh` (without arguments: every `tests/backend/*.test.mjs`); delete `tools/go-green.txt`.

All three files Go only. A red case here is a defect of the server: report it, fix it in the owning package with a Go test of its own.

- [ ] **Step 1: `go-concurrency.test.mjs`:** 20 polls at once with one waiting command → exactly one answer carries it, all are 200; 30 wrong logins at once with `login_max_failures: 8` → at most 8 answers "Login fehlgeschlagen.", the rest 429 or 503; 10 `keys_add` at once → 10 entries, 10 distinct ids, version + 10, distinct `mute_token`s; 10 shortcut-link calls `state=on` for one recipient → one `key_muted` entry, version + 1; a status push every 20 ms during an "Alarm an alle" to a `slow…` recipient → every push answers 200 within 1 s; 200 `status` requests at once → all 200. **Answers never wait for Bark:** with a muted `slow…` recipient whose mute is overdue, a poll, an `api/keys.php` call and a shortcut-link call each answer within 1 s and the info "wieder laut" arrives afterwards; during an "Alarm an alle" to a `slow…` recipient a `keys_add` answers within 1 s.
- [ ] **Step 2: `go-restart.test.mjs`:** a restart loses nothing (session still valid, box status, markers, a delivered command, the history); stop during "Alarm an alle" to a `slow…` recipient → he is served and the result is in the history; `hardKill()` after a poll delivered a command → after the start no poll carries it again; `hardKill()` after the push of a relay alarm → exactly one `relay_alarm`; `hardKill()` during "Alarm an alle" → the started entry is there, after the start `send_interrupted` plus the message, nothing sent again; a second `serve` on the same folder exits with a failure and the sentence about the other program; "kaputte Zustandsdatei: Rückfall mit Rücksprung, dann Notbetrieb" (`state.json` corrupt → start: recipients of the previous version, `keys_version` + 1000, a new recipient gets an id ≥ old `next_id` + 1000, the message "Stand zurueckgesetzt", and a change made now is still there after another restart; then both files corrupt → `keys_list` answers `keys_corrupt`, poll and `api/keys.php` report version 0, the shortcut link answers 500, login works, `keys_add` is refused with the emergency sentence, the message "Dashboard NOTBETRIEB", `fw-alarm health` exits 1 with `state=emergency`).
- [ ] **Step 3: `go-config.test.mjs`:** `writeRawConfig` with a typo → the old password still logs in, every earlier session answers 401, the message "Konfiguration fehlerhaft", `health` says `config_ok=0`; a restart with the broken file starts from the copy; the repaired file → `config_ok=1`; a file with a warning only (`check_interval_seconds = 0`) ends no session.
- [ ] **Step 4:** `bash tools/test-go.sh` → everything passes; five times in a row without a flake (`for i in 1 2 3 4 5; do bash tools/test-go.sh || break; done`). `bash tools/check-go.sh`. Commit — `Tests: Proben mit vielen Anfragen, Neustart, hartem Abbruch und kaputter Konfiguration`.

### Task 26: The remaining commands and the texts of the frontend

**Files:** Create `internal/app/{editconfig.go,checkstate.go,statebump.go}` + tests; modify `cmd/fw-alarm/main.go`, `internal/app/serve.go` (hints of `health`), `nas_dashboard/assets/app/logic.js`, `nas_dashboard/assets/app/view-settings.js`, `tests/dashboard/logic-log.test.mjs`, `logic-status.test.mjs`, `wording.test.mjs`, and the scenario assertions on the changed sentences.

**Interfaces — produces:**
- `fw-alarm edit-config [--config <file>]`: copy next to the file (0600) → `$VISUAL`, else `$EDITOR`, else `nano` → check; broken: show the error and ask `(b)earbeiten oder (v)erwerfen?`; warnings are shown and accepted → replace in one rename, owner and mode kept → `reload` through the control socket when the server runs → print the result.
- `fw-alarm check-state [--config <file>] [--list]`: exit 0 and `Zustand lesbar: <n> Empfänger, Listen-Version <v>.` only for a readable `state.json` (the previous version does not count); `--list`: one line per recipient — id, name, masked key, `stumm`/`dauerhaft` — never a key or token.
- `fw-alarm state-bump [--config <file>]`: takes `server.lock` (a running server → the sentence about the other program; a lock that cannot be created → refuse as well, exit 1), applies `Rewind("restore")`, saves, prints the new numbers.
- Hints of `health`: this start came from the previous version (rewind); the emergency list of the configuration holds fewer keys than the recipient list; `check_interval_seconds` is 0; the lock could not be created; the rescue copy could not be refreshed; the history could not be read at start; a changed `state_dir`/`listen`/`control_socket` waits for a restart.
- `health` also answers the question "whom would an alarm reach right now?" — from `dashapi.RecipientsNow`, the very function a send uses: `alarm_mode=live|demo`, `alarm_targets=<n>`, then one line `alarm_target=<name> <masked key>` per recipient, with ` (Probe-Sperre: wuerde NICHT gesendet)` behind it when the send lock would hold it back; and `send_guard=aus` or `send_guard=aktiv bis <HH:MM> (<n> Keys)` — read with `Config.SendGuard(now)` at the moment of the call, never from the warnings collected when the file was loaded (they would still name a lock that has ended). Never a full key. A lock in force does not change the exit code.
- Frontend: the texts of "Texts this plan fixes" (two new `describeLogEntry` texts, "Wächter" instead of "Wächter-Timer", "in der Konfiguration des Servers" instead of `config.php`).

- [ ] **Step 1: Tests.** `TestEditConfig` (with a fake editor script: valid change → replaced, mode and owner kept; broken change + "v" → file untouched; never a temp file left); `TestCheckState` (readable; only the previous version readable → exit 1; `--list` output holds no key and no token); `TestStateBump` (+1000, open command cancelled; refuses while a server holds the lock; refuses when the lock cannot be created); `TestHealthHints`; `TestHealthAlarmTargets` (LIVE with three recipients; demo mode → exactly one line; with a lock: the lines of the others carry the marker; no line holds a full key); in `tests/dashboard/logic-log.test.mjs` the two new events; the assertions on the changed sentences in `logic-status.test.mjs`, `wording.test.mjs` and the scenarios follow the new wording.
- [ ] **Step 2:** Implement. `node --test "tests/dashboard/*.test.mjs"` → pass; `node --test "tests/backend/*.test.mjs"` (PHP) → pass; `bash tools/test-go.sh` → pass; the scenarios `overview` and `settings` on PHP → `OK`; `bash tools/check-go.sh`.
- [ ] **Step 3:** Commit — `Go: Konfiguration sicher ändern, Zustand prüfen und anheben; Texte der Oberfläche, die für beide Fassungen stimmen`.

### Task 27: One-time import of the PHP data

**Files:** Create `internal/phpimport/{read.go,recipients.go,runtime.go,config.go,run.go}` + tests, `tests/backend/fixtures/php-data/` (an artificial PHP data folder plus `config.json`), `internal/phpimport/testdata/expected/`; modify `cmd/fw-alarm/main.go`.

**Interfaces — produces:** `fw-alarm import-php --data <old folder> --config-json <file> --out-state <new folder> --out-config <config.toml> [--force] [--drop-recipient <id>]… [--drop-config-key <name>]… [--preflight]`.

- Everything of spec §9. In short: lenient reading only here; recipients with id, key, name, phone, mute state and time, volume, "dauerhaft", **mute token** — an entry that cannot be taken over one to one (invalid or duplicate key, invalid token, an 11th entry, a demo recipient that is not in the list) stops the import before anything is written, naming the entry without key and token; only `--drop-recipient <id>` lets one go. An entry without a token gets a new one (no link existed that could break) and is named in the summary.
- **Numbers:** `keys_version` = max(version of the list, `high_water` of `alarm_keys_version.json`, `keys_version` in the last status of the box) + 1 — the box then fetches the list once from the new server, whatever it holds. `next_id` = max(the list's, the one in `alarm_keys_version.json`, highest id + 1). Commands: next id, current command, recent types.
- Taken over as well: demo mode, settings, notify settings (PHP stores numbers and booleans, the new state stores each value as the choice key in text form — converted, then checked with `notify.CheckSetting`; an unknown value falls to the default and is named), `recent_types` (PHP: an object id → type; new: a list ordered by id), last status, seen alarms and boards, markers of the checker, the outside watcher's sign of life, both history files line by line (each line gets a `uid`). Not taken over: sessions, brakes, `log_cache`, lock files, `.bak`.
- **The sign of life is left unset** (spec §9 as amended): the first start then counts as "after an absence", so the silence of box and outside watcher counts from that start — the box is cut off by the bridge hold during the switch, and that must not become "Box OFFLINE".
- Configuration: every key of the JSON file must be known (`data_dir` and `health_check_url` are dropped silently, any other unknown key stops the import unless `--drop-config-key` names it); `bark_keys_alarm` becomes the keys of the imported recipient list; `state_dir`, `listen`, `control_socket` get their defaults; the result is written with `config.Write` and must load again to the same values.
- It writes through the save functions of `internal/store` (same sequence, same read-back) but does not call `store.Open` on the target before it has checked for an existing state — a fresh `Open` saves an empty state at once. Holds `server.lock` of the new folder while it runs (a running server there, or a lock that cannot be created → stop). Never writes into the old folder. Refuses an existing new state unless `--force` (then `Rewind("import")`). List and `.bak` both unreadable → stop. `demo_mode.json` broken or missing despite a `.bak` → LIVE, named in the summary. Writes the rescue copy. Ends by reading its own result with `store.CheckState` and prints a summary: counts, what was adjusted and why — no key, no token.
- `--preflight` only reads and prints what forbids a switch (spec §12.5): demo mode on; a command pending or delivered and not acked; `alarm_pending=1` in the last status; a recipient whose mute is overdue. It also prints the offline threshold in force (the latest moment the bridge hold should be lifted). Exit 1 if anything forbids.
- Two switches for safe first starts. `--send-guard-minutes <n>` (1–180): the written `config.toml` carries the send lock — `send_guard_keys` = the one key in `bark_key_status`, `send_guard_until` = now + n minutes; without a valid `bark_key_status` the import stops. `--scrub-keys` (for the trial): in everything written — `state.json`, `recipients.rescue.json`, `runtime.json`, `history.jsonl`, `config.toml` — every Bark key of the old data (recipients, demo recipient, `bark_keys_alarm`, `bark_key_status`) is replaced by an invented one (`probekey00001` …, the same old key always by the same new one); at the end the import searches every written file for every old key and stops with an error if one is found. The summary says `Bark-Keys ersetzt: <n>, verblieben: 0`.

- [ ] **Step 1: The artificial folder** from what PHP wrote in practice: `alarm_keys.json` with entries of every era (no `mute_token`, no `muted_at`, `muted: 1`, `"mute_volume": "3"`, `mute_permanent` missing), `alarm_keys_version.json` above the list's numbers, `latest_status.json` with a `keys_version` above both, `notify_settings.json` with `"global": []`, `login_throttle.json` as `[]`, `devices_seen.json` with numeric-looking ids, `command_state.json` with a delivered command and `recent_types`, `watchdog_state.json` with every key the checker ever wrote, `commands.log` and `commands.log.1` with every event plus shapes from before 11/2026 (no `demo`, a text starting `DEMO-MODUS`) and one broken line. Variant folders for the refusals.
- [ ] **Step 2: Tests.** `TestImportGolden` (folder → the expected `state.json`/`runtime.json`, compared after removing `uid`s); `TestImportRefusesLoss` (one case per reason; nothing written); `TestDropRecipient`; `TestImportNeverWritesOldFolder` (checksums before and after); `TestImportRefusesExistingState`, `TestForceRewinds`; `TestImportHoldsLock`; `TestImportBrokenDemoBecomesLive`; `TestVersionAboveEverything` (rows: list highest; high-water mark highest; the box's status highest); `TestLifebeatUnset`; `TestEmergencyListRefreshed`; `TestHistoryLineByLine` (count equal, order kept, the broken line skipped and counted in the summary); `TestSummaryHasNoSecrets`; `TestImportedConfigLoads` (odd strings in `bark_alarm_body` survive); `TestUnknownConfigKeyStops`; `TestPreflight`; `TestSendGuardMinutes` (config carries the lock on the status key; no status key → stop; 181 → usage error); `TestScrubKeys` (no old key in any written file — searched as text; recipients keep ids, names, mute states and tokens; demo recipient still points at the same person; a key that also stands in `bark_keys_alarm` gets the same replacement); `FuzzLenientList`.
- [ ] **Step 3:** Implement. `bash tools/check-go.sh`. Commit — `Go: einmalige Übernahme der PHP-Daten – verliert nichts stillschweigend`.

### Task 28: Parity — PHP and Go side by side on the same data

**Files:** Create `tests/backend/parity.mjs`; modify `tests/backend/harness.mjs` (both drivers accept `options.seed`), `tests/backend/README.md`.

**Interfaces — produces:**
- `startBackend({ kind, seed })`: `kind` overrides `FW_BACKEND`; `seed` = `{ data: <PHP data folder>, configJson: <file> }`. PHP driver: copies the folder into its data folder and merges the JSON into its configuration. Go driver: runs `import-php` on the copy into its state folder and uses the written `config.toml`. Both replace, alike, and print the names of the replaced keys: `bark_host` (their own mock), the token hashes, the dashboard accounts — and each keeps its own storage location (`data_dir`, `health_check_url`; `state_dir`, `listen`, `control_socket`).
- `node tests/backend/parity.mjs [--data <folder> --config-json <file>]` (default: the fixture of Task 27): starts both, sends the same request list to both, compares per request the answer (status, body) and what each server sent to its Bark mock (`key`, `fields`; the time stamp in `body` masked), prints a diff per difference, exit 1 on any. It refuses to start unless both effective `bark_host` values are `http://127.0.0.1:<own mock port>`. Normalisation: drop `server_time`, `csrf`, `seen_age_seconds`, cookie values; sort object keys; mask `?v=`. `ALLOWED` in the script lists deliberate differences, each with its spec § (the version after the import is higher; texts naming the configuration file; what §7 changes).
- **Foreign data is scrubbed first, always:** with `--data` the script copies the folder and the JSON file, replaces every Bark key in the copies by an invented one (recipients in `alarm_keys.json` and its `.bak`, the demo recipient in `demo_mode.json` and its `.bak`, `bark_keys_alarm`, `bark_key_status`; then the same replacement as plain text across every file of the copy), searches the copies for every old key and refuses to start if one is left — only then are the two servers started, on the copies. There is no switch to skip this. It prints `Bark-Keys ersetzt: <n>, verblieben: 0`.

- [ ] **Step 1:** Request list: login as admin and reader; `status`, `keys_list`, `log_view` with all five filters and limits 1/25/100, `notify_list`, `settings_list`; box `poll`, `keys`, `status` (old and new firmware), `ack`; `alive`; shortcut link (text, browser, preview, unknown token); every address with GET, POST, HEAD, PUT and DELETE (status and body); a form with odd field names (`a[]`, `a[b]`, a name with a dot, a name with a space) on the status push; a checker run in four prepared states; `alarm` and `false_alarm` in LIVE and in demo mode with one failing recipient; `keys_test`; a resend with `only_ids`.
- [ ] **Step 2:** `node tests/backend/parity.mjs` → no difference outside `ALLOWED`. Every difference is decided: Go wrong → fix Go (with a test in the owning package); a PHP quirk the spec gives up → `ALLOWED` with its §.
- [ ] **Step 3 (controller): the real data, on the PC only.** Take the newest archive in `~/fw-alarm-backups` (no contact with the VM), extract it into a fresh folder with mode 0700 in the job's temp folder, write `config.json` from its `config.php` with `php -r`, run `node tests/backend/parity.mjs --data … --config-json …`. Expected: the line `Bark-Keys ersetzt: <n>, verblieben: 0`, no difference outside `ALLOWED`, and the import's summary names no refusal. After the run, every request both Bark mocks received carries an invented key (the script checks and says so) — and nothing could have left the PC: both `bark_host` values were the mocks. Then delete the folder and confirm it is gone. Nothing from it is ever committed; what the import had to adjust goes into the ledger without names, keys or tokens.
- [ ] **Step 4:** Commit — `Go: Gleichlauf mit der PHP-Fassung auf übernommenen Daten ohne Abweichung`.

### Task 29: Browser scenarios, dashboard tests and wording on Go

**Files:** Modify `tests/dashboard/wording.test.mjs`, `tests/dashboard/e2e/sandbox.mjs`, `tests/dashboard/e2e/README.md`; scenarios only through the divergence protocol.

- [ ] **Step 1:** `FW_BACKEND=go FW_GO_BIN=<abs> node tests/dashboard/e2e/run.mjs <name>` for `smoke shell overview recipients log settings switch` → `OK` seven times. A failure is a difference between PHP and Go: fix Go, not the scenario. One edit of the bench is allowed: the helpers in `sandbox.mjs` that restart the Go server under an open page (`setStatusAge` and the like) may wait until the page has fetched a fresh status again before they return — the page shows "Keine Verbindung" during a restart, and that is right.
- [ ] **Step 2:** `wording.test.mjs`: the place-neutral rules ("Server" in visible texts, "NAS" only for the outside watcher, "DSM" only there) also read the Go sources (`internal/**/*.go` and the templates). The two tests that execute PHP functions stay until Task 37. `node --test "tests/dashboard/*.test.mjs"` → pass.
- [ ] **Step 3 (controller):** compare the screenshots the scenarios write for PHP and for Go (overview, recipients, log, settings, login) — same pages. Anything that differs is fixed in Go or goes to the user.
- [ ] **Step 4:** Commit — `Go: Oberfläche läuft unverändert – alle Chrome-Szenarien grün`.

### Task 30: Server files I — unit, setup, deploy, backup, check

**Files:**
- Create: `server/go/systemd/fw-alarm.service`, `server/go/systemd/fw-alarm-ballast.service`, `server/go/setup.sh`, `server/go/deploy.sh`, `server/go/backup.sh`, `server/go/check.sh`, `server/go/cloudflared/config.yml.example`, `tests/server/go-deploy.test.mjs`, `go-backup.test.mjs`
- Modify: `server/deploy.sh`, `server/backup.sh` (both refuse with a pointer to `server/go/` once `fw-alarm.service` is active on the VM — after the switch they would deploy to a dead webroot and "back up" frozen data) and their tests

Every script finds its own folder through `$(dirname "$0")` (the folder moves in Task 37), takes the VM's address from `FW_DEPLOY_HOST` or `server/deploy.local` as today, and uses a folder in the home directory on the VM, never `/tmp`.

`fw-alarm.service` (the trial in Task 34 confirms it with `systemd-analyze security`; a directive the trial proves wrong is corrected there):

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
Environment=GOMEMLIMIT=200MiB
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
ConfigurationDirectoryMode=0750
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

- `setup.sh` (on the VM as root, refuses to start from `/tmp`): installs both units and `/etc/fw-alarm` (root:fwalarm 0750), enables and starts nothing, touches neither Nginx nor PHP. With `--trial`: additionally `fw-alarm-trial.service` — the same unit with `fw-alarm-trial` as state, runtime and configuration folder, `--config /etc/fw-alarm-trial/config.toml` in `ExecStart` and `ExecStopPost`, and three lines that make a real alarm impossible whatever the file says: `IPAddressDeny=any`, `IPAddressAllow=localhost`, `ExecStartPre=/bin/grep -q '^bark_host = "http://127.0.0.1:9"$' /etc/fw-alarm-trial/config.toml`.
- `deploy.sh [--install-only] <rev>` (spec §13): rev must be on `origin/main` → build from `git -c core.autocrlf=false -c core.eol=lf archive <rev>` in a temp folder with `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -mod=vendor -trimpath -buildvcs=false -ldflags "-s -w -X main.version=<sha>"` → `sha256sum` → `backup.sh` first (a failure: continue, exit 3 at the end) → upload into a fresh folder in the home directory → compare the checksum there → install as root-owned `/usr/local/bin/fw-alarm.new` (0755) → the NEW program runs `check-config` and `check-state` against the real files as user `fwalarm` (one fails → remove `.new`, exit 1) → keep the old program as `fw-alarm.prev` → `mv -f` in one step → `systemctl restart fw-alarm` → `fw-alarm health` and `fw-alarm version`; when `health` does not report a healthy server within 20 s: put `fw-alarm.prev` back, restart, exit 1. Last line: `deployed <sha>: fw-alarm sha256 <64 hex> ok`. **`--install-only`** (before the switch, when no service, state or configuration exists yet): build, upload, checksum, install the binary — no backup, no `check-*`, no `systemctl`, no `health`; last line `installed <sha>: fw-alarm sha256 <64 hex>`.
- `backup.sh`: on the VM `fw-alarm flush` and `fw-alarm check-state`, then fetch `/var/lib/fw-alarm` and `/etc/fw-alarm/config.toml` as one archive to `~/fw-alarm-backups` (the newest 30 kept; refuses a target inside the repo); an archive counts only after the check on the PC (readable, holds `state.json` and `config.toml`; `state.json` strict-readable by a local `fw-alarm check-state` when `bin/fw-alarm` exists); a failed one deletes no older one.
- `check.sh local|public`: inside — `fw-alarm health`; owner and mode of `/var/lib/fw-alarm` (fwalarm, 0700) and `/etc/fw-alarm/config.toml` (root:fwalarm 0640); `fw-alarm version` = deployed revision; unit active; **`nginx` and `php8.3-fpm` neither active nor enabled; the process listening on `127.0.0.1:8080` is `fw-alarm`** (`ss -ltnp`). A send lock in force is printed as its own line `INFO send-guard aktiv bis <HH:MM>` (not a failure on the switch evening; in the quiet days it must not appear). Outside — like the box (TLS 1.2, no `Accept`), without `Accept`, `/classic.php` 302, `/api/health.php` 404, static file types.
- `cloudflared/config.yml.example`: the variant with `service: unix:/run/fw-alarm/http.sock` for Task 37.

- [ ] **Step 1: Tests with doubles** for `ssh`, `go`, `git`, `sha256sum` (pattern of `tests/server/backup.test.mjs`). `go-deploy`: refuses a rev that is not on `origin/main`; the archive is made with both `-c` switches; a checksum mismatch leaves the old program; a failing `check-state` of the new program leaves the old one; an unhealthy server after the restart brings `fw-alarm.prev` back; exit 3 when the backup fails; never `/tmp`; the last line; `--install-only` calls neither `backup.sh` nor `systemctl` nor `health`. `go-backup`: `flush` and `check-state` run before the fetch; an archive without `state.json` is no backup and deletes none; target inside the repo refused. Old scripts: refuse when `fw-alarm.service` is active.
- [ ] **Step 2:** Implement; `bash -n` on every script; `node --test "tests/server/*.test.mjs"` → pass.
- [ ] **Step 3:** Commit — `Server: Dienst, Einspielen, Sichern und Prüfen für die Go-Fassung`.

### Task 31: Server files II — cutover, rollback, runbook

**Files:**
- Create: `server/go/cutover.sh`, `server/go/rollback.sh`, `tests/server/go-cutover.test.mjs`
- Modify: `server/README.md` (new part "Go-Fassung")

- `cutover.sh preflight|stop-timer|switch [--force]|stage <dir>` (on the VM as root):
  - `preflight` — the last line of the PHP checker starts with `WEB_OK` and has no `STATE_WRITE_FAILED`; `fw-alarm import-php --preflight` passes; prints the offline threshold; `bark_key_status` is set in `config.php` (the send lock needs it).
  - `stop-timer` — stop and disable `fw-alarm-cron.timer`, wait for a running `fw-alarm-cron.service`.
  - `switch` — `systemctl disable --now nginx php8.3-fpm` and `systemctl mask` both (a reboot must not bring PHP back, Review Focus 1) → the preflight once more, now on data that no longer changes → write `config.php` as JSON into a 0700 folder in root's home → `import-php` into `/var/lib/fw-alarm` and `/etc/fw-alarm/config.toml` (owners and modes as in Task 30; always with `--send-guard-minutes 90`, so the new server can send to nobody but the admin for its first 90 minutes; `--force` passes through for a second switch) → `check-state` → delete the JSON file → enable `fw-alarm-ballast` if the file `/etc/fw-alarm/ballast` exists (Task 34 decides) → `systemctl enable --now fw-alarm` → `fw-alarm health`. If any step before the start of `fw-alarm` fails: unmask, enable and start Nginx and PHP-FPM again, say so, exit 1.
  - `stage <dir>` — the same export and import into `<dir>` without touching any service, always with `--scrub-keys` (no real Bark key ever exists in the trial), then writes the trial values itself (`bark_host = "http://127.0.0.1:9"`, `listen = "127.0.0.1:8082"`, `control_socket = "/run/fw-alarm-trial/control.sock"`, `state_dir = "<dir>"`) and ends with a check that fails unless `bark_host` is exactly that closed port.
- `rollback.sh` (spec §12.5, the evening of the switch only): `fw-alarm check-state --list` into a file in the home directory → `systemctl disable --now fw-alarm` (and the ballast) → nothing listens on 8080 (`ss -ltn`) → unmask, enable and start Nginx and PHP-FPM → wait up to 5 min for a status push of the box and a call of the outside watcher in the Nginx log → only then enable and start the timer; if the wait ends without both: the timer stays off, and the script says which of the two is missing and how to start the timer by hand later → print what is left to do by hand (compare recipients and mute states with the saved list).

- [ ] **Step 1: Tests with doubles** (`systemctl`, `php`, `fw-alarm`, `ss`, `journalctl`): `switch` disables AND masks Nginx and PHP-FPM before anything else and runs the steps in order; a failing import unmasks and restarts them and never starts `fw-alarm`; the JSON export is deleted in every outcome; `switch` calls the import with `--send-guard-minutes 90` and never with `--scrub-keys`; `stage` calls it with `--scrub-keys`, calls no `systemctl`, and its output configuration has the closed port; `rollback` unmasks, and starts the timer only after both waits — and not at all when one is missing.
- [ ] **Step 2:** Implement; `bash -n`; `node --test "tests/server/*.test.mjs"` → pass.
- [ ] **Step 3:** `server/README.md`, part "Go-Fassung", in plain words, required and optional told apart, exact commands: services and paths; changing the configuration (`sudo fw-alarm edit-config`); new password (`fw-alarm hash-password`); deploy, backup, **restoring a backup** (the steps of spec §13 with `state-bump` — also the way back across a change of the file format: an older program plus `state.schema-<n>.json` as `state.json`, then `state-bump`); the switch and the way back (the commands of Task 35); a table "Meldung → wo nachsehen" with the new messages; what `fw-alarm health` says.
- [ ] **Step 4:** Commit — `Server: Umschalten und Rückweg als Skripte, Runbook für die Go-Fassung`.

### Task 32: Review of the whole and report

- [ ] **Step 1:** Everything once more from a clean state: `bash tools/check-go.sh`; `node --test "tests/backend/*.test.mjs"` (PHP); `bash tools/test-go.sh`; the seven scenarios on both; `node --test "tests/dashboard/*.test.mjs" "tests/server/*.test.mjs" "tests/bridge/*.test.mjs" "tests/watcher/*.test.mjs"`; `node tests/backend/parity.mjs`; the newest run on GitHub is green.
- [ ] **Step 2:** Walk "Rule → test": every row names a test that exists and passes; every case in `php-*.test.mjs` has a row. Walk `CLAUDE.md`: every rule spec §7 does not list is held by a shared test that is green on Go — list any rule without one and write its test.
- [ ] **Step 3:** Whole-branch review by a fresh reviewer on the most capable model, from commit `a5e5833` (the groundwork of 2026-11-17 included). Work its findings in.
- [ ] **Step 4:** `CLAUDE.md` ("Go-Fassung (im Bau)" → what exists, how to test it, what is still PHP), `tests/backend/README.md`, the spec (anything the build decided differently, with the reason), the memory notes. Report to the user: what is built, what the gates below need from them.

---

## Phase C — Gates, trial, switch, cleanup (each needs the user)

### Task 33: Security review — the user's own session

- [ ] **Step 1:** Assign the user the task to start the review session (it waits for their "go"). Scope to hand over: sessions and cookie, login brake and password slots, the check of unknown users, proxy trust, machine and watcher token (constant-time comparison — no test can show it), the shortcut link (previews, brake, no token in redirect or log), static files (no way out of the embedded tree), size and time limits, what access log and journal can hold, the control socket, `report-failure`, the unit's confinement, `deploy.sh`/`cutover.sh` (no `/tmp`, checksum before install), the import (reads hostile-looking data), the CI workflow (pinned, read-only).
- [ ] **Step 2:** Fix every finding with a test in the style of the owning task; rerun Task 32 Step 1; one commit per finding.

### Task 34: Trial on the VM and the memory calculation — needs the user's go (it touches the live machine, not the live service)

- [ ] **Step 1:** The run on GitHub for `HEAD` is green.
- [ ] **Step 2:** Copy `server/go/` to `~/fw-server-go` on the VM; `sudo bash ~/fw-server-go/setup.sh --trial`; `bash server/go/deploy.sh --install-only HEAD` → the last line `installed <sha>: …`.
- [ ] **Step 3:** `sudo bash ~/fw-server-go/cutover.sh stage /var/lib/fw-alarm-trial` — but first, before any real data is touched, prove the network rule of the trial unit on this VM: `sudo systemd-run --wait -p IPAddressDeny=any -p IPAddressAllow=localhost curl -m 5 -sS https://api.day.app/` must FAIL to connect (if it connects, stop: the trial does not start until the rule holds). Then `stage` (import of the real data; the PHP side is only read) → it prints `Bark-Keys ersetzt: <n>, verblieben: 0` and its last line confirms the closed Bark port; install the written configuration as `/etc/fw-alarm-trial/config.toml`; `fw-alarm check-config`, `check-state --list` → the recipients as in the live dashboard.
- [ ] **Step 4:** What the PC cannot show: `systemctl start|stop|restart fw-alarm-trial` and `fw-alarm health`; `systemd-analyze security fw-alarm-trial`; a start with a wrong `bark_host` in the trial file is refused by the unit; the watchdog (`kill -STOP` the process → systemd ends and restarts it within about 3 min); the crash message (`kill -SEGV` → `report-failure` ran once, the marker exists, a second crash within 30 min sends nothing — seen in the journal; the request itself cannot leave); the Unix socket (`listen = "unix:/run/fw-alarm-trial/http.sock"`, `curl --unix-socket`, `cloudflared tunnel ingress validate` on the example file, and which user `cloudflared` runs as — if not root, the socket needs a group of its own, never `fwalarm`); the way back as a dry run (stop → nothing listens on 8082); which of `nginx.service` and `fw-alarm` would win port 8080 at boot is moot once both old services are masked — confirm `systemctl mask` works on a copy of the unit name; memory: an Argon2id hash for the reader in the trial configuration, 30 wrong logins at once → `MemoryPeak` below 256 M, no kill; a look at the dashboard through an SSH tunnel with the real account — trigger nothing that sends.
- [ ] **Step 5: Oracle's 20 % rule** (spec §11.11): share of used memory `(total − available) / total` from `free -m`, compared once with the console; calculation for after the cleanup: today's value − Nginx − PHP-FPM + `fw-alarm`. Try the ballast unit once. **Below 25 %: create `/etc/fw-alarm/ballast`, so the switch enables the ballast unit.** Numbers into `server/README.md`.
- [ ] **Step 6:** Remove the trial: stop and remove the trial unit, `/var/lib/fw-alarm-trial`, `/etc/fw-alarm-trial`, `/run/fw-alarm-trial`; nothing listens on 8082; the live checks (`bash server/check.sh public …`) still pass. Findings → fixes with tests → push → Step 1 again where code changed.

### Task 35: The switch — date and 30 minutes together with the user

Preconditions: Tasks 1–34 done; GitHub green on the revision to deploy; no flash of the box in the last 3 days and none planned for the next 3; not at the start or end of a working day; the user is there.

- [ ] **Step 1 (PC):** `bash server/backup.sh` (the PHP one) → ok. `bash server/go/deploy.sh --install-only HEAD`.
- [ ] **Step 2 (VM):** `sudo bash ~/fw-server-go/cutover.sh preflight` → passes (checker line `WEB_OK`, demo off, no alarm running, no open command, nobody "Evtl. stumm"); note the offline threshold it prints. Otherwise: not today.
- [ ] **Step 3:** `cutover.sh stop-timer`.
- [ ] **Step 4 (NAS):** put the bridge on hold (`bridge_hold.php`, `nas_bridge/README.md`). From now the box cannot reach the server.
- [ ] **Step 5 (VM):** `cutover.sh switch` — from here until its last line there is no dashboard and no manual alarm (expected 3–5 min); the box alarms by itself as always.
- [ ] **Step 6 (NAS), directly after `fw-alarm health` is good:** lift the hold; replay the mute calls it noted. (The longer checks come after — the box should not stay cut off for them.)
- [ ] **Step 7:** `sudo bash ~/fw-server-go/check.sh local` and (PC) `bash server/go/check.sh public https://alarm.tunero.dev` → only `PASS`. Look into the journal of `cloudflared` for failed `api/mute.php` calls during the gap and follow up.
- [ ] **Step 8: Harmless checks only, all under the send lock** (`fw-alarm health` shows `send_guard=aktiv bis …`): the box shows online (journal: `POST /api/status.php` 200 `bridge=1`), the TEST command arrives, the user's own shortcut link on and off, `fw-alarm check` prints a `WEB_OK` line; everyone logs in again.
- [ ] **Step 9: The one real send — together, and only after the server has said whom it would reach.** The user switches demo mode to THEMSELVES and waits for the band to confirm that the box took the demo list. Then on the VM `fw-alarm health` must show exactly: `alarm_mode=demo`, `alarm_targets=1`, one `alarm_target=` line with the user's name and WITHOUT the lock marker (if it carries the marker, the user's recipient key differs from the status key: add it to `send_guard_keys` with `sudo fw-alarm edit-config` — their own key, nobody else's). Anything else: no send today. The sheet in the dashboard must name only the user. Then: **one alarm and one Entwarnung**, each arrives on the user's phone, the result names one recipient reached. Back to LIVE; `fw-alarm health` shows `alarm_mode=live` and all recipients, each with the lock marker — proof that even now nothing could reach them.
- [ ] **Step 10: Lift the send lock** — the last act of the evening, a deliberate one: `sudo fw-alarm edit-config`, remove the two `send_guard_*` lines; `fw-alarm health` shows `send_guard=aus` and no lock marker. (Forgotten, it ends by itself 90 minutes after the switch.) While demo mode is on, the BOX alarms only the demo recipient too — that is how demo mode works; keep Step 9 short.
- [ ] **Step 11:** If a check fails and cannot be fixed on the spot: `sudo bash ~/fw-server-go/rollback.sh` — this evening only. A later second switch uses `cutover.sh switch --force`.
- [ ] **Step 12:** `CLAUDE.md` ("Wo was läuft": Go serves; deploy = `server/go/deploy.sh`), `server/README.md`, memory notes. Commit, push.

### Task 36: Three quiet days

- [ ] Daily: `bash server/go/check.sh local` on the VM (it includes: Nginx and PHP-FPM inactive and masked, `fw-alarm` holds the port); no `INFO send-guard` line (the send lock is off); the journal has no status 5xx (`journalctl -u fw-alarm --since yesterday | grep -c '" 5[0-9][0-9] '` is 0); the check lines hold no `SEND_FAILED`/`WEB_FAIL`; the NAS watcher's log shows `cron_age` below 900; the share of used memory (ballast on when below 25 %). **After every restart of the VM** (04:30 updates): `systemctl is-active fw-alarm cloudflared` and `check.sh local`. The first morning: the box's daily heartbeat arrived and the user is still logged in on the iPhone. A fault found now is fixed in Go and deployed with `server/go/deploy.sh`.

### Task 37: Cleanup — PHP and Nginx leave (the user confirms the deletions)

**Files:**
- Delete: every `*.php` under `nas_dashboard/` and its `SETUP.md`, `data/`; `server/nginx/`, `server/php/`, `server/logrotate/`, `server/systemd/`, `server/setup.sh`, `deploy.sh`, `backup.sh`, `check.sh`; `tests/backend/php-*.test.mjs`, the PHP driver in `harness.mjs`, `tests/backend/parity.mjs`, `tests/backend/fixtures/php-data/`, `internal/phpimport/` and `import-php` in `main.go`, `tests/server/backup.test.mjs` and the PHP deploy case; the two PHP-executing tests in `wording.test.mjs`; `cutover.sh`, `rollback.sh` and their test
- Move: `server/go/*` → `server/`; `nas_dashboard/` → `web/` (`webassets.go`, every path in tests and docs)
- Modify: `tests/backend/harness.mjs` (default driver `go`), `tests/dashboard/wording.test.mjs` (Go sources only), the two frontend texts → `config.toml`, `cloud_watcher/` texts that name `config.php`, `check_offline.php` or the timer (wording only; `tests/watcher/` with them), `.github/workflows/ci.yml` (paths), `.gitattributes` (paths), `CLAUDE.md` (rewritten: "Wo was läuft", "Architektur", test commands — the rules stay), `README.md`, `server/README.md`, `tests/backend/README.md`, `nas_bridge/README.md` (wording only), spec status line

- [ ] **Step 1 (repo):** make the changes; `bash tools/check-go.sh`, `bash tools/test-go.sh`, the seven scenarios, all `node --test` suites → pass; `grep -rn "config\.php\|php-fpm\|nginx" --include='*.go' --include='*.js' --include='*.sh' --include='*.md' .` leaves only history. Commit, push, GitHub green, `bash server/deploy.sh HEAD`.
- [ ] **Step 2 (VM): the Unix socket** — its own step with its own check, and the first real call of the tunnel through the socket (the trial could only validate the file): `listen = "unix:/run/fw-alarm/http.sock"` through `fw-alarm edit-config`, the socket variant of `cloudflared`'s `config.yml` (the old file kept next to it), restart both, `bash server/check.sh public …` → only `PASS`; the way back is the old `config.yml` and the old `listen`.
- [ ] **Step 3 (VM):** a last backup of the old data; list exactly what goes and that it cannot be undone — the user confirms; then `apt-get purge` Nginx and PHP, remove `/srv/fw-alarm`, the old timer units, the Nginx and PHP logs.
- [ ] **Step 4:** memory share measured and recorded; fresh `bash server/backup.sh`; memory notes; the passkey plan is next (rewritten for Go before it starts).

