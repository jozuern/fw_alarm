package web

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"testing"

	webassets "fwalarm/nas_dashboard"
)

// embeddedFiles nennt jede eingebettete Datei (assets/… und manifest.json).
func embeddedFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	err := fs.WalkDir(webassets.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 10 {
		t.Fatalf("nur %d eingebettete Dateien: %q", len(files), files)
	}
	return files
}

// Jede Datei unter nas_dashboard/assets hat einen Typ aus der festen
// Tabelle und wird genau mit ihm ausgeliefert – nie geraten.
func TestStaticTypes(t *testing.T) {
	want := map[string]string{
		".css": "text/css", ".js": "application/javascript", ".json": "application/json",
		".svg": "image/svg+xml", ".png": "image/png", ".txt": "text/plain",
	}
	if len(contentTypes) != len(want) {
		t.Errorf("Typ-Tabelle %v, erwartet %v", contentTypes, want)
	}
	for ext, ct := range want {
		if contentTypes[ext] != ct {
			t.Errorf("%s: %q, erwartet %q", ext, contentTypes[ext], ct)
		}
	}

	addr := serveTCP(t, NewRouter(testCore(nil)))
	sawManifest := false
	for _, p := range embeddedFiles(t) {
		if p == "manifest.json" {
			sawManifest = true
		} else if !strings.HasPrefix(p, "assets/") {
			t.Errorf("eingebettet, aber nicht unter assets/: %s", p)
		}
		ct, ok := want[path.Ext(p)]
		if !ok {
			t.Errorf("%s: Endung %q hat keinen Typ in der Tabelle", p, path.Ext(p))
			continue
		}
		data, err := webassets.FS.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		a := raw(t, addr, http.MethodGet, "/"+p)
		if a.status != 200 || a.header.Get("Content-Type") != ct || a.body != string(data) {
			t.Errorf("GET /%s: %d, Typ %q (erwartet %q), %d Bytes (erwartet %d)",
				p, a.status, a.header.Get("Content-Type"), ct, len(a.body), len(data))
		}
		if a.header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("GET /%s ohne nosniff", p)
		}
	}
	if !sawManifest {
		t.Error("manifest.json nicht eingebettet")
	}
}

func TestStaticETag(t *testing.T) {
	addr := serveTCP(t, NewRouter(testCore(nil)))
	const p = "/assets/app.css"
	data, err := webassets.FS.ReadFile("assets/app.css")
	if err != nil {
		t.Fatal(err)
	}
	etag := `"` + AssetVersion(p) + `"`

	a := raw(t, addr, http.MethodGet, p)
	if a.status != 200 || a.header.Get("ETag") != etag {
		t.Fatalf("GET: %d, ETag %q (erwartet %s)", a.status, a.header.Get("ETag"), etag)
	}
	for _, inm := range []string{etag, "W/" + etag, `"0000000000", ` + etag, "*"} {
		a := raw(t, addr, http.MethodGet, p, "If-None-Match: "+inm)
		if a.status != http.StatusNotModified || a.body != "" || a.header.Get("ETag") != etag {
			t.Errorf("If-None-Match %s: %d, Rumpf %d Bytes, ETag %q", inm, a.status, len(a.body), a.header.Get("ETag"))
		}
	}
	if a := raw(t, addr, http.MethodGet, p, `If-None-Match: "0000000000"`); a.status != 200 || a.body != string(data) {
		t.Errorf("anderes ETag: %d", a.status)
	}

	a = raw(t, addr, http.MethodHead, p)
	if a.status != 200 || a.body != "" || a.header.Get("Content-Length") != strconv.Itoa(len(data)) ||
		a.header.Get("Content-Type") != "text/css" {
		t.Errorf("HEAD: %d, Rumpf %q, Länge %q, Typ %q", a.status, a.body, a.header.Get("Content-Length"), a.header.Get("Content-Type"))
	}
	if a := raw(t, addr, http.MethodPost, p); a.status != http.StatusMethodNotAllowed || a.header.Get("Allow") != "GET, HEAD" || a.body != "" {
		t.Errorf("POST: %d, Allow %q, Rumpf %q", a.status, a.header.Get("Allow"), a.body)
	}
}

func TestAssetVersionIsDigits(t *testing.T) {
	digits := regexp.MustCompile(`^[0-9]{10}$`)
	for _, p := range embeddedFiles(t) {
		v := AssetVersion(p)
		if !digits.MatchString(v) {
			t.Errorf("%s: Version %q ist keine zehnstellige Zahl", p, v)
		}
		if AssetVersion("/"+p) != v {
			t.Errorf("%s: mit führendem Schrägstrich %q", p, AssetVersion("/"+p))
		}
		data, err := webassets.FS.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if want := fmt.Sprintf("%010d", binary.BigEndian.Uint64(sum[:8])%10_000_000_000); v != want {
			t.Errorf("%s: %s, erwartet %s (erste 8 Bytes des SHA-256 modulo 10^10)", p, v, want)
		}
	}
	if v := AssetVersion("assets/gibt-es-nicht.js"); v != "" {
		t.Errorf("Version einer fehlenden Datei: %q", v)
	}
}

// Ein beliebiges If-None-Match: kein Laufzeitfehler, und ein Treffer nur,
// wenn das ETag (oder "*") wirklich darin steht.
func FuzzIfNoneMatch(f *testing.F) {
	const etag = `"1234567890"`
	for _, s := range []string{etag, "W/" + etag, "*", `"x", ` + etag, "", ",,,", `W/`, `"1234567890`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, header string) {
		if etagMatches([]string{header}, etag) && !strings.Contains(header, etag) && !strings.Contains(header, "*") {
			t.Fatalf("Treffer ohne ETag: %q", header)
		}
	})
}
