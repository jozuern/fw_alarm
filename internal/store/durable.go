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
	return s.UpdateThen(ctx, fn, nil)
}

// UpdateThen ist Update mit after: dasselbe fn, dieselben Fehler – dazu
// läuft after GENAU EINMAL, wenn und nur wenn diese Änderung gilt (gesichert
// und als Schnappschuss gesetzt), ob der Aufrufer nil bekam (rechtzeitig)
// oder ErrLate (das Sichern landete nach der Frist). Für Arbeit, die erst
// nach dem Sichern hinausgehen darf (Meldungen an Menschen) – "Update, dann
// senden" verlöre sie bei ErrLate, und "warten, bis die Version erreicht ist"
// täuscht sich, wenn eine andere Änderung die Version hebt, während diese
// scheiterte.
//
// after läuft NIE, wenn fn einen Fehler liefert (so sagt ein Aufrufer auch
// "nichts zu tun" – liefert fn nil, wird gesichert, auch ohne Unterschied),
// die Änderung eine Regel verletzt (ErrInvalid), die Frist vor dem Beginn
// ablief (ErrDropped), das Sichern scheitert (ErrStorage), im Notbetrieb,
// nach Close und auch dann nicht, wenn das Sichern erst landet, nachdem
// Close den Schreiber aufgegeben hat.
//
// Wo after läuft: auf der Goroutine, die gesichert hat – nach dem Umsetzen
// des Zeigers (after sieht die Änderung in State()) und nachdem sie den einen
// Schreiber freigegeben hat (andere Änderungen warten also nie auf after),
// außerhalb jeder Sperre der Ablage. Bei rechtzeitigem Sichern ist after
// zurück, bevor UpdateThen nil liefert. Close wartet auf diese Goroutine wie
// auf jede der Ablage, innerhalb seiner Frist. Deshalb MUSS after sofort
// zurückkehren und längere Arbeit (Bark) an Core.Bg geben: So kann after
// weder Close aufhalten noch es verklemmen – es hält keine Sperre, Core.Bg
// wartet nie, und eine Änderung aus after heraus läuft oder wird nach
// Close abgelehnt. Ein Laufzeitfehler in after wird abgefangen und ins
// Journal geschrieben (ohne den Wert); die Änderung gilt trotzdem.
func (s *Store) UpdateThen(ctx context.Context, fn func(d *model.State) error, after func()) error {
	_, err := s.change(ctx, fn, modeUpdate, after)
	return err
}

// change ist der eine Schreiber der festen Daten (Update, Nachsichern der
// outbox, Resave, Flush). saved: es wurde gesichert und gilt. after: siehe
// UpdateThen (nil = nichts).
func (s *Store) change(ctx context.Context, fn func(d *model.State) error, mode changeMode, after func()) (saved bool, err error) {
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
	if !s.spawn("Sichern der festen Daten", func() { s.saveAndCommit(cur, d, done, after) }) {
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
// frei, ruft after (nur nach einem commit, siehe UpdateThen) und meldet das
// Ergebnis. Ein Laufzeitfehler beim Sichern heißt: gescheitert, der
// Schreiber wird trotzdem frei.
func (s *Store) saveAndCommit(prev, draft *model.State, done chan<- error, after func()) {
	err := fmt.Errorf("%w: Laufzeitfehler beim Sichern", ErrStorage)
	committed := false
	// Das Ergebnis in einem eigenen defer, als ERSTES angemeldet, läuft also
	// zuletzt: Beendet after die Goroutine mit runtime.Goexit, bricht das den
	// defer darunter mitten in runAfter ab – dieser hier läuft trotzdem, und
	// der Aufrufer wartet nicht bis zum Ende seiner Frist.
	defer func() {
		done <- err // Platz 1: wartet nie, auch wenn der Aufrufer schon fort ist
	}()
	defer func() {
		if r := recover(); r != nil {
			s.logPanic("Sichern der festen Daten", r)
			s.saved.durable.Store(false)
		}
		<-s.slot
		if committed && after != nil {
			s.runAfter(after)
		}
	}()
	doc, serr := s.saveStateDoc(draft)
	if serr != nil {
		s.log.Error("feste Daten nicht gesichert – die Änderung gilt nicht", "err", serr)
		err = fmt.Errorf("%w: %w", ErrStorage, serr)
		return
	}
	s.commit(prev, doc)
	committed = true
	s.work.Add(1)
	err = nil
}

// runAfter ruft after nach einem commit, außerhalb jeder Sperre – außer
// Close hat den Schreiber schon aufgegeben (der Prozess endet gleich; was
// after anstößt, liefe ins Leere). Ein Laufzeitfehler darin wird abgefangen.
func (s *Store) runAfter(after func()) {
	if s.gaveUp.Load() {
		s.log.Warn("Änderung nach dem Aufgeben von Close gesichert – die Arbeit danach entfällt")
		return
	}
	defer func() {
		if r := recover(); r != nil {
			s.logPanic("Arbeit nach einer Änderung", r)
		}
	}()
	after()
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
	return s.change(ctx, nil, modePrune, nil)
}
