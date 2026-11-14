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
    $crossSite = (string)($_SERVER['HTTP_SEC_FETCH_SITE'] ?? '') === 'cross-site';

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
  <?php // Öffentliche Basis-URL für die Kurzbefehl-Links (config.php
        // 'public_base_url'). Leer = app.js nimmt die aktuelle Adresse - wer das
        // Dashboard über die LAN-IP öffnet, bekäme sonst Links, die unterwegs
        // nicht funktionieren. ?>
  <meta name="public-base" content="<?= htmlspecialchars((string)($config['public_base_url'] ?? ''), ENT_QUOTES, 'UTF-8') ?>">
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
        <input id="user" name="user" required autocomplete="username" autocapitalize="none" autocorrect="off" spellcheck="false"
               value="<?= htmlspecialchars($loginUser, ENT_QUOTES, 'UTF-8') ?>"<?= $loginUser === '' ? ' autofocus' : '' ?>>
        <label for="password">Passwort</label>
        <?php // Steht der Benutzername schon da (Fehlversuch), springt der
              // Cursor gleich ins Passwortfeld. ?>
        <input id="password" name="password" type="password" required autocomplete="current-password"<?= $loginUser !== '' ? ' autofocus' : '' ?>>
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
      <?php // Demo-Chip: bleibt beim Scrollen sichtbar. Das gelbe Banner steht
            // nur ganz oben - ohne den Chip sähe weiter unten nichts mehr nach
            // Demo-Modus aus. Für den Admin ein Sprung zum Demo-Schalter. ?>
      <?php if ($readonly): ?>
        <span class="chip-demo" data-demo-chip hidden>Demo-Modus</span>
        <span class="chip-readonly" title="Angemeldet als <?= htmlspecialchars((string)($_SESSION['user'] ?? ''), ENT_QUOTES, 'UTF-8') ?>">Nur Lesezugriff</span>
      <?php else: ?>
        <a class="chip-demo" href="#demo" data-demo-chip hidden>Demo-Modus</a>
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
          // gehen - inklusive Hinweis, ob die Box die Liste schon übernommen hat.
          // role="status": Screenreader sagen das Erscheinen des Banners an. ?>
    <div class="demo-banner" data-demo-banner role="status" hidden></div>

    <?php // Held-Bereich: EINE große Statuslampe beantwortet die wichtigste
          // Frage - ist die Alarmkette einsatzbereit? Darunter ein Satz, was der
          // Zustand bedeutet bzw. was zu tun ist. Rechts die zwei Fakten, die
          // man sonst zusammensuchen müsste (wer wird alarmiert, wann war der
          // letzte Alarm); darunter Hinweise auf alles, was gerade auffällig
          // ist. aria-live: Screenreader sagen Zustandswechsel von selbst an. ?>
    <section class="panel hero" aria-label="Gesamtstatus">
      <div class="hero-main">
        <div class="hero-status">
          <span data-state class="state state-boot" aria-live="polite">Verbinde…</span>
          <p class="hero-sub" data-state-sub aria-live="polite" hidden></p>
          <p class="hero-updated" data-updated>Letzter Status wird geladen…</p>
        </div>
        <dl class="hero-facts" data-hero-facts></dl>
      </div>
      <ul class="hero-hints" data-hero-hints hidden></ul>
      <?php if ($readonly): ?>
        <?php // Der Admin sieht einen laufenden Befehl direkt bei den Knöpfen
              // (Panel darunter); die Lese-Rolle hat das Panel nicht. ?>
        <p class="cmd-line" data-command hidden></p>
      <?php endif; ?>
    </section>

    <?php if (!$readonly): ?>
      <?php // Auslösen steht DIREKT unter dem Status: Die manuelle Alarmierung
            // braucht man gerade dann, wenn oben "Box offline" steht - früher
            // lag sie mehrere Bildschirmhöhen tiefer unter der Empfängerliste. ?>
      <section class="panel" id="aktionen">
        <h2 class="panel-title">Auslösen &amp; Testen</h2>
        <div class="actions">
          <button type="button" data-command-test>TEST</button>
          <button type="button" data-command-false-alarm>ENTWARNUNG (Fehlalarm)</button>
        </div>
        <?php // Rot-weißes Absperrband: hier drin haben Klicks echte Folgen. ?>
        <div class="hazard-strip">
          <div class="actions">
            <button type="button" class="danger" data-command-alarm>REAL ALARM</button>
            <button type="button" class="danger" data-command-esp-alarm hidden>DEMO-ALARM über die Box</button>
          </div>
        </div>
        <?php // Rückmeldungen stehen direkt unter den Knöpfen, die sie auslösen
              // (früher oben im Held-Bereich - auf dem Handy außer Sicht).
              // aria-live: Screenreader lesen das Ergebnis einer Aktion vor. ?>
        <p class="cmd-line" data-command hidden></p>
        <button type="button" data-command-cancel hidden>Befehl abbrechen</button>
        <p class="msg" data-command-msg aria-live="polite"></p>
        <div class="result" data-alarm-result aria-live="polite" hidden></div>
        <details class="explain">
          <summary>Was macht welcher Knopf?</summary>
          <ul>
            <li><strong>TEST</strong> läuft über die Box – leise Meldung nur an den Status-Empfänger.</li>
            <li><strong>ENTWARNUNG</strong> schickt allen einen <strong>Critical Alert</strong>
              („letzter Alarm war ein Fehlalarm“) – kurzer, normaler Ton statt Alarmton,
              kommt aber auch durch Stummschalter und Fokus durch.</li>
            <li><strong>REAL ALARM</strong> geht direkt vom NAS an alle – auch wenn die Box offline ist.
              Wird jemand nicht erreicht, lässt sich gezielt nur an ihn nachsenden.</li>
            <li><strong>DEMO-ALARM</strong> erscheint nur im Demo-Modus und löst den Alarm über die
              komplette Kette der Box aus (wie ein echter Relaisalarm, nur an den Test-Empfänger).</li>
          </ul>
        </details>

        <?php // Der Demo-Modus wohnt beim Testen (er IST ein Test-Werkzeug),
              // ist aber eingeklappt, solange er aus ist - man braucht ihn
              // selten. Aktiv klappt ihn app.js auf; Banner oben und Chip in
              // der Kopfzeile zeigen ihn dann zusätzlich an. ?>
        <details class="sub-block demo-block" id="demo" data-demo-details>
          <summary><span class="sub-title">Demo-Modus</span><span class="demo-summary" data-demo-summary>wird geladen…</span></summary>
          <p data-demo-state>Wird geladen…</p>
          <div class="actions spaced">
            <select data-demo-target aria-label="Demo-Empfänger"></select>
            <button type="button" data-demo-toggle disabled>Wird geladen…</button>
          </div>
          <p class="msg" data-demo-msg aria-live="polite"></p>
          <p class="hint">Im <strong>Demo-Modus</strong> gehen alle Alarme nur an den gewählten
            Test-Empfänger. Die Box übernimmt den Wechsel erst nach einigen Sekunden (das Banner
            oben zeigt den Stand). Nach dem Testen zurück auf <strong>LIVE</strong> schalten!</p>
        </details>
      </section>
    <?php endif; ?>

    <section class="panel" id="status">
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

    <section class="panel" id="empfaenger">
      <h2 class="panel-title">Alarm-Empfänger</h2>
      <p class="hint">Gilt für <strong>beide</strong> Alarmwege (Box und Dashboard). Änderungen übernimmt die Box nach wenigen Sekunden.</p>
      <div data-keys-list class="keys-list">Wird geladen…</div>
      <?php if (!$readonly): ?>
        <form data-keys-add class="keys-add">
          <input name="label" placeholder="Name (z. B. Josh)" aria-label="Name des Empfängers" required maxlength="40">
          <?php // title erklärt bei ungültiger Eingabe das erwartete Format
                // (der Browser hängt ihn an seine Validierungs-Meldung an);
                // autocapitalize/spellcheck aus, weil iPhones den Key sonst
                // beim Eintippen groß schreiben oder "korrigieren".
                // Der Bindestrich im Muster MUSS maskiert sein (\-): Aktuelle
                // Browser werten pattern mit dem RegExp-Flag "v" aus, dort ist
                // ein nacktes "-" in einer Zeichenklasse ein Syntaxfehler - das
                // ganze Muster wurde dann still ignoriert (keine Prüfung). ?>
          <input name="key" placeholder="Bark-Key" aria-label="Bark-Key des Empfängers" required pattern="[A-Za-z0-9_\-]{5,64}"
                 title="5–64 Zeichen: Buchstaben, Ziffern, Unter- und Bindestrich (steht in der Bark-App unter dem QR-Code)"
                 autocomplete="off" autocapitalize="none" spellcheck="false">
          <button type="submit">Hinzufügen</button>
        </form>
      <?php endif; ?>
      <?php // Rückmeldung zur Liste insgesamt (Hinzufügen, Löschen). Aktionen an
            // EINEM Empfänger melden sich direkt in dessen Zeile (app.js). ?>
      <p class="msg" data-keys-msg aria-live="polite"></p>
      <details class="explain">
        <summary>Was bedeuten „Stumm“ und „Dauerhaft stumm“?</summary>
        <p><strong>Stumm (Arbeitsmodus):</strong> Alarme kommen weiterhin als Critical Alert an –
          nur mit der eingestellten <strong>Stumm-Lautstärke</strong> (0&nbsp;=&nbsp;lautlos).
          Nach 12&nbsp;Std schaltet das NAS automatisch wieder laut.</p>
        <p><strong>Dauerhaft stumm</strong> schaltet genau diese automatische Rückschaltung
          für einen Empfänger ab – für längere Abwesenheiten (Schichtdienst, Krankheit, Urlaub).
          Die Stummschaltung gilt dann, bis sie jemand von Hand beendet. Der Alarm bleibt auch
          dann <strong>sichtbar</strong> auf dem Sperrbildschirm – niemand fällt aus der
          Alarmierung heraus. Der Wächter erinnert regelmäßig daran, wer dauerhaft stumm ist.</p>
        <?php if (!$readonly): ?>
          <p>Unter <strong>„Mehr“</strong> an jedem Empfänger: die Stumm-Lautstärke, die persönlichen
            <strong>Kurzbefehl-Links</strong> (für iPhone-Kurzbefehle, z.&nbsp;B. automatisch beim
            Betreten oder Verlassen der Arbeit – der Laut-Link beendet auch „dauerhaft stumm“) und der
            <strong>Test-Push</strong>: eine normale, nicht kritische Mitteilung mit dem Alarmton, um
            einen neuen Key und den Ton auf dem iPhone zu prüfen.</p>
        <?php endif; ?>
      </details>
    </section>

    <?php if (!$readonly): ?>
      <?php // Welche Statusmeldungen das NAS dem Admin schickt und wie. Die
            // Liste samt aller Auswahlfelder baut app.js aus der Antwort von
            // notify_list - hier steht nur der Rahmen. ?>
      <section class="panel" id="meldungen">
        <h2 class="panel-title">Meldungen an den Admin</h2>
        <p class="hint">Statusmeldungen, die das NAS an deinen Status-Empfänger schickt. Die Alarmierung
          selbst ist davon unabhängig.</p>
        <p class="stale-note" data-notify-note hidden></p>
        <?php // Eingeklappt: Einstellungen braucht man selten, und die Liste
              // ist lang - offen schöbe sie den Verlauf weit nach unten. Die
              // Zusammenfassung (app.js) sagt, wie viele Arten an sind. ?>
        <details class="notify-wrap" data-notify-details>
          <summary data-notify-summary>Einstellungen anzeigen</summary>
          <div class="notify-list" data-notify-list>Wird geladen…</div>
          <h3 class="sub-title notify-global-title">Für alle Meldungen</h3>
          <div class="notify-global" data-notify-global></div>
          <p class="msg" data-notify-msg aria-live="polite"></p>
        </details>
        <details class="explain">
          <summary>Dringlichkeit, Ruhezeit und was sich hier nicht einstellen lässt</summary>
          <p><strong>Dringlichkeit:</strong> „leise“ landet nur in der Mitteilungszentrale, „normal“
            kommt mit Ton, „dringend“ durchbricht Fokus-Modi, „kritisch“ auch den Stummschalter
            (mit der unten gewählten Lautstärke).</p>
          <p><strong>Ruhezeit:</strong> In dieser Zeit kommen alle Meldungen nur leise an – außer
            denen, bei denen „wie eingestellt (auch nachts)“ gewählt ist.</p>
          <p><strong>„Probe senden“</strong> schickt eine Beispiel-Meldung genau so, wie sie mit den
            aktuellen Einstellungen ankäme.</p>
          <p><strong>Nicht einstellbar</strong> sind die Meldungen, die die Box selbst verschickt:
            der tägliche Heartbeat, die Start-Meldung, „Kontakt klemmt?“, die Warnung bei nicht
            zustellbarem Alarm, die Meldung über eine übernommene Empfängerliste und die Antwort
            auf TEST. Sie kommen aus der Firmware der Box.</p>
        </details>
      </section>
    <?php endif; ?>

    <section class="panel" id="verlauf">
      <h2 class="panel-title">Verlauf</h2>
      <?php // Filter: Die täglichen Stumm-Wechsel der Kurzbefehle verdrängen
            // sonst die Alarme aus dem sichtbaren Fenster. ?>
      <div class="log-filter" role="group" aria-label="Verlauf filtern">
        <button type="button" class="chip-filter" data-log-filter="all" aria-pressed="true">Alles</button>
        <button type="button" class="chip-filter" data-log-filter="alarm" aria-pressed="false">Alarme</button>
        <button type="button" class="chip-filter" data-log-filter="keys" aria-pressed="false">Empfänger</button>
        <button type="button" class="chip-filter" data-log-filter="system" aria-pressed="false">System</button>
      </div>
      <?php // app.js füllt hier pro Tag eine Überschrift und eine echte Liste
            // (ol): Screenreader sagen so "Liste, N Einträge" an. ?>
      <div data-log class="log"><p class="log-plain">Wird geladen…</p></div>
      <?php // Erscheint nur, wenn es voraussichtlich noch ältere Einträge gibt
            // (app.js blendet ihn ein); lädt in 25er-Schritten bis maximal 100. ?>
      <p class="log-more"><button type="button" class="ghost-button" data-log-more hidden>Ältere Ereignisse anzeigen</button></p>
      <details class="explain">
        <summary>Was landet im Verlauf?</summary>
        <p>Die Ereignisse aus dem NAS-Protokoll: Alarme, Tests, Demo-Wechsel, Änderungen an der
          Empfängerliste und Ausfälle der Box. Anders als „Letzter Alarm“ im Status übersteht
          dieser Verlauf auch einen Neustart der Box.</p>
      </details>
    </section>
  </main>
  <script src="assets/app.js?v=<?= (int)@filemtime(__DIR__ . '/assets/app.js') ?>"></script>
<?php endif; ?>
</body>
</html>
