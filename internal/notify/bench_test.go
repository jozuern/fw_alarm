package notify

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "time/tzdata" // Zeitzonen unabhängig vom Rechner, auf dem der Test läuft

	"fwalarm/internal/bark"
	"fwalarm/internal/bg"
	"fwalarm/internal/clock"
	"fwalarm/internal/config"
	"fwalarm/internal/core"
	"fwalarm/internal/model"
	"fwalarm/internal/store"
	"fwalarm/internal/store/storetest"
)

// Prüfstand: eine Ablage im Speicher, eine gestellte Uhr, eine austauschbare
// Konfiguration und als Bark ein httptest-Server auf 127.0.0.1, der jede
// Anfrage festhält. Kein Test erreicht je einen echten Bark-Server; alle Keys
// sind erfunden.

// t0: Dienstag, 17.11.2026, 10:00 UTC = 11:00 in Berlin.
var t0 = time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

const (
	statusKey = "statuskey01"
	annaKey   = "annakey0001"
	jensKey   = "jenskey0002"
)

type barkReq struct {
	Key    string
	Fields url.Values
}

type bench struct {
	t   *testing.T
	c   *core.Core
	clk *clock.Fake
	cfg atomic.Pointer[config.Config]

	mu     sync.Mutex
	reqs   []barkReq
	answer func(key string) int // Status je Key; 0 = Verbindung ohne Antwort schließen
}

func newBench(t *testing.T) *bench {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	b := &bench{t: t, clk: clock.NewFake(t0)}
	srv := httptest.NewServer(http.HandlerFunc(b.serveBark))
	t.Cleanup(srv.Close)
	st, _, err := store.Open(filepath.FromSlash("/daten"), storetest.New(), clock.NewFake(t0), log)
	if err != nil {
		t.Fatal(err)
	}
	group := bg.New(log)
	t.Cleanup(func() {
		if !group.Wait(10 * time.Second) {
			t.Error("Arbeit nebenher endet nicht")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := st.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	b.cfg.Store(&config.Config{
		Timezone: "Europe/Berlin", BarkHost: srv.URL, BarkKeyStatus: statusKey,
		OfflineAfterSeconds: 180, WatchdogOfflineAfterSeconds: 600, DemoReminderAfterSeconds: 14400,
	})
	// Der Bark-Client mit echter Uhr: Zwischen zwei Runden wartet er 500 ms
	// auf seiner Uhr – die gestellte liefe nie weiter.
	b.c = &core.Core{Cfg: b.cfg.Load, CfgOK: func() bool { return true }, Store: st,
		Bark: bark.New(b.cfg.Load, clock.Real(), log), Clock: b.clk, Bg: group, Log: log, Version: "test"}
	return b
}

func (b *bench) serveBark(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		b.t.Errorf("Bark-Attrappe: %v", err)
	}
	key := strings.TrimPrefix(r.URL.Path, "/")
	b.mu.Lock()
	b.reqs = append(b.reqs, barkReq{Key: key, Fields: r.PostForm})
	answer := b.answer
	b.mu.Unlock()
	status := http.StatusOK
	if answer != nil {
		status = answer(key)
	}
	if status == 0 {
		// Keine Antwort: Verbindung zu.
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close() // es soll ja gerade nichts ankommen
				return
			}
		}
		status = http.StatusBadGateway
	}
	w.WriteHeader(status)
}

// setAnswer legt fest, wie die Attrappe je Key antwortet.
func (b *bench) setAnswer(fn func(key string) int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.answer = fn
}

// take liefert die bisherigen Anfragen an Bark und vergisst sie.
func (b *bench) take() []barkReq {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.reqs
	b.reqs = nil
	return out
}

// setCfg ändert eine Kopie der geltenden Konfiguration und setzt sie in Kraft.
func (b *bench) setCfg(change func(c *config.Config)) {
	next := *b.cfg.Load()
	change(&next)
	b.cfg.Store(&next)
}

// update ändert den Zustand (muss gelingen).
func (b *bench) update(fn func(d *model.State)) {
	b.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.c.Store.Update(ctx, func(d *model.State) error { fn(d); return nil }); err != nil {
		b.t.Fatal(err)
	}
}

// stored setzt ausdrücklich gespeicherte Einstellungen einer Art (oder
// "global").
func (b *bench) stored(kind string, values map[string]string) {
	b.t.Helper()
	b.update(func(d *model.State) {
		if kind == "global" {
			for k, v := range values {
				d.Notify.Global[k] = v
			}
			return
		}
		row := d.Notify.Types[kind]
		if row == nil {
			row = map[string]string{}
			d.Notify.Types[kind] = row
		}
		for k, v := range values {
			row[k] = v
		}
	})
}

// recipients setzt die Empfängerliste: Anna (id 3) und Jens (id 5).
func (b *bench) recipients() {
	b.t.Helper()
	b.update(func(d *model.State) {
		d.NextID = 6
		d.Recipients = []model.Recipient{
			{ID: 3, Key: annaKey, Label: "Anna Müller", MuteToken: "annatoken03"},
			{ID: 5, Key: jensKey, Label: "Jens", MuteToken: "jenstoken05"},
		}
	})
}

func (b *bench) send(kind string, force bool) (string, Used) {
	b.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return Send(ctx, b.c, kind, "Titel", "Text", force)
}

func keysOf(reqs []barkReq) []string {
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, r.Key)
	}
	return out
}
