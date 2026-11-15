// Verlauf der neuen Oberfläche (index.php#/verlauf): Filter Alle/Alarme/
// Empfänger/System, Suche über die geladenen Einträge, Tages-Abschnitte,
// Zeilen mit Symbol-Kachel, „Ältere laden“, Vorwahl per #/verlauf?filter=alarm,
// Lese-Rolle. Die Sandbox behält ihren Verlauf zwischen den Läufen: Das
// Szenario erzeugt Ereignisse mit eindeutigem Namen (Lena<zufall>) und prüft
// relativ dazu; seine Empfänger löscht es am Ende wieder.
import { apiSession } from '../cdp.mjs';
import { pushBox } from '../sandbox.mjs';

const q = (s) => JSON.stringify(s);
const text = (page, sel) => page.eval(`(document.querySelector(${q(sel)})?.textContent || '').replace(/\\s+/g, ' ').trim()`);

// Pixelzeilen (an x), in denen elementFromPoint das Element (oder ein
// Kind) trifft - die tatsächliche Tippfläche.
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

// Seite neu laden: Ein reiner Wechsel des #-Teils löst kein load-Ereignis aus.
async function open(page, base, hash) {
  await page.goto('about:blank');
  await page.goto(`${base}/index.php${hash}`);
}

const rows = (page) => page.eval(`Array.from(document.querySelectorAll('.log-row')).map((r) => ({
  text: r.querySelector('.log-text').textContent.replace(/\\s+/g, ' ').trim(),
  tile: Array.from(r.querySelector('.tile').classList).find((c) => c.startsWith('tile-')) || 'tile-neutral',
  time: r.querySelector('time') ? r.querySelector('time').getAttribute('datetime') : '',
  clock: r.querySelector('time') ? r.querySelector('time').textContent.trim() : ''
}))`);

async function typeInto(page, sel, value) {
  await page.waitFor(`!!document.querySelector(${q(sel)})`);
  await page.eval(`(() => { const el = document.querySelector(${q(sel)}); el.focus(); el.select(); })()`);
  if (value === '') {
    await page.send('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Backspace', code: 'Backspace', windowsVirtualKeyCode: 8 });
    await page.send('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Backspace', code: 'Backspace', windowsVirtualKeyCode: 8 });
  } else await page.send('Input.insertText', { text: value });
}

export default async function ({ browser, base, sandbox, assert, shot }) {
  const admin = await apiSession(base, 'admin', 'test');
  const u = Date.now().toString(36);
  const LENA = `Lena${u}`;
  const OTHER = `Moritz${u}`;
  const created = [];
  const add = async (label, key) => {
    const known = new Set((await admin.get('keys_list')).keys.map((k) => k.id));
    const r = await admin.post({ action: 'keys_add', label, key });
    assert(r.ok === true, `keys_add ${label}: ` + JSON.stringify(r));
    const id = (await admin.get('keys_list')).keys.find((k) => !known.has(k.id)).id;
    created.push(id);
    return id;
  };

  // Eigene Empfänger (auch Reste eines abgebrochenen Laufs) entfernen.
  const removeOwn = async () => {
    const list = await admin.get('keys_list');
    for (const k of (list.keys || [])) {
      if (/^(Lena|Moritz)[0-9a-z]+x?\d*$/.test(k.label)) {
        const r = await admin.post({ action: 'keys_delete', id: String(k.id) });
        if (!r.ok) console.error(`Aufräumen: Löschen ${k.label} fehlgeschlagen: ${JSON.stringify(r)}`);
      }
    }
  };

  const page = await browser.newPage();
  await page.send('Log.enable');
  const problems = [];
  page.on('Runtime.consoleAPICalled', (p) => {
    if (p.type === 'error' || p.type === 'assert') problems.push('console: ' + p.args.map((a) => a.value ?? a.description).join(' '));
  });
  page.on('Runtime.exceptionThrown', (p) => problems.push('exception: ' + (p.exceptionDetails.exception?.description || p.exceptionDetails.text)));
  page.on('Log.entryAdded', (p) => {
    if (p.entry.level === 'error' && p.entry.source !== 'network') problems.push('log: ' + p.entry.text + ' ' + (p.entry.url || ''));
  });

  try {
    // ---- Ereignisse erzeugen ------------------------------------------------
    assert((await admin.post({ action: 'demo_set', enabled: '0' })).ok, 'Demo aus');
    await admin.post({ action: 'cancel' });
    await removeOwn();
    const otherId = await add(OTHER, 'mori' + u + 'bb');
    // Genug Einträge für „Ältere laden“ (mehr als eine Seite à 25).
    for (let i = 0; i < 30; i++) {
      assert((await admin.post({ action: 'keys_rename', id: String(otherId), label: i % 2 ? OTHER : OTHER + i })).ok, 'Umbenennen ' + i);
    }
    // Lenas Ereignisse sind die neuesten (liegen in jeder ersten Seite).
    const lenaId = await add(LENA, 'lena' + u + 'aa');
    assert((await admin.post({ action: 'keys_mute', id: String(lenaId), muted: '1' })).ok, 'Lena stumm');
    assert((await admin.post({ action: 'keys_mute', id: String(lenaId), muted: '0' })).ok, 'Lena laut');
    assert((await admin.post({ action: 'keys_rename', id: String(lenaId), label: LENA + 'x' })).ok, 'Lena umbenannt');
    assert((await admin.post({ action: 'keys_rename', id: String(lenaId), label: LENA })).ok, 'Lena zurückbenannt');
    assert((await admin.post({ action: 'enqueue', type: 'TEST' })).ok, 'TEST angelegt');
    assert((await admin.post({ action: 'cancel' })).ok, 'TEST abgebrochen');
    const alarmRes = await admin.post({ action: 'alarm', expect_demo: '0' });
    assert(alarmRes.ok === true || alarmRes.status === 200, 'REAL ALARM gesendet: ' + JSON.stringify(alarmRes));
    await pushBox(base, admin);

    // ---- Mobil: Seite, Filter, Zeilen -----------------------------------------
    await page.viewport({ width: 390, height: 844, mobile: true, scale: 2 });
    await page.send('Network.clearBrowserCookies');
    await page.login(base, 'admin', 'test');
    problems.length = 0;
    await open(page, base, '#/verlauf');
    await page.waitFor(`document.querySelectorAll('.log-row').length > 0`, 10000);
    assert(await text(page, 'main h1') === 'Verlauf', 'Titel Verlauf');
    const seg = () => page.eval(`Array.from(document.querySelectorAll('.log-filter .seg')).map((b) => b.textContent.trim() + (b.getAttribute('aria-pressed') === 'true' ? '*' : ''))`);
    assert(JSON.stringify(await seg()) === JSON.stringify(['Alle*', 'Alarme', 'Empfänger', 'System']), 'Filter: ' + JSON.stringify(await seg()));
    assert((await text(page, '.log-hint')).includes('durchsucht die geladenen Einträge'), 'Suchhinweis');

    // Tages-Abschnitte (Überschriften) und die Reihenfolge: neueste zuerst.
    const heads = await page.eval(`Array.from(document.querySelectorAll('.log-day h2')).map((h) => h.textContent.trim())`);
    assert(heads.length >= 1 && heads[0] === 'Heute', 'erster Abschnitt „Heute“: ' + JSON.stringify(heads));
    assert(new Set(heads).size === heads.length, 'jeder Tag nur einmal: ' + JSON.stringify(heads));
    const all = await rows(page);
    assert(all.length === 25, 'erste Seite: 25 Einträge, nicht ' + all.length);
    assert(all.every((r) => /^\d\d:\d\d$/.test(r.clock) && r.time), 'jede Zeile mit <time datetime> und Uhrzeit');
    assert(await page.eval(`document.querySelectorAll('.log-row .tile .icon').length === document.querySelectorAll('.log-row').length`), 'jede Zeile mit Symbol');

    // Farben nach Art: Umbenennen grau, Alarm rot (nach Filter unten geprüft).
    assert(all.some((r) => r.text.includes('umbenannt') && r.tile === 'tile-neutral'), 'Umbenennen: graue Kachel');

    // ---- Älter laden --------------------------------------------------------
    assert(await page.eval(`!!document.querySelector('.log-more')`), '„Ältere laden“ sichtbar');
    assert(await hitHeight(page, '.log-more') >= 44, '„Ältere laden“: Tippfläche >= 44 px');
    // Doppeltipp: Der Umfang wächst nur um 25, der Knopf ist bis zur Antwort gesperrt.
    const during = await page.eval(`(() => {
      const b = document.querySelector('.log-more');
      b.click(); b.click();
      return { limit: FW.store.getState().log.limit, loading: FW.store.getState().log.loadingMore };
    })()`);
    assert(during.limit === 50 && during.loading === true, 'Doppeltipp: Umfang 50, lädt: ' + JSON.stringify(during));
    await page.waitFor(`document.querySelectorAll('.log-row').length > 25`, 10000);
    await page.waitFor(`!FW.store.getState().log.loadingMore`, 5000);
    assert(await page.eval(`FW.store.getState().log.limit`) === 50, 'Umfang nach Doppeltipp genau 50');
    assert(await text(page, '.log-more') === 'Ältere laden' && await page.eval(`!document.querySelector('.log-more').disabled`), 'Knopf wieder frei');
    const more = (await rows(page)).length;
    assert(more > 25 && more <= 50, 'nach „Ältere laden“ mehr Einträge: ' + more);

    // ---- Filter „Alarme“ ----------------------------------------------------
    await page.eval(`Array.from(document.querySelectorAll('.log-filter .seg')).find((b) => b.textContent.trim() === 'Alarme').click()`);
    await page.waitFor(`document.querySelector('.log-filter .seg[aria-pressed=true]').textContent.trim() === 'Alarme'
      && document.querySelectorAll('.log-row').length > 0 && !Array.from(document.querySelectorAll('.log-row')).some((r) => r.textContent.includes('umbenannt'))`, 10000);
    assert(await page.eval(`location.hash`) === '#/verlauf?filter=alarm', 'Adresse folgt dem Filter: ' + await page.eval('location.hash'));
    const alarms = await rows(page);
    assert(alarms.every((r) => /ALARM|ENTWARNUNG|Befehl|Nachsendung/i.test(r.text)), 'Alarme: nur Alarm-Ereignisse: ' + JSON.stringify(alarms.map((r) => r.text)));
    assert(alarms.some((r) => r.text.includes('REAL ALARM') && r.tile === 'tile-crit'), 'REAL ALARM mit roter Kachel');
    assert(!alarms.some((r) => r.text.includes(LENA)), 'Alarme: keine Empfänger-Ereignisse');
    assert(await page.eval(`document.querySelectorAll('.log-row').length <= 25`), 'Filterwechsel setzt auf 25 zurück');
    await shot(page, 'mobile-alarm');

    // ---- Filter „Empfänger“ + Suche -------------------------------------------
    await page.eval(`Array.from(document.querySelectorAll('.log-filter .seg')).find((b) => b.textContent.trim() === 'Empfänger').click()`);
    await page.waitFor(`document.querySelector('.log-filter .seg[aria-pressed=true]').textContent.trim() === 'Empfänger'
      && Array.from(document.querySelectorAll('.log-row')).some((r) => r.textContent.includes(${q(LENA)}))`, 10000);
    const keysRows = await rows(page);
    assert(keysRows.every((r) => /Empfänger|Stumm|stumm|Lautstärke|Test-Push|Bark-Key|Telefon/.test(r.text)), 'Empfänger: nur Empfänger-Ereignisse: ' + JSON.stringify(keysRows.map((r) => r.text)));
    assert(keysRows.some((r) => r.text.includes(LENA) && r.text.includes('hinzugefügt') && r.tile === 'tile-ok'), 'hinzugefügt: grüne Kachel');
    assert(keysRows.some((r) => r.text.includes(LENA) && r.text.includes('stummgeschaltet') && r.tile === 'tile-warn'), 'stummgeschaltet: orange Kachel');
    const beforeSearch = keysRows.length;
    await typeInto(page, '.log-search', LENA.toLowerCase());
    await page.waitFor(`document.querySelectorAll('.log-row').length > 0 && document.querySelectorAll('.log-row').length < ${beforeSearch}`, 5000);
    const found = await rows(page);
    assert(found.every((r) => r.text.toLowerCase().includes(LENA.toLowerCase())), 'Suche: nur Zeilen mit dem Namen: ' + JSON.stringify(found.map((r) => r.text)));
    assert(found.length >= 5, 'Suche: Lenas Ereignisse (add, mute, unmute, 2x rename): ' + found.length);
    await shot(page, 'mobile-search');
    // Ohne Treffer: eigener Text, nicht der „Noch keine Ereignisse“-Text.
    await typeInto(page, '.log-search', 'gibtesnichtxyz');
    await page.waitFor(`!!document.querySelector('.log-empty')`, 3000);
    assert((await text(page, '.log-empty')).includes('gibtesnichtxyz'), 'kein Treffer: nennt den Suchbegriff: ' + await text(page, '.log-empty'));
    assert(await page.eval(`document.querySelectorAll('.log-row').length === 0`), 'kein Treffer: keine Zeilen');
    // Leeren per Knopf (44 px) stellt alle Zeilen wieder her.
    assert(await hitHeight(page, '.log-search-clear') >= 44, 'Such-Leeren: Tippfläche >= 44 px');
    await page.click('.log-search-clear');
    await page.waitFor(`document.querySelectorAll('.log-row').length >= ${beforeSearch}`, 3000);
    assert(await page.eval(`document.querySelector('.log-search').value === ''`), 'Suchfeld leer');

    // ---- System: Box-/Wächter-Ereignisse (oder der Leertext) -------------------
    await page.eval(`Array.from(document.querySelectorAll('.log-filter .seg')).find((b) => b.textContent.trim() === 'System').click()`);
    await page.waitFor(`document.querySelector('.log-filter .seg[aria-pressed=true]').textContent.trim() === 'System'
      && (document.querySelectorAll('.log-row').length > 0 || (document.querySelector('.log-empty')?.textContent || 'Wird geladen…') !== 'Wird geladen…')`, 10000);
    assert(!(await rows(page)).some((r) => r.text.includes(LENA)), 'System: keine Empfänger-Ereignisse');
    if (await page.eval(`!!document.querySelector('.log-empty')`)) {
      assert((await text(page, '.log-empty')) === 'Keine System-Ereignisse im Verlauf.', 'System-Leertext: ' + await text(page, '.log-empty'));
    }

    // ---- Vorwahl per Adresse --------------------------------------------------
    await open(page, base, '#/verlauf?filter=alarm');
    await page.waitFor(`document.querySelectorAll('.log-row').length > 0`, 10000);
    assert((await seg()).includes('Alarme*'), 'Vorwahl: Alarme gewählt: ' + JSON.stringify(await seg()));
    assert(!(await rows(page)).some((r) => r.text.includes(LENA)), 'Vorwahl: nur Alarme');
    // Unbekannter Filter fällt auf „Alle“.
    await open(page, base, '#/verlauf?filter=quatsch');
    await page.waitFor(`document.querySelectorAll('.log-row').length > 0`, 10000);
    assert((await seg()).includes('Alle*'), 'unbekannter Filter: Alle');
    // Der Link der Übersicht („Letzter Alarm“) landet bei „Alarme“.
    await open(page, base, '#/uebersicht');
    await page.waitFor(`!!document.querySelector('a[href="#/verlauf?filter=alarm"]')`, 10000);
    await page.click('a[href="#/verlauf?filter=alarm"]');
    await page.waitFor(`(document.querySelector('.log-filter .seg[aria-pressed=true]')?.textContent || '').trim() === 'Alarme'`, 5000);

    // ---- Tippfläche der Filter ---------------------------------------------------
    await open(page, base, '#/verlauf');
    await page.waitFor(`document.querySelectorAll('.log-row').length > 0`, 10000);
    assert(await hitHeight(page, '.log-filter .seg') >= 44, 'Filter: Tippfläche >= 44 px');

    // ---- Breit ------------------------------------------------------------------
    await page.viewport({ width: 1280, height: 800, scale: 1 });
    await open(page, base, '#/verlauf');
    await page.waitFor(`document.querySelectorAll('.log-row').length > 0 && !!document.querySelector('.sidebar')`, 10000);
    assert(await page.eval(`document.documentElement.scrollWidth <= window.innerWidth`), 'breit: kein waagerechtes Scrollen');
    await shot(page, 'wide');

    // ---- Hell ---------------------------------------------------------------------
    await page.viewport({ width: 390, height: 844, mobile: true, scale: 2 });
    await page.eval(`localStorage.setItem('fwAlarm.theme', 'light')`);
    await open(page, base, '#/verlauf');
    await page.waitFor(`document.documentElement.dataset.theme === 'light' && document.querySelectorAll('.log-row').length > 0`, 10000);
    await new Promise((r) => setTimeout(r, 600));
    await shot(page, 'mobile-light');
    await page.eval(`localStorage.removeItem('fwAlarm.theme')`);

    // ---- Lese-Rolle ---------------------------------------------------------------
    await page.send('Network.clearBrowserCookies');
    await page.login(base, 'leser', 'test');
    await open(page, base, '#/verlauf');
    await page.waitFor(`document.querySelectorAll('.log-row').length > 0`, 10000);
    const ro = await rows(page);
    assert(!ro.some((r) => /Meldung an den Admin|Meldungen an den Admin|Einstellung „/.test(r.text)), 'Lese-Rolle: keine Admin-Einstellungen im Verlauf');
    assert(await page.eval(`!!document.querySelector('.log-filter') && !!document.querySelector('.log-search')`), 'Lese-Rolle: Filter und Suche da');

    assert(problems.length === 0, 'keine Konsolenfehler:\n' + problems.join('\n'));
  } finally {
    const note = (what, r) => { if (!r || !r.ok) console.error(`Aufräumen: ${what} fehlgeschlagen: ${JSON.stringify(r)}`); };
    try {
      note('Demo aus', await admin.post({ action: 'demo_set', enabled: '0' }));
      await admin.post({ action: 'cancel' });
      await removeOwn();
      await pushBox(base, admin);
    } catch (e) {
      console.error('Aufräumen: ' + e.message);
    }
  }
}
