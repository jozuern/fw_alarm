package store

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// syncDir: Windows kennt kein fsync auf einen Ordner – nichts zu tun.
func syncDir(string) error { return nil }

// lockFile nimmt LockFileEx (ausschließend, ohne Warten) auf name. Die
// Sperre gehört dem Handle: Ein zweites Öffnen im selben Prozess wird
// ebenso abgewiesen wie ein anderer Prozess; das Ende des Prozesses gibt
// sie frei.
func lockFile(name string) (func() error, error) {
	f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	h := windows.Handle(f.Fd())
	ol := new(windows.Overlapped)
	const all = ^uint32(0) // der ganze Bereich, wie cmd/go lockedfile
	err = windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, all, all, ol)
	if err != nil {
		// Schließen nach gescheiterter Sperre: dessen Fehler ändert nichts.
		_ = f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, ErrLocked
		}
		return nil, &os.PathError{Op: "LockFileEx", Path: name, Err: err}
	}
	return func() error {
		return errors.Join(windows.UnlockFileEx(h, 0, all, all, ol), f.Close())
	}, nil
}

// heldRetryFor und heldRetryStep: wie lange und in welchen Schritten
// retryHeld es erneut versucht.
const (
	heldRetryFor  = time.Second
	heldRetryStep = 20 * time.Millisecond
)

// retryHeld wiederholt op bis zu heldRetryFor, solange Windows meldet, dass
// ein anderer die Datei hält (Freigabe- oder Sperrverletzung, Zugriff
// verweigert) – etwa ein Virenscanner, der sie gerade liest. Andere Fehler
// (z.B. "nicht gefunden") kommen sofort zurück.
func retryHeld(op func() error) error {
	deadline := time.Now().Add(heldRetryFor)
	for {
		err := op()
		if err == nil || !heldByOther(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(heldRetryStep)
	}
}

// heldByOther: Fehler der Art "ein anderer hält die Datei".
func heldByOther(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
