// Meldungen an den Admin (dashboard_api.php: notify_list, notify_set,
// notify_test) und die Meldungen bei der Anmeldung: Was der Server an den
// Status-Key schickt, stellt der Admin im Dashboard ein - jede Einstellung
// ist eine Auswahl aus festen Werten, Unbekanntes wird abgelehnt, gespeichert
// wird nur ausdrücklich Gesetztes (alles andere folgt den Standards bzw. der
// Konfiguration, sekundengenau). Die Lese-Rolle erfährt davon nichts.
// Diese Datei braucht außer Anmelden, status und log_view nur die
// notify-Aktionen - keine Empfänger-Aktionen, keinen Status-Push, keinen Poll.
import assert from 'node:assert/strict';
import { after, before, beforeEach, describe, it } from 'node:test';
import {
  ADMIN, READER, STATUS_KEY, WATCHER_TOKEN, now, rawLogin, serverClock, session, settle, sha256,
  startBackend, waitFor,
} from './harness.mjs';

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

// Arten in der Reihenfolge der Oberfläche; outside_watcher nur mit Außen-Wächter.
const OLD_TYPES = ['box_offline', 'nas_web', 'demo_reminder', 'mute_reminder', 'permanent_mute_reminder', 'login_lockout'];
const NEW_TYPES = ['login_success', 'mute_changed', 'mute_expired', 'box_restart', 'box_health', 'weekly_report'];
const LEVELS = ['passive', 'active', 'timeSensitive', 'critical'];
const LEVEL_TEXT = {
  passive: 'leise (nur in der Mitteilungszentrale)',
  active: 'normal (mit Ton)',
  timeSensitive: 'dringend (durchbricht Fokus)',
};
const STAMP = /^\[\d\d\.\d\d\.\d{4} \d\d:\d\d\] /;
const PROXY = { trusted_proxies: ['127.0.0.1', '::1'], proxy_client_ip_header: 'X-Forwarded-For' };

const list = async () => {
  const res = await admin.get('notify_list');
  assert.equal(res.ok, true, JSON.stringify(res));
  return res;
};
const typeOf = (res, key) => res.types.find((t) => t.key === key);
const optionOf = (type, key) => type.options.find((o) => o.key === key);
const set = (type, key, value) => admin.post({ action: 'notify_set', type, key, value });
async function setOk(type, key, value) {
  const res = await set(type, key, value);
  assert.equal(res.ok, true, JSON.stringify(res));
  return res;
}
const probe = (type) => admin.post({ action: 'notify_test', type });
const toStatus = (mark) => backend.bark.to(STATUS_KEY, mark);

describe('Liste der Meldungen (notify_list)', () => {
  it('nur für den Admin: die Lese-Rolle bekommt 403 "readonly"', async () => {
    const res = await reader.get('notify_list');
    assert.equal(res.http, 403);
    assert.equal(res.ok, false);
    assert.equal(res.code, 'readonly');
    const own = await list();
    assert.equal(own.configured, true);
  });

  it('alle Arten in fester Reihenfolge, jede mit Dringlichkeit (level) und Ruhezeit-Verhalten (ignore_quiet)', async () => {
    const res = await list();
    assert.deepEqual(res.types.map((t) => t.key), [...OLD_TYPES, ...NEW_TYPES]);
    for (const type of res.types) {
      assert.deepEqual(type.options.slice(0, 2).map((o) => o.key), ['level', 'ignore_quiet'], type.key);
      const level = optionOf(type, 'level');
      assert.equal(level.value, 'passive', type.key);
      assert.deepEqual(level.choices.map((c) => c.value), LEVELS, type.key);
      const quiet = optionOf(type, 'ignore_quiet');
      assert.equal(quiet.value, '0', type.key);
      assert.deepEqual(quiet.choices.map((c) => c.value).sort(), ['0', '1'], type.key);
    }
  });

  it('ab Werk: die neuen Arten sind aus, die übrigen an', async () => {
    const res = await list();
    for (const key of OLD_TYPES) assert.equal(typeOf(res, key).enabled, true, key);
    for (const key of NEW_TYPES) assert.equal(typeOf(res, key).enabled, false, key);
  });

  it('"Außen-Wächter meldet sich nicht" gibt es nur mit eingerichtetem Außen-Wächter', async () => {
    assert.equal(typeOf(await list(), 'outside_watcher'), undefined);
    const rejected = await set('outside_watcher', 'enabled', '0');
    assert.equal(rejected.ok, false);

    await backend.configure({ watcher_token_sha256: sha256(WATCHER_TOKEN) });
    const res = await list();
    assert.deepEqual(res.types.map((t) => t.key),
      ['box_offline', 'nas_web', 'outside_watcher', ...OLD_TYPES.slice(2), ...NEW_TYPES]);
    assert.equal(typeOf(res, 'outside_watcher').enabled, true);
    assert.equal(typeOf(res, 'outside_watcher').important, true);
  });

  it('Standards aus der Konfiguration gelten sekundengenau: angeboten und ausgewählt', async () => {
    let offline = optionOf(typeOf(await list(), 'box_offline'), 'offline_after_s');
    assert.equal(offline.value, '600');
    assert.equal(offline.choices.find((c) => c.value === '600').text, '10 Min');

    await backend.configure({ watchdog_offline_after_seconds: 777, demo_reminder_after_seconds: 5000 });
    const res = await list();
    offline = optionOf(typeOf(res, 'box_offline'), 'offline_after_s');
    assert.equal(offline.value, '777');
    assert.deepEqual(offline.choices.map((c) => c.value), ['300', '600', '777', '900', '1800', '3600', '7200']);
    assert.equal(offline.choices.find((c) => c.value === '777').text, '777 s (aus config.php)');
    const demo = optionOf(typeOf(res, 'demo_reminder'), 'after_s');
    assert.equal(demo.value, '5000');
    assert.equal(demo.choices.find((c) => c.value === '5000').text, 'nach 5000 s (aus config.php)');
  });
});

describe('Einstellen (notify_set)', () => {
  it('unbekannte Art, Einstellung oder Wert: ok:false, nichts gespeichert', async () => {
    const before = await list();
    const unknown = 'Unbekannte Einstellung.';
    const tries = [
      [['gibtesnicht', 'enabled', '1'], unknown],
      [['box_offline', 'farbe', 'rot'], unknown],
      [['global', 'farbe', '1'], unknown],
      [['', 'enabled', '1'], unknown],
      [['box_offline', 'level', 'laut'], 'Ungültiger Wert für „Dringlichkeit“ – nichts geändert.'],
      [['box_offline', 'enabled', '2'], 'Ungültiger Wert für „Meldung“ – nichts geändert.'],
      [['box_offline', 'enabled', ''], 'Ungültiger Wert für „Meldung“ – nichts geändert.'],
      [['box_offline', 'offline_after_s', '301'], 'Ungültiger Wert für „Als offline melden nach“ – nichts geändert.'],
      [['global', 'critical_volume', '11'], 'Ungültiger Wert für „Lautstärke kritischer Meldungen“ – nichts geändert.'],
    ];
    for (const [[type, key, value], message] of tries) {
      const res = await set(type, key, value);
      assert.equal(res.ok, false, `${type}/${key}=${value}`);
      assert.equal(res.message, message, `${type}/${key}=${value}`);
    }
    assert.deepEqual(await list(), before);
    assert.equal(backend.data.exists('notify_settings.json'), false);
    assert.deepEqual((await admin.log('all')).filter((e) => e.event === 'notify_changed'), []);
  });

  it('ein angebotener Wert wird gespeichert und angezeigt; derselbe noch einmal ändert nichts', async () => {
    await setOk('box_offline', 'level', 'timeSensitive');
    await setOk('global', 'critical_volume', '8');
    const res = await list();
    assert.equal(optionOf(typeOf(res, 'box_offline'), 'level').value, 'timeSensitive');
    assert.equal(res.global.find((o) => o.key === 'critical_volume').value, '8');

    const again = await setOk('box_offline', 'level', 'timeSensitive');
    assert.equal(again.changed, false);
    assert.equal(again.message, 'Ist bereits so eingestellt.');
    assert.equal((await admin.log('all')).filter((e) => e.event === 'notify_changed').length, 2);
  });

  it('gespeichert wird nur ausdrücklich Gesetztes: die übrigen Standards folgen weiter der Konfiguration', async () => {
    await setOk('box_offline', 'repeat_hours', '3');
    await backend.configure({ watchdog_offline_after_seconds: 777 });
    const type = typeOf(await list(), 'box_offline');
    assert.equal(optionOf(type, 'repeat_hours').value, '3');
    assert.equal(optionOf(type, 'offline_after_s').value, '777');
  });
});

describe('Probe-Meldung (notify_test)', () => {
  it('geht als "PROBE: <Name>" an den Status-Key - auch bei abgeschalteter Art; die Antwort nennt die Dringlichkeit', async () => {
    let mark = backend.bark.mark();
    const res = await probe('login_success');   // ab Werk aus
    assert.deepEqual(res, { ok: true, message: `Probe gesendet – Dringlichkeit: ${LEVEL_TEXT.passive}.`, http: 200 });
    const sent = await waitFor(() => toStatus(mark)[0], 'Probe-Meldung');
    assert.equal(sent.fields.title, 'PROBE: Anmeldung am Dashboard');
    assert.match(sent.fields.body, STAMP);
    assert.equal(sent.fields.body.replace(STAMP, ''),
      'Probe-Meldung aus dem Dashboard. So kommt diese Art von Meldung bei dir an.');
    assert.equal(sent.fields.level, 'passive');
    await settle();
    assert.equal(backend.bark.since(mark).length, 1, 'genau eine Meldung');

    await setOk('login_success', 'level', 'timeSensitive');
    mark = backend.bark.mark();
    const urgent = await probe('login_success');
    assert.equal(urgent.message, `Probe gesendet – Dringlichkeit: ${LEVEL_TEXT.timeSensitive}.`);
    assert.equal((await waitFor(() => toStatus(mark)[0], 'dringende Probe')).fields.level, 'timeSensitive');
  });

  it('in der Ruhezeit leise - und die Antwort sagt warum; eine ausgenommene Art bleibt wie eingestellt', async () => {
    await setOk('login_success', 'level', 'active');
    // Ruhezeit ab der aktuellen Stunde des Servers für zwei Stunden.
    const { hour } = serverClock();
    await setOk('global', 'quiet_enabled', '1');
    await setOk('global', 'quiet_from', String(hour));
    await setOk('global', 'quiet_to', String((hour + 2) % 24));

    let mark = backend.bark.mark();
    const capped = await probe('login_success');
    assert.equal(capped.ok, true);
    assert.equal(capped.message,
      `Probe gesendet – Dringlichkeit: ${LEVEL_TEXT.passive} (gerade Ruhezeit, deshalb leise).`);
    assert.equal((await waitFor(() => toStatus(mark)[0], 'Probe in der Ruhezeit')).fields.level, 'passive');

    await setOk('login_success', 'ignore_quiet', '1');
    mark = backend.bark.mark();
    const loud = await probe('login_success');
    assert.equal(loud.message, `Probe gesendet – Dringlichkeit: ${LEVEL_TEXT.active}.`);
    assert.equal((await waitFor(() => toStatus(mark)[0], 'ausgenommene Probe')).fields.level, 'active');
  });

  it('ohne Status-Key: ok:false mit Hinweis auf bark_key_status, nichts gesendet', async () => {
    await backend.configure({ bark_key_status: '' });
    assert.equal((await list()).configured, false);
    const res = await probe('box_offline');
    assert.equal(res.ok, false);
    assert.match(res.message, /bark_key_status/);
    await settle();
    assert.deepEqual(backend.bark.requests, []);
  });

  it('unbekannte Art: ok:false, nichts gesendet', async () => {
    const res = await probe('gibtesnicht');
    assert.equal(res.ok, false);
    assert.equal(res.message, 'Unbekannte Art von Meldung.');
    await settle();
    assert.deepEqual(backend.bark.requests, []);
  });
});

describe('Meldungen bei der Anmeldung', () => {
  it('Login gesperrt: genau einmal, wenn die Fehlversuche die Grenze erreichen - leise, mit Absender und Minuten', async () => {
    await backend.configure({ login_max_failures: 3, login_lockout_window_seconds: 600 });
    const mark = backend.bark.mark();
    for (let i = 0; i < 2; i++) assert.equal((await rawLogin(backend, ADMIN.user, 'falsch')).status, 200);
    await settle();
    assert.deepEqual(toStatus(mark), [], 'vor der Grenze nichts');

    assert.equal((await rawLogin(backend, ADMIN.user, 'falsch')).status, 200);
    const sent = await waitFor(() => toStatus(mark)[0], 'Meldung "Login gesperrt"');
    assert.equal(sent.fields.title, 'Dashboard: Login gesperrt');
    assert.equal(sent.fields.level, 'passive');
    assert.match(sent.fields.body, STAMP);
    assert.equal(sent.fields.body.replace(STAMP, ''),
      'Zu viele fehlgeschlagene Login-Versuche von 127.0.0.1 - IP fuer 10 min gesperrt.');

    // Weitere Versuche (gesperrt, auch mit richtigem Passwort) melden nichts mehr.
    assert.equal((await rawLogin(backend, ADMIN.user, 'falsch')).status, 429);
    assert.equal((await rawLogin(backend, ADMIN.user, ADMIN.password)).status, 429);
    await settle();
    assert.equal(toStatus(mark).length, 1);
  });

  it('hinter dem Proxy nennt sie die Adresse des Besuchers', async () => {
    await backend.configure({ ...PROXY, login_max_failures: 2 });
    const mark = backend.bark.mark();
    for (let i = 0; i < 2; i++) {
      await rawLogin(backend, ADMIN.user, 'falsch', { 'x-forwarded-for': '10.9.9.9, 198.51.100.66' });
    }
    const sent = await waitFor(() => toStatus(mark)[0], 'Meldung "Login gesperrt"');
    assert.match(sent.fields.body, /von 198\.51\.100\.66 - IP fuer 15 min gesperrt\.$/);
    assert.equal(sent.fields.body.includes('10.9.9.9'), false);
  });

  it('Anmeldung am Dashboard: ab Werk keine Meldung', async () => {
    const mark = backend.bark.mark();
    assert.equal((await rawLogin(backend, ADMIN.user, ADMIN.password)).status, 302);
    assert.equal((await rawLogin(backend, READER.user, READER.password)).status, 302);
    await settle();
    assert.deepEqual(backend.bark.since(mark), []);
  });

  it('Anmeldung eingeschaltet: Meldung je nach "Melden bei" - alle, nur Admin, nur Lese-Benutzer', async () => {
    await setOk('login_success', 'enabled', '1');
    const ADMIN_TEXT = "Admin 'admin' hat sich angemeldet (von 127.0.0.1).";
    const READER_TEXT = "Lese-Benutzer 'leser' hat sich angemeldet (von 127.0.0.1).";
    // Meldet sich account an: welche Texte kamen beim Status-Key an?
    const loginTexts = async (account) => {
      const mark = backend.bark.mark();
      assert.equal((await rawLogin(backend, account.user, account.password)).status, 302);
      await settle();
      return toStatus(mark).map((r) => {
        assert.equal(r.fields.title, 'Dashboard: Anmeldung');
        assert.equal(r.fields.level, 'passive');
        assert.match(r.fields.body, STAMP);
        return r.fields.body.replace(STAMP, '');
      });
    };
    assert.deepEqual(await loginTexts(ADMIN), [ADMIN_TEXT], 'alle: Admin');
    assert.deepEqual(await loginTexts(READER), [READER_TEXT], 'alle: Lese-Benutzer');
    await setOk('login_success', 'who', 'admin');
    assert.deepEqual(await loginTexts(ADMIN), [ADMIN_TEXT], 'nur Admin: Admin');
    assert.deepEqual(await loginTexts(READER), [], 'nur Admin: Lese-Benutzer');
    await setOk('login_success', 'who', 'readonly');
    assert.deepEqual(await loginTexts(ADMIN), [], 'nur Lese-Benutzer: Admin');
    assert.deepEqual(await loginTexts(READER), [READER_TEXT], 'nur Lese-Benutzer: Lese-Benutzer');

    // Ein Fehlversuch ist keine Anmeldung.
    const mark = backend.bark.mark();
    await rawLogin(backend, READER.user, 'falsch');
    await settle();
    assert.deepEqual(toStatus(mark), []);
  });

  it('die Antwort auf die Anmeldung (302) wartet nicht auf Bark', async (t) => {
    // Der Test-Server von PHP (php -S) schickt die Antwort erst, wenn auch die
    // Arbeit "nach der Antwort" getan ist (fastcgi_finish_request() gibt es
    // nur unter PHP-FPM) - dort lässt sich das nicht prüfen.
    if (backend.kind === 'php') {
      t.skip('php -S antwortet erst nach den Nachlauf-Aufgaben');
      return;
    }
    await setOk('login_success', 'enabled', '1');
    backend.bark.respond = (req) => (req.key === STATUS_KEY ? { status: 200, delayMs: 3000 } : null);
    const mark = backend.bark.mark();
    const began = Date.now();
    const r = await rawLogin(backend, ADMIN.user, ADMIN.password);
    const took = Date.now() - began;
    assert.equal(r.status, 302);
    assert.ok(took < 1500, `Anmeldung brauchte ${took} ms`);
    const sent = await waitFor(() => toStatus(mark)[0], 'Meldung "Anmeldung"');
    assert.equal(sent.fields.title, 'Dashboard: Anmeldung');
  });
});

describe('Im Verlauf', () => {
  it('eine Änderung steht als notify_changed im Verlauf - nur für den Admin', async () => {
    await setOk('login_success', 'enabled', '1');
    const [entry] = (await admin.log('system')).filter((e) => e.event === 'notify_changed');
    assert.deepEqual({ ...entry, at: 0 }, {
      at: 0, event: 'notify_changed', by: 'admin', label: 'Anmeldung am Dashboard',
      key: 'enabled', option: 'Meldung', value: 'eingeschaltet',
    });
    for (const filter of ['all', 'system']) {
      assert.equal((await reader.log(filter)).some((e) => e.event === 'notify_changed'), false, filter);
    }
  });

  it('für die Lese-Rolle fällt notify_changed VOR dem Abzählen heraus', async () => {
    const at = now() - 1000;
    backend.data.appendLog([
      ...Array.from({ length: 5 }, (_, i) => ({
        event: 'key_added', by: 'admin', label: `K${i + 1}`, key: 'kkkk...', log_at: at + i,
      })),
      ...Array.from({ length: 30 }, (_, i) => ({
        event: 'notify_changed', by: 'admin', type: 'box_offline', key: 'level', label: 'Box offline / wieder online',
        option: 'Dringlichkeit', value: `Stufe ${i}`, log_at: at + 10 + i,
      })),
    ]);
    const forReader = (await reader.get('log_view', { limit: 5, filter: 'all' })).entries;
    assert.deepEqual(forReader.map((e) => `${e.event}:${e.label}`),
      ['key_added:K5', 'key_added:K4', 'key_added:K3', 'key_added:K2', 'key_added:K1']);
    const forAdmin = (await admin.get('log_view', { limit: 5, filter: 'all' })).entries;
    assert.deepEqual(forAdmin.map((e) => `${e.event}:${e.value}`),
      ['Stufe 29', 'Stufe 28', 'Stufe 27', 'Stufe 26', 'Stufe 25'].map((v) => `notify_changed:${v}`));
  });
});
