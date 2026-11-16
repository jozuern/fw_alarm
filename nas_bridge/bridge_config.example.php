<?php
// Vorlage für bridge_config.php (liegt nur auf dem NAS, nicht im Git).
// Kopieren nach bridge_config.php und bei Bedarf anpassen.
// Enthält keine Geheimnisse - nur, wohin die Brücke weiterreicht.
return [
    // Der neue Server, OHNE Schrägstrich am Ende. Die Brücke ruft
    // <target>/api/<datei> auf und prüft dabei das Zertifikat.
    'target' => 'https://alarm.tunero.dev',

    // Wohin index.php und classic.php Besucher der alten Adresse schicken.
    'public_url' => 'https://alarm.tunero.dev/',

    // Datei, in der die Brücke während einer Wartung (bridge_hold.php
    // vorhanden) abgewiesene Mute-Aufrufe merkt - ohne Token, nur mit einem
    // kurzen Fingerabdruck. Absoluter Pfad AUSSERHALB des Web-Ordners, den
    // der Web-Benutzer (http) beschreiben darf.
    'held_log' => '/volume1/web_packages/fw_alarm_data/bridge_held.log',
];
