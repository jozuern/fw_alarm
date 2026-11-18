package dashapi

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"fwalarm/internal/store/storetest"
)

// Ein Verlauf, wie PHP ihn schrieb (älteste Zeile zuerst): mit Feldern, die
// nie hinausgehen (phone, results, version, device_id), Zahlen als Text und
// Zeichen, die json_encode maskiert.
const logLines = `{"event":"key_added","by":"admin","label":"Jürgen <B> & \"C\"","key":"jrgn...","version":2,"phone":"+49 171 5550123","log_at":1763372000}
{"event":"nas_alarm_started","by":"admin","resend":0,"demo":false,"log_at":1763372100}
{"event":"nas_alarm","by":"admin","ok":false,"message":"Alarm an 1 von 2 Empfängern zugestellt – 1 nicht erreicht.","results":[{"id":1,"label":"Anna","key":"annakey00001","ok":true,"http":200,"tries":1}],"resend":0,"demo":false,"log_at":1763372101}
{"event":"setting_changed","by":"admin","label":"Automatisch wieder laut nach","value":"4 Std","log_at":1763372200}
{"event":"cancelled","command":{"id":"21","type":"ALARM","state":"cancelled","created_by":"admin","cancelled_by":"admin","cancel_reason":"Demo-Stand geändert"},"log_at":1763372300}
{"event":"relay_alarm","info":"17.11. 10:05","device_id":"fw-box-1","log_at":"1763372400"}
{"event":"geheim_ereignis","key":"annakey00001","log_at":1763372450}
{"event":"box_offline","minutes":"20","log_at":1763372500}
{"event":"nas_web_failed","detail":"Schritt write/a","log_at":1763372600}
`

// Die Antworten, die PHP auf genau diesen Verlauf gibt (log_view mit limit=100,
// filter=all; aufgenommen mit PHP 8.3 am 18.11.2026 über log_event_view und
// json_response) – server_time ist die Uhr des Servers.
const (
	logAnswerAdmin = `{"ok":true,"entries":[` +
		`{"at":1763372600,"event":"nas_web_failed","detail":"Schritt write/a"},` +
		`{"at":1763372500,"event":"box_offline","minutes":20},` +
		`{"at":1763372400,"event":"relay_alarm","info":"17.11. 10:05","source":"relay"},` +
		`{"at":1763372300,"event":"cancelled","id":21,"type":"ALARM","by":"admin","message":"Demo-Stand ge\u00e4ndert"},` +
		`{"at":1763372200,"event":"setting_changed","by":"admin","label":"Automatisch wieder laut nach","value":"4 Std"},` +
		`{"at":1763372101,"event":"nas_alarm","by":"admin","ok":false,"message":"Alarm an 1 von 2 Empf\u00e4ngern zugestellt \u2013 1 nicht erreicht.","resend":0,"demo":false},` +
		`{"at":1763372100,"event":"nas_alarm_started","by":"admin","resend":0,"demo":false},` +
		`{"at":1763372000,"event":"key_added","by":"admin","label":"J\u00fcrgen <B> & \"C\"","key":"jrgn..."}],` +
		`"server_time":%d}`
	logAnswerReader = `{"ok":true,"entries":[` +
		`{"at":1763372600,"event":"nas_web_failed","detail":"Schritt write/a"},` +
		`{"at":1763372500,"event":"box_offline","minutes":20},` +
		`{"at":1763372400,"event":"relay_alarm","info":"17.11. 10:05","source":"relay"},` +
		`{"at":1763372300,"event":"cancelled","id":21,"type":"ALARM","by":"admin","message":"Demo-Stand ge\u00e4ndert"},` +
		`{"at":1763372101,"event":"nas_alarm","by":"admin","ok":false,"message":"Alarm an 1 von 2 Empf\u00e4ngern zugestellt \u2013 1 nicht erreicht.","resend":0,"demo":false},` +
		`{"at":1763372100,"event":"nas_alarm_started","by":"admin","resend":0,"demo":false},` +
		`{"at":1763372000,"event":"key_added","by":"admin","label":"J\u00fcrgen <B> & \"C\"","key":"jrgn..."}],` +
		`"server_time":%d}`
)

func logBench(t *testing.T, lines string) *bench {
	t.Helper()
	f := storetest.New()
	f.Put(filepath.Join(benchDir, "history.jsonl"), []byte(lines))
	return newBenchOn(t, f)
}

// Die Antwort Byte für Byte wie PHP – Schlüssel, Reihenfolge, Typen –, je
// Rolle; nie ein Feld außerhalb der Tabelle, nie das unbekannte Ereignis.
func TestLogViewAnswer(t *testing.T) {
	b := logBench(t, logLines)
	admin, _ := b.login("admin", "admin")
	reader, _ := b.login("leser", "readonly")
	for _, tc := range []struct {
		who, cookie, want string
	}{{"Admin", admin, logAnswerAdmin}, {"Lese-Rolle", reader, logAnswerReader}} {
		rec := b.serve(req("GET", "action=log_view&limit=100&filter=all", nil, tc.cookie))
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
			t.Fatalf("%s: %d %q", tc.who, rec.Code, rec.Header().Get("Content-Type"))
		}
		if want := fmt.Sprintf(tc.want, t0.Unix()); rec.Body.String() != want {
			t.Errorf("%s:\n%s\nerwartet\n%s", tc.who, rec.Body.String(), want)
		}
		for _, never := range []string{"annakey00001", "5550123", "results", "geheim", "version", "device_id", "phone"} {
			if strings.Contains(rec.Body.String(), never) {
				t.Errorf("%s: %q in der Antwort", tc.who, never)
			}
		}
	}
	// Leer: entries ist eine leere Liste, kein null.
	empty := newBench(t)
	cookie, _ := empty.login("admin", "admin")
	if got, want := empty.serve(req("GET", "action=log_view", nil, cookie)).Body.String(),
		fmt.Sprintf(`{"ok":true,"entries":[],"server_time":%d}`, t0.Unix()); got != want {
		t.Errorf("leer: %s", got)
	}
}

// limit und filter wie PHP: (int) des Parameters der Adresse, auf 1…100
// begrenzt, ohne Angabe 25; ein unbekannter Filter zählt als all. Beides nur
// aus der Adresse – auch bei einem POST (PHP: $_GET).
func TestLogViewParams(t *testing.T) {
	var lines strings.Builder
	for i := 1; i <= 120; i++ {
		fmt.Fprintf(&lines, `{"event":"key_added","by":"admin","label":"K%d","key":"kkkk...","log_at":%d}`+"\n", i, 1763370000+i)
	}
	for i := 1; i <= 3; i++ {
		fmt.Fprintf(&lines, `{"event":"nas_alarm","by":"admin","ok":true,"message":"A%d","resend":0,"log_at":%d}`+"\n", i, 1763371000+i)
	}
	b := logBench(t, lines.String())
	admin, _ := b.login("admin", "admin")
	entries := func(r string) []map[string]any {
		t.Helper()
		var v struct {
			OK      bool             `json:"ok"`
			Entries []map[string]any `json:"entries"`
		}
		if err := json.Unmarshal([]byte(r), &v); err != nil || !v.OK {
			t.Fatalf("Antwort: %s", r)
		}
		return v.Entries
	}
	get := func(query string) []map[string]any {
		t.Helper()
		return entries(b.serve(req("GET", "action=log_view&"+query, nil, admin)).Body.String())
	}
	for query, want := range map[string]int{
		"limit=7": 7, "limit=0": 1, "limit=-5": 1, "limit=1000": 100, "limit=abc": 1, "limit=": 1,
		"limit=7.9": 7, "limit=%203": 3, "limit=1e1": 10, "": 25, "limit[]=5": 1, "limit=7&limit=9": 9,
		"limit=1000&filter=keys": 100, "limit=1000&filter=alarm": 3, "limit=1000&filter=alarm_core": 3,
		"limit=1000&filter=system": 0,
	} {
		if got := len(get(query)); got != want {
			t.Errorf("%q: %d Einträge, erwartet %d", query, got, want)
		}
	}
	// Ein unbekannter Filter zählt als all.
	for _, filter := range []string{"filter=gibtesnicht", "filter=", "filter=ALARM", "filter[]=alarm", "filter=command", ""} {
		got := get("limit=5&" + filter)
		if len(got) != 5 || got[0]["message"] != "A3" || got[3]["label"] != "K120" {
			t.Errorf("%q: %v", filter, got)
		}
	}
	// Per POST (die Aktion im Rumpf, ohne Token – sie ändert nichts): limit
	// und filter weiter nur aus der Adresse.
	post := b.serve(req("POST", "limit=2&filter=keys", url.Values{"action": {"log_view"}, "limit": {"50"}, "filter": {"alarm"}}, admin))
	if got := entries(post.Body.String()); len(got) != 2 || got[0]["label"] != "K120" {
		t.Errorf("POST: %v", got)
	}
}
