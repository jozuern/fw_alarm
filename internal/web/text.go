package web

import "unicode/utf8"

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
