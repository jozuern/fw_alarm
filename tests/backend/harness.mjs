// Prüfstand für die Server-Seite (Box-Protokoll, Wächter, Kurzbefehl-Link,
// Alarm-Versand, Anmeldung und Dashboard-API): startet pro Testdatei ein
// EIGENES Dashboard mit leerem Datenordner und eine Bark-Attrappe im
// Testprozess - eigene Ports, eigener Temp-Ordner, nichts bleibt liegen.
// Die Tests sprechen HTTP und sagen über backend.state / backend.storage,
// WAS sie brauchen ("der letzte Status ist 200 s alt", "Sichern scheitert");
// WO das liegt, weiß allein der Treiber (FW_BACKEND): PHP in
// harness-php.mjs, Go in harness-go.mjs. Hier stehen der Prüfstand, die
// Bark-Attrappe, die Helfer für die Tests und die Prüfungen, die jeder
// Treiber teilt; die Tests importieren nur von hier. backend.data (die
// Dateien von PHP) fasst unter tests/backend/ nur php-*.test.mjs an - die
// Chrome-Szenarien (tests/dashboard/e2e/) noch, bis sie auf dieselben Helfer
// umgestellt sind; backend.files (der Datenordner von Go) nur go-*.test.mjs.
//
// Aufruf: node --test "tests/backend/*.test.mjs"            (PHP)
//         bash tools/test-go.sh [dateien…]                   (Go)
import { createHash } from 'node:crypto';
import { mkdtempSync, rmSync } from 'node:fs';
import { createServer } from 'node:http';
import { createServer as createTcpServer } from 'node:net';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
// Für die Treiber: das Repo (PHP kopiert nas_dashboard/ von dort).
export const REPO = join(here, '..', '..');

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
export const TEST_PASSWORD_HASH = '$2y$04$r60I68m5mVq98EDvnpGXGeS1SZtBd2wWlr0dy0MuX73YPDZ27VzZy';
// bcrypt-Hash von "anderes" (cost 4): ein geändertes Passwort in der Konfiguration.
export const OTHER_PASSWORD_HASH = '$2y$04$arQ57ZxGZu0qqkyhgNiBc.Ri6mtxzvUCvCwP8PvCH/TfBrFX9abKO';

// Ein freier Port, den das System vergibt (Bark-Attrappe, PHP-Server).
export function freePort() {
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

// ============================ ABSICHTS-HELFER ===============================
// Vertrag für JEDEN Treiber (PHP: harness-php.mjs, Go: harness-go.mjs):
// backend.state, backend.storage, backend.words. Alle Helfer sind async.
// Sekunden sind ganze Zahlen und heißen "so lange her"; eine negative Zahl
// liegt in der Zukunft. Die Namen der Felder sind überall die von PHP – wo
// Go anders speichert (recipients statt keys, keys_version statt version,
// die Felder der Box als Liste von Paaren), übersetzt der Go-Treiber; die
// Tests sehen unter beiden dieselben Formen.
//
// backend.state
//   Zeit verschieben (der Eintrag muss schon da sein, sonst Fehler - außer
//   bei ageMark und ageLog):
//     ageBoxStatus(s)          letzter Status der Box kam vor s Sekunden an
//     ageDevice(deviceId, s)   dieses Board hat sich zuletzt vor s gemeldet
//     ageMute(id|'all', s)     Stummschaltung begann vor s ('all' = alle, die gerade stumm sind)
//     ageDemo(s)               Demo-Zustand zuletzt geändert vor s (Fehler, solange er nie
//                              geändert wurde)
//     ageCommand(which, s)     laufender Befehl: which 'created' | 'delivered' vor s
//     ageWatcher(s)            Außen-Wächter zuletzt gesehen vor s (bark_failed_at bleibt)
//     ageMark(name, s)         Merker des Wächters (WATCH_MARKS) = vor s; nur Zeitpunkte
//                              (unter Go wirft ein Wahrheitswert oder Zähler)
//     ageRetry(kind, s)        nur Go (PHP wirft): Sperrfrist nach gescheitertem Versand einer
//                              Meldung dieser Art (ADMIN_KINDS: box_offline, nas_web,
//                              outside_watcher …) = frühester neuer Versuch vor s
//     ageMuteThrottle(s)       gezählte Fehlversuche am Kurzbefehl-Link begannen vor s
//     ageLog(s, match)         passende Verlaufs-Einträge um s älter (log_at -= s) - als
//                              einziger RELATIV ("um s älter machen"), die übrigen age*
//                              setzen ein absolutes "vor s". match ist eine Bedingung
//                              (eintrag) => boolean über den gespeicherten Eintrag, ohne
//                              Angabe alle Einträge. Passt keiner, ändert sich nichts.
//                              Wirft, wenn ein passender Eintrag kein log_at als Zahl hat
//                              (dann ändert sich auch sonst nichts).
//     ageSession(cookie, s)    nur Go (PHP wirft): die Sitzung mit diesem Cookie
//                              ('name=wert', wie es die Anmeldung setzt) ist seit s untätig
//   Herstellen:
//     setWatcher({ seenAgo, barkFailedAt = 0 })   Außen-Wächter zuletzt gesehen vor seenAgo
//     setMarks({ name: wert|null })                mehrere Merker in einem Zug; null löscht
//                                                  (unter Go: 0 bzw. false)
//     setCommand(patch)                            Felder des laufenden Befehls (COMMAND_FIELDS);
//                                                  wirft ohne laufenden Befehl
//     seedLog(entries, { rotated = false })        Einträge wie gegeben (ohne uid, ohne eigenes
//                                                  log_at); rotated = die ältere Datei
//     seedRecipients(entries, { version = 1, nextId = größte id + 1 })
//                                                  Empfängerliste ersetzen, nur gültige Einträge
//                                                  (unter Go bekommt ein Eintrag ohne mute_token
//                                                  einen neuen, fehlende Felder ihren Standard)
//   Lesen - wie gespeichert, nicht wie angezeigt:
//     boxStatus()      gepushte Felder als Objekt + seen_at (Zahl) + via_bridge ('0'|'1'),
//                      ohne remote_addr; null = nie
//     recipients()     { version, keys: [...] } - Einträge mit den Feldern von PHP
//                      (id, key, label, muted, muted_at …)
//     recipient(id)    Eintrag oder null
//     listVersion()    Version der Empfängerliste
//     demo()           Demo-Zustand ({ enabled: false } ohne Eintrag)
//     command()        laufender Befehl oder null (unter Go stehen alle COMMAND_FIELDS da,
//                      nie gesetzte als 0 bzw. '')
//     watcher()        { seen_at, bark_failed_at } oder null
//     marks()          Merker des Wächters als Objekt (unter Go alle, nie gesetzte als 0
//                      bzw. false - also nur auf Wahrheit prüfen, nicht auf undefined)
//     log()            alle Verlaufs-Einträge roh, älteste zuerst
//     muteFailures()   Summe der gezählten Fehlversuche am Kurzbefehl-Link
//     notifyStored()   { global, types }: nur ausdrücklich Gespeichertes
// backend.storage
//   fail(what) / repair(what), what: 'selftest' | 'durable' | 'history' |
//   'volatile' - das Sichern dieses Teils scheitert bzw. geht wieder;
//   repair stellt den Stand von vor fail() her. Ein zweites fail(what) ohne
//   repair dazwischen tut nichts, ebenso repair(what) ohne vorheriges fail.
//   Beides wirkt im laufenden Betrieb, ohne Neustart.
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

// Die festen Tabellen unten (Name → Art des Werts) gelten für jeden Treiber;
// der Go-Treiber baut aus ihnen auch die Felder, die er neu anlegt. Arten:
// 'time' (Unix-Sekunden), 'int' (ganze Zahl), 'bool', 'string'. Nur lesen.
//
// Merker des Wächters - die Namen aus watchdog_state.json von PHP, die auch
// die Go-Fassung führt (Plan: "File formats", runtime.json "watch").
export const MARK_KINDS = new Map([
  ['last_run_at', 'time'], ['offline_notified', 'bool'], ['offline_since', 'time'],
  ['offline_seen_at', 'time'], ['offline_reminded_at', 'time'], ['offline_push_pending', 'bool'],
  ['web_fail_count', 'int'], ['web_fail_since', 'time'], ['web_fail_notified_at', 'time'],
  ['web_push_pending', 'bool'], ['watcher_watch_since', 'time'], ['watcher_silent_seen_at', 'time'],
  ['watcher_notified_at', 'time'], ['watcher_push_pending', 'bool'], ['demo_reminded_at', 'time'],
  ['demo_reminded_for', 'time'], ['mute_reminded_at', 'time'], ['permanent_mute_reminded_at', 'time'],
  ['health_bad_runs', 'int'], ['health_notified_at', 'time'], ['weekly_report_at', 'time'],
  ['last_send_ok_at', 'time'], ['last_send_failed_at', 'time'],
]);
export const WATCH_MARKS = new Set(MARK_KINDS.keys());
// Arten der Meldungen an den Admin (PHP: admin_notify_types()) - die
// Schlüssel der Sperrfrist nach gescheitertem Versand (ageRetry).
export const ADMIN_KINDS = new Set([
  'box_offline', 'nas_web', 'outside_watcher', 'demo_reminder', 'mute_reminder', 'permanent_mute_reminder',
  'login_lockout', 'login_success', 'mute_changed', 'mute_expired', 'box_restart', 'box_health', 'weekly_report',
]);
// Felder des laufenden Befehls (wie api/poll.php, api/ack.php und
// dashboard_api.php sie schreiben).
export const COMMAND_FIELDS = new Map([
  ['id', 'int'], ['type', 'string'], ['state', 'string'], ['created_at', 'time'], ['created_by', 'string'],
  ['demo_changed_at', 'time'], ['delivered_at', 'time'], ['delivered_to', 'string'], ['acked_at', 'time'],
  ['acked_by', 'string'], ['ack_result', 'string'], ['ack_message', 'string'], ['cancelled_at', 'time'],
  ['cancelled_by', 'string'], ['cancel_reason', 'string'], ['expired_at', 'time'],
]);
// Felder eines Empfängers, die sich herstellen lassen.
export const RECIPIENT_FIELDS = new Map([
  ['id', 'int'], ['key', 'string'], ['label', 'string'], ['phone', 'string'], ['added_at', 'time'],
  ['muted', 'bool'], ['muted_at', 'time'], ['mute_volume', 'int'], ['mute_permanent', 'bool'],
  ['mute_token', 'string'],
]);
const BARK_KEY = /^[A-Za-z0-9_-]{5,64}$/;   // wie valid_bark_key() und die Firmware
const MUTE_TOKEN = /^[A-Za-z0-9_-]{8,32}$/; // wie valid_mute_token()
const MAX_RECIPIENTS = 10;
const STORAGE_PARTS = ['selftest', 'durable', 'history', 'volatile'];

// Fehler eines Helfers - auch die Treiber werfen damit.
export function stateError(helper, why) {
  return new Error(`backend.state.${helper}: ${why}`);
}

// Passt value zur Art aus einer der Tabellen oben?
export function fitsKind(kind, value) {
  switch (kind) {
    case 'time':
    case 'int': return Number.isSafeInteger(value);
    case 'bool': return typeof value === 'boolean';
    case 'string': return typeof value === 'string';
    default: return false;
  }
}

function checkMark(helper, name) {
  if (!WATCH_MARKS.has(name)) throw stateError(helper, `unbekannter Merker "${name}"`);
}

// Zeitpunkte sind ganze Unix-Sekunden (Go liest sie streng als ganze Zahl).
function checkSeconds(helper, seconds) {
  if (!Number.isSafeInteger(seconds)) throw stateError(helper, `Sekunden müssen eine ganze Zahl sein, nicht ${seconds}`);
}

// Nur gültige Einträge (die Go-Fassung kann ungültige gar nicht speichern):
// höchstens 10, jede id, jeder Key und jedes mute_token einmal, bekannte
// Felder mit Werten ihrer Art, Stumm-Lautstärke 0 bis 10, dauerhaft stumm
// nur, wenn stumm. Liefert die größte id (0 ohne Einträge).
function checkRecipients(entries) {
  if (!Array.isArray(entries) || entries.length > MAX_RECIPIENTS) {
    throw stateError('seedRecipients', `höchstens ${MAX_RECIPIENTS} Einträge als Liste`);
  }
  const ids = new Set();
  const keys = new Set();
  const tokens = new Set();
  for (const entry of entries) {
    const where = JSON.stringify(entry);
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) throw stateError('seedRecipients', `kein Eintrag: ${where}`);
    const unknown = Object.keys(entry).filter((name) => !RECIPIENT_FIELDS.has(name));
    if (unknown.length) throw stateError('seedRecipients', `unbekannte Felder ${unknown.join(', ')}: ${where}`);
    if (!Number.isInteger(entry.id) || entry.id < 1 || ids.has(entry.id)) throw stateError('seedRecipients', `id fehlt, ungültig oder doppelt: ${where}`);
    if (typeof entry.key !== 'string' || !BARK_KEY.test(entry.key) || keys.has(entry.key)) throw stateError('seedRecipients', `Key ungültig oder doppelt: ${where}`);
    if (typeof entry.label !== 'string') throw stateError('seedRecipients', `label fehlt: ${where}`);
    const wrong = Object.entries(entry).filter(([name, value]) => !fitsKind(RECIPIENT_FIELDS.get(name), value));
    if (wrong.length) throw stateError('seedRecipients', `falsche Art bei ${wrong.map(([name]) => name).join(', ')}: ${where}`);
    if ((entry.mute_volume ?? 0) < 0 || (entry.mute_volume ?? 0) > 10) throw stateError('seedRecipients', `mute_volume 0 bis 10: ${where}`);
    if (entry.mute_permanent && !entry.muted) throw stateError('seedRecipients', `dauerhaft stumm, aber nicht stumm: ${where}`);
    if (entry.mute_token !== undefined && (!MUTE_TOKEN.test(entry.mute_token) || tokens.has(entry.mute_token))) {
      throw stateError('seedRecipients', `mute_token ungültig oder doppelt: ${where}`);
    }
    ids.add(entry.id);
    keys.add(entry.key);
    if (entry.mute_token !== undefined) tokens.add(entry.mute_token);
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
  ageMark: (name, s) => {
    checkMark('ageMark', name);
    checkSeconds('ageMark', s);
    if (MARK_KINDS.get(name) !== 'time') throw stateError('ageMark', `"${name}" ist kein Zeitpunkt`);
    return [name, s];
  },
  ageRetry: (kind, s) => {
    checkSeconds('ageRetry', s);
    if (!ADMIN_KINDS.has(kind)) throw stateError('ageRetry', `unbekannte Art "${kind}"`);
    return [kind, s];
  },
  ageMuteThrottle: (s) => { checkSeconds('ageMuteThrottle', s); return [s]; },
  ageLog: (s, match = () => true) => {
    checkSeconds('ageLog', s);
    if (typeof match !== 'function') throw stateError('ageLog', 'match ist eine Bedingung (eintrag) => boolean');
    return [s, match];
  },
  ageSession: (cookie, s) => {
    checkSeconds('ageSession', s);
    if (typeof cookie !== 'string' || !/^[^=;\s]+=[^;\s]+$/.test(cookie)) {
      throw stateError('ageSession', `Cookie als 'name=wert', nicht ${JSON.stringify(cookie)}`);
    }
    return [cookie, s];
  },

  setWatcher: ({ seenAgo, barkFailedAt = 0 } = {}) => {
    checkSeconds('setWatcher', seenAgo);
    if (!Number.isSafeInteger(barkFailedAt)) throw stateError('setWatcher', `barkFailedAt ist eine ganze Zahl, nicht ${barkFailedAt}`);
    return [{ seenAgo, barkFailedAt }];
  },
  setMarks: (marks) => {
    if (!marks || typeof marks !== 'object' || Array.isArray(marks)) throw stateError('setMarks', 'Merker als Objekt { name: wert|null }');
    for (const [name, value] of Object.entries(marks)) {
      checkMark('setMarks', name);
      if (value !== null && !fitsKind(MARK_KINDS.get(name), value)) {
        throw stateError('setMarks', `"${name}" ist ${MARK_KINDS.get(name)}, nicht ${JSON.stringify(value)}`);
      }
    }
    return [marks];
  },
  setCommand: (patch) => {
    if (!patch || typeof patch !== 'object' || Array.isArray(patch)) throw stateError('setCommand', 'Felder als Objekt');
    const unknown = Object.keys(patch).filter((name) => !COMMAND_FIELDS.has(name));
    if (unknown.length) throw stateError('setCommand', `unbekannte Felder ${unknown.join(', ')}`);
    for (const [name, value] of Object.entries(patch)) {
      if (value !== null && !fitsKind(COMMAND_FIELDS.get(name), value)) {
        throw stateError('setCommand', `"${name}" ist ${COMMAND_FIELDS.get(name)}, nicht ${JSON.stringify(value)}`);
      }
    }
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

// ============================ START =========================================
// options.config: Abweichungen von der Standard-Konfiguration ab dem Start.
// options.port: fester Port statt eines freien (Handarbeit, serve.mjs).
// Treiber-Vertrag: kind, base, dataDir, configure, runWatchdog, reset, stop,
// stopServer()/startServer() (Server anhalten/wieder starten: gleicher Port,
// Daten unberührt), serverLog() sowie state, storage und words (siehe
// ABSICHTS-HELFER).
//
// Nur unter Go (unter PHP werfen sie):
//   hardKill()             Server ohne Vorwarnung beenden (wie ein Absturz)
//   writeRawConfig(text)   config.toml genau so schreiben (ein Tippfehler von Hand)
//                          und warten, bis der Server sie beurteilt hat
//   configText(overrides)  der Text, den configure(overrides) schriebe (Vorlage dafür)
//   health()               was fw-alarm health sagt ({ ok, running, state,
//                          save_ok, config_ok, hints, text })
//   cli(args)              ein fw-alarm-Befehl mit --config/--last-good des Prüfstands
//   files                  der Datenordner von Go roh (nur go-*.test.mjs)
//   startServer({ expectUnhealthy: true })  nach Absicht Kaputtem starten
// Was die Go-Fassung dabei tut und prüft, steht oben in harness-go.mjs.
//
// Die Treiber werden erst hier geladen: harness-php.mjs und harness-go.mjs
// importieren aus dieser Datei, nie umgekehrt.
const DRIVERS = {
  php: async () => {
    const mod = await import('./harness-php.mjs');
    return { start: mod.startPhp, data: mod.dataAccess };
  },
  go: async () => {
    const mod = await import('./harness-go.mjs');
    return { start: mod.startGo };
  },
};
const GO_ONLY = ['hardKill', 'writeRawConfig', 'configText', 'health', 'cli'];

export async function startBackend(options = {}) {
  const kind = BACKEND_KIND;
  if (!DRIVERS[kind]) {
    throw new Error(`Unbekanntes FW_BACKEND "${kind}" (vorhanden: ${Object.keys(DRIVERS).join(', ')})`);
  }
  const loaded = await DRIVERS[kind]();
  const root = mkdtempSync(join(tmpdir(), 'fw-backend-'));
  const bark = await startBarkMock();
  let driver;
  let state;
  let storage;
  try {
    driver = await loaded.start(root, bark, options);
    try {
      // Dieselben Prüfungen der Eingaben für jeden Treiber (ABSICHTS-HELFER).
      state = contractHelpers(kind, 'state', driver.state, STATE_INPUT);
      storage = contractHelpers(kind, 'storage', driver.storage, STORAGE_INPUT);
      if (options.config) await driver.configure(options.config);
    } catch (error) {
      // Scheitert auch das Anhalten, zählt trotzdem der eigentliche Fehler.
      try { await driver.stop(); } catch { /* bewusst verschluckt: der erste Fehler sagt, was los ist */ }
      throw error;
    }
  } catch (error) {
    await bark.stop();
    rmSync(root, { recursive: true, force: true, maxRetries: 10, retryDelay: 100 });
    throw error;
  }
  const backend = {
    ...driver,
    state,
    storage,
    root,
    bark,
    async reset() {
      await driver.reset();
      bark.clear();
    },
    async stop() {
      try {
        await driver.stop();
      } finally {
        await bark.stop();
        if (!process.env.FW_KEEP) {
          rmSync(root, { recursive: true, force: true, maxRetries: 10, retryDelay: 100 });
        }
      }
    },
  };
  for (const name of GO_ONLY) {
    if (typeof driver[name] !== 'function') {
      backend[name] = () => { throw new Error(`backend.${name}: nur unter Go, nicht unter "${kind}"`); };
    }
  }
  // Die Dateien von PHP - unter tests/backend/ nur für php-*.test.mjs; die von
  // Go (backend.files) nur für go-*.test.mjs.
  const absent = (name, owner) => ({
    get() { throw new Error(`backend.${name}: nur unter ${owner}, nicht unter "${kind}"`); },
  });
  if (loaded.data) backend.data = loaded.data(driver.dataDir);
  else Object.defineProperty(backend, 'data', absent('data', 'PHP'));
  if (!driver.files) Object.defineProperty(backend, 'files', absent('files', 'Go'));
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

// Jede Anfrage auf einer eigenen Verbindung (connection: close): Hält der
// Prüfstand den Server zwischendurch an (unter Go bei jedem state-Helfer),
// liefe eine weiterverwendete Verbindung ins Leere.
export async function request(url, init = {}) {
  const headers = new Headers(init.headers ?? {});
  if (!headers.has('connection')) headers.set('connection', 'close');
  const res = await fetch(url, { redirect: 'manual', signal: AbortSignal.timeout(30000), ...init, headers });
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
