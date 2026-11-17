// Befehle an die Box (api/poll.php, api/ack.php): Klartext-Protokoll mit
// gespiegelter Nonce, Auslieferung höchstens einmal, Quittung, Verfall und
// Abbruch, Nummern über Datenverlust hinweg, die Sicherungen des Demo-Alarms
// - und was der Poll nebenbei aufräumt (überfällige Stummschaltungen,
// kaputter Demo-Zustand). Der Poll ist der Takt der Box: Antwortet er falsch,
// alarmiert sie mit einer alten Liste oder gar nicht mehr über das Dashboard.
import assert from 'node:assert/strict';
import { after, before, beforeEach, describe, it } from 'node:test';
import {
  MACHINE_TOKEN, box, now, postForm, request, session, startBackend, waitFor,
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

const enqueue = (type, extra = {}) => admin.post({ action: 'enqueue', type, ...extra });
const command = async () => (await admin.get('status')).command;
const stored = () => backend.data.read('command_state.json').current;
const editCommand = (fn) => backend.data.edit('command_state.json', (d) => fn(d.current));
const events = async (filter) => (await admin.log(filter)).map((e) => e.event);
const none = (nonce, version) => `ok=1\nnonce=${nonce}\nid=0\ntype=NONE\nkeys_version=${version}\n`;

// Demo-Modus einschalten und die Box die Demo-Liste "übernehmen" lassen
// (Status-Push mit aktueller Version und genau einem Empfänger).
async function demoReady(push = (fields) => b.status(fields)) {
  const anna = await admin.addKey('Anna', 'annakey00001');
  await admin.addKey('Bert', 'bertkey00001');
  const on = await admin.post({ action: 'demo_set', enabled: '1', id: anna.id });
  assert.equal(on.ok, true, JSON.stringify(on));
  const version = (await admin.keys()).version;
  const confirm = (extra = {}) => push({ keys_version: String(version), keys_count: '1', ...extra });
  await confirm();
  return { anna, version, confirm };
}

describe('Anmeldung mit dem Maschinen-Token', () => {
  it('Poll, Quittung und Listen-Abruf ohne gültiges Token: 403 FORBIDDEN', async () => {
    for (const name of ['poll', 'ack', 'keys']) {
      const url = `${backend.base}/api/${name}.php`;
      for (const r of [
        await request(`${url}?token=${MACHINE_TOKEN}`),
        await postForm(url, { nonce: 'n1' }),
        await postForm(url, { token: 'falsch', nonce: 'n1' }),
      ]) {
        assert.equal(r.status, 403, name);
        assert.equal(r.text, 'FORBIDDEN', name);
      }
    }
  });
});

describe('Poll ohne Befehl', () => {
  it('antwortet zeichengenau: ok, Nonce, id=0, type=NONE, keys_version', async () => {
    const r = await b.poll({ nonce: 'abc123' });
    assert.equal(r.status, 200);
    assert.equal(r.text, none('abc123', 0));
    assert.match(r.headers.get('content-type'), /^text\/plain/);
    assert.match(r.headers.get('cache-control'), /no-store/);
  });

  it('spiegelt die Nonce bereinigt (nur A-Z a-z 0-9 _ -, höchstens 80 Zeichen)', async () => {
    assert.equal((await b.poll({ nonce: 'ab.c!d-e_f g' })).kv.nonce, 'abcd-e_fg');
    assert.equal((await b.poll({ nonce: 'x'.repeat(100) })).kv.nonce, 'x'.repeat(80));
    assert.equal((await b.poll({ nonce: undefined })).text, none('', 0));
  });

  it('meldet immer die aktuelle Version der Empfängerliste', async () => {
    await admin.addKey('Anna', 'annakey00001');
    const version = (await admin.keys()).version;
    assert.ok(version >= 1);
    assert.equal((await b.poll()).kv.keys_version, String(version));
    await admin.addKey('Bert', 'bertkey00001');
    assert.equal((await b.poll()).kv.keys_version, String(version + 1));
  });
});

describe('TEST-Befehl: Auslieferung höchstens einmal', () => {
  it('anlegen, abholen, quittieren - und nie ein zweites Mal ausliefern', async () => {
    const created = await enqueue('TEST');
    assert.equal(created.ok, true);
    assert.equal(created.command.id, 1);
    assert.equal(created.command.state, 'pending');
    assert.equal(created.command.created_by, 'admin');

    const first = await b.poll({ nonce: 'p1' });
    assert.equal(first.text, 'ok=1\nnonce=p1\nid=1\ntype=TEST\nkeys_version=0\n');
    let current = await command();
    assert.equal(current.state, 'delivered');
    assert.equal(current.delivered_to, 'fw-box-1');

    assert.equal((await b.poll({ nonce: 'p2' })).text, none('p2', 0), 'zweiter Poll: kein Befehl');

    const acked = await b.ack({ id: 1, result: 'executed', message: 'gesendet' });
    assert.equal(acked.status, 200);
    assert.equal(acked.text, 'OK\n');
    current = await command();
    assert.equal(current.state, 'acked');
    assert.equal(current.ack_result, 'executed');
    assert.equal(current.ack_message, 'gesendet');
    assert.equal(current.acked_by, 'fw-box-1');

    assert.equal((await b.poll({ nonce: 'p3' })).text, none('p3', 0), 'nach der Quittung: kein Befehl');

    const log = (await admin.log('system')).filter((e) => e.id === 1).reverse();
    assert.deepEqual(log.map((e) => [e.event, e.type]), [['created', 'TEST'], ['delivered', 'TEST'], ['acked', 'TEST']]);
    assert.equal(log[2].result, 'executed');
  });

  it('dieselbe Quittung noch einmal: bestätigt, aber kein zweiter Eintrag und keine neue Zeit', async () => {
    await enqueue('TEST');
    await b.poll();
    await b.ack({ id: 1, result: 'executed' });
    editCommand((c) => { c.acked_at = 12345; });
    const again = await b.ack({ id: 1, result: 'executed' });
    assert.equal(again.text, 'OK\n');
    assert.equal(stored().acked_at, 12345);
    assert.equal((await events('system')).filter((e) => e === 'acked').length, 1);
  });

  it('Quittung für eine unbekannte Nummer: bestätigt und als verspätet vermerkt', async () => {
    const r = await b.ack({ id: 99, result: 'executed' });
    assert.equal(r.status, 200);
    assert.equal(r.text, 'OK\n');
    const late = (await admin.log('system')).filter((e) => e.event === 'late_ack');
    assert.equal(late.length, 1);
    assert.equal(late[0].id, 99);
    assert.equal(late[0].result, 'executed');
  });

  it('ein noch nicht abgeholter Befehl lässt sich nicht quittieren', async () => {
    await enqueue('TEST');
    assert.equal((await b.ack({ id: 1, result: 'executed' })).text, 'OK\n');
    assert.equal((await command()).state, 'pending');
    const late = (await admin.log('system')).filter((e) => e.event === 'late_ack');
    assert.equal(late.length, 1);
    assert.equal(late[0].type, 'TEST');
    // ... und wird danach ganz normal ausgeliefert.
    assert.equal((await b.poll()).kv.type, 'TEST');
  });

  it('die verspätete Quittung eines abgelösten Befehls kennt noch dessen Typ', async () => {
    await enqueue('TEST');
    await b.poll();
    editCommand((c) => { c.delivered_at = now() - 130; });
    assert.equal((await enqueue('TEST')).command.id, 2);
    await b.ack({ id: 1, result: 'executed' });
    const late = (await admin.log('system')).filter((e) => e.event === 'late_ack');
    assert.deepEqual(late.map((e) => [e.id, e.type]), [[1, 'TEST']]);
    assert.equal((await command()).state, 'pending', 'der neue Befehl bleibt unberührt');
  });

  it('kürzt Ergebnis (80) und Meldung (200) der Quittung', async () => {
    await enqueue('TEST');
    await b.poll();
    await b.ack({ id: 1, result: 'r'.repeat(120), message: 'm'.repeat(300) });
    const current = await command();
    assert.equal(current.ack_result.length, 80);
    assert.equal(current.ack_message.length, 200);
  });
});

describe('Sperren beim Anlegen', () => {
  it('solange ein Befehl wartet, gibt es keinen zweiten', async () => {
    await enqueue('TEST');
    const second = await enqueue('TEST');
    assert.equal(second.ok, false);
    assert.match(second.message, /wartet bereits ein Befehl/);
    assert.equal((await command()).id, 1);
  });

  it('nach der Auslieferung 120 s Sperre, falls die Quittung ausbleibt - danach wieder frei', async () => {
    await enqueue('TEST');
    await b.poll();
    const blocked = await enqueue('TEST');
    assert.equal(blocked.ok, false);
    assert.match(blocked.message, /Bestätigung steht noch aus/);
    editCommand((c) => { c.delivered_at = now() - 121; });
    assert.equal((await enqueue('TEST')).ok, true);
  });

  it('nach der Quittung sofort wieder frei, die Nummern steigen', async () => {
    for (const id of [1, 2, 3]) {
      assert.equal((await enqueue('TEST')).command.id, id);
      assert.equal((await b.poll()).kv.id, String(id));
      await b.ack({ id });
    }
  });

  it('unbekannter Befehlstyp wird abgelehnt', async () => {
    const r = await enqueue('RESET');
    assert.equal(r.ok, false);
    assert.equal(await command(), null);
  });
});

describe('Abbrechen und Verfallen', () => {
  it('ein abgebrochener Befehl wird nicht mehr ausgeliefert', async () => {
    await enqueue('TEST');
    const cancelled = await admin.post({ action: 'cancel' });
    assert.equal(cancelled.ok, true);
    assert.equal((await b.poll()).kv.type, 'NONE');
    const current = await command();
    assert.equal(current.state, 'cancelled');
    assert.equal(current.cancelled_by, 'admin');
    assert.ok((await events('system')).includes('cancelled'));
  });

  it('ohne wartenden Befehl gibt es nichts abzubrechen', async () => {
    assert.equal((await admin.post({ action: 'cancel' })).ok, false);
    await enqueue('TEST');
    await b.poll();
    assert.equal((await admin.post({ action: 'cancel' })).ok, false, 'ausgeliefert ist nicht mehr abbrechbar');
  });

  it('nach 300 s ohne Abholung verfällt der Befehl - die Box bekommt ihn nicht mehr', async () => {
    await enqueue('TEST');
    editCommand((c) => { c.created_at = now() - 301; });
    assert.equal((await b.poll()).kv.type, 'NONE');
    assert.equal((await command()).state, 'expired');
    assert.ok((await events('system')).includes('expired'));
    assert.equal((await enqueue('TEST')).ok, true, 'danach ist das Dashboard wieder frei');
  });

  it('er verfällt auch ohne pollende Box, sobald das Dashboard nachsieht', async () => {
    await enqueue('TEST');
    editCommand((c) => { c.created_at = now() - 301; });
    assert.equal((await command()).state, 'expired');
    assert.equal(stored().state, 'expired', 'gespeichert, nicht nur angezeigt');
  });

  it('kurz vor Ablauf wird noch ausgeliefert; die Frist ist einstellbar', async () => {
    await enqueue('TEST');
    editCommand((c) => { c.created_at = now() - 290; });
    assert.equal((await b.poll()).kv.type, 'TEST');
    await b.ack({ id: 1 });

    await backend.configure({ pending_command_ttl_seconds: 60 });
    await enqueue('TEST');
    editCommand((c) => { c.created_at = now() - 61; });
    assert.equal((await b.poll()).kv.type, 'NONE');
  });
});

describe('Befehls-Nummern über Datenverlust hinweg', () => {
  it('kennt die Box schon höhere Nummern, bekommt der wartende Befehl eine neue', async () => {
    await enqueue('TEST');
    const r = await b.poll({ last_command_id: 50 });
    assert.equal(r.kv.id, '51');
    assert.equal(r.kv.type, 'TEST');
    assert.equal((await command()).id, 51);
    await b.ack({ id: 51 });
    assert.equal((await command()).state, 'acked');
    assert.equal((await enqueue('TEST')).command.id, 52);
  });

  it('auch ohne wartenden Befehl merkt sich der Server die Nummer der Box', async () => {
    await b.poll({ last_command_id: 50 });
    assert.equal((await enqueue('TEST')).command.id, 51);
  });
});

describe('Welche Box bekommt Befehle (box_device_id)', () => {
  it('ein fremdes Board mit dem Token schnappt der Box nichts weg', async () => {
    await backend.configure({ box_device_id: 'fw-box-1' });
    await enqueue('TEST');
    const foreign = await box(backend, { device_id: 'bastel-1' }).poll();
    assert.equal(foreign.status, 200);
    assert.equal(foreign.kv.type, 'NONE');
    assert.equal(foreign.kv.keys_version, '0', 'die Listen-Version bekommt es trotzdem');
    assert.equal((await command()).state, 'pending');
    assert.equal((await b.poll()).kv.type, 'TEST');
  });
});

describe('Demo-Alarm über die Box (ALARM-Befehl)', () => {
  it('im LIVE-Betrieb gibt es ihn nicht', async () => {
    await admin.addKey('Anna', 'annakey00001');
    const r = await enqueue('ALARM', { expect_demo: '0' });
    assert.equal(r.ok, false);
    assert.match(r.message, /nur im Demo-Modus/);
    assert.equal(await command(), null);
  });

  it('ohne bestätigten Modus (expect_demo) oder mit falschem: 409, nichts angelegt', async () => {
    const { anna } = await demoReady();
    for (const extra of [{}, { expect_demo: '0' }, { expect_demo: String(anna.id + 1) }]) {
      const r = await enqueue('ALARM', extra);
      assert.equal(r.http, 409, JSON.stringify(extra));
      assert.equal(r.code, 'mode_changed');
    }
    assert.equal(await command(), null);
  });

  it('erst wenn die Box die Demo-Liste bestätigt hat (Version, genau 1 Empfänger, frisch)', async () => {
    const { anna, version, confirm } = await demoReady();
    const expect = { expect_demo: String(anna.id) };

    await b.status({ keys_version: String(version - 1), keys_count: '1' });
    let r = await enqueue('ALARM', expect);
    assert.equal(r.ok, false, 'alte Version');
    assert.match(r.message, /noch nicht übernommen oder ist offline/);
    assert.equal((await admin.get('status')).demo_sync_ok, false);

    await b.status({ keys_version: String(version), keys_count: '2' });
    assert.equal((await enqueue('ALARM', expect)).ok, false, 'meldet zwei Empfänger');

    await confirm();
    backend.data.edit('latest_status.json', (d) => { d.seen_at = now() - 181; });
    assert.equal((await enqueue('ALARM', expect)).ok, false, 'Status veraltet');

    await confirm();
    assert.equal((await admin.get('status')).demo_sync_ok, true);
    r = await enqueue('ALARM', expect);
    assert.equal(r.ok, true, JSON.stringify(r));
    assert.equal(r.command.type, 'ALARM');
    const polled = await b.poll({ nonce: 'a1' });
    assert.equal(polled.text, `ok=1\nnonce=a1\nid=1\ntype=ALARM\nkeys_version=${version}\n`);
    // Die Befehls-Ereignisse eines Demo-Alarms gehören in den Alarm-Verlauf.
    assert.deepEqual((await events('alarm')).filter((e) => ['created', 'delivered'].includes(e)).reverse(),
      ['created', 'delivered']);
  });

  it('wird der Demo-Modus nach dem Anlegen beendet, liefert der Poll den Alarm NICHT mehr aus', async () => {
    const { anna } = await demoReady();
    assert.equal((await enqueue('ALARM', { expect_demo: String(anna.id) })).ok, true);
    assert.equal((await admin.post({ action: 'demo_set', enabled: '0' })).ok, true);
    const polled = await b.poll();
    assert.equal(polled.kv.type, 'NONE');
    const current = await command();
    assert.equal(current.state, 'cancelled');
    assert.equal(current.cancelled_by, 'Sicherung');
    const cancelled = (await admin.log('alarm')).find((e) => e.event === 'cancelled');
    assert.match(cancelled.message, /Demo-Stand seit dem Anlegen geändert/);
  });

  it('auch aus- und wieder eingeschaltet zählt als geändert', async () => {
    const { anna, confirm } = await demoReady();
    // Der bestätigte Demo-Stand ist älter als "jetzt" (sonst fiele ein
    // Wechsel in derselben Sekunde nicht auf).
    backend.data.edit('demo_mode.json', (d) => { d.changed_at = now() - 100; });
    assert.equal((await enqueue('ALARM', { expect_demo: String(anna.id) })).ok, true);
    await admin.post({ action: 'demo_set', enabled: '0' });
    await admin.post({ action: 'demo_set', enabled: '1', id: anna.id });
    await confirm({ keys_version: String((await admin.keys()).version) });
    assert.equal((await b.poll()).kv.type, 'NONE');
    assert.equal((await command()).state, 'cancelled');
  });

  it('meldet die Box beim Abholen nicht mehr genau einen Empfänger: nicht ausliefern', async () => {
    const { anna, version } = await demoReady();
    assert.equal((await enqueue('ALARM', { expect_demo: String(anna.id) })).ok, true);
    await b.status({ keys_version: String(version), keys_count: '2' });
    assert.equal((await b.poll()).kv.type, 'NONE');
    assert.equal((await command()).state, 'cancelled');
  });
});

describe('Demo oder Relais? Quelle eines Alarms bei alter Firmware (ohne alarm_source)', () => {
  const old = (fields) => b.oldStatus(fields);
  const relayAlarms = async () => (await admin.log('alarm')).filter((e) => e.event === 'relay_alarm');
  // Demo-Alarm anlegen und abholen lassen; danach meldet die Box einen Alarm.
  async function deliveredDemoAlarm() {
    const ready = await demoReady(old);
    assert.equal((await enqueue('ALARM', { expect_demo: String(ready.anna.id) })).ok, true);
    assert.equal((await b.poll()).kv.type, 'ALARM');
    return ready;
  }

  it('kurz nach der Auslieferung (unter 60 s, Quittung steht aus): Demo', async () => {
    const { confirm } = await deliveredDemoAlarm();
    await confirm({ last_alarm: '16.11.2026 12:00:00' });
    assert.deepEqual((await relayAlarms()).map((e) => e.source), ['remote']);
  });

  it('Auslieferung länger als 60 s her und keine Quittung: Relais', async () => {
    const { confirm } = await deliveredDemoAlarm();
    editCommand((c) => { c.delivered_at = now() - 61; });
    await confirm({ last_alarm: '16.11.2026 12:00:00' });
    assert.deepEqual((await relayAlarms()).map((e) => e.source), ['relay']);
  });

  it('quittiert als ausgeführt: bis 300 s nach der Auslieferung Demo, danach Relais', async () => {
    const { confirm } = await deliveredDemoAlarm();
    await b.ack({ id: 1, result: 'executed' });
    editCommand((c) => { c.delivered_at = now() - 200; });
    await confirm({ last_alarm: 'A' });
    editCommand((c) => { c.delivered_at = now() - 301; });
    await confirm({ last_alarm: 'B' });
    assert.deepEqual((await relayAlarms()).map((e) => [e.info, e.source]), [['B', 'relay'], ['A', 'remote']]);
  });

  it('quittiert als übersprungen (skipped_busy): ein Alarm kurz danach ist echt', async () => {
    const { confirm } = await deliveredDemoAlarm();
    await b.ack({ id: 1, result: 'skipped_busy' });
    await confirm({ last_alarm: '16.11.2026 12:00:00' });
    assert.deepEqual((await relayAlarms()).map((e) => e.source), ['relay']);
  });

  it('ein TEST-Befehl macht aus einem Alarm nie einen Demo-Alarm', async () => {
    await old();
    await enqueue('TEST');
    await b.poll();
    await old({ last_alarm: '16.11.2026 12:00:00' });
    assert.deepEqual((await relayAlarms()).map((e) => e.source), ['relay']);
  });
});

describe('Der Poll räumt auf', () => {
  const muteAged = async (label, key, seconds, extra = {}) => {
    const entry = await admin.addKey(label, key);
    assert.equal((await admin.post({ action: 'keys_mute', id: entry.id, muted: '1' })).ok, true);
    backend.data.edit('alarm_keys.json', (d) => {
      Object.assign(d.keys.find((k) => k.id === entry.id), { muted_at: now() - seconds }, extra);
    });
    return entry;
  };
  const entryOf = async (id) => (await admin.keys()).keys.find((k) => k.id === id);

  it('eine überfällige Stummschaltung (über 12 h) endet mit dem nächsten Poll - neue Version sofort in der Antwort', async () => {
    const anna = await muteAged('Anna', 'annakey00001', 43300);
    const before = backend.data.read('alarm_keys.json').version;
    const mark = backend.bark.mark();

    const polled = await b.poll();
    assert.equal(polled.status, 200);
    assert.equal(polled.kv.keys_version, String(before + 1));

    const entry = await entryOf(anna.id);
    assert.equal(entry.muted, false);
    assert.equal(entry.mute_overdue, false);
    const log = (await admin.log('keys')).find((e) => e.event === 'key_unmuted');
    assert.equal(log.source, 'auto');
    assert.equal(log.label, 'Anna');
    // Der Betroffene erfährt es leise - der Poll wartet darauf nicht.
    const [info] = await waitFor(() => backend.bark.to('annakey00001', mark), 'Info "wieder LAUT"');
    assert.equal(info.fields.title, 'Alarmton wieder LAUT');
    assert.equal(info.fields.level, 'passive');
  });

  it('innerhalb der Frist und bei "dauerhaft stumm" bleibt es still', async () => {
    const anna = await muteAged('Anna', 'annakey00001', 43000);
    const bert = await muteAged('Bert', 'bertkey00001', 90000, { mute_permanent: true });
    const before = backend.data.read('alarm_keys.json').version;
    assert.equal((await b.poll()).kv.keys_version, String(before));
    assert.equal((await entryOf(anna.id)).muted, true);
    const permanent = await entryOf(bert.id);
    assert.equal(permanent.muted, true);
    assert.equal(permanent.mute_permanent, true);
  });

  it('mute_max_seconds = 0 schaltet die automatische Rückschaltung ab', async () => {
    await backend.configure({ mute_max_seconds: 0 });
    const anna = await muteAged('Anna', 'annakey00001', 400000);
    await b.poll();
    assert.equal((await entryOf(anna.id)).muted, true);
  });

  it('die im Dashboard gewählte Dauer gilt vor dem Wert der Konfiguration', async () => {
    const anna = await muteAged('Anna', 'annakey00001', 7300);
    await b.poll();
    assert.equal((await entryOf(anna.id)).muted, true, '12 h aus der Konfiguration');
    assert.equal((await admin.post({ action: 'settings_set', key: 'mute_max_seconds', value: '7200' })).ok, true);
    await b.poll();
    assert.equal((await entryOf(anna.id)).muted, false, '2 h aus dem Dashboard');
  });

  it('ein kaputter Demo-Zustand wird auf LIVE repariert - mit neuer Listen-Version', async () => {
    const { version } = await demoReady();
    backend.data.corrupt('demo_mode.json');
    const polled = await b.poll();
    assert.equal(polled.kv.keys_version, String(version + 1));
    assert.equal((await admin.get('status')).demo.enabled, false);
    assert.ok((await events('system')).includes('demo_repaired'));
    assert.equal((await b.keys()).kv.count, '2', 'die Box bekommt wieder die volle Liste');
    // Einmal repariert, bleibt es dabei.
    assert.equal((await b.poll()).kv.keys_version, String(version + 1));
  });

  it('auch ein verschwundener Demo-Zustand (Sicherung noch da) wird repariert', async () => {
    const { anna } = await demoReady();
    await admin.post({ action: 'demo_set', enabled: '0' });
    await admin.post({ action: 'demo_set', enabled: '1', id: anna.id });
    const version = (await admin.keys()).version;
    backend.data.remove('demo_mode.json');
    assert.equal(backend.data.exists('demo_mode.json.bak'), true);
    assert.equal((await b.poll()).kv.keys_version, String(version + 1));
    assert.ok((await events('system')).includes('demo_repaired'));
  });

  it('ohne je einen Demo-Zustand gibt es nichts zu reparieren', async () => {
    await admin.addKey('Anna', 'annakey00001');
    const version = (await admin.keys()).version;
    assert.equal((await b.poll()).kv.keys_version, String(version));
    assert.equal((await events('system')).includes('demo_repaired'), false);
  });
});
