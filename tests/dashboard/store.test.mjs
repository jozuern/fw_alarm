// Tests für nas_dashboard/assets/app/store.js (FW.store): Zustand, Polling,
// Bestätigungen, Alarm-Versand. Ohne Preact-Bundle: fetch, Timer, Uhr,
// localStorage, document und location sind Attrappen.
import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import { loadScripts, plain } from './helpers.mjs';

const FILES = ['nas_dashboard/assets/app/logic.js', 'nas_dashboard/assets/app/api.js', 'nas_dashboard/assets/app/store.js'];
const T0 = 1800000000;   // Server-Zeit (s) der Attrappen

function reply(status, body) {
  return {
    status,
    ok: status >= 200 && status < 300,
    text: async () => (typeof body === 'string' ? body : JSON.stringify(body))
  };
}

class FakeFormData {
  constructor() { this.map = new Map(); }
  set(key, value) { this.map.set(key, String(value)); }
  get(key) { return this.map.has(key) ? this.map.get(key) : null; }
  has(key) { return this.map.has(key); }
}

function memoryStorage(initial = {}) {
  const data = new Map(Object.entries(initial));
  return {
    getItem: (k) => (data.has(k) ? data.get(k) : null),
    setItem: (k, v) => { data.set(k, String(v)); },
    removeItem: (k) => { data.delete(k); },
    has: (k) => data.has(k)
  };
}

function stubDocument() {
  const listeners = {};
  const inputs = [{ value: 'start' }];
  return {
    hidden: false,
    inputs,
    addEventListener(type, fn) { (listeners[type] = listeners[type] || []).push(fn); },
    fire(type) { (listeners[type] || []).forEach((fn) => fn({ type })); },
    querySelectorAll: (sel) => (sel === 'form input[name="csrf"]' ? inputs : [])
  };
}

// Timer, die nur beim Vorspulen laufen - zusammen mit der gefälschten Uhr.
function fakeTimers(clock) {
  let seq = 0;
  const timers = new Map();
  const add = (fn, ms, repeat) => {
    seq += 1;
    timers.set(seq, { fn, at: clock.now + Math.max(0, ms || 0), ms: Math.max(1, ms || 0), repeat });
    return seq;
  };
  return {
    setTimeout: (fn, ms) => add(fn, ms, false),
    clearTimeout: (id) => { timers.delete(id); },
    setInterval: (fn, ms) => add(fn, ms, true),
    clearInterval: (id) => { timers.delete(id); },
    advance(ms) {
      const end = clock.now + ms;
      for (;;) {
        let next = null;
        timers.forEach((t, id) => { if (t.at <= end && (!next || t.at < next.t.at)) next = { id, t }; });
        if (!next) break;
        clock.now = next.t.at;
        if (next.t.repeat) next.t.at += next.t.ms;
        else timers.delete(next.id);
        next.t.fn();
      }
      clock.now = end;
    },
    intervals: () => [...timers.values()].filter((t) => t.repeat).map((t) => t.ms).sort((a, b) => a - b)
  };
}

const settle = async () => { for (let i = 0; i < 30; i += 1) await new Promise((r) => setImmediate(r)); };

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

const statusBody = (over = {}, statusOver = {}) => Object.assign({
  ok: true,
  status: Object.assign({
    relay: 'open', wifi: 'up', rssi: '-60', uptime_s: '3600', keys_version: '5', keys_count: '2',
    last_alarm: 'nie', alarm_pending: '0', alarm_seq: '0', seen_at: T0
  }, statusOver),
  seen_age_seconds: 3,
  offline: false,
  command: null,
  demo: { enabled: false },
  nas_keys_version: 5,
  demo_sync_ok: false,
  device_hint: '',
  timezone: 'Europe/Berlin',
  csrf: 'tok-neu',
  watchdog: { configured: true, last_run: T0 },
  server_time: T0
}, over);

const keysBody = (over = {}) => Object.assign({
  ok: true,
  version: 5,
  keys: [
    { id: 1, label: 'Josh', key_masked: 'jo…01', muted: false, mute_volume: 0, phone: '' },
    { id: 2, label: 'Tom', key_masked: 'fa…01', muted: false, mute_volume: 0, phone: '+49 171 1' }
  ],
  demo: { enabled: false },
  mute_max_seconds: 43200
}, over);

const partialResult = {
  ok: false,
  message: '1 von 2 erreicht.',
  results: [{ id: 1, label: 'Josh', ok: true, http: 200 }, { id: 2, label: 'Tom', ok: false, http: 0 }],
  started_at: T0
};

function harness(opts = {}) {
  const clock = { now: T0 * 1000 };
  const timers = fakeTimers(clock);
  const storage = opts.storage || memoryStorage();
  const doc = stubDocument();
  const reloads = { count: 0 };
  const requests = [];
  const routes = {
    status: () => reply(200, statusBody()),
    keys_list: () => reply(200, keysBody()),
    log_view: (f) => reply(200, { ok: true, entries: [], server_time: T0, filter: f.filter }),
    notify_list: () => reply(200, { ok: true, configured: true, quiet_now: false, global: [], types: [] }),
    settings_list: () => reply(200, { ok: true, settings: { mute_max_seconds: { value: 43200, default: 43200, choices: [] } } })
  };
  const fetch = (url, o) => {
    const isPost = !!(o && o.method === 'POST');
    let fields;
    if (isPost) {
      fields = Object.fromEntries(o.body.map);
    } else {
      fields = {};
      url.split('?')[1].split('&').forEach((p) => { const [k, v] = p.split('='); fields[k] = decodeURIComponent(v); });
    }
    requests.push({ action: fields.action, method: isPost ? 'POST' : 'GET', fields, headers: (o && o.headers) || {} });
    return new Promise((resolve) => {
      const handler = routes[fields.action];
      resolve(handler ? handler(fields) : reply(400, { ok: false, message: 'Unbekannte Aktion.' }));
    });
  };
  // Fenster-Ereignisse (pageshow, focus) - wie document.fire().
  const winListeners = {};
  const ctx = loadScripts(FILES, Object.assign({
    fetch,
    AbortSignal: { timeout: () => ({}) },
    FormData: FakeFormData,
    localStorage: storage,
    document: doc,
    location: { reload() { reloads.count += 1; } },
    htmPreact: { useState() {}, useEffect() {}, useRef() {} },
    addEventListener(type, fn) { (winListeners[type] = winListeners[type] || []).push(fn); },
    __clock: clock
  }, timers));
  vm.runInContext('Date.now = function () { return __clock.now; };', ctx);
  const store = ctx.FW.store;
  return {
    ctx, store, clock, timers, storage, doc, reloads, requests, routes, logic: ctx.FW.logic,
    fireWin: (type) => (winListeners[type] || []).forEach((fn) => fn({ type })),
    s: () => store.getState(),
    d: () => store.derive(),
    posts: (action) => requests.filter((r) => r.method === 'POST' && (!action || r.action === action)),
    gets: (action, filter) => requests.filter((r) => r.method === 'GET' && r.action === action
      && (filter === undefined || r.fields.filter === filter))
  };
}

const ADMIN = { role: 'admin', user: 'admin', csrf: 'tok-alt', publicBase: '', muteMax: 43200 };

test('derive: loading vor dem ersten Status, danach Urteil', async () => {
  const h = harness();
  h.store.init(ADMIN);
  assert.equal(h.d().verdict.key, 'loading');
  await h.store.refreshStatus();
  await h.store.refreshKeys();
  await h.store.refreshAlarmInfo();
  const d = h.d();
  assert.equal(d.verdict.key, 'ok');
  assert.deepEqual(plain(d.hints), []);
  assert.deepEqual(plain(d.overviewRows.map((r) => r.id)), ['box', 'wifi', 'watch', 'keys', 'alarm']);
  assert.equal(d.keysSummary.total, 2);
  assert.equal(d.alarmFact.text, 'keiner aufgezeichnet');
  assert.equal(d.commandLine, null);
  assert.equal(d.boxRows.find((r) => r.id === 'wifi').tone, '');
  // Störung: Status-Fehler UND "veraltet" gehen beide in die Ableitungen.
  h.routes.status = () => { throw new TypeError('Failed to fetch'); };
  await h.store.refreshStatus();
  assert.equal(h.d().verdict.key, 'nas');
  assert.equal(h.d().verdict.title, 'NAS nicht erreichbar');
  assert.equal(h.d().boxRows.find((r) => r.id === 'wifi').tone, 'unknown');
  assert.equal(h.s().failStreak, 1);
});

test('Status-Übernahme: CSRF, Zeitzone, Server-Versatz, device_hint', async () => {
  const h = harness();
  h.store.init(ADMIN);
  h.routes.status = () => reply(200, statusBody({ server_time: T0 + 120, device_hint: '  zwei Boxen  ', timezone: 'Europe/London' }));
  await h.store.refreshStatus();
  const s = h.s();
  assert.equal(s.deviceHint, 'zwei Boxen');
  assert.ok(Math.abs(s.serverOffset - 120) < 0.001);
  assert.equal(h.logic.tz(), 'Europe/London');
  assert.equal(h.doc.inputs[0].value, 'tok-neu');
  assert.equal(h.ctx.FW.api.getCsrf(), 'tok-neu');
  await h.store.keyMute(1, true);
  assert.equal(h.posts('keys_mute')[0].headers['X-CSRF-Token'], 'tok-neu');
  assert.equal(s.statusData.server_time, T0 + 120);
  assert.equal(s.demoSyncOk, false);
  assert.equal(s.demoSyncFor, '0');
  assert.ok(h.d().hints.some((x) => x.text === 'zwei Boxen'));
});

test('Listen-Abgleich: Messung je Versionspaar, Hinweis nach 60 s', async () => {
  const h = harness();
  h.store.init(ADMIN);
  const push = async (boxVersion, nasVersion, over = {}) => {
    h.routes.status = () => reply(200, statusBody(Object.assign({ nas_keys_version: nasVersion }, over), { keys_version: boxVersion }));
    await h.store.refreshStatus();
  };
  await push('4', 5);
  const since = h.s().mismatchSince;
  assert.equal(since, h.clock.now);
  h.clock.now += 30000;
  await push('4', 5);
  assert.equal(h.s().mismatchSince, since);           // gleiches Paar: läuft weiter
  h.clock.now += 40000;
  await push('4', 5);
  assert.equal(h.s().mismatchSince, since);
  assert.ok(h.d().hints.some((x) => /seit 1 Min nicht übernommen/.test(x.text)));
  await push('4', 6);
  assert.equal(h.s().mismatchSince, h.clock.now);     // neues Paar: von vorn
  await push('6', 6);
  assert.equal(h.s().mismatchSince, 0);
  await push('4', 6, { offline: true });
  assert.equal(h.s().mismatchSince, 0);               // offline: keine Messung
});

test('neuer Alarm erkannt → Alarm-Info und Verlauf sofort nachladen', async () => {
  const h = harness();
  h.store.init(ADMIN);
  h.store.setRoute('verlauf');
  await h.store.refreshStatus();
  await settle();
  const before = { info: h.gets('log_view', 'alarm_core').length, log: h.gets('log_view', 'all').length };
  await h.store.refreshStatus();
  await settle();
  assert.equal(h.gets('log_view', 'alarm_core').length, before.info);   // nichts geändert
  h.routes.status = () => reply(200, statusBody({}, { alarm_pending: '1', alarm_seq: '1' }));
  await h.store.refreshStatus();
  await settle();
  assert.equal(h.gets('log_view', 'alarm_core').length, before.info + 1);
  assert.equal(h.gets('log_view', 'all').length, before.log + 1);
});

test('veraltet nach 15 s ohne Antwort (Sekundentakt)', async () => {
  const h = harness();
  h.store.init(ADMIN);
  h.store.start();
  await settle();
  assert.equal(h.s().stale, false);
  h.routes.status = () => new Promise(() => {});   // NAS antwortet nicht mehr
  h.timers.advance(15000);
  await settle();
  assert.equal(h.s().stale, false);
  h.timers.advance(1000);
  await settle();
  assert.equal(h.s().stale, true);
  assert.equal(h.s().nowMs, h.clock.now);
  assert.equal(h.d().verdict.key, 'nas');
  assert.equal(h.d().verdict.title, 'Status veraltet');
  h.store.stop();
});

test('confirm closes on mode change and sends nothing', async () => {
  const h = harness();
  h.store.init(ADMIN);
  await h.store.refreshStatus();
  let fired = 0;
  const id = h.store.openConfirm({ title: 'Alarm an alle senden?', text: 'LIVE. Alarm an 2 Empfänger.', confirmLabel: 'Ja', hold: true, expectDemo: '0', onConfirm: () => { fired += 1; } });
  assert.ok(id);
  assert.equal(h.s().confirm.expectDemo, '0');
  assert.equal(h.s().confirm.hold, true);
  // Eine Live-Aktualisierung OHNE Moduswechsel lässt die Bestätigung stehen.
  await h.store.refreshStatus();
  assert.equal(h.s().confirm.id, id);
  h.routes.status = () => reply(200, statusBody({ demo: { enabled: true, key_id: 2, label: 'Tom' } }));
  await h.store.refreshStatus();
  const s = h.s();
  assert.equal(s.confirm, null);
  assert.equal(s.result.phase, 'note');
  assert.equal(s.result.tone, 'warn');
  assert.match(s.result.head, /^Der Demo-\/LIVE-Modus hat sich gerade geändert – Bestätigung zurückgesetzt\./);
  assert.equal(await h.store.confirmNow(id), null);
  assert.equal(fired, 0);
  assert.equal(h.posts().length, 0);
});

test('Moduswechsel über die Empfängerliste schließt ebenfalls; andere Bestätigungen bleiben', async () => {
  const h = harness();
  h.store.init(ADMIN);
  await h.store.refreshStatus();
  h.store.openConfirm({ title: 'Löschen?', text: 'Tom löschen?', confirmLabel: 'Ja', hold: false, expectDemo: null, onConfirm: () => {} });
  h.routes.keys_list = () => reply(200, keysBody({ demo: { enabled: true, key_id: 1, label: 'Josh' } }));
  await h.store.refreshKeys();
  assert.notEqual(h.s().confirm, null);   // keine Alarm-Bestätigung: bleibt
  h.store.closeConfirm();
  h.store.openConfirm({ title: 'A', text: 'Demo-Alarm an Josh?', hold: true, expectDemo: '1', onConfirm: () => {} });
  h.routes.keys_list = () => reply(200, keysBody({ demo: { enabled: false } }));
  await h.store.refreshKeys();
  assert.equal(h.s().confirm, null);
  // Eine Frage aus einem veralteten Stand wird gar nicht erst geöffnet.
  assert.equal(h.store.openConfirm({ title: 'A', text: 'x', hold: true, expectDemo: '1', onConfirm: () => {} }), null);
  assert.equal(h.s().confirm, null);
});

test('confirmNow after deadline does not fire', async () => {
  const h = harness();
  h.store.init(ADMIN);
  const text = 'Alarm an alle senden?';
  const secs = h.logic.armTimeoutFor(text);
  let fired = 0;
  h.store.openConfirm({ title: 'A', text, hold: true, expectDemo: '0', onConfirm: () => { fired += 1; } });
  assert.equal(h.s().confirm.deadline, h.clock.now + secs * 1000);
  h.clock.now += secs * 1000;     // Timer liefen nicht (iOS im Hintergrund)
  assert.equal(await h.store.confirmNow(), null);
  assert.equal(fired, 0);
  assert.equal(h.s().confirm, null);
  // Gegenprobe: knapp vor der Frist löst es aus.
  const id = h.store.openConfirm({ title: 'A', text, hold: true, expectDemo: '0', onConfirm: () => { fired += 1; return 'gesendet'; } });
  h.clock.now += secs * 1000 - 1;
  assert.equal(await h.store.confirmNow(id + 1), null);   // falsche id: nichts
  assert.equal(fired, 0);
  assert.equal(await h.store.confirmNow(id), 'gesendet');
  assert.equal(fired, 1);
  // Ohne Zutun schließt die Frist die Bestätigung.
  h.store.openConfirm({ title: 'A', text, hold: false, expectDemo: null, onConfirm: () => {} });
  h.timers.advance(secs * 1000);
  assert.equal(h.s().confirm, null);
});

test('höchstens eine offene Bestätigung, closeConfirm mit fremder id schließt nichts', () => {
  const h = harness();
  h.store.init(ADMIN);
  const a = h.store.openConfirm({ title: 'A', text: 'a', onConfirm: () => {} });
  const b = h.store.openConfirm({ title: 'B', text: 'b', onConfirm: () => {} });
  assert.notEqual(a, b);
  assert.equal(h.s().confirm.title, 'B');
  h.store.closeConfirm(a);
  assert.equal(h.s().confirm.id, b);
  h.store.closeConfirm();
  assert.equal(h.s().confirm, null);
});

test('sendNasAlarm: Teilausfall → Ergebnis, nicht Erreichte zuerst, Nachsendung nur an sie', async () => {
  const h = harness();
  h.store.init(ADMIN);
  await h.store.refreshStatus();
  await h.store.refreshKeys();
  const gate = deferred();
  h.routes.alarm = () => gate.promise;
  const sending = h.store.sendNasAlarm('alarm', '0');
  await settle();
  assert.equal(h.s().alarmInFlight, true);
  assert.equal(h.s().result.phase, 'sending');
  assert.equal(h.s().result.head, 'REAL ALARM wird gesendet… (kann bis zu einer Minute dauern – Seite nicht schließen)');
  assert.equal(h.posts('alarm')[0].fields.expect_demo, '0');
  assert.equal(h.posts('alarm')[0].fields.only_ids, undefined);
  gate.resolve(reply(200, partialResult));
  const r = await sending;
  assert.equal(r.ok, false);
  const s = h.s();
  assert.equal(s.alarmInFlight, false);
  assert.equal(s.busyCount, 0);
  assert.equal(s.result.phase, 'done');
  assert.equal(s.result.tone, 'fail');
  assert.match(s.result.head, /^\d\d:\d\d:\d\d Uhr, REAL ALARM: 1 von 2 erreicht\.$/);
  assert.equal(s.result.fresh, true);
  assert.equal(s.result.resend, false);
  assert.deepEqual(plain(s.result.reached), ['Josh']);
  assert.equal(s.result.total, 2);
  assert.deepEqual(plain(s.unreached.entries), [{ id: 2, name: 'Tom', reason: 'keine Verbindung/Timeout' }]);
  assert.equal(s.unreached.startedAt, T0);
  assert.deepEqual(plain(h.d().retry), { ids: [2], names: ['Tom'], label: 'Nur an Tom erneut senden' });

  // Nachsendung über die Bestätigung des Stores (eine Momentaufnahme).
  const id = h.store.confirmResend();
  assert.ok(id);
  const c = h.s().confirm;
  assert.equal(c.tag, 'resend');
  assert.equal(c.expectDemo, '0');
  assert.equal(c.hold, true);
  assert.match(c.text, /NUR an: Tom\?/);
  h.routes.alarm = () => reply(200, { ok: true, message: 'Gesendet.', results: [{ id: 2, label: 'Tom', ok: true, http: 200 }], started_at: T0 + 30 });
  await h.store.confirmNow(id);
  const resend = h.posts('alarm')[1].fields;
  assert.equal(resend.only_ids, '2');
  assert.equal(resend.orig_at, String(T0));
  assert.equal(resend.expect_demo, '0');
  assert.equal(h.s().unreached, null);
  assert.equal(h.s().result.resend, true);
  assert.equal(h.s().result.tone, 'ok');
  assert.equal(h.d().retry, null);
  assert.equal(h.storage.has('fwAlarm.lastUnreached'), false);
});

test('sendNasAlarm network error → unclear, no retry offer', async () => {
  const h = harness();
  h.store.init(ADMIN);
  h.routes.alarm = () => reply(200, partialResult);
  await h.store.sendNasAlarm('alarm', '0');
  h.routes.alarm = () => { throw new TypeError('Failed to fetch'); };
  const r = await h.store.sendNasAlarm('alarm', '0', [2], T0);
  assert.equal(r.ok, false);
  assert.equal(r.kind, 'warn');
  const s = h.s();
  assert.equal(s.result.phase, 'note');
  assert.equal(s.result.tone, 'warn');
  assert.equal(s.result.head, 'Antwort unklar (Verbindung abgebrochen) – REAL ALARM wurde evtl. gesendet. Erst Verlauf prüfen, nicht sofort erneut auslösen!');
  assert.equal(s.result.offerRetry, false);
  assert.equal(h.d().retry, null);
  assert.deepEqual(plain(s.unreached.entries), [{ id: 2, name: 'Tom', reason: 'keine Verbindung/Timeout' }]);   // gemerkte Liste bleibt
  assert.equal(h.posts('alarm').length, 2);   // kein automatischer Neuversuch
  // Verlauf und "Letzter Alarm" nach 5 s und 40 s nachladen.
  h.store.setRoute('verlauf');
  await settle();
  const n = h.gets('log_view', 'alarm_core').length;
  h.timers.advance(5000);
  await settle();
  assert.equal(h.gets('log_view', 'alarm_core').length, n + 1);
  h.timers.advance(35000);
  await settle();
  assert.equal(h.gets('log_view', 'alarm_core').length, n + 2);
  assert.equal(h.posts('alarm').length, 2);
});

test('sendNasAlarm: Zeitlimit und 5xx sind unklar, 4xx sicher nicht gesendet (Nachsendung wieder angeboten)', async () => {
  const h = harness();
  h.store.init(ADMIN);
  h.routes.alarm = () => reply(200, partialResult);
  await h.store.sendNasAlarm('alarm', '0');
  h.routes.alarm = () => { const e = new Error('t'); e.name = 'TimeoutError'; throw e; };
  await h.store.sendNasAlarm('alarm', '0');
  assert.equal(h.s().result.head, 'Antwort unklar (Zeitüberschreitung) – REAL ALARM wurde evtl. gesendet. Erst Verlauf prüfen, nicht sofort erneut auslösen!');
  h.routes.false_alarm = () => reply(502, '<h1>Bad Gateway</h1>');
  await h.store.sendNasAlarm('false_alarm', '0');
  assert.equal(h.s().result.head, 'Antwort unklar (HTTP 502: Bad Gateway) – ENTWARNUNG wurde evtl. gesendet. Erst Verlauf prüfen, nicht sofort erneut auslösen!');
  h.routes.alarm = () => reply(400, { ok: false, message: 'Ungültige Empfänger-Auswahl für die Nachsendung – es wurde NICHTS gesendet.' });
  const r = await h.store.sendNasAlarm('alarm', '0', [2], T0);
  assert.equal(r.kind, 'error');
  assert.equal(h.s().result.tone, 'fail');
  assert.equal(h.s().result.head, 'REAL ALARM NICHT gesendet – Ungültige Empfänger-Auswahl für die Nachsendung – es wurde NICHTS gesendet.');
  assert.equal(h.s().result.offerRetry, true);
  assert.equal(h.d().retry.label, 'Nur an Tom erneut senden');
  // Ein Versand OHNE Ergebnisliste ("nichts gesendet") lässt die Liste stehen.
  h.routes.alarm = () => reply(200, { ok: false, message: 'Keiner der gewählten Empfänger ist noch in der Liste.' });
  await h.store.sendNasAlarm('alarm', '0', [2], T0);
  assert.equal(h.s().result.phase, 'done');
  assert.equal(h.s().result.fresh, false);
  assert.equal(h.s().unreached.entries.length, 1);
});

test('409 mode_changed: Stand übernehmen, nichts gesendet, kein Neuversuch', async () => {
  const h = harness();
  h.store.init(ADMIN);
  await h.store.refreshStatus();
  const demo = { enabled: true, key_id: 2, label: 'Tom' };
  h.routes.status = () => reply(200, statusBody({ demo }));   // passend zum neuen Stand
  h.routes.alarm = () => reply(409, { ok: false, code: 'mode_changed', message: 'Der Modus wurde inzwischen geändert (jetzt DEMO – nur an „Tom“). Es wurde NICHTS gesendet – bitte neu bestätigen.', demo });
  const r = await h.store.sendNasAlarm('alarm', '0');
  assert.equal(r.ok, false);
  assert.equal(h.s().demo.key_id, 2);
  assert.equal(h.s().result.tone, 'fail');
  assert.match(h.s().result.head, /NICHTS gesendet/);
  assert.equal(h.posts('alarm').length, 1);
  // Auch der Demo-Alarm über die Box übernimmt den Stand.
  await settle();
  h.routes.status = () => reply(200, statusBody());
  h.routes.enqueue = () => reply(409, { ok: false, code: 'mode_changed', message: 'x', demo: { enabled: false } });
  const e = await h.store.enqueueBoxAlarm('2');
  assert.equal(e.ok, false);
  assert.equal(h.s().demo.enabled, false);
  assert.equal(h.posts('enqueue')[0].fields.expect_demo, '2');
});

test('CSRF-Fehler: Status neu holen, kein Neuversuch, Text je nach Server', async () => {
  const h = harness();
  h.store.init(ADMIN);
  h.routes.keys_mute = () => reply(403, { ok: false });
  const old = await h.store.keyMute(1, true);
  assert.equal(old.text, 'Sicherheits-Token veraltet – Seite neu laden. Aktion wurde NICHT ausgeführt.');
  await h.store.refreshStatus();   // liefert csrf mit
  const statusGets = h.gets('status').length;
  h.routes.alarm = () => reply(403, { ok: false, code: 'csrf', message: 'Token' });
  const r = await h.store.sendNasAlarm('alarm', '0');
  await settle();
  assert.equal(r.kind, 'error');
  assert.equal(h.s().result.head, 'REAL ALARM NICHT gesendet – Sicherheits-Token erneuert – bitte erneut bestätigen.');
  assert.equal(h.posts('alarm').length, 1);
  assert.ok(h.gets('status').length >= statusGets + 1);
});

test('sendNasAlarm 401 while in flight → no reload, result kept', async () => {
  const h = harness();
  h.store.init(ADMIN);
  h.store.start();
  await settle();
  const gate = deferred();
  h.routes.alarm = () => gate.promise;
  const sending = h.store.sendNasAlarm('alarm', '0');
  await settle();
  // Während der Versand läuft, läuft die Sitzung ab (Status-Abfrage: 401).
  h.routes.status = () => reply(401, { ok: false });
  h.timers.advance(5000);
  await settle();
  assert.equal(h.s().authLost, true);
  assert.deepEqual(h.timers.intervals(), []);   // Abfragen gestoppt
  h.timers.advance(3000);
  assert.equal(h.reloads.count, 0);
  gate.resolve(reply(200, partialResult));
  await sending;
  await settle();
  h.timers.advance(10000);
  await settle();
  assert.equal(h.reloads.count, 0);
  const s = h.s();
  assert.equal(s.result.phase, 'done');
  assert.equal(s.unreached.entries[0].name, 'Tom');
  const v = h.d().verdict;
  assert.equal(v.key, 'auth');
  assert.match(v.sub, /nach dem Lesen des Alarm-Ergebnisses/);
});

test('401 auf den Alarm selbst: NICHT gesendet, kein Neuladen', async () => {
  const h = harness();
  h.store.init(ADMIN);
  h.routes.false_alarm = () => reply(401, { ok: false });
  const r = await h.store.sendNasAlarm('false_alarm', '0');
  assert.equal(r.ok, false);
  assert.equal(h.s().result.head, 'ENTWARNUNG NICHT gesendet – Sitzung abgelaufen, bitte neu anmelden.');
  h.timers.advance(5000);
  assert.equal(h.reloads.count, 0);
  assert.match(h.d().verdict.sub, /nach dem Lesen des Alarm-Ergebnisses/);
});

test('401 ohne laufenden Alarm → nach 1,5 s neu laden', async () => {
  const h = harness();
  h.store.init(ADMIN);
  h.store.start();
  await settle();
  h.routes.keys_list = () => reply(401, { ok: false });
  await h.store.refreshKeys();
  assert.equal(h.s().authLost, true);
  assert.equal(h.d().verdict.sub, 'Sitzung abgelaufen – die Anmeldeseite wird geladen…');
  assert.deepEqual(h.timers.intervals(), []);
  h.timers.advance(1499);
  assert.equal(h.reloads.count, 0);
  h.timers.advance(1);
  assert.equal(h.reloads.count, 1);
  // Nach der Abmeldung startet nichts mehr.
  h.store.start();
  assert.deepEqual(h.timers.intervals(), []);
});

test('unreached persisted in localStorage and restored', async () => {
  const storage = memoryStorage();
  const h = harness({ storage });
  h.store.init(ADMIN);
  h.routes.alarm = () => reply(200, partialResult);
  await h.store.sendNasAlarm('alarm', '0');
  const saved = JSON.parse(storage.getItem('fwAlarm.lastUnreached'));
  assert.deepEqual(saved.entries, [{ id: 2, name: 'Tom', reason: 'keine Verbindung/Timeout' }]);
  assert.equal(saved.action, 'alarm');
  assert.equal(saved.startedAt, T0);

  // "Neu laden": frischer Store mit demselben Speicher.
  const h2 = harness({ storage });
  h2.store.init(ADMIN);
  const s = h2.s();
  assert.deepEqual(plain(s.unreached.entries), saved.entries);
  assert.equal(s.unreached.what, 'REAL ALARM');
  assert.equal(s.result.phase, 'note');
  assert.equal(s.result.tone, 'warn');
  assert.equal(s.result.offerRetry, true);
  assert.equal(h2.d().retry.label, 'Nur an Tom erneut senden');

  // Ausblenden löscht auch den gemerkten Stand.
  h2.store.clearResult();
  assert.equal(h2.s().result, null);
  assert.equal(h2.s().unreached, null);
  assert.equal(storage.has('fwAlarm.lastUnreached'), false);
});

test('gemerkte nicht Erreichte: zu alt oder kaputt → verworfen; Speicher gesperrt → kein Absturz', () => {
  const old = { action: 'alarm', at: T0 * 1000 - 13 * 3600 * 1000, startedAt: 0, entries: [{ id: 2, name: 'Tom', reason: 'HTTP 500' }] };
  const storage = memoryStorage({ 'fwAlarm.lastUnreached': JSON.stringify(old) });
  const h = harness({ storage });
  h.store.init(ADMIN);
  assert.equal(h.s().unreached, null);
  assert.equal(h.s().result, null);
  assert.equal(storage.has('fwAlarm.lastUnreached'), false);
  const broken = memoryStorage({ 'fwAlarm.lastUnreached': '{kaputt' });
  const h2 = harness({ storage: broken });
  h2.store.init(ADMIN);
  assert.equal(h2.s().unreached, null);
  const locked = { getItem() { throw new Error('SecurityError'); }, setItem() { throw new Error('x'); }, removeItem() { throw new Error('x'); } };
  const h3 = harness({ storage: locked });
  h3.store.init(ADMIN);
  assert.equal(h3.s().unreached, null);
  h3.store.setBrowserAlert(true);
  assert.equal(h3.s().browserAlert, true);
});

test('Sperren: kein zweiter Alarm während einer läuft; andere Aktionen sperren den Alarm nicht', async () => {
  const h = harness();
  h.store.init(ADMIN);
  await h.store.refreshStatus();
  const muteGate = deferred();
  h.routes.keys_mute = () => muteGate.promise;
  const muting = h.store.keyMute(1, true);
  await settle();
  assert.equal(h.s().busyCount, 1);
  // Doppelklick-Schutz für TEST & Co.: läuft etwas, wird nichts angelegt.
  const t = await h.store.enqueueTest();
  assert.equal(t.ok, false);
  assert.equal(h.posts('enqueue').length, 0);
  assert.equal((await h.store.cancelCommand()).ok, false);
  assert.equal((await h.store.keyAdd('Lena', 'lena0000001', '')).ok, false);
  assert.equal(h.posts('keys_add').length, 0);
  // ... der Alarm aber läuft trotzdem.
  const alarmGate = deferred();
  h.routes.alarm = () => alarmGate.promise;
  const sending = h.store.sendNasAlarm('alarm', '0');
  await settle();
  assert.equal(h.posts('alarm').length, 1);
  const second = await h.store.sendNasAlarm('alarm', '0');
  assert.equal(second.ok, false);
  assert.equal(h.posts('alarm').length, 1);
  assert.equal(h.store.confirmAlarm('alarm'), null);
  assert.equal(h.store.confirmResend(), null);
  muteGate.resolve(reply(200, { ok: true, message: 'Stumm.' }));
  await muting;
  assert.equal(h.s().alarmInFlight, true);   // das Ende der kürzeren Aktion gibt nichts frei
  alarmGate.resolve(reply(200, { ok: true, message: 'Gesendet.', results: [{ id: 1, label: 'Josh', ok: true, http: 200 }] }));
  await sending;
  assert.equal(h.s().alarmInFlight, false);
  assert.equal(h.s().busyCount, 0);
});

test('Test-Push und Probe-Meldung: höchstens einer gleichzeitig', async () => {
  const h = harness();
  h.store.init(ADMIN);
  const gate = deferred();
  h.routes.keys_test = () => gate.promise;
  const first = h.store.keyTest(2);
  await settle();
  assert.equal(h.s().keyTestInFlight, true);
  assert.equal(h.s().keyTestId, '2');
  assert.equal((await h.store.keyTest(1)).ok, false);
  assert.equal(h.posts('keys_test').length, 1);
  gate.resolve(reply(200, { ok: true, message: 'Test-Push an „Tom“ gesendet.' }));
  const r = await first;
  assert.deepEqual(plain(r), { ok: true, text: 'Test-Push an „Tom“ gesendet.', kind: 'ok' });
  assert.equal(h.s().keyTestInFlight, false);
  const probe = deferred();
  h.routes.notify_test = () => probe.promise;
  const p1 = h.store.notifyTest('box_offline');
  await settle();
  assert.equal(h.s().probeInFlight, true);
  assert.equal(h.s().probeType, 'box_offline');
  assert.equal((await h.store.notifyTest('nas_web')).ok, false);
  probe.resolve(reply(200, { ok: false, message: 'Probe FEHLGESCHLAGEN' }));
  assert.deepEqual(plain(await p1), { ok: false, text: 'Probe FEHLGESCHLAGEN', kind: 'error' });
  assert.equal(h.s().probeInFlight, false);
});

test('confirmAlarm: Frage und expect_demo aus einer Momentaufnahme', async () => {
  const h = harness();
  h.store.init(ADMIN);
  const tom = { enabled: true, key_id: 2, label: 'Tom' };
  h.routes.status = () => reply(200, statusBody({ demo: tom }));
  h.routes.keys_list = () => reply(200, keysBody({ demo: tom }));
  await h.store.refreshStatus();
  await h.store.refreshKeys();
  const id = h.store.confirmAlarm('alarm');
  const c = h.s().confirm;
  assert.equal(c.expectDemo, '2');
  assert.equal(c.hold, true);
  assert.equal(c.title, 'Demo-Alarm an Tom senden?');
  assert.match(c.text, /^DEMO-MODUS: nur „Tom“/);
  h.routes.alarm = () => reply(200, { ok: true, message: 'Gesendet.', results: [{ id: 2, label: 'Tom', ok: true, http: 200 }] });
  await h.store.confirmNow(id);
  assert.equal(h.posts('alarm')[0].fields.expect_demo, '2');
  assert.equal(h.posts('alarm')[0].fields.action, 'alarm');
  // Demo-Alarm über die Box nur im Demo-Modus.
  h.store.confirmAlarm('box_alarm');
  assert.equal(h.s().confirm.expectDemo, '2');
  h.routes.enqueue = () => reply(200, { ok: true, message: 'Befehl angelegt.' });
  await h.store.confirmNow();
  assert.deepEqual(plain(h.posts('enqueue')[0].fields), { action: 'enqueue', type: 'ALARM', expect_demo: '2' });
  await settle();
  h.routes.status = () => reply(200, statusBody());
  h.routes.keys_list = () => reply(200, keysBody());
  await h.store.refreshStatus();
  assert.equal(h.store.confirmAlarm('box_alarm'), null);
  h.store.confirmAlarm('false_alarm');
  assert.equal(h.s().confirm.expectDemo, '0');
  assert.equal(h.s().confirm.title, 'Entwarnung an alle senden?');
});

test('confirmAlarm(kind, onResult): Ergebnis der bestätigten Aktion geht an den Aufrufer', async () => {
  const h = harness();
  h.store.init(ADMIN);
  const tom = { enabled: true, key_id: 2, label: 'Tom' };
  h.routes.status = () => reply(200, statusBody({ demo: tom }));
  h.routes.keys_list = () => reply(200, keysBody({ demo: tom }));
  await h.store.refreshStatus();
  await h.store.refreshKeys();
  const seen = [];
  // Ablehnung des NAS (z.B. Box hat die Demo-Liste noch nicht): kommt beim Knopf an.
  h.routes.enqueue = () => reply(200, { ok: false, message: 'Demo-Alarm gesperrt: Box hat die Liste noch nicht übernommen.' });
  assert.ok(h.store.confirmAlarm('box_alarm', (res) => seen.push(plain(res))));
  const res = await h.store.confirmNow();
  assert.deepEqual(seen, [{ ok: false, text: 'Demo-Alarm gesperrt: Box hat die Liste noch nicht übernommen.', kind: 'error' }]);
  assert.deepEqual(plain(res), seen[0]);
  // Ohne Bestätigung (Abbrechen, Frist) wird der Rückruf nie aufgerufen.
  assert.ok(h.store.confirmAlarm('box_alarm', (r) => seen.push(plain(r))));
  h.store.closeConfirm();
  await settle();
  assert.equal(seen.length, 1);
  // Auch für den Alarm vom NAS (Ergebnis zusätzlich in state.result).
  h.routes.alarm = () => reply(200, { ok: true, message: 'Gesendet.', results: [{ id: 2, label: 'Tom', ok: true, http: 200 }] });
  assert.ok(h.store.confirmAlarm('alarm', (r) => seen.push(plain(r))));
  await h.store.confirmNow();
  assert.equal(seen.length, 2);
  assert.equal(seen[1].ok, true);
  assert.equal(h.s().result.phase, 'done');
});

test('Sichtbarkeit: versteckt → Bestätigung zu, Abfragen aus; sichtbar → sofort wieder', async () => {
  const h = harness();
  h.store.init(ADMIN);
  h.store.start();
  await settle();
  assert.deepEqual(h.timers.intervals(), [1000, 5000, 30000, 30000, 60000, 60000, 60000]);
  h.routes.status = () => reply(200, statusBody({}, { keys_version: '4' }));
  await h.store.refreshStatus();
  assert.ok(h.s().mismatchSince > 0);
  h.store.openConfirm({ title: 'A', text: 'Alarm?', hold: true, expectDemo: '0', onConfirm: () => {} });
  h.doc.hidden = true;
  h.doc.fire('visibilitychange');
  assert.equal(h.s().confirm, null);
  assert.equal(h.s().mismatchSince, 0);
  assert.deepEqual(h.timers.intervals(), []);
  const n = h.gets('status').length;
  h.doc.hidden = false;
  h.doc.fire('visibilitychange');
  assert.equal(h.gets('status').length, n + 1);
  assert.deepEqual(h.timers.intervals(), [1000, 5000, 30000, 30000, 60000, 60000, 60000]);
  h.store.stop();
});

test('Hinweis im Browser: versteckt nur Status alle 60 s, Übergänge an die Abonnenten', async () => {
  const h = harness();
  h.store.init(ADMIN);
  h.store.setBrowserAlert(true);
  assert.equal(h.storage.getItem('fwAlarm.browserAlert'), '1');
  const events = [];
  h.store.onTransition((e) => events.push(`${e.from}>${e.to}`));
  h.store.start();
  await settle();
  h.doc.hidden = true;
  h.doc.fire('visibilitychange');
  assert.deepEqual(h.timers.intervals(), [60000]);
  h.routes.status = () => reply(200, statusBody({ offline: true, seen_age_seconds: 900 }));
  h.timers.advance(60000);
  await settle();
  assert.deepEqual(events, ['ok>offline']);
  h.routes.status = () => { throw new TypeError('Failed to fetch'); };
  h.timers.advance(60000);
  await settle();
  h.timers.advance(60000);
  await settle();
  assert.deepEqual(events, ['ok>offline']);     // erst ab dem 3. Fehlschlag
  h.timers.advance(60000);
  await settle();
  assert.deepEqual(events, ['ok>offline', 'offline>nas']);
  h.routes.status = () => reply(200, statusBody());
  h.timers.advance(60000);
  await settle();
  assert.deepEqual(events, ['ok>offline', 'offline>nas', 'nas>ok']);
  // Abgeschaltet: versteckt ruht alles.
  h.store.setBrowserAlert(false);
  h.doc.hidden = false;
  h.doc.fire('visibilitychange');
  h.doc.hidden = true;
  h.doc.fire('visibilitychange');
  assert.deepEqual(h.timers.intervals(), []);
  assert.equal(h.storage.has('fwAlarm.browserAlert'), false);
});

test('setRoute: Verlauf, Meldungen und Einstellungen nur, wenn ihre Seite sichtbar ist', async () => {
  const h = harness();
  h.store.init(ADMIN);
  h.store.start();
  await settle();
  assert.equal(h.gets('log_view', 'all').length, 0);
  assert.equal(h.gets('notify_list').length, 0);
  assert.equal(h.gets('settings_list').length, 0);
  assert.equal(h.gets('log_view', 'alarm_core').length, 1);
  assert.equal(h.gets('keys_list').length, 1);
  h.store.setRoute('#/verlauf?filter=alarm');
  await settle();
  assert.equal(h.s().route, 'verlauf');
  assert.equal(h.gets('log_view', 'all').length, 1);
  assert.equal(h.gets('log_view', 'all')[0].fields.limit, '25');
  h.store.setRoute({ path: ['einstellungen', 'meldungen'], query: {} });
  await settle();
  assert.equal(h.gets('notify_list').length, 1);
  assert.equal(h.gets('settings_list').length, 1);
  assert.ok(h.s().notify);
  assert.equal(h.s().settings.mute_max_seconds.value, 43200);
  h.timers.advance(30000);
  await settle();
  assert.equal(h.gets('log_view', 'all').length, 1);   // Verlauf nicht sichtbar: kein Abruf
  // Breite Übersicht: "Letzte Ereignisse" (5 neueste, ungefiltert).
  h.routes.log_view = (f) => reply(200, { ok: true, entries: [{ event: 'key_added', at: T0, label: 'A', key: 'x' }], server_time: T0, limit: f.limit });
  h.store.setRoute('uebersicht', true);
  await settle();
  const recent = h.gets('log_view', 'all').pop();
  assert.equal(recent.fields.limit, '5');
  assert.equal(h.s().recent.entries.length, 1);
  h.store.stop();
  // Lese-Rolle: keine Meldungen/Einstellungen.
  const r = harness();
  r.store.init({ role: 'readonly', user: 'leser', csrf: 'x' });
  r.store.setRoute('einstellungen');
  await settle();
  assert.equal(r.gets('notify_list').length, 0);
  assert.equal(r.gets('settings_list').length, 0);
  assert.equal(r.s().readOnly, true);
});

test('Verlauf: Filter, Mehr laden, Suche, veraltete Antworten verworfen', async () => {
  const h = harness();
  h.store.init(ADMIN);
  const make = (n) => Array.from({ length: n }, (_, i) => ({ event: 'key_added', at: T0 - i, label: `E${i}`, key: 'k' }));
  h.routes.log_view = (f) => reply(200, { ok: true, entries: f.filter === 'alarm_core' ? [] : make(Math.min(Number(f.limit), 60)), server_time: T0 });
  h.store.setRoute('verlauf');
  await settle();
  assert.equal(h.s().log.entries.length, 25);
  assert.equal(h.s().log.more, true);
  h.store.loadMoreLog();
  await settle();
  assert.equal(h.s().log.limit, 50);
  assert.equal(h.s().log.entries.length, 50);
  h.store.loadMoreLog();
  await settle();
  assert.equal(h.s().log.entries.length, 60);
  assert.equal(h.s().log.more, false);
  h.store.setLogQuery('E1');
  assert.equal(h.s().log.query, 'E1');
  // Filterwechsel: Antwort für den alten Filter wird verworfen.
  const gate = deferred();
  h.routes.log_view = (f) => (f.filter === 'all' ? gate.promise : reply(200, { ok: true, entries: make(3), server_time: T0 }));
  h.store.loadMoreLog();     // läuft mit filter=all, hängt
  await settle();
  h.store.setLogFilter('alarm');
  assert.equal(h.s().log.filter, 'alarm');
  assert.equal(h.s().log.limit, 25);
  assert.equal(h.s().log.entries, null);
  gate.resolve(reply(200, { ok: true, entries: make(99), server_time: T0 }));
  await settle();
  assert.equal(h.s().log.entries.length, 3);
  h.store.setLogFilter('quatsch');
  assert.equal(h.s().log.filter, 'all');
  // Fehler vor dem ersten Laden: Fehlertext; danach bleibt der alte Stand.
  const e = harness();
  e.store.init(ADMIN);
  e.routes.log_view = () => { throw new TypeError('x'); };
  e.store.setRoute('verlauf');
  await settle();
  assert.equal(e.s().log.error, 'Verlauf konnte nicht geladen werden – neuer Versuch läuft automatisch.');
});

test('Empfänger: beschädigte Liste, Fehler vor dem ersten Laden', async () => {
  const h = harness();
  h.store.init(ADMIN);
  h.routes.keys_list = () => { throw new TypeError('x'); };
  await h.store.refreshKeys();
  assert.equal(h.s().keysError, 'Empfängerliste konnte nicht geladen werden – neuer Versuch läuft automatisch.');
  h.routes.keys_list = () => reply(200, keysBody());
  await h.store.refreshKeys();
  assert.equal(h.s().keysError, '');
  assert.equal(h.s().nasKeysVersion, 5);
  h.routes.keys_list = () => { throw new TypeError('x'); };
  await h.store.refreshKeys();
  assert.equal(h.s().keysError, '');            // alter Stand bleibt stehen
  assert.equal(h.s().keys.keys.length, 2);
  h.routes.keys_list = () => reply(500, { ok: false, code: 'keys_corrupt', message: 'Liste kaputt.' });
  await h.store.refreshKeys();
  assert.equal(h.s().keysCorrupt, 'Liste kaputt.');
  assert.equal(h.s().keys, null);
  assert.ok(h.d().hints.length === 0 || h.d().hints[0].tone === 'crit');
  h.routes.keys_list = () => reply(200, { ok: false, code: 'keys_corrupt' });
  await h.store.refreshKeys();
  assert.match(h.s().keysCorrupt, /^Empfängerliste auf dem NAS beschädigt/);
  await h.store.refreshStatus();
  assert.equal(h.d().hints[0].text, 'Die Empfängerliste auf dem NAS ist beschädigt – siehe „Empfänger“.');
});

test('Empfänger-Aktionen: Felder, Rückmeldung, neue id nach dem Hinzufügen', async () => {
  const h = harness();
  h.store.init(ADMIN);
  await h.store.refreshKeys();
  h.routes.keys_add = () => {
    h.routes.keys_list = () => reply(200, keysBody({ keys: keysBody().keys.concat([{ id: 7, label: 'Lena', key_masked: 'le…01', muted: false, mute_volume: 0 }]) }));
    return reply(200, { ok: true, message: 'Empfänger hinzugefügt.' });
  };
  const added = await h.store.keyAdd('  Lena ', ' lena0000001 ', ' +49 1 ');
  assert.deepEqual(plain(added), { ok: true, text: 'Empfänger hinzugefügt.', kind: 'ok', id: 7 });
  assert.deepEqual(plain(h.posts('keys_add')[0].fields), { action: 'keys_add', label: 'Lena', key: 'lena0000001', phone: '+49 1' });
  await h.store.keyAdd('Max', 'max00000001', '');
  assert.equal(h.posts('keys_add')[1].fields.phone, undefined);
  h.routes.keys_mute = () => reply(200, { ok: true, message: 'Stumm.' });
  h.routes.keys_set_mute_permanent = () => reply(200, { ok: true });
  h.routes.keys_set_mute_volume = () => reply(200, { ok: false, message: 'Ungültig.' });
  h.routes.keys_rename = () => reply(200, { ok: true, message: 'Umbenannt.' });
  h.routes.keys_replace_key = () => reply(200, { ok: true, message: 'Ersetzt.' });
  h.routes.keys_set_phone = () => reply(200, { ok: true, message: 'Gespeichert.' });
  h.routes.keys_delete = () => reply(200, { ok: false, message: 'Der letzte Empfänger kann nicht gelöscht werden.' });
  assert.deepEqual(plain(await h.store.keyMute(2, false)), { ok: true, text: 'Stumm.', kind: 'ok' });
  assert.equal((await h.store.keyPermanent(2, true)).text, 'Gespeichert.');
  assert.deepEqual(plain(await h.store.keyVolume(2, 4)), { ok: false, text: 'Ungültig.', kind: 'error' });
  await h.store.keyRename(2, 'Thomas');
  await h.store.keyReplace(2, 'neu00000001');
  await h.store.keyPhone(2, '');
  assert.equal((await h.store.keyDelete(1)).text, 'Der letzte Empfänger kann nicht gelöscht werden.');
  const f = (a) => plain(h.posts(a)[0].fields);
  assert.deepEqual(f('keys_mute'), { action: 'keys_mute', id: '2', muted: '0' });
  assert.deepEqual(f('keys_set_mute_permanent'), { action: 'keys_set_mute_permanent', id: '2', permanent: '1' });
  assert.deepEqual(f('keys_set_mute_volume'), { action: 'keys_set_mute_volume', id: '2', volume: '4' });
  assert.deepEqual(f('keys_rename'), { action: 'keys_rename', id: '2', label: 'Thomas' });
  assert.deepEqual(f('keys_replace_key'), { action: 'keys_replace_key', id: '2', key: 'neu00000001' });
  assert.deepEqual(f('keys_set_phone'), { action: 'keys_set_phone', id: '2', phone: '' });
  assert.deepEqual(f('keys_delete'), { action: 'keys_delete', id: '1' });
  await settle();
  // Jede Aktion lädt die Liste und (falls sichtbar) den Verlauf nach.
  h.store.setRoute('verlauf');
  await settle();
  const counts = { keys: h.gets('keys_list').length, log: h.gets('log_view', 'all').length };
  await h.store.keyRename(2, 'Tom');
  await settle();
  assert.equal(h.gets('keys_list').length, counts.keys + 1);
  assert.equal(h.gets('log_view', 'all').length, counts.log + 1);
  h.routes.keys_mute = () => { const e = new Error('t'); e.name = 'TimeoutError'; throw e; };
  const t = await h.store.keyMute(1, true);
  assert.equal(t.kind, 'warn');
  assert.equal(h.s().busyCount, 0);
});

test('Demo, TEST, Abbrechen, Meldungen, Einstellungen', async () => {
  const h = harness();
  h.store.init(ADMIN);
  assert.equal((await h.store.demoSet(true, 2)).ok, false);   // Demo-Zustand noch unbekannt
  await h.store.refreshStatus();
  const none = await h.store.demoSet(true, '');
  assert.equal(none.text, 'Bitte zuerst einen Empfänger in der Liste anlegen und als Test-Empfänger auswählen.');
  h.routes.demo_set = () => reply(200, { ok: true, message: 'DEMO-MODUS aktiviert', demo: { enabled: true, key_id: 2, label: 'Tom' } });
  const on = await h.store.demoSet(true, 2);
  assert.equal(on.ok, true);
  assert.equal(h.s().demo.key_id, 2);
  assert.deepEqual(plain(h.posts('demo_set')[0].fields), { action: 'demo_set', enabled: '1', id: '2' });
  await h.store.demoSet(false);
  assert.deepEqual(plain(h.posts('demo_set')[1].fields), { action: 'demo_set', enabled: '0' });
  h.routes.enqueue = () => reply(200, { ok: false, message: 'Es wartet bereits ein Befehl (abbrechen oder warten).' });
  const t = await h.store.enqueueTest();
  assert.deepEqual(plain(t), { ok: false, text: 'Es wartet bereits ein Befehl (abbrechen oder warten).', kind: 'error' });
  assert.deepEqual(plain(h.posts('enqueue')[0].fields), { action: 'enqueue', type: 'TEST' });
  h.routes.enqueue = () => reply(200, { ok: true, message: 'Befehl angelegt.' });
  assert.deepEqual(plain(await h.store.enqueueTest()), { ok: true, text: '', kind: 'ok' });
  h.routes.cancel = () => reply(200, { ok: true, message: 'Befehl abgebrochen.' });
  assert.equal((await h.store.cancelCommand()).text, 'Befehl abgebrochen.');
  h.routes.notify_set = () => reply(200, { ok: true, message: 'Gespeichert – x.' });
  h.store.setRoute('einstellungen/meldungen');
  await settle();
  const before = { notify: h.gets('notify_list').length, status: h.gets('status').length };
  const n = await h.store.notifySet('box_offline', 'enabled', 0);
  assert.equal(n.text, 'Gespeichert – x.');
  assert.deepEqual(plain(h.posts('notify_set')[0].fields), { action: 'notify_set', type: 'box_offline', key: 'enabled', value: '0' });
  await settle();
  assert.equal(h.gets('notify_list').length, before.notify + 1);
  assert.equal(h.gets('status').length, before.status + 1);
  h.routes.settings_set = () => reply(200, { ok: true, message: 'Gespeichert – Automatisch wieder laut nach: 4 Std.' });
  const st = await h.store.settingsSet('mute_max_seconds', 14400);
  assert.equal(st.ok, true);
  assert.deepEqual(plain(h.posts('settings_set')[0].fields), { action: 'settings_set', key: 'mute_max_seconds', value: '14400' });
});

test('Demo-Abgleich: übernommen, noch nicht, offline, unbekannt (Fallback Versionsvergleich)', async () => {
  const h = harness();
  h.store.init(ADMIN);
  const tom = { enabled: true, key_id: 2, label: 'Tom' };
  const push = async (over, statusOver) => {
    h.routes.status = () => reply(200, statusBody(over, statusOver));
    await h.store.refreshStatus();
    return h.d().demoSync;
  };
  assert.equal(await push({}), null);
  assert.equal(await push({ demo: tom, demo_sync_ok: true }), 'synced');
  assert.equal(await push({ demo: tom, demo_sync_ok: false }), 'pending');
  assert.equal(await push({ demo: tom, demo_sync_ok: undefined }, { keys_version: '5' }), 'synced');
  assert.equal(await push({ demo: tom, demo_sync_ok: undefined }, { keys_version: '4' }), 'pending');
  assert.equal(await push({ demo: tom, demo_sync_ok: true, offline: true }), 'offline');
  h.routes.status = () => { throw new TypeError('x'); };
  await h.store.refreshStatus();
  assert.equal(h.d().demoSync, 'unknown');
});

test('unveränderte Daten behalten ihre Referenz', async () => {
  const h = harness();
  h.store.init(ADMIN);
  await h.store.refreshKeys();
  await h.store.refreshStatus();
  const { keys, status } = h.s();
  await h.store.refreshKeys();
  await h.store.refreshStatus();
  assert.equal(h.s().keys, keys);
  assert.equal(h.s().status, status);
  h.routes.keys_list = () => reply(200, keysBody({ version: 6 }));
  await h.store.refreshKeys();
  assert.notEqual(h.s().keys, keys);
  assert.equal(h.s().keys.version, 6);
});

test('Hinweis im Browser: unvollständige Zustellung heißt delivery', async () => {
  const h = harness();
  h.store.init(ADMIN);
  h.store.setBrowserAlert(true);
  const events = [];
  h.store.onTransition((e) => events.push(`${e.from}>${e.to}`));
  await h.store.refreshKeys();
  await h.store.refreshStatus();
  h.routes.status = () => reply(200, statusBody({}, { last_alarm: '[13.11. 21:42] Alarm', key_progress: '1/2', alarm_seq: '1' }));
  await h.store.refreshStatus();
  assert.equal(h.d().verdict.key, 'hints');
  assert.deepEqual(events, ['ok>delivery']);
});

test('Hinzufügen gescheitert: Fehler sofort, ohne auf die Liste zu warten', async () => {
  const h = harness();
  h.store.init(ADMIN);
  h.routes.keys_add = () => reply(200, { ok: false, message: 'Dieser Key ist bereits eingetragen.' });
  h.routes.keys_list = () => new Promise(() => {});   // Liste hängt
  const r = await h.store.keyAdd('Lena', 'lena0000001');
  assert.deepEqual(plain(r), { ok: false, text: 'Dieser Key ist bereits eingetragen.', kind: 'error' });
});

test('Fix 1+3: nach „Antwort unklar“ keine Nachsendung – weder über confirmResend noch über einen Moduswechsel', async () => {
  const h = harness();
  h.store.init(ADMIN);
  await h.store.refreshStatus();
  await h.store.refreshKeys();
  h.routes.alarm = () => reply(200, partialResult);
  await h.store.sendNasAlarm('alarm', '0');
  h.routes.alarm = () => { throw new TypeError('Failed to fetch'); };
  await h.store.sendNasAlarm('alarm', '0');
  await settle();
  assert.equal(h.s().result.offerRetry, false);
  assert.equal(h.s().unreached.entries.length, 1);   // Liste steht noch da ...
  assert.equal(h.store.confirmResend(), null);        // ... aber keine Nachsendung
  assert.equal(h.s().confirm, null);
  // Moduswechsel, während eine Alarm-Bestätigung offen ist.
  assert.ok(h.store.confirmAlarm('alarm'));
  h.routes.status = () => reply(200, statusBody({ demo: { enabled: true, key_id: 2, label: 'Tom' } }));
  await h.store.refreshStatus();
  assert.equal(h.s().confirm, null);
  assert.match(h.s().result.head, /Demo-\/LIVE-Modus hat sich gerade geändert/);
  assert.equal(h.s().result.offerRetry, false);
  assert.equal(h.d().retry, null);
  assert.equal(h.store.confirmResend(), null);
  // Auch eine Frage aus überholtem Stand macht kein Angebot daraus.
  assert.equal(h.store.openConfirm({ title: 'A', text: 'x', hold: true, expectDemo: '0', onConfirm: () => {} }), null);
  assert.equal(h.s().result.offerRetry, false);
  assert.equal(h.posts('alarm').length, 2);
  // Gegenprobe: Nach einem Ergebnis MIT Angebot bleibt es nach dem Wechsel.
  const g = harness();
  g.store.init(ADMIN);
  await g.store.refreshStatus();
  g.routes.alarm = () => reply(200, partialResult);
  await g.store.sendNasAlarm('alarm', '0');
  await settle();
  assert.ok(g.store.confirmAlarm('alarm'));
  g.routes.status = () => reply(200, statusBody({ demo: { enabled: true, key_id: 2, label: 'Tom' } }));
  await g.store.refreshStatus();
  assert.equal(g.s().result.offerRetry, true);
  assert.equal(g.d().retry.label, 'Nur an Tom erneut senden');
});

test('Fix 2: nicht gespeichert → neue Referenz und Revision, auch bei gleichen Daten', async () => {
  const h = harness();
  h.store.init(ADMIN);
  await h.store.refreshStatus();
  await h.store.refreshKeys();
  h.store.setRoute('einstellungen/meldungen');
  await settle();
  const notify0 = h.s().notify;
  assert.ok(notify0);
  h.routes.notify_set = () => reply(200, { ok: false, message: 'Einstellung konnte nicht gespeichert werden (data_dir prüfen).' });
  const r = await h.store.notifySet('box_offline', 'enabled', '0');
  assert.equal(r.ok, false);
  assert.notEqual(h.s().notify, notify0);
  assert.deepEqual(plain(h.s().notify), plain(notify0));
  assert.equal(h.s().rev.notify, 1);
  await settle();                                   // Nachladen mit gleichen Daten
  assert.notEqual(h.s().notify, notify0);
  // Gespeichert: keine Revision.
  h.routes.notify_set = () => reply(200, { ok: true, message: 'Gespeichert.' });
  await h.store.notifySet('box_offline', 'level', 'active');
  assert.equal(h.s().rev.notify, 1);
  // Empfänger (Fehler per Netz), Einstellungen, Demo.
  const keys0 = h.s().keys;
  h.routes.keys_set_mute_volume = () => { throw new TypeError('x'); };
  await h.store.keyVolume(1, 4);
  assert.notEqual(h.s().keys, keys0);
  assert.equal(h.s().rev.keys, 1);
  const settings0 = h.s().settings;
  h.routes.settings_set = () => reply(200, { ok: false, message: 'Ungültiger Wert – nichts geändert.' });
  await h.store.settingsSet('mute_max_seconds', 1);
  assert.notEqual(h.s().settings, settings0);
  assert.equal(h.s().rev.settings, 1);
  await settle();
  const demo0 = h.s().demo;
  h.routes.demo_set = () => reply(200, { ok: false, message: 'Demo-Modus konnte nicht gespeichert werden (data_dir prüfen).' });
  await h.store.demoSet(true, 2);
  assert.notEqual(h.s().demo, demo0);
  assert.equal(h.s().demo.enabled, false);
  assert.equal(h.s().rev.demo, 1);
});

// Ein Test-Push ändert keine Listendaten: Scheitert er (bis zu 30 s später),
// darf er die Bedienelemente der Empfänger NICHT zurücksetzen (sonst ginge
// ein gerade gezogener Regler oder ein halb getipptes Telefon verloren).
test('Fix-Runde Task 10: gescheiterter Test-Push setzt rev.keys und keys nicht zurück', async () => {
  const h = harness();
  h.store.init(ADMIN);
  await h.store.refreshKeys();
  const keys0 = h.s().keys;
  assert.ok(keys0);
  h.routes.keys_test = () => reply(200, { ok: false, message: 'Test-Push an „Tom“ FEHLGESCHLAGEN (HTTP 400 – Bark-Key prüfen).' });
  const r = await h.store.keyTest(2);
  assert.equal(r.ok, false);
  assert.equal(h.s().rev.keys, 0);
  h.routes.keys_test = () => { throw new TypeError('Netz weg'); };
  assert.equal((await h.store.keyTest(2)).ok, false);
  assert.equal(h.s().rev.keys, 0);
  await settle();
  assert.equal(h.s().keys, keys0);   // gleiche Daten nachgeladen: gleiche Referenz
  assert.equal(h.s().keyTestInFlight, false);
});

test('Fix 4: start() taktet sofort – alter Stand gilt gleich als veraltet', async () => {
  const h = harness();
  h.store.init(ADMIN);
  await h.store.refreshStatus();
  h.clock.now += 20000;
  h.routes.status = () => new Promise(() => {});   // Antwort kommt (noch) nicht
  h.store.start();
  assert.equal(h.s().stale, true);
  assert.equal(h.s().nowMs, h.clock.now);
  assert.equal(h.d().verdict.title, 'Status veraltet');
  h.store.stop();
  const g = harness();
  g.store.init(ADMIN);
  await g.store.refreshStatus();
  g.clock.now += 20000;
  g.store.start();
  await settle();
  assert.equal(g.s().stale, false);                 // frische Antwort beendet es
  g.store.stop();
});

test('useStore: neu zeichnen nur bei Änderung des gewählten Werts', async () => {
  const h = harness();
  let renders = 0;
  const cells = [];
  let effects = [];
  let cursor = 0;
  h.ctx.htmPreact = {
    useState(init) {
      const i = cursor++;
      if (!(i in cells)) cells[i] = init;
      return [cells[i], (fn) => { cells[i] = typeof fn === 'function' ? fn(cells[i]) : fn; renders += 1; }];
    },
    useRef(init) { const i = cursor++; if (!(i in cells)) cells[i] = { current: init }; return cells[i]; },
    useEffect(fn) { effects.push(fn); }
  };
  h.store.init(ADMIN);
  const render = () => { cursor = 0; return h.store.useStore((s) => s.busyCount); };
  assert.equal(render(), 0);
  const cleanups = effects.map((fn) => fn());
  effects = [];
  h.store.setRoute('empfaenger');          // anderer Wert: kein Neuzeichnen
  assert.equal(renders, 0);
  const gate = deferred();
  h.routes.keys_mute = () => gate.promise;
  const p = h.store.keyMute(1, true);
  assert.equal(renders, 1);
  assert.equal(render(), 1);
  gate.resolve(reply(200, { ok: true }));
  await p;
  assert.equal(renders, 2);
  cleanups.forEach((fn) => fn && fn());
  await h.store.keyMute(1, false);
  assert.equal(renders, 2);                 // abgemeldet
});

test('Verlauf: „Ältere laden“ ist während des Ladens gesperrt (Doppeltipp = nur +25), Fehler wird gemeldet', async () => {
  const h = harness();
  h.store.init(ADMIN);
  const page = (n) => Array.from({ length: n }, (_, i) => ({ at: T0 - i, event: 'key_added', label: `K${i}`, key: 'abc', id: i }));
  h.routes.log_view = (f) => reply(200, { ok: true, entries: page(Number(f.limit)), server_time: T0, filter: f.filter });
  h.store.setRoute('verlauf');
  await settle();
  assert.equal(h.s().log.limit, 25);
  assert.equal(h.s().log.more, true);
  let release;
  h.routes.log_view = (f) => new Promise((resolve) => { release = () => resolve(reply(200, { ok: true, entries: page(Number(f.limit)), server_time: T0, filter: f.filter })); });
  h.store.loadMoreLog();
  h.store.loadMoreLog();   // Doppeltipp
  assert.equal(h.s().log.limit, 50);
  assert.equal(h.s().log.loadingMore, true);
  await settle();
  assert.equal(h.gets('log_view', 'all').filter((r) => r.fields.limit === '50').length, 1);
  release();
  await settle();
  assert.equal(h.s().log.loadingMore, false);
  assert.equal(h.s().log.limit, 50);
  assert.equal(h.s().log.entries.length, 50);
  // Fehlschlag: Sperre löst sich, Fehlertext steht da, Einträge bleiben.
  h.routes.log_view = () => reply(500, 'kaputt');
  h.store.loadMoreLog();
  await settle();
  assert.equal(h.s().log.loadingMore, false);
  assert.match(h.s().log.error, /Ältere Einträge/);
  assert.equal(h.s().log.moreFailed, true);         // gehört zum Knopf unten
  assert.equal(h.s().log.entries.length, 50);
});

// ==== Abschluss-Prüfung des Neubaus (I1–I3, M1–M7) ==========================

test('I1a: Verlauf-Abruf scheitert bei geladenen Einträgen → Fehler mit Stand, Einträge bleiben', async () => {
  const h = harness();
  h.store.init(ADMIN);
  const one = [{ event: 'key_added', at: T0 - 5, label: 'A', key: 'k' }];
  h.routes.log_view = (f) => reply(200, { ok: true, entries: f.filter === 'alarm_core' ? [] : one, server_time: T0 });
  h.store.setRoute('verlauf');
  await settle();
  assert.equal(h.s().log.entries.length, 1);
  assert.equal(h.s().log.error, '');
  const stale = `Verlauf konnte nicht aktualisiert werden – Stand von ${h.logic.fmtClock(T0)}`;
  h.routes.log_view = () => { throw new TypeError('Failed to fetch'); };
  await h.store.refreshLog();
  assert.equal(h.s().log.entries.length, 1);       // alter Stand bleibt lesbar …
  assert.equal(h.s().log.error, stale);            // … sieht aber nicht frisch aus
  assert.equal(h.s().log.moreFailed, false);       // gehört nach oben, nicht zu „Ältere laden“
  // Eine Antwort ohne ok zählt genauso.
  h.routes.log_view = (f) => reply(200, { ok: true, entries: f.filter === 'alarm_core' ? [] : one, server_time: T0 });
  await h.store.refreshLog();
  assert.equal(h.s().log.error, '');
  h.routes.log_view = () => reply(200, { ok: false, message: 'kaputt' });
  await h.store.refreshLog();
  assert.equal(h.s().log.error, stale);
  // Wieder erreichbar: Fehler weg.
  h.routes.log_view = (f) => reply(200, { ok: true, entries: f.filter === 'alarm_core' ? [] : one, server_time: T0 + 60 });
  await h.store.refreshLog();
  assert.equal(h.s().log.error, '');
});

test('I1a: „Letzter Alarm“ sieht nicht frisch aus, wenn sein Abruf scheitert', async () => {
  const h = harness();
  h.store.init(ADMIN);
  await h.store.refreshStatus();
  h.routes.log_view = () => reply(200, { ok: true, entries: [{ event: 'nas_alarm', at: T0 - 600, ok: true, demo: false }], server_time: T0 });
  await h.store.refreshAlarmInfo();
  const row = () => h.d().overviewRows.find((r) => r.id === 'alarm');
  assert.equal(row().tone, 'crit');                // Alarm der letzten Stunde: rot
  assert.equal(h.s().alarmInfoFailed, false);
  h.routes.log_view = () => { throw new TypeError('Failed to fetch'); };
  await h.store.refreshAlarmInfo();
  assert.equal(h.s().alarmInfoFailed, true);
  assert.equal(row().tone, 'unknown');
  assert.match(row().value, /nicht aktualisiert/);
  h.routes.log_view = () => reply(200, { ok: true, entries: [], server_time: T0 });
  await h.store.refreshAlarmInfo();
  assert.equal(h.s().alarmInfoFailed, false);
  assert.equal(row().tone, '');
  // Noch nie geladen und gescheitert: kein „…“ (sähe nach „lädt gleich“ aus).
  const g = harness();
  g.store.init(ADMIN);
  await g.store.refreshStatus();
  g.routes.log_view = () => reply(500, 'kaputt');
  await g.store.refreshAlarmInfo();
  const r = g.d().overviewRows.find((x) => x.id === 'alarm');
  assert.equal(r.tone, 'unknown');
  assert.equal(r.value, 'nicht abrufbar');
});

test('I1b: „Im Verlauf prüfen“ lädt frisch und leert die Suche; Filter aus der Adresse leert die Suche', async () => {
  const h = harness();
  h.store.init(ADMIN);
  h.routes.log_view = (f) => reply(200, { ok: true, entries: f.filter === 'alarm_core' ? [] : [{ event: 'nas_alarm', at: T0 - 9, ok: true, demo: false }], server_time: T0 });
  h.store.setRoute('verlauf');
  h.store.setLogFilter('alarm');
  await settle();
  h.store.setLogQuery('Tom');
  const before = h.gets('log_view', 'alarm').length;
  // Zurück auf der Übersicht: „Im Verlauf prüfen“ (gleicher Filter wie zuletzt).
  h.store.setRoute('uebersicht');
  h.store.verifyInLog();
  assert.equal(h.s().log.filter, 'alarm');
  assert.equal(h.s().log.query, '');
  assert.equal(h.s().log.entries, null);           // kein gemerkter Stand, der „frisch“ aussieht
  h.store.setRoute('#/verlauf?filter=alarm');
  await settle();
  assert.equal(h.gets('log_view', 'alarm').length, before + 1);
  assert.equal(h.s().log.entries.length, 1);
  // Filter in der Adresse: Suche leeren - auch beim gleichen Filter.
  h.store.setLogQuery('Tom');
  h.store.setLogFilter('alarm', { clearQuery: true });
  assert.equal(h.s().log.query, '');
  // Filterwechsel per Hand behält die Suche.
  h.store.setLogQuery('Tom');
  h.store.setLogFilter('keys');
  assert.equal(h.s().log.query, 'Tom');
  // Abgemeldet: kein endloses „Wird geladen…“.
  h.routes.status = () => reply(401, { ok: false });
  await h.store.refreshStatus();
  h.store.verifyInLog();
  assert.equal(h.s().log.entries, null);
  assert.match(h.s().log.error, /neu anmelden/);
});

test('I1b: PC – von der Übersicht (Letzte Ereignisse) in den Verlauf lädt den Verlauf sofort', async () => {
  const h = harness();
  h.store.init(ADMIN);
  h.store.setRoute('verlauf', true);
  await settle();
  const full = () => h.gets('log_view', 'all').filter((r) => r.fields.limit === '25').length;
  const n = full();
  h.store.setRoute('uebersicht', true);
  await settle();
  h.store.setRoute('verlauf', true);
  await settle();
  assert.equal(full(), n + 1);
});

test('I1c: Band „keine aktuelle Antwort“ aus derive – orange, rot nach „offline“, nicht bei frischen Daten', async () => {
  const h = harness();
  h.store.init(ADMIN);
  assert.equal(h.d().unsureBand, null);            // noch keine Antwort: lädt, kein Band
  await h.store.refreshStatus();
  await h.store.refreshKeys();
  assert.equal(h.d().unsureBand, null);
  const at = h.logic.fmtClockMs(T0 * 1000);
  h.routes.status = () => { throw new TypeError('Failed to fetch'); };
  await h.store.refreshStatus();
  assert.deepEqual(plain(h.d().unsureBand), { tone: 'warn', text: `Keine Verbindung zum NAS – angezeigte Werte können veraltet sein (Stand ${at})` });
  // Spec §5.3: auch Empfänger und Letzter Alarm grau, solange unsicher.
  for (const id of ['box', 'wifi', 'watch', 'keys', 'alarm']) {
    assert.equal(h.d().overviewRows.find((r) => r.id === id).tone, 'unknown', id);
  }
  // Nur veraltet (15 s ohne Antwort): Wortlaut wie im Urteil.
  const g = harness();
  g.store.init(ADMIN);
  await g.store.refreshStatus();
  g.clock.now += 20000;
  g.store.tick();
  assert.equal(g.d().unsureBand.text, `Keine aktuelle Antwort vom NAS – angezeigte Werte können veraltet sein (Stand ${at})`);
  // Letzter bekannter Stand „Box offline“: rot.
  const o = harness();
  o.store.init(ADMIN);
  o.routes.status = () => reply(200, statusBody({ offline: true, seen_age_seconds: 900 }));
  await o.store.refreshStatus();
  assert.equal(o.d().unsureBand, null);            // Box offline, NAS frisch: die Status-Karte reicht
  o.routes.status = () => { const e = new Error('t'); e.name = 'TimeoutError'; throw e; };
  await o.store.refreshStatus();
  assert.deepEqual(plain(o.d().unsureBand), { tone: 'crit', text: `Keine Antwort vom NAS (Zeitüberschreitung) – letzter bekannter Stand (${at}): Box OFFLINE` });
  // Abgemeldet mit gehaltenem Alarm-Ergebnis: Werte werden nicht mehr frisch.
  const a = harness();
  a.store.init(ADMIN);
  a.routes.false_alarm = () => reply(401, { ok: false });
  await a.store.sendNasAlarm('false_alarm', '0');
  assert.equal(a.d().unsureBand.tone, 'warn');
  assert.match(a.d().unsureBand.text, /^Abgemeldet/);
});

test('I3: nach eindeutigem Ergebnis wieder angeboten; nach „unklar“ bzw. abgebrochenem Versand nicht', async () => {
  const PENDING = 'fwAlarm.sendPending';
  // (a) eindeutiges Ergebnis
  const storage = memoryStorage();
  const h = harness({ storage });
  h.store.init(ADMIN);
  h.routes.alarm = () => reply(200, partialResult);
  await h.store.sendNasAlarm('alarm', '0');
  assert.equal(storage.has(PENDING), false);
  const a = harness({ storage });
  a.store.init(ADMIN);
  assert.equal(a.s().result.offerRetry, true);
  assert.equal(a.s().result.head, 'Auf diesem Gerät gemerkt (vor dem Neuladen der Seite):');
  assert.equal(a.d().retry.label, 'Nur an Tom erneut senden');
  // (b) Antwort unklar: Neuladen bietet KEINE Nachsendung an.
  h.routes.alarm = () => { throw new TypeError('Failed to fetch'); };
  await h.store.sendNasAlarm('alarm', '0', [2], T0);
  assert.equal(storage.has(PENDING), true);
  const b = harness({ storage });
  b.store.init(ADMIN);
  assert.equal(b.s().result.offerRetry, false);
  assert.equal(b.s().result.tone, 'warn');
  assert.equal(b.d().retry, null);
  assert.equal(b.store.confirmResend(), null);
  assert.match(b.s().result.head, /^Letzter Versand ohne Ergebnis \(REAL ALARM, \d\d:\d\d:\d\d Uhr\) – erst Verlauf prüfen/);
  assert.equal(b.s().unreached.entries[0].name, 'Tom');   // die Namen bleiben sichtbar
  // (c) Versand begonnen, Seite vor der Antwort verworfen (iOS, Anruf).
  const storage2 = memoryStorage();
  const c = harness({ storage: storage2 });
  c.store.init(ADMIN);
  c.routes.false_alarm = () => new Promise(() => {});
  c.store.sendNasAlarm('false_alarm', '0');
  await settle();
  assert.equal(storage2.has(PENDING), true);
  const c2 = harness({ storage: storage2 });
  c2.store.init(ADMIN);
  assert.equal(c2.s().unreached, null);
  assert.equal(c2.s().result.offerRetry, false);
  assert.match(c2.s().result.head, /^Letzter Versand ohne Ergebnis \(ENTWARNUNG/);
  // … auch mit einer älteren gemerkten Liste.
  const storage3 = memoryStorage();
  const d = harness({ storage: storage3 });
  d.store.init(ADMIN);
  d.routes.alarm = () => reply(200, partialResult);
  await d.store.sendNasAlarm('alarm', '0');
  d.routes.alarm = () => new Promise(() => {});
  d.store.sendNasAlarm('alarm', '0', [2], T0);
  await settle();
  const d2 = harness({ storage: storage3 });
  d2.store.init(ADMIN);
  assert.equal(d2.s().unreached.entries[0].name, 'Tom');
  assert.equal(d2.s().result.offerRetry, false);
  assert.equal(d2.d().retry, null);
  // Sicher NICHT gesendet (4xx) ist eindeutig: Merker weg.
  d2.routes.alarm = () => reply(400, { ok: false, message: 'Ungültige Empfänger-Auswahl – es wurde NICHTS gesendet.' });
  await d2.store.sendNasAlarm('alarm', '0', [2], T0);
  assert.equal(storage3.has(PENDING), false);
  // Ausblenden löscht den Merker ebenfalls; ein zu alter oder kaputter gilt nicht.
  b.store.clearResult();
  assert.equal(storage.has(PENDING), false);
  const old = memoryStorage({ [PENDING]: JSON.stringify({ action: 'alarm', at: T0 * 1000 - 13 * 3600 * 1000 }) });
  const e = harness({ storage: old });
  e.store.init(ADMIN);
  assert.equal(e.s().result, null);
  assert.equal(old.has(PENDING), false);
  const broken = memoryStorage({ [PENDING]: '{kaputt' });
  const f = harness({ storage: broken });
  f.store.init(ADMIN);
  assert.equal(f.s().result, null);
});

test('M1: „Ausblenden“ fragt nach, solange nicht Erreichte gemerkt sind', async () => {
  const h = harness();
  h.store.init(ADMIN);
  h.routes.alarm = () => reply(200, partialResult);
  await h.store.sendNasAlarm('alarm', '0');
  const id = h.store.dismissResult();
  assert.ok(id);
  const c = h.s().confirm;
  assert.equal(c.tag, 'dismiss');
  assert.equal(c.hold, false);
  assert.match(c.title, /ausblenden/i);
  assert.match(c.text, /Tom/);
  assert.ok(h.s().unreached);                       // noch nichts gelöscht
  await h.store.confirmNow(id);
  assert.equal(h.s().unreached, null);
  assert.equal(h.s().result, null);
  assert.equal(h.storage.has('fwAlarm.lastUnreached'), false);
  // Ein neues Ergebnis schließt eine offene Frage (sie gehörte zum alten).
  await h.store.sendNasAlarm('alarm', '0');
  assert.ok(h.store.dismissResult());
  await h.store.sendNasAlarm('alarm', '0');
  assert.equal(h.s().confirm, null);
  assert.ok(h.s().unreached);
  // Ohne nicht Erreichte: sofort weg.
  h.routes.alarm = () => reply(200, { ok: true, message: 'Gesendet.', results: [{ id: 1, label: 'Josh', ok: true, http: 200 }, { id: 2, label: 'Tom', ok: true, http: 200 }], started_at: T0 });
  await h.store.sendNasAlarm('alarm', '0');
  assert.equal(h.s().unreached, null);
  assert.equal(h.store.dismissResult(), null);
  assert.equal(h.s().result, null);
  assert.equal(h.s().confirm, null);
});

test('M3: angehalten, obwohl sichtbar → pageshow/focus startet die Abfragen wieder', async () => {
  const h = harness();
  h.store.init(ADMIN);
  h.store.start();
  await settle();
  const all = [1000, 5000, 30000, 30000, 60000, 60000, 60000];
  h.store.stop();                                   // z.B. Rückkehr aus dem iOS-Seitencache ohne visibilitychange
  assert.deepEqual(h.timers.intervals(), []);
  const n = h.gets('status').length;
  h.clock.now += 20000;
  h.fireWin('pageshow');
  assert.deepEqual(h.timers.intervals(), all);
  assert.equal(h.gets('status').length, n + 1);
  assert.equal(h.s().nowMs, h.clock.now);           // gleich getaktet
  h.store.stop();
  h.fireWin('focus');
  assert.deepEqual(h.timers.intervals(), all);
  h.fireWin('focus');                               // läuft schon: nichts doppelt
  assert.deepEqual(h.timers.intervals(), all);
  // Versteckt: nicht starten.
  h.doc.hidden = true;
  h.doc.fire('visibilitychange');
  h.fireWin('focus');
  h.fireWin('pageshow');
  assert.deepEqual(h.timers.intervals(), []);
  h.doc.hidden = false;
  h.doc.fire('visibilitychange');
  h.store.stop();
});

test('M4: Halten-Knopf nennt die Aktion', async () => {
  const h = harness();
  h.store.init(ADMIN);
  await h.store.refreshStatus();
  await h.store.refreshKeys();
  const label = (kind) => {
    h.store.confirmAlarm(kind);
    const l = h.s().confirm && h.s().confirm.confirmLabel;
    h.store.closeConfirm();
    return l;
  };
  assert.equal(label('alarm'), 'Halten: Alarm senden');
  assert.equal(label('false_alarm'), 'Halten: Entwarnung senden');
  h.routes.alarm = () => reply(200, partialResult);
  await h.store.sendNasAlarm('alarm', '0');
  h.store.confirmResend();
  assert.equal(h.s().confirm.confirmLabel, 'Halten: erneut senden');
  h.store.closeConfirm();
  h.routes.status = () => reply(200, statusBody({ demo: { enabled: true, key_id: 2, label: 'Tom' } }));
  await h.store.refreshStatus();
  assert.equal(label('alarm'), 'Halten: Demo-Alarm senden');
  assert.equal(label('box_alarm'), 'Halten: Demo-Alarm senden');
  // Ohne eigenen Text: Standard fürs Halten bzw. „Bestätigen“.
  h.store.openConfirm({ title: 'x', text: 'y', hold: true, onConfirm: () => {} });
  assert.equal(h.s().confirm.confirmLabel, 'Gedrückt halten zum Senden');
  h.store.openConfirm({ title: 'x', text: 'y', hold: false, onConfirm: () => {} });
  assert.equal(h.s().confirm.confirmLabel, 'Bestätigen');
});

test('M5: beschädigte Empfängerliste – die Alarm-Frage nennt die Not-Liste', async () => {
  const h = harness();
  h.store.init(ADMIN);
  await h.store.refreshStatus();
  h.routes.keys_list = () => reply(200, { ok: false, code: 'keys_corrupt', message: 'Liste beschädigt.' });
  await h.store.refreshKeys();
  h.store.confirmAlarm('alarm');
  const t = h.s().confirm.text;
  assert.match(t, /Empfängerliste auf dem NAS ist beschädigt/);
  assert.match(t, /Not-Liste aus der NAS-Konfiguration/);
  assert.doesNotMatch(t, /noch nicht geladen/);
});

test('M6: Alarm-Frage holt die Empfängerliste neu – die offene Frage bleibt, wie sie ist', async () => {
  const h = harness();
  h.store.init(ADMIN);
  await h.store.refreshStatus();
  await h.store.refreshKeys();
  const n = h.gets('keys_list').length;
  h.routes.keys_list = () => reply(200, keysBody({ version: 6, keys: [...keysBody().keys, { id: 3, label: 'Anna', key_masked: 'an…01', muted: true, mute_volume: 3 }] }));
  h.store.confirmAlarm('alarm');
  const text = h.s().confirm.text;
  assert.match(text, /an 2 Empfänger/);
  await settle();
  assert.equal(h.gets('keys_list').length, n + 1);
  assert.equal(h.s().keys.keys.length, 3);
  assert.equal(h.s().confirm.text, text);           // Momentaufnahme bleibt
  h.store.closeConfirm();
  h.store.confirmAlarm('alarm');
  assert.match(h.s().confirm.text, /an 3 Empfänger/);
  // Nachsendung ebenso.
  h.store.closeConfirm();
  h.routes.alarm = () => reply(200, partialResult);
  await h.store.sendNasAlarm('alarm', '0');
  const m = h.gets('keys_list').length;
  h.store.confirmResend();
  await settle();
  assert.ok(h.gets('keys_list').length > m);
});
