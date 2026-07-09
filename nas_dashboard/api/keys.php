<?php
require __DIR__ . '/../lib/common.php';

// Liefert dem ESP32 die komplette Empfängerliste (Plain-Text-Protokoll wie
// beim Poll). Der ESP32 ruft das nur auf, wenn die im Poll gemeldete
// keys_version von seiner lokalen abweicht. Im Demo-Modus besteht die Liste
// nur aus dem Demo-Key - so testet auch der Relaisalarm am ESP32 gefahrlos.

$config = load_config();
require_machine_auth($config);

$nonce = preg_replace('/[^a-zA-Z0-9_-]/', '', substr((string)($_POST['nonce'] ?? ''), 0, 80));

// Überfällige Stummschaltungen vor der Auslieferung beenden: Der ESP32 soll
// nie einen veralteten Stumm-Zustand übernehmen (bumpt ggf. die Version).
expire_stale_mutes($config);

$state = load_alarm_keys($config);
$entries = effective_alarm_entries($config);

// Pro Empfänger zusätzlich das Stumm-Flag (Arbeitsmodus): Der ESP32 sendet an
// stumme Keys den Critical Alert mit volume=0 (lautlos). Alte Firmware
// ignoriert die muted-Zeilen und alarmiert weiter laut (sicherer Fehlermodus).
$out = "ok=1\nnonce={$nonce}\nversion=" . (int)($state['version'] ?? 0)
     . "\ncount=" . count($entries) . "\n";
foreach ($entries as $i => $entry) {
    $out .= "key{$i}={$entry['key']}\n";
    $out .= "muted{$i}=" . (empty($entry['muted']) ? '0' : '1') . "\n";
}

text_response($out);
