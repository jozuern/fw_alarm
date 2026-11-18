package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fwalarm/internal/config"
)

// Bekannte Geräte zuerst (Spec §6.8, §10): Ausweis-Cookie boss925_device,
// Vorzugsreihe vor der allgemeinen, Obergrenzen der Reihen.

// attackerIP: die n-te Adresse eines Angreifers (Netz für Messungen,
// RFC 2544 – keine echte Adresse).
func attackerIP(n int) string { return fmt.Sprintf("198.18.%d.%d", n/250, n%250+1) }

// expectWait verlangt, dass der eben gestartete Versuch sich anstellt (der
// Zeitgeber von 5 s steht).
func (b *authBench) expectWait(what string) {
	b.t.Helper()
	select {
	case d := <-b.clk.waits:
		if d != verifyMaxWait {
			b.t.Fatalf("%s wartet %v, erwartet %v", what, d, verifyMaxWait)
		}
	case <-time.After(10 * time.Second):
		b.t.Fatalf("%s stellt sich nicht an", what)
	}
}

// expectAtOnce verlangt, dass der Versuch ohne zu warten fertig ist –
// stellt er sich an, ist das ein Fehler (dann zählt er für drain).
func (b *authBench) expectAtOnce(what string, ch <-chan attempt) (attempt, bool) {
	b.t.Helper()
	select {
	case a := <-ch:
		return a, true
	case <-b.clk.waits:
		b.t.Errorf("%s stellt sich an, statt sofort beantwortet zu werden", what)
		return attempt{}, false
	case <-time.After(10 * time.Second):
		b.t.Fatalf("%s endet nicht", what)
		return attempt{}, false
	}
}

// drain beendet offene Versuche: Jede Prüfung scheitert, die Uhr läuft
// weiter, bis alle fertig sind (Wartende laufen nach 5 s ab).
func (b *authBench) drain(calls chan verifyCall, chans ...<-chan attempt) {
	b.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for _, ch := range chans {
		for done := false; !done; {
			select {
			case <-ch:
				done = true
			case c := <-calls:
				c.reply <- false
			case <-time.After(time.Millisecond):
				b.clk.Advance(time.Second)
			}
			if time.Now().After(deadline) {
				b.t.Fatal("offene Versuche enden nicht")
			}
		}
	}
}

// occupy belegt beide Plätze mit Prüfungen, die der Test von Hand beendet.
func (b *authBench) occupy(calls chan verifyCall) (running []verifyCall, chans []<-chan attempt) {
	b.t.Helper()
	for i := range verifySlots {
		chans = append(chans, b.startLogin(adminUser, "belegt", fmt.Sprintf("203.0.113.%d", 200+i)))
		running = append(running, nextCall(b.t, calls))
	}
	return running, chans
}

// Der Kern der Durchsicht: Die allgemeine Reihe ist bis zur Obergrenze voll
// und wird laufend nachgefüllt (jede Prüfung dauert 100 ms). Ein Versuch mit
// gültigem Ausweis kommt dran, sobald eine der beiden laufenden Prüfungen
// endet – nie LoginBusy. Derselbe Versuch ohne Ausweis bekommt LoginBusy.
func TestKnownDeviceFirst(t *testing.T) {
	for _, withPass := range []bool{true, false} {
		name := "ohne Ausweis: sofort 503"
		if withPass {
			name = "mit Ausweis: an der nächsten freien Stelle"
		}
		t.Run(name, func(t *testing.T) {
			b := newAuthBench(t, nil, true)
			b.setCfg(func(c *config.Config) { c.LoginMaxFailures = 1000 })
			calls := make(chan verifyCall, 256)
			useVerifier(t, controlledVerifier(calls))
			pass := devicePassFor(b.cfg.Load(), "admin", testID(7))

			var all []<-chan attempt
			next := 0
			var running []verifyCall
			for range verifySlots {
				all = append(all, b.startLogin(adminUser, "angreifer", attackerIP(next)))
				next++
				running = append(running, nextCall(t, calls))
			}
			fill := func() {
				for slotsWaiting(b.c) < 64 {
					all = append(all, b.startLogin(adminUser, "angreifer", attackerIP(next)))
					next++
					b.expectWait("Angreifer")
				}
			}
			fill()
			// Ein paar Runden Betrieb unter Angriff: Eine Prüfung endet nach
			// 100 ms, der nächste Wartende rückt nach, die Reihe wird nachgefüllt.
			for range 3 {
				b.clk.Advance(100 * time.Millisecond)
				running[0].reply <- false
				running = append(running[1:], nextCall(t, calls))
				fill()
			}

			var hdr [][2]string
			if withPass {
				hdr = append(hdr, withDevice(pass))
			}
			admin := b.startLogin(adminUser, adminPass, "192.0.2.200", hdr...)
			if !withPass {
				if r, ok := b.expectAtOnce("Admin ohne Ausweis", admin); !ok {
					all = append(all, admin) // stellte sich an: drain beendet ihn
				} else if r.res != LoginBusy {
					t.Errorf("Admin ohne Ausweis bei voller Reihe: %v, erwartet LoginBusy", r.res)
				}
				if _, counted := b.loginCount("192.0.2.200"); counted {
					t.Error("LoginBusy wurde gezählt")
				}
				for _, c := range running {
					c.reply <- false
				}
				b.drain(calls, all...)
				return
			}
			b.expectWait("Admin mit Ausweis")
			if prio, general := laneWaiting(b.c); prio != 1 || general != 64 {
				t.Errorf("Vorzugsreihe %d, allgemeine %d – erwartet 1 und 64", prio, general)
			}
			b.clk.Advance(100 * time.Millisecond)
			running[0].reply <- false
			running = running[1:]
			c := nextCall(t, calls)
			if c.pw != adminPass {
				c.reply <- false
				for _, r := range running {
					r.reply <- false
				}
				b.drain(calls, append(all, admin)...)
				t.Fatalf("nach der ersten freien Stelle prüft %q, nicht der Admin", c.pw)
			}
			c.reply <- true
			r := resultOf(t, admin)
			if r.res != LoginOK || sessionCookieOf(t, r.rec) == nil {
				t.Errorf("Admin mit Ausweis: %v", r.res)
			}
			if deviceCookieOf(t, r.rec) != nil {
				t.Error("ein gültiger Ausweis für dieselbe Rolle wurde ersetzt")
			}
			for _, r := range running {
				r.reply <- false
			}
			b.drain(calls, all...)
		})
	}
}

// Was KEIN Ausweis ist, gibt keinen Vorrang – ohne Fehler, ohne dass etwas
// gesetzt wird. Ein gültiger Ausweis gibt Vorrang, sonst nichts: gezählt,
// eine Prüfung, 1 s Pause, gesperrt wie jeder andere.
func TestKnownDevicePassRules(t *testing.T) {
	b := newAuthBench(t, nil, true)
	calls := make(chan verifyCall, 64)
	verified := 0
	useVerifier(t, func(hash, pw string) bool {
		verified++ // nur aus dem Versuch, der gerade prüft – nacheinander
		return controlledVerifier(calls)(hash, pw)
	})
	cfg0 := b.cfg.Load()
	admin := devicePassFor(cfg0, "admin", testID(1))
	reader := devicePassFor(cfg0, "readonly", testID(2))
	head := admin[:strings.LastIndex(admin, ".")]
	readerMAC := reader[strings.LastIndex(reader, ".")+1:]
	lastChar := func(s string) string {
		if strings.HasSuffix(s, "A") {
			return s[:len(s)-1] + "B"
		}
		return s[:len(s)-1] + "A"
	}
	running, chans := b.occupy(calls)
	sender := 0
	general := func(what, cookie string) {
		t.Helper()
		sender++
		ip := fmt.Sprintf("192.0.2.%d", sender)
		ch := b.startLogin(adminUser, adminPass, ip, [2]string{"Cookie", deviceCookie + "=" + cookie})
		b.expectWait(what)
		if prio, gen := laneWaiting(b.c); prio != 0 || gen != 1 {
			t.Errorf("%s: Vorzugsreihe %d, allgemeine %d – erwartet 0 und 1", what, prio, gen)
		}
		b.clk.Advance(verifyMaxWait)
		r := resultOf(t, ch)
		if r.res != LoginBusy || len(r.rec.Header()["Set-Cookie"]) != 0 {
			t.Errorf("%s: %v %q", what, r.res, r.rec.Header()["Set-Cookie"])
		}
		if _, counted := b.loginCount(ip); counted {
			t.Errorf("%s: gezählt", what)
		}
	}
	for what, cookie := range map[string]string{
		"Prüfsumme gefälscht": lastChar(admin),
		"gekürzt":             admin[:len(admin)-1],
		"verlängert":          admin + "A",
		"Version v2":          "v2" + admin[2:],
		"Rolle getauscht":     "v1.r." + admin[len("v1.a."):],
		"Prüfsumme mit dem Schlüssel der anderen Rolle": head + "." + readerMAC,
		"unbekannte Rolle":      "v1.x." + admin[len("v1.a."):],
		"fünf Teile":            head + ".x." + readerMAC[2:],
		"mit Anführungszeichen": `"` + admin[:len(admin)-2] + `"`,
		"leer":                  "",
		"Müll":                  "%%%;;;",
	} {
		general(what, cookie)
	}
	// Vor einem Passwortwechsel ausgestellt: wertlos.
	b.setCfg(func(c *config.Config) { c.DashboardPasswordHash = devicePassTestHash() })
	general("Ausweis von vor dem Passwortwechsel", admin)
	b.cfg.Store(cfg0)
	// Lese-Konto entfernt: sein Ausweis ist wertlos.
	b.setCfg(func(c *config.Config) { c.DashboardReadonlyUser = "" })
	general("Ausweis des entfernten Lese-Kontos", reader)
	b.cfg.Store(cfg0)

	// Ein gültiger Ausweis (auch der des Lese-Kontos, bei Anmeldung als
	// Admin): Vorrang – sonst nichts.
	b.setCfg(func(c *config.Config) { c.LoginMaxFailures = 1 })
	const ip = "192.0.2.150"
	ch := b.startLogin(adminUser, "falsch", ip, withDevice(reader))
	b.expectWait("gültiger Ausweis")
	if prio, gen := laneWaiting(b.c); prio != 1 || gen != 0 {
		t.Errorf("gültiger Ausweis: Vorzugsreihe %d, allgemeine %d", prio, gen)
	}
	before := verified
	running[0].reply <- false
	c := nextCall(t, calls)
	if c.pw != "falsch" {
		t.Fatalf("es prüft %q", c.pw)
	}
	if n, _ := b.loginCount(ip); n != 1 {
		t.Errorf("mit Ausweis vor der Prüfung %d gezählt, erwartet 1", n)
	}
	c.reply <- false
	// Zwei Pausen von 1 s: die des eben beendeten Platzhalters und die dieses
	// Fehlversuchs – erst wenn beide stehen, läuft die Uhr weiter.
	for range 2 {
		select {
		case d := <-b.clk.sleeps:
			if d != time.Second {
				t.Errorf("Pause %v", d)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("keine Pause nach dem Fehlversuch mit Ausweis")
		}
	}
	b.clk.Advance(time.Second)
	if r := resultOf(t, ch); r.res != LoginFailed || deviceCookieOf(t, r.rec) != nil {
		t.Errorf("Fehlversuch mit Ausweis: %v", r.res)
	}
	if verified-before != 1 {
		t.Errorf("%d Prüfungen für einen Versuch mit Ausweis", verified-before)
	}
	// Gesperrt: auch mit Ausweis sofort 429, ohne sich anzustellen.
	if r, ok := b.expectAtOnce("gesperrt mit Ausweis", b.startLogin(adminUser, adminPass, ip, withDevice(reader))); ok && r.res != LoginBlocked {
		t.Errorf("gesperrt mit Ausweis: %v", r.res)
	}
	for _, c := range running[1:] {
		c.reply <- false
	}
	b.drain(calls, chans...)
}

// devicePassTestHash: ein anderer brauchbarer bcrypt-Hash.
func devicePassTestHash() string {
	return "$2y$04$arQ57ZxGZu0qqkyhgNiBc.Ri6mtxzvUCvCwP8PvCH/TfBrFX9abKO"
}

// Ein Ausweis hält höchstens EINEN Platz in der Vorzugsreihe (wartend oder in
// Prüfung) – ein gestohlener füllt sie nicht.
func TestKnownDeviceOneSeatPerPass(t *testing.T) {
	b := newAuthBench(t, nil, true)
	b.setCfg(func(c *config.Config) { c.LoginMaxFailures = 100 })
	calls := make(chan verifyCall, 128)
	useVerifier(t, controlledVerifier(calls))
	pass := devicePassFor(b.cfg.Load(), "admin", testID(9))
	running, all := b.occupy(calls)
	for i := range 50 {
		all = append(all, b.startLogin(adminUser, fmt.Sprintf("p-%02d", i), attackerIP(i), withDevice(pass)))
		b.expectWait(fmt.Sprintf("Versuch %d", i))
	}
	if prio, general := laneWaiting(b.c); prio != 1 || general != 49 {
		t.Fatalf("Vorzugsreihe %d, allgemeine %d – erwartet 1 und 49", prio, general)
	}
	// Der Erste mit dem Ausweis kommt dran und hält den Platz, solange er prüft.
	running[0].reply <- false
	c := nextCall(t, calls)
	if c.pw != "p-00" {
		t.Errorf("es prüft %q, erwartet den Ersten mit Ausweis", c.pw)
	}
	all = append(all, b.startLogin(adminUser, "p-50", attackerIP(50), withDevice(pass)))
	b.expectWait("Versuch 50")
	if prio, general := laneWaiting(b.c); prio != 0 || general != 50 {
		t.Errorf("während der Prüfung: Vorzugsreihe %d, allgemeine %d – erwartet 0 und 50", prio, general)
	}
	// Nach seiner Prüfung ist der Platz wieder frei.
	c.reply <- false
	c2 := nextCall(t, calls) // der Nächste aus der allgemeinen Reihe
	all = append(all, b.startLogin(adminUser, "p-51", attackerIP(51), withDevice(pass)))
	b.expectWait("Versuch 51")
	if prio, _ := laneWaiting(b.c); prio != 1 {
		t.Errorf("nach der Prüfung: Vorzugsreihe %d, erwartet 1", prio)
	}
	c2.reply <- false
	running[1].reply <- false
	b.drain(calls, all...)
}

// "Höchstens eine Prüfung je Absender" gilt auch in der Vorzugsreihe: Prüft
// der Absender gerade, bleibt sein Versuch mit Ausweis stehen, auch wenn ein
// Platz frei wird – bis seine laufende Prüfung endet.
func TestKnownDeviceOneCheckPerSender(t *testing.T) {
	b := newAuthBench(t, nil, true)
	calls := make(chan verifyCall, 16)
	useVerifier(t, controlledVerifier(calls))
	pass := devicePassFor(b.cfg.Load(), "admin", testID(3))
	const ip = "192.0.2.40"
	first := b.startLogin(adminUser, "erster", ip)
	c1 := nextCall(t, calls)
	other := b.startLogin(adminUser, "anderer", "192.0.2.41")
	c2 := nextCall(t, calls)
	second := b.startLogin(adminUser, "zweiter", ip, withDevice(pass))
	b.expectWait("zweiter Versuch mit Ausweis")
	c2.reply <- false // ein Platz wird frei
	deadline := time.Now().Add(10 * time.Second)
	for slotsInUse(b.c) != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	// release und dispatch laufen unter einer Sperre: Ist der Platz frei, ist
	// über ihn entschieden.
	if prio, _ := laneWaiting(b.c); slotsInUse(b.c) != 1 || prio != 1 || len(calls) != 0 {
		t.Fatalf("belegt %d, Vorzugsreihe %d, Prüfungen %d – derselbe Absender prüft zweimal zugleich",
			slotsInUse(b.c), prio, len(calls))
	}
	c1.reply <- false
	if c := nextCall(t, calls); c.pw != "zweiter" {
		t.Errorf("es prüft %q", c.pw)
	} else {
		c.reply <- false
	}
	b.drain(calls, first, other, second)
}

// Das Ausweis-Cookie: nur in der Antwort auf eine gelungene Anmeldung, für
// die Rolle, die sich anmeldete, und nur, wenn kein gültiger für diese Rolle
// mitkam. Abmelden, EndAll, Fehlversuch, Sperre und 503 fassen es nie an.
func TestKnownDeviceCookie(t *testing.T) {
	for _, tc := range []struct {
		name          string
		proxy, secure bool
	}{{"direkt", false, false}, {"vertrauter Proxy mit https", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			b := newAuthBench(t, nil, true)
			hdr := [][2]string{}
			if tc.proxy {
				b.setCfg(func(c *config.Config) { c.TrustedProxies = []string{"192.0.2.1"} })
				hdr = append(hdr, [2]string{"X-Forwarded-Proto", "https"})
			}
			a := b.login(adminUser, adminPass, "192.0.2.1", hdr...)
			ck := deviceCookieOf(t, a.rec)
			if a.res != LoginOK || ck == nil {
				t.Fatalf("Anmeldung ohne Ausweis: %v, Ausweis %v", a.res, ck)
			}
			if ck.Path != "/" || !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode || ck.Secure != tc.secure ||
				ck.MaxAge != 400*24*3600 || !ck.Expires.Equal(b.clk.Now().Add(400*24*time.Hour)) {
				t.Errorf("Eigenschaften: %q", a.rec.Header()["Set-Cookie"])
			}
			if p, ok := parseDevicePass(ck.Value, b.cfg.Load()); !ok || p.role != "admin" || len(ck.Value) != 71 {
				t.Errorf("Ausweis %q: %v %+v", ck.Value, ok, p)
			}
			first := ck.Value
			// Mit gültigem Ausweis derselben Rolle: bleibt (keine neue id).
			a = b.login(adminUser, adminPass, "192.0.2.1", append(hdr, withDevice(first))...)
			if a.res != LoginOK || deviceCookieOf(t, a.rec) != nil || sessionCookieOf(t, a.rec) == nil {
				t.Errorf("mit gültigem Ausweis: %v, %q", a.res, a.rec.Header()["Set-Cookie"])
			}
			// Mit dem Ausweis der anderen Rolle oder einem ungültigen: neu.
			for what, sent := range map[string]string{
				"Lese-Benutzer mit Admin-Ausweis": first,
				"ungültiger Ausweis":              first[:len(first)-1] + "x",
			} {
				user, pw := adminUser, adminPass
				if strings.HasPrefix(what, "Lese") {
					user, pw = readerUser, readerPass
				}
				a = b.login(user, pw, "192.0.2.1", append(hdr, withDevice(sent))...)
				ck := deviceCookieOf(t, a.rec)
				if a.res != LoginOK || ck == nil || ck.Value == first {
					t.Errorf("%s: %v, Ausweis %v", what, a.res, ck)
					continue
				}
				wantRole := "admin"
				if user == readerUser {
					wantRole = "readonly"
				}
				if p, ok := parseDevicePass(ck.Value, b.cfg.Load()); !ok || p.role != wantRole {
					t.Errorf("%s: neuer Ausweis %+v %v", what, p, ok)
				}
			}
			// Abmelden und EndAll fassen ihn nicht an.
			s := sessionCookieOf(t, a.rec).Value
			req := b.request(http.MethodPost, "192.0.2.1", s, nil)
			req.Header.Add("Cookie", deviceCookie+"="+first)
			rec := httptest.NewRecorder()
			if _, ok := Resume(b.c, rec, req); !ok {
				t.Fatal("Resume")
			}
			End(b.c, rec, req)
			EndAll(b.c)
			if deviceCookieOf(t, rec) != nil {
				t.Errorf("Abmelden setzt den Ausweis: %q", rec.Header()["Set-Cookie"])
			}
			// Fehlversuch und Sperre setzen ihn nicht.
			b.setCfg(func(c *config.Config) { c.LoginMaxFailures = 1 })
			for _, want := range []LoginResult{LoginFailed, LoginBlocked} {
				a = b.login(adminUser, "falsch", "192.0.2.77", hdr...)
				if a.res != want || len(a.rec.Header()["Set-Cookie"]) != 0 {
					t.Errorf("%v: %q", a.res, a.rec.Header()["Set-Cookie"])
				}
			}
		})
	}
	t.Run("503 setzt keinen", func(t *testing.T) {
		b := newAuthBench(t, nil, true)
		calls := make(chan verifyCall, 128)
		useVerifier(t, controlledVerifier(calls))
		running, all := b.occupy(calls)
		for i := range 64 {
			all = append(all, b.startLogin(adminUser, "x", attackerIP(i)))
			b.expectWait("Angreifer")
		}
		if r, ok := b.expectAtOnce("bei voller Reihe", b.startLogin(adminUser, adminPass, "192.0.2.9")); ok &&
			(r.res != LoginBusy || len(r.rec.Header()["Set-Cookie"]) != 0) {
			t.Errorf("bei voller Reihe: %v %q", r.res, r.rec.Header()["Set-Cookie"])
		}
		for _, c := range running {
			c.reply <- false
		}
		b.drain(calls, all...)
	})
}

// Obergrenzen (Spec §6.8, Durchsicht von Aufgabe 12): allgemeine Reihe
// höchstens 64 Wartende, höchstens 2 je Absender – darüber sofort 503,
// nicht gezählt, niemand wartet; Vorzugsreihe höchstens 16 Ausweise – der
// 17. wartet in der allgemeinen Reihe.
func TestLoginQueueCaps(t *testing.T) {
	t.Run("der 65. Wartende", func(t *testing.T) {
		b := newAuthBench(t, nil, true)
		calls := make(chan verifyCall, 128)
		useVerifier(t, controlledVerifier(calls))
		running, all := b.occupy(calls)
		for i := range 64 {
			all = append(all, b.startLogin(adminUser, "x", attackerIP(i)))
			b.expectWait(fmt.Sprintf("Wartender %d", i+1))
		}
		pending := b.clk.Pending()
		if r, ok := b.expectAtOnce("der 65.", b.startLogin(adminUser, "x", attackerIP(64))); ok && r.res != LoginBusy {
			t.Errorf("der 65.: %v", r.res)
		}
		if _, counted := b.loginCount(attackerIP(64)); counted {
			t.Error("der 65. ist gezählt")
		}
		if slotsWaiting(b.c) != 64 || b.clk.Pending() != pending {
			t.Errorf("wartend %d, Zeitgeber %d → %d", slotsWaiting(b.c), pending, b.clk.Pending())
		}
		for _, c := range running {
			c.reply <- false
		}
		b.drain(calls, all...)
	})
	t.Run("der dritte Wartende eines Absenders", func(t *testing.T) {
		b := newAuthBench(t, nil, true)
		calls := make(chan verifyCall, 16)
		useVerifier(t, controlledVerifier(calls))
		const ip = "192.0.2.5"
		all := []<-chan attempt{b.startLogin(adminUser, "x1", ip)}
		first := nextCall(t, calls)
		for i := 2; i <= 3; i++ {
			all = append(all, b.startLogin(adminUser, fmt.Sprintf("x%d", i), ip))
			b.expectWait(fmt.Sprintf("x%d", i))
		}
		if r, ok := b.expectAtOnce("x4", b.startLogin(adminUser, "x4", ip)); ok && r.res != LoginBusy {
			t.Errorf("dritter Wartender: %v", r.res)
		}
		if n, _ := b.loginCount(ip); n != 1 {
			t.Errorf("%d gezählt, erwartet nur die laufende Prüfung", n)
		}
		// Ein anderer Absender stellt sich weiter an (hier: Platz frei → sofort).
		other := b.startLogin(adminUser, "y", "192.0.2.6")
		nextCall(t, calls).reply <- false
		first.reply <- false
		b.drain(calls, append(all, other)...)
	})
	t.Run("der 17. Ausweis wartet in der allgemeinen Reihe", func(t *testing.T) {
		b := newAuthBench(t, nil, true)
		calls := make(chan verifyCall, 64)
		useVerifier(t, controlledVerifier(calls))
		running, all := b.occupy(calls)
		for i := range 17 {
			pass := devicePassFor(b.cfg.Load(), "admin", testID(byte(100+i)))
			all = append(all, b.startLogin(adminUser, "x", attackerIP(i), withDevice(pass)))
			b.expectWait(fmt.Sprintf("Ausweis %d", i+1))
		}
		if prio, general := laneWaiting(b.c); prio != 16 || general != 1 {
			t.Errorf("Vorzugsreihe %d, allgemeine %d – erwartet 16 und 1", prio, general)
		}
		for _, c := range running {
			c.reply <- false
		}
		b.drain(calls, all...)
	})
}

// Jeder Cookie-Wert: kein Absturz; als Ausweis gilt er nur, wenn er genau
// die Form hat, die devicePassFor für seine id schreibt.
func FuzzDevicePass(f *testing.F) {
	admin, reader := testHashes()
	cfg := &config.Config{DashboardUser: adminUser, DashboardPasswordHash: admin,
		DashboardReadonlyUser: readerUser, DashboardReadonlyPasswordHash: reader}
	a := devicePassFor(cfg, "admin", testID(1))
	r := devicePassFor(cfg, "readonly", testID(2))
	for _, seed := range []string{a, r, a[:70], a + "x", "v2" + a[2:], "v1.r." + a[5:], `"` + a + `"`,
		strings.ReplaceAll(a, ".", ".."), strings.ToUpper(a), "", "v1", "v1.a..", "v1.a.x.y", "....",
		a[:len(a)-1] + "=", "v1.a." + strings.Repeat("A", 22) + "." + strings.Repeat("A", 43)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, v string) {
		p, ok := parseDevicePass(v, cfg)
		if ok {
			if p.role != "admin" && p.role != "readonly" {
				t.Fatalf("Rolle %q", p.role)
			}
			id, err := deviceText.DecodeString(p.id)
			if err != nil || devicePassFor(cfg, p.role, id) != v {
				t.Fatalf("%q gilt, ist aber nicht die Form von devicePassFor", v)
			}
		}
		req := httptest.NewRequest(http.MethodPost, "/index.php", nil)
		req.Header.Set("Cookie", deviceCookie+"="+v)
		if q, ok := devicePassOf(req, cfg); ok {
			id, err := deviceText.DecodeString(q.id)
			if err != nil || devicePassFor(cfg, q.role, id) == "" {
				t.Fatalf("über das Cookie gilt %q", v)
			}
		}
	})
}
