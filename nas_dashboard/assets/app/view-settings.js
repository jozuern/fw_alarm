(function (root) {
  'use strict';
  const FW = root.FW = root.FW || {};
  const { html, useState, useEffect, useMemo, useRef } = root.htmPreact;
  const store = FW.store;
  const ui = FW.ui;
  const L = FW.logic;
  const cx = ui.cx;
  FW.views = FW.views || {};

  // ==== Einstellungen (Spec §6.5) ===========================================
  // Adressen: #/einstellungen (Liste), …/meldungen, …/meldungen/<art>,
  // …/allgemein (Ruhezeit & Allgemein), …/demo, …/stumm („Automatisch wieder
  // laut nach“), …/box (Details & Diagnose). Die Lese-Rolle sieht nur Box,
  // „Dieses Gerät“ und Konto - Meldungen/Einstellungen fragt der Store für sie
  // gar nicht erst ab (der Server sperrt sie ohnehin).
  // Bedienelemente mit eigenem Zustand sind auf state.rev[bereich]
  // geschlüsselt: Scheitert eine Speicherung, zeigen sie wieder den Stand des
  // Servers. Arten, Beschriftungen und ALLE Möglichkeiten kommen vom Server
  // (notify_list / settings_list) - hier wird nur gezeichnet.
  // Außerdem hier: „Hinweis im Browser bei Störung“ (Spec §8.6), die
  // Benachrichtigungen selbst (store.onTransition, Texte aus FW.logic).

  const BACK_MAIN = { href: '#/einstellungen', label: 'Einstellungen' };
  const BACK_NOTIFY = { href: '#/einstellungen/meldungen', label: 'Meldungen' };
  const ADMIN_PAGES = ['meldungen', 'allgemein', 'demo', 'stumm'];
  const NBSP = ' ';
  const THEME_OPTIONS = [
    { value: 'dark', label: 'Dunkel' },
    { value: 'light', label: 'Hell' },
    { value: 'system', label: 'System' }
  ];
  const UNCONFIGURED = 'In config.php ist kein Status-Empfänger eingetragen (bark_key_status) – das NAS verschickt deshalb gerade gar keine Meldungen. Die Einstellungen hier greifen, sobald der Key gesetzt ist.';

  // ---- Kleine Helfer -----------------------------------------------------------
  // Rückmeldungen erst setzen, wenn die Komponente noch steht.
  function useShow(setMsg) {
    const alive = useRef(true);
    useEffect(() => () => { alive.current = false; }, []);
    return (res) => {
      if (alive.current && res) setMsg(res);
      return res;
    };
  }

  // derive() baut jede Sekunde neue Objekte: Ausschnitt als JSON-Text
  // auswählen (ändert sich nur bei echter Änderung) und hier auspacken.
  function useDerivedJson(pick) {
    const sig = ui.useDerived((d, s) => JSON.stringify(pick(d, s) ?? null));
    return useMemo(() => JSON.parse(sig), [sig]);
  }

  // Sofort-Anzeige während der Speicherung (wie ui.Switch/Segmented): der
  // gewählte Wert gilt, solange der Server-Wert derselbe ist wie beim
  // Umschalten; {ok:false} oder ein Fehler verwirft ihn sofort.
  function usePending(value) {
    const [ov, setOv] = useState(null);
    const alive = useRef(true);
    useEffect(() => () => { alive.current = false; }, []);
    const shown = ov && ov.base === value ? ov.value : value;
    function run(next, result) {
      if (!result || typeof result.then !== 'function') return result;
      const mine = { value: next, base: value };
      setOv(mine);
      const drop = () => { if (alive.current) setOv((cur) => (cur === mine ? null : cur)); };
      result.then((res) => { if (!res || res.ok === false) drop(); }, drop);
      return result;
    }
    return [shown, run];
  }

  // Eine Bestätigung, die zu DIESER Stelle gehört: Sie schließt sich, wenn
  // sich der Stand ändert, auf den sich ihre Frage bezieht (dep - z.B. eine
  // andere Sitzung hat die Meldung schon abgeschaltet), oder die Ansicht geht.
  function useOwnConfirm(dep) {
    const id = useRef(null);
    useEffect(() => () => {
      if (id.current) store.closeConfirm(id.current);
      id.current = null;
    }, [dep]);
    return (opts) => {
      id.current = store.openConfirm(opts);
      return id.current;
    };
  }

  // Text einer Auswahl; Kurzform ohne Klammerzusatz (für Segmente) und der
  // Zusatz allein („leise (nur in der Mitteilungszentrale)“).
  const choiceText = (opt, value) => ((opt.choices || []).find((c) => String(c.value) === String(value)) || {}).text || String(value);
  const shortText = (t) => String(t).replace(/\s*\([^)]*\)\s*$/, '').trim() || String(t);
  const detailText = (t) => {
    const m = /\(([^)]*)\)\s*$/.exec(String(t));
    return m ? m[1] : '';
  };
  // Dauer wie notify_duration_label() in common.php („12 Std“).
  function durationLabel(seconds) {
    const s = Number(seconds) || 0;
    if (s > 0 && s % 3600 === 0) return `${s / 3600}${NBSP}Std`;
    if (s > 0 && s % 60 === 0) return `${s / 60}${NBSP}Min`;
    return `${s}${NBSP}s`;
  }

  // Segment bei höchstens 4 Möglichkeiten, sonst Auswahlliste (Spec §6.5).
  // Ausnahme Stellvertreter: Namen aus der Empfängerliste passen in kein
  // Segment.
  const asSegment = (opt) => (opt.choices || []).length <= 4 && opt.key !== 'extra_id';

  // Dringlichkeits-Etikett einer Art - nur, wenn sie an ist und nicht leise.
  function levelTag(t) {
    const o = (t.options || []).find((x) => x.key === 'level');
    if (!t.enabled || !o || o.value === 'passive') return null;
    return { text: shortText(choiceText(o, o.value)), tone: o.value === 'critical' ? 'crit' : 'neutral' };
  }

  // „22–07 Uhr“ bzw. „aus“ aus den globalen Optionen.
  function quietSummary(n) {
    if (!n || !Array.isArray(n.global)) return '…';
    const g = (k) => n.global.find((o) => o.key === k);
    const on = g('quiet_enabled');
    if (!on) return '';
    if (String(on.value) !== '1') return 'aus';
    const hh = (o) => (o ? choiceText(o, o.value).replace(/:00$/, '') : '?');
    return `${hh(g('quiet_from'))}–${hh(g('quiet_to'))} Uhr`;
  }

  // Wirksame Dauer bis „wieder laut“: Einstellung, sonst Liste, sonst meta.
  function muteSummaryOf(s) {
    const m = s.settings && s.settings.mute_max_seconds;
    if (m) return shortText(choiceText(m, m.value));
    const v = s.keys && Number.isFinite(Number(s.keys.mute_max_seconds)) ? Number(s.keys.mute_max_seconds) : s.muteMax;
    return v > 0 ? durationLabel(v) : 'nie';
  }

  function logout() {
    const form = root.document.getElementById('logout-form');
    if (form) form.submit();
  }

  // ==== Hinweis im Browser bei Störung (Spec §8.6) ===========================
  // Nur, wo der Browser lokale Benachrichtigungen kann - in der iPhone-App
  // (Home-Bildschirm) nicht, und auch nicht in Chrome auf Android (dort
  // gehen sie nur über einen Service Worker, new Notification() wirft).
  // Eingeschaltet fragt der Browser einmal nach der Erlaubnis; die
  // Einstellung liegt pro Gerät im localStorage (Store).
  const ALERT_TITLE = 'Alarm-Wächter';
  const ALERT_TAG = 'fw-alarm';
  const ALERT_DENIED = 'Der Browser erlaubt für diese Seite keine Benachrichtigungen – bitte in den Website-Einstellungen des Browsers erlauben und dann noch einmal einschalten.';
  const nav = () => root.navigator || {};
  const alertSupported = () => 'Notification' in root && nav().standalone !== true && !/Android/i.test(nav().userAgent || '');
  function alertPermission() {
    try { return root.Notification.permission; } catch (err) { return 'denied'; }
  }
  function askPermission() {
    return new Promise((resolve) => {
      let done = false;
      const finish = (p) => { if (!done) { done = true; resolve(p); } };
      try {
        // Ältere Safari-Versionen kennen nur den Rückruf, neuere das Promise.
        const r = root.Notification.requestPermission(finish);
        if (r && typeof r.then === 'function') r.then(finish, () => finish('denied'));
      } catch (err) {
        finish('denied');
      }
    });
  }

  // Wurde eine Störung gemeldet? Dann (und nur dann) kommt beim Zurück ins
  // Ruhige einmal „Wieder in Ordnung“.
  let alerted = false;
  function setBrowserAlert(on) {
    alerted = false;
    store.setBrowserAlert(on);
  }

  async function enableBrowserAlert() {
    if (!alertSupported()) return { ok: false, kind: 'error', text: 'Dieser Browser kann keine Benachrichtigungen anzeigen.' };
    let perm = alertPermission();
    if (perm === 'default') perm = await askPermission();
    if (perm !== 'granted') return { ok: false, kind: 'error', text: ALERT_DENIED };
    setBrowserAlert(true);
    return { ok: true, kind: 'ok', text: 'Eingeschaltet – dieser Browser meldet Störungen, solange das Dashboard hier offen ist (auch im Hintergrund).' };
  }

  function showNotification(body) {
    if (!alertSupported() || alertPermission() !== 'granted') return false;
    try {
      const n = new root.Notification(ALERT_TITLE, { body, tag: ALERT_TAG });
      // Klick holt das Dashboard nach vorn.
      n.onclick = () => {
        try { root.focus(); } catch (err) { /* manche Browser erlauben das nicht */ }
        try { n.close(); } catch (err) { /* schon weg */ }
      };
      return true;
    } catch (err) {
      // z.B. Android-Chrome: dort gehen Benachrichtigungen nur über einen Service Worker.
      if (root.console && root.console.warn) root.console.warn(err);
      return false;
    }
  }

  // Der Store meldet Übergänge nur, solange der Hinweis eingeschaltet ist
  // ('nas' erst ab dem 3. Fehlschlag, 'delivery' = unvollständige Zustellung).
  store.onTransition((e) => {
    const a = L.browserAlertFor(e.to, alerted);
    if (a && showNotification(a.body)) alerted = a.problem;
  });

  function BrowserAlertRow() {
    const on = store.useStore((s) => s.browserAlert);
    const [msg, setMsg] = useState(null);
    const show = useShow(setMsg);
    const labelId = ui.useId('set-alert-label');
    const blocked = on && alertPermission() !== 'granted';
    const change = (next) => {
      setMsg(null);
      if (!next) {
        setBrowserAlert(false);
        return Promise.resolve({ ok: true });
      }
      return enableBrowserAlert().then(show);
    };
    return html`<li class="row set-alert-row" id="set-alert">
      <div class="row-main">
        <span class="row-text">
          <span class="row-label" id=${labelId}>Hinweis im Browser bei Störung</span>
          <span class=${cx('row-sub', blocked && 'set-sub-warn')}>${blocked
            ? html`<span aria-hidden="true">! </span>Der Browser blockiert Benachrichtigungen für diese Seite – so kommt kein Hinweis an.`
            : 'Meldet Box offline, Alarm und NAS-Ausfall – auch bei verstecktem Tab, solange er offen ist.'}</span>
        </span>
        <${ui.Switch} checked=${on} onChange=${change} labelledBy=${labelId}/>
      </div>
      <${ui.Msg} msg=${msg} class="set-msg"/>
    </li>`;
  }

  // ==== Liste (Spec §6.5) ======================================================
  function AdminGroups() {
    const n = store.useStore((s) => s.notify);
    const notifyError = store.useStore((s) => s.notifyError);
    const demoOn = store.useStore((s) => !!(s.demo && s.demo.enabled));
    const demoName = store.useStore((s) => (s.demo && s.demo.label) || 'Test-Empfänger');
    const mute = store.useStore(muteSummaryOf);
    const count = n ? `${n.types.filter((t) => t.enabled).length} von ${n.types.length} an` : (notifyError ? 'nicht geladen' : '…');
    const unconfigured = !!(n && !n.configured);
    return [
      html`<${ui.Group} key="admin" class="set-g-admin">
        <${ui.Row} id="set-notify" label="Meldungen an mich" value=${count} tone=${unconfigured ? 'warn' : ''}
          sub=${unconfigured ? 'Kein Status-Empfänger eingetragen – es geht nichts raus' : undefined}
          icon="bell" iconTone="warn" href="#/einstellungen/meldungen"/>
        <${ui.Row} id="set-quiet" label="Ruhezeit" value=${quietSummary(n)} icon="moon" iconTone="info" href="#/einstellungen/allgemein"/>
        <${ui.Row} id="set-demo" label="Demo-Modus" value=${demoOn ? `nur ${demoName}` : 'aus'} tone=${demoOn ? 'warn' : ''}
          icon="demo" iconTone=${demoOn ? 'warn' : 'neutral'} href="#/einstellungen/demo"/>
      </${ui.Group}>`,
      html`<${ui.Group} key="rcp" header="Empfänger" class="set-g-recipients">
        <${ui.Row} id="set-mute" label="Automatisch wieder laut nach" value=${mute} href="#/einstellungen/stumm"/>
      </${ui.Group}>`
    ];
  }

  function DeviceGroup() {
    const [theme, setThemeState] = useState(() => FW.main.getTheme());
    const labelId = ui.useId('set-theme-label');
    const pick = (v) => {
      setThemeState(FW.main.setTheme(v));
      return null;
    };
    return html`<${ui.Group} header="Dieses Gerät" class="set-g-device">
      <li class="row set-inline-row" id="set-theme"><div class="row-main">
        <span class="row-label" id=${labelId}>Design</span>
        <${ui.Segmented} class="set-theme-seg" options=${THEME_OPTIONS} value=${theme} onChange=${pick} label="Design"/>
      </div></li>
      ${alertSupported() ? html`<${BrowserAlertRow}/>` : null}
    </${ui.Group}>`;
  }

  function AccountGroup() {
    const user = store.useStore((s) => s.user);
    const readOnly = store.useStore((s) => s.readOnly);
    return html`<${ui.Group} header="Konto" class="set-g-account">
      <${ui.Row} id="set-account" label="Angemeldet als" value=${`${user || '–'} · ${readOnly ? 'nur lesen' : 'Admin'}`}/>
      <${ui.Row} id="set-logout" class="set-action" label="Abmelden" chevron=${false} onClick=${logout}/>
      <${ui.Row} id="set-classic" class="set-action" label="Alte Ansicht öffnen" sub="die bisherige Oberfläche – solange es sie noch gibt" href="classic.php"/>
    </${ui.Group}>`;
  }

  function MainList() {
    const readOnly = store.useStore((s) => s.readOnly);
    return html`<${ui.Page} title="Einstellungen" class="set set-main page-narrow">
      ${readOnly ? null : html`<${AdminGroups}/>`}
      <${ui.Group} header="Box" class="set-g-box">
        <${ui.Row} id="set-box" label="Details & Diagnose" sub="WLAN, Wächter, Speicher, Firmware …" icon="box" iconTone="neutral" href="#/einstellungen/box"/>
      </${ui.Group}>
      <${DeviceGroup}/>
      <${AccountGroup}/>
    </${ui.Page}>`;
  }

  // ==== Meldungen an mich ==========================================================
  // Ein Schalter je Art. Eine wichtige Art (important) abzuschalten fragt
  // erst nach - bis zur Bestätigung bleibt der Schalter an; Einschalten geht
  // sofort. link=false: dieselbe Zeile auf der Detailseite.
  function NotifyTypeRow({ t, link }) {
    const rev = store.useStore((s) => s.rev.notify);
    const [msg, setMsg] = useState(null);
    const show = useShow(setMsg);
    const ask = useOwnConfirm(t.enabled);
    const labelId = ui.useId('set-ntype');
    const tag = link ? levelTag(t) : null;
    const change = (next) => {
      setMsg(null);
      if (!next && t.important) {
        ask({
          title: 'Meldung abschalten?',
          text: `Meldung „${t.label}“ wirklich abschalten? Das würde dir dann NICHT mehr gemeldet.`,
          confirmLabel: 'Ja, abschalten',
          hold: false,
          onConfirm: () => store.notifySet(t.key, 'enabled', '0').then(show)
        });
        return null;
      }
      return store.notifySet(t.key, 'enabled', next ? '1' : '0').then(show);
    };
    const text = html`<span class="row-text"><span class="row-label" id=${labelId}>${link ? t.label : 'Diese Meldung senden'}</span></span>`;
    return html`<li class=${cx('row set-ntype', !t.enabled && 'is-off')} id=${`ntype-${t.key}`}>
      <div class="set-ntype-line">
        ${link
          ? html`<a class="row-main set-ntype-link" href=${`#/einstellungen/meldungen/${encodeURIComponent(t.key)}`}>
              ${text}${tag ? html`<${ui.Pill} tone=${tag.tone} class="set-level">${tag.text}</${ui.Pill}>` : null}
              <${ui.Icon} name="chevron" class="row-chev"/></a>`
          : html`<div class="row-main set-ntype-link">${text}</div>`}
        <span class="set-ntype-switch"><${ui.Switch} key=${`${t.key}:${rev}`} checked=${!!t.enabled} onChange=${change} labelledBy=${labelId}/></span>
      </div>
      <${ui.Msg} msg=${msg} class="set-msg"/>
    </li>`;
  }

  function Loading({ error }) {
    return html`<p class=${cx('set-loading', error && 'is-error')} role=${error ? 'alert' : 'status'}>${error || 'Wird geladen…'}</p>`;
  }

  function Unconfigured() {
    return html`<p class="set-warn-note set-unconfigured"><span aria-hidden="true">! </span>${UNCONFIGURED}</p>`;
  }

  function NotifyExplain() {
    return html`<div class="set-explain">
      <p><strong>Dringlichkeit:</strong> „leise“ landet nur in der Mitteilungszentrale, „normal“ kommt mit Ton,
        „dringend“ durchbricht Fokus-Modi, „kritisch“ auch den Stummschalter (mit der unter „Ruhezeit &
        Allgemein“ gewählten Lautstärke).</p>
      <p><strong>Ruhezeit:</strong> In dieser Zeit kommen alle Meldungen nur leise an – außer denen, bei denen
        „wie eingestellt (auch nachts)“ gewählt ist.</p>
      <p><strong>„Probe senden“</strong> schickt eine Beispiel-Meldung genau so, wie sie mit den aktuellen
        Einstellungen ankäme.</p>
      <p><strong>Nicht einstellbar</strong> sind die Meldungen, die die Box selbst verschickt: der tägliche
        Heartbeat, die Start-Meldung, „Kontakt klemmt?“, die Warnung bei nicht zustellbarem Alarm, die Meldung
        über eine übernommene Empfängerliste und die Antwort auf TEST. Sie kommen aus der Firmware der Box.</p>
    </div>`;
  }

  function NotifyPage() {
    const n = store.useStore((s) => s.notify);
    const error = store.useStore((s) => s.notifyError);
    const [explain, setExplain] = useState(false);
    if (!n) {
      return html`<${ui.Page} title="Meldungen an mich" back=${BACK_MAIN} class="set page-narrow"><${Loading} error=${error}/></${ui.Page}>`;
    }
    const rows = (list) => list.map((t) => html`<${NotifyTypeRow} key=${t.key} t=${t} link=${true}/>`);
    const secure = n.types.filter((t) => t.important);
    const info = n.types.filter((t) => !t.important);
    return html`<${ui.Page} title="Meldungen an mich" back=${BACK_MAIN} class="set set-notify-page page-narrow">
      <p class="set-intro">Statusmeldungen, die das NAS an deinen Status-Empfänger schickt. Die Alarmierung selbst ist davon unabhängig.</p>
      ${n.configured ? null : html`<${Unconfigured}/>`}
      ${secure.length ? html`<${ui.Group} header="Sicherheit" class="set-g-security"
        footer="Abschalten nur mit Rückfrage – fiele dann etwas aus, käme keine Meldung.">${rows(secure)}</${ui.Group}>` : null}
      ${info.length ? html`<${ui.Group} header="Info" class="set-g-info"
        footer="Tippen auf eine Meldung: Dringlichkeit, Schwelle, Ruhezeit-Verhalten und „Probe senden“.">${rows(info)}</${ui.Group}>` : null}
      <${ui.Group} class="set-g-general">
        <${ui.Row} id="set-general" label="Ruhezeit & Allgemein" value=${quietSummary(n)} icon="moon" iconTone="info" href="#/einstellungen/allgemein"/>
      </${ui.Group}>
      <div class="set-foot">
        <p>Dringlichkeit, Ruhezeit und was sich hier nicht einstellen lässt.</p>
        <button type="button" class="link-btn set-more" onClick=${() => setExplain(true)}>Mehr erfahren</button>
      </div>
      <${ui.Sheet} open=${explain} variant="sheet" class="set-explain-sheet" title="Meldungen an mich" onClose=${() => setExplain(false)}>
        <${NotifyExplain}/>
      </${ui.Sheet}>
    </${ui.Page}>`;
  }

  // ---- Eine Auswahl (Segment oder Liste) -----------------------------------------
  // Auswahlliste als ganze Zeile: Das native <select> liegt unsichtbar über
  // der Zeile - ein Tipp irgendwo darauf öffnet die Auswahl (iPhone: Rad),
  // sichtbar ist der Text der gewählten Möglichkeit. Solange das Feld den
  // Fokus hat, ändert eine Live-Aktualisierung den angezeigten Wert nicht.
  function Picker({ id, selectId, label, value, choices, onPick, disabled }) {
    const ownId = ui.useId('set-pick');
    const fieldId = selectId || ownId;
    const labelId = ui.useId('set-pick-label');
    const [focusVal, setFocusVal] = useState(null);
    const shown = focusVal !== null ? focusVal : String(value);
    const text = ((choices || []).find((c) => String(c.value) === shown) || {}).text || '';
    return html`<div class="row-main set-select-row" id=${id}>
      <span class="row-label" id=${labelId}>${label}</span>
      <span class="set-select-value" aria-hidden="true">${text}</span>
      <${ui.Icon} name="chevron" class="set-select-chev"/>
      <select id=${fieldId} class="set-select" aria-labelledby=${labelId} value=${shown} disabled=${!!disabled}
        onFocus=${() => setFocusVal(String(value))} onBlur=${() => setFocusVal(null)}
        onChange=${(e) => { const v = e.currentTarget.value; setFocusVal(v); onPick(v); }}>
        ${(choices || []).map((c) => html`<option key=${String(c.value)} value=${String(c.value)}>${c.text}</option>`)}
      </select>
    </div>`;
  }

  function SegOption({ opt, onSave }) {
    const [shown, run] = usePending(opt.value);
    const labelId = ui.useId('set-opt');
    const options = (opt.choices || []).map((c) => ({ value: c.value, label: shortText(c.text) }));
    const detail = detailText(choiceText(opt, shown));
    return html`<p class="set-opt-label" id=${labelId}>${opt.label}</p>
      <${ui.Segmented} options=${options} value=${shown} label=${opt.label} onChange=${(v) => run(v, onSave(v))}/>
      ${detail ? html`<p class="set-opt-detail">${detail}</p>` : null}`;
  }

  function SelectOption({ opt, onSave }) {
    const [shown, run] = usePending(opt.value);
    return html`<${Picker} label=${opt.label} value=${shown} choices=${opt.choices} onPick=${(v) => run(v, onSave(v))}/>`;
  }

  // type = Art der Meldung oder 'global'. Die Rückmeldung bleibt stehen, auch
  // wenn das Bedienelement nach einem Fehlschlag neu aufgebaut wird.
  function OptionRow({ type, opt, idPrefix }) {
    const rev = store.useStore((s) => s.rev.notify);
    const [msg, setMsg] = useState(null);
    const show = useShow(setMsg);
    const save = (v) => {
      setMsg(null);
      return store.notifySet(type, opt.key, v).then(show);
    };
    const seg = asSegment(opt);
    return html`<li class=${cx('row set-opt', seg ? 'row-block' : 'set-opt-select')} id=${`${idPrefix}-${opt.key}`}>
      ${seg
        ? html`<${SegOption} key=${`${opt.key}:${rev}`} opt=${opt} onSave=${save}/>`
        : html`<${SelectOption} key=${`${opt.key}:${rev}`} opt=${opt} onSave=${save}/>`}
      <${ui.Msg} msg=${msg} class="set-msg"/>
    </li>`;
  }

  // Probe: höchstens eine gleichzeitig (der Versand dauert bis ~20 s; ein
  // Doppeltipp schickte sonst eine zweite, evtl. kritische Meldung hinterher).
  function ProbeRow({ type }) {
    const inFlight = store.useStore((s) => s.probeInFlight);
    const [msg, setMsg] = useState(null);
    const show = useShow(setMsg);
    const send = () => {
      if (store.getState().probeInFlight) return;
      setMsg({ ok: true, kind: 'info', text: 'Probe-Meldung wird gesendet…' });
      store.notifyTest(type).then(show);
    };
    return html`<${ui.Row} id="set-probe" label="Probe senden" icon="test" iconTone="info" chevron=${false} disabled=${inFlight} onClick=${send}>
      <${ui.Msg} msg=${msg} class="set-msg"/>
    </${ui.Row}>`;
  }

  function TypePage({ typeKey }) {
    const n = store.useStore((s) => s.notify);
    const error = store.useStore((s) => s.notifyError);
    const t = n ? n.types.find((x) => x.key === typeKey) || null : null;
    // Unbekannte Art (oder auf dem Server entfallen): zurück zur Liste.
    useEffect(() => { if (n && !t) root.location.replace('#/einstellungen/meldungen'); }, [n, t]);
    if (!t) {
      return html`<${ui.Page} title="Meldung" back=${BACK_NOTIFY} class="set page-narrow"><${Loading} error=${error}/></${ui.Page}>`;
    }
    return html`<${ui.Page} title=${t.label} back=${BACK_NOTIFY} class="set set-type page-narrow">
      <p class="set-type-hint">${t.hint}</p>
      ${n.configured ? null : html`<${Unconfigured}/>`}
      <${ui.Group} class="set-g-type-switch" footer=${t.important ? 'Sicherheitsrelevant – Abschalten nur mit Rückfrage.' : undefined}>
        <${NotifyTypeRow} t=${t} link=${false}/>
      </${ui.Group}>
      ${t.options.length ? html`<${ui.Group} header="Einstellungen" class="set-g-options">
        ${t.options.map((o) => html`<${OptionRow} key=${o.key} type=${t.key} opt=${o} idPrefix="nopt"/>`)}
      </${ui.Group}>` : null}
      <${ui.Group} class="set-g-probe" footer="„Probe senden“ schickt eine Beispiel-Meldung genau so, wie sie mit den aktuellen Einstellungen ankäme – auch, wenn diese Meldung gerade abgeschaltet ist.">
        <${ProbeRow} type=${t.key}/>
      </${ui.Group}>
    </${ui.Page}>`;
  }

  // ---- Ruhezeit & Allgemein (globale Optionen) ----------------------------------
  function GeneralPage({ back }) {
    const n = store.useStore((s) => s.notify);
    const error = store.useStore((s) => s.notifyError);
    if (!n) {
      return html`<${ui.Page} title="Ruhezeit & Allgemein" back=${back} class="set page-narrow"><${Loading} error=${error}/></${ui.Page}>`;
    }
    const isQuiet = (o) => /^quiet_/.test(o.key);
    const rows = (list) => list.map((o) => html`<${OptionRow} key=${o.key} type="global" opt=${o} idPrefix="gopt"/>`);
    return html`<${ui.Page} title="Ruhezeit & Allgemein" back=${back} class="set set-general-page page-narrow">
      ${n.configured ? null : html`<${Unconfigured}/>`}
      ${n.quiet_now ? html`<p class="set-quiet-now"><${ui.Icon} name="moon"/><span>Gerade ist Ruhezeit – Meldungen kommen nur leise an.</span></p>` : null}
      <${ui.Group} header="Ruhezeit" class="set-g-quiet"
        footer="In dieser Zeit kommen alle Meldungen nur leise an – außer denen, bei denen „In der Ruhezeit: wie eingestellt (auch nachts)“ gewählt ist.">
        ${rows(n.global.filter(isQuiet))}
      </${ui.Group}>
      <${ui.Group} header="Für alle Meldungen" class="set-g-global"
        footer="Der Ton wirkt erst ab Dringlichkeit „normal“ – leise Meldungen haben keinen. „Zusätzlich senden an“: Diese Person aus der Empfängerliste bekommt dieselben Meldungen (Stellvertreter).">
        ${rows(n.global.filter((o) => !isQuiet(o)))}
      </${ui.Group}>
    </${ui.Page}>`;
  }

  // ==== Demo-Modus =================================================================
  // Zustand, Abgleich mit der Box (Regeln wie bisher renderDemo), Auswahl des
  // Test-Empfängers (nur zum Einschalten) und der Schalter mit Rückfrage.
  const DEMO_SYNC = {
    unknown: { tone: 'warn', text: 'Sync-Status unbekannt – keine aktuelle Antwort vom NAS. Nicht davon ausgehen, dass gefahrlos getestet werden kann!' },
    offline: { tone: 'crit', text: 'Achtung: Die Box ist OFFLINE – ein Relaisalarm würde noch mit der ALTEN Liste senden!' },
    synced: { tone: 'ok', text: 'Die Box hat die Demo-Liste übernommen – du kannst jetzt gefahrlos testen.' },
    pending: { tone: 'warn', text: 'Achtung: Die Box hat die Demo-Liste NOCH NICHT übernommen (das dauert einige Sekunden) – ein Relaisalarm würde noch an ALLE gehen!' }
  };

  function DemoPage() {
    const demo = store.useStore((s) => s.demo);
    const keys = store.useStore((s) => s.keys);
    const busy = store.useStore((s) => s.busyCount > 0);
    const sync = ui.useDerived((d) => d.demoSync || '');
    const [target, setTarget] = useState('');
    const [msg, setMsg] = useState(null);
    const show = useShow(setMsg);
    const demoOn = !!(demo && demo.enabled);
    // Wechselt der Modus (auch aus einer anderen Sitzung), passt eine offene
    // Frage nicht mehr - schließen.
    const ask = useOwnConfirm(`${demoOn}|${demo ? demo.key_id : ''}`);
    const keysCorrupt = store.useStore((s) => s.keysCorrupt);
    const keysError = store.useStore((s) => s.keysError);
    // null = Liste noch nicht geladen (bzw. beschädigt) - dann weder „keine
    // Empfänger“ behaupten noch einschalten lassen.
    const entries = keys && Array.isArray(keys.keys) ? keys.keys : null;
    const chosen = entries ? entries.find((k) => String(k.id) === target) || entries[0] || null : null;
    const now = Date.now() / 1000 + (store.getState().serverOffset || 0);

    const toggle = () => {
      setMsg(null);
      if (!demo) return;
      if (demoOn) {
        ask({
          title: 'Demo-Modus beenden?',
          text: 'Demo-Modus beenden? Danach gehen Alarme wieder an ALLE Empfänger (LIVE).',
          confirmLabel: 'Ja, LIVE schalten',
          hold: false,
          onConfirm: () => store.demoSet(false).then(show)
        });
        return;
      }
      if (!chosen) {
        show({ ok: false, kind: 'error', text: 'Bitte zuerst einen Empfänger in der Liste anlegen und als Test-Empfänger auswählen.' });
        return;
      }
      // Name UND maskierter Key (wie in der Auswahl): Bei zwei gleichnamigen
      // Empfängern sagt die Frage, welches iPhone die Alarme bekommt.
      const pick = chosen;
      ask({
        title: 'Demo-Modus einschalten?',
        text: `Demo-Modus einschalten? Alarme gehen dann NUR an "${pick.label} (${pick.key_masked})". Nach dem Testen unbedingt wieder auf LIVE zurückschalten!`,
        confirmLabel: 'Ja, Demo-Modus einschalten',
        hold: false,
        onConfirm: () => store.demoSet(true, pick.id).then(show)
      });
    };

    let state = html`<p class="set-demo-mode">Wird geladen…</p>`;
    if (demo && demoOn) {
      const s = DEMO_SYNC[sync] || DEMO_SYNC.unknown;
      state = html`<p class="set-demo-mode"><strong class="set-demo-on">DEMO-MODUS</strong> – Alarme gehen NUR an <strong>${demo.label || 'den Test-Empfänger'}</strong>.</p>
        <p class="set-demo-meta">${demo.key_masked ? html`<span class="mono">${demo.key_masked}</span> · ` : null}seit ${
          demo.changed_at ? L.fmtDayTime(Number(demo.changed_at), now) : 'unbekannt'}</p>
        <p class=${cx('set-demo-sync', `is-${s.tone}`)} role="status">${s.tone === 'ok' ? '✓ ' : '! '}${s.text}</p>`;
    } else if (demo) {
      state = html`<p class="set-demo-mode"><strong class="set-demo-live">LIVE</strong> – Alarme gehen an alle Empfänger.</p>`;
    }
    return html`<${ui.Page} title="Demo-Modus" back=${BACK_MAIN} class="set set-demo-page page-narrow">
      <section class=${cx('set-demo-state', demoOn && 'is-on')} aria-label="Zustand">${state}</section>
      ${demo && !demoOn ? html`<${ui.Group} header="Test-Empfänger" class="set-g-demo-target">
        ${entries && entries.length
          ? html`<li class="row set-opt-select"><${Picker} selectId="set-demo-target" label="Alarme nur an" value=${chosen ? String(chosen.id) : ''}
              choices=${entries.map((k) => ({ value: String(k.id), text: `${k.label} (${k.key_masked})` }))}
              onPick=${(v) => { setMsg(null); setTarget(v); }}/></li>`
          : html`<${ui.Item} class=${cx('set-empty', keysCorrupt && 'is-error')}>${entries
            ? 'Noch keine Empfänger eingetragen – bitte zuerst unter „Empfänger“ jemanden anlegen.'
            : (keysCorrupt || keysError || 'Empfängerliste wird geladen…')}</${ui.Item}>`}
      </${ui.Group}>` : null}
      <button type="button" class=${cx('btn btn-block set-demo-toggle', demoOn && 'is-end')}
        disabled=${busy || !demo || (!demoOn && !entries)} onClick=${toggle}>${
        demoOn ? 'Demo-Modus beenden (zurück zu LIVE)' : 'Demo-Modus einschalten'}</button>
      <${ui.Msg} msg=${msg} class="set-demo-msg"/>
      <p class="set-foot">Im Demo-Modus gehen alle Alarme nur an den gewählten Test-Empfänger – vom Dashboard wie von der Box.
        Die Box übernimmt den Wechsel erst nach einigen Sekunden (das Band oben zeigt den Stand). Nach dem Testen zurück auf LIVE schalten!</p>
    </${ui.Page}>`;
  }

  // ==== Automatisch wieder laut nach (Spec §8.4) =====================================
  function MuteChoices({ m, onSave }) {
    const [shown, run] = usePending(String(m.value));
    const headId = ui.useId('set-mute-head');
    return html`<section class="group set-g-mute" aria-labelledby=${headId}>
      <h2 class="group-head" id=${headId}><span>Wieder laut nach</span></h2>
      <div class="group-body" role="group" aria-labelledby=${headId}>
        ${(m.choices || []).map((c) => {
          const v = String(c.value);
          const on = v === String(shown);
          return html`<div class="row" key=${v}>
            <button type="button" class="row-main set-choice" data-value=${v} aria-pressed=${on ? 'true' : 'false'}
              onClick=${() => { if (!on) run(v, onSave(v, shortText(c.text))); }}>
              <span class="row-text"><span class="row-label">${shortText(c.text)}</span></span>
              ${/\(Standard\)\s*$/.test(c.text) ? html`<span class="row-value">Standard</span>` : null}
              <span class="set-check" aria-hidden="true">${on ? html`<${ui.Icon} name="check"/>` : null}</span>
            </button>
          </div>`;
        })}
      </div>
      <p class="group-foot">Gilt auch für laufende Stummschaltungen.</p>
    </section>`;
  }

  function MutePage() {
    const settings = store.useStore((s) => s.settings);
    const error = store.useStore((s) => s.settingsError);
    const rev = store.useStore((s) => s.rev.settings);
    const [msg, setMsg] = useState(null);
    const show = useShow(setMsg);
    const m = settings && settings.mute_max_seconds;
    const ask = useOwnConfirm(m ? m.value : null);
    // Verkürzen wirkt sofort auf laufende Stummschaltungen: Wer schon länger
    // stumm ist, wird gleich laut (und bekommt eine Nachricht) - dann erst
    // fragen und die Zahl nennen. Rückgabe null = Bestätigung offen (die
    // Auswahl springt erst nach dem Speichern um).
    const save = (v, label) => {
      setMsg(null);
      const s = store.getState();
      const keys = s.keys && Array.isArray(s.keys.keys) ? s.keys.keys : null;
      const impact = L.muteShortenImpact(keys, Number(v), Number(m.value), store.derive(s).serverTime);
      const put = () => store.settingsSet('mute_max_seconds', v).then(show);
      if (impact && (impact.count === null || impact.count > 0)) {
        const q = L.muteShortenQuestion(impact, label);
        ask({ title: q.title, text: q.text, confirmLabel: 'Ja, verkürzen', hold: false, onConfirm: put });
        return null;
      }
      return put();
    };
    return html`<${ui.Page} title="Automatisch wieder laut nach" back=${BACK_MAIN} class="set set-mute-page page-narrow">
      <p class="set-intro">Sicherheitsnetz: Wer stumm geschaltet ist, wird nach dieser Zeit automatisch wieder laut – falls das
        Zurückschalten (z.${NBSP}B. per Kurzbefehl) einmal hakt. „Dauerhaft stumm“ ist davon ausgenommen.</p>
      ${m ? html`<${MuteChoices} key=${rev} m=${m} onSave=${save}/>` : html`<${Loading} error=${error}/>`}
      <${ui.Msg} msg=${msg} class="set-mute-msg"/>
    </${ui.Page}>`;
  }

  // ==== Box: Details & Diagnose =======================================================
  // Alle Werte der Box in den Gruppen der Logik (Alarmkette, Verbindung,
  // Wartung); Antippen klappt die Erklärung auf. Ohne frische Daten sind die
  // Werte grau (boxRows) und oben steht, von wann der Stand ist.
  function staleNoteOf(s) {
    if (!s.statusData) return '';
    const d = store.derive(s);
    if (!d.offline && !d.unsure) return '';
    const age = ui.statusAge(s);
    if (!Number.isFinite(age)) return 'Die Box hat sich noch nie gemeldet – es gibt noch keine Werte.';
    const when = L.fmtDayTime(Math.round(d.serverTime - age), d.serverTime);
    return d.unsure
      ? `Keine aktuelle Antwort vom NAS – letzter bekannter Stand von ${when}.`
      : `Box offline – letzter bekannter Stand von ${when}.`;
  }

  function BoxRow({ r, open, onToggle }) {
    const tipId = `box-tip-${r.id}`;
    return html`<li class=${cx('row box-row', ui.toneClass(r.tone), open && 'is-open')} id=${`box-${r.id}`}>
      <button type="button" class="row-main" aria-expanded=${open ? 'true' : 'false'} aria-controls=${open ? tipId : undefined} onClick=${onToggle}>
        <span class="row-text"><span class="row-label">${r.label}</span></span>
        <span class="row-value">${r.value}</span>
        <${ui.Icon} name="chevron" class="row-chev box-chev"/>
      </button>
      ${open ? html`<p class="box-tip" id=${tipId}>${r.tip}</p>` : null}
    </li>`;
  }

  function BoxPage() {
    const rows = useDerivedJson((d) => d.boxRows);
    const stale = store.useStore(staleNoteOf);
    const [open, setOpen] = useState([]);
    const toggle = (id) => setOpen((cur) => (cur.includes(id) ? cur.filter((x) => x !== id) : cur.concat(id)));
    const groups = [];
    rows.forEach((r) => {
      let g = groups.find((x) => x.name === r.group);
      if (!g) groups.push(g = { name: r.group, rows: [] });
      g.rows.push(r);
    });
    return html`<${ui.Page} title="Box" back=${BACK_MAIN} class="set set-box-page page-narrow">
      ${stale ? html`<p class="set-box-stale" role="status"><${ui.Icon} name="clock"/><span>${stale}</span></p>` : null}
      ${groups.map((g) => html`<${ui.Group} key=${g.name} header=${g.name} class="set-g-boxrows">
        ${g.rows.map((r) => html`<${BoxRow} key=${r.id} r=${r} open=${open.includes(r.id)} onToggle=${() => toggle(r.id)}/>`)}
      </${ui.Group}>`)}
      <p class="set-foot">Zeile antippen: Erklärung, was der Wert bedeutet.</p>
      <details class="set-probe-window">
        <summary>Mittwochs leiser? Das Probealarm-Fenster</summary>
        <p>Der wöchentliche Probealarm der Leitstelle löst bewusst die <strong>komplette</strong> Alarmkette aus – er kommt nur
          leiser an: Laut Firmware-Einstellung sendet die Box mittwochs zwischen 18:55 und 19:05${NBSP}Uhr mit Lautstärke 5
          statt 10; alles andere (Ton, Text, Critical Alert) ist identisch. Alarme außerhalb dieses Fensters kommen immer in
          voller Lautstärke.</p>
      </details>
    </${ui.Page}>`;
  }

  // ==== Seite: Unterseite nach der Adresse ==========================================
  // Unbekannte Unterseite bzw. (Lese-Rolle) eine Admin-Seite: zurück zur
  // Liste, ohne neuen Verlaufseintrag. „Ruhezeit & Allgemein“ erreicht man
  // aus der Liste UND aus „Meldungen“ - Zurück führt dorthin, woher man kam.
  FW.views.Settings = function Settings({ path }) {
    const readOnly = store.useStore((s) => s.readOnly);
    const p = Array.isArray(path) ? path : [];
    const page = p[0] || '';
    const key = p.join('/');
    const prev = useRef({ key: null, before: null });
    if (prev.current.key !== key) prev.current = { key, before: prev.current.key };
    const known = page === '' || (page === 'box' && p.length === 1)
      || (page === 'meldungen' && p.length <= 2) || (ADMIN_PAGES.includes(page) && p.length === 1);
    const allowed = known && !(readOnly && ADMIN_PAGES.includes(page));
    useEffect(() => { if (!allowed) root.location.replace('#/einstellungen'); }, [allowed, key]);

    if (!allowed || page === '') return html`<${MainList}/>`;
    if (page === 'box') return html`<${BoxPage}/>`;
    if (page === 'meldungen') return p[1] ? html`<${TypePage} key=${p[1]} typeKey=${p[1]}/>` : html`<${NotifyPage}/>`;
    if (page === 'allgemein') {
      const from = String(prev.current.before || '').split('/')[0];
      return html`<${GeneralPage} back=${from === 'meldungen' ? BACK_NOTIFY : BACK_MAIN}/>`;
    }
    if (page === 'demo') return html`<${DemoPage}/>`;
    return html`<${MutePage}/>`;
  };
})(typeof window !== 'undefined' ? window : globalThis);
