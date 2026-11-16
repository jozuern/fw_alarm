# Oracle Move Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the dashboard (unchanged PHP app + all runtime data) from the Synology NAS to the Oracle VM `fw-alarm-watch`, served at `https://alarm.tunero.dev/` through a Cloudflare Tunnel, with the NAS reduced to a data-free bridge (box calls + old mute links) and swapped into the role of outside watcher — without ever sending an alarm by accident.

**Architecture:** Nginx (loopback only) + PHP-FPM pool `fw-alarm` as user `fwalarm` on the VM; `cloudflared` (locally managed tunnel) is the only way in. A new `nas_bridge/` (plain PHP + cURL, no data, no secrets) replaces the dashboard files on the NAS and forwards exactly five endpoints server-side to the VM until the box is reflashed (~mid-December 2026). The existing `cloud_watcher/fw-alarm-watch.sh` moves from the VM to a DSM scheduled task on the NAS.

**Tech Stack:** Ubuntu 24.04 aarch64, nginx, PHP 8.3 FPM/CLI, cloudflared, systemd timers, Bash + curl, Node 24 `node --test`, existing sandbox/e2e harness under `tests/dashboard/`.

**Spec:** `docs/superpowers/specs/2026-11-16-oracle-move-design.md` (German; § numbers below refer to it). Passkey login is a separate plan: `docs/superpowers/plans/2026-11-16-passkey-login.md`, executed after this one.

## Global Constraints

- **No accidental alarm (spec §10):** staging never holds real Bark keys or the real `bark_host`; on the live system after cutover never trigger REAL ALARM, ENTWARNUNG, DEMO-ALARM or `keys_test`. Live checks use only status pushes, the TEST command, HEAD/preview mute calls and the user's OWN mute link.
- The cutover deploys exactly the app code that runs on the NAS today (`bf1a7c8` app files = `HEAD` of `nas_dashboard/` at plan time); host-neutral wording (Task 12) ships only after the cutover is stable.
- Firmware (`boss925_alarm_bark/`, `platformio.ini`) is not touched; the user edits `config.h` and flashes.
- Secrets (live `config.php`, data files, staging credentials, watcher conf) only ever live in the session scratchpad, `/tmp` on the VM for seconds, or their final place; never in git, never printed. Delete scratch copies after use.
- VM paths: webroot `/srv/fw-alarm/app`, data `/srv/fw-alarm/data`, sessions `/srv/fw-alarm/sessions`, PHP log `/var/log/fw-alarm/php.log`, user/group `fwalarm`, FPM socket `/run/php/fw-alarm.sock`, nginx `127.0.0.1:8080`.
- Deploy excludes `config.php`, `SETUP.md`, `config.example.php`, `data/` (as on the NAS).
- SSH: `ssh -i ~/.ssh/oci_fw_alarm ubuntu@<IP der VM>` (IP is ephemeral — check the Oracle console if it stops answering).
- Bridge timeouts: connect 3 s, total 3.5 s; request body ≤ 16 384 bytes; never answer 403 itself.
- Cloudflare: Bot Fight Mode off for the zone; min TLS 1.2; the apex `tunero.dev` (GitHub Pages) and its DNS records stay untouched.
- Oracle stays Always Free (no Pay-As-You-Go, ≤ 2 OCPU / 12 GB A1 in total).
- Files under `server/`, `nas_bridge/`, `cloud_watcher/` that run on Linux/DSM are LF (extend `.gitattributes`).
- Commits directly on `main`, German messages with umlauts, ending with the `Co-Authored-By:`/`Claude-Session:` lines from the session reminder. Push before every deploy.
- Unit tests: `node --test "tests/dashboard/*.test.mjs"` and (new) `node --test "tests/bridge/*.test.mjs"`.

## Review Focus

1. **Box request while the bridge holds or the VM is unreachable** — box gets 503/502/504 with no `type=`/`ok=` lines and never 403 (403 would push "NAS lehnt Token ab"). Test: Task 5 `hold`/`upstream down`/`slow` cases.
2. **Old mute link opened in a browser or previewed by iMessage/WhatsApp** — browser gets the 303 → done page with working stylesheet; preview and HEAD toggle nothing. Test: Task 5 (303 relative Location, HEAD method kept) and Task 6 step "mute flows".
3. **A colleague's mute automation fires during the data copy** — the toggle must not be lost or half-applied. Test: Task 5 case `hold` (held log line), Task 6 Step 5, Task 9 A4–B5 (hold, sha256 NAS = VM, replay).
4. **Cloudflare challenging a non-browser client** (ESP32 UA, NAS curl, iOS Shortcuts without `Accept`) — must get the app's own response. Test: Task 4 `check.sh public` cases `esp32-ua`, `no-accept`, `tls12`.
5. **Login idle for more than 24 min on the VM** — must still be logged in (Ubuntu's session cleaner must not apply). Test: Task 3 case `session-dir` (run in Task 4 Step 3), and Task 9 C7 "still logged in after the night".

---

## File Structure

| Path | Responsibility |
|---|---|
| `server/README.md` | Runbook (German): layout, setup, deploy, checks, operation, rollback |
| `server/setup.sh` | Idempotent VM setup as root: packages, user, dirs, config files, services |
| `server/nginx/fw-alarm.conf` | nginx server block (127.0.0.1:8080, deny rules, log format without query) |
| `server/php/fw-alarm.conf` | PHP-FPM pool `fw-alarm` |
| `server/systemd/fw-alarm-cron.service`, `fw-alarm-cron.timer` | Watchdog `cron/check_offline.php` every 15 min as `fwalarm` |
| `server/logrotate/fw-alarm` | Rotation of `/var/log/fw-alarm/*.log` |
| `server/cloudflared/config.yml.example` | Tunnel ingress (UUID placeholder) |
| `server/deploy.sh` | From the PC: `git archive <rev>` → VM → rsync into webroot → FPM reload → hash check |
| `server/check.sh` | Stack checks: `check.sh local` (on the VM) / `check.sh public <base>` (from anywhere) |
| `nas_bridge/lib/bridge.php` | `bridge_config()`, `bridge_forward()` |
| `nas_bridge/api/{status,poll,ack,keys,mute}.php` | One-line forwarders |
| `nas_bridge/index.php`, `classic.php`, `dashboard_api.php` | Moved/hold pages |
| `nas_bridge/bridge_config.example.php` | `target`, `public_url`, `hold` (real file gitignored, lives on the NAS) |
| `nas_bridge/README.md` | What the bridge is, deploy order, removal after the flash |
| `tests/bridge/upstream.php`, `tests/bridge/bridge.test.mjs` | Self-contained bridge tests (two `php -S` servers) |
| `cloud_watcher/fw-alarm-watch.sh`, `fw-alarm-watch.conf.example`, `README.md` | + `TARGET_NAME`, DSM operation |

---

### Task 1: Shrink the VM so Oracle never sees it as idle

**Files:** none (Oracle console via Claude in Chrome; work from screenshots — forms live in iframes).

- [ ] **Step 1: Back up the boot volume** — Compute → Instances → `fw-alarm-watch` → Boot volume → Create manual backup `pre-move-2026-11`. Expected: backup state AVAILABLE (Always Free covers 5 backups).
- [ ] **Step 2: Edit shape** to `VM.Standard.A1.Flex` 1 OCPU / **2 GB**, save (instance reboots). Expected: instance RUNNING, shape shows 1 OCPU / 2 GB.
- [ ] **Step 3: Verify on the VM**

Run: `ssh -i ~/.ssh/oci_fw_alarm ubuntu@<IP der VM> 'free -m | head -2; systemctl is-active fw-alarm-watch.timer; journalctl -u fw-alarm-watch.service -n 2 --no-pager'`
Expected: total ≈ 1900–2000 MB, used ≥ 20 % of total, `active`, a fresh `OK http=200` line within 10 min. If the public IP changed, update the memory note `oracle-outside-watcher-vm`.

---

### Task 2: Server config files and idempotent setup

**Files:**
- Create: `server/setup.sh`, `server/nginx/fw-alarm.conf`, `server/php/fw-alarm.conf`, `server/systemd/fw-alarm-cron.service`, `server/systemd/fw-alarm-cron.timer`, `server/logrotate/fw-alarm`, `server/cloudflared/config.yml.example`
- Modify: `.gitattributes` (LF for `server/**`, `nas_bridge/**`)

**Interfaces:**
- Produces: the VM layout from Global Constraints; services `nginx`, `php8.3-fpm` (pool `fw-alarm`), `fw-alarm-cron.timer` (installed, **disabled** until cutover), cloudflared repo configured (package installed, service not yet).

- [ ] **Step 1: Write `server/nginx/fw-alarm.conf`** — `listen 127.0.0.1:8080;` root `/srv/fw-alarm/app`; `index index.php`; `client_max_body_size 64k`; `return 404` for `^/(config\.php|config\.example\.php|SETUP\.md)$` and prefixes `/data/`, `/lib/`, `/cron/`; PHP via `fastcgi_pass unix:/run/php/fw-alarm.sock` with `try_files $uri =404`, `SCRIPT_FILENAME $document_root$fastcgi_script_name`, `fastcgi_read_timeout 130s`; `log_format fwalarm '$time_iso8601 "$request_method $uri" $status $body_bytes_sent rt=$request_time bridge=$http_x_fw_bridge "$http_user_agent"';` (never `$request`/`$args` — mute tokens are in the query); `server_tokens off`.
- [ ] **Step 2: Write `server/php/fw-alarm.conf`** — `[fw-alarm]`, `user/group = fwalarm`, `listen = /run/php/fw-alarm.sock`, `listen.owner/group = www-data`, `listen.mode = 0660`, `pm = ondemand`, `pm.max_children = 8`, `pm.process_idle_timeout = 60s`, `request_terminate_timeout = 180s`, `php_admin_value[session.save_path] = /srv/fw-alarm/sessions`, `session.gc_maxlifetime = 43200`, `session.gc_probability = 1`, `session.gc_divisor = 100`, `error_log = /var/log/fw-alarm/php.log`, `log_errors = on`, `expose_php = off`.
- [ ] **Step 3: Write the systemd units** — service `Type=oneshot`, `User=fwalarm`, `ExecStart=/usr/bin/php /srv/fw-alarm/app/cron/check_offline.php`, `Nice=10`; timer `OnCalendar=*:0/15`, `Persistent=true`, `RandomizedDelaySec=30`.
- [ ] **Step 4: Write `server/logrotate/fw-alarm`** (weekly, rotate 8, compress, `su fwalarm fwalarm`) and `server/cloudflared/config.yml.example` (`tunnel: <UUID>`, `credentials-file: /etc/cloudflared/<UUID>.json`, ingress `alarm.tunero.dev` → `http://127.0.0.1:8080`, catch-all `http_status:404`).
- [ ] **Step 5: Write `server/setup.sh`** (bash, `set -euo pipefail`, must run as root, safe to run twice): apt install `nginx php8.3-fpm php8.3-cli php8.3-curl php8.3-mbstring rsync`; add Cloudflare apt repo (`pkg.cloudflare.com`, keyring in `/usr/share/keyrings/cloudflare-main.gpg`) and install `cloudflared`; create system user `fwalarm` (no home, nologin); create dirs with owners/modes (app root:root 0755, data fwalarm 0750, sessions fwalarm 0700, `/var/log/fw-alarm` fwalarm 0750); install the files from Steps 1–4 (nginx site enabled, default site disabled; pool installed, Ubuntu's `www.conf` renamed to `www.conf.disabled`); `nginx -t && systemctl reload nginx`; `php-fpm8.3 -t && systemctl restart php8.3-fpm`; `systemctl daemon-reload` (timer NOT enabled); assert `sshd -T | grep -q '^passwordauthentication no'`.
- [ ] **Step 6: Run it on the VM twice**

Run: `scp -i ~/.ssh/oci_fw_alarm -r server ubuntu@<IP der VM>:/tmp/fw-server && ssh -i ~/.ssh/oci_fw_alarm ubuntu@<IP der VM> 'sudo bash /tmp/fw-server/setup.sh && sudo bash /tmp/fw-server/setup.sh && systemctl is-active nginx php8.3-fpm && systemctl is-enabled fw-alarm-cron.timer; ss -ltn | grep -E ":8080|:80 "'`
Expected: both runs exit 0; `active active`; timer `disabled`; only `127.0.0.1:8080` listening (no `:80`).

- [ ] **Step 7: Commit** — `git add .gitattributes server && git commit -m "Server: Einrichtung der Oracle-VM (nginx, PHP-FPM, Wächter-Timer, Tunnel-Vorlage)"`

---

### Task 3: Deploy and check scripts

**Files:**
- Create: `server/deploy.sh`, `server/check.sh`

**Interfaces:**
- Produces: `bash server/deploy.sh <git-rev>` (prints `deployed <rev>: N files, 0 mismatches`); `bash server/check.sh local` (run on the VM) and `bash server/check.sh public <base-url>`; both print one `PASS <case>`/`FAIL <case>: <why>` line per case and exit non-zero on any FAIL. Optional env `CHECK_USER`/`CHECK_PASS` enables the login cases.

- [ ] **Step 1: Write `server/deploy.sh`** — `git archive <rev> nas_dashboard` piped over ssh into `/tmp/fw-deploy/<rev>`; remote `sudo rsync -rlt --delete-after --delay-updates --chmod=D755,F644 --exclude=/config.php --exclude=/SETUP.md --exclude=/config.example.php --exclude=/data/ /tmp/fw-deploy/<rev>/nas_dashboard/ /srv/fw-alarm/app/`; `sudo systemctl reload php8.3-fpm`; then compare `sha256sum` of every deployed file with the archive (computed remotely from the extracted tree) and fail on any mismatch; remove the temp tree. Refuse to run if `<rev>` is not on `origin/main` (`git branch -r --contains`).
- [ ] **Step 2: Write `server/check.sh`** with these cases:

| Case | Mode | Request | Pass when |
|---|---|---|---|
| `login-page` | both | `GET /` | 200, body has `login-card` |
| `headers` | both | `GET /` | `Content-Security-Policy` and `Cache-Control: no-store` present; public: `Strict-Transport-Security` present |
| `classic` | both | `GET /classic.php` | 302 |
| `api-401` | both | `GET /dashboard_api.php?action=status` | 401 |
| `health` | local | `GET /api/health.php` | 200, body line `ok=1` |
| `health-hidden` | public | `GET /api/health.php` | 404 |
| `status-403` | both | `POST /api/status.php` without token | 403 |
| `deny` | both | `/config.php`, `/data/index.php`, `/lib/common.php`, `/cron/check_offline.php`, `/SETUP.md` | each 404 |
| `mime` | both | `/assets/app/main.js`, `/manifest.json`, vendor bundle | `application/javascript`, `application/json`, sha256 base64 `YsT/6kAAeMDtp/Q1sicmtJYevfdMhQbxiOkr7EHYrFE=` |
| `esp32-ua` | public | `POST /api/status.php`, UA `ESP32HTTPClient`, no `Accept`, `--tls-max 1.2` | 403 from the app, no `cf-mitigated` header, body has no `cloudflare` |
| `no-accept` | public | `GET /api/mute.php?t=invalid&state=on`, UA `Kurzbefehle/1.0`, no `Accept` | app's error answer, no `cf-mitigated` |
| `tls12` | public | `curl --tlsv1.2 --tls-max 1.2 -I /` | handshake ok, 200 |
| `head-mute` | both | `HEAD /api/mute.php?t=invalid&state=on` | not 5xx, no body |
| `session-dir` | local | `stat /srv/fw-alarm/sessions`; `php-fpm8.3 -tt` pool values | owner `fwalarm`, mode 700; `session.save_path` and `gc_maxlifetime=43200` in the pool dump |
| `login` | both, if `CHECK_USER` | POST login form with `Sec-Fetch-Site: same-origin` | 302 to app, `Set-Cookie` with `HttpOnly`, `Path=/`, `Expires`; public also `Secure`; then `action=status` 200 JSON |

- [ ] **Step 3: Deploy current `HEAD` to the (still config-less) VM and run the local checks**

Run: `git push origin main && bash server/deploy.sh HEAD`
Expected: `deployed <sha>: N files, 0 mismatches`. (`check.sh local` must FAIL at this point with `Konfiguration fehlt.` — proves the checks bite; staging config follows in Task 4.)

- [ ] **Step 4: Commit** — `git commit -m "Server: Deploy- und Prüfskript für die VM"`

---

### Task 4: Staging on the VM, then Cloudflare

**Files:** none in git (staging config, mock and credentials live in the scratchpad and on the VM only).

**Interfaces:**
- Consumes: Tasks 2–3.
- Produces: `https://alarm.tunero.dev/` serving **staging** (test data, Bark mock, random credentials stored in `<scratchpad>/staging-secrets.env`: `CHECK_USER`, `CHECK_PASS`, `STAGING_BOX_TOKEN`).

- [ ] **Step 1: Staging config with sandbox credentials** — generate a `config.php` the way `tests/dashboard/sandbox/make-config.php` does (admin `admin`/`test`, reader `leser`/`test`, token `box-token`, `bark_key_status` `statuskey0001`, `bark_keys_alarm` `['startkey0001']`, `offline_after_seconds` 30) but with `data_dir` `/srv/fw-alarm/data`, `bark_host` `http://127.0.0.1:8789`, `health_check_url` `http://127.0.0.1:8080/api/health.php`, `trusted_proxies` `['127.0.0.1','::1']`, `proxy_client_ip_header` `CF-Connecting-IP`, `public_base_url` `''` (as in the sandbox, so the e2e scenarios see what they expect); install as root:fwalarm 0640.
- [ ] **Step 2: Start the Bark mock** — copy `tests/dashboard/sandbox/bark-mock.php` to `/srv/fw-alarm/test/`; `sudo systemd-run --unit fw-alarm-bark-mock -p User=fwalarm -E BARK_LOG=/srv/fw-alarm/test/bark.log php -S 127.0.0.1:8789 /srv/fw-alarm/test/bark-mock.php`.
- [ ] **Step 3: Local checks + e2e over an SSH tunnel** (no public hostname yet)

Run on the VM: `sudo bash /tmp/fw-server/check.sh local` → all PASS.
Run on the PC: `ssh -N -L 18080:127.0.0.1:8080 -i ~/.ssh/oci_fw_alarm ubuntu@<IP der VM> &` then `SANDBOX_BASE=http://127.0.0.1:18080 SANDBOX_DIR=<scratchpad>/vm-e2e node tests/dashboard/e2e/run.mjs smoke`, same for `api-keys` and `api-settings` (the scenarios that need no local sandbox files).
Expected: all three scenarios pass; `bark.log` on the VM gained the expected mock entries; `sudo grep -c 'day.app' /srv/fw-alarm/app/config.php` is 0 (the staging app cannot reach real Bark — the watcher on the same VM still does, legitimately).

- [ ] **Step 4: Cron as `fwalarm`** — `sudo -u fwalarm php /srv/fw-alarm/app/cron/check_offline.php`; expected: exit 0, `watchdog_state.json.php` gets `last_run_at`, health self-test ok (no `nas_web_failed` in `commands.log.php`).
- [ ] **Step 5: Replace sandbox credentials with random ones** — new admin/reader passwords and box token (`php -r 'echo bin2hex(random_bytes(16));'`), hashes into the staging config, plaintext into `<scratchpad>/staging-secrets.env`; set `public_base_url` to `https://alarm.tunero.dev/`. Re-run `check.sh local` with `CHECK_USER/CHECK_PASS`.
- [ ] **Step 6: Cloudflare zone settings** (Chrome, dash.cloudflare.com → `tunero.dev`): record the current DNS list and settings first (screenshot into scratchpad); Security → Bots: **Bot Fight Mode off**; SSL/TLS → Edge Certificates: Minimum TLS 1.2, Always Use HTTPS on; Rules → Configuration Rules: `http.host eq "alarm.tunero.dev"` → Browser Integrity Check off, Email Obfuscation off, Rocket Loader off, Security Level Essentially Off; Rules → Cache Rules: same expression → Bypass cache. Expected: apex/GitHub Pages records unchanged (compare with the screenshot).
- [ ] **Step 7: Tunnel** — on the VM `cloudflared tunnel login` (open the printed URL in Chrome, authorize zone `tunero.dev`), `cloudflared tunnel create fw-alarm`, `cloudflared tunnel route dns fw-alarm alarm.tunero.dev`; write `/etc/cloudflared/config.yml` from the example with the real UUID; move the credentials JSON to `/etc/cloudflared/` (root 0600); `sudo cloudflared service install`; `systemctl is-active cloudflared`. Delete `~/.cloudflared/cert.pem` afterwards (only needed for create/route; keep a note in `server/README.md` how to re-login).
- [ ] **Step 8: Public checks**

Run: `set -a; . <scratchpad>/staging-secrets.env; set +a; bash server/check.sh public https://alarm.tunero.dev`
Expected: all PASS, including `esp32-ua`, `no-accept`, `tls12`, `login` with `Secure`.

---

### Task 5: The NAS bridge (code + tests)

**Files:**
- Create: `nas_bridge/lib/bridge.php`, `nas_bridge/api/status.php`, `poll.php`, `ack.php`, `keys.php`, `mute.php`, `nas_bridge/index.php`, `classic.php`, `dashboard_api.php`, `nas_bridge/bridge_config.example.php`, `nas_bridge/README.md`, `tests/bridge/upstream.php`, `tests/bridge/bridge.test.mjs`
- Modify: `.gitignore` (`nas_bridge/bridge_config.php`)

**Interfaces:**
- Produces: `bridge_config(): array` (keys `target` string without trailing slash, `public_url` string, `held_log` absolute path of the log for held mute calls; missing file → 500 text `bridge_config fehlt`); `bridge_on_hold(): bool` (= `is_file(<bridge root>/bridge_hold.php)`; `bridge_hold.php` itself only answers 404); `bridge_forward(string $file): never` (`$file` ∈ `status.php|poll.php|ack.php|keys.php|mute.php`, anything else → 404); each `api/<file>` is exactly `require __DIR__ . '/../lib/bridge.php'; bridge_forward('<file>');`. Held-log line format (tab-separated): `<unix time>\t<t|key>\t<first 12 hex of sha256(token)>\t<on|off|?>`.

- [ ] **Step 1: Write the test upstream `tests/bridge/upstream.php`** — router script for `php -S`: appends one JSON line per hit to `$BRIDGE_UP_LOG` (method, path, query, all received headers, body); answers by query: `code=<n>` → that status; `redirect=1` → 303 `Location: mute.php?done=on` plus `Content-Security-Policy: default-src 'self'` and `Set-Cookie: x=1`; `slow=<s>` → sleep; default → 200 `text/plain` body `ok=1\nnonce=<echo of POST nonce>`.
- [ ] **Step 2: Write the failing tests `tests/bridge/bridge.test.mjs`** — `before`: copy `nas_bridge/` to a temp dir with a `bridge_config.php` (`target` `http://127.0.0.1:8791`, `public_url` `https://alarm.tunero.dev/`, `hold` false), start upstream on 8791 and the bridge on 8790 (`php -S`, with `-d extension=curl -d extension=openssl` like `sandbox/start.sh`). Tests:
  - `forwards box POST unchanged`: POST `/api/poll.php` form `token=t&nonce=abc` → 200, body `ok=1\nnonce=abc`, upstream saw POST, `Content-Type: application/x-www-form-urlencoded`, body identical, header `X-FW-Bridge: 1`.
  - `passes upstream 403 through`: `/api/status.php?code=403` → 403.
  - `keeps 303 relative Location and CSP, drops Set-Cookie`: `/api/mute.php?redirect=1` → 303, `Location: mute.php?done=on`, CSP present, no `Set-Cookie`.
  - `keeps HEAD`: HEAD `/api/mute.php?t=x` → upstream logged `HEAD`.
  - `forwards only whitelisted request headers`: send `Cookie`, `Authorization`, `X-Forwarded-For`, `Accept: text/html`, `Sec-Fetch-Dest: document`, `User-Agent: WhatsApp/2`, `Sec-Purpose: prefetch`, `Purpose: prefetch`, `X-Purpose: preview`, `X-Moz: prefetch` → upstream saw the last seven, none of the first three.
  - `rebuilds multipart as urlencoded`: POST `/api/poll.php` as `multipart/form-data` with `token=t`, `nonce=abc` (what `curl -F`/`--form-string` and some Shortcuts send) → upstream saw `Content-Type: application/x-www-form-urlencoded` and body `token=t&nonce=abc`; answer echoes `nonce=abc`.
  - `hold answers 503 without forwarding`: create `bridge_hold.php` in the temp bridge dir → POST `/api/poll.php` → 503, body without `ok=`/`type=`, upstream hit count unchanged; `/dashboard_api.php` → 503; `GET /api/mute.php?t=abc&state=off` → 503 and the held log gains exactly one line `…\tt\t<sha256('abc')[0..12]>\toff` (no `abc` in the file); delete the file → forwarding works again without restarting anything.
  - `upstream down is 502, never 403`: target port 8799 (nothing listening) → 502.
  - `slow upstream is 504 within 4 s`: `/api/poll.php?slow=6` → 504, elapsed < 4000 ms.
  - `oversized body is 413`: 16 385-byte body → 413, not forwarded.
  - `moved pages`: `GET /index.php` and `/classic.php` → 302 `Location: https://alarm.tunero.dev/`; `GET /dashboard_api.php?action=status` → 410 JSON `{"ok":false,"code":"moved",...}`.
  - `no other paths`: `GET /api/health.php` → 404, upstream not hit.
- [ ] **Step 3: Run to see them fail**

Run: `node --test "tests/bridge/*.test.mjs"`
Expected: FAIL (bridge files missing).

- [ ] **Step 4: Implement `nas_bridge/`** per spec §8 (cURL: `CURLOPT_CONNECTTIMEOUT_MS 3000`, `CURLOPT_TIMEOUT_MS 3500`, `CURLOPT_FOLLOWLOCATION false`, `CURLOPT_NOBODY` for HEAD, `CURLOPT_SSL_VERIFYPEER true`, header callback collecting the whitelisted response headers; read the body from `php://input` with a 16 384-byte cap, except `multipart/form-data` → `http_build_query($_POST)` (files dropped); timeout → 504, other cURL error → 502, both `text/plain` `bridge: Server nicht erreichbar`, details via `error_log`). Moved pages: plain HTML/JSON, `Cache-Control: no-store`, no inline script/style.
- [ ] **Step 5: Run tests to verify they pass**

Run: `node --test "tests/bridge/*.test.mjs" && node --test "tests/dashboard/*.test.mjs"`
Expected: all PASS.

- [ ] **Step 6: `nas_bridge/README.md`** (German): purpose, the file set that goes to `\\<NAS>\web\fw_alarm\`, deploy order (`bridge_hold.php` + `lib/bridge.php` + `bridge_config.php` → `api/*` → `dashboard_api.php` → later `index.php`/`classic.php`), hold = presence of `bridge_hold.php`, the held log and how to replay it, what stays on the NAS (`assets/app.css`, `assets/favicon.svg`, `assets/app/theme-boot.js`, old data dir), removal after the flash.
- [ ] **Step 7: Commit** — `git add .gitignore nas_bridge tests/bridge && git commit -m "NAS-Brücke: reicht Box-Aufrufe und alte Mute-Links an den neuen Server weiter"`

---

### Task 6: Bridge probe on the NAS against staging

**Files:** none in git. Probe folder `\\<NAS>\web\fw_alarm_bridge\` (separate from the live `fw_alarm\`).

- [ ] **Step 1: Deploy the probe** — copy `nas_bridge/` (from `git archive HEAD`) plus `nas_dashboard/assets/app.css`, `assets/favicon.svg` and `assets/app/theme-boot.js` into the probe folder; `bridge_config.php` with `target` `https://alarm.tunero.dev`, `public_url` `https://alarm.tunero.dev/`, `held_log` `/volume1/web_packages/fw_alarm_data/bridge_held_probe.log`; no `bridge_hold.php`.
- [ ] **Step 2: Box simulation through the bridge** — with `STAGING_BOX_TOKEN`: `SANDBOX_BASE=https://dein-name.synology.me/fw_alarm_bridge bash tests/dashboard/sandbox/box.sh <scratchpad>/probe token=$STAGING_BOX_TOKEN device_id=bridge-probe` (multipart, exercises the rebuild); then a poll and an ack **url-encoded like the firmware** (`curl --data-urlencode` with `token`, `device_id`, `nonce`, `last_command_id`). Expected: status answer `ok=1`; staging dashboard (logged in with `CHECK_USER`) shows `bridge-probe` fresh; poll echoes the nonce; VM nginx log shows these lines with `bridge=1`.
- [ ] **Step 3: Latency** — 20 polls each through the bridge and directly, `curl -w '%{time_appconnect} %{time_starttransfer}\n'`; server time = starttransfer − appconnect. Expected: bridge p95 ≤ 1.5 s and max ≤ 3.0 s (box waits up to 4 s). **If not met: stop and report** — fallback is to move together with the flash (spec §5).
- [ ] **Step 4: Mute flows** — as staging admin add a recipient with a fake key (`fakekey...`, goes to the mock), read its `mute_token` via `keys_list`. Through the probe bridge: (a) HEAD `state=on` → `keys_list` still loud; (b) GET with UA `WhatsApp/2.23` `state=on` → still loud; (c) GET without `Accept`, UA `Kurzbefehle/1.0`, `state=on` → body `ok=1`, `muted=1`; (d) browser-like (`Accept: text/html`, `Sec-Fetch-Dest: document`) `state=off` → 303 `Location: mute.php?done=…` → following it via the bridge gives 200 HTML whose stylesheet link resolves to `/fw_alarm_bridge/assets/app.css?v=…` → 200 `text/css`; (e) `keys_list` loud again. Delete the recipient.
- [ ] **Step 5: Hold switch on the NAS** — create `bridge_hold.php` in the probe folder → a poll gets 503 at once; a mute call lands in `bridge_held_probe.log`; delete it → the next poll is forwarded again (proves that no PHP cache on the NAS delays the switch).
- [ ] **Step 6: Remove the probe folder and `bridge_held_probe.log`** from the NAS.

---

### Task 7: Watcher texts follow the target

**Files:**
- Modify: `cloud_watcher/fw-alarm-watch.sh`, `cloud_watcher/fw-alarm-watch.conf.example`

**Interfaces:**
- Produces: conf variable `TARGET_NAME` (ASCII, 1–24 chars `[A-Za-z0-9 -]`, default `NAS`); every Bark title/body that names the watched host uses it (e.g. `"$TARGET_NAME NICHT ERREICHBAR"`, `"$TARGET_NAME-Waechter laeuft nicht"`, `"$TARGET_NAME kann Bark nicht erreichen"`). Invalid value → exit 2 like other config errors.

- [ ] **Step 1: Implement** — validation next to the existing config checks; replace the literal host names in message texts only (state keys unchanged, so an old state file keeps working).
- [ ] **Step 2: Verify**

Run: `bash -n cloud_watcher/fw-alarm-watch.sh && grep -nE '"[^"]*NAS[^"]*"' cloud_watcher/fw-alarm-watch.sh`
Expected: syntax ok; remaining `NAS` literals only in comments or the default `TARGET_NAME=NAS`. On the VM: `sudo install` the script (keep `*.bak-<commit>`), `sudo systemctl start fw-alarm-watch.service`, journal `OK http=200` (conf without `TARGET_NAME` → default, unchanged behavior).

- [ ] **Step 3: Commit** — `git commit -m "Außen-Wächter: Name des überwachten Servers einstellbar (TARGET_NAME)"`

---

### Task 8: Prepare the cutover (day before)

**Files:** none in git (scratchpad only).

- [ ] **Step 1: Agree a time slot with the user** (assign a task): outside the colleagues' commute and shift-change times (their mute automations fire then), not in the probe-alarm window (`PROBE_WEEKDAY`/`PROBE_WINDOW_*` in `config.h`), user reachable for ~45 min with iPhone and DSM access.
- [ ] **Step 2: Back up the live NAS files** — copy every file of `\\<NAS>\web\fw_alarm\` that the cutover replaces or removes (`api/{status,poll,ack,keys,mute,health}.php`, `dashboard_api.php`, `index.php`, `classic.php`, `cron/*`) byte-exact to `<scratchpad>/nas-backup/`; confirm baseline: `git hash-object --path=nas_dashboard/<f>` of each NAS file equals `git rev-parse bf1a7c8:nas_dashboard/<f>`.
- [ ] **Step 3: Build the production `config.php`** — copy the live `\\<NAS>\web\fw_alarm\config.php` to the scratchpad; a scratch PHP script loads the array, sets the five values from spec §6 (`data_dir`, `public_base_url`, `health_check_url`, `trusted_proxies`, `proxy_client_ip_header`; keep everything else), writes it with `var_export`; show the user only the list of key NAMES that changed; `scp` to the VM and install as `/srv/fw-alarm/config.prod.php` (root:fwalarm 0640); delete both scratch copies.
- [ ] **Step 4: Prepare bridge files for live** — `<scratchpad>/bridge-live/` = `nas_bridge/` from `git archive HEAD` + `bridge_config.php` (`target` `https://alarm.tunero.dev`, `public_url` `https://alarm.tunero.dev/`, `held_log` `/volume1/web_packages/fw_alarm_data/bridge_held.log`) + `bridge_hold.php`.
- [ ] **Step 5: Dry run of the copy** — list `\\<NAS>\web_packages\fw_alarm_data\` and write the copy list = all files except `*.lock.php`, `login_throttle.json.php`, `mute_throttle.json.php`, `log_cache.json.php`, `bridge_held*.log`. Show the user the list (names only).
- [ ] **Step 6: Replay helper** — scratch PHP script for the VM that reads `bridge_held.log` lines and `alarm_keys.json.php`, matches each hash prefix against `sha256(mute_token)` (for `t`) or `sha256(key)` (for `key`), and prints per recipient name, the LAST held state + time, the current state and the time of the recipient's last mute change in `commands.log.php` (no tokens printed). Replay rule for B5: apply a held state only if it is newer than that last change (a later successful retry by the colleague wins). Test it against the staging data with a hand-made log line.

---

### Task 9: Cutover

**Files:** none in git. Run top to bottom with the user present; every step has its check. **Abort** = jump to the rollback of the phase you are in.

**Phase A — freeze (nothing changes for the box yet)**
- [ ] **A1:** User logs in to the OLD dashboard and confirms: box online, no pending/unacked command, demo state noted, `keys_version` noted.
- [ ] **A2:** User disables the DSM task for `check_offline.php` (DSM → Aufgabenplaner). Check: task shows disabled.
- [ ] **A3:** Prepare the VM (still nothing live there): `sudo systemctl stop fw-alarm-bark-mock`; move `/srv/fw-alarm/data` to `/srv/fw-alarm/data.staging`; create a fresh empty `/srv/fw-alarm/data` (fwalarm 0750). The VM answers with errors from now on — nobody uses it.
- [ ] **A4:** Deploy hold to `\\<NAS>\web\fw_alarm\` in this order: `bridge_hold.php`, `lib/bridge.php`, `bridge_config.php`, `api/status.php`, `api/poll.php`, `api/ack.php`, `api/keys.php`, `api/mute.php`, `dashboard_api.php`. Check: `curl -s -o /dev/null -w '%{http_code}' -X POST https://dein-name.synology.me/fw_alarm/api/poll.php` → 503; old dashboard shows "nicht erreichbar". **The hold clock starts — keep A5–B3 short (target ≤ 5 min).**
- [ ] **A5:** Wait 20 s (in-flight requests), then copy the files from Task 8 Step 5 to `<scratchpad>/data-copy/`, record `sha256sum` of each.

*Rollback A (before B3):* redeploy `<scratchpad>/nas-backup/` files, delete `bridge_hold.php`, `lib/bridge.php` and `bridge_config.php` from the NAS, user re-enables the DSM task, replay the held mute calls (B5) against the NAS. The VM is untouched for the box.

**Phase B — switch the VM to production**
- [ ] **B1:** `scp` the copy; install files into `/srv/fw-alarm/data` as fwalarm 0640; compare `sha256sum` NAS copy = VM → zero mismatches; delete `<scratchpad>/data-copy/` and the VM temp copy.
- [ ] **B2:** `sudo install -o root -g fwalarm -m 0640 /srv/fw-alarm/config.prod.php /srv/fw-alarm/app/config.php && sudo systemctl reload php8.3-fpm`; `sudo bash /tmp/fw-server/check.sh local` → all PASS; `bash server/check.sh public https://alarm.tunero.dev` (no login) → all PASS.
- [ ] **B3:** Un-hold: delete `bridge_hold.php` on the NAS. **Hold ends.**
- [ ] **B4:** Within 2 min: dashboard (`action=status` via the user's login, or `latest_status.json.php` on the VM) shows the box with a fresh status; VM nginx log shows `poll.php` with `bridge=1` about every 10 s; the box's reported `keys_version` equals the server's (so no re-sync and no "Liste übernommen" push). **Abort if not fresh after 5 min.**
- [ ] **B5:** Replay held mute calls — read `\\<NAS>\web_packages\fw_alarm_data\bridge_held.log` (may be absent = nothing held), run the Task 8 Step 6 helper on the VM; for each recipient with a held state, apply the LAST held state through the VM's loopback (`curl -s http://127.0.0.1:8080/api/mute.php --data-urlencode t=<token from alarm_keys> --data-urlencode state=<on|off>`, run on the VM so the token never leaves it); report names + applied states to the user.
- [ ] **B6:** User logs in at `https://alarm.tunero.dev/` and confirms: recipients, names, mute states (after B5), demo state, `keys_version` as in A1; box online. Nothing is triggered.

*Rollback B (after B3):* create `bridge_hold.php` on the NAS again; copy `/srv/fw-alarm/data/*` back to `\\<NAS>\web_packages\fw_alarm_data\` (check the NAS self-test afterwards — permissions!); redeploy `<scratchpad>/nas-backup/`; delete the bridge files; user re-enables the DSM task; replay calls held during the rollback; on the VM disable `fw-alarm-cron.timer` and revert the watcher URL if C1 already ran.

**Phase C — switch on the rest**
- [ ] **C1:** On the VM set `NAS_ALIVE_URL=https://alarm.tunero.dev/api/alive.php` and `TARGET_NAME=Alarm-Server` in `/etc/fw-alarm-watch.conf` (`sudoedit`), `sudo systemctl start fw-alarm-watch.service` → journal `OK http=200`; dashboard watcher row fresh. (Before the cron, so the cron never sees a silent watcher.)
- [ ] **C2:** `sudo systemctl enable --now fw-alarm-cron.timer && sudo systemctl start fw-alarm-cron.service && journalctl -u fw-alarm-cron.service -n 20 --no-pager` → normal run, no error, no `box_offline`, no `watcher_silent`.
- [ ] **C3:** User presses **TEST** in the new dashboard → passive push arrives on the user's phone; command shows `acked`.
- [ ] **C4:** User toggles their OWN old mute link (old NAS URL) on, then off; dashboard follows both times; final state loud.
- [ ] **C5:** Deploy `index.php` and `classic.php` from `bridge-live/` to the NAS; delete `cron/` and `api/health.php` from the NAS (old entry points that could still send pushes). Check: `https://dein-name.synology.me/fw_alarm/` → 302 to `https://alarm.tunero.dev/`.
- [ ] **C6:** The C5 redirect already takes everyone on the old address to the new one, so this is not urgent: assign the user a task to re-add their Home-Bildschirm-App / bookmark from `https://alarm.tunero.dev/` and to tell the one read-only user to do the same when convenient (user's answer 2026-11-16: one read-only user, not critical).
- [ ] **C7 (next morning):** heartbeat arrived as usual; 96 cron runs in the journal without error; dashboard still logged in on the iPhone after the night; nginx log shows no 5xx bursts. Then `sudo rm -rf /srv/fw-alarm/data.staging /srv/fw-alarm/test` and delete `<scratchpad>/staging-secrets.env`.

---

### Task 10: The NAS becomes the outside watcher

**Files:** none in git (NAS folder `\\<NAS>\web_packages\fw_alarm_watch\`).

- [ ] **Step 1: Probe the DSM shell** — put a scratch `probe.sh` into the folder: every tool the script calls (`grep -oE` over `fw-alarm-watch.sh` for the list; at least `bash curl flock mktemp date sha256sum od tr head`) via `command -v`, `bash --version`, `curl --version`, and `curl --retry-all-errors -s -o /dev/null -w '%{http_code}' https://alarm.tunero.dev/` (needs curl ≥ 7.71; exit 2 = too old); user (or Claude via Chrome if DSM is open) runs it once as a DSM task (root) writing `probe.txt`. Expected: no `MISSING`, bash ≥ 4.3, the curl call prints `200`. Otherwise: stop and report.
- [ ] **Step 2: Install** — `fw-alarm-watch.sh` from `git archive HEAD` (LF), `fw-alarm-watch.conf` = the VM's conf (via scratchpad, deleted afterwards) with `NAS_ALIVE_URL=https://alarm.tunero.dev/api/alive.php` and `TARGET_NAME=Alarm-Server`; empty `state/`.
- [ ] **Step 3: DSM task** `fw-alarm-watch`, root: `FW_WATCH_CONF=/volume1/web_packages/fw_alarm_watch/fw-alarm-watch.conf STATE_DIRECTORY=/volume1/web_packages/fw_alarm_watch/state /bin/bash /volume1/web_packages/fw_alarm_watch/fw-alarm-watch.sh`. First run it once by hand (DSM "Ausführen", output to a file) → `OK http=200`; only then schedule every 5 min. Check after 10 min: `state/state` updated (SMB timestamp), DSM task result exit 0.
- [ ] **Step 4: Switch off the VM watcher** — `sudo systemctl disable --now fw-alarm-watch.timer`. Check after 15 min: dashboard watcher row still fresh (now fed by the NAS).
- [ ] **Step 5:** Delete `api/alive.php` from `\\<NAS>\web\fw_alarm\` (nobody calls the NAS's alive.php any more).

---

### Task 11: Host-neutral texts and documentation

**Files:**
- Modify: user-visible strings in `nas_dashboard/` (PHP output, `assets/app/*.js`, Bark texts in `cron/check_offline.php`, `lib/common.php`, `dashboard_api.php`, `api/*.php`), the tests that assert them, `CLAUDE.md`, `README.md`, `nas_dashboard/SETUP.md`, `cloud_watcher/README.md`, `server/README.md` (create)

- [ ] **Step 1: Update the expected strings in the tests first** — for every assertion on a text that names the host: „NAS“ as the dashboard host → „Server“; „DSM-Aufgabe“/„Aufgabenplaner“ (cron) → „Wächter-Timer“; „Web Station“ → „Webserver“; outside-watcher texts → „Außen-Wächter (NAS)“ with the hint to check power/internet at home and the DSM task `fw-alarm-watch`; permission hint → `sudo chown -R fwalarm: /srv/fw-alarm/data`. Texts that really mean the NAS (bridge, watcher on the NAS) stay. Bark strings stay ASCII.
- [ ] **Step 2: Run the tests to see them fail** — `node --test "tests/dashboard/*.test.mjs"` → FAIL on the changed strings.
- [ ] **Step 3: Change the strings in the code**; keep identifiers, event names, file names unchanged.
- [ ] **Step 4: Run unit + e2e** — `node --test "tests/dashboard/*.test.mjs"`; sandbox `setup.sh` then all scenarios `smoke api-settings api-keys api-session shell overview recipients log settings switch`. Expected: all PASS.
- [ ] **Step 5: Docs** — `server/README.md` (runbook: layout, setup, deploy, checks, logs, tunnel re-login, rollback, Always-Free notes incl. the 2-GB reason); `CLAUDE.md` (host = Oracle VM via tunnel, bridge on the NAS until the flash, watcher swapped, deploy via `server/deploy.sh`, VM paths, Cloudflare settings that must stay); `nas_dashboard/SETUP.md` (points to `server/README.md`, NAS part reduced to the bridge); `cloud_watcher/README.md` (runs as DSM task, `TARGET_NAME`); `README.md` overview.
- [ ] **Step 6: Commit, push, deploy** — `git commit -m "Texte ortsneutral, Doku für den Betrieb auf der Oracle-VM"`; `git push origin main && bash server/deploy.sh HEAD`; `bash server/check.sh public https://alarm.tunero.dev` → all PASS.
- [ ] **Step 7: Update memory** — `nas-deploy-via-smb` (now: bridge only), new reference memory for the VM deploy, `oracle-outside-watcher-vm` (watcher moved, VM hosts the dashboard, 2 GB), `planned-oracle-migration` (move done, passkey next, flash pending).

---

### Task 12: After the flash (user, ~mid-December 2026)

**Files:**
- Modify: `nas_bridge/README.md`, `CLAUDE.md`

- [ ] **Step 1: Before the flash** — `bash server/check.sh public https://alarm.tunero.dev` (`esp32-ua`, `tls12` PASS). User sets `REMOTE_BASE_URL "https://alarm.tunero.dev/api"` in `config.h` and flashes.
- [ ] **Step 2: Post-flash tests** (from memory `firmware-on-main-not-flashed`): short and long jumper on the contact, DEMO-ALARM immediately followed by a relay alarm, stuck-contact warning with a short `STUCK_CONTACT_WARN_MS`, Wi-Fi router off/on. Box status carries `alarm_seq`.
- [ ] **Step 3: Box no longer uses the bridge** — VM nginx log: `status.php`/`poll.php` lines without `bridge=1` for 24 h.
- [ ] **Step 4: Remove the box paths from the NAS** (`api/status.php`, `poll.php`, `ack.php`, `keys.php`) and the old dashboard leftovers (`lib/common.php`, `lib/shell.php`, `assets/app/`, `assets/vendor/`, `assets/classic/`, `manifest.json`); keep `api/mute.php`, `lib/bridge.php`, `bridge_config.php`, `index.php`, `classic.php`, `dashboard_api.php`, `assets/app.css`, `assets/favicon.svg`, `assets/app/theme-boot.js` (delete the rest of `assets/app/`). Check: an old mute link still works (own link on/off).
- [ ] **Step 5: Docs + memory** — bridge = mute-link forwarder only; retire memory `firmware-on-main-not-flashed`. Commit.
