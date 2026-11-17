// Package bg führt Arbeit neben den Anfragen aus (Versand je Empfänger,
// Prüf-Läufe, Arbeit nach der Antwort): Ein Laufzeitfehler darin beendet nur
// diese Arbeit, nie das Programm, und beim Herunterfahren wird auf alles
// gewartet, was noch läuft.
package bg

import (
	"fmt"
	"log/slog"
	"runtime"
	"runtime/debug"
	"sync"
	"time"
)

// Group hält die laufende Hintergrundarbeit zusammen.
type Group struct {
	log *slog.Logger

	mu      sync.Mutex
	running int
	idle    chan struct{} // geschlossen, sobald running auf 0 fällt
}

// New liefert eine leere Gruppe; log nil = das Standard-Journal.
func New(log *slog.Logger) *Group {
	if log == nil {
		log = slog.Default()
	}
	idle := make(chan struct{})
	close(idle)
	return &Group{log: log, idle: idle}
}

// Go startet fn in einer eigenen Goroutine. Ein Laufzeitfehler darin wird
// abgefangen und ohne Anfragedaten ins Journal geschrieben: Name der Arbeit,
// bei Fehlern der Sprache deren Text (Index, nil-Zeiger …), sonst nur die
// Art des Werts – ein selbst ausgelöster Fehler kann Daten der Anfrage
// tragen –, dazu der Stapel (er zeigt nur Adressen, keine Inhalte).
func (g *Group) Go(name string, fn func()) {
	g.mu.Lock()
	if g.running == 0 {
		g.idle = make(chan struct{})
	}
	g.running++
	g.mu.Unlock()

	go func() {
		defer g.done()
		defer func() {
			r := recover()
			if r == nil {
				return
			}
			kind := fmt.Sprintf("%T", r)
			if rerr, ok := r.(runtime.Error); ok {
				kind = rerr.Error()
			}
			g.log.Error("Laufzeitfehler in Hintergrundarbeit abgefangen",
				"arbeit", name, "art", kind, "stapel", string(debug.Stack()))
		}()
		fn()
	}()
}

func (g *Group) done() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.running--
	if g.running == 0 {
		close(g.idle)
	}
}

// Wait wartet höchstens timeout darauf, dass keine Arbeit mehr läuft.
// true = alles fertig.
func (g *Group) Wait(timeout time.Duration) bool {
	g.mu.Lock()
	idle := g.idle
	g.mu.Unlock()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-idle:
		return true
	default:
	}
	select {
	case <-idle:
		return true
	case <-timer.C:
		return false
	}
}
