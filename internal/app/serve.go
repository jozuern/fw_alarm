package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"fwalarm/internal/bark"
	"fwalarm/internal/bg"
	"fwalarm/internal/clock"
	"fwalarm/internal/config"
	"fwalarm/internal/control"
	"fwalarm/internal/core"
	"fwalarm/internal/dashapi"
	"fwalarm/internal/store"
	"fwalarm/internal/web"
)

// Fristen beim Beenden. Die vollen Regeln (40 s, Arbeit nebenher, Alarm an
// alle) kommen mit dem geordneten Beenden (Aufgabe 24).
const (
	drainLimit = 10 * time.Second // offene Anfragen dürfen so lange zu Ende laufen
	closeLimit = 10 * time.Second // Ablage schließen (alles Ausstehende sichern)
)

// ServeOptions sind die Angaben für Serve.
type ServeOptions struct {
	ConfigPath   string       // config.toml
	LastGoodPath string       // Kopie der letzten gültigen Fassung, gelesen, wenn config.toml nicht ladbar ist
	Version      string       // Stand des Programms (fw-alarm version)
	Log          *slog.Logger // nil = Text auf stdout

	// wrapListener: nur für die Tests dieses Pakets (von außen nicht
	// erreichbar) – legt eine Hülle um den Lauscher von listen ("http") und
	// den des Steuer-Zugangs ("control"), um ein unerwartetes Ende beim
	// Annehmen zu prüfen. nil = die Lauscher, wie sie sind.
	wrapListener func(which string, ln net.Listener) net.Listener
}

// Serve ist der Server (fw-alarm serve). Start: Konfiguration (sonst die
// Kopie) → Datenordner mit Sperre (ein zweiter Server dort: Abbruch) →
// Steuer-Socket (ein verwaister wird ersetzt) → listen → Kopie der
// Konfiguration in den Datenordner → Anfragen. Er läuft, bis ctx endet
// (Signal), fw-alarm stop kommt oder der HTTP-Server bzw. der Steuer-Zugang
// unerwartet endet (dann mit Fehler); beim Beenden: HTTP zu (offene
// Anfragen höchstens 10 s, Socket von listen fort), kein reload/flush mehr,
// Ablage schließen (alles sichern, Sperre frei), zuletzt der Steuer-Socket.
// Gelöscht wird eine Socket-Datei nur, solange sie noch die eigene ist. nil
// nach einem geordneten Ende; ein Fehler, wenn der Start scheiterte, der
// Server unerwartet endete oder beim Beenden nicht alles gesichert wurde.
func Serve(ctx context.Context, o ServeOptions) error {
	log := o.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stdout, nil))
	}
	clk := clock.Real()

	prov, err := config.NewProvider(o.ConfigPath, o.LastGoodPath, clk, log)
	if err != nil {
		return fmt.Errorf("Konfiguration nicht ladbar: %w", err)
	}
	cfg := prov.Get()

	st, info, err := store.Open(cfg.StateDir, store.OSFS(), clk, log)
	if err != nil {
		if errors.Is(err, store.ErrLocked) {
			return fmt.Errorf("Start abgebrochen – %w (%s)", err, cfg.StateDir)
		}
		return fmt.Errorf("Datenordner %s: %w", cfg.StateDir, err)
	}
	closeStore := func() error {
		cctx, cancel := context.WithTimeout(context.Background(), closeLimit)
		defer cancel()
		if err := st.Close(cctx); err != nil {
			return fmt.Errorf("beim Beenden nicht alles gesichert: %w", err)
		}
		return nil
	}

	c := &core.Core{
		Cfg:     prov.Get,
		CfgOK:   prov.OK,
		Store:   st,
		Bark:    bark.New(prov.Get, clk, log),
		Clock:   clk,
		Bg:      bg.New(log),
		Log:     log,
		Version: o.Version,
	}
	watchConfig(c, prov)
	rt := web.NewRouter(c)
	routes(c, rt)

	stopRequested := make(chan struct{})
	var stopOnce sync.Once
	// stopping endet, sobald das Beenden beginnt: Ein laufendes flush bricht
	// dann ab. ops lässt reload und flush nur zu, solange der Server nicht
	// schon schließt – danach fasst über den Steuer-Zugang nichts mehr den
	// Datenordner an.
	stopping, stopNow := context.WithCancel(context.Background())
	defer stopNow()
	var ops gate
	hooks := control.Hooks{
		// Der Prüf-Lauf kommt mit Aufgabe 21.
		Check:  func(context.Context) string { return "NOCH_NICHT_GEBAUT" },
		Health: func() control.Health { return health(c, prov) },
		Flush: func(ctx context.Context) error {
			if !ops.enter() {
				return errStopping
			}
			defer ops.leave()
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			defer context.AfterFunc(stopping, cancel)()
			return st.Flush(ctx)
		},
		Reload: func() error {
			if !ops.enter() {
				return errStopping
			}
			defer ops.leave()
			return prov.Reload()
		},
		Stop: func() { stopOnce.Do(func() { close(stopRequested) }) },
	}

	ctlLn, err := control.ListenUnix(cfg.ControlSocket)
	if err != nil {
		return errors.Join(fmt.Errorf("Steuer-Zugang: %w", err), closeStore())
	}
	httpLn, httpSock, err := listen(cfg.Listen)
	if err != nil {
		closeSocket(ctlLn, log)
		return errors.Join(fmt.Errorf("listen = %q: %w", cfg.Listen, err), closeStore())
	}
	if err := prov.KeepCopies(cfg.StateDir); err != nil {
		log.Warn("Kopie der Konfiguration nicht geschrieben", "err", err)
	}
	// Was die Server annehmen; Schließen schließt immer auch das Original.
	var httpAccept, ctlAccept net.Listener = httpLn, ctlLn
	if o.wrapListener != nil {
		httpAccept, ctlAccept = o.wrapListener("http", httpLn), o.wrapListener("control", ctlLn)
	}

	ctlCtx, ctlCancel := context.WithCancel(context.Background())
	defer ctlCancel()
	ctlDone := make(chan error, 1)
	go func() { ctlDone <- control.ServeListener(ctlCtx, ctlAccept, hooks, log) }()

	provCtx, provCancel := context.WithCancel(context.Background())
	defer provCancel()
	provDone := make(chan struct{})
	go func() {
		defer close(provDone)
		prov.Run(provCtx)
	}()

	srv := rt.Server(httpSock != nil)
	srvDone := make(chan error, 1)
	go func() { srvDone <- srv.Serve(httpAccept) }()

	log.Info("Server läuft", "version", o.Version, "adresse", httpLn.Addr().String(),
		"steuer", cfg.ControlSocket, "datenordner", cfg.StateDir, "start", info.Mode.String(),
		"konfiguration_ok", prov.OK())

	srvEnded, ctlEnded, runErr := waitForStop(ctx, stopRequested, srvDone, ctlDone, log)
	stopNow()

	// Beenden, in dieser Reihenfolge – ist der Steuer-Socket fort (darauf
	// wartet fw-alarm stop), fasst dieser Server weder den Datenordner noch
	// einen der beiden Socket-Pfade mehr an. Ein Signal oder ein stop, das
	// jetzt noch kommt, ändert nichts: Hier wartet nichts mehr darauf, und
	// jedes Warten unten endet von selbst (Frist, oder der Server ist schon
	// fort und wird nicht noch einmal abgewartet).
	// 1. HTTP: keine neuen Verbindungen, offene Anfragen höchstens drainLimit,
	//    der Socket ist zu (seine Datei fort, wenn sie noch die eigene ist).
	dctx, cancel := context.WithTimeout(context.Background(), drainLimit)
	switch err := srv.Shutdown(dctx); {
	case err == nil:
	case errors.Is(err, context.DeadlineExceeded):
		log.Warn("offene Anfragen nach der Frist abgebrochen", "frist", drainLimit)
		_ = srv.Close() // hart schließen; ein Fehler dabei ändert nichts mehr
	default:
		log.Warn("HTTP-Lauscher nicht sauber geschlossen", "err", err)
		_ = srv.Close() // wie oben
	}
	cancel()
	if !srvEnded { // sonst hat waitForStop sein Ergebnis schon genommen
		if err := <-srvDone; !errors.Is(err, http.ErrServerClosed) {
			log.Warn("HTTP-Server endete beim Beenden mit einem Fehler", "err", err)
		}
	}
	if err := httpLn.Close(); err != nil && httpSock != nil {
		log.Warn("Socket von listen nicht sauber geschlossen", "pfad", httpSock.Path(), "err", err)
	}
	// 2. Kein reload und kein flush mehr (laufende werden abgewartet).
	ops.close()
	// 3. Der Prüfer der Konfiguration (er schreibt die Kopie).
	provCancel()
	<-provDone
	// 4. Die Ablage: alles sichern, Sperre frei.
	closeErr := closeStore()
	if closeErr != nil {
		log.Error("Ablage nicht sauber geschlossen", "err", closeErr)
	}
	// 5. Zuletzt der Steuer-Zugang.
	if !ctlEnded {
		ctlCancel()
		if err := <-ctlDone; err != nil {
			log.Warn("Steuer-Zugang endete mit Fehler", "err", err)
		}
	}
	log.Info("Server beendet")
	return errors.Join(runErr, closeErr)
}

// watchConfig beendet alle Sitzungen, sobald config.toml fehlerhaft wird
// (Spec §6.8) – einmal je Wechsel, nicht bei jeder Prüfung der noch
// fehlerhaften Datei: Wer sich danach mit den Passwörtern der letzten
// gültigen Fassung anmeldet, bleibt angemeldet. Ist sie schon beim Start
// fehlerhaft (Start mit der Kopie), gleich jetzt – eine Passwort-Änderung
// mit Tippfehler, die erst ein Neustart einliest, sperrt ein verlorenes Handy
// ebenso aus. Jede neue gültige Fassung räumt sofort die Sitzungen ab, deren
// Zugangsdaten nicht mehr passen (Spec §8: Passwort geändert – ein
// verlorenes Handy ist draußen, auch wenn es gerade nicht abfragt); die
// übrigen bleiben. Muss vor prov.Run und dem Steuer-Zugang (reload) laufen.
func watchConfig(c *core.Core, prov *config.Provider) {
	// "false" meldet der Provider nur beim Wechsel von gut auf fehlerhaft,
	// "true" bei jeder neuen gültigen Fassung. Die Meldung läuft unter seiner
	// Sperre; EndAll und SweepSessions lesen nur Get (erlaubt).
	prov.OnChange(func(ok bool) {
		if ok {
			web.SweepSessions(c)
			return
		}
		web.EndAll(c)
	})
	if !prov.OK() {
		web.EndAll(c)
	}
}

// routes trägt die dynamischen Adressen ein (die statischen Dateien hat
// NewRouter schon), jede mit den Sicherheits-Kopfzeilen: "/" wie index.php
// (bei Nginx die Index-Datei), classic.php leitet nur um.
func routes(c *core.Core, rt *web.Router) {
	index := web.SecurityHeaders(c, web.IndexHandler(c))
	rt.Handle("/", index)
	rt.Handle("/index.php", index)
	rt.Handle("/classic.php", web.SecurityHeaders(c, web.ClassicHandler()))
	rt.Handle("/dashboard_api.php", web.SecurityHeaders(c, dashapi.Handler(c)))
}

// errStopping: reload oder flush, während der Server schon schließt.
var errStopping = errors.New("der Server wird gerade beendet")

// gate lässt Arbeit herein, bis close gerufen wird; close wartet auf die,
// die schon drin ist.
type gate struct {
	mu     sync.RWMutex
	closed bool
}

func (g *gate) enter() bool {
	g.mu.RLock()
	if g.closed {
		g.mu.RUnlock()
		return false
	}
	return true
}

func (g *gate) leave() { g.mu.RUnlock() }

func (g *gate) close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
}

// waitForStop wartet auf den Grund zum Beenden: Signal (ctx), fw-alarm stop
// – oder das unerwartete Ende des HTTP-Servers oder des Steuer-Zugangs. Ein
// Server, den niemand mehr fragen oder beenden kann, ist nicht gesund: Er
// beendet sich geordnet mit einem Fehler (systemd startet ihn neu).
// srvEnded/ctlEnded: Dessen Ergebnis ist schon genommen – der HTTP-Server
// bzw. der Steuer-Zugang ist fort und wird nicht noch einmal abgewartet.
func waitForStop(ctx context.Context, stop <-chan struct{}, srvDone, ctlDone <-chan error, log *slog.Logger) (srvEnded, ctlEnded bool, err error) {
	select {
	case <-ctx.Done():
		log.Info("Beenden angefordert (Signal)")
	case <-stop:
		log.Info("Beenden angefordert (fw-alarm stop)")
	case err := <-srvDone:
		log.Error("HTTP-Server unerwartet beendet – der Server beendet sich", "err", err)
		return true, false, fmt.Errorf("HTTP-Server unerwartet beendet: %w", err)
	case err := <-ctlDone:
		if err == nil {
			err = errors.New("ohne Fehlermeldung")
		}
		log.Error("Steuer-Zugang unerwartet beendet – der Server beendet sich", "err", err)
		return false, true, fmt.Errorf("Steuer-Zugang unerwartet beendet: %w", err)
	}
	return false, false, nil
}

// health ist die Antwort auf fw-alarm health.
func health(c *core.Core, prov *config.Provider) control.Health {
	saved := c.Store.SaveHealth()
	state := "normal"
	if c.Store.Emergency() {
		state = "emergency"
	}
	return control.Health{
		Version:  c.Version,
		State:    state,
		SaveOK:   saved.Durable && saved.Volatile && saved.Sessions && saved.History && saved.Rescue,
		ConfigOK: prov.OK(),
		Hints:    prov.Hints(),
	}
}

// listen öffnet die Adresse aus listen: "unix:<Pfad>" (Rechte 0600, ein
// verwaister Socket wird ersetzt; sock ist dann dieser Socket) oder
// "host:port".
func listen(addr string) (ln net.Listener, sock *control.Socket, err error) {
	if path, ok := strings.CutPrefix(addr, "unix:"); ok {
		s, err := control.ListenUnix(path)
		if err != nil {
			return nil, nil, err
		}
		return s, s, nil
	}
	ln, err = net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, err
	}
	return ln, nil, nil
}

// closeSocket schließt einen Unix-Socket, der nie benutzt wurde (seine
// Datei geht mit, wenn sie noch die eigene ist).
func closeSocket(s *control.Socket, log *slog.Logger) {
	if err := s.Close(); err != nil {
		log.Warn("Socket nicht geschlossen", "pfad", s.Path(), "err", err)
	}
}
