// Aufruf: node run.mjs <scenario> [--out <dir>]
// Startet das Dashboard über tests/backend/harness.mjs (FW_BACKEND wählt die
// Fassung), führt das Szenario in Chrome (headless) aus und räumt alles ab.
import { mkdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve, dirname } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { startBackend } from '../../backend/harness.mjs';
import { launch } from './cdp.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const argv = process.argv.slice(2);
const scenario = argv[0];
const outIdx = argv.indexOf('--out');
if (!scenario) {
  console.error('Aufruf: node run.mjs <scenario> [--out <dir>]');
  process.exit(2);
}
const out = resolve(outIdx >= 0 ? argv[outIdx + 1] : join(tmpdir(), 'fw-e2e-shots'));
mkdirSync(out, { recursive: true });

function assert(cond, msg) {
  if (!cond) throw new Error('Assertion fehlgeschlagen: ' + msg);
}

let browser;
let backend;
let code = 0;
try {
  const mod = await import(pathToFileURL(join(here, 'scenarios', `${scenario}.mjs`)).href);
  backend = await startBackend({ config: { offline_after_seconds: 30 } });
  // Key-Präfix "slow" antwortet erst nach 4 s (wie die alte Attrappe).
  backend.bark.slowMs = 4000;
  const base = backend.base;
  async function shot(page, name) {
    const path = join(out, `${scenario}-${name}.png`);
    await page.screenshot(path);
    console.log('Screenshot:', path);
  }
  browser = await launch({});
  await mod.default({ browser, base, backend, out, assert, shot });
  console.log(`OK: ${scenario}`);
} catch (e) {
  console.error(`FEHLER: ${scenario}: ${e.stack || e}`);
  code = 1;
} finally {
  if (browser) await browser.close();
  if (backend) await backend.stop();
}
process.exit(code);
