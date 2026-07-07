<?php
require __DIR__ . '/../lib/common.php';

// Offline-Wächter: Der tägliche Heartbeat kommt vom ESP32 selbst - eine tote
// Box schweigt einfach. Das NAS weiß aber jederzeit, wie alt der letzte
// Status-Push ist. Dieses Skript (DSM-Aufgabenplaner, alle 5 Minuten) meldet
// per leiser Bark-Nachricht EINMAL, wenn die Box zu lange schweigt, und
// EINMAL, wenn sie wieder da ist. Ohne 'bark_key_status' in config.php tut
// es nichts (bewusst: so kann es gefahrlos deployt werden).
//
// Aufruf: per CLI (Aufgabenplaner) ODER per HTTP-POST mit Maschinen-Token -
// ein anonymer Web-Aufruf wird von require_machine_auth() mit 403 abgewiesen.

$config = load_config();
if (PHP_SAPI !== 'cli') {
    require_machine_auth($config);
}

$statusKey = trim((string)($config['bark_key_status'] ?? ''));
if ($statusKey === '') {
    text_response("DISABLED (bark_key_status fehlt in config.php)\n");
}

$latest = read_json_file(data_path($config, 'latest_status.json'), []);
$seenAt = (int)($latest['seen_at'] ?? 0);
if ($seenAt <= 0) {
    // Die Box hat sich noch NIE gemeldet (z.B. vor dem ersten Flash) -
    // dann gibt es nichts zu überwachen und auch nichts zu melden.
    text_response("NO_DATA (noch nie ein Status-Push angekommen)\n");
}
$age = time() - $seenAt;

// Eigene, großzügigere Schwelle als die Dashboard-Anzeige (180 s): Der
// Wächter soll nicht bei jedem kurzen WLAN-Schluckauf eine Meldung schicken.
$threshold = max(
    (int)($config['offline_after_seconds'] ?? 180),
    (int)($config['watchdog_offline_after_seconds'] ?? 600)
);
$offline = $age > $threshold;

$result = with_lock($config, 'watchdog', function () use ($config, $statusKey, $age, $offline): string {
    $path = data_path($config, 'watchdog_state.json');
    $state = read_json_file($path, ['offline_notified' => false, 'offline_since' => 0]);
    $notified = !empty($state['offline_notified']);

    if ($offline && !$notified) {
        $mins = max(1, (int)round($age / 60));
        // Bark-Texte bewusst ASCII (keine Umlaute).
        $ok = bark_send_status($config, $statusKey, 'Alarm-Box OFFLINE',
            date('[d.m.Y H:i] ') . "Kein Status vom ESP32 seit {$mins} min. "
            . 'Strom/WLAN/Box pruefen! Der Relaisalarm wuerde gerade NICHT ausgeloest. '
            . '(Manueller REAL ALARM ueber das Dashboard geht weiterhin.)');
        if ($ok) {
            $state['offline_notified'] = true;
            $state['offline_since'] = time() - $age;
            write_json_file($path, $state);
        }
        // Bei Sendefehler nichts persistieren: nächster Lauf versucht es erneut.
        return $ok ? "NOTIFIED_OFFLINE\n" : "SEND_FAILED\n";
    }

    if (!$offline && $notified) {
        $downMins = max(1, (int)round((time() - (int)($state['offline_since'] ?? time())) / 60));
        $ok = bark_send_status($config, $statusKey, 'Alarm-Box wieder ONLINE',
            date('[d.m.Y H:i] ') . "ESP32 meldet sich wieder (Ausfall ca. {$downMins} min).");
        if ($ok) {
            $state['offline_notified'] = false;
            $state['offline_since'] = 0;
            write_json_file($path, $state);
        }
        return $ok ? "NOTIFIED_RECOVERED\n" : "SEND_FAILED\n";
    }

    return $offline ? "STILL_OFFLINE (bereits gemeldet)\n" : "OK\n";
});

text_response($result);
