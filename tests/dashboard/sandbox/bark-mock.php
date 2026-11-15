<?php
// Bark-Attrappe (php -S 127.0.0.1:8789 bark-mock.php, Env BARK_LOG).
// Jede Anfrage = eine JSON-Zeile im Log. Key-Präfix: fail -> 500,
// bad -> 400, slow -> 4 s Verzögerung, sonst 200.
$uri = parse_url($_SERVER['REQUEST_URI'] ?? '/', PHP_URL_PATH) ?: '/';
$fields = $_POST;
$raw = file_get_contents('php://input');
if ($raw !== '' && str_contains((string)($_SERVER['CONTENT_TYPE'] ?? ''), 'json')) {
    $j = json_decode($raw, true);
    if (is_array($j)) {
        $fields = $j + $fields;
    }
}
$fields += $_GET;
$segments = array_values(array_filter(explode('/', $uri), 'strlen'));
$key = (string)($fields['device_key'] ?? ($segments[0] ?? ''));
unset($fields['device_key']);

header('Content-Type: application/json');
// Ohne Key (z.B. die Bereitschaftsprüfung von start.sh) nur 200, kein Log.
if ($key === '') {
    echo '{"code":200,"message":"success"}';
    return;
}
$log = getenv('BARK_LOG') ?: (__DIR__ . '/bark.log');
@file_put_contents(
    $log,
    json_encode(['t' => time(), 'key' => $key, 'fields' => (object)$fields], JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES) . "\n",
    FILE_APPEND | LOCK_EX
);

if (str_starts_with($key, 'fail')) {
    http_response_code(500);
    echo '{"code":500,"message":"mock failure"}';
} elseif (str_starts_with($key, 'bad')) {
    http_response_code(400);
    echo '{"code":400,"message":"mock bad key"}';
} else {
    if (str_starts_with($key, 'slow')) {
        sleep(4);
    }
    echo '{"code":200,"message":"success"}';
}
