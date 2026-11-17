package store

import "fwalarm/internal/model"

// readRescue liest die Not-Kopie im Notbetrieb EINMAL, nachsichtig (Spec
// §6.3); danach bedient sie der Speicher – ein Alarm liest nie eine Datei.
func (s *Store) readRescue() {
	raw, err := s.fsys.ReadFile(s.path(fileRescue))
	if err != nil {
		s.log.Error("Notbetrieb: keine Not-Kopie lesbar – es gilt die Not-Liste der Konfiguration", "err", err)
		return
	}
	r := model.DecodeRescueLenient(raw)
	s.rescue = &r
	targets, demo := r.Targets()
	s.log.Warn("Notbetrieb: Not-Kopie gelesen", "empfaenger", len(targets), "demo", demo, "bytes", len(raw))
}

// Rescue liefert die Empfänger der Not-Kopie aus dem Speicher (nur im
// Notbetrieb gelesen): die gültigen, einmaligen Keys, höchstens 10; mit
// gültigem Demo-Empfänger nur ihn. ok=false: keine Kopie oder kein gültiger
// Key darin – dann gilt die Not-Liste der Konfiguration (Aufgabe 20). Jeder
// Aufruf liefert eine eigene Liste.
func (s *Store) Rescue() (targets []model.Target, ok bool) {
	if s.rescue == nil {
		return nil, false
	}
	targets, _ = s.rescue.Targets()
	if len(targets) == 0 {
		return nil, false
	}
	return targets, true
}
