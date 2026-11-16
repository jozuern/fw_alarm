<?php
// JSON-Aufrufer: Lock-/Speicherfehler kommen als JSON statt als Text (siehe
// with_lock()) - sonst zeigte der Browser nur "NAS nicht erreichbar".
const FW_ALARM_JSON_API = true;
require __DIR__ . '/lib/common.php';

$config = load_config();
// Nur eine bestehende, angemeldete Sitzung fortsetzen - ein Aufruf ohne
// gültige Anmeldung legt keine an (siehe resume_dashboard_session()).
if (!resume_dashboard_session()) {
    http_response_code(401);
    json_response(['ok' => false]);
}
// Sitzung sofort wieder freigeben: PHP-Datei-Sessions bleiben gesperrt,
// solange ein Skript läuft. Ein REAL ALARM (bis ~1 min) hätte sonst ALLE
// anderen Anfragen dieses Browsers blockiert - die Statusanzeige fror ein,
// ENTWARNUNG und Abbrechen warteten. $_SESSION bleibt danach lesbar (CSRF,
// Rolle, Benutzer); last_seen hat resume_dashboard_session() schon gesetzt.
session_write_close();

$method = (string)($_SERVER['REQUEST_METHOD'] ?? 'GET');
$action = $method === 'POST'
    ? (string)($_POST['action'] ?? '')
    : (string)($_GET['action'] ?? 'status');
$user = (string)($_SESSION['user'] ?? 'dashboard');

// ==== Regeln aller Aktionen – an EINER Stelle ===============================
// Jede Aktion steht hier mit ihren Zugangsregeln; geprüft wird zentral, BEVOR
// irgendein Handler läuft (die Handler weiter unten prüfen das nicht noch
// einmal). Eine Aktion, die hier fehlt, wird abgelehnt - eine neue Aktion
// ohne Eintrag ist also gesperrt, statt versehentlich ohne CSRF-/Admin-
// Prüfung erreichbar zu sein. Jeder Eintrag nennt alle drei Regeln
// ausdrücklich:
//   post  = nur per POST (alles, was etwas ändert oder auslöst)
//   csrf  = CSRF-Token nötig (Header X-CSRF-Token oder Feld csrf)
//   admin = nur die Admin-Rolle (die Lese-Rolle bekommt 403 'readonly'; die
//           ausgeblendeten Knöpfe im UI sind nur Komfort, kein Schutz)
// Reihenfolge der Prüfung wie bisher: Methode (405), dann CSRF (403 'csrf'),
// dann Rolle (403 'readonly'). Unbekannt: per GET 405, per POST 400.
const DASHBOARD_ACTIONS = [
    // Lesen (auch per GET)
    'status'                  => ['post' => false, 'csrf' => false, 'admin' => false],
    'keys_list'               => ['post' => false, 'csrf' => false, 'admin' => false],
    'log_view'                => ['post' => false, 'csrf' => false, 'admin' => false],
    // lesend, aber nur für den Admin: Welche Meldungen er bekommt, geht die
    // Lese-Rolle nichts an
    'notify_list'             => ['post' => false, 'csrf' => false, 'admin' => true],
    'settings_list'           => ['post' => false, 'csrf' => false, 'admin' => true],
    // Ändern/Auslösen
    'enqueue'                 => ['post' => true, 'csrf' => true, 'admin' => true],
    'cancel'                  => ['post' => true, 'csrf' => true, 'admin' => true],
    'demo_set'                => ['post' => true, 'csrf' => true, 'admin' => true],
    'keys_add'                => ['post' => true, 'csrf' => true, 'admin' => true],
    'keys_delete'             => ['post' => true, 'csrf' => true, 'admin' => true],
    'keys_rename'             => ['post' => true, 'csrf' => true, 'admin' => true],
    'keys_replace_key'        => ['post' => true, 'csrf' => true, 'admin' => true],
    'keys_set_phone'          => ['post' => true, 'csrf' => true, 'admin' => true],
    'keys_new_mute_token'     => ['post' => true, 'csrf' => true, 'admin' => true],
    'keys_mute'               => ['post' => true, 'csrf' => true, 'admin' => true],
    'keys_set_mute_permanent' => ['post' => true, 'csrf' => true, 'admin' => true],
    'keys_set_mute_volume'    => ['post' => true, 'csrf' => true, 'admin' => true],
    'keys_test'               => ['post' => true, 'csrf' => true, 'admin' => true],
    'notify_set'              => ['post' => true, 'csrf' => true, 'admin' => true],
    'settings_set'            => ['post' => true, 'csrf' => true, 'admin' => true],
    'notify_test'             => ['post' => true, 'csrf' => true, 'admin' => true],
    'alarm'                   => ['post' => true, 'csrf' => true, 'admin' => true],
    'false_alarm'             => ['post' => true, 'csrf' => true, 'admin' => true],
];

$rules = DASHBOARD_ACTIONS[$action] ?? null;
if (($rules === null || $rules['post']) && $method !== 'POST') {
    http_response_code(405);
    json_response(['ok' => false, 'message' => 'Diese Aktion ist nur per POST erlaubt.']);
}
if ($rules === null) {
    http_response_code(400);
    json_response(['ok' => false, 'message' => 'Unbekannte Aktion.']);
}
if ($rules['csrf']) {
    require_csrf();
}
if ($rules['admin']) {
    require_dashboard_admin();
}

// Die Person hat den Modus bestätigt, der beim Scharfschalten des Knopfs
// angezeigt wurde (expect_demo: '0' = LIVE, sonst key_id des
// Demo-Empfängers). Hat er sich inzwischen geändert - etwa aus einer zweiten
// Sitzung -, wird NICHTS gesendet: Ein als "Demo" bestätigter Alarm darf nie
// an alle gehen und umgekehrt. Das Feld ist PFLICHT: Ein noch mit einer
// alten Oberfläche offener Tab bekommt 409 und lädt neu, statt ohne
// Modusschutz zu alarmieren.
// $demo = Momentaufnahme des Demo-Zustands, mit der danach auch GESENDET wird
// (siehe bark_broadcast) - Prüfung und Versand sehen so denselben Stand.
function require_expected_mode(array $config, ?array $demo = null): void
{
    // EINE Quelle für beide Seiten: Der Browser bildet seine Erwartung aus
    // genau dieser Info (enabled + key_id).
    $info = demo_mode_info($config, $demo);
    $actual = $info['enabled'] ? (string)$info['key_id'] : '0';
    if (!isset($_POST['expect_demo']) || !is_string($_POST['expect_demo'])) {
        http_response_code(409);
        json_response([
            'ok' => false,
            'code' => 'mode_changed',
            'message' => 'Die Oberfläche ist veraltet (Modus nicht bestätigt) – es wurde NICHTS gesendet. Seite neu laden.',
            'demo' => $info,
        ]);
    }
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
    $watchdog = [
        'configured' => trim((string)($config['bark_key_status'] ?? '')) !== '',
        'last_run' => (int)($watchdogState['last_run_at'] ?? 0),
        'last_send_ok_at' => (int)($watchdogState['last_send_ok_at'] ?? 0),
        'last_send_failed_at' => (int)($watchdogState['last_send_failed_at'] ?? 0),
    ];
    // Außen-Wächter (Cloud-VM): wann er zuletzt über api/alive.php angeklopft
    // hat, ob er seine eigenen Bark-Meldungen zustellen kann (bark_failed_at,
    // 0 = ok) und ab wann er als stumm gilt (dieselbe Schwelle wie die
    // Meldung des Cron-Wächters). Nur Zeiten - keine Adresse, kein Token.
    if (outside_watcher_configured($config)) {
        $watcherSeen = read_json_file(data_path($config, 'watcher_seen.json'), []);
        $watchdog['outside_seen_at'] = (int)($watcherSeen['seen_at'] ?? 0);
        $watchdog['outside_bark_failed_at'] = (int)($watcherSeen['bark_failed_at'] ?? 0);
        $watchdog['outside_silent_after_s'] = (int)(load_notify_settings($config)['types']['outside_watcher']['silent_after_s'] ?? 3600);
    }
    // Hat der Admin die Ausfall-Meldungen abgeschaltet (Panel "Meldungen an
    // den Admin")? Dann läuft der Wächter zwar, würde einen Ausfall aber nicht
    // melden - das Dashboard weist darauf hin. NUR für den Admin: Die
    // Einstellungen seiner Meldungen gehen die Lese-Rolle nichts an (auch
    // notify_list ist ihr verwehrt).
    if (dashboard_role() === 'admin') {
        $flags = important_notify_flags($config);
        $watchdog['offline_alert'] = $flags['box_offline'];
        $watchdog['web_alert'] = $flags['nas_web'];
        if (outside_watcher_configured($config)) {
            $watchdog['outside_alert'] = $flags['outside_watcher'];
        }
    }
    // Die öffentliche Adresse der Wache braucht die Anzeige nicht.
    unset($latest['remote_addr']);
    $demoInfo = demo_mode_info($config);
    json_response([
        'ok' => true,
        'status' => $latest,
        'seen_age_seconds' => $age,
        'offline' => $age === null || $age > $offlineAfter,
        'command' => $commandState['current'] ?? null,
        // Demo-Modus + NAS-Listenversion: Damit kann das Dashboard anzeigen,
        // ob der ESP32 die aktuelle Liste schon übernommen hat (Vergleich mit
        // dem keys_version-Feld aus dem ESP32-Status).
        'demo' => $demoInfo,
        'nas_keys_version' => (int)(load_alarm_keys($config)['version'] ?? 0),
        // "Box hat die Demo-Liste übernommen" nach DERSELBEN Regel wie die
        // Sperre des DEMO-ALARMs (Version, genau 1 Empfänger, frisch) - das
        // Banner darf nicht "gefahrlos testen" sagen, wo der Server sperrt.
        'demo_sync_ok' => $demoInfo['enabled'] && demo_alarm_block_reason($config) === null,
        // Mehrere/fremde Boards mit dem Maschinen-Token (siehe device_hint()).
        'device_hint' => device_hint($config),
        // Zeitzone für die Zeitangaben im Browser (wie config.php).
        'timezone' => date_default_timezone_get(),
        // Aktuelles CSRF-Token: Ein alter Tab übernimmt es nach einem Neu-Login
        // in einem anderen Tab (gleiches Cookie, neue Sitzung) - sonst
        // scheiterte ausgerechnet sein erster REAL ALARM am veralteten Token.
        'csrf' => (string)($_SESSION['csrf'] ?? ''),
        // Selbstüberwachung des Offline-Wächters: Das Dashboard warnt, wenn
        // die DSM-Aufgabe nie oder zu lange nicht gelaufen ist - und wenn sie
        // zwar läuft, ihre Meldungen aber nicht zustellen kann.
        'watchdog' => $watchdog,
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
                    return ['ok' => false, 'message' => 'Der letzte Befehl wurde an die Box ausgeliefert, ihre Bestätigung steht noch aus.'];
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
        }
        $state['next_id'] = $id + 1;
        $state['current'] = $command;
        remember_command_type($state, $id, $type);
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
    $state = read_alarm_keys_state($config);
    if ($state === null) {
        // Liste UND Sicherung unlesbar: nicht als "keine eingetragen" zeigen -
        // die Box alarmiert weiter mit ihrer gespeicherten Liste, der REAL
        // ALARM mit der Not-Liste aus config.php.
        json_response(['ok' => false, 'code' => 'keys_corrupt', 'message' => ALARM_KEYS_CORRUPT_MESSAGE]);
    }
    $isAdmin = dashboard_role() === 'admin';
    $maxMute = mute_max_seconds($config);
    $entries = [];
    foreach (($state['keys'] ?? []) as $entry) {
        if (!is_array($entry)) {
            continue;
        }
        // Überfällig, aber (mangels Schreibrecht) nicht weggeschrieben: Der
        // NAS-Weg rechnet den Empfänger schon als laut - so zeigt es auch die
        // Liste. Die BOX alarmiert ihn aber noch stumm, bis das Wegschreiben
        // gelingt (mute_overdue -> Warnung an der Zeile).
        $overdue = mute_is_stale($config, $entry);
        $item = [
            'id' => (int)($entry['id'] ?? 0),
            'label' => (string)($entry['label'] ?? ''),
            'key_masked' => mask_bark_key((string)($entry['key'] ?? '')),
            'muted' => !empty($entry['muted']) && !$overdue,
            'mute_overdue' => $overdue,
            'muted_at' => (int)($entry['muted_at'] ?? 0),
            'mute_volume' => mute_volume_of($entry),
            'mute_permanent' => mute_is_permanent($entry),
        ];
        // Die Telefonnummer sieht nur der Admin (nie api/keys.php, nie Bark/Log).
        if ($isAdmin) {
            $item['phone'] = (string)($entry['phone'] ?? '');
        }
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
        'mute_max_seconds' => $maxMute,
    ]);
}

// ==== Verlauf (letzte Ereignisse aus commands.log) ==========================
// Nur lesend, deshalb auch für den Lese-Benutzer erlaubt. Es gehen NUR die
// in log_event_types() freigegebenen Felder an den Browser - egal was künftig
// zusätzlich ins Log geschrieben wird. Bark-Keys stehen im Log ohnehin nur
// maskiert (mask_bark_key beim Schreiben).
if ($action === 'log_view') {
    // Auf Wunsch ältere Ereignisse nachladen ("Ältere anzeigen" im Dashboard).
    // Gedeckelt, damit niemand das komplette Log in den Browser zieht.
    $limit = max(1, min(100, (int)($_GET['limit'] ?? 25)));
    // Filter (Alarme / Empfänger / System): Die täglichen Stumm-Wechsel der
    // Kurzbefehle verdrängen sonst die Alarme aus dem sichtbaren Fenster.
    // Unbekannte Werte zählen als "alles".
    // 'alarm_core' = nur die Versand-Ereignisse (für "Letzter Alarm" im
    // Held-Bereich, ohne die Befehls-Ereignisse der Demo-Alarme).
    $filter = (string)($_GET['filter'] ?? 'all');
    if (!in_array($filter, ['alarm', 'alarm_core', 'keys', 'system'], true)) {
        $filter = 'all';
    }
    // Was die Lese-Rolle nicht sehen darf (notify_changed), wird VOR dem
    // Abzählen aussortiert - sonst bekäme sie weniger als $limit Einträge, und
    // "Ältere anzeigen" verschwände. Welche Ereignisse und Felder ausgeliefert
    // werden, steht allein in log_event_types() (lib/common.php).
    $isAdmin = dashboard_role() === 'admin';
    $skip = log_events_hidden($isAdmin);
    $raw = ($filter === 'all' && $skip === [])
        ? read_command_log_tail($config, $limit)
        : read_command_log_filtered($config, $limit, $filter, $skip);
    $out = [];
    foreach ($raw as $entry) {
        $item = log_event_view($entry, $isAdmin);
        if ($item !== null) {
            $out[] = $item;
        }
    }
    // server_time für relative Zeitangaben ("vor 5 min") - so spielt eine
    // falsch gehende Browser-Uhr keine Rolle.
    json_response(['ok' => true, 'entries' => $out, 'server_time' => time()]);
}

// ÄLTERE Firmware behandelt einen DEMO-ALARM wie einen Relaisalarm - danach
// gilt auch ihre Sperrzeit (COOLDOWN_MS in config.h, Standard 5 min): Ein
// ECHTER Relaisalarm in dieser Zeit wird von der Box verworfen. Wer kurz nach
// einem Demo-Alarm zurück auf LIVE schaltet, muss das wissen. Die Sperrzeit
// kennt das NAS nicht genau; angenommen werden die 300 s des Standards.
function demo_alarm_cooldown_warning(array $config): string
{
    // Firmware ab 11/2026 (erkennbar am immer gesendeten alarm_seq) setzt bei
    // einem DEMO-ALARM keine Sperrzeit mehr - dann gibt es nichts zu warnen.
    if (array_key_exists('alarm_seq', read_json_file(data_path($config, 'latest_status.json'), []))) {
        return '';
    }
    $current = read_json_file(data_path($config, 'command_state.json'), command_state_default())['current'] ?? null;
    if (!is_array($current) || (string)($current['type'] ?? '') !== 'ALARM') {
        return '';
    }
    $executed = (string)($current['state'] ?? '') === 'delivered'
        || ((string)($current['state'] ?? '') === 'acked' && (string)($current['ack_result'] ?? '') === 'executed');
    $until = (int)($current['delivered_at'] ?? 0) + 300;
    if (!$executed || $until <= time()) {
        return '';
    }
    return ' ACHTUNG: Die Box hat gerade einen DEMO-ALARM ausgeführt und erkennt bis ca. '
        . date('H:i', $until) . ' Uhr keinen neuen Relaisalarm (Sperrzeit nach jedem Alarm). '
        . 'Im Ernstfall bis dahin den REAL ALARM benutzen.';
}

// ==== Demo-Modus ein-/ausschalten ===========================================
// Jeder Wechsel erhöht die Listen-Version, damit der ESP32 beim nächsten Poll
// die passende Liste (nur Demo-Key bzw. wieder alle) holt. Läuft unter dem
// 'keys'-Lock, weil Liste und Demo-Zustand zusammen konsistent bleiben müssen.
if ($action === 'demo_set') {
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
                . 'WICHTIG: Die Box übernimmt das erst nach einigen Sekunden – Banner oben beobachten!';
        } else {
            // Ausschalten ist idempotent und erhöht IMMER die Version: So
            // lässt sich ein halb geschriebener Wechsel (Demo aus, Version
            // nicht erhöht -> ESP32 bliebe still auf der Demo-Liste) durch
            // erneutes Ausschalten reparieren.
            $wasEnabled = !empty($oldDemo['enabled']);
            $demo = ['enabled' => false, 'changed_at' => time(), 'by' => $user];
            $message = ($wasEnabled
                    ? 'LIVE-Modus aktiv: Alarme gehen wieder an ALLE Empfänger. '
                    : 'Der Demo-Modus war bereits aus – die volle Liste wird der Box erneut angeboten. ')
                . 'Die Box übernimmt die volle Liste nach wenigen Sekunden.'
                . demo_alarm_cooldown_warning($config);
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
            if (write_json_file($demoPath, $oldDemo)) {
                return ['ok' => false, 'message' => 'Demo-Modus konnte nicht gespeichert werden (data_dir prüfen) – nichts geändert.'];
            }
            // Auch das Zurückrollen scheiterte: Der Modus IST umgeschaltet,
            // die Box hat davon aber nichts erfahren. Laut melden - nie
            // "nichts geändert" behaupten.
            error_log('fw_alarm: demo_set - alarm_keys.json UND Rückrollen von demo_mode.json gescheitert, Zustand inkonsistent');
            return ['ok' => false, 'code' => 'inconsistent',
                'message' => 'ACHTUNG: Zustand inkonsistent – der Demo-Modus ist im Dashboard '
                    . ($enable ? 'EIN' : 'AUS') . ', die Box hat das aber NICHT übernommen. '
                    . 'Datenordner (data_dir) prüfen und danach den Demo-Modus erneut AUS schalten.'];
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

    // Optionale Telefonnummer (nur Anzeige): ungültig = der ganze Antrag
    // scheitert, es wird nichts angelegt.
    $phone = normalize_phone((string)($_POST['phone'] ?? ''));
    if ($phone === null) {
        json_response(['ok' => false, 'message' => PHONE_INVALID_MESSAGE]);
    }

    $result = mutate_alarm_keys($config, function (array &$state) use ($config, $label, $key, $phone): array {
        $keys = is_array($state['keys'] ?? null) ? $state['keys'] : [];
        if (count($keys) >= MAX_ALARM_KEYS) {
            return ['ok' => false, 'message' => 'Maximal ' . MAX_ALARM_KEYS . ' Empfänger möglich.'];
        }
        foreach ($keys as $entry) {
            if (hash_equals((string)($entry['key'] ?? ''), $key)) {
                return ['ok' => false, 'message' => 'Dieser Key ist bereits eingetragen.'];
            }
        }
        // Nie eine schon vergebene id erneut (Hochwassermarke, siehe dort).
        $id = allocate_key_id($config, $state);
        $keys[] = [
            'id' => $id,
            'label' => $label,
            'key' => $key,
            'added_at' => time(),
            // Geheim-Token für den persönlichen Stummschalt-Link (Kurzbefehl).
            'mute_token' => new_mute_token(),
        ];
        if ($phone !== '') {
            $keys[count($keys) - 1]['phone'] = $phone;
        }
        $state['keys'] = $keys;
        return ['ok' => true, 'write' => true, 'bump' => true,
            'message' => 'Empfänger hinzugefügt. Die Box übernimmt die Liste nach wenigen Sekunden.'];
    });
    if (!empty($result['ok'])) {
        log_key_event($config, 'key_added', ['label' => $label, 'key' => $key], $user,
            ['version' => (int)($result['version'] ?? 0)]);
    }

    json_response($result);
}

if ($action === 'keys_delete') {
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
            'message' => 'Empfänger gelöscht. Die Box übernimmt die Liste nach wenigen Sekunden.'];
    });
    if (!empty($result['ok'])) {
        $removed = $result['entry'];
        log_key_event($config, 'key_removed', $removed, $user, ['version' => (int)($result['version'] ?? 0)]);
    }
    unset($result['entry']);   // Klartext-Key nicht an den Browser geben

    json_response($result);
}

// Empfänger umbenennen. Der Name steht nur im Dashboard (nicht in der Liste der
// Box, nicht in Bark-Texten) - deshalb KEIN Versions-Bump und kein Key-Sync.
// Ist es der aktive Demo-Empfänger, zieht das Demo-Banner den Namen mit
// (label in demo_mode.json). Reihenfolge wie bei demo_set: erst demo_mode.json,
// dann (nach dem Callback) alarm_keys.json - beides unter dem 'keys'-Lock.
if ($action === 'keys_rename') {
    $id = (int)($_POST['id'] ?? 0);
    $label = trim((string)($_POST['label'] ?? ''));
    if (preg_match('/^[^\p{Cc}]{1,40}$/u', $label) !== 1) {
        json_response(['ok' => false, 'message' => 'Name fehlt, ist länger als 40 Zeichen oder enthält ungültige Zeichen.']);
    }

    $result = mutate_alarm_keys($config, function (array &$state) use ($config, $id, $label): array {
        $idx = find_key_index($state, $id);
        if ($idx < 0) {
            return ['ok' => false, 'message' => 'Empfänger nicht gefunden.'];
        }
        $oldLabel = (string)($state['keys'][$idx]['label'] ?? '');
        if ($oldLabel === $label) {
            return ['ok' => true, 'changed' => false, 'message' => 'Der Name ist bereits so eingetragen.'];
        }
        $demo = load_demo_mode($config);
        if (!empty($demo['enabled']) && (int)($demo['key_id'] ?? 0) === $id) {
            $demo['label'] = $label;
            if (!backup_then_write($config, 'demo_mode.json', $demo)) {
                return ['ok' => false, 'message' => 'Name konnte nicht gespeichert werden (data_dir prüfen).'];
            }
        }
        $state['keys'][$idx]['label'] = $label;
        return ['ok' => true, 'changed' => true, 'write' => true, 'old_label' => $oldLabel,
            'entry' => $state['keys'][$idx], 'message' => 'Name geändert.'];
    });
    if (!empty($result['ok']) && !empty($result['changed'])) {
        log_key_event($config, 'key_renamed', $result['entry'], $user, ['old_label' => (string)$result['old_label']]);
    }
    unset($result['entry'], $result['old_label']);   // Klartext-Key nicht an den Browser geben

    json_response($result);
}

// Bark-Key eines Empfängers ersetzen (neues iPhone/neue App-Installation):
// Name, Stumm-Zustand, Token (Kurzbefehl-Link) und Telefonnummer bleiben. Die
// Box bekommt eine neue Listen-Version. Der aktive Demo-Empfänger ist
// ausgenommen - demo_mode.json hält seinen Key als Kopie.
if ($action === 'keys_replace_key') {
    $id = (int)($_POST['id'] ?? 0);
    $newKey = trim((string)($_POST['key'] ?? ''));
    if (!valid_bark_key($newKey)) {
        json_response(['ok' => false, 'message' => 'Ungültiger Bark-Key (erlaubt: 5-64 Zeichen, A-Z a-z 0-9 _ -).']);
    }

    $result = mutate_alarm_keys($config, function (array &$state) use ($config, $id, $newKey): array {
        $demo = load_demo_mode($config);
        if (!empty($demo['enabled']) && (int)($demo['key_id'] ?? 0) === $id) {
            return ['ok' => false, 'message' => 'Dieser Empfänger ist gerade der Demo-Empfänger. Zuerst den Demo-Modus beenden.'];
        }
        $idx = find_key_index($state, $id);
        if ($idx < 0) {
            return ['ok' => false, 'message' => 'Empfänger nicht gefunden.'];
        }
        foreach ($state['keys'] as $i => $entry) {
            if ((int)$i !== $idx && is_array($entry) && hash_equals((string)($entry['key'] ?? ''), $newKey)) {
                return ['ok' => false, 'message' => 'Dieser Key ist bereits eingetragen.'];
            }
        }
        $oldKey = (string)($state['keys'][$idx]['key'] ?? '');
        if (hash_equals($oldKey, $newKey)) {
            return ['ok' => true, 'changed' => false, 'message' => 'Dieser Key ist bereits eingetragen.'];
        }
        $state['keys'][$idx]['key'] = $newKey;
        return ['ok' => true, 'changed' => true, 'write' => true, 'bump' => true, 'old_key' => $oldKey,
            'entry' => $state['keys'][$idx],
            'message' => 'Key ersetzt. Die Box übernimmt die Liste nach wenigen Sekunden.'];
    });
    if (!empty($result['ok']) && !empty($result['changed'])) {
        log_key_event($config, 'key_replaced', $result['entry'], $user,
            ['old_key' => mask_bark_key((string)$result['old_key']), 'version' => (int)($result['version'] ?? 0)]);
    }
    unset($result['entry'], $result['old_key']);   // Klartext-Keys nicht an den Browser geben

    json_response($result);
}

// Telefonnummer eines Empfängers (nur Anzeige im Dashboard, nur Admin). Weder
// die Box noch Bark noch der Verlauf bekommen sie - daher kein Versions-Bump.
if ($action === 'keys_set_phone') {
    $id = (int)($_POST['id'] ?? 0);
    $phone = normalize_phone((string)($_POST['phone'] ?? ''));
    if ($phone === null) {
        json_response(['ok' => false, 'message' => PHONE_INVALID_MESSAGE]);
    }

    $result = mutate_alarm_keys($config, function (array &$state) use ($id, $phone): array {
        $idx = find_key_index($state, $id);
        if ($idx < 0) {
            return ['ok' => false, 'message' => 'Empfänger nicht gefunden.'];
        }
        if ((string)($state['keys'][$idx]['phone'] ?? '') === $phone) {
            return ['ok' => true, 'changed' => false, 'message' => 'Telefonnummer ist bereits so eingetragen.'];
        }
        if ($phone === '') {
            unset($state['keys'][$idx]['phone']);
        } else {
            $state['keys'][$idx]['phone'] = $phone;
        }
        return ['ok' => true, 'changed' => true, 'write' => true, 'entry' => $state['keys'][$idx],
            'message' => $phone === '' ? 'Telefonnummer entfernt.' : 'Telefonnummer gespeichert.'];
    });
    if (!empty($result['ok']) && !empty($result['changed'])) {
        log_key_event($config, 'key_phone_changed', $result['entry'], $user);
    }
    unset($result['entry']);

    json_response($result);
}

// Neue Kurzbefehl-Links für EINEN Empfänger: vergibt einen neuen Geheim-Token,
// die bisherigen Links (api/mute.php?t=...) sind damit sofort ungültig. Der
// Ausweg, wenn ein Link in falsche Hände geraten ist (weitergeleitete
// Nachricht, Bildschirmfoto, altes Handy) - wer ihn kennt, könnte den Kollegen
// sonst auf Dauer immer wieder stumm schalten. Am Stumm-Zustand selbst ändert
// sich nichts (ggf. danach von Hand laut schalten). KEIN Versions-Bump: Die
// Box kennt die Tokens nicht. Der neue Token steht nicht in der Antwort - die
// Oberfläche holt ihn mit der Liste (keys_list, nur Admin). Der alte
// key=-Link (eigener Bark-Key) bleibt gültig; den entwertet nur "Key ersetzen".
if ($action === 'keys_new_mute_token') {
    $id = (int)($_POST['id'] ?? 0);

    $result = mutate_alarm_keys($config, function (array &$state) use ($id): array {
        $idx = find_key_index($state, $id);
        if ($idx < 0) {
            return ['ok' => false, 'message' => 'Empfänger nicht gefunden.'];
        }
        $state['keys'][$idx]['mute_token'] = new_mute_token();
        return ['ok' => true, 'changed' => true, 'write' => true, 'entry' => $state['keys'][$idx],
            'message' => 'Neue Kurzbefehl-Links erzeugt – die bisherigen Links funktionieren ab sofort nicht mehr. '
                . 'Die neuen Links kopieren und im Kurzbefehl des Empfängers eintragen.'];
    });
    if (!empty($result['ok']) && !empty($result['changed'])) {
        log_key_event($config, 'key_token_renewed', $result['entry'], $user);
    }
    unset($result['entry']);   // weder Klartext-Key noch Token in der Antwort

    json_response($result);
}

// Arbeitsmodus vom Dashboard aus umschalten (zusätzlich zum Kurzbefehl-Link
// api/mute.php): stumm = Critical Alert mit der eingestellten
// Stumm-Lautstärke (Standard 0 = lautlos). Jeder Wechsel erhöht die
// Listen-Version, damit der ESP32 ihn beim nächsten Poll übernimmt.
if ($action === 'keys_mute') {
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
        // Laut schalten beendet IMMER auch eine dauerhafte Stummschaltung
        // (Regel in apply_mute_change).
        $permanentEnded = apply_mute_change($state['keys'][$idx], $wantMuted);
        return ['ok' => true, 'changed' => true, 'write' => true, 'bump' => true, 'entry' => $state['keys'][$idx],
            'permanent_ended' => $permanentEnded,
            'message' => $wantMuted
                ? 'Empfänger stummgeschaltet (Arbeitsmodus): Alarme kommen dort nur mit der eingestellten Stumm-Lautstärke an. Die Box übernimmt das nach wenigen Sekunden.'
                : ($permanentEnded
                    ? 'Empfänger wieder laut geschaltet – die dauerhafte Stummschaltung ist damit beendet. Die Box übernimmt das nach wenigen Sekunden.'
                    : 'Empfänger wieder laut geschaltet. Die Box übernimmt das nach wenigen Sekunden.')];
    });

    // Log erst NACH dem Lock. Bewusst KEIN Bestätigungs-Push mehr an den
    // Betroffenen (Nutzer-Entscheidung: störte nur) - sichtbar bleibt der
    // Wechsel am Badge in der Liste und im Verlauf.
    if (!empty($result['changed']) && !empty($result['ok'])) {
        $entry = $result['entry'];
        $logExtra = ['source' => 'dashboard', 'version' => (int)($result['version'] ?? 0)];
        log_key_event($config, $wantMuted ? 'key_muted' : 'key_unmuted', $entry, $user, $logExtra);
        // Hat "Laut" eine DAUERHAFTE Stummschaltung beendet, das extra
        // festhalten - wie beim Kurzbefehl-Link (api/mute.php).
        if (!empty($result['permanent_ended'])) {
            log_key_event($config, 'key_mute_permanent', $entry, $user, $logExtra + ['permanent' => false]);
        }
    }
    unset($result['entry'], $result['permanent_ended']);   // Klartext-Key nicht an den Browser geben

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
    $id = (int)($_POST['id'] ?? 0);
    $wantPermanent = (string)($_POST['permanent'] ?? '') === '1';

    // Abgelaufene Stummschaltungen zuerst beenden (nimmt selbst den keys-Lock).
    expire_stale_mutes($config);

    $maxMute = mute_max_seconds($config);
    // Sekundengenau wie eingestellt (z.B. "30 Min"), nicht auf Stunden gerundet.
    $hours = notify_duration_label($maxMute);
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
            ? 'wird aber wie üblich nach ' . $hours . ' automatisch wieder laut geschaltet.'
            : 'bleibt aber stumm, bis jemand sie beendet (die automatische Rückschaltung ist in config.php abgeschaltet).';
        return ['ok' => true, 'changed' => true, 'write' => true, 'bump' => $espChanged,
            'entry' => $state['keys'][$idx],
            'message' => $wantPermanent
                ? 'Empfänger ist jetzt DAUERHAFT stumm'
                  . ($maxMute > 0 ? ': Die automatische Rückschaltung nach ' . $hours . ' greift hier nicht mehr.' : '.')
                  . ' Alarme kommen weiterhin sichtbar an (Stumm-Lautstärke), aber ohne vollen Ton – bis du das hier wieder beendest.'
                : 'Dauerhafte Stummschaltung beendet. Der Empfänger bleibt vorerst stumm, ' . $autoText];
    });

    // Log erst NACH dem Lock (wie bei den anderen Schaltern); kein Push an
    // den Betroffenen - sichtbar bleibt es am Badge und im Verlauf.
    if (!empty($result['changed']) && !empty($result['ok'])) {
        $entry = $result['entry'];
        log_key_event($config, 'key_mute_permanent', $entry, $user,
            ['source' => 'dashboard', 'permanent' => $wantPermanent, 'version' => (int)($result['version'] ?? 0)]);
    }
    unset($result['entry']);   // Klartext-Key nicht an den Browser geben

    json_response($result);
}

// Stumm-Lautstärke pro Empfänger (Arbeitsmodus): 0 = komplett lautlos
// (Standard), 1-10 = der Critical Alert kommt leise mit diesem Pegel an.
// Gilt für BEIDE Alarmwege; jeder Wechsel erhöht die Listen-Version, damit
// der ESP32 den Wert beim nächsten Poll übernimmt.
if ($action === 'keys_set_mute_volume') {
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
                . ' Die Box übernimmt das nach wenigen Sekunden.'];
    });

    // Log erst NACH dem Lock; kein Push an den Betroffenen.
    if (!empty($result['changed']) && !empty($result['ok'])) {
        $entry = $result['entry'];
        log_key_event($config, 'key_mute_volume', $entry, $user,
            ['volume' => $volume, 'version' => (int)($result['version'] ?? 0)]);
    }
    unset($result['entry']);   // Klartext-Key nicht an den Browser geben

    json_response($result);
}

// ==== Meldungen an den Admin einstellen =====================================
// Welche Statusmeldungen das NAS an bark_key_status schickt und wie (siehe
// "MELDUNGEN AN DEN ADMIN" in common.php). Nur für den Admin - auch das
// Lesen, der Lese-Benutzer bekommt das Panel gar nicht erst zu sehen.
// Jede Einstellung ist eine Auswahl aus festen Möglichkeiten; die Oberfläche
// baut ihre Auswahlfelder vollständig aus dieser Antwort.
if ($action === 'notify_list') {
    $settings = load_notify_settings($config);
    // [schlüssel => spec] + aktuelle Werte -> Liste für die Oberfläche. Werte
    // gehen als Text raus (ein <select> kennt nur Text).
    $optionList = static function (array $specs, array $values): array {
        $out = [];
        foreach ($specs as $key => $spec) {
            $choices = [];
            foreach ($spec['choices'] as $value => $text) {
                $choices[] = ['value' => (string)$value, 'text' => (string)$text];
            }
            $out[] = ['key' => $key, 'label' => $spec['label'],
                'value' => (string)($values[$key] ?? $spec['default']), 'choices' => $choices];
        }
        return $out;
    };
    $types = [];
    foreach (admin_notify_types($config) as $key => $def) {
        $types[] = [
            'key' => $key,
            'label' => $def['label'],
            'hint' => $def['hint'],
            'important' => $def['important'],
            'enabled' => $settings['types'][$key]['enabled'],
            'options' => $optionList($def['options'], $settings['types'][$key]),
        ];
    }
    json_response([
        'ok' => true,
        // Ohne Status-Key verschickt das NAS gar nichts - dann sind die
        // Einstellungen wirkungslos, und das Panel sagt das dazu.
        'configured' => trim((string)($config['bark_key_status'] ?? '')) !== '',
        'quiet_now' => notify_in_quiet_hours($settings['global']),
        'global' => $optionList(admin_notify_global_specs($config), $settings['global']),
        'types' => $types,
    ]);
}

// EINE Einstellung ändern: type = Art der Meldung oder 'global', key = die
// Einstellung ('enabled' = Art ein/aus), value = einer der angebotenen Werte.
// Ein unbekannter Wert wird ABGELEHNT (nie stillschweigend der Standard).
// In die Datei kommen nur ausdrücklich gesetzte Werte - alles andere folgt
// weiter den Standards (z.B. den Schwellen aus config.php).
if ($action === 'notify_set') {
    $type = (string)($_POST['type'] ?? '');
    $key = (string)($_POST['key'] ?? '');
    $value = (string)($_POST['value'] ?? '');
    $known = admin_notify_types($config);

    $spec = null;
    $scopeLabel = '';
    if ($type === 'global') {
        $spec = admin_notify_global_specs($config)[$key] ?? null;
        $scopeLabel = 'Alle Meldungen';
    } elseif (isset($known[$type])) {
        $spec = $key === 'enabled'
            ? ['label' => 'Meldung', 'choices' => [1 => 'eingeschaltet', 0 => 'abgeschaltet'], 'default' => 0]
            : ($known[$type]['options'][$key] ?? null);
        $scopeLabel = $known[$type]['label'];
    }
    if ($spec === null) {
        json_response(['ok' => false, 'message' => 'Unbekannte Einstellung.']);
    }
    $picked = null;
    foreach (array_keys($spec['choices']) as $choice) {
        if ((string)$choice === $value) {
            $picked = $choice;
            break;
        }
    }
    if ($picked === null) {
        json_response(['ok' => false, 'message' => 'Ungültiger Wert für „' . $spec['label'] . '“ – nichts geändert.']);
    }

    $result = with_lock($config, 'notify', function () use ($config, $type, $key, $picked, $spec, $scopeLabel): array {
        $current = load_notify_settings($config);
        $old = $type === 'global'
            ? $current['global'][$key]
            : ($key === 'enabled' ? ($current['types'][$type]['enabled'] ? 1 : 0) : $current['types'][$type][$key]);
        if ((string)$old === (string)$picked) {
            return ['ok' => true, 'changed' => false, 'message' => 'Ist bereits so eingestellt.'];
        }
        $path = data_path($config, 'notify_settings.json');
        $stored = read_json_file($path, []);
        $stored['global'] = is_array($stored['global'] ?? null) ? $stored['global'] : [];
        $stored['types'] = is_array($stored['types'] ?? null) ? $stored['types'] : [];
        if ($type === 'global') {
            $stored['global'][$key] = $picked;
        } else {
            $row = is_array($stored['types'][$type] ?? null) ? $stored['types'][$type] : [];
            $row[$key] = $key === 'enabled' ? ($picked === 1) : $picked;
            $stored['types'][$type] = $row;
        }
        if (!write_json_file($path, $stored)) {
            return ['ok' => false, 'message' => 'Einstellung konnte nicht gespeichert werden (data_dir prüfen).'];
        }
        $text = (string)$spec['choices'][$picked];
        if ($type !== 'global' && $key === 'enabled') {
            return ['ok' => true, 'changed' => true, 'value_text' => $text,
                'message' => $picked === 1
                    ? 'Eingeschaltet: „' . $scopeLabel . '“ wird dir gemeldet.'
                    : 'Abgeschaltet: „' . $scopeLabel . '“ wird dir nicht mehr gemeldet.'];
        }
        return ['ok' => true, 'changed' => true, 'value_text' => $text,
            'message' => 'Gespeichert – ' . $scopeLabel . ', ' . $spec['label'] . ': ' . $text . '.'];
    });
    if (!empty($result['ok']) && !empty($result['changed'])) {
        append_command_log($config, [
            'event' => 'notify_changed',
            'by' => $user,
            'type' => $type,
            'key' => $key,
            'label' => $scopeLabel,
            'option' => $spec['label'],
            'value' => (string)($result['value_text'] ?? ''),
        ]);
    }
    unset($result['value_text']);
    json_response($result);
}

// ==== Allgemeine Einstellungen (app_settings.json) ==========================
// Bisher nur: nach wie vielen Sekunden Stummgeschaltete automatisch wieder
// laut werden (mute_max_seconds). Nur Admin. Auswahl aus festen Stufen plus
// dem Wert aus config.php; ein anderer Wert wird abgelehnt.
if ($action === 'settings_list') {
    $default = mute_max_config_seconds($config);
    $choices = [];
    foreach (mute_max_choices($config) as $seconds) {
        $text = $seconds === 0 ? 'nie' : notify_duration_label($seconds);
        $choices[] = ['value' => $seconds, 'text' => $text . ($seconds === $default ? ' (Standard)' : '')];
    }
    json_response([
        'ok' => true,
        'settings' => ['mute_max_seconds' => [
            'value' => mute_max_seconds($config),
            'default' => $default,
            'choices' => $choices,
        ]],
    ]);
}

if ($action === 'settings_set') {
    $key = (string)($_POST['key'] ?? '');
    $raw = trim((string)($_POST['value'] ?? ''));
    if ($key !== 'mute_max_seconds') {
        json_response(['ok' => false, 'message' => 'Unbekannte Einstellung.']);
    }
    $picked = null;
    foreach (mute_max_choices($config) as $choice) {
        if ((string)$choice === $raw) {
            $picked = $choice;
            break;
        }
    }
    if ($picked === null) {
        json_response(['ok' => false, 'message' => 'Ungültiger Wert – nichts geändert.']);
    }
    $result = with_lock($config, 'settings', function () use ($config, $picked): array {
        $path = data_path($config, 'app_settings.json');
        $status = 'ok';
        $stored = read_json_file($path, [], $status);
        if (($stored['mute_max_seconds'] ?? null) === $picked) {
            return ['ok' => true, 'changed' => false, 'message' => 'Ist bereits so eingestellt.'];
        }
        $stored['mute_max_seconds'] = $picked;
        if (!write_json_file($path, $stored)) {
            return ['ok' => false, 'message' => 'Einstellung konnte nicht gespeichert werden (data_dir prüfen).'];
        }
        $text = $picked === 0 ? 'nie' : notify_duration_label($picked);
        return ['ok' => true, 'changed' => true, 'value_text' => $text,
            'message' => 'Gespeichert – Automatisch wieder laut nach: ' . $text . '.'];
    });
    if (!empty($result['ok']) && !empty($result['changed'])) {
        append_command_log($config, [
            'event' => 'setting_changed',
            'by' => $user,
            'label' => 'Automatisch wieder laut nach',
            'value' => (string)($result['value_text'] ?? ''),
        ]);
    }
    unset($result['value_text']);
    json_response($result);
}

// Probe-Meldung: schickt eine Beispiel-Meldung dieser Art genau so, wie sie
// nach den aktuellen Einstellungen ankäme (Dringlichkeit, Ruhezeit, Ton,
// Stellvertreter) - auch wenn die Art gerade abgeschaltet ist.
if ($action === 'notify_test') {
    $type = (string)($_POST['type'] ?? '');
    $known = admin_notify_types($config);
    if (!isset($known[$type])) {
        json_response(['ok' => false, 'message' => 'Unbekannte Art von Meldung.']);
    }
    $settings = load_notify_settings($config);
    // Bark-Texte ASCII - admin_notify_send() setzt Umlaute um. Die Antwort
    // nennt die Dringlichkeit, mit der WIRKLICH gesendet wurde ($used) - nicht
    // eine hier nachgerechnete, die von der Sendelogik abweichen könnte.
    $used = [];
    $sent = admin_notify_send($config, $type, 'PROBE: ' . $known[$type]['label'],
        date('[d.m.Y H:i] ') . 'Probe-Meldung aus dem Dashboard. So kommt diese Art von Meldung bei dir an.',
        $settings, true, $used);
    if ($sent === 'sent') {
        $level = (string)($used['level'] ?? '');
        json_response(['ok' => true, 'message' => 'Probe gesendet – Dringlichkeit: ' . (NOTIFY_LEVELS[$level] ?? $level)
            . (!empty($used['quiet_capped']) ? ' (gerade Ruhezeit, deshalb leise)' : '') . '.']);
    }
    json_response(['ok' => false, 'message' => $sent === 'off'
        ? 'Kein Status-Key (bark_key_status) in config.php eingetragen – es kann nichts gesendet werden.'
        : 'Probe FEHLGESCHLAGEN – Bark hat sie nicht angenommen (Status-Key und Verbindung prüfen).']);
}

// Test-Push an EINEN Empfänger: normale (nicht kritische) Mitteilung mit dem
// Alarmton - prüft beim Einrichten Key und Ton, ohne Demo-Modus und ohne
// echten Alarm (siehe bark_send_test). Ändert nichts an Liste oder Version
// und läuft deshalb ohne Lock.
if ($action === 'keys_test') {
    $id = (int)($_POST['id'] ?? 0);
    $state = load_alarm_keys($config);
    $idx = find_key_index($state, $id);
    if ($idx < 0) {
        json_response(['ok' => false, 'message' => 'Empfänger nicht gefunden.']);
    }
    if (!function_exists('curl_init')) {
        json_response(['ok' => false, 'message' => 'PHP-cURL fehlt. In Web Station im PHP-Profil die Erweiterung "curl" aktivieren.']);
    }
    $entry = $state['keys'][$idx];
    $label = (string)($entry['label'] ?? '');
    $sent = bark_send_test($config, trim((string)($entry['key'] ?? '')));
    log_key_event($config, 'key_test', $entry, $user, ['ok' => $sent['ok'], 'http' => $sent['http']]);
    if ($sent['ok']) {
        json_response(['ok' => true, 'message' => 'Test-Push an „' . $label . '“ gesendet – Bark hat ihn angenommen. '
            . 'Kommt er mit dem Alarmton an, ist das iPhone richtig eingerichtet.']);
    }
    $http = (int)$sent['http'];
    json_response(['ok' => false, 'message' => 'Test-Push an „' . $label . '“ FEHLGESCHLAGEN ('
        . ($http === 0 ? 'keine Verbindung zu Bark/Zeitüberschreitung' : 'HTTP ' . $http)
        . ($http >= 400 && $http < 500 ? ' – Bark-Key prüfen' : '') . ').']);
}

// Nachsendung nach einem Teilausfall: only_ids="3,5" beschränkt REAL ALARM
// bzw. ENTWARNUNG auf diese Empfänger-ids. Ist das Feld vorhanden, aber
// unbrauchbar, wird ABGELEHNT - nie stillschweigend an alle gesendet.
// null = Feld fehlt (normaler Versand an alle).
function requested_only_ids(): ?array
{
    if (!isset($_POST['only_ids'])) {
        return null;
    }
    $ids = [];
    // preg_match statt ctype_digit: Die ctype-Erweiterung ist im
    // Web-Station-Profil nicht garantiert (sonst Fatal Error mitten in der
    // Nachsendung).
    foreach (explode(',', is_string($_POST['only_ids']) ? $_POST['only_ids'] : '') as $part) {
        $part = trim($part);
        if (preg_match('/^[1-9][0-9]{0,8}$/', $part) !== 1) {
            $ids = [];
            break;
        }
        $ids[(int)$part] = (int)$part;
    }
    if (count($ids) === 0 || count($ids) > MAX_ALARM_KEYS) {
        http_response_code(400);
        json_response(['ok' => false, 'message' => 'Ungültige Empfänger-Auswahl für die Nachsendung – es wurde NICHTS gesendet.']);
    }
    return array_values($ids);
}

// Gemeinsamer Ablauf für ENTWARNUNG und REAL ALARM: EINE Momentaufnahme von
// Liste und Demo-Zustand, gegen die erst der bestätigte Modus geprüft und mit
// der dann gesendet wird (dazwischen liegen Log-Schreiben und bei einer
// Nachsendung die Suche im Verlauf - ein Moduswechsel aus einer zweiten
// Sitzung darf in diesem Fenster nicht durchschlagen). Start VOR dem Versand
// festhalten: Bricht der Request ab (Proxy-Timeout), sieht man im Verlauf
// trotzdem, dass gesendet wurde. started_at geht an den Browser zurück, der
// es bei einer Nachsendung als orig_at mitschickt (Zeitstempel im Text).
function run_nas_broadcast(array $config, string $user, string $event, callable $send): void
{
    $snapshot = ['keys' => load_alarm_keys($config), 'demo' => load_demo_mode($config)];
    require_expected_mode($config, $snapshot['demo']);
    $onlyIds = requested_only_ids();
    $resend = $onlyIds === null ? 0 : count($onlyIds);
    $origAt = $onlyIds === null ? 0 : max(0, (int)($_POST['orig_at'] ?? 0));
    $isDemo = demo_state_enabled($snapshot['demo']);
    $startedAt = time();
    append_command_log($config, ['event' => $event . '_started', 'by' => $user, 'resend' => $resend, 'demo' => $isDemo]);
    $result = $send($onlyIds, $snapshot, $origAt);
    append_command_log($config, [
        'event' => $event,
        'by' => $user,
        'ok' => $result['ok'],
        'message' => $result['message'],
        'results' => $result['results'],
        'resend' => $resend,
        'demo' => !empty($result['demo']) || $isDemo,
    ]);
    $result['started_at'] = $startedAt;
    json_response($result);
}

// ENTWARNUNG (Fehlalarm): Critical Alert mit kurzem, normalem Ton an alle
// Empfänger, dass der letzte Alarm ein Fehlalarm war. Bewusst ebenfalls
// critical - wer schon unterwegs ist, muss die Entwarnung sicher mitbekommen.
// Läuft wie der REAL ALARM direkt vom NAS (also auch bei toter ESP32-Kette).
if ($action === 'false_alarm') {
    run_nas_broadcast($config, $user, 'false_alarm',
        static fn(?array $onlyIds, array $snapshot, int $origAt): array =>
            bark_send_false_alarm_all($config, $onlyIds, $snapshot, $origAt));
}

// REAL ALARM: bewusst DIREKT vom NAS an Bark, ohne Umweg über den ESP32.
// Eine manuelle Alarmierung braucht man vor allem dann, wenn die ESP32-Kette
// gerade NICHT funktioniert - dann darf sie nicht am Poll des ESP32 hängen.
if ($action === 'alarm') {
    run_nas_broadcast($config, $user, 'nas_alarm',
        static fn(?array $onlyIds, array $snapshot, int $origAt): array =>
            bark_send_alarm_all($config, $onlyIds, $snapshot, $origAt));
}

http_response_code(400);
json_response(['ok' => false, 'message' => 'Unbekannte Aktion.']);
