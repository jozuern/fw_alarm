<?php
/*
 * Kopiere diese Datei nach config.php und trage echte Geheimnisse ein.
 * config.php darf nicht öffentlich geteilt oder ins Git aufgenommen werden.
 */
return [
    // Lange zufällige Zeichenfolge in boss925_alarm_bark/config.h eintragen.
    // Hier steht NUR der SHA-256-Hash davon:
    // php -r "echo hash('sha256', 'DEIN_LANGER_TOKEN'), PHP_EOL;"
    'machine_token_sha256' => 'PASTE_SHA256_HEX_OF_REMOTE_MACHINE_TOKEN_HERE',

    // Dashboard-Login. Passwort-Hash erzeugen mit:
    // php -r "echo password_hash('DEIN_DASHBOARD_PASSWORT', PASSWORD_DEFAULT), PHP_EOL;"
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

    // ==== Offline-Wächter + Sicherheits-Meldungen (optional) ================
    // Bark-Key des Owners für LEISE Statusmeldungen (level=passive):
    // 1. cron/check_offline.php (DSM-Aufgabenplaner, siehe SETUP.md) meldet
    //    einmalig, wenn die Box zu lange keinen Status gepusht hat - und
    //    einmalig, wenn sie wieder da ist.
    // 2. Das Dashboard meldet, wenn eine IP wegen zu vieler Login-Fehlversuche
    //    gesperrt wurde (jemand ruettelt am Login).
    // Leer ('') = beides aus. Sinnvoll: derselbe Key wie BARK_KEY_STATUS in
    // config.h des ESP32.
    'bark_key_status' => '',
    // Wächter-Schwelle in Sekunden (großzügiger als offline_after_seconds,
    // damit nicht jeder kurze WLAN-Schluckauf eine Meldung auslöst).
    'watchdog_offline_after_seconds' => 600,
    // Bleibt der Demo-Modus länger als diese Zeit aktiv, erinnert der Wächter
    // leise per Bark daran (danach max. 1x pro Tag) - echte Alarme erreichen
    // im Demo-Modus nur den Test-Empfänger!
    'demo_reminder_after_seconds' => 14400,

    // ==== Arbeitsmodus (Stummschaltung per Kurzbefehl-Link) =================
    // Jeder Empfänger kann seinen Eintrag über einen persönlichen Link stumm
    // schalten (z.B. iPhone-Kurzbefehl beim Betreten/Verlassen der Arbeit):
    //   .../api/mute.php?key=<EIGENER_BARK_KEY>&state=on|off
    // Stumm = der Alarm kommt weiterhin als Critical Alert an, aber mit
    // volume=0 (lautlos). Sicherheitsnetz: Nach so vielen Sekunden wird
    // automatisch wieder auf laut geschaltet (0 = nie automatisch) - falls die
    // "Arbeit verlassen"-Automation mal nicht auslöst. Der Wächter-Cron
    // erinnert zusätzlich max. 1x pro Tag, solange Empfänger stumm sind.
    'mute_max_seconds' => 43200,   // 12 Stunden

    // Delivered-Kommandos werden nicht erneut ausgeliefert. Nach dieser Zeit
    // darf das Dashboard einen neuen Befehl anlegen, falls die ACK verloren ging.
    'delivered_command_lock_seconds' => 120,

    // Ein pending-Befehl, den der ESP32 nicht innerhalb dieser Zeit abholt,
    // verfällt automatisch ("expired") - sonst würde er Stunden später noch
    // ausgeführt und das Dashboard bliebe bis dahin blockiert.
    'pending_command_ttl_seconds' => 300,

    // ==== REAL ALARM direkt vom NAS an Bark ================================
    // Der manuelle Alarm-Button sendet OHNE Umweg über den ESP32 - er muss ja
    // gerade dann funktionieren, wenn die ESP32-Kette klemmt.
    // Die Empfaengerliste wird normalerweise IM DASHBOARD gepflegt (Panel
    // "Alarm-Empfaenger"); bark_keys_alarm hier ist nur ein Fallback, solange
    // die Dashboard-Liste noch leer ist.
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

    'timezone' => 'Europe/Berlin',
    // WICHTIG: data_dir UNBEDINGT ausserhalb des Web-Roots waehlen! Hier liegen
    // die Bark-Keys im KLARTEXT. Ein Pfad unter dem ausgelieferten Verzeichnis
    // (z.B. __DIR__ . '/data' oder /volume1/web/...) ist nur durch die
    // .php-Guard-Zeile geschuetzt - das ist eine EINZIGE, zerbrechliche Schicht
    // (haengt an der Nginx-Weiterleitung an PHP). Besser physisch draussen:
    // Auf Synology z.B. /volume1/web_packages/... oder eine eigene Freigabe
    // ausserhalb von /volume1/web (sicherstellen, dass der PHP-Benutzer dort
    // schreiben darf). Die Guard-Zeile bleibt als zweite Schicht erhalten.
    'data_dir' => '/volume1/web_packages/fw_alarm_data',
];
