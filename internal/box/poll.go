package box

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"fwalarm/internal/config"
	"fwalarm/internal/core"
	"fwalarm/internal/model"
	"fwalarm/internal/mutes"
	"fwalarm/internal/store"
	"fwalarm/internal/web"
)

// maxBoardID: so viele Bytes der device_id zählen (PHP: substr(…, 0, 80)).
const maxBoardID = 80

// errQuiet: Der Poll ändert nichts an den festen Daten – nichts wird
// gesichert.
var errQuiet = errors.New("nichts zu sichern")

// PollHandler ist api/poll.php: Die Box fragt alle paar Sekunden nach einem
// Befehl (TEST, oder ALARM als Demo-Werkzeug) und nach der Version ihrer
// Empfängerliste. EIN Kontext mit der Frist der Box (web.BudgetBox, 1,5 s)
// für alles, in dieser Reihenfolge:
//
//  1. Maschinen-Token aus dem Rumpf (web.MachineAuth), sonst 403 FORBIDDEN –
//     und nur dann 403 (zu großer Rumpf 413, abgebrochener 400).
//  2. Überfällige Stummschaltungen zurück auf laut (mutes.Expire): Die Box
//     holt die Liste nur bei neuer Version – ohne das erreichte das
//     Sicherheitsnetz den Relaisweg nur, wenn zufällig jemand das Dashboard
//     offen hat. Ein Fehler ändert nichts an der Antwort.
//  3. Nur für die eingestellte Box (box_device_id; leer = jede): EINE
//     Änderung (Store.Update) mit dem Rest der Frist – model.PollCommand auf
//     dem Entwurf: die Nummer der Box (last_command_id), ein zu alter Befehl
//     verfällt, ein unsicherer Demo-Alarm wird abgebrochen, der wartende als
//     ausgeliefert markiert. Ändert sich dabei nichts (der Normalfall), wird
//     nichts gesichert. Ein anderes Board mit dem Token bekommt nie einen
//     Befehl und ändert nichts (es schnappte der Box sonst einen weg).
//  4. Die Antwort: ok, nonce, id, type, keys_version – zeichengenau wie PHP.
//     Den Befehl trägt sie NUR, wenn die Änderung, die ihn ausgeliefert
//     nennt, gesichert ist (Update lieferte nil). Jeder andere Ausgang ist
//     200 type=NONE mit keys_version: verworfen (ErrDropped – nichts
//     geschah, der Befehl wartet weiter), gescheitert (ErrStorage – ebenso),
//     zu spät gesichert (ErrLate – dann gilt er als ausgeliefert und wird
//     nie gesendet: höchstens einmal, auch über einen Neustart), Notbetrieb.
//     Nie ein HTTP-Fehler: Der schickte die Box in ihren Fehler-Backoff, in
//     dem sie nicht einmal mehr ihren Status pusht.
//
// keys_version ist box.KeysVersion beim Antworten (0 im Notbetrieb – die Box
// behält dann ihre gespeicherte Liste). Die Antwort wartet nie auf Bark und
// auf die Platte höchstens die Frist der Box.
func PollHandler(c *core.Core) web.Handler {
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
		device := web.ClipBytes(f.Get("device_id"), maxBoardID)
		last := commandID(f.Get("last_command_id"))
		ctx, cancel := web.Budget(web.BudgetBox)
		defer cancel()
		_ = mutes.Expire(ctx, c) // ein Fehler ändert nichts an der Antwort (Expire schreibt ihn ins Journal)
		var cmd *model.Command
		if expectedBox(c.Cfg().BoxDeviceID, device) {
			cmd = deliver(ctx, c, device, last)
		}
		web.Text(w, http.StatusOK, pollAnswer(nonce, cmd, KeysVersion(c)))
	}
}

// deliver ist Schritt 3 des Polls: der ausgelieferte Befehl – nur, wenn die
// Änderung rechtzeitig gesichert ist; sonst nil. Der Status der Box (für die
// Sicherung des Demo-Alarms) kommt aus dem Schnappschuss der flüchtigen
// Daten, vor der Änderung – die Änderung selbst rechnet nur.
func deliver(ctx context.Context, c *core.Core, device string, last int) *model.Command {
	cfg := c.Cfg()
	box := c.Store.Runtime().Box
	now := c.Clock.Now()
	var out *model.Command
	err := c.Store.Update(ctx, func(d *model.State) error {
		cmd, changed := d.PollCommand(last, device, box, now, cfg.PendingCommandTTLSeconds)
		if !changed {
			return errQuiet
		}
		out = cmd
		return nil
	})
	switch {
	case err == nil:
		return out
	case errors.Is(err, errQuiet):
		return nil
	case out != nil:
		// Im Entwurf ausgeliefert, aber nicht rechtzeitig gesichert (ErrLate)
		// oder gar nicht (ErrStorage): Die Box bekommt ihn jetzt nicht. Landet
		// das Sichern noch, gilt er als ausgeliefert, und kein Poll trägt ihn
		// mehr; scheitert es, wartet er weiter. (Verworfen – ErrDropped – lief
		// die Änderung gar nicht: out bleibt nil.)
		why := "nicht gesichert – er wartet weiter"
		if errors.Is(err, store.ErrLate) {
			why = "nicht rechtzeitig gesichert – landet das Sichern noch, gilt er als ausgeliefert und geht nie hinaus"
		}
		c.Log.Warn("Befehl nicht an die Box ausgeliefert: "+why, "befehl", out.ID, "err", err)
	}
	return nil
}

// pollAnswer: die Zeilen des Polls (PHP api/poll.php 24, 112-113).
func pollAnswer(nonce string, cmd *model.Command, keysVersion int) string {
	id, typ := 0, "NONE"
	if cmd != nil {
		id, typ = cmd.ID, cmd.Type
	}
	return "ok=1\nnonce=" + nonce + "\nid=" + strconv.Itoa(id) + "\ntype=" + typ + "\nkeys_version=" + strconv.Itoa(keysVersion) + "\n"
}

// commandID: last_command_id der Box wie PHP max(0, (int)…) – führende
// Ziffern zählen ("50abc" = 50), alles andere ist 0. Eine Zahl, die nicht in
// int passt, zählt ebenfalls nicht (die Regel für zu große Nummern steht in
// model.NoteBoxCommandID).
func commandID(raw string) int {
	n := model.IntOf(raw)
	if n <= 0 || n > int64(^uint(0)>>1) {
		return 0
	}
	return int(n)
}

// expectedBox: Bekommt dieses Board Befehle? Ist box_device_id gesetzt, nur
// die Box mit genau diesem Kennzeichen (beide ohne Leerraum außen; PHP:
// is_expected_box) – ein Bastel-Board mit demselben Token darf ihr keinen
// wegschnappen. Leer: jedes.
func expectedBox(boxDeviceID, device string) bool {
	expected := config.TrimPHP(boxDeviceID)
	return expected == "" || expected == config.TrimPHP(device)
}
