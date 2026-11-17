#!/usr/bin/env bash
# Lässt die Server-Tests (tests/backend/) gegen die Go-Fassung laufen.
#
# Aufruf im Repo (Git Bash oder Linux):
#     bash tools/test-go.sh                      die Dateien aus tools/go-green.txt
#     bash tools/test-go.sh <datei> [<datei>…]   genau diese Testdateien
#
# Baut zuerst bin/fw-alarm wie das Produkt (ohne CGO, -trimpath, mit den
# mitgelieferten Modulen aus vendor/), dann
#     FW_BACKEND=go FW_GO_BIN=<absoluter Pfad> node --test <dateien>
# tools/go-green.txt: je Zeile eine Testdatei, Pfad ab dem Repo
# (z.B. tests/backend/go-driver.test.mjs); leere Zeilen und Zeilen mit #
# zählen nicht. Steht dort nichts, gibt es nichts zu tun.
set -euo pipefail

# Unter Windows liegt Go pro Benutzer installiert und nicht im PATH.
for dir in "$HOME/go/bin" "$HOME/AppData/Local/Programs/go/bin"; do
    if [ -d "$dir" ]; then PATH="$dir:$PATH"; fi
done

cd "$(dirname "$0")/.."

exe=""
if [ "$(go env GOOS)" = "windows" ]; then exe=".exe"; fi
mkdir -p bin
# Gebaut wie das Produkt: ohne CGO (unter Linux mit gcc würde sonst cgo
# gelinkt und das geprüfte Programm wiche vom ausgelieferten ab).
CGO_ENABLED=0 go build -mod=vendor -trimpath -o "bin/fw-alarm$exe" ./cmd/fw-alarm

files=()
if [ "$#" -gt 0 ]; then
    files=("$@")
else
    while IFS= read -r line || [ -n "$line" ]; do
        line="${line%$'\r'}"
        case "$line" in '' | '#'*) continue ;; esac
        files+=("$line")
    done < tools/go-green.txt
fi
if [ "${#files[@]}" -eq 0 ]; then
    echo "tools/go-green.txt nennt noch keine Testdatei – nichts zu tun."
    exit 0
fi

# Node unter Windows versteht keine /c/…-Pfade: dann die Form C:/… nehmen.
bin="$(pwd)/bin/fw-alarm$exe"
if command -v cygpath >/dev/null 2>&1; then bin="$(cygpath -m "$bin")"; fi

FW_BACKEND=go FW_GO_BIN="$bin" node --test "${files[@]}"
