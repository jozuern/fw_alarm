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
    if (hash_equals((string)$config['dashboard_user'], $user)
        && password_verify($pass, (string)$config['dashboard_password_hash'])) {
        session_regenerate_id(true);
        $_SESSION['authed'] = true;
        $_SESSION['user'] = $user;
        ensure_csrf();
        header('Location: index.php');
        exit;
    }
    sleep(1);   // einfache Brute-Force-Bremse (DSM Auto-Block greift hier nicht)
    $error = 'Login fehlgeschlagen.';
}

$authed = is_dashboard_authed();
$csrf = $authed ? ensure_csrf() : '';
?>
<!doctype html>
<html lang="de">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="csrf" content="<?= htmlspecialchars($csrf, ENT_QUOTES, 'UTF-8') ?>">
  <title>BOSS-925 Alarm-Wächter</title>
  <link rel="stylesheet" href="assets/style.css">
</head>
<body>
<?php if (!$authed): ?>
  <main class="login">
    <section class="panel">
      <h1>BOSS-925 Alarm-Wächter</h1>
      <p>Dashboard-Login</p>
      <form method="post" autocomplete="off">
        <input type="hidden" name="action" value="login">
        <label for="user">Benutzer</label>
        <input id="user" name="user" required>
        <label for="password">Passwort</label>
        <input id="password" name="password" type="password" required>
        <p class="error"><?= htmlspecialchars($error, ENT_QUOTES, 'UTF-8') ?></p>
        <p style="margin-top:16px"><button type="submit">Anmelden</button></p>
      </form>
    </section>
  </main>
<?php else: ?>
  <main>
    <header class="topbar">
      <div>
        <h1>BOSS-925 Alarm-Wächter</h1>
        <p data-updated>Letzter Status wird geladen…</p>
      </div>
      <form method="post">
        <input type="hidden" name="action" value="logout">
        <input type="hidden" name="csrf" value="<?= htmlspecialchars($csrf, ENT_QUOTES, 'UTF-8') ?>">
        <button type="submit" class="link-button">Abmelden</button>
      </form>
    </header>

    <section class="panel">
      <span data-state class="badge badge-offline">OFFLINE</span>
      <p style="margin-top:10px" data-command>Kein Befehl aktiv.</p>
      <button type="button" data-command-cancel hidden>Befehl abbrechen</button>
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
      <form data-keys-add class="keys-add">
        <input name="label" placeholder="Name (z. B. Josh)" required maxlength="40">
        <input name="key" placeholder="Bark-Key" required pattern="[A-Za-z0-9_-]{5,64}" autocomplete="off">
        <button type="submit">Hinzufügen</button>
      </form>
      <p class="hint" data-keys-msg></p>
    </section>

    <section class="panel">
      <div class="actions">
        <button type="button" data-command-test>TEST</button>
        <button type="button" class="danger" data-command-alarm>REAL ALARM</button>
      </div>
      <p class="hint">TEST läuft über den ESP32 (wartet auf dessen Poll, leise Meldung nur an den Status-Empfänger).<br>
        REAL ALARM wird direkt vom NAS an alle Bark-Empfänger gesendet – funktioniert auch, wenn der ESP32 offline ist.</p>
      <p data-alarm-result hidden></p>
    </section>
  </main>
  <script src="assets/app.js"></script>
<?php endif; ?>
</body>
</html>
