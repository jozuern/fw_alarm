package model

import "encoding/json"

// Sessions sind die Sitzungen des Dashboards (sessions.json), Schlüssel =
// SHA-256 (hex) der Sitzungs-ID – die ID selbst steht nirgends.
type Sessions struct {
	Schema   int                `json:"schema"`
	Sessions map[string]Session `json:"sessions"`
}

// Session ist eine angemeldete Sitzung.
type Session struct {
	User      string `json:"user"`
	Role      string `json:"role"`
	CSRF      string `json:"csrf"`
	AuthStamp string `json:"auth_stamp"`
	CreatedAt int64  `json:"created_at"`
	LastSeen  int64  `json:"last_seen"`
}

// NewSessions liefert eine leere Sitzungsablage.
func NewSessions() *Sessions { return (&Sessions{Schema: Schema}).Clone() }

// MarshalJSON schreibt über eine Kopie, nie null für die Tabelle (wie
// State.MarshalJSON).
func (s Sessions) MarshalJSON() ([]byte, error) {
	type plain Sessions
	return json.Marshal((*plain)(s.Clone()))
}
