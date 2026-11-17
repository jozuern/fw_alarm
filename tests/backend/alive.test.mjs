// Lebenszeichen für den Außen-Wächter (api/alive.php): Ein ANDERER Rechner
// klopft alle 5 Minuten über die öffentliche Adresse an. Er erfährt, ob der
// Server antwortet, wie alt der letzte Status der Box und der letzte Lauf des
// Wächters sind - und der Server merkt sich jeden Aufruf (Gegenüberwachung).
// Eigenes Token, nie das der Box. Jede Regel hier muss auch eine neue
// Server-Version erfüllen; das Skript unter cloud_watcher/ bleibt dasselbe.
// (cron_age_s=-1 vor dem ersten Lauf und stored=0 beim gescheiterten
// Speichern gelten nur für PHP: php-storage.test.mjs.)
import assert from 'node:assert/strict';
import { after, before, beforeEach, describe, it } from 'node:test';
import {
  MACHINE_TOKEN, READER, WATCHER_TOKEN, box, now, postForm, request, session, sha256, startBackend,
} from './harness.mjs';

// Konfiguration mit eingerichtetem Außen-Wächter.
const WATCHER = { watcher_token_sha256: sha256(WATCHER_TOKEN) };
// Die Zeilen der Antwort, in dieser Reihenfolge.
const LINES = ['ok', 'nonce', 'stored', 'box_age_s', 'cron_age_s', 'cron_configured', 'nas_send_failed'];

let backend;
let admin;
before(async () => { backend = await startBackend(); });
after(async () => { await backend.stop(); });
beforeEach(async () => {
  await backend.reset();
  admin = await session(backend);
});

const url = () => `${backend.base}/api/alive.php`;
// Ein Aufruf des Außen-Wächters (Felder mit undefined werden weggelassen).
const knock = (fields = {}) => postForm(url(), { token: WATCHER_TOKEN, nonce: 'n1', ...fields });
// Was der Server vom letzten gültigen Aufruf gespeichert hat (null = nie).
const seen = () => backend.state.watcher();
const watchdogInfo = async (who = admin) => (await who.get('status')).watchdog;
const near = (actual, expected, slack = 3) => Math.abs(Number(actual) - expected) <= slack;

describe('Nicht eingerichtet', () => {
  it('ohne watcher_token_sha256: 404 für GET und POST, auch mit Token', async () => {
    const tries = [
      await request(url()),
      await request(`${url()}?token=${WATCHER_TOKEN}`),
      await postForm(url(), {}),
      await knock(),
      await postForm(url(), { token: MACHINE_TOKEN, nonce: 'n1' }),
    ];
    for (const r of tries) {
      assert.equal(r.status, 404);
      assert.equal(r.text, '');
    }
    assert.equal(await seen(), null);
  });

  it('derselbe Hash wie das Maschinen-Token der Box: 404 - der Wächter darf das Box-Token nicht besitzen', async () => {
    for (const hash of [sha256(MACHINE_TOKEN), `  ${sha256(MACHINE_TOKEN).toUpperCase()} `]) {
      await backend.configure({ watcher_token_sha256: hash });
      const tries = [
        await request(url()),
        await postForm(url(), { token: MACHINE_TOKEN, nonce: 'n1' }),
        await knock(),
      ];
      for (const r of tries) {
        assert.equal(r.status, 404);
        assert.equal(r.text, '');
      }
    }
    assert.equal(await seen(), null);
  });
});

describe('Anmeldung mit dem eigenen Token', () => {
  beforeEach(async () => { await backend.configure(WATCHER); });

  it('GET, kein Token, falsches Token, Token der Box, Token nur in der Adresse: 403 FORBIDDEN', async () => {
    const tries = [
      await request(url()),                                            // GET
      await request(`${url()}?token=${WATCHER_TOKEN}`),                // GET mit Token
      await postForm(url(), { nonce: 'n1' }),                          // kein Token
      await postForm(url(), { token: '', nonce: 'n1' }),               // leeres Token
      await postForm(url(), { token: 'falsch', nonce: 'n1' }),         // falsches Token
      await postForm(url(), { token: MACHINE_TOKEN, nonce: 'n1' }),    // das Token der Box
      await postForm(`${url()}?token=${WATCHER_TOKEN}`, { nonce: 'n1' }),   // Token nur in der Adresse
    ];
    for (const r of tries) {
      assert.equal(r.status, 403);
      assert.equal(r.text, 'FORBIDDEN');
    }
    assert.equal(await seen(), null, 'ungültige Aufrufe werden nicht gemerkt');
    assert.equal((await watchdogInfo()).outside_seen_at, 0);
  });

  it('der Hash in der Konfiguration darf groß geschrieben sein und Leerzeichen haben', async () => {
    await backend.configure({ watcher_token_sha256: `  ${sha256(WATCHER_TOKEN).toUpperCase()} ` });
    const r = await knock();
    assert.equal(r.status, 200);
    assert.equal(r.kv.ok, '1');
  });

  it('das Token des Außen-Wächters öffnet die Endpunkte der Box nicht', async () => {
    const asBox = box(backend, { token: WATCHER_TOKEN });
    assert.equal((await asBox.status()).status, 403);
    assert.equal((await asBox.poll()).status, 403);
    assert.equal((await asBox.keys()).status, 403);
    assert.equal(await backend.state.boxStatus(), null);
  });
});

describe('Antwort auf einen gültigen Aufruf', () => {
  beforeEach(async () => { await backend.configure(WATCHER); });

  it('die Nonce kommt gespiegelt zurück - nur Buchstaben und Ziffern, höchstens 64 Zeichen', async () => {
    assert.equal((await knock({ nonce: 'Abc123xyz' })).kv.nonce, 'Abc123xyz');
    const long = await knock({ nonce: `ab-_.!cd${'x'.repeat(100)}` });
    assert.equal(long.kv.nonce, `abcd${'x'.repeat(60)}`);
    assert.equal(long.kv.nonce.length, 64);
    // Zeilenumbrüche in der Nonce können keine Zeile der Antwort fälschen.
    const forged = await knock({ nonce: 'n1\nok=0\nstored=9' });
    assert.equal(forged.kv.nonce, 'n1ok0stored9');
    assert.equal(forged.kv.ok, '1');
    assert.equal(forged.text.slice(0, -1).split('\n').length, LINES.length);
  });

  it('box_age_s: -1 vor dem ersten Status-Push, danach sein Alter in Sekunden', async () => {
    assert.equal((await knock()).kv.box_age_s, '-1');
    await box(backend).status();
    const fresh = await knock();
    assert.ok(near(fresh.kv.box_age_s, 0), `frisch: ${fresh.kv.box_age_s}`);
    await backend.state.ageBoxStatus(500);
    const aged = await knock();
    assert.ok(near(aged.kv.box_age_s, 500), `nach 500 s: ${aged.kv.box_age_s}`);
  });

  it('nas_send_failed=1, solange die letzte Meldung des Wächters scheiterte und keine spätere gelang', async () => {
    const t = now();
    const cases = [
      [{ last_run_at: t, last_send_ok_at: t - 100, last_send_failed_at: t - 10 }, '1'],   // zuletzt gescheitert
      [{ last_run_at: t, last_send_failed_at: t - 10 }, '1'],                              // noch nie gelungen
      [{ last_run_at: t, last_send_ok_at: t - 10, last_send_failed_at: t - 100 }, '0'],   // danach wieder gelungen
      [{ last_run_at: t, last_send_ok_at: t - 10, last_send_failed_at: t - 10 }, '0'],    // gleichzeitig = kein Fehler
      [{ last_run_at: t, last_send_ok_at: t - 10 }, '0'],
      [{ last_run_at: t }, '0'],
    ];
    for (const [state, expected] of cases) {
      // Genau diese Merker - was im Fall fehlt, wird gelöscht.
      await backend.state.setMarks({ last_send_ok_at: null, last_send_failed_at: null, ...state });
      assert.equal((await knock()).kv.nas_send_failed, expected, JSON.stringify(state));
    }
  });

  it('ältere Wächter-Skripte ohne nonce und bark_failed_at bekommen ok=1 (leere nonce-Zeile)', async () => {
    const r = await postForm(url(), { token: WATCHER_TOKEN });
    assert.equal(r.status, 200);
    const lines = r.text.slice(0, -1).split('\n');
    assert.equal(lines[0], 'ok=1');
    assert.equal(lines[1], 'nonce=');
    assert.equal(lines[2], 'stored=1');
    assert.deepEqual(lines.map((line) => line.slice(0, line.indexOf('='))), LINES);
    assert.equal((await seen()).bark_failed_at, 0);
  });
});

describe('Gegenüberwachung: Jeder gültige Aufruf wird gemerkt', () => {
  beforeEach(async () => { await backend.configure(WATCHER); });

  it('watcher_seen.json hält den Zeitpunkt, das Dashboard zeigt ihn als outside_seen_at', async () => {
    const before = await watchdogInfo();
    assert.equal(before.outside_seen_at, 0);
    assert.equal(before.outside_bark_failed_at, 0);

    assert.equal((await knock()).kv.stored, '1');
    assert.ok(near((await seen()).seen_at, now()), 'seen_at = Ankunft');
    const after = await watchdogInfo();
    assert.equal(after.outside_seen_at, (await seen()).seen_at);
    assert.equal(after.outside_bark_failed_at, 0);
    assert.equal(after.outside_silent_after_s, 3600);
    assert.equal(after.outside_alert, true);

    // Die Lese-Rolle sieht die Zeiten, aber nichts über die Meldungen des Admins.
    const reader = await watchdogInfo(await session(backend, READER));
    assert.equal(reader.outside_seen_at, (await seen()).seen_at);
    assert.equal('outside_alert' in reader, false);
  });

  it('ein späterer Aufruf ersetzt den Zeitpunkt, ein ungültiger lässt ihn stehen', async () => {
    await knock();
    await backend.state.ageWatcher(5000);
    const old = (await seen()).seen_at;

    assert.equal((await postForm(url(), { token: 'falsch', nonce: 'n1' })).status, 403);
    assert.equal((await postForm(url(), { token: MACHINE_TOKEN, nonce: 'n1' })).status, 403);
    assert.equal((await request(`${url()}?token=${WATCHER_TOKEN}`)).status, 403);
    assert.equal((await seen()).seen_at, old);
    assert.equal((await watchdogInfo()).outside_seen_at, old);

    assert.equal((await knock()).kv.ok, '1');
    assert.ok(near((await seen()).seen_at, now()));
    assert.ok(near((await watchdogInfo()).outside_seen_at, now()));
  });

  it('bark_failed_at wird gemerkt und im Dashboard gezeigt; Zukunft wird auf jetzt gekappt, negativ oder fehlend ist 0', async () => {
    const failedAt = now() - 600;
    assert.equal((await knock({ bark_failed_at: failedAt })).kv.ok, '1');
    assert.equal((await seen()).bark_failed_at, failedAt);
    assert.equal((await watchdogInfo()).outside_bark_failed_at, failedAt);

    await knock({ bark_failed_at: now() + 5000 });
    const capped = (await seen()).bark_failed_at;
    assert.ok(near(capped, now()), `Zukunft: ${capped}`);
    assert.ok(near((await watchdogInfo()).outside_bark_failed_at, now()));

    await knock({ bark_failed_at: -5 });
    assert.equal((await seen()).bark_failed_at, 0);
    await knock({ bark_failed_at: failedAt });
    await knock({ bark_failed_at: 'kaputt' });
    assert.equal((await seen()).bark_failed_at, 0);
    await knock({ bark_failed_at: failedAt });
    // Fehlt das Feld (der Wächter kann wieder melden), ist die Störung vorbei.
    await knock();
    assert.equal((await seen()).bark_failed_at, 0);
    assert.equal((await watchdogInfo()).outside_bark_failed_at, 0);
  });
});

describe('Dashboard ohne Außen-Wächter', () => {
  it('ist er nicht eingerichtet, hat die Status-Antwort keine outside_-Felder', async () => {
    // Auch ein liegengebliebener Merker aus früherer Zeit ändert daran nichts.
    await backend.state.setWatcher({ seenAgo: 60 });
    for (const who of [admin, await session(backend, READER)]) {
      const info = await watchdogInfo(who);
      assert.deepEqual(Object.keys(info).filter((name) => name.startsWith('outside_')), []);
      assert.equal(typeof info.configured, 'boolean');
    }
  });
});
