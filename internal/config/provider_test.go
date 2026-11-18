package config

import (
	"bytes"
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fwalarm/internal/clock"
)

// Ein Passwort-Hash, der in keinem Fehlertext und keinem Hinweis stehen darf.
const secretHash = "$2y$10$geheimgeheimgeheimgeheimgeheimgeheimgeheimgeheimgeheim"

func quietLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// confDir ist der Ordner eines Tests: config.toml, die Kopie, der Datenordner.
// Jede geschriebene Fassung bekommt eine eigene Änderungszeit – so erkennt
// der Prüfer sie sicher als neu, auch wenn die Uhr des Dateisystems grob ist.
type confDir struct {
	t    *testing.T
	dir  string
	mods int
}

func newConfDir(t *testing.T) *confDir { return &confDir{t: t, dir: t.TempDir()} }

func (d *confDir) path(name string) string { return filepath.Join(d.dir, name) }

func (d *confDir) config() string   { return d.path("config.toml") }
func (d *confDir) lastGood() string { return d.path("kopie.toml") }

// write legt name mit data an (ersetzt den Inhalt an Ort und Stelle, wie ein
// einfacher Editor).
func (d *confDir) write(name string, data []byte) {
	d.t.Helper()
	if err := os.WriteFile(d.path(name), data, 0o600); err != nil {
		d.t.Fatal(err)
	}
	d.touch(name)
}

func (d *confDir) touch(name string) {
	d.t.Helper()
	d.mods++
	mt := time.Date(2026, 1, 1, 0, 0, d.mods, 0, time.UTC)
	if err := os.Chtimes(d.path(name), mt, mt); err != nil {
		d.t.Fatal(err)
	}
}

func (d *confDir) read(name string) []byte {
	d.t.Helper()
	data, err := os.ReadFile(d.path(name))
	if err != nil {
		d.t.Fatal(err)
	}
	return data
}

// changes merkt sich jeden Aufruf von OnChange.
type changes struct {
	mu  sync.Mutex
	got []bool
}

func (c *changes) add(ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, ok)
}

func (c *changes) list() []bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.got)
}

func (c *changes) want(t *testing.T, want ...bool) {
	t.Helper()
	if got := c.list(); !slices.Equal(got, want) {
		t.Fatalf("OnChange-Aufrufe %v, erwartet %v", got, want)
	}
}

// startProvider öffnet den Provider mit gestellter Uhr und lässt Run laufen
// bis zum Ende des Tests.
func startProvider(t *testing.T, d *confDir) (*Provider, *clock.Fake, *changes) {
	t.Helper()
	clk := clock.NewFake(loadMoment(t))
	p, err := NewProvider(d.config(), d.lastGood(), clk, quietLog())
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	ch := &changes{}
	p.OnChange(ch.add)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return p, clk, ch
}

// waitPending wartet (höchstens 5 s), bis n Zeitgeber der Uhr warten – Run
// stellt seinen nächsten erst, wenn die Prüfung davor ganz fertig ist.
func waitPending(t *testing.T, clk *clock.Fake, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for clk.Pending() != n {
		if time.Now().After(deadline) {
			t.Fatalf("Zeitgeber: %d, erwartet %d", clk.Pending(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// tick lässt genau eine Prüfung laufen und wartet, bis sie fertig ist.
func tick(t *testing.T, clk *clock.Fake) {
	t.Helper()
	waitPending(t, clk, 1)
	clk.Advance(checkEvery)
	waitPending(t, clk, 1)
}

func noSecret(t *testing.T, where, text string) {
	t.Helper()
	if strings.Contains(text, "geheim") || strings.Contains(text, secretHash) {
		t.Errorf("%s enthält ein Geheimnis: %q", where, text)
	}
}

func TestProviderReloadsOnChange(t *testing.T) {
	d := newConfDir(t)
	d.write("config.toml", tomlFile(nil))
	p, clk, ch := startProvider(t, d)
	if p.Get().OfflineAfterSeconds != 180 || !p.OK() || p.Problem() != "" {
		t.Fatalf("Start: offline=%d ok=%v problem=%q", p.Get().OfflineAfterSeconds, p.OK(), p.Problem())
	}

	// Neue Fassung: der Prüfer nimmt sie bei der nächsten Prüfung.
	d.write("config.toml", tomlFile(map[string]string{"offline_after_seconds": "240"}))
	tick(t, clk)
	if got := p.Get().OfflineAfterSeconds; got != 240 || !p.OK() {
		t.Fatalf("nach der Änderung: offline=%d ok=%v", got, p.OK())
	}
	ch.want(t, true)

	// Unverändert: nichts geschieht.
	tick(t, clk)
	ch.want(t, true)

	// Nur angefasst, Inhalt gleich: keine neue Fassung.
	d.touch("config.toml")
	tick(t, clk)
	ch.want(t, true)

	// Gleiche Größe, gleiche Änderungszeit, andere Datei (in einem Schritt
	// ersetzt): die Dateinummer verrät sie.
	old, err := os.Stat(d.config())
	if err != nil {
		t.Fatal(err)
	}
	same := tomlFile(map[string]string{"offline_after_seconds": "300"}) // gleich lang wie "240"
	if len(same) != int(old.Size()) {
		t.Fatalf("Testdatei: Länge %d statt %d", len(same), old.Size())
	}
	if err := os.WriteFile(d.path("neu.toml"), same, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(d.path("neu.toml"), old.ModTime(), old.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(d.path("neu.toml"), d.config()); err != nil {
		t.Fatal(err)
	}
	tick(t, clk)
	if got := p.Get().OfflineAfterSeconds; got != 300 {
		t.Fatalf("in einem Schritt ersetzte Datei nicht erkannt: offline=%d", got)
	}
	ch.want(t, true, true)

	// Reload: sofort, ohne auf den Prüfer zu warten.
	d.write("config.toml", tomlFile(map[string]string{"offline_after_seconds": "360"}))
	if err := p.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := p.Get().OfflineAfterSeconds; got != 360 {
		t.Fatalf("nach Reload: offline=%d", got)
	}
	ch.want(t, true, true, true)
	// Dieselbe Fassung noch einmal: keine neue.
	if err := p.Reload(); err != nil {
		t.Fatalf("zweites Reload: %v", err)
	}
	tick(t, clk) // der Prüfer sieht die Datei, die Reload schon beurteilt hat
	ch.want(t, true, true, true)
}

func TestProviderKeepsLastGood(t *testing.T) {
	d := newConfDir(t)
	good := tomlFile(nil)
	d.write("config.toml", good)
	p, clk, ch := startProvider(t, d)
	state := t.TempDir()
	if err := p.KeepCopies(state); err != nil {
		t.Fatalf("KeepCopies: %v", err)
	}
	copyPath := filepath.Join(state, LastGoodName)

	broken := tomlFile(map[string]string{"dashboard_password_hash": `"` + secretHash + `"`, "farbe": `"rot"`})
	d.write("config.toml", broken)
	tick(t, clk) // zum ersten Mal gesehen: noch kein Urteil
	if !p.OK() || p.Problem() != "" {
		t.Fatalf("nach der ersten Prüfung schon fehlerhaft: ok=%v problem=%q", p.OK(), p.Problem())
	}
	ch.want(t)

	tick(t, clk) // unverändert: jetzt fehlerhaft
	if p.OK() {
		t.Fatal("OK() nach zwei Prüfungen einer fehlerhaften Datei")
	}
	if !strings.Contains(p.Problem(), "farbe") {
		t.Errorf("Problem %q nennt den Grund nicht", p.Problem())
	}
	noSecret(t, "Problem()", p.Problem())
	for _, h := range p.Hints() {
		noSecret(t, "Hinweis", h)
	}
	if !slices.ContainsFunc(p.Hints(), func(h string) bool { return strings.Contains(h, "farbe") }) {
		t.Errorf("Hinweise %q nennen den Fehler nicht", p.Hints())
	}
	if got := p.Get(); got.DashboardPasswordHash != testBcrypt {
		t.Error("Get() liefert nicht mehr die letzte gültige Fassung")
	}
	ch.want(t, false)
	tick(t, clk)
	ch.want(t, false) // kein zweiter Wechsel

	if data, err := os.ReadFile(copyPath); err != nil || !bytes.Equal(data, good) {
		t.Errorf("Kopie nach dem Fehler: %q, %v – erwartet die gültige Fassung", data, err)
	}

	// Repariert: neue Werte, wieder in Ordnung, neue Kopie.
	repaired := tomlFile(map[string]string{"offline_after_seconds": "240"})
	d.write("config.toml", repaired)
	tick(t, clk)
	if !p.OK() || p.Problem() != "" || p.Get().OfflineAfterSeconds != 240 {
		t.Fatalf("nach der Reparatur: ok=%v problem=%q offline=%d", p.OK(), p.Problem(), p.Get().OfflineAfterSeconds)
	}
	if len(p.Hints()) != 0 {
		t.Errorf("Hinweise nach der Reparatur: %q", p.Hints())
	}
	ch.want(t, false, true)
	if data, err := os.ReadFile(copyPath); err != nil || !bytes.Equal(data, repaired) {
		t.Errorf("Kopie nach der Reparatur: %q, %v", data, err)
	}

	// Reload urteilt sofort und nennt den Grund ohne Geheimnis.
	d.write("config.toml", []byte("dashboard_password_hash = "+secretHash+"\n"))
	err := p.Reload()
	if err == nil {
		t.Fatal("Reload nimmt eine fehlerhafte Datei an")
	}
	noSecret(t, "Fehler von Reload", err.Error())
	if p.OK() || p.Problem() != err.Error() {
		t.Errorf("nach Reload: ok=%v problem=%q, Fehler %q", p.OK(), p.Problem(), err)
	}
	if p.Get().OfflineAfterSeconds != 240 {
		t.Error("nach dem fehlerhaften Reload gilt nicht mehr die letzte gültige Fassung")
	}
	ch.want(t, false, true, false)
	if data, err := os.ReadFile(copyPath); err != nil || !bytes.Equal(data, repaired) {
		t.Errorf("die Kopie darf keine fehlerhafte Fassung werden: %q, %v", data, err)
	}
}

// Review Focus 4: Ein Editor, der an Ort und Stelle schreibt, hinterlässt für
// einen Augenblick eine halbe Datei. Ist sie zwei Sekunden später in
// Ordnung, darf nichts geschehen sein – keine beendeten Sitzungen, keine
// Warnung.
func TestProviderIgnoresHalfWrittenFile(t *testing.T) {
	d := newConfDir(t)
	d.write("config.toml", tomlFile(nil))
	p, clk, ch := startProvider(t, d)

	full := tomlFile(map[string]string{"bark_alarm_title": `"Neuer Titel"`, "offline_after_seconds": "240"})
	cut := bytes.Index(full, []byte("Neuer"))
	if cut < 0 {
		t.Fatal("Testdatei ohne Titel")
	}
	d.write("config.toml", full[:cut+3]) // endet mitten im Text
	tick(t, clk)
	if !p.OK() || p.Problem() != "" {
		t.Fatalf("halbe Datei beurteilt: ok=%v problem=%q", p.OK(), p.Problem())
	}
	d.write("config.toml", full)
	tick(t, clk)
	if !p.OK() || p.Get().BarkAlarmTitle != "Neuer Titel" || p.Get().OfflineAfterSeconds != 240 {
		t.Fatalf("fertige Datei: ok=%v titel=%q", p.OK(), p.Get().BarkAlarmTitle)
	}
	ch.want(t, true)

	// Ein Editor, der über eine neue Datei ersetzt: kurz fehlt sie ganz.
	if err := os.Remove(d.config()); err != nil {
		t.Fatal(err)
	}
	tick(t, clk)
	if !p.OK() {
		t.Fatal("eine kurz fehlende Datei wurde beurteilt")
	}
	d.write("config.toml", tomlFile(map[string]string{"offline_after_seconds": "300"}))
	tick(t, clk)
	if !p.OK() || p.Get().OfflineAfterSeconds != 300 {
		t.Fatalf("nach dem Ersetzen: ok=%v offline=%d", p.OK(), p.Get().OfflineAfterSeconds)
	}
	ch.want(t, true, true)

	// Zweimal halb, jedes Mal anders: weiter kein Urteil.
	d.write("config.toml", full[:cut+1])
	tick(t, clk)
	d.write("config.toml", full[:cut+4])
	tick(t, clk)
	d.write("config.toml", full)
	tick(t, clk)
	if !p.OK() {
		t.Fatal("wechselnde halbe Dateien wurden beurteilt")
	}
	ch.want(t, true, true, true)
}

func TestProviderWarningsApply(t *testing.T) {
	d := newConfDir(t)
	d.write("config.toml", tomlFile(map[string]string{"check_interval_seconds": "0"}))
	p, clk, ch := startProvider(t, d)
	if !p.OK() || p.Get().CheckIntervalSeconds != 0 || len(p.Get().Warnings()) != 1 {
		t.Fatalf("Datei mit Hinweis: ok=%v intervall=%d hinweise=%q", p.OK(), p.Get().CheckIntervalSeconds, p.Get().Warnings())
	}
	d.write("config.toml", tomlFile(map[string]string{"bark_host": `"https://api.day.app/pfad"`, "offline_after_seconds": "240"}))
	tick(t, clk)
	if !p.OK() || p.Get().OfflineAfterSeconds != 240 || p.Get().BarkHost != "https://api.day.app/pfad" {
		t.Fatalf("Hinweis-Datei gilt nicht: ok=%v offline=%d", p.OK(), p.Get().OfflineAfterSeconds)
	}
	ch.want(t, true)
}

func TestProviderStartsFromCopy(t *testing.T) {
	broken := tomlFile(map[string]string{"dashboard_password_hash": `"` + secretHash + `"`, "farbe": `"rot"`})
	copyData := tomlFile(map[string]string{"offline_after_seconds": "240"})

	t.Run("fehlerhafte Datei und Kopie", func(t *testing.T) {
		d := newConfDir(t)
		d.write("config.toml", broken)
		d.write("kopie.toml", copyData)
		p, clk, ch := startProvider(t, d)
		if p.OK() {
			t.Fatal("OK() bei einem Start mit der Kopie")
		}
		if p.Get().OfflineAfterSeconds != 240 {
			t.Errorf("es gilt nicht die Kopie: offline=%d", p.Get().OfflineAfterSeconds)
		}
		if !strings.Contains(p.Problem(), "farbe") {
			t.Errorf("Problem %q nennt den Grund nicht", p.Problem())
		}
		noSecret(t, "Problem()", p.Problem())
		if !slices.ContainsFunc(p.Hints(), func(h string) bool { return strings.Contains(h, d.lastGood()) }) {
			t.Errorf("Hinweise %q nennen die Kopie nicht", p.Hints())
		}
		// Eine Kopie wird nie aus der Kopie geschrieben.
		state := t.TempDir()
		if err := p.KeepCopies(state); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(state, LastGoodName)); err == nil {
			t.Error("Kopie aus der Kopie geschrieben")
		}

		// Unverändert bleibt es dabei; repariert gilt die Datei.
		tick(t, clk)
		ch.want(t)
		d.write("config.toml", tomlFile(map[string]string{"offline_after_seconds": "300"}))
		tick(t, clk)
		if !p.OK() || p.Get().OfflineAfterSeconds != 300 || len(p.Hints()) != 0 {
			t.Fatalf("nach der Reparatur: ok=%v offline=%d hinweise=%q", p.OK(), p.Get().OfflineAfterSeconds, p.Hints())
		}
		ch.want(t, true)
		if data, err := os.ReadFile(filepath.Join(state, LastGoodName)); err != nil || !bytes.Equal(data, d.read("config.toml")) {
			t.Errorf("nach der Reparatur keine Kopie: %v", err)
		}
	})

	t.Run("Datei fehlt, Kopie da", func(t *testing.T) {
		d := newConfDir(t)
		d.write("kopie.toml", copyData)
		p, _, _ := startProvider(t, d)
		if p.OK() || p.Get().OfflineAfterSeconds != 240 || !strings.HasPrefix(p.Problem(), "Datei nicht lesbar") {
			t.Errorf("ok=%v offline=%d problem=%q", p.OK(), p.Get().OfflineAfterSeconds, p.Problem())
		}
	})

	for _, tc := range []struct {
		name         string
		config, copy []byte // nil = fehlt
	}{
		{"beides fehlt", nil, nil},
		{"Datei fehlerhaft, Kopie fehlt", broken, nil},
		{"beides fehlerhaft", broken, broken},
		{"Datei fehlt, Kopie fehlerhaft", nil, broken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newConfDir(t)
			if tc.config != nil {
				d.write("config.toml", tc.config)
			}
			if tc.copy != nil {
				d.write("kopie.toml", tc.copy)
			}
			p, err := NewProvider(d.config(), d.lastGood(), clock.NewFake(loadMoment(t)), quietLog())
			if err == nil || p != nil {
				t.Fatalf("Start ohne brauchbare Fassung: %v", err)
			}
			if !strings.Contains(err.Error(), d.lastGood()) {
				t.Errorf("Fehler %q nennt die Kopie nicht", err)
			}
			noSecret(t, "Fehler", err.Error())
		})
	}
}

// countingSource zählt, wie oft der Provider die Datei ansieht.
type countingSource struct{ stats, reads atomic.Int64 }

func (c *countingSource) Stat(name string) (fs.FileInfo, error) {
	c.stats.Add(1)
	return osSource{}.Stat(name)
}

func (c *countingSource) ReadFile(name string) ([]byte, error) {
	c.reads.Add(1)
	return osSource{}.ReadFile(name)
}

func TestProviderNeverReadsInGet(t *testing.T) {
	d := newConfDir(t)
	d.write("config.toml", tomlFile(nil))
	src := &countingSource{}
	p, err := newProvider(d.config(), d.lastGood(), clock.NewFake(loadMoment(t)), quietLog(), src)
	if err != nil {
		t.Fatal(err)
	}
	if src.reads.Load() == 0 {
		t.Fatal("der Zähler sieht das Lesen beim Start nicht")
	}
	src.stats.Store(0)
	src.reads.Store(0)
	for range 1000 {
		if p.Get() == nil || !p.OK() || p.Problem() != "" || p.Hints() != nil {
			t.Fatal("unerwarteter Stand")
		}
	}
	if n, m := src.stats.Load(), src.reads.Load(); n != 0 || m != 0 {
		t.Errorf("1000 Abfragen: %d Stat, %d Lesen – erwartet 0", n, m)
	}
}

// state_dir, listen und control_socket ändern sich im laufenden Server
// nicht; health nennt den neuen Wert als Hinweis.
func TestProviderFixedFields(t *testing.T) {
	d := newConfDir(t)
	d.write("config.toml", tomlFile(map[string]string{
		"state_dir": `"/srv/alt"`, "listen": `"127.0.0.1:8080"`, "control_socket": `"/run/alt.sock"`}))
	p, clk, ch := startProvider(t, d)
	d.write("config.toml", tomlFile(map[string]string{
		"state_dir": `"/srv/neu"`, "listen": `"unix:/run/neu.sock"`, "control_socket": `"/run/neu.sock"`,
		"offline_after_seconds": "240"}))
	tick(t, clk)
	c := p.Get()
	if c.OfflineAfterSeconds != 240 {
		t.Fatalf("die übrigen Werte gelten nicht: offline=%d", c.OfflineAfterSeconds)
	}
	if c.StateDir != "/srv/alt" || c.Listen != "127.0.0.1:8080" || c.ControlSocket != "/run/alt.sock" {
		t.Errorf("feste Werte geändert: %q %q %q", c.StateDir, c.Listen, c.ControlSocket)
	}
	ch.want(t, true)
	hints := strings.Join(p.Hints(), "\n")
	for _, name := range []string{"state_dir", "listen", "control_socket"} {
		if !strings.Contains(hints, name) {
			t.Errorf("Hinweise %q nennen %s nicht", p.Hints(), name)
		}
	}
	if !p.OK() {
		t.Error("ein geänderter fester Wert macht die Datei nicht fehlerhaft")
	}

	d.write("config.toml", tomlFile(map[string]string{
		"state_dir": `"/srv/alt"`, "listen": `"127.0.0.1:8080"`, "control_socket": `"/run/alt.sock"`}))
	tick(t, clk)
	if len(p.Hints()) != 0 {
		t.Errorf("Hinweise nach dem Zurückstellen: %q", p.Hints())
	}
}

func TestProviderWritesCopy(t *testing.T) {
	d := newConfDir(t)
	good := tomlFile(nil)
	d.write("config.toml", good)
	p, clk, _ := startProvider(t, d)
	state := t.TempDir()
	copyPath := filepath.Join(state, LastGoodName)
	if _, err := os.Stat(copyPath); err == nil {
		t.Fatal("Kopie vor KeepCopies")
	}
	if err := p.KeepCopies(state); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("Rechte der Kopie %v, erwartet 0600", info.Mode().Perm())
	}
	if data, _ := os.ReadFile(copyPath); !bytes.Equal(data, good) {
		t.Errorf("Kopie %q", data)
	}
	if _, err := os.Stat(copyPath + ".tmp"); err == nil {
		t.Error("Zwischendatei liegen geblieben")
	}

	next := tomlFile(map[string]string{"offline_after_seconds": "240"})
	d.write("config.toml", next)
	tick(t, clk)
	if data, _ := os.ReadFile(copyPath); !bytes.Equal(data, next) {
		t.Errorf("Kopie folgt der neuen Fassung nicht: %q", data)
	}
}

// Ein Laufzeitfehler in einem Folge-Aufruf hält weder die anderen noch den
// Prüfer auf.
func TestProviderListenerPanic(t *testing.T) {
	d := newConfDir(t)
	d.write("config.toml", tomlFile(nil))
	clk := clock.NewFake(loadMoment(t))
	p, err := NewProvider(d.config(), d.lastGood(), clk, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	p.OnChange(func(bool) { panic("kaputt") })
	ch := &changes{}
	p.OnChange(ch.add)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()
	d.write("config.toml", tomlFile(map[string]string{"offline_after_seconds": "240"}))
	tick(t, clk)
	d.write("config.toml", tomlFile(map[string]string{"offline_after_seconds": "300"}))
	tick(t, clk)
	ch.want(t, true, true)
	if p.Get().OfflineAfterSeconds != 300 {
		t.Error("der Prüfer läuft nach dem Laufzeitfehler nicht weiter")
	}
}
