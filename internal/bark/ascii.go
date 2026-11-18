package bark

import (
	"strings"
	"unicode/utf8"
)

// asciiTable ist die feste Ersetzungstabelle von bark_ascii (PHP) – feste
// Tabelle statt einer Systembibliothek, deren Ergebnis vom System abhängt.
// "\r\n" steht vor "\r", damit die längere Folge gewinnt.
var asciiTable = strings.NewReplacer(
	"ä", "ae", "ö", "oe", "ü", "ue", "Ä", "Ae", "Ö", "Oe", "Ü", "Ue", "ß", "ss",
	"à", "a", "á", "a", "â", "a", "ã", "a", "å", "a", "æ", "ae", "ç", "c",
	"è", "e", "é", "e", "ê", "e", "ë", "e", "ì", "i", "í", "i", "î", "i", "ï", "i",
	"ñ", "n", "ò", "o", "ó", "o", "ô", "o", "õ", "o", "ø", "o",
	"ù", "u", "ú", "u", "û", "u", "ý", "y", "ÿ", "y",
	"À", "A", "Á", "A", "Â", "A", "Ã", "A", "Å", "A", "Æ", "Ae", "Ç", "C",
	"È", "E", "É", "E", "Ê", "E", "Ë", "E", "Ì", "I", "Í", "I", "Î", "I", "Ï", "I",
	"Ñ", "N", "Ò", "O", "Ó", "O", "Ô", "O", "Õ", "O", "Ø", "O",
	"Ù", "U", "Ú", "U", "Û", "U", "Ý", "Y",
	"š", "s", "Š", "S", "ž", "z", "Ž", "Z", "č", "c", "Č", "C", "ć", "c", "Ć", "C",
	"ł", "l", "Ł", "L", "ń", "n", "ś", "s", "ź", "z", "ż", "z", "ę", "e", "ą", "a",
	"ğ", "g", "ş", "s", "ı", "i", "İ", "I",
	"–", "-", "—", "-", "„", `"`, "“", `"`, "”", `"`, "‘", "'", "’", "'", "…", "...",
	"\r\n", "\n", "\r", "\n", "\t", " ",
)

// ASCII macht einen Text für Bark darstellbar (PHP: bark_ascii): Umlaute und
// Akzente nach der festen Tabelle, Zeilenumbrüche bleiben, Tabulator wird
// Leerzeichen, jedes übrige Nicht-ASCII-Zeichen wird zu "?". Ungültiges
// UTF-8 wird wie in PHP byteweise gesäubert (eine Folge = ein "?").
func ASCII(s string) string {
	s = asciiTable.Replace(s)
	keep := func(b byte) bool { return b == '\n' || (b >= 0x20 && b <= 0x7e) }
	var out strings.Builder
	out.Grow(len(s))
	if utf8.ValidString(s) {
		for _, r := range s {
			if r < utf8.RuneSelf && keep(byte(r)) {
				out.WriteRune(r)
			} else {
				out.WriteByte('?')
			}
		}
		return out.String()
	}
	inRun := false
	for i := 0; i < len(s); i++ {
		if keep(s[i]) {
			out.WriteByte(s[i])
			inRun = false
		} else if !inRun {
			out.WriteByte('?')
			inRun = true
		}
	}
	return out.String()
}
