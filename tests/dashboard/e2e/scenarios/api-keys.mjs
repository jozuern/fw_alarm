// Dashboard-API: Empfänger umbenennen, Key ersetzen, Telefonnummer (keys_rename /
// keys_replace_key / keys_set_phone / keys_add mit phone).
import { apiSession } from '../cdp.mjs';

export default async function ({ base, assert }) {
  const s = await apiSession(base, 'admin', 'test');
  // Eindeutige Keys je Lauf: Die Sandbox behält ihre Daten zwischen den Läufen.
  const u = Date.now().toString(36);
  const k1 = 'annakey' + u + 'a', k2 = 'annakey' + u + 'b', k3 = 'annakey' + u + 'c', kMax = 'maxkey' + u;
  const kPhone = 'phonekey' + u, kBad = 'badphone' + u;
  const list = async () => s.get('keys_list');
  const byId = (r, id) => r.keys.find((x) => x.id === id);
  const created = [];

  try {
    // keys_add mit phone: gültig gespeichert, ungültig lehnt alles ab
    let r = await s.post({ action: 'keys_add', label: 'Anna', key: k1 });
    assert(r.ok === true, 'keys_add Anna: ' + JSON.stringify(r));
    // (mehrere "Anna" aus alten Läufen möglich: die mit der jüngsten id nehmen)
    const idAnna = Math.max(...(await list()).keys.filter((k) => k.label === 'Anna').map((k) => k.id));
    created.push(idAnna);

    r = await s.post({ action: 'keys_add', label: 'Telefon', key: kPhone, phone: '+49 171 99887' });
    assert(r.ok === true, 'keys_add mit phone: ' + JSON.stringify(r));
    const idPhone = Math.max(...(await list()).keys.filter((k) => k.label === 'Telefon').map((k) => k.id));
    created.push(idPhone);
    assert(byId(await list(), idPhone).phone === '+49 171 99887', 'phone bei keys_add gespeichert');
    const before = (await list()).keys.length;
    r = await s.post({ action: 'keys_add', label: 'Kaputt', key: kBad, phone: 'abc' });
    assert(r.ok === false, 'ungültiges phone lehnt keys_add ab');
    assert((await list()).keys.length === before, 'bei ungültigem phone wird nichts angelegt');

    const id1 = idAnna;
    const v0 = (await list()).version;
    r = await s.post({ action: 'keys_rename', id: id1, label: 'Anna B.' });
    assert(r.ok === true, 'keys_rename: ' + JSON.stringify(r));
    assert((await list()).version === v0, 'rename erhöht die Version nicht');
    r = await s.post({ action: 'keys_rename', id: id1, label: '' });
    assert(r.ok === false, 'leerer Name abgelehnt');

    assert((await s.post({ action: 'keys_set_phone', id: id1, phone: '+49 171 1234567' })).ok === true, 'set_phone gültig');
    assert((await s.post({ action: 'keys_set_phone', id: id1, phone: 'abc' })).ok === false, 'set_phone ungültig abgelehnt');
    assert((await list()).version === v0, 'phone erhöht die Version nicht');
    let k = byId(await list(), id1);
    assert(k.label === 'Anna B.', 'neuer Name: ' + k.label);
    assert(k.phone === '+49 171 1234567', 'Telefon gespeichert: ' + k.phone);
    const token = k.mute_token;

    r = await s.post({ action: 'keys_replace_key', id: id1, key: k2 });
    assert(r.ok === true, 'replace_key: ' + JSON.stringify(r));
    const after = await list();
    k = byId(after, id1);
    assert(after.version > v0, 'replace_key erhöht die Version');
    assert(k.mute_token === token, 'Token bleibt');
    assert(k.phone === '+49 171 1234567', 'Telefon bleibt');
    assert(k.label === 'Anna B.', 'Name bleibt');

    r = await s.post({ action: 'keys_add', label: 'Max', key: kMax });
    assert(r.ok === true, 'keys_add Max');
    created.push(Math.max(...(await list()).keys.filter((x) => x.label === 'Max').map((x) => x.id)));
    assert((await s.post({ action: 'keys_replace_key', id: id1, key: kMax })).ok === false, 'doppelter Key abgelehnt');
    assert((await s.post({ action: 'keys_replace_key', id: id1, key: 'x' })).ok === false, 'ungültiger Key abgelehnt');
    assert((await s.post({ action: 'keys_replace_key', id: 999999, key: k3 })).ok === false, 'unbekannte id abgelehnt');

    // Demo-Empfänger: Key nicht ersetzbar, Umbenennen zieht das Banner mit
    r = await s.post({ action: 'demo_set', enabled: '1', id: id1 });
    assert(r.ok === true, 'demo_set an: ' + JSON.stringify(r));
    r = await s.post({ action: 'keys_replace_key', id: id1, key: k3 });
    assert(r.ok === false && /Demo-Empfänger/.test(r.message), 'Demo-Empfänger: Key-Ersatz abgelehnt: ' + JSON.stringify(r));
    r = await s.post({ action: 'keys_rename', id: id1, label: 'Anna C.' });
    assert(r.ok === true, 'rename des Demo-Empfängers: ' + JSON.stringify(r));
    assert((await s.get('status')).demo.label === 'Anna C.', 'Demo-Banner zeigt neuen Namen');
    assert((await list()).demo.label === 'Anna C.', 'keys_list.demo zeigt neuen Namen');
    await s.post({ action: 'demo_set', enabled: '0' });

    const ro = await apiSession(base, 'leser', 'test');
    const roList = await ro.get('keys_list');
    assert(roList.keys.length > 0 && roList.keys.every((x) => !('phone' in x)), 'Lese-Rolle sieht kein phone');
    for (const a of ['keys_rename', 'keys_replace_key', 'keys_set_phone']) {
      assert((await ro.post({ action: a, id: id1, label: 'x', key: k3, phone: '123' })).status === 403, a + ' für Lese-Rolle 403');
    }

    // Verlauf: Ereignisse da, keine Telefonnummer, kein Klartext-Key
    const logRes = await s.get('log_view', { limit: 40, filter: 'keys' });
    const events = logRes.entries.map((e) => e.event);
    for (const e of ['key_renamed', 'key_replaced', 'key_phone_changed']) assert(events.includes(e), 'Verlauf ohne ' + e);
    const raw = JSON.stringify(logRes);
    assert(!raw.includes('1234567') && !raw.includes('99887'), 'Telefonnummer im Verlauf');
    assert(!raw.includes(k1) && !raw.includes(k2), 'Klartext-Key im Verlauf');

    // Telefon entfernen
    assert((await s.post({ action: 'keys_set_phone', id: id1, phone: '' })).ok === true, 'phone leeren');
    assert(byId(await list(), id1).phone === '', 'phone ist leer');
  } finally {
    await s.post({ action: 'demo_set', enabled: '0' });
    for (const cid of created) await s.post({ action: 'keys_delete', id: cid });
  }
}
