// Nur PHP: Selbsttest des Datenordners (api/health.php). Der Wächter ruft
// ihn über den Webserver auf, damit er als Web-Benutzer prüft, ob das
// Dashboard noch in seinen Datenordner schreiben kann. Er antwortet nur
// direkt vom Rechner selbst - was ein Proxy oder Tunnel weiterreicht, kommt
// von außen und bekommt 404 (es soll dort weder etwas verraten noch einen
// Schreibzugriff auslösen). Die Go-Fassung hat diese Adresse nicht mehr (sie
// prüft ihre Ablage im eigenen Prüf-Lauf); das Gegenstück nennt die Tabelle
// "Rule → test" im Plan. Diese Datei verschwindet mit PHP.
import assert from 'node:assert/strict';
import { after, before, beforeEach, describe, it } from 'node:test';
import { BACKEND_KIND, request, startBackend } from './harness.mjs';

let backend;

describe('Nur PHP: Selbsttest api/health.php', { skip: BACKEND_KIND !== 'php' && 'nur PHP' }, () => {
  before(async () => { backend = await startBackend(); });
  after(async () => { await backend.stop(); });
  beforeEach(async () => { await backend.reset(); });

  const health = (headers = {}) => request(`${backend.base}/api/health.php`, { headers });

  // Kopfzeilen, die ein Proxy oder Tunnel (Nginx, Cloudflare) mitbringt.
  const PROXY_HEADERS = {
    'X-Forwarded-For': '203.0.113.9',
    'X-Forwarded-Host': 'alarm.example.org',
    'X-Forwarded-Proto': 'https',
    'X-Real-IP': '203.0.113.9',
    'Forwarded': 'for=203.0.113.9;proto=https',
    'Via': '1.1 proxy.example.org',
    'CF-Connecting-IP': '203.0.113.9',
    'CF-Ray': '8a1b2c3d4e5f6789-FRA',
    'CDN-Loop': 'cloudflare',
  };

  describe('Selbsttest vom Rechner selbst', () => {
    it('Datenordner beschreibbar: 200 "ok=1 / step=ok" als Klartext, die Probe-Datei ist wieder weg', async () => {
      const r = await health();
      assert.equal(r.status, 200);
      assert.equal(r.text, 'ok=1\nstep=ok\n');
      assert.match(r.headers.get('content-type'), /^text\/plain/);
      assert.match(r.headers.get('cache-control'), /no-store/);
      assert.equal(backend.data.exists('health_probe.json'), false);
    });

    it('die Probe-Datei lässt sich nicht schreiben: 500 "ok=0 / step=write"', async () => {
      backend.data.block('health_probe.json');
      const r = await health();
      assert.equal(r.status, 500);
      assert.equal(r.text, 'ok=0\nstep=write\n');
      backend.data.unblock('health_probe.json');
      assert.equal((await health()).text, 'ok=1\nstep=ok\n', 'danach wieder in Ordnung');
    });

    it('die Sperrdatei lässt sich nicht öffnen: 500 "ok=0 / step=lock"', async () => {
      backend.data.block('health.lock');
      const r = await health();
      assert.equal(r.status, 500);
      assert.equal(r.text, 'ok=0\nstep=lock\n');
      backend.data.unblock('health.lock');
      assert.equal((await health()).text, 'ok=1\nstep=ok\n', 'danach wieder in Ordnung');
    });
  });

  describe('Über einen Proxy weitergereicht', () => {
    for (const [name, value] of Object.entries(PROXY_HEADERS)) {
      it(`mit ${name}: 404 ohne Inhalt`, async () => {
        const r = await health({ [name]: value });
        assert.equal(r.status, 404);
        assert.equal(r.text, '');
        // Nichts geprüft, nichts geschrieben.
        assert.equal(backend.data.exists('health.lock'), false);
        assert.equal(backend.data.exists('health_probe.json'), false);
      });
    }
  });
});
