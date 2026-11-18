package web

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Fristen für feste Änderungen je Anfrage (Spec §6.2): Box 1,5 s,
// Kurzbefehl-Link 2 s, Oberfläche 5 s – unabhängig davon, ob der Aufrufer
// die Verbindung abbricht (ein abgebrochener Poll darf "ausgeliefert" nicht
// mitten im Sichern verwerfen).
func TestBudget(t *testing.T) {
	for kind, want := range map[BudgetKind]time.Duration{
		BudgetBox:  1500 * time.Millisecond,
		BudgetLink: 2 * time.Second,
		BudgetUI:   5 * time.Second,
	} {
		start := time.Now()
		ctx, cancel := Budget(kind)
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatalf("Art %d: keine Frist", kind)
		}
		if d := deadline.Sub(start); d < want-50*time.Millisecond || d > want+50*time.Millisecond {
			t.Errorf("Art %d: Frist %v, erwartet %v", kind, d, want)
		}
		if ctx.Err() != nil {
			t.Errorf("Art %d: schon abgelaufen", kind)
		}
		cancel()
		if ctx.Err() != context.Canceled {
			t.Errorf("Art %d: cancel wirkt nicht", kind)
		}
	}

	// Läuft wirklich ab.
	ctx, cancel := Budget(BudgetBox)
	defer cancel()
	select {
	case <-ctx.Done():
		if ctx.Err() != context.DeadlineExceeded {
			t.Errorf("Ende: %v", ctx.Err())
		}
	case <-time.After(5 * time.Second):
		t.Error("die Frist der Box läuft nicht ab")
	}

	// Eine andere Art ist ein Fehler im Programm.
	for _, kind := range []BudgetKind{0, BudgetUI + 1, -1} {
		func() {
			defer func() {
				if v := recover(); v == nil || !strings.Contains(fmt.Sprint(v), "Frist") {
					t.Errorf("Art %d: kein klarer Abbruch: %v", kind, v)
				}
			}()
			Budget(kind)
		}()
	}
}
