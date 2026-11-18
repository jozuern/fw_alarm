package web

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fwalarm/internal/config"
	"fwalarm/internal/core"
	"fwalarm/internal/store/storetest"
)

// Anmeldung (PHP: index.php, Zweig action=login; Spec §6.8, §8).

// verifyCall ist eine Passwortprüfung, die der Test von Hand beendet.
type verifyCall struct {
	hash, pw string
	reply    chan bool
}

// controlledVerifier: Jede Prüfung meldet sich auf calls und dauert, bis der
// Test ihr Ergebnis schickt.
func controlledVerifier(calls chan verifyCall) func(hash, pw string) bool {
	return func(hash, pw string) bool {
		c := verifyCall{hash: hash, pw: pw, reply: make(chan bool)}
		calls <- c
		return <-c.reply
	}
}

func nextCall(t testing.TB, calls chan verifyCall) verifyCall {
	t.Helper()
	select {
	case c := <-calls:
		return c
	case <-time.After(10 * time.Second):
		t.Fatal("keine Passwortprüfung")
		return verifyCall{}
	}
}

func resultOf(t testing.TB, ch <-chan attempt) attempt {
	t.Helper()
	select {
	case a := <-ch:
		return a
	case <-time.After(10 * time.Second):
		t.Fatal("Anmeldung endet nicht")
		return attempt{}
	}
}

// Genau EINE Passwortprüfung je Versuch – auch für einen unbekannten
// Benutzer, und zwar gegen den Hash des Admins (index.php:78-83): Ein fester
// Wegwerf-Hash mit anderem Aufwand verriete über die Antwortzeit, ob es den
// Namen gibt.
func TestLoginOneVerifyPerAttempt(t *testing.T) {
	b := newAuthBench(t, nil, true)
	adminHash, readerHash := testHashes()
	var mu sync.Mutex
	var got []string
	useVerifier(t, func(hash, pw string) bool {
		mu.Lock()
		got = append(got, hash)
		mu.Unlock()
		return config.VerifyPassword(hash, pw)
	})
	for i, tc := range []struct {
		name, user, pw string
		want           LoginResult
		hash           string
	}{
		{"Admin, richtiges Passwort", adminUser, adminPass, LoginOK, adminHash},
		{"Admin, falsches Passwort", adminUser, "falsch", LoginFailed, adminHash},
		{"Lese-Benutzer, richtiges Passwort", readerUser, readerPass, LoginOK, readerHash},
		{"Lese-Benutzer mit dem Admin-Passwort", readerUser, adminPass, LoginFailed, readerHash},
		{"unbekannter Benutzer mit dem Admin-Passwort", "niemand", adminPass, LoginFailed, adminHash},
		{"leerer Benutzer", "", "", LoginFailed, adminHash},
		{"Groß/klein zählt", "Admin", adminPass, LoginFailed, adminHash},
	} {
		mu.Lock()
		got = nil
		mu.Unlock()
		a := b.login(tc.user, tc.pw, fmt.Sprintf("192.0.2.%d", 10+i))
		mu.Lock()
		calls := append([]string(nil), got...)
		mu.Unlock()
		if a.res != tc.want {
			t.Errorf("%s: %v, erwartet %v", tc.name, a.res, tc.want)
		}
		if len(calls) != 1 || calls[0] != tc.hash {
			t.Errorf("%s: %d Prüfungen %q, erwartet genau eine gegen %q", tc.name, len(calls), calls, tc.hash)
		}
		if a.shown != tc.user {
			t.Errorf("%s: Name für die Seite %q", tc.name, a.shown)
		}
		if ck := sessionCookieOf(t, a.rec); (ck != nil) != (tc.want == LoginOK) {
			t.Errorf("%s: Cookie %v", tc.name, ck)
		}
	}
	// Nach einem Erfolg gilt die Sitzung mit Rolle und Namen aus der Konfiguration.
	a := b.login(readerUser, readerPass, "192.0.2.50")
	ck := sessionCookieOf(t, a.rec)
	if ck == nil {
		t.Fatal("kein Cookie nach der Anmeldung")
	}
	s, ok, _ := b.resume(ck.Value)
	if !ok || s.Role != "readonly" || s.User != readerUser {
		t.Errorf("Sitzung des Lese-Benutzers: %v %+v", ok, s)
	}
	// Der Name für die Fehlerseite: höchstens 80 Bytes, nie mitten im Zeichen.
	long := strings.Repeat("ä", 50)
	if a := b.login(long, "x", "192.0.2.51"); a.shown != strings.Repeat("ä", 40) {
		t.Errorf("langer Name: %q", a.shown)
	}
}

// Wogegen geprüft wird, wenn ein Hash unbrauchbar ist: immer genau eine
// Prüfung, für unbekannte Namen gegen einen ECHTEN Hash der Konfiguration
// (erst den des Admins, dann den des Lese-Benutzers), erst wenn keiner
// brauchbar ist gegen einen eigenen Argon2id-Hash mit den Werten von
// fw-alarm hash-password. Ein Treffer dabei meldet nie jemanden an.
func TestLoginUnusableHashes(t *testing.T) {
	adminHash, readerHash := testHashes()
	var mu sync.Mutex
	var got []string
	useVerifier(t, func(hash, pw string) bool {
		mu.Lock()
		got = append(got, hash)
		mu.Unlock()
		return config.VerifyPassword(hash, pw)
	})
	run := func(b *authBench, user, pw string, remote string) (LoginResult, []string) {
		t.Helper()
		mu.Lock()
		got = nil
		mu.Unlock()
		a := b.login(user, pw, remote)
		mu.Lock()
		defer mu.Unlock()
		return a.res, append([]string(nil), got...)
	}

	b := newAuthBench(t, nil, true)
	b.setCfg(func(c *config.Config) { c.DashboardPasswordHash = "kaputt" })
	for _, user := range []string{"niemand", adminUser} {
		if res, calls := run(b, user, adminPass, "192.0.2.1"); res != LoginFailed || len(calls) != 1 || calls[0] != readerHash {
			t.Errorf("Admin-Hash unbrauchbar, %q: %v %q", user, res, calls)
		}
	}
	if res, calls := run(b, readerUser, readerPass, "192.0.2.1"); res != LoginOK || len(calls) != 1 || calls[0] != readerHash {
		t.Errorf("Admin-Hash unbrauchbar, Lese-Benutzer: %v %q", res, calls)
	}

	b.setCfg(func(c *config.Config) { c.DashboardReadonlyPasswordHash = "$2y$99$kaputt" })
	var decoy string
	for _, user := range []string{"niemand", adminUser, readerUser} {
		res, calls := run(b, user, "x", "192.0.2.2")
		if res != LoginFailed || len(calls) != 1 || !strings.HasPrefix(calls[0], "$argon2id$v=19$m=19456,t=2,p=1$") {
			t.Errorf("kein Hash brauchbar, %q: %v %q", user, res, calls)
			continue
		}
		if decoy == "" {
			decoy = calls[0]
		} else if calls[0] != decoy {
			t.Errorf("der Ersatz-Hash wechselt: %q, vorher %q", calls[0], decoy)
		}
	}

	// Lese-Benutzer ohne Hash: kein Konto – sein Name zählt als unbekannt
	// (index.php:73).
	b = newAuthBench(t, nil, true)
	b.setCfg(func(c *config.Config) { c.DashboardReadonlyPasswordHash = "" })
	if res, calls := run(b, readerUser, readerPass, "192.0.2.3"); res != LoginFailed || len(calls) != 1 || calls[0] != adminHash {
		t.Errorf("Lese-Benutzer ohne Hash: %v %q", res, calls)
	}
	// Ohne Admin-Namen gibt es kein Admin-Konto: Der leere Name mit dem
	// richtigen Passwort wird geprüft, meldet aber niemanden an.
	b.setCfg(func(c *config.Config) { c.DashboardUser = "" })
	if res, calls := run(b, "", adminPass, "192.0.2.4"); res != LoginFailed || len(calls) != 1 || calls[0] != adminHash {
		t.Errorf("leerer Admin-Name: %v %q", res, calls)
	}
}

// Sec-Fetch-Site (index.php:47-56): cross-site und same-site (und jeder
// andere Wert) → 403, BEVOR irgendetwas gezählt oder geprüft wird;
// same-origin, none und ohne Kopfzeile → Anmeldung geht.
func TestLoginSecFetchSite(t *testing.T) {
	b := newAuthBench(t, nil, true)
	var mu sync.Mutex
	verified := 0
	useVerifier(t, func(hash, pw string) bool {
		mu.Lock()
		verified++
		mu.Unlock()
		return config.VerifyPassword(hash, pw)
	})
	for _, values := range [][]string{{"cross-site"}, {"same-site"}, {"Cross-Site"}, {" cross-site "},
		{"same-origin", "cross-site"}, {"irgendwas"}} {
		var hdr [][2]string
		for _, v := range values {
			hdr = append(hdr, [2]string{"Sec-Fetch-Site", v})
		}
		a := b.login(adminUser, adminPass, "192.0.2.9", hdr...)
		if a.res != LoginForbidden || a.shown != adminUser || len(a.rec.Header()["Set-Cookie"]) != 0 {
			t.Errorf("Sec-Fetch-Site %q: %v %q", values, a.res, a.rec.Header()["Set-Cookie"])
		}
	}
	if _, counted := b.loginCount("192.0.2.9"); counted || verified != 0 {
		t.Errorf("abgewiesene Versuche gezählt (%v) oder geprüft (%d)", counted, verified)
	}
	for i, values := range [][]string{{"same-origin"}, {"none"}, nil, {""}, {"SAME-ORIGIN"}, {" none "}} {
		var hdr [][2]string
		for _, v := range values {
			hdr = append(hdr, [2]string{"Sec-Fetch-Site", v})
		}
		a := b.login(adminUser, adminPass, fmt.Sprintf("192.0.2.%d", 10+i), hdr...)
		if a.res != LoginOK || sessionCookieOf(t, a.rec) == nil {
			t.Errorf("Sec-Fetch-Site %q: %v", values, a.res)
		}
	}
}

// Der Versuch wird VOR der Passwortprüfung gezählt (index.php:50-53); wer
// gesperrt ist, kostet keine Prüfung; ein Erfolg setzt den Zähler zurück.
func TestLoginCountsBeforeCheck(t *testing.T) {
	b := newAuthBench(t, nil, true)
	b.setCfg(func(c *config.Config) { c.LoginMaxFailures = 3 })
	const ip = "192.0.2.7"
	var mu sync.Mutex
	var seen []int
	useVerifier(t, func(hash, pw string) bool {
		n, _ := b.loginCount(ip)
		mu.Lock()
		seen = append(seen, n)
		mu.Unlock()
		return config.VerifyPassword(hash, pw)
	})
	for i, user := range []string{adminUser, "niemand", adminUser} {
		if a := b.login(user, "falsch", ip); a.res != LoginFailed {
			t.Fatalf("Versuch %d: %v", i+1, a.res)
		}
	}
	mu.Lock()
	if want := []int{1, 2, 3}; len(seen) != 3 || seen[0] != want[0] || seen[1] != want[1] || seen[2] != want[2] {
		t.Errorf("Zähler während der Prüfung: %v, erwartet %v", seen, want)
	}
	mu.Unlock()
	// Gesperrt: auch das richtige Passwort, ohne Prüfung, ohne Zählen, ohne Pause.
	for range 3 {
		a := b.login(adminUser, adminPass, ip)
		if a.res != LoginBlocked || len(a.rec.Header()["Set-Cookie"]) != 0 {
			t.Errorf("gesperrter Absender: %v %q", a.res, a.rec.Header()["Set-Cookie"])
		}
	}
	mu.Lock()
	if len(seen) != 3 {
		t.Errorf("ein gesperrter Absender kostet %d Prüfungen", len(seen)-3)
	}
	mu.Unlock()
	if n, _ := b.loginCount(ip); n != 3 {
		t.Errorf("gesperrt weitergezählt: %d", n)
	}
	if len(b.clk.sleeps) != 0 {
		t.Error("Pause bei einem gesperrten Versuch")
	}
	// Erfolg setzt zurück: zwei Fehlversuche, dann richtig, dann wieder drei volle.
	const other = "192.0.2.8"
	for range 2 {
		b.login(adminUser, "falsch", other)
	}
	if a := b.login(adminUser, adminPass, other); a.res != LoginOK {
		t.Fatalf("Anmeldung nach zwei Fehlversuchen: %v", a.res)
	}
	if _, counted := b.loginCount(other); counted {
		t.Error("Erfolg setzt den Zähler nicht zurück")
	}
	if len(b.clk.sleeps) != 0 {
		t.Error("Pause nach einem Erfolg")
	}
	// Ein IPv6-/64 ist ein Absender.
	b.login(adminUser, "falsch", "2001:db8:1:2::1")
	b.login(adminUser, "falsch", "2001:db8:1:2::ffff")
	if n, _ := b.loginCount("2001:db8:1:2::/64"); n != 2 {
		t.Errorf("IPv6 /64: %d", n)
	}
}

// Die Pause von 1 s nach einem Fehlversuch liegt außerhalb der Plätze
// (Spec §6.8): Während A schläft, prüfen zwei andere zugleich.
func TestLoginSleepOutsideSlot(t *testing.T) {
	b := newAuthBench(t, nil, true)
	calls := make(chan verifyCall, 16)
	useVerifier(t, controlledVerifier(calls))
	a := b.startLogin(adminUser, "falsch", "192.0.2.1")
	nextCall(t, calls).reply <- false
	select {
	case d := <-b.clk.sleeps:
		if d != time.Second {
			t.Fatalf("Pause %v, erwartet 1 s", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("keine Pause nach dem Fehlversuch")
	}
	if n := slotsInUse(b.c); n != 0 {
		t.Fatalf("während der Pause sind %d Plätze belegt", n)
	}
	x := b.startLogin(adminUser, "falsch", "192.0.2.2")
	y := b.startLogin(adminUser, "falsch", "192.0.2.3")
	cx, cy := nextCall(t, calls), nextCall(t, calls)
	if n := slotsInUse(b.c); n != 2 {
		t.Errorf("zwei andere prüfen nicht zugleich: %d Plätze", n)
	}
	// A wartet allein auf seinen Zeitgeber von 1 s: Kurz davor läuft keiner
	// ab (A kann also nicht fertig sein), genau dann seiner.
	b.advanceQuiet(time.Second - time.Millisecond)
	b.clk.Advance(time.Millisecond)
	if r := resultOf(t, a); r.res != LoginFailed {
		t.Errorf("A: %v", r.res)
	}
	cx.reply <- false
	cy.reply <- false
	for _, ch := range []<-chan attempt{x, y} {
		if r := b.finish(ch); r.res != LoginFailed {
			t.Errorf("x/y: %v", r.res)
		}
	}
}

// Anmelden geht, wenn nichts gesichert werden kann (Spec §15.11): Die
// Sitzung gilt im Speicher, die Bremse zählt im Speicher.
func TestLoginWorksWhenNothingSaves(t *testing.T) {
	dir := filepath.FromSlash("/daten")
	for _, tc := range []struct {
		name  string
		spoil func(b *authBench) (release func())
	}{
		{"jeder Aufruf scheitert", func(b *authBench) func() {
			b.fs.CrashAt(b.fs.Calls() + 1)
			return func() {}
		}},
		{"die Platte hängt", func(b *authBench) func() {
			r1 := b.fs.Hang(filepath.Join(dir, "sessions.json.tmp"))
			r2 := b.fs.Hang(filepath.Join(dir, "runtime.json.tmp"))
			return func() { r1(); r2() }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newAuthBench(t, nil, false)
			t.Cleanup(tc.spoil(b)) // vor dem Schließen der Ablage wieder frei
			if a := b.login(adminUser, "falsch", "192.0.2.1"); a.res != LoginFailed {
				t.Errorf("Fehlversuch: %v", a.res)
			}
			if n, _ := b.loginCount("192.0.2.1"); n != 1 {
				t.Errorf("Fehlversuch nicht gezählt: %d", n)
			}
			a := b.login(adminUser, adminPass, "192.0.2.1")
			ck := sessionCookieOf(t, a.rec)
			if a.res != LoginOK || ck == nil {
				t.Fatalf("Anmeldung: %v", a.res)
			}
			if s, ok, _ := b.resume(ck.Value); !ok || s.Role != "admin" {
				t.Errorf("Sitzung nicht nutzbar: %v %+v", ok, s)
			}
		})
	}
}

// Anmelden geht im Notbetrieb (Spec §6.3).
func TestLoginWorksInEmergencyMode(t *testing.T) {
	b := newAuthBench(t, func(fs *storetest.FaultFS) {
		fs.Put(filepath.Join(filepath.FromSlash("/daten"), "state.json"), []byte("{kaputt"))
	}, false)
	if !b.c.Store.Emergency() {
		t.Fatal("kein Notbetrieb")
	}
	if a := b.login(adminUser, "falsch", "192.0.2.1"); a.res != LoginFailed {
		t.Errorf("Fehlversuch: %v", a.res)
	}
	a := b.login(adminUser, adminPass, "192.0.2.1")
	ck := sessionCookieOf(t, a.rec)
	if a.res != LoginOK || ck == nil {
		t.Fatalf("Anmeldung im Notbetrieb: %v", a.res)
	}
	if s, ok, _ := b.resume(ck.Value); !ok || s.Role != "admin" {
		t.Errorf("Sitzung im Notbetrieb: %v %+v", ok, s)
	}
}

// OnLogin und OnLockout laufen nebenher, nachdem die Antwort feststeht – sie
// halten sie nicht auf (später senden sie Bark-Meldungen). Gesperrt wird
// beim Erreichen der Grenze gemeldet, einmal (index.php:118-142).
func TestLoginMessagesAfterAnswer(t *testing.T) {
	b := newAuthBench(t, nil, true)
	b.setCfg(func(c *config.Config) { c.LoginMaxFailures = 2; c.LoginLockoutWindowSeconds = 10 })
	block := make(chan struct{})
	var mu sync.Mutex
	var got []string
	OnLogin = func(c *core.Core, role, user, ip string) {
		<-block
		mu.Lock()
		defer mu.Unlock()
		got = append(got, "login "+role+" "+user+" "+ip)
	}
	OnLockout = func(c *core.Core, ip string, windowSeconds int) {
		<-block
		mu.Lock()
		defer mu.Unlock()
		got = append(got, fmt.Sprint("lockout ", ip, " ", windowSeconds))
	}
	var once sync.Once
	release := func() { once.Do(func() { close(block) }) }
	t.Cleanup(func() {
		release()
		OnLogin, OnLockout = nil, nil
	})
	const ip6 = "2001:db8::5"
	for i, want := range []LoginResult{LoginFailed, LoginFailed, LoginBlocked, LoginBlocked} {
		if a := b.login(adminUser, "falsch", ip6); a.res != want {
			t.Errorf("Versuch %d: %v, erwartet %v", i+1, a.res, want)
		}
	}
	if a := b.login(readerUser, readerPass, "192.0.2.3"); a.res != LoginOK {
		t.Errorf("Anmeldung: %v", a.res)
	}
	release()
	if !b.c.Bg.Wait(10 * time.Second) {
		t.Fatal("Meldungen enden nicht")
	}
	mu.Lock()
	defer mu.Unlock()
	want := map[string]bool{"lockout 2001:db8::5 60": true, "login readonly leser 192.0.2.3": true}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] {
		t.Errorf("Meldungen: %q", got)
	}
}

// Ein Versuch, dessen Aufrufer gegangen ist, verlässt die Schlange sofort
// (nicht gezählt) und hält niemanden auf.
func TestLoginWaiterLeaves(t *testing.T) {
	b := newAuthBench(t, nil, true)
	calls := make(chan verifyCall, 16)
	useVerifier(t, controlledVerifier(calls))
	x := b.startLogin(adminUser, "x", "192.0.2.1")
	y := b.startLogin(adminUser, "y", "192.0.2.2")
	cx, cy := nextCall(t, calls), nextCall(t, calls)

	ctx, cancel := context.WithCancel(context.Background())
	r := b.request(http.MethodPost, "192.0.2.3", "", url.Values{"user": {adminUser}, "password": {adminPass}}).WithContext(ctx)
	rec := httptest.NewRecorder()
	f, _ := ParseForm(rec, r)
	gone := make(chan LoginResult, 1)
	go func() {
		res, _ := Login(b.c, rec, r, f)
		gone <- res
	}()
	select {
	case <-b.clk.waits:
	case <-time.After(10 * time.Second):
		t.Fatal("der dritte Versuch wartet nicht")
	}
	cancel()
	select {
	case res := <-gone:
		if res != LoginBusy {
			t.Errorf("gegangener Aufrufer: %v", res)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("der gegangene Aufrufer wartet weiter")
	}
	if _, counted := b.loginCount("192.0.2.3"); counted {
		t.Error("gegangener Aufrufer gezählt")
	}
	if n := slotsWaiting(b.c); n != 0 {
		t.Errorf("%d Wartende übrig", n)
	}
	cx.reply <- false
	cy.reply <- false
	b.finish(x)
	b.finish(y)
}

// Jeder Fehlversuch kostet genau 1 s – auch der mit unbekanntem und der mit
// leerem Namen (sonst verriete die Antwortzeit, ob es den Namen gibt).
func TestLoginFailurePause(t *testing.T) {
	b := newAuthBench(t, nil, true)
	for i, tc := range []struct{ name, user, pw string }{
		{"Admin, falsches Passwort", adminUser, "falsch"},
		{"Lese-Benutzer, falsches Passwort", readerUser, "falsch"},
		{"unbekannter Name", "niemand", adminPass},
		{"leerer Name", "", ""},
	} {
		ch := b.startLogin(tc.user, tc.pw, fmt.Sprintf("192.0.2.%d", 30+i))
		select {
		case d := <-b.clk.sleeps:
			if d != time.Second {
				t.Errorf("%s: Pause %v", tc.name, d)
			}
		case r := <-ch:
			t.Fatalf("%s: fertig ohne Pause (%v)", tc.name, r.res)
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: keine Pause", tc.name)
		}
		b.advanceQuiet(time.Second - time.Millisecond) // vorher kann er nicht fertig sein
		b.clk.Advance(time.Millisecond)
		if r := resultOf(t, ch); r.res != LoginFailed {
			t.Errorf("%s: %v", tc.name, r.res)
		}
	}
}

// Ein riesiges Sperrfenster (auch in einem von Hand gebauten Stand) schaltet
// die Bremse nicht ab: Die Umrechnung in eine Dauer liefe sonst über.
func TestLoginHugeLockoutWindow(t *testing.T) {
	for _, window := range []int{math.MaxInt, 9223372037} {
		b := newAuthBench(t, nil, true)
		b.setCfg(func(c *config.Config) { c.LoginMaxFailures = 2; c.LoginLockoutWindowSeconds = window })
		var once sync.Once
		got := -1
		OnLockout = func(c *core.Core, ip string, windowSeconds int) { once.Do(func() { got = windowSeconds }) }
		for i, want := range []LoginResult{LoginFailed, LoginFailed, LoginBlocked} {
			if a := b.login(adminUser, "falsch", "192.0.2.4"); a.res != want {
				t.Errorf("Fenster %d, Versuch %d: %v, erwartet %v", window, i+1, a.res, want)
			}
		}
		if !b.c.Bg.Wait(10 * time.Second) {
			t.Fatal("Meldung endet nicht")
		}
		OnLockout = nil
		if got != config.MaxLockoutWindowSeconds {
			t.Errorf("Fenster %d: gemeldet %d, erwartet %d", window, got, config.MaxLockoutWindowSeconds)
		}
	}
}
