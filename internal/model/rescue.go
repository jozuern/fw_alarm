package model

import (
	"bytes"
	"encoding/json"
)

// Rescue ist die Not-Kopie nur für den Alarm (recipients.rescue.json):
// gelesen nur im Notbetrieb, und dann nachsichtig.
type Rescue struct {
	Schema     int               `json:"schema"`
	WrittenAt  int64             `json:"written_at"`
	DemoKeyID  int               `json:"demo_key_id"` // 0 = kein Demo-Modus
	Recipients []RescueRecipient `json:"recipients"`
}

// RescueRecipient ist ein Empfänger der Not-Kopie.
type RescueRecipient struct {
	ID            int    `json:"id"`
	Key           string `json:"key"`
	Label         string `json:"label"`
	Muted         bool   `json:"muted"`
	MutedAt       int64  `json:"muted_at"`
	MuteVolume    int    `json:"mute_volume"`
	MutePermanent bool   `json:"mute_permanent"`
}

// MarshalJSON schreibt eine fehlende Liste als [] statt null.
func (r Rescue) MarshalJSON() ([]byte, error) {
	type plain Rescue
	if r.Recipients == nil {
		r.Recipients = []RescueRecipient{}
	}
	return json.Marshal(plain(r))
}

// DecodeRescueLenient liest die Not-Kopie nachsichtig – lieber die gültigen
// Einträge als keiner (Spec §6.3): Einträge, die keine Objekte sind, keinen
// gültigen Key haben oder deren Key schon vorkam, fallen weg; es zählen die
// ersten MaxRecipients gültigen. Jedes Feld wird nachsichtig gelesen wie
// ein Verlaufseintrag (Entry), die Stumm-Lautstärke auf 0–10 begrenzt.
// Unbrauchbare Daten ergeben eine leere Kopie. DemoKeyID bleibt, wie sie
// steht – ob ein Empfänger dieser id übrig ist, entscheidet der Aufrufer.
func DecodeRescueLenient(data []byte) Rescue {
	out := Rescue{Recipients: []RescueRecipient{}}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var doc any
	if dec.Decode(&doc) != nil {
		return out // kein JSON: nichts Brauchbares darin
	}
	top, ok := doc.(map[string]any)
	if !ok {
		return out
	}
	head := Entry(top)
	out.Schema = int(head.Int("schema"))
	out.WrittenAt = head.Int("written_at")
	out.DemoKeyID = int(head.Int("demo_key_id"))
	list, _ := top["recipients"].([]any) // keine Liste: keine Empfänger
	seen := make(map[string]bool, MaxRecipients)
	for _, item := range list {
		if len(out.Recipients) >= MaxRecipients {
			break
		}
		fields, ok := item.(map[string]any)
		if !ok {
			continue
		}
		e := Entry(fields)
		key := phpTrim(e.Str("key"))
		if !ValidKey(key) || seen[key] {
			continue
		}
		seen[key] = true
		out.Recipients = append(out.Recipients, RescueRecipient{
			ID:            int(e.Int("id")),
			Key:           key,
			Label:         e.Str("label"),
			Muted:         e.Bool("muted"),
			MutedAt:       e.Int("muted_at"),
			MuteVolume:    int(min(10, max(0, e.Int("mute_volume")))),
			MutePermanent: e.Bool("mute_permanent"),
		})
	}
	return out
}
