package web

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"

	webassets "fwalarm/nas_dashboard"
)

// contentTypes ist die feste Typ-Tabelle der statischen Dateien (wie die
// mime.types von Nginx, ohne charset). Eine Endung, die hier fehlt, wird
// nicht ausgeliefert – geraten wird nie.
var contentTypes = map[string]string{
	".css":  "text/css",
	".js":   "application/javascript",
	".json": "application/json",
	".svg":  "image/svg+xml",
	".png":  "image/png",
	".txt":  "text/plain",
}

// asset ist eine eingebettete Datei, fertig zum Ausliefern.
type asset struct {
	body    []byte
	ctype   string // "" = Endung nicht in der Tabelle
	version string // zehn Ziffern
	etag    string // "<version>"
}

// embedded liest die eingebetteten Dateien einmal (sie liegen im Programm,
// nicht auf der Platte). Schlüssel: Pfad ohne führenden Schrägstrich
// ("assets/app.css", "manifest.json").
var embedded = sync.OnceValue(func() map[string]*asset {
	m := map[string]*asset{}
	// Ein eingebettetes Dateisystem kann beim Durchgehen nicht scheitern;
	// wäre es doch so, fehlte die Datei in der Tabelle (404), und
	// TestStaticTypes fiele auf.
	_ = fs.WalkDir(webassets.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, err := webassets.FS.ReadFile(p)
		if err != nil {
			return nil
		}
		v := versionOf(data)
		m[p] = &asset{body: data, ctype: contentTypes[path.Ext(p)], version: v, etag: `"` + v + `"`}
		return nil
	})
	return m
})

// versionOf: die ersten 8 Bytes des SHA-256 als vorzeichenlose Zahl modulo
// 10^10, zehnstellig mit führenden Nullen (die Szenarien verlangen Ziffern).
func versionOf(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%010d", binary.BigEndian.Uint64(sum[:8])%10_000_000_000)
}

// AssetVersion liefert den Versions-Parameter (?v=) einer eingebetteten
// Datei – "assets/app.css" oder "/assets/app.css"; "" für eine Datei, die
// es nicht gibt.
func AssetVersion(p string) string {
	if a := embedded()[strings.TrimPrefix(p, "/")]; a != nil {
		return a.version
	}
	return ""
}

// staticHandler liefert eine eingebettete Datei aus: GET und HEAD, Typ aus
// der Tabelle, nosniff, ETag mit 304 bei passendem If-None-Match.
func staticHandler(a *asset) Handler {
	length := strconv.Itoa(len(a.body))
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		h.Set("ETag", a.etag)
		if etagMatches(r.Header.Values("If-None-Match"), a.etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		h.Set("Content-Type", a.ctype)
		h.Set("Content-Length", length)
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(a.body) // scheitert nur, wenn der Aufrufer schon fort ist
	}
}

// etagMatches: schwacher Vergleich wie für If-None-Match vorgesehen (W/
// zählt nicht, "*" passt immer, mehrere Werte durch Kommas getrennt).
func etagMatches(lines []string, etag string) bool {
	for _, line := range lines {
		for part := range strings.SplitSeq(line, ",") {
			tag := strings.TrimSpace(part)
			if tag == "*" || strings.TrimPrefix(tag, "W/") == etag {
				return true
			}
		}
	}
	return false
}
