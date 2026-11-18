package model

import "unicode/utf8"

// ClipBytes kürzt s auf höchstens n Bytes, nie mitten in einem Zeichen
// (ein ungültiges Byte zählt als eigenes Zeichen) – PHPs substr($s, 0, n),
// nur ohne angeschnittene Zeichen. Die einzige Fassung dieser Regel;
// web.ClipBytes reicht nur hierher weiter.
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
