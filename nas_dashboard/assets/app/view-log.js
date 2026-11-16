(function (root) {
  'use strict';
  const FW = root.FW = root.FW || {};
  const { html, useEffect, useMemo } = root.htmPreact;
  const store = FW.store;
  const ui = FW.ui;
  const L = FW.logic;
  const cx = ui.cx;
  FW.views = FW.views || {};

  // ==== Verlauf (Spec §6.4) =================================================
  // Adresse #/verlauf bzw. #/verlauf?filter=alarm|keys|system. Oben der
  // Segment-Filter, darunter die Suche (nur über die GELADENEN Einträge), dann
  // je Tag ein Abschnitt, je Ereignis eine Zeile mit farbiger Symbol-Kachel.
  // Texte, Farbe (tone) und Symbol-Art kommen aus L.describeLogEntry; der
  // Store hält Filter, Umfang und Suchtext.

  const FILTER_OPTIONS = [
    { value: 'all', label: 'Alle' },
    { value: 'alarm', label: 'Alarme' },
    { value: 'keys', label: 'Empfänger' },
    { value: 'system', label: 'System' }
  ];

  // Art des Ereignisses (describeLogEntry.icon) -> Symbol aus FW.ui.
  const ICON_OF = {
    alarm: 'alert', clear: 'check', command: 'box', demo: 'demo', add: 'plus', remove: 'trash',
    edit: 'edit', mute: 'mute', unmute: 'unmute', test: 'test', settings: 'settings', box: 'box', watch: 'watch'
  };
  // Farbe der Kachel nach Ton: rot Alarm, orange Stumm/Warnung, grün
  // hinzugefügt/wieder da, sonst grau. Die Farbe ist nie das einzige Signal:
  // Symbol und Text sagen dasselbe.
  const TILE_OF = { alarm: 'crit', warn: 'warn', ok: 'ok' };

  function iconFor(entry, d) {
    if (entry.event === 'box_restart') return 'restart';
    return ICON_OF[d.icon] || 'list';
  }

  // Einträge samt Beschreibung und Tag. Nur neu berechnen, wenn sich die
  // geladenen Einträge oder der Suchtext ändern (nicht im Sekundentakt).
  function useSections(entries, query, now) {
    return useMemo(() => {
      if (!entries) return null;
      const sections = [];
      let shown = 0;
      entries.forEach((e, i) => {
        if (!L.logMatches(e, query)) return;
        const d = L.describeLogEntry(e);
        const day = L.dayKey(e.at);
        let sec = sections[sections.length - 1];
        if (!sec || sec.day !== day) {
          sec = { day, label: L.fmtDayLabel(e.at, now), rows: [] };
          sections.push(sec);
        }
        sec.rows.push({ key: `${e.at}:${e.event}:${e.id ?? ''}:${i}`, e, d });
        shown += 1;
      });
      return { sections, shown };
    }, [entries, query, now]);
  }

  function LogRow({ e, d }) {
    const tone = TILE_OF[d.tone] || 'neutral';
    return html`<li class="row log-row">
      <span class=${cx('tile', `tile-${tone}`)} aria-hidden="true"><${ui.Icon} name=${iconFor(e, d)}/></span>
      <span class="log-text">${d.text}</span>
      <time class="log-time" datetime=${new Date(e.at * 1000).toISOString()}>${L.fmtClock(e.at)}</time>
    </li>`;
  }

  function Search({ query }) {
    return html`<div class="log-searchbox">
      <${ui.Icon} name="search" class="log-search-icon"/>
      <input type="search" class="input log-search" value=${query} placeholder="Suchen" aria-label="Verlauf durchsuchen"
        autocomplete="off" autocapitalize="off" spellcheck="false" enterkeyhint="search"
        onInput=${(ev) => store.setLogQuery(ev.currentTarget.value)}/>
      ${query ? html`<button type="button" class="log-search-clear" aria-label="Suche leeren"
        onClick=${(ev) => { store.setLogQuery(''); const box = ev.currentTarget.parentNode.querySelector('.log-search'); if (box) box.focus(); }}><${ui.Icon} name="x"/></button>` : null}
    </div>`;
  }

  FW.views.Log = function Log({ query: routeQuery }) {
    const filter = store.useStore((s) => s.log.filter);
    const entries = store.useStore((s) => s.log.entries);
    const more = store.useStore((s) => s.log.more);
    const error = store.useStore((s) => s.log.error);
    const moreFailed = store.useStore((s) => s.log.moreFailed);
    const query = store.useStore((s) => s.log.query);
    const serverTime = store.useStore((s) => s.log.serverTime);
    const busy = store.useStore((s) => s.log.loadingMore);

    // Vorwahl aus der Adresse (nur wenn sie sich ändert; unbekannt = „Alle“).
    // Kommt die Adresse MIT Filter (z.B. „Letzter Alarm“ → Alarme), gilt eine
    // alte Suche nicht mehr - sie würde den gesuchten Alarm verstecken.
    const wanted = L.LOG_FILTERS.includes(routeQuery && routeQuery.filter) ? routeQuery.filter : 'all';
    const fromRoute = !!(routeQuery && routeQuery.filter);
    useEffect(() => { store.setLogFilter(wanted, { clearQuery: fromRoute }); }, [wanted, fromRoute]);

    // Filterwechsel per Hand: Adresse nachführen (ohne neuen Verlaufseintrag und
    // ohne Seitenwechsel), damit Neu-Laden und Zurück den Filter behalten.
    function pick(value) {
      store.setLogFilter(value);
      try {
        const hash = value === 'all' ? '#/verlauf' : `#/verlauf?filter=${value}`;
        root.history.replaceState(null, '', root.location.pathname + root.location.search + hash);
      } catch (err) { /* Adresse bleibt, wie sie ist */ }
    }

    const view = useSections(entries, query, serverTime || Math.floor(Date.now() / 1000));
    const q = query.trim();
    let empty = '';
    if (!entries) empty = error || 'Wird geladen…';
    else if (!entries.length) empty = L.LOG_EMPTY[filter] || L.LOG_EMPTY.all;
    else if (view && !view.shown) empty = `Keine Treffer für „${q}“ unter den geladenen Einträgen.${more ? ' „Ältere laden“ durchsucht mehr.' : ''}`;

    return html`<${ui.Page} title="Verlauf" class="log">
      <div class="log-filter"><${ui.Segmented} options=${FILTER_OPTIONS} value=${filter} onChange=${pick} label="Verlauf filtern"/></div>
      <${Search} query=${query}/>
      <p class="log-hint">durchsucht die geladenen Einträge</p>
      ${entries && error && !moreFailed ? html`<p class="log-empty is-error log-stale" role="alert">${error}</p>` : null}
      ${empty ? html`<p class=${cx('log-empty', error && !entries && 'is-error')} role=${error && !entries ? 'alert' : 'status'}>${empty}</p>` : null}
      ${view ? view.sections.map((sec) => html`<${ui.Group} key=${sec.day} header=${sec.label} class="log-day">
        ${sec.rows.map((r) => html`<${LogRow} key=${r.key} e=${r.e} d=${r.d}/>`)}
      </${ui.Group}>`) : null}
      ${entries && error && moreFailed ? html`<p class="log-empty is-error" role="alert">${error}</p>` : null}
      ${entries && more ? html`<button type="button" class="btn btn-block log-more" disabled=${busy}
        onClick=${() => store.loadMoreLog()}>${busy ? 'Wird geladen…' : 'Ältere laden'}</button>` : null}
    </${ui.Page}>`;
  };
})(typeof window !== 'undefined' ? window : globalThis);
