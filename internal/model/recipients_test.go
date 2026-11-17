package model

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// PHP: valid_bark_key, valid_mute_token, normalize_phone und die Namensregel
// von keys_add ('/^[^\p{Cc}]{1,40}$/u').
func TestTextRules(t *testing.T) {
	keys := map[string]bool{
		"abcde": true, "A-b_9": true, strings.Repeat("k", 64): true,
		"abcd": false, strings.Repeat("k", 65): false, "abc de": false, "abcdé": false, "": false,
		"abcde\n": false, // PHPs "$" ließe den Zeilenumbruch durch; hier nie
	}
	for k, want := range keys {
		if ValidKey(k) != want {
			t.Errorf("ValidKey(%q) != %v", k, want)
		}
	}
	tokens := map[string]bool{
		"abcdefgh": true, strings.Repeat("t", 32): true, "a-b_c-d_": true,
		"abcdefg": false, strings.Repeat("t", 33): false, "abcdefg=": false,
	}
	for k, want := range tokens {
		if ValidMuteToken(k) != want {
			t.Errorf("ValidMuteToken(%q) != %v", k, want)
		}
	}
	labels := map[string]bool{
		"Anna": true, "Ä": true, strings.Repeat("ö", 40): true, "Anna Müller (Zug 2)": true, "😀": true,
		"": false, strings.Repeat("ö", 41): false, "A\tB": false, "A\nB": false, "A\x7fB": false,
		"A\u0085B": false, "\xff": false,
	}
	for s, want := range labels {
		if ValidLabel(s) != want {
			t.Errorf("ValidLabel(%q) != %v", s, want)
		}
	}
	phones := []struct {
		in, out string
		ok      bool
	}{
		{"", "", true}, {"   ", "", true}, {" 0171 / 123-45 ", "0171 / 123-45", true}, {"+49 (171) 1", "+49 (171) 1", true},
		{"12", "", false}, {strings.Repeat("1", 31), "", false}, {"0171x", "", false}, {"0171\n123", "", false},
	}
	for _, p := range phones {
		out, ok := NormalizePhone(p.in)
		if out != p.out || ok != p.ok {
			t.Errorf("NormalizePhone(%q) = %q, %v", p.in, out, ok)
		}
	}
}

func TestNewMuteToken(t *testing.T) {
	a, b := NewMuteToken(), NewMuteToken()
	if len(a) != 12 || !ValidMuteToken(a) || a == b {
		t.Errorf("Tokens %q, %q", a, b)
	}
}

// MaskKey ist PHP mask_bark_key; die Beispiele hat PHP selbst berechnet:
// php -r "require 'nas_dashboard/lib/common.php'; echo mask_bark_key(…);"
func TestMaskKey(t *testing.T) {
	for in, want := range map[string]string{"abcdefgh12345": "abcd...", "ab": "ab...", "": "...", "äbcdef": "äbc..."} {
		if got := MaskKey(in); got != want {
			t.Errorf("MaskKey(%q) = %q, erwartet %q", in, got, want)
		}
	}
}

func TestEffective(t *testing.T) {
	const muteMax = 14400
	s := sampleState()
	before := mustJSON(t, s)

	t.Run("volle Liste", func(t *testing.T) {
		got := s.Effective(t0, muteMax)
		want := []Target{
			{ID: 3, Key: keyAnna, Label: "Anna"},
			{ID: 7, Key: keyBert, Label: "Bert", Muted: true, MuteVolume: 3},
			{ID: 13, Key: keyCarl, Label: "Carl", Muted: true},
		}
		if len(got) != len(want) {
			t.Fatalf("%+v", got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("Ziel %d: %+v, erwartet %+v", i, got[i], want[i])
			}
		}
	})
	t.Run("überfällig stumm zählt als laut, ohne den Zustand zu ändern", func(t *testing.T) {
		later := t0.Add(5 * time.Hour) // Bert seit 6 Std stumm, Grenze 4 Std
		got := s.Effective(later, muteMax)
		if got[1].Muted || got[1].MuteVolume != 3 {
			t.Errorf("Bert: %+v", got[1])
		}
		if !got[2].Muted {
			t.Error("Carl ist dauerhaft stumm – nie überfällig")
		}
		if !bytes.Equal(mustJSON(t, s), before) {
			t.Error("Effective hat den Zustand verändert")
		}
	})
	t.Run("ohne Grenze nie überfällig", func(t *testing.T) {
		if got := s.Effective(t0.Add(1000*time.Hour), 0); !got[1].Muted {
			t.Error("muteMax 0 = keine Rückschaltung")
		}
	})
	t.Run("Demo: nur der Demo-Empfänger, mit seinem Stumm-Zustand", func(t *testing.T) {
		d := sampleState()
		d.Demo = Demo{Enabled: true, KeyID: 7, Key: keyBert, Label: "Bert"}
		got := d.Effective(t0, muteMax)
		if len(got) != 1 || got[0] != (Target{ID: 7, Key: keyBert, Label: "Bert", Muted: true, MuteVolume: 3}) {
			t.Errorf("%+v", got)
		}
	})
	t.Run("Demo-Key nicht in der Liste: der Demo-Eintrag selbst, laut", func(t *testing.T) {
		d := sampleState()
		d.Demo = Demo{Enabled: true, KeyID: 99, Key: "testkey-weg-99", Label: "Weg"}
		got := d.Effective(t0, muteMax)
		if len(got) != 1 || got[0] != (Target{ID: 99, Key: "testkey-weg-99", Label: "Weg"}) {
			t.Errorf("%+v", got)
		}
	})
	t.Run("unplausibler Demo-Key: lieber alle als niemand", func(t *testing.T) {
		d := sampleState()
		d.Demo = Demo{Enabled: true, KeyID: 7, Key: "x"}
		if got := d.Effective(t0, muteMax); len(got) != 3 {
			t.Errorf("%+v", got)
		}
	})
	t.Run("bereinigt wie die Firmware: gültig, einmalig, höchstens 10", func(t *testing.T) {
		d := NewState()
		d.Recipients = append(d.Recipients, Recipient{ID: 1, Key: "kurz"}, Recipient{ID: 2, Key: keyAnna, MuteVolume: 15},
			Recipient{ID: 3, Key: keyAnna})
		for i := 4; i <= 15; i++ {
			d.Recipients = append(d.Recipients, Recipient{ID: i, Key: "testkey-" + strings.Repeat("z", i)})
		}
		got := d.Effective(t0, muteMax)
		if len(got) != MaxRecipients || got[0].ID != 2 || got[0].MuteVolume != 10 || got[9].ID != 12 {
			t.Errorf("%+v", got)
		}
	})
}

func TestBump(t *testing.T) {
	for _, c := range []struct{ own, box, want int }{{5, 0, 6}, {5, 41, 42}, {41, 5, 42}, {0, 0, 1}} {
		s := NewState()
		s.KeysVersion = c.own
		if got := s.Bump(c.box); got != c.want || s.KeysVersion != c.want {
			t.Errorf("eigene %d, Box %d: %d (Zustand %d), erwartet %d", c.own, c.box, got, s.KeysVersion, c.want)
		}
	}
}

// PHP allocate_key_id: über alles hinaus, was je vergeben wurde – über
// next_id und über die größte id der Liste.
func TestAllocateID(t *testing.T) {
	s := NewState()
	s.NextID = 0
	if id := s.AllocateID(); id != 1 || s.NextID != 2 {
		t.Errorf("leer: %d, next_id %d", id, s.NextID)
	}
	if id := s.AllocateID(); id != 2 || s.NextID != 3 {
		t.Errorf("zweite: %d, next_id %d", id, s.NextID)
	}
	s = sampleState() // next_id 14
	if id := s.AllocateID(); id != 14 || s.NextID != 15 {
		t.Errorf("nach next_id: %d", id)
	}
	s.NextID = 5 // hinter der größten id (13)
	if id := s.AllocateID(); id != 14 || s.NextID != 15 {
		t.Errorf("über die größte id: %d, next_id %d", id, s.NextID)
	}
}

func TestFind(t *testing.T) {
	s := sampleState()
	r := s.Find(7)
	if r == nil || r.Label != "Bert" {
		t.Fatalf("%+v", r)
	}
	r.Label = "Berta"
	if s.Recipients[1].Label != "Berta" {
		t.Error("Find liefert keinen Zeiger in die Liste")
	}
	if s.Find(4) != nil || s.Find(0) != nil {
		t.Error("unbekannte id gefunden")
	}
}
