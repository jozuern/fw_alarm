// Tests für server/backup.sh (Sicherung von Daten + config.php der VM auf den
// PC) und für den Sicherungs-Schritt in server/deploy.sh. ssh ist eine
// Attrappe im PATH: Die echte VM wird nie angesprochen, der Befehl der
// Sicherung läuft stattdessen wirklich (echtes tar) gegen einen Test-Ordner.
// Aufruf: node --test "tests/server/*.test.mjs"
import test, { after } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { chmodSync, cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { delimiter, dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const repo = join(here, '..', '..');
const fwd = (p) => p.replace(/\\/g, '/');
const backupScript = fwd(join(repo, 'server', 'backup.sh'));
const tmp = mkdtempSync(join(tmpdir(), 'fw-backup-'));
const bin = join(tmp, 'bin');
mkdirSync(bin);

// ssh-Attrappe. Der letzte Parameter ist der Befehl für die VM:
//  - die Schritte von deploy.sh werden nachgestellt (Ablage unter $FAKE_VM),
//  - "sudo tar … -C /srv/fw-alarm …" (die Sicherung) läuft wirklich, nur mit
//    $FAKE_VM statt /srv/fw-alarm. FAKE_SUDO_EXIT ersetzt danach den
//    Rückgabewert von tar (1 = "Datei änderte sich beim Lesen").
writeFileSync(join(bin, 'ssh'), [
  '#!/usr/bin/env bash',
  'for a in "$@"; do cmd=$a; done',
  'printf \'%s\\n\' "$cmd" >> "$FAKE_SSH_LOG"',
  'vm=$(cygpath -u "$FAKE_VM" 2>/dev/null || printf \'%s\' "$FAKE_VM")',
  'case "$cmd" in',
  '  "mktemp -d /tmp/fw-deploy."*) echo /tmp/fw-deploy.AbCdEfGhIj ;;',
  '  "tar -x -C "*) mkdir -p "$vm/stage" && tar -x -C "$vm/stage" ;;',
  '  "sudo rsync "*) rm -rf "$vm/deployed" && cp -r "$vm/stage/nas_dashboard" "$vm/deployed"',
  // Die Ausnahmen (--exclude=/name) wie rsync: Was ausgenommen ist, kommt nicht an.
  '    for x in $cmd; do case "$x" in --exclude=/*) rm -rf "$vm/deployed/${x#--exclude=/}" ;; esac; done ;;',
  '  "cd \'/srv/fw-alarm/app\' && sudo find "*)',
  // (Unter Windows schreibt sha256sum " *datei"; die VM ist Linux: zwei Leerzeichen.)
  '    cd "$vm/deployed" && find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum | sed "s/ \\*/  /" ;;',
  '  "sudo tar "*" -C /srv/fw-alarm "*)',
  '    sudo() { "$@"; rc=$?; if [ -n "$FAKE_SUDO_EXIT" ]; then return "$FAKE_SUDO_EXIT"; fi; return $rc; }',
  '    real=$(printf \'%s\' "$cmd" | sed "s#/srv/fw-alarm#$vm#g")',
  // FAKE_CUT: Die Verbindung liefert nur die ersten N Bytes, meldet aber Erfolg.
  '    if [ -n "$FAKE_CUT" ]; then eval "$real" | head -c "$FAKE_CUT"; exit 0; fi',
  '    eval "$real" ;;',
  '  *) echo "ssh-Attrappe: unbekannter Aufruf: $cmd" >&2; exit 99 ;;',
  'esac',
  '',
].join('\n'));
chmodSync(join(bin, 'ssh'), 0o755);

after(() => rmSync(tmp, { recursive: true, force: true }));

const KEYS = '<?php http_response_code(404); exit; ?>\n{"version":7,"keys":[{"id":1,"key":"testkey0001","label":"Test"}]}\n';
const CONFIG = "<?php\nreturn ['dashboard_user' => 'test'];\n";
const LOG = '<?php http_response_code(404); exit; ?>\n{"event":"created","log_at":1}\n';

let seq = 0;
// Eigene Test-VM (Ordner wie /srv/fw-alarm) und eigener Zielordner je Test.
function makeVm() {
  seq += 1;
  const vm = join(tmp, `vm${seq}`);
  mkdirSync(join(vm, 'data'), { recursive: true });
  mkdirSync(join(vm, 'app'), { recursive: true });
  writeFileSync(join(vm, 'data', 'alarm_keys.json.php'), KEYS);
  writeFileSync(join(vm, 'data', 'commands.log.php'), LOG);
  writeFileSync(join(vm, 'app', 'config.php'), CONFIG);
  return { vm, dest: join(tmp, `ziel${seq}`), sshLog: join(vm, 'ssh.log') };
}

function run(script, args, t, env = {}, cwd = undefined) {
  const r = spawnSync('bash', [script, ...args], {
    encoding: 'utf8',
    cwd,
    env: {
      ...process.env, PATH: bin + delimiter + process.env.PATH,
      FW_DEPLOY_HOST: 'ubuntu@vm.test', FW_DEPLOY_KEY: fwd(join(tmp, 'kein-schluessel')),
      FW_BACKUP_DIR: fwd(t.dest), FW_BACKUP_KEEP: '',
      FAKE_VM: fwd(t.vm), FAKE_SSH_LOG: fwd(t.sshLog), FAKE_SUDO_EXIT: '', FAKE_CUT: '',
      ...env,
    },
  });
  return { code: r.status, out: r.stdout, err: r.stderr };
}
const backup = (t, env) => run(backupScript, [], t, env);
const ARCHIVE_NAME = /^fw-alarm-\d{8}T\d{6}Z\.tgz$/;
const filesIn = (dir) => (existsSync(dir) ? readdirSync(dir).sort() : []);

// Packt ein Archiv mit echtem tar aus und liefert den Ordner.
function unpack(archive) {
  const out = mkdtempSync(join(tmp, 'aus-'));
  const r = spawnSync('bash', ['-c', 'tar -xzf - -C "$(cygpath -u "$OUT" 2>/dev/null || printf %s "$OUT")" < "$ARCHIVE"'], {
    encoding: 'utf8', env: { ...process.env, OUT: fwd(out), ARCHIVE: fwd(archive) },
  });
  assert.equal(r.status, 0, r.stderr);
  return out;
}

test('legt Empfängerliste, Verlauf und config.php der VM als ein Archiv im Zielordner ab', () => {
  const t = makeVm();
  const r = backup(t);
  assert.equal(r.code, 0, r.err);
  const files = filesIn(t.dest);
  assert.equal(files.length, 1, `Zielordner: ${files}`);
  assert.match(files[0], ARCHIVE_NAME);
  assert.ok(r.out.includes(files[0]), `Ausgabe nennt die Datei nicht: ${r.out}`);
  const out = unpack(join(t.dest, files[0]));
  assert.equal(readFileSync(join(out, 'data', 'alarm_keys.json.php'), 'utf8'), KEYS);
  assert.equal(readFileSync(join(out, 'data', 'commands.log.php'), 'utf8'), LOG);
  assert.equal(readFileSync(join(out, 'app', 'config.php'), 'utf8'), CONFIG);
});

test('ändert sich beim Packen eine Datei (tar meldet 1), gilt die vollständige Sicherung trotzdem', () => {
  const t = makeVm();
  const r = backup(t, { FAKE_SUDO_EXIT: '1' });
  assert.equal(r.code, 0, r.err);
  assert.equal(filesIn(t.dest).length, 1);
});

test('scheitert tar auf der VM, bleibt nichts liegen, das wie eine Sicherung aussieht', () => {
  const t = makeVm();
  const r = backup(t, { FAKE_SUDO_EXIT: '2' });
  assert.equal(r.code, 1, r.out + r.err);
  assert.deepEqual(filesIn(t.dest), []);
  assert.match(r.err, /FEHLER/);
});

test('kommt nur ein Teil des Archivs an, wird es nicht als Sicherung angenommen', () => {
  const t = makeVm();
  const r = backup(t, { FAKE_CUT: '60' });
  assert.equal(r.code, 1, r.out + r.err);
  assert.deepEqual(filesIn(t.dest), []);
  assert.match(r.err, /Archiv/);
});

test('fehlt config.php auf der VM, ist das ein Fehler', () => {
  const t = makeVm();
  rmSync(join(t.vm, 'app', 'config.php'));
  const r = backup(t);
  assert.equal(r.code, 1, r.out + r.err);
  assert.deepEqual(filesIn(t.dest), []);
});

test('ein Archiv ohne Empfängerliste wird nicht als Sicherung angenommen', () => {
  const t = makeVm();
  rmSync(join(t.vm, 'data', 'alarm_keys.json.php'));
  const r = backup(t);
  assert.equal(r.code, 1, r.out + r.err);
  assert.deepEqual(filesIn(t.dest), []);
  assert.match(r.err, /alarm_keys\.json\.php/);
});

test('eine unlesbare Empfängerliste wird nicht als Sicherung angenommen', () => {
  const t = makeVm();
  writeFileSync(join(t.vm, 'data', 'alarm_keys.json.php'), '');
  const r = backup(t);
  assert.equal(r.code, 1, r.out + r.err);
  assert.deepEqual(filesIn(t.dest), []);
  assert.match(r.err, /Empfängerliste/);
});

test('über der Grenze verschwinden die ältesten Sicherungen – fremde Dateien bleiben', () => {
  const t = makeVm();
  mkdirSync(t.dest, { recursive: true });
  for (const name of ['fw-alarm-20260101T000000Z.tgz', 'fw-alarm-20260201T000000Z.tgz', 'fw-alarm-20260301T000000Z.tgz', 'notizen.txt']) {
    writeFileSync(join(t.dest, name), 'alt');
  }
  const r = backup(t, { FW_BACKUP_KEEP: '2' });
  assert.equal(r.code, 0, r.err);
  const files = filesIn(t.dest);
  assert.equal(files.length, 3, `Zielordner: ${files}`);
  assert.equal(files[0], 'fw-alarm-20260301T000000Z.tgz');
  assert.match(files[1], ARCHIVE_NAME);
  assert.notEqual(readFileSync(join(t.dest, files[1]), 'utf8'), 'alt');
  assert.equal(files[2], 'notizen.txt');
});

test('eine gescheiterte Sicherung löscht keine älteren', () => {
  const t = makeVm();
  mkdirSync(t.dest, { recursive: true });
  const old = ['fw-alarm-20260101T000000Z.tgz', 'fw-alarm-20260201T000000Z.tgz'];
  for (const name of old) writeFileSync(join(t.dest, name), 'alt');
  const r = backup(t, { FW_BACKUP_KEEP: '1', FAKE_SUDO_EXIT: '2' });
  assert.equal(r.code, 1, r.out + r.err);
  assert.deepEqual(filesIn(t.dest), old);
});

test('ein unbrauchbares Ziel (FW_DEPLOY_HOST) wird abgelehnt, bevor die VM angesprochen wird', () => {
  const t = makeVm();
  const r = backup(t, { FW_DEPLOY_HOST: 'ubuntu@vm.test; touch x' });
  assert.equal(r.code, 2, r.out + r.err);
  assert.equal(existsSync(t.sshLog), false);
  assert.deepEqual(filesIn(t.dest), []);
});

// --- im Repo: Zielordner-Schutz und der Sicherungs-Schritt von deploy.sh ----
// Eigenes kleines Repo mit einer Kopie der beiden Skripte, einem lokalen
// "origin" (kein Netz) und 12 Dateien unter nas_dashboard/ – dazu extra:
// weitere Dateien unter nas_dashboard/ (Name → Inhalt).
function makeRepo(extra = {}) {
  const t = makeVm();
  const work = join(tmp, `repo${seq}`);
  const origin = join(tmp, `origin${seq}.git`);
  mkdirSync(join(work, 'server'), { recursive: true });
  mkdirSync(join(work, 'nas_dashboard', 'api'), { recursive: true });
  for (let i = 1; i <= 12; i += 1) {
    const name = `datei${String(i).padStart(2, '0')}.php`;
    writeFileSync(join(work, 'nas_dashboard', i % 3 === 0 ? 'api' : '', name), `<?php // Datei ${i}\n`);
  }
  for (const [name, content] of Object.entries(extra)) {
    writeFileSync(join(work, 'nas_dashboard', name), content);
  }
  for (const f of ['deploy.sh', 'backup.sh']) {
    if (existsSync(join(repo, 'server', f))) cpSync(join(repo, 'server', f), join(work, 'server', f));
  }
  const git = (args, cwd = work) => {
    const r = spawnSync('git', ['-c', 'user.name=Test', '-c', 'user.email=test@example.invalid', '-c', 'core.autocrlf=false', ...args], { cwd, encoding: 'utf8' });
    assert.equal(r.status, 0, `git ${args.join(' ')}: ${r.stderr}`);
    return r.stdout.trim();
  };
  git(['init', '-q', '-b', 'main', '.']);
  git(['init', '-q', '--bare', fwd(origin)], tmp);
  git(['add', '-A']);
  git(['commit', '-q', '-m', 'Stand']);
  git(['remote', 'add', 'origin', fwd(origin)]);
  git(['push', '-q', 'origin', 'main']);
  return { ...t, work, sha: git(['rev-parse', 'HEAD']) };
}

test('ein Zielordner im Repo wird abgelehnt (das Repo ist öffentlich)', () => {
  const t = makeRepo();
  const inside = join(t.work, 'sicherung');
  const r = run('server/backup.sh', [], t, { FW_BACKUP_DIR: fwd(inside) }, t.work);
  assert.equal(r.code, 2, r.out + r.err);
  assert.equal(existsSync(inside), false);
  assert.equal(existsSync(t.sshLog), false);
});

test('deploy.sh sichert, bevor sich auf dem Server etwas ändert, und spielt dann ein', () => {
  const t = makeRepo();
  const r = run('server/deploy.sh', ['HEAD'], t, {}, t.work);
  assert.equal(r.code, 0, r.out + r.err);
  assert.ok(r.out.includes(`deployed ${t.sha}: 12 files, 0 mismatches`), r.out + r.err);
  const files = filesIn(t.dest);
  assert.equal(files.length, 1, `Zielordner: ${files}`);
  assert.match(files[0], ARCHIVE_NAME);
  const calls = readFileSync(t.sshLog, 'utf8').split('\n');
  const backupAt = calls.findIndex((c) => c.startsWith('sudo tar '));
  const changeAt = calls.findIndex((c) => c.startsWith('tar -x -C ') || c.startsWith('sudo rsync '));
  assert.ok(backupAt >= 0 && changeAt >= 0 && backupAt < changeAt, `Reihenfolge der Aufrufe:\n${calls.join('\n')}`);
});

test('deploy.sh spielt die Einbettung der Go-Fassung (webassets.go) nicht in den Webroot ein', () => {
  const t = makeRepo({ 'webassets.go': 'package webassets\n' });
  const r = run('server/deploy.sh', ['HEAD'], t, {}, t.work);
  assert.equal(r.code, 0, r.out + r.err);
  assert.ok(r.out.includes(`deployed ${t.sha}: 12 files, 0 mismatches`), r.out + r.err);
  assert.equal(existsSync(join(t.vm, 'deployed', 'webassets.go')), false, 'webassets.go liegt im Webroot');
  const rsync = readFileSync(t.sshLog, 'utf8').split('\n').find((c) => c.startsWith('sudo rsync '));
  assert.match(rsync, /--exclude=\/webassets\.go /);
});

test('scheitert die Sicherung, wird trotzdem eingespielt – der Lauf endet aber mit Warnung und Fehlercode', () => {
  const t = makeRepo();
  const r = run('server/deploy.sh', ['HEAD'], t, { FAKE_SUDO_EXIT: '2' }, t.work);
  assert.ok(r.out.includes(`deployed ${t.sha}: 12 files, 0 mismatches`), r.out + r.err);
  assert.equal(r.code, 3, r.out + r.err);
  assert.deepEqual(filesIn(t.dest), []);
  const lastLine = r.err.trim().split('\n').at(-1);
  assert.match(lastLine, /ACHTUNG.*Sicherung/);
});
