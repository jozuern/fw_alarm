// Treiber für den Go-Server (FW_BACKEND=go, Programm aus FW_GO_BIN – das
// baut und setzt bash tools/test-go.sh). Vertrag und gemeinsame Prüfungen
// stehen in harness.mjs; die Tests importieren alles von dort, nie von hier.
//
// Aufbau im Temp-Ordner des Prüfstands (<root>):
//   config.toml    Standard des Prüfstands + Abweichungen (configure)
//   state/         state_dir des Servers = backend.dataDir
//   control.sock   Steuer-Zugang (fw-alarm health/flush/reload/stop/check)
//   beiseite/      was storage.fail('history') beiseitelegt
// Jeder Aufruf von fw-alarm bekommt --config <root>/config.toml und
// --last-good <root>/state/config.last-good.toml.
//
// Zustand ändern (backend.state, schreibende Helfer): Server geordnet
// anhalten (fw-alarm stop, warten bis der PROZESS fort ist), die Dateien des
// Datenordners ändern, auf demselben Port neu starten – ein Neustart
// verliert nichts, das prüft jeder Helfer mit. Der Server liest seine
// Dokumente streng (fehlendes, unbekanntes Feld oder falsche Art = nicht
// lesbar): Der Treiber ändert nur, was der Server selbst schrieb, und was er
// neu anlegt (Empfänger, Laufzeitdaten, Sitzungen), trägt jedes Feld seiner
// Art (Tabellen in harness.mjs, Plan "File formats"). Lesen: erst
// fw-alarm flush, dann die Dateien. Läuft der Server gerade nicht (der Test
// hat stopServer() gerufen), ändern und lesen die Helfer nur die Dateien.
//
// Nach JEDEM Start prüft der Treiber, dass der Server so gesund ist, wie der
// Test es erwartet: fw-alarm health sagt state=normal, save_ok=1 (außer
// während storage.fail), config_ok=1 (außer nach einem kaputten
// writeRawConfig); die Zeile "Ablage geöffnet" im Journal sagt start=neu nur
// auf einem vorher leeren Datenordner (erster Start, reset), sonst
// start=normal, und ruecksprung, laufzeit_verworfen, sitzungen_verworfen
// sind false; im Datenordner liegt keine NEUE *.corrupt-*-Datei. Sonst hätte
// der Server etwas abgelehnt, das der Treiber schrieb, und still neu
// angefangen - der Start wirft dann mit der Ausgabe des Servers. Ein
// go-*-Test, der über backend.files absichtlich etwas zerstört, startet mit
// startServer({ expectUnhealthy: true }); das gilt auch für die Neustarts
// der Helfer danach, bis zum nächsten startServer() oder reset().
//
// Keine Konfiguration, die der Server liest, zeigt je auf einen fremden
// Bark-Server: ein Wächter ohne Ausnahme über configure, writeRawConfig,
// backend.files.write und .corrupt (Regeln unten bei HOST_RULE). Dateinamen
// in backend.files gibt es nur in einer Schreibweise; backend.cli lässt eine
// eigene Konfiguration nur check-config durch.
//
// storage.fail(what) legt einen nicht leeren Ordner an den festen Namen der
// Zwischendatei, das Sichern dieses Teils scheitert dann im laufenden
// Betrieb (unter Windows wie unter Linux); repair stellt her, was vorher dort
// lag:
//   selftest  selftest.tmp
//   durable   state.json.tmp - feste Änderungen werden abgelehnt
//   history   history.jsonl wandert nach beiseite/, an seine Stelle ein
//             Ordner; repair legt alles Beiseitegelegte in seiner Reihenfolge
//             zurück (Einzelheiten bei failHistory)
//   volatile  runtime.json.tmp und sessions.json.tmp
// Solange etwas gestört ist, lesen die Helfer, was zuletzt gesichert wurde
// (fw-alarm flush scheitert dann, das ist erwartet), und der Server darf mit
// Rückgabe 1 enden (er konnte beim Beenden nicht alles sichern).
import { execFile, spawn } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import {
  appendFileSync, existsSync, linkSync, mkdirSync, readdirSync, readFileSync, renameSync, rmSync, statSync,
  writeFileSync,
} from 'node:fs';
import { createServer as createTcpServer } from 'node:net';
import { join } from 'node:path';
import {
  ADMIN, COMMAND_FIELDS, MARK_KINDS, MACHINE_TOKEN, READER, RECIPIENT_FIELDS, STATUS_KEY, TEST_PASSWORD_HASH,
  now, sha256, sleep, stateError,
} from './harness.mjs';

// ---- Feste Werte -----------------------------------------------------------
// Ports aus 20000–29999, nicht aus dem dynamischen Bereich: Der Server ist
// pro Testdatei hunderte Male kurz fort, und die Verbindungen der Tests
// selbst bekommen dynamische Ports - einer davon könnte sonst genau dann
// unseren Port belegen.
const PORT_MIN = 20000;
const PORT_MAX = 29999;
const START_TIMEOUT_MS = 15000;     // bis der Server antwortet
const PORT_BUSY_RETRY_MS = 10000;   // so lange erneut versuchen, solange der Port noch belegt ist
const EXIT_AFTER_STOP_MS = 10000;   // nach fw-alarm stop muss der Prozess fort sein
const RAW_CONFIG_WAIT_MS = 10000;   // writeRawConfig: so lange auf das Urteil des Servers warten
const LOG_KEEP = 200000;
// Was nach einem Neustart sonst woanders hinzeigte (Datenordner, Port,
// Steuer-Zugang): configure() lehnt diese Schlüssel ab, eine gültige rohe
// Fassung muss ihre Zeilen behalten.
const FIXED_KEYS = ['state_dir', 'listen', 'control_socket'];

// Befehle, die --last-good kennen (die übrigen lehnen es ab).
const SERVER_COMMANDS = new Set(['serve', 'health', 'check', 'flush', 'reload', 'stop']);

// Die neuen Merker der Go-Fassung neben den PHP-Namen (MARK_KINDS).
const NEW_WATCH_FIELDS = () => ({
  last_run_line: '', retry_after: {}, emergency_notified_at: 0, config_broken_notified_at: 0, pending: [],
});
const zeroOf = (kind) => (kind === 'bool' ? false : kind === 'string' ? '' : 0);

// Leere Dokumente in genau der Gestalt von internal/model (alle Felder da,
// richtige Art) - für Laufzeitdaten und Sitzungen, die der Server erst
// schreibt, wenn sich etwas ändert.
function emptyRuntime() {
  const watch = {};
  for (const [name, kind] of MARK_KINDS) watch[name] = zeroOf(kind);
  Object.assign(watch, NEW_WATCH_FIELDS());
  return {
    schema: 1, lifebeat_at: 0, silence_from: 0, box: null, alarms_seen: {}, devices: {}, watcher: null,
    watch, throttle: { login: {}, mute: {} }, outbox: [],
  };
}
const emptySessions = () => ({ schema: 1, sessions: {} });

// Ein neues Stumm-Token wie der Server es vergibt: 16 Zeichen A-Z a-z 0-9.
function newMuteToken() {
  const abc = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
  return [...randomBytes(16)].map((b) => abc[b % abc.length]).join('');
}

// ---- TOML ------------------------------------------------------------------
// Genug TOML für die Konfiguration: Texte, ganze Zahlen, Wahrheitswerte,
// Listen von Texten. Alles andere ist ein Fehler im Test.
function tomlString(text) {
  let out = '"';
  for (const ch of text) {
    const code = ch.codePointAt(0);
    if (ch === '"') out += '\\"';
    else if (ch === '\\') out += '\\\\';
    else if (code < 0x20 || code === 0x7f) out += `\\u${code.toString(16).padStart(4, '0')}`;
    else out += ch;
  }
  return `${out}"`;
}

function tomlValue(key, value) {
  if (typeof value === 'string') return tomlString(value);
  if (typeof value === 'boolean') return String(value);
  if (Number.isSafeInteger(value)) return String(value);
  if (Array.isArray(value) && value.every((v) => typeof v === 'string')) {
    return `[${value.map(tomlString).join(', ')}]`;
  }
  throw new Error(`configure: "${key}" lässt sich nicht als TOML schreiben (${JSON.stringify(value)})`);
}

// Die Schlüssel in der gegebenen Reihenfolge, der Kommentar zuletzt - die
// erste Zeile ist so immer die erste Zuweisung (bark_host, siehe unten).
function tomlText(values) {
  const lines = [];
  for (const [key, value] of Object.entries(values)) {
    if (!/^[A-Za-z0-9_-]+$/.test(key)) throw new Error(`configure: Schlüssel "${key}" ist kein einfacher TOML-Name`);
    lines.push(`${key} = ${tomlValue(key, value)}`);
  }
  lines.push('# Konfiguration des Prüfstands (tests/backend/harness-go.mjs)');
  return `${lines.join('\n')}\n`;
}

// ---- Wächter über jede Konfiguration -------------------------------------
// Der Prüfstand schreibt nie eine Konfiguration, mit der der Server an einen
// echten Bark-Server senden könnte - über keinen Weg (configure,
// writeRawConfig, backend.files.write und .corrupt auf *.toml) und zu
// keinem Zeitpunkt. Die Regel gilt AUSNAHMSLOS für jeden Text, gültig oder
// kaputt, auch leer (guardConfigText unten):
//   - kein """ und kein ''' irgendwo im Text;
//   - GENAU EINE Zeile (ohne \r am Ende), die genau
//     bark_host = "http://127.0.0.1:<1–5 Ziffern>" oder bark_host = "" ist;
//   - keine weitere Zeile, auf der bark_host vor einem = steht (Schlüssel in
//     Anführungszeichen, ohne Leerzeichen, eingerückt, in einem Kommentar
//     oder einem anderen Wert: alles abgelehnt).
// Ob der Parser des Servers den Text annimmt, spielt für den Wächter keine
// Rolle mehr: Ein "kaputter" Text kann später gültig werden (die
// Probe-Sperre wird gegen die Uhr geprüft: mehr als 3 h in der Zukunft ist
// heute kaputt, in einer Minute gültig), und jeder spätere Start oder reload
// nähme ihn dann.
// Warum die Regel genügt: Ein Text, der sie erfüllt, wird vom Parser des
// Servers entweder abgelehnt - jetzt oder später, und dann trägt er immer
// noch die lokale Zeile - oder angenommen; dann ist die Zeile eine echte
// Zuweisung auf oberster Ebene. In einem mehrzeiligen Text kann sie nicht
// stehen (dreifache Anführungszeichen sind verboten); eine Zeile
// schlüssel = wert in einer mehrzeiligen Liste oder unter einer
// Tabellen-Überschrift macht den Text für diesen Parser kaputt (er kennt keine
// Tabellen, unbekannte Schlüssel sind Fehler); eine zweite Zuweisung an
// bark_host ist ein doppelter Schlüssel - ebenfalls kaputt.
// Wer eine kaputte Datei braucht, schreibt die Zeile plus seinen Unsinn;
// configText() beginnt mit der Zeile, also trägt jeder Anfang davon bis zu
// einem Zeilenende sie (halb geschriebene Datei).
const BENCH_HOST_LINE = /^bark_host = "(?:http:\/\/127\.0\.0\.1:\d{1,5})?"$/;
const MENTIONS_HOST = /bark_host[^=]*=/;
const HOST_RULE = 'muss genau eine Zeile bark_host = "http://127.0.0.1:<port>" enthalten – sonst sendete der Server an das echte Bark';

// Wirft, wenn text nicht geschrieben werden darf (Regel oben).
function guardConfigText(text, where) {
  if (typeof text !== 'string') throw new Error(`${where}: die Konfiguration als Text`);
  if (text.includes('"""') || text.includes("'''")) {
    throw new Error(`${where}: dreifache Anführungszeichen (""" oder ''') sind im Prüfstand verboten – `
      + 'in einem mehrzeiligen Text sähe der Wächter nicht sicher, wohin der Server sendet');
  }
  const lines = text.split('\n').map((line) => line.replace(/\r$/, ''));
  const exact = lines.filter((line) => BENCH_HOST_LINE.test(line));
  const mentions = lines.filter((line) => MENTIONS_HOST.test(line));
  if (exact.length !== 1 || mentions.length !== 1) {
    throw new Error(`${where}: jeder Konfigurationstext, gültig oder kaputt, ${HOST_RULE} `
      + `(gefunden: ${mentions.length ? mentions.join(' | ') : 'keine'})`);
  }
}

// Dateinamen in backend.files: genau eine Schreibweise je Datei. Klein,
// ohne ~ (Kurznamen), : (Datenströme), $, Leerzeichen, Schrägstriche und
// ohne Punkt am Ende - geprüft unabhängig vom Dateisystem.
const FILE_NAME = /^[a-z0-9][a-z0-9._-]*$/;
function checkFileName(name) {
  if (typeof name !== 'string' || !FILE_NAME.test(name) || name.endsWith('.')) {
    throw new Error(`backend.files: ${JSON.stringify(name)} ist kein erlaubter Dateiname `
      + '(klein, a-z 0-9 . _ -, nicht mit . am Ende)');
  }
}

// Schalter, die eine andere Konfiguration oder Kopie wählten - in jeder
// Schreibweise des flag-Pakets von Go (-x, --x, -x=…, --x=…).
const CONFIG_FLAG = /^--?(?:config|last-good)(?:=|$)/;

// ---- Ports -----------------------------------------------------------------
function portFree(port) {
  return new Promise((resolve) => {
    const probe = createTcpServer();
    probe.once('error', () => resolve(false));
    probe.listen(port, '127.0.0.1', () => probe.close(() => resolve(true)));
  });
}

async function pickPort() {
  for (let i = 0; i < 200; i++) {
    const port = PORT_MIN + Math.floor(Math.random() * (PORT_MAX - PORT_MIN + 1));
    if (await portFree(port)) return port;
  }
  throw new Error(`Kein freier Port zwischen ${PORT_MIN} und ${PORT_MAX}`);
}

// Antwortet der Server auf GET /manifest.json?
async function manifestAnswers(base) {
  try {
    const res = await fetch(`${base}/manifest.json`, {
      headers: { connection: 'close' }, signal: AbortSignal.timeout(1000),
    });
    await res.arrayBuffer();
    return res.status === 200;
  } catch {
    return false;
  }
}

// Ausgabe von fw-alarm health als Objekt: die name=wert-Zeilen, hint= als Liste.
function parseHealth(result) {
  const out = { ok: result.code === 0, code: result.code, text: result.stdout, stderr: result.stderr, hints: [] };
  for (const line of result.stdout.split('\n')) {
    const eq = line.indexOf('=');
    if (eq <= 0) continue;
    const name = line.slice(0, eq);
    const value = line.slice(eq + 1).replace(/\r$/, '');
    if (name === 'hint') out.hints.push(value);
    else out[name] = value;
  }
  return out;
}

// Wiederholt fn, solange Windows meldet, dass ein anderer die Datei gerade
// hält (Virenscanner, der Server mitten im Schreiben).
async function retryBusy(fn) {
  for (let i = 0; ; i++) {
    try {
      return fn();
    } catch (error) {
      if (!['EPERM', 'EBUSY', 'EACCES'].includes(error.code) || i >= 50) throw error;
      await sleep(20);
    }
  }
}

// ============================ TREIBER =======================================
export async function startGo(root, bark, options = {}) {
  const bin = process.env.FW_GO_BIN;
  if (!bin || !existsSync(bin)) {
    throw new Error(`FW_GO_BIN fehlt oder zeigt ins Leere (${bin ?? 'nicht gesetzt'}) – `
      + 'bash tools/test-go.sh baut bin/fw-alarm und setzt ihn');
  }
  const stateDir = join(root, 'state');
  const asideDir = join(root, 'beiseite');
  const configPath = join(root, 'config.toml');
  const lastGoodPath = join(stateDir, 'config.last-good.toml');
  const socketPath = join(root, 'control.sock');
  mkdirSync(stateDir, { recursive: true });

  const port = options.port || await pickPort();
  const base = `http://127.0.0.1:${port}`;
  // Wie der PHP-Treiber (baseConfig) ohne data_dir und health_check_url, dazu
  // was nur Go kennt. bark_host steht als erste Zeile (Wächter), gleich
  // danach state_dir, listen und control_socket: Ein Anfang des Texts ab vier
  // Zeilen trägt so alles, was writeRawConfig von einer gültigen Fassung
  // verlangt. bark_alarm_title: PHP nimmt ihn aus config.example.php (dort mit
  // "(manuell)"), Go ohne Eintrag den Wert aus dem PHP-Code - so sehen die
  // Tests unter beiden denselben Titel.
  if (!/^http:\/\/127\.0\.0\.1:\d{1,5}$/.test(bark.base)) throw new Error(`Bark-Attrappe nicht lokal: ${bark.base}`);
  const baseConfig = {
    bark_host: bark.base,
    state_dir: stateDir,
    listen: `127.0.0.1:${port}`,
    control_socket: socketPath,
    machine_token_sha256: sha256(MACHINE_TOKEN),
    dashboard_user: ADMIN.user,
    dashboard_password_hash: TEST_PASSWORD_HASH,
    dashboard_readonly_user: READER.user,
    dashboard_readonly_password_hash: TEST_PASSWORD_HASH,
    bark_key_status: STATUS_KEY,
    bark_keys_alarm: ['startkey0001'],
    bark_alarm_title: 'FEUERWEHR-ALARM (manuell)',
    check_interval_seconds: 0,
  };

  // ---- fw-alarm aufrufen ------------------------------------------------
  // Jeder Befehl bekommt --config des Prüfstands und, wo er es kennt,
  // --last-good. Nur check-config darf auf eine eigene Datei zeigen (dann
  // ohne die des Prüfstands - beim flag-Paket gälte sonst die letzte).
  const flagsFor = (args) => {
    const flags = [];
    if (!(args[0] === 'check-config' && args.some((arg) => /^--?config(?:=|$)/.test(arg)))) flags.push('--config', configPath);
    if (SERVER_COMMANDS.has(args[0])) flags.push('--last-good', lastGoodPath);
    return flags;
  };
  // Ein Befehl; liefert { code, stdout, stderr } (code null = abgebrochen).
  const run = (args, timeoutMs) => new Promise((resolve) => {
    execFile(bin, args, { cwd: root, encoding: 'utf8', timeout: timeoutMs, windowsHide: true, maxBuffer: 16 << 20 },
      (error, stdout, stderr) => {
        let code = 0;
        if (error) code = Number.isInteger(error.code) ? error.code : null;
        resolve({ code, stdout: String(stdout ?? ''), stderr: String(stderr ?? '') + (code === null ? `\n${error.message}` : '') });
      });
  });
  const cli = (args, { timeoutMs = 30000 } = {}) => run([...args, ...flagsFor(args)], timeoutMs);
  const health = async () => parseHealth(await cli(['health'], { timeoutMs: 20000 }));
  // Für die Tests: Eine eigene Konfiguration oder Kopie (jede Schreibweise
  // des Schalters) nur bei check-config - es liest und urteilt nur. Allen
  // anderen Befehlen gibt der Prüfstand immer seine eigenen Schalter: Sonst
  // liefe etwa ein serve auf einer Datei, die der Wächter nie sah.
  const testCli = async (args, opts) => {
    if (!Array.isArray(args) || args.some((arg) => typeof arg !== 'string')) throw new Error('backend.cli: Argumente als Liste von Texten');
    if (args[0] !== 'check-config' && args.some((arg) => CONFIG_FLAG.test(arg))) {
      throw new Error(`backend.cli: eigene --config/--last-good nur für check-config, nicht für ${args[0]} – `
        + 'die übrigen Befehle laufen immer mit denen des Prüfstands');
    }
    return cli(args, opts);
  };
  // Ob der Parser des Servers den Text gerade annimmt (fw-alarm check-config
  // auf einer Kopie) - nur, damit writeRawConfig weiß, worauf es wartet; nie
  // eine Bedingung des Wächters.
  const parserAccepts = async (text) => {
    const probe = join(root, 'config.probe.toml');
    writeFileSync(probe, text);
    try {
      const verdict = await run(['check-config', '--config', probe], 30000);
      if (verdict.code !== 0 && verdict.code !== 1) {
        throw new Error(`fw-alarm check-config urteilte nicht (${verdict.code}): ${verdict.stdout}${verdict.stderr}`);
      }
      return verdict.code === 0;
    } finally {
      rmSync(probe, { force: true });
    }
  };

  // ---- Prozess ------------------------------------------------------------
  let log = '';
  let child = null;      // der laufende serve-Prozess
  let exited = null;     // Promise: { code, signal } dieses Prozesses
  let alive = false;
  let expectUnhealthy = false;   // nach Absicht Kaputtes: Start ohne Gesundheitsprüfung
  let configBroken = false;      // config.toml ist (nach Absicht) fehlerhaft
  const failed = new Set();      // gestörte Teile (storage.fail)
  const saved = new Map();       // Name im Datenordner -> Inhalt vor fail() (null = gab es nicht)
  let historyPieces = [];        // was von history.jsonl in beiseite/ liegt, in der Reihenfolge des Entstehens
  let asideSeq = 0;              // laufende Nummer für Namen in beiseite/ (nie zweimal derselbe)
  const keep = (chunk) => { log = (log + chunk).slice(-LOG_KEEP); };
  const killOnExit = () => { try { child?.kill('SIGKILL'); } catch { /* schon beendet */ } };
  process.on('exit', killOnExit);
  const lenient = () => expectUnhealthy || failed.size > 0;

  const corruptFiles = () => {
    try { return new Set(readdirSync(stateDir).filter((name) => name.includes('.corrupt-'))); } catch { return new Set(); }
  };

  // Ein Startversuch: 'up', oder { code, busy, text } wenn der Prozess endete.
  const spawnOnce = async () => {
    const mine = spawn(bin, ['serve', ...flagsFor(['serve'])], {
      cwd: root, stdio: ['ignore', 'pipe', 'pipe'], windowsHide: true,
    });
    let text = '';
    const onData = (chunk) => {
      keep(chunk);
      if (text.length < LOG_KEEP) text += chunk;
    };
    mine.stdout.setEncoding('utf8');
    mine.stderr.setEncoding('utf8');
    mine.stdout.on('data', onData);
    mine.stderr.on('data', onData);
    let ended = null;
    const done = new Promise((resolve) => {
      mine.on('exit', (code, signal) => { ended = { code, signal }; resolve(ended); });
      mine.on('error', (error) => { ended = { code: null, signal: null, error }; resolve(ended); });
    });
    child = mine;
    exited = done.then((result) => { if (child === mine) alive = false; return result; });
    alive = true;
    const end = Date.now() + START_TIMEOUT_MS;
    while (Date.now() < end) {
      if (ended) {
        await exited;
        return { code: ended.code, busy: /Fehler: listen = /.test(text), text: ended.error ? `${text}\n${ended.error.message}` : text };
      }
      if (text.includes('msg="Server läuft"') && await manifestAnswers(base)) return { up: true, text };
      await sleep(25);
    }
    mine.kill('SIGKILL');
    await exited;
    throw new Error(`Go-Server antwortet nach ${START_TIMEOUT_MS} ms nicht (Port ${port}):\n${text}`);
  };

  // Die Felder einer Zeile des Journals (slog-Text: name=wert …) mit dieser Nachricht.
  const journalLine = (text, msg) => {
    const line = text.split('\n').find((l) => l.includes(`msg="${msg}"`));
    if (!line) return null;
    return Object.fromEntries([...line.matchAll(/(\w+)=("(?:[^"\\]|\\.)*"|\S*)/g)].map(([, k, v]) => [k, v]));
  };

  // Ist der Server so gesund, wie der Test es erwartet? (siehe oben)
  // emptyBefore: Der Datenordner war vor dem Start leer - nur dann darf er
  // neu anfangen.
  const checkStarted = async (corruptBefore, emptyBefore, text) => {
    if (expectUnhealthy) return;
    const h = await health();
    const problems = [];
    if (h.running !== '1') problems.push('running ist nicht 1');
    if (h.state !== 'normal') problems.push(`state=${h.state}`);
    if (h.config_ok !== '1' && !configBroken) problems.push(`config_ok=${h.config_ok}`);
    if (h.save_ok !== '1' && failed.size === 0) problems.push(`save_ok=${h.save_ok}`);
    const opened = journalLine(text, 'Ablage geöffnet');
    if (!opened) {
      problems.push('Zeile "Ablage geöffnet" fehlt im Journal');
    } else {
      if (emptyBefore && opened.start !== 'neu') problems.push(`start=${opened.start} statt neu auf einem leeren Datenordner`);
      if (!emptyBefore && opened.start === 'neu') problems.push('start=neu auf einem nicht leeren Datenordner');
      if (!emptyBefore && opened.start !== 'neu' && opened.start !== 'normal') problems.push(`start=${opened.start} statt normal`);
      for (const flag of ['ruecksprung', 'laufzeit_verworfen', 'sitzungen_verworfen']) {
        if (opened[flag] !== 'false') problems.push(`${flag}=${opened[flag]}`);
      }
    }
    const fresh = [...corruptFiles()].filter((name) => !corruptBefore.has(name));
    if (fresh.length) problems.push(`beiseitegelegt: ${fresh.join(', ')}`);
    if (problems.length) {
      throw new Error(`Go-Server nach dem Start nicht gesund (${problems.join('; ')}) – `
        + `hat der Treiber etwas geschrieben, das der Server ablehnt?\n--- fw-alarm health ---\n${h.text}${h.stderr}`
        + `\n--- Journal ---\n${text}`);
    }
  };

  // Startet den Server (auf demselben Port) und wartet, bis er antwortet.
  const launch = async () => {
    if (alive) return;
    const busyUntil = Date.now() + PORT_BUSY_RETRY_MS;
    for (;;) {
      const corruptBefore = corruptFiles();
      // Jeder Versuch für sich: Ein am belegten Port gescheiterter Versuch hat
      // den Datenordner schon angelegt, der nächste startet dann "normal".
      const emptyBefore = readdirSync(stateDir).length === 0;
      const outcome = await spawnOnce();
      if (outcome.up) {
        await checkStarted(corruptBefore, emptyBefore, outcome.text);
        return;
      }
      if (outcome.busy && Date.now() < busyUntil) {
        await sleep(200);
        continue;
      }
      throw new Error(`Go-Server startet nicht (Port ${port}, Rückgabe ${outcome.code}):\n${outcome.text}`);
    }
  };

  // Hält den Server geordnet an; der Datenordner bleibt unberührt.
  const halt = async () => {
    if (!alive) return;
    const mine = child;
    const ending = exited;
    const answer = await cli(['stop'], { timeoutMs: 75000 });
    const result = await Promise.race([ending, sleep(EXIT_AFTER_STOP_MS).then(() => null)]);
    if (result === null) {
      mine.kill('SIGKILL');
      await ending;
      throw new Error(`Go-Server war ${EXIT_AFTER_STOP_MS / 1000} s nach fw-alarm stop noch nicht beendet – `
        + `hart beendet.\nfw-alarm stop (${answer.code}): ${answer.stdout}${answer.stderr}\n--- Journal ---\n${log.slice(-20000)}`);
    }
    if (answer.code !== 0) {
      throw new Error(`fw-alarm stop scheiterte (${answer.code}): ${answer.stdout}${answer.stderr}`);
    }
    if (result.code !== 0 && !lenient()) {
      throw new Error(`Go-Server endete mit Rückgabe ${result.code}:\n${log.slice(-20000)}`);
    }
  };

  const kill = async () => {
    if (!alive) return;
    const ending = exited;
    child.kill('SIGKILL');
    await ending;
  };

  // Alle Schritte nacheinander: Ein Helfer hält den Server an und startet
  // ihn neu; ein zweiter, gleichzeitiger Aufruf darf da nicht hinein.
  let queue = Promise.resolve();
  const serial = (fn) => {
    const next = queue.then(fn, fn);
    queue = next.catch(() => { /* der Fehler gehört dem Aufrufer */ });
    return next;
  };

  // ---- Konfiguration ------------------------------------------------------
  // Der Text der Fassung des Prüfstands mit overrides. Ein überschriebener
  // Schlüssel behält seinen Platz (bark_host bleibt vorn); null entfernt ihn.
  const configText = (overrides) => {
    const fixed = Object.keys(overrides).filter((key) => FIXED_KEYS.includes(key));
    if (fixed.length) {
      throw new Error(`configure: state_dir, listen und control_socket gehören dem Prüfstand (${fixed.join(', ')}) – `
        + 'nach einem Neustart zeigte der Server woanders hin, die Helfer änderten still den alten Ordner');
    }
    const values = { ...baseConfig };
    for (const [key, value] of Object.entries(overrides)) {
      if (value === undefined) continue;
      if (value === null) delete values[key];
      else values[key] = value;
    }
    return tomlText(values);
  };
  // Schreibt config.toml nur nach dem Wächter.
  const writeConfigText = (text, where) => {
    guardConfigText(text, where);
    writeFileSync(configPath, text);
  };
  const writeConfig = (overrides) => writeConfigText(configText(overrides), 'configure');
  // Die Zeilen, die nach einem Neustart sonst woanders hinzeigten - eine
  // gültige rohe Fassung muss sie behalten.
  const fixedLines = FIXED_KEYS.map((key) => `${key} = ${tomlValue(key, baseConfig[key])}`);

  // ---- Datenordner --------------------------------------------------------
  const file = (name) => join(stateDir, name);
  const isFile = (path) => {
    try { return statSync(path).isFile(); } catch { return false; }
  };
  // Ein Dokument des Servers; null, wenn es fehlt. Kaputtes JSON wirft.
  const readDoc = (name) => (isFile(file(name)) ? JSON.parse(readFileSync(file(name), 'utf8')) : null);
  // Zum Lesen: Unlesbares zählt wie fehlend (wie die Leser von PHP).
  const peekDoc = (name) => {
    try { return readDoc(name); } catch { return null; }
  };
  // Ersetzt ein Dokument in einem Schritt (Temp-Datei, Umbenennen).
  const writeDoc = async (name, value) => {
    const tmp = file(`${name}.treiber`);
    writeFileSync(tmp, `${JSON.stringify(value, null, 2)}\n`);
    await retryBusy(() => renameSync(tmp, file(name)));
  };

  // Server anhalten, fn(dokumente) ändern lassen, schreiben, neu starten.
  // fn bekommt { state(), runtime(), sessions() } - jedes Dokument wird beim
  // ersten Zugriff gelesen und am Ende geschrieben, wenn fn es angefasst hat.
  // Wirft fn, wird nichts geschrieben (der Server startet trotzdem wieder).
  const change = (helper, fn) => serial(async () => {
    const wasAlive = alive;
    if (wasAlive) await halt();
    let failure = null;
    let result;
    try {
      const docs = new Map();
      const get = (name, empty) => {
        if (!docs.has(name)) {
          const value = readDoc(name) ?? empty?.();
          if (value === undefined) throw stateError(helper, `${name} fehlt im Datenordner`);
          docs.set(name, value);
        }
        return docs.get(name);
      };
      result = fn({
        state: () => get('state.json'),
        runtime: () => get('runtime.json', emptyRuntime),
        sessions: () => get('sessions.json', emptySessions),
      });
      for (const [name, value] of docs) await writeDoc(name, value);
    } catch (error) {
      failure = error;
    }
    if (wasAlive) await launch();
    if (failure) throw failure;
    return result;
  });

  // Lesen: erst alles sichern lassen (läuft der Server), dann die Dateien.
  const settled = () => serial(async () => {
    if (!alive) return;
    const r = await cli(['flush'], { timeoutMs: 70000 });
    if (r.code !== 0 && !lenient()) throw new Error(`fw-alarm flush scheiterte (${r.code}): ${r.stdout}${r.stderr}`);
  });
  const read = async (fn) => {
    await settled();
    return fn();
  };

  // Verlauf: Zeilen der beiden Dateien, älteste zuerst.
  const historyNames = ['history.1.jsonl', 'history.jsonl'];
  const historyEntries = (name) => {
    if (!isFile(file(name))) return [];
    const out = [];
    for (const line of readFileSync(file(name), 'utf8').split('\n')) {
      if (!line.trim()) continue;
      try { out.push(JSON.parse(line)); } catch { /* abgerissene Zeile: liest auch der Server nicht */ }
    }
    return out;
  };

  const ago = (seconds) => now() - seconds;
  const deepCopy = (value) => JSON.parse(JSON.stringify(value));

  // ---- backend.state ------------------------------------------------------
  // Die Eingaben sind hier schon geprüft (STATE_INPUT in harness.mjs).
  const state = {
    ageBoxStatus: (seconds) => change('ageBoxStatus', (d) => {
      const box = d.runtime().box;
      if (!box) throw stateError('ageBoxStatus', 'noch kein Status der Box gespeichert');
      box.seen_at = ago(seconds);
    }),
    ageDevice: (deviceId, seconds) => change('ageDevice', (d) => {
      const device = Object.hasOwn(d.runtime().devices, deviceId) ? d.runtime().devices[deviceId] : null;
      if (!device) throw stateError('ageDevice', `Board "${deviceId}" hat sich nie gemeldet`);
      device.seen_at = ago(seconds);
    }),
    ageMute: (idOrAll, seconds) => change('ageMute', (d) => {
      const list = d.state().recipients;
      const targets = idOrAll === 'all' ? list.filter((k) => k.muted) : list.filter((k) => k.id === idOrAll);
      if (idOrAll !== 'all' && targets.length === 0) throw stateError('ageMute', `kein Empfänger mit id ${idOrAll}`);
      if (targets.length === 0) throw stateError('ageMute', 'niemand ist stumm');
      for (const k of targets) {
        if (!k.muted) throw stateError('ageMute', `Empfänger ${k.id} ist nicht stumm`);
        k.muted_at = ago(seconds);
      }
    }),
    ageDemo: (seconds) => change('ageDemo', (d) => {
      const demo = d.state().demo;
      if (!demo.changed_at) throw stateError('ageDemo', 'noch kein Demo-Zustand gespeichert');
      demo.changed_at = ago(seconds);
    }),
    ageCommand: (which, seconds) => change('ageCommand', (d) => {
      const current = d.state().commands.current;
      if (!current) throw stateError('ageCommand', 'kein laufender Befehl');
      current[`${which}_at`] = ago(seconds);
    }),
    ageWatcher: (seconds) => change('ageWatcher', (d) => {
      const watcher = d.runtime().watcher;
      if (!watcher) throw stateError('ageWatcher', 'der Außen-Wächter hat sich nie gemeldet');
      watcher.seen_at = ago(seconds);
    }),
    ageMark: (name, seconds) => change('ageMark', (d) => { d.runtime().watch[name] = ago(seconds); }),
    ageRetry: (kind, seconds) => change('ageRetry', (d) => {
      const retry = d.runtime().watch.retry_after;
      if (!Object.hasOwn(retry, kind)) throw stateError('ageRetry', `keine Sperrfrist für "${kind}" gespeichert`);
      retry[kind] = ago(seconds);
    }),
    ageMuteThrottle: (seconds) => change('ageMuteThrottle', (d) => {
      const counted = Object.values(d.runtime().throttle.mute);
      if (counted.length === 0) throw stateError('ageMuteThrottle', 'keine Fehlversuche gezählt');
      for (const who of counted) who.first_at = ago(seconds);
    }),
    // Verlauf in beiden Dateien und in den outboxen von state.json und
    // runtime.json (dort steht ein Eintrag, bis der Verlauf ihn hat - nach
    // dem Anhalten normalerweise nichts mehr). Erst alles ausrechnen, dann
    // schreiben: Wirft ein Eintrag, bleibt alles, wie es war.
    ageLog: (seconds, match) => change('ageLog', (d) => {
      const older = (entry, where) => {
        if (typeof entry.log_at !== 'number' || !Number.isFinite(entry.log_at)) {
          throw stateError('ageLog', `passender Eintrag ohne log_at als Zahl (${where}): ${JSON.stringify(entry)}`);
        }
        entry.log_at -= seconds;
      };
      const rewritten = [];
      for (const name of historyNames) {
        if (!isFile(file(name))) continue;
        let touched = false;
        const lines = readFileSync(file(name), 'utf8').split('\n').map((line) => {
          if (!line.trim().startsWith('{')) return line;
          let entry;
          try { entry = JSON.parse(line); } catch { return line; }
          if (!match(entry)) return line;
          older(entry, name);
          touched = true;
          return JSON.stringify(entry);
        });
        if (touched) rewritten.push([name, lines.join('\n')]);
      }
      const outboxes = [['state.json', d.state], ['runtime.json', d.runtime]];
      for (const [name, doc] of outboxes) {
        if (!isFile(file(name))) continue;
        for (const entry of doc().outbox) if (match(entry)) older(entry, `outbox von ${name}`);
      }
      for (const [name, text] of rewritten) writeFileSync(file(name), text);
    }),
    ageSession: (cookie, seconds) => change('ageSession', (d) => {
      const value = cookie.slice(cookie.indexOf('=') + 1);
      const session = d.sessions().sessions[sha256(value)];
      if (!session) throw stateError('ageSession', 'keine Sitzung zu diesem Cookie gespeichert');
      session.last_seen = ago(seconds);
    }),

    setWatcher: ({ seenAgo, barkFailedAt }) => change('setWatcher', (d) => {
      d.runtime().watcher = { seen_at: ago(seenAgo), bark_failed_at: barkFailedAt };
    }),
    setMarks: (marks) => change('setMarks', (d) => {
      const watch = d.runtime().watch;
      for (const [name, value] of Object.entries(marks)) watch[name] = value ?? zeroOf(MARK_KINDS.get(name));
    }),
    setCommand: (patch) => change('setCommand', (d) => {
      const { commands } = d.state();
      if (!commands.current) throw stateError('setCommand', 'kein laufender Befehl');
      for (const [name, value] of Object.entries(patch)) {
        commands.current[name] = value ?? zeroOf(COMMAND_FIELDS.get(name));
      }
      const { id } = commands.current;
      if (id < 1 || id >= commands.next_id) {
        throw stateError('setCommand', `id ${id} muss zwischen 1 und ${commands.next_id - 1} liegen (commands.next_id)`);
      }
    }),
    // Einträge wie gegeben; der Server liest den Verlauf nur beim Start.
    seedLog: (entries, { rotated }) => change('seedLog', () => {
      const path = file(rotated ? 'history.1.jsonl' : 'history.jsonl');
      const before = existsSync(path) ? readFileSync(path, 'utf8') : '';
      const gap = before && !before.endsWith('\n') ? '\n' : '';
      appendFileSync(path, gap + entries.map((entry) => `${JSON.stringify(entry)}\n`).join(''));
    }),
    seedRecipients: (entries, { version, nextId }) => change('seedRecipients', (d) => {
      const st = d.state();
      const tokens = new Set(entries.map((e) => e.mute_token).filter((t) => t !== undefined));
      const list = entries.map((entry) => {
        const full = {};
        for (const [name, kind] of RECIPIENT_FIELDS) full[name] = entry[name] ?? zeroOf(kind);
        if (entry.mute_token === undefined) {
          do full.mute_token = newMuteToken(); while (tokens.has(full.mute_token));
          tokens.add(full.mute_token);
        }
        return full;
      });
      if (st.demo.enabled && !list.some((k) => k.id === st.demo.key_id && k.key === st.demo.key)) {
        throw stateError('seedRecipients', `Demo-Modus ist an (Empfänger id ${st.demo.key_id}) – `
          + 'die neue Liste muss ihn mit demselben Key enthalten');
      }
      // Unter Go sind keys_version und next_id selbst die Hochwassermarken
      // (PHP führt eigene, sein nächster Schritt läge darüber): Senken hieße,
      // eine Nummer ginge zweimal raus.
      if (version < st.keys_version) {
        throw stateError('seedRecipients', `Version ${version} unter der gespeicherten ${st.keys_version} – `
          + 'unter Go ist sie selbst die Hochwassermarke, ein Test darf sich auf Senken nicht verlassen');
      }
      if (nextId < st.next_id) {
        throw stateError('seedRecipients', `next_id ${nextId} unter dem gespeicherten ${st.next_id} – `
          + 'unter Go ist es selbst die Hochwassermarke, ein Test darf sich auf Senken nicht verlassen');
      }
      st.recipients = list;
      st.keys_version = version;
      st.next_id = nextId;
    }),

    boxStatus: () => read(() => {
      const box = peekDoc('runtime.json')?.box;
      if (!box) return null;
      const seen = new Set();
      const pairs = [];
      for (const [name, value] of box.fields) {
        if (seen.has(name)) continue;
        seen.add(name);
        pairs.push([name, value]);
      }
      // Object.fromEntries legt jedes Feld als eigenes an (auch "__proto__").
      // seen_at und via_bridge setzt der Server, remote_addr gehört nicht
      // dazu - auch nicht, wenn die Box ein Feld so nennt (PHP überschreibt
      // es mit der Absender-Adresse, der Leser lässt es weg).
      const own = new Set(['seen_at', 'via_bridge', 'remote_addr']);
      const fields = Object.fromEntries(pairs.filter(([name]) => !own.has(name)));
      return Object.assign(fields, { seen_at: box.seen_at, via_bridge: box.via_bridge });
    }),
    recipients: () => read(() => {
      const st = peekDoc('state.json');
      return st ? { version: st.keys_version, keys: st.recipients } : { version: 0, keys: [] };
    }),
    recipient: (id) => read(() => peekDoc('state.json')?.recipients.find((k) => k.id === id) ?? null),
    listVersion: () => read(() => peekDoc('state.json')?.keys_version ?? 0),
    demo: () => read(() => peekDoc('state.json')?.demo ?? { enabled: false }),
    command: () => read(() => peekDoc('state.json')?.commands.current ?? null),
    watcher: () => read(() => {
      const watcher = peekDoc('runtime.json')?.watcher;
      return watcher ? { seen_at: watcher.seen_at, bark_failed_at: watcher.bark_failed_at } : null;
    }),
    marks: () => read(() => deepCopy((peekDoc('runtime.json') ?? emptyRuntime()).watch)),
    log: () => read(() => historyNames.flatMap(historyEntries)),
    muteFailures: () => read(() => Object.values(peekDoc('runtime.json')?.throttle.mute ?? {})
      .reduce((sum, who) => sum + Number(who.count ?? 0), 0)),
    notifyStored: () => read(() => {
      const notify = peekDoc('state.json')?.notify;
      return { global: notify?.global ?? {}, types: notify?.types ?? {} };
    }),
  };

  // ---- backend.storage ----------------------------------------------------
  const BLOCKED = { selftest: ['selftest.tmp'], durable: ['state.json.tmp'], volatile: ['runtime.json.tmp', 'sessions.json.tmp'] };
  const asideName = (stem) => join(asideDir, `${stem}-${++asideSeq}`);
  const rmBusy = (path) => rmSync(path, { recursive: true, force: true, maxRetries: 20, retryDelay: 20 });
  // history.jsonl unter einem neuen, nie benutzten Namen nach beiseite/ -
  // ein Umbenennen überschreibt dort also nie etwas.
  const takeHistoryPiece = async () => {
    const piece = asideName('history-stueck');
    await retryBusy(() => renameSync(file('history.jsonl'), piece));
    historyPieces.push(piece);
  };

  // Ein nicht leerer Ordner an name. Schreibt der Server gerade genau dort,
  // gewinnt der Ordner beim nächsten Versuch (sein Sichern scheitert ohnehin).
  const block = async (name) => {
    if (!saved.has(name)) saved.set(name, isFile(file(name)) ? readFileSync(file(name)) : null);
    for (let i = 0; ; i++) {
      rmBusy(file(name));
      try {
        mkdirSync(file(name));
        break;
      } catch (error) {
        if (error.code !== 'EEXIST' || i >= 50) throw error;
        await sleep(10);
      }
    }
    writeFileSync(join(file(name), 'belegt'), 'x');
  };
  const unblock = (name) => {
    rmBusy(file(name));
    const before = saved.get(name);
    saved.delete(name);
    if (before) writeFileSync(file(name), before);
  };

  // Verlauf: Keine Zeile darf verloren gehen, auch wenn der Server genau
  // zwischen zwei Schritten anhängt. fail legt jede history.jsonl, die es
  // vorfindet, als neues Stück beiseite (auch eine, die der Server zwischen
  // Umbenennen und Ordner anlegt). repair fügt die Stücke in ihrer
  // Reihenfolge zusammen und legt das Ergebnis per Verweis (link) an den
  // Namen - ein Verweis ersetzt nie eine vorhandene Datei. Hat der Server in
  // der Lücke schon wieder eine angelegt, wird auch sie ein Stück (sie ist
  // jünger, kommt also ans Ende), und der Versuch beginnt von vorn. Danach
  // steht jede je geschriebene Zeile in history.jsonl, in ihrer Reihenfolge.
  const failHistory = async () => {
    mkdirSync(asideDir, { recursive: true });
    for (let i = 0; ; i++) {
      if (isFile(file('history.jsonl'))) await takeHistoryPiece();
      try {
        mkdirSync(file('history.jsonl'));
        break;
      } catch (error) {
        if (error.code !== 'EEXIST' || i >= 50) throw error;
      }
    }
    writeFileSync(join(file('history.jsonl'), 'belegt'), 'x');
  };
  const repairHistory = async () => {
    const gone = asideName('history-ordner');
    await retryBusy(() => renameSync(file('history.jsonl'), gone));
    for (let i = 0; historyPieces.length > 0; i++) {
      const merged = asideName('history-zusammen');
      writeFileSync(merged, historyPieces.map((piece) => {
        const text = readFileSync(piece, 'utf8');
        return text && !text.endsWith('\n') ? `${text}\n` : text;
      }).join(''));
      try {
        linkSync(merged, file('history.jsonl'));
        rmBusy(merged);
        break;
      } catch (error) {
        rmBusy(merged);
        if (error.code !== 'EEXIST' || i >= 50) throw error;
        await takeHistoryPiece();
      }
    }
    for (const piece of historyPieces) rmBusy(piece);
    historyPieces = [];
    rmBusy(gone);
  };

  const storage = {
    fail: (what) => serial(async () => {
      if (failed.has(what)) return;
      if (what === 'history') await failHistory();
      else for (const name of BLOCKED[what]) await block(name);
      failed.add(what);
    }),
    repair: (what) => serial(async () => {
      if (!failed.has(what)) return;
      if (what === 'history') {
        await repairHistory();
      } else {
        for (const name of BLOCKED[what]) unblock(name);
      }
      failed.delete(what);
    }),
  };

  // ---- backend.files (nur go-*.test.mjs) ----------------------------------
  // Roh auf den Datenordner; der Server liest seine Dateien beim Start, also
  // zwischen stopServer() und startServer() benutzen. Jeder Name geht durch
  // checkFileName (genau eine Schreibweise je Datei - kein Umweg über
  // Groß/Klein, Kurznamen oder Datenströme), jede *.toml beim Schreiben durch
  // den Wächter.
  const checkName = (name) => {
    checkFileName(name);
    return file(name);
  };
  // Eine kaputte Konfiguration: die Zeile des Prüfstands, dann Unsinn - geht
  // durch den Wächter wie jeder andere Text (der Parser lehnt "{kaputt" ab:
  // eine Inline-Tabelle ohne Schlüssel).
  const corruptText = (name) => (name.endsWith('.toml') ? `bark_host = "${bark.base}"\n{kaputt\n` : '{kaputt');
  const files = {
    dir: stateDir,
    path: checkName,
    list: () => readdirSync(stateDir).sort(),
    exists: (name) => existsSync(checkName(name)),
    // JSON (bei .jsonl eine Liste der Zeilen); null, wenn die Datei fehlt.
    read(name) {
      const path = checkName(name);
      if (!isFile(path)) return null;
      const raw = readFileSync(path, 'utf8');
      return name.endsWith('.jsonl') ? raw.split('\n').filter((l) => l.trim()).map((l) => JSON.parse(l)) : JSON.parse(raw);
    },
    text: (name) => (isFile(checkName(name)) ? readFileSync(checkName(name), 'utf8') : null),
    // Text wie gegeben, alles andere als JSON. Eine *.toml (etwa
    // config.last-good.toml, mit der der Server startet, wenn config.toml
    // nicht ladbar ist) erst nach dem Wächter über jede Konfiguration.
    write(name, value) {
      const path = checkName(name);
      const text = typeof value === 'string' ? value : `${JSON.stringify(value, null, 2)}\n`;
      if (name.endsWith('.toml')) guardConfigText(text, `backend.files.write(${name})`);
      writeFileSync(path, text);
      return value;
    },
    corrupt(name) {
      const path = checkName(name);
      const text = corruptText(name);
      if (name.endsWith('.toml')) guardConfigText(text, `backend.files.corrupt(${name})`);
      writeFileSync(path, text);
    },
    remove: (name) => rmBusy(checkName(name)),
  };

  // ---- Start --------------------------------------------------------------
  writeConfig({});
  try {
    await launch();
  } catch (error) {
    process.off('exit', killOnExit);
    await kill();
    throw error;
  }

  return {
    kind: 'go',
    base,
    dataDir: stateDir,
    state,
    storage,
    files,
    words: { configFile: 'config.toml' },
    serverLog: () => log,
    cli: testCli,
    health,
    startServer: (opts = {}) => serial(async () => {
      if (alive) return;
      expectUnhealthy = Boolean(opts.expectUnhealthy);
      await launch();
    }),
    stopServer: () => serial(halt),
    // Ohne Vorwarnung beenden (wie ein Absturz) - nichts wird aufgeräumt.
    hardKill: () => serial(kill),
    // Konfiguration = Standard des Prüfstands + overrides (nicht aufsummiert);
    // null entfernt einen Schlüssel (es gilt der Standard des Servers);
    // state_dir, listen, control_socket nicht. Erst der Wächter, dann
    // schreiben, dann fw-alarm reload (bzw. check-config, steht der Server).
    configure: (overrides = {}) => serial(async () => {
      writeConfig(overrides);
      const r = alive ? await cli(['reload'], { timeoutMs: 20000 }) : await cli(['check-config']);
      configBroken = r.code !== 0;
      if (configBroken) {
        throw new Error(`configure: der Server lehnt die Konfiguration ab (${r.code}): ${r.stderr.trim() || r.stdout.trim()}`);
      }
    }),
    // Der Text, den configure(overrides) schriebe - Vorlage für writeRawConfig.
    configText: (overrides = {}) => configText(overrides),
    // config.toml genau so schreiben wie gegeben (ein Tippfehler von Hand) und
    // warten, bis der Server sie beurteilt hat; liefert, was health dann sagt.
    // Erst der Wächter (für jeden Text, gültig oder kaputt). Dann sagt
    // check-config voraus, ob der Parser ihn annimmt - nur, um zu wissen,
    // worauf zu warten ist; eine gültige Fassung muss zudem state_dir, listen
    // und control_socket des Prüfstands behalten. Gewartet wird auf das
    // Urteil des Prüfers (er übernimmt eine Datei, die 2 s unverändert blieb):
    //   gültig: bis config.last-good.toml genau diese Bytes hält (die Kopie
    //           schreibt er nur aus einer übernommenen Fassung) UND health
    //           config_ok=1 sagt; gilt sie schon, sofort;
    //   kaputt: bis health config_ok=0 sagt (kaputt nach kaputt: sofort - der
    //           Hinweis kann dann noch den alten Grund nennen).
    // Nach 10 s ohne dieses Urteil wirft er mit der letzten Ausgabe von health
    // (auch, wenn der Server anders urteilt als vorhergesagt).
    writeRawConfig: (text) => serial(async () => {
      guardConfigText(text, 'writeRawConfig');
      const valid = await parserAccepts(text);
      const lines = new Set(text.split(/\r?\n/).map((line) => line.trim()));
      const lost = fixedLines.filter((line) => !lines.has(line));
      if (valid && lost.length) {
        throw new Error(`writeRawConfig: eine gültige Konfiguration ohne die Zeilen des Prüfstands ${lost.join(' | ')} `
          + '(backend.configText() liefert sie)');
      }
      const lastGood = () => (isFile(lastGoodPath) ? readFileSync(lastGoodPath, 'utf8') : null);
      const judged = (h) => (valid ? h.config_ok === '1' && lastGood() === text : h.config_ok === '0');
      writeFileSync(configPath, text);
      configBroken = !valid;
      if (!alive) return health();
      const end = Date.now() + RAW_CONFIG_WAIT_MS;
      for (;;) {
        const h = await health();
        if (judged(h)) return h;
        if (Date.now() > end) {
          throw new Error(`writeRawConfig: nach ${RAW_CONFIG_WAIT_MS / 1000} s kein Urteil des Servers `
            + `(erwartet config_ok=${valid ? '1 und die Kopie config.last-good.toml mit genau diesem Text' : '0'}):\n${h.text}${h.stderr}`);
        }
        await sleep(100);
      }
    }),
    // Ein voller Prüf-Lauf im laufenden Server; liefert seine Ausgabe.
    runWatchdog: () => serial(async () => {
      const r = await cli(['check'], { timeoutMs: 200000 });
      if (r.code !== 0) throw new Error(`Wächter-Lauf gescheitert (${r.code}): ${r.stdout}${r.stderr}`);
      return r.stdout.trim();
    }),
    // Zurück auf Anfang: Server an, Datenordner leer, Standard-Konfiguration,
    // nichts gestört.
    // Scheitert das Anhalten, ist der Prozess trotzdem fort, der nächste Test
    // bekommt einen sauberen Server - und dieser Aufruf wirft danach.
    reset: () => serial(async () => {
      let failure = null;
      try {
        await halt();
      } catch (error) {
        failure = error;
        await kill();
      }
      for (const name of readdirSync(stateDir)) {
        rmSync(join(stateDir, name), { recursive: true, force: true, maxRetries: 10, retryDelay: 50 });
      }
      rmSync(asideDir, { recursive: true, force: true, maxRetries: 10, retryDelay: 50 });
      failed.clear();
      saved.clear();
      historyPieces = [];
      expectUnhealthy = false;
      configBroken = false;
      writeConfig({});
      await launch();
      if (failure) throw failure;
    }),
    stop: () => serial(async () => {
      process.off('exit', killOnExit);
      try {
        await halt();
      } finally {
        await kill();
      }
    }),
  };
}
