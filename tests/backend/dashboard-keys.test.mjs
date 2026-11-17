// Empfänger im Dashboard pflegen (dashboard_api.php): umbenennen
// (keys_rename), Key ersetzen (keys_replace_key), Telefonnummer
// (keys_set_phone, keys_add mit phone) und neue Kurzbefehl-Links
// (keys_new_mute_token). Was die Box nicht kennt (Name, Telefon, Token),
// erhöht die Listen-Version nicht; die Telefonnummer sieht nur der Admin und
// sie steht nie im Verlauf. Dazu: Lässt sich der Datenordner gar nicht
// beschreiben, zeigt das Dashboard eine lesbare Meldung - ohne Pfad.
// Einige Fälle fragen die Liste wie die Box ab (api/keys.php), rufen den
// Kurzbefehl-Link auf (api/mute.php) oder legen einen Befehl an (enqueue).
import assert from 'node:assert/strict';
import { after, before, beforeEach, describe, it } from 'node:test';
import { READER, box, postForm, request, session, startBackend } from './harness.mjs';

let backend;
let admin;
before(async () => { backend = await startBackend(); });
after(async () => { await backend.stop(); });
beforeEach(async () => {
  await backend.reset();
  admin = await session(backend);
});

const ANNA_KEY = 'annakey00001';
// Annas neuer Key (neues iPhone) - mit anderem Anfang, damit auch die
// maskierte Form ("neue...") zeigt, dass ersetzt wurde.
const ANNA_KEY_NEW = 'neuekey00001';
const MAX_KEY = 'maxkey000001';
const PHONE_KEY = 'phonekey0001';
const STORAGE = {
  ok: false,
  code: 'storage',
  message: 'Fehler: Datenordner nicht beschreibbar (Rechte des Web-Benutzers auf dem Server prüfen).',
};

const list = () => admin.get('keys_list');
const byId = (r, id) => r.keys.find((x) => x.id === id);
const entry = async (id) => byId(await list(), id);
const version = async () => (await list()).version;
const mute = async (t, state) => {
  const r = await request(`${backend.base}/api/mute.php?${new URLSearchParams({ t, state })}`);
  return { status: r.status, text: r.text, cache: r.headers.get('cache-control') || '' };
};
// Anna mit neuem Namen und Telefonnummer (so beginnt der Ablauf im Dashboard).
async function annaWithPhone() {
  const anna = await admin.addKey('Anna', ANNA_KEY);
  assert.equal((await admin.post({ action: 'keys_rename', id: anna.id, label: 'Anna B.' })).ok, true);
  assert.equal((await admin.post({ action: 'keys_set_phone', id: anna.id, phone: '+49 171 1234567' })).ok, true);
  return entry(anna.id);
}

describe('Telefonnummer', () => {
  it('keys_add speichert sie mit; eine ungültige lehnt das Anlegen ganz ab', async () => {
    let r = await admin.post({ action: 'keys_add', label: 'Anna', key: ANNA_KEY });
    assert.equal(r.ok, true, `keys_add Anna: ${JSON.stringify(r)}`);

    r = await admin.post({ action: 'keys_add', label: 'Telefon', key: PHONE_KEY, phone: '+49 171 99887' });
    assert.equal(r.ok, true, `keys_add mit phone: ${JSON.stringify(r)}`);
    const withPhone = (await list()).keys.find((k) => k.label === 'Telefon');
    assert.equal(withPhone.phone, '+49 171 99887', 'phone bei keys_add gespeichert');

    const before = (await list()).keys.length;
    r = await admin.post({ action: 'keys_add', label: 'Kaputt', key: 'badphone0001', phone: 'abc' });
    assert.equal(r.ok, false, 'ungültiges phone lehnt keys_add ab');
    assert.equal((await list()).keys.length, before, 'bei ungültigem phone wird nichts angelegt');
  });

  it('keys_set_phone: gültig gespeichert, ungültig abgelehnt, keine neue Version; leer entfernt sie', async () => {
    const anna = await admin.addKey('Anna', ANNA_KEY);
    const v0 = await version();
    assert.equal((await admin.post({ action: 'keys_set_phone', id: anna.id, phone: '+49 171 1234567' })).ok, true, 'set_phone gültig');
    assert.equal((await admin.post({ action: 'keys_set_phone', id: anna.id, phone: 'abc' })).ok, false, 'set_phone ungültig abgelehnt');
    assert.equal(await version(), v0, 'phone erhöht die Version nicht');
    assert.equal((await entry(anna.id)).phone, '+49 171 1234567', 'Telefon gespeichert');

    assert.equal((await admin.post({ action: 'keys_set_phone', id: anna.id, phone: '' })).ok, true, 'phone leeren');
    assert.equal((await entry(anna.id)).phone, '', 'phone ist leer');
  });
});

describe('Umbenennen', () => {
  it('keys_rename ändert den Namen ohne neue Version; ein leerer Name wird abgelehnt', async () => {
    const anna = await admin.addKey('Anna', ANNA_KEY);
    const v0 = await version();
    const r = await admin.post({ action: 'keys_rename', id: anna.id, label: 'Anna B.' });
    assert.equal(r.ok, true, `keys_rename: ${JSON.stringify(r)}`);
    assert.equal(await version(), v0, 'rename erhöht die Version nicht');
    assert.equal((await admin.post({ action: 'keys_rename', id: anna.id, label: '' })).ok, false, 'leerer Name abgelehnt');
    assert.equal((await entry(anna.id)).label, 'Anna B.');
  });
});

describe('Key ersetzen', () => {
  it('keys_replace_key: der neue Key gilt, neue Version; Name, Telefon und Kurzbefehl-Token bleiben', async () => {
    const anna = await annaWithPhone();
    const v0 = await version();
    const r = await admin.post({ action: 'keys_replace_key', id: anna.id, key: ANNA_KEY_NEW });
    assert.equal(r.ok, true, `replace_key: ${JSON.stringify(r)}`);
    const after = await list();
    const k = byId(after, anna.id);
    assert.ok(after.version > v0, 'replace_key erhöht die Version');
    assert.equal(k.mute_token, anna.mute_token, 'Token bleibt');
    assert.equal(k.phone, '+49 171 1234567', 'Telefon bleibt');
    assert.equal(k.label, 'Anna B.', 'Name bleibt');
    assert.equal(k.key_masked, 'neue...', 'der neue Key ist eingetragen');

    // Die Box bekommt unter der neuen Version den neuen Key - den alten nicht mehr.
    const forBox = await box(backend).keys();
    assert.equal(forBox.status, 200);
    assert.equal(forBox.kv.version, String(after.version));
    assert.equal(forBox.kv.count, '1');
    assert.equal(forBox.kv.key0, ANNA_KEY_NEW);
    assert.equal(forBox.text.includes(ANNA_KEY), false, 'alter Key nicht mehr in der Liste der Box');
  });

  it('doppelter Key, ungültiger Key und unbekannte id werden abgelehnt', async () => {
    const anna = await admin.addKey('Anna', ANNA_KEY);
    const r = await admin.post({ action: 'keys_add', label: 'Max', key: MAX_KEY });
    assert.equal(r.ok, true, 'keys_add Max');
    const v0 = await version();
    assert.equal((await admin.post({ action: 'keys_replace_key', id: anna.id, key: MAX_KEY })).ok, false, 'doppelter Key abgelehnt');
    assert.equal((await admin.post({ action: 'keys_replace_key', id: anna.id, key: 'x' })).ok, false, 'ungültiger Key abgelehnt');
    assert.equal((await admin.post({ action: 'keys_replace_key', id: 999999, key: ANNA_KEY_NEW })).ok, false, 'unbekannte id abgelehnt');
    assert.equal(await version(), v0, 'nichts geändert');
  });
});

describe('Demo-Empfänger', () => {
  it('sein Key ist nicht ersetzbar; Umbenennen zieht die Anzeige des Demo-Modus mit', async () => {
    const anna = await annaWithPhone();
    let r = await admin.post({ action: 'demo_set', enabled: '1', id: anna.id });
    assert.equal(r.ok, true, `demo_set an: ${JSON.stringify(r)}`);
    r = await admin.post({ action: 'keys_replace_key', id: anna.id, key: ANNA_KEY_NEW });
    assert.equal(r.ok, false, `Demo-Empfänger: Key-Ersatz abgelehnt: ${JSON.stringify(r)}`);
    assert.match(r.message, /Demo-Empfänger/);
    r = await admin.post({ action: 'keys_rename', id: anna.id, label: 'Anna C.' });
    assert.equal(r.ok, true, `rename des Demo-Empfängers: ${JSON.stringify(r)}`);
    assert.equal((await admin.get('status')).demo.label, 'Anna C.', 'Demo-Banner zeigt neuen Namen');
    assert.equal((await list()).demo.label, 'Anna C.', 'keys_list.demo zeigt neuen Namen');
  });
});

describe('Lese-Rolle', () => {
  it('sieht weder Telefonnummer noch Kurzbefehl-Token und darf nichts davon ändern', async () => {
    const anna = await annaWithPhone();
    const reader = await session(backend, READER);
    const roList = await reader.get('keys_list');
    assert.ok(roList.keys.length > 0);
    assert.ok(roList.keys.every((x) => !('phone' in x)), 'Lese-Rolle sieht kein phone');
    assert.ok(roList.keys.every((x) => !('mute_token' in x)), 'Lese-Rolle sieht keinen Kurzbefehl-Token');
    for (const action of ['keys_rename', 'keys_replace_key', 'keys_set_phone', 'keys_new_mute_token']) {
      const r = await reader.post({ action, id: anna.id, label: 'x', key: ANNA_KEY_NEW, phone: '123' });
      assert.equal(r.http, 403, `${action} für Lese-Rolle 403`);
      assert.equal(r.code, 'readonly', action);
    }
    const unchanged = await entry(anna.id);
    assert.equal(unchanged.label, 'Anna B.');
    assert.equal(unchanged.phone, '+49 171 1234567');
    assert.equal(unchanged.mute_token, anna.mute_token);
  });
});

describe('Neue Kurzbefehl-Links (keys_new_mute_token)', () => {
  it('der alte Link gilt sofort nicht mehr, der neue schaltet; der Stumm-Zustand bleibt', async () => {
    const anna = await annaWithPhone();
    const token = anna.mute_token;
    let m = await mute(token, 'on');
    assert.ok(m.status === 200 && /muted=1/.test(m.text), `alter Link schaltet vor dem Erneuern: ${m.text}`);
    assert.match(m.cache, /no-store/, 'Kurzbefehl-Antwort darf nicht zwischengespeichert werden');

    const vTok = await version();
    const r = await admin.post({ action: 'keys_new_mute_token', id: anna.id });
    assert.equal(r.ok, true, `keys_new_mute_token: ${JSON.stringify(r)}`);
    assert.equal(JSON.stringify(r).includes(token), false, 'Antwort enthält keinen Token');
    const k = await entry(anna.id);
    const token2 = k.mute_token;
    assert.ok(typeof token2 === 'string' && token2.length >= 8 && token2 !== token, `neuer Token: ${token2}`);
    assert.equal(k.muted, true, 'Stumm-Zustand bleibt beim Erneuern');
    assert.equal(await version(), vTok, 'neue Links erhöhen die Version nicht');

    m = await mute(token, 'off');
    assert.equal(m.status, 404, 'alter Link nach dem Erneuern ungültig');
    assert.equal((await entry(anna.id)).muted, true, 'alter Link hat nichts geschaltet');
    m = await mute(token2, 'off');
    assert.ok(m.status === 200 && /muted=0/.test(m.text), `neuer Link schaltet: ${m.text}`);
    assert.equal((await entry(anna.id)).muted, false);
  });

  it('unbekannte id wird abgelehnt', async () => {
    await admin.addKey('Anna', ANNA_KEY);
    assert.equal((await admin.post({ action: 'keys_new_mute_token', id: 999999 })).ok, false);
  });
});

describe('Verlauf', () => {
  it('die Ereignisse stehen drin - ohne Telefonnummer, ohne Klartext-Key, ohne Token', async () => {
    const r1 = await admin.post({ action: 'keys_add', label: 'Telefon', key: PHONE_KEY, phone: '+49 171 99887' });
    assert.equal(r1.ok, true);
    const anna = await annaWithPhone();
    assert.equal((await admin.post({ action: 'keys_replace_key', id: anna.id, key: ANNA_KEY_NEW })).ok, true);
    assert.equal((await admin.post({ action: 'keys_new_mute_token', id: anna.id })).ok, true);
    const token2 = (await entry(anna.id)).mute_token;

    const logRes = await admin.get('log_view', { limit: 40, filter: 'keys' });
    const events = logRes.entries.map((e) => e.event);
    for (const e of ['key_renamed', 'key_replaced', 'key_phone_changed', 'key_token_renewed']) {
      assert.ok(events.includes(e), `Verlauf ohne ${e}`);
    }
    const raw = JSON.stringify(logRes);
    assert.ok(!raw.includes('1234567') && !raw.includes('99887'), 'Telefonnummer im Verlauf');
    assert.ok(!raw.includes(ANNA_KEY) && !raw.includes(ANNA_KEY_NEW), 'Klartext-Key im Verlauf');
    assert.ok(!raw.includes(anna.mute_token) && !raw.includes(token2), 'Kurzbefehl-Token im Verlauf');
  });
});

describe('Datenordner nicht beschreibbar', () => {
  // Die Meldung ist für Menschen und verrät weder Pfad noch Benutzernamen.
  async function assertStorageError(res) {
    assert.equal(res.status, 500);
    assert.match(res.headers.get('content-type'), /^application\/json/);
    assert.deepEqual(JSON.parse(res.text), STORAGE);
    assert.equal(res.text.includes('lock'), false);
    for (const path of [backend.dataDir, backend.dataDir.replace(/\\/g, '/'), backend.dataDir.replace(/\\/g, '/').replace(/^[A-Za-z]:/, '')]) {
      assert.equal(res.text.includes(path), false, path);
    }
  }
  const csrf = async () => (await admin.get('status')).csrf;
  const post = async (fields) => postForm(`${backend.base}/dashboard_api.php`, fields,
    { cookie: admin.cookie, 'x-csrf-token': await csrf() });

  it('keys_add: 500 mit lesbarer Meldung, nichts angelegt', async () => {
    await backend.storage.fail('durable');
    try {
      await assertStorageError(await post({ action: 'keys_add', label: 'Anna', key: ANNA_KEY }));
    } finally {
      await backend.storage.repair('durable');
    }
    assert.deepEqual((await list()).keys, []);
  });

  it('notify_set: 500 mit lesbarer Meldung, nichts gespeichert', async () => {
    await backend.storage.fail('durable');
    try {
      await assertStorageError(await post({ action: 'notify_set', type: 'login_success', key: 'enabled', value: '1' }));
    } finally {
      await backend.storage.repair('durable');
    }
    const notify = await admin.get('notify_list');
    assert.equal(notify.types.find((t) => t.key === 'login_success').enabled, false);
  });

  it('enqueue: 500 mit lesbarer Meldung, kein Befehl angelegt', async () => {
    await backend.storage.fail('durable');
    try {
      await assertStorageError(await post({ action: 'enqueue', type: 'TEST' }));
    } finally {
      await backend.storage.repair('durable');
    }
    assert.equal((await admin.get('status')).command, null);
  });
});
