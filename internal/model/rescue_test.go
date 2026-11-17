package model

import (
	"fmt"
	"reflect"
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

// Aus der Not-Kopie die Empfänger eines Alarms im Notbetrieb – je Satz der
// Regel ein Teil.
func TestRescueTargets(t *testing.T) {
	const muteMax = 43200 // 12 h
	anna := RescueRecipient{ID: 3, Key: keyAnna, Label: "Anna"}
	bert := RescueRecipient{ID: 7, Key: keyBert, Label: "Bert", Muted: true, MutedAt: t0.Unix() - 3600, MuteVolume: 3}
	carl := RescueRecipient{ID: 13, Key: keyCarl, Label: "Carl", MuteVolume: 99}
	tgt := func(r RescueRecipient, muted bool, vol int) Target {
		return Target{ID: r.ID, Key: r.Key, Label: r.Label, Muted: muted, MuteVolume: vol}
	}
	many := make([]RescueRecipient, 0, 12)
	for i := 1; i <= 12; i++ {
		many = append(many, RescueRecipient{ID: i, Key: fmt.Sprintf("testkey-r-%02d", i)})
	}
	type want struct {
		targets []Target
		demo    bool
		ok      bool
	}
	check := func(t *testing.T, r Rescue, max int, w want) {
		t.Helper()
		got, demo, ok := r.Targets(t0, max)
		if !reflect.DeepEqual(got, w.targets) || demo != w.demo || ok != w.ok {
			t.Errorf("%+v demo=%v ok=%v, erwartet %+v demo=%v ok=%v", got, demo, ok, w.targets, w.demo, w.ok)
		}
	}

	t.Run("gültiger Demo-Empfänger: nur er, demo", func(t *testing.T) {
		check(t, Rescue{DemoKeyID: 7, Recipients: []RescueRecipient{anna, bert, carl}}, muteMax,
			want{[]Target{tgt(bert, true, 3)}, true, true})
		// auch hinter den ersten 10 gültigen (eine Kopie aus dem Speicher kann mehr halten)
		check(t, Rescue{DemoKeyID: 12, Recipients: many}, muteMax,
			want{[]Target{{ID: 12, Key: "testkey-r-12"}}, true, true})
	})
	t.Run("kein gültiger Demo-Empfänger: alle", func(t *testing.T) {
		check(t, Rescue{DemoKeyID: 99, Recipients: []RescueRecipient{anna, bert}}, muteMax,
			want{[]Target{tgt(anna, false, 0), tgt(bert, true, 3)}, false, true})
		check(t, Rescue{DemoKeyID: 5, Recipients: []RescueRecipient{{ID: 5, Key: "kurz"}, anna}}, muteMax,
			want{[]Target{tgt(anna, false, 0)}, false, true})
	})
	t.Run("alle gültigen, einmaligen Keys in Reihenfolge, höchstens 10", func(t *testing.T) {
		in := append([]RescueRecipient{{ID: 90, Key: "kurz"}, {ID: 91, Key: "mit leer zeichen"},
			{ID: 1, Key: " testkey-r-01 "}}, many...) // getrimmt; danach ist many[0] doppelt
		got, demo, ok := Rescue{Recipients: in}.Targets(t0, muteMax)
		if len(got) != MaxRecipients || demo || !ok {
			t.Fatalf("%d Empfänger demo=%v ok=%v: %+v", len(got), demo, ok, got)
		}
		for i, tg := range got {
			if tg.ID != i+1 || tg.Key != fmt.Sprintf("testkey-r-%02d", i+1) {
				t.Errorf("Eintrag %d: %+v", i, tg)
			}
		}
	})
	t.Run("überfällig stumm zählt als laut, Stumm-Lautstärke bleibt", func(t *testing.T) {
		old := bert
		old.MutedAt = t0.Unix() - muteMax - 1
		check(t, Rescue{Recipients: []RescueRecipient{old}}, muteMax, want{[]Target{tgt(old, false, 3)}, false, true})
		check(t, Rescue{DemoKeyID: 7, Recipients: []RescueRecipient{anna, old}}, muteMax,
			want{[]Target{tgt(old, false, 3)}, true, true})
	})
	t.Run("dauerhaft stumm: nie überfällig", func(t *testing.T) {
		perm := bert
		perm.MutedAt, perm.MutePermanent = t0.Unix()-30*86400, true
		check(t, Rescue{Recipients: []RescueRecipient{perm}}, muteMax, want{[]Target{tgt(perm, true, 3)}, false, true})
	})
	t.Run("muteMax 0: nie überfällig", func(t *testing.T) {
		old := bert
		old.MutedAt = t0.Unix() - 30*86400
		check(t, Rescue{Recipients: []RescueRecipient{old}}, 0, want{[]Target{tgt(old, true, 3)}, false, true})
	})
	t.Run("genau wie MuteIsStale", func(t *testing.T) {
		for _, muted := range []bool{false, true} {
			for _, at := range []int64{0, -5, t0.Unix(), t0.Unix() - muteMax, t0.Unix() - muteMax - 1, t0.Unix() + 60} {
				for _, perm := range []bool{false, true} {
					for _, max := range []int{0, -1, muteMax} {
						rr := RescueRecipient{ID: 1, Key: keyAnna, Muted: muted, MutedAt: at, MutePermanent: perm, MuteVolume: 4}
						got, _, _ := Rescue{Recipients: []RescueRecipient{rr}}.Targets(t0, max)
						r := Recipient{Muted: muted, MutedAt: at, MutePermanent: perm}
						if wantMuted := muted && !MuteIsStale(r, t0, max); got[0].Muted != wantMuted || got[0].MuteVolume != 4 {
							t.Errorf("stumm=%v seit %d dauerhaft=%v max %d: %+v", muted, at, perm, max, got[0])
						}
					}
				}
			}
		}
	})
	t.Run("Stumm-Lautstärke begrenzt auf 0 bis 10", func(t *testing.T) {
		low := RescueRecipient{ID: 4, Key: keyBert, MuteVolume: -3}
		check(t, Rescue{Recipients: []RescueRecipient{carl, low}}, muteMax,
			want{[]Target{tgt(carl, false, 10), tgt(low, false, 0)}, false, true})
	})
	t.Run("kein gültiger Key: ok=false", func(t *testing.T) {
		check(t, Rescue{Recipients: []RescueRecipient{{ID: 1, Key: "kurz"}}}, muteMax, want{[]Target{}, false, false})
		check(t, Rescue{}, muteMax, want{[]Target{}, false, false})
		check(t, Rescue{DemoKeyID: 1, Recipients: []RescueRecipient{{ID: 1, Key: "kurz"}}}, muteMax, want{[]Target{}, false, false})
	})
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
