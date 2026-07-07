<?php
require __DIR__ . '/../lib/common.php';

$config = load_config();
require_machine_auth($config);

$id = (int)($_POST['id'] ?? 0);
$result = substr((string)($_POST['result'] ?? 'unknown'), 0, 80);
$message = substr((string)($_POST['message'] ?? ''), 0, 200);
$deviceId = substr((string)($_POST['device_id'] ?? ''), 0, 80);

$ok = with_lock($config, 'command', function () use ($config, $id, $result, $message, $deviceId): bool {
    $path = data_path($config, 'command_state.json');
    $state = read_json_file($path, command_state_default());
    $current = $state['current'] ?? null;
    if (is_array($current) && (int)($current['id'] ?? 0) === $id) {
        $current['state'] = 'acked';
        $current['acked_at'] = time();
        $current['ack_result'] = $result;
        $current['ack_message'] = $message;
        $current['acked_by'] = $deviceId;
        $state['current'] = $current;
        if (!write_json_file($path, $state)) {
            return false;  // ESP32 soll die ACK später erneut versuchen
        }
        append_command_log($config, ['event' => 'acked', 'command' => $current]);
    } else {
        append_command_log($config, [
            'event' => 'late_ack',
            'id' => $id,
            'result' => $result,
            'message' => $message,
            'device_id' => $deviceId,
        ]);
    }
    return true;
});

if (!$ok) {
    http_response_code(500);
    text_response("WRITE_FAILED\n");
}
text_response("OK\n");
