<?php
require __DIR__ . '/../lib/common.php';

// Lebenszeichen für den Außen-Wächter (eine Cloud-VM, siehe cloud_watcher/).
// Die VM ruft diesen Endpunkt alle 5 Minuten über die ÖFFENTLICHE Adresse auf.
// Antwortet er nicht, ist das NAS von außen nicht erreichbar - Strom oder
// Internet am Standort weg, Router, DDNS, Zertifikat, Web Station. Das kann
// der Cron-Wächter auf dem NAS selbst nie bemerken (er fällt mit aus).
// Umgekehrt merkt sich der Endpunkt jeden gültigen Aufruf (watcher_seen.json):
// Verstummt die VM (z.B. von Oracle als "idle" gestoppt), meldet das der
// Cron-Wächter. So überwachen sich beide gegenseitig.
//
// Eigenes Token (watcher_token_sha256), bewusst NICHT das Maschinen-Token der
// Box: Wer die VM übernimmt, darf weder Status-Pushs der Box fälschen noch
// ihre Befehle abholen. Nur per POST (GET-Parameter landen in Server-Logs).
//
// Absichtlich schlicht und unabhängig vom Rest: kein Lock, Schreiben nur best
// effort. "NAS von außen erreichbar" soll auch dann ok=1 melden, wenn der
// Datenordner klemmt - dafür gibt es die eigene Meldung "NAS-Dashboard DEFEKT".
// Läuft deshalb auch mit älteren Ständen von lib/common.php.

$config = load_config();
$expected = strtolower(trim((string)($config['watcher_token_sha256'] ?? '')));
if ($expected === '') {
    // Nicht eingerichtet: nichts verraten.
    http_response_code(404);
    exit;
}
// Derselbe Hash wie das Maschinen-Token der Box? Dann besäße die VM das
// Box-Token (Status fälschen, Befehle abholen) - genau das soll das eigene
// Token verhindern. Lieber gar nicht antworten (die VM meldet dann "HTTP 404").
if (hash_equals($expected, strtolower(trim((string)($config['machine_token_sha256'] ?? ''))))) {
    error_log('fw_alarm: watcher_token_sha256 ist gleich machine_token_sha256 - alive.php abgeschaltet. '
        . 'Für den Außen-Wächter ein EIGENES Token erzeugen.');
    http_response_code(404);
    exit;
}
$token = (string)($_POST['token'] ?? '');
if (($_SERVER['REQUEST_METHOD'] ?? '') !== 'POST' || $token === ''
    || !hash_equals($expected, hash('sha256', $token))) {
    http_response_code(403);
    exit('FORBIDDEN');
}

// Die Nonce spiegeln: Beweist der VM, dass die Antwort frisch von diesem
// Skript kommt (nicht aus einem Cache, einer Fehlerseite oder einem Portal).
$nonce = substr(preg_replace('/[^A-Za-z0-9]/', '', (string)($_POST['nonce'] ?? '')), 0, 64);

// Kann die VM ihre eigenen Bark-Meldungen gerade nicht zustellen (z.B. der
// Status-Key wurde nach einer Neuinstallation von Bark nur in config.php
// geändert)? Dann zeigt das Dashboard "Außen-Wächter kann nicht melden" -
// sonst stünde dort grün "meldet sich", obwohl kein Ausfall je ankäme.
// 0 = alles ok; ältere VM-Skripte schicken das Feld nicht.
$now = time();
$barkFailedAt = max(0, (int)($_POST['bark_failed_at'] ?? 0));
if ($barkFailedAt > $now) {
    $barkFailedAt = $now;
}
$stored = write_json_file(data_path($config, 'watcher_seen.json'),
    ['seen_at' => $now, 'bark_failed_at' => $barkFailedAt]);

// Zwei Alter für die VM: Läuft der Cron-Wächter auf dem NAS noch (sonst
// würde ein Ausfall der Box nicht gemeldet)? Und wann kam der letzte Status
// der Box (nur als Zusatz in der Meldung - Box-Ausfälle meldet das NAS)?
$latest = read_json_file(data_path($config, 'latest_status.json'), []);
$watchdog = read_json_file(data_path($config, 'watchdog_state.json'), []);
$boxSeen = (int)($latest['seen_at'] ?? 0);
$cronRun = (int)($watchdog['last_run_at'] ?? 0);
// cron_configured=0: Ohne bark_key_status meldet der NAS-Wächter bewusst
// nichts (und schreibt kein last_run_at) - die VM nennt dann den richtigen
// Grund statt "DSM-Aufgabe lief noch nie".
// nas_send_failed=1: Die letzte Admin-Meldung des NAS-Wächters scheiterte,
// seitdem kam keine erfolgreiche - dann erreicht das NAS Bark vermutlich gar
// nicht (ausgehendes Netz/DNS), und auch der manuelle REAL ALARM scheitert.
$cronConfigured = trim((string)($config['bark_key_status'] ?? '')) !== '';
$nasSendFailed = (int)($watchdog['last_send_failed_at'] ?? 0) > (int)($watchdog['last_send_ok_at'] ?? 0);

text_response('ok=1'
    . "\nnonce=" . $nonce
    . "\nstored=" . ($stored ? '1' : '0')
    . "\nbox_age_s=" . ($boxSeen > 0 ? max(0, $now - $boxSeen) : -1)
    . "\ncron_age_s=" . ($cronRun > 0 ? max(0, $now - $cronRun) : -1)
    . "\ncron_configured=" . ($cronConfigured ? '1' : '0')
    . "\nnas_send_failed=" . ($nasSendFailed ? '1' : '0')
    . "\n");
