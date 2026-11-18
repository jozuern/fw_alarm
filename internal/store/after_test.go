package store_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fwalarm/internal/clock"
	"fwalarm/internal/model"
	"fwalarm/internal/store"
	"fwalarm/internal/store/storetest"
)

// afterProbe zählt die Aufrufe von after und hält fest, was after vom
// Zustand sah.
type afterProbe struct {
	s     *store.Store
	label string
	n     atomic.Int32
	saw   atomic.Bool // after sah die Änderung schon im Schnappschuss
	done  chan struct{}
	once  sync.Once
}

func newProbe(s *store.Store, label string) *afterProbe {
	return &afterProbe{s: s, label: label, done: make(chan struct{})}
}

func (p *afterProbe) after() {
	p.n.Add(1)
	p.saw.Store(hasLabel(p.s.State(), p.label))
	p.once.Do(func() { close(p.done) })
}

// never: after wurde nicht gerufen – auch nicht, nachdem alles Ausstehende
// gesichert ist.
func (p *afterProbe) never(t *testing.T, what string) {
	t.Helper()
	flush(t, p.s)
	if n := p.n.Load(); n != 0 {
		t.Errorf("%s: after %d-mal gerufen", what, n)
	}
}

// Rechtzeitig gesichert: after genau einmal – schon gelaufen, wenn
// UpdateThen nil liefert, und es sieht die Änderung im Schnappschuss.
func TestUpdateThenInTime(t *testing.T) {
	s, _ := openFake(t, storetest.New())
	p := newProbe(s, "Dora")
	if err := s.UpdateThen(bounded(t), addRecipient("Dora", nil), p.after); err != nil {
		t.Fatal(err)
	}
	if p.n.Load() != 1 || !p.saw.Load() {
		t.Fatalf("after %d-mal, sah die Änderung: %v", p.n.Load(), p.saw.Load())
	}
	flush(t, s)
	update(t, s, addRecipient("Emil", nil))
	if p.n.Load() != 1 {
		t.Errorf("after %d-mal", p.n.Load())
	}
	// Update ist UpdateThen ohne after.
	if err := s.UpdateThen(bounded(t), addRecipient("Fritz", nil), nil); err != nil || !hasLabel(s.State(), "Fritz") {
		t.Errorf("ohne after: %v", err)
	}
}

// Das Sichern hängt über die Frist: ErrLate – und landet es danach doch,
// läuft after genau einmal, nach dem Umsetzen des Zeigers.
func TestUpdateThenLate(t *testing.T) {
	f := storetest.New()
	s, _ := openFake(t, f)
	release := hang(t, f, at("state.json.tmp"))
	p := newProbe(s, "Dora")
	ctx, cancel := context.WithCancel(context.Background())
	base := f.Calls()
	errc := make(chan error, 1)
	go func() { errc <- s.UpdateThen(ctx, addRecipient("Dora", nil), p.after) }()
	eventually(t, "Sichern hängt", func() bool { return calledSince(f, base, "WriteFileSync", at("state.json.tmp")) })
	cancel()
	if err := recv(t, errc); !errors.Is(err, store.ErrLate) {
		t.Fatalf("UpdateThen über die Frist: %v", err)
	}
	if p.n.Load() != 0 {
		t.Fatal("after vor dem Sichern gerufen")
	}
	release()
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		t.Fatal("after nach dem späten Sichern nie gerufen")
	}
	if !p.saw.Load() {
		t.Error("after sah die Änderung noch nicht")
	}
	flush(t, s)
	if p.n.Load() != 1 {
		t.Errorf("after %d-mal", p.n.Load())
	}
}

// Nie gerufen: Sichern scheitert, die Änderung lehnt ab (so sagt ein Aufrufer
// "nichts zu tun") oder verletzt eine Regel, die Frist war vor dem Beginn um,
// Notbetrieb, Ablage geschlossen.
func TestUpdateThenNever(t *testing.T) {
	f := storetest.New()
	s, _ := openFake(t, f)

	p := newProbe(s, "Dora")
	f.Fail(at("state.json.tmp"), errors.New("Platte voll"))
	if err := s.UpdateThen(bounded(t), addRecipient("Dora", nil), p.after); !errors.Is(err, store.ErrStorage) {
		t.Fatalf("Sichern scheitert: %v", err)
	}
	f.Heal()
	p.never(t, "Sichern scheitert")

	p = newProbe(s, "Dora")
	own := errors.New("nichts zu tun")
	if err := s.UpdateThen(bounded(t), func(*model.State) error { return own }, p.after); err != own {
		t.Errorf("Fehler der Änderung: %v", err)
	}
	p.never(t, "Fehler der Änderung")

	p = newProbe(s, "Dora")
	dup := func(d *model.State) error { d.NextID = 0; return nil } // verletzt Validate nur mit Empfängern
	update(t, s, addRecipient("Anna", nil))
	if err := s.UpdateThen(bounded(t), dup, p.after); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("verletzte Regel: %v", err)
	}
	p.never(t, "verletzte Regel")

	p = newProbe(s, "Dora")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.UpdateThen(ctx, addRecipient("Dora", nil), p.after); !errors.Is(err, store.ErrDropped) {
		t.Errorf("Frist vorher um: %v", err)
	}
	p.never(t, "verworfen")

	closeStore(t, s)
	p = newProbe(s, "Dora")
	if err := s.UpdateThen(bounded(t), addRecipient("Dora", nil), p.after); !errors.Is(err, store.ErrDropped) {
		t.Errorf("nach Close: %v", err)
	}
	if p.n.Load() != 0 {
		t.Error("after nach Close gerufen")
	}

	g := storetest.New()
	g.Put(at("state.json"), garbage)
	e, _ := openFake(t, g)
	p = newProbe(e, "Dora")
	if err := e.UpdateThen(bounded(t), addRecipient("Dora", nil), p.after); !errors.Is(err, store.ErrEmergency) || p.n.Load() != 0 {
		t.Errorf("Notbetrieb: %v, after %d-mal", err, p.n.Load())
	}
}

// 100 gleichzeitige Änderungen: jedes after genau einmal.
func TestUpdateThenConcurrent(t *testing.T) {
	s, _ := openFake(t, storetest.New())
	const n = 100
	counts := make([]atomic.Int32, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.UpdateThen(bounded(t), func(d *model.State) error { d.Bump(0); return nil },
				func() { counts[i].Add(1) }); err != nil {
				t.Errorf("Änderung %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	flush(t, s)
	for i := range n {
		if c := counts[i].Load(); c != 1 {
			t.Errorf("after %d: %d-mal", i, c)
		}
	}
	if v := s.State().KeysVersion; v != n {
		t.Errorf("Version %d", v)
	}
}

// syncBuffer: ein Journal, das der Test lesen kann.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *syncBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *syncBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// Ein Laufzeitfehler in after wird abgefangen und ins Journal geschrieben –
// ohne den Wert –, die Änderung gilt, der Schreiber bleibt frei.
func TestUpdateThenPanicInAfter(t *testing.T) {
	var journal syncBuffer
	s, _, err := store.Open(dir, storetest.New(), clock.NewFake(t0), slog.New(slog.NewTextHandler(&journal, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeStore(t, s) })
	if err := s.UpdateThen(bounded(t), addRecipient("Dora", nil), func() { panic("geheimer-wert") }); err != nil {
		t.Fatalf("Änderung mit abstürzendem after: %v", err)
	}
	if !hasLabel(s.State(), "Dora") {
		t.Error("Änderung gilt nicht")
	}
	log := journal.String()
	if !strings.Contains(log, "Laufzeitfehler in der Ablage abgefangen") || !strings.Contains(log, "nach einer Änderung") ||
		strings.Contains(log, "geheimer-wert") {
		t.Errorf("Journal: %s", log)
	}
	update(t, s, addRecipient("Emil", nil))
	if !hasLabel(s.State(), "Emil") {
		t.Error("nächste Änderung geht nicht")
	}
}

// Beendet after seine Goroutine mit runtime.Goexit (so endet etwa t.FailNow
// in einem after eines Tests), bekommt der Aufrufer sein Ergebnis trotzdem –
// sonst wartete er bis zum Ende seiner Frist und hielte eine gesicherte
// Änderung für verspätet. Die Änderung gilt, der Schreiber ist frei. Goexit
// trifft hier die Goroutine der Ablage, nie die des Tests.
func TestUpdateThenGoexitInAfter(t *testing.T) {
	s, _ := openFake(t, storetest.New())
	errc := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		errc <- s.UpdateThen(ctx, addRecipient("Dora", nil), func() { runtime.Goexit() })
	}()
	if err := recv(t, errc); err != nil {
		t.Fatalf("UpdateThen mit after, das Goexit ruft: %v", err)
	}
	if !hasLabel(s.State(), "Dora") {
		t.Error("Änderung gilt nicht")
	}
	update(t, s, addRecipient("Emil", nil))
	if !hasLabel(s.State(), "Emil") {
		t.Error("nächste Änderung geht nicht")
	}
}

// Close und ein after, das noch aussteht: (a) Landet das hängende Sichern,
// solange Close wartet, läuft after einmal und Close endet sauber. (b) Gibt
// Close den Schreiber auf (Frist um), läuft after nie mehr – auch wenn das
// Sichern danach noch landet.
func TestUpdateThenClose(t *testing.T) {
	t.Run("Close wartet", func(t *testing.T) {
		f := storetest.New()
		s, _ := openFake(t, f)
		release := hang(t, f, at("state.json.tmp"))
		p := newProbe(s, "Dora")
		base := f.Calls()
		errc := make(chan error, 1)
		go func() { errc <- s.UpdateThen(context.Background(), addRecipient("Dora", nil), p.after) }()
		eventually(t, "Sichern hängt", func() bool { return calledSince(f, base, "WriteFileSync", at("state.json.tmp")) })
		closed := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			closed <- s.Close(ctx)
		}()
		// Close läuft (es hält seine Sperre bis zum Ende). Fertig werden kann
		// es nicht, solange das Sichern hängt – Flush braucht den Schreiber –,
		// also landet das Sichern sicher, während Close wartet.
		eventually(t, "Close läuft", s.Closing)
		release()
		if err := recv(t, errc); err != nil {
			t.Errorf("UpdateThen: %v", err)
		}
		if err := recv(t, closed); err != nil {
			t.Errorf("Close: %v", err)
		}
		if p.n.Load() != 1 || !p.saw.Load() {
			t.Errorf("after %d-mal, sah die Änderung: %v", p.n.Load(), p.saw.Load())
		}
	})
	t.Run("Close gibt auf", func(t *testing.T) {
		f := storetest.New()
		s, _ := openFake(t, f)
		release := hang(t, f, at("state.json.tmp"))
		p := newProbe(s, "Dora")
		ctx, cancel := context.WithCancel(context.Background())
		base := f.Calls()
		errc := make(chan error, 1)
		go func() { errc <- s.UpdateThen(ctx, addRecipient("Dora", nil), p.after) }()
		eventually(t, "Sichern hängt", func() bool { return calledSince(f, base, "WriteFileSync", at("state.json.tmp")) })
		cancel()
		if err := recv(t, errc); !errors.Is(err, store.ErrLate) {
			t.Fatalf("UpdateThen: %v", err)
		}
		cctx, ccancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer ccancel()
		if err := s.Close(cctx); err == nil {
			t.Fatal("Close gab den hängenden Schreiber nicht auf")
		}
		release()
		eventually(t, "das Sichern landet doch", func() bool { return hasLabel(s.State(), "Dora") })
		time.Sleep(200 * time.Millisecond) // Zeit, in der after laufen könnte
		if n := p.n.Load(); n != 0 {
			t.Errorf("after nach dem Aufgeben gerufen (%d-mal)", n)
		}
	})
}
