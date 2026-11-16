// Umstellung auf die neue Oberfläche: index.php zeigt die neue Anmeldeseite
// (Fehlversuch: Text, Benutzername bleibt, Fokus im Passwortfeld) und danach
// die neue App; classic.php ist der vorübergehende Rückfall auf die bisherige
// Oberfläche (ohne Anmeldung -> index.php, REAL ALARM geht dort weiter über
// die Bark-Attrappe); next.php und die alten Dateien gibt es nicht mehr;
// manifest.json + Icons für den Home-Bildschirm; Abmelden in den
// Einstellungen führt zur Anmeldeseite, das alte Sitzungs-Cookie gilt nicht
// mehr. Dazu: Design „Hell“ greift schon auf der Anmeldeseite (ohne main.js),
// die Seite geht ohne JavaScript, und die Bestätigungsseite der
// Kurzbefehl-Links (api/mute.php) nutzt das neue Stylesheet.
// Die Sandbox behält ihre Daten: Der eigene Empfänger („swtch…“) wird am Ende
// wieder gelöscht, der Demo-Modus bleibt aus.
import { apiSession } from '../cdp.mjs';
import { pushBox, barkLog } from '../sandbox.mjs';

const q = (s) => JSON.stringify(s);
const text = (page, sel) => page.eval(`(document.querySelector(${q(sel)})?.textContent || '').replace(/\\s+/g, ' ').trim()`);
const COOKIE = 'boss925_dashboard';

// Breite/Höhe aus dem PNG-Kopf (IHDR).
function pngSize(buf) {
  const b = Buffer.from(buf);
  if (b.length < 24 || b.toString('ascii', 1, 4) !== 'PNG') return null;
  return { w: b.readUInt32BE(16), h: b.readUInt32BE(20) };
}

// Seite neu laden: Ein reiner Wechsel des #-Teils löst kein load-Ereignis aus.
async function open(page, url) {
  await page.goto('about:blank');
  await page.goto(url);
}

// Formular der Anmeldeseite ausfüllen und absenden (wartet auf die neue Seite).
async function submitLogin(page, user, pass) {
  const loaded = page.once('Page.loadEventFired');
  await page.eval(`(() => {
    document.querySelector('#user').value = ${q(user)};
    document.querySelector('#password').value = ${q(pass)};
    document.querySelector('#password').form.requestSubmit();
  })()`);
  await loaded;
}

async function sessionCookie(page, base) {
  const { cookies } = await page.send('Network.getCookies', { urls: [base + '/'] });
  return cookies.find((c) => c.name === COOKIE) || null;
}

export default async function ({ browser, base, sandbox, assert, shot }) {
  const admin = await apiSession(base, 'admin', 'test');
  const u = Date.now().toString(36);
  const KEY = 'swtch' + u;
  let keyId = null;

  // ---- Ohne Browser: weg / umgeleitet / ausgeliefert ------------------------
  const next = await fetch(`${base}/next.php`, { redirect: 'manual' });
  assert(next.status === 404, 'next.php gibt es nicht mehr, war HTTP ' + next.status);
  for (const old of ['assets/app.js', 'assets/style.css']) {
    const r = await fetch(`${base}/${old}`, { redirect: 'manual' });
    assert(r.status === 404, `${old} gibt es nicht mehr, war HTTP ${r.status}`);
  }
  const classicAnon = await fetch(`${base}/classic.php`, { redirect: 'manual' });
  assert(classicAnon.status === 302 && /(^|\/)index\.php$/.test(classicAnon.headers.get('location') || ''),
    'classic.php ohne Anmeldung -> index.php, war ' + classicAnon.status + ' ' + classicAnon.headers.get('location'));

  const mf = await fetch(`${base}/manifest.json`);
  assert(mf.ok, 'manifest.json ausgeliefert, war HTTP ' + mf.status);
  const manifest = JSON.parse(await mf.text());
  assert(manifest.display === 'standalone', 'display standalone: ' + manifest.display);
  assert(manifest.name === 'Alarm-Wächter' && manifest.short_name === 'Alarm-Wächter', 'Name: ' + manifest.name + ' / ' + manifest.short_name);
  assert(manifest.start_url === './index.php' && manifest.scope === './', 'start_url/scope: ' + manifest.start_url + ' ' + manifest.scope);
  assert(manifest.background_color === '#000000' && manifest.theme_color === '#000000', 'Farben schwarz');
  for (const size of [180, 192, 512]) {
    const icon = (manifest.icons || []).find((i) => i.sizes === `${size}x${size}`);
    assert(icon && icon.type === 'image/png', `Icon ${size} im Manifest`);
    const r = await fetch(new URL(icon.src, `${base}/manifest.json`));
    assert(r.ok && (r.headers.get('content-type') || '').startsWith('image/png'), `Icon ${size} ausgeliefert (${icon.src}): HTTP ${r.status}`);
    const dim = pngSize(await r.arrayBuffer());
    assert(dim && dim.w === size && dim.h === size, `Icon ${size}: echte Größe ${JSON.stringify(dim)}`);
  }

  // Bestätigungsseite der Kurzbefehl-Links: lesbar mit dem neuen Stylesheet.
  const mute = await fetch(`${base}/api/mute.php?done=off`, { headers: { accept: 'text/html' } });
  const muteHtml = await mute.text();
  assert(mute.status === 200 && muteHtml.includes('../assets/app.css?v=') && muteHtml.includes('Wieder laut'),
    'Kurzbefehl-Seite mit app.css: ' + mute.status + ' ' + muteHtml.slice(0, 300));
  assert(!muteHtml.includes('style.css'), 'Kurzbefehl-Seite ohne altes Stylesheet');

  const page = await browser.newPage();
  await page.send('Log.enable');
  await page.send('Network.enable');
  const problems = [];
  page.on('Runtime.consoleAPICalled', (p) => {
    if (p.type === 'error' || p.type === 'assert') problems.push('console: ' + p.args.map((a) => a.value ?? a.description).join(' '));
  });
  page.on('Runtime.exceptionThrown', (p) => problems.push('exception: ' + (p.exceptionDetails.exception?.description || p.exceptionDetails.text)));
  page.on('Log.entryAdded', (p) => {
    // Der Fehlversuch (HTTP 401/403/429 gibt es hier nicht, Login antwortet 200)
    // und Netz-Meldungen zählen nicht; alles andere (z.B. CSP) schon.
    if (p.entry.level === 'error' && p.entry.source !== 'network') problems.push('log: ' + p.entry.text + ' ' + (p.entry.url || ''));
  });

  try {
    // ---- Anmeldeseite (iPhone) ----------------------------------------------
    await page.viewport({ width: 390, height: 844, mobile: true, scale: 2 });
    await open(page, `${base}/index.php`);
    await page.waitFor(`!!document.querySelector('.login-card')`);
    assert(!(await page.eval(`!!document.getElementById('app')`)), 'Anmeldeseite: keine App-Hülle');
    assert((await page.eval('document.title')).includes('Alarm-Wächter'), 'Titel nennt Alarm-Wächter');
    assert(await page.eval(`document.querySelector('link[rel="manifest"]')?.getAttribute('href') === 'manifest.json'`), 'Anmeldeseite: Manifest verlinkt');
    const metaOf = (name) => page.eval(`document.querySelector('meta[name=${q(name)}]')?.getAttribute('content') ?? null`);
    assert(await metaOf('apple-mobile-web-app-capable') === 'yes', 'apple-mobile-web-app-capable');
    assert(await metaOf('apple-mobile-web-app-status-bar-style') === 'black-translucent', 'Statusleiste black-translucent');
    assert(await metaOf('apple-mobile-web-app-title') === 'Alarm-Wächter', 'Home-Bildschirm-Titel');
    assert(await metaOf('theme-color') === '#000000', 'theme-color schwarz');
    const fields = JSON.parse(await page.eval(`JSON.stringify({
      ua: document.querySelector('#user').getAttribute('autocomplete'),
      uc: document.querySelector('#user').getAttribute('autocapitalize'),
      pa: document.querySelector('#password').getAttribute('autocomplete'),
      pt: document.querySelector('#password').type,
      label: !!document.querySelector('label[for="user"]') && !!document.querySelector('label[for="password"]'),
      btn: (document.querySelector('.login-card ~ button[type="submit"], form button[type="submit"]')?.textContent || '').trim(),
      h1: (document.querySelector('h1')?.textContent || '').trim(),
      lamp: !!document.querySelector('.login-brand .lamp')
    })`));
    assert(fields.ua === 'username' && fields.uc === 'none' && fields.pa === 'current-password' && fields.pt === 'password',
      'Felder mit autocomplete/autocapitalize: ' + JSON.stringify(fields));
    assert(fields.label && fields.btn === 'Anmelden' && fields.h1 === 'Alarm-Wächter' && fields.lamp, 'Aufbau: ' + JSON.stringify(fields));
    assert(await page.eval(`(() => { const b = document.querySelector('form button[type="submit"]');
      return getComputedStyle(b).backgroundColor === getComputedStyle(document.documentElement).getPropertyValue('--alarm').trim()
        || getComputedStyle(b).backgroundColor === 'rgb(255, 59, 48)'; })()`), 'Anmelden ist rot');
    assert(await page.eval(`document.documentElement.scrollWidth <= innerWidth`), 'Anmeldeseite: kein waagrechtes Scrollen');
    assert(await page.eval(`!document.querySelector('.login-error')`), 'ohne Fehlversuch keine Fehlermeldung');
    await shot(page, 'login-mobile');

    // Falsches Passwort: Meldung, Benutzer bleibt, Fokus im Passwortfeld.
    await submitLogin(page, 'admin', 'falsch');
    await page.waitFor(`!!document.querySelector('.login-error')`);
    assert((await text(page, '.login-error')).includes('Login fehlgeschlagen'), 'Fehlertext: ' + await text(page, '.login-error'));
    assert(await page.eval(`document.querySelector('#user').value`) === 'admin', 'Benutzername bleibt stehen');
    assert(await page.eval(`document.querySelector('#password').value`) === '', 'Passwortfeld leer');
    assert(await page.eval(`document.activeElement?.id`) === 'password', 'Fokus im Passwortfeld, war ' + await page.eval(`document.activeElement?.id || document.activeElement?.tagName`));
    assert(await page.eval(`document.querySelector('#password').getAttribute('aria-invalid') === 'true'
      && (document.querySelector('#password').getAttribute('aria-describedby') || '').split(' ').includes(document.querySelector('.login-error').id)`),
      'Passwortfeld mit Fehler verknüpft');
    await shot(page, 'login-error');

    // Richtig: neue App mit Tab-Leiste.
    await submitLogin(page, 'admin', 'test');
    await page.waitFor(`!!document.querySelector('#app .tabbar')`, 10000);
    assert(await page.eval(`!document.querySelector('.login-card')`), 'nach der Anmeldung keine Anmeldeseite');
    assert(await page.eval(`document.querySelector('link[rel="manifest"]')?.getAttribute('href') === 'manifest.json'`), 'App: Manifest verlinkt');
    assert(await metaOf('apple-mobile-web-app-capable') === 'yes', 'App: apple-mobile-web-app-capable');
    assert(await metaOf('apple-mobile-web-app-status-bar-style') === 'black-translucent', 'App: Statusleiste black-translucent');
    assert(await metaOf('apple-mobile-web-app-title') === 'Alarm-Wächter', 'App: Home-Bildschirm-Titel');

    // ---- classic.php: bisherige Oberfläche, REAL ALARM über die Attrappe ------
    assert((await admin.post({ action: 'demo_set', enabled: '0' })).ok, 'Demo aus');
    const known = new Set((await admin.get('keys_list')).keys.map((k) => k.id));
    const added = await admin.post({ action: 'keys_add', label: 'Umstellung', key: KEY });
    assert(added.ok === true, 'keys_add: ' + JSON.stringify(added));
    keyId = (await admin.get('keys_list')).keys.find((k) => !known.has(k.id)).id;
    await pushBox(base, admin);

    await page.viewport({ width: 1280, height: 900, mobile: false, scale: 1 });
    await open(page, `${base}/classic.php`);
    await page.waitFor(`!!document.querySelector('[data-state]')`);
    const assets = JSON.parse(await page.eval(`JSON.stringify({
      css: Array.from(document.querySelectorAll('link[rel="stylesheet"]')).map((l) => l.getAttribute('href')),
      js: Array.from(document.scripts).map((s) => s.getAttribute('src')),
      logout: document.querySelector('form.logout-form')?.getAttribute('action')
    })`));
    assert(assets.css.length === 1 && /^assets\/classic\/style\.css\?v=\d{6,}$/.test(assets.css[0]), 'classic: Stylesheet ' + assets.css);
    assert(assets.js.length === 1 && /^assets\/classic\/app\.js\?v=\d{6,}$/.test(assets.js[0]), 'classic: Skript ' + assets.js);
    assert(assets.logout === 'index.php', 'classic: Abmelden postet an index.php, war ' + assets.logout);
    // Zurück zur neuen Oberfläche: Knopf „Neue Ansicht“ in der Kopfzeile.
    assert(await page.eval(`(() => { const b = Array.from(document.querySelectorAll('.topbar form[method="get"] button.ghost-button'))
      .find((x) => x.textContent.trim() === 'Neue Ansicht');
      return !!b && b.form.getAttribute('action') === 'index.php'; })()`), 'classic: Knopf „Neue Ansicht“ -> index.php');
    // Erst wenn die Liste (samt Demo-Stand) da ist, kennt die Frage den Modus.
    await page.waitFor(`(document.querySelector('[data-keys-list]')?.textContent || '').includes('Umstellung')`, 10000);
    const barkBefore = barkLog(sandbox).length;
    await page.click('[data-command-alarm]');
    await page.waitFor(`!!document.querySelector('.arm-confirm button.danger')`);
    await page.click('.arm-confirm button.danger');
    await page.waitFor(`(() => { const r = document.querySelector('[data-alarm-result]');
      return !!r && !r.hidden && r.textContent.includes('REAL ALARM') && !r.textContent.includes('wird gesendet'); })()`, 60000);
    const sent = barkLog(sandbox).slice(barkBefore).filter((e) => e.key === KEY);
    assert(sent.length >= 1 && sent[0].fields.level === 'critical',
      'classic: REAL ALARM erreicht die Attrappe: ' + JSON.stringify(sent) + ' – Ergebnis: ' + await text(page, '[data-alarm-result]'));
    await shot(page, 'classic');

    // ---- Abmelden in den Einstellungen -> Anmeldeseite, Cookie ungültig -------
    await page.viewport({ width: 390, height: 844, mobile: true, scale: 2 });
    await open(page, `${base}/index.php#/einstellungen`);
    await page.waitFor(`!!document.querySelector('#set-logout button')`, 10000);
    const before = await sessionCookie(page, base);
    assert(before, 'angemeldet: Sitzungs-Cookie vorhanden');
    const loaded = page.once('Page.loadEventFired');
    await page.click('#set-logout button');
    await loaded;
    await page.waitFor(`!!document.querySelector('.login-card')`, 10000);
    assert(await page.eval(`!document.getElementById('app')`), 'nach dem Abmelden: Anmeldeseite');
    const after = await sessionCookie(page, base);
    assert(!after || after.value !== before.value, 'altes Sitzungs-Cookie gelöscht');
    const oldStatus = await fetch(`${base}/dashboard_api.php?action=status`, { headers: { cookie: `${COOKIE}=${before.value}` } });
    assert(oldStatus.status === 401, 'altes Cookie gilt nicht mehr, war HTTP ' + oldStatus.status);
    const classicAfter = await fetch(`${base}/classic.php`, { redirect: 'manual', headers: { cookie: `${COOKIE}=${before.value}` } });
    assert(classicAfter.status === 302, 'classic.php nach dem Abmelden -> Anmeldung, war ' + classicAfter.status);

    // ---- Design „Hell“ schon auf der Anmeldeseite (kein main.js dort) ---------
    await page.eval(`localStorage.setItem('fwAlarm.theme', 'light')`);
    await open(page, `${base}/index.php`);
    await page.waitFor(`!!document.querySelector('.login-card')`);
    assert(await page.eval(`document.documentElement.dataset.theme`) === 'light', 'Anmeldeseite hell');
    assert(await page.eval(`getComputedStyle(document.body).backgroundColor`) === 'rgb(242, 242, 247)', 'heller Hintergrund');
    assert(await metaOf('theme-color') === '#f2f2f7', 'Statusleiste hell');
    await shot(page, 'login-mobile-light');
    await page.viewport({ width: 1280, height: 800, mobile: false, scale: 1 });
    await shot(page, 'login-wide-light');
    await page.eval(`localStorage.removeItem('fwAlarm.theme')`);
    await open(page, `${base}/index.php`);
    await page.waitFor(`!!document.querySelector('.login-card')`);
    assert(await page.eval(`document.documentElement.dataset.theme`) === 'dark', 'Standard Dunkel');
    assert(await page.eval(`getComputedStyle(document.body).backgroundColor`) === 'rgb(0, 0, 0)', 'dunkler Hintergrund');
    await shot(page, 'login-wide');

    // ---- Ohne JavaScript: Anmeldeseite vollständig, dunkel ---------------------
    const bare = await browser.newPage();
    await bare.send('Emulation.setScriptExecutionDisabled', { value: true });
    await bare.viewport({ width: 320, height: 568, mobile: true, scale: 2 });
    await bare.goto(`${base}/index.php`);
    await bare.waitFor(`!!document.querySelector('.login-card') && !!document.querySelector('#password')`);
    assert(await bare.eval(`getComputedStyle(document.body).backgroundColor`) === 'rgb(0, 0, 0)', 'ohne JS: dunkel');
    assert(await bare.eval(`(() => { const b = document.querySelector('form button[type="submit"]').getBoundingClientRect();
      return b.bottom <= innerHeight && b.height >= 44; })()`), 'ohne JS, 320 px: „Anmelden“ ohne Scrollen sichtbar');
    assert(await bare.eval(`document.documentElement.scrollWidth <= innerWidth`), '320 px: kein waagrechtes Scrollen');
    await shot(bare, 'login-small-nojs');

    // Kurzbefehl-Seite im Browser: keine Fehler (CSP), gleiche Optik.
    await open(page, `${base}/api/mute.php?done=on&vol=3&until=${Math.floor(Date.now() / 1000) + 3600}`);
    await page.waitFor(`!!document.querySelector('.login-card')`);
    assert((await text(page, '.login-card')).includes('Stumm geschaltet'), 'Kurzbefehl-Seite: Zustand');
    await shot(page, 'mute-page');

    assert(problems.length === 0, 'keine Konsolenfehler:\n' + problems.join('\n'));
  } finally {
    try { await page.eval(`localStorage.removeItem('fwAlarm.theme')`); } catch { /* Seite evtl. weg */ }
    if (keyId !== null) await admin.post({ action: 'keys_delete', id: String(keyId) });
  }
}
