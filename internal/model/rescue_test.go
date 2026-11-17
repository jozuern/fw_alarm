package model

import (
	"fmt"
	"strings"
	"testing"
)

// Die Not-Kopie nachsichtig: lieber die gültigen Einträge als keiner.
func TestRescueLenient(t *testing.T) {
	var items []string
	items = append(items, `"kein Objekt"`, `17`, `null`, `["liste"]`,
		`{"id": 1, "key": "kurz", "label": "zu kurz"}`,
		`{"id": 2, "key": " testkey-r-02 ", "label": "Zwei", "muted": "1", "muted_at": "5", "mute_volume": 99, "mute_permanent": 1}`,
		`{"id": 3, "key": "testkey-r-02", "label": "doppelt"}`,
		`{"id": "4", "label": "ohne Key"}`)
	for i := 5; i <= 16; i++ { // 12 gültige – nur die ersten 9 passen noch
		items = append(items, fmt.Sprintf(`{"id": %d, "key": "testkey-r-%02d", "label": "N%d", "unbekannt": [1]}`, i, i, i))
	}
	text := `{"schema": 1, "written_at": 77, "demo_key_id": "6", "recipients": [` + strings.Join(items, ",") + `]}`
	got := DecodeRescueLenient([]byte(text))
	if got.Schema != 1 || got.WrittenAt != 77 || got.DemoKeyID != 6 {
		t.Errorf("Kopf: %+v", got)
	}
	if len(got.Recipients) != MaxRecipients {
		t.Fatalf("%d Empfänger: %+v", len(got.Recipients), got.Recipients)
	}
	first := got.Recipients[0]
	if first != (RescueRecipient{ID: 2, Key: "testkey-r-02", Label: "Zwei", Muted: true, MutedAt: 5, MuteVolume: 10, MutePermanent: true}) {
		t.Errorf("erster: %+v", first)
	}
	for i, r := range got.Recipients[1:] {
		if r.ID != i+5 || r.Key != fmt.Sprintf("testkey-r-%02d", i+5) {
			t.Errorf("Eintrag %d: %+v", i+1, r)
		}
	}

	for _, garbage := range []string{"", "{", "nichts", "[]", `"text"`, `{"recipients": "keine Liste"}`, `{"recipients": null}`} {
		r := DecodeRescueLenient([]byte(garbage))
		if r.Recipients == nil || len(r.Recipients) != 0 {
			t.Errorf("%q: %+v", garbage, r)
		}
	}
}

// Was der Server schreibt, liest der nachsichtige Leser unverändert.
func TestRescueRoundTrip(t *testing.T) {
	in := Rescue{Schema: Schema, WrittenAt: 9, DemoKeyID: 3, Recipients: []RescueRecipient{
		{ID: 3, Key: keyAnna, Label: "Anna"},
		{ID: 7, Key: keyBert, Label: "Bert", Muted: true, MutedAt: 4, MuteVolume: 3, MutePermanent: true},
	}}
	b := mustJSON(t, in)
	out := DecodeRescueLenient(b)
	if string(mustJSON(t, out)) != string(b) {
		t.Errorf("verändert:\n%s\n%s", b, mustJSON(t, out))
	}
}
