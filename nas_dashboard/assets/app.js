(function () {
  const csrf = document.querySelector('meta[name="csrf"]')?.content || '';
  // Nur-Lese-Benutzer: Knöpfe fehlen dann im HTML; hier zusätzlich nichts
  // rendern, was Änderungen auslösen würde (der Server blockt ohnehin).
  const readOnly = (document.querySelector('meta[name="role"]')?.content || 'admin') !== 'admin';
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
    return new Date(epoch * 1000).toLocaleString('de-DE');
  }

  // Nur die Uhrzeit (HH:MM) - für "Stumm bis ~18:30". Das Datum wäre hier
  // Rauschen, die Stummschaltung endet spätestens nach wenigen Stunden.
  function fmtClock(epoch) {
    return new Date(epoch * 1000).toLocaleTimeString('de-DE', { hour: '2-digit', minute: '2-digit' });
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

  // Merker für renderDemo(): Das 5-s-Polling darf den Demo-Knopf nicht
  // wieder aktivieren, während noch eine Aktion läuft (Doppelklick-Schutz).
  let actionBusy = false;

  function setBusy(busy) {
    actionBusy = busy;
    // keysAddBtn ist mit dabei, damit ein Doppelklick auf "Hinzufügen" nicht
    // zwei Anfragen abschickt (die zweite endet sonst in einer verwirrenden
    // "bereits eingetragen"-Meldung).
    [testBtn, alarmBtn, espAlarmBtn, falseAlarmBtn, cancelBtn, demoToggleBtn, keysAddBtn].forEach((btn) => {
      if (btn) btn.disabled = busy;
    });
  }

  // Flag-Werte kommen vom ESP32 als '0'/'1'-Strings.
  const flagged = (v) => v === '1' || v === 1;

  // innerHTML nur ersetzen, wenn sich wirklich etwas geändert hat: Sonst
  // klappt alle 5 s ein gerade geöffneter Tooltip zu, Fokus und Textauswahl
  // gehen verloren, und die Seite flackert unnötig.
  function setHtml(el, html) {
    if (!el || el.__lastHtml === html) return;
    el.__lastHtml = html;
    el.innerHTML = html;
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
      tip: 'Zeitpunkt und Text des letzten ausgelösten Alarms (aus Sicht des ESP32, seit dem letzten Neustart). "nie" heißt: kein Alarm seit dem letzten Einschalten.',
      value: (s) => show(s.last_alarm) },
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
      tip: 'Name der Box (device_id aus config.h) und der Kompilier-Zeitpunkt der laufenden Firmware – so sieht man, ob wirklich der neueste Stand läuft.',
      value: (s) => (na(s.device_id) && na(s.fw_build)) ? 'n/a'
        : `${show(s.device_id)} · ${show(s.fw_build)}` },
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
      tip: 'Freier Arbeitsspeicher (Heap) des ESP32. Normal sind grob 150–250 kB. Sinkt der Wert über Tage stetig, deutet das auf ein Speicherleck hin; unter ~50 kB wird es kritisch.',
      value: (s) => na(s.free_heap) ? 'n/a' : fmtHeap(s.free_heap),
      cls: (s) => { const b = parseInt(s.free_heap, 10); return !isNaN(b) && b < 50000 ? 'warn' : ''; } },
    // NAS-seitig, nicht Box-seitig: Der Wächter (DSM-Aufgabe) muss auch dann
    // laufen, wenn die Box offline ist - deshalb liveWhenOffline.
    { group: 'Box & Wartung', label: 'Offline-Wächter (NAS)', liveWhenOffline: true,
      tip: 'Die DSM-Aufgabe auf dem NAS prüft alle 15 Minuten, ob die Box sich noch meldet, und schickt sonst eine Bark-Warnung. Hier steht, ob diese Aufgabe selbst noch läuft – der Wächter darf nicht unbemerkt ausfallen.',
      value: (s, ctx) => ctx.watchdog ? ctx.watchdog.text : 'n/a',
      cls: (s, ctx) => ctx.watchdog && ctx.watchdog.warn ? 'warn' : '' }
  ];

  function renderFields(status, offline, seenAge, watchdog) {
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
      const liveCls = offline && row.liveWhenOffline ? ' tele-current' : '';
      // Der Erklärtext ist ein ECHTES Element (nicht CSS-::after): Nur so
      // liest ein Screenreader ihn über aria-describedby auch wirklich vor.
      const tipId = `tip-${groups.length}-${current.rows.length}`;
      current.rows.push(`<div class="tele-row${cls ? ' tele-' + cls : ''}${liveCls}">
        <span class="tele-label">${escapeHtml(label)} <span class="help" tabindex="0" role="button" aria-expanded="false" aria-label="Erklärung zu „${escapeHtml(label)}“" aria-describedby="${tipId}"><span aria-hidden="true">?</span><span class="tip-bubble" id="${tipId}" role="tooltip">${escapeHtml(tip)}</span></span></span>
        <span class="tele-value">${escapeHtml(value)}</span>
      </div>`);
    });
    // Stale-Hinweis: Wer nur dieses Panel scannt, soll veraltete Werte nicht
    // für live halten (der Hero sagt "Box offline", aber der steht weiter oben).
    fieldsEl.classList.toggle('is-stale', !!offline);
    const staleNote = offline
      ? `<p class="stale-note">Box offline – die gedimmten Werte sind der letzte bekannte Stand${status.seen_at ? ' von ' + escapeHtml(formatTime(status.seen_at)) : ' (die Box hat sich noch nie gemeldet)'}.</p>`
      : '';
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

  function renderDemo(status, offline) {
    if (!demoBannerEl) return;
    const espVersion = status.keys_version;
    const synced = !offline && espVersion !== undefined && nasKeysVersion !== null
      && String(espVersion) === String(nasKeysVersion);

    if (demoInfo && demoInfo.enabled) {
      let syncText;
      if (offline) {
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
      demoToggleBtn.disabled = actionBusy;
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

  async function performDemoSet(fields) {
    setBusy(true);
    try {
      const data = await post(fields);
      if (demoMsgEl) demoMsgEl.textContent = data.message || 'Fehler.';
      if (data.ok && data.demo) demoInfo = data.demo;
    } catch (err) {
      if (demoMsgEl) demoMsgEl.textContent = 'Anfrage fehlgeschlagen.';
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

  // "Läuft noch"-Guards: Antwortet das NAS mal langsamer als das
  // Poll-Intervall, sollen sich die Anfragen nicht stapeln.
  let refreshBusy = false;
  let keysBusy = false;
  let logBusy = false;

  async function refresh() {
    if (refreshBusy) return;
    refreshBusy = true;
    try {
      const res = await fetch('dashboard_api.php?action=status', { credentials: 'same-origin' });
      if (res.status === 401) { handleAuthLoss(); return; }
      const data = await res.json();
      if (!data.ok) return;
      const status = data.status || {};
      demoInfo = data.demo || demoInfo;
      nasKeysVersion = data.nas_keys_version ?? nasKeysVersion;
      // Ampel-Logik der Statuslampe: ein laufender Alarm (Zustellung noch
      // offen) übersteuert das grüne "Einsatzbereit" - der wichtigste
      // Zustand gewinnt. Offline schlägt alles.
      const alarmActive = !data.offline && flagged(status.alarm_pending);
      if (data.offline) setState('Box offline', 'crit');
      else if (alarmActive) setState('Alarm aktiv', 'alarm');
      else setState('Einsatzbereit', 'ok');
      // Status auch im Tab-Titel: so sieht man ohne Tab-Wechsel, ob alles läuft.
      document.title = (alarmActive ? '\u{1F6A8} ' : data.offline ? '\u{1F534} ' : '\u{1F7E2} ') + PAGE_TITLE;
      // seen_age_seconds ist null, wenn sich die Box noch NIE gemeldet hat
      // (Number(null) wäre 0 und würde "Offline seit 0 Min" anzeigen).
      const seenAge = data.seen_age_seconds == null ? NaN : Number(data.seen_age_seconds);
      const ageText = fmtAgeShort(seenAge);
      updatedEl.textContent = `Letzter Status: ${formatTime(status.seen_at)}${ageText ? ` (${ageText})` : ''}`;
      const wdState = watchdogState(data.watchdog, Number(data.server_time));
      renderFields(status, !!data.offline, seenAge, wdState);
      renderCommand(data.command, Number(data.server_time));
      renderWatchdog(wdState);
      renderDemo(status, !!data.offline);
    } catch (err) {
      // Hier ist das NAS selbst nicht erreichbar (oder die Antwort kaputt) -
      // das ist NICHT dasselbe wie "ESP32 offline", deshalb eigener Zustand.
      setState('NAS nicht erreichbar', 'stale');
      document.title = '⚠️ ' + PAGE_TITLE;
      updatedEl.textContent = 'Keine Verbindung zum NAS – angezeigte Werte können veraltet sein.';
    } finally {
      refreshBusy = false;
    }
  }

  async function post(fields) {
    const form = new FormData();
    Object.entries(fields).forEach(([key, value]) => form.set(key, value));
    const res = await fetch('dashboard_api.php', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'X-CSRF-Token': csrf },
      body: form
    });
    if (res.status === 401) {
      handleAuthLoss();
      throw new Error('Sitzung abgelaufen');
    }
    return res.json();
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
      showCommandMsg('Anfrage fehlgeschlagen.');
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
  async function enqueueEspAlarm() {
    setBusy(true);
    try {
      const data = await post({ action: 'enqueue', type: 'ALARM' });
      showCommandMsg(data.message || (data.ok ? 'Befehl angelegt.' : 'Fehler.'));
    } catch (err) {
      showCommandMsg('Anfrage fehlgeschlagen.');
    } finally {
      setBusy(false);
      refresh();
      refreshLog();
    }
  }

  // ENTWARNUNG (Fehlalarm): normale Push-Mitteilung an alle - direkt vom NAS.
  async function sendFalseAlarm() {
    setBusy(true);
    alarmResultEl.hidden = false;
    alarmResultEl.textContent = 'Entwarnung wird gesendet…';
    try {
      const data = await post({ action: 'false_alarm' });
      // Namen statt maskierter Keys: Unter Stress zählt, WER fehlt.
      const lines = (data.results || []).map((r) => `${r.label || r.key} ${r.ok ? 'OK' : 'FEHLER'}`);
      alarmResultEl.textContent = `${data.message || 'Fehler.'}` +
        (lines.length ? ` [${lines.join(', ')}]` : '');
    } catch (err) {
      alarmResultEl.textContent = 'Entwarnungs-Anfrage fehlgeschlagen – Verbindung zum NAS prüfen!';
    } finally {
      setBusy(false);
      refresh();
      refreshLog();
    }
  }

  async function cancelCommand() {
    setBusy(true);
    try {
      const data = await post({ action: 'cancel' });
      showCommandMsg(data.message || 'Fehler.');
    } catch (err) {
      showCommandMsg('Anfrage fehlgeschlagen.');
    } finally {
      setBusy(false);
      refresh();
      refreshLog();
    }
  }

  // REAL ALARM geht direkt vom NAS an Bark (dauert je Empfänger ein paar
  // Sekunden, deshalb Buttons sperren und Ergebnis pro Empfänger anzeigen).
  async function sendRealAlarm() {
    setBusy(true);
    alarmResultEl.hidden = false;
    alarmResultEl.textContent = 'Alarm wird gesendet…';
    try {
      const data = await post({ action: 'alarm' });
      // Namen statt maskierter Keys: Unter Stress zählt, WER fehlt.
      const lines = (data.results || []).map((r) =>
        `${r.label || r.key} ${r.ok ? 'OK' : 'FEHLER (HTTP ' + r.http + ')'}`);
      alarmResultEl.textContent = `${data.message || 'Fehler.'}` +
        (lines.length ? ` [${lines.join(', ')}]` : '');
    } catch (err) {
      alarmResultEl.textContent = 'Alarm-Anfrage fehlgeschlagen – Verbindung zum NAS prüfen!';
    } finally {
      setBusy(false);
      refresh();
      refreshLog();
    }
  }

  // ==== Empfängerliste (Bark-Keys) ==========================================

  async function refreshKeys() {
    if (keysBusy) return;
    keysBusy = true;
    try {
      const res = await fetch('dashboard_api.php?action=keys_list', { credentials: 'same-origin' });
      if (res.status === 401) { handleAuthLoss(); return; }
      const data = await res.json();
      if (!data.ok) return;
      demoInfo = data.demo || demoInfo;
      nasKeysVersion = data.version ?? nasKeysVersion;
      // Offene "Kurzbefehl-Links"-Aufklapper vor einem Neu-Rendern merken:
      // setHtml() ersetzt bei echten Änderungen den ganzen Baum, und ein
      // gerade geöffneter Aufklapper soll dabei nicht zuschnappen.
      const openMore = new Set(Array.from(
        keysListEl.querySelectorAll('details[data-keys-more][open]'),
        (d) => d.dataset.keysMore));
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
          const badge = entry.muted
            ? `<span class="badge-muted" title="Arbeitsmodus: Alarme kommen mit Stumm-Lautstärke ${volText} an. Stumm seit ${escapeHtml(formatTime(entry.muted_at))}${until ? `; spätestens um ${escapeHtml(fmtClock(until))} Uhr schaltet die Box automatisch wieder laut` : ''}.">Stumm${until ? ` bis ~${escapeHtml(fmtClock(until))}` : ''}</span>`
            : '';
          let actions = '';
          if (!readOnly) {
            const volOptions = Array.from({ length: 11 }, (_, v) =>
              `<option value="${v}"${v === vol ? ' selected' : ''}>${v === 0 ? '0 (lautlos)' : v}</option>`).join('');
            // Persönliche Kurzbefehl-Links (Geheim-Token, nur der Admin sieht
            // sie): einmal kopieren, in der Kurzbefehle-App hinterlegen.
            // Bewusst EINGEKLAPPT unter den Aktionen - sie werden nur einmal
            // pro Person gebraucht und sollen die Liste danach nicht länger
            // verbreitern (weniger Knöpfe pro Zeile = ruhigeres Bild).
            const linkRow = entry.mute_token
              ? `<details class="keys-more" data-keys-more="${escapeHtml(String(entry.id))}">
                   <summary>Kurzbefehl-Links</summary>
                   <div class="keys-links">
                     <button type="button" class="chip-link" title="Stumm-Link kopieren" data-key-link="${escapeHtml(entry.mute_token)}" data-key-link-state="on" data-key-label="${escapeHtml(entry.label)}">Stumm</button>
                     <button type="button" class="chip-link" title="Laut-Link kopieren" data-key-link="${escapeHtml(entry.mute_token)}" data-key-link-state="off" data-key-label="${escapeHtml(entry.label)}">Laut</button>
                   </div>
                 </details>`
              : '';
            actions = `<div class="keys-actions">
               <label class="keys-volume">Stumm-Lautstärke
                 <select data-key-volume="${entry.id}" data-key-label="${escapeHtml(entry.label)}">${volOptions}</select>
               </label>
               <button type="button" data-key-mute="${entry.id}" data-key-muted="${entry.muted ? '1' : '0'}" data-key-label="${escapeHtml(entry.label)}">${entry.muted ? 'Laut schalten' : 'Stumm schalten'}</button>
               <button type="button" class="btn-delete" data-key-delete="${entry.id}" data-key-label="${escapeHtml(entry.label)}">Löschen</button>
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
        openMore.forEach((id) => {
          const details = keysListEl.querySelector(`details[data-keys-more="${CSS.escape(id)}"]`);
          if (details) details.open = true;
        });
      }
      // Dropdown für die Demo-Empfänger-Auswahl aus derselben Liste füllen.
      if (demoTargetEl) {
        const selected = demoTargetEl.value;
        const optionsHtml = data.keys.map((entry) =>
          `<option value="${entry.id}">${escapeHtml(entry.label)} (${escapeHtml(entry.key_masked)})</option>`).join('');
        setHtml(demoTargetEl, optionsHtml);
        if (selected && [...demoTargetEl.options].some((o) => o.value === selected)) {
          demoTargetEl.value = selected;
        }
      }
    } catch (err) {
      // Netz-Schluckauf: Die zuletzt bekannte Liste stehen lassen, statt sie
      // durch die Fehlerzeile zu ersetzen (das sah nach Datenverlust aus).
      // Nur wenn noch nie etwas geladen wurde, erscheint der Fehler direkt.
      if (keysListEl.__lastHtml === undefined) {
        setHtml(keysListEl, '<em>Empfängerliste konnte nicht geladen werden – neuer Versuch läuft automatisch.</em>');
      }
    } finally {
      keysBusy = false;
    }
  }

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
        return { text: `${cmd} abgebrochen${who}`, cls: '' };
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
      case 'nas_alarm':
        return { text: `REAL ALARM direkt vom NAS: ${entry.message || ''}${who}`, cls: 'log-alarm' };
      case 'false_alarm':
        return { text: `ENTWARNUNG (Fehlalarm) gesendet: ${entry.message || ''}${who}`, cls: 'log-warn' };
      case 'relay_alarm':
        return { text: `RELAISALARM über den ESP32 ausgelöst: ${entry.info || ''}`, cls: 'log-alarm' };
      case 'box_offline':
        return { text: `ESP32 OFFLINE gemeldet (kein Status seit ${entry.minutes} min)`, cls: 'log-warn' };
      case 'box_recovered':
        return { text: `ESP32 wieder online (Ausfall ca. ${entry.minutes} min)`, cls: '' };
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

  async function refreshLog() {
    if (!logEl || logBusy) return;
    logBusy = true;
    try {
      const res = await fetch(`dashboard_api.php?action=log_view&limit=${logLimit}`, { credentials: 'same-origin' });
      if (res.status === 401) { handleAuthLoss(); return; }
      const data = await res.json();
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
      // Wie bei der Empfängerliste: letzten bekannten Stand stehen lassen.
      if (logEl.__lastHtml === undefined) {
        setHtml(logEl, '<li class="log-plain"><em>Verlauf konnte nicht geladen werden – neuer Versuch läuft automatisch.</em></li>');
      }
    } finally {
      logBusy = false;
    }
  }

  keysListEl?.addEventListener('click', async (event) => {
    // Persönlichen Kurzbefehl-Link (Geheim-Token) in die Zwischenablage
    // kopieren - der Link schaltet den Eintrag stumm (state=on) bzw. laut.
    const linkBtn = event.target.closest('[data-key-link]');
    if (linkBtn) {
      const url = new URL('api/mute.php', location.href);
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
          keysMsgEl.textContent = 'Anfrage fehlgeschlagen.';
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
          keysMsgEl.textContent = 'Anfrage fehlgeschlagen.';
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
      keysMsgEl.textContent = 'Anfrage fehlgeschlagen.';
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
      keysMsgEl.textContent = 'Anfrage fehlgeschlagen.';
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

  function disarm() {
    if (disarmFn) disarmFn();
  }

  // Schärft einen konkreten Knopf: versteckt ihn und zeigt an seiner Stelle
  // Frage + Bestätigen/Abbrechen. Funktioniert auch für Knöpfe, die per
  // Ereignis-Delegation behandelt werden (Empfängerliste): Wird die Liste
  // währenddessen neu gerendert, verschwindet die Frage einfach mit - es kann
  // nichts unbeabsichtigt ausgelöst werden.
  function armInline(btn, questionText, confirmLabel, onConfirm) {
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
    let secondsLeft = ARM_TIMEOUT_S;
    countdown.textContent = `${secondsLeft} s`;
    box.append(question, yes, cancel, countdown);
    btn.hidden = true;
    btn.after(box);
    const timer = setInterval(() => {
      secondsLeft -= 1;
      if (secondsLeft <= 0) {
        disarm();
      } else {
        countdown.textContent = `${secondsLeft} s`;
      }
    }, 1000);
    armedBtn = btn;
    disarmFn = () => {
      clearInterval(timer);
      // Stand der Fokus in der Bestätigung (Tastatur-Bedienung), geht er
      // zurück auf den Auslöse-Knopf statt ins Leere auf <body>.
      const hadFocus = box.contains(document.activeElement);
      box.remove();
      btn.hidden = false;
      if (hadFocus) btn.focus();
      armedBtn = null;
      disarmFn = null;
    };
    cancel.addEventListener('click', disarm);
    yes.addEventListener('click', () => {
      disarm();
      onConfirm();
    });
    // Fokus auf "Abbrechen": Tastatur-Nutzer verlieren den Fokus nicht (der
    // Auslöse-Knopf ist jetzt versteckt), und ein blindes Enter bricht ab.
    cancel.focus();
  }

  function armButton(btn, getQuestion, confirmLabel, onConfirm) {
    if (!btn) return;   // Lese-Rolle: Knopf existiert im HTML nicht
    btn.addEventListener('click', () => {
      armInline(btn, getQuestion(), confirmLabel, onConfirm);
    });
  }

  armButton(alarmBtn, () => demoInfo && demoInfo.enabled
      ? `DEMO-MODUS: Alarm geht nur an "${demoInfo.label || 'Test-Empfänger'}". Jetzt senden?`
      : 'ECHTEN FEUERWEHR-ALARM direkt vom NAS an alle Empfänger senden?',
    'Ja, Alarm auslösen', sendRealAlarm);

  armButton(espAlarmBtn, () =>
      `DEMO-MODUS: Alarm über den ESP32 auslösen? Er durchläuft die komplette Alarm-Kette der Box und geht an "${demoInfo?.label || 'Test-Empfänger'}". Danach greift der normale 5-Minuten-Cooldown der Box.`,
    'Ja, Demo-Alarm auslösen', enqueueEspAlarm);

  armButton(falseAlarmBtn, () => demoInfo && demoInfo.enabled
      ? `DEMO-MODUS: Entwarnung geht nur an "${demoInfo.label || 'Test-Empfänger'}". Jetzt senden?`
      : 'ENTWARNUNG an ALLE senden? („Der letzte Alarm war ein FEHLALARM“ – normale Mitteilung, kein Alarmton)',
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

  function startPolling() {
    if (authLost || refreshTimer !== null) return;
    refresh();
    refreshKeys();
    refreshLog();
    refreshTimer = setInterval(refresh, 5000);
    keysTimer = setInterval(refreshKeys, 30000);
    logTimer = setInterval(refreshLog, 30000);
  }

  function stopPolling() {
    clearInterval(refreshTimer);
    clearInterval(keysTimer);
    clearInterval(logTimer);
    refreshTimer = keysTimer = logTimer = null;
  }

  document.addEventListener('visibilitychange', () => {
    if (document.hidden) stopPolling(); else startPolling();
  });

  startPolling();
})();
