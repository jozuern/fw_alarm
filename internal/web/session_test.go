package web

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"fwalarm/internal/config"
	"fwalarm/internal/model"
)

// Sitzungen (Spec §8; PHP: resume_dashboard_session, is_dashboard_authed,
// dashboard_auth_stamp, refresh_dashboard_cookie, end_dashboard_session).

func keyOfValue(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

func TestSessionRules(t *testing.T) {
	t.Run("erfundene ID: nichts entsteht, kein Cookie", func(t *testing.T) {
		b := newAuthBench(t, nil, true)
		real, _ := b.begin(adminUser, "admin")
		before := b.c.Store.Sessions()
		forged := base64.RawURLEncoding.EncodeToString(make([]byte, 32)) // richtige Form, keine Sitzung
		for _, v := range []string{forged, "abcdefabcdefabcdefabcdefabcdef12", real + "x", real[:42],
			strings.Repeat("A", 5000), "../../etc/passwd", keyOfValue(real), ""} {
			s, ok, rec := b.resume(v)
			if ok || s != nil {
				t.Errorf("Cookie %.20q: angemeldet", v)
			}
			if got := rec.Header()["Set-Cookie"]; len(got) != 0 {
				t.Errorf("Cookie %.20q: Set-Cookie %q", v, got)
			}
		}
		if b.c.Store.Sessions() != before {
			t.Error("eine erfundene ID hat die Sitzungen verändert")
		}
	})

	t.Run("Leerlauf: genau 12 h gilt, eine Sekunde mehr nicht", func(t *testing.T) {
		b := newAuthBench(t, nil, true)
		first, _ := b.begin(adminUser, "admin")
		second, _ := b.begin(adminUser, "admin")
		b.clk.Set(t0.Add(43200 * time.Second))
		if _, ok, _ := b.resume(first); !ok {
			t.Fatal("nach genau 12 h abgemeldet")
		}
		b.clk.Set(t0.Add(43201 * time.Second))
		s, ok, rec := b.resume(second)
		if ok || s != nil || len(rec.Header()["Set-Cookie"]) != 0 {
			t.Errorf("nach 12 h und 1 s: angemeldet %v, Set-Cookie %q", ok, rec.Header()["Set-Cookie"])
		}
		if _, there := b.c.Store.Sessions().Sessions[keyOfValue(second)]; there {
			t.Error("abgelaufene Sitzung bleibt gespeichert")
		}
		if _, ok, _ := b.resume(first); !ok {
			t.Error("die andere Sitzung (eben benutzt) ist mit abgelaufen")
		}
	})

	t.Run("geändertes Passwort beendet nur die Sitzungen dieses Kontos", func(t *testing.T) {
		b := newAuthBench(t, nil, true)
		admin, _ := b.begin(adminUser, "admin")
		reader, _ := b.begin(readerUser, "readonly")
		oldHash := b.cfg.Load().DashboardPasswordHash
		b.setCfg(func(c *config.Config) { c.DashboardPasswordHash = "$2y$10$" + strings.Repeat("x", 53) })
		if s, ok, rec := b.resume(admin); ok || s != nil || len(rec.Header()["Set-Cookie"]) != 0 {
			t.Errorf("Admin nach Passwortwechsel: %v %q", ok, rec.Header()["Set-Cookie"])
		}
		if s, ok, _ := b.resume(reader); !ok || s.Role != "readonly" || s.User != readerUser {
			t.Errorf("Lese-Benutzer nach Passwortwechsel des Admins: %v %+v", ok, s)
		}
		// Zurück auf das alte Passwort: verworfen, nicht nur gesperrt.
		b.setCfg(func(c *config.Config) { c.DashboardPasswordHash = oldHash })
		if _, ok, _ := b.resume(admin); ok {
			t.Error("Admin-Sitzung lebt nach dem Zurückstellen wieder auf")
		}
		admin2, _ := b.begin(adminUser, "admin")
		// Lese-Konto entfernt: seine Sitzung endet, die des Admins bleibt.
		b.setCfg(func(c *config.Config) { c.DashboardReadonlyUser = "" })
		if _, ok, _ := b.resume(reader); ok {
			t.Error("Sitzung des entfernten Lese-Kontos gilt weiter")
		}
		if _, ok, _ := b.resume(admin2); !ok {
			t.Error("Admin-Sitzung endet mit dem Lese-Konto")
		}
		// Benutzername geändert: ebenfalls aus.
		b.setCfg(func(c *config.Config) { c.DashboardUser = "chef" })
		if _, ok, _ := b.resume(admin2); ok {
			t.Error("Admin-Sitzung gilt nach geändertem Benutzernamen")
		}
	})

	t.Run("last_seen höchstens einmal pro Minute", func(t *testing.T) {
		b := newAuthBench(t, nil, true)
		v, _ := b.begin(adminUser, "admin")
		key := keyOfValue(v)
		for _, sec := range []int{1, 30, 59} {
			b.clk.Set(t0.Add(time.Duration(sec) * time.Second))
			before := b.c.Store.Sessions()
			_, ok, rec := b.resume(v)
			if !ok || sessionCookieOf(t, rec) == nil {
				t.Fatalf("nach %d s: angemeldet %v, Cookie erneuert %v", sec, ok, sessionCookieOf(t, rec) != nil)
			}
			if b.c.Store.Sessions() != before {
				t.Errorf("nach %d s geschrieben", sec)
			}
		}
		b.clk.Set(t0.Add(60 * time.Second))
		if _, ok, _ := b.resume(v); !ok {
			t.Fatal("nach 60 s abgemeldet")
		}
		if got := b.c.Store.Sessions().Sessions[key].LastSeen; got != t0.Unix()+60 {
			t.Errorf("last_seen nach 60 s = %d, erwartet %d", got, t0.Unix()+60)
		}
	})

	t.Run("neue ID bei jeder Anmeldung", func(t *testing.T) {
		b := newAuthBench(t, nil, true)
		v1, s1 := b.begin(adminUser, "admin")
		v2, s2 := b.begin(adminUser, "admin")
		if v1 == v2 || s1.CSRF == s2.CSRF {
			t.Fatal("zwei Anmeldungen, dieselbe ID oder dasselbe CSRF-Token")
		}
		form := regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
		for _, v := range []string{v1, v2} {
			raw, err := base64.RawURLEncoding.DecodeString(v)
			if !form.MatchString(v) || err != nil || len(raw) != 32 {
				t.Errorf("Sitzungs-ID %q: nicht 32 Bytes in URL-Base64", v)
			}
		}
		if !regexp.MustCompile(`^[0-9a-f]{48}$`).MatchString(s1.CSRF) {
			t.Errorf("CSRF-Token %q: erwartet 24 Zufallsbytes als Hex (PHP: bin2hex(random_bytes(24)))", s1.CSRF)
		}
		// Gespeichert unter dem SHA-256 – die ID selbst steht nirgends.
		doc, err := json.Marshal(b.c.Store.Sessions())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(doc), v1) || strings.Contains(string(doc), v2) {
			t.Error("die Sitzungs-ID steht in sessions.json")
		}
		stored := b.c.Store.Sessions().Sessions[keyOfValue(v1)]
		if stored.User != adminUser || stored.Role != "admin" || stored.CSRF != s1.CSRF ||
			stored.AuthStamp != b.cfg.Load().AuthStamp("admin") || stored.CreatedAt != t0.Unix() || stored.LastSeen != t0.Unix() {
			t.Errorf("gespeichert: %+v", stored)
		}
		// Eine neue Anmeldung mit dem alten Cookie: Die alte Sitzung ist fort.
		rec := httptest.NewRecorder()
		s3 := Begin(b.c, rec, b.request(http.MethodPost, "192.0.2.1", v1, nil), adminUser, "admin")
		v3 := sessionCookieOf(t, rec).Value
		if v3 == v1 || s3.CSRF == s1.CSRF {
			t.Error("Neuanmeldung übernimmt die alte ID")
		}
		if _, ok, _ := b.resume(v1); ok {
			t.Error("die alte Sitzung gilt nach der Neuanmeldung weiter")
		}
		for _, v := range []string{v2, v3} {
			if _, ok, _ := b.resume(v); !ok {
				t.Error("eine andere Sitzung endete mit der Neuanmeldung")
			}
		}
	})

	t.Run("unbekannte Rolle gilt als Lese-Rolle", func(t *testing.T) {
		b := newAuthBench(t, nil, true)
		v, s := b.begin(readerUser, "chef")
		if s.Role != "readonly" {
			t.Errorf("Begin mit Rolle chef: %q", s.Role)
		}
		if got, ok, _ := b.resume(v); !ok || got.Role != "readonly" {
			t.Errorf("Resume: %v %+v", ok, got)
		}
		// Steht in der Datei eine fremde Rolle, gilt die sichere Richtung.
		b.c.Store.UpdateSessions(func(d *model.Sessions) {
			e := d.Sessions[keyOfValue(v)]
			e.Role = "superuser"
			d.Sessions[keyOfValue(v)] = e
		})
		if got, ok, _ := b.resume(v); !ok || got.Role != "readonly" {
			t.Errorf("Rolle superuser: %v %+v", ok, got)
		}
	})

	t.Run("Abmelden", func(t *testing.T) {
		b := newAuthBench(t, nil, true)
		v, s := b.begin(adminUser, "admin")
		other, _ := b.begin(adminUser, "admin")
		req := b.request(http.MethodPost, "192.0.2.1", v, nil)
		rec := httptest.NewRecorder()
		got, ok := Resume(b.c, rec, req)
		if !ok || got.CSRF != s.CSRF {
			t.Fatal("Resume vor dem Abmelden")
		}
		End(b.c, rec, req)
		if n := len(rec.Header()["Set-Cookie"]); n != 1 {
			t.Fatalf("nach dem Abmelden %d Set-Cookie: %q (die Erneuerung muss fort sein)", n, rec.Header()["Set-Cookie"])
		}
		ck := sessionCookieOf(t, rec)
		if ck.Value != "" || ck.MaxAge >= 0 || !strings.Contains(rec.Header().Get("Set-Cookie"), "Max-Age=0") {
			t.Errorf("Lösch-Cookie: %q", rec.Header().Get("Set-Cookie"))
		}
		if _, there := b.c.Store.Sessions().Sessions[keyOfValue(v)]; there {
			t.Error("Sitzung nach dem Abmelden gespeichert")
		}
		if _, ok, _ := b.resume(v); ok {
			t.Error("nach dem Abmelden angemeldet")
		}
		if _, ok, _ := b.resume(other); !ok {
			t.Error("Abmelden beendet eine andere Sitzung")
		}
		// Ohne (lebende) Sitzung setzt End nichts und schreibt nichts – PHP
		// schickt ohne Sitzung nie ein Lösch-Cookie: ohne Cookie, mit
		// erfundenem, mit dem eben abgemeldeten.
		before := b.c.Store.Sessions()
		for _, cookie := range []string{"", sessionIDText.EncodeToString(make([]byte, 32)), v} {
			rec = httptest.NewRecorder()
			End(b.c, rec, b.request(http.MethodPost, "192.0.2.1", cookie, nil))
			if got := rec.Header()["Set-Cookie"]; len(got) != 0 {
				t.Errorf("Abmelden ohne Sitzung (%.8q): %q", cookie, got)
			}
		}
		if b.c.Store.Sessions() != before {
			t.Error("Abmelden ohne Sitzung hat geschrieben")
		}
	})

	t.Run("ohne Benutzer, CSRF-Token oder Stempel gilt keine", func(t *testing.T) {
		b := newAuthBench(t, nil, true)
		for _, spoil := range []func(*model.Session){
			func(s *model.Session) { s.CSRF = "" },
			func(s *model.Session) { s.User = "" },
			func(s *model.Session) { s.AuthStamp = "" },
		} {
			v, _ := b.begin(adminUser, "admin")
			key := keyOfValue(v)
			b.c.Store.UpdateSessions(func(d *model.Sessions) {
				e := d.Sessions[key]
				spoil(&e)
				d.Sessions[key] = e
			})
			if s, ok, rec := b.resume(v); ok || s != nil || len(rec.Header()["Set-Cookie"]) != 0 {
				t.Errorf("verdorbene Sitzung gilt: %v %q", ok, rec.Header()["Set-Cookie"])
			}
			if _, there := b.c.Store.Sessions().Sessions[key]; there {
				t.Error("verdorbene Sitzung bleibt gespeichert")
			}
		}
	})
}

// Cookie wie PHP dashboard_cookie_options() mit Pfad / : HttpOnly,
// SameSite=Strict, Secure laut vertrautem Proxy, 12 h – beim Anmelden, bei
// jeder Erneuerung und beim Löschen dieselben Eigenschaften.
func TestSessionCookie(t *testing.T) {
	for _, tc := range []struct {
		name   string
		proxy  bool
		proto  string
		secure bool
	}{
		{"direkt, http", false, "", false},
		{"nicht vertraut, X-Forwarded-Proto https", false, "https", false},
		{"vertrauter Proxy, https", true, "https", true},
		{"vertrauter Proxy, http", true, "http", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newAuthBench(t, nil, true)
			if tc.proxy {
				b.setCfg(func(c *config.Config) { c.TrustedProxies = []string{"192.0.2.1"} })
			}
			req := func(cookie string) *http.Request {
				r := b.request(http.MethodGet, "192.0.2.1", cookie, nil)
				if tc.proto != "" {
					r.Header.Set("X-Forwarded-Proto", tc.proto)
				}
				return r
			}
			check := func(what string, rec *httptest.ResponseRecorder, maxAge int) {
				t.Helper()
				line := rec.Header().Get("Set-Cookie")
				ck := sessionCookieOf(t, rec)
				if ck == nil {
					t.Fatalf("%s: kein Cookie", what)
				}
				if ck.Path != "/" || !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode || ck.Secure != tc.secure {
					t.Errorf("%s: %q", what, line)
				}
				if ck.MaxAge != maxAge {
					t.Errorf("%s: Max-Age %d, erwartet %d (%q)", what, ck.MaxAge, maxAge, line)
				}
				if maxAge > 0 && !ck.Expires.Equal(b.clk.Now().Add(12*time.Hour).Truncate(time.Second)) {
					t.Errorf("%s: Expires %v (%q)", what, ck.Expires, line)
				}
				if maxAge < 0 && ck.Expires.After(time.Unix(1, 0)) {
					t.Errorf("%s: Expires %v", what, ck.Expires)
				}
			}
			rec := httptest.NewRecorder()
			Begin(b.c, rec, req(""), adminUser, "admin")
			check("Anmeldung", rec, 43200)
			v := sessionCookieOf(t, rec).Value
			b.clk.Advance(90 * time.Second)
			rec = httptest.NewRecorder()
			if _, ok := Resume(b.c, rec, req(v)); !ok {
				t.Fatal("Resume")
			}
			check("Erneuerung", rec, 43200)
			if sessionCookieOf(t, rec).Value != v {
				t.Error("die Erneuerung trägt eine andere ID")
			}
			rec = httptest.NewRecorder()
			End(b.c, rec, req(v))
			check("Abmelden", rec, -1)
		})
	}
}

// Die Uhr springt zurück: Die Sitzung gilt weiter (last_seen in der Zukunft
// zählt als jetzt) und lebt nach der nächsten Anfrage nicht länger als 12 h.
func TestSessionClockBackwards(t *testing.T) {
	b := newAuthBench(t, nil, true)
	v, _ := b.begin(adminUser, "admin")
	back := t0.Add(-2 * time.Hour)
	b.clk.Set(back)
	if _, ok, rec := b.resume(v); !ok || sessionCookieOf(t, rec) == nil {
		t.Fatal("nach dem Rücksprung der Uhr abgemeldet")
	}
	if got := b.c.Store.Sessions().Sessions[keyOfValue(v)].LastSeen; got != back.Unix() {
		t.Errorf("last_seen = %d, erwartet die neue Zeit %d", got, back.Unix())
	}
	b.clk.Set(back.Add(43201 * time.Second))
	if _, ok, _ := b.resume(v); ok {
		t.Error("die Sitzung lebt nach der Anfrage länger als 12 h (der Rücksprung verlängert sie)")
	}
}

func TestEndAll(t *testing.T) {
	b := newAuthBench(t, nil, true)
	var values []string
	for range 3 {
		v, _ := b.begin(adminUser, "admin")
		values = append(values, v)
	}
	r, _ := b.begin(readerUser, "readonly")
	values = append(values, r)
	EndAll(b.c)
	if n := len(b.c.Store.Sessions().Sessions); n != 0 {
		t.Fatalf("nach EndAll %d Sitzungen", n)
	}
	for _, v := range values {
		if _, ok, rec := b.resume(v); ok || len(rec.Header()["Set-Cookie"]) != 0 {
			t.Error("eine Sitzung gilt nach EndAll")
		}
	}
	// Leer: nichts zu schreiben.
	before := b.c.Store.Sessions()
	EndAll(b.c)
	if b.c.Store.Sessions() != before {
		t.Error("EndAll ohne Sitzungen hat geschrieben")
	}
	// Anmelden geht danach wie immer.
	v, _ := b.begin(adminUser, "admin")
	if _, ok, _ := b.resume(v); !ok {
		t.Error("nach EndAll keine neue Sitzung")
	}
}

func TestSweepSessions(t *testing.T) {
	b := newAuthBench(t, nil, true)
	now := t0.Unix()
	stamp := b.cfg.Load().AuthStamp("admin")
	put := func(key string, s model.Session) {
		b.c.Store.UpdateSessions(func(d *model.Sessions) { d.Sessions[key] = s })
	}
	admin := func(lastSeen int64) model.Session {
		return model.Session{User: adminUser, Role: "admin", CSRF: "c", AuthStamp: stamp, CreatedAt: now - 50000, LastSeen: lastSeen}
	}
	put("frisch", admin(now-100))
	put("genau12h", admin(now-43200))
	put("abgelaufen", admin(now-43201))
	put("uralt", admin(0))
	put("zukunft", admin(now+7200))
	stale := admin(now - 10)
	stale.AuthStamp = "alt"
	put("altes-passwort", stale)
	SweepSessions(b.c)
	got := b.c.Store.Sessions().Sessions
	for _, k := range []string{"abgelaufen", "uralt", "altes-passwort"} {
		if _, there := got[k]; there {
			t.Errorf("%s bleibt", k)
		}
	}
	for _, k := range []string{"frisch", "genau12h", "zukunft"} {
		if _, there := got[k]; !there {
			t.Errorf("%s fehlt", k)
		}
	}
	if got["frisch"].LastSeen != now-100 || got["genau12h"].LastSeen != now-43200 {
		t.Errorf("Sweep ändert last_seen lebender Sitzungen: %+v", got)
	}
	// last_seen aus der Zukunft zählt als jetzt: Die Sitzung lebt höchstens
	// 12 h ab diesem Lauf.
	if got["zukunft"].LastSeen != now {
		t.Errorf("zukunft: last_seen %d, erwartet %d", got["zukunft"].LastSeen, now)
	}
	// Nichts zu tun: nichts geschrieben.
	before := b.c.Store.Sessions()
	SweepSessions(b.c)
	if b.c.Store.Sessions() != before {
		t.Error("Sweep ohne Arbeit hat geschrieben")
	}
	b.clk.Set(t0.Add(43201 * time.Second))
	SweepSessions(b.c)
	if n := len(b.c.Store.Sessions().Sessions); n != 0 {
		t.Errorf("12 h später: %d Sitzungen übrig", n)
	}
}

// CSRFMatches vergleicht in konstanter Zeit; ein leeres Token passt nie.
func TestSessionCSRFMatches(t *testing.T) {
	s := &Session{User: adminUser, Role: "admin", CSRF: "abc123"}
	if !s.CSRFMatches("abc123") {
		t.Error("richtiges Token abgelehnt")
	}
	for _, sent := range []string{"", "abc12", "abc1234", "ABC123", " abc123"} {
		if s.CSRFMatches(sent) {
			t.Errorf("%q angenommen", sent)
		}
	}
	if (&Session{}).CSRFMatches("") || (*Session)(nil).CSRFMatches("abc123") {
		t.Error("leeres Token oder keine Sitzung passt")
	}
	src, err := os.ReadFile("session.go")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?s)func \(s \*Session\) CSRFMatches.*?subtle\.ConstantTimeCompare`).Match(src) {
		t.Error("CSRFMatches vergleicht nicht mit subtle.ConstantTimeCompare")
	}
}

// Jeder Wert des Sitzungs-Cookies: kein Absturz, keine Sitzung, kein
// Set-Cookie, nichts geschrieben.
func FuzzSessionCookie(f *testing.F) {
	b := newAuthBench(f, nil, true)
	real, _ := b.begin(adminUser, "admin")
	for _, seed := range []string{real[:42], real + "x", strings.ToUpper(real), keyOfValue(real), "",
		"abcdefabcdefabcdefabcdefabcdef12", sessionIDText.EncodeToString(make([]byte, 32)), `"` + real[:41] + `"`,
		"a; " + sessionCookie + "=b", strings.Repeat("A", 43), "\x00\xff"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, v string) {
		if strings.Contains(v, real) {
			return // die eine echte ID (der Fuzzer findet sie nicht)
		}
		before := b.c.Store.Sessions()
		req := httptest.NewRequest(http.MethodGet, "/index.php", nil)
		req.Header.Set("Cookie", sessionCookie+"="+v)
		rec := httptest.NewRecorder()
		if s, ok := Resume(b.c, rec, req); ok || s != nil {
			t.Fatalf("Cookie %q: Sitzung %+v", v, s)
		}
		if got := rec.Header()["Set-Cookie"]; len(got) != 0 {
			t.Fatalf("Cookie %q: Set-Cookie %q", v, got)
		}
		if b.c.Store.Sessions() != before {
			t.Fatalf("Cookie %q: Sitzungen verändert", v)
		}
	})
}
