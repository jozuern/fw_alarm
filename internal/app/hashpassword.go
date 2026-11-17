package app

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"fwalarm/internal/config"
)

// RunHashPassword liest ein neues Passwort zweimal und gibt seinen
// Argon2id-Hash für dashboard_password_hash aus. An einer Konsole wird
// verdeckt gelesen (ohne Echo); sonst (Pipe, Git Bash/mintty) gelten die
// ersten beiden Zeilen, und ein Hinweis auf stderr sagt vorher, dass die
// Eingabe sichtbar sein wird. Fragen und Hinweise gehen auf stderr, auf
// stdout steht nur der Hash; das Passwort gibt das Programm nie aus.
func RunHashPassword(in io.Reader, stdout, stderr io.Writer) int {
	first, second, err := readTwice(in, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "Fehler: %v\n", err)
		return 1
	}
	if first == "" {
		fmt.Fprintln(stderr, "Fehler: leeres Passwort – abgebrochen.")
		return 1
	}
	if first != second {
		fmt.Fprintln(stderr, "Fehler: Die beiden Eingaben stimmen nicht überein – abgebrochen.")
		return 1
	}
	hash, err := config.HashPassword(first)
	if err != nil {
		fmt.Fprintf(stderr, "Fehler: %v\n", err)
		return 1
	}
	// Der Hash ist das Ergebnis: Kommt er nicht an, ist der Befehl gescheitert.
	if _, err := fmt.Fprintln(stdout, hash); err != nil {
		fmt.Fprintf(stderr, "Fehler: Ausgabe nicht möglich: %v\n", err)
		return 1
	}
	return 0
}

func readTwice(in io.Reader, prompt io.Writer) (string, string, error) {
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fd := int(f.Fd())
		fmt.Fprint(prompt, "Neues Passwort: ")
		a, err := term.ReadPassword(fd)
		fmt.Fprintln(prompt)
		if err != nil {
			return "", "", fmt.Errorf("Passwort nicht lesbar: %w", err)
		}
		fmt.Fprint(prompt, "Noch einmal: ")
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(prompt)
		if err != nil {
			return "", "", fmt.Errorf("Passwort nicht lesbar: %w", err)
		}
		return string(a), string(b), nil
	}
	// Keine Konsole (Pipe, oder Git Bash/mintty unter Windows): Verdeckt
	// lesen geht nicht – das wird vorher gesagt, gefragt wird trotzdem. Jede
	// Frage steht auf einer eigenen Zeile: Ob das Terminal die Eingabe samt
	// Zeilenende zeigt, ist hier nicht zu erkennen.
	errLines := errors.New("zwei Zeilen erwartet (Passwort und Wiederholung)")
	fmt.Fprintln(prompt, "Hinweis: keine Konsole – die Eingabe wird sichtbar sein.")
	r := bufio.NewReader(in)
	fmt.Fprintln(prompt, "Neues Passwort:")
	a, err := readLine(r)
	if err != nil {
		return "", "", errLines
	}
	fmt.Fprintln(prompt, "Noch einmal:")
	b, err := readLine(r)
	if err != nil {
		return "", "", errLines
	}
	return a, b, nil
}

// readLine liest eine Zeile ohne ihr Zeilenende (\n oder \r\n). Eine letzte
// Zeile ohne Zeilenende zählt mit.
func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return "", err
	}
	line = strings.TrimSuffix(line, "\n")
	return strings.TrimSuffix(line, "\r"), nil
}
