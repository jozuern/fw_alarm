// Minimaler Chrome-DevTools-Protocol-Client (nur Node-Bordmittel, keine npm-Pakete).
import { spawn } from 'node:child_process';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const CHROME = process.env.CHROME || 'C:/Program Files/Google/Chrome/Application/chrome.exe';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

class Page {
  constructor(ws, targetId, browser) {
    this.ws = ws;
    this.targetId = targetId;
    this.browser = browser;
    this.nextId = 1;
    this.pending = new Map();
    this.waiters = [];
    ws.addEventListener('message', (ev) => {
      const msg = JSON.parse(ev.data);
      if (msg.id && this.pending.has(msg.id)) {
        const { resolve, reject } = this.pending.get(msg.id);
        this.pending.delete(msg.id);
        msg.error ? reject(new Error(msg.error.message)) : resolve(msg.result);
      } else if (msg.method) {
        this.waiters = this.waiters.filter((w) => !(w.method === msg.method && (w.resolve(msg.params), true)));
      }
    });
  }

  send(method, params = {}) {
    const id = this.nextId++;
    this.ws.send(JSON.stringify({ id, method, params }));
    return new Promise((resolve, reject) => this.pending.set(id, { resolve, reject }));
  }

  once(method) {
    return new Promise((resolve) => this.waiters.push({ method, resolve }));
  }

  async viewport({ width, height, mobile = false, scale = 1 }) {
    await this.send('Emulation.setDeviceMetricsOverride', {
      width, height, mobile, deviceScaleFactor: scale,
    });
  }

  async goto(url) {
    const loaded = this.once('Page.loadEventFired');
    await this.send('Page.navigate', { url });
    await loaded;
  }

  async eval(expr) {
    const r = await this.send('Runtime.evaluate', {
      expression: expr, awaitPromise: true, returnByValue: true,
    });
    if (r.exceptionDetails) {
      throw new Error('eval: ' + (r.exceptionDetails.exception?.description || r.exceptionDetails.text));
    }
    return r.result.value;
  }

  async waitFor(expr, timeoutMs = 10000) {
    const end = Date.now() + timeoutMs;
    let last;
    while (Date.now() < end) {
      try {
        last = await this.eval(expr);
        if (last) return last;
      } catch (e) {
        last = e.message; // Seite lädt evtl. gerade neu
      }
      await sleep(100);
    }
    throw new Error(`waitFor Timeout (${timeoutMs} ms): ${expr} (zuletzt: ${last})`);
  }

  async screenshot(path) {
    const r = await this.send('Page.captureScreenshot', { format: 'png' });
    writeFileSync(path, Buffer.from(r.data, 'base64'));
  }

  async click(selector) {
    const sel = JSON.stringify(selector);
    await this.waitFor(`!!document.querySelector(${sel})`);
    await this.eval(`document.querySelector(${sel}).click()`);
  }

  async login(base, user, pass) {
    await this.goto(`${base}/index.php`);
    await this.waitFor(`!!document.querySelector('#user')`);
    const loaded = this.once('Page.loadEventFired');
    await this.eval(`(() => {
      document.querySelector('#user').value = ${JSON.stringify(user)};
      document.querySelector('#password').value = ${JSON.stringify(pass)};
      document.querySelector('#password').form.submit();
    })()`);
    await loaded;
  }
}

class Browser {
  constructor(proc, port, profile) {
    this.proc = proc;
    this.port = port;
    this.profile = profile;
    this.pages = [];
  }

  async newPage() {
    const res = await fetch(`http://127.0.0.1:${this.port}/json/new?about:blank`, { method: 'PUT' });
    const target = await res.json();
    const ws = new WebSocket(target.webSocketDebuggerUrl);
    await new Promise((resolve, reject) => {
      ws.addEventListener('open', resolve);
      ws.addEventListener('error', () => reject(new Error('WebSocket-Fehler')));
    });
    const page = new Page(ws, target.id, this);
    await page.send('Page.enable');
    await page.send('Runtime.enable');
    this.pages.push(page);
    return page;
  }

  async close() {
    for (const p of this.pages) {
      try { p.ws.close(); } catch { /* egal */ }
    }
    this.proc.kill();
    await sleep(300);
    try { rmSync(this.profile, { recursive: true, force: true }); } catch { /* egal */ }
  }
}

export async function launch({ port = 9333, headless = true } = {}) {
  const profile = mkdtempSync(join(tmpdir(), 'fw-cdp-'));
  const args = [
    `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`,
    '--no-first-run', '--hide-scrollbars', 'about:blank',
  ];
  if (headless) args.unshift('--headless=new');
  const proc = spawn(CHROME, args, { stdio: 'ignore' });
  const browser = new Browser(proc, port, profile);
  for (let i = 0; i < 100; i++) {
    try {
      const r = await fetch(`http://127.0.0.1:${port}/json/version`);
      if (r.ok) return browser;
    } catch { /* noch nicht bereit */ }
    await sleep(100);
  }
  proc.kill();
  throw new Error('Chrome nicht erreichbar');
}

// Kleiner HTTP-Client für die Dashboard-API (ohne Browser): Cookie-Jar von Hand,
// Login per Formular-POST, CSRF-Token aus der Status-Antwort.
export async function apiSession(base, user, pass) {
  let cookie = '';
  let csrf = '';
  const keep = (res) => {
    for (const c of res.headers.getSetCookie?.() ?? []) {
      const pair = c.split(';')[0];
      if (pair.startsWith('boss925_dashboard=')) cookie = pair;
    }
  };
  const api = (action, params = {}) => `${base}/dashboard_api.php?` + new URLSearchParams({ action, ...params });
  const refreshCsrf = async () => {
    const r = await fetch(api('status'), { headers: { cookie } });
    csrf = (await r.json()).csrf || '';
  };

  const login = await fetch(`${base}/index.php`, {
    method: 'POST',
    redirect: 'manual',
    headers: { 'content-type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams({ action: 'login', user, password: pass }),
  });
  keep(login);
  if (!cookie) throw new Error(`Login fehlgeschlagen (HTTP ${login.status})`);
  await refreshCsrf();

  return {
    async get(action, params = {}) {
      const r = await fetch(api(action, params), { headers: { cookie } });
      return { ...(await r.json()), status: r.status };
    },
    async post(fields) {
      const r = await fetch(`${base}/dashboard_api.php`, {
        method: 'POST',
        headers: { cookie, 'x-csrf-token': csrf, 'content-type': 'application/x-www-form-urlencoded' },
        body: new URLSearchParams(fields),
      });
      const body = { ...(await r.json()), status: r.status };
      if (r.status === 403 && body.code === 'csrf') await refreshCsrf();
      return body;
    },
  };
}
