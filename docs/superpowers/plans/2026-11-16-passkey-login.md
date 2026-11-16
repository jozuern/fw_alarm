# Passkey Login Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Sign in to the dashboard with a passkey (Face ID on the iPhone, Windows Hello on the PC); the password stays as fallback.

**Architecture:** Server-side WebAuthn with the vendored MIT library `lbuchs/WebAuthn` v2.2.0 under `nas_dashboard/lib/vendor/WebAuthn/`. Credentials and short-lived challenges live in the data dir (JSON guard files like all runtime data). Login endpoints sit in `index.php` next to the password login and share its guards; registration and management are dashboard actions. A small classic script `assets/app/passkey.js` (`FW.passkey`) talks to `navigator.credentials` on the login page and in Settings → Konto.

**Tech Stack:** PHP 8.3 (openssl, mbstring), WebAuthn Level 2, vanilla JS classic scripts, Preact/htm views, CDP virtual authenticator for e2e.

**Spec:** `docs/superpowers/specs/2026-11-16-oracle-move-design.md` §11. Runs **after** `docs/superpowers/plans/2026-11-16-oracle-move.md` is done (needs the real domain for the device check).

## Global Constraints

- RP ID = `passkey_rp_id` from config.php, else the host of `public_base_url`; both empty → feature off (no button, endpoints 404). Never derived from the `Host` header.
- `userVerification: required`, attestation `none`, algorithms ES256 (-7) and RS256 (-257), discoverable credentials (`residentKey: required`), timeout 60 s.
- Challenges: 32 random bytes, valid 300 s, single use, at most 200 stored (full → the oldest is dropped), expired ones pruned on every write. Anonymous `passkey_options` calls are throttled per sender with `throttle_hit($config, 'passkey', 30, 300)` (non-fatal like all throttles; 429 when exceeded).
- Unreadable `passkeys.json` and `.bak`: existing sessions stay valid (`error_log`), new passkey logins and registrations fail; the password keeps working. Not being locked out in an emergency wins over revocation.
- Changing an account's password also invalidates that account's passkeys: each entry stores the `dashboard_auth_stamp()` valid when it was created; login ignores entries whose stamp no longer matches, `passkey_list` shows them as „ungültig (Passwort geändert)“ for deletion. (Lost phone = change the password in config.php, and every way in for that account is closed.)
- At most 10 passkeys per account; label 1–40 chars, same character rule as recipient names.
- Adding a passkey requires the account's current password (counts in the login throttle).
- Passkey login: same `Sec-Fetch-Site` rule, same login throttle (`login_throttle_attempt()` before verifying), session only on success, same `auth_stamp`, same `login_success`/`login_lockout` notifications (text adds „per Passkey“). Error answers never say why (`code: "passkey"`).
- Deleting a passkey ends every session that was signed in with it.
- Data format stays portable for the Go rewrite: credential id base64url, public key PEM, sign count int.
- Existing rules hold: CSP without inline script/style, every new action in `DASHBOARD_ACTIONS`, every new log event in `log_event_types()` + `describeLogEntry`, German UI texts with umlauts, „die Box“.
- Local tests run on `http://localhost:8787` (an RP ID cannot be an IP); the sandbox config sets `passkey_rp_id` = `localhost`.

## Review Focus

1. **Replayed login** (the same signed assertion POSTed twice) — second attempt fails, no session. Test: Task 2 scenario case `replay`.
2. **A stolen, logged-in session tries to add its own passkey** — fails without the account password. Test: Task 2 case `register-needs-password`.
3. **Passkey removed on the PC while the iPhone is signed in with it** — the iPhone session ends on its next request (401). Test: Task 2 case `delete-ends-session`.
4. **Reader account tries to see or delete the admin's passkeys** — sees only its own, delete of a foreign id fails. Test: Task 2 case `foreign-delete`.
5. **Browser without WebAuthn (or feature off)** — no passkey button, password login unchanged. Test: Task 4 case `no-webauthn`.

---

## File Structure

| Path | Responsibility |
|---|---|
| `nas_dashboard/lib/vendor/WebAuthn/` (+ `LICENSE`, `VERSION`) | Vendored lbuchs/WebAuthn v2.2.0 `src/` |
| `nas_dashboard/lib/passkeys.php` | Storage, challenge store, option builders, verification wrappers, session check |
| `nas_dashboard/lib/common.php` | `begin_authenticated_session()`, passkey check in `resume_dashboard_session()`, log event types |
| `nas_dashboard/index.php` | `passkey_options` / `passkey_login` endpoints |
| `nas_dashboard/dashboard_api.php` | `passkey_list`, `passkey_register_options`, `passkey_register_finish`, `passkey_delete` |
| `nas_dashboard/lib/shell.php` | Login page: passkey button + `passkey.js`; meta `passkey-enabled` |
| `nas_dashboard/assets/app/passkey.js` | `FW.passkey` |
| `nas_dashboard/assets/app/store.js`, `view-settings.js`, `logic.js` | Settings → Konto, log texts |
| `nas_dashboard/config.example.php` | `passkey_rp_id` |
| `tests/dashboard/e2e/cdp.mjs` | + `addVirtualAuthenticator(page)` |
| `tests/dashboard/sandbox/make-config.php` | + `passkey_rp_id` = `localhost` |
| `tests/dashboard/e2e/scenarios/api-passkey.mjs`, `passkey.mjs` | Backend and UI scenarios |
| `tests/dashboard/passkey.test.mjs` | Unit tests for `FW.passkey` helpers |

---

### Task 1: Vendor the library

**Files:**
- Create: `nas_dashboard/lib/vendor/WebAuthn/**`, `nas_dashboard/lib/vendor/WebAuthn/LICENSE`, `nas_dashboard/lib/vendor/WebAuthn/VERSION`

- [ ] **Step 1: Download** `https://github.com/lbuchs/WebAuthn/archive/refs/tags/v2.2.0.tar.gz` into a new empty scratch dir, extract there, record the tag's commit SHA (`git ls-remote https://github.com/lbuchs/WebAuthn refs/tags/v2.2.0`).
- [ ] **Step 2: Copy** `src/` (all of `Attestation/`, `Binary/`, `CBOR/`, `WebAuthn.php`, `WebAuthnException.php`) and `LICENSE`; write `VERSION` (`lbuchs/WebAuthn v2.2.0 <sha>, MIT, unmodified`).
- [ ] **Step 3: Read** `src/WebAuthn.php` and note in `lib/passkeys.php`'s header comment the exact signatures of the constructor, `getCreateArgs`, `processCreate`, `getGetArgs`, `processGet`, `getChallenge` and how `_checkOrigin` treats `localhost` and subdomains — later tasks use what the source says, not what this plan assumes.
- [ ] **Step 4: Verify**

Run: `for f in $(find nas_dashboard/lib/vendor/WebAuthn -name '*.php'); do php -l "$f" >/dev/null || echo BAD $f; done; php -r 'require "nas_dashboard/lib/vendor/WebAuthn/WebAuthn.php"; echo get_class(new lbuchs\WebAuthn\WebAuthn("FW-Alarm","localhost",["none","packed"])), PHP_EOL;'`
Expected: no `BAD`, prints `lbuchs\WebAuthn\WebAuthn` (if the library needs its own autoloader, add `lib/vendor/WebAuthn/autoload.php` with a PSR-4 `spl_autoload_register` for `lbuchs\WebAuthn\` and use that instead).

- [ ] **Step 5: Commit** — `git commit -m "Passkey: WebAuthn-Bibliothek lbuchs/WebAuthn 2.2.0 eingebettet"`

---

### Task 2: Backend scenario first (fails)

**Files:**
- Modify: `tests/dashboard/e2e/cdp.mjs`, `tests/dashboard/sandbox/make-config.php`, `tests/dashboard/sandbox/README.md`
- Create: `tests/dashboard/e2e/scenarios/api-passkey.mjs`

**Interfaces:**
- Produces: `addVirtualAuthenticator(page): Promise<string>` in `cdp.mjs` (CDP `WebAuthn.enable`, `WebAuthn.addVirtualAuthenticator` with `protocol: 'ctap2'`, `transport: 'internal'`, `hasResidentKey: true`, `hasUserVerification: true`, `isUserVerified: true`; returns the authenticator id).
- Consumes (from Task 3, defined here as the contract):
  - `POST index.php` `action=passkey_options` → 200 `{ok:true, cid, publicKey}` (publicKey = `PublicKeyCredentialRequestOptions` with base64url strings for binary fields).
  - `POST index.php` `action=passkey_login` fields `cid`, `id`, `client_data`, `authenticator_data`, `signature`, `user_handle` (all base64url) → 200 `{ok:true, redirect:"index.php"}` + session cookie, or 401 `{ok:false, code:"passkey"}`.
  - `dashboard_api.php` `passkey_list` (GET) → `{ok, enabled, passkeys:[{id, label, created_at, last_used_at, current}]}`; `passkey_register_options` (POST, csrf, `password`, `label`) → `{ok, cid, publicKey}` or 403 `{code:"password"}`; `passkey_register_finish` (POST, csrf, `cid`, `client_data`, `attestation`) → `{ok, id}`; `passkey_delete` (POST, csrf, `id`) → `{ok, ended_own_session}` or 404 `{code:"not_found"}`.

- [ ] **Step 1: Sandbox config** — `make-config.php` adds `$config['passkey_rp_id'] = 'localhost';`; README notes that passkey scenarios use `SANDBOX_BASE=http://localhost:8787`.
- [ ] **Step 2: Write `api-passkey.mjs`** (page-context `fetch` + `navigator.credentials` on `http://localhost:8787`, virtual authenticator attached) with cases:
  - `register-needs-password`: logged in as admin, `passkey_register_options` with wrong password → 403 `password`; nothing stored.
  - `register`: correct password + label `PC` → options → `navigator.credentials.create` → `finish` → `{ok, id}`; `passkey_list` shows 1 entry `PC`, `current: false`.
  - `login`: logout, `passkey_options` → `navigator.credentials.get` → `passkey_login` → ok; `action=status` 200; `passkey_list` entry `current: true`, `last_used_at` set.
  - `replay`: POST the same `passkey_login` body again → 401 `passkey`.
  - `cross-site`: `passkey_options` sent from Node `fetch` (not the page — Chrome sets `Sec-Fetch-Site` itself) with `Sec-Fetch-Site: cross-site` → 403, nothing counted.
  - `options-throttle`: 31 `passkey_options` calls from Node within a few seconds → the 31st gets 429.
  - `password-change-revokes`: after `register`, change `dashboard_password_hash` in the sandbox config (same mechanism as `api-session`'s `withConfig`) → passkey login 401, `passkey_list` (after password login) shows the entry as invalid.
  - `foreign-delete`: log in as `leser` (password), `passkey_list` → empty; `passkey_delete` with the admin's id → 404 `not_found`.
  - `delete-ends-session`: second browser context logged in with the passkey; first context (admin by password) deletes it → `{ok, ended_own_session:false}`; second context's next `action=status` → 401.
  - `throttle`: `login_max_failures` failed `passkey_login` attempts (garbage signatures) → the next attempt gets 429 even with a valid assertion; password login from the same address is locked too.
- [ ] **Step 3: Run to see it fail**

Run: `bash tests/dashboard/sandbox/setup.sh "$S" && bash tests/dashboard/sandbox/start.sh "$S" && SANDBOX_BASE=http://localhost:8787 SANDBOX_DIR="$S" node tests/dashboard/e2e/run.mjs api-passkey`
Expected: FAIL at `register-needs-password` (unknown action).

---

### Task 3: Backend

**Files:**
- Create: `nas_dashboard/lib/passkeys.php`
- Modify: `nas_dashboard/lib/common.php`, `nas_dashboard/index.php`, `nas_dashboard/dashboard_api.php`, `nas_dashboard/config.example.php`, `nas_dashboard/assets/app/logic.js` (log texts)

**Interfaces:**
- Produces (in `lib/passkeys.php`):
  - `passkey_rp_id(array $config): string` — `''` = off.
  - `passkey_webauthn(array $config): \lbuchs\WebAuthn\WebAuthn` — rp name `FW-Alarm`, formats `['none','packed']`.
  - `read_passkeys(): ?array` / `mutate_passkeys(callable $fn)` — file `passkeys.json`, lock `passkeys`, `backup_then_write()`; shape `{version:1, handles:{admin:b64url, readonly:b64url}, next_id:int, entries:[{id, role, user, label, credential_id, public_key, sign_count, auth_stamp, created_at, last_used_at}]}`; `null` when file and `.bak` are unreadable (writers abort, like `read_alarm_keys_state()`).
  - `passkey_challenge_issue(string $purpose, string $role, string $user, string $challenge): string` (returns `cid` = 32 hex chars) / `passkey_challenge_take(string $cid, string $purpose): ?array` — file `passkey_challenges.json`, lock `passkey_challenges`.
  - `passkey_session_valid(array $config): bool` — false if the session carries `passkey_id` and no entry with that id exists; true (plus `error_log`) when the store is unreadable (`read_passkeys()` = `null`).
- Produces (in `lib/common.php`): `begin_authenticated_session(array $config, string $role, string $user, ?int $passkeyId): void` — the block that is today inline in `index.php` lines 86–114 (regenerate id, session fields, `auth_stamp`, CSRF, deferred `login_success` notify); with `$passkeyId` it also sets `$_SESSION['passkey_id']` and the notify text says „per Passkey“.
- Lock order: `passkeys` and `passkey_challenges` are leaf locks (never held together, never around Bark); the log entry is written after the lock is released.

- [ ] **Step 1: Extract `begin_authenticated_session()`** and use it in the password login; run `api-session` + `smoke` scenarios → PASS (behavior unchanged).
- [ ] **Step 2: Implement `lib/passkeys.php`** per the interfaces; user handles created lazily (32 random bytes) on the first registration of an account.
- [ ] **Step 3: Endpoints** — `index.php`: `passkey_options` and `passkey_login` before the password branch, same `Sec-Fetch-Site` check, `login_throttle_attempt()` before `processGet`, look up the entry by credential id, check `user_handle` equals the account's handle, update `sign_count`/`last_used_at`, then `begin_authenticated_session()`; failure: `sleep(1)`, 401 `{ok:false, code:"passkey"}`, lockout notification at the threshold exactly like the password branch (reuse, don't copy). `dashboard_api.php`: four actions with `DASHBOARD_ACTIONS` entries (`passkey_list`: post false, csrf false, admin false; the three others: post true, csrf true, admin false), all scoped to the session's own role+user. `resume_dashboard_session()`: return false (and end the session) when `passkey_session_valid()` fails.
- [ ] **Step 4: Log events** — `passkey_added` and `passkey_removed` in `log_event_types()` (category `system`, `admin` true, fields `label`, `id`, `role`); texts in `describeLogEntry` („Passkey „PC“ hinzugefügt“, „Passkey „PC“ entfernt“).
- [ ] **Step 5: `config.example.php`** — commented `'passkey_rp_id' => ''` with a German explanation (default = host of `public_base_url`, must equal the address in the browser, no IP).
- [ ] **Step 6: Run**

Run: `bash tests/dashboard/sandbox/setup.sh "$S"` then `SANDBOX_BASE=http://localhost:8787 SANDBOX_DIR="$S" node tests/dashboard/e2e/run.mjs api-passkey`, then the full suite (`smoke api-settings api-keys api-session shell overview recipients log settings switch`) and `node --test "tests/dashboard/*.test.mjs"`.
Expected: all PASS.

- [ ] **Step 7: Commit** — `git commit -m "Passkey: Anmeldung und Verwaltung im Backend"`

---

### Task 4: Frontend

**Files:**
- Create: `nas_dashboard/assets/app/passkey.js`, `tests/dashboard/passkey.test.mjs`, `tests/dashboard/e2e/scenarios/passkey.mjs`
- Modify: `nas_dashboard/lib/shell.php`, `nas_dashboard/assets/app/store.js`, `nas_dashboard/assets/app/view-settings.js`, `nas_dashboard/assets/app.css`, `tests/dashboard/contrast.test.mjs` (if new colour pairs)

**Interfaces:**
- Produces: `FW.passkey = { supported(): boolean, toB64url(buf: ArrayBuffer): string, fromB64url(s: string): ArrayBuffer, decodeRequestOptions(o), decodeCreationOptions(o), encodeAssertion(cred), encodeAttestation(cred), login(): Promise<{ok:boolean, error?:string}>, register(password: string, label: string): Promise<{ok:boolean, code?:string}> }`. `passkey.js` is loaded on the login page (alone) and in the app shell (`app_script_paths()`, before `store.js`).

- [ ] **Step 1: Unit tests `passkey.test.mjs`** (load via `helpers.mjs` vm): base64url round-trip for empty, 1, 2, 3 and 32 bytes incl. `-`/`_`, no padding; `decodeRequestOptions` turns `challenge` and `allowCredentials[].id` into ArrayBuffers and leaves other fields untouched; `supported()` false without `PublicKeyCredential`.
- [ ] **Step 2: Run to fail**, then **implement `passkey.js`**, run → PASS.
- [ ] **Step 3: Login page** — `render_login_page()` gets a „Mit Passkey anmelden“ button (hidden by default, shown by `passkey.js` when `supported()` and meta `passkey-enabled` is `1`), an `aria-live` message line, and the existing password form unchanged below a separator „oder mit Passwort“. Success → `location.href = redirect`; error → „Anmeldung mit Passkey fehlgeschlagen.“ (429 → the throttle text of the password branch).
- [ ] **Step 4: Settings → Konto** — section „Passkeys“: list (label, „angelegt am“, „zuletzt benutzt“, badge „diese Sitzung“), button „Passkey hinzufügen“ opens a sheet (label, current password, „Anlegen“), delete via the confirm sheet (red button, focus on „Abbrechen“; text warns when it is the current session's passkey). Store actions `loadPasskeys()`, `addPasskey(label, password)`, `deletePasskey(id)` with `ui.Msg` feedback; no polling (load on opening the section).
- [ ] **Step 5: UI scenario `passkey.mjs`** — cases `no-webauthn` (WebAuthn hidden via `Page.addScriptToEvaluateOnNewDocument` deleting `window.PublicKeyCredential` → no button, password login works), `add-and-login` (iPhone width: add „iPhone“ in Settings with password, log out, button signs in), `delete-current` (deleting the current passkey logs out to the login page). Screenshots light/dark.
- [ ] **Step 6: Run everything** — unit tests, `api-passkey`, `passkey` (both on `localhost`), full suite. Expected: all PASS.
- [ ] **Step 7: Commit** — `git commit -m "Passkey: Anmeldeseite und Verwaltung in den Einstellungen"`

---

### Task 5: Ship and check on real devices

- [ ] **Step 1: Docs** — CLAUDE.md (passkey rules from Global Constraints, files, lock names), `server/README.md` (`php8.3-mbstring`, nothing else new), memory `planned-oracle-migration` (passkey done). Commit.
- [ ] **Step 2: Deploy** — `git push origin main && bash server/deploy.sh HEAD && bash server/check.sh public https://alarm.tunero.dev` → all PASS.
- [ ] **Step 3: Device check (user, assign a task)** — iPhone (Home-Bildschirm-App): Settings → Konto → add „iPhone“ with Face ID; log out; sign in with the passkey. PC: add „PC“ with Windows Hello; sign in. Delete a test passkey. Report what you saw.
- [ ] **Step 4: Security review hand-off** — prepare the self-contained prompt for a Mythos 5.2 review session (scope: `lib/passkeys.php`, endpoints, session check, vendored library usage; threat model: stolen session, replay, cross-site, reader role; sandbox instructions; fix Critical/High/Medium on `main`, German commits, no audit doc) per memory `mythos-security-reviews`; wait for its "done" before further changes.
