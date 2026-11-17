# NAS-Brücke

Seit dem Umzug läuft das Dashboard auf der Oracle-VM unter
`https://alarm.tunero.dev/`. Zwei Dinge kennen aber weiter die alte Adresse
des NAS (`https://dein-name.synology.me/fw_alarm/…`):

- die **Box** (Adresse steckt in der Firmware, bis sie neu geflasht wird),
- die **Kurzbefehl-Links** der Kollegen (`api/mute.php?t=…`).

Die Brücke ist ein kleiner Satz PHP-Dateien, der im Web-Ordner des NAS an
die Stelle des Dashboards tritt und genau diese Aufrufe serverseitig an den
neuen Server weiterreicht. Sie hält **keine Daten und keine Geheimnisse**
(Token und Keys gehen nur durch) und ist **kein offener Proxy**: Das Ziel
steht fest in `bridge_config.php`, weitergereicht werden nur fünf Dateien.

Weiterleitungen (302/307) wären einfacher, gehen aber nicht: Die Box folgt
keinen, und ob jeder Kurzbefehl einer folgt, ist ungewiss.

## Was die Brücke tut

| Aufruf am NAS | Verhalten |
|---|---|
| `api/status.php`, `api/poll.php`, `api/ack.php`, `api/keys.php` | weiterreichen (Box) |
| `api/mute.php` | weiterreichen (alte Kurzbefehl-Links, auch die Bestätigungsseite im Browser) |
| `index.php`, `classic.php` | 302 auf die neue Adresse (bewusst nicht 301 – der Rückweg bliebe sonst im Browser versperrt) |
| `dashboard_api.php` | 410 mit `{"ok":false,"code":"moved",…}` (in der Wartung 503) |
| alles andere | gibt es nicht (404) |

Weitergereicht werden Methode (GET/POST/HEAD), Query, Rumpf (höchstens
16 KB, sonst 413) und nur diese Kopfzeilen: `Content-Type`, `Accept`,
`Accept-Language`, `User-Agent`, `Sec-Fetch-Dest`, `Sec-Fetch-Mode`,
`Sec-Fetch-Site`, `Sec-Purpose`, `Purpose`, `X-Purpose`, `X-Moz` – dazu
`X-FW-Bridge: 1`, damit das Zugriffsprotokoll des Servers zeigt, was über
die Brücke kam (`bridge=1`). Zurück gehen Statuscode, Rumpf und
`Content-Type`, `Location`, `Cache-Control`, `Content-Security-Policy`,
`X-Content-Type-Options`, `Referrer-Policy`, `X-Frame-Options`. Cookies
werden in keiner Richtung durchgereicht. Ein Rumpf als
`multipart/form-data` wird als `application/x-www-form-urlencoded` neu
aufgebaut.

Zeitlimits: Verbindung 3 s, gesamt 3,5 s – unter den 4 s, die die Box
wartet. Ist der Server nicht erreichbar, antwortet die Brücke mit 502 bzw.
504 (Zeitlimit) und **nie mit 403**: Ein 403 wertet die Box als „Token
abgelehnt“ und warnt per Bark. Fängt Cloudflare eine Anfrage ab
(Kopfzeile `cf-mitigated`) oder kommt sonst ein 403, das nicht von der App
stammt (Sperrseite, Nginx), meldet die Brücke deshalb ebenfalls 502. Ein
403 gibt es nur, wenn die App selbst so antwortet (falsches Token – daran
erkannt, dass die Antwort genau `FORBIDDEN` lautet).
Ursachen stehen im PHP-Fehlerlog des NAS (Zeilen mit `fw-bridge:`).

## Dateien auf dem NAS (`\\<NAS>\web\fw_alarm\`)

Aus diesem Ordner (per `git archive`, Zeilenenden LF):

```
lib/bridge.php
api/status.php  api/poll.php  api/ack.php  api/keys.php  api/mute.php
dashboard_api.php
index.php  classic.php
```

Dazu von Hand:

- `bridge_config.php` – Kopie von `bridge_config.example.php` (liegt nur auf
  dem NAS): `target`, `public_url`, `held_log`.
- `bridge_hold.php` – **nur während einer Wartung**, siehe unten.

Vom alten Dashboard **bleiben** auf dem NAS, weil die Bestätigungsseite des
Mute-Links sie relativ lädt: `assets/app.css`, `assets/favicon.svg`,
`assets/app/theme-boot.js`. Alles andere vom alten Dashboard (auch die alte
`config.php`) ist seit 11/2026 gelöscht. Der alte Datenordner
(`/volume1/web_packages/fw_alarm_data`) ist leer, **muss aber bleiben**: Dort
schreibt die Brücke ihr Halt-Protokoll (`held_log`), und sie legt den Ordner
nicht selbst an – ohne ihn gingen im Halt abgewiesene Mute-Aufrufe verloren.

## Reihenfolge beim Ausrollen

Die Box fragt alle 10 s an, die alten Dateien werden einzeln ersetzt:

1. `bridge_hold.php`, `lib/bridge.php`, `bridge_config.php`
2. `api/status.php`, `api/poll.php`, `api/ack.php`, `api/keys.php`,
   `api/mute.php`
3. `dashboard_api.php`
4. später (wenn der neue Server läuft): `index.php`, `classic.php`

So antwortet jede ersetzte Datei sofort mit „Wartung“ und nichts wird
weitergereicht, bevor der neue Server die Daten hat.

## Wartung („Halt“)

Liegt `bridge_hold.php` im Ordner der Brücke, antworten alle Brücken-Pfade
mit **503**, ohne weiterzureichen – die Box geht in ihren Fehler-Backoff,
niemand schreibt mehr. Es zählt allein, ob die Datei da ist (bei jedem
Aufruf frisch geprüft, ohne PHP-Zwischenspeicher): anlegen = Halt, löschen =
weiter. Inhalt der Datei (damit sie selbst nichts verrät):

```php
<?php
http_response_code(404);
```

Die Datei ist per `.gitignore` gesperrt und darf nie mit ausgerollt werden.

### Im Halt abgewiesene Mute-Aufrufe

Ein Kollege, dessen Automatik „Feierabend → laut“ in die Wartung fällt,
bliebe sonst unbemerkt stumm. Die Brücke merkt sich deshalb jeden im Halt
abgewiesenen Mute-Aufruf in der Datei `held_log` – eine Zeile, durch Tabs
getrennt:

```
<Unix-Zeit>	<t|key>	<erste 12 Hex-Zeichen von sha256(Token bzw. Key)>	<on|off|?>
```

Nie das Token selbst. Nicht gemerkt werden Aufrufe, die ohnehin nichts
geschaltet hätten: Link-Vorschauen (HEAD, Messenger, Vorab-Abrufe des
Browsers – gleiche Regeln wie in `nas_dashboard/api/mute.php`) und Aufrufe
ohne Token/Key.

**Nachholen** nach der Wartung, auf dem Server: Für jede Zeile den
Empfänger suchen, dessen `sha256(mute_token)` (bei `t`) bzw. `sha256(key)`
(bei `key`) mit dem Fingerabdruck beginnt; pro Empfänger zählt der
**letzte** gemerkte Zustand – aber nur, wenn er jünger ist als die letzte
Stumm-Änderung dieses Empfängers im Verlauf (hat der Kollege es später
selbst noch einmal versucht, gilt das). Dann auf dem Server ausführen, damit
das Token ihn nie verlässt:

```bash
curl -s http://127.0.0.1:8080/api/mute.php --data-urlencode t=<Token> --data-urlencode state=<on|off>
```

Danach die Datei `held_log` löschen.

## Tests

```bash
node --test "tests/bridge/*.test.mjs"
```

Startet die Brücke und eine Gegenstelle (`tests/bridge/upstream.php`) als
zwei lokale PHP-Server und prüft Weiterreichen, Kopfzeilen, Zeitlimits, Halt
und die Ersatzseiten.

## Nach dem Flashen der Box

Sobald die Box `https://alarm.tunero.dev/api` direkt anspricht (im
Zugriffsprotokoll des Servers stehen `status.php`/`poll.php` 24 h lang ohne
`bridge=1`):

- vom NAS löschen: `api/status.php`, `api/poll.php`, `api/ack.php`,
  `api/keys.php` (die Reste des alten Dashboards sind schon seit 11/2026
  weg);
- es bleiben: `api/mute.php`, `lib/bridge.php`, `bridge_config.php`,
  `index.php`, `classic.php`, `dashboard_api.php`, `assets/app.css`,
  `assets/favicon.svg`, `assets/app/theme-boot.js` und der leere Ordner
  `fw_alarm_data` für das Halt-Protokoll.

`api/mute.php` bleibt dauerhaft: Alte Links laufen von selbst aus, neu
kopierte zeigen auf `alarm.tunero.dev`.
