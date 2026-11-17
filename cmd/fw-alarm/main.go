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

const usage = `Aufruf: fw-alarm <Befehl> [--config <Datei>]

Befehle:
  version         Stand des Programms ausgeben
  check-config    Konfiguration prüfen (Fehler: Rückgabe 1)
  hash-password   Passwort-Hash für dashboard_password_hash erzeugen

--config: Pfad der Konfiguration (Standard ` + defaultConfigPath + `)
`

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
	default:
		fmt.Fprintf(stderr, "Unbekannter Befehl: %s\n\n%s", cmd, usage)
		return 2
	}
}
