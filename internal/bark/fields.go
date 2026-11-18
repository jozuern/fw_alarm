package bark

import (
	"strconv"
	"time"

	"fwalarm/internal/config"
	"fwalarm/internal/model"
)

// MaskKey zeigt nur die ersten 4 Zeichen eines Keys (eine Fassung: model).
func MaskKey(k string) string { return model.MaskKey(k) }

func clampVolume(v int) string { return strconv.Itoa(max(0, min(10, v))) }

// AlarmFields sind die Bark-Felder des Alarms für EINEN Empfänger
// (PHP: bark_alarm_fields). Stumm (Arbeitsmodus): gleicher Critical Alert mit
// der Stumm-Lautstärke und ohne call=1.
func AlarmFields(cfg *config.Config, body string, muted bool, muteVolume int) map[string]string {
	f := map[string]string{
		"title": cfg.BarkAlarmTitle,
		"body":  body,
		"sound": cfg.BarkAlarmSound,
		"level": "critical",
	}
	if muted {
		f["volume"] = clampVolume(muteVolume)
	} else {
		f["volume"] = clampVolume(cfg.BarkAlarmVolume)
		if cfg.BarkAlarmCall {
			f["call"] = "1"
		}
	}
	return f
}

// FalseAlarmFields sind die Felder der Entwarnung für EINEN Empfänger
// (PHP: bark_false_alarm_fields): Critical Alert mit dem kurzen Ton der
// Entwarnung, nie call=1. Die Lautstärke ohne eigenen Eintrag ist die des
// Alarms (das löst config beim Laden auf).
func FalseAlarmFields(cfg *config.Config, title, body string, muted bool, muteVolume int) map[string]string {
	f := map[string]string{
		"title": title,
		"body":  body,
		"sound": cfg.BarkFalseAlarmSound,
		"level": "critical",
	}
	if muted {
		f["volume"] = clampVolume(muteVolume)
	} else {
		f["volume"] = clampVolume(cfg.BarkFalseAlarmVolume)
	}
	return f
}

// TestFields sind die Felder des Test-Pushs an EINEN Empfänger
// (PHP: bark_send_test): normale Mitteilung (level=active, kein Critical
// Alert, kein call) mit dem Alarmton. Die Uhrzeit steht in der Zeitzone der
// Konfiguration. Bark-Texte ASCII.
func TestFields(cfg *config.Config, now time.Time) map[string]string {
	return map[string]string{
		"title": "TEST - kein Alarm",
		"body": now.In(cfg.Location()).Format("[02.01.2006 15:04] ") +
			"Test-Nachricht vom Alarm-Waechter. " +
			"Hoerst du dabei den Alarmton, ist dein iPhone richtig eingerichtet.",
		"sound": cfg.BarkAlarmSound,
		"level": "active",
	}
}
