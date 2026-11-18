package store

import (
	"fmt"
	"slices"

	"fwalarm/internal/model"
)

// Flüchtige Daten (Spec §6.2): Eine Änderung gilt sofort im Speicher; je
// Datei sichert ein eigener Schreiber nebenher den jeweils neuesten Stand.
// Keine Anfrage wartet darauf, ein Fehlschlag hält nichts auf – er steht
// nur in SaveHealth.

// UpdateRuntime ändert die flüchtigen Daten: Kopie ziehen, fn anwenden,
// Zeiger umsetzen – nie ein Fehler, nie ein Warten auf die Platte. fn
// rechnet nur; Verlaufseinträge legt es mit d.Log in die outbox (ein
// Eintrag ohne uid bekommt hier eine, mit einer Zeile im Journal).
func (s *Store) UpdateRuntime(fn func(d *model.Runtime)) {
	s.rtHold.Lock()
	defer s.rtHold.Unlock() // auch, wenn fn einen Laufzeitfehler auslöst
	cur := s.runtime.Load()
	d := cur.Clone()
	fn(d)
	s.ensureUIDs(fileRuntime, d.Outbox)
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
func (s *Store) Log(e model.Entry) {
	if c, ok := s.entryCopy(e); ok {
		s.UpdateRuntime(func(d *model.Runtime) { d.Log(c) })
	}
}

// entryCopy: eine eigene Kopie von e mit uid (fehlt sie, eine neue und eine
// Zeile im Journal). Ein Eintrag, der sich nicht als JSON schreiben lässt,
// käme nie auf die Platte und hielte runtime.json auf – er wird mit einer
// Zeile im Journal verworfen (false).
func (s *Store) entryCopy(e model.Entry) (model.Entry, bool) {
	c, ok := copyEntry(e)
	if !ok {
		s.log.Error("Verlaufseintrag nicht schreibbar – verworfen", "event", e.Str("event"))
		return nil, false
	}
	box := []model.Entry{c}
	s.ensureUIDs(fileRuntime, box)
	return box[0], true
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

// fixRuntimeUIDs: Einträge ohne uid in der outbox der flüchtigen Daten (aus
// runtime.json beim Start) bekommen eine; so steht es nicht auf der Platte,
// also gilt der Stand als ungesichert.
func (s *Store) fixRuntimeUIDs() {
	s.rtHold.Lock()
	defer s.rtHold.Unlock()
	cur := s.runtime.Load()
	if !slices.ContainsFunc(cur.Outbox, func(e model.Entry) bool { return !hasUID(e) }) {
		return
	}
	d := cur.Clone()
	s.ensureUIDs(fileRuntime, d.Outbox)
	s.runtime.Store(d)
	s.rtVer++
}

// pruneRuntime nimmt aus den flüchtigen Daten, was der Verlauf schon
// angehängt hat – sofort im Speicher; gesichert wird mit dem nächsten
// Durchgang des Schreibers (kick: gleich anstoßen).
func (s *Store) pruneRuntime(kick bool) {
	drop := s.appendedUIDs(kindRuntime)
	if len(drop) > 0 && s.dropFromRuntime(drop) && kick {
		s.rtW.trigger()
	}
}

// dropFromRuntime: die Einträge mit diesen uids aus der outbox; true =
// es fiel etwas heraus.
func (s *Store) dropFromRuntime(drop map[string]bool) bool {
	s.rtHold.Lock()
	defer s.rtHold.Unlock()
	cur := s.runtime.Load()
	hit := func(e model.Entry) bool { return drop[e.Str("uid")] }
	if !slices.ContainsFunc(cur.Outbox, hit) {
		return false
	}
	d := cur.Clone()
	d.Outbox = slices.DeleteFunc(d.Outbox, hit) // die Liste gehört der Kopie
	s.runtime.Store(d)
	s.rtVer++
	return true
}

// markRuntimeDirty / markSessionsDirty: Der Stand gilt als ungesichert
// (Resave nach einem gescheiterten Sichern).
func (s *Store) markRuntimeDirty() {
	s.rtHold.Lock()
	defer s.rtHold.Unlock()
	s.rtVer++
}

func (s *Store) markSessionsDirty() {
	s.ssHold.Lock()
	defer s.ssHold.Unlock()
	s.ssVer++
}

// initWriters legt die Schreiber an (gestartet werden sie in startWriters).
func (s *Store) initWriters() {
	s.rtW = newWriter("runtime.json", s.runtimeRun, func() { s.saved.volatile.Store(false) })
	s.ssW = newWriter("sessions.json", s.sessionsRun, func() { s.saved.sessions.Store(false) })
	s.rescueW = newWriter("recipients.rescue.json", s.rescueRun, func() { s.saved.rescue.Store(false) })
	s.histW = newWriter("history.jsonl", s.historyRun, func() {
		// Was beim Anhängen schon in der Datei steht, ist unbekannt: vor
		// dem nächsten Versuch neu lesen (sonst doppelte Zeilen).
		s.hist.setResync()
		s.saved.history.Store(false)
	})
}

// runtimeSnapshot: der geltende Stand der flüchtigen Daten und seine Version.
func (s *Store) runtimeSnapshot() (*model.Runtime, uint64) {
	s.rtHold.Lock()
	defer s.rtHold.Unlock()
	return s.runtime.Load(), s.rtVer
}

func (s *Store) sessionsSnapshot() (*model.Sessions, uint64) {
	s.ssHold.Lock()
	defer s.ssHold.Unlock()
	return s.sessions.Load(), s.ssVer
}

// runtimeRun sichert den neuesten Stand der flüchtigen Daten, wenn er noch
// nicht gesichert ist; danach gehen die Einträge seiner outbox an den
// Verlauf. rtSaved fasst nur dieser Schreiber an.
func (s *Store) runtimeRun() (bool, error) {
	doc, ver := s.runtimeSnapshot()
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
	doc, ver := s.sessionsSnapshot()
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
