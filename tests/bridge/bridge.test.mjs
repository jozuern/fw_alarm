// Tests für die NAS-Brücke (nas_bridge/): zwei echte PHP-Server.
//   Brücke     127.0.0.1:8790  (Kopie von nas_bridge/ in einem Temp-Ordner)
//   Gegenstelle 127.0.0.1:8791 (tests/bridge/upstream.php, spielt den Server)
//   Brücke 2   127.0.0.1:8792  (Ziel 127.0.0.1:8799 - dort hört niemand)
// Aufruf: node --test "tests/bridge/*.test.mjs"
import test, { before, after } from 'node:test';
import assert from 'node:assert/strict';
import { spawn, execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { cpSync, existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import http from 'node:http';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const repo = join(here, '..', '..');
const BRIDGE = 8790, UPSTREAM = 8791, BRIDGE_DOWN = 8792, DEAD = 8799;

const tmp = mkdtempSync(join(tmpdir(), 'fw-bridge-'));
const upLog = join(tmp, 'upstream.log');
const heldLog = join(tmp, 'held.log');
const bridgeDir = join(tmp, 'bridge');
const bridgeDownDir = join(tmp, 'bridge-down');
const procs = [];

// PHP-Aufruf wie in tests/dashboard/sandbox/start.sh: Unter Windows ist cURL
// nicht von selbst geladen.
function phpArgs() {
  const mods = execFileSync('php', ['-m'], { encoding: 'utf8' }).toLowerCase();
  if (/^curl$/m.test(mods)) return [];
  const bin = execFileSync('php', ['-r', 'echo PHP_BINARY;'], { encoding: 'utf8' }).trim();
  return ['-d', `extension_dir=${join(dirname(bin), 'ext')}`, '-d', 'extension=curl', '-d', 'extension=openssl'];
}

function writeConfig(dir, target) {
  const q = (s) => "'" + String(s).replace(/\\/g, '/').replace(/'/g, "\\'") + "'";
  writeFileSync(join(dir, 'bridge_config.php'),
    `<?php\nreturn [\n    'target' => ${q(target)},\n    'public_url' => 'https://alarm.tunero.dev/',\n`
    + `    'held_log' => ${q(heldLog)},\n];\n`);
}

function startPhp(port, args, env = {}) {
  const child = spawn('php', [...phpArgs(), '-S', `127.0.0.1:${port}`, ...args], {
    env: { ...process.env, ...env }, stdio: 'ignore', windowsHide: true,
  });
  procs.push(child);
}

// Eine Anfrage mit genau den angegebenen Kopfzeilen (fetch() setzte eigene).
function call({ port = BRIDGE, method = 'GET', path, headers = {}, body = null }) {
  return new Promise((resolve, reject) => {
    const started = Date.now();
    const h = { ...headers };
    if (body !== null) h['Content-Length'] = Buffer.byteLength(body);
    const req = http.request({ host: '127.0.0.1', port, method, path, headers: h, agent: false }, (res) => {
      const chunks = [];
      res.on('data', (c) => chunks.push(c));
      res.on('end', () => resolve({
        status: res.statusCode, headers: res.headers,
        body: Buffer.concat(chunks).toString('utf8'), ms: Date.now() - started,
      }));
    });
    req.on('error', reject);
    if (body !== null) req.write(body);
    req.end();
  });
}

async function waitFor(port) {
  for (let i = 0; i < 100; i += 1) {
    try {
      await call({ port, path: '/__bereit' });
      return;
    } catch {
      await new Promise((r) => setTimeout(r, 100));
    }
  }
  throw new Error(`Server auf Port ${port} startet nicht`);
}

// Was die Gegenstelle bisher gesehen hat (ohne die Bereitschaftsprüfung).
function upstreamHits() {
  if (!existsSync(upLog)) return [];
  return readFileSync(upLog, 'utf8').split('\n').filter((l) => l.trim()).map((l) => JSON.parse(l))
    .filter((h) => h.path !== '/__bereit');
}
const lastHit = () => upstreamHits().at(-1);
const FORM = { 'Content-Type': 'application/x-www-form-urlencoded' };
const sha12 = (s) => createHash('sha256').update(s).digest('hex').slice(0, 12);
const heldLines = () => (existsSync(heldLog) ? readFileSync(heldLog, 'utf8').split('\n').filter((l) => l !== '') : []);

before(async () => {
  cpSync(join(repo, 'nas_bridge'), bridgeDir, { recursive: true });
  cpSync(join(repo, 'nas_bridge'), bridgeDownDir, { recursive: true });
  writeConfig(bridgeDir, `http://127.0.0.1:${UPSTREAM}`);
  writeConfig(bridgeDownDir, `http://127.0.0.1:${DEAD}`);
  startPhp(UPSTREAM, [join(here, 'upstream.php')], { BRIDGE_UP_LOG: upLog });
  startPhp(BRIDGE, ['-t', bridgeDir]);
  startPhp(BRIDGE_DOWN, ['-t', bridgeDownDir]);
  await Promise.all([waitFor(UPSTREAM), waitFor(BRIDGE), waitFor(BRIDGE_DOWN)]);
});

after(() => {
  for (const p of procs) p.kill();
  rmSync(tmp, { recursive: true, force: true });
});

test('forwards box POST unchanged', async () => {
  const r = await call({ method: 'POST', path: '/api/poll.php', headers: FORM, body: 'token=t&nonce=abc' });
  assert.equal(r.status, 200);
  assert.equal(r.body, 'ok=1\nnonce=abc');
  assert.match(r.headers['content-type'], /^text\/plain/);
  const hit = lastHit();
  assert.equal(hit.method, 'POST');
  assert.equal(hit.path, '/api/poll.php');
  assert.equal(hit.headers['content-type'], 'application/x-www-form-urlencoded');
  assert.equal(hit.body, 'token=t&nonce=abc');
  assert.equal(hit.headers['x-fw-bridge'], '1');
});

test('forwards all five endpoints to the same file name', async () => {
  for (const file of ['status.php', 'poll.php', 'ack.php', 'keys.php', 'mute.php']) {
    const r = await call({ method: 'POST', path: `/api/${file}`, headers: FORM, body: 'nonce=n1' });
    assert.equal(r.status, 200, file);
    assert.equal(lastHit().path, `/api/${file}`);
  }
});

test('passes upstream 403 through', async () => {
  const r = await call({ method: 'POST', path: '/api/status.php?code=403', headers: FORM, body: 'token=falsch' });
  assert.equal(r.status, 403);
  assert.equal(r.body, 'code 403\n');
});

test('turns a Cloudflare challenge (403 + cf-mitigated) into 502, never 403', async () => {
  const r = await call({ method: 'POST', path: '/api/status.php?code=403&cfm=1', headers: FORM, body: 'token=t' });
  assert.equal(r.status, 502);
  assert.doesNotMatch(r.body, /(^|\n)(ok|type)=/);
});

test('keeps 303 relative Location and CSP, drops Set-Cookie', async () => {
  const r = await call({ path: '/api/mute.php?redirect=1' });
  assert.equal(r.status, 303);
  assert.equal(r.headers.location, 'mute.php?done=on');
  assert.equal(r.headers['content-security-policy'], "default-src 'self'");
  assert.equal(r.headers['set-cookie'], undefined);
  assert.equal(r.headers['strict-transport-security'], undefined);
});

test('keeps HEAD', async () => {
  const r = await call({ method: 'HEAD', path: '/api/mute.php?t=x&state=on' });
  assert.equal(r.status, 200);
  assert.equal(r.body, '');
  const hit = lastHit();
  assert.equal(hit.method, 'HEAD');
  assert.equal(hit.query, 't=x&state=on');
});

test('forwards only whitelisted request headers', async () => {
  await call({
    path: '/api/mute.php?t=x',
    headers: {
      Cookie: 'a=b', Authorization: 'Bearer geheim', 'X-Forwarded-For': '203.0.113.9',
      Accept: 'text/html', 'Accept-Language': 'de-DE', 'Sec-Fetch-Dest': 'document', 'Sec-Fetch-Mode': 'navigate',
      'Sec-Fetch-Site': 'none', 'User-Agent': 'WhatsApp/2', 'Sec-Purpose': 'prefetch', Purpose: 'prefetch',
      'X-Purpose': 'preview', 'X-Moz': 'prefetch',
    },
  });
  const h = lastHit().headers;
  assert.equal(h.cookie, undefined);
  assert.equal(h.authorization, undefined);
  assert.equal(h['x-forwarded-for'], undefined);
  assert.equal(h.accept, 'text/html');
  assert.equal(h['accept-language'], 'de-DE');
  assert.equal(h['sec-fetch-dest'], 'document');
  assert.equal(h['sec-fetch-mode'], 'navigate');
  assert.equal(h['sec-fetch-site'], 'none');
  assert.equal(h['user-agent'], 'WhatsApp/2');
  assert.equal(h['sec-purpose'], 'prefetch');
  assert.equal(h.purpose, 'prefetch');
  assert.equal(h['x-purpose'], 'preview');
  assert.equal(h['x-moz'], 'prefetch');
});

test('does not invent Accept, User-Agent or Content-Type', async () => {
  await call({ path: '/api/mute.php?t=x&state=on' });
  const h = lastHit().headers;
  assert.equal(h.accept, undefined);
  assert.equal(h['user-agent'], undefined);
  assert.equal(h['content-type'], undefined);
  assert.equal(h.expect, undefined);
});

test('rebuilds multipart as urlencoded', async () => {
  const b = 'XfwgrenzeX';
  const body = `--${b}\r\nContent-Disposition: form-data; name="token"\r\n\r\nt\r\n`
    + `--${b}\r\nContent-Disposition: form-data; name="nonce"\r\n\r\nabc\r\n--${b}--\r\n`;
  const r = await call({
    method: 'POST', path: '/api/poll.php', headers: { 'Content-Type': `multipart/form-data; boundary=${b}` }, body,
  });
  assert.equal(r.status, 200);
  assert.equal(r.body, 'ok=1\nnonce=abc');
  const hit = lastHit();
  assert.equal(hit.headers['content-type'], 'application/x-www-form-urlencoded');
  assert.equal(hit.body, 'token=t&nonce=abc');
});

test('hold answers 503 without forwarding', async () => {
  const hold = join(bridgeDir, 'bridge_hold.php');
  rmSync(heldLog, { force: true });
  writeFileSync(hold, "<?php\nhttp_response_code(404);\n");
  try {
    const before503 = upstreamHits().length;
    const poll = await call({ method: 'POST', path: '/api/poll.php', headers: FORM, body: 'token=t&nonce=abc' });
    assert.equal(poll.status, 503);
    assert.doesNotMatch(poll.body, /(^|\n)(ok|type)=/);

    const api = await call({ path: '/dashboard_api.php?action=status' });
    assert.equal(api.status, 503);

    // Vorschau (HEAD, Messenger-Kennung) hätte nichts geschaltet: nicht merken.
    assert.equal((await call({ method: 'HEAD', path: '/api/mute.php?t=abc&state=on' })).status, 503);
    assert.equal((await call({ path: '/api/mute.php?t=abc&state=on', headers: { 'User-Agent': 'WhatsApp/2.23' } })).status, 503);
    assert.equal((await call({ path: '/api/mute.php?t=abc&state=on', headers: { 'Sec-Purpose': 'prefetch' } })).status, 503);
    assert.equal(heldLines().length, 0);

    const mute = await call({ path: '/api/mute.php?t=abc&state=off', headers: { 'User-Agent': 'Kurzbefehle/1.0' } });
    assert.equal(mute.status, 503);
    let lines = heldLines();
    assert.equal(lines.length, 1);
    assert.match(lines[0], new RegExp(`^\\d{10}\\tt\\t${sha12('abc')}\\toff$`));

    // Alt-Form key=<Bark-Key>, per POST, ohne brauchbaren Zustand
    const old = await call({ method: 'POST', path: '/api/mute.php', headers: FORM, body: 'key=bark123&state=quatsch' });
    assert.equal(old.status, 503);
    lines = heldLines();
    assert.equal(lines.length, 2);
    assert.match(lines[1], new RegExp(`^\\d{10}\\tkey\\t${sha12('bark123')}\\t\\?$`));

    const raw = readFileSync(heldLog, 'utf8');
    assert.ok(!raw.includes('abc') && !raw.includes('bark123'), 'Token/Key stehen nicht im Log');
    assert.equal(upstreamHits().length, before503, 'im Halt wird nichts weitergereicht');
  } finally {
    rmSync(hold, { force: true });
  }
  // Datei weg = sofort wieder weiterreichen, ohne Neustart
  const again = await call({ method: 'POST', path: '/api/poll.php', headers: FORM, body: 'token=t&nonce=xyz' });
  assert.equal(again.status, 200);
  assert.equal(again.body, 'ok=1\nnonce=xyz');
});

test('upstream down is 502, never 403', async () => {
  const r = await call({ port: BRIDGE_DOWN, method: 'POST', path: '/api/poll.php', headers: FORM, body: 'token=t&nonce=a' });
  assert.equal(r.status, 502);
  assert.doesNotMatch(r.body, /(^|\n)(ok|type)=/);
  assert.match(r.headers['content-type'], /^text\/plain/);
});

test('slow upstream is 504 within 4 s', async () => {
  const r = await call({ method: 'POST', path: '/api/poll.php?slow=6', headers: FORM, body: 'token=t&nonce=a' });
  assert.equal(r.status, 504);
  assert.ok(r.ms < 4000, `dauerte ${r.ms} ms`);
  assert.doesNotMatch(r.body, /(^|\n)(ok|type)=/);
});

test('oversized body is 413', async () => {
  const hits = upstreamHits().length;
  const ok = await call({ method: 'POST', path: '/api/status.php', headers: FORM, body: 'a=' + 'x'.repeat(16382) });
  assert.equal(ok.status, 200, '16 384 Bytes gehen noch durch');
  assert.equal(upstreamHits().length, hits + 1);
  const r = await call({ method: 'POST', path: '/api/status.php', headers: FORM, body: 'a=' + 'x'.repeat(16383) });
  assert.equal(r.status, 413);
  assert.equal(upstreamHits().length, hits + 1, '16 385 Bytes werden nicht weitergereicht');
});

test('other methods are 405', async () => {
  const hits = upstreamHits().length;
  const r = await call({ method: 'DELETE', path: '/api/poll.php' });
  assert.equal(r.status, 405);
  assert.equal(upstreamHits().length, hits);
});

test('moved pages', async () => {
  for (const path of ['/index.php', '/', '/classic.php']) {
    const r = await call({ path });
    assert.equal(r.status, 302, path);
    assert.equal(r.headers.location, 'https://alarm.tunero.dev/');
    assert.match(r.headers['cache-control'], /no-store/);
  }
  const api = await call({ path: '/dashboard_api.php?action=status' });
  assert.equal(api.status, 410);
  assert.match(api.headers['content-type'], /^application\/json/);
  const json = JSON.parse(api.body);
  assert.equal(json.ok, false);
  assert.equal(json.code, 'moved');
  assert.equal(json.url, 'https://alarm.tunero.dev/');
});

test('no other paths', async () => {
  const hits = upstreamHits().length;
  assert.equal((await call({ path: '/api/health.php' })).status, 404);
  assert.equal((await call({ path: '/api/alive.php' })).status, 404);
  // Auch ein falsch verdrahteter Aufruf reicht nichts außerhalb der Liste weiter.
  writeFileSync(join(bridgeDir, 'api', 'x.php'), "<?php\nrequire __DIR__ . '/../lib/bridge.php';\nbridge_forward('health.php');\n");
  assert.equal((await call({ path: '/api/x.php' })).status, 404);
  assert.equal(upstreamHits().length, hits);
});

test('helper files answer 404 when called directly', async () => {
  assert.equal((await call({ path: '/lib/bridge.php' })).status, 404);
  writeFileSync(join(bridgeDir, 'bridge_hold.php'), "<?php\nhttp_response_code(404);\n");
  try {
    assert.equal((await call({ path: '/bridge_hold.php' })).status, 404);
  } finally {
    rmSync(join(bridgeDir, 'bridge_hold.php'), { force: true });
  }
});

test('missing bridge_config.php is a clear 500', async () => {
  const cfg = join(bridgeDownDir, 'bridge_config.php');
  const saved = readFileSync(cfg, 'utf8');
  rmSync(cfg);
  try {
    const r = await call({ port: BRIDGE_DOWN, method: 'POST', path: '/api/poll.php', headers: FORM, body: 'token=t' });
    assert.equal(r.status, 500);
    assert.match(r.body, /bridge_config fehlt/);
  } finally {
    writeFileSync(cfg, saved);
  }
});
