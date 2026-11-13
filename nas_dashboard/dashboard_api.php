<?php
// JSON-Aufrufer: Lock-/Speicherfehler kommen als JSON statt als Text (siehe
// with_lock()) - sonst zeigte der Browser nur "NAS nicht erreichbar".
const FW_ALARM_JSON_API = true;
require __DIR__ . '/lib/common.php';

$config = load_config();
start_dashboard_session();
require_dashboard_auth();
// Sitzung sofort wieder freigeben: PHP-Datei-Sessions bleiben gesperrt,
// solange ein Skript läuft. Ein REAL ALARM (bis ~1 min) hätte sonst ALLE
// anderen Anfragen dieses Browsers blockiert - die Statusanzeige fror ein,
// ENTWARNUNG und Abbrechen warteten. $_SESSION bleibt danach lesbar (CSRF,
// Rolle, Benutzer); last_seen hat require_dashboard_auth() schon gesetzt.
session_write_close();

$method = (string)($_SERVER['REQUEST_METHOD'] ?? 'GET');
$action = $method === 'POST'
    ? (string)($_POST['action'] ?? '')
    : (string)($_GET['action'] ?? 'status');
$user = (string)($_SESSION['user'] ?? 'dashboard');

// Nur Lese-Aktionen gehen per GET; alles, was etwas ändert oder auslöst, nur
// per POST (zusätzlich zu CSRF und Admin-Rolle).
if (!in_array($action, ['status', 'keys_list', 'log_view'], true) && $method !== 'POST') {
    http_response_code(405);
    json_response(['ok' => false, 'message' => 'Diese Aktion ist nur per POST erlaubt.']);
}

// Die Person hat den Modus bestätigt, der beim Scharfschalten des Knopfs
// angezeigt wurde (expect_demo: '0' = LIVE, sonst key_id des
// Demo-Empfängers). Hat er sich inzwischen geändert - etwa aus einer zweiten
// Sitzung -, wird NICHTS gesendet: Ein als "Demo" bestätigter Alarm darf nie
// an alle gehen und umgekehrt. Ohne Feld (ältere Oberfläche) wie bisher.
function require_expected_mode(array $config): void
{
    if (!isset($_POST['expect_demo'])) {
        return;
    }
    // EINE Quelle für beide Seiten: Der Browser bildet seine Erwartung aus
    // genau dieser Info (enabled + key_id).
    $info = demo_mode_info($config);
    $actual = $info['enabled'] ? (string)$info['key_id'] : '0';
    if ((string)$_POST['expect_demo'] !== $actual) {
        http_response_code(409);
        json_response([
            'ok' => false,
            'code' => 'mode_changed',
            'message' => 'Der Modus wurde inzwischen geändert ('
                . ($actual === '0'
                    ? 'jetzt LIVE – Alarm ginge an ALLE'
                    : 'jetzt DEMO – nur an „' . $info['label'] . '“')
                . '). Es wurde NICHTS gesendet – bitte neu bestätigen.',
            'demo' => $info,
        ]);
    }
}

if ($action === 'status') {
    $latest = read_json_file(data_path($config, 'latest_status.json'), []);
    $seenAt = (int)($latest['seen_at'] ?? 0);
    $age = $seenAt > 0 ? time() - $seenAt : null;
    $offlineAfter = (int)($config['offline_after_seconds'] ?? 180);
    // Läuft laut letztem Push gerade eine Alarm-Zustellung, blockiert die Box
    // zwischen ihren (seriellen) Bark-Versuchen deutlich länger als sonst -
    // dann nicht vorschnell rot "offline" zeigen.
    if ((string)($latest['alarm_pending'] ?? '0') === '1') {
        $offlineAfter = max($offlineAfter, 600);
    }

    // Befehlszustand ohne Lock lesen (der 5-s-Poll braucht so kein
    // Schreibrecht). Nur wenn ein liegengebliebener Befehl gerade verfällt,
    // wird er unter Lock als "expired" gespeichert - auch ohne pollenden ESP32
    // soll er im Dashboard so erscheinen (und die Buttons freigeben).
    $commandState = read_json_file(data_path($config, 'command_state.json'), command_state_default());
    $probe = $commandState;
    if (expire_stale_pending($config, $probe, false)) {
        $commandState = with_lock_soft($config, 'command', function () use ($config): array {
            $path = data_path($config, 'command_state.json');
            $state = read_json_file($path, command_state_default());
            expire_stale_pending($config, $state, true);
            return $state;
        }, $probe);
    }

    $watchdogState = read_json_file(data_path($config, 'watchdog_state.json'), []);
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
        // die DSM-Aufgabe nie oder zu lange nicht gelaufen ist - und wenn sie
        // zwar läuft, ihre Meldungen aber nicht zustellen kann.
        'watchdog' => [
            'configured' => trim((string)($config['bark_key_status'] ?? '')) !== '',
            'last_run' => (int)($watchdogState['last_run_at'] ?? 0),
            'last_send_ok_at' => (int)($watchdogState['last_send_ok_at'] ?? 0),
            'last_send_failed_at' => (int)($watchdogState['last_send_failed_at'] ?? 0),
        ],
        'server_time' => time(),
    ]);
}

// TEST läuft über den ESP32 (Poll + ACK): Er testet damit die halbe Kette
// (WLAN, NAS-Anbindung, Bark-Versand vom ESP32) und meldet das Ergebnis zurück.
// ALARM über den ESP32 ist ein reines Demo-Werkzeug: Er durchläuft die komplette
// Alarm-Zustandsmaschine der Box (startAlarm/Nachsende-Puffer) und geht an die
// aktuelle Empfängerliste - im LIVE-Modus wäre das ein echter Alarm an alle,
// dafür gibt es den direkten REAL ALARM. Deshalb serverseitig gesichert: nur im
// Demo-Modus, nur wenn der ESP32 die Demo-Liste nachweislich schon übernommen
// hat und online ist - geprüft beim Anlegen UND (api/poll.php) beim Ausliefern.
if ($action === 'enqueue') {
    require_csrf();
    require_dashboard_admin();
    $type = strtoupper((string)($_POST['type'] ?? ''));
    if (!in_array($type, ['TEST', 'ALARM'], true)) {
        json_response(['ok' => false, 'message' => 'Ungültiger Befehl.']);
    }
    if ($type === 'ALARM') {
        require_expected_mode($config);
        $reason = demo_alarm_block_reason($config);
        if ($reason !== null) {
            json_response(['ok' => false, 'message' => $reason]);
        }
    }

    $result = with_lock($config, 'command', function () use ($config, $type, $user): array {
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
            'created_by' => $user,
        ];
        if ($type === 'ALARM') {
            // Guard unter dem Lock wiederholen (zwischen erster Prüfung und
            // hier könnte der Demo-Modus gewechselt haben) und den geprüften
            // Demo-Stand festhalten: poll.php liefert den Befehl nur aus, wenn
            // sich daran bis dahin nichts geändert hat.
            $reason = demo_alarm_block_reason($config);
            if ($reason !== null) {
                return ['ok' => false, 'message' => $reason];
            }
            $command['demo_changed_at'] = (int)(load_demo_mode($config)['changed_at'] ?? 0);
            $command['keys_version'] = (int)(load_alarm_keys($config)['version'] ?? 0);
        }
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
    $result = with_lock($config, 'command', function () use ($config, $user): array {
        $path = data_path($config, 'command_state.json');
        $state = read_json_file($path, command_state_default());
        $current = $state['current'] ?? null;
        if (!is_array($current) || ($current['state'] ?? '') !== 'pending') {
            return ['ok' => false, 'message' => 'Kein wartender Befehl zum Abbrechen.'];
        }
        $current['state'] = 'cancelled';
        $current['cancelled_at'] = time();
        $current['cancelled_by'] = $user;
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
    // Nicht fatal: Die Liste soll auch ohne Schreibrecht sichtbar bleiben.
    expire_stale_mutes($config, true);
    // Bestandseinträgen (von vor der Geheim-Link-Funktion) einen Token
    // nachrüsten, damit die "Link kopieren"-Knöpfe für alle funktionieren.
    ensure_mute_tokens($config);
    $state = load_alarm_keys($config);
    $isAdmin = dashboard_role() === 'admin';
    $maxMute = mute_max_seconds($config);
    $entries = [];
    foreach (($state['keys'] ?? []) as $entry) {
        if (!is_array($entry)) {
            continue;
        }
        $item = [
            'id' => (int)($entry['id'] ?? 0),
            'label' => (string)($entry['label'] ?? ''),
            'key_masked' => mask_bark_key((string)($entry['key'] ?? '')),
            'muted' => !empty($entry['muted']),
            'muted_at' => (int)($entry['muted_at'] ?? 0),
            'mute_volume' => mute_volume_of($entry),
            'mute_permanent' => mute_is_permanent($entry),
        ];
        // Wann schaltet das Sicherheitsnetz automatisch wieder laut? So kann
        // das Dashboard "Stumm bis ~HH:MM" zeigen, statt dass die Rückschaltung
        // die Betroffenen später überrascht. Bei "dauerhaft stumm" gibt es
        // bewusst KEINE Endzeit - dort zeigt das Dashboard das auch so an.
        if ($item['muted'] && !$item['mute_permanent'] && $item['muted_at'] > 0 && $maxMute > 0) {
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
                if ($event === 'cancelled' && isset($cmd['cancel_reason'])) {
                    $item['message'] = (string)$cmd['cancel_reason'];
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
            case 'key_mute_permanent':
                $item['by'] = (string)($entry['by'] ?? '');
                $item['label'] = (string)($entry['label'] ?? '');
                $item['permanent'] = !empty($entry['permanent']);
                $item['source'] = (string)($entry['source'] ?? '');
                break;
            case 'nas_alarm_started':
            case 'false_alarm_started':
                $item['by'] = (string)($entry['by'] ?? '');
                break;
            case 'nas_alarm':
            case 'false_alarm':
                $item['by'] = (string)($entry['by'] ?? '');
                $item['ok'] = !empty($entry['ok']);
                $item['message'] = (string)($entry['message'] ?? '');
                break;
            case 'relay_alarm':
                $item['info'] = (string)($entry['info'] ?? '');
                $item['source'] = (string)($entry['source'] ?? 'relay');
                break;
            case 'box_offline':
            case 'box_recovered':
            case 'nas_web_recovered':
                $item['minutes'] = (int)($entry['minutes'] ?? 0);
                break;
            case 'nas_web_failed':
                $item['detail'] = (string)($entry['detail'] ?? '');
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

    $result = with_lock($config, 'keys', function () use ($config, $enable, $id, $user): array {
        $state = read_alarm_keys_state($config);
        if ($state === null) {
            return ['ok' => false, 'message' => ALARM_KEYS_CORRUPT_MESSAGE];
        }
        $demoPath = data_path($config, 'demo_mode.json');
        $oldDemo = read_json_file($demoPath, demo_mode_default());

        if ($enable) {
            // Demo-Empfänger muss aus der gepflegten Liste kommen: So gibt es
            // beim Zurückschalten garantiert wieder eine nicht-leere Liste.
            $idx = find_key_index($state, $id);
            if ($idx < 0) {
                return ['ok' => false, 'message' => 'Demo-Empfänger nicht gefunden. Zuerst im Panel "Alarm-Empfänger" anlegen, dann Demo-Modus einschalten.'];
            }
            $target = $state['keys'][$idx];
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
            // Ausschalten ist idempotent und erhöht IMMER die Version: So
            // lässt sich ein halb geschriebener Wechsel (Demo aus, Version
            // nicht erhöht -> ESP32 bliebe still auf der Demo-Liste) durch
            // erneutes Ausschalten reparieren.
            $wasEnabled = !empty($oldDemo['enabled']);
            $demo = ['enabled' => false, 'changed_at' => time(), 'by' => $user];
            $message = ($wasEnabled
                    ? 'LIVE-Modus aktiv: Alarme gehen wieder an ALLE Empfänger. '
                    : 'Der Demo-Modus war bereits aus – die volle Liste wird dem ESP32 erneut angeboten. ')
                . 'Der ESP32 übernimmt die volle Liste beim nächsten Poll.';
        }

        $version = bump_keys_version($config, $state);
        if (!backup_then_write($config, 'demo_mode.json', $demo)) {
            return ['ok' => false, 'message' => 'Demo-Modus konnte nicht gespeichert werden (data_dir prüfen).'];
        }
        if (!backup_then_write($config, 'alarm_keys.json', $state)) {
            // Zweiter Schritt gescheitert: Demo-Zustand ZURÜCKROLLEN. Sonst
            // wäre der Modus umgeschaltet, die Version aber nicht erhöht - der
            // ESP32 würde nie neu synchronisieren (z.B. echte Relaisalarme
            // weiter nur an den Test-Empfänger, während das Dashboard LIVE
            // zeigt).
            write_json_file($demoPath, $oldDemo);
            return ['ok' => false, 'message' => 'Demo-Modus konnte nicht gespeichert werden (data_dir prüfen) – nichts geändert.'];
        }
        append_command_log($config, [
            'event' => $enable ? 'demo_enabled' : 'demo_disabled',
            'by' => $user,
            'label' => (string)($demo['label'] ?? ''),
            'version' => $version,
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
    // 1-40 Zeichen, gültiges UTF-8, keine Steuerzeichen - ohne mbstring
    // (die Erweiterung ist im Web-Station-Profil nicht immer aktiv).
    if (preg_match('/^[^\p{Cc}]{1,40}$/u', $label) !== 1) {
        json_response(['ok' => false, 'message' => 'Name fehlt, ist länger als 40 Zeichen oder enthält ungültige Zeichen.']);
    }
    if (!valid_bark_key($key)) {
        json_response(['ok' => false, 'message' => 'Ungültiger Bark-Key (erlaubt: 5-64 Zeichen, A-Z a-z 0-9 _ -).']);
    }

    $result = mutate_alarm_keys($config, function (array &$state) use ($label, $key): array {
        $keys = is_array($state['keys'] ?? null) ? $state['keys'] : [];
        if (count($keys) >= MAX_ALARM_KEYS) {
            return ['ok' => false, 'message' => 'Maximal ' . MAX_ALARM_KEYS . ' Empfänger möglich.'];
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
        return ['ok' => true, 'write' => true, 'bump' => true,
            'message' => 'Empfänger hinzugefügt. Der ESP32 übernimmt die Liste beim nächsten Poll.'];
    });
    if (!empty($result['ok'])) {
        append_command_log($config, [
            'event' => 'key_added',
            'by' => $user,
            'label' => $label,
            'key' => mask_bark_key($key),
            'version' => (int)($result['version'] ?? 0),
        ]);
    }

    json_response($result);
}

if ($action === 'keys_delete') {
    require_csrf();
    require_dashboard_admin();
    $id = (int)($_POST['id'] ?? 0);

    $result = mutate_alarm_keys($config, function (array &$state) use ($config, $id): array {
        // Der aktive Demo-Empfänger ist gerade der EINZIGE Alarmweg - er darf
        // erst gelöscht werden, wenn der Demo-Modus wieder aus ist.
        $demo = load_demo_mode($config);
        if (!empty($demo['enabled']) && (int)($demo['key_id'] ?? 0) === $id) {
            return ['ok' => false, 'message' => 'Dieser Empfänger ist gerade der Demo-Empfänger. Zuerst den Demo-Modus beenden.'];
        }
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
        // Sicherheitsnetz: Die Alarmierung darf nie ohne Empfänger dastehen.
        if (count($remaining) === 0) {
            return ['ok' => false, 'message' => 'Der letzte Empfänger kann nicht gelöscht werden.'];
        }
        $state['keys'] = $remaining;
        return ['ok' => true, 'write' => true, 'bump' => true, 'entry' => $removed,
            'message' => 'Empfänger gelöscht. Der ESP32 übernimmt die Liste beim nächsten Poll.'];
    });
    if (!empty($result['ok'])) {
        $removed = $result['entry'];
        append_command_log($config, [
            'event' => 'key_removed',
            'by' => $user,
            'label' => (string)($removed['label'] ?? ''),
            'key' => mask_bark_key((string)($removed['key'] ?? '')),
            'version' => (int)($result['version'] ?? 0),
        ]);
    }
    unset($result['entry']);   // Klartext-Key nicht an den Browser geben

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

    $result = mutate_alarm_keys($config, function (array &$state) use ($id, $wantMuted): array {
        $idx = find_key_index($state, $id);
        if ($idx < 0) {
            return ['ok' => false, 'message' => 'Empfänger nicht gefunden.'];
        }
        $entry = $state['keys'][$idx];
        if (!empty($entry['muted']) === $wantMuted) {
            return ['ok' => true, 'changed' => false,
                'message' => $wantMuted ? 'Empfänger ist bereits stumm.' : 'Empfänger ist bereits laut.'];
        }
        $wasPermanent = mute_is_permanent($entry);
        $state['keys'][$idx]['muted'] = $wantMuted;
        $state['keys'][$idx]['muted_at'] = $wantMuted ? time() : 0;
        // Laut schalten beendet IMMER auch eine dauerhafte Stummschaltung -
        // sonst bliebe ein unsichtbares Flag hängen, das beim nächsten
        // Stummschalten überraschend wieder das Sicherheitsnetz aushebelt.
        if (!$wantMuted) {
            $state['keys'][$idx]['mute_permanent'] = false;
        }
        return ['ok' => true, 'changed' => true, 'write' => true, 'bump' => true, 'entry' => $state['keys'][$idx],
            'message' => $wantMuted
                ? 'Empfänger stummgeschaltet (Arbeitsmodus): Alarme kommen dort nur mit der eingestellten Stumm-Lautstärke an. Der ESP32 übernimmt das beim nächsten Poll.'
                : ($wasPermanent
                    ? 'Empfänger wieder laut geschaltet – die dauerhafte Stummschaltung ist damit beendet. Der ESP32 übernimmt das beim nächsten Poll.'
                    : 'Empfänger wieder laut geschaltet. Der ESP32 übernimmt das beim nächsten Poll.')];
    });

    // Log erst NACH dem Lock. Bewusst KEIN Bestätigungs-Push mehr an den
    // Betroffenen (Nutzer-Entscheidung: störte nur) - sichtbar bleibt der
    // Wechsel am Badge in der Liste und im Verlauf.
    if (!empty($result['changed']) && !empty($result['ok'])) {
        $entry = $result['entry'];
        append_command_log($config, [
            'event' => $wantMuted ? 'key_muted' : 'key_unmuted',
            'by' => $user,
            'source' => 'dashboard',
            'label' => (string)($entry['label'] ?? ''),
            'key' => mask_bark_key((string)($entry['key'] ?? '')),
            'version' => (int)($result['version'] ?? 0),
        ]);
    }
    unset($result['entry']);   // Klartext-Key nicht an den Browser geben

    json_response($result);
}

// DAUERHAFT STUMM (mute_permanent): Schaltet für EINEN Empfänger das
// Sicherheitsnetz ab - seine Stummschaltung läuft dann nicht mehr nach
// mute_max_seconds (Standard 12 Std) automatisch aus, sondern gilt, bis
// jemand sie von Hand beendet. Gedacht für längere Abwesenheiten
// (Schichtdienst, Krankheit, Urlaub), bei denen der Kollege zwar in der
// Liste bleiben soll, aber dauerhaft keinen Ton bekommt.
// Der Alarm selbst kommt weiterhin als Critical Alert an (sichtbar, mit der
// eingestellten Stumm-Lautstärke) - niemand fällt aus der Alarmierung raus.
// Einschalten setzt den Empfänger gleich mit stumm, damit kein Zustand
// entsteht, in dem das Flag gesetzt, der Empfänger aber laut ist.
// Bewusst NUR hier (Dashboard + Admin + CSRF) einschaltbar, nicht über den
// öffentlichen Kurzbefehl-Link - der kann eine Dauer-Stummschaltung nur
// beenden, nie beginnen.
if ($action === 'keys_set_mute_permanent') {
    require_csrf();
    require_dashboard_admin();
    $id = (int)($_POST['id'] ?? 0);
    $wantPermanent = (string)($_POST['permanent'] ?? '') === '1';

    // Abgelaufene Stummschaltungen zuerst beenden (nimmt selbst den keys-Lock).
    expire_stale_mutes($config);

    $maxMute = mute_max_seconds($config);
    $hours = max(1, (int)round($maxMute / 3600));
    $result = mutate_alarm_keys($config, function (array &$state) use ($id, $wantPermanent, $maxMute, $hours): array {
        $idx = find_key_index($state, $id);
        if ($idx < 0) {
            return ['ok' => false, 'message' => 'Empfänger nicht gefunden.'];
        }
        $entry = $state['keys'][$idx];
        $wasMuted = !empty($entry['muted']);
        if (mute_is_permanent($entry) === $wantPermanent) {
            return ['ok' => true, 'changed' => false,
                'message' => $wantPermanent
                    ? 'Empfänger ist bereits dauerhaft stumm.'
                    : 'Empfänger ist bereits zeitlich begrenzt stumm bzw. laut.'];
        }

        $state['keys'][$idx]['mute_permanent'] = $wantPermanent;
        if ($wantPermanent) {
            // Dauerhaft stumm heißt auch: ab jetzt stumm.
            $state['keys'][$idx]['muted'] = true;
            $state['keys'][$idx]['muted_at'] = $wasMuted
                ? (int)($entry['muted_at'] ?? time())   // bestehende Stummzeit behalten
                : time();
        } elseif ($wasMuted) {
            // Zurück auf "zeitlich begrenzt": Die 12 Std neu starten lassen,
            // statt den Empfänger wegen eines alten muted_at beim nächsten
            // expire_stale_mutes sofort wieder laut zu schalten.
            $state['keys'][$idx]['muted_at'] = time();
        }

        // Version nur erhöhen, wenn sich am Stumm-Zustand für den ESP32
        // wirklich etwas ändert: mute_permanent selbst ist reine NAS-Logik
        // (das Sicherheitsnetz läuft auf dem NAS), die Firmware kennt nur
        // muted/mutevol. Ein unnötiger Bump würde nur einen Key-Sync auslösen.
        $espChanged = ($wasMuted !== !empty($state['keys'][$idx]['muted']));
        // Ohne Sicherheitsnetz (mute_max_seconds = 0) gibt es keine
        // automatische Rückschaltung - das darf die Meldung nicht behaupten.
        $autoText = $maxMute > 0
            ? 'wird aber wie üblich nach ' . $hours . ' Std automatisch wieder laut geschaltet.'
            : 'bleibt aber stumm, bis jemand sie beendet (die automatische Rückschaltung ist in config.php abgeschaltet).';
        return ['ok' => true, 'changed' => true, 'write' => true, 'bump' => $espChanged,
            'entry' => $state['keys'][$idx],
            'message' => $wantPermanent
                ? 'Empfänger ist jetzt DAUERHAFT stumm'
                  . ($maxMute > 0 ? ': Die automatische Rückschaltung nach ' . $hours . ' Std greift hier nicht mehr.' : '.')
                  . ' Alarme kommen weiterhin sichtbar an (Stumm-Lautstärke), aber ohne vollen Ton – bis du das hier wieder beendest.'
                : 'Dauerhafte Stummschaltung beendet. Der Empfänger bleibt vorerst stumm, ' . $autoText];
    });

    // Log erst NACH dem Lock (wie bei den anderen Schaltern); kein Push an
    // den Betroffenen - sichtbar bleibt es am Badge und im Verlauf.
    if (!empty($result['changed']) && !empty($result['ok'])) {
        $entry = $result['entry'];
        append_command_log($config, [
            'event' => 'key_mute_permanent',
            'by' => $user,
            'source' => 'dashboard',
            'label' => (string)($entry['label'] ?? ''),
            'key' => mask_bark_key((string)($entry['key'] ?? '')),
            'permanent' => $wantPermanent,
            'version' => (int)($result['version'] ?? 0),
        ]);
    }
    unset($result['entry']);   // Klartext-Key nicht an den Browser geben

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
    // Streng prüfen: Ein leerer oder kaputter Wert darf NICHT still zu 0
    // (= lautlos) werden.
    $volume = filter_var($_POST['volume'] ?? null, FILTER_VALIDATE_INT,
        ['options' => ['min_range' => 0, 'max_range' => 10]]);
    if ($volume === false) {
        json_response(['ok' => false, 'message' => 'Ungültige Lautstärke (erlaubt: 0-10).']);
    }

    $result = mutate_alarm_keys($config, function (array &$state) use ($id, $volume): array {
        $idx = find_key_index($state, $id);
        if ($idx < 0) {
            return ['ok' => false, 'message' => 'Empfänger nicht gefunden.'];
        }
        if (mute_volume_of($state['keys'][$idx]) === $volume) {
            return ['ok' => true, 'changed' => false, 'message' => 'Stumm-Lautstärke ist bereits so eingestellt.'];
        }
        $state['keys'][$idx]['mute_volume'] = $volume;
        return ['ok' => true, 'changed' => true, 'write' => true, 'bump' => true, 'entry' => $state['keys'][$idx],
            'message' => ($volume === 0
                ? 'Gespeichert: Stumm = komplett lautlos.'
                : 'Gespeichert: Stumm = leise mit Lautstärke ' . $volume . '.')
                . ' Der ESP32 übernimmt das beim nächsten Poll.'];
    });

    // Log erst NACH dem Lock; kein Push an den Betroffenen.
    if (!empty($result['changed']) && !empty($result['ok'])) {
        $entry = $result['entry'];
        append_command_log($config, [
            'event' => 'key_mute_volume',
            'by' => $user,
            'label' => (string)($entry['label'] ?? ''),
            'key' => mask_bark_key((string)($entry['key'] ?? '')),
            'volume' => $volume,
            'version' => (int)($result['version'] ?? 0),
        ]);
    }
    unset($result['entry']);   // Klartext-Key nicht an den Browser geben

    json_response($result);
}

// ENTWARNUNG (Fehlalarm): Critical Alert mit kurzem, normalem Ton an alle
// Empfänger, dass der letzte Alarm ein Fehlalarm war. Bewusst ebenfalls
// critical - wer schon unterwegs ist, muss die Entwarnung sicher mitbekommen.
// Läuft wie der REAL ALARM direkt vom NAS (also auch bei toter ESP32-Kette).
if ($action === 'false_alarm') {
    require_csrf();
    require_dashboard_admin();
    require_expected_mode($config);
    // Start VOR dem Versand festhalten: Bricht der Request ab (Proxy-Timeout),
    // sieht man im Verlauf trotzdem, dass gesendet wurde.
    append_command_log($config, ['event' => 'false_alarm_started', 'by' => $user]);
    $result = bark_send_false_alarm_all($config);
    append_command_log($config, [
        'event' => 'false_alarm',
        'by' => $user,
        'ok' => $result['ok'],
        'message' => $result['message'],
        'results' => $result['results'],
    ]);
    json_response($result);
}

// REAL ALARM: bewusst DIREKT vom NAS an Bark, ohne Umweg über den ESP32.
// Eine manuelle Alarmierung braucht man vor allem dann, wenn die ESP32-Kette
// gerade NICHT funktioniert - dann darf sie nicht am Poll des ESP32 hängen.
if ($action === 'alarm') {
    require_csrf();
    require_dashboard_admin();
    require_expected_mode($config);
    append_command_log($config, ['event' => 'nas_alarm_started', 'by' => $user]);
    $result = bark_send_alarm_all($config);
    append_command_log($config, [
        'event' => 'nas_alarm',
        'by' => $user,
        'ok' => $result['ok'],
        'message' => $result['message'],
        'results' => $result['results'],
    ]);
    json_response($result);
}

http_response_code(400);
json_response(['ok' => false, 'message' => 'Unbekannte Aktion.']);
