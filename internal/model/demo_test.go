package model

import (
	"testing"
	"time"
)

// PHP demo_alarm_block_reason – Sätze wörtlich, "" = erlaubt.
func TestDemoBlockReason(t *testing.T) {
	const (
		live = "Der Alarm über die Box geht nur im Demo-Modus. Im LIVE-Betrieb den REAL ALARM (direkt vom Server) benutzen."
		sync = "Die Box hat die Demo-Liste noch nicht übernommen oder ist offline – warten, bis das Demo-Banner die Übernahme bestätigt."
	)
	demo := func() *State {
		s := sampleState()
		s.Demo = Demo{Enabled: true, KeyID: 3, Key: keyAnna, Label: "Anna", ChangedAt: t0.Unix() - 60}
		return s
	}
	box := func(f ...Field) *BoxStatus {
		return &BoxStatus{SeenAt: t0.Unix() - 30, Fields: f}
	}
	synced := box(Field{"keys_version", "12"}, Field{"keys_count", "1"})
	rows := []struct {
		name string
		s    *State
		box  *BoxStatus
		now  time.Time
		want string
	}{
		{"LIVE", sampleState(), synced, t0, live},
		{"Demo mit unplausiblem Key gilt als LIVE", func() *State { s := demo(); s.Demo.Key = "x"; return s }(), synced, t0, live},
		{"übernommen und frisch", demo(), synced, t0, ""},
		{"genau an der Offline-Grenze", demo(), synced, t0.Add(150 * time.Second), ""},
		{"eine Sekunde darüber", demo(), synced, t0.Add(151 * time.Second), sync},
		{"Box nie gesehen", demo(), nil, t0, sync},
		{"andere Listen-Version", demo(), box(Field{"keys_version", "11"}, Field{"keys_count", "1"}), t0, sync},
		{"Box meldet keine Version", demo(), box(Field{"keys_count", "1"}), t0, sync},
		{"Box meldet zwei Empfänger", demo(), box(Field{"keys_version", "12"}, Field{"keys_count", "2"}), t0, sync},
		{"Box meldet keine Anzahl", demo(), box(Field{"keys_version", "12"}), t0, sync},
	}
	for _, r := range rows {
		if got := r.s.DemoBlockReason(r.box, r.now, 180); got != r.want {
			t.Errorf("%s: %q", r.name, got)
		}
	}
}

func TestDemoEnabled(t *testing.T) {
	s := sampleState()
	if s.DemoEnabled() {
		t.Error("LIVE gilt als Demo")
	}
	s.Demo = Demo{Enabled: true, KeyID: 3, Key: keyAnna}
	if !s.DemoEnabled() {
		t.Error("Demo nicht erkannt")
	}
	s.Demo.Key = "x"
	if s.DemoEnabled() {
		t.Error("unplausibler Demo-Key gilt als Demo")
	}
}
