<?php
require __DIR__ . '/lib/common.php';

$config = load_config();
start_dashboard_session();
require_dashboard_auth();

$action = (string)($_GET['action'] ?? $_POST['action'] ?? 'status');

if ($action === 'status') {
    $latest = read_json_file(data_path($config, 'latest_status.json'), []);
    $seenAt = (int)($latest['seen_at'] ?? 0);
    $age = $seenAt > 0 ? time() - $seenAt : null;
    $offlineAfter = (int)($config['offline_after_seconds'] ?? 180);

    $commandState = with_lock($config, 'command', function () use ($config): array {
        $path = data_path($config, 'command_state.json');
        $state = read_json_file($path, command_state_default());
        // Auch ohne pollenden ESP32 soll ein liegengebliebener Befehl im
        // Dashboard als "expired" erscheinen (und die Buttons freigeben).
        expire_stale_pending($config, $state, true);
        return $state;
    });

    json_response([
        'ok' => true,
        'status' => $latest,
        'seen_age_seconds' => $age,
        'offline' => $age === null || $age > $offlineAfter,
        'offline_after_seconds' => $offlineAfter,
        'command' => $commandState['current'] ?? null,
        // Demo-Modus + NAS-Listenversion: Damit kann das Dashboard anzeigen,
        // ob der ESP32 die aktuelle Liste schon übernommen hat (Vergleich mit
        // dem keys_version-Feld aus dem ESP32-Status).
        'demo' => demo_mode_info($config),
        'nas_keys_version' => (int)(load_alarm_keys($config)['version'] ?? 0),
        // Selbstüberwachung des Offline-Wächters: Das Dashboard warnt, wenn
        // die DSM-Aufgabe nie oder zu lange nicht gelaufen ist.
        'watchdog' => [
            'configured' => trim((string)($config['bark_key_status'] ?? '')) !== '',
            'last_run' => (int)(read_json_file(data_path($config, 'watchdog_state.json'), [])['last_run_at'] ?? 0),
        ],
        'server_time' => time(),
    ]);
}

// TEST läuft über den ESP32 (Poll + ACK): Er testet damit die halbe Kette
// (WLAN, NAS-Anbindung, Bark-Versand vom ESP32) und meldet das Ergebnis zurück.
if ($action === 'enqueue') {
    require_csrf();
    require_dashboard_admin();
    $type = strtoupper((string)($_POST['type'] ?? ''));
    if (!in_array($type, ['TEST'], true)) {
        json_response(['ok' => false, 'message' => 'Ungueltiger Befehl.']);
    }

    $result = with_lock($config, 'command', function () use ($config, $type): array {
        $path = data_path($config, 'command_state.json');
        $state = read_json_file($path, command_state_default());
        expire_stale_pending($config, $state, true);
        $current = $state['current'] ?? null;
        if (is_array($current)) {
            $stateName = (string)($current['state'] ?? '');
            if ($stateName === 'pending') {
                return ['ok' => false, 'message' => 'Es wartet bereits ein Befehl (abbrechen oder warten).'];
            }
            if ($stateName === 'delivered') {
                $deliveredAt = (int)($current['delivered_at'] ?? 0);
                $lockSeconds = (int)($config['delivered_command_lock_seconds'] ?? 120);
                if ($deliveredAt > 0 && time() - $deliveredAt < $lockSeconds) {
                    return ['ok' => false, 'message' => 'Letzter Befehl wurde ausgeliefert, ACK steht noch aus.'];
                }
            }
        }

        $id = max(1, (int)($state['next_id'] ?? 1));
        $command = [
            'id' => $id,
            'type' => $type,
            'state' => 'pending',
            'created_at' => time(),
            'created_by' => (string)($_SESSION['user'] ?? 'dashboard'),
        ];
        $state['next_id'] = $id + 1;
        $state['current'] = $command;
        if (!write_json_file($path, $state)) {
            return ['ok' => false, 'message' => 'Befehl konnte nicht gespeichert werden (data_dir pruefen).'];
        }
        append_command_log($config, ['event' => 'created', 'command' => $command]);
        return ['ok' => true, 'message' => 'Befehl angelegt.', 'command' => $command];
    });

    json_response($result);
}

// Einen noch nicht abgeholten Befehl zurücknehmen (z.B. ESP32 offline).
if ($action === 'cancel') {
    require_csrf();
    require_dashboard_admin();
    $result = with_lock($config, 'command', function () use ($config): array {
        $path = data_path($config, 'command_state.json');
        $state = read_json_file($path, command_state_default());
        $current = $state['current'] ?? null;
        if (!is_array($current) || ($current['state'] ?? '') !== 'pending') {
            return ['ok' => false, 'message' => 'Kein wartender Befehl zum Abbrechen.'];
        }
        $current['state'] = 'cancelled';
        $current['cancelled_at'] = time();
        $current['cancelled_by'] = (string)($_SESSION['user'] ?? 'dashboard');
        $state['current'] = $current;
        if (!write_json_file($path, $state)) {
            return ['ok' => false, 'message' => 'Abbruch konnte nicht gespeichert werden.'];
        }
        append_command_log($config, ['event' => 'cancelled', 'command' => $current]);
        return ['ok' => true, 'message' => 'Befehl abgebrochen.', 'command' => $current];
    });

    json_response($result);
}

// ==== Empfängerliste (Bark-Keys) verwalten ==================================
// Der ESP32 übernimmt Änderungen automatisch: poll.php meldet die Version,
// bei Abweichung holt er die Liste über api/keys.php und speichert sie im NVS.

if ($action === 'keys_list') {
    $state = load_alarm_keys($config);
    $entries = [];
    foreach (($state['keys'] ?? []) as $entry) {
        $entries[] = [
            'id' => (int)($entry['id'] ?? 0),
            'label' => (string)($entry['label'] ?? ''),
            'key_masked' => mask_bark_key((string)($entry['key'] ?? '')),
        ];
    }
    json_response([
        'ok' => true,
        'version' => (int)($state['version'] ?? 0),
        'keys' => $entries,
        'demo' => demo_mode_info($config),
    ]);
}

// ==== Verlauf (letzte Ereignisse aus commands.log) ==========================
// Nur lesend, deshalb auch für den Lese-Benutzer erlaubt. Es gehen NUR die
// hier explizit whitelisteten Felder an den Browser - egal was künftig
// zusätzlich ins Log geschrieben wird. Bark-Keys stehen im Log ohnehin nur
// maskiert (mask_bark_key beim Schreiben).
if ($action === 'log_view') {
    $out = [];
    foreach (read_command_log_tail($config, 25) as $entry) {
        $event = (string)($entry['event'] ?? '');
        $item = ['at' => (int)($entry['log_at'] ?? 0), 'event' => $event];
        $cmd = is_array($entry['command'] ?? null) ? $entry['command'] : [];
        switch ($event) {
            case 'created':
            case 'delivered':
            case 'acked':
            case 'expired':
            case 'cancelled':
                $item['id'] = (int)($cmd['id'] ?? 0);
                $item['type'] = (string)($cmd['type'] ?? '');
                $item['by'] = (string)($cmd[$event === 'cancelled' ? 'cancelled_by' : 'created_by'] ?? '');
                if ($event === 'acked') {
                    $item['result'] = (string)($cmd['ack_result'] ?? '');
                    $item['message'] = (string)($cmd['ack_message'] ?? '');
                }
                break;
            case 'late_ack':
                $item['id'] = (int)($entry['id'] ?? 0);
                $item['result'] = (string)($entry['result'] ?? '');
                break;
            case 'demo_enabled':
            case 'demo_disabled':
                $item['by'] = (string)($entry['by'] ?? '');
                $item['label'] = (string)($entry['label'] ?? '');
                break;
            case 'key_added':
            case 'key_removed':
                $item['by'] = (string)($entry['by'] ?? '');
                $item['label'] = (string)($entry['label'] ?? '');
                $item['key'] = (string)($entry['key'] ?? '');
                break;
            case 'nas_alarm':
                $item['by'] = (string)($entry['by'] ?? '');
                $item['ok'] = !empty($entry['ok']);
                $item['message'] = (string)($entry['message'] ?? '');
                break;
            default:
                continue 2;   // unbekannte Events gar nicht erst ausliefern
        }
        $out[] = $item;
    }
    // server_time für relative Zeitangaben ("vor 5 min") - so spielt eine
    // falsch gehende Browser-Uhr keine Rolle.
    json_response(['ok' => true, 'entries' => $out, 'server_time' => time()]);
}

// ==== Demo-Modus ein-/ausschalten ===========================================
// Jeder Wechsel erhöht die Listen-Version, damit der ESP32 beim nächsten Poll
// die passende Liste (nur Demo-Key bzw. wieder alle) holt. Läuft unter dem
// 'keys'-Lock, weil Liste und Demo-Zustand zusammen konsistent bleiben müssen.
if ($action === 'demo_set') {
    require_csrf();
    require_dashboard_admin();
    $enable = (string)($_POST['enabled'] ?? '') === '1';
    $id = (int)($_POST['id'] ?? 0);

    $result = with_lock($config, 'keys', function () use ($config, $enable, $id): array {
        $keysPath = data_path($config, 'alarm_keys.json');
        $state = read_json_file($keysPath, alarm_keys_default());
        $demoPath = data_path($config, 'demo_mode.json');
        $demo = read_json_file($demoPath, demo_mode_default());
        $user = (string)($_SESSION['user'] ?? 'dashboard');

        if ($enable) {
            // Demo-Empfänger muss aus der gepflegten Liste kommen: So gibt es
            // beim Zurückschalten garantiert wieder eine nicht-leere Liste.
            $target = null;
            foreach (($state['keys'] ?? []) as $entry) {
                if ((int)($entry['id'] ?? 0) === $id) {
                    $target = $entry;
                    break;
                }
            }
            if ($target === null) {
                return ['ok' => false, 'message' => 'Demo-Empfaenger nicht gefunden. Zuerst im Panel "Alarm-Empfaenger" anlegen, dann Demo-Modus einschalten.'];
            }
            $demo = [
                'enabled' => true,
                'key' => (string)($target['key'] ?? ''),
                'key_id' => (int)($target['id'] ?? 0),
                'label' => (string)($target['label'] ?? ''),
                'changed_at' => time(),
                'by' => $user,
            ];
            $message = 'DEMO-MODUS aktiviert: Alarme gehen nur noch an "' . (string)($target['label'] ?? '') . '". '
                . 'WICHTIG: Der ESP32 uebernimmt das erst beim naechsten Poll - Banner oben beobachten!';
        } else {
            if (empty($demo['enabled'])) {
                return ['ok' => false, 'message' => 'Demo-Modus ist bereits aus.'];
            }
            $demo = ['enabled' => false, 'changed_at' => time(), 'by' => $user];
            $message = 'LIVE-Modus aktiv: Alarme gehen wieder an ALLE Empfaenger. '
                . 'Der ESP32 uebernimmt die volle Liste beim naechsten Poll.';
        }

        $state['version'] = (int)($state['version'] ?? 0) + 1;
        if (!backup_then_write($config, 'demo_mode.json', $demo)
            || !backup_then_write($config, 'alarm_keys.json', $state)) {
            return ['ok' => false, 'message' => 'Demo-Modus konnte nicht gespeichert werden (data_dir pruefen).'];
        }
        append_command_log($config, [
            'event' => $enable ? 'demo_enabled' : 'demo_disabled',
            'by' => $user,
            'label' => (string)($demo['label'] ?? ''),
            'version' => $state['version'],
        ]);
        return ['ok' => true, 'message' => $message, 'demo' => demo_mode_info($config)];
    });

    json_response($result);
}

if ($action === 'keys_add') {
    require_csrf();
    require_dashboard_admin();
    $label = trim((string)($_POST['label'] ?? ''));
    $key = trim((string)($_POST['key'] ?? ''));
    if ($label === '' || mb_strlen($label) > 40) {
        json_response(['ok' => false, 'message' => 'Name fehlt oder ist laenger als 40 Zeichen.']);
    }
    if (!valid_bark_key($key)) {
        json_response(['ok' => false, 'message' => 'Ungueltiger Bark-Key (erlaubt: 5-64 Zeichen, A-Z a-z 0-9 _ -).']);
    }

    $result = with_lock($config, 'keys', function () use ($config, $label, $key): array {
        $path = data_path($config, 'alarm_keys.json');
        $state = read_json_file($path, alarm_keys_default());
        $keys = is_array($state['keys'] ?? null) ? $state['keys'] : [];
        if (count($keys) >= 10) {
            return ['ok' => false, 'message' => 'Maximal 10 Empfaenger moeglich.'];
        }
        foreach ($keys as $entry) {
            if (hash_equals((string)($entry['key'] ?? ''), $key)) {
                return ['ok' => false, 'message' => 'Dieser Key ist bereits eingetragen.'];
            }
        }
        $id = max(1, (int)($state['next_id'] ?? 1));
        $keys[] = ['id' => $id, 'label' => $label, 'key' => $key, 'added_at' => time()];
        $state['keys'] = $keys;
        $state['next_id'] = $id + 1;
        $state['version'] = (int)($state['version'] ?? 0) + 1;
        if (!backup_then_write($config, 'alarm_keys.json', $state)) {
            return ['ok' => false, 'message' => 'Liste konnte nicht gespeichert werden.'];
        }
        append_command_log($config, [
            'event' => 'key_added',
            'by' => (string)($_SESSION['user'] ?? 'dashboard'),
            'label' => $label,
            'key' => mask_bark_key($key),
            'version' => $state['version'],
        ]);
        return ['ok' => true, 'message' => 'Empfaenger hinzugefuegt. Der ESP32 uebernimmt die Liste beim naechsten Poll.'];
    });

    json_response($result);
}

if ($action === 'keys_delete') {
    require_csrf();
    require_dashboard_admin();
    $id = (int)($_POST['id'] ?? 0);

    $result = with_lock($config, 'keys', function () use ($config, $id): array {
        // Der aktive Demo-Empfänger ist gerade der EINZIGE Alarmweg - er darf
        // erst gelöscht werden, wenn der Demo-Modus wieder aus ist.
        $demo = load_demo_mode($config);
        if (!empty($demo['enabled']) && (int)($demo['key_id'] ?? 0) === $id) {
            return ['ok' => false, 'message' => 'Dieser Empfaenger ist gerade der Demo-Empfaenger. Zuerst den Demo-Modus beenden.'];
        }
        $path = data_path($config, 'alarm_keys.json');
        $state = read_json_file($path, alarm_keys_default());
        $keys = is_array($state['keys'] ?? null) ? $state['keys'] : [];
        $remaining = [];
        $removed = null;
        foreach ($keys as $entry) {
            if ((int)($entry['id'] ?? 0) === $id && $removed === null) {
                $removed = $entry;
            } else {
                $remaining[] = $entry;
            }
        }
        if ($removed === null) {
            return ['ok' => false, 'message' => 'Empfaenger nicht gefunden.'];
        }
        // Sicherheitsnetz: Die Alarmierung darf nie ohne Empfaenger dastehen.
        if (count($remaining) === 0) {
            return ['ok' => false, 'message' => 'Der letzte Empfaenger kann nicht geloescht werden.'];
        }
        $state['keys'] = $remaining;
        $state['version'] = (int)($state['version'] ?? 0) + 1;
        if (!backup_then_write($config, 'alarm_keys.json', $state)) {
            return ['ok' => false, 'message' => 'Liste konnte nicht gespeichert werden.'];
        }
        append_command_log($config, [
            'event' => 'key_removed',
            'by' => (string)($_SESSION['user'] ?? 'dashboard'),
            'label' => (string)($removed['label'] ?? ''),
            'key' => mask_bark_key((string)($removed['key'] ?? '')),
            'version' => $state['version'],
        ]);
        return ['ok' => true, 'message' => 'Empfaenger geloescht. Der ESP32 uebernimmt die Liste beim naechsten Poll.'];
    });

    json_response($result);
}

// REAL ALARM: bewusst DIREKT vom NAS an Bark, ohne Umweg über den ESP32.
// Eine manuelle Alarmierung braucht man vor allem dann, wenn die ESP32-Kette
// gerade NICHT funktioniert - dann darf sie nicht am Poll des ESP32 hängen.
if ($action === 'alarm') {
    require_csrf();
    require_dashboard_admin();
    $result = bark_send_alarm_all($config);
    append_command_log($config, [
        'event' => 'nas_alarm',
        'by' => (string)($_SESSION['user'] ?? 'dashboard'),
        'ok' => $result['ok'],
        'message' => $result['message'],
        'results' => $result['results'],
    ]);
    json_response($result);
}

json_response(['ok' => false]);
