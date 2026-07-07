<?php
require __DIR__ . '/../lib/common.php';

// Liefert dem ESP32 die komplette Empfängerliste (Plain-Text-Protokoll wie
// beim Poll). Der ESP32 ruft das nur auf, wenn die im Poll gemeldete
// keys_version von seiner lokalen abweicht. Im Demo-Modus besteht die Liste
// nur aus dem Demo-Key - so testet auch der Relaisalarm am ESP32 gefahrlos.

$config = load_config();
require_machine_auth($config);

$nonce = preg_replace('/[^a-zA-Z0-9_-]/', '', substr((string)($_POST['nonce'] ?? ''), 0, 80));

$state = load_alarm_keys($config);
$keys = effective_alarm_keys($config);

$out = "ok=1\nnonce={$nonce}\nversion=" . (int)($state['version'] ?? 0)
     . "\ncount=" . count($keys) . "\n";
foreach ($keys as $i => $key) {
    $out .= "key{$i}={$key}\n";
}

text_response($out);
