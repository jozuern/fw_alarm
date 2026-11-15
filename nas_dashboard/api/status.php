<?php
require __DIR__ . '/../lib/common.php';

$config = load_config();
require_machine_auth($config);

$status = clipped_post_fields(['token']);
$status['seen_at'] = time();
$status['remote_addr'] = $_SERVER['REMOTE_ADDR'] ?? '';
$pushDevice = substr((string)($status['device_id'] ?? ''), 0, 80);

// Ist box_device_id gesetzt und meldet sich ein ANDERES Board mit dem Token
// (Bastel-/Ersatz-ESP32): annehmen (sonst ginge es in den Fehler-Backoff),
// aber nur für den Dashboard-Hinweis merken - es darf weder den Status der
// echten Box überschreiben noch deren Ausfall verdecken, und seine Alarme
// gehören nicht in den Verlauf.
if (!is_expected_box($config, $pushDevice)) {
    with_lock_soft($config, 'status', static function () use ($config, $pushDevice, $status): void {
        remember_device_push($config, $pushDevice, $status);
    });
    text_response("OK\n");
}

// Kam der Alarm, den der ESP32 gerade meldet, vom DEMO-Alarm des Dashboards
// (ALARM-Befehl kurz zuvor ausgeliefert) statt vom Relais? Dann im Verlauf
// nicht als Relaisalarm führen. Im Zweifel "relay": Ein echter Alarm darf
// nie als Demo im Verlauf stehen.
function last_alarm_source(array $config, array $status): string
{
    $sent = (string)($status['alarm_source'] ?? '');
    if ($sent === 'relay' || $sent === 'remote') {
        return $sent;   // neuere Firmware meldet die Quelle selbst
    }
    $current = read_json_file(data_path($config, 'command_state.json'), command_state_default())['current'] ?? null;
    if (!is_array($current) || ($current['type'] ?? '') !== 'ALARM') {
        return 'relay';
    }
    $sinceDelivery = time() - (int)($current['delivered_at'] ?? 0);
    $state = (string)($current['state'] ?? '');
    // Quittiert: nur wenn die Box den Befehl wirklich AUSGEFÜHRT hat. Bei
    // "skipped_busy" o.ä. lief kein Demo-Alarm - ein Alarm kurz danach ist echt.
    if ($state === 'acked') {
        return ((string)($current['ack_result'] ?? '') === 'executed' && $sinceDelivery < 300)
            ? 'remote' : 'relay';
    }
    // Ausgeliefert, Quittung steht noch aus: nur ein sehr kurzes Fenster.
    return ($state === 'delivered' && $sinceDelivery < 60) ? 'remote' : 'relay';
}

// Relaisalarme dauerhaft im Verlauf festhalten: last_alarm ist nur die Sicht
// des ESP32 seit seinem letzten Neustart - ändert sich der Wert, war das ein
// neuer Alarm, und der gehört ins commands.log (übersteht Reboots). Verglichen
// wird mit dem zuletzt gemerkten Stand DIESER Box (alarm_seen.json, pro
// device_id): Das verhindert Doppel-Einträge (derselbe Wert kommt ja mit
// jedem Push erneut) und funktioniert auch, wenn ein zweites Board mit
// demselben Token (Bastel-/Ersatz-ESP32) dazwischen pusht - latest_status.json
// hält nur den jeweils letzten Push, egal von wem (außer box_device_id ist
// gesetzt, siehe oben).
// "Demo oder Relais?" (last_alarm_source) ist eine Heuristik, solange die
// Firmware alarm_source nicht selbst meldet: Die Box pusht ihren Status VOR
// der ACK, deshalb gilt ein ausgelieferter ALARM-Befehl innerhalb von 60 s als
// Demo-Quelle - auch wenn die ACK danach "skipped_busy" sagt. Die Fenster
// (300 s / 60 s) setzen voraus, dass COOLDOWN_MS in config.h nicht kürzer ist.
// Neustart der Box erkannt? Wird im Lock gesetzt und erst NACH der Antwort
// ausgewertet (Verlauf, optionale Admin-Meldung) - der ESP32 wartet nicht.
$restart = null;

$alarmInfo = with_lock($config, 'status', function () use ($config, $status, &$restart) {
    $path = data_path($config, 'latest_status.json');
    $previous = read_json_file($path, [], $previousStatus);
    $device = substr((string)($status['device_id'] ?? ''), 0, 80);
    $sameDevice = $previousStatus === 'ok' && (string)($previous['device_id'] ?? '') === (string)($status['device_id'] ?? '');

    // Veralteter Push, der NACH einem jüngeren ankommt? Das passiert, wenn das
    // NAS hängt (Platte wacht auf): Push A läuft bei der Box in den Timeout,
    // Push B kommt durch, dann wird A doch noch verarbeitet. Erkennbar daran,
    // dass A früher eintraf (seen_at = Ankunft) und eine kleinere Laufzeit hat.
    // Würde er verarbeitet, spränge der Status zurück, ein alter Alarm
    // landete erneut im Verlauf, und ein "Neustart" würde gemeldet. Deshalb
    // nur bestätigen. Fenster 60 s: Eine rückwärts gestellte NAS-Uhr (NTP)
    // kann so höchstens eine Minute lang Pushs verschlucken.
    if ($sameDevice && isset($previous['seen_at'], $previous['uptime_s'], $status['uptime_s'])) {
        $lag = (int)$previous['seen_at'] - (int)$status['seen_at'];
        if ($lag > 0 && $lag < 60 && (int)$status['uptime_s'] < (int)$previous['uptime_s']) {
            return null;
        }
    }

    if (!write_json_file($path, $status)) {
        return false;   // Schreibfehler -> HTTP 500 (siehe unten)
    }
    remember_device_push($config, $device, $status);
    // Hat DIESELBE Box seit ihrem vorigen Push neu gestartet? reset_reason
    // sagt dann, warum. Zwei Anzeichen:
    // - die Laufzeit ist kleiner als beim vorigen Push, oder
    // - der errechnete Startzeitpunkt (Ankunft minus Laufzeit) ist deutlich
    //   nach hinten gesprungen. Das fängt auch eine Absturz-Schleife mit EINEM
    //   Push pro Start, bei der die Laufzeit zufällig nicht sinkt (12 s ->
    //   Neustart -> 13 s). Toleranz 10 s für die Laufzeit der Anfrage plus
    //   0,1 % des Abstands für die Abweichung der Box-Uhr nach langen Pausen.
    //   Der Startzeitpunkt hängt aber an der NAS-Uhr: Springt die vorwärts
    //   (NTP-Korrektur), "verschiebt" er sich ebenfalls. Deshalb zählt dieses
    //   Anzeichen nur, wenn die Box auch erst NACH dem vorigen Push gestartet
    //   sein KANN (Laufzeit nicht länger als der Abstand) - eine seit Wochen
    //   laufende Box meldet sonst nach jeder Uhr-Korrektur einen "Neustart".
    // Beide Anzeichen nur, wenn die Box überhaupt NACH dem vorigen Push
    // gestartet sein KANN - bei einem echten Neustart gilt das immer.
    if ($sameDevice && isset($previous['uptime_s'], $previous['seen_at'], $status['uptime_s'])) {
        $prevUptime = (int)$previous['uptime_s'];
        $newUptime = (int)$status['uptime_s'];
        $gap = max(0, (int)$status['seen_at'] - (int)$previous['seen_at']);
        $bootShift = ((int)$status['seen_at'] - $newUptime) - ((int)$previous['seen_at'] - $prevUptime);
        $couldHaveBooted = $newUptime <= $gap + 10;
        if ($couldHaveBooted && ($newUptime < $prevUptime || $bootShift > 10 + 0.001 * $gap)) {
            $reason = (string)($status['reset_reason'] ?? '');
            $restart = [
                'reason' => $reason,
                'abnormal' => in_array($reason, ['panic', 'task_wdt', 'int_wdt', 'wdt', 'brownout'], true),
            ];
        }
    }
    $new = (string)($status['last_alarm'] ?? '');
    $seq = (string)($status['alarm_seq'] ?? '');   // nur neuere Firmware, sonst ''
    $seenPath = data_path($config, 'alarm_seen.json');
    $seen = read_json_file($seenPath, []);
    $known = is_array($seen[$device] ?? null) ? $seen[$device] : null;
    if ($known === null && $previousStatus === 'ok' && array_key_exists('last_alarm', $previous)
        && (string)($previous['device_id'] ?? '') === $device) {
        // Übergang von der alten Logik (Vergleich mit dem vorigen Push).
        $known = ['last_alarm' => (string)$previous['last_alarm'],
            'alarm_seq' => (string)($previous['alarm_seq'] ?? '')];
    }

    // Ohne gemerkten Stand (erster Push dieser Box, Daten verloren) ist kein
    // Vergleich möglich - sonst würde ein alter Alarm erneut verbucht.
    // Text ODER Zähler: Der Zähler erkennt zwei textgleiche Alarme (ohne
    // NTP-Zeit), der Text einen Alarm nach einem Neustart, bei dem der Zähler
    // zufällig wieder denselben Wert hat.
    $changed = $known !== null
        && ($new !== (string)($known['last_alarm'] ?? '') || $seq !== (string)($known['alarm_seq'] ?? ''));
    if ($known === null || $changed) {
        $seen[$device] = ['last_alarm' => $new, 'alarm_seq' => $seq, 'at' => time()];
        // Höchstens 10 Boxen merken (die zuletzt gesehenen).
        uasort($seen, static fn($a, $b): int => (int)($b['at'] ?? 0) <=> (int)($a['at'] ?? 0));
        write_json_file($seenPath, array_slice($seen, 0, 10, true));
    }
    // 'nie' = noch kein Alarm seit Boot; ein Wechsel ZU 'nie' ist nur ein
    // Neustart des ESP32, kein Alarm.
    return ($changed && $new !== '' && $new !== 'nie') ? $new : null;
});

if (is_string($alarmInfo)) {
    append_command_log($config, [
        'event' => 'relay_alarm',
        'device_id' => (string)($status['device_id'] ?? ''),
        'info' => $alarmInfo,
        'source' => last_alarm_source($config, $status),
    ]);
}
$ok = ($alarmInfo !== false);

// Neustart der Box: Ein Neustart nach Absturz, Watchdog oder Spannungseinbruch
// gehört dauerhaft in den Verlauf (die Statuszeile "Letzter Neustart-Grund"
// zeigt immer nur den jüngsten). Die Admin-Meldung dazu ist optional (Art
// box_restart, Standard: aus) und läuft erst nach der Antwort an den ESP32.
if ($ok && $restart !== null) {
    if ($restart['abnormal']) {
        append_command_log($config, [
            'event' => 'box_restart',
            'device_id' => (string)($status['device_id'] ?? ''),
            'reason' => $restart['reason'],
        ]);
    }
    $restartNotify = load_notify_settings($config)['types']['box_restart'];
    if (!empty($restartNotify['enabled'])
        && ($restart['abnormal'] || (string)$restartNotify['scope'] === 'all')) {
        defer_after_response(static function () use ($config, $restart): void {
            // Bark-Texte ASCII (ae/ue/oe).
            $reasons = [
                'panic' => 'einem ABSTURZ (Panic)',
                'task_wdt' => 'einem WATCHDOG-Reset',
                'int_wdt' => 'einem WATCHDOG-Reset',
                'wdt' => 'einem WATCHDOG-Reset',
                'brownout' => 'einem SPANNUNGSEINBRUCH (Brownout) - Netzteil/Kabel pruefen',
                'poweron' => 'dem Einschalten',
                'software' => 'einem Software-Neustart',
                'external' => 'einem Reset per Taster',
            ];
            $why = $reasons[$restart['reason']] ?? ('unbekanntem Grund (' . $restart['reason'] . ')');
            admin_notify_send($config, 'box_restart', 'Alarm-Box neu gestartet',
                date('[d.m.Y H:i] ') . 'Die Box hat nach ' . $why . ' neu gestartet und meldet sich wieder.');
        });
    }
}

// Fehlgeschlagenes Schreiben ehrlich melden: Der ESP32 wertet nur HTTP 200 als
// Erfolg und versucht es später erneut, statt einen stalen Status zu hinterlassen.
if (!$ok) {
    http_response_code(500);
    text_response("WRITE_FAILED\n");
}
text_response("OK\n");
