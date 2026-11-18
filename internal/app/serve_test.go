package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"fwalarm/internal/clock"
	"fwalarm/internal/config"
	"fwalarm/internal/control"
	"fwalarm/internal/store"
)

// syncBuffer: Puffer, den mehrere Goroutinen beschreiben dürfen.
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

// bench ist ein Server-Aufbau in einem Ordner mit kurzem Pfad (Unix-Sockets
// vertragen höchstens etwa 100 Bytes): config.toml, die Kopie, der
// Datenordner, Steuer-Socket und – wenn nicht anders gesetzt – ein
// Unix-Socket für HTTP.
type bench struct {
	t                                 *testing.T
	dir, config, lastGood, state, ctl string
	http                              string // Pfad des HTTP-Sockets ("" bei TCP)
}

func newBench(t *testing.T) *bench {
	t.Helper()
	dir, err := os.MkdirTemp("", "fwa")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	b := &bench{t: t, dir: dir, config: filepath.Join(dir, "config.toml"),
		state: filepath.Join(dir, "state"), ctl: filepath.Join(dir, "c.sock"), http: filepath.Join(dir, "h.sock")}
	b.lastGood = filepath.Join(b.state, config.LastGoodName)
	b.writeConfig(nil)
	return b
}

// configText baut eine gültige Konfiguration; set ändert sie. bark_host ist
// leer – kein Test erreicht je einen echten Bark-Server.
func (b *bench) configText(set func(c *config.Config)) []byte {
	b.t.Helper()
	c, err := config.ParseAt(nil, time.Now())
	if err != nil {
		b.t.Fatal(err)
	}
	c.BarkHost = ""
	c.StateDir = b.state
	c.Listen = "unix:" + b.http
	c.ControlSocket = b.ctl
	c.CheckIntervalSeconds = 0
	if set != nil {
		set(c)
	}
	return config.Write(c)
}

func (b *bench) writeConfig(set func(c *config.Config)) {
	b.t.Helper()
	if err := os.WriteFile(b.config, b.configText(set), 0o600); err != nil {
		b.t.Fatal(err)
	}
}

// start lässt Serve laufen, bis der Steuer-Zugang antwortet. Das Ergebnis
// von Serve kommt auf done; am Ende des Tests wird es beendet.
func (b *bench) start() (done <-chan error, logs *syncBuffer) {
	b.t.Helper()
	logs = &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan error, 1)
	go func() {
		ch <- Serve(ctx, ServeOptions{ConfigPath: b.config, LastGoodPath: b.lastGood, Version: "test-1",
			Log: slog.New(slog.NewTextHandler(logs, nil))})
	}()
	ended := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		ended <- <-ch
		close(finished)
	}()
	b.t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(30 * time.Second):
			b.t.Error("Serve endet nicht")
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case <-finished:
			b.t.Fatalf("Serve endete beim Start: %v\n%s", <-ended, logs)
		default:
		}
		if _, _, err := control.Call(b.ctl, "health", time.Second); err == nil {
			return ended, logs
		}
		if time.Now().After(deadline) {
			b.t.Fatalf("Server antwortet nicht\n%s", logs)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// run ruft einen der Client-Befehle wie die Befehlszeile.
func (b *bench) run(cmd string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = RunControl(cmd, b.config, b.lastGood, &out, &errOut)
	return code, out.String(), errOut.String()
}

func (b *bench) httpClient() *http.Client {
	sock := b.http
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
		DisableKeepAlives: true,
	}}
}

func get(t *testing.T, client *http.Client, url string) (int, http.Header, string) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, string(body)
}

func waitEnded(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("Serve endet nicht")
		return nil
	}
}

func gone(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s liegt nach dem Ende noch da (%v)", p, err)
		}
	}
}

func TestServeStartAndStop(t *testing.T) {
	b := newBench(t)
	for round := range 2 { // ein zweiter Start nach dem Beenden geht ebenso
		done, logs := b.start()

		status, header, body := get(t, b.httpClient(), "http://fw-alarm/manifest.json")
		if status != 200 || header.Get("Content-Type") != "application/json" || !strings.Contains(body, "{") {
			t.Fatalf("Runde %d: GET /manifest.json: %d %q", round, status, header.Get("Content-Type"))
		}
		if status, _, body := get(t, b.httpClient(), "http://fw-alarm/api/health.php"); status != 404 || body != "" {
			t.Errorf("GET /api/health.php: %d %q", status, body)
		}

		code, out, errOut := b.run("health")
		want := "running=1\nversion=test-1\nstate=normal\nsave_ok=1\nconfig_ok=1\n"
		if code != 0 || out != want || errOut != "" {
			t.Errorf("health: %d\n%s%s", code, out, errOut)
		}
		if code, out, _ := b.run("check"); code != 0 || out != "NOCH_NICHT_GEBAUT\n" {
			t.Errorf("check: %d %q", code, out)
		}
		if code, out, errOut := b.run("flush"); code != 0 || out != "Alles gesichert.\n" {
			t.Errorf("flush: %d %q %q", code, out, errOut)
		}
		if code, out, errOut := b.run("reload"); code != 0 || out != "Konfiguration neu geladen.\n" {
			t.Errorf("reload: %d %q %q", code, out, errOut)
		}

		// Die Kopie der geltenden Fassung liegt im Datenordner.
		data, err := os.ReadFile(b.lastGood)
		if err != nil || !bytes.Equal(data, b.configText(nil)) {
			t.Errorf("Kopie: %v", err)
		}
		if info, err := os.Stat(b.lastGood); err == nil && runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Errorf("Rechte der Kopie %v", info.Mode().Perm())
		}

		code, out, errOut = b.run("stop")
		if code != 0 || out != "Server wird beendet.\n" {
			t.Fatalf("stop: %d %q %q", code, out, errOut)
		}
		// stop kehrt erst zurück, wenn der Server fort ist.
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Serve läuft nach stop weiter")
		}
		gone(t, b.ctl, b.http)
		if !strings.Contains(logs.String(), "Server beendet") {
			t.Errorf("Journal ohne Ende:\n%s", logs)
		}
	}
	// Die Sperre ist frei: Ein anderer darf den Datenordner jetzt öffnen.
	st, _, err := store.Open(b.state, store.OSFS(), clock.Real(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("Datenordner nach dem Ende noch gesperrt: %v", err)
	}
	if err := st.Close(context.Background()); err != nil {
		t.Error(err)
	}
}

func TestServeSecondInstanceAborts(t *testing.T) {
	first := newBench(t)
	done, _ := first.start()

	second := newBench(t)
	second.writeConfig(func(c *config.Config) { c.StateDir = first.state })
	var logs syncBuffer
	err := Serve(context.Background(), ServeOptions{ConfigPath: second.config, LastGoodPath: second.lastGood,
		Version: "test-2", Log: slog.New(slog.NewTextHandler(&logs, nil))})
	if err == nil || !strings.Contains(err.Error(), "ein anderes fw-alarm arbeitet schon in diesem Datenordner") {
		t.Fatalf("zweiter Start: %v", err)
	}
	gone(t, second.ctl, second.http)
	if code, out, _ := first.run("health"); code != 0 || !strings.HasPrefix(out, "running=1\n") {
		t.Errorf("der erste Server leidet: %d %q", code, out)
	}
	if code, _, _ := first.run("stop"); code != 0 {
		t.Fatal("stop")
	}
	if err := waitEnded(t, done); err != nil {
		t.Error(err)
	}
}

// TCP auf 127.0.0.1 mit freiem Port; die Adresse steht im Journal. Beendet
// wird über den Kontext (wie ein Signal).
func TestServeTCP(t *testing.T) {
	b := newBench(t)
	b.writeConfig(func(c *config.Config) { c.Listen = "127.0.0.1:0" })
	b.http = ""
	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, ServeOptions{ConfigPath: b.config, LastGoodPath: b.lastGood, Version: "tcp",
			Log: slog.New(slog.NewTextHandler(logs, nil))})
	}()
	addrRe := regexp.MustCompile(`adresse=(127\.0\.0\.1:\d+)`)
	var addr string
	deadline := time.Now().Add(10 * time.Second)
	for addr == "" {
		if m := addrRe.FindStringSubmatch(logs.String()); m != nil {
			addr = m[1]
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("keine Adresse im Journal:\n%s", logs)
		}
		time.Sleep(10 * time.Millisecond)
	}
	status, _, _ := get(t, &http.Client{}, "http://"+addr+"/manifest.json")
	if status != 200 {
		t.Errorf("GET /manifest.json über TCP: %d", status)
	}
	cancel()
	if err := waitEnded(t, done); err != nil {
		t.Errorf("Serve nach dem Signal: %v", err)
	}
	gone(t, b.ctl)
	if _, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		t.Error("der Port ist nach dem Ende noch offen")
	}
}

func TestServeStartsFromCopy(t *testing.T) {
	b := newBench(t)
	// Ein erster Lauf legt die Kopie an.
	done, _ := b.start()
	if code, _, _ := b.run("stop"); code != 0 {
		t.Fatal("stop")
	}
	if err := waitEnded(t, done); err != nil {
		t.Fatal(err)
	}

	secret := "$2y$10$geheimgeheimgeheimgeheimgeheimgeheimgeheimgeheimgeheim"
	if err := os.WriteFile(b.config, []byte("dashboard_password_hash = \""+secret+"\"\nfarbe = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	done, _ = b.start()
	code, out, errOut := b.run("health")
	if code != 1 || !strings.Contains(out, "config_ok=0\n") || !strings.Contains(out, "save_ok=1\n") ||
		!strings.Contains(out, "farbe") || !strings.Contains(out, "hint=gestartet mit der Kopie") {
		t.Errorf("health mit der Kopie: %d\n%s", code, out)
	}
	if strings.Contains(out+errOut, "geheim") {
		t.Errorf("Ausgabe enthält ein Geheimnis:\n%s%s", out, errOut)
	}
	if !strings.Contains(errOut, "Kopie") {
		t.Errorf("kein Hinweis, dass der Steuer-Zugang aus der Kopie stammt: %q", errOut)
	}
	if code, _, errOut := b.run("reload"); code != 1 || !strings.Contains(errOut, "farbe") {
		t.Errorf("reload der fehlerhaften Datei: %d %q", code, errOut)
	}

	b.writeConfig(nil)
	if code, out, errOut := b.run("reload"); code != 0 {
		t.Errorf("reload der reparierten Datei: %d %q %q", code, out, errOut)
	}
	if code, out, _ := b.run("health"); code != 0 || !strings.Contains(out, "config_ok=1\n") {
		t.Errorf("health nach der Reparatur: %d\n%s", code, out)
	}
	if code, _, _ := b.run("stop"); code != 0 {
		t.Fatal("stop")
	}
	if err := waitEnded(t, done); err != nil {
		t.Error(err)
	}

	// Weder Datei noch Kopie: kein Start.
	if err := os.WriteFile(b.config, []byte("farbe = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(b.lastGood); err != nil {
		t.Fatal(err)
	}
	err := Serve(context.Background(), ServeOptions{ConfigPath: b.config, LastGoodPath: b.lastGood, Version: "x",
		Log: slog.New(slog.DiscardHandler)})
	if err == nil || !strings.Contains(err.Error(), "farbe") {
		t.Errorf("Start ohne brauchbare Konfiguration: %v", err)
	}
	gone(t, b.ctl, b.http)
}

func TestControlCommandsWithoutServer(t *testing.T) {
	b := newBench(t)
	code, out, errOut := b.run("health")
	if code != 1 || out != "running=0\n" || !strings.HasPrefix(errOut, "Fehler: ") {
		t.Errorf("health: %d %q %q", code, out, errOut)
	}
	for _, cmd := range []string{"check", "flush", "reload", "stop"} {
		if code, out, errOut := b.run(cmd); code != 1 || out != "" || !strings.HasPrefix(errOut, "Fehler: ") {
			t.Errorf("%s: %d %q %q", cmd, code, out, errOut)
		}
	}
}

// Nach einem harten Abbruch liegen beide Sockets noch da – der nächste
// Start ersetzt sie.
func TestServeStaleSockets(t *testing.T) {
	b := newBench(t)
	for _, p := range []string{b.ctl, b.http} {
		ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: p, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		ln.SetUnlinkOnClose(false)
		if err := ln.Close(); err != nil {
			t.Fatal(err)
		}
	}
	done, _ := b.start()
	if status, _, _ := get(t, b.httpClient(), "http://fw-alarm/manifest.json"); status != 200 {
		t.Errorf("GET über den ersetzten Socket: %d", status)
	}
	if code, _, _ := b.run("stop"); code != 0 {
		t.Fatal("stop")
	}
	if err := waitEnded(t, done); err != nil {
		t.Error(err)
	}
	gone(t, b.ctl, b.http)
}

// Scheitert das Lauschen auf listen, bleibt nichts zurück: kein
// Steuer-Socket, keine Sperre.
func TestServeListenFails(t *testing.T) {
	b := newBench(t)
	if err := os.WriteFile(b.http, []byte("keine Socket-Datei"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Serve(context.Background(), ServeOptions{ConfigPath: b.config, LastGoodPath: b.lastGood, Version: "x",
		Log: slog.New(slog.DiscardHandler)})
	if err == nil || !strings.Contains(err.Error(), "kein Socket") {
		t.Fatalf("Start mit belegtem listen: %v", err)
	}
	gone(t, b.ctl)
	if data, err := os.ReadFile(b.http); err != nil || string(data) != "keine Socket-Datei" {
		t.Errorf("die Datei unter listen wurde angefasst: %q %v", data, err)
	}
	st, _, err := store.Open(b.state, store.OSFS(), clock.Real(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("Sperre nach dem gescheiterten Start: %v", err)
	}
	if err := st.Close(context.Background()); err != nil {
		t.Error(err)
	}
}

func TestDefaultLastGoodPath(t *testing.T) {
	t.Setenv("STATE_DIRECTORY", "")
	if got, want := DefaultLastGoodPath(), filepath.Join(config.DefaultStateDir, config.LastGoodName); got != want {
		t.Errorf("ohne STATE_DIRECTORY: %q, erwartet %q", got, want)
	}
	dir := filepath.Join(t.TempDir(), "zustand")
	t.Setenv("STATE_DIRECTORY", dir)
	if got, want := DefaultLastGoodPath(), filepath.Join(dir, config.LastGoodName); got != want {
		t.Errorf("mit STATE_DIRECTORY: %q, erwartet %q", got, want)
	}
	if runtime.GOOS != "windows" {
		// systemd trennt mehrere Ordner mit ":" – es gilt der erste.
		t.Setenv("STATE_DIRECTORY", "/var/lib/fw-alarm:/var/lib/anderes")
		if got := DefaultLastGoodPath(); got != "/var/lib/fw-alarm/"+config.LastGoodName {
			t.Errorf("mehrere Ordner: %q", got)
		}
	}
}
