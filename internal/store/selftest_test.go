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

// Der Speicher-Selbsttest (Spec §7): selftest.tmp schreiben, zurücklesen,
// löschen – jeder Schritt mit eigenem Namen; danach "save", wenn das letzte
// Sichern eines Dokuments, der Not-Kopie oder des Verlaufs scheiterte.
// Resave sichert dann noch einmal, und der Test ist wieder in Ordnung.
func TestSelfTest(t *testing.T) {
	ok := store.SelfTest{OK: true, Step: "ok"}
	f := storetest.New()
	s, _ := openFake(t, f)
	if got := s.SelfTest(); got != ok {
		t.Fatalf("ohne Störung: %+v", got)
	}
	if _, left := f.Get(at("selftest.tmp")); left {
		t.Error("selftest.tmp bleibt liegen")
	}

	boom := errors.New("Platte voll")
	for _, c := range []struct {
		step        string
		spoil, mend func()
	}{
		{"write", func() {
			f.PutDir(at("selftest.tmp"))
			f.Put(at("selftest.tmp", "inhalt"), []byte("x"))
		}, func() {
			if err := f.Remove(at("selftest.tmp", "inhalt")); err != nil {
				t.Fatal(err)
			}
			if err := f.Remove(at("selftest.tmp")); err != nil {
				t.Fatal(err)
			}
		}},
		{"read", func() { f.Mangle(at("selftest.tmp"), func(b []byte) []byte { return append(b, '!') }) },
			func() { f.Mangle(at("selftest.tmp"), nil) }},
		{"delete", func() { f.FailOp(at("selftest.tmp"), "Remove", boom) },
			func() { f.FailOp(at("selftest.tmp"), "Remove", nil) }},
	} {
		c.spoil()
		if got := s.SelfTest(); got.OK || got.Step != c.step {
			t.Errorf("%s: %+v", c.step, got)
		}
		if c.step == "read" {
			if _, left := f.Get(at("selftest.tmp")); left {
				t.Error("read: selftest.tmp bleibt liegen")
			}
		}
		c.mend()
		if got := s.SelfTest(); got != ok {
			t.Errorf("nach %s: %+v", c.step, got)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, c := range []struct {
		name    string
		tmp     string
		trigger func()
	}{
		{"state.json", "state.json.tmp", func() {
			if err := s.Update(ctx, addRecipient("Dora", nil)); !errors.Is(err, store.ErrStorage) {
				t.Errorf("Update: %v", err)
			}
		}},
		{"runtime.json", "runtime.json.tmp", func() {
			s.UpdateRuntime(func(d *model.Runtime) { d.LifebeatAt++ })
			_ = s.Flush(ctx) // scheitert – gemeldet über SaveHealth
		}},
		{"sessions.json", "sessions.json.tmp", func() {
			s.UpdateSessions(func(d *model.Sessions) { d.Sessions["ab12"] = session("admin") })
			_ = s.Flush(ctx)
		}},
		{"history.jsonl", "history.jsonl", func() {
			s.Log(entry("Verlauf gestört"))
			_ = s.Flush(ctx)
		}},
		{"recipients.rescue.json", "recipients.rescue.json.tmp", func() {
			update(t, s, addRecipient("Emil", nil))
			_ = s.Flush(ctx)
		}},
	} {
		f.Fail(at(c.tmp), boom)
		c.trigger()
		if got := s.SelfTest(); got.OK || got.Step != "save" {
			t.Errorf("%s gescheitert: %+v (SaveHealth %+v)", c.name, got, s.SaveHealth())
		}
		f.Heal()
		s.Resave(ctx)
		if got := s.SelfTest(); got != ok {
			t.Errorf("%s nach Resave: %+v (SaveHealth %+v)", c.name, got, s.SaveHealth())
		}
	}
	flush(t, s)
	if st := stateIn(f, "state.json"); !same(t, st, s.State()) {
		t.Error("state.json ist nicht der Stand im Speicher")
	}
	if rt := runtimeIn(f); rt == nil || rt.LifebeatAt != s.Runtime().LifebeatAt {
		t.Errorf("runtime.json: %+v", rt)
	}

	// Auch im Notbetrieb läuft der Selbsttest (selftest.tmp ist nichts Festes).
	g := storetest.New()
	g.Put(at("state.json"), garbage)
	e, _ := openFake(t, g)
	if got := e.SelfTest(); !e.Emergency() || got != ok {
		t.Errorf("Notbetrieb: %+v", got)
	}
}
