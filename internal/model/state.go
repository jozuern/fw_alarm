// Package model hält die Dokumente des Servers (fester Zustand, flüchtige
// Laufzeitdaten, Sitzungen, Not-Kopie, Verlaufszeilen) und die reinen Regeln
// der Empfängerliste. Es importiert nichts aus diesem Modul und tut nichts
// außer rechnen: kein Netz, keine Datei, keine Uhr außer dem übergebenen
// Zeitpunkt.
//
// Die JSON-Namen der Felder sind die der PHP-Fassung (Plan "File formats"):
// Übernahme und Prüfstand verlassen sich darauf. Jedes Feld wird immer
// geschrieben (kein omitempty), damit das Geschriebene streng zurückgelesen
// und verglichen werden kann.
package model

import "encoding/json"

const (
	// Schema ist die Nummer des Dateiformats, die jedes Dokument trägt.
	Schema = 1
	// MaxRecipients: höchstens so viele Empfänger (PHP: MAX_ALARM_KEYS,
	// die Firmware nimmt nicht mehr).
	MaxRecipients = 10
	// MaxRecentTypes: so viele Befehls-Typen merkt sich der Zustand für
	// verspätete Quittungen (PHP: remember_command_type).
	MaxRecentTypes = 20
)

// Zustände eines Befehls an die Box (PHP: api/poll.php, api/ack.php,
// dashboard_api.php).
const (
	CommandPending   = "pending"
	CommandDelivered = "delivered"
	CommandAcked     = "acked"
	CommandCancelled = "cancelled"
	CommandExpired   = "expired"
)

// State ist der feste Zustand (state.json): erst gesichert, dann gültig.
type State struct {
	Schema int `json:"schema"`
	// KeysVersion und NextID sind ihre eigenen Hochwassermarken: Innerhalb
	// eines Dokuments sinken sie nie; springt das Dokument selbst zurück,
	// greift Rewind.
	KeysVersion int           `json:"keys_version"`
	NextID      int           `json:"next_id"`
	Recipients  []Recipient   `json:"recipients"`
	Demo        Demo          `json:"demo"`
	Settings    Settings      `json:"settings"`
	Notify      NotifyChoices `json:"notify"`
	Commands    Commands      `json:"commands"`
	// Outbox: Verlaufseinträge, die mit diesem Dokument gesichert werden
	// und danach in history.jsonl wandern (höchstens MaxOutbox).
	Outbox []Entry `json:"outbox"`
}

// Recipient ist ein Eintrag der Empfängerliste (PHP: alarm_keys.json).
type Recipient struct {
	ID            int    `json:"id"`
	Key           string `json:"key"`
	Label         string `json:"label"`
	Phone         string `json:"phone"`
	AddedAt       int64  `json:"added_at"`
	Muted         bool   `json:"muted"`
	MutedAt       int64  `json:"muted_at"`
	MuteVolume    int    `json:"mute_volume"`
	MutePermanent bool   `json:"mute_permanent"`
	MuteToken     string `json:"mute_token"`
}

// Demo ist der Demo-Modus (PHP: demo_mode.json).
type Demo struct {
	Enabled   bool   `json:"enabled"`
	KeyID     int    `json:"key_id"`
	Key       string `json:"key"`
	Label     string `json:"label"`
	ChangedAt int64  `json:"changed_at"`
	By        string `json:"by"`
}

// Settings sind die im Dashboard einstellbaren Werte (PHP:
// app_settings.json). MuteMaxSeconds 0 = der Wert der Konfiguration.
type Settings struct {
	MuteMaxSeconds int `json:"mute_max_seconds"`
}

// NotifyChoices hält nur, was der Admin bei den Meldungen ausdrücklich
// gewählt hat – jeden Wert als Auswahl-Schlüssel in Textform (PHP:
// notify_settings.json). Types: Art → Einstellung → Wert.
type NotifyChoices struct {
	Global map[string]string            `json:"global"`
	Types  map[string]map[string]string `json:"types"`
}

// Command ist ein Befehl an die Box – genau die Felder, die PHP in
// api/poll.php, api/ack.php und dashboard_api.php (enqueue, cancel)
// schreibt, plus expired_at aus expire_stale_pending.
type Command struct {
	ID            int    `json:"id"`
	Type          string `json:"type"`
	State         string `json:"state"`
	CreatedAt     int64  `json:"created_at"`
	CreatedBy     string `json:"created_by"`
	DemoChangedAt int64  `json:"demo_changed_at"`
	DeliveredAt   int64  `json:"delivered_at"`
	DeliveredTo   string `json:"delivered_to"`
	AckedAt       int64  `json:"acked_at"`
	AckedBy       string `json:"acked_by"`
	AckResult     string `json:"ack_result"`
	AckMessage    string `json:"ack_message"`
	CancelledAt   int64  `json:"cancelled_at"`
	CancelledBy   string `json:"cancelled_by"`
	CancelReason  string `json:"cancel_reason"`
	ExpiredAt     int64  `json:"expired_at"`
}

// RecentType merkt den Typ eines Befehls, damit eine verspätete Quittung
// ihn noch zuordnen kann (PHP: recent_types, dort id → Typ).
type RecentType struct {
	ID   int    `json:"id"`
	Type string `json:"type"`
}

// Commands: Befehlsnummern, der laufende Befehl (nil = keiner) und die
// zuletzt vergebenen Typen (höchstens MaxRecentTypes, nach id geordnet).
type Commands struct {
	NextID      int          `json:"next_id"`
	Current     *Command     `json:"current"`
	RecentTypes []RecentType `json:"recent_types"`
}

// NewState liefert den leeren Zustand einer ersten Inbetriebnahme
// (PHP: alarm_keys_default, command_state_default).
func NewState() *State {
	return (&State{Schema: Schema, NextID: 1, Commands: Commands{NextID: 1}}).Clone()
}

// Log hängt einen Verlaufseintrag an die Outbox; über MaxOutbox fallen die
// ältesten weg.
func (s *State) Log(e Entry) { s.Outbox = appendOutbox(s.Outbox, e) }

// MarshalJSON schreibt das Dokument über eine Kopie (Clone), damit eine
// fehlende Liste oder Tabelle als [] bzw. {} erscheint und nie als null –
// der strenge Leser lehnte null ab. Die Kopie liest das Original nur.
func (s State) MarshalJSON() ([]byte, error) {
	type plain State
	return json.Marshal((*plain)(s.Clone()))
}
