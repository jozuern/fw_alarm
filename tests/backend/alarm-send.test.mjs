// Manueller Alarm direkt vom Server an Bark (dashboard_api.php: alarm,
// false_alarm, keys_test) - der Weg, den man braucht, wenn die Box oder ihre
// Kette klemmt. Geprüft wird nur, was von außen zu sehen ist: was bei Bark
// ankommt (Zeichen für Zeichen), wer es bekommt, was die Antwort und der
// Verlauf sagen. Die Regeln: bestätigter Modus (expect_demo), Demo-Modus,
// Arbeitsmodus (stumm), Wiederholungen pro Empfänger, Nachsendung nur an die
// nicht Erreichten - und dass ein Alarm nie am Speichern scheitert.
import assert from 'node:assert/strict';
import { readFileSync, writeFileSync } from 'node:fs';
import { after, before, beforeEach, describe, it } from 'node:test';
import {
  READER, STATUS_KEY, now, postForm, session, settle, startBackend, waitFor,
} from './harness.mjs';

let backend;
let admin;
before(async () => { backend = await startBackend(); });
after(async () => { await backend.stop(); });
beforeEach(async () => {
  await backend.reset();
  backend.bark.slowMs = 1500;
  admin = await session(backend);
});

// ---- Empfänger und Texte ---------------------------------------------------
const KEY_A = 'annakey00001';
const KEY_B = 'benkey000001';
const KEY_C = 'carlakey0001';
const KEY_FAIL = 'failkey00001';   // Bark antwortet 500
const KEY_BAD = 'badkey000001';    // Bark antwortet 400
const KEY_DROP = 'dropkey00001';   // Verbindung bricht ab
const KEY_SLOW = 'slowkey00001';   // Bark antwortet erst nach slowMs
const NOT_KEY = 'startkey0001';    // Not-Liste (bark_keys_alarm) des Prüfstands
const ALARM_TITLE = 'FEUERWEHR-ALARM (manuell)';
const ALARM_BODY = 'Manuelle Alarmierung ueber das Dashboard!';
const CLEAR_TITLE = 'ENTWARNUNG - Fehlalarm';
const CLEAR_BODY = 'Der letzte Alarm war ein FEHLALARM - kein Einsatz.';
const LATE = ' (verspaetet zugestellt)';
const RESEND_EMPTY = 'Keiner der ausgewählten Empfänger steht (noch) in der aktuellen Empfängerliste – es wurde NICHTS gesendet.';
const MUTE_MAX = 43200;            // mute_max_seconds ab Werk (12 h)

// ---- Aktionen --------------------------------------------------------------
// expect_demo: '0' = "ich habe LIVE bestätigt"; mit expect_demo: undefined
// fehlt das Feld ganz.
const alarm = (fields = {}) => admin.post({ action: 'alarm', expect_demo: '0', ...fields });
const entwarnung = (fields = {}) => admin.post({ action: 'false_alarm', expect_demo: '0', ...fields });
const testPush = (id) => admin.post({ action: 'keys_test', id });
async function ok(promise) {
  const res = await promise;
  assert.equal(res.ok, true, JSON.stringify(res));
  return res;
}
const mute = (entry) => ok(admin.post({ action: 'keys_mute', id: entry.id, muted: '1' }));
const muteVolume = (entry, volume) => ok(admin.post({ action: 'keys_set_mute_volume', id: entry.id, volume }));
const demoOn = (entry) => ok(admin.post({ action: 'demo_set', enabled: '1', id: entry.id }));
const listEntry = (id) => backend.data.read('alarm_keys.json').keys.find((k) => k.id === id);
// Eine Stummschaltung "seit s Sekunden" - als hätte der Kollege sie so lange nicht beendet.
const muteSince = (entry, seconds) => backend.data.edit('alarm_keys.json', (d) => {
  d.keys.find((k) => k.id === entry.id).muted_at = now() - seconds;
});

// ---- Was bei Bark ankam ----------------------------------------------------
// Felder einer Anfrage als Text - ob der Server sie als Formular oder als JSON
// schickt, ist für Bark dasselbe.
const fieldsOf = (req) => Object.fromEntries(Object.entries(req.fields).map(([k, v]) => [k, String(v)]));
// Text (trägt den Zeitstempel) und alle übrigen Felder getrennt.
const split = (req) => {
  const { body, ...rest } = fieldsOf(req);
  return { body, rest };
};
const sansStamp = (body) => body.replace(/^\[[^\]]*\] /, '');
const sentTo = (key, mark) => backend.bark.to(key, mark);
const keysHit = (mark) => [...new Set(backend.bark.since(mark).map((r) => r.key))].sort();
// Für "es darf NICHTS rausgehen".
async function assertNothingSent(mark, why = 'nichts an Bark') {
  await settle();
  assert.deepEqual(backend.bark.since(mark), [], why);
}

// Die Felder eines Alarms ohne den Text: Critical Alert mit Alarmton; call=1
// (Ton-Wiederholung) nur für laute Empfänger.
const alarmFields = ({ volume = '10', call = true, title = ALARM_TITLE } = {}) => ({
  title, sound: 'alarm_fw', level: 'critical', volume, ...(call ? { call: '1' } : {}),
});
const clearFields = ({ volume = '10', sound = 'glass' } = {}) => ({
  title: CLEAR_TITLE, sound, level: 'critical', volume,
});

// Zeitstempel im Bark-Text: "[TT.MM.JJJJ HH:MM]" in der Zeitzone des Servers.
const clock = new Intl.DateTimeFormat('en-GB', {
  timeZone: 'Europe/Berlin', year: 'numeric', month: '2-digit', day: '2-digit',
  hour: '2-digit', minute: '2-digit', hourCycle: 'h23',
});
function stamp(ts) {
  const p = Object.fromEntries(clock.formatToParts(new Date(ts * 1000)).map((x) => [x.type, x.value]));
  return `[${p.day}.${p.month}.${p.year} ${p.hour}:${p.minute}]`;
}
// Text eines frischen Versands: Stempel = Zeit des Versands. Zwischen
// started_at und dem Stempeln kann die Minute umspringen - beides gilt.
function assertStamped(body, startedAt, text) {
  const allowed = [startedAt, startedAt + 5].map((t) => `${stamp(t)} ${text}`);
  assert.ok(allowed.includes(body), `Text "${body}" - erwartet "${allowed[0]}"`);
}
// Genau EINE Anfrage an diesen Key, mit genau diesen Feldern.
function assertOne(key, mark, expected, startedAt, text) {
  const got = sentTo(key, mark);
  assert.equal(got.length, 1, `genau eine Meldung an ${key}`);
  assert.equal(got[0].method, 'POST');
  const { body, rest } = split(got[0]);
  assert.deepEqual(rest, expected, `Felder an ${key}`);
  assertStamped(body, startedAt, text);
  return got[0];
}
const assertAlarm = (key, mark, res, expected = alarmFields()) => assertOne(key, mark, expected, res.started_at, ALARM_BODY);
const assertClear = (key, mark, res, expected = clearFields()) => assertOne(key, mark, expected, res.started_at, CLEAR_BODY);

// ---- Verlauf ---------------------------------------------------------------
// Wie das Dashboard ihn zeigt (neueste zuerst), ohne die Uhrzeit.
const alarmLog = async () => (await admin.log('alarm')).map(({ at, ...rest }) => rest);
// Der Prüfstand kann den Verlauf nur lesen. Hier werden passende Einträge
// "älter gemacht" (log_at), alles andere bleibt Zeile für Zeile wie gespeichert.
function ageLogEntries(seconds, match) {
  const path = backend.data.path('commands.log');
  const lines = readFileSync(path, 'utf8').split('\n').map((line) => {
    if (!line.startsWith('{')) return line;
    const entry = JSON.parse(line);
    if (!match(entry)) return line;
    entry.log_at -= seconds;
    return JSON.stringify(entry);
  });
  writeFileSync(path, lines.join('\n'));
}

// Eintrag für eine von Hand geschriebene alarm_keys.json.
const handEntry = (id, key, extra = {}) => ({
  id, label: `Hand ${id}`, key, added_at: now() - 1000, mute_token: `handtoken${String(id).padStart(3, '0')}`, ...extra,
});

describe('Alarm: was an Bark geht', () => {
  it('zwei laute Empfänger: je EINE Meldung - Critical Alert, Alarmton, volle Lautstärke, call=1', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    const ben = await admin.addKey('Ben', KEY_B);
    const mark = backend.bark.mark();
    const res = await alarm();

    assert.equal(res.http, 200);
    assert.equal(res.ok, true);
    assert.equal(res.demo, false);
    assert.equal(res.message, '2/2 Empfänger erreicht.');
    assert.ok(Math.abs(res.started_at - now()) <= 5, 'started_at = Zeit des Versands');
    assert.deepEqual(res.results, [
      { id: anna.id, key: 'anna...', ok: true, http: 200, err: '', muted: false, label: 'Anna' },
      { id: ben.id, key: 'benk...', ok: true, http: 200, err: '', muted: false, label: 'Ben' },
    ]);
    // Keys sind Geheimnisse: in der Antwort nur maskiert.
    for (const key of [KEY_A, KEY_B]) assert.equal(JSON.stringify(res).includes(key), false, 'kein Klartext-Key');

    assertAlarm(KEY_A, mark, res);
    assertAlarm(KEY_B, mark, res);
    assert.equal(backend.bark.since(mark).length, 2, 'sonst geht nichts raus');
  });

  it('ein stummer Empfänger bekommt denselben Critical Alert - mit Lautstärke 0 und ohne call', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    const ben = await admin.addKey('Ben', KEY_B);
    await mute(ben);
    const mark = backend.bark.mark();
    const res = await alarm();

    assert.equal(res.ok, true);
    assertAlarm(KEY_A, mark, res);
    assertAlarm(KEY_B, mark, res, alarmFields({ volume: '0', call: false }));
    assert.deepEqual(res.results.map((r) => [r.id, r.ok, r.muted]), [[anna.id, true, false], [ben.id, true, true]]);
    assert.match(res.message, /^2\/2 Empfänger erreicht\. 1 davon stumm/);
  });

  it('mit eingestellter Stumm-Lautstärke 3 kommt er mit volume=3 an, weiterhin ohne call', async () => {
    const ben = await admin.addKey('Ben', KEY_B);
    await mute(ben);
    await muteVolume(ben, 3);
    const mark = backend.bark.mark();
    const res = await alarm();
    assertAlarm(KEY_B, mark, res, alarmFields({ volume: '3', call: false }));
  });

  it('die Stumm-Lautstärke gilt nur, solange jemand stumm ist', async () => {
    const ben = await admin.addKey('Ben', KEY_B);
    await muteVolume(ben, 3);
    const mark = backend.bark.mark();
    const res = await alarm();
    assertAlarm(KEY_B, mark, res);
  });

  it('eine überfällige Stummschaltung zählt als LAUT - und wird nach dem Versand weggeschrieben', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    await mute(anna);
    muteSince(anna, MUTE_MAX + 100);
    const versionBefore = backend.data.read('alarm_keys.json').version;
    const mark = backend.bark.mark();
    const res = await alarm();

    assert.equal(res.ok, true);
    assert.equal(res.results[0].muted, false);
    const critical = sentTo(KEY_A, mark).filter((r) => r.fields.level === 'critical');
    assert.equal(critical.length, 1);
    assert.deepEqual(split(critical[0]).rest, alarmFields());

    // Danach steht der Eintrag als laut in der Liste, unter neuer Version
    // (die Box holt sie sich daraufhin).
    const stored = listEntry(anna.id);
    assert.equal(stored.muted, false);
    assert.equal(stored.muted_at, 0);
    assert.ok(backend.data.read('alarm_keys.json').version > versionBefore, 'neue Listen-Version');
    // Der Betroffene erfährt es leise (nach der Antwort).
    const info = await waitFor(() => sentTo(KEY_A, mark).find((r) => r.fields.level !== 'critical'),
      'Info "wieder laut" an den Betroffenen');
    assert.equal(info.fields.level, 'passive');
    assert.equal(info.fields.title, 'Alarmton wieder LAUT');
    const auto = (await admin.log('keys')).find((e) => e.event === 'key_unmuted');
    assert.equal(auto.by, 'automatisch');
    assert.equal(auto.source, 'auto');
  });

  it('eine Stummschaltung kurz vor der Frist bleibt stumm', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    await mute(anna);
    muteSince(anna, MUTE_MAX - 100);
    const mark = backend.bark.mark();
    const res = await alarm();
    assertAlarm(KEY_A, mark, res, alarmFields({ volume: '0', call: false }));
    assert.equal(listEntry(anna.id).muted, true);
  });

  it('der Alarm geht auch raus, wenn sich die Liste gerade nicht speichern lässt', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    await mute(anna);
    // Überfällig stumm; lesbar bleibt die Liste über ihre Sicherung, speichern
    // lässt sie sich nicht (an ihrer Stelle liegt ein Ordner).
    const state = backend.data.read('alarm_keys.json');
    state.keys[0].muted_at = now() - MUTE_MAX - 100;
    backend.data.write('alarm_keys.json.bak', state);
    backend.data.block('alarm_keys.json');
    try {
      const mark = backend.bark.mark();
      const res = await alarm();
      assert.equal(res.http, 200);
      assert.equal(res.ok, true);
      assert.equal(res.results[0].muted, false, 'für den Alarm zählt er schon als laut');
      assertAlarm(KEY_A, mark, res);
      // Gespeichert wurde nichts - dann gibt es auch keine Info "wieder laut",
      // und das Dashboard zeigt die Zeile als überfällig (die Box alarmiert
      // ihn bis zum Speichern weiter stumm).
      await settle();
      assert.equal(backend.bark.since(mark).length, 1);
      assert.equal(backend.data.read('alarm_keys.json.bak').keys[0].muted, true);
      const row = (await admin.keys()).keys[0];
      assert.equal(row.muted, false);
      assert.equal(row.mute_overdue, true);
    } finally {
      backend.data.unblock('alarm_keys.json');
    }
  });

  it('der Alarm geht auch raus, wenn sich der Verlauf nicht schreiben lässt', async () => {
    await admin.addKey('Anna', KEY_A);
    backend.data.block('commands.log');
    try {
      const mark = backend.bark.mark();
      const res = await alarm();
      assert.equal(res.http, 200);
      assert.equal(res.ok, true);
      assertAlarm(KEY_A, mark, res);
    } finally {
      backend.data.unblock('commands.log');
    }
  });

  it('der Alarm geht auch raus, wenn im Datenordner gar nichts mehr zu speichern ist', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    await admin.addKey('Ben', KEY_B);
    await mute(anna);
    const state = backend.data.read('alarm_keys.json');
    state.keys[0].muted_at = now() - MUTE_MAX - 100;
    backend.data.write('alarm_keys.json.bak', state);
    // Liste, Verlauf und die Sperrdateien dazu: nichts davon lässt sich anlegen
    // oder ersetzen (so sah die Störung 10/2026 für den Alarm aus).
    const blocked = ['alarm_keys.json', 'commands.log', 'keys.lock', 'log.lock'];
    for (const name of blocked) backend.data.block(name);
    try {
      const mark = backend.bark.mark();
      const res = await alarm();
      assert.equal(res.http, 200);
      assert.equal(res.ok, true);
      assert.equal(res.message, '2/2 Empfänger erreicht.');
      assertAlarm(KEY_A, mark, res);
      assertAlarm(KEY_B, mark, res);
    } finally {
      for (const name of blocked) backend.data.unblock(name);
    }
  });

  it('dauerhaft stumm läuft nie ab: auch nach Tagen Stumm-Lautstärke und kein call', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    await ok(admin.post({ action: 'keys_set_mute_permanent', id: anna.id, permanent: '1' }));
    muteSince(anna, 5 * 86400);
    const mark = backend.bark.mark();
    const res = await alarm();
    assertAlarm(KEY_A, mark, res, alarmFields({ volume: '0', call: false }));
    assert.equal(res.results[0].muted, true);
    await settle();
    assert.equal(backend.bark.since(mark).length, 1, 'keine automatische Rückschaltung');
    assert.equal(listEntry(anna.id).muted, true);
  });

  it('Titel und Text gehen so raus, wie sie in der Konfiguration stehen', async () => {
    const title = 'EINSATZ FF Musterdorf: B3 & Co. (100%)';
    const body = 'Wohnungsbrand, Hauptstr. 5 + Nachalarm = "sofort" kommen? #1';
    await backend.configure({ bark_alarm_title: title, bark_alarm_body: body });
    await admin.addKey('Anna', KEY_A);
    const mark = backend.bark.mark();
    const res = await alarm();
    assertOne(KEY_A, mark, alarmFields({ title }), res.started_at, body);
  });

  it('auch Umlaute im konfigurierten Titel und Text werden nicht umgeschrieben', async () => {
    const title = 'Übung Löschzug';
    const body = 'Bitte zum Gerätehaus – Maßnahme läuft';
    await backend.configure({ bark_alarm_title: title, bark_alarm_body: body });
    await admin.addKey('Anna', KEY_A);
    const mark = backend.bark.mark();
    const res = await alarm();
    assertOne(KEY_A, mark, alarmFields({ title }), res.started_at, body);
  });

  it('Ton und Lautstärke kommen aus der Konfiguration', async () => {
    await backend.configure({ bark_alarm_sound: 'sirene', bark_alarm_volume: 7 });
    await admin.addKey('Anna', KEY_A);
    const mark = backend.bark.mark();
    const res = await alarm();
    assertAlarm(KEY_A, mark, res, { ...alarmFields({ volume: '7' }), sound: 'sirene' });
  });

  it('bark_alarm_call aus: niemand bekommt call', async () => {
    await backend.configure({ bark_alarm_call: false });
    const ben = await admin.addKey('Ben', KEY_B);
    await admin.addKey('Anna', KEY_A);
    await mute(ben);
    const mark = backend.bark.mark();
    const res = await alarm();
    assertAlarm(KEY_A, mark, res, alarmFields({ call: false }));
    assertAlarm(KEY_B, mark, res, alarmFields({ volume: '0', call: false }));
  });
});

describe('Alarm: wer ihn bekommt', () => {
  it('leere Dashboard-Liste: die Not-Liste aus der Konfiguration - und die Antwort sagt das', async () => {
    const mark = backend.bark.mark();
    const res = await alarm();
    assert.equal(res.ok, true);
    assertAlarm(NOT_KEY, mark, res);
    assert.deepEqual(keysHit(mark), [NOT_KEY]);
    assert.match(res.message, /^1\/1 Empfänger erreicht\./);
    assert.match(res.message, /ACHTUNG: Fallback-Liste aus config\.php verwendet/);
    // Einträge der Not-Liste haben keine Nummer und keinen Namen.
    assert.deepEqual(res.results.map((r) => [r.id, r.label, r.key, r.ok]), [[0, '', 'star...', true]]);
  });

  it('die Not-Liste wird bereinigt: Doppelte und ungültige Keys fallen weg, höchstens 10', async () => {
    const valid = Array.from({ length: 12 }, (_, i) => `notkey${String(i + 1).padStart(5, '0')}`);
    await backend.configure({
      bark_keys_alarm: [valid[0], valid[0], 'kurz', 'mit leerzeichen', 'un!gueltig', '', ...valid.slice(1)],
    });
    const mark = backend.bark.mark();
    const res = await alarm();
    assert.equal(res.ok, true);
    assert.deepEqual(keysHit(mark), valid.slice(0, 10));
    assert.equal(backend.bark.since(mark).length, 10, 'jeder genau einmal');
    assert.match(res.message, /^10\/10 Empfänger erreicht\./);
  });

  it('ist die Dashboard-Liste gepflegt, bekommt die Not-Liste nichts', async () => {
    await admin.addKey('Anna', KEY_A);
    const mark = backend.bark.mark();
    const res = await alarm();
    assert.deepEqual(keysHit(mark), [KEY_A]);
    assert.doesNotMatch(res.message, /Fallback/);
  });

  it('Liste UND Sicherung unlesbar: ebenfalls die Not-Liste', async () => {
    await admin.addKey('Anna', KEY_A);
    backend.data.corrupt('alarm_keys.json');
    backend.data.corrupt('alarm_keys.json.bak');
    const mark = backend.bark.mark();
    const res = await alarm();
    assert.equal(res.http, 200);
    assert.equal(res.ok, true);
    assertAlarm(NOT_KEY, mark, res);
    assert.deepEqual(keysHit(mark), [NOT_KEY]);
    assert.match(res.message, /Fallback-Liste aus config\.php verwendet/);
  });

  it('nur die Liste kaputt: es gilt die Sicherung', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    await admin.addKey('Ben', KEY_B);
    // Die Sicherung hält einen älteren Stand: nur Anna.
    const state = backend.data.read('alarm_keys.json');
    backend.data.write('alarm_keys.json.bak', { ...state, keys: state.keys.filter((k) => k.id === anna.id) });
    backend.data.corrupt('alarm_keys.json');
    const mark = backend.bark.mark();
    const res = await alarm();
    assert.equal(res.ok, true);
    assert.deepEqual(keysHit(mark), [KEY_A]);
    assert.doesNotMatch(res.message, /Fallback/);
  });

  it('eine Liste nur aus ungültigen Keys zählt wie eine leere: die Not-Liste', async () => {
    backend.data.write('alarm_keys.json', {
      next_id: 5, version: 4, keys: [handEntry(1, 'kurz'), handEntry(2, 'un gueltig!')],
    });
    const mark = backend.bark.mark();
    const res = await alarm();
    assert.equal(res.ok, true);
    assert.deepEqual(keysHit(mark), [NOT_KEY]);
    assert.match(res.message, /Fallback-Liste aus config\.php verwendet/);
  });

  it('weder Dashboard- noch Not-Liste: nichts gesendet, die Antwort sagt warum', async () => {
    await backend.configure({ bark_keys_alarm: [] });
    const mark = backend.bark.mark();
    const res = await alarm();
    assert.equal(res.http, 200);
    assert.equal(res.ok, false);
    assert.match(res.message, /^Keine Alarm-Empfänger/);
    assert.deepEqual(res.results, []);
    await assertNothingSent(mark);
  });

  it('von Hand verdorbene Liste: nur die gültigen, einmaligen, ersten 10 werden alarmiert', async () => {
    const valid = Array.from({ length: 12 }, (_, i) => `handkey${String(i + 1).padStart(5, '0')}`);
    backend.data.write('alarm_keys.json', {
      next_id: 30,
      version: 7,
      keys: [
        handEntry(1, valid[0]),
        handEntry(2, 'kurz'),                 // zu kurz
        'kein Eintrag',                       // gar kein Objekt
        handEntry(3, valid[0]),               // doppelt
        handEntry(4, 'un gueltig!'),          // verbotene Zeichen
        ...valid.slice(1).map((key, i) => handEntry(10 + i, key)),
      ],
    });
    const mark = backend.bark.mark();
    const res = await alarm();
    assert.equal(res.ok, true);
    assert.deepEqual(keysHit(mark), valid.slice(0, 10));
    assert.equal(backend.bark.since(mark).length, 10, 'jeder genau einmal');
    assert.deepEqual(res.results.map((r) => r.id), [1, 10, 11, 12, 13, 14, 15, 16, 17, 18]);
    assert.match(res.message, /^10\/10 Empfänger erreicht\./);
  });

  it('Demo-Modus: NUR der Test-Empfänger - und die Meldung gleicht der echten Zeichen für Zeichen', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    await admin.addKey('Ben', KEY_B);
    await admin.addKey('Carla', KEY_C);
    let mark = backend.bark.mark();
    const live = await alarm();
    const real = split(assertAlarm(KEY_A, mark, live));
    assert.equal(backend.bark.since(mark).length, 3);

    await demoOn(anna);
    mark = backend.bark.mark();
    const res = await alarm({ expect_demo: String(anna.id) });
    assert.equal(res.ok, true);
    assert.equal(res.demo, true);
    assert.match(res.message, /^DEMO-MODUS: nur Test-Empfänger alarmiert\. 1\/1 Empfänger erreicht\./);
    assert.deepEqual(res.results.map((r) => [r.id, r.label, r.ok]), [[anna.id, 'Anna', true]]);
    assert.deepEqual(keysHit(mark), [KEY_A]);
    const demo = split(assertAlarm(KEY_A, mark, res));
    assert.deepEqual(demo.rest, real.rest);
    assert.equal(sansStamp(demo.body), sansStamp(real.body));
  });

  it('Demo-Modus: ein stummer Test-Empfänger bekommt auch den Demo-Alarm mit seiner Stumm-Lautstärke', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    await admin.addKey('Ben', KEY_B);
    await mute(anna);
    await muteVolume(anna, 2);
    await demoOn(anna);
    const mark = backend.bark.mark();
    const res = await alarm({ expect_demo: String(anna.id) });
    assertAlarm(KEY_A, mark, res, alarmFields({ volume: '2', call: false }));
    assert.deepEqual(keysHit(mark), [KEY_A]);
  });
});

describe('Alarm: bestätigter Modus (expect_demo)', () => {
  async function assertRefused(res, mark) {
    assert.equal(res.http, 409);
    assert.equal(res.ok, false);
    assert.equal(res.code, 'mode_changed');
    assert.match(res.message, /NICHTS gesendet/);
    await assertNothingSent(mark);
    // Auch der Verlauf kennt keinen Versand.
    assert.deepEqual(await alarmLog(), []);
  }

  it('Feld fehlt: 409 mode_changed, nichts gesendet', async () => {
    await admin.addKey('Anna', KEY_A);
    const mark = backend.bark.mark();
    const res = await alarm({ expect_demo: undefined });
    await assertRefused(res, mark);
    assert.equal(res.demo.enabled, false);
  });

  it('Feld leer: ebenfalls 409', async () => {
    await admin.addKey('Anna', KEY_A);
    const mark = backend.bark.mark();
    await assertRefused(await alarm({ expect_demo: '' }), mark);
  });

  it('LIVE, aber als Demo bestätigt: 409, nichts gesendet', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    await admin.addKey('Ben', KEY_B);
    const mark = backend.bark.mark();
    const res = await alarm({ expect_demo: String(anna.id) });
    await assertRefused(res, mark);
    // Die Antwort nennt den wirklichen Modus - damit stellt sich die Oberfläche um.
    assert.equal(res.demo.enabled, false);
    assert.equal(res.demo.key_id, 0);
  });

  it('Demo an, aber als LIVE bestätigt: 409, nichts gesendet', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    await admin.addKey('Ben', KEY_B);
    await demoOn(anna);
    const mark = backend.bark.mark();
    const res = await alarm({ expect_demo: '0' });
    await assertRefused(res, mark);
    assert.equal(res.demo.enabled, true);
    assert.equal(res.demo.key_id, anna.id);
    assert.equal(res.demo.label, 'Anna');
    assert.equal(res.demo.key_masked, 'anna...');
  });

  it('Demo für Anna, bestätigt wurde Ben: 409, nichts gesendet', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    const ben = await admin.addKey('Ben', KEY_B);
    await demoOn(anna);
    const mark = backend.bark.mark();
    await assertRefused(await alarm({ expect_demo: String(ben.id) }), mark);
  });

  it('der richtige Wert sendet: 0 im LIVE-Betrieb, die Nummer des Test-Empfängers im Demo-Modus', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    const ben = await admin.addKey('Ben', KEY_B);
    let mark = backend.bark.mark();
    assert.equal((await alarm({ expect_demo: '0' })).ok, true);
    assert.deepEqual(keysHit(mark), [KEY_A, KEY_B].sort());

    await demoOn(ben);
    mark = backend.bark.mark();
    assert.equal((await alarm({ expect_demo: String(ben.id) })).ok, true);
    assert.deepEqual(keysHit(mark), [KEY_B]);
  });
});

describe('Alarm: Teilausfälle und Wiederholungen', () => {
  it('Bark antwortet 500: drei Versuche für diesen Empfänger, die anderen sind mit einem erreicht', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    const defekt = await admin.addKey('Defekt', KEY_FAIL);
    const ben = await admin.addKey('Ben', KEY_B);
    const mark = backend.bark.mark();
    const res = await alarm();

    assert.equal(res.http, 200);
    assert.equal(res.ok, false, 'nicht alle erreicht');
    assert.match(res.message, /^2\/3 Empfänger erreicht\./);
    assert.deepEqual(res.results.map((r) => [r.id, r.label, r.ok, r.http]),
      [[anna.id, 'Anna', true, 200], [defekt.id, 'Defekt', false, 500], [ben.id, 'Ben', true, 200]]);
    assert.equal(sentTo(KEY_FAIL, mark).length, 3);
    assert.equal(sentTo(KEY_A, mark).length, 1);
    assert.equal(sentTo(KEY_B, mark).length, 1);
    // Jeder Versuch ist derselbe Alarm.
    for (const attempt of sentTo(KEY_FAIL, mark)) assert.deepEqual(split(attempt).rest, alarmFields());
  });

  it('Bark antwortet 400: genau EIN Versuch - ein abgelehnter Key wird nicht wiederholt', async () => {
    await admin.addKey('Anna', KEY_A);
    const falsch = await admin.addKey('Falsch', KEY_BAD);
    const mark = backend.bark.mark();
    const res = await alarm();

    assert.equal(res.ok, false);
    assert.match(res.message, /^1\/2 Empfänger erreicht\./);
    assert.equal(sentTo(KEY_BAD, mark).length, 1);
    assert.equal(sentTo(KEY_A, mark).length, 1);
    const failed = res.results.filter((r) => !r.ok);
    assert.deepEqual(failed.map((r) => [r.id, r.label, r.http]), [[falsch.id, 'Falsch', 400]]);
  });

  it('die Verbindung bricht ab: wiederholt, dann als nicht erreicht gemeldet - mit Fehlertext', async () => {
    await admin.addKey('Anna', KEY_A);
    const weg = await admin.addKey('Weg', KEY_DROP);
    const mark = backend.bark.mark();
    const res = await alarm();

    assert.equal(res.ok, false);
    assert.equal(sentTo(KEY_DROP, mark).length, 3);
    assert.equal(sentTo(KEY_A, mark).length, 1);
    const failed = res.results.find((r) => r.id === weg.id);
    assert.equal(failed.ok, false);
    assert.equal(failed.http, 0, 'kein HTTP-Code');
    assert.equal(typeof failed.err, 'string');
    assert.notEqual(failed.err, '', 'die Ursache steht dabei');
  });

  it('scheitert nur der erste Versuch, gilt der Empfänger nach dem zweiten als erreicht', async () => {
    await admin.addKey('Anna', KEY_A);
    const ben = await admin.addKey('Ben', KEY_B);
    let first = true;
    backend.bark.respond = (req) => {
      if (req.key !== KEY_B || !first) return null;
      first = false;
      return { status: 503 };
    };
    const mark = backend.bark.mark();
    const res = await alarm();

    assert.equal(res.ok, true);
    assert.equal(sentTo(KEY_B, mark).length, 2);
    assert.equal(sentTo(KEY_A, mark).length, 1, 'wer erreicht ist, bekommt keinen zweiten Alarm');
    assert.deepEqual(res.results.find((r) => r.id === ben.id).http, 200);
  });

  it('ein langsamer Empfänger hält die anderen nicht auf', async () => {
    backend.bark.slowMs = 1500;
    const lahm = await admin.addKey('Lahm', KEY_SLOW);   // steht VOR den anderen in der Liste
    await admin.addKey('Anna', KEY_A);
    await admin.addKey('Ben', KEY_B);
    const mark = backend.bark.mark();
    const began = Date.now();
    const res = await alarm();
    const took = Date.now() - began;

    assert.equal(res.ok, true);
    assert.equal(res.results.find((r) => r.id === lahm.id).ok, true, 'auf den Langsamen wird gewartet');
    assert.ok(took >= 1400, `die Antwort kommt erst mit dem letzten Ergebnis (${took} ms)`);
    const slowAt = sentTo(KEY_SLOW, mark)[0].t;
    for (const key of [KEY_A, KEY_B]) {
      const [first, ...more] = sentTo(key, mark);
      assert.deepEqual(more, []);
      assert.ok(first.t - slowAt < 1000, `${key} kam ${first.t - slowAt} ms nach dem Langsamen an`);
    }
  });

  it('bark_max_tries 1: ein 500 wird nur einmal versucht', async () => {
    await backend.configure({ bark_max_tries: 1 });
    await admin.addKey('Anna', KEY_A);
    await admin.addKey('Defekt', KEY_FAIL);
    const mark = backend.bark.mark();
    const res = await alarm();
    assert.equal(res.ok, false);
    assert.equal(sentTo(KEY_FAIL, mark).length, 1);
    assert.equal(sentTo(KEY_A, mark).length, 1);
  });

  it('trennt der Browser die Verbindung, läuft der Versand trotzdem zu Ende', async () => {
    await admin.addKey('Anna', KEY_A);
    await admin.addKey('Defekt', KEY_FAIL);
    const { csrf } = await admin.get('status');
    const mark = backend.bark.mark();
    const gone = new AbortController();
    const call = fetch(`${backend.base}/dashboard_api.php`, {
      method: 'POST',
      headers: { 'content-type': 'application/x-www-form-urlencoded', cookie: admin.cookie, 'x-csrf-token': csrf },
      body: new URLSearchParams({ action: 'alarm', expect_demo: '0' }),
      signal: gone.signal,
    });
    // Der erste Versuch ist raus, die Wiederholungen für "Defekt" stehen noch aus.
    await waitFor(() => sentTo(KEY_FAIL, mark).length >= 1, 'erster Versuch');
    gone.abort();
    await assert.rejects(call);

    await waitFor(() => sentTo(KEY_FAIL, mark).length === 3, 'alle drei Versuche trotz getrennter Verbindung');
    assert.equal(sentTo(KEY_A, mark).length, 1);
    // Und das Ergebnis steht im Verlauf.
    const sent = await waitFor(async () => (await alarmLog()).find((e) => e.event === 'nas_alarm'), 'Ergebnis im Verlauf');
    assert.equal(sent.ok, false);
    assert.match(sent.message, /^1\/2 Empfänger erreicht\./);
  });
});

describe('Alarm: Nachsendung nur an die nicht Erreichten (only_ids)', () => {
  it('nach einem Teilausfall geht die Nachsendung nur an den nicht Erreichten - mit der Zeit des ersten Versands', async () => {
    await backend.configure({ bark_max_tries: 1 });
    await admin.addKey('Anna', KEY_A);
    const ben = await admin.addKey('Ben', KEY_B);
    backend.bark.respond = (req) => (req.key === KEY_B ? { status: 503 } : null);
    const first = await alarm();
    assert.equal(first.ok, false);
    const missed = first.results.filter((r) => !r.ok).map((r) => r.id);
    assert.deepEqual(missed, [ben.id]);

    backend.bark.respond = null;
    const mark = backend.bark.mark();
    const res = await alarm({ only_ids: missed.join(','), orig_at: first.started_at });
    assert.equal(res.http, 200);
    assert.equal(res.ok, true);
    assert.equal(res.message, 'Nachsendung: 1/1 Empfänger erreicht.');
    assert.deepEqual(res.results.map((r) => [r.id, r.label, r.ok]), [[ben.id, 'Ben', true]]);
    assert.deepEqual(keysHit(mark), [KEY_B]);
    const [sent, ...more] = sentTo(KEY_B, mark);
    assert.deepEqual(more, []);
    const { body, rest } = split(sent);
    assert.deepEqual(rest, alarmFields(), 'sonst derselbe Alarm');
    assert.equal(body, `${stamp(first.started_at)} ${ALARM_BODY}${LATE}`);
  });

  it('im Text steht die Zeit des URSPRÜNGLICHEN Versands (orig_at), nicht die der Nachsendung', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    const origAt = now() - 2 * 3600 - 120;
    const mark = backend.bark.mark();
    const res = await alarm({ only_ids: String(anna.id), orig_at: origAt });
    assert.equal(res.ok, true);
    assert.equal(fieldsOf(sentTo(KEY_A, mark)[0]).body, `${stamp(origAt)} ${ALARM_BODY}${LATE}`);
  });

  it('orig_at älter als 24 h oder in der Zukunft wird nicht übernommen', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    for (const origAt of [now() - 86400 - 600, now() + 7200]) {
      const mark = backend.bark.mark();
      const from = now();
      const res = await alarm({ only_ids: String(anna.id), orig_at: origAt });
      assert.equal(res.ok, true);
      const { body } = fieldsOf(sentTo(KEY_A, mark)[0]);
      // Ohne brauchbare Zeit (und ohne früheren Versand im Verlauf) gilt jetzt.
      assertStamped(body, from, `${ALARM_BODY}${LATE}`);
    }
  });

  it('ohne orig_at gilt die Zeit des letzten Versands aus dem Verlauf', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    const first = await alarm();
    ageLogEntries(2 * 3600 + 120, (e) => e.event.startsWith('nas_alarm'));
    const mark = backend.bark.mark();
    await alarm({ only_ids: String(anna.id) });
    // Eine weitere Nachsendung ändert daran nichts: Maßgeblich ist der Versand, nicht die Nachsendung davor.
    await alarm({ only_ids: String(anna.id) });
    const bodies = sentTo(KEY_A, mark).map((r) => fieldsOf(r).body);
    const expected = `${stamp(first.started_at - 2 * 3600 - 120)} ${ALARM_BODY}${LATE}`;
    assert.equal(bodies.length, 2);
    // Der Verlaufseintrag entsteht kurz nach started_at - über eine Minutengrenze hinweg gilt die nächste.
    const alt = `${stamp(first.started_at - 2 * 3600 - 120 + 5)} ${ALARM_BODY}${LATE}`;
    for (const body of bodies) assert.ok([expected, alt].includes(body), `Text "${body}" - erwartet "${expected}"`);
  });

  it('... aber nur, wenn dieser Versand höchstens 6 h her ist - sonst gilt jetzt', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    await alarm();
    ageLogEntries(6 * 3600 + 600, (e) => e.event.startsWith('nas_alarm'));
    const mark = backend.bark.mark();
    const from = now();
    await alarm({ only_ids: String(anna.id) });
    assertStamped(fieldsOf(sentTo(KEY_A, mark)[0]).body, from, `${ALARM_BODY}${LATE}`);
  });

  it('die Auswahl kann die Liste nur verkleinern: eine unbekannte Nummer wird übergangen', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    await admin.addKey('Ben', KEY_B);
    const mark = backend.bark.mark();
    const res = await alarm({ only_ids: `${anna.id},999` });
    assert.equal(res.ok, true);
    assert.deepEqual(res.results.map((r) => r.id), [anna.id]);
    assert.deepEqual(keysHit(mark), [KEY_A]);
    assert.equal(backend.bark.since(mark).length, 1);
  });

  it('mehrere Nummern: genau diese Empfänger, jeder einmal', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    await admin.addKey('Ben', KEY_B);
    const carla = await admin.addKey('Carla', KEY_C);
    const mark = backend.bark.mark();
    const res = await alarm({ only_ids: `${carla.id},${anna.id}` });
    assert.equal(res.ok, true);
    assert.deepEqual(keysHit(mark), [KEY_A, KEY_C].sort());
    assert.equal(backend.bark.since(mark).length, 2);
  });

  it('ein stummer Empfänger bekommt auch die Nachsendung mit seiner Stumm-Lautstärke', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    await mute(anna);
    const mark = backend.bark.mark();
    await alarm({ only_ids: String(anna.id) });
    assert.deepEqual(split(sentTo(KEY_A, mark)[0]).rest, alarmFields({ volume: '0', call: false }));
  });

  it('Demo-Modus: eine Auswahl, die einen ANDEREN nennt, sendet NICHTS - nie ersatzweise an alle', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    const ben = await admin.addKey('Ben', KEY_B);
    await demoOn(anna);
    let mark = backend.bark.mark();
    const res = await alarm({ expect_demo: String(anna.id), only_ids: String(ben.id) });
    assert.equal(res.http, 200);
    assert.equal(res.ok, false);
    assert.equal(res.message, RESEND_EMPTY);
    assert.deepEqual(res.results, []);
    assert.equal(res.demo, true);
    await assertNothingSent(mark);

    // Nennt die Auswahl den Test-Empfänger (auch neben anderen), bekommt nur er sie.
    mark = backend.bark.mark();
    const again = await alarm({ expect_demo: String(anna.id), only_ids: `${ben.id},${anna.id}` });
    assert.equal(again.ok, true);
    assert.deepEqual(keysHit(mark), [KEY_A]);
  });

  it('only_ids vorhanden, aber unbrauchbar: 400, nichts gesendet', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    await admin.addKey('Ben', KEY_B);
    const mark = backend.bark.mark();
    for (const bad of ['abc', '', '1,,x', `${anna.id},`, '0', '-1', '1.5', `${anna.id};2`, ' ']) {
      const res = await alarm({ only_ids: bad });
      assert.equal(res.http, 400, `only_ids="${bad}"`);
      assert.equal(res.ok, false);
      assert.match(res.message, /NICHTS gesendet/);
    }
    await assertNothingSent(mark);
    assert.deepEqual(await alarmLog(), [], 'kein Versand im Verlauf');
  });

  it('nur unbekannte Nummern: nichts gesendet, die Antwort erklärt es', async () => {
    await admin.addKey('Anna', KEY_A);
    const mark = backend.bark.mark();
    const res = await alarm({ only_ids: '998,999' });
    assert.equal(res.http, 200);
    assert.equal(res.ok, false);
    assert.equal(res.message, RESEND_EMPTY);
    assert.deepEqual(res.results, []);
    await assertNothingSent(mark);
  });

  it('ein inzwischen gelöschter Empfänger bekommt keine Nachsendung', async () => {
    await admin.addKey('Anna', KEY_A);
    const ben = await admin.addKey('Ben', KEY_B);
    await ok(admin.post({ action: 'keys_delete', id: ben.id }));
    const mark = backend.bark.mark();
    const res = await alarm({ only_ids: String(ben.id) });
    assert.equal(res.ok, false);
    assert.equal(res.message, RESEND_EMPTY);
    await assertNothingSent(mark);
  });

  it('leere Dashboard-Liste: eine Nachsendung geht nicht ersatzweise an die Not-Liste', async () => {
    const mark = backend.bark.mark();
    const res = await alarm({ only_ids: '1' });
    assert.equal(res.ok, false);
    assert.equal(res.message, RESEND_EMPTY);
    await assertNothingSent(mark);
  });
});

describe('Entwarnung', () => {
  it('geht an dieselben Empfänger: Critical Alert mit kurzem Ton, ohne call, Texte in ASCII', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    const ben = await admin.addKey('Ben', KEY_B);
    const mark = backend.bark.mark();
    const res = await entwarnung();

    assert.equal(res.http, 200);
    assert.equal(res.ok, true);
    assert.equal(res.demo, false);
    assert.equal(res.message, 'Entwarnung an 2/2 Empfänger zugestellt.');
    assert.ok(Math.abs(res.started_at - now()) <= 5);
    assert.deepEqual(res.results, [
      { id: anna.id, key: 'anna...', ok: true, http: 200, err: '', muted: false, label: 'Anna' },
      { id: ben.id, key: 'benk...', ok: true, http: 200, err: '', muted: false, label: 'Ben' },
    ]);
    for (const key of [KEY_A, KEY_B]) {
      const sent = assertClear(key, mark, res);
      assert.equal('call' in sent.fields, false);
      assert.match(sent.fields.title, /^[\x20-\x7E]+$/);
      assert.match(sent.fields.body, /^[\x20-\x7E]+$/);
    }
    assert.equal(backend.bark.since(mark).length, 2);
  });

  it('Ton und Lautstärke kommen aus der Konfiguration', async () => {
    await backend.configure({ bark_false_alarm_sound: 'bell', bark_false_alarm_volume: 6 });
    await admin.addKey('Anna', KEY_A);
    const mark = backend.bark.mark();
    const res = await entwarnung();
    assertClear(KEY_A, mark, res, clearFields({ sound: 'bell', volume: '6' }));
  });

  it('ohne eigene Lautstärke gilt die des Alarms', async () => {
    await backend.configure({ bark_false_alarm_volume: null, bark_alarm_volume: 7 });
    await admin.addKey('Anna', KEY_A);
    const mark = backend.bark.mark();
    const res = await entwarnung();
    assertClear(KEY_A, mark, res, clearFields({ volume: '7' }));
  });

  it('bark_alarm_call ändert nichts: die Entwarnung hat nie call', async () => {
    await backend.configure({ bark_alarm_call: true });
    await admin.addKey('Anna', KEY_A);
    const mark = backend.bark.mark();
    const res = await entwarnung();
    assertClear(KEY_A, mark, res);
  });

  it('ein stummer Empfänger bekommt sie mit seiner Stumm-Lautstärke', async () => {
    await admin.addKey('Anna', KEY_A);
    const ben = await admin.addKey('Ben', KEY_B);
    await mute(ben);
    await muteVolume(ben, 4);
    const mark = backend.bark.mark();
    const res = await entwarnung();
    assertClear(KEY_A, mark, res);
    assertClear(KEY_B, mark, res, clearFields({ volume: '4' }));
    assert.match(res.message, /1 davon stumm/);
  });

  it('eine überfällige Stummschaltung zählt auch hier als laut', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    await mute(anna);
    muteSince(anna, MUTE_MAX + 100);
    const mark = backend.bark.mark();
    const res = await entwarnung();
    const critical = sentTo(KEY_A, mark).filter((r) => r.fields.level === 'critical');
    assert.equal(critical.length, 1);
    assert.deepEqual(split(critical[0]).rest, clearFields());
    assert.equal(res.results[0].muted, false);
    await waitFor(() => sentTo(KEY_A, mark).find((r) => r.fields.title === 'Alarmton wieder LAUT'), 'Info "wieder laut"');
    assert.equal(listEntry(anna.id).muted, false);
  });

  it('leere Dashboard-Liste: die Not-Liste, mit Hinweis in der Antwort', async () => {
    const mark = backend.bark.mark();
    const res = await entwarnung();
    assert.equal(res.ok, true);
    assertClear(NOT_KEY, mark, res);
    assert.deepEqual(keysHit(mark), [NOT_KEY]);
    assert.match(res.message, /Fallback-Liste aus config\.php verwendet/);
  });

  it('Demo-Modus: nur der Test-Empfänger', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    await admin.addKey('Ben', KEY_B);
    await demoOn(anna);
    const mark = backend.bark.mark();
    const res = await entwarnung({ expect_demo: String(anna.id) });
    assert.equal(res.ok, true);
    assert.equal(res.demo, true);
    assert.match(res.message, /^DEMO-MODUS: Entwarnung nur an den Test-Empfänger\. Entwarnung an 1\/1 Empfänger zugestellt\./);
    assertClear(KEY_A, mark, res);
    assert.deepEqual(keysHit(mark), [KEY_A]);
  });

  it('der bestätigte Modus gilt wie beim Alarm: fehlt er oder passt er nicht, 409 und nichts gesendet', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    const ben = await admin.addKey('Ben', KEY_B);
    const mark = backend.bark.mark();
    const refused = [
      await entwarnung({ expect_demo: undefined }),
      await entwarnung({ expect_demo: String(anna.id) }),
    ];
    await demoOn(anna);
    refused.push(await entwarnung({ expect_demo: '0' }));
    refused.push(await entwarnung({ expect_demo: String(ben.id) }));
    for (const res of refused) {
      assert.equal(res.http, 409);
      assert.equal(res.ok, false);
      assert.equal(res.code, 'mode_changed');
    }
    await assertNothingSent(mark);
    assert.deepEqual(await alarmLog(), []);
  });

  it('Nachsendung: nur an die Genannten, mit der Zeit der ersten Entwarnung und dem Zusatz', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    await admin.addKey('Ben', KEY_B);
    const origAt = now() - 1800;
    const mark = backend.bark.mark();
    const res = await entwarnung({ only_ids: `${anna.id},999`, orig_at: origAt });
    assert.equal(res.ok, true);
    assert.equal(res.message, 'Nachsendung: Entwarnung an 1/1 Empfänger zugestellt.');
    assert.deepEqual(keysHit(mark), [KEY_A]);
    const [sent, ...more] = sentTo(KEY_A, mark);
    assert.deepEqual(more, []);
    const { body, rest } = split(sent);
    assert.deepEqual(rest, clearFields());
    assert.equal(body, `${stamp(origAt)} ${CLEAR_BODY}${LATE}`);
  });

  it('Nachsendung ohne orig_at nimmt die Zeit der letzten ENTWARNUNG, nicht die eines Alarms', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    await alarm();
    ageLogEntries(3600, (e) => e.event.startsWith('nas_alarm'));
    const mark = backend.bark.mark();
    const from = now();
    await entwarnung({ only_ids: String(anna.id) });
    assertStamped(fieldsOf(sentTo(KEY_A, mark)[0]).body, from, `${CLEAR_BODY}${LATE}`);
  });

  it('Nachsendung: unbrauchbare Auswahl 400, unbekannte Nummern senden nichts', async () => {
    await admin.addKey('Anna', KEY_A);
    const mark = backend.bark.mark();
    for (const bad of ['abc', '', '1,,x']) {
      const res = await entwarnung({ only_ids: bad });
      assert.equal(res.http, 400, `only_ids="${bad}"`);
      assert.equal(res.ok, false);
    }
    const none = await entwarnung({ only_ids: '999' });
    assert.equal(none.http, 200);
    assert.equal(none.ok, false);
    assert.equal(none.message, RESEND_EMPTY);
    await assertNothingSent(mark);
  });

  it('Teilausfall: wie beim Alarm pro Empfänger gemeldet', async () => {
    await backend.configure({ bark_max_tries: 1 });
    await admin.addKey('Anna', KEY_A);
    const defekt = await admin.addKey('Defekt', KEY_FAIL);
    const res = await entwarnung();
    assert.equal(res.ok, false);
    assert.match(res.message, /^Entwarnung an 1\/2 Empfänger zugestellt\./);
    assert.deepEqual(res.results.filter((r) => !r.ok).map((r) => [r.id, r.http]), [[defekt.id, 500]]);
  });
});

describe('Verlauf', () => {
  it('ein Alarm schreibt "gestartet" davor und das Ergebnis danach - im LIVE-Betrieb mit demo: false', async () => {
    await admin.addKey('Anna', KEY_A);
    const res = await alarm();
    const entries = await admin.log('alarm');
    assert.deepEqual(entries.map(({ at, ...rest }) => rest), [
      { event: 'nas_alarm', by: 'admin', ok: true, message: res.message, resend: 0, demo: false },
      { event: 'nas_alarm_started', by: 'admin', resend: 0, demo: false },
    ]);
    for (const e of entries) assert.ok(Math.abs(e.at - res.started_at) <= 5);
    // Auch "Letzter Alarm" auf der Übersicht (nur Versand-Ereignisse) zeigt beide.
    assert.deepEqual((await admin.log('alarm_core')).map((e) => e.event), ['nas_alarm', 'nas_alarm_started']);
  });

  it('im Demo-Modus tragen beide Einträge demo: true', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    await admin.addKey('Ben', KEY_B);
    await demoOn(anna);
    const res = await alarm({ expect_demo: String(anna.id) });
    assert.deepEqual(await alarmLog(), [
      { event: 'nas_alarm', by: 'admin', ok: true, message: res.message, resend: 0, demo: true },
      { event: 'nas_alarm_started', by: 'admin', resend: 0, demo: true },
    ]);
  });

  it('eine Nachsendung trägt resend = Anzahl der Empfänger', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    const ben = await admin.addKey('Ben', KEY_B);
    await admin.addKey('Carla', KEY_C);
    const res = await alarm({ only_ids: `${anna.id},${ben.id}` });
    assert.deepEqual(await alarmLog(), [
      { event: 'nas_alarm', by: 'admin', ok: true, message: res.message, resend: 2, demo: false },
      { event: 'nas_alarm_started', by: 'admin', resend: 2, demo: false },
    ]);
  });

  it('ein Teilausfall steht mit ok: false und der Zusammenfassung im Verlauf', async () => {
    await backend.configure({ bark_max_tries: 1 });
    await admin.addKey('Anna', KEY_A);
    await admin.addKey('Defekt', KEY_FAIL);
    const res = await alarm();
    const [sent] = await alarmLog();
    assert.deepEqual(sent, { event: 'nas_alarm', by: 'admin', ok: false, message: res.message, resend: 0, demo: false });
    assert.match(sent.message, /^1\/2 Empfänger erreicht\./);
  });

  it('eine Entwarnung schreibt false_alarm_started und false_alarm', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    const res = await entwarnung();
    const resend = await entwarnung({ only_ids: String(anna.id) });
    assert.deepEqual(await alarmLog(), [
      { event: 'false_alarm', by: 'admin', ok: true, message: resend.message, resend: 1, demo: false },
      { event: 'false_alarm_started', by: 'admin', resend: 1, demo: false },
      { event: 'false_alarm', by: 'admin', ok: true, message: res.message, resend: 0, demo: false },
      { event: 'false_alarm_started', by: 'admin', resend: 0, demo: false },
    ]);
  });

  it('auch der Lese-Benutzer sieht die Alarme im Verlauf', async () => {
    await admin.addKey('Anna', KEY_A);
    await alarm();
    const reader = await session(backend, READER);
    assert.deepEqual((await reader.log('alarm')).map((e) => e.event), ['nas_alarm', 'nas_alarm_started']);
  });

  it('nirgends im Verlauf steht ein Bark-Key im Klartext', async () => {
    await backend.configure({ bark_max_tries: 1 });
    const anna = await admin.addKey('Anna', KEY_A);
    const defekt = await admin.addKey('Defekt', KEY_FAIL);
    await admin.addKey('Ben', KEY_B);
    await alarm();
    await entwarnung();
    await alarm({ only_ids: String(defekt.id) });
    await testPush(anna.id);
    await testPush(defekt.id);
    await demoOn(anna);
    await alarm({ expect_demo: String(anna.id) });

    const raw = backend.data.log();
    const text = JSON.stringify(raw) + JSON.stringify(await admin.log('all'));
    for (const key of [KEY_A, KEY_B, KEY_FAIL, STATUS_KEY, NOT_KEY]) {
      assert.equal(text.includes(key), false, `Key ${key.slice(0, 4)}... im Verlauf`);
    }
    // Pro Empfänger steht das Ergebnis im gespeicherten Verlauf - der Key nur maskiert.
    const sent = raw.find((e) => e.event === 'nas_alarm');
    assert.deepEqual(sent.results.map((r) => [r.key, r.label, r.ok]),
      [['anna...', 'Anna', true], ['fail...', 'Defekt', false], ['benk...', 'Ben', true]]);
  });
});

describe('Zugang', () => {
  const url = () => `${backend.base}/dashboard_api.php`;
  const parsed = (res) => ({ ...JSON.parse(res.text), http: res.status });
  // Die drei Aktionen so, wie sie sonst senden würden.
  const sending = (id) => [
    { action: 'alarm', expect_demo: '0' },
    { action: 'false_alarm', expect_demo: '0' },
    { action: 'keys_test', id },
  ];

  it('per GET: 405, nichts gesendet', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    const mark = backend.bark.mark();
    for (const { action, ...params } of sending(anna.id)) {
      const res = await admin.get(action, params);
      assert.equal(res.http, 405, action);
      assert.equal(res.ok, false);
    }
    await assertNothingSent(mark);
  });

  it('ohne oder mit falschem CSRF-Token: 403 csrf, nichts gesendet', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    const mark = backend.bark.mark();
    for (const fields of sending(anna.id)) {
      for (const headers of [{ cookie: admin.cookie }, { cookie: admin.cookie, 'x-csrf-token': 'falsch' }]) {
        const res = parsed(await postForm(url(), fields, headers));
        assert.equal(res.http, 403, fields.action);
        assert.equal(res.ok, false);
        assert.equal(res.code, 'csrf');
      }
    }
    await assertNothingSent(mark);
  });

  it('als Lese-Benutzer: 403 readonly, nichts gesendet', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    const reader = await session(backend, READER);
    const mark = backend.bark.mark();
    for (const fields of sending(anna.id)) {
      const res = await reader.post(fields);
      assert.equal(res.http, 403, fields.action);
      assert.equal(res.ok, false);
      assert.equal(res.code, 'readonly');
    }
    // Ohne Token scheitert er schon davor - die Reihenfolge ist Methode, Token, Rolle.
    const noToken = parsed(await postForm(url(), { action: 'alarm', expect_demo: '0' }, { cookie: reader.cookie }));
    assert.equal(noToken.http, 403);
    assert.equal(noToken.code, 'csrf');
    await assertNothingSent(mark);
  });

  it('ohne Sitzung: 401, nichts gesendet', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    const mark = backend.bark.mark();
    for (const fields of sending(anna.id)) {
      for (const headers of [{}, { cookie: 'boss925_dashboard=gibtesnicht' }]) {
        const res = parsed(await postForm(url(), fields, headers));
        assert.equal(res.http, 401, fields.action);
        assert.equal(res.ok, false);
      }
    }
    await assertNothingSent(mark);
  });

  it('nach keinem der abgewiesenen Aufrufe steht ein Versand im Verlauf', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    const reader = await session(backend, READER);
    for (const fields of sending(anna.id)) {
      await admin.get(fields.action, fields);
      await postForm(url(), fields, { cookie: admin.cookie });
      await reader.post(fields);
      await postForm(url(), fields);
    }
    assert.deepEqual(await alarmLog(), []);
    assert.deepEqual((await admin.log('keys')).filter((e) => e.event === 'key_test'), []);
  });
});

describe('Test-Push an einen Empfänger (keys_test)', () => {
  it('EINE normale Mitteilung mit dem Alarmton nur an diesen Empfänger - nicht kritisch, ohne call', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    await admin.addKey('Ben', KEY_B);
    const versionBefore = (await admin.keys()).version;
    const mark = backend.bark.mark();
    const from = now();
    const res = await testPush(anna.id);

    assert.equal(res.http, 200);
    assert.equal(res.ok, true);
    assert.match(res.message, /^Test-Push an „Anna“ gesendet/);
    assert.equal(JSON.stringify(res).includes(KEY_A), false);
    await settle();
    assert.deepEqual(keysHit(mark), [KEY_A]);
    const [sent, ...more] = sentTo(KEY_A, mark);
    assert.deepEqual(more, []);
    const { body, rest } = split(sent);
    assert.deepEqual(rest, { title: 'TEST - kein Alarm', sound: 'alarm_fw', level: 'active' });
    assertStamped(body, from, 'Test-Nachricht vom Alarm-Waechter. '
      + 'Hoerst du dabei den Alarmton, ist dein iPhone richtig eingerichtet.');

    const [entry] = (await admin.log('keys')).filter((e) => e.event === 'key_test');
    assert.deepEqual({ ...entry, at: 0 }, { at: 0, event: 'key_test', by: 'admin', label: 'Anna', ok: true });
    // Die Liste bleibt, wie sie ist - die Box muss nichts neu holen.
    assert.equal((await admin.keys()).version, versionBefore);
  });

  it('der Ton ist der konfigurierte Alarmton', async () => {
    await backend.configure({ bark_alarm_sound: 'sirene' });
    const anna = await admin.addKey('Anna', KEY_A);
    const mark = backend.bark.mark();
    await ok(testPush(anna.id));
    assert.equal(sentTo(KEY_A, mark)[0].fields.sound, 'sirene');
  });

  it('geht auch an einen stummen Empfänger und im Demo-Modus an den Gewählten', async () => {
    const anna = await admin.addKey('Anna', KEY_A);
    const ben = await admin.addKey('Ben', KEY_B);
    await mute(ben);
    await demoOn(anna);
    const mark = backend.bark.mark();
    await ok(testPush(ben.id));
    assert.deepEqual(keysHit(mark), [KEY_B]);
    assert.deepEqual(split(sentTo(KEY_B, mark)[0]).rest, { title: 'TEST - kein Alarm', sound: 'alarm_fw', level: 'active' });
  });

  it('nimmt Bark ihn nicht an: Antwort nicht ok, im Verlauf mit ok: false', async () => {
    const defekt = await admin.addKey('Defekt', KEY_FAIL);
    await admin.addKey('Anna', KEY_A);
    const mark = backend.bark.mark();
    const res = await testPush(defekt.id);

    assert.equal(res.ok, false);
    assert.match(res.message, /^Test-Push an „Defekt“ FEHLGESCHLAGEN \(HTTP 500\)/);
    assert.deepEqual(keysHit(mark), [KEY_FAIL]);
    for (const attempt of sentTo(KEY_FAIL, mark)) assert.equal(attempt.fields.level, 'active');
    const [entry] = (await admin.log('keys')).filter((e) => e.event === 'key_test');
    assert.deepEqual({ ...entry, at: 0 }, { at: 0, event: 'key_test', by: 'admin', label: 'Defekt', ok: false });
  });

  it('lehnt Bark den Key ab (400): Hinweis, den Key zu prüfen', async () => {
    const falsch = await admin.addKey('Falsch', KEY_BAD);
    const mark = backend.bark.mark();
    const res = await testPush(falsch.id);
    assert.equal(res.ok, false);
    assert.match(res.message, /FEHLGESCHLAGEN \(HTTP 400 – Bark-Key prüfen\)/);
    assert.equal(sentTo(KEY_BAD, mark).length, 1, 'ein abgelehnter Key wird nicht wiederholt');
  });

  it('unbekannte Nummer: nicht ok, nichts gesendet', async () => {
    await admin.addKey('Anna', KEY_A);
    const mark = backend.bark.mark();
    for (const id of [999, 0, '', 'abc']) {
      const res = await testPush(id);
      assert.equal(res.ok, false, `id="${id}"`);
      assert.equal(res.message, 'Empfänger nicht gefunden.');
    }
    await assertNothingSent(mark);
    assert.deepEqual((await admin.log('keys')).filter((e) => e.event === 'key_test'), []);
  });
});
