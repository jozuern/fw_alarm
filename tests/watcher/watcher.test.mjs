// Tests für den Außen-Wächter (cloud_watcher/fw-alarm-watch.sh): Welche
// Bark-Texte gehen raus? curl und flock sind Attrappen im PATH - es gibt
// keinen Netzzugriff. Aufruf: node --test "tests/watcher/*.test.mjs"
import test, { after } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { delimiter, dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const script = join(here, '..', '..', 'cloud_watcher', 'fw-alarm-watch.sh').replace(/\\/g, '/');
const tmp = mkdtempSync(join(tmpdir(), 'fw-watch-'));
const bin = join(tmp, 'bin');
mkdirSync(bin);

// curl-Attrappe: liest die Konfiguration von stdin (curl -K -). Bark-Aufrufe
// (mit title=) landen im Log und werden angenommen; der alive-Aufruf
// antwortet je nach FAKE_ALIVE.
writeFileSync(join(bin, 'curl'), `#!/usr/bin/env bash
cfg=$(cat)
if grep -q '^data-urlencode = "title=' <<<"$cfg"; then
  grep -E '^data-urlencode = "(title|body)=' <<<"$cfg" | sed -E 's/^data-urlencode = "(.*)"$/\\1/' >> "$FAKE_LOG"
  printf '200'
  exit 0
fi
echo "alive" >> "$FAKE_LOG.calls"
if [ "$FAKE_ALIVE" = down ]; then
  exit 7
fi
nonce=$(sed -n 's/^data-urlencode = "nonce=\\(.*\\)"$/\\1/p' <<<"$cfg")
printf 'ok=1\\nnonce=%s\\nstored=1\\ncron_age_s=%s\\nbox_age_s=5\\ncron_configured=1\\nnas_send_failed=%s\\n\\n200' \\
  "$nonce" "\${FAKE_CRON_AGE:-60}" "\${FAKE_SENDFAIL:-0}"
`);
writeFileSync(join(bin, 'flock'), '#!/usr/bin/env bash\nexit 0\n');
chmodSync(join(bin, 'curl'), 0o755);
chmodSync(join(bin, 'flock'), 0o755);

after(() => rmSync(tmp, { recursive: true, force: true }));

let seq = 0;
// Eigene Umgebung je Test: Konfiguration, Zustandsordner, Log.
function makeWatcher(extraConf = '') {
  seq += 1;
  const dir = join(tmp, `w${seq}`);
  mkdirSync(join(dir, 'state'), { recursive: true });
  const conf = join(dir, 'watch.conf').replace(/\\/g, '/');
  writeFileSync(conf, 'NAS_ALIVE_URL="https://ziel.test/api/alive.php"\nWATCHER_TOKEN="geheim-token"\n'
    + 'BARK_KEY="statuskey0001"\nBARK_HOST="https://bark.test"\nFAIL_THRESHOLD=1\n' + extraConf);
  const log = join(dir, 'bark.log').replace(/\\/g, '/');
  return {
    dir,
    run(env = {}, pathPrefix = '') {
      const r = spawnSync('bash', [script], {
        encoding: 'utf8',
        env: {
          ...process.env, ...env, PATH: pathPrefix + bin + delimiter + process.env.PATH,
          FW_WATCH_CONF: conf, STATE_DIRECTORY: join(dir, 'state').replace(/\\/g, '/'), FAKE_LOG: log,
        },
      });
      return { code: r.status, out: r.stdout, err: r.stderr };
    },
    // Bisher gesendete Meldungen: [{title, body}, …]
    sent() {
      if (!existsSync(log)) return [];
      const lines = readFileSync(log, 'utf8').split('\n').filter((l) => l !== '');
      const out = [];
      for (let i = 0; i < lines.length; i += 2) {
        out.push({ title: lines[i].replace(/^title=/, ''), body: (lines[i + 1] || '').replace(/^body=/, '') });
      }
      return out;
    },
    aliveCalls: () => (existsSync(log + '.calls') ? readFileSync(log + '.calls', 'utf8').split('\n').filter(Boolean).length : 0),
  };
}
const SERVER = 'TARGET_NAME="Alarm-Server"\n';
// Wörter, die nur zum alten Aufbau passen (Wächter auf der Cloud-VM, Ziel NAS).
const OLD_PLACE = /NAS|DSM|Web Station|Cloud-VM|Standort/;

test('ohne TARGET_NAME heißt das Ziel weiter NAS', () => {
  const w = makeWatcher();
  const r = w.run({ FAKE_ALIVE: 'down' });
  assert.equal(r.code, 0, r.err);
  const [m] = w.sent();
  assert.equal(m.title, 'NAS NICHT ERREICHBAR');
  assert.match(m.body, /NAS antwortet von aussen seit ca\. 0 min nicht: keine Verbindung/);
});

test('nicht erreichbar und wieder da: Texte nennen TARGET_NAME', () => {
  const w = makeWatcher(SERVER);
  assert.equal(w.run({ FAKE_ALIVE: 'down' }).code, 0);
  let m = w.sent().at(-1);
  assert.equal(m.title, 'Alarm-Server NICHT ERREICHBAR');
  assert.match(m.body, /Alarm-Server antwortet von aussen seit ca\. 0 min nicht/);
  assert.match(m.body, /REAL ALARM ueber das Dashboard geht dann nicht/);
  assert.match(m.body, /Alarme der Box laufen unabhaengig davon direkt an Bark/);
  assert.doesNotMatch(m.title + m.body, OLD_PLACE);

  const up = w.run({ FAKE_ALIVE: 'up' });
  assert.equal(up.code, 0, up.err);
  assert.match(up.out, /^OK http=200 /);
  m = w.sent().at(-1);
  assert.equal(m.title, 'Alarm-Server wieder erreichbar');
  assert.match(m.body, /Alarm-Server antwortet von aussen wieder/);
  assert.doesNotMatch(m.title + m.body, OLD_PLACE);
  assert.equal(w.sent().length, 2);
});

test('Wächter auf dem Ziel läuft nicht / läuft wieder', () => {
  const w = makeWatcher(SERVER);
  assert.equal(w.run({ FAKE_ALIVE: 'up', FAKE_CRON_AGE: '4000' }).code, 0);
  let m = w.sent().at(-1);
  assert.equal(m.title, 'Alarm-Server-Waechter laeuft nicht');
  assert.match(m.body, /Der Waechter auf Alarm-Server \(check_offline\.php\) lief seit 67 min nicht mehr/);
  assert.match(m.body, /ein Ausfall der Box wuerde NICHT gemeldet/);
  assert.doesNotMatch(m.title + m.body, OLD_PLACE);

  assert.equal(w.run({ FAKE_ALIVE: 'up', FAKE_CRON_AGE: '30' }).code, 0);
  m = w.sent().at(-1);
  assert.equal(m.title, 'Alarm-Server-Waechter laeuft wieder');
  assert.doesNotMatch(m.title + m.body, OLD_PLACE);
});

test('Ziel kommt nicht zu Bark durch / wieder durch', () => {
  const w = makeWatcher(SERVER);
  assert.equal(w.run({ FAKE_ALIVE: 'up', FAKE_SENDFAIL: '1' }).code, 0);
  let m = w.sent().at(-1);
  assert.equal(m.title, 'Alarm-Server kann Bark nicht erreichen');
  assert.match(m.body, /Manueller REAL ALARM gefaehrdet/);
  assert.match(m.body, /Erreicht Alarm-Server Bark nicht/);
  assert.doesNotMatch(m.title + m.body, OLD_PLACE);

  assert.equal(w.run({ FAKE_ALIVE: 'up', FAKE_SENDFAIL: '0' }).code, 0);
  m = w.sent().at(-1);
  assert.equal(m.title, 'Alarm-Server erreicht Bark wieder');
  assert.doesNotMatch(m.title + m.body, OLD_PLACE);
});

test('Probe-Meldung (--test) nennt das Ziel, nicht die Cloud-VM', () => {
  const w = makeWatcher(SERVER);
  const r = spawnSync('bash', [script, '--test'], {
    encoding: 'utf8',
    env: {
      ...process.env, PATH: bin + delimiter + process.env.PATH,
      FW_WATCH_CONF: join(tmp, `w${seq}`, 'watch.conf').replace(/\\/g, '/'),
      STATE_DIRECTORY: join(tmp, `w${seq}`, 'state').replace(/\\/g, '/'),
      FAKE_LOG: join(tmp, `w${seq}`, 'bark.log').replace(/\\/g, '/'),
    },
  });
  assert.equal(r.status, 0, r.stderr);
  const [m] = w.sent();
  assert.equal(m.title, 'Aussen-Waechter: Probe');
  assert.match(m.body, /wenn Alarm-Server von aussen nicht mehr erreichbar ist/);
  assert.doesNotMatch(m.body, OLD_PLACE);
});

test('alle Bark-Texte sind ASCII', () => {
  const w = makeWatcher('TARGET_NAME="Alarm Server 2"\n');
  w.run({ FAKE_ALIVE: 'down' });
  w.run({ FAKE_ALIVE: 'up', FAKE_CRON_AGE: '4000', FAKE_SENDFAIL: '1' });
  w.run({ FAKE_ALIVE: 'up' });
  const sent = w.sent();
  assert.ok(sent.length >= 5, `nur ${sent.length} Meldungen`);
  for (const m of sent) {
    assert.match(m.title, /^[\x20-\x7e]+$/, m.title);
    assert.match(m.body, /^[\x20-\x7e]+$/, m.body);
    assert.match(m.title + m.body, /Alarm Server 2|Aussen-Waechter/);
  }
});

test('ungültiger TARGET_NAME: Konfigurationsfehler (exit 2), kein Aufruf', () => {
  for (const bad of ['Alarm_Server', 'Alärm', 'x'.repeat(25), '', 'Alarm"Server', 'A$(id)']) {
    const w = makeWatcher(`TARGET_NAME='${bad}'\n`);
    const r = w.run({ FAKE_ALIVE: 'up' });
    assert.equal(r.code, 2, `"${bad}" → exit ${r.code}`);
    assert.match(r.err, /TARGET_NAME/);
    assert.equal(w.aliveCalls(), 0);
    assert.equal(w.sent().length, 0);
  }
  // Grenzfälle, die gelten: 1 und 24 Zeichen, Leerzeichen und Bindestrich
  for (const good of ['A', 'x'.repeat(24), 'Alarm-Server 1']) {
    const w = makeWatcher(`TARGET_NAME='${good}'\n`);
    assert.equal(w.run({ FAKE_ALIVE: 'up' }).code, 0, good);
    assert.equal(w.aliveCalls(), 1);
  }
});

test('Zustandsdatei eines älteren Stands gilt weiter', () => {
  const w = makeWatcher(SERVER);
  // "nicht erreichbar" war schon gemeldet (Schlüssel wie bisher)
  writeFileSync(join(tmp, `w${seq}`, 'state', 'state'),
    `fail_count=4\nfail_since=${Math.floor(Date.now() / 1000) - 1200}\nfail_reason=curl-Fehler 7\ndown_notified_at=${Math.floor(Date.now() / 1000) - 600}\n`);
  assert.equal(w.run({ FAKE_ALIVE: 'up' }).code, 0);
  const sent = w.sent();
  assert.equal(sent.length, 1);
  assert.equal(sent[0].title, 'Alarm-Server wieder erreichbar');
  assert.match(sent[0].body, /nach ca\. 20 min Ausfall; zuletzt: curl-Fehler 7/);
});

// ---- Als root (DSM-Aufgabe auf dem NAS) ---------------------------------------
// Als root wird die Konfiguration als root AUSGEFÜHRT (". $CONF"). Liegt sie -
// oder das Skript oder der Zustandsordner - dort, wo ein anderer Benutzer
// schreiben darf (auf dem NAS: der Web-Benutzer in den Freigaben), hätte der
// damit root. id und stat sind hier Attrappen: "root" mit einstellbaren
// Besitzern/Rechten.
const rootBin = join(tmp, 'rootbin');
mkdirSync(rootBin);
writeFileSync(join(rootBin, 'id'), `#!/usr/bin/env bash
if [ "\${1:-}" = "-u" ]; then echo "\${FAKE_UID:-0}"; else exec /usr/bin/id "$@"; fi
`);
// stat -c '%u %a' PFAD: "0 700", außer der Pfad enthält FAKE_STAT_BAD_MATCH.
writeFileSync(join(rootBin, 'stat'), `#!/usr/bin/env bash
path="\${@: -1}"
if [ -n "\${FAKE_STAT_BAD_MATCH:-}" ] && [[ "$path" == *"$FAKE_STAT_BAD_MATCH"* ]]; then
  echo "$FAKE_STAT_BAD"
else
  echo "0 700"
fi
`);
chmodSync(join(rootBin, 'id'), 0o755);
chmodSync(join(rootBin, 'stat'), 0o755);
const AS_ROOT = rootBin + delimiter;
// Zeile in der Konfiguration, an der man sieht, ob sie ausgeführt wurde.
const TRIPWIRE = 'echo CONF-AUSGEFUEHRT >&2\n';

test('als root: läuft, wenn Skript, Konfiguration und Ordner nur root gehören', () => {
  const w = makeWatcher(SERVER + TRIPWIRE);
  const r = w.run({ FAKE_ALIVE: 'up' }, AS_ROOT);
  assert.equal(r.code, 0, r.err);
  assert.match(r.err, /CONF-AUSGEFUEHRT/);
  assert.equal(w.aliveCalls(), 1);
});

test('als root: was ein anderer Benutzer ändern kann, wird nicht ausgeführt', () => {
  const cases = [
    ['watch.conf', '1023 600', 'Konfiguration gehört nicht root'],
    ['watch.conf', '0 666', 'Konfiguration für alle schreibbar'],
    ['watch.conf', '0 660', 'Konfiguration für die Gruppe schreibbar'],
    ['fw-alarm-watch.sh', '1026 755', 'Skript gehört nicht root'],
    ['cloud_watcher', '0 777', 'Ordner des Skripts für alle schreibbar'],
    ['/state', '0 1777', 'Zustandsordner für alle schreibbar'],
    ['watch.conf', '', 'Rechte nicht lesbar'],
  ];
  for (const [match, bad, what] of cases) {
    const w = makeWatcher(SERVER + TRIPWIRE);
    const r = w.run({ FAKE_ALIVE: 'up', FAKE_STAT_BAD_MATCH: match, FAKE_STAT_BAD: bad }, AS_ROOT);
    assert.equal(r.code, 2, `${what}: exit ${r.code} ${r.err}`);
    assert.match(r.err, /Unsichere Rechte/, what);
    assert.doesNotMatch(r.err, /CONF-AUSGEFUEHRT/, `${what}: Konfiguration wurde trotzdem ausgeführt`);
    assert.equal(w.aliveCalls(), 0, what);
    assert.equal(w.sent().length, 0, what);
    assert.equal(existsSync(join(w.dir, 'state', 'state')), false, `${what}: Zustand wurde geschrieben`);
  }
});

test('nicht als root: keine Rechteprüfung (Betrieb unter systemd mit eigenem Benutzer)', () => {
  const w = makeWatcher(SERVER);
  const r = w.run({ FAKE_ALIVE: 'up', FAKE_UID: '1001', FAKE_STAT_BAD_MATCH: 'watch.conf', FAKE_STAT_BAD: '0 644' }, AS_ROOT);
  assert.equal(r.code, 0, r.err);
  assert.equal(w.aliveCalls(), 1);
});
