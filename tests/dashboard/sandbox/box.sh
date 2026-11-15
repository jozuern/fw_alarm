#!/usr/bin/env bash
# Simuliert einen Status-Push der Box. Aufruf: box.sh <dir> [--age <s>] [feld=wert ...]
# --age <s>: setzt seen_at danach auf jetzt - s (simuliert "Box offline").
set -euo pipefail
dir="${1:?Aufruf: box.sh <dir> [--age <s>] [feld=wert ...]}"; shift
base="${SANDBOX_BASE:-http://127.0.0.1:8787}"
age=""
declare -A f
order=(token device_id fw_build core uptime_s wifi rssi ip relay cooldown waiting_for_release alarm_pending isr_latched key_progress last_alarm last_heartbeat heartbeat_ok stuck_warned ack_pending keys_count keys_version keys_muted reset_reason free_heap max_alloc_heap alarm_seq alarm_source alarm_gaveup)
defaults="token=box-token device_id=fw-box-1 fw_build=sandbox core=2.0.17 uptime_s=360000 wifi=up rssi=-61 ip=192.168.1.50 relay=open cooldown=0 waiting_for_release=0 alarm_pending=0 isr_latched=0 key_progress=0/0 last_alarm=nie last_heartbeat=nie heartbeat_ok=0 stuck_warned=0 ack_pending=0 keys_count=1 keys_version=0 keys_muted=0 reset_reason=poweron free_heap=180000 max_alloc_heap=90000 alarm_seq=0 alarm_source= alarm_gaveup=0"
for kv in $defaults; do f[${kv%%=*}]="${kv#*=}"; done
while [ $# -gt 0 ]; do
  if [ "$1" = "--age" ]; then age="${2:?--age braucht Sekunden}"; shift 2; continue; fi
  k="${1%%=*}"; f[$k]="${1#*=}"
  case " ${order[*]} " in *" $k "*) ;; *) order+=("$k");; esac
  shift
done
args=()
for k in "${order[@]}"; do args+=(--form-string "$k=${f[$k]}"); done
curl -s "${args[@]}" "$base/api/status.php"
echo
if [ -n "$age" ]; then
  wdir="$(cygpath -m "$(cd "$dir" && pwd)")"
  # Dateiformat wie write_json_file(): Guard-Zeile, dann JSON.
  php -r '
    $p = $argv[1] . "/data/latest_status.json.php";
    $raw = file_get_contents($p);
    $nl = strpos($raw, "\n");
    $guard = substr($raw, 0, $nl + 1);
    $d = json_decode(substr($raw, $nl + 1), true);
    $d["seen_at"] = time() - (int)$argv[2];
    file_put_contents($p, $guard . json_encode($d, JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES));
  ' "$wdir" "$age"
fi
