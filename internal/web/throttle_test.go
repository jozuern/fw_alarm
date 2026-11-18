package web

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fwalarm/internal/clock"
	"fwalarm/internal/config"
	"fwalarm/internal/core"
	"fwalarm/internal/model"
	"fwalarm/internal/store"
	"fwalarm/internal/store/storetest"
)

// Fester Zeitpunkt der Tests: 17.11.2026 10:00 UTC.
var t0 = time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

// storeCore: ein Core mit einer Ablage im Speicher (FaultFS) und einer
// gestellten Uhr.
func storeCore(t *testing.T) (*core.Core, *clock.Fake, *storetest.FaultFS) {
	t.Helper()
	clk := clock.NewFake(t0)
	fsys := storetest.New()
	log := slog.New(slog.DiscardHandler)
	st, _, err := store.Open(filepath.FromSlash("/daten"), fsys, clk, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := st.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	cfg := &config.Config{}
	return &core.Core{Cfg: func() *config.Config { return cfg }, Store: st, Clock: clk, Log: log, Version: "test"}, clk, fsys
}

func throttleOf(c *core.Core, bucket string) map[string]model.ThrottleEntry {
	if bucket == "login" {
		return c.Store.Runtime().Throttle.Login
	}
	return c.Store.Runtime().Throttle.Mute
}

// PHP: throttle_hit. Jeder Versuch zählt, bis max erreicht ist; dann ist
// der Absender gesperrt (ohne weiterzuzählen), bis sein Fenster abgelaufen
// ist – gemessen ab dem ersten Versuch, abgelaufen erst NACH window
// Sekunden. Danach zählt er neu.
func TestThrottleWindow(t *testing.T) {
	c, clk, _ := storeCore(t)
	const max, window = 3, 900 * time.Second
	ThrottleHit(c, "login", "198.51.100.7", max, window) // läuft mit dem ersten Versuch unten ab
	for i := 1; i <= max; i++ {
		if h := ThrottleHit(c, "login", "203.0.113.5", max, window); h.Blocked || h.Count != i {
			t.Fatalf("Versuch %d: %+v", i, h)
		}
		clk.Advance(10 * time.Second)
	}
	if h := ThrottleHit(c, "login", "203.0.113.5", max, window); !h.Blocked || h.Count != max {
		t.Fatalf("über max: %+v", h)
	}
	// Ein anderer Absender und die andere Bremse zählen für sich.
	if h := ThrottleHit(c, "login", "203.0.113.6", max, window); h.Blocked || h.Count != 1 {
		t.Errorf("anderer Absender: %+v", h)
	}
	if h := ThrottleHit(c, "mute", "203.0.113.5", max, window); h.Blocked || h.Count != 1 {
		t.Errorf("andere Bremse: %+v", h)
	}
	// Genau window Sekunden nach dem ersten Versuch: noch gesperrt.
	clk.Set(t0.Add(window))
	if h := ThrottleHit(c, "login", "203.0.113.5", max, window); !h.Blocked || h.Count != max {
		t.Errorf("nach genau window: %+v", h)
	}
	if e := throttleOf(c, "login")["203.0.113.5"]; e.Count != max || e.FirstAt != t0.Unix() {
		t.Errorf("gesperrt wird nicht weitergezählt: %+v", e)
	}
	// Eine Sekunde später: abgelaufen, neu gezählt mit neuem Anfang.
	clk.Set(t0.Add(window + time.Second))
	if h := ThrottleHit(c, "login", "203.0.113.5", max, window); h.Blocked || h.Count != 1 {
		t.Errorf("nach Ablauf: %+v", h)
	}
	if e := throttleOf(c, "login")["203.0.113.5"]; e.Count != 1 || e.FirstAt != t0.Unix()+901 {
		t.Errorf("neuer Eintrag: %+v", e)
	}
	// Abgelaufene Einträge anderer Absender werden dabei aufgeräumt, laufende
	// bleiben.
	if _, ok := throttleOf(c, "login")["198.51.100.7"]; ok {
		t.Error("abgelaufener Eintrag blieb stehen")
	}
	if e := throttleOf(c, "login")["203.0.113.6"]; e.Count != 1 {
		t.Errorf("laufender Eintrag: %+v", e)
	}
	// Die andere Bremse räumt ein Treffer hier nicht auf.
	if _, ok := throttleOf(c, "mute")["203.0.113.5"]; !ok {
		t.Error("ein Treffer der Bremse login räumte die Bremse mute auf")
	}
	// max 0: wie in PHP gleich gesperrt, ohne Eintrag.
	if h := ThrottleHit(c, "login", "198.51.100.9", 0, window); !h.Blocked || h.Count != 0 {
		t.Errorf("max 0: %+v", h)
	}
	if _, ok := throttleOf(c, "login")["198.51.100.9"]; ok {
		t.Error("max 0: gezählt")
	}
}

// Höchstens 500 gezählte Absender je Bremse: ein neuer darüber wird weder
// gezählt noch gesperrt (kein Sammel-Zähler, den ein Angreifer füllen und
// so den Admin aussperren könnte). Die schon gezählten zählen weiter; ist
// Platz frei geworden, zählt auch ein neuer wieder.
func TestThrottleCap(t *testing.T) {
	c, clk, _ := storeCore(t)
	const max, window = 2, 900 * time.Second
	for i := range 500 {
		if h := ThrottleHit(c, "mute", fmt.Sprintf("10.0.%d.%d", i/250, i%250), max, window); h.Blocked || h.Count != 1 {
			t.Fatalf("Absender %d: %+v", i, h)
		}
	}
	clk.Advance(time.Minute)
	for _, who := range []string{"10.9.9.9", "2001:db8::/64"} {
		for range 5 {
			if h := ThrottleHit(c, "mute", who, max, window); h.Blocked || h.Count != 0 {
				t.Fatalf("Absender 501 (%s): %+v", who, h)
			}
		}
	}
	if n := len(throttleOf(c, "mute")); n != 500 {
		t.Errorf("%d gezählte Absender", n)
	}
	if _, ok := throttleOf(c, "mute")["10.9.9.9"]; ok {
		t.Error("Absender 501 wurde gezählt")
	}
	// Ein schon gezählter Absender zählt weiter und wird gesperrt.
	ThrottleHit(c, "mute", "10.0.0.0", max, window)
	if h := ThrottleHit(c, "mute", "10.0.0.0", max, window); !h.Blocked {
		t.Errorf("gezählter Absender bei voller Bremse: %+v", h)
	}
	// Die andere Bremse ist nicht voll.
	if h := ThrottleHit(c, "login", "10.9.9.9", max, window); h.Count != 1 {
		t.Errorf("andere Bremse: %+v", h)
	}
	// Nach dem Fenster der ersten 500 ist wieder Platz.
	clk.Set(t0.Add(window + time.Second))
	if h := ThrottleHit(c, "mute", "10.9.9.9", max, window); h.Blocked || h.Count != 1 {
		t.Errorf("nach Ablauf: %+v", h)
	}
}

// Prüfen und Zählen ist ein Schritt: N gleichzeitige Versuche bekommen
// nicht N Plätze unter max.
func TestThrottleConcurrent(t *testing.T) {
	c, _, _ := storeCore(t)
	const max = 5
	var wg sync.WaitGroup
	var mu sync.Mutex
	counted := 0
	for range 50 {
		wg.Go(func() {
			if h := ThrottleHit(c, "login", "203.0.113.5", max, time.Hour); !h.Blocked {
				mu.Lock()
				counted++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if counted != max {
		t.Errorf("%d Versuche durchgelassen, erwartet %d", counted, max)
	}
}

// ThrottleClear (PHP: throttle_clear) nimmt nur diesen Absender aus dieser
// Bremse; gibt es keinen Eintrag, ändert sich nichts.
func TestThrottleClear(t *testing.T) {
	c, _, _ := storeCore(t)
	for range 3 {
		ThrottleHit(c, "login", "203.0.113.5", 3, time.Hour)
	}
	ThrottleHit(c, "login", "203.0.113.6", 3, time.Hour)
	ThrottleHit(c, "mute", "203.0.113.5", 3, time.Hour)
	ThrottleClear(c, "login", "203.0.113.5")
	if h := ThrottleHit(c, "login", "203.0.113.5", 3, time.Hour); h.Blocked || h.Count != 1 {
		t.Errorf("nach Clear: %+v", h)
	}
	if throttleOf(c, "login")["203.0.113.6"].Count != 1 || throttleOf(c, "mute")["203.0.113.5"].Count != 1 {
		t.Error("Clear traf andere Absender oder die andere Bremse")
	}
	before := c.Store.Runtime()
	ThrottleClear(c, "login", "198.51.100.1")
	if c.Store.Runtime() != before {
		t.Error("Clear ohne Eintrag änderte die Laufzeitdaten")
	}
}

// Ein gesperrter oder nicht gezählter Versuch ändert nichts – auch nichts,
// was gesichert werden müsste (unter einem Angriff schriebe der Server
// sonst bei jedem Aufruf runtime.json).
func TestThrottleBlockedChangesNothing(t *testing.T) {
	c, _, _ := storeCore(t)
	for range 2 {
		ThrottleHit(c, "mute", "203.0.113.5", 2, time.Hour)
	}
	before := c.Store.Runtime()
	for range 10 {
		if h := ThrottleHit(c, "mute", "203.0.113.5", 2, time.Hour); !h.Blocked {
			t.Fatalf("nicht gesperrt: %+v", h)
		}
	}
	if c.Store.Runtime() != before {
		t.Error("ein gesperrter Versuch änderte die Laufzeitdaten")
	}
}

// Ein Versuch wartet nie auf die Platte: Auch wenn runtime.json hängt,
// zählt die Bremse sofort.
func TestThrottleNeverWaitsForDisk(t *testing.T) {
	c, _, fsys := storeCore(t)
	release := fsys.Hang(filepath.FromSlash("/daten/runtime.json.tmp"))
	defer release()
	done := make(chan Hit, 1)
	go func() {
		ThrottleHit(c, "login", "203.0.113.5", 3, time.Hour)
		done <- ThrottleHit(c, "login", "203.0.113.5", 3, time.Hour)
	}()
	select {
	case h := <-done:
		if h.Count != 2 {
			t.Errorf("%+v", h)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("die Bremse wartet auf die Platte")
	}
	// Die Platte hing dabei wirklich: Sichern wird nicht fertig.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := c.Store.Flush(ctx); err == nil {
		t.Error("runtime.json wurde trotz hängender Platte gesichert – der Test prüft nichts")
	}
}

// Es gibt genau die Bremsen "login" und "mute"; eine andere ist ein Fehler
// im Programm.
func TestThrottleUnknownBucket(t *testing.T) {
	c, _, _ := storeCore(t)
	for _, call := range []func(){
		func() { ThrottleHit(c, "Login", "203.0.113.5", 3, time.Hour) },
		func() { ThrottleHit(c, "", "203.0.113.5", 3, time.Hour) },
		func() { ThrottleClear(c, "mute_throttle", "203.0.113.5") },
	} {
		func() {
			defer func() {
				v := recover()
				if v == nil || !strings.Contains(fmt.Sprint(v), "Bremse") {
					t.Errorf("kein klarer Abbruch: %v", v)
				}
			}()
			call()
		}()
	}
}
