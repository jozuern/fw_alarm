package store_test

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"fwalarm/internal/clock"
	"fwalarm/internal/store"
)

// Echter Ordner: Ein zweites Open auf denselben Datenordner bricht mit
// ErrLocked ab (unter Linux und Windows sperren sich auch zwei Öffnungen im
// selben Prozess aus); nach Close geht es.
func TestLockSecondOpen(t *testing.T) {
	d := t.TempDir()
	first, info := openIn(t, d, store.OSFS())
	if info.LockUnavailable {
		t.Fatal("Sperre nicht angelegt")
	}
	second, _, err := store.Open(d, store.OSFS(), clock.NewFake(t0), quiet())
	if !errors.Is(err, store.ErrLocked) || second != nil {
		t.Fatalf("zweites Open: %v", err)
	}
	if err.Error() != "ein anderes fw-alarm arbeitet schon in diesem Datenordner" {
		t.Errorf("Text: %q", err)
	}
	closeStore(t, first)
	third, info := openIn(t, d, store.OSFS())
	if info.LockUnavailable || third == nil {
		t.Errorf("nach Close: %+v", info)
	}
}

// lockHelperEnv: Ist sie gesetzt, spielt der Testprozess den ANDEREN
// Prozess, der server.lock im genannten Ordner hält.
const lockHelperEnv = "FW_STORE_LOCK_HELPER"

// Echter Ordner, echter zweiter Prozess: Solange er die Sperre hält, bricht
// Open mit ErrLocked ab; ist er weg, geht es.
func TestLockHeldByOtherProcess(t *testing.T) {
	if d := os.Getenv(lockHelperEnv); d != "" {
		release, err := store.OSFS().Lock(filepath.Join(d, "server.lock"))
		if err != nil {
			fmt.Println("fehler:", err)
			return
		}
		fmt.Println("gesperrt")
		_, _ = io.Copy(io.Discard, os.Stdin) // bis der Test die Eingabe schließt; ihr Inhalt zählt nicht
		if err := release(); err != nil {
			fmt.Println("freigeben:", err)
		}
		return
	}

	d := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHeldByOtherProcess$")
	cmd.Env = append(os.Environ(), lockHelperEnv+"="+d)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill() // läuft er noch, hat der Test schon versagt
		_ = cmd.Wait()
	})
	out := bufio.NewReader(stdout)
	line, err := out.ReadString('\n')
	if strings.TrimSpace(line) != "gesperrt" {
		t.Fatalf("Hilfsprozess: %q %v", line, err)
	}

	s, _, err := store.Open(d, store.OSFS(), clock.NewFake(t0), quiet())
	if !errors.Is(err, store.ErrLocked) || s != nil {
		t.Fatalf("Open, während ein anderer Prozess sperrt: %v", err)
	}

	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	rest, _ := io.ReadAll(out) // Rest der Ausgabe (PASS …), nur für die Meldung unten
	if err := cmd.Wait(); err != nil {
		t.Fatalf("Hilfsprozess: %v\n%s", err, rest)
	}
	_, info := openIn(t, d, store.OSFS())
	if info.LockUnavailable {
		t.Error("nach dem Ende des anderen Prozesses keine Sperre")
	}
}
