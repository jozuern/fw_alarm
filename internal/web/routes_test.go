package web

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fwalarm/internal/core"
)

// syncBuffer: Puffer für das Journal, den mehrere Goroutinen beschreiben dürfen.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func testCore(log *slog.Logger) *core.Core {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &core.Core{Log: log, Version: "test"}
}

// serveOn startet den Server des Routers auf ln und hält ihn bis zum Ende
// des Tests am Laufen.
func serveOn(t *testing.T, rt *Router, ln net.Listener, viaUnix bool) {
	t.Helper()
	srv := rt.Server(viaUnix)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("Serve: %v", err)
		}
	}()
	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
		<-done
	})
}

// serveTCP startet den Server auf 127.0.0.1 mit freiem Port; Ergebnis: host:port.
func serveTCP(t *testing.T, rt *Router) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveOn(t, rt, ln, false)
	return ln.Addr().String()
}

type rawAnswer struct {
	status int
	header http.Header
	body   string
}

// raw schickt genau diese Anfragezeile (ohne Umformung durch einen
// Client) und liest die Antwort.
func raw(t *testing.T, addr, method, target string, header ...string) rawAnswer {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	extra := ""
	for _, h := range header {
		extra += h + "\r\n"
	}
	body := ""
	if method == http.MethodPost {
		body = "a=1"
		extra += "Content-Type: application/x-www-form-urlencoded\r\nContent-Length: 3\r\n"
	}
	if _, err := fmt.Fprintf(conn, "%s %s HTTP/1.1\r\nHost: alarm.example.org\r\nConnection: close\r\n%s\r\n%s",
		method, target, extra, body); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: method})
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return rawAnswer{status: resp.StatusCode, header: resp.Header, body: string(data)}
}

func TestOnlyTableAddressesAnswer(t *testing.T) {
	rt := NewRouter(testCore(nil))
	rt.Handle("/api/poll.php", func(w http.ResponseWriter, r *http.Request) { Text(w, 200, "POLL "+r.Method) })
	addr := serveTCP(t, rt)

	for _, target := range []string{
		"/api/health.php", "/config.toml", "/state.json", "/lib/common.php", "/data/x", "/.git/config",
		"/assets/", "/assets", "/assets/app", "/assets/app/", "/assets/vendor/",
		"/api/", "/api", "/api/poll.php/", "/API/POLL.PHP", "/api/poll.php.bak", "/api/poll",
		"/assets/../manifest.json", "/assets/./app.css", "/manifest.json/", "/nas_dashboard/manifest.json",
		"/config.last-good.toml", "/server.lock", "/history.jsonl", "/runtime.json", "/sessions.json",
		"/webassets.go", "/vendor/modules.txt", "/assets/app.css%00", "/assets/app.css.map",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodHead} {
			a := raw(t, addr, method, target)
			if a.status != http.StatusNotFound || a.body != "" || a.header.Get("Location") != "" {
				t.Errorf("%s %s: %d, Rumpf %q, Location %q – erwartet 404 ohne Rumpf und ohne Umleitung",
					method, target, a.status, a.body, a.header.Get("Location"))
			}
		}
	}

	// Mehrfache Schrägstriche werden zusammengezogen – ohne Umleitung, die
	// aus dem POST der Box ein GET machte.
	for _, target := range []string{"/api/poll.php", "//api//poll.php", "/api///poll.php", "///api/poll.php"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			a := raw(t, addr, method, target)
			if a.status != 200 || a.body != "POLL "+method || a.header.Get("Location") != "" {
				t.Errorf("%s %s: %d %q, Location %q", method, target, a.status, a.body, a.header.Get("Location"))
			}
		}
	}

	// OPTIONS * beantwortet nicht der Server von selbst.
	if a := raw(t, addr, http.MethodOptions, "*"); a.status != http.StatusNotFound {
		t.Errorf("OPTIONS *: %d", a.status)
	}

	// Auch ein gewöhnlicher Client folgt keiner Umleitung, weil es keine gibt.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get("http://" + addr + "//api//poll.php")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("Client: %d", resp.StatusCode)
	}
}

func TestHandleRejectsBadPaths(t *testing.T) {
	rt := NewRouter(testCore(nil))
	h := func(w http.ResponseWriter, r *http.Request) {}
	for _, p := range []string{"", "api/poll.php", "//api/poll.php", "/manifest.json"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Handle(%q) angenommen", p)
				}
			}()
			rt.Handle(p, h)
		}()
	}
}

func TestPanicIs500(t *testing.T) {
	var out syncBuffer
	rt := NewRouter(testCore(slog.New(slog.NewTextHandler(&out, nil))))
	rt.Handle("/kaputt", func(w http.ResponseWriter, r *http.Request) { panic("geheim-aus-der-anfrage") })
	rt.Handle("/halb", func(w http.ResponseWriter, r *http.Request) {
		Text(w, 200, "angefangen")
		panic("geheim-mitten-drin")
	})
	rt.Handle("/abbruch", func(w http.ResponseWriter, r *http.Request) { panic(http.ErrAbortHandler) })
	rt.Handle("/gut", func(w http.ResponseWriter, r *http.Request) { Text(w, 200, "gut") })
	addr := serveTCP(t, rt)

	if a := raw(t, addr, http.MethodGet, "/kaputt?t=geheim-token"); a.status != 500 || a.body != "" {
		t.Errorf("Laufzeitfehler: %d %q", a.status, a.body)
	}
	// Schon angefangen: abgebrochen statt scheinbar vollständig.
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprint(conn, "GET /halb HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	if resp, err := http.ReadResponse(bufio.NewReader(conn), nil); err == nil {
		_, err = io.ReadAll(resp.Body)
		if err == nil {
			t.Error("eine angefangene Antwort sah nach dem Laufzeitfehler vollständig aus")
		}
	}
	conn.Close()
	if a := raw(t, addr, http.MethodGet, "/gut"); a.status != 200 || a.body != "gut" {
		t.Errorf("nach den Laufzeitfehlern: %d %q", a.status, a.body)
	}
	log := out.String()
	if strings.Count(log, "Laufzeitfehler in einer Anfrage abgefangen") != 2 {
		t.Errorf("Journal: %s", log)
	}
	if strings.Contains(log, "geheim") {
		t.Errorf("Journal enthält Anfragedaten: %s", log)
	}
}

func TestServerLimits(t *testing.T) {
	srv := NewRouter(testCore(nil)).Server(false)
	if srv.ReadHeaderTimeout != 10*time.Second || srv.ReadTimeout != 30*time.Second ||
		srv.WriteTimeout != 150*time.Second || srv.IdleTimeout != 120*time.Second || srv.MaxHeaderBytes != 64<<10 {
		t.Errorf("Zeitlimits %v/%v/%v/%v, Kopfzeilen %d", srv.ReadHeaderTimeout, srv.ReadTimeout,
			srv.WriteTimeout, srv.IdleTimeout, srv.MaxHeaderBytes)
	}
	rt := NewRouter(testCore(nil))
	addr := serveTCP(t, rt)
	if a := raw(t, addr, http.MethodGet, "/manifest.json", "X-Gross: "+strings.Repeat("a", 80<<10)); a.status != http.StatusRequestHeaderFieldsTooLarge {
		t.Errorf("80 kB Kopfzeilen: %d", a.status)
	}
}

// Ein Client, der seine Kopfzeilen nicht fertig schickt, wird nach 10 s
// getrennt.
func TestSlowClient(t *testing.T) {
	t.Parallel()
	addr := serveTCP(t, NewRouter(testCore(nil)))
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	start := time.Now()
	if _, err := fmt.Fprint(conn, "GET /manifest.json HTTP/1.1\r\nHost: x\r\n"); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(start.Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(conn)
	elapsed := time.Since(start)
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatalf("nach %v nicht getrennt", elapsed)
	}
	if elapsed < 9*time.Second || elapsed > 15*time.Second {
		t.Errorf("getrennt nach %v, erwartet etwa 10 s", elapsed)
	}
	if strings.HasPrefix(string(data), "HTTP/1.1 200") {
		t.Errorf("Antwort auf unvollständige Kopfzeilen: %q", data)
	}
}

func TestViaUnixSocket(t *testing.T) {
	rt := NewRouter(testCore(nil))
	rt.Handle("/wer", func(w http.ResponseWriter, r *http.Request) { Text(w, 200, fmt.Sprint(ViaUnixSocket(r))) })

	addr := serveTCP(t, rt)
	if a := raw(t, addr, http.MethodGet, "/wer"); a.body != "false" {
		t.Errorf("über TCP: %q", a.body)
	}

	dir, err := os.MkdirTemp("", "fwweb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "h.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	serveOn(t, rt, ln, true)
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}
	defer client.CloseIdleConnections()
	resp, err := client.Get("http://fw-alarm/wer")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "true" {
		t.Errorf("über den Unix-Socket: %q, %v", body, err)
	}
}

// Jeder Pfad: kein Laufzeitfehler, und geantwortet wird nur, was in der
// Tabelle steht (mehrfache Schrägstriche zusammengezogen) – sonst 404 ohne
// Rumpf, nie eine Umleitung.
func FuzzRoutePath(f *testing.F) {
	for _, p := range []string{"/", "//api//poll.php", "/assets/app.css", "/assets/../manifest.json",
		"/assets/", "///", "", "*", "/api/poll.php\x00", "/\xff\xfe", "/assets/app/main.js/"} {
		f.Add(p)
	}
	rt := NewRouter(testCore(nil))
	rt.Handle("/api/poll.php", func(w http.ResponseWriter, r *http.Request) { Text(w, 200, "POLL") })
	f.Fuzz(func(t *testing.T, p string) {
		merged := mergeSlashes(p)
		if strings.Contains(merged, "//") || mergeSlashes(merged) != merged {
			t.Fatalf("mergeSlashes(%q) = %q", p, merged)
		}
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.URL.Path = p
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, req)
		rt.mu.RLock()
		_, known := rt.routes[merged]
		rt.mu.RUnlock()
		switch {
		case rec.Header().Get("Location") != "":
			t.Fatalf("%q: Umleitung", p)
		case !known && (rec.Code != http.StatusNotFound || rec.Body.Len() != 0):
			t.Fatalf("%q: %d mit %d Bytes, erwartet 404 ohne Rumpf", p, rec.Code, rec.Body.Len())
		case known && rec.Code != http.StatusOK:
			t.Fatalf("%q (%q): %d", p, merged, rec.Code)
		}
	})
}
