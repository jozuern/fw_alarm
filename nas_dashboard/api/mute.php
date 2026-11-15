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
// Ebenso Vorab-Abrufe des BROWSERS selbst: Chrome lädt Adressen aus der
// Adresszeile/Verlauf vorab (Sec-Purpose: prefetch / prerender), Firefox
// kennzeichnet das mit X-Moz/Purpose. Sonst schaltete schon das Eintippen
// eines bekannten Links am PC unbemerkt um.
$purpose = strtolower(implode(' ', [
    (string)($_SERVER['HTTP_SEC_PURPOSE'] ?? ''),
    (string)($_SERVER['HTTP_PURPOSE'] ?? ''),
    (string)($_SERVER['HTTP_X_PURPOSE'] ?? ''),
    (string)($_SERVER['HTTP_X_MOZ'] ?? ''),
]));
$isPreview = !in_array($method, ['GET', 'POST'], true)
    || preg_match('/prefetch|prerender|preview/', $purpose) === 1
    || preg_match('/facebookexternalhit|facebot|twitterbot|whatsapp|telegrambot|slackbot|discordbot|'
        . 'linkedinbot|skypeuripreview|signal|bingpreview|embedly|ms-office|safelinks|'
        . 'preview|crawler|spider|bot\/|\bbot\b/i', $agent) === 1;
if ($isPreview) {
    text_response("ok=0\nerror=Vorschau - nichts geaendert\n");
}

// Ruft ein MENSCH den Link im Browser auf (Kurzbefehl "URL öffnen", Antippen
// in den Notizen, Test am PC), bekommt er eine lesbare Bestätigungsseite statt
// der rohen Zeilen "ok=1 / muted=1". Erkannt wird das an der Kennung einer
// Seiten-Navigation (Sec-Fetch-Dest: document bzw. Accept: text/html).
// Kurzbefehle mit "Inhalte von URL abrufen" und Skripte senden das nicht und
// bekommen UNVERÄNDERT die Text-Antwort - an deren Format ändert sich nichts.
define('MUTE_WANTS_HTML', $method === 'GET' && (
    (string)($_SERVER['HTTP_SEC_FETCH_DEST'] ?? '') === 'document'
    || stripos((string)($_SERVER['HTTP_ACCEPT'] ?? ''), 'text/html') !== false));

// Antwort je nach Aufrufer: Text (Kurzbefehl/Skript) oder kleine HTML-Seite
// (Browser). $lamp = Statuslampe der Seite: ok (grün), stale (gelb), crit (rot).
// Die Seite nutzt das Stylesheet des Dashboards (CSP: keine Inline-Styles)
// und enthält bewusst weder den Token noch den Bark-Key.
function mute_answer(string $text, string $lamp, string $headline, string $detail): void
{
    if (!MUTE_WANTS_HTML) {
        text_response($text);
    }
    mute_page($lamp, $headline, $detail);
}

function mute_page(string $lamp, string $headline, string $detail): void
{
    $h = static fn(string $value): string => htmlspecialchars($value, ENT_QUOTES, 'UTF-8');
    $css = '../assets/style.css?v=' . (int)@filemtime(dirname(__DIR__) . '/assets/style.css');
    header('Content-Type: text/html; charset=utf-8');
    header('Cache-Control: no-store');
    echo '<!doctype html><html lang="de"><head><meta charset="utf-8">'
        . '<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">'
        . '<meta name="theme-color" content="#14181d"><title>Alarm-Wächter – Arbeitsmodus</title>'
        . '<link rel="icon" href="../assets/favicon.svg" type="image/svg+xml">'
        . '<link rel="stylesheet" href="' . $h($css) . '"></head><body>'
        . '<main class="login mute-page"><section class="panel">'
        . '<p class="state state-' . $h($lamp) . '">' . $h($headline) . '</p>'
        . '<p class="mute-detail">' . $h($detail) . '</p>'
        . '<p class="hint">Alarm-Wächter, Arbeitsmodus. Diese Seite kannst du schließen.</p>'
        . '</section></main></body></html>';
    exit;
}

// Bestätigungsseite OHNE Wirkung: Nach einem erfolgreichen Umschalten leitet
// ein Browser hierher um (siehe Ende der Datei). Diese Adresse enthält weder
// Token noch Key und schaltet NICHTS. Neu laden, "Zurück" oder ein von iOS
// wiederhergestellter Tab zeigen so nur noch einmal die Bestätigung - der
// Empfänger wird dadurch nicht unbemerkt erneut stumm geschaltet.
// Parameter (nur Zahlen/Schalter, nichts Persönliches): done=on|off,
// vol=0..10, until=Zeitpunkt der automatischen Rückschaltung (0 = keine),
// perm=1 dauerhaft stumm, at=Zeitpunkt des Umschaltens, same=1 = der Aufruf
// hat nichts geändert (Zustand stimmte schon; at ist dann der Beginn der
// bestehenden Stummschaltung bzw. 0).
$done = strtolower(trim((string)($_GET['done'] ?? '')));
if ($method === 'GET' && ($done === 'on' || $done === 'off')) {
    $at = max(0, (int)($_GET['at'] ?? 0));
    $same = (string)($_GET['same'] ?? '') === '1';
    $when = $at > 0 ? date('d.m.', $at) . ' um ' . date('H:i', $at) . ' Uhr' : '';
    if ($same) {
        $stamp = $when !== '' ? ' Unverändert – stumm seit ' . $when . '.' : ' Unverändert – war schon so eingestellt.';
    } else {
        $stamp = $when !== '' ? ' Umgeschaltet am ' . $when . '.' : '';
    }
    if ($done === 'off') {
        mute_page('ok', $same ? 'Laut' : 'Wieder laut', 'Alarme kommen bei dir mit vollem Ton an.' . $stamp);
    }
    $volume = max(0, min(10, (int)($_GET['vol'] ?? 0)));
    $until = max(0, (int)($_GET['until'] ?? 0));
    $detail = 'Alarme kommen bei dir weiterhin an und bleiben sichtbar, aber '
        . ($volume === 0 ? 'lautlos' : 'nur leise (Lautstärke ' . $volume . ' von 10)') . '.';
    if ((string)($_GET['perm'] ?? '') === '1') {
        $detail .= ' Du bist dauerhaft stumm geschaltet: Das endet erst, wenn du oder der Admin wieder laut schaltet.';
    } elseif ($until > 0) {
        $detail .= ' Spätestens am ' . date('d.m.', $until) . ' um ' . date('H:i', $until)
            . ' Uhr wird automatisch wieder laut geschaltet.';
    }
    mute_page('stale', 'Stumm geschaltet', $detail . $stamp);
}

$token = trim((string)($_POST['t'] ?? $_GET['t'] ?? ''));
$key = trim((string)($_POST['key'] ?? $_GET['key'] ?? ''));
$stateParam = strtolower(trim((string)($_POST['state'] ?? $_GET['state'] ?? '')));

if (!in_array($stateParam, ['on', 'off'], true)) {
    http_response_code(400);
    mute_answer("ok=0\nerror=state fehlt (on oder off)\n", 'crit', 'Link unvollständig',
        'In diesem Link fehlt die Angabe, ob stumm oder laut geschaltet werden soll. '
        . 'Bitte den Link im Dashboard neu kopieren. Es wurde nichts geändert.');
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
        mute_answer("ok=0\nerror=zu viele Fehlversuche - spaeter erneut\n", 'crit', 'Zu viele Fehlversuche',
            'Von dieser Adresse kamen zu viele ungültige Aufrufe. Bitte später erneut versuchen.');
    }
    http_response_code(404);
    mute_answer("ok=0\nerror=unbekannter Empfaenger\n", 'crit', 'Unbekannter Link',
        'Dieser Link gehört zu keinem Empfänger (mehr). Bitte beim Admin einen neuen Link holen. '
        . 'Es wurde nichts geändert.');
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

// Speichern gescheitert, Liste unlesbar oder Datenordner gesperrt: ehrlich
// melden (für Browser als Seite) - und NICHT als Fehlversuch zählen.
function mute_storage_failed(): void
{
    http_response_code(500);
    mute_answer("ok=0\nerror=speichern fehlgeschlagen\n", 'crit', 'Nicht gespeichert',
        'Die Änderung konnte gerade nicht gespeichert werden. Bitte später erneut versuchen '
        . 'oder den Admin informieren.');
}

if ($token !== '' ? !valid_mute_token($token) : !valid_bark_key($key)) {
    mute_reject($config);
}

// Liste samt Sicherung unlesbar? Dann ist JEDER Link "unbekannt" - das wäre
// gelogen und zählte gültige Links als Fehlversuche.
$keysState = read_alarm_keys_state($config);
if ($keysState === null) {
    mute_storage_failed();
}

// Erst lock-frei nachsehen, ob es den Empfänger überhaupt gibt: Fehlversuche
// brauchen so weder den 'keys'-Lock noch den Mute-Ablauf darunter.
$known = false;
foreach (($keysState['keys'] ?? []) as $entry) {
    if (mute_entry_matches($entry, $token, $key)) {
        $known = true;
        break;
    }
}
if (!$known) {
    mute_reject($config);
}

// Abgelaufene Stummschaltungen zuerst aufräumen (nimmt selbst den keys-Lock,
// deshalb VOR dem eigenen Lock unten). Nicht fatal - ein gesperrter
// Datenordner meldet sich gleich unten verständlich.
expire_stale_mutes($config, true);

$wantMuted = ($stateParam === 'on');

// $soft = true: Lässt sich der Lock nicht öffnen, kommt write_failed zurück
// statt eines Abbruchs mit Rohtext (Browser bekämen sonst keine Seite).
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
    // "Laut" (state=off) beendet auch eine DAUERHAFTE Stummschaltung (Regel
    // in apply_mute_change): Wer sich selbst wieder laut schaltet, soll danach
    // nicht heimlich weiter vom Sicherheitsnetz ausgenommen sein. Umgekehrt
    // kann der Link eine Dauer-Stummschaltung NICHT beginnen (das geht nur im
    // Dashboard).
    $permanentEnded = apply_mute_change($state['keys'][$idx], $wantMuted);
    return ['found' => true, 'changed' => true, 'write' => true, 'bump' => true,
        'entry' => $state['keys'][$idx], 'permanent_ended' => $permanentEnded];
}, true);

if (!empty($result['write_failed']) || (!isset($result['found']) && empty($result['ok']))) {
    // Speichern gescheitert bzw. Liste unlesbar: ehrlich melden.
    mute_storage_failed();
}
if (empty($result['found'])) {
    mute_reject($config);
}

// Nur bei echter Änderung loggen, und NACH dem Lock. Bewusst KEIN
// Bestätigungs-Push mehr an den Empfänger (Nutzer-Entscheidung: störte nur) -
// der Kurzbefehl sieht den Erfolg an der ok=1-Antwort, das Dashboard am Badge.
if (!empty($result['changed'])) {
    $entry = $result['entry'];
    $logExtra = ['source' => 'shortcut', 'version' => (int)($result['version'] ?? 0)];
    log_key_event($config, $wantMuted ? 'key_muted' : 'key_unmuted', $entry, 'Kurzbefehl-Link', $logExtra);
    // Optionale Admin-Meldung (Art mute_changed, Standard: aus - sonst käme
    // bei jedem Kurzbefehl der Kollegen ein Push). Erst NACH der Antwort,
    // damit der Kurzbefehl nicht auf den Bark-Versand wartet.
    if (admin_notify_enabled($config, 'mute_changed')) {
        $changedLabel = (string)($entry['label'] ?? '?');
        defer_after_response(static function () use ($config, $changedLabel, $wantMuted): void {
            admin_notify_send($config, 'mute_changed', 'Arbeitsmodus',
                date('[d.m.Y H:i] ') . $changedLabel . ' hat sich per Kurzbefehl '
                . ($wantMuted ? 'STUMM geschaltet.' : 'wieder LAUT geschaltet.'));
        });
    }
    // Hat der Link eine DAUERHAFTE Stummschaltung beendet, das extra
    // festhalten - sonst wäre im Verlauf nicht zu sehen, dass eine bewusst
    // gesetzte Dauer-Einstellung nebenbei aufgehoben wurde.
    if (!empty($result['permanent_ended'])) {
        log_key_event($config, 'key_mute_permanent', $entry, 'Kurzbefehl-Link', $logExtra + ['permanent' => false]);
    }
}

// Kurzbefehle und Skripte bekommen wie immer nur ok=1/muted=…
if (!MUTE_WANTS_HTML) {
    text_response("ok=1\nmuted=" . ($wantMuted ? '1' : '0') . "\n");
}

// Browser: NICHT hier antworten, sondern auf die wirkungslose
// Bestätigungsseite umleiten (done=…, siehe oben). Sonst stünde die
// umschaltende Adresse im Tab, und ein späteres Neu-Laden würde erneut
// schalten. 303 = "danach per GET weiter"; der Token-Link landet so auch
// nicht im Verlauf-Eintrag der angezeigten Seite.
$entry = is_array($result['entry'] ?? null) ? $result['entry'] : [];
// Hat der Aufruf nichts geändert (z.B. nur nachsehen), NICHT "jetzt
// umgeschaltet" behaupten: Bei Stumm zählt der Beginn der bestehenden
// Stummschaltung, bei Laut gibt es keinen gespeicherten Zeitpunkt.
if (!empty($result['changed'])) {
    $query = ['done' => $wantMuted ? 'on' : 'off', 'at' => time()];
} else {
    $query = ['done' => $wantMuted ? 'on' : 'off', 'same' => 1,
        'at' => $wantMuted ? (int)($entry['muted_at'] ?? 0) : 0];
}
if ($wantMuted) {
    $maxMute = mute_max_seconds($config);
    $mutedAt = (int)($entry['muted_at'] ?? 0);
    $permanent = mute_is_permanent($entry);
    $query['vol'] = mute_volume_of($entry);
    $query['perm'] = $permanent ? 1 : 0;
    $query['until'] = (!$permanent && $maxMute > 0 && $mutedAt > 0) ? $mutedAt + $maxMute : 0;
}
header('Cache-Control: no-store');
header('Location: mute.php?' . http_build_query($query), true, 303);
exit;
