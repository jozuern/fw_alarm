// Nur PHP: Regeln, die allein an PHPs Ablage im Datenordner hängen - Sperren
// als Dateien, kaputte oder verschwundene Laufzeitdateien, die *.bak-
// Sicherung, der Selbsttest über api/health.php (health_check_url), der
// Wächter als eigener Prozess - und alles, was das Design der Go-Fassung
// (docs/superpowers/specs/2026-11-17-go-server-neubau-design.md, §7) bewusst
// anders regelt. Die Fälle stehen hier UNVERÄNDERT so, wie sie in ihrer
// Herkunftsdatei standen (Gruppen = Datei und Abschnitt dort); das
// Gegenstück für Go nennt die Tabelle "Rule → test" im Plan. Diese Datei
// verschwindet mit PHP.
import assert from 'node:assert/strict';
import { after, before, beforeEach, describe, it } from 'node:test';
import {
  BACKEND_KIND, STATUS_KEY, WATCHER_TOKEN, box, now, postForm, session, sha256, startBackend,
} from './harness.mjs';

let backend;
let admin;
let b;

describe('Nur PHP: Ablage im Datenordner', { skip: BACKEND_KIND !== 'php' && 'nur PHP' }, () => {
  before(async () => { backend = await startBackend(); });
  after(async () => { await backend.stop(); });
  beforeEach(async () => {
    await backend.reset();
    admin = await session(backend);
    b = box(backend);
  });

  // ==========================================================================
  describe('box-status.test.mjs', () => {
    describe('Status-Push', () => {
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
  });

  // ==========================================================================
  describe('box-poll-ack.test.mjs', () => {
    const events = async (filter) => (await admin.log(filter)).map((e) => e.event);

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

    describe('Der Poll räumt auf', () => {
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
    });
  });

  // ==========================================================================
  describe('box-keys.test.mjs', () => {
    const version = async () => (await admin.keys()).version;
    const post = async (fields) => {
      const r = await admin.post(fields);
      assert.equal(r.ok, true, JSON.stringify(r));
      return r;
    };
    // Zeilen key0/muted0/mutevol0 ... als Liste von [key, muted, mutevol].
    const rows = (kv) => Array.from({ length: Number(kv.count) }, (_, i) => [kv[`key${i}`], kv[`muted${i}`], kv[`mutevol${i}`]]);

    describe('Demo-Modus', () => {
      it('ein unplausibler Demo-Key alarmiert lieber alle als niemanden', async () => {
        const anna = await admin.addKey('Anna', 'annakey00001');
        await admin.addKey('Bert', 'bertkey00001');
        await post({ action: 'demo_set', enabled: '1', id: anna.id });
        backend.data.edit('demo_mode.json', (d) => { d.key = 'x y'; });
        assert.equal((await b.keys()).kv.count, '2');
        assert.equal((await admin.get('status')).demo.enabled, false, 'dann zeigt auch das Dashboard nicht "Demo"');
      });
    });

    describe('Bereinigung wie in der Firmware', () => {
      const entry = (id, key, extra = {}) => ({ id, label: `E${id}`, key, added_at: 1, mute_token: `tok${String(id).padStart(9, '0')}`, ...extra });

      it('nur gültige Keys, keine doppelten, höchstens 10 - in der Reihenfolge der Liste', async () => {
        const keys = [
          entry(1, 'validkey00001'),
          entry(2, 'x'),                    // zu kurz
          entry(3, 'has space01'),          // ungültiges Zeichen
          entry(4, 'validkey00001'),        // doppelt
          'kein Objekt',
          entry(5, ' validkey00005 '),      // Leerzeichen außen herum zählen nicht
        ];
        for (let i = 6; i <= 16; i++) keys.push(entry(i, `validkey${String(i).padStart(5, '0')}`));
        backend.data.write('alarm_keys.json', { next_id: 17, version: 7, keys });
        const r = await b.keys();
        assert.equal(r.status, 200);
        assert.equal(r.kv.version, '7');
        assert.equal(r.kv.count, '10');
        assert.deepEqual(rows(r.kv).map((x) => x[0]), [
          'validkey00001', 'validkey00005', 'validkey00006', 'validkey00007', 'validkey00008',
          'validkey00009', 'validkey00010', 'validkey00011', 'validkey00012', 'validkey00013',
        ]);
        assert.equal((await b.poll()).status, 200, 'auch der Poll verkraftet den kaputten Eintrag');
      });
    });

    describe('Kaputte Liste: Rückgriff auf die Sicherung', () => {
      // Nach zwei Änderungen hält die Sicherung den Stand der ersten.
      async function twoSaves() {
        const anna = await admin.addKey('Anna', 'annakey00001');
        const bert = await admin.addKey('Bert', 'bertkey00001');
        return { anna, bert, v: await version() };
      }

      it('unlesbare Liste: die Box bekommt den Stand der Sicherung', async () => {
        const { v } = await twoSaves();
        backend.data.corrupt('alarm_keys.json');
        const r = await b.keys();
        assert.equal(r.status, 200);
        assert.equal(r.kv.version, String(v - 1));
        assert.deepEqual(rows(r.kv).map((x) => x[0]), ['annakey00001']);
      });

      it('falsche Struktur ("keys": false) zählt wie unlesbar', async () => {
        const { v } = await twoSaves();
        backend.data.write('alarm_keys.json', { next_id: 9, version: 99, keys: false });
        const r = await b.keys();
        assert.equal(r.kv.version, String(v - 1));
        assert.equal(r.kv.count, '1');
      });

      it('verschwundene Liste: ebenfalls die Sicherung', async () => {
        const { v } = await twoSaves();
        backend.data.remove('alarm_keys.json');
        assert.equal((await b.keys()).kv.version, String(v - 1));
      });

      it('Liste UND Sicherung unlesbar: leere Liste mit Version 0 (die Box verwirft sie und behält ihre), das Dashboard sagt es', async () => {
        await twoSaves();
        backend.data.corrupt('alarm_keys.json');
        backend.data.corrupt('alarm_keys.json.bak');
        const r = await b.keys();
        assert.equal(r.status, 200);
        assert.equal(r.text, 'ok=1\nnonce=k1\nversion=0\ncount=0\n');
        assert.equal((await b.poll()).kv.keys_version, '0');
        const list = await admin.get('keys_list');
        assert.equal(list.ok, false);
        assert.equal(list.code, 'keys_corrupt');
        // Und niemand fängt mit einer leeren Liste neu an.
        const add = await admin.post({ action: 'keys_add', label: 'Neu', key: 'neukey000001' });
        assert.equal(add.ok, false);
      });
    });

    describe('Versionen und ids laufen nie rückwärts', () => {
      it('nach dem Rückgriff auf die Sicherung bekommt die nächste Änderung eine NEUE Version und id', async () => {
        await admin.addKey('Anna', 'annakey00001');            // Version 1, id 1
        const bert = await admin.addKey('Bert', 'bertkey00001');   // Version 2, id 2
        const v = await version();
        backend.data.corrupt('alarm_keys.json');               // Sicherung: Version 1, nur Anna
        const cara = await admin.addKey('Cara', 'carakey00001');
        assert.equal(await version(), v + 1, 'nicht noch einmal die Version, die die Box mit anderem Inhalt kennt');
        assert.ok(cara.id > bert.id, 'nicht die id des zuletzt Angelegten');
        assert.deepEqual(rows((await b.keys()).kv).map((x) => x[0]), ['annakey00001', 'carakey00001']);
      });

      it('nach Verlust des Datenordners zählt die Version weiter, die die Box zuletzt gemeldet hat', async () => {
        await b.status({ keys_version: '41' });
        await admin.addKey('Anna', 'annakey00001');
        assert.equal(await version(), 42);
      });
    });
  });

  // ==========================================================================
  describe('alive.test.mjs', () => {
    // Konfiguration mit eingerichtetem Außen-Wächter.
    const WATCHER = { watcher_token_sha256: sha256(WATCHER_TOKEN) };
    // Die Zeilen der Antwort, in dieser Reihenfolge.
    const LINES = ['ok', 'nonce', 'stored', 'box_age_s', 'cron_age_s', 'cron_configured', 'nas_send_failed'];
    const url = () => `${backend.base}/api/alive.php`;
    // Ein Aufruf des Außen-Wächters (Felder mit undefined werden weggelassen).
    const knock = (fields = {}) => postForm(url(), { token: WATCHER_TOKEN, nonce: 'n1', ...fields });
    const seen = () => backend.data.read('watcher_seen.json');
    const near = (actual, expected, slack = 3) => Math.abs(Number(actual) - expected) <= slack;

    describe('Antwort auf einen gültigen Aufruf', () => {
      beforeEach(async () => { await backend.configure(WATCHER); });

      it('sieben Zeilen in fester Reihenfolge, als Klartext und nicht zwischenspeicherbar', async () => {
        const r = await knock({ nonce: 'abc123' });
        assert.equal(r.status, 200);
        assert.match(r.headers.get('content-type'), /^text\/plain/);
        assert.match(r.headers.get('cache-control'), /no-store/);
        assert.ok(r.text.endsWith('\n'));
        const lines = r.text.slice(0, -1).split('\n');
        assert.deepEqual(lines.map((line) => line.slice(0, line.indexOf('='))), LINES);
        // Frisch aufgesetzt: noch kein Status der Box, noch kein Lauf des Wächters.
        assert.equal(r.text,
          'ok=1\nnonce=abc123\nstored=1\nbox_age_s=-1\ncron_age_s=-1\ncron_configured=1\nnas_send_failed=0\n');
      });

      it('cron_age_s: -1 vor dem ersten Lauf des Wächters, danach sein Alter in Sekunden', async () => {
        assert.equal((await knock()).kv.cron_age_s, '-1');
        await backend.runWatchdog();
        const fresh = await knock();
        assert.equal(fresh.kv.cron_configured, '1');
        assert.ok(Number(fresh.kv.cron_age_s) >= 0 && Number(fresh.kv.cron_age_s) <= 30, `frisch: ${fresh.kv.cron_age_s}`);
        backend.data.edit('watchdog_state.json', (d) => { d.last_run_at = now() - 1000; });
        const aged = await knock();
        assert.ok(near(aged.kv.cron_age_s, 1000), `nach 1000 s: ${aged.kv.cron_age_s}`);
      });

      it('cron_configured=0, wenn kein bark_key_status eingetragen ist (der Wächter meldet dann nichts)', async () => {
        assert.equal((await knock()).kv.cron_configured, '1');
        await backend.configure({ ...WATCHER, bark_key_status: '' });
        assert.equal((await knock()).kv.cron_configured, '0');
        await backend.configure({ ...WATCHER, bark_key_status: '   ' });
        assert.equal((await knock()).kv.cron_configured, '0');
        // Ein Lauf ohne Status-Key hinterlässt keinen Zeitpunkt.
        await backend.configure({ ...WATCHER, bark_key_status: '' });
        await backend.runWatchdog();
        const r = await knock();
        assert.equal(r.kv.cron_configured, '0');
        assert.equal(r.kv.cron_age_s, '-1');
      });
    });

    describe('Gegenüberwachung: Jeder gültige Aufruf wird gemerkt', () => {
      beforeEach(async () => { await backend.configure(WATCHER); });

      it('lässt sich der Aufruf nicht speichern: trotzdem 200 und ok=1, aber stored=0', async () => {
        backend.data.block('watcher_seen.json');
        try {
          const r = await knock({ nonce: 'abc' });
          assert.equal(r.status, 200);
          assert.equal(r.kv.ok, '1');
          assert.equal(r.kv.nonce, 'abc');
          assert.equal(r.kv.stored, '0');
          assert.deepEqual(r.text.slice(0, -1).split('\n').map((line) => line.slice(0, line.indexOf('='))), LINES);
        } finally {
          backend.data.unblock('watcher_seen.json');
        }
        // Danach wird wieder gemerkt.
        assert.equal((await knock()).kv.stored, '1');
        assert.ok(near(seen().seen_at, now()));
      });
    });
  });

  // ==========================================================================
  describe('watchdog.test.mjs', () => {
    // Ein Lauf des Wächters. Seine Meldungen sind verschickt, bevor er endet.
    const run = () => backend.runWatchdog();
    // Die Ausgabe ist eine Liste "TEIL | TEIL | ..."; ein Teil ist ein Kennwort,
    // manchmal mit Zusatz ("WEB_FAIL (1) ...", "STILL_OFFLINE (bereits gemeldet)").
    const parts = (out) => out.split(' | ');
    const says = (out, word) => parts(out).some((p) => p === word || p.startsWith(`${word} `));
    // Den gespeicherten Stand "s Sekunden alt" machen (als wäre so lange nichts gekommen).
    const ageStatus = (seconds) => backend.data.edit('latest_status.json', (d) => { d.seen_at = now() - seconds; });
    // Die Box hat sich gemeldet und schweigt seitdem (Schwelle des Wächters ab Werk: 600 s).
    const silentBox = async (seconds = 700, headers = {}) => {
      await b.status({}, headers);
      ageStatus(seconds);
    };
    // Im Gedächtnis des Wächters eine Zeit zurückdrehen (statt wirklich zu warten).
    const editState = (fn) => backend.data.edit('watchdog_state.json', fn);
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
    const demoOn = (id) => post({ action: 'demo_set', enabled: '1', id });
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
    // Der Selbsttest des Datenordners scheitert: Die Probe-Datei lässt sich nicht schreiben.
    const breakDataDir = () => backend.data.block('health_probe.json');
    const repairDataDir = () => backend.data.unblock('health_probe.json');
    const withWatcher = (more = {}) => backend.configure({ watcher_token_sha256: sha256(WATCHER_TOKEN), ...more });
    const watcherSeen = (secondsAgo) => backend.data.write('watcher_seen.json',
      { seen_at: now() - secondsAgo, bark_failed_at: 0 });

    describe('Box offline und wieder da', () => {
      it('nimmt Bark die Meldung nicht an: SEND_FAILED, nichts gilt als gemeldet – der nächste Lauf versucht es erneut', async () => {
        await silentBox();
        await backend.configure({ bark_key_status: 'failstatus001' });   // Attrappe antwortet 500
        let out = await run();
        assert.ok(says(out, 'SEND_FAILED'), out);
        assert.ok(backend.bark.to('failstatus001').length >= 1);
        assert.deepEqual(await systemLog('box_offline'), []);
        let wd = await watchdogStatus();
        assert.ok(wd.last_send_failed_at > wd.last_send_ok_at, 'das Dashboard erfährt: Der Wächter kann nicht melden');
        assert.ok(Math.abs(wd.last_send_failed_at - now()) <= 5);

        await backend.configure({});
        const mark = backend.bark.mark();
        out = await run();
        assert.ok(parts(out).includes('NOTIFIED_OFFLINE'), out);
        assert.equal(toAdmin(mark).length, 1);
        assert.equal((await systemLog('box_offline')).length, 1);
        wd = await watchdogStatus();
        assert.ok(wd.last_send_ok_at >= wd.last_send_failed_at);
      });

      it('über die Brücke, der Außen-Wächter meldet sich aber noch: es bleibt bei der Warnung vor dem Ausfall der Box', async () => {
        await withWatcher();
        await silentBox(700, { 'X-FW-Bridge': '1' });
        watcherSeen(500);
        const mark = backend.bark.mark();
        await run();
        const body = text(toAdmin(mark)[0]);
        assert.match(body, /Der Relaisalarm wuerde gerade NICHT ausgeloest/);
        assert.match(body, /Die Box meldet sich noch ueber das NAS zu Hause/);
        assert.doesNotMatch(body, /vermutlich liegt es aber am NAS/);
      });
    });

    describe('Selbsttest des Datenordners', () => {
      it('ab dem zweiten Fehlschlag in Folge "Dashboard DEFEKT", danach einmal "Dashboard wieder OK"', async () => {
        breakDataDir();
        const mark = backend.bark.mark();
        let out = await run();
        assert.match(out, /^WEB_FAIL \(1\) Datenordner nicht beschreibbar/);
        assert.deepEqual(backend.bark.since(mark), []);

        out = await run();
        assert.ok(says(out, 'WEB_FAIL_NOTIFIED'), out);
        let sent = backend.bark.since(mark);
        assert.equal(sent.length, 1);
        assert.equal(sent[0].key, STATUS_KEY);
        assert.equal(sent[0].fields.title, 'Dashboard DEFEKT');
        assert.equal(sent[0].fields.level, 'passive');
        const body = text(sent[0]);
        assert.match(body, /^Datenordner nicht beschreibbar/);
        assert.match(body, /Login und Status-Pushs der Box scheitern!/);
        assert.match(body, /Bark-Alarme der Box laufen davon unabhaengig weiter\./);
        assert.match(body, /sudo chown -R \S+ \S+$/);
        const failed = await systemLog('nas_web_failed');
        assert.equal(failed.length, 1);
        assert.match(failed[0].detail, /^Datenordner nicht beschreibbar/);

        // Weiter gestört: gezählt, aber nicht bei jedem Lauf erneut gemeldet.
        out = await run();
        assert.match(out, /^WEB_FAIL \(3\) /);
        assert.equal(backend.bark.since(mark).length, 1);

        repairDataDir();
        out = await run();
        assert.ok(says(out, 'WEB_RECOVERED'), out);
        sent = backend.bark.since(mark);
        assert.equal(sent.length, 2);
        assert.equal(sent[1].fields.title, 'Dashboard wieder OK');
        assert.match(text(sent[1]), /^Der Datenordner ist wieder beschreibbar \(Stoerung ca\. \d+ min\)\.$/);
        assert.equal((await systemLog('nas_web_recovered')).length, 1);
        assert.equal((await systemLog('nas_web_failed')).length, 1);

        out = await run();
        assert.ok(says(out, 'WEB_OK'), out);
        assert.equal(backend.bark.since(mark).length, 2);
      });

      it('scheitert der Selbsttest und die Box schweigt: "Box OFFLINE" nennt den Server als Ursache – auch in der Erinnerung', async () => {
        await silentBox();
        breakDataDir();
        const mark = backend.bark.mark();
        const out = await run();
        assert.ok(parts(out).includes('NOTIFIED_OFFLINE'), out);
        const [first] = toAdmin(mark);
        assert.equal(first.fields.title, 'Alarm-Box OFFLINE');
        const body = text(first);
        assert.match(body, /^Kein Status vom ESP32 seit 12 min - Ursache ist aber vermutlich der Server, nicht die Box: /);
        assert.match(body, /Datenordner nicht beschreibbar/);
        assert.doesNotMatch(body, /Strom\/WLAN\/Box pruefen/);
        assert.doesNotMatch(body, /NICHT ausgeloest/);

        await notifySet('box_offline', 'repeat_hours', 1);
        editState((d) => { d.offline_reminded_at = now() - 3700; });
        await run();
        const reminder = toAdmin(mark).find((m) => m.fields.title === 'Alarm-Box weiterhin OFFLINE');
        assert.ok(reminder, 'Erinnerung verschickt');
        assert.match(text(reminder), /Ursache ist aber vermutlich der Server/);
      });

      it('solange der Selbsttest so scheitert, wird der Außen-Wächter nicht beurteilt – seine Stille zählt erst ab dem Ende der Störung', async () => {
        await withWatcher();
        watcherSeen(4000);   // länger her als die Frist von 1 Std
        breakDataDir();
        const mark = backend.bark.mark();
        let out = await run();
        assert.ok(says(out, 'WATCHER_UNKNOWN'), out);
        assert.deepEqual(backend.bark.since(mark), []);

        repairDataDir();
        out = await run();
        assert.ok(says(out, 'WATCHER_OK'), out);
        assert.deepEqual(backend.bark.since(mark), [], 'kein voreiliges "meldet sich nicht"');
        assert.deepEqual(await systemLog('watcher_silent'), []);

        // Die Frist läuft ab dem Ende der Störung.
        editState((d) => { d.watcher_unknown_at = now() - 3700; });
        out = await run();
        assert.ok(says(out, 'WATCHER_SILENT_NOTIFIED'), out);
        assert.equal(toAdmin(mark)[0].fields.title, 'Aussen-Waechter meldet sich nicht');
      });
    });

    describe('Selbsttest über health_check_url (heutige Bauweise)', () => {
      it('falsche Adresse (HTTP 404): "Selbsttest falsch eingerichtet" – der Server gilt deshalb nicht als Ursache', async () => {
        await withWatcher({ health_check_url: `${backend.base}/api/gibtsnicht.php` });
        await silentBox();
        const mark = backend.bark.mark();
        let out = await run();
        assert.match(out, /^WEB_FAIL \(1\) Selbsttest falsch eingerichtet \(HTTP 404/);
        assert.ok(parts(out).includes('NOTIFIED_OFFLINE'), out);
        assert.ok(says(out, 'WATCHER_OK'), out);   // der Außen-Wächter wird weiter beurteilt
        const offline = text(toAdmin(mark)[0]);
        assert.match(offline, /Strom\/WLAN\/Box pruefen!/);
        assert.match(offline, /Der Relaisalarm wuerde gerade NICHT ausgeloest/);
        assert.doesNotMatch(offline, /vermutlich der Server/);

        out = await run();
        assert.ok(says(out, 'WEB_FAIL_NOTIFIED'), out);
        const defekt = toAdmin(mark).find((m) => m.fields.title === 'Dashboard DEFEKT');
        assert.ok(defekt, 'Meldung "Dashboard DEFEKT" verschickt');
        const body = text(defekt);
        assert.match(body, /^Selbsttest falsch eingerichtet \(HTTP 404/);
        assert.match(body, /Status-Pushs der Box sind davon vermutlich nicht betroffen/);
        assert.doesNotMatch(body, /scheitern!/);
        assert.doesNotMatch(body, /chown/);
      });

      it('Webserver nicht erreichbar: die Meldung sagt es und rät, die Dienste zu prüfen', async () => {
        await backend.configure({ health_check_url: 'http://127.0.0.1:1/api/health.php' });
        const mark = backend.bark.mark();
        let out = await run();
        assert.match(out, /^WEB_FAIL \(1\) Webserver nicht erreichbar/);
        out = await run();
        assert.ok(says(out, 'WEB_FAIL_NOTIFIED'), out);
        const [sent] = toAdmin(mark);
        assert.equal(sent.fields.title, 'Dashboard DEFEKT');
        const body = text(sent);
        assert.match(body, /^Webserver nicht erreichbar/);
        assert.match(body, /Login und Status-Pushs der Box scheitern!/);
        assert.match(body, /Auf dem Server pruefen: systemctl status/);
        assert.doesNotMatch(body, /chown/);
      });
    });

    describe('Erinnerungen', () => {
      it('beschädigter Demo-Zustand: der Lauf schreibt LIVE fest und erhöht die Listen-Version', async () => {
        const phone = await admin.addKey('Test-Handy', 'demokey00001');
        await demoOn(phone.id);
        const before = await admin.get('status');
        assert.equal(before.demo.enabled, true);

        backend.data.corrupt('demo_mode.json');
        const mark = backend.bark.mark();
        const out = await run();
        assert.equal(backend.data.read('demo_mode.json').enabled, false);
        assert.doesNotMatch(out, /DEMO_/);
        const after = await admin.get('status');
        assert.equal(after.demo.enabled, false);
        assert.ok(after.nas_keys_version > before.nas_keys_version, 'die Box holt sich die volle Liste');
        assert.equal((await systemLog('demo_repaired')).length, 1);
        assert.deepEqual(backend.bark.since(mark), []);
      });
    });

    describe('Wie gemeldet wird (Einstellungen "Meldungen an mich")', () => {
      it('ein eingetragener, aber unbrauchbarer Status-Key ist ein Fehlschlag (SEND_FAILED), kein "abgeschaltet"', async () => {
        await backend.configure({ bark_key_status: 'https://x' });
        await silentBox();
        const mark = backend.bark.mark();
        const out = await run();
        assert.ok(says(out, 'SEND_FAILED'), out);
        assert.ok(!says(out, 'OHNE_PUSH'), out);
        assert.deepEqual(backend.bark.since(mark), []);
        assert.deepEqual(await systemLog('box_offline'), []);
        const wd = await watchdogStatus();
        assert.ok(wd.last_send_failed_at > wd.last_send_ok_at);

        // Mit einem brauchbaren Key holt der nächste Lauf die Meldung nach.
        await backend.configure({});
        assert.ok(parts(await run()).includes('NOTIFIED_OFFLINE'));
        assert.equal(toAdmin(mark).length, 1);
      });
    });
  });
});
