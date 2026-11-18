package mutes

import (
	"context"
	"errors"
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

// Prüfstand: eine Ablage im Speicher (FaultFS), eine gestellte Uhr und als
// Bark ein httptest-Server auf 127.0.0.1, der jede Anfrage festhält. Alle
// Keys sind erfunden; kein Test erreicht einen echten Bark-Server.

// t0: Dienstag, 17.11.2026, 10:00 UTC = 11:00 in Berlin.
var t0 = time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

const (
	statusKey = "statuskey01"
	annaKey   = "annakey0001"
	bertKey   = "bertkey0002"
	caraKey   = "carakey0003"
)

var dir = filepath.FromSlash("/daten")

type barkReq struct {
	Key    string
	Fields url.Values
}

type bench struct {
	t       *testing.T
	c       *core.Core
	fs      *storetest.FaultFS
	clk     *clock.Fake
	journal syncBuffer // das Journal des Core
	cfg     atomic.Pointer[config.Config]

	mu     sync.Mutex
	reqs   []barkReq
	onReq  func(key string) // läuft vor der Antwort (darf warten)
	answer int              // 0 = 200
}

func newBench(t *testing.T) *bench {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	b := &bench{t: t, fs: storetest.New()}
	srv := httptest.NewServer(http.HandlerFunc(b.serveBark))
	t.Cleanup(srv.Close)
	st, _, err := store.Open(dir, b.fs, clock.NewFake(t0), log)
	if err != nil {
		t.Fatal(err)
	}
	group := bg.New(log)
	t.Cleanup(func() {
		if !group.Wait(20 * time.Second) {
			t.Error("Arbeit nebenher endet nicht")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := st.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	b.cfg.Store(&config.Config{Timezone: "Europe/Berlin", BarkHost: srv.URL, BarkKeyStatus: statusKey,
		MuteMaxSeconds: 43200, OfflineAfterSeconds: 180, WatchdogOfflineAfterSeconds: 600})
	b.clk = clock.NewFake(t0)
	b.c = &core.Core{Cfg: b.cfg.Load, CfgOK: func() bool { return true }, Store: st,
		Bark: bark.New(b.cfg.Load, clock.Real(), log), Clock: b.clk, Bg: group,
		Log: slog.New(slog.NewTextHandler(&b.journal, nil)), Version: "test"}
	return b
}

// syncBuffer: ein Journal, das der Test lesen kann.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (w *syncBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *syncBuffer) lines(substr string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for _, l := range strings.Split(w.b.String(), "\n") {
		if strings.Contains(l, substr) {
			out = append(out, l)
		}
	}
	return out
}

func (b *bench) serveBark(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		b.t.Errorf("Bark-Attrappe: %v", err)
	}
	key := strings.TrimPrefix(r.URL.Path, "/")
	b.mu.Lock()
	b.reqs = append(b.reqs, barkReq{Key: key, Fields: r.PostForm})
	on, status := b.onReq, b.answer
	b.mu.Unlock()
	if on != nil {
		on(key)
	}
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
}

func (b *bench) take() []barkReq {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.reqs
	b.reqs = nil
	return out
}

func (b *bench) update(fn func(d *model.State)) {
	b.t.Helper()
	if err := b.c.Store.Update(context.Background(), func(d *model.State) error { fn(d); return nil }); err != nil {
		b.t.Fatal(err)
	}
}

// three: Anna stumm seit muted (Sekunden vor t0), Bert dauerhaft stumm seit
// langem, Cara laut.
func (b *bench) three(annaMutedAgo int64) {
	b.update(func(d *model.State) {
		d.NextID = 4
		d.KeysVersion = 7
		d.Recipients = []model.Recipient{
			{ID: 1, Key: annaKey, Label: "Anna Müller", Muted: true, MutedAt: t0.Unix() - annaMutedAgo, MuteVolume: 2, MuteToken: "annatoken01"},
			{ID: 2, Key: bertKey, Label: "Bert", Muted: true, MutedAt: 1, MutePermanent: true, MuteToken: "berttoken02"},
			{ID: 3, Key: caraKey, Label: "Cara", MuteToken: "caratoken03"},
		}
	})
}

// stateSaves: wie oft state.json bisher gesichert wurde (Schreiben der
// Zwischendatei).
func (b *bench) stateSaves() int {
	n := 0
	tmp := filepath.Join(dir, "state.json.tmp")
	for _, c := range b.fs.Log() {
		if c.Op == "WriteFileSync" && len(c.Names) > 0 && c.Names[0] == tmp {
			n++
		}
	}
	return n
}

// Gesichert wird nur, wenn der Schnappschuss eine überfällige Stummschaltung
// zeigt – sonst nichts (Expire läuft bei JEDEM Poll der Box).
func TestExpireOnlyWhenStale(t *testing.T) {
	b := newBench(t)
	ctx := context.Background()

	// Leere Liste, frisch Stumme, dauerhaft Stumme, keine Höchstdauer: kein Sichern.
	noSave := func(what string, times int) {
		t.Helper()
		saves := b.stateSaves()
		for range times {
			if err := Expire(ctx, b.c); err != nil {
				t.Fatalf("%s: %v", what, err)
			}
		}
		if got := b.stateSaves(); got != saves {
			t.Fatalf("%s: nichts überfällig, trotzdem %d-mal gesichert", what, got-saves)
		}
	}
	noSave("leere Liste", 1)
	b.three(43200) // genau die Höchstdauer: noch nicht überfällig
	noSave("frisch und dauerhaft stumm", 3)
	b.cfg.Store(func() *config.Config { c := *b.cfg.Load(); c.MuteMaxSeconds = 0; return &c }())
	b.update(func(d *model.State) { d.Recipients[0].MutedAt = 1 })
	noSave("Höchstdauer 0 = keine Rückschaltung", 1)

	// Überfällig (Höchstdauer 12 Std aus der Konfiguration): EINE Änderung –
	// Anna laut, Version einmal erhöht, ein Eintrag key_unmuted; Bert
	// (dauerhaft) und Cara bleiben, wie sie sind.
	b.cfg.Store(func() *config.Config { c := *b.cfg.Load(); c.MuteMaxSeconds = 43200; return &c }())
	b.update(func(d *model.State) { d.Recipients[0].MutedAt = t0.Unix() - 43201 })
	saves := b.stateSaves()
	if err := Expire(ctx, b.c); err != nil {
		t.Fatal(err)
	}
	if got := b.stateSaves(); got != saves+1 {
		t.Errorf("%d-mal gesichert, erwartet einmal", got-saves)
	}
	st := b.c.Store.State()
	if a := st.Recipients[0]; a.Muted || a.MutedAt != 0 || a.MuteVolume != 2 {
		t.Errorf("Anna: %+v", a)
	}
	if bert := st.Recipients[1]; !bert.Muted || !bert.MutePermanent {
		t.Errorf("Bert: %+v", bert)
	}
	if st.KeysVersion != 8 {
		t.Errorf("Version %d, erwartet 8", st.KeysVersion)
	}
	var unmuted []model.Entry
	for _, e := range st.Outbox {
		if e.Str("event") == model.EvKeyUnmuted {
			unmuted = append(unmuted, e)
		}
	}
	if len(unmuted) != 1 || unmuted[0].Str("by") != "automatisch" || unmuted[0].Str("source") != "auto" ||
		unmuted[0].Str("label") != "Anna Müller" || unmuted[0].Str("key") != "anna..." || unmuted[0].Int("version") != 8 {
		t.Errorf("Verlauf: %v", unmuted)
	}
	// Danach ist nichts mehr überfällig: kein weiteres Sichern.
	saves = b.stateSaves()
	if err := Expire(ctx, b.c); err != nil || b.stateSaves() != saves {
		t.Errorf("zweiter Aufruf: %v, %d-mal gesichert", err, b.stateSaves()-saves)
	}

	// Die Einstellung im Dashboard gilt vor der Konfiguration.
	b.update(func(d *model.State) {
		d.Settings.MuteMaxSeconds = 7200
		d.Recipients[0].Muted, d.Recipients[0].MutedAt = true, t0.Unix()-7201
	})
	if err := Expire(ctx, b.c); err != nil || b.c.Store.State().Recipients[0].Muted {
		t.Errorf("Höchstdauer aus dem Dashboard nicht beachtet: %v", err)
	}
}

// Die Box-Version zählt mit: Hat die Box zuletzt eine höhere Version
// gemeldet, liegt die neue Version darüber (PHP bump_keys_version).
func TestExpireBumpsAboveBoxVersion(t *testing.T) {
	b := newBench(t)
	b.three(43201)
	b.c.Store.UpdateRuntime(func(d *model.Runtime) {
		d.Box = &model.BoxStatus{SeenAt: t0.Unix(), Fields: model.Fields{{Name: "keys_version", Value: "41"}}}
	})
	if err := Expire(context.Background(), b.c); err != nil {
		t.Fatal(err)
	}
	if v := b.c.Store.State().KeysVersion; v != 42 {
		t.Errorf("Version %d, erwartet 42", v)
	}
}

// Die Meldungen – die leise Info an die Betroffenen, auf Wunsch die an den
// Admin (mute_expired) – gehen erst NACH dem Sichern hinaus, nebenher: Expire
// wartet nie auf Bark. Scheitert das Sichern, geht nichts hinaus.
func TestExpireMessagesAfterUpdate(t *testing.T) {
	b := newBench(t)
	b.three(43201)
	b.update(func(d *model.State) {
		d.Recipients[2].Muted, d.Recipients[2].MutedAt = true, t0.Unix()-50000 // Cara ebenfalls überfällig
	})

	// Sichern scheitert: kein Versand, nichts geändert, der Fehler kommt zurück.
	b.fs.Fail(filepath.Join(dir, "state.json.tmp"), errors.New("Platte voll"))
	if err := Expire(context.Background(), b.c); !errors.Is(err, store.ErrStorage) {
		t.Fatalf("Fehler %v, erwartet ErrStorage", err)
	}
	b.c.Bg.Wait(10 * time.Second)
	if reqs := b.take(); len(reqs) != 0 || !b.c.Store.State().Recipients[0].Muted {
		t.Fatalf("trotz Fehler gesendet oder geändert: %+v", reqs)
	}
	b.fs.Fail(filepath.Join(dir, "state.json.tmp"), nil)

	// Bark hält jede Anfrage fest, bis der Test sie freigibt; dabei muss der
	// Zustand schon gesichert sein.
	release := make(chan struct{})
	var savedFirst atomic.Bool
	savedFirst.Store(true)
	b.mu.Lock()
	b.onReq = func(key string) {
		if key != statusKey && b.c.Store.State().Recipients[0].Muted {
			savedFirst.Store(false)
		}
		<-release
	}
	b.mu.Unlock()
	start := time.Now()
	if err := Expire(context.Background(), b.c); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("Expire wartete auf Bark: %v", d)
	}
	close(release)
	if !b.c.Bg.Wait(15 * time.Second) {
		t.Fatal("Versand endet nicht")
	}
	if !savedFirst.Load() {
		t.Error("Info ging hinaus, bevor die Rückschaltung gesichert war")
	}
	reqs := b.take()
	byKey := map[string]barkReq{}
	for _, r := range reqs {
		byKey[r.Key] = r
	}
	if len(reqs) != 2 || byKey[annaKey].Key == "" || byKey[caraKey].Key == "" {
		t.Fatalf("Anfragen: %+v", reqs)
	}
	wantBody := "[17.11.2026 11:00] Stummschaltung automatisch beendet (war laenger als 12 Std aktiv). " +
		"Alarme kommen wieder als Critical Alert mit Ton."
	for _, r := range reqs {
		if r.Fields.Get("title") != "Alarmton wieder LAUT" || r.Fields.Get("level") != "passive" ||
			r.Fields.Get("body") != wantBody || len(r.Fields) != 3 {
			t.Errorf("Info an %s: %v", r.Key, r.Fields)
		}
	}

	// Mit eingeschalteter Art mute_expired auch der Admin – ASCII, die Namen.
	b.mu.Lock()
	b.onReq = nil
	b.mu.Unlock()
	b.update(func(d *model.State) {
		d.Notify.Types = map[string]map[string]string{"mute_expired": {"enabled": "1"}}
		d.Recipients[0].Muted, d.Recipients[0].MutedAt = true, 5
		d.Recipients[2].Muted, d.Recipients[2].MutedAt = true, 5
	})
	if err := Expire(context.Background(), b.c); err != nil {
		t.Fatal(err)
	}
	b.c.Bg.Wait(15 * time.Second)
	var admin []barkReq
	for _, r := range b.take() {
		if r.Key == statusKey {
			admin = append(admin, r)
		}
	}
	if len(admin) != 1 || admin[0].Fields.Get("title") != "Stummschaltung abgelaufen" ||
		admin[0].Fields.Get("body") != "[17.11.2026 11:00] Automatisch wieder laut geschaltet (Zeitlimit erreicht): Anna Mueller, Cara." {
		t.Errorf("Meldung an den Admin: %+v", admin)
	}
}

// Das Sichern der Rückschaltung landet erst nach der Frist (ErrLate): Expire
// meldet das wie bisher seinem Aufrufer – und gilt die Rückschaltung danach,
// gehen die Info "Alarmton wieder LAUT" und (eingeschaltet) mute_expired
// trotzdem hinaus, genau einmal, nach dem Sichern.
func TestExpireLateSaveStillTells(t *testing.T) {
	b := newBench(t)
	b.three(43201)
	b.update(func(d *model.State) { d.Notify.Types = map[string]map[string]string{"mute_expired": {"enabled": "1"}} })
	release := b.fs.Hang(filepath.Join(dir, "state.json.tmp"))
	t.Cleanup(release)
	var savedFirst atomic.Bool
	savedFirst.Store(true)
	b.mu.Lock()
	b.onReq = func(string) {
		if b.c.Store.State().Recipients[0].Muted {
			savedFirst.Store(false)
		}
	}
	b.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := Expire(ctx, b.c); !errors.Is(err, store.ErrLate) {
		t.Fatalf("Fehler %v, erwartet ErrLate", err)
	}
	time.Sleep(100 * time.Millisecond)
	if reqs := b.take(); len(reqs) != 0 {
		t.Fatalf("vor dem Sichern gesendet: %+v", reqs)
	}
	release()
	var reqs []barkReq
	deadline := time.Now().Add(15 * time.Second)
	for len(reqs) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		reqs = append(reqs, b.take()...)
	}
	if !b.c.Bg.Wait(15 * time.Second) {
		t.Fatal("Versand endet nicht")
	}
	reqs = append(reqs, b.take()...)
	byKey := map[string]int{}
	for _, r := range reqs {
		byKey[r.Key]++
	}
	if len(reqs) != 2 || byKey[annaKey] != 1 || byKey[statusKey] != 1 {
		t.Fatalf("Anfragen: %+v", reqs)
	}
	if !savedFirst.Load() {
		t.Error("Meldung ging hinaus, bevor die Rückschaltung galt")
	}
	if st := b.c.Store.State(); st.Recipients[0].Muted || st.KeysVersion != 8 {
		t.Errorf("Rückschaltung gilt nicht: %+v", st.Recipients[0])
	}
}

// Kaputte Platte und eine überfällige Stummschaltung: Expire läuft bei jedem
// Poll – die Warnung steht höchstens einmal pro Minute im Journal (Uhr des
// Core), die nächste nennt, wie viele dazwischen unterdrückt wurden.
func TestExpireWarningAtMostOncePerMinute(t *testing.T) {
	b := newBench(t)
	b.three(43201)
	b.fs.Fail(filepath.Join(dir, "state.json.tmp"), errors.New("Platte voll"))
	const warn = "überfällige Stummschaltung nicht gesichert"
	expire := func(times int) {
		t.Helper()
		for range times {
			if err := Expire(context.Background(), b.c); !errors.Is(err, store.ErrStorage) {
				t.Fatalf("Fehler %v", err)
			}
		}
	}
	expire(5)
	if got := b.journal.lines(warn); len(got) != 1 || !strings.Contains(got[0], "unterdrueckt=0") {
		t.Fatalf("nach 5 Aufrufen: %q", got)
	}
	b.clk.Advance(59 * time.Second)
	expire(1)
	if got := b.journal.lines(warn); len(got) != 1 {
		t.Fatalf("nach 59 s: %q", got)
	}
	b.clk.Advance(time.Second)
	expire(1)
	got := b.journal.lines(warn)
	if len(got) != 2 || !strings.Contains(got[1], "unterdrueckt=5") {
		t.Fatalf("nach 60 s: %q", got)
	}
	b.clk.Advance(time.Minute)
	expire(1)
	if got := b.journal.lines(warn); len(got) != 3 || !strings.Contains(got[2], "unterdrueckt=0") {
		t.Fatalf("nach weiteren 60 s: %q", got)
	}
}

// Zwei gescheiterte Aufrufe, die in umgekehrter Reihenfolge fertig werden:
// A beginnt und hängt am Sichern, 30 s später scheitert B (die Frist lief ab,
// bevor B an der Reihe war) und schreibt die Warnung; dann gibt A auf
// (ErrLate). Die Bremse misst mit der Uhr beim Schreiben, nicht mit der vom
// Beginn – sonst sähe A wie eine zurückgesprungene Uhr aus und käme durch.
// A sagt "nicht rechtzeitig gesichert" (das Sichern kann noch landen), B
// "nicht gesichert".
func TestExpireWarningClockAtLogTime(t *testing.T) {
	b := newBench(t)
	b.three(43201)
	release := b.fs.Hang(filepath.Join(dir, "state.json.tmp"))
	t.Cleanup(release)
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	saves := b.stateSaves()
	errA := make(chan error, 1)
	go func() { errA <- Expire(ctxA, b.c) }()
	deadline := time.Now().Add(10 * time.Second)
	for b.stateSaves() == saves { // FaultFS hält den Aufruf fest, bevor er hängt
		if time.Now().After(deadline) {
			t.Fatal("A hängt nie am Sichern")
		}
		time.Sleep(time.Millisecond)
	}
	b.clk.Advance(30 * time.Second)
	ctxB, cancelB := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelB()
	if err := Expire(ctxB, b.c); !errors.Is(err, store.ErrDropped) {
		t.Fatalf("B: %v, erwartet ErrDropped", err)
	}
	cancelA()
	select {
	case err := <-errA:
		if !errors.Is(err, store.ErrLate) {
			t.Fatalf("A: %v, erwartet ErrLate", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("A kehrt nicht zurück")
	}
	got := b.journal.lines("überfällige Stummschaltung nicht")
	if len(got) != 1 || !strings.Contains(got[0], "überfällige Stummschaltung nicht gesichert") {
		t.Fatalf("Warnungen: %q", got)
	}
	// Eine Minute nach B darf die nächste durch – A meldet sich als
	// verspätet, mit der einen unterdrückten Zeile.
	release()
	b.three(43201) // wieder überfällig (das späte Sichern hat A erledigt)
	b.fs.Fail(filepath.Join(dir, "state.json.tmp"), store.ErrStorage)
	b.clk.Advance(time.Minute)
	if err := Expire(context.Background(), b.c); !errors.Is(err, store.ErrStorage) {
		t.Fatalf("C: %v", err)
	}
	b.fs.Heal() // Close am Ende sichert noch einmal
	got = b.journal.lines("überfällige Stummschaltung nicht")
	if len(got) != 2 || !strings.Contains(got[1], "unterdrueckt=1") {
		t.Fatalf("nach einer Minute: %q", got)
	}
}

// Die Zeile bei ErrLate sagt, dass die Rückschaltung noch gelten kann.
func TestExpireWarningLateWording(t *testing.T) {
	b := newBench(t)
	b.three(43201)
	release := b.fs.Hang(filepath.Join(dir, "state.json.tmp"))
	t.Cleanup(release)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := Expire(ctx, b.c); !errors.Is(err, store.ErrLate) {
		t.Fatalf("Fehler %v, erwartet ErrLate", err)
	}
	got := b.journal.lines("überfällige Stummschaltung")
	if len(got) != 1 || !strings.Contains(got[0], "überfällige Stummschaltung nicht rechtzeitig gesichert") {
		t.Fatalf("Warnung: %q", got)
	}
}
