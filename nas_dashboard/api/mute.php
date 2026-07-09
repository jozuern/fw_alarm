<?php
require __DIR__ . '/../lib/common.php';

// Arbeitsmodus per iPhone-Kurzbefehl: Jeder Empfänger kann SEINEN Eintrag
// stumm (state=on) oder wieder laut (state=off) schalten, indem er einen
// persönlichen Link aufruft - z.B. automatisch beim Betreten/Verlassen der
// Arbeit ("Kurzbefehle"-App, Automation "URL abrufen"):
//
//   .../api/mute.php?key=<EIGENER_BARK_KEY>&state=on|off
//
// Authentifizierung = Kenntnis des eigenen Bark-Keys: Der Key ist lang und
// zufällig, und wer ihn kennt, könnte der Person ohnehin beliebige
// Bark-Nachrichten schicken - Stummschalten ist das kleinere Recht. Bewusst
// auch per GET erreichbar (anders als die Maschinen-Endpoints), damit der
// Kurzbefehl ein simples "URL öffnen" sein kann; der Key steht dafür in
// Kauf genommen im Webserver-Log.
//
// Stumm heißt: Der Alarm kommt weiterhin als Critical Alert an (durchbricht
// Stummschalter/Fokus), aber mit volume=0 - also lautlos. Sicherheitsnetz:
// Nach mute_max_seconds schaltet das NAS automatisch zurück auf laut
// (expire_stale_mutes).

$config = load_config();

$key = trim((string)($_POST['key'] ?? $_GET['key'] ?? ''));
$stateParam = strtolower(trim((string)($_POST['state'] ?? $_GET['state'] ?? '')));

if (!in_array($stateParam, ['on', 'off'], true)) {
    http_response_code(400);
    text_response("ok=0\nerror=state fehlt (on oder off)\n");
}

// Enumerations-Bremse: Für ungültige wie unbekannte Keys dieselbe verzögerte,
// generische Antwort - der Endpoint verrät nicht, welche Keys existieren.
function mute_reject(): void
{
    usleep(500000);
    http_response_code(404);
    text_response("ok=0\nerror=unbekannter Empfaenger\n");
}

if (!valid_bark_key($key)) {
    mute_reject();
}

// Abgelaufene Stummschaltungen zuerst aufräumen (nimmt selbst den keys-Lock,
// deshalb VOR dem eigenen Lock unten).
expire_stale_mutes($config);

$wantMuted = ($stateParam === 'on');

$result = with_lock($config, 'keys', function () use ($config, $key, $wantMuted): array {
    $state = read_json_file(data_path($config, 'alarm_keys.json'), alarm_keys_default());
    $idx = -1;
    foreach (($state['keys'] ?? []) as $i => $entry) {
        if (hash_equals((string)($entry['key'] ?? ''), $key)) {
            $idx = $i;
            break;
        }
    }
    if ($idx < 0) {
        return ['found' => false];
    }
    $entry = $state['keys'][$idx];
    if (!empty($entry['muted']) === $wantMuted) {
        // Idempotent: Zustand stimmt schon (z.B. Geofence doppelt ausgelöst) -
        // keine Versions-Erhöhung, kein Log-Spam, keine erneute Bestätigung.
        return ['found' => true, 'changed' => false, 'entry' => $entry];
    }
    $state['keys'][$idx]['muted'] = $wantMuted;
    $state['keys'][$idx]['muted_at'] = $wantMuted ? time() : 0;
    $state['version'] = (int)($state['version'] ?? 0) + 1;
    if (!backup_then_write($config, 'alarm_keys.json', $state)) {
        return ['found' => true, 'changed' => false, 'entry' => $entry, 'write_failed' => true];
    }
    return ['found' => true, 'changed' => true, 'entry' => $state['keys'][$idx], 'version' => (int)$state['version']];
});

if (empty($result['found'])) {
    mute_reject();
}
if (!empty($result['write_failed'])) {
    http_response_code(500);
    text_response("ok=0\nerror=speichern fehlgeschlagen\n");
}

// Log + Bestätigungs-Push nur bei echter Änderung, und NACH dem Lock.
if (!empty($result['changed'])) {
    $entry = $result['entry'];
    append_command_log($config, [
        'event' => $wantMuted ? 'key_muted' : 'key_unmuted',
        'by' => 'Kurzbefehl-Link',
        'source' => 'shortcut',
        'label' => (string)($entry['label'] ?? ''),
        'key' => mask_bark_key($key),
        'version' => (int)($result['version'] ?? 0),
    ]);
    // Leise Bestätigung an den Empfänger selbst: So sieht man sofort auf dem
    // iPhone, dass der Kurzbefehl funktioniert hat (ASCII-Texte!).
    if ($wantMuted) {
        $maxAge = mute_max_seconds($config);
        $autoInfo = $maxAge > 0
            ? 'Automatisch wieder LAUT nach ' . max(1, (int)round($maxAge / 3600)) . ' Std oder per Verlassen-Link.'
            : 'ACHTUNG: Kein automatisches Zurueckschalten - Verlassen-Link nicht vergessen!';
        bark_send_status($config, $key, 'Alarmton STUMM (Arbeitsmodus)',
            date('[d.m.Y H:i] ') . 'Alarme kommen jetzt ohne Ton an (weiterhin sichtbar). ' . $autoInfo);
    } else {
        bark_send_status($config, $key, 'Alarmton wieder LAUT',
            date('[d.m.Y H:i] ') . 'Arbeitsmodus beendet - Alarme kommen wieder als Critical Alert mit Ton.');
    }
}

text_response("ok=1\nmuted=" . ($wantMuted ? '1' : '0') . "\n");
