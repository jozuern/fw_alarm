package dashapi

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"fwalarm/internal/config"
	"fwalarm/internal/model"
)

// statusKeys: die Felder der Antwort von status in PHPs Reihenfolge
// (dashboard_api.php, json_response im Zweig status) – nachgesehen auch an
// einer echten Antwort des PHP-Servers (Bericht der Aufgabe 13).
var statusKeys = []string{"ok", "status", "seen_age_seconds", "offline", "command", "demo", "nas_keys_version",
	"demo_sync_ok", "device_hint", "timezone", "csrf", "watchdog", "server_time"}

// topKeys liest die Schlüssel der obersten Ebene eines JSON-Objekts in ihrer
// Reihenfolge.
func topKeys(t *testing.T, body []byte) []string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(string(body)))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("kein Objekt: %s", body)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, tok.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}

func TestStatusKeySet(t *testing.T) {
	b := newBench(t)
	for _, role := range []string{"admin", "readonly"} {
		cookie, s := b.login(map[string]string{"admin": "admin", "readonly": "leser"}[role], role)
		rec := b.serve(req("GET", "action=status", nil, cookie))
		if rec.Code != 200 {
			t.Fatalf("%s: %d", role, rec.Code)
		}
		if got := topKeys(t, rec.Body.Bytes()); !slices.Equal(got, statusKeys) {
			t.Errorf("%s: %v\nerwartet %v", role, got, statusKeys)
		}
		var v map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || v["csrf"] != s.CSRF {
			t.Errorf("%s: csrf %v, erwartet das der Sitzung", role, v["csrf"])
		}
	}

	// Dieselben Schlüssel, in derselben Reihenfolge, stehen im PHP-Code.
	src := phpSource(t, "dashboard_api.php")
	start := strings.Index(src, "if ($action === 'status') {")
	if start < 0 {
		t.Fatal("Zweig status nicht gefunden")
	}
	rest := src[start:]
	open := strings.Index(rest, "    json_response([\n")
	end := strings.Index(rest, "\n    ]);\n")
	if open < 0 || end < open {
		t.Fatal("json_response im Zweig status nicht gefunden")
	}
	var php []string
	for _, m := range regexp.MustCompile(`(?m)^        '([a-z_]+)' =>`).FindAllStringSubmatch(rest[open:end], -1) {
		php = append(php, m[1])
	}
	if !slices.Equal(php, statusKeys) {
		t.Errorf("PHP: %v\nGo:  %v", php, statusKeys)
	}
}

// statusOf: die Antwort von status für die Sitzung cookie.
func (b *bench) statusOf(cookie string) string {
	b.t.Helper()
	rec := b.serve(req("GET", "action=status", nil, cookie))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		b.t.Fatalf("%d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	return rec.Body.String()
}

func (b *bench) setBox(box *model.BoxStatus) {
	b.c.Store.UpdateRuntime(func(d *model.Runtime) { d.Box = box })
}

func TestStatusAnswer(t *testing.T) {
	b := newBench(t)
	cookie, s := b.login("admin", "admin")
	now := t0.Unix()

	// Ein frischer Datenordner: die Antwort, die PHP dann gibt (nachgesehen
	// an einer echten), bis auf CSRF-Token und Uhrzeit.
	want := `{"ok":true,"status":[],"seen_age_seconds":null,"offline":true,"command":null,` +
		`"demo":{"enabled":false,"label":"","key_id":0,"key_masked":"","changed_at":0},"nas_keys_version":0,` +
		`"demo_sync_ok":false,"device_hint":"","timezone":"Europe/Berlin","csrf":"` + s.CSRF + `",` +
		`"watchdog":{"configured":true,"last_run":0,"last_send_ok_at":0,"last_send_failed_at":0},` +
		fmt.Sprintf(`"server_time":%d}`, now)
	if got := b.statusOf(cookie); got != want {
		t.Errorf("frisch:\n%s\nerwartet\n%s", got, want)
	}
	// Ohne Status-Key: configured=false; Zeitzone aus der Konfiguration.
	b.setCfg(func(c *config.Config) { c.BarkKeyStatus = ""; c.Timezone = "UTC" })
	if got := b.statusOf(cookie); !strings.Contains(got, `"configured":false`) || !strings.Contains(got, `"timezone":"UTC"`) {
		t.Errorf("ohne Status-Key: %s", got)
	}
	b.setCfg(func(c *config.Config) { c.BarkKeyStatus = "statuskey01"; c.Timezone = "Europe/Berlin" })

	// Der letzte Push: Felder in ihrer Reihenfolge, seen_at und via_bridge
	// dahinter (ein gleichnamiges Feld der Box an seiner Stelle
	// überschrieben), remote_addr nie.
	stateBefore := b.c.Store.State()
	box := &model.BoxStatus{SeenAt: now - 100, ViaBridge: "1", RemoteAddr: "203.0.113.5", Fields: model.Fields{
		{Name: "device_id", Value: "fw-box-1"}, {Name: "via_bridge", Value: "x"}, {Name: "alarm_pending", Value: "0"},
		{Name: "remote_addr", Value: "198.51.100.1"}, {Name: "keys_version", Value: "7"},
	}}
	b.setBox(box)
	runtimeBefore := b.c.Store.Runtime()
	got := b.statusOf(cookie)
	wantStatus := fmt.Sprintf(`"status":{"device_id":"fw-box-1","via_bridge":"1","alarm_pending":"0","keys_version":"7","seen_at":%d},`+
		`"seen_age_seconds":100,"offline":false,`, now-100)
	if !strings.Contains(got, wantStatus) {
		t.Errorf("mit Push:\n%s\nerwartet darin\n%s", got, wantStatus)
	}
	if strings.Contains(got, "203.0.113.5") || strings.Contains(got, "198.51.100.1") || strings.Contains(got, "remote_addr") {
		t.Errorf("Adresse in der Antwort: %s", got)
	}
	if b.c.Store.State() != stateBefore || b.c.Store.Runtime() != runtimeBefore {
		t.Error("status hat Zustand oder Laufzeitdaten geändert")
	}

	// offline: älter als offline_after_seconds; läuft laut Push eine
	// Zustellung (alarm_pending=1), mindestens 600 s.
	cases := []struct {
		age        int64
		pending    string
		after      int
		wantOff    bool
		wantAgeTxt string
	}{
		{180, "0", 180, false, "180"},
		{181, "0", 180, true, "181"},
		{181, "1", 180, false, "181"},
		{600, "1", 180, false, "600"},
		{601, "1", 180, true, "601"},
		{900, "1", 900, false, "900"},
		{901, "1", 900, true, "901"},
		{601, "true", 180, true, "601"},
		{-30, "0", 180, false, "-30"}, // Uhr zurückgestellt: wie PHP (time() - seen_at)
	}
	for _, tc := range cases {
		b.setCfg(func(c *config.Config) { c.OfflineAfterSeconds = tc.after })
		b.setBox(&model.BoxStatus{SeenAt: now - tc.age, ViaBridge: "0", Fields: model.Fields{{Name: "alarm_pending", Value: tc.pending}}})
		got := b.statusOf(cookie)
		want := fmt.Sprintf(`"seen_age_seconds":%s,"offline":%v,`, tc.wantAgeTxt, tc.wantOff)
		if !strings.Contains(got, want) {
			t.Errorf("Alter %d, alarm_pending %q, Schwelle %d: %s", tc.age, tc.pending, tc.after, got)
		}
	}
	// Ohne Ankunftszeit: nie gesehen.
	b.setBox(&model.BoxStatus{SeenAt: 0, ViaBridge: "0"})
	if got := b.statusOf(cookie); !strings.Contains(got, `"status":{"seen_at":0,"via_bridge":"0"},"seen_age_seconds":null,"offline":true,`) {
		t.Errorf("seen_at 0: %s", got)
	}
}

// Teile, die spätere Aufgaben liefern, kommen über StatusPart – an ihrer
// Stelle in der Antwort, mit der Sitzung der Anfrage.
func TestStatusParts(t *testing.T) {
	b := newBench(t)
	admin, _ := b.login("admin", "admin")
	reader, _ := b.login("leser", "readonly")
	withStatusPart(t, "device_hint", func(x *Ctx) any { return "Hinweis für " + x.Sess.Role })
	withStatusPart(t, "command", func(x *Ctx) any { return map[string]any{"id": 7} })
	if got := b.statusOf(admin); !strings.Contains(got, `"command":{"id":7},`) || !strings.Contains(got, `"device_hint":"Hinweis f\u00fcr admin",`) {
		t.Errorf("Admin: %s", got)
	}
	if got := b.statusOf(reader); !strings.Contains(got, `"device_hint":"Hinweis f\u00fcr readonly",`) {
		t.Errorf("Lese-Rolle: %s", got)
	}

	mustPanic := func(what string, fn func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s: kein Abbruch", what)
			}
		}()
		fn()
	}
	p := newPartSet()
	f := func(*Ctx) any { return 1 }
	for _, name := range []string{"ok", "status", "csrf", "server_time", "timezone", "gibtesnicht"} {
		mustPanic(name, func() { p.set(name, f) })
	}
	mustPanic("ohne Funktion", func() { p.set("demo", nil) })
	for _, name := range []string{"command", "demo", "nas_keys_version", "demo_sync_ok", "device_hint", "watchdog"} {
		p.set(name, f)
		mustPanic(name+" zweimal", func() { p.set(name, f) })
	}
}

// Ein Teil ohne Wert für den frischen Datenordner (oder ein Wert ohne Teil)
// ist ein Fehler im Aufbau des Programms: Abbruch beim Start, nicht eine
// Panik bei jedem Abruf von status.
func TestPartSetNeedsEmptyValue(t *testing.T) {
	mustPanic := func(what string, fn func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s: kein Abbruch", what)
			}
		}()
		fn()
	}
	mustPanic("Teil ohne Wert", func() { buildPartSet(append(slices.Clone(statusPartNames), "neu"), emptyParts) })
	mustPanic("Wert ohne Teil", func() { buildPartSet(statusPartNames[1:], emptyParts) })
	mustPanic("Wert nil", func() {
		empty := maps.Clone(emptyParts)
		empty["command"] = nil
		buildPartSet(statusPartNames, empty)
	})
	p := buildPartSet(statusPartNames, emptyParts) // so baut ihn das Programm
	for _, name := range statusPartNames {
		if p.fns[name] == nil {
			t.Errorf("%s ohne Funktion", name)
		}
	}
}

// Für jeden Teil, den noch keine Aufgabe liefert: der Wert, den PHP auf
// einem frischen Datenordner gibt.
func TestStatusEmptyParts(t *testing.T) {
	b := newBench(t)
	_, s := b.login("leser", "readonly")
	x := &Ctx{Core: b.c, Sess: s, R: req(http.MethodGet, "action=status", nil, "")}
	p := newPartSet()
	want := map[string]string{
		"command":          `null`,
		"demo":             `{"enabled":false,"label":"","key_id":0,"key_masked":"","changed_at":0}`,
		"nas_keys_version": `0`,
		"demo_sync_ok":     `false`,
		"device_hint":      `""`,
		"watchdog":         `{"configured":true,"last_run":0,"last_send_ok_at":0,"last_send_failed_at":0}`,
	}
	for name, w := range want {
		raw, err := json.Marshal(p.value(name, x))
		if err != nil || string(raw) != w {
			t.Errorf("%s: %s %v, erwartet %s", name, raw, err, w)
		}
	}
}
