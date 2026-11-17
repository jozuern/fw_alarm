package clock

import (
	"testing"
	"time"
)

var start = time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

// fired meldet, ob auf dem Kanal schon ein Wert liegt (ohne zu warten).
func fired(ch <-chan time.Time) (time.Time, bool) {
	select {
	case t := <-ch:
		return t, true
	default:
		return time.Time{}, false
	}
}

func TestFakeClock(t *testing.T) {
	f := NewFake(start)
	if got := f.Now(); !got.Equal(start) {
		t.Fatalf("Now() = %v, erwartet %v", got, start)
	}

	ch := f.After(5 * time.Second)
	if f.Pending() != 1 {
		t.Fatalf("Pending() = %d, erwartet 1", f.Pending())
	}
	f.Advance(4 * time.Second)
	if _, ok := fired(ch); ok {
		t.Fatal("Zeitgeber nach 4 s schon ausgelöst")
	}
	f.Advance(time.Second)
	got, ok := fired(ch)
	if !ok {
		t.Fatal("Zeitgeber nach 5 s nicht ausgelöst")
	}
	if want := start.Add(5 * time.Second); !got.Equal(want) {
		t.Fatalf("Zeitgeber lieferte %v, erwartet %v", got, want)
	}
	if f.Pending() != 0 {
		t.Fatalf("Pending() = %d nach dem Auslösen, erwartet 0", f.Pending())
	}

	// Ein Sprung der Wanduhr ändert Now(), lässt aber Zeitgeber unberührt:
	// Sie zählen vergangene Zeit, nicht Uhrzeiten.
	ch = f.After(time.Minute)
	jump := start.Add(-2 * time.Hour)
	f.Set(jump)
	if got := f.Now(); !got.Equal(jump) {
		t.Fatalf("Now() nach Set = %v, erwartet %v", got, jump)
	}
	if _, ok := fired(ch); ok {
		t.Fatal("Set hat einen Zeitgeber ausgelöst")
	}
	f.Set(start.Add(24 * time.Hour))
	if _, ok := fired(ch); ok {
		t.Fatal("Sprung nach vorn hat einen Zeitgeber ausgelöst")
	}
	f.Advance(59 * time.Second)
	if _, ok := fired(ch); ok {
		t.Fatal("Zeitgeber nach 59 s schon ausgelöst")
	}
	f.Advance(time.Second)
	if _, ok := fired(ch); !ok {
		t.Fatal("Zeitgeber nach 60 s nicht ausgelöst")
	}

	// Mehrere Zeitgeber in einem Schritt, in der Reihenfolge ihrer Fälligkeit.
	a := f.After(3 * time.Second)
	b := f.After(time.Second)
	f.Advance(10 * time.Second)
	ta, okA := fired(a)
	tb, okB := fired(b)
	if !okA || !okB {
		t.Fatal("nicht alle fälligen Zeitgeber ausgelöst")
	}
	if !tb.Before(ta) {
		t.Fatalf("Zeitpunkte %v (1 s) und %v (3 s) in falscher Reihenfolge", tb, ta)
	}

	// After(0) feuert sofort, ohne Advance.
	if _, ok := fired(f.After(0)); !ok {
		t.Fatal("After(0) nicht sofort ausgelöst")
	}

	// Sleep blockiert, bis die Zeit weitergestellt wird.
	done := make(chan struct{})
	go func() {
		f.Sleep(2 * time.Second)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for f.Pending() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("Sleep hat keinen Zeitgeber angelegt")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-done:
		t.Fatal("Sleep kehrte vor Advance zurück")
	case <-time.After(20 * time.Millisecond):
	}
	f.Advance(2 * time.Second)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Sleep kehrte nach Advance nicht zurück")
	}
	f.Sleep(0) // darf nicht blockieren
}

func TestRealClock(t *testing.T) {
	c := Real()
	before := time.Now()
	if got := c.Now(); got.Before(before) {
		t.Fatalf("Real().Now() = %v liegt vor %v", got, before)
	}
	select {
	case <-c.After(time.Millisecond):
	case <-time.After(5 * time.Second):
		t.Fatal("Real().After feuerte nicht")
	}
	c.Sleep(time.Millisecond)
}
