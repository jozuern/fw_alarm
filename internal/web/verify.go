package web

import (
	"crypto/subtle"
	"sync"

	"fwalarm/internal/config"
)

// Was bei einem Anmeldeversuch geprüft wird (index.php:64-84, Spec §8):
// genau EINE Passwortprüfung je Versuch, auch für einen unbekannten Namen.
// Wann geprüft wird – Plätze, Reihen, Wartezeit –, regelt slots.go.

// verifyPassword ist die Passwortprüfung – im Programm immer
// config.VerifyPassword. Nur die Tests dieses Pakets setzen eine eigene ein
// (von außen nicht erreichbar), um Prüfungen zu zählen und zu takten.
var verifyPassword = config.VerifyPassword

// passwordTarget sagt, gegen welchen Hash ein Versuch geprüft wird und
// welche Rolle ein Treffer ergibt. Ein Name ist ein Konto, wenn er nicht
// leer ist und genau dem Admin bzw. dem Lese-Benutzer gleicht (zuerst dem
// Admin; der Lese-Benutzer nur mit Hash, index.php:73). Mit brauchbarem Hash
// wird gegen ihn geprüft. Sonst – unbekannter Name oder unbrauchbarer Hash –
// gegen decoyHash, und ein Treffer meldet niemanden an (role ""): Die
// Antwortzeit verrät so nicht, ob es den Namen gibt.
func passwordTarget(cfg *config.Config, user string) (hash, role string) {
	switch {
	case cfg.DashboardUser != "" && sameText(cfg.DashboardUser, user):
		if config.PasswordHashUsable(cfg.DashboardPasswordHash) {
			return cfg.DashboardPasswordHash, "admin"
		}
	case cfg.DashboardReadonlyUser != "" && cfg.DashboardReadonlyPasswordHash != "" &&
		sameText(cfg.DashboardReadonlyUser, user):
		if config.PasswordHashUsable(cfg.DashboardReadonlyPasswordHash) {
			return cfg.DashboardReadonlyPasswordHash, "readonly"
		}
	}
	return decoyHash(cfg), ""
}

// decoyHash ist der Hash für Versuche ohne Konto: der ECHTE Hash des Admins
// (index.php:78-83 – ein fester Wegwerf-Hash mit anderem Aufwand, etwa ein
// Argon2id gegen ein übernommenes bcrypt, verriete über die Antwortzeit, ob
// es den Namen gibt). Ist er unbrauchbar, der des Lese-Benutzers; ist auch
// der es, ein eigener Argon2id-Hash mit den Werten von fw-alarm
// hash-password (dann meldet sich ohnehin niemand an – der Versuch kostet
// trotzdem eine Prüfung wie jeder andere).
func decoyHash(cfg *config.Config) string {
	switch {
	case config.PasswordHashUsable(cfg.DashboardPasswordHash):
		return cfg.DashboardPasswordHash
	case cfg.DashboardReadonlyUser != "" && config.PasswordHashUsable(cfg.DashboardReadonlyPasswordHash):
		return cfg.DashboardReadonlyPasswordHash
	}
	return spareHash()
}

// spareHash: ein Argon2id-Hash über ein zufälliges Passwort, einmal erzeugt
// (beim ersten Bedarf, nicht beim Start).
var spareHash = sync.OnceValue(func() string {
	h, err := config.HashPassword(string(randomBytes(16)))
	if err != nil {
		// Kann nicht eintreten (crypto/rand scheitert nie); dann prüft der
		// Versuch eben gegen nichts.
		return ""
	}
	return h
})

// sameText vergleicht in konstanter Zeit (PHP: hash_equals).
func sameText(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
