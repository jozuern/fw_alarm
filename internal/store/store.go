// Package store ist die Ablage des Servers: der Zustand im Speicher hinter
// atomaren Zeigern (Leser warten auf nichts), das Sichern auf die Platte
// mit Zurücklesen, die Sperre "ein Programm pro Datenordner", die Regeln,
// worauf der Server startet (Spec §6.1–§6.3), der eine Schreiber der festen
// Daten mit Fristen, die Halter der flüchtigen Daten und der Verlauf, der
// mit seiner Änderung reist (Spec §6.2).
//
// Wer was besitzt (Einzelheiten in durable.go, volatile.go, history.go,
// flush.go):
//   - slot (Kanal mit Platz 1): wer ihn hält, ist der eine Schreiber der
//     festen Daten – von der Kopie bis zum Umsetzen des Zeigers.
//   - rtHold/ssHold: das Umsetzen der Zeiger auf Runtime/Sessions samt
//     Versionszähler; nie während eines Zugriffs auf die Platte gehalten.
//   - hist.mu: der Verlauf im Speicher, die Warteschlange zum Anhängen und
//     die Merker je uid; nie während eines Zugriffs auf die Platte gehalten.
//   - Je Datei ein Hintergrund-Schreiber (writer), dazu das Nachsichern der
//     outbox; alle enden bei Close.
//
// Keine dieser Sperren wird gehalten, während eine andere genommen wird –
// außer slot → hist.mu (der Schreiber der festen Daten fragt den Verlauf);
// hist.mu wartet nie auf slot.
package store

import (
	"context"
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

// Was Update sonst melden kann (Spec §6.2). Die Oberfläche macht daraus
// ihre Sätze (Plan "Texts this plan fixes").
var (
	// ErrStorage: Das Sichern scheiterte – die Änderung gilt nicht.
	ErrStorage = errors.New("Sichern gescheitert, die Änderung gilt nicht")
	// ErrLate: Die Frist lief ab, während gesichert wurde. Das Sichern läuft
	// weiter; gelingt es, gilt die Änderung doch (samt Verlauf).
	ErrLate = errors.New("Frist abgelaufen während des Sicherns – die Änderung kann noch gelten")
	// ErrDropped: Die Frist lief ab, bevor die Änderung an der Reihe war –
	// nichts ist geschehen.
	ErrDropped = errors.New("Frist abgelaufen, bevor die Änderung an der Reihe war – nichts geändert")
	// ErrInvalid: Die Änderung verletzt eine Regel des Zustands (Validate) –
	// nichts gesichert.
	ErrInvalid = errors.New("Änderung verletzt eine Regel, nichts gesichert")
)

// Store hält den Zustand im Speicher und sichert ihn. Die Schnappschüsse
// hinter State, Runtime und Sessions sind unveränderlich (Abmachung, Spec
// §6.2): Wer ändern will, arbeitet auf einer Kopie (Update, UpdateRuntime,
// UpdateSessions).
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

	// Der eine Schreiber der festen Daten (durable.go): Wer etwas in slot
	// legt, hält ihn. pruneKick stößt das Nachsichern der outbox an.
	slot      chan struct{}
	pruneKick chan struct{}

	// Halter der flüchtigen Daten (volatile.go): Jede Änderung zählt die
	// Version hoch; der Schreiber merkt sich die zuletzt gesicherte (nur er
	// fasst rtSaved/ssSaved an). rescueVer zählt Änderungen an Liste oder
	// Demo-Modus, rescueSaved die zuletzt gesicherte.
	rtHold      sync.Mutex
	rtVer       uint64
	rtSaved     uint64
	ssHold      sync.Mutex
	ssVer       uint64
	ssSaved     uint64
	rescueVer   atomic.Uint64
	rescueSaved uint64

	hist history // Verlauf (history.go)

	rtW, ssW, rescueW, histW *writer       // Hintergrund-Schreiber (volatile.go)
	work                     atomic.Uint64 // zählt jedes gelungene Sichern und Anhängen (für Flush)
	selfMu                   sync.Mutex

	// Lebenslauf (flush.go): life schützt closed und das Anmelden neuer
	// Goroutinen bei wg; stop endet die Schleifen, bgCtx die Fristen der
	// Arbeit nebenher.
	life     sync.RWMutex
	closed   bool
	started  bool
	stop     chan struct{}
	bgCtx    context.Context
	bgCancel context.CancelFunc
	wg       sync.WaitGroup

	closeMu sync.Mutex
	done    bool         // Close lief schon
	release func() error // server.lock; nil = nicht angelegt oder schon freigegeben
	// gaveUp: Close hat hängende Schreiber aufgegeben – ein Sichern, das
	// danach noch landet, ruft sein after nicht mehr (UpdateThen).
	gaveUp atomic.Bool
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
