package store_test

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"fwalarm/internal/model"
	"fwalarm/internal/store"
	"fwalarm/internal/store/storetest"
)

// schemaDoc: das Dokument v mit der Schema-Nummer n statt 1.
func schemaDoc(t testing.TB, v any, n int) []byte {
	t.Helper()
	b := doc(t, v)
	out := bytes.Replace(b, []byte(`"schema": 1,`), fmt.Appendf(nil, `"schema": %d,`, n), 1)
	if bytes.Equal(out, b) {
		t.Fatalf("schema nicht gefunden:\n%s", b)
	}
	return out
}

// Die vier Zeilen der Start-Tabelle (Spec §6.3).
func TestStartTable(t *testing.T) {
	t.Run("lesbar → normal", func(t *testing.T) {
		for _, prev := range []string{"fehlt", "lesbar", "kaputt"} {
			f := storetest.New()
			plant(t, f, "state.json", sample(12))
			switch prev {
			case "lesbar":
				plant(t, f, "state.prev.json", sample(11))
			case "kaputt":
				f.Put(at("state.prev.json"), garbage)
			}
			before := files(f)
			s, info := open(t, f)
			if info.Mode != store.StartNormal || info.Rewound || s.Emergency() {
				t.Errorf("Vorfassung %s: %s, Rücksprung %v", prev, mode(info.Mode), info.Rewound)
			}
			if !same(t, s.State(), sample(12)) {
				t.Errorf("Vorfassung %s: Version %d", prev, s.State().KeysVersion)
			}
			if after := files(f); fmt.Sprint(after) != fmt.Sprint(before) {
				t.Errorf("Vorfassung %s: Dateien verändert", prev)
			}
		}
	})

	t.Run("beide fehlen, keine Spuren → Neuanfang", func(t *testing.T) {
		f := storetest.New()
		s, info := open(t, f)
		if info.Mode != store.StartFresh || s.Emergency() || info.Rewound {
			t.Fatalf("Start: %s", mode(info.Mode))
		}
		if !same(t, s.State(), model.NewState()) || s.State().KeysVersion != 0 {
			t.Errorf("Zustand: %+v", s.State())
		}
		if !same(t, stateIn(f, "state.json"), model.NewState()) {
			t.Error("leerer Zustand nicht gesichert")
		}
	})

	for _, broken := range []string{"kaputt", "fehlt", "verletzt eine Regel"} {
		t.Run("state.json "+broken+", Vorfassung lesbar → Rückfall", func(t *testing.T) {
			f := storetest.New()
			prev := sample(12)
			plant(t, f, "state.prev.json", prev)
			prevBytes, _ := f.Get(at("state.prev.json"))
			var bad []byte
			switch broken {
			case "kaputt":
				bad = garbage
			case "verletzt eine Regel":
				st := sample(13)
				st.Recipients[1].MuteVolume = 11 // strenger Leser: Validate scheitert
				bad = doc(t, st)
			}
			if bad != nil {
				f.Put(at("state.json"), bad)
			}
			s, info := open(t, f)
			if info.Mode != store.StartFallback || !info.Rewound || info.RewindSource != model.RewindPrev || s.Emergency() {
				t.Fatalf("Start: %s, Rücksprung %v %q", mode(info.Mode), info.Rewound, info.RewindSource)
			}
			corrupt := at(fmt.Sprintf("state.corrupt-%d.json", t0.Unix()))
			got, ok := f.Get(corrupt)
			if bad != nil && (!ok || !bytes.Equal(got, bad)) {
				t.Errorf("unlesbare Datei nicht beiseitegelegt: %v", f.Names())
			}
			if bad == nil && ok {
				t.Error("fehlende Datei beiseitegelegt")
			}
			st := s.State()
			if st.KeysVersion != prev.KeysVersion+1000 || st.NextID != prev.NextID+1000 ||
				st.Commands.NextID != prev.Commands.NextID+1000 || !sameList(st, prev) {
				t.Errorf("Rücksprung: Version %d, next_id %d, Befehl %d", st.KeysVersion, st.NextID, st.Commands.NextID)
			}
			if c := st.Commands.Current; c == nil || c.State != model.CommandCancelled || c.CancelReason != model.RewindCancelReason {
				t.Errorf("offener Befehl: %+v", c)
			}
			if n := len(st.Outbox); n != 2 || st.Outbox[1].Str("event") != model.EvStateRewound || st.Outbox[1].Str("source") != "prev" {
				t.Errorf("Outbox: %v", st.Outbox)
			}
			if !same(t, stateIn(f, "state.json"), st) {
				t.Error("zurückgesetzter Zustand nicht gesichert")
			}
			if b, _ := f.Get(at("state.prev.json")); !bytes.Equal(b, prevBytes) {
				t.Error("Vorfassung verändert")
			}
		})
	}

	type row struct {
		name  string
		setup func(f *storetest.FaultFS)
	}
	for _, r := range []row{
		{"state.json kaputt, Vorfassung fehlt", func(f *storetest.FaultFS) { f.Put(at("state.json"), garbage) }},
		{"beide kaputt", func(f *storetest.FaultFS) {
			f.Put(at("state.json"), garbage)
			f.Put(at("state.prev.json"), []byte(`{"schema": 1}`))
		}},
		{"state.json Schema neuer, Vorfassung fehlt", func(f *storetest.FaultFS) { f.Put(at("state.json"), schemaDoc(t, sample(12), 2)) }},
		{"beide fehlen, aber Spuren", func(f *storetest.FaultFS) { f.Put(at("history.jsonl"), []byte(`{"event":"key_added"}`+"\n")) }},
		{"state.json ist ein Ordner, Vorfassung kaputt", func(f *storetest.FaultFS) {
			f.PutDir(at("state.json"))
			f.Put(at("state.json", "x"), []byte("x"))
			f.Put(at("state.prev.json"), garbage)
		}},
		{"beide kaputt und dieselbe Datei", func(f *storetest.FaultFS) {
			f.Put(at("state.json"), garbage)
			if err := f.Link(at("state.json"), at("state.prev.json")); err != nil {
				panic(err)
			}
		}},
	} {
		t.Run(r.name+" → Notbetrieb", func(t *testing.T) {
			f := storetest.New()
			r.setup(f)
			before := files(f)
			s, info := open(t, f)
			if info.Mode != store.StartEmergency || !s.Emergency() || info.Rewound {
				t.Fatalf("Start: %s", mode(info.Mode))
			}
			if st := s.State(); st == nil || !same(t, st, model.NewState()) {
				t.Errorf("Zustand im Notbetrieb: %+v", st)
			}
			if after := files(f); fmt.Sprint(after) != fmt.Sprint(before) {
				t.Errorf("Dateien verändert:\nvorher %v\nnachher %v", before, after)
			}
		})
	}
}

// Fehlen beide Zustandsdateien, ist es nur dann ein Neuanfang, wenn nichts
// auf frühere Nutzung deutet – sonst Notbetrieb.
func TestFreshOnlyWithoutTraces(t *testing.T) {
	for _, trace := range []string{"history.jsonl", "history.1.jsonl", "recipients.rescue.json",
		"runtime.json", "sessions.json", "state.corrupt-1763373600.json", "state.schema-0.json",
		"runtime.corrupt-1763373600.json", "sessions.corrupt-1763373600.json"} {
		f := storetest.New()
		f.Put(at(trace), []byte("{}"))
		s, info := open(t, f)
		if info.Mode != store.StartEmergency || !s.Emergency() {
			t.Errorf("Spur %s: %s", trace, mode(info.Mode))
		}
		if _, ok := f.Get(at("state.json")); ok {
			t.Errorf("Spur %s: state.json angelegt", trace)
		}
	}
	for _, noTrace := range []string{"config.last-good.toml", "server.lock", "state.json.tmp", "selftest.tmp"} {
		f := storetest.New()
		f.Put(at(noTrace), []byte("x"))
		_, info := open(t, f)
		if info.Mode != store.StartFresh {
			t.Errorf("%s ist keine Spur: %s", noTrace, mode(info.Mode))
		}
	}
	// Lässt sich der Ordner nicht durchsehen, ist ein Neuanfang nicht sicher.
	f := storetest.New()
	f.PutDir(dir)
	f.Fail(dir, errors.New("Lesefehler"))
	_, info := open(t, f)
	if info.Mode != store.StartEmergency {
		t.Errorf("Ordner nicht lesbar: %s", mode(info.Mode))
	}
}

// Sind state.json und Vorfassung dieselbe Datei (ein Sichern starb zwischen
// Verweis und Umbenennen), wird die Vorfassung durch eine Kopie ersetzt –
// ein Bearbeiten an Ort und Stelle beschädigt dann nicht beide.
func TestSameFileRepaired(t *testing.T) {
	f := storetest.New()
	plant(t, f, "state.json", sample(12))
	if err := f.Link(at("state.json"), at("state.prev.json")); err != nil {
		t.Fatal(err)
	}
	s, info := open(t, f)
	if info.Mode != store.StartNormal || !same(t, s.State(), sample(12)) {
		t.Fatalf("Start: %s", mode(info.Mode))
	}
	if f.SameFile(at("state.json"), at("state.prev.json")) {
		t.Fatal("noch dieselbe Datei")
	}
	if !same(t, stateIn(f, "state.prev.json"), sample(12)) {
		t.Error("Vorfassung ist keine Kopie von state.json")
	}
	f.Put(at("state.json"), garbage) // Bearbeiten an Ort und Stelle
	if !same(t, stateIn(f, "state.prev.json"), sample(12)) {
		t.Error("Bearbeiten von state.json beschädigt die Vorfassung")
	}
}

// Ein neueres Schema in state.json heißt: dieses Programm ist zu alt –
// Notbetrieb, was auch immer state.prev.json hält. Ein älteres Programm
// fällt nie hinter Daten zurück, die ein neueres geschrieben hat; die
// Not-Kopie hält den Alarm am Laufen, nichts wird geschrieben.
func TestSchemaNewerIsEmergency(t *testing.T) {
	t.Run("neueres Schema, Vorfassung lesbar", func(t *testing.T) {
		f := storetest.New()
		newer := schemaDoc(t, sample3(13), 2)
		f.Put(at("state.json"), newer)
		plant(t, f, "state.prev.json", sample(12))
		prevBytes, _ := f.Get(at("state.prev.json"))
		s, info := open(t, f)
		if info.Mode != store.StartEmergency || !s.Emergency() || info.Rewound {
			t.Fatalf("Start: %s, Rücksprung %v", mode(info.Mode), info.Rewound)
		}
		if !same(t, s.State(), model.NewState()) {
			t.Errorf("bedient wird nicht der leere Notbetriebs-Zustand: Version %d, %d Empfänger",
				s.State().KeysVersion, len(s.State().Recipients))
		}
		if got, _ := f.Get(at("state.json")); !bytes.Equal(got, newer) {
			t.Error("state.json verändert")
		}
		if got, _ := f.Get(at("state.prev.json")); !bytes.Equal(got, prevBytes) {
			t.Error("state.prev.json verändert")
		}
		for _, c := range f.Log() {
			for _, name := range c.Names {
				if c.Writes() && durable(name) {
					t.Errorf("Schreibaufruf auf feste Daten: %v", c)
				}
			}
		}
		if err := s.SaveState(sample(14)); !errors.Is(err, store.ErrEmergency) {
			t.Errorf("Sichern: %v", err)
		}
	})

	for _, c := range []struct {
		name  string
		setup func(*storetest.FaultFS)
	}{
		{"neueres Schema, Vorfassung fehlt", func(f *storetest.FaultFS) { f.Put(at("state.json"), schemaDoc(t, sample(12), 2)) }},
		{"neueres Schema, Vorfassung kaputt", func(f *storetest.FaultFS) {
			f.Put(at("state.json"), schemaDoc(t, sample(12), 2))
			f.Put(at("state.prev.json"), garbage)
		}},
		{"state.json fehlt, Vorfassung neuer", func(f *storetest.FaultFS) { f.Put(at("state.prev.json"), schemaDoc(t, sample(12), 2)) }},
	} {
		f := storetest.New()
		c.setup(f)
		before := files(f)
		s, info := open(t, f)
		if info.Mode != store.StartEmergency || !s.Emergency() {
			t.Errorf("%s: %s", c.name, mode(info.Mode))
		}
		if after := files(f); fmt.Sprint(after) != fmt.Sprint(before) {
			t.Errorf("%s: Dateien verändert", c.name)
		}
	}
}

// Ein älteres Schema wird angehoben, nachdem das Original einmal als
// state.schema-<n>.json abgelegt ist; ohne Umstellung ist es unlesbar.
func TestMigrationKeepsOriginal(t *testing.T) {
	lifted := 0
	restore := store.SetMigration("state", 0, func(b []byte) ([]byte, error) {
		lifted++
		return bytes.Replace(b, []byte(`"schema": 0,`), []byte(`"schema": 1,`), 1), nil
	})
	defer restore()

	f := storetest.New()
	orig := schemaDoc(t, sample(12), 0)
	f.Put(at("state.json"), orig)
	s, info := open(t, f)
	if info.Mode != store.StartNormal || info.Rewound || !same(t, s.State(), sample(12)) || lifted != 1 {
		t.Fatalf("Start: %s, angehoben %d-mal", mode(info.Mode), lifted)
	}
	if got, _ := f.Get(at("state.schema-0.json")); !bytes.Equal(got, orig) {
		t.Errorf("Original nicht abgelegt: %v", f.Names())
	}
	if !same(t, stateIn(f, "state.json"), sample(12)) {
		t.Error("angehobener Zustand nicht gesichert")
	}
	if got, _ := f.Get(at("state.prev.json")); !bytes.Equal(got, orig) {
		t.Error("Vorfassung ist nicht das Original")
	}

	// Einmal: ein schon abgelegtes Original wird nie überschrieben.
	g := storetest.New()
	g.Put(at("state.json"), orig)
	g.Put(at("state.schema-0.json"), []byte("das erste Original"))
	open(t, g)
	if got, _ := g.Get(at("state.schema-0.json")); string(got) != "das erste Original" {
		t.Errorf("abgelegtes Original überschrieben: %s", got)
	}

	// Lässt sich das Original nicht ablegen, wird state.json nicht ersetzt.
	h := storetest.New()
	h.Put(at("state.json"), orig)
	h.Fail(at("state.schema-0.json"), errors.New("Platte voll"))
	s, info = open(t, h)
	if info.Mode != store.StartNormal || !same(t, s.State(), sample(12)) {
		t.Errorf("Start ohne abgelegtes Original: %s", mode(info.Mode))
	}
	if got, _ := h.Get(at("state.json")); !bytes.Equal(got, orig) {
		t.Error("state.json ersetzt, obwohl das Original nicht abgelegt ist")
	}

	// Ohne Umstellung: älteres Schema = unlesbar.
	restore()
	k := storetest.New()
	k.Put(at("state.json"), orig)
	if _, info := open(t, k); info.Mode != store.StartEmergency {
		t.Errorf("ohne Umstellung, ohne Vorfassung: %s", mode(info.Mode))
	}
	k = storetest.New()
	k.Put(at("state.json"), orig)
	plant(t, k, "state.prev.json", sample(11))
	if _, info := open(t, k); info.Mode != store.StartFallback {
		t.Errorf("ohne Umstellung, Vorfassung lesbar: %s", mode(info.Mode))
	}

	// Flüchtige Daten: mit Umstellung angehoben, ohne verworfen.
	rt := model.NewRuntime()
	rt.LifebeatAt = t0.Unix() - 30
	m := storetest.New()
	plant(t, m, "state.json", sample(12))
	m.Put(at("runtime.json"), schemaDoc(t, rt, 0))
	if s, info := open(t, m); !info.RuntimeDiscarded || s.Runtime().LifebeatAt != 0 {
		t.Errorf("älteres runtime.json ohne Umstellung: verworfen %v", info.RuntimeDiscarded)
	}
	defer store.SetMigration("runtime", 0, func(b []byte) ([]byte, error) {
		return bytes.Replace(b, []byte(`"schema": 0,`), []byte(`"schema": 1,`), 1), nil
	})()
	m = storetest.New()
	plant(t, m, "state.json", sample(12))
	m.Put(at("runtime.json"), schemaDoc(t, rt, 0))
	if s, info := open(t, m); info.RuntimeDiscarded || s.Runtime().LifebeatAt != rt.LifebeatAt {
		t.Errorf("älteres runtime.json mit Umstellung: verworfen %v", info.RuntimeDiscarded)
	}
}

// Nach einem Start im Notbetrieb schreibt die Ablage nichts Festes – kein
// Beiseitelegen, kein Aufräumen, kein Sichern, auch nicht auf Verlangen.
func TestEmergencyWritesNothing(t *testing.T) {
	f := storetest.New()
	f.Put(at("state.json"), garbage)
	f.Put(at("state.prev.json"), []byte(`{"schema": 1}`))
	f.Put(at("state.json.tmp"), []byte("Rest eines Sicherns"))
	f.Put(at("recipients.rescue.json"), doc(t, model.Rescue{Schema: 1, Recipients: []model.RescueRecipient{
		{ID: 3, Key: "testkey-anna-01", Label: "Anna"}}}))
	f.Put(at("recipients.rescue.json.tmp"), []byte("Rest"))
	before := files(f)

	s, info := open(t, f)
	if info.Mode != store.StartEmergency {
		t.Fatalf("Start: %s", mode(info.Mode))
	}
	if err := s.SaveState(sample(12)); !errors.Is(err, store.ErrEmergency) {
		t.Errorf("Sichern im Notbetrieb: %v", err)
	}
	if err := s.SaveRescue(model.Rescue{Schema: 1}); !errors.Is(err, store.ErrEmergency) {
		t.Errorf("Not-Kopie im Notbetrieb: %v", err)
	}
	for _, c := range f.Log() {
		if !c.Writes() {
			continue
		}
		for _, name := range c.Names {
			if durable(name) {
				t.Errorf("Schreibaufruf auf feste Daten: %v", c)
			}
		}
	}
	if after := files(f); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("Dateien verändert:\nvorher %v\nnachher %v", before, after)
	}
}

// Unlesbare flüchtige Daten: beiseitelegen, leer anfangen, Merker im
// StartInfo; das Lebenszeichen kommt aus runtime.json.
func TestRuntimeDiscarded(t *testing.T) {
	f := storetest.New()
	plant(t, f, "state.json", sample(12))
	f.Put(at("runtime.json"), garbage)
	f.Put(at("sessions.json"), []byte(`{"schema": 1, "sessions": {"ab": {"user": "admin"}}}`))
	s, info := open(t, f)
	if info.Mode != store.StartNormal || !info.RuntimeDiscarded || !info.SessionsDiscarded {
		t.Fatalf("Start: %s, %+v", mode(info.Mode), info)
	}
	if !info.LastLifebeat.IsZero() {
		t.Errorf("Lebenszeichen aus verworfenen Daten: %v", info.LastLifebeat)
	}
	for _, name := range []string{"runtime", "sessions"} {
		if _, ok := f.Get(at(name + ".json")); ok {
			t.Errorf("%s.json nicht beiseitegelegt", name)
		}
		if _, ok := f.Get(at(fmt.Sprintf("%s.corrupt-%d.json", name, t0.Unix()))); !ok {
			t.Errorf("%s.corrupt-…: %v", name, f.Names())
		}
	}
	if !bytes.Equal(doc(t, s.Runtime()), doc(t, model.NewRuntime())) || len(s.Sessions().Sessions) != 0 {
		t.Errorf("nicht leer angefangen: %+v %+v", s.Runtime(), s.Sessions())
	}

	// Lesbare flüchtige Daten: übernommen, Lebenszeichen bekannt.
	rt := model.NewRuntime()
	rt.LifebeatAt = t0.Unix() - 42
	rt.Box = &model.BoxStatus{SeenAt: t0.Unix() - 50, Fields: model.Fields{{Name: "device_id", Value: "fw-box-1"}}}
	ss := model.NewSessions()
	ss.Sessions["ab"] = model.Session{User: "admin", Role: "admin", CSRF: "c", AuthStamp: "a"}
	g := storetest.New()
	plant(t, g, "state.json", sample(12))
	plant(t, g, "runtime.json", rt)
	plant(t, g, "sessions.json", ss)
	s, info = open(t, g)
	if info.RuntimeDiscarded || info.SessionsDiscarded || !info.LastLifebeat.Equal(time.Unix(rt.LifebeatAt, 0)) {
		t.Errorf("lesbare flüchtige Daten: %+v", info)
	}
	if !bytes.Equal(doc(t, s.Runtime()), doc(t, rt)) || !bytes.Equal(doc(t, s.Sessions()), doc(t, ss)) {
		t.Error("flüchtige Daten nicht übernommen")
	}

	// Fehlende flüchtige Daten: leer, kein Merker, Lebenszeichen unbekannt.
	h := storetest.New()
	plant(t, h, "state.json", sample(12))
	_, info = open(t, h)
	if info.RuntimeDiscarded || info.SessionsDiscarded || !info.LastLifebeat.IsZero() {
		t.Errorf("fehlende flüchtige Daten: %+v", info)
	}

	// Auch im Notbetrieb werden flüchtige Daten gelesen (Anmelden geht weiter).
	k := storetest.New()
	k.Put(at("state.json"), garbage)
	plant(t, k, "sessions.json", ss)
	s, info = open(t, k)
	if info.Mode != store.StartEmergency || len(s.Sessions().Sessions) != 1 {
		t.Errorf("Notbetrieb: %s, %d Sitzungen", mode(info.Mode), len(s.Sessions().Sessions))
	}
}

// Reste eines Sicherns beim Start: eine Datei wird entfernt, ein Ordner
// bleibt liegen.
func TestLeftoverTmp(t *testing.T) {
	f := storetest.New()
	plant(t, f, "state.json", sample(12))
	for _, name := range []string{"state.json.tmp", "runtime.json.tmp", "sessions.json.tmp", "recipients.rescue.json.tmp", "selftest.tmp"} {
		f.Put(at(name), []byte("Rest"))
	}
	open(t, f)
	for _, name := range f.Names() {
		if strings.HasSuffix(name, ".tmp") {
			t.Errorf("Rest nicht entfernt: %s", name)
		}
	}

	g := storetest.New()
	plant(t, g, "state.json", sample(12))
	g.PutDir(at("runtime.json.tmp"))
	g.Put(at("runtime.json.tmp", "x"), []byte("x"))
	open(t, g)
	if !g.IsDir(at("runtime.json.tmp")) {
		t.Error("Ordner unter runtime.json.tmp entfernt")
	}
}

// Review Focus 5: Der Datenordner ist nicht beschreibbar – nicht einmal die
// Sperre lässt sich anlegen. Der Start gelingt trotzdem (LockUnavailable)
// und bedient, was er lesen konnte.
func TestOpenOnUnwritableDir(t *testing.T) {
	denied := &fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission}
	rt := model.NewRuntime()
	rt.LifebeatAt = t0.Unix() - 20

	f := storetest.New()
	plant(t, f, "state.json", sample(12))
	plant(t, f, "state.prev.json", sample(11))
	plant(t, f, "runtime.json", rt)
	f.Put(at("state.json.tmp"), []byte("Rest"))
	before := files(f)
	f.ReadOnly(denied)
	s, info := open(t, f)
	if !info.LockUnavailable || info.Mode != store.StartNormal || s.Emergency() {
		t.Fatalf("Start: %s, %+v", mode(info.Mode), info)
	}
	if !same(t, s.State(), sample(12)) || s.Runtime().LifebeatAt != rt.LifebeatAt {
		t.Error("gelesener Zustand nicht bedient")
	}
	if err := s.SaveState(sample3(13)); err == nil {
		t.Error("Sichern in einem nicht beschreibbaren Ordner gelungen")
	}
	if after := files(f); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Error("Dateien verändert")
	}

	// Auch der Rückfall bedient die Vorfassung aus dem Speicher.
	g := storetest.New()
	g.Put(at("state.json"), garbage)
	plant(t, g, "state.prev.json", sample(12))
	g.ReadOnly(denied)
	s, info = open(t, g)
	if !info.LockUnavailable || info.Mode != store.StartFallback || !sameList(s.State(), sample(12)) || s.State().KeysVersion != 1012 {
		t.Errorf("Rückfall ohne Schreibrecht: %s, Version %d", mode(info.Mode), s.State().KeysVersion)
	}
}

// CheckState verlangt eine lesbare state.json – die Vorfassung zählt nicht.
func TestCheckState(t *testing.T) {
	f := storetest.New()
	plant(t, f, "state.json", sample(12))
	st, err := store.CheckState(dir, f)
	if err != nil || !same(t, st, sample(12)) {
		t.Errorf("lesbar: %v", err)
	}
	for _, c := range []struct {
		name  string
		setup func(*storetest.FaultFS)
	}{
		{"fehlt", func(*storetest.FaultFS) {}},
		{"nur die Vorfassung lesbar", func(f *storetest.FaultFS) {
			f.Put(at("state.json"), garbage)
			plant(t, f, "state.prev.json", sample(12))
		}},
		{"Schema neuer", func(f *storetest.FaultFS) { f.Put(at("state.json"), schemaDoc(t, sample(12), 2)) }},
	} {
		f := storetest.New()
		c.setup(f)
		st, err := store.CheckState(dir, f)
		if err == nil || st != nil {
			t.Errorf("%s: kein Fehler", c.name)
		} else if !strings.Contains(err.Error(), "state.json") {
			t.Errorf("%s: Fehler nennt die Datei nicht: %v", c.name, err)
		}
		for _, call := range f.Log() {
			if call.Writes() {
				t.Errorf("%s: CheckState schreibt: %v", c.name, call)
			}
		}
	}
}
