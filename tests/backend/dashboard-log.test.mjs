// Verlauf im Dashboard (dashboard_api.php?action=log_view): Der Browser
// bekommt die letzten Einträge aus commands.log - neueste zuerst, und von
// jedem Ereignis NUR die freigegebenen Felder in festem Typ. Unbekannte
// Ereignisse und alles, was sonst noch im Verlauf steht (z.B. die
// Einzelergebnisse eines Alarms samt Bark-Keys), gehen nie hinaus. Dazu die
// Filter (Alarme / Empfänger / System), die Obergrenze und die rotierte
// Datei commands.log.1. Der Verlauf entsteht hier direkt in der Datei
// (backend.data.appendLog), nicht über die Aktionen, die ihn sonst schreiben.
import assert from 'node:assert/strict';
import { after, before, beforeEach, describe, it } from 'node:test';
import { READER, now, session, startBackend } from './harness.mjs';

let backend;
let admin;
let reader;
before(async () => { backend = await startBackend(); });
after(async () => { await backend.stop(); });
beforeEach(async () => {
  await backend.reset();
  admin = await session(backend);
  reader = await session(backend, READER);
});

// ---- Ein Eintrag pro Ereignis ----------------------------------------------
// raw  = Zeile, wie sie im Verlauf steht (mit Feldern, die NICHT hinaus
//        dürfen, und mit Werten im falschen Typ),
// view = was der Browser davon bekommt (ohne at/event),
// cat  = Filter, zu dem der Eintrag gehört ('alarm' | 'keys' | 'system'),
// core = Versand-Ereignis (Filter alarm_core), admin = nur für den Admin.
const SECRET_KEY = 'annakey00001';
const SECRET_TOKEN = 'GeheimToken12';
const PHONE = '+49 171 5550123';
const EVENTS = [
  { cat: 'system',
    raw: { event: 'created', command: { id: '7', type: 'TEST', state: 'pending', created_at: 1, created_by: 'admin' } },
    view: { id: 7, type: 'TEST', by: 'admin' } },
  { cat: 'alarm',
    raw: { event: 'delivered', command: { id: 8, type: 'ALARM', state: 'delivered', created_by: 'admin', delivered_at: 2 } },
    view: { id: 8, type: 'ALARM', by: 'admin' } },
  { cat: 'alarm',
    raw: { event: 'acked', command: { id: 9, type: 'ALARM', created_by: 'admin', ack_result: 'executed', ack_message: 'erledigt' } },
    view: { id: 9, type: 'ALARM', by: 'admin', result: 'executed', message: 'erledigt' } },
  // Fehlt ein Feld, gilt der Standard ('' / 0 / false).
  { cat: 'system',
    raw: { event: 'expired', command: { id: 10, type: 'TEST', state: 'expired' } },
    view: { id: 10, type: 'TEST', by: '' } },
  // "message" gibt es nur, wenn der Abbruch einen Grund hat.
  { cat: 'system',
    raw: { event: 'cancelled', command: { id: 11, type: 'TEST', created_by: 'leser', cancelled_by: 'admin', cancel_reason: 'Demo-Stand geändert' } },
    view: { id: 11, type: 'TEST', by: 'admin', message: 'Demo-Stand geändert' } },
  { cat: 'alarm',
    raw: { event: 'late_ack', id: '12', result: 'executed', type: 'ALARM', device_id: 'fw-box-1' },
    view: { id: 12, result: 'executed', type: 'ALARM' } },
  { cat: 'system',
    raw: { event: 'demo_enabled', by: 'admin', label: 'Anna', version: 5 },
    view: { by: 'admin', label: 'Anna' } },
  { cat: 'system',
    raw: { event: 'demo_disabled', by: 'admin', label: '', version: 6 },
    view: { by: 'admin', label: '' } },
  { cat: 'system', raw: { event: 'demo_repaired' }, view: {} },
  { cat: 'keys',
    raw: { event: 'key_added', by: 'admin', label: 'Anna', key: 'anna...', version: 2, phone: PHONE },
    view: { by: 'admin', label: 'Anna', key: 'anna...' } },
  { cat: 'keys',
    raw: { event: 'key_removed', by: 'admin', label: 'Bert', key: 'bert...', version: 3 },
    view: { by: 'admin', label: 'Bert', key: 'bert...' } },
  // Kein Wert, der kein einfacher Wert ist: aus einer Liste wird ''.
  { cat: 'keys',
    raw: { event: 'key_renamed', by: 'admin', label: 'Anna B.', old_label: ['Anna'], key: 'anna...' },
    view: { by: 'admin', label: 'Anna B.', old_label: '', key: 'anna...' } },
  { cat: 'keys',
    raw: { event: 'key_replaced', by: 'admin', label: 'Anna B.', key: 'neuk...', old_key: 'anna...', version: 4 },
    view: { by: 'admin', label: 'Anna B.', key: 'neuk...', old_key: 'anna...' } },
  { cat: 'keys',
    raw: { event: 'key_phone_changed', by: 'admin', label: 'Anna B.', key: 'neuk...', phone: PHONE },
    view: { by: 'admin', label: 'Anna B.' } },
  { cat: 'keys',
    raw: { event: 'key_token_renewed', by: 'admin', label: 'Anna B.', key: 'neuk...', mute_token: SECRET_TOKEN },
    view: { by: 'admin', label: 'Anna B.' } },
  { cat: 'keys',
    raw: { event: 'key_muted', by: 'Kurzbefehl', label: 'Anna B.', key: 'neuk...', source: 'shortcut', version: 5 },
    view: { by: 'Kurzbefehl', label: 'Anna B.', key: 'neuk...', source: 'shortcut' } },
  { cat: 'keys',
    raw: { event: 'key_unmuted', by: 'automatisch', label: 'Anna B.', key: 'neuk...', source: 'auto', version: 6 },
    view: { by: 'automatisch', label: 'Anna B.', key: 'neuk...', source: 'auto' } },
  { cat: 'keys',
    raw: { event: 'key_mute_volume', by: 'admin', label: 'Anna B.', key: 'neuk...', volume: '3' },
    view: { by: 'admin', label: 'Anna B.', volume: 3 } },
  { cat: 'keys',
    raw: { event: 'key_mute_permanent', by: 'admin', label: 'Anna B.', key: 'neuk...', source: 'dashboard', permanent: 1 },
    view: { by: 'admin', label: 'Anna B.', permanent: true, source: 'dashboard' } },
  { cat: 'keys',
    raw: { event: 'key_test', by: 'admin', label: 'Anna B.', key: 'neuk...', ok: 0, http: 500 },
    view: { by: 'admin', label: 'Anna B.', ok: false } },
  { cat: 'system', admin: true,
    raw: { event: 'notify_changed', by: 'admin', type: 'login_success', key: 'enabled', label: 'Anmeldung am Dashboard', option: 'Meldung', value: 'eingeschaltet' },
    view: { by: 'admin', label: 'Anmeldung am Dashboard', key: 'enabled', option: 'Meldung', value: 'eingeschaltet' } },
  { cat: 'system', admin: true,
    raw: { event: 'setting_changed', by: 'admin', label: 'Automatisch wieder laut nach', value: '4 Std' },
    view: { by: 'admin', label: 'Automatisch wieder laut nach', value: '4 Std' } },
  { cat: 'system',
    raw: { event: 'box_restart', reason: 'panic', device_id: 'fw-box-1', uptime_s: '12' },
    view: { reason: 'panic' } },
  // "demo" nur, wenn der Eintrag es trägt (Alt-Einträge haben es nicht).
  { cat: 'alarm', core: true,
    raw: { event: 'nas_alarm_started', by: 'admin', resend: 0 },
    view: { by: 'admin', resend: 0 } },
  { cat: 'alarm', core: true,
    raw: { event: 'false_alarm_started', by: 'admin', resend: '2', demo: true },
    view: { by: 'admin', resend: 2, demo: true } },
  // Die Einzelergebnisse (mit Klartext-Keys) bleiben im Verlauf.
  { cat: 'alarm', core: true,
    raw: { event: 'nas_alarm', by: 'admin', ok: true, message: 'Alarm an 1 von 1 Empfängern zugestellt.',
      results: [{ id: 1, label: 'Anna', key: SECRET_KEY, ok: true, http: 200, tries: 1 }], resend: 0, demo: false },
    view: { by: 'admin', ok: true, message: 'Alarm an 1 von 1 Empfängern zugestellt.', resend: 0, demo: false } },
  { cat: 'alarm', core: true,
    raw: { event: 'false_alarm', by: 'admin', message: 'Entwarnung an 0 von 1 Empfängern.',
      results: [{ id: 1, label: 'Anna', key: SECRET_KEY, ok: false, http: 500 }], resend: 0 },
    view: { by: 'admin', ok: false, message: 'Entwarnung an 0 von 1 Empfängern.', resend: 0 } },
  // Ohne source war es ein Relaisalarm (Einträge von vor der Unterscheidung).
  { cat: 'alarm', core: true,
    raw: { event: 'relay_alarm', info: '17.11. 10:05', device_id: 'fw-box-1' },
    view: { info: '17.11. 10:05', source: 'relay' } },
  { cat: 'system', raw: { event: 'box_offline', minutes: '20' }, view: { minutes: 20 } },
  { cat: 'system', raw: { event: 'box_recovered', minutes: 25 }, view: { minutes: 25 } },
  { cat: 'system', raw: { event: 'nas_web_recovered', minutes: 60 }, view: { minutes: 60 } },
  { cat: 'system', raw: { event: 'watcher_silent', minutes: 61 }, view: { minutes: 61 } },
  { cat: 'system', raw: { event: 'watcher_recovered' }, view: { minutes: 0 } },
  // Eine Zahl, wo Text erwartet wird, kommt als Text.
  { cat: 'system', raw: { event: 'nas_web_failed', detail: 500 }, view: { detail: '500' } },
];
// Ein Ereignis, das die Tabelle nicht kennt - es darf nie hinausgehen.
const UNKNOWN = { event: 'geheim_ereignis', key: SECRET_KEY, secret: 'nie zeigen' };

// Den Verlauf mit allen Ereignissen füllen (Zeitstempel aufsteigend, das
// unbekannte mittendrin). Liefert die Zeitstempel der Einträge.
function seedAll() {
  const start = now() - 3600;
  const lines = EVENTS.map((e, i) => ({ ...e.raw, log_at: start + i * 10 }));
  lines.splice(10, 0, { ...UNKNOWN, log_at: start + 95 });
  backend.data.appendLog(lines);
  return EVENTS.map((_, i) => start + i * 10);
}
// Was der Browser für diese Ereignisse bekommen muss, neueste zuerst.
const expected = (stamps, keep = () => true) => EVENTS
  .map((e, i) => ({ ...e, at: stamps[i] }))
  .filter(keep)
  .map((e) => ({ at: e.at, event: e.raw.event, ...e.view }))
  .reverse();
// Einfache Einträge mit fortlaufender Nummer im Namen (K1, K2 ...).
const keyAdded = (n, at) => ({ event: 'key_added', by: 'admin', label: `K${n}`, key: 'kkkk...', log_at: at });
const labels = (entries) => entries.map((e) => e.label);

describe('Ausgabe', () => {
  it('neueste zuerst, nur freigegebene Felder in festem Typ - nie das unbekannte Ereignis, nie die Einzelergebnisse', async () => {
    const stamps = seedAll();
    const res = await admin.get('log_view', { limit: 100, filter: 'all' });
    assert.equal(res.http, 200);
    assert.equal(res.ok, true);
    assert.deepEqual(res.entries, expected(stamps));
    const raw = JSON.stringify(res);
    assert.equal(raw.includes('geheim_ereignis'), false);
    assert.equal(raw.includes('results'), false);
    for (const secret of [SECRET_KEY, SECRET_TOKEN, PHONE, 'nie zeigen']) {
      assert.equal(raw.includes(secret), false, secret);
    }
  });

  it('die Lese-Rolle bekommt dasselbe ohne die Einträge nur für den Admin', async () => {
    const stamps = seedAll();
    const res = await reader.get('log_view', { limit: 100, filter: 'all' });
    assert.equal(res.ok, true);
    assert.deepEqual(res.entries, expected(stamps, (e) => !e.admin));
  });

  it('server_time liegt bei (für "vor 5 min" unabhängig von der Uhr des Browsers)', async () => {
    const res = await admin.get('log_view', { limit: 5 });
    assert.equal(res.ok, true);
    assert.deepEqual(res.entries, []);
    assert.ok(Number.isInteger(res.server_time));
    assert.ok(Math.abs(res.server_time - now()) <= 5, `server_time ${res.server_time}`);
  });
});

describe('Filter', () => {
  for (const filter of ['alarm', 'keys', 'system']) {
    it(`${filter}: nur die Einträge dieser Gruppe`, async () => {
      const stamps = seedAll();
      const res = await admin.get('log_view', { limit: 100, filter });
      assert.deepEqual(res.entries, expected(stamps, (e) => e.cat === filter));
    });
  }

  it('alarm_core: nur die Versand-Ereignisse (ohne die Befehle der Demo-Alarme)', async () => {
    const stamps = seedAll();
    const res = await admin.get('log_view', { limit: 100, filter: 'alarm_core' });
    assert.deepEqual(res.entries, expected(stamps, (e) => e.core));
  });

  it('ein unbekannter Filter zählt als "alles"', async () => {
    const stamps = seedAll();
    for (const filter of ['gibtesnicht', '', 'ALARM']) {
      const res = await admin.get('log_view', { limit: 100, filter });
      assert.deepEqual(res.entries, expected(stamps), `filter "${filter}"`);
    }
  });

  it('Befehle (created, late_ack ...) vom Typ ALARM gehören zu "alarm", andere zu "system"', async () => {
    const at = now() - 100;
    backend.data.appendLog([
      { event: 'created', command: { id: 1, type: 'ALARM', created_by: 'admin' }, log_at: at },
      { event: 'late_ack', id: 1, result: 'executed', type: 'ALARM', log_at: at + 1 },
      { event: 'created', command: { id: 2, type: 'TEST', created_by: 'admin' }, log_at: at + 2 },
      { event: 'late_ack', id: 2, result: 'executed', type: 'TEST', log_at: at + 3 },
    ]);
    const ids = async (filter) => (await admin.log(filter)).map((e) => `${e.event}:${e.id}`);
    assert.deepEqual(await ids('alarm'), ['late_ack:1', 'created:1']);
    assert.deepEqual(await ids('system'), ['late_ack:2', 'created:2']);
    assert.deepEqual(await ids('alarm_core'), []);
  });

  it('die Lese-Rolle: Einträge nur für den Admin fallen VOR dem Abzählen heraus', async () => {
    const at = now() - 1000;
    backend.data.appendLog([
      ...Array.from({ length: 5 }, (_, i) => keyAdded(i + 1, at + i)),
      ...Array.from({ length: 30 }, (_, i) => ({ event: 'setting_changed', by: 'admin', label: 'x', value: `${i}`, log_at: at + 10 + i })),
    ]);
    for (const filter of ['all', 'system']) {
      const res = await reader.get('log_view', { limit: 5, filter });
      assert.equal(res.entries.some((e) => e.event === 'setting_changed'), false, filter);
    }
    assert.deepEqual(labels((await reader.get('log_view', { limit: 5, filter: 'all' })).entries),
      ['K5', 'K4', 'K3', 'K2', 'K1']);
  });
});

describe('Menge', () => {
  it('limit gilt von 1 bis 100', async () => {
    const at = now() - 1000;
    backend.data.appendLog(Array.from({ length: 120 }, (_, i) => keyAdded(i + 1, at + i)));
    const count = async (limit, filter = 'all') => (await admin.get('log_view', { limit, filter })).entries.length;
    assert.equal(await count(7), 7);
    assert.equal(await count(0), 1);
    assert.equal(await count(-5), 1);
    assert.equal(await count(1000), 100);
    assert.equal(await count(1000, 'keys'), 100);
    assert.deepEqual(labels((await admin.get('log_view', { limit: 3 })).entries), ['K120', 'K119', 'K118']);
  });

  it('reicht die aktuelle Datei nicht, kommen die Einträge der rotierten Datei commands.log.1 dazu', async () => {
    const at = now() - 1000;
    backend.data.appendLog([keyAdded(1, at), keyAdded(2, at + 1), keyAdded(3, at + 2)], 'commands.log.1');
    backend.data.appendLog([keyAdded(4, at + 3), keyAdded(5, at + 4)]);
    for (const filter of ['all', 'keys']) {
      assert.deepEqual(labels((await admin.get('log_view', { limit: 5, filter })).entries),
        ['K5', 'K4', 'K3', 'K2', 'K1'], filter);
      assert.deepEqual(labels((await admin.get('log_view', { limit: 4, filter })).entries),
        ['K5', 'K4', 'K3', 'K2'], filter);
      assert.deepEqual(labels((await admin.get('log_view', { limit: 2, filter })).entries),
        ['K5', 'K4'], filter);
    }
    assert.deepEqual(labels((await reader.get('log_view', { limit: 5, filter: 'all' })).entries),
      ['K5', 'K4', 'K3', 'K2', 'K1'], 'Lese-Rolle');
  });
});
