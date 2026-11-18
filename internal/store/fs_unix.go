//go:build unix

package store

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// syncDir sichert den Ordner selbst (fsync), damit ein Umbenennen einen
// Stromausfall übersteht.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// lockFile nimmt flock(LOCK_EX|LOCK_NB) auf name. Die Sperre gehört der
// geöffneten Datei: Schließen gibt sie frei, das Ende des Prozesses auch.
// Zwei Öffnungen im selben Prozess sperren einander ebenfalls aus.
func lockFile(name string) (func() error, error) {
	f, err := openLockFile(name)
	if err != nil {
		return nil, err
	}
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err != nil {
		// Schließen nach gescheiterter Sperre: dessen Fehler ändert nichts.
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, &os.PathError{Op: "flock", Path: name, Err: err}
	}
	return f.Close, nil
}

// retryHeld: Unter Unix hält kein Leser eine Datei fest – einfach ausführen.
func retryHeld(op func() error) error { return op() }
