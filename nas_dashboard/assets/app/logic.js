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
      return { warn: true, text: 'noch nie gelaufen – Wächter-Timer auf dem Server eingerichtet?' };
    }
    const age = Math.max(0, serverTime - info.last_run);
    // Der Wächter-Timer läuft alle 15 min - erst warnen, wenn mehr als zwei
    // Läufe ausgefallen sind (35 min).
    if (age > 2100) {
      return { warn: true, text: `seit ${fmtUptime(age)} nicht gelaufen – Wächter-Timer prüfen!` };
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
      return { warn: true, text: 'läuft, aber die Meldung „Dashboard defekt“ ist abgeschaltet' };
    }
    return { warn: false, text: `läuft (zuletzt ${age < 60 ? 'gerade eben' : 'vor ' + fmtUptime(age)})` };
  }

  // Außen-Wächter (seit dem Umzug: das NAS): klopft alle 5 min über api/alive.php an.
  // Ab wann sie als stumm gilt, meldet der Server (outside_silent_after_s);
  // ältere Stände ohne das Feld: eine Stunde.
  // null = nicht eingerichtet.
  const OUTSIDE_SILENT_FALLBACK_S = 3600;
  function outsideState(info, serverTime) {
    if (!info || info.outside_seen_at === undefined) return null;
    const seen = Number(info.outside_seen_at) || 0;
    if (!seen) return { warn: true, text: 'eingerichtet, hat sich aber noch nie gemeldet – läuft die DSM-Aufgabe fw-alarm-watch auf dem NAS?' };
    const age = Math.max(0, serverTime - seen);
    const silentAfter = Number(info.outside_silent_after_s) > 0
      ? Number(info.outside_silent_after_s) : OUTSIDE_SILENT_FALLBACK_S;
    if (age > silentAfter) {
      return { warn: true, text: `meldet sich seit ${fmtUptime(age)} nicht – NAS zu Hause prüfen (Strom/Internet, DSM-Aufgabe fw-alarm-watch)` };
    }
    const last = age < 60 ? 'gerade eben' : 'vor ' + fmtUptime(age);
    // Der Außen-Wächter klopft zwar an, kann aber seine eigenen Bark-Meldungen
    // nicht zustellen - fiele der Server aus, käme keine Warnung.
    const barkFailedAt = Number(info.outside_bark_failed_at) || 0;
    if (barkFailedAt > 0) {
      return { warn: true, text: `meldet sich (zuletzt ${last}), kann aber nicht melden (Bark-Versand fehlgeschlagen seit ${fmtDayTime(barkFailedAt, serverTime)}) – BARK_KEY in fw-alarm-watch.conf auf dem NAS prüfen` };
    }
    // Hat der Admin "Außen-Wächter meldet sich nicht" abgeschaltet, darf das
    // nicht in Vergessenheit geraten (outside_alert kommt nur für den Admin).
    if (info.outside_alert === false) {
      return { warn: true, text: `meldet sich (zuletzt ${last}), aber die Meldung „Außen-Wächter meldet sich nicht“ ist abgeschaltet – fiele das NAS aus, käme keine Warnung` };
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
      tip: 'Zeitpunkt und Text des letzten ausgelösten Alarms aus Sicht der Box (seit ihrem letzten Neustart) und – falls die Firmware es meldet – woher er kam: Melder-Relais oder DEMO-Alarm per Dashboard-Befehl. "nie" heißt: kein Alarm seit dem letzten Einschalten. Den dauerhaften Verlauf führt der Server („Verlauf“).',
      value: (s) => {
        const text = show(s.last_alarm);
        if (na(s.alarm_source) || na(s.last_alarm) || s.last_alarm === 'nie') return text;
        return `${text} · ${ALARM_SOURCES[s.alarm_source] || String(s.alarm_source)}`;
      } },
    { id: 'wifi', group: 'Verbindung', label: 'WLAN',
      tip: 'Empfangsstärke der Box im WLAN des Magazins und ihre aktuelle Adresse. Näher an 0 dBm ist besser: ab ca. -67 gut, unter ca. -80 unzuverlässig – dann Box oder Router umstellen. Ohne WLAN kann die Box weder Bark-Alarme senden noch den Server erreichen.',
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
      tip: 'Empfängerliste, wie sie die Box gespeichert hat: Anzahl, davon stumm (Arbeitsmodus) und die Listen-Version. Stimmt die Version mit dem Server überein, ist die Liste (auch ein Demo-Wechsel) angekommen. Im Demo-Modus steht hier genau 1 Empfänger.',
      value: (s, ctx) => {
        if (na(s.keys_count)) return 'n/a';
        let text = `${s.keys_count} Empfänger`;
        const muted = parseInt(s.keys_muted, 10);
        if (!isNaN(muted) && muted > 0) text += ` (${muted} stumm)`;
        if (!na(s.keys_version)) {
          text += ` · v${s.keys_version}`;
          if (!na(ctx.nasKeysVersion)) {
            text += String(s.keys_version) === String(ctx.nasKeysVersion)
              ? ' (= Server)'
              : ` (Server: v${ctx.nasKeysVersion} – Übernahme steht aus)`;
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
      tip: 'Hat die Box einen Befehl aus dem Dashboard ausgeführt, dessen Bestätigung noch nicht beim Server angekommen ist? Normalerweise "keine offen" – ein Rückstand wird automatisch nachgereicht.',
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
        ? 'Zeit seit dem letzten Lebenszeichen der Box beim Server. Mögliche Ursachen: Stromausfall, WLAN weg oder Server nicht erreichbar.'
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
    // Server-seitig, nicht Box-seitig: Der Wächter (Timer) muss auch dann
    // laufen, wenn die Box offline ist - deshalb live.
    { id: 'watchdog', group: 'Wartung', label: 'Offline-Wächter (Server)', live: true,
      tip: 'Der Wächter-Timer auf dem Server prüft alle 15 Minuten, ob die Box sich noch meldet, und schickt sonst eine Bark-Warnung. Hier steht, ob dieser Timer selbst noch läuft – der Wächter darf nicht unbemerkt ausfallen. Welche Meldungen er schickt, stellt der Admin unter „Einstellungen → Meldungen“ ein.',
      value: (s, ctx) => (ctx.watchdog ? ctx.watchdog.text : 'n/a'),
      cls: (s, ctx) => (ctx.watchdog && ctx.watchdog.warn ? 'warn' : '') },
    // Der Gegenpart von außen: das NAS prüft alle 5 min, ob der Server
    // erreichbar ist. Bewertet in outsideState(), hängt an ctx.watchdog.
    { id: 'outside', group: 'Wartung', label: 'Außen-Wächter (NAS)', live: true,
      tip: 'Das NAS zu Hause ruft den Server alle 5 Minuten von außen auf und meldet per Bark, wenn er nicht erreichbar ist (Server gestoppt, Tunnel oder Internet gestört, Zertifikat) oder wenn der Wächter-Timer dort nicht mehr läuft. Der Server selbst kann seinen eigenen Ausfall nicht melden. Hier steht, wann sich das NAS zuletzt gemeldet hat.',
      value: (s, ctx) => (ctx.watchdog && ctx.watchdog.outside ? ctx.watchdog.outside.text : 'nicht eingerichtet'),
      cls: (s, ctx) => (ctx.watchdog && ctx.watchdog.outside && ctx.watchdog.outside.warn ? 'warn' : '') }
  ];

  const resolve = (v, ctx) => (typeof v === 'function' ? v(ctx) : v);

  // ctx = { offline, seenAge, watchdog (mit .outside), nasKeysVersion, stale }
  function boxRows(s, ctx) {
    s = s || {};
    ctx = ctx || {};
    // Unsicher = das NAS liefert gerade keine aktuellen Daten (Fehler,
    // veraltet oder abgemeldet - dann ruht jede Abfrage): Dann ist ALLES nur
    // der letzte bekannte Stand - auch Laufzeit und Wächter. Live bleiben die
    // NAS-seitigen Zeilen nur bei "Box offline".
    const unsure = !!ctx.stale || !!ctx.statusError || !!ctx.authLost;
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
      add('crit', 'Die Empfängerliste auf dem Server ist beschädigt – siehe „Empfänger“.');
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
    if (wd && wd.warn) add('warn', `Offline-Wächter (Server): ${wd.text}`);
    if (wd && wd.outside && wd.outside.warn) add('warn', `Außen-Wächter (NAS): ${wd.outside.text}`);
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
      return { title: 'Status veraltet', detail: 'Keine aktuelle Antwort vom Server' };
    }
    if (err.kind === 'timeout' && alarmInFlight) {
      // Während ein Alarm-Versand läuft, kann das NAS Status-Abfragen
      // derselben Sitzung zurückstellen - das ist kein Ausfall.
      return { title: 'Server beschäftigt', detail: 'Keine Status-Antwort, solange der Alarm-Versand läuft' };
    }
    if (err.kind === 'network' || err.kind === 'timeout') {
      return {
        title: 'Server nicht erreichbar',
        detail: err.kind === 'timeout' ? 'Keine Antwort vom Server (Zeitüberschreitung)' : 'Keine Verbindung zum Server'
      };
    }
    // Jede andere Fehlerart mit Art-Kennung (http, csrf, readonly, mode_changed
    // ...) wie bisher mit Status und Servermeldung; nur ein Fehler ohne
    // Art-Kennung (kein ApiError) heißt "nicht auswertbar".
    if (typeof err.kind === 'string' && err.kind) {
      return {
        title: 'Server-Störung',
        detail: `Der Server meldet einen Fehler${err.status ? ` (HTTP ${err.status})` : ''}${err.message ? `: ${err.message}` : ''}`
      };
    }
    return { title: 'Server-Störung', detail: 'Die Antwort des Servers konnte nicht ausgewertet werden' };
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
          : 'Letzter bekannter Stand: Box OFFLINE – Relaisalarme kamen da NICHT an. Manuell alarmieren: „Alarm an alle senden“ direkt hier darunter (läuft über den Server – kommt keine Antwort, die Mannschaft anders verständigen).';
      } else {
        sub = 'Das Dashboard bekommt gerade keine frischen Daten. Die Box alarmiert über Bark auch ohne den Server – ob sie gerade läuft, lässt sich von hier aus aber nicht prüfen.';
      }
      return make('nas', offline ? 'crit' : 'warn', title, sub, { detail });
    }

    if (!s) return make('loading', 'warn', 'Status wird geladen', '');

    if (offline) {
      return make('offline', 'crit', 'Box offline', ctx.readOnly
        ? 'Relaisalarme kommen gerade NICHT an. Bitte einen Admin verständigen – er kann manuell alarmieren.'
        : 'Relaisalarme kommen gerade NICHT an. Manuell alarmieren geht weiterhin: „Alarm an alle senden“ direkt hier darunter (läuft direkt über den Server).');
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
    // Abgemeldet: Abfragen ruhen, alles ist nur noch der letzte Stand.
    const unsure = !!(ctx.stale || ctx.statusError || ctx.authLost);
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
      if (wd.warn) parts.push(`Server: ${wd.text}`);
      if (wd.outside && wd.outside.warn) parts.push(`NAS: ${wd.outside.text}`);
      if (parts.length) {
        watchValue = parts.join(' · ');
        watchTone = unsure ? 'unknown' : 'warn';
      } else {
        watchValue = wd.outside ? 'Server + NAS aktiv' : 'Server aktiv';
        watchTone = unsure ? 'unknown' : '';
      }
    }
    rows.push({ id: 'watch', label: 'Wächter', value: watchValue, tone: watchTone, href: '#/einstellungen/box' });

    // Empfänger und Letzter Alarm kommen vom NAS, nicht von der Box: Bei
    // offline Box bleiben sie aktuell - ohne frische NAS-Antwort sind sie
    // aber genauso unsicher wie der Rest (grau, Spec §5.3).
    const rf = ctx.keysError && !ctx.keysCorrupt && !ctx.keysSummary
      ? { text: 'nicht abrufbar', tone: 'unknown' }
      : recipientsFact(ctx.keysSummary, ctx.demo, ctx.keysCorrupt);
    rows.push({ id: 'keys', label: 'Empfänger', value: rf.text, tone: unsure ? 'unknown' : rf.tone, href: '#/empfaenger' });

    // Letzter Alarm: Scheitert sein eigener Abruf (Verlauf, filter=alarm_core),
    // steht der alte Stand grau und mit Zusatz da - er darf nicht frisch
    // aussehen (z.B. beim Prüfen nach „Antwort unklar“).
    const af = ctx.alarmFact || { text: '…', tone: '' };
    let alarmValue = af.text;
    let alarmTone = af.tone;
    if (ctx.alarmInfoFailed) {
      alarmValue = ctx.alarmInfoKnown === false ? 'nicht abrufbar' : `${af.text} (nicht aktualisiert)`;
      alarmTone = 'unknown';
    } else if (unsure) {
      alarmTone = 'unknown';
    }
    rows.push({ id: 'alarm', label: 'Letzter Alarm', value: alarmValue, tone: alarmTone, href: '#/verlauf?filter=alarm' });
    return rows;
  }

  // ---- Band „keine aktuelle Antwort“ (jede Seite) --------------------------
  // Solange der Stand unsicher ist (Status-Abfrage gescheitert oder seit 15 s
  // keine Antwort), steht das oben auf JEDER Seite - Wortlaut wie im Urteil
  // (nasProblemTexts), dazu die Uhrzeit des letzten erfolgreichen Abrufs.
  // Rot, wenn der letzte bekannte Stand „Box offline“ war (Rot bleibt rot).
  // Abgemeldet: nur, solange die Seite für ein Alarm-Ergebnis stehen bleibt
  // (sonst lädt sie gleich die Anmeldung). ctx.lastOkMs = Server-Zeit (ms)
  // der letzten erfolgreichen Status-Antwort, 0 = noch keine. null = kein Band.
  function unsureBand(ctx) {
    if (ctx.authLost) {
      return ctx.alarmInFlight
        ? { tone: 'warn', text: 'Abgemeldet – angezeigte Werte werden nicht mehr aktualisiert. Nach dem Lesen des Alarm-Ergebnisses bitte neu anmelden.' }
        : null;
    }
    if (!ctx.stale && !ctx.statusError) return null;
    const { detail } = nasProblemTexts(ctx.statusError, ctx.alarmInFlight);
    const at = ctx.lastOkMs > 0 ? fmtClockMs(ctx.lastOkMs) : '';
    if (!at) return { tone: 'warn', text: `${detail} – es liegen noch keine Werte vom Server vor` };
    if (ctx.offline) return { tone: 'crit', text: `${detail} – letzter bekannter Stand (${at}): Box OFFLINE` };
    return { tone: 'warn', text: `${detail} – angezeigte Werte können veraltet sein (Stand ${at})` };
  }

  // ==== Teil 2: Alarm-Info, Befehlszeile, Verlauf, Nicht-Erreichte, Empfänger,
  // Bestätigungsfragen ========================================================

  // ---- Letzter Alarm -------------------------------------------------------
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
    for (const e of entries || []) {
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

  // alarmInfo: undefined = noch nicht geladen, null = nichts im Verlauf.
  function alarmFact(alarmInfo, status, now) {
    if (alarmInfo === undefined) return { text: '…', tone: '' };
    if (!alarmInfo) {
      // Nichts im NAS-Verlauf: ersatzweise die Sicht der Box seit ihrem
      // letzten Neustart.
      const raw = status && !na(status.last_alarm) && status.last_alarm !== 'nie'
        ? String(status.last_alarm) : '';
      return { text: raw || 'keiner aufgezeichnet', tone: '' };
    }
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
    return { text, tone: recent ? (alarmInfo.clearedAt && alarmInfo.clearedOk ? 'warn' : 'crit') : '' };
  }

  // ---- Befehlszeile (TEST, DEMO-ALARM über die Box) -------------------------
  // Bewusst "die Box" statt "ESP32" in allen sichtbaren Texten.
  const STATE_LABELS = {
    pending: 'wartet auf die Box',
    delivered: 'an die Box ausgeliefert, Bestätigung offen',
    acked: 'bestätigt',
    expired: 'abgelaufen (die Box hat ihn nicht abgeholt)',
    cancelled: 'abgebrochen'
  };
  // Befehls-Quittungen der Firmware sind feste englische Tokens/Sätze - hier
  // übersetzt. Unbekannte Werte (künftige Firmware) erscheinen unverändert.
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

  // Abgeschlossene Befehle nur so lange anzeigen, danach ausblenden.
  const COMMAND_LINGER_S = 600;
  // Ausgeliefert, aber nach so langer Zeit noch keine Quittung.
  const COMMAND_ACK_GIVEUP_S = 600;

  // null = nichts anzeigen. tone: '' (läuft) / 'warn' (nicht wie gewünscht
  // ausgeführt) / 'done' (erledigt, grau).
  function commandLine(command, serverTime) {
    if (command && serverTime) {
      const finishedAt = {
        acked: command.acked_at,
        expired: command.expired_at,
        cancelled: command.cancelled_at
      }[command.state];
      if (finishedAt && serverTime - finishedAt > COMMAND_LINGER_S) command = null;
    }
    if (!command) return null;
    // Ausgeliefert, aber seit COMMAND_ACK_GIVEUP_S keine Quittung: nicht mehr
    // als "läuft" zeigen - das Ergebnis ist dann schlicht unbekannt.
    const deliveredAt = Number(command.delivered_at) || 0;
    const ackLost = command.state === 'delivered' && !!serverTime && deliveredAt > 0
      && serverTime - deliveredAt > COMMAND_ACK_GIVEUP_S;
    const label = ackLost
      ? 'an die Box ausgeliefert – Bestätigung nicht angekommen'
      : (STATE_LABELS[command.state] || command.state);
    let text = `Befehl #${command.id} ${command.type}: ${label}`;
    if (command.state === 'acked' && command.ack_result) {
      text += ` – ${fmtAckResult(command.ack_result)}`;
      if (command.ack_message) text += ` (${fmtAckMessage(command.ack_message)})`;
    }
    // Vom NAS selbst verworfen (Sicherung beim Ausliefern): den Grund zeigen.
    if (command.state === 'cancelled' && command.cancel_reason) {
      text += ` – ${command.cancel_reason}`;
    }
    // Alles, was NICHT wie gewünscht ausgeführt wurde, ist gelb.
    const warn = (command.state === 'acked' && !!command.ack_result && command.ack_result !== 'executed')
      || command.state === 'expired'
      || (command.state === 'cancelled' && !!command.cancel_reason);
    const done = ackLost || !['pending', 'delivered'].includes(command.state);
    return { text, tone: warn ? 'warn' : (done ? 'done' : ''), canCancel: command.state === 'pending' };
  }

  // ---- Verlauf -------------------------------------------------------------
  // Die Box schickt als Alarm-Info "[13.11. 21:42] <Bark-Text>" (ASCII). Für
  // den Verlauf zählt daraus nur die Zeit, zu der die BOX den Alarm erkannt hat.
  function boxAlarmTime(info) {
    const match = /^\[([^\]]+)\]/.exec(String(info || ''));
    if (match) return ` (von der Box erkannt: ${match[1]})`;
    return info ? `: ${info}` : '';
  }

  // tone: 'alarm' | 'warn' | 'ok' | ''; icon: siehe Spec 6.4.
  function describeLogEntry(entry) {
    const who = entry.by ? ` – von ${entry.by}` : '';
    const cmd = `Befehl #${entry.id} ${entry.type || ''}`.trim();
    const mk = (text, tone, icon) => ({ text, tone, icon });
    switch (entry.event) {
      case 'created':
        return mk(`${cmd} angelegt${who}`, '', 'command');
      case 'delivered':
        return mk(`${cmd} an die Box ausgeliefert`, '', 'command');
      case 'acked':
        return mk(`${cmd} bestätigt${entry.result ? `: ${fmtAckResult(entry.result)}` : ''}${entry.message ? ` (${fmtAckMessage(entry.message)})` : ''}`, '', 'command');
      case 'expired':
        return mk(`${cmd} abgelaufen (die Box hat ihn nicht abgeholt)`, 'warn', 'command');
      case 'cancelled':
        // Mit Grund (entry.message) hat die Sicherung im NAS den Befehl
        // verworfen, z.B. weil der Demo-Modus inzwischen gewechselt hat.
        return mk(`${cmd} abgebrochen${who}${entry.message ? ` – ${entry.message}` : ''}`, entry.message ? 'warn' : '', 'command');
      case 'late_ack':
        return mk(`Verspätete Bestätigung zu Befehl #${entry.id}${entry.type ? ` ${entry.type}` : ''}${entry.result ? `: ${fmtAckResult(entry.result)}` : ''}`, 'warn', 'command');
      case 'demo_enabled':
        return mk(`Demo-Modus EIN – Alarme nur an "${entry.label}"${who}`, 'warn', 'demo');
      case 'demo_disabled':
        return mk(`Demo-Modus AUS – wieder LIVE${who}`, '', 'demo');
      case 'demo_repaired':
        // Vom NAS selbst erkannt und behoben (keine weiteren Felder).
        return mk('Demo-Zustand war beschädigt – automatisch auf LIVE zurückgesetzt, die Box holt die volle Empfängerliste', 'warn', 'demo');
      case 'key_added':
        return mk(`Empfänger "${entry.label}" (${entry.key}) hinzugefügt${who}`, 'ok', 'add');
      case 'key_removed':
        return mk(`Empfänger "${entry.label}" (${entry.key}) gelöscht${who}`, '', 'remove');
      case 'key_renamed':
        return mk(`Empfänger "${entry.old_label}" umbenannt in "${entry.label}"${who}`, '', 'edit');
      case 'key_replaced':
        return mk(`Bark-Key von "${entry.label}" ersetzt (${entry.old_key} → ${entry.key})${who}`, '', 'edit');
      case 'key_phone_changed':
        return mk(`Telefonnummer von "${entry.label}" geändert${who}`, '', 'edit');
      case 'key_token_renewed':
        return mk(`Neue Kurzbefehl-Links für "${entry.label}" erzeugt (die alten gelten nicht mehr)${who}`, '', 'edit');
      case 'key_muted': {
        const src = entry.source === 'shortcut' ? ' – per Kurzbefehl-Link' : who;
        return mk(`Empfänger "${entry.label}" stummgeschaltet (Arbeitsmodus, Alarme mit Stumm-Lautstärke)${src}`, 'warn', 'mute');
      }
      case 'key_unmuted': {
        const src = entry.source === 'shortcut' ? ' – per Kurzbefehl-Link'
          : (entry.source === 'auto' ? ' – automatisch (Zeitlimit erreicht)' : who);
        return mk(`Empfänger "${entry.label}" wieder laut geschaltet${src}`, '', 'unmute');
      }
      case 'key_mute_volume':
        return mk(`Stumm-Lautstärke für "${entry.label}" auf ${entry.volume === 0 ? '0 (lautlos)' : entry.volume} gestellt${who}`, '', 'mute');
      case 'key_mute_permanent': {
        const src = entry.source === 'shortcut' ? ' – per Kurzbefehl-Link' : who;
        return entry.permanent
          ? mk(`Empfänger "${entry.label}" DAUERHAFT stumm geschaltet (keine automatische Rückschaltung mehr)${src}`, 'warn', 'mute')
          : mk(`Dauerhafte Stummschaltung für "${entry.label}" beendet (läuft wieder automatisch aus)${src}`, '', 'unmute');
      }
      case 'key_test':
        return entry.ok
          ? mk(`Test-Push an "${entry.label}" gesendet${who}`, '', 'test')
          : mk(`Test-Push an "${entry.label}" FEHLGESCHLAGEN${who}`, 'warn', 'test');
      case 'notify_changed':
        // key 'enabled' = eine Art von Meldung wurde ein- bzw. abgeschaltet;
        // sonst wurde eine einzelne Einstellung geändert.
        return entry.key === 'enabled'
          ? mk(`Meldung an den Admin "${entry.label}" ${entry.value}${who}`, entry.value === 'abgeschaltet' ? 'warn' : '', 'settings')
          : mk(`Meldungen an den Admin, ${entry.label} – ${entry.option}: ${entry.value}${who}`, '', 'settings');
      case 'setting_changed':
        return mk(`Einstellung „${entry.label}“: ${entry.value}${who}`, '', 'settings');
      // *_started wird VOR dem Versand geschrieben: Steht danach kein
      // Abschluss-Eintrag (nas_alarm/false_alarm) im Verlauf, ist der Versand
      // abgebrochen - wichtig nach einer "Antwort unklar"-Meldung.
      case 'nas_alarm_started':
        return mk((entry.resend
          ? `REAL ALARM: Nachsendung an ${entry.resend} Empfänger gestartet`
          : 'REAL ALARM gestartet') + (entry.demo === true ? ' (DEMO-MODUS)' : '') + who, 'alarm', 'alarm');
      case 'false_alarm_started':
        return mk((entry.resend
          ? `ENTWARNUNG: Nachsendung an ${entry.resend} Empfänger gestartet`
          : 'ENTWARNUNG gestartet') + (entry.demo === true ? ' (DEMO-MODUS)' : '') + who, 'warn', 'clear');
      case 'nas_alarm':
        return mk(`REAL ALARM direkt vom Server: ${entry.message || ''}${who}`, 'alarm', 'alarm');
      case 'false_alarm':
        return mk(`ENTWARNUNG (Fehlalarm) gesendet: ${entry.message || ''}${who}`, 'warn', 'clear');
      case 'relay_alarm':
        // source 'remote' = per Dashboard-Befehl ausgelöster DEMO-Alarm (kein Melder).
        return entry.source === 'remote'
          ? mk(`DEMO-Alarm über die Box ausgelöst (Dashboard-Befehl)${boxAlarmTime(entry.info)}`, 'warn', 'alarm')
          : mk(`RELAISALARM – der Melder hat ausgelöst${boxAlarmTime(entry.info)}`, 'alarm', 'alarm');
      case 'box_offline':
        return mk(`Box OFFLINE gemeldet (kein Status seit ${entry.minutes} min)`, 'warn', 'box');
      case 'box_recovered':
        return mk(`Box wieder online (Ausfall ca. ${entry.minutes} min)`, 'ok', 'box');
      case 'box_restart':
        return mk(`Box neu gestartet – Grund: ${RESET_REASONS[entry.reason] || entry.reason || 'unbekannt'}`, 'warn', 'box');
      case 'nas_web_failed':
        return mk(`Dashboard gestört: ${entry.detail || 'Selbsttest fehlgeschlagen'}`, 'alarm', 'box');
      case 'nas_web_recovered':
        return mk(`Dashboard wieder in Ordnung (Störung ca. ${entry.minutes} min)`, 'ok', 'box');
      case 'watcher_silent':
        return mk(`Außen-Wächter (NAS) meldet sich nicht (seit ${entry.minutes} min)`, 'warn', 'watch');
      case 'watcher_recovered':
        return mk(`Außen-Wächter (NAS) wieder da (Pause ca. ${entry.minutes} min)`, 'ok', 'watch');
      default:
        return mk(String(entry.event || '?'), '', 'settings');
    }
  }

  // Standardmäßig die letzten 25 Ereignisse, in 25er-Schritten bis zum
  // Server-Deckel von 100.
  const LOG_FILTERS = ['all', 'alarm', 'keys', 'system'];
  const LOG_STEP = 25;
  const LOG_MAX = 100;
  const LOG_EMPTY = {
    all: 'Noch keine Ereignisse aufgezeichnet.',
    alarm: 'Keine Alarme im Verlauf.',
    keys: 'Keine Änderungen an den Empfängern im Verlauf.',
    system: 'Keine System-Ereignisse im Verlauf.'
  };

  function logMatches(entry, query) {
    const q = String(query || '').trim().toLowerCase();
    if (!q) return true;
    return describeLogEntry(entry).text.toLowerCase().includes(q);
  }

  // ---- Nicht Erreichte -----------------------------------------------------
  // Grund eines fehlgeschlagenen Empfängers: http 0 heißt beim NAS-Versand
  // "keine Verbindung zu Bark bzw. Zeitüberschreitung" - nicht "HTTP 0".
  const failReason = (r) => (Number(r.http) === 0 ? 'keine Verbindung/Timeout' : `HTTP ${r.http}`);

  const whatOf = (action) => (action === 'alarm' ? 'REAL ALARM' : 'ENTWARNUNG');

  // Gemerkte nicht Erreichte (aus localStorage o.ä.) streng prüfen: Was nicht
  // genau so aussieht wie von buildUnreached() geliefert, wird verworfen -
  // ebenso ein Stand, der älter als ein halber Tag ist.
  const UNREACHED_MAX_AGE_MS = 12 * 3600 * 1000;
  function validateUnreached(raw, nowMs) {
    if (!raw) return null;
    try {
      const v = JSON.parse(raw);
      const okEntry = (e) => !!e && Number.isInteger(e.id) && e.id >= 0
        && typeof e.name === 'string' && typeof e.reason === 'string';
      if (v && (v.action === 'alarm' || v.action === 'false_alarm') && Number.isFinite(v.at)
          && nowMs - v.at < UNREACHED_MAX_AGE_MS
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
      // kaputter Eintrag -> verwerfen
    }
    return null;
  }

  // Merker „Versand ohne Ergebnis“ (localStorage): gesetzt beim Start von
  // REAL ALARM/ENTWARNUNG, bleibt bei „Antwort unklar“ stehen, gelöscht bei
  // einem eindeutigen Ergebnis. Steht er nach dem Neuladen noch da (iOS
  // verwirft die Home-Bildschirm-App z.B. beim Wechsel in die Telefon-App),
  // gibt es KEIN Nachsende-Angebot - erst Verlauf prüfen. Gleiche Prüfung und
  // Höchstalter wie bei den nicht Erreichten.
  function validateSendMarker(raw, nowMs) {
    if (!raw) return null;
    try {
      const v = JSON.parse(raw);
      if (v && (v.action === 'alarm' || v.action === 'false_alarm') && Number.isFinite(v.at)
          && nowMs - v.at < UNREACHED_MAX_AGE_MS) {
        return { action: v.action, what: whatOf(v.action), at: v.at };
      }
    } catch (err) {
      // kaputter Eintrag -> verwerfen
    }
    return null;
  }

  const sendMarkerNote = (m) => `Letzter Versand ohne Ergebnis (${m.what}, ${fmtClockMs(m.at)} Uhr) – erst Verlauf prüfen, nicht sofort erneut auslösen! Er kann trotzdem rausgegangen sein.`;

  // Rein: liefert die neue Liste der nicht Erreichten (das Speichern macht der
  // Store). prev = bisheriger Stand, resend = { ids, origAt } bei einer
  // Nachsendung, sonst null. Nur ein Ergebnis MIT Versand (results) ändert die
  // Liste - "nichts gesendet" lässt sie (dasselbe Objekt) stehen.
  function buildUnreached(prev, action, data, resend, nowMs) {
    const results = Array.isArray(data && data.results) ? data.results : [];
    const failedRaw = results.filter((r) => !r.ok);
    const reachedRaw = results.filter((r) => r.ok);
    const name = (r) => r.label || r.key || '?';
    const entryOf = (r) => ({ id: Number(r.id) > 0 ? Number(r.id) : 0, name: name(r), reason: failReason(r) });
    const failed = failedRaw.map(entryOf);
    const reached = reachedRaw.map(name);
    if (!results.length) return { unreached: prev || null, reached, failed };

    let entries = failed.slice();
    // Server-Zeit des URSPRÜNGLICHEN Versands - für weitere Nachsendungen.
    let startedAt = Number(data.started_at) > 0 ? Number(data.started_at) : 0;
    if (resend) {
      // Nachsendung: Die alte Liste bleibt die Grundlage - gestrichen wird
      // nur, wer JETZT erreicht wurde; wer erneut scheitert, bekommt den
      // neuen Grund. Wer nicht mitgesendet wurde (ohne id, oder inzwischen
      // aus der Liste gelöscht), bleibt offen.
      startedAt = resend.origAt > 0 ? resend.origAt : (prev && prev.startedAt) || 0;
      if (prev && prev.action === action) {
        const fresh = new Map(entries.filter((e) => e.id > 0).map((e) => [e.id, e]));
        const reachedIds = new Set(reachedRaw.map((r) => Number(r.id)).filter((id) => id > 0));
        const merged = [];
        prev.entries.forEach((e) => {
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
    // Die UI zeigt unreached.entries (die zusammengeführte Liste); failed sind nur die frischen Fehlschläge dieser Runde.
    return {
      unreached: entries.length ? { action, what: whatOf(action), at: nowMs, startedAt, entries } : null,
      reached,
      failed
    };
  }

  // ---- Empfänger -----------------------------------------------------------
  const volWords = (vol) => { const v = Number(vol) || 0; return v === 0 ? 'lautlos' : `leise (Lautstärke ${v} von 10)`; };

  // now bleibt im Aufruf, damit die Zeilenlogik später Zeitabhängiges
  // (z.B. "gleich wieder laut") ergänzen kann, ohne die Aufrufer zu ändern.
  function recipientView(entry, demo, now) {
    const demoOn = !!(demo && demo.enabled);
    const vol = Number(entry.mute_volume) || 0;
    const until = Number(entry.mute_until) || 0;
    const permanent = !!entry.mute_permanent;
    const state = !entry.muted ? 'loud' : (permanent ? 'permanent' : 'muted');
    const isDemoTarget = demoOn && Number(entry.id) === Number(demo.key_id);
    // mute_overdue: Die Rückschaltung ist fällig, konnte aber nicht gespeichert
    // werden - die Box kennt noch "stumm" und alarmiert diesen Empfänger dann
    // evtl. noch mit der Stumm-Lautstärke. state bleibt der wirksame Zustand
    // des NAS ('loud'), die Pille ist aber NIE das grüne "Laut" (grün heißt:
    // alles in Ordnung).
    const overdue = !!entry.mute_overdue;
    let pill = state === 'loud' ? { text: 'Laut', tone: 'ok' }
      : (state === 'permanent' ? { text: 'Dauerhaft', tone: 'crit' } : { text: 'Stumm', tone: 'warn' });
    if (overdue) pill = { text: 'Evtl. stumm', tone: 'warn' };
    let sub = '';
    let subTone = '';
    if (overdue) {
      sub = `Die automatische Laut-Rückschaltung ist fällig, konnte auf dem Server aber nicht gespeichert werden (Datenordner nicht beschreibbar?). „Alarm an alle senden“ vom Server kommt laut an – die Box alarmiert evtl. noch ${volWords(vol)}.`;
      subTone = 'warn';
    } else if (state !== 'loud') {
      const since = entry.muted_at ? ` Stumm seit ${fmtDayClock(entry.muted_at)} Uhr` : '';
      let tail = '';
      if (state === 'permanent') tail = `${since ? ',' : ''} keine automatische Rückschaltung`;
      else if (until) tail = `${since ? ',' : ''} automatisch wieder laut um ~${fmtClock(until)} Uhr`;
      sub = `Alarme kommen ${volWords(vol)} an.${since}${tail}${since || tail ? '.' : ''}`;
    }
    return { state, pill, sub, subTone, overdue, isDemoTarget, paused: demoOn && !isDemoTarget };
  }

  // ---- Bestätigungsfragen --------------------------------------------------
  // Die Frage nennt die TATSÄCHLICHE Stumm-Lautstärke dieses Eintrags:
  // "OHNE Ton" stimmt nur bei 0 - sonst kommt der Alarm leise an.
  function muteQuestion(name, want, vol) {
    const v = Number(vol) || 0;
    const how = v === 0 ? 'OHNE Ton' : `nur noch leise (Stumm-Lautstärke ${v} von 10)`;
    if (want === 'permanent') {
      return `"${name}" DAUERHAFT stumm schalten? Alarme kommen dort weiterhin sichtbar an, aber ${how} – und die automatische Rückschaltung greift nicht mehr, bis das hier wieder beendet wird.`;
    }
    return `"${name}" stumm schalten (Arbeitsmodus)? Alarme kommen dort dann ${how} an (aber weiterhin sichtbar).`;
  }

  // Fester Modus-Wortlaut: LIVE bzw. DEMO-MODUS mit dem einen Empfänger.
  const demoName = (demo) => (demo && demo.label) || 'Test-Empfänger';

  // Empfänger, an die der Versand JETZT geht: im Demo-Modus nur der Demo-Key.
  function targetKeys(demo, keys) {
    const list = Array.isArray(keys) ? keys : [];
    if (demo && demo.enabled) return list.filter((k) => Number(k.id) === Number(demo.key_id));
    return list;
  }

  function mutedPhrase(k) {
    const vol = Number(k.mute_volume) || 0;
    const kind = k.mute_permanent ? 'dauerhaft stumm' : 'stumm';
    return vol === 0 ? `${k.label} (${kind}) lautlos` : `${k.label} (${kind}) mit Lautstärke ${vol}`;
  }

  // keys = keys_list.keys (null/undefined = noch nicht geladen).
  // extra = { names, ids, what } für 'resend'; keysCorrupt: Liste samt
  // Sicherung auf dem NAS unlesbar - dann sendet das NAS an die Not-Liste aus
  // config.php (bark_keys_alarm). Im Demo-Modus nicht: Der Demo-Key steht im
  // Demo-Zustand selbst.
  function alarmQuestion(action, demo, keys, extra) {
    extra = extra || {};
    const demoOn = !!(demo && demo.enabled);
    const corrupt = !!extra.keysCorrupt && !demoOn;
    const loaded = Array.isArray(keys);
    const targets = targetKeys(demo, keys);
    const n = targets.length;
    const mode = demoOn ? `DEMO-MODUS: nur „${demoName(demo)}“` : 'LIVE';
    // Wer wirklich Post bekommt: bei der Nachsendung nur die gewählten ids.
    const receivers = action === 'resend' && Array.isArray(extra.ids)
      ? targets.filter((k) => extra.ids.map(Number).includes(Number(k.id)))
      : targets;
    const muted = receivers.filter((k) => k.muted);
    const mutedList = muted.map(mutedPhrase).join('; ');
    const mutedAlarm = muted.length ? ` Stumm geschaltet (Alarm kommt mit Stumm-Lautstärke): ${mutedList}.` : '';
    const mutedClear = muted.length ? ` Stumm geschaltet (kommt mit Stumm-Lautstärke): ${mutedList}.` : '';
    // Eigener, vollständiger Satz zur Empfängerzahl.
    let sendTo;
    if (corrupt) sendTo = 'an die Not-Liste aus der Server-Konfiguration. Die Empfängerliste auf dem Server ist beschädigt.';
    else if (!loaded) sendTo = 'an alle Empfänger der Liste. Die Empfängerliste ist noch nicht geladen.';
    else sendTo = n === 0 ? 'an die Not-Liste der Server-Konfiguration. Die Empfängerliste ist leer.' : `an ${n} Empfänger.`;

    if (action === 'box_alarm') {
      return {
        title: 'Demo-Alarm über die Box auslösen?',
        text: `DEMO-MODUS: Der Alarm durchläuft die komplette Alarm-Kette der Box und geht an „${demoName(demo)}“. Danach greift der normale 5-Minuten-Cooldown der Box.${mutedAlarm}`
      };
    }
    if (action === 'resend') {
      const names = (extra.names || []).join(', ');
      return {
        title: 'Erneut senden?',
        text: `Aktuell ${mode}: ${extra.what || 'Alarm'} erneut senden – NUR an: ${names}?${mutedAlarm}`
      };
    }
    if (action === 'false_alarm') {
      return {
        title: demoOn ? `Demo-Entwarnung an ${demoName(demo)} senden?` : 'Entwarnung an alle senden?',
        text: demoOn
          ? `${mode}. Die Entwarnung geht nur an diesen Empfänger.${mutedClear}`
          : `${mode}. ENTWARNUNG („Der letzte Alarm war ein FEHLALARM“ – Critical Alert mit kurzem Ton, kommt auch durch Stummschalter durch) ${sendTo}${mutedClear}`
      };
    }
    return {
      title: demoOn ? `Demo-Alarm an ${demoName(demo)} senden?` : 'Alarm an alle senden?',
      text: demoOn
        ? `${mode}. Der Alarm geht nur an diesen Empfänger (1 Empfänger), alle anderen pausieren.${mutedAlarm}`
        : `${mode}. ECHTEN FEUERWEHR-ALARM direkt vom Server senden ${sendTo}${mutedAlarm}`
    };
  }

  // „Automatisch wieder laut nach“ verkürzen: Wer schon länger stumm ist als
  // der neue Wert, wird beim nächsten Abgleich sofort laut geschaltet (und
  // bekommt eine Nachricht) - Regel wie mute_is_stale() in common.php
  // (ohne Zeitpunkt = fällig; dauerhaft Stumme nie). Seconds: 0 = nie.
  // null = nicht kürzer (keine Frage); count null = Liste unbekannt.
  function muteShortenImpact(keys, next, current, now) {
    const n = Number(next) || 0;
    const c = Number(current) || 0;
    if (n <= 0 || (c > 0 && n >= c)) return null;
    if (!Array.isArray(keys)) return { count: null, names: [] };
    const hit = keys.filter((k) => k && k.muted && !k.mute_permanent
      && (!(Number(k.muted_at) > 0) || now - Number(k.muted_at) > n));
    return { count: hit.length, names: hit.map((k) => String(k.label || '?')) };
  }

  function muteShortenQuestion(impact, label) {
    const title = `„Automatisch wieder laut nach“ auf ${label} verkürzen?`;
    if (!impact || impact.count === null) {
      return { title, text: `Wer schon länger stumm ist (mehr als ${label}), wird dann sofort wieder laut – und bekommt eine Nachricht. „Dauerhaft stumm“ bleibt stumm.` };
    }
    const one = impact.count === 1;
    const who = one ? '1 stumm geschalteter Empfänger wird' : `${impact.count} stumm geschaltete Empfänger werden`;
    return { title, text: `${who} sofort wieder laut und ${one ? 'bekommt' : 'bekommen'} eine Nachricht: ${impact.names.join(', ')}. „Dauerhaft stumm“ bleibt stumm.` };
  }

  // Frist: mindestens 10 s, bei langen Fragen mehr (bis 25 s) - sonst lief die
  // Zeit ab, bevor man eine der längeren Fragen zu Ende gelesen hatte.
  const ARM_TIMEOUT_MIN_S = 10;
  const ARM_TIMEOUT_MAX_S = 25;
  const armTimeoutFor = (text) => Math.min(ARM_TIMEOUT_MAX_S, Math.max(ARM_TIMEOUT_MIN_S,
    Math.round(4 + String(text).trim().split(/\s+/).length * 0.4)));

  // Der Modus, den eine Alarm-Bestätigung beschreibt (expect_demo): '0' = LIVE,
  // sonst die key_id des Demo-Empfängers.
  const expectDemoOf = (info) => (info && info.enabled ? String(info.key_id) : '0');

  // ---- „Hinweis im Browser bei Störung“ (Spec §8.6) ------------------------
  // Text der Benachrichtigung je Übergang (Schlüssel aus store.onTransition:
  // Urteil, dabei 'nas' erst ab dem 3. Fehlschlag, 'delivery' = Bereit mit
  // unvollständiger Zustellung). Zurück ins Ruhige („Bereit“, Hinweise,
  // Demo-Modus) gibt es EINMAL „Wieder in Ordnung“ - aber nur, wenn vorher
  // eine Störung gemeldet wurde (alerted). Alles andere (Kontakt klemmt,
  // Abgemeldet, Laden) meldet nichts. null = keine Benachrichtigung.
  const BROWSER_ALERT_TEXTS = {
    offline: 'Box offline – Relaisalarme kommen gerade NICHT an. Manuell alarmieren geht weiterhin über das Dashboard.',
    alarm: 'Alarm läuft – die Box stellt gerade zu.',
    standing: 'Alarm ausgelöst – der Melder-Kontakt ist noch geschlossen.',
    nas: 'Server nicht erreichbar – das Dashboard bekommt seit mehreren Abfragen keine aktuellen Daten. Die Box alarmiert über Bark auch ohne den Server.',
    delivery: 'Zustellung unvollständig – beim letzten Alarm der Box wurden nicht alle Empfänger erreicht.'
  };
  const BROWSER_ALERT_CALM = {
    ok: 'Wieder in Ordnung – die Box meldet sich, keine Störung bekannt.',
    hints: 'Wieder in Ordnung – die Box meldet sich (es liegen noch Hinweise an, siehe Übersicht).',
    demo: 'Wieder in Ordnung – die Box meldet sich (der Demo-Modus ist noch aktiv).'
  };
  function browserAlertFor(to, alerted) {
    if (Object.prototype.hasOwnProperty.call(BROWSER_ALERT_TEXTS, to)) return { body: BROWSER_ALERT_TEXTS[to], problem: true };
    if (alerted && Object.prototype.hasOwnProperty.call(BROWSER_ALERT_CALM, to)) return { body: BROWSER_ALERT_CALM[to], problem: false };
    return null;
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
    boxRows, collectHints, verdict, overviewRows, unsureBand, keysSummary, recipientsFact,
    ALARM_KINDS, deriveAlarmInfo, alarmFact,
    STATE_LABELS, ACK_RESULTS, ACK_MESSAGES, fmtAckResult, fmtAckMessage, COMMAND_LINGER_S, COMMAND_ACK_GIVEUP_S, commandLine,
    describeLogEntry, LOG_FILTERS, LOG_STEP, LOG_MAX, LOG_EMPTY, logMatches,
    failReason, whatOf, UNREACHED_MAX_AGE_MS, validateUnreached, validateSendMarker, sendMarkerNote, buildUnreached,
    volWords, recipientView, muteQuestion, alarmQuestion, muteShortenImpact, muteShortenQuestion, armTimeoutFor, expectDemoOf,
    browserAlertFor
  };
})(typeof window !== 'undefined' ? window : globalThis);
