<?php
require __DIR__ . '/../lib/common.php';

// Offline-Wächter: Der tägliche Heartbeat kommt vom ESP32 selbst - eine tote
// Box schweigt einfach. Das NAS weiß aber jederzeit, wie alt der letzte
// Status-Push ist. Dieses Skript (DSM-Aufgabenplaner, alle 5 Minuten) meldet
// per leiser Bark-Nachricht EINMAL, wenn die Box zu lange schweigt, und
// EINMAL, wenn sie wieder da ist. Zusätzlich erinnert es, wenn der
// Demo-Modus zu lange aktiv bleibt (echte Alarme erreichen dann nur den
// Test-Empfänger!), und protokolliert jeden Lauf (last_run_at), damit das
// Dashboard warnen kann, falls die DSM-Aufgabe nicht mehr läuft.
// Ohne 'bark_key_status' in config.php tut es nichts (bewusst: so kann es
// gefahrlos deployt werden).
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
$age = $seenAt > 0 ? time() - $seenAt : null;

// Eigene, großzügigere Schwelle als die Dashboard-Anzeige (180 s): Der
// Wächter soll nicht bei jedem kurzen WLAN-Schluckauf eine Meldung schicken.
$threshold = max(
    (int)($config['offline_after_seconds'] ?? 180),
    (int)($config['watchdog_offline_after_seconds'] ?? 600)
);

$result = with_lock($config, 'watchdog', function () use ($config, $statusKey, $seenAt, $age, $threshold): string {
    $path = data_path($config, 'watchdog_state.json');
    $state = read_json_file($path, [
        'offline_notified' => false,
        'offline_since' => 0,
        'demo_reminded_at' => 0,
    ]);
    // Jeden Lauf festhalten: Das Dashboard zeigt daraus "Wächter zuletzt
    // gelaufen vor X min" und warnt, wenn die DSM-Aufgabe nicht mehr läuft.
    $state['last_run_at'] = time();
    $out = [];

    // ==== 1) Box offline / wieder da ========================================
    if ($seenAt <= 0) {
        // Die Box hat sich noch NIE gemeldet (z.B. vor dem ersten Flash) -
        // nichts zu überwachen, aber der Lauf zählt trotzdem als Lebenszeichen.
        $out[] = 'NO_DATA (noch nie ein Status-Push angekommen)';
    } else {
        $offline = $age > $threshold;
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
            }
            // Bei Sendefehler Flag nicht setzen: nächster Lauf versucht es erneut.
            $out[] = $ok ? 'NOTIFIED_OFFLINE' : 'SEND_FAILED';
        } elseif (!$offline && $notified) {
            $downMins = max(1, (int)round((time() - (int)($state['offline_since'] ?? time())) / 60));
            $ok = bark_send_status($config, $statusKey, 'Alarm-Box wieder ONLINE',
                date('[d.m.Y H:i] ') . "ESP32 meldet sich wieder (Ausfall ca. {$downMins} min).");
            if ($ok) {
                $state['offline_notified'] = false;
                $state['offline_since'] = 0;
            }
            $out[] = $ok ? 'NOTIFIED_RECOVERED' : 'SEND_FAILED';
        } else {
            $out[] = $offline ? 'STILL_OFFLINE (bereits gemeldet)' : 'OK';
        }
    }

    // ==== 2) Demo-Modus vergessen? ==========================================
    // Im Demo-Modus erreichen ECHTE Alarme nur den Test-Empfänger. Das Banner
    // sieht nur, wer das Dashboard offen hat - deshalb nach einer Weile leise
    // erinnern, danach höchstens 1x pro Tag, bis wieder LIVE geschaltet ist.
    $demo = load_demo_mode($config);
    if (!empty($demo['enabled'])) {
        $since = (int)($demo['changed_at'] ?? 0);
        $remindAfter = (int)($config['demo_reminder_after_seconds'] ?? 14400);   // 4 h
        $lastReminded = (int)($state['demo_reminded_at'] ?? 0);
        if ($since > 0 && time() - $since > $remindAfter && time() - $lastReminded > 86400) {
            $hours = max(1, (int)round((time() - $since) / 3600));
            $label = (string)($demo['label'] ?? 'Test-Empfaenger');
            $ok = bark_send_status($config, $statusKey, 'Demo-Modus noch aktiv!',
                date('[d.m.Y H:i] ') . "Der Demo-Modus laeuft seit ca. {$hours} Std - "
                . "ALLE Alarme gehen nur an '{$label}'. Im Dashboard zurueck auf LIVE schalten!");
            if ($ok) {
                $state['demo_reminded_at'] = time();
            }
            $out[] = $ok ? 'DEMO_REMINDED' : 'DEMO_REMIND_FAILED';
        } else {
            $out[] = 'DEMO_ACTIVE';
        }
    } elseif (!empty($state['demo_reminded_at'])) {
        $state['demo_reminded_at'] = 0;   // wieder LIVE: Erinnerung zurücksetzen
    }

    write_json_file($path, $state);
    return implode(' | ', $out) . "\n";
});

text_response($result);
