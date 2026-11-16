(function (root) {
  'use strict';
  const FW = root.FW = root.FW || {};
  const P = root.htmPreact;
  const { html, h, Component, useState, useEffect, useLayoutEffect, useRef } = P;
  const store = FW.store;
  const L = FW.logic;

  // ==== Gemeinsame Bausteine der Oberfläche =================================
  // Alles hier ist Darstellung: Regeln und Daten kommen aus FW.store bzw.
  // FW.logic. Klassen-Namen stehen in app.css (Abschnitte gleichen Namens).
  // Bedienelemente mit eigenem Zustand (Schalter, Segmente, Regler) zeigen
  // eine Änderung sofort an, solange ihre Speicherung läuft; schlägt sie fehl
  // ({ok:false} oder Fehler), springen sie selbst auf den Wert des Servers
  // zurück. Zusätzlich sollen Ansichten sie über state.rev[bereich] als key
  // neu aufbauen (siehe Store, freshen()).

  const cx = (...parts) => parts.filter(Boolean).join(' ');
  function report(err) {
    if (root.console && root.console.error) root.console.error(err);
  }

  // Fortlaufende ids für aria-Bezüge (stabil über die Lebensdauer der Komponente).
  let uid = 0;
  function useId(prefix) {
    const ref = useRef(null);
    if (ref.current === null) ref.current = `${prefix}-${++uid}`;
    return ref.current;
  }

  // Zustands-Farbe -> CSS-Klasse (Spec §5.3). 'alarm'/'fail'/'error' sind rot.
  const TONE = { ok: 'ok', warn: 'warn', crit: 'crit', alarm: 'crit', fail: 'crit', error: 'crit', unknown: 'unknown', done: 'done', info: 'info' };
  const toneClass = (tone) => (TONE[tone] ? `tone-${TONE[tone]}` : '');

  // ---- Symbole -------------------------------------------------------------
  // Eigene Strichsymbole auf einem 24er-Raster; Strichstärke, Enden und Farbe
  // setzt app.css (.icon: 1,7 px, runde Enden, currentColor).
  const C = (cx_, cy, r) => ['circle', { cx: cx_, cy, r }];
  const D = (d) => ['path', { d }];
  const R = (x, y, w, hh, rx) => ['rect', { x, y, width: w, height: hh, rx }];
  const ICONS = {
    overview: [C(12, 12, 8), C(12, 12, 3)],
    people: [C(9, 8, 3.2), D('M3.5 19c.6-3.2 2.8-5 5.5-5s4.9 1.8 5.5 5M16 11.5a2.8 2.8 0 1 0 0-5.6M17.5 14.2c1.8.5 2.9 2 3.2 4.3')],
    list: [D('M5 6h14M5 12h14M5 18h9')],
    settings: [D('M4 7h9M17 7h3M4 17h3M11 17h9'), C(15, 7, 2), C(9, 17, 2)],
    plus: [D('M12 5v14M5 12h14')],
    chevron: [D('M9.5 6l6 6-6 6')],
    back: [D('M15 5l-7 7 7 7')],
    check: [D('M5 12.5l4.2 4.2L19 7')],
    alert: [D('M12 4l8.5 15h-17zM12 10v4M12 16.8h.01')],
    bang: [D('M12 6.5v7M12 17.5h.01')],
    x: [D('M7 7l10 10M17 7L7 17')],
    phone: [D('M5 4h4l2 5-2.5 1.5a11 11 0 0 0 5 5L15 13l5 2v4a2 2 0 0 1-2 2A16 16 0 0 1 3 6a2 2 0 0 1 2-2')],
    copy: [R(9, 9, 11, 11, 2), D('M15 9V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h3')],
    bell: [D('M6 16v-5a6 6 0 0 1 12 0v5l1.5 2h-15zM10 20.5h4')],
    moon: [D('M19 14.5A7.5 7.5 0 0 1 9.5 5a7.5 7.5 0 1 0 9.5 9.5z')],
    demo: [D('M9.5 4h5M10.5 4v5.2l-4.9 8.4A1.6 1.6 0 0 0 7 20h10a1.6 1.6 0 0 0 1.4-2.4l-4.9-8.4V4M7.8 14.5h8.4')],
    box: [R(4, 6, 16, 12, 2), D('M8 10h3M8 14h6')],
    search: [C(11, 11, 6), D('M20 20l-4.5-4.5')],
    mute: [D('M11 5L6 9H3v6h3l5 4zM16 9.5l5 5M21 9.5l-5 5')],
    unmute: [D('M11 5L6 9H3v6h3l5 4zM15.5 9.5a3.5 3.5 0 0 1 0 5M18.5 7a7 7 0 0 1 0 10')],
    restart: [D('M4 12a8 8 0 1 0 2.3-5.6M4 4v4h4')],
    watch: [D('M12 3.5l7 2.8v5.4c0 4.2-2.9 7.5-7 8.8-4.1-1.3-7-4.6-7-8.8V6.3z')],
    edit: [D('M4.5 19.5h4l10-10-4-4-10 10zM13 7l4 4')],
    test: [D('M20.5 3.5L10 14M20.5 3.5l-6.5 17-4-6.5-6.5-4z')],
    trash: [D('M5 7h14M10 7V5h4v2M7 7l1 12.5h8L17 7M10.5 11v5M13.5 11v5')],
    link: [D('M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1')],
    clock: [C(12, 12, 8), D('M12 8v4.5l3 2')]
  };

  function Icon({ name, class: cls }) {
    const parts = ICONS[name];
    if (!parts) return null;
    return html`<svg class=${cx('icon', cls)} viewBox="0 0 24 24" aria-hidden="true" focusable="false">${
      parts.map(([tag, attrs], i) => h(tag, Object.assign({ key: i }, attrs)))}</svg>`;
  }

  // ---- Seite: großer Titel, optional „‹ Zurück“ und Knöpfe rechts ----------
  // back: {href, label}. Die h1 ist per Programm fokussierbar (Seitenwechsel).
  function Page({ title, back, actions, children, class: cls }) {
    return html`<div class=${cx('page', cls)}>
      ${back ? html`<a class="back-link" href=${back.href}><${Icon} name="back"/><span>${back.label || 'Zurück'}</span></a>` : null}
      <header class="page-head">
        <h1 class="large-title" tabindex="-1">${title}</h1>
        ${actions ? html`<div class="page-actions">${actions}</div>` : null}
      </header>
      ${children}
    </div>`;
  }

  // Runder Symbol-Knopf (z.B. „+“ rechts neben dem Titel); label = Name für Screenreader.
  function IconButton({ icon, label, onClick, href, disabled, class: cls }) {
    const face = html`<span class="icon-btn-face"><${Icon} name=${icon}/></span>`;
    return href
      ? html`<a class=${cx('icon-btn', cls)} href=${href} aria-label=${label}>${face}</a>`
      : html`<button type="button" class=${cx('icon-btn', cls)} aria-label=${label} onClick=${onClick} disabled=${disabled}>${face}</button>`;
  }

  // ---- Gruppierte Listen ---------------------------------------------------
  // Group = Abschnitt mit Kopf (Versalien, grau) und Fuß; der Inhalt ist eine
  // echte Liste (<ul>) aus Row/Item. plain: kein <ul> (freier Inhalt).
  function Group({ header, headerRight, footer, children, level = 2, plain, class: cls, id }) {
    const H = `h${level}`;
    return html`<section class=${cx('group', cls)} id=${id}>
      ${header ? html`<${H} class="group-head"><span>${header}</span>${headerRight ? html`<span class="group-head-right">${headerRight}</span>` : null}</${H}>` : null}
      ${plain ? html`<div class="group-body">${children}</div>` : html`<ul class="group-body" role="list">${children}</ul>`}
      ${footer ? html`<p class="group-foot">${footer}</p>` : null}
    </section>`;
  }

  // Freier Listeneintrag (Regler, Knöpfe, Text) mit Innenabstand und Trennlinie.
  function Item({ children, class: cls, id }) {
    return html`<li class=${cx('row', 'row-block', cls)} id=${id}>${children}</li>`;
  }

  // Zeile: Bezeichnung links (optional Unterzeile), Wert rechts. tone färbt
  // Bezeichnung UND Wert (Spec §5.3). href -> Link, onClick -> Knopf, beides
  // mit Pfeil ›. right: eigenes Element rechts (Schalter, Pille) - dann
  // weder href noch onClick (keine verschachtelten Bedienelemente).
  // children stehen unter der Zeile (aufgeklappte Erklärung, Rückmeldung).
  function Row(props) {
    const { label, value, tone, href, onClick, sub, right, children, icon, iconTone, chevron, disabled, current, class: cls, id, labelId } = props;
    const interactive = !!(href || onClick);
    const showChevron = chevron !== undefined ? !!chevron : interactive;
    const hasValue = value !== undefined && value !== null && value !== '';
    const inner = html`
      ${icon ? html`<span class=${cx('tile', `tile-${iconTone || 'neutral'}`)} aria-hidden="true"><${Icon} name=${icon}/></span>` : null}
      <span class="row-text">
        <span class="row-label" id=${labelId}>${label}</span>
        ${sub ? html`<span class="row-sub">${sub}</span>` : null}
      </span>
      ${hasValue ? html`<span class="row-value">${value}</span>` : null}
      ${right || null}
      ${showChevron ? html`<${Icon} name="chevron" class="row-chev"/>` : null}`;
    let main;
    if (href) {
      main = html`<a class="row-main" href=${href} aria-current=${current ? 'page' : undefined}>${inner}</a>`;
    } else if (onClick) {
      main = html`<button type="button" class="row-main" onClick=${onClick} disabled=${!!disabled}>${inner}</button>`;
    } else {
      main = html`<div class="row-main">${inner}</div>`;
    }
    return html`<li class=${cx('row', toneClass(tone), icon && 'has-icon', disabled && 'is-disabled', cls)} id=${id}>${main}${children || null}</li>`;
  }

  // Status-Pille (Laut/Stumm/Dauerhaft …): tone ok | warn | crit | info | neutral.
  function Pill({ tone, children, class: cls }) {
    const t = TONE[tone] && TONE[tone] !== 'unknown' && TONE[tone] !== 'done' ? TONE[tone] : 'neutral';
    return html`<span class=${cx('pill', `pill-${t}`, cls)}>${children}</span>`;
  }

  // Zähler am Übersicht-Eintrag (Tab-Leiste/Seitenleiste). srText: für Screenreader.
  function Badge({ text, tone, srText }) {
    if (!text) return null;
    return html`<span class=${cx('badge', tone === 'crit' ? 'badge-crit' : 'badge-warn')} aria-hidden="true">${text}</span>${
      srText ? html`<span class="sr-only">, ${srText}</span>` : null}`;
  }

  // ---- Sofort-Anzeige während der Speicherung ------------------------------
  // Liefert [angezeigter Wert, run(next, ergebnis)]. run merkt sich den neuen
  // Wert nur, wenn onChange ein Promise liefert (eine Speicherung läuft) - eine
  // Bestätigung (Rückgabe null) ändert die Anzeige erst nach dem Speichern.
  // Der gemerkte Wert gilt, solange der Server-Wert derselbe ist wie beim
  // Umschalten; {ok:false} oder ein Fehler verwirft ihn sofort.
  function useOptimistic(value) {
    const [ov, setOv] = useState(null);
    const alive = useRef(true);
    useEffect(() => () => { alive.current = false; }, []);
    const shown = ov && Object.is(ov.base, value) ? ov.value : value;
    function run(next, result) {
      if (!result || typeof result.then !== 'function') return;
      const mine = { value: next, base: value };
      setOv(mine);
      const drop = () => { if (alive.current) setOv((cur) => (cur === mine ? null : cur)); };
      result.then((res) => { if (!res || res.ok === false) drop(); }, drop);
    }
    return [shown, run];
  }

  // ---- Schalter (role=switch) ----------------------------------------------
  // label: Name für Screenreader (oder labelledBy = id der sichtbaren Bezeichnung).
  function Switch({ checked, onChange, label, labelledBy, disabled, id }) {
    const [shown, run] = useOptimistic(!!checked);
    const click = () => {
      if (disabled) return;
      const next = !shown;
      run(next, onChange ? onChange(next) : null);
    };
    return html`<button type="button" role="switch" id=${id} class=${cx('switch', shown && 'is-on')}
      aria-checked=${shown ? 'true' : 'false'} aria-label=${labelledBy ? undefined : label} aria-labelledby=${labelledBy}
      disabled=${!!disabled} onClick=${click}><span class="switch-knob" aria-hidden="true"></span></button>`;
  }

  // ---- Segment-Schalter ----------------------------------------------------
  // Knöpfe mit aria-pressed in einer benannten Gruppe: Pfeiltasten lösen
  // bewusst nichts aus (Stumm schalten z.B. fragt nach) - Tab + Enter/Leertaste.
  function Segmented({ options, value, onChange, label, disabled, class: cls }) {
    const [shown, run] = useOptimistic(value);
    const same = (a, b) => String(a) === String(b);
    return html`<div class=${cx('segmented', cls)} role="group" aria-label=${label}>
      ${(options || []).map((o) => {
        const on = same(o.value, shown);
        return html`<button type="button" key=${String(o.value)} class=${cx('seg', on && 'is-on')} aria-pressed=${on ? 'true' : 'false'}
          disabled=${!!(disabled || o.disabled)} onClick=${() => { if (!on && !disabled) run(o.value, onChange ? onChange(o.value) : null); }}>${o.label}</button>`;
      })}
    </div>`;
  }

  // ---- Regler --------------------------------------------------------------
  // Natives <input type=range> (Tastatur und VoiceOver bedienbar). Während
  // des Ziehens zählt nur der lokale Wert - Live-Aktualisierungen ändern ihn
  // nicht. onCommit beim Loslassen; per Tastatur erst 0,8 s nach dem letzten
  // Schritt (bzw. beim Verlassen), damit ArrowRight ×4 EINE Speicherung ist.
  const KEY_COMMIT_MS = 800;
  function Slider({ min = 0, max = 10, step = 1, value, onCommit, label, disabled, minLabel, maxLabel, valueText, tone, id }) {
    const inputId = useId('slider');
    const fieldId = id || inputId;
    const [drag, setDrag] = useState(null);
    const [shown, run] = useOptimistic(Number(value));
    const viaKey = useRef(false);
    const keyTimer = useRef(0);
    const pending = useRef(null);   // per Tastatur gewählt, noch nicht gespeichert
    const live = useRef({});
    const cur = drag !== null ? drag : shown;
    live.current = { cur, value: Number(value), onCommit, run };

    function commit(v) {
      clearTimeout(keyTimer.current);
      pending.current = null;
      setDrag(null);
      const now = live.current;
      if (v === now.value && v === now.cur) return;
      now.run(v, now.onCommit ? now.onCommit(v) : null);
    }
    // Beim Schließen (z.B. Sheet zu) eine per Tastatur gewählte Stufe nicht verlieren.
    useEffect(() => () => {
      clearTimeout(keyTimer.current);
      if (pending.current !== null && live.current.onCommit) live.current.onCommit(pending.current);
    }, []);

    const onInput = (e) => setDrag(Number(e.currentTarget.value));
    const onChange = (e) => {
      const v = Number(e.currentTarget.value);
      if (viaKey.current) {
        setDrag(v);
        pending.current = v;
        clearTimeout(keyTimer.current);
        keyTimer.current = setTimeout(() => commit(v), KEY_COMMIT_MS);
      } else {
        commit(v);
      }
    };
    const span = Number(max) - Number(min) || 1;
    const pct = Math.max(0, Math.min(100, ((cur - Number(min)) / span) * 100));
    const text = valueText ? valueText(cur) : String(cur);
    return html`<div class=${cx('slider', tone && `slider-${tone}`)}>
      <div class="slider-head">
        <label class="slider-label" for=${fieldId}>${label}</label>
        <output class="slider-value" for=${fieldId} aria-hidden="true">${text}</output>
      </div>
      <input type="range" class="slider-input" id=${fieldId} min=${min} max=${max} step=${step} value=${cur}
        disabled=${!!disabled} aria-valuetext=${text} style=${{ '--fill': `${pct}%` }}
        onInput=${onInput} onChange=${onChange}
        onKeyDown=${() => { viaKey.current = true; }}
        onPointerDown=${() => { viaKey.current = false; clearTimeout(keyTimer.current); }}
        onBlur=${() => { if (pending.current !== null) commit(pending.current); }}/>
      ${minLabel || maxLabel ? html`<div class="slider-ticks" aria-hidden="true"><span>${minLabel}</span><span>${maxLabel}</span></div>` : null}
    </div>`;
  }

  // ---- Sheet / Dialog / Panel ----------------------------------------------
  // variant 'sheet': schmal (< 900 px) von unten mit Griff, breit zentrierter
  // Dialog. 'dialog': immer zentriert. 'panel': steht im Seitenfluss (PC,
  // Person rechts neben der Liste) - ohne Abdunkeln und ohne Fokus-Falle.
  // Modale Varianten: Fokus-Falle (Tab bleibt drin), Escape und Tipp daneben
  // schließen, beim Schließen kehrt der Fokus zum Auslöser zurück.
  // Übereinander (Bestätigung über einem Person-Sheet) gilt nur das oberste.
  // bare: ohne Kopfzeile/Griff (die Bestätigung baut ihren eigenen Aufbau).
  const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';
  const trapStack = [];
  let trapsWired = false;

  function focusables(node) {
    return Array.from(node.querySelectorAll(FOCUSABLE)).filter((el) => !el.hidden && el.getClientRects().length > 0);
  }
  function focusInto(node) {
    const target = node.querySelector('[data-autofocus]') || focusables(node)[0] || node;
    if (target && typeof target.focus === 'function') target.focus({ preventScroll: true });
  }
  function wireTraps() {
    if (trapsWired || !root.document) return;
    trapsWired = true;
    const doc = root.document;
    doc.addEventListener('keydown', (e) => {
      const top = trapStack[trapStack.length - 1];
      if (!top) return;
      if (e.key === 'Escape') {
        e.preventDefault();
        e.stopPropagation();
        top.close();
      } else if (e.key === 'Tab') {
        const list = focusables(top.node);
        const active = doc.activeElement;
        if (!list.length) {
          e.preventDefault();
          top.node.focus({ preventScroll: true });
        } else if (e.shiftKey && (active === list[0] || !top.node.contains(active))) {
          e.preventDefault();
          list[list.length - 1].focus();
        } else if (!e.shiftKey && (active === list[list.length - 1] || !top.node.contains(active))) {
          e.preventDefault();
          list[0].focus();
        }
      }
    }, true);
    // Fokus, der doch hinaus gelangt (Maus auf den Hintergrund, Screenreader),
    // zurück in das oberste Sheet holen.
    doc.addEventListener('focusin', (e) => {
      const top = trapStack[trapStack.length - 1];
      if (top && e.target instanceof root.Node && !top.node.contains(e.target)) focusInto(top.node);
    }, true);
  }

  function ModalSheet({ onClose, title, children, variant, class: cls, role, labelledBy, describedBy, hideClose, closeLabel, bare }) {
    const ref = useRef(null);
    const titleId = useId('sheet-title');
    const closeRef = useRef(onClose);
    closeRef.current = onClose;
    const close = () => { if (closeRef.current) closeRef.current(); };

    useLayoutEffect(() => {
      wireTraps();
      const doc = root.document;
      const node = ref.current;
      const opener = doc.activeElement;
      const entry = { node, close };
      trapStack.push(entry);
      doc.body.classList.add('has-sheet');
      focusInto(node);
      return () => {
        const i = trapStack.indexOf(entry);
        if (i >= 0) trapStack.splice(i, 1);
        if (!trapStack.length) doc.body.classList.remove('has-sheet');
        // Fokus nur zurückgeben, wenn er im Sheet stand (bzw. verloren ging) -
        // nicht, wenn die Person inzwischen woanders hin geklickt hat.
        const active = doc.activeElement;
        const lost = !active || active === doc.body || node.contains(active);
        if (lost && opener && opener !== doc.body && opener.isConnected && typeof opener.focus === 'function') {
          opener.focus({ preventScroll: true });
        }
      };
    }, []);

    const named = title ? titleId : labelledBy;
    return html`<div class=${cx('sheet-layer', `sheet-layer-${variant}`, cls)}>
      <div class="sheet-backdrop" aria-hidden="true" onClick=${close}></div>
      <div ref=${ref} class=${cx('sheet', `sheet-${variant}`)} role=${role || 'dialog'} aria-modal="true"
        aria-labelledby=${named} aria-label=${named ? undefined : 'Dialog'} aria-describedby=${describedBy} tabindex="-1">
        ${!bare && variant === 'sheet' ? html`<div class="sheet-grab" aria-hidden="true"></div>` : null}
        ${!bare && (title || !hideClose) ? html`<div class="sheet-head">
          <h2 class="sheet-title" id=${titleId}>${title || ''}</h2>
          ${hideClose ? null : html`<button type="button" class="sheet-close" onClick=${close}>${closeLabel || 'Schließen'}</button>`}
        </div>` : null}
        <div class="sheet-body">${children}</div>
      </div>
    </div>`;
  }

  function PanelView({ onClose, title, children, class: cls, hideClose, closeLabel }) {
    const titleId = useId('panel-title');
    return html`<section class=${cx('panel', cls)} aria-labelledby=${title ? titleId : undefined}>
      ${title || (onClose && !hideClose) ? html`<div class="panel-head">
        <h2 class="panel-title" id=${titleId}>${title || ''}</h2>
        ${onClose && !hideClose ? html`<button type="button" class="sheet-close" onClick=${onClose}>${closeLabel || 'Schließen'}</button>` : null}
      </div>` : null}
      <div class="panel-body">${children}</div>
    </section>`;
  }

  function Sheet(props) {
    if (!props.open) return null;
    const variant = props.variant === 'dialog' || props.variant === 'panel' ? props.variant : 'sheet';
    if (variant === 'panel') return h(PanelView, props);
    return h(ModalSheet, Object.assign({}, props, { variant }));
  }

  // ---- Halten zum Senden ---------------------------------------------------
  // Drücken füllt den Knopf in durationMs (CSS-Variable --p 0→1); Loslassen,
  // Abbrechen (pointercancel), Wegziehen oder Fokusverlust vorher bricht ab -
  // ohne Wirkung. Tastatur: Leertaste/Enter gedrückt halten (Wiederholungen
  // zählen nicht). onComplete genau einmal. VoiceOver: Doppeltippen und
  // halten erzeugt dieselben Pointer-Ereignisse. Maßgeblich ist ein Timer,
  // die Füllung ist nur Anzeige (requestAnimationFrame).
  const HOLD_HINT = 'Zum Senden gedrückt halten';
  const HOLD_NUDGE = 'Länger halten – bis der Balken voll ist';
  const HOLD_SLOP_PX = 16;
  function HoldButton({ label = 'Gedrückt halten zum Senden', durationMs = 1500, onComplete, disabled, class: cls }) {
    const ref = useRef(null);
    const hintId = useId('hold-hint');
    const cb = useRef(onComplete);
    cb.current = onComplete;
    const st = useRef({ active: false, done: false, t0: 0, raf: 0, timer: 0, pointerId: null, key: null });
    const [nudge, setNudge] = useState(false);
    const nudgeTimer = useRef(0);

    const paint = (p) => { if (ref.current) ref.current.style.setProperty('--p', String(p)); };
    const stopTimers = () => {
      const s = st.current;
      if (s.raf && root.cancelAnimationFrame) root.cancelAnimationFrame(s.raf);
      clearTimeout(s.timer);
      s.raf = 0;
      s.timer = 0;
    };
    function finish() {
      const s = st.current;
      if (!s.active || s.done) return;
      s.active = false;
      s.done = true;
      stopTimers();
      paint(1);
      if (ref.current) ref.current.classList.add('is-done');
      try {
        if (cb.current) cb.current();
      } catch (err) {
        report(err);
      }
    }
    function begin() {
      const s = st.current;
      if (disabled || s.active || s.done) return false;
      s.active = true;
      s.t0 = Date.now();
      clearTimeout(nudgeTimer.current);
      setNudge(false);
      if (ref.current) ref.current.classList.add('is-holding');
      const frame = () => {
        if (!s.active) return;
        const p = Math.min(1, (Date.now() - s.t0) / durationMs);
        paint(p);
        if (p < 1 && root.requestAnimationFrame) s.raf = root.requestAnimationFrame(frame);
      };
      frame();
      s.timer = setTimeout(finish, durationMs);
      return true;
    }
    function abort(showNudge) {
      const s = st.current;
      s.pointerId = null;
      s.key = null;
      if (!s.active) return;
      s.active = false;
      stopTimers();
      paint(0);
      if (ref.current) ref.current.classList.remove('is-holding');
      if (showNudge) {
        setNudge(true);
        announce(`${HOLD_HINT}, bis der Balken voll ist.`);
        clearTimeout(nudgeTimer.current);
        nudgeTimer.current = setTimeout(() => setNudge(false), 2500);
      }
    }

    useEffect(() => {
      // Tab im Hintergrund / Fenster verliert den Fokus: Loslassen käme evtl. nie an.
      const quit = () => abort(false);
      const doc = root.document;
      doc.addEventListener('visibilitychange', quit);
      root.addEventListener('blur', quit);
      return () => {
        doc.removeEventListener('visibilitychange', quit);
        root.removeEventListener('blur', quit);
        stopTimers();
        clearTimeout(nudgeTimer.current);
      };
    }, []);
    useEffect(() => { if (disabled) abort(false); }, [disabled]);

    const onPointerDown = (e) => {
      if (!e.isPrimary || (e.pointerType === 'mouse' && e.button !== 0)) return;
      e.preventDefault();
      if (!begin()) return;
      st.current.pointerId = e.pointerId;
      try { e.currentTarget.setPointerCapture(e.pointerId); } catch (err) { /* ohne Capture bricht Verlassen ab */ }
    };
    const samePointer = (e) => st.current.pointerId !== null && e.pointerId === st.current.pointerId;
    const onPointerUp = (e) => { if (samePointer(e)) abort(true); };
    const onPointerCancel = (e) => { if (samePointer(e)) abort(false); };
    const onPointerMove = (e) => {
      if (!samePointer(e) || !st.current.active) return;
      const r = e.currentTarget.getBoundingClientRect();
      if (e.clientX < r.left - HOLD_SLOP_PX || e.clientX > r.right + HOLD_SLOP_PX
        || e.clientY < r.top - HOLD_SLOP_PX || e.clientY > r.bottom + HOLD_SLOP_PX) abort(false);
    };
    const isHoldKey = (e) => e.key === ' ' || e.key === 'Enter' || e.key === 'Spacebar';
    const onKeyDown = (e) => {
      if (!isHoldKey(e)) return;
      e.preventDefault();
      if (e.repeat || st.current.active) return;
      if (begin()) st.current.key = e.key;
    };
    const onKeyUp = (e) => {
      if (!isHoldKey(e)) return;
      e.preventDefault();
      if (st.current.key === e.key) abort(true);
    };
    const label_ = nudge ? HOLD_NUDGE : label;
    return html`<button ref=${ref} type="button" class=${cx('hold', nudge && 'is-nudge', cls)} disabled=${!!disabled}
        aria-describedby=${hintId}
        onPointerDown=${onPointerDown} onPointerUp=${onPointerUp} onPointerCancel=${onPointerCancel}
        onPointerMove=${onPointerMove} onLostPointerCapture=${onPointerCancel} onPointerLeave=${onPointerCancel}
        onKeyDown=${onKeyDown} onKeyUp=${onKeyUp} onBlur=${() => abort(false)}
        onClick=${(e) => e.preventDefault()} onContextMenu=${(e) => e.preventDefault()}>
        <span class="hold-label">${label_}</span>
        <span class="hold-fill" aria-hidden="true"><span class="hold-label">${label_}</span></span>
      </button><span id=${hintId} class="sr-only">${HOLD_HINT}</span>`;
  }

  // ---- Bestätigungen (store.confirm) ---------------------------------------
  // Der Store besitzt Frist, Modus-Prüfung und Aktion; hier nur Anzeige und
  // confirmNow()/closeConfirm() mit der id dieser Bestätigung (ein veraltetes
  // Sheet kann so nie eine neuere auslösen). Fokus startet auf „Abbrechen“.
  function ConfirmHost() {
    const c = store.useStore((s) => s.confirm);
    if (!c) return null;
    return html`<${ConfirmSheet} key=${c.id} c=${c}/>`;
  }

  function ConfirmSheet({ c }) {
    const titleId = useId('confirm-title');
    const textId = useId('confirm-text');
    const opened = useRef(Date.now());
    const left = store.useStore((s) => Math.max(0, Math.ceil((c.deadline - Math.max(s.nowMs, opened.current)) / 1000)));
    const close = () => store.closeConfirm(c.id);
    const fire = () => {
      store.confirmNow(c.id).catch(report);
    };
    return html`<${Sheet} open=${true} variant="sheet" bare=${true} class="is-confirm" role="alertdialog"
        labelledBy=${titleId} describedBy=${c.text ? textId : undefined} onClose=${close}>
      <div class="confirm">
        <div class="confirm-box">
          <h2 class="confirm-title" id=${titleId}>${c.title || 'Bitte bestätigen'}</h2>
          ${c.text ? html`<p class="confirm-text" id=${textId}>${c.text}</p>` : null}
          ${c.hold
            ? html`<${HoldButton} label=${c.confirmLabel} onComplete=${fire}/>`
            : html`<button type="button" class="btn btn-danger btn-block" onClick=${fire}>${c.confirmLabel}</button>`}
          <p class="confirm-count" aria-hidden="true">Schließt sich in ${left} s</p>
        </div>
        <button type="button" class="btn btn-cancel btn-block" data-autofocus onClick=${close}>Abbrechen</button>
      </div>
    </${Sheet}>`;
  }

  // ---- Rückmeldung an einem Bedienelement ----------------------------------
  // msg = {text, kind} (genau das Ergebnis der Store-Aktionen) oder null.
  // Zeichen UND Farbe; Erfolg verschwindet nach Math.max(8 s, Wörter × 450 ms),
  // Fehler bleiben stehen. Angesagt wird über announce() (eine Live-Region
  // für alle - in die Zeile eingebettete würden je nach Screenreader doppelt
  // oder gar nicht gelesen).
  const lingerMs = (text) => Math.max(8000, String(text).trim().split(/\s+/).length * 450);
  const MSG_SYMBOL = { ok: '✓', error: '✕', warn: '!' };
  function Msg({ msg, class: cls }) {
    const [goneFor, setGoneFor] = useState(null);
    useEffect(() => {
      if (!msg || !msg.text) return undefined;
      announce(({ error: 'Fehler: ', warn: 'Achtung: ' }[msg.kind] || '') + msg.text);
      if (msg.kind !== 'ok') return undefined;
      const t = setTimeout(() => setGoneFor(msg), lingerMs(msg.text));
      return () => clearTimeout(t);
    }, [msg]);
    if (!msg || !msg.text || goneFor === msg) return null;
    const kind = MSG_SYMBOL[msg.kind] ? msg.kind : 'info';
    return html`<p class=${cx('msg', `msg-${kind}`, cls)}>${
      MSG_SYMBOL[kind] ? html`<span class="msg-sym" aria-hidden="true">${MSG_SYMBOL[kind]}</span>` : null}<span class="msg-text">${msg.text}</span></p>`;
  }

  // ---- Unsichtbare Live-Region ---------------------------------------------
  // Erst leeren, dann kurz danach setzen: So wird auch derselbe Text erneut
  // angesagt. Die Region entsteht beim ersten Aufruf (main.js ruft früh '' auf).
  let liveEl = null;
  let liveTimer = 0;
  function announce(text) {
    const doc = root.document;
    if (!doc || !doc.body) return;
    if (!liveEl) {
      liveEl = doc.createElement('p');
      liveEl.className = 'sr-only';
      liveEl.setAttribute('role', 'status');
      liveEl.setAttribute('aria-live', 'polite');
      doc.body.appendChild(liveEl);
    }
    clearTimeout(liveTimer);
    liveEl.textContent = '';
    if (text) liveTimer = setTimeout(() => { liveEl.textContent = String(text); }, 60);
  }

  // ---- Fehlergrenzen -------------------------------------------------------
  // Ein Fehler in EINEM Teil darf nicht die ganze Oberfläche leeren: um jede
  // Ansicht eine Grenze (main.js), auf der Übersicht zusätzlich um jeden Teil
  // - so bleibt „Auslösen“ stehen, wenn z.B. die Prüfliste abstürzt. Jede
  // Ersatzanzeige bietet einen Ausweg: „Neu laden“ (zur Übersicht) und die
  // alte Ansicht (classic.php), über die sich ebenfalls alarmieren lässt.
  // Gleiche Ziele wie die Ersatzanzeige in lib/shell.php und main.js.
  // inline: kleiner Kasten in der Seite statt einer ganzen Seite.
  function FallbackLinks() {
    return html`<div class="fallback-links">
      <a class="btn btn-tinted btn-block" href="index.php">Neu laden</a>
      <a class="btn btn-tinted btn-block" href="classic.php">Alte Ansicht (classic.php)</a>
    </div>`;
  }

  function Fallback({ title, text, inline }) {
    const msg = text || (inline
      ? 'Dieser Teil konnte nicht angezeigt werden.'
      : 'Diese Ansicht konnte nicht angezeigt werden. Die anderen Bereiche funktionieren weiter.');
    if (inline) {
      return html`<section class="boundary-card" role="alert"><p class="boundary">${msg}</p><${FallbackLinks}/></section>`;
    }
    return html`<${Page} title=${title || 'Fehler'}>
      <p class="boundary" role="alert">${msg}</p>
      <${FallbackLinks}/>
    </${Page}>`;
  }

  class Boundary extends Component {
    constructor() {
      super();
      this.state = { err: null };
    }
    componentDidCatch(err) {
      report(err);
      this.setState({ err });
    }
    render() {
      if (!this.state.err) return this.props.children;
      return h(Fallback, { title: this.props.title, text: this.props.text, inline: !!this.props.inline });
    }
  }

  // ---- Kleine Helfer für Ansichten -----------------------------------------
  // Ableitungen auswählen: derive() liefert jede Sekunde neue Objekte - pick
  // soll deshalb einfache Werte (Strings, Zahlen) liefern, sonst zeichnet die
  // Komponente bei jedem Takt neu.
  const useDerived = (pick) => store.useStore((s) => pick(store.derive(s), s));

  // Alter des neuesten bekannten Box-Status in Sekunden (wächst auch während
  // einer NAS-Störung weiter), NaN = unbekannt.
  function statusAge(s) {
    const data = s.statusData;
    if (!data || data.seen_age_seconds === null || data.seen_age_seconds === undefined || !s.lastOkAt) return NaN;
    return Number(data.seen_age_seconds) + Math.max(0, (s.nowMs - s.lastOkAt) / 1000);
  }
  function statusAgeText(s) {
    if (s.authLost) return 'Abgemeldet';
    if (!s.statusData) return s.statusError ? 'Kein Status vom NAS' : 'Status wird geladen…';
    const age = L.fmtAgeShort(statusAge(s));
    return age ? `Status ${age}` : 'Box hat sich noch nie gemeldet';
  }

  FW.ui = {
    cx, useId, toneClass, ICONS,
    Icon, Page, IconButton, Group, Item, Row, Pill, Badge,
    Switch, Segmented, Slider, Sheet, HoldButton, ConfirmHost, Msg, Boundary, Fallback,
    announce, lingerMs, useDerived, statusAge, statusAgeText
  };
})(typeof window !== 'undefined' ? window : globalThis);
