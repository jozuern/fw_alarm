// Ablage der Go-Fassung: was gilt, wenn nichts gesichert werden kann
// (Entwurf §6.2, §7) – die Gegenstücke zu den Fällen in php-storage.test.mjs,
// die dort nur unter PHP gelten. Nur Go.
import assert from 'node:assert/strict';
import { after, before, beforeEach, describe, it } from 'node:test';
import { BACKEND_KIND, box, session, startBackend } from './harness.mjs';

describe('Ablage (nur Go)', { skip: BACKEND_KIND !== 'go' && 'nur Go' }, () => {
  let backend;
  let admin;
  let b;
  before(async () => { backend = await startBackend(); });
  after(async () => { await backend?.stop(); });
  beforeEach(async () => {
    await backend.reset();
    admin = await session(backend);
    b = box(backend);
  });

  const relayAlarms = async () => (await admin.log('alarm')).filter((e) => e.event === 'relay_alarm');

  describe('box-status.test.mjs', () => {
    // Gegenstück zu "lässt sich die Sperre des Datenordners nicht öffnen: HTTP
    // 500 …" und "lässt sich der Status nicht speichern: HTTP 500
    // WRITE_FAILED …": Der Push gilt im Speicher und wird mit 200 bestätigt
    // (Entwurf §7) – sonst ginge die Box in ihren Fehler-Backoff.
    it('Status wird angenommen, wenn nichts gesichert werden kann', async () => {
      await b.status();
      await backend.storage.fail('volatile');
      await backend.storage.fail('durable');
      try {
        const r = await b.status({ rssi: '-70', last_alarm: '17.11.2026 10:05:00', alarm_seq: '1' });
        assert.equal(r.status, 200);
        assert.equal(r.text, 'OK\n');
        assert.match(r.headers.get('content-type'), /^text\/plain/);
        let st = await admin.get('status');
        assert.equal(st.status.rssi, '-70', 'das Dashboard zeigt den Push');
        assert.equal(st.offline, false);
        // Der Relaisalarm steht im Verlauf (aus dem Speicher), obwohl nichts
        // gesichert ist.
        assert.deepEqual((await relayAlarms()).map((e) => e.info), ['17.11.2026 10:05:00']);

        const old = await b.oldStatus({ rssi: '-66', last_alarm: '17.11.2026 10:09:00' });
        assert.equal(old.status, 200);
        assert.equal(old.text, 'OK\n');
        st = await admin.get('status');
        assert.equal(st.status.rssi, '-66', 'auch die alte Firmware');
        assert.equal('alarm_seq' in st.status, false);
        assert.deepEqual((await relayAlarms()).map((e) => e.info), ['17.11.2026 10:09:00', '17.11.2026 10:05:00']);
      } finally {
        await backend.storage.repair('durable');
        await backend.storage.repair('volatile');
      }
      assert.equal((await b.status({ rssi: '-61' })).text, 'OK\n', 'danach geht es wieder');
      assert.equal((await backend.state.boxStatus()).rssi, '-61');
      // Gesichert landet jeder Alarm genau einmal im Verlauf.
      const saved = (await backend.state.log()).filter((e) => e.event === 'relay_alarm').map((e) => e.info);
      assert.deepEqual(saved, ['17.11.2026 10:05:00', '17.11.2026 10:09:00']);
    });
  });

  describe('box-poll-ack.test.mjs', () => {
    // Gegenstück zu PHPs Poll bei klemmendem Datenordner (je nach Ursache
    // HTTP 500 "ok=0" oder type=NONE): immer 200 type=NONE mit der
    // Listen-Version - ein Befehl geht nur hinaus, wenn "ausgeliefert"
    // gesichert ist (Entwurf §6.2, §7), sonst wartet er weiter. Auch die
    // Quittung bleibt 200 OK.
    it('Poll liefert ohne Speichern keinen Befehl aus', async () => {
      await admin.addKey('Anna', 'annakey00001');
      const version = (await admin.keys()).version;
      assert.ok(version >= 1);
      assert.equal((await admin.post({ action: 'enqueue', type: 'TEST' })).ok, true);
      const none = (nonce) => `ok=1\nnonce=${nonce}\nid=0\ntype=NONE\nkeys_version=${version}\n`;
      await backend.storage.fail('durable');
      try {
        for (const nonce of ['s1', 's2']) {
          const r = await b.poll({ nonce });
          assert.equal(r.status, 200);
          assert.equal(r.text, none(nonce));
          assert.match(r.headers.get('content-type'), /^text\/plain/);
        }
        assert.equal((await admin.get('status')).command.state, 'pending', 'der Befehl wartet weiter');
        const ack = await b.ack({ id: 1, result: 'executed' });
        assert.equal(ack.status, 200);
        assert.equal(ack.text, 'OK\n');
      } finally {
        await backend.storage.repair('durable');
      }
      assert.equal((await b.poll({ nonce: 's3' })).text, `ok=1\nnonce=s3\nid=1\ntype=TEST\nkeys_version=${version}\n`,
        'danach liefert der nächste Poll ihn aus');
      assert.equal((await b.poll({ nonce: 's4' })).text, none('s4'), 'und nur einmal');
      assert.equal((await admin.get('status')).command.state, 'delivered');
      const delivered = (await backend.state.log()).filter((e) => e.event === 'delivered');
      assert.equal(delivered.length, 1);
    });
  });
});
