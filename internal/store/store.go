// Package store ist die Ablage des Servers: der Zustand im Speicher hinter
// atomaren Zeigern (Leser warten auf nichts), das Sichern auf die Platte
// mit Zurücklesen, die Sperre "ein Programm pro Datenordner" und die
// Regeln, worauf der Server startet (Spec §6.1–§6.3).
package store

import (
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"

	"fwalarm/internal/clock"
	"fwalarm/internal/model"
)

// ErrEmergency: Notbetrieb – die Empfängerliste ist nicht lesbar, nichts
// Festes wird geschrieben (Spec §6.3).
var ErrEmergency = errors.New("Notbetrieb: die Empfängerliste ist nicht lesbar, nichts Festes wird gesichert")

// Store hält den Zustand im Speicher und sichert ihn. Die Schnappschüsse
// hinter State, Runtime und Sessions sind unveränderlich (Abmachung, Spec
// §6.2): Wer ändern will, arbeitet auf einer Kopie (Aufgabe 7: Update).
type Store struct {
	dir  string
	fsys FS
	clk  clock.Clock
	log  *slog.Logger

	state    atomic.Pointer[model.State]
	runtime  atomic.Pointer[model.Runtime]
	sessions atomic.Pointer[model.Sessions]

	emergency bool          // beim Start festgelegt, ändert sich danach nie
	rescue    *model.Rescue // Not-Kopie: nur im Notbetrieb, einmal beim Start gelesen

	// Ein Sichern je Datei zur Zeit. stateMu schützt auch stateBytes: der
	// Inhalt von state.json, wie ihn der Start gelesen oder dieser Prozess
	// zuletzt geschrieben hat (nil = keiner) – nur wenn die Datei noch genau
	// so aussieht, wird sie zur Vorfassung (keepPrev).
	stateMu    sync.Mutex
	stateBytes []byte
	runtimeMu  sync.Mutex
	sessionsMu sync.Mutex
	rescueMu   sync.Mutex
	saved      saveHealth

	lockMu  sync.Mutex
	release func() error // server.lock; nil = nicht angelegt oder schon freigegeben
}

// State liefert den festen Zustand – Schnappschuss, nie nil. Im Notbetrieb
// ein leerer Zustand (Listen-Version 0).
func (s *Store) State() *model.State { return s.state.Load() }

// Runtime liefert die flüchtigen Daten – Schnappschuss, nie nil.
func (s *Store) Runtime() *model.Runtime { return s.runtime.Load() }

// Sessions liefert die Sitzungen – Schnappschuss, nie nil.
func (s *Store) Sessions() *model.Sessions { return s.sessions.Load() }

// Emergency: läuft der Server im Notbetrieb?
func (s *Store) Emergency() bool { return s.emergency }
