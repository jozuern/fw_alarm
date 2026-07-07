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
      fmt: (v) => v === 'connected' ? 'verbunden' : (v === 'down' ? 'GETRENNT' : v) },
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
      fmt: (v) => v === 'open' ? 'offen (Ruhe)' : (v === 'closed' ? 'GESCHLOSSEN (Alarm!)' : v) },
    { key: 'isr_latched', label: 'Impuls gemerkt',
      tip: 'Der Interrupt hat einen kurzen Alarm-Impuls zwischengespeichert, den die Hauptschleife gleich verarbeitet. Normalerweise "Nein" – "Ja" ist nur ein kurzer Übergangszustand.',
      fmt: fmtBool },
    { key: 'alarm_pending', label: 'Alarm in Zustellung',
      tip: 'Es läuft gerade ein Alarm, bei dem noch nicht alle Empfänger erreicht wurden. Der ESP32 versucht es automatisch weiter (Nachsende-Puffer).',
      fmt: fmtBool },
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
      fmt: fmtBool },
    { key: 'cooldown', label: 'Cooldown aktiv',
      tip: 'Nach einem Alarm wartet die Box eine Mindestzeit (5 Min), bevor sie erneut alarmieren kann – verhindert Alarm-Spam bei flatterndem Kontakt.',
      fmt: fmtBool },
    { key: 'waiting_for_release', label: 'Wartet auf Kontakt-Öffnung',
      tip: 'Nach einem Alarm wird erst wieder scharf geschaltet, wenn der Kontakt eine Weile stabil offen war. "Ja" heißt: Kontakt war noch nicht lange genug offen.',
      fmt: fmtBool },
    { key: 'stuck_warned', label: 'Klemm-Warnung gesendet',
      tip: 'Der Kontakt war so lange durchgehend geschlossen, dass die Box einmalig "Kontakt klemmt?" an den Status-Empfänger gemeldet hat.',
      fmt: fmtBool },
    { key: 'ack_pending', label: 'Befehls-Quittung offen',
      tip: 'Der ESP32 hat einen NAS-Befehl ausgeführt, konnte die Bestätigung (ACK) aber noch nicht zum NAS zurückmelden. Normalerweise "Nein".',
      fmt: fmtBool }
  ];

  function renderFields(status) {
    fieldsEl.innerHTML = FIELDS.map(({ key, label, tip, fmt }) => {
      const raw = status[key];
      let value = raw === undefined || raw === null || raw === '' ? 'n/a' : raw;
      if (fmt && value !== 'n/a') value = fmt(value);
      // Beim Versionsfeld direkt zeigen, ob der ESP32 den NAS-Stand schon hat.
      if (key === 'keys_version' && value !== 'n/a' && nasKeysVersion !== null) {
        value = String(raw) === String(nasKeysVersion)
          ? `${raw} (= NAS ✓)`
          : `${raw} (NAS: ${nasKeysVersion} – Übernahme steht aus)`;
      }
      return `<div class="field">
        <span>${escapeHtml(label)}<span class="help" tabindex="0" data-tip="${escapeHtml(tip)}">?</span></span>
        <strong>${escapeHtml(value)}</strong>
      </div>`;
    }).join('');
  }

  function renderCommand(command) {
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
      demoBannerEl.innerHTML = `<strong>🧪 DEMO-MODUS AKTIV</strong> – Alarme gehen NUR an
        <strong>${escapeHtml(demoInfo.label || 'Test-Empfänger')}</strong>
        (<code>${escapeHtml(demoInfo.key_masked || '')}</code>), seit ${escapeHtml(formatTime(demoInfo.changed_at))}.<br>${syncText}`;
    } else {
      demoBannerEl.hidden = true;
    }

    if (demoStateEl && demoInfo) {
      if (demoInfo.enabled) {
        demoStateEl.innerHTML = `Aktuell: <strong class="mode-demo">DEMO-MODUS</strong> – Test-Empfänger:
          <strong>${escapeHtml(demoInfo.label || '?')}</strong>`;
      } else {
        demoStateEl.innerHTML = 'Aktuell: <strong class="mode-live">LIVE</strong> – Alarme gehen an alle Empfänger.';
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
    }
  }

  async function refresh() {
    try {
      const res = await fetch('dashboard_api.php?action=status', { credentials: 'same-origin' });
      const data = await res.json();
      if (!data.ok) return;
      const status = data.status || {};
      demoInfo = data.demo || demoInfo;
      nasKeysVersion = data.nas_keys_version ?? nasKeysVersion;
      stateEl.textContent = data.offline ? 'OFFLINE' : 'ONLINE';
      stateEl.className = data.offline ? 'badge badge-offline' : 'badge badge-online';
      updatedEl.textContent = `Letzter Status: ${formatTime(status.seen_at)} (${data.seen_age_seconds ?? 'n/a'} s)`;
      renderFields(status);
      renderCommand(data.command);
      renderDemo(status, !!data.offline);
    } catch (err) {
      stateEl.textContent = 'OFFLINE';
      stateEl.className = 'badge badge-offline';
      updatedEl.textContent = 'Dashboard konnte den Serverstatus nicht laden.';
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
    return res.json();
  }

  async function enqueueTest() {
    setBusy(true);
    try {
      const data = await post({ action: 'enqueue', type: 'TEST' });
      alert(data.message || (data.ok ? 'Befehl angelegt.' : 'Fehler.'));
    } catch (err) {
      alert('Anfrage fehlgeschlagen.');
    } finally {
      setBusy(false);
      refresh();
    }
  }

  async function cancelCommand() {
    setBusy(true);
    try {
      const data = await post({ action: 'cancel' });
      alert(data.message || 'Fehler.');
    } catch (err) {
      alert('Anfrage fehlgeschlagen.');
    } finally {
      setBusy(false);
      refresh();
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
    }
  }

  // ==== Empfängerliste (Bark-Keys) ==========================================

  async function refreshKeys() {
    try {
      const res = await fetch('dashboard_api.php?action=keys_list', { credentials: 'same-origin' });
      const data = await res.json();
      if (!data.ok) return;
      demoInfo = data.demo || demoInfo;
      nasKeysVersion = data.version ?? nasKeysVersion;
      if (!data.keys.length) {
        keysListEl.innerHTML = '<em>Noch keine Empfänger – der ESP32 nutzt seine config.h-Startliste.</em>';
      } else {
        keysListEl.innerHTML = data.keys.map((entry) =>
          `<div class="keys-row">
             <strong>${escapeHtml(entry.label)}</strong>
             <code>${escapeHtml(entry.key_masked)}</code>
             ${readOnly ? '' : `<button type="button" data-key-delete="${entry.id}" data-key-label="${escapeHtml(entry.label)}">Löschen</button>`}
           </div>`).join('') +
          `<p class="hint">Listen-Version auf dem NAS: ${escapeHtml(data.version)} – der ESP32 zeigt seine übernommene Version oben im Statusfeld.</p>`;
      }
      // Dropdown für die Demo-Empfänger-Auswahl aus derselben Liste füllen.
      if (demoTargetEl) {
        const selected = demoTargetEl.value;
        demoTargetEl.innerHTML = data.keys.map((entry) =>
          `<option value="${entry.id}">${escapeHtml(entry.label)} (${escapeHtml(entry.key_masked)})</option>`).join('');
        if (selected && [...demoTargetEl.options].some((o) => o.value === selected)) {
          demoTargetEl.value = selected;
        }
      }
    } catch (err) {
      keysListEl.textContent = 'Empfängerliste konnte nicht geladen werden.';
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

  refresh();
  refreshKeys();
  setInterval(refresh, 5000);
  setInterval(refreshKeys, 30000);
})();
