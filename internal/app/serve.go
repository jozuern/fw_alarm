package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
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
}

// Serve ist der Server (fw-alarm serve). Start: Konfiguration (sonst die
// Kopie) → Datenordner mit Sperre (ein zweiter Server dort: Abbruch) →
// Steuer-Socket (ein verwaister wird ersetzt) → listen → Kopie der
// Konfiguration in den Datenordner → Anfragen. Er läuft, bis ctx endet
// (Signal) oder fw-alarm stop kommt; dann: keine neuen Verbindungen, offene
// Anfragen höchstens 10 s, Ablage schließen (alles sichern, Sperre frei),
// Steuer-Socket und Unix-Socket von listen löschen. nil nach einem
// geordneten Ende; ein Fehler, wenn der Start scheiterte oder beim Beenden
// nicht alles gesichert wurde.
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
	rt := web.NewRouter(c)

	stopRequested := make(chan struct{})
	var stopOnce sync.Once
	hooks := control.Hooks{
		// Der Prüf-Lauf kommt mit Aufgabe 21.
		Check:  func(context.Context) string { return "NOCH_NICHT_GEBAUT" },
		Health: func() control.Health { return health(c, prov) },
		Flush:  st.Flush,
		Reload: prov.Reload,
		Stop:   func() { stopOnce.Do(func() { close(stopRequested) }) },
	}

	ctlLn, err := control.ListenUnix(cfg.ControlSocket)
	if err != nil {
		return errors.Join(fmt.Errorf("Steuer-Zugang: %w", err), closeStore())
	}
	httpLn, viaUnix, err := listen(cfg.Listen)
	if err != nil {
		closeUnix(ctlLn, log)
		return errors.Join(fmt.Errorf("listen = %q: %w", cfg.Listen, err), closeStore())
	}
	if err := prov.KeepCopies(cfg.StateDir); err != nil {
		log.Warn("Kopie der Konfiguration nicht geschrieben", "err", err)
	}

	ctlCtx, ctlCancel := context.WithCancel(context.Background())
	ctlDone := make(chan error, 1)
	go func() { ctlDone <- control.ServeListener(ctlCtx, ctlLn, hooks, log) }()

	provCtx, provCancel := context.WithCancel(context.Background())
	provDone := make(chan struct{})
	go func() {
		defer close(provDone)
		prov.Run(provCtx)
	}()

	srv := rt.Server(viaUnix)
	srvDone := make(chan error, 1)
	go func() { srvDone <- srv.Serve(httpLn) }()

	log.Info("Server läuft", "version", o.Version, "adresse", httpLn.Addr().String(),
		"steuer", cfg.ControlSocket, "datenordner", cfg.StateDir, "start", info.Mode.String(),
		"konfiguration_ok", prov.OK())

	var runErr error
	select {
	case <-ctx.Done():
		log.Info("Beenden angefordert (Signal)")
	case <-stopRequested:
		log.Info("Beenden angefordert (fw-alarm stop)")
	case err := <-srvDone:
		runErr = fmt.Errorf("HTTP-Server unerwartet beendet: %w", err)
		log.Error("HTTP-Server unerwartet beendet", "err", err)
	}

	// Beenden: keine neuen Verbindungen, offene Anfragen dürfen zu Ende laufen.
	dctx, cancel := context.WithTimeout(context.Background(), drainLimit)
	if err := srv.Shutdown(dctx); err != nil {
		log.Warn("offene Anfragen nach der Frist abgebrochen", "frist", drainLimit)
		_ = srv.Close() // hart schließen; ein Fehler dabei ändert nichts mehr
	}
	cancel()
	if viaUnix {
		removeFile(strings.TrimPrefix(cfg.Listen, "unix:"), log)
	}
	provCancel()
	<-provDone
	closeErr := closeStore()
	if closeErr != nil {
		log.Error("Ablage nicht sauber geschlossen", "err", closeErr)
	}
	// Der Steuer-Zugang geht zuletzt: Ist er fort, ist alles gesichert und
	// die Sperre frei (darauf wartet fw-alarm stop).
	ctlCancel()
	if err := <-ctlDone; err != nil {
		log.Warn("Steuer-Zugang endete mit Fehler", "err", err)
	}
	log.Info("Server beendet")
	return errors.Join(runErr, closeErr)
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
// verwaister Socket wird ersetzt) oder "host:port".
func listen(addr string) (ln net.Listener, viaUnix bool, err error) {
	if path, ok := strings.CutPrefix(addr, "unix:"); ok {
		uln, err := control.ListenUnix(path)
		if err != nil {
			return nil, true, err
		}
		return uln, true, nil
	}
	ln, err = net.Listen("tcp", addr)
	if err != nil {
		return nil, false, err
	}
	return ln, false, nil
}

// closeUnix schließt einen Unix-Socket, der nie benutzt wurde, und löscht
// seine Datei.
func closeUnix(ln *net.UnixListener, log *slog.Logger) {
	path := ln.Addr().String()
	if err := ln.Close(); err != nil {
		log.Warn("Socket nicht geschlossen", "pfad", path, "err", err)
	}
	removeFile(path, log)
}

func removeFile(path string, log *slog.Logger) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Warn("Socket-Datei nicht gelöscht", "pfad", path, "err", err)
	}
}
