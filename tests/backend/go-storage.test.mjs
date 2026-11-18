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
});
