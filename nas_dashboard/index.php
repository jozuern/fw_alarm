<?php
require __DIR__ . '/lib/common.php';
require __DIR__ . '/lib/shell.php';

$config = load_config();
// Eine Sitzung gibt es nur für Angemeldete: Sie wird fortgesetzt, wenn der
// Browser ein Sitzungs-Cookie schickt (resume_dashboard_session), und erst
// bei einer ERFOLGREICHEN Anmeldung neu angelegt. Die Anmeldeseite selbst
// braucht keine (der Schutz des Login-Formulars hängt an Sec-Fetch-Site).

if ($_SERVER['REQUEST_METHOD'] === 'POST' && ($_POST['action'] ?? '') === 'logout') {
    // Logout nur per POST + CSRF (ein fremdes <img src="?logout=1"> soll dich
    // nicht abmelden können).
    // Abmelden räumt vollständig auf: Sitzungsdaten, Sitzung UND Cookie.
    if (resume_dashboard_session() && !empty($_SESSION['csrf'])
        && hash_equals((string)$_SESSION['csrf'], (string)($_POST['csrf'] ?? ''))) {
        end_dashboard_session();
    }
    header('Location: index.php');
    exit;
}

$error = '';
// Nach einem Fehlversuch bleibt der Benutzername im Formular stehen (nur das
// Passwort muss neu eingetippt werden). Gekürzt, die Ausgabe ist escaped.
$loginUser = '';
if ($_SERVER['REQUEST_METHOD'] === 'POST' && ($_POST['action'] ?? '') === 'login') {
    $user = (string)($_POST['user'] ?? '');
    $pass = (string)($_POST['password'] ?? '');
    $loginUser = substr($user, 0, 80);

    // Login-POST von einer FREMDEN Seite (Login-CSRF)? Sonst könnte jede
    // Webseite den Browser des Admins Fehlversuche absenden lassen und so
    // seine IP aussperren - genau dann, wenn er den REAL ALARM braucht.
    // Ablehnen, BEVOR irgendetwas gezählt wird.
    // Maßgeblich ist allein der Browser-Header Sec-Fetch-Site: Er sagt
    // eindeutig, ob das Formular von einer fremden Seite kam. BEWUSST kein
    // Vergleich von Origin und Host: Hinter einem Proxy/Portal, der den
    // Host-Header umschreibt, würde der JEDEN Login ablehnen - und ohne Login
    // gibt es keinen manuellen REAL ALARM. Fehlt der Header (sehr alte
    // Browser, Aufruf per http://), wird der Login nicht blockiert.
    // Erlaubt sind nur 'same-origin' (das eigene Formular) und 'none' (vom
    // Browser selbst ausgelöst, z.B. Neu-Laden). Auch 'same-site' wird
    // abgelehnt: Das wäre eine ANDERE Seite unter derselben Domain - läuft
    // das Dashboard als alarm.example.org, zählt dazu alles auf example.org
    // und dessen Unterseiten, und die haben hier nichts abzuschicken.
    $fetchSite = strtolower(trim((string)($_SERVER['HTTP_SEC_FETCH_SITE'] ?? '')));
    $crossSite = $fetchSite !== '' && $fetchSite !== 'same-origin' && $fetchSite !== 'none';

    // Versuch VOR der Passwortprüfung atomar zählen (Prüfen + Zählen in einem
    // Schritt): Parallele Requests bekommen so nicht mehr Versuche als erlaubt.
    // Ein Erfolg setzt den Zähler unten wieder zurück.
    $attempt = $crossSite ? ['blocked' => false, 'count' => 0] : login_throttle_attempt($config);
    if ($crossSite) {
        http_response_code(403);
        $error = 'Anmeldung nur direkt über diese Seite möglich.';
    } elseif ($attempt['blocked']) {
        http_response_code(429);
        $error = 'Zu viele Fehlversuche von dieser Adresse – bitte später erneut versuchen.';
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
            // Unbekannter Benutzer: trotzdem gegen den ECHTEN Admin-Hash prüfen
            // und das Ergebnis verwerfen. Hält die Antwortzeit gleich, auch wenn
            // der Hash mit anderem Aufwand (cost, Argon2) erzeugt wurde - ein
            // fester Wegwerf-Hash verriete dann, ob der Benutzername existiert.
            // Die Rolle bleibt leer: Ein Treffer hier meldet niemanden an.
            password_verify($pass, (string)$config['dashboard_password_hash']);
        }
        if ($role !== '') {
            login_throttle_clear($config);
            // Erst JETZT eine Sitzung öffnen - und ihr sofort eine frische ID
            // geben (eine mitgeschickte alte ID wird dabei verworfen).
            start_dashboard_session();
            session_regenerate_id(true);
            $_SESSION = [];
            refresh_dashboard_cookie();
            $_SESSION['authed'] = true;
            $_SESSION['user'] = $user;
            $_SESSION['role'] = $role;
            $_SESSION['last_seen'] = time();
            // Bindet die Sitzung an die Zugangsdaten: Ein geändertes Passwort
            // meldet sie ab (siehe dashboard_auth_stamp()).
            $_SESSION['auth_stamp'] = dashboard_auth_stamp($config, $role, $user);
            ensure_csrf();
            // Optionale Admin-Meldung "Anmeldung am Dashboard" (Standard: aus).
            // Gesendet wird erst NACH der Antwort - und die Sitzung wird vorher
            // freigegeben, sonst wartete die Weiterleitung auf den Bark-Versand.
            $loginNotify = load_notify_settings($config)['types']['login_success'];
            if (!empty($loginNotify['enabled'])
                && ((string)$loginNotify['who'] === 'all' || (string)$loginNotify['who'] === $role)) {
                $loginIp = login_client_ip();
                session_write_close();
                defer_after_response(static function () use ($config, $role, $user, $loginIp): void {
                    admin_notify_send($config, 'login_success', 'Dashboard: Anmeldung',
                        date('[d.m.Y H:i] ') . ($role === 'admin' ? 'Admin' : 'Lese-Benutzer')
                        . " '" . substr($user, 0, 40) . "' hat sich angemeldet (von {$loginIp}).");
                });
            }
            header('Location: index.php');
            exit;
        }
        $failCount = (int)$attempt['count'];
        // Genau beim Erreichen der Sperrschwelle EINMAL den Owner leise per
        // Bark informieren (nur wenn bark_key_status in config.php gesetzt
        // ist): Bei einer internetoffenen Seite will man wissen, wenn jemand
        // am Login rüttelt. Weitere Versuche derselben IP landen im
        // 429-Zweig oben und lösen keine weitere Meldung aus.
        [$maxFailures, $lockoutWindow] = login_throttle_limits($config);
        $statusKey = trim((string)($config['bark_key_status'] ?? ''));
        // Ob und wie dringend, stellt der Admin im Dashboard ein (Panel
        // "Meldungen an den Admin", Art login_lockout).
        // Gesendet wird erst NACH der Antwort (Bark kann bis zu ~20 s brauchen,
        // ggf. an zwei Empfänger); Text und Adresse jetzt festhalten.
        if ($failCount === $maxFailures && $statusKey !== '') {
            $lockoutText = date('[d.m.Y H:i] ') . 'Zu viele fehlgeschlagene Login-Versuche von '
                . login_client_ip() . ' - IP fuer ' . (int)($lockoutWindow / 60) . ' min gesperrt.';
            defer_after_response(static function () use ($config, $lockoutText): void {
                // Sitzung vorher freigeben: Sonst hielte der Versand die
                // Sitzungssperre, und der nächste Seitenaufruf dieses Browsers
                // wartete auf Bark.
                if (session_status() === PHP_SESSION_ACTIVE) {
                    session_write_close();
                }
                admin_notify_send($config, 'login_lockout', 'Dashboard: Login gesperrt', $lockoutText);
            });
        }
        sleep(1);   // einfache Brute-Force-Bremse (DSM Auto-Block greift hier nicht)
        $error = 'Login fehlgeschlagen.';
    }
}

$authed = resume_dashboard_session();
$csrf = $authed ? ensure_csrf() : '';

// Ausgabe: Angemeldet die App-Hülle der Oberfläche, sonst die Anmeldeseite
// (beides in lib/shell.php). Die bisherige Oberfläche liegt vorübergehend
// als classic.php daneben.
if ($authed) {
    $role = dashboard_role();
    $user = (string)($_SESSION['user'] ?? '');
    // Sitzung gleich freigeben: Die Seite liest nur noch, und die ersten
    // API-Abfragen derselben Sitzung sollen nicht auf sie warten.
    session_write_close();
    render_app_page($config, $csrf, $role, $user);
} else {
    render_login_page($error, $loginUser);
}
