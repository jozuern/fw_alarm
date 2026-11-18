package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"
)

// Bausteine für den Stand vor einem Schritt: der laufende Befehl (nil =
// keiner), die nächste Nummer und die gemerkten Typen.
func withCommand(c *Command, next int, recent ...RecentType) func(s *State) {
	return func(s *State) {
		if c != nil {
			cc := *c
			s.Commands.Current = &cc
		}
		s.Commands.NextID = next
		s.Commands.RecentTypes = append([]RecentType{}, recent...) // leer wie nach Clone, nie nil
	}
}

// demoOn: Demo-Modus mit Anna als Demo-Empfänger, gewechselt vor 100 s.
func demoOn(s *State) {
	s.Recipients = []Recipient{{ID: 3, Key: keyAnna, Label: "Anna", MuteToken: tokenAnna}}
	s.NextID = 4
	s.Demo = Demo{Enabled: true, KeyID: 3, Key: keyAnna, Label: "Anna", ChangedAt: t0.Unix() - 100, By: "admin"}
}

// Befehle, wie sie auf ihrem Weg aussehen (t0 = jetzt).
var (
	pendingTest = Command{ID: 4, Type: CommandTest, State: CommandPending, CreatedAt: t0.Unix() - 20, CreatedBy: "admin"}
	pendingDemo = Command{ID: 4, Type: CommandAlarm, State: CommandPending, CreatedAt: t0.Unix() - 20, CreatedBy: "admin",
		DemoChangedAt: t0.Unix() - 100}
)

func delivered(c Command, ago int64) Command {
	c.State, c.DeliveredAt, c.DeliveredTo = CommandDelivered, t0.Unix()-ago, "fw-box-1"
	return c
}

func acked(c Command, result string) Command {
	c = delivered(c, 10)
	c.State, c.AckedAt, c.AckResult, c.AckMessage, c.AckedBy = CommandAcked, t0.Unix()-5, result, "vorher", "fw-box-1"
	return c
}

func ptr(c Command) *Command { return &c }

// box1/box2: der letzte Status der Box meldet einen bzw. zwei Empfänger.
var (
	box1 = &BoxStatus{SeenAt: t0.Unix() - 5, Fields: Fields{{"device_id", "fw-box-1"}, {"keys_count", "1"}}}
	box2 = &BoxStatus{SeenAt: t0.Unix() - 5, Fields: Fields{{"device_id", "fw-box-1"}, {"keys_count", "2"}}}
)

// wantEntry: der Verlaufseintrag, den ein Schritt schreibt. keys: die Felder
// des Befehls im Eintrag (genau diese, Werte wie am Befehl danach – so wie
// PHP sein Array schreibt); top: die Felder eines late_ack.
type wantEntry struct {
	event string
	keys  []string
	top   map[string]any
}

// lifeRow ist ein Übergang im Lebenslauf eines Befehls.
type lifeRow struct {
	name   string
	php    string // die PHP-Zeilen, die er nachbaut
	start  func(s *State)
	op     func(s *State) any // der Schritt; liefert sein Ergebnis
	result any                // erwartetes Ergebnis
	cmd    *Command           // der laufende Befehl danach (nil = keiner)
	next   int                // commands.next_id danach
	recent []RecentType       // recent_types danach
	entry  *wantEntry         // nil = kein Eintrag; same = nichts geändert
	same   bool               // der ganze Zustand unverändert (auch kein Eintrag)
}

// Was ein Schritt zurückgibt, als vergleichbarer Wert.
type enqueued struct {
	Cmd Command
	Why string
}

type cancelled struct {
	Cmd Command
	OK  bool
}

type polled struct {
	Cmd     *Command
	Changed bool
}

func enqueue(typ string, lock int) func(s *State) any {
	return func(s *State) any {
		c, why := s.Enqueue(typ, "admin", t0, lock)
		return enqueued{c, why}
	}
}

func ack(id int, result string) func(s *State) any {
	return func(s *State) any { return s.Ack(id, "fw-box-1", result, "gesendet", t0) }
}

func poll(last int, box *BoxStatus) func(s *State) any {
	return func(s *State) any {
		c, changed := s.PollCommand(last, "fw-box-1", box, t0, 300)
		return polled{c, changed}
	}
}

// Die Felder, die PHP für einen Befehl schreibt – je Weg.
var (
	keysCreated  = []string{"id", "type", "state", "created_at", "created_by"}
	keysDemo     = append(slices.Clone(keysCreated), "demo_changed_at")
	keysDeliv    = append(slices.Clone(keysCreated), "delivered_at", "delivered_to")
	keysDemoDel  = append(slices.Clone(keysDemo), "delivered_at", "delivered_to")
	keysAcked    = append(slices.Clone(keysDeliv), "acked_at", "ack_result", "ack_message", "acked_by")
	keysExpired  = append(slices.Clone(keysCreated), "expired_at")
	keysCancel   = append(slices.Clone(keysCreated), "cancelled_at", "cancelled_by")
	keysDemoCanc = append(slices.Clone(keysDemo), "cancelled_at", "cancelled_by", "cancel_reason")
)

func lifeRows() []lifeRow {
	at := t0.Unix()
	created := func(id int, typ string) Command {
		return Command{ID: id, Type: typ, State: CommandPending, CreatedAt: at, CreatedBy: "admin"}
	}
	demoCreated := created(1, CommandAlarm)
	demoCreated.DemoChangedAt = at - 100
	deliveredNow := delivered(pendingTest, 0)
	ackedNow := delivered(pendingTest, 10)
	ackedNow.State, ackedNow.AckedAt, ackedNow.AckResult, ackedNow.AckMessage, ackedNow.AckedBy = CommandAcked, at, "executed", "gesendet", "fw-box-1"
	reacked := acked(pendingTest, "status_failed")
	reacked.AckedAt, reacked.AckResult, reacked.AckMessage = at, "executed", "gesendet"
	expiredNow := pendingTest
	expiredNow.CreatedAt = at - 301
	expired := expiredNow
	expired.State, expired.ExpiredAt = CommandExpired, at
	byAdmin := pendingTest
	byAdmin.State, byAdmin.CancelledAt, byAdmin.CancelledBy = CommandCancelled, at, "admin"
	byGuard := pendingDemo
	byGuard.State, byGuard.CancelledAt, byGuard.CancelledBy, byGuard.CancelReason = CommandCancelled, at, DemoGuardBy, DemoGuardReason
	demoDelivered := delivered(pendingDemo, 0)
	lateAck := func(id int, typ string) *wantEntry {
		return &wantEntry{event: EvLateAck, top: map[string]any{"id": id, "type": typ, "result": "executed", "message": "gesendet", "device_id": "fw-box-1"}}
	}
	r4 := []RecentType{{ID: 4, Type: CommandTest}}
	return []lifeRow{
		// --- anlegen (dashboard_api.php 224-286) ---------------------------
		{name: "TEST anlegen", php: "dashboard_api.php 256-282",
			start: withCommand(nil, 1), op: enqueue(CommandTest, 120), result: enqueued{created(1, CommandTest), ""},
			cmd: ptr(created(1, CommandTest)), next: 2, recent: []RecentType{{1, CommandTest}},
			entry: &wantEntry{event: EvCreated, keys: keysCreated}},
		{name: "ALARM anlegen: hält den Demo-Stand fest", php: "dashboard_api.php 264-274",
			start: func(s *State) { demoOn(s); withCommand(nil, 1)(s) }, op: enqueue(CommandAlarm, 120),
			result: enqueued{demoCreated, ""}, cmd: &demoCreated, next: 2, recent: []RecentType{{1, CommandAlarm}},
			entry: &wantEntry{event: EvCreated, keys: keysDemo}},
		{name: "anlegen: unbekannter Typ", php: "dashboard_api.php 226-228",
			start: withCommand(nil, 1), op: enqueue("RESET", 120), result: enqueued{Command{}, MsgInvalidCommand}, same: true},
		{name: "anlegen: klein geschriebener Typ gilt hier nicht (das Großschreiben ist Sache des Aufrufers)", php: "dashboard_api.php 225",
			start: withCommand(nil, 1), op: enqueue("test", 120), result: enqueued{Command{}, MsgInvalidCommand}, same: true},
		{name: "anlegen: es wartet schon einer", php: "dashboard_api.php 244-246",
			start: withCommand(&pendingTest, 5, r4...), op: enqueue(CommandTest, 120), result: enqueued{Command{}, msgCommandWaiting}, same: true},
		{name: "anlegen: vor 119 s ausgeliefert, Quittung fehlt", php: "dashboard_api.php 247-253",
			start: withCommand(ptr(delivered(pendingTest, 119)), 5, r4...), op: enqueue(CommandTest, 120),
			result: enqueued{Command{}, msgAwaitingAck}, same: true},
		{name: "anlegen: vor 120 s ausgeliefert – wieder frei", php: "dashboard_api.php 250 (<)",
			start: withCommand(ptr(delivered(pendingTest, 120)), 5, r4...), op: enqueue(CommandTest, 120),
			result: enqueued{created(5, CommandTest), ""}, cmd: ptr(created(5, CommandTest)), next: 6,
			recent: []RecentType{{4, CommandTest}, {5, CommandTest}}, entry: &wantEntry{event: EvCreated, keys: keysCreated}},
		{name: "anlegen: die Sperre nach der Auslieferung ist einstellbar", php: "dashboard_api.php 249",
			start: withCommand(ptr(delivered(pendingTest, 30)), 5, r4...), op: enqueue(CommandTest, 30),
			result: enqueued{created(5, CommandTest), ""}, cmd: ptr(created(5, CommandTest)), next: 6,
			recent: []RecentType{{4, CommandTest}, {5, CommandTest}}, entry: &wantEntry{event: EvCreated, keys: keysCreated}},
		{name: "anlegen: ausgeliefert ohne Zeit sperrt nicht", php: "dashboard_api.php 250 ($deliveredAt > 0)",
			start: withCommand(ptr(func() Command { c := delivered(pendingTest, 0); c.DeliveredAt = 0; return c }()), 5, r4...),
			op:    enqueue(CommandTest, 120), result: enqueued{created(5, CommandTest), ""}, cmd: ptr(created(5, CommandTest)), next: 6,
			recent: []RecentType{{4, CommandTest}, {5, CommandTest}}, entry: &wantEntry{event: EvCreated, keys: keysCreated}},
		{name: "anlegen nach der Quittung: sofort frei", php: "dashboard_api.php 242-254",
			start: withCommand(ptr(acked(pendingTest, "executed")), 5, r4...), op: enqueue(CommandTest, 120),
			result: enqueued{created(5, CommandTest), ""}, cmd: ptr(created(5, CommandTest)), next: 6,
			recent: []RecentType{{4, CommandTest}, {5, CommandTest}}, entry: &wantEntry{event: EvCreated, keys: keysCreated}},
		{name: "anlegen: Nummer mindestens 1", php: "dashboard_api.php 256 (max(1, …))",
			start: withCommand(nil, 0), op: enqueue(CommandTest, 120), result: enqueued{created(1, CommandTest), ""},
			cmd: ptr(created(1, CommandTest)), next: 2, recent: []RecentType{{1, CommandTest}}, entry: &wantEntry{event: EvCreated, keys: keysCreated}},

		// --- ausliefern (poll.php 100-113) ----------------------------------
		{name: "wartend → ausgeliefert", php: "poll.php 100-113",
			start: withCommand(&pendingTest, 5, r4...), op: func(s *State) any { return s.Deliver("fw-box-1", t0) },
			result: &deliveredNow, cmd: &deliveredNow, next: 5, recent: r4, entry: &wantEntry{event: EvDelivered, keys: keysDeliv}},
		{name: "ausliefern ohne wartenden Befehl: nichts", php: "poll.php 83-86",
			start: withCommand(ptr(delivered(pendingTest, 3)), 5, r4...), op: func(s *State) any { return s.Deliver("fw-box-1", t0) },
			result: (*Command)(nil), same: true},
		{name: "ausliefern ohne Befehl: nichts", php: "poll.php 83-86",
			start: withCommand(nil, 1), op: func(s *State) any { return s.Deliver("fw-box-1", t0) }, result: (*Command)(nil), same: true},

		// --- quittieren (ack.php 18-62) -------------------------------------
		{name: "ausgeliefert → quittiert", php: "ack.php 34-45",
			start: withCommand(ptr(delivered(pendingTest, 10)), 5, r4...), op: ack(4, "executed"), result: AckFirst,
			cmd: &ackedNow, next: 5, recent: r4, entry: &wantEntry{event: EvAcked, keys: keysAcked}},
		{name: "dieselbe Quittung noch einmal: nur bestätigt, keine neue Zeit, kein Eintrag", php: "ack.php 27-33",
			start: withCommand(ptr(acked(pendingTest, "executed")), 5, r4...), op: ack(4, "executed"), result: AckRepeat, same: true},
		{name: "erneute Quittung mit anderem Ergebnis: gilt neu", php: "ack.php 34-45",
			start: withCommand(ptr(acked(pendingTest, "status_failed")), 5, r4...), op: ack(4, "executed"), result: AckFirst,
			cmd: &reacked, next: 5, recent: r4, entry: &wantEntry{event: EvAcked, keys: keysAcked}},
		{name: "Quittung für einen noch wartenden Befehl: verspätet, mit seinem Typ – er bleibt wartend", php: "ack.php 46-59",
			start: withCommand(&pendingTest, 5, r4...), op: ack(4, "executed"), result: AckLate,
			cmd: &pendingTest, next: 5, recent: r4, entry: lateAck(4, CommandTest)},
		{name: "Quittung für einen abgelösten Befehl: der Typ aus recent_types", php: "ack.php 49-51",
			start: withCommand(ptr(created(5, CommandTest)), 6, RecentType{3, CommandAlarm}, RecentType{5, CommandTest}),
			op:    ack(3, "executed"), result: AckLate, cmd: ptr(created(5, CommandTest)), next: 6,
			recent: []RecentType{{3, CommandAlarm}, {5, CommandTest}}, entry: lateAck(3, CommandAlarm)},
		{name: "Quittung für eine unbekannte Nummer: verspätet, ohne Typ", php: "ack.php 46-59",
			start: withCommand(&pendingTest, 5, r4...), op: ack(99, "executed"), result: AckUnknown,
			cmd: &pendingTest, next: 5, recent: r4, entry: lateAck(99, "")},
		{name: "Quittung ohne jeden Befehl: verspätet, ohne Typ", php: "ack.php 46-59",
			start: withCommand(nil, 1), op: ack(1, "executed"), result: AckUnknown, next: 1, entry: lateAck(1, "")},
		{name: "Quittung für einen verfallenen Befehl: verspätet, sein Typ", php: "ack.php 26, 49-50",
			start: withCommand(&expired, 5, r4...), op: ack(4, "executed"), result: AckLate,
			cmd: &expired, next: 5, recent: r4, entry: lateAck(4, CommandTest)},
		{name: "Quittung für einen abgebrochenen Befehl: verspätet", php: "ack.php 26, 49-50",
			start: withCommand(&byAdmin, 5, r4...), op: ack(4, "executed"), result: AckLate,
			cmd: &byAdmin, next: 5, recent: r4, entry: lateAck(4, CommandTest)},

		// --- verfallen (common.php 1097-1116) -------------------------------
		{name: "wartend, älter als die Frist → verfallen", php: "common.php 1103-1114",
			start: withCommand(&expiredNow, 5, r4...), op: func(s *State) any { return s.ExpirePending(t0, 300) }, result: true,
			cmd: &expired, next: 5, recent: r4, entry: &wantEntry{event: EvExpired, keys: keysExpired}},
		{name: "genau 300 s alt: verfällt noch nicht", php: "common.php 1105 (<=)",
			start: withCommand(ptr(func() Command { c := pendingTest; c.CreatedAt = t0.Unix() - 300; return c }()), 5, r4...),
			op:    func(s *State) any { return s.ExpirePending(t0, 300) }, result: false, same: true},
		{name: "ohne Zeit des Anlegens verfällt nichts", php: "common.php 1105 ($createdAt <= 0)",
			start: withCommand(ptr(func() Command { c := pendingTest; c.CreatedAt = 0; return c }()), 5, r4...),
			op:    func(s *State) any { return s.ExpirePending(t0, 300) }, result: false, same: true},
		{name: "ein ausgelieferter verfällt nicht", php: "common.php 1100",
			start: withCommand(ptr(delivered(pendingTest, 3600)), 5, r4...),
			op:    func(s *State) any { return s.ExpirePending(t0, 300) }, result: false, same: true},

		// --- abbrechen (dashboard_api.php 289-309; poll.php 88-98) ---------
		{name: "wartend → vom Admin abgebrochen, ohne Grund (auch im Eintrag keiner)", php: "dashboard_api.php 294-304",
			start:  withCommand(&pendingTest, 5, r4...),
			op:     func(s *State) any { c, ok := s.Cancel("admin", "", t0); return cancelled{c, ok} },
			result: cancelled{byAdmin, true}, cmd: &byAdmin, next: 5, recent: r4, entry: &wantEntry{event: EvCancelled, keys: keysCancel}},
		{name: "abbrechen geht nur wartend", php: "dashboard_api.php 294-296",
			start: withCommand(ptr(delivered(pendingTest, 3)), 5, r4...),
			op:    func(s *State) any { c, ok := s.Cancel("admin", "", t0); return cancelled{c, ok} }, result: cancelled{}, same: true},
		{name: "abbrechen ohne Befehl", php: "dashboard_api.php 294-296",
			start: withCommand(nil, 1),
			op:    func(s *State) any { c, ok := s.Cancel("admin", "", t0); return cancelled{c, ok} }, result: cancelled{}, same: true},

		// --- ein Poll der Box (poll.php 54-113) -------------------------------
		{name: "Poll: TEST wird ausgeliefert", php: "poll.php 100-113",
			start: withCommand(&pendingTest, 5, r4...), op: poll(0, box1), result: polled{&deliveredNow, true},
			cmd: &deliveredNow, next: 5, recent: r4, entry: &wantEntry{event: EvDelivered, keys: keysDeliv}},
		{name: "Poll: ohne Befehl ändert nichts", php: "poll.php 83-86",
			start: withCommand(nil, 1), op: poll(0, box1), result: polled{nil, false}, same: true},
		{name: "Poll: ausgeliefert, noch nicht quittiert – nie ein zweites Mal", php: "poll.php 83-86",
			start: withCommand(ptr(delivered(pendingTest, 3)), 5, r4...), op: poll(4, box1), result: polled{nil, false}, same: true},
		{name: "Poll: zu alt → verfallen, nicht ausgeliefert", php: "poll.php 79-86",
			start: withCommand(&expiredNow, 5, r4...), op: poll(0, box1), result: polled{nil, true},
			cmd: &expired, next: 5, recent: r4, entry: &wantEntry{event: EvExpired, keys: keysExpired}},
		{name: "Poll: Demo-Alarm, Box hat die Demo-Liste – ausgeliefert", php: "poll.php 88, 100-113",
			start: func(s *State) { demoOn(s); withCommand(&pendingDemo, 5, RecentType{4, CommandAlarm})(s) }, op: poll(0, box1),
			result: polled{&demoDelivered, true}, cmd: &demoDelivered, next: 5, recent: []RecentType{{4, CommandAlarm}},
			entry: &wantEntry{event: EvDelivered, keys: keysDemoDel}},
		{name: "Poll: Demo-Alarm, Demo-Stand seit dem Anlegen geändert – von der Sicherung abgebrochen", php: "poll.php 41-44, 88-98",
			start: func(s *State) {
				demoOn(s)
				s.Demo.ChangedAt = t0.Unix() - 50
				withCommand(&pendingDemo, 5, RecentType{4, CommandAlarm})(s)
			}, op: poll(0, box1), result: polled{nil, true}, cmd: &byGuard, next: 5, recent: []RecentType{{4, CommandAlarm}},
			entry: &wantEntry{event: EvCancelled, keys: keysDemoCanc}},
		{name: "Poll: Demo-Alarm, Demo-Modus inzwischen aus – abgebrochen", php: "poll.php 38-40, 88-98",
			start: func(s *State) {
				demoOn(s)
				s.Demo = Demo{ChangedAt: t0.Unix() - 100}
				withCommand(&pendingDemo, 5, RecentType{4, CommandAlarm})(s)
			}, op: poll(0, box1), result: polled{nil, true}, cmd: &byGuard, next: 5, recent: []RecentType{{4, CommandAlarm}},
			entry: &wantEntry{event: EvCancelled, keys: keysDemoCanc}},
		{name: "Poll: Demo-Alarm, die Box meldet zwei Empfänger – abgebrochen", php: "poll.php 45-46, 88-98",
			start: func(s *State) { demoOn(s); withCommand(&pendingDemo, 5, RecentType{4, CommandAlarm})(s) }, op: poll(0, box2),
			result: polled{nil, true}, cmd: &byGuard, next: 5, recent: []RecentType{{4, CommandAlarm}},
			entry: &wantEntry{event: EvCancelled, keys: keysDemoCanc}},
		{name: "Poll: Demo-Alarm, die Box hat sich nie gemeldet – abgebrochen", php: "poll.php 45-46 (keys_count fehlt = 0)",
			start: func(s *State) { demoOn(s); withCommand(&pendingDemo, 5, RecentType{4, CommandAlarm})(s) }, op: poll(0, nil),
			result: polled{nil, true}, cmd: &byGuard, next: 5, recent: []RecentType{{4, CommandAlarm}},
			entry: &wantEntry{event: EvCancelled, keys: keysDemoCanc}},
		{name: "Poll: die Box kennt höhere Nummern – der wartende bekommt eine neue und wird ausgeliefert", php: "poll.php 61-77, 100-113",
			start: withCommand(&pendingTest, 5, r4...), op: poll(50, box1),
			result: polled{ptr(func() Command { c := deliveredNow; c.ID = 51; return c }()), true},
			cmd:    ptr(func() Command { c := deliveredNow; c.ID = 51; return c }()), next: 52,
			recent: []RecentType{{4, CommandTest}, {51, CommandTest}}, entry: &wantEntry{event: EvDelivered, keys: keysDeliv}},
		{name: "Poll: die Box kennt höhere Nummern, kein Befehl – die Nummer wird gemerkt (gesichert)", php: "poll.php 61-65, 75-77",
			start: withCommand(nil, 1), op: poll(50, box1), result: polled{nil, true}, next: 51},
		{name: "Poll: dieselbe Nummer wie bekannt ändert nichts", php: "poll.php 62",
			start: withCommand(ptr(acked(pendingTest, "executed")), 5, r4...), op: poll(4, box1), result: polled{nil, false}, same: true},
	}
}

// TestCommandLifeCycle: jeder Übergang eines Befehls – das Ergebnis des
// Schritts, der Befehl danach, commands.next_id, recent_types und der
// Verlaufseintrag (genau die Felder, die PHP schreibt; nichts, wo nichts
// geschieht). Nach jedem Schritt erfüllt der Zustand die Regeln (Validate).
func TestCommandLifeCycle(t *testing.T) {
	for _, row := range lifeRows() {
		t.Run(row.name, func(t *testing.T) {
			s := NewState()
			row.start(s)
			if err := s.Validate(); err != nil {
				t.Fatalf("Ausgangsstand ungültig: %v", err)
			}
			before := s.Clone()
			got := row.op(s)
			if !reflect.DeepEqual(got, row.result) {
				t.Errorf("Ergebnis %s\nerwartet %s", show(got), show(row.result))
			}
			if err := s.Validate(); err != nil {
				t.Errorf("danach ungültig: %v", err)
			}
			if row.same {
				if !reflect.DeepEqual(s, before) {
					t.Errorf("Zustand geändert: %s", show(s.Commands))
				}
				return
			}
			if !reflect.DeepEqual(s.Commands.Current, row.cmd) {
				t.Errorf("Befehl %s\nerwartet %s", show(s.Commands.Current), show(row.cmd))
			}
			if s.Commands.NextID != row.next {
				t.Errorf("next_id %d, erwartet %d", s.Commands.NextID, row.next)
			}
			if !slices.Equal(s.Commands.RecentTypes, row.recent) {
				t.Errorf("recent_types %v, erwartet %v", s.Commands.RecentTypes, row.recent)
			}
			added := s.Outbox[len(before.Outbox):]
			if row.entry == nil {
				if len(added) != 0 {
					t.Errorf("unerwartete Einträge %v", added)
				}
				return
			}
			if len(added) != 1 {
				t.Fatalf("%d Einträge, erwartet einer: %v", len(added), added)
			}
			checkEntry(t, added[0], *row.entry, s.Commands.Current)
		})
	}
}

// checkEntry vergleicht einen Verlaufseintrag mit dem erwarteten: event,
// log_at, uid und dazu entweder genau diese Felder des Befehls (mit den
// Werten des Befehls danach) oder genau diese Felder oben (late_ack).
func checkEntry(t *testing.T, e Entry, want wantEntry, cmd *Command) {
	t.Helper()
	if e.Str("event") != want.event || e.Int("log_at") != t0.Unix() || e.Str("uid") == "" {
		t.Errorf("Eintrag %v, erwartet %s", e, want.event)
	}
	top := map[string]any{"event": nil, "log_at": nil, "uid": nil}
	if want.top != nil {
		for k, v := range want.top {
			top[k] = v
			if got, err := json.Marshal(e[k]); err != nil || string(got) != string(mustJSON(t, v)) {
				t.Errorf("%s: %s, erwartet %s", k, got, mustJSON(t, v))
			}
		}
	} else {
		top["command"] = nil
		var all map[string]any
		dec := json.NewDecoder(bytes.NewReader(mustJSON(t, cmd)))
		dec.UseNumber()
		if err := dec.Decode(&all); err != nil {
			t.Fatal(err)
		}
		wantCmd := map[string]any{}
		for _, k := range want.keys {
			wantCmd[k] = all[k]
		}
		if !reflect.DeepEqual(e["command"], wantCmd) {
			t.Errorf("Befehl im Eintrag %s\nerwartet %s", mustJSON(t, e["command"]), mustJSON(t, wantCmd))
		}
	}
	for k := range e {
		if _, ok := top[k]; !ok {
			t.Errorf("Feld %q im Eintrag, das PHP nicht schreibt", k)
		}
	}
	if len(e) != len(top) {
		t.Errorf("Felder %v, erwartet %v", e, top)
	}
}

func show(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// Members: die Felder eines Befehls, wie PHP sein Array führt – nur die, die
// sein Weg gesetzt hat, in der Reihenfolge, in der PHP sie setzt (Antworten
// von enqueue, cancel und status; der Verlauf).
func TestCommandMembers(t *testing.T) {
	at := t0.Unix()
	rewound := delivered(pendingDemo, 30)
	rewound.State, rewound.CancelledAt, rewound.CancelledBy, rewound.CancelReason = CommandCancelled, at, "system", RewindCancelReason
	expired := pendingTest
	expired.State, expired.ExpiredAt = CommandExpired, at
	byAdmin := pendingTest
	byAdmin.State, byAdmin.CancelledAt, byAdmin.CancelledBy = CommandCancelled, at, "admin"
	emptyAck := acked(pendingTest, "")
	emptyAck.AckMessage, emptyAck.AckedBy = "", ""
	base := []CommandMember{{"id", 4}, {"type", CommandTest}, {"state", ""}, {"created_at", at - 20}, {"created_by", "admin"}}
	with := func(state string, more ...CommandMember) []CommandMember {
		out := slices.Clone(base)
		out[2].Value = state
		return append(out, more...)
	}
	demo := func(state string, more ...CommandMember) []CommandMember {
		out := with(state, CommandMember{"demo_changed_at", at - 100})
		out[1].Value = CommandAlarm
		return append(out, more...)
	}
	for _, tc := range []struct {
		name string
		cmd  Command
		want []CommandMember
	}{
		{"wartend", pendingTest, with(CommandPending)},
		{"Demo-Alarm wartend", pendingDemo, demo(CommandPending)},
		{"ausgeliefert", delivered(pendingTest, 3), with(CommandDelivered, CommandMember{"delivered_at", at - 3}, CommandMember{"delivered_to", "fw-box-1"})},
		{"quittiert", acked(pendingTest, "executed"), with(CommandAcked, CommandMember{"delivered_at", at - 10}, CommandMember{"delivered_to", "fw-box-1"},
			CommandMember{"acked_at", at - 5}, CommandMember{"ack_result", "executed"}, CommandMember{"ack_message", "vorher"}, CommandMember{"acked_by", "fw-box-1"})},
		{"quittiert mit leeren Texten: die Felder stehen trotzdem da", emptyAck, with(CommandAcked, CommandMember{"delivered_at", at - 10},
			CommandMember{"delivered_to", "fw-box-1"}, CommandMember{"acked_at", at - 5}, CommandMember{"ack_result", ""}, CommandMember{"ack_message", ""},
			CommandMember{"acked_by", ""})},
		{"verfallen", expired, with(CommandExpired, CommandMember{"expired_at", at})},
		{"vom Admin abgebrochen", byAdmin, with(CommandCancelled, CommandMember{"cancelled_at", at}, CommandMember{"cancelled_by", "admin"})},
		{"beim Rücksprung abgebrochen", rewound, demo(CommandCancelled, CommandMember{"delivered_at", at - 30}, CommandMember{"delivered_to", "fw-box-1"},
			CommandMember{"cancelled_at", at}, CommandMember{"cancelled_by", "system"}, CommandMember{"cancel_reason", RewindCancelReason})},
	} {
		if got := tc.cmd.Members(); show(got) != show(tc.want) {
			t.Errorf("%s:\n %s\nerwartet\n %s", tc.name, show(got), show(tc.want))
		}
	}
}

// TestNoteBoxCommandID: die Nummer, die die Box zuletzt gesehen hat (PHP
// api/poll.php 61-77; box-poll-ack.test.mjs "Befehls-Nummern über
// Datenverlust hinweg"). Neue Nummern liegen immer über ihr – sonst
// verwürfe die Box einen neuen Befehl still als Duplikat; ein wartender
// Befehl mit einer Nummer, die sie schon kennt, bekommt eine neue (gemerkt
// in recent_types), ohne Verlaufseintrag (PHP schreibt keinen).
func TestNoteBoxCommandID(t *testing.T) {
	cases := []struct {
		name    string
		cmd     *Command
		next    int
		last    int
		changed bool
		wantID  int // Nummer des laufenden Befehls danach (0 = keiner)
		next2   int
		recent  []RecentType
	}{
		{"wartender Befehl 1, die Box kennt 50: er wird 51, die nächste 52", ptr(created1()), 2, 50, true, 51, 52,
			[]RecentType{{1, CommandTest}, {51, CommandTest}}},
		{"ohne wartenden Befehl merkt sich der Server die 50: die nächste ist 51", nil, 1, 50, true, 0, 51, nil},
		{"die Box kennt nichts (0)", ptr(created1()), 2, 0, false, 1, 2, []RecentType{{1, CommandTest}}},
		{"negative Nummer zählt wie 0", ptr(created1()), 2, -7, false, 1, 2, []RecentType{{1, CommandTest}}},
		{"die Box kennt genau die Nummer des wartenden: neu nummeriert", ptr(created1()), 2, 1, true, 2, 3,
			[]RecentType{{1, CommandTest}, {2, CommandTest}}},
		{"die Box kennt weniger: nichts", ptr(func() Command { c := created1(); c.ID = 7; return c }()), 8, 6, false, 7, 8,
			[]RecentType{{1, CommandTest}}},
		{"die Box kennt die nächste Nummer schon: sie steigt darüber", nil, 8, 8, true, 0, 9, nil},
		{"ausgeliefert, nicht wartend: nur die nächste Nummer steigt", ptr(delivered(created1(), 3)), 2, 50, true, 1, 51,
			[]RecentType{{1, CommandTest}}},
		{"quittiert, die Box meldet dessen Nummer: nichts", ptr(acked(created1(), "executed")), 2, 1, false, 1, 2,
			[]RecentType{{1, CommandTest}}},
		{"unplausibel groß (ab 2^31) zählt nicht", ptr(created1()), 2, 1 << 31, false, 1, 2, []RecentType{{1, CommandTest}}},
		{"die größte plausible Nummer zählt", nil, 1, 1<<31 - 1, true, 0, 1 << 31, nil},
	}
	for _, tc := range cases {
		s := NewState()
		var recent []RecentType
		if tc.cmd != nil {
			recent = []RecentType{{1, CommandTest}}
		}
		withCommand(tc.cmd, tc.next, recent...)(s)
		before := s.Clone()
		if got := s.NoteBoxCommandID(tc.last); got != tc.changed {
			t.Errorf("%s: geändert %v", tc.name, got)
		}
		if !tc.changed && !reflect.DeepEqual(s, before) {
			t.Errorf("%s: ohne Änderung, aber Zustand anders", tc.name)
		}
		id := 0
		if s.Commands.Current != nil {
			id = s.Commands.Current.ID
		}
		if id != tc.wantID || s.Commands.NextID != tc.next2 || !slices.Equal(s.Commands.RecentTypes, tc.recent) {
			t.Errorf("%s: Befehl %d, next_id %d, recent %v – erwartet %d, %d, %v", tc.name, id, s.Commands.NextID,
				s.Commands.RecentTypes, tc.wantID, tc.next2, tc.recent)
		}
		if len(s.Outbox) != len(before.Outbox) {
			t.Errorf("%s: Verlaufseintrag geschrieben", tc.name)
		}
		if err := s.Validate(); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

func created1() Command {
	return Command{ID: 1, Type: CommandTest, State: CommandPending, CreatedAt: t0.Unix() - 5, CreatedBy: "admin"}
}

// TestRecentTypes: Nach 25 Befehlen kennt recent_types die 20 mit den
// höchsten Nummern, aufsteigend (PHP remember_command_type: array_slice
// -20); eine neu nummerierte Nummer kommt dazu, die älteste fällt.
func TestRecentTypes(t *testing.T) {
	s := NewState()
	for i := 1; i <= 25; i++ {
		typ := CommandTest
		if i%3 == 0 {
			typ = CommandAlarm
		}
		now := t0.Add(time.Duration(i-30) * 10 * time.Second)
		if _, why := s.Enqueue(typ, "admin", now, 120); why != "" {
			t.Fatalf("Befehl %d: %s", i, why)
		}
		s.Deliver("fw-box-1", now)
		if kind := s.Ack(i, "fw-box-1", "executed", "", now); kind != AckFirst {
			t.Fatalf("Befehl %d: Quittung %v", i, kind)
		}
	}
	var want []RecentType
	for i := 6; i <= 25; i++ {
		typ := CommandTest
		if i%3 == 0 {
			typ = CommandAlarm
		}
		want = append(want, RecentType{i, typ})
	}
	if !slices.Equal(s.Commands.RecentTypes, want) {
		t.Errorf("recent_types %v\nerwartet %v", s.Commands.RecentTypes, want)
	}
	if err := s.Validate(); err != nil {
		t.Error(err)
	}
	// Die verspätete Quittung von Befehl 6 kennt noch seinen Typ (ALARM), die
	// von 5 nicht mehr.
	before := len(s.Outbox)
	s.Ack(6, "fw-box-1", "executed", "", t0)
	s.Ack(5, "fw-box-1", "executed", "", t0)
	if got := s.Outbox[before:]; len(got) != 2 || got[0].Str("type") != CommandAlarm || got[1].Str("type") != "" {
		t.Errorf("verspätete Quittungen %v", got)
	}
	// Ein neu nummerierter Befehl an der Obergrenze: die neue Nummer kommt
	// dazu, die kleinste fällt.
	if _, why := s.Enqueue(CommandTest, "admin", t0, 120); why != "" {
		t.Fatal(why)
	}
	s.NoteBoxCommandID(40)
	if rt := s.Commands.RecentTypes; len(rt) != 20 || rt[0].ID != 8 || rt[18] != (RecentType{26, CommandTest}) || rt[19] != (RecentType{41, CommandTest}) {
		t.Errorf("nach dem Neu-Nummerieren %v", rt)
	}
	if err := s.Validate(); err != nil {
		t.Error(err)
	}
}

// DemoAlarmStillSafe (PHP demo_alarm_still_safe in api/poll.php): Ein
// Demo-Alarm darf beim Ausliefern noch hinaus, wenn der Demo-Modus noch an ist,
// sein Stand seit dem Anlegen derselbe ist und die Box zuletzt genau einen
// Empfänger gemeldet hat.
func TestDemoAlarmStillSafe(t *testing.T) {
	s := NewState()
	demoOn(s)
	cmd := pendingDemo
	for _, tc := range []struct {
		name string
		edit func(s *State, c *Command)
		box  *BoxStatus
		want bool
	}{
		{"alles wie beim Anlegen", nil, box1, true},
		{"Demo-Modus aus", func(s *State, _ *Command) { s.Demo.Enabled = false }, box1, false},
		{"Demo-Key unbrauchbar: gilt als aus", func(s *State, _ *Command) { s.Demo.Key = "x y" }, box1, false},
		{"Demo-Stand neuer", func(s *State, _ *Command) { s.Demo.ChangedAt++ }, box1, false},
		{"Demo-Stand älter", func(s *State, _ *Command) { s.Demo.ChangedAt-- }, box1, false},
		{"Befehl ohne Demo-Stand (aus PHP übernommen ohne demo_changed_at)", func(_ *State, c *Command) { c.DemoChangedAt = 0 }, box1, false},
		{"Box meldet zwei Empfänger", nil, box2, false},
		{"Box meldet keinen Empfänger", nil, &BoxStatus{Fields: Fields{{"keys_count", "0"}}}, false},
		{"Box meldet die Zahl nicht", nil, &BoxStatus{Fields: Fields{{"device_id", "fw-box-1"}}}, false},
		{"Box nie gesehen", nil, nil, false},
		{"keys_count als PHP-Zahl (\"1abc\")", nil, &BoxStatus{Fields: Fields{{"keys_count", "1abc"}}}, true},
	} {
		st, c := s.Clone(), cmd
		if tc.edit != nil {
			tc.edit(st, &c)
		}
		if got := st.DemoAlarmStillSafe(c, tc.box); got != tc.want {
			t.Errorf("%s: %v, erwartet %v", tc.name, got, tc.want)
		}
	}
}

// Die Ergebnisse von Deliver und Cancel teilen nichts mit dem Entwurf: Wer
// sie nach dem Sichern liest oder ändert, ändert den Schnappschuss nicht.
func TestCommandResultsAreCopies(t *testing.T) {
	s := NewState()
	withCommand(&pendingTest, 5)(s)
	got := s.Deliver("fw-box-1", t0)
	got.State = "geändert"
	if s.Commands.Current.State != CommandDelivered {
		t.Error("Deliver liefert den Befehl des Entwurfs")
	}
	s = NewState()
	withCommand(&pendingTest, 5)(s)
	c, _ := s.PollCommand(0, "fw-box-1", box1, t0, 300)
	c.ID = 99
	if s.Commands.Current.ID != 4 {
		t.Error("PollCommand liefert den Befehl des Entwurfs")
	}
}

// FuzzCommandLifeCycle: beliebige Folgen von Anlegen, Poll, Quittung,
// Abbruch, Verfall, Nummern der Box und Moduswechseln – mit Nummern und
// Ergebnissen, wie sie eine Box schicken kann. Nach jedem Schritt erfüllt
// der Zustand die Regeln (Validate), commands.next_id sinkt nie, und
// HÖCHSTENS EINMAL: Keine Nummer wird zweimal ausgeliefert, und es gibt nie
// mehr Auslieferungen als angelegte Befehle; ausgeliefert wird nur, was
// PollCommand als ausgeliefert zurückgibt.
func FuzzCommandLifeCycle(f *testing.F) {
	f.Add([]byte{0, 2, 2, 3, 2}, int64(0), int64(1), "executed")
	f.Add([]byte{0, 6, 2, 2, 0, 2, 3, 3, 0, 2}, int64(50), int64(51), "")
	f.Add([]byte{1, 7, 2, 1, 2, 4, 5, 5, 0, 2}, int64(-3), int64(99), "skipped_busy")
	f.Add([]byte{0, 0, 0, 13, 13, 2, 6, 2}, int64(1<<31), int64(-1), "duplicate_ignored")
	f.Fuzz(func(t *testing.T, ops []byte, last, ackID int64, result string) {
		s := NewState()
		demoOn(s)
		now := t0
		next := s.Commands.NextID
		delivered := map[int]bool{}
		created, deliveries := 0, 0
		for i, op := range ops {
			if i >= 200 {
				break
			}
			now = now.Add(time.Duration(op/8) * 30 * time.Second)
			outbox := len(s.Outbox)
			switch op % 8 {
			case 0:
				s.Enqueue(CommandTest, "admin", now, 120)
			case 1:
				s.Enqueue(CommandAlarm, "admin", now, 120)
			case 2:
				box := box1
				if op&16 != 0 {
					box = box2
				}
				c, _ := s.PollCommand(int(last%100), "fw-box-1", box, now, 300)
				if c != nil {
					if delivered[c.ID] {
						t.Fatalf("Befehl %d zweimal ausgeliefert", c.ID)
					}
					delivered[c.ID] = true
					if cur := s.Commands.Current; cur == nil || cur.ID != c.ID || cur.State != CommandDelivered {
						t.Fatalf("ausgeliefert %+v, laufend %+v", c, cur)
					}
				}
			case 3:
				s.Ack(int(ackID), "fw-box-1", result, "m", now)
			case 4:
				s.Cancel("admin", "", now)
			case 5:
				s.ExpirePending(now, 300)
			case 6:
				s.NoteBoxCommandID(int(last))
			case 7:
				s.Demo.ChangedAt++ // ein Moduswechsel
			}
			for _, e := range s.Outbox[outbox:] {
				switch e.Str("event") {
				case EvCreated:
					created++
				case EvDelivered:
					deliveries++
				}
			}
			if err := s.Validate(); err != nil {
				t.Fatalf("Schritt %d (%d): %v", i, op%8, err)
			}
			if s.Commands.NextID < next {
				t.Fatalf("next_id sank von %d auf %d", next, s.Commands.NextID)
			}
			next = s.Commands.NextID
		}
		if deliveries > created || deliveries != len(delivered) {
			t.Fatalf("%d Auslieferungen, %d verschiedene Nummern, %d angelegt", deliveries, len(delivered), created)
		}
	})
}
