package store

import "fwalarm/internal/model"

// Zugänge der Tests im Paket store_test zu inneren Teilen. Die Tests liegen
// außerhalb des Pakets, weil storetest.FaultFS store importiert (für
// ErrLocked und die Prüfung, dass FaultFS das FS erfüllt) – ein Test im
// Paket selbst könnte storetest sonst nicht nutzen.

// SaveState, SaveRuntime, SaveSessions und SaveRescue sind die inneren
// Sicherungen, auf denen Aufgabe 7 Update und die Schreiber aufbaut.
func (s *Store) SaveState(st *model.State) error      { return s.saveState(st) }
func (s *Store) SaveRuntime(r *model.Runtime) error   { return s.saveRuntime(r) }
func (s *Store) SaveSessions(x *model.Sessions) error { return s.saveSessions(x) }
func (s *Store) SaveRescue(r model.Rescue) error      { return s.saveRescue(r) }

// SetHistoryRotateAt setzt die Grenze, ab der history.jsonl nach
// history.1.jsonl wandert (sonst 5 MB) – damit ein Test rotieren kann.
func (s *Store) SetHistoryRotateAt(n int64) {
	s.hist.mu.Lock()
	defer s.hist.mu.Unlock()
	s.hist.rotateAt = n
}

// SetHistoryMemLimit setzt, wie viele Bytes an Verlaufszeilen der Speicher
// höchstens hält (sonst etwa 12 MB).
func (s *Store) SetHistoryMemLimit(n int64) {
	s.hist.mu.Lock()
	defer s.hist.mu.Unlock()
	s.hist.memLimit = n
}

// SetDefaultHistoryMemLimit setzt die Grenze, mit der Open den Verlauf
// liest; restore stellt den alten Wert her.
func SetDefaultHistoryMemLimit(n int64) (restore func()) {
	old := historyMemLimit
	historyMemLimit = n
	return func() { historyMemLimit = old }
}

// ResyncHistory führt nur den ersten Schritt eines Durchgangs nach einem
// gescheiterten Anhängen aus: history.jsonl neu lesen und als Wahrheit
// nehmen – damit ein Test History genau danach aufrufen kann.
func (s *Store) ResyncHistory() error {
	batch, _ := s.hist.batch()
	_, err := s.historyResync(batch)
	return err
}

// Closing: Läuft gerade ein Close? Close hält closeMu vom Anfang bis zum
// Ende – ist sie nicht zu haben, steckt es mittendrin (vorher und nachher
// ist sie frei).
func (s *Store) Closing() bool {
	if s.closeMu.TryLock() {
		s.closeMu.Unlock()
		return false
	}
	return true
}

// ParseHistoryLine: der nachsichtige Leser einer Verlaufszeile.
func ParseHistoryLine(b []byte) (model.Entry, bool) { return parseLine(b) }

// UIDsIn: welche der gesuchten uids unter den Zeilen stehen.
func UIDsIn(lines [][]byte, want map[string]bool) map[string]bool { return uidsIn(lines, want) }

// MarshalDoc: die Form, in der die Ablage ein Dokument schreibt.
func MarshalDoc(v any) ([]byte, error) { return marshalDoc(v) }

// SetMigration setzt für einen Test eine Umstellung von Schema from ein
// (doc: state, runtime oder sessions); restore stellt den alten Stand her.
func SetMigration(doc string, from int, fn func([]byte) ([]byte, error)) (restore func()) {
	table := map[string]map[int]func([]byte) ([]byte, error){
		"state": stateMigrations, "runtime": runtimeMigrations, "sessions": sessionsMigrations,
	}[doc]
	old, had := table[from]
	table[from] = fn
	return func() {
		if had {
			table[from] = old
		} else {
			delete(table, from)
		}
	}
}
