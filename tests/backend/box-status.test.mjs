// Status-Push der Box (api/status.php): Anmeldung mit dem Maschinen-Token,
// was gespeichert wird, Relaisalarme und Neustarts im Verlauf, veraltete
// Pushs, fremde Boards. Jede Regel hier muss auch eine neue Server-Version
// erfüllen - mit der alten UND der neuen Firmware.
import assert from 'node:assert/strict';
import { after, before, beforeEach, describe, it } from 'node:test';
import {
  BOX_FIELDS, MACHINE_TOKEN, STATUS_KEY, box, now, postForm, request, session, settle, sha256,
  startBackend, waitFor,
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

const relayAlarms = async () => (await admin.log('alarm')).filter((e) => e.event === 'relay_alarm');
const restarts = async () => (await admin.log('system')).filter((e) => e.event === 'box_restart');
// Den gespeicherten Stand "s Sekunden alt" machen (als wäre so lange nichts gekommen).
const ageStatus = (seconds) => backend.data.edit('latest_status.json', (d) => { d.seen_at = now() - seconds; });

describe('Anmeldung mit dem Maschinen-Token', () => {
  it('ohne gültiges Token: 403 FORBIDDEN, nichts gespeichert', async () => {
    const url = `${backend.base}/api/status.php`;
    const tries = [
      await request(`${url}?token=${MACHINE_TOKEN}`),                       // GET
      await postForm(url, { ...BOX_FIELDS }),                               // kein Token
      await postForm(url, { token: 'falsch', ...BOX_FIELDS }),              // falsches Token
      await postForm(`${url}?token=${MACHINE_TOKEN}`, { ...BOX_FIELDS }),   // Token nur in der Adresse
    ];
    for (const r of tries) {
      assert.equal(r.status, 403);
      assert.equal(r.text, 'FORBIDDEN');
    }
    assert.equal(backend.data.read('latest_status.json'), null);
  });

  it('der Hash in der Konfiguration darf groß geschrieben sein und Leerzeichen haben', async () => {
    await backend.configure({ machine_token_sha256: `  ${sha256(MACHINE_TOKEN).toUpperCase()} ` });
    assert.equal((await b.status()).status, 200);
  });

  it('ohne eingetragenen Hash kommt niemand durch', async () => {
    await backend.configure({ machine_token_sha256: '' });
    assert.equal((await b.status()).status, 403);
    assert.equal((await postForm(`${backend.base}/api/status.php`, { token: '' })).status, 403);
  });
});

describe('Status-Push', () => {
  it('antwortet "OK" als Klartext und nicht zwischenspeicherbar', async () => {
    const r = await b.status();
    assert.equal(r.status, 200);
    assert.equal(r.text, 'OK\n');
    assert.match(r.headers.get('content-type'), /^text\/plain/);
    assert.match(r.headers.get('cache-control'), /no-store/);
    assert.equal(r.headers.get('x-content-type-options'), 'nosniff');
  });

  it('speichert die Felder ohne das Token; das Dashboard zeigt sie ohne Absender-Adresse', async () => {
    await b.status({ rssi: '-70', relay: 'closed' });
    const stored = backend.data.read('latest_status.json');
    assert.equal(stored.rssi, '-70');
    assert.equal(stored.device_id, 'fw-box-1');
    assert.equal('token' in stored, false);
    assert.ok(Math.abs(stored.seen_at - now()) <= 5, 'seen_at = Ankunft');
    assert.equal(stored.via_bridge, '0');

    const st = await admin.get('status');
    assert.equal(st.ok, true);
    assert.equal(st.status.rssi, '-70');
    assert.equal(st.status.relay, 'closed');
    assert.equal(st.offline, false);
    assert.ok(st.seen_age_seconds >= 0 && st.seen_age_seconds <= 5);
    assert.equal('remote_addr' in st.status, false);
    assert.equal('token' in st.status, false);
  });

  it('noch nie ein Push: das Dashboard meldet offline ohne Alter', async () => {
    const st = await admin.get('status');
    assert.equal(st.offline, true);
    assert.equal(st.seen_age_seconds, null);
  });

  it('nach offline_after_seconds ohne Push gilt die Box als offline', async () => {
    await b.status();
    ageStatus(181);
    assert.equal((await admin.get('status')).offline, true);
    ageStatus(170);
    assert.equal((await admin.get('status')).offline, false);
  });

  it('während einer laufenden Alarm-Zustellung erst nach 10 min offline', async () => {
    await b.status({ alarm_pending: '1' });
    ageStatus(400);
    assert.equal((await admin.get('status')).offline, false);
    ageStatus(601);
    assert.equal((await admin.get('status')).offline, true);
  });

  it('über die Brücke gekommen? Es zählt die Kopfzeile X-FW-Bridge, kein Feld', async () => {
    await b.status({}, { 'X-FW-Bridge': '1' });
    assert.equal(backend.data.read('latest_status.json').via_bridge, '1');
    await b.status({ via_bridge: '1' });
    assert.equal(backend.data.read('latest_status.json').via_bridge, '0');
  });

  it('kürzt Werte auf 300 Zeichen, säubert Feldnamen, nimmt höchstens 40 Felder', async () => {
    const fields = { token: MACHINE_TOKEN, device_id: 'fw-box-1', uptime_s: '10', lang: 'x'.repeat(500), 'we!rd': '1' };
    for (let i = 0; i < 60; i++) fields[`f${i}`] = String(i);
    const r = await postForm(`${backend.base}/api/status.php`, fields);
    assert.equal(r.status, 200);
    const stored = backend.data.read('latest_status.json');
    assert.equal(stored.lang.length, 300);
    assert.equal(stored.werd, '1');
    const own = Object.keys(stored).filter((k) => !['seen_at', 'remote_addr', 'via_bridge'].includes(k));
    assert.equal(own.length, 40);
    assert.equal('f59' in stored, false);
  });

  it('die alte Firmware (ohne alarm_seq, core ...) wird genauso angenommen', async () => {
    const r = await b.oldStatus({ rssi: '-66' });
    assert.equal(r.text, 'OK\n');
    const st = await admin.get('status');
    assert.equal(st.status.rssi, '-66');
    assert.equal('alarm_seq' in st.status, false);
    assert.equal(st.offline, false);
  });

  it('lässt sich die Sperre des Datenordners nicht öffnen: HTTP 500 mit lesbarem Hinweis, ohne Pfad', async () => {
    backend.data.block('status.lock');
    try {
      const r = await b.status();
      assert.equal(r.status, 500);
      assert.equal(r.text, 'Fehler: Datenordner nicht beschreibbar (Rechte des Web-Benutzers auf dem Server prüfen).');
      assert.match(r.headers.get('content-type'), /^text\/plain/);
    } finally {
      backend.data.unblock('status.lock');
    }
    assert.equal((await b.status()).text, 'OK\n', 'danach geht es wieder');
  });

  it('lässt sich der Status nicht speichern: HTTP 500 WRITE_FAILED statt eines falschen OK', async () => {
    backend.data.block('latest_status.json');
    try {
      const r = await b.status();
      assert.equal(r.status, 500);
      assert.equal(r.text, 'WRITE_FAILED\n');
    } finally {
      backend.data.unblock('latest_status.json');
    }
  });
});

describe('Relaisalarm im Verlauf', () => {
  it('der erste Push einer Box verbucht ihren alten Alarm nicht nachträglich', async () => {
    await b.status({ last_alarm: '16.11.2026 10:00:00', alarm_seq: '3' });
    assert.deepEqual(await relayAlarms(), []);
  });

  it('ein neuer Wert von last_alarm ist EIN Eintrag - Wiederholungen keiner', async () => {
    await b.status();
    await b.status({ last_alarm: '16.11.2026 10:05:00', alarm_seq: '1' });
    await b.status({ last_alarm: '16.11.2026 10:05:00', alarm_seq: '1' });
    await b.status({ last_alarm: '16.11.2026 10:05:00', alarm_seq: '1' });
    const found = await relayAlarms();
    assert.equal(found.length, 1);
    assert.equal(found[0].info, '16.11.2026 10:05:00');
    assert.equal(found[0].source, 'relay');
  });

  it('gleicher Text, neuer Zähler alarm_seq: ein weiterer Alarm', async () => {
    await b.status();
    await b.status({ last_alarm: 'ohne Uhrzeit', alarm_seq: '1' });
    await b.status({ last_alarm: 'ohne Uhrzeit', alarm_seq: '2' });
    assert.equal((await relayAlarms()).length, 2);
  });

  it('der Wechsel zurück auf "nie" ist ein Neustart der Box, kein Alarm', async () => {
    await b.status();
    await b.status({ last_alarm: '16.11.2026 10:05:00', alarm_seq: '1' });
    await b.status({ last_alarm: 'nie', alarm_seq: '0' });
    assert.equal((await relayAlarms()).length, 1);
    // Derselbe Alarm-Text nach dem Neustart zählt wieder als neuer Alarm.
    await b.status({ last_alarm: '16.11.2026 10:05:00', alarm_seq: '1' });
    assert.equal((await relayAlarms()).length, 2);
  });

  it('meldet die Firmware die Quelle selbst (alarm_source), steht sie so im Verlauf', async () => {
    await b.status();
    await b.status({ last_alarm: 'A', alarm_seq: '1', alarm_source: 'remote' });
    await b.status({ last_alarm: 'B', alarm_seq: '2', alarm_source: 'relay' });
    const found = await relayAlarms();
    assert.deepEqual(found.map((e) => [e.info, e.source]), [['B', 'relay'], ['A', 'remote']]);
  });

  it('alte Firmware ohne Zähler und Quelle: Alarm wird erkannt, Quelle relay', async () => {
    await b.oldStatus();
    await b.oldStatus({ last_alarm: '16.11.2026 11:00:00' });
    await b.oldStatus({ last_alarm: '16.11.2026 11:00:00' });
    const found = await relayAlarms();
    assert.equal(found.length, 1);
    assert.equal(found[0].source, 'relay');
  });

  it('der Stand wird pro Box gemerkt: ein zweites Board dazwischen ändert nichts', async () => {
    const other = box(backend, { device_id: 'bastel-1' });
    await b.status();
    await b.status({ last_alarm: 'A1', alarm_seq: '1' });
    await other.status({ last_alarm: 'nie' });
    await b.status({ last_alarm: 'A1', alarm_seq: '1' });       // kein neuer Alarm
    assert.equal((await relayAlarms()).length, 1);
    await other.status({ last_alarm: 'B1', alarm_seq: '1' });   // Alarm des zweiten Boards
    await b.status({ last_alarm: 'A2', alarm_seq: '2' });
    assert.deepEqual((await relayAlarms()).map((e) => e.info), ['A2', 'B1', 'A1']);
  });
});

describe('Veralteter Push', () => {
  it('ein Push, der NACH einem jüngeren ankommt, wird bestätigt, aber nicht verarbeitet', async () => {
    await b.status({ uptime_s: '5000', last_alarm: 'nie' });
    // Der jüngere Push ist schon gespeichert (Ankunft 30 s "in der Zukunft" des
    // jetzt eintreffenden, mit längerer Laufzeit).
    backend.data.edit('latest_status.json', (d) => { d.seen_at = now() + 30; });
    const late = await b.status({ uptime_s: '4000', rssi: '-99', last_alarm: 'ALT', alarm_seq: '9', reset_reason: 'panic' });
    assert.equal(late.text, 'OK\n');
    const stored = backend.data.read('latest_status.json');
    assert.equal(stored.uptime_s, '5000');
    assert.equal(stored.rssi, '-61');
    assert.deepEqual(await relayAlarms(), []);
    assert.deepEqual(await restarts(), []);
  });
});

describe('Neustart der Box', () => {
  it('nach einem Absturz (kleinere Laufzeit, reset_reason panic): Eintrag im Verlauf', async () => {
    await b.status({ uptime_s: '5000' });
    ageStatus(100);
    await b.status({ uptime_s: '20', reset_reason: 'panic' });
    const found = await restarts();
    assert.equal(found.length, 1);
    assert.equal(found[0].reason, 'panic');
  });

  it('Watchdog und Spannungseinbruch zählen ebenfalls, ein normales Einschalten nicht', async () => {
    await b.status({ uptime_s: '5000' });
    for (const reason of ['task_wdt', 'int_wdt', 'wdt', 'brownout', 'poweron', 'software', 'external']) {
      ageStatus(100);
      await b.status({ uptime_s: '5000' });
      ageStatus(100);
      await b.status({ uptime_s: '20', reset_reason: reason });
    }
    assert.deepEqual((await restarts()).map((e) => e.reason).reverse(), ['task_wdt', 'int_wdt', 'wdt', 'brownout']);
  });

  it('Absturz-Schleife, bei der die Laufzeit nicht sinkt: erkannt am Startzeitpunkt', async () => {
    await b.status({ uptime_s: '12' });
    ageStatus(100);
    await b.status({ uptime_s: '13', reset_reason: 'task_wdt' });
    assert.equal((await restarts()).length, 1);
  });

  it('eine seit Wochen laufende Box ist nach einer Uhr-Korrektur des Servers NICHT neu gestartet', async () => {
    await b.status({ uptime_s: '5000' });
    ageStatus(100);
    // Laufzeit 50 s weiter, der Server sieht aber 100 s Abstand: Der errechnete
    // Startzeitpunkt springt um 50 s - gestartet sein kann sie trotzdem nicht.
    await b.status({ uptime_s: '5050', reset_reason: 'panic' });
    assert.deepEqual(await restarts(), []);
  });

  it('ein anderes Board ist kein Neustart der Box', async () => {
    await b.status({ uptime_s: '5000' });
    ageStatus(100);
    await box(backend, { device_id: 'bastel-1' }).status({ uptime_s: '20', reset_reason: 'panic' });
    assert.deepEqual(await restarts(), []);
  });

  it('die Meldung an den Admin gibt es nur, wenn sie eingeschaltet ist', async () => {
    await b.status({ uptime_s: '5000' });
    ageStatus(100);
    let mark = backend.bark.mark();
    await b.status({ uptime_s: '20', reset_reason: 'panic' });
    await settle();
    assert.deepEqual(backend.bark.since(mark), [], 'ab Werk aus');

    assert.equal((await admin.post({ action: 'notify_set', type: 'box_restart', key: 'enabled', value: '1' })).ok, true);
    await b.status({ uptime_s: '5000' });
    ageStatus(100);
    mark = backend.bark.mark();
    const pushed = await b.status({ uptime_s: '20', reset_reason: 'panic' });
    assert.equal(pushed.text, 'OK\n');
    const [sent] = await waitFor(() => backend.bark.to(STATUS_KEY, mark), 'Meldung "neu gestartet"');
    assert.equal(sent.fields.title, 'Alarm-Box neu gestartet');
    assert.match(sent.fields.body, /einem ABSTURZ \(Panic\)/);
    assert.equal(sent.fields.level, 'passive');

    // Ein normaler Neustart wird nur mit der Auswahl "jedem Neustart" gemeldet.
    await b.status({ uptime_s: '5000' });
    ageStatus(100);
    mark = backend.bark.mark();
    await b.status({ uptime_s: '20', reset_reason: 'poweron' });
    await settle();
    assert.deepEqual(backend.bark.since(mark), []);
    assert.equal((await admin.post({ action: 'notify_set', type: 'box_restart', key: 'scope', value: 'all' })).ok, true);
    await b.status({ uptime_s: '5000' });
    ageStatus(100);
    mark = backend.bark.mark();
    await b.status({ uptime_s: '20', reset_reason: 'poweron' });
    const [normal] = await waitFor(() => backend.bark.to(STATUS_KEY, mark), 'Meldung nach dem Einschalten');
    assert.match(normal.fields.body, /dem Einschalten/);
  });
});

describe('Welche Box zählt (box_device_id)', () => {
  it('ein fremdes Board wird angenommen, ändert aber weder Status noch Verlauf', async () => {
    await backend.configure({ box_device_id: 'fw-box-1' });
    const foreign = box(backend, { device_id: 'bastel-1' });
    await b.status({ rssi: '-61' });
    await foreign.status({ rssi: '-99' });
    const second = await foreign.status({ rssi: '-99', last_alarm: 'FREMD', alarm_seq: '1' });
    assert.equal(second.status, 200);
    assert.equal(second.text, 'OK\n');

    const stored = backend.data.read('latest_status.json');
    assert.equal(stored.device_id, 'fw-box-1');
    assert.equal(stored.rssi, '-61');
    assert.deepEqual(await relayAlarms(), []);
    const hint = (await admin.get('status')).device_hint;
    assert.match(hint, /fremdes Board/);
    assert.match(hint, /bastel-1/);
  });

  it('verstummt die echte Box, bleibt sie trotz pushendem Bastel-Board offline', async () => {
    await backend.configure({ box_device_id: 'fw-box-1' });
    await b.status();
    ageStatus(600);
    await box(backend, { device_id: 'bastel-1' }).status();
    assert.equal((await admin.get('status')).offline, true);
  });

  it('ohne box_device_id: Hinweis, sobald sich mehrere Boards mit dem Token melden', async () => {
    assert.equal((await admin.get('status')).device_hint, '');
    await b.status();
    assert.equal((await admin.get('status')).device_hint, '');
    await box(backend, { device_id: 'bastel-1' }).status();
    assert.match((await admin.get('status')).device_hint, /Mehrere Boards melden sich/);
  });

  it('schweigt eins von mehreren Boards, warnt das Dashboard vor einem verdeckten Ausfall', async () => {
    await b.status();
    await box(backend, { device_id: 'bastel-1' }).status();
    backend.data.edit('devices_seen.json', (d) => { d['fw-box-1'].seen_at = now() - 900; });
    const hint = (await admin.get('status')).device_hint;
    assert.match(hint, /^ACHTUNG/);
    assert.match(hint, /fw-box-1/);
    assert.match(hint, /AUSGEFALLEN/);
  });
});
