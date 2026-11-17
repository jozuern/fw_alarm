// Empfängerliste für die Box (api/keys.php): Klartext-Format, was die Box
// NICHT bekommt (Namen, Tokens, Telefon), Stumm-Zustand, Demo-Modus,
// Bereinigung wie in der Firmware, Rückgriff auf die Sicherung - und dass
// Versionen und ids nie rückwärts laufen. Die Box speichert, was hier kommt,
// und alarmiert danach genau diese Keys.
import assert from 'node:assert/strict';
import { after, before, beforeEach, describe, it } from 'node:test';
import { box, now, session, startBackend } from './harness.mjs';

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
    backend.data.edit('alarm_keys.json', (d) => { d.keys[0].muted_at = now() - 43300; });
    const before = backend.data.read('alarm_keys.json').version;
    const r = await b.keys();
    assert.equal(r.kv.version, String(before + 1));
    assert.deepEqual(rows(r.kv), [['annakey00001', '0', '0']]);
  });

  it('dauerhaft stumm bleibt stumm, egal wie alt', async () => {
    const anna = await admin.addKey('Anna', 'annakey00001');
    await post({ action: 'keys_set_mute_permanent', id: anna.id, permanent: '1' });
    backend.data.edit('alarm_keys.json', (d) => { d.keys[0].muted_at = now() - 900000; });
    const before = backend.data.read('alarm_keys.json').version;
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

  it('gelöschte ids werden nicht neu vergeben', async () => {
    const anna = await admin.addKey('Anna', 'annakey00001');
    const bert = await admin.addKey('Bert', 'bertkey00001');
    await post({ action: 'keys_delete', id: bert.id });
    const cara = await admin.addKey('Cara', 'carakey00001');
    assert.ok(cara.id > bert.id && bert.id > anna.id);
  });
});
