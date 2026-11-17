// Einstellungen der neuen Oberfläche (index.php#/einstellungen): Liste,
// „Meldungen an mich“ (Schalter je Art, wichtige abschalten nur mit Rückfrage,
// Detailseite mit Segment/Auswahl und „Probe senden“), „Ruhezeit &
// Allgemein“, „Automatisch wieder laut nach“, Demo-Modus samt Band auf jeder
// Seite, Box-Details mit aufklappbaren Erklärungen, Design (Hell/Dunkel),
// „Hinweis im Browser bei Störung“ (Notification-Attrappe), Konto, PC-Breite
// und Lese-Rolle.
// Jeder Lauf startet mit leeren Daten: Meldungs- und App-Einstellungen werden
// trotzdem als Datei gesichert und am Ende zurückgeschrieben; die eigenen
// Empfänger (Keys „seta…“/„setb…“) löscht das Szenario wieder, Demo aus.
import { existsSync, readFileSync, writeFileSync, unlinkSync } from 'node:fs';
import { apiSession } from '../cdp.mjs';
import { pushBox, readData, editData, setStatusAge, barkLog } from '../sandbox.mjs';

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const q = (s) => JSON.stringify(s);
const text = (page, sel) => page.eval(`(document.querySelector(${q(sel)})?.textContent || '').replace(/\\s+/g, ' ').trim()`);
const own = (k) => /^set[ab][0-9a-z]{6,}$/.test(k.key);

// Seite neu laden: Ein reiner Wechsel des #-Teils löst kein load-Ereignis aus.
async function open(page, base, hash) {
  await page.goto('about:blank');
  await page.goto(`${base}/index.php${hash}`);
}
// Innerhalb der geladenen Seite wechseln (wie ein Tipp auf einen Link).
const go = (page, hash) => page.eval(`location.hash = ${q(hash)}`);

// Pixelzeilen (an x), in denen elementFromPoint das Element (oder ein
// Kind, auch ::after) trifft - die tatsächliche Tippfläche.
const hitHeight = (page, sel) => page.eval(`(() => {
  const el = document.querySelector(${q(sel)});
  el.scrollIntoView({ block: 'center' });
  const r = el.getBoundingClientRect();
  const x = r.left + r.width / 2;
  let n = 0;
  for (let y = Math.floor(r.top) - 20; y <= Math.ceil(r.bottom) + 20; y++) {
    const hit = document.elementFromPoint(x, y);
    if (hit && el.contains(hit)) n++;
  }
  return n;
})()`);

async function settle(page) {
  await page.waitFor(`Array.from(document.querySelectorAll('.sheet')).every((s) => s.getAnimations().every((a) => a.playState !== 'running'))`);
}
async function waitConfirm(page) {
  await page.waitFor(`!!document.querySelector('.confirm')`, 5000);
  await settle(page);
}
async function until(fn, ms, what) {
  const end = Date.now() + ms;
  let last;
  while (Date.now() < end) {
    last = await fn();
    if (last) return last;
    await sleep(200);
  }
  throw new Error(`Zeitüberschreitung (${ms} ms): ${what}`);
}
// Segment mit dieser Beschriftung innerhalb von sel antippen.
async function clickSeg(page, sel, label) {
  await page.waitFor(`Array.from(document.querySelectorAll(${q(sel + ' .seg')})).some((b) => b.textContent.trim() === ${q(label)})`);
  await page.eval(`Array.from(document.querySelectorAll(${q(sel + ' .seg')})).find((b) => b.textContent.trim() === ${q(label)}).click()`);
}
const pressedSeg = (page, sel) => text(page, `${sel} .seg[aria-pressed="true"]`);
const checked = (page, sel) => page.eval(`document.querySelector(${q(sel + ' .switch')})?.getAttribute('aria-checked')`);

// Antworten einer Aktion in der Seite durch {ok:false} ersetzen (null = aus).
async function failAction(page, action) {
  await page.eval(`(() => {
    if (!window.__realFetch) window.__realFetch = window.fetch;
    if (${action === null ? 'true' : 'false'}) { window.fetch = window.__realFetch; return; }
    window.fetch = async (url, opts) => {
      const body = opts && opts.body instanceof FormData ? opts.body.get('action') : '';
      if (body !== ${q(action)}) return window.__realFetch(url, opts);
      return new Response(JSON.stringify({ ok: false, message: 'Testfehler: nicht gespeichert.' }), { status: 200, headers: { 'Content-Type': 'application/json' } });
    };
  })()`);
}

// Laufzeitdatei im data_dir roh sichern (null = gab es nicht) bzw. zurückschreiben.
function backupFile(backend, name) {
  const p = backend.data.path(name);
  return { p, raw: existsSync(p) ? readFileSync(p, 'utf8') : null };
}
function restoreFile(b) {
  if (b.raw === null) { if (existsSync(b.p)) unlinkSync(b.p); } else writeFileSync(b.p, b.raw);
}

// Notification-Attrappe: zeichnet jede Benachrichtigung auf; die Erlaubnis
// steht erst auf „default“ und wird beim Fragen erteilt. window.focus zählt.
const NOTIFICATION_STUB = `(() => {
  const notes = [];
  window.__notes = notes;
  window.__permAsked = 0;
  window.__focused = 0;
  function FakeNotification(title, opts) {
    const n = { title, body: opts && opts.body, tag: opts && opts.tag, closed: false, onclick: null, close() { this.closed = true; } };
    notes.push(n);
    return n;
  }
  FakeNotification.permission = 'default';
  FakeNotification.requestPermission = () => {
    window.__permAsked++;
    FakeNotification.permission = 'granted';
    return Promise.resolve('granted');
  };
  Object.defineProperty(window, 'Notification', { value: FakeNotification, configurable: true, writable: true });
  window.focus = () => { window.__focused++; };
})()`;
const STANDALONE_STUB = `Object.defineProperty(Navigator.prototype, 'standalone', { get: () => true, configurable: true })`;

export default async function ({ browser, base, backend, assert, shot }) {
  const admin = await apiSession(base, 'admin', 'test');
  const u = Date.now().toString(36);
  const list = () => admin.get('keys_list');
  const notify = () => admin.get('notify_list');
  const typeOf = async (key) => (await notify()).types.find((t) => t.key === key);
  const optOf = async (type, key) => (await typeOf(type)).options.find((o) => o.key === key).value;
  const globalOf = async (key) => (await notify()).global.find((o) => o.key === key).value;
  const backups = [backupFile(backend, 'notify_settings.json'), backupFile(backend, 'app_settings.json')];
  const muteBefore = (await admin.get('settings_list')).settings.mute_max_seconds.value;
  const ids = {};

  const add = async (label, key) => {
    const known = new Set((await list()).keys.map((k) => k.id));
    const r = await admin.post({ action: 'keys_add', label, key });
    assert(r.ok === true, `keys_add ${label}: ` + JSON.stringify(r));
    const added = (await list()).keys.filter((k) => !known.has(k.id));
    assert(added.length === 1, 'neue id für ' + label);
    return added[0].id;
  };
  const removeOwn = async () => {
    for (const k of ((readData(backend, 'alarm_keys.json') || { keys: [] }).keys || []).filter(own)) {
      await admin.post({ action: 'keys_delete', id: String(k.id) });
    }
  };

  const page = await browser.newPage();
  await page.send('Log.enable');
  await page.send('Network.enable');
  const problems = [];
  page.on('Runtime.consoleAPICalled', (p) => {
    if (p.type === 'error' || p.type === 'assert') problems.push('console: ' + p.args.map((a) => a.value ?? a.description).join(' '));
  });
  page.on('Runtime.exceptionThrown', (p) => problems.push('exception: ' + (p.exceptionDetails.exception?.description || p.exceptionDetails.text)));
  page.on('Log.entryAdded', (p) => {
    if (p.entry.level === 'error' && p.entry.source !== 'network') problems.push('log: ' + p.entry.text + ' ' + (p.entry.url || ''));
  });
  const adminReads = [];
  page.on('Network.requestWillBeSent', (p) => {
    if (/action=(notify_list|settings_list)/.test(p.request.url)) adminReads.push(p.request.url);
  });

  try {
    // ---- Ausgangslage: Demo aus, Anna und Ben (stumm), Box meldet sich ----
    assert((await admin.post({ action: 'demo_set', enabled: '0' })).ok, 'Demo aus');
    await removeOwn();
    ids.anna = await add('Anna', 'seta' + u);
    ids.ben = await add('Ben', 'setb' + u);
    assert((await admin.post({ action: 'keys_mute', id: String(ids.ben), muted: '1' })).ok, 'Ben stumm');
    await pushBox(backend, admin);
    const n0 = await notify();
    const on0 = n0.types.filter((t) => t.enabled).length;

    // ---- iPhone: Liste ----------------------------------------------------
    await page.viewport({ width: 390, height: 844, mobile: true, scale: 2 });
    await page.login(base, 'admin', 'test');
    problems.length = 0;
    await open(page, base, '#/einstellungen');
    await page.waitFor(`!!document.querySelector('#set-notify') && (document.querySelector('#set-notify .row-value')?.textContent || '').includes('von')`, 10000);
    assert(await text(page, 'main h1') === 'Einstellungen', 'Titel Einstellungen');
    assert(await text(page, '#set-notify .row-label') === 'Meldungen an mich', 'Zeile Meldungen');
    assert(await text(page, '#set-notify .row-value') === `${on0} von ${n0.types.length} an`, 'Zählung: ' + await text(page, '#set-notify .row-value'));
    assert(await text(page, '#set-quiet .row-value') === 'aus', 'Ruhezeit aus: ' + await text(page, '#set-quiet .row-value'));
    assert(await text(page, '#set-demo .row-value') === 'aus', 'Demo aus');
    await page.waitFor(`(document.querySelector('#set-mute .row-value')?.textContent || '').trim() === '12 Std'`, 5000);
    assert(await page.eval(`!!document.querySelector('#set-box a[href="#/einstellungen/box"]')`), 'Box-Details verlinkt');
    assert(await pressedSeg(page, '#set-theme') === 'Dunkel', 'Design: Dunkel gewählt');
    assert(await page.eval(`!!document.querySelector('#set-alert .switch')`), 'Hinweis im Browser (Chrome kann Notification)');
    assert((await text(page, '#set-account')).includes('admin'), 'Konto: admin');
    assert(await page.eval(`document.querySelector('#set-classic a')?.getAttribute('href') === 'classic.php'`), '„Alte Ansicht öffnen“ → classic.php');
    const heads = await page.eval(`Array.from(document.querySelectorAll('main .group-head')).map((h) => h.textContent.trim())`);
    assert(JSON.stringify(heads) === JSON.stringify(['Empfänger', 'Box', 'Dieses Gerät', 'Konto']), 'Gruppen: ' + JSON.stringify(heads));
    assert(await page.eval(`document.documentElement.scrollWidth <= innerWidth`), 'kein waagrechtes Scrollen');
    for (const sel of ['#set-notify .row-main', '#set-theme .seg', '#set-alert .switch', '#set-logout .row-main', '#set-classic .row-main']) {
      const h = await hitHeight(page, sel);
      assert(h >= 44, `Tippfläche ${sel}: ${h} px`);
    }
    // Abmelden schickt das versteckte Formular ab (hier abgefangen).
    await page.eval(`document.getElementById('logout-form').submit = () => { window.__loggedOut = true; }`);
    await page.click('#set-logout button');
    assert(await page.eval('window.__loggedOut === true'), 'Abmelden schickt #logout-form ab');
    await page.eval('scrollTo(0, 0)');
    await shot(page, 'mobile-main');

    // ---- Meldungen an mich -------------------------------------------------
    await page.click('#set-notify a');
    await page.waitFor(`location.hash === '#/einstellungen/meldungen' && !!document.querySelector('#ntype-box_offline')`, 5000);
    assert(await text(page, 'main h1') === 'Meldungen an mich', 'Titel Meldungen');
    assert(await text(page, '.back-link') === 'Einstellungen', 'Zurück zu Einstellungen');
    const secHead = await text(page, '.set-g-security .group-head');
    const infoHead = await text(page, '.set-g-info .group-head');
    assert(secHead === 'Sicherheit' && infoHead === 'Info', `Gruppen ${secHead} / ${infoHead}`);
    const secKeys = await page.eval(`Array.from(document.querySelectorAll('.set-g-security .set-ntype')).map((r) => r.id.replace('ntype-', ''))`);
    const infoKeys = await page.eval(`Array.from(document.querySelectorAll('.set-g-info .set-ntype')).map((r) => r.id.replace('ntype-', ''))`);
    assert(JSON.stringify(secKeys) === JSON.stringify(n0.types.filter((t) => t.important).map((t) => t.key)), 'Sicherheit = important: ' + JSON.stringify(secKeys));
    assert(JSON.stringify(infoKeys) === JSON.stringify(n0.types.filter((t) => !t.important).map((t) => t.key)), 'Info = Rest: ' + JSON.stringify(infoKeys));
    assert(await page.eval(`document.querySelectorAll('.set-ntype .switch').length`) === n0.types.length, 'ein Schalter je Art');
    assert(await page.eval(`!document.querySelector('.set-unconfigured')`), 'Status-Key gesetzt: kein Hinweis');
    for (const sel of ['#ntype-box_offline .switch', '#ntype-box_offline .set-ntype-link']) {
      const h = await hitHeight(page, sel);
      assert(h >= 44, `Tippfläche ${sel}: ${h} px`);
    }

    // Info-Art: direkt an und aus, Server spiegelt es.
    assert((await typeOf('box_restart')).enabled === false, 'Box neu gestartet: ab Werk aus');
    await page.click('#ntype-box_restart .switch');
    await until(async () => (await typeOf('box_restart')).enabled === true, 6000, 'box_restart an');
    assert(await page.eval(`!document.querySelector('.confirm')`), 'Info-Art: ohne Rückfrage');
    await page.waitFor(`(document.querySelector('#ntype-box_restart .msg')?.textContent || '').includes('Eingeschaltet')`, 5000);
    await page.click('#ntype-box_restart .switch');
    await until(async () => (await typeOf('box_restart')).enabled === false, 6000, 'box_restart aus');
    await page.waitFor(`document.querySelector('#ntype-box_restart .switch').getAttribute('aria-checked') === 'false'`, 5000);

    // Wichtige Art abschalten: erst die Rückfrage, Abbrechen lässt sie an.
    await page.click('#ntype-box_offline .switch');
    await waitConfirm(page);
    assert(await text(page, '.confirm-text') === 'Meldung „Box offline / wieder online“ wirklich abschalten? Das würde dir dann NICHT mehr gemeldet.',
      'Rückfrage: ' + await text(page, '.confirm-text'));
    assert(await checked(page, '#ntype-box_offline') === 'true', 'bis zur Bestätigung bleibt der Schalter an');
    await shot(page, 'mobile-confirm-important');
    await page.click('.confirm .btn-cancel');
    await page.waitFor(`!document.querySelector('.confirm')`, 3000);
    assert(await checked(page, '#ntype-box_offline') === 'true', 'abgebrochen: Schalter an');
    assert((await typeOf('box_offline')).enabled === true, 'abgebrochen: Meldung an');
    // nas_web: bestätigt -> aus; wieder an geht ohne Rückfrage.
    await page.click('#ntype-nas_web .switch');
    await waitConfirm(page);
    await page.click('.confirm .btn-danger');
    await until(async () => (await typeOf('nas_web')).enabled === false, 6000, 'nas_web aus');
    await page.waitFor(`document.querySelector('#ntype-nas_web .switch').getAttribute('aria-checked') === 'false'`, 5000);
    await page.click('#ntype-nas_web .switch');
    await sleep(300);
    assert(await page.eval(`!document.querySelector('.confirm')`), 'Einschalten ohne Rückfrage');
    await until(async () => (await typeOf('nas_web')).enabled === true, 6000, 'nas_web wieder an');

    // ---- Detailseite einer wichtigen Art: Auswahl + Etikett in der Liste -----
    await page.click('#ntype-box_offline .set-ntype-link');
    await page.waitFor(`location.hash === '#/einstellungen/meldungen/box_offline' && !!document.querySelector('#nopt-level')`, 5000);
    assert(await text(page, '.back-link') === 'Meldungen', 'Zurück zu Meldungen');
    await clickSeg(page, '#nopt-level', 'kritisch');
    await until(async () => (await optOf('box_offline', 'level')) === 'critical', 6000, 'Dringlichkeit kritisch');
    assert(await page.eval(`!!document.querySelector('#nopt-offline_after_s select.set-select')`), '6+ Möglichkeiten: Auswahlliste');
    assert(await hitHeight(page, '#nopt-offline_after_s .row-main') >= 44, 'Auswahl-Zeile: Tippfläche >= 44 px');
    await page.eval(`(() => { const s = document.querySelector('#nopt-offline_after_s select'); s.value = '900'; s.dispatchEvent(new Event('change', { bubbles: true })); })()`);
    await until(async () => (await optOf('box_offline', 'offline_after_s')) === '900', 6000, 'offline nach 15 Min');
    await page.waitFor(`(document.querySelector('#nopt-offline_after_s .msg')?.textContent || '').includes('Gespeichert')`, 5000);
    await go(page, '#/einstellungen/meldungen');
    await page.waitFor(`(document.querySelector('#ntype-box_offline .set-level')?.textContent || '').trim() === 'kritisch'`, 5000);
    assert(await page.eval(`!!document.querySelector('#ntype-box_offline .set-level.pill-crit')`), 'Etikett „kritisch“ rot');
    await shot(page, 'mobile-notify');

    // ---- Detailseite einer Info-Art: Segmente, Fehler, Probe ---------------
    await page.click('#ntype-box_restart .set-ntype-link');
    await page.waitFor(`location.hash === '#/einstellungen/meldungen/box_restart' && !!document.querySelector('#nopt-level .seg')`, 5000);
    assert(await text(page, 'main h1') === 'Box neu gestartet', 'Titel = Art: ' + await text(page, 'main h1'));
    assert((await text(page, '.set-type-hint')).includes('Wenn die Box neu startet'), 'Kurzbeschreibung (hint)');
    const levelSegs = await page.eval(`Array.from(document.querySelectorAll('#nopt-level .seg')).map((b) => b.textContent.trim())`);
    assert(JSON.stringify(levelSegs) === JSON.stringify(['leise', 'normal', 'dringend', 'kritisch']), 'Dringlichkeit als Segment: ' + JSON.stringify(levelSegs));
    assert(await page.eval(`document.querySelectorAll('#nopt-scope .seg').length === 2 && document.querySelectorAll('#nopt-ignore_quiet .seg').length === 2`), '≤ 4 Möglichkeiten: Segmente');
    await clickSeg(page, '#nopt-level', 'normal');
    await until(async () => (await optOf('box_restart', 'level')) === 'active', 6000, 'Dringlichkeit normal');
    await page.waitFor(`(document.querySelector('#nopt-level .msg')?.textContent || '').includes('Gespeichert')`, 5000);
    // Nicht gespeichert: Segment springt auf den Stand des Servers zurück.
    await failAction(page, 'notify_set');
    await clickSeg(page, '#nopt-level', 'dringend');
    await page.waitFor(`(document.querySelector('#nopt-level .msg-error')?.textContent || '').includes('Testfehler')`, 5000);
    await page.waitFor(`(document.querySelector('#nopt-level .seg[aria-pressed="true"]')?.textContent || '').trim() === 'normal'`, 5000);
    await failAction(page, null);
    assert((await optOf('box_restart', 'level')) === 'active', 'Server unverändert normal');
    await shot(page, 'mobile-type');
    // Probe: Doppeltipp schickt genau EINE Meldung an den Status-Key.
    const mark = barkLog(backend).length;
    await page.eval(`(() => { const b = document.querySelector('#set-probe button'); b.click(); b.click(); })()`);
    await page.waitFor(`(document.querySelector('#set-probe .msg')?.textContent || '').includes('Probe gesendet')`, 15000);
    await sleep(500);
    const probes = barkLog(backend).slice(mark);
    assert(probes.length === 1 && probes[0].key === 'statuskey0001', 'genau eine Probe an statuskey0001: ' + JSON.stringify(probes));
    assert(await page.eval(`!document.querySelector('#set-probe button').disabled`), 'Probe-Knopf wieder frei');

    // ---- Ruhezeit & Allgemein -------------------------------------------------
    await go(page, '#/einstellungen/meldungen');
    await page.waitFor(`!!document.querySelector('#set-general a')`, 5000);
    await page.click('#set-general a');
    await page.waitFor(`location.hash === '#/einstellungen/allgemein' && !!document.querySelector('#gopt-quiet_enabled .seg')`, 5000);
    assert(await text(page, 'main h1') === 'Ruhezeit & Allgemein', 'Titel Allgemein');
    assert(await text(page, '.back-link') === 'Meldungen', 'von Meldungen gekommen: zurück zu Meldungen');
    assert(await page.eval(`!!document.querySelector('#gopt-quiet_from select') && !!document.querySelector('#gopt-sound select') && !!document.querySelector('#gopt-extra_id select')`), 'Globale Auswahllisten');
    await clickSeg(page, '#gopt-quiet_enabled', 'ein');
    await until(async () => (await globalOf('quiet_enabled')) === '1', 6000, 'Ruhezeit ein');
    const quietNow = (await notify()).quiet_now;
    await page.waitFor(`${quietNow ? '' : '!'}document.querySelector('.set-quiet-now')`, 5000);
    if (quietNow) assert((await text(page, '.set-quiet-now')).includes('Gerade ist Ruhezeit'), 'Gerade ist Ruhezeit');
    await go(page, '#/einstellungen');
    await page.waitFor(`(document.querySelector('#set-quiet .row-value')?.textContent || '').trim() === '22–07 Uhr'`, 5000);
    // Von der Liste aus: zurück führt zur Liste.
    await page.click('#set-quiet a');
    await page.waitFor(`location.hash === '#/einstellungen/allgemein'`, 5000);
    assert(await text(page, '.back-link') === 'Einstellungen', 'von der Liste gekommen: zurück zu Einstellungen');

    // ---- Automatisch wieder laut nach ------------------------------------------
    await go(page, '#/einstellungen');
    await page.click('#set-mute a');
    await page.waitFor(`location.hash === '#/einstellungen/stumm' && !!document.querySelector('.set-choice[data-value="14400"]')`, 5000);
    assert(await text(page, 'main h1') === 'Automatisch wieder laut nach', 'Titel Stumm');
    assert((await text(page, '.set-choice[aria-pressed="true"] .row-label')) === '12 Std', 'gewählt: 12 Std');
    assert((await text(page, '.set-mute-page')).includes('Gilt auch für laufende Stummschaltungen.'), 'Hinweis laufende Stummschaltungen');
    assert(await hitHeight(page, '.set-choice[data-value="14400"]') >= 44, 'Auswahl: Tippfläche >= 44 px');
    // Verkürzen, während Anna schon 5 Std stumm ist: Sie würde sofort laut
    // (und bekäme eine Nachricht) - erst nachfragen, mit Namen.
    assert((await admin.post({ action: 'keys_mute', id: String(ids.anna), muted: '1' })).ok, 'Anna stumm');
    editData(backend, 'alarm_keys.json', (d) => { d.keys.find((k) => k.id === ids.anna).muted_at = Math.floor(Date.now() / 1000) - 5 * 3600; });
    await page.eval('FW.store.refreshKeys()');
    await page.waitFor(`(FW.store.getState().keys?.keys || []).some((k) => k.id === ${ids.anna} && k.muted && Date.now() / 1000 - k.muted_at > 4 * 3600)`, 5000);
    await page.click('.set-choice[data-value="14400"]');
    await waitConfirm(page);
    assert((await text(page, '.confirm-title')).includes('4 Std'), 'Frage nennt den neuen Wert: ' + await text(page, '.confirm-title'));
    const shortenQ = await text(page, '.confirm-text');
    assert(shortenQ.includes('1 stumm geschalteter Empfänger wird sofort wieder laut') && shortenQ.includes('Anna') && !shortenQ.includes('Ben'), 'Frage nennt Anna: ' + shortenQ);
    await shot(page, 'mobile-mute-shorten');
    await page.click('.confirm .btn-cancel');
    await page.waitFor(`!document.querySelector('.confirm')`, 3000);
    await sleep(300);
    assert((await list()).mute_max_seconds === 43200, 'Abbrechen: unverändert 12 Std');
    assert((await text(page, '.set-choice[aria-pressed="true"] .row-label')) === '12 Std', 'Abbrechen: Auswahl bleibt 12 Std');
    await page.click('.set-choice[data-value="14400"]');
    await waitConfirm(page);
    await page.click('.confirm .btn-danger');
    await until(async () => (await list()).mute_max_seconds === 14400, 6000, 'mute_max_seconds 14400');
    await until(async () => !(await list()).keys.find((k) => k.id === ids.anna).muted, 6000, 'Anna wieder laut');
    await page.waitFor(`(document.querySelector('.set-mute-page .msg')?.textContent || '').includes('Gespeichert')`, 5000);
    const ben = (await list()).keys.find((k) => k.id === ids.ben);
    assert(ben.mute_until === ben.muted_at + 14400, 'Ben: mute_until = muted_at + 4 Std');
    await go(page, '#/empfaenger');
    const benSub = await page.eval(`'Stumm bis ~' + FW.logic.fmtClock(${ben.mute_until})`);
    await page.waitFor(`(document.querySelector('#rcp-${ids.ben} .row-sub')?.textContent || '').includes(${q(benSub)})`, 8000);
    await go(page, '#/einstellungen');
    await page.waitFor(`(document.querySelector('#set-mute .row-value')?.textContent || '').trim() === '4 Std'`, 5000);
    await go(page, '#/einstellungen/stumm');
    await page.waitFor(`!!document.querySelector('.set-choice[data-value="43200"]')`, 5000);
    await page.click('.set-choice[data-value="43200"]');
    await until(async () => (await list()).mute_max_seconds === 43200, 6000, 'zurück auf 12 Std');

    // ---- Demo-Modus ----------------------------------------------------------
    await go(page, '#/einstellungen');
    await page.click('#set-demo a');
    await page.waitFor(`location.hash === '#/einstellungen/demo' && !!document.querySelector('#set-demo-target option[value="${ids.anna}"]')`, 5000);
    assert((await text(page, '.set-demo-state')).includes('LIVE'), 'Zustand LIVE: ' + await text(page, '.set-demo-state'));
    await page.eval(`(() => { const s = document.querySelector('#set-demo-target'); s.value = '${ids.anna}'; s.dispatchEvent(new Event('change', { bubbles: true })); })()`);
    const annaMasked = (await list()).keys.find((k) => k.id === ids.anna).key_masked;
    await page.click('.set-demo-toggle');
    await waitConfirm(page);
    const demoQ = await text(page, '.confirm-text');
    assert(demoQ === `Demo-Modus einschalten? Alarme gehen dann NUR an "Anna (${annaMasked})". Nach dem Testen unbedingt wieder auf LIVE zurückschalten!`,
      'Demo-Frage: ' + demoQ);
    assert(demoQ.includes(annaMasked), 'Demo-Frage nennt den maskierten Key: ' + demoQ);
    // Box meldet sich gerade (online), hat aber noch die LIVE-Liste: Nach dem
    // Einschalten muss erst die Warnung stehen, dass ein Relaisalarm noch an
    // ALLE ginge - „übernommen“ erst nach ihrem nächsten Status.
    await pushBox(backend, admin);
    await page.click('.confirm .btn-danger');
    await page.waitFor(`!!document.querySelector('.demo-band') && (document.querySelector('.set-demo-state')?.textContent || '').includes('DEMO-MODUS')`, 8000);
    assert((await list()).demo.enabled === true, 'Server: Demo an');
    await page.eval('FW.store.refreshStatus()');
    await page.waitFor(`(document.querySelector('.set-demo-sync.is-warn')?.textContent || '').includes('NOCH NICHT')`, 8000);
    const pendingSync = await text(page, '.set-demo-sync');
    assert(pendingSync.includes('ein Relaisalarm würde noch an ALLE gehen!'), 'Warnung „noch an ALLE“: ' + pendingSync);
    await shot(page, 'mobile-demo-pending');
    await pushBox(backend, admin);
    await page.eval('FW.store.refreshStatus()');
    await page.waitFor(`(document.querySelector('.set-demo-sync')?.textContent || '').includes('Die Box hat die Demo-Liste übernommen – du kannst jetzt gefahrlos testen.')`, 8000);
    await shot(page, 'mobile-demo');
    for (const hash of ['#/uebersicht', '#/empfaenger', '#/verlauf', '#/einstellungen']) {
      await go(page, hash);
      await page.waitFor(`!!document.querySelector('.demo-band') && (document.querySelector('.demo-band')?.textContent || '').includes('nur Anna')`, 5000);
    }
    assert(await text(page, '#set-demo .row-value') === 'nur Anna', 'Liste: Demo „nur Anna“');
    await go(page, '#/uebersicht');
    await page.waitFor(`(document.querySelector('.ov-alarm')?.textContent || '').trim() === 'Demo-Alarm an Anna'`, 5000);
    await page.click('.demo-band a');
    await page.waitFor(`location.hash === '#/einstellungen/demo' && !!document.querySelector('.set-demo-toggle')`, 5000);
    await page.click('.set-demo-toggle');
    await waitConfirm(page);
    assert(await text(page, '.confirm-text') === 'Demo-Modus beenden? Danach gehen Alarme wieder an ALLE Empfänger (LIVE).', 'Ende-Frage: ' + await text(page, '.confirm-text'));
    await page.click('.confirm .btn-danger');
    await page.waitFor(`!document.querySelector('.demo-band') && (document.querySelector('.set-demo-state')?.textContent || '').includes('LIVE')`, 8000);
    assert((await list()).demo.enabled === false, 'Server: Demo aus');
    // Empfängerliste noch nicht geladen (keys_list hängt): „wird geladen“,
    // nicht „keine Empfänger“ - und Einschalten geht noch nicht.
    const hangId = (await page.send('Page.addScriptToEvaluateOnNewDocument', { source: `(() => {
      const real = window.fetch;
      window.fetch = (url, opts) => (String(url).includes('action=keys_list') ? new Promise(() => {}) : real(url, opts));
    })()` })).identifier;
    await open(page, base, '#/einstellungen/demo');
    await page.waitFor(`(document.querySelector('.set-demo-state')?.textContent || '').includes('LIVE') && !!document.querySelector('.set-g-demo-target .set-empty')`, 8000);
    assert(await text(page, '.set-g-demo-target .set-empty') === 'Empfängerliste wird geladen…', 'Liste lädt: ' + await text(page, '.set-g-demo-target .set-empty'));
    assert(await page.eval(`document.querySelector('.set-demo-toggle').disabled === true`), 'Liste lädt: Einschalten gesperrt');
    await page.send('Page.removeScriptToEvaluateOnNewDocument', { identifier: hangId });
    await open(page, base, '#/einstellungen');
    await page.waitFor(`(document.querySelector('#set-notify .row-value')?.textContent || '').includes('von')`, 10000);

    // ---- Box: Details & Diagnose -----------------------------------------------
    await pushBox(backend, admin);
    await go(page, '#/einstellungen/box');
    await page.waitFor(`document.querySelectorAll('.box-row').length === 14`, 8000);
    assert(await text(page, 'main h1') === 'Box', 'Titel Box');
    const boxHeads = await page.eval(`Array.from(document.querySelectorAll('.set-box-page .group-head')).map((h) => h.textContent.trim())`);
    assert(JSON.stringify(boxHeads) === JSON.stringify(['Alarmkette', 'Verbindung', 'Wartung']), 'Box-Gruppen: ' + JSON.stringify(boxHeads));
    assert(await page.eval(`!document.querySelector('#box-wifi .box-tip') && document.querySelector('#box-wifi .row-main').getAttribute('aria-expanded') === 'false'`), 'Erklärung zu');
    await page.click('#box-wifi .row-main');
    await page.waitFor(`(document.querySelector('#box-wifi .box-tip')?.textContent || '').includes('Empfangsstärke')`, 3000);
    assert(await page.eval(`document.querySelector('#box-wifi .row-main').getAttribute('aria-expanded') === 'true'`), 'aria-expanded=true');
    assert(await page.eval(`!document.querySelector('.set-box-stale')`), 'frisch: kein „letzter bekannter Stand“');
    await shot(page, 'mobile-box');
    await page.click('#box-wifi .row-main');
    await page.waitFor(`!document.querySelector('#box-wifi .box-tip')`, 3000);
    await page.click('.set-probe-window summary');
    await page.waitFor(`(document.querySelector('.set-probe-window')?.textContent || '').includes('18:55')`, 3000);
    // Box offline: gedimmt, mit dem Stand von wann.
    setStatusAge(backend, 120);
    await page.eval('FW.store.refreshStatus()');
    await page.waitFor(`(document.querySelector('.set-box-stale')?.textContent || '').includes('letzter bekannter Stand von') && !!document.querySelector('#box-wifi.tone-unknown')`, 8000);
    await shot(page, 'mobile-box-offline');
    await pushBox(backend, admin);
    await page.eval('FW.store.refreshStatus()');
    await page.waitFor(`!document.querySelector('.set-box-stale')`, 8000);

    // ---- Design: Hell bleibt über das Neuladen -----------------------------------
    await go(page, '#/einstellungen');
    await clickSeg(page, '#set-theme', 'Hell');
    await page.waitFor(`document.documentElement.dataset.theme === 'light'`, 3000);
    assert(await page.eval(`localStorage.getItem('fwAlarm.theme')`) === 'light', 'gespeichert: light');
    await open(page, base, '#/einstellungen');
    await page.waitFor(`document.documentElement.dataset.theme === 'light' && (document.querySelector('#set-notify .row-value')?.textContent || '').includes('von')`, 10000);
    assert(await pressedSeg(page, '#set-theme') === 'Hell', 'nach dem Neuladen: Hell gewählt');
    await sleep(400);
    await shot(page, 'mobile-light');
    await go(page, '#/einstellungen/meldungen');
    await page.waitFor(`!!document.querySelector('#ntype-box_offline')`, 5000);
    await sleep(300);
    await shot(page, 'mobile-notify-light');
    await go(page, '#/einstellungen');
    await clickSeg(page, '#set-theme', 'Dunkel');
    await page.waitFor(`document.documentElement.dataset.theme === 'dark' && localStorage.getItem('fwAlarm.theme') === 'dark'`, 3000);

    // ---- Hinweis im Browser bei Störung ---------------------------------------
    const stubId = (await page.send('Page.addScriptToEvaluateOnNewDocument', { source: NOTIFICATION_STUB })).identifier;
    await open(page, base, '#/einstellungen');
    await page.waitFor(`!!document.querySelector('#set-alert .switch') && typeof window.__notes === 'object'`, 10000);
    assert(await checked(page, '#set-alert') === 'false', 'Hinweis im Browser: aus');
    await page.click('#set-alert .switch');
    await page.waitFor(`document.querySelector('#set-alert .switch').getAttribute('aria-checked') === 'true' && localStorage.getItem('fwAlarm.browserAlert') === '1'`, 5000);
    assert(await page.eval('window.__permAsked') === 1, 'Erlaubnis einmal erfragt');
    // Ausgangsstand abwarten: Die erste Status-Antwort legt fest, wovon aus ein
    // Übergang zählt (php -S arbeitet Anfragen nacheinander ab).
    await page.waitFor(`FW.store.getState().lastOkAt > 0 && ['ok', 'hints'].includes(FW.store.derive().verdict.key)`, 10000);
    await page.eval('FW.store.refreshStatus()');
    // Tab versteckt, Box verstummt: genau EINE Benachrichtigung.
    await page.eval(`(() => { Object.defineProperty(document, 'hidden', { value: true, configurable: true }); document.dispatchEvent(new Event('visibilitychange')); })()`);
    setStatusAge(backend, 120);
    await page.eval('FW.store.refreshStatus()');
    await page.waitFor(`window.__notes.length === 1`, 5000);
    const note1 = await page.eval(`({ title: window.__notes[0].title, body: window.__notes[0].body, tag: window.__notes[0].tag })`);
    assert(note1.title === 'Alarm-Wächter' && note1.tag === 'fw-alarm' && note1.body.includes('Box offline'), 'Benachrichtigung: ' + JSON.stringify(note1));
    await page.eval('FW.store.refreshStatus()');
    await sleep(300);
    assert(await page.eval('window.__notes.length') === 1, 'gleicher Zustand: keine zweite Benachrichtigung');
    await pushBox(backend, admin);
    await page.eval('FW.store.refreshStatus()');
    await page.waitFor(`window.__notes.length === 2`, 5000);
    const note2 = await page.eval(`window.__notes[1].body`);
    assert(note2.includes('Wieder in Ordnung'), '„wieder in Ordnung“: ' + note2);
    await page.eval(`window.__notes[0].onclick && window.__notes[0].onclick()`);
    assert(await page.eval('window.__focused >= 1 && window.__notes[0].closed === true'), 'Klick holt das Fenster nach vorn');
    await page.eval(`(() => { delete document.hidden; document.dispatchEvent(new Event('visibilitychange')); })()`);
    await page.click('#set-alert .switch');
    await page.waitFor(`document.querySelector('#set-alert .switch').getAttribute('aria-checked') === 'false' && localStorage.getItem('fwAlarm.browserAlert') === null`, 5000);
    await page.send('Page.removeScriptToEvaluateOnNewDocument', { identifier: stubId });
    // iPhone-App (Home-Bildschirm): ohne diese Zeile.
    const standaloneId = (await page.send('Page.addScriptToEvaluateOnNewDocument', { source: STANDALONE_STUB })).identifier;
    await open(page, base, '#/einstellungen');
    await page.waitFor(`!!document.querySelector('#set-theme')`, 10000);
    assert(await page.eval(`navigator.standalone === true && !document.querySelector('#set-alert')`), 'iOS-App: kein „Hinweis im Browser“');
    await page.send('Page.removeScriptToEvaluateOnNewDocument', { identifier: standaloneId });

    // ---- PC ----------------------------------------------------------------------
    await page.viewport({ width: 1280, height: 800, mobile: false, scale: 1 });
    await open(page, base, '#/einstellungen');
    await page.waitFor(`!!document.querySelector('.sidebar') && (document.querySelector('#set-notify .row-value')?.textContent || '').includes('von')`, 10000);
    assert(await page.eval(`document.documentElement.scrollWidth <= innerWidth`), 'PC: kein waagrechtes Scrollen');
    await shot(page, 'wide');
    await go(page, '#/einstellungen/meldungen/box_offline');
    await page.waitFor(`!!document.querySelector('#nopt-level .seg')`, 5000);
    await shot(page, 'wide-type');

    // ---- Kleinstes iPhone (320 px): dichteste Seiten ohne waagrechtes Scrollen -----
    await page.viewport({ width: 320, height: 568, mobile: true, scale: 2 });
    for (const [hash, ready, name] of [['#/einstellungen/meldungen', '#ntype-box_offline', 'small-notify'], ['#/einstellungen/box', '#box-keys', 'small-box']]) {
      await go(page, hash);
      await page.waitFor(`!!document.querySelector(${q(ready)})`, 5000);
      await sleep(200);
      assert(await page.eval(`document.documentElement.scrollWidth <= innerWidth`), `320 px: kein waagrechtes Scrollen (${hash})`);
      await shot(page, name);
    }
    // Bezeichnungen brechen nicht mitten im Wort um: Jedes Wort des Textes
    // liegt auf genau einer Zeile (ein Rechteck je Wort).
    const wordsWhole = await page.eval(`(() => {
      const node = document.querySelector('#box-keys .row-label').firstChild;
      let at = 0;
      return node.textContent.split(' ').every((w) => {
        const r = document.createRange();
        r.setStart(node, at); r.setEnd(node, at + w.length); at += w.length + 1;
        return r.getClientRects().length === 1;
      });
    })()`);
    assert(wordsWhole, '320 px: „Empfängerliste (Box)“ bricht nur zwischen Wörtern');

    // ---- Lese-Rolle --------------------------------------------------------------
    await page.viewport({ width: 390, height: 844, mobile: true, scale: 2 });
    await page.send('Network.clearBrowserCookies');
    await page.login(base, 'leser', 'test');
    adminReads.length = 0;
    await open(page, base, '#/einstellungen');
    await page.waitFor(`!!document.querySelector('#set-box') && !!document.querySelector('#set-theme')`, 10000);
    await sleep(1500);
    assert(await page.eval(`!document.querySelector('#set-notify') && !document.querySelector('#set-demo') && !document.querySelector('#set-mute') && !document.querySelector('#set-quiet')`),
      'Lese-Rolle: keine Meldungen/Demo/Stumm/Ruhezeit');
    const roHeads = await page.eval(`Array.from(document.querySelectorAll('main .group-head')).map((h) => h.textContent.trim())`);
    assert(JSON.stringify(roHeads) === JSON.stringify(['Box', 'Dieses Gerät', 'Konto']), 'Lese-Rolle: Gruppen ' + JSON.stringify(roHeads));
    assert((await text(page, '#set-account')).includes('nur lesen'), 'Konto: nur lesen');
    await shot(page, 'readonly');
    await go(page, '#/einstellungen/meldungen');
    await page.waitFor(`location.hash === '#/einstellungen'`, 5000);
    await go(page, '#/einstellungen/box');
    await page.waitFor(`document.querySelectorAll('.box-row').length === 14`, 8000);
    assert(adminReads.length === 0, 'Lese-Rolle fragt notify_list/settings_list nicht ab: ' + adminReads.join(', '));

    assert(problems.length === 0, 'keine Konsolenfehler:\n' + problems.join('\n'));
  } finally {
    const note = (what, r) => { if (!r || !r.ok) console.error(`Aufräumen: ${what} fehlgeschlagen: ${JSON.stringify(r)}`); };
    try {
      note('Demo aus', await admin.post({ action: 'demo_set', enabled: '0' }));
      note('Stumm-Dauer', await admin.post({ action: 'settings_set', key: 'mute_max_seconds', value: String(muteBefore) }));
      backups.forEach(restoreFile);
      await removeOwn();
      await pushBox(backend, admin);
    } catch (e) {
      console.error('Aufräumen: ' + e.message);
    }
  }
}
