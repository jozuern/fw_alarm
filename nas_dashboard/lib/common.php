<?php
declare(strict_types=1);

ini_set('display_errors', '0');
date_default_timezone_set('Europe/Berlin');

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

function is_dashboard_authed(): bool
{
    return !empty($_SESSION['authed']);
}

function require_dashboard_auth(): void
{
    if (!is_dashboard_authed()) {
        http_response_code(401);
        json_response(['ok' => false]);
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
    $prefix = is_file($path) ? '' : DATA_FILE_GUARD;
    @file_put_contents(
        $path,
        $prefix . json_encode($entry, JSON_UNESCAPED_SLASHES) . PHP_EOL,
        FILE_APPEND | LOCK_EX
    );
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

// ============================ BARK-DIREKTVERSAND ============================
// Der manuelle REAL ALARM läuft absichtlich DIREKT vom NAS zu Bark: Der
// Hauptzweck einer manuellen Alarmierung ist ja gerade der Fall, dass der
// ESP32 oder seine Kette klemmt.

// Nur die ersten 4 Zeichen eines Keys zeigen/loggen - Keys sind Geheimnisse.
function mask_bark_key(string $key): string
{
    return substr($key, 0, 4) . '...';
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
    // Führend ist die im Dashboard gepflegte Liste; die statische Liste aus
    // config.php dient nur noch als Fallback, solange noch keine gepflegt ist.
    $keys = managed_alarm_keys($config);
    if (count($keys) === 0) {
        $fallback = $config['bark_keys_alarm'] ?? [];
        if (!is_array($fallback)) {
            $fallback = [];
        }
        $keys = array_values(array_filter(array_map('trim', array_map('strval', $fallback))));
    }
    if (count($keys) === 0) {
        return ['ok' => false, 'message' => 'Keine Alarm-Empfaenger: Liste im Dashboard pflegen (Panel "Alarm-Empfaenger").', 'results' => []];
    }

    // Genug Laufzeit für viele Keys mit Wiederholungen (Standardlimit: 30 s).
    @set_time_limit(30 + count($keys) * 35);

    // Zeitstempel wie in der Firmware; Bark-Texte bewusst ASCII (ae/ue/oe).
    $body = date('[d.m.Y H:i] ') . (string)($config['bark_alarm_body'] ?? 'Manuelle Alarmierung ueber das NAS-Dashboard!');

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
        'message' => sprintf('%d/%d Empfaenger erreicht.', $okCount, count($keys)),
        'results' => $results,
    ];
}
