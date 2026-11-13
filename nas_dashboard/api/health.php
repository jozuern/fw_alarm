<?php
require __DIR__ . '/../lib/common.php';

// Selbsttest für den Cron-Wächter (cron/check_offline.php): Läuft als echter
// Web-Benutzer (http) mit dem echten PHP-Profil und prüft, ob der data_dir
// beschreibbar ist. Hintergrund: Im Oktober 2026 hat ein Update der Gruppe
// 'http' das Schreibrecht entzogen - Login und Status-Pushs scheiterten drei
// Wochen lang still, weil der Wächter als root lief und nichts bemerkte.
//
// Antwortet NUR auf Aufrufe vom NAS selbst (127.0.0.1 / ::1). Alle anderen
// bekommen 404 - so verrät der Endpunkt nichts und niemand von außen kann
// Schreibzugriffe auslösen.
$remote = (string)($_SERVER['REMOTE_ADDR'] ?? '');
if (!in_array($remote, ['127.0.0.1', '::1'], true)) {
    http_response_code(404);
    exit;
}

$config = load_config();
$result = data_dir_write_check($config);
if (!$result['ok']) {
    http_response_code(500);
}
text_response('ok=' . ($result['ok'] ? '1' : '0') . "\nstep=" . $result['step'] . "\n");
