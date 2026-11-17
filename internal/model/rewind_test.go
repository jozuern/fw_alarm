package model

import "testing"

// Rücksprung-Regel (Spec §6.3): Nummern +1000, ein nicht quittierter Befehl
// wird abgebrochen – mit dem Eintrag cancelled wie bei jedem Abbruch –,
// danach der Eintrag state_rewound.
func TestRewind(t *testing.T) {
	const reason = "Stand des Servers zurückgesetzt – nicht ausgeliefert."
	if RewindCancelReason != reason {
		t.Errorf("Grund: %q", RewindCancelReason)
	}
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
			if err := s.Validate(); err != nil {
				t.Errorf("%s/%s: danach ungültig: %v", src, state, err)
			}
			cur := s.Commands.Current
			added := s.Outbox[outbox:]
			cancels := state == CommandPending || state == CommandDelivered
			switch {
			case cancels:
				want := old
				want.State, want.CancelledAt, want.CancelledBy, want.CancelReason = CommandCancelled, t0.Unix(), "system", reason
				if cur == nil || *cur != want {
					t.Errorf("%s/%s: Befehl %+v", src, state, cur)
				}
				if len(added) != 2 {
					t.Fatalf("%s/%s: %d Einträge statt 2", src, state, len(added))
				}
				// Wie PHP: append_command_log(['event' => 'cancelled',
				// 'command' => $current]) – im Verlauf zeigt log_event_types()
				// davon id, type, by (command.cancelled_by) und message
				// (command.cancel_reason).
				c := added[0]
				if len(c) != 4 || c.Str("event") != EvCancelled || c.Int("log_at") != t0.Unix() || c.Str("uid") == "" {
					t.Errorf("%s/%s: Abbruch-Eintrag %v", src, state, c)
				}
				if c.Int("command.id") != int64(old.ID) || c.Str("command.type") != old.Type ||
					c.Str("command.state") != CommandCancelled || c.Str("command.cancelled_by") != "system" ||
					c.Str("command.cancel_reason") != reason || c.Int("command.cancelled_at") != t0.Unix() ||
					c.Str("command.created_by") != old.CreatedBy {
					t.Errorf("%s/%s: Befehl im Eintrag %v", src, state, c["command"])
				}
				added = added[1:]
			case state == "":
				if cur != nil {
					t.Errorf("%s: Befehl aus dem Nichts", src)
				}
			default:
				if cur == nil || *cur != old {
					t.Errorf("%s/%s: Befehl verändert: %+v", src, state, cur)
				}
			}
			if len(added) != 1 {
				t.Fatalf("%s/%s: %d Einträge nach dem Abbruch statt nur state_rewound", src, state, len(added))
			}
			e := added[0]
			if e.Str("event") != EvStateRewound || e.Str("source") != string(src) || e.Str("by") != "system" ||
				e.Int("log_at") != t0.Unix() {
				t.Errorf("%s/%s: Eintrag %v", src, state, e)
			}
		}
	}
}
