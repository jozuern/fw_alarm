package web

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"

	"fwalarm/internal/core"
)

// MachineAuth prüft das Maschinen-Token der Box (PHP:
// require_machine_auth): Es zählt NUR aus dem Rumpf eines POST (f, Feld
// "token") – nie aus Query oder Kopfzeile, die landeten in Protokollen.
// Verglichen wird sein SHA-256 in konstanter Zeit mit machine_token_sha256;
// der Hash darf dort in Groß- oder Kleinbuchstaben und mit Leerraum außen
// stehen (manche Werkzeuge geben ihn groß aus, beim Kopieren rutscht
// Leerraum mit). Ein leeres Token und ein leerer Hash passen nie. false:
// Der Aufrufer antwortet 403 mit dem Rumpf genau "FORBIDDEN" – und das nur
// hierfür.
func MachineAuth(c *core.Core, f *Form) bool {
	if f == nil {
		return false
	}
	tok := f.Get("token")
	expected := strings.ToLower(phpTrim(c.Cfg().MachineTokenSHA256))
	if tok == "" || expected == "" {
		return false
	}
	sum := sha256.Sum256([]byte(tok))
	return subtle.ConstantTimeCompare([]byte(expected), []byte(hex.EncodeToString(sum[:]))) == 1
}
