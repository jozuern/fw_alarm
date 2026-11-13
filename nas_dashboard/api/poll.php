<?php
require __DIR__ . '/../lib/common.php';

$config = load_config();
require_machine_auth($config);

$deviceId = substr((string)($_POST['device_id'] ?? ''), 0, 80);
$nonce = preg_replace('/[^a-zA-Z0-9_-]/', '', substr((string)($_POST['nonce'] ?? ''), 0, 80));
$lastCommandId = max(0, (int)($_POST['last_command_id'] ?? 0));

// Überfällige Stummschaltungen VOR dem Lesen der Version beenden: Der ESP32
// holt die Liste nur bei geänderter keys_version - ohne diesen Aufruf erreichte
// das 12-h-Sicherheitsnetz den Relaisweg nur, wenn zufällig jemand das
// Dashboard offen hat oder der Cron-Wächter läuft. Der Vorab-Check ist ohne
// Lock billig; die Info-Pushs laufen erst NACH der Antwort. Nicht fatal.
expire_stale_mutes($config, true);

// Version der Empfängerliste immer mitliefern: Der ESP32 vergleicht sie mit
// seinem NVS-Stand und holt sich bei Abweichung die Liste über api/keys.php.
$keysVersion = (int)(load_alarm_keys($config)['version'] ?? 0);

// Darf ein DEMO-ALARM JETZT noch ausgeliefert werden? Beim Anlegen wurde
// geprüft, dass der ESP32 die Demo-Liste hat. Hat sich seitdem der Demo-Stand
// geändert (aus-/umgeschaltet), oder meldet der ESP32 nicht mehr genau einen
// Empfänger, könnte der Test-Alarm an die volle Liste gehen -> nicht ausliefern.
function demo_alarm_still_safe(array $config, array $command): bool
{
    if (!demo_mode_enabled($config)) {
        return false;
    }
    $demoChangedAt = (int)(load_demo_mode($config)['changed_at'] ?? 0);
    if (!isset($command['demo_changed_at']) || (int)$command['demo_changed_at'] !== $demoChangedAt) {
        return false;
    }
    $latest = read_json_file(data_path($config, 'latest_status.json'), []);
    return (int)($latest['keys_count'] ?? 0) === 1;
}

$response = with_lock($config, 'command', function () use ($config, $deviceId, $nonce, $keysVersion, $lastCommandId): string {
    $path = data_path($config, 'command_state.json');
    $state = read_json_file($path, command_state_default());

    // IDs müssen über allem liegen, was der ESP32 in diesem Boot schon
    // gesehen hat - sonst verwirft er neue Befehle still als Duplikat (z.B.
    // nachdem command_state.json verloren ging und next_id wieder bei 1 begann).
    $renumbered = false;
    if ($lastCommandId >= (int)($state['next_id'] ?? 1)) {
        $state['next_id'] = $lastCommandId + 1;
        $renumbered = true;
    }
    $pending = $state['current'] ?? null;
    if (is_array($pending) && ($pending['state'] ?? '') === 'pending'
        && (int)($pending['id'] ?? 0) <= $lastCommandId) {
        $pending['id'] = max(1, (int)$state['next_id']);   // wartenden Befehl neu nummerieren
        $state['next_id'] = $pending['id'] + 1;
        $state['current'] = $pending;
        $renumbered = true;
    }
    if ($renumbered) {
        write_json_file($path, $state);
    }

    // Zu alte pending-Befehle NICHT mehr ausliefern: Ein Befehl an einen
    // stundenlang toten ESP32 wäre bei dessen Rückkehr nur noch verwirrend.
    expire_stale_pending($config, $state, true);

    $current = $state['current'] ?? null;
    if (!is_array($current) || ($current['state'] ?? '') !== 'pending') {
        return "ok=1\nnonce={$nonce}\nid=0\ntype=NONE\nkeys_version={$keysVersion}\n";
    }

    if (($current['type'] ?? '') === 'ALARM' && !demo_alarm_still_safe($config, $current)) {
        $current['state'] = 'cancelled';
        $current['cancelled_at'] = time();
        $current['cancelled_by'] = 'Sicherung';
        $current['cancel_reason'] = 'Demo-Stand seit dem Anlegen geändert - nicht ausgeliefert.';
        $state['current'] = $current;
        if (write_json_file($path, $state)) {
            append_command_log($config, ['event' => 'cancelled', 'command' => $current]);
        }
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
