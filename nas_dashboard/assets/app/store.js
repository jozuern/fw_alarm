(function (root) {
  'use strict';
  const FW = root.FW = root.FW || {};
  const L = FW.logic;
  const api = FW.api;

  // ==== Zentraler Zustand des Dashboards ====================================
  // EIN Store für alles, was die Ansichten anzeigen: Status, Empfänger, Demo,
  // Verlauf, Meldungen, laufende Aktionen, offene Bestätigung, Ergebnis nach
  // Alarm/Entwarnung. Die Ansichten lesen über useStore() und lösen nur
  // Aktionen aus; die Regeln stehen hier bzw. in FW.logic (rein, getestet).
  // Portiert aus dem bisherigen app.js (refresh, handleAuthLoss, sendNasAlarm,
  // armInline, Polling) - Spec §7 ist der Vertrag.
  // Kein DOM-Zugriff außer: Sichtbarkeit des Tabs (env.hidden), Neuladen bei
  // abgelaufener Sitzung (env.reload) und - über FW.api.setCsrf - das
  // CSRF-Feld im Abmelde-Formular.

  // Abfrage-Intervalle (Spec §7.1); versteckt mit "Hinweis im Browser" nur
  // der Status, und nur jede Minute (Spec §8.6).
  const INTERVAL = Object.freeze({
    STATUS: 5000, KEYS: 30000, LOG: 30000, ALARM_INFO: 60000, NOTIFY: 60000, SETTINGS: 60000,
    TICK: 1000, BACKGROUND: 60000
  });
  // So lange darf die letzte erfolgreiche Status-Abfrage höchstens her sein,
  // bevor die Status-Karte auf "Status veraltet" wechselt.
  const STALE_AFTER_MS = 15000;
  const AUTH_RELOAD_MS = 1500;
  // Nach "Antwort unklar" den Verlauf nachladen: Er zeigt, ob der Versand
  // gestartet bzw. abgeschlossen wurde (nas_alarm_started / nas_alarm).
  const UNCLEAR_RECHECK_MS = [5000, 40000];
  // "NAS nicht erreichbar" meldet der Browser-Hinweis erst ab so vielen
  // Fehlschlägen in Folge (ein einzelner Aussetzer ist kein Ausfall).
  const NAS_ALERT_AFTER_FAILS = 3;
  const RECENT_LIMIT = 5;
  const UNREACHED_STORE = 'fwAlarm.lastUnreached';
  const BROWSER_ALERT_STORE = 'fwAlarm.browserAlert';
  const MODE_CHANGED_NOTE = 'Der Demo-/LIVE-Modus hat sich gerade geändert – Bestätigung zurückgesetzt. Bitte Lage prüfen und bei Bedarf neu auslösen.';
  const RESTORED_NOTE = 'Auf diesem Gerät gemerkt (vor dem Neuladen der Seite):';
  const BUSY_TEXT = 'Es läuft noch eine andere Aktion – bitte kurz warten und dann erneut versuchen.';

  // Austauschbar für Tests bzw. main.js.
  const env = {
    hidden: () => !!(root.document && root.document.hidden),
    reload: () => { root.location.reload(); }
  };

  function report(err) {
    if (root.console && root.console.error) root.console.error(err);
  }

  // ---- Zustand -------------------------------------------------------------
  function initialState(o) {
    const role = String(o.role || 'admin');
    return {
      role,
      readOnly: role !== 'admin',
      user: String(o.user || ''),
      publicBase: String(o.publicBase || '').trim(),
      muteMax: Number(o.muteMax) > 0 ? Number(o.muteMax) : 0,
      route: 'uebersicht',
      wide: false,
      authLost: false,
      authReloading: false,     // Neuladen zur Anmeldung ist geplant
      status: null,             // Rohwerte der Box (status.status)
      statusData: null,         // letzte erfolgreiche Status-Antwort
      statusError: null,        // Fehler der letzten Status-Abfrage (ApiError)
      stale: false,             // seit STALE_AFTER_MS keine erfolgreiche Antwort
      lastOkAt: 0,              // Browser-Zeit (ms) der letzten erfolgreichen Abfrage
      failStreak: 0,            // Fehlschläge der Status-Abfrage in Folge
      serverOffset: 0,          // Server-Zeit minus Browser-Zeit (s)
      demo: null,
      nasKeysVersion: null,
      demoSyncOk: null,         // Server: Box hat die Demo-Liste übernommen (null = älterer Server)
      demoSyncFor: null,        // für welchen Modus (expectDemoOf) demoSyncOk galt
      deviceHint: '',
      mismatchSince: 0,         // seit wann (ms) die Box eine andere Listen-Version meldet
      keys: null,               // letzte Antwort von keys_list
      keysCorrupt: '',          // Meldung: Liste samt Sicherung unlesbar
      keysError: '',            // Liste konnte noch nie geladen werden
      alarmInfo: undefined,     // undefined = noch nicht geladen, null = nichts im Verlauf
      log: { filter: 'all', limit: L.LOG_STEP, entries: null, more: false, error: '', query: '', serverTime: 0 },
      recent: { entries: null, serverTime: 0 },   // "Letzte Ereignisse" der breiten Übersicht
      notify: null,
      notifyError: '',
      settings: null,
      settingsError: '',
      busyCount: 0,
      alarmInFlight: false,
      keyTestInFlight: false,
      keyTestId: null,
      probeInFlight: false,
      probeType: null,
      unreached: null,
      result: null,
      confirm: null,
      browserAlert: false,
      nowMs: Date.now()
    };
  }

  let state = initialState({});
  const listeners = new Set();
  let mismatchPair = '';      // für welches Versionspaar "Box|NAS" mismatchSince gilt
  let lastAlarmMark = null;   // woran ein neuer Alarm erkannt wird

  function set(patch) {
    state = Object.assign({}, state, patch);
    Array.from(listeners).forEach((fn) => {
      try { fn(state); } catch (err) { report(err); }
    });
  }
  const getState = () => state;
  function subscribe(fn) {
    listeners.add(fn);
    return () => { listeners.delete(fn); };
  }
  const setLog = (patch) => set({ log: Object.assign({}, state.log, patch) });
  const sameJson = (a, b) => JSON.stringify(a) === JSON.stringify(b);
  // Unveränderte Daten behalten ihre Referenz: Ansichten (offene Sheets,
  // Zeilen) zeichnen dann bei einer Abfrage ohne Änderung nicht neu.
  const keep = (prev, next) => (prev !== null && prev !== undefined && sameJson(prev, next) ? prev : next);
  const outcome = (ok, text, kind) => ({ ok, text, kind });
  const busyUp = () => set({ busyCount: state.busyCount + 1 });
  const busyDown = () => set({ busyCount: Math.max(0, state.busyCount - 1) });
  const isAuth = (err) => err instanceof api.ApiError && err.kind === 'auth';
  const keyList = () => (state.keys && Array.isArray(state.keys.keys) ? state.keys.keys : null);

  // Preact-Hook: liest einen Ausschnitt und zeichnet nur neu, wenn sich
  // genau dieser Ausschnitt ändert (Object.is).
  function useStore(selector) {
    const hooks = root.htmPreact;
    const pick = typeof selector === 'function' ? selector : (s) => s;
    const [, force] = hooks.useState(0);
    const ref = hooks.useRef(null);
    const value = pick(state);
    ref.current = { pick, value };
    hooks.useEffect(() => {
      const check = () => {
        const cur = ref.current;
        const next = cur.pick(state);
        if (!Object.is(next, cur.value)) {
          cur.value = next;
          force((n) => n + 1);
        }
      };
      const off = subscribe(check);
      check();   // Änderung zwischen Zeichnen und Anmelden nicht verpassen
      return off;
    }, []);
    return value;
  }

  // ---- Ableitungen (rein, über FW.logic) -----------------------------------
  let memo = { from: null, out: null };
  function derive(s) {
    s = s || state;
    if (memo.from === s) return memo.out;
    const data = s.statusData;
    const status = s.status;
    const offline = !!(data && data.offline);
    const unsure = !!(s.stale || s.statusError);
    const responseTime = data ? Number(data.server_time) : 0;
    const serverTime = s.nowMs / 1000 + s.serverOffset;
    // seen_age_seconds ist null, wenn sich die Box noch NIE gemeldet hat
    // (Number(null) wäre 0 und hieße "offline seit 0 Min").
    const seenAge = data && data.seen_age_seconds != null ? Number(data.seen_age_seconds) : NaN;
    const watchdog = data ? L.watchdogState(data.watchdog, responseTime) : null;
    if (watchdog) watchdog.outside = L.outsideState(data.watchdog, responseTime);
    const keysSummary = s.keysCorrupt ? null : L.keysSummary(s.keys);
    const demoOn = !!(s.demo && s.demo.enabled);
    const mismatchMs = s.mismatchSince ? Math.max(0, s.nowMs - s.mismatchSince) : 0;
    const ctx = {
      status, offline, seenAge, watchdog,
      stale: s.stale, statusError: s.statusError,
      nasKeysVersion: s.nasKeysVersion,
      keysSummary, keysCorrupt: !!s.keysCorrupt,
      demo: s.demo, demoOn, deviceHint: s.deviceHint, mismatchMs,
      readOnly: s.readOnly, authLost: s.authLost,
      // Abgemeldet, aber NICHT neu geladen (ein Alarm lief): dann bleibt der
      // Satz "nach dem Lesen des Alarm-Ergebnisses neu laden" stehen - auch
      // nachdem der Versand fertig ist. Bei authLost zählt nur dieser Zweig.
      alarmInFlight: s.alarmInFlight || (s.authLost && !s.authReloading)
    };
    const hints = L.collectHints(ctx);
    ctx.hints = hints;
    const alarmFact = L.alarmFact(s.alarmInfo, status, serverTime);
    ctx.alarmFact = alarmFact;
    const out = {
      verdict: L.verdict(ctx),
      hints,
      overviewRows: L.overviewRows(ctx),
      boxRows: L.boxRows(status, ctx),
      keysSummary,
      alarmFact,
      commandLine: L.commandLine(data ? data.command : null, responseTime),
      offline, unsure, seenAge, watchdog, serverTime, mismatchMs, demoOn,
      demoSync: demoSyncOf(s, offline, unsure),
      retry: retryOf(s)
    };
    memo = { from: s, out };
    return out;
  }

  // Hat die Box die Demo-Liste übernommen? (Regeln wie bisher renderDemo.)
  // null = kein Demo-Modus; 'unknown' = keine aktuellen Daten (eine alte
  // "übernommen"-Aussage stehen zu lassen, wäre gefährlich); 'offline';
  // 'synced'; 'pending' = ein Relaisalarm ginge noch an ALLE.
  function demoSyncOf(s, offline, unsure) {
    if (!s.demo || !s.demo.enabled) return null;
    if (unsure || !s.status) return 'unknown';
    if (offline) return 'offline';
    const espVersion = s.status.keys_version;
    // "Übernommen" nur, wenn der Server es mit SEINER Regel bestätigt (wie
    // die Sperre des DEMO-ALARMs); älterer Server: Versionsvergleich.
    const synced = typeof s.demoSyncOk === 'boolean'
      ? (s.demoSyncOk && s.demoSyncFor === L.expectDemoOf(s.demo))
      : (espVersion !== undefined && s.nasKeysVersion !== null && String(espVersion) === String(s.nasKeysVersion));
    return synced ? 'synced' : 'pending';
  }

  // Nachsende-Angebot für die gemerkten nicht Erreichten - nur an Einträge
  // der gepflegten Liste (id > 0), nicht an die Not-Liste aus config.php.
  function retryOf(s) {
    const u = s.unreached;
    const r = s.result;
    if (!u || !r || !r.offerRetry || r.phase === 'sending') return null;
    const list = u.entries.filter((e) => e.id > 0);
    if (!list.length) return null;
    return {
      ids: list.map((e) => e.id),
      names: list.map((e) => e.name),
      label: list.length === 1 ? `Nur an ${list[0].name} erneut senden` : `Nur an die ${list.length} nicht Erreichten erneut senden`
    };
  }

  // ---- Route: welche Abfragen gerade gebraucht werden ----------------------
  function routePath(route) {
    if (route && typeof route === 'object') {
      const path = Array.isArray(route) ? route : route.path;
      return (Array.isArray(path) ? path : []).filter(Boolean).join('/');
    }
    return String(route || '').replace(/^#?\/*/, '').split('?')[0].replace(/\/+$/, '');
  }
  const section = (s) => (s.route.split('/')[0] || 'uebersicht');
  const wantsLog = (s) => section(s) === 'verlauf';
  const wantsRecent = (s) => section(s) === 'uebersicht' && s.wide;
  const wantsSettings = (s) => section(s) === 'einstellungen' && !s.readOnly;

  function setRoute(route, wide) {
    const before = { log: wantsLog(state) || wantsRecent(state), settings: wantsSettings(state) };
    set({ route: routePath(route) || 'uebersicht', wide: wide === undefined ? state.wide : !!wide });
    if (!before.log && (wantsLog(state) || wantsRecent(state))) refreshLog();
    if (!before.settings && wantsSettings(state)) {
      refreshNotify();
      refreshSettings();
    }
  }

  // ---- Demo-Zustand --------------------------------------------------------
  // EINZIGE Stelle, an der demo neu gesetzt wird: Wechselt der Modus
  // (LIVE <-> Demo oder anderer Test-Empfänger), während eine Alarm-
  // Bestätigung offen ist, passt deren Frage nicht mehr -> schließen.
  function updateDemo(next) {
    if (!next || typeof next !== 'object') return;
    if (!sameJson(state.demo, next)) set({ demo: next });
    const c = state.confirm;
    if (c && typeof c.expectDemo === 'string' && L.expectDemoOf(state.demo) !== c.expectDemo) {
      closeConfirm();
      // Mit Nachsende-Angebot: Eine gemerkte Nachsendung wird wieder
      // angeboten (ihre Frage nennt dann den neuen Modus).
      showNote(MODE_CHANGED_NOTE, 'warn', true);
    }
  }

  // HTTP 409 mode_changed: Der Server hat NICHTS gesendet, weil sein Modus
  // nicht zur Bestätigung passte - seinen Stand übernehmen, nichts neu senden.
  function applyModeChanged(err) {
    if (err && err.kind === 'mode_changed' && err.data && err.data.demo) updateDemo(err.data.demo);
  }

  // ---- Abfragen ------------------------------------------------------------
  const refreshStatus = api.makePoller(async () => {
    if (state.authLost) return;
    try {
      const data = await api.get('status', {}, api.TIMEOUT.POLL);
      if (!data.ok) throw new api.ApiError('http', data.message || 'Unerwartete Antwort vom NAS.', 200, data);
      adoptStatus(data);
    } catch (err) {
      if (isAuth(err)) return;   // handleAuthLoss() übernimmt
      set({ statusError: err || new api.ApiError('http', '', 0, null), failStreak: state.failStreak + 1 });
    }
    checkTransition();
  });

  function adoptStatus(data) {
    const status = data.status || {};
    // Aktuelles CSRF-Token der Sitzung übernehmen (auch ins Abmelde-Formular).
    if (typeof data.csrf === 'string' && data.csrf) api.setCsrf(data.csrf, true);
    L.setTimezone(data.timezone);
    updateDemo(data.demo);
    const now = Date.now();
    const nasKeysVersion = data.nas_keys_version ?? state.nasKeysVersion;
    const offline = !!data.offline;
    const patch = {
      status: keep(state.status, status), statusData: data, statusError: null, stale: false, lastOkAt: now, failStreak: 0, nowMs: now,
      nasKeysVersion,
      demoSyncOk: typeof data.demo_sync_ok === 'boolean' ? data.demo_sync_ok : null,
      demoSyncFor: L.expectDemoOf(state.demo),
      deviceHint: typeof data.device_hint === 'string' ? data.device_hint.trim() : ''
    };
    const serverTime = Number(data.server_time);
    if (Number.isFinite(serverTime) && serverTime > 0) patch.serverOffset = serverTime - now / 1000;
    // Hält die Abweichung der Listen-Version an? Die Messung gilt für EIN
    // Versionspaar Box|NAS: Ändert sich eine der beiden, beginnt sie von
    // vorn - sonst meldete eine frische Änderung sofort "seit 10 min".
    const differ = !offline && !L.na(status.keys_version) && nasKeysVersion !== null && nasKeysVersion !== undefined
      && String(status.keys_version) !== String(nasKeysVersion);
    const pair = `${status.keys_version}|${nasKeysVersion}`;
    if (!differ) {
      patch.mismatchSince = 0;
      mismatchPair = '';
    } else if (!state.mismatchSince || mismatchPair !== pair) {
      patch.mismatchSince = now;
      mismatchPair = pair;
    }
    set(patch);
    // Neuer Alarm aus Sicht der Box (anderer Text, Zustellung begonnen/
    // beendet, neuer Zähler)? Dann Alarm-Info und Verlauf sofort nachladen.
    const mark = `${status.last_alarm}|${status.alarm_pending}|${status.alarm_seq}`;
    if (lastAlarmMark !== null && mark !== lastAlarmMark) {
      refreshAlarmInfo();
      refreshLog();
    }
    lastAlarmMark = mark;
  }

  function showKeysCorrupt(data) {
    set({
      keysCorrupt: (data && typeof data.message === 'string' && data.message)
        || 'Empfängerliste auf dem NAS beschädigt (Liste und Sicherung unlesbar) – bitte die Dateien im Datenordner prüfen.',
      keys: null,
      keysError: ''
    });
  }

  const refreshKeys = api.makePoller(async () => {
    if (state.authLost) return;
    try {
      const data = await api.get('keys_list', {}, api.TIMEOUT.POLL);
      if (!data.ok) {
        if (data.code === 'keys_corrupt') showKeysCorrupt(data);
        return;
      }
      if (!Array.isArray(data.keys)) return;
      updateDemo(data.demo);
      set({ keys: keep(state.keys, data), keysCorrupt: '', keysError: '', nasKeysVersion: data.version ?? state.nasKeysVersion });
    } catch (err) {
      if (isAuth(err)) return;
      // Je nach Server-Stand kommt "Liste beschädigt" auch als HTTP-Fehler.
      if (err instanceof api.ApiError && err.data && err.data.code === 'keys_corrupt') {
        showKeysCorrupt(err.data);
        return;
      }
      // Netz-Schluckauf: die zuletzt bekannte Liste stehen lassen (alles
      // andere sähe nach Datenverlust aus) - Fehler nur, wenn noch nichts da ist.
      if (!state.keys && !state.keysCorrupt) {
        set({ keysError: 'Empfängerliste konnte nicht geladen werden – neuer Versuch läuft automatisch.' });
      }
    }
  });

  async function loadLog() {
    const { filter, limit } = state.log;
    try {
      const data = await api.get('log_view', { limit, filter }, api.TIMEOUT.POLL);
      // Filter/Umfang inzwischen gewechselt: Diese Antwort gehört zum alten
      // Stand - die Abfrage für den neuen läuft schon hinterher (Poller).
      if (!data.ok || filter !== state.log.filter || limit !== state.log.limit) return;
      const entries = Array.isArray(data.entries) ? data.entries : [];
      setLog({
        entries: keep(state.log.entries, entries),
        // "Ältere laden" nur, wenn die Seite voll zurückkam und der
        // Server-Deckel noch nicht erreicht ist.
        more: limit < L.LOG_MAX && entries.length >= limit,
        error: '',
        serverTime: Number(data.server_time) || Math.floor(Date.now() / 1000)
      });
    } catch (err) {
      if (isAuth(err)) return;
      if (state.log.entries === null) setLog({ error: 'Verlauf konnte nicht geladen werden – neuer Versuch läuft automatisch.' });
    }
  }

  async function loadRecent() {
    try {
      const data = await api.get('log_view', { limit: RECENT_LIMIT, filter: 'all' }, api.TIMEOUT.POLL);
      if (!data.ok) return;
      set({ recent: { entries: keep(state.recent.entries, Array.isArray(data.entries) ? data.entries : []), serverTime: Number(data.server_time) || 0 } });
    } catch (err) {
      // letzter bekannter Stand bleibt
    }
  }

  // Verlauf nur, solange seine Seite (bzw. die breite Übersicht mit
  // "Letzte Ereignisse") sichtbar ist.
  const refreshLog = api.makePoller(async () => {
    if (state.authLost) return;
    if (wantsLog(state)) await loadLog();
    if (wantsRecent(state)) await loadRecent();
  });

  // Jüngster Alarm für "Letzter Alarm": nur die Versand-Ereignisse
  // (filter=alarm_core) - unabhängig vom Filter des Verlaufs und ohne die
  // Befehls-Ereignisse der Demo-Alarme.
  const refreshAlarmInfo = api.makePoller(async () => {
    if (state.authLost) return;
    try {
      const data = await api.get('log_view', { limit: 12, filter: 'alarm_core' }, api.TIMEOUT.POLL);
      if (!data.ok) return;
      set({ alarmInfo: keep(state.alarmInfo, L.deriveAlarmInfo(data.entries || [])) });
    } catch (err) {
      // die Zeile behält den letzten bekannten Stand
    }
  });

  // Meldungen/Einstellungen: nur für den Admin und nur auf ihren Seiten.
  // Wie bei der Empfängerliste: Fehler nur, solange noch nichts geladen ist.
  function adminPoller(action, dataKey, pick) {
    const errorKey = `${dataKey}Error`;
    return api.makePoller(async () => {
      if (state.authLost || !wantsSettings(state)) return;
      try {
        const data = await api.get(action, {}, api.TIMEOUT.POLL);
        if (!data.ok) return;
        set({ [dataKey]: keep(state[dataKey], pick(data)), [errorKey]: '' });
      } catch (err) {
        if (isAuth(err)) return;
        if (state[dataKey] === null) set({ [errorKey]: 'Einstellungen konnten nicht geladen werden – neuer Versuch läuft automatisch.' });
      }
    });
  }
  const refreshNotify = adminPoller('notify_list', 'notify', (data) => data);
  const refreshSettings = adminPoller('settings_list', 'settings', (data) => data.settings || null);

  function refreshAll() {
    return Promise.all([refreshStatus(), refreshKeys(), refreshAlarmInfo(), refreshLog(), refreshNotify(), refreshSettings()]);
  }

  // ---- Takt, Start/Stopp, Sichtbarkeit -------------------------------------
  let timers = [];
  let backgroundTimer = null;

  // Sekündlich: Zeitangaben laufen weiter, und ohne erfolgreiche Antwort
  // seit 15 s gilt der Stand als veraltet (Status-Karte nicht mehr grün).
  function tick() {
    const now = Date.now();
    const patch = { nowMs: now };
    if (!state.authLost) patch.stale = !!state.lastOkAt && now - state.lastOkAt > STALE_AFTER_MS;
    set(patch);
    if (state.confirm && now >= state.confirm.deadline) closeConfirm();
  }

  function stopBackground() {
    if (backgroundTimer !== null) clearInterval(backgroundTimer);
    backgroundTimer = null;
  }

  function start() {
    if (state.authLost || timers.length) return;
    stopBackground();
    refreshStatus();
    refreshKeys();
    refreshLog();
    refreshAlarmInfo();
    refreshNotify();
    refreshSettings();
    // .tick: Der Timer lässt einen Takt aus, solange die vorige Abfrage
    // noch läuft (siehe makePoller).
    timers = [
      setInterval(refreshStatus.tick, INTERVAL.STATUS),
      setInterval(refreshKeys.tick, INTERVAL.KEYS),
      setInterval(refreshLog.tick, INTERVAL.LOG),
      setInterval(refreshAlarmInfo.tick, INTERVAL.ALARM_INFO),
      setInterval(refreshNotify.tick, INTERVAL.NOTIFY),
      setInterval(refreshSettings.tick, INTERVAL.SETTINGS),
      setInterval(tick, INTERVAL.TICK)
    ];
  }

  function stop() {
    timers.forEach((id) => clearInterval(id));
    timers = [];
    stopBackground();
  }

  function resetMismatch() {
    mismatchPair = '';
    if (state.mismatchSince) set({ mismatchSince: 0 });
  }

  // Tab im Hintergrund: Abfragen aus UND jede offene Bestätigung schließen -
  // wer zurückkommt, soll keine (evtl. veraltete) Alarm-Frage mit einem Tipp
  // bestätigen können. Die Messung "Liste nicht übernommen" beginnt neu:
  // Während der Tab versteckt war, wurde nichts beobachtet.
  function handleVisibility() {
    resetMismatch();
    if (env.hidden()) {
      closeConfirm();
      stop();
      if (state.browserAlert && !state.authLost) backgroundTimer = setInterval(refreshStatus.tick, INTERVAL.BACKGROUND);
    } else {
      start();
    }
  }

  // Sitzung serverseitig abgelaufen (HTTP 401): Abfragen aus, "Abgemeldet",
  // dann zur Anmeldung neu laden. Läuft gerade ein REAL ALARM/eine
  // ENTWARNUNG, wird NICHT neu geladen: Die Anfrage läuft auf dem NAS weiter -
  // ihr Ergebnis (wer NICHT erreicht wurde) muss lesbar bleiben.
  function handleAuthLoss() {
    if (state.authLost) return;
    stop();
    const keep = state.alarmInFlight;
    set({ authLost: true, authReloading: !keep });
    if (keep) return;
    setTimeout(() => {
      // Inzwischen doch ein Versand gestartet? Dann ebenfalls stehen lassen.
      if (state.alarmInFlight) {
        set({ authReloading: false });
        return;
      }
      env.reload();
    }, AUTH_RELOAD_MS);
  }

  // ---- "Hinweis im Browser bei Störung": Übergänge (Spec §8.6) -------------
  // Schlüssel = Urteil, mit zwei Abweichungen: 'nas' erst ab dem 3.
  // Fehlschlag in Folge (bis dahin gilt der vorige Stand), und "Bereit mit
  // Hinweisen" heißt 'delivery', wenn die letzte Zustellung unvollständig ist.
  const transitionListeners = new Set();
  let watchKey = null;
  function onTransition(fn) {
    transitionListeners.add(fn);
    return () => { transitionListeners.delete(fn); };
  }
  function checkTransition() {
    const d = derive();
    let next = d.verdict.key;
    if (next === 'nas' && state.failStreak < NAS_ALERT_AFTER_FAILS) next = watchKey;
    if (next === 'hints' && !d.offline && L.deliveryIncomplete(state.status)) next = 'delivery';
    if (next === null || next === watchKey) return;
    const from = watchKey;
    watchKey = next;
    if (from === null || !state.browserAlert) return;
    const event = { from, to: next, hidden: env.hidden(), failStreak: state.failStreak };
    Array.from(transitionListeners).forEach((fn) => {
      try { fn(event); } catch (err) { report(err); }
    });
  }

  function readStored(key) {
    try { return root.localStorage.getItem(key); } catch (err) { return null; }
  }
  function writeStored(key, value) {
    try {
      if (value === null) root.localStorage.removeItem(key);
      else root.localStorage.setItem(key, value);
    } catch (err) {
      // Speicher gesperrt (privates Fenster o.ä.) - dann nur in dieser Seite.
    }
  }

  function setBrowserAlert(on) {
    set({ browserAlert: !!on });
    writeStored(BROWSER_ALERT_STORE, on ? '1' : null);
  }

  // ---- Bestätigungen -------------------------------------------------------
  // Höchstens EINE offene Bestätigung. Die Frist rechnet gegen eine feste
  // Uhrzeit (iOS pausiert Timer im Hintergrund); bestätigt nach Ablauf löst
  // NICHT aus. expectDemo (nur Alarm-Bestätigungen): der Modus, den die Frage
  // beschreibt - weicht der tatsächliche ab, wird geschlossen.
  let confirmSeq = 0;
  let confirmAction = null;
  let confirmTimer = null;

  function openConfirm(opts) {
    const o = opts || {};
    closeConfirm();
    const expectDemo = typeof o.expectDemo === 'string' ? o.expectDemo : null;
    // Frage aus einem schon überholten Stand: gar nicht erst öffnen.
    if (expectDemo !== null && L.expectDemoOf(state.demo) !== expectDemo) {
      showNote(MODE_CHANGED_NOTE, 'warn', true);
      return null;
    }
    const text = String(o.text || '');
    const id = ++confirmSeq;
    const deadline = Date.now() + L.armTimeoutFor(text) * 1000;
    confirmAction = typeof o.onConfirm === 'function' ? o.onConfirm : null;
    set({
      confirm: {
        id, deadline, text, expectDemo,
        title: String(o.title || ''),
        confirmLabel: String(o.confirmLabel || 'Bestätigen'),
        hold: !!o.hold,
        tag: o.tag ? String(o.tag) : null
      }
    });
    confirmTimer = setTimeout(() => closeConfirm(id), deadline - Date.now());
    return id;
  }

  // Ohne id: die offene schließen; mit id nur genau diese (ein veraltetes
  // Sheet darf keine neuere Bestätigung schließen).
  function closeConfirm(id) {
    const c = state.confirm;
    if (!c || (id !== undefined && id !== null && id !== c.id)) return;
    clearTimeout(confirmTimer);
    confirmTimer = null;
    confirmAction = null;
    set({ confirm: null });
  }

  // Liefert das Ergebnis der bestätigten Aktion (Promise) bzw. null, wenn
  // nichts ausgelöst wurde (Frist abgelaufen, Modus gewechselt, andere id).
  function confirmNow(id) {
    const c = state.confirm;
    if (!c || (id !== undefined && id !== null && id !== c.id)) return Promise.resolve(null);
    const action = confirmAction;
    const expired = Date.now() >= c.deadline;
    const modeChanged = c.expectDemo !== null && L.expectDemoOf(state.demo) !== c.expectDemo;
    closeConfirm();
    if (modeChanged) {
      showNote(MODE_CHANGED_NOTE, 'warn', true);
      return Promise.resolve(null);
    }
    if (expired || !action) return Promise.resolve(null);
    try {
      return Promise.resolve(action());
    } catch (err) {
      return Promise.reject(err);
    }
  }

  // Alarm-Bestätigungen (Halten zum Senden): Frage UND expect_demo aus
  // DERSELBEN Momentaufnahme. Der Server sendet nur, wenn der Modus beim
  // Senden noch stimmt (sonst 409). kind: 'alarm' | 'false_alarm' | 'box_alarm'.
  const CONFIRM_LABELS = {
    alarm: 'Ja, Alarm auslösen',
    false_alarm: 'Ja, Entwarnung senden',
    box_alarm: 'Ja, Demo-Alarm auslösen'
  };
  function confirmAlarm(kind) {
    if (state.alarmInFlight || !CONFIRM_LABELS[kind]) return null;
    const demo = state.demo;
    // Der Alarm über die Box ist ein reines Demo-Werkzeug - im LIVE-Modus
    // wäre er ein echter Alarm an alle (der Server blockt das zusätzlich).
    if (kind === 'box_alarm' && !(demo && demo.enabled)) return null;
    const expect = L.expectDemoOf(demo);
    const q = L.alarmQuestion(kind, demo, keyList());
    const send = kind === 'box_alarm' ? () => enqueueBoxAlarm(expect) : () => sendNasAlarm(kind, expect);
    return openConfirm({ title: q.title, text: q.text, confirmLabel: CONFIRM_LABELS[kind], hold: true, expectDemo: expect, onConfirm: send });
  }

  // "Nur an die nicht Erreichten erneut senden" - immer aus dem gemerkten
  // Stand; die Frage nennt den Modus, der JETZT gilt.
  function confirmResend() {
    const u = state.unreached;
    if (state.alarmInFlight || !u) return null;
    const retry = u.entries.filter((e) => e.id > 0);
    if (!retry.length) return null;
    const ids = retry.map((e) => e.id);
    const demo = state.demo;
    const expect = L.expectDemoOf(demo);
    const q = L.alarmQuestion('resend', demo, keyList(), { names: retry.map((e) => e.name), what: u.what, ids });
    return openConfirm({
      title: q.title, text: q.text, confirmLabel: 'Ja, erneut senden', hold: true, expectDemo: expect, tag: 'resend',
      onConfirm: () => sendNasAlarm(u.action, expect, ids, u.startedAt)
    });
  }

  // ---- Ergebnis nach Alarm/Entwarnung --------------------------------------
  // result.phase: 'sending' (läuft), 'done' (Antwort mit Ergebnis), 'note'
  // (Fehler/Hinweis). Die nicht Erreichten stehen in state.unreached (die
  // zusammengeführte Liste - die Ansicht zeigt unreached.entries, nie nur
  // result.failed). fresh: dieses Ergebnis hat unreached neu gesetzt.
  function makeResult(fields) {
    return Object.assign({
      phase: 'note', tone: '', head: '', action: null, what: null, resend: false, fresh: false,
      failed: [], reached: [], total: 0, offerRetry: false, at: 0
    }, fields);
  }

  // Ein neues Ergebnis schließt eine offene Nachsende-Bestätigung (sie
  // gehörte zum alten Ergebnis).
  function setResult(result) {
    if (state.confirm && state.confirm.tag === 'resend') closeConfirm();
    set({ result });
  }

  function showNote(head, tone, offerRetry) {
    setResult(makeResult({ phase: tone ? 'note' : 'sending', tone: tone || '', head, offerRetry: !!offerRetry }));
  }

  // Wer zuletzt NICHT erreicht wurde, bleibt stehen, bis ein neueres
  // Ergebnis es ersetzt oder "Ausblenden" - auch über ein Neuladen hinweg
  // (localStorage, max. 12 h; iOS verwirft sessionStorage mit der App).
  function setUnreached(value) {
    set({ unreached: value });
    writeStored(UNREACHED_STORE, value ? JSON.stringify(value) : null);
  }

  function restoreUnreached() {
    const raw = readStored(UNREACHED_STORE);
    if (!raw) return null;
    const value = L.validateUnreached(raw, Date.now());
    if (!value) writeStored(UNREACHED_STORE, null);
    return value;
  }

  function clearResult() {
    if (state.confirm && state.confirm.tag === 'resend') closeConfirm();
    setUnreached(null);
    set({ result: null });
  }

  function showSendResult(action, what, data, resend) {
    // Uhrzeit nach der Uhr des NAS, nicht nach der evtl. falschen Geräte-Uhr.
    const nowMs = (Date.now() / 1000 + state.serverOffset) * 1000;
    const results = Array.isArray(data.results) ? data.results : [];
    const r = L.buildUnreached(state.unreached, action, data, resend, nowMs);
    // Nur ein Ergebnis MIT Versand ändert die gemerkte Liste - "nichts
    // gesendet" lässt die Namen stehen.
    if (results.length) setUnreached(r.unreached);
    const head = `${L.fmtClockMs(nowMs)} Uhr, ${what}: ${data.message || (data.ok ? 'Gesendet.' : 'Fehler.')}`;
    setResult(makeResult({
      phase: 'done', tone: data.ok === false ? 'fail' : 'ok', head, action, what, resend: !!resend,
      fresh: results.length > 0, failed: r.failed, reached: r.reached, total: results.length, offerRetry: true, at: nowMs
    }));
    return outcome(data.ok !== false, head, data.ok === false ? 'error' : 'ok');
  }

  // REAL ALARM und ENTWARNUNG gehen direkt vom NAS an Bark (meist ~35 s,
  // schlimmstenfalls ~60 s); währenddessen sind alle Alarm-Knöpfe gesperrt.
  // "NICHT gesendet" nur, wenn das sicher ist - sonst "Antwort unklar", damit
  // niemand blind ein zweites Mal alarmiert. onlyIds = Nachsendung nur an
  // diese Empfänger (nie ersatzweise an alle), origAt = Server-Zeit (s) des
  // ursprünglichen Versands. expect_demo geht IMMER mit.
  async function sendNasAlarm(action, expect, onlyIds = null, origAt = 0) {
    if (action !== 'alarm' && action !== 'false_alarm') return outcome(false, 'Unbekannte Aktion – nichts gesendet.', 'error');
    if (state.alarmInFlight) return outcome(false, 'Es läuft bereits ein Versand – bitte dessen Ergebnis abwarten.', 'error');
    const what = L.whatOf(action);
    const resend = onlyIds !== null && onlyIds !== undefined;
    set({ alarmInFlight: true, busyCount: state.busyCount + 1 });
    showNote(`${what} wird gesendet… (kann bis zu einer Minute dauern – Seite nicht schließen)`, '');
    try {
      const fields = { action, expect_demo: expect === null || expect === undefined ? undefined : String(expect) };
      if (resend) {
        fields.only_ids = Array.isArray(onlyIds) ? onlyIds.join(',') : String(onlyIds);
        // So trägt die Nachsendung die Uhrzeit des ERSTEN Versands.
        if (origAt > 0) fields.orig_at = String(Math.floor(origAt));
      }
      const data = await api.post(fields, api.TIMEOUT.ALARM);
      return showSendResult(action, what, data, resend ? { ids: onlyIds, origAt } : null);
    } catch (err) {
      const kind = err instanceof api.ApiError ? err.kind : 'network';
      let text;
      let unclear = false;
      if (kind === 'auth') {
        text = `${what} NICHT gesendet – Sitzung abgelaufen, bitte neu anmelden.`;
      } else if (kind === 'csrf') {
        text = `${what} NICHT gesendet – ${api.csrfHint()}.`;
      } else if (kind === 'readonly') {
        text = `${what} NICHT gesendet – ${err.message || 'nur Lesezugriff'}`;
      } else if (kind === 'mode_changed') {
        applyModeChanged(err);
        text = err.message || `${what} NICHT gesendet – der Demo-/LIVE-Modus hat sich geändert. Bitte Lage prüfen und neu auslösen.`;
      } else if (kind === 'http' && err.status >= 400 && err.status < 500) {
        // Vom Server abgelehnt, bevor etwas rausging.
        text = `${what} NICHT gesendet – ${err.message || `HTTP ${err.status}`}`;
      } else {
        // Zeitlimit, abgerissene Verbindung, 5xx oder kaputte Antwort: Der
        // Server kann bereits (teilweise) gesendet haben.
        unclear = true;
        let detail = 'Verbindung abgebrochen';
        if (kind === 'timeout') detail = 'Zeitüberschreitung';
        else if (kind === 'http') detail = `HTTP ${err.status}${err.message ? `: ${err.message}` : ''}`;
        text = `Antwort unklar (${detail}) – ${what} wurde evtl. gesendet. Erst Verlauf prüfen, nicht sofort erneut auslösen!`;
      }
      // Eine sicher NICHT rausgegangene Nachsendung wird wieder angeboten
      // (mit neuer Bestätigung) - nach "unklar" bewusst nicht.
      showNote(text, unclear ? 'warn' : 'fail', resend && !unclear);
      if (unclear) {
        UNCLEAR_RECHECK_MS.forEach((ms) => setTimeout(() => { refreshLog(); refreshAlarmInfo(); }, ms));
      }
      return outcome(false, text, unclear ? 'warn' : 'error');
    } finally {
      set({ alarmInFlight: false, busyCount: Math.max(0, state.busyCount - 1) });
      refreshStatus();
      refreshLog();
      refreshAlarmInfo();
    }
  }

  // ---- Einfache Aktionen ---------------------------------------------------
  // EIN Muster: Zähler laufender Aktionen hoch, POST, Ergebnis als
  // {ok, text, kind}, danach nachladen, was die Aktion verändert haben kann.
  async function runAction(fields, opts) {
    const o = opts || {};
    busyUp();
    try {
      const data = await api.post(fields, o.timeoutMs || api.TIMEOUT.POST);
      if (o.onData) o.onData(data);
      const text = o.text ? o.text(data) : (data.message || (data.ok ? (o.okText || 'Gespeichert.') : 'Fehler.'));
      return outcome(!!data.ok, text, api.kindOf(data));
    } catch (err) {
      applyModeChanged(err);
      return outcome(false, api.errorText(err), api.errorKind(err));
    } finally {
      busyDown();
      (o.after || []).forEach((fn) => fn());
    }
  }

  // Doppelklick-Schutz für TEST, DEMO-ALARM, Abbrechen, Hinzufügen und den
  // Demo-Schalter: Läuft noch eine Aktion, wird nichts angelegt. Die
  // Alarm-Knöpfe hängen NICHT daran (ein langsamer Test-Push darf den Alarm
  // nicht sperren).
  const refusedBusy = () => Promise.resolve(outcome(false, BUSY_TEXT, 'error'));

  // Befehl an die Box (TEST bzw. im Demo-Modus ALARM): Die Befehlszeile zeigt
  // ihn ab jetzt - "Befehl angelegt." wäre nur eine Doppelung.
  function enqueue(fields) {
    return runAction(fields, {
      text: (data) => (data.ok ? '' : (data.message || 'Fehler.')),
      after: [refreshStatus, refreshLog]
    });
  }

  function enqueueTest() {
    if (state.busyCount > 0) return refusedBusy();
    return enqueue({ action: 'enqueue', type: 'TEST' });
  }

  // DEMO-Alarm über die Box: teilt sich die Befehls-Warteschlange mit TEST
  // und sperrt währenddessen die Alarm-Knöpfe.
  async function enqueueBoxAlarm(expect) {
    if (state.alarmInFlight) return outcome(false, 'Es läuft bereits ein Versand – bitte dessen Ergebnis abwarten.', 'error');
    if (state.busyCount > 0) return refusedBusy();
    set({ alarmInFlight: true });
    try {
      return await enqueue({ action: 'enqueue', type: 'ALARM', expect_demo: expect === null || expect === undefined ? undefined : String(expect) });
    } finally {
      set({ alarmInFlight: false });
    }
  }

  function cancelCommand() {
    if (state.busyCount > 0) return refusedBusy();
    return runAction({ action: 'cancel' }, { okText: 'Befehl abgebrochen.', after: [refreshStatus, refreshLog] });
  }

  function demoSet(enabled, id) {
    if (state.busyCount > 0) return refusedBusy();
    if (!state.demo) return Promise.resolve(outcome(false, 'Der Demo-Zustand ist noch nicht geladen – bitte kurz warten.', 'error'));
    const fields = { action: 'demo_set', enabled: enabled ? '1' : '0' };
    if (enabled) {
      if (id === undefined || id === null || id === '') {
        return Promise.resolve(outcome(false, 'Bitte zuerst einen Empfänger in der Liste anlegen und als Test-Empfänger auswählen.', 'error'));
      }
      fields.id = String(id);
    }
    return runAction(fields, {
      onData: (data) => { if (data.ok && data.demo) updateDemo(data.demo); },
      after: [refreshStatus, refreshKeys, refreshLog]
    });
  }

  // Eine Aktion an EINEM Empfänger; danach Liste und Verlauf nachladen.
  function keyAction(fields, opts) {
    return runAction(fields, Object.assign({ after: [refreshKeys, refreshLog] }, opts));
  }

  // Neue id im Ergebnis (keys_add liefert keine): nach dem Nachladen der
  // Liste der Eintrag, den es vorher nicht gab - für "Test-Push senden".
  async function keyAdd(label, key, phone) {
    if (state.busyCount > 0) return refusedBusy();
    const before = new Set((keyList() || []).map((k) => Number(k.id)));
    const fields = { action: 'keys_add', label: String(label || '').trim(), key: String(key || '').trim() };
    const tel = String(phone || '').trim();
    if (tel) fields.phone = tel;
    const res = await runAction(fields, { after: [refreshLog] });
    if (!res.ok) {
      refreshKeys();   // nicht abwarten: die Fehlermeldung soll sofort stehen
      return res;
    }
    await refreshKeys();
    const added = (keyList() || []).filter((k) => !before.has(Number(k.id)));
    res.id = added.length === 1 ? Number(added[0].id) : null;
    return res;
  }

  const keyDelete = (id) => keyAction({ action: 'keys_delete', id: String(id) });
  const keyMute = (id, muted) => keyAction({ action: 'keys_mute', id: String(id), muted: muted ? '1' : '0' });
  const keyPermanent = (id, on) => keyAction({ action: 'keys_set_mute_permanent', id: String(id), permanent: on ? '1' : '0' });
  const keyVolume = (id, vol) => keyAction({ action: 'keys_set_mute_volume', id: String(id), volume: String(vol) });
  const keyRename = (id, label) => keyAction({ action: 'keys_rename', id: String(id), label: String(label || '').trim() });
  const keyReplace = (id, key) => keyAction({ action: 'keys_replace_key', id: String(id), key: String(key || '').trim() });
  const keyPhone = (id, phone) => keyAction({ action: 'keys_set_phone', id: String(id), phone: String(phone || '').trim() });

  // Test-Push: höchstens einer gleichzeitig (der Versand dauert bis ~20 s).
  async function keyTest(id) {
    if (state.keyTestInFlight) return outcome(false, 'Es läuft bereits ein Test-Push – bitte kurz warten.', 'error');
    set({ keyTestInFlight: true, keyTestId: String(id) });
    try {
      return await keyAction({ action: 'keys_test', id: String(id) }, { timeoutMs: api.TIMEOUT.SEND_ONE, okText: 'Gesendet.' });
    } finally {
      set({ keyTestInFlight: false, keyTestId: null });
    }
  }

  // Meldungen an den Admin: der Wächter-Hinweis im Status hängt an zwei
  // dieser Einstellungen - deshalb auch den Status neu holen.
  function notifySet(type, key, value) {
    return runAction({ action: 'notify_set', type: String(type), key: String(key), value: String(value) },
      { after: [refreshNotify, refreshLog, refreshStatus] });
  }

  async function notifyTest(type) {
    if (state.probeInFlight) return outcome(false, 'Es läuft bereits eine Probe-Meldung – bitte kurz warten.', 'error');
    set({ probeInFlight: true, probeType: String(type) });
    try {
      return await runAction({ action: 'notify_test', type: String(type) }, { timeoutMs: api.TIMEOUT.SEND_ONE, okText: 'Gesendet.' });
    } finally {
      set({ probeInFlight: false, probeType: null });
    }
  }

  // "Automatisch wieder laut nach": ändert auch mute_until in der Liste.
  function settingsSet(key, value) {
    return runAction({ action: 'settings_set', key: String(key), value: String(value) },
      { after: [refreshSettings, refreshKeys, refreshLog] });
  }

  // ---- Verlauf -------------------------------------------------------------
  function setLogFilter(filter) {
    const f = L.LOG_FILTERS.includes(filter) ? filter : 'all';
    if (f === state.log.filter) return;
    setLog({ filter: f, limit: L.LOG_STEP, entries: null, more: false, error: '' });
    refreshLog();
  }

  // Suche: filtert clientseitig die geladenen Einträge (L.logMatches).
  function setLogQuery(query) {
    setLog({ query: String(query || '') });
  }

  function loadMoreLog() {
    setLog({ limit: Math.min(L.LOG_MAX, state.log.limit + L.LOG_STEP) });
    refreshLog();
  }

  // ---- Start ---------------------------------------------------------------
  let wired = false;
  // opts aus den meta-Tags der Seite: role, user, csrf, publicBase, muteMax.
  function init(opts) {
    const o = opts || {};
    stop();
    closeConfirm();
    state = initialState(o);
    memo = { from: null, out: null };
    mismatchPair = '';
    lastAlarmMark = null;
    watchKey = null;
    api.setCsrf(String(o.csrf || ''));
    const restored = restoreUnreached();
    set({ unreached: restored, browserAlert: readStored(BROWSER_ALERT_STORE) === '1' });
    // Gemerkte nicht Erreichte (z.B. vor einem Neuladen nach Ablauf der
    // Anmeldung) wieder anzeigen - samt Nachsende-Angebot.
    if (restored) showNote(RESTORED_NOTE, 'warn', true);
    if (!wired) {
      wired = true;
      api.onAuthLoss(handleAuthLoss);
      // Bei einem CSRF-Fehler sofort den Status neu holen - er bringt das
      // aktuelle Token mit (kein automatischer Neuversuch der Aktion).
      api.onCsrfError(() => { refreshStatus(); });
      const doc = root.document;
      if (doc && typeof doc.addEventListener === 'function') doc.addEventListener('visibilitychange', handleVisibility);
    }
  }

  FW.store = {
    INTERVAL, STALE_AFTER_MS, env,
    init, start, stop, tick, getState, subscribe, useStore, derive, setRoute,
    refreshAll, refreshStatus, refreshKeys, refreshLog, refreshAlarmInfo, refreshNotify, refreshSettings,
    openConfirm, closeConfirm, confirmNow, confirmAlarm, confirmResend,
    enqueueTest, enqueueBoxAlarm, cancelCommand, sendNasAlarm, clearResult,
    demoSet, keyAdd, keyDelete, keyMute, keyPermanent, keyVolume, keyTest, keyRename, keyReplace, keyPhone,
    notifySet, notifyTest, settingsSet,
    setLogFilter, setLogQuery, loadMoreLog,
    setBrowserAlert, onTransition
  };
})(typeof window !== 'undefined' ? window : globalThis);
