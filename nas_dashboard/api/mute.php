<?php
require __DIR__ . '/../lib/common.php';

// Arbeitsmodus per iPhone-Kurzbefehl: Jeder Empfänger kann SEINEN Eintrag
// stumm (state=on) oder wieder laut (state=off) schalten, indem er einen
// persönlichen Link aufruft - z.B. automatisch beim Betreten/Verlassen der
// Arbeit ("Kurzbefehle"-App, Automation "URL abrufen"):
//
//   .../api/mute.php?t=<GEHEIM-TOKEN>&state=on|off     (im Dashboard kopierbar)
//   .../api/mute.php?key=<EIGENER_BARK_KEY>&state=on|off  (alte Links, bleiben gültig)
//
// Authentifizierung = Kenntnis des eigenen Geheim-Tokens (zufällig, taucht
// nirgends sonst auf) bzw. des eigenen Bark-Keys. Beides ist lang und nicht
// erratbar - ein bloßer Name wäre es, deshalb gibt es bewusst KEINEN
// name=-Parameter. Bewusst auch per GET erreichbar (anders als die
// Maschinen-Endpoints), damit der Kurzbefehl ein simples "URL öffnen" sein
// kann; der Token steht dafür in Kauf genommen im Webserver-Log.
//
// Stumm heißt: Der Alarm kommt weiterhin als Critical Alert an (durchbricht
// Stummschalter/Fokus), aber mit der im Dashboard eingestellten
// Stumm-Lautstärke (Standard 0 = lautlos). Sicherheitsnetz: Nach
// mute_max_seconds schaltet das NAS automatisch zurück auf laut
// (expire_stale_mutes). Eine Bestätigungs-Nachricht gibt es beim manuellen
// Umschalten bewusst NICHT mehr (störte nur); die automatische Rückschaltung
// meldet sich weiterhin.

$config = load_config();

$token = trim((string)($_POST['t'] ?? $_GET['t'] ?? ''));
$key = trim((string)($_POST['key'] ?? $_GET['key'] ?? ''));
$stateParam = strtolower(trim((string)($_POST['state'] ?? $_GET['state'] ?? '')));

if (!in_array($stateParam, ['on', 'off'], true)) {
    http_response_code(400);
    text_response("ok=0\nerror=state fehlt (on oder off)\n");
}

// Enumerations-Bremse: Für ungültige wie unbekannte Tokens/Keys dieselbe
// verzögerte, generische Antwort - der Endpoint verrät nicht, was existiert.
function mute_reject(): void
{
    usleep(500000);
    http_response_code(404);
    text_response("ok=0\nerror=unbekannter Empfaenger\n");
}

if ($token !== '' ? !valid_mute_token($token) : !valid_bark_key($key)) {
    mute_reject();
}

// Abgelaufene Stummschaltungen zuerst aufräumen (nimmt selbst den keys-Lock,
// deshalb VOR dem eigenen Lock unten).
expire_stale_mutes($config);

$wantMuted = ($stateParam === 'on');

$result = with_lock($config, 'keys', function () use ($config, $token, $key, $wantMuted): array {
    $state = read_json_file(data_path($config, 'alarm_keys.json'), alarm_keys_default());
    $idx = -1;
    foreach (($state['keys'] ?? []) as $i => $entry) {
        $match = $token !== ''
            ? hash_equals((string)($entry['mute_token'] ?? ''), $token)
            : hash_equals((string)($entry['key'] ?? ''), $key);
        if ($match) {
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
        // keine Versions-Erhöhung und kein Log-Spam.
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

// Nur bei echter Änderung loggen, und NACH dem Lock. Bewusst KEIN
// Bestätigungs-Push mehr an den Empfänger (Nutzer-Entscheidung: störte nur) -
// der Kurzbefehl sieht den Erfolg an der ok=1-Antwort, das Dashboard am Badge.
if (!empty($result['changed'])) {
    $entry = $result['entry'];
    append_command_log($config, [
        'event' => $wantMuted ? 'key_muted' : 'key_unmuted',
        'by' => 'Kurzbefehl-Link',
        'source' => 'shortcut',
        'label' => (string)($entry['label'] ?? ''),
        'key' => mask_bark_key((string)($entry['key'] ?? '')),
        'version' => (int)($result['version'] ?? 0),
    ]);
}

text_response("ok=1\nmuted=" . ($wantMuted ? '1' : '0') . "\n");
