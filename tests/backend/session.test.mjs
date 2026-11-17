// Anmeldung und Sitzung am Dashboard (POST index.php, Cookie boss925_dashboard):
// Das Cookie gilt 12 Std (übersteht das Schließen der iPhone-App), wird bei
// jeder angemeldeten Anfrage erneuert und beim Abmelden gelöscht. Dazu die
// Sicherungen: keine Sitzung für Unbekannte, Anmeldung nur vom eigenen
// Formular, ein geändertes Passwort meldet ab, die Bremse gegen
// Durchprobieren und das Verhalten hinter einem Proxy/Tunnel.
// Diese Datei braucht nur Anmelden/Abmelden, die Seiten index.php und
// classic.php, die Aktion status und abgelehnte Aufrufe - keinen Status-Push
// der Box.
import assert from 'node:assert/strict';
import { existsSync, readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { after, before, beforeEach, describe, it } from 'node:test';
import {
  ADMIN, OTHER_PASSWORD_HASH, READER, now, postForm, rawLogin, request, session, startBackend,
} from './harness.mjs';

let backend;
before(async () => { backend = await startBackend(); });
after(async () => { await backend.stop(); });
beforeEach(async () => { await backend.reset(); });

const NAME = 'boss925_dashboard';
const FAILED = 'Login fehlgeschlagen.';
const IDLE_MAX = 43200;   // 12 Stunden

// ---- Cookies einer Antwort ---------------------------------------------------
const cookies = (res) => res.headers.getSetCookie?.() ?? [];
const find = (res) => cookies(res).find((c) => c.startsWith(`${NAME}=`));
const pairOf = (c) => c.split(';')[0];
const attr = (c, n) => c.split(';').map((p) => p.trim())
  .find((p) => p.toLowerCase().startsWith(`${n.toLowerCase()}=`))?.split('=')[1];
// Restlaufzeit in Sekunden: Max-Age, sonst aus expires.
function expiresIn(c) {
  const maxAge = attr(c, 'Max-Age');
  if (maxAge !== undefined) return Number(maxAge);
  const expires = attr(c, 'expires');
  return expires === undefined ? null : (Date.parse(expires) - Date.now()) / 1000;
}
const isSecure = (c) => /;\s*secure/i.test(c);

// ---- Aufrufe ---------------------------------------------------------------
const statusUrl = () => `${backend.base}/dashboard_api.php?action=status`;
const status = (cookie) => request(statusUrl(), { headers: cookie ? { cookie } : {} });
// Als Admin anmelden und das Sitzungs-Cookie (name=wert) liefern.
async function loginCookie() {
  const r = await rawLogin(backend, ADMIN.user, ADMIN.password);
  assert.equal(r.status, 302, `Anmeldung: HTTP ${r.status}`);
  return pairOf(find(r));
}
const csrfOf = async (cookie) => JSON.parse((await status(cookie)).text).csrf;
const logout = (cookie, fields) => postForm(`${backend.base}/index.php`, { action: 'logout', ...fields }, { cookie });
// Sitzungsdateien, wo der Prüfstand sie sehen kann (Go); unter PHP liegen sie
// außerhalb des Prüfstands - dann gibt es hier nichts zu prüfen.
const sessionFiles = () => (backend.sessionsDir && existsSync(backend.sessionsDir)
  ? readdirSync(backend.sessionsDir) : []);

describe('Ohne Anmeldung keine Sitzung', () => {
  it('die Anmeldeseite setzt kein Cookie und ist nicht zwischenspeicherbar', async () => {
    const r = await request(`${backend.base}/index.php`);
    assert.equal(r.status, 200);
    assert.deepEqual(cookies(r), []);
    assert.match(r.headers.get('cache-control'), /no-store/);
    assert.deepEqual(sessionFiles(), []);
  });

  it('eine anonyme API-Anfrage: 401 {"ok":false}, kein Cookie, nicht zwischenspeicherbar', async () => {
    const r = await status();
    assert.equal(r.status, 401);
    assert.deepEqual(JSON.parse(r.text), { ok: false });
    assert.deepEqual(cookies(r), []);
    assert.match(r.headers.get('cache-control'), /no-store/);
    assert.deepEqual(sessionFiles(), []);
  });

  it('eine erfundene Sitzungs-ID: 401, weder ein neues noch ein Lösch-Cookie', async () => {
    const r = await status(`${NAME}=abcdefabcdefabcdefabcdefabcdef12`);
    assert.equal(r.status, 401);
    assert.deepEqual(cookies(r), []);
    assert.deepEqual(sessionFiles(), []);
  });

  it('ein Fehlversuch setzt kein Cookie', async () => {
    const r = await rawLogin(backend, ADMIN.user, 'falsch');
    assert.equal(r.status, 200);
    assert.ok(r.text.includes(FAILED));
    assert.deepEqual(cookies(r), []);
    assert.deepEqual(sessionFiles(), []);
  });

  it('classic.php ohne Anmeldung: Weiterleitung ohne Cookie', async () => {
    const r = await request(`${backend.base}/classic.php`);
    assert.equal(r.status, 302);
    assert.deepEqual(cookies(r), []);
  });
});

describe('Anmeldung nur vom eigenen Formular', () => {
  it('Sec-Fetch-Site cross-site und same-site: 403 ohne Cookie - und nicht als Fehlversuch gezählt', async () => {
    await backend.configure({ login_max_failures: 2 });
    for (const site of ['cross-site', 'same-site']) {
      for (const password of [ADMIN.password, 'falsch']) {
        const r = await rawLogin(backend, ADMIN.user, password, { 'sec-fetch-site': site });
        assert.equal(r.status, 403, `${site}, war HTTP ${r.status}`);
        assert.deepEqual(cookies(r), []);
        assert.ok(r.text.includes('Anmeldung nur direkt über diese Seite möglich.'));
      }
    }
    // Vier abgewiesene Versuche, Sperre ab zwei: Gezählt wurde keiner.
    const own = await rawLogin(backend, ADMIN.user, ADMIN.password);
    assert.equal(own.status, 302);
  });

  it('same-origin, none und ohne Sec-Fetch-Site: Anmeldung geht (302 mit Cookie)', async () => {
    for (const headers of [{ 'sec-fetch-site': 'same-origin' }, { 'sec-fetch-site': 'none' }, {}]) {
      const r = await rawLogin(backend, ADMIN.user, ADMIN.password, headers);
      assert.equal(r.status, 302, `${JSON.stringify(headers)}, war HTTP ${r.status}`);
      assert.ok(find(r), JSON.stringify(headers));
    }
  });
});

describe('Cookie', () => {
  it('die Anmeldung setzt boss925_dashboard: 12 Std, HttpOnly, SameSite=Strict - und leitet auf index.php', async () => {
    const r = await rawLogin(backend, ADMIN.user, ADMIN.password);
    assert.equal(r.status, 302);
    assert.equal(new URL(r.headers.get('location'), `${backend.base}/index.php`).pathname, '/index.php');
    const c = find(r);
    assert.ok(c, 'Login setzt das Cookie');
    const left = expiresIn(c);
    assert.ok(left !== null && Math.abs(left - IDLE_MAX) <= 60, `Login-Cookie ~12 h, war ${left}`);
    assert.match(c, /httponly/i);
    assert.match(c, /samesite=strict/i);
  });

  it('jede angemeldete Anfrage erneuert das Cookie: gleiche Eigenschaften, gleicher Pfad', async () => {
    const c1 = find(await rawLogin(backend, ADMIN.user, ADMIN.password));
    const cookie = pairOf(c1);
    for (const r of [await status(cookie), await request(`${backend.base}/index.php`, { headers: { cookie } })]) {
      assert.equal(r.status, 200);
      const c2 = find(r);
      assert.ok(c2, 'Anfrage erneuert das Cookie');
      assert.equal(pairOf(c2), cookie, 'dieselbe Sitzung');
      const left = expiresIn(c2);
      assert.ok(left !== null && Math.abs(left - IDLE_MAX) <= 60, `erneuertes Cookie ~12 h, war ${left}`);
      assert.equal(attr(c2, 'path'), attr(c1, 'path'));
      assert.match(c2, /httponly/i);
      assert.match(c2, /samesite=strict/i);
      assert.equal(isSecure(c2), isSecure(c1), 'Secure gleich');
    }
  });

  it('eine zweite Anmeldung vergibt eine neue Sitzungs-ID; die alte gilt nicht mehr, das CSRF-Token ist neu', async () => {
    const cookie = await loginCookie();
    const csrf = await csrfOf(cookie);
    assert.ok(csrf);

    // Die alte ID wird mitgeschickt - übernommen wird sie nicht.
    const relog = await rawLogin(backend, ADMIN.user, ADMIN.password, { cookie });
    assert.equal(relog.status, 302);
    const cookie2 = pairOf(find(relog));
    assert.notEqual(cookie2, cookie, 'Neuanmeldung vergibt eine neue Sitzungs-ID');
    assert.equal((await status(cookie)).status, 401, 'alte Sitzungs-ID nach Neuanmeldung ungültig');
    const csrf2 = await csrfOf(cookie2);
    assert.ok(csrf2 && csrf2 !== csrf, 'neue Sitzung, neues CSRF-Token');
  });
});

describe('Sitzung an die Zugangsdaten gebunden', () => {
  it('ein geändertes Passwort meldet die Sitzungen DIESES Kontos sofort ab', async () => {
    const admin = await session(backend);
    const reader = await session(backend, READER);
    await backend.configure({ dashboard_password_hash: OTHER_PASSWORD_HASH });
    assert.equal((await admin.get('status')).http, 401);
    assert.equal((await reader.get('status')).http, 200);
    await backend.configure({});
    assert.equal((await admin.get('status')).http, 401, 'verworfen, nicht nur gesperrt');
  });

  it('das Lese-Konto entfernt: seine Sitzung ist abgemeldet, die des Admins bleibt', async () => {
    const admin = await session(backend);
    const reader = await session(backend, READER);
    await backend.configure({ dashboard_readonly_user: '' });
    assert.equal((await reader.get('status')).http, 401);
    assert.equal((await admin.get('status')).http, 200);
  });

  it('länger als 12 Std ohne Anfrage: abgemeldet', async (t) => {
    // (Die Art des Servers steht erst nach dem Start fest - deshalb hier
    // übersprungen und nicht schon beim Anlegen des Tests.)
    if (backend.kind === 'php') {
      t.skip('PHP-Sitzungen liegen außerhalb des Prüfstands');
      return;
    }
    const cookie = await loginCookie();
    assert.equal((await status(cookie)).status, 200);
    // Als wäre die letzte Anfrage 12 Std und eine Minute her.
    let edited = 0;
    for (const name of sessionFiles()) {
      const path = join(backend.sessionsDir, name);
      const data = JSON.parse(readFileSync(path, 'utf8'));
      if (!('last_seen' in data)) continue;
      data.last_seen = now() - IDLE_MAX - 60;
      writeFileSync(path, JSON.stringify(data));
      edited++;
    }
    assert.ok(edited > 0, 'keine Sitzungsdatei mit last_seen gefunden');
    assert.equal((await status(cookie)).status, 401);
  });
});

describe('Abmelden', () => {
  it('mit CSRF-Token: abgelaufenes Cookie, danach 401', async () => {
    const cookie = await loginCookie();
    const out = await logout(cookie, { csrf: await csrfOf(cookie) });
    assert.equal(out.status, 302);
    const c = find(out);
    assert.ok(c, 'Logout sendet Set-Cookie');
    const left = expiresIn(c);
    assert.ok(left !== null && left <= 0, `Logout-Cookie abgelaufen: ${c}`);
    assert.equal((await status(cookie)).status, 401, 'nach Logout 401');
  });

  it('ohne oder mit falschem CSRF-Token: nichts abgemeldet, kein Lösch-Cookie', async () => {
    const cookie = await loginCookie();
    for (const fields of [{}, { csrf: 'falsch' }]) {
      const out = await logout(cookie, fields);
      assert.equal(out.status, 302);
      for (const c of cookies(out).filter((x) => x.startsWith(`${NAME}=`))) {
        const left = expiresIn(c);
        assert.ok(left === null || left > 0, `kein Lösch-Cookie: ${c}`);
      }
      assert.equal((await status(cookie)).status, 200, `Sitzung bleibt (${JSON.stringify(fields)})`);
    }
  });
});

describe('Bremse gegen Durchprobieren', () => {
  it('8 Fehlversuche: "Login fehlgeschlagen."; der 9. bekommt 429 - auch mit richtigem Passwort', async () => {
    for (let i = 0; i < 8; i++) {
      const r = await rawLogin(backend, ADMIN.user, `falsch${i}`);
      assert.equal(r.status, 200, `Fehlversuch ${i + 1}`);
      assert.ok(r.text.includes(FAILED));
    }
    const blocked = await rawLogin(backend, ADMIN.user, 'falsch9');
    assert.equal(blocked.status, 429);
    assert.ok(blocked.text.includes('Zu viele Fehlversuche von dieser Adresse'));
    const right = await rawLogin(backend, ADMIN.user, ADMIN.password);
    assert.equal(right.status, 429);
    assert.deepEqual(cookies(right), []);
  });

  it('eine erfolgreiche Anmeldung setzt den Zähler zurück', async () => {
    await backend.configure({ login_max_failures: 3 });
    for (let i = 0; i < 2; i++) assert.equal((await rawLogin(backend, ADMIN.user, 'falsch')).status, 200);
    assert.equal((await rawLogin(backend, ADMIN.user, ADMIN.password)).status, 302);
    // Wieder drei volle Fehlversuche, erst der vierte ist gesperrt.
    for (let i = 0; i < 3; i++) {
      assert.equal((await rawLogin(backend, ADMIN.user, 'falsch')).status, 200, `Fehlversuch ${i + 1}`);
    }
    assert.equal((await rawLogin(backend, ADMIN.user, 'falsch')).status, 429);
  });

  it('unbekannter Benutzer und falsches Passwort: dieselbe Antwort, beide zählen', async () => {
    await backend.configure({ login_max_failures: 2 });
    const unknown = await rawLogin(backend, 'niemand', ADMIN.password);
    const wrong = await rawLogin(backend, ADMIN.user, 'falsch');
    for (const r of [unknown, wrong]) {
      assert.equal(r.status, 200);
      assert.ok(r.text.includes(FAILED));
      assert.deepEqual(cookies(r), []);
    }
    // Die Seite unterscheidet sich nur im stehen gebliebenen Benutzernamen.
    assert.equal(unknown.text.replace('value="niemand"', 'value="X"'), wrong.text.replace('value="admin"', 'value="X"'));
    assert.equal((await rawLogin(backend, ADMIN.user, ADMIN.password)).status, 429);
  });
});

describe('Hinter einem Proxy', () => {
  const PROXY = { trusted_proxies: ['127.0.0.1', '::1'], proxy_client_ip_header: 'X-Forwarded-For' };

  it('ohne trusted_proxies zählen X-Forwarded-Proto und X-Forwarded-For nicht', async () => {
    const plain = await rawLogin(backend, ADMIN.user, ADMIN.password, { 'x-forwarded-proto': 'https' });
    assert.equal(plain.status, 302);
    assert.equal(isSecure(find(plain)), false, `kein Secure: ${find(plain)}`);
    assert.equal(plain.headers.get('strict-transport-security'), null, 'kein HSTS');
    // Jeder Besucher könnte sich sonst eine Adresse aussuchen: Gezählt wird der Absender selbst.
    await backend.configure({ login_max_failures: 2 });
    await rawLogin(backend, ADMIN.user, 'falsch', { 'x-forwarded-for': '198.51.100.1' });
    await rawLogin(backend, ADMIN.user, 'falsch', { 'x-forwarded-for': '198.51.100.2' });
    const other = await rawLogin(backend, ADMIN.user, ADMIN.password, { 'x-forwarded-for': '198.51.100.3' });
    assert.equal(other.status, 429);
  });

  it('vom vertrauten Proxy mit HTTPS: Cookie mit Secure und HSTS', async () => {
    await backend.configure(PROXY);
    const r = await rawLogin(backend, ADMIN.user, ADMIN.password,
      { 'x-forwarded-proto': 'https', 'x-forwarded-for': '198.51.100.7' });
    assert.equal(r.status, 302);
    assert.equal(isSecure(find(r)), true, `Cookie mit Secure: ${find(r)}`);
    assert.equal(r.headers.get('strict-transport-security'), 'max-age=31536000');
  });

  it('kommt die Anfrage nicht von einem eingetragenen Proxy, zählen die Kopfzeilen nicht', async () => {
    await backend.configure({ ...PROXY, trusted_proxies: ['10.0.0.1'] });
    const r = await rawLogin(backend, ADMIN.user, ADMIN.password, { 'x-forwarded-proto': 'https' });
    assert.equal(r.status, 302);
    assert.equal(isSecure(find(r)), false);
    assert.equal(r.headers.get('strict-transport-security'), null);
  });

  it('die Sperre trifft den LETZTEN Eintrag von X-Forwarded-For; ein vorangestellter ändert nichts', async () => {
    await backend.configure({ ...PROXY, login_max_failures: 2 });
    const attacker = { 'x-forwarded-for': '10.9.9.9, 198.51.100.66' };
    for (let i = 0; i < 2; i++) await rawLogin(backend, ADMIN.user, 'falsch', attacker);
    const blocked = await rawLogin(backend, ADMIN.user, ADMIN.password, attacker);
    assert.equal(blocked.status, 429, 'Angreifer-Adresse gesperrt');
    // Eine andere Adresse hinter demselben Proxy bleibt frei.
    const other = await rawLogin(backend, ADMIN.user, ADMIN.password, { 'x-forwarded-for': '198.51.100.7' });
    assert.equal(other.status, 302);
    assert.ok(find(other));
    // Frei erfundene Einträge davor ändern den gezählten Absender nicht.
    const spoof = await rawLogin(backend, ADMIN.user, ADMIN.password, { 'x-forwarded-for': '1.2.3.4, 198.51.100.66' });
    assert.equal(spoof.status, 429, 'vorangestellter Eintrag umgeht die Sperre nicht');
  });
});
