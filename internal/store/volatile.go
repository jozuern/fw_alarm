package store

import (
	"errors"
	"fmt"
	"sync"

	"fwalarm/internal/model"
)

// Flüchtige Daten (Spec §6.2): Eine Änderung gilt sofort im Speicher; je
// Datei sichert ein eigener Schreiber nebenher den jeweils neuesten Stand.
// Keine Anfrage wartet darauf, ein Fehlschlag hält nichts auf – er steht
// nur in SaveHealth.

// UpdateRuntime ändert die flüchtigen Daten: Kopie ziehen, fn anwenden,
// Zeiger umsetzen – nie ein Fehler, nie ein Warten auf die Platte. fn
// rechnet nur; Verlaufseinträge legt es mit d.Log in die outbox.
func (s *Store) UpdateRuntime(fn func(d *model.Runtime)) {
	s.rtHold.Lock()
	defer s.rtHold.Unlock() // auch, wenn fn einen Laufzeitfehler auslöst
	cur := s.runtime.Load()
	d := cur.Clone()
	fn(d)
	s.noteOverflow(fileRuntime, cur.Outbox, d.Outbox) // cur ist unveränderlich
	s.runtime.Store(d)
	s.rtVer++
	s.rtW.trigger()
}

// UpdateSessions ändert die Sitzungen wie UpdateRuntime die flüchtigen
// Daten: sofort im Speicher, gesichert nebenher.
func (s *Store) UpdateSessions(fn func(d *model.Sessions)) {
	s.ssHold.Lock()
	defer s.ssHold.Unlock()
	d := s.sessions.Load().Clone()
	fn(d)
	s.sessions.Store(d)
	s.ssVer++
	s.ssW.trigger()
}

// Lifebeat setzt das Lebenszeichen des Servers (lifebeat_at = jetzt).
func (s *Store) Lifebeat() {
	now := s.clk.Now().Unix()
	s.UpdateRuntime(func(d *model.Runtime) { d.LifebeatAt = now })
}

// Log hält einen Verlaufseintrag ohne eigene Änderung fest (z.B. key_test):
// Er reist in der outbox der flüchtigen Daten. Gespeichert wird eine eigene
// Kopie – ändert der Aufrufer seinen Eintrag danach, trifft das nichts.
func (s *Store) Log(e model.Entry) { s.logEntry(e) }

// logEntry legt eine Kopie von e in die outbox der flüchtigen Daten; ihre
// uid (leer, wenn e keine hat). Ein Eintrag, der sich nicht als JSON
// schreiben lässt, käme nie auf die Platte und hielte runtime.json auf –
// er wird mit einer Zeile im Journal verworfen.
func (s *Store) logEntry(e model.Entry) (uid string, ok bool) {
	c, ok := copyEntry(e)
	if !ok {
		s.log.Error("Verlaufseintrag nicht schreibbar – verworfen", "event", e.Str("event"))
		return "", false
	}
	s.UpdateRuntime(func(d *model.Runtime) { d.Log(c) })
	return c.Str("uid"), true
}

// noteOverflow: Fielen Einträge aus der outbox, weil sie voll war (Spec
// §6.2: höchstens 500, die ältesten fallen weg), eine Zeile ins Journal.
// before ist die outbox vor der Änderung (sie selbst nimmt nichts heraus).
func (s *Store) noteOverflow(file string, before, after []model.Entry) {
	if len(after) < model.MaxOutbox || len(before) == 0 {
		return
	}
	still := make(map[string]bool, len(after))
	for _, e := range after {
		still[e.Str("uid")] = true
	}
	lost := 0
	for _, e := range before {
		if !still[e.Str("uid")] {
			lost++
		}
	}
	if lost > 0 {
		s.log.Warn("outbox voll – die ältesten Verlaufseinträge fallen weg", "datei", file, "anzahl", lost)
	}
}

// pruneRuntime nimmt aus den flüchtigen Daten, was der Verlauf schon
// angehängt hat – sofort im Speicher; gesichert wird mit dem nächsten
// Durchgang des Schreibers (kick: gleich anstoßen; beim Start nicht).
func (s *Store) pruneRuntime(kick bool) {
	drop := s.appendedUIDs(kindRuntime)
	if len(drop) == 0 {
		return
	}
	s.rtHold.Lock()
	cur := s.runtime.Load()
	n := 0
	for _, e := range cur.Outbox {
		if drop[e.Str("uid")] {
			n++
		}
	}
	if n > 0 {
		d := cur.Clone()
		keep := d.Outbox[:0] // die Liste gehört der Kopie
		for _, e := range d.Outbox {
			if !drop[e.Str("uid")] {
				keep = append(keep, e)
			}
		}
		d.Outbox = keep
		s.runtime.Store(d)
		s.rtVer++
	}
	s.rtHold.Unlock()
	if n > 0 && kick {
		s.rtW.trigger()
	}
}

// ---- Hintergrund-Schreiber ------------------------------------------------

// writer ist ein Hintergrund-Schreiber: Er führt run aus, sobald er
// angestoßen wird – ein Durchgang zur Zeit; was während eines Durchgangs
// anstößt, löst genau einen weiteren aus, der dann den neuesten Stand nimmt.
// run liefert, ob es etwas geschrieben hat (zählt in Store.work), und den
// Fehler; ein Laufzeitfehler darin wird abgefangen (onPanic markiert den
// Schaden).
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

// initWriters legt die Schreiber an (gestartet werden sie in startWriters).
func (s *Store) initWriters() {
	s.rtW = newWriter("runtime.json", s.runtimeRun, func() { s.saved.volatile.Store(false) })
	s.ssW = newWriter("sessions.json", s.sessionsRun, func() { s.saved.sessions.Store(false) })
	s.rescueW = newWriter("recipients.rescue.json", s.rescueRun, func() { s.saved.rescue.Store(false) })
	s.histW = newWriter("history.jsonl", func() (bool, error) { return s.historyRun(true) }, func() {
		// Was beim Anhängen schon in der Datei steht, ist unbekannt: vor
		// dem nächsten Versuch neu lesen (sonst doppelte Zeilen).
		s.hist.mu.Lock()
		s.hist.resync = true
		s.hist.mu.Unlock()
		s.saved.history.Store(false)
	})
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
	w.mu.Lock()
	w.begun++
	w.mu.Unlock()
	did, err := s.runGuarded(w)
	if did {
		s.work.Add(1)
	}
	w.mu.Lock()
	w.ended++
	w.lastErr = err
	close(w.progress)
	w.progress = make(chan struct{})
	w.mu.Unlock()
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

// runtimeRun sichert den neuesten Stand der flüchtigen Daten, wenn er noch
// nicht gesichert ist; danach gehen die Einträge seiner outbox an den
// Verlauf. rtSaved fasst nur dieser Schreiber an.
func (s *Store) runtimeRun() (bool, error) {
	s.rtHold.Lock()
	doc, ver := s.runtime.Load(), s.rtVer
	s.rtHold.Unlock()
	if ver == s.rtSaved {
		return false, nil
	}
	if err := s.saveRuntime(doc); err != nil {
		return false, fmt.Errorf("%s: %w", fileRuntime, err)
	}
	s.rtSaved = ver
	s.handover(kindRuntime, doc.Outbox)
	return true, nil
}

// sessionsRun sichert den neuesten Stand der Sitzungen, wenn nötig.
func (s *Store) sessionsRun() (bool, error) {
	s.ssHold.Lock()
	doc, ver := s.sessions.Load(), s.ssVer
	s.ssHold.Unlock()
	if ver == s.ssSaved {
		return false, nil
	}
	if err := s.saveSessions(doc); err != nil {
		return false, fmt.Errorf("%s: %w", fileSessions, err)
	}
	s.ssSaved = ver
	return true, nil
}

// rescueRun schreibt die Not-Kopie aus dem geltenden Zustand neu, wenn sich
// Liste oder Demo-Modus seit dem letzten Schreiben geändert haben. Erst die
// Version, dann der Zustand lesen (commit setzt erst den Zeiger, dann die
// Version): Die Kopie ist mindestens so neu wie die Version.
func (s *Store) rescueRun() (bool, error) {
	if s.emergency {
		return false, nil // dann ist sie das Einzige, was der Alarm hat
	}
	ver := s.rescueVer.Load()
	if ver == s.rescueSaved {
		return false, nil
	}
	if err := s.saveRescue(model.RescueFrom(s.state.Load(), s.clk.Now())); err != nil {
		return false, fmt.Errorf("%s: %w", fileRescue, err)
	}
	s.rescueSaved = ver
	return true, nil
}
