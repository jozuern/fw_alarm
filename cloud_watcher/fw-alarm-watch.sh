#!/bin/bash
# =============================================================================
# Außen-Wächter für das FW-Alarm-NAS (läuft auf einer kleinen Cloud-VM)
# =============================================================================
# Der Cron-Wächter auf dem NAS meldet, wenn die Box schweigt - aber nicht,
# wenn das NAS selbst weg ist (Strom/Internet am Standort, Router, DDNS,
# Zertifikat, Web Station). Dann fällt er nämlich mit aus. Dieses Skript
# schaut deshalb von AUSSEN: alle 5 Minuten (systemd-Timer) ein POST an
# api/alive.php des NAS. Es meldet per Bark an den Status-Key:
#   - "NAS NICHT ERREICHBAR"   nach FAIL_THRESHOLD Fehlschlägen in Folge,
#                              Erinnerung alle REPEAT_HOURS, einmal "wieder da"
#   - "NAS-Waechter laeuft nicht" wenn die DSM-Aufgabe check_offline.php länger
#                              als CRON_STALE_S nicht gelaufen ist, bzw.
#     "NAS-Waechter meldet nichts" wenn in config.php bark_key_status fehlt
#                              (in beiden Fällen würde ein Ausfall der Box
#                              nicht gemeldet); Erinnerung alle REPEAT_HOURS,
#                              einmal "laeuft wieder"
#   - "NAS kann Bark nicht erreichen" wenn die letzte Meldung des NAS-Wächters
#                              an Bark scheiterte (FAIL_THRESHOLD Läufe in
#                              Folge) - dann geht auch der manuelle REAL ALARM
#                              nicht raus; Erinnerung alle REPEAT_HOURS,
#                              einmal "wieder"
# Als erledigt gilt eine Meldung NUR, wenn Bark sie angenommen hat (HTTP 200)
# - sonst versucht es der nächste Lauf erneut. Scheitert der Versand, erfährt
# das NAS das beim nächsten Aufruf (Feld bark_failed_at) und zeigt es im
# Dashboard an; hat sich die Meldung bis zum nächsten erfolgreichen Versand
# erledigt, kommt einmal "Aussen-Waechter kann wieder melden".
# Umgekehrt merkt sich das NAS jeden Aufruf und meldet, wenn diese VM
# verstummt (z.B. von Oracle als "idle" gestoppt). Deshalb ruft das Skript
# das NAS gar nicht erst auf, wenn es seinen Zustand nicht speichern kann
# (exit 1): Ein Wächter, der nicht sauber arbeiten kann, soll beim NAS als
# verstummt auffallen, statt sich dort als gesund zu melden.
#
# Token und Bark-Key gehen per "curl -K -" über stdin an curl, nie als
# Argument - sonst stünden sie für jeden Benutzer der VM in der Prozessliste.
# Nur HTTPS (--proto =https).
#
# Bark-Texte bewusst ASCII (ae/oe/ue): Bark stellt Umlaute nicht sauber dar.
#
# Aufruf:  fw-alarm-watch           ein Prüflauf (so ruft es der Timer auf)
#          fw-alarm-watch --test    schickt eine Probe-Meldung an Bark
# Exit:    0 = Lauf erledigt (auch wenn das NAS nicht erreichbar war),
#          1 = Zustand nicht speicherbar bzw. Probe-Meldung gescheitert,
#          2 = Konfiguration fehlt oder ist ungültig (dann kein NAS-Aufruf)
# Konfiguration: /etc/fw-alarm-watch.conf (Vorlage: fw-alarm-watch.conf.example)
# =============================================================================
set -u

CONF="${FW_WATCH_CONF:-/etc/fw-alarm-watch.conf}"
STATE_DIR="${STATE_DIRECTORY:-/var/lib/fw-alarm-watch}"
STATE="$STATE_DIR/state"

mode=check
case "${1:-}" in
    "")     ;;
    --test) mode=test ;;
    *)      echo "Aufruf: $0 [--test]" >&2; exit 2 ;;
esac

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

# ---- Konfiguration prüfen (vor jedem Netzzugriff) -----------------------------
conf_error() {
    echo "$CONF: $*" >&2
    exit 2
}
if [ -z "$NAS_ALIVE_URL" ] || [ -z "$WATCHER_TOKEN" ] || [ -z "$BARK_KEY" ]; then
    conf_error "NAS_ALIVE_URL, WATCHER_TOKEN und BARK_KEY müssen gesetzt sein"
fi
for v in NAS_ALIVE_URL BARK_HOST WATCHER_TOKEN BARK_KEY BARK_LEVEL; do
    # Häufigste Ursache: Datei unter Windows bearbeitet (CR am Zeilenende).
    if [[ ${!v} =~ [[:space:][:cntrl:]] ]]; then
        conf_error "$v enthält Leer- oder Steuerzeichen (Windows-Zeilenenden? sed -i 's/\\r\$//' $CONF)"
    fi
done
for v in NAS_ALIVE_URL BARK_HOST; do
    case ${!v} in
        https://?*) ;;
        *) conf_error "$v muss mit https:// beginnen (ist: '${!v}')" ;;
    esac
done
# Gleiches Format, das auch das Dashboard für Bark-Keys verlangt.
if ! [[ $BARK_KEY =~ ^[A-Za-z0-9_-]{5,64}$ ]]; then
    conf_error "BARK_KEY hat kein gültiges Format (5-64 Zeichen: Buchstaben, Ziffern, _ und -)"
fi
case $BARK_LEVEL in
    passive|active|timeSensitive|critical) ;;
    *) conf_error "BARK_LEVEL muss passive, active, timeSensitive oder critical sein (ist: '$BARK_LEVEL')" ;;
esac
for v in FAIL_THRESHOLD REPEAT_HOURS CRON_STALE_S; do
    if ! [[ ${!v} =~ ^[0-9]{1,9}$ ]]; then
        conf_error "$v muss eine ganze Zahl ohne Vorzeichen sein (ist: '${!v}')"
    fi
    # 10#: führende Nullen ("08") nicht als Oktalzahl lesen.
    printf -v "$v" '%d' "$((10#${!v}))"
done

now=$(date +%s)
stamp=$(date '+[%d.%m.%Y %H:%M]')

# ---- Sperre gegen parallele Läufe (Timer + Handaufruf) -------------------------
# Liegt im Zustandsverzeichnis: Ist das nicht beschreibbar, scheitert schon
# das hier - und das ist genau der Fall "Zustand nicht speicherbar".
if ! exec 9>"$STATE_DIR/lock"; then
    echo "Zustandsverzeichnis $STATE_DIR fehlt oder ist nicht beschreibbar - Abbruch ohne NAS-Aufruf (das NAS meldet den Aussen-Waechter dann als verstummt)." >&2
    exit 1
fi
flock -n 9
lock_rc=$?
if [ "$lock_rc" -eq 1 ]; then
    echo "Es läuft bereits ein Prüflauf - dieser Aufruf endet ohne Wirkung." >&2
    [ "$mode" = test ] && exit 1
    exit 0
elif [ "$lock_rc" -ne 0 ]; then
    echo "flock fehlt oder scheitert (Rückgabe $lock_rc) - Abbruch ohne NAS-Aufruf." >&2
    exit 1
fi

# Dauer in Sekunden als kurzer ASCII-Text. Passt hinter "seit ca.", "vor"
# und "nach ca. … Ausfall" (deshalb Dativ "Tagen").
fmt_dur() {
    local s=$1
    [ "$s" -lt 0 ] && s=0
    if [ "$s" -lt 5400 ]; then
        echo "$(( (s + 30) / 60 )) min"
    elif [ "$s" -lt 172800 ]; then
        echo "$(( (s + 1800) / 3600 )) Std"
    else
        echo "$(( (s + 43200) / 86400 )) Tagen"
    fi
}

# Erinnerung fällig? $1 = Zeitpunkt der letzten Meldung. REPEAT_HOURS=0 = nie.
repeat_due() {
    [ "$REPEAT_HOURS" -gt 0 ] && [ $(( now - $1 )) -gt $(( REPEAT_HOURS * 3600 )) ]
}

# Eine Zeile für curls Konfiguration (curl -K -): Wert in Anführungszeichen,
# Backslash und Anführungszeichen maskiert. Steuerzeichen sind oben schon
# ausgeschlossen bzw. kommen in den festen Texten nicht vor. Die Ersetzungen
# bewusst in '…': Unmaskierte Backslashes im Ersatztext behandelt Bash ab 5.2
# anders als vorher.
cfg_line() {
    local v=${2//'\'/'\\'}
    v=${v//'"'/'\"'}
    printf '%s = "%s"\n' "$1" "$v"
}

# Eine Meldung an Bark. Rückgabe 0 nur, wenn Bark sie angenommen hat.
# Bis zu drei Versuche; bei HTTP 4xx sofort aufgeben (Key falsch o.ae.).
# Führt bark_failed_at: Zeitpunkt des ersten gescheiterten Versands seit dem
# letzten erfolgreichen (0 = alles gut) - geht beim nächsten Aufruf ans NAS.
bark_tried=0
bark_send() {
    local title=$1 msg=$2 try code
    bark_tried=1
    for try in 1 2 3; do
        # Fehlertexte von curl landen bewusst im Journal (stderr).
        code=$( {
            cfg_line url "${BARK_HOST%/}/$BARK_KEY"
            cfg_line data-urlencode "title=$title"
            cfg_line data-urlencode "body=$msg"
            cfg_line data-urlencode "level=$BARK_LEVEL"
            cfg_line data-urlencode "group=FW-Alarm"
        } | curl -sS -o /dev/null -w '%{http_code}' -m 20 --proto =https -K - ) || code=000
        if [ "$code" = "200" ]; then
            bark_failed_at=0
            return 0
        fi
        case "$code" in 4??) break ;; esac
        [ "$try" -lt 3 ] && sleep 5
    done
    echo "Bark-Versand fehlgeschlagen (HTTP $code): $title" >&2
    [ "$bark_failed_at" -eq 0 ] && bark_failed_at=$now
    return 1
}

# ---- Zustand laden -----------------------------------------------------------
fail_count=0; fail_since=0; fail_reason=""; down_notified_at=0
cron_notified_at=0; cron_reason=""
sendfail_count=0; sendfail_since=0; sendfail_notified_at=0
bark_failed_at=0
if [ -e "$STATE" ] && [ ! -r "$STATE" ]; then
    echo "Zustand $STATE ist nicht lesbar - Abbruch ohne NAS-Aufruf." >&2
    exit 1
fi
if [ -r "$STATE" ]; then
    # Nur die bekannten Schlüssel übernehmen (die Datei schreibt nur dieses
    # Skript, aber sicher ist sicher).
    while IFS='=' read -r k v; do
        case "$k" in
            fail_count|fail_since|down_notified_at|cron_notified_at|sendfail_count|sendfail_since|sendfail_notified_at|bark_failed_at)
                [[ "$v" =~ ^[0-9]{1,12}$ ]] && printf -v "$k" '%d' "$((10#$v))" ;;
            fail_reason) fail_reason=$v ;;
            cron_reason)
                case "$v" in stale|unconfigured) cron_reason=$v ;; esac ;;
        esac
    done < "$STATE"
fi
# Zustand eines älteren Skript-Stands: gemeldet war dort nur "lief nicht".
[ "$cron_notified_at" -gt 0 ] && [ -z "$cron_reason" ] && cron_reason=stale

save_state() {
    local tmp="$STATE.tmp"
    # Ein einziges printf: Sein Rückgabewert erfasst jeden Schreibfehler.
    if printf '%s\n' \
        "fail_count=$fail_count" \
        "fail_since=$fail_since" \
        "fail_reason=$fail_reason" \
        "down_notified_at=$down_notified_at" \
        "cron_notified_at=$cron_notified_at" \
        "cron_reason=$cron_reason" \
        "sendfail_count=$sendfail_count" \
        "sendfail_since=$sendfail_since" \
        "sendfail_notified_at=$sendfail_notified_at" \
        "bark_failed_at=$bark_failed_at" > "$tmp" \
        && mv -f "$tmp" "$STATE"; then
        return 0
    fi
    rm -f "$tmp" 2>/dev/null
    echo "Zustand $STATE nicht speicherbar (Platte voll? Rechte?)" >&2
    return 1
}

# Probe-Schreiben VOR jedem Netzzugriff: Kann der Lauf sein Ergebnis nicht
# festhalten, darf er sich beim NAS nicht als gesund melden.
if ! save_state; then
    echo "Abbruch ohne NAS-Aufruf - das NAS meldet den Aussen-Waechter dann als verstummt." >&2
    exit 1
fi

if [ "$mode" = test ]; then
    sent=0
    if bark_send "Aussen-Waechter: Probe" \
        "$stamp Probe-Meldung der Cloud-VM. Kommt diese Nachricht an, kann der Aussen-Waechter melden, wenn das NAS von aussen nicht mehr erreichbar ist."; then
        sent=1
    fi
    save_state || exit 1
    if [ "$sent" -eq 1 ]; then
        echo "Probe-Meldung gesendet."
        exit 0
    fi
    exit 1
fi

# ---- 1) NAS von außen erreichbar? ---------------------------------------------
nonce=$(head -c 12 /dev/urandom | od -An -tx1 | tr -d ' \n')
# Ein Lauf versucht es bis zu dreimal (je 20 s), damit ein einzelner
# verlorener Request nicht gleich als Fehlschlag zählt. Antwort und
# HTTP-Code kommen zusammen über stdout (letzte Zeile = Code), ohne
# Temp-Datei.
resp=$( {
    cfg_line url "$NAS_ALIVE_URL"
    cfg_line data-urlencode "token=$WATCHER_TOKEN"
    cfg_line data-urlencode "nonce=$nonce"
    cfg_line data-urlencode "bark_failed_at=$bark_failed_at"
} | curl -sS -m 20 --proto =https --retry 2 --retry-delay 5 --retry-all-errors \
    -w '\n%{http_code}' -K - )
rc=$?
http=${resp##*$'\n'}
resp_body=${resp%$'\n'*}
[[ $http =~ ^[0-9]{3}$ ]] || http=000

# Wert der ersten Antwortzeile "name=wert" (Rückgabe 1, wenn sie fehlt).
resp_field() {
    local re=$'(^|\n)'"$1"$'=([^\r\n]*)'
    [[ $resp_body =~ $re ]] || return 1
    printf '%s' "${BASH_REMATCH[2]}"
}
# Alter in Sekunden aus der Antwort; fehlend, negativ oder kaputt = -1.
resp_age() {
    local v
    v=$(resp_field "$1") || v=""
    if [[ $v =~ ^[0-9]{1,10}$ ]]; then echo "$((10#$v))"; else echo -1; fi
}

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
    reason="HTTP 404 - api/alive.php fehlt, watcher_token_sha256 nicht gesetzt oder gleich machine_token_sha256"
elif [ "$http" != "200" ]; then
    reason="HTTP $http (Web Station/PHP gestoert?)"
elif [ "$(resp_field ok)" != "1" ] || [ "$(resp_field nonce)" != "$nonce" ]; then
    reason="unerwartete Antwort (keine gueltige Rueckmeldung von alive.php)"
fi

if [ -z "$reason" ]; then
    # ---- erreichbar ----
    stored=$(resp_field stored) || stored="?"
    cron_age=$(resp_age cron_age_s)
    box_age=$(resp_age box_age_s)
    # Neue Zeilen; ein älteres alive.php kennt sie nicht -> Verhalten wie bisher.
    cron_configured=1
    [ "$(resp_field cron_configured)" = "0" ] && cron_configured=0
    nas_send_failed=0
    [ "$(resp_field nas_send_failed)" = "1" ] && nas_send_failed=1

    if [ "$down_notified_at" -gt 0 ]; then
        down=$(( now - fail_since ))
        last=""
        [ -n "$fail_reason" ] && last="; zuletzt: $fail_reason"
        if bark_send "NAS wieder erreichbar" \
            "$stamp Das NAS antwortet von aussen wieder (nach ca. $(fmt_dur "$down") Ausfall$last)."; then
            down_notified_at=0
        fi
    fi
    if [ "$down_notified_at" -eq 0 ]; then
        fail_count=0; fail_since=0; fail_reason=""
    fi

    # ---- 2) Läuft der Cron-Wächter auf dem NAS noch (und darf er melden)? ----
    box_txt="unbekannt"
    [ "$box_age" -ge 0 ] && box_txt="vor $(fmt_dur "$box_age")"
    cron_issue=""
    if [ "$cron_configured" -eq 0 ]; then
        # Ohne bark_key_status meldet der NAS-Wächter bewusst nichts und
        # schreibt auch kein last_run_at - "lief noch nie" wäre irreführend.
        cron_issue=unconfigured
    elif [ "$cron_age" -lt 0 ] || [ "$cron_age" -gt "$CRON_STALE_S" ]; then
        cron_issue=stale
    fi
    if [ -n "$cron_issue" ]; then
        # Wechselt der Grund, sofort neu melden (sonst stünde der falsche im Push).
        if [ "$cron_notified_at" -eq 0 ] || [ "$cron_issue" != "$cron_reason" ] \
            || repeat_due "$cron_notified_at"; then
            if [ "$cron_issue" = unconfigured ]; then
                title="NAS-Waechter meldet nichts"
                text="$stamp Auf dem NAS ist bark_key_status in config.php nicht gesetzt - der NAS-Waechter meldet dann nichts, ein Ausfall der Box wuerde NICHT gemeldet. Box zuletzt gemeldet: $box_txt. config.php auf dem NAS pruefen."
            else
                title="NAS-Waechter laeuft nicht"
                if [ "$cron_age" -lt 0 ]; then ran="noch nie"; else ran="seit $(fmt_dur "$cron_age") nicht mehr"; fi
                text="$stamp Die DSM-Aufgabe check_offline.php lief $ran - ein Ausfall der Box wuerde NICHT gemeldet. Box zuletzt gemeldet: $box_txt. DSM > Aufgabenplaner pruefen."
            fi
            if bark_send "$title" "$text"; then
                cron_notified_at=$now
                cron_reason=$cron_issue
            fi
        fi
    elif [ "$cron_notified_at" -gt 0 ]; then
        if bark_send "NAS-Waechter laeuft wieder" \
            "$stamp Der NAS-Waechter (check_offline.php) laeuft wieder und meldet Ausfaelle der Box. Box zuletzt gemeldet: $box_txt."; then
            cron_notified_at=0
            cron_reason=""
        fi
    fi

    # ---- 3) Kommt das NAS selbst noch zu Bark durch? ----
    # nas_send_failed=1: Die letzte Meldung des NAS-Wächters an Bark scheiterte
    # und seitdem ging keine durch - z.B. ausgehendes Netz/DNS des NAS kaputt.
    # Dann scheitert auch der manuelle REAL ALARM. Erst nach FAIL_THRESHOLD
    # Läufen in Folge melden (ein einzelner Bark-Schluckauf ist kein Ausfall).
    if [ "$nas_send_failed" -eq 1 ]; then
        sendfail_count=$(( sendfail_count + 1 ))
        [ "$sendfail_since" -eq 0 ] && sendfail_since=$now
        if [ "$sendfail_count" -ge "$FAIL_THRESHOLD" ] \
            && { [ "$sendfail_notified_at" -eq 0 ] || repeat_due "$sendfail_notified_at"; }; then
            if [ "$sendfail_notified_at" -eq 0 ]; then
                title="NAS kann Bark nicht erreichen"
            else
                title="NAS kann Bark weiterhin nicht erreichen"
            fi
            if bark_send "$title" \
                "$stamp Manueller REAL ALARM gefaehrdet: Die letzte Meldung des NAS-Waechters an Bark ist gescheitert, seitdem ging keine durch (von hier beobachtet seit ca. $(fmt_dur $(( now - sendfail_since )))). Erreicht das NAS Bark nicht (Internet/DNS des NAS?), geht auch der REAL ALARM ueber das Dashboard nicht raus. Alarme der Box laufen direkt an Bark. Sonst bark_key_status in config.php pruefen."; then
                sendfail_notified_at=$now
            fi
        fi
    else
        if [ "$sendfail_notified_at" -gt 0 ]; then
            if bark_send "NAS erreicht Bark wieder" \
                "$stamp Der NAS-Waechter kommt wieder zu Bark durch (nach ca. $(fmt_dur $(( now - sendfail_since ))))."; then
                sendfail_notified_at=0
            fi
        fi
        if [ "$sendfail_notified_at" -eq 0 ]; then
            sendfail_count=0; sendfail_since=0
        fi
    fi

    # ---- 4) Eigener Bark-Fehlschlag, aber nichts mehr zu melden? ----
    # Hat sich die gescheiterte Meldung inzwischen erledigt (z.B. NAS wieder
    # da, bevor "NICHT ERREICHBAR" durchkam), bliebe bark_failed_at - und die
    # Warnung im Dashboard - sonst stehen, bis wieder etwas zu melden ist.
    if [ "$bark_failed_at" -gt 0 ] && [ "$bark_tried" -eq 0 ]; then
        bark_send "Aussen-Waechter kann wieder melden" \
            "$stamp Die Cloud-VM kam zuletzt nicht zu Bark durch (erster Fehlschlag vor ca. $(fmt_dur $(( now - bark_failed_at )))) - Meldungen aus dieser Zeit sind evtl. nicht angekommen. Jetzt geht es wieder." \
            || true
    fi

    save_state || exit 1
    echo "OK http=$http stored=$stored cron_age=$cron_age box_age=$box_age cron_configured=$cron_configured nas_send_failed=$nas_send_failed bark_failed_at=$bark_failed_at"
    exit 0
fi

# ---- nicht erreichbar ----
fail_count=$(( fail_count + 1 ))
[ "$fail_since" -eq 0 ] && fail_since=$now
fail_reason=$reason
echo "FAIL $fail_count: $reason"
if [ "$fail_count" -ge "$FAIL_THRESHOLD" ]; then
    down=$(( now - fail_since ))
    if [ "$down_notified_at" -eq 0 ]; then
        title="NAS NICHT ERREICHBAR"
    else
        title="NAS weiterhin nicht erreichbar"
    fi
    if [ "$down_notified_at" -eq 0 ] || repeat_due "$down_notified_at"; then
        if bark_send "$title" \
            "$stamp Das NAS antwortet von aussen seit ca. $(fmt_dur "$down") nicht: $reason. Der manuelle REAL ALARM ueber das Dashboard geht dann nicht. Liegt die Ursache am Standort (Strom/Internet), ist auch die Box betroffen."; then
            down_notified_at=$now
        fi
    fi
fi
save_state || exit 1
exit 0
