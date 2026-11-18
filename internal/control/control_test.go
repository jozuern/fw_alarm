package control

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func quiet() *slog.Logger { return slog.New(slog.DiscardHandler) }

// shortDir: ein Ordner mit kurzem Pfad (Unix-Sockets vertragen höchstens
// etwa 100 Bytes), gelöscht am Ende des Tests.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "fwc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	return dir
}

// startServe startet Serve und wartet, bis der Socket antwortet. Am Ende des
// Tests wird es beendet, und der Test wartet darauf.
func startServe(t *testing.T, sock string, h Hooks) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan error, 1)
	go func() { ch <- Serve(ctx, sock, h, quiet()) }()
	t.Cleanup(func() {
		cancel()
		if err := <-ch; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, _, err := Call(sock, "health", time.Second); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Steuer-Zugang antwortet nicht")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type recorder struct {
	health  atomic.Pointer[Health]
	flushes atomic.Int32
	reloads atomic.Int32
	stops   atomic.Int32
	flush   atomic.Pointer[error]
	reload  atomic.Pointer[error]
}

func (r *recorder) hooks() Hooks {
	return Hooks{
		Check:  func(context.Context) string { return "WEB_OK Probe" },
		Health: func() Health { return *r.health.Load() },
		Flush: func(context.Context) error {
			r.flushes.Add(1)
			if e := r.flush.Load(); e != nil {
				return *e
			}
			return nil
		},
		Reload: func() error {
			r.reloads.Add(1)
			if e := r.reload.Load(); e != nil {
				return *e
			}
			return nil
		},
		Stop: func() { r.stops.Add(1) },
	}
}

func call(t *testing.T, sock, cmd string) (string, bool) {
	t.Helper()
	out, ok, err := Call(sock, cmd, 5*time.Second)
	if err != nil {
		t.Fatalf("%s: %v", cmd, err)
	}
	return out, ok
}

func TestControlRoundTrip(t *testing.T) {
	sock := filepath.Join(shortDir(t), "c.sock")
	r := &recorder{}
	r.health.Store(&Health{Version: "v1.2", State: "normal", SaveOK: true, ConfigOK: true,
		Hints: []string{"erster Hinweis", "zweiter\nüber zwei Zeilen"}})
	startServe(t, sock, r.hooks())

	if runtime.GOOS != "windows" {
		info, err := os.Stat(sock)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("Rechte des Sockets %v, erwartet 0600", info.Mode().Perm())
		}
	}

	out, ok := call(t, sock, "health")
	want := "running=1\nversion=v1.2\nstate=normal\nsave_ok=1\nconfig_ok=1\nhint=erster Hinweis\nhint=zweiter über zwei Zeilen\n"
	if out != want || !ok {
		t.Errorf("health: ok=%v\n%s\nerwartet\n%s", ok, out, want)
	}
	for _, h := range []Health{
		{Version: "v", State: "normal", SaveOK: false, ConfigOK: true},
		{Version: "v", State: "normal", SaveOK: true, ConfigOK: false},
		{Version: "v", State: "emergency", SaveOK: true, ConfigOK: true},
	} {
		r.health.Store(&h)
		out, ok := call(t, sock, "health")
		if ok {
			t.Errorf("health %+v: ok", h)
		}
		if !strings.Contains(out, "state="+h.State+"\n") ||
			!strings.Contains(out, "save_ok="+map[bool]string{true: "1", false: "0"}[h.SaveOK]+"\n") ||
			!strings.Contains(out, "config_ok="+map[bool]string{true: "1", false: "0"}[h.ConfigOK]+"\n") {
			t.Errorf("health %+v:\n%s", h, out)
		}
	}

	if out, ok := call(t, sock, "check"); out != "WEB_OK Probe" || !ok {
		t.Errorf("check: %q %v", out, ok)
	}

	if out, ok := call(t, sock, "flush"); !ok || out != "Alles gesichert." || r.flushes.Load() != 1 {
		t.Errorf("flush: %q %v (%d Aufrufe)", out, ok, r.flushes.Load())
	}
	flushErr := errors.New("Sichern gescheitert: runtime.json")
	r.flush.Store(&flushErr)
	if out, ok := call(t, sock, "flush"); ok || !strings.Contains(out, "runtime.json") || !strings.HasPrefix(out, "Fehler: ") {
		t.Errorf("flush mit Fehler: %q %v", out, ok)
	}

	if out, ok := call(t, sock, "reload"); !ok || out != "Konfiguration neu geladen." || r.reloads.Load() != 1 {
		t.Errorf("reload: %q %v", out, ok)
	}
	reloadErr := errors.New("unbekannter Schlüssel „farbe“")
	r.reload.Store(&reloadErr)
	if out, ok := call(t, sock, "reload"); ok || !strings.Contains(out, "farbe") || !strings.HasPrefix(out, "Fehler: ") {
		t.Errorf("reload mit Fehler: %q %v", out, ok)
	}

	if out, ok := call(t, sock, "stop"); !ok || out != "Server wird beendet." {
		t.Errorf("stop: %q %v", out, ok)
	}
	waitFor(t, "Stop", func() bool { return r.stops.Load() == 1 })

	if _, _, err := Call(sock, "unbekannt", time.Second); err == nil {
		t.Error("ein unbekannter Befehl wurde gesendet")
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: nicht eingetreten", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Läuft der Aufrufer ab, endet auch die Arbeit auf der Server-Seite.
func TestControlCallTimeoutCancels(t *testing.T) {
	sock := filepath.Join(shortDir(t), "c.sock")
	var cancelled atomic.Bool
	h := Hooks{
		Health: func() Health { return Health{Version: "v", State: "normal", SaveOK: true, ConfigOK: true} },
		Flush: func(ctx context.Context) error {
			<-ctx.Done()
			cancelled.Store(true)
			return ctx.Err()
		},
	}
	startServe(t, sock, h)
	if _, _, err := Call(sock, "flush", 200*time.Millisecond); err == nil {
		t.Fatal("flush ohne Ende hat geantwortet")
	}
	waitFor(t, "Abbruch des Flush", cancelled.Load)
}

func TestControlStopEndsServe(t *testing.T) {
	sock := filepath.Join(shortDir(t), "c.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	h := Hooks{
		Health: func() Health { return Health{Version: "v", State: "normal", SaveOK: true, ConfigOK: true} },
		Stop:   cancel,
	}
	go func() { done <- Serve(ctx, sock, h, quiet()) }()
	waitFor(t, "Start", func() bool { _, _, err := Call(sock, "health", time.Second); return err == nil })

	out, ok, err := Call(sock, "stop", 5*time.Second)
	if err != nil || !ok || out != "Server wird beendet." {
		t.Fatalf("stop: %q %v %v", out, ok, err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve endet nach stop nicht")
	}
	if _, err := os.Lstat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Socket-Datei nach dem Ende: %v", err)
	}
	if _, _, err := Call(sock, "health", time.Second); err == nil {
		t.Error("nach dem Ende antwortet noch jemand")
	}
	if !WaitGone(sock, time.Second) {
		t.Error("WaitGone sieht den beendeten Server noch")
	}
}

func TestStaleControlSocket(t *testing.T) {
	dir := shortDir(t)
	healthy := Hooks{Health: func() Health { return Health{Version: "v", State: "normal", SaveOK: true, ConfigOK: true} }}

	// Liegen geblieben nach einem harten Abbruch: wird ersetzt.
	stale := filepath.Join(dir, "alt.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: stale, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ln.SetUnlinkOnClose(false)
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(stale); err != nil {
		t.Fatalf("verwaister Socket fehlt: %v", err)
	}
	startServe(t, stale, healthy)
	if out, ok := call(t, stale, "health"); !ok || !strings.HasPrefix(out, "running=1\n") {
		t.Errorf("über den ersetzten Socket: %q %v", out, ok)
	}

	// Ein Socket, an dem noch jemand lauscht, wird nie übernommen.
	busy := filepath.Join(dir, "belegt.sock")
	other, err := net.Listen("unix", busy)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	go func() {
		for {
			c, err := other.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if err := Serve(context.Background(), busy, healthy, quiet()); err == nil || !strings.Contains(err.Error(), "in Benutzung") {
		t.Errorf("belegter Socket: %v", err)
	}
	if _, err := os.Lstat(busy); err != nil {
		t.Errorf("der belegte Socket wurde gelöscht: %v", err)
	}

	// Etwas anderes als ein Socket wird nie gelöscht (eine falsch
	// eingetragene Datei, etwa state.json).
	file := filepath.Join(dir, "state.json")
	if err := os.WriteFile(file, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Serve(context.Background(), file, healthy, quiet()); err == nil || !strings.Contains(err.Error(), "kein Socket") {
		t.Errorf("Datei statt Socket: %v", err)
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "{}" {
		t.Errorf("die Datei wurde angefasst: %q %v", data, err)
	}

	// Zu lang für einen Unix-Socket: klare Meldung.
	long := filepath.Join(dir, strings.Repeat("x", 120)+".sock")
	if err := Serve(context.Background(), long, healthy, quiet()); err == nil || !strings.Contains(err.Error(), "zu lang") {
		t.Errorf("zu langer Pfad: %v", err)
	}
}

func TestCallWithoutServer(t *testing.T) {
	sock := filepath.Join(shortDir(t), "c.sock")
	if _, _, err := Call(sock, "health", time.Second); err == nil || !strings.Contains(err.Error(), sock) {
		t.Errorf("ohne Server: %v", err)
	}
	if !WaitGone(sock, time.Second) {
		t.Error("WaitGone ohne Server")
	}
}

// Ein Nachfolger hat den Pfad schon übernommen (schneller Neustart): Das
// Ende des alten Servers löscht seinen Socket nicht.
func TestSocketLeavesSuccessorAlone(t *testing.T) {
	sock := filepath.Join(shortDir(t), "c.sock")
	healthy := Hooks{Health: func() Health { return Health{Version: "v", State: "normal", SaveOK: true, ConfigOK: true} }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, sock, healthy, quiet()) }()
	waitFor(t, "Start", func() bool { _, _, err := Call(sock, "health", time.Second); return err == nil })

	if err := os.Remove(sock); err != nil {
		t.Fatal(err)
	}
	successor, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer successor.Close()
	go func() {
		for {
			c, err := successor.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	cancel()
	if err := <-done; err != nil {
		t.Errorf("Serve: %v", err)
	}
	if _, err := os.Lstat(sock); err != nil {
		t.Fatalf("der Socket des Nachfolgers wurde gelöscht: %v", err)
	}
	conn, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		t.Fatalf("der Nachfolger ist nicht mehr erreichbar: %v", err)
	}
	conn.Close()

	// Und ein eigener Socket, den niemand übernommen hat, verschwindet mit
	// ListenUnix + Close wie bisher.
	own := filepath.Join(filepath.Dir(sock), "eigen.sock")
	ln, err := ListenUnix(own)
	if err != nil {
		t.Fatal(err)
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(own); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("eigener Socket nach Close: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Errorf("zweites Close: %v", err)
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// Verwaist ist ein Socket nur, wenn der Verbindungsversuch sagt, dass dort
// niemand lauscht (verweigert) oder die Datei inzwischen fort ist. Jeder
// andere Fehler – fehlende Rechte, keine Antwort – lässt die Datei liegen.
func TestStaleOnlyWhenRefused(t *testing.T) {
	dial := func(err error) error {
		return &net.OpError{Op: "dial", Net: "unix", Err: os.NewSyscallError("connect", err)}
	}
	cases := []struct {
		name  string
		err   error
		stale bool
	}{
		{"verweigert", dial(refusedErrno), true},
		{"Datei fort", dial(syscall.ENOENT), true},
		{"keine Rechte", dial(os.ErrPermission), false},
		{"keine Antwort", dial(timeoutErr{}), false},
		{"anderer Fehler", dial(errors.New("seltsam")), false},
	}
	for _, tc := range cases {
		if got := staleSocket(tc.err); got != tc.stale {
			t.Errorf("%s: verwaist=%v, erwartet %v", tc.name, got, tc.stale)
		}
	}
}

// Unter Linux: ein Socket ohne Rechte wird nie als verwaist gelöscht.
func TestStaleSocketWithoutPermission(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("nur unter Unix und nicht als root (root darf trotz Rechten 0000 verbinden)")
	}
	sock := filepath.Join(shortDir(t), "zu.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := os.Chmod(sock, 0); err != nil {
		t.Fatal(err)
	}
	healthy := Hooks{Health: func() Health { return Health{Version: "v", State: "normal", SaveOK: true, ConfigOK: true} }}
	if err := Serve(context.Background(), sock, healthy, quiet()); err == nil || !strings.Contains(err.Error(), "nicht gelöscht") {
		t.Errorf("Socket ohne Rechte: %v", err)
	}
	if _, err := os.Lstat(sock); err != nil {
		t.Errorf("der Socket ohne Rechte wurde gelöscht: %v", err)
	}
}
