<?php
require __DIR__ . '/../lib/common.php';

$config = load_config();
require_machine_auth($config);

$status = clipped_post_fields(['token']);
$status['seen_at'] = time();
$status['remote_addr'] = $_SERVER['REMOTE_ADDR'] ?? '';

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
// hält nur den jeweils letzten Push, egal von wem.
$alarmInfo = with_lock($config, 'status', function () use ($config, $status) {
    $path = data_path($config, 'latest_status.json');
    $previous = read_json_file($path, [], $previousStatus);
    if (!write_json_file($path, $status)) {
        return false;   // Schreibfehler -> HTTP 500 (siehe unten)
    }

    $device = substr((string)($status['device_id'] ?? ''), 0, 80);
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

// Fehlgeschlagenes Schreiben ehrlich melden: Der ESP32 wertet nur HTTP 200 als
// Erfolg und versucht es später erneut, statt einen stalen Status zu hinterlassen.
if (!$ok) {
    http_response_code(500);
    text_response("WRITE_FAILED\n");
}
text_response("OK\n");
