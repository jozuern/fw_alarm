# Umzug des Dashboards auf die Oracle-VM — Design

Stand: 2026-11-16 · Status: Entwurf, wartet auf Freigabe. Maßgeblich für den
heutigen Stand bleibt CLAUDE.md; dieses Dokument beschreibt die Planung.

## 1. Absicht und Warum

Das Dashboard (`nas_dashboard/`, PHP) zieht samt Daten vom Synology NAS
auf die vorhandene Oracle-Always-Free-VM `fw-alarm-watch` um und ist danach
unter **`https://alarm.tunero.dev/`** erreichbar (Cloudflare Tunnel, keine
offenen Ports). Dazu kommt die Anmeldung per **Passkey** (Face ID / Windows
Hello), das Passwort bleibt als Rückfall.

Gründe des Nutzers: Ausfälle des NAS, das Risiko, das NAS ins Internet zu
stellen, das Alter des NAS und seine begrenzten Möglichkeiten.

Reihenfolge (entschieden 2026-11-16): **erst Umzug mit dem bestehenden
PHP-Backend** (reiner Wechsel des Hosts, alle Tests bleiben gültig), danach
Passkey, später als eigenes Projekt die Neuentwicklung in Go. Nie Umzug und
Neubau gleichzeitig.

## 2. Ziele, Nicht-Ziele, Anti-Ziele

**Ziele**
- Dashboard, Verlauf, Empfängerliste, Stummschaltungen, Demo-Modus,
  Meldungen und Wächter laufen auf der VM genauso wie heute auf dem NAS.
- Die Kollegen brauchen **keine neuen Kurzbefehl-Links**: Die alten Links
  (`https://dein-name.synology.me/fw_alarm/api/mute.php?t=…`) funktionieren
  weiter.
- Die Box läuft ohne neues Flashen weiter (Flashen geht erst in ca. 4 Wochen,
  Mitte Dezember 2026).
- Gegenseitige Überwachung bleibt: Die VM meldet, wenn das NAS (der
  Außen-Wächter) verstummt, das NAS meldet, wenn die VM nicht erreichbar ist.
- Passkey-Anmeldung für beide Rollen, Passwort als Rückfall.

**Nicht-Ziele**
- Keine Änderung an der Firmware (`boss925_alarm_bark/`, `platformio.ini`).
  Die neue Adresse kommt mit dem ohnehin anstehenden Flashen; den Wert in
  `config.h` setzt der Nutzer.
- Kein Umbau des PHP-Codes über das Nötige hinaus; interne Namen
  (`nas_dashboard/`, Funktionsnamen, Ereignisnamen) bleiben bis zur
  Go-Neuentwicklung.
- Kein Cloudflare Access vor dem Dashboard (Mail-Code wäre im Notfall
  Reibung).
- Kein Pay-as-you-go bei Oracle.

**Anti-Ziele (wäre ein Fehlschlag, auch wenn es technisch läuft)**
- Ein Alarm (Critical Alert) geht durch den Umzug versehentlich an
  Empfänger raus.
- Ein Alarm der Box geht verloren oder ein Befehl wird doppelt ausgeführt.
- Eine Stummschaltung geht beim Umzug verloren (jemand bleibt ungewollt
  stumm).
- Ein Kollege muss etwas neu einrichten.
- Ein Ausfall des neuen Servers bleibt unbemerkt.

## 3. Ausgangslage

- **Box:** ruft `https://dein-name.synology.me/fw_alarm/api/{status,poll,ack,keys}.php`
  auf (fest in der Firmware). `HTTPClient` folgt **keinen** Weiterleitungen.
  Zeitlimit: Verbindung 4 s, TLS-Handshake ≥ 4 s, danach wartet sie bis zu
  4 s auf Antwortdaten (`REMOTE_HTTP_TIMEOUT_MS` 4000). Sie ignoriert
  Befehle, deren Nummer nicht größer ist als die zuletzt gesehene.
- **NAS:** Web Station (Nginx + PHP 8.3), Dateien unter `\\<NAS>\web\fw_alarm\`,
  Laufzeitdaten unter `/volume1/web_packages/fw_alarm_data`
  (per SMB erreichbar als `\\<NAS>\web_packages\fw_alarm_data`),
  `config.php` nur auf dem NAS, Cron-Wächter als DSM-Aufgabe alle 15 min.
- **VM:** Ubuntu 24.04.5 aarch64, A1.Flex 1 OCPU / 6 GB, 45 GB Platte,
  Zeitzone Europe/Berlin, eingehend nur SSH (iptables), unbeaufsichtigte
  Updates mit Neustart 04:30. Darauf läuft der Außen-Wächter
  (`fw-alarm-watch.timer`), der das NAS über `api/alive.php` prüft.
  Speicher belegt: 553 MB (9 %).
- **Domain:** `tunero.dev` liegt bei Cloudflare (DNS dort); die Hauptadresse
  ist die GitHub-Pages-Seite des Nutzers und bleibt unangetastet.

## 4. Was den Umzug prägt (Fakten mit Folgen)

1. **Box ohne Flashen umziehen geht nur über das NAS.** Bis zum Flashen
   reicht das NAS die vier Aufrufe der Box serverseitig an die VM weiter
   („Brücke“). Folge: Bis zum Flashen hängt die Box-Verbindung weiter am
   NAS; fällt es aus, erscheint die Box im Dashboard als offline (die
   Bark-Alarme der Box laufen davon unabhängig weiter).
2. **Bot Fight Mode von Cloudflare lässt sich im Free-Plan nicht pro Pfad
   ausnehmen** (weder WAF- noch Konfigurationsregeln). Er muss für die
   ganze Zone `tunero.dev` aus sein, sonst blockt Cloudflare die Box, das
   NAS (Brücke, Außen-Wächter) und die Kurzbefehle.
3. **Cloudflare beendet TLS** und sieht Maschinen-Token, Bark-Keys und
   Stumm-Tokens im Klartext (bewusst in Kauf genommen, Vorgabe aus der
   Sicherheitsprüfung).
4. **Cloudflare bricht Anfragen nach 100 s ab** (Fehler 524). Der
   ungünstigste REAL ALARM dauert ca. 31 s (3 Versuche à 10 s, parallel) —
   passt.
5. **Oracle stoppt „idle“ Always-Free-VMs:** idle heißt über 7 Tage
   CPU (95. Perzentil) < 20 %, Netz < 20 % und bei A1 Speicher < 20 %. Die
   VM liegt heute bei allen drei darunter. Gegenmittel ohne Kosten: Speicher
   auf 2 GB verkleinern (dann ca. 35–40 % belegt). Always Free erlaubt
   insgesamt 2 OCPU / 12 GB A1.
6. **Hinter dem Tunnel** kommen alle Aufrufe von 127.0.0.1. Ohne
   `trusted_proxies` + `proxy_client_ip_header` zählt die Login-Bremse alle
   Besucher als eine Adresse und das Cookie verliert `Secure`.
7. **Ubuntu räumt Sitzungen per Cron nach php.ini** (Standard 24 min) —
   eigener Sitzungsordner nötig, sonst endet das 12-h-Login.
8. **Mute-Links im Zugriffsprotokoll:** Das Token steht in der Adresse.
   Nginx darf nur den Pfad protokollieren, nicht die Query.
9. **Datenordner kopieren, nicht neu anfangen:** Befehlsnummern (sonst
   ignoriert die Box neue Befehle), `keys_version`, Stumm-Tokens, Verlauf,
   Wächter-Zustand.
10. **RP-ID eines Passkeys kann keine IP sein:** Lokale Passkey-Tests laufen
    über `http://localhost:8787`, nicht `127.0.0.1`.

## 5. Ansätze

| Frage | Gewählt | Verworfen und warum |
|---|---|---|
| Wie erreicht die Box die VM vor dem Flashen? | **Brücke auf dem NAS** (serverseitiges Weiterreichen per PHP-cURL) | Umzug auf das Flashen warten lassen: Umzug und neue Firmware am selben Tag, Fehler nicht zuzuordnen (Nutzer will eine riskante Änderung nach der anderen). Weiterleitung (302/307): die Box folgt keinen. DSM-Reverse-Proxy: arbeitet pro Hostname, nicht pro Pfad. |
| Alte Mute-Links | **Gleiche Brücke, serverseitig** | Weiterleitung 307: erhielte die Absender-Adresse für die Bremse, aber ob jeder Kurzbefehl/jedes Skript Weiterleitungen folgt, ist ungewiss — ein gescheitertes `state=off` hieße „bleibt stumm“. Nachteil serverseitig: Fehlversuche über die Brücke zählen alle auf die NAS-Adresse; gültige Tokens funktionieren laut Regel trotzdem immer. |
| Welche VM? | **Die vorhandene `fw-alarm-watch`** (Nutzer, 2026-11-16) | Zweite VM: A1-Kapazität in Frankfurt war knapp. |
| Tunnel-Art | **Lokal verwalteter Tunnel** (`cloudflared tunnel login/create`, Konfiguration als Datei im Repo) | Im Dashboard verwalteter Tunnel: braucht das Zero-Trust-Konto, Zustand nur in der Cloudflare-Oberfläche. |
| Wer überwacht die VM? | **Das NAS** führt das vorhandene `fw-alarm-watch.sh` als DSM-Aufgabe aus (Rollentausch) | Externer Dienst (z. B. healthchecks.io): neues Konto, Bark-Key bei Dritten. Für später notiert, wenn das NAS ganz wegfällt. |
| Passkey-Bibliothek | **`lbuchs/WebAuthn` v2.2.0**, eingebettet unter `lib/vendor/` (MIT, nur openssl + mbstring) | `web-auth/webauthn-lib`: viele Abhängigkeiten, Composer. Eigenbau: eigene Krypto-Prüfung ist das größere Risiko. |
| Wann Passkey? | **Nach dem Umzug**, eigener Plan | Im Umzug: zwei Änderungen auf einmal. |

## 6. Aufbau auf der VM

- **Pakete:** `nginx`, `php8.3-fpm`, `php8.3-cli`, `php8.3-curl`,
  `php8.3-mbstring` (für Passkey), `cloudflared` (Paketquelle von
  Cloudflare), `rsync`.
- **Benutzer:** `fwalarm` (Systembenutzer, keine Anmeldung). PHP läuft als
  `fwalarm` in einem eigenen FPM-Pool; der Standard-Pool `www` wird
  abgeschaltet.
- **Pfade:** Webroot `/srv/fw-alarm/app` (Inhalt = `nas_dashboard/` ohne
  `SETUP.md`, `config.example.php`, `data/`; root:root, lesbar),
  `config.php` darin (root:fwalarm 0640, beim Deploy nie angefasst),
  Daten `/srv/fw-alarm/data` (fwalarm 0750), Sitzungen
  `/srv/fw-alarm/sessions` (fwalarm 0700), PHP-Log
  `/var/log/fw-alarm/php.log` (mit logrotate).
- **Nginx:** nur `127.0.0.1:8080`; 404 für `config.php`,
  `config.example.php`, `SETUP.md`, `/data/`, `/lib/`, `/cron/`;
  `client_max_body_size 64k`; `fastcgi_read_timeout 130s`; Zugriffsprotokoll
  nur mit Methode + `$uri` (ohne Query), Status, Größe, User-Agent, Kennung
  der Brücke, Dauer.
- **PHP-FPM-Pool `fw-alarm`:** Socket `/run/php/fw-alarm.sock`
  (www-data 0660), `pm = ondemand`, `pm.max_children = 8`,
  `request_terminate_timeout = 180s`, eigener Sitzungsordner mit
  `gc_maxlifetime 43200`, `gc_probability 1`/`gc_divisor 100`, Fehlerlog
  in die Datei.
- **Cron-Wächter:** systemd-Timer `fw-alarm-cron.timer` alle 15 min,
  `php cron/check_offline.php` als `fwalarm`.
- **Tunnel:** `cloudflared` als Dienst, Ingress `alarm.tunero.dev` →
  `http://127.0.0.1:8080`, sonst 404.
- **VM-Größe:** 1 OCPU / 2 GB (siehe §4.5).
- **config.php (Produktion):** wie auf dem NAS, mit geänderten Werten:
  `data_dir` = `/srv/fw-alarm/data`, `public_base_url` =
  `https://alarm.tunero.dev/`, `health_check_url` =
  `http://127.0.0.1:8080/api/health.php`, `trusted_proxies` =
  `['127.0.0.1', '::1']`, `proxy_client_ip_header` = `CF-Connecting-IP`.
  Neue Kurzbefehl-Links zeigen damit auf die neue Adresse; die alten laufen
  über die Brücke aus.

## 7. Cloudflare

- DNS: `alarm` als CNAME auf den Tunnel (proxied), angelegt durch
  `cloudflared tunnel route dns`. Andere Einträge bleiben unberührt.
- Zone: **Bot Fight Mode aus.** SSL/TLS: Mindestversion TLS 1.2 (die Box
  kann nur 1.2), „Always Use HTTPS“ an.
- Konfigurationsregel für `alarm.tunero.dev`: Browser Integrity Check aus,
  E-Mail-Verschleierung aus, Rocket Loader aus, Sicherheitsstufe
  „Essentially off“ (die Box, das NAS und Kurzbefehle sind keine Browser).
- Cache-Regel für `alarm.tunero.dev`: Cache umgehen (wenig Verkehr, nie ein
  veralteter Stand).
- HSTS setzt weiter die App (`request_is_https()` über
  `X-Forwarded-Proto` des vertrauten Proxys).

## 8. Die Brücke auf dem NAS (`nas_bridge/`)

Ein eigener, kleiner Satz PHP-Dateien ohne Daten und ohne Geheimnisse, der
nach dem Umzug an die Stelle des Dashboards im NAS-Webordner tritt.

- **Weitergereicht** werden genau `api/status.php`, `api/poll.php`,
  `api/ack.php`, `api/keys.php` und `api/mute.php` — an
  `<target>/api/<datei>` mit festem Ziel aus `bridge_config.php`. Kein
  offener Proxy: Andere Pfade werden nie weitergereicht.
- **Anfrage:** Methode (GET/POST/HEAD), Query, Rumpf (max. 16 KB, sonst
  413) und nur diese Kopfzeilen: `Content-Type`, `Accept`,
  `Accept-Language`, `User-Agent`, `Sec-Fetch-Dest`, `Sec-Fetch-Mode`,
  `Sec-Fetch-Site`, `Sec-Purpose`, `Purpose`, `X-Purpose`, `X-Moz` (die
  letzten vier braucht `mute.php`, um Vorab-Abrufe des Browsers zu
  erkennen). Dazu `X-FW-Bridge: 1` (nur fürs Zugriffsprotokoll der VM — so
  sieht man nach dem Flashen, ob die Box noch über die Brücke kommt).
  Zertifikat des Ziels wird geprüft. Kommt der Rumpf als
  `multipart/form-data` (PHP gibt ihn dann nicht roh heraus), baut die
  Brücke ihn aus den Formularfeldern als
  `application/x-www-form-urlencoded` neu auf.
- **Antwort:** Statuscode, Rumpf und nur diese Kopfzeilen: `Content-Type`,
  `Location`, `Cache-Control`, `Content-Security-Policy`,
  `X-Content-Type-Options`, `Referrer-Policy`, `X-Frame-Options`. Eine
  relative `Location` (z. B. `mute.php?done=on`) bleibt relativ und führt
  so wieder über die Brücke.
- **Zeitlimits:** Verbindung 3 s, gesamt 3,5 s (unter den 4 s der Box). Ist
  die VM nicht erreichbar: 502 bzw. 504 mit kurzem Text, **nie 403** (403
  löst bei der Box die Bark-Warnung „NAS lehnt Token ab“ aus).
- **Wartung (Halt):** Liegt die Datei `bridge_hold.php` neben der Brücke
  (geprüft mit `is_file()`, nicht über eine zwischengespeicherte
  PHP-Konfiguration), antworten alle Brücken-Pfade 503 ohne Weiterreichen.
  So schreibt während des Datenumzugs niemand mehr (Box geht in ihren
  Backoff). Ein im Halt abgewiesener Mute-Aufruf wird protokolliert
  (Zeit, Parameter `t`/`key`, die ersten 12 Hex-Zeichen des SHA-256 des
  Tokens, gewünschter Zustand — nie das Token selbst) und nach dem
  Umschalten nachgeholt; sonst bliebe ein Kollege, dessen Automatik
  „Feierabend → laut“ in den Halt fiel, unbemerkt stumm.
- **Ersatzseiten:** `index.php` und `classic.php` leiten auf die neue
  Adresse um (302); `dashboard_api.php` antwortet 410 mit
  `{"ok":false,"code":"moved",…}` (im Halt-Zustand 503).
- **Bleibt auf dem NAS:** `assets/app.css`, `assets/favicon.svg` und
  `assets/app/theme-boot.js` (die Browser-Seite des Mute-Links lädt sie
  relativ), der alte
  Datenordner als eingefrorene Rückfall-Kopie, `api/alive.php` bis der
  Wächter getauscht ist.
- **Nach dem Flashen:** Die vier Box-Pfade fallen weg, `mute.php` bleibt
  dauerhaft (alte Links laufen aus, neue zeigen auf `alarm.tunero.dev`).

## 9. Ablauf

1. **Vorbereitung ohne Auswirkung:** VM verkleinern, Repo-Bausteine
   (`server/`, `nas_bridge/`, Wächter-Text) mit Tests.
2. **Probebetrieb auf der VM** mit Test-Daten, Test-Zugängen und
   Bark-Attrappe; zuerst nur per SSH-Tunnel geprüft, dann über Cloudflare
   (mit zufälligen Zugängen).
3. **Brücke im Probebetrieb** in einem eigenen NAS-Ordner gegen die
   Probe-VM, inkl. Messung der Zusatzzeit.
4. **Umschalten** zu einer mit dem Nutzer verabredeten, ruhigen Zeit
   (siehe §10).
5. **Wächter tauschen:** sofort beim Umschalten zeigt der VM-Wächter auf
   die neue Adresse (kein stiller Abschnitt); danach übernimmt das NAS per
   DSM-Aufgabe, der VM-Wächter wird abgeschaltet.
6. **Aufräumen und Texte:** Sichtbare Texte, die den Ort nennen („NAS“,
   „DSM-Aufgabe“, „Web Station“, „Cloud-VM/Oracle-Konsole“), werden
   ortsneutral; Doku (CLAUDE.md, SETUP, READMEs).
7. **Passkey** (eigener Plan).
8. **Flashen (Nutzer, ca. Mitte Dezember):** `REMOTE_BASE_URL` =
   `https://alarm.tunero.dev/api`, danach die Box-Pfade der Brücke
   entfernen.

## 10. Sicherheit gegen versehentlichen Alarm

Wege, auf denen ein Critical Alert entsteht: Relais der Box (läuft direkt
zu Bark, vom Umzug unberührt), REAL ALARM und ENTWARNUNG im Dashboard (an
alle), DEMO-ALARM über die Box (an den Demo-Empfänger), Test-Push an einen
Empfänger (`keys_test`, mit Alarmton). Regeln für den ganzen Umzug:

- **Probebetrieb nie mit echten Keys:** Test-Konfiguration mit
  `bark_host` = Bark-Attrappe auf der VM und Test-Keys; echte Daten und
  echter `bark_host` kommen erst beim Umschalten gemeinsam auf die VM,
  nachdem die Attrappe gestoppt ist.
- **Auf dem echten System nach dem Umschalten nie** REAL ALARM,
  ENTWARNUNG, DEMO-ALARM oder Test-Push auslösen. Geprüft wird nur mit
  Status-Pushs, dem TEST-Befehl (geht leise nur an den Status-Key), HEAD-
  bzw. Vorschau-Aufrufen des Mute-Links und einem An/Aus des
  **eigenen** Mute-Links.
- **Befehlsschlange leer** vor dem Umschalten (kein `pending`, kein
  `delivered` ohne Quittung); Befehle verfallen ohnehin nach 300 s.
- **Keine Doppel-Wächter:** DSM-Aufgabe auf dem NAS wird VOR dem Halt
  abgeschaltet, der Cron der VM erst NACH dem Umschalten eingeschaltet.
- **Keine verlorene Stummschaltung:** Halt-Zustand der Brücke während des
  Kopierens (so kurz wie möglich: Kopieren, Einspielen, automatische
  Prüfungen, sofort freigeben — die Sichtprüfung des Nutzers folgt danach);
  Prüfsummen aller Dateien NAS = VM; im Halt abgewiesene Mute-Aufrufe
  werden nachgeholt. Umschalten nicht in den Pendelzeiten der Kollegen.
- **Rückweg** vor und nach dem Freigeben der Brücke beschrieben, mit
  Abbruchkriterien (Plan, Aufgabe „Umschalten“).

## 11. Passkey-Anmeldung (Kurzfassung, Details im eigenen Plan)

- Beide Rollen können für ihr eigenes Konto Passkeys anlegen und löschen
  (Einstellungen → Konto). Anlegen verlangt das aktuelle Passwort (eine
  gestohlene Sitzung soll keinen dauerhaften Zugang anlegen können).
- Anmeldeseite: Knopf „Mit Passkey anmelden“ (auffindbare Passkeys, kein
  Benutzername nötig). Passwort-Formular bleibt.
- Gleiche Regeln wie beim Passwort: `Sec-Fetch-Site`-Prüfung, Login-Bremse
  pro Absender, Sitzung erst bei Erfolg, `auth_stamp` wie beim Passwort
  (Passwort ändern meldet auch Passkey-Sitzungen ab). Zusätzlich: Wird
  ein Passkey gelöscht, enden die mit ihm angemeldeten Sitzungen, und
  ein geändertes Passwort macht die Passkeys dieses Kontos ungültig
  (Handy verloren = Passwort ändern schließt jeden Zugang; Vorschlag,
  Bestätigung des Nutzers steht aus).
- RP-ID und Herkunft aus `public_base_url` (optional `passkey_rp_id`);
  `userVerification` erforderlich; Attestation `none`; ES256 und RS256.
- Herausforderungen liegen kurzlebig (5 min, einmalig) im Datenordner,
  nicht in einer Sitzung — vor der Anmeldung gibt es bewusst keine.
  Anonyme Abrufe sind pro Absender gebremst (30 je 5 min); ist der
  Speicher voll (200), fällt die älteste heraus.
- Ist die Passkey-Datei samt `.bak` unlesbar, bleiben bestehende Sitzungen
  gültig (Fehlerlog) — im Notfall nicht ausgesperrt werden geht vor; neue
  Passkey-Anmeldungen sind dann nicht möglich, das Passwort schon.
- Datenformat portabel für die Go-Neuentwicklung (Credential-ID base64url,
  öffentlicher Schlüssel als PEM).

## 12. Was dem Umsetzenden überlassen bleibt

- Genaue Gestaltung der Ersatzseiten der Brücke (Text, schlichtes HTML mit
  `app.css`).
- Wortwahl der ortsneutralen Texte, solange sie die Bark-Regel (ASCII)
  einhält und keine Aussage verliert.
- Aufteilung von `server/setup.sh` in Schritte, solange es wiederholbar
  (idempotent) bleibt.
