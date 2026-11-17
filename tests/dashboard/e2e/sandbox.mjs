// Hilfen für die Szenarien: Status-Push der simulierten Box, ein Lauf des
// Wächters, Bark-Anfragen des Prüfstands, Server an/aus.
// Dünne Hüllen um tests/backend/harness.mjs - was dahinter läuft (PHP oder
// später Go), entscheidet FW_BACKEND dort.
import { box } from '../../backend/harness.mjs';

// Status-Push der Box. Mit session (apiSession des Admins) passen
// keys_version/keys_count/keys_muted zur aktuellen Empfängerliste - sonst
// erscheint nach 60 s der Hinweis „Box hat die Liste nicht übernommen“.
// headers: zusätzliche Kopfzeilen des Pushs (z.B. X-FW-Bridge wie von der NAS-Brücke).
export async function pushBox(backend, session, overrides = {}, headers = {}) {
  const fields = {};
  if (session) {
    const list = await session.get('keys_list');
    if (!list.ok) throw new Error('keys_list fehlgeschlagen: ' + JSON.stringify(list));
    const keys = Array.isArray(list.keys) ? list.keys : [];
    const demo = list.demo && list.demo.enabled ? list.demo : null;
    const target = demo ? keys.find((k) => String(k.id) === String(demo.key_id)) : null;
    fields.keys_version = String(list.version);
    fields.keys_count = String(demo ? 1 : keys.length);
    fields.keys_muted = String(demo ? (target && target.muted ? 1 : 0) : keys.filter((k) => k.muted).length);
  }
  Object.assign(fields, overrides);
  const res = await box(backend).status(fields, headers);
  if (res.status !== 200 || res.text.trim() !== 'OK') {
    throw new Error(`Status-Push abgelehnt (HTTP ${res.status}): ${res.text.trim()}`);
  }
  return fields;
}

// Ein Lauf des Wächters (wie der Timer auf dem Server).
export function runWatchdog(backend) {
  return backend.runWatchdog();
}

// Letzter Status-Push "vor <s> Sekunden": simuliert eine verstummte Box, ohne
// die übrigen Werte zu ändern.
export async function setStatusAge(backend, seconds) {
  await backend.state.ageBoxStatus(seconds);
}

// Bark-Attrappe: alle bisherigen Anfragen ({t, key, fields}).
export function barkLog(backend) {
  return backend.bark.requests.map(({ t, key, fields }) => ({ t, key, fields }));
}

// Dashboard-Server anhalten/wieder starten (gleicher Port, Daten bleiben).
export function sandboxServer(backend, action) {
  return action === 'stop' ? backend.stopServer() : backend.startServer();
}
