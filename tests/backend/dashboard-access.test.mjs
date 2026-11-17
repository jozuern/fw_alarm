// Zugangsregeln der Dashboard-API (dashboard_api.php): Jede Aktion steht mit
// ihren drei Regeln in EINER Tabelle - post (nur per POST), csrf
// (CSRF-Token nötig), admin (nur die Admin-Rolle). Geprüft wird vor jedem
// Handler in dieser Reihenfolge: Anmeldung (401), Methode (405), CSRF (403
// "csrf"), Rolle (403 "readonly"); eine unbekannte Aktion ist gesperrt. Ein
// abgelehnter Aufruf darf nichts auslösen: keine Bark-Meldung, kein
// Verlaufseintrag. Diese Datei braucht außer Anmelden nur die Aktion status
// und abgelehnte Aufrufe.
import assert from 'node:assert/strict';
import { after, before, beforeEach, describe, it } from 'node:test';
import {
  READER, postForm, request, session, settle, startBackend,
} from './harness.mjs';

let backend;
let admin;
let reader;
before(async () => { backend = await startBackend(); });
after(async () => { await backend.stop(); });
beforeEach(async () => {
  await backend.reset();
  // Ein Empfänger (id 1), direkt in die Liste gelegt: Die Aktionen hätten so
  // jemanden, an dem sie etwas ändern oder dem sie etwas senden.
  await backend.state.seedRecipients(
    [{ id: 1, label: 'Anna', key: 'annakey00001', added_at: 1, mute_token: 'AnnasToken12' }],
    { version: 1, nextId: 2 });
  admin = await session(backend);
  reader = await session(backend, READER);
});

// Abschrift der Tabelle DASHBOARD_ACTIONS (nas_dashboard/dashboard_api.php).
const ACTIONS = {
  // Lesen (auch per GET)
  status:                  { post: false, csrf: false, admin: false },
  keys_list:               { post: false, csrf: false, admin: false },
  log_view:                { post: false, csrf: false, admin: false },
  // lesend, aber nur für den Admin
  notify_list:             { post: false, csrf: false, admin: true },
  settings_list:           { post: false, csrf: false, admin: true },
  // Ändern/Auslösen
  enqueue:                 { post: true, csrf: true, admin: true },
  cancel:                  { post: true, csrf: true, admin: true },
  demo_set:                { post: true, csrf: true, admin: true },
  keys_add:                { post: true, csrf: true, admin: true },
  keys_delete:             { post: true, csrf: true, admin: true },
  keys_rename:             { post: true, csrf: true, admin: true },
  keys_replace_key:        { post: true, csrf: true, admin: true },
  keys_set_phone:          { post: true, csrf: true, admin: true },
  keys_new_mute_token:     { post: true, csrf: true, admin: true },
  keys_mute:               { post: true, csrf: true, admin: true },
  keys_set_mute_permanent: { post: true, csrf: true, admin: true },
  keys_set_mute_volume:    { post: true, csrf: true, admin: true },
  keys_test:               { post: true, csrf: true, admin: true },
  notify_set:              { post: true, csrf: true, admin: true },
  settings_set:            { post: true, csrf: true, admin: true },
  notify_test:             { post: true, csrf: true, admin: true },
  alarm:                   { post: true, csrf: true, admin: true },
  false_alarm:             { post: true, csrf: true, admin: true },
};
const NAMES = Object.keys(ACTIONS);
// Felder, mit denen die Aktion etwas täte, wenn sie durchkäme - so zeigt
// "nichts bei Bark, nichts im Verlauf", dass wirklich abgelehnt wurde.
const FIELDS = {
  enqueue: { type: 'TEST' },
  demo_set: { enabled: '1', id: 1 },
  keys_add: { label: 'Eindringling', key: 'fremdkey0001' },
  keys_delete: { id: 1 },
  keys_rename: { id: 1, label: 'x' },
  keys_replace_key: { id: 1, key: 'fremdkey0002' },
  keys_set_phone: { id: 1, phone: '123' },
  keys_new_mute_token: { id: 1 },
  keys_mute: { id: 1, muted: '1' },
  keys_set_mute_permanent: { id: 1, permanent: '1' },
  keys_set_mute_volume: { id: 1, volume: '3' },
  keys_test: { id: 1 },
  notify_set: { type: 'login_success', key: 'enabled', value: '1' },
  settings_set: { key: 'mute_max_seconds', value: '7200' },
  notify_test: { type: 'box_offline' },
  alarm: { expect_demo: '0' },
  false_alarm: { expect_demo: '0' },
};
const STATUS_KEYS = ['ok', 'status', 'seen_age_seconds', 'offline', 'command', 'demo', 'nas_keys_version',
  'demo_sync_ok', 'device_hint', 'timezone', 'csrf', 'watchdog', 'server_time'];

const apiUrl = (params = {}) => `${backend.base}/dashboard_api.php?${new URLSearchParams(params)}`;
const getAs = (cookie, action) => request(apiUrl({ action }), { headers: cookie ? { cookie } : {} });
const postAs = (cookie, action, headers = {}) => postForm(`${backend.base}/dashboard_api.php`,
  { action, ...FIELDS[action] }, { ...(cookie ? { cookie } : {}), ...headers });
const json = (r) => JSON.parse(r.text);

// Nach jedem abgelehnten Aufruf: nichts bei Bark angekommen.
function assertRejected(r, httpStatus, code, what) {
  assert.equal(r.status, httpStatus, `${what}: HTTP ${r.status} ${r.text}`);
  assert.match(r.headers.get('content-type'), /^application\/json/, what);
  const body = json(r);
  assert.equal(body.ok, false, what);
  if (code !== undefined) assert.equal(body.code, code, what);
  assert.equal(backend.bark.requests.length, 0, `${what}: nichts an Bark`);
  return body;
}
// Am Ende jedes Falls: auch nach der Antwort nichts gesendet, nichts im Verlauf.
async function assertNothingHappened() {
  await settle();
  assert.equal(backend.bark.requests.length, 0, 'nichts an Bark');
  assert.deepEqual(await backend.state.log(), [], 'nichts im Verlauf');
}

describe('Ohne Anmeldung', () => {
  it('jede Aktion, per GET und per POST: 401 {"ok":false}', async () => {
    for (const action of [...NAMES, 'gibtesnicht']) {
      for (const r of [await getAs(null, action), await postAs(null, action)]) {
        assertRejected(r, 401, undefined, action);
        assert.deepEqual(json(r), { ok: false }, action);
      }
    }
    await assertNothingHappened();
  });
});

describe('Methode', () => {
  it('eine post-Aktion per GET: 405 - auch für die Lese-Rolle', async () => {
    for (const action of NAMES.filter((a) => ACTIONS[a].post)) {
      for (const who of [admin, reader]) {
        const body = assertRejected(await getAs(who.cookie, action), 405, undefined, action);
        assert.equal(body.message, 'Diese Aktion ist nur per POST erlaubt.', action);
      }
    }
    await assertNothingHappened();
  });

  it('unbekannte Aktion: per GET 405, per POST 400 - auch mit gültigem Token', async () => {
    const token = { 'x-csrf-token': (await admin.get('status')).csrf };
    for (const action of ['gibtesnicht', 'STATUS', 'keys_list ', '']) {
      const viaGet = assertRejected(await getAs(admin.cookie, action), 405, undefined, `GET "${action}"`);
      assert.equal(viaGet.message, 'Diese Aktion ist nur per POST erlaubt.');
      const viaPost = assertRejected(await postAs(admin.cookie, action, token), 400, undefined, `POST "${action}"`);
      assert.equal(viaPost.message, 'Unbekannte Aktion.');
    }
    await assertNothingHappened();
  });
});

describe('CSRF-Token', () => {
  it('jede csrf-Aktion ohne Token: 403 "csrf"', async () => {
    for (const action of NAMES.filter((a) => ACTIONS[a].csrf)) {
      assertRejected(await postAs(admin.cookie, action), 403, 'csrf', action);
    }
    await assertNothingHappened();
  });

  it('jede csrf-Aktion mit falschem Token: 403 "csrf"', async () => {
    for (const action of NAMES.filter((a) => ACTIONS[a].csrf)) {
      assertRejected(await postAs(admin.cookie, action, { 'x-csrf-token': 'falsch' }), 403, 'csrf', action);
    }
    await assertNothingHappened();
  });

  it('die Prüfung kommt vor der Rolle: Lese-Rolle ohne Token bekommt 403 "csrf"', async () => {
    for (const action of NAMES.filter((a) => ACTIONS[a].csrf)) {
      assertRejected(await postAs(reader.cookie, action), 403, 'csrf', action);
    }
    await assertNothingHappened();
  });

  it('das Token gilt auch als Formularfeld csrf (statt X-CSRF-Token): die Lese-Rolle kommt damit bis zur Rolle', async () => {
    const token = (await reader.get('status')).csrf;
    assert.ok(token, 'CSRF-Token der Lese-Rolle');
    const asField = (action, csrf) => postForm(`${backend.base}/dashboard_api.php`,
      { action, ...FIELDS[action], csrf }, { cookie: reader.cookie });
    for (const action of NAMES.filter((a) => ACTIONS[a].csrf)) {
      // Richtiges Token im Feld: die CSRF-Prüfung ist bestanden, es scheitert erst an der Rolle.
      assertRejected(await asField(action, token), 403, 'readonly', `${action} (Feld)`);
      // Dasselbe als Kopfzeile.
      assertRejected(await postAs(reader.cookie, action, { 'x-csrf-token': token }), 403, 'readonly', `${action} (Kopfzeile)`);
      // Falsches Token im Feld: weiter "csrf".
      assertRejected(await asField(action, 'falsch'), 403, 'csrf', `${action} (falsches Feld)`);
    }
    await assertNothingHappened();
  });
});

describe('Lese-Rolle', () => {
  it('jede admin-Aktion: 403 "readonly" - auch mit gültigem Token', async () => {
    for (const action of NAMES.filter((a) => ACTIONS[a].admin)) {
      const r = ACTIONS[action].post
        ? await reader.post({ action, ...FIELDS[action] })
        : await reader.get(action);
      assert.equal(r.http, 403, `${action}: HTTP ${r.http}`);
      assert.equal(r.ok, false, action);
      assert.equal(r.code, 'readonly', action);
      assert.equal(backend.bark.requests.length, 0, `${action}: nichts an Bark`);
    }
    await assertNothingHappened();
  });

  it('status darf sie lesen', async () => {
    const r = await reader.get('status');
    assert.equal(r.http, 200);
    assert.equal(r.ok, true);
  });
});

describe('Antwort von status', () => {
  it('genau diese Felder - für Admin und Lese-Rolle', async () => {
    for (const who of [admin, reader]) {
      const r = await getAs(who.cookie, 'status');
      assert.equal(r.status, 200);
      assert.deepEqual(Object.keys(json(r)).sort(), [...STATUS_KEYS].sort());
      assert.ok(json(r).csrf, 'CSRF-Token der Sitzung');
    }
  });

  it('ob Ausfall-Meldungen abgeschaltet sind, erfährt nur der Admin (watchdog.offline_alert/web_alert)', async () => {
    const forAdmin = (await admin.get('status')).watchdog;
    assert.equal(forAdmin.offline_alert, true);
    assert.equal(forAdmin.web_alert, true);
    const forReader = (await reader.get('status')).watchdog;
    assert.equal('offline_alert' in forReader, false);
    assert.equal('web_alert' in forReader, false);
  });
});
