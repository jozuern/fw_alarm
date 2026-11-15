(function () {
  // CSRF-Token der Sitzung: Startwert aus der Seite, danach aus jeder
  // Status-Antwort aktualisiert (let statt const) - sonst scheiterte ein alter
  // Tab nach einer Neuanmeldung in einem anderen Tab an einem veralteten Token.
  let csrf = document.querySelector('meta[name="csrf"]')?.content || '';
  // Liefert der Server das Token mit der Status-Antwort aus? (Ältere Stände
  // nicht - dann hilft bei einem CSRF-Fehler nur Neuladen.)
  let csrfFromServer = false;
  // Nur-Lese-Benutzer: Knöpfe fehlen dann im HTML; hier zusätzlich nichts
  // rendern, was Änderungen auslösen würde (der Server blockt ohnehin).
  const readOnly = (document.querySelector('meta[name="role"]')?.content || 'admin') !== 'admin';
  // Öffentliche Basis-Adresse für die Kurzbefehl-Links (aus config.php, über
  // index.php). Leer = aus der aktuellen Adresse ableiten.
  const publicBase = (document.querySelector('meta[name="public-base"]')?.content || '').trim();
  const stateEl = document.querySelector('[data-state]');
  const stateSubEl = document.querySelector('[data-state-sub]');
  const fieldsEl = document.querySelector('[data-fields]');
  const commandEl = document.querySelector('[data-command]');
  const updatedEl = document.querySelector('[data-updated]');
  const heroFactsEl = document.querySelector('[data-hero-facts]');
  const heroHintsEl = document.querySelector('[data-hero-hints]');
  const testBtn = document.querySelector('[data-command-test]');
  const alarmBtn = document.querySelector('[data-command-alarm]');
  const espAlarmBtn = document.querySelector('[data-command-esp-alarm]');
  const falseAlarmBtn = document.querySelector('[data-command-false-alarm]');
  const cancelBtn = document.querySelector('[data-command-cancel]');
  const alarmResultEl = document.querySelector('[data-alarm-result]');
  const keysListEl = document.querySelector('[data-keys-list]');
  const keysAddForm = document.querySelector('[data-keys-add]');
  const keysAddBtn = keysAddForm?.querySelector('button');
  const keysMsgEl = document.querySelector('[data-keys-msg]');
  const demoBannerEl = document.querySelector('[data-demo-banner]');
  const demoChipEl = document.querySelector('[data-demo-chip]');
  const demoDetailsEl = document.querySelector('[data-demo-details]');
  const demoSummaryEl = document.querySelector('[data-demo-summary]');
  const demoStateEl = document.querySelector('[data-demo-state]');
  const demoTargetEl = document.querySelector('[data-demo-target]');
  const demoToggleBtn = document.querySelector('[data-demo-toggle]');
  const demoMsgEl = document.querySelector('[data-demo-msg]');
  const commandMsgEl = document.querySelector('[data-command-msg]');
  const logEl = document.querySelector('[data-log]');
  const logMoreBtn = document.querySelector('[data-log-more]');
  const miniLampEl = document.querySelector('[data-state-mini]');
  const notifyPanelEl = document.querySelector('#meldungen');
  const notifyListEl = document.querySelector('[data-notify-list]');
  const notifyGlobalEl = document.querySelector('[data-notify-global]');
  const notifyMsgEl = document.querySelector('[data-notify-msg]');
  const notifyNoteEl = document.querySelector('[data-notify-note]');
  const notifySummaryEl = document.querySelector('[data-notify-summary]');

  // Bewusst "die Box" statt "ESP32" in allen sichtbaren Texten: So heißt sie
  // auch im Status-Panel und in den Bark-Meldungen des Wächters.
  const STATE_LABELS = {
    pending: 'wartet auf die Box',
    delivered: 'an die Box ausgeliefert, Bestätigung offen',
    acked: 'bestätigt',
    expired: 'abgelaufen (die Box hat ihn nicht abgeholt)',
    cancelled: 'abgebrochen'
  };

  // Befehls-Quittungen der Firmware sind feste englische Tokens/Sätze - hier
  // übersetzt, damit die Zeile nicht Denglisch ist. Unbekannte Werte
  // (künftige Firmware) erscheinen unverändert.
  const ACK_RESULTS = {
    executed: 'ausgeführt',
    skipped_busy: 'übersprungen – Box nicht auslösebereit',
    duplicate_ignored: 'Duplikat ignoriert',
    status_failed: 'Bark-Versand fehlgeschlagen',
    unknown_command: 'unbekannter Befehlstyp'
  };
  const ACK_MESSAGES = {
    'real alarm started': 'Alarm gestartet',
    'test status sent via Bark': 'Test-Meldung über Bark gesendet',
    'bark status send FAILED - check BARK_KEY_STATUS/WLAN': 'Bark-Versand fehlgeschlagen – Status-Key/WLAN prüfen',
    'alarm state machine blocked by cooldown or rearm': 'Cooldown/Wiederbewaffnung aktiv',
    'command id already handled in this boot': 'Befehls-ID wurde bereits verarbeitet',
    'unsupported command type': 'Befehlstyp unbekannt'
  };
  const fmtAckResult = (v) => ACK_RESULTS[v] || v;
  const fmtAckMessage = (v) => ACK_MESSAGES[v] || v;

  // Zuletzt bekannter Demo-/Versionsstand (wird von refresh()/refreshKeys()
  // aktualisiert und u.a. für den REAL-ALARM-Bestätigungstext gebraucht).
  let demoInfo = null;
  let nasKeysVersion = null;

  const PAGE_TITLE = document.title;

  // Zeitlimits je Anfrageart: Ohne Limit hält eine hängende Anfrage den
  // In-Flight-Guard ewig fest, und der Hero bliebe grün "Einsatzbereit".
  // REAL ALARM/ENTWARNUNG senden serverseitig an alle Empfänger (meist
  // ~35 s, schlimmstenfalls ~60 s) - deshalb dort großzügig. Ein einzelner
  // Bark-Versand (Test-Push, Probe-Meldung) braucht bis ~21 s.
  const TIMEOUT_POLL_MS = 10000;
  const TIMEOUT_POST_MS = 20000;
  const TIMEOUT_SEND_ONE_MS = 30000;
  const TIMEOUT_ALARM_MS = 120000;
  // So lange darf die letzte erfolgreiche Status-Abfrage höchstens her sein,
  // bevor der Hero auf "Status veraltet" (gelb) wechselt.
  const STALE_AFTER_MS = 15000;
  // Alle Zeitangaben in der Zeitzone der Wache - egal, wie die Uhr des
  // anzeigenden Geräts eingestellt ist. Der Server meldet sie mit der
  // Status-Antwort (timezone); bis dahin bzw. bei älteren Ständen gilt Berlin.
  let TZ = 'Europe/Berlin';

  // Zeitzone aus der Status-Antwort übernehmen - nur, wenn der Browser sie
  // kennt (ein unbekannter Name ließe sonst jede Zeitformatierung scheitern).
  function applyTimezone(name) {
    if (typeof name !== 'string' || !name || name === TZ) return;
    try {
      new Intl.DateTimeFormat('de-DE', { timeZone: name });
      TZ = name;
    } catch (err) {
      // unbekannte Zeitzone -> bisherige behalten
    }
  }

  // Große Statuslampe im Held-Bereich + Mini-Lampe in der Kopfzeile in einem
  // Rutsch setzen. cls: 'ok' (grün), 'stale' (gelb = Aussage unsicher bzw.
  // eingeschränkt), 'crit' (rot, ruhig) oder 'alarm' (rot, blinkend - die
  // Zustellung läuft gerade). sub = ein Satz darunter: was der Zustand
  // bedeutet bzw. was jetzt zu tun ist.
  function setState(word, cls, sub = '') {
    // Nur bei echter Änderung schreiben: [data-state] ist eine aria-live-Region,
    // und identischer Text alle 5 s würde Screenreader unnötig erneut ansagen.
    if (stateEl.textContent !== word) stateEl.textContent = word;
    const stateClass = 'state state-' + cls;
    if (stateEl.className !== stateClass) stateEl.className = stateClass;
    if (stateSubEl) {
      if (stateSubEl.textContent !== sub) stateSubEl.textContent = sub;
      stateSubEl.hidden = sub === '';
    }
    if (miniLampEl) {
      const miniClass = 'mini-lamp mini-' + cls;
      if (miniLampEl.className !== miniClass) miniLampEl.className = miniClass;
    }
  }

  // Sitzung serverseitig abgelaufen (HTTP 401): deutlich machen statt still
  // mit veralteten Daten weiterzulaufen, dann zur Login-Seite neu laden.
  // Läuft gerade ein REAL ALARM/eine ENTWARNUNG, wird NICHT neu geladen: Die
  // Anfrage war schon angemeldet und läuft auf dem NAS weiter - ihr Ergebnis
  // (wer NICHT erreicht wurde) muss lesbar bleiben. Dann nur ein Hinweis.
  let authLost = false;
  function handleAuthLoss() {
    if (authLost) return;
    authLost = true;
    stopPolling();
    document.title = '\u{1F512} ' + PAGE_TITLE;
    setState('Abgemeldet', 'stale');
    const keepText = 'Sitzung abgelaufen – bitte nach dem Lesen des Alarm-Ergebnisses die Seite neu laden (Anmeldung).';
    if (alarmInFlight) {
      updatedEl.textContent = keepText;
      return;
    }
    updatedEl.textContent = 'Sitzung abgelaufen – die Anmeldeseite wird geladen…';
    setTimeout(() => {
      // Inzwischen doch ein Versand gestartet? Dann ebenfalls stehen lassen.
      if (alarmInFlight) {
        updatedEl.textContent = keepText;
        return;
      }
      location.reload();
    }, 1500);
  }

  function formatTime(epoch) {
    if (!epoch) return 'nie';
    return new Date(epoch * 1000).toLocaleString('de-DE', { timeZone: TZ });
  }

  // Nur die Uhrzeit (HH:MM) - für "Stumm bis ~18:30" und den Verlauf.
  function fmtClock(epoch) {
    return new Date(epoch * 1000).toLocaleTimeString('de-DE', { hour: '2-digit', minute: '2-digit', timeZone: TZ });
  }

  // Uhrzeit mit Sekunden aus einem Browser-Zeitstempel (ms) - für "zuletzt
  // erfolgreich abgerufen um 14:03:22".
  function fmtClockMs(ms) {
    return new Date(ms).toLocaleTimeString('de-DE', { timeZone: TZ });
  }

  // Kalendertag in der Zeitzone der Wache als sortierbarer Schlüssel
  // ("2026-11-13") - zum Gruppieren des Verlaufs und für "heute/gestern".
  const dayKey = (epoch) => new Date(epoch * 1000).toLocaleDateString('sv-SE', { timeZone: TZ });

  // Kurzes Datum + Uhrzeit ohne Komma ("13.11. 23:40") - für Fließtext.
  function fmtDayClock(epoch) {
    const day = new Date(epoch * 1000).toLocaleDateString('de-DE', { day: '2-digit', month: '2-digit', timeZone: TZ });
    return `${day} ${fmtClock(epoch)}`;
  }

  // Tages-Überschrift im Verlauf: "Heute", "Gestern", sonst Wochentag + Datum.
  function fmtDayLabel(epoch, now) {
    const key = dayKey(epoch);
    if (key === dayKey(now)) return 'Heute';
    if (key === dayKey(now - 86400)) return 'Gestern';
    return new Date(epoch * 1000).toLocaleDateString('de-DE',
      { weekday: 'long', day: '2-digit', month: '2-digit', year: 'numeric', timeZone: TZ });
  }

  // "heute 21:42 Uhr" / "gestern 21:42 Uhr" / "12.11. 21:42 Uhr".
  function fmtDayTime(epoch, now) {
    const key = dayKey(epoch);
    if (key === dayKey(now)) return `heute ${fmtClock(epoch)} Uhr`;
    if (key === dayKey(now - 86400)) return `gestern ${fmtClock(epoch)} Uhr`;
    return `${fmtDayClock(epoch)} Uhr`;
  }

  // Relative Zeit für junge Ereignisse ("vor 5 min"); ältere bekommen das
  // absolute Datum. Rechnet mit der Server-Zeit, nicht der Browser-Uhr.
  function fmtRelTime(epoch, serverTime) {
    if (!epoch) return 'nie';
    const age = serverTime - epoch;
    if (!Number.isFinite(age) || age < 0) return formatTime(epoch);
    if (age < 90) return 'gerade eben';
    if (age < 3600) return `vor ${Math.round(age / 60)} min`;
    if (age < 86400) return `vor ${Math.round(age / 3600)} Std`;
    return formatTime(epoch);
  }

  // Alter des letzten Status für die Hero-Zeile: junge Werte sekundengenau
  // (da sieht man das 5-s-Polling arbeiten), ältere menschenlesbar über
  // fmtUptime statt als roher Sekundenwert ("vor 21 Min" statt "1260 s").
  function fmtAgeShort(age) {
    if (!Number.isFinite(age) || age < 0) return null;
    if (age < 90) return `vor ${Math.round(age)} s`;
    return `vor ${fmtUptime(age)}`;
  }

  function escapeHtml(value) {
    return String(value).replace(/[&<>"']/g, (char) => ({
      '&': '&amp;',
      '<': '&lt;',
      '>': '&gt;',
      '"': '&quot;',
      "'": '&#039;'
    })[char]);
  }

  // Laufende Aktionen als ZÄHLER statt als Ja/Nein: Laufen zwei Aktionen
  // überlappend (z.B. Stummschalten während eines REAL ALARM), darf das
  // Ende der kürzeren die Knöpfe nicht wieder freigeben, solange die längere
  // noch läuft. Auch renderDemo() fragt den Zähler ab (Doppelklick-Schutz).
  let busyCount = 0;
  // Eigene Sperre für die Alarm-Knöpfe: Solange eine Alarm-/Entwarnungs-
  // Anfrage läuft, bleiben REAL ALARM, ENTWARNUNG und DEMO-ALARM gesperrt -
  // unabhängig davon, was andere Aktionen am Zähler machen.
  let alarmInFlight = false;
  // "Nur an die nicht Erreichten erneut senden" im Ergebnis-Kasten - wird wie
  // die Alarm-Knöpfe gesperrt, solange etwas läuft.
  let retryBtn = null;

  function applyBusy() {
    const busy = busyCount > 0;
    // keysAddBtn ist mit dabei, damit ein Doppelklick auf "Hinzufügen" nicht
    // zwei Anfragen abschickt (die zweite endet sonst in einer verwirrenden
    // "bereits eingetragen"-Meldung).
    [testBtn, cancelBtn, keysAddBtn].forEach((btn) => {
      if (btn) btn.disabled = busy;
    });
    // Den Demo-Knopf erst freigeben, wenn der Demo-Zustand bekannt ist.
    if (demoToggleBtn) demoToggleBtn.disabled = busy || !demoInfo;
    // REAL ALARM, ENTWARNUNG und die Nachsendung hängen NUR an alarmInFlight
    // (Doppelklick-Schutz): Ein langsamer Test-Push oder eine hängende
    // Listen-Aktion darf den Alarm nicht bis zu 30 s sperren. Der DEMO-Alarm
    // über die Box teilt sich die Befehls-Warteschlange mit TEST und bleibt
    // deshalb auch an den Zähler gekoppelt.
    [alarmBtn, falseAlarmBtn, retryBtn].forEach((btn) => {
      if (btn) btn.disabled = alarmInFlight;
    });
    if (espAlarmBtn) espAlarmBtn.disabled = busy || alarmInFlight;
  }

  function setBusy(busy) {
    busyCount = Math.max(0, busyCount + (busy ? 1 : -1));
    applyBusy();
  }

  // Flag-Werte kommen von der Box als '0'/'1'-Strings.
  const flagged = (v) => v === '1' || v === 1;

  // innerHTML nur ersetzen, wenn sich wirklich etwas geändert hat: Sonst
  // klappt alle 5 s ein gerade geöffneter Tooltip zu, Fokus und Textauswahl
  // gehen verloren, und die Seite flackert unnötig.
  // Auch bei einer ECHTEN Änderung (RSSI/Laufzeit ändern sich ständig) bleibt
  // der Bedienzustand erhalten: Geöffnete ?-Sprechblasen, aufgeklappte
  // <details> und der Tastatur-Fokus werden über das stabile data-fkey-
  // Attribut gemerkt und im neuen Baum wiederhergestellt.
  function setHtml(el, html) {
    if (!el || el.__lastHtml === html) return;
    const keysOf = (selector) => Array.from(el.querySelectorAll(selector), (n) => n.dataset.fkey);
    const openTips = keysOf('.help.tip-open[data-fkey]');
    const openDetails = keysOf('details[open][data-fkey]');
    const active = document.activeElement;
    const focusKey = active && active !== el && el.contains(active) && active.dataset
      ? (active.dataset.fkey || null) : null;
    el.__lastHtml = html;
    el.innerHTML = html;
    const find = (key) => el.querySelector(`[data-fkey="${CSS.escape(key)}"]`);
    openTips.forEach((key) => { const help = find(key); if (help) setTipOpen(help, true); });
    // <details> VOR dem Fokus öffnen: In einem zugeklappten <details> lässt
    // sich nichts fokussieren.
    openDetails.forEach((key) => { const d = find(key); if (d) d.open = true; });
    if (focusKey) {
      const target = find(focusKey);
      if (target) target.focus({ preventScroll: true });
    }
  }

  // setHtml() überspringen lassen gilt nur, solange niemand außer setHtml()
  // den Inhalt verändert hat. Hat die Person ein Bedienelement umgestellt und
  // das Speichern ist gescheitert, muss trotz gleichem HTML neu gezeichnet
  // werden (null statt undefined: undefined heißt "noch nie geladen").
  function forceRedraw(el, render) {
    if (el) el.__lastHtml = null;
    render();
  }

  // ==== Rückmeldungen =======================================================
  // EIN Muster für alle Aktionen: Die Meldung steht direkt bei dem, was sie
  // ausgelöst hat, und zeigt per Farbe UND Zeichen (✓ ✕ !, siehe style.css),
  // wie es ausging: 'ok' = geklappt, 'error' = nicht ausgeführt, 'warn' =
  // unklar (z.B. Zeitüberschreitung), 'info' = läuft noch. Erfolg verschwindet
  // nach kurzer Zeit von selbst - Fehler und Unklares bleiben stehen, bis die
  // nächste Aktion sie ersetzt. Früher standen alle Meldungen grau am Panel-
  // Ende (teils außer Sicht) und blieben für immer stehen.

  // Lesezeit: mindestens 8 s, bei langen Texten entsprechend mehr.
  const lingerMs = (text) => Math.max(8000, String(text).trim().split(/\s+/).length * 450);

  function showMsg(el, text, kind = 'info') {
    if (!el) return;
    clearTimeout(el.__msgTimer);
    el.textContent = text || '';
    el.className = 'msg' + (text ? ' msg-' + kind : '');
    if (text && kind === 'ok') {
      el.__msgTimer = setTimeout(() => {
        el.textContent = '';
        el.className = 'msg';
      }, lingerMs(text));
    }
  }

  // Art der Rückmeldung aus einer Server-Antwort bzw. einem Fehler ableiten.
  const kindOf = (data) => (data && data.ok ? 'ok' : 'error');
  const errorKind = (err) => (err instanceof ApiError && err.kind === 'timeout' ? 'warn' : 'error');

  // Unsichtbare Live-Region für Screenreader: Die Zeilen-Rückmeldungen
  // entstehen per innerHTML bei jedem Neuzeichnen neu - ein darin
  // eingebettetes role="status" wird je nach Screenreader gar nicht oder
  // mehrfach angesagt. Deshalb wird jede Zeilen-Meldung zusätzlich hier
  // hinein gespiegelt (sichtbar bleibt sie nur in ihrer Zeile). index.php hat
  // keine passende unsichtbare Region, darum legt app.js sie selbst an.
  const srLiveEl = (() => {
    const node = document.createElement('p');
    node.className = 'sr-only';
    node.setAttribute('role', 'status');
    node.setAttribute('aria-live', 'polite');
    document.body.append(node);
    return node;
  })();
  let srLiveTimer = null;
  function announce(text) {
    clearTimeout(srLiveTimer);
    // Erst leeren, dann (kurz danach) setzen: So wird auch derselbe Text ein
    // zweites Mal angesagt.
    srLiveEl.textContent = '';
    if (text) srLiveTimer = setTimeout(() => { srLiveEl.textContent = text; }, 60);
  }

  // Rückmeldung an EINER Listenzeile (Empfänger, Art von Meldung): Die Listen
  // werden per innerHTML neu aufgebaut, deshalb lebt die Meldung als Zustand
  // und wird beim Zeichnen über html(id) mit eingesetzt. Angesagt wird sie
  // über announce() (siehe oben), nicht über die Zeile selbst.
  function makeRowMsg(container, render) {
    let current = null;
    let timer = null;
    return {
      html(id) {
        return current && current.id === String(id)
          ? `<p class="msg msg-${current.kind}">${escapeHtml(current.text)}</p>` : '';
      },
      set(id, text, kind) {
        clearTimeout(timer);
        current = text ? { id: String(id), text, kind } : null;
        // Das Zeichen (✓ ✕ !) steht nur im CSS - für die Ansage als Wort.
        if (text) announce(({ error: 'Fehler: ', warn: 'Achtung: ' }[kind] || '') + text);
        render();
        if (current && kind === 'ok') {
          const clear = () => {
            // Offene Bestätigungsfrage in der Liste? Dann nicht dazwischen
            // neu zeichnen (sie würde kommentarlos verschwinden).
            if (container.querySelector('.arm-confirm')) {
              timer = setTimeout(clear, 2000);
              return;
            }
            current = null;
            render();
          };
          timer = setTimeout(clear, lingerMs(text));
        }
      }
    };
  }

  // ==== Statusfelder: Formatierung + Erklärungen ============================
  // Die Rohwerte kommen 1:1 von der Box (0/1, open/closed, Sekunden). Hier
  // werden sie in verständliche Anzeigen übersetzt; das ?-Symbol zeigt beim
  // Überfahren bzw. Antippen die Erklärung zu jedem Wert.

  const na = (v) => v === undefined || v === null || v === '';
  const show = (v) => na(v) ? 'n/a' : String(v);

  function fmtUptime(value) {
    const s = parseInt(value, 10);
    if (isNaN(s)) return value;
    const d = Math.floor(s / 86400);
    const h = Math.floor((s % 86400) / 3600);
    const m = Math.floor((s % 3600) / 60);
    if (d > 0) return `${d} T ${h} Std ${m} Min`;
    if (h > 0) return `${h} Std ${m} Min`;
    return `${m} Min`;
  }

  function fmtRssi(value) {
    const dbm = parseInt(value, 10);
    if (isNaN(dbm)) return value;
    let quality = 'sehr schwach';
    if (dbm >= -55) quality = 'sehr gut';
    else if (dbm >= -67) quality = 'gut';
    else if (dbm >= -75) quality = 'mittel';
    else if (dbm >= -85) quality = 'schwach';
    return `${dbm} dBm (${quality})`;
  }

  // Neustart-Gründe der Box (esp_reset_reason). Watchdog/Absturz/Brownout
  // sind Warnzeichen - Einschalten und Software-Neustart sind normal.
  const RESET_REASONS = {
    poweron: 'Einschalten (normal)',
    software: 'Software-Neustart (normal)',
    external: 'Reset-Taster/extern',
    panic: 'ABSTURZ (Panic)',
    task_wdt: 'WATCHDOG-Reset (Task)',
    int_wdt: 'WATCHDOG-Reset (Interrupt)',
    wdt: 'WATCHDOG-Reset',
    brownout: 'BROWNOUT (Spannungseinbruch!)',
    deepsleep: 'Aufwachen aus Deep-Sleep',
    unknown: 'unbekannt'
  };
  const RESET_WARN = ['panic', 'task_wdt', 'int_wdt', 'wdt', 'brownout'];

  function fmtHeap(value) {
    const bytes = parseInt(value, 10);
    if (isNaN(bytes)) return value;
    return `${Math.round(bytes / 1024)} kB frei`;
  }

  // Warnschwellen für den Speicher: insgesamt frei bzw. größter freier Block
  // (ein zerstückelter Heap lässt TLS-Verbindungen scheitern, obwohl in Summe
  // noch genug frei ist).
  // ACHTUNG, doppelt gepflegt: Dieselben Schwellen (HEAP_WARN, MAX_ALLOC_WARN,
  // weakWifi -80 dBm, heartbeatFailed) prüft der Wächter-Cron für die Meldung
  // "box_health" (nas_dashboard/cron/check_offline.php) - Änderungen immer an
  // BEIDEN Stellen nachziehen.
  const HEAP_WARN = 50000;
  const MAX_ALLOC_WARN = 30000;
  const lowBytes = (v, limit) => { const b = parseInt(v, 10); return !isNaN(b) && b < limit; };
  const weakWifi = (s) => { const dbm = parseInt(s.rssi, 10); return !isNaN(dbm) && dbm <= -80; };
  // heartbeat_ok=0 heißt nur dann "gescheitert", wenn es schon einen Versuch
  // gab: Nach einem Neustart meldet die Box 0 und "nie", bis der erste
  // Heartbeat fällig ist - das ist kein Fehler (gleiche Regel im Wächter-Cron).
  const heartbeatFailed = (s) => !na(s.heartbeat_ok) && !flagged(s.heartbeat_ok)
    && !na(s.last_heartbeat) && s.last_heartbeat !== 'nie';

  // Zustellung des letzten Alarms der Box unvollständig? key_progress ist
  // "erreicht/gesamt". Gilt nur, wenn seit dem Neustart ein Alarm war und die
  // Box nicht mehr nachsendet (alarm_pending=0) - dann bleibt der Rest ohne
  // Alarm. Neuere Firmware meldet das zusätzlich ausdrücklich (alarm_gaveup=1:
  // Nachsende-Frist abgelaufen, Keys dauerhaft abgelehnt oder gar keine
  // Empfänger) und zählt gegen die Liste zum Zeitpunkt des Alarms. Ältere
  // Firmware zählt gegen ihre AKTUELLE Liste; wurde die Liste nach dem Alarm
  // geändert, kann sich die Gesamtzahl dort verschoben haben.
  function keyProgressParts(s) {
    const match = /^(\d+)\s*\/\s*(\d+)$/.exec(String(s.key_progress ?? '').trim());
    return match ? [Number(match[1]), Number(match[2])] : null;
  }
  const deliveryIncomplete = (s) => {
    if (flagged(s.alarm_pending) || na(s.last_alarm) || s.last_alarm === 'nie') return false;
    if (flagged(s.alarm_gaveup)) return true;
    const parts = keyProgressParts(s);
    return !!parts && parts[0] < parts[1];
  };

  // Woher kam der letzte Alarm? (Feld alarm_source - meldet erst neuere
  // Firmware; bei älterer bleibt es leer.)
  const ALARM_SOURCES = {
    relay: 'Melder-Relais',
    remote: 'DEMO-Alarm über die Box (Dashboard-Befehl)'
  };

  // Alles, was die Box meldet, wird angezeigt - aber VERDICHTET: Die 22
  // Rohwerte des Status-Pushs werden zu 12 verständlichen Zeilen in drei
  // Gruppen zusammengefasst (z.B. Signalstärke + IP = eine WLAN-Zeile).
  // value() baut den Anzeigetext, cls() liefert ''/'warn'/'crit' für die
  // Warnfarbe; ctx = { offline, seenAge } für Sonderfälle (Offline-Anzeige).
  // hint(s) = true: Die Auffälligkeit gehört zusätzlich als Hinweis in den
  // Held-Bereich ganz oben - sonst sähe man sie nur, wenn man dieses Panel
  // Zeile für Zeile liest.
  const ROWS = [
    { group: 'Alarmkette', label: 'Melder-Kontakt',
      tip: 'Zustand des Relaiskontakts am Swissphone-Lader: "offen" = Ruhe, "GESCHLOSSEN" = Alarmkontakt liegt an. Klemmt der Kontakt ungewöhnlich lange, meldet die Box einmalig "Kontakt klemmt?" an den Status-Empfänger.',
      value: (s) => {
        if (na(s.relay)) return 'n/a';
        if (s.relay === 'open') return 'offen (Ruhe)';
        if (s.relay !== 'closed') return String(s.relay);
        return flagged(s.stuck_warned) ? 'GESCHLOSSEN – klemmt? (Warnung gesendet)' : 'GESCHLOSSEN (Alarm!)';
      },
      cls: (s) => s.relay === 'closed' ? 'crit' : '' },
    { group: 'Alarmkette', label: 'Auslösebereit',
      tip: 'Kann die Box gerade einen NEUEN Alarm auslösen? Nach einem Alarm gilt ein Mindestabstand (Cooldown, 5 Min) und der Kontakt muss erst wieder stabil offen gewesen sein (Wiederbewaffnung) – das verhindert Alarm-Spam bei flatterndem Kontakt. "Impuls wird verarbeitet" heißt: Ein erkannter Alarm-Impuls wartet gerade kurz auf die Hauptschleife.',
      value: (s) => {
        if (na(s.cooldown) && na(s.waiting_for_release)) return 'n/a';
        if (flagged(s.alarm_pending)) return 'Nein – Alarm läuft noch';
        if (flagged(s.isr_latched)) return 'gleich – Impuls wird verarbeitet';
        if (flagged(s.waiting_for_release)) return 'Nein – wartet auf Kontakt-Öffnung';
        if (flagged(s.cooldown)) return 'Nein – Cooldown läuft';
        return 'Ja';
      },
      cls: (s) => (flagged(s.alarm_pending) || flagged(s.isr_latched)
        || flagged(s.waiting_for_release) || flagged(s.cooldown)) ? 'warn' : '' },
    { group: 'Alarmkette', label: 'Zustellung',
      tip: 'Wie viele Empfänger beim letzten/laufenden Alarm erfolgreich benachrichtigt wurden (erreicht/gesamt). Läuft die Zustellung noch, versucht die Box automatisch weiter, bis alle versorgt sind (Nachsende-Puffer). Gelb heißt: Die Box sendet nicht mehr nach, aber nicht alle wurden erreicht – die übrigen ggf. per „REAL ALARM“ bzw. telefonisch verständigen. Achtung: Die Box zählt gegen ihre AKTUELLE Empfängerliste – wurde die Liste nach dem Alarm geändert (auch ein Demo-Wechsel), kann sich die Gesamtzahl verschoben haben.',
      value: (s) => {
        if (flagged(s.alarm_pending)) return `läuft – bisher ${show(s.key_progress)}`;
        if (na(s.last_alarm) || s.last_alarm === 'nie') return '–';
        if (deliveryIncomplete(s)) return `nur ${show(s.key_progress)} erreicht – NICHT alle`;
        return `${show(s.key_progress)} erreicht`;
      },
      cls: (s) => {
        if (flagged(s.alarm_pending)) return 'crit';
        return deliveryIncomplete(s) ? 'warn' : '';
      },
      hint: (s) => deliveryIncomplete(s),
      // Eigener Text für den Held-Bereich: dort zählt, was jetzt zu tun ist.
      hintText: (s) => `Nicht alle Empfänger erreicht (letzter Alarm der Box: ${show(s.key_progress)}) – ${readOnly
        ? 'ggf. einen Admin verständigen, er kann per „REAL ALARM“ nachsenden'
        : 'ggf. per „REAL ALARM“ nachsenden bzw. die übrigen anders verständigen'}. Wurde die Empfängerliste seit dem Alarm geändert, kann die Zählung verschoben sein.` },
    { group: 'Alarmkette', label: 'Letzter Alarm',
      tip: 'Zeitpunkt und Text des letzten ausgelösten Alarms aus Sicht der Box (seit ihrem letzten Neustart) und – falls die Firmware es meldet – woher er kam: Melder-Relais oder DEMO-Alarm per Dashboard-Befehl. "nie" heißt: kein Alarm seit dem letzten Einschalten. Den dauerhaften Verlauf führt das NAS (Panel "Verlauf").',
      value: (s) => {
        const text = show(s.last_alarm);
        if (na(s.alarm_source) || na(s.last_alarm) || s.last_alarm === 'nie') return text;
        return `${text} · ${ALARM_SOURCES[s.alarm_source] || String(s.alarm_source)}`;
      } },
    { group: 'Verbindung & Abgleich', label: 'WLAN',
      tip: 'Empfangsstärke der Box im WLAN des Magazins und ihre aktuelle Adresse. Näher an 0 dBm ist besser: ab ca. -67 gut, unter ca. -80 unzuverlässig – dann Box oder Router umstellen. Ohne WLAN kann die Box weder Bark-Alarme senden noch das NAS erreichen.',
      value: (s) => {
        if (s.wifi === 'down') return 'GETRENNT';
        if (na(s.rssi)) return 'n/a';
        return `${fmtRssi(s.rssi)} · ${show(s.ip)}`;
      },
      cls: (s) => {
        if (s.wifi === 'down') return 'crit';
        return weakWifi(s) ? 'warn' : '';
      },
      hint: (s) => s.wifi === 'down' || weakWifi(s) },
    { group: 'Verbindung & Abgleich', label: 'Empfängerliste (Box)',
      tip: 'Empfängerliste, wie sie die Box gespeichert hat: Anzahl, davon stumm (Arbeitsmodus) und die Listen-Version. Stimmt die Version mit dem NAS überein, ist die Liste (auch ein Demo-Wechsel) angekommen. Im Demo-Modus steht hier genau 1 Empfänger.',
      value: (s) => {
        if (na(s.keys_count)) return 'n/a';
        let text = `${s.keys_count} Empfänger`;
        const muted = parseInt(s.keys_muted, 10);
        if (!isNaN(muted) && muted > 0) text += ` (${muted} stumm)`;
        if (!na(s.keys_version)) {
          text += ` · v${s.keys_version}`;
          if (nasKeysVersion !== null) {
            text += String(s.keys_version) === String(nasKeysVersion)
              ? ' (= NAS)'
              : ` (NAS: v${nasKeysVersion} – Übernahme steht aus)`;
          }
        }
        return text;
      },
      cls: (s) => (!na(s.keys_version) && nasKeysVersion !== null
        && String(s.keys_version) !== String(nasKeysVersion)) ? 'warn' : '' },
    { group: 'Verbindung & Abgleich', label: 'Täglicher Heartbeat',
      tip: 'Die Box meldet sich 1x täglich leise beim Status-Empfänger ("Ich lebe noch"). Hier steht der letzte Versuch; schlug er fehl, probiert die Box es automatisch erneut.',
      value: (s) => {
        if (na(s.last_heartbeat)) return 'n/a';
        return String(s.last_heartbeat) + (heartbeatFailed(s) ? ' – letzter Versuch fehlgeschlagen' : '');
      },
      cls: (s) => heartbeatFailed(s) ? 'warn' : '',
      hint: (s) => heartbeatFailed(s) },
    { group: 'Verbindung & Abgleich', label: 'Befehls-Quittung',
      tip: 'Hat die Box einen Befehl aus dem Dashboard ausgeführt, dessen Bestätigung noch nicht beim NAS angekommen ist? Normalerweise "keine offen" – ein Rückstand wird automatisch nachgereicht.',
      value: (s) => na(s.ack_pending) ? 'n/a'
        : (flagged(s.ack_pending) ? 'offen – wird nachgereicht' : 'keine offen'),
      cls: (s) => flagged(s.ack_pending) ? 'warn' : '' },
    { group: 'Box & Wartung', label: 'Box / Firmware',
      tip: 'Name der Box (device_id aus config.h) und die Firmware-Kennung FW_BUILD_MARKER aus config.h. Neuere Firmware hängt automatisch den Kompilier-Zeitpunkt an und meldet die Version des Arduino-ESP32-Cores – daran sieht man, ob wirklich der neueste Stand läuft. Bei älterer Firmware ist die Kennung ein frei gewählter Text, der nur etwas aussagt, wenn er bei jeder neuen Firmware angepasst wurde.',
      value: (s) => (na(s.device_id) && na(s.fw_build)) ? 'n/a'
        : `${show(s.device_id)} · ${show(s.fw_build)}${na(s.core) ? '' : ` · Core ${s.core}`}` },
    { group: 'Box & Wartung', liveWhenOffline: true,
      label: (ctx) => ctx.offline ? 'Offline seit' : 'Läuft seit',
      tip: (ctx) => ctx.offline
        ? 'Zeit seit dem letzten Lebenszeichen der Box beim NAS. Mögliche Ursachen: Stromausfall, WLAN weg oder NAS nicht erreichbar.'
        : 'Zeit seit dem letzten Neustart der Box. Ein unerwarteter Sprung auf wenige Minuten bedeutet: Die Box hat neu gestartet (z.B. Stromausfall oder Watchdog).',
      value: (s, ctx) => ctx.offline
        ? (Number.isFinite(ctx.seenAge) ? fmtUptime(ctx.seenAge) : 'unbekannt (noch nie gemeldet)')
        : (na(s.uptime_s) ? 'n/a' : fmtUptime(s.uptime_s)),
      cls: (s, ctx) => ctx.offline ? 'crit' : '' },
    { group: 'Box & Wartung', label: 'Letzter Neustart-Grund',
      tip: 'Warum die Box zuletzt neu gestartet ist. "Einschalten"/"Software-Neustart" sind normal. Watchdog/Absturz deuten auf ein Firmware-Problem, Brownout auf ein Netzteil-/Kabelproblem hin.',
      value: (s) => na(s.reset_reason) ? 'n/a' : (RESET_REASONS[s.reset_reason] || String(s.reset_reason)),
      cls: (s) => RESET_WARN.includes(String(s.reset_reason)) ? 'warn' : '',
      // Oben nur am ersten Tag nach dem Neustart - der Grund bleibt sonst
      // wochenlang stehen und würde als Dauer-Hinweis nur noch übersehen.
      hint: (s) => RESET_WARN.includes(String(s.reset_reason)) && parseInt(s.uptime_s, 10) < 86400 },
    { group: 'Box & Wartung', label: 'Freier Speicher',
      tip: 'Freier Arbeitsspeicher (Heap) der Box. Normal sind grob 150–250 kB. Sinkt der Wert über Tage stetig, deutet das auf ein Speicherleck hin; unter ~50 kB wird es kritisch. "Größter Block" (nur, falls die Firmware ihn meldet) ist das größte zusammenhängende freie Stück – unter ~30 kB können HTTPS-Verbindungen scheitern, obwohl in Summe noch genug frei ist.',
      value: (s) => {
        if (na(s.free_heap)) return 'n/a';
        const maxAlloc = parseInt(s.max_alloc_heap, 10);
        return fmtHeap(s.free_heap) + (isNaN(maxAlloc) ? '' : ` · größter Block ${Math.round(maxAlloc / 1024)} kB`);
      },
      cls: (s) => (lowBytes(s.free_heap, HEAP_WARN) || lowBytes(s.max_alloc_heap, MAX_ALLOC_WARN)) ? 'warn' : '',
      hint: (s) => lowBytes(s.free_heap, HEAP_WARN) || lowBytes(s.max_alloc_heap, MAX_ALLOC_WARN) },
    // NAS-seitig, nicht Box-seitig: Der Wächter (DSM-Aufgabe) muss auch dann
    // laufen, wenn die Box offline ist - deshalb liveWhenOffline. Sein
    // Hinweis für den Held-Bereich kommt direkt aus watchdogState().
    { group: 'Box & Wartung', label: 'Offline-Wächter (NAS)', liveWhenOffline: true,
      tip: 'Die DSM-Aufgabe auf dem NAS prüft alle 15 Minuten, ob die Box sich noch meldet, und schickt sonst eine Bark-Warnung. Hier steht, ob diese Aufgabe selbst noch läuft – der Wächter darf nicht unbemerkt ausfallen. Welche Meldungen er schickt, stellt der Admin im Panel "Meldungen an den Admin" ein.',
      value: (s, ctx) => ctx.watchdog ? ctx.watchdog.text : 'n/a',
      cls: (s, ctx) => ctx.watchdog && ctx.watchdog.warn ? 'warn' : '' },
    // Der Gegenpart von außen: eine Cloud-VM prüft alle 5 min, ob das NAS
    // erreichbar ist. Bewertet in outsideState(), hängt an ctx.watchdog.
    { group: 'Box & Wartung', label: 'Außen-Wächter (Cloud)', liveWhenOffline: true,
      tip: 'Eine kleine Cloud-VM ruft das NAS alle 5 Minuten von außen auf und meldet per Bark, wenn es nicht erreichbar ist (Strom oder Internet am Standort weg, Router, Zertifikat) oder wenn die DSM-Aufgabe nicht mehr läuft. Das NAS selbst kann seinen eigenen Ausfall nicht melden. Hier steht, wann sich die VM zuletzt gemeldet hat.',
      value: (s, ctx) => ctx.watchdog && ctx.watchdog.outside ? ctx.watchdog.outside.text : 'nicht eingerichtet',
      cls: (s, ctx) => ctx.watchdog && ctx.watchdog.outside && ctx.watchdog.outside.warn ? 'warn' : '' }
  ];

  const rowLabel = (row, ctx) => (typeof row.label === 'function' ? row.label(ctx) : row.label);

  // nasNote: gesetzt, wenn das NAS selbst gerade keine aktuellen Daten
  // liefert (nicht erreichbar/Störung/veraltet) - dann ist ALLES nur der
  // letzte bekannte Stand, auch "Offline seit" und der Wächter.
  function renderFields(status, offline, seenAge, watchdog, nasNote) {
    const ctx = { offline, seenAge, watchdog };
    // Zeilen nach Gruppen bündeln; jede Gruppe wird eine eigene Spalte.
    const groups = [];
    let current = null;
    ROWS.forEach((row) => {
      if (!current || current.name !== row.group) {
        current = { name: row.group, rows: [] };
        groups.push(current);
      }
      const label = rowLabel(row, ctx);
      const tip = typeof row.tip === 'function' ? row.tip(ctx) : row.tip;
      const value = row.value(status, ctx);
      const cls = row.cls ? (row.cls(status, ctx) || '') : '';
      // Offline gilt: Alle Box-Werte sind nur der letzte bekannte Stand und
      // werden gedimmt - außer den Zeilen, die weiterhin aktuell sind
      // ("Offline seit" und der NAS-seitige Wächter).
      const liveCls = offline && !nasNote && row.liveWhenOffline ? ' tele-current' : '';
      // Der Erklärtext ist ein ECHTES Element (nicht CSS-::after): Nur so
      // liest ein Screenreader ihn über aria-describedby auch wirklich vor.
      // Die tipId hängt nur an der (festen) Position in ROWS und dient
      // zugleich als data-fkey, damit setHtml() offene Blase/Fokus erhält.
      // .help ist die (auf dem Handy 44 px große) Tippfläche, .help-dot der
      // sichtbare kleine Kreis darin.
      const tipId = `tip-${groups.length}-${current.rows.length}`;
      current.rows.push(`<div class="tele-row${cls ? ' tele-' + cls : ''}${liveCls}">
        <span class="tele-label">${escapeHtml(label)} <span class="help" data-fkey="${tipId}" tabindex="0" role="button" aria-expanded="false" aria-label="Erklärung zu „${escapeHtml(label)}“" aria-describedby="${tipId}"><span class="help-dot" aria-hidden="true">?</span><span class="tip-bubble" id="${tipId}" role="tooltip">${escapeHtml(tip)}</span></span></span>
        <span class="tele-value">${escapeHtml(value)}</span>
      </div>`);
    });
    // Stale-Hinweis: Wer nur dieses Panel scannt, soll veraltete Werte nicht
    // für live halten (der Hero sagt "Box offline", aber der steht weiter oben).
    fieldsEl.classList.toggle('is-stale', !!offline || !!nasNote);
    let staleNote = '';
    if (nasNote) {
      staleNote = `<p class="stale-note">${escapeHtml(nasNote)}</p>`;
    } else if (offline) {
      staleNote = `<p class="stale-note">Box offline – die gedimmten Werte sind der letzte bekannte Stand${status.seen_at ? ' von ' + escapeHtml(formatTime(status.seen_at)) : ' (die Box hat sich noch nie gemeldet)'}.</p>`;
    }
    setHtml(fieldsEl, staleNote + groups.map((g) =>
      `<section class="tele-group"><h3>${escapeHtml(g.name)}</h3>${g.rows.join('')}</section>`).join(''));
  }

  // Abgeschlossene Befehle (bestätigt/abgelaufen/abgebrochen) nur noch so
  // lange anzeigen, danach ausblenden – sonst steht der letzte Befehl ewig da.
  const COMMAND_LINGER_S = 600;
  // Ausgeliefert, aber nach so langer Zeit noch keine Quittung: Die kommt
  // dann voraussichtlich nicht mehr (Box neu gestartet, ACK verloren).
  const COMMAND_ACK_GIVEUP_S = 600;

  // Zeile zum laufenden/letzten Box-Befehl (TEST, DEMO-ALARM). Beim Admin
  // steht sie direkt unter den Knöpfen, bei der Lese-Rolle im Held-Bereich.
  // Ohne Befehl ist sie ganz ausgeblendet: "Kein Befehl aktiv." war der
  // Normalfall und belegte dauerhaft den wichtigsten Platz der Seite.
  function renderCommand(command, serverTime) {
    if (!commandEl) return;
    if (command && serverTime) {
      const finishedAt = {
        acked: command.acked_at,
        expired: command.expired_at,
        cancelled: command.cancelled_at
      }[command.state];
      if (finishedAt && serverTime - finishedAt > COMMAND_LINGER_S) command = null;
    }
    if (!command) {
      commandEl.hidden = true;
      if (commandEl.textContent !== '') commandEl.textContent = '';
      if (cancelBtn) cancelBtn.hidden = true;
      return;
    }
    // Ausgeliefert, aber seit COMMAND_ACK_GIVEUP_S keine Quittung: nicht mehr
    // als "läuft" zeigen - das Ergebnis ist dann schlicht unbekannt.
    const deliveredAt = Number(command.delivered_at) || 0;
    const ackLost = command.state === 'delivered' && serverTime && deliveredAt > 0
      && serverTime - deliveredAt > COMMAND_ACK_GIVEUP_S;
    const label = ackLost
      ? 'an die Box ausgeliefert – Bestätigung nicht angekommen'
      : (STATE_LABELS[command.state] || command.state);
    let text = `Befehl #${command.id} ${command.type}: ${label}`;
    if (command.state === 'acked' && command.ack_result) {
      text += ` – ${fmtAckResult(command.ack_result)}`;
      if (command.ack_message) text += ` (${fmtAckMessage(command.ack_message)})`;
    }
    // Vom NAS selbst verworfen (Sicherung beim Ausliefern): den Grund zeigen,
    // sonst käme ein Demo-Alarm kommentarlos nie an.
    if (command.state === 'cancelled' && command.cancel_reason) {
      text += ` – ${command.cancel_reason}`;
    }
    if (commandEl.textContent !== text) commandEl.textContent = text;
    commandEl.hidden = false;
    // Ausgang des Befehls: Alles, was NICHT wie gewünscht ausgeführt wurde
    // (Quittung ≠ executed, abgelaufen, vom NAS mit Grund verworfen), ist gelb
    // mit "!" - früher stand es grau wie ein Erfolg da. Erfolg, ein von Hand
    // abgebrochener Befehl und eine verlorene Quittung sind grau; nur was noch
    // läuft, bekommt die volle Textfarbe.
    const warn = (command.state === 'acked' && !!command.ack_result && command.ack_result !== 'executed')
      || command.state === 'expired'
      || (command.state === 'cancelled' && !!command.cancel_reason);
    const done = ackLost || !['pending', 'delivered'].includes(command.state);
    commandEl.classList.toggle('cmd-warn', warn);
    commandEl.classList.toggle('cmd-done', done && !warn);
    if (cancelBtn) cancelBtn.hidden = command.state !== 'pending';
  }

  // Selbstüberwachung des Offline-Wächters: Der Wächter, der das Schweigen
  // der Box melden soll, darf nicht selbst unbemerkt schweigen. Der Zustand
  // wird EINMAL bewertet und zweifach genutzt: als Statuszeile in "Box &
  // Wartung" (immer) und im Warnfall als Hinweis im Held-Bereich.
  function watchdogState(info, serverTime) {
    if (!info) return null;
    if (!info.configured) {
      return { warn: true, text: 'nicht konfiguriert (bark_key_status in config.php setzen)' };
    }
    if (!info.last_run) {
      return { warn: true, text: 'noch nie gelaufen – DSM-Aufgabe im Aufgabenplaner eingerichtet?' };
    }
    const age = Math.max(0, serverTime - info.last_run);
    // Die DSM-Aufgabe läuft alle 15 min - erst warnen, wenn mehr als zwei
    // Läufe ausgefallen sind (35 min), sonst schlägt die Warnung ständig
    // fälschlich an, nur weil der nächste Lauf noch aussteht.
    if (age > 2100) {
      return { warn: true, text: `seit ${fmtUptime(age)} nicht gelaufen – DSM-Aufgabe prüfen!` };
    }
    // Läuft, aber sein letzter Bark-Versand ist gescheitert (und seitdem kam
    // kein erfolgreicher): Dann würde er eine Offline-Box gar nicht melden.
    const sendOk = Number(info.last_send_ok_at) || 0;
    const sendFailed = Number(info.last_send_failed_at) || 0;
    if (sendFailed > sendOk) {
      return { warn: true, text: `läuft, kann aber nicht melden (Bark-Versand fehlgeschlagen ${fmtRelTime(sendFailed, serverTime)}) – Status-Key/cURL prüfen` };
    }
    // Läuft und könnte melden - aber der Admin hat genau die Ausfall-Meldung
    // abgeschaltet (Panel "Meldungen an den Admin"). Das ist seine
    // Entscheidung, soll aber nicht in Vergessenheit geraten.
    if (info.offline_alert === false) {
      return { warn: true, text: 'läuft, aber die Meldung „Box offline“ ist abgeschaltet – ein Ausfall der Box würde nicht gemeldet' };
    }
    if (info.web_alert === false) {
      return { warn: true, text: 'läuft, aber die Meldung „NAS-Dashboard defekt“ ist abgeschaltet' };
    }
    return { warn: false, text: `läuft (zuletzt ${age < 60 ? 'gerade eben' : 'vor ' + fmtUptime(age)})` };
  }

  // Außen-Wächter (Cloud-VM): Sie klopft alle 5 min über api/alive.php an.
  // Ab wann sie als stumm gilt, meldet der Server (outside_silent_after_s -
  // dieselbe Frist wie die Bark-Meldung des Cron-Wächters); ältere Stände
  // ohne das Feld: eine Stunde.
  // null = nicht eingerichtet (die Zeile zeigt dann "nicht eingerichtet").
  const OUTSIDE_SILENT_FALLBACK_S = 3600;
  function outsideState(info, serverTime) {
    if (!info || info.outside_seen_at === undefined) return null;
    const seen = Number(info.outside_seen_at) || 0;
    if (!seen) return { warn: true, text: 'eingerichtet, hat sich aber noch nie gemeldet – läuft die VM?' };
    const age = Math.max(0, serverTime - seen);
    const silentAfter = Number(info.outside_silent_after_s) > 0
      ? Number(info.outside_silent_after_s) : OUTSIDE_SILENT_FALLBACK_S;
    if (age > silentAfter) {
      return { warn: true, text: `meldet sich seit ${fmtUptime(age)} nicht – VM in der Oracle-Konsole prüfen (gestoppt?)` };
    }
    const last = age < 60 ? 'gerade eben' : 'vor ' + fmtUptime(age);
    // Wie beim Cron-Wächter: Die VM klopft zwar an, kann aber ihre eigenen
    // Bark-Meldungen nicht zustellen - fiele das NAS aus, käme keine Warnung.
    const barkFailedAt = Number(info.outside_bark_failed_at) || 0;
    if (barkFailedAt > 0) {
      return { warn: true, text: `meldet sich (zuletzt ${last}), kann aber nicht melden (Bark-Versand fehlgeschlagen seit ${fmtDayTime(barkFailedAt, serverTime)}) – BARK_KEY in /etc/fw-alarm-watch.conf auf der VM prüfen` };
    }
    // Wie bei den beiden anderen sicherheitsrelevanten Meldungen: Hat der
    // Admin "Außen-Wächter meldet sich nicht" abgeschaltet, darf das nicht in
    // Vergessenheit geraten (outside_alert kommt nur für den Admin).
    if (info.outside_alert === false) {
      return { warn: true, text: `meldet sich (zuletzt ${last}), aber die Meldung „Außen-Wächter meldet sich nicht“ ist abgeschaltet – fiele die VM aus, käme keine Warnung` };
    }
    return { warn: false, text: `meldet sich (zuletzt ${last})` };
  }

  // ==== Held-Bereich: Fakten und Hinweise ===================================
  // Die große Lampe sagt, ob die Kette der Box arbeitet. Ob im Ernstfall auch
  // jemand etwas HÖRT und was zuletzt passiert ist, stand früher verstreut in
  // drei Panels - jetzt daneben: wer alarmiert wird (und wie viele stumm
  // sind) und wann der letzte Alarm war. Darunter alles, was gerade auffällig
  // ist. Gefüttert aus drei Abfragen (Status, Empfängerliste, Alarm-Verlauf);
  // jede ruft nach ihrem Eintreffen renderHeroExtras() auf.

  let keysSummary = null;     // { total, muted, permanent } aus keys_list
  let alarmInfo;              // undefined = noch nicht geladen, null = nichts im Verlauf
  let serverOffset = 0;       // Server-Zeit minus Browser-Zeit (s)
  let mismatchSince = 0;      // seit wann (ms) die Box eine andere Listen-Version meldet
  let mismatchPair = '';      // für welches Versionspaar "Box|NAS" mismatchSince gilt
  let keysCorrupt = '';       // Meldung, wenn die Empfängerliste samt Sicherung unlesbar ist
  let deviceHint = '';        // Hinweis des Servers zu mehreren Boxen mit demselben Token
  const serverNow = () => Date.now() / 1000 + serverOffset;

  // Ein Alarm gilt so lange als "frisch" (rot hervorgehoben, mit "vor X min").
  const ALARM_RECENT_S = 3600;
  const ALARM_KINDS = {
    relay: 'Relaisalarm (Melder)',
    demo_box: 'Demo-Alarm über die Box',
    manual: 'manueller Alarm (Dashboard)',
    manual_demo: 'Demo-Alarm (Dashboard)',
    manual_open: 'manueller Alarm – Ergebnis offen',
    manual_demo_open: 'Demo-Alarm (Dashboard) – Ergebnis offen'
  };

  // Jüngsten Alarm aus den Verlaufs-Einträgen (neueste zuerst) ableiten.
  // Nachsendungen zählen nicht als eigener Alarm. Eine Entwarnung, die NACH
  // dem Alarm kam (steht also weiter oben), wird mit angezeigt - aber ehrlich:
  // - Eine Entwarnung, die nur an den Demo-Empfänger ging, entwarnt keinen
  //   echten Alarm (sie wird dann nicht angerechnet).
  // - clearedOk sagt, ob die Entwarnung ALLE erreicht hat (ggf. erst mit der
  //   Nachsendung). Sonst bleibt der Alarm rot und die Zeile sagt es dazu.
  function deriveAlarmInfo(entries) {
    // Demo-Versand: Neuere Server markieren ihn im Verlauf (demo); bei
    // Alt-Einträgen ohne das Feld bleibt nur der Anzeigetext als Erkennung.
    const isDemo = (e) => (typeof e.demo === 'boolean' ? e.demo : /^DEMO-MODUS/.test(e.message || ''));
    let cleared = null;        // jüngste Entwarnung nach dem Alarm: { at, ok, demo }
    let clearResent = null;    // Ergebnis der jüngsten Entwarnungs-Nachsendung
    let resent = null;         // Ergebnis der jüngsten Alarm-Nachsendung
    const finish = (info, alarmIsDemo) => {
      if (cleared && (alarmIsDemo || !cleared.demo)) {
        info.clearedAt = cleared.at;
        info.clearedOk = cleared.ok || clearResent === true;
      }
      return info;
    };
    for (const e of entries) {
      if (e.resend) {
        if (e.event === 'nas_alarm' && resent === null) resent = !!e.ok;
        if (e.event === 'false_alarm' && clearResent === null && !cleared) clearResent = !!e.ok;
        continue;
      }
      if (e.event === 'false_alarm') {
        if (!cleared) cleared = { at: e.at, ok: !!e.ok, demo: isDemo(e) };
      } else if (e.event === 'relay_alarm') {
        const demo = e.source === 'remote';
        return finish({ kind: demo ? 'demo_box' : 'relay', at: e.at }, demo);
      } else if (e.event === 'nas_alarm') {
        const demo = isDemo(e);
        return finish({ kind: demo ? 'manual_demo' : 'manual', at: e.at, ok: e.ok, resent }, demo);
      } else if (e.event === 'nas_alarm_started') {
        // Ohne demo-Feld (Alt-Eintrag) unbekannt - wie bisher als echter Alarm.
        const demo = e.demo === true;
        return finish({ kind: demo ? 'manual_demo_open' : 'manual_open', at: e.at }, demo);
      }
    }
    return null;
  }

  function lastAlarmFact() {
    if (alarmInfo === undefined) return ['…', ''];
    if (!alarmInfo) {
      // Nichts im NAS-Verlauf: ersatzweise die Sicht der Box seit ihrem
      // letzten Neustart.
      const raw = lastStatus && !na(lastStatus.last_alarm) && lastStatus.last_alarm !== 'nie'
        ? String(lastStatus.last_alarm) : '';
      return [raw || 'keiner aufgezeichnet', ''];
    }
    const now = serverNow();
    const age = now - alarmInfo.at;
    const recent = age >= 0 && age < ALARM_RECENT_S;
    let text = recent
      ? `${fmtRelTime(alarmInfo.at, now)} (${fmtClock(alarmInfo.at)} Uhr)`
      : fmtDayTime(alarmInfo.at, now);
    text += `, ${ALARM_KINDS[alarmInfo.kind] || 'Alarm'}`;
    if (alarmInfo.kind === 'manual' && alarmInfo.ok === false) {
      text += alarmInfo.resent ? ' – nicht alle erreicht, Nachsendung zugestellt' : ' – nicht alle erreicht';
    }
    if (alarmInfo.clearedAt) {
      text += alarmInfo.clearedOk
        ? `. Entwarnung um ${fmtClock(alarmInfo.clearedAt)} Uhr gesendet`
        : `. Entwarnung um ${fmtClock(alarmInfo.clearedAt)} Uhr – NICHT alle erreicht`;
    }
    // Gelb erst, wenn die Entwarnung wirklich alle erreicht hat.
    return [text, recent ? (alarmInfo.clearedAt && alarmInfo.clearedOk ? 'warn' : 'crit') : ''];
  }

  function recipientsFact() {
    // Liste samt Sicherung unlesbar: Das ist NICHT "keine eingetragen".
    if (keysCorrupt) return ['Liste auf dem NAS beschädigt – siehe „Alarm-Empfänger“', 'crit'];
    if (demoInfo && demoInfo.enabled) {
      return [`nur ${demoInfo.label || 'der Test-Empfänger'} (Demo-Modus)`, 'warn'];
    }
    if (!keysSummary) return ['…', ''];
    const { total, muted, permanent, overdue } = keysSummary;
    if (total === 0) return ['keine eingetragen – die Box nutzt ihre Startliste', 'warn'];
    // Fällige, aber nicht gespeicherte Laut-Rückschaltung: Das NAS zählt den
    // Eintrag als laut, die Box alarmiert ihn evtl. noch stumm.
    const tail = overdue ? `; bei ${overdue} ist die Rückschaltung nicht gespeichert – die Box alarmiert evtl. noch stumm` : '';
    if (muted === 0) return [`${total}, alle laut${tail}`, overdue ? 'warn' : ''];
    if (muted === total) return [`${total}, ALLE stumm${tail}`, 'warn'];
    return [`${total}, davon ${muted} stumm${permanent ? ` (${permanent} dauerhaft)` : ''}${tail}`, overdue ? 'warn' : ''];
  }

  function renderHeroExtras() {
    if (heroFactsEl) {
      const rows = [['Empfänger', ...recipientsFact()], ['Letzter Alarm', ...lastAlarmFact()]];
      setHtml(heroFactsEl, rows.map(([label, text, cls]) =>
        `<div class="fact${cls ? ' fact-' + cls : ''}"><dt>${escapeHtml(label)}</dt><dd>${escapeHtml(text)}</dd></div>`).join(''));
    }
    if (!heroHintsEl) return;
    const hints = [];
    // Bei einer NAS-Störung sind alle Werte alt - dann keine Hinweise daraus.
    if (!statusProblem && lastStatus) {
      const demoOn = !!(demoInfo && demoInfo.enabled);
      if (!demoOn && keysSummary && keysSummary.total > 0 && keysSummary.muted === keysSummary.total) {
        hints.push(`Alle ${keysSummary.total} Empfänger sind stumm geschaltet – ein Alarm käme bei niemandem mit vollem Ton an.`);
      }
      // Fertiger Text vom Server: mehrere Boxen mit demselben Token bzw. ein
      // ignoriertes fremdes Board (dann stimmen die Statuswerte evtl. nicht).
      if (deviceHint) hints.push(deviceHint);
      if (lastWd && lastWd.warn) hints.push(`Offline-Wächter (NAS): ${lastWd.text}`);
      if (lastWd && lastWd.outside && lastWd.outside.warn) hints.push(`Außen-Wächter (Cloud): ${lastWd.outside.text}`);
      if (!lastOffline) {
        const ctx = { offline: false, seenAge: lastSeenAge, watchdog: lastWd };
        ROWS.forEach((row) => {
          if (!row.hint || !row.hint(lastStatus)) return;
          hints.push(row.hintText
            ? row.hintText(lastStatus, ctx)
            : `${rowLabel(row, ctx)}: ${row.value(lastStatus, ctx)}`);
        });
        // Kurz nach jeder Änderung ist die Abweichung normal (die Box holt
        // die Liste beim nächsten Abgleich) - erst melden, wenn sie anhält.
        if (mismatchSince && Date.now() - mismatchSince > 60000) {
          hints.push(`Empfängerliste: Die Box hat den aktuellen Stand seit ${fmtUptime((Date.now() - mismatchSince) / 1000)} nicht übernommen.`);
        }
      }
    }
    heroHintsEl.hidden = hints.length === 0;
    setHtml(heroHintsEl, hints.map((hint) => `<li>${escapeHtml(hint)}</li>`).join(''));
  }

  // ==== Demo-Modus ==========================================================
  // Der Wechsel greift auf der Box erst nach deren nächstem Abgleich. Deshalb
  // vergleicht das Banner die Listen-Version der Box mit der des NAS und
  // sagt klar, ob man schon gefahrlos testen kann.

  let prevDemoEnabled = null;
  // Serverseitige Aussage "die Box hat die Demo-Liste übernommen" (status:
  // demo_sync_ok) - berechnet mit DERSELBEN Regel wie die Sperre des
  // DEMO-ALARMs (Version bestätigt, genau 1 Empfänger, Status frisch). null =
  // älterer Server ohne das Feld. demoSyncFor = für welchen Modus
  // (expectDemoOf) sie galt: Wechselt der Modus, bevor der nächste Status
  // kommt, gilt sie nicht mehr.
  let demoSyncOk = null;
  let demoSyncFor = null;

  // Beschriftung der Auslöse-Knöpfe: Im Demo-Modus steht dran, wohin es geht.
  function setLabel(btn, text) {
    if (btn && btn.textContent !== text) btn.textContent = text;
  }

  // syncUnknown: Das NAS liefert gerade keine aktuellen Daten - dann lässt
  // sich NICHT sagen, ob die Box die Demo-Liste übernommen hat (eine alte
  // "übernommen"-Aussage stehen zu lassen, wäre gefährlich).
  function renderDemo(status, offline, syncUnknown) {
    const enabled = !!(demoInfo && demoInfo.enabled);
    const target = enabled ? (demoInfo.label || 'Test-Empfänger') : '';
    const espVersion = status.keys_version;
    // "Übernommen" nur, wenn der Server es mit seiner Regel bestätigt (Versions-
    // gleichheit allein sagt nichts über die Anzahl bzw. die Frische). Ohne das
    // Feld (älterer Server): wie bisher der Versionsvergleich.
    const synced = typeof demoSyncOk === 'boolean'
      ? (!offline && demoSyncOk && demoSyncFor === expectDemoOf(demoInfo))
      : (!offline && espVersion !== undefined && nasKeysVersion !== null
        && String(espVersion) === String(nasKeysVersion));

    if (demoBannerEl) {
      if (enabled) {
        let syncText;
        if (syncUnknown) {
          syncText = 'Sync-Status unbekannt – keine aktuelle Antwort vom NAS. Nicht davon ausgehen, dass gefahrlos getestet werden kann!';
        } else if (offline) {
          syncText = 'Achtung: Die Box ist OFFLINE – ein Relaisalarm würde noch mit der ALTEN Liste senden!';
        } else if (synced) {
          // Die Lese-Rolle kann nichts auslösen - für sie nur die Tatsache.
          syncText = readOnly
            ? 'Die Box hat die Demo-Liste übernommen.'
            : 'Die Box hat die Demo-Liste übernommen – du kannst jetzt gefahrlos testen.';
        } else {
          syncText = 'Achtung: Die Box hat die Demo-Liste NOCH NICHT übernommen (das dauert einige Sekunden) – ein Relaisalarm würde noch an ALLE gehen!';
        }
        demoBannerEl.hidden = false;
        setHtml(demoBannerEl, `<strong>DEMO-MODUS AKTIV</strong> – Alarme gehen NUR an
          <strong>${escapeHtml(target)}</strong>
          (<code>${escapeHtml(demoInfo.key_masked || '')}</code>), seit ${escapeHtml(formatTime(demoInfo.changed_at))}.<br>${syncText}`);
      } else {
        demoBannerEl.hidden = true;
      }
    }
    // Chip in der klebenden Kopfzeile: Das Banner scrollt weg, der Chip nicht.
    // Auf schmalen Bildschirmen steht nur "Demo" (der Name wird per CSS
    // ausgeblendet), sonst bräche die Kopfzeile in zwei Zeilen um.
    if (demoChipEl) {
      demoChipEl.hidden = !enabled;
      setHtml(demoChipEl, enabled
        ? `Demo<span class="chip-long">: nur ${escapeHtml(target)}</span>` : 'Demo-Modus');
    }

    if (!demoInfo) return;
    if (demoStateEl) {
      setHtml(demoStateEl, enabled
        ? `Aktuell: <strong class="mode-demo">DEMO-MODUS</strong> – Test-Empfänger: <strong>${escapeHtml(target)}</strong>`
        : 'Aktuell: <strong class="mode-live">LIVE</strong> – Alarme gehen an alle Empfänger.');
    }
    if (demoSummaryEl) {
      setLabel(demoSummaryEl, enabled ? `AKTIV – nur ${target}` : 'aus (LIVE)');
      demoSummaryEl.classList.toggle('is-on', enabled);
    }
    // Beim Einschalten (auch aus einer anderen Sitzung) klappt der Block auf,
    // damit der Schalter zum Beenden sichtbar ist. Danach bleibt er, wie die
    // Person ihn lässt.
    if (demoDetailsEl && enabled && prevDemoEnabled !== true) demoDetailsEl.open = true;
    prevDemoEnabled = enabled;

    if (demoToggleBtn) {
      demoToggleBtn.disabled = busyCount > 0;
      setLabel(demoToggleBtn, enabled ? 'Demo-Modus beenden (zurück zu LIVE)' : 'Demo-Modus einschalten');
      // Rot markiert den Wechsel zurück zu LIVE (danach sind Alarme wieder "scharf").
      demoToggleBtn.classList.toggle('danger', enabled);
    }
    if (demoTargetEl) {
      // Auswahl nur zeigen, solange sie gebraucht wird (zum Einschalten).
      demoTargetEl.hidden = enabled;
    }
    setLabel(alarmBtn, enabled ? `ALARM (Demo: nur ${target})` : 'REAL ALARM');
    setLabel(falseAlarmBtn, enabled ? `ENTWARNUNG (Demo: nur ${target})` : 'ENTWARNUNG (Fehlalarm)');
    // Der Alarm über die Box ist ein reines Demo-Werkzeug - im LIVE-Modus wäre
    // er ein echter Alarm an alle (der Server blockt das zusätzlich).
    if (espAlarmBtn) {
      if (armedBtn === espAlarmBtn) {
        // Seine Inline-Bestätigung ist gerade offen (der Knopf selbst ist
        // versteckt): Endet der Demo-Modus währenddessen, wird entschärft.
        if (!enabled) {
          disarm();
          espAlarmBtn.hidden = true;
        }
      } else {
        espAlarmBtn.hidden = !enabled;
      }
    }
  }

  // Erwartung für expect_demo: '0' = LIVE, sonst die key_id des Demo-
  // Empfängers. Unbekannter Zustand (noch nichts geladen) zählt als LIVE -
  // genau das sagt dann auch die Bestätigungsfrage.
  const expectDemoOf = (info) => (info && info.enabled ? String(info.key_id) : '0');

  // EINZIGE Stelle, an der demoInfo neu gesetzt wird: Wechselt der Modus
  // (LIVE <-> Demo oder anderer Test-Empfänger), während eine Alarm-
  // Bestätigung offen ist, passt deren Frage nicht mehr -> entschärfen.
  function updateDemoInfo(next) {
    if (!next) return;
    // Hat sich Modus oder Test-Empfänger geändert, muss auch die
    // Empfängerliste neu gezeichnet werden (Demo-Badge, pausierte Zeilen) -
    // nicht erst beim nächsten 30-s-Abruf der Liste.
    const changed = !demoInfo || !!demoInfo.enabled !== !!next.enabled
      || String(demoInfo.key_id) !== String(next.key_id) || demoInfo.label !== next.label;
    demoInfo = next;
    if (armedExpect !== null && expectDemoOf(demoInfo) !== armedExpect) {
      disarm();
      // Mit true: Eine gemerkte Nachsendung an die nicht Erreichten wird
      // wieder angeboten (ihre Frage nennt dann den neuen Modus).
      showResultNote('Der Demo-/LIVE-Modus hat sich gerade geändert – Bestätigung zurückgesetzt. Bitte Lage prüfen und bei Bedarf neu auslösen.', 'warn', true);
    }
    if (changed) renderKeys();
  }

  // HTTP 409 mode_changed: Der Server hat NICHTS gesendet, weil sein Modus
  // nicht zur Bestätigungsfrage passte. Seinen aktuellen Stand übernehmen
  // und das Banner sofort neu zeichnen (kein automatischer Neuversuch!).
  function applyModeChanged(err) {
    if (!err || err.kind !== 'mode_changed' || !err.data || !err.data.demo) return;
    updateDemoInfo(err.data.demo);
    renderDemo(lastStatus || {}, lastOffline, !!statusProblem);
    renderKeys();
    renderHeroExtras();
  }

  async function performDemoSet(fields) {
    setBusy(true);
    try {
      const data = await post(fields);
      showMsg(demoMsgEl, data.message || 'Fehler.', kindOf(data));
      if (data.ok && data.demo) updateDemoInfo(data.demo);
    } catch (err) {
      showMsg(demoMsgEl, errorText(err), errorKind(err));
    } finally {
      setBusy(false);
      refresh();
      refreshKeys();
      refreshLog();
    }
  }

  // Demo-Wechsel mit derselben Inline-Bestätigung wie die Auslöse-Knöpfe
  // (statt confirm(), siehe Abschnitt "Zweistufige Inline-Bestätigung").
  function toggleDemo() {
    if (!demoInfo) return;
    if (demoInfo.enabled) {
      armInline(demoToggleBtn,
        'Demo-Modus beenden? Danach gehen Alarme wieder an ALLE Empfänger (LIVE).',
        'Ja, LIVE schalten',
        () => performDemoSet({ action: 'demo_set', enabled: '0' }));
      return;
    }
    const id = demoTargetEl?.value || '';
    if (!id) {
      showMsg(demoMsgEl, 'Bitte zuerst einen Empfänger in der Liste anlegen und als Test-Empfänger auswählen.', 'error');
      return;
    }
    const label = demoTargetEl.selectedOptions[0]?.textContent || '?';
    armInline(demoToggleBtn,
      `Demo-Modus einschalten? Alarme gehen dann NUR an "${label}". Nach dem Testen unbedingt wieder auf LIVE zurückschalten!`,
      'Ja, Demo-Modus einschalten',
      () => performDemoSet({ action: 'demo_set', enabled: '1', id }));
  }

  // ==== Zentrale Anfrage-Funktion ===========================================
  // ALLE Anfragen ans NAS laufen über api(): mit Zeitlimit und mit einer
  // klaren Fehlerart, damit die Oberfläche unterscheiden kann zwischen
  //   'network'/'timeout' -> NAS nicht erreichbar (Antwort kam nie an),
  //   'http'              -> NAS antwortet, aber mit Fehler (z.B. HTTP 500),
  //   'auth'              -> Sitzung abgelaufen (401, lädt zur Anmeldung),
  //   'csrf'/'readonly'   -> Aktion serverseitig abgelehnt (403),
  //   'mode_changed'      -> Demo-/LIVE-Modus passt nicht zur Bestätigung (409).

  class ApiError extends Error {
    constructor(kind, message, status, data) {
      super(message || '');
      this.kind = kind;
      this.status = status || 0;
      this.data = data || null;
    }
  }

  // Zeitlimit-Signal: AbortSignal.timeout(), wo vorhanden; ältere Browser
  // (z.B. iOS < 16) bekommen dasselbe über AbortController + setTimeout.
  function timeoutSignal(ms) {
    if (typeof AbortSignal !== 'undefined' && typeof AbortSignal.timeout === 'function') {
      return { signal: AbortSignal.timeout(ms), cancel: () => {} };
    }
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), ms);
    return { signal: ctrl.signal, cancel: () => clearTimeout(timer) };
  }

  // Abbruch durch das Zeitlimit (TimeoutError bzw. AbortError beim
  // Fallback) - wir brechen sonst nie selbst ab, also ist das eindeutig.
  const isAbort = (err) => !!err && (err.name === 'TimeoutError' || err.name === 'AbortError');

  // Nicht-JSON-Fehlerseiten (text/plain-500, HTML vom Webserver) lesbar
  // kürzen. Landet ausschließlich per textContent im DOM.
  function shortText(text) {
    const plain = String(text || '').replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ').trim();
    return plain.length > 200 ? plain.slice(0, 200) + '…' : plain;
  }

  async function api(url, opts, timeoutMs) {
    const t = timeoutSignal(timeoutMs);
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
        handleAuthLoss();
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
        throw new ApiError('http', 'Antwort des NAS ist kein gültiges JSON' + (text ? `: ${shortText(text)}` : '.'), res.status, null);
      }
      return json;
    } finally {
      t.cancel();
    }
  }

  // Einheitliche Fehlermeldung für einfache Aktionen (Empfängerliste, Demo,
  // TEST, Abbrechen). REAL ALARM/ENTWARNUNG haben eigene, schärfere Texte.
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
        return 'Keine Antwort vom NAS (Zeitüberschreitung) – ob die Aktion ausgeführt wurde, ist unklar. Bitte Anzeige/Verlauf prüfen.';
      case 'network':
        return 'NAS nicht erreichbar – Verbindung prüfen.';
      default:
        return `Fehler${err.status ? ` (HTTP ${err.status})` : ''}${err.message ? `: ${err.message}` : '.'}`;
    }
  }

  // ==== Polling mit "Läuft noch"-Guard ======================================
  // Antwortet das NAS mal langsamer als das Poll-Intervall, sollen sich die
  // Anfragen nicht stapeln. Zwei Arten von Aufrufen:
  //   poller()       - AUSDRÜCKLICH angefordert (z.B. direkt nach einer
  //                    Aktion): Läuft gerade eine Abfrage, folgt EINMAL eine
  //                    weitere hinterher, statt verloren zu gehen - sonst
  //                    zeigte die Seite bis zum nächsten Intervall den alten
  //                    Stand.
  //   poller.tick()  - vom Intervall-Timer: Läuft gerade eine Abfrage, wird
  //                    der Takt einfach ausgelassen. Sonst würde ein NAS, das
  //                    langsamer antwortet als das Intervall, pausenlos
  //                    Abfrage an Abfrage bekommen - genau dann, wenn es
  //                    ohnehin überlastet ist.
  function makePoller(fn) {
    let busy = false;
    let rerun = false;
    const start = async (fromTimer) => {
      if (busy) {
        if (!fromTimer) rerun = true;
        return;
      }
      busy = true;
      try {
        await fn();
      } finally {
        busy = false;
        const again = rerun && !authLost;
        rerun = false;
        if (again) start(false);
      }
    };
    const run = () => start(false);
    run.tick = () => start(true);
    return run;
  }

  // Letzter erfolgreich abgerufener Stand - für die Anzeige "veraltet".
  let lastOkAt = 0;            // Browser-Zeit (ms) der letzten erfolgreichen Status-Abfrage
  let statusProblem = null;    // null = letzte Abfrage OK, sonst der Fehler
  let problemRenderKey = null; // welche Störungs-Anzeige zuletzt aufgebaut wurde
  let lastStatus = null;
  let lastOffline = false;
  let lastSeenAge = NaN;
  let lastWd = null;
  let lastAlarmMark = null;    // woran ein neuer Alarm erkannt wird (siehe refresh)

  const refresh = makePoller(async () => {
    try {
      const data = await api('dashboard_api.php?action=status', {}, TIMEOUT_POLL_MS);
      if (!data.ok) throw new ApiError('http', data.message || 'Unerwartete Antwort vom NAS.', 200, data);
      const status = data.status || {};
      // Aktuelles CSRF-Token der Sitzung übernehmen (siehe csrf oben).
      if (typeof data.csrf === 'string' && data.csrf) {
        setCsrf(data.csrf);
        csrfFromServer = true;
      }
      applyTimezone(data.timezone);
      updateDemoInfo(data.demo);
      nasKeysVersion = data.nas_keys_version ?? nasKeysVersion;
      demoSyncOk = typeof data.demo_sync_ok === 'boolean' ? data.demo_sync_ok : null;
      demoSyncFor = expectDemoOf(demoInfo);
      deviceHint = typeof data.device_hint === 'string' ? data.device_hint.trim() : '';
      statusProblem = null;
      problemRenderKey = null;
      lastOkAt = Date.now();
      const serverTime = Number(data.server_time);
      if (Number.isFinite(serverTime) && serverTime > 0) serverOffset = serverTime - Date.now() / 1000;
      const offline = !!data.offline;

      // Ampel-Logik der Statuslampe - der wichtigste Zustand gewinnt:
      // Offline schlägt alles. Danach die Alarm-Zustände, dann die
      // Einschränkungen (Kontakt klemmt, Demo-Modus), sonst grün.
      const alarmActive = !offline && flagged(status.alarm_pending);
      // Klemmender Kontakt: Die Box ist zwar online, kann aber keinen NEUEN
      // Alarm erkennen. stuck_warned setzt die Box erst, wenn der Kontakt
      // UNGEWÖHNLICH lange zu bleibt (30 min). Ältere Firmware setzt es beim
      // Öffnen/Wiederbewaffnen NICHT zurück (erst beim nächsten Alarm) - neuere
      // schon. Deshalb zählt es nur, solange der Kontakt laut Box auch wirklich
      // noch geschlossen ist (relay=closed); sonst bliebe die Lampe bei alter
      // Firmware nach dem Öffnen auf "Kontakt klemmt" stehen.
      const stuck = !offline && flagged(status.stuck_warned) && status.relay === 'closed';
      // Zustellung durch, Kontakt noch geschlossen: Der Melder steht weiter
      // auf Alarm. Das ist nach jedem Alarm normal (bis der Melder quittiert
      // ist) und KEINE Störung - aber eben auch nicht "alles ruhig": Früher
      // stand hier grün "Einsatzbereit", während zwei Zeilen tiefer rot
      // "GESCHLOSSEN (Alarm!)" stand.
      const alarmStanding = !offline && !alarmActive && !stuck && status.relay === 'closed';
      const demoOn = !!(demoInfo && demoInfo.enabled);
      let icon;
      if (offline) {
        setState('Box offline', 'crit', readOnly
          ? 'Relaisalarme kommen gerade NICHT an. Bitte einen Admin verständigen – er kann manuell alarmieren.'
          : 'Relaisalarme kommen gerade NICHT an. Manuell alarmieren geht weiterhin: „REAL ALARM“ direkt hier darunter.');
        icon = '\u{1F534} ';
      } else if (alarmActive) {
        setState('Alarm aktiv', 'alarm', `Die Box stellt gerade zu – bisher ${show(status.key_progress)} erreicht.`);
        icon = '\u{1F6A8} ';
      } else if (stuck) {
        setState('Kontakt klemmt', 'stale',
          'Der Melder-Kontakt ist ungewöhnlich lange geschlossen. Solange erkennt die Box keinen neuen Alarm – Melder und Lader prüfen.');
        icon = '⚠️ ';
      } else if (alarmStanding) {
        setState('Alarm ausgelöst', 'crit',
          `${deliveryIncomplete(status)
            ? `Zustellung beendet, aber nur ${show(status.key_progress)} erreicht – NICHT alle.`
            : `Zustellung abgeschlossen (${show(status.key_progress)} erreicht).`} Der Melder-Kontakt ist noch geschlossen – ein neuer Alarm wird erst erkannt, wenn er wieder offen war.`);
        icon = '\u{1F6A8} ';
      } else if (demoOn) {
        setState('Demo-Modus', 'stale',
          `Alarme gehen NUR an ${demoInfo.label || 'den Test-Empfänger'} – alle anderen werden gerade nicht alarmiert.`);
        icon = '\u{1F7E1} ';
      } else {
        setState('Einsatzbereit', 'ok');
        icon = '\u{1F7E2} ';
      }
      // Status auch im Tab-Titel: so sieht man ohne Tab-Wechsel, ob alles läuft.
      document.title = icon + PAGE_TITLE;
      // seen_age_seconds ist null, wenn sich die Box noch NIE gemeldet hat
      // (Number(null) wäre 0 und würde "Offline seit 0 Min" anzeigen).
      const seenAge = data.seen_age_seconds == null ? NaN : Number(data.seen_age_seconds);
      const ageText = fmtAgeShort(seenAge);
      updatedEl.textContent = `Letzter Status: ${formatTime(status.seen_at)}${ageText ? ` (${ageText})` : ''}`;
      const wdState = watchdogState(data.watchdog, serverTime);
      if (wdState) wdState.outside = outsideState(data.watchdog, serverTime);
      // Hält die Abweichung der Listen-Version an? (Hinweis im Held-Bereich)
      // Die Messung gilt für EIN Versionspaar Box|NAS: Ändert sich eine der
      // beiden (neue Änderung im Dashboard, Box hat einen Zwischenstand
      // übernommen), beginnt sie von vorn - sonst meldete eine frische
      // Änderung sofort "seit 10 min nicht übernommen".
      const versionsDiffer = !offline && !na(status.keys_version) && nasKeysVersion !== null
        && String(status.keys_version) !== String(nasKeysVersion);
      const versionPair = `${status.keys_version}|${nasKeysVersion}`;
      if (!versionsDiffer) {
        mismatchSince = 0;
        mismatchPair = '';
      } else if (!mismatchSince || mismatchPair !== versionPair) {
        mismatchSince = Date.now();
        mismatchPair = versionPair;
      }
      lastStatus = status;
      lastOffline = offline;
      lastSeenAge = seenAge;
      lastWd = wdState;
      renderFields(status, offline, seenAge, wdState, null);
      renderCommand(data.command, serverTime);
      renderDemo(status, offline, false);
      renderHeroExtras();
      // Neuer Alarm aus Sicht der Box (anderer Text oder Zustellung
      // begonnen/beendet)? Dann den Alarm-Verlauf sofort nachladen, statt bis
      // zu eine Minute auf den Takt zu warten.
      const mark = `${status.last_alarm}|${status.alarm_pending}|${status.alarm_seq}`;
      if (lastAlarmMark !== null && mark !== lastAlarmMark) {
        refreshAlarmInfo();
        refreshLog();
      }
      lastAlarmMark = mark;
    } catch (err) {
      if (err instanceof ApiError && err.kind === 'auth') return;   // handleAuthLoss() übernimmt
      statusProblem = err || new ApiError('http', '', 0, null);
      showStatusProblem();
    }
  });

  // Das NAS liefert gerade keine aktuellen Daten: NICHT dasselbe wie "Box
  // offline", deshalb eigene (gelbe) Zustände:
  //   - Netzwerk/Zeitlimit -> "NAS nicht erreichbar"
  //   - HTTP-Fehler        -> "NAS-Störung" + Server-Meldung
  //   - nur zu lange her   -> "Status veraltet" (z.B. Tab lag im Hintergrund)
  // In allen Fällen werden die Statuswerte gedimmt und das Demo-Banner sagt
  // "Sync-Status unbekannt" statt einer alten Entwarnung.
  function showStatusProblem() {
    if (authLost) return;
    const err = statusProblem;
    let word;
    let text;
    if (!err) {
      word = 'Status veraltet';
      text = 'Keine aktuelle Antwort vom NAS';
    } else if (err.kind === 'timeout' && alarmInFlight) {
      // Während ein Alarm-Versand läuft, kann das NAS Status-Abfragen
      // derselben Sitzung zurückstellen - das ist kein Ausfall.
      word = 'NAS beschäftigt';
      text = 'Keine Status-Antwort, solange der Alarm-Versand läuft';
    } else if (err.kind === 'network' || err.kind === 'timeout') {
      word = 'NAS nicht erreichbar';
      text = err.kind === 'timeout' ? 'Keine Antwort vom NAS (Zeitüberschreitung)' : 'Keine Verbindung zum NAS';
    } else if (err instanceof ApiError) {
      word = 'NAS-Störung';
      text = `Das NAS meldet einen Fehler${err.status ? ` (HTTP ${err.status})` : ''}${err.message ? `: ${err.message}` : ''}`;
    } else {
      word = 'NAS-Störung';
      text = 'Die Antwort des NAS konnte nicht ausgewertet werden';
    }
    // Nur diese eine Zeile läuft im Sekundentakt mit (Altersangabe).
    const ageText = lastOkAt ? fmtAgeShort((Date.now() - lastOkAt) / 1000) : null;
    updatedEl.textContent = `${text} – angezeigte Werte können veraltet sein${ageText ? ` (letzter erfolgreicher Abruf ${ageText})` : ''}.`;
    // Alles Übrige (Lampe, gedimmte Felder, Demo-Banner) ändert sich NICHT
    // jede Sekunde - nur neu aufbauen, wenn sich an der Lage etwas geändert
    // hat. Sonst würde der Ticker während einer Störung das ganze
    // Statuspanel sekündlich neu zusammensetzen.
    const renderKey = [word, text, lastOkAt, nasKeysVersion, lastOffline,
      demoInfo ? `${demoInfo.enabled}:${demoInfo.key_id}` : ''].join('|');
    if (renderKey === problemRenderKey) return;
    problemRenderKey = renderKey;
    // War die Box beim letzten erfolgreichen Abruf OFFLINE, ist das die
    // wichtigste Aussage - und der Hinweis auf REAL ALARM darf mit der
    // NAS-Störung nicht verschwinden. Der beruhigende Satz ("läuft unabhängig
    // weiter") nur ohne diesen Stand, und auch dann vorsichtig: Ob die Box
    // gerade läuft, lässt sich ohne NAS nicht sehen.
    let sub;
    if (lastOffline) {
      sub = readOnly
        ? 'Letzter bekannter Stand: Box OFFLINE – Relaisalarme kamen da NICHT an. Bitte einen Admin verständigen – er kann manuell alarmieren.'
        : 'Letzter bekannter Stand: Box OFFLINE – Relaisalarme kamen da NICHT an. Manuell alarmieren: „REAL ALARM“ direkt hier darunter (läuft über das NAS – kommt keine Antwort, die Mannschaft anders verständigen).';
    } else {
      sub = 'Das Dashboard bekommt gerade keine frischen Daten. Die Box alarmiert über Bark auch ohne das NAS – ob sie gerade läuft, lässt sich von hier aus aber nicht prüfen.';
    }
    // Rot bleibt rot: Eine NAS-Störung macht einen Box-Ausfall nicht harmloser.
    setState(word, lastOffline ? 'crit' : 'stale', sub);
    document.title = '⚠️ ' + PAGE_TITLE;
    const note = `Keine aktuellen Daten vom NAS – die gedimmten Werte sind der letzte bekannte Stand${lastOkAt ? ` (abgerufen um ${fmtClockMs(lastOkAt)} Uhr)` : ''}.`;
    if (lastStatus) {
      renderFields(lastStatus, lastOffline, lastSeenAge, lastWd, note);
    } else {
      fieldsEl.classList.add('is-stale');
      setHtml(fieldsEl, `<p class="stale-note">${escapeHtml(note)}</p>`);
    }
    renderDemo(lastStatus || {}, lastOffline, true);
    renderHeroExtras();
  }

  // Sekündlicher Ticker (nur bei sichtbarem Tab): Kam zu lange keine
  // erfolgreiche Status-Antwort, darf der Hero nicht weiter grün
  // "Einsatzbereit" zeigen. Bei einem bekannten Fehler hält er zusätzlich
  // die Altersangabe ("letzter erfolgreicher Abruf vor 23 s") aktuell.
  function tick() {
    if (authLost) return;
    if (statusProblem || (lastOkAt && Date.now() - lastOkAt > STALE_AFTER_MS)) showStatusProblem();
  }

  // Neues CSRF-Token übernehmen - auch im Abmelde-Formular der Seite, das
  // sonst mit dem Token vom Seitenaufruf abgeschickt würde.
  function setCsrf(token) {
    if (token === csrf) return;
    csrf = token;
    document.querySelectorAll('form input[name="csrf"]').forEach((input) => { input.value = token; });
  }

  // Bei einem CSRF-Fehler sofort den Status neu holen - er bringt das
  // aktuelle Token mit. Bewusst KEIN automatischer Neuversuch: Die Person
  // bestätigt die Aktion noch einmal selbst (siehe errorText()).
  async function post(fields, timeoutMs = TIMEOUT_POST_MS) {
    const form = new FormData();
    Object.entries(fields).forEach(([key, value]) => form.set(key, value));
    try {
      return await api('dashboard_api.php', {
        method: 'POST',
        headers: { 'X-CSRF-Token': csrf },
        body: form
      }, timeoutMs);
    } catch (err) {
      if (err instanceof ApiError && err.kind === 'csrf') refresh();
      throw err;
    }
  }

  // Text für einen CSRF-Fehler: Liefert der Server das Token mit dem Status
  // aus, ist es nach dem refresh() oben schon erneuert - sonst hilft nur
  // Neuladen der Seite.
  const csrfHint = () => (csrfFromServer
    ? 'Sicherheits-Token erneuert – bitte erneut bestätigen'
    : 'Sicherheits-Token veraltet – Seite neu laden');

  // ==== Auslösen & Testen ===================================================

  // Befehl an die Box (TEST bzw. im Demo-Modus ALARM): landet in der
  // Warteschlange, die Box holt ihn beim nächsten Abgleich ab. Die
  // Rückmeldung und der Fortschritt stehen direkt unter den Knöpfen.
  async function enqueue(fields) {
    setBusy(true);
    try {
      const data = await post(fields);
      // Die Fortschrittszeile darüber zeigt den Befehl ab jetzt - "Befehl
      // angelegt." allein wäre nur eine Doppelung.
      showMsg(commandMsgEl, data.ok ? '' : (data.message || 'Fehler.'), 'error');
    } catch (err) {
      applyModeChanged(err);
      showMsg(commandMsgEl, errorText(err), errorKind(err));
    } finally {
      setBusy(false);
      refresh();
      refreshLog();
    }
  }

  const enqueueTest = () => enqueue({ action: 'enqueue', type: 'TEST' });

  // DEMO-Alarm über die Box: wie ein echter Relaisalarm (startAlarm +
  // Nachsende-Puffer). Der Server erlaubt das NUR im Demo-Modus und nur,
  // wenn die Box die Demo-Liste schon übernommen hat.
  async function enqueueEspAlarm(expectDemo) {
    alarmInFlight = true;
    applyBusy();
    try {
      await enqueue({ action: 'enqueue', type: 'ALARM', expect_demo: expectDemo });
    } finally {
      alarmInFlight = false;
      applyBusy();
    }
  }

  async function cancelCommand() {
    setBusy(true);
    try {
      const data = await post({ action: 'cancel' });
      showMsg(commandMsgEl, data.message || 'Fehler.', kindOf(data));
    } catch (err) {
      showMsg(commandMsgEl, errorText(err), errorKind(err));
    } finally {
      setBusy(false);
      refresh();
      refreshLog();
    }
  }

  // ---- Ergebnis-Kasten für REAL ALARM / ENTWARNUNG -------------------------
  // Unter Stress zählt EINE Frage: Wer wurde NICHT erreicht? Deshalb stehen
  // die nicht Erreichten als eigene Liste zuerst (früher: eine Klammerzeile
  // mit allen Namen), dazu die Uhrzeit des Versands und ein Knopf, der
  // gezielt nur an sie nachsendet. Alles per DOM-Knoten/textContent gebaut.

  const el = (tag, cls, text) => {
    const node = document.createElement(tag);
    if (cls) node.className = cls;
    if (text !== undefined) node.textContent = text;
    return node;
  };

  // Grund eines fehlgeschlagenen Empfängers: http 0 heißt beim NAS-Versand
  // "keine Verbindung zu Bark bzw. Zeitüberschreitung" - nicht "HTTP 0".
  const failReason = (r) => (Number(r.http) === 0 ? 'keine Verbindung/Timeout' : `HTTP ${r.http}`);

  // Wer zuletzt NICHT erreicht wurde, bleibt hier stehen, bis ein neueres
  // Ergebnis es ersetzt oder der Admin "Ausblenden" klickt - auch wenn
  // danach eine Nachsendung läuft, scheitert, unklar endet oder gar nichts
  // sendet oder sich der Demo-/LIVE-Modus ändert. Diese Namen braucht man zum
  // Telefonieren; der Verlauf führt sie nicht. Zusätzlich in sessionStorage
  // gespiegelt: Ein Neuladen der Seite (z.B. nach Ablauf der Anmeldung)
  // verliert sie so nicht.
  //   { action ('alarm'|'false_alarm'), what, at (ms, Server-Zeit des
  //     Ergebnisses), startedAt (s, Server-Zeit des URSPRÜNGLICHEN Versands,
  //     0 = unbekannt), entries: [{ id (0 = ohne id), name, reason }] } oder null.
  let lastUnreached = null;
  const UNREACHED_STORE = 'fwAlarm.lastUnreached';
  const whatOf = (action) => (action === 'alarm' ? 'REAL ALARM' : 'ENTWARNUNG');

  function setUnreached(value) {
    lastUnreached = value;
    try {
      if (value) sessionStorage.setItem(UNREACHED_STORE, JSON.stringify(value));
      else sessionStorage.removeItem(UNREACHED_STORE);
    } catch (err) {
      // Speicher gesperrt (privates Fenster o.ä.) - dann nur in dieser Seite.
    }
  }

  // Gemerkten Stand nach einem Neuladen zurückholen. Streng geprüft: Was
  // nicht genau so aussieht wie von setUnreached() geschrieben, wird verworfen
  // - ebenso ein Stand, der älter als ein halber Tag ist (der Tab kann lange
  // offen bleiben; dann sind die Namen kein aktueller Auftrag mehr).
  const UNREACHED_MAX_AGE_MS = 12 * 3600 * 1000;
  function restoreUnreached() {
    let raw = null;
    try {
      raw = sessionStorage.getItem(UNREACHED_STORE);
    } catch (err) {
      return null;
    }
    if (!raw) return null;
    try {
      const v = JSON.parse(raw);
      const okEntry = (e) => !!e && Number.isInteger(e.id) && e.id >= 0
        && typeof e.name === 'string' && typeof e.reason === 'string';
      if (v && (v.action === 'alarm' || v.action === 'false_alarm') && Number.isFinite(v.at)
          && Date.now() - v.at < UNREACHED_MAX_AGE_MS
          && Array.isArray(v.entries) && v.entries.length > 0 && v.entries.every(okEntry)) {
        return {
          action: v.action,
          what: whatOf(v.action),
          at: v.at,
          startedAt: Number(v.startedAt) > 0 ? Number(v.startedAt) : 0,
          entries: v.entries.map((e) => ({ id: e.id, name: e.name, reason: e.reason }))
        };
      }
    } catch (err) {
      // kaputter Eintrag -> unten verwerfen
    }
    try {
      sessionStorage.removeItem(UNREACHED_STORE);
    } catch (err) {
      // egal
    }
    return null;
  }

  function unreachedList() {
    const list = el('ul', 'result-list');
    lastUnreached.entries.forEach((e) => list.append(el('li', '', `${e.name} (${e.reason})`)));
    return list;
  }

  function unreachedNodes() {
    if (!lastUnreached) return [];
    return [el('p', 'result-label',
      `Zuletzt NICHT erreicht (${lastUnreached.what}, ${fmtClockMs(lastUnreached.at)} Uhr) – bitte anders verständigen (Telefon):`), unreachedList()];
  }

  // "Nur an die nicht Erreichten erneut senden" - immer aus dem gemerkten
  // Stand gebaut, damit der Knopf auch nach einem Hinweis (Modus-Wechsel)
  // oder einem Ergebnis ohne Versand wieder angeboten werden kann.
  // Nachsenden geht nur an Einträge der gepflegten Liste (mit id) - nicht an
  // die Not-Liste aus config.php.
  function retryButton() {
    const u = lastUnreached;
    if (!u) return null;
    const retry = u.entries.filter((e) => e.id > 0);
    if (!retry.length) return null;
    const ids = retry.map((e) => e.id);
    const names = retry.map((e) => e.name).join(', ');
    const btn = el('button', '', retry.length === 1
      ? `Nur an ${retry[0].name} erneut senden`
      : `Nur an die ${retry.length} nicht Erreichten erneut senden`);
    btn.type = 'button';
    btn.addEventListener('click', () => {
      if (alarmInFlight || btn.disabled) return;
      // Auch die Nachsendung bestätigt den Modus, der JETZT gilt - und die
      // Frage nennt ihn ausdrücklich. Der Server sendet nur, wenn er noch
      // stimmt, und nur an Empfänger, die in der aktuellen (im Demo-Modus:
      // auf den Test-Empfänger verkürzten) Liste stehen.
      const snapshot = demoInfo;
      const expect = expectDemoOf(snapshot);
      const mode = snapshot && snapshot.enabled
        ? `Aktuell DEMO-MODUS (alarmiert wird nur „${snapshot.label || 'Test-Empfänger'}“)`
        : 'Aktuell LIVE';
      armInline(btn, `${mode}: ${u.what} erneut senden – NUR an: ${names}?`,
        'Ja, erneut senden', () => sendNasAlarm(u.action, expect, ids, u.startedAt), expect);
    });
    retryBtn = btn;
    return btn;
  }

  function clearResult() {
    if (!alarmResultEl) return;
    if (armedBtn && alarmResultEl.contains(armedBtn)) disarm();
    retryBtn = null;
    setUnreached(null);
    alarmResultEl.hidden = true;
    alarmResultEl.className = 'result';
    alarmResultEl.replaceChildren();
  }

  function dismissButton() {
    const btn = el('button', 'ghost-button', 'Ausblenden');
    btn.type = 'button';
    btn.addEventListener('click', clearResult);
    return btn;
  }

  // Einfache Meldung im Ergebnis-Kasten (Versand läuft, Fehler, Hinweis).
  // kind: '' (läuft), 'fail' oder 'warn'. offerRetry: den Nachsende-Knopf
  // für die gemerkten nicht Erreichten wieder anbieten (z.B. nach einem
  // Modus-Wechsel - sonst verschwände er ersatzlos).
  function showResultNote(text, kind, offerRetry = false) {
    if (!alarmResultEl) return;
    if (armedBtn && alarmResultEl.contains(armedBtn)) disarm();
    retryBtn = null;
    alarmResultEl.hidden = false;
    alarmResultEl.className = 'result' + (kind ? ' result-' + kind : '');
    const nodes = [el('p', 'result-head', text), ...unreachedNodes()];
    if (kind) {
      const row = el('div', 'actions result-actions');
      const retry = offerRetry ? retryButton() : null;
      if (retry) row.append(retry);
      row.append(dismissButton());
      nodes.push(row);
    }
    alarmResultEl.replaceChildren(...nodes);
    applyBusy();
  }

  // resend: { ids, origAt } bei einer Nachsendung, sonst null.
  function showSendResult(action, what, data, resend) {
    const results = Array.isArray(data.results) ? data.results : [];
    const failed = results.filter((r) => !r.ok);
    const reached = results.filter((r) => r.ok);
    const name = (r) => r.label || r.key || '?';
    // Uhrzeit nach der Uhr des NAS (wie der Rest der Seite), nicht nach der
    // evtl. falsch gehenden Uhr dieses Geräts.
    const nowMs = serverNow() * 1000;
    retryBtn = null;
    alarmResultEl.hidden = false;
    alarmResultEl.className = 'result ' + (data.ok === false ? 'result-fail' : 'result-ok');
    const nodes = [el('p', 'result-head',
      `${fmtClockMs(nowMs)} Uhr, ${what}: ${data.message || (data.ok ? 'Gesendet.' : 'Fehler.')}`)];
    // Nur ein Ergebnis MIT Versand ändert die gemerkte Liste. "Nichts
    // gesendet" (keine results, z.B. leere Schnittmenge einer Nachsendung)
    // lässt sie stehen - sonst wären die Namen weg, obwohl niemand erreicht
    // wurde.
    if (results.length) {
      const entryOf = (r) => ({ id: Number(r.id) > 0 ? Number(r.id) : 0, name: name(r), reason: failReason(r) });
      let entries = failed.map(entryOf);
      // Server-Zeit des URSPRÜNGLICHEN Versands - für weitere Nachsendungen.
      let startedAt = Number(data.started_at) > 0 ? Number(data.started_at) : 0;
      if (resend) {
        // Nachsendung: Die alte Liste bleibt die Grundlage - gestrichen wird
        // nur, wer JETZT erreicht wurde; wer erneut scheitert, bekommt den
        // neuen Grund. Wer nicht mitgesendet wurde (ohne id, oder inzwischen
        // aus der Liste gelöscht), bleibt offen.
        startedAt = resend.origAt > 0 ? resend.origAt
          : (lastUnreached && lastUnreached.startedAt) || 0;
        if (lastUnreached && lastUnreached.action === action) {
          const fresh = new Map(entries.filter((e) => e.id > 0).map((e) => [e.id, e]));
          const reachedIds = new Set(reached.map((r) => Number(r.id)).filter((id) => id > 0));
          const merged = [];
          lastUnreached.entries.forEach((e) => {
            if (e.id > 0 && reachedIds.has(e.id)) return;
            if (e.id > 0 && fresh.has(e.id)) {
              merged.push(fresh.get(e.id));
              fresh.delete(e.id);
              return;
            }
            merged.push(e);
          });
          entries.forEach((e) => { if (!(e.id > 0) || fresh.has(e.id)) merged.push(e); });
          entries = merged;
        }
      }
      setUnreached(entries.length ? { action, what, at: nowMs, startedAt, entries } : null);
      if (lastUnreached) {
        nodes.push(el('p', 'result-label', resend
          ? 'Weiterhin NICHT erreicht – bitte anders verständigen (Telefon):'
          : 'NICHT erreicht – bitte anders verständigen (Telefon):'));
        nodes.push(unreachedList());
        if (reached.length) {
          nodes.push(el('p', 'result-reached', `${resend ? 'Jetzt erreicht' : 'Erreicht'}: ${reached.map(name).join(', ')}`));
        }
      }
    } else {
      nodes.push(...unreachedNodes());
    }
    const row = el('div', 'actions result-actions');
    const retry = retryButton();
    if (retry) row.append(retry);
    row.append(dismissButton());
    nodes.push(row);
    alarmResultEl.replaceChildren(...nodes);
    applyBusy();
  }

  // REAL ALARM und ENTWARNUNG gehen direkt vom NAS an Bark - parallel an
  // alle Empfänger, meist ~35 s, schlimmstenfalls ~60 s. Währenddessen
  // bleiben alle Alarm-Knöpfe gesperrt (alarmInFlight). Im Fehlerfall
  // entscheidet die Fehlerart über die Aussage: "NICHT gesendet" nur dann,
  // wenn das sicher ist - sonst "Antwort unklar", damit niemand blind ein
  // zweites Mal alarmiert. onlyIds = Nachsendung nur an diese Empfänger,
  // origAt = Server-Zeit (s) des ursprünglichen Versands (0 = unbekannt).
  // expect_demo geht IMMER mit (der Server lehnt ohne das Feld ab).
  async function sendNasAlarm(action, expectDemo, onlyIds = null, origAt = 0) {
    if (!alarmResultEl) return;
    const what = whatOf(action);
    alarmInFlight = true;
    setBusy(true);
    showResultNote(`${what} wird gesendet… (kann bis zu einer Minute dauern – Seite nicht schließen)`, '');
    try {
      const fields = { action, expect_demo: expectDemo };
      if (onlyIds) {
        fields.only_ids = onlyIds.join(',');
        // So trägt die Nachsendung die Uhrzeit des ERSTEN Versands (plus
        // "verspätet zugestellt") - nicht die der Nachsendung.
        if (origAt > 0) fields.orig_at = String(Math.floor(origAt));
      }
      const data = await post(fields, TIMEOUT_ALARM_MS);
      showSendResult(action, what, data, onlyIds ? { ids: onlyIds, origAt } : null);
    } catch (err) {
      const kind = err instanceof ApiError ? err.kind : 'network';
      let text;
      let unclear = false;
      if (kind === 'auth') {
        text = `${what} NICHT gesendet – Sitzung abgelaufen, bitte neu anmelden.`;
      } else if (kind === 'csrf') {
        text = `${what} NICHT gesendet – ${csrfHint()}.`;
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
      // Ist eine NACHSENDUNG sicher nicht rausgegangen, wird sie wieder
      // angeboten (mit neuer Bestätigung). Bei "Antwort unklar" bewusst
      // nicht - erst den Verlauf prüfen.
      showResultNote(text, unclear ? 'warn' : 'fail', !!onlyIds && !unclear);
      // Der Verlauf zeigt nach ein paar Sekunden, ob der Versand gestartet
      // bzw. abgeschlossen wurde (nas_alarm_started / nas_alarm).
      if (unclear) [5000, 40000].forEach((ms) => setTimeout(refreshLog, ms));
    } finally {
      alarmInFlight = false;
      setBusy(false);
      refresh();
      refreshLog();
      refreshAlarmInfo();
    }
  }

  // ENTWARNUNG (Fehlalarm): Critical Alert mit kurzem, normalem Ton an alle -
  // direkt vom NAS. Muss durchdringen, damit niemand umsonst losfährt.
  const sendFalseAlarm = (expectDemo) => sendNasAlarm('false_alarm', expectDemo);

  // REAL ALARM: bewusst direkt vom NAS (auch bei toter Kette der Box).
  const sendRealAlarm = (expectDemo) => sendNasAlarm('alarm', expectDemo);

  // ==== Empfängerliste (Bark-Keys) ==========================================
  // Eine Zeile pro Person: Name, Zustand und EIN dreiteiliger Schalter
  // Laut | Stumm | Dauerhaft stumm (früher zwei Knöpfe mit wechselnder
  // Beschriftung für diese drei Zustände). Selten Gebrauchtes -
  // Stumm-Lautstärke, Kurzbefehl-Links, Test-Push, Löschen - liegt hinter
  // "Mehr". So bleibt die Liste auch mit zehn Personen überschaubar.

  let keyTestInFlight = false;    // läuft gerade ein Test-Push?
  let lastKeys = null;            // letzte Antwort von keys_list
  const openRows = new Set();     // ids (als Text) mit aufgeklapptem "Mehr"
  const keysRowMsg = makeRowMsg(keysListEl, () => renderKeys());

  const volWords = (vol) => (vol === 0 ? 'lautlos' : `leise (Lautstärke ${vol} von 10)`);

  function renderKeys() {
    if (!keysListEl) return;
    // Offene Bestätigungsfrage in der Liste? Dann später neu zeichnen.
    if (deferWhileArmed(keysListEl, renderKeys)) return;
    // Liste samt Sicherung auf dem NAS unlesbar: deutlich sagen, statt
    // "keine eingetragen" bzw. eine veraltete Liste zu zeigen.
    if (keysCorrupt) {
      setHtml(keysListEl, `<p class="keys-corrupt">${escapeHtml(keysCorrupt)}</p>`);
      return;
    }
    const data = lastKeys;
    if (!data) return;
    const demoOn = !!(demoInfo && demoInfo.enabled);
    if (!data.keys.length) {
      setHtml(keysListEl, '<p class="keys-empty">Noch keine Empfänger eingetragen – die Box nutzt solange ihre fest eingebaute Startliste.</p>');
      return;
    }
    const note = demoOn
      ? `<p class="keys-note">Demo-Modus: Alarmiert wird gerade nur <strong>${escapeHtml(demoInfo.label || 'der Test-Empfänger')}</strong> – alle anderen pausieren.</p>`
      : '';
    setHtml(keysListEl, note + data.keys.map((entry) => {
      // id kommt als Zahl vom Server - trotzdem escaped (Grundregel).
      // data-fkey macht die Bedienelemente für setHtml() wiedererkennbar.
      const id = escapeHtml(String(entry.id));
      const label = escapeHtml(entry.label);
      // Stumm-Lautstärke (0 = lautlos): gilt, sobald der Eintrag stumm ist.
      const vol = Number(entry.mute_volume) || 0;
      // Zeitpunkt der automatischen Laut-Rückschaltung (Sicherheitsnetz).
      const until = Number(entry.mute_until) || 0;
      const permanent = !!entry.mute_permanent;
      const state = !entry.muted ? 'loud' : (permanent ? 'permanent' : 'muted');
      const isDemoTarget = demoOn && Number(entry.id) === Number(demoInfo.key_id);

      let badge = '';
      if (state === 'permanent') {
        badge = '<span class="badge-muted badge-permanent">Dauerhaft stumm</span>';
      } else if (state === 'muted') {
        badge = `<span class="badge-muted">Stumm${until ? ` bis ~${escapeHtml(fmtClock(until))}` : ''}</span>`;
      }
      if (isDemoTarget) badge += '<span class="badge-demo">Demo-Empfänger</span>';
      // muted ist der WIRKSAME Zustand (überfällige Stummschaltung = laut).
      // mute_overdue: Die Rückschaltung ist fällig, konnte aber nicht
      // gespeichert werden - die Box kennt noch "stumm" und alarmiert diesen
      // Empfänger dann evtl. noch mit der Stumm-Lautstärke.
      const overdue = !!entry.mute_overdue;
      if (overdue) badge += '<span class="badge-muted badge-overdue">Rückschaltung nicht gespeichert – Box evtl. noch stumm</span>';

      // Was "stumm" für diese Person gerade heißt - als lesbare Zeile (stand
      // früher nur im Maus-Tooltip des Badges und war am Handy unerreichbar).
      let sub = '';
      if (overdue) {
        sub = `<p class="keys-sub keys-sub-warn">${escapeHtml(`Die automatische Laut-Rückschaltung ist fällig, konnte auf dem NAS aber nicht gespeichert werden (Datenordner nicht beschreibbar?). Der REAL ALARM vom NAS kommt laut an – die Box alarmiert evtl. noch ${volWords(vol)}.`)}</p>`;
      } else if (state !== 'loud') {
        const since = entry.muted_at ? ` Stumm seit ${fmtDayClock(entry.muted_at)} Uhr` : '';
        let tail = '';
        if (state === 'permanent') tail = `${since ? ',' : ''} keine automatische Rückschaltung`;
        else if (until) tail = `${since ? ',' : ''} automatisch wieder laut um ~${fmtClock(until)} Uhr`;
        sub = `<p class="keys-sub">${escapeHtml(`Alarme kommen ${volWords(vol)} an.${since}${tail}${since || tail ? '.' : ''}`)}</p>`;
      }

      let controls = '';
      let more = '';
      if (!readOnly) {
        const open = openRows.has(String(entry.id));
        const seg = (value, text) =>
          `<button type="button" class="seg-btn${state === value ? ' is-on' : ''}" aria-pressed="${state === value}" data-fkey="st-${value}-${id}" data-key-state="${value}" data-key-current="${state}" data-key-id="${id}" data-key-label="${label}" data-key-vol="${vol}">${text}</button>`;
        controls = `<div class="seg" role="group" aria-label="Alarmton für ${label}">${seg('loud', 'Laut')}${seg('muted', 'Stumm')}${seg('permanent', 'Dauerhaft stumm')}</div>
          <button type="button" class="more-btn" data-fkey="more-${id}" data-key-more="${id}" aria-expanded="${open}">${open ? 'Weniger' : 'Mehr'}</button>`;
        if (open) {
          const volOptions = Array.from({ length: 11 }, (_, v) =>
            `<option value="${v}"${v === vol ? ' selected' : ''}>${v === 0 ? '0 (lautlos)' : v}</option>`).join('');
          // Persönliche Kurzbefehl-Links (Geheim-Token, nur der Admin sieht
          // sie): einmal kopieren, in der Kurzbefehle-App hinterlegen.
          const links = entry.mute_token
            ? `<div class="keys-links"><span>Kurzbefehl-Link kopieren:</span>
                 <button type="button" class="chip-link" data-fkey="link-on-${id}" data-key-link="${escapeHtml(entry.mute_token)}" data-key-link-state="on" data-key-id="${id}" data-key-label="${label}">Stumm</button>
                 <button type="button" class="chip-link" data-fkey="link-off-${id}" data-key-link="${escapeHtml(entry.mute_token)}" data-key-link-state="off" data-key-id="${id}" data-key-label="${label}">Laut</button>
               </div>`
            : '';
          more = `<div class="keys-more">
              <label class="keys-volume">Stumm-Lautstärke
                <select data-fkey="vol-${id}" data-key-volume="${id}">${volOptions}</select>
              </label>
              ${links}
              <div class="keys-more-actions">
                <button type="button" data-fkey="test-${id}" data-key-test="${id}" data-key-label="${label}">Test-Push senden</button>
                <button type="button" class="btn-delete" data-fkey="del-${id}" data-key-delete="${id}" data-key-label="${label}">Löschen</button>
              </div>
            </div>`;
        }
      }
      return `<div class="keys-row${state !== 'loud' ? ' muted' : ''}${demoOn && !isDemoTarget ? ' paused' : ''}">
          <div class="keys-head">
            <div class="keys-id"><strong>${label}</strong><code>${escapeHtml(entry.key_masked)}</code>${badge}</div>
            ${controls}
          </div>${sub}${keysRowMsg.html(entry.id)}${more}
        </div>`;
    }).join(''));
  }

  // Antwort "Liste samt Sicherung unlesbar" (code keys_corrupt) - kommt je
  // nach Server-Stand als normale Antwort oder als HTTP-Fehler mit JSON.
  function showKeysCorrupt(data) {
    keysCorrupt = (data && typeof data.message === 'string' && data.message)
      || 'Empfängerliste auf dem NAS beschädigt (Liste und Sicherung unlesbar) – bitte die Dateien im Datenordner prüfen.';
    lastKeys = null;
    keysSummary = null;
    renderKeys();
    renderHeroExtras();
  }

  const refreshKeys = makePoller(async () => {
    try {
      const data = await api('dashboard_api.php?action=keys_list', {}, TIMEOUT_POLL_MS);
      if (!data.ok) {
        if (data.code === 'keys_corrupt') showKeysCorrupt(data);
        return;
      }
      if (!Array.isArray(data.keys)) return;
      keysCorrupt = '';
      updateDemoInfo(data.demo);
      nasKeysVersion = data.version ?? nasKeysVersion;
      lastKeys = data;
      keysSummary = {
        total: data.keys.length,
        muted: data.keys.filter((entry) => entry.muted).length,
        permanent: data.keys.filter((entry) => entry.muted && entry.mute_permanent).length,
        overdue: data.keys.filter((entry) => entry.mute_overdue).length
      };
      // Zeilen, die es nicht mehr gibt, nicht als "aufgeklappt" mitschleppen.
      const ids = new Set(data.keys.map((entry) => String(entry.id)));
      openRows.forEach((id) => { if (!ids.has(id)) openRows.delete(id); });
      renderKeys();
      renderHeroExtras();
      // Dropdown für die Demo-Empfänger-Auswahl aus derselben Liste füllen.
      if (demoTargetEl) {
        const selected = demoTargetEl.value;
        const optionsHtml = data.keys.map((entry) =>
          `<option value="${escapeHtml(String(entry.id))}">${escapeHtml(entry.label)} (${escapeHtml(entry.key_masked)})</option>`).join('');
        setHtml(demoTargetEl, optionsHtml);
        if (selected && [...demoTargetEl.options].some((o) => o.value === selected)) {
          demoTargetEl.value = selected;
        }
      }
    } catch (err) {
      // Netz-Schluckauf: Die zuletzt bekannte Liste stehen lassen, statt sie
      // durch die Fehlerzeile zu ersetzen (das sah nach Datenverlust aus).
      // Nur wenn noch nie etwas geladen wurde, erscheint der Fehler direkt.
      if (err instanceof ApiError && err.kind === 'auth') return;
      if (err instanceof ApiError && err.data && err.data.code === 'keys_corrupt') {
        showKeysCorrupt(err.data);
        return;
      }
      if (keysListEl.__lastHtml === undefined) {
        setHtml(keysListEl, '<p class="keys-empty">Empfängerliste konnte nicht geladen werden – neuer Versuch läuft automatisch.</p>');
      }
    }
  });

  // Eine Aktion an EINEM Empfänger: Die Rückmeldung erscheint in seiner Zeile.
  // opts.listOnOk: Bei Erfolg gibt es die Zeile nicht mehr (Löschen) - dann
  // steht die Meldung unter der Liste. opts.pending: Zwischenstand, solange
  // die Anfrage läuft (Bark-Versand dauert ein paar Sekunden).
  async function keyAction(id, fields, opts = {}) {
    setBusy(true);
    if (opts.pending) keysRowMsg.set(id, opts.pending, 'info');
    let failed = false;
    try {
      const data = await post(fields, opts.timeoutMs || TIMEOUT_POST_MS);
      const text = data.message || (data.ok ? 'Gespeichert.' : 'Fehler.');
      failed = !data.ok;
      if (opts.listOnOk && data.ok) {
        keysRowMsg.set(id, '', 'ok');
        showMsg(keysMsgEl, text, 'ok');
      } else {
        keysRowMsg.set(id, text, kindOf(data));
      }
    } catch (err) {
      failed = true;
      keysRowMsg.set(id, errorText(err), errorKind(err));
    } finally {
      setBusy(false);
      // Nach einem Fehlschlag zeigt ein umgestelltes Bedienelement (z.B. die
      // Lautstärke-Auswahl) noch den NICHT gespeicherten Wert: Die Person hat
      // es verändert, nicht setHtml() - bei gleichem HTML würde deshalb nicht
      // neu gezeichnet. Neuzeichnen erzwingen, damit wieder der Stand des
      // Servers dasteht.
      if (failed) forceRedraw(keysListEl, renderKeys);
      refreshKeys();
      refreshLog();
    }
  }

  // Basis-Adresse für die Kurzbefehl-Links: bevorzugt die öffentliche
  // Adresse aus config.php (meta public-base) - wer das Dashboard intern
  // über die NAS-IP öffnet, soll trotzdem einen Link bekommen, der auch
  // unterwegs auf dem iPhone funktioniert. Immer GENAU ein "/" am Ende,
  // egal ob die Konfiguration einen hat; ungültig/leer -> aktuelle Adresse.
  function muteLinkBase() {
    if (publicBase) {
      try {
        const base = new URL(publicBase, location.href);
        base.pathname = base.pathname.replace(/\/*$/, '/');
        base.search = '';
        base.hash = '';
        return base;
      } catch (err) {
        // ungültige Konfiguration -> Fallback unten
      }
    }
    return new URL('./', location.href);
  }

  keysListEl?.addEventListener('click', async (event) => {
    // Dreiteiliger Schalter Laut | Stumm | Dauerhaft stumm. Bestätigt wird
    // nur die Richtung, in der jemand weniger hört; "lauter" geht direkt.
    const segBtn = event.target.closest('[data-key-state]');
    if (segBtn) {
      const want = segBtn.dataset.keyState;
      const current = segBtn.dataset.keyCurrent;
      const id = segBtn.dataset.keyId;
      const name = segBtn.dataset.keyLabel;
      if (want === current) return;
      if (want === 'loud') {
        keyAction(id, { action: 'keys_mute', id, muted: '0' });
        return;
      }
      if (want === 'muted' && current === 'permanent') {
        // Zurück von "dauerhaft" auf zeitlich begrenzt: Das Sicherheitsnetz
        // greift wieder - die sichere Richtung, also ohne Rückfrage.
        keyAction(id, { action: 'keys_set_mute_permanent', id, permanent: '0' });
        return;
      }
      // Die Frage nennt die TATSÄCHLICHE Stumm-Lautstärke dieses Eintrags:
      // "OHNE Ton" stimmt nur bei 0 - sonst kommt der Alarm leise an.
      const vol = parseInt(segBtn.dataset.keyVol, 10) || 0;
      const how = vol === 0 ? 'OHNE Ton' : `nur noch leise (Stumm-Lautstärke ${vol} von 10)`;
      // Die Frage erscheint unter der Kopfzeile des Eintrags; der Schalter
      // bleibt sichtbar (er zeigt weiter den aktuellen Zustand).
      const place = { after: segBtn.closest('.keys-head'), hide: null };
      if (want === 'muted') {
        armInline(segBtn,
          `"${name}" stumm schalten (Arbeitsmodus)? Alarme kommen dort dann ${how} an (aber weiterhin sichtbar).`,
          'Ja, stumm schalten',
          () => keyAction(id, { action: 'keys_mute', id, muted: '1' }), null, place);
      } else {
        armInline(segBtn,
          `"${name}" DAUERHAFT stumm schalten? Alarme kommen dort weiterhin sichtbar an, aber ${how} – und die automatische Rückschaltung greift nicht mehr, bis das hier wieder beendet wird.`,
          'Ja, dauerhaft stumm',
          () => keyAction(id, { action: 'keys_set_mute_permanent', id, permanent: '1' }), null, place);
      }
      return;
    }

    const moreBtn = event.target.closest('[data-key-more]');
    if (moreBtn) {
      const id = moreBtn.dataset.keyMore;
      if (openRows.has(id)) openRows.delete(id);
      else openRows.add(id);
      renderKeys();
      return;
    }

    // Persönlichen Kurzbefehl-Link (Geheim-Token) in die Zwischenablage
    // kopieren - der Link schaltet den Eintrag stumm (state=on) bzw. laut.
    const linkBtn = event.target.closest('[data-key-link]');
    if (linkBtn) {
      const url = new URL('api/mute.php', muteLinkBase());
      url.searchParams.set('t', linkBtn.dataset.keyLink);
      url.searchParams.set('state', linkBtn.dataset.keyLinkState);
      const what = linkBtn.dataset.keyLinkState === 'on' ? 'Stumm' : 'Laut';
      const id = linkBtn.dataset.keyId;
      const fkey = linkBtn.dataset.fkey;
      try {
        await navigator.clipboard.writeText(url.toString());
        keysRowMsg.set(id, `„${what}“-Link für ${linkBtn.dataset.keyLabel} kopiert – im iPhone-Kurzbefehl unter „Inhalte von URL abrufen“ einfügen.`, 'ok');
        // Erfolg auch direkt am Chip zeigen. Die Liste wurde durch die
        // Meldung gerade neu gezeichnet - den Chip deshalb neu suchen.
        const chip = keysListEl.querySelector(`[data-fkey="${CSS.escape(fkey)}"]`);
        if (chip) {
          chip.classList.add('copied');
          chip.textContent = 'kopiert';
          setTimeout(() => {
            chip.classList.remove('copied');
            chip.textContent = what;
          }, 1600);
        }
      } catch (err) {
        // Zwischenablage kann blockiert sein (alter Browser, fehlende
        // Berechtigung) - dann den Link zum manuellen Kopieren anzeigen.
        keysRowMsg.set(id, `Kopieren nicht möglich – Link („${what}“): ${url.toString()}`, 'warn');
      }
      return;
    }

    // Test-Push: normale, nicht kritische Mitteilung mit dem Alarmton -
    // prüft einen neuen Key (und den Ton auf dem iPhone) ohne echten Alarm.
    // Mit Rückfrage: Auf dem iPhone der Person spielt dabei der ALARMTON -
    // das soll nicht mit einem einzelnen (versehentlichen) Tipp passieren.
    const testPushBtn = event.target.closest('[data-key-test]');
    if (testPushBtn) {
      const id = testPushBtn.dataset.keyTest;
      const name = testPushBtn.dataset.keyLabel;
      armInline(testPushBtn,
        `Test-Push an "${name}" senden? Auf dem iPhone dort spielt einmal der ALARMTON – als normale Mitteilung, kein Alarm (bei Stummschalter oder Fokus bleibt sie leise). Am besten vorher Bescheid geben.`,
        'Ja, Test-Push senden',
        () => {
          // Solange ein Test-Push unterwegs ist, keinen zweiten hinterherschicken.
          if (keyTestInFlight) return;
          keyTestInFlight = true;
          keyAction(id, { action: 'keys_test', id },
            { pending: `Test-Push an ${name} wird gesendet…`, timeoutMs: TIMEOUT_SEND_ONE_MS })
            .finally(() => { keyTestInFlight = false; });
        });
      return;
    }

    const delBtn = event.target.closest('[data-key-delete]');
    if (!delBtn) return;
    const id = delBtn.dataset.keyDelete;
    armInline(delBtn,
      `Empfänger "${delBtn.dataset.keyLabel}" wirklich löschen? Er bekommt dann KEINE Alarme mehr!`,
      'Ja, löschen',
      () => keyAction(id, { action: 'keys_delete', id }, { listOnOk: true }));
  });

  // Stumm-Lautstärke pro Empfänger (0 = lautlos): wirkt, sobald der Eintrag
  // stumm geschaltet ist - für beide Alarmwege.
  keysListEl?.addEventListener('change', (event) => {
    const sel = event.target.closest('[data-key-volume]');
    if (!sel) return;
    const id = sel.dataset.keyVolume;
    keyAction(id, { action: 'keys_set_mute_volume', id, volume: sel.value });
  });

  keysAddForm?.addEventListener('submit', async (event) => {
    event.preventDefault();
    const label = keysAddForm.elements.label.value.trim();
    const key = keysAddForm.elements.key.value.trim();
    setBusy(true);
    try {
      const data = await post({ action: 'keys_add', label, key });
      showMsg(keysMsgEl, data.message || 'Fehler.', kindOf(data));
      if (data.ok) keysAddForm.reset();
    } catch (err) {
      showMsg(keysMsgEl, errorText(err), errorKind(err));
    } finally {
      setBusy(false);
      refreshKeys();
      refreshLog();
    }
  });

  // ==== Meldungen an den Admin ==============================================
  // Welche Statusmeldungen das NAS dem Admin schickt und wie. Arten,
  // Beschriftungen und ALLE Auswahlmöglichkeiten kommen vom Server
  // (notify_list) - hier wird nur gezeichnet und jede Änderung einzeln
  // gespeichert (notify_set). Das Panel gibt es nur für den Admin.

  let notifyData = null;
  const openNotify = new Set();   // Arten mit aufgeklappten Einstellungen
  // Läuft gerade eine Probe-Meldung? Dann sind alle "Probe senden"-Knöpfe
  // gesperrt - der Versand dauert bis ~20 s, und ein zweiter Klick schickte
  // sonst eine zweite (evtl. kritische) Meldung hinterher.
  let probeInFlight = false;
  const notifyRowMsg = notifyListEl ? makeRowMsg(notifyListEl, () => renderNotify()) : null;

  const selectHtml = (opt, type, fkey) =>
    `<label class="opt"><span class="opt-label">${escapeHtml(opt.label)}</span>
       <select data-fkey="${fkey}" data-notify-type="${escapeHtml(type)}" data-notify-opt="${escapeHtml(opt.key)}">${opt.choices.map((c) =>
    `<option value="${escapeHtml(c.value)}"${c.value === opt.value ? ' selected' : ''}>${escapeHtml(c.text)}</option>`).join('')}</select>
     </label>`;

  function renderNotify() {
    const data = notifyData;
    if (!data || !notifyListEl) return;
    // Offene Bestätigungsfrage (wichtige Meldung abschalten)? Später zeichnen.
    if (deferWhileArmed(notifyListEl, renderNotify)) return;
    if (notifyNoteEl) {
      notifyNoteEl.hidden = !!data.configured;
      notifyNoteEl.textContent = data.configured ? ''
        : 'In config.php ist kein Status-Empfänger eingetragen (bark_key_status) – das NAS verschickt deshalb gerade gar keine Meldungen. Die Einstellungen hier greifen, sobald der Key gesetzt ist.';
    }
    // Zusammenfassung an der eingeklappten Liste: wie viele Arten an sind.
    if (notifySummaryEl) {
      const on = data.types.filter((t) => t.enabled).length;
      const summary = `${on} von ${data.types.length} Meldungen eingeschaltet – anzeigen und einstellen`;
      if (notifySummaryEl.textContent !== summary) notifySummaryEl.textContent = summary;
    }
    setHtml(notifyListEl, data.types.map((t) => {
      const key = escapeHtml(t.key);
      const open = openNotify.has(t.key);
      // Eingestellte Dringlichkeit als kleines Etikett - aber nur, wenn sie
      // vom leisen Normalfall abweicht.
      const level = t.options.find((o) => o.key === 'level');
      const levelText = level ? ((level.choices.find((c) => c.value === level.value) || {}).text || '') : '';
      const badge = t.enabled && level && level.value !== 'passive'
        ? `<span class="badge-level">${escapeHtml(levelText.replace(/\s*\(.*$/, ''))}</span>` : '';
      const more = open
        ? `<div class="notify-more">
             ${t.options.map((o) => selectHtml(o, t.key, `nopt-${key}-${escapeHtml(o.key)}`)).join('')}
             <button type="button" data-fkey="ntest-${key}" data-notify-test="${key}"${probeInFlight ? ' disabled' : ''}>Probe senden</button>
           </div>`
        : '';
      return `<div class="notify-row${t.enabled ? '' : ' is-off'}">
          <div class="notify-head">
            <label class="notify-main">
              <input type="checkbox" class="switch" role="switch" data-fkey="nsw-${key}" data-notify-switch="${key}" data-notify-label="${escapeHtml(t.label)}" data-notify-important="${t.important ? '1' : '0'}"${t.enabled ? ' checked' : ''}>
              <span class="notify-text"><strong>${escapeHtml(t.label)}</strong>${badge}<span class="notify-hint">${escapeHtml(t.hint)}</span></span>
            </label>
            <button type="button" class="more-btn" data-fkey="nmore-${key}" data-notify-more="${key}" aria-expanded="${open}">${open ? 'Weniger' : 'Einstellen'}</button>
          </div>${notifyRowMsg.html(t.key)}${more}
        </div>`;
    }).join(''));
    if (notifyGlobalEl) {
      setHtml(notifyGlobalEl, data.global.map((o) => selectHtml(o, 'global', `ngl-${escapeHtml(o.key)}`)).join('')
        + (data.quiet_now ? '<p class="notify-quiet">Gerade ist Ruhezeit.</p>' : ''));
    }
  }

  const refreshNotify = makePoller(async () => {
    if (!notifyListEl) return;
    try {
      const data = await api('dashboard_api.php?action=notify_list', {}, TIMEOUT_POLL_MS);
      if (!data.ok) return;
      notifyData = data;
      renderNotify();
    } catch (err) {
      if (err instanceof ApiError && err.kind === 'auth') return;
      if (notifyListEl.__lastHtml === undefined) {
        setHtml(notifyListEl, '<p class="keys-empty">Einstellungen konnten nicht geladen werden – neuer Versuch läuft automatisch.</p>');
      }
    }
  });

  // Rückmeldung zu einer Art steht in ihrer Zeile, zu "Für alle Meldungen"
  // direkt unter diesem Block.
  function notifyMsg(type, text, kind) {
    if (type === 'global') showMsg(notifyMsgEl, text, kind);
    else notifyRowMsg.set(type, text, kind);
  }

  async function notifySet(type, key, value) {
    setBusy(true);
    let failed = false;
    try {
      const data = await post({ action: 'notify_set', type, key, value });
      failed = !data.ok;
      notifyMsg(type, data.message || (data.ok ? 'Gespeichert.' : 'Fehler.'), kindOf(data));
    } catch (err) {
      failed = true;
      notifyMsg(type, errorText(err), errorKind(err));
    } finally {
      setBusy(false);
      // Nicht gespeichert: Schalter bzw. Auswahl stehen noch auf dem Wert,
      // den die Person gewählt hat - der Server hat aber den alten. Beide
      // Bereiche zwingend neu zeichnen, sonst sähe z.B. eine Meldung
      // eingeschaltet aus, die es nicht ist.
      if (failed) {
        if (notifyGlobalEl) notifyGlobalEl.__lastHtml = null;
        forceRedraw(notifyListEl, renderNotify);
      }
      refreshNotify();
      refreshLog();
      refresh();   // der Wächter-Hinweis hängt an zwei dieser Einstellungen
    }
  }

  notifyPanelEl?.addEventListener('change', (event) => {
    const sw = event.target.closest('[data-notify-switch]');
    if (sw) {
      const type = sw.dataset.notifySwitch;
      if (!sw.checked && sw.dataset.notifyImportant === '1') {
        // Sicherheitsrelevante Meldung abschalten: erst nachfragen. Bis zur
        // Bestätigung springt der Schalter zurück auf "ein".
        sw.checked = true;
        armInline(sw,
          `Meldung „${sw.dataset.notifyLabel}“ wirklich abschalten? Das würde dir dann NICHT mehr gemeldet.`,
          'Ja, abschalten',
          () => notifySet(type, 'enabled', '0'), null, { after: sw.closest('.notify-head'), hide: null });
        return;
      }
      notifySet(type, 'enabled', sw.checked ? '1' : '0');
      return;
    }
    const sel = event.target.closest('[data-notify-opt]');
    if (sel) notifySet(sel.dataset.notifyType, sel.dataset.notifyOpt, sel.value);
  });

  notifyPanelEl?.addEventListener('click', async (event) => {
    const moreBtn = event.target.closest('[data-notify-more]');
    if (moreBtn) {
      const type = moreBtn.dataset.notifyMore;
      if (openNotify.has(type)) openNotify.delete(type);
      else openNotify.add(type);
      renderNotify();
      return;
    }
    const testBtnEl = event.target.closest('[data-notify-test]');
    if (!testBtnEl) return;
    if (probeInFlight) return;
    const type = testBtnEl.dataset.notifyTest;
    probeInFlight = true;
    setBusy(true);
    notifyRowMsg.set(type, 'Probe-Meldung wird gesendet…', 'info');
    try {
      const data = await post({ action: 'notify_test', type }, TIMEOUT_SEND_ONE_MS);
      notifyRowMsg.set(type, data.message || (data.ok ? 'Gesendet.' : 'Fehler.'), kindOf(data));
    } catch (err) {
      notifyRowMsg.set(type, errorText(err), errorKind(err));
    } finally {
      probeInFlight = false;
      setBusy(false);
      renderNotify();   // Knöpfe wieder freigeben
    }
  });

  // ==== Verlauf (Ereignisse aus dem NAS-Protokoll) ==========================
  // Anders als "Letzter Alarm" im Status überlebt dieser Verlauf einen
  // Neustart der Box, weil er auf dem NAS liegt (commands.log).

  // Die Box schickt als Alarm-Info "[13.11. 21:42] <Bark-Text>" (ASCII, weil
  // derselbe Text an Bark geht). Für den Verlauf zählt daraus nur die Zeit,
  // zu der die BOX den Alarm erkannt hat.
  function boxAlarmTime(info) {
    const match = /^\[([^\]]+)\]/.exec(String(info || ''));
    if (match) return ` (von der Box erkannt: ${match[1]})`;
    return info ? `: ${info}` : '';
  }

  function describeLogEntry(entry) {
    const who = entry.by ? ` – von ${entry.by}` : '';
    const cmd = `Befehl #${entry.id} ${entry.type || ''}`.trim();
    switch (entry.event) {
      case 'created':
        return { text: `${cmd} angelegt${who}`, cls: '' };
      case 'delivered':
        return { text: `${cmd} an die Box ausgeliefert`, cls: '' };
      case 'acked':
        return { text: `${cmd} bestätigt${entry.result ? `: ${fmtAckResult(entry.result)}` : ''}${entry.message ? ` (${fmtAckMessage(entry.message)})` : ''}`, cls: '' };
      case 'expired':
        return { text: `${cmd} abgelaufen (die Box hat ihn nicht abgeholt)`, cls: 'log-warn' };
      case 'cancelled':
        // Mit Grund (entry.message) hat die Sicherung im NAS den Befehl
        // verworfen, z.B. weil der Demo-Modus inzwischen gewechselt hat.
        return {
          text: `${cmd} abgebrochen${who}${entry.message ? ` – ${entry.message}` : ''}`,
          cls: entry.message ? 'log-warn' : ''
        };
      case 'late_ack':
        return { text: `Verspätete Bestätigung zu Befehl #${entry.id}${entry.type ? ` ${entry.type}` : ''}${entry.result ? `: ${fmtAckResult(entry.result)}` : ''}`, cls: 'log-warn' };
      case 'demo_enabled':
        return { text: `Demo-Modus EIN – Alarme nur an "${entry.label}"${who}`, cls: 'log-warn' };
      case 'demo_disabled':
        return { text: `Demo-Modus AUS – wieder LIVE${who}`, cls: '' };
      case 'demo_repaired':
        // Vom NAS selbst erkannt und behoben (keine weiteren Felder).
        return { text: 'Demo-Zustand war beschädigt – automatisch auf LIVE zurückgesetzt, die Box holt die volle Empfängerliste', cls: 'log-warn' };
      case 'key_added':
        return { text: `Empfänger "${entry.label}" (${entry.key}) hinzugefügt${who}`, cls: '' };
      case 'key_removed':
        return { text: `Empfänger "${entry.label}" (${entry.key}) gelöscht${who}`, cls: '' };
      case 'key_muted': {
        const src = entry.source === 'shortcut' ? ' – per Kurzbefehl-Link' : who;
        return { text: `Empfänger "${entry.label}" stummgeschaltet (Arbeitsmodus, Alarme mit Stumm-Lautstärke)${src}`, cls: 'log-warn' };
      }
      case 'key_unmuted': {
        const src = entry.source === 'shortcut' ? ' – per Kurzbefehl-Link'
          : (entry.source === 'auto' ? ' – automatisch (Zeitlimit erreicht)' : who);
        return { text: `Empfänger "${entry.label}" wieder laut geschaltet${src}`, cls: '' };
      }
      case 'key_mute_volume':
        return { text: `Stumm-Lautstärke für "${entry.label}" auf ${entry.volume === 0 ? '0 (lautlos)' : entry.volume} gestellt${who}`, cls: '' };
      case 'key_mute_permanent': {
        const src = entry.source === 'shortcut' ? ' – per Kurzbefehl-Link' : who;
        return entry.permanent
          ? { text: `Empfänger "${entry.label}" DAUERHAFT stumm geschaltet (keine automatische Rückschaltung mehr)${src}`, cls: 'log-warn' }
          : { text: `Dauerhafte Stummschaltung für "${entry.label}" beendet (läuft wieder automatisch aus)${src}`, cls: '' };
      }
      case 'key_test':
        return entry.ok
          ? { text: `Test-Push an "${entry.label}" gesendet${who}`, cls: '' }
          : { text: `Test-Push an "${entry.label}" FEHLGESCHLAGEN${who}`, cls: 'log-warn' };
      case 'notify_changed':
        // key 'enabled' = eine Art von Meldung wurde ein- bzw. abgeschaltet;
        // sonst wurde eine einzelne Einstellung geändert.
        return entry.key === 'enabled'
          ? { text: `Meldung an den Admin "${entry.label}" ${entry.value}${who}`, cls: entry.value === 'abgeschaltet' ? 'log-warn' : '' }
          : { text: `Meldungen an den Admin, ${entry.label} – ${entry.option}: ${entry.value}${who}`, cls: '' };
      // *_started wird VOR dem Versand geschrieben: Steht danach kein
      // Abschluss-Eintrag (nas_alarm/false_alarm) im Verlauf, ist der Versand
      // abgebrochen - wichtig nach einer "Antwort unklar"-Meldung.
      // demo (neuere Server): Versand lief im Demo-Modus, also nur an den
      // Test-Empfänger. Die Abschluss-Einträge sagen das schon im Text.
      case 'nas_alarm_started':
        return { text: (entry.resend
          ? `REAL ALARM: Nachsendung an ${entry.resend} Empfänger gestartet`
          : 'REAL ALARM gestartet') + (entry.demo === true ? ' (DEMO-MODUS)' : '') + who, cls: 'log-alarm' };
      case 'false_alarm_started':
        return { text: (entry.resend
          ? `ENTWARNUNG: Nachsendung an ${entry.resend} Empfänger gestartet`
          : 'ENTWARNUNG gestartet') + (entry.demo === true ? ' (DEMO-MODUS)' : '') + who, cls: 'log-warn' };
      case 'nas_alarm':
        return { text: `REAL ALARM direkt vom NAS: ${entry.message || ''}${who}`, cls: 'log-alarm' };
      case 'false_alarm':
        return { text: `ENTWARNUNG (Fehlalarm) gesendet: ${entry.message || ''}${who}`, cls: 'log-warn' };
      case 'relay_alarm':
        // source 'remote' = per Dashboard-Befehl ausgelöster DEMO-Alarm (kein Melder).
        return entry.source === 'remote'
          ? { text: `DEMO-Alarm über die Box ausgelöst (Dashboard-Befehl)${boxAlarmTime(entry.info)}`, cls: 'log-warn' }
          : { text: `RELAISALARM – der Melder hat ausgelöst${boxAlarmTime(entry.info)}`, cls: 'log-alarm' };
      case 'box_offline':
        return { text: `Box OFFLINE gemeldet (kein Status seit ${entry.minutes} min)`, cls: 'log-warn' };
      case 'box_recovered':
        return { text: `Box wieder online (Ausfall ca. ${entry.minutes} min)`, cls: '' };
      case 'box_restart':
        return { text: `Box neu gestartet – Grund: ${RESET_REASONS[entry.reason] || entry.reason || 'unbekannt'}`, cls: 'log-warn' };
      case 'nas_web_failed':
        return { text: `NAS-Dashboard gestört: ${entry.detail || 'Selbsttest fehlgeschlagen'}`, cls: 'log-alarm' };
      case 'nas_web_recovered':
        return { text: `NAS-Dashboard wieder in Ordnung (Störung ca. ${entry.minutes} min)`, cls: '' };
      case 'watcher_silent':
        return { text: `Außen-Wächter (Cloud) meldet sich nicht (seit ${entry.minutes} min)`, cls: 'log-warn' };
      case 'watcher_recovered':
        return { text: `Außen-Wächter (Cloud) wieder da (Pause ca. ${entry.minutes} min)`, cls: '' };
      default:
        return { text: String(entry.event || '?'), cls: '' };
    }
  }

  // Standardmäßig die letzten 25 Ereignisse; der "Ältere anzeigen"-Knopf
  // erhöht in 25er-Schritten bis zum Server-Deckel von 100.
  const LOG_STEP = 25;
  const LOG_MAX = 100;
  let logLimit = LOG_STEP;
  // Filter: Die täglichen Stumm-Wechsel der Kurzbefehle verdrängen sonst die
  // Alarme aus dem sichtbaren Fenster.
  let logFilter = 'all';
  const LOG_EMPTY = {
    all: 'Noch keine Ereignisse aufgezeichnet.',
    alarm: 'Keine Alarme im Verlauf.',
    keys: 'Keine Änderungen an den Empfängern im Verlauf.',
    system: 'Keine System-Ereignisse im Verlauf.'
  };

  const refreshLog = makePoller(async () => {
    if (!logEl) return;
    const filter = logFilter;
    try {
      const data = await api(`dashboard_api.php?action=log_view&limit=${logLimit}&filter=${filter}`, {}, TIMEOUT_POLL_MS);
      // Filter inzwischen gewechselt: Diese Antwort gehört zum alten - die
      // Abfrage für den neuen läuft schon hinterher (makePoller).
      if (!data.ok || filter !== logFilter) return;
      const entries = data.entries || [];
      // Knopf nur zeigen, wenn es voraussichtlich noch mehr gibt (die Seite
      // kam "voll" zurück) und der Deckel noch nicht erreicht ist.
      if (logMoreBtn) logMoreBtn.hidden = logLimit >= LOG_MAX || entries.length < logLimit;
      if (!entries.length) {
        setHtml(logEl, `<p class="log-plain">${escapeHtml(LOG_EMPTY[filter] || LOG_EMPTY.all)}</p>`);
        return;
      }
      const now = Number(data.server_time) || Math.floor(Date.now() / 1000);
      // Pro Tag eine Überschrift, in der Zeile die genaue Uhrzeit. Früher
      // stand dort "vor 3 Std" - gerundet, und die genaue Zeit nur im
      // Maus-Tooltip (am Handy unerreichbar).
      let html = '';
      let lastDay = null;
      entries.forEach((entry) => {
        const day = dayKey(entry.at);
        if (day !== lastDay) {
          if (lastDay !== null) html += '</ol>';
          html += `<h3 class="log-day">${escapeHtml(fmtDayLabel(entry.at, now))}</h3><ol class="log-list">`;
          lastDay = day;
        }
        const { text, cls } = describeLogEntry(entry);
        html += `<li class="log-row${cls ? ' ' + cls : ''}">
          <span class="log-dot" aria-hidden="true"></span>
          <time class="log-time" datetime="${escapeHtml(new Date(entry.at * 1000).toISOString())}">${escapeHtml(fmtClock(entry.at))}</time>
          <span class="log-text">${escapeHtml(text)}</span>
        </li>`;
      });
      html += '</ol>';
      setHtml(logEl, html);
    } catch (err) {
      if (err instanceof ApiError && err.kind === 'auth') return;
      // Wie bei der Empfängerliste: letzten bekannten Stand stehen lassen.
      if (logEl.__lastHtml === undefined) {
        setHtml(logEl, '<p class="log-plain">Verlauf konnte nicht geladen werden – neuer Versuch läuft automatisch.</p>');
      }
    }
  });

  // Jüngster Alarm für den Held-Bereich: eigene kleine Abfrage nur über die
  // Versand-Ereignisse (filter=alarm_core) - unabhängig davon, was im
  // Verlaufs-Panel gerade gefiltert oder von Stumm-Wechseln verdrängt ist,
  // und ohne die Befehls-Ereignisse der Demo-Alarme, die sonst den echten
  // letzten Alarm aus dem Fenster schieben könnten.
  const refreshAlarmInfo = makePoller(async () => {
    try {
      const data = await api('dashboard_api.php?action=log_view&limit=12&filter=alarm_core', {}, TIMEOUT_POLL_MS);
      if (!data.ok) return;
      alarmInfo = deriveAlarmInfo(data.entries || []);
      renderHeroExtras();
    } catch (err) {
      // Die Fakten-Zeile behält den letzten bekannten Stand.
    }
  });

  document.querySelectorAll('[data-log-filter]').forEach((btn) => {
    btn.addEventListener('click', () => {
      if (btn.dataset.logFilter === logFilter) return;
      logFilter = btn.dataset.logFilter;
      logLimit = LOG_STEP;
      document.querySelectorAll('[data-log-filter]').forEach((other) =>
        other.setAttribute('aria-pressed', other === btn ? 'true' : 'false'));
      refreshLog();
    });
  });

  testBtn?.addEventListener('click', enqueueTest);
  cancelBtn?.addEventListener('click', cancelCommand);
  demoToggleBtn?.addEventListener('click', toggleDemo);
  logMoreBtn?.addEventListener('click', () => {
    logLimit = Math.min(LOG_MAX, logLimit + LOG_STEP);
    refreshLog();
  });
  // Demo-Chip in der Kopfzeile: springt zum Demo-Schalter - der Block muss
  // dafür aufgeklappt sein (der Sprung selbst ist der normale Anker-Link).
  demoChipEl?.addEventListener('click', () => {
    if (demoDetailsEl) demoDetailsEl.open = true;
  });

  // ==== Zweistufige Inline-Bestätigung für Aktionen mit Folgen ==============
  // Ein natives confirm() ist mit einem einzigen (auch versehentlichen)
  // Enter-Druck bestätigt und blockiert obendrein das 5-s-Polling. Stattdessen:
  // Der 1. Klick "schärft" nur - es erscheint die Frage mit separatem
  // Bestätigungsknopf, die sich nach einer Frist von selbst wieder entschärft.
  // Gilt für ALLE bestätigungspflichtigen Aktionen (Alarm/Entwarnung,
  // Demo-Wechsel, Stummschalten, Löschen, wichtige Meldung abschalten), damit
  // sich die Seite überall gleich anfühlt. Der Bestätigungsknopf bekommt
  // bewusst KEINEN Auto-Fokus: Wer Enter auf dem Auslöse-Knopf drückt, kann
  // nicht blind doppelt bestätigen - der Fokus landet stattdessen auf
  // "Abbrechen" (die sichere Richtung).

  // Frist: mindestens 10 s, bei langen Fragen mehr - sonst lief die Zeit ab,
  // bevor man eine der längeren Fragen (ca. 40 Wörter) zu Ende gelesen hatte.
  const ARM_TIMEOUT_MIN_S = 10;
  const ARM_TIMEOUT_MAX_S = 25;
  const armTimeoutFor = (text) => Math.min(ARM_TIMEOUT_MAX_S, Math.max(ARM_TIMEOUT_MIN_S,
    Math.round(4 + String(text).trim().split(/\s+/).length * 0.4)));

  let armedBtn = null;      // höchstens EIN geschärfter Knopf gleichzeitig
  let disarmFn = null;
  // Nur bei Alarm-Knöpfen gesetzt: der Modus (expect_demo), den die offene
  // Bestätigungsfrage beschreibt. updateDemoInfo() entschärft, sobald der
  // tatsächliche Modus davon abweicht.
  let armedExpect = null;
  // Fortlaufende Nummer für die ids der Bestätigungsfragen (aria-Bezüge).
  let armSeq = 0;

  // Neuaufbau einer Liste aufschieben, solange darin eine Bestätigungsfrage
  // offen ist: Sonst verschwände die Frage beim 30-s-Abruf (oder durch die
  // Rückmeldung einer anderen Zeile) kommentarlos, mitten im Lesen. Nach dem
  // Entschärfen wird nachgeholt. render prüft dabei selbst erneut - wurde
  // inzwischen wieder etwas in derselben Liste geschärft, wartet es weiter.
  const deferredRenders = new Map();   // Listen-Element -> render()
  function deferWhileArmed(listEl, render) {
    if (!armedBtn || !listEl || !listEl.contains(armedBtn)) return false;
    deferredRenders.set(listEl, render);
    return true;
  }
  function flushDeferredRenders() {
    const pending = [...deferredRenders.values()];
    deferredRenders.clear();
    pending.forEach((render) => render());
  }

  function disarm() {
    if (disarmFn) disarmFn();
  }

  // Schärft einen konkreten Knopf und zeigt Frage + Bestätigen/Abbrechen.
  // Normalfall: Die Frage ersetzt den Knopf (er wird versteckt). opts.after =
  // anderes Element, hinter dem die Frage erscheinen soll; opts.hide = was
  // währenddessen versteckt wird (null = nichts - z.B. bleibt der dreiteilige
  // Stumm-Schalter sichtbar). Funktioniert auch für Knöpfe in neu gezeichneten
  // Listen: Deren Neuaufbau wartet, solange die Frage offen ist
  // (deferWhileArmed); wird eine Liste trotzdem neu aufgebaut, verschwindet
  // die Frage einfach mit - es kann nichts unbeabsichtigt ausgelöst werden.
  // Der Countdown rechnet gegen eine feste Frist (Date.now()) statt Ticks zu
  // zählen: iOS pausiert Timer im Hintergrund - eine Bestätigung darf nach
  // der Rückkehr nicht noch minutenlang scharf sein.
  // Barrierefreiheit: Die Bestätigung ist eine Gruppe, benannt durch ihre
  // Frage; beide Knöpfe verweisen zusätzlich per aria-describedby darauf -
  // so liest ein Screenreader beim Fokus auf "Abbrechen" die Frage mit vor.
  function armInline(btn, questionText, confirmLabel, onConfirm, expectDemo = null, opts = {}) {
    disarm();
    const anchor = opts.after || btn;
    const hideEl = opts.hide === undefined ? btn : opts.hide;
    const timeoutS = armTimeoutFor(questionText);
    const questionId = `arm-q-${++armSeq}`;
    const box = document.createElement('div');
    box.className = 'arm-confirm';
    box.setAttribute('role', 'group');
    box.setAttribute('aria-labelledby', questionId);
    const question = document.createElement('span');
    question.className = 'arm-question';
    question.id = questionId;
    question.textContent = questionText;
    const yes = document.createElement('button');
    yes.type = 'button';
    yes.className = 'danger';
    yes.textContent = confirmLabel;
    yes.setAttribute('aria-describedby', questionId);
    const cancel = document.createElement('button');
    cancel.type = 'button';
    cancel.textContent = 'Abbrechen';
    cancel.setAttribute('aria-describedby', questionId);
    const countdown = document.createElement('span');
    countdown.className = 'arm-count';
    // Der Viertelsekunden-Countdown wäre vorgelesen nur Rauschen.
    countdown.setAttribute('aria-hidden', 'true');
    const deadline = Date.now() + timeoutS * 1000;
    const secondsLeft = () => Math.ceil((deadline - Date.now()) / 1000);
    countdown.textContent = `${timeoutS} s`;
    box.append(question, yes, cancel, countdown);
    if (hideEl) hideEl.hidden = true;
    anchor.after(box);
    const timer = setInterval(() => {
      const left = secondsLeft();
      if (left <= 0) {
        disarm();
      } else {
        countdown.textContent = `${left} s`;
      }
    }, 250);
    armedBtn = btn;
    armedExpect = expectDemo;
    disarmFn = () => {
      clearInterval(timer);
      // Stand der Fokus in der Bestätigung (Tastatur-Bedienung), geht er
      // zurück auf den Auslöse-Knopf statt ins Leere auf <body>.
      const hadFocus = box.contains(document.activeElement);
      box.remove();
      if (hideEl) hideEl.hidden = false;
      if (hadFocus && btn.isConnected) btn.focus();
      armedBtn = null;
      armedExpect = null;
      disarmFn = null;
      // Aufgeschobenen Neuaufbau nachholen - im nächsten Takt: Schärft
      // armInline() gleich danach einen anderen Knopf derselben Liste, würde
      // ein sofortiger Neuaufbau genau diesen Knopf aus dem DOM werfen.
      if (deferredRenders.size) setTimeout(flushDeferredRenders, 0);
    };
    cancel.addEventListener('click', disarm);
    yes.addEventListener('click', () => {
      // Frist schon abgelaufen (Timer lief im Hintergrund nicht)? Dann nur
      // entschärfen - NICHT auslösen.
      const expired = secondsLeft() <= 0;
      disarm();
      if (!expired) onConfirm();
    });
    // Fokus auf "Abbrechen": Tastatur-Nutzer verlieren den Fokus nicht (der
    // Auslöse-Knopf ist evtl. versteckt), und ein blindes Enter bricht ab.
    cancel.focus();
  }

  // Alarm-Knöpfe: Frage UND Erwartung kommen aus DERSELBEN Momentaufnahme
  // von demoInfo. Die Erwartung geht als expect_demo an den Server - hat
  // sich der Modus bis zum Bestätigen geändert, sendet er nichts (HTTP 409).
  function armButton(btn, getQuestion, confirmLabel, onConfirm) {
    if (!btn) return;   // Lese-Rolle: Knopf existiert im HTML nicht
    btn.addEventListener('click', () => {
      if (alarmInFlight || btn.disabled) return;
      const snapshot = demoInfo;
      const expect = expectDemoOf(snapshot);
      armInline(btn, getQuestion(snapshot), confirmLabel, () => onConfirm(expect), expect);
    });
  }

  armButton(alarmBtn, (info) => info && info.enabled
      ? `DEMO-MODUS: Alarm geht nur an "${info.label || 'Test-Empfänger'}". Jetzt senden?`
      : 'ECHTEN FEUERWEHR-ALARM direkt vom NAS an alle Empfänger senden?',
    'Ja, Alarm auslösen', sendRealAlarm);

  armButton(espAlarmBtn, (info) =>
      `DEMO-MODUS: Alarm über die Box auslösen? Er durchläuft die komplette Alarm-Kette der Box und geht an "${info?.label || 'Test-Empfänger'}". Danach greift der normale 5-Minuten-Cooldown der Box.`,
    'Ja, Demo-Alarm auslösen', enqueueEspAlarm);

  armButton(falseAlarmBtn, (info) => info && info.enabled
      ? `DEMO-MODUS: Entwarnung geht nur an "${info.label || 'Test-Empfänger'}". Jetzt senden?`
      : 'ENTWARNUNG an ALLE senden? („Der letzte Alarm war ein FEHLALARM“ – Critical Alert mit kurzem Ton, kommt auch durch Stummschalter durch)',
    'Ja, Entwarnung senden', sendFalseAlarm);

  // ==== ?-Tooltips per Tipp/Klick ===========================================
  // Auf dem Handy gibt es kein Hover: Ein Tipp auf das ? öffnet die
  // Sprechblase, ein zweiter Tipp (oder ein Tipp irgendwo daneben bzw.
  // Escape) schließt sie wieder. Es ist immer höchstens eine Blase offen.

  // aria-expanded spiegelt den Blasen-Zustand für Screenreader (das ? ist
  // ein role="button" mit aufklappbarer Erklärung).
  function setTipOpen(el, open) {
    el.classList.toggle('tip-open', open);
    el.setAttribute('aria-expanded', open ? 'true' : 'false');
  }

  function closeTips(except) {
    document.querySelectorAll('.help.tip-open').forEach((el) => {
      if (el !== except) setTipOpen(el, false);
    });
  }

  document.addEventListener('click', (event) => {
    const help = event.target.closest('.help');
    closeTips(help);
    if (help) setTipOpen(help, !help.classList.contains('tip-open'));
  });

  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape') {
      closeTips(null);
      disarm();   // Escape entschärft auch eine offene Alarm-Bestätigung
    } else if ((event.key === 'Enter' || event.key === ' ')
        && event.target instanceof Element && event.target.classList.contains('help')) {
      // Tastatur-Bedienung: Enter/Leertaste auf dem fokussierten ? schaltet
      // die Blase wie ein Klick um.
      event.preventDefault();
      event.target.click();
    }
  });

  // ==== Polling nur bei sichtbarem Tab ======================================
  // Ein tagelang im Hintergrund offener Tab würde das NAS sonst rund um die
  // Uhr alle 5 s abfragen. Beim Zurückwechseln wird sofort aktualisiert.

  let timers = [];

  function startPolling() {
    if (authLost || timers.length) return;
    refresh();
    refreshKeys();
    refreshLog();
    refreshAlarmInfo();
    refreshNotify();
    // .tick: Der Timer überspringt einen Takt, solange die vorige Abfrage
    // noch läuft (siehe makePoller).
    timers = [
      setInterval(refresh.tick, 5000),
      setInterval(refreshKeys.tick, 30000),
      setInterval(refreshLog.tick, 30000),
      setInterval(refreshAlarmInfo.tick, 60000),
      setInterval(refreshNotify.tick, 60000),
      setInterval(tick, 1000),
      // Zeitangaben im Held-Bereich ("vor 12 min") laufen auch ohne neue
      // Daten weiter.
      setInterval(renderHeroExtras, 30000)
    ];
  }

  function stopPolling() {
    timers.forEach(clearInterval);
    timers = [];
  }

  // Tab im Hintergrund: Polling aus UND jede offene Bestätigung entschärfen -
  // wer zurückkommt, soll nicht eine (evtl. veraltete) Alarm-Frage mit einem
  // Tipp bestätigen können.
  // Die Messung "Box hat die Liste seit X nicht übernommen" beginnt nach der
  // Rückkehr neu: Während der Tab im Hintergrund lag, wurde nichts
  // beobachtet - die Zeit dazwischen darf nicht als "nicht übernommen" zählen.
  document.addEventListener('visibilitychange', () => {
    mismatchSince = 0;
    mismatchPair = '';
    if (document.hidden) {
      disarm();
      stopPolling();
    } else {
      startPolling();
    }
  });

  // Gemerkte nicht Erreichte dieser Browser-Sitzung (z.B. vor einem
  // Neuladen nach Ablauf der Anmeldung) wieder anzeigen - samt Nachsende-
  // Knopf, dessen Frage den dann aktuellen Modus nennt.
  if (alarmResultEl) {
    const restored = restoreUnreached();
    if (restored) {
      lastUnreached = restored;
      showResultNote('Aus dieser Browser-Sitzung gemerkt (vor dem Neuladen der Seite):', 'warn', true);
    }
  }

  startPolling();
})();
