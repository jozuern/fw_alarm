#!/usr/bin/env bash
# Spielt einen Stand von nas_dashboard/ auf die Oracle-VM ein.
#
# Aufruf vom PC (Git Bash), im Repo:
#     bash server/deploy.sh <git-rev>          z.B. HEAD
#
# Ablauf: git archive <rev> → VM (/tmp/fw-deploy/<sha>) → rsync in den Webroot
# → PHP-FPM neu laden → Prüfsummen ALLER eingespielten Dateien gegen die
# Git-Blobs dieses Stands vergleichen. Ausgabe am Ende:
#     deployed <sha>: N files, 0 mismatches
#
# Nie angefasst werden config.php (liegt nur auf der VM) und die Daten.
# Nicht eingespielt werden SETUP.md, config.example.php und data/.
# Eingespielt wird nur, was auf origin/main liegt (erst pushen).
#
# Ziel überschreibbar: FW_DEPLOY_HOST (Standard ubuntu@<IP der VM>),
# FW_DEPLOY_KEY (Standard ~/.ssh/oci_fw_alarm).
set -euo pipefail

rev="${1:-}"
if [ -z "$rev" ]; then
    echo "Aufruf: bash server/deploy.sh <git-rev>" >&2
    exit 2
fi
host="${FW_DEPLOY_HOST:-ubuntu@<IP der VM>}"
key="${FW_DEPLOY_KEY:-$HOME/.ssh/oci_fw_alarm}"
app="/srv/fw-alarm/app"

ssh_vm() { ssh -i "$key" -o BatchMode=yes -o ConnectTimeout=15 "$host" "$@"; }

cd "$(git rev-parse --show-toplevel)"
sha="$(git rev-parse --verify --quiet "$rev^{commit}")" || {
    echo "Unbekannter Stand: $rev" >&2
    exit 2
}
git fetch --quiet origin main
if ! git merge-base --is-ancestor "$sha" origin/main; then
    echo "Abbruch: $sha liegt nicht auf origin/main - erst 'git push origin main'." >&2
    exit 1
fi

# Was nicht auf den Server gehört (gleiche Liste wie die rsync-Ausnahmen unten).
skip='^nas_dashboard/(config\.php|SETUP\.md|config\.example\.php|data/)'

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# Soll-Liste: sha256 jedes Git-Blobs dieses Stands (unabhängig davon, wie die
# Dateien im Arbeitsverzeichnis des PCs gerade aussehen - CRLF/LF).
git ls-tree -r --name-only "$sha" nas_dashboard/ | grep -vE "$skip" | LC_ALL=C sort > "$tmp/files"
count="$(wc -l < "$tmp/files" | tr -d ' ')"
if [ "$count" -lt 10 ]; then
    echo "Abbruch: nur $count Dateien unter nas_dashboard/ in $sha - falscher Stand?" >&2
    exit 1
fi
: > "$tmp/want"
while IFS= read -r f; do
    sum="$(git cat-file blob "$sha:$f" | sha256sum | cut -d' ' -f1)"
    printf '%s  %s\n' "$sum" "${f#nas_dashboard/}" >> "$tmp/want"
done < "$tmp/files"

# Archiv ohne Zeilenenden-Umwandlung (core.autocrlf des PCs darf nicht
# mitreden): Auf dem Server liegen exakt die Bytes aus Git.
remote_tmp="/tmp/fw-deploy/$sha"
git -c core.autocrlf=false -c core.eol=lf archive --format=tar "$sha" nas_dashboard \
    | ssh_vm "rm -rf '$remote_tmp' && mkdir -p '$remote_tmp' && tar -x -C '$remote_tmp'"

# --delay-updates: Alle Dateien werden erst am Ende gemeinsam umbenannt (die
# Box fragt alle 10 s an - das Fenster mit gemischtem Stand bleibt winzig).
# --delete-after entfernt, was es im neuen Stand nicht mehr gibt; die
# Ausnahmen (config.php!) bleiben dabei unberührt.
ssh_vm "sudo rsync -rlt --delete-after --delay-updates --chmod=D755,F644 \
    --exclude=/config.php --exclude=/SETUP.md --exclude=/config.example.php --exclude=/data/ \
    '$remote_tmp/nas_dashboard/' '$app/' \
  && sudo systemctl reload php8.3-fpm \
  && printf '%s %s\n' '$sha' \"\$(date -Is)\" | sudo tee /srv/fw-alarm/deployed-rev >/dev/null \
  && rm -rf '$remote_tmp'"

# Ist-Liste vom Server: alles im Webroot außer config.php.
ssh_vm "cd '$app' && sudo find . -type f ! -path ./config.php -print0 | LC_ALL=C sort -z | sudo xargs -0 sha256sum" \
    | sed 's#  \./#  #' | tr -d '\r' | LC_ALL=C sort -k2 > "$tmp/have"
LC_ALL=C sort -k2 "$tmp/want" > "$tmp/want.sorted"

if diff "$tmp/want.sorted" "$tmp/have" > "$tmp/diff"; then
    echo "deployed $sha: $count files, 0 mismatches"
else
    bad="$(grep -c '^[<>]' "$tmp/diff" || true)"
    echo "FEHLER: Server weicht von $sha ab ($bad Zeilen; < = erwartet, > = auf dem Server):" >&2
    sed 's/^\([<>]\) [0-9a-f]\{64\}  /\1 /' "$tmp/diff" | grep '^[<>]' | sort -u >&2
    exit 1
fi
