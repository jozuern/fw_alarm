<?php
declare(strict_types=1);

ini_set('display_errors', '0');
date_default_timezone_set('Europe/Berlin');

// Sicherheits-Header für ALLE Antworten (Dashboard und API). Verhindert u.a.
// Clickjacking (das Dashboard in einem fremden iframe) und MIME-Sniffing.
// Für den ESP32 sind die Header wirkungslos, aber unschädlich.
if (!headers_sent()) {
    header('X-Frame-Options: DENY');
    header("Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'");
    header('X-Content-Type-Options: nosniff');
    header('Referrer-Policy: strict-origin-when-cross-origin');
    header('Permissions-Policy: camera=(), microphone=(), geolocation=()');
    if (!empty($_SERVER['HTTPS']) && $_SERVER['HTTPS'] !== 'off') {
        header('Strict-Transport-Security: max-age=31536000');
    }
}

// Guard-Zeile am Anfang jeder Laufzeitdatei: Die Dateien heißen bewusst *.php,
// damit Nginx sie NIE als statische Datei ausliefert, sondern an PHP übergibt -
// und PHP bricht dank dieser Zeile sofort ab. So bleiben Status/Befehle/Log
// auch dann privat, wenn data_dir im Webroot liegt (Synology Web Station
// ignoriert .htaccess, weil dort Nginx läuft).
const DATA_FILE_GUARD = "<?php http_response_code(404); exit; ?>\n";

function load_config(): array
{
    $path = dirname(__DIR__) . '/config.php';
    if (!is_file($path)) {
        http_response_code(500);
        exit('Konfiguration fehlt.');
    }
    $config = require $path;
    if (isset($config['timezone'])) {
        date_default_timezone_set((string)$config['timezone']);
    }
    return $config;
}

function data_dir(array $config): string
{
    $dir = (string)$config['data_dir'];
    if (!is_dir($dir)) {
        mkdir($dir, 0750, true);
    }
    // Falls Nginx-Verzeichnislisting (autoindex) aktiv sein sollte, verhindert
    // diese Datei, dass jemand die Namen der Laufzeitdateien aufzählen kann.
    $guard = $dir . '/index.php';
    if (!is_file($guard)) {
        @file_put_contents($guard, "<?php http_response_code(404); exit;\n");
    }
    return $dir;
}

// Laufzeitdateien bekommen die Endung .php (siehe DATA_FILE_GUARD oben).
function data_path(array $config, string $name): string
{
    return data_dir($config) . '/' . $name . '.php';
}

function read_json_file(string $path, array $default): array
{
    if (!is_file($path)) {
        return $default;
    }
    $raw = file_get_contents($path);
    if ($raw === false || $raw === '') {
        return $default;
    }
    // Guard-Zeile (erste Zeile) überspringen.
    if (str_starts_with($raw, '<?php')) {
        $nl = strpos($raw, "\n");
        $raw = $nl === false ? '' : substr($raw, $nl + 1);
    }
    $data = json_decode($raw, true);
    return is_array($data) ? $data : $default;
}

// Schreibt atomar (tmp + rename). Gibt false zurück, wenn irgendetwas
// fehlschlägt - die Aufrufer melden das als HTTP 500 an den ESP32/Browser,
// statt einen Erfolg vorzutäuschen.
function write_json_file(string $path, array $data): bool
{
    $tmp = $path . '.tmp';
    $json = json_encode($data, JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES);
    if ($json === false) {
        return false;
    }
    $written = @file_put_contents($tmp, DATA_FILE_GUARD . $json, LOCK_EX);
    if ($written === false) {
        return false;
    }
    if (!@rename($tmp, $path)) {
        @unlink($tmp);
        return false;
    }
    return true;
}

// Wie write_json_file, hält aber vorher die letzte Version als *.bak-Datei
// fest (ebenfalls .php-guarded). Gedacht für die Empfängerliste/Demo-Zustand:
// Die Liste ist die EINE Quelle für beide Alarmwege - geht die Datei kaputt,
// lässt sich der letzte Stand von Hand zurückholen (Datei umbenennen).
function backup_then_write(array $config, string $name, array $data): bool
{
    $path = data_path($config, $name);
    if (is_file($path)) {
        @copy($path, data_path($config, $name . '.bak'));
    }
    return write_json_file($path, $data);
}

function with_lock(array $config, string $name, callable $callback)
{
    $lockPath = data_path($config, $name . '.lock');
    $handle = @fopen($lockPath, 'c');
    if ($handle === false) {
        http_response_code(500);
        exit('Fehler.');
    }
    flock($handle, LOCK_EX);
    try {
        return $callback();
    } finally {
        flock($handle, LOCK_UN);
        fclose($handle);
    }
}

// Maschinen-Token NUR per POST akzeptieren: GET-Parameter landen sonst im
// Klartext in Webserver-Logs.
function require_machine_auth(array $config): void
{
    $token = (string)($_POST['token'] ?? '');
    $expected = (string)($config['machine_token_sha256'] ?? '');
    if ($token === '' || $expected === '' || !hash_equals($expected, hash('sha256', $token))) {
        http_response_code(403);
        exit('FORBIDDEN');
    }
}

// ======================= LOGIN-BREMSE (pro IP) ==============================
// sleep(1) allein lässt sich mit vielen parallelen Verbindungen aushebeln.
// Deshalb zusätzlich: Nach zu vielen Fehlversuchen wird die IP für ein
// Zeitfenster gesperrt. Ein erfolgreicher Login setzt den Zähler zurück.

function login_client_ip(): string
{
    return (string)($_SERVER['REMOTE_ADDR'] ?? 'unknown');
}

function login_throttle_limits(array $config): array
{
    return [
        max(1, (int)($config['login_max_failures'] ?? 8)),
        max(60, (int)($config['login_lockout_window_seconds'] ?? 900)),
    ];
}

// true = diese IP hat ihr Fehlversuch-Budget im aktuellen Fenster aufgebraucht.
function login_throttle_blocked(array $config): bool
{
    [$max, $window] = login_throttle_limits($config);
    $entry = read_json_file(data_path($config, 'login_throttle.json'), [])[login_client_ip()] ?? null;
    if (!is_array($entry)) {
        return false;
    }
    if (time() - (int)($entry['first_at'] ?? 0) > $window) {
        return false;
    }
    return (int)($entry['count'] ?? 0) >= $max;
}

// Gibt den neuen Fehlversuchs-Zählerstand der IP zurück - der Aufrufer kann
// so genau beim Erreichen der Sperrschwelle einmalig den Owner benachrichtigen.
function login_throttle_record_failure(array $config): int
{
    [, $window] = login_throttle_limits($config);
    return (int)with_lock($config, 'login', function () use ($config, $window): int {
        $path = data_path($config, 'login_throttle.json');
        $all = read_json_file($path, []);
        $now = time();
        foreach ($all as $ip => $entry) {   // abgelaufene Fenster aufräumen
            if ($now - (int)($entry['first_at'] ?? 0) > $window) {
                unset($all[$ip]);
            }
        }
        $ip = login_client_ip();
        $entry = is_array($all[$ip] ?? null) ? $all[$ip] : ['count' => 0, 'first_at' => $now];
        $entry['count'] = (int)($entry['count'] ?? 0) + 1;
        $all[$ip] = $entry;
        write_json_file($path, $all);
        return (int)$entry['count'];
    });
}

function login_throttle_clear(array $config): void
{
    with_lock($config, 'login', function () use ($config): void {
        $path = data_path($config, 'login_throttle.json');
        $all = read_json_file($path, []);
        unset($all[login_client_ip()]);
        write_json_file($path, $all);
    });
}

function start_dashboard_session(): void
{
    session_name('boss925_dashboard');
    session_set_cookie_params([
        'lifetime' => 0,
        'path' => '/',
        'secure' => !empty($_SERVER['HTTPS']) && $_SERVER['HTTPS'] !== 'off',
        'httponly' => true,
        'samesite' => 'Strict',
    ]);
    session_start();
}

// Sitzung läuft nach dieser Inaktivität serverseitig ab. Wichtig, weil
// Browser Session-Cookies über einen Neustart hinweg wiederherstellen können -
// "Browser zu = abgemeldet" stimmt also nicht immer. Ein offener Dashboard-Tab
// bleibt angemeldet (das 5-s-Polling zählt als Aktivität).
const SESSION_MAX_IDLE_SECONDS = 43200;   // 12 Stunden

function is_dashboard_authed(): bool
{
    if (empty($_SESSION['authed'])) {
        return false;
    }
    $last = (int)($_SESSION['last_seen'] ?? 0);
    if ($last > 0 && time() - $last > SESSION_MAX_IDLE_SECONDS) {
        $_SESSION = [];
        session_destroy();
        return false;
    }
    $_SESSION['last_seen'] = time();
    return true;
}

function require_dashboard_auth(): void
{
    if (!is_dashboard_authed()) {
        http_response_code(401);
        json_response(['ok' => false]);
    }
}

// Rolle der angemeldeten Person: 'admin' (voller Zugriff) oder 'readonly'
// (nur ansehen). Sessions von vor dieser Funktion stammen immer vom
// Admin-Login, deshalb ist 'admin' der Fallback.
function dashboard_role(): string
{
    return (string)($_SESSION['role'] ?? 'admin');
}

// Auslösende/ändernde Aktionen sind dem Admin vorbehalten. Der Lese-Benutzer
// wird hier serverseitig gestoppt - die ausgeblendeten Knöpfe im UI sind nur
// Komfort, kein Schutz.
function require_dashboard_admin(): void
{
    if (dashboard_role() !== 'admin') {
        http_response_code(403);
        json_response(['ok' => false, 'message' => 'Nur Lesezugriff: Dieser Benutzer darf nichts ausloesen oder aendern.']);
    }
}

function ensure_csrf(): string
{
    if (empty($_SESSION['csrf'])) {
        $_SESSION['csrf'] = bin2hex(random_bytes(24));
    }
    return (string)$_SESSION['csrf'];
}

function require_csrf(): void
{
    $sent = (string)($_SERVER['HTTP_X_CSRF_TOKEN'] ?? $_POST['csrf'] ?? '');
    if ($sent === '' || empty($_SESSION['csrf']) || !hash_equals((string)$_SESSION['csrf'], $sent)) {
        http_response_code(403);
        json_response(['ok' => false]);
    }
}

function json_response(array $payload): void
{
    header('Content-Type: application/json; charset=utf-8');
    echo json_encode($payload, JSON_UNESCAPED_SLASHES);
    exit;
}

function text_response(string $payload): void
{
    header('Content-Type: text/plain; charset=utf-8');
    echo $payload;
    exit;
}

function clipped_post_fields(array $skip = []): array
{
    $out = [];
    foreach ($_POST as $key => $value) {
        // Der ESP32 sendet ~20 Felder; das Limit verhindert, dass jemand mit
        // riesigen POSTs unnötig große Statusdateien auf die Platte schreibt.
        if (count($out) >= 40) {
            break;
        }
        if (in_array($key, $skip, true)) {
            continue;
        }
        if (!is_scalar($value)) {
            continue;
        }
        $safeKey = preg_replace('/[^a-zA-Z0-9_:-]/', '', (string)$key);
        if ($safeKey === '') {
            continue;
        }
        $out[$safeKey] = substr((string)$value, 0, 300);
    }
    return $out;
}

function command_state_default(): array
{
    return ['next_id' => 1, 'current' => null];
}

// Lässt einen zu lange wartenden pending-Befehl verfallen. Ohne diese TTL würde
// ein Befehl an einen gerade toten ESP32 unbegrenzt lauern und Stunden später
// noch ausgeführt (bei TEST harmlos, aber das Dashboard bliebe blockiert).
// Muss innerhalb von with_lock('command') aufgerufen werden, wenn $persist=true.
// Gibt true zurück, wenn der Befehl gerade abgelaufen ist.
function expire_stale_pending(array $config, array &$state, bool $persist): bool
{
    $current = $state['current'] ?? null;
    if (!is_array($current) || ($current['state'] ?? '') !== 'pending') {
        return false;
    }
    $ttl = (int)($config['pending_command_ttl_seconds'] ?? 300);
    $createdAt = (int)($current['created_at'] ?? 0);
    if ($createdAt <= 0 || time() - $createdAt <= $ttl) {
        return false;
    }
    $current['state'] = 'expired';
    $current['expired_at'] = time();
    $state['current'] = $current;
    if ($persist) {
        write_json_file(data_path($config, 'command_state.json'), $state);
        append_command_log($config, ['event' => 'expired', 'command' => $current]);
    }
    return true;
}

function append_command_log(array $config, array $entry): void
{
    $entry['log_at'] = time();
    $path = data_path($config, 'commands.log');
    // Rotation: sonst füllt das Log über Jahre die Platte, und volle Platte
    // heißt: Status-/Befehlsdateien lassen sich nicht mehr schreiben.
    if (is_file($path) && (int)@filesize($path) > 5 * 1024 * 1024) {
        $old = data_path($config, 'commands.log.1');
        @unlink($old);
        @rename($path, $old);
    }
    $prefix = is_file($path) ? '' : DATA_FILE_GUARD;
    @file_put_contents(
        $path,
        $prefix . json_encode($entry, JSON_UNESCAPED_SLASHES) . PHP_EOL,
        FILE_APPEND | LOCK_EX
    );
}

// Liest die letzten Einträge aus commands.log für das Verlaufs-Panel.
// Bewusst nur das Datei-Ende (Tail), damit auch eine 5-MB-Logdatei das
// Dashboard nicht ausbremst. Neueste Einträge zuerst.
function read_command_log_tail(array $config, int $limit = 25): array
{
    $path = data_path($config, 'commands.log');
    if (!is_file($path)) {
        return [];
    }
    $size = (int)@filesize($path);
    $chunk = 64 * 1024;
    $handle = @fopen($path, 'rb');
    if ($handle === false) {
        return [];
    }
    $offset = max(0, $size - $chunk);
    fseek($handle, $offset);
    $raw = (string)stream_get_contents($handle);
    fclose($handle);

    $lines = explode("\n", $raw);
    if ($offset > 0) {
        array_shift($lines);   // erste Zeile ist evtl. angeschnitten
    }
    $entries = [];
    foreach ($lines as $line) {
        $line = trim($line);
        if ($line === '' || str_starts_with($line, '<?php')) {
            continue;   // Guard-Zeile überspringen
        }
        $data = json_decode($line, true);
        if (is_array($data)) {
            $entries[] = $data;
        }
    }
    return array_reverse(array_slice($entries, -$limit));
}

// ============================ EMPFÄNGERLISTE ================================
// Die Alarm-Empfänger werden im Dashboard gepflegt und liegen versioniert in
// alarm_keys.json(.php). Der ESP32 sieht die Version im Poll und holt sich bei
// Abweichung die Liste über api/keys.php (persistiert sie dann im NVS-Flash).
// Der NAS-Direktalarm nutzt DIESELBE Liste -> eine Quelle für beide Alarmwege.

function alarm_keys_default(): array
{
    return ['next_id' => 1, 'version' => 0, 'keys' => []];
}

function load_alarm_keys(array $config): array
{
    return read_json_file(data_path($config, 'alarm_keys.json'), alarm_keys_default());
}

// Bark-Keys sind kurze alphanumerische Tokens.
function valid_bark_key(string $key): bool
{
    return preg_match('/^[A-Za-z0-9_-]{5,64}$/', $key) === 1;
}

// Nur die Key-Strings der gepflegten Liste (für Versand/ESP32-Sync).
function managed_alarm_keys(array $config): array
{
    $keys = [];
    foreach ((load_alarm_keys($config)['keys'] ?? []) as $entry) {
        $key = trim((string)($entry['key'] ?? ''));
        if ($key !== '') {
            $keys[] = $key;
        }
    }
    return $keys;
}

// ============================== DEMO-MODUS ==================================
// Im Demo-Modus gehen ALLE Alarme (ESP32-Relaisalarm UND NAS-Direktalarm) nur
// an EINEN Test-Empfänger aus der gepflegten Liste. Umgesetzt ohne
// Firmware-Änderung über die versionierte Empfängerliste: api/keys.php liefert
// im Demo-Modus nur den Demo-Key, und jeder Moduswechsel erhöht die
// Listen-Version - der ESP32 holt sich die (Demo- bzw. volle) Liste dann beim
// nächsten Poll automatisch. WICHTIG: Der Wechsel greift auf dem ESP32 erst
// nach dessen nächstem Poll; das Dashboard zeigt an, ob er schon so weit ist.

function demo_mode_default(): array
{
    return ['enabled' => false];
}

function load_demo_mode(array $config): array
{
    return read_json_file(data_path($config, 'demo_mode.json'), demo_mode_default());
}

function demo_mode_enabled(array $config): bool
{
    $demo = load_demo_mode($config);
    return !empty($demo['enabled']) && valid_bark_key(trim((string)($demo['key'] ?? '')));
}

// Kompakte Demo-Info für das Dashboard (Key nur maskiert - Keys sind Geheimnisse).
function demo_mode_info(array $config): array
{
    $demo = load_demo_mode($config);
    $enabled = !empty($demo['enabled']);
    return [
        'enabled' => $enabled,
        'label' => $enabled ? (string)($demo['label'] ?? '') : '',
        'key_id' => $enabled ? (int)($demo['key_id'] ?? 0) : 0,
        'key_masked' => $enabled ? mask_bark_key((string)($demo['key'] ?? '')) : '',
        'changed_at' => (int)($demo['changed_at'] ?? 0),
        'by' => (string)($demo['by'] ?? ''),
    ];
}

// Die Keys, an die JETZT alarmiert würde: Demo-Modus -> nur der Demo-Key,
// sonst die volle gepflegte Liste. Wird von api/keys.php (ESP32-Sync) und vom
// NAS-Direktalarm benutzt - beide Alarmwege sehen immer dieselbe Liste.
function effective_alarm_keys(array $config): array
{
    $demo = load_demo_mode($config);
    if (!empty($demo['enabled'])) {
        $key = trim((string)($demo['key'] ?? ''));
        if (valid_bark_key($key)) {
            return [$key];
        }
        // Unplausibler Demo-Key: lieber die volle Liste als gar keine Alarmierung.
    }
    return managed_alarm_keys($config);
}

// ============================ BARK-DIREKTVERSAND ============================
// Der manuelle REAL ALARM läuft absichtlich DIREKT vom NAS zu Bark: Der
// Hauptzweck einer manuellen Alarmierung ist ja gerade der Fall, dass der
// ESP32 oder seine Kette klemmt.

// Nur die ersten 4 Zeichen eines Keys zeigen/loggen - Keys sind Geheimnisse.
function mask_bark_key(string $key): string
{
    return substr($key, 0, 4) . '...';
}

// Leise Statusmeldung (level=passive) an EINEN Bark-Key - das Gegenstück zu
// sendStatus() in der Firmware. Genutzt vom Offline-Wächter (cron/) und der
// Login-Sperren-Meldung. Titel/Body müssen ASCII sein (ae/ue/oe, keine
// Umlaute - Bark stellt sie nicht korrekt dar). Knappe Timeouts, damit z.B.
// der Login-Pfad nie lange an Bark hängt.
function bark_send_status(array $config, string $key, string $title, string $body): bool
{
    if (!function_exists('curl_init') || !valid_bark_key($key)) {
        return false;
    }
    $host = rtrim((string)($config['bark_host'] ?? 'https://api.day.app'), '/');
    $form = http_build_query([
        'title' => $title,
        'body'  => $body,
        'level' => 'passive',
    ]);
    for ($attempt = 1; $attempt <= 2; $attempt++) {
        $ch = curl_init($host . '/' . rawurlencode($key));
        curl_setopt_array($ch, [
            CURLOPT_POST => true,
            CURLOPT_POSTFIELDS => $form,
            CURLOPT_HTTPHEADER => ['Content-Type: application/x-www-form-urlencoded'],
            CURLOPT_RETURNTRANSFER => true,
            CURLOPT_CONNECTTIMEOUT => 5,
            CURLOPT_TIMEOUT => 10,
        ]);
        curl_exec($ch);
        $code = (int)curl_getinfo($ch, CURLINFO_RESPONSE_CODE);
        curl_close($ch);
        if ($code === 200) {
            return true;
        }
        if ($code >= 400 && $code < 500) {
            return false;   // Client-Fehler: Wiederholen zwecklos.
        }
        if ($attempt < 2) {
            usleep(500000);
        }
    }
    return false;
}

// Sendet einen Critical Alert an EINEN Bark-Key, mit Wiederholung wie in der
// Firmware: HTTP 4xx wird nicht wiederholt (Key falsch), alles andere schon.
function bark_send_alarm_one(array $config, string $key, string $body): array
{
    $host = rtrim((string)($config['bark_host'] ?? 'https://api.day.app'), '/');
    $tries = max(1, (int)($config['bark_max_tries'] ?? 3));
    $form = http_build_query([
        'title'  => (string)($config['bark_alarm_title'] ?? 'FEUERWEHR-ALARM'),
        'body'   => $body,
        'sound'  => (string)($config['bark_alarm_sound'] ?? 'alarm_fw'),
        'level'  => 'critical',
        'volume' => (int)($config['bark_alarm_volume'] ?? 10),
    ]);
    if (!empty($config['bark_alarm_call'])) {
        $form .= '&call=1';
    }

    $httpCode = 0;
    for ($attempt = 1; $attempt <= $tries; $attempt++) {
        $ch = curl_init($host . '/' . rawurlencode($key));
        curl_setopt_array($ch, [
            CURLOPT_POST => true,
            CURLOPT_POSTFIELDS => $form,
            CURLOPT_HTTPHEADER => ['Content-Type: application/x-www-form-urlencoded'],
            CURLOPT_RETURNTRANSFER => true,
            CURLOPT_CONNECTTIMEOUT => 5,
            CURLOPT_TIMEOUT => 10,
        ]);
        curl_exec($ch);
        $httpCode = (int)curl_getinfo($ch, CURLINFO_RESPONSE_CODE);
        curl_close($ch);

        if ($httpCode === 200) {
            return ['key' => mask_bark_key($key), 'ok' => true, 'http' => 200];
        }
        if ($httpCode >= 400 && $httpCode < 500) {
            break;  // Client-Fehler: Wiederholen zwecklos, Key prüfen.
        }
        if ($attempt < $tries) {
            usleep(500000);
        }
    }
    return ['key' => mask_bark_key($key), 'ok' => false, 'http' => $httpCode];
}

// Alarmiert alle konfigurierten Keys, pro Key isoliert - ein toter Key
// blockiert die anderen nicht. Gibt pro Key ein Ergebnis zurück.
function bark_send_alarm_all(array $config): array
{
    if (!function_exists('curl_init')) {
        return ['ok' => false, 'message' => 'PHP-cURL fehlt. In Web Station im PHP-Profil die Erweiterung "curl" aktivieren.', 'results' => []];
    }
    // Führend ist die im Dashboard gepflegte Liste (im Demo-Modus nur der
    // Demo-Key); die statische Liste aus config.php dient nur noch als
    // Fallback, solange noch keine gepflegt ist.
    $demoActive = demo_mode_enabled($config);
    $keys = effective_alarm_keys($config);
    $usedFallback = false;
    if (count($keys) === 0) {
        $fallback = $config['bark_keys_alarm'] ?? [];
        if (!is_array($fallback)) {
            $fallback = [];
        }
        $keys = array_values(array_filter(array_map('trim', array_map('strval', $fallback))));
        $usedFallback = count($keys) > 0;
    }
    if (count($keys) === 0) {
        return ['ok' => false, 'message' => 'Keine Alarm-Empfaenger: Liste im Dashboard pflegen (Panel "Alarm-Empfaenger").', 'results' => []];
    }

    // Genug Laufzeit für viele Keys mit Wiederholungen (Standardlimit: 30 s).
    @set_time_limit(30 + count($keys) * 35);

    // Zeitstempel wie in der Firmware; Bark-Texte bewusst ASCII (ae/ue/oe).
    // Bewusst KEIN Demo-Zusatz im Text: Ein Demo-Alarm soll exakt so aussehen
    // und klingen wie ein echter - er geht nur an weniger Empfänger.
    $body = date('[d.m.Y H:i] ')
        . (string)($config['bark_alarm_body'] ?? 'Manuelle Alarmierung ueber das NAS-Dashboard!');

    $results = [];
    $okCount = 0;
    foreach ($keys as $key) {
        $result = bark_send_alarm_one($config, $key, $body);
        if ($result['ok']) {
            $okCount++;
        }
        $results[] = $result;
    }
    return [
        'ok' => $okCount === count($keys),
        // Deutlich machen, wenn die statische Fallback-Liste zum Einsatz kam -
        // sonst glaubt man, die (leere) Dashboard-Liste sei massgeblich gewesen.
        'message' => ($demoActive ? 'DEMO-MODUS: nur Test-Empfaenger alarmiert. ' : '')
            . sprintf('%d/%d Empfaenger erreicht.', $okCount, count($keys))
            . ($usedFallback ? ' ACHTUNG: Fallback-Liste aus config.php verwendet, die Dashboard-Liste ist leer!' : ''),
        'results' => $results,
    ];
}
