package store

import (
	"errors"
	"io/fs"
	"time"

	"fwalarm/internal/model"
)

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
