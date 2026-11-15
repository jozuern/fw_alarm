import test from 'node:test';
import assert from 'node:assert/strict';
import { loadScripts } from './helpers.mjs';

const L = loadScripts(['nas_dashboard/assets/app/logic.js']).FW.logic;

test('every known log event has a text', () => {
  for (const ev of ['created','delivered','acked','expired','cancelled','late_ack','demo_enabled','demo_disabled','demo_repaired',
    'key_added','key_removed','key_muted','key_unmuted','key_mute_volume','key_mute_permanent','key_test','notify_changed',
    'nas_alarm_started','false_alarm_started','nas_alarm','false_alarm','relay_alarm','box_offline','box_recovered','box_restart',
    'nas_web_failed','nas_web_recovered','watcher_silent','watcher_recovered','key_renamed','key_replaced','key_phone_changed','setting_changed'])
    assert.notEqual(L.describeLogEntry({ event: ev, label: 'A', old_label: 'B', id: 1, type: 'TEST' }).text, ev);
  assert.equal(L.describeLogEntry({ event: 'relay_alarm', source: 'relay', info: '[13.11. 21:42] x' }).tone, 'alarm');
  assert.ok(L.logMatches({ event: 'key_added', label: 'Anna', key: 'ab…cd' }, 'anna'));
});

test('new events', () => {
  const d = (o) => L.describeLogEntry(o);
  assert.equal(d({ event: 'key_renamed', label: 'Neu', old_label: 'Alt', by: 'josh' }).text, 'Empfänger "Alt" umbenannt in "Neu" – von josh');
  assert.equal(d({ event: 'key_replaced', label: 'A', old_key: 'ab…cd', key: 'ef…gh' }).text, 'Bark-Key von "A" ersetzt (ab…cd → ef…gh)');
  assert.equal(d({ event: 'key_phone_changed', label: 'A', by: 'x' }).text, 'Telefonnummer von "A" geändert – von x');
  assert.equal(d({ event: 'setting_changed', label: 'Ruhezeit', value: 'aus', by: 'x' }).text, 'Einstellung „Ruhezeit“: aus – von x');
});

test('texts, tones, icons ported from app.js', () => {
  const d = (o) => L.describeLogEntry(o);
  assert.equal(d({ event: 'acked', id: 3, type: 'TEST', result: 'executed', message: 'real alarm started' }).text, 'Befehl #3 TEST bestätigt: ausgeführt (Alarm gestartet)');
  assert.equal(d({ event: 'key_muted', label: 'A', source: 'shortcut' }).text, 'Empfänger "A" stummgeschaltet (Arbeitsmodus, Alarme mit Stumm-Lautstärke) – per Kurzbefehl-Link');
  assert.equal(d({ event: 'key_unmuted', label: 'A', source: 'auto' }).text, 'Empfänger "A" wieder laut geschaltet – automatisch (Zeitlimit erreicht)');
  assert.equal(d({ event: 'key_mute_volume', label: 'A', volume: 0 }).text, 'Stumm-Lautstärke für "A" auf 0 (lautlos) gestellt');
  assert.equal(d({ event: 'nas_alarm_started', resend: 2, demo: true, by: 'x' }).text, 'REAL ALARM: Nachsendung an 2 Empfänger gestartet (DEMO-MODUS) – von x');
  assert.equal(d({ event: 'nas_alarm_started' }).tone, 'alarm');
  assert.equal(d({ event: 'false_alarm' }).tone, 'warn');
  assert.equal(d({ event: 'relay_alarm', source: 'remote', info: '[13.11. 21:42] x' }).text, 'DEMO-Alarm über die Box ausgelöst (Dashboard-Befehl) (von der Box erkannt: 13.11. 21:42)');
  assert.equal(d({ event: 'box_restart', reason: 'task_wdt' }).text, 'Box neu gestartet – Grund: WATCHDOG-Reset (Task)');
  assert.equal(d({ event: 'notify_changed', key: 'enabled', label: 'L', value: 'abgeschaltet' }).tone, 'warn');
  assert.equal(d({ event: 'cancelled', id: 1, type: 'ALARM', message: 'x' }).tone, 'warn');
  assert.equal(d({ event: 'box_recovered', minutes: 4 }).tone, 'ok');
  assert.equal(d({ event: 'key_mute_permanent', label: 'A', permanent: false }).text, 'Dauerhafte Stummschaltung für "A" beendet (läuft wieder automatisch aus)');
  assert.equal(d({ event: 'unknown_thing' }).text, 'unknown_thing');
  assert.equal(d({ event: 'nas_alarm', message: 'm' }).icon, 'alarm');
  assert.equal(d({ event: 'key_test', ok: false, label: 'A' }).tone, 'warn');
});

test('log constants and filter', () => {
  assert.deepEqual(Array.from(L.LOG_FILTERS), ['all', 'alarm', 'keys', 'system']);
  assert.equal(L.LOG_STEP, 25); assert.equal(L.LOG_MAX, 100);
  assert.equal(L.LOG_EMPTY.alarm, 'Keine Alarme im Verlauf.');
  assert.equal(L.logMatches({ event: 'box_offline', minutes: 20 }, ''), true);
  assert.equal(L.logMatches({ event: 'box_offline', minutes: 20 }, 'OFFLINE'), true);
  assert.equal(L.logMatches({ event: 'box_offline', minutes: 20 }, 'nix'), false);
  assert.equal(L.failReason({ http: 0 }), 'keine Verbindung/Timeout');
  assert.equal(L.failReason({ http: 502 }), 'HTTP 502');
});
