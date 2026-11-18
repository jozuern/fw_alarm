package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"fwalarm/internal/config"
	"fwalarm/internal/model"
)

// Die Vergleichsdateien in testdata/ sind die Ausgabe von PHP selbst
// (render_login_page/render_app_page in nas_dashboard/lib/shell.php), einmal
// aufgenommen im Repo (Git Bash oder Linux, PHP 8.3) mit:
//
//	inc='require "nas_dashboard/lib/common.php"; require "nas_dashboard/lib/shell.php";'
//	cfg='$d = "/" . ltrim(strtr(preg_replace("/^[A-Za-z]:/", "", sys_get_temp_dir()), "\\", "/"), "/") . "/fw-golden-leer";
//	     $c = ["data_dir" => $d, "public_base_url" => "https://alarm.example.org", "mute_max_seconds" => 14400];'
//	php -r "$inc"' render_login_page("", "");' > internal/web/testdata/login.html
//	php -r "$inc"' render_login_page("Login fehlgeschlagen.", "admin");' > internal/web/testdata/login-error.html
//	php -r "$inc$cfg"' render_app_page($c, "0123456789abcdef0123456789abcdef0123456789abcdef", "admin", "admin");' > internal/web/testdata/app-admin.html
//	php -r "$inc$cfg"' render_app_page($c, "fedcba9876543210fedcba9876543210fedcba9876543210", "readonly", "leser");' > internal/web/testdata/app-reader.html
//
// (data_dir: ein leerer Ordner – mute_max_seconds liest dort app_settings,
// findet nichts und nimmt den Wert der Konfiguration; data_dir() legt ihn
// an.) Verglichen wird Byte für Byte, nachdem ?v=<Ziffern> zu ?v=N wurde
// (PHP nimmt die Dateizeit, Go AssetVersion). Die Eingaben enthalten keine
// Zeichen, die html/template anders maskiert als htmlspecialchars (" ' +).

var versionParam = regexp.MustCompile(`\?v=[0-9]+`)

func normalizeVersions(s string) string { return versionParam.ReplaceAllString(s, "?v=N") }

func golden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// pageBench: Prüfstand der Anmeldung mit den Werten der Vergleichsdateien.
func pageBench(t *testing.T) *authBench {
	b := newAuthBench(t, nil, true)
	b.setCfg(func(c *config.Config) {
		c.PublicBaseURL = "https://alarm.example.org"
		c.MuteMaxSeconds = 14400
	})
	return b
}

func TestPagesEqualPHP(t *testing.T) {
	b := pageBench(t)
	cases := []struct {
		file   string
		render func(w http.ResponseWriter)
	}{
		{"login.html", func(w http.ResponseWriter) { renderLogin(b.c, w, 200, "", "") }},
		{"login-error.html", func(w http.ResponseWriter) { renderLogin(b.c, w, 200, "Login fehlgeschlagen.", "admin") }},
		{"app-admin.html", func(w http.ResponseWriter) {
			renderApp(b.c, w, 200, &Session{User: "admin", Role: "admin", CSRF: "0123456789abcdef0123456789abcdef0123456789abcdef"})
		}},
		{"app-reader.html", func(w http.ResponseWriter) {
			renderApp(b.c, w, 200, &Session{User: "leser", Role: "readonly", CSRF: "fedcba9876543210fedcba9876543210fedcba9876543210"})
		}},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		tc.render(rec)
		want := normalizeVersions(golden(t, tc.file))
		got := rec.Body.String()
		if normalizeVersions(got) != want {
			t.Errorf("%s: weicht von PHP ab\n--- Go ---\n%s\n--- PHP ---\n%s", tc.file, got, want)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=UTF-8" {
			t.Errorf("%s: Content-Type %q", tc.file, ct)
		}
		// Jede Datei mit der Version, unter der sie ausgeliefert wird.
		for _, m := range regexp.MustCompile(`"((?:assets/)[^"?]+)\?v=([0-9]+)"`).FindAllStringSubmatch(got, -1) {
			if v := AssetVersion(m[1]); v == "" || v != m[2] {
				t.Errorf("%s: %s?v=%s, ausgeliefert wird Version %q", tc.file, m[1], m[2], v)
			}
		}
		if n := strings.Count(got, "?v="); n < 4 {
			t.Errorf("%s: nur %d Versions-Parameter", tc.file, n)
		}
	}
}

// Eine gespeicherte Rückschaltdauer gilt auch auf der Seite (mute-max), die
// Rolle wird wie bei der Sitzung gelesen.
func TestAppPageValues(t *testing.T) {
	b := pageBench(t)
	if err := b.c.Store.Update(context.Background(), func(d *model.State) error {
		d.Settings.MuteMaxSeconds = 7200
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	renderApp(b.c, rec, 429, &Session{User: "x", Role: "irgendwas", CSRF: "c"})
	body := rec.Body.String()
	for _, want := range []string{`<meta name="mute-max" content="7200">`, `<meta name="role" content="readonly">`} {
		if !strings.Contains(body, want) {
			t.Errorf("fehlt: %s", want)
		}
	}
	if rec.Code != 429 {
		t.Errorf("Status %d", rec.Code)
	}
}

// serveIndex beantwortet r mit IndexHandler (umhüllt wie in app) und stellt
// die Uhr um jede Pause weiter, in der die Anmeldung schläft.
func (b *authBench) serveIndex(r *http.Request) *httptest.ResponseRecorder {
	b.t.Helper()
	h := SecurityHeaders(b.c, IndexHandler(b.c))
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h(rec, r)
	}()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-done:
			return rec
		case d := <-b.clk.sleeps:
			b.clk.Advance(d)
		case <-deadline:
			b.t.Fatal("index.php antwortet nicht")
			return nil
		}
	}
}

func loginForm(user, pw string) url.Values {
	return url.Values{"action": {"login"}, "user": {user}, "password": {pw}}
}

func TestLoginPageStatuses(t *testing.T) {
	const (
		failed    = "Login fehlgeschlagen."
		blocked   = "Zu viele Fehlversuche von dieser Adresse – bitte später erneut versuchen."
		forbidden = "Anmeldung nur direkt über diese Seite möglich."
		busy      = "Gerade zu viele Anmeldungen gleichzeitig – bitte in ein paar Sekunden noch einmal versuchen."
	)
	loginPage := func(t *testing.T, rec *httptest.ResponseRecorder, status int, msg, user string) {
		t.Helper()
		body := rec.Body.String()
		if rec.Code != status {
			t.Errorf("Status %d, erwartet %d", rec.Code, status)
		}
		if !strings.Contains(body, `<span class="msg-text">`+msg+`</span>`) {
			t.Errorf("Meldung %q fehlt:\n%s", msg, body)
		}
		if !strings.Contains(body, `value="`+user+`">`) || !strings.Contains(body, `<body class="login-page">`) {
			t.Errorf("keine Anmeldeseite mit Benutzer %q:\n%s", user, body)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=UTF-8" {
			t.Errorf("Content-Type %q", ct)
		}
		if sc := rec.Header()["Set-Cookie"]; len(sc) != 0 {
			t.Errorf("Cookie gesetzt: %q", sc)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Error("ohne Sicherheits-Kopfzeilen")
		}
	}

	t.Run("Fehlversuch 200, unbekannter Name gleich", func(t *testing.T) {
		b := pageBench(t)
		wrong := b.serveIndex(b.request(http.MethodPost, "192.0.2.1", "", loginForm(adminUser, "falsch")))
		loginPage(t, wrong, 200, failed, adminUser)
		unknown := b.serveIndex(b.request(http.MethodPost, "192.0.2.2", "", loginForm("niemand", "falsch")))
		loginPage(t, unknown, 200, failed, "niemand")
		if a, u := strings.Replace(wrong.Body.String(), `value="admin"`, `value="X"`, 1),
			strings.Replace(unknown.Body.String(), `value="niemand"`, `value="X"`, 1); a != u {
			t.Errorf("unbekannter Name und falsches Passwort unterscheiden sich:\n%s\n---\n%s", a, u)
		}
		if got := normalizeVersions(wrong.Body.String()); got != normalizeVersions(golden(t, "login-error.html")) {
			t.Errorf("Fehlerseite weicht von PHP ab:\n%s", got)
		}
	})

	t.Run("gesperrt 429", func(t *testing.T) {
		b := pageBench(t)
		for range 8 {
			ThrottleHit(b.c, "login", "192.0.2.1", 8, 900*time.Second)
		}
		loginPage(t, b.serveIndex(b.request(http.MethodPost, "192.0.2.1", "", loginForm(adminUser, adminPass))), 429, blocked, adminUser)
	})

	t.Run("fremde Seite 403", func(t *testing.T) {
		b := pageBench(t)
		r := b.request(http.MethodPost, "192.0.2.1", "", loginForm(adminUser, adminPass))
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		loginPage(t, b.serveIndex(r), 403, forbidden, adminUser)
	})

	t.Run("kein Platz 503 mit dem Satz", func(t *testing.T) {
		b := pageBench(t)
		entered := make(chan struct{}, 2)
		release := make(chan struct{})
		useVerifier(t, func(hash, pw string) bool {
			entered <- struct{}{}
			<-release
			return false
		})
		first := b.startLogin(adminUser, "a", "192.0.2.10")
		second := b.startLogin(adminUser, "b", "192.0.2.11")
		<-entered
		<-entered
		// Beide Plätze belegt; der Aufrufer ist schon fort – sofort 503.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		r := b.request(http.MethodPost, "192.0.2.12", "", loginForm(adminUser, adminPass)).WithContext(ctx)
		loginPage(t, b.serveIndex(r), 503, busy, adminUser)
		close(release)
		b.finish(first)
		b.finish(second)
	})

	t.Run("Fehlversuch mit gültiger Sitzung zeigt die App (index.php:148)", func(t *testing.T) {
		b := pageBench(t)
		value, _ := b.begin(adminUser, "admin")
		rec := b.serveIndex(b.request(http.MethodPost, "192.0.2.1", value, loginForm(adminUser, "falsch")))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `<body class="app">`) {
			t.Errorf("%d:\n%s", rec.Code, rec.Body.String())
		}
		if ck := sessionCookieOf(t, rec); ck == nil || ck.Value != value || ck.MaxAge != sessionMaxIdle {
			t.Errorf("Sitzung nicht erneuert: %v", ck)
		}
		// Gesperrt und angemeldet: die App mit Status 429.
		for range 8 {
			ThrottleHit(b.c, "login", "192.0.2.1", 8, 900*time.Second)
		}
		rec = b.serveIndex(b.request(http.MethodPost, "192.0.2.1", value, loginForm(adminUser, adminPass)))
		if rec.Code != 429 || !strings.Contains(rec.Body.String(), `<body class="app">`) {
			t.Errorf("gesperrt: %d", rec.Code)
		}
	})

	t.Run("Erfolg 302 auf index.php mit beiden Cookies, Sitzung zuerst", func(t *testing.T) {
		b := pageBench(t)
		rec := b.serveIndex(b.request(http.MethodPost, "192.0.2.1", "", loginForm(adminUser, adminPass)))
		if rec.Code != 302 || rec.Header().Get("Location") != "index.php" || rec.Body.Len() != 0 {
			t.Errorf("%d %q %q", rec.Code, rec.Header().Get("Location"), rec.Body.String())
		}
		sc := rec.Header()["Set-Cookie"]
		if len(sc) != 2 || !strings.HasPrefix(sc[0], sessionCookie+"=") || !strings.HasPrefix(sc[1], deviceCookie+"=") {
			t.Errorf("Cookies: %q", sc)
		}
	})

	t.Run("ohne Anmeldung: Anmeldeseite ohne Cookie, mit erfundenem Cookie ebenso", func(t *testing.T) {
		b := pageBench(t)
		for _, ck := range []string{"", strings.Repeat("A", 43), "kaputt"} {
			rec := b.serveIndex(b.request(http.MethodGet, "192.0.2.1", ck, nil))
			if rec.Code != 200 || normalizeVersions(rec.Body.String()) != normalizeVersions(golden(t, "login.html")) {
				t.Errorf("Cookie %q: %d", ck, rec.Code)
			}
			if sc := rec.Header()["Set-Cookie"]; len(sc) != 0 {
				t.Errorf("Cookie %q: %q", ck, sc)
			}
		}
	})
}

func TestIndexLogout(t *testing.T) {
	logout := func(csrf string) url.Values { return url.Values{"action": {"logout"}, "csrf": {csrf}} }
	check := func(t *testing.T, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != 302 || rec.Header().Get("Location") != "index.php" || rec.Body.Len() != 0 {
			t.Errorf("%d %q %q", rec.Code, rec.Header().Get("Location"), rec.Body.String())
		}
		if deviceCookieOf(t, rec) != nil {
			t.Error("Abmelden fasst den Geräte-Ausweis an")
		}
	}
	b := pageBench(t)
	value, s := b.begin(adminUser, "admin")

	for _, wrong := range []string{"", "falsch", s.CSRF + "x"} {
		rec := b.serveIndex(b.request(http.MethodPost, "192.0.2.1", value, logout(wrong)))
		check(t, rec)
		if ck := sessionCookieOf(t, rec); ck == nil || ck.MaxAge != sessionMaxIdle {
			t.Errorf("Token %q: nur die Erneuerung erwartet, war %v", wrong, ck)
		}
		if _, ok, _ := b.resume(value); !ok {
			t.Fatalf("Token %q hat abgemeldet", wrong)
		}
	}
	// Das Token als Kopfzeile zählt beim Abmelden nicht (PHP: nur das Feld).
	r := b.request(http.MethodPost, "192.0.2.1", value, logout(""))
	r.Header.Set("X-CSRF-Token", s.CSRF)
	check(t, b.serveIndex(r))
	if _, ok, _ := b.resume(value); !ok {
		t.Fatal("Kopfzeile hat abgemeldet")
	}

	rec := b.serveIndex(b.request(http.MethodPost, "192.0.2.1", value, logout(s.CSRF)))
	check(t, rec)
	if ck := sessionCookieOf(t, rec); ck == nil || ck.MaxAge >= 0 {
		t.Errorf("Lösch-Cookie erwartet, war %v", ck)
	}
	if _, ok, _ := b.resume(value); ok {
		t.Fatal("nach dem Abmelden noch angemeldet")
	}
	// Ohne Sitzung: Weiterleitung ohne Cookie.
	rec = b.serveIndex(b.request(http.MethodPost, "192.0.2.1", value, logout(s.CSRF)))
	check(t, rec)
	if sc := rec.Header()["Set-Cookie"]; len(sc) != 0 {
		t.Errorf("ohne Sitzung: %q", sc)
	}
}

func TestClassicRedirect(t *testing.T) {
	b := pageBench(t)
	value, _ := b.begin(adminUser, "admin")
	h := SecurityHeaders(b.c, ClassicHandler())
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodHead, http.MethodPut} {
		for _, ck := range []string{"", value} {
			rec := httptest.NewRecorder()
			h(rec, b.request(method, "192.0.2.1", ck, nil))
			if rec.Code != 302 || rec.Header().Get("Location") != "index.php" || rec.Body.Len() != 0 {
				t.Errorf("%s: %d %q %q", method, rec.Code, rec.Header().Get("Location"), rec.Body.String())
			}
			if sc := rec.Header()["Set-Cookie"]; len(sc) != 0 {
				t.Errorf("%s mit Cookie %v: %q", method, ck != "", sc)
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("%s: ohne Sicherheits-Kopfzeilen", method)
			}
		}
	}
}
