// Empfänger der neuen Oberfläche (next.php#/empfaenger): Liste mit
// Status-Pillen und Abgleich-Zeile, Hinzufügen (mit Test-Push), Person als
// Sheet (iPhone) bzw. Panel rechts (PC) mit Laut/Stumm/Dauerhaft stumm,
// Stumm-Lautstärke, Telefon, Kurzbefehl-Links, Umbenennen, Key ersetzen und
// Löschen; eine Live-Aktualisierung mitten in einer Eingabe; zehn Empfänger
// mit langen Namen auf dem kleinsten iPhone; Lese-Rolle.
// Die Sandbox behält ihre Daten zwischen den Läufen: Vorhandene Empfänger
// werden für die Dauer des Laufs herausgenommen und am Ende wieder angelegt;
// alles, was das Szenario anlegt (Keys „lena0000001“ und „rcp…“), löscht es
// wieder - auch Reste eines abgebrochenen Laufs.
import { apiSession } from '../cdp.mjs';
import { pushBox, readData, barkLog } from '../sandbox.mjs';

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const q = (s) => JSON.stringify(s);
const LENA_KEY = 'lena0000001';
// Wortlaut wie im bisherigen Dashboard (app.js, Test-Push-Rückfrage).
const testPushText = (name) => `Test-Push an "${name}" senden? Auf dem iPhone dort spielt einmal der ALARMTON – als normale Mitteilung, kein Alarm (bei Stummschalter oder Fokus bleibt sie leise). Am besten vorher Bescheid geben.`;
const own = (k) => k.key === LENA_KEY || /^rcp[0-9a-z]{6,}$/.test(k.key);

const text = (page, sel) => page.eval(`(document.querySelector(${q(sel)})?.textContent || '').replace(/\\s+/g, ' ').trim()`);
const pillOf = (page, id) => text(page, `#rcp-${id} .rcp-state-pill`);
const labelOf = (page, id) => text(page, `#rcp-${id} .row-label`);

// Feld fokussieren, Inhalt markieren und wie per Tastatur ersetzen.
async function typeInto(page, sel, value) {
  await page.waitFor(`!!document.querySelector(${q(sel)})`);
  await page.eval(`(() => { const el = document.querySelector(${q(sel)}); el.focus(); el.select(); })()`);
  if (value === '') await keyPress(page, 'Backspace');
  else await page.send('Input.insertText', { text: value });
}

const KEYS = { ArrowRight: 39, ArrowLeft: 37, Enter: 13, Tab: 9, Escape: 27, Backspace: 8 };
async function keyPress(page, key) {
  const code = KEYS[key];
  const ev = { key, code: key, windowsVirtualKeyCode: code, nativeVirtualKeyCode: code };
  await page.send('Input.dispatchKeyEvent', Object.assign({ type: 'keyDown' }, ev, key === 'Enter' ? { text: '\r' } : {}));
  await page.send('Input.dispatchKeyEvent', Object.assign({ type: 'keyUp' }, ev));
}

// Bis Sheets/Bestätigungen fertig hereingeglitten sind (Positionen endgültig).
async function settle(page) {
  await page.waitFor(`Array.from(document.querySelectorAll('.sheet')).every((s) => s.getAnimations().every((a) => a.playState !== 'running'))`);
}
async function waitConfirm(page) {
  await page.waitFor(`!!document.querySelector('.confirm')`, 5000);
  await settle(page);
}
async function clickSeg(page, label) {
  await page.waitFor(`Array.from(document.querySelectorAll('.rcp-person .seg')).some((b) => b.textContent.trim() === ${q(label)})`);
  await page.eval(`Array.from(document.querySelectorAll('.rcp-person .seg')).find((b) => b.textContent.trim() === ${q(label)}).click()`);
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

// Genau 40 Zeichen (Höchstlänge des Namens).
const longName = (i) => `Maximiliane-Sophie Brandmeister-Oberhof ${i}`.slice(0, 39) + String(i % 10);

export default async function ({ browser, base, sandbox, assert, shot }) {
  const admin = await apiSession(base, 'admin', 'test');
  const other = await apiSession(base, 'admin', 'test');
  const u = Date.now().toString(36);
  const ANNA_KEY = 'rcpa' + u;
  const list = () => admin.get('keys_list');
  const entry = async (id) => (await list()).keys.find((k) => k.id === id);
  // Vorhandene Empfänger mit vollem Key merken (die API liefert nur maskierte).
  const stored = readData(sandbox, 'alarm_keys');
  const initial = stored && Array.isArray(stored.keys) ? stored.keys : [];
  const before = initial.filter((k) => !own(k)).map((k) => ({ id: k.id, label: k.label, key: k.key, phone: k.phone || '' }));
  const ids = {};

  const add = async (label, key, phone) => {
    const known = new Set((await list()).keys.map((k) => k.id));
    const fields = { action: 'keys_add', label, key };
    if (phone) fields.phone = phone;
    const r = await admin.post(fields);
    assert(r.ok === true, `keys_add ${label}: ` + JSON.stringify(r));
    const added = (await list()).keys.filter((k) => !known.has(k.id));
    assert(added.length === 1, 'neue id für ' + label);
    return added[0].id;
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
    // ---- Ausgangslage: nur Anna ---------------------------------------------
    assert((await admin.post({ action: 'demo_set', enabled: '0' })).ok, 'Demo aus');
    ids.anna = await add('Anna', ANNA_KEY);
    for (const k of (await list()).keys) {
      if (k.id === ids.anna) continue;
      const r = await admin.post({ action: 'keys_delete', id: String(k.id) });
      assert(r.ok, `Empfänger ${k.label} herausnehmen: ` + JSON.stringify(r));
    }
    assert((await list()).keys.length === 1, 'genau 1 Empfänger');
    await pushBox(base, admin);

    await page.viewport({ width: 390, height: 844, mobile: true, scale: 2 });
    await page.login(base, 'admin', 'test');
    problems.length = 0;   // nur die neue Oberfläche zählt
    await page.goto(`${base}/next.php#/empfaenger`);
    await page.waitFor(`!!document.querySelector('#rcp-${ids.anna}')`, 10000);
    assert(await page.eval(`document.querySelector('main h1')?.textContent.trim() === 'Empfänger'`), 'Titel Empfänger');
    await page.waitFor(`!!document.querySelector('.rcp-sync.is-ok')`, 10000);
    const sync = await text(page, '.rcp-sync');
    assert(sync.includes('1 Empfänger') && sync.includes('Box ist auf dem neuesten Stand'), 'Abgleich: ' + sync);
    assert(await pillOf(page, ids.anna) === 'Laut', 'Anna: Pille Laut');
    assert(await page.eval(`!!document.querySelector('.rcp-add')`), 'Knopf „+“');

    // ---- Hinzufügen: Lena mit Telefon, danach Test-Push ---------------------
    await page.click('.rcp-add');
    await page.waitFor(`!!document.querySelector('.rcp-add-form')`);
    await settle(page);
    assert(await page.eval(`document.activeElement?.id === 'rcp-add-name'`), 'Fokus im Namensfeld');
    await typeInto(page, '#rcp-add-name', 'Lena');
    await typeInto(page, '#rcp-add-key', 'ab');
    await page.waitFor(`!!document.querySelector('.rcp-key-hint.is-warn')`, 3000);
    assert((await text(page, '.rcp-key-hint')).includes('5–64 Zeichen'), 'Hinweis zum Key-Format: ' + await text(page, '.rcp-key-hint'));
    assert(await page.eval(`document.querySelector('.rcp-add-submit').disabled`), 'ungültiger Key: Hinzufügen gesperrt');
    await typeInto(page, '#rcp-add-key', LENA_KEY);
    await page.waitFor(`!!document.querySelector('.rcp-key-hint.is-ok')`, 3000);
    await typeInto(page, '#rcp-add-phone', '+49 171 2345678');
    assert(await page.eval(`document.querySelector('#rcp-add-phone').type === 'tel'`), 'Telefon-Feld type=tel');
    await shot(page, 'add');
    await page.click('.rcp-add-submit');
    await page.waitFor(`!!document.querySelector('.rcp-add-test')`, 10000);
    assert((await text(page, '.rcp-msg-add')).includes('hinzugefügt'), 'Rückmeldung: ' + await text(page, '.rcp-msg-add'));
    assert(await page.eval(`document.activeElement?.classList.contains('rcp-add-test')`), 'Fokus auf „Test-Push senden“');
    ids.lena = (await list()).keys.find((k) => k.label === 'Lena').id;
    assert((await entry(ids.lena)).phone === '+49 171 2345678', 'Telefon gespeichert');
    await page.waitFor(`!!document.querySelector('#rcp-${ids.lena}')`, 5000);
    assert(await pillOf(page, ids.lena) === 'Laut', 'Lena in der Liste: Pille Laut');
    let mark = barkLog(sandbox).length;
    await page.click('.rcp-add-test');
    await waitConfirm(page);
    assert(await text(page, '.confirm-text') === testPushText('Lena'), 'Test-Push-Frage: ' + await text(page, '.confirm-text'));
    await page.click('.confirm .btn-danger');
    await page.waitFor(`(document.querySelector('.rcp-msg-addtest')?.textContent || '').includes('gesendet')`, 15000);
    await sleep(500);
    const pushed = barkLog(sandbox).slice(mark);
    assert(pushed.length === 1 && pushed[0].key === LENA_KEY && pushed[0].fields.level === 'active',
      'genau ein Test-Push level=active an Lena: ' + JSON.stringify(pushed));
    await page.click('.rcp-add-done');
    await page.waitFor(`!document.querySelector('.rcp-add-form') && !document.querySelector('.rcp-add-test')`);
    // Die Box hat die neue Liste noch nicht: erst „übernimmt …“, nach dem Push grün.
    await page.waitFor(`!!document.querySelector('.rcp-sync.is-pending')`, 8000);
    await pushBox(base, admin);
    await page.waitFor(`!!document.querySelector('.rcp-sync.is-ok')`, 8000);

    // ---- Person: Laut / Stumm / Dauerhaft, Regler ---------------------------
    await page.click(`#rcp-${ids.lena} a`);
    await page.waitFor(`!!document.querySelector('.rcp-person[data-id="${ids.lena}"]')`, 5000);
    await settle(page);
    assert(await page.eval(`location.hash`) === `#/empfaenger/${ids.lena}`, 'Adresse #/empfaenger/<id>');
    assert(await text(page, '.rcp-detail .sheet-title') === 'Lena', 'Sheet-Titel: ' + await text(page, '.rcp-detail .sheet-title'));
    assert(await page.eval(`document.querySelector('.rcp-phone-input').value`) === '+49 171 2345678', 'Telefon im Sheet');
    assert(await page.eval(`document.querySelector('.rcp-call')?.getAttribute('href')`) === 'tel:+491712345678', 'Anrufen-Link');
    assert((await text(page, '.rcp-person .rcp-vol')).includes('gilt, wenn stumm'), 'laut: Hinweis „gilt, wenn stumm“');
    await shot(page, 'person');

    await clickSeg(page, 'Stumm');
    await waitConfirm(page);
    const muteQ = await text(page, '.confirm-text');
    assert(muteQ.includes('OHNE Ton') && muteQ.includes('"Lena"'), 'Stumm-Frage: ' + muteQ);
    await shot(page, 'confirm-mute');
    await page.click('.confirm .btn-danger');
    await page.waitFor(`(document.querySelector('#rcp-${ids.lena} .rcp-state-pill')?.textContent || '').trim() === 'Stumm'`, 8000);
    await page.waitFor(`(document.querySelector('.rcp-person .rcp-state')?.textContent || '').includes('wird um')`, 5000);
    assert(!(await text(page, '.rcp-person .rcp-vol')).includes('gilt, wenn stumm'), 'stumm: kein Hinweis „gilt, wenn stumm“');

    // Regler per Tastatur: 4 × Pfeil rechts = EINE Speicherung (nach 0,8 s).
    await page.eval(`document.querySelector('.rcp-person .slider-input').focus()`);
    for (let i = 0; i < 4; i++) await keyPress(page, 'ArrowRight');
    assert(await text(page, '.rcp-person .slider-value') === '4', 'Regler zeigt 4');
    await until(async () => (await entry(ids.lena)).mute_volume === 4, 6000, 'mute_volume 4 gespeichert');
    await page.waitFor(`(document.querySelector('.rcp-person .rcp-state')?.textContent || '').includes('Lautstärke 4')`, 8000);

    await clickSeg(page, 'Dauerhaft stumm');
    await waitConfirm(page);
    const permQ = await text(page, '.confirm-text');
    assert(permQ.includes('DAUERHAFT') && permQ.includes('Stumm-Lautstärke 4 von 10'), 'Dauerhaft-Frage nennt die echte Lautstärke: ' + permQ);
    await page.click('.confirm .btn-danger');
    await page.waitFor(`(document.querySelector('#rcp-${ids.lena} .rcp-state-pill')?.textContent || '').trim() === 'Dauerhaft'`, 8000);
    // Dauerhaft -> Stumm ist die sichere Richtung: ohne Rückfrage.
    await clickSeg(page, 'Stumm');
    await sleep(300);
    assert(await page.eval(`!document.querySelector('.confirm')`), 'Dauerhaft → Stumm ohne Rückfrage');
    await page.waitFor(`(document.querySelector('#rcp-${ids.lena} .rcp-state-pill')?.textContent || '').trim() === 'Stumm'`, 8000);
    await clickSeg(page, 'Laut');
    await sleep(300);
    assert(await page.eval(`!document.querySelector('.confirm')`), 'Laut ohne Rückfrage');
    await page.waitFor(`(document.querySelector('#rcp-${ids.lena} .rcp-state-pill')?.textContent || '').trim() === 'Laut'`, 8000);
    assert((await entry(ids.lena)).muted === false, 'Lena laut gespeichert');

    // ---- Live-Aktualisierung mitten in der Bedienung ------------------------
    // Umbenennen-Feld offen mit halb getipptem Namen, Regler mit dem Finger
    // gezogen (nicht losgelassen), Anna ändert sich auf einem anderen Gerät,
    // dann alle Abfragen plus zwei Status-Takte: nichts davon darf das Sheet
    // schließen, den Regler zurücksetzen, das Feld leeren oder den Fokus nehmen.
    await page.click('.rcp-rename button');
    await page.waitFor(`document.activeElement?.classList.contains('rcp-rename-input')`, 3000);
    await typeInto(page, '.rcp-rename-input', 'Le');
    const box = await page.eval(`(() => { const r = document.querySelector('.rcp-person .slider-input').getBoundingClientRect();
      return { left: r.left, width: r.width, y: r.top + r.height / 2 }; })()`);
    const xAt = (v) => box.left + 14 + (box.width - 28) * (v / 10);
    await page.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: xAt(4), y: box.y }] });
    for (const v of [5, 6, 7, 8]) {
      await page.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x: xAt(v), y: box.y }] });
      await sleep(60);
    }
    await sleep(200);
    const dragged = Number(await page.eval(`document.querySelector('.rcp-person .slider-input').value`));
    assert(dragged > 4, 'Regler gezogen: ' + dragged);
    assert(await text(page, '.rcp-person .slider-value') === String(dragged), 'Anzeige folgt dem Finger: ' + await text(page, '.rcp-person .slider-value'));
    assert(await page.eval(`document.activeElement?.classList.contains('rcp-rename-input')`), 'Fokus bleibt im Namensfeld beim Ziehen');
    assert((await other.post({ action: 'keys_mute', id: String(ids.anna), muted: '1' })).ok, 'Anna stumm (anderes Gerät)');
    await page.eval('FW.store.refreshAll()');
    await sleep(11000);
    assert(await pillOf(page, ids.anna) === 'Stumm', 'Aktualisierung angekommen: Anna stumm');
    assert(await page.eval(`!!document.querySelector('.rcp-person[data-id="${ids.lena}"]')`), 'Sheet bleibt offen');
    assert(await page.eval(`document.activeElement?.classList.contains('rcp-rename-input')`), 'Fokus bleibt im Namensfeld');
    assert(await page.eval(`document.querySelector('.rcp-rename-input').value`) === 'Le', 'halb getippter Name bleibt');
    assert(Number(await page.eval(`document.querySelector('.rcp-person .slider-input').value`)) === dragged, 'Regler bleibt beim gezogenen Wert');
    assert(await text(page, '.rcp-person .slider-value') === String(dragged), 'Anzeige bleibt beim gezogenen Wert');
    assert((await entry(ids.lena)).mute_volume === 4, 'während des Ziehens nichts gespeichert');
    await shot(page, 'live-refresh');
    await page.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
    await until(async () => (await entry(ids.lena)).mute_volume === dragged, 6000, `Loslassen speichert ${dragged}`);

    // ---- Umbenennen, Key ersetzen -------------------------------------------
    await typeInto(page, '.rcp-rename-input', 'Lena Berger');
    await keyPress(page, 'Enter');
    await page.waitFor(`(document.querySelector('#rcp-${ids.lena} .row-label')?.textContent || '').trim() === 'Lena Berger'`, 8000);
    assert(await text(page, '.rcp-detail .sheet-title') === 'Lena Berger', 'Sheet-Titel mit neuem Namen');
    assert(await page.eval(`!document.querySelector('.rcp-rename-input')`), 'Feld nach dem Speichern zu');
    assert(await page.eval(`!!document.activeElement?.closest('.rcp-rename')`), 'Fokus zurück auf „Umbenennen“');

    const v0 = (await list()).version;
    const masked0 = (await entry(ids.lena)).key_masked;
    await page.click('.rcp-replace button');
    await typeInto(page, '.rcp-replace-input', 'rcpr' + u);
    await page.click('.rcp-replace-go');
    await waitConfirm(page);
    assert((await text(page, '.confirm-text')).includes('Der alte Key bekommt danach keine Alarme mehr.'), 'Ersetzen-Frage: ' + await text(page, '.confirm-text'));
    await page.click('.confirm .btn-danger');
    await page.waitFor(`(document.querySelector('.rcp-msg-replace')?.textContent || '').includes('Key ersetzt')`, 8000);
    await until(async () => (await list()).version > v0, 5000, 'keys_version erhöht');
    assert((await entry(ids.lena)).key_masked !== masked0, 'maskierter Key geändert');

    // ---- Kurzbefehl-Links ---------------------------------------------------
    // Headless-Chrome verweigert die Zwischenablage meist; hier fest ersetzt,
    // damit beide Wege (verweigert / kopiert) sicher drankommen.
    await page.eval(`Object.defineProperty(navigator, 'clipboard', { configurable: true,
      value: { writeText: () => Promise.reject(new DOMException('verweigert', 'NotAllowedError')) } })`);
    await page.click('.rcp-link-on button');
    await page.waitFor(`(document.querySelector('.rcp-msg-link')?.textContent || '').includes('api/mute.php?t=')`, 3000);
    const fallback = await text(page, '.rcp-msg-link');
    assert(fallback.includes('Kopieren nicht möglich') && fallback.includes('state=on')
      && fallback.includes(`${base}/api/mute.php?t=`), 'Ersatz-Anzeige mit Link: ' + fallback);
    await page.eval(`Object.defineProperty(navigator, 'clipboard', { configurable: true,
      value: { writeText: (t) => { window.__copied = t; return Promise.resolve(); } } })`);
    await page.click('.rcp-link-off button');
    await page.waitFor(`(document.querySelector('.rcp-msg-link')?.textContent || '').includes('kopiert')`, 3000);
    const copied = await page.eval('window.__copied');
    assert(copied.startsWith(`${base}/api/mute.php?t=`) && copied.includes('state=off'), 'kopierter Laut-Link: ' + copied);

    // ---- Telefon ändern (Enter speichert) ------------------------------------
    await typeInto(page, '.rcp-phone-input', '0171 999');
    await keyPress(page, 'Enter');
    await until(async () => (await entry(ids.lena)).phone === '0171 999', 6000, 'Telefon geändert');
    await page.waitFor(`document.querySelector('.rcp-call')?.getAttribute('href') === 'tel:0171999'`, 8000);
    await shot(page, 'person-bottom');

    // ---- Löschen; der letzte lässt sich nicht löschen -----------------------
    await page.click('.rcp-delete button');
    await waitConfirm(page);
    assert((await text(page, '.confirm-text')).includes('wirklich löschen'), 'Lösch-Frage');
    await page.click('.confirm .btn-danger');
    await page.waitFor(`!document.querySelector('.rcp-person') && !document.querySelector('#rcp-${ids.lena}')`, 8000);
    assert(await page.eval('location.hash') === '#/empfaenger', 'zurück zur Liste');
    assert((await text(page, '.rcp-msg-list')).includes('gelöscht'), 'Rückmeldung unter der Liste: ' + await text(page, '.rcp-msg-list'));
    delete ids.lena;

    await page.click(`#rcp-${ids.anna} a`);
    await page.waitFor(`!!document.querySelector('.rcp-person[data-id="${ids.anna}"]')`, 5000);
    await settle(page);
    await page.click('.rcp-delete button');
    await waitConfirm(page);
    await page.click('.confirm .btn-danger');
    await page.waitFor(`(document.querySelector('.rcp-msg-delete')?.textContent || '').includes('letzte Empfänger')`, 8000);
    assert(await page.eval(`!!document.querySelector('.rcp-person[data-id="${ids.anna}"] .msg-error')`), 'Fehler steht im Sheet');
    await shot(page, 'last-delete');
    await page.click('.rcp-detail .sheet-close');
    await page.waitFor(`!document.querySelector('.rcp-person') && location.hash === '#/empfaenger'`, 3000);

    // ---- „Mehr erfahren“ -----------------------------------------------------
    const muteMax = Number((await list()).mute_max_seconds) || 0;
    await page.click('.rcp-more');
    await page.waitFor(`!!document.querySelector('.rcp-explain')`, 3000);
    const explain = (await text(page, '.rcp-explain')).replace(/\u00a0/g, ' ');
    assert(explain.includes('Dauerhaft stumm') && explain.includes('Kurzbefehl-Links (für iPhone-Kurzbefehle'), 'Erklärung: ' + explain);
    if (muteMax > 0 && muteMax % 3600 === 0) assert(explain.includes(`Nach ${muteMax / 3600} Std`), 'Dauer aus der Einstellung: ' + explain);
    await settle(page);
    await shot(page, 'explain');
    await page.click('.rcp-explain-sheet .sheet-close');
    await page.waitFor(`!document.querySelector('.rcp-explain')`, 3000);

    // ---- Zehn Empfänger mit 40 Zeichen auf 320 × 568 ------------------------
    const count = (await list()).keys.length;
    const ten = [];
    for (let i = count; i < 10; i++) {
      const id = await add(longName(i), `rcp${i}x${u}`, i === 3 ? '+49 160 5550001' : '');
      ten.push(id);
      if (i % 3 === 1) {
        assert((await admin.post({ action: 'keys_mute', id: String(id), muted: '1' })).ok, 'stumm');
        assert((await admin.post({ action: 'keys_set_mute_volume', id: String(id), volume: '3' })).ok, 'Lautstärke 3');
      } else if (i % 3 === 2) {
        assert((await admin.post({ action: 'keys_set_mute_permanent', id: String(id), permanent: '1' })).ok, 'dauerhaft');
      }
    }
    ids.phone = ten[3 - count];
    const demoId = ten.find((id, k) => (count + k) % 3 === 2);
    assert((await list()).keys.length === 10, 'genau 10 Empfänger');
    assert((await admin.post({ action: 'demo_set', enabled: '1', id: String(demoId) })).ok, 'Demo ein');
    await pushBox(base, admin);
    await page.viewport({ width: 320, height: 568, mobile: true, scale: 2 });
    await page.eval('FW.store.refreshAll()');
    await page.waitFor(`document.querySelectorAll('.rcp-list .rcp-row').length === 10 && !!document.querySelector('#rcp-${demoId} .rcp-demo-pill')`, 10000);
    await page.eval('scrollTo(0, 0)');
    const fit = JSON.parse(await page.eval(`JSON.stringify({
      sw: document.documentElement.scrollWidth, w: innerWidth,
      pills: Array.from(document.querySelectorAll('.rcp-list .pill')).map((p) => { const r = p.getBoundingClientRect();
        return { right: Math.round(r.right), width: Math.round(r.width) }; }),
      paused: document.querySelectorAll('.rcp-row.is-paused').length })`));
    assert(fit.sw <= fit.w, `320 px: kein waagrechtes Scrollen (${fit.sw} > ${fit.w})`);
    assert(fit.pills.length >= 11 && fit.pills.every((p) => p.width > 0 && p.right <= fit.w), 'alle Pillen sichtbar: ' + JSON.stringify(fit.pills));
    assert(fit.paused === 9, 'Demo: neun pausiert, gefunden ' + fit.paused);
    const demoNote = await text(page, '.rcp-demo-note');
    assert(demoNote.includes(`nur ${longName(2)} – alle anderen pausieren`), 'Demo-Hinweis: ' + demoNote);
    await shot(page, 'ten-small');
    await page.eval('scrollTo(0, document.documentElement.scrollHeight)');
    await sleep(150);
    await shot(page, 'ten-small-bottom');
    // Die Liste ist voll: „+“ erklärt das, statt ein Formular zu zeigen.
    await page.eval('scrollTo(0, 0)');
    await page.click('.rcp-add');
    await page.waitFor(`(document.querySelector('.rcp-add-sheet')?.textContent || '').includes('höchstens 10')`, 3000);
    assert(await page.eval(`!document.querySelector('.rcp-add-form')`), 'volle Liste: kein Formular');
    await page.click('.rcp-add-sheet .sheet-close');
    await page.waitFor(`!document.querySelector('.rcp-add-sheet')`, 3000);
    // Person mit langem Namen im Sheet auf 320 px.
    await page.click(`#rcp-${demoId} a`);
    await page.waitFor(`!!document.querySelector('.rcp-person[data-id="${demoId}"]')`, 5000);
    await settle(page);
    assert(await page.eval(`document.documentElement.scrollWidth <= innerWidth
      && document.querySelector('.rcp-detail .sheet').scrollWidth <= document.querySelector('.rcp-detail .sheet').clientWidth`), '320 px: Sheet ohne waagrechtes Scrollen');
    await shot(page, 'ten-small-person');
    await page.click('.rcp-detail .sheet-close');
    await page.waitFor(`!document.querySelector('.rcp-person')`, 3000);
    assert((await admin.post({ action: 'demo_set', enabled: '0' })).ok, 'Demo aus');

    // ---- PC: Liste links, Panel rechts --------------------------------------
    await page.viewport({ width: 1280, height: 800, mobile: false, scale: 1 });
    await page.waitFor(`!!document.querySelector('.sidebar') && !!document.querySelector('.rcp-split .rcp-panel') && !document.querySelector('.demo-band')`, 10000);
    assert(await page.eval(`!!document.querySelector('.rcp-add')?.textContent.includes('Hinzufügen')`), 'PC: Knopf „+ Hinzufügen“');
    const [first, second] = ten;
    await page.click(`#rcp-${first} a`);
    await page.waitFor(`!!document.querySelector('.rcp-panel .rcp-person[data-id="${first}"]')`, 5000);
    await page.eval(`window.__panel = document.querySelector('.rcp-panel')`);
    await page.click(`#rcp-${second} a`);
    await page.waitFor(`!!document.querySelector('.rcp-panel .rcp-person[data-id="${second}"]')`, 5000);
    assert(await page.eval(`window.__panel === document.querySelector('.rcp-panel') && window.__panel.isConnected`), 'Panel bleibt stehen (wird nicht geschlossen)');
    assert(await page.eval(`!document.querySelector('.sheet-layer')`), 'PC: kein Sheet');
    assert(await page.eval(`(() => { const l = document.querySelector('.rcp-list').getBoundingClientRect();
      const p = document.querySelector('.rcp-panel').getBoundingClientRect(); return p.left >= l.right; })()`), 'Panel rechts neben der Liste');
    assert(await page.eval(`document.querySelector('#rcp-${second} a').getAttribute('aria-current') === 'page'`), 'gewählte Zeile markiert');
    assert((await text(page, '.rcp-panel .panel-title')) === longName(count + 1), 'Panel-Titel = zweite Person');
    assert(await page.eval(`document.documentElement.scrollWidth <= innerWidth`), 'PC: kein waagrechtes Scrollen');
    await shot(page, 'wide');
    await page.eval(`(() => { document.querySelector('.rcp-panel').scrollTop = 10000; scrollTo(0, document.documentElement.scrollHeight); })()`);
    await sleep(150);
    await shot(page, 'wide-panel-bottom');
    await page.eval(`(() => { document.querySelector('.rcp-panel').scrollTop = 0; scrollTo(0, 0); })()`);
    // Box hat die Liste seit > 60 s nicht übernommen -> orange. Statt eine
    // Minute zu warten, beginnt die Messung „vor 61 s“: Abfragen anhalten,
    // die erste Status-Antwort mit zurückgestellter Uhr übernehmen, die
    // zweite mit der echten (die Messung läuft weiter, der Stand ist frisch).
    await page.eval('FW.store.stop()');
    await pushBox(base, admin, { keys_version: '1' });
    await page.eval(`(async () => { const real = Date.now; Date.now = () => real() - 61000;
      try { await FW.store.refreshStatus(); } finally { Date.now = real; }
      await FW.store.refreshStatus(); })()`);
    await page.waitFor(`!!document.querySelector('.rcp-sync.is-warn')`, 3000);
    const warnSync = await text(page, '.rcp-sync');
    await page.eval('FW.store.start()');
    assert(warnSync.includes('Box hat die Änderung noch nicht übernommen (seit 1 Min)'), 'Abgleich orange: ' + warnSync);
    await pushBox(base, admin);
    await page.waitFor(`!!document.querySelector('.rcp-sync.is-ok')`, 8000);

    // ---- Lese-Rolle ----------------------------------------------------------
    await page.send('Network.clearBrowserCookies');
    await page.viewport({ width: 390, height: 844, mobile: true, scale: 2 });
    await page.login(base, 'leser', 'test');
    await page.goto(`${base}/next.php#/empfaenger`);
    await page.waitFor(`document.querySelectorAll('.rcp-list .rcp-row').length === 10`, 10000);
    const noControls = `!document.querySelector('.rcp-add') && !document.querySelector('.segmented') && !document.querySelector('.slider-input')
      && !document.querySelector('.rcp-phone-input') && !document.querySelector('.rcp-link-on') && !document.querySelector('.rcp-test')
      && !document.querySelector('.rcp-rename') && !document.querySelector('.rcp-replace') && !document.querySelector('.rcp-delete')
      && !document.documentElement.outerHTML.includes('5550001') && !document.documentElement.outerHTML.includes('Telefon')`;
    assert(await page.eval(noControls), 'Lese-Rolle: keine Bedienelemente, kein Telefon (Liste)');
    await page.click(`#rcp-${ids.phone} a`);
    await page.waitFor(`!!document.querySelector('.rcp-person[data-id="${ids.phone}"]')`, 5000);
    await settle(page);
    assert(await page.eval(noControls), 'Lese-Rolle: Person nur lesend, ohne Telefon/Links');
    assert((await text(page, '.rcp-person')).includes('Stumm-Lautstärke'), 'Lese-Rolle sieht die Stumm-Lautstärke');
    await shot(page, 'readonly');

    assert(problems.length === 0, 'keine Konsolenfehler:\n' + problems.join('\n'));
  } finally {
    // Aufräumen: Demo aus, eigene Empfänger bis auf einen weg (die Liste
    // fasst höchstens 10, und den letzten lässt das NAS nicht löschen), alte
    // zurück, dann den Rest der eigenen. Fehlschläge werden gemeldet, ändern
    // aber das Ergebnis des Laufs nicht.
    const note = (what, r) => { if (!r || !r.ok) console.error(`Aufräumen: ${what} fehlgeschlagen: ${JSON.stringify(r)}`); };
    const ownLeft = () => (readData(sandbox, 'alarm_keys') || { keys: [] }).keys.filter(own);
    try {
      note('Demo aus', await admin.post({ action: 'demo_set', enabled: '0' }));
      for (const k of ownLeft().slice(1)) note(`Empfänger ${k.label} löschen`, await admin.post({ action: 'keys_delete', id: String(k.id) }));
      const now = readData(sandbox, 'alarm_keys');
      const present = new Set((now && Array.isArray(now.keys) ? now.keys : []).map((k) => k.key));
      for (const k of before) {
        if (present.has(k.key)) continue;
        const fields = { action: 'keys_add', label: k.label, key: k.key };
        if (k.phone) fields.phone = k.phone;
        note(`${k.label} zurück`, await admin.post(fields));
      }
      for (const k of ownLeft()) {
        const r = await admin.post({ action: 'keys_delete', id: String(k.id) });
        if (before.length) note(`Empfänger ${k.label} löschen`, r);
      }
      await pushBox(base, admin);
    } catch (err) {
      console.error('Aufräumen unvollständig:', err.message);
    }
  }
}
