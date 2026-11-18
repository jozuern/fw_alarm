package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
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

// closeStore schließt die Ablage; die Grenze von 30 s zählt nur, wenn ein
// Schreiber hängt (dann scheitert der Test, statt ewig zu warten).
func closeStore(t testing.TB, s *store.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// openFake öffnet die Ablage im Datenordner der FaultFS mit einer gestellten
// Uhr, die der Test weiterstellen kann.
func openFake(t testing.TB, f *storetest.FaultFS) (*store.Store, *clock.Fake) {
	t.Helper()
	clk := clock.NewFake(t0)
	s, _, err := store.Open(dir, f, clk, quiet())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { closeStore(t, s) })
	return s, clk
}

// hang lässt jeden Aufruf auf name hängen, bis die gelieferte Funktion
// gerufen wird – spätestens am Ende des Tests, vor dem Schließen der Ablage
// (Aufräumen läuft rückwärts).
func hang(t testing.TB, f *storetest.FaultFS, name string) (release func()) {
	t.Helper()
	release = f.Hang(name)
	t.Cleanup(release)
	return release
}

// eventually wartet, bis cond gilt. Die Grenze von 10 s zählt nur, wenn
// der Test ohnehin scheitert – sie entscheidet nie über ein Ergebnis.
func eventually(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("wartet vergeblich: %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// flush: alles Ausstehende auf die Platte; der Test scheitert bei einem
// Fehler.
func flush(t testing.TB, s *store.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
}

// calledSince: Begann seit Aufruf Nummer n ein Aufruf op, der name berührt?
// (FaultFS hält einen Aufruf fest, bevor er an einem Hänger wartet.)
func calledSince(f *storetest.FaultFS, n int, op, name string) bool {
	for _, c := range f.Log()[n:] {
		if c.Op == op && contains(c.Names, name) {
			return true
		}
	}
	return false
}

// countSince: wie viele Aufrufe op auf name seit Aufruf Nummer n.
func countSince(f *storetest.FaultFS, n int, op, name string) int {
	k := 0
	for _, c := range f.Log()[n:] {
		if c.Op == op && contains(c.Names, name) {
			k++
		}
	}
	return k
}

// entry: ein Verlaufseintrag (key_test) mit dem Namen label zur Zeit at.
func entryAt(label string, at time.Time) model.Entry {
	return model.NewEntry(model.EvKeyTest, at, map[string]any{"by": "admin", "label": label, "ok": true})
}

func entry(label string) model.Entry { return entryAt(label, t0) }

// uidOf: die uid eines Eintrags.
func uidOf(e model.Entry) string { return e.Str("uid") }

// fileLines: die Zeilen einer Verlaufsdatei der FaultFS (ohne leere).
func fileLines(f *storetest.FaultFS, name string) []string {
	b, _ := f.Get(at(name))
	var out []string
	for line := range strings.SplitSeq(string(b), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// uidCount: wie oft die uid in history.jsonl und history.1.jsonl steht
// (als Feld "uid" einer lesbaren Zeile).
func uidCount(f *storetest.FaultFS, uid string) int {
	n := 0
	for _, name := range []string{"history.jsonl", "history.1.jsonl"} {
		for _, line := range fileLines(f, name) {
			var m map[string]any
			if json.Unmarshal([]byte(line), &m) == nil && m["uid"] == uid {
				n++
			}
		}
	}
	return n
}

// uidsOf: die uids einer Liste von Einträgen, in ihrer Reihenfolge.
func uidsOf(list []model.Entry) []string {
	out := make([]string, len(list))
	for i, e := range list {
		out[i] = uidOf(e)
	}
	return out
}

// countIn: wie oft die uid in einer Liste von Einträgen vorkommt.
func countIn(list []model.Entry, uid string) int {
	n := 0
	for _, e := range list {
		if uidOf(e) == uid {
			n++
		}
	}
	return n
}

// outboxOf: die uids der outbox eines gesicherten Dokuments (state.json oder
// runtime.json); nil, wenn es fehlt oder nicht lesbar ist.
func outboxOf(f *storetest.FaultFS, name string) []string {
	b, ok := f.Get(at(name))
	if !ok {
		return nil
	}
	switch name {
	case "state.json":
		if st, err := model.DecodeState(b); err == nil {
			return uidsOf(st.Outbox)
		}
	case "runtime.json":
		if rt, err := model.DecodeRuntime(b); err == nil {
			return uidsOf(rt.Outbox)
		}
	}
	return nil
}

// runtimeIn liest runtime.json der FaultFS streng; nil = fehlt oder nicht
// lesbar.
func runtimeIn(f *storetest.FaultFS) *model.Runtime {
	b, ok := f.Get(at("runtime.json"))
	if !ok {
		return nil
	}
	rt, err := model.DecodeRuntime(b)
	if err != nil {
		return nil
	}
	return rt
}

// sessionsIn liest sessions.json der FaultFS streng; nil = fehlt oder
// nicht lesbar.
func sessionsIn(f *storetest.FaultFS) *model.Sessions {
	b, ok := f.Get(at("sessions.json"))
	if !ok {
		return nil
	}
	ss, err := model.DecodeSessions(b)
	if err != nil {
		return nil
	}
	return ss
}

// addRecipient: eine Änderung, die einen Empfänger anlegt (erfundener Key
// und Token aus dem Namen), die Listen-Version erhöht und – wenn e nicht nil
// ist – e in die outbox legt.
func addRecipient(label string, e model.Entry) func(d *model.State) error {
	return func(d *model.State) error {
		id := d.AllocateID()
		d.Recipients = append(d.Recipients, model.Recipient{ID: id, Key: "testkey-" + strings.ToLower(label),
			Label: label, AddedAt: t0.Unix(), MuteToken: fmt.Sprintf("tok%s%05d", label, id)})
		d.Bump(0)
		if e != nil {
			d.Log(e)
		}
		return nil
	}
}

// hasLabel: steht ein Empfänger dieses Namens im Zustand?
func hasLabel(st *model.State, label string) bool {
	if st == nil {
		return false
	}
	for _, r := range st.Recipients {
		if r.Label == label {
			return true
		}
	}
	return false
}

// update: Update mit Hintergrund-Kontext; der Test scheitert bei einem Fehler.
func update(t testing.TB, s *store.Store, fn func(d *model.State) error) {
	t.Helper()
	if err := s.Update(context.Background(), fn); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

// clockAt: eine gestellte Uhr bei t0.
func clockAt() *clock.Fake { return clock.NewFake(t0) }

// runtimeGoroutines: Zahl der laufenden Goroutinen (für den Nachweis, dass
// Close alle Schreiber beendet).
func runtimeGoroutines() int { return runtime.NumGoroutine() }

// session: eine angemeldete Sitzung (erfundene Werte).
func session(user string) model.Session {
	return model.Session{User: user, Role: "admin", CSRF: "csrf-" + user, AuthStamp: "stamp", CreatedAt: t0.Unix(), LastSeen: t0.Unix()}
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
