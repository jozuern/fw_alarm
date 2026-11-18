package dashapi

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// Die Tabelle aller Aktionen: Namen, Reihenfolge und die drei Regeln wie
// DASHBOARD_ACTIONS in nas_dashboard/dashboard_api.php – einmal als Abschrift
// hier, einmal aus dem PHP-Code selbst gelesen (der zweite Teil geht mit der
// PHP-Fassung, Aufgabe 37).
func TestActionTable(t *testing.T) {
	want := []rule{
		{"status", false, false, false},
		{"keys_list", false, false, false},
		{"log_view", false, false, false},
		{"notify_list", false, false, true},
		{"settings_list", false, false, true},
		{"enqueue", true, true, true},
		{"cancel", true, true, true},
		{"demo_set", true, true, true},
		{"keys_add", true, true, true},
		{"keys_delete", true, true, true},
		{"keys_rename", true, true, true},
		{"keys_replace_key", true, true, true},
		{"keys_set_phone", true, true, true},
		{"keys_new_mute_token", true, true, true},
		{"keys_mute", true, true, true},
		{"keys_set_mute_permanent", true, true, true},
		{"keys_set_mute_volume", true, true, true},
		{"keys_test", true, true, true},
		{"notify_set", true, true, true},
		{"settings_set", true, true, true},
		{"notify_test", true, true, true},
		{"alarm", true, true, true},
		{"false_alarm", true, true, true},
	}
	if len(table) != 23 || len(want) != 23 {
		t.Fatalf("%d Aktionen in der Tabelle, erwartet 23", len(table))
	}
	for i, w := range want {
		if table[i] != w {
			t.Errorf("Zeile %d: %+v, erwartet %+v", i, table[i], w)
		}
		got, ok := actions.lookup(w.name)
		if !ok || got.Post != w.post || got.CSRF != w.csrf || got.Admin != w.admin {
			t.Errorf("%s: im Register %+v %v", w.name, got, ok)
		}
	}

	src := phpSource(t, "dashboard_api.php")
	block, ok := phpBlock(src, "const DASHBOARD_ACTIONS = [\n", "\n];\n")
	if !ok {
		t.Fatal("DASHBOARD_ACTIONS nicht gefunden")
	}
	row := regexp.MustCompile(`^\s*'([a-z_]+)'\s*=> \['post' => (true|false), 'csrf' => (true|false), 'admin' => (true|false)\],$`)
	var php []rule
	for _, line := range strings.Split(block, "\n")[1:] {
		if trimmed := strings.TrimSpace(line); trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		m := row.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("Zeile in DASHBOARD_ACTIONS nicht lesbar: %q", line)
		}
		php = append(php, rule{m[1], m[2] == "true", m[3] == "true", m[4] == "true"})
	}
	if len(php) != len(table) {
		t.Fatalf("PHP: %d Aktionen, Go: %d", len(php), len(table))
	}
	for i := range php {
		if php[i] != table[i] {
			t.Errorf("Zeile %d: PHP %+v, Go %+v", i, php[i], table[i])
		}
	}
}

// phpBlock: der Text ab open bis vor das erste close danach; false, wenn
// eines fehlt – dann scheitert der Test mit seiner Meldung, statt beim
// Zerschneiden abzustürzen (etwa nach einer Umbenennung im PHP-Code).
func phpBlock(src, open, close string) (string, bool) {
	start := strings.Index(src, open)
	if start < 0 {
		return "", false
	}
	end := strings.Index(src[start:], close)
	if end < 0 {
		return "", false
	}
	return src[start : start+end], true
}

func TestPHPBlock(t *testing.T) {
	src := "a\nconst X = [\n  1,\n];\nb"
	if got, ok := phpBlock(src, "const X = [\n", "\n];\n"); !ok || got != "const X = [\n  1," {
		t.Errorf("gefunden: %q %v", got, ok)
	}
	for _, m := range [][2]string{{"const Y = [\n", "\n];\n"}, {"const X = [\n", "\n);\n"}, {"b", "a"}} {
		if got, ok := phpBlock(src, m[0], m[1]); ok || got != "" {
			t.Errorf("%q … %q: %q %v", m[0], m[1], got, ok)
		}
	}
}

// Register nimmt nur Aktionen aus der Tabelle, jede einmal, mit Handler; die
// Regeln stehen in der Tabelle – wer sie mitgibt, muss sie genau so nennen.
func TestRegisterRules(t *testing.T) {
	h := func(*Ctx) {}
	mustPanic := func(what string, fn func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s: kein Abbruch", what)
			}
		}()
		fn()
	}
	reg := newRegistry()
	mustPanic("unbekannte Aktion", func() { reg.register("gibtesnicht", Action{Handle: h}) })
	mustPanic("ohne Handler", func() { reg.register("keys_add", Action{}) })
	mustPanic("andere Regeln", func() { reg.register("keys_add", Action{Post: true, Handle: h}) })
	mustPanic("andere Regeln (Lesen als Admin)", func() { reg.register("keys_list", Action{Admin: true, Handle: h}) })
	reg.register("keys_add", Action{Post: true, CSRF: true, Admin: true, Handle: h}) // genau wie die Tabelle
	reg.register("keys_list", Action{Handle: h})                                     // nur der Handler
	mustPanic("zweimal", func() { reg.register("keys_add", Action{Handle: h}) })
	if a, _ := reg.lookup("keys_add"); a.Handle == nil || !a.Post || !a.CSRF || !a.Admin {
		t.Errorf("keys_add: %+v", a)
	}
	if a, _ := reg.lookup("notify_set"); a.Handle != nil {
		t.Error("notify_set hat einen Handler, ohne registriert zu sein")
	}
	// status ist mit dieser Aufgabe gebaut.
	if a, ok := actions.lookup("status"); !ok || a.Handle == nil {
		t.Error("status ohne Handler")
	}
}

// Was ein Browser je Fall zurückbekommt – die Bytes, die PHP schreibt
// (json_encode ohne JSON_UNESCAPED_UNICODE).
const (
	body401      = `{"ok":false}`
	body405      = `{"ok":false,"message":"Diese Aktion ist nur per POST erlaubt."}`
	body400      = `{"ok":false,"message":"Unbekannte Aktion."}`
	bodyCSRF     = `{"ok":false,"code":"csrf","message":"Sicherheits-Token veraltet \u2013 Seite neu laden. Aktion wurde NICHT ausgef\u00fchrt."}`
	bodyReadonly = `{"ok":false,"code":"readonly","message":"Nur Lesezugriff: Dieser Benutzer darf nichts ausl\u00f6sen oder \u00e4ndern."}`
	body501      = `{"ok":false,"message":"Noch nicht gebaut."}`
)

// Die Prüfung vor jedem Handler, Schritt für Schritt: jeder Aufruf verletzt
// alle Regeln ab einer Stelle – geantwortet wird die früheste.
func TestAccessOrder(t *testing.T) {
	b := newBench(t)
	admin, adminSess := b.login("admin", "admin")
	reader, readerSess := b.login("leser", "readonly")
	ran := 0
	withHandle(t, "keys_add", func(x *Ctx) {
		ran++
		if x.Sess == nil || x.Form == nil || x.Core != b.c || x.R == nil || x.W == nil {
			t.Error("Ctx unvollständig")
		}
		x.W.WriteHeader(http.StatusTeapot)
	})
	tok := func(s string) [2]string { return [2]string{"X-CSRF-Token", s} }
	add := url.Values{"action": {"keys_add"}}
	cases := []struct {
		name   string
		r      *http.Request
		status int
		body   string
	}{
		{"ohne Anmeldung, GET unbekannt", req("GET", "action=gibtesnicht", nil, ""), 401, body401},
		{"ohne Anmeldung, POST", req("POST", "", add, "", tok(adminSess.CSRF)), 401, body401},
		{"erfundenes Cookie", req("POST", "", add, strings.Repeat("A", 43), tok(adminSess.CSRF)), 401, body401},
		{"post-Aktion per GET (Lese-Rolle, ohne Token)", req("GET", "action=keys_add", nil, reader), 405, body405},
		{"unbekannt per GET", req("GET", "action=gibtesnicht", nil, admin), 405, body405},
		{"leere Aktion per GET", req("GET", "action=", nil, admin), 405, body405},
		{"Aktion als Liste per GET", req("GET", "action[]=status", nil, admin), 405, body405},
		{"unbekannt per POST, ohne Token", req("POST", "", url.Values{"action": {"STATUS"}}, reader), 400, body400},
		{"POST ohne Feld action (Query zählt nicht)", req("POST", "action=status", url.Values{}, admin), 400, body400},
		{"Lese-Rolle ohne Token", req("POST", "", add, reader), 403, bodyCSRF},
		{"Admin mit falschem Token", req("POST", "", add, admin, tok("falsch")), 403, bodyCSRF},
		{"Token der anderen Sitzung", req("POST", "", add, admin, tok(readerSess.CSRF)), 403, bodyCSRF},
		{"Lese-Rolle mit Token", req("POST", "", add, reader, tok(readerSess.CSRF)), 403, bodyReadonly},
		{"Lese-Rolle, admin-Aktion per GET", req("GET", "action=notify_list", nil, reader), 403, bodyReadonly},
		{"Admin, Aktion noch ohne Handler", req("POST", "", url.Values{"action": {"notify_set"}}, admin, tok(adminSess.CSRF)), 501, body501},
		{"Admin, Lese-Aktion noch ohne Handler", req("GET", "action=settings_list", nil, admin), 501, body501},
	}
	for _, tc := range cases {
		rec := b.serve(tc.r)
		if rec.Code != tc.status || rec.Body.String() != tc.body {
			t.Errorf("%s: %d %s, erwartet %d %s", tc.name, rec.Code, rec.Body.String(), tc.status, tc.body)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
			t.Errorf("%s: Content-Type %q", tc.name, ct)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: ohne Sicherheits-Kopfzeilen", tc.name)
		}
		if tc.status == 401 && len(rec.Header()["Set-Cookie"]) != 0 {
			t.Errorf("%s: Cookie gesetzt", tc.name)
		}
	}
	if ran != 0 {
		t.Fatalf("Handler lief %d-mal trotz Ablehnung", ran)
	}
	// Alles erfüllt: der Handler läuft, mit erneuertem Cookie.
	rec := b.serve(req("POST", "", add, admin, tok(adminSess.CSRF)))
	if rec.Code != http.StatusTeapot || ran != 1 {
		t.Errorf("durchgelassen: %d, Handler %d-mal", rec.Code, ran)
	}
	if len(rec.Header()["Set-Cookie"]) != 1 {
		t.Errorf("Cookie nicht erneuert: %q", rec.Header()["Set-Cookie"])
	}
	// Ohne action: status; die letzte Angabe zählt (wie $_GET).
	for _, q := range []string{"", "action=keys_list&action=status"} {
		if rec := b.serve(req("GET", q, nil, reader)); rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), `{"ok":true,"status":`) {
			t.Errorf("%q: %d %s", q, rec.Code, rec.Body.String())
		}
	}
	// HEAD zählt wie GET (PHP: alles außer POST).
	if rec := b.serve(req("HEAD", "action=keys_add", nil, admin)); rec.Code != 405 {
		t.Errorf("HEAD: %d", rec.Code)
	}
}

// Das CSRF-Token zählt aus der Kopfzeile X-CSRF-Token, sonst aus dem Feld
// csrf – ist die Kopfzeile da (auch leer), zählt nur sie (PHP: ?? ).
func TestCSRFFromHeaderAndField(t *testing.T) {
	b := newBench(t)
	reader, s := b.login("leser", "readonly")
	form := func(csrf string) url.Values {
		v := url.Values{"action": {"keys_delete"}, "id": {"1"}}
		if csrf != "" {
			v.Set("csrf", csrf)
		}
		return v
	}
	cases := []struct {
		name string
		r    *http.Request
		body string
	}{
		{"Kopfzeile", req("POST", "", form(""), reader, [2]string{"X-CSRF-Token", s.CSRF}), bodyReadonly},
		{"Kopfzeile in anderer Schreibweise", req("POST", "", form(""), reader, [2]string{"x-csrf-token", s.CSRF}), bodyReadonly},
		{"Feld", req("POST", "", form(s.CSRF), reader), bodyReadonly},
		{"Kopfzeile richtig, Feld falsch", req("POST", "", form("falsch"), reader, [2]string{"X-CSRF-Token", s.CSRF}), bodyReadonly},
		{"Kopfzeile leer, Feld richtig", req("POST", "", form(s.CSRF), reader, [2]string{"X-CSRF-Token", ""}), bodyCSRF},
		{"Kopfzeile falsch, Feld richtig", req("POST", "", form(s.CSRF), reader, [2]string{"X-CSRF-Token", "falsch"}), bodyCSRF},
		{"zwei Kopfzeilen", req("POST", "", form(""), reader, [2]string{"X-CSRF-Token", s.CSRF}, [2]string{"X-CSRF-Token", s.CSRF}), bodyCSRF},
		{"nur in der Query", req("POST", "csrf="+s.CSRF, form(""), reader), bodyCSRF},
		{"Token mit Leerzeichen", req("POST", "", form(" "+s.CSRF), reader), bodyCSRF},
		{"keins", req("POST", "", form(""), reader), bodyCSRF},
	}
	for _, tc := range cases {
		if rec := b.serve(tc.r); rec.Code != 403 || rec.Body.String() != tc.body {
			t.Errorf("%s: %d %s", tc.name, rec.Code, rec.Body.String())
		}
	}
	// Eine Kopfzeile mit Unterstrich lässt Nginx fallen – sie zählt nicht.
	r := req("POST", "", form(""), reader)
	r.Header["X_csrf_token"] = []string{s.CSRF}
	if rec := b.serve(r); rec.Body.String() != bodyCSRF {
		t.Errorf("X_CSRF_TOKEN: %s", rec.Body.String())
	}
	// Multipart wie urlencoded.
	mp := multipartRequest(t, map[string]string{"action": "keys_delete", "csrf": s.CSRF}, reader)
	if rec := b.serve(mp); rec.Body.String() != bodyReadonly {
		t.Errorf("multipart: %s", rec.Body.String())
	}
}
