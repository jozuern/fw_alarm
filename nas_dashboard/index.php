<?php
require __DIR__ . '/lib/common.php';

$config = load_config();
start_dashboard_session();

if ($_SERVER['REQUEST_METHOD'] === 'POST' && ($_POST['action'] ?? '') === 'logout') {
    // Logout nur per POST + CSRF (ein fremdes <img src="?logout=1"> soll dich
    // nicht abmelden können).
    if (is_dashboard_authed() && !empty($_SESSION['csrf'])
        && hash_equals((string)$_SESSION['csrf'], (string)($_POST['csrf'] ?? ''))) {
        session_destroy();
    }
    header('Location: index.php');
    exit;
}

$error = '';
if ($_SERVER['REQUEST_METHOD'] === 'POST' && ($_POST['action'] ?? '') === 'login') {
    $user = (string)($_POST['user'] ?? '');
    $pass = (string)($_POST['password'] ?? '');

    if (login_throttle_blocked($config)) {
        http_response_code(429);
        $error = 'Zu viele Fehlversuche von dieser Adresse - bitte spaeter erneut versuchen.';
    } else {
        // Zwei Konten: der Admin (voller Zugriff) und optional ein reiner
        // Lese-Benutzer (sieht alles, darf aber nichts auslösen oder ändern).
        // Der Lese-Login ist nur aktiv, wenn beide Werte in config.php gesetzt sind.
        // Es wird IMMER genau ein password_verify() ausgeführt - sonst verrät die
        // Antwortzeit, ob ein Benutzername existiert.
        $roUser = (string)($config['dashboard_readonly_user'] ?? '');
        $roHash = (string)($config['dashboard_readonly_password_hash'] ?? '');
        $role = '';
        if (hash_equals((string)$config['dashboard_user'], $user)) {
            if (password_verify($pass, (string)$config['dashboard_password_hash'])) {
                $role = 'admin';
            }
        } elseif ($roUser !== '' && $roHash !== '' && hash_equals($roUser, $user)) {
            if (password_verify($pass, $roHash)) {
                $role = 'readonly';
            }
        } else {
            // Dummy-Vergleich gegen einen festen Wegwerf-Hash (KEIN echtes
            // Passwort, verifiziert gegen nichts Reales): haelt die Antwortzeit
            // konstant, damit sie nicht verraet, ob der Benutzername existiert.
            password_verify($pass, '$2y$10$khBdEi9J4PEa.A0wgqTFn.y3qAqDtiUV/swQATVQUKjg1mYQu391y');
        }
        if ($role !== '') {
            login_throttle_clear($config);
            session_regenerate_id(true);
            $_SESSION['authed'] = true;
            $_SESSION['user'] = $user;
            $_SESSION['role'] = $role;
            ensure_csrf();
            header('Location: index.php');
            exit;
        }
        $failCount = login_throttle_record_failure($config);
        // Genau beim Erreichen der Sperrschwelle EINMAL den Owner leise per
        // Bark informieren (nur wenn bark_key_status in config.php gesetzt
        // ist): Bei einer internetoffenen Seite will man wissen, wenn jemand
        // am Login rüttelt. Weitere Versuche derselben IP landen im
        // 429-Zweig oben und lösen keine weitere Meldung aus.
        [$maxFailures, $lockoutWindow] = login_throttle_limits($config);
        $statusKey = trim((string)($config['bark_key_status'] ?? ''));
        if ($failCount === $maxFailures && $statusKey !== '') {
            bark_send_status($config, $statusKey, 'Dashboard: Login gesperrt',
                date('[d.m.Y H:i] ') . 'Zu viele fehlgeschlagene Login-Versuche von '
                . login_client_ip() . ' - IP fuer ' . (int)($lockoutWindow / 60)
                . ' min gesperrt.');
        }
        sleep(1);   // einfache Brute-Force-Bremse (DSM Auto-Block greift hier nicht)
        $error = 'Login fehlgeschlagen.';
    }
}

$authed = is_dashboard_authed();
$csrf = $authed ? ensure_csrf() : '';
$readonly = $authed && dashboard_role() !== 'admin';
?>
<!doctype html>
<html lang="de">
<head>
  <meta charset="utf-8">
  <?php // viewport-fit=cover: auf iPhones mit Notch darf der dunkle Hintergrund
        // bis in die Rundungen laufen (die safe-area-Abstände regelt das CSS). ?>
  <meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
  <meta name="csrf" content="<?= htmlspecialchars($csrf, ENT_QUOTES, 'UTF-8') ?>">
  <meta name="role" content="<?= $readonly ? 'readonly' : 'admin' ?>">
  <title>BOSS-925 Alarm-Wächter</title>
  <?php // Passend zum dunklen "Leitstand"-Design färbt sich auch die
        // Browser-Leiste (Safari/Chrome auf dem Handy) dunkel ein. ?>
  <meta name="theme-color" content="#14181d">
  <link rel="icon" href="assets/favicon.svg" type="image/svg+xml">
  <?php // Für "Zum Home-Bildschirm" auf dem iPhone (iOS ignoriert SVG-Favicons). ?>
  <link rel="apple-touch-icon" href="assets/apple-touch-icon.png">
  <?php // Dateizeit als Versions-Parameter: Browser laden nach jedem Update
        // frische Dateien, statt tagelang eine alte app.js aus dem Cache zu nutzen. ?>
  <link rel="stylesheet" href="assets/style.css?v=<?= (int)@filemtime(__DIR__ . '/assets/style.css') ?>">
</head>
<body>
<noscript><div class="noscript">Dieses Dashboard braucht JavaScript – ohne läuft die Live-Anzeige nicht.</div></noscript>
<?php if (!$authed): ?>
  <main class="login">
    <div class="brand-block">
      <span class="brand-lamp" aria-hidden="true"></span>
      <h1 class="brand-title">Alarm-Wächter</h1>
      <p class="brand-sub">BOSS-925 · Feuerwehr-Zusatzalarmierung</p>
    </div>
    <section class="panel login-panel">
      <?php // autocomplete-Attribute statt autocomplete="off": So können
            // Passwort-Manager ein STARKES Passwort speichern und ausfüllen -
            // bei einer öffentlich erreichbaren Seite wichtiger als das
            // (von Browsern ohnehin ignorierte) Abschalten. ?>
      <form method="post">
        <input type="hidden" name="action" value="login">
        <label for="user">Benutzer</label>
        <?php // autocapitalize/autocorrect aus: iPhones machen sonst aus dem
              // Benutzernamen "Josh" ein "JOSH" oder korrigieren ihn weg. ?>
        <input id="user" name="user" required autofocus autocomplete="username" autocapitalize="none" autocorrect="off" spellcheck="false">
        <label for="password">Passwort</label>
        <input id="password" name="password" type="password" required autocomplete="current-password">
        <p class="error"><?= htmlspecialchars($error, ENT_QUOTES, 'UTF-8') ?></p>
        <p class="submit-row"><button type="submit" class="danger">Anmelden</button></p>
      </form>
    </section>
  </main>
<?php else: ?>
  <?php // Kopfzeile klebt beim Scrollen oben; die Mini-Lampe spiegelt den
        // großen Status, damit man ihn auch weiter unten auf der Seite sieht. ?>
  <header class="topbar">
    <div class="topbar-inner">
      <span class="mini-lamp" data-state-mini aria-hidden="true"></span>
      <div class="brand">
        <h1>Alarm-Wächter</h1>
        <span class="brand-tag">BOSS-925</span>
      </div>
      <?php if ($readonly): ?>
        <span class="chip-readonly" title="Angemeldet als <?= htmlspecialchars((string)($_SESSION['user'] ?? ''), ENT_QUOTES, 'UTF-8') ?>">Nur Lesezugriff</span>
      <?php endif; ?>
      <form method="post" class="logout-form">
        <input type="hidden" name="action" value="logout">
        <input type="hidden" name="csrf" value="<?= htmlspecialchars($csrf, ENT_QUOTES, 'UTF-8') ?>">
        <button type="submit" class="ghost-button">Abmelden</button>
      </form>
    </div>
  </header>
  <main>
    <?php // Demo-Banner: unübersehbar, solange Alarme nur an den Test-Empfänger
          // gehen - inklusive Hinweis, ob der ESP32 die Liste schon übernommen hat.
          // role="status": Screenreader sagen das Erscheinen des Banners an. ?>
    <div class="demo-banner" data-demo-banner role="status" hidden></div>

    <?php // Held-Bereich: EINE große Statuslampe beantwortet die wichtigste
          // Frage - ist die Alarmkette einsatzbereit? aria-live: Screenreader
          // sagen Zustandswechsel (Einsatzbereit/Offline/Alarm) von selbst an. ?>
    <section class="panel hero">
      <span data-state class="state state-boot" aria-live="polite">Verbinde…</span>
      <p class="hero-updated" data-updated>Letzter Status wird geladen…</p>
      <div class="hero-command">
        <p data-command>Kein Befehl aktiv.</p>
        <?php if (!$readonly): ?>
          <button type="button" data-command-cancel hidden>Befehl abbrechen</button>
        <?php endif; ?>
        <?php // aria-live: Screenreader lesen das Ergebnis einer Aktion vor,
              // ohne dass man die Meldung unterhalb des Knopfs suchen muss.
              // Gilt für alle Rückmelde-Zeilen (data-*-msg / data-alarm-result). ?>
        <p class="hint" data-command-msg aria-live="polite"></p>
        <p class="hint" data-watchdog></p>
      </div>
    </section>

    <section class="panel">
      <h2 class="panel-title">Status der Box</h2>
      <div class="tele" data-fields></div>
      <?php // Statischer Hinweis (Werte stammen aus config.h der Firmware und
            // können sich dort ändern) - erspart die Mittwochs-Verwirrung,
            // warum der Probealarm leiser ankam. ?>
      <details class="explain">
        <summary>Mittwochs leiser? Das Probealarm-Fenster</summary>
        <p>Der wöchentliche Probealarm der Leitstelle löst bewusst die <strong>komplette</strong>
          Alarmkette aus – er kommt nur leiser an: Laut Firmware-Einstellung sendet die Box
          mittwochs zwischen 18:55 und 19:05&nbsp;Uhr mit Lautstärke 5 statt 10; alles andere
          (Ton, Text, Critical Alert) ist identisch. Alarme außerhalb dieses Fensters kommen
          immer in voller Lautstärke.</p>
      </details>
    </section>

    <section class="panel">
      <h2 class="panel-title">Alarm-Empfänger</h2>
      <p class="hint">Gilt für <strong>beide</strong> Alarmwege – Änderungen übernimmt der ESP32 beim nächsten Poll.</p>
      <div data-keys-list class="keys-list">Wird geladen…</div>
      <?php if (!$readonly): ?>
        <form data-keys-add class="keys-add">
          <input name="label" placeholder="Name (z. B. Josh)" required maxlength="40">
          <?php // title erklärt bei ungültiger Eingabe das erwartete Format
                // (der Browser hängt ihn an seine Validierungs-Meldung an);
                // autocapitalize/spellcheck aus, weil iPhones den Key sonst
                // beim Eintippen groß schreiben oder "korrigieren". ?>
          <input name="key" placeholder="Bark-Key" required pattern="[A-Za-z0-9_-]{5,64}"
                 title="5–64 Zeichen: Buchstaben, Ziffern, Unter- und Bindestrich (steht in der Bark-App unter dem QR-Code)"
                 autocomplete="off" autocapitalize="none" spellcheck="false">
          <button type="submit">Hinzufügen</button>
        </form>
      <?php endif; ?>
      <details class="explain">
        <summary>Was bedeutet „Stumm“ (Arbeitsmodus)?</summary>
        <p>Alarme kommen weiterhin als Critical Alert an – nur mit der eingestellten
          <strong>Stumm-Lautstärke</strong> (0&nbsp;=&nbsp;lautlos). Umschalten per Knopf oder
          persönlichem Kurzbefehl-Link („Kurzbefehl-Links“ am Eintrag). Nach 12&nbsp;Std schaltet
          die Box automatisch wieder laut.</p>
      </details>
      <p class="hint" data-keys-msg aria-live="polite"></p>
    </section>

    <?php if (!$readonly): ?>
      <section class="panel">
        <h2 class="panel-title">Auslösen &amp; Testen</h2>
        <div class="actions">
          <button type="button" data-command-test>TEST</button>
          <button type="button" data-command-false-alarm>ENTWARNUNG (Fehlalarm)</button>
        </div>
        <?php // Rot-weißes Absperrband: hier drin haben Klicks echte Folgen. ?>
        <div class="hazard-strip">
          <div class="actions">
            <button type="button" class="danger" data-command-alarm>REAL ALARM</button>
            <button type="button" class="danger" data-command-esp-alarm hidden>DEMO-ALARM über ESP32</button>
          </div>
        </div>
        <details class="explain">
          <summary>Was macht welcher Knopf?</summary>
          <ul>
            <li><strong>TEST</strong> läuft über den ESP32 – leise Meldung nur an den Status-Empfänger.</li>
            <li><strong>ENTWARNUNG</strong> schickt allen eine <strong>normale</strong> Mitteilung
              („letzter Alarm war ein Fehlalarm“) – kein Alarmton.</li>
            <li><strong>REAL ALARM</strong> geht direkt vom NAS an alle – auch wenn der ESP32 offline ist.</li>
            <li><strong>DEMO-ALARM</strong> erscheint nur im Demo-Modus und löst den Alarm über die
              komplette ESP32-Kette aus (wie ein echter Relaisalarm, nur an den Test-Empfänger).</li>
          </ul>
        </details>
        <p data-alarm-result aria-live="polite" hidden></p>

        <?php // Der Demo-Modus wohnt beim Testen (er IST ein Test-Werkzeug).
              // Für die Lese-Rolle entfällt er komplett - den einzig wichtigen
              // Fall (Demo aktiv) zeigt das gelbe Banner oben unübersehbar. ?>
        <div class="sub-block">
          <h3 class="sub-title">Demo-Modus</h3>
          <p data-demo-state>Wird geladen…</p>
          <div class="actions spaced">
            <select data-demo-target aria-label="Demo-Empfänger"></select>
            <button type="button" data-demo-toggle disabled>Wird geladen…</button>
          </div>
          <details class="explain">
            <summary>Wie funktioniert der Demo-Modus?</summary>
            <p>Im <strong>Demo-Modus</strong> gehen alle Alarme nur an den gewählten Test-Empfänger.
              Der ESP32 übernimmt den Wechsel erst beim <strong>nächsten Poll</strong> (das Banner
              oben zeigt den Stand). Nach dem Testen zurück auf <strong>LIVE</strong> schalten!</p>
          </details>
          <p class="hint" data-demo-msg aria-live="polite"></p>
        </div>
      </section>
    <?php endif; ?>

    <section class="panel">
      <h2 class="panel-title">Verlauf</h2>
      <div data-log class="log-list">Wird geladen…</div>
      <?php // Erscheint nur, wenn es voraussichtlich noch ältere Einträge gibt
            // (app.js blendet ihn ein); lädt in 25er-Schritten bis maximal 100. ?>
      <p class="log-more"><button type="button" class="ghost-button" data-log-more hidden>Ältere Ereignisse anzeigen</button></p>
      <details class="explain">
        <summary>Was landet im Verlauf?</summary>
        <p>Die letzten Ereignisse aus dem NAS-Protokoll: Alarme, Tests, Demo-Wechsel
          und Änderungen an der Empfängerliste. Anders als „Letzter Alarm" oben übersteht
          dieser Verlauf auch einen Neustart des ESP32.</p>
      </details>
    </section>
  </main>
  <script src="assets/app.js?v=<?= (int)@filemtime(__DIR__ . '/assets/app.js') ?>"></script>
<?php endif; ?>
</body>
</html>
