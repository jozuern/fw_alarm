package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// Je eine Zeile pro Regel von Validate: Der verdorbene Zustand lässt sich
// weder sichern (Validate) noch, so geschrieben, wieder lesen (DecodeState).
func TestInvalidListCannotBeSaved(t *testing.T) {
	if err := sampleState().Validate(); err != nil {
		t.Fatalf("gültiger Zustand abgelehnt: %v", err)
	}
	eleven := func(s *State) {
		s.Recipients = nil
		for i := 1; i <= 11; i++ {
			s.Recipients = append(s.Recipients, Recipient{ID: i, Key: "testkey-" + strings.Repeat("x", i),
				Label: "n", MuteToken: "tok-" + strings.Repeat("y", 8+i)})
		}
		s.NextID = 12
	}
	rows := []struct {
		name   string
		spoil  func(s *State)
		phrase string // Teil der Fehlermeldung
	}{
		{"mehr als 10 Empfänger", eleven, "10"},
		{"Key zu kurz", func(s *State) { s.Recipients[0].Key = "abcd" }, "Key"},
		{"Key mit Leerzeichen", func(s *State) { s.Recipients[0].Key = "testkey anna" }, "Key"},
		{"Key mit Zeilenumbruch am Ende", func(s *State) { s.Recipients[0].Key = keyAnna + "\n" }, "Key"},
		{"Key doppelt", func(s *State) { s.Recipients[1].Key = keyAnna }, "Key"},
		{"id 0", func(s *State) { s.Recipients[0].ID = 0 }, "id"},
		{"id negativ", func(s *State) { s.Recipients[0].ID = -3 }, "id"},
		{"id doppelt", func(s *State) { s.Recipients[1].ID = 3 }, "id"},
		{"id gleich next_id", func(s *State) { s.Recipients[2].ID = 14 }, "next_id"},
		{"id über next_id", func(s *State) { s.NextID = 10 }, "next_id"},
		{"Stumm-Token ungültig", func(s *State) { s.Recipients[0].MuteToken = "kurz" }, "Token"},
		{"Stumm-Token fehlt", func(s *State) { s.Recipients[0].MuteToken = "" }, "Token"},
		{"Stumm-Token doppelt", func(s *State) { s.Recipients[1].MuteToken = tokenAnna }, "Token"},
		{"dauerhaft ohne stumm", func(s *State) { s.Recipients[0].MutePermanent = true }, "dauerhaft"},
		{"Stumm-Lautstärke 11", func(s *State) { s.Recipients[0].MuteVolume = 11 }, "Lautstärke"},
		{"Stumm-Lautstärke -1", func(s *State) { s.Recipients[0].MuteVolume = -1 }, "Lautstärke"},
		{"Demo-Empfänger fehlt", func(s *State) {
			s.Demo = Demo{Enabled: true, KeyID: 99, Key: keyAnna, Label: "Anna"}
		}, "Demo"},
		{"Demo-Key passt nicht zur id", func(s *State) {
			s.Demo = Demo{Enabled: true, KeyID: 3, Key: keyBert, Label: "Anna"}
		}, "Demo"},
		{"Befehlsnummer gleich next_id", func(s *State) { s.Commands.Current.ID = s.Commands.NextID }, "Befehl"},
		{"Befehlsnummer über next_id", func(s *State) { s.Commands.NextID = 2 }, "Befehl"},
		{"mehr als 20 Befehls-Typen", func(s *State) {
			s.Commands.RecentTypes = nil
			for i := 1; i <= 21; i++ {
				s.Commands.RecentTypes = append(s.Commands.RecentTypes, RecentType{ID: i, Type: "TEST"})
			}
			s.Commands.NextID = 30
			s.Commands.Current.ID = 25
		}, "20"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			s := sampleState()
			r.spoil(s)
			err := s.Validate()
			if err == nil {
				t.Fatal("ungültiger Zustand angenommen")
			}
			if !strings.Contains(err.Error(), r.phrase) {
				t.Errorf("Meldung ohne %q: %v", r.phrase, err)
			}
			for _, secret := range []string{keyAnna, keyBert, tokenAnna, tokenBert} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("Meldung verrät ein Geheimnis: %v", err)
				}
			}
			b, mErr := json.Marshal(s)
			if mErr != nil {
				t.Fatal(mErr)
			}
			if _, err := DecodeState(b); err == nil {
				t.Error("so geschrieben, wäre er wieder lesbar")
			}
		})
	}
	// Grenzen, die gelten: genau 10 Empfänger, Lautstärke 0 und 10, Demo
	// auf einen Empfänger der Liste, dauerhaft stumm mit stumm.
	ok := sampleState()
	eleven(ok)
	ok.Recipients = ok.Recipients[:10]
	ok.Recipients[0].MuteVolume = 10
	ok.Recipients[1].Muted, ok.Recipients[1].MutePermanent = true, true
	ok.Demo = Demo{Enabled: true, KeyID: 2, Key: ok.Recipients[1].Key}
	if err := ok.Validate(); err != nil {
		t.Errorf("Grenzfall abgelehnt: %v", err)
	}
}
