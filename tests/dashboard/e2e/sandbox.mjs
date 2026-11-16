// Hilfen für die Szenarien: Status-Push der simulierten Box, ein Lauf des
// Cron-Wächters, Laufzeitdateien und Bark-Log der Sandbox, Server an/aus.
import { execFileSync, spawn } from 'node:child_process';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const scripts = join(dirname(fileURLToPath(import.meta.url)), '..', 'sandbox');

// Standardwerte wie in sandbox/box.sh (beide Stellen gleich halten).
const BOX_DEFAULTS = {
  token: 'box-token', device_id: 'fw-box-1', fw_build: 'sandbox', core: '2.0.17', uptime_s: '360000',
  wifi: 'up', rssi: '-61', ip: '192.168.1.50', relay: 'open', cooldown: '0', waiting_for_release: '0',
  alarm_pending: '0', isr_latched: '0', key_progress: '0/0', last_alarm: 'nie', last_heartbeat: 'nie',
  heartbeat_ok: '0', stuck_warned: '0', ack_pending: '0', keys_count: '1', keys_version: '0', keys_muted: '0',
  reset_reason: 'poweron', free_heap: '180000', max_alloc_heap: '90000', alarm_seq: '0', alarm_source: '',
  alarm_gaveup: '0',
};

// Git-Bash-Pfad (/c/...) in einen Pfad wandeln, den Node und PHP verstehen.
export function winPath(p) {
  return String(p).replace(/^\/([a-zA-Z])\//, (_, d) => `${d.toUpperCase()}:/`);
}

// Status-Push der Box. Mit session (apiSession des Admins) passen
// keys_version/keys_count/keys_muted zur aktuellen Empfängerliste - sonst
// erscheint nach 60 s der Hinweis „Box hat die Liste nicht übernommen“.
// headers: zusätzliche Kopfzeilen des Pushs (z.B. X-FW-Bridge wie von der NAS-Brücke).
export async function pushBox(base, session, overrides = {}, headers = {}) {
  const fields = { ...BOX_DEFAULTS };
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
  const form = new FormData();
  for (const [k, v] of Object.entries(fields)) form.append(k, String(v));
  const res = await fetch(`${base}/api/status.php`, { method: 'POST', body: form, headers });
  const text = await res.text();
  if (!res.ok || text.trim() !== 'OK') throw new Error(`Status-Push abgelehnt (HTTP ${res.status}): ${text.trim()}`);
  return fields;
}

// Ein Lauf von cron/check_offline.php (wie der DSM-Aufgabenplaner), mit
// denselben Erweiterungen wie start.sh (curl für den Selbsttest).
export function runWatchdog(sandbox) {
  const php = execFileSync('php', ['-r', 'echo PHP_BINARY;'], { encoding: 'utf8' }).trim();
  const ext = join(dirname(php), 'ext');
  return execFileSync('php', [
    '-d', `extension_dir=${ext}`, '-d', 'extension=curl', '-d', 'extension=openssl',
    join(winPath(sandbox), 'site', 'cron', 'check_offline.php'),
  ], { encoding: 'utf8' });
}

// Laufzeitdatei im data_dir lesen/ändern (Format wie write_json_file():
// Guard-Zeile, dann JSON). name ohne Endung, z.B. 'watchdog_state'.
// Liefert null, wenn die Datei fehlt.
export function readData(sandbox, name) {
  const p = join(winPath(sandbox), 'data', `${name}.json.php`);
  if (!existsSync(p)) return null;
  const raw = readFileSync(p, 'utf8');
  const nl = raw.indexOf('\n');
  return JSON.parse(raw.slice(nl + 1));
}
export function editData(sandbox, name, fn) {
  const p = join(winPath(sandbox), 'data', `${name}.json.php`);
  const raw = readFileSync(p, 'utf8');
  const nl = raw.indexOf('\n');
  const data = JSON.parse(raw.slice(nl + 1));
  fn(data);
  writeFileSync(p, raw.slice(0, nl + 1) + JSON.stringify(data, null, 4));
  return data;
}

// Letzter Status-Push "vor <s> Sekunden" (wie box.sh --age): simuliert eine
// verstummte Box, ohne die übrigen Werte zu ändern.
export function setStatusAge(sandbox, seconds) {
  return editData(sandbox, 'latest_status', (d) => { d.seen_at = Math.floor(Date.now() / 1000) - seconds; });
}

// Bark-Attrappe: alle bisher protokollierten Anfragen ({t, key, fields}).
export function barkLog(sandbox) {
  const p = join(winPath(sandbox), 'bark.log');
  if (!existsSync(p)) return [];
  return readFileSync(p, 'utf8').split('\n').filter((l) => l.trim()).map((l) => JSON.parse(l));
}

// Dashboard-Server (bzw. Attrappe) der Sandbox stoppen/starten - über die
// Skripte unter sandbox/ (Git Bash; dort bekommt taskkill die richtige PID).
// Starten läuft losgelöst (detached): Sonst hinge der neue php-Prozess im
// Job-Objekt dieses Node-Prozesses und endete mit dem Szenario.
export function sandboxServer(sandbox, action, which = 'site') {
  const script = join(scripts, action === 'stop' ? 'stop.sh' : 'start.sh').replace(/\\/g, '/');
  if (action === 'stop') {
    return Promise.resolve(execFileSync('bash', [script, sandbox, which], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }));
  }
  return new Promise((resolve, reject) => {
    const child = spawn('bash', [script, sandbox, which], { detached: true, stdio: 'ignore', windowsHide: true });
    child.on('error', reject);
    child.on('exit', (code) => (code === 0 ? resolve('gestartet') : reject(new Error(`start.sh endete mit ${code}`))));
  });
}
