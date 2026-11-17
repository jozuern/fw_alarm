#!/usr/bin/env bash
# Prüfungen der Go-Fassung – dieselben wie bei GitHub (.github/workflows/ci.yml),
# ohne den Race-Detektor (er braucht einen C-Compiler, den der PC nicht hat).
#
# Aufruf im Repo (Git Bash oder Linux):  bash tools/check-go.sh
# Eine Zeile je Prüfung; bei der ersten, die scheitert, deren Ausgabe und
# Rückgabe 1.
set -uo pipefail

# Unter Windows liegt Go pro Benutzer installiert und nicht im PATH; die
# Werkzeuge (staticcheck, govulncheck) installiert "go install" nach ~/go/bin.
for dir in "$HOME/go/bin" "$HOME/AppData/Local/Programs/go/bin"; do
    if [ -d "$dir" ]; then PATH="$dir:$PATH"; fi
done

cd "$(dirname "$0")/.." || exit 1

check() {
    local name="$1"
    shift
    local out
    if out="$("$@" 2>&1)"; then
        printf 'ok      %s\n' "$name"
    else
        printf 'FEHLER  %s\n%s\n' "$name" "$out"
        exit 1
    fi
}

# gofmt -l meldet unformatierte Dateien, scheitert aber nicht daran.
gofmt_clean() {
    local out
    out="$(gofmt -l cmd internal nas_dashboard)" || return 1
    if [ -n "$out" ]; then
        printf 'nicht gofmt-formatiert:\n%s\n' "$out"
        return 1
    fi
}

vet_linux_arm64() { GOOS=linux GOARCH=arm64 go vet ./...; }

check "gofmt -l cmd internal nas_dashboard" gofmt_clean
# go.mod/go.sum so, wie go mod tidy sie schriebe (sonst Unterschied und Fehler).
check "go mod tidy -diff" go mod tidy -diff
check "go vet ./..." go vet ./...
check "GOOS=linux GOARCH=arm64 go vet ./..." vet_linux_arm64
check "staticcheck ./..." staticcheck ./...
check "govulncheck ./..." govulncheck ./...
check "go test -mod=vendor ./..." go test -mod=vendor ./...
