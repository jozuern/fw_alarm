// Package notify ist die EINE Stelle für Meldungen an den Admin (PHP: der
// Abschnitt "MELDUNGEN AN DEN ADMIN" in lib/common.php): welche Arten es gibt
// und was sich an ihnen einstellen lässt (Types, GlobalSpecs), wie der Admin
// sie eingestellt hat (Settings) und der Versand, durch den jede Meldung an
// den Status-Key geht (Send). Dazu die Info an einen betroffenen Empfänger
// (SendTo) und die zwei Meldungen bei der Anmeldung (login.go).
//
// Gespeichert wird nur, was der Admin ausdrücklich gewählt hat
// (model.State.Notify, je Wert der Auswahl-Schlüssel als Text); alles andere
// folgt den Standards – zwei davon sekundengenau der Konfiguration.
package notify

import (
	"cmp"
	"slices"
	"strconv"

	"fwalarm/internal/config"
	"fwalarm/internal/core"
	"fwalarm/internal/model"
	"fwalarm/internal/textde"
)

// Choice ist eine der angebotenen Möglichkeiten einer Einstellung: Value ist
// der Auswahl-Schlüssel als Text (so steht er im Zustand und im Formular),
// Text die Beschriftung.
type Choice struct{ Value, Text string }

// OptionDef ist eine Einstellung: Schlüssel, Beschriftung, die Möglichkeiten
// in der Reihenfolge der Oberfläche und der Standard (ein Value daraus).
type OptionDef struct {
	Key     string
	Label   string
	Choices []Choice
	Default string
}

// TypeDef ist eine Art von Meldung (PHP: ein Eintrag von admin_notify_types).
// Important = sicherheitsrelevant: Das Dashboard fragt vor dem Abschalten
// nach und weist auf den abgeschalteten Zustand hin. Options beginnt bei
// jeder Art mit level und ignore_quiet.
type TypeDef struct {
	Key            string
	Label          string
	Hint           string
	Important      bool
	DefaultEnabled bool
	Options        []OptionDef
}

// Arten, die an anderer Stelle beim Namen gebraucht werden.
const (
	KindBoxOffline     = "box_offline"
	KindNasWeb         = "nas_web"
	KindOutsideWatcher = "outside_watcher"
	KindLoginLockout   = "login_lockout"
	KindLoginSuccess   = "login_success"
)

// Dringlichkeit = Bark-Level (PHP: NOTIFY_LEVELS).
var levels = []Choice{
	{"passive", "leise (nur in der Mitteilungszentrale)"},
	{"active", "normal (mit Ton)"},
	{"timeSensitive", "dringend (durchbricht Fokus)"},
	{"critical", "kritisch (durchbricht auch den Stummschalter)"},
}

// LevelText ist die Beschriftung einer Dringlichkeit (für Antworten wie die
// der Probe-Meldung); eine unbekannte bleibt, wie sie ist (PHP:
// NOTIFY_LEVELS[$level] ?? $level).
func LevelText(level string) string {
	for _, c := range levels {
		if c.Value == level {
			return c.Text
		}
	}
	return level
}

// In Bark eingebaute Töne; "" = Standardton des iPhones. Wirkt erst ab
// Dringlichkeit "normal" – leise Meldungen haben keinen Ton (PHP:
// NOTIFY_SOUNDS).
var sounds = []Choice{
	{"", "Standardton"},
	{"bell", "Glocke (bell)"},
	{"chime", "Klang (chime)"},
	{"glass", "Glas (glass)"},
	{"minuet", "Menuett (minuet)"},
	{"newsflash", "Eilmeldung (newsflash)"},
	{"telegraph", "Telegraf (telegraph)"},
	{"horn", "Horn (horn)"},
	{"electronic", "Elektronisch (electronic)"},
}

// nasWebHint: der Hinweis der Art nas_web. Anders als PHP ("Login und Status
// der Box scheitern dann") nennt er, was im Go-Server wirklich ausfällt
// (Spec §7, Plan "Texts this plan fixes").
const nasWebHint = "Wenn das Dashboard nicht mehr in seinen Datenordner schreiben kann " +
	"(Änderungen werden dann nicht gespeichert, Befehle an die Box nicht ausgeliefert)."

// configWord: so heißt die Konfiguration in den Beschriftungen (PHP:
// "config.php").
const configWord = "config.toml"

// ints baut Möglichkeiten mit ganzzahligen Schlüsseln aus Paaren
// (Schlüssel, Text).
func ints(pairs ...any) []Choice {
	out := make([]Choice, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, Choice{strconv.Itoa(pairs[i].(int)), pairs[i+1].(string)})
	}
	return out
}

// hoursChoices: Beschriftungen für Wiederholungs-Abstände in Stunden, 0 =
// keine Wiederholung (PHP: notify_hours_choices).
func hoursChoices(hours ...int) []Choice {
	out := make([]Choice, 0, len(hours))
	for _, h := range hours {
		var text string
		switch {
		case h == 0:
			text = "nie (nur einmal)"
		case h == 1:
			text = "jede Stunde"
		case h == 24:
			text = "einmal pro Tag"
		case h == 168:
			text = "einmal pro Woche"
		case h%24 == 0:
			text = "alle " + strconv.Itoa(h/24) + " Tage"
		default:
			text = "alle " + strconv.Itoa(h) + " Std"
		}
		out = append(out, Choice{strconv.Itoa(h), text})
	}
	return out
}

// withDefault fügt einen Standard aus der Konfiguration SEKUNDENGENAU in eine
// Auswahl mit Sekunden als Schlüssel ein, falls er dort fehlt, und sortiert
// dann nach dem Schlüssel (PHP: notify_choices_with_default mit ksort). So
// gilt ein eigener Wert der Konfiguration unverändert weiter – er wird nicht
// auf die nächste angebotene Stufe gerundet.
func withDefault(choices []Choice, def int, prefix string) []Choice {
	value := strconv.Itoa(def)
	if slices.ContainsFunc(choices, func(c Choice) bool { return c.Value == value }) {
		return choices
	}
	text := prefix + textde.DurationLabel(def)
	if def == 0 {
		text = "sofort"
	}
	out := append(slices.Clone(choices), Choice{value, text + " (aus " + configWord + ")"})
	slices.SortStableFunc(out, func(a, b Choice) int {
		x, _ := strconv.Atoi(a.Value) // nur ganzzahlige Schlüssel (oben gebaut)
		y, _ := strconv.Atoi(b.Value)
		return cmp.Compare(x, y)
	})
	return out
}

// OutsideWatcherConfigured: Ist der Außen-Wächter eingerichtet? Erkennbar am
// Hash seines Tokens in der Konfiguration (PHP: outside_watcher_configured).
func OutsideWatcherConfigured(cfg *config.Config) bool {
	return config.TrimPHP(cfg.WatcherTokenSHA256) != ""
}

// Types liefert alle Arten von Meldungen in der Reihenfolge der Oberfläche
// (PHP: admin_notify_types) – outside_watcher nur mit eingerichtetem
// Außen-Wächter. Die Standards von offline_after_s und after_s kommen
// sekundengenau aus der Konfiguration.
func Types(c *core.Core) []TypeDef { return typesFor(c.Cfg()) }

func typesFor(cfg *config.Config) []TypeDef {
	yesNo := ints(1, "ja", 0, "nein")
	// Beide Standards SEKUNDENGENAU aus der Konfiguration (nicht gerundet):
	// Ohne gespeicherte Auswahl verhält sich der Wächter genau wie vorher.
	offlineDefault := max(1, cfg.OfflineAfterSeconds, cfg.WatchdogOfflineAfterSeconds)
	demoDefault := max(0, cfg.DemoReminderAfterSeconds)
	weekdays := ints(1, "Montag", 2, "Dienstag", 3, "Mittwoch", 4, "Donnerstag", 5, "Freitag", 6, "Samstag", 7, "Sonntag")
	hours := make([]Choice, 24)
	for h := range hours {
		hours[h] = Choice{strconv.Itoa(h), twoDigits(h) + ":00 Uhr"}
	}

	types := []TypeDef{
		{
			Key:            KindBoxOffline,
			Label:          "Box offline / wieder online",
			Hint:           "Wenn die Box zu lange keinen Status mehr schickt – dann würde ein Relaisalarm nicht ausgelöst.",
			Important:      true,
			DefaultEnabled: true,
			Options: []OptionDef{
				{Key: "offline_after_s", Label: "Als offline melden nach",
					Choices: withDefault(ints(300, "5 Min", 600, "10 Min", 900, "15 Min", 1800, "30 Min",
						3600, "1 Std", 7200, "2 Std"), offlineDefault, ""),
					Default: strconv.Itoa(offlineDefault)},
				{Key: "repeat_hours", Label: "Erinnern, solange sie offline bleibt",
					Choices: hoursChoices(0, 1, 3, 6, 12, 24), Default: "0"},
				{Key: "recovery", Label: "Auch melden, wenn sie wieder online ist", Choices: yesNo, Default: "1"},
			},
		},
		{
			Key:            KindNasWeb,
			Label:          "Dashboard defekt / wieder in Ordnung",
			Hint:           nasWebHint,
			Important:      true,
			DefaultEnabled: true,
			Options: []OptionDef{
				{Key: "repeat_hours", Label: "Erinnern, solange die Störung anhält",
					Choices: hoursChoices(0, 6, 12, 24), Default: "24"},
				{Key: "recovery", Label: "Auch melden, wenn es wieder geht", Choices: yesNo, Default: "1"},
			},
		},
		{
			Key:            KindOutsideWatcher,
			Label:          "Außen-Wächter meldet sich nicht",
			Hint:           "Wenn sich das NAS, das den Server von außen überwacht, nicht mehr meldet – z.B. weil zu Hause Strom oder Internet weg sind. Dann würde ein Ausfall des Servers selbst nicht mehr gemeldet.",
			Important:      true,
			DefaultEnabled: true,
			Options: []OptionDef{
				{Key: "silent_after_s", Label: "Melden nach",
					Choices: ints(1800, "30 Min", 3600, "1 Std", 7200, "2 Std", 21600, "6 Std"), Default: "3600"},
				{Key: "repeat_hours", Label: "Erinnern, solange er stumm bleibt",
					Choices: hoursChoices(0, 24, 72, 168), Default: "72"},
				{Key: "recovery", Label: "Auch melden, wenn er wieder da ist", Choices: yesNo, Default: "1"},
			},
		},
		{
			Key:            "demo_reminder",
			Label:          "Demo-Modus vergessen",
			Hint:           "Erinnerung, wenn der Demo-Modus länger aktiv bleibt – echte Alarme erreichen dann nur den Test-Empfänger.",
			DefaultEnabled: true,
			Options: []OptionDef{
				{Key: "after_s", Label: "Erste Erinnerung",
					Choices: withDefault(ints(3600, "nach 1 Std", 7200, "nach 2 Std", 14400, "nach 4 Std",
						28800, "nach 8 Std", 43200, "nach 12 Std", 86400, "nach 24 Std"), demoDefault, "nach "),
					Default: strconv.Itoa(demoDefault)},
				{Key: "repeat_hours", Label: "Danach wiederholen", Choices: hoursChoices(6, 12, 24), Default: "24"},
			},
		},
		{
			Key:            "mute_reminder",
			Label:          "Empfänger sind stumm",
			Hint:           "Erinnerung, solange jemand im Arbeitsmodus stumm ist.",
			DefaultEnabled: true,
			Options: []OptionDef{
				{Key: "repeat_hours", Label: "Erinnern", Choices: hoursChoices(12, 24, 48, 168), Default: "24"},
			},
		},
		{
			Key:            "permanent_mute_reminder",
			Label:          "Empfänger sind dauerhaft stumm",
			Hint:           "Erinnerung, solange jemand dauerhaft stumm geschaltet ist.",
			DefaultEnabled: true,
			Options: []OptionDef{
				{Key: "repeat_days", Label: "Erinnern",
					Choices: ints(7, "einmal pro Woche", 14, "alle 2 Wochen", 30, "einmal im Monat"), Default: "7"},
			},
		},
		{
			Key:            KindLoginLockout,
			Label:          "Login gesperrt",
			Hint:           "Wenn eine Adresse nach zu vielen falschen Anmeldeversuchen gesperrt wurde.",
			DefaultEnabled: true,
		},
		// ---- ab hier: neue, zunächst abgeschaltete Arten --------------------
		{
			Key:   KindLoginSuccess,
			Label: "Anmeldung am Dashboard",
			Hint:  "Jedes Mal, wenn sich jemand erfolgreich am Dashboard anmeldet (mit Adresse).",
			Options: []OptionDef{
				{Key: "who", Label: "Melden bei",
					Choices: []Choice{{"all", "jeder Anmeldung"}, {"readonly", "nur Lese-Benutzer"}, {"admin", "nur Admin"}},
					Default: "all"},
			},
		},
		{
			Key:   "mute_changed",
			Label: "Jemand schaltet sich stumm oder laut",
			Hint:  "Wenn ein Empfänger seinen Kurzbefehl-Link benutzt. Kann bei vielen Kollegen häufig kommen.",
		},
		{
			Key:   "mute_expired",
			Label: "Stummschaltung automatisch beendet",
			Hint:  "Wenn das Sicherheitsnetz jemanden nach Ablauf des Zeitlimits wieder laut schaltet – meist hat dann dessen Automation gehakt.",
		},
		{
			Key:   "box_restart",
			Label: "Box neu gestartet",
			Hint:  "Wenn die Box neu startet. Nach Absturz, Watchdog oder Spannungseinbruch deutet das auf ein Problem hin.",
			Options: []OptionDef{
				{Key: "scope", Label: "Melden bei",
					Choices: []Choice{{"abnormal", "nur Absturz, Watchdog, Spannungseinbruch"}, {"all", "jedem Neustart"}},
					Default: "abnormal"},
			},
		},
		{
			Key:   "box_health",
			Label: "Auffälligkeiten an der Box",
			Hint:  "Schwaches WLAN, wenig freier Speicher oder ein gescheiterter Heartbeat-Versand – bevor daraus ein Ausfall wird.",
			Options: []OptionDef{
				{Key: "repeat_hours", Label: "Höchstens", Choices: hoursChoices(6, 12, 24, 168), Default: "24"},
			},
		},
		{
			Key:   "weekly_report",
			Label: "Wochenbericht",
			Hint:  "Kurze Zusammenfassung der letzten 7 Tage: Alarme, Entwarnungen, Ausfälle, stumme Empfänger.",
			Options: []OptionDef{
				{Key: "weekday", Label: "Wochentag", Choices: weekdays, Default: "1"},
				{Key: "hour", Label: "Frühestens um", Choices: hours, Default: "8"},
			},
		},
	}

	// Ohne eingerichteten Außen-Wächter gibt es nichts zu melden – dann soll
	// die Art auch nicht als Schalter im Dashboard auftauchen.
	if !OutsideWatcherConfigured(cfg) {
		types = slices.DeleteFunc(types, func(t TypeDef) bool { return t.Key == KindOutsideWatcher })
	}

	// Für JEDE Art gleich, und zuerst: Dringlichkeit und Verhalten in der
	// Ruhezeit. Die Möglichkeiten als eigene Kopie – ändert ein Aufrufer eine,
	// träfe das sonst jede spätere Antwort (und LevelText).
	for i := range types {
		common := []OptionDef{
			{Key: "level", Label: "Dringlichkeit", Choices: slices.Clone(levels), Default: "passive"},
			{Key: "ignore_quiet", Label: "In der Ruhezeit",
				Choices: ints(0, "nur leise zustellen", 1, "wie eingestellt (auch nachts)"), Default: "0"},
		}
		types[i].Options = append(common, types[i].Options...)
	}
	return types
}

// GlobalSpecs liefert die Einstellungen, die für alle Arten gelten (PHP:
// admin_notify_global_specs). Den Stellvertreter (extra_id) wählt der Admin
// aus der gepflegten Empfängerliste – so wird hier kein weiterer Bark-Key
// eingetippt und gespeichert.
func GlobalSpecs(c *core.Core) []OptionDef { return globalSpecsFor(c.Store.State()) }

func globalSpecsFor(st *model.State) []OptionDef {
	hours := make([]Choice, 24)
	for h := range hours {
		hours[h] = Choice{strconv.Itoa(h), twoDigits(h) + ":00"}
	}
	volumes := make([]Choice, 11)
	for v := range volumes {
		volumes[v] = Choice{strconv.Itoa(v), strconv.Itoa(v)}
	}
	volumes[0].Text = "0 (lautlos)"
	extra := []Choice{{"0", "niemand"}}
	for _, r := range st.Recipients {
		if r.ID <= 0 {
			continue
		}
		// Wie ein PHP-Array: eine zweite gleiche id ersetzt den Text an ihrer Stelle.
		value := strconv.Itoa(r.ID)
		if i := slices.IndexFunc(extra, func(c Choice) bool { return c.Value == value }); i >= 0 {
			extra[i].Text = r.Label
			continue
		}
		extra = append(extra, Choice{value, r.Label})
	}
	return []OptionDef{
		{Key: "quiet_enabled", Label: "Ruhezeit", Choices: ints(0, "aus", 1, "ein"), Default: "0"},
		{Key: "quiet_from", Label: "Ruhezeit von", Choices: hours, Default: "22"},
		{Key: "quiet_to", Label: "Ruhezeit bis", Choices: hours, Default: "7"},
		{Key: "sound", Label: "Ton", Choices: slices.Clone(sounds), Default: ""}, // eigene Kopie wie levels
		{Key: "critical_volume", Label: "Lautstärke kritischer Meldungen", Choices: volumes, Default: "5"},
		{Key: "extra_id", Label: "Zusätzlich senden an", Choices: extra, Default: "0"},
	}
}

func twoDigits(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}
