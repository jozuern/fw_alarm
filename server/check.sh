#!/usr/bin/env bash
# Prüft den Aufbau des Dashboard-Servers - ohne etwas auszulösen.
#
#     sudo bash check.sh local                      auf der VM (gegen 127.0.0.1:8080)
#     bash server/check.sh public https://alarm.tunero.dev    von überall
#
# Pro Fall eine Zeile "PASS <fall>" oder "FAIL <fall>: <warum>"; Rückgabe 1,
# sobald ein Fall scheitert. Mit CHECK_USER/CHECK_PASS in der Umgebung wird
# zusätzlich die Anmeldung geprüft (nur mit Test-Zugängen benutzen).
#
# Gesendet werden nur harmlose Anfragen: Seitenabrufe, ein Status-Push OHNE
# gültiges Token, Mute-Aufrufe mit einem ungültigen Token (ändern nichts;
# der Fall no-accept zählt als EIN Fehlversuch in der Mute-Bremse des
# Absenders - Grenze 20 in 15 min).
set -uo pipefail

mode="${1:-}"
case "$mode" in
    local)
        base="http://127.0.0.1:8080"
        ;;
    public)
        base="${2:-}"
        base="${base%/}"
        ;;
    *)
        base=""
        ;;
esac
if [ -z "$base" ]; then
    echo "Aufruf: check.sh local | check.sh public <https://adresse>" >&2
    exit 2
fi

VENDOR_PATH="/assets/vendor/htm-preact-3.1.1.umd.js"
VENDOR_SHA256_B64="YsT/6kAAeMDtp/Q1sicmtJYevfdMhQbxiOkr7EHYrFE="

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
chmod 700 "$tmp"
# Unter Git Bash (Windows) braucht curl für "name@datei" einen Windows-Pfad.
ftmp="$tmp"
if command -v cygpath >/dev/null 2>&1; then
    ftmp="$(cygpath -m "$tmp")"
fi

fails=0
pass() { echo "PASS $1"; }
fail() { echo "FAIL $1: $2"; fails=$((fails + 1)); }

# req <curl-Argumente…>: setzt $code (000 = keine Verbindung), Kopfzeilen in
# $tmp/h, Rumpf in $tmp/b.
code=000
req() {
    : > "$tmp/h"
    : > "$tmp/b"
    code="$(curl -sS --max-time 25 -o "$ftmp/b" -D "$ftmp/h" -w '%{http_code}' "$@" 2> "$tmp/e")" || code=000
}
hdr() { grep -i "^$1:" "$tmp/h" | tail -n 1 | sed 's/^[^:]*:[[:space:]]*//' | tr -d '\r'; }
has_hdr() { grep -qi "^$1:" "$tmp/h"; }
# Anfang des Rumpfs in einer Zeile (für die Fehlermeldung).
snip() { head -c 80 "$tmp/b" | tr '\r\n' '  '; }
why() { echo "HTTP $code $(snip)$(head -c 120 "$tmp/e" | tr '\r\n' '  ')"; }

# --- login-page + headers (dieselbe Antwort) --------------------------------
req "$base/"
if [ "$code" = 200 ] && grep -q 'login-card' "$tmp/b"; then
    pass login-page
else
    fail login-page "$(why)"
fi
miss=""
has_hdr 'Content-Security-Policy' || miss="$miss Content-Security-Policy"
hdr 'Cache-Control' | grep -qi 'no-store' || miss="$miss Cache-Control:no-store"
if [ "$mode" = public ]; then
    has_hdr 'Strict-Transport-Security' || miss="$miss Strict-Transport-Security"
fi
if [ -z "$miss" ]; then pass headers; else fail headers "fehlt:$miss"; fi

# --- classic (alte Lesezeichen: nur noch 302 auf index.php) ---------------
req "$base/classic.php"
loc="$(hdr Location)"
case "$code $loc" in
    "302 index.php" | "302 "*/index.php) pass classic ;;
    *) fail classic "$(why) Location: ${loc:-fehlt}" ;;
esac

# --- api-401 --------------------------------------------------------------
req "$base/dashboard_api.php?action=status"
if [ "$code" = 401 ]; then pass api-401; else fail api-401 "$(why)"; fi

# --- health (nur auf der VM erreichbar) -------------------------------------
req "$base/api/health.php"
if [ "$mode" = local ]; then
    if [ "$code" = 200 ] && tr -d '\r' < "$tmp/b" | grep -qx 'ok=1'; then
        pass health
    else
        fail health "$(why)"
    fi
else
    if [ "$code" = 404 ]; then pass health-hidden; else fail health-hidden "$(why)"; fi
fi

# --- status-403 (Status-Push ohne Token) ------------------------------------
req -d '' "$base/api/status.php"
if [ "$code" = 403 ]; then pass status-403; else fail status-403 "$(why)"; fi

# --- deny -------------------------------------------------------------------
bad=""
for p in /config.php /data/index.php /lib/common.php /cron/check_offline.php /SETUP.md; do
    req "$base$p"
    [ "$code" = 404 ] || bad="$bad $p=$code"
done
if [ -z "$bad" ]; then pass deny; else fail deny "nicht 404:$bad"; fi

# --- mime -------------------------------------------------------------------
bad=""
req "$base/assets/app/main.js"
case "$code $(hdr 'Content-Type')" in
    "200 application/javascript"*) ;;
    *) bad="$bad main.js=$code/$(hdr 'Content-Type')" ;;
esac
req "$base/manifest.json"
case "$code $(hdr 'Content-Type')" in
    "200 application/json"*) ;;
    *) bad="$bad manifest.json=$code/$(hdr 'Content-Type')" ;;
esac
req "$base$VENDOR_PATH"
sum="$(openssl dgst -sha256 -binary < "$tmp/b" | openssl base64)"
if [ "$code" != 200 ] || [ "$sum" != "$VENDOR_SHA256_B64" ]; then
    bad="$bad vendor=$code/$sum"
fi
if [ -z "$bad" ]; then pass mime; else fail mime "$bad"; fi

if [ "$mode" = public ]; then
    # --- esp32-ua: Aufruf wie von der Box (HTTP/1.1, TLS 1.2, kein Accept) --
    # Erwartet: das 403 der APP ("FORBIDDEN"), keine Sperrseite von Cloudflare.
    req --http1.1 --tlsv1.2 --tls-max 1.2 -A 'ESP32HTTPClient' -H 'Accept:' \
        -d 'token=check-sh-kein-token&device_id=check-sh' "$base/api/status.php"
    if [ "$code" = 403 ] && [ "$(tr -d '\r\n' < "$tmp/b")" = "FORBIDDEN" ] \
        && ! has_hdr 'cf-mitigated' && ! grep -qi 'cloudflare' "$tmp/b"; then
        pass esp32-ua
    else
        fail esp32-ua "$(why) cf-mitigated=$(hdr 'cf-mitigated')"
    fi

    # --- no-accept: Kurzbefehl ohne Accept-Kopfzeile ------------------------
    # Erwartet: die Fehlerantwort der APP (ok=0 …), nicht Cloudflare.
    req -A 'Kurzbefehle/1.0' -H 'Accept:' "$base/api/mute.php?t=invalid&state=on"
    if { [ "$code" = 404 ] || [ "$code" = 429 ]; } && head -n 1 "$tmp/b" | tr -d '\r' | grep -qx 'ok=0' \
        && ! has_hdr 'cf-mitigated'; then
        pass no-accept
    else
        fail no-accept "$(why) cf-mitigated=$(hdr 'cf-mitigated')"
    fi

    # --- tls12: Die Box kann nur TLS 1.2 ------------------------------------
    req --tlsv1.2 --tls-max 1.2 -I "$base/"
    if [ "$code" = 200 ]; then pass tls12; else fail tls12 "$(why)"; fi
fi

# --- head-mute: Link-Vorschau per HEAD darf nichts kaputt machen -------------
# (curl schreibt bei -I die Kopfzeilen auch in die Rumpf-Datei; einen Rumpf
# liest es bei HEAD nie. Geprüft wird: Antwort kommt, kein 5xx, und sie sagt
# selbst, dass sie keinen Text mitschickt bzw. die Vorschau-Antwort ist.)
req --http1.1 -I "$base/api/mute.php?t=invalid&state=on"
if [ "$code" != 000 ] && [ "$code" -lt 500 ]; then
    pass head-mute
else
    fail head-mute "HTTP $code $(head -c 120 "$tmp/e" | tr '\r\n' '  ')"
fi

# --- session-dir (nur auf der VM, braucht root) -----------------------------
if [ "$mode" = local ]; then
    st="$(stat -c '%U %a' /srv/fw-alarm/sessions 2>/dev/null || true)"
    dump="$(php-fpm8.3 -tt 2>&1 || true)"
    bad=""
    [ "$st" = "fwalarm 700" ] || bad="$bad Ordner=${st:-fehlt}"
    grep -q 'session\.save_path\] = /srv/fw-alarm/sessions$' <<< "$dump" || bad="$bad save_path"
    grep -q 'session\.gc_maxlifetime\] = 43200$' <<< "$dump" || bad="$bad gc_maxlifetime"
    grep -q 'session\.gc_probability\] = 1$' <<< "$dump" || bad="$bad gc_probability"
    if [ -z "$bad" ]; then
        pass session-dir
    elif [ "$(id -u)" -ne 0 ]; then
        fail session-dir "$bad (als root ausführen: sudo bash check.sh local)"
    else
        fail session-dir "$bad"
    fi
fi

# --- login (nur mit CHECK_USER/CHECK_PASS) ----------------------------------
if [ -n "${CHECK_USER:-}" ]; then
    # Passwort über eine Datei übergeben, nicht in der Prozessliste zeigen.
    printf '%s' "${CHECK_PASS:-}" > "$tmp/p"
    req -c "$ftmp/jar" -H 'Sec-Fetch-Site: same-origin' \
        --data-urlencode 'action=login' --data-urlencode "user=$CHECK_USER" \
        --data-urlencode "password@$ftmp/p" "$base/"
    rm -f "$tmp/p"
    cookie="$(grep -i '^Set-Cookie:' "$tmp/h" | grep -vi 'deleted' | tail -n 1 | tr -d '\r')"
    bad=""
    [ "$code" = 302 ] || bad="$bad HTTP=$code"
    [ "$(hdr 'Location')" = "index.php" ] || bad="$bad Location=$(hdr 'Location')"
    grep -qi 'HttpOnly' <<< "$cookie" || bad="$bad HttpOnly"
    grep -qiE 'path=/(;|$)' <<< "$cookie" || bad="$bad Path=/"
    grep -qi 'expires=' <<< "$cookie" || bad="$bad Expires"
    if [ "$mode" = public ]; then
        grep -qiE '; *secure' <<< "$cookie" || bad="$bad Secure"
    fi
    if [ -z "$bad" ]; then
        req -b "$ftmp/jar" "$base/dashboard_api.php?action=status"
        if [ "$code" != 200 ] || [ "$(head -c 1 "$tmp/b")" != "{" ]; then
            bad="$bad status-nach-login=$code"
        fi
    fi
    if [ -z "$bad" ]; then pass login; else fail login "$bad"; fi
fi

[ "$fails" -eq 0 ]
