package web

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"fwalarm/internal/config"
	"fwalarm/internal/core"
)

// Plätze für Passwortprüfungen (Spec §6.8): höchstens zwei zugleich,
// höchstens einer je Absender, Wartende der Reihe nach je Absender,
// höchstens 5 s – danach LoginBusy, nicht gezählt.

// slotsInUse / slotsWaiting: belegte Plätze und Wartende (unter der Sperre
// gelesen, damit der Race-Detektor durchsieht).
func slotsInUse(c *core.Core) int {
	q := slotsFor(c)
	q.mu.Lock()
	defer q.mu.Unlock()
	return verifySlots - q.free
}

func slotsWaiting(c *core.Core) int {
	q := slotsFor(c)
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, w := range q.waiting {
		n += len(w)
	}
	return n
}

func TestLoginSlots(t *testing.T) {
	t.Run("drei Absender je 10 Versuche: höchstens zwei, einer je Absender, der Reihe nach", func(t *testing.T) {
		b := newAuthBench(t, nil, true)
		b.setCfg(func(c *config.Config) { c.LoginMaxFailures = 100 })
		calls := make(chan verifyCall, 64)
		useVerifier(t, controlledVerifier(calls))
		senders := []string{"A", "B", "C"}
		ip := map[string]string{"A": "192.0.2.1", "B": "192.0.2.2", "C": "192.0.2.3"}

		active := map[string]verifyCall{} // Absender → laufende Prüfung
		started := map[string][]string{}  // Absender → Versuche in der Reihenfolge ihres Beginns
		take := func(c verifyCall) {
			t.Helper()
			s := c.pw[:1]
			if _, dup := active[s]; dup {
				t.Fatalf("zwei Prüfungen von %s zugleich", s)
			}
			active[s] = c
			if len(active) > 2 {
				t.Fatalf("%d Prüfungen zugleich", len(active))
			}
			started[s] = append(started[s], c.pw)
		}

		// Je Absender höchstens 2 Wartende: refill stellt reihum (A, B, C, A,
		// B, C …) je einen Versuch an, solange ein Absender weniger als 2
		// wartend und noch nicht alle 10 unterwegs hat – Ankunft wie a1 b1 c1
		// a2 b2 c2 … Ein Versuch ist angekommen, sobald er prüft oder in der
		// Schlange wartet.
		var results []<-chan attempt
		sent := map[string]int{}
		queued := map[string]int{} // wartend, nach Sicht des Tests
		refill := func() {
			t.Helper()
			for more := true; more; {
				more = false
				for _, s := range senders {
					if queued[s] >= 2 || sent[s] >= 10 {
						continue
					}
					more = true
					sent[s]++
					results = append(results, b.startLogin(adminUser, fmt.Sprintf("%s-%02d", s, sent[s]), ip[s]))
					select {
					case c := <-calls:
						take(c)
					case d := <-b.clk.waits:
						if d != 5*time.Second {
							t.Fatalf("Wartezeit %v, erwartet 5 s", d)
						}
						queued[s]++
					case <-time.After(10 * time.Second):
						t.Fatalf("Versuch %s-%02d kommt nicht an", s, sent[s])
					}
				}
			}
		}
		refill()
		if slotsWaiting(b.c) != 6 {
			t.Fatalf("%d Wartende, erwartet 6", slotsWaiting(b.c))
		}

		// settle: bis jede zugeteilte Prüfung begonnen und jede beendete ihren
		// Platz freigegeben hat (dann ist belegt == laufend).
		settle := func() {
			t.Helper()
			deadline := time.Now().Add(10 * time.Second)
			for len(active) != slotsInUse(b.c) {
				select {
				case c := <-calls:
					take(c)
					queued[c.pw[:1]]--
				case <-time.After(time.Millisecond):
				}
				if time.Now().After(deadline) {
					t.Fatalf("kommt nicht zur Ruhe: %d laufend, %d belegt", len(active), slotsInUse(b.c))
				}
			}
		}
		for step := 0; ; step++ {
			settle()
			if len(active) == 0 {
				break
			}
			// Gerecht: Kein Absender ist mehr als eine Prüfung voraus.
			lo, hi := 10, 0
			busyOrLeft := 0
			for _, s := range senders {
				n := len(started[s])
				lo, hi = min(lo, n), max(hi, n)
				if _, on := active[s]; on || n < 10 {
					busyOrLeft++
				}
			}
			if hi-lo > 1 {
				t.Fatalf("Schritt %d: ungerecht verteilt: %v", step, started)
			}
			// Kein Platz bleibt leer, solange zwei Absender etwas zu prüfen haben.
			if len(active) != min(2, busyOrLeft) {
				t.Fatalf("Schritt %d: %d laufend, möglich %d", step, len(active), min(2, busyOrLeft))
			}
			// Jede Prüfung dauert 100 ms.
			b.clk.Advance(100 * time.Millisecond)
			for s, c := range active {
				c.reply <- false
				delete(active, s)
			}
			settle()
			refill()
		}
		// Je Absender in der Reihenfolge der Ankunft.
		for _, s := range senders {
			if len(started[s]) != 10 {
				t.Fatalf("%s: %d Prüfungen", s, len(started[s]))
			}
			for i, pw := range started[s] {
				if pw != fmt.Sprintf("%s-%02d", s, i+1) {
					t.Errorf("%s: Reihenfolge %v", s, started[s])
					break
				}
			}
		}
		b.clk.Advance(2 * time.Second) // die letzten Pausen
		for _, ch := range results {
			if r := resultOf(t, ch); r.res != LoginFailed {
				t.Errorf("Ergebnis %v", r.res)
			}
		}
		for _, s := range senders {
			if n, _ := b.loginCount(ip[s]); n != 10 {
				t.Errorf("%s: %d gezählt", s, n)
			}
		}
		if slotsInUse(b.c) != 0 || slotsWaiting(b.c) != 0 {
			t.Errorf("danach belegt %d, wartend %d", slotsInUse(b.c), slotsWaiting(b.c))
		}
	})

	t.Run("wer länger als 5 s wartet: LoginBusy, nicht gezählt", func(t *testing.T) {
		b := newAuthBench(t, nil, true)
		calls := make(chan verifyCall, 16)
		useVerifier(t, controlledVerifier(calls))
		x1 := b.startLogin(adminUser, "x1", "192.0.2.1")
		cx := nextCall(t, calls)
		y1 := b.startLogin(adminUser, "y1", "192.0.2.2")
		cy := nextCall(t, calls)
		waiting := func(what string) <-chan attempt {
			t.Helper()
			ch := b.startLogin(adminUser, adminPass, map[string]string{"z": "192.0.2.3", "x2": "192.0.2.1"}[what])
			b.expectWait(what) // genau 5 s
			return ch
		}
		z := waiting("z")   // ein neuer Absender, beide Plätze belegt
		x2 := waiting("x2") // derselbe Absender wie eine laufende Prüfung
		// Ihre Zeitgeber laufen nicht vor 5 s ab – vorher kann keiner fertig sein.
		b.advanceQuiet(5*time.Second - time.Millisecond)
		b.clk.Advance(time.Millisecond)
		for name, ch := range map[string]<-chan attempt{"z": z, "x2": x2} {
			r := resultOf(t, ch)
			if r.res != LoginBusy || len(r.rec.Header()["Set-Cookie"]) != 0 {
				t.Errorf("%s nach 5 s: %v %q", name, r.res, r.rec.Header()["Set-Cookie"])
			}
		}
		if _, counted := b.loginCount("192.0.2.3"); counted {
			t.Error("ein abgewiesener Wartender ist gezählt")
		}
		if n, _ := b.loginCount("192.0.2.1"); n != 1 {
			t.Errorf("x: %d gezählt, erwartet nur die laufende Prüfung", n)
		}
		if len(b.clk.sleeps) != 0 {
			t.Error("Pause nach LoginBusy")
		}
		if slotsWaiting(b.c) != 0 || slotsInUse(b.c) != 2 {
			t.Errorf("wartend %d, belegt %d", slotsWaiting(b.c), slotsInUse(b.c))
		}
		cx.reply <- false
		cy.reply <- false
		b.finish(x1)
		b.finish(y1)
		if slotsInUse(b.c) != 0 {
			t.Error("Plätze bleiben belegt")
		}
	})

	t.Run("ein Absender bekommt nie zwei Plätze, auch wenn einer frei ist", func(t *testing.T) {
		b := newAuthBench(t, nil, true)
		calls := make(chan verifyCall, 16)
		useVerifier(t, controlledVerifier(calls))
		// Drei Adressen aus demselben /64: ein Absender.
		x1 := b.startLogin(adminUser, "x1", "2001:db8::1")
		cx1 := nextCall(t, calls)
		var later []<-chan attempt
		for _, ip := range []string{"2001:db8::2", "2001:db8::3"} {
			later = append(later, b.startLogin(adminUser, "x", ip))
			select {
			case c := <-calls:
				t.Fatalf("%s prüft, obwohl derselbe Absender schon prüft (ein Platz ist frei)", c.pw)
			case <-b.clk.waits:
			case <-time.After(10 * time.Second):
				t.Fatal("der Versuch kommt nicht an")
			}
		}
		// Der freie Platz geht sofort an einen anderen Absender.
		y := b.startLogin(adminUser, "y", "192.0.2.2")
		cy := nextCall(t, calls)
		cx1.reply <- false
		cy.reply <- false
		// Jetzt prüft genau einer der Wartenden; der andere wartet weiter.
		cx := nextCall(t, calls)
		deadline := time.Now().Add(10 * time.Second)
		for slotsInUse(b.c) != 1 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if n, w := slotsInUse(b.c), slotsWaiting(b.c); n != 1 || w != 1 || len(calls) != 0 {
			t.Fatalf("belegt %d, wartend %d, weitere Prüfungen %d – erwartet 1, 1, 0", n, w, len(calls))
		}
		cx.reply <- false
		nextCall(t, calls).reply <- false
		for _, ch := range append(later, x1, y) {
			if r := b.finish(ch); r.res != LoginFailed {
				t.Errorf("Ergebnis %v", r.res)
			}
		}
	})

	t.Run("ein gesperrter Absender stellt sich nicht an", func(t *testing.T) {
		b := newAuthBench(t, nil, true)
		b.setCfg(func(c *config.Config) { c.LoginMaxFailures = 1 })
		calls := make(chan verifyCall, 16)
		useVerifier(t, controlledVerifier(calls))
		s := b.startLogin(adminUser, "falsch", "192.0.2.9")
		nextCall(t, calls).reply <- false
		if r := b.finish(s); r.res != LoginFailed {
			t.Fatalf("erster Versuch: %v", r.res)
		}
		x := b.startLogin(adminUser, "x", "192.0.2.1")
		y := b.startLogin(adminUser, "y", "192.0.2.2")
		cx, cy := nextCall(t, calls), nextCall(t, calls)
		// Beide Plätze belegt: Der Gesperrte bekommt sofort 429, ohne zu warten.
		if r := resultOf(t, b.startLogin(adminUser, adminPass, "192.0.2.9")); r.res != LoginBlocked {
			t.Errorf("gesperrter Absender bei vollen Plätzen: %v", r.res)
		}
		if len(b.clk.waits) != 0 || slotsWaiting(b.c) != 0 {
			t.Error("ein gesperrter Absender hat sich angestellt")
		}
		cx.reply <- false
		cy.reply <- false
		b.finish(x)
		b.finish(y)
	})

	t.Run("ein Laufzeitfehler in der Prüfung gibt den Platz frei", func(t *testing.T) {
		b := newAuthBench(t, nil, true)
		useVerifier(t, func(hash, pw string) bool { panic("Probe") })
		for range 3 {
			func() {
				defer func() {
					if recover() == nil {
						t.Error("kein Laufzeitfehler")
					}
				}()
				r := b.request("POST", "192.0.2.1", "", map[string][]string{"user": {adminUser}, "password": {"x"}})
				rec := httptest.NewRecorder()
				f, _ := ParseForm(rec, r)
				Login(b.c, rec, r, f)
			}()
		}
		if slotsInUse(b.c) != 0 {
			t.Errorf("nach Laufzeitfehlern %d Plätze belegt", slotsInUse(b.c))
		}
	})
}
