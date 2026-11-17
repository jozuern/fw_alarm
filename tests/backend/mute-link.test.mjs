// Kurzbefehl-Link (api/mute.php): Jeder Empfänger schaltet mit seinem
// persönlichen Link den eigenen Eintrag stumm (state=on) oder wieder laut
// (state=off). Der Link ist öffentlich erreichbar - deshalb die Bremse gegen
// Durchprobieren, Link-Vorschauen, die nichts umschalten, und für Browser eine
// Seite ohne Token und ohne Key. Jede Regel hier muss auch eine neue
// Server-Version erfüllen: Die verteilten Kurzbefehle bleiben dieselben.
import assert from 'node:assert/strict';
import { after, before, beforeEach, describe, it } from 'node:test';
import {
  STATUS_KEY, box, now, postForm, request, session, settle, startBackend, waitFor,
} from './harness.mjs';

const ANNA_KEY = 'annakey00001';
const BERT_KEY = 'bertkey00001';
const MUTE_MAX = 43200;   // Standard von mute_max_seconds: 12 Stunden
// Die Klartext-Antworten (ASCII, so lesen sie die Kurzbefehle der Kollegen).
const TEXT = {
  on: 'ok=1\nmuted=1\n',
  off: 'ok=1\nmuted=0\n',
  noState: 'ok=0\nerror=state fehlt (on oder off)\n',
  unknown: 'ok=0\nerror=unbekannter Empfaenger\n',
  tooMany: 'ok=0\nerror=zu viele Fehlversuche - spaeter erneut\n',
  preview: 'ok=0\nerror=Vorschau - nichts geaendert\n',
  storage: 'ok=0\nerror=speichern fehlgeschlagen\n',
};
// So meldet sich ein Browser, der eine Seite öffnet.
const BROWSER = { accept: 'text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8' };

let backend;
let admin;
let anna;
before(async () => { backend = await startBackend(); });
after(async () => { await backend.stop(); });
beforeEach(async () => {
  await backend.reset();
  admin = await session(backend);
  anna = await admin.addKey('Anna', ANNA_KEY);
});

const endpoint = () => `${backend.base}/api/mute.php`;
const muteUrl = (query) => `${endpoint()}?${new URLSearchParams(query)}`;
// Annas Link (bzw. derselbe Link mit einem anderen Token).
const link = (state, token = anna.mute_token) => muteUrl({ t: token, state });
const call = (state, headers = {}) => request(link(state), { headers });
const entryOf = async (id = anna.id) => (await admin.keys()).keys.find((k) => k.id === id);
const version = async () => (await admin.keys()).version;
const keyEvents = async (...events) => (await admin.log('keys')).filter((e) => events.includes(e.event));
// Gezählte Fehlversuche aller Absender (0 = die Bremse hat nichts gezählt).
const countedFailures = () => Object.values(backend.data.read('mute_throttle.json') ?? {})
  .reduce((sum, who) => sum + Number(who?.count ?? 0), 0);
// Die Parameter der Umleitung (Location ist relativ zur aufgerufenen Adresse).
const redirectOf = (r, from) => new URL(r.headers.get('location'), from);
// "16.11. um 14:05 Uhr" in der Zeitzone des Servers (config.php: Europe/Berlin).
function stamp(seconds) {
  const p = Object.fromEntries(new Intl.DateTimeFormat('de-DE', {
    timeZone: 'Europe/Berlin', day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit', hourCycle: 'h23',
  }).formatToParts(new Date(seconds * 1000)).map((part) => [part.type, part.value]));
  return `${p.day}.${p.month}. um ${p.hour}:${p.minute} Uhr`;
}
// Eine Seite für Menschen: HTML, nicht zwischenspeicherbar, ohne eingebettete
// Styles und Skripte (die CSP verbietet sie) - und ohne Token und Bark-Key.
function assertPage(r) {
  assert.match(r.headers.get('content-type'), /^text\/html/);
  assert.match(r.headers.get('cache-control'), /no-store/);
  const csp = r.headers.get('content-security-policy');
  assert.match(csp, /style-src 'self'/);
  assert.match(csp, /script-src 'self'/);
  assert.doesNotMatch(csp, /unsafe-inline/);
  assert.match(r.text, /^<!doctype html>/i);
  assert.doesNotMatch(r.text, /<style/i, 'kein <style>-Block');
  assert.doesNotMatch(r.text, /\sstyle\s*=/i, 'kein style=-Attribut');
  for (const script of r.text.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script>/gi)) {
    assert.match(script[1], /\ssrc="[^"]+"/, 'Skripte nur per Adresse');
    assert.equal(script[2].trim(), '', 'kein eingebettetes Skript');
  }
  assert.equal(r.text.includes(anna.mute_token), false, 'Token steht nicht in der Seite');
  assert.equal(r.text.includes(ANNA_KEY), false, 'Bark-Key steht nicht in der Seite');
}

describe('Kurzbefehl oder Skript (kein Browser)', () => {
  it('state=on schaltet stumm: "ok=1 / muted=1" als Klartext, neue Listen-Version, Eintrag im Verlauf', async () => {
    const before = await version();
    const r = await call('on');
    assert.equal(r.status, 200);
    assert.equal(r.text, TEXT.on);
    assert.match(r.headers.get('content-type'), /^text\/plain/);
    assert.match(r.headers.get('cache-control'), /no-store/);

    const list = await admin.keys();
    const entry = list.keys.find((k) => k.id === anna.id);
    assert.equal(entry.muted, true);
    assert.equal(entry.mute_overdue, false);
    assert.ok(Math.abs(entry.muted_at - now()) <= 5, 'muted_at = Zeitpunkt des Aufrufs');
    assert.equal(list.version, before + 1);

    const events = await keyEvents('key_muted', 'key_unmuted');
    assert.equal(events.length, 1);
    assert.equal(events[0].event, 'key_muted');
    assert.equal(events[0].source, 'shortcut');
    assert.equal(events[0].label, 'Anna');

    // Die Box bekommt den Empfänger unter der neuen Version als stumm.
    const forBox = (await box(backend).keys()).kv;
    assert.equal(forBox.version, String(before + 1));
    assert.equal(forBox.key0, ANNA_KEY);
    assert.equal(forBox.muted0, '1');
  });

  it('derselbe Aufruf noch einmal: gleiche Antwort, keine neue Version, kein zweiter Eintrag', async () => {
    await call('on');
    // Die bestehende Stummschaltung läuft schon 10 Minuten.
    const since = now() - 600;
    backend.data.edit('alarm_keys.json', (list) => { list.keys.find((k) => k.id === anna.id).muted_at = since; });
    const before = await version();

    const again = await call('on');
    assert.equal(again.status, 200);
    assert.equal(again.text, TEXT.on);
    const list = await admin.keys();
    assert.equal(list.version, before);
    const entry = list.keys.find((k) => k.id === anna.id);
    assert.equal(entry.muted, true);
    assert.equal(entry.muted_at, since, 'der Beginn der Stummschaltung bleibt');
    assert.equal((await keyEvents('key_muted', 'key_unmuted')).length, 1);
  });

  it('state=off schaltet wieder laut: "ok=1 / muted=0", neue Version, key_unmuted im Verlauf', async () => {
    await call('on');
    const before = await version();
    const r = await call('off');
    assert.equal(r.status, 200);
    assert.equal(r.text, TEXT.off);
    assert.match(r.headers.get('cache-control'), /no-store/);

    const list = await admin.keys();
    const entry = list.keys.find((k) => k.id === anna.id);
    assert.equal(entry.muted, false);
    assert.equal('mute_until' in entry, false);
    assert.equal(list.version, before + 1);
    const events = await keyEvents('key_muted', 'key_unmuted');
    assert.deepEqual(events.map((e) => e.event), ['key_unmuted', 'key_muted']);
    assert.equal(events[0].source, 'shortcut');
    assert.equal((await box(backend).keys()).kv.muted0, '0');

    // Auch "laut" ist idempotent.
    const again = await call('off');
    assert.equal(again.text, TEXT.off);
    assert.equal(await version(), before + 1);
    assert.equal((await keyEvents('key_muted', 'key_unmuted')).length, 2);
  });

  it('POST mit t und state im Rumpf wirkt genauso', async () => {
    const before = await version();
    const on = await postForm(endpoint(), { t: anna.mute_token, state: 'on' });
    assert.equal(on.status, 200);
    assert.equal(on.text, TEXT.on);
    assert.equal((await entryOf()).muted, true);
    const off = await postForm(endpoint(), { t: anna.mute_token, state: 'off' });
    assert.equal(off.status, 200);
    assert.equal(off.text, TEXT.off);
    assert.equal((await entryOf()).muted, false);
    assert.equal(await version(), before + 2);
  });

  it('die alte Link-Form key=<Bark-Key> gilt weiter', async () => {
    const on = await request(muteUrl({ key: ANNA_KEY, state: 'on' }));
    assert.equal(on.status, 200);
    assert.equal(on.text, TEXT.on);
    assert.equal((await entryOf()).muted, true);
    const off = await request(muteUrl({ key: ANNA_KEY, state: 'off' }));
    assert.equal(off.text, TEXT.off);
    assert.equal((await entryOf()).muted, false);
    assert.deepEqual((await keyEvents('key_muted', 'key_unmuted')).map((e) => e.source), ['shortcut', 'shortcut']);
  });

  it('fehlt state oder ist es unbrauchbar: 400, nichts geändert', async () => {
    const before = await version();
    const tries = [
      await request(muteUrl({ t: anna.mute_token })),
      await request(muteUrl({ t: anna.mute_token, state: '' })),
      await request(muteUrl({ t: anna.mute_token, state: 'vielleicht' })),
      await request(muteUrl({ t: anna.mute_token, state: '1' })),
      await request(muteUrl({ key: ANNA_KEY })),
      await postForm(endpoint(), { t: anna.mute_token }),
    ];
    for (const r of tries) {
      assert.equal(r.status, 400);
      assert.equal(r.text, TEXT.noState);
      assert.match(r.headers.get('cache-control'), /no-store/);
    }
    assert.equal(await version(), before);
    assert.equal((await entryOf()).muted, false);
    assert.deepEqual(await keyEvents('key_muted', 'key_unmuted'), []);
  });

  it('unbekannter und falsch geformter Token: dieselbe 404-Antwort - sie verrät nicht, was es gibt', async () => {
    const before = await version();
    const tries = [
      await request(link('on', 'gibtesnicht12')),                      // gut geformt, aber unbekannt
      await request(link('on', 'kurz')),                               // zu kurz
      await request(link('on', 'mit leerzeichen!')),                   // unerlaubte Zeichen
      await request(link('on', 'x'.repeat(40))),                       // zu lang
      await request(muteUrl({ state: 'on' })),                         // weder Token noch Key
      await request(muteUrl({ key: 'fremderkey001', state: 'on' })),   // alte Form, unbekannter Key
      await request(muteUrl({ t: ANNA_KEY, state: 'on' })),            // der Bark-Key ist kein Token
      await request(muteUrl({ key: anna.mute_token, state: 'on' })),   // der Token ist kein Bark-Key
      await postForm(endpoint(), { t: 'gibtesnicht12', state: 'on' }),
    ];
    for (const r of tries) {
      assert.equal(r.status, 404);
      assert.equal(r.text, TEXT.unknown);
      assert.match(r.headers.get('content-type'), /^text\/plain/);
    }
    assert.equal(await version(), before);
    assert.equal((await entryOf()).muted, false);
    assert.deepEqual(await keyEvents('key_muted', 'key_unmuted'), []);
  });

  it('beim Umschalten von Hand geht keine Bestätigung an den Empfänger', async () => {
    const mark = backend.bark.mark();
    assert.equal((await call('on')).text, TEXT.on);
    assert.equal((await call('off')).text, TEXT.off);
    await settle();
    assert.deepEqual(backend.bark.since(mark), []);
  });

  it('die Meldung an den Admin (mute_changed) gibt es nur, wenn sie eingeschaltet ist', async () => {
    let mark = backend.bark.mark();
    assert.equal((await call('on')).text, TEXT.on);
    await settle();
    assert.deepEqual(backend.bark.since(mark), [], 'ab Werk aus');

    assert.equal((await admin.post({ action: 'notify_set', type: 'mute_changed', key: 'enabled', value: '1' })).ok, true);
    mark = backend.bark.mark();
    assert.equal((await call('off')).text, TEXT.off);
    const loud = await waitFor(() => backend.bark.to(STATUS_KEY, mark)[0], 'Meldung "wieder LAUT"');
    assert.equal(loud.fields.title, 'Arbeitsmodus');
    assert.match(loud.fields.body, /Anna hat sich per Kurzbefehl wieder LAUT geschaltet\./);

    mark = backend.bark.mark();
    assert.equal((await call('on')).text, TEXT.on);
    const muted = await waitFor(() => backend.bark.to(STATUS_KEY, mark)[0], 'Meldung "STUMM"');
    assert.equal(muted.fields.title, 'Arbeitsmodus');
    assert.match(muted.fields.body, /Anna/);
    assert.match(muted.fields.body, /STUMM geschaltet/);
    assert.equal(muted.fields.level, 'passive');

    // Ein Aufruf, der nichts ändert, meldet auch nichts.
    mark = backend.bark.mark();
    assert.equal((await call('on')).text, TEXT.on);
    await settle();
    assert.deepEqual(backend.bark.since(mark), []);
    // Der Betroffene selbst bekommt nie etwas.
    assert.deepEqual(backend.bark.to(ANNA_KEY), []);
  });

  it('im Verlauf steht nie der Token, und der Bark-Key nur maskiert', async () => {
    await call('on');
    await call('off');
    await request(muteUrl({ key: ANNA_KEY, state: 'on' }));
    await request(link('off', 'gibtesnicht12'));
    const stored = JSON.stringify(backend.data.log());
    assert.equal(stored.includes(anna.mute_token), false);
    assert.equal(stored.includes(ANNA_KEY), false);
    assert.ok(stored.includes('anna...'), 'Key maskiert auf die ersten vier Zeichen');
    const shown = JSON.stringify(await admin.log('all'));
    assert.equal(shown.includes(anna.mute_token), false);
    assert.equal(shown.includes(ANNA_KEY), false);
  });
});

describe('Bremse gegen Durchprobieren', () => {
  it('20 Fehlversuche bekommen 404, der 21. bekommt 429 - ein gültiger Link geht trotzdem', async () => {
    for (let i = 0; i < 20; i++) {
      // Abwechselnd unbekannt und falsch geformt: beides zählt.
      const r = await request(link('on', i % 2 ? 'kurz' : `geraten${String(i).padStart(5, '0')}`));
      assert.equal(r.status, 404, `Fehlversuch ${i + 1}`);
      assert.equal(r.text, TEXT.unknown);
    }
    const blocked = await request(link('on', 'geraten99999'));
    assert.equal(blocked.status, 429);
    assert.equal(blocked.text, TEXT.tooMany);
    assert.equal((await request(link('off', 'kurz'))).status, 429);
    const page = await request(link('on', 'geraten99999'), { headers: BROWSER });
    assert.equal(page.status, 429);
    assertPage(page);
    assert.match(page.text, /Zu viele Fehlversuche/);

    // Der gesperrte Absender kann mit einem GÜLTIGEN Link weiter schalten -
    // sonst sperrte jeder hinter derselben Adresse die Links aller Kollegen.
    const on = await call('on');
    assert.equal(on.status, 200);
    assert.equal(on.text, TEXT.on);
    assert.equal((await entryOf()).muted, true);
    const off = await call('off');
    assert.equal(off.status, 200);
    assert.equal(off.text, TEXT.off);
    assert.equal((await entryOf()).muted, false);

    // Ein Erfolg hebt die Sperre für Fehlversuche nicht auf.
    assert.equal((await request(link('on', 'geraten99999'))).status, 429);
  });

  it('erfolgreiche Aufrufe zählen nie als Versuch', async () => {
    for (let i = 0; i < 25; i++) {
      assert.equal((await call(i % 2 ? 'off' : 'on')).status, 200);
    }
    assert.equal(countedFailures(), 0);
    for (let i = 0; i < 20; i++) {
      assert.equal((await request(link('on', 'gibtesnicht12'))).status, 404, `Fehlversuch ${i + 1}`);
    }
    assert.equal((await request(link('on', 'gibtesnicht12'))).status, 429);
  });

  it('nach 15 Minuten ist die Sperre vorbei', async () => {
    for (let i = 0; i < 20; i++) await request(link('on', 'gibtesnicht12'));
    assert.equal((await request(link('on', 'gibtesnicht12'))).status, 429);
    // Als lägen die Fehlversuche 15 Minuten zurück.
    backend.data.edit('mute_throttle.json', (all) => {
      for (const who of Object.keys(all)) all[who].first_at = now() - 901;
    });
    const r = await request(link('on', 'gibtesnicht12'));
    assert.equal(r.status, 404);
    assert.equal(r.text, TEXT.unknown);
  });
});

describe('Link-Vorschauen schalten nichts um', () => {
  // Nichts geschaltet: gleiche Version, weiter laut, kein Eintrag im Verlauf.
  const assertUntouched = async (before) => {
    const list = await admin.keys();
    assert.equal(list.version, before);
    assert.equal(list.keys.find((k) => k.id === anna.id).muted, false);
    assert.deepEqual(await keyEvents('key_muted', 'key_unmuted'), []);
  };

  it('eine HEAD-Anfrage ändert nichts', async () => {
    const before = await version();
    const r = await request(link('on'), { method: 'HEAD' });
    assert.ok(r.status < 500, `HTTP ${r.status}`);
    assert.equal(r.text, '');
    await assertUntouched(before);
  });

  it('Vorschau-Abrufer der Messenger (User-Agent): "Vorschau - nichts geaendert"', async () => {
    const before = await version();
    const agents = [
      'WhatsApp/2.23.20 A',
      'facebookexternalhit/1.1',
      'TelegramBot (like TwitterBot)',
      // iMessage holt die Vorschau mit dieser Kennung schon beim ABSENDER.
      'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_11_1) AppleWebKit/601.2.4 (KHTML, like Gecko) '
        + 'Version/9.0.1 Safari/601.2.4 facebookexternalhit/1.1 Facebot Twitterbot/1.0',
      'Slackbot-LinkExpanding 1.0 (+https://api.slack.com/robots)',
      'Mozilla/5.0 (compatible; Discordbot/2.0; +https://discordapp.com)',
    ];
    for (const agent of agents) {
      const r = await call('on', { 'user-agent': agent });
      assert.equal(r.status, 200, agent);
      assert.equal(r.text, TEXT.preview, agent);
      assert.match(r.headers.get('cache-control'), /no-store/);
    }
    // Auch per POST und wenn der Abrufer sich als Browser ausgibt.
    const posted = await postForm(endpoint(), { t: anna.mute_token, state: 'on' }, { 'user-agent': 'WhatsApp/2.23.20 A' });
    assert.equal(posted.text, TEXT.preview);
    const asBrowser = await call('on', { ...BROWSER, 'user-agent': 'facebookexternalhit/1.1' });
    assert.equal(asBrowser.status, 200);
    assert.equal(asBrowser.text, TEXT.preview);
    await assertUntouched(before);
  });

  it('Vorab-Abruf des Browsers (Sec-Purpose: prefetch): nichts geändert', async () => {
    const before = await version();
    const marks = [
      { 'sec-purpose': 'prefetch' },
      { 'sec-purpose': 'prefetch;prerender' },
      { purpose: 'prefetch' },
      { 'x-purpose': 'preview' },
      { 'x-moz': 'prefetch' },
    ];
    for (const headers of marks) {
      const r = await call('on', { ...BROWSER, ...headers });
      assert.equal(r.status, 200, JSON.stringify(headers));
      assert.equal(r.text, TEXT.preview, JSON.stringify(headers));
    }
    await assertUntouched(before);
  });

  it('ein normaler iOS-Kurzbefehl schaltet', async () => {
    const on = await call('on', { 'user-agent': 'Shortcuts/1 CFNetwork/1410 Darwin/22.6.0' });
    assert.equal(on.status, 200);
    assert.equal(on.text, TEXT.on);
    assert.equal((await entryOf()).muted, true);
    const off = await call('off', { 'user-agent': 'BackgroundShortcutRunner/2607.1 CFNetwork/1568.300.101 Darwin/24.2.0' });
    assert.equal(off.text, TEXT.off);
    assert.equal((await entryOf()).muted, false);
  });
});

describe('Aufruf im Browser', () => {
  it('gültiger Link: 303 auf die Bestätigungsseite - ohne Token und ohne Key in der Adresse', async () => {
    const before = await version();
    const r = await call('on', BROWSER);
    assert.equal(r.status, 303);
    assert.match(r.headers.get('cache-control'), /no-store/);
    const location = r.headers.get('location');
    assert.ok(location.startsWith('mute.php?done=on'), location);
    for (const part of ['at=', 'vol=', 'perm=0', 'until=']) {
      assert.ok(location.includes(part), `${part} in ${location}`);
    }
    assert.equal(location.includes('same='), false);
    assert.equal(location.includes(anna.mute_token), false);
    assert.equal(location.includes(ANNA_KEY), false);
    assert.equal(r.text.includes(anna.mute_token), false);

    // Geschaltet wurde trotzdem - wie beim Kurzbefehl.
    const list = await admin.keys();
    assert.equal(list.version, before + 1);
    assert.equal(list.keys.find((k) => k.id === anna.id).muted, true);
    assert.deepEqual((await keyEvents('key_muted')).map((e) => e.source), ['shortcut']);

    // Die Seite hinter der Umleitung bestätigt nur.
    const page = await request(redirectOf(r, link('on')).href, { headers: BROWSER });
    assert.equal(page.status, 200);
    assertPage(page);
    assert.match(page.text, /Stumm geschaltet/);
    assert.equal(await version(), before + 1);
  });

  it('als Seiten-Aufruf zählt auch Sec-Fetch-Dest: document; state=off leitet auf done=off', async () => {
    await call('on');
    const r = await call('off', { 'sec-fetch-dest': 'document' });
    assert.equal(r.status, 303);
    const target = redirectOf(r, link('off'));
    assert.ok(r.headers.get('location').startsWith('mute.php?done=off'));
    assert.ok(Math.abs(Number(target.searchParams.get('at')) - now()) <= 5);
    assert.equal(target.searchParams.has('same'), false);
    assert.equal((await entryOf()).muted, false);
    const page = await request(target.href, { headers: BROWSER });
    assert.equal(page.status, 200);
    assert.match(page.text, /Wieder laut/);
  });

  it('die Bestätigungsseite (done=…) zeigt nur an und schaltet nichts', async () => {
    const before = await version();
    const at = now() - 120;
    const until = at + MUTE_MAX;

    const on = await request(muteUrl({ done: 'on', at, vol: 0, perm: 0, until }), { headers: BROWSER });
    assert.equal(on.status, 200);
    assertPage(on);
    assert.match(on.text, /Stumm geschaltet/);
    assert.match(on.text, /lautlos/);
    assert.ok(on.text.includes(`Umgeschaltet am ${stamp(at)}`), 'Zeitpunkt des Umschaltens');
    assert.ok(on.text.includes(`Spätestens am ${stamp(until)} wird automatisch wieder laut geschaltet`), 'Ende');

    const quiet = await request(muteUrl({ done: 'on', at, vol: 3, perm: 1, until: 0 }), { headers: BROWSER });
    assert.match(quiet.text, /Lautstärke 3 von 10/);
    assert.match(quiet.text, /dauerhaft stumm/);
    assert.doesNotMatch(quiet.text, /automatisch wieder laut/);

    const off = await request(muteUrl({ done: 'off', at }), { headers: BROWSER });
    assert.equal(off.status, 200);
    assertPage(off);
    assert.match(off.text, /Wieder laut/);

    const same = await request(muteUrl({ done: 'on', same: 1, at, vol: 0, perm: 0, until }), { headers: BROWSER });
    assert.equal(same.status, 200);
    assert.match(same.text, /Stumm geschaltet/);
    assert.ok(same.text.includes(`Unverändert – stumm seit ${stamp(at)}`));
    const sameOff = await request(muteUrl({ done: 'off', same: 1, at: 0 }), { headers: BROWSER });
    assert.match(sameOff.text, /Unverändert/);
    assert.doesNotMatch(sameOff.text, /Wieder laut/);

    // Keiner dieser Aufrufe hat etwas geschaltet.
    const list = await admin.keys();
    assert.equal(list.version, before);
    assert.equal(list.keys.find((k) => k.id === anna.id).muted, false);
    assert.deepEqual(await keyEvents('key_muted', 'key_unmuted'), []);
  });

  it('ein zweiter Aufruf bei schon passendem Zustand leitet mit same=1 um', async () => {
    await call('on', BROWSER);
    const since = now() - 600;
    backend.data.edit('alarm_keys.json', (list) => { list.keys.find((k) => k.id === anna.id).muted_at = since; });
    const before = await version();

    const again = await call('on', BROWSER);
    assert.equal(again.status, 303);
    const q = redirectOf(again, link('on')).searchParams;
    assert.equal(q.get('done'), 'on');
    assert.equal(q.get('same'), '1');
    assert.equal(q.get('at'), String(since), 'at = Beginn der bestehenden Stummschaltung');
    assert.equal(await version(), before);

    await call('off', BROWSER);
    const againOff = await call('off', BROWSER);
    assert.equal(againOff.status, 303);
    const qOff = redirectOf(againOff, link('off')).searchParams;
    assert.equal(qOff.get('done'), 'off');
    assert.equal(qOff.get('same'), '1');
    assert.equal(qOff.get('at'), '0');
  });

  it('unbekannter Token: 404 als Seite "Unbekannter Link"', async () => {
    for (const token of ['gibtesnicht12', 'kurz']) {
      const r = await request(link('on', token), { headers: BROWSER });
      assert.equal(r.status, 404);
      assertPage(r);
      assert.match(r.text, /Unbekannter Link/);
      assert.equal(r.text.includes(token), false);
    }
    assert.equal((await entryOf()).muted, false);
  });

  it('fehlendes state: 400 als Seite "Link unvollständig"', async () => {
    const r = await request(muteUrl({ t: anna.mute_token }), { headers: BROWSER });
    assert.equal(r.status, 400);
    assertPage(r);
    assert.match(r.text, /Link unvollständig/);
    assert.equal((await entryOf()).muted, false);
  });

  it('die Seiten laden Stylesheet und Skript nur per Adresse vom selben Server', async () => {
    const pageUrl = muteUrl({ done: 'on', at: now(), vol: 0, perm: 0, until: now() + MUTE_MAX });
    const page = await request(pageUrl, { headers: BROWSER });
    assertPage(page);
    const sheets = [...page.text.matchAll(/<link\b[^>]*\brel="stylesheet"[^>]*>/gi)];
    assert.equal(sheets.length, 1, 'genau ein Stylesheet');
    const refs = [...page.text.matchAll(/<(?:link|script)\b[^>]*?\b(?:href|src)="([^"]+)"/gi)]
      .map((m) => m[1].replace(/&amp;/g, '&'));
    assert.ok(refs.length >= 2);
    for (const ref of refs) {
      const target = new URL(ref, pageUrl);
      assert.equal(target.origin, new URL(backend.base).origin, ref);
      const got = await request(target.href);
      assert.equal(got.status, 200, ref);
    }
  });
});

describe('Dauerhaft stumm und Sicherheitsnetz', () => {
  it('state=off beendet auch eine dauerhafte Stummschaltung - mit eigenem Eintrag im Verlauf', async () => {
    assert.equal((await admin.post({ action: 'keys_set_mute_permanent', id: anna.id, permanent: '1' })).ok, true);
    let entry = await entryOf();
    assert.equal(entry.muted, true);
    assert.equal(entry.mute_permanent, true);

    const r = await call('off');
    assert.equal(r.status, 200);
    assert.equal(r.text, TEXT.off);
    entry = await entryOf();
    assert.equal(entry.muted, false);
    assert.equal(entry.mute_permanent, false);

    const ended = (await keyEvents('key_mute_permanent'))[0];
    assert.equal(ended.permanent, false);
    assert.equal(ended.source, 'shortcut');
    assert.equal(ended.label, 'Anna');
    assert.equal((await keyEvents('key_unmuted'))[0].source, 'shortcut');
  });

  it('state=on setzt nie "dauerhaft" - auch nicht nach einer beendeten Dauer-Stummschaltung', async () => {
    const tries = await request(muteUrl({ t: anna.mute_token, state: 'on', permanent: '1', perm: '1', mute_permanent: '1' }));
    assert.equal(tries.text, TEXT.on);
    let entry = await entryOf();
    assert.equal(entry.muted, true);
    assert.equal(entry.mute_permanent, false);
    assert.equal(entry.mute_until, entry.muted_at + MUTE_MAX, 'das Sicherheitsnetz gilt');

    // Dauerhaft (Dashboard) -> laut (Link) -> stumm (Link): wieder mit Sicherheitsnetz.
    assert.equal((await admin.post({ action: 'keys_set_mute_permanent', id: anna.id, permanent: '1' })).ok, true);
    await call('off');
    await call('on');
    entry = await entryOf();
    assert.equal(entry.muted, true);
    assert.equal(entry.mute_permanent, false);
    assert.equal(entry.mute_until, entry.muted_at + MUTE_MAX);
    assert.deepEqual((await keyEvents('key_mute_permanent')).map((e) => e.permanent), [false, true]);
  });

  it('eine überfällige Stummschaltung eines ANDEREN endet beim Aufruf nebenbei', async () => {
    const bert = await admin.addKey('Bert', BERT_KEY);
    assert.equal((await admin.post({ action: 'keys_mute', id: bert.id, muted: '1' })).ok, true);
    const before = await version();
    backend.data.edit('alarm_keys.json', (list) => {
      list.keys.find((k) => k.id === bert.id).muted_at = now() - (MUTE_MAX + 100);
    });

    const mark = backend.bark.mark();
    assert.equal((await call('on')).text, TEXT.on);
    const info = await waitFor(() => backend.bark.to(BERT_KEY, mark)[0], 'Info "wieder LAUT" an Bert');
    assert.equal(info.fields.title, 'Alarmton wieder LAUT');
    assert.equal(info.fields.level, 'passive');
    assert.match(info.fields.body, /Stummschaltung automatisch beendet/);

    // Gespeichert vom Link-Aufruf selbst (noch bevor das Dashboard nachsieht).
    const stored = backend.data.read('alarm_keys.json');
    assert.equal(stored.keys.find((k) => k.id === bert.id).muted, false);
    assert.equal(stored.keys.find((k) => k.id === anna.id).muted, true);
    assert.ok(stored.version > before);

    await settle();
    assert.deepEqual(backend.bark.to(ANNA_KEY, mark), []);
    assert.deepEqual(backend.bark.to(STATUS_KEY, mark), [], 'mute_expired ist ab Werk aus');
    const auto = (await keyEvents('key_unmuted')).find((e) => e.label === 'Bert');
    assert.equal(auto.source, 'auto');
    assert.equal((await entryOf(bert.id)).muted, false);
  });

  it('eine DAUERHAFTE Stummschaltung eines anderen bleibt dabei bestehen', async () => {
    const bert = await admin.addKey('Bert', BERT_KEY);
    assert.equal((await admin.post({ action: 'keys_set_mute_permanent', id: bert.id, permanent: '1' })).ok, true);
    backend.data.edit('alarm_keys.json', (list) => {
      list.keys.find((k) => k.id === bert.id).muted_at = now() - 30 * 86400;
    });
    const mark = backend.bark.mark();
    assert.equal((await call('on')).text, TEXT.on);
    await settle();
    assert.deepEqual(backend.bark.since(mark), []);
    const entry = await entryOf(bert.id);
    assert.equal(entry.muted, true);
    assert.equal(entry.mute_permanent, true);
    assert.equal('mute_until' in entry, false);
  });

  it('die Umleitung nennt Stumm-Lautstärke und Ende: until = muted_at + mute_max_seconds', async () => {
    assert.equal((await admin.post({ action: 'keys_set_mute_volume', id: anna.id, volume: '3' })).ok, true);
    const first = await call('on', BROWSER);
    assert.equal(first.status, 303);
    let q = redirectOf(first, link('on')).searchParams;
    let entry = await entryOf();
    assert.ok(Math.abs(Number(q.get('at')) - entry.muted_at) <= 2);
    assert.equal(q.get('vol'), '3');
    assert.equal(q.get('perm'), '0');
    assert.equal(q.get('until'), String(entry.muted_at + MUTE_MAX));
    assert.equal(entry.mute_until, entry.muted_at + MUTE_MAX);

    // Bei einer schon laufenden Stummschaltung zählt ihr Beginn.
    const since = now() - 600;
    backend.data.edit('alarm_keys.json', (list) => { list.keys.find((k) => k.id === anna.id).muted_at = since; });
    q = redirectOf(await call('on', BROWSER), link('on')).searchParams;
    assert.equal(q.get('same'), '1');
    assert.equal(q.get('until'), String(since + MUTE_MAX));

    // Die Dauer kommt aus der Konfiguration; 0 = keine automatische Rückschaltung.
    await backend.configure({ mute_max_seconds: 7200 });
    q = redirectOf(await call('on', BROWSER), link('on')).searchParams;
    assert.equal(q.get('until'), String(since + 7200));
    await backend.configure({ mute_max_seconds: 0 });
    q = redirectOf(await call('on', BROWSER), link('on')).searchParams;
    assert.equal(q.get('until'), '0');
  });

  it('dauerhaft stumm und state=on: perm=1, kein Ende (until=0), nichts geändert', async () => {
    assert.equal((await admin.post({ action: 'keys_set_mute_permanent', id: anna.id, permanent: '1' })).ok, true);
    const before = await version();
    const r = await call('on', BROWSER);
    assert.equal(r.status, 303);
    const q = redirectOf(r, link('on')).searchParams;
    assert.equal(q.get('done'), 'on');
    assert.equal(q.get('same'), '1');
    assert.equal(q.get('perm'), '1');
    assert.equal(q.get('until'), '0');

    const list = await admin.keys();
    assert.equal(list.version, before);
    const entry = list.keys.find((k) => k.id === anna.id);
    assert.equal(entry.muted, true);
    assert.equal(entry.mute_permanent, true);
  });
});

describe('Störung beim Speichern', () => {
  it('Liste samt Sicherung unlesbar: 500 "speichern fehlgeschlagen" - und kein Fehlversuch', async () => {
    backend.data.corrupt('alarm_keys.json');
    backend.data.corrupt('alarm_keys.json.bak');
    for (let i = 0; i < 5; i++) {
      const r = await call(i % 2 ? 'off' : 'on');
      assert.equal(r.status, 500);
      assert.equal(r.text, TEXT.storage);
      assert.match(r.headers.get('cache-control'), /no-store/);
    }
    const page = await call('on', BROWSER);
    assert.equal(page.status, 500);
    assertPage(page);
    assert.match(page.text, /Nicht gespeichert/);
    assert.equal(countedFailures(), 0);

    // Wären die sechs Aufrufe gezählt worden, käme unter den nächsten 20
    // Fehlversuchen schon ein 429.
    backend.data.remove('alarm_keys.json');
    backend.data.remove('alarm_keys.json.bak');
    for (let i = 0; i < 20; i++) {
      assert.equal((await request(link('on', 'gibtesnicht12'))).status, 404, `Fehlversuch ${i + 1}`);
    }
    assert.equal((await request(link('on', 'gibtesnicht12'))).status, 429);
  });

  it('Liste lässt sich nicht speichern: 500, nichts geschaltet, kein Eintrag im Verlauf', async () => {
    // Lesbar bleibt die Liste über ihre Sicherung; an der Stelle der Liste
    // selbst liegt ein Ordner - das Ersetzen der Datei scheitert.
    backend.data.write('alarm_keys.json.bak', backend.data.read('alarm_keys.json'));
    backend.data.block('alarm_keys.json');
    try {
      const r = await call('on');
      assert.equal(r.status, 500);
      assert.equal(r.text, TEXT.storage);
      const page = await call('on', BROWSER);
      assert.equal(page.status, 500);
      assertPage(page);
      assert.match(page.text, /Nicht gespeichert/);
    } finally {
      backend.data.unblock('alarm_keys.json');
    }
    assert.equal(countedFailures(), 0);
    assert.equal((await entryOf()).muted, false);
    assert.deepEqual(await keyEvents('key_muted', 'key_unmuted'), []);

    // Sobald das Speichern wieder geht, schaltet derselbe Link.
    assert.equal((await call('on')).text, TEXT.on);
    assert.equal((await entryOf()).muted, true);
  });
});
