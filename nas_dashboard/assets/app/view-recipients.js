(function (root) {
  'use strict';
  const FW = root.FW = root.FW || {};
  const { html, useState, useEffect, useRef } = root.htmPreact;
  const store = FW.store;
  const ui = FW.ui;
  const L = FW.logic;
  const cx = ui.cx;
  FW.views = FW.views || {};

  // ==== Empfänger (Spec §6.3) ===============================================
  // Adresse #/empfaenger bzw. #/empfaenger/<id>. Liste mit Status-Pillen und
  // Abgleich-Zeile; die Person öffnet am iPhone als Sheet von unten, am PC
  // als Panel rechts neben der Liste. Jede Änderung speichert sofort über den
  // Store; Rückmeldung steht am jeweiligen Bedienelement (Fehler bleiben).
  // Bedienelemente mit eigenem Zustand (Segmente, Regler, Telefon-Feld) sind
  // auf state.rev.keys geschlüsselt: Scheitert eine Speicherung, zeigen sie
  // wieder den Stand des Servers. Live-Abfragen schließen nichts, setzen
  // nichts zurück und nehmen keinen Fokus - die Person ist nur auf ihre id
  // geschlüsselt, halb Getipptes lebt in lokalem Zustand.

  const MAX_KEYS = 10;                                   // wie MAX_ALARM_KEYS (common.php)
  const KEY_RE = /^[A-Za-z0-9_-]{5,64}$/;                // wie valid_bark_key()
  const KEY_HINT = '5–64 Zeichen: Buchstaben, Ziffern, Unter- und Bindestrich (steht in der Bark-App unter dem QR-Code)';
  const SYNC_WARN_MS = 60000;                            // Hinweis „nicht übernommen“ erst nach 60 s
  const NBSP = '\u00a0';
  const MODE_OPTIONS = [
    { value: 'loud', label: 'Laut' },
    { value: 'muted', label: 'Stumm' },
    { value: 'permanent', label: 'Dauerhaft stumm' }
  ];

  // Rückmeldungen erst setzen, wenn die Komponente noch steht.
  function useAlive() {
    const ref = useRef(true);
    useEffect(() => () => { ref.current = false; }, []);
    return ref;
  }
  function useShow(setMsg) {
    const alive = useAlive();
    return (res) => {
      if (alive.current && res) setMsg(res);
      return res;
    };
  }

  // ---- Texte ----------------------------------------------------------------
  // Dauer wie notify_duration_label() in common.php („12 Std“, „30 Min“).
  function durationLabel(seconds) {
    const s = Number(seconds) || 0;
    if (s > 0 && s % 3600 === 0) return `${s / 3600}${NBSP}Std`;
    if (s > 0 && s % 60 === 0) return `${s / 60}${NBSP}Min`;
    return `${s}${NBSP}s`;
  }

  // Heute nur die Uhrzeit, sonst mit Datum (Zeitzone der Wache, Server-Uhr).
  const serverNow = () => Date.now() / 1000 + (store.getState().serverOffset || 0);
  const isToday = (epoch) => L.dayKey(epoch) === L.dayKey(serverNow());
  const whenText = (epoch) => (isToday(epoch) ? L.fmtClock(epoch) : L.fmtDayClock(epoch));
  // Zusatz zur Uhrzeit der Rückschaltung: nichts (heute), „(morgen)“ oder das Datum.
  function dayHint(epoch) {
    if (isToday(epoch)) return '';
    if (L.dayKey(epoch) === L.dayKey(serverNow() + 86400)) return ' (morgen)';
    return ` (${L.fmtDayClock(epoch).split(' ')[0]})`;
  }
  const volShort = (vol) => (vol === 0 ? 'lautlos' : `Lautstärke ${vol}`);
  const volOf = (entry) => Number(entry.mute_volume) || 0;

  // Unterzeile in der Liste: maskierter Key bzw. kurz, wie stumm.
  function listSub(entry, v) {
    const vol = volOf(entry);
    let main;
    if (entry.mute_overdue) {
      main = html`<span class="rcp-sub-warn"><span aria-hidden="true">! </span>Rückschaltung nicht gespeichert – Box evtl. noch stumm</span>`;
    } else if (v.state === 'permanent') {
      main = `dauerhaft · ${volShort(vol)}`;
    } else if (v.state === 'muted') {
      const until = Number(entry.mute_until) || 0;
      main = until ? `Stumm bis ~${L.fmtClock(until)} · ${volShort(vol)}` : `Stumm · ${volShort(vol)}`;
    } else {
      main = html`<span class="mono">${entry.key_masked}</span>`;
    }
    return v.paused ? html`<span>pausiert · </span>${main}` : main;
  }

  // Satz zum Zustand im Kopf der Person („Stumm seit 08:02 · wird um ~20:02
  // automatisch wieder laut“) plus, was beim Alarm ankommt.
  function stateLines(entry, v) {
    if (entry.mute_overdue) return { warn: true, main: v.sub, vol: '' };
    if (v.state === 'loud') return { warn: false, main: 'Laut – Alarme kommen mit vollem Alarmton an.', vol: '' };
    const since = entry.muted_at ? ` seit ${whenText(entry.muted_at)}` : '';
    const until = Number(entry.mute_until) || 0;
    let tail = 'keine automatische Rückschaltung';
    if (v.state === 'muted' && until) tail = `wird um ~${L.fmtClock(until)}${dayHint(until)} automatisch wieder laut`;
    return {
      warn: false,
      main: `${v.state === 'permanent' ? 'Dauerhaft stumm' : 'Stumm'}${since} · ${tail}`,
      vol: `Alarme kommen ${L.volWords(volOf(entry))} an – weiterhin sichtbar als Critical Alert.`
    };
  }

  // ---- Abgleich mit der Box (Versionsvergleich, Hinweis erst nach 60 s) ------
  // Ohne aktuelle Daten wird nichts behauptet: grau „unbekannt“ statt eines
  // alten Grüns. Während der ersten 60 s ist eine Abweichung normal (die Box
  // holt die Liste bei ihrem nächsten Abruf).
  function syncOf(s) {
    if (s.keysCorrupt || !s.keys) return null;
    const d = store.derive(s);
    const status = s.status;
    if (d.unsure || !status) return { tone: 'unknown', text: 'Abgleich mit der Box unbekannt – keine aktuelle Antwort vom NAS' };
    if (d.offline) return { tone: 'unknown', text: 'Box offline – ob sie die Liste übernommen hat, ist gerade unbekannt' };
    if (L.na(status.keys_version) || s.nasKeysVersion === null || s.nasKeysVersion === undefined) {
      return { tone: 'unknown', text: 'Abgleich mit der Box unbekannt' };
    }
    if (String(status.keys_version) === String(s.nasKeysVersion)) return { tone: 'ok', text: 'Box ist auf dem neuesten Stand' };
    if (d.mismatchMs > SYNC_WARN_MS) {
      return { tone: 'warn', text: `Box hat die Änderung noch nicht übernommen (seit ${L.fmtUptime(d.mismatchMs / 1000)})` };
    }
    return { tone: 'pending', text: 'Box übernimmt die Änderung in den nächsten Sekunden…' };
  }

  // Eigene kleine Komponente: Der Text hängt am Sekundentakt (nur echte
  // Wechsel zeichnen neu - die Auswahl ist ein einfacher Text).
  function SyncLine({ count }) {
    const sig = store.useStore((s) => {
      const x = syncOf(s);
      return x ? `${x.tone}|${x.text}` : '';
    });
    if (!sig || count === null) return null;
    const [tone, text] = sig.split('|');
    return html`<p class=${cx('rcp-sync', `is-${tone}`)}>
      <span class="rcp-sync-dot" aria-hidden="true"></span>
      <span class="rcp-sync-count">${count} Empfänger · </span><span class="rcp-sync-text">${tone === 'warn' ? '! ' : ''}${text}</span>
    </p>`;
  }

  // ---- Liste ----------------------------------------------------------------
  function RecipientRow({ entry, demo, current }) {
    const v = L.recipientView(entry, demo, 0);
    const pills = html`<span class="rcp-pills">
      <${ui.Pill} tone=${v.pill.tone} class="rcp-state-pill">${v.pill.text}</${ui.Pill}>
      ${v.isDemoTarget ? html`<${ui.Pill} tone="warn" class="rcp-demo-pill">Demo-Empfänger</${ui.Pill}>` : null}
    </span>`;
    return html`<${ui.Row} id=${`rcp-${entry.id}`} class=${cx('rcp-row', v.paused && 'is-paused')}
      label=${entry.label} sub=${listSub(entry, v)} href=${`#/empfaenger/${entry.id}`} current=${current} right=${pills}/>`;
  }

  function RecipientList({ entries, selId, demo }) {
    const corrupt = store.useStore((s) => s.keysCorrupt);
    const error = store.useStore((s) => s.keysError);
    if (corrupt) return html`<p class="rcp-corrupt"><span aria-hidden="true">✕ </span>${corrupt}</p>`;
    if (!entries) {
      return html`<${ui.Group} class="rcp-list"><${ui.Item} class="rcp-empty">${error || 'Wird geladen…'}</${ui.Item}></${ui.Group}>`;
    }
    if (!entries.length) {
      return html`<${ui.Group} class="rcp-list"><${ui.Item} class="rcp-empty">Noch keine Empfänger eingetragen – die Box nutzt solange ihre fest eingebaute Startliste.</${ui.Item}></${ui.Group}>`;
    }
    return html`<${ui.Group} class="rcp-list">${entries.map((e) => html`<${RecipientRow} key=${e.id} entry=${e} demo=${demo}
      current=${selId === Number(e.id)}/>`)}</${ui.Group}>`;
  }

  // ---- Lautstärke für die Stumm-Frage ------------------------------------------
  // Die Frage muss die Lautstärke nennen, die der Regler ZEIGT - auch wenn
  // deren Speicherung noch läuft bzw. die Liste sie noch nicht zurückgemeldet
  // hat (Regler 4 -> 0, sofort „Stumm“: sonst hieße es „leise (4 von 10)“,
  // ankommen würde aber lautlos). volRef hält die zuletzt abgeschickte Stufe
  // samt dem Server-Wert, gegen den sie lief ({value, base}); sobald die
  // Liste einen anderen Wert meldet, gilt wieder der Server.
  function questionVolume(entry, volRef) {
    const server = volOf(entry);
    const p = volRef.current;
    return p && p.base === server ? p.value : server;
  }
  // Per Tastatur gewählte, noch nicht gespeicherte Stufe (ui.Slider speichert
  // erst nach 0,8 s bzw. beim Verlassen): vor der Frage abschicken. Ein
  // echter Tipp auf „Stumm“ nimmt dem Regler meist schon selbst den Fokus -
  // Safari am Mac fokussiert Knöpfe beim Klick aber nicht.
  function flushSlider(id) {
    const a = root.document && root.document.activeElement;
    const person = a && a.closest ? a.closest('.rcp-person') : null;
    if (person && a.classList.contains('slider-input') && person.getAttribute('data-id') === String(id)) a.blur();
  }

  // ---- Person: Laut / Stumm / Dauerhaft stumm -------------------------------
  // Lauter geht sofort; leiser fragt nach und nennt die tatsächliche
  // Stumm-Lautstärke. Dauerhaft -> Stumm ist die sichere Richtung (das
  // Sicherheitsnetz greift wieder) und geht ohne Rückfrage.
  function ModeControl({ entry, state, volRef }) {
    const rev = store.useStore((s) => s.rev.keys);
    const [msg, setMsg] = useState(null);
    const show = useShow(setMsg);
    const id = Number(entry.id);
    const name = entry.label;
    const change = (want) => {
      if (want === state) return null;
      setMsg(null);
      if (want === 'loud') return store.keyMute(id, false).then(show);
      if (want === 'muted' && state === 'permanent') return store.keyPermanent(id, false).then(show);
      const permanent = want === 'permanent';
      flushSlider(id);
      store.openConfirm({
        title: permanent ? 'Dauerhaft stumm schalten?' : 'Stumm schalten?',
        text: L.muteQuestion(name, want, questionVolume(entry, volRef)),
        confirmLabel: permanent ? 'Ja, dauerhaft stumm' : 'Ja, stumm schalten',
        hold: false,
        onConfirm: () => (permanent ? store.keyPermanent(id, true) : store.keyMute(id, true)).then(show)
      });
      return null;
    };
    return html`<div class="rcp-mode">
      <${ui.Segmented} key=${`mode:${id}:${rev}`} options=${MODE_OPTIONS} value=${state} onChange=${change}
        label=${`Alarmton für ${name}`}/>
      <${ui.Msg} msg=${msg} class="rcp-msg-mode"/>
    </div>`;
  }

  // Regler 0–10: gespeichert beim Loslassen (Tastatur: nach 0,8 s bzw. beim
  // Verlassen - siehe ui.Slider). Gilt, sobald die Person stumm ist.
  // Überfällige Rückschaltung (NAS rechnet laut, nicht gespeichert): statt
  // „gilt, wenn stumm“ die Warnung, dass die Box evtl. noch so alarmiert.
  function VolumeControl({ entry, state, overdue, volRef }) {
    const rev = store.useStore((s) => s.rev.keys);
    const [msg, setMsg] = useState(null);
    const show = useShow(setMsg);
    const id = Number(entry.id);
    const commit = (v) => {
      setMsg(null);
      const mine = { value: v, base: volOf(entry) };
      volRef.current = mine;
      return store.keyVolume(id, v).then((res) => {
        if ((!res || !res.ok) && volRef.current === mine) volRef.current = null;
        return show(res);
      });
    };
    let hint = null;
    if (overdue) {
      hint = html`<p class="rcp-vol-hint is-warn"><span aria-hidden="true">! </span>Die Box alarmiert evtl. noch mit dieser Lautstärke – die Rückschaltung auf laut ist nicht gespeichert.</p>`;
    } else if (state === 'loud') {
      hint = html`<p class="rcp-vol-hint">gilt, wenn stumm</p>`;
    }
    return html`<section class="group rcp-vol"><div class="group-body rcp-vol-box">
      <${ui.Slider} key=${`vol:${id}:${rev}`} min=${0} max=${10} value=${volOf(entry)} onCommit=${commit}
        label="Stumm-Lautstärke" tone="warn" minLabel="0" maxLabel="10" valueText=${(v) => (v === 0 ? 'lautlos' : String(v))}/>
      ${hint}
      <${ui.Msg} msg=${msg} class="rcp-msg-vol"/>
    </div></section>`;
  }

  // ---- Telefon (nur Admin): eintragen/ändern, speichert bei Enter/Verlassen --
  // Zeigt nach dem Speichern den neuen Wert, bis die Liste ihn bestätigt;
  // scheitert es, gilt wieder der Wert des Servers (Feld neu über rev).
  function PhoneInput({ value, onSave, id }) {
    const [draft, setDraft] = useState(null);       // null = zeigt den gespeicherten Wert
    const [pending, setPending] = useState(null);   // {value, base} während/nach dem Speichern
    const alive = useAlive();
    const shown = draft !== null ? draft : (pending && pending.base === value ? pending.value : value);
    const commit = () => {
      if (draft === null) return;
      const next = draft.trim();
      setDraft(null);
      if (next === value.trim()) return;
      const mine = { value: next, base: value };
      setPending(mine);
      const drop = () => { if (alive.current) setPending((cur) => (cur === mine ? null : cur)); };
      onSave(next).then((res) => { if (!res || !res.ok) drop(); }, drop);
    };
    return html`<input id=${id} class="rcp-inline-input rcp-phone-input" type="tel" inputmode="tel" autocomplete="tel"
      maxlength="30" placeholder="optional" value=${shown} enterkeyhint="done"
      onInput=${(e) => setDraft(e.currentTarget.value)} onBlur=${commit}
      onKeyDown=${(e) => { if (e.key === 'Enter') { e.preventDefault(); commit(); } }}/>`;
  }

  function PhoneRows({ entry }) {
    const rev = store.useStore((s) => s.rev.keys);
    const [msg, setMsg] = useState(null);
    const show = useShow(setMsg);
    const fieldId = ui.useId('rcp-phone');
    const id = Number(entry.id);
    const phone = String(entry.phone || '');
    const tel = phone.replace(/[^0-9+]/g, '');
    const save = (next) => {
      setMsg(null);
      return store.keyPhone(id, next).then(show);
    };
    return [
      // Die ganze 44-px-Zeile ist das Label: Ein Tipp irgendwo darauf setzt
      // den Cursor ins Feld (das Feld selbst ist nur so hoch wie seine Schrift).
      html`<li class="row rcp-phone-row" key="phone">
        <label class="row-main rcp-field">
          <span class="row-label">Telefon</span>
          <${PhoneInput} key=${`tel:${id}:${rev}`} id=${fieldId} value=${phone} onSave=${save}/>
        </label>
        <${ui.Msg} msg=${msg} class="rcp-msg-phone"/>
      </li>`,
      tel ? html`<li class="row has-icon" key="call"><a class="row-main rcp-call" href=${`tel:${tel}`} aria-label=${`${entry.label} anrufen (${phone})`}>
        <span class="tile tile-ok" aria-hidden="true"><${ui.Icon} name="phone"/></span>
        <span class="row-text"><span class="row-label">Anrufen</span></span>
        <${ui.Icon} name="chevron" class="row-chev"/>
      </a></li>` : null
    ];
  }

  // ---- Kurzbefehl-Links „Stumm“ / „Laut“ (Geheim-Token, nur Admin) -----------
  // Basis-Adresse wie bisher (muteLinkBase): bevorzugt die öffentliche
  // Adresse aus config.php (meta public-base) - wer das Dashboard intern über
  // die NAS-IP öffnet, bekommt trotzdem einen Link, der unterwegs auf dem
  // iPhone funktioniert. Immer GENAU ein „/“ am Ende; ungültig/leer ->
  // aktuelle Adresse.
  function muteLinkBase() {
    const publicBase = store.getState().publicBase;
    if (publicBase) {
      try {
        const base = new URL(publicBase, root.location.href);
        base.pathname = base.pathname.replace(/\/*$/, '/');
        base.search = '';
        base.hash = '';
        return base;
      } catch (err) {
        // ungültige Konfiguration -> Fallback unten
      }
    }
    return new URL('./', root.location.href);
  }

  function LinkRows({ entry }) {
    const [msg, setMsg] = useState(null);
    const [copied, setCopied] = useState('');
    const show = useShow(setMsg);
    const alive = useAlive();
    const timer = useRef(0);
    useEffect(() => () => clearTimeout(timer.current), []);
    if (!entry.mute_token) return null;
    const copy = async (state) => {
      const url = new URL('api/mute.php', muteLinkBase());
      url.searchParams.set('t', entry.mute_token);
      url.searchParams.set('state', state);
      const what = state === 'on' ? 'Stumm' : 'Laut';
      setMsg(null);
      try {
        const clip = root.navigator && root.navigator.clipboard;
        if (!clip || typeof clip.writeText !== 'function') throw new Error('keine Zwischenablage');
        await clip.writeText(url.toString());
        show({ ok: true, kind: 'ok', text: `„${what}“-Link für ${entry.label} kopiert – im iPhone-Kurzbefehl unter „Inhalte von URL abrufen“ einfügen.` });
        if (alive.current) setCopied(state);
        clearTimeout(timer.current);
        timer.current = setTimeout(() => { if (alive.current) setCopied(''); }, 1600);
      } catch (err) {
        // Zwischenablage blockiert (Berechtigung, alter Browser): Link zum
        // manuellen Kopieren anzeigen.
        show({ ok: false, kind: 'warn', text: `Kopieren nicht möglich – Link („${what}“): ${url.toString()}` });
      }
    };
    const row = (state, label) => html`<${ui.Row} key=${state} class=${`rcp-link-${state}`} label=${label} icon="link" iconTone="neutral"
      value=${copied === state ? '✓ kopiert' : 'Kopieren'} tone=${copied === state ? 'ok' : ''} chevron=${false} onClick=${() => copy(state)}/>`;
    return [
      row('on', 'Kurzbefehl-Link „Stumm“'),
      row('off', 'Kurzbefehl-Link „Laut“'),
      msg ? html`<li class="row row-block rcp-msg-row" key="msg"><${ui.Msg} msg=${msg} class="rcp-msg-link"/></li>` : null
    ];
  }

  // ---- Test-Push (Rückfrage wie bisher; höchstens einer gleichzeitig) --------
  function confirmTestPush(id, name, show) {
    return store.openConfirm({
      title: 'Test-Push senden?',
      text: `Test-Push an "${name}" senden? Auf dem iPhone dort spielt einmal der ALARMTON – als normale Mitteilung, kein Alarm (bei Stummschalter oder Fokus bleibt sie leise). Am besten vorher Bescheid geben.`,
      confirmLabel: 'Ja, Test-Push senden',
      hold: false,
      onConfirm: () => {
        if (!store.getState().keyTestInFlight) show({ ok: true, kind: 'info', text: `Test-Push an ${name} wird gesendet…` });
        return store.keyTest(id).then(show);
      }
    });
  }

  function TestRow({ entry }) {
    const inFlight = store.useStore((s) => s.keyTestInFlight);
    const [msg, setMsg] = useState(null);
    const show = useShow(setMsg);
    const open = () => {
      setMsg(null);
      confirmTestPush(Number(entry.id), entry.label, show);
    };
    return html`<${ui.Row} class="rcp-test" label="Test-Push senden" icon="test" iconTone="info" disabled=${inFlight} onClick=${open}>
      <${ui.Msg} msg=${msg} class="rcp-msg-test"/>
    </${ui.Row}>`;
  }

  // Aufgeklapptes Feld in einer Zeile: beim Öffnen Fokus ins Feld, beim
  // Schließen zurück auf den Zeilen-Knopf - aber nur, wenn der Fokus mit dem
  // Feld verloren ging (nicht, wenn inzwischen woanders geklickt wurde).
  function useInlineFocus(open, inputRef, rowId, select) {
    const wasOpen = useRef(false);
    useEffect(() => {
      if (open) {
        wasOpen.current = true;
        if (inputRef.current) {
          inputRef.current.focus();
          if (select) inputRef.current.select();
        }
        return;
      }
      if (!wasOpen.current) return;
      wasOpen.current = false;
      const doc = root.document;
      const active = doc.activeElement;
      const row = doc.getElementById(rowId);
      const btn = row && row.querySelector('.row-main');
      if (btn && (!active || active === doc.body)) btn.focus({ preventScroll: true });
    }, [open]);
  }

  // ---- Umbenennen (Feld in der Zeile; Enter oder „Sichern“ speichert) --------
  // Verlassen des Felds speichert bewusst NICHT - ein halb getippter Name
  // soll nie versehentlich übernommen werden.
  function RenameRow({ entry }) {
    const [open, setOpen] = useState(false);
    const [draft, setDraft] = useState('');
    const [saving, setSaving] = useState(false);
    const [msg, setMsg] = useState(null);
    const alive = useAlive();
    const inputRef = useRef(null);
    const fieldId = ui.useId('rcp-rename');
    const rowId = ui.useId('rcp-rename-row');
    useInlineFocus(open, inputRef, rowId, true);
    const start = () => {
      setMsg(null);
      setDraft(entry.label);
      setOpen(true);
    };
    const save = (e) => {
      if (e) e.preventDefault();
      if (saving) return;
      const next = draft.trim();
      if (!next) {
        setMsg({ ok: false, kind: 'error', text: 'Bitte einen Namen eingeben.' });
        return;
      }
      if (next === entry.label) {
        setOpen(false);
        return;
      }
      setSaving(true);
      setMsg(null);
      store.keyRename(Number(entry.id), next).then((res) => {
        if (!alive.current) return;
        setSaving(false);
        setMsg(res);
        if (res.ok) setOpen(false);
      });
    };
    if (!open) {
      return html`<${ui.Row} id=${rowId} class="rcp-rename" label="Umbenennen" icon="edit" iconTone="neutral" onClick=${start}>
        <${ui.Msg} msg=${msg} class="rcp-msg-rename"/>
      </${ui.Row}>`;
    }
    return html`<li class="row row-block rcp-edit">
      <form class="rcp-edit-form" onSubmit=${save}>
        <label class="rcp-field-label" for=${fieldId}>Neuer Name</label>
        <input id=${fieldId} ref=${inputRef} class="input rcp-rename-input" maxlength="40" autocomplete="off" enterkeyhint="done"
          value=${draft} onInput=${(e) => setDraft(e.currentTarget.value)}/>
        <div class="btn-row rcp-edit-btns">
          <button type="button" class="btn" onClick=${() => { setOpen(false); setMsg(null); }}>Abbrechen</button>
          <button type="submit" class="btn rcp-rename-save" disabled=${saving}>Sichern</button>
        </div>
      </form>
      <${ui.Msg} msg=${msg} class="rcp-msg-rename"/>
    </li>`;
  }

  // ---- Bark-Key ersetzen (neues iPhone / neue App) ----------------------------
  // Behält id, Name, Stumm-Zustand, Lautstärke, Kurzbefehl-Token und Telefon.
  function ReplaceRow({ entry }) {
    const [open, setOpen] = useState(false);
    const [draft, setDraft] = useState('');
    const [msg, setMsg] = useState(null);
    const show = useShow(setMsg);
    const alive = useAlive();
    const inputRef = useRef(null);
    const fieldId = ui.useId('rcp-replace');
    const hintId = ui.useId('rcp-replace-hint');
    const rowId = ui.useId('rcp-replace-row');
    useInlineFocus(open, inputRef, rowId, false);
    const key = draft.trim();
    const valid = KEY_RE.test(key);
    const ask = (e) => {
      if (e) e.preventDefault();
      if (!valid) return;
      setMsg(null);
      store.openConfirm({
        title: `Bark-Key von „${entry.label}“ ersetzen?`,
        text: 'Name, Stumm-Zustand, Lautstärke, Kurzbefehl-Links und Telefon bleiben erhalten. Der alte Key bekommt danach keine Alarme mehr.',
        confirmLabel: 'Ja, Key ersetzen',
        hold: false,
        onConfirm: () => store.keyReplace(Number(entry.id), key).then((res) => {
          show(res);
          if (res && res.ok && alive.current) {
            setOpen(false);
            setDraft('');
          }
          return res;
        })
      });
    };
    if (!open) {
      return html`<${ui.Row} id=${rowId} class="rcp-replace" label="Key ersetzen" icon="restart" iconTone="neutral"
        onClick=${() => { setMsg(null); setOpen(true); }}>
        <${ui.Msg} msg=${msg} class="rcp-msg-replace"/>
      </${ui.Row}>`;
    }
    return html`<li class="row row-block rcp-edit">
      <form class="rcp-edit-form" onSubmit=${ask}>
        <label class="rcp-field-label" for=${fieldId}>Neuer Bark-Key</label>
        <input id=${fieldId} ref=${inputRef} class="input mono rcp-replace-input" autocomplete="off" autocapitalize="none"
          spellcheck="false" maxlength="64" aria-describedby=${hintId} value=${draft} onInput=${(e) => setDraft(e.currentTarget.value)}/>
        <${KeyHint} id=${hintId} value=${key}/>
        <div class="btn-row rcp-edit-btns">
          <button type="button" class="btn" onClick=${() => { setOpen(false); setDraft(''); setMsg(null); }}>Abbrechen</button>
          <button type="submit" class="btn rcp-replace-go" disabled=${!valid}>Ersetzen…</button>
        </div>
      </form>
      <${ui.Msg} msg=${msg} class="rcp-msg-replace"/>
    </li>`;
  }

  // ---- Löschen (rot, mit Rückfrage; letzter/aktiver Demo-Empfänger: Server) --
  function DeleteRow({ entry, onDeleted }) {
    const [msg, setMsg] = useState(null);
    const show = useShow(setMsg);
    const ask = () => {
      setMsg(null);
      store.openConfirm({
        title: 'Empfänger löschen?',
        text: `Empfänger "${entry.label}" wirklich löschen? Er bekommt dann KEINE Alarme mehr!`,
        confirmLabel: 'Ja, löschen',
        hold: false,
        onConfirm: () => store.keyDelete(Number(entry.id)).then((res) => {
          if (res && res.ok) onDeleted(res);
          else show(res);
          return res;
        })
      });
    };
    return html`<${ui.Row} class="rcp-delete" label="Empfänger löschen" tone="crit" icon="trash" iconTone="crit" chevron=${false} onClick=${ask}>
      <${ui.Msg} msg=${msg} class="rcp-msg-delete"/>
    </${ui.Row}>`;
  }

  // ---- Hinweis zum Key-Format beim Tippen --------------------------------------
  function KeyHint({ id, value }) {
    const v = String(value || '');
    let tone = 'info';
    let text = KEY_HINT;
    if (v && KEY_RE.test(v)) {
      tone = 'ok';
      text = '✓ Format passt.';
    } else if (v) {
      tone = 'warn';
      text = `! ${KEY_HINT}`;
    }
    return html`<p id=${id} class=${cx('rcp-key-hint', `is-${tone}`)}>${text}</p>`;
  }

  // ---- Person (Sheet bzw. Panel) -------------------------------------------------
  function Person({ entry, demo, readOnly, onDeleted }) {
    const volRef = useRef(null);   // zuletzt abgeschickte Stumm-Lautstärke (siehe questionVolume)
    const v = L.recipientView(entry, demo, 0);
    const lines = stateLines(entry, v);
    let demoNote = '';
    if (v.isDemoTarget) demoNote = 'Demo-Empfänger: Im Demo-Modus bekommt nur diese Person die Alarme.';
    else if (v.paused) demoNote = `Pausiert: Im Demo-Modus bekommt gerade nur ${(demo && demo.label) || 'der Test-Empfänger'} die Alarme.`;
    const vol = volOf(entry);
    return html`<div class="rcp-person" data-id=${entry.id}>
      <p class="rcp-key mono">${entry.key_masked}</p>
      <div class="rcp-state">
        <p class=${cx('rcp-state-main', lines.warn && 'is-warn')}>${lines.warn ? html`<span aria-hidden="true">! </span>` : null}${lines.main}</p>
        ${lines.vol ? html`<p class="rcp-state-vol">${lines.vol}</p>` : null}
      </div>
      ${demoNote ? html`<p class="rcp-person-demo">${demoNote}</p>` : null}
      ${readOnly ? html`<${ui.Group} class="rcp-facts">
          <${ui.Row} label="Zustand" right=${html`<${ui.Pill} tone=${v.pill.tone}>${v.pill.text}</${ui.Pill}>`}/>
          <${ui.Row} label="Stumm-Lautstärke" value=${vol === 0 ? '0 (lautlos)' : `${vol} von 10`}/>
        </${ui.Group}>
        <p class="note">Ändern kann das nur ein Admin.</p>`
      : html`<${ModeControl} entry=${entry} state=${v.state} volRef=${volRef}/>
        <${VolumeControl} entry=${entry} state=${v.state} overdue=${v.overdue} volRef=${volRef}/>
        <${ui.Group} class="rcp-contact">
          <${PhoneRows} entry=${entry}/>
          <${LinkRows} entry=${entry}/>
        </${ui.Group}>
        <${ui.Group} class="rcp-actions">
          <${TestRow} entry=${entry}/>
          <${RenameRow} entry=${entry}/>
          <${ReplaceRow} entry=${entry}/>
          <${DeleteRow} entry=${entry} onDeleted=${onDeleted}/>
        </${ui.Group}>`}
    </div>`;
  }

  // ---- Hinzufügen ------------------------------------------------------------------
  // Name, Bark-Key (Prüfung beim Tippen), optional Telefon. Nach Erfolg:
  // „Test-Push senden“ (dieselbe Rückfrage wie in der Person).
  function AddForm({ count, onClose }) {
    const [full] = useState(() => count >= MAX_KEYS);
    const busy = store.useStore((s) => s.busyCount > 0);
    const testInFlight = store.useStore((s) => s.keyTestInFlight);
    const [name, setName] = useState('');
    const [key, setKey] = useState('');
    const [phone, setPhone] = useState('');
    const [msg, setMsg] = useState(null);
    const [testMsg, setTestMsg] = useState(null);
    const [added, setAdded] = useState(null);   // {id, name}
    const alive = useAlive();
    const showTest = useShow(setTestMsg);
    const testRef = useRef(null);
    const hintId = ui.useId('rcp-add-key-hint');
    useEffect(() => { if (added && testRef.current) testRef.current.focus(); }, [added]);

    if (full) {
      return html`<p class="rcp-add-full">Die Liste ist voll – höchstens ${MAX_KEYS} Empfänger. Bitte zuerst jemanden löschen.</p>`;
    }
    const k = key.trim();
    const valid = name.trim().length > 0 && KEY_RE.test(k);
    const submit = async (e) => {
      e.preventDefault();
      if (!valid || busy) return;
      setMsg(null);
      const res = await store.keyAdd(name, k, phone);
      if (!alive.current) return;
      setMsg(res);
      if (res.ok) setAdded({ id: res.id, name: name.trim() });
    };
    const again = () => {
      setName('');
      setKey('');
      setPhone('');
      setMsg(null);
      setTestMsg(null);
      setAdded(null);
    };
    if (added) {
      return html`<div class="rcp-add-done-box">
        <${ui.Msg} msg=${msg} class="rcp-msg-add"/>
        ${added.id ? html`<p class="rcp-add-offer">Jetzt prüfen, ob der Key stimmt und der Alarmton auf dem iPhone ankommt:</p>
          <button type="button" ref=${testRef} class="btn btn-block rcp-add-test" disabled=${testInFlight}
            onClick=${() => { setTestMsg(null); confirmTestPush(added.id, added.name, showTest); }}>
            <${ui.Icon} name="test"/><span>Test-Push an ${added.name} senden</span></button>
          <${ui.Msg} msg=${testMsg} class="rcp-msg-addtest"/>` : null}
        <div class="btn-row rcp-add-btns">
          <button type="button" class="btn rcp-add-again" onClick=${again}>Weiteren hinzufügen</button>
          <button type="button" class="btn rcp-add-done" onClick=${onClose}>Fertig</button>
        </div>
      </div>`;
    }
    return html`<form class="rcp-add-form" onSubmit=${submit} novalidate>
      <div class="rcp-form-field">
        <label class="rcp-field-label" for="rcp-add-name">Name</label>
        <input id="rcp-add-name" class="input" maxlength="40" required autocomplete="off" placeholder="z. B. Josh"
          data-autofocus value=${name} onInput=${(e) => setName(e.currentTarget.value)}/>
      </div>
      <div class="rcp-form-field">
        <label class="rcp-field-label" for="rcp-add-key">Bark-Key</label>
        <input id="rcp-add-key" class="input mono" required pattern="[A-Za-z0-9_\\-]{5,64}" maxlength="64" title=${KEY_HINT}
          autocomplete="off" autocapitalize="none" spellcheck="false" aria-describedby=${hintId}
          value=${key} onInput=${(e) => setKey(e.currentTarget.value)}/>
        <${KeyHint} id=${hintId} value=${k}/>
      </div>
      <div class="rcp-form-field">
        <label class="rcp-field-label" for="rcp-add-phone">Telefon (optional)</label>
        <input id="rcp-add-phone" class="input" type="tel" inputmode="tel" autocomplete="tel" maxlength="30"
          value=${phone} onInput=${(e) => setPhone(e.currentTarget.value)}/>
        <p class="rcp-field-hint">Nur für „Anrufen“ hier im Dashboard – geht nie an die Box oder an Bark.</p>
      </div>
      <button type="submit" class="btn btn-block rcp-add-submit" disabled=${!valid || busy}>Hinzufügen</button>
      <${ui.Msg} msg=${msg} class="rcp-msg-add"/>
    </form>`;
  }

  // ---- „Mehr erfahren“: Stumm, Dauerhaft stumm, Rückschaltung ------------------
  function Explain({ readOnly, muteMax }) {
    return html`<div class="rcp-explain">
      <p><strong>Stumm (Arbeitsmodus):</strong> Alarme kommen weiterhin als Critical Alert an – nur mit der
        eingestellten <strong>Stumm-Lautstärke</strong> (0${NBSP}=${NBSP}lautlos). ${muteMax > 0
          ? `Nach ${durationLabel(muteMax)} schaltet das NAS automatisch wieder laut.`
          : 'Eine automatische Rückschaltung ist abgeschaltet (Einstellungen → „Automatisch wieder laut nach“).'}</p>
      <p><strong>Dauerhaft stumm</strong> schaltet genau diese automatische Rückschaltung für einen Empfänger ab –
        für längere Abwesenheiten (Schichtdienst, Krankheit, Urlaub). Die Stummschaltung gilt dann, bis sie jemand
        von Hand beendet. Der Alarm bleibt auch dann <strong>sichtbar</strong> auf dem Sperrbildschirm – niemand fällt
        aus der Alarmierung heraus. Der Wächter erinnert regelmäßig daran, wer dauerhaft stumm ist.</p>
      ${readOnly ? null : html`<p>In der Person stehen außerdem die persönlichen <strong>Kurzbefehl-Links</strong> (für
        iPhone-Kurzbefehle, z.${NBSP}B. automatisch beim Betreten oder Verlassen der Arbeit – der Laut-Link
        beendet auch „dauerhaft stumm“) und der <strong>Test-Push</strong>: eine normale, nicht kritische Mitteilung
        mit dem Alarmton, um einen neuen Key und den Ton auf dem iPhone zu prüfen.</p>`}
      <p>Die Liste gilt für <strong>beide</strong> Alarmwege (Box und Dashboard). Änderungen übernimmt die Box nach
        wenigen Sekunden.</p>
    </div>`;
  }

  // ---- Zurück zur Liste ---------------------------------------------------------
  // Kam die Person aus der Liste (Antippen), geht Schließen einen Schritt
  // zurück - dann schließt auch die Zurück-Geste das Sheet, und es bleibt kein
  // Eintrag im Verlauf. Direkt geöffnet (Link, Neuladen): Adresse ersetzen.
  function useLeave(id) {
    const viaList = useRef(false);
    const prev = useRef(id);
    const leaving = useRef(null);
    useEffect(() => {
      if (prev.current === null && id !== null) viaList.current = true;
      if (id === null) viaList.current = false;
      prev.current = id;
      leaving.current = null;
    }, [id]);
    return () => {
      if (id === null || leaving.current === id) return;
      leaving.current = id;
      if (viaList.current && root.history.length > 1) root.history.back();
      else root.location.replace('#/empfaenger');
    };
  }

  // ---- Seite ------------------------------------------------------------------------
  FW.views.Recipients = function Recipients({ id, wide }) {
    const readOnly = store.useStore((s) => s.readOnly);
    const keys = store.useStore((s) => s.keys);
    const demo = store.useStore((s) => s.demo);
    const metaMuteMax = store.useStore((s) => s.muteMax);
    const [adding, setAdding] = useState(false);
    const [explain, setExplain] = useState(false);
    const [listMsg, setListMsg] = useState(null);
    const routeId = id === null || id === undefined || id === '' ? null : String(id);
    const leave = useLeave(routeId);
    const entries = keys && Array.isArray(keys.keys) ? keys.keys : null;
    const selId = routeId !== null && /^\d+$/.test(routeId) ? Number(routeId) : null;
    const selected = entries && selId !== null ? entries.find((k) => Number(k.id) === selId) || null : null;
    const demoOn = !!(demo && demo.enabled);
    const muteMax = keys && Number.isFinite(Number(keys.mute_max_seconds)) ? Number(keys.mute_max_seconds) : metaMuteMax;

    // Unbekannte bzw. inzwischen gelöschte Person: zurück zur Liste, sobald
    // die Liste geladen ist.
    useEffect(() => {
      if (entries && routeId !== null && !selected) leave();
    }, [entries, routeId, selected]);

    const onDeleted = (res) => {
      setListMsg(res);
      leave();
    };
    const openAdd = () => {
      setListMsg(null);
      setAdding(true);
    };

    const actions = readOnly ? null : (wide
      ? html`<button type="button" class="btn btn-small rcp-add" onClick=${openAdd}><${ui.Icon} name="plus"/><span>Hinzufügen</span></button>`
      : html`<${ui.IconButton} icon="plus" label="Empfänger hinzufügen" class="rcp-add" onClick=${openAdd}/>`);
    const person = selected
      ? html`<${Person} key=${selected.id} entry=${selected} demo=${demo} readOnly=${readOnly} onDeleted=${onDeleted}/>`
      : null;

    return html`<${ui.Page} title="Empfänger" actions=${actions} class="rcp">
      <${SyncLine} count=${entries ? entries.length : null}/>
      ${demoOn ? html`<p class="rcp-demo-note"><span aria-hidden="true">! </span>Demo-Modus: Alarmiert wird gerade nur <strong>${
        demo.label || 'der Test-Empfänger'}</strong> – alle anderen pausieren.</p>` : null}
      <div class=${cx('rcp-split', wide && 'is-wide')}>
        <div class="rcp-main">
          <${RecipientList} entries=${entries} selId=${selId} demo=${demo}/>
          <${ui.Msg} msg=${listMsg} class="rcp-msg-list"/>
          <div class="rcp-foot">
            <p>Stumm = Alarm kommt weiter als Critical Alert, nur mit der eingestellten Lautstärke.</p>
            <button type="button" class="link-btn rcp-more" onClick=${() => setExplain(true)}>Mehr erfahren</button>
          </div>
        </div>
        ${wide ? html`<${ui.Sheet} open=${true} variant="panel" class="rcp-panel" title=${selected ? selected.label : undefined}>
          ${person || html`<p class="rcp-panel-empty">${readOnly
            ? 'Person in der Liste anklicken, um die Einzelheiten zu sehen.'
            : 'Person in der Liste anklicken – hier stehen dann Laut/Stumm, Stumm-Lautstärke, Kurzbefehl-Links und alles Weitere.'}</p>`}
        </${ui.Sheet}>` : null}
      </div>
      ${wide ? null : html`<${ui.Sheet} open=${!!selected} variant="sheet" class="rcp-detail" title=${selected ? selected.label : ''}
        onClose=${leave}>${person}</${ui.Sheet}>`}
      <${ui.Sheet} open=${adding} variant="sheet" class="rcp-add-sheet" title="Empfänger hinzufügen" onClose=${() => setAdding(false)}>
        <${AddForm} count=${entries ? entries.length : 0} onClose=${() => setAdding(false)}/>
      </${ui.Sheet}>
      <${ui.Sheet} open=${explain} variant="sheet" class="rcp-explain-sheet" title="Stumm und Dauerhaft stumm" onClose=${() => setExplain(false)}>
        <${Explain} readOnly=${readOnly} muteMax=${muteMax}/>
      </${ui.Sheet}>
    </${ui.Page}>`;
  };
})(typeof window !== 'undefined' ? window : globalThis);
