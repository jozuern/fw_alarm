package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"fwalarm/internal/model"
	"fwalarm/internal/store"
	"fwalarm/internal/store/storetest"
)

// Flüchtiges wartet auf nichts: Bei hängender Platte kehren 1 000
// Änderungen zurück und gelten sofort; nach dem Lösen hält EIN weiteres
// Sichern den neuesten Stand (was währenddessen kam, ist zusammengefasst).
func TestVolatileNeverBlocks(t *testing.T) {
	for _, c := range []struct {
		file   string
		change func(s *store.Store, i int)
		got    func(s *store.Store) int64
		saved  func(f *storetest.FaultFS) int64
	}{
		{"runtime.json",
			func(s *store.Store, i int) { s.UpdateRuntime(func(d *model.Runtime) { d.LifebeatAt = int64(i) }) },
			func(s *store.Store) int64 { return s.Runtime().LifebeatAt },
			func(f *storetest.FaultFS) int64 {
				if rt := runtimeIn(f); rt != nil {
					return rt.LifebeatAt
				}
				return -1
			}},
		{"sessions.json",
			func(s *store.Store, i int) {
				s.UpdateSessions(func(d *model.Sessions) { x := session("admin"); x.LastSeen = int64(i); d.Sessions["ab12"] = x })
			},
			func(s *store.Store) int64 { return s.Sessions().Sessions["ab12"].LastSeen },
			func(f *storetest.FaultFS) int64 {
				if ss := sessionsIn(f); ss != nil {
					return ss.Sessions["ab12"].LastSeen
				}
				return -1
			}},
	} {
		t.Run(c.file, func(t *testing.T) {
			f := storetest.New()
			s, _ := openFake(t, f)
			release := hang(t, f, at(c.file+".tmp"))
			base := f.Calls()
			done := make(chan struct{})
			go func() {
				for i := 1; i <= 1000; i++ {
					c.change(s, i)
				}
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("Änderungen warten auf die hängende Platte")
			}
			if got := c.got(s); got != 1000 {
				t.Errorf("im Speicher: %d", got)
			}
			release()
			flush(t, s)
			if got := c.saved(f); got != 1000 {
				t.Errorf("gesichert: %d", got)
			}
			if n := countSince(f, base, "WriteFileSync", at(c.file+".tmp")); n < 1 || n > 2 {
				t.Errorf("%d Sicherungen für 1 000 Änderungen (erwartet: die hängende und eine danach)", n)
			}
		})
	}
}

// Scheitert das Sichern flüchtiger Daten, merkt es nur SaveHealth: keine
// Änderung wartet, keine meldet einen Fehler, alles gilt im Speicher; nach
// der Reparatur holt das nächste Sichern alles nach.
func TestVolatileSaveFailureIsSilent(t *testing.T) {
	f := storetest.New()
	s, _ := openFake(t, f)
	for _, name := range []string{"runtime.json.tmp", "sessions.json.tmp"} {
		f.PutDir(at(name)) // wie der Prüfstand: ein nicht leerer Ordner unter dem Temp-Namen
		f.Put(at(name, "inhalt"), []byte("x"))
	}
	s.UpdateRuntime(func(d *model.Runtime) { d.LifebeatAt = 42 })
	s.UpdateSessions(func(d *model.Sessions) { d.Sessions["ab12"] = session("admin") })
	if s.Runtime().LifebeatAt != 42 || len(s.Sessions().Sessions) != 1 {
		t.Fatal("flüchtige Änderung gilt nicht im Speicher")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Flush(ctx); !errors.Is(err, store.ErrStorage) {
		t.Errorf("Flush: %v", err)
	}
	if h := s.SaveHealth(); h.Volatile || h.Sessions || !h.Durable || !h.History || !h.Rescue {
		t.Errorf("SaveHealth: %+v", h)
	}
	if st := s.SelfTest(); st.OK || st.Step != "save" {
		t.Errorf("SelfTest: %+v", st)
	}
	for _, name := range []string{"runtime.json.tmp", "sessions.json.tmp"} {
		if err := f.Remove(at(name, "inhalt")); err != nil {
			t.Fatal(err)
		}
		if err := f.Remove(at(name)); err != nil {
			t.Fatal(err)
		}
	}
	flush(t, s)
	if h := s.SaveHealth(); !h.Volatile || !h.Sessions {
		t.Errorf("SaveHealth nach der Reparatur: %+v", h)
	}
	if rt := runtimeIn(f); rt == nil || rt.LifebeatAt != 42 {
		t.Errorf("runtime.json: %+v", rt)
	}
	if ss := sessionsIn(f); ss == nil || len(ss.Sessions) != 1 {
		t.Errorf("sessions.json: %+v", ss)
	}
}

// Die Not-Kopie folgt jeder Änderung an Liste oder Demo-Modus – nebenher:
// Kann sie nicht geschrieben werden, gelingt die Änderung trotzdem, nur
// SaveHealth().Rescue meldet es; Resave holt sie nach. Eine Änderung, die
// Liste und Demo nicht berührt, schreibt sie nicht.
func TestRescueFollowsChanges(t *testing.T) {
	f := storetest.New()
	s, _ := openFake(t, f)
	rescue := func() model.Rescue {
		b, _ := f.Get(at("recipients.rescue.json"))
		return model.DecodeRescueLenient(b)
	}
	labels := func(r model.Rescue) []string {
		var out []string
		for _, x := range r.Recipients {
			out = append(out, x.Label)
		}
		return out
	}

	update(t, s, addRecipient("Dora", nil))
	flush(t, s)
	r := rescue()
	if len(r.Recipients) != 1 || r.Recipients[0].Label != "Dora" || r.Recipients[0].Key != "testkey-dora" || r.DemoKeyID != 0 {
		t.Fatalf("Not-Kopie nach dem Anlegen: %+v", r)
	}
	dora := s.State().Recipients[0]

	update(t, s, func(d *model.State) error {
		d.Demo = model.Demo{Enabled: true, KeyID: dora.ID, Key: dora.Key, Label: dora.Label, ChangedAt: t0.Unix(), By: "admin"}
		d.Bump(0)
		return nil
	})
	flush(t, s)
	if r := rescue(); r.DemoKeyID != dora.ID {
		t.Errorf("Not-Kopie nach Demo an: %+v", r)
	}

	update(t, s, func(d *model.State) error {
		model.ApplyMute(d.Find(dora.ID), true, t0)
		d.Bump(0)
		return nil
	})
	flush(t, s)
	if r := rescue(); len(r.Recipients) != 1 || !r.Recipients[0].Muted {
		t.Errorf("Not-Kopie nach Stumm: %+v", r)
	}

	base := f.Calls()
	update(t, s, func(d *model.State) error { d.Settings.MuteMaxSeconds = 7200; return nil })
	flush(t, s)
	if n := countSince(f, base, "WriteFileSync", at("recipients.rescue.json.tmp")); n != 0 {
		t.Errorf("Not-Kopie ohne Änderung an Liste oder Demo %d-mal geschrieben", n)
	}

	f.Fail(at("recipients.rescue.json.tmp"), errors.New("Platte voll"))
	update(t, s, addRecipient("Emil", nil)) // gelingt – die Not-Kopie hält nichts auf
	if !hasLabel(s.State(), "Emil") || !hasLabel(stateIn(f, "state.json"), "Emil") {
		t.Fatal("Änderung gilt nicht")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Flush(ctx); !errors.Is(err, store.ErrStorage) {
		t.Errorf("Flush mit unschreibbarer Not-Kopie: %v", err)
	}
	if h := s.SaveHealth(); h.Rescue || !h.Durable {
		t.Errorf("SaveHealth: %+v", h)
	}
	if got := labels(rescue()); len(got) != 1 {
		t.Errorf("Not-Kopie trotz Fehler geschrieben: %v", got)
	}
	f.Heal()
	s.Resave(bounded(t))
	if h := s.SaveHealth(); !h.Rescue {
		t.Errorf("SaveHealth nach Resave: %+v", h)
	}
	if got := labels(rescue()); len(got) != 2 || got[1] != "Emil" {
		t.Errorf("Not-Kopie nach Resave: %v", got)
	}
}

// Seltsame Texte (ein rohes 0xFF-Byte, Anführungszeichen, "<", U+2028) in
// Name, Verlauf, Sitzung und Merkern: Das Sichern gelingt, ein Neustart
// liest alles wieder (ungültiges UTF-8 als U+FFFD) – und der Zustand im
// Speicher ist schon nach dem Sichern genau der, den die Platte hält.
func TestSaveSurvivesOddStrings(t *testing.T) {
	odd := "Anna \xff \"zitiert\" <b>&amp;   Ende"
	want := "Anna � \"zitiert\" <b>&amp;   Ende"
	f := storetest.New()
	s, _, err := store.Open(dir, f, clockAt(), quiet())
	if err != nil {
		t.Fatal(err)
	}
	e := model.NewEntry(model.EvKeyAdded, t0, map[string]any{"by": "admin", "label": odd})
	update(t, s, func(d *model.State) error {
		id := d.AllocateID()
		d.Recipients = append(d.Recipients, model.Recipient{ID: id, Key: "testkey-anna-01", Label: odd, AddedAt: t0.Unix(), MuteToken: "tokAnna0001"})
		d.Demo = model.Demo{Enabled: true, KeyID: id, Key: "testkey-anna-01", Label: odd, ChangedAt: t0.Unix(), By: odd}
		d.Log(e)
		return nil
	})
	if got := s.State().Recipients[0].Label; got != want {
		t.Errorf("im Speicher nach dem Sichern: %q", got)
	}
	s.Log(model.NewEntry(model.EvKeyTest, t0, map[string]any{"label": odd}))
	s.UpdateSessions(func(d *model.Sessions) { x := session("admin"); x.User = odd; d.Sessions["ab12"] = x })
	s.UpdateRuntime(func(d *model.Runtime) { d.Watch.LastRunLine = odd })
	if err := s.Close(bounded(t)); err != nil {
		t.Fatalf("Close: %v", err)
	}

	re, info := open(t, f)
	if info.Mode != store.StartNormal || info.RuntimeDiscarded || info.SessionsDiscarded {
		t.Fatalf("Neustart: %s, %+v", mode(info.Mode), info)
	}
	st := re.State()
	if len(st.Recipients) != 1 || st.Recipients[0].Label != want || st.Demo.Label != want || st.Demo.By != want {
		t.Errorf("Zustand nach dem Neustart: %+v %+v", st.Recipients, st.Demo)
	}
	if got := re.Runtime().Watch.LastRunLine; got != want {
		t.Errorf("Merker: %q", got)
	}
	if got := re.Sessions().Sessions["ab12"].User; got != want {
		t.Errorf("Sitzung: %q", got)
	}
	hist := re.History(10, nil)
	if len(hist) != 2 {
		t.Fatalf("Verlauf: %d Einträge", len(hist))
	}
	for _, h := range hist {
		if h.Str("label") != want {
			t.Errorf("Verlauf: %q", h.Str("label"))
		}
	}
	for _, line := range fileLines(f, "history.jsonl") {
		for _, r := range line {
			if r == ' ' || r == ' ' {
				t.Errorf("roher Zeilentrenner im Verlauf: %q", line)
			}
		}
	}
}

// Flush wartet, bis alles Ausstehende auf der Platte liegt: Zustand,
// flüchtige Daten, Sitzungen, Verlauf, Not-Kopie; die outbox ist danach
// leer. Er wiederholt ein gescheitertes Sichern, meldet bleibende Fehler und
// gibt an seiner Frist auf.
func TestFlush(t *testing.T) {
	f := storetest.New()
	s, _ := openFake(t, f)
	e1, e2 := entry("fest"), entry("flüchtig")
	update(t, s, addRecipient("Dora", e1))
	s.UpdateRuntime(func(d *model.Runtime) { d.LifebeatAt = 77 })
	s.UpdateSessions(func(d *model.Sessions) { d.Sessions["ab12"] = session("admin") })
	s.Log(e2)
	flush(t, s)

	if st := stateIn(f, "state.json"); !hasLabel(st, "Dora") || len(st.Outbox) != 0 {
		t.Errorf("state.json: %+v", st)
	}
	if rt := runtimeIn(f); rt == nil || rt.LifebeatAt != 77 || len(rt.Outbox) != 0 {
		t.Errorf("runtime.json: %+v", rt)
	}
	if ss := sessionsIn(f); ss == nil || len(ss.Sessions) != 1 {
		t.Errorf("sessions.json: %+v", ss)
	}
	for _, e := range []model.Entry{e1, e2} {
		if n := uidCount(f, uidOf(e)); n != 1 {
			t.Errorf("Verlauf: %s %d-mal", e.Str("label"), n)
		}
	}
	if b, _ := f.Get(at("recipients.rescue.json")); len(model.DecodeRescueLenient(b).Recipients) != 1 {
		t.Errorf("Not-Kopie: %s", b)
	}

	// Hängt das Sichern, gibt Flush an seiner Frist auf; danach gelingt es.
	release := hang(t, f, at("runtime.json.tmp"))
	base := f.Calls()
	s.UpdateRuntime(func(d *model.Runtime) { d.LifebeatAt = 78 })
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- s.Flush(ctx) }()
	eventually(t, "runtime.json hängt", func() bool { return calledSince(f, base, "WriteFileSync", at("runtime.json.tmp")) })
	cancel()
	if err := recv(t, errc); !errors.Is(err, context.Canceled) {
		t.Errorf("Flush an der Frist: %v", err)
	}
	release()
	flush(t, s)
	if rt := runtimeIn(f); rt == nil || rt.LifebeatAt != 78 {
		t.Errorf("runtime.json nach dem Lösen: %+v", rt)
	}

	// Ein gescheitertes Sichern meldet Flush; der nächste Flush wiederholt es.
	f.Fail(at("runtime.json.tmp"), errors.New("Platte voll"))
	s.UpdateRuntime(func(d *model.Runtime) { d.LifebeatAt = 79 })
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	if err := s.Flush(ctx2); !errors.Is(err, store.ErrStorage) {
		t.Errorf("Flush bei Fehler: %v", err)
	}
	f.Heal()
	flush(t, s)
	if rt := runtimeIn(f); rt == nil || rt.LifebeatAt != 79 {
		t.Errorf("runtime.json nach dem Wiederholen: %+v", rt)
	}
}

// Ein geordnetes Beenden verliert nichts (Spec §6.2): Nach Close sieht ein
// neuer Start jede flüchtige Änderung und jeden Eintrag. Close ist mehrfach
// rufbar, beendet alle Schreiber, und danach wird nichts Festes mehr
// angenommen.
func TestCloseWritesEverything(t *testing.T) {
	baseline := runtimeGoroutines()
	f := storetest.New()
	s, _, err := store.Open(dir, f, clockAt(), quiet())
	if err != nil {
		t.Fatal(err)
	}
	e1, e2 := entry("fest"), entry("flüchtig")
	update(t, s, addRecipient("Dora", e1))
	s.UpdateSessions(func(d *model.Sessions) { d.Sessions["ab12"] = session("admin") })
	s.Log(e2)
	s.Lifebeat()
	if !s.LogAndWait(entry("gewartet"), time.Hour) {
		t.Error("LogAndWait")
	}
	if err := s.Close(bounded(t)); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Close(bounded(t)); err != nil {
		t.Errorf("zweites Close: %v", err)
	}
	if err := s.Update(bounded(t), addRecipient("Emil", nil)); !errors.Is(err, store.ErrDropped) {
		t.Errorf("Update nach Close: %v", err)
	}
	eventually(t, "alle Schreiber beendet", func() bool { return runtimeGoroutines() <= baseline })

	re, _ := open(t, f)
	if re.Runtime().LifebeatAt != t0.Unix() {
		t.Errorf("Lebenszeichen: %d", re.Runtime().LifebeatAt)
	}
	if len(re.Sessions().Sessions) != 1 {
		t.Errorf("Sitzungen: %+v", re.Sessions())
	}
	hist := re.History(10, nil)
	for _, e := range []model.Entry{e1, e2} {
		if n := countIn(hist, uidOf(e)); n != 1 {
			t.Errorf("Verlauf: %s %d-mal", e.Str("label"), n)
		}
	}
	if len(hist) != 3 {
		t.Errorf("Verlauf: %d Einträge", len(hist))
	}
	if n := len(outboxOf(f, "state.json")) + len(outboxOf(f, "runtime.json")); n != 0 {
		t.Errorf("outbox nach Close nicht leer: %d", n)
	}
}
