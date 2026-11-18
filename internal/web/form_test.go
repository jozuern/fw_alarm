package web

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const formType = "application/x-www-form-urlencoded"

// postForm baut einen POST mit diesem Rumpf und Inhaltstyp.
func postForm(target, ctype, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	if ctype != "" {
		r.Header.Set("Content-Type", ctype)
	}
	return r
}

// parse ruft ParseForm auf und verlangt, dass es nicht schon geantwortet hat.
func parse(t *testing.T, r *http.Request) *Form {
	t.Helper()
	rec := httptest.NewRecorder()
	f, ok := ParseForm(rec, r)
	if !ok || f == nil {
		t.Fatalf("ParseForm: ok=%v, Antwort %d %q", ok, rec.Code, rec.Body.String())
	}
	return f
}

// pairsOf liefert, was Each liefert, als "name=wert"-Liste.
func pairsOf(f *Form) []string {
	var out []string
	f.Each(func(name, value string) bool {
		out = append(out, name+"="+value)
		return true
	})
	return out
}

// multipartBody baut einen multipart/form-data-Rumpf aus Paaren (name,
// wert); ein Name mit dem Präfix "datei:" wird als Datei-Teil geschrieben.
func multipartBody(t *testing.T, pairs ...string) (ctype, body string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for i := 0; i+1 < len(pairs); i += 2 {
		name, value := pairs[i], pairs[i+1]
		var w io.Writer
		var err error
		if file, ok := strings.CutPrefix(name, "datei:"); ok {
			w, err = mw.CreateFormFile(file, "bild.png")
		} else {
			w, err = mw.CreateFormField(name)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return mw.FormDataContentType(), buf.String()
}

// 60 Felder, einige doppelt: Reihenfolge des ersten Auftretens, letzter
// Wert – für urlencoded und multipart gleich (der Status-Push behält die
// ersten 40, Spec §6.4).
func TestFormOrderAndLastValue(t *testing.T) {
	var pairs []string
	for i := range 60 {
		pairs = append(pairs, fmt.Sprintf("f%02d", i), fmt.Sprintf("v%d", i))
	}
	// Wiederholungen: f05 und f30 kommen später noch einmal mit neuem Wert.
	pairs = append(pairs, "f05", "neu5", "f30", "neu30", "f05", "letzter5")
	want := make([]string, 0, 60)
	for i := range 60 {
		v := fmt.Sprintf("v%d", i)
		switch i {
		case 5:
			v = "letzter5"
		case 30:
			v = "neu30"
		}
		want = append(want, fmt.Sprintf("f%02d=%s", i, v))
	}

	var enc []string
	for i := 0; i < len(pairs); i += 2 {
		enc = append(enc, url.QueryEscape(pairs[i])+"="+url.QueryEscape(pairs[i+1]))
	}
	mpType, mpBody := multipartBody(t, pairs...)
	for name, r := range map[string]*http.Request{
		"urlencoded": postForm("/api/status.php", formType, strings.Join(enc, "&")),
		"multipart":  postForm("/api/status.php", mpType, mpBody),
	} {
		t.Run(name, func(t *testing.T) {
			f := parse(t, r)
			if got := pairsOf(f); strings.Join(got, "&") != strings.Join(want, "&") {
				t.Errorf("Each:\n%v\nerwartet\n%v", got, want)
			}
			if f.Get("f05") != "letzter5" || f.Get("f30") != "neu30" || f.Get("f59") != "v59" {
				t.Errorf("Get: f05=%q f30=%q f59=%q", f.Get("f05"), f.Get("f30"), f.Get("f59"))
			}
			if !f.Has("f00") || f.Has("f60") || f.Get("f60") != "" {
				t.Errorf("Has/Get eines fehlenden Felds: %v %v %q", f.Has("f00"), f.Has("f60"), f.Get("f60"))
			}
			// Each hört auf, sobald fn false liefert.
			n := 0
			f.Each(func(string, string) bool { n++; return n < 3 })
			if n != 3 {
				t.Errorf("Each nach false weitergelaufen: %d Aufrufe", n)
			}
		})
	}
}

// Ein leeres Feld ist da (Has), nur leer – anders als ein fehlendes.
func TestFormEmptyValue(t *testing.T) {
	f := parse(t, postForm("/", formType, "a=&b&c=3"))
	if !f.Has("a") || !f.Has("b") || f.Get("a") != "" || f.Get("b") != "" || f.Get("c") != "3" {
		t.Errorf("Felder: %v", pairsOf(f))
	}
}

// Dekodiert wie PHP (urldecode): "+" ist ein Leerzeichen, ein "%" ohne zwei
// Hex-Ziffern bleibt stehen, getrennt wird nur an "&" (";" gehört zum Wert).
func TestFormDecodesLikePHP(t *testing.T) {
	f := parse(t, postForm("/", formType+"; charset=UTF-8",
		"a=x+y%20z&b=100%&c=%zz%4&d=1;e=2&n%61me=%C3%A4&f=a=b&=leer&&g"))
	for name, want := range map[string]string{
		"a": "x y z", "b": "100%", "c": "%zz%4", "d": "1;e=2", "name": "ä", "f": "a=b", "g": "",
	} {
		if got := f.Get(name); got != want || !f.Has(name) {
			t.Errorf("%s = %q (da: %v), erwartet %q", name, got, f.Has(name), want)
		}
	}
	// Ein leerer Name ist kein Feld.
	if f.Has("") {
		t.Errorf("leerer Name als Feld: %v", pairsOf(f))
	}
	if got := len(pairsOf(f)); got != 7 {
		t.Errorf("%d Felder: %v", got, pairsOf(f))
	}
}

// Namen mit "[" ergäben in PHP eine Liste (oder einen umgebauten Namen) –
// solche Felder gibt es nicht, auch nicht verschlüsselt (%5B). Ein Name
// nur mit "]" bleibt wie in PHP ein gewöhnliches Feld.
func TestFormSkipsBrackets(t *testing.T) {
	body := "token[]=a&token[x]=b&a%5B%5D=c&[x]=d&b[=e&ok=1&c]=2"
	mpType, mpBody := multipartBody(t, "token[]", "a", "token[x]", "b", "b[", "e", "ok", "1", "c]", "2")
	for name, r := range map[string]*http.Request{
		"urlencoded": postForm("/", formType, body),
		"multipart":  postForm("/", mpType, mpBody),
	} {
		t.Run(name, func(t *testing.T) {
			f := parse(t, r)
			if got := strings.Join(pairsOf(f), "&"); got != "ok=1&c]=2" {
				t.Errorf("Felder %q, erwartet nur ok=1&c]=2", got)
			}
			if f.Has("token") || f.Has("token[]") || f.Has("a") {
				t.Error("ein Name mit Klammern ist als Feld lesbar")
			}
		})
	}
}

// Ein Rumpf mit anderem Inhaltstyp hat keine Felder; Datei-Teile und Teile
// ohne Namen sind keine Felder; die Query zählt nie, und nur ein POST hat
// Felder (wie $_POST in PHP).
func TestFormOtherContentType(t *testing.T) {
	for _, ctype := range []string{"", "text/plain", "application/json", "application/x-www-form-urlencodedx",
		"multipart/mixed; boundary=x", "multipart/form-data", "multipart/form-data; boundary=", ";;;"} {
		f := parse(t, postForm("/?token=aus-der-query", ctype, "token=x&a=1"))
		if got := pairsOf(f); len(got) != 0 {
			t.Errorf("Inhaltstyp %q: Felder %v", ctype, got)
		}
	}
	// Groß geschrieben und mit Parametern ist es derselbe Typ.
	if f := parse(t, postForm("/", "Application/X-WWW-Form-Urlencoded; charset=utf-8", "a=1")); f.Get("a") != "1" {
		t.Errorf("Typ in anderer Schreibweise: %v", pairsOf(f))
	}
	// Die Query ist nie ein Feld.
	if f := parse(t, postForm("/?token=aus-der-query&a=q", formType, "a=1")); f.Has("token") || f.Get("a") != "1" {
		t.Errorf("Query gelesen: %v", pairsOf(f))
	}
	// Andere Methoden haben keine Felder.
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		r := httptest.NewRequest(method, "/", strings.NewReader("a=1"))
		r.Header.Set("Content-Type", formType)
		if f := parse(t, r); f.Has("a") {
			t.Errorf("%s hat Felder: %v", method, pairsOf(f))
		}
	}
}

// Welche Teile eines multipart-Rumpfs Felder sind (wie PHP, rfc1867.c):
// Teile mit Dateinamen (auch leerem) sind Dateien, Teile ohne Namen nichts;
// die Art der Content-Disposition prüft PHP nicht. Werte bleiben, wie sie
// gesendet wurden (keine Dekodierung nach Content-Transfer-Encoding).
func TestFormMultipartParts(t *testing.T) {
	body := "Vorspann\r\n" +
		"--XyZ\r\nContent-Disposition: form-data; name=\"bild\"; filename=\"bild.png\"\r\nContent-Type: image/png\r\n\r\nPNG\r\n" +
		"--XyZ\r\nContent-Disposition: form-data; name=\"leer\"; filename=\"\"\r\n\r\nX\r\n" +
		"--XyZ\r\nContent-Disposition: form-data\r\n\r\nohne Namen\r\n" +
		"--XyZ\r\nContent-Type: text/plain\r\n\r\nohne Content-Disposition\r\n" +
		"--XyZ\r\nContent-Disposition: attachment; name=\"anhang\"\r\n\r\nY\r\n" +
		"--XyZ\r\nContent-Disposition: form-data; name=\"qp\"\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\na=3Db\r\n" +
		"--XyZ\r\nContent-Disposition: form-data; name=\"a+b%20\"\r\n\r\nx+y%20\r\n" +
		"--XyZ\r\nContent-Disposition: form-data; name=\"a\"\r\n\r\n1\r\n--XyZ--\r\nNachspann"
	f := parse(t, postForm("/", "multipart/form-data; boundary=XyZ", body))
	if got := strings.Join(pairsOf(f), "&"); got != "anhang=Y&qp=a=3Db&a+b%20=x+y%20&a=1" {
		t.Errorf("Felder %q", got)
	}
}

// Ein kaputter multipart-Rumpf: was vor dem Fehler vollständig war, gilt.
func TestFormBrokenMultipart(t *testing.T) {
	mpType, mpBody := multipartBody(t, "a", "1", "b", "2")
	cut := strings.LastIndex(mpBody, "\r\n--")
	f := parse(t, postForm("/", mpType, mpBody[:cut]))
	if !f.Has("a") || f.Has("b") {
		t.Errorf("abgeschnittener Rumpf: %v", pairsOf(f))
	}
	f = parse(t, postForm("/", mpType, "kein multipart"))
	if len(pairsOf(f)) != 0 {
		t.Errorf("Unsinn: %v", pairsOf(f))
	}
}

// Höchstens 1000 Paare werden gelesen (PHP: max_input_vars) – auch
// übergangene zählen; so bleibt die Arbeit je Anfrage begrenzt.
func TestFormMaxPairs(t *testing.T) {
	var b strings.Builder
	for i := range 1200 {
		fmt.Fprintf(&b, "f%d=%d&", i, i)
	}
	f := parse(t, postForm("/", formType, b.String()))
	if !f.Has("f999") || f.Has("f1000") || len(pairsOf(f)) != 1000 {
		t.Errorf("%d Felder, f999 %v, f1000 %v", len(pairsOf(f)), f.Has("f999"), f.Has("f1000"))
	}
	f = parse(t, postForm("/", formType, strings.Repeat("x[]=1&", 1000)+"spaet=1"))
	if f.Has("spaet") {
		t.Error("übergangene Paare zählen nicht mit")
	}
	// multipart von Hand mit kurzer Grenze – sonst wären 1100 Teile über 64 kB.
	b.Reset()
	for i := range 1100 {
		fmt.Fprintf(&b, "--B\r\nContent-Disposition: form-data; name=m%d\r\n\r\nv\r\n", i)
	}
	b.WriteString("--B--\r\n")
	f = parse(t, postForm("/", "multipart/form-data; boundary=B", b.String()))
	if !f.Has("m999") || f.Has("m1000") {
		t.Errorf("multipart: m999 %v, m1000 %v", f.Has("m999"), f.Has("m1000"))
	}
}

// countingReader liefert n Bytes "a" und zählt, wie viele davon abgeholt
// wurden.
type countingReader struct {
	left, taken int
}

func (c *countingReader) Read(p []byte) (int, error) {
	if c.left == 0 {
		return 0, io.EOF
	}
	n := min(len(p), c.left)
	for i := range n {
		p[i] = 'a'
	}
	c.left -= n
	c.taken += n
	return n, nil
}

// Höchstens 64 kB Rumpf (gezählt wie gesendet, auch multipart): darüber
// 413, ohne den Rest zu lesen.
func TestFormTooLarge(t *testing.T) {
	const limit = 64 << 10
	ok := func(body string, ctype string) bool {
		rec := httptest.NewRecorder()
		_, ok := ParseForm(rec, postForm("/", ctype, body))
		return ok
	}
	exact := "a=" + strings.Repeat("x", limit-2)
	if !ok(exact, formType) {
		t.Error("genau 64 kB abgelehnt")
	}
	if ok(exact+"x", formType) {
		t.Error("64 kB + 1 angenommen")
	}
	// multipart: gezählt wird der Rumpf wie gesendet, nicht die Werte.
	mpType, mpBody := multipartBody(t, "a", strings.Repeat("x", limit-300))
	if len(mpBody) > limit && ok(mpBody, mpType) || len(mpBody) <= limit && !ok(mpBody, mpType) {
		t.Errorf("multipart mit %d Bytes falsch beurteilt", len(mpBody))
	}
	mpType, mpBody = multipartBody(t, "a", strings.Repeat("x", limit-100))
	if len(mpBody) <= limit || ok(mpBody, mpType) {
		t.Errorf("multipart über 64 kB (%d Bytes) angenommen", len(mpBody))
	}
	// Auch ohne Felder (anderer Typ, andere Methode) gilt die Grenze.
	if ok(exact+"x", "text/plain") {
		t.Error("großer Rumpf mit anderem Typ angenommen")
	}

	for _, declared := range []bool{true, false} {
		t.Run(fmt.Sprintf("Länge angegeben=%v", declared), func(t *testing.T) {
			body := &countingReader{left: 10 << 20}
			r := httptest.NewRequest(http.MethodPost, "/api/status.php", body)
			r.Header.Set("Content-Type", formType)
			r.ContentLength = -1 // wie chunked: Länge unbekannt
			if declared {
				r.ContentLength = 10 << 20
			}
			rec := httptest.NewRecorder()
			f, ok := ParseForm(rec, r)
			if ok || f != nil {
				t.Fatal("10 MB angenommen")
			}
			if rec.Code != http.StatusRequestEntityTooLarge || rec.Body.Len() != 0 {
				t.Errorf("Antwort %d %q, erwartet 413 ohne Rumpf", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("Connection") != "close" {
				t.Error("413 ohne Connection: close – der Server läse den Rest des Rumpfs")
			}
			switch {
			case declared && body.taken != 0:
				t.Errorf("bei angegebener Länge %d Bytes gelesen, erwartet 0", body.taken)
			case body.taken > limit+1:
				t.Errorf("%d Bytes gelesen, höchstens 64 kB + 1 erlaubt", body.taken)
			}
		})
	}
}

// Über eine echte Verbindung: Die 413 kommt sofort, obwohl der Client erst
// einen Teil des angekündigten Rumpfs geschickt hat (ohne "Connection:
// close" läse net/http einen Rest unter 256 kB erst noch – hier bis zum
// Zeitlimit, denn er kommt nie). Bei angekündigten 10 MB schließt der
// Server gleich danach, ohne etwas vom Rest zu lesen.
func TestFormTooLargeOverConnection(t *testing.T) {
	rt := NewRouter(testCore(nil))
	rt.Handle("/api/status.php", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := ParseForm(w, r); ok {
			Text(w, 200, "angenommen")
		}
	})
	addr := serveTCP(t, rt)
	for _, declared := range []int{100 << 10, 10 << 20} {
		start := time.Now()
		a := rawBody(t, addr, fmt.Sprintf("POST /api/status.php HTTP/1.1\r\nHost: x\r\nContent-Type: %s"+
			"\r\nContent-Length: %d\r\n\r\n", formType, declared), strings.Repeat("a", 64<<10), declared > 256<<10)
		if a.status != http.StatusRequestEntityTooLarge || a.body != "" || !a.connClose {
			t.Errorf("%d Bytes angekündigt: Antwort %d %q, Connection: close %v", declared, a.status, a.body, a.connClose)
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("%d Bytes angekündigt: Antwort erst nach %v", declared, d)
		}
		if declared > 256<<10 && !a.closed {
			t.Errorf("%d Bytes angekündigt: Verbindung blieb offen", declared)
		}
	}
}

type rawBodyAnswer struct {
	status    int
	body      string
	connClose bool // die Antwort trägt "Connection: close"
	closed    bool // der Server hat die Verbindung nach der Antwort beendet
}

// rawBody schickt head (Anfragezeile und Kopfzeilen) und danach body über
// eine eigene Verbindung und liest die Antwort; checkClosed: dazu
// nachsehen, ob der Server die Verbindung danach schließt.
func rawBody(t *testing.T, addr, head, body string, checkClosed bool) rawBodyAnswer {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, head); err != nil {
		t.Fatal(err)
	}
	// Der Rumpf geht nebenher: Liest der Server nicht, darf das Schreiben
	// den Test nicht aufhalten (ein Fehler dabei ist dann erwartet).
	go func() { _, _ = io.WriteString(conn, body) }()
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatalf("Antwort: %v", err)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	a := rawBodyAnswer{status: resp.StatusCode, body: string(data), connClose: resp.Close}
	if checkClosed {
		if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		_, err = br.ReadByte()
		var ne net.Error
		a.closed = err != nil && !(errors.As(err, &ne) && ne.Timeout())
	}
	return a
}

// Ein Rumpf, der beim Lesen abbricht, wird nicht halb ausgewertet: 400.
func TestFormReadError(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", io.MultiReader(strings.NewReader("token=abc&a=1"), errReader{}))
	r.Header.Set("Content-Type", formType)
	rec := httptest.NewRecorder()
	if f, ok := ParseForm(rec, r); ok || f != nil || rec.Code != http.StatusBadRequest || rec.Body.Len() != 0 {
		t.Errorf("abgebrochener Rumpf: ok=%v, %d %q", ok, rec.Code, rec.Body.String())
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// Jeder Name und Wert ist gültiges UTF-8 (ungültige Bytes → U+FFFD).
func TestFormMakesValidUTF8(t *testing.T) {
	mpType, mpBody := multipartBody(t, "n\xffm", "w\xc3", "ok", "ä\xe2\x82")
	for name, r := range map[string]*http.Request{
		"urlencoded": postForm("/", formType, "n%FFm=w%C3&ok=%C3%A4%E2%82"),
		"multipart":  postForm("/", mpType, mpBody),
	} {
		t.Run(name, func(t *testing.T) {
			f := parse(t, r)
			if f.Get("n\uFFFDm") != "w\uFFFD" || f.Get("ok") != "ä\uFFFD" {
				t.Errorf("Felder %q", pairsOf(f))
			}
			f.Each(func(name, value string) bool {
				if !utf8.ValidString(name) || !utf8.ValidString(value) {
					t.Errorf("ungültiges UTF-8: %q=%q", name, value)
				}
				return true
			})
		})
	}
}

func TestClipBytes(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"ää", 3, "ä"},
		{"ää", 4, "ää"},
		{"ää", 1, ""},
		{"abc", 2, "ab"},
		{"abc", 10, "abc"},
		{"abc", 0, ""},
		{"abc", -1, ""},
		{"a€b", 3, "a"},
		{"a€b", 4, "a€"},
		{"😀x", 3, ""},
		{"\xff\xfeab", 2, "\xff\xfe"}, // ungültige Bytes zählen je für sich
		{"a\xe2\x82", 2, "a\xe2"},
	} {
		if got := ClipBytes(c.in, c.n); got != c.want {
			t.Errorf("ClipBytes(%q, %d) = %q, erwartet %q", c.in, c.n, got, c.want)
		}
	}
}

// Jeder Rumpf: kein Laufzeitfehler; jeder Name einmal, nicht leer, ohne
// "[", gültiges UTF-8; Get passt zu Each; höchstens 1000 Felder.
func FuzzParseForm(f *testing.F) {
	mpBody := "--XyZ\r\nContent-Disposition: form-data; name=\"a\"\r\n\r\n1\r\n--XyZ\r\n" +
		"Content-Disposition: form-data; name=\"datei\"; filename=\"x\"\r\n\r\nPNG\r\n--XyZ--\r\n"
	for _, seed := range []struct {
		multipart bool
		body      string
	}{
		{false, "token=abc&device_id=fw-box-1&nonce=1"},
		{false, "a=1&a=2&b[]=3&=4&&%zz=%&+=+"},
		{false, "n%FF=%C3&x"},
		{false, ""},
		{true, mpBody},
		{true, "--XyZ\r\nContent-Disposition: form-data; name=\"a[]\"\r\n\r\n1\r\n--XyZ--"},
		{true, "--XyZ\r\n\r\n\r\n--XyZ--"},
		{true, "kein multipart"},
	} {
		f.Add(seed.multipart, []byte(seed.body))
	}
	f.Fuzz(func(t *testing.T, isMultipart bool, body []byte) {
		ctype := formType
		if isMultipart {
			ctype = "multipart/form-data; boundary=XyZ"
		}
		r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		r.Header.Set("Content-Type", ctype)
		rec := httptest.NewRecorder()
		form, ok := ParseForm(rec, r)
		if len(body) > 64<<10 {
			if ok || rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("%d Bytes: ok=%v %d", len(body), ok, rec.Code)
			}
			return
		}
		if !ok || form == nil {
			t.Fatalf("abgelehnt: %d", rec.Code)
		}
		seen := map[string]bool{}
		n := 0
		form.Each(func(name, value string) bool {
			n++
			switch {
			case name == "" || strings.Contains(name, "["):
				t.Fatalf("Name %q", name)
			case !utf8.ValidString(name) || !utf8.ValidString(value):
				t.Fatalf("ungültiges UTF-8: %q=%q", name, value)
			case seen[name]:
				t.Fatalf("Name %q zweimal", name)
			case !form.Has(name) || form.Get(name) != value:
				t.Fatalf("Get(%q) = %q, Each %q", name, form.Get(name), value)
			}
			seen[name] = true
			return true
		})
		if n > 1000 {
			t.Fatalf("%d Felder", n)
		}
		// Ohne "%", "+" und "[" ist urlencoded ein schlichtes Zerlegen.
		if !isMultipart && !bytes.ContainsAny(body, "%+[") && utf8.Valid(body) {
			want := map[string]string{}
			for i, seg := range strings.Split(string(body), "&") {
				if i >= 1000 {
					break
				}
				name, value, _ := strings.Cut(seg, "=")
				if name != "" {
					want[name] = value
				}
			}
			if len(want) != n {
				t.Fatalf("%d Felder, erwartet %d", n, len(want))
			}
			for name, value := range want {
				if form.Get(name) != value {
					t.Fatalf("Get(%q) = %q, erwartet %q", name, form.Get(name), value)
				}
			}
		}
	})
}
