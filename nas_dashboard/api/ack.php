<?php
require __DIR__ . '/../lib/common.php';

$config = load_config();
require_machine_auth($config);

$id = (int)($_POST['id'] ?? 0);
$result = substr((string)($_POST['result'] ?? 'unknown'), 0, 80);
$message = substr((string)($_POST['message'] ?? ''), 0, 200);
$deviceId = substr((string)($_POST['device_id'] ?? ''), 0, 80);

// Nicht fatal: Lässt sich die Quittung nicht speichern (Datenordner nicht
// beschreibbar), wird sie trotzdem bestätigt und nur protokolliert. Mit
// HTTP 500 wiederholte die Box die ACK endlos - und pollt so lange nicht
// (keine Listen-Updates, kein Demo->LIVE) und pusht im Fehler-Backoff auch
// keinen Status mehr. Eine verlorene ACK zeigt das Dashboard ohnehin
// ehrlich als "ausgeliefert, Bestätigung fehlt".
$ok = with_lock_soft($config, 'command', function () use ($config, $id, $result, $message, $deviceId): bool {
    $path = data_path($config, 'command_state.json');
    $state = read_json_file($path, command_state_default());
    $current = $state['current'] ?? null;
    // Nur ein AUSGELIEFERTER Befehl kann quittiert werden (bzw. eine erneut
    // geschickte ACK für einen schon quittierten). Ein pending/cancelled/
    // expired-Befehl mit zufällig gleicher ID (z.B. nach Verlust von
    // command_state.json) darf nicht fälschlich als "ausgeführt" erscheinen.
    $currentState = is_array($current) ? (string)($current['state'] ?? '') : '';
    // Dieselbe ACK noch einmal (die Antwort kam nach dem fsync zu spät für den
    // 4-s-Timeout der Box): bestätigen, aber weder acked_at überschreiben noch
    // ein zweites "acked" in den Verlauf schreiben.
    if (is_array($current) && (int)($current['id'] ?? 0) === $id && $currentState === 'acked'
        && (string)($current['ack_result'] ?? '') === $result) {
        return true;
    }
    if (is_array($current) && (int)($current['id'] ?? 0) === $id
        && in_array($currentState, ['delivered', 'acked'], true)) {
        $current['state'] = 'acked';
        $current['acked_at'] = time();
        $current['ack_result'] = $result;
        $current['ack_message'] = $message;
        $current['acked_by'] = $deviceId;
        $state['current'] = $current;
        if (!write_json_file($path, $state)) {
            return false;
        }
        append_command_log($config, ['event' => 'acked', 'command' => $current]);
    } else {
        // Typ mitschreiben, soweit noch bekannt: Die verspätete ACK eines
        // Demo-Alarms gehört in den Alarm-Verlauf (log_event_category()).
        $type = (is_array($current) && (int)($current['id'] ?? 0) === $id)
            ? (string)($current['type'] ?? '')
            : (string)($state['recent_types'][(string)$id] ?? '');
        append_command_log($config, [
            'event' => 'late_ack',
            'id' => $id,
            'type' => $type,
            'result' => $result,
            'message' => $message,
            'device_id' => $deviceId,
        ]);
    }
    return true;
}, false);

if (!$ok) {
    error_log('fw_alarm: Quittung für Befehl ' . $id . ' nicht gespeichert (Datenordner nicht beschreibbar?)');
}
text_response("OK\n");
