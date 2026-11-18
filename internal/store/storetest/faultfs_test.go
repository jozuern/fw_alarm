package storetest_test

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"testing"

	"fwalarm/internal/store"
	"fwalarm/internal/store/storetest"
)

// Die FaultFS verhält sich, wo die Regeln der Ablage davon abhängen, wie
// das echte System: dieselbe Folge von Aufrufen auf einem echten Ordner und
// auf der FaultFS – gleiches Gelingen oder Scheitern, gleiche Inhalte.
func TestBehavesLikeOSFS(t *testing.T) {
	realDir := t.TempDir()
	fake := storetest.New()
	fakeDir := filepath.FromSlash("/probe")
	if err := fake.MkdirAll(fakeDir); err != nil {
		t.Fatal(err)
	}
	type step struct {
		name string
		run  func(fsys store.FS, d string) (string, error) // Ergebnis zum Vergleich
	}
	p := filepath.Join
	read := func(fsys store.FS, d, name string) (string, error) {
		b, err := fsys.ReadFile(p(d, name))
		return string(b), err
	}
	steps := []step{
		{"schreiben", func(fsys store.FS, d string) (string, error) { return "", fsys.WriteFileSync(p(d, "a"), []byte("1")) }},
		{"verweisen", func(fsys store.FS, d string) (string, error) { return "", fsys.Link(p(d, "a"), p(d, "b")) }},
		{"verweisen, Ziel da", func(fsys store.FS, d string) (string, error) { return "", fsys.Link(p(d, "a"), p(d, "b")) }},
		{"verweisen, Quelle fehlt", func(fsys store.FS, d string) (string, error) { return "", fsys.Link(p(d, "fehlt"), p(d, "c")) }},
		{"dieselbe Datei", func(fsys store.FS, d string) (string, error) {
			return fmt.Sprint(fsys.SameFile(p(d, "a"), p(d, "b")), fsys.SameFile(p(d, "a"), p(d, "fehlt"))), nil
		}},
		{"über einen Verweis kürzen", func(fsys store.FS, d string) (string, error) {
			if err := fsys.WriteFileSync(p(d, "b"), []byte("22")); err != nil {
				return "", err
			}
			return read(fsys, d, "a")
		}},
		{"umbenennen ersetzt das Ziel", func(fsys store.FS, d string) (string, error) {
			if err := fsys.WriteFileSync(p(d, "c"), []byte("3")); err != nil {
				return "", err
			}
			if err := fsys.Rename(p(d, "c"), p(d, "a")); err != nil {
				return "", err
			}
			a, _ := read(fsys, d, "a")
			b, _ := read(fsys, d, "b")
			_, errC := fsys.Stat(p(d, "c"))
			return a + b + fmt.Sprint(errors.Is(errC, errNotExist)), nil
		}},
		{"anhängen", func(fsys store.FS, d string) (string, error) {
			if err := fsys.AppendSync(p(d, "a"), []byte("4")); err != nil {
				return "", err
			}
			return read(fsys, d, "a")
		}},
		{"fehlende Datei entfernen", func(fsys store.FS, d string) (string, error) { return "", fsys.Remove(p(d, "fehlt")) }},
		{"fehlende Datei lesen", func(fsys store.FS, d string) (string, error) {
			_, err := fsys.ReadFile(p(d, "fehlt"))
			return fmt.Sprint(errors.Is(err, errNotExist)), nil
		}},
		{"umbenennen, Quelle fehlt", func(fsys store.FS, d string) (string, error) { return "", fsys.Rename(p(d, "fehlt"), p(d, "a")) }},
		{"Ordner mit Inhalt anlegen", func(fsys store.FS, d string) (string, error) {
			if err := fsys.MkdirAll(p(d, "ordner")); err != nil {
				return "", err
			}
			return "", fsys.WriteFileSync(p(d, "ordner", "f"), []byte("x"))
		}},
		{"in einen Ordner schreiben", func(fsys store.FS, d string) (string, error) {
			return "", fsys.WriteFileSync(p(d, "ordner"), []byte("x"))
		}},
		{"einen Ordner lesen", func(fsys store.FS, d string) (string, error) { return read(fsys, d, "ordner") }},
		{"über einen Ordner umbenennen", func(fsys store.FS, d string) (string, error) { return "", fsys.Rename(p(d, "a"), p(d, "ordner")) }},
		{"Ordner mit Inhalt entfernen", func(fsys store.FS, d string) (string, error) { return "", fsys.Remove(p(d, "ordner")) }},
		{"Ordner ansehen", func(fsys store.FS, d string) (string, error) {
			fi, err := fsys.Stat(p(d, "ordner"))
			if err != nil {
				return "", err
			}
			fa, err := fsys.Stat(p(d, "a"))
			if err != nil {
				return "", err
			}
			return fmt.Sprint(fi.IsDir(), fa.IsDir(), fa.Mode().IsRegular(), fa.Size()), nil
		}},
		{"Ordner durchsehen", func(fsys store.FS, d string) (string, error) {
			names, err := fsys.ReadDir(d)
			return fmt.Sprint(names), err
		}},
		{"fehlenden Ordner durchsehen", func(fsys store.FS, d string) (string, error) {
			names, err := fsys.ReadDir(p(d, "ohne"))
			return fmt.Sprint(len(names), errors.Is(err, errNotExist)), nil
		}},
		{"in einen fehlenden Ordner schreiben", func(fsys store.FS, d string) (string, error) {
			err := fsys.WriteFileSync(p(d, "ohne", "x"), []byte("x"))
			return fmt.Sprint(errors.Is(err, errNotExist)), nil
		}},
		{"in einem fehlenden Ordner anhängen", func(fsys store.FS, d string) (string, error) {
			err := fsys.AppendSync(p(d, "ohne", "x"), []byte("x"))
			return fmt.Sprint(errors.Is(err, errNotExist)), nil
		}},
		{"in einen fehlenden Ordner umbenennen", func(fsys store.FS, d string) (string, error) {
			err := fsys.Rename(p(d, "a"), p(d, "ohne", "a"))
			a, _ := read(fsys, d, "a")
			return fmt.Sprint(err != nil, a), nil
		}},
		{"in einen fehlenden Ordner verweisen", func(fsys store.FS, d string) (string, error) {
			return "", fsys.Link(p(d, "a"), p(d, "ohne", "b"))
		}},
		{"in einem fehlenden Ordner sperren", func(fsys store.FS, d string) (string, error) {
			_, err := fsys.Lock(p(d, "ohne", "server.lock"))
			return fmt.Sprint(errors.Is(err, errNotExist), errors.Is(err, store.ErrLocked)), nil
		}},
		{"der fehlende Ordner bleibt fehlend", func(fsys store.FS, d string) (string, error) {
			_, err := fsys.Stat(p(d, "ohne"))
			return fmt.Sprint(errors.Is(err, errNotExist)), nil
		}},
		{"Datei entfernen", func(fsys store.FS, d string) (string, error) {
			if err := fsys.Remove(p(d, "b")); err != nil {
				return "", err
			}
			_, err := fsys.Stat(p(d, "b"))
			return fmt.Sprint(errors.Is(err, errNotExist)), nil
		}},
		{"sperren", func(fsys store.FS, d string) (string, error) {
			release, err := fsys.Lock(p(d, "server.lock"))
			if err != nil {
				return "", err
			}
			_, err2 := fsys.Lock(p(d, "server.lock"))
			if err := release(); err != nil {
				return "", err
			}
			release3, err3 := fsys.Lock(p(d, "server.lock"))
			if err3 == nil {
				err3 = release3()
			}
			return fmt.Sprint(errors.Is(err2, store.ErrLocked), err3), nil
		}},
	}
	for _, s := range steps {
		gotReal, errReal := s.run(store.OSFS(), realDir)
		gotFake, errFake := s.run(fake, fakeDir)
		if (errReal == nil) != (errFake == nil) || gotReal != gotFake {
			t.Errorf("%s: echt %q %v – FaultFS %q %v", s.name, gotReal, errReal, gotFake, errFake)
		}
	}
}

// errNotExist: "fehlt" – das echte System und die FaultFS melden es beide so.
var errNotExist = fs.ErrNotExist

// Fehler je Datei, Fehler am n-ten Aufruf, Hänger und Schreibschutz.
func TestFaults(t *testing.T) {
	f := storetest.New()
	a := filepath.FromSlash("/d/a")
	boom := errors.New("kaputt")
	if err := f.MkdirAll(filepath.FromSlash("/d")); err != nil {
		t.Fatal(err)
	}

	f.Fail(a, boom)
	if err := f.WriteFileSync(a, []byte("x")); !errors.Is(err, boom) {
		t.Errorf("Fail: %v", err)
	}
	if err := f.Rename(filepath.FromSlash("/d/b"), a); !errors.Is(err, boom) {
		t.Errorf("Fail trifft auch das Ziel: %v", err)
	}
	f.Fail(a, nil)
	if err := f.WriteFileSync(a, []byte("x")); err != nil {
		t.Errorf("nach Fail(nil): %v", err)
	}

	n := f.Calls()
	f.FailCall(n + 2)
	if _, err := f.ReadFile(a); err != nil {
		t.Errorf("Aufruf %d: %v", n+1, err)
	}
	if _, err := f.ReadFile(a); !errors.Is(err, storetest.ErrInjected) {
		t.Errorf("Aufruf %d: %v", n+2, err)
	}
	if _, err := f.ReadFile(a); err != nil {
		t.Errorf("Aufruf %d: %v", n+3, err)
	}
	if log := f.Log(); len(log) != n+3 || log[n+1].Op != "ReadFile" || log[n+1].Err == nil {
		t.Errorf("Log: %v", log[n:])
	}

	f.ShortWrite(a, 3)
	if err := f.WriteFileSync(a, []byte("123456")); err != nil {
		t.Errorf("ShortWrite meldet: %v", err)
	}
	if b, _ := f.Get(a); string(b) != "123" {
		t.Errorf("ShortWrite legt ab: %q", b)
	}
	f.Heal()

	release := f.Hang(a)
	done := make(chan error, 1)
	go func() { done <- f.WriteFileSync(a, []byte("später")) }()
	select {
	case err := <-done:
		t.Fatalf("Hang wartet nicht: %v", err)
	default:
	}
	if _, err := f.ReadFile(filepath.FromSlash("/d/anders")); err == nil {
		t.Error("andere Datei") // fehlt: Fehler erwartet – der Aufruf darf nicht hängen
	}
	release()
	if err := <-done; err != nil {
		t.Errorf("nach release: %v", err)
	}
	if b, _ := f.Get(a); string(b) != "später" {
		t.Errorf("nach release: %q", b)
	}

	denied := errors.New("kein Schreibrecht")
	f.ReadOnly(denied)
	for name, err := range map[string]error{
		"WriteFileSync": f.WriteFileSync(a, nil),
		"Remove":        f.Remove(a),
		"MkdirAll":      f.MkdirAll(filepath.FromSlash("/d/neu")),
	} {
		if !errors.Is(err, denied) {
			t.Errorf("ReadOnly %s: %v", name, err)
		}
	}
	if _, err := f.Lock(filepath.FromSlash("/d/server.lock")); !errors.Is(err, denied) || errors.Is(err, store.ErrLocked) {
		t.Errorf("ReadOnly Lock: %v", err)
	}
	if _, err := f.ReadFile(a); err != nil {
		t.Errorf("ReadOnly lesen: %v", err)
	}
	f.Heal()

	f.NoLinks(true)
	if err := f.Link(a, filepath.FromSlash("/d/c")); err == nil {
		t.Error("NoLinks: Link gelungen")
	}
	f.Heal()

	// Absturz: ab dem n-ten Aufruf scheitert alles; danach sieht ein neuer
	// Prozess dieselben Dateien, Hardlinks verbunden, ohne Fehler und Sperren.
	b := filepath.FromSlash("/d/b")
	if err := f.Link(a, b); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Lock(filepath.FromSlash("/d/server.lock")); err != nil {
		t.Fatal(err)
	}
	f.CrashAt(f.Calls() + 2)
	if err := f.WriteFileSync(filepath.FromSlash("/d/vorher"), []byte("1")); err != nil {
		t.Errorf("vor dem Absturz: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := f.WriteFileSync(filepath.FromSlash("/d/nachher"), []byte("2")); !errors.Is(err, storetest.ErrInjected) {
			t.Errorf("nach dem Absturz: %v", err)
		}
	}
	g := f.AfterCrash()
	if got, ok := g.Get(filepath.FromSlash("/d/vorher")); !ok || string(got) != "1" {
		t.Errorf("AfterCrash: vorher %q", got)
	}
	if _, ok := g.Get(filepath.FromSlash("/d/nachher")); ok {
		t.Error("AfterCrash: nachher geschrieben")
	}
	if !g.SameFile(a, b) {
		t.Error("AfterCrash: Hardlink getrennt")
	}
	g.Put(a, []byte("neu"))
	if got, _ := g.Get(b); string(got) != "neu" {
		t.Errorf("AfterCrash: Hardlink nicht verbunden: %q", got)
	}
	if got, _ := f.Get(a); string(got) == "neu" {
		t.Error("AfterCrash teilt Dateien mit dem Original")
	}
	if release, err := g.Lock(filepath.FromSlash("/d/server.lock")); err != nil {
		t.Errorf("AfterCrash: Sperre hängt noch: %v", err)
	} else if err := release(); err != nil {
		t.Error(err)
	}
	if !slices.Contains(f.Names(), a) {
		t.Errorf("Names: %v", f.Names())
	}
}
