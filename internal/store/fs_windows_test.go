package store_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"fwalarm/internal/store"
)

// holdFile öffnet name so, wie ein Virenscanner es tut: lesend, ohne
// anderen das Schreiben oder Löschen zu erlauben.
func holdFile(t *testing.T, name string) (release func()) {
	t.Helper()
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once // freigegeben vom Zeitgeber und am Ende des Falls
	return func() {
		once.Do(func() {
			if err := windows.CloseHandle(h); err != nil {
				t.Error(err)
			}
		})
	}
}

// Ein Leser hält das Ziel 300 ms fest: Rename, Remove und Link gelingen
// trotzdem (OSFS wiederholt bis zu 1 s). Für Rename und Remove zeigt der
// Versuch ohne Wiederholung, dass das Festhalten wirklich sperrt; Link
// sperrt ein Leser unter Windows nicht – dort genügt das Gelingen.
func TestRetryWhileHeldWindows(t *testing.T) {
	fsys := store.OSFS()
	d := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(d, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := []struct {
		name   string
		held   string
		raw    func() error // ohne Wiederholung; nil = sperrt nicht
		op     func() error
		verify func()
	}{
		{name: "Rename", held: write("ziel-r", "alt"),
			raw: func() error { return os.Rename(write("quelle-r", "neu"), filepath.Join(d, "ziel-r")) },
			op:  func() error { return fsys.Rename(filepath.Join(d, "quelle-r"), filepath.Join(d, "ziel-r")) },
			verify: func() {
				if b, err := os.ReadFile(filepath.Join(d, "ziel-r")); err != nil || string(b) != "neu" {
					t.Errorf("Rename: %q %v", b, err)
				}
			}},
		{name: "Remove", held: write("ziel-x", "x"),
			raw: func() error { return os.Remove(filepath.Join(d, "ziel-x")) },
			op:  func() error { return fsys.Remove(filepath.Join(d, "ziel-x")) },
			verify: func() {
				if _, err := os.Stat(filepath.Join(d, "ziel-x")); !os.IsNotExist(err) {
					t.Errorf("Remove: noch da (%v)", err)
				}
			}},
		{name: "Link", held: write("ziel-l", "l"),
			op: func() error { return fsys.Link(filepath.Join(d, "ziel-l"), filepath.Join(d, "verweis")) },
			verify: func() {
				if !fsys.SameFile(filepath.Join(d, "ziel-l"), filepath.Join(d, "verweis")) {
					t.Error("Link: keine dieselbe Datei")
				}
			}},
	}
	for _, c := range cases {
		release := holdFile(t, c.held)
		if c.raw != nil {
			if err := c.raw(); err == nil {
				release()
				t.Fatalf("%s: gelingt auch ohne Wiederholung – das Festhalten sperrt nicht", c.name)
			}
		}
		timer := time.AfterFunc(300*time.Millisecond, release)
		start := time.Now()
		err := c.op()
		took := time.Since(start)
		timer.Stop()
		release()
		if err != nil {
			t.Errorf("%s: %v (nach %v)", c.name, err, took)
			continue
		}
		if c.raw != nil && took < 250*time.Millisecond {
			t.Errorf("%s: schon nach %v fertig – hat nicht gewartet", c.name, took)
		}
		c.verify()
	}
}
