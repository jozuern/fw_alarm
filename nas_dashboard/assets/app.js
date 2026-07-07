(function () {
  const csrf = document.querySelector('meta[name="csrf"]')?.content || '';
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

  const STATE_LABELS = {
    pending: 'wartet auf ESP32',
    delivered: 'an ESP32 ausgeliefert, ACK offen',
    acked: 'bestätigt',
    expired: 'abgelaufen (ESP32 hat nicht gepollt)',
    cancelled: 'abgebrochen'
  };

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
    [testBtn, alarmBtn, cancelBtn].forEach((btn) => {
      if (btn) btn.disabled = busy;
    });
  }

  function renderFields(status) {
    const labels = [
      ['device_id', 'Box'],
      ['fw_build', 'Firmware'],
      ['wifi', 'WLAN'],
      ['rssi', 'RSSI'],
      ['ip', 'IP'],
      ['uptime_s', 'Uptime (s)'],
      ['relay', 'Kontakt'],
      ['isr_latched', 'ISR-Latch'],
      ['alarm_pending', 'Alarm offen'],
      ['key_progress', 'Empfänger'],
      ['keys_count', 'Empfänger gesamt'],
      ['keys_version', 'Empfängerliste (Version)'],
      ['last_alarm', 'Letzter Alarm'],
      ['last_heartbeat', 'Letzter Heartbeat'],
      ['heartbeat_ok', 'Heartbeat OK'],
      ['cooldown', 'Cooldown'],
      ['waiting_for_release', 'Wiederbewaffnung'],
      ['stuck_warned', 'Klemm-Warnung']
    ];
    fieldsEl.innerHTML = labels.map(([key, label]) => {
      const value = status[key] ?? 'n/a';
      return `<div class="field"><span>${escapeHtml(label)}</span><strong>${escapeHtml(value)}</strong></div>`;
    }).join('');
  }

  function renderCommand(command) {
    if (!command) {
      commandEl.textContent = 'Kein Befehl aktiv.';
      cancelBtn.hidden = true;
      return;
    }
    const label = STATE_LABELS[command.state] || command.state;
    let text = `Befehl #${command.id} ${command.type}: ${label}`;
    if (command.state === 'acked' && command.ack_result) {
      text += ` – ${command.ack_result}`;
      if (command.ack_message) text += ` (${command.ack_message})`;
    }
    commandEl.textContent = text;
    cancelBtn.hidden = command.state !== 'pending';
  }

  async function refresh() {
    try {
      const res = await fetch('dashboard_api.php?action=status', { credentials: 'same-origin' });
      const data = await res.json();
      if (!data.ok) return;
      const status = data.status || {};
      stateEl.textContent = data.offline ? 'OFFLINE' : 'ONLINE';
      stateEl.className = data.offline ? 'badge badge-offline' : 'badge badge-online';
      updatedEl.textContent = `Letzter Status: ${formatTime(status.seen_at)} (${data.seen_age_seconds ?? 'n/a'} s)`;
      renderFields(status);
      renderCommand(data.command);
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
      if (!data.keys.length) {
        keysListEl.innerHTML = '<em>Noch keine Empfänger – der ESP32 nutzt seine config.h-Startliste.</em>';
        return;
      }
      keysListEl.innerHTML = data.keys.map((entry) =>
        `<div class="keys-row">
           <strong>${escapeHtml(entry.label)}</strong>
           <code>${escapeHtml(entry.key_masked)}</code>
           <button type="button" data-key-delete="${entry.id}" data-key-label="${escapeHtml(entry.label)}">Löschen</button>
         </div>`).join('') +
        `<p class="hint">Listen-Version auf dem NAS: ${escapeHtml(data.version)} – der ESP32 zeigt seine übernommene Version oben im Statusfeld.</p>`;
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
  alarmBtn?.addEventListener('click', () => {
    if (confirm('ECHTEN FEUERWEHR-ALARM direkt vom NAS an alle Empfänger senden?')) {
      sendRealAlarm();
    }
  });

  refresh();
  refreshKeys();
  setInterval(refresh, 5000);
  setInterval(refreshKeys, 30000);
})();
