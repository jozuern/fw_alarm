<?php
// VORÜBERGEHENDER RÜCKFALL: die bisherige Oberfläche des Dashboards,
// eingefroren (assets/classic/app.js + style.css, Stand vor der Umstellung).
// Erreichbar über Einstellungen → Konto → „Alte Ansicht öffnen“ (bzw. die
// Ersatzanzeigen der neuen Oberfläche), falls in der neuen (index.php) etwas
// klemmt; zurück über „Neue Ansicht“ oben rechts. Bleibt nur einige Wochen und
// wird dann in einer eigenen Aufgabe entfernt - hier nichts mehr ausbauen.
// Anmelden und Abmelden laufen über index.php.
require __DIR__ . '/lib/common.php';

$config = load_config();
start_dashboard_session();

if (!is_dashboard_authed()) {
    header('Location: index.php');
    exit;
}

$csrf = ensure_csrf();
$readonly = dashboard_role() !== 'admin';
$sessionUser = (string)($_SESSION['user'] ?? '');
// Sitzung gleich freigeben: Die Seite liest nur noch, und die ersten
// API-Abfragen derselben Sitzung sollen nicht auf sie warten.
session_write_close();
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
  <title>BOSS-925 Alarm-Wächter (alte Ansicht)</title>
  <?php // Passend zum dunklen "Leitstand"-Design färbt sich auch die
        // Browser-Leiste (Safari/Chrome auf dem Handy) dunkel ein. ?>
  <meta name="theme-color" content="#14181d">
  <link rel="icon" href="assets/favicon.svg" type="image/svg+xml">
  <?php // Für "Zum Home-Bildschirm" auf dem iPhone (iOS ignoriert SVG-Favicons). ?>
  <link rel="apple-touch-icon" href="assets/apple-touch-icon.png">
  <?php // Dateizeit als Versions-Parameter: Browser laden nach jedem Update
        // frische Dateien, statt tagelang eine alte app.js aus dem Cache zu nutzen. ?>
  <link rel="stylesheet" href="assets/classic/style.css?v=<?= (int)@filemtime(__DIR__ . '/assets/classic/style.css') ?>">
</head>
<body>
<noscript><div class="noscript">Dieses Dashboard braucht JavaScript – ohne läuft die Live-Anzeige nicht.</div></noscript>
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
        <span class="chip-readonly" title="Angemeldet als <?= htmlspecialchars($sessionUser, ENT_QUOTES, 'UTF-8') ?>">Nur Lesezugriff</span>
      <?php else: ?>
        <a class="chip-demo" href="#demo" data-demo-chip hidden>Demo-Modus</a>
      <?php endif; ?>
      <?php // Abmelden läuft über index.php (Login/Logout wohnen dort). ?>
      <form method="post" action="index.php" class="logout-form">
        <input type="hidden" name="action" value="logout">
        <input type="hidden" name="csrf" value="<?= htmlspecialchars($csrf, ENT_QUOTES, 'UTF-8') ?>">
        <button type="submit" class="ghost-button">Abmelden</button>
      </form>
      <?php // Zurück zur neuen Oberfläche. Als GET-Formular, damit der Knopf
            // genau wie „Abmelden“ aussieht (ghost-button gilt nur für
            // <button>) - ohne neue Styles in der eingefrorenen Kopie. ?>
      <form method="get" action="index.php">
        <button type="submit" class="ghost-button">Neue Ansicht</button>
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
          nur mit der eingestellten <strong>Stumm-Lautstärke</strong> (0&nbsp;=&nbsp;lautlos).<?php
          // Wert aus config.php (mute_max_seconds, 0 = keine Rückschaltung) -
          // nicht fest "12 Std".
          $muteMax = mute_max_seconds($config);
          if ($muteMax > 0): ?>
          Nach <?= htmlspecialchars(str_replace(' ', "\u{00A0}", notify_duration_label($muteMax)), ENT_QUOTES, 'UTF-8') ?>
          schaltet das NAS automatisch wieder laut.<?php else: ?>
          Eine automatische Rückschaltung ist abgeschaltet (mute_max_seconds = 0).<?php endif; ?></p>
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
        <details class="notify-wrap">
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
  <script src="assets/classic/app.js?v=<?= (int)@filemtime(__DIR__ . '/assets/classic/app.js') ?>"></script>
</body>
</html>
