package web

import "testing"

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

// phpTrim entfernt genau, was PHPs trim() entfernt.
func TestPHPTrim(t *testing.T) {
	for in, want := range map[string]string{
		" \t\n\r\x00\x0babc \t": "abc",
		"\u00a0abc\u00a0":       "\u00a0abc\u00a0", // geschütztes Leerzeichen bleibt
		"\fabc":                 "\fabc",           // Seitenvorschub bleibt
		"a b":                   "a b",
	} {
		if got := phpTrim(in); got != want {
			t.Errorf("phpTrim(%q) = %q, erwartet %q", in, got, want)
		}
	}
}
