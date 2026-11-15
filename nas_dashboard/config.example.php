<?php
/*
 * Kopiere diese Datei nach config.php und trage echte Geheimnisse ein.
 * config.php darf nicht öffentlich geteilt oder ins Git aufgenommen werden.
 *
 * ===========================================================================
 * >>>>>>>>>>  HIER STEHEN ALLE WERTE, DIE DU EDITIEREN MUSST/KANNST  <<<<<<<<<
 * ===========================================================================
 */
return [
    // Lange zufällige Zeichenfolge, die in boss925_alarm_bark/config.h als
    // REMOTE_MACHINE_TOKEN eingetragen wird. Hier steht NUR der SHA-256-Hash
    // davon. Token UND Hash in einem Schritt erzeugen (das Geheimnis steht so
    // nie in der Shell-History) - Zeile 1 = Token für config.h, Zeile 2 = Hash:
    // php -r '$t=bin2hex(random_bytes(32)); echo $t, PHP_EOL, hash("sha256", $t), PHP_EOL;'
    'machine_token_sha256' => 'PASTE_SHA256_HEX_OF_REMOTE_MACHINE_TOKEN_HERE',

    // Dashboard-Login. Passwort-Hash erzeugen (Passwort wird eingetippt bzw.
    // per stdin übergeben, nicht als Argument - sonst landet es in der
    // History, und Zeichen wie $ ! ' verfälschen es):
    // php -r 'echo password_hash(rtrim(fgets(STDIN), "\r\n"), PASSWORD_DEFAULT), PHP_EOL;'
    'dashboard_user' => 'admin',
    'dashboard_password_hash' => 'PASTE_PASSWORD_HASH_HERE',

    // Optionaler zweiter Benutzer mit reinem LESE-Zugriff: Er sieht Status
    // und Empfängerliste, kann aber weder TEST noch REAL ALARM auslösen und
    // die Liste nicht ändern. Beide Werte leer lassen ('') = Login deaktiviert.
    // Hash erzeugen wie oben mit password_hash().
    'dashboard_readonly_user' => '',
    'dashboard_readonly_password_hash' => '',

    // Login-Bremse: Nach so vielen Fehlversuchen wird die IP-Adresse für das
    // Zeitfenster (Sekunden) gesperrt - zusätzlich zur sleep(1)-Bremse.
    'login_max_failures' => 8,
    'login_lockout_window_seconds' => 900,

    // Nach so vielen Sekunden ohne Status-Push gilt die Box als offline.
    'offline_after_seconds' => 180,

    // Optional: Kennung der ECHTEN Box - derselbe Wert wie REMOTE_DEVICE_ID in
    // config.h (z.B. 'boss925-01'). Gesetzt zählen für Online-Anzeige,
    // Offline-Wächter und Demo-Sicherung nur die Status-Pushs dieser Box, und
    // nur sie bekommt Befehle (TEST/DEMO-ALARM). Pushs anderer Boards mit
    // demselben Maschinen-Token (Bastel-/Ersatz-ESP32) werden angenommen, aber
    // nur als Hinweis "fremdes Board meldet sich" im Dashboard geführt - ohne
    // den Eintrag würde ein weiter pushendes Bastel-Board den Ausfall der
    // echten Box verdecken. Leer ('') = jeder Push mit dem Token zählt; melden
    // sich dann mehrere Boards, warnt das Dashboard.
    'box_device_id' => '',

    // ==== Meldungen an den Admin + Offline-Wächter (optional) ===============
    // Bark-Key des Owners für ALLE Statusmeldungen des NAS an den Admin:
    // cron/check_offline.php (DSM-Aufgabenplaner, siehe SETUP.md) meldet,
    // wenn die Box zu lange keinen Status gepusht hat (und wenn sie wieder da
    // ist), wenn das Dashboard nicht mehr in den data_dir schreiben kann
    // ("NAS-Dashboard DEFEKT"), wenn der Außen-Wächter verstummt, und erinnert
    // an Demo-Modus und Stummschaltungen; dazu kommen die Meldung bei einer
    // Login-IP-Sperre und die optionalen Arten (Anmeldung, Neustart der Box,
    // Wochenbericht ...). WELCHE Meldungen an diesen Key gehen und wie dringend
    // (ein/aus pro Art, Dringlichkeit, Ruhezeit, Ton, Stellvertreter,
    // Wiederholungen), stellst du im Dashboard ein: Panel "Meldungen an den
    // Admin" (Standard: leise). Die Werte hier darunter sind nur die
    // Standards, solange dort nichts anderes gewählt ist.
    // Leer ('') = ALLE Admin-Meldungen des NAS aus (das meldet dann auch der
    // Außen-Wächter). Sinnvoll: derselbe Key wie BARK_KEY_STATUS in config.h
    // des ESP32 und BARK_KEY in /etc/fw-alarm-watch.conf der Cloud-VM.
    'bark_key_status' => '',
    // Wächter-Schwelle in Sekunden (großzügiger als offline_after_seconds,
    // damit nicht jeder kurze WLAN-Schluckauf eine Meldung auslöst).
    'watchdog_offline_after_seconds' => 600,
    // Bleibt der Demo-Modus länger als diese Zeit aktiv, erinnert der Wächter
    // leise per Bark daran (danach max. 1x pro Tag) - echte Alarme erreichen
    // im Demo-Modus nur den Test-Empfänger!
    'demo_reminder_after_seconds' => 14400,
    // Selbsttest des Wächters: Er ruft api/health.php über den Webserver auf
    // (läuft dann als Web-Benutzer 'http') und meldet per Bark, wenn das
    // Dashboard nicht mehr in den data_dir schreiben kann. Nur ändern, wenn
    // das Dashboard NICHT unter /fw_alarm/ liegt. health.php antwortet nur
    // auf Aufrufe vom NAS selbst (127.0.0.1).
    // 'health_check_url' => 'https://127.0.0.1/fw_alarm/api/health.php',

    // Außen-Wächter (optional, siehe cloud_watcher/README.md): Eine kleine
    // Cloud-VM ruft api/alive.php alle 5 min von AUSSEN auf und meldet per
    // Bark, wenn das NAS nicht erreichbar ist - das kann der Wächter auf dem
    // NAS nicht, er fällt ja mit aus. Umgekehrt meldet dieser Cron-Wächter,
    // wenn die VM verstummt. Hier steht NUR der SHA-256-Hash des Wächter-
    // Tokens (eigenes Token, NICHT das Maschinen-Token der Box); der Klartext
    // kommt in /etc/fw-alarm-watch.conf auf der VM. Erzeugen wie oben:
    // php -r '$t=bin2hex(random_bytes(32)); echo $t, PHP_EOL, hash("sha256", $t), PHP_EOL;'
    // Leer = aus (api/alive.php antwortet dann 404). Derselbe Hash wie
    // machine_token_sha256 wird ebenfalls mit 404 abgelehnt - die VM darf nie
    // das Token der Box kennen.
    'watcher_token_sha256' => '',

    // ==== Arbeitsmodus (Stummschaltung per Kurzbefehl-Link) =================
    // Jeder Empfänger kann seinen Eintrag über einen persönlichen Link stumm
    // schalten (z.B. iPhone-Kurzbefehl beim Betreten/Verlassen der Arbeit):
    //   .../api/mute.php?t=<GEHEIM-TOKEN>&state=on|off
    // Den fertigen Link kopiert der Admin im Dashboard unter "Mehr" am
    // Eintrag. Die alte Form ?key=<EIGENER_BARK_KEY> bleibt für
    // Bestands-Kurzbefehle gültig. Stumm = der Alarm kommt weiterhin als
    // Critical Alert an, aber mit der im Dashboard eingestellten
    // Stumm-Lautstärke (Standard 0 = lautlos). Sicherheitsnetz: Nach so
    // vielen Sekunden wird automatisch wieder auf laut geschaltet (0 = nie
    // automatisch) - falls die "Arbeit verlassen"-Automation mal nicht
    // auslöst. Der Wächter-Cron erinnert zusätzlich max. 1x pro Tag,
    // solange Empfänger stumm sind.
    'mute_max_seconds' => 43200,   // 12 Stunden

    // Öffentliche Adresse des Dashboards (mit abschließendem /), z.B.
    // 'https://dein-name.synology.me/fw_alarm/'. Daraus baut das Dashboard die
    // kopierbaren Kurzbefehl-Links. Leer = die Adresse, unter der das
    // Dashboard gerade geöffnet ist - wer es über die LAN-IP öffnet, kopiert
    // dann aber Links, die unterwegs nicht funktionieren.
    'public_base_url' => '',

    // Delivered-Kommandos werden nicht erneut ausgeliefert. Nach dieser Zeit
    // darf das Dashboard einen neuen Befehl anlegen, falls die ACK verloren ging.
    'delivered_command_lock_seconds' => 120,

    // Ein pending-Befehl, den der ESP32 nicht innerhalb dieser Zeit abholt,
    // verfällt automatisch ("expired") - sonst würde er Stunden später noch
    // ausgeführt und das Dashboard bliebe bis dahin blockiert.
    'pending_command_ttl_seconds' => 300,

    // ==== REAL ALARM direkt vom NAS an Bark ================================
    // Der manuelle Alarm-Button sendet OHNE Umweg über den ESP32 - er muss ja
    // gerade dann funktionieren, wenn die ESP32-Kette klemmt. Alle Empfänger
    // bekommen ihn gleichzeitig (parallel), je bis zu bark_max_tries Versuche.
    // Die Empfängerliste wird normalerweise IM DASHBOARD gepflegt (Panel
    // "Alarm-Empfänger"); bark_keys_alarm hier ist nur ein Fallback, solange
    // die Dashboard-Liste noch leer ist (oder samt Sicherung unlesbar wäre).
    // ACHTUNG: Titel/Body nur ASCII (ae/ue/oe), Bark zeigt keine Umlaute.
    'bark_host' => 'https://api.day.app',
    'bark_keys_alarm' => [
        // 'KEY_PERSON_1',
        // 'KEY_PERSON_2',
    ],
    'bark_alarm_title' => 'FEUERWEHR-ALARM (manuell)',
    'bark_alarm_body' => 'Manuelle Alarmierung ueber das NAS-Dashboard!',
    'bark_alarm_sound' => 'alarm_fw',
    'bark_alarm_volume' => 10,
    'bark_alarm_call' => true,
    'bark_max_tries' => 3,

    // ==== ENTWARNUNG (Fehlalarm) ===========================================
    // Geht an dieselben Empfänger wie der Alarm und ist ebenfalls ein
    // CRITICAL ALERT - wer schon zum Einsatz aufgebrochen ist, muss die
    // Entwarnung genauso sicher mitbekommen (durch Stummschalter und Fokus
    // hindurch). Unterschied zum Alarm: ein kurzer, normaler Ton statt des
    // Alarmtons und keine Ton-Wiederholung (kein call=1).
    // Ton: einer der in Bark eingebauten Kurztöne, z.B. 'glass' (klassisches
    // kurzes "Pling"), 'bell', 'chime', 'telegraph' oder 'paymentsuccess'.
    'bark_false_alarm_sound' => 'glass',
    // Lautstärke 0-10. Ohne Eintrag wird bark_alarm_volume verwendet.
    'bark_false_alarm_volume' => 10,

    'timezone' => 'Europe/Berlin',
    // WICHTIG: data_dir UNBEDINGT außerhalb des Web-Roots wählen und als
    // ABSOLUTEN Pfad angeben! Hier liegen die Bark-Keys im KLARTEXT. Ein Pfad
    // unter dem ausgelieferten Verzeichnis (z.B. /volume1/web/...) ist nur
    // durch die .php-Guard-Zeile geschützt - das ist eine EINZIGE,
    // zerbrechliche Schicht (hängt an der Nginx-Weiterleitung an PHP). Besser
    // physisch draußen: Auf Synology am besten eine eigene DSM-Freigabe
    // außerhalb von /volume1/web (z.B. "fw_alarm_data", Gruppe http
    // Lesen/Schreiben, siehe SETUP.md). Alternative /volume1/web_packages/...
    // - dort kann aber ein Web-Station-Update die Rechte zurücksetzen (dann
    // scheitern Login und Status-Pushs still). Die Guard-Zeile bleibt als
    // zweite Schicht erhalten.
    'data_dir' => '/volume1/fw_alarm_data',
];
