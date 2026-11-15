#!/usr/bin/env bash
# Startet Dashboard (127.0.0.1:8787) und/oder Bark-Attrappe (127.0.0.1:8789).
# Aufruf: start.sh <dir> [site|mock|all]
set -euo pipefail
dir="${1:?Aufruf: start.sh <dir> [site|mock|all]}"
which="${2:-all}"
here="$(cd "$(dirname "$0")" && pwd)"
dir="$(cd "$dir" && pwd)"
wdir="$(cygpath -m "$dir")"
php_ext="$(dirname "$(command -v php)")/ext"
mkdir -p "$dir/pids"
ports=""
if [ "$which" = site ] || [ "$which" = all ]; then
  php -d extension_dir="$php_ext" -d extension=curl -d extension=openssl \
    -S 127.0.0.1:8787 -t "$wdir/site" >"$dir/site.out" 2>&1 &
  echo $! >"$dir/pids/site"
  ports="$ports 8787"
fi
if [ "$which" = mock ] || [ "$which" = all ]; then
  BARK_LOG="$wdir/bark.log" php -S 127.0.0.1:8789 "$(cygpath -m "$here/bark-mock.php")" >"$dir/mock.out" 2>&1 &
  echo $! >"$dir/pids/mock"
  ports="$ports 8789"
fi
# Auf Erreichbarkeit warten
for p in $ports; do
  for _ in $(seq 1 50); do
    curl -s -o /dev/null "http://127.0.0.1:$p/" && break
    sleep 0.2
  done
done
echo "gestartet ($which)"
