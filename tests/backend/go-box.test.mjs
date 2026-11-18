// Box-Protokoll der Go-Fassung – was nur sie so regelt (Entwurf §6.3, §7):
// Meldet die Box eine höhere Listen-Version als der Server (etwa nach dem
// Zurückspielen einer älteren Sicherung), hebt der Server seine sofort
// darüber – PHP zählte sie erst bei der nächsten Änderung mit
// (php-storage.test.mjs: "nach Verlust des Datenordners zählt die Version
// weiter …"). Nur Go.
import assert from 'node:assert/strict';
import { after, before, beforeEach, describe, it } from 'node:test';
import { BACKEND_KIND, box, session, settle, startBackend, waitFor } from './harness.mjs';

describe('Box-Protokoll (nur Go)', { skip: BACKEND_KIND !== 'go' && 'nur Go' }, () => {
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

  const version = () => backend.state.listVersion();
  const reaches = (v, what) => waitFor(async () => (await version()) === v, what);

  describe('Status-Push', () => {
    it('meldet die Box eine höhere Listen-Version, hebt der Server seine sofort darüber', async () => {
      assert.equal(await version(), 0);
      await b.status({ keys_version: '41' });
      await reaches(42, 'Listen-Version 42 nach dem Push mit 41');
      // Die Box holt die Liste unter der neuen Version.
      assert.equal((await b.keys()).kv.version, '42');
      // Dieselbe Meldung noch einmal ändert nichts mehr ...
      await b.status({ keys_version: '41' });
      await settle();
      assert.equal(await version(), 42);
      // ... und die nächste Änderung zählt darüber weiter (nie eine Nummer,
      // die die Box mit anderem Inhalt kennt).
      await admin.addKey('Anna', 'annakey00001');
      assert.equal(await version(), 43);
      // Die alte Firmware ebenso.
      await b.oldStatus({ keys_version: '50' });
      await reaches(51, 'Listen-Version 51 nach dem Push der alten Firmware');
    });

    it('nur die eingestellte Box hebt die Version, ein fremdes Board nicht', async () => {
      await backend.configure({ box_device_id: 'fw-box-1' });
      await box(backend, { device_id: 'bastel-1' }).status({ keys_version: '99' });
      await box(backend, { device_id: 'bastel-1' }).oldStatus({ keys_version: '99' });
      await settle();
      assert.equal(await version(), 0);
      await b.status({ keys_version: '7' });
      await reaches(8, 'Listen-Version 8 nach dem Push der eingestellten Box');
    });
  });
});
