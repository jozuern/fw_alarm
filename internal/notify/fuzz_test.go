package notify

import (
	"errors"
	"testing"

	"fwalarm/internal/config"
	"fwalarm/internal/model"
)

// notify_set nimmt type, key und value aus der Anfrage. Für jede Eingabe:
// CheckSetting und Apply brechen nie ab und liefern nur ihre zwei Fehler;
// was CheckSetting annimmt, nimmt Apply an (außer einem Stellvertreter, den
// es nicht gibt), und danach gilt genau dieser Wert; was abgelehnt wird,
// ändert nichts.
func FuzzCheckSetting(f *testing.F) {
	for _, seed := range [][3]string{
		{"box_offline", "enabled", "1"}, {"box_offline", "level", "critical"}, {"global", "extra_id", "3"},
		{"global", "extra_id", "03"}, {"global", "quiet_from", "23"}, {"box_offline", "offline_after_s", "777"},
		{"global", "sound", ""}, {"login_success", "who", "readonly"}, {"", "", ""}, {"outside_watcher", "enabled", "0"},
		{"box_offline", "level", "laut"}, {"global", "extra_id", "99999999999999999999"}, {"weekly_report", "hour", "\x00"},
	} {
		f.Add(seed[0], seed[1], seed[2])
	}
	cfg := &config.Config{OfflineAfterSeconds: 180, WatchdogOfflineAfterSeconds: 777, DemoReminderAfterSeconds: 14400}
	f.Fuzz(func(t *testing.T, kind, key, value string) {
		d := model.NewState()
		d.NextID = 4
		d.Recipients = []model.Recipient{{ID: 3, Key: "annakey0001", Label: "Anna", MuteToken: "annatoken03"}}
		var invalid *InvalidValueError
		checked := CheckSetting(cfg, kind, key, value)
		if checked != nil && !errors.Is(checked, ErrUnknownSetting) && !errors.As(checked, &invalid) {
			t.Fatalf("CheckSetting: unerwarteter Fehler %v", checked)
		}
		before := resolve(cfg, d)
		ch, err := Apply(cfg, d, kind, key, value)
		if err != nil {
			if !errors.Is(err, ErrUnknownSetting) && !errors.As(err, &invalid) {
				t.Fatalf("Apply: unerwarteter Fehler %v", err)
			}
			if checked == nil && !(kind == scopeGlobal && key == "extra_id") {
				t.Fatalf("CheckSetting nimmt %q/%q=%q an, Apply nicht: %v", kind, key, value, err)
			}
			if len(d.Notify.Global) != 0 || len(d.Notify.Types) != 0 {
				t.Fatalf("abgelehnt, aber geändert: %v", d.Notify)
			}
			return
		}
		if checked != nil {
			t.Fatalf("Apply nimmt %q/%q=%q an, CheckSetting nicht: %v", kind, key, value, checked)
		}
		after := resolve(cfg, d)
		var got, old string
		switch {
		case kind == scopeGlobal:
			got, old = after.Global[key], before.Global[key]
		case key == "enabled":
			got, old = boolText(after.Types[kind].Enabled), boolText(before.Types[kind].Enabled)
		default:
			got, old = after.Types[kind].Values[key], before.Types[kind].Values[key]
		}
		if got != value {
			t.Fatalf("%q/%q=%q angenommen, es gilt aber %q", kind, key, value, got)
		}
		if ch.Changed != (old != value) || (!ch.Changed && (len(d.Notify.Global) != 0 || len(d.Notify.Types) != 0)) {
			t.Fatalf("Changed=%v bei %q → %q, gespeichert %v", ch.Changed, old, value, d.Notify)
		}
	})
}

func boolText(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
