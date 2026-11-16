(function (root) {
  'use strict';
  const FW = root.FW = root.FW || {};

  // ==== Anfragen ans NAS ====================================================
  // ALLE Anfragen laufen über request(): mit Zeitlimit und mit einer klaren
  // Fehlerart, damit die Oberfläche unterscheiden kann zwischen
  //   'network'/'timeout' -> NAS nicht erreichbar (Antwort kam nie an),
  //   'http'              -> NAS antwortet, aber mit Fehler (z.B. HTTP 500),
  //   'auth'              -> Sitzung abgelaufen (401),
  //   'csrf'/'readonly'   -> Aktion serverseitig abgelehnt (403),
  //   'mode_changed'      -> Demo-/LIVE-Modus passt nicht zur Bestätigung (409).
  // Portiert aus dem bisherigen app.js (api(), errorText(), makePoller()) -
  // Fehlerarten, Texte und die Sonderfälle älterer Server-Stände unverändert.
  // Einziger DOM-Zugriff: das CSRF-Feld im Abmelde-Formular (setCsrf).

  const ENDPOINT = 'dashboard_api.php';

  // Zeitlimits je Anfrageart: Ohne Limit hält eine hängende Anfrage den
  // "läuft noch"-Schutz der Poller ewig fest, und die Status-Karte bliebe
  // grün. REAL ALARM/ENTWARNUNG senden serverseitig an alle Empfänger (meist
  // ~35 s, schlimmstenfalls ~60 s) - deshalb dort großzügig. Ein einzelner
  // Bark-Versand (Test-Push, Probe-Meldung) braucht bis ~21 s.
  const TIMEOUT = Object.freeze({ POLL: 10000, POST: 20000, SEND_ONE: 30000, ALARM: 120000 });

  class ApiError extends Error {
    constructor(kind, message, status, data) {
      super(message || '');
      this.name = 'ApiError';
      this.kind = kind;
      this.status = status || 0;
      this.data = data || null;
    }
  }

  // CSRF-Token der Sitzung: Startwert aus der Seite, danach aus jeder
  // Status-Antwort - sonst scheiterte ein alter Tab nach einer Neuanmeldung
  // in einem anderen Tab an einem veralteten Token.
  let csrf = '';
  // Liefert der Server das Token mit der Status-Antwort aus? (Ältere Stände
  // nicht - dann hilft bei einem CSRF-Fehler nur Neuladen.)
  let csrfFromServer = false;
  // Einmal 401 = abgemeldet: Danach laufen keine Nachläufe der Poller mehr.
  let authLost = false;
  const authLossHandlers = [];
  const csrfErrorHandlers = [];

  // Ein Fehler in einem Empfänger darf die Anfrage selbst nicht stören.
  function notify(list, arg) {
    list.slice().forEach((fn) => {
      try {
        fn(arg);
      } catch (err) {
        if (root.console && root.console.error) root.console.error(err);
      }
    });
  }

  const subscriber = (list) => (fn) => {
    list.push(fn);
    return () => {
      const i = list.indexOf(fn);
      if (i >= 0) list.splice(i, 1);
    };
  };
  const onAuthLoss = subscriber(authLossHandlers);
  const onCsrfError = subscriber(csrfErrorHandlers);
  const isAuthLost = () => authLost;

  // Neues CSRF-Token übernehmen - auch im Abmelde-Formular der Seite, das
  // sonst mit dem Token vom Seitenaufruf abgeschickt würde. fromServer: das
  // Token kam mit einer Status-Antwort (siehe csrfHint).
  function setCsrf(token, fromServer) {
    if (fromServer) csrfFromServer = true;
    if (typeof token !== 'string' || token === csrf) return;
    csrf = token;
    const doc = root.document;
    if (doc && typeof doc.querySelectorAll === 'function') {
      doc.querySelectorAll('form input[name="csrf"]').forEach((input) => { input.value = token; });
    }
  }
  const getCsrf = () => csrf;

  // Zeitlimit-Signal: AbortSignal.timeout(), wo vorhanden; ältere Browser
  // (z.B. iOS < 16) bekommen dasselbe über AbortController + setTimeout.
  function timeoutSignal(ms) {
    if (typeof AbortSignal !== 'undefined' && AbortSignal && typeof AbortSignal.timeout === 'function') {
      return { signal: AbortSignal.timeout(ms), cancel: () => {} };
    }
    if (typeof AbortController === 'undefined') return { signal: undefined, cancel: () => {} };
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), ms);
    return { signal: ctrl.signal, cancel: () => clearTimeout(timer) };
  }

  // Abbruch durch das Zeitlimit (TimeoutError bzw. AbortError beim
  // Fallback) - wir brechen sonst nie selbst ab, also ist das eindeutig.
  const isAbort = (err) => !!err && (err.name === 'TimeoutError' || err.name === 'AbortError');

  // Nicht-JSON-Fehlerseiten (text/plain-500, HTML vom Webserver) lesbar
  // kürzen. Landet ausschließlich als Text im DOM.
  function shortText(text) {
    const plain = String(text || '').replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ').trim();
    return plain.length > 200 ? plain.slice(0, 200) + '…' : plain;
  }

  async function request(url, opts, timeoutMs) {
    const t = timeoutSignal(timeoutMs || TIMEOUT.POLL);
    try {
      let res;
      try {
        res = await fetch(url, Object.assign({ credentials: 'same-origin' }, opts, { signal: t.signal }));
      } catch (err) {
        throw new ApiError(isAbort(err) ? 'timeout' : 'network', '', 0, null);
      }
      let text = '';
      try {
        text = await res.text();
      } catch (err) {
        // Kopfzeilen kamen an, der Inhalt nicht: Das NAS HAT die Anfrage
        // also bekommen - für Alarme heißt das "Antwort unklar".
        throw new ApiError(isAbort(err) ? 'timeout' : 'network', '', res.status, null);
      }
      let data = null;
      try { data = text ? JSON.parse(text) : null; } catch (e) { data = null; }
      const json = data && typeof data === 'object' ? data : null;
      const code = json && typeof json.code === 'string' ? json.code : '';
      const message = json && typeof json.message === 'string' ? json.message : '';

      if (res.status === 401) {
        authLost = true;
        notify(authLossHandlers, null);
        throw new ApiError('auth', 'Sitzung abgelaufen – bitte neu anmelden.', 401, json);
      }
      if (res.status === 409 && code === 'mode_changed') {
        throw new ApiError('mode_changed', message, 409, json);
      }
      if (res.status === 403) {
        // Ältere Server-Stände schicken bei CSRF-Fehlern nur {ok:false}
        // (ohne code/message) und bei Lesezugriff nur eine message.
        if (code === 'readonly' || (!code && message)) {
          throw new ApiError('readonly', message, 403, json);
        }
        if (code === 'csrf' || !code) {
          throw new ApiError('csrf', message, 403, json);
        }
      }
      if (!res.ok) {
        throw new ApiError('http', message || (json ? '' : shortText(text)), res.status, json);
      }
      if (!json) {
        throw new ApiError('http', 'Antwort des Servers ist kein gültiges JSON' + (text ? `: ${shortText(text)}` : '.'), res.status, null);
      }
      return json;
    } finally {
      t.cancel();
    }
  }

  // Lesende Aktion per GET. Leere Werte (undefined/null) fallen weg.
  function get(action, params = {}, timeoutMs = TIMEOUT.POLL) {
    let url = `${ENDPOINT}?action=${encodeURIComponent(action)}`;
    Object.keys(params || {}).forEach((key) => {
      const value = params[key];
      if (value === undefined || value === null) return;
      url += `&${encodeURIComponent(key)}=${encodeURIComponent(String(value))}`;
    });
    return request(url, {}, timeoutMs);
  }

  // Ändernde Aktion per POST (FormData + X-CSRF-Token). Bei einem CSRF-Fehler
  // holen die Empfänger von onCsrfError den Status neu - er bringt das
  // aktuelle Token mit. Bewusst KEIN automatischer Neuversuch: Die Person
  // bestätigt die Aktion noch einmal selbst (siehe errorText()).
  async function post(fields, timeoutMs = TIMEOUT.POST) {
    const form = new FormData();
    Object.keys(fields || {}).forEach((key) => {
      const value = fields[key];
      if (value === undefined || value === null) return;
      form.set(key, String(value));
    });
    try {
      return await request(ENDPOINT, {
        method: 'POST',
        headers: { 'X-CSRF-Token': csrf },
        body: form
      }, timeoutMs);
    } catch (err) {
      if (err instanceof ApiError && err.kind === 'csrf') notify(csrfErrorHandlers, err);
      throw err;
    }
  }

  // Text für einen CSRF-Fehler: Liefert der Server das Token mit dem Status
  // aus, ist es nach dem Neuladen des Status schon erneuert - sonst hilft nur
  // Neuladen der Seite.
  const csrfHint = () => (csrfFromServer
    ? 'Sicherheits-Token erneuert – bitte erneut bestätigen'
    : 'Sicherheits-Token veraltet – Seite neu laden');

  // Einheitliche Fehlermeldung für einfache Aktionen (Empfänger, Demo, TEST,
  // Abbrechen, Meldungen). REAL ALARM/ENTWARNUNG haben eigene, schärfere
  // Texte (store.sendNasAlarm).
  function errorText(err) {
    if (!(err instanceof ApiError)) return 'Anfrage fehlgeschlagen.';
    switch (err.kind) {
      case 'auth':
        return 'Sitzung abgelaufen – die Anmeldeseite wird geladen…';
      case 'csrf':
        return `${csrfHint()}. Aktion wurde NICHT ausgeführt.`;
      case 'readonly':
        return err.message || 'Nur Lesezugriff – Aktion wurde NICHT ausgeführt.';
      case 'mode_changed':
        return err.message || 'Der Demo-/LIVE-Modus hat sich geändert – Aktion wurde NICHT ausgeführt.';
      case 'timeout':
        return 'Keine Antwort vom Server (Zeitüberschreitung) – ob die Aktion ausgeführt wurde, ist unklar. Bitte Anzeige/Verlauf prüfen.';
      case 'network':
        return 'Server nicht erreichbar – Verbindung prüfen.';
      default:
        return `Fehler${err.status ? ` (HTTP ${err.status})` : ''}${err.message ? `: ${err.message}` : '.'}`;
    }
  }

  // Art der Rückmeldung: 'ok' = geklappt, 'error' = nicht ausgeführt,
  // 'warn' = unklar (Zeitüberschreitung - kann ausgeführt worden sein).
  const kindOf = (data) => (data && data.ok ? 'ok' : 'error');
  const errorKind = (err) => (err instanceof ApiError && err.kind === 'timeout' ? 'warn' : 'error');

  // ==== Polling mit "läuft noch"-Schutz =====================================
  // Antwortet das NAS mal langsamer als das Intervall, sollen sich die
  // Anfragen nicht stapeln. Zwei Arten von Aufrufen:
  //   run()       - AUSDRÜCKLICH angefordert (z.B. direkt nach einer Aktion):
  //                 Läuft gerade eine Abfrage, folgt EINMAL eine weitere
  //                 hinterher, statt verloren zu gehen. Das Promise erfüllt
  //                 sich erst nach einem Durchlauf, der NACH dem Aufruf
  //                 begonnen hat (also mit frischen Daten).
  //   run.tick()  - vom Intervall-Timer: Läuft gerade eine Abfrage, wird der
  //                 Takt einfach ausgelassen - sonst bekäme ein überlastetes
  //                 NAS pausenlos Abfrage an Abfrage.
  function makePoller(fn) {
    let busy = false;
    let rerun = false;
    let waiters = [];
    const start = async (fromTimer) => {
      if (busy) {
        if (fromTimer) return undefined;
        rerun = true;
        return new Promise((resolve) => { waiters.push(resolve); });
      }
      busy = true;
      try {
        await fn();
      } finally {
        busy = false;
        const again = rerun && !authLost;
        rerun = false;
        const done = waiters;
        waiters = [];
        const release = () => done.forEach((resolve) => resolve());
        if (again) start(false).then(release, release);
        else release();
      }
      return undefined;
    };
    const run = () => start(false);
    run.tick = () => start(true);
    return run;
  }

  FW.api = {
    ENDPOINT, TIMEOUT, ApiError,
    request, get, post,
    setCsrf, getCsrf, csrfHint, onAuthLoss, onCsrfError, isAuthLost,
    errorText, errorKind, kindOf, shortText,
    makePoller
  };
})(typeof window !== 'undefined' ? window : globalThis);
