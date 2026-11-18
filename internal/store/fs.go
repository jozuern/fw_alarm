package store

import (
	"errors"
	"io/fs"
	"os"
	"sort"
)

// FS ist die Dateischicht der Ablage. Das Programm nutzt OSFS; Tests nehmen
// storetest.FaultFS, das Fehler, Hänger und verkürzte Schreibvorgänge
// gezielt einbaut. Namen sind vollständige Pfade.
type FS interface {
	ReadFile(name string) ([]byte, error)
	WriteFileSync(name string, data []byte) error // anlegen/kürzen, schreiben, fsync, schließen (0600)
	AppendSync(name string, data []byte) error    // öffnen, anhängen, fsync, schließen
	Rename(from, to string) error                 // ersetzt das Ziel in einem Schritt
	Link(from, to string) error                   // scheitert, wenn from fehlt oder to schon da ist
	Remove(name string) error                     // eine fehlende Datei ist kein Fehler
	SyncDir(dir string) error                     // Windows: nichts
	Stat(name string) (fs.FileInfo, error)
	SameFile(a, b string) bool
	MkdirAll(dir string) error // 0700
	// ReadDir nennt die Namen (ohne Pfad) im Ordner, sortiert – für die
	// Spuren früherer Nutzung (state.corrupt-* …), die der Start sucht.
	ReadDir(dir string) ([]string, error)
	// Lock nimmt eine ausschließende Sperre auf die Datei name (flock bzw.
	// LockFileEx), angelegt bei Bedarf. ErrLocked, wenn ein anderer sie
	// hält; jeder andere Fehler heißt: Sperre nicht anlegbar.
	Lock(name string) (release func() error, err error)
}

// ErrLocked: ein anderer Prozess hält server.lock.
var ErrLocked = errors.New("ein anderes fw-alarm arbeitet schon in diesem Datenordner")

// OSFS liefert die Dateischicht des Betriebssystems. Unter Windows werden
// Rename, Remove und Link bis zu 1 s wiederholt, wenn ein anderer (etwa ein
// Virenscanner) die Datei hält.
func OSFS() FS { return osFS{} }

type osFS struct{}

func (osFS) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }

func (osFS) WriteFileSync(name string, data []byte) error {
	return writeSync(name, data, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
}

func (osFS) AppendSync(name string, data []byte) error {
	return writeSync(name, data, os.O_WRONLY|os.O_CREATE|os.O_APPEND)
}

// writeSync öffnet, schreibt, sichert (fsync) und schließt; jeder Fehler
// zählt, auch der des Schließens.
func writeSync(name string, data []byte, flag int) error {
	f, err := os.OpenFile(name, flag, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Sync()
	}
	return errors.Join(werr, f.Close())
}

func (osFS) Rename(from, to string) error {
	return retryHeld(func() error { return os.Rename(from, to) })
}

func (osFS) Link(from, to string) error {
	return retryHeld(func() error { return os.Link(from, to) })
}

func (osFS) Remove(name string) error {
	err := retryHeld(func() error { return os.Remove(name) })
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (osFS) SyncDir(dir string) error { return syncDir(dir) }

func (osFS) Stat(name string) (fs.FileInfo, error) { return os.Stat(name) }

func (osFS) SameFile(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
}

func (osFS) MkdirAll(dir string) error { return os.MkdirAll(dir, 0o700) }

func (osFS) ReadDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, err
}

func (osFS) Lock(name string) (func() error, error) { return lockFile(name) }

// openLockFile öffnet server.lock zum Sperren: schreibend, bei Bedarf
// angelegt. Sind nur die Rechte zum Schreiben weg (Störung 10/2026: Rechte
// nach einem Update geändert), genügt lesend – flock und LockFileEx sperren
// auch dann, und ein zweiter Prozess bleibt ausgesperrt. Nur wenn sie sich
// weder öffnen noch anlegen lässt, ist die Sperre nicht zu haben.
func openLockFile(name string) (*os.File, error) {
	f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0o600)
	if errors.Is(err, fs.ErrPermission) {
		if ro, rerr := os.Open(name); rerr == nil {
			return ro, nil
		}
		// Auch lesend nicht (fehlt, oder keinerlei Rechte): es bleibt beim
		// ersten Fehler, er nennt den eigentlichen Grund.
	}
	return f, err
}
