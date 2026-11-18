package store_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"fwalarm/internal/model"
	"fwalarm/internal/store"
	"fwalarm/internal/store/storetest"
)

// Eine Flut flüchtiger Einträge bei unbeschreibbarem Verlauf verdrängt
// keinen Eintrag der festen Daten: Die Übergabe hält je Dokument nur, was
// noch in dessen outbox steht – was aus der outbox der flüchtigen Daten
// fiel (dort gemeldet), fällt auch aus der Übergabe; der frühe Eintrag des
// Zustands kommt nach der Reparatur genau einmal an.
func TestHandoverKeepsEachOutbox(t *testing.T) {
	f := storetest.New()
	s, _ := openFake(t, f)
	f.Fail(at("history.jsonl"), errors.New("nicht beschreibbar"))
	early := entry("fest, früh")
	update(t, s, func(d *model.State) error { d.Settings.MuteMaxSeconds = 60; d.Log(early); return nil })
	ctx := bounded(t)
	var newest model.Entry
	for i := range 1200 {
		newest = entry(fmt.Sprint("flüchtig ", i))
		s.Log(newest)
		if i%50 == 49 {
			// Sichert runtime.json (übergibt deren outbox); der Verlauf
			// scheitert – der Fehler ist hier gewollt.
			_ = s.Flush(ctx)
		}
	}
	if !slices.Contains(uidsOf(s.State().Outbox), uidOf(early)) {
		t.Fatal("Ausgangslage: der frühe Eintrag steht nicht mehr in der outbox des Zustands")
	}
	f.Heal()
	flush(t, s)
	if n := uidCount(f, uidOf(early)); n != 1 {
		t.Errorf("früher Eintrag des Zustands: %d-mal im Verlauf", n)
	}
	if n := uidCount(f, uidOf(newest)); n != 1 {
		t.Errorf("neuester flüchtiger Eintrag: %d-mal", n)
	}
	volatile := 0
	for _, line := range fileLines(f, "history.jsonl") {
		if e, ok := store.ParseHistoryLine([]byte(line)); ok && strings.HasPrefix(e.Str("label"), "flüchtig ") {
			volatile++
		}
	}
	if volatile != model.MaxOutbox {
		t.Errorf("%d flüchtige Zeilen – erwartet die %d, die in der outbox blieben", volatile, model.MaxOutbox)
	}
}

// Einträge ohne uid (ein Programmierfehler): Der Zustand nimmt sie so nicht
// an (Validate), die Ablage gibt ihnen beim Eintreten eine neue uid und
// schreibt eine Zeile ins Journal – keiner geht verloren, keiner fällt mit
// einem anderen zusammen, und eine alte Zeile ohne uid (PHP) zählt nicht als
// "schon angehängt".
func TestEntriesWithoutUID(t *testing.T) {
	bare := func(label string) model.Entry {
		return model.Entry{"event": model.EvKeyTest, "log_at": json.Number("1763373600"), "label": label}
	}
	labelCount := func(f *storetest.FaultFS, label string) int {
		n := 0
		for _, line := range fileLines(f, "history.jsonl") {
			if e, ok := store.ParseHistoryLine([]byte(line)); ok && e.Str("label") == label {
				if e.Str("uid") == "" {
					return -1
				}
				n++
			}
		}
		return n
	}

	f := storetest.New()
	s, j := openLogged(t, f)
	update(t, s, func(d *model.State) error { d.Log(bare("fest 1")); d.Log(bare("fest 2")); return nil })
	s.UpdateRuntime(func(d *model.Runtime) { d.Log(bare("flüchtig")) })
	s.Log(bare("Log"))
	if !s.LogAndWait(bare("LogAndWait"), time.Hour) {
		t.Error("LogAndWait ohne uid wartet nicht")
	}
	flush(t, s)
	for _, label := range []string{"fest 1", "fest 2", "flüchtig", "Log", "LogAndWait"} {
		if n := labelCount(f, label); n != 1 {
			t.Errorf("%s: %d-mal im Verlauf (-1 = ohne uid)", label, n)
		}
	}
	if n := j.count("Verlaufs-Eintrag ohne uid"); n != 5 {
		t.Errorf("Journal: %d Zeilen statt 5", n)
	}

	// Beim Start: runtime.json mit einem Eintrag ohne uid, im Verlauf eine
	// alte PHP-Zeile ohne uid.
	g := storetest.New()
	plant(t, g, "state.json", sample(12))
	rt := model.NewRuntime()
	rt.Outbox = []model.Entry{bare("vom letzten Lauf")}
	plant(t, g, "runtime.json", rt)
	g.Put(at("history.jsonl"), []byte(`{"event":"key_added","log_at":1763373000,"label":"PHP"}`+"\n"))
	re, rj := openLogged(t, g)
	flush(t, re)
	if n := labelCount(g, "vom letzten Lauf"); n != 1 {
		t.Errorf("Eintrag ohne uid aus runtime.json: %d-mal", n)
	}
	if rj.count("Verlaufs-Eintrag ohne uid") != 1 {
		t.Error("Journal: keine Zeile zum Eintrag ohne uid beim Start")
	}

	// Zeilen ohne uid (oder mit leerer) sind für den Abgleich keine Treffer.
	lines := [][]byte{[]byte(`{"event":"a"}`), []byte(`{"event":"b","uid":""}`), []byte(`{"event":"c","uid":"x"}`)}
	if got := store.UIDsIn(lines, map[string]bool{"": true, "x": true}); len(got) != 1 || !got["x"] {
		t.Errorf("UIDsIn: %v", got)
	}
}

// Liest der Verlauf history.jsonl nach einem gescheiterten Anhängen neu, sind
// die Einträge, die er dort findet, im selben Schritt angehängt: Ein
// History-Aufruf direkt nach diesem Schritt zeigt sie genau einmal.
func TestResyncShowsEntriesOnce(t *testing.T) {
	f := storetest.New()
	s, _ := openFake(t, f)
	f.FailAfter(at("history.jsonl"), "AppendSync", errors.New("fsync gescheitert"))
	e := entry("schon in der Datei")
	base := f.Calls()
	s.Log(e)
	eventually(t, "Anhängen meldet den Fehler", func() bool {
		for _, c := range f.Log()[base:] {
			if c.Op == "AppendSync" && c.Err != nil {
				return true
			}
		}
		return false
	})
	f.Heal()
	if err := s.ResyncHistory(); err != nil {
		t.Fatal(err)
	}
	if n := countIn(s.History(10, nil), uidOf(e)); n != 1 {
		t.Errorf("History direkt nach dem Neulesen: %d-mal", n)
	}
	flush(t, s)
	if n := uidCount(f, uidOf(e)); n != 1 {
		t.Errorf("Verlauf: %d-mal", n)
	}
}

// Open wartet nicht auf den Verlauf: Hängt das Anhängen an history.jsonl
// (nach einem Absturz ist die Platte verdächtig), kehrt Open zurück und
// bedient den Zustand; was in der outbox stand, zeigt History schon und
// kommt an, sobald die Platte wieder will.
func TestOpenDoesNotWaitForHistory(t *testing.T) {
	f := storetest.New()
	plant(t, f, "state.json", sample(12))
	rt := model.NewRuntime()
	e := entry("vom letzten Lauf")
	rt.Log(e)
	plant(t, f, "runtime.json", rt)
	f.Put(at("history.jsonl"), []byte(`{"event":"key_added","log_at":1763373000,"uid":"alt"}`+"\n"))
	release := f.HangOp(at("history.jsonl"), "AppendSync")
	t.Cleanup(release)

	opened := make(chan *store.Store, 1)
	go func() {
		s, _, err := store.Open(dir, f, clockAt(), quiet())
		if err != nil {
			t.Error(err)
		}
		opened <- s
	}()
	var s *store.Store
	select {
	case s = <-opened:
	case <-time.After(10 * time.Second):
		t.Fatal("Open wartet auf das Anhängen an den Verlauf")
	}
	t.Cleanup(func() { closeStore(t, s) })
	t.Cleanup(release) // vor dem Schließen (Aufräumen läuft rückwärts)
	if !same(t, s.State(), sample(12)) {
		t.Error("Zustand nicht bedient")
	}
	if got := uidsOf(s.History(10, nil)); !slices.Equal(got, []string{uidOf(e), "alt"}) {
		t.Errorf("History: %v", got)
	}
	release()
	flush(t, s)
	if n := uidCount(f, uidOf(e)); n != 1 {
		t.Errorf("Verlauf: %d-mal", n)
	}
}

// Lässt sich history.jsonl nicht rotieren, wächst die Datei weiter – der
// Speicher aber nicht: Über der Grenze fallen die ältesten Zeilen aus der
// Ansicht im Speicher (auf der Platte bleiben sie), mit einer Zeile im
// Journal; SaveHealth().History bleibt false. Das gilt auch für eine zu
// große Datei beim Start.
func TestHistoryMemoryCapped(t *testing.T) {
	const limit = 1000
	f := storetest.New()
	plant(t, f, "state.json", sample(12))
	s, j := openLogged(t, f)
	s.SetHistoryRotateAt(200)
	s.SetHistoryMemLimit(limit)
	f.FailOp(at("history.1.jsonl"), "Rename", errors.New("gesperrt"))
	ctx := bounded(t)
	var all []model.Entry
	for i := range 30 {
		e := entryAt(fmt.Sprint("Nr. ", i), t0.Add(time.Duration(i)*time.Second))
		s.Log(e)
		_ = s.Flush(ctx) // die gescheiterte Rotation meldet Flush je nach Reihenfolge – SaveHealth hält sie
		all = append(all, e)
	}
	if n := len(fileLines(f, "history.jsonl")); n != 30 {
		t.Errorf("auf der Platte %d Zeilen statt 30", n)
	}
	checkNewest(t, s.History(1000, nil), all, limit)
	if n := j.count("Verlauf im Speicher gekürzt"); n != 1 {
		t.Errorf("Journal: %d Zeilen zum Kürzen", n)
	}
	if s.SaveHealth().History {
		t.Error("SaveHealth meldet die gescheiterte Rotation nicht")
	}

	// Beim Start: eine zu große Datei.
	defer store.SetDefaultHistoryMemLimit(limit)()
	g := storetest.New()
	plant(t, g, "state.json", sample(12))
	var text strings.Builder
	for _, e := range all {
		text.WriteString(lineOf(t, e) + "\n")
	}
	g.Put(at("history.jsonl"), []byte(text.String()))
	re, _ := openFake(t, g)
	checkNewest(t, re.History(1000, nil), all, limit)
}

// checkNewest: got sind die neuesten Einträge von all (neueste zuerst),
// weniger als alle, zusammen höchstens limit Bytes (Zeilen samt
// Zeilenende).
func checkNewest(t *testing.T, got, all []model.Entry, limit int) {
	t.Helper()
	if len(got) == 0 || len(got) >= len(all) {
		t.Fatalf("im Speicher %d von %d Einträgen", len(got), len(all))
	}
	size := 0
	for i, e := range got {
		if uidOf(e) != uidOf(all[len(all)-1-i]) {
			t.Errorf("Eintrag %d ist nicht der %d.-neueste", i, i+1)
		}
		size += len(lineOf(t, e)) + 1
	}
	if size > limit {
		t.Errorf("%d Bytes im Speicher, Grenze %d", size, limit)
	}
}

// HistoryScan ist History für Leser mit Filter: Zeilen, die die Vorprüfung
// ablehnt, kommen nie bei keep an; nach maxBytes Bytes an Zeilen der Dateien
// (neueste zuerst, samt Zeilenende) ist Schluss – auch über beide Dateien
// hinweg. Was noch nicht angehängt ist, zählt nicht zur Grenze und geht
// nicht durch die Vorprüfung (es ist keine Zeile). Ohne Grenze und ohne
// Vorprüfung ist es History.
func TestHistoryScan(t *testing.T) {
	f := storetest.New()
	plant(t, f, "state.json", sample(12))
	lines := []string{
		`{"event":"key_added","label":"A","log_at":1763373000,"uid":"a"}`,
		`{"event":"key_added","label":"B skip","log_at":1763373001,"uid":"b"}`,
		`{"event":"key_added","label":"C","log_at":1763373002,"uid":"c"}`,
		`{"event":"key_added","label":"D","log_at":1763373003,"uid":"d"}`,
	}
	f.Put(at("history.1.jsonl"), []byte(lines[0]+"\n"+lines[1]+"\n"))
	f.Put(at("history.jsonl"), []byte(lines[2]+"\n"+lines[3]+"\n"))
	s, _ := openFake(t, f)
	release := hang(t, f, at("history.jsonl"))
	pend := entryAt("skip, aber noch nicht angehängt", t0)
	s.Log(pend)

	var seen []string
	may := func(line []byte) bool {
		e, _ := store.ParseHistoryLine(line)
		seen = append(seen, uidOf(e))
		return !strings.Contains(string(line), "skip")
	}
	size := func(i int) int64 { return int64(len(lines[i])) + 1 }
	cases := []struct {
		name     string
		maxBytes int64
		want     []string
		seen     []string
	}{
		{"ohne Grenze", 0, []string{uidOf(pend), "d", "c", "a"}, []string{"d", "c", "b", "a"}},
		{"Grenze nach C", size(3) + size(2), []string{uidOf(pend), "d", "c"}, []string{"d", "c"}},
		{"ein Byte mehr: B wird noch angesehen", size(3) + size(2) + 1, []string{uidOf(pend), "d", "c"}, []string{"d", "c", "b"}},
		{"Grenze über beide Dateien", size(3) + size(2) + size(1) + 1, []string{uidOf(pend), "d", "c", "a"}, []string{"d", "c", "b", "a"}},
	}
	for _, tc := range cases {
		seen = nil
		if got := uidsOf(s.HistoryScan(10, tc.maxBytes, may, nil)); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v statt %v", tc.name, got, tc.want)
		}
		if !slices.Equal(seen, tc.seen) {
			t.Errorf("%s: Vorprüfung sah %v statt %v", tc.name, seen, tc.seen)
		}
	}
	// limit zählt wie bei History nur, was keep annimmt.
	onlyC := func(e model.Entry) bool { return e.Str("label") == "C" || e.Str("label") == "A" }
	if got := uidsOf(s.HistoryScan(1, 0, may, onlyC)); !slices.Equal(got, []string{"c"}) {
		t.Errorf("limit 1: %v", got)
	}
	if got, want := uidsOf(s.HistoryScan(10, 0, nil, nil)), uidsOf(s.History(10, nil)); !slices.Equal(got, want) {
		t.Errorf("ohne Grenze und Vorprüfung %v, History %v", got, want)
	}
	release()
	flush(t, s)
}
