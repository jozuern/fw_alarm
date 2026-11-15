#!/usr/bin/env bash
# Legt die Test-Sandbox an: <dir>/site (Kopie von nas_dashboard ohne
# config.php/data), <dir>/data, <dir>/site/config.php. Erneutes Ausführen
# kopiert nur site/ neu (data/ und config.php bleiben).
set -euo pipefail
dir="${1:?Aufruf: setup.sh <dir>}"
here="$(cd "$(dirname "$0")" && pwd)"
src="$here/../../../nas_dashboard"
mkdir -p "$dir/data" "$dir/site"
dir="$(cd "$dir" && pwd)"
keep=""
if [ -f "$dir/site/config.php" ]; then
  keep="$(mktemp)"; cp "$dir/site/config.php" "$keep"
fi
rm -rf "$dir/site"
mkdir -p "$dir/site"
( cd "$src" && tar --exclude=./config.php --exclude=./data -cf - . ) | ( cd "$dir/site" && tar -xf - )
if [ -n "$keep" ]; then
  cp "$keep" "$dir/site/config.php"; rm -f "$keep"
else
  php "$(cygpath -m "$here/make-config.php")" "$(cygpath -m "$dir")"
fi
echo "Sandbox bereit: $dir"
