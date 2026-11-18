package bark

import (
	"testing"
	"time"

	"fwalarm/internal/config"
	"fwalarm/internal/model"
)

func testCfg() *config.Config {
	return &config.Config{
		BarkAlarmTitle: "FEUERWEHR-ALARM", BarkAlarmSound: "alarm_fw",
		BarkAlarmVolume: 10, BarkAlarmCall: true,
		BarkFalseAlarmSound: "glass", BarkFalseAlarmVolume: 10,
		Timezone: "Europe/Berlin",
	}
}

func eq(t *testing.T, got, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("Felder %v, soll %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("Feld %s = %q, soll %q (alle: %v)", k, got[k], v, got)
		}
	}
}

func TestAlarmFields(t *testing.T) {
	cfg := testCfg()
	eq(t, AlarmFields(cfg, "Text", false, 3), map[string]string{
		"title": "FEUERWEHR-ALARM", "body": "Text", "sound": "alarm_fw",
		"level": "critical", "volume": "10", "call": "1"})
	eq(t, AlarmFields(cfg, "Text", true, 3), map[string]string{
		"title": "FEUERWEHR-ALARM", "body": "Text", "sound": "alarm_fw",
		"level": "critical", "volume": "3"})
	cfg.BarkAlarmCall = false
	if _, ok := AlarmFields(cfg, "T", false, 0)["call"]; ok {
		t.Error("bark_alarm_call=false: kein call")
	}
	// Klemmen auf 0..10.
	cfg.BarkAlarmVolume = 99
	if v := AlarmFields(cfg, "T", false, 0)["volume"]; v != "10" {
		t.Errorf("volume = %s", v)
	}
	if v := AlarmFields(cfg, "T", true, -4)["volume"]; v != "0" {
		t.Errorf("volume = %s", v)
	}
	if v := AlarmFields(cfg, "T", true, 40)["volume"]; v != "10" {
		t.Errorf("volume = %s", v)
	}
}

func TestFalseAlarmFields(t *testing.T) {
	cfg := testCfg()
	cfg.BarkAlarmVolume = 7
	cfg.BarkFalseAlarmVolume = 7 // ohne Eintrag: wie der Alarm (das übernimmt config)
	got := FalseAlarmFields(cfg, "ENTWARNUNG", "Fehlalarm", false, 2)
	eq(t, got, map[string]string{"title": "ENTWARNUNG", "body": "Fehlalarm",
		"sound": "glass", "level": "critical", "volume": "7"})
	if _, ok := got["call"]; ok {
		t.Error("Entwarnung nie mit call")
	}
	eq(t, FalseAlarmFields(cfg, "E", "F", true, 2), map[string]string{"title": "E", "body": "F",
		"sound": "glass", "level": "critical", "volume": "2"})
	cfg.BarkFalseAlarmVolume = 4
	if v := FalseAlarmFields(cfg, "E", "F", false, 0)["volume"]; v != "4" {
		t.Errorf("volume = %s", v)
	}
	cfg.BarkFalseAlarmVolume = 55
	if v := FalseAlarmFields(cfg, "E", "F", false, 0)["volume"]; v != "10" {
		t.Errorf("volume = %s", v)
	}
}

func TestTestFields(t *testing.T) {
	cfg := testCfg()
	now := time.Date(2026, 11, 18, 21, 5, 0, 0, time.UTC) // 22:05 in Berlin
	eq(t, TestFields(cfg, now), map[string]string{
		"title": "TEST - kein Alarm",
		"body": "[18.11.2026 22:05] Test-Nachricht vom Alarm-Waechter. " +
			"Hoerst du dabei den Alarmton, ist dein iPhone richtig eingerichtet.",
		"sound": "alarm_fw", "level": "active"})
}

func TestMaskKey(t *testing.T) {
	for _, k := range []string{"", "ab", "abcd", "abcdef1234567", "ÄÖÜabcdef"} {
		if MaskKey(k) != model.MaskKey(k) {
			t.Errorf("MaskKey(%q) weicht ab", k)
		}
	}
	if MaskKey("abcdef1234") != "abcd..." {
		t.Error("Maske")
	}
}
