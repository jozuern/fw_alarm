import test from 'node:test';
import assert from 'node:assert/strict';
import { loadScripts, plain } from './helpers.mjs';

const L = loadScripts(['nas_dashboard/assets/app/logic.js']).FW.logic;

test('alarm info with clearing', () => {
  const e = [{ event: 'false_alarm', at: 200, ok: true, demo: false }, { event: 'nas_alarm', at: 100, ok: false, demo: false }];
  assert.deepEqual(plain(L.deriveAlarmInfo(e)), { kind: 'manual', at: 100, ok: false, resent: null, clearedAt: 200, clearedOk: true });
  assert.equal(L.deriveAlarmInfo([{ event: 'false_alarm', at: 200, ok: true, demo: true }, { event: 'relay_alarm', at: 100 }]).clearedAt, undefined);
  assert.equal(L.deriveAlarmInfo([{ event: 'nas_alarm', at: 1, message: 'DEMO-MODUS: …' }]).kind, 'manual_demo');
});

test('alarm info: resend, started, empty', () => {
  const r = L.deriveAlarmInfo([{ event: 'nas_alarm', at: 300, resend: 2, ok: true }, { event: 'nas_alarm', at: 100, ok: false }]);
  assert.equal(r.at, 100); assert.equal(r.resent, true);
  assert.equal(L.deriveAlarmInfo([{ event: 'nas_alarm_started', at: 5, demo: true }]).kind, 'manual_demo_open');
  assert.equal(L.deriveAlarmInfo([{ event: 'nas_alarm_started', at: 5 }]).kind, 'manual_open');
  assert.equal(L.deriveAlarmInfo([{ event: 'relay_alarm', at: 5, source: 'remote' }]).kind, 'demo_box');
  assert.equal(L.deriveAlarmInfo([{ event: 'box_offline', at: 5 }]), null);
  const c = L.deriveAlarmInfo([{ event: 'false_alarm', at: 9, ok: true, resend: 1 }, { event: 'false_alarm', at: 8, ok: false, demo: false }, { event: 'relay_alarm', at: 1 }]);
  assert.equal(c.clearedAt, 8); assert.equal(c.clearedOk, true);
});

test('alarm fact', () => {
  assert.deepEqual(plain(L.alarmFact(undefined, null, 0)), { text: '…', tone: '' });
  assert.equal(L.alarmFact(null, { last_alarm: 'nie' }, 0).text, 'keiner aufgezeichnet');
  assert.equal(L.alarmFact(null, { last_alarm: '[13.11. 21:42] x' }, 0).text, '[13.11. 21:42] x');
  const now = 1800000000;
  const f = L.alarmFact({ kind: 'relay', at: now - 600 }, null, now);
  assert.equal(f.tone, 'crit'); assert.match(f.text, /^vor 10 min \(.*Uhr\), Relaisalarm \(Melder\)$/);
  const g = L.alarmFact({ kind: 'manual', at: now - 600, ok: false, resent: true, clearedAt: now - 300, clearedOk: true }, null, now);
  assert.equal(g.tone, 'warn'); assert.match(g.text, /nicht alle erreicht, Nachsendung zugestellt\. Entwarnung um .* Uhr gesendet/);
  const h = L.alarmFact({ kind: 'relay', at: now - 600, clearedAt: now - 300, clearedOk: false }, null, now);
  assert.equal(h.tone, 'crit'); assert.match(h.text, /NICHT alle erreicht$/);
  assert.equal(L.alarmFact({ kind: 'relay', at: now - 90000 }, null, now).tone, '');
});

test('unreached merge on resend', () => {
  const prev = { action: 'alarm', what: 'REAL ALARM', at: 1, startedAt: 50, entries: [{ id: 3, name: 'Tom', reason: 'HTTP 500' }, { id: 0, name: 'Notliste', reason: 'HTTP 0' }] };
  const r = L.buildUnreached(prev, 'alarm', { ok: true, results: [{ id: 3, label: 'Tom', ok: true }] }, { ids: [3], origAt: 50 }, 1000);
  assert.deepEqual(plain(r.unreached.entries), [{ id: 0, name: 'Notliste', reason: 'HTTP 0' }]);
  assert.equal(r.unreached.startedAt, 50);
  assert.deepEqual(plain(r.reached), ['Tom']);
  const none = L.buildUnreached(prev, 'alarm', { ok: false, results: [] }, { ids: [9], origAt: 0 }, 1000);
  assert.equal(none.unreached, prev);
});

test('unreached: first send, renewed failure, all reached', () => {
  const first = L.buildUnreached(null, 'false_alarm', { started_at: 77, results: [{ id: 2, label: 'A', ok: false, http: 0 }, { id: 4, key: 'k', ok: true }] }, null, 5);
  assert.equal(first.unreached.what, 'ENTWARNUNG'); assert.equal(first.unreached.startedAt, 77);
  assert.deepEqual(plain(first.unreached.entries), [{ id: 2, name: 'A', reason: 'keine Verbindung/Timeout' }]);
  assert.deepEqual(plain(first.failed), [{ id: 2, name: 'A', reason: 'keine Verbindung/Timeout' }]);
  const again = L.buildUnreached(first.unreached, 'false_alarm', { results: [{ id: 2, label: 'A', ok: false, http: 500 }] }, { ids: [2], origAt: 0 }, 9);
  assert.equal(again.unreached.startedAt, 77);
  assert.equal(again.unreached.entries[0].reason, 'HTTP 500');
  const done = L.buildUnreached(first.unreached, 'false_alarm', { results: [{ id: 2, label: 'A', ok: true }] }, { ids: [2], origAt: 77 }, 9);
  assert.equal(done.unreached, null);
  const other = L.buildUnreached(first.unreached, 'alarm', { results: [{ id: 5, label: 'B', ok: false, http: 400 }] }, { ids: [5], origAt: 0 }, 9);
  assert.deepEqual(plain(other.unreached.entries.map((e) => e.id)), [5]);
});

test('validateUnreached', () => {
  const ok = JSON.stringify({ action: 'alarm', at: 1000, startedAt: 0, entries: [{ id: 1, name: 'A', reason: 'x' }] });
  assert.ok(L.validateUnreached(ok, 2000));
  assert.equal(L.validateUnreached(ok, 1000 + 43200001), null);
  assert.equal(L.validateUnreached('{"action":"x"}', 2000), null);
  assert.equal(L.validateUnreached('nonsense', 2000), null);
  assert.equal(L.validateUnreached(null, 2000), null);
  assert.equal(L.validateUnreached(JSON.stringify({ action: 'alarm', at: 1000, entries: [{ id: -1, name: 'A', reason: 'x' }] }), 2000), null);
  assert.equal(L.validateUnreached(ok, 2000).what, 'REAL ALARM');
});

test('command line', () => {
  assert.equal(L.commandLine({ id: 4, type: 'TEST', state: 'pending' }, 100).canCancel, true);
  assert.equal(L.commandLine({ id: 4, type: 'TEST', state: 'acked', acked_at: 100, ack_result: 'executed' }, 801), null);
  assert.equal(L.commandLine({ id: 4, type: 'ALARM', state: 'acked', acked_at: 100, ack_result: 'skipped_busy' }, 200).tone, 'warn');
  assert.match(L.commandLine({ id: 4, type: 'TEST', state: 'delivered', delivered_at: 100 }, 701).text, /Bestätigung nicht angekommen/);
  assert.equal(L.commandLine(null, 1), null);
  const a = L.commandLine({ id: 4, type: 'TEST', state: 'acked', acked_at: 100, ack_result: 'executed', ack_message: 'test status sent via Bark' }, 200);
  assert.equal(a.tone, 'done'); assert.equal(a.canCancel, false); assert.match(a.text, /ausgeführt/);
  assert.equal(L.commandLine({ id: 4, type: 'TEST', state: 'delivered', delivered_at: 100 }, 200).tone, '');
  assert.equal(L.commandLine({ id: 5, type: 'ALARM', state: 'cancelled', cancelled_at: 10, cancel_reason: 'Demo-Modus geändert' }, 20).tone, 'warn');
  assert.equal(L.commandLine({ id: 5, type: 'ALARM', state: 'expired', expired_at: 10 }, 20).tone, 'warn');
});

test('confirmation texts name mode and muted volumes', () => {
  const keys = [{ id: 1, label: 'Josh', muted: false }, { id: 2, label: 'Anna', muted: true, mute_volume: 3 }];
  const q = L.alarmQuestion('alarm', { enabled: false }, keys);
  assert.match(q.text, /2 Empfänger/); assert.match(q.text, /Anna \(stumm\) mit Lautstärke 3/);
  assert.match(L.alarmQuestion('alarm', { enabled: true, label: 'Anna', key_id: 2 }, keys).title, /Demo/);
  assert.equal(L.armTimeoutFor('kurz'), 10); assert.equal(L.armTimeoutFor('w '.repeat(80)), 25);
});

test('confirmation variants', () => {
  const keys = [{ id: 1, label: 'Josh', muted: false }, { id: 2, label: 'Anna', muted: true, mute_volume: 0 }];
  assert.match(L.alarmQuestion('alarm', { enabled: false }, keys).text, /LIVE/);
  assert.match(L.alarmQuestion('alarm', { enabled: false }, keys).text, /Anna \(stumm\) lautlos/);
  const d = L.alarmQuestion('alarm', { enabled: true, label: 'Josh', key_id: 1 }, keys);
  assert.match(d.text, /DEMO-MODUS: nur/); assert.match(d.text, /1 Empfänger/); assert.doesNotMatch(d.text, /Anna/);
  assert.match(L.alarmQuestion('false_alarm', { enabled: false }, keys).title, /Entwarnung/);
  assert.match(L.alarmQuestion('box_alarm', { enabled: true, label: 'Josh', key_id: 1 }, keys).text, /5-Minuten-Cooldown/);
  const r = L.alarmQuestion('resend', { enabled: false }, keys, { names: ['Tom', 'Eva'], what: 'REAL ALARM' });
  assert.match(r.text, /LIVE/); assert.match(r.text, /NUR an: Tom, Eva/);
  assert.equal(L.expectDemoOf({ enabled: true, key_id: 7 }), '7'); assert.equal(L.expectDemoOf({ enabled: false }), '0'); assert.equal(L.expectDemoOf(null), '0');
  assert.equal(L.volWords(0), 'lautlos'); assert.equal(L.volWords(4), 'leise (Lautstärke 4 von 10)');
  assert.match(L.muteQuestion('Anna', 'muted', 0), /OHNE Ton/);
  assert.match(L.muteQuestion('Anna', 'permanent', 3), /DAUERHAFT.*Stumm-Lautstärke 3 von 10/);
});

test('recipient view', () => {
  const r = L.recipientView({ id: 2, label: 'Anna', muted: true, mute_volume: 3, muted_at: 1000, mute_until: 44200 }, { enabled: false }, 2000);
  assert.equal(r.state, 'muted'); assert.equal(r.pill.text, 'Stumm'); assert.match(r.sub, /Lautstärke 3/);
  assert.equal(L.recipientView({ id: 2, label: 'A', muted: true, mute_permanent: true }, { enabled: false }, 0).pill.tone, 'crit');
  assert.equal(L.recipientView({ id: 1, label: 'J', muted: false }, { enabled: true, key_id: 2 }, 0).paused, true);
  const p = L.recipientView({ id: 2, label: 'A', muted: true, mute_permanent: true }, { enabled: false }, 0);
  assert.equal(p.pill.text, 'Dauerhaft'); assert.match(p.sub, /keine automatische Rückschaltung/);
  const o = L.recipientView({ id: 2, label: 'A', muted: false, mute_overdue: true, mute_volume: 0 }, { enabled: false }, 0);
  assert.equal(o.subTone, 'warn'); assert.match(o.sub, /nicht gespeichert/);
  assert.equal(L.recipientView({ id: 2, label: 'A', muted: false }, { enabled: true, key_id: 2 }, 0).isDemoTarget, true);
  assert.equal(L.recipientView({ id: 2, label: 'A', muted: false }, { enabled: false }, 0).pill.text, 'Laut');
});

// Rückschaltung fällig, aber nicht gespeichert: Das NAS rechnet „laut“
// (muted=false), die Box alarmiert evtl. noch mit der Stumm-Lautstärke - das
// darf nie wie ein grünes „Laut“ aussehen.
test('fix round: überfällige Stummschaltung zeigt orange „Evtl. stumm“, nie grün', () => {
  const o = L.recipientView({ id: 2, label: 'A', muted: false, mute_overdue: true, mute_volume: 4 }, { enabled: false }, 0);
  assert.equal(o.state, 'loud');   // wirksamer Zustand des NAS
  assert.equal(o.pill.text, 'Evtl. stumm');
  assert.equal(o.pill.tone, 'warn');
  assert.equal(o.overdue, true);
  assert.equal(L.recipientView({ id: 2, label: 'A', muted: false }, { enabled: false }, 0).overdue, false);
});

test('fix round: resend question names only muted re-send targets', () => {
  const keys = [{ id: 1, label: 'Tom', muted: true, mute_volume: 2 }, { id: 2, label: 'Anna', muted: true, mute_volume: 3 }, { id: 3, label: 'Eva', muted: false }];
  const q = L.alarmQuestion('resend', { enabled: false }, keys, { names: ['Tom'], ids: [1], what: 'REAL ALARM' });
  assert.match(q.text, /Tom \(stumm\) mit Lautstärke 2/); assert.doesNotMatch(q.text, /Anna/);
});

test('fix round: muted line in every sending question', () => {
  const keys = [{ id: 1, label: 'Josh', muted: false }, { id: 2, label: 'Anna', muted: true, mute_volume: 3 }];
  const demo = { enabled: true, label: 'Anna', key_id: 2 };
  const fa = L.alarmQuestion('false_alarm', demo, keys).text;
  assert.match(fa, /Anna \(stumm\) mit Lautstärke 3/); assert.doesNotMatch(fa, /Alarm kommt/);
  assert.match(L.alarmQuestion('box_alarm', demo, keys).text, /Anna \(stumm\) mit Lautstärke 3/);
  assert.match(L.alarmQuestion('false_alarm', { enabled: false }, keys).text, /kommt mit Stumm-Lautstärke\): Anna/);
});

test('fix round: empty vs. not loaded list', () => {
  const empty = L.alarmQuestion('alarm', { enabled: false }, []).text;
  assert.match(empty, /Not-Liste der Server-Konfiguration\. Die Empfängerliste ist leer\./);
  assert.doesNotMatch(empty, /senden an Die/);
  const nl = L.alarmQuestion('alarm', { enabled: false }, null).text;
  assert.match(nl, /noch nicht geladen/); assert.doesNotMatch(nl, /leer/);
  assert.match(L.alarmQuestion('alarm', { enabled: false }, undefined).text, /noch nicht geladen/);
});

test('fix round: volume normalised', () => {
  assert.match(L.muteQuestion('A', 'muted', '0'), /OHNE Ton/);
  assert.match(L.muteQuestion('A', 'muted', '3'), /Stumm-Lautstärke 3 von 10/);
  assert.equal(L.volWords('0'), 'lautlos'); assert.equal(L.volWords('3'), 'leise (Lautstärke 3 von 10)');
});

test('fix round: buildUnreached merge order', () => {
  const prev = { action: 'alarm', what: 'REAL ALARM', at: 1, startedAt: 50, entries: [{ id: 3, name: 'A', reason: 'r' }, { id: 5, name: 'B', reason: 'r' }, { id: 0, name: 'N', reason: 'r' }] };
  const a = L.buildUnreached(prev, 'alarm', { results: [{ id: 3, label: 'A', ok: true }] }, { ids: [3], origAt: 50 }, 9);
  assert.deepEqual(plain(a.unreached.entries.map((e) => e.id)), [5, 0]);
  const b = L.buildUnreached(prev, 'alarm', { results: [{ id: 5, label: 'B', ok: false, http: 502 }, { id: 7, label: 'C', ok: false, http: 0 }] }, { ids: [5, 7], origAt: 50 }, 9);
  assert.deepEqual(plain(b.unreached.entries), [
    { id: 3, name: 'A', reason: 'r' }, { id: 5, name: 'B', reason: 'HTTP 502' }, { id: 0, name: 'N', reason: 'r' },
    { id: 7, name: 'C', reason: 'keine Verbindung/Timeout' }]);
});

test('fix round: clearedOk and demo counting', () => {
  const failed = L.deriveAlarmInfo([{ event: 'false_alarm', at: 20, ok: false, demo: false }, { event: 'relay_alarm', at: 10 }]);
  assert.equal(failed.clearedOk, false);
  const resent = L.deriveAlarmInfo([{ event: 'false_alarm', at: 30, ok: true, resend: 1 }, { event: 'false_alarm', at: 20, ok: false, demo: false }, { event: 'relay_alarm', at: 10 }]);
  assert.equal(resent.clearedOk, true);
  const demo = L.deriveAlarmInfo([{ event: 'false_alarm', at: 20, ok: true, demo: true }, { event: 'nas_alarm', at: 10, ok: true, demo: true }]);
  assert.equal(demo.clearedAt, 20);
  assert.equal(L.deriveAlarmInfo([{ event: 'false_alarm', at: 20, ok: true, demo: true }, { event: 'nas_alarm', at: 10, ok: true, demo: false }]).clearedAt, undefined);
});

// ==== Abschluss-Prüfung des Neubaus ==========================================

test('M5: beschädigte Liste – die Frage nennt die Not-Liste, nicht „noch nicht geladen“', () => {
  for (const kind of ['alarm', 'false_alarm']) {
    const t = L.alarmQuestion(kind, { enabled: false }, null, { keysCorrupt: true }).text;
    assert.match(t, /an die Not-Liste aus der Server-Konfiguration/, kind);
    assert.match(t, /Die Empfängerliste auf dem Server ist beschädigt/, kind);
    assert.doesNotMatch(t, /noch nicht geladen/, kind);
  }
  // Demo-Modus: Der Demo-Key steht im Demo-Zustand selbst - die Frage bleibt.
  const d = L.alarmQuestion('alarm', { enabled: true, label: 'Josh', key_id: 1 }, null, { keysCorrupt: true }).text;
  assert.match(d, /DEMO-MODUS: nur/); assert.doesNotMatch(d, /Not-Liste/);
  assert.match(L.alarmQuestion('alarm', { enabled: false }, null).text, /noch nicht geladen/);
});

test('I3: Merker für einen Versand ohne Ergebnis', () => {
  const now = 1800000000000;
  assert.deepEqual(plain(L.validateSendMarker(JSON.stringify({ action: 'alarm', at: now - 1000 }), now)), { action: 'alarm', what: 'REAL ALARM', at: now - 1000 });
  assert.equal(L.validateSendMarker(JSON.stringify({ action: 'false_alarm', at: now - 1000, unclear: true }), now).what, 'ENTWARNUNG');
  assert.equal(L.validateSendMarker(JSON.stringify({ action: 'alarm', at: now - 13 * 3600 * 1000 }), now), null);
  assert.equal(L.validateSendMarker(JSON.stringify({ action: 'x', at: now }), now), null);
  assert.equal(L.validateSendMarker('{kaputt', now), null);
  assert.equal(L.validateSendMarker(null, now), null);
  const t = L.sendMarkerNote({ action: 'alarm', what: 'REAL ALARM', at: now });
  assert.match(t, /^Letzter Versand ohne Ergebnis \(REAL ALARM, \d\d:\d\d:\d\d Uhr\) – erst Verlauf prüfen, nicht sofort erneut auslösen!/);
});

test('M7: Verkürzen von „Automatisch wieder laut nach“ – wer sofort laut würde', () => {
  const now = 1800000000;
  const keys = [
    { id: 1, label: 'Anna', muted: true, mute_permanent: false, muted_at: now - 3 * 3600 },
    { id: 2, label: 'Ben', muted: true, mute_permanent: true, muted_at: now - 30 * 3600 },
    { id: 3, label: 'Cem', muted: false, muted_at: 0 },
    { id: 4, label: 'Dora', muted: true, muted_at: now - 1800 },
    { id: 5, label: 'Eva', muted: true, muted_at: 0 }
  ];
  // 12 Std -> 2 Std: Anna (3 Std) und Eva (ohne Zeitpunkt = schon fällig).
  assert.deepEqual(plain(L.muteShortenImpact(keys, 7200, 43200, now)), { count: 2, names: ['Anna', 'Eva'] });
  // Länger oder „nie“: keine Frage.
  assert.equal(L.muteShortenImpact(keys, 43200, 7200, now), null);
  assert.equal(L.muteShortenImpact(keys, 0, 7200, now), null);
  // Von „nie“ auf 2 Std ist kürzer.
  assert.equal(L.muteShortenImpact(keys, 7200, 0, now).count, 2);
  // Kürzer, aber niemand wäre fällig: count 0 (die Ansicht fragt dann nicht).
  assert.deepEqual(plain(L.muteShortenImpact([keys[3]], 7200, 43200, now)), { count: 0, names: [] });
  // Liste unbekannt: count null (die Ansicht fragt vorsichtshalber).
  assert.deepEqual(plain(L.muteShortenImpact(null, 7200, 43200, now)), { count: null, names: [] });
  const q = L.muteShortenQuestion({ count: 2, names: ['Anna', 'Eva'] }, '2 Std');
  assert.match(q.title, /2 Std/);
  assert.match(q.text, /2 stumm geschaltete Empfänger werden sofort wieder laut/);
  assert.match(q.text, /Anna, Eva/);
  assert.match(L.muteShortenQuestion({ count: 1, names: ['Anna'] }, '2 Std').text, /1 stumm geschalteter Empfänger wird sofort wieder laut/);
  assert.match(L.muteShortenQuestion({ count: null, names: [] }, '2 Std').text, /länger stumm/);
});
