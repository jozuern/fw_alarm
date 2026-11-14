#!/bin/bash
# =============================================================================
# Außen-Wächter für das FW-Alarm-NAS (läuft auf einer kleinen Cloud-VM)
# =============================================================================
# Der Cron-Wächter auf dem NAS meldet, wenn die Box schweigt - aber nicht,
# wenn das NAS selbst weg ist (Strom/Internet am Standort, Router, DDNS,
# Zertifikat, Web Station). Dann fällt er nämlich mit aus. Dieses Skript
# schaut deshalb von AUSSEN: alle 5 Minuten (systemd-Timer) ein POST an
# api/alive.php des NAS. Es meldet per Bark an den Status-Key:
#   - "NAS nicht erreichbar"   nach FAIL_THRESHOLD Fehlschlägen in Folge,
#                              Erinnerung alle REPEAT_HOURS, einmal "wieder da"
#   - "NAS-Wächter läuft nicht" wenn die DSM-Aufgabe check_offline.php länger
#                              als CRON_STALE_S nicht gelaufen ist (dann würde
#                              ein Ausfall der Box nicht gemeldet)
# Als erledigt gilt eine Meldung NUR, wenn Bark sie angenommen hat - sonst
# versucht es der nächste Lauf erneut.
# Umgekehrt merkt sich das NAS jeden Aufruf und meldet, wenn diese VM
# verstummt (z.B. von Oracle als "idle" gestoppt).
#
# Bark-Texte bewusst ASCII (ae/oe/ue): Bark stellt Umlaute nicht sauber dar.
#
# Aufruf:  fw-alarm-watch           ein Prüflauf (so ruft es der Timer auf)
#          fw-alarm-watch --test    schickt eine Probe-Meldung an Bark
# Konfiguration: /etc/fw-alarm-watch.conf (Vorlage: fw-alarm-watch.conf.example)
# =============================================================================
set -u

CONF="${FW_WATCH_CONF:-/etc/fw-alarm-watch.conf}"
STATE_DIR="${STATE_DIRECTORY:-/var/lib/fw-alarm-watch}"
STATE="$STATE_DIR/state"

# ---- Standards (in der Konfiguration überschreibbar) -------------------------
BARK_HOST="https://api.day.app"
BARK_LEVEL="active"        # passive | active | timeSensitive | critical
FAIL_THRESHOLD=3           # Fehlschläge in Folge bis zur Meldung (Takt 5 min)
REPEAT_HOURS=24            # Erinnerung, solange das NAS weg bleibt (0 = nie)
CRON_STALE_S=2700          # 45 min = drei ausgefallene 15-min-Läufe
NAS_ALIVE_URL=""
WATCHER_TOKEN=""
BARK_KEY=""

if [ ! -r "$CONF" ]; then
    echo "Konfiguration $CONF fehlt oder ist nicht lesbar" >&2
    exit 2
fi
# shellcheck source=/dev/null
. "$CONF"

if [ -z "$NAS_ALIVE_URL" ] || [ -z "$WATCHER_TOKEN" ] || [ -z "$BARK_KEY" ]; then
    echo "NAS_ALIVE_URL, WATCHER_TOKEN und BARK_KEY müssen in $CONF gesetzt sein" >&2
    exit 2
fi

now=$(date +%s)
stamp=$(date '+[%d.%m.%Y %H:%M]')

# Dauer in Sekunden als kurzer ASCII-Text.
fmt_dur() {
    local s=$1
    if [ "$s" -lt 5400 ]; then
        echo "$(( (s + 30) / 60 )) min"
    elif [ "$s" -lt 172800 ]; then
        echo "$(( (s + 1800) / 3600 )) Std"
    else
        echo "$(( (s + 43200) / 86400 )) Tagen"
    fi
}

# Eine Meldung an Bark. Rückgabe 0 nur, wenn Bark sie angenommen hat.
# Bis zu drei Versuche; bei HTTP 4xx sofort aufgeben (Key falsch o.ae.).
bark_send() {
    local title=$1 body=$2 try code
    for try in 1 2 3; do
        code=$(curl -sS -o /dev/null -w '%{http_code}' -m 20 \
            --data-urlencode "title=$title" \
            --data-urlencode "body=$body" \
            --data-urlencode "level=$BARK_LEVEL" \
            --data-urlencode "group=FW-Alarm" \
            "${BARK_HOST%/}/$BARK_KEY" 2>/dev/null) || code=000
        [ "$code" = "200" ] && return 0
        case "$code" in 4??) break ;; esac
        [ "$try" -lt 3 ] && sleep 5
    done
    echo "Bark-Versand fehlgeschlagen (HTTP $code): $title" >&2
    return 1
}

if [ "${1:-}" = "--test" ]; then
    if bark_send "Aussen-Waechter: Probe" \
        "$stamp Probe-Meldung der Cloud-VM. Kommt diese Nachricht an, kann der Aussen-Waechter melden, wenn das NAS von aussen nicht mehr erreichbar ist."; then
        echo "Probe-Meldung gesendet."
        exit 0
    fi
    exit 1
fi

# ---- Zustand laden -----------------------------------------------------------
fail_count=0; fail_since=0; fail_reason=""; down_notified_at=0
cron_notified_at=0
if [ -r "$STATE" ]; then
    # Nur die bekannten Schlüssel übernehmen (die Datei schreibt nur dieses
    # Skript, aber sicher ist sicher).
    while IFS='=' read -r k v; do
        case "$k" in
            fail_count|fail_since|down_notified_at|cron_notified_at)
                [[ "$v" =~ ^[0-9]+$ ]] && printf -v "$k" '%s' "$v" ;;
            fail_reason) fail_reason=$v ;;
        esac
    done < "$STATE"
fi

save_state() {
    local tmp="$STATE.tmp"
    {
        echo "fail_count=$fail_count"
        echo "fail_since=$fail_since"
        echo "fail_reason=$fail_reason"
        echo "down_notified_at=$down_notified_at"
        echo "cron_notified_at=$cron_notified_at"
    } > "$tmp" && mv -f "$tmp" "$STATE"
}

# ---- 1) NAS von außen erreichbar? ---------------------------------------------
nonce=$(head -c 12 /dev/urandom | od -An -tx1 | tr -d ' \n')
body_file=$(mktemp)
trap 'rm -f "$body_file"' EXIT
# Ein Lauf versucht es bis zu dreimal (je 20 s), damit ein einzelner
# verlorener Request nicht gleich als Fehlschlag zählt.
http=$(curl -sS -o "$body_file" -w '%{http_code}' -m 20 \
    --retry 2 --retry-delay 5 --retry-all-errors \
    --data-urlencode "token=$WATCHER_TOKEN" \
    --data-urlencode "nonce=$nonce" \
    "$NAS_ALIVE_URL" 2>/dev/null)
rc=$?

# Der Grund landet im Bark-Text der Meldung - deshalb ASCII.
reason=""
if [ $rc -ne 0 ]; then
    case $rc in
        6)  reason="Name nicht aufloesbar (DDNS?)" ;;
        7)  reason="keine Verbindung (NAS aus, Internet/Router am Standort, Portweiterleitung?)" ;;
        28) reason="Zeitueberschreitung (NAS oder Leitung haengt?)" ;;
        35|60) reason="TLS/Zertifikat ungueltig (Let's Encrypt abgelaufen?)" ;;
        *)  reason="curl-Fehler $rc" ;;
    esac
elif [ "$http" = "403" ]; then
    reason="HTTP 403 - Token passt nicht (watcher_token_sha256 in config.php?)"
elif [ "$http" = "404" ]; then
    reason="HTTP 404 - api/alive.php fehlt oder watcher_token_sha256 nicht gesetzt"
elif [ "$http" != "200" ]; then
    reason="HTTP $http (Web Station/PHP gestoert?)"
elif ! grep -qx 'ok=1' "$body_file" || ! grep -qx "nonce=$nonce" "$body_file"; then
    reason="unerwartete Antwort (keine gueltige Rueckmeldung von alive.php)"
fi

if [ -z "$reason" ]; then
    # ---- erreichbar ----
    if [ "$down_notified_at" -gt 0 ]; then
        down=$(( now - fail_since ))
        if bark_send "NAS wieder erreichbar" \
            "$stamp Das NAS antwortet von aussen wieder (Ausfall ca. $(fmt_dur "$down"))."; then
            down_notified_at=0
        fi
    fi
    if [ "$down_notified_at" -eq 0 ]; then
        fail_count=0; fail_since=0; fail_reason=""
    fi

    # ---- 2) Läuft der Cron-Wächter auf dem NAS noch? ----
    cron_age=$(sed -n 's/^cron_age_s=\(-\{0,1\}[0-9]*\)$/\1/p' "$body_file" | head -1)
    box_age=$(sed -n 's/^box_age_s=\(-\{0,1\}[0-9]*\)$/\1/p' "$body_file" | head -1)
    [ -z "$cron_age" ] && cron_age=-1
    [ -z "$box_age" ] && box_age=-1
    box_txt="unbekannt"
    [ "$box_age" -ge 0 ] && box_txt="vor $(fmt_dur "$box_age")"
    if [ "$cron_age" -lt 0 ] || [ "$cron_age" -gt "$CRON_STALE_S" ]; then
        repeat=$(( REPEAT_HOURS * 3600 ))
        if [ "$cron_notified_at" -eq 0 ] \
            || { [ "$repeat" -gt 0 ] && [ $(( now - cron_notified_at )) -gt "$repeat" ]; }; then
            if [ "$cron_age" -lt 0 ]; then ran="noch nie"; else ran="seit $(fmt_dur "$cron_age") nicht mehr"; fi
            if bark_send "NAS-Waechter laeuft nicht" \
                "$stamp Die DSM-Aufgabe check_offline.php lief $ran - ein Ausfall der Box wuerde NICHT gemeldet. Box zuletzt gemeldet: $box_txt. DSM > Aufgabenplaner pruefen."; then
                cron_notified_at=$now
            fi
        fi
    elif [ "$cron_notified_at" -gt 0 ]; then
        if bark_send "NAS-Waechter laeuft wieder" \
            "$stamp Die DSM-Aufgabe check_offline.php laeuft wieder. Box zuletzt gemeldet: $box_txt."; then
            cron_notified_at=0
        fi
    fi
    save_state
    echo "OK http=$http cron_age=$cron_age box_age=$box_age"
    exit 0
fi

# ---- nicht erreichbar ----
fail_count=$(( fail_count + 1 ))
[ "$fail_since" -eq 0 ] && fail_since=$now
fail_reason=$reason
echo "FAIL $fail_count: $reason"
if [ "$fail_count" -ge "$FAIL_THRESHOLD" ]; then
    repeat=$(( REPEAT_HOURS * 3600 ))
    down=$(( now - fail_since ))
    if [ "$down_notified_at" -eq 0 ]; then
        title="NAS NICHT ERREICHBAR"
    else
        title="NAS weiterhin nicht erreichbar"
    fi
    if [ "$down_notified_at" -eq 0 ] \
        || { [ "$repeat" -gt 0 ] && [ $(( now - down_notified_at )) -gt "$repeat" ]; }; then
        if bark_send "$title" \
            "$stamp Das NAS antwortet von aussen seit ca. $(fmt_dur "$down") nicht: $reason. Der manuelle REAL ALARM ueber das Dashboard geht dann nicht. Liegt die Ursache am Standort (Strom/Internet), ist auch die Box betroffen."; then
            down_notified_at=$now
        fi
    fi
fi
save_state
exit 0
