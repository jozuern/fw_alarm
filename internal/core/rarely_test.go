package core

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"fwalarm/internal/clock"
)

// journal fängt die Zeilen des Journals auf.
type journal struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (j *journal) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.buf.Write(p)
}

func (j *journal) lines() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return strings.Split(strings.TrimSuffix(j.buf.String(), "\n"), "\n")
}

// WarnRarely: höchstens eine Zeile je Art und Minute (auf der Uhr des Core),
// die nächste nennt, wie viele dazwischen unterdrückt wurden; jede Art und
// jeder Core für sich; springt die Uhr zurück, gilt die Minute als um.
func TestWarnRarely(t *testing.T) {
	j := &journal{}
	clk := clock.NewFake(time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC))
	c := &Core{Clock: clk, Log: slog.New(slog.NewTextHandler(j, nil))}
	other := &Core{Clock: clk, Log: c.Log}
	for range 5 {
		c.WarnRarely("poll", "Befehl nicht gesichert", "befehl", 1)
	}
	c.WarnRarely("ack", "Quittung nicht gesichert")
	other.WarnRarely("poll", "anderer Core")
	clk.Advance(59 * time.Second)
	c.WarnRarely("poll", "Befehl nicht gesichert")
	clk.Advance(time.Second)
	c.WarnRarely("poll", "Befehl nicht gesichert", "befehl", 2)
	clk.Set(clk.Now().Add(-10 * time.Second))
	c.WarnRarely("poll", "zurückgesprungen")
	want := []string{
		`msg="Befehl nicht gesichert" befehl=1 unterdrueckt=0`,
		`msg="Quittung nicht gesichert" unterdrueckt=0`,
		`msg="anderer Core" unterdrueckt=0`,
		`msg="Befehl nicht gesichert" befehl=2 unterdrueckt=5`,
		`msg=zurückgesprungen unterdrueckt=0`,
	}
	got := j.lines()
	if len(got) != len(want) {
		t.Fatalf("%d Zeilen:\n%s", len(got), strings.Join(got, "\n"))
	}
	for i, w := range want {
		if !strings.Contains(got[i], "level=WARN") || !strings.HasSuffix(got[i], w) {
			t.Errorf("Zeile %d: %s\nerwartet …%s", i, got[i], w)
		}
	}
}
