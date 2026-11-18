package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"unicode/utf16"
	"unicode/utf8"
)

// Text antwortet mit Klartext (PHP: text_response).
func Text(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body) // scheitert nur, wenn der Aufrufer schon fort ist
}

// JSON antwortet mit v als JSON (PHP: json_response), geschrieben wie
// json_encode(…, JSON_UNESCAPED_SLASHES | JSON_INVALID_UTF8_SUBSTITUTE):
// "/" und <>& roh, alles außer ASCII als \uXXXX, ungültige Bytes als
// U+FFFD, ohne Zeilenende. Ein Wert, der sich nicht darstellen lässt (ein
// Fehler im Programm), wird 500 ohne Rumpf – wie PHP, wenn json_encode
// scheitert.
func JSON(w http.ResponseWriter, status int, v any) {
	body, err := encodeJSON(v)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body) // scheitert nur, wenn der Aufrufer schon fort ist
}

func encodeJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return asciiJSON(bytes.TrimSuffix(buf.Bytes(), []byte("\n"))), nil
}

// asciiJSON schreibt jedes Zeichen außerhalb von ASCII als \uXXXX (über
// U+FFFF als Ersatzpaar), wie PHP ohne JSON_UNESCAPED_UNICODE. Solche
// Zeichen stehen in JSON nur in Texten, das Ergebnis ist also dasselbe
// Dokument.
func asciiJSON(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for len(b) > 0 {
		if b[0] < utf8.RuneSelf {
			out = append(out, b[0])
			b = b[1:]
			continue
		}
		r, n := utf8.DecodeRune(b) // encoding/json schreibt nur gültiges UTF-8
		b = b[n:]
		if r > 0xFFFF {
			hi, lo := utf16.EncodeRune(r)
			out = fmt.Appendf(out, `\u%04x\u%04x`, hi, lo)
			continue
		}
		out = fmt.Appendf(out, `\u%04x`, r)
	}
	return out
}
