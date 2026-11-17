# Server des Dashboards (Oracle-VM)

Seit 11/2026 läuft das Dashboard (`nas_dashboard/`, PHP) nicht mehr auf dem
Synology-NAS, sondern auf einer kostenlosen Oracle-VM und ist unter
**`https://alarm.tunero.dev/`** erreichbar. Dieser Ordner enthält alles, um
die VM einzurichten, einen Stand einzuspielen und den Aufbau zu prüfen.

```
Box (ESP32) ──► NAS-Brücke (bis zum Flashen) ──┐
Kurzbefehl-Links (alte Adresse) ──► NAS-Brücke ─┤
Browser, neue Links ────────────────────────────┴─► Cloudflare ══Tunnel══► VM
                                                      nginx 127.0.0.1:8080 → PHP-FPM (fwalarm)
NAS (DSM-Aufgabe fw-alarm-watch) ──► prüft den Server alle 5 min von außen
```

- Die VM hat **keinen offenen Port** außer SSH. Der einzige Weg zum
  Dashboard ist der Cloudflare Tunnel (`cloudflared` baut ihn von innen auf).
- Das NAS reicht die Aufrufe der Box und alte Kurzbefehl-Links weiter
  (`nas_bridge/README.md`) und überwacht den Server von außen
  (`cloud_watcher/README.md`).
- Bark-Alarme der Box laufen wie immer direkt zu Bark – unabhängig vom
  Server.

## Die VM

| | |
|---|---|
| Anbieter | Oracle Cloud, Always Free, Region Frankfurt, Instanz `fw-alarm-watch` |
| Größe | VM.Standard.A1.Flex, 1 OCPU, **1 GB** Speicher + 1 GB Auslagerungsdatei |
| System | Ubuntu 24.04 (aarch64), Zeitzone Europe/Berlin, Sicherheits-Updates automatisch (Neustart bei Bedarf 04:30) |
| Zugang | `ssh -i ~/.ssh/oci_fw_alarm ubuntu@<öffentliche IP>` – die IP steht in der Oracle-Konsole (sie kann sich ändern) |

**Warum nur 1 GB:** Oracle stoppt Always-Free-VMs, die es für unbenutzt hält
(über 7 Tage CPU, Netz **und** Speicher unter 20 %). Mit 2 GB lag die
Speicherauslastung bei ca. 21 %, mit 1 GB bei ca. 40 %. Die
Auslagerungsdatei fängt Spitzen ab. Nicht auf „Pay As You Go“ umstellen –
das Konto bleibt kostenlos, solange es ein Free-Tier-Konto ist. Fällt die VM
trotzdem aus, meldet das der Außen-Wächter auf dem NAS; in der Konsole
unter Compute → Instances lässt sie sich wieder starten.

## Wo was liegt

| Pfad | Inhalt | Besitzer / Rechte |
|---|---|---|
| `/srv/fw-alarm/app` | Webroot = `nas_dashboard/` ohne `SETUP.md`, `config.example.php`, `data/`, `webassets.go` | root, lesbar |
| `/srv/fw-alarm/app/config.php` | Konfiguration mit Geheimnissen – liegt **nur hier**, wird beim Einspielen nie angefasst | root:fwalarm 0640 |
| `/srv/fw-alarm/data` | Laufzeitdaten (Empfängerliste, Verlauf, Zustände) | fwalarm 0750 |
| `/srv/fw-alarm/sessions` | PHP-Sitzungen (eigener Ordner, sonst endete das 12-h-Login nach 24 min) | fwalarm 0700 |
| `/srv/fw-alarm/deployed-rev` | welcher Git-Stand eingespielt ist und wann | root |
| `/var/log/fw-alarm/php.log` | PHP-Fehlerlog (8 Wochen) | fwalarm |
| `/var/log/nginx/fw-alarm.access.log` | Zugriffe: Zeit, Methode, **Pfad ohne Query**, Status, Dauer, `bridge=1` für Aufrufe über die NAS-Brücke | 14 Tage |
| `/etc/cloudflared/` | `config.yml` und die Zugangsdatei des Tunnels (`<UUID>.json`, root 0600) | root |

Dienste: `nginx`, `php8.3-fpm` (Pool `fw-alarm`, Benutzer `fwalarm`),
`cloudflared`, `fw-alarm-cron.timer` (Wächter `cron/check_offline.php` alle
15 min).

`config.php` enthält gegenüber der Vorlage diese Werte für den Betrieb
hinter dem Tunnel:

```php
'data_dir' => '/srv/fw-alarm/data',
'public_base_url' => 'https://alarm.tunero.dev/',
'health_check_url' => 'http://127.0.0.1:8080/api/health.php',
'trusted_proxies' => ['127.0.0.1', '::1'],
'proxy_client_ip_header' => 'CF-Connecting-IP',
```

Ohne die letzten beiden zählte die Login-Bremse alle Besucher als eine
Adresse, und das Sitzungs-Cookie ginge ohne `Secure` raus.

## Einrichten (einmalig, wiederholbar)

```bash
ssh -i ~/.ssh/oci_fw_alarm ubuntu@<IP> 'rm -rf ~/fw-server'
scp -i ~/.ssh/oci_fw_alarm -r server ubuntu@<IP>:fw-server
ssh -i ~/.ssh/oci_fw_alarm ubuntu@<IP> 'sudo bash ~/fw-server/setup.sh'
```

Immer in den **eigenen Home-Ordner** kopieren, nie nach `/tmp`: Was root
ausführt (oder was Geheimnisse enthält, z. B. eine `config.php` auf dem Weg
an ihren Platz), darf nicht dort liegen, wo ein anderer Benutzer der VM
Dateien unterschieben oder mitlesen kann. `setup.sh` verweigert den Start aus
`/tmp`.

`setup.sh` installiert die Pakete, legt Benutzer, Ordner und die
Auslagerungsdatei an, richtet Nginx, den PHP-FPM-Pool, den Wächter-Timer
und logrotate ein und prüft am Ende, dass Nginx nur auf `127.0.0.1:8080`
hört und SSH keine Passwort-Anmeldung erlaubt. Ein zweiter Lauf ändert
nichts (er startet nur PHP-FPM neu). Es fasst weder `config.php` noch Daten
an und schaltet den Wächter-Timer weder ein noch aus
(`sudo systemctl enable --now fw-alarm-cron.timer`).

### Tunnel

```bash
cloudflared tunnel login          # gibt eine Adresse aus: im Browser öffnen, Zone tunero.dev freigeben
cloudflared tunnel create fw-alarm
cloudflared tunnel route dns fw-alarm alarm.tunero.dev
# /etc/cloudflared/config.yml aus cloudflared/config.yml.example (UUID eintragen),
# ~/.cloudflared/<UUID>.json nach /etc/cloudflared/ (root, 0600)
sudo cloudflared service install
rm ~/.cloudflared/cert.pem        # wird nur für create/route gebraucht
```

Die Anmeldedatei `cert.pem` liegt bewusst nicht mehr auf der VM. Für eine
Änderung am Tunnel (neuer Name, weitere Adresse) einfach erneut
`cloudflared tunnel login` ausführen und sie danach wieder löschen.
`cloudflared` aktualisiert sich nicht selbst (per Paket installiert):
`sudo apt-get update && sudo apt-get install --only-upgrade cloudflared &&
sudo systemctl restart cloudflared` – alle paar Monate, Cloudflare
unterstützt Versionen etwa ein Jahr.

### Cloudflare (Zone tunero.dev) – muss so bleiben

- **Bot Fight Mode aus** (lässt sich im Free-Plan nicht pro Adresse
  ausnehmen und blockte sonst Box, NAS und Kurzbefehle).
- SSL/TLS → Edge Certificates: Mindestversion **TLS 1.2** (die Box kann nur
  1.2), „Always Use HTTPS“ an.
- Rules → Configuration Rule „alarm.tunero.dev - keine Browser-Pruefungen“
  (`http.host eq "alarm.tunero.dev"`): Browser Integrity Check aus, Email
  Obfuscation aus, Rocket Loader aus, I'm Under Attack aus, RUM aus.
- Caching → Cache Rule „alarm.tunero.dev - Cache umgehen“: Bypass cache.
- DNS: `alarm` ist ein CNAME auf den Tunnel (proxied). Die übrigen Einträge
  (Hauptadresse = GitHub Pages, Mail) gehören nicht dazu.

HSTS setzt die App selbst. Cloudflare beendet TLS und sieht damit
Maschinen-Token, Bark-Keys und Stumm-Tokens im Klartext (bewusst in Kauf
genommen).

## Einspielen (Deploy)

Vom PC, im Repo (Git Bash):

```bash
git push origin main
bash server/deploy.sh HEAD
bash server/check.sh public https://alarm.tunero.dev
```

Die Adresse der VM steht bewusst nicht im (öffentlichen) Repo. `deploy.sh`
liest sie aus `FW_DEPLOY_HOST` oder aus der lokalen, nicht versionierten Datei
`server/deploy.local`:

```
FW_DEPLOY_HOST=ubuntu@<IP der VM>
```

`deploy.sh` spielt genau den genannten Git-Stand von `nas_dashboard/` ein
(nur was auf `origin/main` liegt), lädt PHP-FPM neu und vergleicht danach
die Prüfsumme **jeder** Datei auf dem Server mit Git:
`deployed <sha>: N files, 0 mismatches`. Ein laufender „Alarm an alle“
darf beim Neuladen ausreden (bis 40 s). Zurück auf einen älteren Stand:
`bash server/deploy.sh <älterer-commit>`.

Bevor sich auf dem Server etwas ändert, holt `deploy.sh` eine **Sicherung**
von Daten und `config.php` auf diesen PC (`server/backup.sh`, siehe
„Sicherung und Rückweg“). Scheitert sie, wird trotzdem eingespielt – der
Lauf endet dann mit einer Warnung und Rückgabe 3; danach
`bash server/backup.sh` von Hand wiederholen.

`check.sh` prüft, ohne etwas auszulösen: Anmeldeseite, Sicherheits-Kopfzeilen,
gesperrte Pfade (`config.php`, `lib/`, `cron/`, `data/`), Dateitypen, die
Antwort auf einen Aufruf wie von der Box (TLS 1.2, kein `Accept`) und auf
einen Kurzbefehl ohne `Accept` – beides muss die App selbst beantworten,
nicht Cloudflare. Auf der VM zusätzlich Selbsttest und Sitzungsordner:
`sudo bash ~/fw-server/check.sh local`.

## Betrieb

```bash
cat /srv/fw-alarm/deployed-rev                              # was läuft?
systemctl status nginx php8.3-fpm cloudflared fw-alarm-cron.timer
journalctl -u fw-alarm-cron.service -n 20                   # letzte Läufe des Wächters
journalctl -u cloudflared -n 50                             # Tunnel
sudo tail -f /var/log/nginx/fw-alarm.access.log             # Zugriffe (bridge=1 = über das NAS)
sudo tail -n 50 /var/log/fw-alarm/php.log                   # PHP-Fehler
sudo -u fwalarm php /srv/fw-alarm/app/cron/check_offline.php   # Wächter von Hand
```

| Meldung / Anzeige | Wo nachsehen |
|---|---|
| Bark „Alarm-Server NICHT ERREICHBAR“ (vom NAS) | Oracle-Konsole: läuft die Instanz? Dann `systemctl status cloudflared nginx php8.3-fpm` |
| Bark „Dashboard DEFEKT“ | Die Meldung nennt den Schritt: Datenordner → `sudo chown -R fwalarm: /srv/fw-alarm/data`; Webserver → `systemctl status nginx php8.3-fpm`. Danach `sudo bash ~/fw-server/check.sh local` |
| Bark „Aussen-Waechter meldet sich nicht“ | NAS zu Hause: Strom/Internet, DSM → Aufgabenplaner → `fw-alarm-watch` |
| Bark „Alarm-Server-Waechter laeuft nicht“ (vom NAS) | `systemctl status fw-alarm-cron.timer`, `journalctl -u fw-alarm-cron.service` – oder der Datenordner ist für `fwalarm` nicht beschreibbar (siehe unten) |
| Bark „Alarm-Box OFFLINE“ | Bis zum Flashen meldet sich die Box über das NAS zu Hause. Die Meldung sagt, ob vermutlich das NAS die Ursache ist (dann schweigt auch der Außen-Wächter). Kommt der tägliche Heartbeat der Box noch, läuft sie und alarmiert weiter. |
| Dashboard: „Offline-Wächter (Server)“ läuft nicht | `systemctl status fw-alarm-cron.timer`, `journalctl -u fw-alarm-cron.service` |
| Box im Dashboard offline, Heartbeat kommt aber | bis zum Flashen: läuft die Brücke auf dem NAS? (`nas_bridge/README.md`) |

### Bekannte Grenzen

- **Der Wächter läuft als `fwalarm`, nicht als root.** Kann `fwalarm` den
  Datenordner nicht beschreiben (falscher Besitzer nach einer Wiederherstellung,
  Platte voll), scheitert auch der Wächter selbst an seiner Sperrdatei – dann
  kommt weder „Dashboard DEFEKT“ noch „Box OFFLINE“. Auffangnetz: Der
  Außen-Wächter auf dem NAS meldet nach 45 min „Alarm-Server-Waechter laeuft
  nicht“. Nach jedem Eingriff in `/srv/fw-alarm/data` deshalb
  `sudo bash ~/fw-server/check.sh local` laufen lassen (Fall `health`).
- **Geheim-Tokens in Protokollen:** Ist Nginx oder PHP-FPM gerade nicht
  erreichbar, schreibt `cloudflared` die ganze Adresse eines Aufrufs ins
  Journal und Nginx ins Fehlerprotokoll – bei einem Kurzbefehl-Link samt
  Token. Beide sind nur für root und die Gruppe `adm` lesbar; das
  Zugriffsprotokoll enthält nie eine Query.
- **`setup.sh` startet PHP-FPM neu** – nicht während eines laufenden „Alarm an
  alle“ ausführen (`deploy.sh` lädt dagegen schonend neu).

## Sicherheit

- **Konten:** Wer das Cloudflare-Konto hat, sieht jedes Passwort und Token auf
  dem Weg zum Dashboard und bestimmt, wohin `alarm.tunero.dev` zeigt – dort,
  bei Oracle und bei GitHub zweistufige Anmeldung benutzen.
- **SSH-Schlüssel:** `~/.ssh/oci_fw_alarm` ist root auf der VM
  (`ubuntu` hat sudo ohne Passwort). Mit Passphrase schützen
  (`ssh-keygen -p -f ~/.ssh/oci_fw_alarm`).
- **Öffentliches Repo:** keine echten Adressen (NAS, VM) in versionierten
  Dateien – Platzhalter verwenden.
- Von außen ist auf der VM nur SSH erreichbar (nur mit Schlüssel);
  `setup.sh` prüft das bei jedem Lauf.

## Sicherung und Rückweg

Die Empfängerliste (Bark-Keys und die Kurzbefehl-Tokens der Kollegen), der
Verlauf, die Einstellungen und `config.php` liegen nur auf der VM. Geht sie
verloren, braucht ohne Sicherung jeder Kollege neue Kurzbefehl-Links. Die Box
alarmiert in jedem Fall weiter (sie hat ihre Liste selbst gespeichert).

### Kopie auf dem PC

```bash
bash server/backup.sh
```

holt `/srv/fw-alarm/data` und `config.php` als ein Archiv nach
`~/fw-alarm-backups` (`fw-alarm-<Zeit in UTC>.tgz`; die letzten 30 bleiben,
`FW_BACKUP_KEEP`). `deploy.sh` macht das vor jedem Einspielen von selbst;
nach einer Änderung an den Empfängern lohnt ein Aufruf von Hand. Ein Archiv
wird erst nach einer Prüfung abgelegt (lesbar, Empfängerliste und
`config.php` enthalten) – scheitert sie, bleibt nichts liegen und keine
ältere Sicherung wird gelöscht.

Das Archiv enthält **Geheimnisse** (alle Bark-Keys, Tokens, Passwort-Hashes):
Es liegt im eigenen Benutzerordner, nie im Repo (das Skript bricht dann ab)
und in keinem Ordner, der in eine Cloud abgeglichen wird.

### Wöchentliche Sicherung des Boot-Volumes (Oracle)

In der Oracle-Konsole (Storage → Block Storage → Backup Policies) gibt es die
Richtlinie `fw-alarm-weekly`: jeden Montag 00:00 UTC eine inkrementelle
Sicherung, Aufbewahrung 15 Tage. Sie ist dem Boot-Volume
`fw-alarm-watch (Boot Volume)` zugewiesen; die Sicherungen stehen unter
„Boot Volume Backups“. Dort liegt auch die manuelle Sicherung von vor dem
Umzug (`pre-move-2026-11`).

Always Free erlaubt insgesamt **5** Volume-Sicherungen. Mit 15 Tagen liegen
höchstens 3 automatische dort, dazu die manuelle. Die Aufbewahrung deshalb
nicht verlängern und keine weiteren manuellen Sicherungen ansammeln – über
der Grenze scheitern die automatischen, ohne dass es jemand merkt.

Aus einer Sicherung lässt sich in der Konsole ein neues Boot-Volume erzeugen
und damit eine Instanz starten. Sie bekommt eine neue öffentliche Adresse
(`server/deploy.local` anpassen); der Tunnel verbindet sich von selbst wieder.
Die Daten sind dann so alt wie die Sicherung – danach die jüngste Kopie vom
PC zurückspielen (nächster Abschnitt).

### Zurückspielen

Auf einer neuen VM zuerst „Einrichten“, Tunnel und ein `deploy.sh`. Dann:

```bash
# vom PC: Archiv in einen eigenen Ordner auf der VM (nie /tmp)
ssh -i ~/.ssh/oci_fw_alarm ubuntu@<IP> 'install -d -m 700 ~/fw-restore'
scp -i ~/.ssh/oci_fw_alarm ~/fw-alarm-backups/fw-alarm-<Zeit>.tgz ubuntu@<IP>:fw-restore/

# auf der VM
sudo systemctl stop nginx fw-alarm-cron.timer        # niemand schreibt mehr
sudo mv /srv/fw-alarm/data /srv/fw-alarm/data.alt    # den jetzigen Stand aufheben
sudo tar -xzf ~/fw-restore/fw-alarm-<Zeit>.tgz -C /srv/fw-alarm
sudo chown -R fwalarm:fwalarm /srv/fw-alarm/data && sudo chmod 750 /srv/fw-alarm/data
sudo chown root:fwalarm /srv/fw-alarm/app/config.php && sudo chmod 640 /srv/fw-alarm/app/config.php
rm -rf ~/fw-restore
sudo systemctl start nginx fw-alarm-cron.timer
sudo bash ~/fw-server/check.sh local
```

Danach im Dashboard die Empfänger ansehen: Die Stumm-Zustände sind die vom
Zeitpunkt der Sicherung. Einmal den eigenen Eintrag stumm und wieder laut
schalten – das erhöht die Version der Liste, und die Box holt sie sicher neu.
`data.alt` löschen, wenn alles stimmt.

### Rückzug auf das NAS

Ein Rückzug hieße: Brücke anhalten (`bridge_hold.php`), die Daten aus einer
Sicherung in den Datenordner des NAS kopieren, die alten Dashboard-Dateien
aus Git (`bf1a7c8`) samt einer `config.php` wieder einspielen, DSM-Aufgabe
`check_offline.php` wieder anlegen. Auf dem NAS selbst liegen seit dem
Aufräumen (11/2026) weder alte Daten noch eine `config.php`: Beides kommt aus
einer Sicherung, und in der `config.php` von dort stehen die Pfade der VM
(`data_dir`, `health_check_url`) – die müssen auf das NAS umgestellt werden.
