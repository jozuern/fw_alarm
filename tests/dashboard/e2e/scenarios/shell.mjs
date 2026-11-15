// App-Gerüst der neuen Oberfläche (index.php): Tab-Leiste am iPhone,
// Seitenleiste am PC, Hash-Router, Tab-Titel und Badge nach dem Urteil,
// Design-Umschaltung, Bestätigungs-Sheets mit Halten-zum-Senden - und das
// alles ohne Konsolenfehler (CSP-Verstöße landen dort ebenfalls).
import { apiSession } from '../cdp.mjs';
import { pushBox, runWatchdog } from '../sandbox.mjs';

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const q = (sel) => JSON.stringify(sel);

// Mittelpunkt eines Elements (Viewport-Koordinaten) für echte Eingaben.
async function center(page, sel) {
  return page.eval(`(() => {
    const r = document.querySelector(${q(sel)}).getBoundingClientRect();
    return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
  })()`);
}

// Echte Maus: drücken, halten, loslassen (erzeugt Pointer-Ereignisse wie ein Mensch).
async function mouseHold(page, sel, ms) {
  const { x, y } = await center(page, sel);
  await page.send('Input.dispatchMouseEvent', { type: 'mouseMoved', x, y });
  await page.send('Input.dispatchMouseEvent', { type: 'mousePressed', x, y, button: 'left', buttons: 1, clickCount: 1 });
  await sleep(ms);
  await page.send('Input.dispatchMouseEvent', { type: 'mouseReleased', x, y, button: 'left', buttons: 0, clickCount: 1 });
}

// Echter Finger (iPhone): Berührung halten und loslassen.
async function touchHold(page, sel, ms) {
  const { x, y } = await center(page, sel);
  await page.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y, id: 1 }] });
  await sleep(ms);
  await page.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
}

const KEYS = {
  ' ': { key: ' ', code: 'Space', windowsVirtualKeyCode: 32, text: ' ' },
  Enter: { key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, text: '\r' },
  Escape: { key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 },
  Tab: { key: 'Tab', code: 'Tab', windowsVirtualKeyCode: 9 },
  ShiftTab: { key: 'Tab', code: 'Tab', windowsVirtualKeyCode: 9, modifiers: 8 },
};
async function keyHold(page, key, ms) {
  const k = KEYS[key];
  await page.send('Input.dispatchKeyEvent', { type: 'keyDown', ...k });
  await sleep(ms);
  const { text, ...up } = k;
  await page.send('Input.dispatchKeyEvent', { type: 'keyUp', ...up });
}

// Treffbare Höhe eines Elements in der Mitte: zählt die Pixel-Zeilen, in denen
// elementFromPoint das Element (oder sein Inneres, auch ::after) trifft.
// Vorher ins Bild holen: elementFromPoint sieht nur den sichtbaren Bereich
// (die Proben stehen am Ende einer inzwischen gefüllten Übersicht).
async function hitHeight(page, sel) {
  return page.eval(`(() => {
    const el = document.querySelector(${q(sel)});
    el.scrollIntoView({ block: 'center' });
    const r = el.getBoundingClientRect();
    const x = r.left + r.width / 2;
    let n = 0;
    for (let y = Math.floor(r.top) - 30; y <= Math.ceil(r.bottom) + 30; y++) {
      const hit = document.elementFromPoint(x, y);
      if (hit && (hit === el || el.contains(hit))) n++;
    }
    return n;
  })()`);
}

// Bestätigung wie eine Ansicht öffnen; ausgelöst wird nur ein Zähler.
async function openTestConfirm(page, hold) {
  await page.eval(`(() => {
    window.__fired = window.__fired || 0;
    FW.store.openConfirm({
      title: 'Probe-Bestätigung', text: 'Nur ein Test im Gerüst – es wird nichts gesendet.',
      confirmLabel: 'Ja, ausführen', hold: ${hold ? 'true' : 'false'},
      onConfirm: () => { window.__fired += 1; }
    });
  })()`);
  // Erst nach dem Hereingleiten ist die Position des Knopfs endgültig.
  await page.waitFor(`(() => { const s = document.querySelector('.sheet-layer .sheet');
    return !!s && !!document.querySelector('.confirm') && s.getAnimations().every((a) => a.playState !== 'running'); })()`);
}
const fired = (page) => page.eval('window.__fired || 0');
const confirmOpen = (page) => page.eval(`!!document.querySelector('.confirm')`);

export default async function ({ browser, base, sandbox, assert, shot }) {
  // Sandbox auf „Alles bereit“: LIVE-Modus, frischer Status-Push mit passender
  // Listen-Version, der Cron-Wächter ist einmal gelaufen.
  const admin = await apiSession(base, 'admin', 'test');
  await admin.post({ action: 'demo_set', enabled: '0' });
  await pushBox(base, admin);
  runWatchdog(sandbox);

  // Ohne Anmeldung zeigt index.php die Anmeldeseite statt der App-Hülle.
  const anon = await fetch(`${base}/index.php`, { redirect: 'manual' });
  const anonHtml = await anon.text();
  assert(anon.status === 200 && anonHtml.includes('login-card') && !anonHtml.includes('<div id="app">'),
    'anonym: Anmeldeseite, war ' + anon.status);

  const page = await browser.newPage();
  await page.send('Log.enable');
  const problems = [];
  page.on('Runtime.consoleAPICalled', (p) => {
    if (p.type === 'error' || p.type === 'assert') problems.push('console: ' + p.args.map((a) => a.value ?? a.description).join(' '));
  });
  page.on('Runtime.exceptionThrown', (p) => problems.push('exception: ' + (p.exceptionDetails.exception?.description || p.exceptionDetails.text)));
  page.on('Log.entryAdded', (p) => { if (p.entry.level === 'error') problems.push('log: ' + p.entry.text + ' ' + (p.entry.url || '')); });

  // ---- iPhone-Breite --------------------------------------------------------
  await page.viewport({ width: 390, height: 844, mobile: true, scale: 2 });
  await page.login(base, 'admin', 'test');
  problems.length = 0;   // nur die neue Oberfläche zählt
  await page.goto('about:blank');
  await page.goto(`${base}/index.php`);
  await page.waitFor(`!!document.querySelector('.tabbar')`);
  // Skripte: zuerst theme-boot.js synchron im <head> (Design vor dem ersten
  // Zeichnen), dann die App in fester Reihenfolge, alle defer; alle mit
  // ?v=<Dateizeit>.
  const scripts = await page.eval(`JSON.stringify(Array.from(document.scripts).map((s) => [s.getAttribute('src'), s.defer, s.parentNode === document.head]))`);
  const order = ['vendor/htm-preact-3.1.1.umd.js', 'app/logic.js', 'app/api.js', 'app/store.js', 'app/ui.js',
    'app/view-overview.js', 'app/view-recipients.js', 'app/view-log.js', 'app/view-settings.js', 'app/main.js'];
  const got = JSON.parse(scripts);
  const [boot, ...app] = got;
  assert(boot && boot[0].startsWith('assets/app/theme-boot.js?v=') && !boot[1] && boot[2] && /\?v=\d{6,}$/.test(boot[0]),
    'theme-boot.js synchron im Kopf: ' + scripts);
  assert(app.length === order.length && app.every(([src, d], i) => d && src.startsWith('assets/' + order[i] + '?v=') && /\?v=\d{6,}$/.test(src)),
    'Skript-Tags: ' + scripts);
  assert(await page.eval(`(() => { const css = document.querySelector('link[rel="stylesheet"]');
    const boot = document.scripts[0];
    return !!css && !!(boot.compareDocumentPosition(css) & Node.DOCUMENT_POSITION_FOLLOWING); })()`), 'theme-boot.js steht vor dem Stylesheet');
  assert(await page.eval(`document.querySelectorAll('.tabbar a').length`) === 4, 'Tab-Leiste hat 4 Einträge');
  assert(await page.eval(`!document.querySelector('.sidebar')`), 'schmal: keine Seitenleiste');
  assert(await page.eval(`(() => { const r = document.querySelector('.tabbar').getBoundingClientRect();
    return r.height >= 44 && Math.abs(r.bottom - innerHeight) < 1; })()`), 'Tab-Leiste sichtbar am unteren Rand');
  assert(await page.eval(`document.querySelector('main h1').textContent.trim()`) === 'Übersicht', 'Start = Übersicht');
  assert(await page.eval(`document.querySelector('.tabbar a[aria-current="page"]').textContent.includes('Übersicht')`), 'Übersicht-Tab aktiv');

  // Tab-Titel nach dem ersten Status-Abruf: alles bereit -> grün, kein Badge.
  await page.waitFor(`document.title.startsWith('🟢')`, 10000);
  assert((await page.eval('document.title')).includes('Alarm-Wächter'), 'Titel nennt Alarm-Wächter');
  assert(await page.eval(`!document.querySelector('.tabbar .badge')`), 'kein Badge, solange alles bereit ist');
  assert(await page.eval(`document.documentElement.scrollWidth <= innerWidth`), 'schmal: kein waagrechtes Scrollen');
  await shot(page, 'mobile');

  // Router: zweiter Tab, Unterseiten, unbekannte Adresse.
  await page.click('.tabbar a:nth-child(2)');
  await page.waitFor(`location.hash === '#/empfaenger' && document.querySelector('main h1')?.textContent.trim() === 'Empfänger'`);
  assert(await page.eval(`document.querySelector('.tabbar a[aria-current="page"]').textContent.includes('Empfänger')`), 'Empfänger-Tab aktiv');
  // Verschachtelte Adresse: Bereich Einstellungen, Unterseite „Meldungen an mich“.
  await page.eval(`location.hash = '#/einstellungen/meldungen'`);
  await page.waitFor(`document.querySelector('main h1')?.textContent.trim() === 'Meldungen an mich'
    && (document.querySelector('.tabbar a[aria-current="page"]')?.textContent || '').includes('Einstellungen')`);
  await page.eval(`location.hash = '#/gibt-es-nicht'`);
  await page.waitFor(`document.querySelector('main h1')?.textContent.trim() === 'Übersicht'`);
  assert(await page.eval(`location.hash`) === '#/uebersicht', 'unbekannte Adresse -> #/uebersicht');
  const parsed = await page.eval(`JSON.stringify(FW.main.parseRoute('#/verlauf?filter=alarm&x=%C3%BC'))`);
  assert(parsed === JSON.stringify({ path: ['verlauf'], query: { filter: 'alarm', x: 'ü' } }), 'parseRoute: ' + parsed);

  // Badge + Titel bei einem Hinweis (schwaches WLAN), danach wieder grün.
  await pushBox(base, admin, { rssi: '-85' });
  await page.waitFor(`document.title.startsWith('🟡') && document.querySelector('.tabbar .badge')?.textContent.trim() === '1'`, 10000);
  await pushBox(base, admin);
  await page.waitFor(`document.title.startsWith('🟢') && !document.querySelector('.tabbar .badge')`, 10000);

  // ---- Halten zum Senden (über eine Bestätigung wie aus einer Ansicht) ------
  await openTestConfirm(page, true);
  assert(await page.eval(`document.activeElement?.textContent.trim() === 'Abbrechen'`), 'Fokus startet auf „Abbrechen“');
  assert(await page.eval(`(() => { const b = document.querySelector('.confirm .hold');
    const d = document.getElementById(b.getAttribute('aria-describedby') || '');
    return !!d && d.textContent.trim() === 'Zum Senden gedrückt halten'; })()`), 'Halten-Knopf beschrieben');
  assert(/\d+\s*s/.test(await page.eval(`document.querySelector('.confirm-count')?.textContent || ''`)), 'Restzeit sichtbar');
  await shot(page, 'mobile-confirm');
  await mouseHold(page, '.confirm .hold', 300);
  await sleep(200);
  assert(await fired(page) === 0, 'kurz gedrückt (Maus): nichts ausgelöst');
  assert(await confirmOpen(page), 'kurz gedrückt: Bestätigung bleibt offen');
  await touchHold(page, '.confirm .hold', 400);
  await sleep(200);
  assert(await fired(page) === 0, 'kurz gedrückt (Finger): nichts ausgelöst');
  await touchHold(page, '.confirm .hold', 1700);
  await page.waitFor(`!document.querySelector('.confirm')`, 3000);
  assert(await fired(page) === 1, 'Finger 1,7 s gehalten: genau einmal ausgelöst');

  await openTestConfirm(page, true);
  await mouseHold(page, '.confirm .hold', 1700);
  await page.waitFor(`!document.querySelector('.confirm')`, 3000);
  assert(await fired(page) === 2, 'Maus 1,7 s gehalten: ausgelöst');

  await openTestConfirm(page, true);
  await page.eval(`document.querySelector('.confirm .hold').focus()`);
  await keyHold(page, ' ', 300);
  await keyHold(page, 'Enter', 50);
  await sleep(200);
  assert(await fired(page) === 2, 'Leertaste/Enter kurz: nichts ausgelöst');
  await keyHold(page, ' ', 1700);
  await page.waitFor(`!document.querySelector('.confirm')`, 3000);
  assert(await fired(page) === 3, 'Leertaste 1,7 s gehalten: ausgelöst');

  // Escape schließt, ohne auszulösen.
  await openTestConfirm(page, true);
  await keyHold(page, 'Escape', 20);
  await page.waitFor(`!document.querySelector('.confirm')`, 3000);
  assert(await fired(page) === 3, 'Escape löst nichts aus');

  // Fokus-Falle: Tab/Umschalt+Tab bleiben in der Bestätigung; Escape gibt
  // den Fokus an das auslösende Element zurück.
  await page.eval(`document.querySelector('.tabbar a:nth-child(3)').focus()`);
  await openTestConfirm(page, true);
  for (let i = 0; i < 5; i++) await keyHold(page, 'Tab', 10);
  assert(await page.eval(`document.querySelector('.sheet').contains(document.activeElement)`), 'Tab bleibt in der Bestätigung');
  for (let i = 0; i < 3; i++) await keyHold(page, 'ShiftTab', 10);
  assert(await page.eval(`document.querySelector('.sheet').contains(document.activeElement)`), 'Umschalt+Tab bleibt in der Bestätigung');
  await keyHold(page, 'Escape', 20);
  await page.waitFor(`!document.querySelector('.confirm')`, 3000);
  assert(await page.eval(`document.activeElement === document.querySelector('.tabbar a:nth-child(3)')`), 'Fokus zurück beim Auslöser');
  assert(await fired(page) === 3, 'Tab/Escape lösen nichts aus');

  // Zwei-Knopf-Variante: Fokus auf „Abbrechen“, roter Bestätigen-Knopf.
  await openTestConfirm(page, false);
  assert(await page.eval(`document.activeElement?.textContent.trim() === 'Abbrechen'`), 'Zwei Knöpfe: Fokus auf „Abbrechen“');
  assert(await page.eval(`!document.querySelector('.confirm .hold')`), 'Zwei Knöpfe: kein Halten-Knopf');
  await page.click('.confirm .btn-danger');
  await page.waitFor(`!document.querySelector('.confirm')`, 3000);
  assert(await fired(page) === 4, 'Bestätigen-Knopf löst aus');

  // ---- PC-Breite -------------------------------------------------------------
  await pushBox(base, admin);
  await page.viewport({ width: 1280, height: 800, mobile: false, scale: 1 });
  await page.waitFor(`!!document.querySelector('.sidebar')`);
  assert(await page.eval(`!document.querySelector('.tabbar')`), 'breit: keine Tab-Leiste');
  assert(await page.eval(`document.querySelectorAll('.sidebar nav a').length`) === 4, 'Seitenleiste hat 4 Bereiche');
  await page.waitFor(`/Status vor \\d+/.test(document.querySelector('.sidebar').textContent)`, 8000);
  assert(await page.eval(`document.documentElement.scrollWidth <= innerWidth`), 'breit: kein waagrechtes Scrollen');
  await page.waitFor(`document.title.startsWith('🟢')`, 10000);

  // Tippflächen mind. 44 px - auch wo das Sichtbare kleiner ist („Abmelden“,
  // Segmente, kleiner Knopf). „Abmelden“ darf nicht in die Statuszeile reichen.
  for (const sel of ['.sidebar nav a', '.side-foot .link-btn']) {
    const hh = await hitHeight(page, sel);
    assert(hh >= 44, `Tippfläche ${sel}: ${hh} px`);
  }
  assert(await page.eval(`(() => { const r = document.querySelector('.side-age').getBoundingClientRect();
    const hit = document.elementFromPoint(r.left + 10, r.top + r.height / 2);
    return !hit || !hit.closest('.link-btn'); })()`), '„Abmelden“ reicht nicht in die Statuszeile');
  await page.eval(`(() => {
    const { html, render } = htmPreact;
    const ui = FW.ui;
    const box = document.createElement('div');
    box.id = 'tap-probe';
    document.querySelector('main').appendChild(box);
    const cell = { padding: '24px 0' };
    render(html\`<div style=\${cell}><\${ui.Segmented} label="Probe" value="a" onChange=\${() => null}
        options=\${[{ value: 'a', label: 'Laut' }, { value: 'b', label: 'Stumm' }]}/></div>
      <div style=\${cell}><button type="button" class="btn btn-small" id="probe-small">Klein</button></div>
      <div style=\${cell}><\${ui.Switch} id="probe-switch" label="Probe" checked=\${false} onChange=\${() => null}/></div>
      <div style=\${cell}><\${ui.IconButton} icon="plus" label="Probe" onClick=\${() => null}/></div>\`, box);
  })()`);
  for (const sel of ['#tap-probe .seg', '#tap-probe .seg + .seg', '#probe-small', '#probe-switch', '#tap-probe .icon-btn']) {
    const hh = await hitHeight(page, sel);
    assert(hh >= 44, `Tippfläche ${sel}: ${hh} px`);
  }
  await page.eval(`(() => { htmPreact.render(null, document.getElementById('tap-probe')); document.getElementById('tap-probe').remove(); })()`);
  await shot(page, 'wide');
  await openTestConfirm(page, true);
  await shot(page, 'wide-confirm');
  await page.click('.confirm .btn-cancel');
  await page.waitFor(`!document.querySelector('.confirm')`, 3000);

  // ---- Design pro Gerät -----------------------------------------------------
  await page.eval(`localStorage.setItem('fwAlarm.theme', 'light')`);
  await page.goto(`${base}/index.php`);
  await page.waitFor(`!!document.querySelector('.sidebar')`);
  assert(await page.eval(`document.documentElement.dataset.theme`) === 'light', 'Design Hell übersteht das Neuladen');
  await page.viewport({ width: 390, height: 844, mobile: true, scale: 2 });
  await page.waitFor(`!!document.querySelector('.tabbar')`);
  await shot(page, 'mobile-light');
  await page.eval(`localStorage.removeItem('fwAlarm.theme')`);
  await page.goto(`${base}/index.php`);
  await page.waitFor(`!!document.querySelector('.tabbar')`);
  assert(await page.eval(`document.documentElement.dataset.theme`) === 'dark', 'Standard ist Dunkel');

  assert(problems.length === 0, 'keine Konsolenfehler:\n' + problems.join('\n'));
}
