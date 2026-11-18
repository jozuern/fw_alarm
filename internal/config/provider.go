package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"fwalarm/internal/clock"
)

// LastGoodName heißt die Kopie der zuletzt gültig geladenen Fassung im
// Datenordner (Spec §6.8).
const LastGoodName = "config.last-good.toml"

// checkEvery: Abstand der Prüfungen im Hintergrund (Spec §6.8).
const checkEvery = 2 * time.Second

// fileSource ist, wie der Provider die Datei sieht. Das Programm nimmt
// osSource; ein Test zählt darüber, ob gelesen wird.
type fileSource interface {
	Stat(name string) (fs.FileInfo, error)
	ReadFile(name string) ([]byte, error)
}

type osSource struct{}

func (osSource) Stat(name string) (fs.FileInfo, error) { return os.Stat(name) }
func (osSource) ReadFile(name string) ([]byte, error)  { return readFile(name) }

// stamp ist der Stand einer Datei, wie Stat ihn sieht: Größe,
// Änderungszeit, Dateinummer – oder der Grund, warum Stat scheiterte.
type stamp struct {
	info fs.FileInfo // nil: Stat scheiterte
	err  string
}

func stampOf(info fs.FileInfo, err error) stamp {
	if err != nil {
		return stamp{err: err.Error()}
	}
	// Unter Windows holt Go die Dateinummer erst beim ersten Vergleich – über
	// den Pfad, also von der Datei, die DANN dort liegt. Ein Vergleich gleich
	// nach Stat hält die Nummer dieser Datei fest (unter Linux steht sie
	// schon im Ergebnis von Stat).
	_ = os.SameFile(info, info) // nur für die Wirkung, das Ergebnis ist immer true
	return stamp{info: info}
}

// same: Sieht die Datei aus wie bei a? os.SameFile vergleicht die
// Dateinummer (Gerät und Inode bzw. unter Windows Datenträger und Index).
func (a stamp) same(b stamp) bool {
	if a.info == nil || b.info == nil {
		return a.info == nil && b.info == nil && a.err == b.err
	}
	return a.info.Size() == b.info.Size() && a.info.ModTime().Equal(b.info.ModTime()) &&
		os.SameFile(a.info, b.info)
}

// reading ist eine geänderte Fassung (gültig oder fehlerhaft), über die noch
// nicht geurteilt ist: Erst wenn die Datei bei der nächsten Prüfung genauso
// aussieht, gilt sie bzw. gilt als fehlerhaft.
type reading struct {
	st   stamp
	data []byte
}

// report ist, was Problem() und Hints() liefern – als Ganzes umgesetzt.
type report struct {
	problem string
	hints   []string
}

// fixedValues gelten vom Start bis zum Ende des Prozesses.
type fixedValues struct{ stateDir, listen, controlSocket string }

// Provider hält die geltende Konfiguration im Speicher (Spec §6.8): Get
// liest nie die Datei. Run prüft sie alle 2 s (Änderungszeit, Größe,
// Dateinummer) und setzt bei einer gültigen neuen Fassung den Zeiger um;
// Reload tut dasselbe sofort.
//
// Eine geänderte Datei beurteilt der Prüfer erst, wenn sie bei der nächsten
// Prüfung unverändert ist (gleicher Stand, gleiche Bytes) – gültig oder
// fehlerhaft. Ein Editor, der gerade schreibt, löst so nichts aus, auch
// nicht, wenn sein halber Stand gültiges TOML mit weniger Werten ist (dann
// gälten die Standards: keine Probe-Sperre, der echte Bark-Server). Eine
// fehlerhafte Datei: Es gilt die letzte gute Fassung weiter, OK() ist false
// und Problem() nennt den Grund (ohne Geheimnisse: die Fehlertexte von
// ParseAt nennen nie einen Wert).
//
// state_dir, listen und control_socket gelten vom Start an bis zum Ende:
// Eine neue Fassung mit anderen Werten dort gilt im Übrigen, Get liefert
// für diese drei weiter die alten, und Hints nennt die Änderung.
//
// Nach KeepCopies legt jede Fassung von config.toml, die in Kraft gesetzt
// wird, eine Kopie (LastGoodName, Rechte 0600) in den genannten Ordner
// (provider_copy.go); mit ihr startet NewProvider, wenn config.toml nicht
// ladbar ist.
type Provider struct {
	path, lastGoodPath string
	clk                clock.Clock
	log                *slog.Logger
	src                fileSource

	cur  atomic.Pointer[Config]
	ok   atomic.Bool
	info atomic.Pointer[report]

	// mu: eine Beurteilung zur Zeit (Run, Reload, KeepCopies); schützt alle
	// Felder darunter. Die Folge-Aufrufe (OnChange) laufen unter mu.
	mu       sync.Mutex
	seen     stamp    // Stand der Datei bei der letzten Beurteilung
	suspect  *reading // geändert gelesen, noch nicht beurteilt
	curBytes []byte   // Inhalt der geltenden Fassung
	latest   *Config  // zuletzt gültig gelesene Fassung von config.toml, wie in der Datei
	fixed    fixedValues
	fromCopy bool // die geltende Fassung stammt aus der Kopie
	problem  string
	copyDir  string // "" = (noch) keine Kopien

	lmu       sync.Mutex
	listeners []func(ok bool)
}

// NewProvider lädt path. Ist die Datei nicht ladbar, startet er mit der
// Kopie lastGoodPath (OK() false, Problem() nennt den Grund); ist auch die
// nicht ladbar, ist das der Fehler. log nil = das Standard-Journal.
func NewProvider(path, lastGoodPath string, clk clock.Clock, log *slog.Logger) (*Provider, error) {
	return newProvider(path, lastGoodPath, clk, log, osSource{})
}

func newProvider(path, lastGoodPath string, clk clock.Clock, log *slog.Logger, src fileSource) (*Provider, error) {
	if log == nil {
		log = slog.Default()
	}
	p := &Provider{path: path, lastGoodPath: lastGoodPath, clk: clk, log: log, src: src}
	p.mu.Lock()
	defer p.mu.Unlock()

	st, data, c, err := p.load()
	p.seen = st
	if err == nil {
		p.fixed = fixedOf(c)
		p.latest = c
		p.cur.Store(c)
		p.curBytes = data
		p.ok.Store(true)
		p.publish()
		p.log.Info("Konfiguration geladen", "datei", path)
		p.logWarnings(c)
		return p, nil
	}
	reason := err.Error()
	copyCfg, copyData, cerr := p.loadCopy()
	if cerr != nil {
		return nil, fmt.Errorf("%s: %s; Kopie der letzten gültigen Fassung %s: %s",
			filepath.Base(path), reason, lastGoodPath, cerr)
	}
	p.fixed = fixedOf(copyCfg)
	p.cur.Store(copyCfg)
	p.curBytes = copyData
	p.fromCopy = true
	p.problem = reason
	p.publish()
	p.log.Error("config.toml nicht ladbar – Start mit der Kopie der letzten gültigen Fassung",
		"grund", reason, "kopie", lastGoodPath)
	return p, nil
}

func fixedOf(c *Config) fixedValues {
	return fixedValues{stateDir: c.StateDir, listen: c.Listen, controlSocket: c.ControlSocket}
}

// load liest und prüft config.toml; st ist der Stand vor dem Lesen.
func (p *Provider) load() (st stamp, data []byte, c *Config, err error) {
	info, err := p.src.Stat(p.path)
	st = stampOf(info, err)
	if err != nil {
		return st, nil, nil, fmt.Errorf("Datei nicht lesbar: %w", err)
	}
	data, err = p.src.ReadFile(p.path)
	if err != nil {
		return st, nil, nil, err
	}
	c, err = ParseAt(data, p.clk.Now())
	if err == nil && p.fixed.listen != "" {
		// listen gilt erst nach einem Neustart: Bis dahin lauscht der Server
		// auf dem Wert vom Start – gegen ihn zählt die Regel für den
		// Unix-Socket, auch wenn die neue Fassung schon TCP nennt.
		if err = checkUnixListen(c, p.fixed.listen); err != nil {
			c = nil
		}
	}
	return st, data, c, err
}

func (p *Provider) loadCopy() (*Config, []byte, error) {
	if p.lastGoodPath == "" {
		return nil, nil, errors.New("kein Pfad angegeben")
	}
	data, err := p.src.ReadFile(p.lastGoodPath)
	if err != nil {
		return nil, nil, err
	}
	c, err := ParseAt(data, p.clk.Now())
	if err != nil {
		return nil, nil, err
	}
	return c, data, nil
}

// Get liefert die geltende Fassung – aus dem Speicher, nie nil.
func (p *Provider) Get() *Config { return p.cur.Load() }

// OK: false = config.toml ist fehlerhaft (oder war es beim Start), es gilt
// die letzte gute Fassung.
func (p *Provider) OK() bool { return p.ok.Load() }

// Problem nennt den Grund, warum config.toml fehlerhaft ist ("" = in
// Ordnung) – ohne Geheimnisse.
func (p *Provider) Problem() string { return p.info.Load().problem }

// Hints nennt, was fw-alarm health zur Konfiguration sagen soll: den Fehler
// einer fehlerhaften Datei, einen Start mit der Kopie und geänderte Werte,
// die erst nach einem Neustart gelten. nil = nichts.
func (p *Provider) Hints() []string { return slices.Clone(p.info.Load().hints) }

// OnChange meldet fn jeden Wechsel gut↔fehlerhaft und jede neue gültige
// Fassung (ok = OK() danach). fn läuft in der Goroutine, die geprüft hat,
// und darf Get, OK, Problem und Hints fragen, aber nie Reload rufen. Ein
// Laufzeitfehler in fn wird abgefangen und ins Journal geschrieben.
func (p *Provider) OnChange(fn func(ok bool)) {
	p.lmu.Lock()
	defer p.lmu.Unlock()
	p.listeners = append(p.listeners, fn)
}

// Run prüft die Datei alle 2 s, bis ctx endet.
func (p *Provider) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.clk.After(checkEvery):
		}
		p.safely("Prüfung der Konfiguration", p.check)
	}
}

// check ist eine Prüfung des Hintergrund-Prüfers. Jede geänderte Lesung
// wartet eine Prüfung: Erst wenn die nächste denselben Stand und dieselben
// Bytes findet, gilt sie (gültig) bzw. gilt die Datei als fehlerhaft.
func (p *Provider) check() {
	p.mu.Lock()
	defer p.mu.Unlock()
	info, serr := p.src.Stat(p.path)
	if p.suspect == nil && stampOf(info, serr).same(p.seen) {
		return
	}
	st, data, c, err := p.load()
	if p.suspect != nil && p.suspect.st.same(st) && bytes.Equal(p.suspect.data, data) {
		p.suspect = nil
		p.seen = st
		if err != nil {
			p.reject(err)
			return
		}
		p.accept(c, data)
		return
	}
	if err == nil && p.ok.Load() && bytes.Equal(data, p.curBytes) {
		// Inhalt wie die geltende Fassung (nur angefasst, oder nach einer halben
		// Fassung wieder vollständig): nichts zu beurteilen.
		p.suspect = nil
		p.seen = st
		return
	}
	// Zum ersten Mal so gesehen: vielleicht schreibt gerade ein Editor.
	p.suspect = &reading{st: st, data: data}
}

// Reload lädt config.toml sofort und urteilt gleich: nil = die neue Fassung
// gilt (oder sie ist dieselbe wie bisher); sonst ist die Datei fehlerhaft,
// es gilt die letzte gute, und der Fehler nennt den Grund ohne Geheimnisse.
func (p *Provider) Reload() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	st, data, c, err := p.load()
	p.suspect = nil
	p.seen = st
	if err != nil {
		p.reject(err)
		return err
	}
	p.accept(c, data)
	return nil
}

// accept setzt eine gültig gelesene Fassung von config.toml in Kraft (unter
// mu). Derselbe Inhalt wie die geltende, die in Ordnung ist, ist keine neue
// Fassung.
func (p *Provider) accept(c *Config, data []byte) {
	p.latest = c
	if p.ok.Load() && bytes.Equal(data, p.curBytes) {
		p.publish()
		return
	}
	p.cur.Store(p.withFixed(c))
	p.curBytes = data
	p.fromCopy = false
	p.problem = ""
	p.ok.Store(true)
	p.publish()
	p.log.Info("neue Fassung der Konfiguration gilt", "datei", p.path)
	p.logWarnings(c)
	if err := p.writeCopy(); err != nil {
		p.log.Warn("Kopie der Konfiguration nicht geschrieben", "err", err)
	}
	p.notify(true)
}

// reject: config.toml ist fehlerhaft, es gilt die letzte gute Fassung
// weiter (unter mu).
func (p *Provider) reject(err error) {
	wasOK := p.ok.Load()
	p.ok.Store(false)
	p.problem = err.Error()
	p.publish()
	p.log.Error("config.toml fehlerhaft – es gilt die letzte gültige Fassung", "grund", p.problem)
	if wasOK {
		p.notify(false)
	}
}

// withFixed liefert c mit den Werten, die vom Start an gelten.
func (p *Provider) withFixed(c *Config) *Config {
	if fixedOf(c) == p.fixed {
		return c
	}
	eff := *c
	eff.StateDir, eff.Listen, eff.ControlSocket = p.fixed.stateDir, p.fixed.listen, p.fixed.controlSocket
	return &eff
}

// publish setzt Problem und Hinweise neu (unter mu).
func (p *Provider) publish() {
	r := &report{problem: p.problem}
	if p.problem != "" {
		r.hints = append(r.hints, "config.toml fehlerhaft – es gilt die letzte gültige Fassung: "+p.problem)
	}
	if p.fromCopy {
		r.hints = append(r.hints, "gestartet mit der Kopie "+p.lastGoodPath+" (config.toml war beim Start nicht ladbar)")
	}
	if l := p.latest; l != nil {
		for _, f := range []struct{ name, now, fixed string }{
			{"state_dir", l.StateDir, p.fixed.stateDir},
			{"listen", l.Listen, p.fixed.listen},
			{"control_socket", l.ControlSocket, p.fixed.controlSocket},
		} {
			if f.now != f.fixed {
				r.hints = append(r.hints, f.name+" in config.toml geändert – der neue Wert gilt erst nach einem Neustart des Servers")
			}
		}
	}
	p.info.Store(r)
}

func (p *Provider) logWarnings(c *Config) {
	for _, w := range c.Warnings() {
		p.log.Warn("Hinweis zur Konfiguration", "hinweis", w)
	}
}

// notify ruft die Folge-Aufrufe (unter mu).
func (p *Provider) notify(ok bool) {
	p.lmu.Lock()
	fns := slices.Clone(p.listeners)
	p.lmu.Unlock()
	for _, fn := range fns {
		p.safely("Folge einer neuen Konfiguration", func() { fn(ok) })
	}
}

// safely führt fn aus; ein Laufzeitfehler darin landet wie bei bg.Go ohne
// Daten im Journal (Name, bei Fehlern der Sprache deren Text, sonst die Art
// des Werts, dazu der Stapel).
func (p *Provider) safely(name string, fn func()) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		kind := fmt.Sprintf("%T", r)
		if rerr, ok := r.(runtime.Error); ok {
			kind = rerr.Error()
		}
		p.log.Error("Laufzeitfehler abgefangen", "arbeit", name, "art", kind, "stapel", string(debug.Stack()))
	}()
	fn()
}
