package dashapi

import (
	"bytes"
	"context"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "time/tzdata" // Zeitzonen unabhängig vom Rechner, auf dem der Test läuft

	"fwalarm/internal/bg"
	"fwalarm/internal/clock"
	"fwalarm/internal/config"
	"fwalarm/internal/core"
	"fwalarm/internal/store"
	"fwalarm/internal/store/storetest"
	"fwalarm/internal/web"
)

// Prüfstand der Dashboard-API: eine Ablage im Speicher, eine gestellte Uhr,
// eine Konfiguration, die der Test austauschen kann, und Sitzungen über
// web.Begin (wie nach einer gelungenen Anmeldung – die Passwortprüfung
// selbst ist Sache von web).

var t0 = time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

type bench struct {
	t   *testing.T
	c   *core.Core
	clk *clock.Fake
	cfg atomic.Pointer[config.Config]
	h   web.Handler
}

func newBench(t *testing.T) *bench {
	t.Helper()
	return newBenchOn(t, storetest.New())
}

// benchDir: der Datenordner des Prüfstands in seiner Dateischicht.
var benchDir = filepath.FromSlash("/daten")

// newBenchOn: der Prüfstand auf einer Dateischicht, in die der Test vorher
// Dateien gelegt hat (etwa einen Verlauf).
func newBenchOn(t *testing.T, f *storetest.FaultFS) *bench {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	b := &bench{t: t, clk: clock.NewFake(t0)}
	st, _, err := store.Open(benchDir, f, clock.NewFake(t0), log)
	if err != nil {
		t.Fatal(err)
	}
	group := bg.New(log)
	t.Cleanup(func() {
		if !group.Wait(10 * time.Second) {
			t.Error("Arbeit nebenher endet nicht")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := st.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	b.cfg.Store(&config.Config{
		DashboardUser: "admin", DashboardPasswordHash: "hash-des-admins",
		DashboardReadonlyUser: "leser", DashboardReadonlyPasswordHash: "hash-des-lesers",
		LoginMaxFailures: 8, LoginLockoutWindowSeconds: 900,
		OfflineAfterSeconds: 180, Timezone: "Europe/Berlin", BarkKeyStatus: "statuskey01",
	})
	b.c = &core.Core{Cfg: b.cfg.Load, CfgOK: func() bool { return true }, Store: st, Clock: b.clk,
		Bg: group, Log: log, Version: "test"}
	b.h = web.SecurityHeaders(b.c, Handler(b.c))
	return b
}

// setCfg ändert eine Kopie der geltenden Konfiguration und setzt sie in Kraft.
func (b *bench) setCfg(change func(c *config.Config)) {
	next := *b.cfg.Load()
	change(&next)
	b.cfg.Store(&next)
}

// login legt eine Sitzung an und liefert den Wert ihres Cookies.
func (b *bench) login(user, role string) (cookie string, s *web.Session) {
	b.t.Helper()
	rec := httptest.NewRecorder()
	s = web.Begin(b.c, rec, httptest.NewRequest(http.MethodPost, "/index.php", nil), user, role)
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "boss925_dashboard" {
			cookie = ck.Value
		}
	}
	if cookie == "" {
		b.t.Fatal("Begin setzt kein Cookie")
	}
	return cookie, s
}

// req baut eine Anfrage an dashboard_api.php: GET mit query, POST mit den
// Feldern form (urlencoded); hdr sind weitere Kopfzeilen.
func req(method, query string, form url.Values, cookie string, hdr ...[2]string) *http.Request {
	var r *http.Request
	target := "/dashboard_api.php"
	if query != "" {
		target += "?" + query
	}
	if form != nil {
		r = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	r.RemoteAddr = "192.0.2.1:40000"
	if cookie != "" {
		r.Header.Set("Cookie", "boss925_dashboard="+cookie)
	}
	for _, h := range hdr {
		r.Header.Add(h[0], h[1])
	}
	return r
}

func (b *bench) serve(r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	b.h(rec, r)
	return rec
}

// withHandle setzt für die Dauer des Tests einen eigenen Handler für name
// (an der Tabelle vorbei – Register nimmt jede Aktion nur einmal).
func withHandle(t *testing.T, name string, fn func(x *Ctx)) {
	t.Helper()
	actions.mu.Lock()
	row := actions.rows[name]
	if row == nil {
		actions.mu.Unlock()
		t.Fatalf("keine Aktion %q", name)
	}
	old := row.Handle
	row.Handle = fn
	actions.mu.Unlock()
	t.Cleanup(func() {
		actions.mu.Lock()
		row.Handle = old
		actions.mu.Unlock()
	})
}

// phpSource liest eine Datei des PHP-Dashboards (CRLF wie LF); ohne sie wird
// der Test übersprungen (die PHP-Fassung geht mit Aufgabe 37).
func phpSource(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "nas_dashboard", name))
	if os.IsNotExist(err) {
		t.Skip("PHP-Fassung nicht mehr da")
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

// multipartRequest: ein POST an dashboard_api.php als multipart/form-data.
func multipartRequest(t *testing.T, fields map[string]string, cookie string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/dashboard_api.php", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.RemoteAddr = "192.0.2.1:40000"
	r.Header.Set("Cookie", "boss925_dashboard="+cookie)
	return r
}

// withStatusPart setzt für die Dauer des Tests einen Teil der Antwort von
// status (an StatusPart vorbei – das nimmt jeden Teil nur einmal).
func withStatusPart(t *testing.T, name string, fn func(x *Ctx) any) {
	t.Helper()
	parts.mu.Lock()
	old, had := parts.fns[name]
	if !had {
		parts.mu.Unlock()
		t.Fatalf("kein Teil %q", name)
	}
	parts.fns[name] = fn
	parts.mu.Unlock()
	t.Cleanup(func() {
		parts.mu.Lock()
		parts.fns[name] = old
		parts.mu.Unlock()
	})
}
