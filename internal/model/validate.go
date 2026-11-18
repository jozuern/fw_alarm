package model

import "fmt"

// Validate prüft die Regeln, die jeder gesicherte Zustand erfüllt – eine
// Liste mit ungültigen Einträgen lässt sich nicht speichern (Spec §7).
// Die Meldungen nennen Positionen und ids, nie Keys oder Tokens.
func (s *State) Validate() error {
	if n := len(s.Recipients); n > MaxRecipients {
		return fmt.Errorf("%d Empfänger – höchstens %d möglich", n, MaxRecipients)
	}
	keys := make(map[string]bool, len(s.Recipients))
	ids := make(map[int]bool, len(s.Recipients))
	tokens := make(map[string]bool, len(s.Recipients))
	for i, r := range s.Recipients {
		where := fmt.Sprintf("Empfänger %d (id %d)", i+1, r.ID)
		switch {
		case !ValidKey(r.Key):
			return fmt.Errorf("%s: Bark-Key ungültig", where)
		case keys[r.Key]:
			return fmt.Errorf("%s: Bark-Key doppelt", where)
		case r.ID <= 0:
			return fmt.Errorf("%s: id muss größer als 0 sein", where)
		case r.ID >= s.NextID:
			return fmt.Errorf("%s: id nicht kleiner als next_id (%d)", where, s.NextID)
		case ids[r.ID]:
			return fmt.Errorf("%s: id doppelt", where)
		case !ValidMuteToken(r.MuteToken):
			return fmt.Errorf("%s: Stumm-Token ungültig", where)
		case tokens[r.MuteToken]:
			return fmt.Errorf("%s: Stumm-Token doppelt", where)
		case r.MutePermanent && !r.Muted:
			return fmt.Errorf("%s: dauerhaft stumm, aber nicht stumm", where)
		case r.MuteVolume < 0 || r.MuteVolume > 10:
			return fmt.Errorf("%s: Stumm-Lautstärke %d außerhalb von 0 bis 10", where, r.MuteVolume)
		}
		keys[r.Key], ids[r.ID], tokens[r.MuteToken] = true, true, true
	}
	if s.Demo.Enabled {
		r := s.Find(s.Demo.KeyID)
		if r == nil {
			return fmt.Errorf("Demo-Modus: Empfänger id %d steht nicht in der Liste", s.Demo.KeyID)
		}
		if r.Key != s.Demo.Key {
			return fmt.Errorf("Demo-Modus: Key passt nicht zum Empfänger id %d", s.Demo.KeyID)
		}
	}
	// Jeder Verlaufseintrag braucht eine uid: An ihr erkennt die Ablage, was
	// schon im Verlauf steht (sonst fielen Einträge ohne uid zusammen).
	for i, e := range s.Outbox {
		if uid, ok := e["uid"].(string); !ok || uid == "" {
			return fmt.Errorf("Verlaufseintrag %d der outbox ohne uid", i+1)
		}
	}
	if c := s.Commands.Current; c != nil && c.ID >= s.Commands.NextID {
		return fmt.Errorf("Befehl %d: Nummer nicht kleiner als commands.next_id (%d)", c.ID, s.Commands.NextID)
	}
	if n := len(s.Commands.RecentTypes); n > MaxRecentTypes {
		return fmt.Errorf("%d gemerkte Befehls-Typen – höchstens %d", n, MaxRecentTypes)
	}
	for i := 1; i < len(s.Commands.RecentTypes); i++ {
		if prev, cur := s.Commands.RecentTypes[i-1].ID, s.Commands.RecentTypes[i].ID; cur <= prev {
			return fmt.Errorf("gemerkte Befehls-Typen nicht aufsteigend nach id (Eintrag %d: id %d nach id %d)", i+1, cur, prev)
		}
	}
	return nil
}
