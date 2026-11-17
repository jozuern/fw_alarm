package store

import (
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// retryHeld wiederholt nur, solange ein anderer die Datei hält – "nicht
// gefunden" kommt sofort zurück, und nach heldRetryFor ist Schluss.
func TestRetryOnlyWhileHeldWindows(t *testing.T) {
	failing := func(errno windows.Errno, times int) (func() error, *int) {
		n := 0
		return func() error {
			n++
			if times < 0 || n <= times {
				return &os.PathError{Op: "rename", Path: "x", Err: errno}
			}
			return nil
		}, &n
	}

	for _, errno := range []windows.Errno{windows.ERROR_SHARING_VIOLATION, windows.ERROR_ACCESS_DENIED, windows.ERROR_LOCK_VIOLATION} {
		op, n := failing(errno, 3)
		if err := retryHeld(op); err != nil || *n != 4 {
			t.Errorf("%v: %v nach %d Versuchen", errno, err, *n)
		}
	}

	for _, errno := range []windows.Errno{windows.ERROR_FILE_NOT_FOUND, windows.ERROR_PATH_NOT_FOUND, windows.ERROR_ALREADY_EXISTS} {
		op, n := failing(errno, -1)
		if err := retryHeld(op); err == nil || *n != 1 {
			t.Errorf("%v: %v nach %d Versuchen – darf nicht wiederholen", errno, err, *n)
		}
	}

	op, n := failing(windows.ERROR_SHARING_VIOLATION, -1)
	start := time.Now()
	err := retryHeld(op)
	took := time.Since(start)
	if err == nil || took < heldRetryFor || took > heldRetryFor+500*time.Millisecond || *n < 2 {
		t.Errorf("dauerhaft gehalten: %v nach %v und %d Versuchen", err, took, *n)
	}
}
