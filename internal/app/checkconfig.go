// Package app enthält die Unterbefehle von fw-alarm. Jeder liefert seinen
// Rückgabewert; beendet wird das Programm allein in main.
//
// Schreibfehler bei Meldungen an das Terminal (stdout/stderr) werden nicht
// ausgewertet: Es gäbe keinen anderen Weg, sie zu melden, und der
// Rückgabewert sagt das Ergebnis ohnehin.
package app

import (
	"fmt"
	"io"
	"time"

	"fwalarm/internal/config"
)

// RunCheckConfig prüft die Konfiguration so, wie der Server sie laden würde
// (Zeitpunkt now): fehlerhaft → "Fehler: <Grund>" und 1; sonst je Hinweis
// eine Zeile "Hinweis: …", dann "Konfiguration in Ordnung." und 0. Kein
// Wert der Datei erscheint in der Ausgabe.
func RunCheckConfig(path string, now time.Time, stdout, stderr io.Writer) int {
	c, err := config.LoadAt(path, now)
	if err != nil {
		fmt.Fprintf(stderr, "Fehler: %v\n", err)
		return 1
	}
	for _, w := range c.Warnings() {
		fmt.Fprintf(stdout, "Hinweis: %s\n", w)
	}
	fmt.Fprintln(stdout, "Konfiguration in Ordnung.")
	return 0
}
