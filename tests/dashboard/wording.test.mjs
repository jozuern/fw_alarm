// Ortsneutrale Texte: Seit dem Umzug (11/2026) läuft das Dashboard auf dem
// Server (Oracle-VM hinter dem Cloudflare Tunnel). Das NAS ist nur noch
// Brücke und Außen-Wächter. Sichtbare Texte (Oberfläche, Bark-Meldungen des
// Wächters, Fehlermeldungen) dürfen den alten Aufbau nicht mehr nennen.
import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { loadScripts } from './helpers.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const read = (rel) => fs.readFileSync(path.join(root, rel), 'utf8');
const L = loadScripts(['nas_dashboard/assets/app/logic.js']).FW.logic;

// Alles, was ausgeliefert wird - ohne die eingefrorene alte Oberfläche
// (classic.php, assets/classic/) und ohne die Vendor-Bibliothek.
const dash = path.join(root, 'nas_dashboard');
const sources = [
  ...fs.readdirSync(path.join(dash, 'assets', 'app')).filter((f) => f.endsWith('.js')).map((f) => `assets/app/${f}`),
  ...fs.readdirSync(path.join(dash, 'api')).filter((f) => f.endsWith('.php')).map((f) => `api/${f}`),
  'lib/common.php', 'lib/shell.php', 'cron/check_offline.php', 'dashboard_api.php', 'index.php',
].map((f) => `nas_dashboard/${f}`);

// Zeilen, die nur Kommentar sind, zählen nicht (dort darf die Geschichte stehen).
const codeLines = (rel) => read(rel).split(/\r?\n/)
  .map((text, i) => ({ text, no: i + 1 }))
  .filter((l) => !/^\s*(\/\/|\*|\/\*|#)/.test(l.text));

test('kein sichtbarer Text nennt den alten Aufbau', () => {
  const old = /Web Station|Aufgabenplaner|Oracle|Cloud-VM|Cloud\)|File Station|Gruppe http|Benutzer http|Synology/;
  const hits = [];
  for (const rel of sources) {
    for (const l of codeLines(rel)) {
      if (old.test(l.text)) hits.push(`${rel}:${l.no}: ${l.text.trim().slice(0, 120)}`);
    }
  }
  assert.deepEqual(hits, []);
});

test('„DSM“ kommt nur noch beim Außen-Wächter auf dem NAS vor', () => {
  const hits = [];
  for (const rel of sources) {
    for (const l of codeLines(rel)) {
      if (/DSM/.test(l.text) && !/fw-alarm-watch/.test(l.text)) hits.push(`${rel}:${l.no}: ${l.text.trim().slice(0, 120)}`);
    }
  }
  assert.deepEqual(hits, []);
});

test('Offline-Wächter: Texte nennen den Wächter-Timer auf dem Server', () => {
  assert.equal(L.watchdogState({ configured: true, last_run: 0 }, 1).text,
    'noch nie gelaufen – Wächter-Timer auf dem Server eingerichtet?');
  assert.equal(L.watchdogState({ configured: true, last_run: 1000 }, 1000 + 2400).text,
    'seit 40 Min nicht gelaufen – Wächter-Timer prüfen!');
  assert.equal(L.watchdogState({ configured: true, last_run: 1000, web_alert: false }, 1010).text,
    'läuft, aber die Meldung „Dashboard defekt“ ist abgeschaltet');
});

test('Außen-Wächter: Texte nennen das NAS und was dort zu prüfen ist', () => {
  assert.equal(L.outsideState({ outside_seen_at: 0 }, 1000).text,
    'eingerichtet, hat sich aber noch nie gemeldet – läuft die DSM-Aufgabe fw-alarm-watch auf dem NAS?');
  assert.equal(L.outsideState({ outside_seen_at: 1000, outside_silent_after_s: 3600 }, 1000 + 7200).text,
    'meldet sich seit 2 Std 0 Min nicht – NAS zu Hause prüfen (Strom/Internet, DSM-Aufgabe fw-alarm-watch)');
  assert.match(L.outsideState({ outside_seen_at: 1000, outside_bark_failed_at: 900 }, 1100).text,
    /BARK_KEY in fw-alarm-watch\.conf auf dem NAS prüfen$/);
  assert.match(L.outsideState({ outside_seen_at: 1000, outside_alert: false }, 1100).text,
    /fiele das NAS aus, käme keine Warnung$/);
});

test('Prüfzeilen und Hinweise: Server innen, NAS außen', () => {
  const rows = L.boxRows({ keys_count: '6', keys_version: '3' },
    { offline: false, seenAge: 5, watchdog: { warn: false, text: 'läuft' }, nasKeysVersion: 3, stale: false });
  assert.equal(rows.find((r) => r.id === 'watchdog').label, 'Offline-Wächter (Server)');
  assert.equal(rows.find((r) => r.id === 'outside').label, 'Außen-Wächter (NAS)');
  assert.equal(rows.find((r) => r.id === 'keys').value, '6 Empfänger · v3 (= Server)');
  const tips = rows.map((r) => (typeof r.tip === 'string' ? r.tip : '')).join(' ');
  assert.doesNotMatch(tips, /Cloud|DSM-Aufgabe auf dem NAS prüft/);
  assert.match(tips, /Das NAS zu Hause ruft den Server alle 5 Minuten von außen auf/);
  // Außer beim Außen-Wächter nennt kein Hilfetext das NAS.
  for (const r of rows.filter((x) => x.id !== 'outside')) {
    const tip = typeof r.tip === 'function' ? r.tip({ offline: false }) + r.tip({ offline: true }) : String(r.tip || '');
    assert.doesNotMatch(tip, /NAS/, r.id);
  }
  assert.ok(rows.every((r) => r.tip), 'jede Prüfzeile hat einen Hilfetext');

  const ov = L.overviewRows({ status: { relay: 'open', wifi: 'up', rssi: '-61', uptime_s: '100' }, offline: false,
    stale: false, statusError: null, demo: null, seenAge: 5,
    watchdog: { warn: true, text: 'A', outside: { warn: true, text: 'B' } },
    keysSummary: null, keysCorrupt: '', alarmFact: { text: 'keiner', tone: '' } });
  assert.equal(ov.find((r) => r.id === 'watch').value, 'Server: A · NAS: B');
  const quiet = L.overviewRows({ status: { relay: 'open', wifi: 'up', rssi: '-61', uptime_s: '100' }, offline: false,
    stale: false, statusError: null, demo: null, seenAge: 5, watchdog: { warn: false, text: 'ok' },
    keysSummary: null, keysCorrupt: '', alarmFact: { text: 'keiner', tone: '' } });
  assert.equal(quiet.find((r) => r.id === 'watch').value, 'Server aktiv');
});

test('Verlauf: Ereignisse des Servers und des Außen-Wächters', () => {
  const text = (e) => L.describeLogEntry(e).text;
  assert.equal(text({ event: 'nas_alarm', message: 'x' }), 'REAL ALARM direkt vom Server: x');
  assert.equal(text({ event: 'nas_web_failed', detail: 'd' }), 'Dashboard gestört: d');
  assert.equal(text({ event: 'nas_web_recovered', minutes: 5 }), 'Dashboard wieder in Ordnung (Störung ca. 5 min)');
  assert.equal(text({ event: 'watcher_silent', minutes: 70 }), 'Außen-Wächter (NAS) meldet sich nicht (seit 70 min)');
  assert.equal(text({ event: 'watcher_recovered', minutes: 9 }), 'Außen-Wächter (NAS) wieder da (Pause ca. 9 min)');
});

test('Wächter-Meldungen (PHP): neue Hinweise, weiterhin ASCII', () => {
  const cron = read('nas_dashboard/cron/check_offline.php');
  // Rechte-Hinweis für den Server statt der DSM-Klickfolge
  assert.match(cron, /sudo chown -R fwalarm: \/srv\/fw-alarm\/data/);
  // Außen-Wächter ist jetzt das NAS: was zu Hause zu prüfen ist
  assert.match(cron, /Der Aussen-Waechter \(NAS\) hat sich \{\$since\} gemeldet\./);
  assert.match(cron, /DSM-Aufgabe fw-alarm-watch/);
  assert.match(cron, /'Dashboard DEFEKT'/);
  assert.match(cron, /'Dashboard wieder OK'/);
  // Alles, was als Titel/Text an $notify() geht, ist ASCII.
  const calls = cron.match(/\$notify\([\s\S]*?\);/g) || [];
  assert.ok(calls.length >= 8, `nur ${calls.length} $notify-Aufrufe gefunden`);
  for (const call of calls) {
    assert.match(call, /^[\x09\x0a\x0d\x20-\x7e]*$/, call.slice(0, 80));
  }
  const common = read('nas_dashboard/lib/common.php');
  assert.match(common, /'Webserver nicht erreichbar \('/);
  assert.match(common, /'Manuelle Alarmierung ueber das Dashboard!'/);
  assert.match(common, /const LOCK_FAILED_MESSAGE = 'Fehler: Datenordner nicht beschreibbar \(auf dem Server: sudo chown -R fwalarm: \/srv\/fw-alarm\/data\)\.';/);
});
