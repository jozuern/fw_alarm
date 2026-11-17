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
		"s": 0, "t": 1, "fa": 0, "nil": 0, "fehlt": 0, "map.id": 9, "list": 0,
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
}
