// Sitzungen und Cookies der Go-Fassung – was sich nur dort fragen lässt:
// welche Sitzungen der Server gespeichert hat (sessions.json in seinem
// Datenordner; session.test.mjs kann das nur von außen prüfen) und der
// Geräte-Ausweis boss925_device, den es nur unter Go gibt (Entwurf §6.8,
// "bekannte Geräte zuerst"). Nur Go - backend.files nur in go-*.test.mjs.
import assert from 'node:assert/strict';
import { after, before, beforeEach, describe, it } from 'node:test';
import {
  ADMIN, BACKEND_KIND, postForm, rawLogin, request, startBackend,
} from './harness.mjs';

const SESSION = 'boss925_dashboard';
const DEVICE = 'boss925_device';

// Namen der Cookies, die eine Antwort setzt, in ihrer Reihenfolge.
const setNames = (res) => (res.headers.getSetCookie?.() ?? []).map((c) => c.split('=')[0]);
const setCookie = (res, name) => (res.headers.getSetCookie?.() ?? []).find((c) => c.startsWith(`${name}=`));
const pairOf = (c) => c.split(';')[0];
const maxAge = (c) => Number(c.split(';').map((p) => p.trim()).find((p) => /^max-age=/i.test(p))?.split('=')[1]);
// Ein Cookie in der Form einer Sitzungs-ID (43 Zeichen URL-Base64), das
// keine Sitzung nennt, und eins in ganz anderer Form.
const FORGED = [`${SESSION}=${'A'.repeat(43)}`, `${SESSION}=abcdefabcdefabcdefabcdefabcdef12`];

describe('Sitzungen und Cookies (nur Go)', { skip: BACKEND_KIND !== 'go' && 'nur Go' }, () => {
  let backend;
  before(async () => { backend = await startBackend(); });
  after(async () => { await backend?.stop(); });
  beforeEach(async () => { await backend.reset(); });

  const index = (cookie) => request(`${backend.base}/index.php`, { headers: cookie ? { cookie } : {} });
  const status = (cookie) => request(`${backend.base}/dashboard_api.php?action=status`, { headers: cookie ? { cookie } : {} });
  // Die gespeicherten Sitzungen: Server geordnet anhalten (er sichert dabei
  // alles), sessions.json lesen, wieder starten.
  async function storedSessions() {
    await backend.stopServer();
    const doc = backend.files.read('sessions.json');
    await backend.startServer();
    return doc === null ? {} : doc.sessions;
  }

  it('ein anonymer Aufruf legt keine Sitzung an', async () => {
    const calls = [
      await index(),
      await status(),
      await rawLogin(backend, ADMIN.user, 'falsch'),
      await rawLogin(backend, 'niemand', ADMIN.password),
    ];
    for (const forged of FORGED) calls.push(await status(forged), await index(forged));
    for (const r of calls) assert.ok([200, 401].includes(r.status), `HTTP ${r.status}`);
    assert.deepEqual(Object.keys(await storedSessions()), [], 'keine Sitzung gespeichert');

    // Gegenprobe: Eine gelungene Anmeldung steht danach in derselben Datei.
    const ok = await rawLogin(backend, ADMIN.user, ADMIN.password);
    assert.equal(ok.status, 302);
    assert.equal(Object.keys(await storedSessions()).length, 1, 'die Anmeldung ist gespeichert');
  });

  it('anonym, Fehlversuch und erfundenes Cookie setzen weder Sitzungs-Cookie noch Ausweis', async () => {
    const calls = {
      'Anmeldeseite': await index(),
      'API ohne Anmeldung': await status(),
      'classic.php': await request(`${backend.base}/classic.php`),
      'Fehlversuch': await rawLogin(backend, ADMIN.user, 'falsch'),
      'unbekannter Name': await rawLogin(backend, 'niemand', ADMIN.password),
    };
    for (const forged of FORGED) {
      calls[`API mit ${forged}`] = await status(forged);
      calls[`Seite mit ${forged}`] = await index(forged);
      calls[`Fehlversuch mit ${forged}`] = await rawLogin(backend, ADMIN.user, 'falsch', { cookie: forged });
    }
    for (const [what, r] of Object.entries(calls)) assert.deepEqual(setNames(r), [], what);
  });

  it('die Anmeldung setzt beide Cookies, das Sitzungs-Cookie zuerst; Abmelden löscht nur das Sitzungs-Cookie', async () => {
    const login = await rawLogin(backend, ADMIN.user, ADMIN.password);
    assert.equal(login.status, 302);
    assert.deepEqual(setNames(login), [SESSION, DEVICE], 'Sitzung, dann Ausweis');
    const session = pairOf(setCookie(login, SESSION));
    const device = pairOf(setCookie(login, DEVICE));
    const both = `${session}; ${device}`;

    // Angemeldete Anfragen erneuern nur die Sitzung, den Ausweis nicht.
    const st = await status(both);
    assert.equal(st.status, 200);
    assert.deepEqual(setNames(st), [SESSION]);
    const csrf = JSON.parse(st.text).csrf;

    const out = await postForm(`${backend.base}/index.php`, { action: 'logout', csrf }, { cookie: both });
    assert.equal(out.status, 302);
    assert.deepEqual(setNames(out), [SESSION], 'nur das Sitzungs-Cookie');
    assert.ok(maxAge(setCookie(out, SESSION)) <= 0, `gelöscht: ${setCookie(out, SESSION)}`);
    assert.equal((await status(both)).status, 401, 'abgemeldet');

    // Mit dem Ausweis von vorher: neue Sitzung, kein neuer Ausweis.
    const again = await rawLogin(backend, ADMIN.user, ADMIN.password, { cookie: device });
    assert.equal(again.status, 302);
    assert.deepEqual(setNames(again), [SESSION]);
  });
});
