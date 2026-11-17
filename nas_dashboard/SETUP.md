# Synology-Setup für das BOSS-925 Dashboard

> **Stand 11/2026:** Das Dashboard dieses Projekts läuft inzwischen nicht mehr
> auf dem Synology-NAS, sondern auf einem kleinen Linux-Server hinter einem
> Cloudflare Tunnel – Einrichtung und Betrieb stehen in
> [`server/README.md`](../server/README.md). Auf dem NAS liegen nur noch die
> **Brücke** ([`nas_bridge/README.md`](../nas_bridge/README.md): reicht die
> Aufrufe der Box und alte Kurzbefehl-Links weiter) und der **Außen-Wächter**
> ([`cloud_watcher/README.md`](../cloud_watcher/README.md)).
>
> Diese Anleitung beschreibt weiterhin den Weg, das Dashboard **direkt auf
> einem Synology-NAS** zu betreiben – er funktioniert unverändert und ist der
> Rückweg. Die Meldungen des Wächters nennen den Ort des Dashboards neutral
> („Server“, „Dashboard DEFEKT“) und geben Hinweise für den Linux-Server; auf
> dem NAS gelten sinngemäß die Schritte aus Abschnitt 5 (Rechte der Gruppe
> `http` auf den Datenordner, DSM-Aufgabe).

## 1. Pakete installieren

Installiere im Synology Paket-Zentrum:

- **Web Station**
- **PHP 8.3**

Docker wird nicht benötigt.
Nginx als Back-end ist in Ordnung; Apache ist nicht erforderlich.

Prüfe im PHP-Profil (Web Station → Skriptsprache-Einstellungen → PHP-Profil
bearbeiten → Erweiterungen), dass **curl** aktiviert ist – der manuelle
REAL ALARM sendet damit direkt vom NAS an Bark (an alle Empfänger parallel).
Weitere Erweiterungen (z. B. mbstring) sind nicht nötig.

**Möglichst kein Reverse-Proxy davor:** Läuft das Dashboard hinter einem
Proxy auf demselben Rechner („Reverse Proxy“ von DSM unter Anmeldeportal →
Erweitert, Nginx, Cloudflare Tunnel), kommen aus Sicht von PHP ALLE Aufrufe von
`127.0.0.1`, und HTTPS endet schon am Proxy. Am einfachsten: Web Station direkt
auf Port 443 ausliefern lassen. Geht das nicht, in `config.php` **beide**
Einträge setzen:

```php
'trusted_proxies' => ['127.0.0.1', '::1'],
'proxy_client_ip_header' => 'CF-Connecting-IP',   // bzw. 'X-Real-IP' / 'X-Forwarded-For'
```

Ohne sie zählt die Login-Sperre alle Besucher als eine Adresse (jeder im
Internet sperrt mit ein paar Fehlversuchen den Admin aus – genau dann, wenn er
den REAL ALARM braucht), und das Sitzungs-Cookie geht ohne „Secure“ raus. Der
Proxy muss `X-Forwarded-Proto: https` mitschicken (Cloudflare Tunnel tut das).
Prüfen: Nach einer Anmeldung über die öffentliche Adresse zeigt die Meldung
„Anmeldung am Dashboard“ (Einstellungen → Meldungen an mich) die echte Adresse
des Besuchers, nicht `127.0.0.1`. `api/health.php` lehnt weitergereichte
Aufrufe ab (erkennbar an den Kopfzeilen des Proxys) und bleibt so von außen
unerreichbar. Die Einträge NICHT setzen, wenn PHP zusätzlich direkt aus dem
Internet erreichbar ist.

## 2. Dateien ablegen

Kopiere den Inhalt von `nas_dashboard/` in den Web-Ordner der Synology, zum Beispiel:

```text
/volume1/web/fw_alarm/
```

Kopiere danach:

```text
config.example.php -> config.php
```

`config.php` bleibt auf der Synology und enthält deine echten Geheimnisse
(Token-Hash, Passwort-Hash und die Bark-Keys für den NAS-Direktalarm).

**Aktualisieren (Reihenfolge):** Erst `lib/` und `assets/` kopieren (dazu
`api/`, `cron/`, `dashboard_api.php`, `classic.php`, `manifest.json`), **zuletzt
`index.php`**. `index.php` bindet `lib/shell.php` ein – käme sie zuerst, fände
eine neue `index.php` kurz die alte bzw. fehlende Hülle, und die Seite bliebe
leer oder zeigte einen PHP-Fehler. Bleibt nach einem Update trotzdem etwas
hängen: Die Übersicht bietet dann selbst „Neu laden“ an (siehe Abschnitt 7).

### Datenordner (`data_dir`)

Der Datenordner muss für den **PHP-Prozess beschreibbar** sein. Auf DSM:
File Station → Rechtsklick auf den Ordner → Eigenschaften → Berechtigung → dem
Benutzer, unter dem PHP läuft (bzw. der Gruppe `http`), Lesen/Schreiben geben
und auf Unterordner anwenden. Ob es funktioniert, siehst du daran, dass nach dem
ersten Status-Push des ESP32 eine `latest_status.json.php` erscheint; vorher
antworten die APIs mit HTTP 500.

**Lege den Ordner UNBEDINGT außerhalb des Web-Roots ab** (also NICHT unter
`/volume1/web/…`). Hier liegen die Bark-Keys im **Klartext**. Ein Pfad im
ausgelieferten Baum ist nur durch die Guard-Zeile (unten) geschützt – eine
einzige, zerbrechliche Schicht.

Empfohlen: eine **eigene DSM-Freigabe** außerhalb von `/volume1/web`, z. B.
Systemsteuerung → Freigegebener Ordner → Erstellen → Name `fw_alarm_data`
(Papierkorb aus), danach unter Berechtigungen dem Systembenutzer bzw. der
Gruppe `http` Lesen/Schreiben geben. Pfad für `config.php`:
`/volume1/fw_alarm_data`. Die Rechte dieser Freigabe gehören dir allein –
kein Paket-Update fasst sie an.

Alternative: ein Unterordner von `/volume1/web_packages` (z. B.
`/volume1/web_packages/fw_alarm_data`; per URL ebenfalls nicht erreichbar).
**Achtung:** Diesen Ordner verwaltet Web Station – ein Update kann dort die
Rechte zurücksetzen. Genau das ist hier schon passiert (Störung 10/2026: `http`
hatte nach einem Update nur noch Leserecht, Login und Status-Pushs scheiterten
still). Der Selbsttest des Wächters (Abschnitt 5) meldet das inzwischen, nach
jedem DSM-/Web-Station-Update aber trotzdem kurz prüfen.

Beachte ggf. `open_basedir` im PHP-Profil.

Alle Laufzeitdateien heißen zusätzlich absichtlich `*.php` und beginnen mit einer
Guard-Zeile (`<?php http_response_code(404); exit; ?>`): Nginx liefert sie
dadurch **nie** als statische Datei aus – wichtig, weil Web Station (Nginx)
`.htaccess`-Dateien ignoriert. Das ist die **zweite** Schicht (Verteidigung in
der Tiefe), ersetzt aber NICHT den Ordner außerhalb des Web-Roots.

## 3. Geheimnisse erzeugen

Maschinen-Token für den ESP32 – Token **und** Hash in einem Schritt, damit der
Token nie als Argument in der Shell-History landet:

```bash
php -r '$t=bin2hex(random_bytes(32)); echo $t, PHP_EOL, hash("sha256", $t), PHP_EOL;'
```

Zeile 1 (den Klartext-Token) trägst du in `boss925_alarm_bark/config.h` als
`REMOTE_MACHINE_TOKEN` ein, Zeile 2 (den SHA-256-Hash) in
`nas_dashboard/config.php` als `machine_token_sha256`. **Klartext und Hash
müssen zueinander passen** – wenn du den Klartext verlierst, erzeuge beides neu.
(Groß-/Kleinschreibung des Hashes und Leerzeichen drumherum spielen keine Rolle.)

Dashboard-Passwort – nach dem Befehl das Passwort eintippen und Enter drücken
(es steht so weder in der History noch können Zeichen wie `$`, `!` oder `'` es
verfälschen):

```bash
php -r 'echo password_hash(rtrim(fgets(STDIN), "\r\n"), PASSWORD_DEFAULT), PHP_EOL;'
```

Den Hash trägst du in `dashboard_password_hash` ein.

Optionaler **Lese-Benutzer** (z. B. für Kameraden, die nur den Status sehen
sollen): In `config.php` `dashboard_readonly_user` und
`dashboard_readonly_password_hash` setzen (Hash genauso mit `password_hash()`
erzeugen). Dieser Benutzer sieht Status und Empfängerliste (Keys maskiert),
kann aber weder TEST noch REAL ALARM auslösen und die Liste nicht ändern –
die Sperre greift serverseitig, nicht nur im Browser. Beide Werte leer = Login aus.

Bark-Keys der Alarm-Empfänger: **im Dashboard pflegen** (Bereich
„Empfänger“). Die Liste ist versioniert, gilt für beide Alarmwege und
wird vom ESP32 automatisch übernommen und im NVS gespeichert.
`bark_keys_alarm` in `config.php` und `BARK_KEYS_ALARM` in `config.h` sind nur
Start-/Fallback-Listen, solange die Dashboard-Liste leer ist.

Optional, aber empfohlen, sobald es außer der echten Box noch einen
Bastel- oder Ersatz-ESP32 mit demselben Maschinen-Token gibt: in `config.php`
`box_device_id` auf denselben Wert setzen wie `REMOTE_DEVICE_ID` in `config.h`
der echten Box (z. B. `'boss925-01'`). Dann zählen für Online-Anzeige,
Offline-Wächter und Demo-Sicherung nur deren Status-Pushs, und nur sie bekommt
Befehle (TEST, DEMO-ALARM). Pushs anderer Boards nimmt das NAS weiter an, führt
sie aber nur als Hinweis „fremdes Board meldet sich“. Ohne den Eintrag zählt
jeder Push mit dem Token – ein weiter pushendes Bastel-Board würde dann den
Ausfall der echten Box verdecken; melden sich mehrere Boards, warnt das
Dashboard. Ein Tippfehler im Wert lässt die echte Box als offline erscheinen.

## 4. HTTPS und Portweiterleitung

Nutze Synology DDNS oder eine eigene Domain, zum Beispiel:

```text
dein-name.synology.me
```

Erstelle in DSM ein Let's-Encrypt-Zertifikat für diesen Hostnamen und weise es Web
Station zu. Leite an deinem Router genau **TCP-Port 443** von außen auf die
Synology weiter.

Dashboard-URL (extern bzw. im LAN):

```text
https://dein-name.synology.me/fw_alarm/
https://192.168.1.50/fw_alarm/
```

ESP32-API-Basis-URL für `REMOTE_BASE_URL` – im LAN am robustesten die lokale
IP (funktioniert dank `setInsecure()` trotz Zertifikat-Mismatch und auch ohne
Internet):

```text
https://192.168.1.50/fw_alarm/api
```

## 5. Offline-Wächter (optional, empfohlen)

Der tägliche Heartbeat kommt vom ESP32 selbst – eine tote Box schweigt einfach,
und ohne offenes Dashboard merkt das niemand. `cron/check_offline.php` schließt
die Lücke: Es prüft das Alter des letzten Status-Pushs und schickt dir **einmal**
eine leise Bark-Meldung, wenn die Box zu lange schweigt (Default: 10 Minuten,
`watchdog_offline_after_seconds`), und **einmal**, wenn sie wieder da ist.

Einrichtung:

1. In `config.php` deinen Status-Bark-Key eintragen (derselbe wie
   `BARK_KEY_STATUS` in `config.h`):

   ```php
   'bark_key_status' => 'DEIN_BARK_KEY',
   ```

2. DSM → **Systemsteuerung → Aufgabenplaner → Erstellen → Geplante Aufgabe →
   Benutzerdefiniertes Skript**:
   - Benutzer: `root` (oder ein Benutzer mit **Lese- UND Schreibrecht** auf
     den `data_dir` und Leserecht auf den Web-Ordner – der Wächter schreibt
     seinen Zustand, den Verlauf und beendet überfällige Stummschaltungen).
     Gehört der `data_dir` dem Web-Benutzer selbst (klassische Unix-Rechte
     statt File-Station-Berechtigung), übergibt der Wächter als `root`
     angelegte Dateien automatisch an dessen Besitzer/Gruppe (braucht die
     Erweiterung `posix` im CLI-PHP). Die Rechte-Bits fasst er nie an, und bei
     einem `root`-eigenen Ordner ändert er nichts – dort regeln die
     File-Station-Berechtigungen (ACL) den Zugriff.
   - Zeitplan: täglich, **alle 15 Minuten** wiederholen
   - Skript – immer mit dem vollen Pfad des PHP-8.3-Pakets (das `php` im
     PATH ist auf DSM oft ein anderes PHP ohne cURL; per SSH mit
     `which php83` prüfbar):

     ```bash
     /usr/local/bin/php83 -f /volume1/web/fw_alarm/cron/check_offline.php
     ```

3. Aufgabe einmal manuell ausführen („Ausführen") und das Ergebnis ansehen
   (Aufgabenplaner → Aktion → „Ergebnis anzeigen"). Gut ist z. B.
   `WEB_OK | OK` (bzw. `WEB_OK | NO_DATA`, solange die Box noch nie gepusht
   hat), ggf. ergänzt um `DEMO_ACTIVE`, `MUTED_ACTIVE (n)` o. ä.
   - `DISABLED` – der Key aus Schritt 1 fehlt noch (überfällige
     Stummschaltungen beendet der Lauf trotzdem). Der Wächter meldet dann
     bewusst nichts und vermerkt auch keinen Lauf – das Dashboard warnt, und
     ein eingerichteter Außen-Wächter meldet „NAS-Waechter meldet nichts“.
   - `NO_CURL` – das verwendete CLI-PHP hat keine curl-Erweiterung → vollen
     Pfad wie oben verwenden.
   - `WEB_FAIL (n) …` – der Selbsttest (unten) schlägt fehl; `WEB_FAIL_NOTIFIED`
     = „NAS-Dashboard DEFEKT“ gemeldet, `WEB_RECOVERED` = wieder OK.
   - `HEALTH_ISSUES (n)` / `HEALTH_NOTIFIED` – Auffälligkeiten an der Box
     (schwaches WLAN, wenig Speicher, Heartbeat gescheitert; nur, wenn diese
     Meldung eingeschaltet ist).
   - `WATCHER_OK` / `WATCHER_SILENT (bereits gemeldet)` /
     `WATCHER_SILENT_NOTIFIED` / `WATCHER_RECOVERED` – Zustand des
     Außen-Wächters (nur, wenn er eingerichtet ist). `WATCHER_UNKNOWN (…)` –
     der Datenordner klemmt, deshalb sind die Aufrufe der VM nicht zu
     beurteilen; das deckt die Meldung „NAS-Dashboard DEFEKT“ ab.
   - `STATE_WRITE_FAILED (…)` – der Wächter konnte seinen eigenen Zustand
     (`watchdog_state.json.php`) nicht speichern (Platte voll? Rechte?). Dann
     zählt er nicht weiter und wiederholt Meldungen – sofort beheben.
   - `SEND_FAILED` / `…_FAILED` – Bark hat die Meldung nicht angenommen
     (Status-Key prüfen; erreicht das NAS Bark überhaupt?). Das Dashboard zeigt
     das ebenfalls an, und ein eingerichteter Außen-Wächter meldet nach drei
     Läufen „NAS kann Bark nicht erreichen“ – dann scheitert auch der manuelle
     REAL ALARM.

Das Skript ist auch per HTTPS aufrufbar (POST mit dem Maschinen-Token wie bei
`api/status.php`) – anonyme Web-Aufrufe weist es mit 403 ab. Derselbe
`bark_key_status` aktiviert außerdem eine Meldung, wenn eine IP wegen zu
vieler Login-Fehlversuche gesperrt wurde.

**Welche Meldungen du bekommst und wie**, stellst du im Dashboard ein
(Einstellungen → „Meldungen an mich“, nur Admin) – ohne etwas an `config.php` zu ändern:

- pro Art von Meldung: ein/aus, Dringlichkeit (leise / normal / dringend /
  kritisch), Verhalten in der Ruhezeit und „Probe senden“;
- je nach Art zusätzlich: nach wie vielen Minuten die Box als offline gilt,
  ob und wie oft erinnert wird, ob auch „wieder online“ bzw. „wieder OK“
  gemeldet wird;
- für alle Meldungen: Ruhezeit (dann kommt alles nur leise an, außer den
  Arten, die du davon ausnimmst), Ton, Lautstärke kritischer Meldungen und ein
  Stellvertreter aus der Empfängerliste, der die Meldungen zusätzlich bekommt.

Ohne Änderung bleibt alles wie bisher: die bekannten Meldungen sind an und
leise, die Werte kommen aus `config.php`. Die zusätzlichen Arten (Anmeldung am
Dashboard, Stumm-/Laut-Wechsel per Kurzbefehl, automatisch beendete
Stummschaltung, Neustart der Box, Auffälligkeiten an der Box, Wochenbericht)
sind aus, bis du sie einschaltest. Schaltest du „Box offline“ oder
„NAS-Dashboard defekt“ ab, fragt das Dashboard nach und zeigt es danach als
Hinweis an. Steht im Ergebnis der DSM-Aufgabe `OHNE_PUSH (…)`, war eine
Meldung fällig, aber im Dashboard abgeschaltet.

Nicht einstellbar sind die Meldungen, die die Box selbst verschickt (täglicher
Heartbeat, Start-Meldung, „Kontakt klemmt?“, Warnung bei nicht zustellbarem
Alarm, Übernahme der Empfängerliste, Antwort auf TEST) – sie kommen aus der
Firmware.

Der Wächter übernimmt noch drei weitere Aufgaben:

- **Selbsttest des Dashboards**: Der Wächter läuft als `root` und würde es
  selbst nie merken, wenn der Web-Benutzer `http` nicht mehr in den
  `data_dir` schreiben darf (passiert z. B., wenn ein Web-Station-Update die
  Rechte von `web_packages` zurücksetzt – dann scheitern Login und alle
  Status-Pushs still). Deshalb ruft er bei jedem Lauf `api/health.php` über
  `https://127.0.0.1/…` auf; das Skript läuft als `http` und prüft Lock-Datei,
  Schreiben, Zurücklesen und Löschen – und ob die Dateien beschreibbar sind,
  die das Dashboard an Ort und Stelle beschreibt (Verlauf, Lock-Dateien,
  Sicherungen; die Meldung nennt dann die betroffene Datei). Ab **zwei Fehlschlägen in
  Folge** kommt die Bark-Meldung „NAS-Dashboard DEFEKT“, danach alle 24 h
  erneut, bis es wieder geht (dann einmal „wieder OK“; Abstand und „wieder OK“
  sind im Dashboard einstellbar). Abhilfe meist:
  File Station → `fw_alarm_data` → Eigenschaften → Berechtigung → Gruppe
  `http` Lesen + Schreiben. `health.php` antwortet nur auf Aufrufe vom NAS
  selbst, alle anderen bekommen 404.

- **Demo-Modus-Erinnerung**: Bleibt der Demo-Modus länger als
  `demo_reminder_after_seconds` (Default 4 h) aktiv, erinnert er leise per
  Bark daran (danach max. 1× pro Tag; beides im Dashboard einstellbar) – im
  Demo-Modus erreichen echte Alarme nur den Test-Empfänger.
- **Selbstüberwachung**: Jeder Lauf wird protokolliert; das Dashboard zeigt
  „Offline-Wächter: zuletzt gelaufen vor X min" und warnt, wenn die Aufgabe
  nie oder seit mehr als 35 Minuten nicht gelaufen ist – also mehr als zwei
  verpasste 15-Minuten-Läufe (z. B. weil sie im Aufgabenplaner deaktiviert
  wurde) – und auch, wenn sie zwar läuft, Bark ihre letzte Meldung aber
  abgelehnt hat.

### Außen-Wächter (optional)

Der Wächter oben läuft AUF dem NAS – fällt das NAS selbst aus (Strom oder
Internet am Standort, Router, DDNS, Zertifikat), meldet er nichts. Dafür gibt es
den Außen-Wächter: eine kleine kostenlose Cloud-VM, die `api/alive.php` alle
5 Minuten von außen aufruft und per Bark meldet, wenn das NAS nicht erreichbar
ist oder die DSM-Aufgabe nicht mehr läuft. Mit einem aktuellen `api/alive.php`
meldet sie außerdem, wenn in `config.php` der `bark_key_status` fehlt (dann
meldet der Wächter oben bewusst nichts) und wenn das NAS selbst Bark nicht
mehr erreicht (dann scheitert auch der manuelle REAL ALARM). Umgekehrt meldet
der Wächter oben, wenn die VM verstummt, und das Dashboard zeigt, wenn die VM
ihre eigenen Meldungen nicht bei Bark loswird. Die Einstellungen unter
„Meldungen an mich“ gelten nur für das NAS, nicht für die VM.
Einrichtung: [`cloud_watcher/README.md`](../cloud_watcher/README.md); auf dem
NAS braucht es nur `api/alive.php` und `watcher_token_sha256` in `config.php`
(eigenes Token – derselbe Hash wie `machine_token_sha256` wird abgelehnt).

### Backup der Laufzeitdaten

Empfängerliste, Demo-Zustand, Einstellungen (`notify_settings.json.php`,
`app_settings.json.php` – z. B. „Automatisch wieder laut nach“) und Verlauf
leben im `data_dir` (außerhalb des Web-Roots, z. B. `/volume1/fw_alarm_data`).
Nimm den Ordner in dein NAS-Backup auf
(z. B. Hyper Backup), wenn du eins hast. Zusätzlich hält das Dashboard vor
jeder Änderung der Empfängerliste die letzte **lesbare** Vorversion als
`*.bak.php`-Datei fest. Ist `alarm_keys.json.php` beschädigt, greifen Dashboard,
ESP32-Sync und REAL ALARM automatisch auf die Sicherung zurück; sind beide
unlesbar, verweigert das Dashboard Änderungen, statt mit einer leeren Liste neu
anzufangen. Die Listen-Version läuft dabei nie rückwärts (Hochwassermarke in
`alarm_keys_version.json.php`), damit der ESP32 einen wiederhergestellten Stand
sicher neu holt.

## 6. Härtung

- Wähle ein langes Maschinen-Token und ein separates starkes Dashboard-Passwort.
- Aktiviere DSM-Firewall und Auto-Block für wiederholte Fehlversuche (schützt
  DSM; das Dashboard-Login bremst Brute-Force zusätzlich mit `sleep(1)` **und**
  sperrt eine IP nach zu vielen Fehlversuchen für ein Zeitfenster –
  `login_max_failures` / `login_lockout_window_seconds` in `config.php`).
- Eingebaut sind außerdem: Sicherheits-Header (u. a. Content-Security-Policy,
  `X-Frame-Options`, HSTS bei HTTPS, zentral in `lib/common.php`), ein
  Session-Ablauf nach 12 h Inaktivität (bis dahin hält die Sitzung auch
  verlässlich – das Dashboard setzt dafür `session.gc_maxlifetime`, sonst
  könnte PHP sie je nach Profil schon nach 24 min, dem PHP-Standard,
  wegräumen), Rotation von
  `commands.log` ab 5 MB
  sowie `index.php`-Platzhalter gegen Verzeichnislisting in `api/`, `lib/`,
  `cron/` und im `data_dir`. Logins von fremden Seiten (Login-CSRF) werden
  abgewiesen, bevor sie die Login-Sperre zählen.
- **Handy verloren oder Passwort weitergegeben?** In `config.php` einen neuen
  `dashboard_password_hash` eintragen: Das meldet sofort ALLE Sitzungen dieses
  Kontos ab (auch die auf dem verlorenen Gerät). Eine offene Sitzung läuft
  sonst nicht von selbst ab, solange ihr Tab weiter abfragt. Entsprechend
  beendet ein geänderter/entfernter Lese-Benutzer dessen Sitzungen.
- **Kurzbefehl-Link in falschen Händen?** Am Empfänger „Neue Links erzeugen“
  – die bisherigen Links gelten sofort nicht mehr; die neuen im Kurzbefehl
  des Empfängers eintragen. (Alte Links der Form `?key=…` hängen am Bark-Key:
  dafür „Key ersetzen“.)
- Kurzbefehl-Links (`api/mute.php`) möglichst nicht per Messenger/Mail
  verschicken: Link-Vorschauen und Mail-Scanner rufen Links schon beim
  Absender ab. Bekannte Vorschau-Abrufe (und HEAD-Anfragen) schalten zwar
  bewusst nichts um, aber nicht jeder Scanner gibt sich zu erkennen. Für die
  kopierten Links in `config.php` `public_base_url` setzen, sonst stammen sie
  von der Adresse, unter der das Dashboard gerade offen ist (z. B. LAN-IP).
- Lösche nach der Einrichtung `config.example.php` und `SETUP.md` aus dem
  Webroot auf dem NAS – sie verraten Angreifern nur die Struktur (das Original
  bleibt ja im Git-Repo).
- Halte DSM, Web Station und PHP aktuell.
- Gib keine PHP-Fehler öffentlich aus. Die Dateien selbst senden nur knappe
  Fehlermeldungen.
- `config.php` ist nur so lange geschützt, wie PHP ausgeführt wird – wenn PHP
  im Web-Station-Profil deaktiviert wird, wäre der Quelltext (inkl. Bark-Keys)
  herunterladbar. Nach Änderungen an Web Station kurz prüfen, dass
  `https://…/fw_alarm/config.php` eine leere Seite liefert.

## 7. Dashboard als App auf dem iPhone

Das Dashboard lässt sich wie eine App auf den Home-Bildschirm legen: in Safari
die Seite öffnen → Teilen → **„Zum Home-Bildschirm“**. Es startet dann ohne
Browser-Leisten, mit schwarzer Statusleiste und dem FW-Symbol (`manifest.json`,
`assets/icons/`, `assets/apple-touch-icon.png`). Die Anmeldung hält bis 12 h
nach der letzten Nutzung, auch wenn die App ganz geschlossen wird (das
Sitzungs-Cookie hat eine Laufzeit und wird bei jeder Nutzung erneuert). Am PC
geht dasselbe in Chrome/Edge über „App installieren“.

Bewusst **ohne Offline-Modus** (kein Service Worker): Das Dashboard lädt immer
frisch vom NAS. Ist das NAS nicht erreichbar, steht das oben auf jeder Seite
(mit der Uhrzeit des letzten Stands) – alte Daten sehen nie wie aktuelle aus.

Design (Dunkel/Hell/System) und „Hinweis im Browser bei Störung“ stellt jedes
Gerät unter Einstellungen → „Dieses Gerät“ für sich ein (gespeichert im
Browser, nicht auf dem NAS).

### Ausweg und alte Adresse (`classic.php`)

Startet die Oberfläche nicht oder stürzt ein Teil ab, zeigt sie selbst „Neu
laden“ an – nie eine leere Seite.

Die alte Ansicht ist entfernt: `classic.php` leitet nur noch auf `index.php`
um, damit alte Lesezeichen weiter ankommen.

Entfallen sind `next.php`, `assets/app.js`, `assets/style.css` und der Ordner
`assets/classic/`. Ein Kopieren aufs NAS löscht sie nicht – nach dem Update
dort von Hand entfernen.

## Design

Der ESP32 sendet Status per Formular-POST an `api/status.php` und pollt
`api/poll.php` auf Befehle (alle 10 s, 4 s Timeout, 30 s Backoff bei Fehlern).
Die Poll-Antwort ist ein kleines Textprotokoll:

```text
ok=1
nonce=esp32nonce
id=17
type=TEST
keys_version=12
```

Ohne Befehl kommt `id=0` und `type=NONE`. Die `nonce` wird vom ESP32 im Poll
mitgeschickt und vom NAS gespiegelt; Antworten mit falscher Nonce werden
verworfen, damit alte Poll-Antworten nicht versehentlich erneut wirken. Ein neuer
Befehl liegt als `pending` in `command_state.json.php`. Beim Poll wird er unter
Dateisperre sofort als `delivered` markiert und höchstens einmal ausgeliefert
(at-most-once); der
ESP32 quittiert danach `api/ack.php`. Alte, quittierte oder bereits ausgelieferte
Befehle werden nicht erneut gesendet, auch nicht nach NAS-Neustart. Diese
Entscheidung schützt vor Replay-Doppelalarmen. Wenn die ACK verloren geht, wird
nicht heimlich erneut ausgelöst; das Dashboard zeigt den offenen Zustand, und ein
neuer manueller Befehl bekommt eine neue monotone ID. Ein `pending`-Befehl, den
der ESP32 nicht innerhalb `pending_command_ttl_seconds` (Default 300 s) abholt,
verfällt automatisch als `expired` und lässt sich im Dashboard auch aktiv
abbrechen.

Über das Command-Protokoll laufen **TEST** (leise Bark-Meldung vom ESP32 an den
Status-Empfänger – testet WLAN, NAS-Anbindung und Bark-Versand der Box) und der
**DEMO-ALARM über die Box** (nur im Demo-Modus; das NAS prüft beim Anlegen UND
beim Ausliefern, dass der ESP32 nachweislich die Demo-Liste mit genau einem
Empfänger hat – sonst wird der Befehl verworfen). Der manuelle **REAL ALARM**
wird direkt vom NAS per cURL an Bark gesendet (`level=critical`, an alle
Empfänger parallel, pro Key isoliert mit Retry, HTTP 4xx ohne Retry): Manuelle
Alarmierung muss gerade dann funktionieren, wenn die ESP32-Kette klemmt. Sie
hängt auch nicht am Schreibrecht auf den `data_dir`. Wurde jemand nicht
erreicht, nennt das Dashboard ihn zuerst und bietet an, **nur an die nicht
Erreichten** nachzusenden (das gilt genauso für die ENTWARNUNG).

Zum Einrichten eines neuen Empfängers gibt es in seiner Ansicht (Bereich
„Empfänger“, Person antippen) den **Test-Push**:
eine normale, nicht kritische Mitteilung mit dem Alarmton. Kommt sie mit dem
Alarmton an, stimmen Key und Ton auf dem iPhone – ganz ohne Demo-Modus und
ohne echten Alarm. Das Dashboard fragt vorher nach, weil dabei der Alarmton
spielt.

Die **Empfängerliste** liegt versioniert in `alarm_keys.json.php` im `data_dir`
und wird im Dashboard verwaltet. `poll.php` meldet dem ESP32 bei jedem Poll die
aktuelle `keys_version`; weicht sie von seiner ab, holt er die Liste über
`api/keys.php` (gleiches Text-Protokoll, Token-Auth, gespiegelte Nonce) und
speichert sie im NVS-Flash. Leere oder unplausible Listen verwirft er, während
eines offenen Alarms synchronisiert er nicht, und eine Übernahme meldet er dem
Owner per Statusmeldung, sobald sich die Empfänger selbst geändert haben
(reine Stumm-Wechsel laufen still). `poll.php` beendet vor jeder Antwort auch
überfällige Stummschaltungen – so erreicht das Sicherheitsnetz (Standard
12 h, im Dashboard unter Einstellungen → „Automatisch wieder laut nach“
einstellbar) den Relaisweg auch ohne Cron-Wächter.

Der **Demo-Modus** (Einstellungen → Demo-Modus, nur Admin) leitet ALLE Alarme –
Relaisalarm über den ESP32 und REAL ALARM vom NAS – auf genau einen
Test-Empfänger aus der Liste um. Er nutzt denselben Mechanismus: `demo_mode.json.php`
im `data_dir` merkt sich den Zustand, `api/keys.php` liefert im Demo-Modus nur
den Demo-Key, und jeder Moduswechsel erhöht die `keys_version`, sodass der ESP32
die passende Liste beim nächsten Poll übernimmt (Firmware unverändert). Das
Dashboard zeigt auf jeder Seite ein oranges Band, solange Demo aktiv ist, inklusive der Info, ob
der ESP32 die Demo-Liste schon übernommen hat – **vorher nicht testen**, sonst
alarmiert ein Relaistest noch alle. Der aktive Demo-Empfänger ist nicht löschbar,
und der Demo-Empfänger muss aus der gepflegten Liste stammen (dadurch gibt es
beim Zurückschalten garantiert wieder eine nicht-leere Liste). Nach dem Testen
zurück auf LIVE schalten – das Band erinnert daran.

TLS läuft über HTTPS mit Let's Encrypt auf der Synology. Auf dem ESP32 wird wie
beim bestehenden Bark-Pfad `setInsecure()` verwendet: Das ist auf Mikrocontrollern
robuster gegen Root-CA-/Uhrzeit-Probleme. Die Vertraulichkeit hängt dadurch stärker
am schwer erratbaren Maschinen-Token (nur per POST akzeptiert); das Dashboard
selbst nutzt eine separate Session-Anmeldung, Passwort-Hash und CSRF-Schutz.
