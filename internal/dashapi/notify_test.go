package dashapi

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"

	"fwalarm/internal/config"
	"fwalarm/internal/model"
)

// notifyCall: ein POST an dashboard_api.php mit Token; Status, Rumpf als
// Tabelle und als Text.
func (b *bench) notifyCall(cookie, csrf string, form url.Values) (int, map[string]any, string) {
	b.t.Helper()
	rec := b.serve(req("POST", "", form, cookie, [2]string{"X-CSRF-Token", csrf}))
	var v map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		b.t.Fatalf("keine JSON-Antwort: %d %s", rec.Code, rec.Body.String())
	}
	return rec.Code, v, rec.Body.String()
}

// notify_list: alles, was die Oberfläche zum Einstellen braucht – in PHPs
// Reihenfolge; nur für den Admin.
func TestNotifyList(t *testing.T) {
	b := newBench(t)
	admin, _ := b.login("admin", "admin")
	reader, _ := b.login("leser", "readonly")

	if rec := b.serve(req("GET", "action=notify_list", nil, reader)); rec.Code != 403 || rec.Body.String() != bodyReadonly {
		t.Errorf("Lese-Rolle: %d %s", rec.Code, rec.Body.String())
	}
	rec := b.serve(req("GET", "action=notify_list", nil, admin))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if got := topKeys(t, rec.Body.Bytes()); !slices.Equal(got, []string{"ok", "configured", "quiet_now", "global", "types"}) {
		t.Errorf("Felder: %v", got)
	}
	// Kein Bark-Key, kein Token, keine Telefonnummer in der Antwort – nur
	// Namen der Empfänger als Stellvertreter-Wahl.
	type choice struct{ Value, Text string }
	type option struct {
		Key, Label, Value string
		Choices           []choice
	}
	type kind struct {
		Key, Label, Hint   string
		Important, Enabled bool
		Options            []option
	}
	var ans struct {
		OK, Configured bool
		QuietNow       bool `json:"quiet_now"`
		Global         []option
		Types          []kind
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ans); err != nil {
		t.Fatal(err)
	}
	if !ans.OK || !ans.Configured || ans.QuietNow {
		t.Errorf("ok/configured/quiet_now: %+v", ans)
	}
	var keys []string
	for _, k := range ans.Types {
		keys = append(keys, k.Key)
		if len(k.Options) < 2 || k.Options[0].Key != "level" || k.Options[1].Key != "ignore_quiet" {
			t.Errorf("%s: Optionen %v", k.Key, k.Options)
		}
	}
	want := []string{"box_offline", "nas_web", "demo_reminder", "mute_reminder", "permanent_mute_reminder",
		"login_lockout", "login_success", "mute_changed", "mute_expired", "box_restart", "box_health", "weekly_report"}
	if !slices.Equal(keys, want) {
		t.Errorf("Arten: %v", keys)
	}
	// Reihenfolge der Felder je Art und je Option (wie dashboard_api.php).
	raw := rec.Body.String()
	for _, frag := range []string{
		phpText(`{"key":"box_offline","label":"Box offline / wieder online","hint":"Wenn die Box zu lange keinen Status mehr schickt – dann würde ein Relaisalarm nicht ausgelöst.","important":true,"enabled":true,"options":[{"key":"level","label":"Dringlichkeit","value":"passive","choices":[{"value":"passive","text":"leise (nur in der Mitteilungszentrale)"},`),
		`{"key":"offline_after_s","label":"Als offline melden nach","value":"600","choices":[{"value":"300","text":"5 Min"},`,
		`"global":[{"key":"quiet_enabled","label":"Ruhezeit","value":"0","choices":[{"value":"0","text":"aus"},{"value":"1","text":"ein"}]},`,
		phpText(`{"key":"extra_id","label":"Zusätzlich senden an","value":"0","choices":[{"value":"0","text":"niemand"}]}]`),
	} {
		if !strings.Contains(raw, frag) {
			t.Errorf("Antwort ohne\n%s\nin\n%s", frag, raw)
		}
	}

	// Gespeicherte Werte, Stellvertreter aus der Liste, Ruhezeit jetzt,
	// ohne Status-Key nicht eingerichtet.
	b.update(func(d *model.State) {
		d.NextID = 4
		d.Recipients = []model.Recipient{{ID: 3, Key: "annakey0001", Label: "Anna", Phone: "0170 1234567", MuteToken: "annatoken03"}}
		d.Notify.Global = map[string]string{"quiet_enabled": "1", "quiet_from": "11", "quiet_to": "12", "extra_id": "3"}
		d.Notify.Types = map[string]map[string]string{"login_success": {"enabled": "1", "who": "admin"}}
	})
	b.setCfg(func(c *config.Config) { c.BarkKeyStatus = "" })
	rec = b.serve(req("GET", "action=notify_list", nil, admin))
	if err := json.Unmarshal(rec.Body.Bytes(), &ans); err != nil {
		t.Fatal(err)
	}
	if ans.Configured || !ans.QuietNow { // t0 = 11:00 in Berlin
		t.Errorf("configured/quiet_now: %v %v", ans.Configured, ans.QuietNow)
	}
	for _, frag := range []string{
		phpText(`{"key":"extra_id","label":"Zusätzlich senden an","value":"3","choices":[{"value":"0","text":"niemand"},{"value":"3","text":"Anna"}]}`),
		`"key":"login_success","label":"Anmeldung am Dashboard"`,
		`{"key":"who","label":"Melden bei","value":"admin","choices":`,
	} {
		if !strings.Contains(rec.Body.String(), frag) {
			t.Errorf("Antwort ohne %s", frag)
		}
	}
	for _, secret := range []string{"annakey0001", "annatoken03", "1234567"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("%q in der Antwort", secret)
		}
	}
	for _, k := range ans.Types {
		if k.Key == "login_success" && !k.Enabled {
			t.Error("login_success nicht eingeschaltet")
		}
	}
}

// phpText schreibt s, wie PHPs json_encode (und web.JSON) Nicht-ASCII
// schreibt: \uXXXX, jenseits der BMP als UTF-16-Paar. So stehen die
// erwarteten Antworten hier lesbar.
func phpText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x80 {
			b.WriteRune(r)
			continue
		}
		for _, u := range utf16.Encode([]rune{r}) {
			fmt.Fprintf(&b, "%cu%04x", '\\', u)
		}
	}
	return b.String()
}

func (b *bench) update(fn func(d *model.State)) {
	b.t.Helper()
	if err := b.c.Store.Update(b.t.Context(), func(d *model.State) error { fn(d); return nil }); err != nil {
		b.t.Fatal(err)
	}
}

// notify_set: nur angebotene Werte; gespeichert wird nur ausdrücklich
// Gesetztes; jede Änderung als notify_changed im Verlauf.
func TestNotifySet(t *testing.T) {
	b := newBench(t)
	admin, s := b.login("admin", "admin")
	set := func(kind, key, value string) (int, map[string]any, string) {
		t.Helper()
		return b.notifyCall(admin, s.CSRF, url.Values{"action": {"notify_set"}, "type": {kind}, "key": {key}, "value": {value}})
	}
	changes := func() []model.Entry {
		return b.c.Store.History(100, func(e model.Entry) bool { return e.Str("event") == model.EvNotifyChanged })
	}

	for _, tc := range []struct{ kind, key, value, body string }{
		{"gibtesnicht", "enabled", "1", `{"ok":false,"message":"Unbekannte Einstellung."}`},
		{"global", "enabled", "1", `{"ok":false,"message":"Unbekannte Einstellung."}`},
		{"", "", "", `{"ok":false,"message":"Unbekannte Einstellung."}`},
		{"outside_watcher", "enabled", "0", `{"ok":false,"message":"Unbekannte Einstellung."}`},
		{"box_offline", "level", "laut", phpText(`{"ok":false,"message":"Ungültiger Wert für „Dringlichkeit“ – nichts geändert."}`)},
		{"global", "extra_id", "3", phpText(`{"ok":false,"message":"Ungültiger Wert für „Zusätzlich senden an“ – nichts geändert."}`)},
	} {
		code, _, body := set(tc.kind, tc.key, tc.value)
		if code != 200 || body != tc.body {
			t.Errorf("%s/%s=%q: %d %s", tc.kind, tc.key, tc.value, code, body)
		}
	}
	if st := b.c.Store.State().Notify; len(st.Global) != 0 || len(st.Types) != 0 || len(changes()) != 0 {
		t.Fatalf("abgelehnt, aber gespeichert: %v %v", st, changes())
	}

	for _, tc := range []struct{ kind, key, value, body string }{
		{"login_success", "enabled", "1", phpText(`{"ok":true,"changed":true,"message":"Eingeschaltet: „Anmeldung am Dashboard“ wird dir gemeldet."}`)},
		{"box_offline", "enabled", "0", phpText(`{"ok":true,"changed":true,"message":"Abgeschaltet: „Box offline / wieder online“ wird dir nicht mehr gemeldet."}`)},
		{"box_offline", "level", "timeSensitive", phpText(`{"ok":true,"changed":true,"message":"Gespeichert – Box offline / wieder online, Dringlichkeit: dringend (durchbricht Fokus)."}`)},
		{"global", "critical_volume", "0", phpText(`{"ok":true,"changed":true,"message":"Gespeichert – Alle Meldungen, Lautstärke kritischer Meldungen: 0 (lautlos)."}`)},
		{"box_offline", "level", "timeSensitive", `{"ok":true,"changed":false,"message":"Ist bereits so eingestellt."}`},
		{"box_offline", "offline_after_s", "600", `{"ok":true,"changed":false,"message":"Ist bereits so eingestellt."}`},
	} {
		code, _, body := set(tc.kind, tc.key, tc.value)
		if code != 200 || body != tc.body {
			t.Errorf("%s/%s=%q: %d %s", tc.kind, tc.key, tc.value, code, body)
		}
	}
	want := model.NotifyChoices{
		Global: map[string]string{"critical_volume": "0"},
		Types: map[string]map[string]string{
			"login_success": {"enabled": "1"},
			"box_offline":   {"enabled": "0", "level": "timeSensitive"},
		},
	}
	if got := b.c.Store.State().Notify; !notifyEqual(got, want) {
		t.Errorf("gespeichert: %v, erwartet %v", got, want)
	}

	// Verlauf: genau die Änderungen, neueste zuerst, mit PHPs Feldern.
	got := changes()
	if len(got) != 4 {
		t.Fatalf("%d Einträge notify_changed: %v", len(got), got)
	}
	first := got[3]
	for k, v := range map[string]string{"by": "admin", "type": "login_success", "key": "enabled",
		"label": "Anmeldung am Dashboard", "option": "Meldung", "value": "eingeschaltet"} {
		if first.Str(k) != v {
			t.Errorf("Eintrag %s=%q, erwartet %q (%v)", k, first.Str(k), v, first)
		}
	}
	if first.Int("log_at") != t0.Unix() || first.Str("uid") == "" {
		t.Errorf("log_at/uid: %v", first)
	}
	if got[0].Str("type") != "global" || got[0].Str("label") != "Alle Meldungen" || got[0].Str("value") != "0 (lautlos)" {
		t.Errorf("global im Verlauf: %v", got[0])
	}

	// Stellvertreter: nur jemand aus der Liste; im Verlauf sein Name.
	b.update(func(d *model.State) {
		d.NextID = 4
		d.Recipients = []model.Recipient{{ID: 3, Key: "annakey0001", Label: "Anna", MuteToken: "annatoken03"}}
	})
	if code, _, body := set("global", "extra_id", "3"); code != 200 ||
		body != phpText(`{"ok":true,"changed":true,"message":"Gespeichert – Alle Meldungen, Zusätzlich senden an: Anna."}`) {
		t.Errorf("Stellvertreter: %d %s", code, body)
	}

	// Speichern scheitert: die Antwort zum Datenordner, nichts geändert.
	before := b.c.Store.State().Notify
	b.fs.ReadOnly(&fs.PathError{Op: "open", Path: "/daten", Err: fs.ErrPermission})
	code, v, body := set("weekly_report", "enabled", "1")
	b.fs.ReadOnly(nil)
	if code != 500 || v["code"] != "storage" || strings.Contains(body, "daten") {
		t.Errorf("Speichern scheitert: %d %s", code, body)
	}
	if !notifyEqual(b.c.Store.State().Notify, before) {
		t.Error("trotz Fehler geändert")
	}
	// Die Lese-Rolle darf nichts einstellen.
	reader, rs := b.login("leser", "readonly")
	if code, _, body := b.notifyCall(reader, rs.CSRF, url.Values{"action": {"notify_set"}, "type": {"box_offline"},
		"key": {"enabled"}, "value": {"1"}}); code != 403 || body != bodyReadonly {
		t.Errorf("Lese-Rolle: %d %s", code, body)
	}
}

func notifyEqual(a, b model.NotifyChoices) bool {
	if len(a.Global) != len(b.Global) || len(a.Types) != len(b.Types) {
		return false
	}
	for k, v := range a.Global {
		if b.Global[k] != v {
			return false
		}
	}
	for kind, row := range a.Types {
		other, ok := b.Types[kind]
		if !ok || len(other) != len(row) {
			return false
		}
		for k, v := range row {
			if other[k] != v {
				return false
			}
		}
	}
	return true
}

// notify_test: eine Probe-Meldung über denselben Weg wie jede Meldung – auch
// bei abgeschalteter Art; die Antwort nennt die Dringlichkeit, mit der
// wirklich gesendet wurde.
func TestNotifyTest(t *testing.T) {
	b := newBench(t)
	admin, s := b.login("admin", "admin")
	probe := func(kind string) string {
		t.Helper()
		code, _, body := b.notifyCall(admin, s.CSRF, url.Values{"action": {"notify_test"}, "type": {kind}})
		if code != 200 {
			t.Errorf("%s: Status %d", kind, code)
		}
		return body
	}

	if body := probe("login_success"); body != phpText(`{"ok":true,"message":"Probe gesendet – Dringlichkeit: leise (nur in der Mitteilungszentrale)."}`) {
		t.Errorf("Probe: %s", body)
	}
	reqs := b.barkTake()
	if len(reqs) != 1 || reqs[0].Key != "statuskey01" || reqs[0].Fields.Get("title") != "PROBE: Anmeldung am Dashboard" ||
		reqs[0].Fields.Get("body") != "[17.11.2026 11:00] Probe-Meldung aus dem Dashboard. So kommt diese Art von Meldung bei dir an." ||
		reqs[0].Fields.Get("level") != "passive" {
		t.Errorf("gesendet: %+v", reqs)
	}
	// Umlaute im Namen der Art gehen als ASCII hinaus.
	probe("box_health")
	if reqs := b.barkTake(); len(reqs) != 1 || reqs[0].Fields.Get("title") != "PROBE: Auffaelligkeiten an der Box" {
		t.Errorf("ASCII: %+v", reqs)
	}

	// In der Ruhezeit leise – mit Begründung; ausgenommen wie eingestellt.
	b.update(func(d *model.State) {
		d.Notify.Global = map[string]string{"quiet_enabled": "1", "quiet_from": "11", "quiet_to": "13"}
		d.Notify.Types = map[string]map[string]string{"box_offline": {"level": "critical"}}
	})
	if body := probe("box_offline"); body != phpText(`{"ok":true,"message":"Probe gesendet – Dringlichkeit: leise (nur in der Mitteilungszentrale) (gerade Ruhezeit, deshalb leise)."}`) {
		t.Errorf("Ruhezeit: %s", body)
	}
	b.update(func(d *model.State) { d.Notify.Types["box_offline"]["ignore_quiet"] = "1" })
	if body := probe("box_offline"); body != phpText(`{"ok":true,"message":"Probe gesendet – Dringlichkeit: kritisch (durchbricht auch den Stummschalter)."}`) {
		t.Errorf("ausgenommen: %s", body)
	}
	if reqs := b.barkTake(); len(reqs) != 2 || reqs[1].Fields.Get("level") != "critical" || reqs[1].Fields.Get("volume") != "5" {
		t.Errorf("kritisch: %+v", reqs)
	}

	// Bark nimmt nicht an.
	b.barkAnswers(500)
	if body := probe("box_offline"); body != phpText(`{"ok":false,"message":"Probe FEHLGESCHLAGEN – Bark hat sie nicht angenommen (Status-Key und Verbindung prüfen)."}`) {
		t.Errorf("Bark 500: %s", body)
	}
	b.barkAnswers(0)
	b.barkTake()
	// Unbrauchbarer Status-Key: gescheitert, nichts gesendet.
	b.setCfg(func(c *config.Config) { c.BarkKeyStatus = "x!geheim" })
	if body := probe("box_offline"); !strings.Contains(body, "Probe FEHLGESCHLAGEN") {
		t.Errorf("unbrauchbarer Key: %s", body)
	}
	// Ohne Status-Key: der Hinweis nennt bark_key_status und config.toml.
	b.setCfg(func(c *config.Config) { c.BarkKeyStatus = "" })
	if body := probe("box_offline"); body != phpText(`{"ok":false,"message":"Kein Status-Key (bark_key_status) in config.toml eingetragen – es kann nichts gesendet werden."}`) {
		t.Errorf("ohne Status-Key: %s", body)
	}
	b.setCfg(func(c *config.Config) { c.BarkKeyStatus = "statuskey01" })
	// Unbekannte Art.
	for _, kind := range []string{"gibtesnicht", "", "global", "outside_watcher"} {
		if body := probe(kind); body != `{"ok":false,"message":"Unbekannte Art von Meldung."}` {
			t.Errorf("%q: %s", kind, body)
		}
	}
	if reqs := b.barkTake(); len(reqs) != 0 {
		t.Errorf("trotzdem gesendet: %+v", reqs)
	}
	// Mit Außen-Wächter gibt es outside_watcher.
	b.setCfg(func(c *config.Config) { c.WatcherTokenSHA256 = strings.Repeat("d", 64) })
	if body := probe("outside_watcher"); !strings.Contains(body, `"ok":true`) {
		t.Errorf("outside_watcher: %s", body)
	}
	b.barkTake()
}
