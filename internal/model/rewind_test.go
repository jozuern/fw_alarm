package model

import "testing"

// Rücksprung-Regel (Spec §6.3): Nummern +1000, ein nicht quittierter Befehl
// wird abgebrochen, Eintrag state_rewound.
func TestRewind(t *testing.T) {
	for _, src := range []RewindSource{RewindPrev, RewindRestore, RewindImport} {
		for _, state := range []string{CommandPending, CommandDelivered, CommandAcked, CommandCancelled, CommandExpired, ""} {
			s := sampleState() // keys_version 12, next_id 14, commands.next_id 8
			if state == "" {
				s.Commands.Current = nil
			} else {
				s.Commands.Current.State = state
			}
			before := s.Commands.Current
			var old Command
			if before != nil {
				old = *before
			}
			outbox := len(s.Outbox)
			s.Rewind(src, t0)
			if s.KeysVersion != 1012 || s.NextID != 1014 || s.Commands.NextID != 1008 {
				t.Errorf("%s/%s: Nummern %d/%d/%d", src, state, s.KeysVersion, s.NextID, s.Commands.NextID)
			}
			cur := s.Commands.Current
			switch state {
			case CommandPending, CommandDelivered:
				want := old
				want.State, want.CancelledAt, want.CancelledBy, want.CancelReason = CommandCancelled, t0.Unix(), "system", "rewind"
				if cur == nil || *cur != want {
					t.Errorf("%s/%s: Befehl %+v", src, state, cur)
				}
			case "":
				if cur != nil {
					t.Errorf("%s: Befehl aus dem Nichts", src)
				}
			default:
				if cur == nil || *cur != old {
					t.Errorf("%s/%s: Befehl verändert: %+v", src, state, cur)
				}
			}
			if err := s.Validate(); err != nil {
				t.Errorf("%s/%s: danach ungültig: %v", src, state, err)
			}
			added := s.Outbox[outbox:]
			if len(added) != 1 {
				t.Fatalf("%s/%s: %d Einträge statt 1", src, state, len(added))
			}
			e := added[0]
			if e.Str("event") != EvStateRewound || e.Str("source") != string(src) || e.Str("by") != "system" ||
				e.Int("log_at") != t0.Unix() {
				t.Errorf("%s/%s: Eintrag %v", src, state, e)
			}
		}
	}
}
