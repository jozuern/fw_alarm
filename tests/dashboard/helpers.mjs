// Gemeinsame Helfer für die Node-Tests des Dashboard-Frontends.
import vm from 'node:vm';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');

// Führt repo-relative Skripte (klassische Scripts, keine Module) nacheinander
// in EINEM vm-Kontext aus; `globals` landen vorher im globalen Objekt.
export function loadScripts(files, globals = {}) {
  const context = vm.createContext({});
  Object.assign(context, globals);
  for (const file of files) {
    const code = fs.readFileSync(path.join(repoRoot, file), 'utf8');
    vm.runInContext(code, context, { filename: file });
  }
  return context;
}

// Objekte aus dem vm-Kontext stammen aus einem anderen Realm und scheitern
// sonst an deepStrictEqual - über JSON in den Haupt-Realm holen.
export const plain = (v) => JSON.parse(JSON.stringify(v));
