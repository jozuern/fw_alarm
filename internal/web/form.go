package web

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
)

// Formulare (Spec §6.4): Felder NUR aus dem Rumpf eines POST – nie aus der
// Query (wie $_POST in PHP), urlencoded UND multipart. Bei doppelten Namen
// zählt der letzte Wert, die Stelle bleibt die des ersten Auftretens (wie
// ein PHP-Array). Ein Rumpf mit anderem Inhaltstyp hat keine Felder.

// maxBody ist die Obergrenze des Rumpfs wie gesendet (Nginx:
// client_max_body_size 64k).
const maxBody = 64 << 10

// maxFormPairs: Mehr Paare liest auch PHP nicht (max_input_vars = 1000);
// jedes Paar zählt, auch ein übergangenes. So bleibt die Arbeit je Anfrage
// begrenzt, wie viele kleine Felder ein Rumpf auch trägt.
const maxFormPairs = 1000

// Form sind die Felder eines Rumpfs: jeder Name einmal, in der Reihenfolge
// seines ersten Auftretens, mit seinem letzten Wert. Namen und Werte sind
// gültiges UTF-8.
type Form struct {
	pairs []formPair     // Paare in der Reihenfolge des Rumpfs
	index map[string]int // Name → Stelle in pairs
}

type formPair struct{ name, value string }

// ParseForm liest den Rumpf (höchstens 64 kB) und zerlegt ihn. false: Die
// Anfrage ist schon beantwortet – 413, wenn der Rumpf zu groß ist (sofort,
// ohne den Rest zu lesen; siehe answerTooLarge), 400, wenn er sich nicht
// lesen ließ (eine abgebrochene Übertragung wird nie halb ausgewertet). Die
// Grenze gilt für jede Methode; Felder hat nur ein POST. Ein Handler ruft
// ParseForm höchstens einmal – der Rumpf ist danach gelesen.
func ParseForm(w http.ResponseWriter, r *http.Request) (*Form, bool) {
	if r.ContentLength > maxBody {
		answerTooLarge(w)
		return nil, false
	}
	var body []byte
	if r.Body != nil {
		var err error
		// Höchstens ein Byte mehr als erlaubt: daran erkennt man "zu groß",
		// auch ohne angegebene Länge (chunked).
		body, err = io.ReadAll(io.LimitReader(r.Body, maxBody+1))
		switch {
		case len(body) > maxBody:
			answerTooLarge(w)
			return nil, false
		case err != nil:
			w.Header().Set("Connection", "close")
			w.WriteHeader(http.StatusBadRequest)
			return nil, false
		}
	}
	f := &Form{index: map[string]int{}}
	if r.Method != http.MethodPost {
		return f, true
	}
	switch phpContentType(r.Header.Get("Content-Type")) {
	case "application/x-www-form-urlencoded":
		f.parseURLEncoded(body)
	case "multipart/form-data":
		f.parseMultipart(r.Header.Get("Content-Type"), body)
	}
	return f, true
}

// answerTooLarge: 413 ohne Rumpf, mit "Connection: close". Ohne diese
// Kopfzeile läse net/http einen Rest unter 256 kB erst noch, BEVOR die
// Antwort hinausgeht – und wartete auf einen Client, der nicht weiter
// sendet, bis zum Zeitlimit. So geht die 413 sofort hinaus, und die
// Verbindung wird nicht wieder benutzt. (Beim Schließen verwirft net/http
// selbst noch höchstens 256 kB eines solchen Rests – nie in den Speicher,
// begrenzt durch das Lese-Zeitlimit; bei angekündigten 10 MB gar nichts.)
func answerTooLarge(w http.ResponseWriter) {
	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusRequestEntityTooLarge)
}

// phpContentType: der Inhaltstyp, wie PHP ihn für $_POST vergleicht –
// alles vor dem ersten ";", "," oder Leerzeichen, klein geschrieben
// (main/SAPI.c).
func phpContentType(v string) string {
	if i := strings.IndexAny(v, ";, "); i >= 0 {
		v = v[:i]
	}
	return strings.ToLower(v)
}

// Get liefert den letzten Wert des Felds, "" wenn es fehlt.
func (f *Form) Get(name string) string {
	if i, ok := f.index[name]; ok {
		return f.pairs[i].value
	}
	return ""
}

// Has sagt, ob es das Feld gibt (auch leer).
func (f *Form) Has(name string) bool {
	_, ok := f.index[name]
	return ok
}

// Each ruft fn für jedes Feld: jeder Name einmal, in der Reihenfolge seines
// ersten Auftretens, mit dem letzten Wert. Liefert fn false, ist Schluss.
func (f *Form) Each(fn func(name, value string) bool) {
	for _, p := range f.pairs {
		if !fn(p.name, p.value) {
			return
		}
	}
}

// add nimmt ein Feld auf. Übergangen werden ein leerer Name und Namen mit
// "[" – daraus machte PHP eine Liste (a[], a[b]) oder einen umgebauten
// Namen (a[b → a_b), unter seinem eigenen Namen wäre es dort nie lesbar.
func (f *Form) add(name, value string) {
	name = strings.ToValidUTF8(name, "\uFFFD")
	if name == "" || strings.Contains(name, "[") {
		return
	}
	value = strings.ToValidUTF8(value, "\uFFFD")
	if i, ok := f.index[name]; ok {
		f.pairs[i].value = value
		return
	}
	f.index[name] = len(f.pairs)
	f.pairs = append(f.pairs, formPair{name: name, value: value})
}

// parseURLEncoded zerlegt wie PHP (main/php_variables.c, add_post_var):
// getrennt wird nur an "&", Name und Wert am ersten "=", ein Paar ohne "="
// hat einen leeren Wert; beides wird mit phpURLDecode entschlüsselt.
func (f *Form) parseURLEncoded(body []byte) {
	s := string(body)
	for n := 0; s != "" && n < maxFormPairs; n++ {
		var pair string
		pair, s, _ = strings.Cut(s, "&")
		name, value, _ := strings.Cut(pair, "=")
		f.add(phpURLDecode(name), phpURLDecode(value))
	}
}

// phpURLDecode entschlüsselt wie PHPs urldecode: "+" wird ein Leerzeichen,
// "%" mit zwei Hex-Ziffern das Byte; jedes andere "%" bleibt stehen (nie ein
// Fehler).
func phpURLDecode(s string) string {
	if !strings.ContainsAny(s, "+%") {
		return s
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '+':
			out = append(out, ' ')
		case c == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]):
			out = append(out, unhex(s[i+1])<<4|unhex(s[i+2]))
			i += 2
		default:
			out = append(out, c)
		}
	}
	return string(out)
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c <= '9':
		return c - '0'
	case c <= 'F':
		return c - 'A' + 10
	default:
		return c - 'a' + 10
	}
}

// parseMultipart zerlegt multipart/form-data wie PHP (main/rfc1867.c):
// Felder sind die Teile mit einem Namen in der Content-Disposition (deren
// Art prüft PHP nicht); ein Teil mit Dateinamen – auch leerem – ist eine
// Datei und kein Feld. Werte bleiben, wie gesendet (keine Dekodierung nach
// Content-Transfer-Encoding). Ist der Rumpf kaputt, gilt, was bis dahin
// vollständig war.
func (f *Form) parseMultipart(ctype string, body []byte) {
	_, params, err := mime.ParseMediaType(ctype)
	if err != nil || params["boundary"] == "" {
		return
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for n := 0; n < maxFormPairs; n++ {
		part, err := mr.NextRawPart()
		if err != nil {
			return // io.EOF: fertig; sonst kaputt – was vollständig war, gilt
		}
		value, err := io.ReadAll(part)
		if err != nil {
			return
		}
		_, disp, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		if err != nil {
			continue
		}
		if _, isFile := disp["filename"]; isFile {
			continue
		}
		if name, ok := disp["name"]; ok {
			f.add(name, string(value))
		}
	}
}
