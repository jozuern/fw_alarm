# NAS-Dashboard: Neubau des Frontends — Design

Stand: 2026-11-15 · Status: Entwurf zur Freigabe

## 1. Absicht und Warum

Das NAS-Dashboard (`nas_dashboard/`) bekommt ein komplett neues Frontend:
neue Optik, neue Struktur, neue Bedienung. Es soll sich wie eine gut gemachte,
moderne App anfühlen — schlicht, schnell, eindeutig — und **nicht** nach
generiertem Einheits-Look aussehen. Das bisherige Design-System („Leitstand“)
muss nicht erhalten bleiben. Neue Funktionen und Einstellungen sind
ausdrücklich erwünscht, wo sie sich lohnen.

Wer es nutzt: **nur der Admin** (eine Person), auf dem **iPhone** als
Webseiten-Kurzbefehl auf dem Home-Bildschirm und am **PC**. Typische Gründe,
es zu öffnen:

- kurz prüfen, ob alles gesund ist und funktioniert,
- den Verlauf ansehen,
- Empfänger hinzufügen,
- die Stumm-Lautstärke einzelner Personen ändern,
- die eigenen Admin-Meldungen einstellen.

Dazu der seltene Notfall: manuell alarmieren, wenn die Box offline ist
(Vorgabe aus CLAUDE.md, bleibt einen Tipp vom Start entfernt).

Der Lese-Benutzer (optional, `dashboard_readonly_user`) wird weiter
unterstützt, aber nicht eigens gestaltet.

## 2. Ziele, Nicht-Ziele, Anti-Ziele

**Ziele**
- Ein Blick auf den Start beantwortet „Ist alles gesund?“ — ein Urteil in
  Worten plus eine kurze Prüfliste, Details erst auf Antippen.
- Jede der Routine-Aufgaben ist höchstens zwei Tipps entfernt.
- Alles Auffällige springt ins Auge: **rot** = offline/kaputt/jetzt handeln,
  **orange** = unregelmäßig/Aufmerksamkeit (Wunsch des Nutzers).
- Fühlt sich auf dem iPhone wie eine native App an (dunkel, Systemschrift,
  gruppierte Listen, Sheets, Tab-Leiste), am PC wie eine aufgeräumte
  Einstellungs-App (Seitenleiste, zweispaltig).
- Alle Sicherheits-Eigenschaften des heutigen Frontends bleiben erhalten
  (Abschnitt 7).

**Nicht-Ziele**
- Keine Änderung an der Firmware (`boss925_alarm_bark/`, `platformio.ini`).
- Kein Umbau der Alarm-Logik auf dem NAS; Backend-Änderungen nur additiv für
  die neuen Funktionen (Abschnitt 8).
- Keine Push-Benachrichtigungen aufs iPhone über das Dashboard (dafür gibt es
  Bark), kein Service Worker.
- Keine Mehrbenutzer-/Rechteverwaltung.

**Anti-Ziele (wäre ein Fehlschlag, selbst wenn es „funktioniert“)**
- Ein Alarm, eine Entwarnung oder eine Nachsendung kann versehentlich
  ausgelöst werden — oder ist im Notfall schwer zu finden.
- Veraltete Daten sehen aus wie aktuelle (z. B. grün, obwohl das NAS nicht
  antwortet).
- Die 5-s-Aktualisierung stört die Bedienung (schließt Sheets, setzt einen
  Regler zurück, klaut den Fokus).
- Typischer „vibe-coded“ Look: Verläufe, Glas-Effekte, Lila, Emoji-Icons,
  Inter-Schrift, überall Karten mit Schatten.
- Die Seite lädt nach einem Update alte Skripte aus dem Cache.

## 3. Rahmenbedingungen

- **CSP** (zentral in `lib/common.php`): `default-src 'self'`, kein
  `unsafe-inline`, kein `unsafe-eval`. Also: keine CDN-Skripte/-Schriften, keine
  `<script>`-Inhalte im HTML, keine `style=`-Attribute im ausgelieferten HTML.
  (Setzen von `element.style` per JS/CSSOM ist erlaubt.)
- **Kein Build-Schritt auf dem NAS**: Web Station liefert statische Dateien und
  PHP 8.3 aus. Was im Repo liegt, wird so ausgeliefert.
- **Cache**: Jede Asset-Datei bekommt `?v=filemtime()` (wie heute).
- **Backend-API bleibt**: alle bestehenden Aktionen in `dashboard_api.php` mit
  unveränderten Antworten. Die Zugangsregeln stehen in `DASHBOARD_ACTIONS`;
  jede neue Aktion braucht dort einen Eintrag.
- **Sprache**: alle sichtbaren Texte Deutsch mit echten Umlauten, „die Box“
  statt „ESP32“. Bark-Texte (falls neue entstehen) bleiben ASCII.
- **Die Box im Betrieb hat evtl. noch ältere Firmware** (siehe Memory „Firmware
  on main not flashed“): Das Frontend muss mit und ohne die neueren
  Status-Felder (`alarm_seq`, `alarm_source`, `core`, `max_alloc_heap`,
  `alarm_gaveup`) korrekt anzeigen — wie heute.
- **Browser**: aktuelles iOS-Safari (Home-Bildschirm-Modus) und aktuelle
  Desktop-Browser (Chrome/Edge/Firefox). Kein IE, kein altes Android.

## 4. Lösungswege (Approaches)

| Weg | Beschreibung | Abwägung |
|---|---|---|
| A | Reines JavaScript ohne Bibliothek, in mehrere Dateien aufgeteilt | Keine Abhängigkeit. Aber der heutige Code kämpft schon mit dem Kernproblem (Neuzeichnen alle 5 s zerstört Fokus, offene Bestätigungen, halbe Eingaben — `setHtml`/`data-fkey`/`deferWhileArmed`/`forceRedraw`); die neue Oberfläche mit Tabs, Sheets, Reglern und Navigation vervielfacht das. |
| **B** | **Reines JavaScript + Preact/htm als eine kleine, im Repo abgelegte Datei** | Komponenten und Diffing lösen das Neuzeichnen-Problem strukturell (Elemente und ihr Zustand bleiben erhalten). Kein Build, CSP bleibt `'self'`, `htm` braucht kein `eval`. Kosten: eine Fremd-Datei (MIT, stabil). |
| C | Vite + Svelte/TypeScript, auf dem PC gebaut, Ergebnis auf das NAS kopiert | Beste Werkzeuge, aber Node/npm im Repo, gebaute Dateien in Git, Build-Schritt bei jedem Deploy — zu viel für ein Ein-Personen-Werkzeug. |

**Gewählt: B.** Der Nutzer hat das Wie freigegeben („your decision“). B
entfernt die fehleranfälligste Mechanik des heutigen Codes, ohne einen
Build-Schritt einzuführen.

Zusätzlich (Teil der Entscheidung): Das heutige Dashboard bleibt einige Wochen
als **Rückfall-Ansicht unter `classic.php`** erreichbar (Abschnitt 9.4).

## 5. Optik

### 5.1 Grundhaltung
„Native“-Richtung, **dunkel als Standard**: Sie orientiert sich an iOS
(gruppierte Listen, große Titel, Sheets, Segment-Schalter, Schalter, Regler),
ohne iOS nachzubauen. Charakter kommt aus Zurückhaltung und Präzision, nicht
aus Effekten: echtes Schwarz, eine Akzentfarbe (Feuerwehrrot), Farbe nur mit
Bedeutung, tabellarische Ziffern für alle Zeiten und Zahlen, klare
Sprache statt Fachbegriffen.

Referenz: die Mockups aus der Abstimmung (Dateien `native-dark.html`,
`settings-features.html`, `pc-layout.html` im Visual-Companion-Ordner der
Sitzung; der Builder kennt sie nicht — diese Beschreibung ist maßgeblich).

### 5.2 Farben (Tokens)
Dunkel (Standard):

| Token | Wert | Verwendung |
|---|---|---|
| `--bg` | `#000000` | Seitenhintergrund |
| `--surface` | `#1c1c1e` | gruppierte Listen, Karten |
| `--surface-2` | `#2c2c2e` | Segment-Hintergrund, verschachtelte Gruppen |
| `--surface-3` | `#3a3a3c` | aktives Segment, Hover |
| `--separator` | `#38383a` | Haarlinien (0,5 px, eingerückt) |
| `--text` | `#ffffff` | Text |
| `--text-2` | `#98989f` | Sekundärtext, Werte |
| `--text-3` | `#5a5a5e` | Chevrons, unbekannte Werte |
| `--accent` | `#ff453a` | Akzent (aktiver Tab, Links, Zurück) |
| `--alarm` | `#ff3b30` | Alarm-Knopf (Fläche) |
| `--ok` | `#30d158` | grün = in Ordnung |
| `--warn` | `#ff9f0a` | orange = unregelmäßig |
| `--crit` | `#ff6961` | rot (Text) = offline/kaputt |
| `--info` | `#0a84ff` | „Abbrechen“ in Sheets, Fokus |

Hell (Option „Hell“ bzw. „System“ bei hellem Gerät): die entsprechenden
iOS-Hellwerte (`#f2f2f7` Hintergrund, `#ffffff` Flächen, `#c6c6c8` Linien,
`#000`/`#6d6d72` Text, `#d70015` Akzent, `#34c759`/`#ff9500`/`#d70015`
Status). Beide Varianten müssen WCAG AA für Text erfüllen.

### 5.3 Farb-Regel für Zustände (verbindlich, überall gleich)
- **Rot** (`--crit`): offline, kaputt, nicht zugestellt, jetzt handeln.
  Beispiele: Box offline, Zustellung unvollständig, Wächter kann nicht melden,
  Empfängerliste beschädigt.
- **Orange** (`--warn`): unregelmäßig, Aufmerksamkeit. Beispiele: schwaches
  WLAN, Heartbeat fehlgeschlagen, Außen-Wächter still, Box hat Liste noch nicht
  übernommen, Demo-Modus, jemand stumm (als Status-Pille), Neustart durch
  Watchdog/Absturz/Brownout, wenig Speicher.
- **Grau** (`--text-3`): Wert ist gerade unbekannt (Box offline, NAS antwortet
  nicht) — veraltete Werte sehen nie normal aus.
- In Listenzeilen werden **Bezeichnung und Wert** eingefärbt.
- Farbe ist nie das einzige Signal: Status-Kreis mit Symbol (✓ / ! / ✕),
  Text sagt den Zustand in Worten.

### 5.4 Schrift, Raster, Form
- Nur Systemschrift: `-apple-system, "SF Pro Text", "Segoe UI Variable Text",
  "Segoe UI", system-ui, sans-serif`; Monospace nur für maskierte Keys.
  Keine Webfonts.
- Größen: großer Titel 30–34 px fett; Abschnitts-Kopf 12 px Versalien grau;
  Listenzeilen 15–16 px (iPhone) / 14 px (PC); Eingabefelder mind. 16 px
  (iOS zoomt sonst).
- Radien: Gruppen 12–13 px, Knöpfe 12 px, Pillen rund. Abstände in einem
  4-px-Raster. Tippflächen mind. 44 px.
- Icons: eigene Inline-SVG-Strichsymbole (1,7 px), keine Emoji, keine
  Icon-Schrift.
- Bewegung: Sheets gleiten (≈ 250 ms), Seitenwechsel ohne Animation oder sehr
  kurz; „Alarm läuft“ pulsiert ruhig. `prefers-reduced-motion` schaltet alles
  ab.

## 6. Struktur und Bildschirme

### 6.1 Navigation
Vier Bereiche: **Übersicht · Empfänger · Verlauf · Einstellungen**.

- **iPhone / schmal (< 900 px)**: Tab-Leiste unten (mit `safe-area`),
  große Titel, Unterseiten mit „‹ Zurück“, Detail-Ansichten als Sheet von
  unten.
- **PC / breit (≥ 900 px)**: Seitenleiste links (Marke mit Status-Lämpchen,
  vier Bereiche, unten Benutzer · Abmelden · „Status vor N s“); Inhalte
  zweispaltig; Sheets werden zentrierte Dialoge; die Person-Details bei
  Empfänger stehen als Panel rechts neben der Liste.
- Adressen per Hash (`#/uebersicht`, `#/empfaenger`, `#/empfaenger/3`,
  `#/verlauf`, `#/einstellungen`, `#/einstellungen/meldungen`,
  `#/einstellungen/meldungen/box_offline`, `#/einstellungen/box`,
  `#/einstellungen/demo` …), damit Zurück-Geste/-Taste und Neuladen
  funktionieren. Start = Übersicht.
- Die Übersicht-Taste bzw. der Seitenleisten-Eintrag trägt ein **Badge**,
  sobald das Urteil nicht grün ist (rot/orange, Zahl = Anzahl Hinweise); der
  Tab-Titel bekommt wie heute ein Status-Symbol.
- Ist der Demo-Modus aktiv, steht auf **jeder** Seite oben ein schmales
  oranges Band („Demo-Modus: nur Anna · Box hat übernommen“ bzw. „… NOCH NICHT
  übernommen – ein Relaisalarm ginge noch an alle!“); Antippen führt zur
  Demo-Seite.

### 6.2 Übersicht
Von oben nach unten:

1. **Status-Karte**: Kreis-Symbol + Urteil in einem Wort/kurzen Satz + ein
   Satz, was das bedeutet bzw. was zu tun ist + „Status vor N s“. Rangfolge
   (wichtigster gewinnt; Texte sinngemäß wie heute):
   1. Abgemeldet (Sitzung abgelaufen)
   2. Box offline — rot (mit Hinweis „manuell alarmieren geht weiter“)
   3. NAS-Problem: „NAS nicht erreichbar“ / „NAS-Störung“ / „Status
      veraltet“ / „NAS beschäftigt“ — orange; **rot, wenn der letzte bekannte
      Stand „Box offline“ war** (Rot bleibt rot)
   4. Alarm läuft — rot, pulsierend, „bisher x/y erreicht“
   5. Kontakt klemmt — orange
   6. Alarm ausgelöst (Zustellung durch, Kontakt noch geschlossen) — rot,
      ruhig; nennt unvollständige Zustellung
   7. Demo-Modus — orange
   8. **NEU**: „Bereit – N Hinweis(e)“, wenn die Kette arbeitet, aber
      Hinweise anliegen (Liste der Hinweise direkt unter der Karte, je eine
      Zeile, eingefärbt). Die Farbe des Urteils (und des Badges) ist die
      **schlimmste** Hinweis-Farbe: ein roter Hinweis macht das Urteil rot.
   9. Alles bereit — grün

   **Was ein Hinweis ist** (abschließend; Texte sinngemäß wie heute
   `renderHeroExtras`/`ROWS.hint`) *(Entscheidung des Designers nach
   Builder-Check)*:
   - **rot**: letzte Zustellung der Box unvollständig (`deliveryIncomplete`);
     Empfängerliste auf dem NAS beschädigt (`keys_corrupt`).
   - **orange**: Offline-Wächter mit Problem (`watchdogState().warn`);
     Außen-Wächter mit Problem (`outsideState().warn`); WLAN ≤ −80 dBm;
     Heartbeat fehlgeschlagen; Neustart durch Watchdog/Absturz/Brownout (nur
     am ersten Tag); wenig Speicher; Box hat die Liste seit > 60 s nicht
     übernommen; **alle** Empfänger stumm; `device_hint`; Rückschaltung
     überfällig (`mute_overdue`).
   - **Kein Hinweis**: einzelne stumme oder dauerhaft stumme Empfänger
     (Alltag durch die Kurzbefehle — sie stehen in der Prüfzeile „Empfänger“
     und in der Liste), ein Alarm aus der letzten Stunde (steht rot in der
     Zeile „Letzter Alarm“).
   - Bei NAS-Störung werden keine Hinweise aus alten Daten abgeleitet (wie
     heute).

   **Position von „Auslösen“** *(Entscheidung des Designers)*: Normal steht
   die Prüfliste zwischen Status-Karte und Auslösen (wie im abgestimmten
   Mockup). Ist das Urteil **rot** (Box offline, NAS-Problem mit letztem Stand
   „offline“, Alarm läuft, Alarm ausgelöst, roter Hinweis), rückt die Gruppe
   „Auslösen“ direkt unter die Status-Karte — vor Hinweise und Prüfliste —,
   damit der Alarm-Knopf auch auf kleinen iPhones ohne Scrollen sichtbar ist.
2. **Prüfliste** (gruppierte Liste, Zeilen mit › führen zum Detail):
   - Box — „online · läuft seit 4 Tagen“ / „offline seit 23 min“ → Box-Details
   - WLAN — „gut · −61 dBm“ / „GETRENNT“ / „unbekannt“
   - Wächter — „NAS + Cloud aktiv“ bzw. das Problem des betroffenen Wächters
     (Cloud-Zeile entfällt, wenn nicht eingerichtet) → Box-Details
   - Empfänger — „6, 1 stumm“ / „nur Anna (Demo)“ / „Liste beschädigt“ →
     Empfänger
   - Letzter Alarm — wie heute „Letzter Alarm“ (Art, Zeit, Zustellung,
     Entwarnung) → Verlauf mit Filter „Alarme“
3. **Auslösen** (nur Admin): großer roter Knopf „Alarm an alle senden“
   (Demo: „Demo-Alarm an Anna“), darunter „Entwarnung“ und „Test“; im
   Demo-Modus zusätzlich „Demo-Alarm über die Box“. Darunter die
   **Befehls-Zeile** für TEST/DEMO-ALARM (Zustand, Quittung, „Abbrechen“ solange
   `pending`) — Logik wie heute (`renderCommand`).
4. **Ergebnis-Karte** nach Alarm/Entwarnung (Abschnitt 7.4), direkt unter
   Auslösen.
5. Nur PC (rechte Spalte): „Letzte Ereignisse“ (5 Einträge, Link „Ganzen
   Verlauf anzeigen“) und „Stumm geschaltet“ (wer, Lautstärke, bis wann).

Lese-Rolle: keine Auslösen-Gruppe; stattdessen der Satz, einen Admin zu
verständigen (wie heute).

### 6.3 Empfänger
- Großer Titel, rechts „+“ (PC: Knopf „+ Hinzufügen“).
- Zeile darunter: Abgleich mit der Box — grün „Box ist auf dem neuesten
  Stand“ bzw. orange „Box hat die Änderung noch nicht übernommen (seit …)“
  (Regeln wie heute: Versionsvergleich, Hinweis erst nach 60 s).
- Liste: je Person Name, darunter Unterzeile (maskierter Key bzw. „Stumm bis
  ~18:30 · Lautstärke 3“ / „dauerhaft · lautlos“ / Rückschaltung-überfällig-
  Warnung), rechts Status-Pille **Laut** (grün) / **Stumm** (orange) /
  **Dauerhaft** (rot) und ggf. „Demo-Empfänger“; im Demo-Modus sind alle
  anderen gedimmt mit Hinweis „pausiert“.
- Antippen öffnet die **Person** (iPhone: Sheet; PC: Panel rechts):
  - Kopf: Name, maskierter Key, Satz zum Zustand („Stumm seit 08:02 · wird um
    ~20:02 automatisch wieder laut“).
  - Segment-Schalter **Laut | Stumm | Dauerhaft stumm**. Regeln wie heute:
    lauter geht sofort; leiser (Stumm, Dauerhaft) mit Bestätigung, die die
    tatsächliche Stumm-Lautstärke nennt; Dauerhaft → Stumm ohne Rückfrage.
  - **Regler „Stumm-Lautstärke“ 0–10** (0 = lautlos), Wert groß daneben;
    gespeichert beim Loslassen (nicht während des Ziehens); gilt, sobald die
    Person stumm ist (bei „Laut“ sichtbar, aber mit Hinweis „gilt, wenn stumm“).
  - **NEU Telefon** (optional, nur Admin): Nummer eintragen/ändern; erscheint
    als „Anrufen“ (`tel:`) hier und im Alarm-Ergebnis.
  - Kurzbefehl-Links „Stumm“ / „Laut“ kopieren (wie heute: `public_base_url`,
    Fallback-Anzeige zum manuellen Kopieren).
  - Test-Push senden (mit Bestätigung, wie heute).
  - **NEU Umbenennen**, **NEU Key ersetzen** (behält id, Stumm-Zustand,
    Lautstärke, Kurzbefehl-Token, Telefon).
  - Empfänger löschen (rot, Bestätigung; letzter bzw. aktiver Demo-Empfänger
    serverseitig gesperrt — Meldung anzeigen).
  - Jede Änderung speichert sofort; Rückmeldung als kurzes Häkchen am
    Bedienelement bzw. Fehlerzeile im Sheet (Fehler bleiben stehen).
- **Hinzufügen** (Sheet/Dialog): Name, Bark-Key (Prüfung beim Tippen: 5–64
  Zeichen `A-Za-z0-9_-`, Hinweis wo der Key in der Bark-App steht), optional
  Telefon. Nach Erfolg: Angebot „Test-Push senden“ (mit derselben Bestätigung).
- Fußnote: kurzer Satz, was „Stumm“ bedeutet; „Mehr erfahren“ öffnet die
  längere Erklärung (Stumm vs. Dauerhaft, automatische Rückschaltung — Wert
  aus der Einstellung).
- Lese-Rolle: Liste ohne Bedienelemente, Person-Ansicht nur lesend, keine
  Tokens/Telefonnummern.

### 6.4 Verlauf
- Segment-Filter **Alle | Alarme | Empfänger | System** (`filter=` wie heute).
- **NEU Suche**: Feld über der Liste; filtert clientseitig die geladenen
  Einträge über den angezeigten Text (Hinweis „durchsucht die geladenen
  Einträge“); „Ältere laden“ in 25er-Schritten bis 100 wie heute.
- Pro Tag eine Abschnitts-Überschrift (Heute/Gestern/Wochentag + Datum), pro
  Eintrag: farbiges Symbol nach Art (rot Alarm, orange Stumm/Warnung, grün
  hinzugefügt/wieder da, grau System), Text, ggf. Unterzeile, Uhrzeit rechts.
- Texte aus der bestehenden `describeLogEntry`-Logik (vollständig übernehmen)
  plus Texte für die neuen Ereignisse (Abschnitt 8).

### 6.5 Einstellungen
Gruppierte Liste:
- **Meldungen an mich** (Admin) › „7 von 12 an“
  - Unterseite: Arten in zwei Gruppen **Sicherheit** (`important` = true)
    und **Info**; je Zeile Name, Dringlichkeits-Etikett (wenn nicht leise),
    Schalter. Abschalten einer wichtigen Art mit Bestätigung (wie heute).
    Hinweis, wenn `bark_key_status` fehlt.
  - Antippen einer Art: Detailseite mit Kurzbeschreibung (`hint`), allen
    Optionen aus `notify_list` (Auswahl je `choices`, Segment bei ≤ 4
    Möglichkeiten, sonst Auswahlliste) und „Probe senden“.
  - **Ruhezeit & Allgemein**: die globalen Optionen (`global` aus
    `notify_list`: Ruhezeit, Ton, Lautstärke kritischer Meldungen,
    Stellvertreter), „Gerade ist Ruhezeit“.
  - Erklärtext (Dringlichkeit, Ruhezeit, was nicht einstellbar ist) als
    Fußnote/„Mehr erfahren“.
- **Ruhezeit** › (Abkürzung auf die Ruhezeit-Option, zeigt „22–07 Uhr“)
- **Demo-Modus** › „aus“/„nur Anna“: Seite mit Zustand, Sync-Aussage (Regeln
  wie heute `renderDemo`), Auswahl des Test-Empfängers, Ein-/Ausschalter mit
  Bestätigung.
- **Empfänger → NEU „Automatisch wieder laut nach“** › 2/4/6/8/12/16/24 Std
  (Abschnitt 8.4).
- **Box → Details & Diagnose** ›: alle Status-Werte in den Gruppen
  Alarmkette / Verbindung / Wartung (Inhalte und Warnregeln = heutige `ROWS`
  inkl. Offline-Wächter und Außen-Wächter); jede Zeile antippbar → Erklärung
  (heutige `tip`-Texte) klappt darunter auf. Fußnote „Mittwochs leiser? Das
  Probealarm-Fenster“. Bei offline/NAS-Störung: gedimmt + Hinweis „letzter
  bekannter Stand von …“.
- **Dieses Gerät (NEU)**: Design **Dunkel | Hell | System** (Standard Dunkel);
  **„Hinweis im Browser bei Störung“** (Abschnitt 8.6) — nur angezeigt, wo
  der Browser lokale Benachrichtigungen kann.
- **Konto**: angemeldet als … (Rolle), Abmelden (POST + CSRF wie heute),
  „Alte Ansicht öffnen“ (Link auf `classic.php`, solange es sie gibt).
- Lese-Rolle sieht nur: Box-Details, Dieses Gerät, Konto.

### 6.6 Anmeldeseite
Neu gestaltet im selben Stil (zentrierte Karte, Marke mit Lämpchen, große
Felder, roter „Anmelden“-Knopf). PHP-Logik in `index.php` bleibt unverändert
(Throttle, Cross-Site-Prüfung, Rollen, Fehlermeldungen, Benutzername bleibt
nach Fehlversuch stehen, Fokus aufs Passwortfeld, `autocomplete`-Attribute,
`autocapitalize="none"`).

### 6.7 Home-Bildschirm-App
- `manifest.json` (Name „Alarm-Wächter“, `display: standalone`, Farben
  schwarz, Icons 180/192/512 px aus `favicon.svg` erzeugt) und
  `apple-mobile-web-app-capable` / `status-bar-style: black-translucent`,
  `theme-color` schwarz.
- **Anmeldung übersteht das Schließen der App** (Entscheidung des Nutzers):
  iOS verwirft im App-Modus Sitzungs-Cookies, sobald die App ganz geschlossen
  wird. Deshalb wird das Sitzungs-Cookie in `start_dashboard_session()` ein
  **dauerhaftes Cookie mit 12 h Laufzeit** (= heutiges Leerlauf-Limit
  `session.gc_maxlifetime`), das bei jeder angemeldeten Anfrage erneuert wird.
  Sicherheits-Semantik sonst unverändert: nach 12 h ohne Nutzung neu
  anmelden, Abmelden löscht Sitzung und Cookie, `use_strict_mode`,
  Cookie-Pfad, `HttpOnly`/`Secure`/`SameSite` wie heute. CLAUDE.md
  entsprechend anpassen.
- Alle Ränder berücksichtigen `env(safe-area-inset-*)`.
- **Kein Service Worker** (das Notfall-Werkzeug muss immer frisch laden).
- Ziehen-zum-Aktualisieren: optional (nur wenn es sich mit dem iOS-Scrollen
  verträgt; sonst weglassen — die Daten aktualisieren sich ohnehin alle 5 s).

## 7. Verhalten, das erhalten bleiben muss (Vertrag)

Quelle ist das heutige `assets/app.js`; alles hier Genannte wird inhaltlich
übernommen (gerne umstrukturiert, aber nicht abgeschwächt).

### 7.1 Datenabruf
- Zentrale Anfrage-Funktion mit Zeitlimits je Art (Poll 10 s, POST 20 s,
  Einzel-Bark 30 s, Alarm 120 s) und Fehlerarten `network`/`timeout`/`http`/
  `auth`/`csrf`/`readonly`/`mode_changed` inkl. der Sonderfälle für ältere
  Server-Stände (403 ohne `code`).
- Poller mit „läuft noch“-Schutz (Timer-Takt wird übersprungen, ausdrücklich
  angeforderte Abfragen laufen einmal hinterher).
- Intervalle: Status 5 s, Empfänger 30 s, Verlauf 30 s, Alarm-Info 60 s,
  Meldungen 60 s; Verlauf/Meldungen nur, solange ihre Seite (bzw. bei
  Verlauf: Übersicht am PC mit „Letzte Ereignisse“) sichtbar ist.
- Tab im Hintergrund: Abfragen stoppen und **jede offene Bestätigung
  schließen**; bei Rückkehr sofort aktualisieren; die „Liste nicht
  übernommen“-Messung beginnt neu. Ausnahme: „Hinweis im Browser bei Störung“
  aktiv (Abschnitt 8.6).
- CSRF-Token aus jeder Status-Antwort übernehmen (auch ins Abmelde-Formular);
  bei CSRF-Fehler Status neu holen, **kein** automatischer Neuversuch.
- Zeitzone aus der Status-Antwort; alle Zeiten in der Zeitzone der Wache;
  „vor N“-Angaben gegen die Server-Zeit (`server_time`-Versatz).
- Sekündlicher Ticker: ohne erfolgreiche Antwort seit 15 s → Störungs-Zustand
  (Abschnitt 6.2, Punkt 3) statt grün; Störungsanzeige nur bei Lageänderung
  neu aufbauen.
- HTTP 401 → „Abgemeldet“, Neuladen zur Anmeldung — **außer** während ein
  Alarm/eine Entwarnung läuft (dann Ergebnis lesbar lassen, nur Hinweis).

### 7.2 Bestätigungen
- **Halten zum Senden (NEU, ersetzt den Doppel-Klick mit Countdown) für
  alles, was einen Alarm sendet**: REAL ALARM, ENTWARNUNG, DEMO-ALARM über die
  Box, Nachsendung an die nicht Erreichten. Ablauf: Knopf → Sheet/Dialog mit
  Frage (nennt Modus LIVE/Demo, Empfängerzahl und wer stumm mit welcher
  Lautstärke) → Knopf „Gedrückt halten zum Senden“ füllt sich in **1,5 s**;
  Loslassen vorher bricht ab. Tastatur: Leertaste/Enter gedrückt halten.
  Screenreader: Beschreibung „Zum Senden gedrückt halten“.
- **Andere Aktionen mit Folgen** (Demo ein/aus, stumm/dauerhaft stumm,
  löschen, Key ersetzen, Test-Push, wichtige Meldung abschalten): Sheet mit
  Frage und rotem Bestätigungs-Knopf; Fokus startet auf „Abbrechen“.
- Für alle Bestätigungen: schließen sich nach einer Frist (10–25 s je nach
  Länge der Frage, gegen eine feste Uhrzeit gerechnet — iOS pausiert Timer),
  ein Bestätigen nach Fristablauf löst **nicht** aus; Escape schließt;
  höchstens eine offene Bestätigung.
- **Modus-Momentaufnahme**: Frage und `expect_demo` stammen aus demselben
  Stand. Ändert sich Demo/LIVE, während eine Alarm-Bestätigung offen ist →
  schließen + Hinweis. HTTP 409 `mode_changed` → Stand übernehmen, nichts
  automatisch neu senden.
- Live-Aktualisierungen dürfen eine offene Bestätigung, ein offenes Sheet,
  einen gezogenen Regler oder eine Eingabe nicht verändern.

### 7.3 Sperren gegen Doppel-Auslösen
- Während ein Alarm/eine Entwarnung läuft (`alarmInFlight`): alle
  Alarm-Knöpfe und die Nachsendung gesperrt; andere Aktionen sperren die
  Alarm-Knöpfe **nicht**.
- TEST/DEMO-ALARM/Abbrechen/Hinzufügen/Demo-Schalter: Doppelklick-Schutz
  (Zähler laufender Aktionen); Test-Push und Probe-Meldung: höchstens eine
  gleichzeitig.

### 7.4 Ergebnis nach Alarm/Entwarnung
- Während des Versands: „wird gesendet… (bis zu einer Minute, Seite nicht
  schließen)“.
- Ergebnis: Uhrzeit (Server-Uhr), Art, „x von y erreicht“; **nicht
  Erreichte zuerst** mit Grund (`http 0` = „keine Verbindung/Timeout“) und
  **NEU „Anrufen“**, falls Telefon hinterlegt; „Nur an … erneut senden“
  (Halten zum Senden, nur ids > 0, `only_ids`, `orig_at`); Liste der
  Erreichten; „Ausblenden“.
- Nachsende-Ergebnis: Zusammenführen wie heute (erreichte streichen, neue
  Gründe, nicht Mitgesendete bleiben).
- „Nichts gesendet“ (leere Schnittmenge) lässt die gemerkte Liste stehen.
- Fehler: „NICHT gesendet“ nur, wenn sicher (401/403/409/4xx); sonst
  „Antwort unklar – erst Verlauf prüfen, nicht sofort erneut auslösen!“ und
  Verlauf nach 5 s / 40 s nachladen; Nachsendung nach sicherem Fehlschlag
  wieder anbieten, nach „unklar“ nicht.
- Gemerkte nicht Erreichte in `localStorage` statt `sessionStorage`
  (Prüfung beim Laden wie heute, max. 12 h alt), werden nach Neuladen wieder
  angezeigt *(Änderung: iOS verwirft `sessionStorage`, wenn die
  Home-Bildschirm-App geschlossen wird)*.

### 7.5 Anzeige-Logik
- Alle heutigen Ableitungen übernehmen: `deliveryIncomplete`
  (`alarm_gaveup`, `key_progress`), Kontakt klemmt nur bei `relay=closed`,
  `heartbeatFailed` nur mit vorherigem Versuch, Speicher-Schwellen
  (`HEAP_WARN` 50 kB, `MAX_ALLOC_WARN` 30 kB, WLAN ≤ −80 dBm — **doppelt
  gepflegt mit `cron/check_offline.php`**), Neustart-Gründe inkl.
  Warn-Gründe (Hinweis nur am ersten Tag), Offline-Wächter
  (`watchdogState`: nicht konfiguriert / nie gelaufen / > 35 min / Bark
  gescheitert / Meldung abgeschaltet), Außen-Wächter (`outsideState`),
  `device_hint`, „alle stumm“, `mute_overdue`, `keys_corrupt`,
  Demo-Sync über `demo_sync_ok` (Fallback Versionsvergleich), letzter Alarm
  aus `log_view&filter=alarm_core` (`deriveAlarmInfo` inkl. Entwarnung,
  Demo-Erkennung alter Einträge), Befehlszeile inkl. Übersetzungen der
  Firmware-Quittungen und „Bestätigung nicht angekommen“ nach 600 s,
  abgeschlossene Befehle nach 600 s ausblenden.
- Neuer Alarm erkannt (`last_alarm|alarm_pending|alarm_seq` ändert sich) →
  Alarm-Info und Verlauf sofort nachladen.

### 7.6 Barrierefreiheit
Echte Überschriften und Listen, Status-Wechsel per `aria-live` (nur bei
echter Änderung), Zeilen-Rückmeldungen zusätzlich über eine unsichtbare
Live-Region, Sheets mit Fokus-Falle und Rückgabe des Fokus, alle Bedienungen
per Tastatur, sichtbarer Fokus, Farbe nie allein.

## 8. Neue Funktionen (mit Backend-Änderungen)

Alle neuen Aktionen: Eintrag in `DASHBOARD_ACTIONS` (`post`/`csrf`/`admin`
= true), Schreiben über `mutate_alarm_keys()` bzw. eigenen Lock, Verlauf über
`log_key_event()` bzw. `append_command_log()`, neue Ereignisse in
`log_event_types()` (Kategorie `keys` bzw. `system`, Felder freigeben) und in
der Verlaufs-Textlogik des Frontends. `classic.php` muss mit allen Änderungen
weiter funktionieren (Änderungen sind additiv).

### 8.1 Empfänger umbenennen — `keys_rename`
Felder `id`, `label` (gleiche Prüfung wie `keys_add`). **Keine**
Versionserhöhung (Namen gehen nicht an die Box, `api/keys.php` liefert keine
Namen). Verlauf `key_renamed` (alter und neuer Name, Key maskiert).

### 8.2 Bark-Key ersetzen — `keys_replace_key`
Felder `id`, `key` (Prüfung `valid_bark_key`, kein Duplikat zu einem anderen
Eintrag). Behält id, Name, Stumm-Felder, Lautstärke, `mute_token`, Telefon.
**Erhöht `keys_version`** über `bump_keys_version()` (die Box braucht den
neuen Key). Verlauf `key_replaced` (alter und neuer Key maskiert).
**Gesperrt für den aktiven Demo-Empfänger** (wie Löschen): `demo_mode.json`
hält eine eigene Kopie von Key und Name, Demo-Alarme gingen sonst weiter an den
alten Key. Meldung: erst Demo-Modus beenden.
Bestätigung im Frontend: „Der alte Key bekommt danach keine Alarme mehr.“

### 8.3 Telefonnummer — `keys_set_phone`
Felder `id`, `phone` (leer = entfernen; erlaubt `+`, Ziffern, Leerzeichen,
`-`, `/`, `(`, `)`, max. 30 Zeichen). Gespeichert als `phone` im Eintrag.
`keys_list` liefert `phone` **nur an den Admin** (wie `mute_token`);
`api/keys.php` und alle Bark-Texte nie. Keine Versionserhöhung. Verlauf
`key_phone_changed` ohne die Nummer.

### 8.4 „Automatisch wieder laut nach“ — `settings_list` / `settings_set`
- Neue Datei `app_settings.json` im `data_dir` (über `data_path()` /
  `write_json_file()` / Guard, eigener Lock `settings`, ins
  `inplace`/Rechte-Konzept einordnen wie die anderen per `rename()`
  ersetzten Dateien).
- `mute_max_seconds($config)` liest zuerst die Einstellung, sonst
  `config.php`, sonst 43200 — **eine** Definition, damit Leseseite
  (`mute_is_stale`), `expire_stale_mutes()`, `keys_list` (`mute_until`),
  `api/mute.php` und der Cron-Wächter automatisch folgen.
- Auswahl: 2, 4, 6, 8, 12, 16, 24 Std; der Wert aus `config.php` ist immer
  zusätzlich wählbar (sekundengenau, auch 0 = „nie“, falls dort so gesetzt);
  „nie“ wird sonst **nicht** angeboten (dafür gibt es „dauerhaft stumm“).
- `keys_list` liefert zusätzlich `mute_max_seconds` (wirksamer Wert, für
  beide Rollen) — daraus entstehen die Texte in der Empfänger-Ansicht.
- `settings_list` (GET, Admin) liefert Wert, Standard und Auswahl;
  `settings_set` (POST, Admin, CSRF) speichert nur erlaubte Werte. Keine
  Versionserhöhung. Verlauf `setting_changed` (Admin-only wie
  `notify_changed`). Hinweis im Frontend: „Gilt auch für laufende
  Stummschaltungen.“
- `index.php`/Frontend-Texte, die heute die Dauer aus `config.php` nennen,
  nutzen den wirksamen Wert.

### 8.5 Verlauf-Suche, Badge, Theme
Rein clientseitig (siehe 6.1, 6.4, 6.5). Theme und Browser-Hinweis pro Gerät
in `localStorage` (jeder Zugriff in try/catch; ohne Speicher gilt der
Standard).

### 8.6 „Hinweis im Browser bei Störung“ (PC)
- Pro Gerät einschaltbar; fragt beim Einschalten die Benachrichtigungs-
  Erlaubnis. Nur sichtbar, wo `Notification` verfügbar ist und lokale
  Benachrichtigungen funktionieren (auf dem iPhone ausgeblendet).
- Solange aktiv, läuft bei verstecktem Tab **nur** die Status-Abfrage weiter,
  alle 60 s (statt ganz zu stoppen).
- Meldet Übergänge in: Box offline, Alarm läuft / Alarm ausgelöst, NAS nicht
  erreichbar (ab 3 Fehlschlägen in Folge), Zustellung unvollständig — je
  einmal pro Übergang, plus einmal „wieder in Ordnung“. Klick fokussiert den
  Tab.

### 8.7 Home-Bildschirm-Modus, Anmeldeseite
Siehe 6.6, 6.7.

## 9. Code-Aufbau

### 9.1 Dateien
```
nas_dashboard/
  index.php            Login/Logout-Logik unverändert; liefert Anmeldeseite
                       oder App-Hülle (<div id="app">, meta csrf/role/
                       public-base/mute-max, Skript-Tags mit ?v=)
  classic.php          Rückfall: heutige Oberfläche (eingefroren)
  manifest.json
  assets/
    vendor/            Preact + Hooks + htm (eine UMD-Datei, Version fest),
                       LICENSE-Dateien daneben
    app/               neue Oberfläche, klassische Skripte (kein type=module)
                       in fester Reihenfolge, gemeinsamer Namensraum
    app.css            neues Stylesheet
    classic/           eingefrorene app.js + style.css für classic.php
    icons/             PNG-Icons fürs Manifest
```
Die Aufteilung von `assets/app/` legt der Builder fest. Vorgabe: die reine
Logik (Ableitungen aus 7.5, Formatierung, Verlaufs-Texte, Zusammenführen der
nicht Erreichten, Urteil-Rangfolge) liegt in einer Datei **ohne DOM- und
Preact-Zugriff**, damit sie unter Node getestet werden kann.

Warum keine ES-Module: Importe innerhalb von Modulen tragen kein
`?v=`, Import-Maps sind Inline-Skripte (CSP) — nach einem Deploy liefen sonst
alte Teile aus dem Cache.

### 9.2 Preact
- `htm` + Preact (inkl. Hooks), feste Version, als eine Datei vendort (z. B.
  das Standalone-Bundle von `htm/preact`). Keine weitere Bibliothek.
- Zustand: ein zentraler Store (Status, Empfänger, Demo, Verlauf,
  Meldungen, laufende Aktionen) mit den Pollern aus 7.1; Komponenten lesen
  daraus. Lokaler Bedienzustand (offenes Sheet, Regler beim Ziehen, Eingaben,
  offene Bestätigung) lebt in Komponenten und wird von Abfragen nicht
  überschrieben.
- Listen mit stabilen `key` (Empfänger-id, Meldungs-Art).

### 9.3 Tests
- Kein Test-Gerüst im Repo bisher. Neu: `tests/dashboard/*.test.mjs` im
  Repo-Wurzelordner (wird nicht aufs NAS kopiert), ausführbar mit
  `node --test` (Node 24, ohne npm), lädt die Logik-Datei per `vm`.
  Abgedeckt: Urteil-Rangfolge, `deliveryIncomplete`, `heartbeatFailed`,
  Wächter-Zustände, `deriveAlarmInfo`, Zusammenführen der nicht Erreichten,
  `restoreUnreached`-Prüfung, Verlaufs-Texte aller Ereignisse.
- Backend: PHP-Syntaxprüfung (`php -l`) und Durchspielen der neuen Aktionen
  in der lokalen Sandbox.
- Ende-zu-Ende in der lokalen Sandbox (Memory „Local dashboard sandbox“:
  `php -S`, Bark-Attrappe, simulierte Box per `api/status.php`, Headless
  Chrome über CDP, iPhone-Emulation): mindestens alles bereit, Hinweise,
  Box offline, NAS nicht erreichbar, Alarm läuft, Alarm ausgelöst, Demo-Modus
  (übernommen / nicht übernommen), Alarm mit Teil-Ausfall + Nachsendung +
  Anrufen, Sitzung abgelaufen während Alarm, alle Empfänger-Aktionen,
  Meldungen ändern + Probe, Lese-Rolle, Hell/Dunkel, PC- und iPhone-Breite.
  Screenshots als Nachweis.

### 9.4 Rückfall `classic.php`
- Heutige `index.php`-Oberfläche als `classic.php`, Assets nach
  `assets/classic/` kopiert, Pfade angepasst. Nicht angemeldet → Weiterleitung
  auf `index.php`; Abmelden postet an `index.php`.
- Bleibt einige Wochen; das Entfernen ist eine spätere, eigene Aufgabe.
- Link darauf in Einstellungen → Konto.

### 9.5 Dokumentation
- CLAUDE.md: Abschnitt über das Frontend (Seitenaufbau, Rangfolge,
  Bestätigungen, Polling, „doppelt gepflegte Schwellen“ mit neuem Dateinamen,
  Preact-Vendor, neue Aktionen/Ereignisse/`app_settings.json`, Tests)
  aktualisieren; „Aufbau der Seite (bewusst so)“ neu beschreiben.
- `nas_dashboard/SETUP.md`: Home-Bildschirm, `classic.php`, neue Datei im
  `data_dir`, falls dort relevant.

### 9.6 Auslieferung
Lokal fertigstellen und prüfen, dann committen (direkt auf `main`). Das
Kopieren aufs NAS (Memory „NAS deploy via SMB“) erst nach Rückmeldung des
Nutzers.

## 10. Dem Builder überlassen
- Genaue Datei-Aufteilung in `assets/app/`, Komponenten-Schnitt, Namen.
- Exakte Abstände/Größen innerhalb des beschriebenen Rasters, genaue
  Hell-Palette (AA-geprüft), Icon-Zeichnungen.
- Wortlaut neuer Texte (Deutsch, „die Box“, Ton wie heute: klar, ohne
  Fachjargon).
- Ob Ziehen-zum-Aktualisieren umgesetzt wird (6.7).
- Reihenfolge der Umsetzung.
