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
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="csrf" content="<?= htmlspecialchars($csrf, ENT_QUOTES, 'UTF-8') ?>">
  <meta name="role" content="<?= $readonly ? 'readonly' : 'admin' ?>">
  <title>BOSS-925 Alarm-Wächter</title>
  <meta name="theme-color" content="#b42318">
  <link rel="icon" href="assets/favicon.svg" type="image/svg+xml">
  <?php // Für "Zum Home-Bildschirm" auf dem iPhone (iOS ignoriert SVG-Favicons). ?>
  <link rel="apple-touch-icon" href="assets/apple-touch-icon.png">
  <?php // Dateizeit als Versions-Parameter: Browser laden nach jedem Update
        // frische Dateien, statt tagelang eine alte app.js aus dem Cache zu nutzen. ?>
  <link rel="stylesheet" href="assets/style.css?v=<?= (int)@filemtime(__DIR__ . '/assets/style.css') ?>">
</head>
<body>
<?php if (!$authed): ?>
  <main class="login">
    <section class="panel">
      <h1>BOSS-925 Alarm-Wächter</h1>
      <p>Dashboard-Login</p>
      <?php // autocomplete-Attribute statt autocomplete="off": So können
            // Passwort-Manager ein STARKES Passwort speichern und ausfüllen -
            // bei einer öffentlich erreichbaren Seite wichtiger als das
            // (von Browsern ohnehin ignorierte) Abschalten. ?>
      <form method="post">
        <input type="hidden" name="action" value="login">
        <label for="user">Benutzer</label>
        <input id="user" name="user" required autofocus autocomplete="username">
        <label for="password">Passwort</label>
        <input id="password" name="password" type="password" required autocomplete="current-password">
        <p class="error"><?= htmlspecialchars($error, ENT_QUOTES, 'UTF-8') ?></p>
        <p class="submit-row"><button type="submit">Anmelden</button></p>
      </form>
    </section>
  </main>
<?php else: ?>
  <main>
    <header class="topbar">
      <div>
        <h1>BOSS-925 Alarm-Wächter</h1>
        <p data-updated>Letzter Status wird geladen…</p>
        <?php if ($readonly): ?>
          <p class="hint">Angemeldet als <strong><?= htmlspecialchars((string)($_SESSION['user'] ?? ''), ENT_QUOTES, 'UTF-8') ?></strong> – nur Lesezugriff.</p>
        <?php endif; ?>
      </div>
      <form method="post">
        <input type="hidden" name="action" value="logout">
        <input type="hidden" name="csrf" value="<?= htmlspecialchars($csrf, ENT_QUOTES, 'UTF-8') ?>">
        <button type="submit" class="link-button">Abmelden</button>
      </form>
    </header>

    <?php // Demo-Banner: unübersehbar, solange Alarme nur an den Test-Empfänger
          // gehen - inklusive Hinweis, ob der ESP32 die Liste schon übernommen hat. ?>
    <div class="demo-banner" data-demo-banner hidden></div>

    <section class="panel">
      <span data-state class="badge badge-offline">OFFLINE</span>
      <p data-command>Kein Befehl aktiv.</p>
      <?php if (!$readonly): ?>
        <button type="button" data-command-cancel hidden>Befehl abbrechen</button>
      <?php endif; ?>
      <p class="hint" data-command-msg></p>
      <p class="hint" data-watchdog></p>
    </section>

    <section class="panel">
      <div class="grid" data-fields></div>
    </section>

    <section class="panel">
      <h2>Alarm-Empfänger</h2>
      <p class="hint">Diese Liste gilt für <strong>beide</strong> Alarmwege (ESP32-Relaisalarm und
        NAS-Direktalarm). Der ESP32 übernimmt Änderungen automatisch beim nächsten Poll und
        speichert sie dauerhaft – solange die Liste leer ist, nutzt er seine <code>config.h</code>-Startliste.</p>
      <div data-keys-list class="keys-list">Wird geladen…</div>
      <?php if (!$readonly): ?>
        <form data-keys-add class="keys-add">
          <input name="label" placeholder="Name (z. B. Josh)" required maxlength="40">
          <input name="key" placeholder="Bark-Key" required pattern="[A-Za-z0-9_-]{5,64}" autocomplete="off">
          <button type="submit">Hinzufügen</button>
        </form>
      <?php endif; ?>
      <p class="hint" data-keys-msg></p>
    </section>

    <section class="panel">
      <h2>Betriebsmodus</h2>
      <p data-demo-state>Wird geladen…</p>
      <?php if (!$readonly): ?>
        <div class="actions spaced">
          <select data-demo-target aria-label="Demo-Empfänger"></select>
          <button type="button" data-demo-toggle disabled>Wird geladen…</button>
        </div>
      <?php endif; ?>
      <p class="hint">Im <strong>Demo-Modus</strong> gehen ALLE Alarme (Relaisalarm über den ESP32
        <em>und</em> REAL ALARM vom NAS) nur an den gewählten Test-Empfänger – ideal zum Testen,
        z.&nbsp;B. indem man am ESP32 GPIO&nbsp;27 kurz mit GND verbindet.
        Der ESP32 übernimmt den Wechsel erst bei seinem <strong>nächsten Poll</strong> –
        das Banner oben zeigt an, sobald es so weit ist. Nach dem Testen unbedingt
        zurück auf <strong>LIVE</strong> schalten!</p>
      <p class="hint" data-demo-msg></p>
    </section>

    <?php if (!$readonly): ?>
      <section class="panel">
        <div class="actions">
          <button type="button" data-command-test>TEST</button>
          <button type="button" class="danger" data-command-alarm>REAL ALARM</button>
        </div>
        <p class="hint">TEST läuft über den ESP32 (wartet auf dessen Poll, leise Meldung nur an den Status-Empfänger).<br>
          REAL ALARM wird direkt vom NAS an alle Bark-Empfänger gesendet – funktioniert auch, wenn der ESP32 offline ist.</p>
        <p data-alarm-result hidden></p>
      </section>
    <?php endif; ?>

    <section class="panel">
      <h2>Verlauf</h2>
      <p class="hint">Die letzten Ereignisse aus dem NAS-Protokoll: Alarme, Tests, Demo-Wechsel
        und Änderungen an der Empfängerliste. Anders als „Letzter Alarm" oben übersteht
        dieser Verlauf auch einen Neustart des ESP32.</p>
      <div data-log class="log-list">Wird geladen…</div>
    </section>
  </main>
  <script src="assets/app.js?v=<?= (int)@filemtime(__DIR__ . '/assets/app.js') ?>"></script>
<?php endif; ?>
</body>
</html>
