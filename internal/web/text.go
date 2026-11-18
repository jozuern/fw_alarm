package web

import (
	"strings"
	"unicode/utf8"
)

// Kleine Helfer für Texte, die mehrere Teile des Pakets brauchen.

// phpTrim entfernt, was PHPs trim() entfernt (nicht mehr – z.B. kein
// geschütztes Leerzeichen): für Werte aus Konfiguration und Kopfzeilen,
// die PHP vor dem Vergleich so bereinigte (proxy.go, machine.go).
func phpTrim(s string) string { return strings.Trim(s, " \t\n\r\x00\x0b") }

// ClipBytes kürzt s auf höchstens n Bytes, nie mitten in einem Zeichen
// (ein ungültiges Byte zählt als eigenes Zeichen).
func ClipBytes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	end := 0
	for end < len(s) {
		_, size := utf8.DecodeRuneInString(s[end:])
		if end+size > n {
			break
		}
		end += size
	}
	return s[:end]
}
