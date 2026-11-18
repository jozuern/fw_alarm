package box

import (
	"net/http"
	"strconv"
	"strings"

	"fwalarm/internal/core"
	"fwalarm/internal/mutes"
	"fwalarm/internal/web"
)

// KeysHandler ist api/keys.php: die Empfängerliste für die Box. Die Box ruft
// das nur, wenn die Version aus dem Poll von ihrer abweicht; sie speichert,
// was hier kommt, und alarmiert danach genau diese Keys. In dieser
// Reihenfolge:
//
//  1. Maschinen-Token aus dem Rumpf (web.MachineAuth), sonst 403 FORBIDDEN.
//  2. Überfällige Stummschaltungen zurück auf laut (mutes.Expire) – mit der
//     Frist der Box; ein Fehler ändert nichts an der Antwort. Erst dadurch
//     entsteht für sie eine neue Version.
//  3. Die Antwort aus EINEM Schnappschuss – Version und Inhalt passen immer
//     zusammen: ok, nonce, version, count, je Empfänger key<i>, muted<i>,
//     mutevol<i>. Die Empfänger sind model.State.Effective: nur gültige Keys,
//     keine doppelten, höchstens 10 (genau, was die Firmware annimmt);
//     überfällige Stummschaltungen schon laut; im Demo-Modus nur der
//     Demo-Empfänger mit seinem Stumm-Zustand. Namen, ids, Telefonnummern,
//     Kurzbefehl-Tokens und "dauerhaft" gehen NIE an die Box – und die
//     Not-Liste der Konfiguration ebenso wenig.
//  4. Notbetrieb: version=0, count=0 – die Box verwirft das und behält ihre
//     gespeicherte Liste.
func KeysHandler(c *core.Core) web.Handler {
	return func(w http.ResponseWriter, r *http.Request) {
		f, ok := web.ParseForm(w, r)
		if !ok {
			return
		}
		if !web.MachineAuth(c, f) {
			forbidden(w)
			return
		}
		nonce := Nonce(f.Get("nonce"))
		ctx, cancel := web.Budget(web.BudgetBox)
		_ = mutes.Expire(ctx, c) // ein Fehler ändert nichts an der Antwort (Expire schreibt ihn ins Journal)
		cancel()
		web.Text(w, http.StatusOK, keysAnswer(c, nonce))
	}
}

// keysAnswer baut die Zeilen aus EINEM Schnappschuss des Zustands.
func keysAnswer(c *core.Core, nonce string) string {
	var b strings.Builder
	b.WriteString("ok=1\nnonce=" + nonce + "\n")
	if c.Store.Emergency() {
		b.WriteString("version=0\ncount=0\n")
		return b.String()
	}
	st := c.Store.State()
	// Nur Key, Stumm-Zustand und Stumm-Lautstärke der Empfänger gehen hinaus –
	// id und Name aus model.Target braucht nur der Alarm des Servers.
	targets := st.Effective(c.Clock.Now(), st.MuteMax(c.Cfg().MuteMaxSeconds))
	b.WriteString("version=" + strconv.Itoa(st.KeysVersion) + "\ncount=" + strconv.Itoa(len(targets)) + "\n")
	for i, t := range targets {
		n := strconv.Itoa(i)
		b.WriteString("key" + n + "=" + t.Key + "\n")
		b.WriteString("muted" + n + "=" + boolDigit(t.Muted) + "\n")
		b.WriteString("mutevol" + n + "=" + strconv.Itoa(t.MuteVolume) + "\n")
	}
	return b.String()
}

// boolDigit: 1 oder 0 (PHP: empty($entry['muted']) ? '0' : '1').
func boolDigit(v bool) string {
	if v {
		return "1"
	}
	return "0"
}
