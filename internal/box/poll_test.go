package box

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"fwalarm/internal/config"
	"fwalarm/internal/model"
	"fwalarm/internal/store/storetest"
)

// Befehle an die Box: der Poll (api/poll.php) und die Quittung (api/ack.php)
// wie die Box sie schickt, gegen die Ablage im Speicher – mit einer Platte,
// die hängt oder scheitert, wo der Test es will.

var stateTmp = filepath.Join(dir, "state.json.tmp")

// sendTo: api/poll.php bzw. api/ack.php mit diesem Rumpf (POST, urlencoded).
func (b *bench) sendTo(h func(http.ResponseWriter, *http.Request), target string, form url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h(rec, r)
	return rec
}

// pollAs: ein Poll wie die Box – das Token, dieses Board, diese Nonce und
// Nummer; die Antwort muss 200 Klartext ohne Zwischenspeichern sein.
func (b *bench) pollAs(device, nonce string, last int) (string, error) {
	rec := b.sendTo(b.poll, "/api/poll.php", url.Values{"token": {machineToken}, "device_id": {device},
		"nonce": {nonce}, "last_command_id": {strconv.Itoa(last)}})
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" || rec.Header().Get("Cache-Control") != "no-store" {
		return "", fmt.Errorf("%d %v %q", rec.Code, rec.Header(), rec.Body.String())
	}
	return rec.Body.String(), nil
}

// pollBox: pollAs für die Box fw-box-1, nur aus der Goroutine des Tests.
func (b *bench) pollBox(nonce string, last int) string {
	b.t.Helper()
	got, err := b.pollAs("fw-box-1", nonce, last)
	if err != nil {
		b.t.Fatal(err)
	}
	return got
}

// ackBox: eine Quittung wie die Box.
func (b *bench) ackBox(form url.Values) *httptest.ResponseRecorder {
	all := url.Values{"token": {machineToken}, "device_id": {"fw-box-1"}}
	for k, v := range form {
		all[k] = v
	}
	return b.sendTo(b.ack, "/api/ack.php", all)
}

// enqueue legt einen Befehl an wie das Dashboard (die Regeln des Modells).
func (b *bench) enqueue(typ string) model.Command {
	b.t.Helper()
	var cmd model.Command
	b.update(func(d *model.State) {
		var why string
		if cmd, why = d.Enqueue(typ, "admin", b.c.Clock.Now(), 120); why != "" {
			b.t.Fatal(why)
		}
	})
	return cmd
}

// current: der laufende Befehl im Schnappschuss (nil = keiner).
func (b *bench) current() *model.Command { return b.c.Store.State().Commands.Current }

// stateWrites: wie oft die Ablage bisher state.json geschrieben hat.
func stateWrites(f *storetest.FaultFS) int {
	n := 0
	for _, call := range f.Log() {
		if call.Op == "WriteFileSync" && len(call.Names) > 0 && call.Names[0] == stateTmp {
			n++
		}
	}
	return n
}

// waitFor wartet höchstens 5 s, bis ok gilt.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("nicht eingetreten: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

const noneP = "ok=1\nnonce=p\nid=0\ntype=NONE\nkeys_version="

// Die Zeilen zeichengenau wie PHP: ok, nonce, id, type, keys_version – ohne
// Befehl id=0 type=NONE; die Nonce gespiegelt (bereinigt); ein ausgelieferter
// Befehl nie ein zweites Mal, auch nicht nach der Quittung.
func TestPollAnswers(t *testing.T) {
	b := newBench(t)
	if got := b.pollBox("abc123", 0); got != "ok=1\nnonce=abc123\nid=0\ntype=NONE\nkeys_version=0\n" {
		t.Errorf("ohne Befehl: %q", got)
	}
	if got := b.pollBox("ab.c!d-e_f g", 0); got != "ok=1\nnonce=abcd-e_fg\nid=0\ntype=NONE\nkeys_version=0\n" {
		t.Errorf("Nonce: %q", got)
	}
	b.seed(7, anna)
	b.enqueue(model.CommandTest)
	if got := b.pollBox("p1", 0); got != "ok=1\nnonce=p1\nid=1\ntype=TEST\nkeys_version=7\n" {
		t.Errorf("mit Befehl: %q", got)
	}
	cur := b.current()
	if cur.State != model.CommandDelivered || cur.DeliveredTo != "fw-box-1" || cur.DeliveredAt != t0.Unix() {
		t.Errorf("ausgeliefert: %+v", cur)
	}
	if got := b.pollBox("p", 1); got != noneP+"7\n" {
		t.Errorf("zweiter Poll: %q", got)
	}
	if rec := b.ackBox(url.Values{"id": {"1"}, "result": {"executed"}, "message": {"gesendet"}}); rec.Code != 200 || rec.Body.String() != "OK\n" {
		t.Fatalf("Quittung: %d %q", rec.Code, rec.Body.String())
	}
	if got := b.pollBox("p", 1); got != noneP+"7\n" {
		t.Errorf("nach der Quittung: %q", got)
	}
	if n := len(b.events(model.EvDelivered)); n != 1 {
		t.Errorf("%d Einträge delivered", n)
	}
	// Ohne Nonce, ohne device_id, ohne last_command_id: die Zeilen trotzdem.
	rec := b.sendTo(b.poll, "/api/poll.php", url.Values{"token": {machineToken}})
	if rec.Code != 200 || rec.Body.String() != "ok=1\nnonce=\nid=0\ntype=NONE\nkeys_version=7\n" {
		t.Errorf("leerer Poll: %d %q", rec.Code, rec.Body.String())
	}
}

// Nur ein falsches Maschinen-Token gibt 403 FORBIDDEN – und dann geschieht
// nichts: kein Befehl ausgeliefert, keine Nummer gemerkt, keine
// Stummschaltung zurückgeschaltet.
func TestPollForbidden(t *testing.T) {
	b := newBench(t)
	a := anna
	a.Muted, a.MutedAt = true, 1 // überfällig
	b.seed(4, a)
	b.enqueue(model.CommandTest)
	writes := stateWrites(b.fs)
	for name, form := range map[string]url.Values{
		"ohne Token":      {"last_command_id": {"50"}},
		"falsches Token":  {"token": {"falsch"}, "last_command_id": {"50"}},
		"leeres Token":    {"token": {""}},
		"Token als Liste": {"token[]": {machineToken}},
	} {
		rec := b.sendTo(b.poll, "/api/poll.php", form)
		if rec.Code != 403 || rec.Body.String() != "FORBIDDEN" {
			t.Errorf("%s: %d %q", name, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	b.poll(rec, httptest.NewRequest(http.MethodGet, "/api/poll.php?token="+machineToken, nil))
	if rec.Code != 403 || rec.Body.String() != "FORBIDDEN" {
		t.Errorf("GET: %d %q", rec.Code, rec.Body.String())
	}
	st := b.c.Store.State()
	if st.Commands.Current.State != model.CommandPending || st.Commands.NextID != 2 || !st.Recipients[0].Muted || stateWrites(b.fs) != writes {
		t.Errorf("abgewiesen, aber geändert: %+v", st.Commands)
	}
}

// Höchstens einmal – das Sichern entscheidet: Die Antwort trägt den Befehl
// nur, wenn "ausgeliefert" rechtzeitig gesichert ist. Jeder andere Ausgang
// ist 200 type=NONE mit keys_version, und kein späterer Poll trägt je einen
// Befehl, den ein früherer getragen hat oder der als ausgeliefert gesichert
// wurde.
func TestPollDeliversOnlyWhenSaved(t *testing.T) {
	t.Run("das Sichern hängt: NONE nach der Frist, danach gilt er als ausgeliefert und kommt nie", func(t *testing.T) {
		b := newBench(t)
		b.seed(3, anna)
		b.enqueue(model.CommandTest)
		release := b.fs.Hang(stateTmp)
		start := time.Now()
		got := b.pollBox("p", 0)
		took := time.Since(start)
		if got != noneP+"3\n" {
			t.Errorf("während das Sichern hängt: %q", got)
		}
		if took < 1400*time.Millisecond || took > 2500*time.Millisecond {
			t.Errorf("Antwort nach %v, erwartet etwa 1,5 s", took)
		}
		if b.current().State != model.CommandPending {
			t.Error("nicht gesichert, aber schon als ausgeliefert sichtbar")
		}
		// Ein zweiter Poll, solange das Sichern noch hängt: auch NONE (seine
		// Änderung kommt nicht an die Reihe).
		if got := b.pollBox("p", 0); got != noneP+"3\n" {
			t.Errorf("zweiter Poll: %q", got)
		}
		release()
		waitFor(t, "ausgeliefert gesichert", func() bool { return b.current().State == model.CommandDelivered })
		for i := range 5 {
			if got := b.pollBox("p", 0); got != noneP+"3\n" {
				t.Fatalf("Poll %d nach dem späten Sichern: %q", i, got)
			}
		}
		if n := len(b.events(model.EvDelivered)); n != 1 {
			t.Errorf("%d Einträge delivered", n)
		}
	})

	t.Run("das Sichern scheitert: NONE sofort, der Befehl wartet, der nächste Poll liefert ihn", func(t *testing.T) {
		b := newBench(t)
		b.seed(3, anna)
		b.enqueue(model.CommandTest)
		b.fs.Fail(stateTmp, errors.New("Platte voll"))
		start := time.Now()
		if got := b.pollBox("p", 0); got != noneP+"3\n" {
			t.Errorf("Sichern scheitert: %q", got)
		}
		if took := time.Since(start); took > 500*time.Millisecond {
			t.Errorf("Antwort nach %v", took)
		}
		if b.current().State != model.CommandPending || len(b.events(model.EvDelivered)) != 0 {
			t.Errorf("nicht gesichert, aber %+v", b.current())
		}
		b.fs.Heal()
		if got := b.pollBox("p", 0); got != "ok=1\nnonce=p\nid=1\ntype=TEST\nkeys_version=3\n" {
			t.Errorf("nach der Reparatur: %q", got)
		}
	})

	t.Run("die Änderung kommt nicht an die Reihe (verworfen): NONE, nichts geschah", func(t *testing.T) {
		b := newBench(t)
		b.seed(3, anna)
		b.enqueue(model.CommandTest)
		// Eine andere Änderung hängt beim Sichern und hält den Schreiber.
		release := b.fs.Hang(stateTmp)
		before := len(b.fs.Log())
		other := make(chan error, 1)
		go func() {
			other <- b.c.Store.Update(context.Background(), func(d *model.State) error { d.Settings.MuteMaxSeconds = 7200; return nil })
		}()
		waitFor(t, "Schreiber belegt", func() bool { return writingSince(b.fs, before) })
		if got := b.pollBox("p", 0); got != noneP+"3\n" {
			t.Errorf("verworfen: %q", got)
		}
		release()
		if err := <-other; err != nil {
			t.Fatal(err)
		}
		if b.current().State != model.CommandPending {
			t.Errorf("verworfen, aber %+v", b.current())
		}
		if got := b.pollBox("p", 0); !strings.Contains(got, "id=1\ntype=TEST\n") {
			t.Errorf("danach: %q", got)
		}
	})

	t.Run("Notbetrieb: NONE mit keys_version 0", func(t *testing.T) {
		f := storetest.New()
		f.Put(filepath.Join(dir, "state.json"), []byte("{kaputt"))
		b := newBenchOn(t, f)
		if got := b.pollBox("p", 3); got != noneP+"0\n" {
			t.Errorf("Notbetrieb: %q", got)
		}
	})

	t.Run("Neustart zwischen dem Sichern und der Antwort: auf der Platte ausgeliefert – nie wieder", func(t *testing.T) {
		b := newBench(t)
		b.seed(3, anna)
		b.enqueue(model.CommandTest)
		if got := b.pollBox("p", 0); !strings.Contains(got, "type=TEST") {
			t.Fatalf("erster Poll: %q", got) // diese Antwort "geht verloren"
		}
		after := reopened(t, b.fs.AfterCrash())
		if got := after.pollBox("p", 0); got != noneP+"3\n" {
			t.Errorf("nach dem Neustart: %q", got)
		}
		if after.current().State != model.CommandDelivered {
			t.Errorf("nach dem Neustart: %+v", after.current())
		}
	})

	t.Run("Absturz, während das Sichern hängt: die Box bekam nichts – nach dem Neustart wird er ausgeliefert", func(t *testing.T) {
		b := newBench(t)
		b.seed(3, anna)
		b.enqueue(model.CommandTest)
		release := b.fs.Hang(stateTmp)
		if got := b.pollBox("p", 0); got != noneP+"3\n" {
			t.Errorf("während das Sichern hängt: %q", got)
		}
		crashed := b.fs.AfterCrash() // die neue state.json ist nie angekommen
		release()
		after := reopened(t, crashed)
		if after.current().State != model.CommandPending {
			t.Fatalf("nach dem Absturz: %+v", after.current())
		}
		if got := after.pollBox("p", 0); got != "ok=1\nnonce=p\nid=1\ntype=TEST\nkeys_version=3\n" {
			t.Errorf("nach dem Neustart: %q", got)
		}
	})
}

// writingSince: Hat seit dem Aufruf Nummer before ein Sichern von
// state.json begonnen (bei Hang wartet es dann)?
func writingSince(f *storetest.FaultFS, before int) bool {
	for _, call := range f.Log()[before:] {
		if call.Op == "WriteFileSync" && len(call.Names) > 0 && call.Names[0] == stateTmp {
			return true
		}
	}
	return false
}

// reopened: ein neuer Prüfstand auf den Dateien nach einem Absturz – wie ein
// neuer Prozess.
func reopened(t *testing.T, f *storetest.FaultFS) *bench {
	t.Helper()
	b := newBenchOn(t, f)
	if b.c.Store.Emergency() {
		t.Fatal("nach dem Absturz im Notbetrieb")
	}
	return b
}

// 20 Polls zugleich: genau einer trägt den Befehl.
func TestTwentyPollsOneCommand(t *testing.T) {
	b := newBench(t)
	b.seed(3, anna)
	b.enqueue(model.CommandTest)
	var wg sync.WaitGroup
	answers := make(chan string, 20)
	errs := make(chan error, 20)
	start := make(chan struct{})
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got, err := b.pollAs("fw-box-1", "n"+strconv.Itoa(i), 0)
			if err != nil {
				errs <- err
				return
			}
			answers <- got
		}()
	}
	close(start)
	wg.Wait()
	close(answers)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	carried := 0
	for got := range answers {
		if strings.Contains(got, "\nid=1\ntype=TEST\n") {
			carried++
		} else if !strings.Contains(got, "\nid=0\ntype=NONE\nkeys_version=3\n") {
			t.Errorf("Antwort %q", got)
		}
	}
	if carried != 1 {
		t.Errorf("%d Antworten mit dem Befehl, erwartet genau eine", carried)
	}
	if n := len(b.events(model.EvDelivered)); n != 1 {
		t.Errorf("%d Einträge delivered", n)
	}
}

// Ein Poll, der nichts ändert, sichert nichts: 1 000 Polls mit derselben
// Nummer (Liste da, Befehl quittiert, eine Stummschaltung noch in der Frist)
// schreiben gar nichts.
func TestPollWritesNothingWhenIdle(t *testing.T) {
	b := newBench(t)
	a := anna
	a.Muted, a.MutedAt = true, t0.Unix()-60
	b.seed(5, a, bert)
	b.enqueue(model.CommandTest)
	b.pollBox("p", 0)
	b.ackBox(url.Values{"id": {"1"}, "result": {"executed"}})
	if !b.c.Bg.Wait(5 * time.Second) {
		t.Fatal("Arbeit nebenher endet nicht")
	}
	b.quiet()
	before := len(b.fs.Log())
	for range 1000 {
		if got := b.pollBox("p", 1); got != noneP+"5\n" {
			t.Fatalf("%q", got)
		}
	}
	for _, call := range b.fs.Log()[before:] {
		if call.Writes() {
			t.Fatalf("ein Poll ohne Neues schrieb: %v", call)
		}
	}
}

// Die ganze Anfrage hält die Frist der Box ein, was die Platte auch tut:
// Hängt schon das Sichern der Rückschaltung (mutes.Expire), bleibt für den
// Befehl nichts übrig – NONE nach etwa 1,5 s (nicht 3 s), der Befehl wartet.
func TestPollWithinBoxBudget(t *testing.T) {
	b := newBench(t)
	a := anna
	a.Muted, a.MutedAt = true, 1 // überfällig
	b.seed(4, a)
	b.enqueue(model.CommandTest)
	release := b.fs.Hang(stateTmp)
	start := time.Now()
	got := b.pollBox("p", 0)
	if took := time.Since(start); took > 2500*time.Millisecond {
		t.Errorf("Antwort nach %v", took)
	}
	if got != noneP+"4\n" {
		t.Errorf("%q", got)
	}
	release()
	waitFor(t, "Rückschaltung gesichert", func() bool { return b.c.Store.State().KeysVersion == 5 })
	if b.current().State != model.CommandPending {
		t.Errorf("Befehl %+v", b.current())
	}
	if got := b.pollBox("p", 0); got != "ok=1\nnonce=p\nid=1\ntype=TEST\nkeys_version=5\n" {
		t.Errorf("danach: %q", got)
	}
}

// Überfällige Stummschaltungen enden mit dem Poll – die neue Version steht
// sofort in seiner Antwort.
func TestPollExpiresMutes(t *testing.T) {
	b := newBench(t)
	a := anna
	a.Muted, a.MutedAt = true, t0.Unix()-43201
	b.seed(4, a)
	if got := b.pollBox("p", 0); got != noneP+"5\n" {
		t.Errorf("%q", got)
	}
	if b.c.Store.State().Recipients[0].Muted {
		t.Error("noch stumm")
	}
}

// Nur die eingestellte Box bekommt Befehle (box_device_id): Ein anderes
// Board bekommt NONE mit der Listen-Version und ändert nichts – auch seine
// Nummer zählt nicht (PHP: is_expected_box vor dem Befehls-Lock).
func TestPollOnlyExpectedBox(t *testing.T) {
	b := newBench(t)
	b.seed(6, anna)
	b.configure(func(cfg *config.Config) { cfg.BoxDeviceID = " fw-box-1\t" })
	b.enqueue(model.CommandTest)
	writes := stateWrites(b.fs)
	got, err := b.pollAs("bastel-1", "f", 50)
	if err != nil || got != "ok=1\nnonce=f\nid=0\ntype=NONE\nkeys_version=6\n" {
		t.Errorf("fremdes Board: %q %v", got, err)
	}
	if st := b.c.Store.State(); st.Commands.Current.State != model.CommandPending || st.Commands.NextID != 2 || stateWrites(b.fs) != writes {
		t.Errorf("fremdes Board hat geändert: %+v", st.Commands)
	}
	if got, _ := b.pollAs(" fw-box-1 ", "e", 0); !strings.Contains(got, "id=1\ntype=TEST\n") {
		t.Errorf("die eingestellte Box (Leerraum außen zählt nicht): %q", got)
	}
	// Ohne box_device_id bekommt jedes Board Befehle.
	b.configure(func(cfg *config.Config) { cfg.BoxDeviceID = "" })
	b.advance(200 * time.Second) // nach der Sperre von 120 s
	b.enqueue(model.CommandTest)
	if got, _ := b.pollAs("bastel-1", "f", 0); !strings.Contains(got, "id=2\ntype=TEST\n") {
		t.Errorf("ohne box_device_id: %q", got)
	}
}

// Die Box kennt schon höhere Nummern (der Server verlor seine Befehle): Der
// wartende bekommt eine neue, und auch ohne Befehl merkt sich der Server die
// Nummer (gesichert).
func TestPollRenumbers(t *testing.T) {
	b := newBench(t)
	b.enqueue(model.CommandTest)
	if got := b.pollBox("p", 50); got != "ok=1\nnonce=p\nid=51\ntype=TEST\nkeys_version=0\n" {
		t.Errorf("%q", got)
	}
	if b.current().ID != 51 || b.c.Store.State().Commands.NextID != 52 {
		t.Errorf("%+v", b.c.Store.State().Commands)
	}
	b.ackBox(url.Values{"id": {"51"}})
	b.pollBox("p", 80)
	if next := b.c.Store.State().Commands.NextID; next != 81 {
		t.Errorf("next_id %d nach der Nummer 80 der Box", next)
	}
	if n := stateWrites(b.fs); n == 0 {
		t.Error("nichts gesichert")
	}
}

// Ein Demo-Alarm (ALARM-Befehl) wird beim Ausliefern noch einmal gesichert:
// Hat sich der Demo-Stand seit dem Anlegen geändert oder meldet die Box nicht
// mehr genau einen Empfänger, bricht die Sicherung ihn ab – NONE, Eintrag
// cancelled mit dem Grund. Sonst geht er hinaus.
func TestDemoAlarmCancelledAfterModeChange(t *testing.T) {
	demoReady := func(t *testing.T) *bench {
		b := newBench(t)
		b.seed(8, anna, bert)
		b.update(func(d *model.State) {
			d.Demo = model.Demo{Enabled: true, KeyID: 1, Key: anna.Key, Label: anna.Label, ChangedAt: t0.Unix() - 100}
		})
		b.push(fwNew, "keys_version", "8", "keys_count", "1")
		b.enqueue(model.CommandAlarm)
		return b
	}
	t.Run("alles wie beim Anlegen: ausgeliefert", func(t *testing.T) {
		b := demoReady(t)
		if got := b.pollBox("a1", 0); got != "ok=1\nnonce=a1\nid=1\ntype=ALARM\nkeys_version=8\n" {
			t.Errorf("%q", got)
		}
	})
	for name, change := range map[string]func(b *bench){
		"Demo-Modus aus": func(b *bench) {
			b.update(func(d *model.State) { d.Demo = model.Demo{ChangedAt: t0.Unix()}; d.KeysVersion++ })
		},
		"aus und wieder ein": func(b *bench) {
			b.update(func(d *model.State) { d.Demo.ChangedAt = t0.Unix(); d.KeysVersion += 2 })
		},
		"die Box meldet zwei Empfänger": func(b *bench) { b.push(fwNew, "keys_version", "8", "keys_count", "2") },
		"die alte Firmware meldet zwei": func(b *bench) { b.push(fwOld, "keys_version", "8", "keys_count", "2") },
	} {
		t.Run(name+": abgebrochen, nicht ausgeliefert", func(t *testing.T) {
			b := demoReady(t)
			change(b)
			if got := b.pollBox("a1", 0); !strings.Contains(got, "\nid=0\ntype=NONE\n") {
				t.Errorf("%q", got)
			}
			cur := b.current()
			if cur.State != model.CommandCancelled || cur.CancelledBy != "Sicherung" ||
				cur.CancelReason != "Demo-Stand seit dem Anlegen geändert - nicht ausgeliefert." || cur.DeliveredAt != 0 {
				t.Errorf("%+v", cur)
			}
			e := b.events(model.EvCancelled)
			if len(e) != 1 || e[0].Str("command.cancel_reason") != cur.CancelReason || e[0].Str("command.type") != "ALARM" {
				t.Errorf("Verlauf %v", e)
			}
			if got := b.pollBox("a2", 0); !strings.Contains(got, "type=NONE") {
				t.Errorf("danach: %q", got)
			}
		})
	}
}

// last_command_id wie PHP max(0, (int)…).
func TestCommandID(t *testing.T) {
	for raw, want := range map[string]int{
		"": 0, "0": 0, "7": 7, " 12": 12, "50abc": 50, "-3": 0, "abc": 0, "1e3": 1000, "2147483647": 2147483647,
		"99999999999999999999": 9223372036854775807,
	} {
		if got := commandID(raw); got != want {
			t.Errorf("%q: %d, erwartet %d", raw, got, want)
		}
	}
}

// expectedBox: box_device_id leer = jedes Board; sonst genau dieses, ohne
// Leerraum außen.
func TestExpectedBox(t *testing.T) {
	for _, tc := range []struct {
		cfg, device string
		want        bool
	}{
		{"", "egal", true}, {"", "", true}, {"fw-box-1", "fw-box-1", true}, {" fw-box-1 ", "fw-box-1\n", true},
		{"fw-box-1", "bastel-1", false}, {"fw-box-1", "", false}, {"fw-box-1", "FW-BOX-1", false},
	} {
		if got := expectedBox(tc.cfg, tc.device); got != tc.want {
			t.Errorf("%q/%q: %v", tc.cfg, tc.device, got)
		}
	}
}

// FuzzCommandID: Was die Box als last_command_id schickt, wird nie zu einer
// negativen Nummer und ist sonst genau PHPs (int) (model.IntOf).
func FuzzCommandID(f *testing.F) {
	for _, s := range []string{"", "0", "50", "50abc", "-3", "1e3", "99999999999999999999", " 7", "\xff", "0x10"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		got, n := commandID(raw), model.IntOf(raw)
		switch {
		case got < 0:
			t.Fatalf("%q: %d", raw, got)
		case n <= 0 && got != 0, n > 0 && int64(got) != n:
			t.Fatalf("%q: %d, IntOf %d", raw, got, n)
		}
	})
}
