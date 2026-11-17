// Package store ist die Ablage des Servers: der Zustand im Speicher hinter
// atomaren Zeigern (Leser warten auf nichts), das Sichern auf die Platte
// mit Zurücklesen, die Sperre "ein Programm pro Datenordner" und die
// Regeln, worauf der Server startet (Spec §6.1–§6.3).
package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fwalarm/internal/clock"
	"fwalarm/internal/model"
)

// ErrEmergency: Notbetrieb – die Empfängerliste ist nicht lesbar, nichts
// Festes wird geschrieben (Spec §6.3).
var ErrEmergency = errors.New("Notbetrieb: die Empfängerliste ist nicht lesbar, nichts Festes wird gesichert")

// StartMode: worauf der Server gestartet ist (Start-Tabelle, Spec §6.3).
type StartMode int

const (
	StartNormal    StartMode = iota // state.json lesbar
	StartFresh                      // beide Zustandsdateien fehlen, keine Spur früherer Nutzung
	StartFallback                   // Rückfall auf state.prev.json mit Rücksprung
	StartEmergency                  // Notbetrieb
)

func (m StartMode) String() string {
	switch m {
	case StartNormal:
		return "normal"
	case StartFresh:
		return "neu"
	case StartFallback:
		return "rueckfall"
	case StartEmergency:
		return "notbetrieb"
	}
	return fmt.Sprintf("StartMode(%d)", int(m))
}

// StartInfo sagt, wie der Start verlief – für die Meldungen an den Admin
// (Aufgabe 23), die Auszeit-Regel (Aufgabe 21) und fw-alarm health.
type StartInfo struct {
	Mode                                StartMode
	Rewound                             bool
	RewindSource                        model.RewindSource
	RuntimeDiscarded, SessionsDiscarded bool
	LockUnavailable                     bool      // Sperre ließ sich nicht anlegen (Ordner nicht beschreibbar)
	LastLifebeat                        time.Time // null = unbekannt
}

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

	// Ein Sichern je Datei zur Zeit. stateMu schützt auch stateOnDisk:
	// state.json auf der Platte ist die lesbare (beim Start gelesen oder
	// von diesem Prozess geschrieben) – nur dann wird sie zur Vorfassung.
	stateMu     sync.Mutex
	stateOnDisk bool
	runtimeMu   sync.Mutex
	sessionsMu  sync.Mutex
	rescueMu    sync.Mutex
	saved       saveHealth

	lockMu  sync.Mutex
	release func() error // server.lock; nil = nicht angelegt oder schon freigegeben
}

// Open öffnet den Datenordner dir: Sperre server.lock (bis Close), dann
// die Start-Tabelle (Spec §6.3) und die flüchtigen Daten. Einziger Fehler:
// ErrLocked – ein anderer Prozess arbeitet schon in diesem Ordner. Lässt
// sich die Sperre gar nicht anlegen (Ordner nicht beschreibbar), startet
// er trotzdem (StartInfo.LockUnavailable) und bedient, was er lesen konnte.
func Open(dir string, fsys FS, clk clock.Clock, log *slog.Logger) (*Store, StartInfo, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Store{dir: dir, fsys: fsys, clk: clk, log: log}
	for _, ok := range []*atomic.Bool{&s.saved.durable, &s.saved.volatile, &s.saved.sessions, &s.saved.rescue} {
		ok.Store(true)
	}
	var info StartInfo

	if err := fsys.MkdirAll(dir); err != nil {
		log.Error("Datenordner nicht anlegbar", "err", err)
	}
	release, err := fsys.Lock(s.path(fileLock))
	switch {
	case errors.Is(err, ErrLocked):
		return nil, StartInfo{}, ErrLocked
	case err != nil:
		info.LockUnavailable = true
		log.Error("server.lock nicht anlegbar – Start trotzdem", "err", err)
	default:
		s.release = release
	}

	now := clk.Now()
	s.openState(now, &info)
	s.openVolatile(now, &info)
	log.Info("Ablage geöffnet", "start", info.Mode.String(), "ruecksprung", info.Rewound,
		"laufzeit_verworfen", info.RuntimeDiscarded, "sitzungen_verworfen", info.SessionsDiscarded,
		"sperre_fehlt", info.LockUnavailable)
	return s, info, nil
}

// Close gibt server.lock frei; mehrfach rufbar. (Aufgabe 7 wartet hier
// zuvor auf die Schreiber, begrenzt durch ctx.)
func (s *Store) Close(ctx context.Context) error {
	s.lockMu.Lock()
	defer s.lockMu.Unlock()
	if s.release == nil {
		return nil
	}
	err := s.release()
	s.release = nil
	return err
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

// docStatus: was beim Lesen einer Datei herauskam.
type docStatus int

const (
	docMissing docStatus = iota
	docOK
	docBroken // nicht lesbar: Lesefehler, kaputt, Regel verletzt, älteres Schema ohne Umstellung
	docNewer  // Schema neuer als das Programm
)

// stateRead ist eine gelesene Zustandsdatei.
type stateRead struct {
	status docStatus
	doc    *model.State
	raw    []byte
	lifted bool // aus einem älteren Schema angehoben
	from   int  // dessen Nummer
	err    error
}

func (r stateRead) describe() string {
	switch r.status {
	case docMissing:
		return "fehlt"
	case docOK:
		return "lesbar"
	}
	return r.err.Error()
}

// readState liest eine Zustandsdatei streng (samt Validate); ein älteres
// Schema wird angehoben, wenn es eine Umstellung gibt.
func (s *Store) readState(name string) stateRead {
	raw, err := s.fsys.ReadFile(s.path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return stateRead{status: docMissing}
	}
	if err != nil {
		return stateRead{status: docBroken, err: err}
	}
	doc, from, lifted, newer, err := decodeDoc(raw, model.DecodeState, stateMigrations)
	switch {
	case newer:
		return stateRead{status: docNewer, raw: raw, from: from, err: err}
	case err != nil:
		return stateRead{status: docBroken, raw: raw, err: err}
	}
	return stateRead{status: docOK, doc: doc, raw: raw, lifted: lifted, from: from}
}

// decodeDoc liest ein Dokument streng. Ein SchemaError wird zuerst
// angesehen: neuer → newer; älter → über table anheben und dann streng
// lesen (fehlt eine Stufe, ist es nicht lesbar).
func decodeDoc[T any](raw []byte, decode func([]byte) (T, error),
	table map[int]func([]byte) ([]byte, error)) (doc T, from int, lifted, newer bool, err error) {
	doc, err = decode(raw)
	var se *model.SchemaError
	if !errors.As(err, &se) {
		return doc, 0, false, false, err
	}
	if se.Found > model.Schema {
		return doc, se.Found, false, true, err
	}
	up, lerr := lift(table, raw, se.Found, model.Schema)
	if lerr != nil {
		return doc, se.Found, false, false, fmt.Errorf("%v: %w", err, lerr)
	}
	doc, err = decode(up)
	return doc, se.Found, err == nil, false, err
}

// openState wendet die Start-Tabelle an (Spec §6.3):
//
//	state.json            state.prev.json   Folge
//	lesbar                egal              normal
//	fehlt                 fehlt, keine Spur Neuanfang (leerer Zustand, gleich gesichert)
//	fehlt oder unlesbar   lesbar            Rückfall: beiseitelegen, Rücksprung, sichern
//	Schema neuer          egal              Notbetrieb
//	sonst                                   Notbetrieb
func (s *Store) openState(now time.Time, info *StartInfo) {
	cur, prev := s.readState(fileState), s.readState(filePrev)
	switch {
	case cur.status == docOK:
		info.Mode = StartNormal
	case cur.status == docNewer:
		// Dieses Programm ist zu alt. Auch eine lesbare Vorfassung hilft
		// nicht: Ein Rückfall überschriebe den neueren Stand beim nächsten
		// Sichern; der Weg zurück ist die Anleitung (state.schema-<n>.json).
		info.Mode = StartEmergency
	case prev.status == docOK && (cur.status == docMissing || cur.status == docBroken):
		info.Mode = StartFallback
	case cur.status == docMissing && prev.status == docMissing:
		info.Mode = s.freshOrEmergency()
	default:
		info.Mode = StartEmergency
	}

	if info.Mode == StartEmergency {
		s.emergency = true
		s.state.Store(model.NewState())
		s.log.Error("Notbetrieb: Empfängerliste nicht lesbar – nichts Festes wird geschrieben",
			"state.json", cur.describe(), "state.prev.json", prev.describe())
		s.readRescue()
		return
	}

	// Reste eines Sicherns der festen Daten (im Notbetrieb bleibt alles liegen).
	s.dropTmp(s.path(fileState + tmpSuffix))
	s.dropTmp(s.path(fileRescue + tmpSuffix))

	switch info.Mode {
	case StartNormal:
		s.stateOnDisk = true
		s.repairSameFile(cur.raw)
		s.state.Store(cur.doc)
		if cur.lifted {
			s.saveLifted(cur, cur.doc)
		}
	case StartFallback:
		s.log.Error("state.json nicht lesbar – Start auf der Vorfassung", "state.json", cur.describe())
		if cur.status == docBroken {
			if err := s.moveAside(fileState, "state", now); err != nil {
				s.log.Error("state.json nicht beiseitelegbar", "err", err)
			}
		}
		st := prev.doc
		st.Rewind(model.RewindPrev, now)
		info.Rewound, info.RewindSource = true, model.RewindPrev
		s.state.Store(st)
		if prev.lifted {
			s.saveLifted(prev, st)
		} else if err := s.saveState(st); err != nil {
			s.log.Error("Zustand nach dem Rückfall nicht gesichert – gilt aus dem Speicher", "err", err)
		}
	case StartFresh:
		st := model.NewState()
		s.state.Store(st)
		// Gleich sichern: Sonst machten die flüchtigen Daten, die gleich
		// entstehen, den nächsten Start zum Notbetrieb (Spur ohne Zustand).
		if err := s.saveState(st); err != nil {
			s.log.Error("leerer Zustand nicht gesichert – gilt aus dem Speicher", "err", err)
		}
	}
}

// freshOrEmergency: Fehlen beide Zustandsdateien, ist es nur ohne jede Spur
// früherer Nutzung ein Neuanfang; lässt sich der Ordner nicht durchsehen,
// ist das nicht sicher – Notbetrieb.
func (s *Store) freshOrEmergency() StartMode {
	traces, err := s.traces()
	if err != nil {
		s.log.Error("Datenordner nicht durchsehbar – kein Neuanfang", "err", err)
		return StartEmergency
	}
	if len(traces) > 0 {
		s.log.Error("Zustand fehlt, aber der Ordner wurde schon genutzt", "spuren", strings.Join(traces, ","))
		return StartEmergency
	}
	return StartFresh
}

// traces nennt die Spuren früherer Nutzung im Datenordner: Not-Kopie,
// Verlauf, flüchtige Daten, beiseitegelegte und abgelegte Fassungen. Keine
// Spur sind server.lock, config.last-good.toml und Temp-Dateien.
func (s *Store) traces() ([]string, error) {
	names, err := s.fsys.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil // kein Ordner: nichts darin
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range names {
		if isTrace(n) {
			out = append(out, n)
		}
	}
	return out, nil
}

func isTrace(name string) bool {
	switch name {
	case fileRescue, fileHistory, fileHistory1, fileRuntime, fileSessions:
		return true
	}
	for _, prefix := range []string{"state.corrupt-", "state.schema-", "runtime.corrupt-", "sessions.corrupt-"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// saveLifted sichert einen aus einem älteren Schema angehobenen Zustand –
// erst, wenn das Original einmal als state.schema-<n>.json abgelegt ist
// (sonst gälte er nur aus dem Speicher, bis ein Sichern gelingt).
func (s *Store) saveLifted(r stateRead, st *model.State) {
	if err := s.keepOriginal(r); err != nil {
		s.log.Error("Original des älteren Schemas nicht ablegbar – angehobener Zustand nicht gesichert", "schema", r.from, "err", err)
		return
	}
	if err := s.saveState(st); err != nil {
		s.log.Error("angehobener Zustand nicht gesichert – gilt aus dem Speicher", "err", err)
	}
}

// keepOriginal legt das Original eines älteren Schemas EINMAL als
// state.schema-<n>.json ab; ein schon abgelegtes wird nie überschrieben.
func (s *Store) keepOriginal(r stateRead) error {
	name := s.path(fmt.Sprintf("state.schema-%d.json", r.from))
	_, err := s.fsys.Stat(name)
	if err == nil {
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := s.fsys.WriteFileSync(name, r.raw); err != nil {
		return err
	}
	return s.fsys.SyncDir(s.dir)
}

// repairSameFile: Sind state.json und state.prev.json dieselbe Datei (ein
// Sichern starb zwischen Verweis und Umbenennen), wird die Vorfassung
// durch eine Kopie ersetzt – sonst beschädigte ein Bearbeiten an Ort und
// Stelle beide. Erst entfernen (nur der Name verschwindet), dann neu
// schreiben: Schreiben über den Verweis kürzte state.json mit.
func (s *Store) repairSameFile(raw []byte) {
	cur, prev := s.path(fileState), s.path(filePrev)
	if !s.fsys.SameFile(cur, prev) {
		return
	}
	err := s.fsys.Remove(prev)
	if err == nil {
		err = s.fsys.WriteFileSync(prev, raw)
	}
	if err == nil {
		err = s.fsys.SyncDir(s.dir)
	}
	if err != nil {
		s.log.Error("state.prev.json ist dieselbe Datei wie state.json – Kopie scheiterte", "err", err)
		return
	}
	s.log.Warn("state.prev.json war dieselbe Datei wie state.json – durch eine Kopie ersetzt")
}

// moveAside legt eine unlesbare Datei als <base>.corrupt-<Unix-Zeit>.json
// beiseite (gibt es den Namen schon, mit -2, -3 … davor) – für die
// Fehlersuche.
func (s *Store) moveAside(name, base string, now time.Time) error {
	stem := fmt.Sprintf("%s.corrupt-%d", base, now.Unix())
	for i := 1; i <= 100; i++ {
		target := stem + ".json"
		if i > 1 {
			target = fmt.Sprintf("%s-%d.json", stem, i)
		}
		_, err := s.fsys.Stat(s.path(target))
		if err == nil {
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := s.fsys.Rename(s.path(name), s.path(target)); err != nil {
			return err
		}
		s.log.Warn("unlesbare Datei beiseitegelegt", "datei", name, "nach", target)
		return s.fsys.SyncDir(s.dir)
	}
	return fmt.Errorf("kein freier Name für %s", stem)
}

// openVolatile liest runtime.json und sessions.json. Unlesbar → beiseite
// legen, leer anfangen, Merker im StartInfo; ein älteres Schema ohne
// Umstellung zählt als unlesbar. Auch im Notbetrieb (Anmelden geht weiter).
func (s *Store) openVolatile(now time.Time, info *StartInfo) {
	for _, name := range []string{fileRuntime + tmpSuffix, fileSessions + tmpSuffix, fileSelfTest} {
		s.dropTmp(s.path(name))
	}
	rt, discarded := readVolatile(s, fileRuntime, "runtime", model.DecodeRuntime, runtimeMigrations, now)
	if rt == nil {
		rt = model.NewRuntime()
	}
	info.RuntimeDiscarded = discarded
	if rt.LifebeatAt > 0 {
		info.LastLifebeat = time.Unix(rt.LifebeatAt, 0)
	}
	s.runtime.Store(rt)

	ss, discarded := readVolatile(s, fileSessions, "sessions", model.DecodeSessions, sessionsMigrations, now)
	if ss == nil {
		ss = model.NewSessions()
	}
	info.SessionsDiscarded = discarded
	s.sessions.Store(ss)
}

// readVolatile liest ein flüchtiges Dokument; nil ohne discarded = fehlt.
func readVolatile[T any](s *Store, name, base string, decode func([]byte) (*T, error),
	table map[int]func([]byte) ([]byte, error), now time.Time) (doc *T, discarded bool) {
	raw, err := s.fsys.ReadFile(s.path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false
	}
	if err == nil {
		var d *T
		if d, _, _, _, err = decodeDoc(raw, decode, table); err == nil {
			return d, false
		}
	}
	s.log.Error(name+" nicht lesbar – leer angefangen", "err", err)
	if err := s.moveAside(name, base, now); err != nil {
		s.log.Error(name+" nicht beiseitelegbar", "err", err)
	}
	return nil, true
}

// CheckState liest state.json aus dir streng (fw-alarm check-state, Ende
// der Übernahme): Nur eine lesbare state.json zählt, ein Rückfall auf die
// Vorfassung nicht. Ein älteres Schema mit Umstellung ist lesbar (der
// Start hebt es an). Schreibt nichts, nimmt keine Sperre.
func CheckState(dir string, fsys FS) (*model.State, error) {
	raw, err := fsys.ReadFile(filepath.Join(dir, fileState))
	if err != nil {
		return nil, fmt.Errorf("state.json nicht lesbar: %w", err)
	}
	st, _, _, newer, err := decodeDoc(raw, model.DecodeState, stateMigrations)
	if newer {
		return nil, fmt.Errorf("state.json hat ein neueres Schema als dieses Programm: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("state.json nicht lesbar: %w", err)
	}
	return st, nil
}
