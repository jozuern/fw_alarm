// fw-alarm ist der Server des Alarm-Dashboards samt seinen Werkzeugen.
// Hier wird nur auf die Unterbefehle verteilt; die Arbeit liegt in
// internal/app. main ist die einzige Stelle, die das Programm beendet.
//
// Schreibfehler bei Meldungen an das Terminal werden nicht ausgewertet: Es
// gäbe keinen anderen Weg, sie zu melden; der Rückgabewert sagt das Ergebnis.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"
	_ "time/tzdata" // Zeitzonen eingebaut: unabhängig von der Ausstattung des Rechners

	"fwalarm/internal/app"
)

// version setzt der Bau: go build -ldflags "-X main.version=<Git-Stand>".
var version = "dev"

const defaultConfigPath = "/etc/fw-alarm/config.toml"

const usage = `Aufruf: fw-alarm <Befehl> [--config <Datei>] [--last-good <Datei>]

Befehle:
  serve           den Server starten (läuft bis SIGTERM oder fw-alarm stop)
  health          Zustand des laufenden Servers (Rückgabe 0 = gesund)
  check           vollen Prüf-Lauf im laufenden Server anstoßen
  flush           warten, bis der laufende Server alles gesichert hat
  reload          Konfiguration im laufenden Server sofort neu laden
  stop            den laufenden Server geordnet beenden
  version         Stand des Programms ausgeben
  check-config    Konfiguration prüfen (Fehler: Rückgabe 1)
  hash-password   Passwort-Hash für dashboard_password_hash erzeugen

--config: Pfad der Konfiguration (Standard ` + defaultConfigPath + `)
--last-good: Kopie der letzten gültigen Konfiguration (nur serve, health,
  check, flush, reload, stop) – gilt, wenn config.toml nicht ladbar ist.
  Standard: $STATE_DIRECTORY/config.last-good.toml, ohne STATE_DIRECTORY
  /var/lib/fw-alarm/config.last-good.toml
`

// serverCommands nehmen --last-good: serve und die Befehle an den
// laufenden Server.
var serverCommands = map[string]bool{
	"serve": true, "health": true, "check": true, "flush": true, "reload": true, "stop": true,
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd := args[0]
	switch cmd {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	}

	flags := flag.NewFlagSet(cmd, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, usage) }
	configPath := flags.String("config", defaultConfigPath, "Pfad der Konfiguration")
	lastGood := new(string)
	if serverCommands[cmd] {
		lastGood = flags.String("last-good", app.DefaultLastGoodPath(), "Kopie der letzten gültigen Konfiguration")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "Unerwartete Angabe: %s\n", flags.Arg(0))
		return 2
	}

	switch cmd {
	case "version":
		fmt.Fprintln(stdout, version)
		return 0
	case "check-config":
		return app.RunCheckConfig(*configPath, time.Now(), stdout, stderr)
	case "hash-password":
		return app.RunHashPassword(stdin, stdout, stderr)
	case "serve":
		return app.RunServe(*configPath, *lastGood, version, stdout, stderr)
	case "health", "check", "flush", "reload", "stop":
		return app.RunControl(cmd, *configPath, *lastGood, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Unbekannter Befehl: %s\n\n%s", cmd, usage)
		return 2
	}
}
