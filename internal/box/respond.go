// Package box sind die Adressen der Box (PHP: api/status.php, poll.php,
// ack.php, keys.php): Klartext-Zeilen (name=wert), die Nonce gespiegelt, das
// Maschinen-Token nur aus dem Rumpf eines POST, ein falsches Token = 403 mit
// dem Rumpf genau "FORBIDDEN" (die Box wertet genau das als "Token
// abgelehnt" und warnt per Bark) – und für nichts anderes. Die Antworten
// warten nie auf Bark und auf die Platte höchstens bis zur Frist der Box
// (web.BudgetBox): Die Box gibt nach 4 s auf, die Brücke nach 3 s.
package box

import (
	"net/http"

	"fwalarm/internal/core"
	"fwalarm/internal/web"
)

// maxNonce: so viele Bytes der Nonce werden gespiegelt.
const maxNonce = 80

// Nonce: die Nonce der Box zum Spiegeln – ihre ersten 80 Bytes, davon nur
// A-Z a-z 0-9 _ - (PHP: substr auf 80, zählt Bytes, dann preg_replace mit
// [^a-zA-Z0-9_-] durch nichts). Die Box prüft, dass ihre Nonce zurückkommt –
// so passt keine alte Antwort auf eine neue Frage.
func Nonce(raw string) string {
	if len(raw) > maxNonce {
		raw = raw[:maxNonce]
	}
	out := make([]byte, 0, len(raw))
	for i := range len(raw) {
		c := raw[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' || c == '-' {
			out = append(out, c)
		}
	}
	return string(out)
}

// KeysVersion: die Listen-Version, die die Box sieht (Poll und api/keys.php);
// 0 im Notbetrieb – die Box verwirft eine Liste unter Version 0 und behält
// ihre gespeicherte (Spec §6.3).
func KeysVersion(c *core.Core) int {
	if c.Store.Emergency() {
		return 0
	}
	return c.Store.State().KeysVersion
}

// forbidden: falsches oder fehlendes Maschinen-Token (PHP:
// require_machine_auth) – 403, Rumpf genau "FORBIDDEN".
func forbidden(w http.ResponseWriter) { web.Text(w, http.StatusForbidden, "FORBIDDEN") }
