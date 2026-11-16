// Kontrast der neuen Oberfläche (WCAG AA), berechnet aus den Tokens in
// app.css: jedes Schrift-/Hintergrund-Paar, das das Stylesheet bildet, in
// beiden Designs und in allen Ebenen (Seite, Sheet, Panel). Text ≥ 4,5 : 1,
// nur echte große Schrift (≥ 19 px fett) und Symbole/Fokusring ≥ 3 : 1.
// Tabelle ausgeben: CONTRAST_TABLE=1 node --test tests/dashboard/contrast.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const read = (rel) => readFileSync(new URL(rel, import.meta.url), 'utf8');
const css = read('../../nas_dashboard/assets/app.css').replace(/\/\*[\s\S]*?\*\//g, '');
const uiJs = read('../../nas_dashboard/assets/app/ui.js');

// ---- CSS in Regeln zerlegen (@media aufgelöst, @keyframes übersprungen) ----
function rules(text, media = '') {
  const out = [];
  let i = 0;
  while (i < text.length) {
    const open = text.indexOf('{', i);
    if (open < 0) break;
    const sel = text.slice(i, open).trim();
    let depth = 1;
    let j = open + 1;
    while (depth && j < text.length) {
      if (text[j] === '{') depth++;
      else if (text[j] === '}') depth--;
      j++;
    }
    const body = text.slice(open + 1, j - 1);
    if (sel.startsWith('@media')) out.push(...rules(body, sel));
    else if (!sel.startsWith('@')) out.push({ sels: sel.split(',').map((s) => s.trim()), body, media });
    i = j;
  }
  return out;
}
const ALL = rules(css);
function props(sel, media = '') {
  const vars = {};
  ALL.filter((r) => r.media === media && r.sels.includes(sel)).forEach((r) => {
    for (const m of r.body.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) vars[m[1]] = m[2].trim();
  });
  return vars;
}
const DARK = props(':root');
const LIGHT = props(':root[data-theme="light"]');
const SYSTEM = props(':root[data-theme="system"]', '@media (prefers-color-scheme: light)');
const LAYER = { page: {}, sheet: props('.sheet'), panel: props('.panel') };
// Hellere Flächen IN Sheets/Panels (Gruppen, Knöpfe) mit eigenen Schriftfarben.
const RAISED = { page: {}, sheet: props('.sheet .group-body'), panel: props('.panel .group-body') };

function scope(theme, layer, raised) {
  return Object.assign({}, DARK, theme === 'light' ? LIGHT : {}, LAYER[layer], raised ? RAISED[layer] : {});
}

function resolve(value, vars, seen = 0) {
  if (seen > 20) throw new Error('Zyklus bei ' + value);
  const v = String(value).trim();
  const m = v.match(/^var\(\s*(--[\w-]+)\s*(?:,\s*([\s\S]+))?\)$/);
  if (!m) return v;
  if (vars[m[1]] !== undefined) return resolve(vars[m[1]], vars, seen + 1);
  if (m[2] !== undefined) return resolve(m[2], vars, seen + 1);
  throw new Error('Token fehlt: ' + m[1]);
}

function parseColor(s) {
  let m = s.match(/^#([0-9a-f]{3})$/i);
  if (m) return [...m[1]].map((c) => parseInt(c + c, 16)).concat(1);
  m = s.match(/^#([0-9a-f]{6})$/i);
  if (m) return [0, 2, 4].map((k) => parseInt(m[1].slice(k, k + 2), 16)).concat(1);
  m = s.match(/^rgba?\(\s*([\d.]+)\s*,\s*([\d.]+)\s*,\s*([\d.]+)\s*(?:,\s*([\d.]+)\s*)?\)$/i);
  if (m) return [Number(m[1]), Number(m[2]), Number(m[3]), m[4] === undefined ? 1 : Number(m[4])];
  throw new Error('Farbe nicht lesbar: ' + s);
}
// Schichten von unten nach oben übereinander legen (die unterste ist deckend).
function flatten(layers, vars) {
  let rgb = null;
  for (const layer of layers) {
    const [r, g, b, a] = parseColor(resolve(layer.startsWith('--') ? `var(${layer})` : layer, vars));
    if (!rgb) {
      if (a < 1) throw new Error('unterste Schicht nicht deckend: ' + layer);
      rgb = [r, g, b];
    } else {
      rgb = [r, g, b].map((c, k) => c * a + rgb[k] * (1 - a));
    }
  }
  return rgb;
}
const lin = (c) => {
  const x = c / 255;
  return x <= 0.04045 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4;
};
const lum = ([r, g, b]) => 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
const ratio = (a, b) => {
  const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
};

// ---- Paare ----------------------------------------------------------------
// Ebene „page“: Seite (--bg) und Gruppen/Knöpfe darauf. „sheet“: Fläche des
// Sheets und die helleren Gruppen darin. „panel“: Panel neben der Liste (PC).
const BASE = { page: ['--bg'], sheet: ['--sheet-bg'], panel: ['--bg', '--surface'] };
const CARD = (layer) => [...BASE[layer], 'var(--group-bg, var(--surface))'];
const TEXT = ['--text', '--text-2', '--accent-text', '--ok-text', '--warn-text', '--crit-text', '--info-text', '--unknown-text'];
const PILLS = [['ok', '--ok-text', '--tint-ok'], ['warn', '--warn-text', '--tint-warn'], ['crit', '--crit-text', '--tint-crit'],
  ['info', '--info-text', '--tint-info'], ['neutral', '--pill-neutral-text', '--pill-neutral-bg']];
const AA = 4.5;
const LARGE = 3;   // große Schrift bzw. Symbole/Fokusring (WCAG 1.4.3 / 1.4.11)

// Gewählte Zeile der Empfängerliste (PC): eigene Fläche, hellere Schriftfarben.
const SELECTED = props('.rcp-list .row-main[aria-current="page"]');
// Gruppen der Einstellungen: Segment-Leiste eine Stufe heller als die Gruppe.
const SET_GROUP = props('.set .group-body');

function pairs() {
  const list = [];
  // Paare auf einer Gruppe/einem Knopf (CARD) gelten mit den Werten der helleren Fläche.
  // extra: Variablen, die eine Regel an dieser Stelle zusätzlich setzt.
  const add = (theme, layer, name, fg, bg, min = AA, extra = null) => list.push({
    theme, layer, name, fg, bg, min, extra, raised: bg.includes('var(--group-bg, var(--surface))')
  });
  for (const theme of ['dark', 'light']) {
    for (const layer of ['page', 'sheet', 'panel']) {
      for (const t of TEXT) {
        add(theme, layer, `${t} auf Fläche`, t, BASE[layer]);
        add(theme, layer, `${t} in Gruppe`, t, CARD(layer));
      }
      for (const [name, fg, tint] of PILLS) add(theme, layer, `Pille ${name}`, fg, [...CARD(layer), tint]);
      add(theme, layer, 'Segment Text', '--text', [...BASE[layer], '--seg-track']);
      add(theme, layer, 'Segment aktiv', '--text', [...BASE[layer], '--seg-track', '--seg-on']);
      add(theme, layer, 'Fokusring auf Fläche', '--info', BASE[layer], LARGE);
      add(theme, layer, 'Fokusring in Gruppe', '--info', CARD(layer), LARGE);
      add(theme, layer, 'Kachel rot (Symbol)', '#ffffff', [...CARD(layer), '--alarm'], LARGE);
      add(theme, layer, 'Kachel orange (Symbol)', '#000000', [...CARD(layer), '--warn'], LARGE);
      add(theme, layer, 'Kachel grün (Symbol)', '#000000', [...CARD(layer), '--ok'], LARGE);
      add(theme, layer, 'Kachel blau (Symbol)', '#ffffff', [...CARD(layer), '--info'], LARGE);
      add(theme, layer, 'Kachel grau (Symbol)', '#ffffff', [...CARD(layer), '--tile-neutral'], LARGE);
    }
    // Verlauf: Suchfeld (Platzhalter, Lupe, Leeren-Kreuz) liegt auf --field-bg.
    add(theme, 'page', 'Verlauf Suchfeld Platzhalter', '--text-2', ['--bg', '--field-bg']);
    add(theme, 'page', 'Tab-Leiste inaktiv', '--text-2', ['--bg', '--bar-bg']);
    add(theme, 'page', 'Tab-Leiste aktiv', '--accent-text', ['--bg', '--bar-bg']);
    add(theme, 'page', 'Seitenleiste Eintrag', '--side-text', ['--side-bg']);
    add(theme, 'page', 'Seitenleiste Fuß/Anzahl', '--text-2', ['--side-bg']);
    add(theme, 'page', 'Seitenleiste gewählt', '--text', ['--side-bg', '--side-on']);
    add(theme, 'page', 'Demo-Band', '#000000', ['--bg', '--warn']);
    // Band „keine aktuelle Antwort vom NAS“ (getönt, orange bzw. rot).
    add(theme, 'page', 'Band NAS-Störung orange', '--warn-text', ['--bg', '--surface']);
    add(theme, 'page', 'Band NAS-Störung rot', '--crit-text', ['--bg', '--status-crit-bg']);
    // Ersatzanzeige beim Start (shell.php/main.js) und Fehlergrenzen (ui.Fallback).
    add(theme, 'page', 'Start-Ersatz: Titel/Hinweis', '--text-2', ['--bg']);
    add(theme, 'page', 'Start-Ersatz/Fehlergrenze: Meldung', '--text', ['--bg']);
    add(theme, 'page', 'Start-Ersatz/Fehlergrenze: Knöpfe', '--accent-text', CARD('page'));
    add(theme, 'page', 'Status-Karte: Neu anmelden', '--accent-text', ['--bg', '--surface']);
    add(theme, 'page', 'Badge rot', '#ffffff', ['--bg', '--badge-crit']);
    add(theme, 'page', 'Badge orange', '#000000', ['--bg', '--warn']);
    add(theme, 'page', 'Alarm-Knopf (19 px fett)', '#ffffff', ['--bg', '--alarm'], LARGE);
    add(theme, 'sheet', 'Bestätigen rot (19 px fett)', '#ffffff', ['--surface', '--alarm'], LARGE);
    add(theme, 'sheet', 'Halten: Etikett', '--hold-ink', ['--surface', '--hold-track']);
    add(theme, 'sheet', 'Halten: Etikett auf Füllung (19 px fett)', '#ffffff', ['--surface', '--alarm'], LARGE);
    add(theme, 'sheet', 'Bestätigung Text', '--confirm-text', ['--surface']);
    add(theme, 'sheet', 'Bestätigung Restzeit', '--text-2', ['--surface']);
    add(theme, 'sheet', 'Abbrechen', '--info-text', ['--surface']);
    add(theme, 'sheet', 'Schließen (Kopf)', '--accent-text', ['--sheet-bg']);
    // Übersicht: Status-Karte (rot getönt bzw. Fläche), Kreis-Symbole,
    // Befehlszeile, Ergebnis-Karte, Anrufen, Farbpunkte der Ereignisse.
    add(theme, 'page', 'Status-Karte rot: Titel', '--crit-text', ['--bg', '--status-crit-bg']);
    add(theme, 'page', 'Status-Karte rot: Satz/Alter', '--text-2', ['--bg', '--status-crit-bg']);
    add(theme, 'page', 'Status-Karte rot: Schrift', '--text', ['--bg', '--status-crit-bg']);
    add(theme, 'page', 'Status-Karte orange: Titel', '--warn-text', ['--bg', '--surface']);
    add(theme, 'page', 'Status-Karte: Satz/Alter', '--text-2', ['--bg', '--surface']);
    add(theme, 'page', 'Status-Kreis grün (Symbol)', '#000000', ['--bg', '--ok'], LARGE);
    add(theme, 'page', 'Status-Kreis orange (Symbol)', '#000000', ['--bg', '--warn'], LARGE);
    add(theme, 'page', 'Status-Kreis rot (Symbol)', '#ffffff', ['--bg', '--alarm'], LARGE);
    add(theme, 'page', 'Status-Kreis grau (Symbol)', '#ffffff', ['--bg', '--tile-neutral'], LARGE);
    add(theme, 'page', 'Befehlszeile orange', '--warn-text', ['--bg', '--surface']);
    add(theme, 'page', 'Befehlszeile erledigt/Symbol', '--text-2', ['--bg', '--surface']);
    add(theme, 'page', 'Befehlszeile Abbrechen', '--accent-text', ['--bg', '--surface']);
    add(theme, 'page', 'Ergebnis: Titel grün / ✓', '--ok-text', ['--bg', '--surface']);
    add(theme, 'page', 'Ergebnis: Titel rot / NICHT erreicht', '--crit-text', ['--bg', '--surface']);
    add(theme, 'page', 'Ergebnis: Hinweis orange', '--warn-text', ['--bg', '--surface']);
    add(theme, 'page', 'Ergebnis: Grund/Erreicht/Zeit', '--text-2', ['--bg', '--surface']);
    add(theme, 'page', 'Ergebnis: Ausblenden', '--accent-text', ['--bg', '--surface']);
    add(theme, 'page', 'Ergebnis: ✕-Kreis (Symbol)', '#ffffff', ['--bg', '--surface', '--alarm'], LARGE);
    add(theme, 'page', 'Nachsenden rot (19 px fett)', '#ffffff', ['--bg', '--surface', '--alarm'], LARGE);
    add(theme, 'page', 'Anrufen (grüne Pille)', '#000000', ['--bg', '--surface', '--ok']);
    add(theme, 'page', 'Lese-Rolle: Satz', '--text-2', ['--bg', '--surface']);
    add(theme, 'page', 'Ereignis-Punkt rot', '--crit-text', ['--bg', '--surface'], LARGE);
    add(theme, 'page', 'Ereignis-Punkt orange', '--warn-text', ['--bg', '--surface'], LARGE);
    add(theme, 'page', 'Ereignis-Punkt grün', '--ok-text', ['--bg', '--surface'], LARGE);
    add(theme, 'page', 'Ereignis-Punkt grau', '--text-2', ['--bg', '--surface'], LARGE);
    // Empfänger: gewählte Zeile am PC (Name, Unterzeile, Warnung, Pillen,
    // Akzent-Strich), Knöpfe/Felder IN einer Gruppe (Fläche --inset-field).
    const sel = [...CARD('page'), '--row-selected'];
    add(theme, 'page', 'Empfänger gewählt: Name', '--text', sel, AA, SELECTED);
    add(theme, 'page', 'Empfänger gewählt: Unterzeile', '--text-2', sel, AA, SELECTED);
    add(theme, 'page', 'Empfänger gewählt: Warnung', '--warn-text', sel, AA, SELECTED);
    for (const [name, fg, tint] of PILLS) add(theme, 'page', `Empfänger gewählt: Pille ${name}`, fg, [...sel, tint], AA, SELECTED);
    add(theme, 'page', 'Empfänger gewählt: Strich (Grafik)', '--accent', sel, LARGE, SELECTED);
    add(theme, 'sheet', 'Empfänger: Knopf/Feld in Gruppe', '--accent-text', ['--sheet-bg'], AA, RAISED.sheet);
    add(theme, 'sheet', 'Empfänger: Feld-Text in Gruppe', '--text', ['--sheet-bg']);
    add(theme, 'panel', 'Empfänger: Knopf/Feld in Gruppe', '--accent-text', ['--bg', '--surface'], AA, RAISED.panel);
    add(theme, 'panel', 'Empfänger: Feld-Text in Gruppe', '--text', ['--bg', '--surface']);
    // Einstellungen: Segmente IN Gruppen (eigene, hellere Leiste), Werte und
    // Erklärungen in Gruppen, Hinweise auf der Seite, Zustands-Karte des
    // Demo-Modus und der Knopf „zurück zu LIVE“.
    add(theme, 'page', 'Einstellungen: Segment in Gruppe', '--text', [...CARD('page'), '--seg-track'], AA, SET_GROUP);
    add(theme, 'page', 'Einstellungen: Segment in Gruppe aktiv', '--text', [...CARD('page'), '--seg-track', '--seg-on'], AA, SET_GROUP);
    add(theme, 'page', 'Einstellungen: Auswahl-Wert/Erklärung/Zusatz', '--text-2', CARD('page'));
    add(theme, 'page', 'Einstellungen: Abmelden/Alte Ansicht', '--accent-text', CARD('page'));
    add(theme, 'page', 'Einstellungen: Haken (Symbol)', '--accent-text', CARD('page'), LARGE);
    add(theme, 'page', 'Einstellungen: Warnung in Zeile', '--warn-text', CARD('page'));
    add(theme, 'page', 'Einstellungen: Liste beschädigt in Zeile', '--crit-text', CARD('page'));
    add(theme, 'page', 'Einstellungen: Warnung auf der Seite', '--warn-text', ['--bg']);
    add(theme, 'page', 'Einstellungen: Gerade ist Ruhezeit', '--info-text', ['--bg']);
    add(theme, 'page', 'Einstellungen: letzter bekannter Stand', '--unknown-text', ['--bg']);
    add(theme, 'page', 'Einstellungen: Probealarm-Fenster (Link)', '--accent-text', ['--bg']);
    add(theme, 'page', 'Einstellungen: Probealarm-Fenster (Text)', '--text-2', ['--bg']);
    for (const [name, fg] of [['Text', '--text'], ['DEMO-MODUS / nicht übernommen', '--warn-text'], ['LIVE / übernommen', '--ok-text'],
      ['Box offline', '--crit-text'], ['Key/seit', '--text-2']]) {
      add(theme, 'page', `Einstellungen: Demo-Karte ${name}`, fg, ['--bg', '--surface']);
    }
    add(theme, 'page', 'Einstellungen: Demo beenden (Knopf)', '--crit-text', ['--bg', '--surface']);
    // Anmeldeseite: Marke und Fuß auf der Seite, Felder in der Karte
    // (--surface), Fehlerzeile, roter Knopf, Fokusring ums Feld.
    add(theme, 'page', 'Anmeldung: Untertitel/Fuß', '--text-2', ['--bg']);
    add(theme, 'page', 'Anmeldung: Feld-Bezeichnung', '--text-2', ['--bg', '--surface']);
    add(theme, 'page', 'Anmeldung: Eingabe', '--text', ['--bg', '--surface']);
    add(theme, 'page', 'Anmeldung: Fehler', '--crit-text', ['--bg']);
    add(theme, 'page', 'Anmeldung: Anmelden (19 px fett)', '#ffffff', ['--bg', '--alarm'], LARGE);
    add(theme, 'page', 'Anmeldung: Fokusring ums Feld', '--info', ['--bg', '--surface'], LARGE);
    // Bestätigungsseite der Kurzbefehl-Links (api/mute.php).
    for (const [name, fg] of [['grün', '--ok-text'], ['orange', '--warn-text'], ['rot', '--crit-text'], ['Text', '--text']]) {
      add(theme, 'page', `Kurzbefehl-Seite: ${name}`, fg, ['--bg', '--surface']);
    }
  }
  return list;
}

function evaluate() {
  return pairs().map((p) => {
    const vars = Object.assign(scope(p.theme, p.layer, p.raised), p.extra || {});
    const fg = flatten([...[p.bg].flat(), p.fg], vars);
    const bg = flatten([p.bg].flat(), vars);
    const r = ratio(fg, bg);
    return Object.assign({}, p, { r, ok: r >= p.min });
  });
}

test('Kontrast: alle Schrift-/Hintergrund-Paare erfüllen WCAG AA (beide Designs, alle Ebenen)', () => {
  const rows = evaluate();
  if (process.env.CONTRAST_TABLE) {
    const pad = (s, n) => String(s).padEnd(n);
    console.log(['Design', 'Ebene', 'Paar', 'Kontrast', 'Mindest', ''].map((s, k) => pad(s, [6, 6, 44, 9, 8, 2][k])).join(' '));
    rows.forEach((x) => console.log([pad(x.theme, 6), pad(x.layer, 6), pad(x.name, 44), pad(x.r.toFixed(2), 9), pad(x.min, 8), x.ok ? 'ok' : 'ZU WENIG'].join(' ')));
  }
  const bad = rows.filter((x) => !x.ok).map((x) => `${x.theme}/${x.layer} ${x.name}: ${x.r.toFixed(2)} < ${x.min}`);
  assert.deepEqual(bad, []);
});

test('Kontrast: die helleren Schriftfarben gelten für Gruppen UND Knöpfe in Sheets und Panels', () => {
  const rule = ALL.find((r) => r.sels.includes('.sheet .group-body'));
  assert.ok(rule, 'Regel für Gruppen im Sheet');
  for (const sel of ['.panel .group-body', '.sheet .btn:not(.btn-cancel)', '.panel .btn']) {
    assert.ok(rule.sels.includes(sel), `Regel deckt ${sel} ab`);
  }
  assert.deepEqual(RAISED.panel, RAISED.sheet);
  assert.ok(Object.keys(RAISED.sheet).length >= 4, 'hellere Werte gesetzt');
  // Die gewählte Empfänger-Zeile setzt dieselben helleren Werte.
  for (const [k, v] of Object.entries(RAISED.sheet)) assert.equal(SELECTED[k], v, `gewählte Zeile: ${k}`);
});

// Weiß auf Rot (Füllung des Halten-Knopfs) erreicht nur 3 : 1 - das genügt
// nur für große Schrift: mind. 19 px fett. Das Etikett nennt die Aktion
// („Halten: Alarm senden“), es muss also so groß bleiben.
test('Kontrast: Etikett des Halten-Knopfs bleibt große Schrift (≥ 19 px fett)', () => {
  const body = ALL.filter((r) => r.media === '' && r.sels.includes('.hold-label')).map((r) => r.body).join(';');
  const size = Number((body.match(/font-size:\s*(\d+)px/) || [])[1]);
  const weight = Number((body.match(/font-weight:\s*(\d+)/) || [])[1]);
  assert.ok(size >= 19, `.hold-label font-size ${size}`);
  assert.ok(weight >= 700, `.hold-label font-weight ${weight}`);
});

test('Kontrast: „System“ bei hellem Gerät hat genau die Hell-Werte', () => {
  assert.ok(Object.keys(LIGHT).length > 20, 'Hell-Block gefunden');
  assert.deepEqual(SYSTEM, LIGHT);
});

test('Jede Zustands-Farbe aus ui.js hat eine Regel in app.css', () => {
  const tones = [...new Set([...uiJs.match(/const TONE = \{([^}]+)\}/)[1].matchAll(/:\s*'(\w+)'/g)].map((m) => m[1]))];
  assert.ok(tones.length >= 6, 'TONE-Tabelle gefunden: ' + tones);
  const missing = tones.filter((t) => !new RegExp(`\\.tone-${t}\\b`).test(css));
  assert.deepEqual(missing, [], 'Zeilen-Farben ohne CSS');
  const pills = ['ok', 'warn', 'crit', 'info', 'neutral'].filter((t) => !new RegExp(`\\.pill-${t}\\b`).test(css));
  assert.deepEqual(pills, [], 'Pillen ohne CSS');
  const tiles = ['crit', 'warn', 'ok', 'info'].filter((t) => !new RegExp(`\\.tile-${t}\\b`).test(css));
  assert.deepEqual(tiles, [], 'Kacheln ohne CSS');
});
