(function (root) {
  'use strict';
  const FW = root.FW = root.FW || {};
  const { html, useState, useEffect, useMemo, useRef } = root.htmPreact;
  const store = FW.store;
  const ui = FW.ui;
  const L = FW.logic;
  const cx = ui.cx;
  FW.views = FW.views || {};

  // ==== Übersicht (Spec §6.2) ===============================================
  // Von oben: Status-Karte, Hinweise, Prüfliste, „Auslösen“ (Knöpfe,
  // Befehlszeile, Ergebnis-Karte). Ist das Urteil rot, rückt „Auslösen“
  // direkt unter die Status-Karte - der Alarm-Knopf muss dann auch auf einem
  // kleinen iPhone ohne Scrollen sichtbar sein. PC: rechte Spalte mit den
  // letzten Ereignissen und den Stummen.
  // Regeln (Frist, Modus-Prüfung, Sperren, Ergebnis) liegen im Store, Texte
  // in FW.logic - hier steht nur die Darstellung.

  // derive() baut jede Sekunde neue Objekte. Ausschnitte deshalb als
  // JSON-Text auswählen (ändert sich nur bei echter Änderung) und hier wieder
  // auspacken - so zeichnet z.B. die Status-Karte nicht jede Sekunde neu und
  // ihre Live-Region meldet nur echte Wechsel.
  function useDerivedJson(pick) {
    const sig = ui.useDerived((d, s) => JSON.stringify(pick(d, s) ?? null));
    return useMemo(() => JSON.parse(sig), [sig]);
  }

  // Rückmeldungen erst setzen, wenn die Ansicht noch steht (die Aktion kann
  // länger dauern als der Besuch der Seite).
  function useAlive() {
    const ref = useRef(true);
    useEffect(() => () => { ref.current = false; }, []);
    return ref;
  }

  const keyListOf = (s) => (s.keys && Array.isArray(s.keys.keys) ? s.keys.keys : null);

  // ---- Status-Karte ----------------------------------------------------------
  // Kreis mit Symbol (Farbe nie allein): ✓ in Ordnung, ! Aufmerksamkeit,
  // ✕ offline/kaputt. „Alarm läuft“ pulsiert ruhig.
  function lookOf(v) {
    if (v.key === 'loading') return { cls: 'is-neutral', icon: 'clock' };
    if (v.key === 'auth') return { cls: 'is-neutral', icon: 'bang' };
    if (v.tone === 'ok') return { cls: 'is-ok', icon: 'check' };
    if (v.tone === 'alarm') return { cls: 'is-alarm', icon: 'bang' };
    if (v.tone === 'crit') return { cls: 'is-crit', icon: v.key === 'standing' ? 'bang' : 'x' };
    return { cls: 'is-warn', icon: 'bang' };
  }
  // „Alles bereit“ liefert die Logik ohne Satz - die Karte sagt trotzdem,
  // was das heißt (ohne mehr zu versprechen, als geprüft ist).
  const OK_SUB = 'Die Box meldet sich, es ist keine Störung bekannt.';

  // Zeile unter dem Urteil: „Status vor N s“ bzw. bei einer NAS-Störung, was
  // fehlt und wie alt der angezeigte Stand ist (läuft im Sekundentakt mit -
  // deshalb eine eigene kleine Komponente).
  function statusLineOf(s) {
    const v = store.derive(s).verdict;
    if (v.key === 'auth') return '';
    if (v.key === 'nas') {
      const age = s.lastOkAt ? L.fmtAgeShort((s.nowMs - s.lastOkAt) / 1000) : null;
      return `${v.detail || 'Keine aktuelle Antwort vom NAS'} – angezeigte Werte können veraltet sein${age ? ` (letzter erfolgreicher Abruf ${age})` : ''}.`;
    }
    return ui.statusAgeText(s);
  }
  function StatusLine() {
    const text = store.useStore(statusLineOf);
    return text ? html`<p class="status-age">${text}</p>` : null;
  }

  function StatusCard() {
    const v = useDerivedJson((d) => ({ key: d.verdict.key, tone: d.verdict.tone, title: d.verdict.title, sub: d.verdict.sub }));
    const titleId = ui.useId('status-title');
    const look = lookOf(v);
    const sub = v.sub || (v.key === 'ok' ? OK_SUB : '');
    return html`<section class=${cx('status-card', look.cls)} aria-labelledby=${titleId}>
      <span class="status-icon" aria-hidden="true"><${ui.Icon} name=${look.icon}/></span>
      <div class="status-body">
        <div class="status-live" role="status" aria-live="polite" aria-atomic="true">
          <h2 class="status-title" id=${titleId}>${v.title}</h2>
          ${sub ? html`<p class="status-sub">${sub}</p>` : null}
        </div>
        <${StatusLine}/>
      </div>
    </section>`;
  }

  // ---- Hinweise und Prüfliste ------------------------------------------------
  // Rote Hinweise zuerst, sonst in der Reihenfolge der Logik.
  function Hints() {
    const hints = useDerivedJson((d) => d.hints);
    if (!hints.length) return null;
    const sorted = hints.map((h, i) => ({ h, i }))
      .sort((a, b) => ((b.h.tone === 'crit') - (a.h.tone === 'crit')) || a.i - b.i)
      .map((x) => x.h);
    return html`<${ui.Group} header="Hinweise" class="ov-hints">
      ${sorted.map((h, i) => html`<${ui.Row} key=${`${i}:${h.text}`} label=${h.text} tone=${h.tone}
        icon="alert" iconTone=${h.tone === 'crit' ? 'crit' : 'warn'}/>`)}
    </${ui.Group}>`;
  }

  function Checklist() {
    const rows = useDerivedJson((d) => d.overviewRows);
    return html`<${ui.Group} class="ov-checks">
      ${rows.map((r) => html`<${ui.Row} key=${r.id} class=${`chk-${r.id}`} label=${r.label} value=${r.value}
        tone=${r.tone} href=${r.href || undefined}/>`)}
    </${ui.Group}>`;
  }

  // ---- Befehlszeile (TEST, DEMO-ALARM über die Box) ---------------------------
  // Zustand und Quittung wie bisher (logic.commandLine); „Abbrechen“, solange
  // der Befehl noch auf die Box wartet (nur Admin).
  function CommandLine({ canCancel, onResult }) {
    const cmd = useDerivedJson((d) => d.commandLine);
    const busy = store.useStore((s) => s.busyCount > 0);
    if (!cmd) return null;
    const cancel = () => {
      store.cancelCommand().then((res) => { if (onResult) onResult(res); });
    };
    return html`<div class=${cx('cmd-line', cmd.tone && `is-${cmd.tone}`)}>
      <span class="cmd-icon" aria-hidden="true"><${ui.Icon} name="box"/></span>
      <p class="cmd-text" aria-live="polite">${cmd.tone === 'warn' ? html`<span class="cmd-sym" aria-hidden="true">! </span>` : null}${cmd.text}</p>
      ${canCancel && cmd.canCancel
        ? html`<button type="button" class="link-btn cmd-cancel" disabled=${busy} onClick=${cancel}>Abbrechen</button>`
        : null}
    </div>`;
  }

  // ---- Ergebnis-Karte nach Alarm/Entwarnung (Spec §7.4) -----------------------
  // Unter Stress zählt EINE Frage: Wer wurde NICHT erreicht? Die stehen
  // zuerst - aus unreached.entries (der zusammengeführten Liste), nie nur aus
  // result.failed (das sind nur die frischen Fehlschläge dieser Runde).
  function resultLook(r) {
    if (!r) return { tone: 'warn', title: '' };
    if (r.phase === 'sending') return { tone: 'busy', title: '' };
    if (r.phase === 'done') {
      const ok = r.tone === 'ok';
      if (r.total > 0) return { tone: ok ? 'ok' : 'fail', title: `${r.reached.length} von ${r.total} erreicht` };
      return { tone: ok ? 'ok' : 'fail', title: ok ? 'Gesendet' : 'Nichts gesendet' };
    }
    // Sicher NICHT rausgegangen (401/403/409/4xx) - das steht groß oben.
    if (r.tone === 'fail') return { tone: 'fail', title: 'NICHT gesendet' };
    // Hinweis (Antwort unklar, Modus gewechselt, nach dem Neuladen gemerkt):
    // Der Satz selbst ist die Aussage.
    return { tone: 'warn', title: '' };
  }

  function unreachedLabel(r, u) {
    if (r && r.phase === 'done' && r.fresh) {
      return r.resend
        ? 'Weiterhin NICHT erreicht – bitte anders verständigen (Telefon):'
        : 'NICHT erreicht – bitte anders verständigen (Telefon):';
    }
    return `Zuletzt NICHT erreicht (${u.what}, ${L.fmtClockMs(u.at)} Uhr) – bitte anders verständigen (Telefon):`;
  }

  function ResultCard() {
    const r = store.useStore((s) => s.result);
    const u = store.useStore((s) => s.unreached);
    const keys = store.useStore((s) => s.keys);
    const readOnly = store.useStore((s) => s.readOnly);
    const inFlight = store.useStore((s) => s.alarmInFlight);
    const retry = ui.useDerived((d) => (d.retry ? d.retry.label : ''));
    const titleId = ui.useId('result-title');
    const look = resultLook(r);
    const ref = useRef(null);
    const prevPhase = useRef(null);

    // Ein neues Ergebnis (nicht jede Sekunde) einmal ansagen. Beim Versand
    // (Beginn und Ende) die Karte ins Bild holen: Am iPhone stünde sie sonst
    // unter dem Knopf außer Sicht. Hinweise aus Abfragen oder nach dem
    // Neuladen verschieben die Seite nicht.
    useEffect(() => {
      const before = prevPhase.current;
      prevPhase.current = r ? r.phase : null;
      if (!r) return;
      if (r.head) ui.announce(look.title ? `${look.title}. ${r.head}` : r.head);
      const node = ref.current;
      if ((r.phase === 'sending' || before === 'sending') && node && typeof node.scrollIntoView === 'function') {
        const calm = !!(root.matchMedia && root.matchMedia('(prefers-reduced-motion: reduce)').matches);
        node.scrollIntoView({ block: 'nearest', behavior: calm ? 'auto' : 'smooth' });
      }
    }, [r]);

    if (!r && !u) return null;
    // Telefon nur aus der Admin-Liste (die Lese-Rolle bekommt keine Nummern).
    const phoneOf = (id) => {
      if (readOnly || !keys || !Array.isArray(keys.keys) || !(id > 0)) return '';
      const k = keys.keys.find((x) => Number(x.id) === Number(id));
      return k && k.phone ? String(k.phone) : '';
    };
    const sending = !!r && r.phase === 'sending';
    return html`<section ref=${ref} class=${cx('result-card', `is-${look.tone}`)} aria-labelledby=${look.title ? titleId : undefined}
        aria-label=${look.title ? undefined : 'Ergebnis'}>
      ${look.title ? html`<h3 class="result-title" id=${titleId}>${look.title}</h3>` : null}
      ${r && r.head ? html`<p class=${cx('result-head', !look.title && 'is-lead')}>${
        look.tone === 'warn' && !look.title ? html`<span class="result-sym" aria-hidden="true">! </span>` : null}${r.head}</p>` : null}
      ${u ? html`<p class="result-label">${unreachedLabel(r, u)}</p>
        <ul class="result-list" role="list">${u.entries.map((e, i) => {
          const phone = phoneOf(e.id);
          const tel = phone.replace(/[^0-9+]/g, '');
          return html`<li key=${`${e.id}:${i}`} class="result-entry">
            <span class="result-x" aria-hidden="true"><${ui.Icon} name="x"/></span>
            <span class="result-who"><span class="result-name">${e.name}</span><span class="result-reason">${e.reason}</span></span>
            ${tel ? html`<a class="call-btn" href=${`tel:${tel}`} title=${phone} aria-label=${`${e.name} anrufen (${phone})`}><${ui.Icon} name="phone"/><span>Anrufen</span></a>` : null}
          </li>`;
        })}</ul>` : null}
      ${retry && !readOnly ? html`<button type="button" class="btn btn-danger btn-block result-retry" disabled=${inFlight}
        onClick=${() => { store.confirmResend(); }}>${retry}</button>` : null}
      ${r && r.phase === 'done' && r.reached.length ? html`<p class="result-reached"><span class="result-check" aria-hidden="true">✓ </span>${
        r.resend ? 'Jetzt erreicht' : 'Erreicht'}: ${r.reached.join(', ')}</p>` : null}
      ${sending ? null : html`<div class="result-foot">
        ${look.tone === 'warn' && r && !r.offerRetry ? html`<a class="link-btn" href="#/verlauf?filter=alarm">Im Verlauf prüfen</a>` : null}
        <button type="button" class="link-btn result-dismiss" onClick=${() => store.clearResult()}>Ausblenden</button>
      </div>`}
    </section>`;
  }

  // ---- Auslösen ---------------------------------------------------------------
  // Alarm, Entwarnung und die Nachsendung sperrt NUR ein laufender Versand
  // (alarmInFlight) - ein langsamer Test-Push darf den Alarm nicht sperren.
  // TEST und der Demo-Alarm über die Box teilen sich die Befehlsschlange und
  // hängen deshalb auch am Zähler laufender Aktionen.
  function AdminActions() {
    const demoOn = store.useStore((s) => !!(s.demo && s.demo.enabled));
    const demoName = store.useStore((s) => (s.demo && s.demo.label) || 'Test-Empfänger');
    const inFlight = store.useStore((s) => s.alarmInFlight);
    const busy = store.useStore((s) => s.busyCount > 0);
    const [msg, setMsg] = useState(null);
    const alive = useAlive();
    const headId = ui.useId('actions-head');
    const show = (res) => {
      if (alive.current && res) setMsg(res);
      return res;
    };

    // Frage und expect_demo aus EINER Momentaufnahme (im Store).
    const alarm = () => { setMsg(null); store.confirmAlarm('alarm'); };
    const falseAlarm = () => { setMsg(null); store.confirmAlarm('false_alarm'); };
    const test = () => { setMsg(null); store.enqueueTest().then(show); };
    // Wie store.confirmAlarm('box_alarm') - aber mit Rückmeldung an dieser
    // Stelle: Lehnt das NAS den Befehl ab (z.B. Box hat die Demo-Liste noch
    // nicht übernommen), soll das hier stehen und nicht verloren gehen.
    const boxAlarm = () => {
      const s = store.getState();
      if (s.alarmInFlight || !(s.demo && s.demo.enabled)) return;
      const expect = L.expectDemoOf(s.demo);
      const q = L.alarmQuestion('box_alarm', s.demo, keyListOf(s));
      setMsg(null);
      store.openConfirm({
        title: q.title, text: q.text, confirmLabel: 'Ja, Demo-Alarm auslösen', hold: true, expectDemo: expect,
        onConfirm: () => store.enqueueBoxAlarm(expect).then(show)
      });
    };

    return html`<section class="group ov-actions" aria-labelledby=${headId}>
      <h2 class="group-head" id=${headId}><span>Auslösen</span></h2>
      <button type="button" class="btn btn-alarm btn-block ov-alarm" disabled=${inFlight} onClick=${alarm}>${
        demoOn ? `Demo-Alarm an ${demoName}` : 'Alarm an alle senden'}</button>
      <div class="btn-row ov-row2">
        <button type="button" class="btn btn-tinted ov-false-alarm" disabled=${inFlight} onClick=${falseAlarm}>Entwarnung</button>
        <button type="button" class="btn btn-tinted ov-test" disabled=${busy} onClick=${test}>Test</button>
      </div>
      ${demoOn ? html`<button type="button" class="btn btn-tinted btn-block ov-box-alarm" disabled=${busy || inFlight}
        onClick=${boxAlarm}><${ui.Icon} name="demo"/><span>Demo-Alarm über die Box</span></button>` : null}
      <${CommandLine} canCancel=${true} onResult=${show}/>
      <${ui.Msg} msg=${msg}/>
      <${ResultCard}/>
      <p class="group-foot">Alarm und Entwarnung gehen direkt vom NAS an die Empfänger – auch wenn die Box offline ist. Test läuft über die Box (leise Meldung an den Status-Empfänger).</p>
    </section>`;
  }

  // Lese-Rolle: keine Knöpfe, stattdessen wen man verständigt.
  function ReadonlyActions() {
    const headId = ui.useId('actions-head');
    return html`<section class="group ov-actions" aria-labelledby=${headId}>
      <h2 class="group-head" id=${headId}><span>Auslösen</span></h2>
      <p class="ov-readonly">Mit diesem Zugang lässt sich nichts auslösen. Muss manuell alarmiert werden, bitte einen Admin verständigen – er kann direkt über das NAS alarmieren.</p>
      <${CommandLine} canCancel=${false}/>
      <${ResultCard}/>
    </section>`;
  }

  function Actions() {
    const readOnly = store.useStore((s) => s.readOnly);
    return readOnly ? html`<${ReadonlyActions}/>` : html`<${AdminActions}/>`;
  }

  // ---- Rechte Spalte (PC) -------------------------------------------------------
  const DOT = { alarm: 'crit', warn: 'warn', ok: 'ok' };

  function Recent() {
    const recent = store.useStore((s) => s.recent);
    const entries = recent.entries;
    const now = recent.serverTime || Math.floor(Date.now() / 1000);
    return html`<${ui.Group} header="Letzte Ereignisse" class="ov-recent">
      ${entries === null ? html`<${ui.Item} class="ov-empty">Wird geladen…</${ui.Item}>` : null}
      ${entries && !entries.length ? html`<${ui.Item} class="ov-empty">${L.LOG_EMPTY.all}</${ui.Item}>` : null}
      ${(entries || []).slice(0, 5).map((e, i) => {
        const d = L.describeLogEntry(e);
        const day = L.dayKey(e.at) === L.dayKey(now) ? '' : L.fmtDayLabel(e.at, now);
        return html`<li key=${`${e.at}:${e.event}:${i}`} class="row ov-event"><div class="row-main">
          <span class=${cx('ov-dot', `dot-${DOT[d.tone] || 'neutral'}`)} aria-hidden="true"></span>
          <span class="row-text"><span class="row-label">${d.text}</span>${day ? html`<span class="row-sub">${day}</span>` : null}</span>
          <span class="row-value">${L.fmtClock(e.at)}</span>
        </div></li>`;
      })}
      <li class="row"><a class="row-main ov-more" href="#/verlauf">Ganzen Verlauf anzeigen</a></li>
    </${ui.Group}>`;
  }

  // Wer stumm ist, mit Lautstärke und bis wann (bzw. „dauerhaft“).
  function mutedText(k) {
    if (k.mute_overdue) return 'Rückschaltung fällig – nicht gespeichert';
    const vol = Number(k.mute_volume) || 0;
    const how = vol === 0 ? 'lautlos' : `Lautstärke ${vol}`;
    if (k.mute_permanent) return `dauerhaft · ${how}`;
    const until = Number(k.mute_until) || 0;
    return until ? `${how} · bis ~${L.fmtClock(until)}` : how;
  }

  function MutedList() {
    const keys = store.useStore((s) => s.keys);
    const corrupt = store.useStore((s) => s.keysCorrupt);
    const list = keys && Array.isArray(keys.keys) ? keys.keys.filter((k) => k.muted || k.mute_overdue) : null;
    let empty = '';
    if (corrupt) empty = 'Empfängerliste beschädigt – siehe „Empfänger“.';
    else if (!list) empty = 'Wird geladen…';
    else if (!list.length) empty = 'Niemand ist stumm geschaltet – alle bekommen den Alarm laut.';
    return html`<${ui.Group} header="Stumm geschaltet" class="ov-muted">
      ${empty ? html`<${ui.Item} class="ov-empty">${empty}</${ui.Item}>` : null}
      ${(list || []).map((k) => html`<${ui.Row} key=${k.id} label=${k.label} value=${mutedText(k)}
        tone=${k.mute_overdue ? 'warn' : ''} href=${`#/empfaenger/${k.id}`}/>`)}
    </${ui.Group}>`;
  }

  // ---- Seite --------------------------------------------------------------------
  // Feste keys: Rückt „Auslösen“ nach oben, verschiebt Preact den Block
  // (eine Rückmeldung darin bleibt stehen), statt ihn neu aufzubauen.
  FW.views.Overview = function Overview({ wide }) {
    const raise = ui.useDerived((d) => !!d.verdict.raiseActions);
    return html`<${ui.Page} title="Übersicht" class="ov">
      <div class=${cx('ov-cols', wide && 'cols')}>
        <div class="ov-main">
          ${[
            html`<${StatusCard} key="status"/>`,
            raise ? html`<${Actions} key="actions"/>` : null,
            html`<${Hints} key="hints"/>`,
            html`<${Checklist} key="checks"/>`,
            raise ? null : html`<${Actions} key="actions"/>`
          ]}
        </div>
        ${wide ? html`<div class="ov-side"><${Recent}/><${MutedList}/></div>` : null}
      </div>
    </${ui.Page}>`;
  };
})(typeof window !== 'undefined' ? window : globalThis);
