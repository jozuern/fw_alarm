package web

import (
	"net/http"

	"fwalarm/internal/core"
)

// securityHeaders sind die Sicherheits-Kopfzeilen jeder dynamischen
// Antwort, wörtlich aus dem Kopf von nas_dashboard/lib/common.php: gegen
// Clickjacking (das Dashboard in einem fremden iframe) und MIME-Sniffing,
// eine CSP ohne unsafe-inline, kein Zwischenspeichern (ein gespeichertes
// "ok=1" des Kurzbefehl-Links hieße: der Kollege glaubt, er sei wieder
// laut – der Server hat den Aufruf nie gesehen), keine Suchmaschinen.
var securityHeaders = [...][2]string{
	{"X-Frame-Options", "DENY"},
	{"Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"},
	{"Cache-Control", "no-store"},
	{"X-Content-Type-Options", "nosniff"},
	{"Referrer-Policy", "strict-origin-when-cross-origin"},
	{"Permissions-Policy", "camera=(), microphone=(), geolocation=()"},
	{"X-Robots-Tag", "noindex, nofollow"},
}

// hstsValue: Strict-Transport-Security, nur wenn die Anfrage per HTTPS kam
// (PHP: load_config mit request_is_https).
const hstsValue = "max-age=31536000"

// SecurityHeaders setzt die Sicherheits-Kopfzeilen, bevor next läuft (wie
// PHP ganz oben in common.php) – so tragen sie auch Antworten, die schon
// ParseForm gibt (413). Strict-Transport-Security nur bei IsHTTPS. Auch
// die 500 nach einem Laufzeitfehler in next trägt genau diese Kopfzeilen
// (und keine, die next gesetzt hat): Der Router setzt sie nach dem Leeren
// wieder ein (keepOnPanic).
func SecurityHeaders(c *core.Core, next Handler) Handler {
	return func(w http.ResponseWriter, r *http.Request) {
		https := IsHTTPS(c, r)
		h := w.Header()
		for name, v := range newSecurityHeaders(https) {
			h[name] = v
		}
		keepOnPanic(w, newSecurityHeaders(https))
		next(w, r)
	}
}

// newSecurityHeaders liefert die Kopfzeilen als eigene, frische Tabelle –
// ändert next die Antwort-Kopfzeilen, trifft das die Fassung für die 500
// nicht.
func newSecurityHeaders(https bool) http.Header {
	h := make(http.Header, len(securityHeaders)+1)
	for _, kv := range securityHeaders {
		h.Set(kv[0], kv[1])
	}
	if https {
		h.Set("Strict-Transport-Security", hstsValue)
	}
	return h
}

// keepOnPanic gibt dem Router (trackingWriter) die Kopfzeilen, die auch eine
// 500 nach einem Laufzeitfehler tragen soll. Ohne Router dazwischen (ein
// Test mit Recorder) gibt es niemanden, dem man sie geben könnte.
func keepOnPanic(w http.ResponseWriter, h http.Header) {
	for {
		if tw, ok := w.(*trackingWriter); ok {
			tw.keep = h
			return
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return
		}
		w = u.Unwrap()
	}
}
