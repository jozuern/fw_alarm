package model

import "time"

// RewindSource: woraus ein Start aufsetzt, der nicht auf der zuletzt selbst
// geschriebenen state.json beruht.
type RewindSource string

const (
	RewindPrev    RewindSource = "prev"    // Rückfall auf state.prev.json
	RewindRestore RewindSource = "restore" // zurückgespielte Sicherung (fw-alarm state-bump)
	RewindImport  RewindSource = "import"  // erneute Übernahme der PHP-Daten
)

// RewindJump: um so viel springen Listen-Version, id-Zähler und
// Befehlsnummer bei einem Rücksprung.
const RewindJump = 1000

// Rewind wendet die Rücksprung-Regel an (Spec §6.3): Box oder Browser
// könnten Nummern kennen, die der Server sonst mit anderem Inhalt noch
// einmal vergäbe. Deshalb springen keys_version, next_id und
// commands.next_id um RewindJump; ein wartender oder ausgelieferter, nicht
// quittierter Befehl wird abgebrochen (sonst führte ihn die Box ein zweites
// Mal aus); der Eintrag state_rewound kommt in die Outbox.
func (s *State) Rewind(src RewindSource, now time.Time) {
	s.KeysVersion += RewindJump
	s.NextID += RewindJump
	s.Commands.NextID += RewindJump
	if c := s.Commands.Current; c != nil && (c.State == CommandPending || c.State == CommandDelivered) {
		cancelled := *c
		cancelled.State = CommandCancelled
		cancelled.CancelledAt = now.Unix()
		cancelled.CancelledBy = "system"
		cancelled.CancelReason = "rewind"
		s.Commands.Current = &cancelled
	}
	s.Log(NewEntry(EvStateRewound, now, map[string]any{"source": string(src), "by": "system"}))
}
