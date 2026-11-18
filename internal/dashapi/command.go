package dashapi

import (
	"errors"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"fwalarm/internal/core"
	"fwalarm/internal/model"
	"fwalarm/internal/web"
)

// Befehle an die Box im Dashboard (PHP: dashboard_api.php, Zweige enqueue
// und cancel, der Teil command von status): TEST läuft über die Box (Poll
// und Quittung) und testet so die halbe Kette mit; ALARM über die Box ist
// ein reines Demo-Werkzeug – er durchläuft die ganze Alarm-Zustellung der
// Box und ginge an ihre aktuelle Liste, im LIVE-Betrieb also an alle (dafür
// gibt es den Alarm direkt vom Server). Deshalb nur im Demo-Modus und nur,
// wenn die Box die Demo-Liste nachweislich übernommen hat und online ist –
// geprüft beim Anlegen (hier) UND beim Ausliefern (internal/box,
// model.DemoAlarmStillSafe).

func init() {
	Register("enqueue", Action{Handle: handleEnqueue})
	Register("cancel", Action{Handle: handleCancel})
	StatusPart("command", commandPart)
}

// Sätze, wörtlich aus dashboard_api.php.
const (
	msgCommandCreated   = "Befehl angelegt."
	msgCommandCancelled = "Befehl abgebrochen."
	msgNothingToCancel  = "Kein wartender Befehl zum Abbrechen."
	msgModeStale        = "Die Oberfläche ist veraltet (Modus nicht bestätigt) – es wurde NICHTS gesendet. Seite neu laden."
)

// handleEnqueue (dashboard_api.php 224-286): type (groß geschrieben wie PHPs
// strtoupper, nur ASCII) muss TEST oder ALARM sein – sonst "Ungültiger
// Befehl.", ohne die Ablage anzufassen. Dann EINE Änderung mit der Frist der
// Oberfläche, alles auf dem Entwurf (zwei gleichzeitige Anfragen können nie
// beide anlegen):
//
//  1. Ein zu alter wartender Befehl verfällt (model.State.ExpirePending) –
//     gesichert nur zusammen mit dem neuen Befehl.
//  2. Nur ALARM: der bestätigte Modus (expect_demo, Pflicht – sonst 409
//     mode_changed, checkExpectedMode) und die Sperre des Demo-Alarms
//     (model.State.DemoBlockReason gegen den letzten Status der Box).
//  3. model.State.Enqueue: kein anderer wartet, ein ausgelieferter ist
//     quittiert oder länger als delivered_command_lock_seconds her.
//
// Antwort {"ok":true,"message":"Befehl angelegt.","command":…} bzw.
// {"ok":false,"message":<Grund>}; scheitert das Sichern: web.StorageError.
func handleEnqueue(x *Ctx) {
	typ := asciiUpper(x.Form.Get("type"))
	if typ != model.CommandTest && typ != model.CommandAlarm {
		reply(x, model.MsgInvalidCommand)
		return
	}
	cfg := x.Core.Cfg()
	now, by := x.Core.Clock.Now(), x.Sess.User
	box := x.Core.Store.Runtime().Box
	expect, hasExpect := x.Form.Get("expect_demo"), x.Form.Has("expect_demo")
	apply(x, func(d *model.State) (web.Object, error) {
		d.ExpirePending(now, cfg.PendingCommandTTLSeconds)
		if typ == model.CommandAlarm {
			if err := checkExpectedMode(d, expect, hasExpect); err != nil {
				return nil, err
			}
			if why := d.DemoBlockReason(box, now, cfg.OfflineAfterSeconds); why != "" {
				return nil, refuse(why)
			}
		}
		cmd, why := d.Enqueue(typ, by, now, cfg.DeliveredCommandLockSeconds)
		if why != "" {
			return nil, refuse(why)
		}
		return web.Object{{Key: "ok", Value: true}, {Key: "message", Value: msgCommandCreated}, {Key: "command", Value: commandView(cmd)}}, nil
	})
}

// handleCancel (dashboard_api.php 289-309): einen noch nicht abgeholten
// Befehl zurücknehmen (etwa wenn die Box offline ist) – nur einen wartenden,
// ohne Grund (model.State.Cancel). Antwort {"ok":true,"message":"Befehl
// abgebrochen.","command":…} bzw. "Kein wartender Befehl zum Abbrechen.".
func handleCancel(x *Ctx) {
	now, by := x.Core.Clock.Now(), x.Sess.User
	apply(x, func(d *model.State) (web.Object, error) {
		cmd, ok := d.Cancel(by, "", now)
		if !ok {
			return nil, refuse(msgNothingToCancel)
		}
		return web.Object{{Key: "ok", Value: true}, {Key: "message", Value: msgCommandCancelled}, {Key: "command", Value: commandView(cmd)}}, nil
	})
}

// checkExpectedMode (PHP require_expected_mode): Die Person hat den Modus
// bestätigt, der beim Scharfschalten des Knopfs angezeigt wurde (expect_demo:
// "0" = LIVE, sonst key_id des Demo-Empfängers). Fehlt das Feld (eine alte
// Oberfläche) oder hat sich der Modus inzwischen geändert – etwa aus einer
// zweiten Sitzung –, geschieht NICHTS: 409 mode_changed mit dem Modus von st
// (demo). Ein als "Demo" bestätigter Alarm darf nie an alle gehen und
// umgekehrt. st ist der Stand, mit dem danach auch gehandelt wird (der
// Entwurf bzw. die Momentaufnahme eines Versands). nil = bestätigt.
func checkExpectedMode(st *model.State, sent string, has bool) error {
	info := demoInfo(st)
	actual := "0"
	if st.DemoEnabled() {
		actual = strconv.Itoa(st.Demo.KeyID)
	}
	var message string
	switch {
	case !has:
		message = msgModeStale
	case sent != actual:
		now := "jetzt LIVE – Alarm ginge an ALLE"
		if actual != "0" {
			now = "jetzt DEMO – nur an „" + st.Demo.Label + "“"
		}
		message = "Der Modus wurde inzwischen geändert (" + now + "). Es wurde NICHTS gesendet – bitte neu bestätigen."
	default:
		return nil
	}
	return &answered{status: http.StatusConflict, obj: web.Object{
		{Key: "ok", Value: false}, {Key: "code", Value: "mode_changed"}, {Key: "message", Value: message}, {Key: "demo", Value: info},
	}}
}

// commandPart: der Teil command der Antwort von status (PHP: command_state
// 'current', für jede Rolle) – der laufende Befehl mit den Feldern, die PHP
// zeigte (model.Command.Members), oder null. Wartet er länger als
// pending_command_ttl_seconds, erscheint er als verfallen (expired, jetzt) –
// auch ohne pollende Box soll er so dastehen und die Knöpfe freigeben; das
// Verfallen wird nebenher gesichert (expireBeside), die Antwort wartet nicht
// darauf.
func commandPart(x *Ctx) any {
	cur := x.Core.Store.State().Commands.Current
	if cur == nil {
		return nil
	}
	shown := *cur
	now := x.Core.Clock.Now()
	if shown.PendingPast(now, x.Core.Cfg().PendingCommandTTLSeconds) {
		shown.State, shown.ExpiredAt = model.CommandExpired, now.Unix()
		expireBeside(x.Core)
	}
	return commandView(shown)
}

// commandView: ein Befehl als JSON-Objekt in PHPs Reihenfolge.
func commandView(c model.Command) web.Object {
	members := c.Members()
	obj := make(web.Object, len(members))
	for i, m := range members {
		obj[i] = web.Member{Key: m.Key, Value: m.Value}
	}
	return obj
}

// errNotExpired: Im Entwurf verfällt (inzwischen) nichts – nichts zu sichern.
var errNotExpired = errors.New("nichts verfallen")

// expireRetryAfter: so lange nach einem gescheiterten Versuch beginnt kein
// neuer (auf der Uhr des Core).
const expireRetryAfter = 5 * time.Second

// besideState: je Core, ob gerade ein Verfallen nebenher gesichert wird, und
// wann frühestens der nächste Versuch beginnen darf.
type besideState struct {
	busy      atomic.Bool
	mu        sync.Mutex
	notBefore time.Time
}

// waiting: Läuft noch die Pause nach einem gescheiterten Versuch? Liegt ihr
// Ende weiter als die Pause voraus, ist die Uhr zurückgesprungen – dann gilt
// sie als um (sonst wartete das Verfallen, bis die Uhr den alten Stand
// wieder erreicht).
func (b *besideState) waiting(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return now.Before(b.notBefore) && b.notBefore.Sub(now) <= expireRetryAfter
}

func (b *besideState) failedAt(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.notBefore = now.Add(expireRetryAfter)
}

// expiring: *besideState je Core (im Programm einer; die Tests bauen viele).
var expiring sync.Map

// expireBeside sichert das Verfallen eines zu alten Befehls nebenher (Core.Bg)
// – EINE Änderung mit der Frist der Oberfläche (5 s), wie jede feste Änderung
// neben einer Anfrage. Höchstens eine zur Zeit, wie viele Tabs auch fragen:
// Läuft schon eine, geschieht nichts. Scheitert sie, beginnt frühestens 5 s
// später die nächste (beim nächsten Abruf von status, solange der Befehl noch
// wartet), und das Journal sagt es höchstens einmal je Minute.
func expireBeside(c *core.Core) {
	v, _ := expiring.LoadOrStore(c, &besideState{})
	st := v.(*besideState)
	if st.waiting(c.Clock.Now()) || !st.busy.CompareAndSwap(false, true) {
		return
	}
	c.Bg.Go("verfallenen Befehl sichern", func() {
		defer st.busy.Store(false) // erst nach failedAt: kein Versuch dazwischen
		ctx, cancel := web.Budget(web.BudgetUI)
		defer cancel()
		now, ttl := c.Clock.Now(), c.Cfg().PendingCommandTTLSeconds
		err := c.Store.Update(ctx, func(d *model.State) error {
			if !d.ExpirePending(now, ttl) {
				return errNotExpired
			}
			return nil
		})
		if err != nil && !errors.Is(err, errNotExpired) {
			st.failedAt(c.Clock.Now())
			c.WarnRarely("expire", "verfallener Befehl nicht gesichert – ein späterer Abruf versucht es erneut", "err", err)
		}
	})
}

// asciiUpper: wie PHPs strtoupper (seit PHP 8.2 ohne Locale): nur a-z werden
// A-Z, alle anderen Bytes bleiben – strings.ToUpper machte etwa aus "ſ" ein
// "S".
func asciiUpper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'a' <= c && c <= 'z' {
			b[i] = c - 'a' + 'A'
		}
	}
	return string(b)
}
