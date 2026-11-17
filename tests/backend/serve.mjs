// Von Hand: startet ein Dashboard samt Bark-Attrappe, gibt die Adresse aus und
// läuft bis Strg+C. Zugänge wie in den Tests: Admin admin/test, Leser leser/test,
// Maschinen-Token box-token.
//   node tests/backend/serve.mjs [--port 8787]
// FW_BACKEND wählt die Fassung, FW_KEEP=1 lässt den Datenordner liegen.
import { startBackend, ADMIN, READER, MACHINE_TOKEN, STATUS_KEY } from './harness.mjs';

const argv = process.argv.slice(2);
const portIdx = argv.indexOf('--port');
const port = portIdx >= 0 ? Number(argv[portIdx + 1]) : undefined;
if (portIdx >= 0 && !(port > 0 && port < 65536)) {
  console.error('Aufruf: node tests/backend/serve.mjs [--port 8787]');
  process.exit(2);
}

const backend = await startBackend({ port, config: { offline_after_seconds: 30 } });
backend.bark.slowMs = 4000;
console.log(`Dashboard:  ${backend.base}/`);
console.log(`Bark-Attrappe: ${backend.bark.base}`);
console.log(`Datenordner: ${backend.dataDir}`);
console.log(`Admin ${ADMIN.user} / ${ADMIN.password}, Leser ${READER.user} / ${READER.password}, `
  + `Maschinen-Token ${MACHINE_TOKEN}, Status-Key ${STATUS_KEY}`);
console.log('Beenden mit Strg+C.');

let stopping = false;
const shutdown = async () => {
  if (stopping) return;
  stopping = true;
  await backend.stop();
  process.exit(0);
};
process.on('SIGINT', shutdown);
process.on('SIGTERM', shutdown);
setInterval(() => {}, 1 << 30);
