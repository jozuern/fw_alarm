package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestText(t *testing.T) {
	rec := httptest.NewRecorder()
	Text(rec, http.StatusForbidden, "FORBIDDEN")
	if rec.Code != 403 || rec.Body.String() != "FORBIDDEN" || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("%d %q %q", rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
	}
}

// JSON schreibt wie PHPs json_encode(…, JSON_UNESCAPED_SLASHES |
// JSON_INVALID_UTF8_SUBSTITUTE): Schrägstriche und <>& roh, alles außer
// ASCII als Rückstrich-u mit vier Hex-Ziffern, kaputte Bytes als U+FFFD,
// kein Zeilenende. In den Erwartungen steht ~ für den Rückstrich.
func TestJSONLikePHP(t *testing.T) {
	cases := []struct {
		v    any
		want string
	}{
		{map[string]any{"ok": true, "message": "Gr\xc3\xbc\xc3\x9fe/<a href=\"x\">&"}, `{"message":"Gr~u00fc~u00dfe/<a href=~"x~">&","ok":true}`},
		{map[string]string{"e": string(rune(0x1F600)), "l": string(rune(0x2028)), "k": "a\xffb"}, `{"e":"~ud83d~ude00","k":"a~ufffdb","l":"~u2028"}`},
		{map[string]string{"c": "\n\t\b\f\x01"}, `{"c":"~n~t~b~f~u0001"}`},
		{[]int{}, `[]`},
		{struct {
			B string `json:"b"`
			A int    `json:"a"`
		}{"\xc3\xa4", 1}, `{"b":"~u00e4","a":1}`},
	}
	for _, tc := range cases {
		want := strings.ReplaceAll(tc.want, "~", `\`)
		rec := httptest.NewRecorder()
		JSON(rec, http.StatusConflict, tc.v)
		if rec.Code != 409 || rec.Body.String() != want || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
			t.Errorf("%q: %d %s (erwartet %s) %q", tc.v, rec.Code, rec.Body.String(), want, rec.Header().Get("Content-Type"))
		}
	}

	rec := httptest.NewRecorder()
	JSON(rec, 200, map[string]any{"kanal": make(chan int)})
	if rec.Code != 500 || rec.Body.Len() != 0 {
		t.Errorf("nicht darstellbarer Wert: %d %q", rec.Code, rec.Body.String())
	}
}
