// Einstellungen im Dashboard (dashboard_api.php: settings_list, settings_set):
// bisher nur die Dauer bis "automatisch wieder laut" (mute_max_seconds).
// Angeboten werden feste Stufen plus der Wert aus der Konfiguration - der
// unverändert, als "(Standard)" markiert. Ein anderer Wert wird abgelehnt.
// Nur für den Admin; eine Änderung steht im Verlauf, sichtbar nur für ihn.
import assert from 'node:assert/strict';
import { after, before, beforeEach, describe, it } from 'node:test';
import { READER, session, startBackend } from './harness.mjs';

let backend;
let admin;
before(async () => { backend = await startBackend(); });
after(async () => { await backend.stop(); });
beforeEach(async () => {
  await backend.reset();
  admin = await session(backend);
});

const STEPS = [7200, 14400, 21600, 28800, 43200, 57600, 86400];
const setMuteMax = (value) => admin.post({ action: 'settings_set', key: 'mute_max_seconds', value });
const muteMax = async () => (await admin.get('settings_list')).settings.mute_max_seconds;
const changes = (entries) => entries.filter((e) => e.event === 'setting_changed');

describe('Automatisch wieder laut nach (mute_max_seconds)', () => {
  it('Standard 43200; Stufe 14400 wird gespeichert, 999 und ein unbekannter Schlüssel abgelehnt', async () => {
    let r = await admin.get('settings_list');
    assert.equal(r.settings.mute_max_seconds.value, 43200);
    assert.ok(r.settings.mute_max_seconds.choices.some((c) => c.value === 7200), 'Stufe 7200 fehlt');

    r = await setMuteMax('14400');
    assert.equal(r.ok, true, JSON.stringify(r));
    assert.equal((await admin.get('keys_list')).mute_max_seconds, 14400, 'keys_list zeigt 14400');
    r = await setMuteMax('999');
    assert.equal(r.ok, false, 'Wert 999 muss abgelehnt werden');
    assert.equal((await admin.get('keys_list')).mute_max_seconds, 14400, 'Wert bleibt 14400');
    r = await admin.post({ action: 'settings_set', key: 'foo', value: '7200' });
    assert.equal(r.ok, false, 'unbekannter Schlüssel abgelehnt');

    // Zurück auf den Standard.
    r = await setMuteMax('43200');
    assert.equal(r.ok, true, 'Zurücksetzen');
    assert.equal((await muteMax()).value, 43200);
  });

  it('angeboten werden die Stufen; genau diese nimmt settings_set an, jeden anderen Wert nicht', async () => {
    const offered = (await muteMax()).choices.map((c) => c.value);
    assert.deepEqual(offered, STEPS);
    for (const value of offered) {
      const r = await setMuteMax(String(value));
      assert.equal(r.ok, true, `${value}: ${JSON.stringify(r)}`);
      assert.equal((await muteMax()).value, value);
    }
    await setMuteMax('14400');
    for (const value of ['999', '0', '3600', '43201', '-7200', '7200.0', 'abc', '']) {
      const r = await setMuteMax(value);
      assert.equal(r.ok, false, `Wert "${value}"`);
      assert.equal(r.message, 'Ungültiger Wert – nichts geändert.', `Wert "${value}"`);
    }
    assert.equal((await muteMax()).value, 14400, 'nichts geändert');
  });

  it('der Wert aus der Konfiguration steht unverändert in der Auswahl, markiert als (Standard)', async () => {
    let r = await muteMax();
    assert.equal(r.default, 43200);
    assert.equal(r.choices.find((c) => c.value === 43200).text, '12 Std (Standard)');
    assert.equal(r.choices.filter((c) => c.text.includes('(Standard)')).length, 1);

    await backend.configure({ mute_max_seconds: 5400 });
    r = await muteMax();
    assert.equal(r.default, 5400);
    assert.equal(r.value, 5400);
    assert.deepEqual(r.choices.map((c) => c.value), [5400, ...STEPS]);
    assert.equal(r.choices.find((c) => c.value === 5400).text, '90 Min (Standard)');
    assert.equal(r.choices.find((c) => c.value === 43200).text, '12 Std');
    assert.equal((await admin.get('keys_list')).mute_max_seconds, 5400);

    const set = await setMuteMax('5400');
    assert.equal(set.ok, true);
    assert.equal((await muteMax()).value, 5400);
  });

  it('eine Änderung steht als setting_changed im Verlauf - nur für den Admin; keine Änderung, kein Eintrag', async () => {
    assert.equal((await setMuteMax('14400')).ok, true);
    const log = await admin.get('log_view', { limit: 5, filter: 'system' });
    assert.equal(log.entries[0].event, 'setting_changed', `Verlauf: ${log.entries[0]?.event}`);
    assert.deepEqual({ ...log.entries[0], at: 0 }, {
      at: 0, event: 'setting_changed', by: 'admin', label: 'Automatisch wieder laut nach', value: '4 Std',
    });

    const same = await setMuteMax('14400');
    assert.equal(same.ok, true);
    assert.equal(same.changed, false);
    assert.equal(changes(await admin.log('all')).length, 1);

    const reader = await session(backend, READER);
    for (const filter of ['all', 'system']) {
      assert.deepEqual(changes(await reader.log(filter)), [], filter);
    }
  });

  it('die Lese-Rolle darf die Einstellung weder lesen noch ändern', async () => {
    const reader = await session(backend, READER);
    const set = await reader.post({ action: 'settings_set', key: 'mute_max_seconds', value: '7200' });
    assert.equal(set.http, 403, 'Lese-Rolle 403');
    assert.equal(set.code, 'readonly');
    const read = await reader.get('settings_list');
    assert.equal(read.http, 403, 'Lese-Rolle settings_list 403');
    assert.equal(read.code, 'readonly');
    assert.equal((await muteMax()).value, 43200);
  });
});
