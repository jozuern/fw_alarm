package web

import (
	"crypto/rand"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"fwalarm/internal/core"
)

// Was Sitzungs-Cookie und Geräte-Ausweis teilen: Eigenschaften und Setzen
// eines Cookies, Zufall.

// putCookie ergänzt ck um die Eigenschaften, die jedes Cookie des Servers
// trägt (PHP: dashboard_cookie_options – Pfad hier "/", der Server hat nur
// diesen einen Ort; HttpOnly, SameSite=Strict, Secure laut IsHTTPS), und
// ersetzt ein Cookie desselben Namens, das diese Antwort schon trägt: Eine
// Antwort setzt jedes höchstens einmal (die Erneuerung durch Resume weicht
// der Abmeldung oder der neuen ID).
func putCookie(c *core.Core, w http.ResponseWriter, r *http.Request, ck *http.Cookie) {
	ck.Path = "/"
	ck.HttpOnly = true
	ck.SameSite = http.SameSiteStrictMode
	ck.Secure = IsHTTPS(c, r)
	h := w.Header()
	h["Set-Cookie"] = slices.DeleteFunc(h["Set-Cookie"], func(line string) bool {
		return strings.HasPrefix(line, ck.Name+"=")
	})
	h.Add("Set-Cookie", ck.String())
}

// randomBytes liefert n Bytes aus crypto/rand (scheitert nie: Kann das
// System keinen Zufall liefern, beendet Go das Programm, statt eine
// vorhersagbare ID auszugeben).
func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b) // liefert seit Go 1.24 nie einen Fehler
	return b
}

func logger(c *core.Core) *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}
