<?php
require __DIR__ . '/../lib/common.php';

$config = load_config();
require_machine_auth($config);

$status = clipped_post_fields(['token']);
$status['seen_at'] = time();
$status['remote_addr'] = $_SERVER['REMOTE_ADDR'] ?? '';

$ok = with_lock($config, 'status', function () use ($config, $status): bool {
    return write_json_file(data_path($config, 'latest_status.json'), $status);
});

// Fehlgeschlagenes Schreiben ehrlich melden: Der ESP32 wertet nur HTTP 200 als
// Erfolg und versucht es später erneut, statt einen stalen Status zu hinterlassen.
if (!$ok) {
    http_response_code(500);
    text_response("WRITE_FAILED\n");
}
text_response("OK\n");
