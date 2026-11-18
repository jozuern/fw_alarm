package web

import (
	"crypto/tls"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"fwalarm/internal/config"
)

// Die Sicherheits-Kopfzeilen, Buchstabe für Buchstabe aus dem Kopf von
// nas_dashboard/lib/common.php (Strict-Transport-Security aus
// load_config()).
var phpSecurityHeaders = map[string]string{
	"X-Frame-Options":         "DENY",
	"Content-Security-Policy": "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'",
	"Cache-Control":           "no-store",
	"X-Content-Type-Options":  "nosniff",
	"Referrer-Policy":         "strict-origin-when-cross-origin",
	"Permissions-Policy":      "camera=(), microphone=(), geolocation=()",
	"X-Robots-Tag":            "noindex, nofollow",
}

const phpHSTS = "max-age=31536000"

// checkSecurityHeaders verlangt jede Sicherheits-Kopfzeile genau einmal
// mit genau dem Wert aus PHP; HSTS genau dann, wenn hsts.
func checkSecurityHeaders(t *testing.T, what string, h http.Header, hsts bool) {
	t.Helper()
	for name, want := range phpSecurityHeaders {
		if got := h.Values(name); len(got) != 1 || got[0] != want {
			t.Errorf("%s: %s = %q, erwartet %q", what, name, got, want)
		}
	}
	got := h.Values("Strict-Transport-Security")
	switch {
	case hsts && (len(got) != 1 || got[0] != phpHSTS):
		t.Errorf("%s: Strict-Transport-Security = %q, erwartet %q", what, got, phpHSTS)
	case !hsts && len(got) != 0:
		t.Errorf("%s: Strict-Transport-Security ohne HTTPS: %q", what, got)
	}
}

func TestSecurityHeaders(t *testing.T) {
	c := cfgCore(&config.Config{TrustedProxies: []string{"127.0.0.1"}, ProxyClientIPHeader: "X-Forwarded-For"})
	var seen http.Header
	h := SecurityHeaders(c, func(w http.ResponseWriter, r *http.Request) {
		seen = w.Header().Clone() // schon gesetzt, bevor der Handler läuft
		w.Header().Set("X-Eigen", "1")
		Text(w, 200, "ok")
	})
	tlsReq := from("203.0.113.5:4711")
	tlsReq.TLS = &tls.ConnectionState{}
	for _, c := range []struct {
		what string
		r    *http.Request
		hsts bool
	}{
		{"direkt, ohne HTTPS", from("203.0.113.5:4711"), false},
		{"nicht vertrauter Absender mit X-Forwarded-Proto: https", from("203.0.113.5:4711", "X-Forwarded-Proto: https"), false},
		{"vertrauter Proxy ohne HTTPS", from("127.0.0.1:5000", "X-Forwarded-Proto: http"), false},
		{"vertrauter Proxy mit HTTPS", from("127.0.0.1:5000", "X-Forwarded-Proto: https"), true},
		{"Unix-Socket mit HTTPS", overUnix(from("@", "X-Forwarded-Proto: https")), true},
		{"direkte TLS-Verbindung", tlsReq, true},
	} {
		rec := httptest.NewRecorder()
		h(rec, c.r)
		checkSecurityHeaders(t, c.what, rec.Header(), c.hsts)
		checkSecurityHeaders(t, c.what+" (vor dem Handler)", seen, c.hsts)
		if rec.Code != 200 || rec.Body.String() != "ok" || rec.Header().Get("X-Eigen") != "1" ||
			rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Errorf("%s: Antwort des Handlers %d %q %v", c.what, rec.Code, rec.Body.String(), rec.Header())
		}
	}

	// Auch eine Antwort, die ParseForm selbst gibt (413), trägt sie.
	big := SecurityHeaders(c, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := ParseForm(w, r); ok {
			Text(w, 200, "angenommen")
		}
	})
	rec := httptest.NewRecorder()
	big(rec, postForm("/", formType, strings.Repeat("a", 70<<10)))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("413 erwartet, %d", rec.Code)
	}
	checkSecurityHeaders(t, "413", rec.Header(), false)
}

// Nach einem Laufzeitfehler im Handler trägt die 500 weiter alle
// Sicherheits-Kopfzeilen mit den Werten aus PHP (auch HSTS und no-store) –
// und nichts, was der Handler selbst gesetzt oder geändert hatte.
func TestSecurityHeadersSurvivePanic(t *testing.T) {
	var out syncBuffer
	c := cfgCore(&config.Config{TrustedProxies: []string{"127.0.0.1"}})
	c.Log = slog.New(slog.NewTextHandler(&out, nil))
	rt := NewRouter(c)
	rt.Handle("/kaputt", SecurityHeaders(c, func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Set-Cookie", "boss925_dashboard=geheim-sitzung; Path=/")
		h.Set("Location", "index.php")
		h.Set("Cache-Control", "public, max-age=600")
		h.Del("X-Frame-Options")
		h["Content-Security-Policy"][0] = "default-src *" // auch eine geänderte Liste trifft die 500 nicht
		panic("geheim-aus-der-anfrage")
	}))
	addr := serveTCP(t, rt)
	for _, https := range []bool{false, true} {
		header := []string{}
		if https {
			header = append(header, "X-Forwarded-Proto: https")
		}
		a := raw(t, addr, http.MethodGet, "/kaputt?t=geheim-token", header...)
		if a.status != 500 || a.body != "" {
			t.Errorf("https=%v: %d %q", https, a.status, a.body)
		}
		checkSecurityHeaders(t, "500", a.header, https)
		for _, name := range []string{"Content-Type", "Set-Cookie", "Location"} {
			if v := a.header.Get(name); v != "" {
				t.Errorf("https=%v: 500 mit %s: %q", https, name, v)
			}
		}
	}
	if log := out.String(); strings.Count(log, "Laufzeitfehler in einer Anfrage abgefangen") != 2 || strings.Contains(log, "geheim") {
		t.Errorf("Journal: %s", log)
	}
}

// Die Kopfzeilen sind genau die aus dem Block "if (!headers_sent()) { … }"
// ganz oben in common.php – dieselben Namen (keine mehr, keine weniger),
// dieselben Werte –, dazu Strict-Transport-Security aus load_config().
// Solange es die PHP-Fassung gibt: Aufgabe 37 entfernt sie, dann entfällt
// dieser Vergleich (der Test meldet sich dann als übersprungen).
func TestSecurityHeadersMatchPHP(t *testing.T) {
	data, err := os.ReadFile("../../nas_dashboard/lib/common.php")
	if os.IsNotExist(err) {
		t.Skip("PHP-Fassung nicht mehr da")
	}
	if err != nil {
		t.Fatal(err)
	}
	src := strings.ReplaceAll(string(data), "\r\n", "\n") // unter Windows mit CRLF ausgecheckt
	// header('Name: Wert'); oder header("Name: Wert"); – der Wert der CSP
	// steht in doppelten Anführungszeichen und enthält einfache.
	re := regexp.MustCompile(`header\((?:'([A-Za-z-]+): ([^']*)'|"([A-Za-z-]+): ([^"]*)")\);`)

	const open = "\nif (!headers_sent()) {\n"
	start := strings.Index(src, open)
	if start < 0 {
		t.Fatal("Block if (!headers_sent()) { … } nicht gefunden")
	}
	length := strings.Index(src[start+len(open):], "\n}\n")
	if length < 0 {
		t.Fatal("Ende des Blocks nicht gefunden")
	}
	block := src[start+len(open) : start+len(open)+length]
	found := map[string]string{}
	matches := re.FindAllStringSubmatch(block, -1)
	for _, m := range matches {
		name, value := m[1]+m[3], m[2]+m[4]
		if _, dup := found[name]; dup {
			t.Errorf("%s zweimal im Block", name)
		}
		found[name] = value
	}
	if calls := strings.Count(block, "header("); calls != len(matches) {
		t.Errorf("%d Aufrufe von header() im Block, %d davon lesbar", calls, len(matches))
	}
	if len(found) != len(phpSecurityHeaders) {
		t.Errorf("im Block: %q, in Go: %q", found, phpSecurityHeaders)
	}
	for name, value := range phpSecurityHeaders {
		if got, ok := found[name]; !ok || got != value {
			t.Errorf("%s: PHP %q, Go %q", name, got, value)
		}
	}

	// HSTS steht in load_config(), nur bei HTTPS.
	var hsts []string
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		if m[1]+m[3] == "Strict-Transport-Security" {
			hsts = append(hsts, m[2]+m[4])
		}
	}
	if len(hsts) != 1 || hsts[0] != phpHSTS {
		t.Errorf("Strict-Transport-Security in common.php: %q, Go %q", hsts, phpHSTS)
	}
}
