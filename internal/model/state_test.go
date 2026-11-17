package model

import (
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// Die Felder eines Befehls laut Plan ("File formats") – genau die, die PHP
// in api/poll.php, api/ack.php und dashboard_api.php schreibt.
var commandFields = []string{
	"id", "type", "state", "created_at", "created_by", "demo_changed_at", "delivered_at", "delivered_to",
	"acked_at", "acked_by", "ack_result", "ack_message", "cancelled_at", "cancelled_by", "cancel_reason",
	"expired_at",
}

func jsonNames(t reflect.Type) []string {
	var out []string
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		out = append(out, name)
	}
	return out
}

func TestCommandFieldsMatchPHP(t *testing.T) {
	if got := jsonNames(reflect.TypeFor[Command]()); !slices.Equal(got, commandFields) {
		t.Errorf("Felder von Command:\n%v\nerwartet:\n%v", got, commandFields)
	}
	// Gegenprobe an den PHP-Quellen, solange es sie gibt (Aufgabe 37
	// entfernt sie): jedes Feld, das PHP einem Befehl zuweist, steht in der
	// Liste – und jedes der Liste kommt dort vor.
	var src strings.Builder
	for _, name := range []string{"api/poll.php", "api/ack.php", "dashboard_api.php", "lib/common.php"} {
		b, err := os.ReadFile("../../nas_dashboard/" + name)
		if os.IsNotExist(err) {
			t.Skip("PHP-Quellen nicht mehr vorhanden")
		}
		if err != nil {
			t.Fatal(err)
		}
		src.Write(b)
	}
	php := map[string]bool{}
	for _, m := range regexp.MustCompile(`\$(?:current|command)\['([a-z_]+)'\]\s*=`).FindAllStringSubmatch(src.String(), -1) {
		php[m[1]] = true
	}
	block := regexp.MustCompile(`(?s)\$command = \[(.*?)\];`).FindStringSubmatch(src.String())
	if block == nil {
		t.Fatal("enqueue-Block in dashboard_api.php nicht gefunden")
	}
	for _, m := range regexp.MustCompile(`'([a-z_]+)' =>`).FindAllStringSubmatch(block[1], -1) {
		php[m[1]] = true
	}
	for name := range php {
		if !slices.Contains(commandFields, name) {
			t.Errorf("PHP schreibt %q, der Plan kennt es nicht", name)
		}
	}
	for _, name := range commandFields {
		if !php[name] {
			t.Errorf("%q schreibt PHP nicht", name)
		}
	}
}

// Eine Konstante je Ereignis aus PHP log_event_types(), plus die zwei neuen.
func TestEventsMatchPHP(t *testing.T) {
	seen := map[string]bool{}
	for _, ev := range AllEvents {
		if seen[ev] {
			t.Errorf("doppelt in AllEvents: %s", ev)
		}
		seen[ev] = true
	}
	if len(AllEvents) != 36 {
		t.Errorf("%d Ereignisse statt 34 + 2", len(AllEvents))
	}
	if !seen[EvStateRewound] || !seen[EvSendInterrupted] {
		t.Error("die neuen Ereignisse fehlen")
	}
	b, err := os.ReadFile("../../nas_dashboard/lib/common.php")
	if os.IsNotExist(err) {
		t.Skip("PHP-Quellen nicht mehr vorhanden")
	}
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)\r?\n    \$types = \[\r?\n(.*?)\r?\n    \];`).FindSubmatch(b)
	if block == nil {
		t.Fatal("log_event_types() nicht gefunden")
	}
	var php []string
	for _, m := range regexp.MustCompile(`(?m)^ {8}'([a-z_]+)' =>`).FindAllSubmatch(block[1], -1) {
		php = append(php, string(m[1]))
	}
	want := append(slices.Clone(php), EvStateRewound, EvSendInterrupted)
	if !slices.Equal(AllEvents, want) {
		t.Errorf("AllEvents:\n%v\nPHP (+ neue):\n%v", AllEvents, want)
	}
}

// Der 501. Eintrag verdrängt den ältesten – in beiden Dokumenten.
func TestOutboxCap(t *testing.T) {
	s := NewState()
	r := NewRuntime()
	for i := range MaxOutbox + 1 {
		at := t0.Add(time.Duration(i) * time.Second)
		s.Log(NewEntry(EvKeyTest, at, map[string]any{"n": i}))
		r.Log(NewEntry(EvKeyTest, at, map[string]any{"n": i}))
	}
	for name, box := range map[string][]Entry{"State": s.Outbox, "Runtime": r.Outbox} {
		if len(box) != MaxOutbox {
			t.Errorf("%s: %d Einträge statt %d", name, len(box), MaxOutbox)
			continue
		}
		if first, last := box[0].Int("n"), box[len(box)-1].Int("n"); first != 1 || last != MaxOutbox {
			t.Errorf("%s: erster %d, letzter %d", name, first, last)
		}
	}
}

// Neue Dokumente tragen das Schema und lesen sich zurück.
func TestNewDocuments(t *testing.T) {
	s := NewState()
	if s.Schema != Schema || s.NextID != 1 || s.Commands.NextID != 1 || s.KeysVersion != 0 {
		t.Errorf("leerer Zustand: %+v", s)
	}
	if _, err := DecodeState(mustJSON(t, s)); err != nil {
		t.Error(err)
	}
	if _, err := DecodeRuntime(mustJSON(t, NewRuntime())); err != nil {
		t.Error(err)
	}
	if _, err := DecodeSessions(mustJSON(t, NewSessions())); err != nil {
		t.Error(err)
	}
	for _, doc := range []any{sampleState(), sampleRuntime(), sampleSessions()} {
		b := mustJSON(t, doc)
		var err error
		switch doc.(type) {
		case *State:
			_, err = DecodeState(b)
		case *Runtime:
			_, err = DecodeRuntime(b)
		case *Sessions:
			_, err = DecodeSessions(b)
		}
		if err != nil {
			t.Errorf("%T: %v\n%s", doc, err, b)
		}
	}
}
