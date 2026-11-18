package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fwalarm/internal/config"
)

func call(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = run(args, strings.NewReader(""), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestRun(t *testing.T) {
	if code, out, _ := call("version"); code != 0 || out != "dev\n" {
		t.Errorf("version: %d %q", code, out)
	}
	if code, _, errOut := call(); code != 2 || !strings.Contains(errOut, "Aufruf:") {
		t.Errorf("ohne Befehl: %d %q", code, errOut)
	}
	if code, _, errOut := call("gibtsnicht"); code != 2 || !strings.Contains(errOut, "Unbekannter Befehl: gibtsnicht") {
		t.Errorf("unbekannter Befehl: %d %q", code, errOut)
	}
	if code, _, _ := call("version", "überzählig"); code != 2 {
		t.Errorf("überzählige Angabe: %d", code)
	}
	if code, out, _ := call("check-config", "--config", "../../server/go/config.example.toml"); code != 0 ||
		!strings.HasSuffix(out, "Konfiguration in Ordnung.\n") {
		t.Errorf("check-config mit --config: %d %q", code, out)
	}
	if code, _, errOut := call("check-config", "-config", "gibt/es/nicht.toml"); code != 1 || !strings.HasPrefix(errOut, "Fehler: ") {
		t.Errorf("check-config ohne Datei: %d %q", code, errOut)
	}
	// hash-password nimmt --config an (wie jeder Befehl), braucht es aber nicht.
	if code, _, errOut := call("hash-password", "--config", "egal.toml"); code != 1 || !strings.Contains(errOut, "\nFehler: ") {
		t.Errorf("hash-password ohne Eingabe: %d %q", code, errOut)
	}
}

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

// writeServerConfig legt in dir eine Konfiguration an (Sockets im kurzen
// Ordner dir, Datenordner state, bark_host leer) und liefert ihren Pfad.
func writeServerConfig(t *testing.T, dir, name, state string) string {
	t.Helper()
	c, err := config.ParseAt(nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c.BarkHost = ""
	c.StateDir = state
	c.Listen = "unix:" + filepath.Join(dir, name+"-h.sock")
	c.ControlSocket = filepath.Join(dir, name+"-c.sock")
	c.CheckIntervalSeconds = 0
	path := filepath.Join(dir, name+".toml")
	if err := os.WriteFile(path, config.Write(c), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// serve, health und stop über die Befehlszeile; ein zweiter Server auf
// demselben Datenordner bricht mit Rückgabe 1 und dem Grund ab.
func TestServeCommands(t *testing.T) {
	dir, err := os.MkdirTemp("", "fwm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	state := filepath.Join(dir, "state")
	lastGood := filepath.Join(state, "config.last-good.toml")
	first := writeServerConfig(t, dir, "eins", state)

	var out, errOut syncBuffer
	codes := make(chan int, 1)
	go func() {
		codes <- run([]string{"serve", "--config", first, "--last-good", lastGood}, strings.NewReader(""), &out, &errOut)
	}()
	t.Cleanup(func() {
		call("stop", "--config", first, "--last-good", lastGood)
		select {
		case <-codes:
		case <-time.After(30 * time.Second):
			t.Error("serve endet nicht")
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		code, stdout, _ := call("health", "--config", first, "--last-good", lastGood)
		if code == 0 && strings.HasPrefix(stdout, "running=1\nversion=dev\n") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Server antwortet nicht: %d %q\n%s%s", code, stdout, out.String(), errOut.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	second := writeServerConfig(t, dir, "zwei", state)
	code, _, stderr := call("serve", "--config", second, "--last-good", lastGood)
	if code != 1 || !strings.HasPrefix(stderr, "Fehler: ") ||
		!strings.Contains(stderr, "ein anderes fw-alarm arbeitet schon in diesem Datenordner") {
		t.Errorf("zweiter serve: %d %q", code, stderr)
	}

	if code, stdout, stderr := call("stop", "--config", first, "--last-good", lastGood); code != 0 || stdout != "Server wird beendet.\n" {
		t.Fatalf("stop: %d %q %q", code, stdout, stderr)
	}
	select {
	case code := <-codes:
		if code != 0 {
			t.Errorf("serve endete mit %d: %s", code, errOut.String())
		}
		codes <- code // für das Aufräumen oben
	case <-time.After(10 * time.Second):
		t.Fatal("serve endet nach stop nicht")
	}
	if code, stdout, _ := call("health", "--config", first, "--last-good", lastGood); code != 1 || stdout != "running=0\n" {
		t.Errorf("health ohne Server: %d %q", code, stdout)
	}
	// --last-good gilt nur für serve und die Befehle an den Server.
	if code, _, _ := call("check-config", "--config", first, "--last-good", lastGood); code != 2 {
		t.Errorf("check-config mit --last-good: %d", code)
	}
}
