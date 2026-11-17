package model

import (
	"slices"
	"time"
)

// Arbeitsmodus (Stummschaltung pro Empfänger) und das Sicherheitsnetz, das
// Stumme nach einer Höchstdauer zurück auf laut schaltet – außer bei
// "dauerhaft stumm".

// MuteIsStale: Ist diese Stummschaltung überfällig (länger als muteMax,
// nicht dauerhaft)? Eine Definition für die Leseseite (Effective) und das
// Aufräumen (ExpireStaleMutes); muteMax ≤ 0 = keine Rückschaltung (PHP:
// mute_is_stale).
func MuteIsStale(r Recipient, now time.Time, muteMax int) bool {
	if !r.Muted || r.MutePermanent || muteMax <= 0 {
		return false
	}
	return r.MutedAt <= 0 || now.Unix()-r.MutedAt > int64(muteMax)
}

// MuteMaxChoices: die angebotenen Höchstdauern in Sekunden, aufsteigend –
// feste Stufen plus der Wert der Konfiguration, damit er unverändert
// angeboten wird (PHP: mute_max_choices).
func MuteMaxChoices(cfgSeconds int) []int {
	c := []int{7200, 14400, 21600, 28800, 43200, 57600, 86400, max(0, cfgSeconds)}
	slices.Sort(c)
	return slices.Compact(c)
}

// MuteMax: wirksame Höchstdauer – die Einstellung im Dashboard, wenn sie
// gesetzt und noch eine angebotene Stufe ist, sonst die Konfiguration
// (PHP: mute_max_seconds).
func (s *State) MuteMax(cfgSeconds int) int {
	if o := s.Settings.MuteMaxSeconds; o != 0 && slices.Contains(MuteMaxChoices(cfgSeconds), o) {
		return o
	}
	return max(0, cfgSeconds)
}

// ApplyMute schaltet einen Empfänger stumm oder laut – die eine Stelle für
// alle Schalter (Dashboard, Kurzbefehl-Link, Rückschaltung). Laut beendet
// immer auch "dauerhaft stumm" (laut ist die sichere Richtung). Liefert,
// ob dabei eine dauerhafte Stummschaltung endete (PHP: apply_mute_change).
func ApplyMute(r *Recipient, muted bool, now time.Time) (permanentEnded bool) {
	wasPermanent := r.MutePermanent
	r.Muted = muted
	r.MutedAt = 0
	if muted {
		r.MutedAt = now.Unix()
	} else {
		r.MutePermanent = false
	}
	return wasPermanent && !muted
}

// ExpireStaleMutes schaltet überfällige Stummschaltungen zurück auf laut,
// erhöht dafür einmal die Listen-Version (die Box holt die Liste dann neu)
// und schreibt je Betroffenem den Eintrag key_unmuted, wie PHP
// expire_stale_mutes ihn schreibt. Liefert die Betroffenen im Stand VOR
// dem Zurückschalten (wie PHP); ist nichts überfällig, ändert sich nichts.
// boxVersion ist die keys_version des letzten Status-Pushs (0 = unbekannt):
// Wie bei PHP bump_keys_version zählt sie bei jeder Erhöhung mit.
func (s *State) ExpireStaleMutes(now time.Time, muteMax int, boxVersion int) []Recipient {
	var expired []Recipient
	for i := range s.Recipients {
		r := &s.Recipients[i]
		if !MuteIsStale(*r, now, muteMax) {
			continue
		}
		expired = append(expired, *r)
		ApplyMute(r, false, now)
	}
	if len(expired) == 0 {
		return nil
	}
	version := s.Bump(boxVersion)
	for _, r := range expired {
		s.Log(NewEntry(EvKeyUnmuted, now, map[string]any{
			"by":      "automatisch",
			"label":   r.Label,
			"key":     MaskKey(r.Key),
			"source":  "auto",
			"version": version,
		}))
	}
	return expired
}

// muteVolumeOf: Stumm-Lautstärke auf 0–10 begrenzt (PHP: mute_volume_of).
func muteVolumeOf(r Recipient) int { return min(10, max(0, r.MuteVolume)) }
