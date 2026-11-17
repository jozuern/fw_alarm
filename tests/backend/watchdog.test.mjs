// Der Wächter (cron/check_offline.php, auf dem Server alle 15 min): was ein Lauf
// ausgibt, welche Meldungen an Bark gehen, was im Verlauf landet und was das
// Dashboard danach zeigt. Box offline/wieder da, Selbsttest des Datenordners,
// Erinnerungen (Demo-Modus, Stumme), die wählbaren Arten, der Außen-Wächter
// und die Einstellungen "Meldungen an mich". Jede Regel hier muss auch eine
// neue Server-Version erfüllen. (Texte und Folgen des Selbsttests von PHP,
// health_check_url, der kaputte Demo-Zustand und "der nächste Lauf versucht
// es erneut" gelten nur für PHP: php-storage.test.mjs.)
import assert from 'node:assert/strict';
import { after, before, beforeEach, describe, it } from 'node:test';
import {
  STATUS_KEY, WATCHER_TOKEN, box, now, postForm, serverClock, session, sha256, startBackend,
} from './harness.mjs';

let backend;
let admin;
let b;
before(async () => { backend = await startBackend(); });
after(async () => { await backend.stop(); });
beforeEach(async () => {
  await backend.reset();
  admin = await session(backend);
  b = box(backend);
});

// Ein Lauf des Wächters. Seine Meldungen sind verschickt, bevor er endet.
const run = () => backend.runWatchdog();
// Die Ausgabe ist eine Liste "TEIL | TEIL | ..."; ein Teil ist ein Kennwort,
// manchmal mit Zusatz ("WEB_FAIL (1) ...", "STILL_OFFLINE (bereits gemeldet)").
const parts = (out) => out.split(' | ');
const says = (out, word) => parts(out).some((p) => p === word || p.startsWith(`${word} `));
// Den gespeicherten Stand "s Sekunden alt" machen (als wäre so lange nichts gekommen).
const ageStatus = (seconds) => backend.state.ageBoxStatus(seconds);
// Die Box hat sich gemeldet und schweigt seitdem (Schwelle des Wächters ab Werk: 600 s).
const silentBox = async (seconds = 700, headers = {}) => {
  await b.status({}, headers);
  await ageStatus(seconds);
};
// Im Gedächtnis des Wächters eine Zeit zurückdrehen (statt wirklich zu warten):
// der Merker name ist s Sekunden alt.
const ageMark = (name, seconds) => backend.state.ageMark(name, seconds);
// Eine Einstellung unter "Meldungen an mich" setzen (type = Art oder 'global').
const notifySet = async (type, key, value) => {
  const r = await admin.post({ action: 'notify_set', type, key, value: String(value) });
  assert.equal(r.ok, true, `notify_set ${type}.${key}=${value}: ${JSON.stringify(r)}`);
};
const post = async (fields) => {
  const r = await admin.post(fields);
  assert.equal(r.ok, true, `${fields.action}: ${JSON.stringify(r)}`);
  return r;
};
const mute = (id, muted = true) => post({ action: 'keys_mute', id, muted: muted ? '1' : '0' });
const mutePermanent = (id) => post({ action: 'keys_set_mute_permanent', id, permanent: '1' });
const demoOn = (id) => post({ action: 'demo_set', enabled: '1', id });
const demoOff = () => post({ action: 'demo_set', enabled: '0' });
// Alle Stummschaltungen "s Sekunden alt" machen.
const ageMutes = (seconds) => backend.state.ageMute('all', seconds);
// Ein Empfänger, wie gespeichert (nicht wie das Dashboard ihn zeigt).
const storedKey = (id) => backend.state.recipient(id);
const systemLog = async (event) => (await admin.log('system')).filter((e) => e.event === event);
const watchdogStatus = async () => (await admin.get('status')).watchdog;
// Meldungen an den Admin (Status-Key) seit der Marke.
const toAdmin = (mark) => backend.bark.to(STATUS_KEY, mark);
// Jede Wächter-Meldung beginnt mit "[TT.MM.JJJJ HH:MM] "; liefert den Text dahinter.
const STAMP = /^\[\d{2}\.\d{2}\.\d{4} \d{2}:\d{2}\] /;
const text = (message) => {
  assert.match(message.fields.body, STAMP);
  return message.fields.body.replace(STAMP, '');
};
// Der Selbsttest des Speichers scheitert (bzw. geht wieder).
const breakDataDir = () => backend.storage.fail('selftest');
const repairDataDir = () => backend.storage.repair('selftest');
const withWatcher = (more = {}) => backend.configure({ watcher_token_sha256: sha256(WATCHER_TOKEN), ...more });
// Der Außen-Wächter hat zuletzt vor s Sekunden angeklopft.
const watcherSeen = (secondsAgo) => backend.state.setWatcher({ seenAgo: secondsAgo });

describe('Start und Grundlagen', () => {
  it('ohne bark_key_status: DISABLED – eine überfällige Stummschaltung beendet der Lauf trotzdem', async () => {
    const anna = await admin.addKey('Anna', 'annakey00001');
    await mute(anna.id);
    await ageMutes(43300);
    await backend.configure({ bark_key_status: '' });
    const mark = backend.bark.mark();
    const out = await run();
    assert.match(out, /^DISABLED/);

    // Wie gespeichert gelesen - die Empfängerliste im Dashboard würde die
    // Rückschaltung beim Anzeigen sonst selbst erledigen.
    assert.equal((await storedKey(anna.id)).muted, false);
    const [info] = backend.bark.to('annakey00001', mark);
    assert.equal(info.fields.title, 'Alarmton wieder LAUT');
    assert.deepEqual(toAdmin(mark), []);
    assert.equal((await watchdogStatus()).configured, false);
  });

  it('noch nie ein Status-Push: NO_DATA – der Lauf zählt trotzdem als Lebenszeichen', async () => {
    const mark = backend.bark.mark();
    const out = await run();
    assert.ok(says(out, 'NO_DATA'), out);
    const lastRun = (await backend.state.marks()).last_run_at;
    assert.ok(Math.abs(lastRun - now()) <= 5, 'last_run_at = jetzt');
    const wd = await watchdogStatus();
    assert.equal(wd.configured, true);
    assert.equal(wd.last_run, lastRun);
    assert.deepEqual(backend.bark.since(mark), []);
  });

  it('frischer Status: WEB_OK und OK, keine Meldung', async () => {
    await b.status();
    const mark = backend.bark.mark();
    const out = await run();
    assert.ok(says(out, 'WEB_OK'), out);
    assert.ok(says(out, 'OK'), out);
    assert.deepEqual(backend.bark.since(mark), []);
  });
});

describe('Box offline und wieder da', () => {
  it('nach mehr als 10 min ohne Status: genau EINE leise Meldung "Alarm-Box OFFLINE" und ein Eintrag im Verlauf', async () => {
    await silentBox(590);
    const mark = backend.bark.mark();
    let out = await run();
    assert.ok(says(out, 'OK'), out);
    assert.deepEqual(backend.bark.since(mark), [], 'unter der Schwelle');

    await ageStatus(700);
    out = await run();
    assert.ok(parts(out).includes('NOTIFIED_OFFLINE'), out);
    const sent = backend.bark.since(mark);
    assert.equal(sent.length, 1);
    assert.equal(sent[0].key, STATUS_KEY);
    assert.equal(sent[0].fields.title, 'Alarm-Box OFFLINE');
    assert.equal(sent[0].fields.level, 'passive');
    assert.equal('sound' in sent[0].fields, false);
    assert.equal('volume' in sent[0].fields, false);
    const body = text(sent[0]);
    assert.match(body, /^Kein Status vom ESP32 seit 12 min\. /);
    assert.match(body, /Strom\/WLAN\/Box pruefen!/);
    assert.match(body, /Der Relaisalarm wuerde gerade NICHT ausgeloest/);
    assert.doesNotMatch(body, /NAS zu Hause/);

    const logged = await systemLog('box_offline');
    assert.equal(logged.length, 1);
    assert.equal(logged[0].minutes, 12);
    const wd = await watchdogStatus();
    assert.ok(Math.abs(wd.last_send_ok_at - now()) <= 5);
    assert.equal(wd.last_send_failed_at, 0);
  });

  it('zweiter Lauf: STILL_OFFLINE (bereits gemeldet), keine weitere Meldung', async () => {
    await silentBox();
    await run();
    const mark = backend.bark.mark();
    const out = await run();
    assert.ok(parts(out).includes('STILL_OFFLINE (bereits gemeldet)'), out);
    assert.deepEqual(backend.bark.since(mark), []);
    assert.equal((await systemLog('box_offline')).length, 1);
  });

  it('meldet sich die Box wieder: einmal "Alarm-Box wieder ONLINE" und box_recovered im Verlauf', async () => {
    await silentBox();
    await run();
    await b.status();
    const mark = backend.bark.mark();
    let out = await run();
    assert.ok(says(out, 'NOTIFIED_RECOVERED'), out);
    const sent = backend.bark.since(mark);
    assert.equal(sent.length, 1);
    assert.equal(sent[0].key, STATUS_KEY);
    assert.equal(sent[0].fields.title, 'Alarm-Box wieder ONLINE');
    assert.equal(sent[0].fields.level, 'passive');
    assert.match(text(sent[0]), /^ESP32 meldet sich wieder \(Ausfall ca\. 12 min\)\.$/);
    const logged = await systemLog('box_recovered');
    assert.equal(logged.length, 1);
    assert.equal(logged[0].minutes, 12);

    out = await run();
    assert.ok(says(out, 'OK'), out);
    assert.equal(backend.bark.since(mark).length, 1);
  });

  it('Erinnerung "weiterhin OFFLINE" nur, wenn eingestellt – mit demselben Text wie die erste Meldung', async () => {
    await silentBox();
    const mark = backend.bark.mark();
    await run();
    const [first] = toAdmin(mark);

    // Ab Werk (0 = nie): auch nach langer Zeit keine Erinnerung.
    await ageMark('offline_reminded_at', 30 * 86400);
    let out = await run();
    assert.ok(parts(out).includes('STILL_OFFLINE (bereits gemeldet)'), out);
    assert.equal(toAdmin(mark).length, 1);

    await notifySet('box_offline', 'repeat_hours', 1);
    await ageMark('offline_reminded_at', 3500);
    out = await run();
    assert.ok(says(out, 'STILL_OFFLINE'), out);
    await ageMark('offline_reminded_at', 3700);
    out = await run();
    assert.ok(says(out, 'OFFLINE_REMINDED'), out);
    const sent = toAdmin(mark);
    assert.equal(sent.length, 2);
    assert.equal(sent[1].fields.title, 'Alarm-Box weiterhin OFFLINE');
    assert.equal(sent[1].fields.level, 'passive');
    assert.equal(text(sent[1]), text(first));
    assert.match(text(sent[1]), /Der Relaisalarm wuerde gerade NICHT ausgeloest/);

    // Die Stunde zählt neu; im Verlauf bleibt es bei EINEM Ausfall.
    out = await run();
    assert.ok(says(out, 'STILL_OFFLINE'), out);
    assert.equal(toAdmin(mark).length, 2);
    assert.equal((await systemLog('box_offline')).length, 1);
  });

  it('Art abgeschaltet: OHNE_PUSH, keine Meldung, aber ein Eintrag im Verlauf – nach dem Einschalten wird nachgeholt', async () => {
    await notifySet('box_offline', 'enabled', 0);
    await silentBox();
    const mark = backend.bark.mark();
    let out = await run();
    assert.ok(parts(out).includes('NOTIFIED_OFFLINE'), out);
    assert.ok(says(out, 'OHNE_PUSH'), out);
    assert.match(out, /OHNE_PUSH [^|]*box_offline/);
    assert.deepEqual(backend.bark.since(mark), []);
    assert.equal((await systemLog('box_offline')).length, 1);

    out = await run();
    assert.ok(says(out, 'STILL_OFFLINE'), out);
    assert.deepEqual(backend.bark.since(mark), []);

    await notifySet('box_offline', 'enabled', 1);
    out = await run();
    assert.ok(parts(out).includes('NOTIFIED_OFFLINE (nachgeholt)'), out);
    assert.ok(!says(out, 'OHNE_PUSH'), out);
    const sent = toAdmin(mark);
    assert.equal(sent.length, 1);
    assert.equal(sent[0].fields.title, 'Alarm-Box OFFLINE');
    assert.match(text(sent[0]), /^Kein Status vom ESP32 seit/);
    assert.equal((await systemLog('box_offline')).length, 1, 'kein zweiter Eintrag für denselben Ausfall');

    // Nachgeholt ist gemeldet: kein drittes Mal, und "wieder ONLINE" gibt es jetzt auch.
    out = await run();
    assert.ok(says(out, 'STILL_OFFLINE'), out);
    await b.status();
    out = await run();
    assert.ok(says(out, 'NOTIFIED_RECOVERED'), out);
    assert.deepEqual(toAdmin(mark).map((m) => m.fields.title), ['Alarm-Box OFFLINE', 'Alarm-Box wieder ONLINE']);
  });

  it('Art abgeschaltet und die Box kommt zurück: kein "wieder ONLINE", aber box_recovered im Verlauf', async () => {
    await notifySet('box_offline', 'enabled', 0);
    await silentBox();
    await run();
    await b.status();
    const mark = backend.bark.mark();
    await run();
    assert.deepEqual(backend.bark.since(mark), []);
    assert.equal((await systemLog('box_recovered')).length, 1);
    assert.ok(says(await run(), 'OK'));
  });

  it('ohne zugestellte OFFLINE-Meldung kein "wieder ONLINE" – auch wenn die Art inzwischen wieder an ist', async () => {
    await notifySet('box_offline', 'enabled', 0);
    await silentBox();
    await run();
    await notifySet('box_offline', 'enabled', 1);
    await b.status();   // die Box ist zurück, bevor der Wächter nachholen konnte
    const mark = backend.bark.mark();
    await run();
    assert.deepEqual(backend.bark.since(mark), []);
    assert.equal((await systemLog('box_recovered')).length, 1);
  });

  it('Schwelle nach der Meldung hochgesetzt, kein neuer Status: KEIN "wieder ONLINE"', async () => {
    await silentBox();
    await run();
    await notifySet('box_offline', 'offline_after_s', 3600);
    const mark = backend.bark.mark();
    const out = await run();
    assert.ok(parts(out).includes('STILL_OFFLINE (bereits gemeldet)'), out);
    assert.deepEqual(backend.bark.since(mark), []);
    assert.deepEqual(await systemLog('box_recovered'), []);

    // Erst ein NEUER Status-Push beendet den Ausfall.
    await b.status();
    assert.ok(says(await run(), 'NOTIFIED_RECOVERED'));
    assert.equal(toAdmin(mark)[0].fields.title, 'Alarm-Box wieder ONLINE');
  });

  it('die eingestellte Schwelle gilt: mit 1 Std ist eine Box nach 12 min noch nicht offline', async () => {
    await notifySet('box_offline', 'offline_after_s', 3600);
    await silentBox(700);
    const mark = backend.bark.mark();
    assert.ok(says(await run(), 'OK'));
    await ageStatus(3700);
    assert.ok(parts(await run()).includes('NOTIFIED_OFFLINE'));
    assert.match(text(toAdmin(mark)[0]), /^Kein Status vom ESP32 seit 62 min\. /);
  });

  it('"wieder online" abgewählt (recovery=0): zurück ohne Meldung, Ausfall beendet, Eintrag im Verlauf', async () => {
    await notifySet('box_offline', 'recovery', 0);
    await silentBox();
    await run();
    await b.status();
    let mark = backend.bark.mark();
    await run();
    assert.deepEqual(backend.bark.since(mark), []);
    assert.equal((await systemLog('box_recovered')).length, 1);
    assert.ok(says(await run(), 'OK'));

    // Der Ausfall ist abgeschlossen: Der nächste wird wieder gemeldet.
    await ageStatus(700);
    mark = backend.bark.mark();
    assert.ok(parts(await run()).includes('NOTIFIED_OFFLINE'));
    assert.equal(toAdmin(mark)[0].fields.title, 'Alarm-Box OFFLINE');
  });

  it('kam der letzte Status über die Brücke: die Meldung nennt zusätzlich das NAS zu Hause', async () => {
    await silentBox(700, { 'X-FW-Bridge': '1' });
    const mark = backend.bark.mark();
    await run();
    const body = text(toAdmin(mark)[0]);
    assert.match(body, /Der Relaisalarm wuerde gerade NICHT ausgeloest/);
    assert.match(body, /Die Box meldet sich noch ueber das NAS zu Hause/);
    assert.match(body, /alarmiert sie trotzdem direkt ueber Bark/);
  });

  it('über die Brücke und der Außen-Wächter schweigt seit über 9 min: vermutlich das NAS, nicht die Box', async () => {
    await withWatcher();
    await silentBox(700, { 'X-FW-Bridge': '1' });
    await watcherSeen(600);
    const mark = backend.bark.mark();
    const out = await run();
    assert.ok(parts(out).includes('NOTIFIED_OFFLINE'), out);
    const [sent] = toAdmin(mark);
    assert.equal(sent.fields.title, 'Alarm-Box OFFLINE');
    const body = text(sent);
    assert.match(body, /^Kein Status vom ESP32 seit 12 min - vermutlich liegt es aber am NAS zu Hause/);
    assert.match(body, /auch der Aussen-Waechter dort meldet sich seit 10 min nicht/);
    assert.match(body, /alarmiert weiter direkt ueber Bark/);
    assert.doesNotMatch(body, /NICHT ausgeloest/);
  });

  it('nicht über die Brücke: ein stummer Außen-Wächter ändert den Text nicht', async () => {
    await withWatcher();
    await silentBox(700);
    await watcherSeen(600);
    const mark = backend.bark.mark();
    await run();
    const body = text(toAdmin(mark)[0]);
    assert.match(body, /Der Relaisalarm wuerde gerade NICHT ausgeloest/);
    assert.doesNotMatch(body, /NAS zu Hause/);
  });
});

describe('Selbsttest des Datenordners', () => {
  // Texte der Meldungen, die Schuld an der Box-Stille und der Außen-Wächter
  // während der Störung gelten nur für PHP: php-storage.test.mjs.
  it('ein einzelner Fehlschlag meldet nichts – die Zählung beginnt nach einem guten Lauf von vorn', async () => {
    const mark = backend.bark.mark();
    await breakDataDir();
    assert.match(await run(), /^WEB_FAIL \(1\) /);
    await repairDataDir();
    assert.ok(says(await run(), 'WEB_OK'));
    await breakDataDir();
    assert.match(await run(), /^WEB_FAIL \(1\) /);
    assert.deepEqual(backend.bark.since(mark), []);
    assert.deepEqual(await systemLog('nas_web_failed'), []);
  });

  it('hält die Störung an, erinnert der Wächter nach 24 h erneut', async () => {
    await breakDataDir();
    await run();
    const mark = backend.bark.mark();
    await run();
    await ageMark('web_fail_notified_at', 86000);
    assert.ok(says(await run(), 'WEB_FAIL'));
    await ageMark('web_fail_notified_at', 86500);
    assert.ok(says(await run(), 'WEB_FAIL_NOTIFIED'));
    assert.deepEqual(toAdmin(mark).map((m) => m.fields.title), ['Dashboard DEFEKT', 'Dashboard DEFEKT']);
    assert.equal((await systemLog('nas_web_failed')).length, 1, 'im Verlauf steht die Störung einmal');
  });

  it('Art abgeschaltet: OHNE_PUSH und ein Eintrag im Verlauf – nach dem Einschalten wird "Dashboard DEFEKT" nachgeholt', async () => {
    await notifySet('nas_web', 'enabled', 0);
    await breakDataDir();
    await run();
    const mark = backend.bark.mark();
    let out = await run();
    assert.ok(says(out, 'WEB_FAIL_NOTIFIED'), out);
    assert.match(out, /OHNE_PUSH [^|]*nas_web/);
    assert.deepEqual(backend.bark.since(mark), []);
    assert.equal((await systemLog('nas_web_failed')).length, 1);

    await notifySet('nas_web', 'enabled', 1);
    out = await run();
    assert.ok(says(out, 'WEB_FAIL_NOTIFIED'), out);
    assert.ok(!says(out, 'OHNE_PUSH'), out);
    assert.deepEqual(toAdmin(mark).map((m) => m.fields.title), ['Dashboard DEFEKT']);
    assert.equal((await systemLog('nas_web_failed')).length, 1, 'kein zweiter Eintrag für dieselbe Störung');
  });

  it('ging "Dashboard DEFEKT" nie raus, gibt es auch kein "wieder OK" – nur den Eintrag im Verlauf', async () => {
    await notifySet('nas_web', 'enabled', 0);
    await breakDataDir();
    await run();
    await run();
    await notifySet('nas_web', 'enabled', 1);
    await repairDataDir();
    const mark = backend.bark.mark();
    await run();
    assert.deepEqual(backend.bark.since(mark), []);
    assert.equal((await systemLog('nas_web_recovered')).length, 1);
    assert.ok(says(await run(), 'WEB_OK'));
  });
});

describe('Erinnerungen', () => {
  it('Demo-Modus vergessen: nach 4 Std eine Erinnerung, dann höchstens einmal am Tag – eine neue Demo-Sitzung zählt von vorn', async () => {
    const phone = await admin.addKey('Test-Händi', 'demokey00001');
    await demoOn(phone.id);
    const mark = backend.bark.mark();
    let out = await run();
    assert.ok(says(out, 'DEMO_ACTIVE'), out);
    assert.deepEqual(backend.bark.since(mark), []);

    await backend.state.ageDemo(14500);
    out = await run();
    assert.ok(says(out, 'DEMO_REMINDED'), out);
    let sent = backend.bark.since(mark);
    assert.equal(sent.length, 1);
    assert.equal(sent[0].key, STATUS_KEY);
    assert.equal(sent[0].fields.title, 'Demo-Modus noch aktiv!');
    assert.equal(sent[0].fields.level, 'passive');
    const body = text(sent[0]);
    assert.match(body, /^Der Demo-Modus laeuft seit ca\. 4 Std - /);
    assert.match(body, /ALLE Alarme gehen nur an 'Test-Haendi'/);

    out = await run();
    assert.ok(says(out, 'DEMO_ACTIVE'), out);
    assert.equal(backend.bark.since(mark).length, 1);

    await ageMark('demo_reminded_at', 86500);
    out = await run();
    assert.ok(says(out, 'DEMO_REMINDED'), out);
    assert.equal(backend.bark.since(mark).length, 2);

    // Aus und wieder an zwischen zwei Läufen: Die neue Sitzung ist frisch ...
    await demoOff();
    await demoOn(phone.id);
    out = await run();
    assert.ok(says(out, 'DEMO_ACTIVE'), out);
    assert.equal(backend.bark.since(mark).length, 2);
    // ... und wird nach ihren eigenen 4 Std erinnert, obwohl die letzte
    // Erinnerung (der alten Sitzung) noch keinen Tag her ist.
    await backend.state.ageDemo(14500);
    out = await run();
    assert.ok(says(out, 'DEMO_REMINDED'), out);
    sent = backend.bark.since(mark);
    assert.equal(sent.length, 3);
    assert.equal(sent[2].fields.title, 'Demo-Modus noch aktiv!');
  });

  it('wieder LIVE: keine Demo-Erinnerung mehr', async () => {
    const phone = await admin.addKey('Test-Handy', 'demokey00001');
    await demoOn(phone.id);
    await backend.state.ageDemo(14500);
    assert.ok(says(await run(), 'DEMO_REMINDED'));
    await demoOff();
    const mark = backend.bark.mark();
    const out = await run();
    assert.doesNotMatch(out, /DEMO_/);
    assert.deepEqual(backend.bark.since(mark), []);
  });

  it('jemand ist stumm: eine Erinnerung mit den Namen (ASCII), dann höchstens einmal am Tag', async () => {
    const joerg = await admin.addKey('Jörg', 'joergkey0001');
    await admin.addKey('Berta', 'bertakey0001');
    await mute(joerg.id);
    const mark = backend.bark.mark();
    let out = await run();
    assert.ok(says(out, 'MUTE_REMINDED'), out);
    const sent = backend.bark.since(mark);
    assert.equal(sent.length, 1, 'nur der Admin, nicht die Empfänger');
    assert.equal(sent[0].key, STATUS_KEY);
    assert.equal(sent[0].fields.title, 'Empfaenger stummgeschaltet');
    assert.equal(sent[0].fields.level, 'passive');
    assert.equal(text(sent[0]), '1 Empfaenger im Arbeitsmodus (Alarme ohne Ton): Joerg.');

    out = await run();
    assert.ok(parts(out).includes('MUTED_ACTIVE (1)'), out);
    assert.equal(backend.bark.since(mark).length, 1);

    await ageMark('mute_reminded_at', 86500);
    out = await run();
    assert.ok(says(out, 'MUTE_REMINDED'), out);
    assert.equal(backend.bark.since(mark).length, 2);
  });

  it('alle wieder laut: die Erinnerung beginnt beim nächsten Stummen von vorn', async () => {
    const joerg = await admin.addKey('Jörg', 'joergkey0001');
    await mute(joerg.id);
    assert.ok(says(await run(), 'MUTE_REMINDED'));
    await mute(joerg.id, false);
    const mark = backend.bark.mark();
    let out = await run();
    assert.doesNotMatch(out, /MUTE/);
    assert.deepEqual(backend.bark.since(mark), []);

    await mute(joerg.id);
    out = await run();
    assert.ok(says(out, 'MUTE_REMINDED'), out);
    assert.equal(toAdmin(mark).length, 1);
  });

  it('dauerhaft Stumme zählen getrennt: eigene Meldung, höchstens einmal pro Woche', async () => {
    const paula = await admin.addKey('Paula', 'paulakey0001');
    const joerg = await admin.addKey('Jörg', 'joergkey0001');
    await mutePermanent(paula.id);
    const mark = backend.bark.mark();
    let out = await run();
    assert.ok(says(out, 'PERMANENT_MUTE_REMINDED'), out);
    assert.ok(!says(out, 'MUTE_REMINDED') && !says(out, 'MUTED_ACTIVE'), out);
    let sent = backend.bark.since(mark);
    assert.equal(sent.length, 1);
    assert.equal(sent[0].key, STATUS_KEY);
    assert.equal(sent[0].fields.title, 'Dauerhaft stummgeschaltet');
    assert.equal(sent[0].fields.level, 'passive');
    assert.match(text(sent[0]), /^1 Empfaenger sind DAUERHAFT stumm /);
    assert.match(text(sent[0]), /: Paula\. Noch aktuell\?/);

    out = await run();
    assert.ok(parts(out).includes('PERMANENT_MUTED (1)'), out);
    await ageMark('permanent_mute_reminded_at', 6 * 86400);
    out = await run();
    assert.ok(parts(out).includes('PERMANENT_MUTED (1)'), out);
    assert.equal(backend.bark.since(mark).length, 1);

    // Ein zeitlich begrenzt Stummer daneben bekommt seine eigene (tägliche) Erinnerung.
    await mute(joerg.id);
    out = await run();
    assert.ok(says(out, 'MUTE_REMINDED'), out);
    assert.ok(parts(out).includes('PERMANENT_MUTED (1)'), out);
    sent = backend.bark.since(mark);
    assert.equal(sent.length, 2);
    assert.equal(text(sent[1]), '1 Empfaenger im Arbeitsmodus (Alarme ohne Ton): Joerg.');

    await ageMark('permanent_mute_reminded_at', 7 * 86400 + 100);
    out = await run();
    assert.ok(says(out, 'PERMANENT_MUTE_REMINDED'), out);
    assert.ok(parts(out).includes('MUTED_ACTIVE (1)'), out);
    sent = backend.bark.since(mark);
    assert.equal(sent.length, 3);
    assert.equal(sent[2].fields.title, 'Dauerhaft stummgeschaltet');
  });

  it('überfällige Stummschaltung (über 12 Std): der Lauf schaltet wieder laut und sagt es dem Betroffenen – dauerhaft Stumme bleiben', async () => {
    const anna = await admin.addKey('Anna', 'annakey00001');
    const paula = await admin.addKey('Paula', 'paulakey0001');
    await mute(anna.id);
    await mutePermanent(paula.id);
    const versionBefore = (await admin.keys()).version;

    // Noch nicht überfällig: bleibt stumm.
    await ageMutes(43000);
    let mark = backend.bark.mark();
    await run();
    assert.equal((await storedKey(anna.id)).muted, true);
    assert.deepEqual(backend.bark.to('annakey00001', mark), []);

    await ageMutes(43300);
    mark = backend.bark.mark();
    const out = await run();
    // Wie gespeichert gelesen - die Empfängerliste im Dashboard würde die
    // Rückschaltung beim Anzeigen sonst selbst erledigen.
    assert.equal((await storedKey(anna.id)).muted, false);
    assert.equal((await storedKey(paula.id)).muted, true);
    const info = backend.bark.to('annakey00001', mark);
    assert.equal(info.length, 1);
    assert.equal(info[0].fields.title, 'Alarmton wieder LAUT');
    assert.equal(info[0].fields.level, 'passive');
    assert.match(text(info[0]), /^Stummschaltung automatisch beendet \(war laenger als 12 Std aktiv\)\. /);
    assert.deepEqual(backend.bark.to('paulakey0001', mark), []);
    // Im selben Lauf gilt Anna schon als laut, Paula weiter als dauerhaft stumm.
    assert.ok(!says(out, 'MUTE_REMINDED') && !says(out, 'MUTED_ACTIVE'), out);
    assert.ok(says(out, 'PERMANENT_MUTED') || says(out, 'PERMANENT_MUTE_REMINDED'), out);

    const list = await admin.keys();
    const row = (id) => list.keys.find((k) => k.id === id);
    assert.equal(row(anna.id).muted, false);
    assert.equal(row(anna.id).mute_overdue, false);
    assert.equal(row(paula.id).muted, true);
    assert.equal(row(paula.id).mute_permanent, true);
    assert.ok(list.version > versionBefore, 'neue Listen-Version: Die Box holt den Stand');

    const unmuted = (await admin.log('keys')).filter((e) => e.event === 'key_unmuted');
    assert.equal(unmuted.length, 1);
    assert.equal(unmuted[0].label, 'Anna');
    assert.equal(unmuted[0].source, 'auto');
  });
});

describe('Wählbare Arten (ab Werk aus)', () => {
  it('Auffälligkeiten an der Box: erst im zweiten Lauf in Folge gemeldet, dann höchstens im eingestellten Abstand', async () => {
    await b.status({ rssi: '-85' });
    const mark = backend.bark.mark();
    assert.doesNotMatch(await run(), /HEALTH_/, 'ab Werk aus');
    assert.doesNotMatch(await run(), /HEALTH_/);

    await notifySet('box_health', 'enabled', 1);
    let out = await run();
    assert.ok(parts(out).includes('HEALTH_ISSUES (1)'), out);
    assert.deepEqual(backend.bark.since(mark), []);

    out = await run();
    assert.ok(says(out, 'HEALTH_NOTIFIED'), out);
    const sent = backend.bark.since(mark);
    assert.equal(sent.length, 1);
    assert.equal(sent[0].key, STATUS_KEY);
    assert.equal(sent[0].fields.title, 'Alarm-Box: Auffaelligkeit');
    assert.equal(sent[0].fields.level, 'passive');
    assert.match(text(sent[0]), /^WLAN schwach \(-85 dBm\)\. /);

    out = await run();
    assert.ok(parts(out).includes('HEALTH_ISSUES (1)'), out);
    assert.equal(backend.bark.since(mark).length, 1);
    await ageMark('health_notified_at', 86500);
    assert.ok(says(await run(), 'HEALTH_NOTIFIED'));
    assert.equal(backend.bark.since(mark).length, 2);
  });

  it('heartbeat_ok=0 mit last_heartbeat=nie ist kein Fehler; ein gescheiterter Heartbeat und wenig Speicher sind welche', async () => {
    await notifySet('box_health', 'enabled', 1);
    await b.status({ heartbeat_ok: '0', last_heartbeat: 'nie' });
    const mark = backend.bark.mark();
    assert.doesNotMatch(await run(), /HEALTH_/);
    assert.doesNotMatch(await run(), /HEALTH_/);

    await b.status({ heartbeat_ok: '0', last_heartbeat: '16.11.2026 08:00:00', free_heap: '40000' });
    let out = await run();
    assert.ok(parts(out).includes('HEALTH_ISSUES (2)'), out);
    out = await run();
    assert.ok(says(out, 'HEALTH_NOTIFIED'), out);
    const [sent] = toAdmin(mark);
    const body = text(sent);
    assert.match(body, /wenig freier Speicher \(39 kB\)/i);
    assert.match(body, /der taegliche Heartbeat der Box konnte nicht gesendet werden/);
    assert.doesNotMatch(body, /WLAN schwach/);
  });

  it('eine unauffällige Box dazwischen: die zwei Läufe müssen wieder in Folge sein', async () => {
    await notifySet('box_health', 'enabled', 1);
    await b.status({ rssi: '-85' });
    const mark = backend.bark.mark();
    assert.ok(parts(await run()).includes('HEALTH_ISSUES (1)'));
    await b.status({ rssi: '-61' });
    assert.doesNotMatch(await run(), /HEALTH_/);
    await b.status({ rssi: '-85' });
    assert.ok(parts(await run()).includes('HEALTH_ISSUES (1)'));
    assert.deepEqual(backend.bark.since(mark), []);
  });

  it('Wochenbericht: am gewählten Wochentag einmal, mit den Zahlen aus dem Verlauf', async () => {
    await b.status();
    await b.status({ last_alarm: '16.11.2026 10:05:00', alarm_seq: '1' });   // ein Relaisalarm
    const today = serverClock().weekday;
    await notifySet('weekly_report', 'enabled', 1);
    await notifySet('weekly_report', 'hour', 0);
    await notifySet('weekly_report', 'weekday', (today % 7) + 1);   // morgen
    const mark = backend.bark.mark();
    let out = await run();
    assert.ok(!says(out, 'WEEKLY_REPORT'), out);
    assert.deepEqual(backend.bark.since(mark), []);

    await notifySet('weekly_report', 'weekday', today);
    out = await run();
    assert.ok(says(out, 'WEEKLY_REPORT'), out);
    const sent = backend.bark.since(mark);
    assert.equal(sent.length, 1);
    assert.equal(sent[0].key, STATUS_KEY);
    assert.equal(sent[0].fields.title, 'Alarm-Waechter: Wochenbericht');
    assert.equal(sent[0].fields.level, 'passive');
    const body = text(sent[0]);
    assert.match(body, /^Letzte 7 Tage - Relaisalarme: 1, manuelle Alarme: 0, Entwarnungen: 0, Ausfaelle der Box: 0\. /);
    assert.match(body, /Jetzt: Box online, 0 Empfaenger\.$/);

    out = await run();
    assert.ok(!says(out, 'WEEKLY_REPORT'), out);
    assert.equal(backend.bark.since(mark).length, 1);
  });
});

describe('Außen-Wächter', () => {
  it('nicht eingerichtet: der Lauf erwähnt ihn nicht', async () => {
    assert.doesNotMatch(await run(), /WATCHER_/);
  });

  it('eingerichtet, aber noch nie gemeldet: die Frist zählt ab dem ersten Lauf, dann "meldet sich nicht ... noch nie"', async () => {
    await withWatcher();
    const mark = backend.bark.mark();
    let out = await run();
    assert.ok(says(out, 'WATCHER_OK'), out);
    assert.ok(Math.abs((await backend.state.marks()).watcher_watch_since - now()) <= 5);
    assert.deepEqual(backend.bark.since(mark), []);

    await ageMark('watcher_watch_since', 3700);
    out = await run();
    assert.ok(says(out, 'WATCHER_SILENT_NOTIFIED'), out);
    const sent = backend.bark.since(mark);
    assert.equal(sent.length, 1);
    assert.equal(sent[0].key, STATUS_KEY);
    assert.equal(sent[0].fields.title, 'Aussen-Waechter meldet sich nicht');
    assert.equal(sent[0].fields.level, 'passive');
    const body = text(sent[0]);
    assert.match(body, /hat sich noch nie gemeldet\./);
    assert.match(body, /Ein Ausfall des Servers selbst wuerde so NICHT gemeldet\./);
    assert.equal((await systemLog('watcher_silent')).length, 1);
  });

  it('gemeldet, dann über 1 Std stumm: eine Meldung – nach dem nächsten Aufruf von alive.php "wieder da"', async () => {
    await withWatcher();
    await watcherSeen(3500);
    const mark = backend.bark.mark();
    let out = await run();
    assert.ok(says(out, 'WATCHER_OK'), out);
    assert.deepEqual(backend.bark.since(mark), []);

    await watcherSeen(3700);
    out = await run();
    assert.ok(says(out, 'WATCHER_SILENT_NOTIFIED'), out);
    let sent = backend.bark.since(mark);
    assert.equal(sent.length, 1);
    assert.equal(sent[0].fields.title, 'Aussen-Waechter meldet sich nicht');
    assert.match(text(sent[0]), /hat sich seit 62 min nicht gemeldet\./);
    const silent = await systemLog('watcher_silent');
    assert.equal(silent.length, 1);
    assert.equal(silent[0].minutes, 62);

    out = await run();
    assert.ok(parts(out).includes('WATCHER_SILENT (bereits gemeldet)'), out);
    assert.equal(backend.bark.since(mark).length, 1);

    // Der Außen-Wächter klopft wieder an.
    const alive = await postForm(`${backend.base}/api/alive.php`, { token: WATCHER_TOKEN, nonce: 'abc123' });
    assert.equal(alive.status, 200);
    assert.equal(alive.kv.ok, '1');
    out = await run();
    assert.ok(says(out, 'WATCHER_RECOVERED'), out);
    sent = backend.bark.since(mark);
    assert.equal(sent.length, 2);
    assert.equal(sent[1].key, STATUS_KEY);
    assert.equal(sent[1].fields.title, 'Aussen-Waechter wieder da');
    assert.match(text(sent[1]), /meldet sich wieder \(Pause ca\. 62 min\)\.$/);
    assert.equal((await systemLog('watcher_recovered')).length, 1);

    out = await run();
    assert.ok(says(out, 'WATCHER_OK'), out);
    assert.equal(backend.bark.since(mark).length, 2);
  });

  it('Frist nach der Meldung hochgesetzt, kein neuer Aufruf: KEIN "wieder da"', async () => {
    await withWatcher();
    await watcherSeen(3700);
    await run();
    await notifySet('outside_watcher', 'silent_after_s', 7200);
    const mark = backend.bark.mark();
    const out = await run();
    assert.ok(parts(out).includes('WATCHER_SILENT (bereits gemeldet)'), out);
    assert.deepEqual(backend.bark.since(mark), []);
    assert.deepEqual(await systemLog('watcher_recovered'), []);
  });

  it('bleibt er stumm, erinnert der Wächter ab Werk alle 3 Tage: "weiterhin stumm"', async () => {
    await withWatcher();
    await watcherSeen(3700);
    const mark = backend.bark.mark();
    await run();
    await ageMark('watcher_notified_at', 71 * 3600);
    assert.ok(parts(await run()).includes('WATCHER_SILENT (bereits gemeldet)'));
    await ageMark('watcher_notified_at', 73 * 3600);
    assert.ok(says(await run(), 'WATCHER_SILENT_NOTIFIED'));
    assert.deepEqual(toAdmin(mark).map((m) => m.fields.title),
      ['Aussen-Waechter meldet sich nicht', 'Aussen-Waechter weiterhin stumm']);
    assert.equal((await systemLog('watcher_silent')).length, 1, 'im Verlauf steht die Stille einmal');
  });

  it('Art abgeschaltet: OHNE_PUSH und ein Eintrag im Verlauf – nach dem Einschalten wird nachgeholt', async () => {
    await withWatcher();
    await notifySet('outside_watcher', 'enabled', 0);
    await watcherSeen(3700);
    const mark = backend.bark.mark();
    let out = await run();
    assert.ok(says(out, 'WATCHER_SILENT_NOTIFIED'), out);
    assert.match(out, /OHNE_PUSH [^|]*outside_watcher/);
    assert.deepEqual(backend.bark.since(mark), []);
    assert.equal((await systemLog('watcher_silent')).length, 1);

    await notifySet('outside_watcher', 'enabled', 1);
    out = await run();
    assert.ok(says(out, 'WATCHER_SILENT_NOTIFIED'), out);
    assert.ok(!says(out, 'OHNE_PUSH'), out);
    assert.deepEqual(toAdmin(mark).map((m) => m.fields.title), ['Aussen-Waechter meldet sich nicht']);
    assert.equal((await systemLog('watcher_silent')).length, 1);
  });
});

describe('Wie gemeldet wird (Einstellungen "Meldungen an mich")', () => {
  it('Dringlichkeit kritisch: level=critical mit Lautstärke 5; ein gewählter Ton geht mit', async () => {
    await notifySet('box_offline', 'level', 'critical');
    await silentBox();
    let mark = backend.bark.mark();
    await run();
    const [offline] = toAdmin(mark);
    assert.equal(offline.fields.title, 'Alarm-Box OFFLINE');
    assert.equal(offline.fields.level, 'critical');
    assert.equal(offline.fields.volume, '5');
    assert.equal('sound' in offline.fields, false, 'ohne Auswahl der Standardton');

    await notifySet('global', 'sound', 'bell');
    await notifySet('global', 'critical_volume', 8);
    await b.status();
    mark = backend.bark.mark();
    await run();
    const [online] = toAdmin(mark);
    assert.equal(online.fields.title, 'Alarm-Box wieder ONLINE');
    assert.equal(online.fields.level, 'critical');
    assert.equal(online.fields.volume, '8');
    assert.equal(online.fields.sound, 'bell');
  });

  it('Ruhezeit: eine kritische Meldung kommt nur leise an – außer die Art ist davon ausgenommen', async () => {
    // Von der laufenden Stunde an zwei Stunden lang (auch über einen Stundenwechsel im Test).
    const { hour } = serverClock();
    await notifySet('global', 'quiet_from', hour);
    await notifySet('global', 'quiet_to', (hour + 2) % 24);
    await notifySet('global', 'quiet_enabled', 1);
    await notifySet('global', 'sound', 'bell');
    await notifySet('box_offline', 'level', 'critical');
    await silentBox();
    let mark = backend.bark.mark();
    assert.ok(parts(await run()).includes('NOTIFIED_OFFLINE'));
    const [capped] = toAdmin(mark);
    assert.equal(capped.fields.title, 'Alarm-Box OFFLINE');
    assert.equal(capped.fields.level, 'passive');
    assert.equal('volume' in capped.fields, false);
    assert.equal('sound' in capped.fields, false);

    await notifySet('box_offline', 'ignore_quiet', 1);
    await b.status();
    mark = backend.bark.mark();
    await run();
    const [loud] = toAdmin(mark);
    assert.equal(loud.fields.title, 'Alarm-Box wieder ONLINE');
    assert.equal(loud.fields.level, 'critical');
    assert.equal(loud.fields.volume, '5');
    assert.equal(loud.fields.sound, 'bell');
  });

  it('Stellvertreter: dieselbe Meldung geht an den Status-Key und an ihn', async () => {
    const deputy = await admin.addKey('Vertretung', 'deputykey001');
    await notifySet('global', 'extra_id', deputy.id);
    await silentBox();
    const mark = backend.bark.mark();
    const out = await run();
    assert.ok(parts(out).includes('NOTIFIED_OFFLINE'), out);
    const sent = backend.bark.since(mark);
    assert.deepEqual(sent.map((m) => m.key).sort(), ['deputykey001', STATUS_KEY].sort());
    assert.deepEqual(sent[0].fields, sent[1].fields);
    assert.equal(sent[0].fields.title, 'Alarm-Box OFFLINE');
  });

  it('scheitert der Status-Key, der Stellvertreter nimmt aber an: gemeldet – und der Status-Key gilt als gestört', async () => {
    const deputy = await admin.addKey('Vertretung', 'deputykey001');
    await notifySet('global', 'extra_id', deputy.id);
    await backend.configure({ bark_key_status: 'failstatus001' });   // Attrappe antwortet 500
    await silentBox();
    const mark = backend.bark.mark();
    let out = await run();
    assert.ok(parts(out).includes('NOTIFIED_OFFLINE'), out);
    assert.equal(backend.bark.to('deputykey001', mark).length, 1);
    assert.equal(backend.bark.to('deputykey001', mark)[0].fields.title, 'Alarm-Box OFFLINE');
    assert.ok(backend.bark.to('failstatus001', mark).length >= 1);
    assert.equal((await systemLog('box_offline')).length, 1);
    const wd = await watchdogStatus();
    assert.ok(Math.abs(wd.last_send_failed_at - now()) <= 5);
    assert.ok(wd.last_send_failed_at > wd.last_send_ok_at);

    // Gemeldet ist gemeldet: Der Stellvertreter bekommt sie nicht bei jedem Lauf erneut.
    out = await run();
    assert.ok(parts(out).includes('STILL_OFFLINE (bereits gemeldet)'), out);
    assert.equal(backend.bark.to('deputykey001', mark).length, 1);
  });
});
