package bark

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fwalarm/internal/clock"
	"fwalarm/internal/config"
)

// mock ist ein Bark-Ersatz auf 127.0.0.1; er zählt die Aufrufe je Key.
type mock struct {
	srv   *httptest.Server
	mu    sync.Mutex
	count map[string]int
	total atomic.Int32
}

func newMock(t *testing.T, h func(w http.ResponseWriter, r *http.Request, key string)) *mock {
	t.Helper()
	m := &mock{count: map[string]int{}}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/")
		m.mu.Lock()
		m.count[key]++
		m.mu.Unlock()
		m.total.Add(1)
		if h != nil {
			h(w, r, key)
		}
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mock) n(key string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.count[key]
}

// 12:00 UTC = 13:00 in Berlin (Winterzeit).
var t0 = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

func newClient(m *mock, mut func(*config.Config)) (*Client, *clock.Fake, *config.Config) {
	cfg := testCfg()
	cfg.BarkHost = m.srv.URL + "/"
	if mut != nil {
		mut(cfg)
	}
	clk := clock.NewFake(t0)
	return New(func() *config.Config { return cfg }, clk), clk, cfg
}

func jobs(keys ...string) []Job {
	var js []Job
	for _, k := range keys {
		js = append(js, Job{Key: k, Fields: map[string]string{"title": "T", "body": "B"}})
	}
	return js
}

func waitPending(t *testing.T, clk *clock.Fake, n int) {
	t.Helper()
	for i := 0; i < 2000 && clk.Pending() < n; i++ {
		time.Sleep(2 * time.Millisecond)
	}
	if clk.Pending() < n {
		t.Fatalf("Pause zwischen den Runden nie erreicht")
	}
}

func TestPostManyRounds(t *testing.T) {
	t.Run("500 wird in Runden wiederholt, mit Pause", func(t *testing.T) {
		m := newMock(t, func(w http.ResponseWriter, r *http.Request, key string) { w.WriteHeader(500) })
		c, clk, _ := newClient(m, nil)
		done := make(chan []Result, 1)
		go func() { done <- c.PostMany(context.Background(), jobs("keyaaaa1"), 3) }()
		waitPending(t, clk, 1)
		if m.n("keyaaaa1") != 1 {
			t.Fatalf("vor der Pause %d Versuche", m.n("keyaaaa1"))
		}
		clk.Advance(499 * time.Millisecond)
		time.Sleep(50 * time.Millisecond)
		if m.n("keyaaaa1") != 1 {
			t.Fatal("zu früh wiederholt")
		}
		clk.Advance(time.Millisecond)
		for i := 0; i < 1000 && m.n("keyaaaa1") < 2; i++ {
			time.Sleep(2 * time.Millisecond)
		}
		waitPending(t, clk, 1)
		clk.Advance(500 * time.Millisecond)
		r := <-done
		if m.n("keyaaaa1") != 3 {
			t.Errorf("Versuche = %d", m.n("keyaaaa1"))
		}
		if r[0].OK || r[0].HTTP != 500 {
			t.Errorf("Ergebnis %+v", r[0])
		}
	})
	t.Run("400 genau ein Versuch", func(t *testing.T) {
		m := newMock(t, func(w http.ResponseWriter, r *http.Request, key string) { w.WriteHeader(400) })
		c, _, _ := newClient(m, nil)
		r := c.PostMany(context.Background(), jobs("keyaaaa1"), 3)
		if m.n("keyaaaa1") != 1 || r[0].OK || r[0].HTTP != 400 {
			t.Errorf("n=%d %+v", m.n("keyaaaa1"), r[0])
		}
	})
	t.Run("nur 200 zählt, 204 nicht", func(t *testing.T) {
		m := newMock(t, func(w http.ResponseWriter, r *http.Request, key string) {
			if key == "key204ab" {
				w.WriteHeader(204)
			}
		})
		c, _, _ := newClient(m, nil)
		r := c.PostMany(context.Background(), jobs("keyok123", "key204ab"), 1)
		if !r[0].OK || r[0].HTTP != 200 || r[1].OK || r[1].HTTP != 204 {
			t.Errorf("%+v", r)
		}
	})
	t.Run("abgerissene Verbindung wird wiederholt, Err ohne Key", func(t *testing.T) {
		m := newMock(t, func(w http.ResponseWriter, r *http.Request, key string) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
		})
		c, clk, _ := newClient(m, nil)
		done := make(chan []Result, 1)
		go func() { done <- c.PostMany(context.Background(), jobs("geheimkey99"), 2) }()
		waitPending(t, clk, 1)
		clk.Advance(500 * time.Millisecond)
		r := <-done
		if m.n("geheimkey99") != 2 {
			t.Errorf("Versuche = %d", m.n("geheimkey99"))
		}
		if r[0].OK || r[0].HTTP != 0 || r[0].Err == "" {
			t.Errorf("%+v", r[0])
		}
		if strings.Contains(r[0].Err, "geheimkey99") {
			t.Errorf("Key im Fehlertext: %q", r[0].Err)
		}
	})
	t.Run("ein langsamer Key hält die anderen nicht auf", func(t *testing.T) {
		var mu sync.Mutex
		arrived := map[string]time.Duration{}
		start := time.Now()
		m := newMock(t, func(w http.ResponseWriter, r *http.Request, key string) {
			mu.Lock()
			arrived[key] = time.Since(start)
			mu.Unlock()
			if key == "keyslow1" {
				time.Sleep(600 * time.Millisecond)
			}
		})
		c, _, _ := newClient(m, nil)
		r := c.PostMany(context.Background(), jobs("keyslow1", "keyfast1", "keyfast2"), 1)
		total := time.Since(start)
		for i, x := range r {
			if !x.OK {
				t.Errorf("Ergebnis %d: %+v", i, x)
			}
		}
		mu.Lock()
		defer mu.Unlock()
		for _, k := range []string{"keyfast1", "keyfast2"} {
			if arrived[k] > 400*time.Millisecond {
				t.Errorf("%s kam erst nach %v an", k, arrived[k])
			}
		}
		if total > 1500*time.Millisecond {
			t.Errorf("gesamt %v: nicht gleichzeitig", total)
		}
	})
	t.Run("302 wird nicht verfolgt", func(t *testing.T) {
		m := newMock(t, func(w http.ResponseWriter, r *http.Request, key string) {
			if key == "keyredir1" {
				http.Redirect(w, r, "/ziel0000", http.StatusFound)
			}
		})
		c, _, _ := newClient(m, nil)
		r := c.PostMany(context.Background(), jobs("keyredir1"), 1)
		if m.n("ziel0000") != 0 {
			t.Error("Weiterleitung verfolgt")
		}
		if r[0].OK || r[0].HTTP != 302 {
			t.Errorf("%+v", r[0])
		}
	})
	t.Run("eine 50-MB-Antwort wächst nicht im Speicher", func(t *testing.T) {
		m := newMock(t, func(w http.ResponseWriter, r *http.Request, key string) {
			chunk := make([]byte, 1<<20)
			for i := 0; i < 50; i++ {
				if _, err := w.Write(chunk); err != nil {
					return
				}
			}
		})
		c, _, _ := newClient(m, nil)
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		r := c.PostMany(context.Background(), jobs("keybig001"), 1)
		runtime.GC()
		runtime.ReadMemStats(&after)
		if !r[0].OK || r[0].HTTP != 200 {
			t.Errorf("%+v", r[0])
		}
		if grown := int64(after.HeapAlloc) - int64(before.HeapAlloc); grown > 20<<20 {
			t.Errorf("Heap wuchs um %d Byte", grown)
		}
	})
	t.Run("Host wird bei jedem Versand neu gelesen; Pfad und Inhalt", func(t *testing.T) {
		var mu sync.Mutex
		var gotCT, gotBody, gotMethod string
		m := newMock(t, func(w http.ResponseWriter, r *http.Request, key string) {
			_ = r.ParseForm()
			mu.Lock()
			gotCT, gotMethod = r.Header.Get("Content-Type"), r.Method
			gotBody = r.PostForm.Get("title") + "|" + r.PostForm.Get("body")
			mu.Unlock()
		})
		c, _, cfg := newClient(m, func(c *config.Config) { c.BarkHost = "http://127.0.0.1:1" })
		cfg.BarkHost = m.srv.URL + "//" // Änderung zur Laufzeit
		r := c.PostMany(context.Background(),
			[]Job{{Key: "keyform01", Fields: map[string]string{"title": "Grüße & mehr", "body": "a b"}}}, 1)
		if !r[0].OK {
			t.Fatalf("%+v", r[0])
		}
		mu.Lock()
		defer mu.Unlock()
		if gotMethod != "POST" || gotCT != "application/x-www-form-urlencoded" || gotBody != "Grüße & mehr|a b" {
			t.Errorf("%s %s %q", gotMethod, gotCT, gotBody)
		}
	})
	t.Run("abgebrochener Kontext bricht ab", func(t *testing.T) {
		started := make(chan struct{}, 1)
		release := make(chan struct{})
		m := newMock(t, func(w http.ResponseWriter, r *http.Request, key string) {
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-r.Context().Done():
			case <-release:
			}
		})
		t.Cleanup(func() { close(release) }) // läuft vor dem Schließen des Servers
		c, _, _ := newClient(m, nil)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan []Result, 1)
		go func() { done <- c.PostMany(ctx, jobs("keycancel1"), 3) }()
		<-started
		cancel()
		select {
		case r := <-done:
			if r[0].OK || r[0].Err == "" {
				t.Errorf("%+v", r[0])
			}
		case <-time.After(3 * time.Second):
			t.Fatal("PostMany kehrt nach Abbruch nicht zurück")
		}
	})
	t.Run("leere Liste", func(t *testing.T) {
		m := newMock(t, nil)
		c, _, _ := newClient(m, nil)
		if r := c.PostMany(context.Background(), nil, 3); len(r) != 0 {
			t.Error(r)
		}
	})
}

func TestPostManyRecovers(t *testing.T) {
	m := newMock(t, nil)
	c, _, _ := newClient(m, nil)
	orig := c.send
	c.send = func(ctx context.Context, host string, j Job) Result {
		if j.Key == "keyboom01" {
			panic("geheimer Wert keyboom01")
		}
		return orig(ctx, host, j)
	}
	r := c.PostMany(context.Background(), jobs("keyaaaa01", "keyboom01", "keybbbb01"), 1)
	if !r[0].OK || !r[2].OK {
		t.Errorf("die anderen müssen laufen: %+v", r)
	}
	if r[1].OK || r[1].Err == "" || strings.Contains(r[1].Err, "keyboom01") || strings.Contains(r[1].Err, "geheimer") {
		t.Errorf("%+v", r[1])
	}
}

func guardOn(c *config.Config, until string, keys ...string) {
	c.SendGuardKeys = keys
	c.SendGuardUntil = until
}

func TestSendGuardBlocksEveryOtherKey(t *testing.T) {
	m := newMock(t, func(w http.ResponseWriter, r *http.Request, key string) { w.WriteHeader(500) })
	// Sperre bis 14:00 Berlin = 13:00 UTC; die Uhr steht auf 12:00 UTC.
	c, clk, _ := newClient(m, func(c *config.Config) { guardOn(c, "2026-11-18 14:00", "keyadmin1") })
	done := make(chan []Result, 1)
	go func() {
		done <- c.PostMany(context.Background(),
			jobs("keyadmin1", "key0000a", "key0000b", "key0000c", "key0000d", "key0000e"), 2)
	}()
	waitPending(t, clk, 1)
	clk.Advance(500 * time.Millisecond)
	r := <-done
	if m.total.Load() != 2 || m.n("keyadmin1") != 2 {
		t.Errorf("Anfragen gesamt %d, an den gelisteten Key %d", m.total.Load(), m.n("keyadmin1"))
	}
	for _, k := range []string{"key0000a", "key0000b", "key0000c", "key0000d", "key0000e"} {
		if m.n(k) != 0 {
			t.Errorf("Anfrage an gesperrten Key %s", k)
		}
	}
	if r[0].HTTP != 500 {
		t.Errorf("gelisteter Key lief nicht: %+v", r[0])
	}
	for i := 1; i < 6; i++ {
		if r[i] != (Result{OK: false, HTTP: 0, Err: guardErr}) {
			t.Errorf("Ergebnis %d: %+v", i, r[i])
		}
	}
	if until, active := c.Guard(); !active || until.IsZero() {
		t.Errorf("Guard() = %v %v", until, active)
	}
}

func TestSendGuardBlocksWithoutNetwork(t *testing.T) {
	// Unerreichbarer Host: für gesperrte Keys darf nicht einmal ein Verbindungsversuch stattfinden.
	m := newMock(t, nil)
	c, _, _ := newClient(m, func(c *config.Config) {
		c.BarkHost = "http://does-not-exist.invalid"
		guardOn(c, "2026-11-18 14:00", "keyadmin1")
	})
	r := c.PostMany(context.Background(), jobs("key0000a"), 3)
	if r[0] != (Result{Err: guardErr}) {
		t.Errorf("%+v", r[0])
	}
}

func TestSendGuardUnsetOrExpired(t *testing.T) {
	keys := []string{"keyadmin1", "key0000a", "key0000b", "key0000c", "key0000d", "key0000e"}
	for _, expired := range []bool{false, true} {
		name := "keine Sperre"
		if expired {
			name = "vor einer Sekunde abgelaufen"
		}
		t.Run(name, func(t *testing.T) {
			m := newMock(t, nil)
			c, clk, _ := newClient(m, func(c *config.Config) {
				if expired {
					guardOn(c, "2026-11-18 14:00", "keyadmin1")
				}
			})
			if expired {
				// 14:00 Berlin = 13:00 UTC; eine Sekunde danach.
				clk.Set(time.Date(2026, 11, 18, 13, 0, 1, 0, time.UTC))
			}
			if _, active := c.Guard(); active {
				t.Fatal("Sperre darf nicht gelten")
			}
			r := c.PostMany(context.Background(), jobs(keys...), 1)
			for i, x := range r {
				if !x.OK {
					t.Errorf("Ergebnis %d: %+v", i, x)
				}
			}
			if m.total.Load() != 6 {
				t.Errorf("Anfragen = %d", m.total.Load())
			}
		})
	}
}

func TestSendGuardReadPerSend(t *testing.T) {
	m := newMock(t, nil)
	cfg := testCfg()
	cfg.BarkHost = m.srv.URL
	c := New(func() *config.Config { return cfg }, clock.NewFake(t0))
	if r := c.PostMany(context.Background(), jobs("key0000a"), 1); !r[0].OK {
		t.Fatalf("%+v", r[0])
	}
	next := *cfg // neue Konfiguration, wie nach einem Neuladen
	guardOn(&next, "2026-11-18 14:00", "keyadmin1")
	cfg = &next
	r := c.PostMany(context.Background(), jobs("key0000a"), 1)
	if r[0].Err != guardErr || m.n("key0000a") != 1 {
		t.Errorf("%+v, Anfragen %d", r[0], m.n("key0000a"))
	}
}
