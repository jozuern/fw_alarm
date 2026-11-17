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
// Argon2id-Hash für dashboard_password_hash aus. Am Terminal wird verdeckt
// gelesen (ohne Echo); kommt die Eingabe aus einer Pipe, gelten die ersten
// beiden Zeilen. Das Passwort erscheint nie in einer Ausgabe.
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
	r := bufio.NewReader(in)
	a, errA := readLine(r)
	b, errB := readLine(r)
	if errA != nil || errB != nil {
		return "", "", errors.New("zwei Zeilen erwartet (Passwort und Wiederholung)")
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
