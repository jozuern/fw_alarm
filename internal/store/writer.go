package store

import (
	"errors"
	"sync"
)

// writer ist ein Hintergrund-Schreiber: Er führt run aus, sobald er
// angestoßen wird – ein Durchgang zur Zeit; was während eines Durchgangs
// anstößt, löst genau einen weiteren aus, der dann den neuesten Stand nimmt.
// run liefert, ob es etwas geschrieben hat (zählt in Store.work), und den
// Fehler; ein Laufzeitfehler darin wird abgefangen (onPanic markiert den
// Schaden – es läuft, nachdem jede Sperre, die run hielt, per defer frei
// ist).
type writer struct {
	name    string
	run     func() (did bool, err error)
	onPanic func()
	kick    chan struct{} // Platz 1: fasst Anstöße zusammen

	mu       sync.Mutex
	begun    uint64        // Durchgänge begonnen
	ended    uint64        // Durchgänge beendet
	lastErr  error         // Fehler des zuletzt beendeten Durchgangs
	progress chan struct{} // wird nach jedem Durchgang geschlossen und ersetzt
}

// errPanic: Ein Durchgang endete mit einem Laufzeitfehler.
var errPanic = errors.New("Laufzeitfehler beim Sichern (im Journal)")

func newWriter(name string, run func() (bool, error), onPanic func()) *writer {
	return &writer{name: name, run: run, onPanic: onPanic, kick: make(chan struct{}, 1), progress: make(chan struct{})}
}

// trigger stößt den Schreiber an; wartet nie.
func (w *writer) trigger() {
	select {
	case w.kick <- struct{}{}:
	default: // schon angestoßen
	}
}

// writerLoop: Durchgänge, bis die Ablage geschlossen wird.
func (s *Store) writerLoop(w *writer) {
	for {
		select {
		case <-w.kick:
		case <-s.stop:
			return
		}
		s.attempt(w)
	}
}

// attempt: ein Durchgang, gezählt; danach erfährt jeder Wartende davon.
func (s *Store) attempt(w *writer) {
	w.begin()
	did, err := s.runGuarded(w)
	if did {
		s.work.Add(1)
	}
	w.end(err)
}

func (w *writer) begin() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.begun++
}

func (w *writer) end(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ended++
	w.lastErr = err
	close(w.progress)
	w.progress = make(chan struct{})
}

// state: wie viele Durchgänge begonnen haben, und der Kanal, der nach dem
// laufenden (oder nächsten) schließt.
func (w *writer) state() (begun uint64, progress chan struct{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.begun, w.progress
}

// result: Ist nach Durchgang b0 einer beendet, dessen Fehler (done = true);
// sonst der Kanal, auf den als Nächstes zu warten ist.
func (w *writer) result(b0 uint64) (progress chan struct{}, done bool, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ended > b0 {
		return nil, true, w.lastErr
	}
	return w.progress, false, nil
}

func (s *Store) runGuarded(w *writer) (did bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			s.logPanic(w.name, r)
			if w.onPanic != nil {
				w.onPanic()
			}
			did, err = false, errPanic
		}
	}()
	return w.run()
}
