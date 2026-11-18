// Package web ist die HTTP-Seite des Servers: die Tabelle der Adressen,
// Antworten, statische Dateien (hier), später Formulare, Kopfzeilen,
// Sitzungen, Anmeldung und Seiten.
//
// Die Tabelle kennt nur genaue Pfade (Spec §6.4). Mehrfache Schrägstriche
// werden wie bei Nginx zusammengezogen; umgeleitet wird nie (eine Umleitung
// machte aus dem POST der Box ein GET). Was nicht in der Tabelle steht, ist
// 404 ohne Rumpf. Jede Adresse entscheidet selbst über die Methode.
package web

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"fwalarm/internal/core"
)

// Handler beantwortet die Anfragen an eine Adresse – jede Methode.
type Handler func(w http.ResponseWriter, r *http.Request)

// Zeitlimits und Größe der Kopfzeilen (Spec §6.4). Leerlauf 120 s:
// cloudflared hält Verbindungen bis zu 90 s.
const (
	headerTimeout  = 10 * time.Second
	readTimeout    = 30 * time.Second
	writeTimeout   = 150 * time.Second
	idleTimeout    = 120 * time.Second
	maxHeaderBytes = 64 << 10
)

// Router ist die Tabelle der Adressen.
type Router struct {
	log    *slog.Logger
	mu     sync.RWMutex
	routes map[string]Handler
}

// NewRouter baut die Tabelle mit den statischen Dateien (manifest.json und
// jede Datei unter assets/). Die übrigen Adressen trägt app mit Handle ein.
func NewRouter(c *core.Core) *Router {
	log := c.Log
	if log == nil {
		log = slog.Default()
	}
	rt := &Router{log: log, routes: map[string]Handler{}}
	for name, a := range embedded() {
		if a.ctype == "" {
			// Ohne Typ aus der Tabelle wird nichts ausgeliefert (geraten wird nie);
			// TestStaticTypes verlangt für jede Datei einen.
			log.Warn("eingebettete Datei ohne Inhaltstyp – nicht ausgeliefert", "datei", name)
			continue
		}
		rt.Handle("/"+name, staticHandler(a))
	}
	return rt
}

// Handle trägt h für genau den Pfad path ein (beginnt mit "/", ohne
// doppelte Schrägstriche). Ein Pfad zweimal oder ein unbrauchbarer Pfad ist
// ein Fehler im Aufbau des Programms – er fällt beim ersten Start auf.
func (rt *Router) Handle(path string, h Handler) {
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "//") || h == nil {
		panic(fmt.Sprintf("web: unbrauchbare Adresse %q", path))
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if _, dup := rt.routes[path]; dup {
		panic(fmt.Sprintf("web: Adresse %q zweimal eingetragen", path))
	}
	rt.routes[path] = h
}

// ServeHTTP sucht den Handler zum Pfad (mehrfache Schrägstriche
// zusammengezogen). Ein Laufzeitfehler im Handler beendet nur diese
// Anfrage: Status 500 ohne Rumpf, oder – war die Antwort schon begonnen –
// eine abgebrochene Verbindung (eine halbe Antwort darf nicht vollständig
// aussehen).
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := mergeSlashes(r.URL.Path)
	rt.mu.RLock()
	h := rt.routes[path]
	rt.mu.RUnlock()
	if h == nil {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusNotFound)
		return
	}
	tw := &trackingWriter{ResponseWriter: w}
	defer func() {
		v := recover()
		if v == nil {
			return
		}
		if v == http.ErrAbortHandler {
			panic(v) // gewollter Abbruch: net/http schließt still
		}
		kind := fmt.Sprintf("%T", v)
		if rerr, ok := v.(runtime.Error); ok {
			kind = rerr.Error()
		}
		// Ohne den Wert selbst (er kann Anfragedaten tragen) und ohne Query.
		rt.log.Error("Laufzeitfehler in einer Anfrage abgefangen", "pfad", path, "art", kind,
			"stapel", string(debug.Stack()))
		if tw.wrote {
			panic(http.ErrAbortHandler)
		}
		w.WriteHeader(http.StatusInternalServerError)
	}()
	h(tw, r)
}

// mergeSlashes zieht aufeinanderfolgende Schrägstriche zu einem zusammen.
func mergeSlashes(p string) string {
	if !strings.Contains(p, "//") {
		return p
	}
	var b strings.Builder
	b.Grow(len(p))
	prev := false
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c == '/' && prev {
			continue
		}
		prev = c == '/'
		b.WriteByte(c)
	}
	return b.String()
}

// trackingWriter merkt sich, ob die Antwort schon begonnen hat.
type trackingWriter struct {
	http.ResponseWriter
	wrote bool
}

func (t *trackingWriter) WriteHeader(code int) {
	if code >= 200 {
		t.wrote = true
	}
	t.ResponseWriter.WriteHeader(code)
}

func (t *trackingWriter) Write(b []byte) (int, error) {
	t.wrote = true
	return t.ResponseWriter.Write(b)
}

// Flush gibt das Gepufferte weiter (für Handler, die http.Flusher erwarten).
func (t *trackingWriter) Flush() {
	t.wrote = true
	// Kann der darunterliegende Writer nicht weitergeben, bleibt es gepuffert –
	// es geht dann mit dem Ende der Antwort hinaus.
	_ = http.NewResponseController(t.ResponseWriter).Flush()
}

// Unwrap gibt http.ResponseController den eigentlichen Writer.
func (t *trackingWriter) Unwrap() http.ResponseWriter { return t.ResponseWriter }

type viaUnixKey struct{}

// Server baut den http.Server für die Tabelle mit den Zeitlimits des Specs.
// viaUnix: Er lauscht auf einem Unix-Socket (dann kam jede Verbindung über
// den Tunnel auf diesem Rechner, siehe ViaUnixSocket).
func (rt *Router) Server(viaUnix bool) *http.Server {
	return &http.Server{
		Handler:           rt,
		ReadHeaderTimeout: headerTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		// "OPTIONS *" beantwortet sonst net/http selbst – nur die Tabelle antwortet.
		DisableGeneralOptionsHandler: true,
		ConnContext: func(ctx context.Context, _ net.Conn) context.Context {
			return context.WithValue(ctx, viaUnixKey{}, viaUnix)
		},
		ErrorLog: slog.NewLogLogger(rt.log.Handler(), slog.LevelWarn),
	}
}

// ViaUnixSocket sagt, ob die Anfrage über den Unix-Socket aus listen kam
// (listen = "unix:<Pfad>").
func ViaUnixSocket(r *http.Request) bool {
	v, _ := r.Context().Value(viaUnixKey{}).(bool)
	return v
}
