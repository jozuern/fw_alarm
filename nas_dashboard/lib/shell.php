<?php
declare(strict_types=1);

// App-Hülle der neuen Oberfläche: <head> mit den Werten, die das Frontend
// beim Start braucht (meta-Tags statt Inline-Skript - die CSP verbietet
// Skript-Inhalte im HTML), ein leeres <div id="app"> und die Skripte in
// fester Reihenfolge. Genutzt von next.php (später von index.php).

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

function render_app_page(array $config, string $csrf, string $role, string $user): void
{
    $e = static fn(string $s): string => htmlspecialchars($s, ENT_QUOTES, 'UTF-8');
    $role = $role === 'admin' ? 'admin' : 'readonly';
    ?>
<!doctype html>
<html lang="de">
<head>
  <meta charset="utf-8">
  <?php // viewport-fit=cover: Der Hintergrund läuft bis in die Rundungen des
        // iPhones; die Abstände regelt app.css über env(safe-area-inset-*). ?>
  <meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
  <meta name="csrf" content="<?= $e($csrf) ?>">
  <meta name="role" content="<?= $e($role) ?>">
  <meta name="user" content="<?= $e($user) ?>">
  <?php // Öffentliche Basis-URL für die Kurzbefehl-Links (config.php
        // 'public_base_url'; leer = aktuelle Adresse). ?>
  <meta name="public-base" content="<?= $e((string)($config['public_base_url'] ?? '')) ?>">
  <?php // Wirksame Dauer bis zur automatischen Laut-Rückschaltung (Sekunden). ?>
  <meta name="mute-max" content="<?= (int)mute_max_seconds($config) ?>">
  <meta name="theme-color" content="#000000">
  <meta name="color-scheme" content="dark light">
  <title>Alarm-Wächter</title>
  <link rel="icon" href="<?= $e(asset_url('assets/favicon.svg')) ?>" type="image/svg+xml">
  <link rel="apple-touch-icon" href="<?= $e(asset_url('assets/apple-touch-icon.png')) ?>">
  <link rel="stylesheet" href="<?= $e(asset_url('assets/app.css')) ?>">
</head>
<body class="app">
<noscript><p class="noscript">Dieses Dashboard braucht JavaScript – ohne läuft die Live-Anzeige nicht.</p></noscript>
<div id="app"></div>
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
