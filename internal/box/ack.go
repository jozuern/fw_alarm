package box

import (
	"errors"
	"net/http"

	"fwalarm/internal/core"
	"fwalarm/internal/model"
	"fwalarm/internal/store"
	"fwalarm/internal/web"
)

const (
	// maxAckResult, maxAckMessage: so viele Bytes von Ergebnis und Meldung
	// einer Quittung werden gespeichert (PHP: substr auf 80 bzw. 200).
	maxAckResult  = 80
	maxAckMessage = 200
)

// AckHandler ist api/ack.php: Die Box quittiert einen ausgelieferten Befehl
// (Ergebnis executed, skipped_busy, duplicate_ignored …). In dieser
// Reihenfolge:
//
//  1. Maschinen-Token aus dem Rumpf (web.MachineAuth), sonst 403 FORBIDDEN –
//     und nur dann 403.
//  2. id wie PHP (int); result höchstens 80 Bytes (fehlt es: "unknown"),
//     message höchstens 200, device_id höchstens 80 – nie mitten in einem
//     Zeichen (web.ClipBytes).
//  3. EINE Änderung (Store.Update) mit der Frist der Box: model.State.Ack auf
//     dem Entwurf – quittiert (Eintrag acked), oder eine verspätete Quittung
//     (Eintrag late_ack mit dem Typ, soweit bekannt). Dieselbe Quittung
//     noch einmal ändert nichts und sichert nichts.
//  4. Antwort IMMER 200 "OK\n" – auch wenn nichts gesichert werden kann
//     (dann eine Zeile im Journal, und der Befehl bleibt "ausgeliefert"; das
//     Dashboard zeigt das ehrlich als "Bestätigung fehlt"). Mit einem Fehler
//     wiederholte die Box die Quittung bis zu ihrer Grenze, pollte so lange
//     nicht (keine neue Liste, kein Demo→LIVE) und pushte im Fehler-Backoff
//     keinen Status mehr.
func AckHandler(c *core.Core) web.Handler {
	return func(w http.ResponseWriter, r *http.Request) {
		f, ok := web.ParseForm(w, r)
		if !ok {
			return
		}
		if !web.MachineAuth(c, f) {
			forbidden(w)
			return
		}
		id := int(model.IntOf(f.Get("id")))
		result := "unknown"
		if f.Has("result") {
			result = web.ClipBytes(f.Get("result"), maxAckResult)
		}
		message := web.ClipBytes(f.Get("message"), maxAckMessage)
		device := web.ClipBytes(f.Get("device_id"), maxBoardID)
		now := c.Clock.Now()
		ctx, cancel := web.Budget(web.BudgetBox)
		defer cancel()
		err := c.Store.Update(ctx, func(d *model.State) error {
			if d.Ack(id, device, result, message, now) == model.AckRepeat {
				return errQuiet // nur bestätigen
			}
			return nil
		})
		switch {
		case err == nil, errors.Is(err, errQuiet):
		case errors.Is(err, store.ErrLate):
			c.Log.Warn("Quittung der Box nicht rechtzeitig gesichert – landet das Sichern noch, gilt sie; bis dahin steht der Befehl als ausgeliefert", "befehl", id, "err", err)
		default:
			c.Log.Warn("Quittung der Box nicht gesichert – der Befehl bleibt als ausgeliefert stehen", "befehl", id, "err", err)
		}
		web.Text(w, http.StatusOK, "OK\n")
	}
}
