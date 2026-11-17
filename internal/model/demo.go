package model

import "time"

// Sätze von PHP demo_alarm_block_reason, wörtlich.
const (
	demoBlockLive = "Der Alarm über die Box geht nur im Demo-Modus. Im LIVE-Betrieb den REAL ALARM (direkt vom Server) benutzen."
	demoBlockSync = "Die Box hat die Demo-Liste noch nicht übernommen oder ist offline – warten, bis das Demo-Banner die Übernahme bestätigt."
)

// DemoEnabled: Geht im Demo-Modus WIRKLICH alles nur an den Demo-Key? Nur
// bei gesetztem Schalter UND plausiblem Key – sonst alarmiert Effective die
// volle Liste (PHP: demo_state_enabled).
func (s *State) DemoEnabled() bool {
	return s.Demo.Enabled && ValidKey(phpTrim(s.Demo.Key))
}

// DemoBlockReason: Darf ein Demo-Alarm über die Box laufen? Nur im
// Demo-Modus und nur, wenn der letzte Status der Box die aktuelle
// Listen-Version bestätigt, genau EINEN Empfänger meldet und nicht älter
// als offlineAfter Sekunden ist – sonst könnte er noch an die volle Liste
// gehen. "" = erlaubt, sonst der Grund (PHP: demo_alarm_block_reason).
// box nil = die Box hat sich nie gemeldet.
func (s *State) DemoBlockReason(box *BoxStatus, now time.Time, offlineAfter int) string {
	if !s.DemoEnabled() {
		return demoBlockLive
	}
	boxVersion, keysCount, seenAt := int64(-1), int64(0), int64(0)
	if box != nil {
		if v, ok := box.Fields.find("keys_version"); ok {
			boxVersion = phpInt(v)
		}
		keysCount = phpInt(box.Fields.Get("keys_count"))
		seenAt = box.SeenAt
	}
	if boxVersion != int64(s.KeysVersion) || keysCount != 1 || now.Unix()-seenAt > int64(offlineAfter) {
		return demoBlockSync
	}
	return ""
}
