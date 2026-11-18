package web

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"fwalarm/internal/bg"
	"fwalarm/internal/clock"
	"fwalarm/internal/config"
	"fwalarm/internal/core"
	"fwalarm/internal/store"
	"fwalarm/internal/store/storetest"
)

// Prüfstand für Sitzungen und Anmeldung: eine Ablage im Speicher (FaultFS),
// eine gestellte Uhr, die meldet, wann jemand schläft oder wartet, und eine
// Konfiguration, die der Test austauschen kann.

// Zugangsdaten der Tests (bcrypt mit dem kleinsten Aufwand – schnell).
const (
	adminUser  = "admin"
	adminPass  = "admin-pass"
	readerUser = "leser"
	readerPass = "leser-pass"
)

var testHashes = sync.OnceValues(func() (admin, reader string) {
	hash := func(pw string) string {
		h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
		if err != nil {
			panic(err)
		}
		return string(h)
	}
	return hash(adminPass), hash(readerPass)
})

// testClock ist die gestellte Uhr mit Meldungen: Jedes Sleep und jedes After
// meldet seine Dauer, sobald sein Zeitgeber steht – so stellt ein Test die
// Uhr erst weiter, wenn wirklich jemand darauf wartet (nie echtes Schlafen).
type testClock struct {
	*clock.Fake
	sleeps chan time.Duration
	waits  chan time.Duration
}

func newTestClock() *testClock {
	return &testClock{Fake: clock.NewFake(t0), sleeps: make(chan time.Duration, 512), waits: make(chan time.Duration, 512)}
}

func (k *testClock) Sleep(d time.Duration) {
	ch := k.Fake.After(d)
	k.sleeps <- d
	<-ch
}

func (k *testClock) After(d time.Duration) <-chan time.Time {
	ch := k.Fake.After(d)
	k.waits <- d
	return ch
}

type authBench struct {
	t   testing.TB
	c   *core.Core
	clk *testClock
	fs  *storetest.FaultFS
	cfg atomic.Pointer[config.Config]
}

// newAuthBench: Admin und Lese-Benutzer mit bcrypt-Hashes, Bremse mit den
// Standards von PHP (8 Versuche, 900 s). prepare (darf nil sein) richtet die
// Dateischicht vor dem Öffnen ein; strict: Close muss gelingen.
func newAuthBench(t testing.TB, prepare func(fs *storetest.FaultFS), strict bool) *authBench {
	t.Helper()
	b := &authBench{t: t, clk: newTestClock(), fs: storetest.New()}
	if prepare != nil {
		prepare(b.fs)
	}
	log := slog.New(slog.DiscardHandler)
	// Die Ablage hat ihre eigene Uhr: Ihre Zeitgeber sollen die Meldungen der
	// Test-Uhr nicht stören.
	st, _, err := store.Open(filepath.FromSlash("/daten"), b.fs, clock.NewFake(t0), log)
	if err != nil {
		t.Fatal(err)
	}
	group := bg.New(log)
	t.Cleanup(func() {
		if !group.Wait(10 * time.Second) {
			t.Error("Arbeit nebenher endet nicht")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := st.Close(ctx); err != nil && strict {
			t.Errorf("Close: %v", err)
		}
	})
	admin, reader := testHashes()
	cfg := &config.Config{
		DashboardUser: adminUser, DashboardPasswordHash: admin,
		DashboardReadonlyUser: readerUser, DashboardReadonlyPasswordHash: reader,
		LoginMaxFailures: 8, LoginLockoutWindowSeconds: 900,
	}
	b.cfg.Store(cfg)
	b.c = &core.Core{Cfg: b.cfg.Load, CfgOK: func() bool { return true }, Store: st, Clock: b.clk,
		Bg: group, Log: log, Version: "test"}
	return b
}

// setCfg ändert eine Kopie der geltenden Konfiguration und setzt sie in Kraft.
func (b *authBench) setCfg(change func(c *config.Config)) {
	next := *b.cfg.Load()
	change(&next)
	b.cfg.Store(&next)
}

// request baut eine Anfrage von remote ("192.0.2.1"), optional mit Cookie.
func (b *authBench) request(method, remote, cookie string, body url.Values) *http.Request {
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, "/index.php", strings.NewReader(body.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, "/index.php", nil)
	}
	r.RemoteAddr = net.JoinHostPort(remote, "40000")
	if cookie != "" {
		r.Header.Set("Cookie", sessionCookie+"="+cookie)
	}
	return r
}

// begin meldet user mit role an (wie nach einer erfolgreichen Anmeldung) und
// liefert den Wert des Cookies.
func (b *authBench) begin(user, role string) (value string, s *Session) {
	b.t.Helper()
	rec := httptest.NewRecorder()
	s = Begin(b.c, rec, b.request(http.MethodPost, "192.0.2.1", "", nil), user, role)
	ck := sessionCookieOf(b.t, rec)
	if ck == nil {
		b.t.Fatal("Begin setzt kein Cookie")
	}
	return ck.Value, s
}

// resume setzt die Sitzung zum Cookie-Wert value fort.
func (b *authBench) resume(value string) (*Session, bool, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	s, ok := Resume(b.c, rec, b.request(http.MethodGet, "192.0.2.1", value, nil))
	return s, ok, rec
}

// sessionCookieOf liefert das Sitzungs-Cookie der Antwort (nil: keins) und
// verlangt, dass es höchstens eins gibt.
func sessionCookieOf(t testing.TB, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	return cookieOf(t, rec, sessionCookie)
}

// deviceCookieOf: der Geräte-Ausweis der Antwort (nil: keiner gesetzt).
func deviceCookieOf(t testing.TB, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	return cookieOf(t, rec, deviceCookie)
}

func cookieOf(t testing.TB, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	var found *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		if ck.Name != name {
			continue
		}
		if found != nil {
			t.Errorf("zwei Cookies %s in einer Antwort: %q", name, rec.Header()["Set-Cookie"])
		}
		found = ck
	}
	return found
}

// withDevice: Kopfzeile für startLogin/login, die den Geräte-Ausweis mitschickt.
func withDevice(pass string) [2]string { return [2]string{"Cookie", deviceCookie + "=" + pass} }

// testID: eine feste Ausweis-id (16 Bytes aus n).
func testID(n byte) []byte { return bytes.Repeat([]byte{n}, deviceIDBytes) }

// laneWaiting: Wartende in den Ausweis-Reihen und in der allgemeinen Reihe
// (unter der Sperre gelesen, damit der Race-Detektor durchsieht).
func laneWaiting(c *core.Core) (prio, general int) {
	q := slotsFor(c)
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, w := range q.waiting {
		general += len(w)
	}
	for _, p := range q.prio {
		prio += len(p)
	}
	return prio, general
}

// classWaiting: Wartende in der Admin- und in der Lese-Ausweis-Reihe.
func classWaiting(c *core.Core) (admin, reader int) {
	q := slotsFor(c)
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.prio[classAdmin]), len(q.prio[classReader])
}

// advanceQuiet stellt die Uhr um d weiter und verlangt, dass dabei kein
// Zeitgeber ablief – also kein Wartender und kein Schlafender aufwachen
// konnte (deterministisch, ohne auf ein Ergebnis zu warten, das nicht kommt).
func (b *authBench) advanceQuiet(d time.Duration) {
	b.t.Helper()
	before := b.clk.Pending()
	b.clk.Advance(d)
	if after := b.clk.Pending(); after != before {
		b.t.Fatalf("nach %v liefen %d Zeitgeber ab – zu früh", d, before-after)
	}
}

// attempt ist das Ergebnis eines Anmeldeversuchs.
type attempt struct {
	res   LoginResult
	shown string
	rec   *httptest.ResponseRecorder
}

// startLogin schickt einen Anmeldeversuch von remote ab, ohne zu warten;
// hdr sind zusätzliche Kopfzeilen (Name, Wert).
func (b *authBench) startLogin(user, pw, remote string, hdr ...[2]string) <-chan attempt {
	r := b.request(http.MethodPost, remote, "", url.Values{"action": {"login"}, "user": {user}, "password": {pw}})
	for _, h := range hdr {
		r.Header.Add(h[0], h[1])
	}
	rec := httptest.NewRecorder()
	f, ok := ParseForm(rec, r)
	if !ok {
		b.t.Fatal("ParseForm")
	}
	ch := make(chan attempt, 1)
	go func() {
		res, shown := Login(b.c, rec, r, f)
		ch <- attempt{res, shown, rec}
	}()
	return ch
}

// login: ein Versuch bis zum Ende – schläft er (die Pause nach einem
// Fehlversuch), wird die Uhr um genau diese Dauer weitergestellt.
func (b *authBench) login(user, pw, remote string, hdr ...[2]string) attempt {
	b.t.Helper()
	return b.finish(b.startLogin(user, pw, remote, hdr...))
}

func (b *authBench) finish(ch <-chan attempt) attempt {
	b.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case a := <-ch:
			return a
		case d := <-b.clk.sleeps:
			b.clk.Advance(d)
		case <-deadline:
			b.t.Fatal("Anmeldung endet nicht")
			return attempt{}
		}
	}
}

// useVerifier setzt für die Dauer des Tests eine eigene Passwortprüfung ein.
func useVerifier(t testing.TB, fn func(hash, pw string) bool) {
	t.Helper()
	old := verifyPassword
	verifyPassword = fn
	t.Cleanup(func() { verifyPassword = old })
}

// loginCount: gezählte Versuche des Absenders in der Login-Bremse.
func (b *authBench) loginCount(who string) (count int, present bool) {
	e, ok := b.c.Store.Runtime().Throttle.Login[who]
	return e.Count, ok
}
