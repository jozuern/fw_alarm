package web

import (
	"crypto/rand"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"fwalarm/internal/core"
)

// Was Sitzungs-Cookie und Geräte-Ausweis teilen: Lesen und Setzen eines
// Cookies, Zufall.

// maxSameNameCookies: So viele Cookies eines Namens sieht der Server an.
// Schickt der Browser mehrere (eine Seite unter einer Nachbar-Adresse kann
// eines mit längerem Pfad unterschieben, das er dann zuerst schickt), zählt
// unter ihnen beim Sitzungs-Cookie das erste, das eine lebende Sitzung nennt,
// beim Geräte-Ausweis der gültige der besten Klasse (Admin vor Lese-Konto) –
// so verdeckt ein untergeschobenes das echte nicht. Die Grenze hält die
// Arbeit je Anfrage klein (je Wert höchstens zwei HMAC bzw. ein SHA-256).
const maxSameNameCookies = 4

// firstCookies liefert die Werte der ersten maxSameNameCookies Cookies namens
// name, in der Reihenfolge der Anfrage (jedes zählt, auch eines, das danach
// nicht passt).
func firstCookies(r *http.Request, name string) []string {
	var vals []string
	for _, ck := range r.CookiesNamed(name) {
		if len(vals) == maxSameNameCookies {
			break
		}
		vals = append(vals, ck.Value)
	}
	return vals
}

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
