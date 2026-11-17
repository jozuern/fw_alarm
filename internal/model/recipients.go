package model

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Target ist ein Empfänger, an den JETZT alarmiert würde (PHP: ein Eintrag
// von effective_alarm_entries).
type Target struct {
	ID         int
	Key        string
	Label      string
	Muted      bool
	MuteVolume int
}

// ValidKey: Bark-Keys sind 5–64 Zeichen aus A-Z a-z 0-9 _ - (PHP:
// valid_bark_key, wie die Firmware). Anders als PHPs "$" lässt das keinen
// Zeilenumbruch am Ende durch.
func ValidKey(k string) bool { return tokenChars(k, 5, 64) }

// ValidMuteToken: 8–32 Zeichen aus A-Z a-z 0-9 _ - (PHP: valid_mute_token).
func ValidMuteToken(t string) bool { return tokenChars(t, 8, 32) }

func tokenChars(s string, minLen, maxLen int) bool {
	if len(s) < minLen || len(s) > maxLen {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// ValidLabel: Name eines Empfängers – 1–40 Zeichen, gültiges UTF-8, keine
// Steuerzeichen (PHP keys_add: '/^[^\p{Cc}]{1,40}$/u'). Der Aufrufer
// schneidet vorher Leerraum ab wie PHPs trim().
func ValidLabel(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	n := 0
	for _, r := range s {
		if unicode.Is(unicode.Cc, r) {
			return false
		}
		n++
	}
	return n >= 1 && n <= 40
}

// NormalizePhone: Telefonnummer eines Empfängers (nur Anzeige). Liefert die
// bereinigte Nummer ("" = keine) und ob sie gültig ist: 3–30 Zeichen aus
// Ziffern, + - / ( ) und Leerzeichen (PHP: normalize_phone).
func NormalizePhone(s string) (string, bool) {
	p := phpTrim(s)
	if p == "" {
		return "", true
	}
	if len(p) < 3 || len(p) > 30 {
		return "", false
	}
	for i := range len(p) {
		if !strings.ContainsRune("0123456789+-/() ", rune(p[i])) {
			return "", false
		}
	}
	return p, true
}

// NewMuteToken: 9 Zufallsbytes als 12 URL-sichere Zeichen (72 Bit; PHP:
// new_mute_token).
func NewMuteToken() string {
	var b [9]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read scheitert nie (Go ≥ 1.24 bricht sonst das Programm ab)
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// Effective liefert die Empfänger, an die JETZT alarmiert würde (PHP:
// effective_alarm_entries) – für den Alarm des Servers und die Liste der
// Box gleich. Bereinigt wie die Firmware: nur gültige Keys, keine
// Doppelten, höchstens MaxRecipients. Überfällige Stummschaltungen zählen
// schon als laut, ohne den Zustand zu ändern. Im Demo-Modus nur der
// Demo-Empfänger (mit seinem Stumm-Zustand); bei einem unplausiblen
// Demo-Key lieber die volle Liste als gar keine Alarmierung.
func (s *State) Effective(now time.Time, muteMax int) []Target {
	list := make([]Target, 0, min(len(s.Recipients), MaxRecipients))
	byKey := make(map[string]int, len(s.Recipients))
	for _, r := range s.Recipients {
		key := phpTrim(r.Key)
		if !ValidKey(key) {
			continue
		}
		if _, dup := byKey[key]; dup {
			continue
		}
		if len(list) >= MaxRecipients {
			break
		}
		byKey[key] = len(list)
		list = append(list, Target{
			ID:         r.ID,
			Key:        key,
			Label:      r.Label,
			Muted:      r.Muted && !MuteIsStale(r, now, muteMax),
			MuteVolume: muteVolumeOf(r),
		})
	}
	if s.Demo.Enabled {
		key := phpTrim(s.Demo.Key)
		if ValidKey(key) {
			if i, ok := byKey[key]; ok {
				return []Target{list[i]}
			}
			return []Target{{ID: s.Demo.KeyID, Key: key, Label: s.Demo.Label}}
		}
	}
	return list
}

// Bump erhöht die Listen-Version über die eigene und die zuletzt von der
// Box gemeldete hinaus (PHP: bump_keys_version; die Hochwassermarke ist der
// Zustand selbst). Liefert die neue Version.
func (s *State) Bump(boxVersion int) int {
	s.KeysVersion = max(s.KeysVersion, boxVersion) + 1
	return s.KeysVersion
}

// AllocateID vergibt die id für einen neuen Empfänger – über alles hinaus,
// was je vergeben wurde (PHP: allocate_key_id).
func (s *State) AllocateID() int {
	maxID := 0
	for _, r := range s.Recipients {
		maxID = max(maxID, r.ID)
	}
	id := max(1, s.NextID, maxID+1)
	s.NextID = id + 1
	return id
}

// Find liefert den Empfänger mit dieser id (zum Ändern im Entwurf) oder nil
// (PHP: find_key_index).
func (s *State) Find(id int) *Recipient {
	for i := range s.Recipients {
		if s.Recipients[i].ID == id {
			return &s.Recipients[i]
		}
	}
	return nil
}

// phpTrim entfernt, was PHPs trim() entfernt: " \t\n\r\0\x0B".
func phpTrim(s string) string { return strings.Trim(s, " \t\n\r\x00\x0b") }

// MaskKey: nur die ersten 4 Bytes eines Keys plus "..." – Keys sind
// Geheimnisse (PHP: mask_bark_key, dort substr, also Bytes). Die einzige
// Fassung dieser Regel; bark.MaskKey reicht nur hierher weiter.
func MaskKey(k string) string {
	if len(k) > 4 {
		k = k[:4]
	}
	return k + "..."
}
