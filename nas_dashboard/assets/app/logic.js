(function (root) {
  'use strict';
  const FW = root.FW = root.FW || {};

  // ==== Reine Anzeige-Logik (ohne DOM, ohne fetch, ohne Preact) ==============
  // Hier steht, was das Dashboard aus den Rohwerten der Box ableitet: Formate,
  // Zeilen, Hinweise, Urteil. Portiert aus dem bisherigen app.js - Texte,
  // Schwellen und Regeln unverändert. Läuft deshalb auch in Node (Tests).

  // ---- Zeitzone ------------------------------------------------------------
  // Alle Zeitangaben in der Zeitzone der Wache - egal, wie die Uhr des
  // anzeigenden Geräts eingestellt ist. Der Server meldet sie mit der
  // Status-Antwort; bis dahin bzw. bei älteren Ständen gilt Berlin.
  let TZ = 'Europe/Berlin';

  // Nur übernehmen, wenn Intl sie kennt (ein unbekannter Name ließe sonst
  // jede Zeitformatierung scheitern).
  function setTimezone(name) {
    if (typeof name !== 'string' || !name || name === TZ) return;
    try {
      new Intl.DateTimeFormat('de-DE', { timeZone: name });
      TZ = name;
    } catch (err) {
      // unbekannte Zeitzone -> bisherige behalten
    }
  }
  const tz = () => TZ;

  // ---- Rohwerte ------------------------------------------------------------
  // Flag-Werte kommen von der Box als '0'/'1'-Strings.
  const na = (v) => v === undefined || v === null || v === '';
  const show = (v) => (na(v) ? 'n/a' : String(v));
  const flagged = (v) => v === '1' || v === 1;

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

  function rssiQuality(value) {
    const dbm = parseInt(value, 10);
    if (isNaN(dbm)) return null;
    if (dbm >= -55) return 'sehr gut';
    if (dbm >= -67) return 'gut';
    if (dbm >= -75) return 'mittel';
    if (dbm >= -85) return 'schwach';
    return 'sehr schwach';
  }

  function fmtRssi(value) {
    const dbm = parseInt(value, 10);
    if (isNaN(dbm)) return value;
    return `${dbm} dBm (${rssiQuality(dbm)})`;
  }

  function fmtHeap(value) {
    const bytes = parseInt(value, 10);
    if (isNaN(bytes)) return value;
    return `${Math.round(bytes / 1024)} kB frei`;
  }

  // ---- Zeitformate (immer in der Zeitzone der Wache) -----------------------
  function fmtDateTime(epoch) {
    if (!epoch) return 'nie';
    return new Date(epoch * 1000).toLocaleString('de-DE', { timeZone: TZ });
  }

  // Nur die Uhrzeit (HH:MM).
  function fmtClock(epoch) {
    return new Date(epoch * 1000).toLocaleTimeString('de-DE', { hour: '2-digit', minute: '2-digit', timeZone: TZ });
  }

  // Uhrzeit mit Sekunden aus einem Browser-Zeitstempel (ms).
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
    if (!Number.isFinite(age) || age < 0) return fmtDateTime(epoch);
    if (age < 90) return 'gerade eben';
    if (age < 3600) return `vor ${Math.round(age / 60)} min`;
    if (age < 86400) return `vor ${Math.round(age / 3600)} Std`;
    return fmtDateTime(epoch);
  }

  // Alter des letzten Status: junge Werte sekundengenau, ältere
  // menschenlesbar über fmtUptime ("vor 21 Min" statt "1260 s").
  function fmtAgeShort(age) {
    if (!Number.isFinite(age) || age < 0) return null;
    if (age < 90) return `vor ${Math.round(age)} s`;
    return `vor ${fmtUptime(age)}`;
  }

  // ---- Neustart-Gründe und Schwellen ---------------------------------------
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
  const lowMemory = (s) => lowBytes(s.free_heap, HEAP_WARN) || lowBytes(s.max_alloc_heap, MAX_ALLOC_WARN);
  // heartbeat_ok=0 heißt nur dann "gescheitert", wenn es schon einen Versuch
  // gab: Nach einem Neustart meldet die Box 0 und "nie", bis der erste
  // Heartbeat fällig ist - das ist kein Fehler (gleiche Regel im Wächter-Cron).
  const heartbeatFailed = (s) => !na(s.heartbeat_ok) && !flagged(s.heartbeat_ok)
    && !na(s.last_heartbeat) && s.last_heartbeat !== 'nie';
  // Neustart durch Watchdog/Absturz/Brownout: als Hinweis nur am ersten Tag
  // nach dem Neustart - der Grund bleibt sonst wochenlang stehen und würde
  // als Dauer-Hinweis nur noch übersehen.
  const resetWarnFresh = (s) => RESET_WARN.includes(String(s.reset_reason)) && parseInt(s.uptime_s, 10) < 86400;

  // ---- Alarm-Zustände aus Sicht der Box ------------------------------------
  // key_progress ist "erreicht/gesamt".
  function keyProgressParts(s) {
    const match = /^(\d+)\s*\/\s*(\d+)$/.exec(String(s.key_progress ?? '').trim());
    return match ? [Number(match[1]), Number(match[2])] : null;
  }

  // Zustellung des letzten Alarms der Box unvollständig? Gilt nur, wenn seit
  // dem Neustart ein Alarm war und die Box nicht mehr nachsendet
  // (alarm_pending=0) - dann bleibt der Rest ohne Alarm. Neuere Firmware
  // meldet das zusätzlich ausdrücklich (alarm_gaveup=1) und zählt gegen die
  // Liste zum Zeitpunkt des Alarms; ältere gegen ihre AKTUELLE Liste.
  function deliveryIncomplete(s) {
    if (flagged(s.alarm_pending) || na(s.last_alarm) || s.last_alarm === 'nie') return false;
    if (flagged(s.alarm_gaveup)) return true;
    const parts = keyProgressParts(s);
    return !!parts && parts[0] < parts[1];
  }

  const alarmRunning = (s, offline) => !offline && flagged(s.alarm_pending);

  // Klemmender Kontakt: Die Box ist zwar online, kann aber keinen NEUEN Alarm
  // erkennen. stuck_warned setzt die Box erst, wenn der Kontakt UNGEWÖHNLICH
  // lange zu bleibt (30 min). Ältere Firmware setzt es beim Öffnen NICHT
  // zurück - deshalb zählt es nur, solange relay=closed ist.
  const isStuck = (s, offline) => !offline && flagged(s.stuck_warned) && s.relay === 'closed';

  // Zustellung durch, Kontakt noch geschlossen: Der Melder steht weiter auf
  // Alarm. Das ist nach jedem Alarm normal und KEINE Störung - aber auch
  // nicht "alles ruhig".
  const alarmStanding = (s, offline) => !offline && !alarmRunning(s, offline)
    && !isStuck(s, offline) && s.relay === 'closed';

  // Woher kam der letzte Alarm? (Feld alarm_source - meldet erst neuere
  // Firmware; bei älterer bleibt es leer.)
  const ALARM_SOURCES = {
    relay: 'Melder-Relais',
    remote: 'DEMO-Alarm über die Box (Dashboard-Befehl)'
  };

  // ---- Wächter -------------------------------------------------------------
  // Selbstüberwachung des Offline-Wächters: Der Wächter, der das Schweigen
  // der Box melden soll, darf nicht selbst unbemerkt schweigen.
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
    // Läufe ausgefallen sind (35 min).
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
    // abgeschaltet. Das ist seine Entscheidung, soll aber nicht in
    // Vergessenheit geraten.
    if (info.offline_alert === false) {
      return { warn: true, text: 'läuft, aber die Meldung „Box offline“ ist abgeschaltet – ein Ausfall der Box würde nicht gemeldet' };
    }
    if (info.web_alert === false) {
      return { warn: true, text: 'läuft, aber die Meldung „NAS-Dashboard defekt“ ist abgeschaltet' };
    }
    return { warn: false, text: `läuft (zuletzt ${age < 60 ? 'gerade eben' : 'vor ' + fmtUptime(age)})` };
  }

  // Außen-Wächter (Cloud-VM): Sie klopft alle 5 min über api/alive.php an.
  // Ab wann sie als stumm gilt, meldet der Server (outside_silent_after_s);
  // ältere Stände ohne das Feld: eine Stunde.
  // null = nicht eingerichtet.
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
    // Die VM klopft zwar an, kann aber ihre eigenen Bark-Meldungen nicht
    // zustellen - fiele das NAS aus, käme keine Warnung.
    const barkFailedAt = Number(info.outside_bark_failed_at) || 0;
    if (barkFailedAt > 0) {
      return { warn: true, text: `meldet sich (zuletzt ${last}), kann aber nicht melden (Bark-Versand fehlgeschlagen seit ${fmtDayTime(barkFailedAt, serverTime)}) – BARK_KEY in /etc/fw-alarm-watch.conf auf der VM prüfen` };
    }
    // Hat der Admin "Außen-Wächter meldet sich nicht" abgeschaltet, darf das
    // nicht in Vergessenheit geraten (outside_alert kommt nur für den Admin).
    if (info.outside_alert === false) {
      return { warn: true, text: `meldet sich (zuletzt ${last}), aber die Meldung „Außen-Wächter meldet sich nicht“ ist abgeschaltet – fiele die VM aus, käme keine Warnung` };
    }
    return { warn: false, text: `meldet sich (zuletzt ${last})` };
  }

  // ---- Zeilen der Box-Details ----------------------------------------------
  // Jede Zeile: Gruppe, Kennung, Bezeichnung, Anzeigetext, Farbe, Erklärung.
  // Farbe: '' (normal) / 'warn' / 'crit' / 'unknown' (Wert gerade unbekannt:
  // Box offline oder NAS liefert keine aktuellen Daten - veraltete Werte
  // sehen nie normal aus). Live bleiben die Zeilen, die weiter aktuell sind
  // (Laufzeit/"Offline seit" und die beiden Wächter).
  const ROW_DEFS = [
    { id: 'relay', group: 'Alarmkette', label: 'Melder-Kontakt',
      tip: 'Zustand des Relaiskontakts am Swissphone-Lader: "offen" = Ruhe, "GESCHLOSSEN" = Alarmkontakt liegt an. Klemmt der Kontakt ungewöhnlich lange, meldet die Box einmalig "Kontakt klemmt?" an den Status-Empfänger.',
      value: (s) => {
        if (na(s.relay)) return 'n/a';
        if (s.relay === 'open') return 'offen (Ruhe)';
        if (s.relay !== 'closed') return String(s.relay);
        return flagged(s.stuck_warned) ? 'GESCHLOSSEN – klemmt? (Warnung gesendet)' : 'GESCHLOSSEN (Alarm!)';
      },
      cls: (s) => (s.relay === 'closed' ? 'crit' : '') },
    { id: 'ready', group: 'Alarmkette', label: 'Auslösebereit',
      tip: 'Kann die Box gerade einen NEUEN Alarm auslösen? Nach einem Alarm gilt ein Mindestabstand (Cooldown, 5 Min) und der Kontakt muss erst wieder stabil offen gewesen sein (Wiederbewaffnung) – das verhindert Alarm-Spam bei flatterndem Kontakt. "Impuls wird verarbeitet" heißt: Ein erkannter Alarm-Impuls wartet gerade kurz auf die Hauptschleife.',
      value: (s) => {
        if (na(s.cooldown) && na(s.waiting_for_release)) return 'n/a';
        if (flagged(s.alarm_pending)) return 'Nein – Alarm läuft noch';
        if (flagged(s.isr_latched)) return 'gleich – Impuls wird verarbeitet';
        if (flagged(s.waiting_for_release)) return 'Nein – wartet auf Kontakt-Öffnung';
        if (flagged(s.cooldown)) return 'Nein – Cooldown läuft';
        return 'Ja';
      },
      cls: (s) => ((flagged(s.alarm_pending) || flagged(s.isr_latched)
        || flagged(s.waiting_for_release) || flagged(s.cooldown)) ? 'warn' : '') },
    { id: 'delivery', group: 'Alarmkette', label: 'Zustellung',
      tip: 'Wie viele Empfänger beim letzten/laufenden Alarm erfolgreich benachrichtigt wurden (erreicht/gesamt). Läuft die Zustellung noch, versucht die Box automatisch weiter, bis alle versorgt sind (Nachsende-Puffer). Rot heißt: Die Box sendet nicht mehr nach, aber nicht alle wurden erreicht – die übrigen ggf. per „Alarm an alle senden“ bzw. telefonisch verständigen. Achtung: Die Box zählt gegen ihre AKTUELLE Empfängerliste – wurde die Liste nach dem Alarm geändert (auch ein Demo-Wechsel), kann sich die Gesamtzahl verschoben haben.',
      value: (s) => {
        if (flagged(s.alarm_pending)) return `läuft – bisher ${show(s.key_progress)}`;
        if (na(s.last_alarm) || s.last_alarm === 'nie') return '–';
        if (deliveryIncomplete(s)) return `nur ${show(s.key_progress)} erreicht – NICHT alle`;
        return `${show(s.key_progress)} erreicht`;
      },
      cls: (s) => {
        if (flagged(s.alarm_pending)) return 'crit';
        return deliveryIncomplete(s) ? 'crit' : '';
      } },
    { id: 'lastAlarm', group: 'Alarmkette', label: 'Letzter Alarm',
      tip: 'Zeitpunkt und Text des letzten ausgelösten Alarms aus Sicht der Box (seit ihrem letzten Neustart) und – falls die Firmware es meldet – woher er kam: Melder-Relais oder DEMO-Alarm per Dashboard-Befehl. "nie" heißt: kein Alarm seit dem letzten Einschalten. Den dauerhaften Verlauf führt das NAS („Verlauf“).',
      value: (s) => {
        const text = show(s.last_alarm);
        if (na(s.alarm_source) || na(s.last_alarm) || s.last_alarm === 'nie') return text;
        return `${text} · ${ALARM_SOURCES[s.alarm_source] || String(s.alarm_source)}`;
      } },
    { id: 'wifi', group: 'Verbindung', label: 'WLAN',
      tip: 'Empfangsstärke der Box im WLAN des Magazins und ihre aktuelle Adresse. Näher an 0 dBm ist besser: ab ca. -67 gut, unter ca. -80 unzuverlässig – dann Box oder Router umstellen. Ohne WLAN kann die Box weder Bark-Alarme senden noch das NAS erreichen.',
      value: (s) => {
        if (s.wifi === 'down') return 'GETRENNT';
        if (na(s.rssi)) return 'n/a';
        return `${fmtRssi(s.rssi)} · ${show(s.ip)}`;
      },
      cls: (s) => {
        if (s.wifi === 'down') return 'crit';
        return weakWifi(s) ? 'warn' : '';
      } },
    { id: 'keys', group: 'Verbindung', label: 'Empfängerliste (Box)',
      tip: 'Empfängerliste, wie sie die Box gespeichert hat: Anzahl, davon stumm (Arbeitsmodus) und die Listen-Version. Stimmt die Version mit dem NAS überein, ist die Liste (auch ein Demo-Wechsel) angekommen. Im Demo-Modus steht hier genau 1 Empfänger.',
      value: (s, ctx) => {
        if (na(s.keys_count)) return 'n/a';
        let text = `${s.keys_count} Empfänger`;
        const muted = parseInt(s.keys_muted, 10);
        if (!isNaN(muted) && muted > 0) text += ` (${muted} stumm)`;
        if (!na(s.keys_version)) {
          text += ` · v${s.keys_version}`;
          if (!na(ctx.nasKeysVersion)) {
            text += String(s.keys_version) === String(ctx.nasKeysVersion)
              ? ' (= NAS)'
              : ` (NAS: v${ctx.nasKeysVersion} – Übernahme steht aus)`;
          }
        }
        return text;
      },
      cls: (s, ctx) => ((!na(s.keys_version) && !na(ctx.nasKeysVersion)
        && String(s.keys_version) !== String(ctx.nasKeysVersion)) ? 'warn' : '') },
    { id: 'heartbeat', group: 'Verbindung', label: 'Täglicher Heartbeat',
      tip: 'Die Box meldet sich 1x täglich leise beim Status-Empfänger ("Ich lebe noch"). Hier steht der letzte Versuch; schlug er fehl, probiert die Box es automatisch erneut.',
      value: (s) => {
        if (na(s.last_heartbeat)) return 'n/a';
        return String(s.last_heartbeat) + (heartbeatFailed(s) ? ' – letzter Versuch fehlgeschlagen' : '');
      },
      cls: (s) => (heartbeatFailed(s) ? 'warn' : '') },
    { id: 'ack', group: 'Verbindung', label: 'Befehls-Quittung',
      tip: 'Hat die Box einen Befehl aus dem Dashboard ausgeführt, dessen Bestätigung noch nicht beim NAS angekommen ist? Normalerweise "keine offen" – ein Rückstand wird automatisch nachgereicht.',
      value: (s) => (na(s.ack_pending) ? 'n/a'
        : (flagged(s.ack_pending) ? 'offen – wird nachgereicht' : 'keine offen')),
      cls: (s) => (flagged(s.ack_pending) ? 'warn' : '') },
    { id: 'firmware', group: 'Wartung', label: 'Box / Firmware',
      tip: 'Name der Box (device_id aus config.h) und die Firmware-Kennung FW_BUILD_MARKER aus config.h. Neuere Firmware hängt automatisch den Kompilier-Zeitpunkt an und meldet die Version des Arduino-ESP32-Cores – daran sieht man, ob wirklich der neueste Stand läuft. Bei älterer Firmware ist die Kennung ein frei gewählter Text, der nur etwas aussagt, wenn er bei jeder neuen Firmware angepasst wurde.',
      value: (s) => ((na(s.device_id) && na(s.fw_build)) ? 'n/a'
        : `${show(s.device_id)} · ${show(s.fw_build)}${na(s.core) ? '' : ` · Core ${s.core}`}`) },
    { id: 'uptime', group: 'Wartung', live: true,
      label: (ctx) => (ctx.offline ? 'Offline seit' : 'Läuft seit'),
      tip: (ctx) => (ctx.offline
        ? 'Zeit seit dem letzten Lebenszeichen der Box beim NAS. Mögliche Ursachen: Stromausfall, WLAN weg oder NAS nicht erreichbar.'
        : 'Zeit seit dem letzten Neustart der Box. Ein unerwarteter Sprung auf wenige Minuten bedeutet: Die Box hat neu gestartet (z.B. Stromausfall oder Watchdog).'),
      value: (s, ctx) => (ctx.offline
        ? (Number.isFinite(ctx.seenAge) ? fmtUptime(ctx.seenAge) : 'unbekannt (noch nie gemeldet)')
        : (na(s.uptime_s) ? 'n/a' : fmtUptime(s.uptime_s))),
      cls: (s, ctx) => (ctx.offline ? 'crit' : '') },
    { id: 'reset', group: 'Wartung', label: 'Letzter Neustart-Grund',
      tip: 'Warum die Box zuletzt neu gestartet ist. "Einschalten"/"Software-Neustart" sind normal. Watchdog/Absturz deuten auf ein Firmware-Problem, Brownout auf ein Netzteil-/Kabelproblem hin.',
      value: (s) => (na(s.reset_reason) ? 'n/a' : (RESET_REASONS[s.reset_reason] || String(s.reset_reason))),
      cls: (s) => (RESET_WARN.includes(String(s.reset_reason)) ? 'warn' : '') },
    { id: 'memory', group: 'Wartung', label: 'Freier Speicher',
      tip: 'Freier Arbeitsspeicher (Heap) der Box. Normal sind grob 150–250 kB. Sinkt der Wert über Tage stetig, deutet das auf ein Speicherleck hin; unter ~50 kB wird es kritisch. "Größter Block" (nur, falls die Firmware ihn meldet) ist das größte zusammenhängende freie Stück – unter ~30 kB können HTTPS-Verbindungen scheitern, obwohl in Summe noch genug frei ist.',
      value: (s) => {
        if (na(s.free_heap)) return 'n/a';
        const maxAlloc = parseInt(s.max_alloc_heap, 10);
        return fmtHeap(s.free_heap) + (isNaN(maxAlloc) ? '' : ` · größter Block ${Math.round(maxAlloc / 1024)} kB`);
      },
      cls: (s) => (lowMemory(s) ? 'warn' : '') },
    // NAS-seitig, nicht Box-seitig: Der Wächter (DSM-Aufgabe) muss auch dann
    // laufen, wenn die Box offline ist - deshalb live.
    { id: 'watchdog', group: 'Wartung', label: 'Offline-Wächter (NAS)', live: true,
      tip: 'Die DSM-Aufgabe auf dem NAS prüft alle 15 Minuten, ob die Box sich noch meldet, und schickt sonst eine Bark-Warnung. Hier steht, ob diese Aufgabe selbst noch läuft – der Wächter darf nicht unbemerkt ausfallen. Welche Meldungen er schickt, stellt der Admin unter „Einstellungen → Meldungen“ ein.',
      value: (s, ctx) => (ctx.watchdog ? ctx.watchdog.text : 'n/a'),
      cls: (s, ctx) => (ctx.watchdog && ctx.watchdog.warn ? 'warn' : '') },
    // Der Gegenpart von außen: eine Cloud-VM prüft alle 5 min, ob das NAS
    // erreichbar ist. Bewertet in outsideState(), hängt an ctx.watchdog.
    { id: 'outside', group: 'Wartung', label: 'Außen-Wächter (Cloud)', live: true,
      tip: 'Eine kleine Cloud-VM ruft das NAS alle 5 Minuten von außen auf und meldet per Bark, wenn es nicht erreichbar ist (Strom oder Internet am Standort weg, Router, Zertifikat) oder wenn die DSM-Aufgabe nicht mehr läuft. Das NAS selbst kann seinen eigenen Ausfall nicht melden. Hier steht, wann sich die VM zuletzt gemeldet hat.',
      value: (s, ctx) => (ctx.watchdog && ctx.watchdog.outside ? ctx.watchdog.outside.text : 'nicht eingerichtet'),
      cls: (s, ctx) => (ctx.watchdog && ctx.watchdog.outside && ctx.watchdog.outside.warn ? 'warn' : '') }
  ];

  const resolve = (v, ctx) => (typeof v === 'function' ? v(ctx) : v);

  // ctx = { offline, seenAge, watchdog (mit .outside), nasKeysVersion, stale }
  function boxRows(s, ctx) {
    s = s || {};
    ctx = ctx || {};
    // Unsicher = das NAS liefert gerade keine aktuellen Daten (Fehler oder
    // veraltet): Dann ist ALLES nur der letzte bekannte Stand - auch Laufzeit
    // und Wächter. Live bleiben die NAS-seitigen Zeilen nur bei "Box offline".
    const unsure = !!ctx.stale || !!ctx.statusError;
    const dim = !!ctx.offline;
    return ROW_DEFS.map((def) => {
      const cls = def.cls ? (def.cls(s, ctx) || '') : '';
      return {
        group: def.group,
        id: def.id,
        label: resolve(def.label, ctx),
        value: def.value(s, ctx),
        tone: unsure || (dim && !def.live) ? 'unknown' : cls,
        tip: resolve(def.tip, ctx)
      };
    });
  }

  // ---- Hinweise ------------------------------------------------------------
  // Abschließende Liste (Spec 6.2 "Was ein Hinweis ist"). Bei NAS-Störung
  // bzw. ohne Status: keine Hinweise aus alten Daten.
  function collectHints(ctx) {
    const s = ctx && ctx.status;
    if (!ctx || ctx.stale || ctx.statusError || !s) return [];
    const hints = [];
    const add = (tone, text) => hints.push({ tone, text });
    const rowOf = (id) => boxRows(s, {
      offline: false, seenAge: NaN, watchdog: ctx.watchdog || null, nasKeysVersion: ctx.nasKeysVersion, stale: false
    }).find((r) => r.id === id);
    const rowText = (id) => { const r = rowOf(id); return `${r.label}: ${r.value}`; };

    // Liste samt Sicherung auf dem NAS unlesbar: NICHT "keine eingetragen".
    if (ctx.keysCorrupt) {
      add('crit', 'Die Empfängerliste auf dem NAS ist beschädigt – siehe „Empfänger“.');
    }
    const sum = ctx.keysSummary;
    if (!ctx.demoOn && sum && sum.total > 0 && sum.muted === sum.total) {
      add('warn', `Alle ${sum.total} Empfänger sind stumm geschaltet – ein Alarm käme bei niemandem mit vollem Ton an.`);
    }
    // Fällige, aber nicht gespeicherte Laut-Rückschaltung: Das NAS zählt den
    // Eintrag als laut, die Box alarmiert ihn evtl. noch stumm.
    if (sum && sum.overdue > 0) {
      add('warn', `Empfänger: bei ${sum.overdue} ist die Rückschaltung nicht gespeichert – die Box alarmiert evtl. noch stumm.`);
    }
    // Fertiger Text vom Server: mehrere Boxen mit demselben Token bzw. ein
    // ignoriertes fremdes Board (dann stimmen die Statuswerte evtl. nicht).
    if (ctx.deviceHint) add('warn', ctx.deviceHint);
    const wd = ctx.watchdog;
    if (wd && wd.warn) add('warn', `Offline-Wächter (NAS): ${wd.text}`);
    if (wd && wd.outside && wd.outside.warn) add('warn', `Außen-Wächter (Cloud): ${wd.outside.text}`);
    if (!ctx.offline) {
      if (deliveryIncomplete(s)) {
        add('crit', `Nicht alle Empfänger erreicht (letzter Alarm der Box: ${show(s.key_progress)}) – ${ctx.readOnly
          ? 'ggf. einen Admin verständigen, er kann per „Alarm an alle senden“ nachsenden'
          : 'ggf. per „Alarm an alle senden“ nachsenden bzw. die übrigen anders verständigen'}. Wurde die Empfängerliste seit dem Alarm geändert, kann die Zählung verschoben sein.`);
      }
      if (s.wifi === 'down' || weakWifi(s)) add('warn', rowText('wifi'));
      if (heartbeatFailed(s)) add('warn', rowText('heartbeat'));
      if (resetWarnFresh(s)) add('warn', rowText('reset'));
      if (lowMemory(s)) add('warn', rowText('memory'));
      // Kurz nach jeder Änderung ist die Abweichung normal (die Box holt die
      // Liste beim nächsten Abgleich) - erst melden, wenn sie anhält.
      if (ctx.mismatchMs > 60000) {
        add('warn', `Empfängerliste: Die Box hat den aktuellen Stand seit ${fmtUptime(ctx.mismatchMs / 1000)} nicht übernommen.`);
      }
    }
    return hints;
  }

  // ---- Empfänger / Letzter Alarm -------------------------------------------
  // Ein Alarm gilt so lange als "frisch" (rot hervorgehoben, mit "vor X min").
  const ALARM_RECENT_S = 3600;

  function keysSummary(data) {
    if (!data || !Array.isArray(data.keys)) return null;
    return {
      total: data.keys.length,
      muted: data.keys.filter((entry) => entry.muted).length,
      permanent: data.keys.filter((entry) => entry.muted && entry.mute_permanent).length,
      overdue: data.keys.filter((entry) => entry.mute_overdue).length
    };
  }

  // Kurzform für die Prüfzeile ("6, 1 stumm"); Texte wie bisher im Held-Bereich.
  function recipientsFact(summary, demo, corrupt) {
    if (corrupt) return { text: 'Liste beschädigt', tone: 'crit' };
    if (demo && demo.enabled) {
      return { text: `nur ${demo.label || 'der Test-Empfänger'} (Demo)`, tone: 'warn' };
    }
    if (!summary) return { text: '…', tone: '' };
    const { total, muted, permanent, overdue } = summary;
    if (total === 0) return { text: 'keine eingetragen – die Box nutzt ihre Startliste', tone: 'warn' };
    const tail = overdue ? `; Rückschaltung bei ${overdue} nicht gespeichert – die Box alarmiert evtl. noch stumm` : '';
    if (muted === 0) return { text: `${total}, alle laut${tail}`, tone: overdue ? 'warn' : '' };
    if (muted === total) return { text: `${total}, ALLE stumm${tail}`, tone: 'warn' };
    return {
      text: `${total}, ${muted} stumm${permanent ? ` (${permanent} dauerhaft)` : ''}${tail}`,
      tone: overdue ? 'warn' : ''
    };
  }

  // ---- Urteil der Status-Karte ---------------------------------------------
  // Rangfolge (wichtigster gewinnt): Abgemeldet, NAS-Problem (überstimmt die
  // Box-Lampe, wie bisher showStatusProblem), Box offline, Alarm läuft,
  // Kontakt klemmt, Alarm ausgelöst, Demo-Modus, Hinweise, alles bereit.
  function nasProblemTexts(err, alarmInFlight) {
    if (!err) {
      return { title: 'Status veraltet', detail: 'Keine aktuelle Antwort vom NAS' };
    }
    if (err.kind === 'timeout' && alarmInFlight) {
      // Während ein Alarm-Versand läuft, kann das NAS Status-Abfragen
      // derselben Sitzung zurückstellen - das ist kein Ausfall.
      return { title: 'NAS beschäftigt', detail: 'Keine Status-Antwort, solange der Alarm-Versand läuft' };
    }
    if (err.kind === 'network' || err.kind === 'timeout') {
      return {
        title: 'NAS nicht erreichbar',
        detail: err.kind === 'timeout' ? 'Keine Antwort vom NAS (Zeitüberschreitung)' : 'Keine Verbindung zum NAS'
      };
    }
    // Jede andere Fehlerart mit Art-Kennung (http, csrf, readonly, mode_changed
    // ...) wie bisher mit Status und Servermeldung; nur ein Fehler ohne
    // Art-Kennung (kein ApiError) heißt "nicht auswertbar".
    if (typeof err.kind === 'string' && err.kind) {
      return {
        title: 'NAS-Störung',
        detail: `Das NAS meldet einen Fehler${err.status ? ` (HTTP ${err.status})` : ''}${err.message ? `: ${err.message}` : ''}`
      };
    }
    return { title: 'NAS-Störung', detail: 'Die Antwort des NAS konnte nicht ausgewertet werden' };
  }

  function verdict(ctx) {
    const make = (key, tone, title, sub, extra) => Object.assign(
      { key, tone, title, sub: sub || '', raiseActions: tone === 'crit' || tone === 'alarm' }, extra);
    const s = ctx.status;
    const offline = !!ctx.offline;

    if (ctx.authLost) return make('auth', 'warn', 'Abgemeldet', ctx.alarmInFlight
      ? 'Sitzung abgelaufen – bitte nach dem Lesen des Alarm-Ergebnisses die Seite neu laden (Anmeldung).'
      : 'Sitzung abgelaufen – die Anmeldeseite wird geladen…');

    if (ctx.statusError || ctx.stale) {
      const { title, detail } = nasProblemTexts(ctx.statusError, ctx.alarmInFlight);
      // War die Box beim letzten erfolgreichen Abruf OFFLINE, ist das die
      // wichtigste Aussage - und der Hinweis auf „Alarm an alle senden“ darf mit der
      // NAS-Störung nicht verschwinden. Rot bleibt rot.
      let sub;
      if (offline) {
        sub = ctx.readOnly
          ? 'Letzter bekannter Stand: Box OFFLINE – Relaisalarme kamen da NICHT an. Bitte einen Admin verständigen – er kann manuell alarmieren.'
          : 'Letzter bekannter Stand: Box OFFLINE – Relaisalarme kamen da NICHT an. Manuell alarmieren: „Alarm an alle senden“ direkt hier darunter (läuft über das NAS – kommt keine Antwort, die Mannschaft anders verständigen).';
      } else {
        sub = 'Das Dashboard bekommt gerade keine frischen Daten. Die Box alarmiert über Bark auch ohne das NAS – ob sie gerade läuft, lässt sich von hier aus aber nicht prüfen.';
      }
      return make('nas', offline ? 'crit' : 'warn', title, sub, { detail });
    }

    if (!s) return make('loading', 'warn', 'Status wird geladen', '');

    if (offline) {
      return make('offline', 'crit', 'Box offline', ctx.readOnly
        ? 'Relaisalarme kommen gerade NICHT an. Bitte einen Admin verständigen – er kann manuell alarmieren.'
        : 'Relaisalarme kommen gerade NICHT an. Manuell alarmieren geht weiterhin: „Alarm an alle senden“ direkt hier darunter (läuft direkt über das NAS).');
    }
    if (alarmRunning(s, offline)) {
      return make('alarm', 'alarm', 'Alarm läuft', `Die Box stellt gerade zu – bisher ${show(s.key_progress)} erreicht.`);
    }
    if (isStuck(s, offline)) {
      return make('stuck', 'warn', 'Kontakt klemmt',
        'Der Melder-Kontakt ist ungewöhnlich lange geschlossen. Solange erkennt die Box keinen neuen Alarm – Melder und Lader prüfen.');
    }
    if (alarmStanding(s, offline)) {
      return make('standing', 'crit', 'Alarm ausgelöst',
        `${deliveryIncomplete(s)
          ? `Zustellung beendet, aber nur ${show(s.key_progress)} erreicht – NICHT alle.`
          : `Zustellung abgeschlossen (${show(s.key_progress)} erreicht).`} Der Melder-Kontakt ist noch geschlossen – ein neuer Alarm wird erst erkannt, wenn er wieder offen war.`);
    }
    if (ctx.demo && ctx.demo.enabled) {
      return make('demo', 'warn', 'Demo-Modus',
        `Alarme gehen NUR an ${ctx.demo.label || 'den Test-Empfänger'} – alle anderen werden gerade nicht alarmiert.`);
    }
    const hints = ctx.hints || [];
    if (hints.length) {
      const tone = hints.some((h) => h.tone === 'crit') ? 'crit' : 'warn';
      return make('hints', tone, `Bereit – ${hints.length === 1 ? '1 Hinweis' : hints.length + ' Hinweise'}`,
        'Die Kette der Box arbeitet, aber es liegt etwas an – siehe unten.');
    }
    return make('ok', 'ok', 'Alles bereit', '');
  }

  // ---- Prüfliste der Übersicht ---------------------------------------------
  function overviewRows(ctx) {
    const s = ctx.status || {};
    const offline = !!ctx.offline;
    const unsure = !!(ctx.stale || ctx.statusError);
    const noStatus = !ctx.status;
    const dim = offline || unsure || noStatus;
    const rows = [];

    // Box
    let boxValue;
    let boxTone = '';
    if (noStatus) {
      boxValue = 'unbekannt';
      boxTone = 'unknown';
    } else if (offline) {
      boxValue = Number.isFinite(ctx.seenAge) ? `offline seit ${fmtUptime(ctx.seenAge)}` : 'offline (noch nie gemeldet)';
      boxTone = unsure ? 'unknown' : 'crit';
    } else {
      boxValue = na(s.uptime_s) ? 'online' : `online · läuft seit ${fmtUptime(s.uptime_s)}`;
      boxTone = unsure ? 'unknown' : '';
    }
    rows.push({ id: 'box', label: 'Box', value: boxValue, tone: boxTone, href: '#/einstellungen/box' });

    // WLAN
    let wifiValue = 'unbekannt';
    let wifiTone = 'unknown';
    if (!dim) {
      if (s.wifi === 'down') {
        wifiValue = 'GETRENNT';
        wifiTone = 'crit';
      } else if (!na(s.rssi) && rssiQuality(s.rssi)) {
        wifiValue = `${rssiQuality(s.rssi)} · ${parseInt(s.rssi, 10)} dBm`;
        wifiTone = weakWifi(s) ? 'warn' : '';
      }
    }
    rows.push({ id: 'wifi', label: 'WLAN', value: wifiValue, tone: wifiTone, href: null });

    // Wächter (NAS-seitig: bleibt auch bei offline Box aktuell)
    const wd = ctx.watchdog;
    let watchValue = 'unbekannt';
    let watchTone = 'unknown';
    if (wd) {
      const parts = [];
      if (wd.warn) parts.push(`NAS: ${wd.text}`);
      if (wd.outside && wd.outside.warn) parts.push(`Cloud: ${wd.outside.text}`);
      if (parts.length) {
        watchValue = parts.join(' · ');
        watchTone = unsure ? 'unknown' : 'warn';
      } else {
        watchValue = wd.outside ? 'NAS + Cloud aktiv' : 'NAS aktiv';
        watchTone = unsure ? 'unknown' : '';
      }
    }
    rows.push({ id: 'watch', label: 'Wächter', value: watchValue, tone: watchTone, href: '#/einstellungen/box' });

    // Empfänger
    const rf = recipientsFact(ctx.keysSummary, ctx.demo, ctx.keysCorrupt);
    rows.push({ id: 'keys', label: 'Empfänger', value: rf.text, tone: rf.tone, href: '#/empfaenger' });

    // Letzter Alarm
    const af = ctx.alarmFact || { text: '…', tone: '' };
    rows.push({ id: 'alarm', label: 'Letzter Alarm', value: af.text, tone: af.tone, href: '#/verlauf?filter=alarm' });
    return rows;
  }

  FW.logic = {
    setTimezone, tz,
    na, show, flagged,
    fmtUptime, rssiQuality, fmtRssi, fmtHeap,
    fmtDateTime, fmtClock, fmtClockMs, fmtDayClock, dayKey, fmtDayLabel, fmtDayTime, fmtRelTime, fmtAgeShort,
    RESET_REASONS, RESET_WARN, HEAP_WARN, MAX_ALLOC_WARN, ALARM_RECENT_S,
    keyProgressParts, deliveryIncomplete, heartbeatFailed, weakWifi, lowMemory, resetWarnFresh,
    isStuck, alarmStanding,
    watchdogState, outsideState,
    boxRows, collectHints, verdict, overviewRows, keysSummary, recipientsFact
  };
})(typeof window !== 'undefined' ? window : globalThis);
