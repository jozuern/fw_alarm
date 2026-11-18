package store

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"runtime/debug"
	"time"
)

// flushRounds: so oft geht Flush höchstens über alle Schreiber. Ein Durchgang
// kann neue Arbeit machen (das Anhängen an den Verlauf lässt die outbox
// nachsichern); nach dem dritten ist ohne neue Änderungen nichts mehr offen.
const flushRounds = 4

// errClosed: Die Ablage ist geschlossen, ihre Schreiber laufen nicht mehr.
var errClosed = errors.New("Ablage geschlossen")

// startWriters startet die Hintergrund-Schreiber und das Nachsichern der
// outbox; sie laufen bis Close.
func (s *Store) startWriters() {
	s.bgCtx, s.bgCancel = context.WithCancel(context.Background())
	s.life.Lock()
	s.started = true
	s.life.Unlock()
	for _, w := range []*writer{s.rtW, s.ssW, s.rescueW, s.histW} {
		s.spawn(w.name, func() { s.writerLoop(w) })
	}
	s.spawn("Nachsichern der outbox", s.pruneLoop)
}

// spawn startet fn als Goroutine der Ablage: Close wartet auf sie, ein
// Laufzeitfehler darin landet ohne Daten im Journal und beendet nur sie.
// Nach Close startet nichts mehr (false).
func (s *Store) spawn(name string, fn func()) bool {
	s.life.RLock()
	if s.closed {
		s.life.RUnlock()
		return false
	}
	s.wg.Add(1)
	s.life.RUnlock()
	go func() {
		defer s.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				s.logPanic(name, r)
			}
		}()
		fn()
	}()
	return true
}

// isClosed: Lief Close schon so weit, dass nichts Neues mehr beginnt?
func (s *Store) isClosed() bool {
	s.life.RLock()
	defer s.life.RUnlock()
	return s.closed
}

// logPanic schreibt einen abgefangenen Laufzeitfehler ins Journal – ohne
// Daten: Name der Arbeit, bei Fehlern der Sprache deren Text, sonst nur die
// Art des Werts, dazu der Stapel (nur Adressen).
func (s *Store) logPanic(name string, r any) {
	kind := fmt.Sprintf("%T", r)
	if rerr, ok := r.(runtime.Error); ok {
		kind = rerr.Error()
	}
	s.log.Error("Laufzeitfehler in der Ablage abgefangen", "arbeit", name, "art", kind, "stapel", string(debug.Stack()))
}

// budget: ein Kontext, der nach d auf der Uhr der Ablage endet (gestellt im
// Test) – und spätestens, wenn die Ablage schließt.
func (s *Store) budget(d time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(s.bgCtx)
	timer := s.clk.After(d)
	if !s.spawn("Frist", func() {
		select {
		case <-timer:
			cancel()
		case <-ctx.Done():
		}
	}) {
		cancel()
	}
	return ctx, cancel
}

// flushWriter stößt w an und wartet auf den ersten Durchgang, der danach
// begann (er nimmt den neuesten Stand); liefert dessen Fehler.
func (s *Store) flushWriter(ctx context.Context, w *writer) error {
	w.mu.Lock()
	b0, ch := w.begun, w.progress
	w.mu.Unlock()
	w.trigger()
	for {
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		case <-s.stop:
			return errClosed
		}
		w.mu.Lock()
		if w.ended > b0 {
			err := w.lastErr
			w.mu.Unlock()
			return err
		}
		ch = w.progress
		w.mu.Unlock()
	}
}

// Flush wartet, bis alles Ausstehende auf der Platte liegt (fw-alarm flush,
// vor einer Sicherung; Close): ein Sichern der festen Daten, das nach seiner
// Frist noch läuft, die neuesten flüchtigen Daten und Sitzungen, die Not-Kopie,
// jeder übergebene Verlaufseintrag – und das Nachsichern der outbox. Ein
// zuvor gescheitertes Sichern wird dabei wiederholt. Fehler: ErrStorage (was
// nicht gesichert werden konnte) oder das Ende von ctx.
//
// Nach dem ersten Durchgang über alle Schreiber liegt alles, was vor dem
// Aufruf geschah, auf der Platte. Die weiteren nehmen, was dabei angehängt
// wurde, aus den outboxen – bis ein Durchgang vergeht, in dem nirgends
// gesichert oder angehängt wurde (s.work zählt auch, was die Schreiber von
// sich aus tun; das Ergebnis des eigenen Anstoßes allein sähe Arbeit nicht,
// die ein Durchgang kurz davor erledigt hat).
func (s *Store) Flush(ctx context.Context) error {
	for range flushRounds {
		before := s.work.Load()
		var errs []error
		if !s.emergency {
			_, err := s.change(ctx, nil, modePrune)
			switch {
			case err == nil:
			case ctx.Err() != nil:
				return fmt.Errorf("Frist abgelaufen: %w", ctx.Err())
			default:
				errs = append(errs, fmt.Errorf("feste Daten: %w", err))
			}
		}
		for _, w := range []*writer{s.rtW, s.ssW, s.histW, s.rescueW} {
			err := s.flushWriter(ctx, w)
			switch {
			case err == nil:
			case ctx.Err() != nil:
				return fmt.Errorf("Frist abgelaufen: %w", ctx.Err())
			case errors.Is(err, errClosed):
				return err
			default:
				errs = append(errs, fmt.Errorf("%w: %w", ErrStorage, err))
			}
		}
		if len(errs) > 0 {
			return errors.Join(errs...)
		}
		if s.work.Load() == before {
			return nil
		}
	}
	return nil
}

// Close schreibt alles Ausstehende (Flush – ein geordnetes Beenden verliert
// nichts, Spec §6.2), beendet danach alle Goroutinen der Ablage und gibt
// server.lock frei; begrenzt durch ctx. Hängen Schreiber an der Platte,
// bleibt die Sperre (das Ende des Prozesses gibt sie frei) – sonst könnte
// ein zweiter Prozess anfangen, während dieser noch schreibt. Mehrfach
// rufbar; danach nimmt die Ablage keine feste Änderung mehr an (ErrDropped).
func (s *Store) Close(ctx context.Context) error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.done {
		return nil
	}
	s.done = true
	var errs []error
	s.life.RLock()
	started := s.started
	s.life.RUnlock()
	if started {
		if err := s.Flush(ctx); err != nil {
			s.log.Error("beim Beenden nicht alles gesichert", "err", err)
			errs = append(errs, err)
		}
		s.life.Lock()
		s.closed = true
		s.life.Unlock()
		s.bgCancel()
		close(s.stop)
		finished := make(chan struct{})
		go func() { s.wg.Wait(); close(finished) }()
		select {
		case <-finished:
		case <-ctx.Done():
			s.log.Error("Schreiber der Ablage hängen – server.lock bleibt bis zum Ende des Prozesses")
			return errors.Join(append(errs, fmt.Errorf("Schreiber hängen: %w", ctx.Err()))...)
		}
	}
	if s.release != nil {
		if err := s.release(); err != nil {
			errs = append(errs, err)
		}
		s.release = nil
	}
	return errors.Join(errs...)
}
