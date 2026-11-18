package box

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "time/tzdata" // Zeitzonen unabhängig vom Rechner, auf dem der Test läuft

	"fwalarm/internal/bark"
	"fwalarm/internal/bg"
	"fwalarm/internal/clock"
	"fwalarm/internal/config"
	"fwalarm/internal/core"
	"fwalarm/internal/dashapi"
	"fwalarm/internal/model"
	"fwalarm/internal/store"
	"fwalarm/internal/store/storetest"
	"fwalarm/internal/web"
)

// Prüfstand: Ablage im Speicher, gestellte Uhr, Bark-Attrappe auf 127.0.0.1
// (für die Info "wieder laut"), das Maschinen-Token "geheim-token-der-box".
// Alle Keys und Tokens sind erfunden.

var t0 = time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

const machineToken = "geheim-token-der-box"

var dir = filepath.FromSlash("/daten")

var (
	anna = model.Recipient{ID: 1, Key: "annakey00001", Label: "Annabella", Phone: "+49 171 5550123", MuteToken: "annatoken001"}
	bert = model.Recipient{ID: 2, Key: "bertkey00002", Label: "Bert", MuteToken: "berttoken002"}
)

type bench struct {
	t     *testing.T
	c     *core.Core
	fs    *storetest.FaultFS
	keys  web.Handler
	barkN atomic.Int64
}

func newBench(t *testing.T) *bench { return newBenchOn(t, storetest.New()) }

func newBenchOn(t *testing.T, f *storetest.FaultFS) *bench {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	b := &bench{t: t, fs: f}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { b.barkN.Add(1) }))
	t.Cleanup(srv.Close)
	st, _, err := store.Open(dir, f, clock.NewFake(t0), log)
	if err != nil {
		t.Fatal(err)
	}
	group := bg.New(log)
	t.Cleanup(func() {
		if !group.Wait(20 * time.Second) {
			t.Error("Arbeit nebenher endet nicht")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := st.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	sum := sha256.Sum256([]byte(machineToken))
	cfg := &config.Config{Timezone: "Europe/Berlin", BarkHost: srv.URL, MuteMaxSeconds: 43200, OfflineAfterSeconds: 180,
		MachineTokenSHA256: " " + strings.ToUpper(hex.EncodeToString(sum[:])) + " ", BarkKeysAlarm: []string{"notkey00001"},
		DashboardUser: "admin", DashboardPasswordHash: "hash", WatchdogOfflineAfterSeconds: 600}
	b.c = &core.Core{Cfg: func() *config.Config { return cfg }, CfgOK: func() bool { return true }, Store: st,
		Bark: bark.New(func() *config.Config { return cfg }, clock.Real(), log), Clock: clock.NewFake(t0), Bg: group, Log: log, Version: "test"}
	b.keys = web.SecurityHeaders(b.c, KeysHandler(b.c))
	return b
}

func (b *bench) update(fn func(d *model.State)) {
	b.t.Helper()
	if err := b.c.Store.Update(context.Background(), func(d *model.State) error { fn(d); return nil }); err != nil {
		b.t.Fatal(err)
	}
}

func (b *bench) seed(v int, rs ...model.Recipient) {
	b.t.Helper()
	b.update(func(d *model.State) {
		maxID := 0
		for _, r := range rs {
			maxID = max(maxID, r.ID)
		}
		d.NextID, d.KeysVersion, d.Recipients = maxID+1, v, rs
	})
}

// call: api/keys.php wie die Box (POST, urlencoded); Status, Rumpf, Kopfzeilen.
func (b *bench) call(method string, form url.Values) *httptest.ResponseRecorder {
	var r *http.Request
	if form != nil {
		r = httptest.NewRequest(method, "/api/keys.php", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, "/api/keys.php", nil)
	}
	rec := httptest.NewRecorder()
	b.keys(rec, r)
	return rec
}

// fetch: api/keys.php wie die Box; bricht den Test ab, wenn die Antwort
// nicht 200 Klartext ohne Zwischenspeichern ist. Nur aus der Goroutine des
// Tests rufen (FailNow) – nebenläufig: tryFetch.
func (b *bench) fetch(nonce string) string {
	b.t.Helper()
	got, err := b.tryFetch(nonce)
	if err != nil {
		b.t.Fatal(err)
	}
	return got
}

// tryFetch: wie fetch, meldet eine falsche Antwort aber als Fehler – für
// Goroutinen neben dem Test.
func (b *bench) tryFetch(nonce string) (string, error) {
	rec := b.call(http.MethodPost, url.Values{"token": {machineToken}, "device_id": {"fw-box-1"}, "nonce": {nonce}})
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" || rec.Header().Get("Cache-Control") != "no-store" {
		return "", fmt.Errorf("%d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	return rec.Body.String(), nil
}

// Das Format zeichengenau: ok, nonce, version, count, dann je Empfänger
// key/muted/mutevol – Stumme bleiben in der Liste; überfällige kommen als
// laut unter einer NEUEN Version; dauerhaft Stumme bleiben stumm.
func TestKeysAnswer(t *testing.T) {
	b := newBench(t)
	// Ohne Empfänger: Version 0, count 0 – die Not-Liste der Konfiguration
	// geht NICHT an die Box.
	if got := b.fetch("k9"); got != "ok=1\nnonce=k9\nversion=0\ncount=0\n" {
		t.Errorf("leer: %q", got)
	}
	a, bb := anna, bert
	a.Muted, a.MutedAt, a.MuteVolume = true, t0.Unix()-100, 3
	b.seed(7, a, bb)
	if got := b.fetch("abc"); got != "ok=1\nnonce=abc\nversion=7\ncount=2\nkey0=annakey00001\nmuted0=1\nmutevol0=3\nkey1=bertkey00002\nmuted1=0\nmutevol1=0\n" {
		t.Errorf("Liste: %q", got)
	}
	// Überfällig: laut, neue Version (das sichert Expire vor der Antwort).
	b.update(func(d *model.State) { d.Recipients[0].MutedAt = t0.Unix() - 43201 })
	if got := b.fetch("x"); got != "ok=1\nnonce=x\nversion=8\ncount=2\nkey0=annakey00001\nmuted0=0\nmutevol0=3\nkey1=bertkey00002\nmuted1=0\nmutevol1=0\n" {
		t.Errorf("überfällig: %q", got)
	}
	// Dauerhaft stumm bleibt stumm, egal wie alt.
	b.update(func(d *model.State) {
		d.Recipients[1].Muted, d.Recipients[1].MutedAt, d.Recipients[1].MutePermanent = true, 1, true
	})
	if got := b.fetch("x"); !strings.HasSuffix(got, "version=8\ncount=2\nkey0=annakey00001\nmuted0=0\nmutevol0=3\nkey1=bertkey00002\nmuted1=1\nmutevol1=0\n") {
		t.Errorf("dauerhaft: %q", got)
	}
	// Demo-Modus: nur der Demo-Empfänger, mit seinem Stumm-Zustand.
	b.update(func(d *model.State) {
		d.Demo = model.Demo{Enabled: true, KeyID: 2, Key: bert.Key, Label: "Bert"}
		d.KeysVersion = 9
	})
	if got := b.fetch("d"); got != "ok=1\nnonce=d\nversion=9\ncount=1\nkey0=bertkey00002\nmuted0=1\nmutevol0=0\n" {
		t.Errorf("Demo: %q", got)
	}
}

// Name, id, Telefonnummer, Kurzbefehl-Token und "dauerhaft" gehen NIE an die
// Box – nur die Zeilen der Firmware.
func TestKeysNeverDeliverSecrets(t *testing.T) {
	b := newBench(t)
	a := anna
	a.Muted, a.MutedAt, a.MutePermanent, a.MuteVolume = true, t0.Unix(), true, 2
	b.seed(4, a, bert)
	got := b.fetch("n")
	for _, secret := range []string{"Annabella", "Bert", "5550123", "annatoken001", "berttoken002", "permanent", "id", "label",
		"phone", "token", "notkey00001"} {
		if strings.Contains(got, secret) {
			t.Errorf("%q geht an die Box: %q", secret, got)
		}
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		name, _, _ := strings.Cut(line, "=")
		names = append(names, name)
	}
	if want := "ok nonce version count key0 muted0 mutevol0 key1 muted1 mutevol1"; strings.Join(names, " ") != want {
		t.Errorf("Zeilen %v, erwartet %s", names, want)
	}
}

// Das Maschinen-Token zählt nur aus dem Rumpf eines POST; sonst 403 mit dem
// Rumpf genau FORBIDDEN – und Expire läuft gar nicht erst.
func TestKeysForbidden(t *testing.T) {
	b := newBench(t)
	a := anna
	a.Muted, a.MutedAt = true, 1 // überfällig
	b.seed(4, a)
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"GET":                b.call(http.MethodGet, nil),
		"ohne Token":         b.call(http.MethodPost, url.Values{"nonce": {"x"}}),
		"falsches Token":     b.call(http.MethodPost, url.Values{"token": {"falsch"}}),
		"leeres Token":       b.call(http.MethodPost, url.Values{"token": {""}}),
		"Token als Liste":    b.call(http.MethodPost, url.Values{"token[]": {machineToken}}),
		"Token groß":         b.call(http.MethodPost, url.Values{"token": {strings.ToUpper(machineToken)}}),
		"Token mit Leerraum": b.call(http.MethodPost, url.Values{"token": {machineToken + " "}}),
	} {
		if rec.Code != 403 || rec.Body.String() != "FORBIDDEN" {
			t.Errorf("%s: %d %q", name, rec.Code, rec.Body.String())
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/keys.php?token="+machineToken, nil)
	rec := httptest.NewRecorder()
	b.keys(rec, r)
	if rec.Code != 403 || rec.Body.String() != "FORBIDDEN" {
		t.Errorf("Token in der Adresse: %d %q", rec.Code, rec.Body.String())
	}
	if st := b.c.Store.State(); st.KeysVersion != 4 || !st.Recipients[0].Muted {
		t.Errorf("abgewiesen, aber Expire lief: %+v", st)
	}
}

// Notbetrieb: version=0, count=0 – die Box verwirft das und behält ihre
// gespeicherte Liste (auch die Not-Liste der Konfiguration geht nicht an sie).
func TestKeysEmergency(t *testing.T) {
	f := storetest.New()
	f.Put(filepath.Join(dir, "state.json"), []byte("{kaputt"))
	b := newBenchOn(t, f)
	if !b.c.Store.Emergency() {
		t.Fatal("kein Notbetrieb")
	}
	if got := b.fetch("e1"); got != "ok=1\nnonce=e1\nversion=0\ncount=0\n" {
		t.Errorf("%q", got)
	}
	if KeysVersion(b.c) != 0 {
		t.Errorf("KeysVersion %d", KeysVersion(b.c))
	}
}

// Die Antwort wartet auf die Platte höchstens die Frist der Box (1,5 s) – hängt
// das Sichern der Rückschaltung, kommt die Liste trotzdem (überfällig schon
// laut gerechnet, Version noch die alte); gilt die Rückschaltung später, holt
// die Box sie unter der neuen Version.
func TestKeysWithinBoxBudget(t *testing.T) {
	b := newBench(t)
	a := anna
	a.Muted, a.MutedAt = true, 1
	b.seed(4, a)
	release := b.fs.Hang(filepath.Join(dir, "state.json.tmp"))
	start := time.Now()
	got := b.fetch("h")
	if d := time.Since(start); d > 2500*time.Millisecond {
		t.Errorf("Antwort nach %v", d)
	}
	if got != "ok=1\nnonce=h\nversion=4\ncount=1\nkey0=annakey00001\nmuted0=0\nmutevol0=0\n" {
		t.Errorf("%q", got)
	}
	release()
	deadline := time.Now().Add(5 * time.Second)
	for b.c.Store.State().KeysVersion != 5 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := b.fetch("h"); !strings.Contains(got, "version=5\n") {
		t.Errorf("nach dem Freigeben: %q", got)
	}
	// Scheitert das Sichern, ebenso: Antwort sofort, Liste wie gerechnet.
	b.update(func(d *model.State) { d.Recipients[0].Muted, d.Recipients[0].MutedAt = true, 1 })
	b.fs.Fail(filepath.Join(dir, "state.json.tmp"), errors.New("Platte voll"))
	if got := b.fetch("f"); got != "ok=1\nnonce=f\nversion=5\ncount=1\nkey0=annakey00001\nmuted0=0\nmutevol0=0\n" {
		t.Errorf("Sichern scheitert: %q", got)
	}
}

// Version und Inhalt kommen aus EINEM Schnappschuss: Während demo_set (über
// die echte Aktion) mehrmals zwischen Demo und LIVE wechselt, ist jede der
// 1 000 Antworten entweder die volle Liste unter einer LIVE-Version oder die
// Demo-Liste unter einer Demo-Version.
func TestKeysAnswerFromOneSnapshot(t *testing.T) {
	b := newBench(t)
	b.seed(10, anna, bert) // gerade Versionen = LIVE, ungerade = Demo
	api := web.SecurityHeaders(b.c, dashapi.Handler(b.c))
	rec := httptest.NewRecorder()
	sess := web.Begin(b.c, rec, httptest.NewRequest(http.MethodPost, "/index.php", nil), "admin", "admin")
	var cookie string
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "boss925_dashboard" {
			cookie = ck.Value
		}
	}
	demoSet := func(enable bool) {
		form := url.Values{"action": {"demo_set"}, "enabled": {"0"}}
		if enable {
			form = url.Values{"action": {"demo_set"}, "enabled": {"1"}, "id": {"2"}}
		}
		r := httptest.NewRequest(http.MethodPost, "/dashboard_api.php", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Cookie", "boss925_dashboard="+cookie)
		r.Header.Set("X-CSRF-Token", sess.CSRF)
		w := httptest.NewRecorder()
		api(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
			t.Errorf("demo_set: %d %s", w.Code, w.Body.String())
		}
	}
	full := "count=2\nkey0=annakey00001\nmuted0=0\nmutevol0=0\nkey1=bertkey00002\nmuted1=0\nmutevol1=0\n"
	demo := "count=1\nkey0=bertkey00002\nmuted0=0\nmutevol0=0\n"

	var calls atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan string, 1000)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for calls.Add(1) <= 1000 {
				// Kein t.Fatal hier (nicht die Goroutine des Tests): Fehler
				// sammeln, der Test meldet sie nach wg.Wait.
				got, ferr := b.tryFetch("s")
				if ferr != nil {
					errs <- ferr.Error()
					continue
				}
				rest, ok := strings.CutPrefix(got, "ok=1\nnonce=s\nversion=")
				vText, list, _ := strings.Cut(rest, "\n")
				v, err := strconv.Atoi(vText)
				switch {
				case !ok || err != nil:
					errs <- fmt.Sprintf("unlesbar: %q", got)
				case v%2 == 0 && list != full:
					errs <- fmt.Sprintf("LIVE-Version %d mit %q", v, list)
				case v%2 == 1 && list != demo:
					errs <- fmt.Sprintf("Demo-Version %d mit %q", v, list)
				}
			}
		}()
	}
	for i := 0; calls.Load() < 1000; i++ {
		demoSet(i%2 == 0)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if b.c.Store.State().KeysVersion <= 11 {
		t.Errorf("kaum gewechselt: Version %d", b.c.Store.State().KeysVersion)
	}
}

// Nonce: die ersten 80 Bytes, davon nur A-Z a-z 0-9 _ -.
func TestNonce(t *testing.T) {
	for in, want := range map[string]string{
		"":                              "",
		"k1":                            "k1",
		"ab.c!d-e_f g":                  "abcd-e_fg",
		strings.Repeat("x", 100):        strings.Repeat("x", 80),
		"äöü" + strings.Repeat("y", 80): strings.Repeat("y", 74), // ä/ö/ü je 2 Bytes zählen mit, wie PHPs substr
		"a\x00b\nc\r=d&e":               "abcde",
	} {
		if got := Nonce(in); got != want {
			t.Errorf("Nonce(%q) = %q, erwartet %q", in, got, want)
		}
	}
}

// FuzzNonce: höchstens 80 Zeichen, nur die erlaubten, und genau die
// erlaubten Bytes der ersten 80 in ihrer Reihenfolge.
func FuzzNonce(f *testing.F) {
	for _, s := range []string{"", "n1", "ab.c!d-e_f g", strings.Repeat("x", 100), "ä" + strings.Repeat("y", 90), "\xff\xfe-_"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := Nonce(s)
		if len(got) > 80 {
			t.Fatalf("%d Zeichen", len(got))
		}
		head := s
		if len(head) > 80 {
			head = head[:80]
		}
		var want strings.Builder
		for _, c := range []byte(head) {
			if strings.IndexByte("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-", c) >= 0 {
				want.WriteByte(c)
			}
		}
		if got != want.String() {
			t.Fatalf("Nonce(%q) = %q, erwartet %q", s, got, want.String())
		}
	})
}

// KeysVersion: die Version des Zustands.
func TestKeysVersion(t *testing.T) {
	b := newBench(t)
	if KeysVersion(b.c) != 0 {
		t.Error("frisch nicht 0")
	}
	b.seed(42, anna)
	if KeysVersion(b.c) != 42 {
		t.Errorf("%d", KeysVersion(b.c))
	}
}
