// Hilfen für die Szenarien: Status-Push der simulierten Box und ein Lauf des
// Cron-Wächters in der Sandbox-Kopie.
import { execFileSync } from 'node:child_process';
import { dirname, join } from 'node:path';

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
export async function pushBox(base, session, overrides = {}) {
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
  const res = await fetch(`${base}/api/status.php`, { method: 'POST', body: form });
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
