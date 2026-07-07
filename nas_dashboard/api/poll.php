<?php
require __DIR__ . '/../lib/common.php';

$config = load_config();
require_machine_auth($config);

$deviceId = substr((string)($_POST['device_id'] ?? ''), 0, 80);
$nonce = preg_replace('/[^a-zA-Z0-9_-]/', '', substr((string)($_POST['nonce'] ?? ''), 0, 80));

// Version der Empfängerliste immer mitliefern: Der ESP32 vergleicht sie mit
// seinem NVS-Stand und holt sich bei Abweichung die Liste über api/keys.php.
$keysVersion = (int)(load_alarm_keys($config)['version'] ?? 0);

$response = with_lock($config, 'command', function () use ($config, $deviceId, $nonce, $keysVersion): string {
    $path = data_path($config, 'command_state.json');
    $state = read_json_file($path, command_state_default());

    // Zu alte pending-Befehle NICHT mehr ausliefern: Ein Befehl an einen
    // stundenlang toten ESP32 wäre bei dessen Rückkehr nur noch verwirrend.
    expire_stale_pending($config, $state, true);

    $current = $state['current'] ?? null;
    if (!is_array($current) || ($current['state'] ?? '') !== 'pending') {
        return "ok=1\nnonce={$nonce}\nid=0\ntype=NONE\nkeys_version={$keysVersion}\n";
    }

    $current['state'] = 'delivered';
    $current['delivered_at'] = time();
    $current['delivered_to'] = $deviceId;
    $state['current'] = $current;
    if (!write_json_file($path, $state)) {
        // Zustand nicht persistierbar -> Befehl NICHT ausliefern, sonst wäre
        // die at-most-once-Garantie weg (Replay nach NAS-Problem).
        http_response_code(500);
        return "ok=0\n";
    }
    append_command_log($config, ['event' => 'delivered', 'command' => $current]);

    return "ok=1\nnonce={$nonce}\nid=" . (int)$current['id'] . "\ntype=" . $current['type']
         . "\nkeys_version={$keysVersion}\n";
});

text_response($response);
