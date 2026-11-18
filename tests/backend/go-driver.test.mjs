// Der Treiber des Prüfstands für den Go-Server (harness-go.mjs): Starten,
// Anhalten, hartes Beenden, Konfiguration, Zurücksetzen, Zustand über
// Anhalten → Dateien ändern → Starten, Sichern scheitern lassen. Nur Go -
// diese Datei prüft den Prüfstand selbst, damit jeder spätere Test auf ihm
// stehen kann. backend.files (der rohe Datenordner) nur hier und in den
// übrigen go-*.test.mjs.
import assert from 'node:assert/strict';
import { readFileSync, readdirSync, statSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:http';
import { join } from 'node:path';
import { after, before, beforeEach, describe, it } from 'node:test';
import {
  BACKEND_KIND, COMMAND_FIELDS, MARK_KINDS, RECIPIENT_FIELDS, now, request, session, sha256, startBackend,
} from './harness.mjs';

const near = (actual, expected, slack = 5) => Math.abs(actual - expected) <= slack;
// Die Absagen des Wächters über jede Konfiguration.
const ONE_HOST_LINE = /muss genau eine Zeile bark_host = "http:\/\/127\.0\.0\.1:<port>" enthalten – sonst sendete der Server an das echte Bark/;
const TRIPLE_QUOTES = /dreifache Anführungszeichen/;
const BAD_NAME = /ist kein erlaubter Dateiname/;
// "JJJJ-MM-TT HH:MM" in Berlin, seconds Sekunden ab jetzt (die Form von
// send_guard_until).
function berlinMinute(seconds) {
  const parts = Object.fromEntries(new Intl.DateTimeFormat('en-GB', {
    timeZone: 'Europe/Berlin', year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hourCycle: 'h23',
  }).formatToParts(new Date(Date.now() + seconds * 1000)).map((p) => [p.type, p.value]));
  return `${parts.year}-${parts.month}-${parts.day} ${parts.hour}:${parts.minute}`;
}
const isDir = (path) => {
  try { return statSync(path).isDirectory(); } catch { return false; }
};

describe('Treiber für den Go-Server', { skip: BACKEND_KIND !== 'go' && 'nur Go' }, () => {
  let backend;
  before(async () => { backend = await startBackend(); });
  after(async () => { await backend?.stop(); });
  beforeEach(async () => { await backend.reset(); });

  const manifest = () => request(`${backend.base}/manifest.json`);
  // Die bark_host-Zeile des Prüfstands (zeigt auf die Bark-Attrappe).
  const hostLine = () => `bark_host = "${backend.bark.base}"`;
  // config.toml des Prüfstands, wie sie gerade auf der Platte liegt.
  const configOnDisk = () => readFileSync(join(backend.root, 'config.toml'), 'utf8');
  // Ändert ein Dokument des Servers von Hand, während er steht.
  async function editWhileStopped(name, fn) {
    await backend.stopServer();
    const doc = backend.files.read(name);
    fn(doc);
    backend.files.write(name, doc);
    await backend.startServer();
  }
  // Laufzeitdaten anlegen lassen (der Treiber schreibt sie vollständig), dann
  // von Hand ergänzen.
  async function editRuntime(fn) {
    await backend.state.setMarks({ last_run_at: now() });
    await editWhileStopped('runtime.json', fn);
  }

  describe('Starten und Anhalten', () => {
    it('nach reset() antwortet /manifest.json und fw-alarm health meldet gesund', async () => {
      const r = await manifest();
      assert.equal(r.status, 200);
      const h = await backend.health();
      assert.equal(h.ok, true, h.text);
      assert.equal(h.running, '1');
      assert.equal(h.state, 'normal');
      assert.equal(h.save_ok, '1');
      assert.equal(h.config_ok, '1');
      assert.ok(backend.files.exists('state.json'), 'der frische Start sichert den leeren Zustand');
      assert.equal(backend.kind, 'go');
      assert.equal(backend.words.configFile, 'config.toml');
      assert.equal(backend.dataDir, backend.files.dir);
    });

    it('stopServer() und startServer(): dazwischen fort, danach unter derselben Adresse zurück', async () => {
      const base = backend.base;
      await backend.stopServer();
      await assert.rejects(manifest());
      const down = await backend.health();
      assert.equal(down.ok, false);
      assert.equal(down.running, '0');
      await backend.startServer();
      assert.equal(backend.base, base);
      assert.equal((await manifest()).status, 200);
      assert.equal((await backend.health()).ok, true);
    });

    it('hardKill() und startServer(): der Server kommt wieder (Sperre frei, verwaister Steuer-Socket ersetzt)', async () => {
      await backend.hardKill();
      await assert.rejects(manifest());
      await backend.startServer();
      assert.equal((await manifest()).status, 200);
      assert.equal((await backend.health()).ok, true);
    });

    it('ein zweites serve auf demselben Datenordner bricht mit Rückgabe ≠ 0 ab, der erste läuft weiter', async () => {
      const second = await backend.cli(['serve'], { timeoutMs: 15000 });
      assert.equal(second.code, 1, `${second.stdout}${second.stderr}`);
      assert.match(second.stderr, /anderes fw-alarm arbeitet schon in diesem Datenordner/);
      assert.equal((await manifest()).status, 200);
      assert.equal((await backend.health()).ok, true);
    });

    it('50 Anfragen über 10 Neustarts: keine scheitert an einer toten Verbindung', async () => {
      for (let round = 0; round < 10; round++) {
        for (let i = 0; i < 5; i++) assert.equal((await manifest()).status, 200, `Runde ${round}, Anfrage ${i}`);
        await backend.stopServer();
        await backend.startServer();
      }
    });
  });

  describe('Konfiguration', () => {
    it('configure() gilt sofort; ein kaputtes writeRawConfig zeigt config_ok=0, das nächste configure() heilt', async () => {
      await backend.configure({ offline_after_seconds: 777 });
      assert.match(backend.files.text('config.last-good.toml'), /^offline_after_seconds = 777$/m,
        'der Server hat die neue Fassung übernommen (Kopie der letzten gültigen)');

      const broken = await backend.writeRawConfig(`${hostLine()}\noffline_after_seconds = \n`);
      assert.equal(broken.config_ok, '0', broken.text);
      assert.ok(broken.hints.some((h) => /fehlerhaft/.test(h)), broken.text);
      assert.match(backend.files.text('config.last-good.toml'), /^offline_after_seconds = 777$/m, 'die letzte gute gilt weiter');

      await backend.configure({ offline_after_seconds: 778 });
      const healed = await backend.health();
      assert.equal(healed.config_ok, '1', healed.text);
      assert.equal(healed.ok, true);
      assert.match(backend.files.text('config.last-good.toml'), /^offline_after_seconds = 778$/m);
    });

    it('configure() wirft bei einer Fassung, die der Server ablehnt - und schreibt nie ein fremdes bark_host', async () => {
      await assert.rejects(backend.configure({ gibt_es_nicht: 1 }), /lehnt die Konfiguration ab.*unbekannter Schlüssel/s);
      const judged = await backend.health();
      assert.equal(judged.config_ok, '0', 'reload hat sie sofort als fehlerhaft beurteilt');
      assert.equal(judged.state, 'normal', 'es gilt die letzte gute');
      await backend.configure({});
      assert.equal((await backend.health()).ok, true);
      await assert.rejects(backend.configure({ bark_host: 'https://example.invalid' }), ONE_HOST_LINE);
      await assert.rejects(backend.configure({ bark_host: null }), ONE_HOST_LINE);
      await assert.rejects(backend.writeRawConfig('bark_key_status = "x"\n'), ONE_HOST_LINE);
      const withoutPort = backend.configText().split('\n').filter((line) => !line.startsWith('listen =')).join('\n');
      await assert.rejects(backend.writeRawConfig(withoutPort), /ohne die Zeilen des Prüfstands listen = /);
      assert.equal((await backend.health()).ok, true);
      assert.match(backend.files.text('config.last-good.toml'), new RegExp(`^bark_host = "${backend.bark.base}"$`, 'm'));
    });

    it('configure() lehnt state_dir, listen und control_socket ab - der Treiber hinge sonst am alten Ordner', async () => {
      for (const key of ['state_dir', 'listen', 'control_socket']) {
        await assert.rejects(backend.configure({ [key]: 'x' }), /state_dir, listen und control_socket/, key);
      }
      assert.equal((await backend.health()).ok, true);
    });

    it('ein von Hand behobener Tippfehler: writeRawConfig mit der gültigen Fassung macht config_ok wieder 1', async () => {
      const good = backend.configText({ offline_after_seconds: 555 });
      const typo = await backend.writeRawConfig(good.replace('offline_after_seconds = 555', 'offline_after_seconds = 555x'));
      assert.equal(typo.config_ok, '0', typo.text);
      const fixed = await backend.writeRawConfig(good);
      assert.equal(fixed.config_ok, '1', fixed.text);
      assert.equal(backend.files.text('config.last-good.toml'), good, 'zurück erst, wenn der Server genau sie übernommen hat');
      await backend.stopServer();
      await backend.startServer();
      assert.equal((await backend.health()).ok, true, 'ein Neustart mit der von Hand geschriebenen Fassung');
    });

    it('writeRawConfig mit der geltenden Fassung kehrt sofort zurück', async () => {
      const started = Date.now();
      const same = await backend.writeRawConfig(backend.configText());
      assert.equal(same.config_ok, '1', same.text);
      assert.ok(Date.now() - started < 1500, `${Date.now() - started} ms`);
    });
  });

  // Eine Konfiguration, die der Server liest, darf nie auf einen fremden
  // Bark-Server zeigen - über keinen Weg des Treibers. Fremd ist hier immer
  // example.invalid (nie ein echter Name).
  describe('Wächter über jede Konfiguration', () => {
    // Die Fassung des Prüfstands ohne ihre bark_host-Zeile.
    const withoutHost = () => backend.configText().split('\n').filter((line) => !line.startsWith('bark_host =')).join('\n');
    // Nimmt der Parser des Servers diesen Text an? (check-config auf einer Kopie)
    const parserAccepts = async (text, name = 'probe-test.toml') => {
      const path = join(backend.root, name);
      writeFileSync(path, text);
      return (await backend.cli(['check-config', '--config', path])).code === 0;
    };
    // Weder geschrieben noch übernommen: es gilt weiter die Fassung des Prüfstands.
    async function unchanged() {
      const h = await backend.health();
      assert.equal(h.ok, true, h.text);
      assert.match(backend.files.text('config.last-good.toml'), new RegExp(`^bark_host = "${backend.bark.base}"$`, 'm'));
    }

    it('"""…""" und \'\'\'…\'\'\' als bark_host: abgelehnt, obwohl der Parser sie annimmt', async () => {
      for (const quote of ['"""', "'''"]) {
        const text = `${withoutHost()}bark_host = ${quote}https://example.invalid${quote}\n`;
        assert.equal(await parserAccepts(text), true, `${quote}: der Parser nimmt sie an`);
        await assert.rejects(backend.writeRawConfig(text), TRIPLE_QUOTES, quote);
      }
      await unchanged();
    });

    it('eine bark_host-Zeile in einem mehrzeiligen Text ersetzt den Schlüssel nicht: abgelehnt', async () => {
      const text = `${withoutHost()}bark_alarm_body = """\nbark_host = "${backend.bark.base}"\n"""\n`;
      assert.equal(await parserAccepts(text), true);
      await assert.rejects(backend.writeRawConfig(text), TRIPLE_QUOTES);
      await unchanged();
    });

    it('bark_host in anderer Schreibweise (Anführungszeichen um den Schlüssel, ohne Leerzeichen): abgelehnt', async () => {
      for (const line of ['"bark_host" = "https://example.invalid"', 'bark_host="https://example.invalid"',
        `bark_host="${backend.bark.base}"`, `  bark_host = "${backend.bark.base}"`]) {
        const text = `${withoutHost()}${line}\n`;
        assert.equal(await parserAccepts(text), true, line);
        await assert.rejects(backend.writeRawConfig(text), ONE_HOST_LINE, line);
      }
      await unchanged();
    });

    it('ein gültiger Anfang ohne bark_host-Zeile: abgelehnt (der Server nähme den echten Bark-Server)', async () => {
      const prefix = `${withoutHost().split('\n').slice(0, 4).join('\n')}\n`;
      assert.equal(await parserAccepts(prefix), true, prefix);
      await assert.rejects(backend.writeRawConfig(prefix), ONE_HOST_LINE);
      await unchanged();
    });

    it('die Fassung des Prüfstands beginnt mit der bark_host-Zeile: jeder Anfang bis zu einem Zeilenende trägt sie', async () => {
      const lines = backend.configText().split('\n');
      assert.equal(lines[0], hostLine(), 'die erste Zeile');
      let valid = 0;
      for (let n = 1; n <= lines.length; n++) {
        const prefix = `${lines.slice(0, n).join('\n')}\n`;
        assert.equal(prefix.split('\n').filter((line) => line.includes('bark_host')).length, 1, `Anfang mit ${n} Zeilen`);
        if (await parserAccepts(prefix)) valid += 1;
      }
      assert.ok(valid > 0);
      // Ein abgeschnittener Anfang geht durch den Wächter und wird geschrieben
      // (ab vier Zeilen trägt er auch state_dir, listen und control_socket).
      const cut = `${lines.slice(0, 4).join('\n')}\n`;
      const h = await backend.writeRawConfig(cut);
      assert.equal(configOnDisk(), cut);
      assert.equal(h.config_ok, (await parserAccepts(cut)) ? '1' : '0', h.text);
    });

    it('die Grenze der Probe-Sperre: heute kaputt, in einer Minute gültig - mit fremdem Host abgelehnt, nichts geschrieben', async () => {
      const before = configOnDisk();
      const text = `bark_host = "https://example.invalid"\nsend_guard_keys = ["probekey01"]\n`
        + `send_guard_until = "${berlinMinute(3 * 3600 + 120)}"\n${withoutHost()}`;
      assert.equal(await parserAccepts(text), false, 'heute: mehr als 3 h in der Zukunft');
      await assert.rejects(backend.writeRawConfig(text), ONE_HOST_LINE);
      assert.equal(configOnDisk(), before, 'config.toml unverändert');
      await unchanged();
    });

    it('configure ohne bark_host an der Grenze der Probe-Sperre: der Wächter wirft, nicht erst der Server', async () => {
      const before = configOnDisk();
      await assert.rejects(backend.configure({
        bark_host: null, send_guard_keys: ['probekey01'], send_guard_until: berlinMinute(3 * 3600 + 120),
      }), ONE_HOST_LINE);
      assert.equal(configOnDisk(), before, 'config.toml unverändert');
      await unchanged();
    });

    it('kein TOML, ein leerer Text: ohne die bark_host-Zeile abgelehnt - mit ihr geschrieben, der Server lehnt ab', async () => {
      const before = configOnDisk();
      await assert.rejects(backend.writeRawConfig('{{{ kein toml\n'), ONE_HOST_LINE);
      await assert.rejects(backend.writeRawConfig(''), ONE_HOST_LINE);
      assert.equal(configOnDisk(), before);
      await unchanged();
      const garbage = `${hostLine()}\n{{{ kein toml\n`;
      const h = await backend.writeRawConfig(garbage);
      assert.equal(configOnDisk(), garbage);
      assert.equal(h.config_ok, '0', h.text);
    });

    it('backend.files.write auf config.last-good.toml und config.toml: derselbe Wächter, auch für kaputte Texte', async () => {
      await backend.stopServer();
      const foreign = `${withoutHost()}bark_host = "https://example.invalid"\n`;
      const before = backend.files.text('config.last-good.toml');
      for (const name of ['config.last-good.toml', 'config.toml']) {
        assert.throws(() => backend.files.write(name, foreign), ONE_HOST_LINE, name);
        assert.throws(() => backend.files.write(name, 'bark_host = "https://example.invalid"\n= kaputt\n'), ONE_HOST_LINE, name);
        assert.throws(() => backend.files.write(name, `${withoutHost()}x = """y"""\n`), TRIPLE_QUOTES, name);
      }
      assert.equal(backend.files.text('config.last-good.toml'), before);
      assert.equal(backend.files.exists('config.toml'), false);
      backend.files.write('config.last-good.toml', backend.configText());
      await backend.startServer();
      await unchanged();
    });

    it('backend.files.corrupt auf einer Konfiguration: die bark_host-Zeile, dann Unsinn - kaputt, nie fremd', async () => {
      await backend.stopServer();
      backend.files.corrupt('config.last-good.toml');
      const text = backend.files.text('config.last-good.toml');
      assert.equal(text.split('\n')[0], hostLine());
      assert.equal(text.split('\n').filter((line) => line.includes('bark_host')).length, 1);
      assert.equal(await parserAccepts(text), false, 'der Parser lehnt sie ab');
      backend.files.write('config.last-good.toml', backend.configText());
      await backend.startServer();
    });

    it('Dateinamen nur in einer Schreibweise: Großbuchstaben, Kurznamen, Datenströme, Punkt oder Leerzeichen am Ende abgelehnt', async () => {
      await backend.stopServer();
      const before = backend.files.text('config.last-good.toml');
      const aliases = ['config.last-good.TOML', 'CONFIG~1.TOM', 'config~1.tom', 'config.last-good.toml::$DATA',
        'config.toml.', 'config.toml '];
      for (const name of aliases) {
        assert.throws(() => backend.files.write(name, backend.configText()), BAD_NAME, `write ${name}`);
        for (const fn of ['read', 'text', 'exists', 'remove', 'corrupt', 'path']) {
          assert.throws(() => backend.files[fn](name), BAD_NAME, `${fn} ${name}`);
        }
      }
      assert.equal(backend.files.text('config.last-good.toml'), before);
      await backend.startServer();
      await unchanged();
    });

    it('erlaubt: die Fassung des Prüfstands, bark_host = "" (sendet nichts) und kaputte Texte mit der Zeile', async () => {
      const empty = `${withoutHost()}bark_host = ""\n`;
      const applied = await backend.writeRawConfig(empty);
      assert.equal(applied.config_ok, '1', applied.text);
      assert.equal(backend.files.text('config.last-good.toml'), empty);
      const broken = await backend.writeRawConfig(`${hostLine()}\nbark_alarm_title = "offen\n`);
      assert.equal(broken.config_ok, '0', 'ein kaputter Text mit der Zeile geht durch - der Server übernimmt ihn nicht');
      assert.equal(backend.files.text('config.last-good.toml'), empty);
      await backend.configure({ bark_host: '' });
      await backend.configure({});
      await unchanged();
    });

    it('cli: eigene --config/--last-good (jede Schreibweise) nur für check-config', async () => {
      const other = join(backend.root, 'gibt-es-nicht.toml');
      for (const command of ['serve', 'reload']) {
        for (const flag of ['config', 'last-good']) {
          for (const args of [[`--${flag}`, other], [`-${flag}`, other], [`--${flag}=${other}`], [`-${flag}=${other}`]]) {
            await assert.rejects(backend.cli([command, ...args]), /eigene --config\/--last-good/, `${command} ${args.join(' ')}`);
          }
        }
      }
      const broken = join(backend.root, 'kaputt-test.toml');
      writeFileSync(broken, `${hostLine()}\n= kaputt\n`);
      for (const args of [['--config', broken], [`-config=${broken}`]]) {
        assert.equal((await backend.cli(['check-config', ...args])).code, 1, args.join(' '));
      }
      assert.equal((await backend.cli(['check-config'])).code, 0, 'ohne Angabe die Fassung des Prüfstands');
      await unchanged();
    });
  });

  describe('Sichern scheitern lassen', () => {
    it('durable, volatile, selftest: ein nicht leerer Ordner an der Zwischendatei, repair entfernt ihn', async () => {
      const parts = { durable: ['state.json.tmp'], volatile: ['runtime.json.tmp', 'sessions.json.tmp'], selftest: ['selftest.tmp'] };
      for (const [what, names] of Object.entries(parts)) {
        await backend.storage.fail(what);
        await backend.storage.fail(what);   // ein zweites fail tut nichts
        for (const name of names) {
          const path = backend.files.path(name);
          assert.ok(isDir(path), `${what}: ${name} ist ein Ordner`);
          assert.ok(readdirSync(path).length > 0, `${what}: ${name} ist nicht leer`);
        }
        await backend.storage.repair(what);
        await backend.storage.repair(what); // repair ohne fail tut nichts
        for (const name of names) assert.equal(backend.files.exists(name), false, `${what}: ${name} ist fort`);
      }
    });

    it('der Ordner übersteht einen Neustart; der Server läuft gestört weiter, repair macht ihn wieder gesund', async () => {
      await backend.storage.fail('durable');
      await backend.stopServer();
      await backend.startServer();
      assert.ok(isDir(backend.files.path('state.json.tmp')), 'der Start räumt nur Dateien fort, keinen Ordner');
      const h = await backend.health();
      assert.equal(h.state, 'normal');
      await backend.storage.repair('durable');
      assert.equal(backend.files.exists('state.json.tmp'), false);
    });

    it('durable: keys_add antwortet 500 code storage, health sagt save_ok=0 – nach repair und einer gelungenen Änderung save_ok=1', async () => {
      const admin = await session(backend);
      await backend.storage.fail('durable');
      const failed = await admin.post({ action: 'keys_add', label: 'Anna', key: 'annakey00001' });
      assert.equal(failed.http, 500, JSON.stringify(failed));
      assert.equal(failed.code, 'storage');
      assert.equal(failed.message, 'Fehler: Datenordner nicht beschreibbar (Rechte des Web-Benutzers auf dem Server prüfen).');
      let h = await backend.health();
      assert.equal(h.save_ok, '0', h.text);
      assert.equal(h.state, 'normal');
      await backend.storage.repair('durable');
      const added = await admin.post({ action: 'keys_add', label: 'Anna', key: 'annakey00001' });
      assert.equal(added.ok, true, JSON.stringify(added));
      h = await backend.health();
      assert.equal(h.save_ok, '1', h.text);
      assert.equal(h.ok, true, h.text);
      assert.deepEqual((await admin.keys()).keys.map((k) => k.label), ['Anna']);
    });

    it('history: die Datei wandert beiseite, ein Ordner tritt an ihre Stelle, repair bringt genau sie zurück', async () => {
      await backend.state.seedLog([{ event: 'key_added', label: 'K1', log_at: 1000 }]);
      const before = backend.files.text('history.jsonl');
      await backend.storage.fail('history');
      assert.ok(isDir(backend.files.path('history.jsonl')));
      assert.ok(readdirSync(backend.files.path('history.jsonl')).length > 0);
      await backend.storage.repair('history');
      assert.equal(backend.files.text('history.jsonl'), before);
      assert.deepEqual(await backend.state.log(), [{ event: 'key_added', label: 'K1', log_at: 1000 }]);
    });

    it('history ohne vorhandene Datei: repair lässt keine zurück', async () => {
      await backend.storage.fail('history');
      await backend.storage.repair('history');
      assert.equal(backend.files.exists('history.jsonl'), false);
    });

    it('history: was der Server während der Störung schreiben wollte, steht nach repair hinter dem Alten', async () => {
      const k1 = { event: 'key_added', label: 'K1', log_at: 1000 };
      await backend.state.seedLog([k1]);
      await backend.storage.fail('history');
      // Ein Eintrag in der outbox: Der Server hängt ihn beim Start an - das
      // scheitert am Ordner, er bleibt in der Warteschlange.
      const k2 = { event: 'key_added', label: 'K2', log_at: 2000, uid: 'ab'.repeat(16) };
      await editWhileStopped('state.json', (st) => { st.outbox = [k2]; });
      await backend.storage.repair('history');
      assert.deepEqual(await backend.state.log(), [k1, k2], 'flush hängt ihn an die zurückgeholte Datei an');
    });
  });

  describe('Zustand: anhalten, ändern, starten', () => {
    it('jeder Merker der Liste einmal gesetzt: der Server liest die Laufzeitdaten und bleibt gesund', async () => {
      const t = now();
      const values = {};
      let i = 0;
      for (const [name, kind] of MARK_KINDS) {
        i += 1;
        values[name] = kind === 'bool' ? true : kind === 'int' ? i : t - i;
      }
      await backend.state.setMarks(values);
      const marks = await backend.state.marks();
      for (const [name, value] of Object.entries(values)) assert.equal(marks[name], value, name);
      assert.equal((await backend.health()).ok, true);
      assert.deepEqual(backend.files.list().filter((n) => n.includes('.corrupt-')), []);

      await backend.state.ageMark('last_run_at', 100);
      assert.ok(near((await backend.state.marks()).last_run_at, now() - 100));
      await backend.state.setMarks(Object.fromEntries([...MARK_KINDS.keys()].map((name) => [name, null])));
      const cleared = await backend.state.marks();
      for (const [name, kind] of MARK_KINDS) assert.equal(cleared[name], kind === 'bool' ? false : 0, name);
    });

    it('Empfänger: fehlende Felder bekommen ihren Standard und ein Token, der Server übernimmt die Liste', async () => {
      await backend.state.seedRecipients([
        { id: 1, key: 'annakey00001', label: 'Anna' },
        { id: 3, key: 'bertkey00001', label: 'Bert', muted: true, muted_at: 5, mute_volume: 2, mute_token: 'BertsToken12' },
      ], { version: 7 });
      const list = await backend.state.recipients();
      assert.equal(list.version, 7);
      assert.deepEqual(list.keys.map((k) => k.id), [1, 3]);
      for (const k of list.keys) assert.deepEqual(Object.keys(k).sort(), [...RECIPIENT_FIELDS.keys()].sort());
      assert.match(list.keys[0].mute_token, /^[A-Za-z0-9]{16}$/);
      assert.equal(list.keys[1].mute_token, 'BertsToken12');
      assert.equal(await backend.state.listVersion(), 7);
      assert.equal((await backend.state.recipient(3)).mute_volume, 2);
      assert.equal(await backend.state.recipient(2), null);
      assert.equal(backend.files.read('state.json').next_id, 4);

      await backend.state.ageMute(3, 100);
      assert.ok(near((await backend.state.recipient(3)).muted_at, now() - 100));
      await assert.rejects(backend.state.ageMute(1, 5), /nicht stumm/);
      await backend.state.ageMute('all', 50);
      assert.ok(near((await backend.state.recipient(3)).muted_at, now() - 50));

      await backend.stopServer();
      await backend.startServer();
      assert.deepEqual(await backend.state.recipients(), { ...list, keys: [list.keys[0], { ...list.keys[1], muted_at: (await backend.state.recipient(3)).muted_at }] },
        'ein Neustart verliert nichts');
    });

    it('seedRecipients senkt unter Go weder die Version noch next_id (sie sind dort selbst die Hochwassermarken)', async () => {
      await backend.state.seedRecipients([{ id: 4, key: 'annakey00001', label: 'Anna' }], { version: 7, nextId: 9 });
      await assert.rejects(backend.state.seedRecipients([{ id: 1, key: 'annakey00001', label: 'Anna' }], { version: 6, nextId: 9 }),
        /Version 6 unter der gespeicherten 7/);
      await assert.rejects(backend.state.seedRecipients([{ id: 1, key: 'annakey00001', label: 'Anna' }], { version: 7 }),
        /next_id 2 unter dem gespeicherten 9/);
      await backend.state.seedRecipients([{ id: 1, key: 'bertkey00001', label: 'Bert' }], { version: 7, nextId: 9 });
      assert.equal((await backend.state.recipients()).keys[0].label, 'Bert');
    });

    it('der laufende Befehl: ohne ihn werfen setCommand und ageCommand, mit ihm ändern sie nur ihn', async () => {
      await assert.rejects(backend.state.setCommand({ acked_at: 1 }), /kein laufender Befehl/);
      await assert.rejects(backend.state.ageCommand('created', 1), /kein laufender Befehl/);
      const t = now();
      const command = Object.fromEntries([...COMMAND_FIELDS].map(([name, kind]) => [name, kind === 'string' ? '' : 0]));
      Object.assign(command, { id: 1, type: 'TEST', state: 'delivered', created_at: t - 60, created_by: 'admin', delivered_at: t - 50, delivered_to: 'fw-box-1' });
      await editWhileStopped('state.json', (st) => {
        st.commands.current = command;
        st.commands.next_id = 2;
      });
      await backend.state.setCommand({ acked_at: 12345, ack_result: 'executed', ack_message: 'x' });
      assert.equal((await backend.state.command()).acked_at, 12345);
      await backend.state.setCommand({ ack_message: null });
      assert.equal((await backend.state.command()).ack_message, '');
      await backend.state.ageCommand('delivered', 30);
      const stored = await backend.state.command();
      assert.ok(near(stored.delivered_at, now() - 30));
      assert.equal(stored.created_at, t - 60);
      await assert.rejects(backend.state.setCommand({ id: 5 }), /commands\.next_id/);
      assert.equal((await backend.state.command()).id, 1);
    });

    it('Status der Box und Boards: als Objekt mit den Namen von PHP, ohne remote_addr', async () => {
      assert.equal(await backend.state.boxStatus(), null);
      await assert.rejects(backend.state.ageBoxStatus(10), /noch kein Status/);
      await assert.rejects(backend.state.ageDevice('fw-box-1', 10), /nie gemeldet/);
      const t = now();
      await editRuntime((rt) => {
        rt.box = {
          seen_at: t, via_bridge: '1', remote_addr: '192.0.2.7',
          fields: [['device_id', 'fw-box-1'], ['rssi', '-61'], ['__proto__', 'x'], ['rssi', '-99'], ['remote_addr', 'gepusht']],
        };
        rt.devices = { 'fw-box-1': { seen_at: t, fw_build: 'test' } };
      });
      const stored = await backend.state.boxStatus();
      assert.equal(stored.device_id, 'fw-box-1');
      assert.equal(stored.rssi, '-61', 'das erste Feld eines Namens gilt');
      assert.equal(Object.hasOwn(stored, '__proto__'), true);
      assert.equal(stored.via_bridge, '1');
      assert.equal(stored.seen_at, t);
      assert.equal('remote_addr' in stored, false, 'auch ein gepushtes Feld dieses Namens nicht (PHP überschreibt es)');

      await backend.state.ageBoxStatus(200);
      assert.ok(near((await backend.state.boxStatus()).seen_at, now() - 200));
      await backend.state.ageDevice('fw-box-1', 900);
      assert.ok(near(backend.files.read('runtime.json').devices['fw-box-1'].seen_at, now() - 900));
      await assert.rejects(backend.state.ageDevice('bastel-1', 10), /nie gemeldet/);
    });

    it('Außen-Wächter, Sperrfrist und Bremse: herstellen, verschieben, lesen', async () => {
      assert.equal(await backend.state.watcher(), null);
      await assert.rejects(backend.state.ageWatcher(10), /nie gemeldet/);
      await backend.state.setWatcher({ seenAgo: 60, barkFailedAt: 5 });
      let seen = await backend.state.watcher();
      assert.ok(near(seen.seen_at, now() - 60));
      assert.equal(seen.bark_failed_at, 5);
      await backend.state.ageWatcher(10);
      seen = await backend.state.watcher();
      assert.ok(near(seen.seen_at, now() - 10));
      assert.equal(seen.bark_failed_at, 5, 'bark_failed_at bleibt');

      await assert.rejects(backend.state.ageRetry('box_offline', 60), /keine Sperrfrist/);
      await assert.rejects(backend.state.ageRetry('gibt_es_nicht', 60), /unbekannte Art/);
      await assert.rejects(backend.state.ageMuteThrottle(60), /keine Fehlversuche/);
      const t = now();
      await editRuntime((rt) => {
        rt.watch.retry_after = { box_offline: t + 900 };
        rt.throttle.mute = { '198.51.100.1': { count: 3, first_at: t } };
      });
      await backend.state.ageRetry('box_offline', 60);
      assert.ok(near(backend.files.read('runtime.json').watch.retry_after.box_offline, now() - 60));
      assert.equal(await backend.state.muteFailures(), 3);
      await backend.state.ageMuteThrottle(901);
      assert.ok(near(backend.files.read('runtime.json').throttle.mute['198.51.100.1'].first_at, now() - 901));
    });

    it('Sitzungen: ageSession findet die Sitzung über den SHA-256 des Cookie-Werts', async () => {
      await assert.rejects(backend.state.ageSession('boss925_dashboard=abc', 10), /keine Sitzung/);
      await assert.rejects(backend.state.ageSession('ohne-gleich', 10), /name=wert/);
      const t = now();
      await backend.stopServer();
      backend.files.write('sessions.json', {
        schema: 1,
        sessions: { [sha256('abc')]: { user: 'admin', role: 'admin', csrf: 'c', auth_stamp: 's', created_at: t, last_seen: t } },
      });
      await backend.startServer();
      await backend.state.ageSession('boss925_dashboard=abc', 3600);
      assert.ok(near(backend.files.read('sessions.json').sessions[sha256('abc')].last_seen, now() - 3600));
    });

    it('Verlauf: seedLog roh, log() älteste zuerst, ageLog nur Passendes, ohne Treffer nichts', async () => {
      const k1 = { event: 'key_added', label: 'K1', log_at: 1000 };
      const k2 = { event: 'key_added', label: 'K2', log_at: 2000 };
      const s3 = { event: 'setting_changed', label: 'x', log_at: 3000 };
      await backend.state.seedLog([k1], { rotated: true });
      await backend.state.seedLog([k2, s3]);
      assert.deepEqual(await backend.state.log(), [k1, k2, s3], 'wie gegeben: ohne uid, ohne eigenes log_at');

      await backend.state.ageLog(100, (e) => e.event === 'key_added');
      assert.deepEqual((await backend.state.log()).map((e) => e.log_at), [900, 1900, 3000]);
      await backend.state.ageLog(100, (e) => e.event === 'gibt_es_nicht');
      assert.deepEqual((await backend.state.log()).map((e) => e.log_at), [900, 1900, 3000]);

      await backend.state.seedLog([{ event: 'ohne_zeit' }]);
      await assert.rejects(backend.state.ageLog(1), /ohne log_at/);
      assert.deepEqual((await backend.state.log()).map((e) => e.log_at), [900, 1900, 3000, undefined], 'nichts geändert');
    });

    it('Demo-Zustand und Meldungen: gelesen wie gespeichert; ageDemo wirft, solange nie umgeschaltet', async () => {
      assert.equal((await backend.state.demo()).enabled, false);
      await assert.rejects(backend.state.ageDemo(10), /noch kein Demo-Zustand/);
      assert.deepEqual(await backend.state.notifyStored(), { global: {}, types: {} });
    });
  });

  describe('Fehler des Treibers fallen auf', () => {
    it('ein Dokument, das der Server ablehnt, lässt den Start scheitern - mit dem Grund', async () => {
      await backend.stopServer();
      backend.files.write('runtime.json', { schema: 1 });
      await assert.rejects(backend.startServer(), /nicht gesund.*laufzeit_verworfen=true.*beiseitegelegt: runtime\.corrupt-/s);
    });

    it('startServer({ expectUnhealthy: true }) startet nach Absicht Kaputtes ohne Prüfung', async () => {
      await backend.stopServer();
      backend.files.corrupt('sessions.json');
      await backend.startServer({ expectUnhealthy: true });
      assert.ok(backend.files.list().some((n) => n.startsWith('sessions.corrupt-')));
      assert.equal((await manifest()).status, 200);
    });

    it('state.json kaputt, Vorfassung da: der Start auf dem Rückfall wirft - mit expectUnhealthy startet er', async () => {
      await backend.stopServer();
      backend.files.write('state.prev.json', backend.files.text('state.json'));
      backend.files.corrupt('state.json');
      await assert.rejects(backend.startServer(), /nicht gesund.*start=rueckfall.*ruecksprung=true/s);
      await backend.stopServer();
      backend.files.corrupt('state.json');
      await backend.startServer({ expectUnhealthy: true });
      assert.equal(backend.files.list().filter((n) => n.startsWith('state.corrupt-')).length >= 1, true);
      assert.equal((await manifest()).status, 200);
    });

    it('runtime.json Unsinn: verworfen - der Start wirft, mit expectUnhealthy startet er', async () => {
      await backend.stopServer();
      backend.files.corrupt('runtime.json');
      await assert.rejects(backend.startServer(), /nicht gesund.*laufzeit_verworfen=true/s);
      await backend.stopServer();
      backend.files.corrupt('runtime.json');
      await backend.startServer({ expectUnhealthy: true });
      assert.equal((await manifest()).status, 200);
    });

    it('ein Neuanfang auf einem nicht leeren Datenordner fällt auf (state.json verschwunden)', async () => {
      await backend.stopServer();
      backend.files.remove('state.json');
      await assert.rejects(backend.startServer(), /nicht gesund.*start=neu auf einem nicht leeren Datenordner/s);
    });
  });

  describe('Anfragen des Prüfstands', () => {
    it('request() sendet connection: close', async () => {
      const seen = [];
      const server = createServer((req, res) => { seen.push(req.headers.connection); res.end('ok'); });
      await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
      try {
        const url = `http://127.0.0.1:${server.address().port}/`;
        await request(url);
        await request(url, { headers: { cookie: 'a=b' } });
        assert.deepEqual(seen, ['close', 'close']);
      } finally {
        server.closeAllConnections();
        await new Promise((resolve) => server.close(resolve));
      }
    });
  });
});
