#!/usr/bin/env bash
# Richtet die Oracle-VM für das FW-Alarm-Dashboard ein: Pakete, Benutzer,
# Ordner, Nginx, PHP-FPM-Pool, Wächter-Timer, Paketquelle für cloudflared.
#
# Aufruf auf der VM (als root), aus einer Kopie des Ordners server/:
#     sudo bash /tmp/fw-server/setup.sh
#
# Wiederholbar: Ein zweiter Lauf ändert nichts mehr (außer einem Neustart
# von PHP-FPM und einem Reload von Nginx). Was das Skript bewusst NICHT tut:
#   - config.php und Daten anfassen (kommen beim Umschalten bzw. bleiben),
#   - den Wächter-Timer ein- oder ausschalten (bleibt, wie er ist),
#   - den Tunnel einrichten (cloudflared tunnel login/create, siehe README).
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
    echo "setup.sh muss als root laufen: sudo bash $0" >&2
    exit 1
fi

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
for f in nginx/fw-alarm.conf php/fw-alarm.conf systemd/fw-alarm-cron.service \
         systemd/fw-alarm-cron.timer logrotate/fw-alarm; do
    if [ ! -f "$here/$f" ]; then
        echo "Datei fehlt neben setup.sh: $f" >&2
        exit 1
    fi
done

step() { printf '\n== %s\n' "$*"; }

export DEBIAN_FRONTEND=noninteractive
# needrestart soll während des Laufs weder nachfragen noch Dienste neu starten.
export NEEDRESTART_SUSPEND=1

PHP_VER=8.3
POOL_DIR="/etc/php/$PHP_VER/fpm/pool.d"

step "Pakete"
apt-get update -q
apt-get install -y -q --no-install-recommends \
    nginx "php$PHP_VER-fpm" "php$PHP_VER-cli" "php$PHP_VER-curl" "php$PHP_VER-mbstring" \
    rsync curl ca-certificates

step "Paketquelle von Cloudflare und cloudflared"
install -d -m 0755 /usr/share/keyrings
if [ ! -s /usr/share/keyrings/cloudflare-main.gpg ]; then
    curl -fsSL https://pkg.cloudflare.com/cloudflare-main.gpg -o /usr/share/keyrings/cloudflare-main.gpg.neu
    mv /usr/share/keyrings/cloudflare-main.gpg.neu /usr/share/keyrings/cloudflare-main.gpg
    chmod 0644 /usr/share/keyrings/cloudflare-main.gpg
fi
cf_list='deb [signed-by=/usr/share/keyrings/cloudflare-main.gpg] https://pkg.cloudflare.com/cloudflared any main'
if [ "$(cat /etc/apt/sources.list.d/cloudflared.list 2>/dev/null || true)" != "$cf_list" ]; then
    printf '%s\n' "$cf_list" > /etc/apt/sources.list.d/cloudflared.list
    apt-get update -q
fi
apt-get install -y -q cloudflared

step "Auslagerungsdatei (1 GB)"
# Die VM hat bewusst nur 1 GB Speicher: Oracle stoppt Always-Free-VMs, deren
# Speicher-, CPU- und Netzauslastung über 7 Tage unter 20 % bleibt - mit
# 2 GB lag die Speicherauslastung genau an dieser Grenze. Die
# Auslagerungsdatei fängt Spitzen ab (apt-Updates, mehrere PHP-Prozesse).
# (Erst einlesen, dann suchen - siehe Hinweis zu pipefail unten.)
swaps="$(swapon --show=NAME --noheadings)"
if ! grep -qx '/swapfile' <<<"$swaps"; then
    if [ ! -f /swapfile ]; then
        dd if=/dev/zero of=/swapfile bs=1M count=1024 status=none
        chmod 600 /swapfile
        mkswap /swapfile >/dev/null
    fi
    swapon /swapfile
fi
if ! grep -q '^/swapfile[[:space:]]' /etc/fstab; then
    printf '/swapfile none swap sw 0 0\n' >> /etc/fstab
fi
# Nur als Sicherheitsnetz: erst auslagern, wenn es wirklich eng wird.
printf 'vm.swappiness = 10\n' > /etc/sysctl.d/60-fw-alarm-swap.conf
sysctl -q -p /etc/sysctl.d/60-fw-alarm-swap.conf

step "Benutzer fwalarm"
if ! id -u fwalarm >/dev/null 2>&1; then
    useradd --system --user-group --no-create-home --home-dir /nonexistent \
        --shell /usr/sbin/nologin fwalarm
fi

step "Ordner"
install -d -o root -g root -m 0755 /srv/fw-alarm /srv/fw-alarm/app
install -d -o fwalarm -g fwalarm -m 0750 /srv/fw-alarm/data
install -d -o fwalarm -g fwalarm -m 0700 /srv/fw-alarm/sessions
install -d -o fwalarm -g fwalarm -m 0750 /var/log/fw-alarm

step "Nginx"
install -o root -g root -m 0644 "$here/nginx/fw-alarm.conf" /etc/nginx/sites-available/fw-alarm
ln -sfn ../sites-available/fw-alarm /etc/nginx/sites-enabled/fw-alarm
# Die Standard-Seite von Ubuntu hört auf Port 80 an allen Adressen - weg damit.
rm -f /etc/nginx/sites-enabled/default
nginx -t
systemctl enable nginx >/dev/null
systemctl reload-or-restart nginx

step "PHP-FPM-Pool fw-alarm"
install -o root -g root -m 0644 "$here/php/fw-alarm.conf" "$POOL_DIR/fw-alarm.conf"
if [ -e "$POOL_DIR/www.conf" ]; then
    mv -f "$POOL_DIR/www.conf" "$POOL_DIR/www.conf.disabled"
fi
"php-fpm$PHP_VER" -t
systemctl enable "php$PHP_VER-fpm" >/dev/null
systemctl restart "php$PHP_VER-fpm"

step "Wächter-Timer (wird hier NICHT eingeschaltet) und logrotate"
install -o root -g root -m 0644 "$here/systemd/fw-alarm-cron.service" /etc/systemd/system/fw-alarm-cron.service
install -o root -g root -m 0644 "$here/systemd/fw-alarm-cron.timer" /etc/systemd/system/fw-alarm-cron.timer
install -o root -g root -m 0644 "$here/logrotate/fw-alarm" /etc/logrotate.d/fw-alarm
systemctl daemon-reload

step "Prüfungen"
# Anmeldung per Passwort muss aus sein (nur SSH-Schlüssel).
# (Erst einlesen, dann suchen: "sshd -T | grep -q" scheitert mit pipefail,
# weil grep nach dem Treffer aufhört zu lesen.)
sshd_conf="$(sshd -T)"
if ! grep -q '^passwordauthentication no' <<<"$sshd_conf"; then
    echo "FEHLER: sshd erlaubt Passwort-Anmeldung (PasswordAuthentication no erwartet)" >&2
    exit 1
fi
# Nginx darf nur auf 127.0.0.1:8080 hören.
sockets="$(ss -ltnpH)"
listen="$(awk '/nginx/ {print $4}' <<<"$sockets" | sort -u | tr '\n' ' ')"
if [ "$listen" != "127.0.0.1:8080 " ]; then
    echo "FEHLER: Nginx hört auf: ${listen:-nichts} (erwartet nur 127.0.0.1:8080)" >&2
    exit 1
fi
if [ ! -S /run/php/fw-alarm.sock ]; then
    echo "FEHLER: Socket /run/php/fw-alarm.sock fehlt" >&2
    exit 1
fi
echo "nginx: $(systemctl is-active nginx), php-fpm: $(systemctl is-active "php$PHP_VER-fpm"), Wächter-Timer: $(systemctl is-enabled fw-alarm-cron.timer 2>/dev/null || true)"
echo "setup.sh fertig."
