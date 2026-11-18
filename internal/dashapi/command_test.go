package dashapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fwalarm/internal/config"
	"fwalarm/internal/model"
)

// Befehle im Dashboard: enqueue, cancel und der Teil command von status –
// Antworten Byte für Byte wie PHP (dashboard_api.php 224-309).

// commandBench: der Prüfstand mit PHPs Standards für Befehle (Verfall nach
// 300 s, Sperre nach der Auslieferung 120 s).
func commandBench(t *testing.T) *bench {
	t.Helper()
	b := newBench(t)
	b.setCfg(func(c *config.Config) { c.PendingCommandTTLSeconds, c.DeliveredCommandLockSeconds = 300, 120 })
	return b
}

// quiet wartet, bis die Ablage alles gesichert hat – auch das Nachsichern der
// outbox nach einer Änderung (sonst erwischte ein Test, der danach die
// Platte anhält, womöglich dieses Sichern).
func (b *bench) quiet() {
	b.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.c.Store.Flush(ctx); err != nil {
		b.t.Fatal(err)
	}
}

// cur: der laufende Befehl im Schnappschuss.
func (b *bench) cur() *model.Command { return b.c.Store.State().Commands.Current }

// statusCommand: der Teil command der Antwort von status (roh).
func (b *bench) statusCommand(cookie string) string {
	b.t.Helper()
	got := b.statusOf(cookie)
	_, rest, ok := strings.Cut(got, `"command":`)
	end := strings.Index(rest, `,"demo":`)
	if !ok || end < 0 {
		b.t.Fatalf("status ohne command: %s", got)
	}
	return rest[:end]
}

// demoReady: Demo-Modus mit Anna, die Box hat die Demo-Liste übernommen
// (Status mit aktueller Version und genau einem Empfänger, frisch).
func (b *bench) demoReady() {
	b.t.Helper()
	b.seed(8, anna, bert)
	b.update(func(d *model.State) {
		d.Demo = model.Demo{Enabled: true, KeyID: 1, Key: anna.Key, Label: "Anna", ChangedAt: t0.Unix() - 100}
	})
	b.setBox(&model.BoxStatus{SeenAt: t0.Unix() - 5, Fields: model.Fields{{Name: "keys_version", Value: "8"}, {Name: "keys_count", Value: "1"}}})
}

// enqueue: Typen, Sperren und die Antworten.
func TestEnqueue(t *testing.T) {
	b := commandBench(t)
	admin := b.admin()
	at := t0.Unix()
	admin.want(fmt.Sprintf(`{"ok":true,"message":"Befehl angelegt.","command":{"id":1,"type":"TEST","state":"pending","created_at":%d,"created_by":"admin"}}`, at),
		"action", "enqueue", "type", "TEST")
	admin.want(`{"ok":false,"message":"Es wartet bereits ein Befehl (abbrechen oder warten)."}`, "action", "enqueue", "type", "TEST")
	e := b.events(model.EvCreated)
	if len(e) != 1 || e[0].Int("command.id") != 1 || e[0].Str("command.created_by") != "admin" {
		t.Errorf("Verlauf %v", e)
	}
	// Ausgeliefert, Quittung fehlt: 120 s Sperre, danach frei.
	b.update(func(d *model.State) { d.Deliver("fw-box-1", t0) })
	b.clk.Set(t0.Add(119 * time.Second))
	admin.want(`{"ok":false,"message":"Der letzte Befehl wurde an die Box ausgeliefert, ihre Bestätigung steht noch aus."}`, "action", "enqueue", "type", "TEST")
	b.clk.Set(t0.Add(120 * time.Second))
	admin.want(fmt.Sprintf(`{"ok":true,"message":"Befehl angelegt.","command":{"id":2,"type":"TEST","state":"pending","created_at":%d,"created_by":"admin"}}`, at+120),
		"action", "enqueue", "type", "test") // klein geschrieben gilt (strtoupper)
}

// Ein anderer Typ als TEST/ALARM: "Ungültiger Befehl." – ohne die Ablage
// anzufassen; großgeschrieben wird nur ASCII.
func TestEnqueueInvalidType(t *testing.T) {
	b := commandBench(t)
	admin := b.admin()
	b.quiet()
	before := stateWrites(b)
	for _, typ := range []string{"RESET", "", "teſt", "TEST ", "ALARM\n", "NONE"} {
		admin.want(`{"ok":false,"message":"Ungültiger Befehl."}`, "action", "enqueue", "type", typ)
	}
	if b.cur() != nil || stateWrites(b) != before {
		t.Errorf("angelegt oder gesichert: %+v", b.cur())
	}
}

// stateWrites: wie oft die Ablage bisher state.json geschrieben hat.
func stateWrites(b *bench) int {
	n := 0
	for _, call := range b.fs.Log() {
		if call.Op == "WriteFileSync" && len(call.Names) > 0 && call.Names[0] == filepath.Join(benchDir, "state.json.tmp") {
			n++
		}
	}
	return n
}

// Der Demo-Alarm über die Box (ALARM): nur mit bestätigtem Modus (409
// mode_changed sonst, nichts angelegt), nur im Demo-Modus und nur, wenn die
// Box die Demo-Liste bestätigt hat; der Befehl hält den Demo-Stand fest.
func TestEnqueueAlarm(t *testing.T) {
	t.Run("LIVE", func(t *testing.T) {
		b := commandBench(t)
		admin := b.admin()
		b.seed(2, anna)
		admin.want(`{"ok":false,"message":"Der Alarm über die Box geht nur im Demo-Modus. Im LIVE-Betrieb den REAL ALARM (direkt vom Server) benutzen."}`,
			"action", "enqueue", "type", "ALARM", "expect_demo", "0")
		code, got := admin.do("action", "enqueue", "type", "ALARM", "expect_demo", "1")
		if want := phpText(`{"ok":false,"code":"mode_changed","message":"Der Modus wurde inzwischen geändert (jetzt LIVE – Alarm ginge an ALLE). Es wurde NICHTS gesendet – bitte neu bestätigen.","demo":{"enabled":false,"label":"","key_id":0,"key_masked":"","changed_at":0}}`); code != 409 || got != want {
			t.Errorf("%d %s\nerwartet %s", code, got, want)
		}
		if b.cur() != nil {
			t.Error("angelegt")
		}
	})
	t.Run("Demo", func(t *testing.T) {
		b := commandBench(t)
		admin := b.admin()
		b.demoReady()
		demo := fmt.Sprintf(`{"enabled":true,"label":"Anna","key_id":1,"key_masked":"%s","changed_at":%d}`, model.MaskKey(anna.Key), t0.Unix()-100)
		for name, kv := range map[string][]string{
			"ohne expect_demo":  {"action", "enqueue", "type", "ALARM"},
			"LIVE bestätigt":    {"action", "enqueue", "type", "ALARM", "expect_demo", "0"},
			"anderer Empfänger": {"action", "enqueue", "type", "ALARM", "expect_demo", "2"},
		} {
			code, got := admin.do(kv...)
			message := "Der Modus wurde inzwischen geändert (jetzt DEMO – nur an „Anna“). Es wurde NICHTS gesendet – bitte neu bestätigen."
			if name == "ohne expect_demo" {
				message = "Die Oberfläche ist veraltet (Modus nicht bestätigt) – es wurde NICHTS gesendet. Seite neu laden."
			}
			if want := phpText(`{"ok":false,"code":"mode_changed","message":"` + message + `","demo":` + demo + `}`); code != 409 || got != want {
				t.Errorf("%s: %d %s\nerwartet %s", name, code, got, want)
			}
		}
		if b.cur() != nil {
			t.Fatal("angelegt")
		}
		// Box mit alter Version / zwei Empfängern / veraltet: gesperrt.
		sync := "Die Box hat die Demo-Liste noch nicht übernommen oder ist offline – warten, bis das Demo-Banner die Übernahme bestätigt."
		for _, box := range []*model.BoxStatus{
			{SeenAt: t0.Unix() - 5, Fields: model.Fields{{Name: "keys_version", Value: "7"}, {Name: "keys_count", Value: "1"}}},
			{SeenAt: t0.Unix() - 5, Fields: model.Fields{{Name: "keys_version", Value: "8"}, {Name: "keys_count", Value: "2"}}},
			{SeenAt: t0.Unix() - 181, Fields: model.Fields{{Name: "keys_version", Value: "8"}, {Name: "keys_count", Value: "1"}}},
			nil,
		} {
			b.setBox(box)
			admin.want(`{"ok":false,"message":"`+sync+`"}`, "action", "enqueue", "type", "ALARM", "expect_demo", "1")
		}
		b.setBox(&model.BoxStatus{SeenAt: t0.Unix() - 5, Fields: model.Fields{{Name: "keys_version", Value: "8"}, {Name: "keys_count", Value: "1"}}})
		admin.want(fmt.Sprintf(`{"ok":true,"message":"Befehl angelegt.","command":{"id":1,"type":"ALARM","state":"pending","created_at":%d,"created_by":"admin","demo_changed_at":%d}}`,
			t0.Unix(), t0.Unix()-100), "action", "enqueue", "type", "ALARM", "expect_demo", "1")
		if c := b.cur(); c == nil || c.DemoChangedAt != t0.Unix()-100 {
			t.Errorf("%+v", c)
		}
	})
}

// Scheitert das Sichern: 500 code storage, kein Befehl angelegt.
func TestEnqueueStorage(t *testing.T) {
	b := commandBench(t)
	admin := b.admin()
	b.fs.Fail(filepath.Join(benchDir, "state.json.tmp"), errors.New("Platte voll"))
	code, got := admin.do("action", "enqueue", "type", "TEST")
	if want := phpText(`{"ok":false,"code":"storage","message":"Fehler: Datenordner nicht beschreibbar (Rechte des Web-Benutzers auf dem Server prüfen)."}`); code != 500 || got != want {
		t.Errorf("%d %s", code, got)
	}
	if b.cur() != nil || len(b.events(model.EvCreated)) != 0 {
		t.Errorf("angelegt: %+v", b.cur())
	}
}

// cancel: nur ein wartender Befehl; ohne Grund – auch im Verlauf, den log_view
// genau so zeigt wie PHP (ohne message).
func TestCancel(t *testing.T) {
	b := commandBench(t)
	admin := b.admin()
	admin.want(`{"ok":false,"message":"Kein wartender Befehl zum Abbrechen."}`, "action", "cancel")
	admin.do("action", "enqueue", "type", "TEST")
	b.clk.Set(t0.Add(10 * time.Second))
	at := t0.Unix()
	admin.want(fmt.Sprintf(`{"ok":true,"message":"Befehl abgebrochen.","command":{"id":1,"type":"TEST","state":"cancelled","created_at":%d,"created_by":"admin","cancelled_at":%d,"cancelled_by":"admin"}}`, at, at+10),
		"action", "cancel")
	admin.want(`{"ok":false,"message":"Kein wartender Befehl zum Abbrechen."}`, "action", "cancel")
	// PHPs Antwort von log_view auf diesen Eintrag (nachgesehen mit
	// log_event_view in PHP 8.3): kein "message", wo kein Grund ist.
	rec := b.serve(req("GET", "action=log_view&filter=system", nil, admin.cookie))
	want := fmt.Sprintf(`{"at":%d,"event":"cancelled","id":1,"type":"TEST","by":"admin"}`, at+10)
	if !strings.Contains(rec.Body.String(), `"entries":[`+want+`,`) {
		t.Errorf("log_view %s\nerwartet zuerst %s", rec.Body.String(), want)
	}
	// Ein ausgelieferter ist nicht mehr abzubrechen.
	b.clk.Set(t0.Add(20 * time.Second))
	admin.do("action", "enqueue", "type", "TEST")
	b.update(func(d *model.State) { d.Deliver("fw-box-1", b.clk.Now()) })
	admin.want(`{"ok":false,"message":"Kein wartender Befehl zum Abbrechen."}`, "action", "cancel")
	if b.cur().State != model.CommandDelivered {
		t.Errorf("%+v", b.cur())
	}
}

// Der Teil command von status: null ohne Befehl; der Befehl mit den Feldern,
// die PHP zeigte, für jede Rolle; ein zu lange wartender erscheint als
// verfallen, und das wird nebenher gesichert – die Antwort wartet nicht
// darauf, auch nicht auf eine hängende Platte, und höchstens ein Sichern
// läuft zur Zeit.
func TestCommandStatusPart(t *testing.T) {
	b := commandBench(t)
	admin, reader := b.admin(), b.reader()
	if got := b.statusCommand(admin.cookie); got != "null" {
		t.Errorf("ohne Befehl: %s", got)
	}
	admin.do("action", "enqueue", "type", "TEST")
	at := t0.Unix()
	pending := fmt.Sprintf(`{"id":1,"type":"TEST","state":"pending","created_at":%d,"created_by":"admin"}`, at)
	for _, p := range []person{admin, reader} {
		if got := b.statusCommand(p.cookie); got != pending {
			t.Errorf("wartend: %s", got)
		}
	}
	b.clk.Set(t0.Add(300 * time.Second))
	if got := b.statusCommand(admin.cookie); got != pending {
		t.Errorf("genau 300 s: %s", got)
	}

	// 301 s, die Platte hängt: sofort als verfallen gezeigt, gesichert erst,
	// wenn sie wieder geht – und nur einmal.
	b.quiet()
	release := b.fs.Hang(filepath.Join(benchDir, "state.json.tmp"))
	b.clk.Set(t0.Add(301 * time.Second))
	expired := fmt.Sprintf(`{"id":1,"type":"TEST","state":"expired","created_at":%d,"created_by":"admin","expired_at":%d}`, at, at+301)
	for i := range 3 {
		start := time.Now()
		if got := b.statusCommand(reader.cookie); got != expired {
			t.Errorf("verfallen %d: %s", i, got)
		}
		if took := time.Since(start); took > 2*time.Second { // mit Warten auf das Sichern: 5 s (Frist der Oberfläche)
			t.Errorf("status wartete %v", took)
		}
	}
	if b.cur().State != model.CommandPending {
		t.Error("vor dem Sichern schon verfallen")
	}
	release()
	if !b.c.Bg.Wait(10 * time.Second) {
		t.Fatal("Sichern nebenher endet nicht")
	}
	if c := b.cur(); c.State != model.CommandExpired || c.ExpiredAt != at+301 {
		t.Errorf("gesichert: %+v", c)
	}
	if n := len(b.events(model.EvExpired)); n != 1 {
		t.Errorf("%d Einträge expired", n)
	}
	if got := b.statusCommand(admin.cookie); got != expired {
		t.Errorf("danach: %s", got)
	}
	// Danach ist das Dashboard frei.
	if code, got := admin.do("action", "enqueue", "type", "TEST"); code != http.StatusOK || !strings.Contains(got, `"ok":true`) {
		t.Errorf("danach anlegen: %d %s", code, got)
	}
}

// Das Verfallen nebenher: höchstens EIN Sichern zur Zeit, wie viele Tabs auch
// fragen – und nach einem gescheiterten frühestens 5 s (Uhr des Core) später
// der nächste Versuch (springt die Uhr zurück, ist die Pause um). Sonst
// begänne bei kaputter Platte jeder Abruf von status einen neuen. Gezählt
// werden die Versuche, state.json zu schreiben.
func TestExpireBesideOneAtATime(t *testing.T) {
	b := commandBench(t)
	admin := b.admin()
	admin.do("action", "enqueue", "type", "TEST")
	b.quiet()
	tmp := filepath.Join(benchDir, "state.json.tmp")
	b.fs.Fail(tmp, errors.New("Platte voll")) // jeder Versuch scheitert …
	release := b.fs.Hang(tmp)                 // … der erste erst, wenn der Test es will
	b.clk.Set(t0.Add(301 * time.Second))
	base := stateWrites(b)
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.serve(req("GET", "action=status", nil, admin.cookie))
		}()
	}
	wg.Wait()
	for deadline := time.Now().Add(5 * time.Second); stateWrites(b) == base; {
		if time.Now().After(deadline) {
			t.Fatal("kein Versuch begonnen")
		}
		time.Sleep(5 * time.Millisecond)
	}
	for range 5 {
		b.statusCommand(admin.cookie) // während der eine hängt
	}
	release()
	settle := func() {
		t.Helper()
		if !b.c.Bg.Wait(10 * time.Second) {
			t.Fatal("Arbeit nebenher endet nicht")
		}
	}
	settle()
	if n := stateWrites(b) - base; n != 1 {
		t.Fatalf("%d Versuche zugleich, erwartet einer", n)
	}
	// Gescheitert: die nächsten 5 s kein neuer Versuch.
	for range 5 {
		b.statusCommand(admin.cookie)
	}
	b.clk.Advance(4999 * time.Millisecond)
	b.statusCommand(admin.cookie)
	settle()
	if n := stateWrites(b) - base; n != 1 {
		t.Errorf("%d Versuche vor Ablauf der Pause", n)
	}
	b.clk.Advance(time.Millisecond)
	b.statusCommand(admin.cookie)
	settle()
	if n := stateWrites(b) - base; n != 2 {
		t.Errorf("nach der Pause %d Versuche, erwartet 2", n)
	}
	// Springt die Uhr zurück (hier von 306 s auf 302 s, der Befehl ist noch
	// zu alt), ist die Pause um – sonst wartete das Verfallen, bis die Uhr den
	// alten Stand wieder erreicht.
	b.clk.Set(t0.Add(302 * time.Second))
	b.statusCommand(admin.cookie)
	settle()
	if n := stateWrites(b) - base; n != 3 {
		t.Errorf("nach dem Rücksprung der Uhr %d Versuche, erwartet 3", n)
	}
	// Die Platte geht wieder: nach der nächsten Pause gesichert, einmal.
	b.fs.Heal()
	b.clk.Advance(5 * time.Second)
	b.statusCommand(admin.cookie)
	settle()
	if c := b.cur(); c.State != model.CommandExpired || len(b.events(model.EvExpired)) != 1 {
		t.Errorf("nach der Reparatur: %+v, %d Einträge", c, len(b.events(model.EvExpired)))
	}
}

// asciiUpper wie PHPs strtoupper: nur a-z.
func TestASCIIUpper(t *testing.T) {
	for in, want := range map[string]string{"test": "TEST", "Alarm": "ALARM", "teſt": "TEſT", "äb": "äB", "": ""} {
		if got := asciiUpper(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}
