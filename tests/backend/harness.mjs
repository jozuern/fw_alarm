// Prüfstand für die Server-Seite (Box-Protokoll, Wächter, Kurzbefehl-Link,
// Alarm-Versand, Anmeldung und Dashboard-API): startet pro Testdatei ein
// EIGENES Dashboard mit leerem Datenordner und eine Bark-Attrappe im
// Testprozess - eigene Ports, eigener Temp-Ordner, nichts bleibt liegen.
// Die Tests sprechen HTTP und sagen über backend.state / backend.storage,
// WAS sie brauchen ("der letzte Status ist 200 s alt", "Sichern scheitert");
// WO das liegt, weiß allein der Treiber hier (FW_BACKEND: heute PHP, später
// Go). backend.data (die Dateien von PHP) fasst unter tests/backend/ nur
// php-*.test.mjs an - die Chrome-Szenarien (tests/dashboard/e2e/) noch, bis
// sie auf dieselben Helfer umgestellt sind.
//
// Aufruf: node --test "tests/backend/*.test.mjs"
import { execFile, execFileSync, spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  appendFileSync, cpSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync,
  statSync, writeFileSync,
} from 'node:fs';
import { createServer } from 'node:http';
import { createServer as createTcpServer } from 'node:net';
import { tmpdir } from 'node:os';
import { dirname, join, relative, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const REPO = join(here, '..', '..');

// Welche Server-Fassung geprüft wird. Eine Datei, die nur für eine Fassung
// gilt, hängt alles an EIN describe:
//   describe('…', { skip: BACKEND_KIND !== 'php' && 'nur PHP' }, () => { … });
// (backend gibt es beim Einlesen der Datei noch nicht.)
export const BACKEND_KIND = process.env.FW_BACKEND || 'php';

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

// ---- Zugänge des Prüfstands -------------------------------------------------
export const MACHINE_TOKEN = 'box-token';
export const WATCHER_TOKEN = 'watcher-token';
export const STATUS_KEY = 'statuskey0001';
export const ADMIN = { user: 'admin', password: 'test' };
export const READER = { user: 'leser', password: 'test' };
// bcrypt-Hash von "test" (cost 4, damit die Anmeldung im Test nichts kostet).
const TEST_PASSWORD_HASH = '$2y$04$r60I68m5mVq98EDvnpGXGeS1SZtBd2wWlr0dy0MuX73YPDZ27VzZy';
// bcrypt-Hash von "anderes" (cost 4): ein geändertes Passwort in der Konfiguration.
export const OTHER_PASSWORD_HASH = '$2y$04$arQ57ZxGZu0qqkyhgNiBc.Ri6mtxzvUCvCwP8PvCH/TfBrFX9abKO';
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

// ============================ DATENORDNER (PHP) =============================
// Laufzeitdateien wie die Anwendung sie schreibt: <name>.php, erste Zeile =
// Guard, danach JSON (bzw. beim Verlauf eine JSON-Zeile pro Eintrag). name ist
// der logische Name aus CLAUDE.md, z.B. 'latest_status.json', 'commands.log'.
// Als backend.data unter tests/backend/ nur für php-*.test.mjs (Regeln, die
// allein PHPs Ablage betreffen); alle anderen Tests nehmen backend.state /
// backend.storage. (Die Chrome-Szenarien unter tests/dashboard/e2e/ nutzen
// es noch, bis sie umgestellt sind.)
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

// ============================ ABSICHTS-HELFER ===============================
// Vertrag für JEDEN Treiber (PHP hier, Go später): backend.state,
// backend.storage, backend.words. Alle Helfer sind async. Sekunden heißen
// "so lange her"; eine negative Zahl liegt in der Zukunft.
//
// backend.state
//   Zeit verschieben (der Eintrag muss schon da sein, sonst Fehler - außer
//   bei ageMark):
//     ageBoxStatus(s)          letzter Status der Box kam vor s Sekunden an
//     ageDevice(deviceId, s)   dieses Board hat sich zuletzt vor s gemeldet
//     ageMute(id|'all', s)     Stummschaltung begann vor s ('all' = alle, die gerade stumm sind)
//     ageDemo(s)               Demo-Zustand zuletzt geändert vor s
//     ageCommand(which, s)     laufender Befehl: which 'created' | 'delivered' vor s
//     ageWatcher(s)            Außen-Wächter zuletzt gesehen vor s (bark_failed_at bleibt)
//     ageMark(name, s)         Merker des Wächters (WATCH_MARKS) = vor s
//     ageRetry(kind, s)        nur Go: Sperrfrist nach gescheitertem Versand
//     ageMuteThrottle(s)       gezählte Fehlversuche am Kurzbefehl-Link begannen vor s
//     ageLog(s, match)         passende Verlaufs-Einträge um s älter (log_at -= s) - als
//                              einziger RELATIV ("um s älter machen"), die übrigen age*
//                              setzen ein absolutes "vor s". match ist eine Bedingung
//                              (eintrag) => boolean über den gespeicherten Eintrag, ohne
//                              Angabe alle Einträge. Wirft, wenn ein passender Eintrag
//                              kein log_at als Zahl hat (dann ändert sich nichts).
//     ageSession(cookie, s)    nur Go: diese Sitzung ist seit s untätig
//   Herstellen:
//     setWatcher({ seenAgo, barkFailedAt = 0 })   Außen-Wächter zuletzt gesehen vor seenAgo
//     setMarks({ name: wert|null })                mehrere Merker in einem Zug; null löscht
//     setCommand(patch)                            Felder des laufenden Befehls (COMMAND_FIELDS);
//                                                  wirft ohne laufenden Befehl
//     seedLog(entries, { rotated = false })        Einträge wie gegeben (ohne uid, ohne eigenes
//                                                  log_at); rotated = die ältere Datei
//     seedRecipients(entries, { version = 1, nextId = größte id + 1 })
//                                                  Empfängerliste ersetzen, nur gültige Einträge
//   Lesen - wie gespeichert, nicht wie angezeigt:
//     boxStatus()      gepushte Felder + seen_at + via_bridge ('0'|'1'), null = nie
//     recipients()     { version, keys: [...] }
//     recipient(id)    Eintrag oder null
//     listVersion()    Version der Empfängerliste
//     demo()           Demo-Zustand ({ enabled: false } ohne Eintrag)
//     command()        laufender Befehl oder null
//     watcher()        { seen_at, bark_failed_at } oder null
//     marks()          Merker des Wächters als Objekt
//     log()            alle Verlaufs-Einträge roh, älteste zuerst
//     muteFailures()   Summe der gezählten Fehlversuche am Kurzbefehl-Link
//     notifyStored()   { global, types }: nur ausdrücklich Gespeichertes
// backend.storage
//   fail(what) / repair(what), what: 'selftest' | 'durable' | 'history' |
//   'volatile' - das Sichern dieses Teils scheitert bzw. geht wieder;
//   repair stellt den Stand von vor fail() her. Ein zweites fail(what) ohne
//   repair dazwischen tut nichts, ebenso repair(what) ohne vorheriges fail.
// backend.words
//   configFile: Name der Konfigurationsdatei in Texten ('config.php' | 'config.toml').
//
// Die Eingaben prüft startBackend() für JEDEN Treiber gleich (STATE_INPUT,
// STORAGE_INPUT unten): Dieselbe falsche Eingabe wirft unter PHP und Go
// denselben Fehler, der Treiber bekommt nur geprüfte Werte. Und jeder Helfer
// gibt zuerst die Kontrolle ab, bevor der Treiber etwas anfasst: Ein Aufruf
// ohne await fällt so auch unter PHP auf (unter Go liefe er in einen
// Neustart des Servers hinein).
const yieldFirst = () => new Promise((resolve) => setImmediate(resolve));

// Merker des Wächters - die Namen aus watchdog_state.json von PHP, die auch
// die Go-Fassung führt (Plan: "File formats", runtime.json "watch").
const WATCH_MARKS = new Set([
  'last_run_at', 'offline_notified', 'offline_since', 'offline_seen_at', 'offline_reminded_at',
  'offline_push_pending', 'web_fail_count', 'web_fail_since', 'web_fail_notified_at', 'web_push_pending',
  'watcher_watch_since', 'watcher_silent_seen_at', 'watcher_notified_at', 'watcher_push_pending',
  'demo_reminded_at', 'demo_reminded_for', 'mute_reminded_at', 'permanent_mute_reminded_at',
  'health_bad_runs', 'health_notified_at', 'weekly_report_at', 'last_send_ok_at', 'last_send_failed_at',
]);
// Felder des laufenden Befehls (wie api/poll.php, api/ack.php und
// dashboard_api.php sie schreiben).
const COMMAND_FIELDS = new Set([
  'id', 'type', 'state', 'created_at', 'created_by', 'demo_changed_at', 'delivered_at', 'delivered_to',
  'acked_at', 'acked_by', 'ack_result', 'ack_message', 'cancelled_at', 'cancelled_by', 'cancel_reason',
  'expired_at',
]);
// Felder eines Empfängers, die sich herstellen lassen.
const RECIPIENT_FIELDS = new Set([
  'id', 'key', 'label', 'phone', 'added_at', 'muted', 'muted_at', 'mute_volume', 'mute_permanent', 'mute_token',
]);
const BARK_KEY = /^[A-Za-z0-9_-]{5,64}$/;   // wie valid_bark_key() und die Firmware
const MAX_RECIPIENTS = 10;
const STORAGE_PARTS = ['selftest', 'durable', 'history', 'volatile'];

function stateError(helper, why) {
  return new Error(`backend.state.${helper}: ${why}`);
}

function checkMark(helper, name) {
  if (!WATCH_MARKS.has(name)) throw stateError(helper, `unbekannter Merker "${name}"`);
}

function checkSeconds(helper, seconds) {
  if (!Number.isFinite(seconds)) throw stateError(helper, `Sekunden müssen eine Zahl sein, nicht ${seconds}`);
}

// Nur gültige Einträge (die Go-Fassung kann ungültige gar nicht speichern):
// höchstens 10, jede id und jeder Key einmal, bekannte Felder. Liefert die
// größte id (0 ohne Einträge).
function checkRecipients(entries) {
  if (!Array.isArray(entries) || entries.length > MAX_RECIPIENTS) {
    throw stateError('seedRecipients', `höchstens ${MAX_RECIPIENTS} Einträge als Liste`);
  }
  const ids = new Set();
  const keys = new Set();
  for (const entry of entries) {
    const where = JSON.stringify(entry);
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) throw stateError('seedRecipients', `kein Eintrag: ${where}`);
    const unknown = Object.keys(entry).filter((name) => !RECIPIENT_FIELDS.has(name));
    if (unknown.length) throw stateError('seedRecipients', `unbekannte Felder ${unknown.join(', ')}: ${where}`);
    if (!Number.isInteger(entry.id) || entry.id < 1 || ids.has(entry.id)) throw stateError('seedRecipients', `id fehlt, ungültig oder doppelt: ${where}`);
    if (typeof entry.key !== 'string' || !BARK_KEY.test(entry.key) || keys.has(entry.key)) throw stateError('seedRecipients', `Key ungültig oder doppelt: ${where}`);
    if (typeof entry.label !== 'string') throw stateError('seedRecipients', `label fehlt: ${where}`);
    ids.add(entry.id);
    keys.add(entry.key);
  }
  return Math.max(0, ...ids);
}

// Eingaben jedes Helfers: prüfen (wirft mit "backend.state.<name>: …") und
// die Argumente liefern, mit denen der Treiber aufgerufen wird - Standards
// schon eingesetzt. Ein Helfer, der hier fehlt, gehört nicht zum Vertrag.
const anything = (...args) => args;
const STATE_INPUT = {
  ageBoxStatus: (s) => { checkSeconds('ageBoxStatus', s); return [s]; },
  ageDevice: (deviceId, s) => { checkSeconds('ageDevice', s); return [deviceId, s]; },
  ageMute: (idOrAll, s) => { checkSeconds('ageMute', s); return [idOrAll, s]; },
  ageDemo: (s) => { checkSeconds('ageDemo', s); return [s]; },
  ageCommand: (which, s) => {
    checkSeconds('ageCommand', s);
    if (!['created', 'delivered'].includes(which)) throw stateError('ageCommand', `which ist 'created' oder 'delivered', nicht "${which}"`);
    return [which, s];
  },
  ageWatcher: (s) => { checkSeconds('ageWatcher', s); return [s]; },
  ageMark: (name, s) => { checkMark('ageMark', name); checkSeconds('ageMark', s); return [name, s]; },
  ageRetry: (kind, s) => { checkSeconds('ageRetry', s); return [kind, s]; },
  ageMuteThrottle: (s) => { checkSeconds('ageMuteThrottle', s); return [s]; },
  ageLog: (s, match = () => true) => {
    checkSeconds('ageLog', s);
    if (typeof match !== 'function') throw stateError('ageLog', 'match ist eine Bedingung (eintrag) => boolean');
    return [s, match];
  },
  ageSession: (cookie, s) => { checkSeconds('ageSession', s); return [cookie, s]; },

  setWatcher: ({ seenAgo, barkFailedAt = 0 } = {}) => {
    checkSeconds('setWatcher', seenAgo);
    return [{ seenAgo, barkFailedAt }];
  },
  setMarks: (marks) => {
    if (!marks || typeof marks !== 'object' || Array.isArray(marks)) throw stateError('setMarks', 'Merker als Objekt { name: wert|null }');
    for (const name of Object.keys(marks)) checkMark('setMarks', name);
    return [marks];
  },
  setCommand: (patch) => {
    if (!patch || typeof patch !== 'object' || Array.isArray(patch)) throw stateError('setCommand', 'Felder als Objekt');
    const unknown = Object.keys(patch).filter((name) => !COMMAND_FIELDS.has(name));
    if (unknown.length) throw stateError('setCommand', `unbekannte Felder ${unknown.join(', ')}`);
    return [patch];
  },
  seedLog: (entries, { rotated = false } = {}) => {
    if (!Array.isArray(entries) || entries.some((e) => !e || typeof e !== 'object' || Array.isArray(e))) {
      throw stateError('seedLog', 'Einträge als Liste von Objekten');
    }
    return [entries, { rotated: Boolean(rotated) }];
  },
  seedRecipients: (entries, { version = 1, nextId } = {}) => {
    const maxId = checkRecipients(entries);
    const next = nextId ?? maxId + 1;
    if (!Number.isInteger(next) || next <= maxId) {
      throw stateError('seedRecipients', `nextId ${next} muss größer sein als jede id`);
    }
    if (!Number.isInteger(version) || version < 0) throw stateError('seedRecipients', `Version ${version} ungültig`);
    return [entries, { version, nextId: next }];
  },

  boxStatus: anything,
  recipients: anything,
  recipient: anything,
  listVersion: anything,
  demo: anything,
  command: anything,
  watcher: anything,
  marks: anything,
  log: anything,
  muteFailures: anything,
  notifyStored: anything,
};
const storagePart = (helper) => (what) => {
  if (!STORAGE_PARTS.includes(what)) throw new Error(`backend.storage.${helper}: unbekannter Teil "${what}" (${STORAGE_PARTS.join(', ')})`);
  return [what];
};
const STORAGE_INPUT = { fail: storagePart('fail'), repair: storagePart('repair') };

// Die Helfer eines Treibers hinter den gemeinsamen Prüfungen (siehe oben).
// Fehlt dem Treiber ein Helfer des Vertrags, scheitert schon der Start.
function contractHelpers(kind, part, impl, inputs) {
  const missing = Object.keys(inputs).filter((name) => typeof impl?.[name] !== 'function');
  if (missing.length) throw new Error(`Treiber "${kind}": backend.${part} ohne ${missing.join(', ')}`);
  return Object.fromEntries(Object.entries(inputs).map(([name, input]) => [name, async (...args) => {
    await yieldFirst();
    return impl[name](...input(...args));
  }]));
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

async function startPhp(root, bark, options = {}) {
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

// ============================ START =========================================
// options.config: Abweichungen von der Standard-Konfiguration ab dem Start.
// options.port: fester Port statt eines freien (Handarbeit, serve.mjs).
// Treiber-Vertrag: base, dataDir, configure, runWatchdog, reset, stop,
// stopServer()/startServer() (Server anhalten/wieder starten: gleicher Port,
// Daten unberührt) sowie state, storage und words (siehe ABSICHTS-HELFER).
export async function startBackend(options = {}) {
  const kind = BACKEND_KIND;
  const drivers = { php: startPhp };
  if (!drivers[kind]) {
    throw new Error(`Unbekanntes FW_BACKEND "${kind}" (vorhanden: ${Object.keys(drivers).join(', ')})`);
  }
  const root = mkdtempSync(join(tmpdir(), 'fw-backend-'));
  const bark = await startBarkMock();
  let driver;
  let state;
  let storage;
  try {
    driver = await drivers[kind](root, bark, options);
    // Dieselben Prüfungen der Eingaben für jeden Treiber (ABSICHTS-HELFER).
    try {
      state = contractHelpers(kind, 'state', driver.state, STATE_INPUT);
      storage = contractHelpers(kind, 'storage', driver.storage, STORAGE_INPUT);
    } catch (error) {
      // Scheitert auch das Anhalten, zählt trotzdem der eigentliche Fehler.
      try { await driver.stop(); } catch { /* bewusst verschluckt: der Vertragsfehler sagt, was los ist */ }
      throw error;
    }
  } catch (error) {
    await bark.stop();
    rmSync(root, { recursive: true, force: true, maxRetries: 10, retryDelay: 100 });
    throw error;
  }
  if (options.config) await driver.configure(options.config);
  const backend = {
    ...driver,
    state,
    storage,
    root,
    bark,
    // Die Dateien von PHP - unter tests/backend/ nur für php-*.test.mjs.
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
// Das Anmeldeformular abschicken (POST /index.php) - die Antwort, wie sie ist
// (Weiterleitung, Cookie, Anmeldeseite mit Fehler).
export function rawLogin(backend, user, password, headers = {}) {
  return postForm(`${backend.base}/index.php`, { action: 'login', user, password }, headers);
}

// Anmelden und die JSON-API benutzen (wie ein Browser, ohne Browser).
export async function session(backend, account = ADMIN) {
  const base = backend.base;
  let cookie = '';
  let csrf = '';
  const api = (action, params = {}) => `${base}/dashboard_api.php?` + new URLSearchParams({ action, ...params });
  const login = await rawLogin(backend, account.user, account.password);
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
