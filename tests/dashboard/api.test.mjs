// Tests für nas_dashboard/assets/app/api.js (FW.api): Fehlerarten, Zeitlimits,
// CSRF, Poller. fetch/AbortSignal/FormData/document sind Attrappen.
import test from 'node:test';
import assert from 'node:assert/strict';
import { loadScripts } from './helpers.mjs';

const FILES = ['nas_dashboard/assets/app/logic.js', 'nas_dashboard/assets/app/api.js'];

// Antwort im Stil von fetch(): status, ok, text().
function reply(status, body, textError) {
  return {
    status,
    ok: status >= 200 && status < 300,
    text: textError
      ? async () => { throw textError; }
      : async () => (typeof body === 'string' ? body : JSON.stringify(body))
  };
}

class FakeFormData {
  constructor() { this.map = new Map(); }
  set(key, value) { this.map.set(key, String(value)); }
  get(key) { return this.map.has(key) ? this.map.get(key) : null; }
  has(key) { return this.map.has(key); }
}

const settle = async () => { for (let i = 0; i < 10; i += 1) await new Promise((r) => setImmediate(r)); };

function setup(handler, extra = {}) {
  const calls = [];
  const inputs = [{ value: 'alt' }, { value: 'alt' }];
  const globals = Object.assign({
    fetch: async (url, opts) => { calls.push({ url, opts }); return handler(url, opts, calls.length); },
    AbortSignal: { timeout: (ms) => ({ timeoutMs: ms }) },
    setTimeout,
    clearTimeout,
    FormData: FakeFormData,
    document: { querySelectorAll: (sel) => (sel === 'form input[name="csrf"]' ? inputs : []) }
  }, extra);
  const ctx = loadScripts(FILES, globals);
  return { api: ctx.FW.api, calls, inputs };
}

test('401 → auth + callback', async () => {
  const { api } = setup(() => reply(401, { ok: false }));
  let lost = 0;
  api.onAuthLoss(() => { lost += 1; });
  await assert.rejects(api.get('status'), (e) => e.kind === 'auth' && e.status === 401);
  assert.equal(lost, 1);
  assert.equal(api.isAuthLost(), true);
});

test('409 mode_changed keeps data', async () => {
  const demo = { enabled: true, key_id: 3, label: 'Anna' };
  const { api } = setup(() => reply(409, { ok: false, code: 'mode_changed', message: 'Modus geändert', demo }));
  await assert.rejects(api.post({ action: 'alarm', expect_demo: '0' }),
    (e) => e.kind === 'mode_changed' && e.status === 409 && e.data.demo.key_id === 3 && e.message === 'Modus geändert');
});

test('409 ohne mode_changed bleibt http', async () => {
  const { api } = setup(() => reply(409, { ok: false, message: 'Konflikt' }));
  await assert.rejects(api.post({ action: 'x' }), (e) => e.kind === 'http' && e.status === 409 && e.message === 'Konflikt');
});

test('403 old server without code but message → readonly', async () => {
  const { api } = setup(() => reply(403, { ok: false, message: 'Nur Lesezugriff.' }));
  let csrfErrors = 0;
  api.onCsrfError(() => { csrfErrors += 1; });
  await assert.rejects(api.post({ action: 'keys_add' }), (e) => e.kind === 'readonly' && e.message === 'Nur Lesezugriff.');
  assert.equal(csrfErrors, 0);
  const second = setup(() => reply(403, { ok: false, code: 'readonly', message: 'Nur Admin.' }));
  await assert.rejects(second.api.get('notify_list'), (e) => e.kind === 'readonly');
});

test('403 with code csrf → csrf + onCsrfError, no retry', async () => {
  const { api, calls } = setup(() => reply(403, { ok: false, code: 'csrf', message: 'Token ungültig' }));
  let csrfErrors = 0;
  api.onCsrfError(() => { csrfErrors += 1; });
  await assert.rejects(api.post({ action: 'alarm', expect_demo: '0' }), (e) => e.kind === 'csrf');
  assert.equal(calls.length, 1);
  assert.equal(csrfErrors, 1);
  // Älterer Server: 403 nur mit {ok:false} (ohne code und message) = CSRF.
  const old = setup(() => reply(403, { ok: false }));
  await assert.rejects(old.api.post({ action: 'cancel' }), (e) => e.kind === 'csrf');
  assert.equal(old.calls.length, 1);
});

test('500 html → http with short text', async () => {
  const html = `<html><body><h1>500 Internal Server Error</h1><p>${'x'.repeat(300)}</p></body></html>`;
  const { api } = setup(() => reply(500, html));
  await assert.rejects(api.get('status'), (e) => {
    assert.equal(e.kind, 'http');
    assert.equal(e.status, 500);
    assert.ok(e.message.startsWith('500 Internal Server Error x'));
    assert.ok(!e.message.includes('<'));
    assert.equal(e.message.length, 201);
    assert.ok(e.message.endsWith('…'));
    return true;
  });
});

test('Fehler-JSON und kaputtes JSON', async () => {
  const a = setup(() => reply(500, { ok: false, message: 'Datenordner gesperrt' }));
  await assert.rejects(a.api.get('status'), (e) => e.kind === 'http' && e.message === 'Datenordner gesperrt' && e.data.ok === false);
  const b = setup(() => reply(200, 'Warning: blah'));
  await assert.rejects(b.api.get('status'), (e) => e.kind === 'http' && e.status === 200
    && e.message === 'Antwort des NAS ist kein gültiges JSON: Warning: blah');
  const c = setup(() => reply(200, ''));
  await assert.rejects(c.api.get('status'), (e) => e.kind === 'http' && e.message === 'Antwort des NAS ist kein gültiges JSON.');
  const d = setup(() => reply(200, { ok: true, x: 1 }));
  assert.equal((await d.api.get('status')).x, 1);
});

test('abort → timeout', async () => {
  const a = setup(() => { const e = new Error('t'); e.name = 'TimeoutError'; throw e; });
  await assert.rejects(a.api.get('status'), (e) => e.kind === 'timeout' && e.status === 0);
  const b = setup(() => { const e = new Error('a'); e.name = 'AbortError'; throw e; });
  await assert.rejects(b.api.get('status'), (e) => e.kind === 'timeout');
  const c = setup(() => { throw new TypeError('Failed to fetch'); });
  await assert.rejects(c.api.get('status'), (e) => e.kind === 'network');
  // Kopfzeilen kamen an, der Inhalt nicht: Status bleibt erhalten.
  const abort = new Error('a'); abort.name = 'AbortError';
  const d = setup(() => reply(200, '', abort));
  await assert.rejects(d.api.post({ action: 'alarm' }), (e) => e.kind === 'timeout' && e.status === 200);
  const f = setup(() => reply(502, '', new TypeError('reset')));
  await assert.rejects(f.api.get('status'), (e) => e.kind === 'network' && e.status === 502);
});

test('Zeitlimit: AbortSignal.timeout mit dem Limit der Anfrageart', async () => {
  const { api, calls } = setup(() => reply(200, { ok: true }));
  await api.get('status');
  await api.post({ action: 'keys_add' });
  await api.post({ action: 'alarm' }, api.TIMEOUT.ALARM);
  assert.deepEqual(calls.map((c) => c.opts.signal.timeoutMs), [10000, 20000, 120000]);
  assert.deepEqual({ ...api.TIMEOUT }, { POLL: 10000, POST: 20000, SEND_ONE: 30000, ALARM: 120000 });
  assert.equal(calls[0].opts.credentials, 'same-origin');
});

test('Zeitlimit ohne AbortSignal.timeout: AbortController + Timer', async () => {
  const timers = [];
  const cleared = [];
  class FakeController {
    constructor() { this.signal = { aborted: false, listeners: [] }; }
    abort() {
      this.signal.aborted = true;
      this.signal.listeners.forEach((fn) => fn());
    }
  }
  const { api } = setup((url, opts) => {
    if (url.includes('hang')) {
      return new Promise((resolve, reject) => {
        opts.signal.listeners.push(() => { const e = new Error('aborted'); e.name = 'AbortError'; reject(e); });
      });
    }
    return reply(200, { ok: true });
  }, {
    AbortSignal: undefined,
    AbortController: FakeController,
    setTimeout: (fn, ms) => { timers.push({ fn, ms }); return timers.length; },
    clearTimeout: (id) => { cleared.push(id); }
  });
  await api.get('status');
  assert.deepEqual(cleared, [1]);   // Timer nach der Antwort gelöscht
  const pending = api.get('hang');
  await settle();
  assert.equal(timers[1].ms, 10000);
  timers[1].fn();
  await assert.rejects(pending, (e) => e.kind === 'timeout');
});

test('get baut die Adresse, post schickt FormData mit X-CSRF-Token', async () => {
  const { api, calls, inputs } = setup(() => reply(200, { ok: true }));
  await api.get('log_view', { limit: 25, filter: 'a&b', skip: undefined });
  assert.equal(calls[0].url, 'dashboard_api.php?action=log_view&limit=25&filter=a%26b');
  api.setCsrf('tok1');
  assert.equal(api.getCsrf(), 'tok1');
  assert.deepEqual(inputs.map((i) => i.value), ['tok1', 'tok1']);
  await api.post({ action: 'keys_mute', id: 3, muted: '1', leer: null });
  const { opts } = calls[1];
  assert.equal(calls[1].url, 'dashboard_api.php');
  assert.equal(opts.method, 'POST');
  assert.equal(opts.headers['X-CSRF-Token'], 'tok1');
  assert.equal(opts.body.get('action'), 'keys_mute');
  assert.equal(opts.body.get('id'), '3');
  assert.equal(opts.body.has('leer'), false);
});

test('poller: tick skipped while busy, explicit run re-runs once', async () => {
  const { api } = setup(() => reply(200, { ok: true }));
  let calls = 0;
  let release = null;
  const run = api.makePoller(() => { calls += 1; return new Promise((r) => { release = r; }); });
  const first = run();
  run.tick();                  // Takt während der Abfrage: ausgelassen
  const second = run();        // ausdrücklich: läuft einmal hinterher
  const third = run();         // ... aber nur EINmal
  assert.equal(calls, 1);
  release();
  await settle();
  assert.equal(calls, 2);
  release();
  await Promise.all([first, second, third]);
  assert.equal(calls, 2);
  run.tick();                  // nicht beschäftigt: Takt läuft
  assert.equal(calls, 3);
  release();
  await settle();
  assert.equal(calls, 3);
});

test('poller: ausdrücklicher Aufruf wartet auf den Nachlauf', async () => {
  const { api } = setup(() => reply(200, { ok: true }));
  const order = [];
  let release = null;
  const run = api.makePoller(() => new Promise((r) => { release = r; }));
  run().then(() => order.push('erster'));
  const later = run().then(() => order.push('nachlauf'));
  release();
  await settle();
  assert.deepEqual(order, ['erster']);
  release();
  await later;
  assert.deepEqual(order, ['erster', 'nachlauf']);
});

test('poller: kein Nachlauf nach Abmeldung', async () => {
  let status = 200;
  const { api } = setup(() => reply(status, { ok: true }));
  let calls = 0;
  let release = null;
  const run = api.makePoller(() => { calls += 1; return new Promise((r) => { release = r; }); });
  const first = run();
  const second = run();
  status = 401;
  await assert.rejects(api.get('status'));
  release();
  await Promise.all([first, second]);
  assert.equal(calls, 1);
});

test('errorText and errorKind', async () => {
  const { api } = setup(() => reply(200, { ok: true }));
  const E = api.ApiError;
  assert.equal(api.errorText(new Error('x')), 'Anfrage fehlgeschlagen.');
  assert.equal(api.errorText(new E('auth', '', 401)), 'Sitzung abgelaufen – die Anmeldeseite wird geladen…');
  assert.equal(api.errorText(new E('csrf', '', 403)), 'Sicherheits-Token veraltet – Seite neu laden. Aktion wurde NICHT ausgeführt.');
  api.setCsrf('neu', true);   // Token kommt ab jetzt mit der Status-Antwort
  assert.equal(api.csrfHint(), 'Sicherheits-Token erneuert – bitte erneut bestätigen');
  assert.equal(api.errorText(new E('csrf', '', 403)), 'Sicherheits-Token erneuert – bitte erneut bestätigen. Aktion wurde NICHT ausgeführt.');
  assert.equal(api.errorText(new E('readonly', '', 403)), 'Nur Lesezugriff – Aktion wurde NICHT ausgeführt.');
  assert.equal(api.errorText(new E('readonly', 'Nur Admin.', 403)), 'Nur Admin.');
  assert.equal(api.errorText(new E('mode_changed', '', 409)), 'Der Demo-/LIVE-Modus hat sich geändert – Aktion wurde NICHT ausgeführt.');
  assert.match(api.errorText(new E('timeout', '', 0)), /^Keine Antwort vom NAS \(Zeitüberschreitung\) – ob die Aktion ausgeführt wurde, ist unklar/);
  assert.equal(api.errorText(new E('network', '', 0)), 'NAS nicht erreichbar – Verbindung prüfen.');
  assert.equal(api.errorText(new E('http', 'kaputt', 500)), 'Fehler (HTTP 500): kaputt');
  assert.equal(api.errorText(new E('http', '', 0)), 'Fehler.');
  assert.equal(api.errorKind(new E('timeout', '', 0)), 'warn');
  assert.equal(api.errorKind(new E('network', '', 0)), 'error');
  assert.equal(api.errorKind(new Error('x')), 'error');
  assert.equal(api.kindOf({ ok: true }), 'ok');
  assert.equal(api.kindOf({ ok: false }), 'error');
});
