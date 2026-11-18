package box

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"time"

	"fwalarm/internal/core"
	"fwalarm/internal/model"
	"fwalarm/internal/notify"
	"fwalarm/internal/textde"
	"fwalarm/internal/web"
)

const (
	// maxPushFields: so viele Felder eines Pushs werden gespeichert (PHP:
	// clipped_post_fields; die Box sendet um die 30).
	maxPushFields = 40
	// maxValueBytes: so viele Bytes je Wert.
	maxValueBytes = 300
	// restartNoticeLimit: so lange darf die Meldung "neu gestartet" dauern –
	// der Bark-Client braucht für zwei Versuche schlimmstenfalls gut 20 s.
	restartNoticeLimit = 30 * time.Second
)

// errNothing: Die Version des Servers liegt (inzwischen) nicht mehr unter
// der der Box – nichts zu sichern.
var errNothing = errors.New("Listen-Version schon darüber")

// StatusHandler ist api/status.php: der Status-Push der Box (etwa jede
// Minute). In dieser Reihenfolge:
//
//  1. Maschinen-Token aus dem Rumpf (web.MachineAuth), sonst 403 FORBIDDEN –
//     und nur dann 403. Ein zu großer Rumpf ist 413, ein abgebrochener 400
//     (web.ParseForm).
//  2. Die Felder kürzen (clippedFields), Ankunft auf der Uhr des Servers,
//     die Kopfzeile X-FW-Bridge (ihr Vorhandensein, nicht ihr Wert – PHP:
//     isset), die Adresse des Absenders.
//  3. EINE Änderung der flüchtigen Daten (Store.UpdateRuntime: sofort im
//     Speicher, gesichert nebenher): model.EvaluatePush auf dem Entwurf –
//     so melden zwei gleichzeitige Pushs denselben Alarm nie zweimal –, die
//     Änderung anwenden, relay_alarm und box_restart in dieselbe outbox.
//     Kein Zugriff auf die Platte, kein Warten auf die festen Daten.
//  4. Antwort 200 "OK\n" – immer, auch für einen veralteten Push und den
//     eines fremden Boards (ein Fehler schickte die Box in ihren
//     Fehler-Backoff, in dem sie gar nicht mehr pusht; Spec §7).
//  5. Nach der Antwort (Core.Bg): die Meldung box_restart, wenn eingeschaltet
//     (tellRestart); und meldet die eingestellte Box eine höhere Listen-Version
//     als der Server, deren Übernahme (liftVersion).
func StatusHandler(c *core.Core) web.Handler {
	var lifting atomic.Bool // eine Übernahme der Listen-Version zur Zeit
	return func(w http.ResponseWriter, r *http.Request) {
		f, ok := web.ParseForm(w, r)
		if !ok {
			return
		}
		if !web.MachineAuth(c, f) {
			forbidden(w)
			return
		}
		now := c.Clock.Now()
		push := clippedFields(f)
		v := acceptPush(c, push, len(r.Header.Values("X-FW-Bridge")) > 0, web.ClientIP(c, r), now)
		web.Text(w, http.StatusOK, "OK\n")
		if v.Restart != nil {
			tellRestart(c, *v.Restart, now)
		}
		if !v.Stale && !v.Foreign {
			liftVersion(c, model.BoxKeysVersion(&model.BoxStatus{Fields: push}), &lifting)
		}
	}
}

// acceptPush wendet den Push auf die flüchtigen Daten an – ausgewertet IN
// der Änderung, auf dem jeweils neuesten Stand (der Befehlsstand für die
// Quelle eines Alarms kommt aus dem Schnappschuss der festen Daten). Ein
// veralteter Push ändert nichts (gesichert wird trotzdem einmal mehr – das
// kommt nur vor, wenn der Server hing).
func acceptPush(c *core.Core, push model.Fields, viaBridge bool, remote string, now time.Time) model.PushVerdict {
	cmds := c.Store.State().Commands
	boxID := c.Cfg().BoxDeviceID
	var v model.PushVerdict
	c.Store.UpdateRuntime(func(d *model.Runtime) {
		var next func(*model.Runtime)
		next, v = model.EvaluatePush(d, cmds, push, viaBridge, remote, now, boxID)
		next(d)
		if v.RelayAlarm != nil {
			d.Log(*v.RelayAlarm)
		}
		if v.Restart != nil && v.Restart.Entry != nil {
			d.Log(*v.Restart.Entry)
		}
	})
	return v
}

// clippedFields: was vom Push gespeichert wird (PHP: clipped_post_fields mit
// 'token' übersprungen) – die Felder in ihrer Reihenfolge, höchstens 40.
// Namen behalten nur a-z A-Z 0-9 _ : - (Byte für Byte); wird ein Name dadurch
// leer, fällt das Feld weg, gleicht er einem früheren, ersetzt sein Wert
// dessen Wert an dessen Stelle. Jeder Wert höchstens 300 Bytes, nie mitten in
// einem Zeichen (web.ClipBytes; PHPs substr schnitte dort). Das Token nie –
// anders als PHP auch nicht, wenn ein Name erst durch das Säubern "token"
// heißt.
func clippedFields(f *web.Form) model.Fields {
	var out model.Fields
	at := map[string]int{}
	f.Each(func(name, value string) bool {
		if len(out) >= maxPushFields {
			return false
		}
		if name == "token" {
			return true
		}
		safe := safeFieldName(name)
		if safe == "" || safe == "token" {
			return true
		}
		value = web.ClipBytes(value, maxValueBytes)
		if i, ok := at[safe]; ok {
			out[i].Value = value
			return true
		}
		at[safe] = len(out)
		out = append(out, model.Field{Name: safe, Value: value})
		return true
	})
	return out
}

// safeFieldName: nur a-z A-Z 0-9 _ : - (PHP: preg_replace mit dem Muster
// /[^a-zA-Z0-9_:-]/ ohne /u – Byte für Byte).
func safeFieldName(name string) string {
	out := make([]byte, 0, len(name))
	for i := range len(name) {
		c := name[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' || c == ':' || c == '-' {
			out = append(out, c)
		}
	}
	return string(out)
}

// tellRestart meldet einen Neustart der Box an den Admin (Art box_restart,
// ab Werk aus) – nebenher, nach der Antwort: bei "nur Absturz, Watchdog,
// Spannungseinbruch" (scope abnormal, Standard) nur für diese Gründe, bei
// "jedem Neustart" (scope all) für jeden. Die Zeit im Text ist die des Pushs.
func tellRestart(c *core.Core, r model.RestartInfo, now time.Time) {
	c.Bg.Go("Meldung: Box neu gestartet", func() {
		kind := notify.Settings(c).Types["box_restart"]
		if !kind.Enabled || (!r.Abnormal && kind.Values["scope"] != "all") {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), restartNoticeLimit)
		defer cancel()
		// Das Ergebnis hat keine Folge: Wie in PHP wird diese Meldung nicht
		// wiederholt (das Journal hat den Fehlschlag von bark).
		notify.Send(ctx, c, "box_restart", "Alarm-Box neu gestartet",
			textde.Stamp(now, c.Cfg().Location())+"Die Box hat nach "+restartWhy(r.Reason)+" neu gestartet und meldet sich wieder.", false)
	})
}

// restartWhy: der Grund eines Neustarts für die Meldung (PHP: $reasons in
// api/status.php; Bark-Texte ASCII).
func restartWhy(reason string) string {
	switch reason {
	case "panic":
		return "einem ABSTURZ (Panic)"
	case "task_wdt", "int_wdt", "wdt":
		return "einem WATCHDOG-Reset"
	case "brownout":
		return "einem SPANNUNGSEINBRUCH (Brownout) - Netzteil/Kabel pruefen"
	case "poweron":
		return "dem Einschalten"
	case "software":
		return "einem Software-Neustart"
	case "external":
		return "einem Reset per Taster"
	}
	return "unbekanntem Grund (" + reason + ")"
}

// liftVersion: Meldet die Box eine höhere Listen-Version als der Server
// (etwa nach dem Zurückspielen einer älteren Sicherung), hebt der Server
// seine sofort darüber – die Box holt die Liste dann neu (Spec §6.3, §7;
// PHP zählte sie erst bei der nächsten Änderung mit). boxVersion ist schon
// geprüft (model.BoxKeysVersion: unplausibel = 0, also nie darüber). Liegt
// der Server schon darüber – der übliche Fall, die Box meldet dieselbe
// Version mit jedem Push –, geschieht nichts, auch kein Sichern. Sonst
// nebenher EINE Änderung mit der Frist der Oberfläche (5 s): keys_version =
// boxVersion + 1 (State.Bump). Läuft schon eine, übernimmt der nächste Push
// das; ebenso nach einem Fehlschlag (eine Zeile im Journal).
func liftVersion(c *core.Core, boxVersion int, lifting *atomic.Bool) {
	if c.Store.Emergency() || boxVersion <= c.Store.State().KeysVersion {
		return
	}
	if !lifting.CompareAndSwap(false, true) {
		return
	}
	c.Bg.Go("Listen-Version der Box übernehmen", func() {
		defer lifting.Store(false)
		ctx, cancel := web.Budget(web.BudgetUI)
		defer cancel()
		err := c.Store.Update(ctx, func(d *model.State) error {
			if boxVersion <= d.KeysVersion {
				return errNothing
			}
			d.Bump(boxVersion)
			return nil
		})
		if err != nil && !errors.Is(err, errNothing) {
			c.Log.Warn("höhere Listen-Version der Box nicht übernommen – der nächste Status-Push versucht es erneut",
				"box", boxVersion, "err", err)
		}
	})
}
