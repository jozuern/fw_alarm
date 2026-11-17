package bg

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer: Puffer für das Journal, den mehrere Goroutinen beschreiben dürfen.
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

func TestGroupRecoversPanic(t *testing.T) {
	var out syncBuffer
	g := New(slog.New(slog.NewTextHandler(&out, nil)))

	// Eine Zeichenkette als Laufzeitfehler kann Anfragedaten tragen – sie
	// darf nicht ins Journal.
	g.Go("versand", func() { panic("geheim-aus-der-anfrage") })
	// Ein Laufzeitfehler der Sprache selbst (hier: Index außerhalb).
	g.Go("auswertung", func() {
		var list []int
		_ = list[3]
	})
	if !g.Wait(5 * time.Second) {
		t.Fatal("Wait: Arbeit nach einem Laufzeitfehler nicht beendet")
	}

	// Danach arbeitet die Gruppe weiter.
	ran := make(chan struct{})
	g.Go("danach", func() { close(ran) })
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("nach einem Laufzeitfehler läuft keine Arbeit mehr")
	}
	if !g.Wait(5 * time.Second) {
		t.Fatal("Wait nach weiterer Arbeit nicht fertig")
	}

	log := out.String()
	for _, want := range []string{"versand", "auswertung", "index out of range"} {
		if !strings.Contains(log, want) {
			t.Errorf("Journal nennt %q nicht:\n%s", want, log)
		}
	}
	if strings.Contains(log, "geheim-aus-der-anfrage") {
		t.Errorf("Journal enthält den Wert des Laufzeitfehlers:\n%s", log)
	}
}

func TestGroupWait(t *testing.T) {
	g := New(nil)
	if !g.Wait(0) {
		t.Fatal("Wait ohne Arbeit muss sofort true liefern")
	}

	release := make(chan struct{})
	started := make(chan struct{})
	g.Go("haengt", func() {
		close(started)
		<-release
	})
	<-started
	begin := time.Now()
	if g.Wait(50 * time.Millisecond) {
		t.Fatal("Wait meldet fertig, obwohl noch gearbeitet wird")
	}
	if waited := time.Since(begin); waited < 40*time.Millisecond {
		t.Fatalf("Wait gab nach %v auf, erwartet etwa 50 ms", waited)
	}

	close(release)
	if !g.Wait(5 * time.Second) {
		t.Fatal("Wait meldet nicht fertig, obwohl die Arbeit endete")
	}

	// Viele gleichzeitige Aufgaben, auch während jemand wartet.
	var mu sync.Mutex
	count := 0
	for range 50 {
		g.Go("viele", func() {
			time.Sleep(time.Millisecond)
			mu.Lock()
			count++
			mu.Unlock()
		})
	}
	if !g.Wait(5 * time.Second) {
		t.Fatal("Wait: 50 Aufgaben nicht fertig")
	}
	mu.Lock()
	defer mu.Unlock()
	if count != 50 {
		t.Fatalf("%d von 50 Aufgaben gelaufen", count)
	}
}
