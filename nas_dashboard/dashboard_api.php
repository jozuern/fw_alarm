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
// ALARM über den ESP32 ist ein reines Demo-Werkzeug: Er durchläuft die komplette
// Alarm-Zustandsmaschine der Box (startAlarm/Nachsende-Puffer) und geht an die
// aktuelle Empfängerliste - im LIVE-Modus wäre das ein echter Alarm an alle,
// dafür gibt es den direkten REAL ALARM. Deshalb serverseitig doppelt gesichert:
// nur im Demo-Modus, und nur wenn der ESP32 die Demo-Liste nachweislich schon
// übernommen hat (Versions-Vergleich) und online ist.
if ($action === 'enqueue') {
    require_csrf();
    require_dashboard_admin();
    $type = strtoupper((string)($_POST['type'] ?? ''));
    if (!in_array($type, ['TEST', 'ALARM'], true)) {
        json_response(['ok' => false, 'message' => 'Ungültiger Befehl.']);
    }
    if ($type === 'ALARM') {
        if (!demo_mode_enabled($config)) {
            json_response(['ok' => false, 'message' => 'ALARM über den ESP32 geht nur im Demo-Modus. Im LIVE-Betrieb den REAL ALARM (direkt vom NAS) benutzen.']);
        }
        $latest = read_json_file(data_path($config, 'latest_status.json'), []);
        $espVersion = (int)($latest['keys_version'] ?? -1);
        $nasVersion = (int)(load_alarm_keys($config)['version'] ?? 0);
        $age = time() - (int)($latest['seen_at'] ?? 0);
        if ($espVersion !== $nasVersion || $age > (int)($config['offline_after_seconds'] ?? 180)) {
            json_response(['ok' => false, 'message' => 'Der ESP32 hat die Demo-Liste noch nicht übernommen oder ist offline - warten, bis das Demo-Banner die Übernahme bestätigt.']);
        }
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
            return ['ok' => false, 'message' => 'Befehl konnte nicht gespeichert werden (data_dir prüfen).'];
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
    // Überfällige Stummschaltungen beim Anzeigen gleich mit beenden - so
    // greift das Sicherheitsnetz auch ohne eingerichteten Cron-Wächter.
    expire_stale_mutes($config);
    // Bestandseinträgen (von vor der Geheim-Link-Funktion) einen Token
    // nachrüsten, damit die "Link kopieren"-Knöpfe für alle funktionieren.
    ensure_mute_tokens($config);
    $state = load_alarm_keys($config);
    $isAdmin = dashboard_role() === 'admin';
    $maxMute = mute_max_seconds($config);
    $entries = [];
    foreach (($state['keys'] ?? []) as $entry) {
        $item = [
            'id' => (int)($entry['id'] ?? 0),
            'label' => (string)($entry['label'] ?? ''),
            'key_masked' => mask_bark_key((string)($entry['key'] ?? '')),
            'muted' => !empty($entry['muted']),
            'muted_at' => (int)($entry['muted_at'] ?? 0),
            'mute_volume' => mute_volume_of($entry),
        ];
        // Wann schaltet das Sicherheitsnetz automatisch wieder laut? So kann
        // das Dashboard "Stumm bis ~HH:MM" zeigen, statt dass die Rückschaltung
        // die Betroffenen später überrascht.
        if ($item['muted'] && $item['muted_at'] > 0 && $maxMute > 0) {
            $item['mute_until'] = $item['muted_at'] + $maxMute;
        }
        // Der Geheim-Link-Token geht NUR an den Admin (er verteilt die
        // Kurzbefehl-Links) - der Lese-Benutzer bekommt ihn nicht zu sehen.
        if ($isAdmin) {
            $item['mute_token'] = (string)($entry['mute_token'] ?? '');
        }
        $entries[] = $item;
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
    // Auf Wunsch ältere Ereignisse nachladen ("Ältere anzeigen" im Dashboard).
    // Gedeckelt, damit niemand das komplette Log in den Browser zieht.
    $limit = max(1, min(100, (int)($_GET['limit'] ?? 25)));
    $out = [];
    foreach (read_command_log_tail($config, $limit) as $entry) {
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
            case 'key_muted':
            case 'key_unmuted':
                $item['by'] = (string)($entry['by'] ?? '');
                $item['label'] = (string)($entry['label'] ?? '');
                $item['key'] = (string)($entry['key'] ?? '');
                $item['source'] = (string)($entry['source'] ?? '');
                break;
            case 'key_mute_volume':
                $item['by'] = (string)($entry['by'] ?? '');
                $item['label'] = (string)($entry['label'] ?? '');
                $item['volume'] = (int)($entry['volume'] ?? 0);
                break;
            case 'nas_alarm':
            case 'false_alarm':
                $item['by'] = (string)($entry['by'] ?? '');
                $item['ok'] = !empty($entry['ok']);
                $item['message'] = (string)($entry['message'] ?? '');
                break;
            case 'relay_alarm':
                $item['info'] = (string)($entry['info'] ?? '');
                break;
            case 'box_offline':
            case 'box_recovered':
                $item['minutes'] = (int)($entry['minutes'] ?? 0);
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
                return ['ok' => false, 'message' => 'Demo-Empfänger nicht gefunden. Zuerst im Panel "Alarm-Empfänger" anlegen, dann Demo-Modus einschalten.'];
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
                . 'WICHTIG: Der ESP32 übernimmt das erst beim nächsten Poll - Banner oben beobachten!';
        } else {
            if (empty($demo['enabled'])) {
                return ['ok' => false, 'message' => 'Demo-Modus ist bereits aus.'];
            }
            $demo = ['enabled' => false, 'changed_at' => time(), 'by' => $user];
            $message = 'LIVE-Modus aktiv: Alarme gehen wieder an ALLE Empfänger. '
                . 'Der ESP32 übernimmt die volle Liste beim nächsten Poll.';
        }

        $state['version'] = (int)($state['version'] ?? 0) + 1;
        if (!backup_then_write($config, 'demo_mode.json', $demo)
            || !backup_then_write($config, 'alarm_keys.json', $state)) {
            return ['ok' => false, 'message' => 'Demo-Modus konnte nicht gespeichert werden (data_dir prüfen).'];
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
        json_response(['ok' => false, 'message' => 'Name fehlt oder ist länger als 40 Zeichen.']);
    }
    if (!valid_bark_key($key)) {
        json_response(['ok' => false, 'message' => 'Ungültiger Bark-Key (erlaubt: 5-64 Zeichen, A-Z a-z 0-9 _ -).']);
    }

    $result = with_lock($config, 'keys', function () use ($config, $label, $key): array {
        $path = data_path($config, 'alarm_keys.json');
        $state = read_json_file($path, alarm_keys_default());
        $keys = is_array($state['keys'] ?? null) ? $state['keys'] : [];
        if (count($keys) >= 10) {
            return ['ok' => false, 'message' => 'Maximal 10 Empfänger möglich.'];
        }
        foreach ($keys as $entry) {
            if (hash_equals((string)($entry['key'] ?? ''), $key)) {
                return ['ok' => false, 'message' => 'Dieser Key ist bereits eingetragen.'];
            }
        }
        $id = max(1, (int)($state['next_id'] ?? 1));
        $keys[] = [
            'id' => $id,
            'label' => $label,
            'key' => $key,
            'added_at' => time(),
            // Geheim-Token für den persönlichen Stummschalt-Link (Kurzbefehl).
            'mute_token' => new_mute_token(),
        ];
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
        return ['ok' => true, 'message' => 'Empfänger hinzugefügt. Der ESP32 übernimmt die Liste beim nächsten Poll.'];
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
            return ['ok' => false, 'message' => 'Dieser Empfänger ist gerade der Demo-Empfänger. Zuerst den Demo-Modus beenden.'];
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
            return ['ok' => false, 'message' => 'Empfänger nicht gefunden.'];
        }
        // Sicherheitsnetz: Die Alarmierung darf nie ohne Empfaenger dastehen.
        if (count($remaining) === 0) {
            return ['ok' => false, 'message' => 'Der letzte Empfänger kann nicht gelöscht werden.'];
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
        return ['ok' => true, 'message' => 'Empfänger gelöscht. Der ESP32 übernimmt die Liste beim nächsten Poll.'];
    });

    json_response($result);
}

// Arbeitsmodus vom Dashboard aus umschalten (zusätzlich zum Kurzbefehl-Link
// api/mute.php): stumm = Critical Alert mit der eingestellten
// Stumm-Lautstärke (Standard 0 = lautlos). Jeder Wechsel erhöht die
// Listen-Version, damit der ESP32 ihn beim nächsten Poll übernimmt.
if ($action === 'keys_mute') {
    require_csrf();
    require_dashboard_admin();
    $id = (int)($_POST['id'] ?? 0);
    $wantMuted = (string)($_POST['muted'] ?? '') === '1';

    // Abgelaufene Stummschaltungen zuerst beenden (nimmt selbst den keys-Lock).
    expire_stale_mutes($config);

    $result = with_lock($config, 'keys', function () use ($config, $id, $wantMuted): array {
        $path = data_path($config, 'alarm_keys.json');
        $state = read_json_file($path, alarm_keys_default());
        $idx = -1;
        foreach (($state['keys'] ?? []) as $i => $entry) {
            if ((int)($entry['id'] ?? 0) === $id) {
                $idx = $i;
                break;
            }
        }
        if ($idx < 0) {
            return ['ok' => false, 'message' => 'Empfänger nicht gefunden.'];
        }
        $entry = $state['keys'][$idx];
        if (!empty($entry['muted']) === $wantMuted) {
            return ['ok' => true, 'changed' => false,
                'message' => $wantMuted ? 'Empfänger ist bereits stumm.' : 'Empfänger ist bereits laut.'];
        }
        $state['keys'][$idx]['muted'] = $wantMuted;
        $state['keys'][$idx]['muted_at'] = $wantMuted ? time() : 0;
        $state['version'] = (int)($state['version'] ?? 0) + 1;
        if (!backup_then_write($config, 'alarm_keys.json', $state)) {
            return ['ok' => false, 'message' => 'Liste konnte nicht gespeichert werden.'];
        }
        return ['ok' => true, 'changed' => true, 'entry' => $state['keys'][$idx], 'version' => (int)$state['version'],
            'message' => $wantMuted
                ? 'Empfänger stummgeschaltet (Arbeitsmodus): Alarme kommen dort nur mit der eingestellten Stumm-Lautstärke an. Der ESP32 übernimmt das beim nächsten Poll.'
                : 'Empfänger wieder laut geschaltet. Der ESP32 übernimmt das beim nächsten Poll.'];
    });

    // Log erst NACH dem Lock. Bewusst KEIN Bestätigungs-Push mehr an den
    // Betroffenen (Nutzer-Entscheidung: störte nur) - sichtbar bleibt der
    // Wechsel am Badge in der Liste und im Verlauf.
    if (!empty($result['changed'])) {
        $entry = $result['entry'];
        append_command_log($config, [
            'event' => $wantMuted ? 'key_muted' : 'key_unmuted',
            'by' => (string)($_SESSION['user'] ?? 'dashboard'),
            'source' => 'dashboard',
            'label' => (string)($entry['label'] ?? ''),
            'key' => mask_bark_key((string)($entry['key'] ?? '')),
            'version' => (int)($result['version'] ?? 0),
        ]);
        unset($result['entry']);   // Klartext-Key nicht an den Browser geben
    }

    json_response($result);
}

// Stumm-Lautstärke pro Empfänger (Arbeitsmodus): 0 = komplett lautlos
// (Standard), 1-10 = der Critical Alert kommt leise mit diesem Pegel an.
// Gilt für BEIDE Alarmwege; jeder Wechsel erhöht die Listen-Version, damit
// der ESP32 den Wert beim nächsten Poll übernimmt.
if ($action === 'keys_set_mute_volume') {
    require_csrf();
    require_dashboard_admin();
    $id = (int)($_POST['id'] ?? 0);
    $volume = (int)($_POST['volume'] ?? -1);
    if ($volume < 0 || $volume > 10) {
        json_response(['ok' => false, 'message' => 'Ungültige Lautstärke (erlaubt: 0-10).']);
    }

    $result = with_lock($config, 'keys', function () use ($config, $id, $volume): array {
        $path = data_path($config, 'alarm_keys.json');
        $state = read_json_file($path, alarm_keys_default());
        $idx = -1;
        foreach (($state['keys'] ?? []) as $i => $entry) {
            if ((int)($entry['id'] ?? 0) === $id) {
                $idx = $i;
                break;
            }
        }
        if ($idx < 0) {
            return ['ok' => false, 'message' => 'Empfänger nicht gefunden.'];
        }
        if (mute_volume_of($state['keys'][$idx]) === $volume) {
            return ['ok' => true, 'changed' => false, 'message' => 'Stumm-Lautstärke ist bereits so eingestellt.'];
        }
        $state['keys'][$idx]['mute_volume'] = $volume;
        $state['version'] = (int)($state['version'] ?? 0) + 1;
        if (!backup_then_write($config, 'alarm_keys.json', $state)) {
            return ['ok' => false, 'message' => 'Liste konnte nicht gespeichert werden.'];
        }
        return ['ok' => true, 'changed' => true, 'entry' => $state['keys'][$idx], 'version' => (int)$state['version'],
            'message' => ($volume === 0
                ? 'Gespeichert: Stumm = komplett lautlos.'
                : 'Gespeichert: Stumm = leise mit Lautstärke ' . $volume . '.')
                . ' Der ESP32 übernimmt das beim nächsten Poll.'];
    });

    // Log erst NACH dem Lock; kein Push an den Betroffenen.
    if (!empty($result['changed'])) {
        $entry = $result['entry'];
        append_command_log($config, [
            'event' => 'key_mute_volume',
            'by' => (string)($_SESSION['user'] ?? 'dashboard'),
            'label' => (string)($entry['label'] ?? ''),
            'key' => mask_bark_key((string)($entry['key'] ?? '')),
            'volume' => $volume,
            'version' => (int)($result['version'] ?? 0),
        ]);
        unset($result['entry']);   // Klartext-Key nicht an den Browser geben
    }

    json_response($result);
}

// ENTWARNUNG (Fehlalarm): normale Push-Mitteilung an alle Empfänger, dass der
// letzte Alarm ein Fehlalarm war - bewusst KEIN Critical Alert. Läuft wie der
// REAL ALARM direkt vom NAS (funktioniert also auch bei toter ESP32-Kette).
if ($action === 'false_alarm') {
    require_csrf();
    require_dashboard_admin();
    $result = bark_send_false_alarm_all($config);
    append_command_log($config, [
        'event' => 'false_alarm',
        'by' => (string)($_SESSION['user'] ?? 'dashboard'),
        'ok' => $result['ok'],
        'message' => $result['message'],
    ]);
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
