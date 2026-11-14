# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Was dieses Projekt ist

ESP32-Firmware (Arduino-Framework, Board `esp32dev`) für eine inoffizielle
Feuerwehr-**Zusatz**alarmierung. Der ESP32 überwacht den potenzialfreien
Relaiskontakt eines Swissphone-Laders (DIN Pin 1→GND, Pin 3→GPIO27,
`INPUT_PULLUP`, LOW = Alarm). Schließt der Kontakt, sendet die Box über den Dienst
**Bark** einen iOS Critical Alert an alle Empfänger. Es wird **kein Funk/POCSAG
dekodiert** – einziger Eingang ist der trockene Relaiskontakt.

Kernprinzip im README betont und einzuhalten: **Zusatz, kein Ersatz** für den Melder.

## Build / Flash / Monitor

PlatformIO (CLI-Weg):
```bash
pio run                 # kompilieren
pio run -t upload       # auf den ESP32 flashen
pio device monitor      # serielle Ausgabe, 115200 Baud
```
`platformio.ini` setzt `src_dir = boss925_alarm_bark`, damit der Ordnername exakt
zur `.ino` passt – dadurch lässt sich dasselbe Projekt **ohne Änderung auch in der
Arduino IDE** öffnen (Board: „ESP32 Dev Module"). Es gibt keine Tests und kein Lint.

## Architektur

Alles liegt in `boss925_alarm_bark/`:
- `boss925_alarm_bark.ino` — die gesamte Firmware-Logik.
- `config.h` — **alle** vom Nutzer editierbaren Werte (Secrets). Per `.gitignore`
  ausgeschlossen; existiert lokal als Kopie von `config.example.h`.
- `config.example.h` — Vorlage mit Platzhaltern, im Git.

Der `.ino`-Code liest **nichts** fest verdrahtet; jeder konfigurierbare Wert kommt
als `#define` (bzw. das Array `BARK_KEYS_ALARM[]`) aus `config.h`. Beim Ändern von
Verhalten/Defaults daher **immer beide** Config-Dateien synchron halten.
Einzige Laufzeit-Ausnahme: Die aktiven Alarm-Empfänger (`alarmKeys[]`) kommen
nach dem ersten Dashboard-Sync aus dem NVS, nicht mehr aus `config.h`
(siehe Empfängerlisten-Bullet unten).

Zusätzlich gibt es `nas_dashboard/`: PHP-Dateien für Synology Web Station
(Nginx + PHP 8.3, kein Docker, keine DB). Das Dashboard ist ein optionales
Rendezvous-System für Monitoring und manuelle Befehle. Der ESP32 bleibt
outbound-only: Status-Push nach oben, Command-Polling nach oben. Bark muss auch
bei NAS-Ausfall unverändert unabhängig funktionieren.

Und `cloud_watcher/`: der **Außen-Wächter** – Bash-Skript plus systemd-Timer
für eine kostenlose Cloud-VM (Oracle Always Free, Frankfurt, Ubuntu 24.04 ARM).
Sie ruft alle 5 min `api/alive.php` über die ÖFFENTLICHE NAS-Adresse auf und
meldet per Bark an den Status-Key, wenn das NAS von außen nicht erreichbar ist
(3 Fehlschläge in Folge) oder die DSM-Aufgabe `check_offline.php` > 45 min
nicht lief. Gegenrichtung: `alive.php` merkt sich jeden Aufruf
(`watcher_seen.json`), der Cron-Wächter meldet eine verstummte VM (Art
`outside_watcher`, nur vorhanden, wenn `watcher_token_sha256` gesetzt ist –
`outside_watcher_configured()`). Regeln: eigenes Token (`watcher_token_sha256`,
nur POST), NIE das Maschinen-Token der Box (die VM darf weder Status fälschen
noch Befehle abholen); `alive.php` bleibt schlicht und lauffähig mit älteren
Ständen von `common.php` (nur `load_config`/`data_path`/`read_json_file`/
`write_json_file`/`text_response`), schreibt best effort und meldet `ok=1`
auch bei klemmendem Datenordner (dafür gibt es `nas_web`); Bark-Texte der VM
ASCII; Meldungen gelten nur bei angenommener Zustellung als erledigt. Die VM
bleibt bewusst im Always-Free-Kontingent (kein Pay-as-you-go) – das Risiko
„Oracle stoppt idle VMs“ deckt die gegenseitige Überwachung ab. Dateien unter
`cloud_watcher/` sind LF (`.gitattributes`).

Ablauf in `loop()`: `feedWatchdog()` → `maintainWiFi()` (nicht-blockierender
Reconnect) → `processPendingAlarm()` (Nachsende-Runden) → `handleHeartbeat()` →
`handleRemoteMonitor()` (NAS ACK/Poll/Status, kurze Timeouts + Backoff) →
Wiederbewaffnungs-/Cooldown-Gate → Erkennung (Pegel-Abfrage im loop **plus**
Interrupt-Latch) → `startAlarm()`. Alarm geht als `level=critical` an alle Keys,
Status/Heartbeat/Test als `level=passive` nur an `BARK_KEY_STATUS`.

Wichtige Eigenschaften, die beim Refactoring erhalten bleiben müssen:
- **Pro Key isoliert senden** mit Retry (`BARK_MAX_TRIES`); ein toter Key darf die
  anderen nicht blockieren. `sendBark()` gibt `bool` zurück; bei HTTP 4xx wird
  **nicht** wiederholt (Client-Fehler). `sendStatus()` gibt ebenfalls `bool` zurück.
- **Nachsende-Puffer**: `startAlarm()` merkt den Alarm (`alarmPending`, `keyDone[]`,
  Zeitstempel der Erkennung im Text); `processPendingAlarm()` versucht alle
  `ALARM_RETRY_MS` erneut die noch offenen Keys, bis alle versorgt sind oder
  `ALARM_RETRY_GIVEUP_MS` erreicht ist (dann Status-Warnung an den Owner).
  Nachgesendete Alarme bekommen den Zusatz „(verspaetet zugestellt)".
- **Echte Flankenerkennung**: Nach einem Alarm erst wieder scharf, wenn der Kontakt
  `REARM_OPEN_MS` stabil offen war (`waitingForRelease`) — ein dauerhaft
  geschlossener Kontakt darf NICHT periodisch neu alarmieren. Zusätzlich
  **Cooldown** `COOLDOWN_MS` als Mindestabstand. Bleibt der Kontakt
  `STUCK_CONTACT_WARN_MS` geschlossen → einmalige „Kontakt klemmt?"-Statuswarnung.
- **Interrupt-Latch** (`relayEdge`, `IRAM_ATTR`): misst die Impulsdauer per
  Pin-Interrupt und rastet Impulse ≥ `DEBOUNCE_MS` ein (`isrLatched`), damit kein
  Alarm verloren geht, während `loop()` in einer blockierenden HTTPS-Sendung steckt.
- **Entprellung** `DEBOUNCE_MS` gilt in beiden Pfaden (Pegel-Abfrage und ISR).
  Heartbeat/WLAN laufen auch während Cooldown/Wiederbewaffnung weiter.
- **Heartbeat** mit NTP = 1×/Kalendertag ab `HEARTBEAT_HOUR`; ohne NTP Fallback auf
  millis-Abstand (`HEARTBEAT_MS`). Als erledigt markiert wird **nur bei Erfolg**;
  nach Fehlversuch neuer Versuch nach `HEARTBEAT_RETRY_MS`. Solange ein Alarm
  offen ist (`alarmPending`), pausiert der Heartbeat (Alarm hat Vorrang).
- **NAS-Monitoring ist Nebenlogik**: `REMOTE_*` sendet Status per
  `application/x-www-form-urlencoded` an `api/status.php`, pollt `api/poll.php`
  und quittiert `api/ack.php`. Keine JSON-/HTTP-Zusatzbibliothek in der Firmware.
  NAS-Fehler/TLS/Timeouts sind nicht fatal, nutzen kurze Timeouts und Backoff und
  dürfen die Bark-Alarmierung nicht verhindern.
- **Manueller REAL ALARM läuft DIREKT vom NAS zu Bark** (`bark_send_alarm_all()`
  in `nas_dashboard/lib/common.php`, PHP-cURL, `level=critical`, an alle Keys
  PARALLEL über `bark_post_many()` (curl_multi), pro Key isoliert mit Retry,
  HTTP 4xx ohne Retry, `ignore_user_abort`; Titel/Text gehen unverändert raus
  wie in config.php eingetragen — `bark_ascii()` sichert nur die
  Statusmeldungen mit Nutzereingaben ab). Der Alarmpfad braucht KEIN
  Schreibrecht: überfällige Stummschaltungen rechnet er beim Lesen als laut
  (`mute_is_stale()`), weggeschrieben wird erst NACH dem Versand und nur
  best effort (`with_lock_soft()`). Vorher/nachher landen `nas_alarm_started`/
  `nas_alarm` im Verlauf. Bewusste Entscheidung: Die manuelle
  Alarmierung braucht man gerade dann, wenn die ESP32-Kette klemmt — sie darf
  deshalb nicht am Poll des ESP32 hängen. `ALARM`-Commands legt das
  Dashboard nur noch als **Demo-Werkzeug** an (Button „DEMO-ALARM über
  die Box"): serverseitig gesichert (`demo_alarm_block_reason()`) — nur bei
  aktivem Demo-Modus UND nur, wenn der letzte Status-Push des ESP32 die
  aktuelle `keys_version` bestätigt, genau EINEN Empfänger (`keys_count=1`)
  meldet und jünger als `offline_after_seconds` ist (sonst könnte der
  Testalarm noch an die volle Liste gehen). Geprüft beim Anlegen (auch erneut
  unter dem `command`-Lock) UND beim Ausliefern in `poll.php`: Hat sich der
  Demo-Stand seit dem Anlegen geändert (`demo_changed_at` im Befehl), wird der
  Befehl als `cancelled` verworfen. REAL ALARM, ENTWARNUNG und DEMO-ALARM
  senden `expect_demo` mit (`'0'` = LIVE, sonst `key_id`); weicht der Modus
  ab, antwortet das NAS mit 409 `mode_changed` und sendet NICHTS.
  **Nachsendung nach einem Teilausfall**: REAL ALARM und ENTWARNUNG nehmen
  optional `only_ids="3,5"` an (`requested_only_ids()` → `bark_broadcast()`);
  die Auswahl kann die effektive Empfängerliste nur VERKLEINERN (Demo-Modus
  bleibt beim Test-Empfänger, gelöschte ids fallen heraus). Ein vorhandenes,
  aber unbrauchbares Feld wird mit 400 abgelehnt, eine leere Schnittmenge
  sendet nichts — NIE ersatzweise an alle. Dafür tragen die Sende-Ergebnisse
  und `effective_alarm_entries()` die `id` (`api/keys.php` gibt sie nicht aus);
  im Verlauf steht `resend` = Anzahl. Der `ALARM`-Zweig in der Firmware
  läuft durch dieselbe `startAlarm()`/`processPendingAlarm()`-Logik wie ein
  Relaisalarm.
  **TEST** läuft dagegen absichtlich über den ESP32 (Poll → `sendStatus()` an
  `BARK_KEY_STATUS`) — er testet die halbe Kette mit — und darf die
  Alarm-Zustandsmaschine nicht verändern. **ENTWARNUNG** (`false_alarm` /
  `bark_send_false_alarm_all()` → `bark_false_alarm_fields()`): ebenfalls
  **Critical Alert** (ASCII-Texte) an die effektive Empfängerliste, direkt vom
  NAS — muss durchdringen, sonst fährt jemand umsonst los. Unterschied zum
  Alarm: kurzer Standardton (`bark_false_alarm_sound`, Default `glass`) statt
  `alarm_fw` und **kein** `call=1`. Stummschaltungen (Arbeitsmodus) gelten
  wie beim Alarm, `expire_stale_mutes()` läuft vorher. Im Demo-Modus geht
  auch sie nur an den Test-Empfänger.
- **Empfängerliste wird im Dashboard gepflegt** (eine Quelle für beide
  Alarmwege): versioniert in `alarm_keys.json(.php)` im `data_dir`, verwaltet
  über `dashboard_api.php` (`keys_list`/`keys_add`/`keys_delete`, dazu
  `keys_test` = Test-Push an EINEN Empfänger über `bark_send_test()`: normale
  Mitteilung `level=active` mit dem Alarmton, bewusst KEIN Critical Alert und
  ohne Lock/Versions-Bump — prüft Key und Ton ohne Demo-Modus; das Dashboard
  fragt vorher nach, weil dabei der Alarmton spielt; letzter
  Empfänger nicht löschbar, max. 10 = `MAX_ALARM_KEYS`, Key-Format validiert).
  Alle Schreiber laufen über `mutate_alarm_keys()` (Lock, Lesen, Ändern,
  `backup_then_write()`); Versionen nur über `bump_keys_version()` — monoton
  über eine Hochwassermarke (`alarm_keys_version.json`), damit ein `.bak`-Restore
  die Version nie auf eine Nummer zurücksetzt, die der ESP32 mit anderem Inhalt
  kennt. `read_json_file()` unterscheidet `missing`/`corrupt`;
  `read_alarm_keys_state()` fällt bei kaputter Datei auf die `.bak` zurück und
  liefert `null`, wenn beide unlesbar sind — Schreiber brechen dann ab, statt
  mit leerer Liste neu anzufangen. `backup_then_write()` sichert nur einen
  lesbaren Stand. `effective_alarm_entries()` bereinigt (gültige Keys, keine
  Duplikate, max. 10) — genau das, was die Firmware akzeptiert. `poll.php` liefert
  `keys_version=N` mit; weicht sie ab, holt der ESP32 die Liste über
  `api/keys.php` (Plain Text: `version=`, `count=`, `key0=`…, Nonce gespiegelt)
  und persistiert sie per `Preferences` im NVS (`syncAlarmKeys()`), damit sie
  Reboot/NAS-Ausfall übersteht. Sync **nie** während `alarmPending`; leere oder
  unplausible Listen werden verworfen; eine Übernahme wird nur dann per
  `sendStatus()` an den Owner gemeldet, wenn sich das **Key-Set** geändert hat
  (Anzahl oder Keys, inkl. Demo-Wechsel) — reine Stumm-Wechsel (Arbeitsmodus)
  laufen bewusst still, sonst käme bei jedem Kurzbefehl-Aufruf der Kollegen
  ein Push. `BARK_KEYS_ALARM` (config.h) und `bark_keys_alarm`
  (config.php) sind nur noch Start-/Fallback-Listen, solange die
  Dashboard-Liste leer ist (Version 0).
- **Demo-Modus (Dashboard, nur Admin)**: leitet beide Alarmwege auf genau einen
  Test-Empfänger aus der gepflegten Liste um — ohne Firmware-Änderung, über
  denselben Empfängerlisten-Mechanismus: `demo_mode.json` im `data_dir` hält den
  Zustand, `effective_alarm_entries()` in `common.php` liefert im Demo-Modus nur
  den Demo-Key (genutzt von `api/keys.php` UND `bark_send_alarm_all()`), jeder
  Moduswechsel erhöht die `keys_version` → ESP32 holt die Liste beim nächsten
  Poll. `demo_set` schreibt erst `demo_mode.json`, dann `alarm_keys.json`
  (diese Reihenfolge ist gegenüber dem lockfreien Leser `api/keys.php`
  selbstheilend — NICHT tauschen); scheitert der zweite Schritt, wird der
  Demo-Zustand zurückgerollt. Ausschalten ist idempotent und erhöht immer die
  Version (Reparaturweg). Schutzregeln: Demo-Empfänger muss aus der Liste stammen (beim
  Zurückschalten existiert so garantiert eine nicht-leere Liste, die der ESP32
  akzeptiert), der aktive Demo-Empfänger ist nicht löschbar, das Dashboard zeigt
  ein Warn-Banner mit Sync-Status (Vergleich ESP32-`keys_version` vs.
  NAS-Version). Der Alarm selbst ist im Demo-Modus bewusst zeichengenau
  identisch mit einem echten (gleicher Text/Ton/Level, kein Demo-Zusatz) —
  nur die Empfängerliste ist kürzer.
- **Arbeitsmodus (Stummschaltung pro Empfänger)**: `muted`/`muted_at`/
  `mute_volume` pro Eintrag in `alarm_keys.json`; stumme Keys bekommen
  weiterhin denselben Critical Alert, aber mit ihrer **Stumm-Lautstärke**
  (`mute_volume` 0–10, Default 0 = lautlos) und ohne `call=1` — durchbricht
  trotzdem Stummschalter/Fokus (beide Alarmwege: `bark_alarm_fields()` mit
  `$muted`/`$muteVolume` und die Firmware über `alarmKeyMuted[]`/
  `alarmKeyMuteVolume[]`). Umschalten per öffentlichem GET/POST-Endpoint
  `api/mute.php?t=<GEHEIM_TOKEN>&state=on|off` (Token `mute_token` pro
  Eintrag, im Dashboard kopierbar und nur an die Admin-Rolle ausgeliefert;
  Alt-Form `key=<BARK_KEY>` bleibt gültig; Bremse gegen Durchprobieren über
  einen persistenten Zähler pro Absender (`throttle_*`, Bucket `mute`, KEIN
  `usleep`) — gezählt und gebremst werden NUR Fehlversuche, ein gültiger
  Token funktioniert immer (sonst könnte jemand hinter derselben Adresse die
  Links aller Kollegen sperren); idempotent; ruft ein BROWSER den Link auf
  (`Sec-Fetch-Dest: document` bzw. `Accept: text/html`), antwortet
  `mute_answer()` mit einer kleinen lesbaren HTML-Seite (ohne Token/Key,
  Stylesheet des Dashboards) — Kurzbefehle und Skripte bekommen UNVERÄNDERT
  den Text `ok=1`/`muted=…`; HEAD-Anfragen und bekannte Link-Vorschau-Abrufer
  (iMessage/WhatsApp/… User-Agents) schalten bewusst NICHTS um;
  bewusst KEIN `name=`-Parameter — Namen wären erratbar) oder per
  Dashboard-Aktion `keys_mute` (Admin+CSRF); Stumm-Lautstärke über
  `keys_set_mute_volume` (Admin+CSRF). Jeder Wechsel erhöht die
  `keys_version`; `api/keys.php` liefert `muted{i}=0|1`- und
  `mutevol{i}=0..10`-Zeilen mit (NIE die Tokens), die Firmware persistiert
  sie im NVS (`m{i}`/`mv{i}`, fehlend = laut bzw. 0 — alte Firmware/altes
  NAS bleiben so in sicheren Modi). `ensure_mute_tokens()` rüstet
  Bestandseinträgen Tokens nach (Lazy, ohne Versions-Bump, nimmt selbst den
  `keys`-Lock). Sicherheitsnetz `expire_stale_mutes()` (`mute_max_seconds`,
  Default 12 h, 0 = aus): läuft in poll.php (damit es den Relaisweg auch OHNE
  Cron erreicht), mute.php, keys.php, keys_list, nach `bark_send_alarm_all()`
  und im Cron-Wächter (dort auch ohne `bark_key_status`); die Leseseite
  rechnet überfällige Stummschaltungen schon vorher als laut
  (`mute_is_stale()` — EINE Definition für beide). Nimmt selbst den
  `keys`-Lock (NIE aus einem gehaltenen `keys`-Lock aufrufen; `$soft=true` =
  nicht fatal); die Info-Pushs laufen erst NACH der Antwort
  (`defer_after_response()` + `fastcgi_finish_request()`), nie davor — der
  ESP32 hat nur 4 s Timeout. Der Wächter erinnert zusätzlich am Status-Key (Standard max. 1×/Tag),
  solange jemand stumm ist (`mute_reminded_at` in `watchdog_state.json`).
  Beim **manuellen** Umschalten geht bewusst KEINE Bestätigung mehr an den
  betroffenen Key (Nutzerentscheidung, störte nur); nur die **automatische**
  Rückschaltung durch `expire_stale_mutes()` meldet sich weiterhin
  (ASCII-Texte!).
- **Dauerhaft stumm (`mute_permanent`)**: Pro Eintrag abschaltbares
  Sicherheitsnetz für längere Abwesenheiten. Ist das Flag gesetzt, überspringt
  `expire_stale_mutes()` den Eintrag (`mute_is_permanent()`) — die
  Stummschaltung läuft dann NIE von selbst aus. Bewusst **reine NAS-Logik**:
  Die Firmware kennt weiterhin nur `muted`/`mutevol`, `api/keys.php` liefert
  das Flag NICHT aus, und es gibt **keine** Firmware-Änderung dafür. Deshalb
  bumpt `keys_set_mute_permanent` (Admin+CSRF) die `keys_version` nur, wenn
  sich dabei auch `muted` ändert — sonst löst es einen sinnlosen Key-Sync aus.
  Einschalten setzt immer zugleich `muted=true` (kein Zustand „permanent, aber
  laut"). Beenden geht über `keys_set_mute_permanent`, `keys_mute` (Laut) und
  `api/mute.php?state=off` — alle drei löschen das Flag, weil „laut" immer die
  sichere Richtung ist. Der öffentliche Kurzbefehl-Link kann es bewusst **nur
  beenden, nie setzen**. `keys_list` liefert `mute_permanent` mit und lässt
  `mute_until` dann weg (es gibt keine Endzeit); das Dashboard zeigt einen
  eigenen roten Badge. Der Cron-Wächter zählt dauerhaft Stumme getrennt und
  erinnert für sie nur 1×/Woche (`permanent_mute_reminded_at`) statt 1×/Tag —
  eine bewusste Dauereinstellung darf nicht täglich pingen. (Beides sind
  Standardwerte — die Abstände stellt der Admin im Dashboard ein, siehe
  nächster Punkt.)
- **Meldungen an den Admin (einstellbar)**: Was das NAS an `bark_key_status`
  schickt und wie, stellt der Admin im Dashboard ein (Panel „Meldungen an den
  Admin“; `notify_list`/`notify_set`/`notify_test` in `dashboard_api.php`, alle
  nur Admin; gespeichert in `notify_settings.json` unter dem `notify`-Lock).
  EINE Quelle für Arten und Möglichkeiten: `admin_notify_types()` und
  `admin_notify_global_specs()` in `common.php`. Jeder Wert ist eine Auswahl
  aus festen `choices`; `load_notify_settings()` liefert immer einen
  vollständigen, geprüften Stand (Unbekanntes fällt auf den Standard),
  `notify_set` lehnt unbekannte Werte ab und speichert NUR ausdrücklich
  Gesetztes — alles andere folgt weiter den Standards bzw. `config.php`
  (`watchdog_offline_after_seconds`, `demo_reminder_after_seconds`). Pro Art:
  ein/aus, Dringlichkeit (Bark-Level passive/active/timeSensitive/critical),
  Verhalten in der Ruhezeit und Artspezifisches (Offline-Schwelle,
  Wiederholungen, „wieder da“-Meldung …); global: Ruhezeit (kappt die
  Dringlichkeit auf leise, außer bei Arten mit `ignore_quiet`), Ton,
  Lautstärke kritischer Meldungen und ein Stellvertreter (`extra_id` aus der
  Empfängerliste, best effort). ALLE Admin-Meldungen des NAS laufen über
  `admin_notify_send()` (Rückgabe `sent`/`failed`/`off`, Texte per
  `bark_ascii()`); `bark_send_status()` bleibt nur für die Info an einen
  BETROFFENEN Empfänger (automatische Laut-Rückschaltung). Standard = Verhalten
  von vorher: die alten Arten sind an und leise; NEUE Arten (`login_success`,
  `mute_changed`, `mute_expired`, `box_restart`, `box_health`,
  `weekly_report`) sind AUS. Im Wächter gilt eine abgeschaltete alte Art als
  erledigt (`$notify()` liefert true: Einmal-Logik und Verlaufseintrag laufen
  weiter, Ausgabe `OHNE_PUSH …`); die neuen Arten wertet er abgeschaltet gar
  nicht erst aus. `box_offline` und `nas_web` sind sicherheitsrelevant
  (`important`): Das Dashboard fragt vor dem Abschalten nach und zeigt den
  Zustand als Hinweis (`watchdog.offline_alert`/`web_alert` im Status).
  Meldungen aus Web-Anfragen (Login, Kurzbefehl-Link, Status-Push der Box)
  gehen per `defer_after_response()` erst NACH der Antwort raus — beim Login
  vorher `session_write_close()`, sonst wartet die Weiterleitung auf Bark.
  `box_restart` erkennt `api/status.php` an der gesunkenen `uptime_s` derselben
  `device_id` ODER an einem nach hinten gesprungenen Startzeitpunkt
  (`seen_at − uptime_s`, fängt Absturz-Schleifen mit einem Push pro Start).
  Regeln, die beim Umbau bleiben müssen: (1) `admin_notify_send()` liefert
  `off` NUR bei abgeschalteter Art oder leerem Status-Key — ein eingetragener,
  aber ungültiger Key ist `failed` (sonst hakt der Wächter einen Ausfall still
  als gemeldet ab). (2) Die Standards aus `config.php` gelten SEKUNDENGENAU
  (Auswahl-Schlüssel sind Sekunden: `offline_after_s`, `after_s`), nichts wird
  gerundet. (3) „Wieder online“ gibt es erst nach einem NEUEN Status-Push
  (`offline_seen_at`), nicht schon, weil die Schwelle hochgesetzt wurde.
  (4) War eine fällige Meldung abgeschaltet (`offline_push_pending`/
  `web_push_pending`), wird sie nach dem Wiedereinschalten sofort nachgeholt;
  ohne zugestellte Ausfall-Meldung gibt es kein „wieder da“. (5) Die
  Erinnerung „weiterhin OFFLINE“ nutzt denselben Text wie die erste Meldung
  (inkl. NAS-Diagnose bei `blocks_pushes`). (6) Die Lese-Rolle erfährt nichts
  über diese Einstellungen: kein `notify_changed` in `log_view`, kein
  `offline_alert`/`web_alert` im Status (dort liest `important_notify_flags()`
  nur die zwei Schalter, nicht die ganze Tabelle). (7) `heartbeat_ok=0` zählt
  nur als Fehler, wenn `last_heartbeat` nicht `nie` ist — Schwellen für
  `box_health` stehen doppelt (Cron und `app.js`), beide Stellen anpassen.
  Der Kurzbefehl-Link leitet Browser nach dem Umschalten per 303 auf
  `mute.php?done=…` um (ohne Token, schaltet nichts), damit Neu-Laden nicht
  erneut schaltet. NICHT einstellbar ist, was die Firmware selbst sendet
  (Heartbeat, Start-Meldung, „Kontakt klemmt?“, Listen-Übernahme, TEST).
- **Command-Protokoll** ist Plain Text (`ok=1`, `nonce=...`, `id=...`,
  `type=...`). Der ESP32 schickt pro Poll eine Nonce, die das NAS spiegeln muss.
  Das NAS vergibt monotone IDs, markiert einen gepollten Befehl sofort persistent
  als `delivered` und sendet ihn nicht erneut; der ESP32 ACKt danach. Diese
  at-most-once-Auslieferung verhindert Replay-Doppelalarme bei NAS-Reboot oder
  verlorenen alten Antworten. ACK-Verlust führt zu Dashboard-Status `delivered`,
  nicht zu heimlichem Neuauslösen. Ein nicht abgeholter `pending`-Befehl
  verfällt nach `pending_command_ttl_seconds` (Default 300 s) als `expired` und
  ist im Dashboard abbrechbar (`cancelled`) — ein Befehl an einen toten ESP32
  darf nicht Stunden später noch ausgeführt werden.
- **Probealarm-Fenster dämpft nur die Lautstärke, unterdrückt nichts**: Der
  wöchentliche ILS-Probealarm löst weiterhin bewusst den vollen Alarm aus (testet
  die ganze Kette wöchentlich mit) — er geht nur leiser raus. Erkennt die Firmware
  einen Alarm im Fenster (`inProbeAlarmWindow()`: `PROBE_WEEKDAY`,
  `PROBE_WINDOW_START_MIN`…`PROBE_WINDOW_END_MIN`, alles aus `config.h`), sendet sie
  `PROBE_ALARM_VOLUME` (5) statt `ALARM_VOLUME` (10) — sonst zeichengenau identisch
  (gleicher Text/Ton, weiterhin `level=critical`, weiterhin `call=1`). Das Fenster
  darf **niemals** dazu führen, dass ein Alarm ausbleibt. Sicherungen, die bleiben
  müssen: ohne NTP-Zeit (`getLocalTime()` schlägt fehl) gilt die volle Lautstärke;
  außerhalb von `PROBE_WEEKDAY` ebenso; die Lautstärke wird **einmal bei der
  Erkennung** in `pendingVolume` festgehalten (wie `pendingBody`), damit
  Nachsende-Runden nach Fensterende nicht plötzlich laut werden. Stumme Empfänger
  (Arbeitsmodus) bleiben unabhängig davon bei ihrer Stumm-Lautstärke. Der manuelle
  Dashboard-Alarm (`bark_send_alarm_all()`) kennt das Fenster bewusst **nicht** —
  ein Mensch löst nie den Probealarm aus.
- Bark-Versand: simple `application/x-www-form-urlencoded`-POST mit manuellem
  `urlEncode()` — **keine JSON-/HTTP-Bibliothek** hinzufügen. Nur ESP32-Core-Libs
  (`WiFi`, `HTTPClient`, `WiFiClientSecure`, `time.h`, `esp_task_wdt`,
  `esp_timer`, `Preferences`).
- Watchdog-Init ist core-versionsabhängig (`ESP_ARDUINO_VERSION_MAJOR >= 3` vs. 2.x)
  — beide Zweige beibehalten. Im 3.x-Zweig ist der Task-Watchdog ab Werk schon
  initialisiert: liefert `esp_task_wdt_init()` `ESP_ERR_INVALID_STATE`, muss
  `esp_task_wdt_reconfigure()` folgen, sonst bleiben die Default-Werte (5 s, kein
  Reset) aktiv.

## Bewusste Entscheidungen (nicht „wegoptimieren")

- TLS via `client.setInsecure()` (keine Zertifikatsprüfung) — gewollt, einfach/robust.
  Gilt auch für das NAS; Authentifizierung läuft über ein langes Maschinen-Token.
- OTA ist **nicht** eingebaut, nur als README-TODO dokumentiert.
- Alarm-Sound `alarm_fw` ist ein **eigener importierter** Bark-Ton; muss auf jedem
  Empfänger-iPhone vorhanden sein, sonst Standardton.
- Synology-Dashboard: separate Auth für Maschine (`REMOTE_MACHINE_TOKEN`, im PHP
  nur SHA-256-Hash, **nur per POST**, Hash-Vergleich case-/whitespace-tolerant)
  und Mensch (Dashboard-Login mit Passwort-Hash + CSRF, `sleep(1)` plus
  Sperrfenster pro Absender (IPv4 einzeln, IPv6 als /64) nach zu vielen
  Fehlversuchen als Brute-Force-Bremse — der Versuch wird VOR
  `password_verify` atomar gezählt (`throttle_hit()`), Login-POSTs von fremden
  Seiten (nur `Sec-Fetch-Site: cross-site` — bewusst KEIN Origin/Host-Vergleich,
  der hinter einem Host-umschreibenden Proxy jeden Login sperren würde) werden
  vorher abgewiesen; alle
  Throttle-Locks sind nicht fatal, damit ein fehlendes Schreibrecht den Login
  nicht sperrt), Session-Idle-Timeout 12 h, `session_write_close()` direkt
  nach der Auth in `dashboard_api.php` (ein laufender REAL ALARM blockiert so
  keine anderen Requests derselben Sitzung), ändernde Aktionen nur per POST,
  Sicherheits-Header/CSP zentral oben in `common.php`, `commands.log`-Rotation
  ab 5 MB, Assets mit `?v=filemtime()`-Cache-Busting). Zusätzlich optionaler
  Lese-Benutzer (`dashboard_readonly_user`/`..._password_hash`, Rolle in
  `$_SESSION['role']`, fehlende Rolle = `readonly`, fail-closed): sieht
  Status + Empfängerliste, alle ändernden/auslösenden
  Aktionen werden **serverseitig** per `require_dashboard_admin()` geblockt
  (UI-Ausblendung ist nur Komfort). Das Verlaufs-Panel liest die letzten
  `commands.log`-Zeilen per Tail-Read (`log_view` in `dashboard_api.php`;
  optional `filter=alarm|keys|system` über `log_event_category()`/
  `read_command_log_filtered()` — sonst verdrängen die täglichen Stumm-Wechsel
  der Kurzbefehle die Alarme aus dem Fenster) und
  liefert **nur serverseitig whitelistete Felder** aus (Keys stehen im Log nur
  maskiert). Dauerhaft im Verlauf landen auch: Relaisalarme (`relay_alarm` —
  `api/status.php` loggt, wenn sich `last_alarm` (oder, bei neuerer Firmware,
  der Zähler `alarm_seq`) gegenüber dem zuletzt gemerkten Stand DIESER Box
  ändert — gemerkt pro `device_id` in `alarm_seen.json`, damit ein zweites
  Board mit demselben Token nichts unterdrückt; Wechsel zu „nie" =
  ESP32-Reboot, kein Alarm; `source=remote` = Demo-Alarm nur, wenn die Box den
  ALARM-Befehl kurz zuvor mit `executed` quittiert hat — im Zweifel `relay`) und
  Wächter-Ereignisse (`box_offline`/`box_recovered`, gleiche Einmal-Semantik
  wie die Bark-Meldung), außerdem `box_restart` (nur nach
  Absturz/Watchdog/Brownout), `key_test`, `notify_changed` und
  `watcher_silent`/`watcher_recovered` (Außen-Wächter verstummt/wieder da). Der ESP32-Status-Push enthält als Diagnose-Felder
  zusätzlich `reset_reason` (Token, im Frontend übersetzt und bei
  Watchdog/Panic/Brownout gelb markiert), `free_heap` (Warnfarbe < 50 kB) und
  `keys_muted`. Das Frontend pollt nur bei sichtbarem Tab (Page Visibility API),
  behandelt HTTP 401 als „Sitzung abgelaufen" (Reload zur Login-Seite) und
  unterscheidet „NAS nicht erreichbar" (gelber Badge) von „ESP32 offline"
  (roter Badge; zeigt dann „Offline seit" statt der stalen Laufzeit); kritische
  Statusfelder werden gelb/rot eingefärbt, `innerHTML` wird nur bei echter
  Änderung ersetzt (Tooltip-/Fokus-Erhalt), pro Poll-Funktion gibt es einen
  In-Flight-Guard. Aufbau der Seite (bewusst so, nicht „aufräumen“):
  „Auslösen & Testen“ steht DIREKT unter dem Held-Bereich — die manuelle
  Alarmierung braucht man bei „Box offline“ und darf nicht wieder unter die
  Empfängerliste rutschen. Der Held-Bereich zeigt neben der Lampe einen Satz
  zum Zustand, zwei Fakten (Empfänger samt Stumm-Zahl; letzter Alarm aus
  `log_view&filter=alarm`) und Hinweise (ROWS mit `hint()`, Wächter, „alle
  stumm“). Lampen-Rangfolge: offline > Alarm aktiv > Kontakt klemmt > „Alarm
  ausgelöst“ (Zustellung durch, Kontakt noch geschlossen — rot, ruhig; KEIN
  grünes „Einsatzbereit“, solange darunter „GESCHLOSSEN (Alarm!)“ steht) >
  Demo-Modus (gelb, plus Chip in der Kopfzeile) > Einsatzbereit.
  Rückmeldungen stehen bei der auslösenden Aktion (`showMsg()`/`makeRowMsg()`:
  Farbe UND Zeichen, Erfolg verschwindet von selbst, Fehler bleiben). Empfänger:
  eine Zeile mit dreiteiligem Schalter Laut/Stumm/Dauerhaft stumm, Seltenes
  hinter „Mehr“. Das Ergebnis von REAL ALARM/ENTWARNUNG nennt die nicht
  Erreichten zuerst und bietet die Nachsendung nur an sie an. Sichtbare Texte
  sagen durchgehend „die Box“ statt „ESP32“. Die Frist der Inline-Bestätigung
  wächst mit der Länge der Frage (10–25 s). Die CSP kommt ohne `unsafe-inline` aus (keine
  `style=`-Attribute in `index.php` einführen!). Empfängerliste/Demo-Zustand
  werden vor jedem Schreiben als `*.bak`-Guard-Datei gesichert
  (`backup_then_write()`). Optionaler Offline-Wächter: `cron/check_offline.php`
  (DSM-Aufgabenplaner alle 15 min, CLI oder POST mit Maschinen-Token) meldet
  per `admin_notify_send()` (ASCII-Texte! Dringlichkeit, Abstände und
  Ein/Aus laut „Meldungen an den Admin“, Standard level=passive) einmalig
  Offline/Recovery an `bark_key_status`, erinnert an einen zu lange aktiven
  Demo-Modus (`demo_reminder_after_seconds`, danach max. 1×/Tag) und schreibt
  bei jedem Lauf `last_run_at` in `watchdog_state.json` — das Dashboard warnt
  daraus, wenn die DSM-Aufgabe fehlt oder hängt. Weil der Wächter als root
  läuft, prüft er die Schreibrechte des Web-Benutzers über einen Selbsttest:
  `web_health_check()` ruft `api/health.php` per `https://127.0.0.1/…` auf
  (`health_check_url`; antwortet NUR auf Loopback, sonst 404), das als `http`
  `data_dir_write_check()` ausführt (Lock, Schreiben, Zurücklesen, Löschen).
  Ab 2 Fehlschlägen in Folge Bark „NAS-Dashboard DEFEKT“, Erinnerung alle
  24 h, einmal „wieder OK“; Verlaufs-Events `nas_web_failed`/
  `nas_web_recovered`. Scheitert der Selbsttest, nennt die „Box OFFLINE“-
  Meldung das NAS als wahrscheinliche Ursache (Störung 10/2026: Box lief,
  `http` hatte nach einem Update nur noch Leserecht auf `web_packages`).
  `with_lock()` zeigt dann statt „Fehler.“ einen lesbaren Hinweis (ohne
  Pfad; Pfad nur per `error_log`; JSON für `dashboard_api.php` über die
  Konstante `FW_ALARM_JSON_API`). Was nur best effort ist (Mute-Ablauf im
  Alarmpfad, Login-Throttle, Verlauf), nutzt `with_lock_soft()` statt des
  fatalen `with_lock()`. Legt der root-Cron Dateien neu an, übergibt
  `fix_owner()` sie an Besitzer/Gruppe des `data_dir` — aber NUR, wenn der
  Ordner einem Nicht-root-Benutzer gehört, und OHNE je die Rechte-Bits
  anzufassen (kein `chmod`: auf Synology kommt der Zugriff von `http` meist
  über File-Station-ACLs, die ein chmod aushebeln könnte — die Funktion darf
  Zugriff nie wegnehmen). `data_dir_write_check()` prüft zusätzlich (Schritt
  `perm`), ob die Dateien beschreibbar sind, die das Dashboard an Ort und
  Stelle beschreibt (`inplace_data_files()`: Verlauf, Lock-Dateien, `*.bak`) —
  bewusst NICHT alle Dateien, denn die übrigen ersetzt es per `rename()`.
  Bei `perm` nennt die DEFEKT-Meldung die Datei, und „Box OFFLINE“ gibt dem
  NAS nur dann die Schuld, wenn die Störung die Status-Pushs wirklich
  blockiert (`blocks_pushes`). Derselbe Key aktiviert die
  Meldung bei ausgelöster Login-IP-Sperre. Ohne `bark_key_status` in
  config.php sind alle Meldewege inaktiv. Keine Geheimnisse in
  clientseitigem JavaScript. Laufzeitdaten (`data_dir`) bei Nginx möglichst
  außerhalb des Webroots ablegen; zusätzlich heißen alle Laufzeitdateien `*.php`
  mit Guard-Zeile (`DATA_FILE_GUARD`), damit Nginx sie nie als statische Datei
  ausliefert — Web Station ignoriert `.htaccess`. Diese Namens-/Guard-Logik
  steckt zentral in `data_path()`/`write_json_file()`/`read_json_file()`.
- `REMOTE_HTTP_TIMEOUT_MS` nicht wieder unter ~4 s drücken: Der TLS-Handshake
  ESP32↔Synology braucht typischerweise 1–3 s; zu knappe Timeouts führen zu
  Dauer-Backoff und einem flappenden ONLINE/OFFLINE-Status.

## Konventionen

- Kommentare und Nutzertexte auf **Deutsch mit echten Umlauten (ä/ö/ü/ß)**, UTF-8 –
  auch in Git-Commit-Nachrichten. **Ausnahme:** Strings, die als `title`/`body` an
  Bark gehen (`ALARM_TITLE`, `ALARM_BODY`, die `sendStatus()`-Texte), müssen **ASCII
  (ae/ue/oe)** sein, weil die Bark-Notification keine Umlaute korrekt darstellt.
- Editierbare Werte in den Config-Dateien klar oben markiert lassen
  (`>>> HIER STEHEN ALLE WERTE … <<<`).
- Secrets niemals in `config.example.h` oder die `.ino` schreiben.
