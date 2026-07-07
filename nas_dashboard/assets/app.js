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
  const cancelBtn = document.querySelector('[data-command-cancel]');
  const alarmResultEl = document.querySelector('[data-alarm-result]');
  const keysListEl = document.querySelector('[data-keys-list]');
  const keysAddForm = document.querySelector('[data-keys-add]');
  const keysMsgEl = document.querySelector('[data-keys-msg]');
  const demoBannerEl = document.querySelector('[data-demo-banner]');
  const demoStateEl = document.querySelector('[data-demo-state]');
  const demoTargetEl = document.querySelector('[data-demo-target]');
  const demoToggleBtn = document.querySelector('[data-demo-toggle]');
  const demoMsgEl = document.querySelector('[data-demo-msg]');
  const commandMsgEl = document.querySelector('[data-command-msg]');
  const logEl = document.querySelector('[data-log]');

  const STATE_LABELS = {
    pending: 'wartet auf ESP32',
    delivered: 'an ESP32 ausgeliefert, ACK offen',
    acked: 'bestätigt',
    expired: 'abgelaufen (ESP32 hat nicht gepollt)',
    cancelled: 'abgebrochen'
  };

  // Zuletzt bekannter Demo-/Versionsstand (wird von refresh()/refreshKeys()
  // aktualisiert und u.a. für den REAL-ALARM-Bestätigungstext gebraucht).
  let demoInfo = null;
  let nasKeysVersion = null;

  const PAGE_TITLE = document.title;

  // Sitzung serverseitig abgelaufen (HTTP 401): deutlich machen statt still
  // mit veralteten Daten weiterzulaufen, dann zur Login-Seite neu laden.
  let authLost = false;
  function handleAuthLoss() {
    if (authLost) return;
    authLost = true;
    stopPolling();
    document.title = '\u{1F512} ' + PAGE_TITLE;
    stateEl.textContent = 'ABGEMELDET';
    stateEl.className = 'badge badge-stale';
    updatedEl.textContent = 'Sitzung abgelaufen – die Anmeldeseite wird geladen…';
    setTimeout(() => location.reload(), 1500);
  }

  function formatTime(epoch) {
    if (!epoch) return 'nie';
    return new Date(epoch * 1000).toLocaleString('de-DE');
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

  function setBusy(busy) {
    [testBtn, alarmBtn, cancelBtn, demoToggleBtn].forEach((btn) => {
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

  function fmtBool(value) {
    if (value === '1' || value === 1) return 'Ja';
    if (value === '0' || value === 0) return 'Nein';
    return value;
  }

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

  const FIELDS = [
    { key: 'device_id', label: 'Box',
      tip: 'Name des ESP32 (device_id aus config.h). Nur zur Wiedererkennung, falls es mal mehrere Boxen gibt.' },
    { key: 'fw_build', label: 'Firmware',
      tip: 'Datum und Uhrzeit, an dem die laufende Firmware kompiliert wurde. So sieht man, ob die Box wirklich den neuesten Stand hat.' },
    { key: 'wifi', label: 'WLAN',
      tip: 'Ist der ESP32 gerade mit dem WLAN verbunden? Ohne WLAN kann er weder Bark-Alarme senden noch das NAS erreichen.',
      fmt: (v) => v === 'connected' ? 'verbunden' : (v === 'down' ? 'GETRENNT' : v),
      warn: (v) => v === 'down' ? 'crit' : '' },
    { key: 'rssi', label: 'WLAN-Signal',
      tip: 'Empfangsstärke in dBm. Näher an 0 ist besser: ab ca. -67 gut, unter ca. -80 wird es unzuverlässig – dann Box oder Router umstellen.',
      fmt: fmtRssi },
    { key: 'ip', label: 'IP-Adresse',
      tip: 'Aktuelle Adresse des ESP32 im WLAN des Magazins.' },
    { key: 'uptime_s', label: 'Läuft seit',
      tip: 'Zeit seit dem letzten Neustart des ESP32. Ein unerwarteter Sprung auf wenige Minuten bedeutet: Die Box hat neu gestartet (z.B. Stromausfall oder Watchdog).',
      fmt: fmtUptime },
    { key: 'relay', label: 'Melder-Kontakt',
      tip: 'Zustand des Relaiskontakts am Swissphone-Lader: "offen" = Ruhe, "GESCHLOSSEN" = Alarmkontakt liegt an. Dauerhaft geschlossen deutet auf einen klemmenden Kontakt hin.',
      fmt: (v) => v === 'open' ? 'offen (Ruhe)' : (v === 'closed' ? 'GESCHLOSSEN (Alarm!)' : v),
      warn: (v) => v === 'closed' ? 'crit' : '' },
    { key: 'isr_latched', label: 'Impuls gemerkt',
      tip: 'Der Interrupt hat einen kurzen Alarm-Impuls zwischengespeichert, den die Hauptschleife gleich verarbeitet. Normalerweise "Nein" – "Ja" ist nur ein kurzer Übergangszustand.',
      fmt: fmtBool, warn: (v) => flagged(v) ? 'warn' : '' },
    { key: 'alarm_pending', label: 'Alarm in Zustellung',
      tip: 'Es läuft gerade ein Alarm, bei dem noch nicht alle Empfänger erreicht wurden. Der ESP32 versucht es automatisch weiter (Nachsende-Puffer).',
      fmt: fmtBool, warn: (v) => flagged(v) ? 'crit' : '' },
    { key: 'key_progress', label: 'Zustellung',
      tip: 'Beim letzten/laufenden Alarm: wie viele Empfänger schon erfolgreich benachrichtigt wurden (erreicht/gesamt).' },
    { key: 'keys_count', label: 'Empfänger auf ESP32',
      tip: 'Anzahl der Alarm-Empfänger, die der ESP32 aktuell gespeichert hat. Im Demo-Modus ist das genau 1 (nur der Test-Empfänger).' },
    { key: 'keys_version', label: 'Empfängerliste (Version)',
      tip: 'Version der Empfängerliste, die der ESP32 übernommen hat. Stimmt sie mit der NAS-Version überein, ist die Liste (auch ein Demo-Wechsel) angekommen.' },
    { key: 'last_alarm', label: 'Letzter Alarm',
      tip: 'Zeitpunkt und Ergebnis des letzten ausgelösten Alarms (aus Sicht des ESP32, seit dem letzten Neustart).' },
    { key: 'last_heartbeat', label: 'Letzter Heartbeat',
      tip: 'Der ESP32 meldet sich 1x täglich leise beim Status-Empfänger ("Ich lebe noch"). Hier steht, wann das zuletzt passiert ist und ob es geklappt hat.' },
    { key: 'heartbeat_ok', label: 'Heartbeat OK',
      tip: 'War der letzte tägliche Lebenszeichen-Versand an Bark erfolgreich?',
      fmt: fmtBool, warn: (v) => flagged(v) ? '' : 'warn' },
    { key: 'cooldown', label: 'Cooldown aktiv',
      tip: 'Nach einem Alarm wartet die Box eine Mindestzeit (5 Min), bevor sie erneut alarmieren kann – verhindert Alarm-Spam bei flatterndem Kontakt.',
      fmt: fmtBool, warn: (v) => flagged(v) ? 'warn' : '' },
    { key: 'waiting_for_release', label: 'Wartet auf Kontakt-Öffnung',
      tip: 'Nach einem Alarm wird erst wieder scharf geschaltet, wenn der Kontakt eine Weile stabil offen war. "Ja" heißt: Kontakt war noch nicht lange genug offen.',
      fmt: fmtBool, warn: (v) => flagged(v) ? 'warn' : '' },
    { key: 'stuck_warned', label: 'Klemm-Warnung gesendet',
      tip: 'Der Kontakt war so lange durchgehend geschlossen, dass die Box einmalig "Kontakt klemmt?" an den Status-Empfänger gemeldet hat.',
      fmt: fmtBool, warn: (v) => flagged(v) ? 'warn' : '' },
    { key: 'ack_pending', label: 'Befehls-Quittung offen',
      tip: 'Der ESP32 hat einen NAS-Befehl ausgeführt, konnte die Bestätigung (ACK) aber noch nicht zum NAS zurückmelden. Normalerweise "Nein".',
      fmt: fmtBool, warn: (v) => flagged(v) ? 'warn' : '' }
  ];

  function renderFields(status, offline, seenAge) {
    setHtml(fieldsEl, FIELDS.map(({ key, label, tip, fmt, warn }) => {
      const raw = status[key];
      let value = raw === undefined || raw === null || raw === '' ? 'n/a' : raw;
      if (fmt && value !== 'n/a') value = fmt(value);
      // Warnfarbe des Feldes (gelb/rot) aus dem Rohwert ableiten.
      let cls = '';
      if (warn && raw !== undefined && raw !== null && raw !== '') {
        cls = warn(String(raw)) || '';
      }
      // Offline ist die gemeldete Laufzeit nur ein alter Stand – stattdessen
      // zeigen, wie lange sich die Box schon nicht mehr gemeldet hat.
      if (key === 'uptime_s' && offline) {
        label = 'Offline seit';
        tip = 'Zeit seit dem letzten Lebenszeichen des ESP32 beim NAS. Mögliche Ursachen: Stromausfall, WLAN weg oder NAS nicht erreichbar.';
        value = Number.isFinite(seenAge) ? fmtUptime(seenAge) : 'unbekannt (noch nie gemeldet)';
        cls = 'crit';
      }
      // Beim Versionsfeld direkt zeigen, ob der ESP32 den NAS-Stand schon hat.
      if (key === 'keys_version' && value !== 'n/a' && nasKeysVersion !== null) {
        if (String(raw) === String(nasKeysVersion)) {
          value = `${raw} (= NAS ✓)`;
        } else {
          value = `${raw} (NAS: ${nasKeysVersion} – Übernahme steht aus)`;
          cls = 'warn';
        }
      }
      return `<div class="field${cls ? ' field-' + cls : ''}">
        <span>${escapeHtml(label)} <span class="help" tabindex="0" data-tip="${escapeHtml(tip)}">(?)</span></span>
        <strong>${escapeHtml(value)}</strong>
      </div>`;
    }).join(''));
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
      commandEl.textContent = 'Kein Befehl aktiv.';
      if (cancelBtn) cancelBtn.hidden = true;
      return;
    }
    const label = STATE_LABELS[command.state] || command.state;
    let text = `Befehl #${command.id} ${command.type}: ${label}`;
    if (command.state === 'acked' && command.ack_result) {
      text += ` – ${command.ack_result}`;
      if (command.ack_message) text += ` (${command.ack_message})`;
    }
    commandEl.textContent = text;
    if (cancelBtn) cancelBtn.hidden = command.state !== 'pending';
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
        syncText = '⚠️ ESP32 ist OFFLINE – der Relaisalarm würde noch mit der ALTEN Liste senden!';
      } else if (synced) {
        syncText = '✔ Der ESP32 hat die Demo-Liste übernommen – du kannst jetzt gefahrlos testen.';
      } else {
        syncText = '⚠️ Der ESP32 hat die Demo-Liste NOCH NICHT übernommen (wartet auf den nächsten Poll) – ein Relaisalarm würde noch an ALLE gehen!';
      }
      demoBannerEl.hidden = false;
      setHtml(demoBannerEl, `<strong>🧪 DEMO-MODUS AKTIV</strong> – Alarme gehen NUR an
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
      demoToggleBtn.disabled = false;
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
  }

  async function toggleDemo() {
    if (!demoInfo) return;
    let fields;
    if (demoInfo.enabled) {
      if (!confirm('Demo-Modus beenden? Danach gehen Alarme wieder an ALLE Empfänger (LIVE).')) return;
      fields = { action: 'demo_set', enabled: '0' };
    } else {
      const id = demoTargetEl?.value || '';
      if (!id) {
        if (demoMsgEl) demoMsgEl.textContent = 'Bitte zuerst einen Empfänger in der Liste anlegen und als Test-Empfänger auswählen.';
        return;
      }
      const label = demoTargetEl.selectedOptions[0]?.textContent || '?';
      if (!confirm(`Demo-Modus einschalten? Alarme gehen dann NUR an "${label}". Nach dem Testen unbedingt wieder auf LIVE zurückschalten!`)) return;
      fields = { action: 'demo_set', enabled: '1', id };
    }
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
      stateEl.textContent = data.offline ? 'OFFLINE' : 'ONLINE';
      stateEl.className = data.offline ? 'badge badge-offline' : 'badge badge-online';
      // Status auch im Tab-Titel: so sieht man ohne Tab-Wechsel, ob alles läuft.
      document.title = (data.offline ? '\u{1F534} ' : '\u{1F7E2} ') + PAGE_TITLE;
      updatedEl.textContent = `Letzter Status: ${formatTime(status.seen_at)} (${data.seen_age_seconds ?? 'n/a'} s)`;
      // seen_age_seconds ist null, wenn sich die Box noch NIE gemeldet hat
      // (Number(null) wäre 0 und würde "Offline seit 0 Min" anzeigen).
      renderFields(status, !!data.offline,
        data.seen_age_seconds == null ? NaN : Number(data.seen_age_seconds));
      renderCommand(data.command, Number(data.server_time));
      renderDemo(status, !!data.offline);
    } catch (err) {
      // Hier ist das NAS selbst nicht erreichbar (oder die Antwort kaputt) -
      // das ist NICHT dasselbe wie "ESP32 offline", deshalb eigener Zustand.
      stateEl.textContent = 'NAS NICHT ERREICHBAR';
      stateEl.className = 'badge badge-stale';
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
      const lines = (data.results || []).map((r) =>
        `${r.key} ${r.ok ? 'OK' : 'FEHLER (HTTP ' + r.http + ')'}`);
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
      if (!data.keys.length) {
        setHtml(keysListEl, '<em>Noch keine Empfänger – der ESP32 nutzt seine config.h-Startliste.</em>');
      } else {
        setHtml(keysListEl, data.keys.map((entry) =>
          `<div class="keys-row">
             <strong>${escapeHtml(entry.label)}</strong>
             <code>${escapeHtml(entry.key_masked)}</code>
             ${readOnly ? '' : `<button type="button" data-key-delete="${entry.id}" data-key-label="${escapeHtml(entry.label)}">Löschen</button>`}
           </div>`).join('') +
          `<p class="hint">Listen-Version auf dem NAS: ${escapeHtml(data.version)} – der ESP32 zeigt seine übernommene Version oben im Statusfeld.</p>`);
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
      setHtml(keysListEl, escapeHtml('Empfängerliste konnte nicht geladen werden.'));
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
        return { text: `${cmd} bestätigt${entry.result ? `: ${entry.result}` : ''}${entry.message ? ` (${entry.message})` : ''}`, cls: '' };
      case 'expired':
        return { text: `${cmd} abgelaufen (ESP32 hat nicht gepollt)`, cls: 'log-warn' };
      case 'cancelled':
        return { text: `${cmd} abgebrochen${who}`, cls: '' };
      case 'late_ack':
        return { text: `Verspätete Bestätigung zu Befehl #${entry.id}${entry.result ? `: ${entry.result}` : ''}`, cls: 'log-warn' };
      case 'demo_enabled':
        return { text: `Demo-Modus EIN – Alarme nur an "${entry.label}"${who}`, cls: 'log-warn' };
      case 'demo_disabled':
        return { text: `Demo-Modus AUS – wieder LIVE${who}`, cls: '' };
      case 'key_added':
        return { text: `Empfänger "${entry.label}" (${entry.key}) hinzugefügt${who}`, cls: '' };
      case 'key_removed':
        return { text: `Empfänger "${entry.label}" (${entry.key}) gelöscht${who}`, cls: '' };
      case 'nas_alarm':
        return { text: `REAL ALARM direkt vom NAS: ${entry.message || ''}${who}`, cls: 'log-alarm' };
      default:
        return { text: String(entry.event || '?'), cls: '' };
    }
  }

  async function refreshLog() {
    if (!logEl || logBusy) return;
    logBusy = true;
    try {
      const res = await fetch('dashboard_api.php?action=log_view', { credentials: 'same-origin' });
      if (res.status === 401) { handleAuthLoss(); return; }
      const data = await res.json();
      if (!data.ok) return;
      const entries = data.entries || [];
      if (!entries.length) {
        setHtml(logEl, '<em>Noch keine Ereignisse aufgezeichnet.</em>');
        return;
      }
      setHtml(logEl, entries.map((entry) => {
        const { text, cls } = describeLogEntry(entry);
        return `<div class="log-row${cls ? ' ' + cls : ''}">
          <span class="log-time">${escapeHtml(formatTime(entry.at))}</span>
          <span class="log-text">${escapeHtml(text)}</span>
        </div>`;
      }).join(''));
    } catch (err) {
      setHtml(logEl, escapeHtml('Verlauf konnte nicht geladen werden.'));
    } finally {
      logBusy = false;
    }
  }

  keysListEl?.addEventListener('click', async (event) => {
    const btn = event.target.closest('[data-key-delete]');
    if (!btn) return;
    if (!confirm(`Empfänger "${btn.dataset.keyLabel}" wirklich löschen? Er bekommt dann KEINE Alarme mehr!`)) return;
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
  alarmBtn?.addEventListener('click', () => {
    const question = demoInfo && demoInfo.enabled
      ? `DEMO-MODUS: Alarm geht nur an "${demoInfo.label || 'Test-Empfänger'}". Jetzt senden?`
      : 'ECHTEN FEUERWEHR-ALARM direkt vom NAS an alle Empfänger senden?';
    if (confirm(question)) {
      sendRealAlarm();
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
