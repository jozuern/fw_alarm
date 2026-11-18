package model

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"testing"
)

// NewEntry setzt event, log_at und uid; die Felder sind danach JSON-Werte
// – ein Eintrag im Speicher sieht aus wie einer, der von der Platte kommt.
func TestNewEntry(t *testing.T) {
	cmd := Command{ID: 7, Type: "TEST", State: CommandAcked}
	e := NewEntry(EvAcked, t0, map[string]any{
		"command": cmd, "event": "falsch", "uid": "falsch", "log_at": 1,
		"list": []string{"a", "b"}, "n": 3, "bad": "\xff", "nan": math.NaN(),
	})
	if e.Str("event") != EvAcked || e.Int("log_at") != t0.Unix() {
		t.Errorf("event/log_at: %v", e)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(e.Str("uid")) {
		t.Errorf("uid: %q", e.Str("uid"))
	}
	if other := NewEntry(EvAcked, t0, nil); other.Str("uid") == e.Str("uid") {
		t.Error("zwei Einträge mit derselben uid")
	}
	if _, ok := e["command"].(map[string]any); !ok {
		t.Errorf("command ist %T, kein JSON-Objekt", e["command"])
	}
	if e.Int("command.id") != 7 || e.Str("command.state") != CommandAcked {
		t.Errorf("Unterfeld: %v", e["command"])
	}
	if _, ok := e["list"].([]any); !ok {
		t.Errorf("list ist %T", e["list"])
	}
	if e.Str("bad") != "�" {
		t.Errorf("ungültiges UTF-8: %q", e.Str("bad"))
	}
	if e.Str("nan") != "NaN" {
		t.Errorf("NaN als Text: %q", e.Str("nan"))
	}
	// Im Speicher wie von der Platte: einmal schreiben und lesen ändert nichts.
	b := mustJSON(t, e)
	var back Entry
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&back); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mustJSON(t, back), b) {
		t.Errorf("anders nach dem Lesen:\n%s\n%s", b, mustJSON(t, back))
	}
}

// Die Leser sind nachsichtig wie PHPs (string)/(int)/!empty.
func TestEntryAccessors(t *testing.T) {
	e := Entry{
		"s": "Text", "n": json.Number("42"), "f": 3.9, "i": 5, "i64": int64(6),
		"ns": "17", "lead": " 12abc", "neg": "-3", "fs": "2.7", "exp": "1e3",
		"t": true, "fa": false, "one": "1", "zero": "0", "empty": "", "zn": json.Number("0"),
		"map":      map[string]any{"id": json.Number("9"), "deep": map[string]any{"x": "y"}},
		"emptymap": map[string]any{}, "list": []any{1}, "nil": nil,
		"dotted.key": "direkt",
	}
	strs := map[string]string{
		"s": "Text", "n": "42", "f": "3.9", "i": "5", "i64": "6", "t": "1", "fa": "", "nil": "", "fehlt": "",
		"map": "", "list": "", "map.id": "9", "map.deep.x": "y", "map.fehlt": "", "s.x": "", "dotted.key": "direkt",
	}
	for k, want := range strs {
		if got := e.Str(k); got != want {
			t.Errorf("Str(%q) = %q, erwartet %q", k, got, want)
		}
	}
	ints := map[string]int64{
		"n": 42, "f": 3, "i": 5, "i64": 6, "ns": 17, "lead": 12, "neg": -3, "fs": 2, "exp": 1000,
		"s": 0, "t": 1, "fa": 0, "nil": 0, "fehlt": 0, "map.id": 9, "list": 1, "emptymap": 0, "map": 1,
	}
	for k, want := range ints {
		if got := e.Int(k); got != want {
			t.Errorf("Int(%q) = %d, erwartet %d", k, got, want)
		}
	}
	bools := map[string]bool{
		"t": true, "one": true, "n": true, "s": true, "f": true, "map": true, "list": true,
		"fa": false, "zero": false, "empty": false, "zn": false, "nil": false, "fehlt": false, "emptymap": false,
	}
	for k, want := range bools {
		if got := e.Bool(k); got != want {
			t.Errorf("Bool(%q) = %v, erwartet %v", k, got, want)
		}
	}
	// Dieselben Regeln für einen Wert ohne Eintrag (etwa einen Parameter
	// der Anfrage).
	for k, v := range e {
		if StrOf(v) != e.Str(k) || IntOf(v) != e.Int(k) || BoolOf(v) != e.Bool(k) {
			t.Errorf("%q: StrOf/IntOf/BoolOf weichen von Str/Int/Bool ab", k)
		}
	}
	if IntOf(" 25x") != 25 || IntOf("abc") != 0 || IntOf(nil) != 0 || StrOf(nil) != "" || BoolOf(nil) {
		t.Error("Werte ohne Eintrag")
	}
}

// Zahlen als Text genau wie PHP: eine JSON-Zahl so, wie PHP sie nach
// json_decode mit (string) schreibt – eine ganze Zahl, die in int64 passt,
// bleibt ihre Ziffernfolge, alles andere ist ein float (precision 14,
// zend_gcvt). Die Erwartungen sind die Ausgabe von PHP 8.3 selbst
// (aufgenommen am 18.11.2026, vorher in logview, TestViewTypes).
func TestStrOfNumbersLikePHP(t *testing.T) {
	cases := []struct{ json, want string }{
		{"500", "500"}, {"1.0", "1"}, {"1e3", "1000"}, {"1.5", "1.5"}, {"0.30000000000000004", "0.3"},
		{"1e20", "1.0E+20"}, {"1e-5", "1.0E-5"}, {"-1.5e-7", "-1.5E-7"}, {"0.0001", "0.0001"}, {"0.001", "0.001"},
		{"1e14", "1.0E+14"}, {"12345678901234.5", "12345678901234"}, {"123456789012345.0", "1.2345678901234E+14"},
		{"99999999999999.99", "1.0E+14"}, {"1.23456789012345678", "1.2345678901235"},
		{"1234567890123456789012", "1.2345678901235E+21"}, {"9223372036854775807", "9223372036854775807"},
		{"9223372036854775808", "9.2233720368548E+18"}, {"-9223372036854775808", "-9223372036854775808"},
		{"1.5e300", "1.5E+300"}, {"2.5e-300", "2.5E-300"}, {"-0.0", "-0"}, {"-0", "0"}, {"1e400", "INF"},
		{"-1e400", "-INF"}, {"5e-324", "4.9406564584125E-324"},
	}
	for _, tc := range cases {
		if got := StrOf(json.Number(tc.json)); got != tc.want {
			t.Errorf("StrOf(%s) = %q, erwartet %q", tc.json, got, tc.want)
		}
	}
	// Eine Kommazahl, die nicht aus JSON kommt, ebenso.
	for f, want := range map[float64]string{3.9: "3.9", 1e20: "1.0E+20", 7: "7", math.Inf(1): "INF", math.NaN(): "NAN"} {
		if got := StrOf(f); got != want {
			t.Errorf("StrOf(%v) = %q, erwartet %q", f, got, want)
		}
	}
}

// (int) auf ein Array: 1, wenn es etwas enthält, sonst 0 – wie PHP.
func TestIntOfArraysLikePHP(t *testing.T) {
	cases := []struct {
		v    any
		want int64
	}{
		{[]any{json.Number("5")}, 1}, {[]any{}, 0}, {map[string]any{"a": json.Number("1")}, 1}, {map[string]any{}, 0},
	}
	for _, tc := range cases {
		if got := IntOf(tc.v); got != tc.want {
			t.Errorf("IntOf(%v) = %d, erwartet %d", tc.v, got, tc.want)
		}
	}
}
