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
    // Privates Betriebs-Werkzeug im offenen Internet: Suchmaschinen sollen
    // weder die Login-Seite noch API-Antworten indexieren.
    header('X-Robots-Tag: noindex, nofollow');
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
    $dir = rtrim((string)($config['data_dir'] ?? ''), '/');
    // Nur absolute Pfade: Ein relativer Pfad würde je nach Skript (Dashboard,
    // api/, cron/) in einem ANDEREN Ordner landen - der ESP32-Status wäre im
    // Dashboard unsichtbar. Ein leerer Pfad ließe den root-Cron nach / schreiben.
    if ($dir === '' || $dir[0] !== '/') {
        error_log('fw_alarm: data_dir in config.php fehlt oder ist kein absoluter Pfad.');
        http_response_code(500);
        exit('Konfiguration fehlerhaft: data_dir muss ein absoluter Pfad sein.');
    }
    if (!is_dir($dir)) {
        mkdir($dir, 0750, true);
        fix_owner($dir, dirname($dir));   // Vorbild ist der ELTERN-Ordner
    }
    // Falls Nginx-Verzeichnislisting (autoindex) aktiv sein sollte, verhindert
    // diese Datei, dass jemand die Namen der Laufzeitdateien aufzählen kann.
    $guard = $dir . '/index.php';
    if (!is_file($guard)) {
        @file_put_contents($guard, "<?php http_response_code(404); exit;\n");
        fix_owner($guard, $dir);
    }
    return $dir;
}

// Der Offline-Wächter (cron/) läuft als root. Legt er eine Datei im data_dir
// NEU an (Log nach der Rotation, *.bak, Lock-Datei), gehörte sie sonst root.
// Gehört der data_dir dem Web-Benutzer selbst (klassische Unix-Rechte), könnte
// der sie danach nicht mehr beschreiben (Log-Einträge verschwänden still,
// Lock-Dateien führten zu HTTP 500). Deshalb übernimmt eine als root angelegte
// Datei Besitzer und Gruppe des Ordners.
// BEWUSST zurückhaltend - die Funktion darf Zugriff nie WEGNEHMEN:
// - Die Rechte-Bits (chmod) bleiben unangetastet. Auf Synology kommt der
//   Zugriff von 'http' meist über File-Station-Berechtigungen (ACL) oder über
//   die "andere"-Bits; ein chmod könnte beides aushebeln.
// - Gehört der Ordner root, wird der Besitzer nicht geändert (dann regeln
//   ohnehin ACLs den Zugriff, und alles bleibt wie vom System angelegt).
function fix_owner(string $path, ?string $dir = null): void
{
    if (!function_exists('posix_geteuid') || posix_geteuid() !== 0 || !file_exists($path)) {
        return;
    }
    $ref = $dir ?? dirname($path);
    $uid = @fileowner($ref);
    $gid = @filegroup($ref);
    if ($uid === false || $uid === 0) {
        return;
    }
    if (@fileowner($path) !== $uid) {
        @chown($path, $uid);
    }
    if ($gid !== false && @filegroup($path) !== $gid) {
        @chgrp($path, $gid);
    }
}

// Laufzeitdateien, die der Web-Benutzer AN ORT UND STELLE beschreibt (anhängen,
// überschreiben, Lock öffnen). Alle anderen ersetzt er per rename() - dafür
// zählt allein das Schreibrecht auf den Ordner. 'watchdog.lock' fehlt bewusst:
// Den nimmt nur der Cron-Wächter.
function inplace_data_files(array $config): array
{
    $names = ['commands.log', 'alarm_keys.json.bak', 'demo_mode.json.bak',
        'keys.lock', 'command.lock', 'status.lock', 'log.lock', 'notify.lock',
        'login_throttle.lock', 'mute_throttle.lock', 'health.lock'];
    return array_map(static fn(string $name): string => data_path($config, $name), $names);
}

// Laufzeitdateien bekommen die Endung .php (siehe DATA_FILE_GUARD oben).
function data_path(array $config, string $name): string
{
    return data_dir($config) . '/' . $name . '.php';
}

// Liest eine Laufzeit-JSON-Datei. $status meldet zusätzlich, WARUM ggf. der
// Default kam: 'ok', 'missing' (Datei gibt es nicht) oder 'corrupt' (leer,
// unlesbar oder kein gültiges JSON). Das Unterscheiden ist wichtig: Eine
// kaputte Empfängerliste darf NICHT still wie eine leere behandelt werden.
function read_json_file(string $path, array $default, ?string &$status = null): array
{
    $status = 'ok';
    if (!is_file($path)) {
        $status = 'missing';
        return $default;
    }
    $raw = @file_get_contents($path);
    if ($raw === false || $raw === '') {
        $status = 'corrupt';
        error_log('fw_alarm: Laufzeitdatei leer/unlesbar: ' . basename($path));
        return $default;
    }
    // Guard-Zeile (erste Zeile) überspringen.
    if (str_starts_with($raw, '<?php')) {
        $nl = strpos($raw, "\n");
        $raw = $nl === false ? '' : substr($raw, $nl + 1);
    }
    $data = json_decode($raw, true);
    if (!is_array($data)) {
        $status = 'corrupt';
        error_log('fw_alarm: Laufzeitdatei kein gültiges JSON: ' . basename($path));
        return $default;
    }
    return $data;
}

// Schreibt atomar (tmp + fsync + rename). Gibt false zurück, wenn irgendetwas
// fehlschlägt - die Aufrufer melden das als HTTP 500 an den ESP32/Browser,
// statt einen Erfolg vorzutäuschen. Die Temp-Datei endet ebenfalls auf .php
// (Guard greift auch für eine liegengebliebene Leiche) und hat einen
// zufälligen Namen (kein Konflikt mit einer fremden, z.B. root-eigenen Leiche).
function write_json_file(string $path, array $data): bool
{
    $json = json_encode($data, JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES | JSON_INVALID_UTF8_SUBSTITUTE);
    if ($json === false) {
        return false;
    }
    $base = str_ends_with($path, '.php') ? substr($path, 0, -4) : $path;
    $tmp = $base . '.tmp-' . bin2hex(random_bytes(4)) . '.php';
    $payload = DATA_FILE_GUARD . $json;
    $handle = @fopen($tmp, 'xb');
    if ($handle === false) {
        return false;
    }
    $ok = @fwrite($handle, $payload) === strlen($payload) && @fflush($handle);
    if ($ok && function_exists('fsync')) {
        $ok = @fsync($handle);
    }
    @fclose($handle);
    if (!$ok) {
        @unlink($tmp);
        return false;
    }
    fix_owner($tmp);
    if (!@rename($tmp, $path)) {
        @unlink($tmp);
        return false;
    }
    return true;
}

// Wie write_json_file, hält aber vorher die letzte Version als *.bak-Datei
// fest (ebenfalls .php-guarded). Gedacht für die Empfängerliste/Demo-Zustand:
// Die Liste ist die EINE Quelle für beide Alarmwege - geht die Datei kaputt,
// greift load_alarm_keys() automatisch auf die *.bak zurück. Gesichert wird
// nur ein LESBARER Stand - sonst würde eine kaputte Datei die letzte gute
// Sicherung überschreiben.
function backup_then_write(array $config, string $name, array $data): bool
{
    $path = data_path($config, $name);
    if (is_file($path)) {
        read_json_file($path, [], $status);
        if ($status === 'ok') {
            $bak = data_path($config, $name . '.bak');
            if (@copy($path, $bak)) {
                fix_owner($bak);
            }
        }
    }
    return write_json_file($path, $data);
}

// Fehlermeldung, wenn eine Lock-Datei nicht zu öffnen ist (meist fehlendes
// Schreibrecht der Gruppe http auf den data_dir).
const LOCK_FAILED_MESSAGE = 'Fehler: Datenordner nicht beschreibbar (Berechtigungen der Gruppe http prüfen).';

function open_lock(array $config, string $name)
{
    $lockPath = data_path($config, $name . '.lock');
    $existed = is_file($lockPath);
    $handle = @fopen($lockPath, 'c');
    if ($handle === false) {
        // Häufigste Ursache (Störung 10-11/2026): Ein Web-Station-/DSM-Update
        // hat der Gruppe 'http' das Schreibrecht auf den data_dir entzogen.
        // Den Pfad gibt es nur im PHP-Fehlerlog, nicht im Browser (Struktur-Leak).
        error_log('fw_alarm: Lock-Datei nicht beschreibbar: ' . $lockPath);
        return false;
    }
    if (!$existed) {
        fix_owner($lockPath);
    }
    flock($handle, LOCK_EX);
    return $handle;
}

function with_lock(array $config, string $name, callable $callback)
{
    $handle = open_lock($config, $name);
    if ($handle === false) {
        http_response_code(500);
        // Dashboard-API (JSON-Aufrufer) bekommt JSON - sonst sähe der Browser
        // nur eine unlesbare Antwort und zeigte "NAS nicht erreichbar".
        if (defined('FW_ALARM_JSON_API')) {
            json_response(['ok' => false, 'code' => 'storage', 'message' => LOCK_FAILED_MESSAGE]);
        }
        if (!headers_sent()) {
            header('Content-Type: text/plain; charset=utf-8');
        }
        exit(LOCK_FAILED_MESSAGE);
    }
    try {
        return $callback();
    } finally {
        flock($handle, LOCK_UN);
        fclose($handle);
    }
}

// Wie with_lock, aber NICHT fatal: Lässt sich der Lock nicht öffnen, kommt
// $onFail zurück statt eines exit(). Für alles, was nur "best effort" ist und
// einen wichtigeren Pfad nicht mitreißen darf - vor allem den manuellen
// REAL ALARM und den Login (Störung 10/2026: kein Schreibrecht -> früher
// brachen beide mit HTTP 500 ab, obwohl sie gar nicht schreiben müssen).
function with_lock_soft(array $config, string $name, callable $callback, $onFail = null)
{
    $handle = open_lock($config, $name);
    if ($handle === false) {
        return $onFail;
    }
    try {
        return $callback();
    } finally {
        flock($handle, LOCK_UN);
        fclose($handle);
    }
}

// Arbeit NACH der Antwort erledigen (z.B. Bark-Info-Pushs, bis ~20 s): Der
// Aufrufer (ESP32 mit 4-s-Timeout, Browser, manueller Alarm) wartet so nicht
// darauf. fastcgi_finish_request() schließt die Verbindung zum Client, das
// Skript läuft danach weiter. In der CLI (Cron) laufen die Jobs am Ende.
function defer_after_response(callable $job): void
{
    static $jobs = null;
    if ($jobs === null) {
        $jobs = [];
        register_shutdown_function(static function () use (&$jobs): void {
            if (PHP_SAPI !== 'cli' && function_exists('fastcgi_finish_request')) {
                @fastcgi_finish_request();
            }
            ignore_user_abort(true);
            // while statt foreach: Jobs, die ein Job selbst noch anmeldet,
            // laufen so ebenfalls.
            while (($deferred = array_shift($jobs)) !== null) {
                try {
                    $deferred();
                } catch (Throwable $e) {
                    error_log('fw_alarm: Nachlauf-Job fehlgeschlagen: ' . $e->getMessage());
                }
            }
        });
    }
    $jobs[] = $job;
}

// Selbsttest für den data_dir: Kann DIESER PHP-Prozess dort Lock-Dateien
// öffnen und Dateien schreiben, zurücklesen und löschen? Gedacht für
// api/health.php - also für den echten Web-Benutzer (http). Der Cron-Wächter
// läuft als root und würde einen Rechteverlust selbst nie bemerken.
// Liefert ['ok' => bool, 'step' => 'ok'|'lock'|'write'|'read'|'delete'|'perm'].
// 'perm' (dann zusätzlich 'file' => Dateiname): Eine BESTEHENDE Datei, die das
// Dashboard an Ort und Stelle beschreiben muss (Verlauf, Lock, *.bak - siehe
// inplace_data_files()), ist für diesen Benutzer nicht beschreibbar, z.B. weil
// der root-Cron sie angelegt hat. Das Anlegen neuer Dateien allein sagt
// darüber nichts. Geprüft werden NUR diese Dateien: Alles andere ersetzt das
// Dashboard per rename() - ein fremder Besitzer wäre dort kein Fehler.
function data_dir_write_check(array $config): array
{
    foreach (inplace_data_files($config) as $file) {
        if (is_file($file) && !is_writable($file)) {
            error_log('fw_alarm: Laufzeitdatei nicht beschreibbar: ' . basename($file));
            return ['ok' => false, 'step' => 'perm', 'file' => basename($file)];
        }
    }
    $handle = @fopen(data_path($config, 'health.lock'), 'c');
    if ($handle === false) {
        return ['ok' => false, 'step' => 'lock'];
    }
    flock($handle, LOCK_EX);
    try {
        $path = data_path($config, 'health_probe.json');
        $nonce = bin2hex(random_bytes(8));
        if (!write_json_file($path, ['nonce' => $nonce, 'at' => time()])) {
            return ['ok' => false, 'step' => 'write'];
        }
        if ((read_json_file($path, [])['nonce'] ?? '') !== $nonce) {
            return ['ok' => false, 'step' => 'read'];
        }
        if (!@unlink($path)) {
            return ['ok' => false, 'step' => 'delete'];
        }
        return ['ok' => true, 'step' => 'ok'];
    } finally {
        flock($handle, LOCK_UN);
        fclose($handle);
    }
}

// Ruft api/health.php über den Webserver auf (also als Web-Benutzer, mit
// echtem PHP-Profil) und wertet die Antwort aus. Für den Cron-Wächter.
// Liefert ['ok' => bool, 'detail' => kurzer ASCII-Text für Bark/Verlauf].
function web_health_check(array $config): array
{
    $url = (string)($config['health_check_url'] ?? 'https://127.0.0.1/fw_alarm/api/health.php');
    if (!function_exists('curl_init')) {
        return ['ok' => false, 'detail' => 'PHP-cURL fehlt (Selbsttest nicht moeglich)'];
    }
    $ch = curl_init($url);
    curl_setopt_array($ch, [
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_CONNECTTIMEOUT => 5,
        CURLOPT_TIMEOUT => 10,
        // Aufruf geht an 127.0.0.1 - das Let's-Encrypt-Zertifikat lautet auf
        // den DDNS-Namen und passt hier nie. Es verlässt das NAS nicht.
        CURLOPT_SSL_VERIFYPEER => false,
        CURLOPT_SSL_VERIFYHOST => 0,
    ]);
    $body = curl_exec($ch);
    $http = (int)curl_getinfo($ch, CURLINFO_HTTP_CODE);
    $err = curl_error($ch);
    curl_close($ch);

    // 'blocks_pushes': Können wegen dieser Störung auch die Status-Pushs der
    // Box nicht gespeichert werden? Davon hängt ab, ob eine "Box OFFLINE"-
    // Meldung das NAS als Ursache nennen darf.
    if ($body === false) {
        return ['ok' => false, 'blocks_pushes' => true,
            'detail' => 'Web Station nicht erreichbar (' . $err . ')'];
    }
    if ($http === 200 && preg_match('/^ok=1$/m', (string)$body)) {
        return ['ok' => true, 'blocks_pushes' => false, 'detail' => 'ok'];
    }
    if (preg_match('/^step=(\w+)$/m', (string)$body, $m)) {
        if ($m[1] === 'perm') {
            // Nur EINE Datei ist betroffen - der Ordner selbst ist beschreibbar.
            $file = preg_match('/^file=([\w.\-]+)$/m', (string)$body, $fm) ? $fm[1] : '?';
            return ['ok' => false, 'step' => 'perm', 'file' => $file,
                'blocks_pushes' => str_starts_with($file, 'status.lock'),
                'detail' => 'Laufzeitdatei ' . $file . ' ist fuer den Web-Benutzer nicht beschreibbar'];
        }
        $steps = [
            'lock' => 'Lock-Datei',
            'write' => 'Schreiben',
            'read' => 'Zuruecklesen',
            'delete' => 'Loeschen',
        ];
        return ['ok' => false, 'step' => $m[1], 'blocks_pushes' => true,
            'detail' => 'Datenordner nicht beschreibbar (Schritt: '
                . ($steps[$m[1]] ?? $m[1]) . ', HTTP ' . $http . ')'];
    }
    return ['ok' => false, 'blocks_pushes' => true, 'detail' => 'Selbsttest lieferte HTTP ' . $http];
}

// Maschinen-Token NUR per POST akzeptieren: GET-Parameter landen sonst im
// Klartext in Webserver-Logs.
function require_machine_auth(array $config): void
{
    $token = (string)($_POST['token'] ?? '');
    // Hash tolerant vergleichen: Manche Tools (z.B. PowerShell Get-FileHash)
    // geben ihn in GROSSBUCHSTABEN aus, beim Kopieren rutschen Leerzeichen mit.
    $expected = strtolower(trim((string)($config['machine_token_sha256'] ?? '')));
    if ($token === '' || $expected === '' || !hash_equals($expected, hash('sha256', $token))) {
        http_response_code(403);
        exit('FORBIDDEN');
    }
}

// ======================= BREMSE PRO ABSENDER ================================
// sleep(1) allein lässt sich mit vielen parallelen Verbindungen aushebeln.
// Deshalb zusätzlich: Nach zu vielen Versuchen wird der Absender für ein
// Zeitfenster gesperrt. Genutzt für den Login (jeder Versuch zählt, Erfolg
// setzt zurück) und für api/mute.php (nur unbekannte Tokens zählen).
// Alle Lock-Zugriffe sind NICHT fatal: Fehlt das Schreibrecht auf den
// data_dir, wird nur nicht gebremst - Login und Alarm bleiben erreichbar.

function login_client_ip(): string
{
    return (string)($_SERVER['REMOTE_ADDR'] ?? 'unknown');
}

// Schlüssel, unter dem ein Absender gezählt wird: IPv4 einzeln, IPv6 als
// ganzes /64 - ein Angreifer bekommt sonst pro Adresse seines Präfixes ein
// neues Budget. IPv4-mapped IPv6 (::ffff:a.b.c.d) zählt wie die IPv4.
function client_throttle_key(): string
{
    $ip = login_client_ip();
    $bin = @inet_pton($ip);
    if ($bin !== false && strlen($bin) === 16) {
        if (substr($bin, 0, 12) === str_repeat("\0", 10) . "\xff\xff") {
            return (string)inet_ntop(substr($bin, 12));
        }
        return inet_ntop(substr($bin, 0, 8) . str_repeat("\0", 8)) . '/64';
    }
    return $ip;
}

function login_throttle_limits(array $config): array
{
    return [
        max(1, (int)($config['login_max_failures'] ?? 8)),
        max(60, (int)($config['login_lockout_window_seconds'] ?? 900)),
    ];
}

// Hat dieser Absender sein Budget im aktuellen Fenster aufgebraucht? (Nur
// lesend, ohne Lock - für den schnellen Vorab-Check.)
function throttle_blocked(array $config, string $bucket, int $max, int $window): bool
{
    $entry = read_json_file(data_path($config, $bucket . '_throttle.json'), [])[client_throttle_key()] ?? null;
    if (!is_array($entry) || time() - (int)($entry['first_at'] ?? 0) > $window) {
        return false;
    }
    return (int)($entry['count'] ?? 0) >= $max;
}

// Zählt einen Versuch des Absenders - atomar, d.h. Prüfen und Zählen in EINEM
// Schritt unter dem Lock (N parallele Requests bekommen so nicht N Versuche).
// Liefert ['blocked' => bool, 'count' => int]. Bei 'blocked' wird nicht
// weitergezählt.
function throttle_hit(array $config, string $bucket, int $max, int $window): array
{
    return with_lock_soft($config, $bucket . '_throttle', function () use ($config, $bucket, $max, $window): array {
        $path = data_path($config, $bucket . '_throttle.json');
        $all = read_json_file($path, []);
        $now = time();
        foreach ($all as $who => $entry) {   // abgelaufene Fenster aufräumen
            if (!is_array($entry) || $now - (int)($entry['first_at'] ?? 0) > $window) {
                unset($all[$who]);
            }
        }
        $who = client_throttle_key();
        $entry = is_array($all[$who] ?? null) ? $all[$who] : ['count' => 0, 'first_at' => $now];
        if ((int)$entry['count'] >= $max) {
            return ['blocked' => true, 'count' => (int)$entry['count']];
        }
        $entry['count'] = (int)$entry['count'] + 1;
        $all[$who] = $entry;
        write_json_file($path, $all);
        return ['blocked' => false, 'count' => (int)$entry['count']];
    }, ['blocked' => false, 'count' => 0]);
}

function throttle_clear(array $config, string $bucket): void
{
    $path = data_path($config, $bucket . '_throttle.json');
    // Billiger Vorab-Check ohne Lock: Meist gibt es gar keinen Eintrag - dann
    // muss ein erfolgreicher Login auch nichts schreiben (und scheitert nicht
    // an fehlendem Schreibrecht).
    if (!isset(read_json_file($path, [])[client_throttle_key()])) {
        return;
    }
    with_lock_soft($config, $bucket . '_throttle', function () use ($path): void {
        $all = read_json_file($path, []);
        unset($all[client_throttle_key()]);
        write_json_file($path, $all);
    });
}

// Login: Versuch VOR der Passwortprüfung reservieren. Liefert
// ['blocked' => bool, 'count' => int].
function login_throttle_attempt(array $config): array
{
    [$max, $window] = login_throttle_limits($config);
    return throttle_hit($config, 'login', $max, $window);
}

function login_throttle_clear(array $config): void
{
    throttle_clear($config, 'login');
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
// (nur ansehen). Fehlt die Rolle, gilt die SICHERE Richtung 'readonly'
// (fail-closed): Der Login setzt sie immer, und Sitzungen von vor der
// Rollen-Funktion sind längst über das 12-h-Idle-Timeout abgelaufen.
function dashboard_role(): string
{
    $role = (string)($_SESSION['role'] ?? '');
    return $role === 'admin' ? 'admin' : 'readonly';
}

// Auslösende/ändernde Aktionen sind dem Admin vorbehalten. Der Lese-Benutzer
// wird hier serverseitig gestoppt - die ausgeblendeten Knöpfe im UI sind nur
// Komfort, kein Schutz.
function require_dashboard_admin(): void
{
    if (dashboard_role() !== 'admin') {
        http_response_code(403);
        json_response(['ok' => false, 'code' => 'readonly',
            'message' => 'Nur Lesezugriff: Dieser Benutzer darf nichts auslösen oder ändern.']);
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
        json_response(['ok' => false, 'code' => 'csrf',
            'message' => 'Sicherheits-Token veraltet – Seite neu laden. Aktion wurde NICHT ausgeführt.']);
    }
}

function json_response(array $payload): void
{
    if (!headers_sent()) {
        header('Content-Type: application/json; charset=utf-8');
    }
    // INVALID_UTF8_SUBSTITUTE: Ein kaputtes Byte (z.B. ein abgeschnittenes
    // Umlaut-Zeichen) darf nie die GANZE Antwort leer machen.
    echo json_encode($payload, JSON_UNESCAPED_SLASHES | JSON_INVALID_UTF8_SUBSTITUTE);
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
    $line = json_encode($entry, JSON_UNESCAPED_SLASHES | JSON_INVALID_UTF8_SUBSTITUTE) . PHP_EOL;
    // Rotation + Anhängen unter EINEM Lock: Sonst konnte ein paralleler
    // Prozess die gerade rotierte Datei wieder löschen (5 MB Verlauf weg).
    // Nicht fatal - ein fehlender Verlaufseintrag darf nie einen Alarm- oder
    // Statuspfad abbrechen.
    $written = with_lock_soft($config, 'log', function () use ($config, $line): bool {
        $path = data_path($config, 'commands.log');
        // Rotation: sonst füllt das Log über Jahre die Platte, und volle Platte
        // heißt: Status-/Befehlsdateien lassen sich nicht mehr schreiben.
        if (is_file($path) && (int)@filesize($path) > 5 * 1024 * 1024) {
            $old = data_path($config, 'commands.log.1');
            @unlink($old);
            @rename($path, $old);
        }
        $isNew = !is_file($path);
        $ok = @file_put_contents($path, ($isNew ? DATA_FILE_GUARD : '') . $line, FILE_APPEND | LOCK_EX) !== false;
        if ($isNew) {
            fix_owner($path);   // vom root-Cron angelegt? -> http muss weiter anhängen können
        }
        return $ok;
    }, false);
    if (!$written) {
        error_log('fw_alarm: Verlaufseintrag konnte nicht geschrieben werden: ' . (string)($entry['event'] ?? '?'));
    }
}

// Liest die letzten Einträge aus commands.log für das Verlaufs-Panel.
// Liest das Datei-Ende rückwärts in Blöcken, bis genug Einträge da sind
// (auch große nas_alarm-Zeilen), und nimmt bei Bedarf die rotierte Datei
// commands.log.1 dazu. Neueste Einträge zuerst.
function read_command_log_tail(array $config, int $limit = 25): array
{
    $entries = [];
    foreach (['commands.log', 'commands.log.1'] as $name) {
        $need = $limit - count($entries);
        if ($need <= 0) {
            break;
        }
        $entries = array_merge($entries, read_log_file_tail(data_path($config, $name), $need));
    }
    return $entries;
}

// Grobe Einteilung der Verlaufs-Ereignisse für den Filter im Dashboard:
// 'alarm' (Relaisalarm, REAL ALARM, Entwarnung, Demo-Alarm-Befehle), 'keys'
// (alles an der Empfängerliste, inkl. Stumm-Wechsel und Test-Push) und
// 'system' (TEST-Befehle, Demo-Modus, Wächter-Ereignisse). Ohne diesen
// Filter verdrängen die täglichen Stumm-Wechsel der Kurzbefehle die Alarme
// aus dem sichtbaren Fenster.
function log_event_category(array $entry): string
{
    $event = (string)($entry['event'] ?? '');
    if (in_array($event, ['relay_alarm', 'nas_alarm', 'nas_alarm_started',
        'false_alarm', 'false_alarm_started'], true)) {
        return 'alarm';
    }
    if (str_starts_with($event, 'key_')) {
        return 'keys';
    }
    if (in_array($event, ['created', 'delivered', 'acked', 'expired', 'cancelled'], true)) {
        $type = is_array($entry['command'] ?? null) ? (string)($entry['command']['type'] ?? '') : '';
        return $type === 'ALARM' ? 'alarm' : 'system';
    }
    return 'system';
}

// Billige Vorprüfung an der ROHEN Log-Zeile: Kann sie zur Kategorie gehören?
// Erspart das JSON-Dekodieren der vielen Zeilen, die es sicher nicht tun
// (maßgeblich bleibt danach log_event_category()). Verlässt sich darauf, dass
// json_encode Anführungszeichen in Werten maskiert - ein Name kann die
// Zeichenfolge '"event":"key_' also nicht vortäuschen.
function log_line_may_match(string $line, string $category): bool
{
    $isKey = strpos($line, '"event":"key_') !== false;
    $isAlarmEvent = strpos($line, '"event":"relay_alarm"') !== false
        || strpos($line, '"event":"nas_alarm') !== false
        || strpos($line, '"event":"false_alarm') !== false;
    if ($category === 'keys') {
        return $isKey;
    }
    if ($category === 'alarm') {
        return $isAlarmEvent || strpos($line, '"type":"ALARM"') !== false;
    }
    return !$isKey && !$isAlarmEvent;   // 'system': der Rest
}

// Geht die Zeilen einer Log-Datei vom ENDE her durch (neueste zuerst), in
// Blöcken, ohne die ganze Datei zu laden. $onLine(string): bool - false
// beendet den Durchlauf. Liest höchstens $maxBytes; Rückgabe = gelesene Bytes.
function scan_log_file_reverse(string $path, callable $onLine, int $maxBytes): int
{
    if ($maxBytes <= 0 || !is_file($path)) {
        return 0;
    }
    $handle = @fopen($path, 'rb');
    if ($handle === false) {
        return 0;
    }
    $offset = (int)@filesize($path);
    $done = 0;
    $carry = '';
    while ($offset > 0 && $done < $maxBytes) {
        $read = min(64 * 1024, $offset, $maxBytes - $done);
        $offset -= $read;
        fseek($handle, $offset);
        $lines = explode("\n", (string)fread($handle, $read) . $carry);
        $done += $read;
        // Die erste Zeile des Blocks ist evtl. angeschnitten - sie gehört
        // zum nächsten (älteren) Block. Am Dateianfang ist sie vollständig.
        $carry = $offset > 0 ? (string)array_shift($lines) : '';
        for ($i = count($lines) - 1; $i >= 0; $i--) {
            $line = trim($lines[$i]);
            if ($line === '' || str_starts_with($line, '<?php')) {
                continue;   // Leerzeile bzw. Guard-Zeile
            }
            if ($onLine($line) === false) {
                fclose($handle);
                return $done;
            }
        }
    }
    fclose($handle);
    return $done;
}

// Wie read_command_log_tail(), aber nur Einträge einer Kategorie - neueste
// zuerst, bis $limit beisammen sind. Sucht dafür rückwärts durch das Log und
// die rotierte Datei; früher endete die Suche nach 1500 Zeilen, wodurch
// ältere Alarme hinter vielen Stumm-Wechseln wieder unsichtbar wurden - genau
// das, was der Filter verhindern soll. Gedeckelt auf $maxBytes insgesamt,
// damit der Abruf auch auf einem kleinen NAS billig bleibt (dekodiert werden
// nur Zeilen, die die Vorprüfung bestehen).
function read_command_log_filtered(array $config, int $limit, string $category, int $maxBytes = 4194304): array
{
    $out = [];
    $budget = $maxBytes;
    foreach (['commands.log', 'commands.log.1'] as $name) {
        if (count($out) >= $limit || $budget <= 0) {
            break;
        }
        $budget -= scan_log_file_reverse(data_path($config, $name),
            static function (string $line) use (&$out, $limit, $category): bool {
                if (!log_line_may_match($line, $category)) {
                    return true;
                }
                $entry = json_decode($line, true);
                if (is_array($entry) && log_event_category($entry) === $category) {
                    $out[] = $entry;
                }
                return count($out) < $limit;
            }, $budget);
    }
    return $out;
}

// Neueste zuerst; höchstens $limit Einträge, höchstens 1 MB gelesen.
function read_log_file_tail(string $path, int $limit): array
{
    if ($limit <= 0 || !is_file($path)) {
        return [];
    }
    $handle = @fopen($path, 'rb');
    if ($handle === false) {
        return [];
    }
    $size = (int)@filesize($path);
    $chunk = 64 * 1024;
    $maxRead = 1024 * 1024;
    $offset = $size;
    $data = '';
    // Rückwärts blockweise lesen, bis genug vollständige Zeilen beisammen sind.
    while ($offset > 0 && $size - $offset < $maxRead) {
        $read = min($chunk, $offset);
        $offset -= $read;
        fseek($handle, $offset);
        $data = (string)fread($handle, $read) . $data;
        if (substr_count($data, "\n") > $limit + 1) {
            break;
        }
    }
    fclose($handle);

    $lines = explode("\n", $data);
    if ($offset > 0) {
        array_shift($lines);   // erste Zeile ist evtl. angeschnitten
    }
    $entries = [];
    foreach ($lines as $line) {
        $line = trim($line);
        if ($line === '' || str_starts_with($line, '<?php')) {
            continue;   // Guard-Zeile überspringen
        }
        $decoded = json_decode($line, true);
        if (is_array($decoded)) {
            $entries[] = $decoded;
        }
    }
    return array_reverse(array_slice($entries, -$limit));
}

// ============================ EMPFÄNGERLISTE ================================
// Die Alarm-Empfänger werden im Dashboard gepflegt und liegen versioniert in
// alarm_keys.json(.php). Der ESP32 sieht die Version im Poll und holt sich bei
// Abweichung die Liste über api/keys.php (persistiert sie dann im NVS-Flash).
// Der NAS-Direktalarm nutzt DIESELBE Liste -> eine Quelle für beide Alarmwege.

// Obergrenze der Empfängerliste - identisch zu MAX_ALARM_KEYS in der Firmware.
const MAX_ALARM_KEYS = 10;

// Meldung, wenn die Empfängerliste UND ihre Sicherung unlesbar sind.
const ALARM_KEYS_CORRUPT_MESSAGE = 'Empfängerliste beschädigt (alarm_keys.json und .bak unlesbar) – '
    . 'nichts geändert. Bitte die Dateien im data_dir prüfen.';

function alarm_keys_default(): array
{
    return ['next_id' => 1, 'version' => 0, 'keys' => []];
}

// Liest die Empfängerliste. Ist alarm_keys.json kaputt, wird automatisch die
// letzte gute Sicherung (*.bak) genommen. null = BEIDE unbrauchbar - Schreiber
// müssen dann abbrechen, statt eine leere Liste "neu anzufangen" (das würde
// die Version zurücksetzen und die gute Sicherung überschreiben).
function read_alarm_keys_state(array $config): ?array
{
    $state = read_json_file(data_path($config, 'alarm_keys.json'), alarm_keys_default(), $status);
    if ($status !== 'corrupt') {
        return $state;
    }
    $bak = read_json_file(data_path($config, 'alarm_keys.json.bak'), alarm_keys_default(), $bakStatus);
    if ($bakStatus === 'ok') {
        error_log('fw_alarm: alarm_keys.json unlesbar - nutze alarm_keys.json.bak');
        return $bak;
    }
    error_log('fw_alarm: alarm_keys.json UND .bak unlesbar');
    return null;
}

// Für Leser (Alarm, Anzeige): im Notfall lieber eine leere Liste (-> Fallback
// aus config.php) als ein Abbruch.
function load_alarm_keys(array $config): array
{
    return read_alarm_keys_state($config) ?? alarm_keys_default();
}

// Erhöht die Listen-Version - und zwar über ALLES hinaus, was je vergeben
// wurde (Hochwassermarke in einer eigenen Datei). Wird die Liste aus einer
// .bak zurückgeholt, läuft die Version so nie rückwärts auf eine Nummer, die
// der ESP32 schon mit ANDEREM Inhalt kennt (er würde dann nie neu holen).
// Muss unter dem 'keys'-Lock aufgerufen werden.
function bump_keys_version(array $config, array &$state): int
{
    $hwPath = data_path($config, 'alarm_keys_version.json');
    $hw = (int)(read_json_file($hwPath, [])['high_water'] ?? 0);
    $next = max((int)($state['version'] ?? 0), $hw) + 1;
    $state['version'] = $next;
    write_json_file($hwPath, ['high_water' => $next]);
    return $next;
}

// Position eines Eintrags (per id) in der Liste, -1 = nicht gefunden.
function find_key_index(array $state, int $id): int
{
    foreach (($state['keys'] ?? []) as $i => $entry) {
        if (is_array($entry) && (int)($entry['id'] ?? 0) === $id) {
            return (int)$i;
        }
    }
    return -1;
}

// Lesen-Ändern-Schreiben der Empfängerliste unter dem 'keys'-Lock - das eine
// Muster für alle Schreiber (Dashboard, Kurzbefehl-Link). $mutate bekommt den
// Zustand per Referenz und liefert das Ergebnis-Array. Setzt es 'write' =>
// true, wird gespeichert (mit 'bump' => true unter neuer Listen-Version, die
// dann als 'version' im Ergebnis steht). Ist die Liste samt Sicherung
// unlesbar, wird NICHTS geschrieben.
function mutate_alarm_keys(array $config, callable $mutate): array
{
    return with_lock($config, 'keys', function () use ($config, $mutate): array {
        $state = read_alarm_keys_state($config);
        if ($state === null) {
            return ['ok' => false, 'message' => ALARM_KEYS_CORRUPT_MESSAGE];
        }
        $result = $mutate($state);
        $write = !empty($result['write']);
        $bump = !empty($result['bump']);
        unset($result['write'], $result['bump']);
        if (!$write) {
            return $result;
        }
        $result['version'] = $bump ? bump_keys_version($config, $state) : (int)($state['version'] ?? 0);
        if (!backup_then_write($config, 'alarm_keys.json', $state)) {
            return ['ok' => false, 'message' => 'Liste konnte nicht gespeichert werden.', 'write_failed' => true];
        }
        return $result;
    });
}

// Darf ein DEMO-ALARM über den ESP32 laufen? Nur im Demo-Modus, und nur wenn
// der letzte Status-Push des ESP32 (a) die aktuelle Listen-Version bestätigt,
// (b) genau EINEN Empfänger meldet (= die Demo-Liste - schützt auch vor einem
// halb geschriebenen Moduswechsel) und (c) frisch ist. Sonst könnte der
// Test-Alarm noch an die volle Liste gehen. null = erlaubt, sonst der Grund.
function demo_alarm_block_reason(array $config): ?string
{
    if (!demo_mode_enabled($config)) {
        return 'Der Alarm über die Box geht nur im Demo-Modus. Im LIVE-Betrieb den REAL ALARM (direkt vom NAS) benutzen.';
    }
    $latest = read_json_file(data_path($config, 'latest_status.json'), []);
    $espVersion = (int)($latest['keys_version'] ?? -1);
    $nasVersion = (int)(load_alarm_keys($config)['version'] ?? 0);
    $age = time() - (int)($latest['seen_at'] ?? 0);
    if ($espVersion !== $nasVersion || (int)($latest['keys_count'] ?? 0) !== 1
        || $age > (int)($config['offline_after_seconds'] ?? 180)) {
        return 'Die Box hat die Demo-Liste noch nicht übernommen oder ist offline – warten, bis das Demo-Banner die Übernahme bestätigt.';
    }
    return null;
}

// Bark-Keys sind kurze alphanumerische Tokens.
function valid_bark_key(string $key): bool
{
    return preg_match('/^[A-Za-z0-9_-]{5,64}$/', $key) === 1;
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
// 'enabled' sagt, wohin Alarme WIRKLICH gehen: wie demo_mode_enabled() nur
// bei gesetztem Flag UND plausiblem Demo-Key. Bei einem unplausiblen Key
// alarmiert effective_alarm_entries() die volle Liste - dann darf das
// Dashboard nicht "Demo" anzeigen (und Browser und Server müssen sich über
// den Modus einig sein, siehe expect_demo in dashboard_api.php).
function demo_mode_info(array $config): array
{
    $demo = load_demo_mode($config);
    $enabled = !empty($demo['enabled']) && valid_bark_key(trim((string)($demo['key'] ?? '')));
    return [
        'enabled' => $enabled,
        'label' => $enabled ? (string)($demo['label'] ?? '') : '',
        'key_id' => $enabled ? (int)($demo['key_id'] ?? 0) : 0,
        'key_masked' => $enabled ? mask_bark_key((string)($demo['key'] ?? '')) : '',
        'changed_at' => (int)($demo['changed_at'] ?? 0),
        'by' => (string)($demo['by'] ?? ''),
    ];
}

// Die Empfänger, an die JETZT alarmiert würde, inklusive Stumm-Flag und
// Stumm-Lautstärke pro Key: Demo-Modus -> nur der Demo-Key (mit seinem
// aktuellen Stumm-Zustand), sonst die volle gepflegte Liste. Wird von
// api/keys.php (ESP32-Sync) und vom NAS-Direktalarm benutzt - beide Alarmwege
// sehen immer dieselbe Liste.
// muted = Arbeitsmodus: Der Alarm geht trotzdem als Critical Alert raus, aber
// mit der eingestellten Stumm-Lautstärke (mute_volume, Standard 0 = lautlos) -
// der Empfänger SIEHT ihn also weiterhin.
//
// Bereinigt wie die Firmware: nur gültige Keys, keine Duplikate, höchstens
// MAX_ALARM_KEYS - sonst würde der ESP32 die Liste verwerfen, während das NAS
// sie benutzt (beide Wege alarmierten verschiedene Empfänger).
// Überfällige Stummschaltungen gelten hier schon als LAUT (mute_is_stale), auch
// wenn expire_stale_mutes() sie noch nicht weggeschrieben hat: Die Alarmierung
// hängt so nicht am Schreibrecht auf den data_dir.
// $keysState/$demo optional übergeben, damit ein Aufrufer (api/keys.php) Liste,
// Version und Demo-Zustand aus EINEM Lesevorgang ausliefern kann.
function effective_alarm_entries(array $config, ?array $keysState = null, ?array $demo = null): array
{
    // label läuft mit, damit das Dashboard im Sende-Ergebnis NAMEN zeigen kann
    // ("Josh FEHLER" statt "abc... FEHLER") - unter Stress zählt, WER fehlt.
    $infoByKey = [];
    foreach ((($keysState ?? load_alarm_keys($config))['keys'] ?? []) as $entry) {
        if (!is_array($entry)) {
            continue;
        }
        $key = trim((string)($entry['key'] ?? ''));
        if (!valid_bark_key($key) || isset($infoByKey[$key])) {
            continue;
        }
        if (count($infoByKey) >= MAX_ALARM_KEYS) {
            error_log('fw_alarm: Empfängerliste hat mehr als ' . MAX_ALARM_KEYS . ' Einträge - Rest ignoriert');
            break;
        }
        // id läuft mit, damit das Dashboard nach einem Teilausfall gezielt NUR
        // an die nicht Erreichten nachsenden kann (only_ids, siehe
        // bark_broadcast). api/keys.php gibt sie nicht aus - die Firmware
        // kennt keine ids.
        $infoByKey[$key] = [
            'id' => (int)($entry['id'] ?? 0),
            'muted' => !empty($entry['muted']) && !mute_is_stale($config, $entry),
            'mute_volume' => mute_volume_of($entry),
            'label' => (string)($entry['label'] ?? ''),
        ];
    }
    $demo = $demo ?? load_demo_mode($config);
    if (!empty($demo['enabled'])) {
        $key = trim((string)($demo['key'] ?? ''));
        if (valid_bark_key($key)) {
            $info = $infoByKey[$key]
                ?? ['id' => (int)($demo['key_id'] ?? 0), 'muted' => false, 'mute_volume' => 0,
                    'label' => (string)($demo['label'] ?? '')];
            return [['id' => (int)($info['id'] ?? 0), 'key' => $key, 'muted' => $info['muted'],
                'mute_volume' => $info['mute_volume'], 'label' => (string)($info['label'] ?? '')]];
        }
        // Unplausibler Demo-Key: lieber die volle Liste als gar keine Alarmierung.
    }
    $entries = [];
    foreach ($infoByKey as $key => $info) {
        $entries[] = ['id' => (int)($info['id'] ?? 0), 'key' => (string)$key, 'muted' => $info['muted'],
            'mute_volume' => $info['mute_volume'], 'label' => (string)($info['label'] ?? '')];
    }
    return $entries;
}

// Empfänger für einen NAS-Direktversand (Alarm/Entwarnung): die effektive
// Liste, sonst die Not-Liste aus config.php. Liefert zusätzlich, ob der
// Fallback griff und ob der Demo-Modus aktiv ist - für die Meldung im Dashboard.
function alarm_recipients(array $config): array
{
    $entries = effective_alarm_entries($config);
    $usedFallback = false;
    if (count($entries) === 0) {
        $fallback = $config['bark_keys_alarm'] ?? [];
        foreach ((is_array($fallback) ? $fallback : []) as $key) {
            $key = trim((string)$key);
            if (valid_bark_key($key) && count($entries) < MAX_ALARM_KEYS) {
                // id 0: Einträge der Not-Liste haben keine Dashboard-id (und
                // lassen sich deshalb nicht gezielt nachsenden).
                $entries[] = ['id' => 0, 'key' => $key, 'muted' => false, 'mute_volume' => 0, 'label' => ''];
            }
        }
        $usedFallback = count($entries) > 0;
    }
    return ['entries' => $entries, 'used_fallback' => $usedFallback, 'demo' => demo_mode_enabled($config)];
}

// ========================= ARBEITSMODUS (STUMM) =============================
// Kollegen können ihren Empfänger per Kurzbefehl-Link (api/mute.php, Geheim-
// Token t= oder alt key=) stumm schalten, z.B. automatisch beim Betreten der
// Arbeit: Der Alarm kommt dann als Critical Alert mit der pro Empfänger
// eingestellten Stumm-Lautstärke an (mute_volume, Standard 0 = lautlos) -
// weiterhin sichtbar und Fokus-durchbrechend; der Empfänger wird NICHT aus
// der Liste genommen. Sicherheitsnetz: Nach mute_max_seconds wird automatisch
// auf laut zurückgeschaltet - falls die "Arbeit verlassen"-Automation eines
// iPhones mal nicht auslöst, verschläft niemand dauerhaft die Alarme.
//
// AUSNAHME "Dauerhaft stumm" (mute_permanent): Für Kollegen, die auf
// unbestimmte Zeit keinen Ton bekommen sollen (Schichtdienst, Krankheit,
// längere Abwesenheit), lässt sich das Sicherheitsnetz pro Empfänger
// abschalten - die Stummschaltung läuft dann NICHT mehr nach
// mute_max_seconds ab, sondern gilt bis jemand sie von Hand beendet.
// Bewusst nur über das Dashboard und nur für die Admin-Rolle setzbar (der
// Kurzbefehl-Link kann das NICHT einschalten, nur beenden) - eine dauerhafte
// Stummschaltung ist eine Entscheidung, die jemand bewusst treffen muss.
// Der Alarm selbst kommt weiterhin als Critical Alert an: sichtbar auf dem
// Sperrbildschirm, nur eben mit der Stumm-Lautstärke.

function mute_max_seconds(array $config): int
{
    return max(0, (int)($config['mute_max_seconds'] ?? 43200));
}

// Ist dieser Eintrag dauerhaft stumm geschaltet (Sicherheitsnetz aus)?
// Fehlt das Feld (Alt-Eintrag), gilt die sichere Richtung: nein, also läuft
// die Stummschaltung wie bisher nach mute_max_seconds ab.
function mute_is_permanent(array $entry): bool
{
    return !empty($entry['mute_permanent']);
}

// Stumm-Lautstärke eines Eintrags: 0 = komplett lautlos (Standard, das alte
// Verhalten), 1-10 = der Critical Alert kommt leise mit diesem Pegel an.
// Wird im Dashboard pro Empfänger eingestellt und gilt für beide Alarmwege.
function mute_volume_of(array $entry): int
{
    return max(0, min(10, (int)($entry['mute_volume'] ?? 0)));
}

// Ist die Stummschaltung dieses Eintrags überfällig (länger als
// mute_max_seconds, nicht dauerhaft)? EINE Definition für alle: die
// Leseseite (effective_alarm_entries -> Alarm/ESP32-Sync) und das
// Aufräumen (expire_stale_mutes).
function mute_is_stale(array $config, array $entry): bool
{
    if (empty($entry['muted']) || mute_is_permanent($entry)) {
        return false;
    }
    $maxAge = mute_max_seconds($config);
    if ($maxAge <= 0) {
        return false;
    }
    $since = (int)($entry['muted_at'] ?? 0);
    return $since <= 0 || time() - $since > $maxAge;
}

// ==================== GEHEIM-LINK-TOKEN (KURZBEFEHL) ========================
// Jeder Empfänger bekommt einen zufälligen Token für seinen persönlichen
// Stummschalt-Link (api/mute.php?t=...). Bewusst ein EIGENER Token statt des
// Bark-Keys: Der Token kann nur stumm/laut schalten (kleineres Recht), taucht
// nirgends sonst auf (nicht in api/keys.php, nicht in Logs) und der alte
// key=-Link bleibt für bestehende Kurzbefehle trotzdem gültig.

// 9 Zufallsbytes -> 12 URL-sichere Zeichen (72 Bit, nicht erratbar).
function new_mute_token(): string
{
    return rtrim(strtr(base64_encode(random_bytes(9)), '+/', '-_'), '=');
}

function valid_mute_token(string $token): bool
{
    return preg_match('/^[A-Za-z0-9_-]{8,32}$/', $token) === 1;
}

// Lazy-Migration: Einträgen aus der Zeit vor der Token-Funktion einen Token
// nachrüsten. BEWUSST ohne Versions-Bump - der ESP32 braucht Tokens nicht,
// er soll deswegen keine Liste neu synchronisieren.
// ACHTUNG: nimmt selbst den 'keys'-Lock - NIE aus einem bereits gehaltenen
// 'keys'-Lock heraus aufrufen (flock würde sich selbst blockieren).
function ensure_mute_tokens(array $config): void
{
    // Ohne Typ-Angabe am Parameter: Ein kaputter Listeneintrag (kein Array,
    // z.B. nach einer Handänderung) darf hier keinen TypeError auslösen.
    $needsToken = static function ($entry): bool {
        return is_array($entry) && !valid_mute_token((string)($entry['mute_token'] ?? ''));
    };
    // Billiger Vorab-Check ohne Lock: Meist haben längst alle einen Token.
    if (count(array_filter(load_alarm_keys($config)['keys'] ?? [], $needsToken)) === 0) {
        return;
    }
    with_lock_soft($config, 'keys', function () use ($config, $needsToken): void {
        $state = read_alarm_keys_state($config);
        if ($state === null) {
            return;   // Liste kaputt: nichts überschreiben
        }
        $changed = false;
        foreach (($state['keys'] ?? []) as $i => $entry) {
            if ($needsToken($entry)) {
                $state['keys'][$i]['mute_token'] = new_mute_token();
                $changed = true;
            }
        }
        if ($changed) {
            backup_then_write($config, 'alarm_keys.json', $state);
        }
    });
}

// Schaltet zu lange stummgeschaltete Empfänger automatisch wieder laut und
// erhöht dabei die Listen-Version (-> der ESP32 holt den Stand beim nächsten
// Poll). Läuft in api/poll.php (damit das Netz den Relaisweg auch OHNE
// Cron-Wächter erreicht), api/mute.php, api/keys.php, keys_list, nach dem
// NAS-Direktalarm und im Cron-Wächter. Die Leseseite rechnet überfällige
// Stummschaltungen ohnehin schon als laut (mute_is_stale) - dies hier ist das
// Wegschreiben plus Benachrichtigung.
// $soft = true: fehlendes Schreibrecht ist kein Abbruch (best effort).
// Die Info-Pushs an die Betroffenen laufen NACH der Antwort
// (defer_after_response) - sie dürfen weder den ESP32 (4-s-Timeout) noch einen
// Alarm aufhalten. Gibt die laut geschalteten Einträge zurück.
// ACHTUNG: nimmt selbst den 'keys'-Lock - NIE aus einem bereits gehaltenen
// 'keys'-Lock heraus aufrufen (flock würde sich selbst blockieren).
function expire_stale_mutes(array $config, bool $soft = false): array
{
    if (mute_max_seconds($config) <= 0) {
        return [];
    }
    // Dauerhaft stumm (bewusst im Dashboard gesetzt) ist nie "überfällig" -
    // das regelt mute_is_stale(). Nur ein Mensch beendet das wieder.
    // Ohne Typ-Angabe am Parameter: Diese Funktion läuft bei JEDEM Poll des
    // ESP32 - ein kaputter Listeneintrag (kein Array) darf sie nicht mit einem
    // TypeError abbrechen (der Poll antwortete sonst dauerhaft mit HTTP 500).
    $isStale = static fn($entry): bool => is_array($entry) && mute_is_stale($config, $entry);
    // Billiger Vorab-Check ohne Lock: Meist ist gar nichts abgelaufen.
    if (count(array_filter(load_alarm_keys($config)['keys'] ?? [], $isStale)) === 0) {
        return [];
    }

    $work = function () use ($config, $isStale): array {
        $state = read_alarm_keys_state($config);
        if ($state === null) {
            return ['expired' => [], 'version' => 0];
        }
        $expired = [];
        foreach (($state['keys'] ?? []) as $i => $entry) {
            if (!is_array($entry) || !$isStale($entry)) {
                continue;
            }
            $state['keys'][$i]['muted'] = false;
            $state['keys'][$i]['muted_at'] = 0;
            $expired[] = $entry;
        }
        if (count($expired) === 0) {
            return ['expired' => [], 'version' => 0];
        }
        $version = bump_keys_version($config, $state);
        if (!backup_then_write($config, 'alarm_keys.json', $state)) {
            return ['expired' => [], 'version' => 0];
        }
        return ['expired' => $expired, 'version' => $version];
    };
    $result = $soft
        ? with_lock_soft($config, 'keys', $work, ['expired' => [], 'version' => 0])
        : with_lock($config, 'keys', $work);

    // Log erst NACH dem Lock; Info-Push erst nach der Antwort.
    foreach ($result['expired'] as $entry) {
        append_command_log($config, [
            'event' => 'key_unmuted',
            'by' => 'automatisch',
            'source' => 'auto',
            'label' => (string)($entry['label'] ?? ''),
            'key' => mask_bark_key((string)($entry['key'] ?? '')),
            'version' => $result['version'],
        ]);
    }
    if (count($result['expired']) > 0) {
        $expiredEntries = $result['expired'];
        defer_after_response(static function () use ($config, $expiredEntries): void {
            foreach ($expiredEntries as $entry) {
                // Leise Info an den Betroffenen selbst (ASCII, siehe bark_send_status).
                bark_send_status($config, (string)($entry['key'] ?? ''), 'Alarmton wieder LAUT',
                    date('[d.m.Y H:i] ') . 'Stummschaltung automatisch beendet (war laenger als '
                    . max(1, (int)round(mute_max_seconds($config) / 3600)) . ' Std aktiv). '
                    . 'Alarme kommen wieder als Critical Alert mit Ton.');
            }
            // Auf Wunsch auch den Admin informieren (Art "mute_expired",
            // Standard: aus): Lief ein Zeitlimit ab, hat meist die
            // "Arbeit verlassen"-Automation des Betroffenen gehakt.
            $labels = array_map(static fn($entry): string => (string)($entry['label'] ?? '?'), $expiredEntries);
            admin_notify_send($config, 'mute_expired', 'Stummschaltung abgelaufen',
                date('[d.m.Y H:i] ') . 'Automatisch wieder laut geschaltet (Zeitlimit erreicht): '
                . implode(', ', $labels) . '.');
        });
    }
    return $result['expired'];
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

// Bark stellt in der Notification keine Umlaute korrekt dar. Feste Texte sind
// deshalb ohnehin ASCII - diese Funktion sichert zusätzlich die STATUS-
// Meldungen ab, die Nutzereingaben enthalten (z.B. Empfänger-Namen in den
// Wächter-Meldungen). Bewusst NICHT für den konfigurierten Alarm-Titel/-Text:
// Die gehen unverändert raus, wie in config.php eingetragen.
// Feste Ersetzungstabelle statt iconv (dessen Ergebnis hängt vom System ab und
// erzeugt Artefakte wie "e für ë); Zeilenumbrüche bleiben erhalten, übrige
// Nicht-ASCII-Zeichen werden zu "?" statt spurlos zu verschwinden.
function bark_ascii(string $text): string
{
    $text = strtr($text, [
        'ä' => 'ae', 'ö' => 'oe', 'ü' => 'ue', 'Ä' => 'Ae', 'Ö' => 'Oe', 'Ü' => 'Ue', 'ß' => 'ss',
        'à' => 'a', 'á' => 'a', 'â' => 'a', 'ã' => 'a', 'å' => 'a', 'æ' => 'ae', 'ç' => 'c',
        'è' => 'e', 'é' => 'e', 'ê' => 'e', 'ë' => 'e', 'ì' => 'i', 'í' => 'i', 'î' => 'i', 'ï' => 'i',
        'ñ' => 'n', 'ò' => 'o', 'ó' => 'o', 'ô' => 'o', 'õ' => 'o', 'ø' => 'o',
        'ù' => 'u', 'ú' => 'u', 'û' => 'u', 'ý' => 'y', 'ÿ' => 'y',
        'À' => 'A', 'Á' => 'A', 'Â' => 'A', 'Ã' => 'A', 'Å' => 'A', 'Æ' => 'Ae', 'Ç' => 'C',
        'È' => 'E', 'É' => 'E', 'Ê' => 'E', 'Ë' => 'E', 'Ì' => 'I', 'Í' => 'I', 'Î' => 'I', 'Ï' => 'I',
        'Ñ' => 'N', 'Ò' => 'O', 'Ó' => 'O', 'Ô' => 'O', 'Õ' => 'O', 'Ø' => 'O',
        'Ù' => 'U', 'Ú' => 'U', 'Û' => 'U', 'Ý' => 'Y',
        'š' => 's', 'Š' => 'S', 'ž' => 'z', 'Ž' => 'Z', 'č' => 'c', 'Č' => 'C', 'ć' => 'c', 'Ć' => 'C',
        'ł' => 'l', 'Ł' => 'L', 'ń' => 'n', 'ś' => 's', 'ź' => 'z', 'ż' => 'z', 'ę' => 'e', 'ą' => 'a',
        'ğ' => 'g', 'ş' => 's', 'ı' => 'i', 'İ' => 'I',
        '–' => '-', '—' => '-', '„' => '"', '“' => '"', '”' => '"', '‘' => "'", '’' => "'", '…' => '...',
        "\r\n" => "\n", "\r" => "\n", "\t" => ' ',
    ]);
    // Jedes verbliebene Nicht-ASCII-ZEICHEN (nicht Byte) wird zu einem "?".
    $clean = preg_replace('/[^\x0A\x20-\x7E]/u', '?', $text);
    if ($clean === null) {
        // Ungültiges UTF-8: byteweise säubern.
        $clean = (string)preg_replace('/[^\x0A\x20-\x7E]+/', '?', $text);
    }
    return $clean;
}

// Sendet Bark-Nachrichten an mehrere Keys PARALLEL (curl_multi), pro Key
// isoliert, mit Wiederholung wie in der Firmware: HTTP 4xx wird nicht
// wiederholt (Key falsch), alles andere bis zu $tries Mal - in Runden, jeweils
// nur für die noch offenen Keys. Ein toter Key blockiert die anderen nicht,
// und der letzte Empfänger wartet nicht mehr auf die Timeouts aller
// vorherigen (früher seriell: bei gestörtem Bark und 10 Keys bis ~5 min).
// $jobs: Liste von ['key' => string, 'fields' => array].
// Ergebnis pro Job (gleiche Reihenfolge): ['ok' => bool, 'http' => int].
function bark_post_many(array $config, array $jobs, int $tries): array
{
    $jobs = array_values($jobs);
    $results = array_fill(0, count($jobs), ['ok' => false, 'http' => 0]);
    if (count($jobs) === 0 || !function_exists('curl_multi_init')) {
        return $results;
    }
    $host = rtrim((string)($config['bark_host'] ?? 'https://api.day.app'), '/');
    $open = array_keys($jobs);
    for ($attempt = 1; $attempt <= max(1, $tries) && count($open) > 0; $attempt++) {
        if ($attempt > 1) {
            usleep(500000);
        }
        $multi = curl_multi_init();
        $handles = [];
        foreach ($open as $i) {
            $ch = curl_init($host . '/' . rawurlencode((string)$jobs[$i]['key']));
            curl_setopt_array($ch, [
                CURLOPT_POST => true,
                CURLOPT_POSTFIELDS => http_build_query($jobs[$i]['fields']),
                CURLOPT_HTTPHEADER => ['Content-Type: application/x-www-form-urlencoded'],
                CURLOPT_RETURNTRANSFER => true,
                CURLOPT_CONNECTTIMEOUT => 5,
                CURLOPT_TIMEOUT => 10,
            ]);
            curl_multi_add_handle($multi, $ch);
            $handles[$i] = $ch;
        }
        do {
            $status = curl_multi_exec($multi, $running);
            if ($running > 0 && curl_multi_select($multi, 1.0) === -1) {
                usleep(10000);   // select() nicht verfügbar: kurz warten statt Dauerschleife
            }
        } while ($running > 0 && $status === CURLM_OK);

        $stillOpen = [];
        foreach ($handles as $i => $ch) {
            $code = (int)curl_getinfo($ch, CURLINFO_RESPONSE_CODE);
            curl_multi_remove_handle($multi, $ch);
            curl_close($ch);
            $results[$i] = ['ok' => $code === 200, 'http' => $code];
            if ($code !== 200 && !($code >= 400 && $code < 500)) {
                $stillOpen[] = $i;   // 4xx: Key falsch, Wiederholen zwecklos
            }
        }
        curl_multi_close($multi);
        $open = $stillOpen;
    }
    return $results;
}

// ==================== MELDUNGEN AN DEN ADMIN (einstellbar) ==================
// Welche Statusmeldungen das NAS an bark_key_status schickt - und WIE -, lässt
// sich im Dashboard einstellen (Panel "Meldungen an den Admin", nur Admin):
// pro Art ein/aus, Dringlichkeit (Bark-Level), Verhalten in der Ruhezeit und
// artspezifische Werte (Schwellen, Wiederholungen); global Ruhezeit, Ton,
// Lautstärke kritischer Meldungen und ein zusätzlicher Empfänger
// (Stellvertreter). Gespeichert in notify_settings.json im data_dir.
//
// Grundsätze:
// - Ohne gespeicherte Auswahl gilt das Verhalten von vorher: die bisherigen
//   Arten sind EIN und leise (level=passive), die Werte kommen aus config.php
//   (watchdog_offline_after_seconds, demo_reminder_after_seconds). Alle NEUEN
//   Arten sind AUS, bis der Admin sie einschaltet.
// - Jeder Wert ist eine Auswahl aus festen Möglichkeiten ('choices'). Gelesen
//   wird über load_notify_settings(): unbekannte oder kaputte Werte fallen auf
//   den Standard zurück - eine beschädigte Datei kann nichts Unsinniges
//   einstellen.
// - Betroffen sind NUR Meldungen, die das NAS selbst verschickt. Heartbeat,
//   Start-Meldung, "Kontakt klemmt?", die Meldung über eine übernommene
//   Empfängerliste und die TEST-Antwort kommen aus der Firmware und laufen
//   daran vorbei. Auch die Info an einen BETROFFENEN Empfänger (automatische
//   Laut-Rückschaltung) ist keine Admin-Meldung und bleibt unberührt.
// - Alarme sind hiervon vollständig unabhängig.
//
// EINE Quelle für Arten und Möglichkeiten: Wächter, Login, Status-Push und
// Oberfläche lesen alle von hier.

// Dringlichkeit = Bark-Level.
const NOTIFY_LEVELS = [
    'passive' => 'leise (nur in der Mitteilungszentrale)',
    'active' => 'normal (mit Ton)',
    'timeSensitive' => 'dringend (durchbricht Fokus)',
    'critical' => 'kritisch (durchbricht auch den Stummschalter)',
];

// In Bark eingebaute Töne; '' = Standardton des iPhones. Wirkt erst ab
// Dringlichkeit "normal" - leise Meldungen haben keinen Ton.
const NOTIFY_SOUNDS = [
    '' => 'Standardton',
    'bell' => 'Glocke (bell)',
    'chime' => 'Klang (chime)',
    'glass' => 'Glas (glass)',
    'minuet' => 'Menuett (minuet)',
    'newsflash' => 'Eilmeldung (newsflash)',
    'telegraph' => 'Telegraf (telegraph)',
    'horn' => 'Horn (horn)',
    'electronic' => 'Elektronisch (electronic)',
];

// Beschriftungen für Wiederholungs-Abstände in Stunden (0 = keine Wiederholung).
function notify_hours_choices(array $hours, string $zero = 'nie (nur einmal)'): array
{
    $out = [];
    foreach ($hours as $h) {
        if ($h === 0) {
            $out[$h] = $zero;
        } elseif ($h === 1) {
            $out[$h] = 'jede Stunde';
        } elseif ($h === 24) {
            $out[$h] = 'einmal pro Tag';
        } elseif ($h === 168) {
            $out[$h] = 'einmal pro Woche';
        } elseif ($h % 24 === 0) {
            $out[$h] = 'alle ' . intdiv($h, 24) . ' Tage';
        } else {
            $out[$h] = 'alle ' . $h . ' Std';
        }
    }
    return $out;
}

// Dauer in Sekunden als kurzer Text ("10 Min", "4 Std", "450 s").
function notify_duration_label(int $seconds): string
{
    if ($seconds > 0 && $seconds % 3600 === 0) {
        return intdiv($seconds, 3600) . ' Std';
    }
    if ($seconds > 0 && $seconds % 60 === 0) {
        return intdiv($seconds, 60) . ' Min';
    }
    return $seconds . ' s';
}

// Fügt einen aus config.php stammenden Standardwert SEKUNDENGENAU in eine
// Auswahl (Schlüssel = Sekunden) ein, falls er dort nicht vorkommt. So gilt
// ein individuell gesetzter config.php-Wert unverändert weiter - er wird
// nicht auf die nächste angebotene Stufe gerundet.
function notify_choices_with_default(array $choices, int $default, string $prefix = ''): array
{
    if (!isset($choices[$default])) {
        $choices[$default] = ($default === 0 ? 'sofort' : $prefix . notify_duration_label($default))
            . ' (aus config.php)';
        ksort($choices);
    }
    return $choices;
}

// Außen-Wächter (Cloud-VM, siehe cloud_watcher/ und api/alive.php) eingerichtet?
// Erkennbar am Hash seines Tokens in config.php.
function outside_watcher_configured(array $config): bool
{
    return trim((string)($config['watcher_token_sha256'] ?? '')) !== '';
}

// Alle Arten von Admin-Meldungen mit Beschriftung, Standard und Möglichkeiten.
// 'important' = sicherheitsrelevant: Das Dashboard fragt vor dem Abschalten
// nach und weist auf den abgeschalteten Zustand hin.
function admin_notify_types(array $config): array
{
    $yesNo = [1 => 'ja', 0 => 'nein'];
    // Beide Standards SEKUNDENGENAU aus config.php (nicht gerundet): Ohne
    // gespeicherte Auswahl verhält sich der Wächter exakt wie vorher.
    $offlineDefault = max(1, max(
        (int)($config['offline_after_seconds'] ?? 180),
        (int)($config['watchdog_offline_after_seconds'] ?? 600)
    ));
    $demoDefault = max(0, (int)($config['demo_reminder_after_seconds'] ?? 14400));
    $weekdays = [1 => 'Montag', 2 => 'Dienstag', 3 => 'Mittwoch', 4 => 'Donnerstag',
        5 => 'Freitag', 6 => 'Samstag', 7 => 'Sonntag'];
    $hours = [];
    for ($h = 0; $h < 24; $h++) {
        $hours[$h] = sprintf('%02d:00 Uhr', $h);
    }

    $types = [
        'box_offline' => [
            'label' => 'Box offline / wieder online',
            'hint' => 'Wenn die Box zu lange keinen Status mehr schickt – dann würde ein Relaisalarm nicht ausgelöst.',
            'important' => true,
            'default_enabled' => true,
            'options' => [
                'offline_after_s' => ['label' => 'Als offline melden nach',
                    'choices' => notify_choices_with_default(
                        [300 => '5 Min', 600 => '10 Min', 900 => '15 Min', 1800 => '30 Min',
                            3600 => '1 Std', 7200 => '2 Std'],
                        $offlineDefault),
                    'default' => $offlineDefault],
                'repeat_hours' => ['label' => 'Erinnern, solange sie offline bleibt',
                    'choices' => notify_hours_choices([0, 1, 3, 6, 12, 24]), 'default' => 0],
                'recovery' => ['label' => 'Auch melden, wenn sie wieder online ist',
                    'choices' => $yesNo, 'default' => 1],
            ],
        ],
        'nas_web' => [
            'label' => 'NAS-Dashboard defekt / wieder in Ordnung',
            'hint' => 'Wenn das Dashboard nicht mehr in seinen Datenordner schreiben kann (Login und Status der Box scheitern dann).',
            'important' => true,
            'default_enabled' => true,
            'options' => [
                'repeat_hours' => ['label' => 'Erinnern, solange die Störung anhält',
                    'choices' => notify_hours_choices([0, 6, 12, 24]), 'default' => 24],
                'recovery' => ['label' => 'Auch melden, wenn es wieder geht',
                    'choices' => $yesNo, 'default' => 1],
            ],
        ],
        // Nur sichtbar, wenn der Außen-Wächter eingerichtet ist (siehe unten).
        'outside_watcher' => [
            'label' => 'Außen-Wächter meldet sich nicht',
            'hint' => 'Wenn sich die Cloud-VM, die das NAS von außen überwacht, nicht mehr meldet – z.B. weil Oracle sie als „idle“ gestoppt hat. Dann würde ein Ausfall des NAS selbst nicht mehr gemeldet.',
            'important' => true,
            'default_enabled' => true,
            'options' => [
                'silent_after_s' => ['label' => 'Melden nach',
                    'choices' => [1800 => '30 Min', 3600 => '1 Std', 7200 => '2 Std', 21600 => '6 Std'],
                    'default' => 3600],
                'repeat_hours' => ['label' => 'Erinnern, solange sie stumm bleibt',
                    'choices' => notify_hours_choices([0, 24, 72, 168]), 'default' => 72],
                'recovery' => ['label' => 'Auch melden, wenn sie wieder da ist',
                    'choices' => $yesNo, 'default' => 1],
            ],
        ],
        'demo_reminder' => [
            'label' => 'Demo-Modus vergessen',
            'hint' => 'Erinnerung, wenn der Demo-Modus länger aktiv bleibt – echte Alarme erreichen dann nur den Test-Empfänger.',
            'important' => false,
            'default_enabled' => true,
            'options' => [
                'after_s' => ['label' => 'Erste Erinnerung',
                    'choices' => notify_choices_with_default(
                        [3600 => 'nach 1 Std', 7200 => 'nach 2 Std', 14400 => 'nach 4 Std',
                            28800 => 'nach 8 Std', 43200 => 'nach 12 Std', 86400 => 'nach 24 Std'],
                        $demoDefault, 'nach '),
                    'default' => $demoDefault],
                'repeat_hours' => ['label' => 'Danach wiederholen',
                    'choices' => notify_hours_choices([6, 12, 24]), 'default' => 24],
            ],
        ],
        'mute_reminder' => [
            'label' => 'Empfänger sind stumm',
            'hint' => 'Erinnerung, solange jemand im Arbeitsmodus stumm ist.',
            'important' => false,
            'default_enabled' => true,
            'options' => [
                'repeat_hours' => ['label' => 'Erinnern',
                    'choices' => notify_hours_choices([12, 24, 48, 168]), 'default' => 24],
            ],
        ],
        'permanent_mute_reminder' => [
            'label' => 'Empfänger sind dauerhaft stumm',
            'hint' => 'Erinnerung, solange jemand dauerhaft stumm geschaltet ist.',
            'important' => false,
            'default_enabled' => true,
            'options' => [
                'repeat_days' => ['label' => 'Erinnern',
                    'choices' => [7 => 'einmal pro Woche', 14 => 'alle 2 Wochen', 30 => 'einmal im Monat'],
                    'default' => 7],
            ],
        ],
        'login_lockout' => [
            'label' => 'Login gesperrt',
            'hint' => 'Wenn eine Adresse nach zu vielen falschen Anmeldeversuchen gesperrt wurde.',
            'important' => false,
            'default_enabled' => true,
            'options' => [],
        ],
        // ---- ab hier: neue, zunächst abgeschaltete Arten --------------------
        'login_success' => [
            'label' => 'Anmeldung am Dashboard',
            'hint' => 'Jedes Mal, wenn sich jemand erfolgreich am Dashboard anmeldet (mit Adresse).',
            'important' => false,
            'default_enabled' => false,
            'options' => [
                'who' => ['label' => 'Melden bei',
                    'choices' => ['all' => 'jeder Anmeldung', 'readonly' => 'nur Lese-Benutzer', 'admin' => 'nur Admin'],
                    'default' => 'all'],
            ],
        ],
        'mute_changed' => [
            'label' => 'Jemand schaltet sich stumm oder laut',
            'hint' => 'Wenn ein Empfänger seinen Kurzbefehl-Link benutzt. Kann bei vielen Kollegen häufig kommen.',
            'important' => false,
            'default_enabled' => false,
            'options' => [],
        ],
        'mute_expired' => [
            'label' => 'Stummschaltung automatisch beendet',
            'hint' => 'Wenn das Sicherheitsnetz jemanden nach Ablauf des Zeitlimits wieder laut schaltet – meist hat dann dessen Automation gehakt.',
            'important' => false,
            'default_enabled' => false,
            'options' => [],
        ],
        'box_restart' => [
            'label' => 'Box neu gestartet',
            'hint' => 'Wenn die Box neu startet. Nach Absturz, Watchdog oder Spannungseinbruch deutet das auf ein Problem hin.',
            'important' => false,
            'default_enabled' => false,
            'options' => [
                'scope' => ['label' => 'Melden bei',
                    'choices' => ['abnormal' => 'nur Absturz, Watchdog, Spannungseinbruch', 'all' => 'jedem Neustart'],
                    'default' => 'abnormal'],
            ],
        ],
        'box_health' => [
            'label' => 'Auffälligkeiten an der Box',
            'hint' => 'Schwaches WLAN, wenig freier Speicher oder ein gescheiterter Heartbeat-Versand – bevor daraus ein Ausfall wird.',
            'important' => false,
            'default_enabled' => false,
            'options' => [
                'repeat_hours' => ['label' => 'Höchstens',
                    'choices' => notify_hours_choices([6, 12, 24, 168]), 'default' => 24],
            ],
        ],
        'weekly_report' => [
            'label' => 'Wochenbericht',
            'hint' => 'Kurze Zusammenfassung der letzten 7 Tage: Alarme, Entwarnungen, Ausfälle, stumme Empfänger.',
            'important' => false,
            'default_enabled' => false,
            'options' => [
                'weekday' => ['label' => 'Wochentag', 'choices' => $weekdays, 'default' => 1],
                'hour' => ['label' => 'Frühestens um', 'choices' => $hours, 'default' => 8],
            ],
        ],
    ];

    // Ohne eingerichteten Außen-Wächter gibt es nichts zu melden - dann soll
    // die Art auch nicht als Schalter im Dashboard auftauchen.
    if (!outside_watcher_configured($config)) {
        unset($types['outside_watcher']);
    }

    // Für JEDE Art gleich: Dringlichkeit und Verhalten in der Ruhezeit.
    $common = [
        'level' => ['label' => 'Dringlichkeit', 'choices' => NOTIFY_LEVELS, 'default' => 'passive'],
        'ignore_quiet' => ['label' => 'In der Ruhezeit',
            'choices' => [0 => 'nur leise zustellen', 1 => 'wie eingestellt (auch nachts)'], 'default' => 0],
    ];
    foreach ($types as $key => $def) {
        $types[$key]['options'] = $common + $def['options'];
    }
    return $types;
}

// Einstellungen, die für alle Arten gelten.
function admin_notify_global_specs(array $config): array
{
    $hours = [];
    for ($h = 0; $h < 24; $h++) {
        $hours[$h] = sprintf('%02d:00', $h);
    }
    $volumes = [];
    for ($v = 0; $v <= 10; $v++) {
        $volumes[$v] = $v === 0 ? '0 (lautlos)' : (string)$v;
    }
    // Stellvertreter: jemand aus der gepflegten Empfängerliste (so muss hier
    // kein weiterer Bark-Key eingetippt und gespeichert werden).
    $extra = [0 => 'niemand'];
    foreach ((load_alarm_keys($config)['keys'] ?? []) as $entry) {
        if (is_array($entry) && (int)($entry['id'] ?? 0) > 0) {
            $extra[(int)$entry['id']] = (string)($entry['label'] ?? '?');
        }
    }
    return [
        'quiet_enabled' => ['label' => 'Ruhezeit', 'choices' => [0 => 'aus', 1 => 'ein'], 'default' => 0],
        'quiet_from' => ['label' => 'Ruhezeit von', 'choices' => $hours, 'default' => 22],
        'quiet_to' => ['label' => 'Ruhezeit bis', 'choices' => $hours, 'default' => 7],
        'sound' => ['label' => 'Ton', 'choices' => NOTIFY_SOUNDS, 'default' => ''],
        'critical_volume' => ['label' => 'Lautstärke kritischer Meldungen', 'choices' => $volumes, 'default' => 5],
        'extra_id' => ['label' => 'Zusätzlich senden an', 'choices' => $extra, 'default' => 0],
    ];
}

// Prüft einen (gespeicherten oder gesendeten) Wert gegen die Möglichkeiten und
// gibt ihn im Typ des Auswahl-Schlüssels zurück - sonst den Standard.
function notify_pick(array $spec, $value)
{
    foreach (array_keys($spec['choices']) as $choice) {
        if (is_scalar($value) && (string)$choice === (string)$value) {
            return $choice;
        }
    }
    return $spec['default'];
}

// Vollständige, geprüfte Einstellungen:
// ['global' => [schlüssel => wert], 'types' => [art => ['enabled' => bool, ...]]]
function load_notify_settings(array $config): array
{
    $stored = read_json_file(data_path($config, 'notify_settings.json'), []);
    $out = ['global' => [], 'types' => []];
    $storedGlobal = is_array($stored['global'] ?? null) ? $stored['global'] : [];
    foreach (admin_notify_global_specs($config) as $key => $spec) {
        $out['global'][$key] = notify_pick($spec, $storedGlobal[$key] ?? $spec['default']);
    }
    $storedTypes = is_array($stored['types'] ?? null) ? $stored['types'] : [];
    foreach (admin_notify_types($config) as $type => $def) {
        $row = is_array($storedTypes[$type] ?? null) ? $storedTypes[$type] : [];
        $settings = ['enabled' => array_key_exists('enabled', $row) ? !empty($row['enabled']) : $def['default_enabled']];
        foreach ($def['options'] as $key => $spec) {
            $settings[$key] = notify_pick($spec, $row[$key] ?? $spec['default']);
        }
        $out['types'][$type] = $settings;
    }
    return $out;
}

function admin_notify_enabled(array $config, string $type): bool
{
    return !empty(load_notify_settings($config)['types'][$type]['enabled']);
}

// Nur die Frage "sind die beiden sicherheitsrelevanten Arten an?" - für den
// Status-Abruf des Dashboards (alle 5 s pro offenem Tab). Liest dafür allein
// notify_settings.json, statt über load_notify_settings() die ganze
// Arten-Tabelle samt Empfängerliste aufzubauen. Beide Arten sind ab Werk an:
// Nur ein ausdrücklich gespeichertes "aus" zählt.
function important_notify_flags(array $config): array
{
    $stored = read_json_file(data_path($config, 'notify_settings.json'), []);
    $on = static function (string $type) use ($stored): bool {
        $row = $stored['types'][$type] ?? null;
        return !(is_array($row) && array_key_exists('enabled', $row) && empty($row['enabled']));
    };
    return ['box_offline' => $on('box_offline'), 'nas_web' => $on('nas_web')];
}

// Liegt $now in der Ruhezeit? Gerechnet in vollen Stunden; "von 22 bis 7"
// läuft über Mitternacht. Gleiche Anfangs- und Endzeit = keine Ruhezeit.
function notify_in_quiet_hours(array $global, ?int $now = null): bool
{
    if (empty($global['quiet_enabled'])) {
        return false;
    }
    $from = (int)$global['quiet_from'];
    $to = (int)$global['quiet_to'];
    if ($from === $to) {
        return false;
    }
    $hour = (int)date('G', $now ?? time());
    return $from < $to ? ($hour >= $from && $hour < $to) : ($hour >= $from || $hour < $to);
}

// Sendet EINE Admin-Meldung der Art $type nach den Einstellungen des Admins:
// Dringlichkeit, Ruhezeit (dann nur leise, außer die Art ist davon
// ausgenommen), Ton, Lautstärke kritischer Meldungen und Stellvertreter.
// Rückgabe: 'sent' (Bark hat für den Status-Empfänger angenommen), 'failed'
// oder 'off' (Art abgeschaltet bzw. kein Status-Key - es wurde NICHTS
// gesendet). $force = Probe-Meldung aus dem Dashboard: sendet auch eine
// abgeschaltete Art. Texte gehen per bark_ascii() als ASCII raus.
// Der Stellvertreter ist "best effort": Sein Ergebnis ändert die Rückgabe nicht.
function admin_notify_send(array $config, string $type, string $title, string $body,
    ?array $settings = null, bool $force = false): string
{
    $key = trim((string)($config['bark_key_status'] ?? ''));
    $settings = $settings ?? load_notify_settings($config);
    $row = $settings['types'][$type] ?? null;
    // 'off' NUR, wenn bewusst nichts gesendet werden soll: Art abgeschaltet
    // oder gar kein Status-Key eingetragen.
    if ($key === '' || $row === null || (!$force && empty($row['enabled']))) {
        return 'off';
    }
    // Ein eingetragener, aber unbrauchbarer Key (z.B. die ganze Bark-URL
    // hineinkopiert) ist ein FEHLER, kein "abgeschaltet": Der Wächter soll
    // das als gescheiterten Versand führen und im Dashboard warnen, statt den
    // Fall still als erledigt abzuhaken.
    if (!valid_bark_key($key) || !function_exists('curl_init')) {
        return 'failed';
    }
    $global = $settings['global'];
    $level = (string)$row['level'];
    if (empty($row['ignore_quiet']) && notify_in_quiet_hours($global)) {
        $level = 'passive';
    }
    $fields = ['title' => bark_ascii($title), 'body' => bark_ascii($body), 'level' => $level];
    if ($level !== 'passive' && (string)$global['sound'] !== '') {
        $fields['sound'] = (string)$global['sound'];
    }
    if ($level === 'critical') {
        $fields['volume'] = (int)$global['critical_volume'];
    }
    $jobs = [['key' => $key, 'fields' => $fields]];
    $extraId = (int)$global['extra_id'];
    if ($extraId > 0) {
        $state = load_alarm_keys($config);
        $idx = find_key_index($state, $extraId);
        $extraKey = $idx >= 0 ? trim((string)($state['keys'][$idx]['key'] ?? '')) : '';
        if (valid_bark_key($extraKey) && $extraKey !== $key) {
            $jobs[] = ['key' => $extraKey, 'fields' => $fields];
        }
    }
    return bark_post_many($config, $jobs, 2)[0]['ok'] ? 'sent' : 'failed';
}

// Text des Wochenberichts (ASCII, geht an Bark): Zählt die Ereignisse der
// letzten 7 Tage aus dem Verlauf und hängt den aktuellen Stand an.
function weekly_report_text(array $config, bool $boxOnline): string
{
    $since = time() - 7 * 86400;
    $relay = 0;
    $manual = 0;
    $cleared = 0;
    $outages = 0;
    foreach (read_command_log_tail($config, 3000) as $entry) {
        if ((int)($entry['log_at'] ?? 0) < $since) {
            break;   // neueste zuerst - alles Weitere ist älter
        }
        $isDemo = str_starts_with((string)($entry['message'] ?? ''), 'DEMO-MODUS');
        switch ((string)($entry['event'] ?? '')) {
            case 'relay_alarm':
                if ((string)($entry['source'] ?? 'relay') !== 'remote') {
                    $relay++;
                }
                break;
            case 'nas_alarm':
                if (empty($entry['resend']) && !$isDemo) {
                    $manual++;
                }
                break;
            case 'false_alarm':
                if (empty($entry['resend']) && !$isDemo) {
                    $cleared++;
                }
                break;
            case 'box_offline':
                $outages++;
                break;
        }
    }
    $total = 0;
    $muted = 0;
    foreach ((load_alarm_keys($config)['keys'] ?? []) as $entry) {
        if (!is_array($entry)) {
            continue;
        }
        $total++;
        if (!empty($entry['muted'])) {
            $muted++;
        }
    }
    return "Letzte 7 Tage - Relaisalarme: {$relay}, manuelle Alarme: {$manual}, "
        . "Entwarnungen: {$cleared}, Ausfaelle der Box: {$outages}. "
        . 'Jetzt: Box ' . ($boxOnline ? 'online' : 'OFFLINE') . ", {$total} Empfaenger"
        . ($muted > 0 ? " (davon {$muted} stumm)" : '')
        . (demo_mode_enabled($config) ? ', DEMO-MODUS AKTIV' : '') . '.';
}

// Leise Statusmeldung (level=passive) an EINEN Bark-Key - das Gegenstück zu
// sendStatus() in der Firmware. Seit die Admin-Meldungen einstellbar sind
// (admin_notify_send), nur noch für die Info an einen BETROFFENEN Empfänger
// genutzt: die automatische Laut-Rückschaltung. Texte werden
// per bark_ascii() abgesichert. Knappe Timeouts, max. 2 Versuche, damit z.B.
// der Login-Pfad nie lange an Bark hängt.
function bark_send_status(array $config, string $key, string $title, string $body): bool
{
    if (!function_exists('curl_init') || !valid_bark_key($key)) {
        return false;
    }
    $fields = ['title' => bark_ascii($title), 'body' => bark_ascii($body), 'level' => 'passive'];
    return bark_post_many($config, [['key' => $key, 'fields' => $fields]], 2)[0]['ok'];
}

// Test-Push an EINEN Empfänger (Dashboard, "Test-Push senden"): eine NORMALE
// Mitteilung (level=active, kein Critical Alert, kein call=1) mit dem
// Alarmton. Prüft beim Einrichten eines neuen Kollegen zwei Dinge auf einmal:
// ob der Key stimmt und ob der importierte Alarmton auf dem iPhone vorhanden
// ist (sonst spielt Bark den Standardton) - ohne Demo-Modus und ohne echten
// Alarm. Bewusst NICHT kritisch: ein Test darf Stummschalter und Fokus nicht
// durchbrechen. Bark-Texte ASCII (ae/ue/oe).
function bark_send_test(array $config, string $key): array
{
    if (!function_exists('curl_init') || !valid_bark_key($key)) {
        return ['ok' => false, 'http' => 0];
    }
    $fields = [
        'title' => 'TEST - kein Alarm',
        'body'  => date('[d.m.Y H:i] ') . 'Test-Nachricht vom Alarm-Waechter. '
            . 'Hoerst du dabei den Alarmton, ist dein iPhone richtig eingerichtet.',
        'sound' => (string)($config['bark_alarm_sound'] ?? 'alarm_fw'),
        'level' => 'active',
    ];
    return bark_post_many($config, [['key' => $key, 'fields' => $fields]], 2)[0];
}

// Meldung, wenn eine Nachsendung (only_ids) niemanden mehr trifft.
const BARK_RESEND_EMPTY_MESSAGE = 'Keiner der ausgewählten Empfänger steht (noch) in der aktuellen '
    . 'Empfängerliste – es wurde NICHTS gesendet.';

// Bark-Felder des REAL ALARM für EINEN Empfänger - wie in der Firmware.
// muted = Arbeitsmodus: gleicher Critical Alert, aber mit der eingestellten
// Stumm-Lautstärke (muteVolume, 0 = lautlos; durchbricht trotzdem
// Stummschalter/Fokus) und ohne call=1 (Ton-Wiederholung).
function bark_alarm_fields(array $config, string $body, bool $muted, int $muteVolume): array
{
    $fields = [
        'title'  => (string)($config['bark_alarm_title'] ?? 'FEUERWEHR-ALARM'),
        'body'   => $body,
        'sound'  => (string)($config['bark_alarm_sound'] ?? 'alarm_fw'),
        'level'  => 'critical',
        'volume' => $muted
            ? max(0, min(10, $muteVolume))
            : max(0, min(10, (int)($config['bark_alarm_volume'] ?? 10))),
    ];
    // Fehlt der Eintrag in config.php, gilt die laute Richtung (call=1) - wie
    // bei allen anderen Alarm-Defaults.
    if (!$muted && ($config['bark_alarm_call'] ?? true)) {
        $fields['call'] = 1;
    }
    return $fields;
}

// ENTWARNUNG (Fehlalarm) an EINEN Empfänger: ebenfalls ein CRITICAL ALERT, denn
// wer gerade zum Einsatz aufbricht, muss die Entwarnung genauso sicher
// mitbekommen wie den Alarm - auch durch Stummschalter und Fokus hindurch.
// Unterschied zum Alarm: ein kurzer, normaler Ton (bark_false_alarm_sound,
// z.B. 'glass') statt des Alarmtons und KEIN call=1 - es soll einmal deutlich
// klingeln, nicht dauerhaft schrillen.
// muted = Arbeitsmodus: gleiche Regel wie beim Alarm - wer den Alarm leise
// bekommen hat, bekommt auch die Entwarnung leise.
function bark_false_alarm_fields(array $config, string $title, string $body, bool $muted, int $muteVolume): array
{
    // Lautstärke standardmäßig wie beim Alarm - die Entwarnung soll ja jeden
    // erreichen; bark_false_alarm_volume kann das bei Bedarf absenken.
    $volume = (int)($config['bark_false_alarm_volume'] ?? ($config['bark_alarm_volume'] ?? 10));
    return [
        'title'  => $title,
        'body'   => $body,
        'sound'  => (string)($config['bark_false_alarm_sound'] ?? 'glass'),
        'level'  => 'critical',
        'volume' => $muted ? max(0, min(10, $muteVolume)) : max(0, min(10, $volume)),
    ];
}

// Gemeinsamer Ablauf für REAL ALARM und ENTWARNUNG: Empfänger bestimmen
// (effektive Liste, sonst Not-Liste aus config.php), alle PARALLEL senden,
// Ergebnis pro Empfänger. $fieldsFor($entry) liefert die Bark-Felder.
// Läuft auch weiter, wenn der Browser die Verbindung trennt (Timeout im
// Proxy/Nginx) - ein halb verschickter Alarm wäre das Schlimmste.
// $onlyIds (Nachsendung nach einem Teilausfall): nur an diese Empfänger-ids.
// Die Auswahl kann die effektive Liste ausschließlich VERKLEINERN - im
// Demo-Modus bleibt es beim Test-Empfänger, gelöschte ids fallen heraus, und
// eine leere Schnittmenge sendet NICHTS (count 0), nie ersatzweise an alle.
function bark_broadcast(array $config, callable $fieldsFor, ?array $onlyIds = null): array
{
    ignore_user_abort(true);
    @set_time_limit(120);

    $recipients = alarm_recipients($config);
    $entries = $recipients['entries'];
    if ($onlyIds !== null) {
        $entries = array_values(array_filter($entries, static fn(array $entry): bool =>
            (int)($entry['id'] ?? 0) > 0 && in_array((int)$entry['id'], $onlyIds, true)));
    }
    $jobs = [];
    foreach ($entries as $entry) {
        $jobs[] = ['key' => (string)$entry['key'], 'fields' => $fieldsFor($entry)];
    }
    $sent = bark_post_many($config, $jobs, max(1, (int)($config['bark_max_tries'] ?? 3)));

    $results = [];
    $okCount = 0;
    $mutedCount = 0;
    foreach ($entries as $i => $entry) {
        $muted = !empty($entry['muted']);
        if ($muted) {
            $mutedCount++;
        }
        if ($sent[$i]['ok']) {
            $okCount++;
        }
        $results[] = [
            'id' => (int)($entry['id'] ?? 0),
            'key' => mask_bark_key((string)$entry['key']),
            'ok' => $sent[$i]['ok'],
            'http' => $sent[$i]['http'],
            'muted' => $muted,
            'label' => (string)($entry['label'] ?? ''),
        ];
    }
    // Überfällige Stummschaltungen wurden beim Senden schon als laut
    // behandelt (mute_is_stale) - jetzt, NACH dem Versand, auch wegschreiben.
    // Nicht fatal: fehlendes Schreibrecht darf das Ergebnis nicht verschlucken.
    expire_stale_mutes($config, true);

    return [
        'count' => count($entries),
        'ok_count' => $okCount,
        'muted_count' => $mutedCount,
        'used_fallback' => $recipients['used_fallback'],
        'demo' => $recipients['demo'],
        'results' => $results,
    ];
}

// ENTWARNUNG (Fehlalarm): CRITICAL ALERT mit kurzem, normalem Ton an alle
// aktuellen Empfänger - für den Fall, dass ein Alarm versehentlich ausgelöst
// wurde. Muss genauso durchdringen wie der Alarm selbst, sonst fährt jemand
// umsonst los. Nutzt dieselbe Empfängerliste wie der Alarm (im Demo-Modus also
// nur den Test-Empfänger, so lässt sich auch die Entwarnung gefahrlos üben).
function bark_send_false_alarm_all(array $config, ?array $onlyIds = null): array
{
    if (!function_exists('curl_init')) {
        return ['ok' => false, 'message' => 'PHP-cURL fehlt. In Web Station im PHP-Profil die Erweiterung "curl" aktivieren.', 'results' => []];
    }
    // Bark-Texte bewusst ASCII (ae/ue/oe).
    $title = 'ENTWARNUNG - Fehlalarm';
    $body = date('[d.m.Y H:i] ') . 'Der letzte Alarm war ein FEHLALARM - kein Einsatz.';
    $sent = bark_broadcast($config, static fn(array $entry): array => bark_false_alarm_fields(
        $config, $title, $body, !empty($entry['muted']), (int)($entry['mute_volume'] ?? 0)), $onlyIds);

    if ($sent['count'] === 0) {
        return ['ok' => false, 'message' => $onlyIds !== null
            ? BARK_RESEND_EMPTY_MESSAGE
            : 'Keine Empfänger: Liste im Dashboard pflegen (Panel "Alarm-Empfänger").', 'results' => []];
    }
    return [
        'ok' => $sent['ok_count'] === $sent['count'],
        // Diese Zusammenfassung geht nur an Dashboard und Verlauf (nie an
        // Bark) - darf deshalb, anders als $title/$body, echte Umlaute haben.
        'message' => ($sent['demo'] ? 'DEMO-MODUS: Entwarnung nur an den Test-Empfänger. ' : '')
            . ($onlyIds !== null ? 'Nachsendung: ' : '')
            . sprintf('Entwarnung an %d/%d Empfänger zugestellt.', $sent['ok_count'], $sent['count'])
            . ($sent['muted_count'] > 0 ? sprintf(' %d davon stumm (Arbeitsmodus, leise/lautlos).', $sent['muted_count']) : '')
            . ($sent['used_fallback'] ? ' ACHTUNG: Fallback-Liste aus config.php verwendet, die Dashboard-Liste ist leer!' : ''),
        'results' => $sent['results'],
    ];
}

// Alarmiert alle Empfänger, pro Key isoliert und parallel - ein toter Key
// blockiert die anderen nicht. Gibt pro Key ein Ergebnis zurück.
// Führend ist die im Dashboard gepflegte Liste (im Demo-Modus nur der
// Demo-Key); die statische Liste aus config.php dient nur noch als Fallback,
// solange keine gepflegt ist.
function bark_send_alarm_all(array $config, ?array $onlyIds = null): array
{
    if (!function_exists('curl_init')) {
        return ['ok' => false, 'message' => 'PHP-cURL fehlt. In Web Station im PHP-Profil die Erweiterung "curl" aktivieren.', 'results' => []];
    }
    // Zeitstempel wie in der Firmware; Bark-Texte bewusst ASCII (ae/ue/oe).
    // Bewusst KEIN Demo-Zusatz im Text: Ein Demo-Alarm soll exakt so aussehen
    // und klingen wie ein echter - er geht nur an weniger Empfänger.
    $body = date('[d.m.Y H:i] ')
        . (string)($config['bark_alarm_body'] ?? 'Manuelle Alarmierung ueber das NAS-Dashboard!');
    $sent = bark_broadcast($config, static fn(array $entry): array => bark_alarm_fields(
        $config, $body, !empty($entry['muted']), (int)($entry['mute_volume'] ?? 0)), $onlyIds);

    if ($sent['count'] === 0) {
        return ['ok' => false, 'message' => $onlyIds !== null
            ? BARK_RESEND_EMPTY_MESSAGE
            : 'Keine Alarm-Empfänger: Liste im Dashboard pflegen (Panel "Alarm-Empfänger").', 'results' => []];
    }
    return [
        'ok' => $sent['ok_count'] === $sent['count'],
        // Deutlich machen, wenn die statische Fallback-Liste zum Einsatz kam -
        // sonst glaubt man, die (leere) Dashboard-Liste sei maßgeblich gewesen.
        // Geht nur an Dashboard/Verlauf (nie an Bark) - echte Umlaute erlaubt.
        'message' => ($sent['demo'] ? 'DEMO-MODUS: nur Test-Empfänger alarmiert. ' : '')
            . ($onlyIds !== null ? 'Nachsendung: ' : '')
            . sprintf('%d/%d Empfänger erreicht.', $sent['ok_count'], $sent['count'])
            . ($sent['muted_count'] > 0 ? sprintf(' %d davon stumm (Arbeitsmodus, leise/lautlos).', $sent['muted_count']) : '')
            . ($sent['used_fallback'] ? ' ACHTUNG: Fallback-Liste aus config.php verwendet, die Dashboard-Liste ist leer!' : ''),
        'results' => $sent['results'],
    ];
}
