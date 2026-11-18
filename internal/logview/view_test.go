package logview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"fwalarm/internal/clock"
	"fwalarm/internal/core"
	"fwalarm/internal/model"
	"fwalarm/internal/store"
	"fwalarm/internal/store/storetest"
)

var t0 = time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

// entryOf: eine Verlaufszeile, wie die Ablage sie liest (Zahlen als
// json.Number).
func entryOf(t testing.TB, line string) model.Entry {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(line))
	dec.UseNumber()
	var e model.Entry
	if err := dec.Decode(&e); err != nil {
		t.Fatalf("%s: %v", line, err)
	}
	return e
}

// viewJSON: die Ansicht in der Reihenfolge von Keys, als JSON.
func viewJSON(t testing.TB, v map[string]any) string {
	t.Helper()
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range Keys(v) {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		vb, err := json.Marshal(v[k])
		if err != nil {
			t.Fatal(err)
		}
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
	}
	if len(Keys(v)) != len(v) {
		t.Errorf("Keys %v nennt nicht alle Schlüssel von %v", Keys(v), v)
	}
	b.WriteByte('}')
	return b.String()
}

// Jeder Wert kommt in seinem festen Typ hinaus – genau wie PHP ihn umwandelt.
// Die Erwartungen sind die Ausgabe von PHP selbst: log_event_view() und
// json_encode auf genau diese Zeilen (aufgenommen mit PHP 8.3 am
// 18.11.2026; Nicht-ASCII hier roh, PHP schreibt \u-Folgen – das macht erst
// die Antwort, web.JSON).
func TestViewTypes(t *testing.T) {
	cases := []struct{ line, want string }{
		// Zahlen als Text (alte PHP-Zeilen) werden Zahlen.
		{`{"event":"box_offline","minutes":"7"}`, `{"at":0,"event":"box_offline","minutes":7}`},
		{`{"event":"box_offline","minutes":" 12abc"}`, `{"at":0,"event":"box_offline","minutes":12}`},
		{`{"event":"box_offline","minutes":"1e3"}`, `{"at":0,"event":"box_offline","minutes":1000}`},
		{`{"event":"box_offline","minutes":7.9}`, `{"at":0,"event":"box_offline","minutes":7}`},
		{`{"event":"box_offline","minutes":-7.9}`, `{"at":0,"event":"box_offline","minutes":-7}`},
		{`{"event":"box_offline","minutes":true}`, `{"at":0,"event":"box_offline","minutes":1}`},
		{`{"event":"box_offline","minutes":"0x1A"}`, `{"at":0,"event":"box_offline","minutes":0}`},
		{`{"event":"box_offline","minutes":[1]}`, `{"at":0,"event":"box_offline","minutes":1}`},
		{`{"event":"box_offline","minutes":[]}`, `{"at":0,"event":"box_offline","minutes":0}`},
		{`{"event":"box_offline","minutes":{"a":1}}`, `{"at":0,"event":"box_offline","minutes":1}`},
		{`{"event":"box_offline","minutes":"9223372036854775808"}`, `{"at":0,"event":"box_offline","minutes":9223372036854775807}`},
		// Fehlt ein Wert (oder ist er null): der Standard.
		{`{"event":"watcher_recovered"}`, `{"at":0,"event":"watcher_recovered","minutes":0}`},
		{`{"event":"box_offline","minutes":null,"log_at":"1763372700"}`, `{"at":1763372700,"event":"box_offline","minutes":0}`},
		{`{"event":"box_offline","log_at":[1]}`, `{"at":1,"event":"box_offline","minutes":0}`},
		// Text: Zahlen wie PHP sie nach json_decode schreibt.
		{`{"event":"nas_web_failed","detail":500}`, `{"at":0,"event":"nas_web_failed","detail":"500"}`},
		{`{"event":"nas_web_failed","detail":1.0}`, `{"at":0,"event":"nas_web_failed","detail":"1"}`},
		{`{"event":"nas_web_failed","detail":1e3}`, `{"at":0,"event":"nas_web_failed","detail":"1000"}`},
		{`{"event":"nas_web_failed","detail":1.5}`, `{"at":0,"event":"nas_web_failed","detail":"1.5"}`},
		{`{"event":"nas_web_failed","detail":0.30000000000000004}`, `{"at":0,"event":"nas_web_failed","detail":"0.3"}`},
		{`{"event":"nas_web_failed","detail":1e20}`, `{"at":0,"event":"nas_web_failed","detail":"1.0E+20"}`},
		{`{"event":"nas_web_failed","detail":1e-5}`, `{"at":0,"event":"nas_web_failed","detail":"1.0E-5"}`},
		{`{"event":"nas_web_failed","detail":-1.5e-7}`, `{"at":0,"event":"nas_web_failed","detail":"-1.5E-7"}`},
		{`{"event":"nas_web_failed","detail":0.0001}`, `{"at":0,"event":"nas_web_failed","detail":"0.0001"}`},
		{`{"event":"nas_web_failed","detail":0.001}`, `{"at":0,"event":"nas_web_failed","detail":"0.001"}`},
		{`{"event":"nas_web_failed","detail":1e14}`, `{"at":0,"event":"nas_web_failed","detail":"1.0E+14"}`},
		{`{"event":"nas_web_failed","detail":12345678901234.5}`, `{"at":0,"event":"nas_web_failed","detail":"12345678901234"}`},
		{`{"event":"nas_web_failed","detail":123456789012345.0}`, `{"at":0,"event":"nas_web_failed","detail":"1.2345678901234E+14"}`},
		{`{"event":"nas_web_failed","detail":99999999999999.99}`, `{"at":0,"event":"nas_web_failed","detail":"1.0E+14"}`},
		{`{"event":"nas_web_failed","detail":1.23456789012345678}`, `{"at":0,"event":"nas_web_failed","detail":"1.2345678901235"}`},
		{`{"event":"nas_web_failed","detail":1234567890123456789012}`, `{"at":0,"event":"nas_web_failed","detail":"1.2345678901235E+21"}`},
		{`{"event":"nas_web_failed","detail":9223372036854775807}`, `{"at":0,"event":"nas_web_failed","detail":"9223372036854775807"}`},
		{`{"event":"nas_web_failed","detail":9223372036854775808}`, `{"at":0,"event":"nas_web_failed","detail":"9.2233720368548E+18"}`},
		{`{"event":"nas_web_failed","detail":-9223372036854775808}`, `{"at":0,"event":"nas_web_failed","detail":"-9223372036854775808"}`},
		{`{"event":"nas_web_failed","detail":1.5e300}`, `{"at":0,"event":"nas_web_failed","detail":"1.5E+300"}`},
		{`{"event":"nas_web_failed","detail":2.5e-300}`, `{"at":0,"event":"nas_web_failed","detail":"2.5E-300"}`},
		{`{"event":"nas_web_failed","detail":-0.0}`, `{"at":0,"event":"nas_web_failed","detail":"-0"}`},
		{`{"event":"nas_web_failed","detail":-0}`, `{"at":0,"event":"nas_web_failed","detail":"0"}`},
		{`{"event":"nas_web_failed","detail":1e400}`, `{"at":0,"event":"nas_web_failed","detail":"INF"}`},
		{`{"event":"nas_web_failed","detail":-1e400}`, `{"at":0,"event":"nas_web_failed","detail":"-INF"}`},
		{`{"event":"nas_web_failed","detail":5e-324}`, `{"at":0,"event":"nas_web_failed","detail":"4.9406564584125E-324"}`},
		// Text: true "1", false/null "", Objekte und Listen "".
		{`{"event":"nas_web_failed","detail":true}`, `{"at":0,"event":"nas_web_failed","detail":"1"}`},
		{`{"event":"nas_web_failed","detail":false}`, `{"at":0,"event":"nas_web_failed","detail":""}`},
		{`{"event":"nas_web_failed","detail":{"a":1}}`, `{"at":0,"event":"nas_web_failed","detail":""}`},
		{`{"event":"key_renamed","by":"admin","label":"Anna B.","old_label":["Anna"],"key":"anna..."}`,
			`{"at":0,"event":"key_renamed","by":"admin","label":"Anna B.","old_label":"","key":"anna..."}`},
		// Wahrheitswerte wie !empty().
		{`{"event":"key_test","ok":"0"}`, `{"at":0,"event":"key_test","by":"","label":"","ok":false}`},
		{`{"event":"key_test","ok":"0.0"}`, `{"at":0,"event":"key_test","by":"","label":"","ok":true}`},
		{`{"event":"key_test","ok":0.0}`, `{"at":0,"event":"key_test","by":"","label":"","ok":false}`},
		{`{"event":"key_test","ok":[]}`, `{"at":0,"event":"key_test","by":"","label":"","ok":false}`},
		{`{"event":"key_test","ok":[0]}`, `{"at":0,"event":"key_test","by":"","label":"","ok":true}`},
		{`{"event":"key_test","ok":" "}`, `{"at":0,"event":"key_test","by":"","label":"","ok":true}`},
		// Pfade: Teil für Teil hinein; ein Schlüssel mit Punkt zählt nicht.
		{`{"event":"created","command":{"id":"3","type":"ALARM"}}`, `{"at":0,"event":"created","id":3,"type":"ALARM","by":""}`},
		{`{"event":"created","command.id":5,"command":{"id":3}}`, `{"at":0,"event":"created","id":3,"type":"","by":""}`},
		{`{"event":"created","command":"x","type":"ALARM"}`, `{"at":0,"event":"created","id":0,"type":"","by":""}`},
		{`{"event":"created","command":[1,2]}`, `{"at":0,"event":"created","id":0,"type":"","by":""}`},
		// Optional: nur, wenn da (null zählt als fehlt); Standard von source.
		{`{"event":"nas_alarm_started","demo":null}`, `{"at":0,"event":"nas_alarm_started","by":"","resend":0}`},
		{`{"event":"nas_alarm_started","demo":0}`, `{"at":0,"event":"nas_alarm_started","by":"","resend":0,"demo":false}`},
		{`{"event":"nas_alarm_started","demo":"x"}`, `{"at":0,"event":"nas_alarm_started","by":"","resend":0,"demo":true}`},
		{`{"event":"relay_alarm","source":null}`, `{"at":0,"event":"relay_alarm","info":"","source":"relay"}`},
		{`{"event":"relay_alarm","source":""}`, `{"at":0,"event":"relay_alarm","info":"","source":""}`},
		// Die Einzelergebnisse eines Versands bleiben im Verlauf.
		{`{"event":"nas_alarm","by":"admin","ok":true,"message":"Alarm an 1 von 1 Empfängern zugestellt.","results":[{"id":1,"label":"Anna","key":"annakey00001","ok":true,"http":200,"tries":1}],"resend":0,"demo":false,"log_at":1763372700}`,
			`{"at":1763372700,"event":"nas_alarm","by":"admin","ok":true,"message":"Alarm an 1 von 1 Empfängern zugestellt.","resend":0,"demo":false}`},
	}
	for _, tc := range cases {
		for _, admin := range []bool{true, false} {
			v, ok := View(entryOf(t, tc.line), admin)
			if !ok {
				t.Errorf("%s: nicht gezeigt", tc.line)
				continue
			}
			if got := viewJSON(t, v); got != tc.want {
				t.Errorf("%s:\n%s\nerwartet\n%s", tc.line, got, tc.want)
			}
		}
	}

	// Nie gezeigt: unbekannte Ereignisse, ein Ereignis, das kein Text ist,
	// eine Zeile ohne Ereignis; die Einträge nur für den Admin nicht der
	// Lese-Rolle.
	for _, line := range []string{`{"event":"geheim","key":"annakey00001"}`, `{"event":5}`, `{"event":["key_added"]}`, `{"noevent":1}`, `{"event":""}`} {
		if v, ok := View(entryOf(t, line), true); ok || v != nil {
			t.Errorf("%s: gezeigt: %v", line, v)
		}
	}
	for _, line := range []string{`{"event":"notify_changed","by":"a"}`, `{"event":"setting_changed","by":"a"}`, `{"event":"state_rewound","source":"prev","by":"system"}`} {
		if _, ok := View(entryOf(t, line), false); ok {
			t.Errorf("%s: der Lese-Rolle gezeigt", line)
		}
		if _, ok := View(entryOf(t, line), true); !ok {
			t.Errorf("%s: dem Admin nicht gezeigt", line)
		}
	}
	// Was View liefert, teilt nichts mit dem Eintrag.
	e := entryOf(t, `{"event":"key_added","by":"admin","label":"Anna","key":"anna..."}`)
	v, _ := View(e, true)
	v["label"] = "verändert"
	if e.Str("label") != "Anna" {
		t.Error("View teilt Werte mit dem Eintrag")
	}
}

// Einträge, wie die PHP-Fassung sie schrieb und die Übernahme sie
// unverändert in history.jsonl legt: ohne uid, ohne Felder, die neuere
// Fassungen schreiben (demo), Zahlen als Text – sie stehen neben Einträgen
// des Go-Servers und werden gezeigt wie diese.
func TestOldEntriesWithoutUID(t *testing.T) {
	c, _ := newCore(t, map[string]string{"history.jsonl": strings.Join([]string{
		`{"event":"nas_alarm_started","by":"admin","resend":0,"log_at":1763372700}`,
		`{"event":"nas_alarm","by":"admin","ok":true,"message":"Alarm an 2 von 2 Empf\u00e4ngern zugestellt.","results":[{"id":1,"label":"Anna","key":"annakey00001","ok":true,"http":200,"tries":1}],"resend":0,"log_at":1763372701}`,
		`{"event":"box_offline","minutes":"20","log_at":"1763372760"}`,
		`{"event":"created","command":{"id":"7","type":"TEST","state":"pending","created_at":1763372800,"created_by":"admin"},"log_at":1763372800}`,
	}, "\n") + "\n"})
	c.Store.Log(model.NewEntry(model.EvKeyAdded, time.Unix(1763372900, 0), map[string]any{"by": "admin", "label": "Neu", "key": "neuk..."}))

	got := Read(c, 10, "all", false)
	want := []string{
		`{"at":1763372900,"event":"key_added","by":"admin","label":"Neu","key":"neuk..."}`,
		`{"at":1763372800,"event":"created","id":7,"type":"TEST","by":"admin"}`,
		`{"at":1763372760,"event":"box_offline","minutes":20}`,
		`{"at":1763372701,"event":"nas_alarm","by":"admin","ok":true,"message":"Alarm an 2 von 2 Empfängern zugestellt.","resend":0}`,
		`{"at":1763372700,"event":"nas_alarm_started","by":"admin","resend":0}`,
	}
	if len(got) != len(want) {
		t.Fatalf("%d Einträge statt %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if g := viewJSON(t, got[i]); g != want[i] {
			t.Errorf("Eintrag %d:\n%s\nerwartet\n%s", i, g, want[i])
		}
	}
	// Die uid des neuen Eintrags geht nicht hinaus.
	if _, has := got[0]["uid"]; has {
		t.Error("uid in der Ansicht")
	}
	// Alarm-Filter: dieselben alten Einträge.
	if got := Read(c, 10, "alarm_core", true); len(got) != 2 || got[0]["event"] != "nas_alarm" {
		t.Errorf("alarm_core: %v", got)
	}
}

// Was die Rolle nicht sehen darf – und was niemand sehen darf (unbekannte
// Ereignisse) –, fällt VOR dem Abzählen weg: Die Lese-Rolle bekommt so viele
// Einträge wie verlangt, sonst verschwände "Ältere anzeigen".
func TestHiddenBeforeCounting(t *testing.T) {
	var lines []string
	at := int64(1763372000)
	for i := 1; i <= 5; i++ {
		lines = append(lines, fmt.Sprintf(`{"event":"key_added","by":"admin","label":"K%d","key":"kkkk...","log_at":%d}`, i, at))
		at++
	}
	for i := range 30 {
		lines = append(lines, fmt.Sprintf(`{"event":"setting_changed","by":"admin","label":"x","value":"%d","log_at":%d}`, i, at))
		at++
		lines = append(lines, fmt.Sprintf(`{"event":"geheim_ereignis","secret":"nie zeigen","log_at":%d}`, at))
		at++
	}
	c, _ := newCore(t, map[string]string{"history.jsonl": strings.Join(lines, "\n") + "\n"})
	labels := func(views []map[string]any) []string {
		var out []string
		for _, v := range views {
			out = append(out, fmt.Sprint(v["event"], ":", v["label"]))
		}
		return out
	}
	want := []string{"key_added:K5", "key_added:K4", "key_added:K3", "key_added:K2", "key_added:K1"}
	for _, filter := range []string{"all", "keys"} {
		if got := labels(Read(c, 5, filter, false)); !slices.Equal(got, want) {
			t.Errorf("Lese-Rolle, %s: %v", filter, got)
		}
	}
	if got := Read(c, 5, "system", false); len(got) != 0 {
		t.Errorf("Lese-Rolle, system: %v", labels(got))
	}
	// Der Admin sieht die Einstellungen – die unbekannten Ereignisse auch er
	// nicht, und sie zählen auch für ihn nicht mit.
	got := Read(c, 5, "all", true)
	if len(got) != 5 {
		t.Fatalf("Admin: %d Einträge", len(got))
	}
	for _, v := range got {
		if v["event"] != "setting_changed" {
			t.Errorf("Admin: %v", v)
		}
	}
	if got := Read(c, 31, "system", true); len(got) != 30 {
		t.Errorf("Admin, system: %d Einträge statt 30", len(got))
	}
}

// Filter wie PHP log_entry_in_filter: Kategorie aus der Tabelle, Befehle je
// nach Typ, unbekannte Ereignisse "system", alarm_core nur Versand-Ereignisse.
func TestInFilter(t *testing.T) {
	cases := []struct {
		line string
		in   []string // die Filter außer all, zu denen er gehört
	}{
		{`{"event":"key_muted"}`, []string{"keys"}},
		{`{"event":"nas_alarm"}`, []string{"alarm", "alarm_core"}},
		{`{"event":"send_interrupted"}`, []string{"alarm", "alarm_core"}},
		{`{"event":"box_offline"}`, []string{"system"}},
		{`{"event":"state_rewound"}`, []string{"system"}},
		{`{"event":"created","command":{"type":"ALARM"}}`, []string{"alarm"}},
		{`{"event":"created","command":{"type":"TEST"}}`, []string{"system"}},
		{`{"event":"created","command":{"id":1}}`, []string{"system"}},
		{`{"event":"created","command":[1],"type":"ALARM"}`, []string{"system"}},
		{`{"event":"created","command":"x","type":"ALARM"}`, []string{"alarm"}},
		{`{"event":"created","type":"ALARM"}`, []string{"alarm"}},
		{`{"event":"late_ack","type":"ALARM"}`, []string{"alarm"}},
		{`{"event":"late_ack","type":["ALARM"]}`, []string{"system"}},
		{`{"event":"late_ack","type":"alarm"}`, []string{"system"}},
		{`{"event":"geheim"}`, []string{"system"}},
		{`{"noevent":1}`, []string{"system"}},
	}
	for _, tc := range cases {
		e := entryOf(t, tc.line)
		if !InFilter(e, "all") {
			t.Errorf("%s: nicht in all", tc.line)
		}
		for _, f := range []string{"alarm", "alarm_core", "keys", "system", "command", "gibtesnicht", ""} {
			if got, want := InFilter(e, f), slices.Contains(tc.in, f); got != want {
				t.Errorf("%s in %q: %v", tc.line, f, got)
			}
		}
	}
}

// Die Vorprüfung an der rohen Zeile ist großzügiger als der Filter: Jede
// Zeile, die View zeigt und InFilter annimmt, besteht sie – für die Zeilen
// beider Fassungen (PHP json_encode, Go encoding/json). Und sie lehnt ab, was
// sicher nicht passt (sonst spart sie nichts).
func TestMayMatch(t *testing.T) {
	lines := []string{
		`{"event":"key_added","by":"admin","label":"Anna","key":"anna...","version":2,"log_at":1763372700}`,
		`{"event":"created","command":{"id":1,"type":"ALARM","created_by":"admin"},"log_at":1763372700}`,
		`{"event":"created","command":{"id":2,"type":"TEST","created_by":"admin"},"log_at":1763372700}`,
		`{"event":"late_ack","id":1,"type":"ALARM","result":"executed","log_at":1763372700}`,
		`{"event":"nas_alarm","by":"admin","ok":true,"message":"x","results":[{"id":1,"label":"Anna","key":"annakey00001","ok":true}],"resend":0,"log_at":1}`,
		`{"event":"box_offline","minutes":20,"log_at":1}`,
		`{"event":"relay_alarm","info":"17.11. 10:05","device_id":"fw-box-1","log_at":1}`,
		`{"event":"setting_changed","by":"admin","label":"x","value":"4 Std","log_at":1}`,
		// Ein Name, der "event" heißt, täuscht nichts vor.
		`{"event":"box_restart","reason":"\"event\":\"key_added\"","log_at":1}`,
		`{"event":"key_muted","label":"event","log_at":1}`,
		// Ein Unterobjekt mit eigenem event zählt mit (großzügig).
		`{"event":"box_offline","x":{"event":"key_added"},"log_at":1}`,
		`{"x":{"event":"box_offline"},"event":"key_added","log_at":1}`,
	}
	for _, e := range []model.Entry{
		model.NewEntry(model.EvKeyAdded, t0, map[string]any{"label": "Ä<>&", "key": "kkkk..."}),
		model.NewEntry(model.EvCancelled, t0, map[string]any{"command": model.Command{ID: 3, Type: "ALARM", State: model.CommandCancelled}}),
		model.NewEntry(model.EvStateRewound, t0, map[string]any{"source": "prev", "by": "system"}),
		model.NewEntry(model.EvSendInterrupted, t0, map[string]any{"kind": "nas_alarm", "started_at": 5}),
	} {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(b))
	}
	for _, line := range lines {
		checkGenerous(t, []byte(line))
	}
	// Was sie sicher ablehnt.
	rejects := []struct {
		filter string
		admin  bool
		line   string
	}{
		{"keys", true, lines[4]},
		{"keys", true, lines[5]},
		{"alarm_core", true, lines[1]},
		{"alarm_core", true, lines[0]},
		{"alarm", true, lines[0]},
		{"alarm", true, lines[2]},
		{"system", true, lines[0]},
		{"system", true, lines[4]},
		{"gibtesnicht", true, lines[5]},
		// Nur für den Admin: für die Lese-Rolle nie.
		{"all", false, lines[7]},
		{"system", false, lines[7]},
		// Unbekannt: für niemanden.
		{"all", true, `{"event":"geheim_ereignis","secret":"nie zeigen","log_at":1}`},
		{"system", true, `{"event":"geheim_ereignis","log_at":1}`},
	}
	for _, r := range rejects {
		if mayMatch([]byte(r.line), r.filter, r.admin) {
			t.Errorf("%s (Admin %v): %s nicht abgelehnt", r.filter, r.admin, r.line)
		}
	}
	// Eine Zeile ohne das Muster wird dekodiert, statt übergangen zu werden.
	for _, f := range []string{"all", "keys", "alarm", "alarm_core", "system"} {
		if !mayMatch([]byte(`{"event" : "key_added"}`), f, false) {
			t.Errorf("%s: Zeile ohne Muster übergangen", f)
		}
	}
}

// parseLine liest eine Verlaufszeile wie die Ablage: genau ein Objekt,
// Zahlen als json.Number.
func parseLine(line []byte) (model.Entry, bool) {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	return model.Entry(m), true
}

// checkGenerous: Zeigt View den Eintrag der Zeile der Rolle und nimmt
// InFilter ihn an, muss die Vorprüfung ihn durchlassen.
func checkGenerous(t testing.TB, line []byte) {
	t.Helper()
	e, ok := parseLine(line)
	if !ok {
		return
	}
	for _, admin := range []bool{true, false} {
		if _, shown := View(e, admin); !shown {
			continue
		}
		for _, f := range []string{"all", "alarm", "alarm_core", "keys", "system"} {
			if InFilter(e, f) && !mayMatch(line, f, admin) {
				t.Errorf("Vorprüfung %s (Admin %v) lehnt ab, was geliefert würde: %s", f, admin, line)
			}
		}
	}
}

// Für jede Zeile, wie encoding/json sie schreibt: Die Vorprüfung lässt alles
// durch, was gezeigt würde.
func FuzzMayMatch(f *testing.F) {
	for _, s := range []string{
		`{"event":"key_added","label":"Anna"}`,
		`{"event":"created","command":{"type":"ALARM"}}`,
		`{"event":"late_ack","type":"ALARM"}`,
		`{"event":"nas_alarm","results":[{"event":"x"}]}`,
		`{"event":"box_offline","x":{"event":"key_added"}}`,
		`{"event":"relay_alarm","source":"remote"}`,
		`{"event":"cancelled","command":{"type":"TEST","cancel_reason":"\"event\":\"x\""}}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var e map[string]any
		if dec.Decode(&e) != nil || e == nil {
			return
		}
		line, err := json.Marshal(e)
		if err != nil {
			return
		}
		checkGenerous(t, line)
	})
}

// Read liest höchstens maxScanBytes an Zeilen: Eine seltene Art ganz unten
// in einem großen Verlauf bleibt dann aus – wie bei PHP (4 MiB).
func TestReadBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"event":"nas_alarm","by":"admin","ok":true,"message":"tief unten","resend":0,"log_at":1}` + "\n")
	filler := `{"event":"key_muted","by":"Kurzbefehl","label":"Anna","key":"anna...","source":"shortcut","log_at":2}` + "\n"
	for b.Len() < maxScanBytes+len(filler) {
		b.WriteString(filler)
	}
	b.WriteString(`{"event":"false_alarm","by":"admin","ok":true,"message":"oben","resend":0,"log_at":3}` + "\n")
	c, _ := newCore(t, map[string]string{"history.jsonl": b.String()})
	got := Read(c, 12, "alarm_core", true)
	if len(got) != 1 || got[0]["message"] != "oben" {
		t.Errorf("alarm_core über die Grenze hinaus: %v", got)
	}
	// Innerhalb der Grenze kommen beide Dateien zusammen.
	c2, _ := newCore(t, map[string]string{
		"history.1.jsonl": `{"event":"nas_alarm","by":"admin","ok":true,"message":"alt","resend":0,"log_at":1}` + "\n",
		"history.jsonl":   strings.Repeat(filler, 1000) + `{"event":"false_alarm","by":"admin","ok":true,"message":"neu","resend":0,"log_at":3}` + "\n",
	})
	got = Read(c2, 12, "alarm_core", true)
	if len(got) != 2 || got[0]["message"] != "neu" || got[1]["message"] != "alt" {
		t.Errorf("über beide Dateien: %v", got)
	}
}

// newCore: ein Core mit einer Ablage, in deren Datenordner files liegen.
func newCore(t testing.TB, files map[string]string) (*core.Core, *storetest.FaultFS) {
	t.Helper()
	f := storetest.New()
	dir := filepath.FromSlash("/daten")
	for name, text := range files {
		f.Put(filepath.Join(dir, name), []byte(text))
	}
	log := slog.New(slog.DiscardHandler)
	st, _, err := store.Open(dir, f, clock.NewFake(t0), log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := st.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return &core.Core{Store: st, Clock: clock.NewFake(t0), Log: log}, f
}

// Was ein Aufruf kostet: ein Verlauf von gut 5 MB (history.jsonl) und
// genauso viel in history.1.jsonl – fast nur Stumm-Wechsel der Kurzbefehle,
// alle 200 Zeilen ein Alarm. Je Filter und Menge wie die Oberfläche sie
// abfragt; zum Vergleich ohne Vorprüfung und ohne Grenze.
func BenchmarkRead(b *testing.B) {
	var text strings.Builder
	for i := 0; text.Len() < 5<<20; i++ {
		if i%200 == 0 {
			fmt.Fprintf(&text, `{"event":"nas_alarm","by":"admin","ok":true,"message":"Alarm an 9 von 9 Empf\u00e4ngern zugestellt.","results":[%s],"resend":0,"demo":false,"log_at":%d}`+"\n",
				strings.TrimSuffix(strings.Repeat(`{"id":1,"label":"Anna M\u00fcller","key":"annakey00001","ok":true,"http":200,"tries":1},`, 9), ","), 1763000000+i)
			continue
		}
		fmt.Fprintf(&text, `{"event":"key_muted","by":"Kurzbefehl","label":"J\u00fcrgen","key":"jrgn...","source":"shortcut","version":%d,"log_at":%d}`+"\n", i, 1763000000+i)
	}
	c, _ := newCore(b, map[string]string{"history.jsonl": text.String(), "history.1.jsonl": text.String()})
	for _, q := range []struct {
		filter string
		limit  int
		admin  bool
	}{{"all", 25, true}, {"all", 100, false}, {"alarm_core", 12, true}, {"alarm", 25, true}, {"keys", 25, true}, {"system", 25, true}} {
		b.Run(fmt.Sprintf("%s-%d-admin=%v", q.filter, q.limit, q.admin), func(b *testing.B) {
			for b.Loop() {
				Read(c, q.limit, q.filter, q.admin)
			}
		})
	}
	for _, q := range []struct {
		filter string
		limit  int
	}{{"alarm_core", 12}, {"system", 25}} {
		b.Run(fmt.Sprintf("ohne-Vorpruefung-und-Grenze/%s-%d", q.filter, q.limit), func(b *testing.B) {
			keep := func(e model.Entry) bool { return InFilter(e, q.filter) }
			for b.Loop() {
				c.Store.HistoryScan(q.limit, 0, nil, keep)
			}
		})
	}
	// Der schlimmste Fall: Jede Zeile besteht die Vorprüfung, keine wird
	// gezeigt (die Lese-Rolle vor lauter Einstellungs-Änderungen) – die
	// Grenze von 4 MiB wird ganz dekodiert.
	var worst strings.Builder
	for i := 0; worst.Len() < 5<<20; i++ {
		fmt.Fprintf(&worst, `{"event":"setting_changed","by":"admin","label":"Automatisch wieder laut nach","value":"%d Std","log_at":%d}`+"\n", i%24, 1763000000+i)
	}
	cw, _ := newCore(b, map[string]string{"history.jsonl": worst.String()})
	b.Run("schlimmster-Fall/all-100-admin=false", func(b *testing.B) {
		for b.Loop() {
			if len(Read(cw, 100, "all", false)) != 0 {
				b.Fatal("gezeigt")
			}
		}
	})
}
