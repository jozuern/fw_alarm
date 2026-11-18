package store_test

import (
	"bytes"
	"context"
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

// lineOf: die Zeile, die der Verlauf für e schreibt.
func lineOf(t testing.TB, e model.Entry) string {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Ein Eintrag steht erst in history.jsonl, wenn sein Dokument gesichert ist
// – für die festen Daten (state.json) wie für die flüchtigen (runtime.json).
// Ein gescheitertes Sichern hinterlässt keine Zeile.
func TestHistoryAfterSaveOnly(t *testing.T) {
	t.Run("fest", func(t *testing.T) {
		f := storetest.New()
		s, _ := openFake(t, f)
		release := hang(t, f, at("state.json.tmp"))
		e := entry("fest")
		base := f.Calls()
		errc := make(chan error, 1)
		go func() { errc <- s.Update(context.Background(), addRecipient("Dora", e)) }()
		eventually(t, "state.json hängt", func() bool { return calledSince(f, base, "WriteFileSync", at("state.json.tmp")) })
		if calledSince(f, base, "AppendSync", at("history.jsonl")) || uidCount(f, uidOf(e)) != 0 {
			t.Error("Zeile vor dem Sichern des Dokuments")
		}
		release()
		if err := <-errc; err != nil {
			t.Fatal(err)
		}
		flush(t, s)
		if n := uidCount(f, uidOf(e)); n != 1 {
			t.Fatalf("Zeile %d-mal", n)
		}
		renamed, appended := -1, -1
		for i, c := range f.Log()[base:] {
			if c.Op == "Rename" && contains(c.Names, at("state.json")) && renamed < 0 {
				renamed = i
			}
			if c.Op == "AppendSync" && appended < 0 {
				appended = i
			}
		}
		if renamed < 0 || appended < renamed {
			t.Errorf("Anhängen (Aufruf %d) nicht nach dem Umbenennen von state.json (Aufruf %d)", appended, renamed)
		}

		f.Fail(at("state.json.tmp"), errors.New("Platte voll"))
		e2 := entry("gescheitert")
		if err := s.Update(context.Background(), addRecipient("Emil", e2)); !errors.Is(err, store.ErrStorage) {
			t.Fatalf("Update: %v", err)
		}
		f.Heal()
		flush(t, s)
		if n := uidCount(f, uidOf(e2)); n != 0 {
			t.Errorf("gescheitertes Sichern hinterlässt eine Zeile (%d)", n)
		}
	})

	t.Run("flüchtig", func(t *testing.T) {
		f := storetest.New()
		s, _ := openFake(t, f)
		release := hang(t, f, at("runtime.json.tmp"))
		e := entry("flüchtig")
		base := f.Calls()
		s.Log(e)
		eventually(t, "runtime.json hängt", func() bool { return calledSince(f, base, "WriteFileSync", at("runtime.json.tmp")) })
		if calledSince(f, base, "AppendSync", at("history.jsonl")) || uidCount(f, uidOf(e)) != 0 {
			t.Error("Zeile vor dem Sichern des Dokuments")
		}
		release()
		flush(t, s)
		if n := uidCount(f, uidOf(e)); n != 1 {
			t.Errorf("Zeile %d-mal", n)
		}

		f.Fail(at("runtime.json.tmp"), errors.New("Platte voll"))
		e2 := entry("gescheitert")
		s.Log(e2)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.Flush(ctx); !errors.Is(err, store.ErrStorage) {
			t.Errorf("Flush: %v", err)
		}
		if n := uidCount(f, uidOf(e2)); n != 0 {
			t.Errorf("Zeile ohne gesichertes Dokument (%d)", n)
		}
		f.Heal()
		flush(t, s)
		if n := uidCount(f, uidOf(e2)); n != 1 {
			t.Errorf("nach dem Sichern: Zeile %d-mal", n)
		}
	})
}

// Absturz zwischen "Dokument gesichert" und "Zeile angehängt" und zwischen
// "Zeile angehängt" und "outbox nachgesichert": Nach dem Neustart steht die
// Zeile genau einmal im Verlauf, und die outbox wird leer.
func TestNoDuplicateAfterRestart(t *testing.T) {
	for _, kind := range []struct{ name, doc string }{{"fest", "state.json"}, {"flüchtig", "runtime.json"}} {
		act := func(t *testing.T, s *store.Store, f *storetest.FaultFS, e model.Entry) {
			t.Helper()
			if kind.doc == "state.json" {
				update(t, s, addRecipient("Dora", e))
				return
			}
			s.Log(e)
			eventually(t, "runtime.json gesichert", func() bool { return slices.Contains(outboxOf(f, "runtime.json"), uidOf(e)) })
		}
		restart := func(t *testing.T, g *storetest.FaultFS, e model.Entry) {
			t.Helper()
			re, _ := open(t, g)
			if n := countIn(re.History(100, nil), uidOf(e)); n != 1 {
				t.Errorf("History nach dem Neustart: %d-mal", n)
			}
			flush(t, re)
			if n := uidCount(g, uidOf(e)); n != 1 {
				t.Errorf("Verlauf nach dem Neustart: Zeile %d-mal", n)
			}
			if box := outboxOf(g, kind.doc); len(box) != 0 {
				t.Errorf("%s: outbox nach dem Neustart %v", kind.doc, box)
			}
			closeStore(t, re)
			again, _ := open(t, g)
			flush(t, again)
			if n := uidCount(g, uidOf(e)); n != 1 {
				t.Errorf("nach dem zweiten Neustart: Zeile %d-mal", n)
			}
		}

		t.Run(kind.name+": gesichert, nicht angehängt", func(t *testing.T) {
			f := storetest.New()
			s, _ := openFake(t, f)
			release := hang(t, f, at("history.jsonl"))
			e := entry("Absturz 1")
			base := f.Calls()
			act(t, s, f, e)
			eventually(t, "Anhängen hängt", func() bool { return calledSince(f, base, "AppendSync", at("history.jsonl")) })
			g := f.AfterCrash()
			release()
			if uidCount(g, uidOf(e)) != 0 || !slices.Contains(outboxOf(g, kind.doc), uidOf(e)) {
				t.Fatalf("Ausgangslage: Zeile %d-mal, outbox %v", uidCount(g, uidOf(e)), outboxOf(g, kind.doc))
			}
			restart(t, g, e)
		})

		t.Run(kind.name+": angehängt, outbox nicht nachgesichert", func(t *testing.T) {
			f := storetest.New()
			s, _ := openFake(t, f)
			releaseHist := hang(t, f, at("history.jsonl"))
			e := entry("Absturz 2")
			base := f.Calls()
			act(t, s, f, e)
			eventually(t, "Anhängen hängt", func() bool { return calledSince(f, base, "AppendSync", at("history.jsonl")) })
			hang(t, f, at(kind.doc+".tmp")) // das Nachsichern hängt
			releaseHist()
			eventually(t, "Zeile angehängt", func() bool { return uidCount(f, uidOf(e)) == 1 })
			g := f.AfterCrash()
			if !slices.Contains(outboxOf(g, kind.doc), uidOf(e)) {
				t.Fatalf("Ausgangslage: outbox %v", outboxOf(g, kind.doc))
			}
			restart(t, g, e)
		})
	}
}

// Nach dem Anhängen nimmt ein Nachsichern die Einträge aus der outbox –
// ohne weitere Änderung, ohne Flush. Scheitert es, wird das übergangen; die
// nächste Änderung nimmt sie mit, und der Verlauf zeigt sie nie doppelt.
func TestOutboxPrunedByFollowUpSave(t *testing.T) {
	f := storetest.New()
	s, _ := openFake(t, f)

	e := entry("fest")
	update(t, s, addRecipient("Dora", e))
	eventually(t, "state.json ohne den Eintrag", func() bool {
		st := stateIn(f, "state.json")
		return hasLabel(st, "Dora") && len(st.Outbox) == 0 && uidCount(f, uidOf(e)) == 1
	})
	if len(s.State().Outbox) != 0 {
		t.Errorf("outbox im Speicher: %v", uidsOf(s.State().Outbox))
	}

	e2 := entry("flüchtig")
	s.Log(e2)
	eventually(t, "runtime.json ohne den Eintrag", func() bool {
		rt := runtimeIn(f)
		return rt != nil && len(rt.Outbox) == 0 && uidCount(f, uidOf(e2)) == 1
	})

	// Das Nachsichern scheitert: übergangen.
	releaseHist := hang(t, f, at("history.jsonl"))
	e3 := entry("Nachsichern scheitert")
	base := f.Calls()
	update(t, s, addRecipient("Emil", e3))
	eventually(t, "Anhängen hängt", func() bool { return calledSince(f, base, "AppendSync", at("history.jsonl")) })
	f.Fail(at("state.json.tmp"), errors.New("Platte voll"))
	base = f.Calls()
	releaseHist()
	eventually(t, "Nachsichern versucht", func() bool {
		for _, c := range f.Log()[base:] {
			if c.Op == "WriteFileSync" && contains(c.Names, at("state.json.tmp")) && c.Err != nil {
				return true
			}
		}
		return false
	})
	if n := uidCount(f, uidOf(e3)); n != 1 {
		t.Errorf("Verlauf: %d-mal", n)
	}
	if !slices.Contains(uidsOf(s.State().Outbox), uidOf(e3)) {
		t.Error("outbox ohne gelungenes Sichern geändert")
	}
	if n := countIn(s.History(100, nil), uidOf(e3)); n != 1 {
		t.Errorf("History zeigt den angehängten Eintrag aus der outbox %d-mal", n)
	}
	f.Heal()
	update(t, s, func(d *model.State) error { d.Settings.MuteMaxSeconds = 3600; return nil })
	if slices.Contains(outboxOf(f, "state.json"), uidOf(e3)) || slices.Contains(uidsOf(s.State().Outbox), uidOf(e3)) {
		t.Error("die nächste Änderung nimmt den Eintrag nicht aus der outbox")
	}
	flush(t, s)
	if n := uidCount(f, uidOf(e3)); n != 1 {
		t.Errorf("Verlauf am Ende: %d-mal", n)
	}
}

// History zeigt, was noch nicht angehängt ist, vorn – neueste zuerst: aus
// einer gesicherten Änderung, deren Zeile noch aussteht, und aus Flüchtigem,
// das noch nicht einmal gesichert ist. History öffnet dabei nie eine Datei.
func TestHistoryReadsPending(t *testing.T) {
	f := storetest.New()
	old1 := `{"event":"key_added","log_at":1763373000,"uid":"alt1"}`
	old2 := `{"event":"key_removed","log_at":1763373100,"uid":"alt2"}`
	f.Put(at("history.1.jsonl"), []byte(old1+"\n"))
	f.Put(at("history.jsonl"), []byte(old2+"\n"))
	plant(t, f, "state.json", sample(12))
	s, _ := openFake(t, f)

	// Beide Schreiber bleiben an ihrem Hänger stehen; die Änderung berührt
	// die Liste nicht (sonst schriebe die Not-Kopie nebenher) – danach
	// fasst nichts im Hintergrund mehr die Platte an.
	releaseHist := hang(t, f, at("history.jsonl"))
	e1 := entryAt("fest", t0)
	base := f.Calls()
	update(t, s, func(d *model.State) error { d.Settings.MuteMaxSeconds = 60; d.Log(e1); return nil })
	releaseRt := hang(t, f, at("runtime.json.tmp"))
	e2 := entryAt("flüchtig", t0.Add(time.Second))
	s.Log(e2)
	eventually(t, "beide Schreiber hängen", func() bool {
		return calledSince(f, base, "AppendSync", at("history.jsonl")) && calledSince(f, base, "WriteFileSync", at("runtime.json.tmp"))
	})

	want := []string{uidOf(e2), uidOf(e1), "alt2", "alt1"}
	if got := uidsOf(s.History(10, nil)); !slices.Equal(got, want) {
		t.Errorf("History: %v statt %v", got, want)
	}
	if got := uidsOf(s.History(1, nil)); !slices.Equal(got, want[:1]) {
		t.Errorf("History(1): %v", got)
	}
	onlyFest := func(e model.Entry) bool { return e.Str("label") == "fest" || e.Str("event") == "key_removed" }
	if got := uidsOf(s.History(10, onlyFest)); !slices.Equal(got, []string{uidOf(e1), "alt2"}) {
		t.Errorf("History mit Filter: %v", got)
	}

	// Kein Aufruf der Dateischicht – auch nicht bei 1 000 Aufrufen.
	n := f.Calls()
	for range 1000 {
		_ = s.History(50, nil)
	}
	if f.Calls() != n {
		t.Errorf("History öffnet Dateien: %v", f.Log()[n:])
	}

	// Was der Aufrufer bekommt, ist seins: Ändern trifft nicht den Speicher.
	got := s.History(10, nil)
	got[0]["label"] = "verändert"
	if s.History(1, nil)[0].Str("label") != "flüchtig" {
		t.Error("History teilt Einträge mit dem Speicher")
	}

	releaseRt()
	releaseHist()
	flush(t, s)
	if got := uidsOf(s.History(10, nil)); !slices.Equal(got, want) {
		t.Errorf("History nach dem Anhängen: %v statt %v", got, want)
	}
}

// Endet history.jsonl nicht mit einem Zeilenende (eine abgerissene Zeile),
// schreibt der Verlauf zuerst eines – die neue Zeile bleibt heil, die
// abgerissene wird beim Lesen übersprungen.
func TestAppendAfterTornLine(t *testing.T) {
	f := storetest.New()
	whole := `{"event":"key_added","log_at":1763373000,"uid":"ganz"}`
	torn := `{"event":"key_removed","log_a`
	f.Put(at("history.jsonl"), []byte(whole+"\n"+torn))
	plant(t, f, "state.json", sample(12))
	s, _ := openFake(t, f)
	if got := uidsOf(s.History(10, nil)); !slices.Equal(got, []string{"ganz"}) {
		t.Errorf("History mit abgerissener Zeile: %v", got)
	}
	e := entry("danach")
	s.Log(e)
	flush(t, s)
	b, _ := f.Get(at("history.jsonl"))
	if want := whole + "\n" + torn + "\n" + lineOf(t, e) + "\n"; string(b) != want {
		t.Errorf("history.jsonl:\n%q\nstatt\n%q", b, want)
	}
	if got := uidsOf(s.History(10, nil)); !slices.Equal(got, []string{uidOf(e), "ganz"}) {
		t.Errorf("History: %v", got)
	}
	e2 := entry("noch eine")
	s.Log(e2)
	flush(t, s)
	b, _ = f.Get(at("history.jsonl"))
	if want := whole + "\n" + torn + "\n" + lineOf(t, e) + "\n" + lineOf(t, e2) + "\n"; string(b) != want {
		t.Errorf("zweites Anhängen:\n%q", b)
	}
}

// Über der Grenze wandert history.jsonl nach history.1.jsonl, bevor
// angehängt wird; der Speicher hält beide, die älteste Datei fällt beim
// nächsten Mal heraus. Scheitert das Umbenennen, wird trotzdem angehängt.
func TestRotation(t *testing.T) {
	f := storetest.New()
	plant(t, f, "state.json", sample(12))
	s, _ := openFake(t, f)
	s.SetHistoryRotateAt(300)

	var all []model.Entry
	logOne := func(i int) (before []byte) {
		before, _ = f.Get(at("history.jsonl"))
		e := entryAt(fmt.Sprint("Nr. ", i), t0.Add(time.Duration(i)*time.Second))
		s.Log(e)
		flush(t, s)
		all = append(all, e)
		return before
	}
	newestFirst := func(list []model.Entry) []string {
		out := uidsOf(list)
		slices.Reverse(out)
		return out
	}

	var first int // Zahl der Zeilen in der ersten Datei
	for i := 0; ; i++ {
		if i > 50 {
			t.Fatal("keine Rotation")
		}
		before := logOne(i)
		if _, ok := f.Get(at("history.1.jsonl")); ok {
			if got, _ := f.Get(at("history.1.jsonl")); !bytes.Equal(got, before) || len(before) <= 300 {
				t.Errorf("history.1.jsonl ist nicht die Datei davor (%d Bytes)", len(before))
			}
			if lines := fileLines(f, "history.jsonl"); len(lines) != 1 || lines[0] != lineOf(t, all[i]) {
				t.Errorf("history.jsonl nach der Rotation: %v", lines)
			}
			first = i
			break
		}
	}
	if got := uidsOf(s.History(100, nil)); !slices.Equal(got, newestFirst(all)) {
		t.Errorf("History über beide Dateien: %d Einträge", len(got))
	}

	// Zweite Rotation: die erste Datei fällt aus Platte und Speicher.
	for i := first + 1; ; i++ {
		if i > first+50 {
			t.Fatal("keine zweite Rotation")
		}
		logOne(i)
		if lines := fileLines(f, "history.jsonl"); len(lines) == 1 {
			break
		}
	}
	if got := uidsOf(s.History(100, nil)); !slices.Equal(got, newestFirst(all[first:])) {
		t.Errorf("History nach der zweiten Rotation: %v", got)
	}

	// Umbenennen scheitert: Die Zeile kommt trotzdem an, SaveHealth meldet
	// es; danach rotiert der nächste Schub.
	for b, _ := f.Get(at("history.jsonl")); len(b) <= 300; b, _ = f.Get(at("history.jsonl")) {
		logOne(len(all))
	}
	f.FailOp(at("history.1.jsonl"), "Rename", errors.New("gesperrt"))
	e := entry("trotzdem")
	s.Log(e)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Ob Flush den Fehler sieht, hängt davon ab, wer den Schub anhängt –
	// sein eigener Anstoß oder der Schreiber davor; ausstehend ist danach
	// nichts mehr (die Zeile steht da), den Fehler hält SaveHealth.
	_ = s.Flush(ctx)
	if n := uidCount(f, uidOf(e)); n != 1 || s.SaveHealth().History {
		t.Errorf("Zeile %d-mal, SaveHealth %+v", n, s.SaveHealth())
	}
	f.Heal()
	logOne(len(all))
	if lines := fileLines(f, "history.jsonl"); len(lines) != 1 || !s.SaveHealth().History {
		t.Errorf("nach der Reparatur: %d Zeilen, SaveHealth %+v", len(lines), s.SaveHealth())
	}
}

// Ein Verlauf, der sich beim Start nicht lesen lässt (ein Ordner an seiner
// Stelle), ist im Speicher leer, und SaveHealth().History ist false;
// Änderungen gelingen trotzdem. Ist der Ordner weg, holt der Verlauf alles
// nach.
func TestHistoryUnreadableAtStart(t *testing.T) {
	f := storetest.New()
	plant(t, f, "state.json", sample(12))
	f.PutDir(at("history.jsonl"))
	f.Put(at("history.jsonl", "inhalt"), []byte(`{"event":"key_added","uid":"im Ordner"}`+"\n"))
	f.Put(at("history.1.jsonl"), []byte(`{"event":"key_added","log_at":1,"uid":"alt"}`+"\n"))
	s, _ := openFake(t, f)
	if s.Emergency() {
		t.Fatal("Notbetrieb")
	}
	if got := uidsOf(s.History(10, nil)); !slices.Equal(got, []string{"alt"}) {
		t.Errorf("History: %v", got)
	}
	if s.SaveHealth().History {
		t.Error("SaveHealth meldet den unlesbaren Verlauf nicht")
	}
	e := entry("fest")
	update(t, s, addRecipient("Dora", e))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Flush(ctx); !errors.Is(err, store.ErrStorage) {
		t.Errorf("Flush: %v", err)
	}
	if got := uidsOf(s.History(10, nil)); !slices.Equal(got, []string{uidOf(e), "alt"}) {
		t.Errorf("History mit ausstehendem Eintrag: %v", got)
	}

	if err := f.Remove(at("history.jsonl", "inhalt")); err != nil {
		t.Fatal(err)
	}
	if err := f.Remove(at("history.jsonl")); err != nil {
		t.Fatal(err)
	}
	flush(t, s)
	if !s.SaveHealth().History || uidCount(f, uidOf(e)) != 1 {
		t.Errorf("nach der Reparatur: SaveHealth %+v, Zeile %d-mal", s.SaveHealth(), uidCount(f, uidOf(e)))
	}
	if got := uidsOf(s.History(10, nil)); !slices.Equal(got, []string{uidOf(e), "alt"}) {
		t.Errorf("History nach der Reparatur: %v", got)
	}
}

// Meldet das Anhängen einen Fehler, obwohl die Zeile schon in der Datei
// steht (fsync scheitert nach dem Schreiben), liest der nächste Versuch die
// Datei neu – die Zeile steht danach genau einmal da.
func TestAppendRetryNeverDoubles(t *testing.T) {
	f := storetest.New()
	s, _ := openFake(t, f)
	f.FailAfter(at("history.jsonl"), "AppendSync", errors.New("fsync gescheitert"))
	e := entry("einmal")
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
	if n := uidCount(f, uidOf(e)); n != 1 {
		t.Fatalf("Ausgangslage: Zeile %d-mal", n)
	}
	f.Heal()
	e2 := entry("danach")
	s.Log(e2)
	flush(t, s)
	for _, x := range []model.Entry{e, e2} {
		if n := uidCount(f, uidOf(x)); n != 1 {
			t.Errorf("%s: %d-mal", x.Str("label"), n)
		}
		if n := countIn(s.History(10, nil), uidOf(x)); n != 1 {
			t.Errorf("History %s: %d-mal", x.Str("label"), n)
		}
	}
	if !s.SaveHealth().History {
		t.Error("SaveHealth nach dem Wiederholen")
	}
}

// LogAndWait: steht der Eintrag auf der Platte, true; hängt die Platte,
// false nach der Frist (gestellte Uhr) – der Eintrag kommt später trotzdem
// an.
func TestLogAndWait(t *testing.T) {
	f := storetest.New()
	s, clk := openFake(t, f)
	e := entry("sofort")
	if !s.LogAndWait(e, 300*time.Millisecond) {
		t.Fatal("LogAndWait ohne Störung: false")
	}
	if n := uidCount(f, uidOf(e)); n != 1 {
		t.Errorf("nach true: Zeile %d-mal", n)
	}

	for _, file := range []string{"history.jsonl", "runtime.json.tmp"} {
		release := hang(t, f, at(file))
		e2 := entry("hängt an " + file)
		pending := clk.Pending()
		res := make(chan bool, 1)
		go func() { res <- s.LogAndWait(e2, 300*time.Millisecond) }()
		eventually(t, "LogAndWait wartet", func() bool { return clk.Pending() > pending })
		clk.Advance(299 * time.Millisecond)
		select {
		case <-res:
			t.Fatalf("%s: kehrt vor der Frist zurück", file)
		default:
		}
		clk.Advance(time.Millisecond)
		select {
		case ok := <-res:
			if ok {
				t.Errorf("%s: true bei hängender Platte", file)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: kehrt nach der Frist nicht zurück", file)
		}
		if n := uidCount(f, uidOf(e2)); n != 0 {
			t.Errorf("%s: Zeile trotz hängender Platte", file)
		}
		release()
		flush(t, s)
		if n := uidCount(f, uidOf(e2)); n != 1 {
			t.Errorf("%s: kommt später nicht an (%d)", file, n)
		}
	}
}

// Verlaufszeilen kommen von außen (Übernahme, Prüfstand, von Hand): Was auch
// immer in history.jsonl steht – History stürzt nicht, liefert genau die
// Zeilen, die ein JSON-Objekt sind, und eine danach angehängte Zeile bleibt
// heil, auch über einen Neustart.
func FuzzHistoryLine(f *testing.F) {
	for _, seed := range []string{
		`{"event":"key_added","log_at":1763373000,"uid":"a"}` + "\n",
		`{"event":"key_added","log_a`,
		"null\n[]\n\"text\"\n17\n",
		`{"a":1}{"b":2}` + "\n" + `{"a":1} x` + "\n",
		"{\"label\":\"\xff\xfe\"}\n",
		`{"n":1e999,"m":-0,"k":12345678901234567890}` + "\n",
		`{"x":{"y":[1,2,{"z":null}]},"uid":["kein","Text"]}` + "\n",
		`{"a":1,"a":2}` + "\r\n\r\n",
		"\n\n\n",
		"",
		strings.Repeat("[", 200) + "\n",
		`{"u":"  <>&"}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		fsys := storetest.New()
		plant(t, fsys, "state.json", sample(12))
		fsys.Put(at("history.jsonl"), data)
		s, _, err := store.Open(dir, fsys, clockAt(), quiet())
		if err != nil {
			t.Fatal(err)
		}
		defer closeStore(t, s)

		objects := 0
		for seg := range bytes.SplitSeq(data, []byte("\n")) {
			if json.Valid(seg) && bytes.HasPrefix(bytes.TrimLeft(seg, " \t\r"), []byte("{")) {
				objects++
			}
		}
		got := s.History(1_000_000, nil)
		if len(got) != objects {
			t.Errorf("%d Einträge statt %d", len(got), objects)
		}
		for _, e := range got {
			b, err := json.Marshal(e)
			if err != nil {
				t.Fatalf("Eintrag nicht schreibbar: %v", err)
			}
			dec := json.NewDecoder(bytes.NewReader(b))
			dec.UseNumber()
			var again model.Entry
			if err := dec.Decode(&again); err != nil || again == nil {
				t.Fatalf("Eintrag nicht wieder lesbar: %v", err)
			}
		}

		e := entry("danach")
		s.Log(e)
		flush(t, s)
		back, _ := fsys.Get(at("history.jsonl"))
		want := slices.Clone(data)
		if len(want) > 0 && want[len(want)-1] != '\n' {
			want = append(want, '\n')
		}
		want = append(want, lineOf(t, e)+"\n"...)
		if !bytes.Equal(back, want) {
			t.Errorf("history.jsonl nach dem Anhängen:\n%q\nstatt\n%q", back, want)
		}
		if newest := s.History(1, nil); len(newest) != 1 || uidOf(newest[0]) != uidOf(e) {
			t.Errorf("neuester Eintrag: %v", newest)
		}
		closeStore(t, s)
		re, _, err := store.Open(dir, fsys, clockAt(), quiet())
		if err != nil {
			t.Fatal(err)
		}
		defer closeStore(t, re)
		if newest := re.History(1, nil); len(newest) != 1 || uidOf(newest[0]) != uidOf(e) {
			t.Errorf("nach dem Neustart: %v", newest)
		}
	})
}
