<?php
require __DIR__ . '/../lib/common.php';

// Offline-Wächter: Der tägliche Heartbeat kommt vom ESP32 selbst - eine tote
// Box schweigt einfach. Das NAS weiß aber jederzeit, wie alt der letzte
// Status-Push ist. Dieses Skript (DSM-Aufgabenplaner, alle 15 Minuten) meldet
// per leiser Bark-Nachricht EINMAL, wenn die Box zu lange schweigt, und
// EINMAL, wenn sie wieder da ist. Zusätzlich erinnert es, wenn der
// Demo-Modus zu lange aktiv bleibt (echte Alarme erreichen dann nur den
// Test-Empfänger!), beendet überfällige Stummschaltungen (Arbeitsmodus,
// Sicherheitsnetz gegen hängengebliebene "Arbeit verlassen"-Automationen),
// erinnert max. 1x pro Tag, solange Empfänger stumm sind (bei DAUERHAFT
// stummen nur 1x pro Woche - die sind eine bewusste Einstellung und laufen
// nie von selbst aus, eine tägliche Meldung wäre nur Lärm), meldet, wenn der
// Außen-Wächter (Cloud-VM) verstummt, und protokolliert jeden Lauf
// (last_run_at), damit das Dashboard - und der Außen-Wächter - warnen kann,
// falls die DSM-Aufgabe nicht mehr läuft.
// Ohne 'bark_key_status' in config.php tut es nichts (bewusst: so kann es
// gefahrlos deployt werden).
//
// Aufruf: per CLI (Aufgabenplaner) ODER per HTTP-POST mit Maschinen-Token -
// ein anonymer Web-Aufruf wird von require_machine_auth() mit 403 abgewiesen.

$config = load_config();
if (PHP_SAPI !== 'cli') {
    require_machine_auth($config);
}

// Überfällige Stummschaltungen beenden - IMMER, auch ohne Status-Key: Das
// Sicherheitsnetz darf nicht davon abhängen, ob der Owner Meldungen bekommt.
// (Nimmt den 'keys'-Lock; die Info-Pushs an die Betroffenen laufen am Ende.)
expire_stale_mutes($config);

// Läuft der Wächter als root: Dateien, die das Dashboard an Ort und Stelle
// beschreibt (Verlauf, Lock-Dateien, Sicherungen), an den Besitzer des
// data_dir übergeben - auch solche, die ein früherer Lauf als root angelegt
// hat. fix_owner() tut nur etwas, wenn der Ordner einem Nicht-root-Benutzer
// gehört, und fasst die Rechte-Bits nie an (siehe dort).
foreach (inplace_data_files($config) as $inplaceFile) {
    fix_owner($inplaceFile);
}

$statusKey = trim((string)($config['bark_key_status'] ?? ''));
if ($statusKey === '') {
    text_response("DISABLED (bark_key_status fehlt in config.php; Stumm-Ablauf lief trotzdem)\n");
}
if (!function_exists('curl_init')) {
    // Ohne cURL kann der Wächter weder melden noch den Selbsttest machen -
    // das CLI-PHP der DSM-Aufgabe ist oft ein anderes als das der Web Station.
    text_response("NO_CURL (PHP-CLI ohne curl-Erweiterung - in der DSM-Aufgabe /usr/local/bin/php83 verwenden)\n");
}

$latest = read_json_file(data_path($config, 'latest_status.json'), []);
$seenAt = (int)($latest['seen_at'] ?? 0);
$age = $seenAt > 0 ? time() - $seenAt : null;

// Was der Admin gemeldet haben will und wie (Dashboard-Panel "Meldungen an
// den Admin"). Ohne gespeicherte Auswahl gilt das Verhalten von vorher.
$settings = load_notify_settings($config);

// Eigene, großzügigere Schwelle als die Dashboard-Anzeige (180 s): Der
// Wächter soll nicht bei jedem kurzen WLAN-Schluckauf eine Meldung schicken.
// Der Wert kommt aus den Einstellungen (Standard dort: der config.php-Wert
// watchdog_offline_after_seconds); nie kürzer als die Dashboard-Anzeige.
$threshold = max(
    (int)($config['offline_after_seconds'] ?? 180),
    (int)$settings['types']['box_offline']['offline_after_s']
);

// Selbsttest des Web-PHP VOR dem Lock (HTTP-Aufruf, bis zu 10 s - den
// Wächter-Lock nicht so lange halten). Siehe Abschnitt 0 unten.
$web = web_health_check($config);

$result = with_lock($config, 'watchdog', function () use ($config, $latest, $seenAt, $age, $threshold, $web, $settings): string {
    $types = $settings['types'];
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
    $suppressed = [];   // Arten, die in diesem Lauf fällig waren, aber abgeschaltet sind

    // Jede Meldung über diesen Helfer: Er merkt sich, ob Bark zuletzt
    // ANGENOMMEN hat. Sonst zeigte das Dashboard "Wächter läuft", obwohl
    // keine einzige Meldung ankommt (falscher Status-Key, Bark-4xx ...).
    // $type = Art der Meldung (siehe admin_notify_types()); Dringlichkeit,
    // Ruhezeit, Ton und Stellvertreter regelt admin_notify_send(). Hat der
    // Admin die Art im Dashboard abgeschaltet, wird NICHT gesendet, der Fall
    // gilt aber als erledigt (true): Einmal-Logik und Verlaufseintrag laufen
    // wie sonst weiter - es fehlt nur der Bark-Push. Der Sende-Zeitstempel
    // bleibt dabei unberührt (er sagt, ob Bark zuletzt wirklich angenommen hat).
    $notify = function (string $type, string $title, string $body) use ($config, $settings, &$state, &$suppressed): bool {
        $sent = admin_notify_send($config, $type, $title, $body, $settings);
        if ($sent === 'off') {
            $suppressed[$type] = true;
            return true;
        }
        $state[$sent === 'sent' ? 'last_send_ok_at' : 'last_send_failed_at'] = time();
        return $sent === 'sent';
    };

    // ==== 0) Kann das Dashboard selbst noch schreiben? ======================
    // Dieser Wächter läuft als root - fehlende Schreibrechte des Web-Benutzers
    // sieht er selbst nie. Deshalb fragt er api/health.php, das als 'http'
    // läuft. Gemeldet wird erst ab 2 Fehlschlägen in Folge (~30 min), damit
    // ein NAS-Neustart nicht gleich piept; danach Erinnerung alle 24 h, bis es
    // wieder geht (eine Einmal-Meldung ging im Oktober 2026 unter).
    $webFailCount = (int)($state['web_fail_count'] ?? 0);
    $webNotifiedAt = (int)($state['web_fail_notified_at'] ?? 0);
    if ($web['ok']) {
        if ($webNotifiedAt > 0) {
            $downMins = max(1, (int)round((time() - (int)($state['web_fail_since'] ?? time())) / 60));
            // "Wieder OK" lässt sich getrennt abwählen - und es entfällt auch,
            // wenn die DEFEKT-Meldung nie rausging (Art war abgeschaltet):
            // Dann wird nur der Zustand zurückgesetzt und der Verlaufseintrag
            // geschrieben.
            $skipPush = empty($types['nas_web']['recovery']) || !empty($state['web_push_pending']);
            $ok = $skipPush ? true : $notify('nas_web', 'NAS-Dashboard wieder OK',
                date('[d.m.Y H:i] ') . "Der Datenordner ist wieder beschreibbar (Stoerung ca. {$downMins} min).");
            if ($ok) {
                append_command_log($config, ['event' => 'nas_web_recovered', 'minutes' => $downMins]);
                $state['web_fail_count'] = 0;
                $state['web_fail_since'] = 0;
                $state['web_fail_notified_at'] = 0;
                $state['web_push_pending'] = false;
            }
            $out[] = $ok ? 'WEB_RECOVERED' : 'WEB_RECOVER_SEND_FAILED';
        } else {
            $state['web_fail_count'] = 0;
            $state['web_fail_since'] = 0;
            $out[] = 'WEB_OK';
        }
    } else {
        $webFailCount++;
        $state['web_fail_count'] = $webFailCount;
        if (empty($state['web_fail_since'])) {
            $state['web_fail_since'] = time();
        }
        // Erinnerungs-Abstand aus den Einstellungen (Standard 24 h, 0 = nur einmal).
        $webRepeat = (int)$types['nas_web']['repeat_hours'] * 3600;
        // Die Meldung war fällig, während die Art abgeschaltet war, und ist
        // inzwischen wieder eingeschaltet: sofort nachholen (wie bei der Box).
        $webCatchUp = !empty($state['web_push_pending']) && !empty($types['nas_web']['enabled']);
        if ($webFailCount >= 2 && ($webNotifiedAt === 0 || $webCatchUp
            || ($webRepeat > 0 && time() - $webNotifiedAt > $webRepeat))) {
            // Bark-Texte bewusst ASCII (keine Umlaute).
            // Ist nur EINE bestehende Datei betroffen (step=perm), stimmt die
            // große Aussage "Login und Status-Pushs scheitern" nicht - dann
            // genau sagen, was klemmt.
            $impact = ($web['step'] ?? '') === 'perm'
                ? '. Verlauf, Empfaenger-Aenderungen oder Stummschalten koennen dadurch scheitern. '
                    . 'Die Datei im Datenordner (fw_alarm_data) dem Web-Benutzer http zuordnen '
                    . 'bzw. ihm Lesen+Schreiben geben. '
                : '. Login und Status-Pushs der Box scheitern! '
                    . 'Meist fehlt der Gruppe http das Schreibrecht: DSM > File Station > web_packages > '
                    . 'fw_alarm_data > Eigenschaften > Berechtigung > http Lesen+Schreiben. ';
            $ok = $notify('nas_web', 'NAS-Dashboard DEFEKT',
                date('[d.m.Y H:i] ') . $web['detail'] . $impact
                . 'Bark-Alarme der Box laufen davon unabhaengig weiter.');
            if ($ok) {
                if ($webNotifiedAt === 0) {
                    append_command_log($config, ['event' => 'nas_web_failed', 'detail' => $web['detail']]);
                }
                $state['web_fail_notified_at'] = time();
                $state['web_push_pending'] = isset($suppressed['nas_web']);
            }
            $out[] = $ok ? 'WEB_FAIL_NOTIFIED' : 'WEB_FAIL_SEND_FAILED';
        } else {
            $out[] = 'WEB_FAIL (' . $webFailCount . ') ' . $web['detail'];
        }
    }

    // ==== 1) Box offline / wieder da ========================================
    if ($seenAt <= 0) {
        // Die Box hat sich noch NIE gemeldet (z.B. vor dem ersten Flash) -
        // nichts zu überwachen, aber der Lauf zählt trotzdem als Lebenszeichen.
        $out[] = 'NO_DATA (noch nie ein Status-Push angekommen)';
    } else {
        $notified = !empty($state['offline_notified']);
        // Solange seit der OFFLINE-Meldung KEIN neuer Status-Push kam, bleibt
        // die Box offline - auch wenn die Schwelle inzwischen im Dashboard
        // hochgesetzt wurde. Sonst käme ein falsches "wieder ONLINE", obwohl
        // sich die Box gar nicht gemeldet hat.
        $noNewPush = $notified
            && $seenAt <= (int)($state['offline_seen_at'] ?? ($state['offline_since'] ?? 0));
        $offline = $age > $threshold || $noNewPush;
        $mins = max(1, (int)round($age / 60));
        $duration = $mins >= 120 ? (int)round($mins / 60) . ' Std' : $mins . ' min';
        // Bark-Texte bewusst ASCII (keine Umlaute).
        // Scheitert gleichzeitig der Selbsttest, kann die Box ihre Pushs
        // gar nicht ablegen - dann NICHT zur Box schicken (Oktober 2026:
        // die Box lief, das NAS war kaputt, die Meldung führte in die Irre).
        // Nur wenn die Störung die Status-Pushs wirklich blockiert - eine
        // einzelne fremde Verlaufsdatei macht die Box nicht "offline".
        // Gilt für die erste Meldung UND für jede Erinnerung.
        $offlineBody = empty($web['blocks_pushes'])
            ? "Kein Status vom ESP32 seit {$duration}. "
                . 'Strom/WLAN/Box pruefen! Der Relaisalarm wuerde gerade NICHT ausgeloest. '
                . '(Manueller REAL ALARM ueber das Dashboard geht weiterhin.)'
            : "Kein Status vom ESP32 seit {$duration} - Ursache ist aber vermutlich das NAS, "
                . 'nicht die Box: ' . $web['detail'] . '. '
                . 'Kommen die taeglichen Bark-Heartbeats der Box noch an, laeuft sie normal.';
        $offlineRepeat = (int)$types['box_offline']['repeat_hours'] * 3600;
        // Die OFFLINE-Meldung war fällig, während die Art im Dashboard
        // abgeschaltet war (kein Push), und ist inzwischen wieder
        // eingeschaltet: dann jetzt nachholen - sonst erführe der Admin von
        // einem noch laufenden Ausfall erst durch das spätere "wieder ONLINE".
        $catchUp = !empty($state['offline_push_pending']) && !empty($types['box_offline']['enabled']);
        if ($offline && !$notified) {
            $ok = $notify('box_offline', 'Alarm-Box OFFLINE', date('[d.m.Y H:i] ') . $offlineBody);
            if ($ok) {
                $state['offline_notified'] = true;
                $state['offline_reminded_at'] = time();
                $state['offline_since'] = time() - $age;
                $state['offline_seen_at'] = $seenAt;
                $state['offline_push_pending'] = isset($suppressed['box_offline']);
                // Ausfall auch im Verlauf festhalten (nicht nur als flüchtiger
                // Bark-Push) - gleiche Einmal-Semantik wie die Benachrichtigung.
                append_command_log($config, ['event' => 'box_offline', 'minutes' => $mins]);
            }
            // Bei Sendefehler Flag nicht setzen: nächster Lauf versucht es erneut.
            $out[] = $ok ? 'NOTIFIED_OFFLINE' : 'SEND_FAILED';
        } elseif ($offline && $notified && ($catchUp
            || ($offlineRepeat > 0 && time() - (int)($state['offline_reminded_at'] ?? 0) > $offlineRepeat))) {
            // Nachholen (siehe $catchUp) bzw. auf Wunsch erinnern, solange die
            // Box offline bleibt (Standard: nur die eine Meldung - eine
            // einzelne geht leicht unter).
            $ok = $notify('box_offline', $catchUp ? 'Alarm-Box OFFLINE' : 'Alarm-Box weiterhin OFFLINE',
                date('[d.m.Y H:i] ') . $offlineBody);
            if ($ok) {
                $state['offline_reminded_at'] = time();
                $state['offline_push_pending'] = isset($suppressed['box_offline']);
            }
            $out[] = $ok ? ($catchUp ? 'NOTIFIED_OFFLINE (nachgeholt)' : 'OFFLINE_REMINDED') : 'SEND_FAILED';
        } elseif (!$offline && $notified) {
            $downMins = max(1, (int)round((time() - (int)($state['offline_since'] ?? time())) / 60));
            // "Wieder online" lässt sich getrennt abwählen. Auch wenn die
            // OFFLINE-Meldung nie rausging (Art war abgeschaltet), gibt es
            // kein "wieder da" - dann nur Zustand zurücksetzen und den
            // Verlaufseintrag schreiben.
            $skipPush = empty($types['box_offline']['recovery']) || !empty($state['offline_push_pending']);
            $ok = $skipPush ? true : $notify('box_offline', 'Alarm-Box wieder ONLINE',
                date('[d.m.Y H:i] ') . "ESP32 meldet sich wieder (Ausfall ca. {$downMins} min).");
            if ($ok) {
                $state['offline_notified'] = false;
                $state['offline_since'] = 0;
                $state['offline_seen_at'] = 0;
                $state['offline_push_pending'] = false;
                append_command_log($config, ['event' => 'box_recovered', 'minutes' => $downMins]);
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
        // Beide Abstände aus den Einstellungen (Standard: demo_reminder_after_seconds
        // aus config.php bzw. 24 h).
        $remindAfter = (int)$types['demo_reminder']['after_s'];
        $demoRepeat = (int)$types['demo_reminder']['repeat_hours'] * 3600;
        // Die Erinnerung gehört zur jeweiligen Demo-Sitzung: Wurde der Modus
        // seit der letzten Erinnerung neu eingeschaltet (auch innerhalb von 15
        // min aus und wieder an), zählt die neue Sitzung von vorn.
        if ((int)($state['demo_reminded_for'] ?? 0) !== $since) {
            $state['demo_reminded_at'] = 0;
            $state['demo_reminded_for'] = $since;
        }
        $lastReminded = (int)($state['demo_reminded_at'] ?? 0);
        if ($since > 0 && time() - $since > $remindAfter && time() - $lastReminded > $demoRepeat) {
            $hours = max(1, (int)round((time() - $since) / 3600));
            $label = (string)($demo['label'] ?? 'Test-Empfaenger');
            $ok = $notify('demo_reminder', 'Demo-Modus noch aktiv!',
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

    // ==== 3) Arbeitsmodus: Erinnerung an Stummschaltungen ===================
    // Überfällige Stummschaltungen hat expire_stale_mutes() schon ganz oben
    // (vor dem Lock) beendet. Solange danach noch jemand stumm ist, den Owner
    // max. 1x pro Tag leise erinnern - stumme Empfänger bekommen echte Alarme
    // nur OHNE Ton. Namen werden in bark_send_status() auf ASCII umgesetzt.
    $mutedLabels = [];
    $permanentLabels = [];
    foreach ((load_alarm_keys($config)['keys'] ?? []) as $entry) {
        if (empty($entry['muted'])) {
            continue;
        }
        // Dauerhaft Stumme getrennt zählen: Sie sind eine bewusste
        // Entscheidung im Dashboard und laufen nie von selbst aus - eine
        // TÄGLICHE Erinnerung dafür wäre nur Lärm, den man wegklickt.
        if (mute_is_permanent($entry)) {
            $permanentLabels[] = (string)($entry['label'] ?? '?');
        } else {
            $mutedLabels[] = (string)($entry['label'] ?? '?');
        }
    }
    if (count($mutedLabels) > 0) {
        $lastReminded = (int)($state['mute_reminded_at'] ?? 0);
        if (time() - $lastReminded > (int)$types['mute_reminder']['repeat_hours'] * 3600) {
            $ok = $notify('mute_reminder', 'Empfaenger stummgeschaltet',
                date('[d.m.Y H:i] ') . count($mutedLabels) . ' Empfaenger im Arbeitsmodus (Alarme ohne Ton): '
                . implode(', ', $mutedLabels) . '.');
            if ($ok) {
                $state['mute_reminded_at'] = time();
            }
            $out[] = $ok ? 'MUTE_REMINDED' : 'MUTE_REMIND_FAILED';
        } else {
            $out[] = 'MUTED_ACTIVE (' . count($mutedLabels) . ')';
        }
    } elseif (!empty($state['mute_reminded_at'])) {
        $state['mute_reminded_at'] = 0;   // alle wieder laut: Erinnerung zurücksetzen
    }

    // ==== 4) Dauerhaft Stumme: seltene Erinnerung (max. 1x pro Woche) ======
    // Ganz verschweigen wäre falsch - wer seit Monaten lautlos ist, wurde
    // vielleicht schlicht vergessen. Einmal pro Woche reicht dafür.
    if (count($permanentLabels) > 0) {
        $lastReminded = (int)($state['permanent_mute_reminded_at'] ?? 0);
        if (time() - $lastReminded > (int)$types['permanent_mute_reminder']['repeat_days'] * 86400) {
            $ok = $notify('permanent_mute_reminder', 'Dauerhaft stummgeschaltet',
                date('[d.m.Y H:i] ') . count($permanentLabels)
                . ' Empfaenger sind DAUERHAFT stumm (Alarme kommen dort ohne Ton an, '
                . 'die automatische Rueckschaltung ist fuer sie abgeschaltet): '
                . implode(', ', $permanentLabels) . '. Noch aktuell? Sonst im Dashboard beenden.');
            if ($ok) {
                $state['permanent_mute_reminded_at'] = time();
            }
            $out[] = $ok ? 'PERMANENT_MUTE_REMINDED' : 'PERMANENT_MUTE_REMIND_FAILED';
        } else {
            $out[] = 'PERMANENT_MUTED (' . count($permanentLabels) . ')';
        }
    } elseif (!empty($state['permanent_mute_reminded_at'])) {
        $state['permanent_mute_reminded_at'] = 0;   // keiner mehr dauerhaft stumm
    }

    // ==== 5) Auffälligkeiten an der Box (nur wenn eingeschaltet) ============
    // Schwaches WLAN, knapper Speicher oder ein gescheiterter Heartbeat-Versand
    // sind Vorboten eines Ausfalls. Gleiche Schwellen wie die Warnfarben im
    // Dashboard (app.js: weakWifi, HEAP_WARN, MAX_ALLOC_WARN, heartbeatFailed) -
    // bei Änderungen BEIDE Stellen anpassen. Gemeldet wird erst, wenn die
    // Auffälligkeit in ZWEI Läufen in
    // Folge besteht (ein einzelner schwacher Messwert ist normal), und
    // höchstens im eingestellten Abstand. Nur bei frischem Status - die Werte
    // einer Box, die offline ist, sagen nichts mehr.
    if (!empty($types['box_health']['enabled']) && $seenAt > 0 && $age <= $threshold) {
        $issues = [];
        $rssi = (int)($latest['rssi'] ?? 0);
        if ($rssi < 0 && $rssi <= -80) {
            $issues[] = "WLAN schwach ({$rssi} dBm)";
        }
        $heap = (int)($latest['free_heap'] ?? 0);
        if ($heap > 0 && $heap < 50000) {
            $issues[] = 'wenig freier Speicher (' . (int)round($heap / 1024) . ' kB)';
        }
        // Größter zusammenhängender freier Block (nur neuere Firmware): Ein
        // zerstückelter Heap lässt HTTPS scheitern, obwohl in Summe genug frei ist.
        $maxAlloc = (int)($latest['max_alloc_heap'] ?? 0);
        if ($maxAlloc > 0 && $maxAlloc < 30000) {
            $issues[] = 'Speicher zerstueckelt (groesster Block ' . (int)round($maxAlloc / 1024) . ' kB)';
        }
        // heartbeat_ok=0 heißt nur dann "gescheitert", wenn es schon einen
        // Versuch gab: Nach einem Neustart meldet die Box 0 und "nie", bis der
        // erste Heartbeat fällig ist - das ist kein Fehler.
        $lastHeartbeat = trim((string)($latest['last_heartbeat'] ?? ''));
        if ((string)($latest['heartbeat_ok'] ?? '1') === '0' && $lastHeartbeat !== '' && $lastHeartbeat !== 'nie') {
            $issues[] = 'der taegliche Heartbeat der Box konnte nicht gesendet werden';
        }
        if (count($issues) > 0) {
            $state['health_bad_runs'] = (int)($state['health_bad_runs'] ?? 0) + 1;
            $healthRepeat = max(1, (int)$types['box_health']['repeat_hours']) * 3600;
            if ($state['health_bad_runs'] >= 2 && time() - (int)($state['health_notified_at'] ?? 0) > $healthRepeat) {
                $ok = $notify('box_health', 'Alarm-Box: Auffaelligkeit',
                    date('[d.m.Y H:i] ') . ucfirst(implode('; ', $issues))
                    . '. Details im Dashboard unter "Status der Box".');
                if ($ok) {
                    $state['health_notified_at'] = time();
                }
                $out[] = $ok ? 'HEALTH_NOTIFIED' : 'HEALTH_SEND_FAILED';
            } else {
                $out[] = 'HEALTH_ISSUES (' . count($issues) . ')';
            }
        } else {
            $state['health_bad_runs'] = 0;
        }
    }

    // ==== 6) Wochenbericht (nur wenn eingeschaltet) =========================
    // Am gewählten Wochentag ab der gewählten Stunde, höchstens einmal pro
    // Woche: kurze Zusammenfassung aus dem Verlauf. Fällt der Lauf an dem Tag
    // aus (NAS aus), entfällt der Bericht dieser Woche.
    if (!empty($types['weekly_report']['enabled'])
        && (int)date('N') === (int)$types['weekly_report']['weekday']
        && (int)date('G') >= (int)$types['weekly_report']['hour']
        && time() - (int)($state['weekly_report_at'] ?? 0) > 6 * 86400) {
        $boxOnline = $seenAt > 0 && $age <= $threshold;
        $ok = $notify('weekly_report', 'Alarm-Waechter: Wochenbericht',
            date('[d.m.Y H:i] ') . weekly_report_text($config, $boxOnline));
        if ($ok) {
            $state['weekly_report_at'] = time();
        }
        $out[] = $ok ? 'WEEKLY_REPORT' : 'WEEKLY_REPORT_FAILED';
    }

    // ==== 7) Außen-Wächter (Cloud-VM) verstummt? ===========================
    // Die VM prüft das NAS alle 5 min von außen (api/alive.php merkt sich jeden
    // Aufruf in watcher_seen.json). Fällt SIE aus - Oracle stoppt kostenlose
    // VMs, die längere Zeit "idle" sind -, würde ein Ausfall des NAS selbst
    // nicht mehr gemeldet. Deshalb überwachen sich beide gegenseitig. Gleiche
    // Regeln wie bei der Box: "wieder da" erst nach einem NEUEN Aufruf, eine
    // abgeschaltet fällige Meldung wird nach dem Einschalten nachgeholt.
    if (isset($types['outside_watcher'])) {
        $wt = $types['outside_watcher'];
        $watcherSeen = (int)(read_json_file(data_path($config, 'watcher_seen.json'), [])['seen_at'] ?? 0);
        // Hat sich die VM noch NIE gemeldet, zählt die Frist ab dem ersten
        // Lauf, der den eingerichteten Wächter gesehen hat.
        if ($watcherSeen === 0 && empty($state['watcher_watch_since'])) {
            $state['watcher_watch_since'] = time();
        }
        $watcherRef = $watcherSeen > 0 ? $watcherSeen : (int)$state['watcher_watch_since'];
        $watcherNotifiedAt = (int)($state['watcher_notified_at'] ?? 0);
        $watcherSilent = time() - $watcherRef > (int)$wt['silent_after_s']
            || ($watcherNotifiedAt > 0 && $watcherSeen <= (int)($state['watcher_silent_seen_at'] ?? 0));
        $watcherMins = max(1, (int)round((time() - $watcherRef) / 60));
        if ($watcherSilent) {
            $watcherRepeat = (int)$wt['repeat_hours'] * 3600;
            $watcherCatchUp = !empty($state['watcher_push_pending']) && !empty($wt['enabled']);
            if ($watcherNotifiedAt === 0 || $watcherCatchUp
                || ($watcherRepeat > 0 && time() - $watcherNotifiedAt > $watcherRepeat)) {
                $since = $watcherSeen > 0
                    ? 'seit ' . ($watcherMins >= 120 ? (int)round($watcherMins / 60) . ' Std' : $watcherMins . ' min') . ' nicht'
                    : 'noch nie';
                // Bark-Texte bewusst ASCII (keine Umlaute).
                $ok = $notify('outside_watcher',
                    ($watcherNotifiedAt > 0 && !$watcherCatchUp) ? 'Aussen-Waechter weiterhin stumm' : 'Aussen-Waechter meldet sich nicht',
                    date('[d.m.Y H:i] ') . "Die Cloud-VM hat sich {$since} beim NAS gemeldet. "
                    . 'Ein Ausfall des NAS selbst (Strom/Internet am Standort) wuerde so NICHT gemeldet. '
                    . 'Oracle stoppt kostenlose VMs, die laenger idle sind: Oracle-Konsole > Compute > '
                    . 'Instances > fw-alarm-watch pruefen und ggf. starten.');
                if ($ok) {
                    if ($watcherNotifiedAt === 0) {
                        append_command_log($config, ['event' => 'watcher_silent', 'minutes' => $watcherMins]);
                        $state['watcher_silent_seen_at'] = $watcherSeen;
                    }
                    $state['watcher_notified_at'] = time();
                    $state['watcher_push_pending'] = isset($suppressed['outside_watcher']);
                }
                $out[] = $ok ? 'WATCHER_SILENT_NOTIFIED' : 'WATCHER_SEND_FAILED';
            } else {
                $out[] = 'WATCHER_SILENT (bereits gemeldet)';
            }
        } elseif ($watcherNotifiedAt > 0) {
            // Ging die Stumm-Meldung nie raus (Art war abgeschaltet), gibt es
            // auch kein "wieder da" - nur Zustand zurücksetzen und Verlauf.
            $skipPush = empty($wt['recovery']) || !empty($state['watcher_push_pending']);
            // Pause ab dem letzten Aufruf VOR der Stille (hatte sich die VM
            // nie gemeldet: ab Beginn der Überwachung).
            $silentFrom = (int)($state['watcher_silent_seen_at'] ?? 0) ?: (int)($state['watcher_watch_since'] ?? time());
            $downMins = max(1, (int)round((time() - $silentFrom) / 60));
            $ok = $skipPush ? true : $notify('outside_watcher', 'Aussen-Waechter wieder da',
                date('[d.m.Y H:i] ') . "Die Cloud-VM meldet sich wieder beim NAS (Pause ca. {$downMins} min).");
            if ($ok) {
                append_command_log($config, ['event' => 'watcher_recovered', 'minutes' => $downMins]);
                $state['watcher_notified_at'] = 0;
                $state['watcher_silent_seen_at'] = 0;
                $state['watcher_push_pending'] = false;
            }
            $out[] = $ok ? 'WATCHER_RECOVERED' : 'WATCHER_SEND_FAILED';
        } else {
            $out[] = 'WATCHER_OK';
        }
    }

    // Zustand konnte nicht gespeichert werden (z.B. volle Platte)? Melden -
    // sonst blieben Zähler wie web_fail_count ewig bei 1 ("DEFEKT" käme nie),
    // und OFFLINE würde alle 15 min erneut gemeldet.
    if (!write_json_file($path, $state)) {
        $out[] = 'STATE_WRITE_FAILED (watchdog_state.json nicht speicherbar)';
    }
    // Im Protokoll der DSM-Aufgabe sichtbar machen, dass eine fällige Meldung
    // bewusst NICHT gesendet wurde (sonst sähe "NOTIFIED_OFFLINE" nach Push aus).
    if (count($suppressed) > 0) {
        $out[] = 'OHNE_PUSH (im Dashboard abgeschaltet): ' . implode(', ', array_keys($suppressed));
    }
    return implode(' | ', $out) . "\n";
});

text_response($result);
