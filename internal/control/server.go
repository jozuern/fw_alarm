// Package control ist der Steuer-Zugang des Servers (Spec §6.6): HTTP über
// einen Unix-Socket (Rechte 0600), den nur erreicht, wer die Datei öffnen
// darf. Die Befehle fw-alarm check, health, flush, reload und stop sprechen
// den laufenden Server darüber an.
//
// Protokoll: POST /<befehl> ohne Rumpf; Antwort Status 200 und JSON
// {"ok": true|false, "output": "<Text>"}. ok sagt das Ergebnis (bei
// health: alles in Ordnung), output ist, was der Befehl ausgibt. Ein
// unbekannter Pfad ist 404, eine andere Methode 405 – beide in derselben
// Form mit ok=false.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"runtime/debug"
	"strings"
	"time"
)

// Hooks sind die Antworten des Servers auf die Befehle.
type Hooks struct {
	Check  func(ctx context.Context) string // voller Prüf-Lauf, seine Zeile
	Health func() Health
	Flush  func(ctx context.Context) error
	Reload func() error
	Stop   func() // stößt das Beenden an und kehrt gleich zurück
}

// Health ist der Zustand für fw-alarm health. State: normal | emergency
// (ein Start aus der Vorfassung ist ein Hinweis, kein Zustand).
type Health struct {
	Version, State   string
	SaveOK, ConfigOK bool
	Hints            []string
}

// OK: Gilt der Server als gesund (Rückgabe 0 von fw-alarm health)?
func (h Health) OK() bool { return h.State == "normal" && h.SaveOK && h.ConfigOK }

// Text sind die Zeilen von fw-alarm health: running=1, version=…, state=…,
// save_ok=0|1, config_ok=0|1, je Hinweis eine Zeile hint=…. Ein
// Zeilenumbruch in einem Wert wird zum Leerzeichen – jede Angabe bleibt
// eine Zeile.
func (h Health) Text() string {
	var b strings.Builder
	line := func(k, v string) { b.WriteString(k + "=" + oneLine(v) + "\n") }
	line("running", "1")
	line("version", h.Version)
	line("state", h.State)
	line("save_ok", bit(h.SaveOK))
	line("config_ok", bit(h.ConfigOK))
	for _, hint := range h.Hints {
		line("hint", hint)
	}
	return b.String()
}

func bit(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

// oneLine ersetzt Steuerzeichen (Zeilenumbrüche, Tabulatoren …) durch
// Leerzeichen.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// commands sind die Befehle des Steuer-Zugangs.
var commands = []string{"check", "health", "flush", "reload", "stop"}

// answer ist die Antwort auf einen Befehl.
type answer struct {
	OK     bool   `json:"ok"`
	Output string `json:"output"`
}

// Serve lauscht auf socketPath (ListenUnix) und beantwortet die Befehle,
// bis ctx endet; dann schließt es den Socket, löscht die Datei und gibt nil
// zurück. Ein Fehler beim Lauschen kommt sofort zurück.
func Serve(ctx context.Context, socketPath string, h Hooks, log *slog.Logger) error {
	ln, err := ListenUnix(socketPath)
	if err != nil {
		return err
	}
	return ServeListener(ctx, ln, h, log)
}

// ServeListener wie Serve, auf einem schon offenen Socket (so erfährt der
// Start einen Fehler beim Lauschen, bevor er weitermacht). Endet der Socket
// von selbst (Fehler beim Annehmen), kommt dieser Fehler zurück. Wenn
// ServeListener zurückkehrt, ist der Socket geschlossen, seine Datei fort
// (sofern sie noch die eigene war), und nichts von ihm fasst den Pfad mehr an.
func ServeListener(ctx context.Context, ln *Socket, h Hooks, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	srv := &http.Server{
		Handler:           handler(h, log),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       30 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	var err error
	finished := false
	select {
	case <-ctx.Done():
	case err = <-served:
		finished = true
	}
	// Laufende Befehle (ein flush, ein check) bekommen kurz Zeit; danach wird
	// geschlossen, und ihr Kontext endet.
	shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if serr := srv.Shutdown(shCtx); serr != nil {
		_ = srv.Close() // nach dem gescheiterten Shutdown: hart schließen, ein Fehler ändert nichts mehr
	}
	if !finished {
		<-served // Serve schließt den Socket beim Zurückkehren – erst danach geht es weiter
	}
	if cerr := ln.Close(); cerr != nil { // schon geschlossen: dasselbe Ergebnis noch einmal
		log.Warn("Steuer-Socket nicht sauber geschlossen", "pfad", ln.Path(), "err", cerr)
	}
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

func handler(h Hooks, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	for _, cmd := range commands {
		mux.HandleFunc("/"+cmd, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				reply(w, http.StatusMethodNotAllowed, answer{Output: "nur POST"})
				return
			}
			defer func() {
				if v := recover(); v != nil {
					kind := fmt.Sprintf("%T", v)
					if rerr, ok := v.(runtime.Error); ok {
						kind = rerr.Error()
					}
					log.Error("Laufzeitfehler im Steuer-Zugang abgefangen", "befehl", cmd, "art", kind,
						"stapel", string(debug.Stack()))
					reply(w, http.StatusOK, answer{Output: "Fehler: interner Fehler im Server (siehe Journal)"})
				}
			}()
			run(w, r, cmd, h)
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusNotFound, answer{Output: "unbekannter Befehl"})
	})
	return mux
}

func run(w http.ResponseWriter, r *http.Request, cmd string, h Hooks) {
	notAvailable := answer{Output: "Fehler: " + cmd + " gibt es in diesem Server nicht"}
	switch cmd {
	case "health":
		if h.Health == nil {
			reply(w, http.StatusOK, notAvailable)
			return
		}
		st := h.Health()
		reply(w, http.StatusOK, answer{OK: st.OK(), Output: st.Text()})
	case "check":
		if h.Check == nil {
			reply(w, http.StatusOK, notAvailable)
			return
		}
		reply(w, http.StatusOK, answer{OK: true, Output: h.Check(r.Context())})
	case "flush":
		if h.Flush == nil {
			reply(w, http.StatusOK, notAvailable)
			return
		}
		if err := h.Flush(r.Context()); err != nil {
			reply(w, http.StatusOK, answer{Output: "Fehler: nicht alles gesichert: " + oneLine(err.Error())})
			return
		}
		reply(w, http.StatusOK, answer{OK: true, Output: "Alles gesichert."})
	case "reload":
		if h.Reload == nil {
			reply(w, http.StatusOK, notAvailable)
			return
		}
		if err := h.Reload(); err != nil {
			reply(w, http.StatusOK, answer{Output: "Fehler: " + oneLine(err.Error()) + " – es gilt weiter die letzte gültige Fassung."})
			return
		}
		reply(w, http.StatusOK, answer{OK: true, Output: "Konfiguration neu geladen."})
	case "stop":
		if h.Stop == nil {
			reply(w, http.StatusOK, notAvailable)
			return
		}
		// Erst die Antwort, dann das Beenden: Der Aufrufer soll sie bekommen.
		reply(w, http.StatusOK, answer{OK: true, Output: "Server wird beendet."})
		_ = http.NewResponseController(w).Flush() // scheitert es, geht die Antwort mit dem Ende des Handlers hinaus
		h.Stop()
	}
}

func reply(w http.ResponseWriter, status int, a answer) {
	body, err := json.Marshal(a)
	if err != nil {
		// Zwei einfache Felder lassen sich immer darstellen.
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body) // scheitert nur, wenn der Aufrufer schon fort ist
}
