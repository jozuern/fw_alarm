package model

import (
	"bytes"
	"encoding/json"
	"time"
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

// RescueFrom baut die Not-Kopie aus dem festen Zustand (Spec §6.3): je
// Empfänger id, Key, Name und Stumm-Zustand – nie Telefonnummer oder
// Stumm-Token –, dazu der Demo-Empfänger, wenn der Demo-Modus an ist; now ist
// der Zeitpunkt des Schreibens. Was Targets daraus macht, ist genau das, was
// Effective aus dem Zustand macht (TestRescueFrom).
func RescueFrom(st *State, now time.Time) Rescue {
	r := Rescue{Schema: Schema, WrittenAt: now.Unix(), Recipients: make([]RescueRecipient, 0, len(st.Recipients))}
	if st.Demo.Enabled {
		r.DemoKeyID = st.Demo.KeyID
	}
	for _, x := range st.Recipients {
		r.Recipients = append(r.Recipients, RescueRecipient{
			ID:            x.ID,
			Key:           x.Key,
			Label:         x.Label,
			Muted:         x.Muted,
			MutedAt:       x.MutedAt,
			MuteVolume:    x.MuteVolume,
			MutePermanent: x.MutePermanent,
		})
	}
	return r
}

// Targets liefert die Empfänger, an die ein Alarm im Notbetrieb aus dieser
// Not-Kopie ginge (Spec §6.3) – die Regel gehört hierher, Zeit und
// Höchstdauer kennt der Aufrufer:
//   - Steht ein gültiger Demo-Empfänger in der Kopie (DemoKeyID eines
//     gültigen, einmaligen Eintrags), nur er, und demo = true.
//   - Sonst alle gültigen, einmaligen Keys in ihrer Reihenfolge, höchstens
//     MaxRecipients – lieber die gültigen Einträge als keiner; ein
//     Demo-Verweis ins Leere alarmiert lieber alle als niemanden.
//   - Eine überfällige Stummschaltung zählt als laut, genau wie bei
//     MuteIsStale (dauerhaft = nie überfällig, muteMax ≤ 0 = nie); die
//     Stumm-Lautstärke bleibt (auf 0–10 begrenzt).
//   - ok = false, wenn kein gültiger Key übrig bleibt.
func (r Rescue) Targets(now time.Time, muteMax int) (targets []Target, demo bool, ok bool) {
	list := make([]Target, 0, min(len(r.Recipients), MaxRecipients))
	seen := make(map[string]bool, len(r.Recipients))
	for _, rr := range r.Recipients {
		key := phpTrim(rr.Key)
		if !ValidKey(key) || seen[key] {
			continue
		}
		seen[key] = true
		stale := MuteIsStale(Recipient{Muted: rr.Muted, MutedAt: rr.MutedAt, MutePermanent: rr.MutePermanent}, now, muteMax)
		tg := Target{
			ID:         rr.ID,
			Key:        key,
			Label:      rr.Label,
			Muted:      rr.Muted && !stale,
			MuteVolume: min(10, max(0, rr.MuteVolume)),
		}
		if r.DemoKeyID != 0 && rr.ID == r.DemoKeyID {
			return []Target{tg}, true, true
		}
		if len(list) < MaxRecipients {
			list = append(list, tg)
		}
	}
	return list, false, len(list) > 0
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
