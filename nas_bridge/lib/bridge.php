<?php
// NAS-Brücke: reicht genau fünf Aufrufe serverseitig an den neuen Server
// weiter (api/status.php, poll.php, ack.php, keys.php für die Box und
// api/mute.php für alte Kurzbefehl-Links). Keine Daten, keine Geheimnisse,
// kein offener Proxy - das Ziel steht fest in bridge_config.php.
// Hintergrund und Betrieb: nas_bridge/README.md.
declare(strict_types=1);

// Diese Datei direkt aufgerufen: nichts zu holen.
if (realpath((string)($_SERVER['SCRIPT_FILENAME'] ?? '')) === __FILE__) {
    http_response_code(404);
    exit;
}

// Nur diese Dateien werden weitergereicht - alles andere nie.
const BRIDGE_FILES = ['status.php', 'poll.php', 'ack.php', 'keys.php', 'mute.php'];

// Größter Rumpf einer Anfrage (die Box schickt wenige hundert Bytes).
const BRIDGE_MAX_BODY = 16384;

// Zeitlimits: zusammen unter den 4 s, die die Box auf eine Antwort wartet.
const BRIDGE_CONNECT_TIMEOUT_MS = 3000;
const BRIDGE_TOTAL_TIMEOUT_MS = 3500;

// Kopfzeilen der Anfrage, die mitgehen ($_SERVER-Name => Name). Die
// Sec-/Purpose-/X-Moz-Zeilen braucht mute.php, um Vorab-Abrufe zu erkennen.
// Bewusst NICHT dabei: Cookie, Authorization, X-Forwarded-*.
const BRIDGE_REQUEST_HEADERS = [
    'CONTENT_TYPE' => 'Content-Type',
    'HTTP_ACCEPT' => 'Accept',
    'HTTP_ACCEPT_LANGUAGE' => 'Accept-Language',
    'HTTP_USER_AGENT' => 'User-Agent',
    'HTTP_SEC_FETCH_DEST' => 'Sec-Fetch-Dest',
    'HTTP_SEC_FETCH_MODE' => 'Sec-Fetch-Mode',
    'HTTP_SEC_FETCH_SITE' => 'Sec-Fetch-Site',
    'HTTP_SEC_PURPOSE' => 'Sec-Purpose',
    'HTTP_PURPOSE' => 'Purpose',
    'HTTP_X_PURPOSE' => 'X-Purpose',
    'HTTP_X_MOZ' => 'X-Moz',
];

// Kopfzeilen der Antwort, die zurückgehen (kleingeschrieben => Schreibweise).
// Bewusst NICHT dabei: Set-Cookie, Strict-Transport-Security.
const BRIDGE_RESPONSE_HEADERS = [
    'content-type' => 'Content-Type',
    'location' => 'Location',
    'cache-control' => 'Cache-Control',
    'content-security-policy' => 'Content-Security-Policy',
    'x-content-type-options' => 'X-Content-Type-Options',
    'referrer-policy' => 'Referrer-Policy',
    'x-frame-options' => 'X-Frame-Options',
];

// Ordner der Brücke (dort liegen bridge_config.php und ggf. bridge_hold.php).
function bridge_root(): string
{
    return dirname(__DIR__);
}

// Eigene Antwort der Brücke: kurzer Text, nie zwischenspeichern.
function bridge_text(int $status, string $text): never
{
    http_response_code($status);
    header('Content-Type: text/plain; charset=utf-8');
    header('Cache-Control: no-store');
    header('X-Content-Type-Options: nosniff');
    echo $text;
    exit;
}

// Konfiguration lesen; null, wenn die Datei fehlt oder kein brauchbares Ziel
// nennt.
function bridge_read_config(): ?array
{
    $path = bridge_root() . '/bridge_config.php';
    if (!is_file($path)) {
        return null;
    }
    $config = require $path;
    $target = is_array($config) ? rtrim(trim((string)($config['target'] ?? '')), '/') : '';
    if (preg_match('~^https?://[^\s/?#]+~i', $target) !== 1) {
        error_log('fw-bridge: bridge_config.php nennt kein brauchbares target');
        return null;
    }
    $public = trim((string)($config['public_url'] ?? ''));
    return [
        'target' => $target,
        'public_url' => $public !== '' ? $public : $target . '/',
        'held_log' => trim((string)($config['held_log'] ?? '')),
    ];
}

function bridge_config(): array
{
    $config = bridge_read_config();
    if ($config === null) {
        bridge_text(500, "bridge_config fehlt\n");
    }
    return $config;
}

// Wartung ("Halt"): Liegt bridge_hold.php neben der Brücke, wird nichts
// weitergereicht. Bewusst eine Datei und kein Wert in bridge_config.php:
// Die Konfiguration kann PHP zwischenspeichern (OPcache), das Vorhandensein
// einer Datei wird bei jedem Aufruf frisch geprüft - der Halt greift und
// endet sofort.
function bridge_on_hold(): bool
{
    $flag = bridge_root() . '/bridge_hold.php';
    clearstatcache(true, $flag);
    return is_file($flag);
}

// Würde mute.php diesen Aufruf als Vorschau behandeln (und nichts schalten)?
// Gleiche Regeln wie in nas_dashboard/api/mute.php ($isPreview) - bei einer
// Änderung dort hier nachziehen.
function bridge_mute_is_preview(): bool
{
    $method = (string)($_SERVER['REQUEST_METHOD'] ?? 'GET');
    $agent = (string)($_SERVER['HTTP_USER_AGENT'] ?? '');
    $purpose = strtolower(implode(' ', [
        (string)($_SERVER['HTTP_SEC_PURPOSE'] ?? ''),
        (string)($_SERVER['HTTP_PURPOSE'] ?? ''),
        (string)($_SERVER['HTTP_X_PURPOSE'] ?? ''),
        (string)($_SERVER['HTTP_X_MOZ'] ?? ''),
    ]));
    return !in_array($method, ['GET', 'POST'], true)
        || preg_match('/prefetch|prerender|preview/', $purpose) === 1
        || preg_match('/facebookexternalhit|facebot|twitterbot|whatsapp|telegrambot|slackbot|discordbot|'
            . 'linkedinbot|skypeuripreview|signal|bingpreview|embedly|ms-office|safelinks|'
            . 'preview|crawler|spider|bot\/|\bbot\b/i', $agent) === 1;
}

// Im Halt abgewiesenen Mute-Aufruf merken, damit er nach der Wartung
// nachgeholt werden kann (sonst bliebe jemand, dessen Automatik "Feierabend
// → laut" in die Wartung fiel, unbemerkt stumm). Eine Zeile, durch Tabs
// getrennt: Zeit, t|key, die ersten 12 Hex-Zeichen von sha256(Token bzw.
// Key), gewünschter Zustand (on|off|?). NIE das Token oder der Key selbst.
// Vorschau-Aufrufe und Aufrufe ohne Token/Key hätten nichts geschaltet und
// werden nicht gemerkt.
function bridge_note_held_mute(?array $config): void
{
    if (bridge_mute_is_preview()) {
        return;
    }
    $token = trim((string)($_POST['t'] ?? $_GET['t'] ?? ''));
    $key = trim((string)($_POST['key'] ?? $_GET['key'] ?? ''));
    if ($token === '' && $key === '') {
        return;
    }
    $state = strtolower(trim((string)($_POST['state'] ?? $_GET['state'] ?? '')));
    $line = time() . "\t" . ($token !== '' ? 't' : 'key') . "\t"
        . substr(hash('sha256', $token !== '' ? $token : $key), 0, 12) . "\t"
        . (in_array($state, ['on', 'off'], true) ? $state : '?') . "\n";
    $path = (string)($config['held_log'] ?? '');
    if ($path === '' || @file_put_contents($path, $line, FILE_APPEND | LOCK_EX) === false) {
        error_log('fw-bridge: Mute-Aufruf im Halt konnte nicht gemerkt werden (held_log in bridge_config.php?)');
    }
}

// Reicht den aktuellen Aufruf an <target>/api/<file> weiter und gibt dessen
// Antwort zurück. Eigene Antworten der Brücke: 404 (unbekannte Datei), 405,
// 413, 414, 500 (Konfiguration fehlt), 502/504 (Server nicht erreichbar),
// 503 (Halt) - NIE 403: Das wertet die Box als "Token abgelehnt" und warnt
// den Owner per Bark. Ein 403 gibt es nur, wenn die App selbst so antwortet.
function bridge_forward(string $file): never
{
    if (!in_array($file, BRIDGE_FILES, true)) {
        bridge_text(404, "Not Found\n");
    }

    if (bridge_on_hold()) {
        header('Retry-After: 120');
        if ($file === 'mute.php') {
            bridge_note_held_mute(bridge_read_config());
            // Format wie die Fehlerantworten von mute.php (Kurzbefehle
            // werten "ok=" aus); Bark-Regel: ASCII.
            bridge_text(503, "ok=0\nerror=Wartung - bitte in ein paar Minuten erneut aufrufen\n");
        }
        // Für die Box bewusst OHNE ok=/type=-Zeilen.
        bridge_text(503, "bridge: Wartung\n");
    }

    $method = strtoupper((string)($_SERVER['REQUEST_METHOD'] ?? 'GET'));
    if (!in_array($method, ['GET', 'POST', 'HEAD'], true)) {
        header('Allow: GET, POST, HEAD');
        bridge_text(405, "bridge: Methode nicht erlaubt\n");
    }

    $config = bridge_config();
    if (!function_exists('curl_init')) {
        error_log('fw-bridge: PHP-cURL fehlt');
        bridge_text(502, "bridge: Server nicht erreichbar\n");
    }

    $query = (string)($_SERVER['QUERY_STRING'] ?? '');
    if (strlen($query) > 4096 || preg_match('/[\x00-\x20\x7f]/', $query) === 1) {
        bridge_text(414, "bridge: Adresse unbrauchbar\n");
    }

    // Rumpf: höchstens BRIDGE_MAX_BODY Bytes. multipart/form-data gibt PHP
    // nicht roh heraus (php://input ist dann leer) - aus den Formularfeldern
    // als application/x-www-form-urlencoded neu aufbauen (Dateien entfallen).
    $contentType = trim((string)($_SERVER['CONTENT_TYPE'] ?? ''));
    if ((int)($_SERVER['CONTENT_LENGTH'] ?? 0) > BRIDGE_MAX_BODY) {
        bridge_text(413, "bridge: Anfrage zu gross\n");
    }
    $body = '';
    if ($method === 'POST') {
        if (stripos($contentType, 'multipart/form-data') === 0) {
            $body = http_build_query($_POST);
            $contentType = 'application/x-www-form-urlencoded';
        } else {
            $body = (string)file_get_contents('php://input', false, null, 0, BRIDGE_MAX_BODY + 1);
        }
        if (strlen($body) > BRIDGE_MAX_BODY) {
            bridge_text(413, "bridge: Anfrage zu gross\n");
        }
    }

    // Kopfzeilen: nur die erlaubten. Fehlt eine, darf cURL nichts Eigenes
    // einsetzen ("Name:" ohne Wert unterdrückt z.B. sein "Accept: */*") -
    // mute.php unterscheidet Browser und Kurzbefehl an genau diesen Zeilen.
    $headers = ['X-FW-Bridge: 1', 'Expect:'];
    foreach (BRIDGE_REQUEST_HEADERS as $serverName => $name) {
        $value = $name === 'Content-Type' ? $contentType : trim((string)($_SERVER[$serverName] ?? ''));
        if ($value !== '' && strlen($value) <= 2048 && preg_match('/[\x00-\x1f\x7f]/', $value) !== 1) {
            $headers[] = $name . ': ' . $value;
        } elseif (in_array($name, ['Content-Type', 'Accept', 'User-Agent'], true)) {
            $headers[] = $name . ':';
        }
    }

    $responseHeaders = [];
    $ch = curl_init($config['target'] . '/api/' . $file . ($query !== '' ? '?' . $query : ''));
    curl_setopt_array($ch, [
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_FOLLOWLOCATION => false,
        CURLOPT_CONNECTTIMEOUT_MS => BRIDGE_CONNECT_TIMEOUT_MS,
        CURLOPT_TIMEOUT_MS => BRIDGE_TOTAL_TIMEOUT_MS,
        CURLOPT_NOSIGNAL => true,
        CURLOPT_SSL_VERIFYPEER => true,
        CURLOPT_SSL_VERIFYHOST => 2,
        CURLOPT_PROTOCOLS => CURLPROTO_HTTP | CURLPROTO_HTTPS,
        CURLOPT_HTTPHEADER => $headers,
        CURLOPT_HEADERFUNCTION => static function ($ch, string $line) use (&$responseHeaders): int {
            if (stripos($line, 'HTTP/') === 0) {
                // Neue Antwort (z.B. nach "100 Continue"): von vorn sammeln.
                $responseHeaders = [];
            } elseif (($pos = strpos($line, ':')) !== false) {
                $responseHeaders[strtolower(trim(substr($line, 0, $pos)))][] = trim(substr($line, $pos + 1));
            }
            return strlen($line);
        },
    ]);
    if ($method === 'HEAD') {
        curl_setopt($ch, CURLOPT_NOBODY, true);
    } elseif ($method === 'POST') {
        curl_setopt($ch, CURLOPT_POST, true);
        curl_setopt($ch, CURLOPT_POSTFIELDS, $body);
    }
    $responseBody = curl_exec($ch);
    $errno = curl_errno($ch);
    $error = curl_error($ch);
    $status = (int)curl_getinfo($ch, CURLINFO_RESPONSE_CODE);
    curl_close($ch);

    if ($responseBody === false || $errno !== 0 || $status < 100) {
        error_log('fw-bridge: ' . $file . ' nicht weitergereicht (cURL ' . $errno . ': ' . $error . ')');
        bridge_text($errno === CURLE_OPERATION_TIMEDOUT ? 504 : 502, "bridge: Server nicht erreichbar\n");
    }
    // Hat Cloudflare die Anfrage abgefangen (Sperr- oder Prüfseite), ist das
    // keine Antwort der App: als "nicht erreichbar" melden, nicht als 403.
    if (isset($responseHeaders['cf-mitigated'])) {
        error_log('fw-bridge: ' . $file . ' von Cloudflare abgefangen (cf-mitigated: '
            . implode(',', $responseHeaders['cf-mitigated']) . ', HTTP ' . $status . ')');
        bridge_text(502, "bridge: Server nicht erreichbar\n");
    }

    http_response_code($status);
    foreach (BRIDGE_RESPONSE_HEADERS as $lower => $name) {
        foreach ($responseHeaders[$lower] ?? [] as $i => $value) {
            header($name . ': ' . $value, $i === 0);
        }
    }
    if (!isset($responseHeaders['cache-control'])) {
        header('Cache-Control: no-store');
    }
    if ($method !== 'HEAD') {
        echo $responseBody;
    }
    exit;
}

// index.php / classic.php: Das Dashboard ist umgezogen. Bewusst 302, nicht
// 301 - eine dauerhafte Weiterleitung merkt sich der Browser, ein Rückweg
// auf das NAS käme dann bei niemandem mehr an.
function bridge_moved_page(): never
{
    $url = bridge_config()['public_url'];
    $shown = htmlspecialchars($url, ENT_QUOTES, 'UTF-8');
    http_response_code(302);
    header('Location: ' . $url);
    header('Content-Type: text/html; charset=utf-8');
    header('Cache-Control: no-store');
    echo '<!doctype html><html lang="de"><head><meta charset="utf-8"><title>Alarm-Wächter</title></head>'
        . '<body><p>Das Dashboard ist umgezogen: <a href="' . $shown . '">' . $shown . '</a></p></body></html>';
    exit;
}

// dashboard_api.php: Die alte Oberfläche (noch offener Tab) bekommt eine
// eindeutige Antwort statt eines Fehlers ohne Erklärung.
function bridge_moved_api(): never
{
    header('Content-Type: application/json; charset=utf-8');
    header('Cache-Control: no-store');
    header('X-Content-Type-Options: nosniff');
    if (bridge_on_hold()) {
        http_response_code(503);
        header('Retry-After: 120');
        echo json_encode(['ok' => false, 'code' => 'maintenance',
            'message' => 'Wartung – bitte in ein paar Minuten erneut versuchen.'], JSON_UNESCAPED_UNICODE);
        exit;
    }
    $url = (string)(bridge_read_config()['public_url'] ?? '');
    http_response_code(410);
    echo json_encode(['ok' => false, 'code' => 'moved',
        'message' => 'Das Dashboard ist umgezogen' . ($url !== '' ? ': ' . $url : '.'), 'url' => $url],
        JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES);
    exit;
}
