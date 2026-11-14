# Außen-Wächter (Cloud-VM)

Der Cron-Wächter auf dem NAS (`nas_dashboard/cron/check_offline.php`) meldet,
wenn die Box schweigt. Fällt aber das **NAS selbst** aus – Strom oder Internet
am Standort, Router, DDNS, Zertifikat, Web Station –, fällt er mit aus und
meldet nichts. Dann geht auch der manuelle REAL ALARM über das Dashboard nicht.

Der Außen-Wächter schließt diese Lücke von **außen**: Eine kleine Linux-VM
(hier Oracle Cloud „Always Free“, Region Frankfurt) ruft alle 5 Minuten
`api/alive.php` über die öffentliche Adresse des NAS auf und meldet per Bark an
den Status-Key:

| Meldung | Wann |
|---|---|
| `NAS NICHT ERREICHBAR` | nach 3 Fehlschlägen in Folge (ca. 10–15 min), Erinnerung alle 24 h, einmal „wieder erreichbar“ |
| `NAS-Waechter laeuft nicht` | die DSM-Aufgabe `check_offline.php` lief > 45 min nicht (dann würde ein Ausfall der Box nicht gemeldet) |

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

Die Dateien müssen LF-Zeilenenden haben (`.gitattributes` sorgt dafür; beim
Kopieren von Windows aus sicherheitshalber `tr -d '\r'`).

## Einrichtung

1. **VM anlegen** (Oracle-Konsole → Compute → Instances → Create): Ubuntu 24.04,
   Shape `VM.Standard.A1.Flex` mit 1 OCPU / 6 GB (Always Free), öffentliches
   Subnetz mit öffentlicher IPv4, eigener SSH-Public-Key. Meldet Oracle „Out of
   capacity“, eine andere Availability Domain probieren (oder den Shape
   `VM.Standard.E2.1.Micro`, ebenfalls kostenlos).
2. **Token erzeugen** (Zeile 1 = Klartext für die VM, Zeile 2 = Hash für das NAS):
   ```bash
   php -r '$t=bin2hex(random_bytes(32)); echo $t, PHP_EOL, hash("sha256", $t), PHP_EOL;'
   ```
   Den Hash in `config.php` des NAS als `'watcher_token_sha256' => '…',`
   eintragen und `nas_dashboard/api/alive.php` auf das NAS kopieren.
3. **Auf der VM** (als `ubuntu`, Dateien vorher nach `/tmp` kopiert):
   ```bash
   sudo timedatectl set-timezone Europe/Berlin
   sudo useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin fwwatch
   sudo install -m 755 /tmp/fw-alarm-watch.sh /usr/local/bin/fw-alarm-watch
   sudo install -m 644 /tmp/fw-alarm-watch.service /tmp/fw-alarm-watch.timer /etc/systemd/system/
   sudo install -m 640 -o root -g fwwatch /tmp/fw-alarm-watch.conf /etc/fw-alarm-watch.conf
   sudo systemctl daemon-reload
   sudo -u fwwatch /usr/local/bin/fw-alarm-watch --test     # Probe-Meldung an Bark
   sudo systemctl start fw-alarm-watch.service               # ein echter Prüflauf
   sudo systemctl enable --now fw-alarm-watch.timer
   ```
   Optional (so eingerichtet): Sicherheitsupdates mit nächtlichem Neustart bei
   Bedarf – `/etc/apt/apt.conf.d/52fw-alarm-reboot` mit
   `Unattended-Upgrade::Automatic-Reboot "true";` und
   `Unattended-Upgrade::Automatic-Reboot-Time "04:30";`.

## Betrieb

```bash
systemctl list-timers fw-alarm-watch.timer          # nächster Lauf
journalctl -u fw-alarm-watch.service -n 20          # letzte Läufe (OK/FAIL + Grund)
sudo cat /var/lib/fw-alarm-watch/state              # Fehlerzähler, Meldezeitpunkte
```

- **Kostenlos bleiben:** Nur Always-Free-Ressourcen verwenden (A1 bis 2 OCPU /
  12 GB gesamt, Boot-Volume im 200-GB-Freikontingent). Kein Upgrade auf
  Pay-as-you-go nötig; das Risiko „VM als idle gestoppt“ deckt die gegenseitige
  Überwachung ab. Kommt die Meldung „Aussen-Waechter meldet sich nicht“:
  Oracle-Konsole → Compute → Instances → `fw-alarm-watch` → Start.
- **Token wechseln:** neues Paar erzeugen, Hash in `config.php`, Klartext in
  `/etc/fw-alarm-watch.conf` – beides zusammen, sonst meldet die VM „HTTP 403“.
- **Abschalten:** `sudo systemctl disable --now fw-alarm-watch.timer` und
  `watcher_token_sha256` in `config.php` leeren (dann antwortet `alive.php` 404
  und der Cron-Wächter prüft die VM nicht mehr).
