package box

import (
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"fwalarm/internal/clock"
	"fwalarm/internal/config"
	"fwalarm/internal/model"
	"fwalarm/internal/store/storetest"
	"fwalarm/internal/web"
)

// Die Felder eines Pushs wie im Prüfstand (Auszug aus BOX_FIELDS der
// Firmware auf main); die alte Firmware sendet oldFirmwareMissing nicht.
var pushFields = []string{
	"device_id", "fw-box-1", "fw_build", "test", "core", "2.0.17", "uptime_s", "360000", "rssi", "-61",
	"relay", "open", "alarm_pending", "0", "last_alarm", "nie", "keys_count", "1", "keys_version", "0",
	"reset_reason", "poweron", "max_alloc_heap", "90000", "alarm_seq", "0", "alarm_source", "", "alarm_gaveup", "0",
}

var oldFirmwareMissing = []string{"core", "alarm_seq", "alarm_source", "max_alloc_heap", "alarm_gaveup"}

type firmware bool

const (
	fwNew firmware = true
	fwOld firmware = false
)

func (fw firmware) String() string {
	if fw == fwNew {
		return "neue Firmware"
	}
	return "alte Firmware"
}

// fieldsOf: die Felder eines Pushs dieser Firmware in ihrer Reihenfolge;
// over ersetzt Werte oder hängt neue Felder an (Name, Wert, …).
func fieldsOf(fw firmware, over ...string) []string {
	out := slices.Clone(pushFields)
	for i := 0; i+1 < len(over); i += 2 {
		found := false
		for j := 0; j+1 < len(out); j += 2 {
			if out[j] == over[i] {
				out[j+1], found = over[i+1], true
			}
		}
		if !found {
			out = append(out, over[i], over[i+1])
		}
	}
	if fw == fwOld {
		var kept []string
		for j := 0; j+1 < len(out); j += 2 {
			if !slices.Contains(oldFirmwareMissing, out[j]) {
				kept = append(kept, out[j], out[j+1])
			}
		}
		out = kept
	}
	return out
}

// encode: ein urlencodierter Rumpf mit den Feldern in genau dieser Reihenfolge.
func encode(pairs ...string) string {
	parts := make([]string, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, url.QueryEscape(pairs[i])+"="+url.QueryEscape(pairs[i+1]))
	}
	return strings.Join(parts, "&")
}

// post: api/status.php mit diesem Rumpf (urlencoded) und diesen Kopfzeilen.
func (b *bench) post(raw string, h http.Header) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/status.php", strings.NewReader(raw))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "192.0.2.9:4711"
	for k, v := range h {
		r.Header[k] = v
	}
	rec := httptest.NewRecorder()
	b.status(rec, r)
	return rec
}

// push: ein Status-Push wie die Box (das Token zuerst); die Antwort muss
// 200 "OK\n" sein.
func (b *bench) push(fw firmware, over ...string) {
	b.t.Helper()
	b.pushWith(nil, fw, over...)
}

func (b *bench) pushWith(h http.Header, fw firmware, over ...string) {
	b.t.Helper()
	rec := b.post(encode(append([]string{"token", machineToken}, fieldsOf(fw, over...)...)...), h)
	if rec.Code != http.StatusOK || rec.Body.String() != "OK\n" {
		b.t.Fatalf("Status-Push: %d %q", rec.Code, rec.Body.String())
	}
}

// settle: wartet, bis die Arbeit nach der Antwort fertig ist.
func (b *bench) settle() {
	b.t.Helper()
	if !b.c.Bg.Wait(10 * time.Second) {
		b.t.Fatal("Arbeit nach der Antwort endet nicht")
	}
}

// advance: die Uhr des Servers um d verstellen (auch zurück).
func (b *bench) advance(d time.Duration) {
	clk := b.c.Clock.(*clock.Fake)
	clk.Set(clk.Now().Add(d))
}

// events: die Verlaufseinträge dieses Ereignisses, neueste zuerst (aus dem
// Speicher – Datei plus noch nicht Angehängtes).
func (b *bench) events(event string) []model.Entry {
	return b.c.Store.History(100, func(e model.Entry) bool { return e.Str("event") == event })
}

// formOf: die Felder dieses Rumpfs, wie der Handler sie liest.
func formOf(t *testing.T, raw string) *web.Form {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/status.php", strings.NewReader(raw))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	f, ok := web.ParseForm(httptest.NewRecorder(), r)
	if !ok {
		t.Fatal("Rumpf nicht lesbar")
	}
	return f
}

// Was vom Push bleibt (PHP: clipped_post_fields(['token'])): höchstens 40
// Felder in der Reihenfolge des Pushs, jeder Wert höchstens 300 Bytes – nie
// mitten in einem Zeichen –, Namen nur aus a-z A-Z 0-9 _ : -, ein Name, der
// dadurch leer wird, fällt weg, ein Name, der dadurch einem früheren gleicht,
// ersetzt dessen Wert an dessen Stelle. Das Token nie – auch nicht unter
// einem Namen, der erst durch das Säubern "token" hieße.
func TestClippedFields(t *testing.T) {
	var many []string
	for i := range 60 {
		many = append(many, fmt.Sprintf("f%d", i), fmt.Sprint(i))
	}
	want40 := model.Fields{{Name: "device_id", Value: "fw-box-1"}}
	for i := range 39 {
		want40 = append(want40, model.Field{Name: fmt.Sprintf("f%d", i), Value: fmt.Sprint(i)})
	}
	cases := []struct {
		name  string
		pairs []string
		want  model.Fields
	}{
		{"höchstens 40 Felder, das Token zählt nicht mit", append([]string{"token", machineToken, "device_id", "fw-box-1"}, many...), want40},
		{"300 Bytes", []string{"lang", strings.Repeat("x", 500)}, model.Fields{{Name: "lang", Value: strings.Repeat("x", 300)}}},
		{"300 Bytes aus ä", []string{"u", strings.Repeat("ä", 200)}, model.Fields{{Name: "u", Value: strings.Repeat("ä", 150)}}},
		{"ein ä, das die Grenze überschritte, fällt ganz weg", []string{"u", "a" + strings.Repeat("ä", 200)},
			model.Fields{{Name: "u", Value: "a" + strings.Repeat("ä", 149)}}},
		{"Feldnamen gesäubert", []string{"we!rd", "1", "!!!", "2", "a:b-c_D9", "3", "a.b", "4", "äb", "5"},
			model.Fields{{Name: "werd", Value: "1"}, {Name: "a:b-c_D9", Value: "3"}, {Name: "ab", Value: "4"}, {Name: "b", Value: "5"}}},
		{"nach dem Säubern gleich: der spätere Wert an der früheren Stelle", []string{"x!y", "1", "z", "2", "xy", "3"},
			model.Fields{{Name: "xy", Value: "3"}, {Name: "z", Value: "2"}}},
		{"nie das Token", []string{"token", machineToken, "tok!en", machineToken, "rssi", "-61"},
			model.Fields{{Name: "rssi", Value: "-61"}}},
		{"leerer Wert bleibt", []string{"alarm_source", ""}, model.Fields{{Name: "alarm_source", Value: ""}}},
	}
	for _, tc := range cases {
		if got := clippedFields(formOf(t, encode(tc.pairs...))); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n %v\nerwartet\n %v", tc.name, got, tc.want)
		}
	}
	// Ungültige Bytes werden schon beim Lesen des Formulars U+FFFD (3 Bytes,
	// eins je zusammenhängender Folge) – gekürzt wird danach, ganze Zeichen:
	// "xx" und 74-mal U+FFFD + "a" sind 298 Bytes, das nächste U+FFFD passte
	// nicht mehr ganz.
	got := clippedFields(formOf(t, "v=xx"+strings.Repeat("%FF%FEa", 100)))
	if want := "xx" + strings.Repeat("\uFFFDa", 74); len(got) != 1 || got[0].Value != want {
		t.Errorf("ungültige Bytes: %q", got)
	}
}

// FuzzClippedFields: was auch immer im Rumpf steht – höchstens 40 Felder,
// jeder Name einmal, nicht leer, nur erlaubte Zeichen, nie "token"; jeder
// Wert gültiges UTF-8 mit höchstens 300 Bytes.
func FuzzClippedFields(f *testing.F) {
	for _, s := range []string{"", "token=x&a=1", "a=1&a=2&b!=3", "x=%FF%FE&%E4=1", strings.Repeat("a=1&", 50) + "z=" + strings.Repeat("ä", 200), "tok!en=1&token=2"} {
		f.Add(s)
	}
	names := regexp.MustCompile(`^[a-zA-Z0-9_:-]+$`)
	f.Fuzz(func(t *testing.T, raw string) {
		got := clippedFields(formOf(t, raw))
		if len(got) > 40 {
			t.Fatalf("%d Felder", len(got))
		}
		seen := map[string]bool{}
		for _, x := range got {
			if !names.MatchString(x.Name) || x.Name == "token" || seen[x.Name] {
				t.Fatalf("Name %q", x.Name)
			}
			seen[x.Name] = true
			if len(x.Value) > 300 || !utf8.ValidString(x.Value) {
				t.Fatalf("Wert %q", x.Value)
			}
		}
	})
}

// Die Antwort wie PHP: 200 "OK\n" als Klartext, nicht zwischenspeicherbar –
// für jeden angenommenen Push, auch einen veralteten oder den eines fremden
// Boards, und für ein Formular als multipart.
func TestStatusAnswers(t *testing.T) {
	b := newBench(t)
	check := func(name string, rec *httptest.ResponseRecorder) {
		t.Helper()
		h := rec.Header()
		if rec.Code != 200 || rec.Body.String() != "OK\n" || h.Get("Content-Type") != "text/plain; charset=utf-8" ||
			h.Get("Cache-Control") != "no-store" || h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: %d %q %v", name, rec.Code, rec.Body.String(), h)
		}
	}
	check("Push", b.post(encode(append([]string{"token", machineToken}, fieldsOf(fwNew, "uptime_s", "5000")...)...), nil))
	b.advance(-30 * time.Second)
	check("veralteter Push", b.post(encode("token", machineToken, "device_id", "fw-box-1", "uptime_s", "4000"), nil))
	if got := b.c.Store.Runtime().Box.Fields.Get("uptime_s"); got != "5000" {
		t.Errorf("veralteter Push verarbeitet: uptime_s %q", got)
	}
	b.configure(func(c *config.Config) { c.BoxDeviceID = "fw-box-1" })
	check("fremdes Board", b.post(encode("token", machineToken, "device_id", "bastel-1"), nil))

	var buf strings.Builder
	mw := multipart.NewWriter(&buf)
	for _, kv := range [][2]string{{"token", machineToken}, {"device_id", "fw-box-1"}, {"rssi", "-77"}} {
		_ = mw.WriteField(kv[0], kv[1]) // in einen strings.Builder: scheitert nie
	}
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/status.php", strings.NewReader(buf.String()))
	r.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	b.status(rec, r)
	check("multipart", rec)
	if got := b.c.Store.Runtime().Box.Fields.Get("rssi"); got != "-77" {
		t.Errorf("multipart: rssi %q", got)
	}
}

// Nur ein falsches oder fehlendes Maschinen-Token antwortet 403 mit dem
// Rumpf genau "FORBIDDEN" (die Box warnt dann ihren Besitzer) – und dann ist
// nichts gespeichert. Ein zu großer Rumpf ist 413, nie 403.
func TestStatusForbidden(t *testing.T) {
	b := newBench(t)
	get := func(target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		b.status(rec, httptest.NewRequest(http.MethodGet, target, nil))
		return rec
	}
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"GET":                 get("/api/status.php"),
		"GET mit Token":       get("/api/status.php?token=" + machineToken),
		"ohne Token":          b.post(encode("device_id", "fw-box-1"), nil),
		"falsches Token":      b.post(encode("token", "falsch", "device_id", "fw-box-1"), nil),
		"leeres Token":        b.post(encode("token", "", "device_id", "fw-box-1"), nil),
		"Token als Liste":     b.post(encode("token[]", machineToken), nil),
		"Token in der Query":  b.postTo("/api/status.php?token="+machineToken, encode("device_id", "fw-box-1")),
		"Token als Kopfzeile": b.post(encode("device_id", "fw-box-1"), http.Header{"Token": {machineToken}}),
	} {
		if rec.Code != 403 || rec.Body.String() != "FORBIDDEN" {
			t.Errorf("%s: %d %q", name, rec.Code, rec.Body.String())
		}
	}
	hash := b.cfg.Load().MachineTokenSHA256
	b.configure(func(c *config.Config) { c.MachineTokenSHA256 = "" })
	if rec := b.post(encode("token", machineToken), nil); rec.Code != 403 || rec.Body.String() != "FORBIDDEN" {
		t.Errorf("ohne eingetragenen Hash: %d %q", rec.Code, rec.Body.String())
	}
	if rec := b.post(encode("token", ""), nil); rec.Code != 403 {
		t.Errorf("ohne Hash, leeres Token: %d", rec.Code)
	}
	b.configure(func(c *config.Config) { c.MachineTokenSHA256 = hash })
	if rec := b.post("token="+machineToken+"&big="+strings.Repeat("x", 70000), nil); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("zu großer Rumpf: %d %q", rec.Code, rec.Body.String())
	}
	b.settle()
	if rt := b.c.Store.Runtime(); rt.Box != nil || len(rt.Devices) != 0 || len(rt.AlarmsSeen) != 0 {
		t.Errorf("abgewiesen, aber gespeichert: %+v", rt)
	}
}

// postTo: wie post, an eine andere Adresse (etwa mit Query).
func (b *bench) postTo(target, raw string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(raw))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	b.status(rec, r)
	return rec
}

// Was gespeichert wird: die Felder ohne das Token, Ankunft auf der Uhr des
// Servers, die Absender-Adresse und ob der Push über die Brücke kam. Es zählt
// allein die Kopfzeile X-FW-Bridge – ihr Vorhandensein, nicht ihr Wert (PHP:
// isset($_SERVER['HTTP_X_FW_BRIDGE'])) –, nie ein Feld.
func TestStatusStored(t *testing.T) {
	b := newBench(t)
	for _, fw := range []firmware{fwNew, fwOld} {
		b.push(fw, "rssi", "-70")
		box := b.c.Store.Runtime().Box
		want := model.Fields{}
		pairs := fieldsOf(fw, "rssi", "-70")
		for i := 0; i+1 < len(pairs); i += 2 {
			want = append(want, model.Field{Name: pairs[i], Value: pairs[i+1]})
		}
		if !reflect.DeepEqual(box.Fields, want) || box.SeenAt != t0.Unix() || box.RemoteAddr != "192.0.2.9" || box.ViaBridge != "0" {
			t.Errorf("%s: %+v", fw, box)
		}
	}
	for name, tc := range map[string]struct {
		h    http.Header
		over []string
		want string
	}{
		"Kopfzeile 1":           {http.Header{"X-Fw-Bridge": {"1"}}, nil, "1"},
		"Kopfzeile 0 zählt":     {http.Header{"X-Fw-Bridge": {"0"}}, nil, "1"},
		"Kopfzeile leer zählt":  {http.Header{"X-Fw-Bridge": {""}}, nil, "1"},
		"ohne Kopfzeile":        {nil, nil, "0"},
		"Feld via_bridge nicht": {nil, []string{"via_bridge", "1"}, "0"},
	} {
		b.pushWith(tc.h, fwNew, tc.over...)
		if got := b.c.Store.Runtime().Box.ViaBridge; got != tc.want {
			t.Errorf("%s: via_bridge %q, erwartet %q", name, got, tc.want)
		}
	}
}

// Die Antwort wartet auf keine Platte: Hängt das Sichern der festen Daten,
// der flüchtigen Daten und des Verlaufs, kommt die Antwort trotzdem sofort –
// auch für einen Push mit Relaisalarm, Neustart (Meldung eingeschaltet) und
// höherer Listen-Version, die alle Arbeit nach der Antwort auslösen. Das
// Dashboard sieht den Push sofort, der Verlauf die Einträge (aus dem
// Speicher).
func TestStatusNeverTouchesDurable(t *testing.T) {
	b := newBench(t)
	b.update(func(d *model.State) {
		d.Notify.Types = map[string]map[string]string{"box_restart": {"enabled": "1"}}
		d.KeysVersion = 5
	})
	b.push(fwNew, "uptime_s", "5000")
	b.advance(100 * time.Second)
	b.settle()
	for _, name := range []string{"state.json.tmp", "runtime.json.tmp", "sessions.json.tmp", "history.jsonl", "recipients.rescue.json.tmp"} {
		release := b.fs.Hang(filepath.Join(dir, name))
		t.Cleanup(release)
	}
	start := time.Now()
	rec := b.post(encode(append([]string{"token", machineToken}, fieldsOf(fwNew, "uptime_s", "20", "reset_reason", "panic",
		"last_alarm", "17.11.2026 11:01:40", "alarm_seq", "1", "keys_version", "41")...)...), nil)
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Errorf("Antwort nach %v", d)
	}
	if rec.Code != 200 || rec.Body.String() != "OK\n" {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	if got := b.c.Store.Runtime().Box.Fields.Get("uptime_s"); got != "20" {
		t.Errorf("Dashboard sieht uptime_s %q", got)
	}
	if len(b.events(model.EvRelayAlarm)) != 1 || len(b.events(model.EvBoxRestart)) != 1 {
		t.Errorf("Verlauf: %v / %v", b.events(model.EvRelayAlarm), b.events(model.EvBoxRestart))
	}
}

// Die Listen-Version nach einem Push (Spec §6.3): Meldet die EINGESTELLTE
// Box (ohne box_device_id: jede) eine höhere, plausible Version (unter 2^31)
// als der Server, setzt eine Änderung nach der Antwort die des Servers auf
// diese + 1 – die Box holt die Liste dann neu. Meldet sie dieselbe Version
// wieder (alle 60 s), wird nichts mehr gesichert.
func TestVersionLiftOnlyExpectedBoardAndPlausible(t *testing.T) {
	type step struct {
		name  string
		fw    firmware
		over  []string
		boxID string
		want  int
		saves int // wie oft state.json danach gesichert wurde
	}
	steps := []step{
		{"höhere Version der Box", fwNew, []string{"keys_version", "41"}, "", 42, 1},
		{"dieselbe Version noch einmal: kein Sichern", fwNew, []string{"keys_version", "41"}, "", 42, 0},
		{"die Box hat aufgeholt", fwNew, []string{"keys_version", "42"}, "", 42, 0},
		{"alte Firmware ebenso", fwOld, []string{"keys_version", "50"}, "", 51, 1},
		{"größte plausible", fwNew, []string{"keys_version", "2147483647"}, "", 2147483648, 1},
		{"ab 2^31 unplausibel", fwNew, []string{"keys_version", "4294967296"}, "", 2147483648, 0},
	}
	b := newBench(t)
	b.update(func(d *model.State) { d.KeysVersion = 5 })
	saves := func() int {
		n := 0
		for _, c := range b.fs.Log() {
			if c.Op == "WriteFileSync" && len(c.Names) > 0 && c.Names[0] == filepath.Join(dir, "state.json.tmp") {
				n++
			}
		}
		return n
	}
	for _, s := range steps {
		before := saves()
		b.configure(func(c *config.Config) { c.BoxDeviceID = s.boxID })
		b.push(s.fw, s.over...)
		b.settle()
		if v, n := b.c.Store.State().KeysVersion, saves()-before; v != s.want || n != s.saves {
			t.Errorf("%s: Version %d, %d-mal gesichert – erwartet %d, %d", s.name, v, n, s.want, s.saves)
		}
	}

	// Unplausibel, fremdes Board, veralteter Push: nie.
	b = newBench(t)
	b.update(func(d *model.State) { d.KeysVersion = 5 })
	for _, v := range []string{"-5", "abc", "", "99999999999999999999"} {
		b.push(fwNew, "keys_version", v)
	}
	b.configure(func(c *config.Config) { c.BoxDeviceID = "fw-box-1" })
	b.push(fwNew, "device_id", "bastel-1", "keys_version", "99")
	b.push(fwNew, "uptime_s", "5000")
	b.advance(-30 * time.Second) // der nächste Push traf früher ein: veraltet
	b.push(fwNew, "uptime_s", "4000", "keys_version", "77")
	b.settle()
	if v := b.c.Store.State().KeysVersion; v != 5 {
		t.Errorf("Version %d nach unplausiblen, fremden und veralteten Pushs", v)
	}
	if got := b.c.Store.Runtime().Box.Fields.Get("keys_version"); got != "0" {
		t.Errorf("gespeichert keys_version %q", got)
	}
	// Die eingestellte Box (Kennzeichen mit Leerraum in der Konfiguration).
	b.configure(func(c *config.Config) { c.BoxDeviceID = " fw-box-1 " })
	b.advance(time.Minute)
	b.push(fwNew, "keys_version", "99")
	b.settle()
	if v := b.c.Store.State().KeysVersion; v != 100 {
		t.Errorf("eingestellte Box: Version %d", v)
	}

	// Notbetrieb: nichts (die festen Daten sind nicht lesbar).
	f := storetest.New()
	f.Put(filepath.Join(dir, "state.json"), []byte("{kaputt"))
	e := newBenchOn(t, f)
	e.push(fwNew, "keys_version", "41")
	e.settle()
	if e.c.Store.State().KeysVersion != 0 || e.c.Store.Runtime().Box == nil {
		t.Errorf("Notbetrieb: Version %d, Status %v", e.c.Store.State().KeysVersion, e.c.Store.Runtime().Box)
	}
}

// Scheitert das Anheben, versucht es der nächste Push erneut – die Antwort
// bleibt 200 "OK\n".
func TestVersionLiftRetriedByNextPush(t *testing.T) {
	b := newBench(t)
	b.update(func(d *model.State) { d.KeysVersion = 5 })
	b.fs.Fail(filepath.Join(dir, "state.json.tmp"), errors.New("Platte voll"))
	b.push(fwNew, "keys_version", "41")
	b.settle()
	if v := b.c.Store.State().KeysVersion; v != 5 {
		t.Fatalf("Version %d trotz scheiterndem Sichern", v)
	}
	b.fs.Heal()
	b.push(fwNew, "keys_version", "41")
	b.settle()
	if v := b.c.Store.State().KeysVersion; v != 42 {
		t.Errorf("nach dem Heilen: Version %d", v)
	}
}

// Viele gleichzeitige Pushs mit demselben neuen Alarm: genau EIN relay_alarm
// – ausgewertet wird in der Änderung selbst, auf dem jeweils neuesten Stand.
func TestRelayAlarmOnceWhenConcurrent(t *testing.T) {
	b := newBench(t)
	b.push(fwNew)
	var wg sync.WaitGroup
	errs := make(chan string, 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := b.post(encode(append([]string{"token", machineToken}, fieldsOf(fwNew, "last_alarm", "A", "alarm_seq", "1")...)...), nil)
			if rec.Code != 200 {
				errs <- fmt.Sprintf("%d %q", rec.Code, rec.Body.String())
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if got := b.events(model.EvRelayAlarm); len(got) != 1 {
		t.Errorf("%d Einträge relay_alarm", len(got))
	}
}

// Neustart: Ins Verlauf nur nach Absturz, Watchdog oder Spannungseinbruch;
// die Meldung an den Admin (Art box_restart, ab Werk aus) nach der Antwort,
// "nur Absturz …" oder "jedem Neustart" – Texte wie PHP (ASCII).
func TestRestartNotice(t *testing.T) {
	b := newBench(t)
	restart := func(fw firmware, reason string) {
		t.Helper()
		b.push(fw, "uptime_s", "5000")
		b.advance(100 * time.Second)
		b.push(fw, "uptime_s", "20", "reset_reason", reason)
		b.settle()
	}
	toAdmin := func() []barkReq {
		var out []barkReq
		for _, r := range b.sent() {
			if r.Key == statusKey {
				out = append(out, r)
			}
		}
		return out
	}
	restart(fwNew, "panic")
	if got := toAdmin(); len(got) != 0 {
		t.Errorf("ab Werk aus, gesendet: %v", got)
	}
	if got := b.events(model.EvBoxRestart); len(got) != 1 || got[0].Str("reason") != "panic" || got[0].Str("device_id") != "fw-box-1" {
		t.Errorf("Verlauf: %v", got)
	}

	b.update(func(d *model.State) { d.Notify.Types = map[string]map[string]string{"box_restart": {"enabled": "1"}} })
	for _, fw := range []firmware{fwNew, fwOld} {
		restart(fw, "brownout")
		restart(fw, "poweron") // nur bei "jedem Neustart"
	}
	got := toAdmin()
	if len(got) != 2 {
		t.Fatalf("Meldungen: %v", got)
	}
	for _, r := range got {
		if r.Fields.Get("title") != "Alarm-Box neu gestartet" || r.Fields.Get("level") != "passive" ||
			!strings.HasPrefix(r.Fields.Get("body"), "[17.11.2026 11:") ||
			!strings.HasSuffix(r.Fields.Get("body"), "] Die Box hat nach einem SPANNUNGSEINBRUCH (Brownout) - Netzteil/Kabel pruefen neu gestartet und meldet sich wieder.") {
			t.Errorf("Meldung: %v", r.Fields)
		}
	}
	if n := len(b.events(model.EvBoxRestart)); n != 3 {
		t.Errorf("%d Einträge box_restart (poweron gehört nicht hinein)", n)
	}

	b.update(func(d *model.State) {
		d.Notify.Types = map[string]map[string]string{"box_restart": {"enabled": "1", "scope": "all"}}
	})
	restart(fwNew, "poweron")
	restart(fwNew, "xyz")
	got = toAdmin()[2:]
	if len(got) != 2 || !strings.HasSuffix(got[0].Fields.Get("body"), "Die Box hat nach dem Einschalten neu gestartet und meldet sich wieder.") ||
		!strings.HasSuffix(got[1].Fields.Get("body"), "Die Box hat nach unbekanntem Grund (xyz) neu gestartet und meldet sich wieder.") {
		t.Errorf("jeder Neustart: %v", got)
	}
}

// Die Gründe im Text der Meldung (PHP: $reasons in api/status.php).
func TestRestartWhy(t *testing.T) {
	for reason, want := range map[string]string{
		"panic":    "einem ABSTURZ (Panic)",
		"task_wdt": "einem WATCHDOG-Reset",
		"int_wdt":  "einem WATCHDOG-Reset",
		"wdt":      "einem WATCHDOG-Reset",
		"brownout": "einem SPANNUNGSEINBRUCH (Brownout) - Netzteil/Kabel pruefen",
		"poweron":  "dem Einschalten",
		"software": "einem Software-Neustart",
		"external": "einem Reset per Taster",
		"":         "unbekanntem Grund ()",
		"Panic":    "unbekanntem Grund (Panic)",
	} {
		if got := restartWhy(reason); got != want {
			t.Errorf("%q: %q, erwartet %q", reason, got, want)
		}
	}
}

// Bytes, die kein UTF-8 sind (Name, Wert, device_id), kommen als gültiger
// Text an – runtime.json lässt sich sichern und streng zurücklesen, und der
// Stand danach ist derselbe.
func TestPushWithInvalidUTF8IsSaved(t *testing.T) {
	b := newBench(t)
	rec := b.post("token="+machineToken+"&device_id=box%FF%FE&rssi=%C3&na%FFme=1&last_alarm=%E4", nil)
	if rec.Code != 200 {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.c.Store.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	raw, ok := b.fs.Get(filepath.Join(dir, "runtime.json"))
	if !ok {
		t.Fatal("runtime.json fehlt")
	}
	saved, err := model.DecodeRuntime(raw)
	if err != nil {
		t.Fatalf("runtime.json nicht lesbar: %v", err)
	}
	mem := b.c.Store.Runtime()
	if !reflect.DeepEqual(saved.Box, mem.Box) || !reflect.DeepEqual(saved.Devices, mem.Devices) || !reflect.DeepEqual(saved.AlarmsSeen, mem.AlarmsSeen) {
		t.Errorf("gesichert %+v\nim Speicher %+v", saved.Box, mem.Box)
	}
	if _, ok := mem.Devices["box\uFFFD"]; !ok {
		t.Errorf("Boards: %v", mem.Devices)
	}
}
