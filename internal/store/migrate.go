package store

import "fmt"

// Umstellungen älterer Schemata (Spec §6.3): je Dokument eine Tabelle, der
// Schlüssel n hebt ein Dokument von Schema n auf n+1 (Bytes rein, Bytes
// raus). Heute gibt es nur Schema 1, alle Tabellen sind leer. Ein neues
// Feld braucht ein neues Schema und hier seine Umstellung – der strenge
// Leser lehnt ein fehlendes Feld ab.
//
// Vor dem Anheben von state.json wird das Original einmal als
// state.schema-<n>.json abgelegt (open.go). Ein flüchtiges Dokument, dessen
// älteres Schema keine Umstellung hat, wird verworfen wie ein unlesbares.
var (
	stateMigrations    = map[int]func([]byte) ([]byte, error){}
	runtimeMigrations  = map[int]func([]byte) ([]byte, error){}
	sessionsMigrations = map[int]func([]byte) ([]byte, error){}
)

// lift hebt data Schritt für Schritt von Schema from auf target. Fehlt eine
// Stufe oder scheitert sie, ist das Dokument nicht lesbar.
func lift(table map[int]func([]byte) ([]byte, error), data []byte, from, target int) ([]byte, error) {
	for n := from; n < target; n++ {
		step, ok := table[n]
		if !ok {
			return nil, fmt.Errorf("keine Umstellung von Schema %d auf %d", n, n+1)
		}
		next, err := step(data)
		if err != nil {
			return nil, fmt.Errorf("Umstellung von Schema %d auf %d: %w", n, n+1, err)
		}
		data = next
	}
	return data, nil
}
