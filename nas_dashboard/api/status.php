<?php
require __DIR__ . '/../lib/common.php';

$config = load_config();
require_machine_auth($config);

$status = clipped_post_fields(['token']);
$status['seen_at'] = time();
$status['remote_addr'] = $_SERVER['REMOTE_ADDR'] ?? '';

// Relaisalarme dauerhaft im Verlauf festhalten: last_alarm ist nur die Sicht
// des ESP32 seit seinem letzten Neustart - ändert sich der Wert, war das ein
// neuer Alarm, und der gehört ins commands.log (übersteht Reboots). Der
// Vergleich mit dem vorigen Push verhindert Doppel-Einträge, denn derselbe
// Wert kommt ja mit jedem Status-Push erneut.
$alarmInfo = with_lock($config, 'status', function () use ($config, $status) {
    $path = data_path($config, 'latest_status.json');
    $previous = read_json_file($path, []);
    if (!write_json_file($path, $status)) {
        return false;   // Schreibfehler -> HTTP 500 (siehe unten)
    }
    $old = (string)($previous['last_alarm'] ?? '');
    $new = (string)($status['last_alarm'] ?? '');
    // 'nie' = noch kein Alarm seit Boot; ein Wechsel ZU 'nie' ist nur ein
    // Neustart des ESP32, kein Alarm.
    return ($new !== '' && $new !== 'nie' && $new !== $old) ? $new : null;
});

if (is_string($alarmInfo)) {
    append_command_log($config, [
        'event' => 'relay_alarm',
        'device_id' => (string)($status['device_id'] ?? ''),
        'info' => $alarmInfo,
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
