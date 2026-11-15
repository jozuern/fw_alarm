#!/usr/bin/env bash
# Stoppt Dashboard und/oder Bark-Attrappe. Aufruf: stop.sh <dir> [site|mock|all]
dir="${1:?Aufruf: stop.sh <dir> [site|mock|all]}"
which="${2:-all}"
for n in site mock; do
  if [ "$which" = all ] || [ "$which" = "$n" ]; then
    f="$dir/pids/$n"
    if [ -f "$f" ]; then
      pid="$(cat "$f")"
      # Git-Bash-PID -> Windows-PID, damit taskkill den php.exe trifft
      winpid="$(ps -p "$pid" 2>/dev/null | awk 'NR==2 {print $4}')"
      # Ohne Windows-PID (veraltete PID-Datei) NIE taskkill: MSYS- und
      # Windows-PIDs sind getrennte Räume, es könnte ein fremder Prozess sein.
      if [ -n "$winpid" ]; then
        MSYS_NO_PATHCONV=1 taskkill /F /PID "$winpid" >/dev/null 2>&1 || true
      else
        kill "$pid" 2>/dev/null || true
      fi
      rm -f "$f"
    fi
  fi
done
echo "gestoppt ($which)"
