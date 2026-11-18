package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"fwalarm/internal/config"
	"fwalarm/internal/control"
)

// Wie lange die Befehle höchstens auf die Antwort des Servers warten.
var callTimeouts = map[string]time.Duration{
	"health": 10 * time.Second,
	"reload": 15 * time.Second,
	"stop":   15 * time.Second,
	"flush":  60 * time.Second,
	"check":  3 * time.Minute,
}

// stopWait: so lange wartet fw-alarm stop nach der Antwort darauf, dass der
// Server fort ist (er lässt offene Anfragen zu Ende laufen und sichert alles).
const stopWait = 60 * time.Second

// DefaultLastGoodPath ist der Standard für --last-good: die Kopie im
// Datenordner des Dienstes – $STATE_DIRECTORY (setzt systemd; bei mehreren
// Ordnern der erste), sonst /var/lib/fw-alarm.
func DefaultLastGoodPath() string {
	dir := config.DefaultStateDir
	if env := os.Getenv("STATE_DIRECTORY"); env != "" {
		if list := filepath.SplitList(env); len(list) > 0 && list[0] != "" {
			dir = list[0]
		}
	}
	return filepath.Join(dir, config.LastGoodName)
}

// RunServe ist fw-alarm serve: Journal als Text auf stdout, Ende bei SIGTERM
// oder Interrupt (oder fw-alarm stop). 0 nach einem geordneten Ende; sonst
// "Fehler: …" auf stderr und 1.
func RunServe(configPath, lastGoodPath, version string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := Serve(ctx, ServeOptions{ConfigPath: configPath, LastGoodPath: lastGoodPath, Version: version,
		Log: slog.New(slog.NewTextHandler(stdout, nil))})
	if err != nil {
		fmt.Fprintf(stderr, "Fehler: %v\n", err)
		return 1
	}
	return 0
}

// RunControl ist fw-alarm check, health, flush, reload und stop: der Befehl
// an den laufenden Server über den Steuer-Socket aus control_socket.
//
//   - health und check: die Antwort auf stdout (health: running=1, version=…,
//     state=…, save_ok=…, config_ok=…, hint=…); 0 nur, wenn der Server
//     gesund ist bzw. der Lauf stattfand.
//   - flush, reload, stop: bei Erfolg die Meldung auf stdout und 0, sonst
//     "Fehler: …" auf stderr und 1. stop kehrt erst zurück, wenn der Server
//     fort ist (höchstens 60 s).
//   - Server nicht erreichbar: "Fehler: …" auf stderr und 1; health schreibt
//     vorher running=0 auf stdout.
func RunControl(command, configPath, lastGoodPath string, stdout, stderr io.Writer) int {
	timeout, known := callTimeouts[command]
	if !known {
		fmt.Fprintf(stderr, "Fehler: unbekannter Befehl %s\n", command)
		return 2
	}
	sock := controlSocket(configPath, lastGoodPath, stderr)
	out, ok, err := control.Call(sock, command, timeout)
	if err != nil {
		if command == "health" {
			fmt.Fprintln(stdout, "running=0")
		}
		fmt.Fprintf(stderr, "Fehler: %v\n", err)
		return 1
	}
	switch {
	case command == "health" || command == "check":
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		fmt.Fprint(stdout, out)
	case ok:
		fmt.Fprintln(stdout, out)
	default:
		fmt.Fprintln(stderr, out)
	}
	if !ok {
		return 1
	}
	if command == "stop" && !control.WaitGone(sock, stopWait) {
		fmt.Fprintf(stderr, "Fehler: der Server läuft nach %v noch\n", stopWait)
		return 1
	}
	return 0
}

// controlSocket sucht den Steuer-Socket: aus config.toml, sonst aus der
// Kopie der letzten gültigen Fassung, sonst am Standardort. Ein Ausweichen
// sagt ein Hinweis auf stderr (stdout bleibt die Antwort des Servers).
func controlSocket(configPath, lastGoodPath string, stderr io.Writer) string {
	now := time.Now()
	c, err := config.LoadAt(configPath, now)
	if err == nil {
		return c.ControlSocket
	}
	if cp, cerr := config.LoadAt(lastGoodPath, now); cerr == nil {
		fmt.Fprintf(stderr, "Hinweis: %s nicht ladbar (%v) – Steuer-Zugang laut der Kopie %s\n",
			configPath, err, lastGoodPath)
		return cp.ControlSocket
	}
	fmt.Fprintf(stderr, "Hinweis: %s nicht ladbar (%v) und keine Kopie – Steuer-Zugang am Standardort %s\n",
		configPath, err, config.DefaultControlSocket)
	return config.DefaultControlSocket
}
