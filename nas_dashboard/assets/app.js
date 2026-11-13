(function () {
  const csrf = document.querySelector('meta[name="csrf"]')?.content || '';
  // Nur-Lese-Benutzer: Knöpfe fehlen dann im HTML; hier zusätzlich nichts
  // rendern, was Änderungen auslösen würde (der Server blockt ohnehin).
  const readOnly = (document.querySelector('meta[name="role"]')?.content || 'admin') !== 'admin';
  // Öffentliche Basis-Adresse für die Kurzbefehl-Links (aus config.php, über
  // index.php). Leer = aus der aktuellen Adresse ableiten.
  const publicBase = (document.querySelector('meta[name="public-base"]')?.content || '').trim();
  const stateEl = document.querySelector('[data-state]');
  const fieldsEl = document.querySelector('[data-fields]');
  const commandEl = document.querySelector('[data-command]');
  const updatedEl = document.querySelector('[data-updated]');
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
  const demoStateEl = document.querySelector('[data-demo-state]');
  const demoTargetEl = document.querySelector('[data-demo-target]');
  const demoToggleBtn = document.querySelector('[data-demo-toggle]');
  const demoMsgEl = document.querySelector('[data-demo-msg]');
  const commandMsgEl = document.querySelector('[data-command-msg]');
  const watchdogEl = document.querySelector('[data-watchdog]');
  const logEl = document.querySelector('[data-log]');
  const miniLampEl = document.querySelector('[data-state-mini]');

  const STATE_LABELS = {
    pending: 'wartet auf ESP32',
    delivered: 'an ESP32 ausgeliefert, ACK offen',
    acked: 'bestätigt',
    expired: 'abgelaufen (ESP32 hat nicht gepollt)',
    cancelled: 'abgebrochen'
  };

  // Befehls-Quittungen der Firmware sind feste englische Tokens/Sätze - hier
  // übersetzt, damit die prominenteste Zeile der Seite nicht Denglisch ist.
  // Unbekannte Werte (künftige Firmware) erscheinen unverändert.
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
  // ~35 s, schlimmstenfalls ~60 s) - deshalb dort großzügig.
  const TIMEOUT_POLL_MS = 10000;
  const TIMEOUT_POST_MS = 20000;
  const TIMEOUT_ALARM_MS = 120000;
  // So lange darf die letzte erfolgreiche Status-Abfrage höchstens her sein,
  // bevor der Hero auf "Status veraltet" (gelb) wechselt.
  const STALE_AFTER_MS = 15000;
  // Alle Zeitangaben in der Zeitzone der Wache - egal, wie die Uhr des
  // anzeigenden Geräts eingestellt ist.
  const TZ = 'Europe/Berlin';

  // Große Statuslampe im Held-Bereich + Mini-Lampe in der Kopfzeile in einem
  // Rutsch setzen. cls: 'ok' (grün), 'stale' (gelb = Aussage unsicher),
  // 'crit' (rot, ruhig) oder 'alarm' (rot, blinkend - Alarm läuft gerade).
  function setState(word, cls) {
    // Nur bei echter Änderung schreiben: [data-state] ist eine aria-live-Region,
    // und identischer Text alle 5 s würde Screenreader unnötig erneut ansagen.
    if (stateEl.textContent !== word) stateEl.textContent = word;
    const stateClass = 'state state-' + cls;
    if (stateEl.className !== stateClass) stateEl.className = stateClass;
    if (miniLampEl) {
      const miniClass = 'mini-lamp mini-' + cls;
      if (miniLampEl.className !== miniClass) miniLampEl.className = miniClass;
    }
  }

  // Sitzung serverseitig abgelaufen (HTTP 401): deutlich machen statt still
  // mit veralteten Daten weiterzulaufen, dann zur Login-Seite neu laden.
  let authLost = false;
  function handleAuthLoss() {
    if (authLost) return;
    authLost = true;
    stopPolling();
    document.title = '\u{1F512} ' + PAGE_TITLE;
    setState('Abgemeldet', 'stale');
    updatedEl.textContent = 'Sitzung abgelaufen – die Anmeldeseite wird geladen…';
    setTimeout(() => location.reload(), 1500);
  }

  function formatTime(epoch) {
    if (!epoch) return 'nie';
    return new Date(epoch * 1000).toLocaleString('de-DE', { timeZone: TZ });
  }

  // Nur die Uhrzeit (HH:MM) - für "Stumm bis ~18:30". Das Datum wäre hier
  // Rauschen, die Stummschaltung endet spätestens nach wenigen Stunden.
  function fmtClock(epoch) {
    return new Date(epoch * 1000).toLocaleTimeString('de-DE', { hour: '2-digit', minute: '2-digit', timeZone: TZ });
  }

  // Uhrzeit mit Sekunden aus einem Browser-Zeitstempel (ms) - für "zuletzt
  // erfolgreich abgerufen um 14:03:22".
  function fmtClockMs(ms) {
    return new Date(ms).toLocaleTimeString('de-DE', { timeZone: TZ });
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
    [alarmBtn, espAlarmBtn, falseAlarmBtn].forEach((btn) => {
      if (btn) btn.disabled = busy || alarmInFlight;
    });
  }

  function setBusy(busy) {
    busyCount = Math.max(0, busyCount + (busy ? 1 : -1));
    applyBusy();
  }

  // Flag-Werte kommen vom ESP32 als '0'/'1'-Strings.
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
    // sich nichts fokussieren (z.B. die Kurzbefehl-Link-Chips).
    openDetails.forEach((key) => { const d = find(key); if (d) d.open = true; });
    if (focusKey) {
      const target = find(focusKey);
      if (target) target.focus({ preventScroll: true });
    }
  }

  // ==== Statusfelder: Formatierung + Erklärungen ============================
  // Die Rohwerte kommen 1:1 vom ESP32 (0/1, open/closed, Sekunden). Hier werden
  // sie in verständliche Anzeigen übersetzt; das ?-Symbol zeigt beim Überfahren
  // die Erklärung zu jedem Wert.

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

  // Neustart-Gründe des ESP32 (esp_reset_reason). Watchdog/Absturz/Brownout
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
  const HEAP_WARN = 50000;
  const MAX_ALLOC_WARN = 30000;
  const lowBytes = (v, limit) => { const b = parseInt(v, 10); return !isNaN(b) && b < limit; };

  // Woher kam der letzte Alarm? (Feld alarm_source neuerer Firmware)
  const ALARM_SOURCES = {
    relay: 'Melder-Relais',
    remote: 'DEMO-Alarm über ESP32 (Dashboard-Befehl)'
  };

  // Alles, was die Box meldet, wird angezeigt - aber VERDICHTET: Die 22
  // Rohwerte des Status-Pushs werden zu 12 verständlichen Zeilen in drei
  // Gruppen zusammengefasst (z.B. Signalstärke + IP = eine WLAN-Zeile).
  // value() baut den Anzeigetext, cls() liefert ''/'warn'/'crit' für die
  // Warnfarbe; ctx = { offline, seenAge } für Sonderfälle (Offline-Anzeige).
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
      tip: 'Wie viele Empfänger beim letzten/laufenden Alarm erfolgreich benachrichtigt wurden (erreicht/gesamt). Läuft die Zustellung noch, versucht die Box automatisch weiter, bis alle versorgt sind (Nachsende-Puffer).',
      value: (s) => {
        if (flagged(s.alarm_pending)) return `läuft – bisher ${show(s.key_progress)}`;
        if (na(s.last_alarm) || s.last_alarm === 'nie') return '–';
        return `${show(s.key_progress)} erreicht`;
      },
      cls: (s) => flagged(s.alarm_pending) ? 'crit' : '' },
    { group: 'Alarmkette', label: 'Letzter Alarm',
      tip: 'Zeitpunkt und Text des letzten ausgelösten Alarms (aus Sicht des ESP32, seit dem letzten Neustart) und – bei neuerer Firmware – woher er kam: Melder-Relais oder DEMO-Alarm per Dashboard-Befehl. "nie" heißt: kein Alarm seit dem letzten Einschalten.',
      value: (s) => {
        const text = show(s.last_alarm);
        if (na(s.alarm_source) || na(s.last_alarm) || s.last_alarm === 'nie') return text;
        return `${text} · ${ALARM_SOURCES[s.alarm_source] || String(s.alarm_source)}`;
      } },
    { group: 'Verbindung & Abgleich', label: 'WLAN',
      tip: 'Empfangsstärke des ESP32 im WLAN des Magazins und seine aktuelle Adresse. Näher an 0 dBm ist besser: ab ca. -67 gut, unter ca. -80 unzuverlässig – dann Box oder Router umstellen. Ohne WLAN kann die Box weder Bark-Alarme senden noch das NAS erreichen.',
      value: (s) => {
        if (s.wifi === 'down') return 'GETRENNT';
        if (na(s.rssi)) return 'n/a';
        return `${fmtRssi(s.rssi)} · ${show(s.ip)}`;
      },
      cls: (s) => {
        if (s.wifi === 'down') return 'crit';
        const dbm = parseInt(s.rssi, 10);
        return !isNaN(dbm) && dbm <= -80 ? 'warn' : '';
      } },
    { group: 'Verbindung & Abgleich', label: 'Empfängerliste (Box)',
      tip: 'Empfängerliste, wie sie der ESP32 gespeichert hat: Anzahl, davon stumm (Arbeitsmodus) und die Listen-Version. Stimmt die Version mit dem NAS überein, ist die Liste (auch ein Demo-Wechsel) angekommen. Im Demo-Modus steht hier genau 1 Empfänger.',
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
        const failed = !na(s.heartbeat_ok) && !flagged(s.heartbeat_ok);
        return String(s.last_heartbeat) + (failed ? ' – letzter Versuch fehlgeschlagen' : '');
      },
      cls: (s) => (!na(s.heartbeat_ok) && !flagged(s.heartbeat_ok)) ? 'warn' : '' },
    { group: 'Verbindung & Abgleich', label: 'Befehls-Quittung',
      tip: 'Hat die Box einen NAS-Befehl ausgeführt, dessen Bestätigung (ACK) noch nicht beim NAS angekommen ist? Normalerweise "keine offen" – ein Rückstand wird automatisch nachgereicht.',
      value: (s) => na(s.ack_pending) ? 'n/a'
        : (flagged(s.ack_pending) ? 'offen – wird nachgereicht' : 'keine offen'),
      cls: (s) => flagged(s.ack_pending) ? 'warn' : '' },
    { group: 'Box & Wartung', label: 'Box / Firmware',
      tip: 'Name der Box (device_id aus config.h), der Kompilier-Zeitpunkt der laufenden Firmware und – bei neuerer Firmware – die Version des Arduino-ESP32-Cores. So sieht man, ob wirklich der neueste Stand läuft.',
      value: (s) => (na(s.device_id) && na(s.fw_build)) ? 'n/a'
        : `${show(s.device_id)} · ${show(s.fw_build)}${na(s.core) ? '' : ` · Core ${s.core}`}` },
    { group: 'Box & Wartung', liveWhenOffline: true,
      label: (ctx) => ctx.offline ? 'Offline seit' : 'Läuft seit',
      tip: (ctx) => ctx.offline
        ? 'Zeit seit dem letzten Lebenszeichen des ESP32 beim NAS. Mögliche Ursachen: Stromausfall, WLAN weg oder NAS nicht erreichbar.'
        : 'Zeit seit dem letzten Neustart des ESP32. Ein unerwarteter Sprung auf wenige Minuten bedeutet: Die Box hat neu gestartet (z.B. Stromausfall oder Watchdog).',
      value: (s, ctx) => ctx.offline
        ? (Number.isFinite(ctx.seenAge) ? fmtUptime(ctx.seenAge) : 'unbekannt (noch nie gemeldet)')
        : (na(s.uptime_s) ? 'n/a' : fmtUptime(s.uptime_s)),
      cls: (s, ctx) => ctx.offline ? 'crit' : '' },
    { group: 'Box & Wartung', label: 'Letzter Neustart-Grund',
      tip: 'Warum der ESP32 zuletzt neu gestartet ist. "Einschalten"/"Software-Neustart" sind normal. Watchdog/Absturz deuten auf ein Firmware-Problem, Brownout auf ein Netzteil-/Kabelproblem hin.',
      value: (s) => na(s.reset_reason) ? 'n/a' : (RESET_REASONS[s.reset_reason] || String(s.reset_reason)),
      cls: (s) => RESET_WARN.includes(String(s.reset_reason)) ? 'warn' : '' },
    { group: 'Box & Wartung', label: 'Freier Speicher',
      tip: 'Freier Arbeitsspeicher (Heap) des ESP32. Normal sind grob 150–250 kB. Sinkt der Wert über Tage stetig, deutet das auf ein Speicherleck hin; unter ~50 kB wird es kritisch. "Größter Block" (neuere Firmware) ist das größte zusammenhängende freie Stück – unter ~30 kB können HTTPS-Verbindungen scheitern, obwohl in Summe noch genug frei ist.',
      value: (s) => {
        if (na(s.free_heap)) return 'n/a';
        const maxAlloc = parseInt(s.max_alloc_heap, 10);
        return fmtHeap(s.free_heap) + (isNaN(maxAlloc) ? '' : ` · größter Block ${Math.round(maxAlloc / 1024)} kB`);
      },
      cls: (s) => (lowBytes(s.free_heap, HEAP_WARN) || lowBytes(s.max_alloc_heap, MAX_ALLOC_WARN)) ? 'warn' : '' },
    // NAS-seitig, nicht Box-seitig: Der Wächter (DSM-Aufgabe) muss auch dann
    // laufen, wenn die Box offline ist - deshalb liveWhenOffline.
    { group: 'Box & Wartung', label: 'Offline-Wächter (NAS)', liveWhenOffline: true,
      tip: 'Die DSM-Aufgabe auf dem NAS prüft alle 15 Minuten, ob die Box sich noch meldet, und schickt sonst eine Bark-Warnung. Hier steht, ob diese Aufgabe selbst noch läuft – der Wächter darf nicht unbemerkt ausfallen.',
      value: (s, ctx) => ctx.watchdog ? ctx.watchdog.text : 'n/a',
      cls: (s, ctx) => ctx.watchdog && ctx.watchdog.warn ? 'warn' : '' }
  ];

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
      const label = typeof row.label === 'function' ? row.label(ctx) : row.label;
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
      const tipId = `tip-${groups.length}-${current.rows.length}`;
      current.rows.push(`<div class="tele-row${cls ? ' tele-' + cls : ''}${liveCls}">
        <span class="tele-label">${escapeHtml(label)} <span class="help" data-fkey="${tipId}" tabindex="0" role="button" aria-expanded="false" aria-label="Erklärung zu „${escapeHtml(label)}“" aria-describedby="${tipId}"><span aria-hidden="true">?</span><span class="tip-bubble" id="${tipId}" role="tooltip">${escapeHtml(tip)}</span></span></span>
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

  function renderCommand(command, serverTime) {
    if (command && serverTime) {
      const finishedAt = {
        acked: command.acked_at,
        expired: command.expired_at,
        cancelled: command.cancelled_at
      }[command.state];
      if (finishedAt && serverTime - finishedAt > COMMAND_LINGER_S) command = null;
    }
    if (!command) {
      // Leerzustand gedimmt (Klasse weg = muted-Grundfarbe): "Kein Befehl
      // aktiv" ist der Normalfall und soll nicht wie eine Meldung wirken.
      commandEl.textContent = 'Kein Befehl aktiv.';
      commandEl.classList.remove('command-active');
      if (cancelBtn) cancelBtn.hidden = true;
      return;
    }
    const label = STATE_LABELS[command.state] || command.state;
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
    commandEl.textContent = text;
    commandEl.classList.add('command-active');
    if (cancelBtn) cancelBtn.hidden = command.state !== 'pending';
  }

  // Selbstüberwachung des Offline-Wächters: Der Wächter, der das Schweigen
  // der Box melden soll, darf nicht selbst unbemerkt schweigen. Der Zustand
  // wird EINMAL bewertet und zweifach genutzt: als ruhige Statuszeile in
  // "Box & Wartung" (immer) und als Warnzeile im Hero (NUR im Warnfall -
  // "läuft normal" wäre dort Dauerrauschen im wichtigsten Panel).
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
    return { warn: false, text: `läuft (zuletzt ${age < 60 ? 'gerade eben' : 'vor ' + fmtUptime(age)})` };
  }

  function renderWatchdog(state) {
    if (!watchdogEl) return;
    const warn = !!(state && state.warn);
    watchdogEl.textContent = warn ? `Offline-Wächter: ${state.text}` : '';
    watchdogEl.classList.toggle('warn-text', warn);
  }

  // ==== Demo-Modus ==========================================================
  // Der Wechsel greift auf dem ESP32 erst nach dessen nächstem Poll. Deshalb
  // vergleicht das Banner die Listen-Version des ESP32 mit der des NAS und
  // sagt klar, ob man schon gefahrlos testen kann.

  // syncUnknown: Das NAS liefert gerade keine aktuellen Daten - dann lässt
  // sich NICHT sagen, ob der ESP32 die Demo-Liste übernommen hat (eine alte
  // "übernommen"-Aussage stehen zu lassen, wäre gefährlich).
  function renderDemo(status, offline, syncUnknown) {
    if (!demoBannerEl) return;
    const espVersion = status.keys_version;
    const synced = !offline && espVersion !== undefined && nasKeysVersion !== null
      && String(espVersion) === String(nasKeysVersion);

    if (demoInfo && demoInfo.enabled) {
      let syncText;
      if (syncUnknown) {
        syncText = 'Sync-Status unbekannt – keine aktuelle Antwort vom NAS. Nicht davon ausgehen, dass gefahrlos getestet werden kann!';
      } else if (offline) {
        syncText = 'Achtung: Der ESP32 ist OFFLINE – ein Relaisalarm würde noch mit der ALTEN Liste senden!';
      } else if (synced) {
        syncText = 'Der ESP32 hat die Demo-Liste übernommen – du kannst jetzt gefahrlos testen.';
      } else {
        syncText = 'Achtung: Der ESP32 hat die Demo-Liste NOCH NICHT übernommen (wartet auf den nächsten Poll) – ein Relaisalarm würde noch an ALLE gehen!';
      }
      demoBannerEl.hidden = false;
      setHtml(demoBannerEl, `<strong>DEMO-MODUS AKTIV</strong> – Alarme gehen NUR an
        <strong>${escapeHtml(demoInfo.label || 'Test-Empfänger')}</strong>
        (<code>${escapeHtml(demoInfo.key_masked || '')}</code>), seit ${escapeHtml(formatTime(demoInfo.changed_at))}.<br>${syncText}`);
    } else {
      demoBannerEl.hidden = true;
    }

    if (demoStateEl && demoInfo) {
      if (demoInfo.enabled) {
        setHtml(demoStateEl, `Aktuell: <strong class="mode-demo">DEMO-MODUS</strong> – Test-Empfänger:
          <strong>${escapeHtml(demoInfo.label || '?')}</strong>`);
      } else {
        setHtml(demoStateEl, 'Aktuell: <strong class="mode-live">LIVE</strong> – Alarme gehen an alle Empfänger.');
      }
    }
    if (demoToggleBtn && demoInfo) {
      demoToggleBtn.disabled = busyCount > 0;
      demoToggleBtn.textContent = demoInfo.enabled
        ? 'Demo-Modus beenden (zurück zu LIVE)'
        : 'Demo-Modus einschalten';
      // Rot markiert den Wechsel zurück zu LIVE (danach sind Alarme wieder "scharf").
      demoToggleBtn.classList.toggle('danger', demoInfo.enabled);
    }
    if (demoTargetEl) {
      // Auswahl nur zeigen, solange sie gebraucht wird (zum Einschalten).
      demoTargetEl.hidden = !!(demoInfo && demoInfo.enabled);
    }
    // Der ESP32-Testalarm ist ein reines Demo-Werkzeug - im LIVE-Modus wäre er
    // ein echter Alarm an alle (der Server blockt das zusätzlich serverseitig).
    if (espAlarmBtn) {
      const showEspAlarm = !!(demoInfo && demoInfo.enabled);
      if (armedBtn === espAlarmBtn) {
        // Seine Inline-Bestätigung ist gerade offen (der Knopf selbst ist
        // versteckt): Endet der Demo-Modus währenddessen, wird entschärft.
        if (!showEspAlarm) {
          disarm();
          espAlarmBtn.hidden = true;
        }
      } else {
        espAlarmBtn.hidden = !showEspAlarm;
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
    demoInfo = next;
    if (armedExpect !== null && expectDemoOf(demoInfo) !== armedExpect) {
      disarm();
      if (alarmResultEl) {
        alarmResultEl.hidden = false;
        alarmResultEl.classList.remove('result-fail');
        alarmResultEl.classList.add('result-warn');
        alarmResultEl.textContent = 'Der Demo-/LIVE-Modus hat sich gerade geändert – Bestätigung zurückgesetzt. Bitte Lage prüfen und bei Bedarf neu auslösen.';
      }
    }
  }

  // HTTP 409 mode_changed: Der Server hat NICHTS gesendet, weil sein Modus
  // nicht zur Bestätigungsfrage passte. Seinen aktuellen Stand übernehmen
  // und das Banner sofort neu zeichnen (kein automatischer Neuversuch!).
  function applyModeChanged(err) {
    if (!err || err.kind !== 'mode_changed' || !err.data || !err.data.demo) return;
    updateDemoInfo(err.data.demo);
    renderDemo(lastStatus || {}, lastOffline, !!statusProblem);
  }

  async function performDemoSet(fields) {
    setBusy(true);
    try {
      const data = await post(fields);
      if (demoMsgEl) demoMsgEl.textContent = data.message || 'Fehler.';
      if (data.ok && data.demo) updateDemoInfo(data.demo);
    } catch (err) {
      if (demoMsgEl) demoMsgEl.textContent = errorText(err);
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
      if (demoMsgEl) demoMsgEl.textContent = 'Bitte zuerst einen Empfänger in der Liste anlegen und als Test-Empfänger auswählen.';
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
        return 'Sicherheits-Token veraltet – Seite neu laden. Aktion wurde NICHT ausgeführt.';
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

  const refresh = makePoller(async () => {
    try {
      const data = await api('dashboard_api.php?action=status', {}, TIMEOUT_POLL_MS);
      if (!data.ok) throw new ApiError('http', data.message || 'Unerwartete Antwort vom NAS.', 200, data);
      const status = data.status || {};
      updateDemoInfo(data.demo);
      nasKeysVersion = data.nas_keys_version ?? nasKeysVersion;
      statusProblem = null;
      problemRenderKey = null;
      lastOkAt = Date.now();
      // Ampel-Logik der Statuslampe: ein laufender Alarm (Zustellung noch
      // offen) übersteuert das grüne "Einsatzbereit" - der wichtigste
      // Zustand gewinnt. Offline schlägt alles.
      const alarmActive = !data.offline && flagged(status.alarm_pending);
      // Klemmender Kontakt: Die Box ist zwar online, kann aber keinen NEUEN
      // Alarm erkennen (wartet auf Kontakt-Öffnung) - das ist NICHT
      // einsatzbereit. Maßgeblich ist allein stuck_warned: Das setzt die Box
      // erst, wenn der Kontakt UNGEWÖHNLICH lange zu bleibt (30 min). Direkt
      // nach jedem Alarm ist "Kontakt zu + wartet auf Öffnung" dagegen der
      // Normalzustand (bis der Melder quittiert ist) - das darf nicht als
      // Störung erscheinen. Ein reiner Cooldown bleibt ebenfalls grün.
      const stuck = !data.offline && flagged(status.stuck_warned);
      if (data.offline) setState('Box offline', 'crit');
      else if (alarmActive) setState('Alarm aktiv', 'alarm');
      else if (stuck) setState('Kontakt klemmt – keine neuen Alarme erkennbar', 'stale');
      else setState('Einsatzbereit', 'ok');
      // Status auch im Tab-Titel: so sieht man ohne Tab-Wechsel, ob alles läuft.
      document.title = (alarmActive ? '\u{1F6A8} ' : data.offline ? '\u{1F534} ' : stuck ? '⚠️ ' : '\u{1F7E2} ') + PAGE_TITLE;
      // seen_age_seconds ist null, wenn sich die Box noch NIE gemeldet hat
      // (Number(null) wäre 0 und würde "Offline seit 0 Min" anzeigen).
      const seenAge = data.seen_age_seconds == null ? NaN : Number(data.seen_age_seconds);
      const ageText = fmtAgeShort(seenAge);
      updatedEl.textContent = `Letzter Status: ${formatTime(status.seen_at)}${ageText ? ` (${ageText})` : ''}`;
      const wdState = watchdogState(data.watchdog, Number(data.server_time));
      lastStatus = status;
      lastOffline = !!data.offline;
      lastSeenAge = seenAge;
      lastWd = wdState;
      renderFields(status, !!data.offline, seenAge, wdState, null);
      renderCommand(data.command, Number(data.server_time));
      renderWatchdog(wdState);
      renderDemo(status, !!data.offline, false);
    } catch (err) {
      if (err instanceof ApiError && err.kind === 'auth') return;   // handleAuthLoss() übernimmt
      statusProblem = err || new ApiError('http', '', 0, null);
      showStatusProblem();
    }
  });

  // Das NAS liefert gerade keine aktuellen Daten: NICHT dasselbe wie "ESP32
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
    const renderKey = [word, text, lastOkAt, nasKeysVersion,
      demoInfo ? `${demoInfo.enabled}:${demoInfo.key_id}` : ''].join('|');
    if (renderKey === problemRenderKey) return;
    problemRenderKey = renderKey;
    setState(word, 'stale');
    document.title = '⚠️ ' + PAGE_TITLE;
    const note = `Keine aktuellen Daten vom NAS – die gedimmten Werte sind der letzte bekannte Stand${lastOkAt ? ` (abgerufen um ${fmtClockMs(lastOkAt)} Uhr)` : ''}.`;
    if (lastStatus) {
      renderFields(lastStatus, lastOffline, lastSeenAge, lastWd, note);
    } else {
      fieldsEl.classList.add('is-stale');
      setHtml(fieldsEl, `<p class="stale-note">${escapeHtml(note)}</p>`);
    }
    renderDemo(lastStatus || {}, lastOffline, true);
  }

  // Sekündlicher Ticker (nur bei sichtbarem Tab): Kam zu lange keine
  // erfolgreiche Status-Antwort, darf der Hero nicht weiter grün
  // "Einsatzbereit" zeigen. Bei einem bekannten Fehler hält er zusätzlich
  // die Altersangabe ("letzter erfolgreicher Abruf vor 23 s") aktuell.
  function tick() {
    if (authLost) return;
    if (statusProblem || (lastOkAt && Date.now() - lastOkAt > STALE_AFTER_MS)) showStatusProblem();
  }

  function post(fields, timeoutMs = TIMEOUT_POST_MS) {
    const form = new FormData();
    Object.entries(fields).forEach(([key, value]) => form.set(key, value));
    return api('dashboard_api.php', {
      method: 'POST',
      headers: { 'X-CSRF-Token': csrf },
      body: form
    }, timeoutMs);
  }

  // Ergebnis inline statt per alert(): alert() blockiert die ganze Seite
  // (inklusive des 5-s-Pollings), bis jemand auf OK klickt.
  function showCommandMsg(text) {
    if (commandMsgEl) commandMsgEl.textContent = text;
  }

  async function enqueueTest() {
    setBusy(true);
    try {
      const data = await post({ action: 'enqueue', type: 'TEST' });
      showCommandMsg(data.message || (data.ok ? 'Befehl angelegt.' : 'Fehler.'));
    } catch (err) {
      showCommandMsg(errorText(err));
    } finally {
      setBusy(false);
      refresh();
      refreshLog();
    }
  }

  // DEMO-Alarm über den ESP32: legt einen ALARM-Befehl in die Warteschlange,
  // den die Box beim nächsten Poll abholt und wie einen echten Relaisalarm
  // ausführt (startAlarm + Nachsende-Puffer). Der Server erlaubt das NUR im
  // Demo-Modus und nur, wenn der ESP32 die Demo-Liste schon übernommen hat.
  // expect_demo geht zusätzlich mit (der Server darf es prüfen, muss aber nicht).
  async function enqueueEspAlarm(expectDemo) {
    alarmInFlight = true;
    setBusy(true);
    try {
      const data = await post({ action: 'enqueue', type: 'ALARM', expect_demo: expectDemo });
      showCommandMsg(data.message || (data.ok ? 'Befehl angelegt.' : 'Fehler.'));
    } catch (err) {
      applyModeChanged(err);
      showCommandMsg(errorText(err));
    } finally {
      alarmInFlight = false;
      setBusy(false);
      refresh();
      refreshLog();
    }
  }

  // Grund eines fehlgeschlagenen Empfängers: http 0 heißt beim NAS-Versand
  // "keine Verbindung zu Bark bzw. Zeitüberschreitung" - nicht "HTTP 0".
  const failReason = (r) => (Number(r.http) === 0 ? 'keine Verbindung/Timeout' : `HTTP ${r.http}`);

  // REAL ALARM und ENTWARNUNG gehen direkt vom NAS an Bark - parallel an
  // alle Empfänger, meist ~35 s, schlimmstenfalls ~60 s. Währenddessen
  // bleiben alle Alarm-Knöpfe gesperrt (alarmInFlight), und das Ergebnis
  // erscheint pro Empfänger. Im Fehlerfall entscheidet die Fehlerart über
  // die Aussage: "NICHT gesendet" nur dann, wenn das sicher ist - sonst
  // "Antwort unklar", damit niemand blind ein zweites Mal alarmiert.
  async function sendNasAlarm(action, expectDemo) {
    if (!alarmResultEl) return;
    const what = action === 'alarm' ? 'Alarm' : 'Entwarnung';
    alarmInFlight = true;
    setBusy(true);
    alarmResultEl.hidden = false;
    alarmResultEl.classList.remove('result-fail', 'result-warn');
    alarmResultEl.textContent = `${what} wird gesendet… (kann bis zu einer Minute dauern – Seite nicht schließen)`;
    try {
      const data = await post({ action, expect_demo: expectDemo }, TIMEOUT_ALARM_MS);
      // Namen statt maskierter Keys: Unter Stress zählt, WER fehlt.
      const lines = (data.results || []).map((r) =>
        `${r.label || r.key} ${r.ok ? 'OK' : `FEHLER (${failReason(r)})`}`);
      alarmResultEl.textContent = `${data.message || (data.ok ? 'Gesendet.' : 'Fehler.')}` +
        (lines.length ? ` [${lines.join(', ')}]` : '');
      // Teil-/Totalausfall rot hervorheben - wer nicht erreicht wurde, muss
      // anders (Telefon) benachrichtigt werden.
      alarmResultEl.classList.toggle('result-fail', data.ok === false);
    } catch (err) {
      const kind = err instanceof ApiError ? err.kind : 'network';
      let text;
      let unclear = false;
      if (kind === 'auth') {
        text = `${what} NICHT gesendet – Sitzung abgelaufen, bitte neu anmelden.`;
      } else if (kind === 'csrf') {
        text = `${what} NICHT gesendet – Sicherheits-Token veraltet, Seite neu laden.`;
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
      alarmResultEl.textContent = text;
      alarmResultEl.classList.add(unclear ? 'result-warn' : 'result-fail');
      // Der Verlauf zeigt nach ein paar Sekunden, ob der Versand gestartet
      // bzw. abgeschlossen wurde (nas_alarm_started / nas_alarm).
      if (unclear) [5000, 40000].forEach((ms) => setTimeout(refreshLog, ms));
    } finally {
      alarmInFlight = false;
      setBusy(false);
      refresh();
      refreshLog();
    }
  }

  // ENTWARNUNG (Fehlalarm): Critical Alert mit kurzem, normalem Ton an alle -
  // direkt vom NAS. Muss durchdringen, damit niemand umsonst losfährt.
  const sendFalseAlarm = (expectDemo) => sendNasAlarm('false_alarm', expectDemo);

  // REAL ALARM: bewusst direkt vom NAS (auch bei toter ESP32-Kette).
  const sendRealAlarm = (expectDemo) => sendNasAlarm('alarm', expectDemo);

  async function cancelCommand() {
    setBusy(true);
    try {
      const data = await post({ action: 'cancel' });
      showCommandMsg(data.message || 'Fehler.');
    } catch (err) {
      showCommandMsg(errorText(err));
    } finally {
      setBusy(false);
      refresh();
      refreshLog();
    }
  }

  // ==== Empfängerliste (Bark-Keys) ==========================================

  const refreshKeys = makePoller(async () => {
    try {
      const data = await api('dashboard_api.php?action=keys_list', {}, TIMEOUT_POLL_MS);
      if (!data.ok) return;
      updateDemoInfo(data.demo);
      nasKeysVersion = data.version ?? nasKeysVersion;
      // Offene "Kurzbefehl-Links"-Aufklapper, Fokus und Auswahl bleiben bei
      // einem Neu-Rendern erhalten: setHtml() merkt sie sich über data-fkey.
      if (!data.keys.length) {
        setHtml(keysListEl, '<em>Noch keine Empfänger – der ESP32 nutzt seine config.h-Startliste.</em>');
      } else {
        setHtml(keysListEl, data.keys.map((entry) => {
          // Stumm-Lautstärke (0 = lautlos): gilt, sobald der Eintrag stumm ist.
          const vol = Number(entry.mute_volume) || 0;
          const volText = vol === 0 ? '0 (lautlos)' : `${vol} von 10`;
          // "bis ~HH:MM": Zeitpunkt der automatischen Laut-Rückschaltung
          // (Sicherheitsnetz) - so überrascht sie später niemanden.
          const until = Number(entry.mute_until) || 0;
          // Dauerhaft stumm: Das Sicherheitsnetz (automatische Rückschaltung)
          // ist für diesen Eintrag abgeschaltet - deshalb ein eigener, klar
          // unterscheidbarer Badge OHNE Endzeit. Nur ein Mensch beendet das.
          const permanent = !!entry.mute_permanent;
          let badge = '';
          if (entry.muted && permanent) {
            badge = `<span class="badge-muted badge-permanent" title="DAUERHAFT stumm: Alarme kommen mit Stumm-Lautstärke ${volText} an (sichtbar, aber ohne vollen Ton). Stumm seit ${escapeHtml(formatTime(entry.muted_at))}. Die automatische Rückschaltung ist hier abgeschaltet – das bleibt so, bis es jemand von Hand beendet.">Dauerhaft stumm</span>`;
          } else if (entry.muted) {
            badge = `<span class="badge-muted" title="Arbeitsmodus: Alarme kommen mit Stumm-Lautstärke ${volText} an. Stumm seit ${escapeHtml(formatTime(entry.muted_at))}${until ? `; spätestens um ${escapeHtml(fmtClock(until))} Uhr schaltet die Box automatisch wieder laut` : ''}.">Stumm${until ? ` bis ~${escapeHtml(fmtClock(until))}` : ''}</span>`;
          }
          let actions = '';
          if (!readOnly) {
            // id kommt als Zahl vom Server - trotzdem escaped (Grundregel).
            // data-fkey macht die Bedienelemente für setHtml() wiedererkennbar.
            const id = escapeHtml(String(entry.id));
            const volOptions = Array.from({ length: 11 }, (_, v) =>
              `<option value="${v}"${v === vol ? ' selected' : ''}>${v === 0 ? '0 (lautlos)' : v}</option>`).join('');
            // Persönliche Kurzbefehl-Links (Geheim-Token, nur der Admin sieht
            // sie): einmal kopieren, in der Kurzbefehle-App hinterlegen.
            // Bewusst EINGEKLAPPT unter den Aktionen - sie werden nur einmal
            // pro Person gebraucht und sollen die Liste danach nicht länger
            // verbreitern (weniger Knöpfe pro Zeile = ruhigeres Bild).
            const linkRow = entry.mute_token
              ? `<details class="keys-more" data-keys-more="${id}" data-fkey="more-${id}">
                   <summary data-fkey="more-sum-${id}">Kurzbefehl-Links</summary>
                   <div class="keys-links">
                     <button type="button" class="chip-link" title="Stumm-Link kopieren" data-fkey="link-on-${id}" data-key-link="${escapeHtml(entry.mute_token)}" data-key-link-state="on" data-key-label="${escapeHtml(entry.label)}">Stumm</button>
                     <button type="button" class="chip-link" title="Laut-Link kopieren" data-fkey="link-off-${id}" data-key-link="${escapeHtml(entry.mute_token)}" data-key-link-state="off" data-key-label="${escapeHtml(entry.label)}">Laut</button>
                   </div>
                 </details>`
              : '';
            actions = `<div class="keys-actions">
               <label class="keys-volume">Stumm-Lautstärke
                 <select data-fkey="vol-${id}" data-key-volume="${id}" data-key-label="${escapeHtml(entry.label)}">${volOptions}</select>
               </label>
               <button type="button" data-fkey="mute-${id}" data-key-mute="${id}" data-key-muted="${entry.muted ? '1' : '0'}" data-key-label="${escapeHtml(entry.label)}">${entry.muted ? 'Laut schalten' : 'Stumm schalten'}</button>
               <button type="button" data-fkey="perm-${id}" data-key-permanent="${id}" data-key-is-permanent="${permanent ? '1' : '0'}" data-key-label="${escapeHtml(entry.label)}" title="${permanent ? 'Dauer-Stummschaltung beenden: Der Empfänger bleibt zunächst stumm, wird aber wieder automatisch laut geschaltet.' : 'Dauerhaft stumm schalten: Die automatische Rückschaltung wird für diesen Empfänger abgeschaltet. Alarme kommen weiterhin sichtbar an, aber ohne vollen Ton.'}">${permanent ? 'Dauer beenden' : 'Dauerhaft stumm'}</button>
               <button type="button" class="btn-delete" data-fkey="del-${id}" data-key-delete="${id}" data-key-label="${escapeHtml(entry.label)}">Löschen</button>
             </div>${linkRow}`;
          }
          return `<div class="keys-row${entry.muted ? ' muted' : ''}">
             <div class="keys-head">
               <strong>${escapeHtml(entry.label)}</strong>
               <code>${escapeHtml(entry.key_masked)}</code>
               ${badge}
             </div>
             ${actions}
           </div>`;
        }).join('') +
          `<p class="hint">Listen-Version auf dem NAS: ${escapeHtml(data.version)} – der ESP32 zeigt seine übernommene Version oben in der Status-Zeile „Empfängerliste (Box)“.<br>
           „Kurzbefehl-Links“ kopieren den persönlichen Stumm-/Laut-Link der Person (für iPhone-Kurzbefehle, z.&nbsp;B. automatisch beim Betreten/Verlassen der Arbeit).</p>`);
      }
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
      if (keysListEl.__lastHtml === undefined) {
        setHtml(keysListEl, '<em>Empfängerliste konnte nicht geladen werden – neuer Versuch läuft automatisch.</em>');
      }
    }
  });

  // ==== Verlauf (letzte Ereignisse aus dem NAS-Protokoll) ===================
  // Anders als "Letzter Alarm" im Status überlebt dieser Verlauf einen
  // ESP32-Neustart, weil er auf dem NAS liegt (commands.log).

  function describeLogEntry(entry) {
    const who = entry.by ? ` – von ${entry.by}` : '';
    const cmd = `Befehl #${entry.id} ${entry.type || ''}`.trim();
    switch (entry.event) {
      case 'created':
        return { text: `${cmd} angelegt${who}`, cls: '' };
      case 'delivered':
        return { text: `${cmd} an ESP32 ausgeliefert`, cls: '' };
      case 'acked':
        return { text: `${cmd} bestätigt${entry.result ? `: ${fmtAckResult(entry.result)}` : ''}${entry.message ? ` (${fmtAckMessage(entry.message)})` : ''}`, cls: '' };
      case 'expired':
        return { text: `${cmd} abgelaufen (ESP32 hat nicht gepollt)`, cls: 'log-warn' };
      case 'cancelled':
        // Mit Grund (entry.message) hat die Sicherung im NAS den Befehl
        // verworfen, z.B. weil der Demo-Modus inzwischen gewechselt hat.
        return {
          text: `${cmd} abgebrochen${who}${entry.message ? ` – ${entry.message}` : ''}`,
          cls: entry.message ? 'log-warn' : ''
        };
      case 'late_ack':
        return { text: `Verspätete Bestätigung zu Befehl #${entry.id}${entry.result ? `: ${fmtAckResult(entry.result)}` : ''}`, cls: 'log-warn' };
      case 'demo_enabled':
        return { text: `Demo-Modus EIN – Alarme nur an "${entry.label}"${who}`, cls: 'log-warn' };
      case 'demo_disabled':
        return { text: `Demo-Modus AUS – wieder LIVE${who}`, cls: '' };
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
      // *_started wird VOR dem Versand geschrieben: Steht danach kein
      // Abschluss-Eintrag (nas_alarm/false_alarm) im Verlauf, ist der Versand
      // abgebrochen - wichtig nach einer "Antwort unklar"-Meldung.
      case 'nas_alarm_started':
        return { text: `REAL ALARM gestartet${who}`, cls: 'log-alarm' };
      case 'false_alarm_started':
        return { text: `ENTWARNUNG gestartet${who}`, cls: 'log-warn' };
      case 'nas_alarm':
        return { text: `REAL ALARM direkt vom NAS: ${entry.message || ''}${who}`, cls: 'log-alarm' };
      case 'false_alarm':
        return { text: `ENTWARNUNG (Fehlalarm) gesendet: ${entry.message || ''}${who}`, cls: 'log-warn' };
      case 'relay_alarm':
        // source 'remote' = per Dashboard-Befehl ausgelöster DEMO-Alarm (kein Melder).
        return entry.source === 'remote'
          ? { text: `DEMO-Alarm über den ESP32 ausgelöst (Dashboard-Befehl): ${entry.info || ''}`, cls: 'log-warn' }
          : { text: `RELAISALARM über den ESP32 ausgelöst: ${entry.info || ''}`, cls: 'log-alarm' };
      case 'box_offline':
        return { text: `ESP32 OFFLINE gemeldet (kein Status seit ${entry.minutes} min)`, cls: 'log-warn' };
      case 'box_recovered':
        return { text: `ESP32 wieder online (Ausfall ca. ${entry.minutes} min)`, cls: '' };
      case 'nas_web_failed':
        return { text: `NAS-Dashboard gestört: ${entry.detail || 'Selbsttest fehlgeschlagen'}`, cls: 'log-alarm' };
      case 'nas_web_recovered':
        return { text: `NAS-Dashboard wieder in Ordnung (Störung ca. ${entry.minutes} min)`, cls: '' };
      default:
        return { text: String(entry.event || '?'), cls: '' };
    }
  }

  // Standardmäßig die letzten 25 Ereignisse; der "Ältere anzeigen"-Knopf
  // erhöht in 25er-Schritten bis zum Server-Deckel von 100.
  const LOG_STEP = 25;
  const LOG_MAX = 100;
  let logLimit = LOG_STEP;
  const logMoreBtn = document.querySelector('[data-log-more]');

  const refreshLog = makePoller(async () => {
    if (!logEl) return;
    try {
      const data = await api(`dashboard_api.php?action=log_view&limit=${logLimit}`, {}, TIMEOUT_POLL_MS);
      if (!data.ok) return;
      const entries = data.entries || [];
      // Knopf nur zeigen, wenn es voraussichtlich noch mehr gibt (die Seite
      // kam "voll" zurück) und der Deckel noch nicht erreicht ist.
      if (logMoreBtn) logMoreBtn.hidden = logLimit >= LOG_MAX || entries.length < logLimit;
      if (!entries.length) {
        setHtml(logEl, '<li class="log-plain"><em>Noch keine Ereignisse aufgezeichnet.</em></li>');
        return;
      }
      const serverTime = Number(data.server_time) || Math.floor(Date.now() / 1000);
      setHtml(logEl, entries.map((entry) => {
        const { text, cls } = describeLogEntry(entry);
        return `<li class="log-row${cls ? ' ' + cls : ''}">
          <span class="log-dot" aria-hidden="true"></span>
          <span class="log-time" title="${escapeHtml(formatTime(entry.at))}">${escapeHtml(fmtRelTime(entry.at, serverTime))}</span>
          <span class="log-text">${escapeHtml(text)}</span>
        </li>`;
      }).join(''));
    } catch (err) {
      if (err instanceof ApiError && err.kind === 'auth') return;
      // Wie bei der Empfängerliste: letzten bekannten Stand stehen lassen.
      if (logEl.__lastHtml === undefined) {
        setHtml(logEl, '<li class="log-plain"><em>Verlauf konnte nicht geladen werden – neuer Versuch läuft automatisch.</em></li>');
      }
    }
  });

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
    // Persönlichen Kurzbefehl-Link (Geheim-Token) in die Zwischenablage
    // kopieren - der Link schaltet den Eintrag stumm (state=on) bzw. laut.
    const linkBtn = event.target.closest('[data-key-link]');
    if (linkBtn) {
      const url = new URL('api/mute.php', muteLinkBase());
      url.searchParams.set('t', linkBtn.dataset.keyLink);
      url.searchParams.set('state', linkBtn.dataset.keyLinkState);
      const what = linkBtn.dataset.keyLinkState === 'on' ? 'Stumm' : 'Laut';
      try {
        await navigator.clipboard.writeText(url.toString());
        keysMsgEl.textContent = `„${what}“-Link für ${linkBtn.dataset.keyLabel} kopiert – im iPhone-Kurzbefehl unter „URL öffnen“ / „Inhalte von URL abrufen“ einfügen.`;
        // Erfolg auch direkt am Chip zeigen - die Meldung unten übersieht
        // man leicht. Der Grundtext ist fest ("Stumm"/"Laut"), deshalb kann
        // die Rücksetzung ihn gefahrlos wiederherstellen.
        linkBtn.classList.add('copied');
        linkBtn.textContent = 'kopiert';
        setTimeout(() => {
          linkBtn.classList.remove('copied');
          linkBtn.textContent = what;
        }, 1600);
      } catch (err) {
        // Zwischenablage kann blockiert sein (alter Browser, fehlende
        // Berechtigung) - dann den Link zum manuellen Kopieren anzeigen.
        keysMsgEl.textContent = `Kopieren nicht möglich – Link („${what}“, ${linkBtn.dataset.keyLabel}): ${url.toString()}`;
      }
      return;
    }
    // Arbeitsmodus-Umschalter (Stumm/Laut) - Inline-Bestätigung nur beim
    // Stummschalten (lauter machen ist immer die "sichere" Richtung).
    const muteBtn = event.target.closest('[data-key-mute]');
    if (muteBtn) {
      const toMuted = muteBtn.dataset.keyMuted !== '1';
      const doMute = async () => {
        setBusy(true);
        try {
          const data = await post({ action: 'keys_mute', id: muteBtn.dataset.keyMute, muted: toMuted ? '1' : '0' });
          keysMsgEl.textContent = data.message || 'Fehler.';
        } catch (err) {
          keysMsgEl.textContent = errorText(err);
        } finally {
          setBusy(false);
          refreshKeys();
          refreshLog();
        }
      };
      if (toMuted) {
        armInline(muteBtn,
          `"${muteBtn.dataset.keyLabel}" stumm schalten (Arbeitsmodus)? Alarme kommen dort dann OHNE Ton an (aber weiterhin sichtbar).`,
          'Ja, stumm schalten', doMute);
      } else {
        doMute();
      }
      return;
    }

    // Dauerhaft stumm: schaltet für EINEN Empfänger das Sicherheitsnetz ab
    // (keine automatische Rückschaltung mehr). Einschalten ist die riskante
    // Richtung -> Inline-Bestätigung; Beenden geht direkt.
    const permBtn = event.target.closest('[data-key-permanent]');
    if (permBtn) {
      const toPermanent = permBtn.dataset.keyIsPermanent !== '1';
      const doPermanent = async () => {
        setBusy(true);
        try {
          const data = await post({
            action: 'keys_set_mute_permanent',
            id: permBtn.dataset.keyPermanent,
            permanent: toPermanent ? '1' : '0',
          });
          keysMsgEl.textContent = data.message || 'Fehler.';
        } catch (err) {
          keysMsgEl.textContent = errorText(err);
        } finally {
          setBusy(false);
          refreshKeys();
          refreshLog();
        }
      };
      if (toPermanent) {
        armInline(permBtn,
          `"${permBtn.dataset.keyLabel}" DAUERHAFT stumm schalten? Alarme kommen dort weiterhin sichtbar an, aber ohne vollen Ton – und die automatische Rückschaltung greift nicht mehr, bis du das hier wieder beendest.`,
          'Ja, dauerhaft stumm', doPermanent);
      } else {
        doPermanent();
      }
      return;
    }

    const btn = event.target.closest('[data-key-delete]');
    if (!btn) return;
    armInline(btn,
      `Empfänger "${btn.dataset.keyLabel}" wirklich löschen? Er bekommt dann KEINE Alarme mehr!`,
      'Ja, löschen',
      async () => {
        setBusy(true);
        try {
          const data = await post({ action: 'keys_delete', id: btn.dataset.keyDelete });
          keysMsgEl.textContent = data.message || 'Fehler.';
        } catch (err) {
          keysMsgEl.textContent = errorText(err);
        } finally {
          setBusy(false);
          refreshKeys();
          refreshLog();
        }
      });
  });

  // Stumm-Lautstärke pro Empfänger (0 = lautlos): wirkt, sobald der Eintrag
  // stumm geschaltet ist - für beide Alarmwege.
  keysListEl?.addEventListener('change', async (event) => {
    const sel = event.target.closest('[data-key-volume]');
    if (!sel) return;
    setBusy(true);
    try {
      const data = await post({ action: 'keys_set_mute_volume', id: sel.dataset.keyVolume, volume: sel.value });
      keysMsgEl.textContent = data.message || 'Fehler.';
    } catch (err) {
      keysMsgEl.textContent = errorText(err);
    } finally {
      setBusy(false);
      refreshKeys();
      refreshLog();
    }
  });

  keysAddForm?.addEventListener('submit', async (event) => {
    event.preventDefault();
    const label = keysAddForm.elements.label.value.trim();
    const key = keysAddForm.elements.key.value.trim();
    setBusy(true);
    try {
      const data = await post({ action: 'keys_add', label, key });
      keysMsgEl.textContent = data.message || 'Fehler.';
      if (data.ok) keysAddForm.reset();
    } catch (err) {
      keysMsgEl.textContent = errorText(err);
    } finally {
      setBusy(false);
      refreshKeys();
      refreshLog();
    }
  });

  testBtn?.addEventListener('click', enqueueTest);
  cancelBtn?.addEventListener('click', cancelCommand);
  demoToggleBtn?.addEventListener('click', toggleDemo);
  logMoreBtn?.addEventListener('click', () => {
    logLimit = Math.min(LOG_MAX, logLimit + LOG_STEP);
    refreshLog();
  });

  // ==== Zweistufige Inline-Bestätigung für Aktionen mit Folgen ==============
  // Ein natives confirm() ist mit einem einzigen (auch versehentlichen)
  // Enter-Druck bestätigt und blockiert obendrein das 5-s-Polling. Stattdessen:
  // Der 1. Klick "schärft" nur - anstelle des Knopfs erscheint die Frage mit
  // separatem Bestätigungsknopf, die sich nach ARM_TIMEOUT_S von selbst wieder
  // entschärft. Gilt für ALLE bestätigungspflichtigen Aktionen (Alarm/Entwarnung,
  // Demo-Wechsel, Stummschalten, Löschen), damit sich die Seite überall gleich
  // anfühlt. Der Bestätigungsknopf bekommt bewusst KEINEN Auto-Fokus: Wer Enter
  // auf dem Auslöse-Knopf drückt, kann nicht blind doppelt bestätigen - der
  // Fokus landet stattdessen auf "Abbrechen" (die sichere Richtung).

  const ARM_TIMEOUT_S = 10;
  let armedBtn = null;      // höchstens EIN geschärfter Knopf gleichzeitig
  let disarmFn = null;
  // Nur bei Alarm-Knöpfen gesetzt: der Modus (expect_demo), den die offene
  // Bestätigungsfrage beschreibt. updateDemoInfo() entschärft, sobald der
  // tatsächliche Modus davon abweicht.
  let armedExpect = null;

  function disarm() {
    if (disarmFn) disarmFn();
  }

  // Schärft einen konkreten Knopf: versteckt ihn und zeigt an seiner Stelle
  // Frage + Bestätigen/Abbrechen. Funktioniert auch für Knöpfe, die per
  // Ereignis-Delegation behandelt werden (Empfängerliste): Wird die Liste
  // währenddessen neu gerendert, verschwindet die Frage einfach mit - es kann
  // nichts unbeabsichtigt ausgelöst werden.
  // Der Countdown rechnet gegen eine feste Frist (Date.now()) statt Ticks zu
  // zählen: iOS pausiert Timer im Hintergrund - eine "10-s-Bestätigung" darf
  // nach der Rückkehr nicht noch minutenlang scharf sein.
  function armInline(btn, questionText, confirmLabel, onConfirm, expectDemo = null) {
    disarm();
    const box = document.createElement('div');
    box.className = 'arm-confirm';
    const question = document.createElement('span');
    question.className = 'arm-question';
    question.textContent = questionText;
    const yes = document.createElement('button');
    yes.type = 'button';
    yes.className = 'danger';
    yes.textContent = confirmLabel;
    const cancel = document.createElement('button');
    cancel.type = 'button';
    cancel.textContent = 'Abbrechen';
    const countdown = document.createElement('span');
    countdown.className = 'arm-count';
    const deadline = Date.now() + ARM_TIMEOUT_S * 1000;
    const secondsLeft = () => Math.ceil((deadline - Date.now()) / 1000);
    countdown.textContent = `${ARM_TIMEOUT_S} s`;
    box.append(question, yes, cancel, countdown);
    btn.hidden = true;
    btn.after(box);
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
      btn.hidden = false;
      if (hadFocus) btn.focus();
      armedBtn = null;
      armedExpect = null;
      disarmFn = null;
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
    // Auslöse-Knopf ist jetzt versteckt), und ein blindes Enter bricht ab.
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
      `DEMO-MODUS: Alarm über den ESP32 auslösen? Er durchläuft die komplette Alarm-Kette der Box und geht an "${info?.label || 'Test-Empfänger'}". Danach greift der normale 5-Minuten-Cooldown der Box.`,
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

  let refreshTimer = null;
  let keysTimer = null;
  let logTimer = null;
  let tickTimer = null;

  function startPolling() {
    if (authLost || refreshTimer !== null) return;
    refresh();
    refreshKeys();
    refreshLog();
    // .tick: Der Timer überspringt einen Takt, solange die vorige Abfrage
    // noch läuft (siehe makePoller).
    refreshTimer = setInterval(refresh.tick, 5000);
    keysTimer = setInterval(refreshKeys.tick, 30000);
    logTimer = setInterval(refreshLog.tick, 30000);
    tickTimer = setInterval(tick, 1000);
  }

  function stopPolling() {
    clearInterval(refreshTimer);
    clearInterval(keysTimer);
    clearInterval(logTimer);
    clearInterval(tickTimer);
    refreshTimer = keysTimer = logTimer = tickTimer = null;
  }

  // Tab im Hintergrund: Polling aus UND jede offene Bestätigung entschärfen -
  // wer zurückkommt, soll nicht eine (evtl. veraltete) Alarm-Frage mit einem
  // Tipp bestätigen können.
  document.addEventListener('visibilitychange', () => {
    if (document.hidden) {
      disarm();
      stopPolling();
    } else {
      startPolling();
    }
  });

  startPolling();
})();
