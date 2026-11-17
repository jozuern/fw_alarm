<?php
declare(strict_types=1);

// Seiten-Hüllen der Oberfläche (genutzt von index.php):
// - render_login_page(): die Anmeldeseite (reines HTML-Formular, geht auch
//   ohne JavaScript);
// - render_app_page(): <head> mit den Werten, die das Frontend beim Start
//   braucht (meta-Tags statt Inline-Skript - die CSP verbietet Skript-Inhalte
//   im HTML), <div id="app"> mit einer Ersatzanzeige und die Skripte in
//   fester Reihenfolge.
// Beide teilen sich den Kopf (render_head_tags()): Home-Bildschirm-App,
// Symbole, Design-Start, Stylesheet.

// Reihenfolge ist Pflicht: Jede Datei hängt ihren Teil an window.FW, spätere
// Dateien nutzen die früheren (kein type=module - Importe trügen kein ?v=).
function app_script_paths(): array
{
    return [
        'assets/vendor/htm-preact-3.1.1.umd.js',
        'assets/app/logic.js',
        'assets/app/api.js',
        'assets/app/store.js',
        'assets/app/ui.js',
        'assets/app/view-overview.js',
        'assets/app/view-recipients.js',
        'assets/app/view-log.js',
        'assets/app/view-settings.js',
        'assets/app/main.js',
    ];
}

// Dateizeit als Versions-Parameter: Nach einem Update lädt der Browser die
// neuen Dateien, statt alte aus dem Cache zu mischen.
function asset_url(string $rel): string
{
    return $rel . '?v=' . (int)@filemtime(dirname(__DIR__) . '/' . $rel);
}

// Gemeinsamer Kopf von Anmeldeseite und App (nach <meta charset>).
function render_head_tags(): void
{
    $e = static fn(string $s): string => htmlspecialchars($s, ENT_QUOTES, 'UTF-8');
    ?>
  <?php // viewport-fit=cover: Der Hintergrund läuft bis in die Rundungen des
        // iPhones; die Abstände regelt app.css über env(safe-area-inset-*). ?>
  <meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
  <meta name="theme-color" content="#000000">
  <meta name="color-scheme" content="dark light">
  <?php // Home-Bildschirm-App (Spec 6.7): eigenes Fenster ohne Browser-Leisten,
        // Statusleiste über dem schwarzen Hintergrund. BEWUSST kein Service
        // Worker - das Notfall-Werkzeug muss immer frisch vom NAS laden. ?>
  <link rel="manifest" href="manifest.json">
  <meta name="mobile-web-app-capable" content="yes">
  <meta name="apple-mobile-web-app-capable" content="yes">
  <meta name="apple-mobile-web-app-status-bar-style" content="black-translucent">
  <meta name="apple-mobile-web-app-title" content="Alarm-Wächter">
  <link rel="icon" href="<?= $e(asset_url('assets/favicon.svg')) ?>" type="image/svg+xml">
  <link rel="apple-touch-icon" href="<?= $e(asset_url('assets/apple-touch-icon.png')) ?>">
  <?php // Design (Dunkel/Hell/System) vor dem ersten Zeichnen setzen - sonst
        // blitzt bei „Hell“ kurz Schwarz auf. Synchron und VOR dem Stylesheet
        // (ein Skript danach wartete auf das CSS); eigene Datei wegen der CSP. ?>
  <script src="<?= $e(asset_url('assets/app/theme-boot.js')) ?>"></script>
  <link rel="stylesheet" href="<?= $e(asset_url('assets/app.css')) ?>">
<?php
}

// Anmeldeseite. $error = Meldung des letzten Versuchs (leer = keine),
// $loginUser = Benutzername, der nach einem Fehlversuch stehen bleibt.
function render_login_page(string $error, string $loginUser): void
{
    $e = static fn(string $s): string => htmlspecialchars($s, ENT_QUOTES, 'UTF-8');
    $failed = $error !== '';
    ?>
<!doctype html>
<html lang="de">
<head>
  <meta charset="utf-8">
<?php render_head_tags(); ?>
  <title>Anmelden – Alarm-Wächter</title>
</head>
<body class="login-page">
<main class="login">
  <?php // Marke mit Lämpchen wie in der Seitenleiste der App. Vor der
        // Anmeldung ist der Zustand unbekannt - das Lämpchen ist hier die rote
        // Rundumleuchte als Markenzeichen, kein Status. ?>
  <header class="login-brand">
    <span class="lamp lamp-brand" aria-hidden="true"></span>
    <h1 class="login-title">Alarm-Wächter</h1>
    <p class="login-sub">BOSS-925 · Feuerwehr-Zusatzalarmierung</p>
  </header>
  <?php // autocomplete-Attribute statt autocomplete="off": So können
        // Passwort-Manager ein STARKES Passwort speichern und ausfüllen - bei
        // einer öffentlich erreichbaren Seite wichtiger als das (von Browsern
        // ohnehin ignorierte) Abschalten. ?>
  <form class="login-form" method="post">
    <input type="hidden" name="action" value="login">
    <div class="login-card">
      <div class="login-field">
        <label class="login-label" for="user">Benutzer</label>
        <?php // autocapitalize/autocorrect aus: iPhones machen sonst aus dem
              // Benutzernamen "Josh" ein "JOSH" oder korrigieren ihn weg. ?>
        <input class="login-input" id="user" name="user" required autocomplete="username" autocapitalize="none" autocorrect="off" spellcheck="false"
               value="<?= $e($loginUser) ?>"<?= $loginUser === '' ? ' autofocus' : '' ?>>
      </div>
      <div class="login-field">
        <label class="login-label" for="password">Passwort</label>
        <?php // Steht der Benutzername schon da (Fehlversuch), springt der
              // Cursor gleich ins Passwortfeld; die Meldung ist mit ihm
              // verknüpft (Screenreader lesen sie beim Fokus vor). ?>
        <input class="login-input" id="password" name="password" type="password" required autocomplete="current-password"<?= $loginUser !== '' ? ' autofocus' : '' ?><?= $failed ? ' aria-invalid="true" aria-describedby="login-error"' : '' ?>>
      </div>
    </div>
    <?php if ($failed): ?>
    <p class="msg msg-error login-error" id="login-error" role="alert"><span class="msg-sym" aria-hidden="true">✕</span><span class="msg-text"><?= $e($error) ?></span></p>
    <?php endif; ?>
    <button type="submit" class="btn btn-danger btn-block login-submit">Anmelden</button>
  </form>
  <p class="login-foot">Zusatzalarmierung – ersetzt nicht den Melder.</p>
  <noscript><p class="login-foot">Nach der Anmeldung braucht das Dashboard JavaScript – ohne läuft die Live-Anzeige nicht.</p></noscript>
</main>
</body>
</html>
<?php
}

function render_app_page(array $config, string $csrf, string $role, string $user): void
{
    $e = static fn(string $s): string => htmlspecialchars($s, ENT_QUOTES, 'UTF-8');
    $role = $role === 'admin' ? 'admin' : 'readonly';
    ?>
<!doctype html>
<html lang="de">
<head>
  <meta charset="utf-8">
<?php render_head_tags(); ?>
  <meta name="csrf" content="<?= $e($csrf) ?>">
  <meta name="role" content="<?= $e($role) ?>">
  <meta name="user" content="<?= $e($user) ?>">
  <?php // Öffentliche Basis-URL für die Kurzbefehl-Links (config.php
        // 'public_base_url'; leer = aktuelle Adresse). ?>
  <meta name="public-base" content="<?= $e((string)($config['public_base_url'] ?? '')) ?>">
  <?php // Wirksame Dauer bis zur automatischen Laut-Rückschaltung (Sekunden). ?>
  <meta name="mute-max" content="<?= (int)mute_max_seconds($config) ?>">
  <title>Alarm-Wächter</title>
</head>
<body class="app">
<noscript><p class="noscript">Dieses Dashboard braucht JavaScript – ohne läuft die Live-Anzeige nicht.</p></noscript>
<?php // Ersatzanzeige, bis main.js die App zeichnet (meist nur einen Augenblick).
      // Lädt ein Skript nicht (oder bricht ab), bleibt sie stehen - statt
      // einer leeren schwarzen Seite ohne Alarm-Knopf: Hinweis und Links
      // erscheinen nach kurzer Zeit (app.css, .boot), „Neu laden“ geht auch
      // ohne JavaScript. Gleiches Ziel wie showBootError() in main.js und
      // ui.Fallback. ?>
<div id="app">
  <div class="boot">
    <p class="boot-title">Alarm-Wächter wird geladen…</p>
    <p class="boot-help">Bleibt diese Seite so stehen, bitte neu laden.</p>
    <div class="boot-links">
      <a class="btn btn-tinted btn-block" href="index.php">Neu laden</a>
    </div>
  </div>
</div>
<?php // Abmelden nur per POST + CSRF; das Frontend schickt dieses Formular ab.
      // Das Token hält FW.api.setCsrf() aktuell (es kommt mit jedem Status). ?>
<form id="logout-form" method="post" action="index.php" hidden>
  <input type="hidden" name="action" value="logout">
  <input type="hidden" name="csrf" value="<?= $e($csrf) ?>">
</form>
<?php foreach (app_script_paths() as $path): ?>
<script src="<?= $e(asset_url($path)) ?>" defer></script>
<?php endforeach; ?>
</body>
</html>
<?php
}
