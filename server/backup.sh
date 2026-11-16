#!/usr/bin/env bash
# Sichert die Daten des Dashboards und config.php von der Oracle-VM auf
# diesen PC.
#
# Aufruf vom PC (Git Bash), im Repo:
#     bash server/backup.sh
# server/deploy.sh ruft es vor jedem Einspielen selbst auf.
#
# Gesichert werden /srv/fw-alarm/data (Empfängerliste samt Bark-Keys und
# Kurzbefehl-Tokens, Verlauf, Einstellungen) und /srv/fw-alarm/app/config.php.
# Beides gibt es sonst nur auf der VM. Das Archiv enthält also GEHEIMNISSE:
# Es liegt außerhalb des (öffentlichen) Repos im eigenen Benutzerordner und
# gehört in keinen Ordner, der in eine Cloud abgeglichen wird.
#
# Zielordner: FW_BACKUP_DIR  (Standard ~/fw-alarm-backups)
# Anzahl:     FW_BACKUP_KEEP (Standard 30; ältere Sicherungen werden gelöscht)
# VM und Schlüssel wie bei deploy.sh: FW_DEPLOY_HOST bzw. server/deploy.local,
# FW_DEPLOY_KEY (Standard ~/.ssh/oci_fw_alarm).
#
# Ausgabe am Ende:
#     Sicherung: <Datei> (N Dateien) - M Sicherungen im Ordner
# Rückgabe: 0 = gesichert; 1 = gescheitert (es bleibt keine halbe Datei
# liegen, ältere Sicherungen bleiben unberührt); 2 = falsch aufgerufen.
# Zurückspielen: server/README.md, Abschnitt „Sicherung und Rückweg“.
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"

host="${FW_DEPLOY_HOST:-}"
if [ -z "$host" ] && [ -f "$repo/server/deploy.local" ]; then
    host="$(sed -n 's/^FW_DEPLOY_HOST=//p' "$repo/server/deploy.local" | head -n 1 | tr -d '\r" ')"
fi
if ! [[ $host =~ ^[A-Za-z0-9._-]+@[A-Za-z0-9.:_-]+$ ]]; then
    echo "Ziel fehlt: FW_DEPLOY_HOST=ubuntu@<IP der VM> setzen oder in server/deploy.local eintragen." >&2
    exit 2
fi
key="${FW_DEPLOY_KEY:-$HOME/.ssh/oci_fw_alarm}"

keep="${FW_BACKUP_KEEP:-30}"
if ! [[ $keep =~ ^[1-9][0-9]*$ ]]; then
    echo "FW_BACKUP_KEEP muss eine Zahl ab 1 sein (ist: $keep)." >&2
    exit 2
fi

# Zielordner auflösen, OHNE ihn schon anzulegen: Liegt er im Repo, wird
# abgebrochen - das Repo ist öffentlich, ein "git add" nähme das Archiv mit.
dest="${FW_BACKUP_DIR:-$HOME/fw-alarm-backups}"
probe="$dest"
rest=""
while [ ! -d "$probe" ]; do
    rest="/$(basename "$probe")$rest"
    probe="$(dirname "$probe")"
done
dest="$(cd "$probe" && pwd -P)$rest"
case "$dest/" in
    "$repo"/*)
        echo "Abbruch: Der Zielordner $dest liegt im Repo. Sicherungen enthalten Geheimnisse und gehören nicht dorthin (FW_BACKUP_DIR ändern)." >&2
        exit 2
        ;;
esac

ssh_vm() { ssh -i "$key" -o BatchMode=yes -o ConnectTimeout=15 "$host" "$@"; }
fail() {
    echo "FEHLER: Sicherung gescheitert - $*" >&2
    exit 1
}

mkdir -p "$dest"
chmod 700 "$dest" 2>/dev/null || true
part="$dest/.fw-alarm-$$.part"
trap 'rm -f "$part"' EXIT

# tar meldet 1, wenn sich eine Datei ändert, während es sie liest (die Box
# meldet sich alle paar Sekunden) - das Archiv ist dann trotzdem vollständig.
# Ob wirklich alles drin ist, wird gleich am Archiv selbst geprüft.
remote='sudo tar -czf - -C /srv/fw-alarm data app/config.php || [ $? -eq 1 ]'
if ! ssh_vm "$remote" > "$part"; then
    fail "die VM hat kein vollständiges Archiv geliefert (Verbindung, sudo oder tar)."
fi

# Prüfen, bevor die Datei ihren endgültigen Namen bekommt. (Erst einlesen,
# dann suchen: "tar | grep -q" scheitert mit pipefail, weil grep nach dem
# Treffer aufhört zu lesen.)
members="$(tar -tzf - < "$part" 2>/dev/null)" || fail "das Archiv ist beschädigt oder unvollständig (Übertragung abgebrochen?)."
for need in data/alarm_keys.json.php app/config.php; do
    grep -qxF "$need" <<<"$members" || fail "im Archiv fehlt $need."
done
keys="$(tar -xzOf - data/alarm_keys.json.php < "$part")" || fail "das Archiv lässt sich nicht lesen."
grep -q '"keys"' <<<"$keys" \
    || fail "die Empfängerliste in der Sicherung ist nicht lesbar (data/alarm_keys.json.php) - ältere Sicherungen bleiben erhalten."

name="fw-alarm-$(date -u +%Y%m%dT%H%M%SZ).tgz"
chmod 600 "$part" 2>/dev/null || true
mv -f "$part" "$dest/$name"

# Aufräumen: nur eigene Dateien (Namensmuster), die ältesten zuerst.
shopt -s nullglob
all=("$dest"/fw-alarm-[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]T[0-9][0-9][0-9][0-9][0-9][0-9]Z.tgz)
shopt -u nullglob
extra=$(( ${#all[@]} - keep ))
if [ "$extra" -gt 0 ]; then
    rm -f -- "${all[@]:0:extra}"
else
    extra=0
fi

files="$(grep -vc '/$' <<<"$members" || true)"
echo "Sicherung: $dest/$name ($files Dateien) - $(( ${#all[@]} - extra )) Sicherungen im Ordner"
