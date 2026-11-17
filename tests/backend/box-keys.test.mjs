// Empfängerliste für die Box (api/keys.php): Klartext-Format, was die Box
// NICHT bekommt (Namen, Tokens, Telefon), Stumm-Zustand, Demo-Modus,
// Grenzen der Liste - und dass Versionen und ids nie rückwärts laufen. Die
// Box speichert, was hier kommt, und alarmiert danach genau diese Keys.
// (Von Hand verdorbene Listen, der Rückgriff auf die Sicherung und die
// Versionen danach gelten nur für PHP: php-storage.test.mjs.)
import assert from 'node:assert/strict';
import { after, before, beforeEach, describe, it } from 'node:test';
import { box, session, startBackend } from './harness.mjs';

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

const version = async () => (await admin.keys()).version;
const post = async (fields) => {
  const r = await admin.post(fields);
  assert.equal(r.ok, true, JSON.stringify(r));
  return r;
};
// Zeilen key0/muted0/mutevol0 ... als Liste von [key, muted, mutevol].
const rows = (kv) => Array.from({ length: Number(kv.count) }, (_, i) => [kv[`key${i}`], kv[`muted${i}`], kv[`mutevol${i}`]]);

describe('Format der Antwort', () => {
  it('ohne Empfänger: Version 0, count 0 - die Not-Liste aus der Konfiguration geht NICHT an die Box', async () => {
    const r = await b.keys({ nonce: 'k9' });
    assert.equal(r.status, 200);
    assert.equal(r.text, 'ok=1\nnonce=k9\nversion=0\ncount=0\n');
    assert.match(r.headers.get('content-type'), /^text\/plain/);
    assert.match(r.headers.get('cache-control'), /no-store/);
  });

  it('zeichengenau: ok, Nonce, version, count, dann je Empfänger key/muted/mutevol', async () => {
    await admin.addKey('Anna', 'annakey00001');
    await admin.addKey('Bert', 'bertkey00001');
    const v = await version();
    const r = await b.keys({ nonce: 'abc' });
    assert.equal(r.text, `ok=1\nnonce=abc\nversion=${v}\ncount=2\n`
      + 'key0=annakey00001\nmuted0=0\nmutevol0=0\n'
      + 'key1=bertkey00001\nmuted1=0\nmutevol1=0\n');
  });

  it('Name, id, Telefonnummer und Kurzbefehl-Token gehen nie an die Box', async () => {
    const anna = await admin.addKey('Annabella', 'annakey00001', { phone: '+49 171 5550123' });
    const text = (await b.keys()).text;
    assert.equal(text.includes('Annabella'), false);
    assert.equal(text.includes('5550123'), false);
    assert.ok(anna.mute_token.length >= 8);
    assert.equal(text.includes(anna.mute_token), false);
    assert.deepEqual(Object.keys((await b.keys()).kv).sort(),
      ['count', 'key0', 'muted0', 'mutevol0', 'nonce', 'ok', 'version']);
  });

  it('spiegelt die Nonce bereinigt (nur A-Z a-z 0-9 _ -, höchstens 80 Zeichen)', async () => {
    assert.equal((await b.keys({ nonce: 'ab.c!d-e_f g' })).kv.nonce, 'abcd-e_fg');
    assert.equal((await b.keys({ nonce: 'x'.repeat(100) })).kv.nonce, 'x'.repeat(80));
  });

  it('die Version im Poll und in der Liste ist dieselbe', async () => {
    await admin.addKey('Anna', 'annakey00001');
    assert.equal((await b.poll()).kv.keys_version, (await b.keys()).kv.version);
  });
});

describe('Jede Änderung, die die Box betrifft, erhöht die Version', () => {
  it('anlegen, löschen, Key ersetzen, stumm/laut, Stumm-Lautstärke, Demo an/aus', async () => {
    const anna = await admin.addKey('Anna', 'annakey00001');
    const bert = await admin.addKey('Bert', 'bertkey00001');
    let v = await version();
    const bumps = async (fields, what) => {
      await post(fields);
      const next = await version();
      assert.equal(next, v + 1, what);
      v = next;
    };
    await bumps({ action: 'keys_mute', id: anna.id, muted: '1' }, 'stumm');
    await bumps({ action: 'keys_mute', id: anna.id, muted: '0' }, 'laut');
    await bumps({ action: 'keys_set_mute_volume', id: anna.id, volume: '4' }, 'Stumm-Lautstärke');
    await bumps({ action: 'keys_replace_key', id: anna.id, key: 'annakey00002' }, 'neuer Key');
    await bumps({ action: 'demo_set', enabled: '1', id: bert.id }, 'Demo an');
    await bumps({ action: 'demo_set', enabled: '0' }, 'Demo aus');
    await bumps({ action: 'demo_set', enabled: '0' }, 'Demo erneut aus (Reparaturweg)');
    await bumps({ action: 'keys_delete', id: anna.id }, 'löschen');
  });

  it('was die Box nicht kennt, erhöht sie NICHT: Name, Telefon, neuer Kurzbefehl-Link', async () => {
    const anna = await admin.addKey('Anna', 'annakey00001');
    const v = await version();
    await post({ action: 'keys_rename', id: anna.id, label: 'Anna B.' });
    await post({ action: 'keys_set_phone', id: anna.id, phone: '0171 5550123' });
    await post({ action: 'keys_new_mute_token', id: anna.id });
    assert.equal(await version(), v);
    // Unveränderter Zustand ebenso wenig.
    await post({ action: 'keys_mute', id: anna.id, muted: '0' });
    await post({ action: 'keys_set_mute_volume', id: anna.id, volume: '0' });
    assert.equal(await version(), v);
  });

  it('"dauerhaft stumm" erhöht sie nur, wenn sich dabei stumm/laut ändert', async () => {
    const anna = await admin.addKey('Anna', 'annakey00001');
    let v = await version();
    await post({ action: 'keys_set_mute_permanent', id: anna.id, permanent: '1' });   // laut -> stumm
    assert.equal(await version(), v + 1);
    assert.deepEqual(rows((await b.keys()).kv), [['annakey00001', '1', '0']]);
    v += 1;
    await post({ action: 'keys_set_mute_permanent', id: anna.id, permanent: '0' });   // bleibt stumm
    assert.equal(await version(), v);
    await post({ action: 'keys_set_mute_permanent', id: anna.id, permanent: '1' });   // war schon stumm
    assert.equal(await version(), v);
    assert.equal((await b.keys()).text.includes('permanent'), false, 'das Flag selbst kennt die Box nicht');
  });
});

describe('Stumm-Zustand (Arbeitsmodus)', () => {
  it('stumme Empfänger bleiben in der Liste: muted=1 und ihre Stumm-Lautstärke', async () => {
    const anna = await admin.addKey('Anna', 'annakey00001');
    await admin.addKey('Bert', 'bertkey00001');
    await post({ action: 'keys_set_mute_volume', id: anna.id, volume: '3' });
    assert.deepEqual(rows((await b.keys()).kv), [['annakey00001', '0', '3'], ['bertkey00001', '0', '0']]);
    await post({ action: 'keys_mute', id: anna.id, muted: '1' });
    assert.deepEqual(rows((await b.keys()).kv), [['annakey00001', '1', '3'], ['bertkey00001', '0', '0']]);
  });

  it('eine überfällige Stummschaltung kommt als laut - unter einer NEUEN Version', async () => {
    const anna = await admin.addKey('Anna', 'annakey00001');
    await post({ action: 'keys_mute', id: anna.id, muted: '1' });
    await backend.state.ageMute(anna.id, 43300);
    const before = await backend.state.listVersion();
    const r = await b.keys();
    assert.equal(r.kv.version, String(before + 1));
    assert.deepEqual(rows(r.kv), [['annakey00001', '0', '0']]);
  });

  it('dauerhaft stumm bleibt stumm, egal wie alt', async () => {
    const anna = await admin.addKey('Anna', 'annakey00001');
    await post({ action: 'keys_set_mute_permanent', id: anna.id, permanent: '1' });
    await backend.state.ageMute(anna.id, 900000);
    const before = await backend.state.listVersion();
    const r = await b.keys();
    assert.equal(r.kv.version, String(before));
    assert.deepEqual(rows(r.kv), [['annakey00001', '1', '0']]);
  });
});

describe('Demo-Modus', () => {
  it('die Box bekommt nur den Demo-Empfänger - und nach dem Ausschalten wieder alle', async () => {
    await admin.addKey('Anna', 'annakey00001');
    const bert = await admin.addKey('Bert', 'bertkey00001');
    await post({ action: 'demo_set', enabled: '1', id: bert.id });
    let r = await b.keys();
    assert.equal(r.kv.version, String(await version()));
    assert.deepEqual(rows(r.kv), [['bertkey00001', '0', '0']]);
    await post({ action: 'demo_set', enabled: '0' });
    r = await b.keys();
    assert.deepEqual(rows(r.kv).map((x) => x[0]), ['annakey00001', 'bertkey00001']);
  });

  it('ist der Demo-Empfänger stumm, gilt das auch in der Demo-Liste', async () => {
    const anna = await admin.addKey('Anna', 'annakey00001');
    await post({ action: 'keys_set_mute_volume', id: anna.id, volume: '2' });
    await post({ action: 'keys_mute', id: anna.id, muted: '1' });
    await post({ action: 'demo_set', enabled: '1', id: anna.id });
    assert.deepEqual(rows((await b.keys()).kv), [['annakey00001', '1', '2']]);
  });

  it('Demo-Empfänger muss aus der Liste stammen und ist nicht löschbar', async () => {
    const anna = await admin.addKey('Anna', 'annakey00001');
    await admin.addKey('Bert', 'bertkey00001');
    assert.equal((await admin.post({ action: 'demo_set', enabled: '1', id: 999 })).ok, false);
    assert.equal((await admin.get('status')).demo.enabled, false);
    await post({ action: 'demo_set', enabled: '1', id: anna.id });
    assert.equal((await admin.post({ action: 'keys_delete', id: anna.id })).ok, false);
    assert.deepEqual(rows((await b.keys()).kv).map((x) => x[0]), ['annakey00001']);
  });
});

describe('Bereinigung wie in der Firmware', () => {
  it('das Dashboard nimmt höchstens 10 Empfänger, keinen Key doppelt und nur gültige', async () => {
    for (let i = 1; i <= 10; i++) await admin.addKey(`E${i}`, `validkey${String(i).padStart(5, '0')}`);
    assert.equal((await admin.post({ action: 'keys_add', label: 'Elf', key: 'validkey00011' })).ok, false);
    assert.equal((await b.keys()).kv.count, '10');
    await post({ action: 'keys_delete', id: (await admin.keys()).keys[9].id });
    assert.equal((await admin.post({ action: 'keys_add', label: 'Doppelt', key: 'validkey00001' })).ok, false);
    assert.equal((await admin.post({ action: 'keys_add', label: 'Kurz', key: 'abc' })).ok, false);
    assert.equal((await admin.post({ action: 'keys_add', label: 'Zeichen', key: 'mit leer zeichen' })).ok, false);
    assert.equal((await b.keys()).kv.count, '9');
  });

  it('der letzte Empfänger ist nicht löschbar', async () => {
    const anna = await admin.addKey('Anna', 'annakey00001');
    const r = await admin.post({ action: 'keys_delete', id: anna.id });
    assert.equal(r.ok, false);
    assert.equal((await b.keys()).kv.count, '1');
  });
});

describe('Versionen und ids laufen nie rückwärts', () => {
  it('gelöschte ids werden nicht neu vergeben', async () => {
    const anna = await admin.addKey('Anna', 'annakey00001');
    const bert = await admin.addKey('Bert', 'bertkey00001');
    await post({ action: 'keys_delete', id: bert.id });
    const cara = await admin.addKey('Cara', 'carakey00001');
    assert.ok(cara.id > bert.id && bert.id > anna.id);
  });
});
