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
// nie von selbst aus, eine tägliche Meldung wäre nur Lärm), und protokolliert
// jeden Lauf (last_run_at), damit das Dashboard warnen kann, falls die
// DSM-Aufgabe nicht mehr läuft.
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

// Eigene, großzügigere Schwelle als die Dashboard-Anzeige (180 s): Der
// Wächter soll nicht bei jedem kurzen WLAN-Schluckauf eine Meldung schicken.
$threshold = max(
    (int)($config['offline_after_seconds'] ?? 180),
    (int)($config['watchdog_offline_after_seconds'] ?? 600)
);

// Selbsttest des Web-PHP VOR dem Lock (HTTP-Aufruf, bis zu 10 s - den
// Wächter-Lock nicht so lange halten). Siehe Abschnitt 0 unten.
$web = web_health_check($config);

$result = with_lock($config, 'watchdog', function () use ($config, $statusKey, $seenAt, $age, $threshold, $web): string {
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

    // Jede Meldung über diesen Helfer: Er merkt sich, ob Bark zuletzt
    // ANGENOMMEN hat. Sonst zeigte das Dashboard "Wächter läuft", obwohl
    // keine einzige Meldung ankommt (falscher Status-Key, Bark-4xx ...).
    $notify = function (string $title, string $body) use ($config, $statusKey, &$state): bool {
        $ok = bark_send_status($config, $statusKey, $title, $body);
        $state[$ok ? 'last_send_ok_at' : 'last_send_failed_at'] = time();
        return $ok;
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
            $ok = $notify('NAS-Dashboard wieder OK',
                date('[d.m.Y H:i] ') . "Der Datenordner ist wieder beschreibbar (Stoerung ca. {$downMins} min).");
            if ($ok) {
                append_command_log($config, ['event' => 'nas_web_recovered', 'minutes' => $downMins]);
                $state['web_fail_count'] = 0;
                $state['web_fail_since'] = 0;
                $state['web_fail_notified_at'] = 0;
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
        if ($webFailCount >= 2 && ($webNotifiedAt === 0 || time() - $webNotifiedAt > 86400)) {
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
            $ok = $notify('NAS-Dashboard DEFEKT',
                date('[d.m.Y H:i] ') . $web['detail'] . $impact
                . 'Bark-Alarme der Box laufen davon unabhaengig weiter.');
            if ($ok) {
                if ($webNotifiedAt === 0) {
                    append_command_log($config, ['event' => 'nas_web_failed', 'detail' => $web['detail']]);
                }
                $state['web_fail_notified_at'] = time();
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
        $offline = $age > $threshold;
        $notified = !empty($state['offline_notified']);
        if ($offline && !$notified) {
            $mins = max(1, (int)round($age / 60));
            // Bark-Texte bewusst ASCII (keine Umlaute).
            // Scheitert gleichzeitig der Selbsttest, kann die Box ihre Pushs
            // gar nicht ablegen - dann NICHT zur Box schicken (Oktober 2026:
            // die Box lief, das NAS war kaputt, die Meldung führte in die Irre).
            // Nur wenn die Störung die Status-Pushs wirklich blockiert - eine
            // einzelne fremde Verlaufsdatei macht die Box nicht "offline".
            $body = empty($web['blocks_pushes'])
                ? "Kein Status vom ESP32 seit {$mins} min. "
                    . 'Strom/WLAN/Box pruefen! Der Relaisalarm wuerde gerade NICHT ausgeloest. '
                    . '(Manueller REAL ALARM ueber das Dashboard geht weiterhin.)'
                : "Kein Status vom ESP32 seit {$mins} min - Ursache ist aber vermutlich das NAS, "
                    . 'nicht die Box: ' . $web['detail'] . '. '
                    . 'Kommen die taeglichen Bark-Heartbeats der Box noch an, laeuft sie normal.';
            $ok = $notify('Alarm-Box OFFLINE', date('[d.m.Y H:i] ') . $body);
            if ($ok) {
                $state['offline_notified'] = true;
                $state['offline_since'] = time() - $age;
                // Ausfall auch im Verlauf festhalten (nicht nur als flüchtiger
                // Bark-Push) - gleiche Einmal-Semantik wie die Benachrichtigung.
                append_command_log($config, ['event' => 'box_offline', 'minutes' => $mins]);
            }
            // Bei Sendefehler Flag nicht setzen: nächster Lauf versucht es erneut.
            $out[] = $ok ? 'NOTIFIED_OFFLINE' : 'SEND_FAILED';
        } elseif (!$offline && $notified) {
            $downMins = max(1, (int)round((time() - (int)($state['offline_since'] ?? time())) / 60));
            $ok = $notify('Alarm-Box wieder ONLINE',
                date('[d.m.Y H:i] ') . "ESP32 meldet sich wieder (Ausfall ca. {$downMins} min).");
            if ($ok) {
                $state['offline_notified'] = false;
                $state['offline_since'] = 0;
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
        $remindAfter = (int)($config['demo_reminder_after_seconds'] ?? 14400);   // 4 h
        // Die Erinnerung gehört zur jeweiligen Demo-Sitzung: Wurde der Modus
        // seit der letzten Erinnerung neu eingeschaltet (auch innerhalb von 15
        // min aus und wieder an), zählt die neue Sitzung von vorn.
        if ((int)($state['demo_reminded_for'] ?? 0) !== $since) {
            $state['demo_reminded_at'] = 0;
            $state['demo_reminded_for'] = $since;
        }
        $lastReminded = (int)($state['demo_reminded_at'] ?? 0);
        if ($since > 0 && time() - $since > $remindAfter && time() - $lastReminded > 86400) {
            $hours = max(1, (int)round((time() - $since) / 3600));
            $label = (string)($demo['label'] ?? 'Test-Empfaenger');
            $ok = $notify('Demo-Modus noch aktiv!',
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
        if (time() - $lastReminded > 86400) {
            $ok = $notify('Empfaenger stummgeschaltet',
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
        if (time() - $lastReminded > 604800) {
            $ok = $notify('Dauerhaft stummgeschaltet',
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

    // Zustand konnte nicht gespeichert werden (z.B. volle Platte)? Melden -
    // sonst blieben Zähler wie web_fail_count ewig bei 1 ("DEFEKT" käme nie),
    // und OFFLINE würde alle 15 min erneut gemeldet.
    if (!write_json_file($path, $state)) {
        $out[] = 'STATE_WRITE_FAILED (watchdog_state.json nicht speicherbar)';
    }
    return implode(' | ', $out) . "\n";
});

text_response($result);
