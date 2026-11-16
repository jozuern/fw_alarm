// Anmeldung: Cookie gilt 12 Std (übersteht das Schließen der iPhone-App),
// wird bei jeder angemeldeten Anfrage erneuert und beim Abmelden gelöscht.
// Dazu die Sicherungen der Sitzung: keine Sitzung für Unbekannte, ein
// geändertes Passwort meldet ab, Verhalten hinter einem Proxy/Tunnel.
import { copyFileSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { pushBox, readData, winPath } from '../sandbox.mjs';

const NAME = 'boss925_dashboard';
const cookies = (res) => res.headers.getSetCookie?.() ?? [];
const find = (res) => cookies(res).find((c) => c.startsWith(NAME + '='));
const attr = (c, n) => c.split(';').map((p) => p.trim()).find((p) => p.toLowerCase().startsWith(n.toLowerCase() + '='))?.split('=')[1];
const expiresIn = (c) => {
  const ma = attr(c, 'Max-Age');
  if (ma !== undefined) return Number(ma);
  const ex = attr(c, 'expires');
  return ex === undefined ? null : (Date.parse(ex) - Date.now()) / 1000;
};

const loginRequest = (base, user, password, headers = {}) => fetch(`${base}/index.php`, {
  method: 'POST',
  redirect: 'manual',
  headers: { 'content-type': 'application/x-www-form-urlencoded', ...headers },
  body: new URLSearchParams({ action: 'login', user, password }),
});

// config.php der Sandbox für die Dauer von fn() um Einträge ergänzen bzw.
// ändern (PHP-Ausdruck, der $c verändert); danach wieder das Original.
async function withConfig(sandbox, php, fn) {
  const site = join(winPath(sandbox), 'site');
  const cfg = join(site, 'config.php');
  const base = join(site, 'config.base.php');
  copyFileSync(cfg, base);
  writeFileSync(cfg, `<?php\n$c = require __DIR__ . '/config.base.php';\n${php}\nreturn $c;\n`);
  try {
    return await fn();
  } finally {
    rmSync(cfg, { force: true });
    renameSync(base, cfg);
  }
}

export default async function ({ base, assert, sandbox }) {
  // ---- Unbekannte bekommen KEINE Sitzung (kein Cookie, keine Sitzungsdatei) ----
  const anonPage = await fetch(`${base}/index.php`);
  assert(anonPage.status === 200 && cookies(anonPage).length === 0, 'Anmeldeseite setzt kein Cookie: ' + cookies(anonPage));
  assert(/no-store/.test(anonPage.headers.get('cache-control') || ''), 'Anmeldeseite: Cache-Control no-store');
  const anon = await fetch(`${base}/dashboard_api.php?action=status`);
  assert(anon.status === 401, 'anonym 401');
  assert(cookies(anon).length === 0, 'anonyme API-Anfrage setzt kein Cookie: ' + cookies(anon));
  assert(/no-store/.test(anon.headers.get('cache-control') || ''), '401: Cache-Control no-store');
  // Erfundene Sitzungs-ID: abgelehnt, und es kommt weder ein neues noch ein Lösch-Cookie.
  const forged = await fetch(`${base}/dashboard_api.php?action=status`, { headers: { cookie: `${NAME}=abcdefabcdefabcdefabcdefabcdef12` } });
  assert(forged.status === 401 && cookies(forged).length === 0, 'erfundene Sitzungs-ID: 401 ohne Cookie: ' + cookies(forged));
  const failed = await loginRequest(base, 'admin', 'falsch');
  assert(cookies(failed).length === 0, 'Fehlversuch setzt kein Cookie');
  const classic = await fetch(`${base}/classic.php`, { redirect: 'manual' });
  assert(classic.status === 302 && cookies(classic).length === 0, 'classic.php ohne Anmeldung: Weiterleitung ohne Cookie');

  // ---- Anmeldung nur vom eigenen Formular: fremde Seiten zählen nicht einmal als Fehlversuch ----
  // (auch nicht eine andere Seite derselben Domain: 'same-site')
  for (const site of ['cross-site', 'same-site']) {
    const foreign = await loginRequest(base, 'admin', 'test', { 'sec-fetch-site': site });
    assert(foreign.status === 403 && cookies(foreign).length === 0, `Login mit Sec-Fetch-Site ${site}: 403 ohne Cookie, war HTTP ${foreign.status}`);
  }
  for (const site of ['same-origin', 'none']) {
    const own = await loginRequest(base, 'admin', 'test', { 'sec-fetch-site': site });
    assert(own.status === 302 && find(own), `Login mit Sec-Fetch-Site ${site} geht, war HTTP ${own.status}`);
  }

  // ---- Anmelden, Cookie, Erneuerung ----
  const login = await loginRequest(base, 'admin', 'test');
  const c1 = find(login);
  assert(c1, 'Login setzt das Cookie');
  let left = expiresIn(c1);
  assert(left !== null && Math.abs(left - 43200) <= 60, 'Login-Cookie ~12 h, war ' + left);
  assert(/httponly/i.test(c1) && /samesite=strict/i.test(c1), 'HttpOnly + SameSite=Strict: ' + c1);
  const cookie = c1.split(';')[0];

  const st = await fetch(`${base}/dashboard_api.php?action=status`, { headers: { cookie } });
  const c2 = find(st);
  assert(c2, 'Status-Anfrage erneuert das Cookie');
  left = expiresIn(c2);
  assert(left !== null && Math.abs(left - 43200) <= 60, 'erneuertes Cookie ~12 h, war ' + left);
  assert(attr(c2, 'path') === attr(c1, 'path'), 'gleicher Pfad: ' + attr(c1, 'path') + ' / ' + attr(c2, 'path'));
  assert(/httponly/i.test(c2) && /samesite=strict/i.test(c2), 'Attribute erneuert: ' + c2);
  assert(/secure/i.test(c1) === /secure/i.test(c2), 'Secure gleich');
  const csrf = (await st.json()).csrf;

  // Eine zweite Anmeldung bekommt eine NEUE Sitzungs-ID (keine Übernahme einer mitgeschickten).
  const relog = await loginRequest(base, 'admin', 'test', { cookie });
  const cRelog = find(relog);
  assert(cRelog && cRelog.split(';')[0] !== cookie, 'Neuanmeldung vergibt eine neue Sitzungs-ID');
  const oldAfterRelog = await fetch(`${base}/dashboard_api.php?action=status`, { headers: { cookie } });
  assert(oldAfterRelog.status === 401, 'alte Sitzungs-ID nach Neuanmeldung ungültig');
  const cookie2 = cRelog.split(';')[0];
  const st2 = await fetch(`${base}/dashboard_api.php?action=status`, { headers: { cookie: cookie2 } });
  const csrf2 = (await st2.json()).csrf;
  assert(csrf2 && csrf2 !== csrf, 'neue Sitzung, neues CSRF-Token');

  // ---- Geändertes Passwort meldet bestehende Sitzungen ab (verlorenes Handy) ----
  const roLogin = await loginRequest(base, 'leser', 'test');
  const roCookie = find(roLogin).split(';')[0];
  await withConfig(sandbox, `$c['dashboard_password_hash'] = password_hash('anderes', PASSWORD_DEFAULT);`, async () => {
    const r = await fetch(`${base}/dashboard_api.php?action=status`, { headers: { cookie: cookie2 } });
    assert(r.status === 401, 'Admin-Sitzung nach Passwortwechsel abgemeldet, war HTTP ' + r.status);
    const ro = await fetch(`${base}/dashboard_api.php?action=status`, { headers: { cookie: roCookie } });
    assert(ro.status === 200, 'Sitzung des Lese-Benutzers bleibt (sein Passwort ist unverändert)');
  });
  // Auch mit dem alten Passwort zurück: Die Sitzung wurde verworfen, nicht nur gesperrt.
  const gone = await fetch(`${base}/dashboard_api.php?action=status`, { headers: { cookie: cookie2 } });
  assert(gone.status === 401, 'verworfene Sitzung bleibt ungültig');
  await withConfig(sandbox, `$c['dashboard_readonly_user'] = '';`, async () => {
    const ro = await fetch(`${base}/dashboard_api.php?action=status`, { headers: { cookie: roCookie } });
    assert(ro.status === 401, 'Lese-Konto entfernt: Sitzung abgemeldet, war HTTP ' + ro.status);
  });

  // ---- Abmelden ----
  const login3 = await loginRequest(base, 'admin', 'test');
  const cookie3 = find(login3).split(';')[0];
  const csrf3 = (await (await fetch(`${base}/dashboard_api.php?action=status`, { headers: { cookie: cookie3 } })).json()).csrf;
  const out = await fetch(`${base}/index.php`, {
    method: 'POST',
    redirect: 'manual',
    headers: { cookie: cookie3, 'content-type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams({ action: 'logout', csrf: csrf3 }),
  });
  const c3 = find(out);
  assert(c3, 'Logout sendet Set-Cookie');
  left = expiresIn(c3);
  assert(left !== null && left <= 0, 'Logout-Cookie abgelaufen: ' + c3);
  const after = await fetch(`${base}/dashboard_api.php?action=status`, { headers: { cookie: cookie3 } });
  assert(after.status === 401, 'nach Logout 401');

  // ---- Selbsttest: von einem Proxy weitergereicht = von außen -> 404 ----
  const direct = await fetch(`${base}/api/health.php`);
  assert(direct.status === 200, 'health.php direkt vom NAS: 200, war ' + direct.status);
  for (const h of [{ 'x-forwarded-for': '203.0.113.9' }, { 'cf-connecting-ip': '203.0.113.9' }, { 'x-real-ip': '203.0.113.9' }]) {
    const proxied = await fetch(`${base}/api/health.php`, { headers: h });
    assert(proxied.status === 404, 'health.php über einen Proxy: 404 (' + Object.keys(h)[0] + '), war ' + proxied.status);
  }

  // ---- Hinter einem Proxy: Absender-Adresse und HTTPS nur vom vertrauten Proxy ----
  // Ohne Einstellung zählen die Kopfzeilen nicht (jeder könnte sie fälschen).
  const plain = await loginRequest(base, 'admin', 'test', { 'x-forwarded-proto': 'https' });
  assert(!/;\s*secure/i.test(find(plain)), 'ohne trusted_proxies kein Secure über X-Forwarded-Proto: ' + find(plain));
  assert(!plain.headers.get('strict-transport-security'), 'ohne trusted_proxies kein HSTS über X-Forwarded-Proto');
  const proxyCfg = `$c['trusted_proxies'] = ['127.0.0.1', '::1']; $c['proxy_client_ip_header'] = 'X-Forwarded-For'; $c['login_max_failures'] = 2;`;
  await withConfig(sandbox, proxyCfg, async () => {
    const viaProxy = await loginRequest(base, 'admin', 'test', { 'x-forwarded-proto': 'https', 'x-forwarded-for': '198.51.100.7' });
    assert(/;\s*secure/i.test(find(viaProxy)), 'hinter dem Proxy mit HTTPS: Cookie mit Secure: ' + find(viaProxy));
    assert(viaProxy.headers.get('strict-transport-security'), 'hinter dem Proxy mit HTTPS: HSTS gesetzt');
    // Die Sperre trifft die Adresse aus der Kopfzeile (letzter Eintrag), nicht den Proxy.
    const attacker = { 'x-forwarded-for': '10.9.9.9, 198.51.100.66' };
    for (let i = 0; i < 2; i++) await loginRequest(base, 'admin', 'falsch', attacker);
    const blocked = await loginRequest(base, 'admin', 'test', attacker);
    assert(blocked.status === 429, 'Angreifer-Adresse gesperrt, war HTTP ' + blocked.status);
    const other = await loginRequest(base, 'admin', 'test', { 'x-forwarded-for': '198.51.100.7' });
    assert(other.status === 302 && find(other), 'andere Adresse hinter demselben Proxy bleibt frei, war HTTP ' + other.status);
    // Vorangestellte (frei erfundene) Einträge ändern den gezählten Absender nicht.
    const spoof = await loginRequest(base, 'admin', 'test', { 'x-forwarded-for': '1.2.3.4, 198.51.100.66' });
    assert(spoof.status === 429, 'vorangestellter Eintrag umgeht die Sperre nicht, war HTTP ' + spoof.status);
  });

  // ---- Status-Push merkt sich, ob er über die NAS-Brücke kam (X-FW-Bridge) ----
  // Der Wächter braucht das für den Text von „Box OFFLINE“ (solange die Box
  // über das NAS meldet, kann auch ein Ausfall dort die Ursache sein).
  await pushBox(base, null, {}, { 'X-FW-Bridge': '1' });
  assert(readData(sandbox, 'latest_status').via_bridge === '1', 'Push über die Brücke: via_bridge=1');
  // Ein Feld im Rumpf kann das nicht vortäuschen - es zählt die Kopfzeile.
  await pushBox(base, null, { via_bridge: '1' });
  assert(readData(sandbox, 'latest_status').via_bridge === '0', 'direkter Push: via_bridge=0, auch mit gleichnamigem Feld');
}
