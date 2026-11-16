(function (root) {
  'use strict';
  const FW = root.FW = root.FW || {};

  // ==== Notbremse beim Start ================================================
  // Fehlt ein Baustein (Datei nicht geladen, Skriptfehler beim Laden), bliebe
  // sonst eine leere schwarze Seite - ohne Alarm-Knopf und ohne Ausweg (die
  // Home-Bildschirm-App hat keinen Neu-laden-Knopf). Dann statt der App eine
  // schlichte Meldung mit „Neu laden“ und der alten Ansicht (classic.php),
  // gebaut ohne Preact. Dieselbe Meldung bei einem Fehler in boot() (unten).
  // Aufbau wie die Ersatzanzeige in lib/shell.php (Klassen .boot*).
  function showBootError(reason) {
    if (root.console && root.console.error) root.console.error('Alarm-Wächter: Start abgebrochen –', reason);
    const doc = root.document;
    const el = doc && doc.getElementById('app');
    if (!el) return;
    const make = (tag, cls, text) => {
      const node = doc.createElement(tag);
      if (cls) node.className = cls;
      if (text) node.textContent = text;
      return node;
    };
    const link = (href, text) => {
      const a = make('a', 'btn btn-tinted btn-block', text);
      a.setAttribute('href', href);
      return a;
    };
    const box = make('div', 'boot is-error');
    box.setAttribute('role', 'alert');
    box.appendChild(make('p', 'boot-title', 'Die Oberfläche konnte nicht starten.'));
    box.appendChild(make('p', 'boot-help', 'Bitte neu laden. Klappt es weiter nicht: die alte Ansicht öffnen – auch dort lässt sich alarmieren.'));
    const links = make('div', 'boot-links');
    links.appendChild(link('index.php', 'Neu laden'));
    links.appendChild(link('classic.php', 'Alte Ansicht (classic.php)'));
    box.appendChild(links);
    el.textContent = '';
    el.appendChild(box);
  }

  const missing = [
    [root.htmPreact, 'Preact (assets/vendor)'], [FW.logic, 'logic.js'], [FW.api, 'api.js'], [FW.store, 'store.js'],
    [FW.ui, 'ui.js'], [FW.views && FW.views.Overview, 'view-overview.js']
  ].filter(([part]) => !part).map(([, name]) => name);
  if (missing.length) {
    showBootError(`fehlt: ${missing.join(', ')}`);
    return;
  }

  const P = root.htmPreact;
  const { html, render, useState, useEffect, useRef } = P;
  const store = FW.store;
  const ui = FW.ui;
  const cx = ui.cx;

  // ==== Start der Oberfläche: Router, Rahmen, Tab-Titel, Design =============
  // Adressen per Hash (#/uebersicht, #/empfaenger/3, #/verlauf?filter=alarm,
  // #/einstellungen/meldungen …), damit Zurück-Geste und Neuladen gehen.
  // Schmal (< 900 px): Tab-Leiste unten; breit: Seitenleiste links.

  const SECTIONS = ['uebersicht', 'empfaenger', 'verlauf', 'einstellungen'];
  const NAV = [
    { id: 'uebersicht', label: 'Übersicht', icon: 'overview' },
    { id: 'empfaenger', label: 'Empfänger', icon: 'people' },
    { id: 'verlauf', label: 'Verlauf', icon: 'list' },
    { id: 'einstellungen', label: 'Einstellungen', icon: 'settings' }
  ];
  const WIDE_QUERY = '(min-width: 900px)';
  const LIGHT_QUERY = '(prefers-color-scheme: light)';
  const THEME_STORE = 'fwAlarm.theme';
  const THEMES = ['dark', 'light', 'system'];
  const THEME_COLOR = { dark: '#000000', light: '#f2f2f7' };
  const TITLE = 'Alarm-Wächter';

  // ---- Router ----------------------------------------------------------------
  const decode = (s) => {
    try { return decodeURIComponent(s); } catch (err) { return s; }
  };

  // '#/verlauf?filter=alarm' -> {path: ['verlauf'], query: {filter: 'alarm'}}.
  // Unbekannter Bereich (oder leer) -> Übersicht, ohne Parameter.
  function parseRoute(hash) {
    const raw = String(hash || '').replace(/^#/, '');
    const qi = raw.indexOf('?');
    const pathPart = qi >= 0 ? raw.slice(0, qi) : raw;
    const path = pathPart.split('/').filter(Boolean).map(decode);
    if (!path.length || !SECTIONS.includes(path[0])) return { path: ['uebersicht'], query: {} };
    const query = {};
    if (qi >= 0) new URLSearchParams(raw.slice(qi + 1)).forEach((v, k) => { query[k] = v; });
    return { path, query };
  }

  function navigate(hash) {
    const target = `#/${String(hash || '').replace(/^#?\/*/, '')}`;
    if (root.location.hash !== target) root.location.hash = target;
  }

  // Aktuelle Adresse lesen; eine unbekannte wird (ohne neuen Verlaufseintrag)
  // durch #/uebersicht ersetzt, damit Zurück nicht wieder ins Leere führt.
  function currentRoute() {
    const hash = root.location.hash;
    const route = parseRoute(hash);
    const section = hash.replace(/^#\/*/, '').split(/[/?]/)[0];
    if (hash && hash !== '#' && hash !== '#/' && !SECTIONS.includes(decode(section)) && root.history && root.history.replaceState) {
      root.history.replaceState(null, '', '#/uebersicht');
    }
    return route;
  }

  function useRoute() {
    const [route, setRoute] = useState(currentRoute);
    useEffect(() => {
      const on = () => setRoute(currentRoute());
      root.addEventListener('hashchange', on);
      return () => root.removeEventListener('hashchange', on);
    }, []);
    return route;
  }

  function useMedia(query) {
    const [match, setMatch] = useState(() => !!(root.matchMedia && root.matchMedia(query).matches));
    useEffect(() => {
      const mq = root.matchMedia ? root.matchMedia(query) : null;
      if (!mq) return undefined;
      const on = () => setMatch(mq.matches);
      if (mq.addEventListener) mq.addEventListener('change', on); else mq.addListener(on);
      on();
      return () => { if (mq.removeEventListener) mq.removeEventListener('change', on); else mq.removeListener(on); };
    }, [query]);
    return match;
  }

  // ---- Design pro Gerät (localStorage, jeder Zugriff abgesichert) -----------
  function getTheme() {
    try {
      const v = root.localStorage.getItem(THEME_STORE);
      return THEMES.includes(v) ? v : 'dark';
    } catch (err) {
      return 'dark';
    }
  }

  function applyTheme(theme) {
    const t = THEMES.includes(theme) ? theme : 'dark';
    const doc = root.document;
    doc.documentElement.setAttribute('data-theme', t);
    // Farbe der Browser-/Statusleiste passend zum Hintergrund.
    const light = t === 'light' || (t === 'system' && root.matchMedia && root.matchMedia(LIGHT_QUERY).matches);
    const meta = doc.querySelector('meta[name="theme-color"]');
    if (meta) meta.setAttribute('content', light ? THEME_COLOR.light : THEME_COLOR.dark);
    return t;
  }

  function setTheme(theme) {
    const t = THEMES.includes(theme) ? theme : 'dark';
    try { root.localStorage.setItem(THEME_STORE, t); } catch (err) { /* ohne Speicher gilt es nur bis zum Neuladen */ }
    return applyTheme(t);
  }

  // ---- Tab-Titel und Badge nach dem Urteil ----------------------------------
  function titleIcon(key, tone) {
    if (key === 'auth') return '🔒';
    if (tone === 'ok') return '🟢';
    if (tone === 'crit' || tone === 'alarm') return '🔴';
    return '🟡';
  }

  // Badge am Übersicht-Eintrag, sobald das Urteil nicht grün ist: Zahl der
  // Hinweise, sonst „!“. Beim allerersten Laden (noch keine Antwort) keins -
  // da gibt es noch keine Aussage, nur den gelben Tab-Titel.
  function badgeOf(key, tone, hintCount) {
    if (tone === 'ok' || key === 'loading') return null;
    const n = Number(hintCount) || 0;
    return {
      text: n > 0 ? String(n) : '!',
      tone: tone === 'crit' || tone === 'alarm' ? 'crit' : 'warn',
      srText: n > 0 ? (n === 1 ? '1 Hinweis' : `${n} Hinweise`) : 'Achtung'
    };
  }

  function useVerdictSignature() {
    // Nur einfache Werte auswählen: derive() liefert jede Sekunde neue Objekte.
    const sig = store.useStore((s) => {
      const d = store.derive(s);
      return `${d.verdict.key}|${d.verdict.tone}|${d.hints.length}`;
    });
    const [key, tone, hints] = sig.split('|');
    return { key, tone, hints: Number(hints) };
  }

  // ---- Rahmen ------------------------------------------------------------------
  function logout() {
    const form = root.document.getElementById('logout-form');
    if (!form) return;
    store.forgetDevice();   // gemerkte Namen gehen mit der Abmeldung
    form.submit();
  }

  function NavLabel({ item, badge }) {
    return html`<span class="nav-label">${item.label}</span>${item.id === 'uebersicht' && badge ? html`<${ui.Badge} ...${badge}/>` : null}`;
  }

  function TabBar({ section, badge }) {
    return html`<nav class="tabbar" aria-label="Bereiche">${NAV.map((item) => {
      const on = item.id === section;
      const b = item.id === 'uebersicht' ? badge : null;
      // Sichtbares Badge am Symbol, die Ansage dazu NACH dem Namen
      // („Übersicht, 2 Hinweise“).
      return html`<a key=${item.id} class=${cx('tab', on && 'is-on')} href=${`#/${item.id}`} aria-current=${on ? 'page' : undefined}>
        <span class="tab-icon"><${ui.Icon} name=${item.icon}/>${b ? html`<${ui.Badge} text=${b.text} tone=${b.tone}/>` : null}</span>
        <span class="tab-label">${item.label}</span>${b ? html`<span class="sr-only">, ${b.srText}</span>` : null}
      </a>`;
    })}</nav>`;
  }

  function StatusAge() {
    const text = store.useStore(ui.statusAgeText);
    return html`<p class="side-age">${text}</p>`;
  }

  function Sidebar({ section, badge, verdict }) {
    const user = store.useStore((s) => s.user);
    const readOnly = store.useStore((s) => s.readOnly);
    const total = store.useStore((s) => {
      const sum = store.derive(s).keysSummary;
      return sum ? sum.total : -1;
    });
    let lamp = verdict.tone;
    if (verdict.key === 'loading' || verdict.key === 'auth') lamp = 'unknown';
    else if (verdict.key === 'alarm') lamp = 'alarm';
    return html`<aside class="sidebar">
      <div class="brand"><span class=${cx('lamp', `lamp-${lamp}`)} aria-hidden="true"></span><span class="brand-name">${TITLE}</span></div>
      <nav class="side-nav" aria-label="Bereiche">${NAV.map((item) => {
        const on = item.id === section;
        return html`<a key=${item.id} class=${cx('side-item', on && 'is-on')} href=${`#/${item.id}`} aria-current=${on ? 'page' : undefined}>
          <${ui.Icon} name=${item.icon}/>
          <${NavLabel} item=${item} badge=${badge}/>
          ${item.id === 'empfaenger' && total >= 0 ? html`<span class="side-count">${total}</span>` : null}
        </a>`;
      })}</nav>
      <div class="side-foot">
        <p class="side-user"><span>${user || 'Angemeldet'}${readOnly ? ' (nur lesen)' : ''}</span><span aria-hidden="true"> · </span><button type="button" class="link-btn" onClick=${logout}>Abmelden</button></p>
        <${StatusAge}/>
      </div>
    </aside>`;
  }

  // Oranges Band auf jeder Seite, solange der Demo-Modus aktiv ist (Spec §6.1).
  const SYNC_TEXT = {
    synced: 'Box hat übernommen',
    pending: 'Box hat NOCH NICHT übernommen – ein Relaisalarm ginge noch an alle!',
    offline: 'Box offline – sie würde noch mit der alten Liste alarmieren!',
    unknown: 'Abgleich unbekannt – keine aktuelle Antwort vom NAS'
  };
  function DemoBand() {
    const label = store.useStore((s) => (s.demo && s.demo.label) || 'den Test-Empfänger');
    const sync = store.useStore((s) => store.derive(s).demoSync || 'unknown');
    const readOnly = store.useStore((s) => s.readOnly);
    const body = html`<${ui.Icon} name="demo" class="band-icon"/><span class="band-text">Demo-Modus: nur ${label} · ${SYNC_TEXT[sync] || SYNC_TEXT.unknown}</span>`;
    return html`<div class=${cx('demo-band', sync !== 'synced' && 'is-pending')} role="status">${readOnly
      ? html`<div class="band-inner">${body}</div>`
      : html`<a class="band-inner" href="#/einstellungen/demo">${body}<${ui.Icon} name="chevron" class="band-chev"/></a>`}</div>`;
  }

  // Schmales Band auf jeder Seite, solange der Stand unsicher ist: keine
  // frische Antwort vom NAS (veraltet/Störung) bzw. abgemeldet mit stehen
  // gebliebenem Alarm-Ergebnis. Orange, rot wenn der letzte bekannte Stand
  // „Box offline“ war; Text aus FW.logic.unsureBand (Wortlaut wie das Urteil).
  // Auf der Übersicht ersetzt es die Problemzeile der Status-Karte.
  function UnsureBand() {
    const sig = store.useStore((s) => {
      const b = store.derive(s).unsureBand;
      return b ? `${b.tone}|${b.text}` : '';
    });
    if (!sig) return null;
    const cut = sig.indexOf('|');
    const tone = sig.slice(0, cut);
    return html`<div class=${cx('unsure-band', tone === 'crit' && 'is-crit')} role="status">
      <div class="band-inner"><${ui.Icon} name="alert" class="band-icon"/><span class="band-text">${sig.slice(cut + 1)}</span></div>
    </div>`;
  }

  // Props für alle Ansichten: wide, query, sub (Pfad nach dem Bereich),
  // id (= sub[0], z.B. Empfänger-id) und path (= sub, für die Einstellungen).
  const VIEW_OF = { uebersicht: 'Overview', empfaenger: 'Recipients', verlauf: 'Log', einstellungen: 'Settings' };
  const VIEW_TITLE = { uebersicht: 'Übersicht', empfaenger: 'Empfänger', verlauf: 'Verlauf', einstellungen: 'Einstellungen' };

  function App() {
    const wide = useMedia(WIDE_QUERY);
    const route = useRoute();
    const verdict = useVerdictSignature();
    const demoOn = store.useStore((s) => !!(s.demo && s.demo.enabled));
    const unsure = store.useStore((s) => !!store.derive(s).unsureBand);
    const mainRef = useRef(null);
    const section = route.path[0];
    const sub = route.path.slice(1);
    const routeKey = `${route.path.join('/')}?${new URLSearchParams(route.query).toString()}`;
    const badge = badgeOf(verdict.key, verdict.tone, verdict.hints);

    // Store: welche Abfragen die sichtbare Seite braucht.
    useEffect(() => { store.setRoute(route, wide); }, [routeKey, wide]);

    useEffect(() => { root.document.title = `${titleIcon(verdict.key, verdict.tone)} ${TITLE}`; }, [verdict.key, verdict.tone]);

    // Seitenwechsel: nach oben und Fokus auf den großen Titel (Screenreader
    // hören die neue Seite). Ein Empfänger-Detail ist Sheet/Panel - kein
    // Seitenwechsel.
    const pageKey = section === 'empfaenger' ? section : route.path.join('/');
    const firstPage = useRef(true);
    useEffect(() => {
      if (firstPage.current) {
        firstPage.current = false;
        return;
      }
      root.scrollTo(0, 0);
      const h1 = mainRef.current && mainRef.current.querySelector('h1');
      if (h1) h1.focus({ preventScroll: true });
    }, [pageKey]);

    // Fehlt eine Ansicht (Datei nicht geladen), nicht nur den Titel zeigen,
    // sondern sagen, was los ist - mit Ausweg.
    const View = (FW.views && FW.views[VIEW_OF[section]]) || null;
    const props = { wide, query: route.query, sub, id: sub.length ? sub[0] : null, path: sub };
    const bands = unsure || demoOn;
    return html`<div class=${cx('shell', wide ? 'is-wide' : 'is-narrow', bands && 'has-band')}>
      ${wide ? html`<${Sidebar} section=${section} badge=${badge} verdict=${verdict}/>` : null}
      <div class="main-col">
        ${bands ? html`<div class="bands">${unsure ? html`<${UnsureBand}/>` : null}${demoOn ? html`<${DemoBand}/>` : null}</div>` : null}
        <main class="content" id="main" ref=${mainRef}>
          <${ui.Boundary} key=${section} title=${VIEW_TITLE[section]}>
            ${View ? html`<${View} ...${props}/>`
              : html`<${ui.Fallback} title=${VIEW_TITLE[section]} text="Diese Ansicht konnte nicht geladen werden. Die anderen Bereiche funktionieren weiter."/>`}
          </${ui.Boundary}>
        </main>
      </div>
      ${wide ? null : html`<${TabBar} section=${section} badge=${badge}/>`}
      <${ui.ConfirmHost}/>
    </div>`;
  }

  // ---- Start -------------------------------------------------------------------
  function meta(name) {
    const el = root.document.querySelector(`meta[name="${name}"]`);
    return el ? el.getAttribute('content') || '' : '';
  }

  function boot() {
    const doc = root.document;
    applyTheme(getTheme());
    // „System“: bei Wechsel des Geräte-Designs die Leistenfarbe nachziehen.
    if (root.matchMedia) {
      const mq = root.matchMedia(LIGHT_QUERY);
      const on = () => applyTheme(getTheme());
      if (mq.addEventListener) mq.addEventListener('change', on); else mq.addListener(on);
    }
    ui.announce('');   // Live-Region früh anlegen, damit die erste Ansage ankommt
    store.init({
      role: meta('role'), user: meta('user'), csrf: meta('csrf'),
      publicBase: meta('public-base'), muteMax: Number(meta('mute-max')) || 0
    });
    store.setRoute(currentRoute(), !!(root.matchMedia && root.matchMedia(WIDE_QUERY).matches));
    store.start();
    // Im Hintergrund geöffnet (z.B. neuer Tab am PC): wie beim Verstecken
    // gleich wieder anhalten - die Regeln dafür stehen im Store.
    if (doc.hidden) doc.dispatchEvent(new Event('visibilitychange'));
    // Die Ersatzanzeige aus lib/shell.php („Wird geladen…“) erst jetzt
    // entfernen; die äußerste Fehlergrenze fängt Fehler im Rahmen selbst
    // (Bänder, Tab-Leiste, Seitenleiste) - auch dann bleibt ein Ausweg.
    const el = doc.getElementById('app');
    el.textContent = '';
    render(html`<${ui.Boundary} title=${TITLE} text="Die Oberfläche konnte nicht angezeigt werden."><${App}/></${ui.Boundary}>`, el);
  }

  FW.main = { SECTIONS, THEMES, parseRoute, navigate, getTheme, setTheme, App };

  try {
    boot();
  } catch (err) {
    showBootError(err);
  }
})(typeof window !== 'undefined' ? window : globalThis);
