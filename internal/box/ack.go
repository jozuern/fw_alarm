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
//     dem Entwurf. Gilt die Quittung (AckFirst), wird gesichert (Eintrag
//     acked). Dieselbe Quittung noch einmal ändert nichts. Eine verspätete
//     Quittung (AckLate, AckUnknown) ändert den Zustand nicht: nichts Festes
//     wird gesichert, ihr Eintrag late_ack geht über die flüchtigen Daten in
//     den Verlauf (Store.Log) – auch wenn state.json nicht geschrieben werden
//     kann. Kommt die Änderung gar nicht an die Reihe (verworfen,
//     Notbetrieb), entscheidet der Schnappschuss, ob sie verspätet war.
//  4. Antwort IMMER 200 "OK\n". Lässt sich eine geltende Quittung nicht
//     sichern, bleibt der Befehl "ausgeliefert" (das Dashboard zeigt das
//     ehrlich als "Bestätigung fehlt"), und das Journal sagt es – höchstens
//     einmal je Minute (Core.WarnRarely). Mit einem Fehler wiederholte die
//     Box die Quittung bis zu ihrer Grenze, pollte so lange nicht (keine
//     neue Liste, kein Demo→LIVE) und pushte im Fehler-Backoff keinen Status
//     mehr.
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
		kind, late, ran := model.AckUnknown, model.Entry(nil), false
		err := c.Store.Update(ctx, func(d *model.State) error {
			ran = true
			kind, late = d.Ack(id, device, result, message, now)
			if kind != model.AckFirst {
				return errQuiet // wiederholt oder verspätet: nichts Festes zu sichern
			}
			return nil
		})
		if !ran {
			// Verworfen oder Notbetrieb: War sie verspätet, braucht sie nichts
			// Festes – das sagt auch der Schnappschuss (im Notbetrieb leer:
			// jede Nummer ist unbekannt).
			kind, late = c.Store.State().Clone().Ack(id, device, result, message, now)
		}
		if late != nil {
			c.Store.Log(late)
		}
		if kind == model.AckFirst && err != nil {
			why := "nicht gesichert – der Befehl bleibt als ausgeliefert stehen"
			switch {
			case errors.Is(err, store.ErrLate):
				why = "nicht rechtzeitig gesichert – landet das Sichern noch, gilt sie; bis dahin steht der Befehl als ausgeliefert"
			case errors.Is(err, store.ErrDropped):
				why = "nicht gesichert (nicht an die Reihe gekommen) – der Befehl bleibt als ausgeliefert stehen"
			}
			c.WarnRarely("ack", "Quittung der Box "+why, "befehl", id, "err", err)
		}
		web.Text(w, http.StatusOK, "OK\n")
	}
}
