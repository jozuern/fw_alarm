<?php
// Test-Gegenstelle für die Brücken-Tests: spielt den neuen Server.
// Start: BRIDGE_UP_LOG=<datei> php -S 127.0.0.1:8791 upstream.php
// Jeder Aufruf = eine JSON-Zeile im Log (Methode, Pfad, Query, Kopfzeilen,
// Rumpf). Die Antwort richtet sich nach der Query:
//   code=<n>     dieser Statuscode
//   forbidden=1  403 mit dem Rumpf der App bei falschem Maschinen-Token ("FORBIDDEN")
//   cfm=1        zusätzlich die Kopfzeile "cf-mitigated: challenge"
//   redirect=1   303 mit relativer Location, CSP und einem Cookie
//   slow=<s>     erst nach s Sekunden antworten
//   sonst        200 text/plain "ok=1\nnonce=<nonce aus dem POST>"
$headers = [];
foreach (getallheaders() as $name => $value) {
    $headers[strtolower($name)] = $value;
}
$body = (string)file_get_contents('php://input');
$log = getenv('BRIDGE_UP_LOG');
if ($log !== false && $log !== '') {
    file_put_contents($log, json_encode([
        'method' => $_SERVER['REQUEST_METHOD'] ?? '',
        'path' => parse_url($_SERVER['REQUEST_URI'] ?? '/', PHP_URL_PATH),
        'query' => (string)($_SERVER['QUERY_STRING'] ?? ''),
        'headers' => $headers,
        'body' => $body,
    ], JSON_UNESCAPED_SLASHES) . "\n", FILE_APPEND | LOCK_EX);
}

if (isset($_GET['slow'])) {
    sleep((int)$_GET['slow']);
}
if (isset($_GET['cfm'])) {
    header('cf-mitigated: challenge');
}
if (isset($_GET['redirect'])) {
    http_response_code(303);
    header('Location: mute.php?done=on');
    header("Content-Security-Policy: default-src 'self'");
    header('Set-Cookie: x=1');
    header('Strict-Transport-Security: max-age=31536000');
    exit;
}
if (isset($_GET['forbidden'])) {
    // Wie require_machine_auth() in nas_dashboard/lib/common.php.
    http_response_code(403);
    exit('FORBIDDEN');
}
header('Content-Type: text/plain; charset=utf-8');
if (isset($_GET['code'])) {
    http_response_code((int)$_GET['code']);
    echo 'code ' . (int)$_GET['code'] . "\n";
    exit;
}
echo "ok=1\nnonce=" . (string)($_POST['nonce'] ?? '');
