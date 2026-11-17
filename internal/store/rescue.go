package store

import (
	"slices"

	"fwalarm/internal/model"
)

// readRescue liest die Not-Kopie im Notbetrieb EINMAL, nachsichtig (Spec
// §6.3), und hält sie im Speicher – ein Alarm liest nie eine Datei. Was
// sie für einen Alarm heißt, entscheidet model.Rescue.Targets beim
// Aufrufer (er kennt Zeit und Höchstdauer der Stummschaltung).
func (s *Store) readRescue() {
	raw, err := s.fsys.ReadFile(s.path(fileRescue))
	if err != nil {
		s.log.Error("Notbetrieb: keine Not-Kopie lesbar – es gilt die Not-Liste der Konfiguration", "err", err)
		return
	}
	r := model.DecodeRescueLenient(raw)
	s.rescue = &r
	s.log.Warn("Notbetrieb: Not-Kopie gelesen", "eintraege", len(r.Recipients),
		"demo", r.DemoKeyID != 0, "bytes", len(raw))
}

// Rescue liefert die Not-Kopie, wie sie beim Start im Notbetrieb EINMAL
// nachsichtig gelesen wurde – aus dem Speicher, ohne Auslegung; jeder
// Aufruf bekommt eine eigene Liste. false: keine lesbare Kopie (keine
// Datei, nicht lesbar) oder kein Notbetrieb – nur dort wird sie gelesen.
// Ließ sich die Datei lesen, war aber kein JSON, ist die Kopie leer;
// Targets meldet dann ok=false (Aufgabe 20: Not-Liste der Konfiguration).
func (s *Store) Rescue() (model.Rescue, bool) {
	if s.rescue == nil {
		return model.Rescue{}, false
	}
	r := *s.rescue
	r.Recipients = slices.Clone(r.Recipients)
	return r, true
}
