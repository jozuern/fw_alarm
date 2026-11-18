package bark

import "testing"

func TestASCII(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Größe ÄÖÜ ß", "Groesse AeOeUe ss"},
		{"àáâãå æ ç", "aaaaa ae c"},
		{"èéêë ìíîï ñ òóôõø ùúû ýÿ", "eeee iiii n ooooo uuu yy"},
		{"ÀÁÂÃÅ Æ Ç ÈÉÊË ÌÍÎÏ Ñ ÒÓÔÕØ ÙÚÛ Ý", "AAAAA Ae C EEEE IIII N OOOOO UUU Y"},
		{"šŠžŽčČćĆ łŁńśźżęą ğşıİ", "sSzZcCcC lLnszzea gsiI"},
		{"a–b—c „x“ ”y‘ ’z…", `a-b-c "x" "y' 'z...`},
		{"eins\r\nzwei\rdrei\nvier", "eins\nzwei\ndrei\nvier"},
		{"a\tb", "a b"},
		{"Text 日本 und 🔥", "Text ?? und ?"},
		{"a\x00b", "a?b"},
		{"bad \xff\xfe bytes", "bad ? bytes"},
		{"", ""},
		{"schon ascii [1] ~", "schon ascii [1] ~"},
	}
	for _, c := range cases {
		if got := ASCII(c.in); got != c.want {
			t.Errorf("ASCII(%q) = %q, soll %q", c.in, got, c.want)
		}
	}
}
