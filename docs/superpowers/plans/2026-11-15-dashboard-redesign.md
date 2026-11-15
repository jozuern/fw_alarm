# Dashboard-Neubau Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the NAS dashboard frontend (`nas_dashboard/`) with a new dark, iOS-style app (tabs on iPhone, sidebar on PC) built on vendored Preact/htm without a build step, plus the agreed new features (rename, replace key, phone, auto-unmute setting, log search, theme, browser alert, home-screen mode, 12-h persistent login).

**Architecture:** PHP backend stays; new actions are additive. The new UI is a set of classic `<script>` files under `nas_dashboard/assets/app/` sharing the global `FW` namespace: a DOM-free `logic.js` (unit-tested in Node via `vm`), `api.js` (requests/pollers), `store.js` (central state + actions), `ui.js` (shared components), one file per view, and `main.js` (router + layout). It is developed behind `next.php` and switched into `index.php` in the last task; the current UI is frozen as `classic.php`.

**Tech Stack:** PHP 8.3 (Synology Web Station), vanilla JS (ES2020, classic scripts), Preact + hooks + htm 3.1.1 standalone UMD bundle (vendored), CSS custom properties, Node 24 `node --test` for unit tests, headless Chrome over CDP for end-to-end checks.

**Spec:** `docs/superpowers/specs/2026-11-15-dashboard-redesign-design.md` (German). Section numbers below (§) refer to it. The current frontend `nas_dashboard/assets/app.js` is the reference for every behavior the spec's §7 says to carry over — port it, don't reinvent it.

## Global Constraints

- CSP stays `default-src 'self'` without `unsafe-inline`/`unsafe-eval`: no inline `<script>` content, no `style=` attributes in PHP-rendered HTML, no CDN; setting `el.style`/CSS variables from JS is fine.
- No build step; every asset tag carries `?v=<filemtime>`; no ES modules, no service worker.
- All visible text German with real umlauts; say „die Box“, never „ESP32“; anything sent to Bark stays ASCII.
- Firmware (`boss925_alarm_bark/`, `platformio.ini`) is not touched.
- Backend changes are additive; existing actions keep their response shape; `classic.php` must keep working against the new backend.
- Every new dashboard action gets an explicit `DASHBOARD_ACTIONS` entry (`post`/`csrf`/`admin` all `true` for the new write actions; `settings_list` = `post false, csrf false, admin true`).
- Every new log event gets a `log_event_types()` entry and a text in `logic.describeLogEntry`.
- The NAS must work with old AND new firmware status fields (`alarm_seq`, `alarm_source`, `core`, `max_alloc_heap`, `alarm_gaveup` may be missing).
- Never create `nas_dashboard/config.php` in the repo; the sandbox lives in the session scratchpad.
- Request timeouts: poll 10 000 ms, POST 20 000 ms, single Bark send 30 000 ms, alarm 120 000 ms. Stale after 15 000 ms without a successful status.
- Poll intervals: status 5 s, keys 30 s, log 30 s, alarm info 60 s, notify 60 s; hidden tab stops polling (exception §8.6: status every 60 s).
- Hold-to-confirm duration 1 500 ms; confirmation deadline 10–25 s (`Math.min(25, Math.max(10, Math.round(4 + words * 0.4)))`).
- Breakpoint: `< 900px` tab bar + bottom sheets; `≥ 900px` sidebar + dialogs/side panel.
- Colors (dark): `--bg #000`, `--surface #1c1c1e`, `--surface-2 #2c2c2e`, `--surface-3 #3a3a3c`, `--separator #38383a`, `--text #fff`, `--text-2 #98989f`, `--text-3 #5a5a5e`, `--accent #ff453a`, `--alarm #ff3b30`, `--ok #30d158`, `--warn #ff9f0a`, `--crit #ff6961`, `--info #0a84ff`. Light: `#f2f2f7`/`#fff`/`#c6c6c8`/`#000`/`#6d6d72`, accent `#d70015`, status `#34c759`/`#ff9500`/`#d70015`.
- System font stack only: `-apple-system, "SF Pro Text", "Segoe UI Variable Text", "Segoe UI", system-ui, sans-serif`; tabular numerals for times/numbers; inputs ≥ 16px; tap targets ≥ 44px.
- Commits go directly to `main`, German commit messages with umlauts, ending with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. `core.autocrlf=true` handles line endings; new `*.sh` under `tests/` must be LF (`.gitattributes`).

## Review Focus

1. **A 5-s status refresh while a sheet, slider drag, text input or confirmation is open** — nothing the user is doing may close, reset or lose focus. Test: Task 10, step "live refresh keeps sheet state".
2. **App sent to background (iPhone) or tab hidden while an alarm confirmation is open** — it must close, and a later tap must not send. Test: Task 9, step "visibility closes confirm".
3. **Demo/LIVE switched from another device while an alarm confirmation is open, or between confirm and send** — confirmation closes with a note; a 409 sends nothing and adopts the new mode. Test: Task 9, step "mode change while armed".
4. **Session expires (401) while REAL ALARM is in flight** — result stays readable, no reload. Test: Task 7, store test "sendNasAlarm 401 while in flight" (the Windows `php -S` sandbox is single-threaded, so this cannot be raced end-to-end).
5. **320-px-wide iPhone with 10 recipients, long names and several hints while the box is offline** — no horizontal scrolling, alarm button visible without scrolling. Test: Task 9, step "small screen offline", and Task 10, step "ten recipients narrow".

---

## File Structure

| Path | Responsibility |
|---|---|
| `tests/dashboard/sandbox/setup.sh`, `make-config.php`, `bark-mock.php`, `start.sh`, `stop.sh`, `box.sh` | Local sandbox: copy of the dashboard + config + Bark mock + simulated box |
| `tests/dashboard/e2e/cdp.mjs` | Minimal Chrome DevTools Protocol client (launch, viewport, goto, eval, screenshot, login) |
| `tests/dashboard/e2e/run.mjs` + `scenarios/*.mjs` | End-to-end scenarios with assertions and screenshots |
| `tests/dashboard/helpers.mjs` | Loads browser scripts into a Node `vm` context |
| `tests/dashboard/*.test.mjs` | Unit tests (`node --test tests/dashboard/`) |
| `nas_dashboard/lib/common.php` | + app settings (`mute_max_seconds` override), cookie refresh, new log event types |
| `nas_dashboard/dashboard_api.php` | + `settings_list`, `settings_set`, `keys_rename`, `keys_replace_key`, `keys_set_phone`; `keys_list` + `phone`, `mute_max_seconds` |
| `nas_dashboard/lib/shell.php` | Renders the new app shell (head, meta, script tags) — used by `next.php`, later `index.php` |
| `nas_dashboard/next.php` | Temporary entry for the new UI (removed in Task 13) |
| `nas_dashboard/assets/vendor/htm-preact-3.1.1.umd.js` + `LICENSE-htm.txt`, `LICENSE-preact.txt` | Vendored Preact/htm |
| `nas_dashboard/assets/app/logic.js` | Pure logic (no DOM, no Preact) → `FW.logic` |
| `nas_dashboard/assets/app/api.js` | `FW.api`: requests, errors, pollers, CSRF |
| `nas_dashboard/assets/app/store.js` | `FW.store`: state, polling, actions, confirm/result state |
| `nas_dashboard/assets/app/ui.js` | `FW.ui`: shared components |
| `nas_dashboard/assets/app/view-overview.js`, `view-recipients.js`, `view-log.js`, `view-settings.js` | `FW.views.*` |
| `nas_dashboard/assets/app/main.js` | Router, layout (tab bar/sidebar), demo band, badge, title, theme, mount |
| `nas_dashboard/assets/app.css` | All styles of the new UI and the new login page |
| `nas_dashboard/classic.php`, `assets/classic/app.js`, `assets/classic/style.css` | Frozen old UI (Task 13) |
| `nas_dashboard/manifest.json`, `assets/icons/icon-192.png`, `icon-512.png` | Home-screen app mode (Task 13) |

Script order (both `next.php` and final `index.php`, produced by `app_script_paths()`): `assets/vendor/htm-preact-3.1.1.umd.js`, `assets/app/logic.js`, `api.js`, `store.js`, `ui.js`, `view-overview.js`, `view-recipients.js`, `view-log.js`, `view-settings.js`, `main.js` — all `defer`.

Every `assets/app/*.js` file is wrapped as:

```js
(function (root) {
  'use strict';
  const FW = root.FW = root.FW || {};
  // ...
  FW.logic = { /* exports */ };
})(typeof window !== 'undefined' ? window : globalThis);
```

---

### Task 1: Sandbox and end-to-end tooling

**Files:**
- Create: `tests/dashboard/sandbox/setup.sh`, `make-config.php`, `bark-mock.php`, `start.sh`, `stop.sh`, `box.sh`, `README.md`
- Create: `tests/dashboard/e2e/cdp.mjs`, `tests/dashboard/e2e/run.mjs`, `tests/dashboard/e2e/scenarios/smoke.mjs`
- Modify: `.gitattributes` (add `tests/dashboard/sandbox/*.sh text eol=lf`)

**Interfaces:**
- Produces (shell): `setup.sh <dir>` creates `<dir>/site` (copy of `nas_dashboard/` without `config.php`/`data/`), `<dir>/data`, `<dir>/site/config.php`; re-running only re-copies `site/` (keeps `data/`, `config.php`). `start.sh <dir>` serves `site` on `127.0.0.1:8787` and the mock on `127.0.0.1:8789` (PIDs in `<dir>/pids`), `stop.sh <dir>`. `box.sh <dir> [field=value ...]` POSTs a status push with defaults (below) overridden by arguments.
- Sandbox credentials: admin `admin`/`test`, readonly `leser`/`test`, machine token `box-token`, `bark_key_status` `statuskey0001`.
- Bark mock: logs every request as one JSON line `{"t":<epoch>,"key":"…","fields":{…}}` to `<dir>/bark.log`; key prefix `fail` → HTTP 500, `bad` → HTTP 400, `slow` → sleep 4 s then 200; otherwise 200 `{"code":200,"message":"success"}`.
- Produces (Node): `cdp.mjs` exports `launch({ port = 9333, headless = true }) → Promise<Browser>`; `Browser.newPage() → Promise<Page>`; `Browser.close()`; `Page.viewport({ width, height, mobile = false, scale = 1 })`; `Page.goto(url)` (waits for `load`); `Page.eval(expr) → value` (awaits promises, returns by value); `Page.waitFor(expr, timeoutMs = 10000)` (polls until truthy); `Page.screenshot(path)`; `Page.login(base, user, pass)`; `Page.click(selector)`.
- `run.mjs <scenario> [--out <dir>]` imports `scenarios/<scenario>.mjs` whose default export is `async ({ browser, base, sandbox, out, assert, shot }) => void`; `shot(page, name)` writes `<out>/<scenario>-<name>.png`; exit code 1 on failure. Env: `SANDBOX_DIR` (required), `SANDBOX_BASE` (default `http://127.0.0.1:8787`).

- [ ] **Step 1: Write `make-config.php`**

PHP CLI script `php make-config.php <dir>` that writes `<dir>/site/config.php` from `nas_dashboard/config.example.php`'s array with these overrides: `machine_token_sha256 = hash('sha256','box-token')`, `dashboard_password_hash = password_hash('test', PASSWORD_DEFAULT)`, `dashboard_readonly_user = 'leser'`, `dashboard_readonly_password_hash = password_hash('test', PASSWORD_DEFAULT)`, `bark_key_status = 'statuskey0001'`, `bark_host = 'http://127.0.0.1:8789'`, `bark_keys_alarm = ['startkey0001']`, `data_dir` = absolute data path in drive-relative form (strip `C:`; `data_dir()` rejects `C:\`), `health_check_url = 'http://127.0.0.1:8787/api/health.php'`, `offline_after_seconds = 30` (fast offline tests).

- [ ] **Step 2: Write `setup.sh`, `start.sh`, `stop.sh`, `box.sh`, `bark-mock.php`**

`start.sh` runs `php -d extension_dir="$(dirname "$(command -v php)")/ext" -d extension=curl -d extension=openssl -S 127.0.0.1:8787 -t "$dir/site"` and `BARK_LOG="$dir/bark.log" php -S 127.0.0.1:8789 bark-mock.php` in the background. `box.sh` defaults: `token=box-token device_id=fw-box-1 fw_build=sandbox core=2.0.17 uptime_s=360000 wifi=up rssi=-61 ip=192.168.1.50 relay=open cooldown=0 waiting_for_release=0 alarm_pending=0 isr_latched=0 key_progress=0/0 last_alarm=nie last_heartbeat=nie heartbeat_ok=0 stuck_warned=0 ack_pending=0 keys_count=1 keys_version=0 keys_muted=0 reset_reason=poweron free_heap=180000 max_alloc_heap=90000 alarm_seq=0 alarm_source= alarm_gaveup=0`, POSTed with `curl --form-string` to `/api/status.php`. `README.md`: the five commands and the credentials (Git Bash pitfalls: `MSYS_NO_PATHCONV=1`).

- [ ] **Step 3: Write `cdp.mjs`, `run.mjs`, `scenarios/smoke.mjs`**

Chrome path default `C:/Program Files/Google/Chrome/Application/chrome.exe` (env `CHROME` overrides), flags `--headless=new --remote-debugging-port=<port> --user-data-dir=<tmp> --no-first-run --hide-scrollbars`; page target via `PUT /json/new?about:blank`; WebSocket = Node's global `WebSocket`. `login()` fills `#user`/`#password` and submits the form (works for old and new login pages, both keep these ids). `smoke.mjs`: login as admin on `/index.php`, assert `document.title` contains `Alarm-Wächter`, screenshot.

- [ ] **Step 4: Verify the toolchain against the CURRENT dashboard**

Run:
```bash
S="$TMPDIR_SCRATCH/sandbox"   # session scratchpad
bash tests/dashboard/sandbox/setup.sh "$S" && bash tests/dashboard/sandbox/start.sh "$S"
bash tests/dashboard/sandbox/box.sh "$S"
SANDBOX_DIR="$S" node tests/dashboard/e2e/run.mjs smoke --out "$S/shots"
```
Expected: `box.sh` prints `OK`; `run.mjs` exits 0 and `$S/shots/smoke-*.png` shows the logged-in current dashboard with „Einsatzbereit“.

- [ ] **Step 5: Commit**

```bash
git add .gitattributes tests/dashboard/sandbox tests/dashboard/e2e
git commit -m "Tests: lokale Sandbox und Ende-zu-Ende-Werkzeug fürs Dashboard"
```

---

### Task 2: Backend — auto-unmute setting (`settings_list` / `settings_set`)

**Files:**
- Modify: `nas_dashboard/lib/common.php` (near `mute_max_seconds()` ~1694, `log_event_types()` ~931, lock/inplace lists)
- Modify: `nas_dashboard/dashboard_api.php` (`DASHBOARD_ACTIONS` ~36, `keys_list` ~305, new handlers)
- Modify: `nas_dashboard/index.php` (the „Stumm“ explanation uses `mute_max_seconds($config)` — unchanged call, now effective value)
- Modify: `CLAUDE.md` (Arbeitsmodus bullet: effective value from `app_settings.json`)

**Interfaces:**
- Produces (PHP): `load_app_settings(array $config): array` → `['mute_max_seconds' => ?int]` (missing/corrupt file → `null` entry); `mute_max_choices(array $config): array` → list of seconds, sorted, = `[7200, 14400, 21600, 28800, 43200, 57600, 86400]` ∪ `{config value}`; `mute_max_seconds(array $config): int` → override if set and in `mute_max_choices()`, else config value (default 43200). Data file `app_settings.json` via `data_path()`/`write_json_file()`, lock name `settings`.
- Produces (HTTP): `GET dashboard_api.php?action=settings_list` → `{ok:true, settings:{mute_max_seconds:{value:int, default:int, choices:[{value:int, text:string}]}}}` (`text` via `notify_duration_label()`, config value's text suffixed ` (Standard)`, `0` → `nie`). `POST action=settings_set key=mute_max_seconds value=<int>` → `{ok, message}`; unknown key/value → `ok:false`, nothing written. `keys_list` response adds `mute_max_seconds:int`.
- Log event `setting_changed`: `['cat' => 'system', 'admin' => true, 'fields' => ['by' => $by, 'label' => $label, 'value' => ['str','value']]]` (label „Automatisch wieder laut nach“, value = duration text).

- [ ] **Step 1: Write the failing check**

Add scenario `tests/dashboard/e2e/scenarios/api-settings.mjs` (HTTP only, uses `fetch` with the session cookie from a CDP login, or a cookie-jar `fetch` helper `apiSession(base, user, pass)` added to `cdp.mjs`, returning `{ get(action, params), post(fields) }` that handles CSRF via the status response's `csrf`):
```js
const s = await apiSession(base, 'admin', 'test');
let r = await s.get('settings_list');
assert.equal(r.settings.mute_max_seconds.value, 43200);
assert.ok(r.settings.mute_max_seconds.choices.some((c) => c.value === 7200));
r = await s.post({ action: 'settings_set', key: 'mute_max_seconds', value: '14400' });
assert.equal(r.ok, true);
assert.equal((await s.get('keys_list')).mute_max_seconds, 14400);
r = await s.post({ action: 'settings_set', key: 'mute_max_seconds', value: '999' });
assert.equal(r.ok, false);
assert.equal((await s.get('keys_list')).mute_max_seconds, 14400);
const ro = await apiSession(base, 'leser', 'test');
assert.equal((await ro.post({ action: 'settings_set', key: 'mute_max_seconds', value: '7200' })).status, 403);
const log = await s.get('log_view', { limit: 5, filter: 'system' });
assert.equal(log.entries[0].event, 'setting_changed');
```
(`post` returns the parsed JSON plus `status` = HTTP status.)

- [ ] **Step 2: Run it to verify it fails**

Run: `bash tests/dashboard/sandbox/setup.sh "$S" && SANDBOX_DIR="$S" node tests/dashboard/e2e/run.mjs api-settings`
Expected: FAIL (`Unbekannte Aktion` / HTTP 400).

- [ ] **Step 3: Implement** the PHP functions and the two handlers (pattern: `notify_set` — `with_lock($config, 'settings', …)`, read-modify-write, `append_command_log` after the lock with event `setting_changed`). Add `app_settings.json` handling exactly like other `rename()`-replaced data files (it is NOT an in-place file). `keys_list` adds `'mute_max_seconds' => mute_max_seconds($config)`.

- [ ] **Step 4: Verify**

Run: `php -l nas_dashboard/lib/common.php && php -l nas_dashboard/dashboard_api.php` → `No syntax errors`; re-run setup + `run.mjs api-settings` → exit 0. Also run `php nas_dashboard/cron/check_offline.php` inside the sandbox copy (with the `-d` flags from `start.sh`) → no PHP warnings.

- [ ] **Step 5: Commit** — `git commit -m "Dashboard-API: Dauer bis „automatisch wieder laut“ im Dashboard einstellbar"`

---

### Task 3: Backend — rename, replace key, phone

**Files:**
- Modify: `nas_dashboard/dashboard_api.php` (new handlers after `keys_delete`, `keys_list` item)
- Modify: `nas_dashboard/lib/common.php` (`log_event_types()`)
- Modify: `CLAUDE.md` (Empfängerliste bullet)

**Interfaces:**
- `POST action=keys_rename id label` → label rule identical to `keys_add` (`/^[^\p{Cc}]{1,40}$/u`); no version bump; log `key_renamed` fields `by, label (new), old_label, key`. If the entry is the active demo recipient, also update `label` in `demo_mode.json` (via the same helper `demo_set` uses to write it) so the banner shows the new name.
- `POST action=keys_replace_key id key` → `valid_bark_key`, not equal to another entry's key, refused for the active demo recipient with message „Dieser Empfänger ist gerade der Demo-Empfänger. Zuerst den Demo-Modus beenden.“; keeps every other field; `'bump' => true`; log `key_replaced` fields `by, label, key (new masked), old_key (old masked)`.
- `POST action=keys_set_phone id phone` → trimmed; `''` removes the field; else must match `/^[0-9+\-\/() ]{3,30}$/`; no bump; log `key_phone_changed` fields `by, label` (never the number).
- `keys_list`: admin items get `'phone' => (string)($entry['phone'] ?? '')`; readonly items never contain `phone`.
- All three `cat => 'keys'`.

- [ ] **Step 1: Write the failing check** — scenario `api-keys.mjs`:
```js
const s = await apiSession(base, 'admin', 'test');
await s.post({ action: 'keys_add', label: 'Anna', key: 'annakey0001' });
const id = (await s.get('keys_list')).keys.find((k) => k.label === 'Anna').id;
const v0 = (await s.get('keys_list')).version;
assert.equal((await s.post({ action: 'keys_rename', id, label: 'Anna B.' })).ok, true);
assert.equal((await s.get('keys_list')).version, v0);                       // no bump
assert.equal((await s.post({ action: 'keys_set_phone', id, phone: '+49 171 1234567' })).ok, true);
assert.equal((await s.post({ action: 'keys_set_phone', id, phone: 'abc' })).ok, false);
let k = (await s.get('keys_list')).keys.find((x) => x.id === id);
assert.equal(k.label, 'Anna B.'); assert.equal(k.phone, '+49 171 1234567');
const token = k.mute_token;
assert.equal((await s.post({ action: 'keys_replace_key', id, key: 'annakey0002' })).ok, true);
const after = await s.get('keys_list');
k = after.keys.find((x) => x.id === id);
assert.ok(after.version > v0); assert.equal(k.mute_token, token); assert.equal(k.phone, '+49 171 1234567');
await s.post({ action: 'keys_add', label: 'Max', key: 'maxkey00001' });
assert.equal((await s.post({ action: 'keys_replace_key', id, key: 'maxkey00001' })).ok, false); // duplicate
await s.post({ action: 'demo_set', enabled: '1', id });
assert.equal((await s.post({ action: 'keys_replace_key', id, key: 'annakey0003' })).ok, false); // demo target
await s.post({ action: 'keys_rename', id, label: 'Anna C.' });
assert.equal((await s.get('status')).demo.label, 'Anna C.');
await s.post({ action: 'demo_set', enabled: '0' });
const ro = await apiSession(base, 'leser', 'test');
assert.ok((await ro.get('keys_list')).keys.every((x) => !('phone' in x)));
const events = (await s.get('log_view', { limit: 20, filter: 'keys' })).entries.map((e) => e.event);
for (const e of ['key_renamed', 'key_replaced', 'key_phone_changed']) assert.ok(events.includes(e));
assert.ok(!JSON.stringify(await s.get('log_view', { limit: 20, filter: 'keys' })).includes('1234567'));
```

- [ ] **Step 2: Run to verify it fails** — `run.mjs api-keys` → FAIL (unknown action).
- [ ] **Step 3: Implement** with `mutate_alarm_keys()` (pattern: `keys_delete` for the demo check, `keys_add` for validation/duplicates) and `log_key_event()` after the lock.
- [ ] **Step 4: Verify** — `php -l` both files; setup + `run.mjs api-keys` and `run.mjs api-settings` → exit 0; `curl` `api/keys.php` with the machine token in the sandbox shows the replaced key and no phone line.
- [ ] **Step 5: Commit** — `git commit -m "Dashboard-API: Empfänger umbenennen, Key ersetzen, Telefonnummer"`

---

### Task 4: Backend — login survives closing the iPhone app (12 h)

**Files:**
- Modify: `nas_dashboard/lib/common.php` (`start_dashboard_session()` ~571, `is_dashboard_authed()` ~609)
- Modify: `CLAUDE.md` (session sentence: persistent 12-h cookie, refreshed per authenticated request)

**Interfaces:**
- Produces: `refresh_dashboard_cookie(): void` — if headers not sent, `setcookie(session_name(), session_id(), ['expires' => time() + SESSION_MAX_IDLE_SECONDS, 'path' => dashboard_cookie_path(), 'secure' => <same as today>, 'httponly' => true, 'samesite' => 'Strict'])`. Called from `is_dashboard_authed()` when it returns `true`, and right after `session_regenerate_id(true)` on login (index.php). `start_dashboard_session()` sets `'lifetime' => SESSION_MAX_IDLE_SECONDS`. Logout unchanged (expires = 1).

- [ ] **Step 1: Write the failing check** — scenario `api-session.mjs`: log in via plain `fetch` (`redirect: 'manual'`), assert the login response's `Set-Cookie` for `boss925_dashboard` has an `Expires`/`Max-Age` ≈ 12 h (±60 s); then GET `dashboard_api.php?action=status` with that cookie and assert it again sends `Set-Cookie` with a fresh expiry; POST logout and assert the cookie is cleared.
- [ ] **Step 2: Run to verify it fails** — expected FAIL (no `Expires` today).
- [ ] **Step 3: Implement** as specified.
- [ ] **Step 4: Verify** — `php -l`; setup + `run.mjs api-session` → exit 0; `run.mjs smoke` still exit 0.
- [ ] **Step 5: Commit** — `git commit -m "Anmeldung: Cookie gilt 12 Std und übersteht das Schließen der iPhone-App"`

---

### Task 5: Vendor Preact/htm + logic part 1 (formatting, box rows, hints, verdict, overview rows)

**Files:**
- Create: `nas_dashboard/assets/vendor/htm-preact-3.1.1.umd.js` (= `https://unpkg.com/htm@3.1.1/preact/standalone.umd.js`, sha256 `YsT/6kAAeMDtp/Q1sicmtJYevfdMhQbxiOkr7EHYrFE=` base64; download into an empty scratch folder, verify hash, copy; do NOT execute it outside the browser), `LICENSE-htm.txt` (Apache-2.0 from the htm package), `LICENSE-preact.txt` (MIT from preact 10.x)
- Create: `nas_dashboard/assets/app/logic.js`
- Create: `tests/dashboard/helpers.mjs`, `tests/dashboard/logic-status.test.mjs`

**Interfaces:**
- `helpers.mjs` exports `loadScripts(files: string[], globals = {}) → context` (runs repo-relative files in one `vm` context whose global object has `globals` assigned) and `plain(v) = JSON.parse(JSON.stringify(v))` (vm objects fail `deepStrictEqual` across realms).
- Vendor global: `window.htmPreact` with `html, render, h, Component, createContext, useState, useEffect, useLayoutEffect, useRef, useMemo, useCallback, useReducer, useContext`.
- `FW.logic` (this task):
  - `setTimezone(name: string): void` (ignored if `Intl` rejects it), `tz(): string` (default `Europe/Berlin`)
  - `na(v): boolean`, `show(v): string`, `flagged(v): boolean`
  - `fmtUptime(s): string` (`"4 T 3 Std 12 Min"` / `"3 Std 12 Min"` / `"12 Min"`), `rssiQuality(dbm): string|null`, `fmtRssi(v): string` (`"-61 dBm (gut)"`), `fmtHeap(v): string`
  - `fmtDateTime(epoch): string` (0 → `'nie'`), `fmtClock(epoch)`, `fmtDayClock(epoch)`, `dayKey(epoch)`, `fmtDayLabel(epoch, now)`, `fmtDayTime(epoch, now)`, `fmtRelTime(epoch, now)`, `fmtAgeShort(age): string|null`
  - `RESET_REASONS`, `RESET_WARN`, `HEAP_WARN = 50000`, `MAX_ALLOC_WARN = 30000`
  - `keyProgressParts(s): [number, number]|null`, `deliveryIncomplete(s): boolean`, `heartbeatFailed(s): boolean`, `weakWifi(s): boolean`, `lowMemory(s): boolean`, `resetWarnFresh(s): boolean`, `isStuck(s, offline): boolean`, `alarmStanding(s, offline): boolean`
  - `watchdogState(info, serverTime): {warn: boolean, text: string}|null` and `outsideState(info, serverTime): {warn, text}|null` (texts verbatim from `app.js` 757–821)
  - `boxRows(s, ctx): Array<{group: 'Alarmkette'|'Verbindung'|'Wartung', id: string, label: string, value: string, tone: ''|'warn'|'crit'|'unknown', tip: string}>`; `ctx = {offline, seenAge, watchdog (with .outside), nasKeysVersion, stale: boolean}`; rows/values/tips ported from `ROWS` (`app.js` 510–642); `tone 'unknown'` for every non-live row when `offline || stale`.
  - `collectHints(ctx): Array<{tone: 'warn'|'crit', text: string}>`; `ctx = {status, offline, stale, watchdog, keysSummary: {total, muted, permanent, overdue}|null, keysCorrupt: string, demoOn: boolean, deviceHint: string, mismatchMs: number, readOnly: boolean}` — exactly the list in spec §6.2 „Was ein Hinweis ist“; returns `[]` when `stale` or no status.
  - `verdict(ctx): {key: string, tone: 'ok'|'warn'|'crit'|'alarm', title: string, sub: string, raiseActions: boolean}`; `ctx = {authLost, statusError: {kind, status, message}|null, stale, alarmInFlight, offline, status, demo, readOnly, hints}`; order §6.2 (1–9), except that `statusError` or `stale` always yields `nas` (as today's `showStatusProblem` overrides the lamp) with tone `crit` when `offline` (last known) else `warn`; keys `auth|offline|nas|alarm|stuck|standing|demo|hints|ok`; titles: `Abgemeldet`, `Box offline`, `NAS nicht erreichbar`/`NAS-Störung`/`Status veraltet`/`NAS beschäftigt`, `Alarm läuft`, `Kontakt klemmt`, `Alarm ausgelöst`, `Demo-Modus`, `Bereit – 1 Hinweis`/`Bereit – N Hinweise`, `Alles bereit`; subs ported from `refresh()`/`showStatusProblem()` (`app.js` 1367–1392, 1487–1494); `raiseActions = tone === 'crit' || tone === 'alarm'`.
  - `overviewRows(ctx): Array<{id: 'box'|'wifi'|'watch'|'keys'|'alarm', label, value, tone, href: string|null}>`; `ctx` = verdict ctx + `{seenAge, watchdog, keysSummary, keysCorrupt, alarmFact: {text, tone}}`; hrefs `#/einstellungen/box`, `null`, `#/einstellungen/box`, `#/empfaenger`, `#/verlauf?filter=alarm`.
  - `keysSummary(keysListData): {total, muted, permanent, overdue}|null` (port of `refreshKeys` 2046–2051).
  - `ALARM_RECENT_S = 3600`; `recipientsFact(keysSummary, demo, keysCorrupt): {text, tone}` (texts from `app.js` 923–938, shortened to the row format `6, 1 stumm`).

- [ ] **Step 1: Write the failing tests** (`logic-status.test.mjs`), e.g.:
```js
const L = loadScripts(['nas_dashboard/assets/app/logic.js']).FW.logic;
const base = { relay: 'open', wifi: 'up', rssi: '-61', alarm_pending: '0', key_progress: '0/0', last_alarm: 'nie',
  heartbeat_ok: '0', last_heartbeat: 'nie', uptime_s: '360000', free_heap: '180000', reset_reason: 'poweron', keys_version: '3' };
const v = (o) => L.verdict({ authLost: false, statusError: null, stale: false, alarmInFlight: false, offline: false,
  status: base, demo: { enabled: false }, readOnly: false, hints: [], ...o });

test('ranking', () => {
  assert.equal(v({}).key, 'ok');
  assert.equal(v({ offline: true, status: { ...base, alarm_pending: '1' } }).key, 'offline');
  assert.equal(v({ status: { ...base, alarm_pending: '1', key_progress: '2/6' } }).tone, 'alarm');
  assert.equal(v({ status: { ...base, relay: 'closed', stuck_warned: '1' } }).key, 'stuck');
  assert.equal(v({ status: { ...base, relay: 'closed', key_progress: '6/6', last_alarm: '[13.11. 21:42] x' } }).key, 'standing');
  assert.equal(v({ demo: { enabled: true, label: 'Anna' } }).key, 'demo');
  assert.equal(v({ hints: [{ tone: 'warn', text: 'x' }] }).title, 'Bereit – 1 Hinweis');
  assert.equal(v({ hints: [{ tone: 'warn', text: 'x' }, { tone: 'crit', text: 'y' }] }).tone, 'crit');
  assert.equal(v({ hints: [{ tone: 'crit', text: 'y' }] }).raiseActions, true);
  assert.equal(v({ authLost: true, offline: true }).key, 'auth');
});
test('nas problem keeps red when last known offline', () => {
  const r = v({ statusError: { kind: 'network' }, offline: true });
  assert.equal(r.key, 'nas'); assert.equal(r.tone, 'crit'); assert.equal(r.title, 'NAS nicht erreichbar');
  assert.equal(v({ statusError: { kind: 'timeout' }, alarmInFlight: true }).title, 'NAS beschäftigt');
  assert.equal(v({ stale: true }).title, 'Status veraltet');
  assert.equal(v({ statusError: { kind: 'http', status: 500, message: 'x' } }).title, 'NAS-Störung');
});
test('hints', () => {
  const h = (o) => L.collectHints({ status: base, offline: false, stale: false, watchdog: null, keysSummary: { total: 3, muted: 1, permanent: 0, overdue: 0 },
    keysCorrupt: '', demoOn: false, deviceHint: '', mismatchMs: 0, readOnly: false, ...o });
  assert.deepEqual(plain(h({})), []);                                                        // one muted is not a hint
  assert.equal(h({ keysSummary: { total: 3, muted: 3, permanent: 0, overdue: 0 } })[0].tone, 'warn');
  assert.equal(h({ status: { ...base, last_alarm: 'x', key_progress: '5/6' } })[0].tone, 'crit');
  assert.equal(h({ status: { ...base, last_alarm: 'x', alarm_gaveup: '1', key_progress: '6/6' } })[0].tone, 'crit');
  assert.equal(h({ status: { ...base, rssi: '-80' } })[0].tone, 'warn');
  assert.equal(h({ status: { ...base, heartbeat_ok: '0', last_heartbeat: 'nie' } }).length, 0);
  assert.equal(h({ status: { ...base, heartbeat_ok: '0', last_heartbeat: '14.11. 08:00' } }).length, 1);
  assert.equal(h({ status: { ...base, reset_reason: 'task_wdt', uptime_s: '3600' } }).length, 1);
  assert.equal(h({ status: { ...base, reset_reason: 'task_wdt', uptime_s: '90000' } }).length, 0);
  assert.equal(h({ mismatchMs: 61000 }).length, 1);
  assert.equal(h({ mismatchMs: 59000 }).length, 0);
  assert.equal(h({ keysCorrupt: 'kaputt' })[0].tone, 'crit');
  assert.equal(h({ stale: true, keysCorrupt: 'kaputt' }).length, 0);
});
test('watchdog', () => {
  assert.equal(L.watchdogState({ configured: true, last_run: 1000 }, 1000 + 2101).warn, true);
  assert.equal(L.watchdogState({ configured: true, last_run: 1000 }, 1000 + 2000).warn, false);
  assert.equal(L.watchdogState({ configured: true, last_run: 1000, last_send_failed_at: 900, last_send_ok_at: 800 }, 1100).warn, true);
  assert.equal(L.outsideState({ outside_seen_at: 1000, outside_silent_after_s: 3600 }, 1000 + 3601).warn, true);
  assert.equal(L.outsideState({}, 1000), null);
});
test('box rows grey out when offline except live rows', () => {
  const rows = L.boxRows(base, { offline: true, seenAge: 1380, watchdog: { warn: false, text: 'läuft' }, nasKeysVersion: 3, stale: false });
  assert.equal(rows.find((r) => r.id === 'wifi').tone, 'unknown');
  assert.equal(rows.find((r) => r.id === 'uptime').label, 'Offline seit');
  assert.equal(rows.find((r) => r.id === 'uptime').tone, 'crit');
});
test('format', () => {
  assert.equal(L.fmtUptime(360000), '4 T 4 Std 0 Min');
  assert.equal(L.fmtRssi('-61'), '-61 dBm (gut)');
  assert.equal(L.fmtDateTime(0), 'nie');
});
```

- [ ] **Step 2: Run** `node --test tests/dashboard/` → FAIL (file missing).
- [ ] **Step 3: Implement** `logic.js` (port from `app.js`; formatting uses `Intl` with `tz()`).
- [ ] **Step 4: Run** `node --test tests/dashboard/` → all pass.
- [ ] **Step 5: Commit** — `git commit -m "Neues Dashboard: Preact/htm eingebunden, Status-Logik mit Tests"`

---

### Task 6: Logic part 2 (alarm info, command line, log texts, unreached, recipients, confirmations)

**Files:**
- Modify: `nas_dashboard/assets/app/logic.js`
- Create: `tests/dashboard/logic-alarm.test.mjs`, `tests/dashboard/logic-log.test.mjs`

**Interfaces (added to `FW.logic`):**
- `deriveAlarmInfo(entries): AlarmInfo|null` — verbatim port of `app.js` 858–893 (`{kind, at, ok?, resent?, clearedAt?, clearedOk?}`); `ALARM_KINDS`.
- `alarmFact(alarmInfo, status, now): {text: string, tone: ''|'warn'|'crit'}` — port of `lastAlarmFact` (895–921); `undefined` info → `{text:'…', tone:''}`.
- `commandLine(command, serverTime): null|{text: string, tone: ''|'warn'|'done', canCancel: boolean}` — port of `renderCommand` (703–751) incl. `ACK_RESULTS`, `ACK_MESSAGES`, `STATE_LABELS`, 600-s linger and ack-giveup.
- `describeLogEntry(entry): {text: string, tone: 'alarm'|'warn'|'ok'|'', icon: 'alarm'|'clear'|'mute'|'unmute'|'add'|'remove'|'edit'|'test'|'demo'|'command'|'box'|'watch'|'settings'}` — texts verbatim from `app.js` 2453–2551 (`cls` → `tone`: `log-alarm`→`alarm`, `log-warn`→`warn`, recoveries/added → `ok`, else `''`), plus: `key_renamed` → `Empfänger "<old_label>" umbenannt in "<label>"<who>`, `key_replaced` → `Bark-Key von "<label>" ersetzt (<old_key> → <key>)<who>`, `key_phone_changed` → `Telefonnummer von "<label>" geändert<who>`, `setting_changed` → `Einstellung „<label>“: <value><who>`.
- `LOG_FILTERS = ['all','alarm','keys','system']`, `LOG_EMPTY` texts, `LOG_STEP = 25`, `LOG_MAX = 100`, `logMatches(entry, query): boolean` (case-insensitive substring over `describeLogEntry(entry).text`).
- `failReason(result): string` (`http 0` → `keine Verbindung/Timeout`).
- `UNREACHED_MAX_AGE_MS = 43200000`; `validateUnreached(raw: string|null, nowMs): Unreached|null` (port of `restoreUnreached` checks); `buildUnreached(prev: Unreached|null, action, data, resend: {ids, origAt}|null, nowMs): {unreached: Unreached|null, reached: string[], failed: Array<{id,name,reason}>}` — port of `showSendResult`'s merge (1769–1835). `Unreached = {action, what, at, startedAt, entries: [{id, name, reason}]}`.
- `recipientView(entry, demo, now): {state: 'loud'|'muted'|'permanent', pill: {text, tone: 'ok'|'warn'|'crit'}, sub: string, subTone: ''|'warn', isDemoTarget: boolean, paused: boolean}` — texts from `renderKeys` (1956–1981); pill `Laut`/`Stumm`/`Dauerhaft`.
- `volWords(vol)`; `muteQuestion(name, want: 'muted'|'permanent', vol): string` (texts from 2156–2173).
- `alarmQuestion(action: 'alarm'|'false_alarm'|'box_alarm'|'resend', demo, keys, extra = {}): {title: string, text: string}` (`keys` = `keys_list.keys`; `extra = {names: string[], what: 'REAL ALARM'|'ENTWARNUNG'}` for `resend`) — names the mode (LIVE / „DEMO-MODUS: nur …“), number of recipients and every muted recipient with volume (`Anna (stumm) mit Lautstärke 3`, `lautlos` for 0); for `resend` lists the names; for `box_alarm` mentions the 5-minute cooldown.
- `armTimeoutFor(text): number` (seconds).
- `expectDemoOf(demo): string` (`'0'` or `String(key_id)`).

- [ ] **Step 1: Write the failing tests**:
```js
test('alarm info with clearing', () => {
  const e = [{ event: 'false_alarm', at: 200, ok: true, demo: false }, { event: 'nas_alarm', at: 100, ok: false, demo: false }];
  assert.deepEqual(plain(L.deriveAlarmInfo(e)), { kind: 'manual', at: 100, ok: false, resent: null, clearedAt: 200, clearedOk: true });
  assert.equal(L.deriveAlarmInfo([{ event: 'false_alarm', at: 200, ok: true, demo: true }, { event: 'relay_alarm', at: 100 }]).clearedAt, undefined);
  assert.equal(L.deriveAlarmInfo([{ event: 'nas_alarm', at: 1, message: 'DEMO-MODUS: …' }]).kind, 'manual_demo');
});
test('unreached merge on resend', () => {
  const prev = { action: 'alarm', what: 'REAL ALARM', at: 1, startedAt: 50, entries: [{ id: 3, name: 'Tom', reason: 'HTTP 500' }, { id: 0, name: 'Notliste', reason: 'HTTP 0' }] };
  const r = L.buildUnreached(prev, 'alarm', { ok: true, results: [{ id: 3, label: 'Tom', ok: true }] }, { ids: [3], origAt: 50 }, 1000);
  assert.deepEqual(plain(r.unreached.entries), [{ id: 0, name: 'Notliste', reason: 'HTTP 0' }]);
  assert.equal(r.unreached.startedAt, 50);
  const none = L.buildUnreached(prev, 'alarm', { ok: false, results: [] }, { ids: [9], origAt: 0 }, 1000);
  assert.equal(none.unreached, prev);                                      // nothing sent keeps the list
});
test('validateUnreached', () => {
  const ok = JSON.stringify({ action: 'alarm', at: 1000, startedAt: 0, entries: [{ id: 1, name: 'A', reason: 'x' }] });
  assert.ok(L.validateUnreached(ok, 2000));
  assert.equal(L.validateUnreached(ok, 1000 + 43200001), null);
  assert.equal(L.validateUnreached('{"action":"x"}', 2000), null);
  assert.equal(L.validateUnreached('nonsense', 2000), null);
});
test('command line', () => {
  assert.equal(L.commandLine({ id: 4, type: 'TEST', state: 'pending' }, 100).canCancel, true);
  assert.equal(L.commandLine({ id: 4, type: 'TEST', state: 'acked', acked_at: 100, ack_result: 'executed' }, 801), null);
  assert.equal(L.commandLine({ id: 4, type: 'ALARM', state: 'acked', acked_at: 100, ack_result: 'skipped_busy' }, 200).tone, 'warn');
  assert.match(L.commandLine({ id: 4, type: 'TEST', state: 'delivered', delivered_at: 100 }, 701).text, /Bestätigung nicht angekommen/);
});
test('confirmation texts name mode and muted volumes', () => {
  const keys = [{ id: 1, label: 'Josh', muted: false }, { id: 2, label: 'Anna', muted: true, mute_volume: 3 }];
  const q = L.alarmQuestion('alarm', { enabled: false }, keys);
  assert.match(q.text, /2 Empfänger/); assert.match(q.text, /Anna \(stumm\) mit Lautstärke 3/);
  assert.match(L.alarmQuestion('alarm', { enabled: true, label: 'Anna', key_id: 2 }, keys).title, /Demo/);
  assert.equal(L.armTimeoutFor('kurz'), 10); assert.equal(L.armTimeoutFor('w '.repeat(80)), 25);
});
test('every known log event has a text', () => {
  for (const ev of ['created','delivered','acked','expired','cancelled','late_ack','demo_enabled','demo_disabled','demo_repaired',
    'key_added','key_removed','key_muted','key_unmuted','key_mute_volume','key_mute_permanent','key_test','notify_changed',
    'nas_alarm_started','false_alarm_started','nas_alarm','false_alarm','relay_alarm','box_offline','box_recovered','box_restart',
    'nas_web_failed','nas_web_recovered','watcher_silent','watcher_recovered','key_renamed','key_replaced','key_phone_changed','setting_changed'])
    assert.notEqual(L.describeLogEntry({ event: ev, label: 'A', old_label: 'B', id: 1, type: 'TEST' }).text, ev);
  assert.equal(L.describeLogEntry({ event: 'relay_alarm', source: 'relay', info: '[13.11. 21:42] x' }).tone, 'alarm');
  assert.ok(L.logMatches({ event: 'key_added', label: 'Anna', key: 'ab…cd' }, 'anna'));
});
test('recipient view', () => {
  const r = L.recipientView({ id: 2, label: 'Anna', muted: true, mute_volume: 3, muted_at: 1000, mute_until: 44200 }, { enabled: false }, 2000);
  assert.equal(r.state, 'muted'); assert.equal(r.pill.text, 'Stumm'); assert.match(r.sub, /Lautstärke 3/);
  assert.equal(L.recipientView({ id: 2, label: 'A', muted: true, mute_permanent: true }, { enabled: false }, 0).pill.tone, 'crit');
  assert.equal(L.recipientView({ id: 1, label: 'J', muted: false }, { enabled: true, key_id: 2 }, 0).paused, true);
});
```
- [ ] **Step 2: Run** `node --test tests/dashboard/` → new tests FAIL.
- [ ] **Step 3: Implement** in `logic.js`.
- [ ] **Step 4: Run** → all pass.
- [ ] **Step 5: Commit** — `git commit -m "Neues Dashboard: Alarm-, Verlaufs- und Empfänger-Logik mit Tests"`

---

### Task 7: `api.js` and `store.js`

**Files:**
- Create: `nas_dashboard/assets/app/api.js`, `nas_dashboard/assets/app/store.js`
- Create: `tests/dashboard/api.test.mjs`, `tests/dashboard/store.test.mjs`

**Interfaces:**
- `FW.api`: `ApiError(kind, message, status, data)`; `TIMEOUT = {POLL: 10000, POST: 20000, SEND_ONE: 30000, ALARM: 120000}`; `request(url, opts, timeoutMs) → Promise<object>` (port of `api()` 1204–1254 incl. 401 → `onAuthLoss` callbacks, 409 `mode_changed`, 403 `readonly`/`csrf` incl. old-server fallbacks, non-JSON → `http`); `get(action, params = {}, timeoutMs = TIMEOUT.POLL)`; `post(fields, timeoutMs = TIMEOUT.POST)` (FormData + `X-CSRF-Token`; on `csrf` error triggers `onCsrfError` callbacks, never retries); `setCsrf(token)` (also updates every `form input[name="csrf"]`, e.g. the logout form), `getCsrf()`, `onAuthLoss(fn)`, `onCsrfError(fn)`; `errorText(err)`, `errorKind(err)` (ports of 1258–1276, 346–347); `makePoller(fn) → run` with `run.tick()` (port 1291–1312).
- `FW.store`:
  - `init({role, user, csrf, publicBase, muteMax})` (from meta tags), `start()`, `stop()`, `getState()`, `subscribe(fn) → unsubscribe`, `useStore(selector) → value` (Preact hook, re-renders on change of the selected value via `Object.is`), `setRoute(route)` (controls which pollers run: log only on `verlauf` or wide `uebersicht`; notify/settings only on `einstellungen/*`).
  - State fields: `role, user, authLost, status, statusData (last ok status response), statusError, stale, lastOkAt, serverOffset, demo, nasKeysVersion, demoSyncOk, demoSyncFor, deviceHint, mismatchSince, keys, keysCorrupt, alarmInfo, log {filter, limit, entries, more, error, query}, notify, settings, busyCount, alarmInFlight, keyTestInFlight, probeInFlight, unreached, result {phase: 'sending'|'done'|'note', tone: ''|'ok'|'warn'|'fail', head, failed, reached, offerRetry}|null, confirm (see below)|null, nowMs`.
  - Derived getter `derive() → {verdict, hints, overviewRows, boxRows, keysSummary, alarmFact, commandLine}` (pure, calls `FW.logic`).
  - Confirm: `openConfirm({title, text, confirmLabel, hold: boolean, expectDemo: string|null, onConfirm})` (computes `deadline = Date.now() + armTimeoutFor(text)*1000`), `closeConfirm()`, `confirmNow()` (no-op and close if past deadline). Closed automatically on `visibilitychange` hidden and when `expectDemoOf(demo) !== confirm.expectDemo` (then `result = note` with „Der Demo-/LIVE-Modus hat sich gerade geändert – Bestätigung zurückgesetzt. …“).
  - Actions (each returns `Promise<{ok: boolean, text: string, kind: 'ok'|'error'|'warn'}>` and refreshes what the old code refreshed): `enqueueTest()`, `enqueueBoxAlarm(expect)`, `cancelCommand()`, `sendNasAlarm(action, expect, onlyIds = null, origAt = 0)` (port 1845–1900 using `logic.buildUnreached`, persists `unreached` in `localStorage` key `fwAlarm.lastUnreached`), `clearResult()`, `demoSet(enabled, id)`, `keyAdd(label, key, phone)`, `keyDelete(id)`, `keyMute(id, muted)`, `keyPermanent(id, on)`, `keyVolume(id, vol)`, `keyTest(id)`, `keyRename(id, label)`, `keyReplace(id, key)`, `keyPhone(id, phone)`, `notifySet(type, key, value)`, `notifyTest(type)`, `settingsSet(key, value)`, `setLogFilter(f)`, `setLogQuery(q)`, `loadMoreLog()`, `refreshAll()`.
  - Visibility: hidden → `stop()` + `closeConfirm()` + reset mismatch; visible → `start()`. If `device.browserAlert` is on, hidden keeps only status polling at 60 s and calls `FW.store.onTransition(fn)` subscribers with `{from, to}` verdict keys.
  - `nowMs` ticks every 1 s (and drives „stale“ after 15 000 ms).
  - Status handling ports `refresh()` (1324–1439): CSRF/timezone/demo/version/`demo_sync_ok`/`device_hint` adoption, server offset, mismatch measurement per version pair, and „new alarm“ detection (`last_alarm|alarm_pending|alarm_seq` changed → refresh alarm info + log at once). Auth loss ports `handleAuthLoss` (145–165): stop polling, `authLost = true`, reload after 1 500 ms unless `alarmInFlight`.

- [ ] **Step 1: Write the failing tests** — `api.test.mjs` loads `logic.js` + `api.js` with stubbed `fetch`/`AbortSignal`/`setTimeout`/`FormData`:
```js
test('401 → auth + callback', async () => { /* stub fetch → status 401 */ await assert.rejects(api.get('status'), (e) => e.kind === 'auth'); assert.equal(lost, 1); });
test('409 mode_changed keeps data', async () => { /* {code:'mode_changed', demo:{…}} */ await assert.rejects(api.post({action:'alarm'}), (e) => e.kind === 'mode_changed' && e.data.demo); });
test('403 old server without code but message → readonly', …);
test('403 with code csrf → csrf + onCsrfError, no retry', …);   // fetch called exactly once
test('500 html → http with short text', …);
test('abort → timeout', …);
test('poller: tick skipped while busy, explicit run re-runs once', async () => { /* count calls */ });
```
`store.test.mjs` loads vendor bundle is NOT needed: load `logic.js`, `api.js`, `store.js` with `globals = { htmPreact: { useState(){}, useEffect(){}, useRef(){} }, localStorage: memoryStorage(), document: stubDocument(), fetch: router }`, and assert:
```js
test('confirm closes on mode change and sends nothing', …);   // openConfirm(expectDemo '0'), status poll returns demo enabled → confirm null, result.phase 'note'
test('confirmNow after deadline does not fire', …);           // fake Date.now
test('sendNasAlarm network error → unclear, no retry offer', …);
test('sendNasAlarm 401 while in flight → no reload, result kept', …);  // location.reload stub not called
test('unreached persisted in localStorage and restored', …);
```
- [ ] **Step 2: Run** `node --test tests/dashboard/` → FAIL.
- [ ] **Step 3: Implement** both files.
- [ ] **Step 4: Run** → pass.
- [ ] **Step 5: Commit** — `git commit -m "Neues Dashboard: Anfragen, Poller und zentraler Zustand mit Tests"`

---

### Task 8: App shell, styles, shared components, router (`next.php`)

**Files:**
- Create: `nas_dashboard/lib/shell.php`, `nas_dashboard/next.php`, `nas_dashboard/assets/app.css`, `nas_dashboard/assets/app/ui.js`, `nas_dashboard/assets/app/main.js`
- Create: stub views `view-overview.js`, `view-recipients.js`, `view-log.js`, `view-settings.js` (each renders its large title only)
- Create: `tests/dashboard/e2e/scenarios/shell.mjs`

**Interfaces:**
- `shell.php`: `app_script_paths(): array` (order from File Structure), `asset_url(string $rel): string` (`$rel . '?v=' . (int)@filemtime(...)`), `render_app_page(array $config, string $csrf, string $role, string $user): void` — full `<!doctype html>` with `<meta charset>`, viewport `width=device-width, initial-scale=1, viewport-fit=cover`, metas `csrf`, `role`, `user`, `public-base`, `mute-max` (`mute_max_seconds($config)`), `theme-color #000000`, `color-scheme dark light`, favicon/apple-touch-icon, `app.css`, `<body class="app">`, `<noscript>` text, `<div id="app"></div>`, hidden logout `<form id="logout-form" method="post" action="index.php">` with `action=logout` + `csrf`, and the script tags (`defer`).
- `next.php`: `require lib/common.php` + `lib/shell.php`; `load_config()`; `start_dashboard_session()`; not authed → `header('Location: index.php')`; else `render_app_page(...)`.
- `FW.ui` components (all `html`-tagged, props as listed): `Icon({name})` (inline SVG set: `overview, people, list, settings, plus, chevron, back, check, alert, x, phone, copy, bell, moon, demo, box, search, mute, unmute, restart, watch, edit, test`), `Group({header?, footer?, children})`, `Row({label, value?, tone?, href?, onClick?, sub?, right?, children?})`, `Pill({tone, children})`, `Switch({checked, onChange, label, disabled?})` (`role=switch`), `Segmented({options: [{value, label}], value, onChange, label, disabled?})`, `Slider({min, max, value, onCommit, label, disabled?})` (local value while dragging; `onCommit` on release/keyboard change), `Sheet({open, onClose, title, children, variant: 'sheet'|'dialog'|'panel'})` (focus trap, Escape, restores focus, bottom sheet `< 900px`, centered dialog `≥ 900px`), `HoldButton({label, durationMs = 1500, onComplete, disabled})` (pointer + Space/Enter hold, progress via CSS var `--p`, `aria-describedby` „Zum Senden gedrückt halten“), `ConfirmHost()` (renders `store.confirm` as Sheet; hold or two-button; focus starts on „Abbrechen“; countdown text), `Msg({msg: {text, kind}|null})` (✓ ✕ ! + color; `ok` disappears after `Math.max(8000, words*450)` ms), `announce(text)` (hidden live region), `Toast` not needed.
- `main.js`: `parseRoute(hash) → {path: string[], query: Record<string,string>}`, `navigate(hash)`, `App()` layout: `≥ 900px` sidebar (brand + lamp, nav with badge, footer user · Abmelden · „Status vor N s“), else tab bar; demo band when `demo.enabled`; document title `<icon> Alarm-Wächter` by verdict tone (🟢 ok, 🟡 warn, 🔴 crit/alarm, 🔒 auth); badge = hint count or `!` when verdict tone ≠ ok; applies theme (`localStorage fwAlarm.theme` = `dark|light|system`, default `dark`) as `data-theme` on `<html>`; starts store; mounts with `htmPreact.render`.
- `app.css`: tokens (Global Constraints), `[data-theme=light]` and `@media (prefers-color-scheme: light)` for `system`, base layout (safe areas), large title, group/row/separator, pill, switch, segmented, slider, sheet/dialog/panel, hold button, tab bar, sidebar, demo band, msg, focus ring (`--info`), `prefers-reduced-motion`.

- [ ] **Step 1: Write the failing e2e** — `shell.mjs`: login, `goto(base + '/next.php')`; at 390×844 mobile assert `.tabbar` visible with 4 items and no `.sidebar`; click 2nd tab → `location.hash === '#/empfaenger'` and an `h1` „Empfänger“; at 1280×800 assert `.sidebar` visible and no `.tabbar`; `document.documentElement.scrollWidth <= innerWidth`; no console errors (collect via `Runtime.consoleAPICalled`/`Runtime.exceptionThrown`); `document.title` starts with 🟢 after one status poll; switch theme by setting localStorage `light` and reload → `html[data-theme=light]`. Screenshots both widths.
- [ ] **Step 2: Run** → FAIL (404 `next.php`).
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** setup + `run.mjs shell` → exit 0; `node --test tests/dashboard/` still passes; check the screenshots visually against spec §5/§6.1.
- [ ] **Step 5: Commit** — `git commit -m "Neues Dashboard: App-Gerüst, Navigation, Grundbausteine (unter next.php)"`

---

### Task 9: Übersicht

**Files:**
- Modify: `nas_dashboard/assets/app/view-overview.js`, `nas_dashboard/assets/app.css`
- Create: `tests/dashboard/e2e/scenarios/overview.mjs`

**Interfaces:**
- `FW.views.Overview()` — order per spec §6.2: status card (verdict icon ✓/!/✕, title, sub, „Status vor N s“ or the problem line from `showStatusProblem`), when `raiseActions` the Auslösen group directly after the card, hints list (each row tone-colored), checklist (`overviewRows`, rows with `href` navigate), Auslösen group (admin only; labels from §6.2 incl. demo variants; „Demo-Alarm über die Box“ only in demo mode), command line (`commandLine`, „Abbrechen“ when `canCancel`), result card (`store.result` + `unreached`: failed first with reason and `tel:` link when the recipient has `phone`, resend `HoldButton` via confirm, reached list, „Ausblenden“), readonly sentence instead of Auslösen. Wide layout: right column „Letzte Ereignisse“ (5 newest from `log_view` filter `all`, „Ganzen Verlauf anzeigen“) and „Stumm geschaltet“.
- Buttons open `store.openConfirm` with `logic.alarmQuestion(...)`, `hold: true`, `expectDemo = logic.expectDemoOf(snapshot)`; TEST runs directly; resend uses `alarmQuestion('resend', …)`.

- [ ] **Step 1: Write the failing e2e** — `overview.mjs` (sandbox with 3 keys: `josh0000001`, `fail0000001` named Tom with phone `+49 171 1`, `anna0000001` muted vol 3). Every box push in scenarios goes through a helper `pushBox(overrides)` that passes `keys_version`/`keys_count`/`keys_muted` from the current `keys_list` (otherwise a list-mismatch hint appears after 60 s). Steps:
  - **ready**: `box.sh` fresh → card title „Alles bereit“, checklist rows box/wifi/watch/keys/alarm, title 🟢 (watchdog not run in sandbox → expect hint about the Offline-Wächter → title „Bereit – 1 Hinweis“; then run `cron/check_offline.php` once → „Alles bereit“).
  - **hold to send**: click „Alarm an alle senden“ → confirm sheet text contains `3 Empfänger` and `Anna (stumm) mit Lautstärke 3`; a short press (pointerdown/up after 300 ms) sends nothing (`bark.log` unchanged); holding 1 600 ms sends; result card shows „2 von 3 erreicht“, Tom first with `a[href^="tel:"]`, „Nur an Tom erneut senden“; `bark.log` has `level=critical` for all three, `volume=3` for Anna.
  - **resend**: hold resend → `bark.log` gains exactly one request (key `fail0000001`, body contains `verspaetet zugestellt`); Tom still listed („Weiterhin NICHT erreicht“).
  - **reload keeps unreached**: reload page → result card restored from `localStorage`.
  - **visibility closes confirm**: open alarm confirm, run `Object.defineProperty(document,'hidden',{value:true,configurable:true}); document.dispatchEvent(new Event('visibilitychange'))` → confirm gone; restore visible; nothing sent.
  - **mode change while armed**: open alarm confirm, enable demo via a second `apiSession` → within 7 s confirm gone and note „Demo-/LIVE-Modus hat sich gerade geändert“ visible; nothing sent.
  - **409**: open confirm with LIVE snapshot, switch demo via API, immediately complete hold (before next poll) → response 409, card text contains „NICHT gesendet“, `bark.log` unchanged, demo band visible.
  - **box offline / small screen offline**: viewport 320×568, set `latest_status.json.php` `seen_at` 10 min back (helper in `box.sh`: `box.sh "$S" --age 600`) → title 🔴, card „Box offline“, alarm button `getBoundingClientRect().bottom <= innerHeight`, `scrollWidth <= innerWidth`, WLAN row tone `unknown`.
  - **NAS unreachable**: stop the PHP site server → within 16 s card „NAS nicht erreichbar“ (red because last known offline in previous step, orange otherwise — test both orders), restart server.
  - **alarm states**: `box.sh alarm_pending=1 key_progress=1/3` → „Alarm läuft“; `relay=closed alarm_pending=0 key_progress=3/3 last_alarm=x` → „Alarm ausgelöst“, actions raised; `stuck_warned=1 relay=closed` → „Kontakt klemmt“.
  - **TEST command**: click Test → command line „wartet auf die Box“ with „Abbrechen“; cancel → „abgebrochen“.
  - **readonly**: login `leser` → no Auslösen group, readonly sentence present.
  - Screenshots for each state at 390×844 and the ready state at 1280×800.
- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `run.mjs overview` → exit 0; `node --test tests/dashboard/` passes; inspect screenshots.
- [ ] **Step 5: Commit** — `git commit -m "Neues Dashboard: Übersicht mit Halten-zum-Senden und Ergebnis-Karte"`

---

### Task 10: Empfänger

**Files:**
- Modify: `nas_dashboard/assets/app/view-recipients.js`, `nas_dashboard/assets/app.css`
- Create: `tests/dashboard/e2e/scenarios/recipients.mjs`

**Interfaces:**
- `FW.views.Recipients({id})` — route `#/empfaenger` and `#/empfaenger/<id>`; header with „+“ (wide: „+ Hinzufügen“); sync line (green „Box ist auf dem neuesten Stand“ / orange „Box hat die Änderung noch nicht übernommen (seit …)“ after 60 s, from `mismatchSince`); demo note; list rows from `logic.recipientView`; detail = `Sheet` (`< 900px`) or right `panel` (`≥ 900px`) with: Segmented Laut/Stumm/Dauerhaft stumm (rules spec §6.3, questions from `logic.muteQuestion`, confirm `hold: false`), Slider 0–10 (`onCommit` → `keyVolume`; hint „gilt, wenn stumm“ when loud), Telefon row (edit inline, `type=tel`, save on blur/Enter), Kurzbefehl-Links (copy; fallback shows URL; base = `public-base` meta, port of `muteLinkBase` 2121–2134), Test-Push (confirm text from 2227), Umbenennen (inline field), Key ersetzen (field + confirm „Der alte Key bekommt danach keine Alarme mehr.“), Löschen (confirm, red); per-control `Msg`; readonly: no controls, no links/phone. Add sheet: Name (≤ 40), Key (`pattern [A-Za-z0-9_\-]{5,64}`, live hint text from today's `title`), Telefon optional; after success offer „Test-Push senden“. Footer „Mehr erfahren“ sheet with the explanation texts (index.php 341–363, duration from `keys.mute_max_seconds`).

- [ ] **Step 1: Write the failing e2e** — `recipients.mjs`:
  - add „Lena“ with key `lena0000001` and phone → appears in list with pill „Laut“; offered Test-Push → confirm → `bark.log` gains one request `level=active` for `lena0000001`.
  - open Lena → Stumm → confirm text contains „OHNE Ton“ → pill „Stumm“, sub contains „wird um“; slider to 4 (keyboard ArrowRight ×4 then commit) → `keys_list` `mute_volume === 4`; Dauerhaft stumm → pill „Dauerhaft“; Laut → no confirm, pill „Laut“.
  - **live refresh keeps sheet state**: open Lena's sheet, focus the rename field and type „Le“ without saving, start a slider drag (pointerdown, move, no up), change Anna from another `apiSession`, run `FW.store.refreshAll()` via eval and wait 11 s (≥ two status polls) → sheet still open, field still focused with „Le“, slider value still the dragged one; release → saved.
  - rename → list shows new name; replace key → confirm → `keys_list.version` increased; copy link → clipboard permission denied in headless → fallback message with URL containing `api/mute.php?t=` and `state=on`.
  - delete with confirm → gone; deleting the last one → error message shown in the sheet.
  - **ten recipients narrow**: add up to 10 with 40-char names at 320×568 → `scrollWidth <= innerWidth`, every pill visible (`getBoundingClientRect().right <= innerWidth`).
  - wide 1280×800: clicking rows switches the right panel without closing; screenshot.
  - readonly: no „+“, no segmented, no phone text in DOM.
- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `run.mjs recipients` → exit 0; inspect screenshots.
- [ ] **Step 5: Commit** — `git commit -m "Neues Dashboard: Empfänger mit Sheet/Panel, Regler, Umbenennen, Key ersetzen, Telefon"`

---

### Task 11: Verlauf

**Files:**
- Modify: `nas_dashboard/assets/app/view-log.js`, `nas_dashboard/assets/app.css`
- Create: `tests/dashboard/e2e/scenarios/log.mjs`

**Interfaces:**
- `FW.views.Log({query})` — Segmented filter (`?filter=` from route preselects), search field (`store.setLogQuery`, filters `log.entries` with `logic.logMatches`, hint „durchsucht die geladenen Einträge“), day sections (`fmtDayLabel`), rows with icon tile colored by `tone` (`alarm` red, `warn` orange, `ok` green, else grey), text, time (`<time datetime>`), „Ältere laden“ while `log.more` and `limit < 100`, empty texts `LOG_EMPTY`, readonly sees no `notify_changed`/`setting_changed` (server already filters).

- [ ] **Step 1: Write the failing e2e** — generate events (add/mute/rename keys, TEST command, a REAL ALARM) then: filter „Alarme“ shows only alarm events; search „lena“ narrows to Lena's events; „Ältere laden“ appears after > 25 events and loads more; `#/verlauf?filter=alarm` preselects „Alarme“; screenshot both widths.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** → exit 0.
- [ ] **Step 5: Commit** — `git commit -m "Neues Dashboard: Verlauf mit Filter und Suche"`

---

### Task 12: Einstellungen

**Files:**
- Modify: `nas_dashboard/assets/app/view-settings.js`, `nas_dashboard/assets/app/store.js` (browser alert transitions if not done), `nas_dashboard/assets/app.css`
- Create: `tests/dashboard/e2e/scenarios/settings.mjs`

**Interfaces:**
- `FW.views.Settings({path})` — routes: `einstellungen` (main list §6.5), `einstellungen/meldungen` (Sicherheit = `important`, Info = rest; `Switch` per type; wichtige abschalten → confirm text from `app.js` 2400; „Nicht konfiguriert“ note when `!notify.configured`), `einstellungen/meldungen/<type>` (hint, every option: `Segmented` when ≤ 4 choices else `<select>`, „Probe senden“ with `probeInFlight` lock), `einstellungen/allgemein` (global options + „Gerade ist Ruhezeit“; the main list's „Ruhezeit“ row links here and shows the current quiet-time choice text), `einstellungen/demo` (state, sync sentence from `renderDemo` 1013–1031, target select, toggle with confirm texts 1146–1161), `einstellungen/stumm` („Automatisch wieder laut nach“ choices from `settings_list`, note „Gilt auch für laufende Stummschaltungen.“), `einstellungen/box` (`logic.boxRows` grouped; tap row toggles its `tip`; stale note; footnote Probealarm-Fenster text from index.php 306–313), device section (Design Segmented Dunkel/Hell/System → `localStorage fwAlarm.theme`; „Hinweis im Browser bei Störung“ Switch only if `'Notification' in window` and not iOS standalone (`navigator.standalone === true` hides it) → requests permission, stores `fwAlarm.browserAlert=1`), Konto (role text, Abmelden submits `#logout-form`, „Alte Ansicht öffnen“ → `classic.php`). Readonly: only Box, device, Konto.
- Browser alert: `store` notifies on verdict transitions into `offline`, `alarm`, `standing`, `nas` (only after 3 consecutive failed polls), and hint „Zustellung“ (red delivery hint); one `new Notification('Alarm-Wächter', {body, tag: 'fw-alarm'})` per transition, plus one „wieder in Ordnung“ when returning to `ok`/`hints`; click focuses the window.

- [ ] **Step 1: Write the failing e2e** — toggle „Box neu gestartet“ off/on (→ `notify_list` reflects), switch off „Box offline“ → confirm required, cancel keeps it on; change Dringlichkeit of a type → saved; „Probe senden“ → `bark.log` has one request to `statuskey0001`; set auto-unmute to 4 Std → `keys_list.mute_max_seconds === 14400` and a muted recipient's sub shows the new time; demo on (select Anna, confirm) → demo band on every tab, alarm button label „Demo-Alarm an Anna“; demo off; Box page shows groups and a tip expands on tap; theme Hell → `data-theme=light` persists over reload; browser alert: grant permission via CDP `Browser.grantPermissions`, enable, hide tab, make box offline → a `Notification` is constructed (stub `window.Notification` via `Page.addScriptToEvaluateOnNewDocument` to record calls); readonly sees only Box/Gerät/Konto; screenshots.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** → exit 0.
- [ ] **Step 5: Commit** — `git commit -m "Neues Dashboard: Einstellungen, Meldungen, Demo, Box-Details, Gerät"`

---

### Task 13: Switch-over, login page, home-screen mode, classic fallback, docs

**Files:**
- Create: `nas_dashboard/classic.php`, `nas_dashboard/assets/classic/app.js`, `nas_dashboard/assets/classic/style.css` (copies of today's files; `classic.php` = today's `index.php` minus the login/logout handling: not authed → redirect `index.php`; logout form `action="index.php"`; asset paths `assets/classic/…`)
- Modify: `nas_dashboard/index.php` (login/logout PHP unchanged except `refresh_dashboard_cookie()` after `session_regenerate_id`; authed → `render_app_page(...)`; login page new markup with ids `user`/`password`, classes from `app.css`, `<link rel="manifest">`)
- Modify: `nas_dashboard/lib/shell.php` (add manifest link, `apple-mobile-web-app-capable`, `apple-mobile-web-app-status-bar-style black-translucent`, `apple-mobile-web-app-title Alarm-Wächter`)
- Create: `nas_dashboard/manifest.json` (`name`/`short_name` „Alarm-Wächter“, `start_url: "./index.php"`, `scope: "./"`, `display: "standalone"`, `background_color`/`theme_color` `#000000`, icons 180/192/512), `nas_dashboard/assets/icons/icon-192.png`, `icon-512.png` (render `assets/favicon.svg` on black via headless Chrome screenshot)
- Delete: `nas_dashboard/next.php`, `nas_dashboard/assets/app.js`, `nas_dashboard/assets/style.css`
- Modify: `CLAUDE.md` (frontend description: new structure, verdict order, hints, confirmations incl. hold, polling, Preact vendor, file list, „Schwellen doppelt“ now `assets/app/logic.js` ↔ `cron/check_offline.php`, tests + sandbox, `classic.php` temporary), `nas_dashboard/SETUP.md` (home screen, `classic.php`, `app_settings.json`), `cron/check_offline.php` comment pointing to the new file for the doubled thresholds
- Modify: all e2e scenarios: `next.php` → `index.php`

- [ ] **Step 1: Write the failing e2e** — `switch.mjs`: `GET /index.php` unauthenticated shows the new login (`.login-card`), wrong password → error text, username kept, focus on password; correct → new app (`#app .tabbar` at 390 px); `/classic.php` authed renders the old UI (`[data-state]` present) and its REAL ALARM still works against the mock; `/classic.php` unauthenticated redirects to `index.php`; `/next.php` 404; `manifest.json` served and parseable with `display === 'standalone'`; logout from Einstellungen returns to the login page and the cookie is cleared.
- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Verify everything**
  Run: `node --test tests/dashboard/` → all pass; `php -l` on every changed PHP file → no errors; setup + every scenario (`smoke api-settings api-keys api-session shell overview recipients log settings switch`) → exit 0; review all screenshots at 320, 390 and 1280 px in dark and light.
- [ ] **Step 5: Commit** — `git commit -m "Dashboard: neue Oberfläche live geschaltet, alte als classic.php"`

---

## Self-review notes (done while writing)

- Spec coverage: §5 → Tasks 8–12 (+ global constraints); §6.1 → 8; §6.2 → 5, 9; §6.3 → 6, 10; §6.4 → 6, 11; §6.5 → 12; §6.6/6.7 → 4, 13; §7 → 5–7, 9–12; §8.1–8.3 → 3; §8.4 → 2, 12; §8.5 → 8, 11, 12; §8.6 → 12; §9 → File Structure, 1, 13. Pull-to-refresh (§6.7, optional) is deliberately not planned.
- Deploy to the NAS is not part of this plan (spec §9.6: only after the user's go).
