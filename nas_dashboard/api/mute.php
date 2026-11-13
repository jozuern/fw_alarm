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

// Link-Vorschauen dürfen NICHTS umschalten: iMessage, WhatsApp, Signal,
// Mail-Scanner usw. rufen einen verschickten Link schon beim ABSENDER ab
// (teils per HEAD) - ohne diese Sperre würde der Admin einen Kollegen
// unbemerkt stumm schalten, nur indem er ihm seinen Link schickt. Ein
// echter Kurzbefehl ("URL abrufen") sendet GET/POST mit iOS-Kennung.
$method = (string)($_SERVER['REQUEST_METHOD'] ?? 'GET');
$agent = (string)($_SERVER['HTTP_USER_AGENT'] ?? '');
$isPreview = !in_array($method, ['GET', 'POST'], true)
    || preg_match('/facebookexternalhit|facebot|twitterbot|whatsapp|telegrambot|slackbot|discordbot|'
        . 'linkedinbot|skypeuripreview|signal|bingpreview|embedly|ms-office|safelinks|'
        . 'preview|crawler|spider|bot\/|\bbot\b/i', $agent) === 1;
if ($isPreview) {
    text_response("ok=0\nerror=Vorschau - nichts geaendert\n");
}

$token = trim((string)($_POST['t'] ?? $_GET['t'] ?? ''));
$key = trim((string)($_POST['key'] ?? $_GET['key'] ?? ''));
$stateParam = strtolower(trim((string)($_POST['state'] ?? $_GET['state'] ?? '')));

if (!in_array($stateParam, ['on', 'off'], true)) {
    http_response_code(400);
    text_response("ok=0\nerror=state fehlt (on oder off)\n");
}

// Bremse gegen Durchprobieren: NUR Fehlversuche (ungültige/unbekannte Tokens)
// zählen pro Absender; nach zu vielen bekommen weitere FEHLversuche 429.
// Ein GÜLTIGER Token wird nie gebremst: Hinter einer gemeinsamen Adresse
// (Wachen-WLAN, Mobilfunk-NAT) könnte sonst jeder mit ein paar
// Unsinns-Aufrufen die Links aller Kollegen sperren - auch das sichere
// "wieder laut". (Früher: usleep pro Fehlversuch - das hielt nur PHP-Worker
// fest und war parallel umgehbar.) Für ungültige wie unbekannte Tokens
// dieselbe generische Antwort - der Endpoint verrät nicht, was existiert.
const MUTE_MAX_FAILURES = 20;
const MUTE_FAILURE_WINDOW = 900;

function mute_reject(array $config): void
{
    $hit = throttle_hit($config, 'mute', MUTE_MAX_FAILURES, MUTE_FAILURE_WINDOW);
    if (!empty($hit['blocked'])) {
        http_response_code(429);
        text_response("ok=0
error=zu viele Fehlversuche - spaeter erneut
");
    }
    http_response_code(404);
    text_response("ok=0
error=unbekannter Empfaenger
");
}

// Gehört der Token (bzw. alt: der Bark-Key) zu diesem Listeneintrag?
function mute_entry_matches($entry, string $token, string $key): bool
{
    if (!is_array($entry)) {
        return false;
    }
    return $token !== ''
        ? hash_equals((string)($entry['mute_token'] ?? ''), $token)
        : hash_equals((string)($entry['key'] ?? ''), $key);
}

if ($token !== '' ? !valid_mute_token($token) : !valid_bark_key($key)) {
    mute_reject($config);
}

// Erst lock-frei nachsehen, ob es den Empfänger überhaupt gibt: Fehlversuche
// brauchen so weder den 'keys'-Lock noch den Mute-Ablauf darunter.
$known = false;
foreach ((load_alarm_keys($config)['keys'] ?? []) as $entry) {
    if (mute_entry_matches($entry, $token, $key)) {
        $known = true;
        break;
    }
}
if (!$known) {
    mute_reject($config);
}

// Abgelaufene Stummschaltungen zuerst aufräumen (nimmt selbst den keys-Lock,
// deshalb VOR dem eigenen Lock unten).
expire_stale_mutes($config);

$wantMuted = ($stateParam === 'on');

$result = mutate_alarm_keys($config, function (array &$state) use ($token, $key, $wantMuted): array {
    $idx = -1;
    foreach (($state['keys'] ?? []) as $i => $entry) {
        if (mute_entry_matches($entry, $token, $key)) {
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
    $wasPermanent = mute_is_permanent($entry);
    $state['keys'][$idx]['muted'] = $wantMuted;
    $state['keys'][$idx]['muted_at'] = $wantMuted ? time() : 0;
    // "Laut" (state=off) beendet auch eine DAUERHAFTE Stummschaltung: Der
    // Kurzbefehl darf immer in die sichere Richtung wirken - wer sich selbst
    // wieder laut schaltet, soll danach nicht heimlich weiter vom
    // Sicherheitsnetz ausgenommen sein. Umgekehrt kann der Link eine
    // Dauer-Stummschaltung NICHT beginnen (das geht nur im Dashboard).
    if (!$wantMuted) {
        $state['keys'][$idx]['mute_permanent'] = false;
    }
    return ['found' => true, 'changed' => true, 'write' => true, 'bump' => true,
        'entry' => $state['keys'][$idx], 'permanent_ended' => ($wasPermanent && !$wantMuted)];
});

if (!empty($result['write_failed']) || (!isset($result['found']) && empty($result['ok']))) {
    // Speichern gescheitert bzw. Liste unlesbar: ehrlich melden.
    http_response_code(500);
    text_response("ok=0\nerror=speichern fehlgeschlagen\n");
}
if (empty($result['found'])) {
    mute_reject($config);
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
    // Hat der Link eine DAUERHAFTE Stummschaltung beendet, das extra
    // festhalten - sonst wäre im Verlauf nicht zu sehen, dass eine bewusst
    // gesetzte Dauer-Einstellung nebenbei aufgehoben wurde.
    if (!empty($result['permanent_ended'])) {
        append_command_log($config, [
            'event' => 'key_mute_permanent',
            'by' => 'Kurzbefehl-Link',
            'source' => 'shortcut',
            'label' => (string)($entry['label'] ?? ''),
            'key' => mask_bark_key((string)($entry['key'] ?? '')),
            'permanent' => false,
            'version' => (int)($result['version'] ?? 0),
        ]);
    }
}

text_response("ok=1\nmuted=" . ($wantMuted ? '1' : '0') . "\n");
