package store_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fwalarm/internal/model"
	"fwalarm/internal/store"
	"fwalarm/internal/store/storetest"
)

// Fristen (Spec §6.2): (a) ein Sichern, das über die Frist hängt, meldet
// ErrLate – und gilt, sobald es fertig ist, samt Verlauf; (b) wer hinter ihm
// wartet, gibt an seiner Frist auf (ErrDropped), seine Änderung läuft nie;
// (c) ein gescheitertes Sichern ist ErrStorage, nichts ändert sich.
func TestUpdateDeadlines(t *testing.T) {
	t.Run("a und b: Sichern hängt über die Frist", func(t *testing.T) {
		f := storetest.New()
		s, _ := openFake(t, f)
		release := hang(t, f, at("state.json.tmp"))
		e := entry("spät")

		ctx, cancel := context.WithCancel(context.Background())
		base := f.Calls()
		errc := make(chan error, 1)
		go func() { errc <- s.Update(ctx, addRecipient("Dora", e)) }()
		eventually(t, "Sichern hängt", func() bool { return calledSince(f, base, "WriteFileSync", at("state.json.tmp")) })
		cancel() // die Frist läuft ab
		if err := <-errc; !errors.Is(err, store.ErrLate) {
			t.Fatalf("Update über die Frist: %v", err)
		}
		if hasLabel(s.State(), "Dora") {
			t.Error("Änderung gilt, bevor sie gesichert ist")
		}
		if n := countIn(s.History(100, nil), uidOf(e)); n != 0 {
			t.Error("Verlauf zeigt eine Änderung, die noch nicht gilt")
		}

		// (b) Eine zweite Änderung wartet hinter der hängenden und gibt an
		// ihrer Frist auf: Sie lief nie.
		var ran atomic.Bool
		ctx2, cancel2 := context.WithCancel(context.Background())
		errc2 := make(chan error, 1)
		go func() {
			errc2 <- s.Update(ctx2, func(d *model.State) error { ran.Store(true); return addRecipient("Emil", nil)(d) })
		}()
		cancel2()
		if err := <-errc2; !errors.Is(err, store.ErrDropped) {
			t.Fatalf("wartende Änderung: %v", err)
		}

		release()
		eventually(t, "Änderung gilt nach dem Sichern", func() bool { return hasLabel(s.State(), "Dora") })
		flush(t, s)
		if ran.Load() || hasLabel(s.State(), "Emil") {
			t.Error("die verworfene Änderung lief doch")
		}
		if !hasLabel(stateIn(f, "state.json"), "Dora") {
			t.Error("state.json ohne die späte Änderung")
		}
		if n := uidCount(f, uidOf(e)); n != 1 {
			t.Errorf("Verlauf: Eintrag %d-mal", n)
		}
		if n := countIn(s.History(100, nil), uidOf(e)); n != 1 {
			t.Errorf("History: Eintrag %d-mal", n)
		}
	})

	t.Run("c: Sichern scheitert", func(t *testing.T) {
		f := storetest.New()
		s, _ := openFake(t, f)
		before := s.State()
		f.Fail(at("state.json.tmp"), errors.New("Platte voll"))
		e := entry("gescheitert")
		err := s.Update(context.Background(), addRecipient("Dora", e))
		if !errors.Is(err, store.ErrStorage) || errors.Is(err, store.ErrLate) {
			t.Fatalf("Update: %v", err)
		}
		if s.State() != before || hasLabel(s.State(), "Dora") {
			t.Error("Schnappschuss nach gescheitertem Sichern geändert")
		}
		if s.SaveHealth().Durable {
			t.Error("SaveHealth meldet das gescheiterte Sichern nicht")
		}
		f.Heal()
		flush(t, s)
		if uidCount(f, uidOf(e)) != 0 || countIn(s.History(100, nil), uidOf(e)) != 0 {
			t.Error("Verlauf zeigt eine Änderung, die nicht gilt")
		}
	})

	t.Run("Frist schon abgelaufen: verworfen, ohne zu laufen", func(t *testing.T) {
		f := storetest.New()
		s, _ := openFake(t, f)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		ran := false
		base := f.Calls()
		err := s.Update(ctx, func(d *model.State) error { ran = true; return nil })
		if !errors.Is(err, store.ErrDropped) || ran {
			t.Errorf("Update: %v, lief %v", err, ran)
		}
		if f.Calls() != base {
			t.Errorf("Aufrufe der Dateischicht: %v", f.Log()[base:])
		}
	})

	t.Run("Fehler der Änderung und verletzte Regel: nichts gesichert", func(t *testing.T) {
		f := storetest.New()
		s, _ := openFake(t, f)
		update(t, s, addRecipient("Anna", nil))
		before := s.State()
		base := f.Calls()
		own := errors.New("abgelehnt")
		calls := 0
		if err := s.Update(context.Background(), func(d *model.State) error { calls++; d.KeysVersion = 99; return own }); err != own {
			t.Errorf("Fehler der Änderung: %v", err)
		}
		dup := func(d *model.State) error {
			calls++
			d.Recipients = append(d.Recipients, d.Recipients[0])
			d.Recipients[1].ID = d.AllocateID()
			return nil
		}
		if err := s.Update(context.Background(), dup); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("doppelter Key: %v", err)
		}
		if calls != 2 {
			t.Errorf("Änderungen %d-mal gerufen", calls)
		}
		if s.State() != before {
			t.Error("Schnappschuss geändert")
		}
		for _, c := range f.Log()[base:] {
			if c.Writes() {
				t.Errorf("Schreibaufruf: %v", c)
			}
		}
	})

	t.Run("Notbetrieb: sofort ErrEmergency", func(t *testing.T) {
		f := storetest.New()
		f.Put(at("state.json"), garbage)
		s, _ := openFake(t, f)
		if !s.Emergency() {
			t.Fatal("kein Notbetrieb")
		}
		ran := false
		if err := s.Update(context.Background(), func(d *model.State) error { ran = true; return nil }); !errors.Is(err, store.ErrEmergency) || ran {
			t.Errorf("Update im Notbetrieb: %v, lief %v", err, ran)
		}
	})
}

// Ein Schreiber zur Zeit: 100 gleichzeitige Änderungen zählen je um eins
// hoch – am Ende 100, und nie liefen zwei Änderungen zugleich. Daneben
// laufen flüchtige Änderungen und Leser (Futter für den Race-Detektor).
func TestOneWriterAtATime(t *testing.T) {
	f := storetest.New()
	s, _ := openFake(t, f)
	var inside, most atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = s.State().Settings.MuteMaxSeconds
				_ = s.Runtime().LifebeatAt
				_ = s.History(20, nil)
				_ = s.SaveHealth()
			}
		})
	}
	for i := range 100 {
		wg.Go(func() {
			errs <- s.Update(context.Background(), func(d *model.State) error {
				n := inside.Add(1)
				defer inside.Add(-1)
				for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
				}
				runtime.Gosched()
				d.Settings.MuteMaxSeconds++
				d.Log(entry(fmt.Sprint("fest ", i)))
				return nil
			})
		})
		wg.Go(func() {
			s.UpdateRuntime(func(d *model.Runtime) { d.LifebeatAt++ })
			s.Log(entry(fmt.Sprint("flüchtig ", i)))
		})
	}
	wg.Wait()
	close(stop)
	readers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Update: %v", err)
		}
	}
	if got := s.State().Settings.MuteMaxSeconds; got != 100 {
		t.Errorf("Zähler im Zustand: %d", got)
	}
	if got := s.Runtime().LifebeatAt; got != 100 {
		t.Errorf("Zähler der flüchtigen Daten: %d", got)
	}
	if m := most.Load(); m != 1 {
		t.Errorf("%d Änderungen liefen zugleich", m)
	}
	flush(t, s)
	if got := stateIn(f, "state.json"); got == nil || got.Settings.MuteMaxSeconds != 100 {
		t.Errorf("state.json: %+v", got)
	}
	hist := s.History(1000, nil)
	if len(hist) != 200 {
		t.Errorf("History: %d Einträge statt 200", len(hist))
	}
	lines := fileLines(f, "history.jsonl")
	if len(lines) != 200 {
		t.Errorf("history.jsonl: %d Zeilen statt 200", len(lines))
	}
	uids := uidsOf(hist)
	sort.Strings(uids)
	if len(slices.Compact(uids)) != len(hist) {
		t.Error("History zeigt Einträge doppelt")
	}
}

// Leser warten auf nichts (Spec §6.2): Während das Sichern der festen und
// der flüchtigen Daten und das Anhängen an den Verlauf hängen, kehren
// State, Runtime, Sessions, History und SaveHealth sofort zurück.
func TestReadersNeverWait(t *testing.T) {
	f := storetest.New()
	s, _ := openFake(t, f)
	for i := range 50 {
		s.Log(entry(fmt.Sprint("alt ", i)))
	}
	flush(t, s)

	// Der Verlauf hängt (ein Eintrag wartet auf das Anhängen) …
	hang(t, f, at("history.jsonl"))
	base := f.Calls()
	s.Log(entry("hängt im Verlauf"))
	eventually(t, "Anhängen hängt", func() bool { return calledSince(f, base, "AppendSync", at("history.jsonl")) })
	// … runtime.json hängt …
	hang(t, f, at("runtime.json.tmp"))
	base = f.Calls()
	s.UpdateRuntime(func(d *model.Runtime) { d.LifebeatAt = 7 })
	eventually(t, "runtime.json hängt", func() bool { return calledSince(f, base, "WriteFileSync", at("runtime.json.tmp")) })
	// … und state.json hängt, mit einer Änderung, die den Schreiber hält.
	release := hang(t, f, at("state.json.tmp"))
	base = f.Calls()
	errc := make(chan error, 1)
	go func() { errc <- s.Update(context.Background(), addRecipient("Dora", entry("fest"))) }()
	eventually(t, "state.json hängt", func() bool { return calledSince(f, base, "WriteFileSync", at("state.json.tmp")) })

	for name, read := range map[string]func(){
		"State":      func() { _ = s.State() },
		"Runtime":    func() { _ = s.Runtime() },
		"Sessions":   func() { _ = s.Sessions() },
		"History":    func() { _ = s.History(100, nil) },
		"SaveHealth": func() { _ = s.SaveHealth() },
	} {
		times := make(chan time.Duration, 21)
		go func() {
			for range 21 {
				start := time.Now()
				read()
				times <- time.Since(start)
			}
			close(times)
		}()
		var took []time.Duration
		timeout := time.After(10 * time.Second)
		for len(took) < 21 {
			select {
			case d := <-times:
				took = append(took, d)
			case <-timeout:
				t.Fatalf("%s wartet auf die hängende Platte", name)
			}
		}
		slices.Sort(took)
		if median := took[len(took)/2]; median > 10*time.Millisecond {
			t.Errorf("%s: Median %v", name, median)
		}
	}
	if got := s.Runtime().LifebeatAt; got != 7 {
		t.Errorf("flüchtige Änderung gilt nicht sofort: %d", got)
	}
	release()
	if err := <-errc; err != nil {
		t.Errorf("Update nach dem Lösen: %v", err)
	}
}

// Bark ist nie Teil einer Änderung, und ein Laufzeitfehler beim Sichern
// reißt nichts mit: Die Änderung gilt nicht (ErrStorage), der Schreiber ist
// wieder frei; die Hintergrund-Schreiber fangen ihn ab und schreiben danach
// weiter.
func TestWriterSurvivesPanic(t *testing.T) {
	f := storetest.New()
	s, _ := openFake(t, f)
	boom := func([]byte) []byte { panic("Platte explodiert") }

	f.Mangle(at("state.json.tmp"), boom)
	if err := s.Update(context.Background(), addRecipient("Dora", nil)); !errors.Is(err, store.ErrStorage) {
		t.Errorf("Update mit Laufzeitfehler: %v", err)
	}
	f.Mangle(at("state.json.tmp"), nil)
	update(t, s, addRecipient("Emil", nil))
	if hasLabel(s.State(), "Dora") || !hasLabel(s.State(), "Emil") {
		t.Errorf("nach dem Laufzeitfehler: %+v", s.State().Recipients)
	}

	f.Mangle(at("runtime.json.tmp"), boom)
	f.Mangle(at("history.jsonl"), boom)
	e := entry("nach dem Laufzeitfehler")
	s.Log(e)
	s.UpdateRuntime(func(d *model.Runtime) { d.LifebeatAt = 9 })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Flush(ctx); err == nil {
		t.Error("Flush trotz Laufzeitfehler gelungen")
	}
	if h := s.SaveHealth(); h.Volatile {
		t.Errorf("SaveHealth nach dem Laufzeitfehler: %+v", h)
	}
	f.Mangle(at("runtime.json.tmp"), nil)
	// runtime.json wird gesichert, der Eintrag geht an den Verlauf – der
	// scheitert noch.
	if err := s.Flush(ctx); err == nil {
		t.Error("Flush trotz Laufzeitfehler im Verlauf gelungen")
	}
	if h := s.SaveHealth(); !h.Volatile || h.History {
		t.Errorf("SaveHealth nach dem Laufzeitfehler im Verlauf: %+v", h)
	}
	f.Mangle(at("history.jsonl"), nil)
	s.Resave(context.Background())
	flush(t, s)
	if rt := runtimeIn(f); rt == nil || rt.LifebeatAt != 9 {
		t.Errorf("runtime.json nach dem Laufzeitfehler: %+v", rt)
	}
	if n := uidCount(f, uidOf(e)); n != 1 {
		t.Errorf("Verlauf: Eintrag %d-mal", n)
	}
	if h := s.SaveHealth(); !h.Volatile || !h.History || !h.Durable {
		t.Errorf("SaveHealth danach: %+v", h)
	}
}
