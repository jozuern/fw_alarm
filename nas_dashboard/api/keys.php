<?php
require __DIR__ . '/../lib/common.php';

// Liefert dem ESP32 die komplette Empfängerliste (Plain-Text-Protokoll wie
// beim Poll). Der ESP32 ruft das nur auf, wenn die im Poll gemeldete
// keys_version von seiner lokalen abweicht. Im Demo-Modus besteht die Liste
// nur aus dem Demo-Key - so testet auch der Relaisalarm am ESP32 gefahrlos.

$config = load_config();
require_machine_auth($config);

$nonce = preg_replace('/[^a-zA-Z0-9_-]/', '', substr((string)($_POST['nonce'] ?? ''), 0, 80));

// Überfällige Stummschaltungen vor der Auslieferung wegschreiben (bumpt ggf.
// die Version). Die Info-Pushs an die Betroffenen laufen erst NACH der
// Antwort - früher liefen sie davor und der ESP32 brach nach seinem
// 4-s-Timeout ab. Nicht fatal: Auch ohne Schreibrecht bekommt der ESP32 die
// Liste (überfällige Stummschaltungen rechnet effective_alarm_entries
// ohnehin schon als laut).
expire_stale_mutes($config, true);

// Liste, Version und Demo-Zustand EINMAL lesen und daraus die Antwort bauen -
// so passen Version und Inhalt garantiert zusammen.
$state = load_alarm_keys($config);
$demo = load_demo_mode($config);
$entries = effective_alarm_entries($config, $state, $demo);

// Pro Empfänger zusätzlich das Stumm-Flag (Arbeitsmodus) und die
// Stumm-Lautstärke: Der ESP32 sendet an stumme Keys den Critical Alert mit
// mutevol statt der vollen Lautstärke (0 = lautlos). Alte Firmware ignoriert
// muted-/mutevol-Zeilen und alarmiert weiter laut bzw. (mit muted-Support,
// ohne mutevol) lautlos - beides sichere Fehlermodi. Die Geheim-Link-Tokens
// werden hier bewusst NICHT ausgeliefert, der ESP32 braucht sie nicht.
// effective_alarm_entries() liefert nur gültige Keys, höchstens
// MAX_ALARM_KEYS - genau das, was die Firmware akzeptiert.
$out = "ok=1\nnonce={$nonce}\nversion=" . (int)($state['version'] ?? 0)
     . "\ncount=" . count($entries) . "\n";
foreach ($entries as $i => $entry) {
    $out .= "key{$i}={$entry['key']}\n";
    $out .= "muted{$i}=" . (empty($entry['muted']) ? '0' : '1') . "\n";
    $out .= "mutevol{$i}=" . (int)($entry['mute_volume'] ?? 0) . "\n";
}

text_response($out);
