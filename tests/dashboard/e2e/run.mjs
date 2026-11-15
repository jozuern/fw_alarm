// Aufruf: SANDBOX_DIR=<dir> node run.mjs <scenario> [--out <dir>]
import { mkdirSync } from 'node:fs';
import { join, resolve, dirname } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { launch } from './cdp.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const argv = process.argv.slice(2);
const scenario = argv[0];
const outIdx = argv.indexOf('--out');
const sandbox = process.env.SANDBOX_DIR;
if (!scenario || !sandbox) {
  console.error('Aufruf: SANDBOX_DIR=<dir> node run.mjs <scenario> [--out <dir>]');
  process.exit(2);
}
const out = resolve(outIdx >= 0 ? argv[outIdx + 1] : join(sandbox, 'shots'));
const base = process.env.SANDBOX_BASE || 'http://127.0.0.1:8787';
mkdirSync(out, { recursive: true });

function assert(cond, msg) {
  if (!cond) throw new Error('Assertion fehlgeschlagen: ' + msg);
}
async function shot(page, name) {
  const path = join(out, `${scenario}-${name}.png`);
  await page.screenshot(path);
  console.log('Screenshot:', path);
}

let browser;
let code = 0;
try {
  const mod = await import(pathToFileURL(join(here, 'scenarios', `${scenario}.mjs`)).href);
  browser = await launch({});
  await mod.default({ browser, base, sandbox, out, assert, shot });
  console.log(`OK: ${scenario}`);
} catch (e) {
  console.error(`FEHLER: ${scenario}: ${e.stack || e}`);
  code = 1;
} finally {
  if (browser) await browser.close();
}
process.exit(code);
