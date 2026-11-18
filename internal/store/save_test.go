package store_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"fwalarm/internal/clock"
	"fwalarm/internal/model"
	"fwalarm/internal/store"
	"fwalarm/internal/store/storetest"
)

// Ein stiller Schreibfehler (die Platte legt andere Bytes ab) fällt beim
// Zurücklesen auf: Das Sichern scheitert, die alte Datei bleibt, die
// Temp-Datei ist weg.
func TestSaveReadBack(t *testing.T) {
	f := storetest.New()
	plant(t, f, "state.json", sample(12))
	plant(t, f, "state.prev.json", sample(11))
	s, _ := open(t, f)
	oldState, _ := f.Get(at("state.json"))
	oldPrev, _ := f.Get(at("state.prev.json"))

	f.Mangle(at("state.json.tmp"), func(b []byte) []byte { return bytes.Replace(b, []byte("Anna"), []byte("Anne"), 1) })
	if err := s.SaveState(sample3(13)); err == nil {
		t.Fatal("Sichern trotz falsch abgelegter Bytes gelungen")
	}
	if got, _ := f.Get(at("state.json")); !bytes.Equal(got, oldState) {
		t.Errorf("state.json verändert:\n%s", got)
	}
	if got, _ := f.Get(at("state.prev.json")); !bytes.Equal(got, oldPrev) {
		t.Errorf("state.prev.json verändert:\n%s", got)
	}
	if _, ok := f.Get(at("state.json.tmp")); ok {
		t.Error("Temp-Datei liegt noch da")
	}

	f.Mangle(at("state.json.tmp"), nil)
	if err := s.SaveState(sample3(13)); err != nil {
		t.Fatalf("Sichern nach der Störung: %v", err)
	}
	if !same(t, stateIn(f, "state.json"), sample3(13)) {
		t.Error("state.json nicht die neue Fassung")
	}

	// Was der strenge Leser nicht läse, wird gar nicht erst geschrieben.
	bad := sample3(14)
	bad.Recipients[1].Key = bad.Recipients[0].Key
	before := f.Calls()
	if err := s.SaveState(bad); err == nil {
		t.Error("ungültige Liste gesichert")
	}
	for _, c := range f.Log()[before:] {
		if c.Writes() {
			t.Errorf("ungültige Liste: Schreibaufruf %v", c)
		}
	}
}

// Die Temp-Datei kommt nur halb an (die Platte meldet trotzdem Erfolg):
// Sichern scheitert, state.json bleibt die alte.
func TestSaveKeepsGoodFileOnShortWrite(t *testing.T) {
	f := storetest.New()
	plant(t, f, "state.json", sample(12))
	s, _ := open(t, f)
	old, _ := f.Get(at("state.json"))

	next := sample3(13)
	f.ShortWrite(at("state.json.tmp"), len(doc(t, next))/2)
	if err := s.SaveState(next); err == nil {
		t.Fatal("Sichern trotz halber Temp-Datei gelungen")
	}
	if got, _ := f.Get(at("state.json")); !bytes.Equal(got, old) {
		t.Errorf("state.json verändert:\n%s", got)
	}
	if _, ok := f.Get(at("state.json.tmp")); ok {
		t.Error("halbe Temp-Datei liegt noch da")
	}
}

// Nach zwei Sicherungen hält state.prev.json die erste.
func TestPrevIsStateBefore(t *testing.T) {
	f := storetest.New()
	s, info := open(t, f)
	if info.Mode != store.StartFresh {
		t.Fatalf("Start: %s", mode(info.Mode))
	}
	first, second := sample(12), sample3(13)
	for _, st := range []*model.State{first, second} {
		if err := s.SaveState(st); err != nil {
			t.Fatal(err)
		}
	}
	if !same(t, stateIn(f, "state.prev.json"), first) {
		t.Errorf("state.prev.json ist nicht die erste Sicherung")
	}
	if !same(t, stateIn(f, "state.json"), second) {
		t.Errorf("state.json ist nicht die zweite Sicherung")
	}
	if f.SameFile(at("state.json"), at("state.prev.json")) {
		t.Error("state.json und state.prev.json sind dieselbe Datei")
	}
}

// Das allererste Sichern (Neuanfang) gelingt ohne Vorfassung und legt
// keine an; erst das nächste macht die erste zur Vorfassung.
func TestFirstSaveEver(t *testing.T) {
	f := storetest.New()
	s, info := open(t, f)
	if info.Mode != store.StartFresh {
		t.Fatalf("Start: %s", mode(info.Mode))
	}
	if !same(t, stateIn(f, "state.json"), model.NewState()) {
		t.Errorf("state.json nach dem Neuanfang: %s", files(f)[at("state.json")])
	}
	if _, ok := f.Get(at("state.prev.json")); ok {
		t.Error("Vorfassung nach dem allerersten Sichern")
	}
	if err := s.SaveState(sample(1)); err != nil {
		t.Fatal(err)
	}
	if !same(t, stateIn(f, "state.prev.json"), model.NewState()) {
		t.Error("das zweite Sichern hat den leeren Anfang nicht als Vorfassung")
	}
}

// Start auf der Vorfassung: Das Sichern danach lässt state.prev.json stehen,
// bis die neue state.json liegt – und auch, wenn es scheitert.
func TestSaveAfterFallback(t *testing.T) {
	f := storetest.New()
	f.Put(at("state.json"), garbage)
	plant(t, f, "state.prev.json", sample(12))
	prevBytes, _ := f.Get(at("state.prev.json"))

	s, info := open(t, f)
	if info.Mode != store.StartFallback {
		t.Fatalf("Start: %s", mode(info.Mode))
	}
	for _, c := range f.Log() {
		if c.Writes() && contains(c.Names, at("state.prev.json")) {
			t.Errorf("Vorfassung beim Start angefasst: %v", c)
		}
	}
	if got, _ := f.Get(at("state.prev.json")); !bytes.Equal(got, prevBytes) {
		t.Error("Vorfassung verändert")
	}
	rewound := stateIn(f, "state.json")
	if rewound == nil || rewound.KeysVersion != 1012 {
		t.Fatalf("state.json nach dem Rückfall: %v", rewound)
	}

	// Erst das nächste Sichern macht die zurückgesetzte Fassung zur Vorfassung.
	next := sample3(1013)
	next.NextID = 1009
	next.Commands = rewound.Commands
	if err := s.SaveState(next); err != nil {
		t.Fatal(err)
	}
	if !same(t, stateIn(f, "state.prev.json"), rewound) {
		t.Error("Vorfassung ist nicht der Stand vor dem Sichern")
	}

	// Scheitert das Sichern direkt nach dem Rückfall, bleibt die Vorfassung –
	// der nächste Start fällt wieder auf sie zurück, nie auf einen Neuanfang.
	g := storetest.New()
	g.Put(at("state.json"), garbage)
	plant(t, g, "state.prev.json", sample(12))
	g.Fail(at("state.json.tmp"), errors.New("Platte voll"))
	s2, info2 := open(t, g)
	if info2.Mode != store.StartFallback || s2.State().KeysVersion != 1012 {
		t.Fatalf("Start: %s, Version %d", mode(info2.Mode), s2.State().KeysVersion)
	}
	if got, _ := g.Get(at("state.prev.json")); !bytes.Equal(got, prevBytes) {
		t.Error("Vorfassung nach gescheitertem Sichern verändert")
	}
	if err := s2.SaveState(sample(1013)); err == nil {
		t.Error("Sichern trotz Fehler gelungen")
	}
	g.Heal()
	closeStore(t, s2)
	s3, info3 := open(t, g)
	if info3.Mode != store.StartFallback || !sameList(s3.State(), sample(12)) {
		t.Errorf("Neustart: %s, %+v", mode(info3.Mode), s3.State().Recipients)
	}
}

// Review Focus 2: Für die drei Ausgangslagen normal, Neuanfang und Rückfall
// scheitert jeder einzelne Aufruf der Dateischicht der Reihe nach (beim
// Neuanfang und beim Rückfall jeder Aufruf des Starts, der selbst sichert).
// Danach findet ein neuer Start immer einen lesbaren Zustand, nie zwei
// unlesbare Dateien, und nie einen Neuanfang, wo vorher ein Zustand lag.
// Dazu ein Rückfall, bei dem sich die kaputte state.json nicht beiseitelegen
// lässt – zwei Fehler zugleich, der Fall, für den das Sichern eine
// vorhandene state.json nur dann zur Vorfassung macht, wenn sie der eigene
// Stand ist. Jede Lage mit und ohne Hardlinks (dann sichert die Kopie die
// Vorfassung), und jede auch als Absturz: Ab dem n-ten Aufruf scheitert
// alles, kein Aufräumen läuft mehr, der Neustart sieht die liegenden Dateien.
func TestStateNeverMissing(t *testing.T) {
	walked := map[string]int{}
	for _, crash := range []bool{false, true} {
		for _, noLinks := range []bool{false, true} {
			suffix := ""
			if noLinks {
				suffix += " ohne Hardlinks"
			}
			if crash {
				suffix += ", Absturz"
			}

			// normal: state.json und Vorfassung lesbar, ein Sichern.
			before, after := sample(12), sample3(13)
			walked["normal"+suffix] = walk(t, "normal"+suffix, func(f *storetest.FaultFS) {
				plant(t, f, "state.json", before)
				plant(t, f, "state.prev.json", sample(11))
			}, func(t *testing.T, f *storetest.FaultFS) (*store.Store, func() error) {
				s, info := open(t, f)
				if info.Mode != store.StartNormal {
					t.Fatalf("Start: %s", mode(info.Mode))
				}
				return s, func() error { return s.SaveState(after) }
			}, noLinks, crash, func(t *testing.T, f *storetest.FaultFS, saved error, re *store.Store, info store.StartInfo) {
				if info.Mode != store.StartNormal {
					t.Errorf("Neustart: %s", mode(info.Mode))
				}
				got := re.State()
				if saved == nil && !same(t, got, after) || !same(t, got, before) && !same(t, got, after) {
					t.Errorf("Neustart liest Version %d (Sichern: %v)", got.KeysVersion, saved)
				}
			})

			// Neuanfang: leerer Ordner, das allererste Sichern beim Start.
			walked["Neuanfang"+suffix] = walk(t, "Neuanfang"+suffix, func(*storetest.FaultFS) {},
				nil, noLinks, crash, func(t *testing.T, f *storetest.FaultFS, saved error, re *store.Store, info store.StartInfo) {
					switch info.Mode {
					case store.StartFresh:
					case store.StartNormal:
						if !same(t, re.State(), model.NewState()) {
							t.Errorf("Neustart liest Version %d", re.State().KeysVersion)
						}
					default:
						t.Errorf("Neustart: %s (Spuren hinterlassen: %v)", mode(info.Mode), f.Names())
					}
				})

			// Rückfall: state.json kaputt, Vorfassung lesbar; Beiseitelegen,
			// Rücksprung und das Sichern ohne state.json beim Start.
			prev := sample(12)
			walked["Rückfall"+suffix] = walk(t, "Rückfall"+suffix, func(f *storetest.FaultFS) {
				f.Put(at("state.json"), garbage)
				plant(t, f, "state.prev.json", prev)
			}, nil, noLinks, crash, fallbackCheck(prev))

			// Rückfall, bei dem sich die kaputte state.json nicht beiseitelegen
			// lässt (dauerhafter Fehler) – dazu scheitert ein zweiter Aufruf.
			// Die kaputte Datei darf dann nie zur Vorfassung werden.
			walked["Rückfall, Beiseitelegen scheitert"+suffix] = walk(t, "Rückfall, Beiseitelegen scheitert"+suffix, func(f *storetest.FaultFS) {
				f.Put(at("state.json"), garbage)
				plant(t, f, "state.prev.json", prev)
				f.Fail(at(fmt.Sprintf("state.corrupt-%d.json", t0.Unix())), errors.New("nicht beiseitelegbar"))
			}, nil, noLinks, crash, fallbackCheck(prev))
		}
	}
	names := make([]string, 0, len(walked))
	single, crashes := 0, 0
	for k, n := range walked {
		names = append(names, k)
		if strings.HasSuffix(k, ", Absturz") {
			crashes += n
		} else {
			single += n
		}
	}
	sort.Strings(names)
	for _, k := range names {
		t.Logf("%s: %d Aufrufe", k, walked[k])
	}
	t.Logf("zusammen: %d einzelne Fehler, %d Absturzpunkte", single, crashes)
}

// fallbackCheck: Nach einem Rückfall findet der Neustart die Liste der
// Vorfassung (zurückgesetzt), normal oder wieder als Rückfall.
func fallbackCheck(prev *model.State) func(*testing.T, *storetest.FaultFS, error, *store.Store, store.StartInfo) {
	return func(t *testing.T, f *storetest.FaultFS, saved error, re *store.Store, info store.StartInfo) {
		if info.Mode != store.StartNormal && info.Mode != store.StartFallback {
			t.Errorf("Neustart: %s", mode(info.Mode))
		}
		got := re.State()
		if !sameList(got, prev) || got.KeysVersion < prev.KeysVersion+1000 {
			t.Errorf("Neustart: Version %d, Liste %+v", got.KeysVersion, got.Recipients)
		}
	}
}

// walk lässt für eine Ausgangslage den n-ten Aufruf scheitern, n = 1, 2, …,
// bis der Vorgang ohne den n-ten Aufruf auskommt; liefert die Zahl der
// Aufrufe des Vorgangs. Ohne act ist der Vorgang der Start selbst (Open),
// sonst das, was act nach einem Start ohne Fehler liefert.
//
// Zwei Arten: crash = false – nur der n-te Aufruf scheitert, danach läuft
// alles weiter (auch das Aufräumen); crash = true – der Prozess stürzt beim
// n-ten Aufruf ab: Ab dort scheitert jeder Aufruf, und der Neustart sieht
// die Dateien so, wie sie dann liegen (FaultFS.AfterCrash). Nach jedem
// Durchgang: mindestens eine der beiden Zustandsdateien lesbar (sofern es
// vorher eine gab), dann check auf einem neuen Start ohne Fehler.
func walk(t *testing.T, name string, setup func(*storetest.FaultFS),
	act func(*testing.T, *storetest.FaultFS) (*store.Store, func() error), noLinks, crash bool,
	check func(t *testing.T, f *storetest.FaultFS, saved error, re *store.Store, info store.StartInfo)) int {
	t.Helper()
	what := "Aufruf"
	if crash {
		what = "Absturz bei Aufruf"
	}
	for n := 1; n < 500; n++ {
		f := storetest.New()
		setup(f)
		hadState := stateIn(f, "state.json") != nil || stateIn(f, "state.prev.json") != nil
		f.NoLinks(noLinks)
		fail := f.FailCall
		if crash {
			fail = f.CrashAt
		}
		var s *store.Store
		var saved error
		var base int
		if act == nil {
			base = f.Calls()
			fail(base + n)
			var err error
			s, _, err = store.Open(dir, f, clock.NewFake(t0), quiet())
			if err != nil {
				t.Fatalf("%s, %s %d: Open: %v", name, what, n, err)
			}
		} else {
			var save func() error
			s, save = act(t, f)
			base = f.Calls()
			fail(base + n)
			saved = save()
		}
		reached := f.Calls() >= base+n
		failed := callAt(f, base+n)
		view := f // nach einem einzelnen Fehler: dieselbe Platte, Fehler zurückgenommen
		if crash {
			view = f.AfterCrash() // der abgestürzte Prozess hält nichts mehr
		} else {
			f.Heal()
		}
		if err := s.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if hadState && stateIn(view, "state.json") == nil && stateIn(view, "state.prev.json") == nil {
			t.Errorf("%s, %s %d (%v): weder state.json noch state.prev.json lesbar: %v",
				name, what, n, failed, view.Names())
		}
		re, info, err := store.Open(dir, view, clock.NewFake(t0), quiet())
		if err != nil {
			t.Fatalf("%s, %s %d: Neustart: %v", name, what, n, err)
		}
		failedBefore := t.Failed()
		check(t, view, saved, re, info)
		if !failedBefore && t.Failed() {
			t.Logf("%s, %s %d gescheitert: %v", name, what, n, failed)
		}
		if err := re.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !reached {
			return n - 1
		}
	}
	t.Fatalf("%s: endet nicht", name)
	return 0
}

// callAt liefert den Aufruf Nummer n aus dem Log.
func callAt(f *storetest.FaultFS, n int) string {
	log := f.Log()
	if n < 1 || n > len(log) {
		return "–"
	}
	return log[n-1].String()
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// sameList: dieselben Empfänger (ids, Keys, Namen, Stumm-Zustand).
func sameList(a, b *model.State) bool {
	if len(a.Recipients) != len(b.Recipients) {
		return false
	}
	for i := range a.Recipients {
		if a.Recipients[i] != b.Recipients[i] {
			return false
		}
	}
	return true
}

// Jemand bearbeitet state.json an Ort und Stelle, während der Server läuft,
// und verdirbt sie. Das nächste Sichern macht die fremde Fassung nicht zur
// Vorfassung (die alte bleibt) und ersetzt sie durch den Stand im Speicher;
// scheitert dabei ein Aufruf – auch das Umbenennen danach –, findet ein
// Neustart trotzdem einen lesbaren Zustand.
func TestForeignEditKeepsPrev(t *testing.T) {
	before, prev, after := sample(12), sample(11), sample3(13)
	setup := func(f *storetest.FaultFS) {
		plant(t, f, "state.json", before)
		plant(t, f, "state.prev.json", prev)
	}
	edit := func(t *testing.T, f *storetest.FaultFS) (*store.Store, func() error) {
		s, info := open(t, f)
		if info.Mode != store.StartNormal {
			t.Fatalf("Start: %s", mode(info.Mode))
		}
		f.Put(at("state.json"), garbage) // an Ort und Stelle: derselbe inode
		return s, func() error { return s.SaveState(after) }
	}

	f := storetest.New()
	setup(f)
	prevBytes, _ := f.Get(at("state.prev.json"))
	_, save := edit(t, f)
	if err := save(); err != nil {
		t.Fatalf("Sichern: %v", err)
	}
	if got, _ := f.Get(at("state.prev.json")); !bytes.Equal(got, prevBytes) {
		t.Errorf("Vorfassung ersetzt durch:\n%s", got)
	}
	if !same(t, stateIn(f, "state.json"), after) {
		t.Error("state.json ist nicht der Stand im Speicher")
	}
	for _, c := range f.Log() {
		if (c.Op == "Remove" || c.Op == "Link") && contains(c.Names, at("state.prev.json")) {
			t.Errorf("Vorfassung angefasst: %v", c)
		}
	}

	for _, c := range []struct{ noLinks, crash bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		name := fmt.Sprintf("fremd bearbeitet, ohne Hardlinks %v, Absturz %v", c.noLinks, c.crash)
		n := walk(t, name, setup, edit, c.noLinks, c.crash,
			func(t *testing.T, f *storetest.FaultFS, saved error, re *store.Store, info store.StartInfo) {
				got := re.State()
				switch info.Mode {
				case store.StartNormal:
					if !same(t, got, after) {
						t.Errorf("Neustart normal, aber Version %d", got.KeysVersion)
					}
				case store.StartFallback:
					if !sameList(got, prev) || got.KeysVersion != prev.KeysVersion+1000 {
						t.Errorf("Rückfall, aber Version %d", got.KeysVersion)
					}
				default:
					t.Errorf("Neustart: %s", mode(info.Mode))
				}
			})
		t.Logf("%s: %d Aufrufe", name, n)
	}
}

// Echter Ordner: ein nicht leerer Ordner unter state.json.tmp. Der Start
// gelingt (der Ordner bleibt liegen), Sichern scheitert, nach dem Entfernen
// des Ordners gelingt es wieder.
func TestTmpIsFolder(t *testing.T) {
	d := t.TempDir()
	s, info := openIn(t, d, store.OSFS())
	if info.Mode != store.StartFresh {
		t.Fatalf("Start: %s", mode(info.Mode))
	}
	if err := s.SaveState(sample(12)); err != nil {
		t.Fatal(err)
	}
	closeStore(t, s)

	tmp := filepath.Join(d, "state.json.tmp")
	if err := os.MkdirAll(filepath.Join(tmp, "inhalt"), 0o700); err != nil {
		t.Fatal(err)
	}
	s, info = openIn(t, d, store.OSFS())
	if info.Mode != store.StartNormal || !same(t, s.State(), sample(12)) {
		t.Fatalf("Start mit Ordner unter state.json.tmp: %s, Version %d", mode(info.Mode), s.State().KeysVersion)
	}
	if fi, err := os.Stat(tmp); err != nil || !fi.IsDir() {
		t.Fatalf("Ordner unter state.json.tmp nicht mehr da: %v", err)
	}
	if err := s.SaveState(sample3(13)); err == nil {
		t.Fatal("Sichern trotz Ordner unter state.json.tmp gelungen")
	}
	b, err := os.ReadFile(filepath.Join(d, "state.json"))
	if err != nil || !bytes.Equal(b, doc(t, sample(12))) {
		t.Fatalf("state.json nach gescheitertem Sichern: %v\n%s", err, b)
	}

	if err := os.RemoveAll(tmp); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveState(sample3(13)); err != nil {
		t.Fatalf("Sichern nach dem Entfernen des Ordners: %v", err)
	}
	closeStore(t, s)
	s, info = openIn(t, d, store.OSFS())
	if info.Mode != store.StartNormal || !same(t, s.State(), sample3(13)) {
		t.Errorf("Neustart: %s, Version %d", mode(info.Mode), s.State().KeysVersion)
	}
}

// Echter Ordner: der ganze Ablauf mit der Dateischicht des Systems – die
// Vorfassung als Hardlink, danach getrennte Dateien; Rechte 0600/0700
// (unter Windows nicht erzwungen, dort nicht geprüft).
func TestSaveOnDisk(t *testing.T) {
	d := filepath.Join(t.TempDir(), "state")
	s, info := openIn(t, d, store.OSFS())
	if info.Mode != store.StartFresh || info.LockUnavailable {
		t.Fatalf("Start: %s, Sperre fehlt: %v", mode(info.Mode), info.LockUnavailable)
	}
	first, second := sample(12), sample3(13)
	for _, st := range []*model.State{first, second} {
		if err := s.SaveState(st); err != nil {
			t.Fatal(err)
		}
	}
	for name, want := range map[string]*model.State{"state.json": second, "state.prev.json": first} {
		b, err := os.ReadFile(filepath.Join(d, name))
		if err != nil || !bytes.Equal(b, doc(t, want)) {
			t.Errorf("%s: %v\n%s", name, err, b)
		}
	}
	if store.OSFS().SameFile(filepath.Join(d, "state.json"), filepath.Join(d, "state.prev.json")) {
		t.Error("state.json und state.prev.json sind dieselbe Datei")
	}
	entries, err := os.ReadDir(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("Temp-Datei übrig: %s", e.Name())
		}
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(d); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("Ordner: %v %v", fi.Mode(), err)
		}
		for _, name := range []string{"state.json", "state.prev.json", "server.lock"} {
			if fi, err := os.Stat(filepath.Join(d, name)); err != nil || fi.Mode().Perm() != 0o600 {
				t.Errorf("%s: %v %v", name, fi.Mode(), err)
			}
		}
	}
	closeStore(t, s)
	s, info = openIn(t, d, store.OSFS())
	if info.Mode != store.StartNormal || !same(t, s.State(), second) {
		t.Errorf("Neustart: %s, Version %d", mode(info.Mode), s.State().KeysVersion)
	}
}

// Die flüchtigen Dokumente und die Not-Kopie gehen denselben Weg (Temp-Datei,
// Zurücklesen, Umbenennen), ohne Vorfassung.
func TestSaveVolatileAndRescue(t *testing.T) {
	f := storetest.New()
	s, _ := open(t, f)
	rt := model.NewRuntime()
	rt.LifebeatAt = t0.Unix()
	ss := model.NewSessions()
	ss.Sessions["ab12"] = model.Session{User: "admin", Role: "admin", CSRF: "c", AuthStamp: "a", CreatedAt: 1, LastSeen: 2}
	rc := model.Rescue{Schema: model.Schema, WrittenAt: t0.Unix(), DemoKeyID: 3,
		Recipients: []model.RescueRecipient{{ID: 3, Key: "testkey-anna-01", Label: "Anna"}}}
	for name, save := range map[string]func() error{
		"runtime.json":           func() error { return s.SaveRuntime(rt) },
		"sessions.json":          func() error { return s.SaveSessions(ss) },
		"recipients.rescue.json": func() error { return s.SaveRescue(rc) },
	} {
		if err := save(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if _, ok := f.Get(at(name + ".tmp")); ok {
			t.Errorf("%s: Temp-Datei übrig", name)
		}
		if _, ok := f.Get(at(strings.TrimSuffix(name, ".json") + ".prev.json")); ok {
			t.Errorf("%s: Vorfassung angelegt", name)
		}
		f.ShortWrite(at(name+".tmp"), 10)
		if err := save(); err == nil {
			t.Errorf("%s: Sichern trotz halber Temp-Datei gelungen", name)
		}
		f.Heal()
	}
	if b, _ := f.Get(at("runtime.json")); !bytes.Equal(b, doc(t, rt)) {
		t.Errorf("runtime.json:\n%s", b)
	}
	if b, _ := f.Get(at("sessions.json")); !bytes.Equal(b, doc(t, ss)) {
		t.Errorf("sessions.json:\n%s", b)
	}
	if b, _ := f.Get(at("recipients.rescue.json")); !bytes.Equal(b, doc(t, rc)) {
		t.Errorf("recipients.rescue.json:\n%s", b)
	}
}
