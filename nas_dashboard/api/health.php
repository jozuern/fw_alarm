<?php
require __DIR__ . '/../lib/common.php';

// Selbsttest für den Cron-Wächter (cron/check_offline.php): Läuft als echter
// Web-Benutzer (http) mit dem echten PHP-Profil und prüft, ob der data_dir
// beschreibbar ist. Hintergrund: Im Oktober 2026 hat ein Update der Gruppe
// 'http' das Schreibrecht entzogen - Login und Status-Pushs scheiterten drei
// Wochen lang still, weil der Wächter als root lief und nichts bemerkte.
//
// Antwortet NUR auf Aufrufe vom NAS selbst (127.0.0.1 / ::1, bei
// Dual-Stack-Listenern auch ::ffff:127.0.0.1). Alle anderen bekommen 404 -
// so verrät der Endpunkt nichts und niemand von außen kann Schreibzugriffe
// auslösen. ACHTUNG: Hinter einem Reverse-Proxy auf dem NAS selbst (DSM
// "Reverse Proxy") kommen ALLE Aufrufe von 127.0.0.1 - siehe SETUP.md.
$remote = (string)($_SERVER['REMOTE_ADDR'] ?? '');
if (!in_array($remote, ['127.0.0.1', '::1', '::ffff:127.0.0.1'], true)) {
    http_response_code(404);
    exit;
}
// Von einem Proxy/Tunnel auf dem NAS weitergereicht (Cloudflare Tunnel,
// Reverse-Proxy)? Der kommt ebenfalls von 127.0.0.1, bringt aber eine dieser
// Kopfzeilen mit - der Wächter (cURL direkt) schickt keine davon. So bleibt
// der Selbsttest auch hinter einem Proxy von außen unerreichbar.
foreach (['HTTP_X_FORWARDED_FOR', 'HTTP_X_FORWARDED_HOST', 'HTTP_X_FORWARDED_PROTO', 'HTTP_X_REAL_IP',
    'HTTP_FORWARDED', 'HTTP_VIA', 'HTTP_CF_CONNECTING_IP', 'HTTP_CF_RAY', 'HTTP_CDN_LOOP'] as $proxyHeader) {
    if (isset($_SERVER[$proxyHeader])) {
        http_response_code(404);
        exit;
    }
}

$config = load_config();
$result = data_dir_write_check($config);
if (!$result['ok']) {
    http_response_code(500);
}
text_response('ok=' . ($result['ok'] ? '1' : '0') . "\nstep=" . $result['step'] . "\n"
    . (isset($result['file']) ? 'file=' . $result['file'] . "\n" : '')
    . (isset($result['files']) ? 'files=' . implode(',', $result['files']) . "\n" : ''));
