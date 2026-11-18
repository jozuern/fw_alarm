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

// PHP: client_throttle_key – IPv4 einzeln, IPv6 als ganzes /64,
// IPv4-in-IPv6 wie die IPv4; was keine Adresse ist, bleibt, wie es ist.
func TestThrottleKey(t *testing.T) {
	for in, want := range map[string]string{
		"::ffff:1.2.3.4":            "1.2.3.4",
		"::FFFF:1.2.3.4":            "1.2.3.4",
		"1.2.3.4":                   "1.2.3.4",
		"2001:db8:1:2:3:4:5:6":      "2001:db8:1:2::/64",
		"2001:DB8:1:2:ffff::9":      "2001:db8:1:2::/64",
		"2001:db8:1:2::":            "2001:db8:1:2::/64",
		"2001:0:0:1::5":             "2001:0:0:1::/64",
		"2001:db8::1":               "2001:db8::/64",
		"::1":                       "::/64",
		"fe80::1:2:3:4":             "fe80::/64",
		"1:2:3:4:5:6:7:8":           "1:2:3:4::/64",
		"unknown":                   "unknown",
		"fe80::1%eth0":              "fe80::1%eth0",
		"":                          "",
		"::ffff:1.2.3.4:5":          "::ffff:1.2.3.4:5",
		"2001:db8:aaaa:bbbb:cccc::": "2001:db8:aaaa:bbbb::/64",
	} {
		if got := ThrottleKey(in); got != want {
			t.Errorf("ThrottleKey(%q) = %q, erwartet %q", in, got, want)
		}
	}
	// Zwei Adressen desselben /64 zählen als ein Absender, ein anderes /64 nicht.
	if ThrottleKey("2001:db8:1:2::a") != ThrottleKey("2001:db8:1:2:ffff:ffff:ffff:ffff") ||
		ThrottleKey("2001:db8:1:2::a") == ThrottleKey("2001:db8:1:3::a") {
		t.Error("das /64 wird nicht zusammengefasst")
	}
}

// PHP: throttle_hit. Jeder Versuch zählt, bis limit erreicht ist; dann ist
// der Absender gesperrt (ohne weiterzuzählen), bis sein Fenster abgelaufen
// ist – gemessen ab dem ersten Versuch, abgelaufen erst NACH window
// Sekunden. Danach zählt er neu.
func TestThrottleWindow(t *testing.T) {
	c, clk, _ := storeCore(t)
	const limit, window = 3, 900 * time.Second
	ThrottleHit(c, "login", "198.51.100.7", limit, window) // läuft mit dem ersten Versuch unten ab
	for i := 1; i <= limit; i++ {
		if h := ThrottleHit(c, "login", "203.0.113.5", limit, window); h.Blocked || h.Count != i {
			t.Fatalf("Versuch %d: %+v", i, h)
		}
		clk.Advance(10 * time.Second)
	}
	if h := ThrottleHit(c, "login", "203.0.113.5", limit, window); !h.Blocked || h.Count != limit {
		t.Fatalf("über limit: %+v", h)
	}
	// Ein anderer Absender und die andere Bremse zählen für sich.
	if h := ThrottleHit(c, "login", "203.0.113.6", limit, window); h.Blocked || h.Count != 1 {
		t.Errorf("anderer Absender: %+v", h)
	}
	if h := ThrottleHit(c, "mute", "203.0.113.5", limit, window); h.Blocked || h.Count != 1 {
		t.Errorf("andere Bremse: %+v", h)
	}
	// Genau window Sekunden nach dem ersten Versuch: noch gesperrt.
	clk.Set(t0.Add(window))
	if h := ThrottleHit(c, "login", "203.0.113.5", limit, window); !h.Blocked || h.Count != limit {
		t.Errorf("nach genau window: %+v", h)
	}
	if e := throttleOf(c, "login")["203.0.113.5"]; e.Count != limit || e.FirstAt != t0.Unix() {
		t.Errorf("gesperrt wird nicht weitergezählt: %+v", e)
	}
	// Eine Sekunde später: abgelaufen, neu gezählt mit neuem Anfang.
	clk.Set(t0.Add(window + time.Second))
	if h := ThrottleHit(c, "login", "203.0.113.5", limit, window); h.Blocked || h.Count != 1 {
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
	// limit 0: wie in PHP gleich gesperrt, ohne Eintrag.
	if h := ThrottleHit(c, "login", "198.51.100.9", 0, window); !h.Blocked || h.Count != 0 {
		t.Errorf("limit 0: %+v", h)
	}
	if _, ok := throttleOf(c, "login")["198.51.100.9"]; ok {
		t.Error("limit 0: gezählt")
	}
}

// Ein Eintrag, dessen Anfang nach jetzt liegt (die Uhr ist zurückgesprungen,
// etwa ein Start mit falscher Uhr und dann NTP), gilt als abgelaufen –
// bewusst anders als PHP, dort bliebe er so lange gesperrt, wie der Sprung
// war, und hielte die Obergrenze voll. Der nächste gezählte Versuch räumt
// ihn weg.
func TestThrottleEntryFromTheFuture(t *testing.T) {
	c, clk, _ := storeCore(t)
	const limit, window = 3, 900 * time.Second
	for range limit + 1 {
		ThrottleHit(c, "login", "203.0.113.5", limit, window)
	}
	if h := ThrottleHit(c, "login", "203.0.113.5", limit, window); !h.Blocked {
		t.Fatalf("nicht gesperrt: %+v", h)
	}
	clk.Set(t0.Add(-2 * time.Hour))
	if h := ThrottleHit(c, "login", "203.0.113.5", limit, window); h.Blocked || h.Count != 1 {
		t.Errorf("nach dem Rücksprung: %+v", h)
	}
	if e := throttleOf(c, "login")["203.0.113.5"]; e.Count != 1 || e.FirstAt != t0.Add(-2*time.Hour).Unix() {
		t.Errorf("neues Fenster: %+v", e)
	}

	// 500 Einträge aus der Zukunft halten die Obergrenze nicht voll.
	clk.Set(t0)
	for i := range 500 {
		ThrottleHit(c, "mute", fmt.Sprintf("10.0.%d.%d", i/250, i%250), limit, window)
	}
	clk.Set(t0.Add(-2 * time.Hour))
	if h := ThrottleHit(c, "mute", "10.9.9.9", limit, window); h.Blocked || h.Count != 1 {
		t.Errorf("neuer Absender bei 500 Einträgen aus der Zukunft: %+v", h)
	}
	if m := throttleOf(c, "mute"); len(m) != 1 {
		t.Errorf("Einträge aus der Zukunft nicht weggeräumt: %d übrig", len(m))
	}
	// Auch die Vorab-Prüfung (ohne Zählen) sieht einen Eintrag aus der Zukunft
	// nicht als gesperrt.
	clk.Set(t0)
	for range limit {
		ThrottleHit(c, "mute", "198.51.100.3", limit, window)
	}
	clk.Set(t0.Add(-time.Second))
	before := c.Store.Runtime()
	if h := ThrottleHit(c, "mute", "198.51.100.3", limit, window); h.Blocked || h.Count != 1 {
		t.Errorf("eine Sekunde zurück: %+v", h)
	}
	if c.Store.Runtime() == before {
		t.Error("der neue Versuch wurde nicht gezählt")
	}
}

// steppedClock ist die gestellte Uhr mit einem Einschub: Beim nächsten
// Ablesen nach arm springt die Uhr um eine Sekunde vor, between läuft (ein
// anderer Versuch, der schon die neue Sekunde sieht) – geliefert wird aber
// die Zeit von VOR dem Sprung. So liegt zwischen dem ersten Ablesen eines
// Versuchs und seiner Entscheidung unter dem Halter genau eine
// Sekundengrenze, und ein anderer Versuch war dazwischen – ohne Schlafen,
// ohne echte Gleichzeitigkeit.
type steppedClock struct {
	*clock.Fake
	mu      sync.Mutex
	between func()
}

func (s *steppedClock) arm(between func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.between = between
}

func (s *steppedClock) Now() time.Time {
	s.mu.Lock()
	fn := s.between
	s.between = nil
	s.mu.Unlock()
	now := s.Fake.Now()
	if fn != nil {
		s.Fake.Advance(time.Second)
		fn()
	}
	return now
}

// Zwei Versuche über eine Sekundengrenze: Versuch A liest die Uhr in Sekunde
// S, bevor er den Halter bekommt; Versuch B liest S+1 und legt seinen
// Eintrag zuerst an. Entscheidet A nach seiner alten Zeit, sähe er B's
// Eintrag als "aus der Zukunft" und räumte ihn weg. Unter dem Halter zählt
// deshalb eine dort abgelesene Zeit: A zählt IN B's Eintrag, ein Absender
// an der Grenze bleibt gesperrt, Einträge anderer aus S+1 bleiben stehen.
func TestThrottleSecondBoundary(t *testing.T) {
	const limit, window = 3, 900 * time.Second
	setup := func(t *testing.T) (*core.Core, *steppedClock) {
		c, clk, _ := storeCore(t)
		sc := &steppedClock{Fake: clk}
		c.Clock = sc // die Ablage liest ihre eigene Uhr (clk) – nur web sieht den Einschub
		return c, sc
	}
	s1 := t0.Add(time.Second).Unix()

	t.Run("A zählt in den Eintrag von B", func(t *testing.T) {
		c, sc := setup(t)
		sc.arm(func() { ThrottleHit(c, "login", "203.0.113.5", limit, window) })
		if h := ThrottleHit(c, "login", "203.0.113.5", limit, window); h.Blocked || h.Count != 2 {
			t.Errorf("A: %+v, erwartet Count 2", h)
		}
		if e := throttleOf(c, "login")["203.0.113.5"]; e.Count != 2 || e.FirstAt != s1 {
			t.Errorf("Eintrag %+v, erwartet {2 %d}", e, s1)
		}
	})
	t.Run("an der Grenze bleibt gesperrt", func(t *testing.T) {
		c, sc := setup(t)
		sc.arm(func() {
			for range limit {
				ThrottleHit(c, "login", "203.0.113.5", limit, window)
			}
		})
		if h := ThrottleHit(c, "login", "203.0.113.5", limit, window); !h.Blocked || h.Count != limit {
			t.Errorf("A: %+v, erwartet gesperrt mit %d", h, limit)
		}
		if e := throttleOf(c, "login")["203.0.113.5"]; e.Count != limit || e.FirstAt != s1 {
			t.Errorf("Eintrag %+v, erwartet {%d %d}", e, limit, s1)
		}
	})
	t.Run("Eintrag eines anderen bleibt", func(t *testing.T) {
		c, sc := setup(t)
		sc.arm(func() { ThrottleHit(c, "mute", "198.51.100.4", limit, window) })
		if h := ThrottleHit(c, "mute", "203.0.113.5", limit, window); h.Blocked || h.Count != 1 {
			t.Errorf("A: %+v", h)
		}
		m := throttleOf(c, "mute")
		if e := m["198.51.100.4"]; e.Count != 1 || e.FirstAt != s1 {
			t.Errorf("Eintrag des anderen %+v, erwartet {1 %d}", e, s1)
		}
		if e := m["203.0.113.5"]; e.Count != 1 || e.FirstAt != s1 {
			t.Errorf("Eintrag von A %+v, erwartet {1 %d} (Zeit unter dem Halter)", e, s1)
		}
	})
}

// Höchstens 500 gezählte Absender je Bremse: ein neuer darüber wird weder
// gezählt noch gesperrt (kein Sammel-Zähler, den ein Angreifer füllen und
// so den Admin aussperren könnte). Die schon gezählten zählen weiter; ist
// Platz frei geworden, zählt auch ein neuer wieder.
func TestThrottleCap(t *testing.T) {
	c, clk, _ := storeCore(t)
	const limit, window = 2, 900 * time.Second
	for i := range 500 {
		if h := ThrottleHit(c, "mute", fmt.Sprintf("10.0.%d.%d", i/250, i%250), limit, window); h.Blocked || h.Count != 1 {
			t.Fatalf("Absender %d: %+v", i, h)
		}
	}
	clk.Advance(time.Minute)
	for _, who := range []string{"10.9.9.9", "2001:db8::/64"} {
		for range 5 {
			if h := ThrottleHit(c, "mute", who, limit, window); h.Blocked || h.Count != 0 {
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
	ThrottleHit(c, "mute", "10.0.0.0", limit, window)
	if h := ThrottleHit(c, "mute", "10.0.0.0", limit, window); !h.Blocked {
		t.Errorf("gezählter Absender bei voller Bremse: %+v", h)
	}
	// Die andere Bremse ist nicht voll.
	if h := ThrottleHit(c, "login", "10.9.9.9", limit, window); h.Count != 1 {
		t.Errorf("andere Bremse: %+v", h)
	}
	// Nach dem Fenster der ersten 500 ist wieder Platz.
	clk.Set(t0.Add(window + time.Second))
	if h := ThrottleHit(c, "mute", "10.9.9.9", limit, window); h.Blocked || h.Count != 1 {
		t.Errorf("nach Ablauf: %+v", h)
	}
}

// Prüfen und Zählen ist ein Schritt: N gleichzeitige Versuche bekommen
// nicht N Plätze unter limit.
func TestThrottleConcurrent(t *testing.T) {
	c, _, _ := storeCore(t)
	const limit = 5
	var wg sync.WaitGroup
	var mu sync.Mutex
	counted := 0
	for range 50 {
		wg.Go(func() {
			if h := ThrottleHit(c, "login", "203.0.113.5", limit, time.Hour); !h.Blocked {
				mu.Lock()
				counted++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if counted != limit {
		t.Errorf("%d Versuche durchgelassen, erwartet %d", counted, limit)
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
