package bark

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"fwalarm/internal/clock"
	"fwalarm/internal/config"
)

// logBuf fängt Journal-Zeilen auf.
type logBuf struct {
	mu sync.Mutex
	sb strings.Builder
}

func (b *logBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.Write(p)
}

func (b *logBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.String()
}

func (b *logBuf) lines() []string {
	s := strings.TrimSpace(b.String())
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func bufLog() (*slog.Logger, *logBuf) {
	b := &logBuf{}
	return slog.New(slog.NewTextHandler(b, nil)), b
}

// run führt PostMany aus und stellt die Uhr über jede Pause zwischen den
// Runden weiter, bis es zurückkehrt.
func run(t *testing.T, c *Client, clk *clock.Fake, js []Job, tries int) []Result {
	t.Helper()
	done := make(chan []Result, 1)
	go func() { done <- c.PostMany(context.Background(), js, tries) }()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case r := <-done:
			return r
		case <-deadline:
			t.Fatal("PostMany kehrt nicht zurück")
		default:
		}
		if clk.Pending() > 0 {
			clk.Advance(roundPause)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestBodyIsNeverRead(t *testing.T) {
	// Eine endlose Antwort: Wer den Rumpf liest, bleibt bis zum Zeitlimit hängen.
	stop := make(chan struct{})
	m := newMock(t, func(w http.ResponseWriter, r *http.Request, key string) {
		chunk := make([]byte, 64<<10)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := w.Write(chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	})
	t.Cleanup(func() { close(stop) }) // läuft vor dem Schließen des Servers
	c, _, _ := newClient(m, nil)
	done := make(chan []Result, 1)
	go func() { done <- c.PostMany(context.Background(), jobs("keyendless"), 1) }()
	select {
	case r := <-done:
		if !r[0].OK || r[0].HTTP != 200 {
			t.Errorf("%+v", r[0])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PostMany hängt an der endlosen Antwort – der Rumpf wird gelesen")
	}
}

func TestSlowKeyDoesNotDelayOthers(t *testing.T) {
	release := make(chan struct{})
	arrived := make(chan string, 3)
	m := newMock(t, func(w http.ResponseWriter, r *http.Request, key string) {
		arrived <- key
		if key == "keyslow1" {
			select {
			case <-release:
			case <-time.After(8 * time.Second): // Notbremse, greift nur, wenn der Test scheitert
			}
		}
	})
	c, _, _ := newClient(m, nil)
	done := make(chan []Result, 1)
	go func() { done <- c.PostMany(context.Background(), jobs("keyslow1", "keyfast1", "keyfast2"), 1) }()
	// Beide schnellen Keys müssen ankommen, solange der langsame noch hängt.
	seen := map[string]bool{}
	deadline := time.After(5 * time.Second)
	for !(seen["keyfast1"] && seen["keyfast2"]) {
		select {
		case k := <-arrived:
			seen[k] = true
		case <-deadline:
			close(release)
			t.Fatalf("schnelle Keys kamen nicht an, solange der langsame hängt: %v", seen)
		}
	}
	close(release)
	for i, x := range <-done {
		if !x.OK {
			t.Errorf("Ergebnis %d: %+v", i, x)
		}
	}
}

func TestGuardedRecoversAndLogsWithoutSecrets(t *testing.T) {
	log, buf := bufLog()
	r := guarded(log, func() Result { panic("geheimer Wert keyboom01") })
	if r.OK || r.Err != errPanicked || strings.Contains(r.Err, "keyboom01") {
		t.Errorf("%+v", r)
	}
	lines := buf.lines()
	// Eine Zeile (der Stapel steht in einem Feld, ohne Zeilenumbruch).
	if len(lines) != 1 {
		t.Fatalf("Journal: %d Zeilen: %q", len(lines), buf.String())
	}
	if strings.Contains(lines[0], "keyboom01") || strings.Contains(lines[0], "geheimer") {
		t.Errorf("Wert im Journal: %s", lines[0])
	}
	if !strings.Contains(lines[0], "string") || !strings.Contains(lines[0], "stapel") {
		t.Errorf("Art und Stapel fehlen: %s", lines[0])
	}
	// Ein Fehler der Sprache nennt seinen Text.
	log2, buf2 := bufLog()
	var m map[string]int
	guarded(log2, func() Result { m["x"] = 1; return Result{} })
	if !strings.Contains(buf2.String(), "nil map") {
		t.Errorf("Laufzeitfehler ohne Text: %s", buf2.String())
	}
	if r := guarded(log, func() Result { return Result{OK: true, HTTP: 200} }); !r.OK {
		t.Error("ohne Panik unverändert")
	}
}

func TestPostManyPanicsInJobsAreIsolated(t *testing.T) {
	m := newMock(t, nil)
	log, buf := bufLog()
	cfg := testCfg()
	cfg.BarkHost = m.srv.URL
	c := New(func() *config.Config { return cfg }, clock.NewFake(t0), log)
	c.http = nil // lässt jeden Versuch mit einem Laufzeitfehler scheitern
	r := c.PostMany(context.Background(), jobs("keyaaaa01", "keybbbb01"), 1)
	for i, x := range r {
		if x.OK || x.Err != errPanicked {
			t.Errorf("Ergebnis %d: %+v", i, x)
		}
	}
	out := buf.String()
	if strings.Count(out, "Laufzeitfehler beim Bark-Versand") != 2 {
		t.Errorf("Journal: %s", out)
	}
	if strings.Contains(out, "keyaaaa01") || strings.Contains(out, "keybbbb01") {
		t.Errorf("Key im Journal: %s", out)
	}
}

func TestFailedAttemptsAreLoggedMasked(t *testing.T) {
	m := newMock(t, func(w http.ResponseWriter, r *http.Request, key string) {
		switch key {
		case "geheimkey400":
			w.WriteHeader(400)
		case "geheimkey500":
			w.WriteHeader(500)
		}
	})
	log, buf := bufLog()
	cfg := testCfg()
	cfg.BarkHost = m.srv.URL
	guardOn(cfg, "2026-11-18 14:00", "geheimkey400", "geheimkey500")
	clk := clock.NewFake(t0)
	c := New(func() *config.Config { return cfg }, clk, log)
	run(t, c, clk, jobs("geheimkey400", "geheimkey500", "geheimkeyok1", "geheimkeyok2", "geheimkeyok3"), 2)
	out := buf.String()
	for _, k := range []string{"geheimkey400", "geheimkey500", "geheimkeyok", m.srv.URL, "127.0.0.1"} {
		if strings.Contains(out, k) {
			t.Errorf("%q im Journal:\n%s", k, out)
		}
	}
	// 400: eine Zeile (Runde 1); 500: zwei Zeilen (Runde 1 und 2); Sperre: eine Zeile für alle drei.
	if n := strings.Count(out, "Bark-Versand gescheitert"); n != 3 {
		t.Errorf("%d Zeilen über gescheiterte Versuche:\n%s", n, out)
	}
	if n := strings.Count(out, "Probe-Sperre hält"); n != 1 || !strings.Contains(out, "anzahl=3") {
		t.Errorf("Sperr-Zeile:\n%s", out)
	}
	if !strings.Contains(out, "http=400") || !strings.Contains(out, "http=500") || !strings.Contains(out, "runde=2") || !strings.Contains(out, "gehe...") {
		t.Errorf("Inhalt der Zeilen:\n%s", out)
	}
}

func TestUnusableHostSendsNothing(t *testing.T) {
	m := newMock(t, nil)
	bare := strings.TrimPrefix(m.srv.URL, "http://") // erreichbar, aber ohne Schema
	for host, want := range map[string]string{
		"":              errNoHost,
		"   ":           errNoHost,
		"/":             errNoHost,
		bare:            errBadHost,
		"ftp://x.test":  errBadHost,
		"api.day.app":   errBadHost,
		"httpx://a.b/c": errBadHost,
	} {
		cfg := testCfg() // ohne BarkHost, wie ein von Hand gebauter Stand
		cfg.BarkHost = host
		clk := clock.NewFake(t0)
		c := New(func() *config.Config { return cfg }, clk, quietLog())
		r := c.PostMany(context.Background(), jobs("key0000a", "key0000b"), 3) // keine Runden: kehrt sofort zurück
		for i, x := range r {
			if x != (Result{Err: want}) {
				t.Errorf("Host %q, Ergebnis %d: %+v", host, i, x)
			}
		}
	}
	if m.total.Load() != 0 {
		t.Errorf("%d Anfragen trotz unbrauchbarem Host", m.total.Load())
	}
	// Auch mit Sperre behalten gesperrte Jobs ihr Sperr-Ergebnis.
	cfg := testCfg()
	guardOn(cfg, "2026-11-18 14:00", "keyadmin1")
	c := New(func() *config.Config { return cfg }, clock.NewFake(t0), quietLog())
	r := c.PostMany(context.Background(), jobs("keyadmin1", "key0000a"), 1)
	if r[0].Err != errNoHost || r[1].Err != guardErr {
		t.Errorf("%+v", r)
	}
}

func TestSendGuardReadOnceEndsDuringCall(t *testing.T) {
	m := newMock(t, func(w http.ResponseWriter, r *http.Request, key string) { w.WriteHeader(500) })
	c, clk, _ := newClient(m, func(c *config.Config) { guardOn(c, "2026-11-18 14:00", "keyadmin1") })
	done := make(chan []Result, 1)
	go func() {
		done <- c.PostMany(context.Background(), jobs("keyadmin1", "key0000a", "key0000b"), 2)
	}()
	waitPending(t, clk, 1)
	// Die Sperre läuft in der Pause ab (14:00 Berlin = 13:00 UTC).
	clk.Set(time.Date(2026, 11, 18, 13, 0, 1, 0, time.UTC))
	clk.Advance(roundPause)
	r := <-done
	if m.n("key0000a") != 0 || m.n("key0000b") != 0 {
		t.Errorf("gesperrte Keys bekamen Anfragen: %d %d", m.n("key0000a"), m.n("key0000b"))
	}
	if m.n("keyadmin1") != 2 {
		t.Errorf("gelisteter Key: %d Anfragen", m.n("keyadmin1"))
	}
	if r[1].Err != guardErr || r[2].Err != guardErr {
		t.Errorf("%+v", r)
	}
	// Der nächste Aufruf sieht die abgelaufene Sperre.
	if _, active := c.Guard(); active {
		t.Error("Sperre gilt nach Ablauf noch")
	}
}

func TestDeviceKeyFieldRefused(t *testing.T) {
	for _, withLock := range []bool{false, true} {
		for _, name := range []string{"device_key", "Device_Key", "DEVICE_KEYS", "device_keys"} {
			m := newMock(t, nil)
			c, _, _ := newClient(m, func(c *config.Config) {
				if withLock {
					guardOn(c, "2026-11-18 14:00", "keyadmin1")
				}
			})
			js := []Job{
				{Key: "keyadmin1", Fields: map[string]string{"title": "T", name: "keyfremd1"}},
				{Key: "keyadmin1", Fields: map[string]string{"title": "T"}},
			}
			r := c.PostMany(context.Background(), js, 1)
			if r[0] != (Result{Err: errBadField}) {
				t.Errorf("%s (Sperre %v): %+v", name, withLock, r[0])
			}
			if !r[1].OK || m.total.Load() != 1 {
				t.Errorf("%s (Sperre %v): zweiter Job %+v, Anfragen %d", name, withLock, r[1], m.total.Load())
			}
		}
	}
}

func TestNilConfig(t *testing.T) {
	m := newMock(t, nil)
	c := New(func() *config.Config { return nil }, clock.NewFake(t0), quietLog())
	r := c.PostMany(context.Background(), jobs("key0000a", "key0000b"), 3)
	if len(r) != 2 || r[0] != (Result{Err: errNoConfig}) || r[1] != (Result{Err: errNoConfig}) {
		t.Errorf("%+v", r)
	}
	if until, active := c.Guard(); active || !until.IsZero() {
		t.Errorf("Guard() = %v %v", until, active)
	}
	if len(c.PostMany(context.Background(), nil, 1)) != 0 || m.total.Load() != 0 {
		t.Error("leere Liste / Anfragen")
	}
}

func TestRetriesOfOtherStatuses(t *testing.T) {
	for _, code := range []int{204, 302} {
		m := newMock(t, func(w http.ResponseWriter, r *http.Request, key string) {
			if code == 302 {
				http.Redirect(w, r, "/ziel0000", http.StatusFound)
				return
			}
			w.WriteHeader(code)
		})
		c, clk, _ := newClient(m, nil)
		r := run(t, c, clk, jobs("keyother1"), 2)
		if m.n("keyother1") != 2 || r[0].OK || r[0].HTTP != code {
			t.Errorf("%d: Anfragen %d, %+v", code, m.n("keyother1"), r[0])
		}
		if m.n("ziel0000") != 0 {
			t.Errorf("%d: Weiterleitung verfolgt", code)
		}
	}
}

func TestRoundsOnlyForOpenJobs(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}
	m := newMock(t, func(w http.ResponseWriter, r *http.Request, key string) {
		mu.Lock()
		seen[key]++
		n := seen[key]
		mu.Unlock()
		switch {
		case key == "key400aa":
			w.WriteHeader(400)
		case key != "keyfirst" && n == 1:
			w.WriteHeader(500) // erste Runde scheitert, zweite gelingt
		}
	})
	c, clk, _ := newClient(m, nil)
	r := run(t, c, clk, jobs("keylate01", "key400aa", "keyfirst", "keylate02"), 3)
	want := []Result{{OK: true, HTTP: 200}, {HTTP: 400}, {OK: true, HTTP: 200}, {OK: true, HTTP: 200}}
	for i := range want {
		if r[i] != want[i] {
			t.Errorf("Ergebnis %d: %+v, soll %+v", i, r[i], want[i])
		}
	}
	for key, n := range map[string]int{"keylate01": 2, "key400aa": 1, "keyfirst": 1, "keylate02": 2} {
		if m.n(key) != n {
			t.Errorf("%s: %d Anfragen, soll %d", key, m.n(key), n)
		}
	}
}
