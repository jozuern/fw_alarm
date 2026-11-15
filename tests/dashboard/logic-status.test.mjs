import test from 'node:test';
import assert from 'node:assert/strict';
import { loadScripts, plain } from './helpers.mjs';

const L = loadScripts(['nas_dashboard/assets/app/logic.js']).FW.logic;
const base = { relay: 'open', wifi: 'up', rssi: '-61', alarm_pending: '0', key_progress: '0/0', last_alarm: 'nie',
  heartbeat_ok: '0', last_heartbeat: 'nie', uptime_s: '360000', free_heap: '180000', reset_reason: 'poweron', keys_version: '3' };
const v = (o) => L.verdict({ authLost: false, statusError: null, stale: false, alarmInFlight: false, offline: false,
  status: base, demo: { enabled: false }, readOnly: false, hints: [], ...o });

test('ranking', () => {
  assert.equal(v({}).key, 'ok');
  assert.equal(v({ offline: true, status: { ...base, alarm_pending: '1' } }).key, 'offline');
  assert.equal(v({ status: { ...base, alarm_pending: '1', key_progress: '2/6' } }).tone, 'alarm');
  assert.equal(v({ status: { ...base, relay: 'closed', stuck_warned: '1' } }).key, 'stuck');
  assert.equal(v({ status: { ...base, relay: 'closed', key_progress: '6/6', last_alarm: '[13.11. 21:42] x' } }).key, 'standing');
  assert.equal(v({ demo: { enabled: true, label: 'Anna' } }).key, 'demo');
  assert.equal(v({ hints: [{ tone: 'warn', text: 'x' }] }).title, 'Bereit – 1 Hinweis');
  assert.equal(v({ hints: [{ tone: 'warn', text: 'x' }, { tone: 'crit', text: 'y' }] }).tone, 'crit');
  assert.equal(v({ hints: [{ tone: 'crit', text: 'y' }] }).raiseActions, true);
  assert.equal(v({ authLost: true, offline: true }).key, 'auth');
});
test('nas problem keeps red when last known offline', () => {
  const r = v({ statusError: { kind: 'network' }, offline: true });
  assert.equal(r.key, 'nas'); assert.equal(r.tone, 'crit'); assert.equal(r.title, 'NAS nicht erreichbar');
  assert.equal(v({ statusError: { kind: 'timeout' }, alarmInFlight: true }).title, 'NAS beschäftigt');
  assert.equal(v({ stale: true }).title, 'Status veraltet');
  assert.equal(v({ statusError: { kind: 'http', status: 500, message: 'x' } }).title, 'NAS-Störung');
});
test('hints', () => {
  const h = (o) => L.collectHints({ status: base, offline: false, stale: false, watchdog: null, keysSummary: { total: 3, muted: 1, permanent: 0, overdue: 0 },
    keysCorrupt: '', demoOn: false, deviceHint: '', mismatchMs: 0, readOnly: false, ...o });
  assert.deepEqual(plain(h({})), []);                                                        // one muted is not a hint
  assert.equal(h({ keysSummary: { total: 3, muted: 3, permanent: 0, overdue: 0 } })[0].tone, 'warn');
  assert.equal(h({ status: { ...base, last_alarm: 'x', key_progress: '5/6' } })[0].tone, 'crit');
  assert.equal(h({ status: { ...base, last_alarm: 'x', alarm_gaveup: '1', key_progress: '6/6' } })[0].tone, 'crit');
  assert.equal(h({ status: { ...base, rssi: '-80' } })[0].tone, 'warn');
  assert.equal(h({ status: { ...base, heartbeat_ok: '0', last_heartbeat: 'nie' } }).length, 0);
  assert.equal(h({ status: { ...base, heartbeat_ok: '0', last_heartbeat: '14.11. 08:00' } }).length, 1);
  assert.equal(h({ status: { ...base, reset_reason: 'task_wdt', uptime_s: '3600' } }).length, 1);
  assert.equal(h({ status: { ...base, reset_reason: 'task_wdt', uptime_s: '90000' } }).length, 0);
  assert.equal(h({ mismatchMs: 61000 }).length, 1);
  assert.equal(h({ mismatchMs: 59000 }).length, 0);
  assert.equal(h({ keysCorrupt: 'kaputt' })[0].tone, 'crit');
  assert.equal(h({ stale: true, keysCorrupt: 'kaputt' }).length, 0);
});
test('hints: more branches', () => {
  const h = (o) => L.collectHints({ status: base, offline: false, stale: false, watchdog: null, keysSummary: null,
    keysCorrupt: '', demoOn: false, deviceHint: '', mismatchMs: 0, readOnly: false, ...o });
  // alle stumm zählt im Demo-Modus nicht
  assert.equal(h({ demoOn: true, keysSummary: { total: 2, muted: 2, permanent: 0, overdue: 0 } }).length, 0);
  // überfällige Rückschaltung
  assert.equal(h({ keysSummary: { total: 3, muted: 1, permanent: 0, overdue: 1 } }).length, 1);
  // Wächter-Hinweise auch bei offline Box; Box-Hinweise nicht
  const wd = { warn: true, text: 'x', outside: { warn: true, text: 'y' } };
  assert.equal(h({ watchdog: wd, offline: true, status: { ...base, rssi: '-90' } }).length, 2);
  assert.equal(h({ deviceHint: 'Fremdes Board' })[0].text, 'Fremdes Board');
  // wenig Speicher: Summe bzw. größter Block
  assert.equal(h({ status: { ...base, free_heap: '40000' } }).length, 1);
  assert.equal(h({ status: { ...base, max_alloc_heap: '20000' } }).length, 1);
  // Lese-Rolle bekommt anderen Text
  const del = { ...base, last_alarm: 'x', key_progress: '5/6' };
  assert.match(h({ status: del, readOnly: true })[0].text, /Admin verständigen/);
  assert.equal(h({ status: null }).length, 0);
});
test('watchdog', () => {
  assert.equal(L.watchdogState({ configured: true, last_run: 1000 }, 1000 + 2101).warn, true);
  assert.equal(L.watchdogState({ configured: true, last_run: 1000 }, 1000 + 2000).warn, false);
  assert.equal(L.watchdogState({ configured: true, last_run: 1000, last_send_failed_at: 900, last_send_ok_at: 800 }, 1100).warn, true);
  assert.equal(L.outsideState({ outside_seen_at: 1000, outside_silent_after_s: 3600 }, 1000 + 3601).warn, true);
  assert.equal(L.outsideState({}, 1000), null);
});
test('watchdog: remaining branches', () => {
  assert.equal(L.watchdogState(null, 1), null);
  assert.equal(L.watchdogState({ configured: false }, 1).warn, true);
  assert.equal(L.watchdogState({ configured: true, last_run: 0 }, 1).warn, true);
  assert.equal(L.watchdogState({ configured: true, last_run: 1000, offline_alert: false }, 1010).warn, true);
  assert.equal(L.watchdogState({ configured: true, last_run: 1000, web_alert: false }, 1010).warn, true);
  assert.equal(L.watchdogState({ configured: true, last_run: 1000 }, 1010).text, 'läuft (zuletzt gerade eben)');
  assert.equal(L.outsideState({ outside_seen_at: 0 }, 1000).warn, true);
  assert.equal(L.outsideState({ outside_seen_at: 1000, outside_bark_failed_at: 900 }, 1100).warn, true);
  assert.equal(L.outsideState({ outside_seen_at: 1000, outside_alert: false }, 1100).warn, true);
  // Fallback-Frist: eine Stunde
  assert.equal(L.outsideState({ outside_seen_at: 1000 }, 1000 + 3600).warn, false);
  assert.equal(L.outsideState({ outside_seen_at: 1000 }, 1000 + 3601).warn, true);
});
test('box rows grey out when offline except live rows', () => {
  const rows = L.boxRows(base, { offline: true, seenAge: 1380, watchdog: { warn: false, text: 'läuft' }, nasKeysVersion: 3, stale: false });
  assert.equal(rows.find((r) => r.id === 'wifi').tone, 'unknown');
  assert.equal(rows.find((r) => r.id === 'uptime').label, 'Offline seit');
  assert.equal(rows.find((r) => r.id === 'uptime').tone, 'crit');
});
test('box rows: ids, groups, stale, values', () => {
  const rows = L.boxRows({ ...base, relay: 'closed', keys_count: '6', keys_muted: '1' },
    { offline: false, seenAge: NaN, watchdog: null, nasKeysVersion: 4, stale: false });
  assert.deepEqual(plain(rows.map((r) => r.id)), ['relay', 'ready', 'delivery', 'lastAlarm', 'wifi', 'keys', 'heartbeat',
    'ack', 'firmware', 'uptime', 'reset', 'memory', 'watchdog', 'outside']);
  assert.deepEqual(plain([...new Set(rows.map((r) => r.group))]), ['Alarmkette', 'Verbindung', 'Wartung']);
  assert.equal(rows[0].tone, 'crit');
  assert.equal(rows.find((r) => r.id === 'keys').value, '6 Empfänger (1 stumm) · v3 (NAS: v4 – Übernahme steht aus)');
  assert.equal(rows.find((r) => r.id === 'keys').tone, 'warn');
  assert.equal(rows.find((r) => r.id === 'uptime').label, 'Läuft seit');
  assert.equal(rows.find((r) => r.id === 'outside').value, 'nicht eingerichtet');
  const stale = L.boxRows(base, { offline: false, seenAge: NaN, watchdog: { warn: true, text: 'x' }, nasKeysVersion: 3, stale: true });
  assert.equal(stale.find((r) => r.id === 'relay').tone, 'unknown');
  // R6: bei unsicherer Datenlage ist ausnahmslos alles unbekannt
  assert.ok(stale.every((r) => r.tone === 'unknown'));
  const errRows = L.boxRows(base, { offline: false, seenAge: NaN, watchdog: { warn: true, text: 'x' }, nasKeysVersion: 3, stale: false, statusError: { kind: 'network' } });
  assert.ok(errRows.every((r) => r.tone === 'unknown'));
  const staleOff = L.boxRows(base, { offline: true, seenAge: 100, watchdog: { warn: true, text: 'x' }, nasKeysVersion: 3, stale: true });
  assert.ok(staleOff.every((r) => r.tone === 'unknown'));
  // online und sicher: Wächter-Zeile behält ihre echte Farbe
  const fresh = L.boxRows(base, { offline: false, seenAge: NaN, watchdog: { warn: true, text: 'x' }, nasKeysVersion: 3, stale: false });
  assert.equal(fresh.find((r) => r.id === 'watchdog').tone, 'warn');
  assert.equal(fresh.find((r) => r.id === 'uptime').tone, '');
  // Zustellung unvollständig ist rot
  const del = L.boxRows({ ...base, last_alarm: 'x', key_progress: '5/6' }, { offline: false, stale: false });
  assert.equal(del.find((r) => r.id === 'delivery').tone, 'crit');
});
test('statusError alone: no hints, overview watch unknown', () => {
  const ctx = { status: { ...base, rssi: '-90' }, offline: false, stale: false, statusError: { kind: 'network' }, watchdog: null,
    keysSummary: null, keysCorrupt: 'x', demoOn: false, deviceHint: 'h', mismatchMs: 0, readOnly: false };
  assert.equal(L.collectHints(ctx).length, 0);
  const rows = L.overviewRows({ ...ctx, demo: null, seenAge: 5, watchdog: { warn: false, text: 'ok' }, alarmFact: null });
  assert.equal(rows.find((r) => r.id === 'watch').tone, 'unknown');
  const rows2 = L.overviewRows({ ...ctx, statusError: null, stale: false, demo: null, seenAge: 5, watchdog: { warn: false, text: 'ok' }, alarmFact: null });
  assert.equal(rows2.find((r) => r.id === 'watch').tone, '');
});
test('verdict ranking: adjacent pairs', () => {
  const crit = [{ tone: 'crit', text: 'y' }];
  const demo = { enabled: true, label: 'Anna' };
  const closed = { ...base, relay: 'closed' };
  assert.equal(v({ authLost: true, statusError: { kind: 'network' } }).key, 'auth');
  assert.equal(v({ statusError: { kind: 'network' }, offline: true, status: { ...base, alarm_pending: '1' } }).key, 'nas');
  assert.equal(v({ offline: true, status: { ...base, alarm_pending: '1' } }).key, 'offline');
  assert.equal(v({ status: { ...closed, alarm_pending: '1', stuck_warned: '1' } }).key, 'alarm');
  assert.equal(v({ status: { ...closed, stuck_warned: '1' } }).key, 'stuck');
  assert.equal(v({ status: closed, demo, hints: crit }).key, 'standing');
  assert.equal(v({ demo, hints: crit }).key, 'demo');
  assert.equal(v({ hints: crit }).key, 'hints');
  assert.equal(v({ hints: [] }).key, 'ok');
  const so = v({ stale: true, offline: true });
  assert.equal(so.key, 'nas'); assert.equal(so.tone, 'crit');
  assert.equal(v({ status: null }).key, 'loading');
});
test('verdict: auth variants and error kinds', () => {
  assert.match(v({ authLost: true, alarmInFlight: true }).sub, /Alarm-Ergebnisses/);
  assert.match(v({ authLost: true }).sub, /Anmeldeseite/);
  for (const kind of ['http', 'csrf', 'readonly', 'mode_changed']) {
    const r = v({ statusError: { kind, status: 403, message: 'Nein' } });
    assert.equal(r.title, 'NAS-Störung');
    assert.equal(r.detail, 'Das NAS meldet einen Fehler (HTTP 403): Nein');
  }
  assert.equal(v({ statusError: {} }).detail, 'Die Antwort des NAS konnte nicht ausgewertet werden');
  assert.match(v({ offline: true }).sub, /Alarm an alle senden/);
  assert.doesNotMatch(v({ offline: true }).sub, /REAL ALARM/);
});
test('format', () => {
  assert.equal(L.fmtUptime(360000), '4 T 4 Std 0 Min');
  assert.equal(L.fmtRssi('-61'), '-61 dBm (gut)');
  assert.equal(L.fmtDateTime(0), 'nie');
});
test('format: more', () => {
  assert.equal(L.fmtUptime(12600), '3 Std 30 Min');
  assert.equal(L.fmtUptime(720), '12 Min');
  assert.equal(L.rssiQuality('-90'), 'sehr schwach');
  assert.equal(L.rssiQuality('x'), null);
  assert.equal(L.fmtHeap('180000'), '176 kB frei');
  assert.equal(L.fmtAgeShort(30), 'vor 30 s');
  assert.equal(L.fmtAgeShort(1260), 'vor 21 Min');
  assert.equal(L.fmtAgeShort(NaN), null);
  assert.equal(L.tz(), 'Europe/Berlin');
  L.setTimezone('Nicht/Vorhanden');
  assert.equal(L.tz(), 'Europe/Berlin');
  // 2026-11-13 22:40 UTC = 23:40 Berlin
  const e = Date.UTC(2026, 10, 13, 22, 40) / 1000;
  assert.equal(L.fmtDayClock(e), '13.11. 23:40');
  assert.equal(L.dayKey(e), '2026-11-13');
  assert.equal(L.fmtDayTime(e, e + 60), 'heute 23:40 Uhr');
  assert.equal(L.fmtDayTime(e, e + 86400), 'gestern 23:40 Uhr');
  assert.equal(L.fmtDayLabel(e, e + 86400 * 5).startsWith('Freitag'), true);
  assert.equal(L.fmtRelTime(e, e + 30), 'gerade eben');
  assert.equal(L.fmtRelTime(e, e + 600), 'vor 10 min');
  assert.equal(L.fmtRelTime(e, e + 7200), 'vor 2 Std');
});
test('overview rows and recipients fact', () => {
  assert.equal(L.recipientsFact({ total: 6, muted: 1, permanent: 0, overdue: 0 }, { enabled: false }, '').text, '6, 1 stumm');
  assert.equal(L.recipientsFact({ total: 6, muted: 6, permanent: 0, overdue: 0 }, null, '').tone, 'warn');
  assert.equal(L.recipientsFact(null, { enabled: true, label: 'Anna' }, '').text, 'nur Anna (Demo)');
  assert.equal(L.recipientsFact(null, null, 'x').text, 'Liste beschädigt');
  assert.deepEqual(plain(L.keysSummary({ keys: [{ muted: true, mute_permanent: true }, { mute_overdue: true }] })),
    { total: 2, muted: 1, permanent: 1, overdue: 1 });
  assert.equal(L.keysSummary({}), null);
  const rows = L.overviewRows({ status: base, offline: false, stale: false, statusError: null, demo: null, seenAge: 5,
    watchdog: { warn: false, text: 'ok', outside: { warn: false, text: 'ok' } },
    keysSummary: { total: 6, muted: 1, permanent: 0, overdue: 0 }, keysCorrupt: '', alarmFact: { text: 'keiner', tone: '' } });
  assert.deepEqual(plain(rows.map((r) => r.id)), ['box', 'wifi', 'watch', 'keys', 'alarm']);
  assert.equal(rows[0].value, 'online · läuft seit 4 T 4 Std 0 Min');
  assert.equal(rows[1].value, 'gut · -61 dBm');
  assert.equal(rows[2].value, 'NAS + Cloud aktiv');
  assert.deepEqual(plain(rows.map((r) => r.href)), ['#/einstellungen/box', null, '#/einstellungen/box', '#/empfaenger', '#/verlauf?filter=alarm']);
  const off = L.overviewRows({ status: base, offline: true, stale: false, statusError: null, demo: null, seenAge: 1380,
    watchdog: null, keysSummary: null, keysCorrupt: '', alarmFact: null });
  assert.equal(off[0].value, 'offline seit 23 Min'); assert.equal(off[0].tone, 'crit');
  assert.equal(off[1].tone, 'unknown');
});
test('Hinweis im Browser: Text je Übergang, „wieder in Ordnung“ nur nach einer Störungs-Meldung', () => {
  for (const to of ['offline', 'alarm', 'standing', 'nas', 'delivery']) {
    const r = L.browserAlertFor(to, false);
    assert.equal(r.problem, true, to);
    assert.ok(r.body.length > 10, to);
  }
  assert.ok(L.browserAlertFor('offline', false).body.startsWith('Box offline'));
  assert.ok(L.browserAlertFor('alarm', false).body.startsWith('Alarm läuft'));
  assert.ok(L.browserAlertFor('standing', false).body.startsWith('Alarm ausgelöst'));
  assert.ok(L.browserAlertFor('nas', false).body.startsWith('NAS nicht erreichbar'));
  assert.ok(L.browserAlertFor('delivery', false).body.startsWith('Zustellung unvollständig'));
  // Zurück ins Ruhige: nur, wenn vorher eine Störung gemeldet wurde.
  assert.equal(L.browserAlertFor('ok', false), null);
  assert.equal(L.browserAlertFor('hints', false), null);
  const ok = L.browserAlertFor('ok', true);
  assert.equal(ok.problem, false);
  assert.ok(ok.body.startsWith('Wieder in Ordnung'));
  assert.ok(L.browserAlertFor('hints', true).body.startsWith('Wieder in Ordnung'));
  assert.ok(L.browserAlertFor('demo', true).body.includes('Demo-Modus'));
  // Weder Störung noch „in Ordnung“: nichts melden.
  for (const to of ['stuck', 'auth', 'loading', 'irgendwas']) {
    assert.equal(L.browserAlertFor(to, true), null, to);
    assert.equal(L.browserAlertFor(to, false), null, to);
  }
});
