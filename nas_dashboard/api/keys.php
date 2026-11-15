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
// Liste, und überfällige Stummschaltungen rechnet effective_alarm_entries
// darin schon als laut - ABER: Die Box fragt nur bei neuer Version nach, und
// die entsteht erst beim Wegschreiben (siehe expire_stale_mutes()).
expire_stale_mutes($config, true);
// Kaputten Demo-Zustand reparieren (LIVE + neue Version), siehe common.php.
repair_broken_demo_mode($config);

// Liste, Version und Demo-Zustand als EINE Momentaufnahme lesen und daraus die
// Antwort bauen - so passen Version und Inhalt garantiert zusammen. Unter dem
// 'keys'-Lock (alle Schreiber halten ihn, auch demo_set samt Rückrollen),
// soweit er sich öffnen lässt; ohne Schreibrecht lockfrei. Dann ist die
// Lese-REIHENFOLGE wichtig: erst die Liste (Version), dann der Demo-Zustand -
// passend zu demo_set, das erst demo_mode.json und dann alarm_keys.json
// schreibt. Umgekehrt könnte die Box die alte Liste unter der neuen Version
// speichern und nie neu holen. NICHT tauschen.
$snapshot = with_lock_soft($config, 'keys', static fn(): array => [
    'state' => load_alarm_keys($config),
    'demo' => load_demo_mode($config),
], null);
if ($snapshot === null) {
    $snapshot = ['state' => load_alarm_keys($config)];
    $snapshot['demo'] = load_demo_mode($config);
}
$state = $snapshot['state'];
$demo = $snapshot['demo'];
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
