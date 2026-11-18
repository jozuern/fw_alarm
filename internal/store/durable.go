package store

import (
	"context"
	"fmt"
	"slices"
	"time"

	"fwalarm/internal/model"
)

// followUpBudget: so lange darf das Nachsichern der outbox dauern, Warten
// auf den Schreiber eingeschlossen (wie jede Änderung neben einer Anfrage,
// Plan "Deadlines for durable changes").
const followUpBudget = 5 * time.Second

// changeMode: was ein Durchgang des Schreibers tun soll.
type changeMode int

const (
	modeUpdate changeMode = iota // eine Änderung: immer sichern
	modePrune                    // Nachsichern: nur wenn angehängte Einträge aus der outbox fallen
	modeForce                    // Resave: sichern, auch wenn sich nichts ändert
)

// Update ändert den festen Zustand (Spec §6.2) – der einzige Weg dazu:
//
//  1. In der Schlange auf den einen Schreiber warten; endet ctx vorher:
//     ErrDropped, nichts ist geschehen (fn läuft nie).
//  2. Mit dem Schreiber: Kopie des Schnappschusses ziehen, Einträge aus der
//     outbox nehmen, die schon im Verlauf stehen, fn(Entwurf) – ein Fehler
//     von fn kommt unverändert zurück, nichts wird gesichert –, dann
//     Validate (verletzt: ErrInvalid).
//  3. Sichern in einer eigenen Goroutine; der Aufrufer wartet darauf oder
//     auf ctx. Rechtzeitig gesichert: Zeiger umsetzen, die neuen Einträge
//     der outbox an den Verlauf geben, die Not-Kopie nebenher erneuern, wenn
//     Liste oder Demo-Modus sich änderten, Schreiber freigeben, nil.
//     Gescheitert: ErrStorage, nichts hat sich geändert. Endet ctx während
//     des Sicherns: sofort ErrLate – das Sichern läuft weiter, gelingt es,
//     gilt die Änderung genau wie oben, und erst dann ist der Schreiber
//     wieder frei.
//
// Im Notbetrieb sofort ErrEmergency. fn wird höchstens einmal gerufen; es
// rechnet nur (kein Netz, keine Datei, keine Uhr außer übergebenen Werten)
// und legt Verlaufseinträge mit d.Log in die outbox.
func (s *Store) Update(ctx context.Context, fn func(d *model.State) error) error {
	_, err := s.change(ctx, fn, modeUpdate)
	return err
}

// change ist der eine Schreiber der festen Daten (Update, Nachsichern der
// outbox, Resave, Flush). saved: es wurde gesichert und gilt.
func (s *Store) change(ctx context.Context, fn func(d *model.State) error, mode changeMode) (saved bool, err error) {
	if s.emergency {
		return false, ErrEmergency
	}
	if s.isClosed() {
		return false, fmt.Errorf("%w: Ablage geschlossen", ErrDropped)
	}
	// Schon abgelaufen: nicht erst anstellen (select wählte sonst zufällig
	// zwischen freiem Schreiber und abgelaufener Frist).
	if ctx.Err() != nil {
		return false, ErrDropped
	}
	select {
	case s.slot <- struct{}{}:
	case <-ctx.Done():
		return false, ErrDropped
	}
	if ctx.Err() != nil {
		<-s.slot
		return false, ErrDropped
	}
	held := true
	defer func() {
		if held { // auch, wenn fn einen Laufzeitfehler auslöst
			<-s.slot
		}
	}()

	cur := s.state.Load()
	d := cur.Clone()
	pruned := s.dropAppended(d)
	switch mode {
	case modeUpdate:
		before := slices.Clone(d.Outbox) // für die Zeile, wenn die outbox überläuft
		if err := fn(d); err != nil {
			return false, err
		}
		s.ensureUIDs(fileState, d.Outbox) // sonst lehnte Validate den Eintrag ab
		s.noteOverflow(fileState, before, d.Outbox)
	case modePrune:
		if pruned == 0 {
			return false, nil
		}
	}
	if err := d.Validate(); err != nil {
		return false, fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	done := make(chan error, 1)
	if !s.spawn("Sichern der festen Daten", func() { s.saveAndCommit(cur, d, done) }) {
		return false, fmt.Errorf("%w: Ablage geschlossen", ErrDropped)
	}
	held = false // gibt jetzt saveAndCommit frei
	select {
	case err := <-done:
		return err == nil, err
	case <-ctx.Done():
		select { // fertig und abgelaufen zugleich: das Ergebnis zählt
		case err := <-done:
			return err == nil, err
		default:
			return false, ErrLate
		}
	}
}

// saveAndCommit sichert den Entwurf und setzt danach – rechtzeitig oder
// nach der Frist – den Zeiger um (commit). Erst dann gibt es den Schreiber
// frei und meldet das Ergebnis. Ein Laufzeitfehler beim Sichern heißt:
// gescheitert, der Schreiber wird trotzdem frei.
func (s *Store) saveAndCommit(prev, draft *model.State, done chan<- error) {
	err := fmt.Errorf("%w: Laufzeitfehler beim Sichern", ErrStorage)
	defer func() {
		if r := recover(); r != nil {
			s.logPanic("Sichern der festen Daten", r)
			s.saved.durable.Store(false)
		}
		<-s.slot
		done <- err
	}()
	doc, serr := s.saveStateDoc(draft)
	if serr != nil {
		s.log.Error("feste Daten nicht gesichert – die Änderung gilt nicht", "err", serr)
		err = fmt.Errorf("%w: %w", ErrStorage, serr)
		return
	}
	s.commit(prev, doc)
	s.work.Add(1)
	err = nil
}

// commit: Der Zustand ist gesichert – jetzt gilt er. Die Einträge seiner
// outbox gehen an den Verlauf; hat sich geändert, was die Not-Kopie hält,
// schreibt sie ihr Schreiber nebenher neu (ein Fehlschlag hält nichts auf).
// Die Reihenfolge Zeiger → rescueVer zählt: Wer rescueVer liest und dann den
// Zustand, sieht mindestens diesen Stand.
func (s *Store) commit(prev, doc *model.State) {
	s.state.Store(doc)
	s.handover(kindState, doc.Outbox)
	if rescueChanged(prev, doc) {
		s.rescueVer.Add(1)
		s.rescueW.trigger()
	}
}

// rescueChanged: Unterscheiden sich die Not-Kopien der beiden Zustände
// (Liste mit Stumm-Zustand, Demo-Empfänger)?
func rescueChanged(a, b *model.State) bool {
	ra, rb := model.RescueFrom(a, time.Time{}), model.RescueFrom(b, time.Time{})
	return ra.DemoKeyID != rb.DemoKeyID || !slices.Equal(ra.Recipients, rb.Recipients)
}

// dropAppended nimmt aus der outbox des Entwurfs, was der Verlauf schon
// angehängt hat; liefert, wie viele.
func (s *Store) dropAppended(d *model.State) int {
	drop := s.appendedUIDs(kindState)
	if len(drop) == 0 {
		return 0
	}
	keep := d.Outbox[:0] // die Liste gehört dem Entwurf (Clone)
	for _, e := range d.Outbox {
		if !drop[e.Str("uid")] {
			keep = append(keep, e)
		}
	}
	n := len(d.Outbox) - len(keep)
	d.Outbox = keep
	return n
}

// pruneLoop ist das Nachsichern der outbox: Nach jedem Anhängen von
// Einträgen der festen Daten nimmt es sie mit einem eigenen Sichern aus der
// outbox – nebenher, höchstens eines wartet (pruneKick fasst zusammen), mit
// 5 s Frist; ein Fehlschlag wird übergangen (das nächste Sichern holt es
// nach, und der Verlauf zeigt nichts doppelt).
func (s *Store) pruneLoop() {
	for {
		select {
		case <-s.pruneKick:
		case <-s.stop:
			return
		}
		ctx, cancel := s.budget(followUpBudget)
		_, err := s.guardedChange(ctx, "Nachsichern der outbox")
		cancel()
		if err != nil {
			s.log.Warn("Nachsichern der outbox gescheitert – das nächste Sichern holt es nach", "err", err)
		}
	}
}

// guardedChange: ein Nachsichern, das einen Laufzeitfehler abfängt, statt
// die Schleife zu beenden.
func (s *Store) guardedChange(ctx context.Context, name string) (saved bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			s.logPanic(name, r)
			saved, err = false, fmt.Errorf("%w: Laufzeitfehler", ErrStorage)
		}
	}()
	return s.change(ctx, nil, modePrune)
}
