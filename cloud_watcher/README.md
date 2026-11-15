# Außen-Wächter (Cloud-VM)

Der Cron-Wächter auf dem NAS (`nas_dashboard/cron/check_offline.php`) meldet,
wenn die Box schweigt. Fällt aber das **NAS selbst** aus – Strom oder Internet
am Standort, Router, DDNS, Zertifikat, Web Station –, fällt er mit aus und
meldet nichts. Dann geht auch der manuelle REAL ALARM über das Dashboard nicht.

Der Außen-Wächter schließt diese Lücke von **außen**: Eine kleine Linux-VM
(hier Oracle Cloud „Always Free“, Region Frankfurt) ruft alle 5 Minuten
`api/alive.php` über die öffentliche Adresse des NAS auf und meldet per Bark an
den Status-Key:

| Meldung | Wann | Danach |
|---|---|---|
| `NAS NICHT ERREICHBAR` | nach 3 Fehlschlägen in Folge (ca. 10–15 min) | Erinnerung `NAS weiterhin nicht erreichbar` alle 24 h, einmal `NAS wieder erreichbar` (mit Dauer und letztem Fehler) |
| `NAS-Waechter laeuft nicht` | die DSM-Aufgabe `check_offline.php` lief > 45 min nicht (dann würde ein Ausfall der Box nicht gemeldet) | Erinnerung alle 24 h, einmal `NAS-Waechter laeuft wieder` |
| `NAS-Waechter meldet nichts` | in `config.php` des NAS fehlt `bark_key_status` – dann meldet der NAS-Wächter bewusst gar nichts (auch keinen Ausfall der Box) | wie oben |
| `NAS kann Bark nicht erreichen` | die letzte Meldung des NAS-Wächters an Bark ist gescheitert und seitdem ging keine durch (3 Läufe in Folge) – z. B. ausgehendes Internet/DNS des NAS gestört; dann geht auch der manuelle REAL ALARM nicht raus | Erinnerung `NAS kann Bark weiterhin nicht erreichen` alle 24 h, einmal `NAS erreicht Bark wieder` |
| `Aussen-Waechter kann wieder melden` | eine Meldung der VM selbst kam nicht bei Bark an, hat sich aber erledigt, bevor ein neuer Versuch durchging | – |

`NAS-Waechter meldet nichts` und `NAS kann Bark nicht erreichen` brauchen ein
aktuelles `api/alive.php` (Antwortzeilen `cron_configured`/`nas_send_failed`);
mit einem älteren entfallen sie, und ein fehlender `bark_key_status` erscheint
wie bisher als „NAS-Waechter laeuft nicht … noch nie“. Eine Meldung gilt erst
als erledigt, wenn Bark sie angenommen hat (HTTP 200) – sonst versucht es der
nächste Lauf erneut. Scheitert der Versand der VM, erfährt das NAS es beim
nächsten Aufruf (`bark_failed_at`), und das Dashboard warnt in der Zeile
„Außen-Wächter (Cloud)“: „… kann aber nicht melden“.

**Einstellungen:** Für die VM gelten nur `BARK_KEY` und `BARK_LEVEL` (alle
Meldungen gleich dringend) sowie die Schwellen in `/etc/fw-alarm-watch.conf`.
Ruhezeit, Stellvertreter, Ton und Dringlichkeit aus dem Dashboard-Panel
„Meldungen an den Admin“ gelten **nicht** für sie – die VM liest die
Einstellungen des NAS nicht (sie soll ja gerade dann melden, wenn das NAS weg
ist).

**Gegenseitig:** `alive.php` merkt sich jeden Aufruf. Verstummt die VM – Oracle
stoppt kostenlose VMs, die eine Woche lang „idle“ sind –, meldet das der
Cron-Wächter auf dem NAS („Aussen-Waechter meldet sich nicht“, einstellbar im
Dashboard unter „Meldungen an den Admin“). Das Dashboard zeigt den Zustand in
„Box & Wartung“ als Zeile „Außen-Wächter (Cloud)“.

Bark-Alarme der Box laufen davon völlig unabhängig. Der Außen-Wächter ist –
wie das ganze Projekt – ein **Zusatz, kein Ersatz**.

## Dateien

| Datei | Ziel auf der VM |
|---|---|
| `fw-alarm-watch.sh` | `/usr/local/bin/fw-alarm-watch` (755, root) |
| `fw-alarm-watch.service` | `/etc/systemd/system/` |
| `fw-alarm-watch.timer` | `/etc/systemd/system/` |
| `fw-alarm-watch.conf.example` | Vorlage für `/etc/fw-alarm-watch.conf` (640, root:fwwatch) – **enthält Geheimnisse, nie ins Git** |

Die Dateien müssen LF-Zeilenenden haben. Für die Dateien aus dem Repo sorgt
`.gitattributes` dafür; die selbst ausgefüllte `fw-alarm-watch.conf` steht aber
nicht im Git und hat nach dem Bearbeiten unter Windows meist CRLF. Deshalb auf
der VM vor dem Installieren immer (Schritt 4):

```bash
sed -i 's/\r$//' /tmp/fw-alarm-watch.sh /tmp/fw-alarm-watch.service /tmp/fw-alarm-watch.timer /tmp/fw-alarm-watch.conf
```

Ein vergessenes CR fällt trotzdem auf: Das Skript prüft die Konfiguration beim
Start und bricht mit einem Hinweis im Journal ab.

## Einrichtung

1. **VM anlegen** (Oracle-Konsole → Compute → Instances → Create): Name
   **`fw-alarm-watch`** (die Meldung „Aussen-Waechter meldet sich nicht“ des
   NAS nennt genau diesen Namen), Ubuntu 24.04,
   Shape `VM.Standard.A1.Flex` mit 1 OCPU / 6 GB (Always Free), öffentliches
   Subnetz mit öffentlicher IPv4, eigener SSH-Public-Key. Meldet Oracle „Out of
   capacity“, eine andere Availability Domain probieren (oder den Shape
   `VM.Standard.E2.1.Micro`, ebenfalls kostenlos).
2. **Token erzeugen** (Zeile 1 = Klartext für die VM, Zeile 2 = Hash für das NAS):
   ```bash
   php -r '$t=bin2hex(random_bytes(32)); echo $t, PHP_EOL, hash("sha256", $t), PHP_EOL;'
   ```
   Den Hash in `config.php` des NAS als `'watcher_token_sha256' => '…',`
   eintragen und `nas_dashboard/api/alive.php` auf das NAS kopieren. Eigenes
   Token, **nicht** das Maschinen-Token der Box – derselbe Hash wie
   `machine_token_sha256` wird mit 404 abgelehnt.
3. **Konfiguration anlegen:** `fw-alarm-watch.conf.example` nach
   `fw-alarm-watch.conf` kopieren (die Kopie ignoriert Git) und ausfüllen:
   `NAS_ALIVE_URL` (öffentliche `https://`-Adresse von `api/alive.php`),
   `WATCHER_TOKEN` (Zeile 1 aus Schritt 2) und `BARK_KEY` (dein Status-Key,
   sinnvollerweise derselbe wie `bark_key_status` in `config.php`). Die
   übrigen Werte passen meist so.
4. **Auf der VM** (als `ubuntu`, die vier Dateien vorher nach `/tmp` kopiert,
   z. B. per `scp`):
   ```bash
   sed -i 's/\r$//' /tmp/fw-alarm-watch.sh /tmp/fw-alarm-watch.service /tmp/fw-alarm-watch.timer /tmp/fw-alarm-watch.conf
   sudo timedatectl set-timezone Europe/Berlin
   sudo useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin fwwatch
   sudo install -m 755 /tmp/fw-alarm-watch.sh /usr/local/bin/fw-alarm-watch
   sudo install -m 644 /tmp/fw-alarm-watch.service /tmp/fw-alarm-watch.timer /etc/systemd/system/
   sudo install -m 640 -o root -g fwwatch /tmp/fw-alarm-watch.conf /etc/fw-alarm-watch.conf
   rm -f /tmp/fw-alarm-watch.conf                              # enthält Token + Bark-Key
   # Zustandsverzeichnis gleich anlegen (sonst erst beim ersten Timer-Lauf),
   # damit die Probe-Meldung schon jetzt funktioniert:
   sudo install -d -m 700 -o fwwatch -g fwwatch /var/lib/fw-alarm-watch
   sudo systemctl daemon-reload
   sudo -u fwwatch /usr/local/bin/fw-alarm-watch --test     # Probe-Meldung an Bark
   sudo systemctl start fw-alarm-watch.service               # ein echter Prüflauf
   journalctl -u fw-alarm-watch.service -n 5                 # muss „OK http=200 …“ zeigen
   sudo systemctl enable --now fw-alarm-watch.timer
   ```
   Wer die Konfiguration lieber direkt auf der VM ausfüllt: statt der
   ausgefüllten Kopie die leere Vorlage `fw-alarm-watch.conf.example` mit dem
   `install`-Befehl oben nach `/etc/fw-alarm-watch.conf` legen und mit
   `sudoedit /etc/fw-alarm-watch.conf` ausfüllen – dann liegen die
   Geheimnisse nie in `/tmp`.

   Optional (so eingerichtet): Sicherheitsupdates mit nächtlichem Neustart bei
   Bedarf – `/etc/apt/apt.conf.d/52fw-alarm-reboot` mit
   `Unattended-Upgrade::Automatic-Reboot "true";` und
   `Unattended-Upgrade::Automatic-Reboot-Time "04:30";`.

## Betrieb

```bash
systemctl list-timers fw-alarm-watch.timer          # nächster Lauf
journalctl -u fw-alarm-watch.service -n 20          # letzte Läufe (OK/FAIL + Grund)
systemctl --failed                                  # gescheiterte Läufe (exit 1/2)
sudo cat /var/lib/fw-alarm-watch/state              # Fehlerzähler, Meldezeitpunkte
```

- **Rückgabewerte:** `0` = Lauf erledigt (auch wenn das NAS nicht erreichbar
  war – das steht als `FAIL n: …` im Journal), `1` = Zustand nicht speicherbar
  (Platte voll, Rechte von `/var/lib/fw-alarm-watch`) bzw. Probe-Meldung
  gescheitert, `2` = Konfiguration ungültig (z. B. `http://` statt `https://`,
  Zahl mit Buchstaben, unbekanntes `BARK_LEVEL`, Windows-Zeilenenden). Bei `1`
  und `2` ruft das Skript das NAS gar nicht erst auf: Ein Wächter, der nicht
  sauber arbeiten kann, soll beim NAS als verstummt auffallen („Aussen-Waechter
  meldet sich nicht“), statt sich dort als gesund zu melden.
- **Geheimnisse:** Token und Bark-Key gibt das Skript per `curl -K -` über
  stdin weiter, nicht als Argument – sie tauchen also nicht in `ps` auf. Eine
  Sperre (`/var/lib/fw-alarm-watch/lock`) verhindert, dass ein Handaufruf und
  der Timer gleichzeitig laufen.
- **Kostenlos bleiben:** Nur Always-Free-Ressourcen verwenden (A1 bis 2 OCPU /
  12 GB gesamt, Boot-Volume im 200-GB-Freikontingent). Kein Upgrade auf
  Pay-as-you-go nötig; das Risiko „VM als idle gestoppt“ deckt die gegenseitige
  Überwachung ab. Kommt die Meldung „Aussen-Waechter meldet sich nicht“:
  Oracle-Konsole → Compute → Instances → `fw-alarm-watch` → Start. Läuft die
  VM, liegt es am Skript: `systemctl --failed` und das Journal zeigen den
  Grund (Rückgabewert 1 oder 2, siehe oben).
- **Token wechseln:** neues Paar erzeugen, Hash in `config.php`, Klartext in
  `/etc/fw-alarm-watch.conf` – beides zusammen, sonst meldet die VM „HTTP 403“.
- **Abschalten:** `sudo systemctl disable --now fw-alarm-watch.timer` und
  `watcher_token_sha256` in `config.php` leeren (dann antwortet `alive.php` 404
  und der Cron-Wächter prüft die VM nicht mehr).
