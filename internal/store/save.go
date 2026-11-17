package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sync/atomic"

	"fwalarm/internal/model"
)

// Feste Namen im Datenordner (Plan "File formats").
const (
	fileState    = "state.json"
	filePrev     = "state.prev.json"
	fileRuntime  = "runtime.json"
	fileSessions = "sessions.json"
	fileRescue   = "recipients.rescue.json"
	fileHistory  = "history.jsonl"
	fileHistory1 = "history.1.jsonl"
	fileLock     = "server.lock"
	fileSelfTest = "selftest.tmp"
	tmpSuffix    = ".tmp"
)

// errMismatch: Das Zurückgelesene ist nicht, was geschrieben werden sollte.
var errMismatch = errors.New("zurückgelesen anders als geschrieben")

// saveHealth: Ergebnis des letzten Sicherns je Dokument (true = gelungen
// oder noch keins gescheitert). Aufgabe 7 meldet es als SaveHealth; so
// zählt auch ein gescheitertes Sichern beim Start (Neuanfang, Rückfall).
type saveHealth struct {
	durable, volatile, sessions, rescue atomic.Bool
}

// marshalDoc: die Form eines Dokuments auf der Platte – eingerückt (für
// Menschen lesbar, etwa bei der Fehlersuche), mit Zeilenende.
func marshalDoc(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Leser je Dokument für das Zurücklesen: streng lesen (beim Zustand samt
// Validate) und wieder schreiben. Die Not-Kopie wird – wie im Notbetrieb –
// nachsichtig gelesen; was dabei wegfiele, fällt beim Vergleich auf.
func rereadState(b []byte) ([]byte, error) {
	d, err := model.DecodeState(b)
	if err != nil {
		return nil, err
	}
	return marshalDoc(d)
}

func rereadRuntime(b []byte) ([]byte, error) {
	d, err := model.DecodeRuntime(b)
	if err != nil {
		return nil, err
	}
	return marshalDoc(d)
}

func rereadSessions(b []byte) ([]byte, error) {
	d, err := model.DecodeSessions(b)
	if err != nil {
		return nil, err
	}
	return marshalDoc(d)
}

func rereadRescue(b []byte) ([]byte, error) { return marshalDoc(model.DecodeRescueLenient(b)) }

// canonical bringt ein Dokument in die Form, die sein Leser genau so
// zurückliest: schreiben, lesen, noch einmal schreiben. Was der strenge
// Leser nicht läse – auch ein Zustand, der eine Regel verletzt –, wird so
// gar nicht erst geschrieben.
func canonical(v any, reread func([]byte) ([]byte, error)) ([]byte, error) {
	b, err := marshalDoc(v)
	if err != nil {
		return nil, err
	}
	return reread(b)
}

// saveState sichert den festen Zustand als state.json (writeDoc), samt
// Vorfassung (keepPrev). Im Notbetrieb nie: ErrEmergency, ohne einen
// Aufruf der Dateischicht. Ein Sichern von state.json zur Zeit.
func (s *Store) saveState(st *model.State) error {
	if s.emergency {
		return ErrEmergency
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	data, err := canonical(st, rereadState)
	if err == nil {
		err = s.writeDoc(fileState, data, rereadState)
	}
	s.saved.durable.Store(err == nil)
	return err
}

// saveRuntime sichert die flüchtigen Daten als runtime.json – auch im
// Notbetrieb (sie sind nicht "fest").
func (s *Store) saveRuntime(r *model.Runtime) error {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	data, err := canonical(r, rereadRuntime)
	if err == nil {
		err = s.writeDoc(fileRuntime, data, rereadRuntime)
	}
	s.saved.volatile.Store(err == nil)
	return err
}

// saveSessions sichert die Sitzungen als sessions.json – auch im
// Notbetrieb (Anmelden geht weiter).
func (s *Store) saveSessions(x *model.Sessions) error {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	data, err := canonical(x, rereadSessions)
	if err == nil {
		err = s.writeDoc(fileSessions, data, rereadSessions)
	}
	s.saved.sessions.Store(err == nil)
	return err
}

// saveRescue sichert die Not-Kopie. Im Notbetrieb nie (ErrEmergency): Sie
// ist dann das Einzige, was der Alarm hat.
func (s *Store) saveRescue(r model.Rescue) error {
	if s.emergency {
		return ErrEmergency
	}
	s.rescueMu.Lock()
	defer s.rescueMu.Unlock()
	data, err := canonical(r, rereadRescue)
	if err == nil {
		err = s.writeDoc(fileRescue, data, rereadRescue)
	}
	s.saved.rescue.Store(err == nil)
	return err
}

// writeDoc sichert data unter name im Datenordner (Spec §6.3):
//
//  1. data nach <name>.tmp schreiben (anlegen/kürzen, schreiben, fsync,
//     schließen), zurücklesen: die Bytes gleich, und der Leser des
//     Dokuments schreibt das Gelesene wieder genau so;
//  2. nur bei state.json: die bisherige als state.prev.json festhalten
//     (keepPrev);
//  3. <name>.tmp → <name> umbenennen (ersetzt in einem Schritt), den
//     Ordner sichern (fsync).
//
// Jeder gescheiterte Schritt heißt: gescheitert. Die Temp-Datei wird dann
// entfernt, sofern dort eine Datei liegt. Die alte Datei bleibt bis zum
// Umbenennen unberührt.
func (s *Store) writeDoc(name string, data []byte, reread func([]byte) ([]byte, error)) error {
	target := s.path(name)
	tmp := target + tmpSuffix
	if err := s.fsys.WriteFileSync(tmp, data); err != nil {
		s.dropTmp(tmp)
		return fmt.Errorf("%s schreiben: %w", name+tmpSuffix, err)
	}
	back, err := s.fsys.ReadFile(tmp)
	if err == nil && !bytes.Equal(back, data) {
		err = errMismatch
	}
	if err == nil {
		var again []byte
		if again, err = reread(back); err == nil && !bytes.Equal(again, data) {
			err = errMismatch
		}
	}
	if err != nil {
		s.dropTmp(tmp)
		return fmt.Errorf("%s zurücklesen: %w", name+tmpSuffix, err)
	}
	if name == fileState {
		if err := s.keepPrev(); err != nil {
			s.dropTmp(tmp)
			return fmt.Errorf("%s festhalten: %w", filePrev, err)
		}
	}
	if err := s.fsys.Rename(tmp, target); err != nil {
		s.dropTmp(tmp)
		return fmt.Errorf("%s umbenennen: %w", name+tmpSuffix, err)
	}
	if name == fileState {
		s.stateOnDisk = true // was dort liegt, ist eben streng zurückgelesen
	}
	if err := s.fsys.SyncDir(s.dir); err != nil {
		return fmt.Errorf("Datenordner sichern: %w", err)
	}
	return nil
}

// keepPrev hält vor dem Umbenennen die bisherige state.json als
// state.prev.json fest (Verweis auf dieselbe Datei, notfalls Kopie) – aber
// nur, wenn es sie gibt UND sie die lesbare ist, die der Start gelesen oder
// dieser Prozess geschrieben hat (stateOnDisk). Beim allerersten Sichern,
// beim Sichern nach einem Rückfall und wenn eine unlesbare state.json nicht
// beiseitegelegt werden konnte, bleibt eine vorhandene Vorfassung
// unangetastet, bis die neue state.json liegt. So fehlen state.json und
// state.prev.json nie beide, und nie sind beide unlesbar.
func (s *Store) keepPrev() error {
	if !s.stateOnDisk {
		return nil
	}
	cur, prev := s.path(fileState), s.path(filePrev)
	fi, err := s.fsys.Stat(cur)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // von außen entfernt: nichts festzuhalten, die Vorfassung bleibt
	}
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return nil // keine Datei mehr (von außen ersetzt): die Vorfassung bleibt
	}
	if err := s.fsys.Remove(prev); err != nil {
		return err
	}
	if s.fsys.Link(cur, prev) == nil {
		return nil
	}
	// Kein Verweis möglich (Dateisystem ohne Hardlinks …) – der Fehler
	// zählt nicht, die Kopie tritt an seine Stelle.
	data, err := s.fsys.ReadFile(cur)
	if err != nil {
		return err
	}
	return s.fsys.WriteFileSync(prev, data)
}

// dropTmp entfernt eine Temp-Datei, wenn dort eine Datei liegt; ein Ordner
// bleibt liegen (der Prüfstand legt einen dorthin, damit das Sichern
// scheitert). Ein Fehler hier ändert nichts – der nächste Start räumt
// Reste auf –, er steht nur im Journal.
func (s *Store) dropTmp(tmp string) {
	fi, err := s.fsys.Stat(tmp)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.log.Warn("Temp-Datei nicht prüfbar", "datei", filepath.Base(tmp), "err", err)
		}
		return
	}
	if !fi.Mode().IsRegular() {
		s.log.Warn("unter dem Temp-Namen liegt keine Datei – bleibt liegen", "datei", filepath.Base(tmp))
		return
	}
	if err := s.fsys.Remove(tmp); err != nil {
		s.log.Warn("Temp-Datei nicht entfernbar", "datei", filepath.Base(tmp), "err", err)
	}
}

// path liefert den Pfad einer Datei im Datenordner.
func (s *Store) path(name string) string { return filepath.Join(s.dir, name) }
