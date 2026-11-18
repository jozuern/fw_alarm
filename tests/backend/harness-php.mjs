// Treiber für den PHP-Server (FW_BACKEND=php, Standard): php -S auf einem
// freien Port, die Laufzeitdateien von PHP im Datenordner. Vertrag und
// gemeinsame Prüfungen stehen in harness.mjs; die Tests importieren alles
// von dort, nie von hier.
import { execFile, execFileSync, spawn } from 'node:child_process';
import {
  appendFileSync, cpSync, existsSync, mkdirSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync,
} from 'node:fs';
import { dirname, join, relative, sep } from 'node:path';
import {
  ADMIN, MACHINE_TOKEN, READER, REPO, STATUS_KEY, TEST_PASSWORD_HASH, freePort, now, sha256, sleep, stateError,
} from './harness.mjs';

const DATA_FILE_GUARD = '<?php http_response_code(404); exit; ?>\n';

// ============================ DATENORDNER (PHP) =============================
// Laufzeitdateien wie die Anwendung sie schreibt: <name>.php, erste Zeile =
// Guard, danach JSON (bzw. beim Verlauf eine JSON-Zeile pro Eintrag). name ist
// der logische Name aus CLAUDE.md, z.B. 'latest_status.json', 'commands.log'.
// Als backend.data unter tests/backend/ nur für php-*.test.mjs (Regeln, die
// allein PHPs Ablage betreffen); alle anderen Tests nehmen backend.state /
// backend.storage. (Die Chrome-Szenarien unter tests/dashboard/e2e/ nutzen
// es noch, bis sie umgestellt sind.)
export function dataAccess(dataDir) {
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
    // Hängt Einträge roh an den Verlauf an (eine JSON-Zeile pro Eintrag, ohne
    // eigenes log_at) - so entsteht ein Verlauf beliebiger Länge ohne die
    // Aktionen dafür. Eine neue Datei bekommt zuerst die Guard-Zeile.
    // name = 'commands.log.1' füllt die rotierte (ältere) Datei.
    appendLog(entries, name = 'commands.log') {
      const lines = entries.map((entry) => `${JSON.stringify(entry)}\n`).join('');
      appendFileSync(file(name), (existsSync(file(name)) ? '' : DATA_FILE_GUARD) + lines);
    },
  };
  return api;
}

// PHP: Zustand liegt in den Laufzeitdateien von dataAccess().
//
// storage.fail(what) legt nicht leere Ordner an die Stelle dieser Dateien
// (das Ersetzen per rename bzw. das Öffnen der Lock-Datei scheitert dann);
// geprüft gegen with_lock() (fatal: 500 mit dem Satz "Datenordner nicht
// beschreibbar"), with_lock_soft() (still, der Ausweichwert) und
// read_alarm_keys_state():
//   selftest  health_probe.json - api/health.php scheitert im Schritt write,
//             der Wächter meldet "WEB_FAIL (n) Datenordner nicht beschreibbar".
//   durable   erst die Liste nach alarm_keys.json.bak kopieren (so bleibt sie
//             über den Rückgriff lesbar, wie unter Go aus dem Speicher), dann
//             alarm_keys.json, keys.lock, command.lock, notify.lock,
//             settings.lock - Änderungen an Empfängern, Demo-Modus,
//             Befehlen, Meldungs- und App-Einstellungen scheitern (Dashboard:
//             500 mit code storage), der Poll liefert keinen Befehl aus, die
//             Box bekommt die Liste weiter.
//   history   commands.log und log.lock - kein Verlaufseintrag wird
//             geschrieben, die Aktion selbst läuft weiter.
//   volatile  latest_status.json und watcher_seen.json - der Status-Push
//             antwortet 500 WRITE_FAILED (anders als Go), alive.php stored=0.
// repair(what) entfernt die Ordner und schreibt zurück, was vorher dort lag
// (bei durable auch die alte alarm_keys.json.bak).
const PHP_STORAGE = {
  selftest: ['health_probe.json'],
  durable: ['alarm_keys.json', 'keys.lock', 'command.lock', 'notify.lock', 'settings.lock'],
  history: ['commands.log', 'log.lock'],
  volatile: ['latest_status.json', 'watcher_seen.json'],
};

function phpBench(dataDir) {
  const files = dataAccess(dataDir);
  const failed = new Set();
  const saved = new Map();   // name -> Inhalt vor fail() (null = gab es nicht)
  const isFile = (name) => {
    try { return statSync(files.path(name)).isFile(); } catch { return false; }
  };
  // Inhalt einer Laufzeitdatei, null wenn sie fehlt (oder gerade ein Ordner ist).
  const read = (name) => (isFile(name) ? files.read(name) : null);
  const ago = (seconds) => now() - seconds;
  // Vorhandene Datei ändern; fehlt sie, ist der Aufruf ein Fehler im Test.
  const change = (helper, name, missing, fn) => {
    const value = read(name);
    if (value === null) throw stateError(helper, missing);
    fn(value);
    files.write(name, value);
  };
  const recipientsState = () => {
    // Wie read_alarm_keys_state(): Fehlt die Liste (oder liegt dort während
    // storage.fail('durable') ein Ordner), gilt die Sicherung.
    for (const name of ['alarm_keys.json', 'alarm_keys.json.bak']) {
      const value = read(name);
      if (value && (value.keys === undefined || Array.isArray(value.keys))) return value;
    }
    return { version: 0, keys: [] };
  };
  const logEntries = (name) => (isFile(name)
    ? readFileSync(files.path(name), 'utf8').split('\n')
      .filter((line) => line.trim().startsWith('{')).map((line) => JSON.parse(line))
    : []);
  const watchState = () => read('watchdog_state.json') ?? {};

  // Die Eingaben sind hier schon geprüft (STATE_INPUT, STORAGE_INPUT).
  const state = {
    ageBoxStatus(seconds) {
      change('ageBoxStatus', 'latest_status.json', 'noch kein Status der Box gespeichert',
        (d) => { d.seen_at = ago(seconds); });
    },
    ageDevice(deviceId, seconds) {
      change('ageDevice', 'devices_seen.json', 'noch kein Board gesehen', (all) => {
        if (!all[deviceId]) throw stateError('ageDevice', `Board "${deviceId}" hat sich nie gemeldet`);
        all[deviceId].seen_at = ago(seconds);
      });
    },
    ageMute(idOrAll, seconds) {
      change('ageMute', 'alarm_keys.json', 'keine Empfängerliste gespeichert', (list) => {
        const keys = (list.keys ?? []).filter((k) => k && typeof k === 'object');
        const targets = idOrAll === 'all' ? keys.filter((k) => k.muted) : keys.filter((k) => k.id === idOrAll);
        if (idOrAll !== 'all' && targets.length === 0) throw stateError('ageMute', `kein Empfänger mit id ${idOrAll}`);
        if (targets.length === 0) throw stateError('ageMute', 'niemand ist stumm');
        for (const k of targets) {
          if (!k.muted) throw stateError('ageMute', `Empfänger ${k.id} ist nicht stumm`);
          k.muted_at = ago(seconds);
        }
      });
    },
    ageDemo(seconds) {
      change('ageDemo', 'demo_mode.json', 'noch kein Demo-Zustand gespeichert', (d) => { d.changed_at = ago(seconds); });
    },
    ageCommand(which, seconds) {
      change('ageCommand', 'command_state.json', 'kein Befehl gespeichert', (d) => {
        if (!d.current) throw stateError('ageCommand', 'kein laufender Befehl');
        d.current[`${which}_at`] = ago(seconds);
      });
    },
    ageWatcher(seconds) {
      change('ageWatcher', 'watcher_seen.json', 'der Außen-Wächter hat sich nie gemeldet', (d) => { d.seen_at = ago(seconds); });
    },
    ageMark(name, seconds) {
      files.write('watchdog_state.json', { ...watchState(), [name]: ago(seconds) });
    },
    ageRetry() {
      throw stateError('ageRetry', 'nur unter Go - PHP versucht eine gescheiterte Meldung beim nächsten Lauf erneut, eine Sperrfrist gibt es dort nicht');
    },
    ageMuteThrottle(seconds) {
      change('ageMuteThrottle', 'mute_throttle.json', 'keine Fehlversuche gezählt', (all) => {
        if (Object.keys(all).length === 0) throw stateError('ageMuteThrottle', 'keine Fehlversuche gezählt');
        for (const who of Object.keys(all)) all[who].first_at = ago(seconds);
      });
    },
    ageLog(seconds, match) {
      // Erst beide Dateien ausrechnen, dann schreiben: Wirft ein Eintrag,
      // bleibt alles, wie es war.
      const changed = [];
      for (const name of ['commands.log.1', 'commands.log']) {
        if (!isFile(name)) continue;
        const path = files.path(name);
        // Zeile für Zeile wie gespeichert; nur passende Einträge ändern sich.
        const lines = readFileSync(path, 'utf8').split('\n').map((line) => {
          if (!line.startsWith('{')) return line;
          const entry = JSON.parse(line);
          if (!match(entry)) return line;
          if (typeof entry.log_at !== 'number' || !Number.isFinite(entry.log_at)) {
            throw stateError('ageLog', `passender Eintrag ohne log_at als Zahl: ${line}`);
          }
          entry.log_at -= seconds;
          return JSON.stringify(entry);
        });
        changed.push([path, lines.join('\n')]);
      }
      for (const [path, text] of changed) writeFileSync(path, text);
    },
    ageSession() {
      throw stateError('ageSession', 'nicht unter PHP - die Sitzungen von PHP liegen außerhalb des Prüfstands');
    },

    setWatcher({ seenAgo, barkFailedAt }) {
      files.write('watcher_seen.json', { seen_at: ago(seenAgo), bark_failed_at: barkFailedAt });
    },
    setMarks(marks) {
      const next = watchState();
      for (const [name, value] of Object.entries(marks)) {
        if (value === null) delete next[name]; else next[name] = value;
      }
      files.write('watchdog_state.json', next);
    },
    setCommand(patch) {
      change('setCommand', 'command_state.json', 'kein Befehl gespeichert', (d) => {
        if (!d.current) throw stateError('setCommand', 'kein laufender Befehl');
        Object.assign(d.current, patch);
      });
    },
    seedLog(entries, { rotated }) {
      files.appendLog(entries, rotated ? 'commands.log.1' : 'commands.log');
    },
    seedRecipients(entries, { version, nextId }) {
      files.write('alarm_keys.json', { next_id: nextId, version, keys: entries });
    },

    boxStatus() {
      const stored = read('latest_status.json');
      if (stored === null) return null;
      const { remote_addr: _remoteAddr, ...fields } = stored;   // die Absender-Adresse gehört nicht dazu
      return fields;
    },
    recipients() {
      const list = recipientsState();
      return { version: Number(list.version ?? 0), keys: list.keys ?? [] };
    },
    recipient(id) {
      return (recipientsState().keys ?? []).find((k) => k && k.id === id) ?? null;
    },
    listVersion() {
      return Number(recipientsState().version ?? 0);
    },
    demo() {
      return read('demo_mode.json') ?? { enabled: false };
    },
    command() {
      return read('command_state.json')?.current ?? null;
    },
    watcher() {
      const seen = read('watcher_seen.json');
      return seen === null ? null : { seen_at: seen.seen_at, bark_failed_at: seen.bark_failed_at };
    },
    marks() {
      return watchState();
    },
    log() {
      return [...logEntries('commands.log.1'), ...logEntries('commands.log')];
    },
    muteFailures() {
      return Object.values(read('mute_throttle.json') ?? {}).reduce((sum, who) => sum + Number(who?.count ?? 0), 0);
    },
    notifyStored() {
      const stored = read('notify_settings.json') ?? {};
      return { global: stored.global ?? {}, types: stored.types ?? {} };
    },
  };

  const remember = (name) => {
    if (!saved.has(name)) saved.set(name, isFile(name) ? readFileSync(files.path(name)) : null);
  };
  const restore = (name) => {
    const before = saved.get(name);
    saved.delete(name);
    if (before) writeFileSync(files.path(name), before);
    else rmSync(files.path(name), { recursive: true, force: true });
  };
  const storage = {
    fail(what) {
      const names = PHP_STORAGE[what];
      if (failed.has(what)) return;
      if (what === 'durable' && isFile('alarm_keys.json')) {
        remember('alarm_keys.json.bak');
        writeFileSync(files.path('alarm_keys.json.bak'), readFileSync(files.path('alarm_keys.json')));
      }
      for (const name of names) {
        remember(name);
        files.block(name);
      }
      failed.add(what);
    },
    repair(what) {
      const names = PHP_STORAGE[what];
      if (!failed.has(what)) return;
      for (const name of names) {
        files.unblock(name);
        restore(name);
      }
      if (what === 'durable' && saved.has('alarm_keys.json.bak')) restore('alarm_keys.json.bak');
      failed.delete(what);
    },
  };

  return {
    state,
    storage,
    // Nach reset(): Der Datenordner ist leer, nichts ist mehr gestört.
    forget() {
      failed.clear();
      saved.clear();
    },
  };
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

export async function startPhp(root, bark, options = {}) {
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

  const port = options.port || await freePort();
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
  let child = null;
  let exited = null;
  let alive = false;
  const keep = (chunk) => { log = (log + chunk).slice(-200000); };
  const killOnExit = () => { try { child?.kill(); } catch { /* schon beendet */ } };
  process.on('exit', killOnExit);

  // Startet den Server (auf demselben Port) und wartet, bis er antwortet.
  const startServer = async () => {
    if (alive) return;
    const mine = spawn(bin, [...flags, '-S', `127.0.0.1:${port}`, '-t', site],
      { cwd: site, stdio: ['ignore', 'pipe', 'pipe'], windowsHide: true });
    child = mine;
    alive = true;
    mine.stdout.on('data', keep);
    mine.stderr.on('data', keep);
    exited = new Promise((resolve) => mine.on('exit', () => { alive = false; resolve(); }));
    let up = false;
    for (let i = 0; i < 100 && !up; i++) {
      try {
        await fetch(`${base}/manifest.json`, { signal: AbortSignal.timeout(1000) });
        up = true;
      } catch {
        if (!alive) break;
        await sleep(100);
      }
    }
    if (!up) {
      killOnExit();
      throw new Error(`PHP-Server startet nicht (Port ${port}):\n${log}`);
    }
  };
  // Hält den Server an; der Datenordner bleibt unberührt.
  const stopServer = async () => {
    if (!alive) return;
    killOnExit();
    await exited;
  };
  await startServer();
  const bench = phpBench(dataDir);

  return {
    kind: 'php',
    base,
    dataDir,
    state: bench.state,
    storage: bench.storage,
    words: { configFile: 'config.php' },
    serverLog: () => log,
    startServer,
    stopServer,
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
      bench.forget();
      writeConfig({});
    },
    async stop() {
      process.off('exit', killOnExit);
      await stopServer();
    },
  };
}
