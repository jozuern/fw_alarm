package store_test

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fwalarm/internal/clock"
	"fwalarm/internal/model"
	"fwalarm/internal/store"
	"fwalarm/internal/store/storetest"
)

// Fester Zeitpunkt der Tests: 17.11.2026 10:00 UTC.
var t0 = time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

// dir ist der Datenordner in der FaultFS.
var dir = filepath.FromSlash("/daten")

// at liefert den Pfad einer Datei im Datenordner der FaultFS.
func at(names ...string) string { return filepath.Join(append([]string{dir}, names...)...) }

// garbage ist eine Datei, die kein Leser versteht.
var garbage = []byte("{kaputt")

func quiet() *slog.Logger { return slog.New(slog.DiscardHandler) }

// sample: ein gültiger Zustand der Listen-Version v – zwei Empfänger (Bert
// stumm) und ein wartender Befehl. Erfundene Keys und Tokens.
func sample(v int) *model.State {
	s := model.NewState()
	s.KeysVersion = v
	s.NextID = 8
	s.Recipients = []model.Recipient{
		{ID: 3, Key: "testkey-anna-01", Label: "Anna", AddedAt: t0.Unix() - 86400, MuteToken: "tokAnna0001"},
		{ID: 7, Key: "testkey-bert-02", Label: "Bert", AddedAt: t0.Unix() - 86400,
			Muted: true, MutedAt: t0.Unix() - 3600, MuteVolume: 3, MuteToken: "tokBert0002"},
	}
	s.Commands = model.Commands{NextID: 5, Current: &model.Command{ID: 4, Type: "TEST",
		State: model.CommandPending, CreatedAt: t0.Unix() - 10, CreatedBy: "admin"}}
	return s
}

// sample3: wie sample, dazu Carl – damit sich zwei Fassungen auch in der
// Liste unterscheiden.
func sample3(v int) *model.State {
	s := sample(v)
	s.NextID = 9
	s.Recipients = append(s.Recipients, model.Recipient{ID: 8, Key: "testkey-carl-03", Label: "Carl",
		AddedAt: t0.Unix(), MuteToken: "tokCarl0003"})
	return s
}

// doc: das Dokument in der Form, in der die Ablage es schreibt.
func doc(t testing.TB, v any) []byte {
	t.Helper()
	b, err := store.MarshalDoc(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func plant(t testing.TB, f *storetest.FaultFS, name string, v any) { f.Put(at(name), doc(t, v)) }

// open öffnet die Ablage auf fsys im Ordner d mit gestellter Uhr; der Test
// scheitert bei einem Fehler. Geschlossen wird am Ende des Tests (Close
// ist mehrfach rufbar).
func openIn(t testing.TB, d string, fsys store.FS) (*store.Store, store.StartInfo) {
	t.Helper()
	s, info, err := store.Open(d, fsys, clock.NewFake(t0), quiet())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s == nil {
		t.Fatal("Open: kein Store")
	}
	t.Cleanup(func() { closeStore(t, s) })
	return s, info
}

func open(t testing.TB, f *storetest.FaultFS) (*store.Store, store.StartInfo) {
	t.Helper()
	return openIn(t, dir, f)
}

func closeStore(t testing.TB, s *store.Store) {
	t.Helper()
	if err := s.Close(context.Background()); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// stateIn liest eine Zustandsdatei der FaultFS streng; nil = fehlt oder
// nicht lesbar.
func stateIn(f *storetest.FaultFS, name string) *model.State {
	b, ok := f.Get(at(name))
	if !ok {
		return nil
	}
	st, err := model.DecodeState(b)
	if err != nil {
		return nil
	}
	return st
}

// same vergleicht zwei Zustände über ihre geschriebene Form.
func same(t testing.TB, a, b *model.State) bool {
	t.Helper()
	if a == nil || b == nil {
		return a == b
	}
	return bytes.Equal(doc(t, a), doc(t, b))
}

// files: alle Dateien der FaultFS mit Inhalt (ohne server.lock).
func files(f *storetest.FaultFS) map[string]string {
	out := map[string]string{}
	for _, name := range f.Names() {
		if filepath.Base(name) == "server.lock" {
			continue
		}
		if b, ok := f.Get(name); ok {
			out[name] = string(b)
		}
	}
	return out
}

// durable: gehört der Name zu den festen Daten (Zustand, Vorfassung,
// beiseitegelegte und abgelegte Fassungen, Not-Kopie, deren Temp-Dateien)?
func durable(name string) bool {
	base := filepath.Base(name)
	return strings.HasPrefix(base, "state.") || strings.HasPrefix(base, "recipients.")
}

// mode: Startart als Text für Meldungen.
func mode(m store.StartMode) string {
	switch m {
	case store.StartNormal:
		return "normal"
	case store.StartFresh:
		return "Neuanfang"
	case store.StartFallback:
		return "Rückfall"
	case store.StartEmergency:
		return "Notbetrieb"
	}
	return "?"
}
