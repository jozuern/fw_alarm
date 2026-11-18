package box

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fwalarm/internal/model"
	"fwalarm/internal/store/storetest"
)

// deliveredTest: ein TEST-Befehl, an die Box ausgeliefert (Poll).
func (b *bench) deliveredTest() model.Command {
	b.t.Helper()
	cmd := b.enqueue(model.CommandTest)
	if got := b.pollBox("p", cmd.ID-1); !strings.Contains(got, "type=TEST") {
		b.t.Fatalf("nicht ausgeliefert: %q", got)
	}
	b.quiet()
	return cmd
}

// okAck: Die Antwort einer Quittung ist immer 200 "OK\n" (Klartext, ohne
// Zwischenspeichern).
func okAck(t *testing.T, what string, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != 200 || rec.Body.String() != "OK\n" || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" ||
		rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("%s: %d %v %q", what, rec.Code, rec.Header(), rec.Body.String())
	}
}

// Die Quittung antwortet IMMER "OK" – auch wenn nichts gesichert werden kann
// (mit einem Fehler wiederholte die Box sie endlos, pollt so lange nicht und
// pusht im Fehler-Backoff keinen Status mehr). Lässt sie sich nicht sichern,
// bleibt der Befehl ausgeliefert.
func TestAckAlwaysOK(t *testing.T) {
	t.Run("gesichert", func(t *testing.T) {
		b := newBench(t)
		b.deliveredTest()
		okAck(t, "Quittung", b.ackBox(url.Values{"id": {"1"}, "result": {"executed"}, "message": {"gesendet"}}))
		cur := b.current()
		if cur.State != model.CommandAcked || cur.AckResult != "executed" || cur.AckMessage != "gesendet" ||
			cur.AckedBy != "fw-box-1" || cur.AckedAt != t0.Unix() {
			t.Errorf("%+v", cur)
		}
		e := b.events(model.EvAcked)
		if len(e) != 1 || e[0].Str("command.ack_result") != "executed" || e[0].Int("command.id") != 1 {
			t.Errorf("Verlauf %v", e)
		}
	})
	t.Run("die Platte scheitert", func(t *testing.T) {
		b := newBench(t)
		b.deliveredTest()
		b.fs.Fail(stateTmp, errors.New("Platte voll"))
		start := time.Now()
		okAck(t, "Platte scheitert", b.ackBox(url.Values{"id": {"1"}, "result": {"executed"}}))
		if took := time.Since(start); took > underBudget {
			t.Errorf("Antwort nach %v", took)
		}
		if b.current().State != model.CommandDelivered || len(b.events(model.EvAcked)) != 0 {
			t.Errorf("nicht gesichert, aber %+v", b.current())
		}
		// Die Box schickt sie noch einmal (das macht sie nur bei einem
		// Fehler – hier zur Probe): jetzt gilt sie.
		b.fs.Heal()
		okAck(t, "erneut", b.ackBox(url.Values{"id": {"1"}, "result": {"executed"}}))
		if b.current().State != model.CommandAcked {
			t.Errorf("%+v", b.current())
		}
	})
	t.Run("die Platte hängt: OK nach der Frist der Box", func(t *testing.T) {
		b := newBench(t)
		b.deliveredTest()
		release := b.fs.Hang(stateTmp)
		start := time.Now()
		okAck(t, "Platte hängt", b.ackBox(url.Values{"id": {"1"}, "result": {"executed"}}))
		if took := time.Since(start); took < underBudget || took > oneBudget {
			t.Errorf("Antwort nach %v, erwartet etwa 1,5 s", took)
		}
		if b.current().State != model.CommandDelivered {
			t.Errorf("vor dem Sichern %+v", b.current())
		}
		release()
		waitFor(t, "Quittung spät gesichert", func() bool { return b.current().State == model.CommandAcked })
	})
	t.Run("Notbetrieb: OK, als verspätet vermerkt", func(t *testing.T) {
		f := storetest.New()
		f.Put(filepath.Join(dir, "state.json"), []byte("{kaputt"))
		b := newBenchOn(t, f)
		okAck(t, "Notbetrieb", b.ackBox(url.Values{"id": {"1"}, "result": {"executed"}}))
		if late := b.events(model.EvLateAck); len(late) != 1 || late[0].Int("id") != 1 || late[0].Str("type") != "" {
			t.Errorf("late_ack %v", late)
		}
	})
	t.Run("unbekannte Nummer, Unsinn, nichts: OK, vermerkt, nichts Festes gesichert", func(t *testing.T) {
		b := newBench(t)
		b.quiet()
		writes := stateWrites(b.fs)
		forms := []url.Values{{"id": {"99"}}, {"id": {"abc"}}, {"id": {"-1"}}, {}, {"id": {"99999999999999999999"}}}
		for _, form := range forms {
			okAck(t, form.Encode(), b.ackBox(form))
		}
		b.quiet()
		if n := stateWrites(b.fs); n != writes {
			t.Errorf("verspätete Quittungen sicherten state.json %d-mal", n-writes)
		}
		if late := b.events(model.EvLateAck); len(late) != len(forms) {
			t.Errorf("%d late_ack, erwartet %d", len(late), len(forms))
		}
	})
	t.Run("verspätete Quittung bei kaputter Platte: der Eintrag kommt in den Verlauf, sobald die flüchtige Seite schreiben kann", func(t *testing.T) {
		b := newBench(t)
		b.enqueue(model.CommandTest) // wartet noch – eine Quittung dafür ist verspätet
		b.fs.Fail(stateTmp, errors.New("Platte voll"))
		runtimeTmp := filepath.Join(dir, "runtime.json.tmp")
		b.fs.Fail(runtimeTmp, errors.New("Platte voll"))
		okAck(t, "kaputte Platte", b.ackBox(url.Values{"id": {"1"}, "result": {"executed"}}))
		if late := b.events(model.EvLateAck); len(late) != 1 || late[0].Str("type") != model.CommandTest {
			t.Fatalf("aus dem Speicher: %v", late)
		}
		inFile := func() bool {
			raw, _ := b.fs.Get(filepath.Join(dir, "history.jsonl"))
			return strings.Contains(string(raw), `"event":"late_ack"`)
		}
		if inFile() {
			t.Fatal("schon im Verlauf, obwohl runtime.json nicht gesichert werden kann")
		}
		b.fs.Fail(runtimeTmp, nil) // die flüchtige Seite geht wieder, die feste nicht
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = b.c.Store.Flush(ctx) // scheitert an state.json – die flüchtigen Daten sichert es trotzdem
		if !inFile() {
			t.Error("late_ack nicht im Verlauf, obwohl runtime.json wieder gesichert werden kann")
		}
		if b.current().State != model.CommandPending {
			t.Errorf("%+v", b.current())
		}
	})
}

// Das Journal: eine Quittung, die gelten müsste, aber nicht gesichert werden
// kann, steht höchstens einmal je Minute darin – mit einem Satz, der stimmt
// ("bleibt als ausgeliefert stehen"); eine verspätete Quittung oder der
// Notbetrieb brauchen nichts Festes und schreiben nichts ins Journal.
func TestAckJournal(t *testing.T) {
	b := newBench(t)
	b.deliveredTest()
	j := b.captureJournal()
	b.fs.Fail(stateTmp, errors.New("Platte voll"))
	for range 5 {
		okAck(t, "kaputte Platte", b.ackBox(url.Values{"id": {"1"}, "result": {"executed"}}))
	}
	okAck(t, "unbekannt", b.ackBox(url.Values{"id": {"99"}, "result": {"executed"}}))
	got := j.warnings("Quittung")
	if len(got) != 1 || !strings.Contains(got[0], "Quittung der Box nicht gesichert – der Befehl bleibt als ausgeliefert stehen") ||
		!strings.Contains(got[0], "befehl=1") {
		t.Errorf("%d Zeilen: %v", len(got), got)
	}
	b.advance(time.Minute)
	okAck(t, "eine Minute später", b.ackBox(url.Values{"id": {"1"}, "result": {"executed"}}))
	if got := j.warnings("Quittung"); len(got) != 2 || !strings.Contains(got[1], "unterdrueckt=4") {
		t.Errorf("nach einer Minute: %v", got)
	}

	f := storetest.New()
	f.Put(filepath.Join(dir, "state.json"), []byte("{kaputt"))
	e := newBenchOn(t, f)
	je := e.captureJournal()
	okAck(t, "Notbetrieb", e.ackBox(url.Values{"id": {"1"}, "result": {"executed"}}))
	if got := je.warnings(""); len(got) != 0 {
		t.Errorf("Notbetrieb: %v", got)
	}
}

// Nur ein falsches Maschinen-Token gibt 403 FORBIDDEN – nichts wird
// quittiert, nichts vermerkt.
func TestAckForbidden(t *testing.T) {
	b := newBench(t)
	b.deliveredTest()
	for name, form := range map[string]url.Values{
		"ohne Token":     {"id": {"1"}, "result": {"executed"}},
		"falsches Token": {"token": {"falsch"}, "id": {"1"}},
	} {
		rec := b.sendTo(b.ack, "/api/ack.php", form)
		if rec.Code != 403 || rec.Body.String() != "FORBIDDEN" {
			t.Errorf("%s: %d %q", name, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	b.ack(rec, httptest.NewRequest(http.MethodGet, "/api/ack.php?token="+machineToken+"&id=1", nil))
	if rec.Code != 403 || rec.Body.String() != "FORBIDDEN" {
		t.Errorf("GET: %d %q", rec.Code, rec.Body.String())
	}
	if b.current().State != model.CommandDelivered || len(b.events(model.EvLateAck)) != 0 {
		t.Errorf("abgewiesen, aber %+v", b.current())
	}
}

// Dieselbe Quittung noch einmal (die Antwort kam für die 4 s der Box zu
// spät): bestätigt, aber weder eine neue Zeit noch ein zweites acked – und
// nichts gesichert.
func TestAckRepeatSavesNothing(t *testing.T) {
	b := newBench(t)
	b.deliveredTest()
	okAck(t, "erste", b.ackBox(url.Values{"id": {"1"}, "result": {"executed"}}))
	b.quiet()
	writes := stateWrites(b.fs)
	b.advance(30 * time.Second)
	okAck(t, "zweite", b.ackBox(url.Values{"id": {"1"}, "result": {"executed"}, "message": {"anders"}}))
	if cur := b.current(); cur.AckedAt != t0.Unix() || cur.AckMessage != "" {
		t.Errorf("%+v", cur)
	}
	if stateWrites(b.fs) != writes || len(b.events(model.EvAcked)) != 1 {
		t.Errorf("wiederholte Quittung gesichert oder vermerkt")
	}
	// Mit anderem Ergebnis gilt sie neu (PHP: nur gleiches Ergebnis ist eine
	// Wiederholung).
	okAck(t, "anderes Ergebnis", b.ackBox(url.Values{"id": {"1"}, "result": {"status_failed"}}))
	if cur := b.current(); cur.AckResult != "status_failed" || cur.AckedAt != t0.Unix()+30 || len(b.events(model.EvAcked)) != 2 {
		t.Errorf("%+v", cur)
	}
}

// Ergebnis höchstens 80, Meldung höchstens 200 Bytes – nie mitten in einem
// Zeichen; ohne Ergebnis "unknown", ohne Meldung leer; acked_by höchstens
// 80 Bytes der device_id.
func TestAckClips(t *testing.T) {
	b := newBench(t)
	b.deliveredTest()
	okAck(t, "lang", b.ackBox(url.Values{"id": {"1"}, "result": {strings.Repeat("r", 120)},
		"message": {"ä" + strings.Repeat("m", 300)}, "device_id": {strings.Repeat("d", 90)}}))
	cur := b.current()
	if cur.AckResult != strings.Repeat("r", 80) || cur.AckMessage != "ä"+strings.Repeat("m", 198) || cur.AckedBy != strings.Repeat("d", 80) {
		t.Errorf("%q %q %q", cur.AckResult, cur.AckMessage, cur.AckedBy)
	}
	b.advance(200 * time.Second)
	b.deliveredTest()
	okAck(t, "ohne Ergebnis", b.ackBox(url.Values{"id": {"2"}}))
	if cur := b.current(); cur.AckResult != "unknown" || cur.AckMessage != "" {
		t.Errorf("%+v", cur)
	}
}

// Eine verspätete Quittung kennt noch den Typ ihres Befehls (recent_types):
// Die eines Demo-Alarms gehört in den Alarm-Verlauf. Für einen noch
// wartenden Befehl mit dieser Nummer: vermerkt, er bleibt wartend und wird
// danach ganz normal ausgeliefert. Eine unbekannte Nummer: ohne Typ.
func TestLateAckCarriesType(t *testing.T) {
	b := newBench(t)
	b.deliveredTest()
	b.advance(130 * time.Second) // die Sperre nach der Auslieferung ist vorbei
	if cmd := b.enqueue(model.CommandTest); cmd.ID != 2 {
		t.Fatalf("neuer Befehl %d", cmd.ID)
	}
	okAck(t, "abgelöst", b.ackBox(url.Values{"id": {"1"}, "result": {"executed"}, "message": {"spät"}}))
	okAck(t, "wartend", b.ackBox(url.Values{"id": {"2"}, "result": {"executed"}}))
	okAck(t, "unbekannt", b.ackBox(url.Values{"id": {"99"}, "result": {"executed"}}))
	late := b.events(model.EvLateAck) // neueste zuerst
	if len(late) != 3 {
		t.Fatalf("%d verspätete Quittungen", len(late))
	}
	for i, want := range []struct {
		id  int64
		typ string
	}{{99, ""}, {2, "TEST"}, {1, "TEST"}} {
		e := late[i]
		if e.Int("id") != want.id || e.Str("type") != want.typ || e.Str("result") != "executed" || e.Str("device_id") != "fw-box-1" {
			t.Errorf("late_ack %d: %v", i, e)
		}
	}
	if late[2].Str("message") != "spät" {
		t.Errorf("Meldung %v", late[2])
	}
	if b.current().ID != 2 || b.current().State != model.CommandPending {
		t.Errorf("der neue Befehl %+v", b.current())
	}
	if got := b.pollBox("p", 1); !strings.Contains(got, "id=2\ntype=TEST\n") {
		t.Errorf("danach ausgeliefert: %q", got)
	}
}
