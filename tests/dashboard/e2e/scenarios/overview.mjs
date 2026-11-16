// Übersicht der neuen Oberfläche (index.php): Status-Karte samt Urteil,
// Hinweise, Prüfliste, Auslösen mit Halten-zum-Senden, Ergebnis-Karte („wer
// wurde NICHT erreicht“, Anrufen, Nachsendung), Schutz der offenen Bestätigung
// (Tab versteckt, Moduswechsel, 409), Box offline auf kleinem iPhone, NAS
// nicht erreichbar, Alarm-Zustände, TEST-Befehl, Lese-Rolle und die rechte
// Spalte am PC.
// Die Sandbox behält ihre Daten zwischen den Läufen: Das Szenario legt seine
// drei Empfänger mit eindeutigen Keys an, nimmt vorhandene für die Dauer des
// Laufs heraus (sonst stimmte „3 Empfänger“ nicht) und stellt sie am Ende
// wieder her.
import { apiSession } from '../cdp.mjs';
import { pushBox, runWatchdog, readData, editData, setStatusAge, barkLog, sandboxServer } from '../sandbox.mjs';

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const q = (sel) => JSON.stringify(sel);

async function center(page, sel) {
  return page.eval(`(() => {
    const r = document.querySelector(${q(sel)}).getBoundingClientRect();
    return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
  })()`);
}

// Echte Maus: drücken, halten, loslassen (Pointer-Ereignisse wie ein Mensch).
async function mouseHold(page, sel, ms) {
  const { x, y } = await center(page, sel);
  await page.send('Input.dispatchMouseEvent', { type: 'mouseMoved', x, y });
  await page.send('Input.dispatchMouseEvent', { type: 'mousePressed', x, y, button: 'left', buttons: 1, clickCount: 1 });
  await sleep(ms);
  await page.send('Input.dispatchMouseEvent', { type: 'mouseReleased', x, y, button: 'left', buttons: 0, clickCount: 1 });
}

// Bestätigung über einen Knopf der Übersicht öffnen; erst nach dem
// Hereingleiten steht der Halten-Knopf an seiner endgültigen Stelle.
async function openConfirmVia(page, sel) {
  await page.click(sel);
  await page.waitFor(`(() => { const s = document.querySelector('.sheet-layer .sheet');
    return !!s && !!document.querySelector('.confirm') && s.getAnimations().every((a) => a.playState !== 'running'); })()`);
}

const text = (page, sel) => page.eval(`(document.querySelector(${q(sel)})?.textContent || '').replace(/\\s+/g, ' ').trim()`);
const titleIs = (t) => `document.querySelector('.status-title')?.textContent.trim() === ${q(t)}`;
const cardHas = (t) => `(document.querySelector('.result-card')?.textContent || '').includes(${q(t)})`;
// Steht „Auslösen“ vor der Prüfliste (rotes Urteil) oder danach (normal)?
const actionsRaised = (page) => page.eval(`(() => {
  const a = document.querySelector('.ov-actions'), c = document.querySelector('.ov-checks');
  return !!a && !!c && !!(a.compareDocumentPosition(c) & Node.DOCUMENT_POSITION_FOLLOWING);
})()`);
// Ergebnis-Karte für den Screenshot ins Bild holen (wartet ein sanftes
// Scrollen der Seite selbst ab).
async function cardInView(page) {
  await sleep(600);
  await page.eval(`document.querySelector('.result-card').scrollIntoView({ block: 'center' })`);
  await sleep(100);
}

export default async function ({ browser, base, sandbox, assert, shot }) {
  const admin = await apiSession(base, 'admin', 'test');
  const u = Date.now().toString(36);
  // Key-Präfix fail = die Bark-Attrappe antwortet mit HTTP 500.
  const K = { josh: 'josh' + u, tom: 'fail' + u, anna: 'anna' + u };
  const ids = {};
  const list = () => admin.get('keys_list');
  // Vorhandene Empfänger (frühere Läufe) mit vollem Key merken - die API
  // liefert nur maskierte Keys, die Datei im data_dir die echten. Reste eines
  // abgebrochenen Laufs DIESES Szenarios werden nur entfernt, nicht wieder
  // angelegt (sonst würden sie von Lauf zu Lauf weitergereicht).
  const stored = readData(sandbox, 'alarm_keys');
  const before = (stored && Array.isArray(stored.keys) ? stored.keys : [])
    .map((k) => ({ id: k.id, label: k.label, key: k.key, phone: k.phone || '' }));
  const leftover = (k) => ['Josh', 'Tom', 'Anna'].includes(k.label) && /^(josh|fail|anna)[0-9a-z]{6,}$/.test(k.key);
  let serverDown = false;

  const page = await browser.newPage();
  await page.send('Log.enable');
  await page.send('Network.enable');
  const problems = [];
  page.on('Runtime.consoleAPICalled', (p) => {
    if (p.type === 'error' || p.type === 'assert') problems.push('console: ' + p.args.map((a) => a.value ?? a.description).join(' '));
  });
  page.on('Runtime.exceptionThrown', (p) => problems.push('exception: ' + (p.exceptionDetails.exception?.description || p.exceptionDetails.text)));
  // Abgelehnte Anfragen (409, Server gestoppt) meldet Chrome als Netz-Fehler -
  // die gehören hier zum Ablauf; alles andere (z.B. CSP) zählt.
  page.on('Log.entryAdded', (p) => {
    if (p.entry.level === 'error' && p.entry.source !== 'network') problems.push('log: ' + p.entry.text + ' ' + (p.entry.url || ''));
  });
  // HTTP-Status der POSTs an dashboard_api.php (für den 409-Schritt).
  const posts = new Map();
  const postStatus = [];
  page.on('Network.requestWillBeSent', (p) => {
    if (p.request.method === 'POST' && p.request.url.includes('dashboard_api.php')) posts.set(p.requestId, true);
  });
  page.on('Network.responseReceived', (p) => { if (posts.has(p.requestId)) postStatus.push(p.response.status); });
  // Laufende Abrufe der Empfängerliste (die Alarm-Frage holt sie beim Öffnen
  // neu): Vor dem Einfrieren der Abfragen abwarten, sonst sähe ein später
  // eintreffender Abruf den Moduswechsel und schlösse die Frage zu Recht.
  const keysInFlight = new Set();
  page.on('Network.requestWillBeSent', (p) => { if (p.request.url.includes('action=keys_list')) keysInFlight.add(p.requestId); });
  page.on('Network.loadingFinished', (p) => keysInFlight.delete(p.requestId));
  page.on('Network.loadingFailed', (p) => keysInFlight.delete(p.requestId));
  const keysSettled = async () => {
    for (let i = 0; i < 100 && keysInFlight.size; i++) await sleep(50);
    assert(keysInFlight.size === 0, 'Abruf der Empfängerliste hängt');
  };
  // Eingriffe ins Netz (CDP Fetch): 'fail-post' = POSTs brechen ab, bevor sie
  // das NAS erreichen; 'hold-post' = die Antwort eines POSTs wird
  // zurückgehalten (heldResponses), bis das Szenario sie freigibt.
  let fetchMode = null;
  const heldResponses = [];
  page.on('Fetch.requestPaused', (p) => {
    const post = p.request.method === 'POST';
    const atResponse = p.responseStatusCode !== undefined || p.responseErrorReason !== undefined;
    if (fetchMode === 'fail-post' && post && !atResponse) {
      page.send('Fetch.failRequest', { requestId: p.requestId, errorReason: 'ConnectionReset' }).catch(() => {});
    } else if (fetchMode === 'hold-post' && post && atResponse) {
      heldResponses.push(p.requestId);
    } else {
      page.send('Fetch.continueRequest', { requestId: p.requestId }).catch(() => {});
    }
  });

  try {
    // ---- Empfänger: genau Josh, Tom (Telefon, scheitert), Anna (stumm, 3) ---
    assert((await admin.post({ action: 'demo_set', enabled: '0' })).ok, 'Demo aus');
    const add = async (name, label, key, phone) => {
      const known = new Set((await list()).keys.map((k) => k.id));
      const fields = { action: 'keys_add', label, key };
      if (phone) fields.phone = phone;
      const r = await admin.post(fields);
      assert(r.ok === true, `keys_add ${label}: ` + JSON.stringify(r));
      const added = (await list()).keys.filter((k) => !known.has(k.id));
      assert(added.length === 1, 'neue id für ' + label);
      ids[name] = added[0].id;
    };
    await add('josh', 'Josh', K.josh);
    await add('tom', 'Tom', K.tom, '+49 171 1');
    await add('anna', 'Anna', K.anna);
    assert((await admin.post({ action: 'keys_mute', id: String(ids.anna), muted: '1' })).ok, 'Anna stumm');
    assert((await admin.post({ action: 'keys_set_mute_volume', id: String(ids.anna), volume: '3' })).ok, 'Anna Lautstärke 3');
    for (const k of before) {
      const r = await admin.post({ action: 'keys_delete', id: String(k.id) });
      assert(r.ok, `alten Empfänger ${k.label} herausnehmen: ` + JSON.stringify(r));
    }
    assert((await list()).keys.length === 3, 'genau 3 Empfänger');

    // ---- Bereit: erst mit Hinweis (Wächter nie gelaufen), dann alles bereit
    if (readData(sandbox, 'watchdog_state')) editData(sandbox, 'watchdog_state', (d) => { d.last_run_at = 0; });
    await pushBox(base, admin);
    await page.viewport({ width: 390, height: 844, mobile: true, scale: 2 });
    await page.login(base, 'admin', 'test');
    problems.length = 0;   // nur die neue Oberfläche zählt
    await page.goto('about:blank');
    await page.goto(`${base}/index.php`);
    await page.waitFor(titleIs('Bereit – 1 Hinweis'), 10000);
    assert((await text(page, '.ov-hints')).includes('Offline-Wächter'), 'Hinweis nennt den Offline-Wächter');
    assert(await page.eval(`!!document.querySelector('.ov-hints .row.tone-warn')`), 'Hinweis-Zeile orange');
    assert(await page.eval(`!!document.querySelector('.chk-watch.tone-warn')`), 'Prüfzeile Wächter orange');
    assert((await page.eval('document.title')).startsWith('🟡'), 'Tab-Titel gelb bei einem Hinweis');
    await shot(page, 'ready-hint');

    runWatchdog(sandbox);
    await pushBox(base, admin);
    await page.waitFor(titleIs('Alles bereit'), 12000);
    await page.waitFor(`document.title.startsWith('🟢')`, 5000);
    const rows = await page.eval(`JSON.stringify(Array.from(document.querySelectorAll('.ov-checks .row')).map((r) => r.className.match(/chk-(\\w+)/)?.[1]))`);
    assert(rows === JSON.stringify(['box', 'wifi', 'watch', 'keys', 'alarm']), 'Prüfliste: ' + rows);
    assert(await page.eval(`!!document.querySelector('.chk-box a[href="#/einstellungen/box"]') && !document.querySelector('.chk-wifi a')`), 'Box führt zu den Details, WLAN ohne Ziel');
    assert((await text(page, '.chk-keys')).includes('3, 1 stumm'), 'Empfänger-Zeile: ' + await text(page, '.chk-keys'));
    assert(await page.eval(`!document.querySelector('.ov-hints')`), 'keine Hinweise');
    assert(!(await actionsRaised(page)), 'normal: Prüfliste vor „Auslösen“');
    assert(await text(page, '.ov-alarm') === 'Alarm an alle senden', 'Alarm-Knopf: ' + await text(page, '.ov-alarm'));
    assert(await page.eval(`!document.querySelector('.ov-box-alarm')`), 'LIVE: kein „Demo-Alarm über die Box“');
    assert(await page.eval(`document.documentElement.scrollWidth <= innerWidth`), 'kein waagrechtes Scrollen');
    await shot(page, 'ready');

    // ---- PC: rechte Spalte mit Letzten Ereignissen und Stummen ---------------
    await page.viewport({ width: 1280, height: 800, mobile: false, scale: 1 });
    await page.waitFor(`!!document.querySelector('.sidebar') && document.querySelectorAll('.ov-recent .ov-event').length > 0`, 12000);
    assert(await page.eval(`(() => { const l = document.querySelector('.status-card').getBoundingClientRect();
      const r = document.querySelector('.ov-recent').getBoundingClientRect(); return r.left >= l.right; })()`), 'PC: Ereignisse rechts neben der Status-Karte');
    assert(await page.eval(`document.querySelectorAll('.ov-recent .ov-event').length <= 5`), 'höchstens 5 Ereignisse');
    assert(await text(page, '.ov-recent a[href="#/verlauf"]') === 'Ganzen Verlauf anzeigen', 'Link zum Verlauf');
    const muted = await text(page, '.ov-muted');
    assert(muted.includes('Anna') && muted.includes('Lautstärke 3') && !muted.includes('Josh'), 'Stumm geschaltet: ' + muted);
    assert(await page.eval(`document.documentElement.scrollWidth <= innerWidth`), 'PC: kein waagrechtes Scrollen');
    await shot(page, 'wide');
    await page.viewport({ width: 390, height: 844, mobile: true, scale: 2 });
    await page.waitFor(`!!document.querySelector('.tabbar') && !document.querySelector('.ov-recent')`);

    // ---- Halten zum Senden ----------------------------------------------------
    let mark = barkLog(sandbox).length;
    await openConfirmVia(page, '.ov-alarm');
    const question = await text(page, '.confirm-text');
    assert(question.includes('3 Empfänger'), 'Frage nennt 3 Empfänger: ' + question);
    assert(question.includes('Anna (stumm) mit Lautstärke 3'), 'Frage nennt Annas Lautstärke: ' + question);
    // Der Halten-Knopf nennt die Aktion; die Beschreibung bleibt.
    assert(await text(page, '.confirm .hold > .hold-label') === 'Halten: Alarm senden', 'Halten-Knopf: ' + await text(page, '.confirm .hold > .hold-label'));
    assert(await page.eval(`document.getElementById(document.querySelector('.confirm .hold').getAttribute('aria-describedby'))?.textContent.trim() === 'Zum Senden gedrückt halten'`), 'Halten-Knopf beschrieben');
    await shot(page, 'confirm');
    await mouseHold(page, '.confirm .hold', 300);
    await sleep(700);
    assert(barkLog(sandbox).length === mark, 'kurz gedrückt: nichts gesendet');
    assert(await page.eval(`!!document.querySelector('.confirm')`), 'kurz gedrückt: Bestätigung bleibt offen');
    await mouseHold(page, '.confirm .hold', 1600);
    await page.waitFor(`!document.querySelector('.confirm')`, 3000);
    await page.waitFor(`(document.querySelector('.result-title')?.textContent || '').includes('2 von 3 erreicht')`, 40000);
    assert(await page.eval(`(() => { const li = document.querySelector('.result-list li');
      return !!li && li.textContent.includes('Tom') && !!li.querySelector('a[href^="tel:"]'); })()`), 'Tom zuerst, mit Anrufen');
    assert(await page.eval(`document.querySelector('.result-list a[href^="tel:"]').getAttribute('href')`) === 'tel:+491711', 'tel:-Link ohne Leerzeichen');
    assert(await text(page, '.result-retry') === 'Nur an Tom erneut senden', 'Nachsenden: ' + await text(page, '.result-retry'));
    const reached = await text(page, '.result-reached');
    assert(reached.includes('Josh') && reached.includes('Anna') && !reached.includes('Tom'), 'Erreicht: ' + reached);
    let sent = barkLog(sandbox).slice(mark);
    const to = (k) => sent.filter((e) => e.key === k);
    for (const k of Object.values(K)) {
      assert(to(k).length >= 1 && to(k).every((e) => e.fields.level === 'critical'), `level=critical an ${k}`);
    }
    assert(to(K.anna).every((e) => String(e.fields.volume) === '3'), 'Anna mit Lautstärke 3');
    assert(to(K.josh).every((e) => String(e.fields.volume) === '10' && String(e.fields.call) === '1'), 'Josh laut mit call=1');
    await cardInView(page);
    await shot(page, 'result');

    // ---- Nachsendung nur an Tom -----------------------------------------------
    mark = barkLog(sandbox).length;
    await openConfirmVia(page, '.result-retry');
    assert(await text(page, '.confirm-title') === 'Erneut senden?', 'Nachsende-Frage');
    assert((await text(page, '.confirm-text')).includes('NUR an: Tom'), 'Nachsende-Frage nennt Tom');
    assert(await text(page, '.confirm .hold > .hold-label') === 'Halten: erneut senden', 'Halten-Knopf der Nachsendung');
    await mouseHold(page, '.confirm .hold', 1600);
    await page.waitFor(`(document.querySelector('.result-title')?.textContent || '').includes('0 von 1 erreicht')`, 40000);
    sent = barkLog(sandbox).slice(mark);
    const hit = [...new Set(sent.map((e) => e.key))];
    // Genau EIN Empfänger; die Attrappe antwortet 500, das NAS versucht es
    // deshalb bis zu bark_max_tries (3) Mal.
    assert(hit.length === 1 && hit[0] === K.tom && sent.length <= 3, 'Nachsendung nur an Tom: ' + JSON.stringify(sent.map((e) => e.key)));
    assert(sent.every((e) => String(e.fields.body).includes('verspaetet zugestellt')), 'Text „verspaetet zugestellt“');
    assert(await page.eval(cardHas('Weiterhin NICHT erreicht')), 'Weiterhin NICHT erreicht');
    assert((await text(page, '.result-list li')).includes('Tom'), 'Tom weiter in der Liste');
    await cardInView(page);
    await shot(page, 'resend');
    // Niemand erreicht: „Ausblenden“ steht direkt unter „erneut senden“ - mind.
    // 8 px Luft bis zu seiner Tippfläche (elementFromPoint: treffbare Pixel).
    assert(await page.eval(`!document.querySelector('.result-reached')`), 'keine Zeile „Erreicht“ dazwischen');
    const gap = await page.eval(`(() => {
      const retry = document.querySelector('.result-retry').getBoundingClientRect();
      const d = document.querySelector('.result-dismiss');
      const x = d.getBoundingClientRect().left + d.getBoundingClientRect().width / 2;
      for (let y = Math.ceil(retry.bottom); y < retry.bottom + 80; y++) {
        const hit = document.elementFromPoint(x, y);
        if (hit && (hit === d || d.contains(hit))) return y - retry.bottom;
      }
      return 999;
    })()`);
    assert(gap >= 8, `„Ausblenden“ beginnt ${gap} px unter „erneut senden“ (mind. 8)`);
    // „Ausblenden“ mit gemerkten nicht Erreichten: erst nachfragen, Abbrechen
    // lässt alles stehen.
    await page.click('.result-dismiss');
    await page.waitFor(`!!document.querySelector('.confirm') && (document.querySelector('.confirm-title')?.textContent || '').includes('ausblenden')`, 3000);
    assert((await text(page, '.confirm-text')).includes('Tom'), 'Ausblenden-Frage nennt Tom');
    await page.click('.confirm .btn-cancel');
    await page.waitFor(`!document.querySelector('.confirm')`, 3000);
    assert(await page.eval(`!!document.querySelector('.result-card') && localStorage.getItem('fwAlarm.lastUnreached') !== null`), 'Abbrechen: Liste bleibt');

    // Zurück auf die Übersicht: Das Ergebnis steht noch da, wird aber NICHT
    // erneut angesagt (nur echte Wechsel).
    await page.eval(`(() => { window.__said = []; window.__announce = FW.ui.announce;
      FW.ui.announce = (t) => { window.__said.push(String(t)); return window.__announce(t); }; })()`);
    await page.eval(`location.hash = '#/verlauf'`);
    await page.waitFor(`document.querySelector('main h1')?.textContent.trim() === 'Verlauf'`);
    await page.eval(`location.hash = '#/uebersicht'`);
    await page.waitFor(`!!document.querySelector('.result-card') && ${cardHas('Weiterhin NICHT erreicht')}`);
    await sleep(400);
    const said = JSON.parse(await page.eval('JSON.stringify(window.__said)'));
    assert(!said.some((t) => t.includes('erreicht')), 'Ergebnis beim Zurückkehren nicht erneut angesagt: ' + JSON.stringify(said));
    await page.eval('FW.ui.announce = window.__announce');

    // ---- Neuladen: die nicht Erreichten bleiben (localStorage) ---------------
    await page.goto('about:blank');
    await page.goto(`${base}/index.php`);
    await page.waitFor(cardHas('Auf diesem Gerät gemerkt'), 10000);
    // Die Nummer kommt mit der Empfängerliste (eigene Abfrage nach dem Laden).
    await page.waitFor(`(() => { const li = document.querySelector('.result-list li');
      return !!li && li.textContent.includes('Tom') && !!li.querySelector('a[href^="tel:"]'); })()`, 10000);
    assert(await page.eval(cardHas('Zuletzt NICHT erreicht')), 'nach dem Neuladen: „Zuletzt NICHT erreicht“');
    assert(await text(page, '.result-retry') === 'Nur an Tom erneut senden', 'nach dem Neuladen: Nachsendung angeboten');
    await cardInView(page);
    await shot(page, 'reload');

    // ---- Antwort unklar, dann Neuladen: KEINE Nachsendung, erst Verlauf ------
    // Der Alarm-POST bricht ab, bevor er das NAS erreicht (Verbindung
    // zurückgesetzt) - für die Seite heißt das „Antwort unklar“. Danach wie
    // nach einem Wechsel in die Telefon-App: Seite neu geladen.
    await page.waitFor(titleIs('Alles bereit'), 10000);
    mark = barkLog(sandbox).length;
    fetchMode = 'fail-post';
    await page.send('Fetch.enable', { patterns: [{ urlPattern: '*dashboard_api.php*', requestStage: 'Request' }] });
    await openConfirmVia(page, '.ov-alarm');
    await mouseHold(page, '.confirm .hold', 1600);
    await page.waitFor(cardHas('Antwort unklar'), 15000);
    fetchMode = null;
    await page.send('Fetch.disable');
    assert(barkLog(sandbox).length === mark, 'abgebrochen: nichts gesendet');
    assert(await page.eval(`!document.querySelector('.result-retry')`), 'unklar: keine Nachsendung');
    assert(await page.eval(`localStorage.getItem('fwAlarm.sendPending') !== null`), 'Merker „ohne Ergebnis“ gesetzt');
    await page.goto('about:blank');
    await page.goto(`${base}/index.php`);
    await page.waitFor(cardHas('Letzter Versand ohne Ergebnis'), 10000);
    await page.waitFor(`(document.querySelector('.result-list li')?.textContent || '').includes('Tom')`, 10000);
    assert(await page.eval(`!document.querySelector('.result-retry')`), 'nach dem Neuladen: KEINE Nachsendung');
    assert(await page.eval(`!!document.querySelector('.result-check-log')`), 'nach dem Neuladen: „Im Verlauf prüfen“');
    await cardInView(page);
    await shot(page, 'restored-unclear');
    // „Im Verlauf prüfen“: Alarme, ohne alte Suche, frisch geladen.
    await page.eval(`FW.store.setLogQuery('gibtesnichtxyz')`);
    await page.click('.result-check-log');
    await page.waitFor(`location.hash === '#/verlauf?filter=alarm' && document.querySelectorAll('.log-row').length > 0`, 10000);
    assert(await page.eval(`document.querySelector('.log-search').value === '' && FW.store.getState().log.query === ''`), 'Im Verlauf prüfen: Suche geleert');
    assert(await text(page, '.log-filter .seg[aria-pressed=true]') === 'Alarme', 'Im Verlauf prüfen: Filter Alarme');
    // Zurück wie vorher (sonst „Ausblenden“): Merker weg, Seite neu.
    await page.eval(`localStorage.removeItem('fwAlarm.sendPending')`);
    await page.goto('about:blank');
    await page.goto(`${base}/index.php`);
    await page.waitFor(cardHas('Auf diesem Gerät gemerkt'), 10000);

    // ---- Tab versteckt: offene Bestätigung schließt, nichts geht raus ---------
    await page.waitFor(titleIs('Alles bereit'), 10000);
    mark = barkLog(sandbox).length;
    await openConfirmVia(page, '.ov-alarm');
    await page.eval(`(() => { Object.defineProperty(document, 'hidden', { value: true, configurable: true });
      document.dispatchEvent(new Event('visibilitychange')); })()`);
    await page.waitFor(`!document.querySelector('.confirm')`, 3000);
    await page.eval(`(() => { delete document.hidden; document.dispatchEvent(new Event('visibilitychange')); })()`);
    await sleep(800);
    assert(barkLog(sandbox).length === mark, 'versteckt: nichts gesendet');
    assert(await page.eval(`!document.querySelector('.confirm')`), 'zurück im Vordergrund: Bestätigung bleibt zu');

    // ---- Moduswechsel von einem anderen Gerät, während die Frage offen ist ---
    const other = await apiSession(base, 'admin', 'test');
    mark = barkLog(sandbox).length;
    await openConfirmVia(page, '.ov-alarm');
    assert((await other.post({ action: 'demo_set', enabled: '1', id: String(ids.josh) })).ok, 'Demo ein (anderes Gerät)');
    await page.waitFor(`!document.querySelector('.confirm') && ${cardHas('Demo-/LIVE-Modus hat sich gerade geändert')}`, 7000);
    assert(barkLog(sandbox).length === mark, 'Moduswechsel: nichts gesendet');
    await cardInView(page);
    await shot(page, 'mode-changed');
    // Demo-Modus: Knöpfe nennen den Test-Empfänger, der Box-Alarm erscheint.
    await pushBox(base, admin);
    await page.waitFor(`document.querySelector('.ov-alarm')?.textContent.trim() === 'Demo-Alarm an Josh'
      && !!document.querySelector('.ov-box-alarm') && !!document.querySelector('.demo-band')`, 10000);
    await page.waitFor(`${titleIs('Demo-Modus')} && (document.querySelector('.demo-band')?.textContent || '').includes('Box hat übernommen')`, 10000);
    assert(await text(page, '.ov-box-alarm') === 'Demo-Alarm über die Box', 'Box-Alarm-Knopf');
    await page.eval('scrollTo(0, 0)');
    await shot(page, 'demo');

    // ---- 409: Modus wechselt zwischen Bestätigen und Senden -------------------
    assert((await admin.post({ action: 'demo_set', enabled: '0' })).ok, 'Demo aus');
    await pushBox(base, admin);
    await page.waitFor(`document.querySelector('.ov-alarm')?.textContent.trim() === 'Alarm an alle senden' && !document.querySelector('.demo-band')`, 10000);
    mark = barkLog(sandbox).length;
    await openConfirmVia(page, '.ov-alarm');
    // Status und Empfängerliste kommen bis zum Senden nicht durch: Sonst holte
    // eine Abfrage den neuen Modus noch vor dem Halten ab - dann schlösse die
    // Bestätigung (Schritt oben), statt dass der Server mit 409 ablehnt.
    // (FW.store.stop() reicht dafür nicht mehr: Fokus/pageshow starten die
    // Abfragen bei sichtbarer Seite von selbst wieder.)
    await keysSettled();
    await page.send('Network.setBlockedURLs', { urls: ['*action=status*', '*action=keys_list*'] });
    assert((await other.post({ action: 'demo_set', enabled: '1', id: String(ids.josh) })).ok, 'Demo ein (anderes Gerät)');
    postStatus.length = 0;
    await mouseHold(page, '.confirm .hold', 1600);
    await page.waitFor(cardHas('NICHT gesendet'), 15000);
    await page.send('Network.setBlockedURLs', { urls: [] });
    assert(postStatus.includes(409), 'Server antwortet 409: ' + JSON.stringify(postStatus));
    assert(barkLog(sandbox).length === mark, '409: nichts gesendet');
    await page.waitFor(`!!document.querySelector('.demo-band')`, 5000);
    assert(await page.eval(`!document.querySelector('.result-retry')`), '409: keine Nachsendung angeboten');
    await cardInView(page);
    await shot(page, '409');
    assert((await admin.post({ action: 'demo_set', enabled: '0' })).ok, 'Demo aus');

    // ---- Box offline, kleines iPhone -----------------------------------------
    await page.viewport({ width: 320, height: 568, mobile: true, scale: 2 });
    await pushBox(base, admin);
    setStatusAge(sandbox, 600);
    await page.waitFor(`${titleIs('Box offline')} && document.title.startsWith('🔴')`, 10000);
    await page.eval('scrollTo(0, 0)');
    assert(await page.eval(`!!document.querySelector('.status-card.is-crit')`), 'Status-Karte rot');
    assert(await actionsRaised(page), 'offline: „Auslösen“ direkt unter der Status-Karte');
    assert(await page.eval(`(() => { const b = document.querySelector('.ov-alarm').getBoundingClientRect();
      const bar = document.querySelector('.tabbar').getBoundingClientRect();
      return b.bottom <= innerHeight && b.bottom <= bar.top; })()`), '320×568: Alarm-Knopf ohne Scrollen sichtbar');
    assert(await page.eval(`document.documentElement.scrollWidth <= innerWidth`), '320 px: kein waagrechtes Scrollen');
    assert(await page.eval(`!!document.querySelector('.chk-wifi.tone-unknown')`), 'WLAN grau (unbekannt)');
    assert(await page.eval(`!!document.querySelector('.chk-box.tone-crit')`), 'Box-Zeile rot');
    await shot(page, 'offline-small');
    await page.viewport({ width: 390, height: 844, mobile: true, scale: 2 });
    await page.eval('scrollTo(0, 0)');
    await shot(page, 'offline');

    // ---- Server nicht erreichbar: rot nach „offline“, sonst orange --------------
    // Ungünstigster Fall fürs kleine iPhone: Band „Server nicht erreichbar“ UND
    // Demo-Band oben, dazu das längste rote Urteil („Server nicht erreichbar“,
    // letzter Stand offline) - der Alarm-Knopf muss trotzdem über der
    // Tab-Leiste stehen.
    assert((await admin.post({ action: 'demo_set', enabled: '1', id: String(ids.josh) })).ok, 'Demo ein');
    await pushBox(base, admin);
    setStatusAge(sandbox, 600);
    await page.waitFor(`${titleIs('Box offline')} && !!document.querySelector('.demo-band')`, 10000);
    await page.viewport({ width: 320, height: 568, mobile: true, scale: 2 });
    await sandboxServer(sandbox, 'stop', 'site');
    serverDown = true;
    await page.waitFor(`${titleIs('Server nicht erreichbar')} && !!document.querySelector('.status-card.is-crit') && !!document.querySelector('.demo-band')
      && !!document.querySelector('.bands .unsure-band.is-crit')`, 16000);
    await page.eval('scrollTo(0, 0)');
    assert((await text(page, '.status-card')).includes('Box OFFLINE'), 'nennt den letzten Stand „Box OFFLINE“');
    // Was fehlt und von wann der Stand ist, sagt das Band (rot: zuletzt offline).
    const redBand = await text(page, '.unsure-band');
    assert(redBand.includes('Keine Verbindung zum Server') && /letzter bekannter Stand \(\d\d:\d\d:\d\d\): Box OFFLINE/.test(redBand), 'Band rot: ' + redBand);
    assert(await page.eval(`!document.querySelector('.status-age')`), 'keine doppelte Problemzeile in der Karte');
    assert(await page.eval(`document.querySelector('.bands').firstElementChild.classList.contains('unsure-band')`), 'NAS-Band über dem Demo-Band');
    const fit = await page.eval(`(() => { const b = document.querySelector('.ov-alarm').getBoundingClientRect();
      const bar = document.querySelector('.tabbar').getBoundingClientRect();
      return { bottom: Math.round(b.bottom), bar: Math.round(bar.top), width: document.documentElement.scrollWidth }; })()`);
    assert(fit.bottom <= fit.bar, `320×568 mit Demo-Band und Server-Störung: Alarm-Knopf endet bei ${fit.bottom} px, Tab-Leiste beginnt bei ${fit.bar} px`);
    assert(fit.width <= 320, '320 px: kein waagrechtes Scrollen (' + fit.width + ')');
    await shot(page, 'nas-red-small-demo');
    await page.viewport({ width: 390, height: 844, mobile: true, scale: 2 });
    await page.eval('scrollTo(0, 0)');
    await shot(page, 'nas-red');
    await sandboxServer(sandbox, 'start', 'site');
    serverDown = false;
    assert((await admin.post({ action: 'demo_set', enabled: '0' })).ok, 'Demo aus');
    await pushBox(base, admin);
    await page.waitFor(titleIs('Alles bereit') + ` && !document.querySelector('.bands')`, 15000);
    // Den Verlauf einmal laden (das Gerät merkt sich den Stand), zurück.
    await page.eval(`location.hash = '#/verlauf'`);
    await page.waitFor(`document.querySelectorAll('.log-row').length > 0 && !document.querySelector('.log-stale')`, 10000);
    await page.eval(`location.hash = '#/uebersicht'`);
    await page.waitFor(titleIs('Alles bereit'), 5000);
    await sandboxServer(sandbox, 'stop', 'site');
    serverDown = true;
    await page.waitFor(`${titleIs('Server nicht erreichbar')} && !!document.querySelector('.status-card.is-warn') && !!document.querySelector('.unsure-band:not(.is-crit)')`, 16000);
    const band = await text(page, '.unsure-band');
    assert(band.includes('Keine Verbindung zum Server') && /angezeigte Werte können veraltet sein \(Stand \d\d:\d\d:\d\d\)/.test(band), 'Band orange: ' + band);
    for (const id of ['box', 'wifi', 'watch', 'keys', 'alarm']) {
      assert(await page.eval(`!!document.querySelector('.chk-${id}.tone-unknown')`), `Prüfzeile ${id} grau, solange das NAS nicht antwortet`);
    }
    await shot(page, 'nas-orange');
    // Verlauf ohne NAS: der gemerkte Stand bleibt lesbar, aber mit Hinweis
    // oben - und das Band steht auch hier.
    await page.eval(`location.hash = '#/verlauf'`);
    await page.waitFor(`!!document.querySelector('.log-stale') && document.querySelectorAll('.log-row').length > 0 && !!document.querySelector('.unsure-band')`, 15000);
    assert(/^Verlauf konnte nicht aktualisiert werden – Stand von \d\d:\d\d$/.test(await text(page, '.log-stale')), 'Verlauf: ' + await text(page, '.log-stale'));
    assert(await page.eval(`(() => { const s = document.querySelector('.log-stale'), r = document.querySelector('.log-row');
      return !!(s.compareDocumentPosition(r) & Node.DOCUMENT_POSITION_FOLLOWING); })()`), 'Hinweis steht über den Einträgen');
    await page.eval('scrollTo(0, 0)');
    await shot(page, 'nas-log');
    await page.eval(`location.hash = '#/uebersicht'`);
    await sandboxServer(sandbox, 'start', 'site');
    serverDown = false;
    await pushBox(base, admin);
    await page.waitFor(titleIs('Alles bereit') + ` && !document.querySelector('.bands')`, 15000);

    // ---- Alarm-Zustände der Box ----------------------------------------------
    await pushBox(base, admin, { alarm_pending: '1', key_progress: '1/3' });
    await page.waitFor(`${titleIs('Alarm läuft')} && !!document.querySelector('.status-card.is-alarm')`, 10000);
    assert((await text(page, '.status-card')).includes('1/3'), 'Alarm läuft nennt „1/3“');
    assert(await actionsRaised(page), 'Alarm läuft: „Auslösen“ oben');
    await shot(page, 'alarm-running');
    await pushBox(base, admin, { relay: 'closed', alarm_pending: '0', key_progress: '3/3', last_alarm: 'x' });
    await page.waitFor(`${titleIs('Alarm ausgelöst')} && !!document.querySelector('.status-card.is-crit')`, 10000);
    assert(await actionsRaised(page), 'Alarm ausgelöst: „Auslösen“ oben');
    await shot(page, 'alarm-standing');
    await pushBox(base, admin, { stuck_warned: '1', relay: 'closed' });
    await page.waitFor(`${titleIs('Kontakt klemmt')} && !!document.querySelector('.status-card.is-warn')`, 10000);
    assert(!(await actionsRaised(page)), 'Kontakt klemmt (orange): „Auslösen“ an der normalen Stelle');
    await shot(page, 'stuck');
    await pushBox(base, admin);
    await page.waitFor(titleIs('Alles bereit'), 10000);

    // ---- TEST über die Box: Befehlszeile mit „Abbrechen“ ---------------------
    await page.click('.ov-test');
    await page.waitFor(`(document.querySelector('.cmd-line')?.textContent || '').includes('wartet auf die Box') && !!document.querySelector('.cmd-cancel')`, 10000);
    await page.eval(`document.querySelector('.cmd-line').scrollIntoView({ block: 'center' })`);
    await shot(page, 'test-pending');
    await page.click('.cmd-cancel');
    await page.waitFor(`(document.querySelector('.cmd-line')?.textContent || '').includes('abgebrochen') && !document.querySelector('.cmd-cancel')`, 10000);

    // ---- Abgemeldet während eines Alarms: Ergebnis bleibt, „Neu anmelden“ ---
    // Die Antwort auf den Alarm-POST wird zurückgehalten, bis die Sitzung
    // abgelaufen ist (Cookie gelöscht -> nächste Status-Abfrage 401); dann
    // kommt das Ergebnis. Ohne „Neu anmelden“ gäbe es in der Home-Bildschirm-
    // App keinen Weg weiter.
    await page.eval('scrollTo(0, 0)');
    fetchMode = 'hold-post';
    await page.send('Fetch.enable', { patterns: [{ urlPattern: '*dashboard_api.php*', requestStage: 'Response' }] });
    await openConfirmVia(page, '.ov-alarm');
    await mouseHold(page, '.confirm .hold', 1600);
    for (let i = 0; i < 400 && !heldResponses.length; i++) await sleep(50);
    assert(heldResponses.length === 1, 'Antwort des Alarms zurückgehalten: ' + heldResponses.length);
    await page.send('Network.clearBrowserCookies');
    await page.waitFor(`FW.store.getState().authLost === true`, 12000);
    assert(await page.eval(`!document.querySelector('.status-relogin')`), 'während des Versands noch kein „Neu anmelden“');
    fetchMode = null;
    for (const id of heldResponses.splice(0)) await page.send('Fetch.continueRequest', { requestId: id });
    await page.send('Fetch.disable');
    await page.waitFor(`(document.querySelector('.result-title')?.textContent || '').includes('von 3 erreicht')
      && !!document.querySelector('.status-relogin') && !!document.querySelector('.result-relogin')`, 15000);
    assert(await text(page, '.status-title') === 'Abgemeldet', 'Urteil Abgemeldet');
    assert(/^Abgemeldet – angezeigte Werte werden nicht mehr aktualisiert/.test(await text(page, '.unsure-band')), 'Band: abgemeldet');
    assert(await page.eval(`document.querySelector('.status-relogin').getAttribute('href') === 'index.php'
      && document.querySelector('.result-relogin').getAttribute('href') === 'index.php'`), '„Neu anmelden“ führt zur Anmeldung');
    assert(await page.eval(`!document.querySelector('.result-check-log')`), 'abgemeldet: statt „Im Verlauf prüfen“ „Neu anmelden“');
    await page.eval('scrollTo(0, 0)');
    await shot(page, 'relogin');
    await cardInView(page);
    await shot(page, 'relogin-result');

    // ---- Lese-Rolle ------------------------------------------------------------
    // Mit allem, was dem Admin Knöpfe zeigen würde: Demo-Modus, wartender
    // TEST-Befehl, gemerkte nicht Erreichte (localStorage dieses Geräts).
    const headed = `Array.from(document.querySelectorAll('h1, h2, h3, h4, .group-head')).some((h) => h.textContent.trim() === 'Auslösen')`;
    assert(await page.eval(headed), 'Admin: Gruppe „Auslösen“ vorhanden');
    assert((await admin.post({ action: 'demo_set', enabled: '1', id: String(ids.josh) })).ok, 'Demo ein');
    await pushBox(base, admin);
    assert((await admin.post({ action: 'enqueue', type: 'TEST' })).ok, 'TEST angelegt (Admin)');
    await page.send('Network.clearBrowserCookies');
    await page.login(base, 'leser', 'test');
    await page.goto('about:blank');
    await page.goto(`${base}/index.php`);
    await page.waitFor(`!!document.querySelector('.status-card') && !!document.querySelector('.ov-readonly')
      && (document.querySelector('.cmd-line')?.textContent || '').includes('wartet auf die Box')
      && !!document.querySelector('.demo-band') && ${cardHas('Tom')}`, 10000);
    assert(!(await page.eval(headed)), 'Lese-Rolle: keine Gruppe „Auslösen“');
    for (const sel of ['.ov-alarm', '.ov-test', '.ov-false-alarm', '.ov-box-alarm', '.cmd-cancel', '.result-retry', 'a[href^="tel:"]']) {
      assert(await page.eval(`!document.querySelector(${q(sel)})`), `Lese-Rolle: kein ${sel}`);
    }
    assert((await text(page, '.ov-readonly')).includes('Admin'), 'Satz: einen Admin verständigen');
    await shot(page, 'readonly');

    // ---- Ausblenden: erst nachfragen, dann Ergebnis-Karte und Liste weg -----
    await page.click('.result-dismiss');
    await page.waitFor(`!!document.querySelector('.confirm .btn-danger')`, 3000);
    assert(await page.eval(`!!document.querySelector('.result-card')`), 'erst nach dem Bestätigen ausgeblendet');
    await page.click('.confirm .btn-danger');
    await page.waitFor(`!document.querySelector('.result-card')`, 3000);
    assert(await page.eval(`localStorage.getItem('fwAlarm.lastUnreached') === null`), 'gemerkte Liste gelöscht');
    assert((await admin.post({ action: 'cancel' })).ok, 'TEST abgebrochen (Admin)');
    assert((await admin.post({ action: 'demo_set', enabled: '0' })).ok, 'Demo aus');

    assert(problems.length === 0, 'keine Konsolenfehler:\n' + problems.join('\n'));
  } finally {
    // Aufräumen: Server wieder an, Demo aus, alte Empfänger zurück, eigene weg
    // (den letzten lässt das NAS nicht löschen - dann bleibt er stehen).
    // Fehlschläge werden gemeldet, ändern aber das Ergebnis des Laufs nicht.
    const note = (what, r) => { if (!r || !r.ok) console.error(`Aufräumen: ${what} fehlgeschlagen: ${JSON.stringify(r)}`); };
    try {
      if (serverDown) await sandboxServer(sandbox, 'start', 'site');
      note('Demo aus', await admin.post({ action: 'demo_set', enabled: '0' }));
      // Ein noch wartender TEST-Befehl (Lauf mittendrin abgebrochen) würde
      // den nächsten Lauf blockieren; ohne wartenden Befehl ist das ein No-op.
      await admin.post({ action: 'cancel' });
      const now = readData(sandbox, 'alarm_keys');
      const present = new Set((now && Array.isArray(now.keys) ? now.keys : []).map((k) => k.key));
      for (const k of before) {
        if (present.has(k.key) || leftover(k)) continue;
        const fields = { action: 'keys_add', label: k.label, key: k.key };
        if (k.phone) fields.phone = k.phone;
        note(`${k.label} zurück`, await admin.post(fields));
      }
      const ownIds = Object.values(ids);
      for (const [i, id] of ownIds.entries()) {
        const r = await admin.post({ action: 'keys_delete', id: String(id) });
        // Der letzte Empfänger der Liste darf bleiben (NAS lehnt das Löschen ab).
        if (i < ownIds.length - 1 || before.some((k) => !leftover(k))) note(`Empfänger ${id} löschen`, r);
      }
      await pushBox(base, admin);
    } catch (err) {
      console.error('Aufräumen unvollständig:', err.message);
    }
  }
}
