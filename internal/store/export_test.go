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
