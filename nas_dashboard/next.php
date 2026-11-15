<?php
// Vorläufiger Einstieg der neuen Oberfläche, solange index.php noch die
// bisherige zeigt (entfällt mit der Umstellung). Anmelden und Abmelden laufen
// weiter über index.php.
require __DIR__ . '/lib/common.php';
require __DIR__ . '/lib/shell.php';

$config = load_config();
start_dashboard_session();

if (!is_dashboard_authed()) {
    header('Location: index.php');
    exit;
}

$csrf = ensure_csrf();
$role = dashboard_role();
$user = (string)($_SESSION['user'] ?? '');
// Sitzung gleich freigeben: Die Seite liest nur noch, und die ersten
// API-Abfragen derselben Sitzung sollen nicht auf sie warten.
session_write_close();

render_app_page($config, $csrf, $role, $user);
