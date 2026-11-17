package model

import (
	"bytes"
	"slices"
	"testing"
	"time"
)

// PHP mute_is_stale: dauerhaft nie, ohne Grenze nie, ohne Zeitpunkt sofort,
// sonst erst NACH der Grenze.
func TestMuteIsStale(t *testing.T) {
	now := t0.Unix()
	rows := []struct {
		name    string
		r       Recipient
		muteMax int
		want    bool
	}{
		{"laut", Recipient{MutedAt: now - 99999}, 3600, false},
		{"dauerhaft", Recipient{Muted: true, MutePermanent: true, MutedAt: now - 99999}, 3600, false},
		{"ohne Grenze", Recipient{Muted: true, MutedAt: now - 99999}, 0, false},
		{"ohne Zeitpunkt", Recipient{Muted: true}, 3600, true},
		{"genau an der Grenze", Recipient{Muted: true, MutedAt: now - 3600}, 3600, false},
		{"eine Sekunde darüber", Recipient{Muted: true, MutedAt: now - 3601}, 3600, true},
	}
	for _, r := range rows {
		if got := MuteIsStale(r.r, t0, r.muteMax); got != r.want {
			t.Errorf("%s: %v", r.name, got)
		}
	}
}

// PHP mute_max_seconds/mute_max_choices: die Einstellung gilt nur, wenn sie
// eine der angebotenen Stufen ist; sonst die Konfiguration.
func TestMuteMax(t *testing.T) {
	if got := MuteMaxChoices(43200); !slices.Equal(got, []int{7200, 14400, 21600, 28800, 43200, 57600, 86400}) {
		t.Errorf("Stufen: %v", got)
	}
	if got := MuteMaxChoices(3600); got[0] != 3600 || len(got) != 8 {
		t.Errorf("Stufen mit eigenem Wert: %v", got)
	}
	s := NewState()
	for _, c := range []struct{ setting, cfg, want int }{
		{0, 43200, 43200}, {14400, 43200, 14400}, {3600, 43200, 43200}, {3600, 3600, 3600}, {7200, -5, 7200}, {0, -5, 0},
	} {
		s.Settings.MuteMaxSeconds = c.setting
		if got := s.MuteMax(c.cfg); got != c.want {
			t.Errorf("Einstellung %d, Konfiguration %d: %d", c.setting, c.cfg, got)
		}
	}
}

func TestApplyMute(t *testing.T) {
	r := Recipient{Muted: true, MutedAt: 5, MutePermanent: true}
	if ended := ApplyMute(&r, false, t0); !ended || r.Muted || r.MutedAt != 0 || r.MutePermanent {
		t.Errorf("laut beendet dauerhaft: %v %+v", ended, r)
	}
	r = Recipient{Muted: true, MutedAt: 5, MutePermanent: true}
	if ended := ApplyMute(&r, true, t0); ended || !r.Muted || r.MutedAt != t0.Unix() || !r.MutePermanent {
		t.Errorf("stumm lässt dauerhaft stehen: %v %+v", ended, r)
	}
	r = Recipient{}
	if ended := ApplyMute(&r, true, t0); ended || !r.Muted || r.MutedAt != t0.Unix() {
		t.Errorf("stumm: %v %+v", ended, r)
	}
	if ended := ApplyMute(&r, false, t0); ended || r.Muted || r.MutedAt != 0 {
		t.Errorf("laut ohne dauerhaft: %v %+v", ended, r)
	}
}

func TestExpireStaleMutes(t *testing.T) {
	const muteMax = 14400
	t.Run("nichts überfällig: nichts ändert sich", func(t *testing.T) {
		s := sampleState()
		before := mustJSON(t, s)
		if got := s.ExpireStaleMutes(t0, muteMax, 0); len(got) != 0 {
			t.Errorf("%+v", got)
		}
		if !bytes.Equal(mustJSON(t, s), before) {
			t.Error("Zustand verändert")
		}
	})
	t.Run("ohne Grenze: nichts", func(t *testing.T) {
		s := sampleState()
		s.Recipients[1].MutedAt = 1
		if got := s.ExpireStaleMutes(t0, 0, 0); len(got) != 0 || !s.Recipients[1].Muted {
			t.Errorf("%+v", got)
		}
	})
	t.Run("überfällige zurück auf laut", func(t *testing.T) {
		s := sampleState()
		s.Recipients[0].Muted, s.Recipients[0].MutedAt = true, t0.Unix()-5*3600
		later := t0.Add(4 * time.Hour) // Anna 9 Std, Bert 5 Std stumm, Carl dauerhaft
		outboxBefore := len(s.Outbox)
		got := s.ExpireStaleMutes(later, muteMax, 0)
		if len(got) != 2 || got[0].ID != 3 || got[1].ID != 7 {
			t.Fatalf("Betroffene: %+v", got)
		}
		if !got[0].Muted || got[1].MutedAt == 0 {
			t.Error("geliefert wird der Stand vor dem Zurückschalten (wie PHP)")
		}
		for _, i := range []int{0, 1} {
			if r := s.Recipients[i]; r.Muted || r.MutedAt != 0 || r.MutePermanent {
				t.Errorf("noch stumm: %+v", r)
			}
		}
		if !s.Recipients[2].Muted {
			t.Error("dauerhaft stumm wurde zurückgeschaltet")
		}
		if s.KeysVersion != 13 {
			t.Errorf("Version %d statt einmal +1 (13)", s.KeysVersion)
		}
		entries := s.Outbox[outboxBefore:]
		if len(entries) != 2 {
			t.Fatalf("%d Verlaufseinträge statt 2", len(entries))
		}
		// Wie PHP: log_key_event('key_unmuted', $entry, 'automatisch',
		// ['source' => 'auto', 'version' => …]).
		for i, e := range entries {
			want := map[string]string{"event": EvKeyUnmuted, "by": "automatisch", "label": got[i].Label,
				"key": got[i].Key[:4] + "...", "source": "auto", "version": "13"}
			for k, v := range want {
				if e.Str(k) != v {
					t.Errorf("Eintrag %d, %s = %q, erwartet %q", i, k, e.Str(k), v)
				}
			}
			if len(e) != len(want)+2 || e.Int("log_at") != later.Unix() || e.Str("uid") == "" {
				t.Errorf("Eintrag %d: %v", i, e)
			}
		}
		if again := s.ExpireStaleMutes(later, muteMax, 0); len(again) != 0 || s.KeysVersion != 13 {
			t.Error("zweiter Lauf hat noch etwas geändert")
		}
	})
	// Wie PHP bump_keys_version: Jede Erhöhung kennt die zuletzt von der Box
	// gemeldete Version – auch die beim Zurückschalten.
	t.Run("die Version der Box zählt mit", func(t *testing.T) {
		s := sampleState()
		s.KeysVersion = 5
		later := t0.Add(4 * time.Hour) // nur Bert überfällig
		got := s.ExpireStaleMutes(later, muteMax, 41)
		if len(got) != 1 || s.KeysVersion != 42 {
			t.Fatalf("%d Betroffene, Version %d statt 42", len(got), s.KeysVersion)
		}
		if e := s.Outbox[len(s.Outbox)-1]; e.Int("version") != 42 {
			t.Errorf("Eintrag: %v", e)
		}
	})
}
