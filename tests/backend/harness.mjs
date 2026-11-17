// Prüfstand für die Server-Seite (Box-Protokoll, Wächter, Kurzbefehl-Link,
// Alarm-Versand): startet pro Testdatei ein EIGENES Dashboard mit leerem
// Datenordner und eine Bark-Attrappe im Testprozess - eigene Ports, eigener
// Temp-Ordner, nichts bleibt liegen. Die Tests sprechen nur HTTP und fassen
// den Datenordner über backend.data an; welche Implementierung dahinter läuft
// (heute PHP, später Go), entscheidet allein der Treiber hier (FW_BACKEND).
//
// Aufruf: node --test "tests/backend/*.test.mjs"
import { execFile, execFileSync, spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  cpSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync,
} from 'node:fs';
import { createServer } from 'node:http';
import { createServer as createTcpServer } from 'node:net';
import { tmpdir } from 'node:os';
import { dirname, join, relative, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const REPO = join(here, '..', '..');

export const sha256 = (text) => createHash('sha256').update(text).digest('hex');
export const now = () => Math.floor(Date.now() / 1000);
export const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

// Wartet, bis probe() etwas Wahres liefert (z.B. eine Bark-Meldung, die der
// Server erst NACH seiner Antwort verschickt). Liefert den Wert oder wirft.
export async function waitFor(probe, what = 'Bedingung', timeoutMs = 8000) {
  const end = Date.now() + timeoutMs;
  for (;;) {
    const value = await probe();
    // Eine leere Liste heißt "noch nichts da" (in JavaScript wäre sie wahr).
    if (Array.isArray(value) ? value.length > 0 : value) return value;
    if (Date.now() > end) throw new Error(`Nicht eingetreten nach ${timeoutMs} ms: ${what}`);
    await sleep(50);
  }
}
// Für "es darf NICHTS kommen": so lange warten, wie ein Nachlauf höchstens braucht.
export const settle = () => sleep(400);

// ---- Zugänge des Prüfstands (wie tests/dashboard/sandbox) ------------------
export const MACHINE_TOKEN = 'box-token';
export const WATCHER_TOKEN = 'watcher-token';
export const STATUS_KEY = 'statuskey0001';
export const ADMIN = { user: 'admin', password: 'test' };
export const READER = { user: 'leser', password: 'test' };
// bcrypt-Hash von "test" (cost 4, damit die Anmeldung im Test nichts kostet).
const TEST_PASSWORD_HASH = '$2y$04$r60I68m5mVq98EDvnpGXGeS1SZtBd2wWlr0dy0MuX73YPDZ27VzZy';
const DATA_FILE_GUARD = '<?php http_response_code(404); exit; ?>\n';

function freePort() {
  return new Promise((resolve, reject) => {
    const probe = createTcpServer();
    probe.on('error', reject);
    probe.listen(0, '127.0.0.1', () => {
      const { port } = probe.address();
      probe.close(() => resolve(port));
    });
  });
}

// ============================ BARK-ATTRAPPE =================================
// Nimmt jede Bark-Anfrage an und merkt sie sich: {t, key, fields}. Antwort
// nach Key-Präfix: fail -> 500, bad -> 400, slow -> 200 nach slowMs,
// drop -> Verbindung kappen (Transportfehler), sonst 200. Ein Test kann mit
// bark.respond = (anfrage) => ({status: 503}) alles überschreiben (null =
// Standard).
export async function startBarkMock() {
  const mock = { requests: [], slowMs: 1500, respond: null };
  const sockets = new Set();
  const server = createServer((req, res) => {
    const chunks = [];
    req.on('data', (chunk) => chunks.push(chunk));
    req.on('end', () => {
      const url = new URL(req.url, 'http://bark.invalid');
      const segments = url.pathname.split('/').filter(Boolean).map(decodeURIComponent);
      const raw = Buffer.concat(chunks).toString('utf8');
      let fields = {};
      if (String(req.headers['content-type'] || '').includes('json')) {
        try { fields = JSON.parse(raw) || {}; } catch { fields = {}; }
      } else {
        fields = Object.fromEntries(new URLSearchParams(raw));
      }
      for (const [k, v] of url.searchParams) if (!(k in fields)) fields[k] = v;
      const key = String(fields.device_key ?? segments[0] ?? '');
      delete fields.device_key;
      res.setHeader('content-type', 'application/json');
      if (key === '') {
        res.end('{"code":200,"message":"success"}');
        return;
      }
      const entry = { t: Date.now(), key, fields, method: req.method };
      mock.requests.push(entry);
      let plan = mock.respond ? mock.respond(entry) : null;
      if (plan == null) {
        if (key.startsWith('fail')) plan = { status: 500 };
        else if (key.startsWith('bad')) plan = { status: 400 };
        else if (key.startsWith('slow')) plan = { status: 200, delayMs: mock.slowMs };
        else if (key.startsWith('drop')) plan = { drop: true };
        else plan = { status: 200 };
      }
      if (plan.drop) {
        req.socket.destroy();
        return;
      }
      const status = plan.status ?? 200;
      const answer = () => {
        res.statusCode = status;
        res.end(JSON.stringify({ code: status, message: status === 200 ? 'success' : 'mock' }));
      };
      if (plan.delayMs) setTimeout(answer, plan.delayMs); else answer();
    });
  });
  server.on('connection', (socket) => {
    sockets.add(socket);
    socket.on('close', () => sockets.delete(socket));
  });
  const port = await freePort();
  await new Promise((resolve, reject) => {
    server.on('error', reject);
    server.listen(port, '127.0.0.1', resolve);
  });
  mock.base = `http://127.0.0.1:${port}`;
  mock.mark = () => mock.requests.length;
  mock.since = (mark = 0) => mock.requests.slice(mark);
  mock.to = (key, mark = 0) => mock.requests.slice(mark).filter((r) => r.key === key);
  mock.clear = () => { mock.requests.length = 0; mock.respond = null; mock.slowMs = 1500; };
  mock.stop = () => new Promise((resolve) => {
    for (const socket of sockets) socket.destroy();
    server.close(() => resolve());
  });
  return mock;
}

// ============================ DATENORDNER ===================================
// Laufzeitdateien wie die Anwendung sie schreibt: <name>.php, erste Zeile =
// Guard, danach JSON (bzw. beim Verlauf eine JSON-Zeile pro Eintrag). name ist
// der logische Name aus CLAUDE.md, z.B. 'latest_status.json', 'commands.log'.
function dataAccess(dataDir) {
  const file = (name) => join(dataDir, `${name}.php`);
  const body = (name) => {
    const raw = readFileSync(file(name), 'utf8');
    return raw.startsWith('<?php') ? raw.slice(raw.indexOf('\n') + 1) : raw;
  };
  const api = {
    dir: dataDir,
    path: file,
    exists: (name) => existsSync(file(name)),
    read(name) {
      return existsSync(file(name)) ? JSON.parse(body(name)) : null;
    },
    write(name, value) {
      writeFileSync(file(name), DATA_FILE_GUARD + JSON.stringify(value, null, 4));
      return value;
    },
    // Ändert eine vorhandene Datei; fehlt sie, bekommt fn ein leeres Objekt.
    edit(name, fn) {
      const value = api.read(name) ?? {};
      fn(value);
      return api.write(name, value);
    },
    remove(name) {
      rmSync(file(name), { recursive: true, force: true });
    },
    // Kaputte Datei (kein JSON) - wie nach einem abgebrochenen Schreiben von Hand.
    corrupt(name) {
      writeFileSync(file(name), DATA_FILE_GUARD + '{kaputt');
    },
    // Macht das Speichern DIESER Datei unmöglich, auf jedem System: An ihrer
    // Stelle liegt ein nicht leerer Ordner, das Ersetzen per rename scheitert.
    // (Ein Schreibschutz per chmod wirkt unter Windows und als root nicht.)
    block(name) {
      rmSync(file(name), { recursive: true, force: true });
      mkdirSync(file(name));
      writeFileSync(join(file(name), 'belegt'), 'x');
    },
    unblock(name) {
      rmSync(file(name), { recursive: true, force: true });
    },
    // Alle Einträge des Verlaufs (älteste zuerst), roh wie gespeichert.
    log() {
      if (!existsSync(file('commands.log'))) return [];
      return body('commands.log').split('\n').filter((line) => line.trim()).map((line) => JSON.parse(line));
    },
  };
  return api;
}

// ============================ TREIBER: PHP ==================================
let phpInfo = null;
function php() {
  if (phpInfo) return phpInfo;
  const out = execFileSync('php', ['-r',
    'echo PHP_BINARY, "\\n", (int)extension_loaded("curl"), (int)extension_loaded("openssl");'],
  { encoding: 'utf8' }).trim().split('\n');
  const bin = out[0].trim();
  const flags = [];
  // Lokales PHP ohne php.ini (Windows): curl/openssl von Hand dazuladen.
  if (out[1] !== '11') {
    flags.push('-d', `extension_dir=${join(dirname(bin), 'ext')}`);
    if (out[1][0] !== '1') flags.push('-d', 'extension=curl');
    if (out[1][1] !== '1') flags.push('-d', 'extension=openssl');
  }
  phpInfo = { bin, flags };
  return phpInfo;
}

// data_dir muss mit "/" beginnen: unter Windows die laufwerks-relative Form
// (/Users/...), PHP läuft dafür mit Arbeitsordner auf demselben Laufwerk.
const phpPath = (p) => p.replace(/\\/g, '/').replace(/^[A-Za-z]:/, '');

// config.php des Prüfstands: Vorlage aus dem Repo plus die Werte aus
// config.test.json - so stellt backend.configure() ohne Neustart um.
const CONFIG_PHP = `<?php
$c = require __DIR__ . '/config.example.php';
$o = json_decode((string)@file_get_contents(__DIR__ . '/config.test.json'), true);
return array_replace($c, is_array($o) ? $o : []);
`;

async function startPhp(root, bark) {
  const { bin, flags } = php();
  const site = join(root, 'site');
  const dataDir = join(root, 'data');
  // FW_BACKEND_SRC: ein anderer Stand der Anwendung als der im Repo (z.B.
  // eine absichtlich verfälschte Kopie, um zu prüfen, dass die Tests anschlagen).
  const source = process.env.FW_BACKEND_SRC || join(REPO, 'nas_dashboard');
  cpSync(source, site, {
    recursive: true,
    filter: (src) => {
      const rel = relative(source, src);
      return rel !== 'config.php' && rel.split(sep)[0] !== 'data';
    },
  });
  mkdirSync(dataDir, { recursive: true });
  writeFileSync(join(site, 'config.php'), CONFIG_PHP);

  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  const baseConfig = {
    machine_token_sha256: sha256(MACHINE_TOKEN),
    dashboard_user: ADMIN.user,
    dashboard_password_hash: TEST_PASSWORD_HASH,
    dashboard_readonly_user: READER.user,
    dashboard_readonly_password_hash: TEST_PASSWORD_HASH,
    bark_key_status: STATUS_KEY,
    bark_host: bark.base,
    bark_keys_alarm: ['startkey0001'],
    data_dir: phpPath(dataDir),
    health_check_url: `${base}/api/health.php`,
  };
  const writeConfig = (overrides) => writeFileSync(join(site, 'config.test.json'),
    JSON.stringify({ ...baseConfig, ...overrides }));
  writeConfig({});

  let log = '';
  const child = spawn(bin, [...flags, '-S', `127.0.0.1:${port}`, '-t', site],
    { cwd: site, stdio: ['ignore', 'pipe', 'pipe'], windowsHide: true });
  const keep = (chunk) => { log = (log + chunk).slice(-200000); };
  child.stdout.on('data', keep);
  child.stderr.on('data', keep);
  const exited = new Promise((resolve) => child.on('exit', resolve));
  const killOnExit = () => { try { child.kill(); } catch { /* schon beendet */ } };
  process.on('exit', killOnExit);

  // Warten, bis der Server antwortet.
  let up = false;
  for (let i = 0; i < 100 && !up; i++) {
    try {
      await fetch(`${base}/manifest.json`, { signal: AbortSignal.timeout(1000) });
      up = true;
    } catch {
      if (child.exitCode !== null) break;
      await sleep(100);
    }
  }
  if (!up) {
    killOnExit();
    throw new Error(`PHP-Server startet nicht (Port ${port}):\n${log}`);
  }

  return {
    kind: 'php',
    base,
    dataDir,
    serverLog: () => log,
    // Konfiguration = Standard des Prüfstands + overrides (nicht aufsummiert).
    async configure(overrides = {}) {
      writeConfig(overrides);
    },
    // Ein Lauf des Wächters (wie der systemd-Timer auf dem Server).
    runWatchdog() {
      return new Promise((resolve, reject) => {
        execFile(bin, [...flags, join(site, 'cron', 'check_offline.php')],
          { cwd: site, encoding: 'utf8', timeout: 60000 },
          (error, stdout, stderr) => (error
            ? reject(new Error(`Wächter-Lauf gescheitert: ${error.message}\n${stderr}`))
            : resolve(String(stdout).trim())));
      });
    },
    // Zurück auf Anfang: leerer Datenordner, Standard-Konfiguration.
    async reset() {
      for (const name of readdirSync(dataDir)) {
        rmSync(join(dataDir, name), { recursive: true, force: true, maxRetries: 10, retryDelay: 50 });
      }
      writeConfig({});
    },
    async stop() {
      process.off('exit', killOnExit);
      killOnExit();
      await exited;
    },
  };
}

// ============================ START =========================================
// options.config: Abweichungen von der Standard-Konfiguration ab dem Start.
export async function startBackend(options = {}) {
  const kind = process.env.FW_BACKEND || 'php';
  const drivers = { php: startPhp };
  if (!drivers[kind]) {
    throw new Error(`Unbekanntes FW_BACKEND "${kind}" (vorhanden: ${Object.keys(drivers).join(', ')})`);
  }
  const root = mkdtempSync(join(tmpdir(), 'fw-backend-'));
  const bark = await startBarkMock();
  let driver;
  try {
    driver = await drivers[kind](root, bark);
  } catch (error) {
    await bark.stop();
    rmSync(root, { recursive: true, force: true, maxRetries: 10, retryDelay: 100 });
    throw error;
  }
  if (options.config) await driver.configure(options.config);
  const backend = {
    ...driver,
    root,
    bark,
    data: dataAccess(driver.dataDir),
    async reset() {
      await driver.reset();
      bark.clear();
    },
    async stop() {
      await driver.stop();
      await bark.stop();
      if (!process.env.FW_KEEP) {
        rmSync(root, { recursive: true, force: true, maxRetries: 10, retryDelay: 100 });
      }
    },
  };
  return backend;
}

// ============================ HTTP-HELFER ===================================
// Antwort als {status, text, headers, kv}; kv = die "name=wert"-Zeilen des
// Klartext-Protokolls.
export function parseKv(text) {
  const out = {};
  for (const line of String(text).split('\n')) {
    const eq = line.indexOf('=');
    if (eq > 0) out[line.slice(0, eq)] = line.slice(eq + 1);
  }
  return out;
}

export async function request(url, init = {}) {
  const res = await fetch(url, { redirect: 'manual', signal: AbortSignal.timeout(30000), ...init });
  const text = await res.text();
  return { status: res.status, text, headers: res.headers, kv: parseKv(text) };
}

// POST wie die Firmware: application/x-www-form-urlencoded.
export function postForm(url, fields = {}, headers = {}) {
  const body = new URLSearchParams();
  for (const [k, v] of Object.entries(fields)) if (v !== undefined && v !== null) body.append(k, String(v));
  return request(url, {
    method: 'POST',
    headers: { 'content-type': 'application/x-www-form-urlencoded', ...headers },
    body,
  });
}

// ============================ DIE BOX =======================================
// Felder eines Status-Pushs der aktuellen Firmware (Stand main).
export const BOX_FIELDS = {
  device_id: 'fw-box-1', fw_build: 'test', core: '2.0.17', uptime_s: '360000',
  wifi: 'up', rssi: '-61', ip: '192.168.1.50', relay: 'open', cooldown: '0', waiting_for_release: '0',
  alarm_pending: '0', isr_latched: '0', key_progress: '0/0', last_alarm: 'nie', last_heartbeat: 'nie',
  heartbeat_ok: '0', stuck_warned: '0', ack_pending: '0', keys_count: '1', keys_version: '0', keys_muted: '0',
  reset_reason: 'poweron', free_heap: '180000', max_alloc_heap: '90000', alarm_seq: '0', alarm_source: '',
  alarm_gaveup: '0',
};
// Die Box im Betrieb läuft noch mit einem älteren Stand: Diese Felder kennt sie nicht.
export const OLD_FIRMWARE_MISSING = ['core', 'alarm_seq', 'alarm_source', 'max_alloc_heap', 'alarm_gaveup'];

// Eine simulierte Box. overrides gelten für jeden Aufruf; ein Feld mit dem
// Wert undefined wird weggelassen (so entsteht z.B. die alte Firmware).
export function box(backend, defaults = {}) {
  const token = defaults.token ?? MACHINE_TOKEN;
  const deviceId = defaults.device_id ?? BOX_FIELDS.device_id;
  const api = (name) => `${backend.base}/api/${name}.php`;
  return {
    deviceId,
    status(overrides = {}, headers = {}) {
      return postForm(api('status'), { token, ...BOX_FIELDS, ...defaults, ...overrides }, headers);
    },
    oldStatus(overrides = {}, headers = {}) {
      const fields = { token, ...BOX_FIELDS, ...defaults, ...overrides };
      for (const name of OLD_FIRMWARE_MISSING) if (!(name in overrides)) delete fields[name];
      return postForm(api('status'), fields, headers);
    },
    poll(fields = {}) {
      return postForm(api('poll'), { token, device_id: deviceId, nonce: 'n1', last_command_id: 0, ...fields });
    },
    ack(fields = {}) {
      return postForm(api('ack'), { token, device_id: deviceId, result: 'executed', message: '', ...fields });
    },
    keys(fields = {}) {
      return postForm(api('keys'), { token, device_id: deviceId, nonce: 'k1', ...fields });
    },
  };
}

// ============================ DASHBOARD-SITZUNG =============================
// Anmelden und die JSON-API benutzen (wie ein Browser, ohne Browser).
export async function session(backend, account = ADMIN) {
  const base = backend.base;
  let cookie = '';
  let csrf = '';
  const api = (action, params = {}) => `${base}/dashboard_api.php?` + new URLSearchParams({ action, ...params });
  const login = await postForm(`${base}/index.php`, { action: 'login', user: account.user, password: account.password });
  for (const c of login.headers.getSetCookie?.() ?? []) {
    const pair = c.split(';')[0];
    if (pair.startsWith('boss925_dashboard=')) cookie = pair;
  }
  if (!cookie) throw new Error(`Anmeldung gescheitert (HTTP ${login.status})`);
  // Der HTTP-Code steht als "http" neben den Feldern der Antwort (die Antwort
  // von action=status hat selbst ein Feld "status").
  const json = (res) => {
    try { return { ...JSON.parse(res.text), http: res.status }; } catch { return { http: res.status, raw: res.text }; }
  };
  const refreshCsrf = async () => {
    csrf = json(await request(api('status'), { headers: { cookie } })).csrf || '';
  };
  await refreshCsrf();
  const self = {
    cookie,
    async get(action, params = {}) {
      return json(await request(api(action, params), { headers: { cookie } }));
    },
    async post(fields) {
      const res = await postForm(`${base}/dashboard_api.php`, fields, { cookie, 'x-csrf-token': csrf });
      const body = json(res);
      if (res.status === 403 && body.code === 'csrf') await refreshCsrf();
      return body;
    },
    // Empfänger anlegen und seinen Eintrag (mit id, mute_token ...) liefern.
    async addKey(label, key, extra = {}) {
      const added = await self.post({ action: 'keys_add', label, key, ...extra });
      if (!added.ok) throw new Error('keys_add gescheitert: ' + JSON.stringify(added));
      const list = await self.get('keys_list');
      return list.keys.filter((k) => k.label === label).sort((a, b) => b.id - a.id)[0];
    },
    async keys() {
      const list = await self.get('keys_list');
      if (!list.ok) throw new Error('keys_list gescheitert: ' + JSON.stringify(list));
      return list;
    },
    // Einträge des Verlaufs (neueste zuerst), so wie das Dashboard sie bekommt.
    async log(filter = 'all', limit = 100) {
      const res = await self.get('log_view', { limit, filter });
      if (!res.ok) throw new Error('log_view gescheitert: ' + JSON.stringify(res));
      return res.entries;
    },
  };
  return self;
}

// Wochentag (1 = Montag ... 7 = Sonntag) und Stunde in der Zeitzone des Servers.
export function serverClock(timeZone = 'Europe/Berlin') {
  const parts = new Intl.DateTimeFormat('en-GB', { timeZone, weekday: 'short', hour: 'numeric', hourCycle: 'h23' })
    .formatToParts(new Date());
  const weekday = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'].indexOf(parts.find((p) => p.type === 'weekday').value) + 1;
  return { weekday, hour: Number(parts.find((p) => p.type === 'hour').value) };
}
