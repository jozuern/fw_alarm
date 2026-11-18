package notify

import (
	"errors"
	"strconv"
	"time"

	"fwalarm/internal/config"
	"fwalarm/internal/core"
	"fwalarm/internal/model"
)

// Resolved ist der vollständige, geprüfte Stand aller Einstellungen (PHP:
// load_notify_settings): jede Einstellung mit einem ihrer angebotenen Werte
// (Auswahl-Schlüssel als Text), nie leer, nie unbekannt.
type Resolved struct {
	Global map[string]string       // Einstellung → Wert (quiet_enabled, quiet_from …)
	Types  map[string]KindSettings // Art → ihre Einstellungen; nur Arten aus Types
}

// KindSettings sind die Einstellungen einer Art: ein/aus und je Option der
// Wert (level, ignore_quiet und die eigenen der Art).
type KindSettings struct {
	Enabled bool
	Values  map[string]string
}

// Int liest einen Wert als Zahl (Sekunden, Stunden, Tage …); 0, wenn es ihn
// nicht gibt oder er keine Zahl ist.
func (k KindSettings) Int(key string) int {
	n, _ := strconv.Atoi(k.Values[key]) // die Auswahl-Schlüssel dieser Werte sind Zahlen
	return n
}

// Settings liefert den geltenden Stand: Für jede Einstellung zählt der
// gespeicherte Wert, wenn er einer der angebotenen ist, sonst der Standard –
// so folgt alles nicht ausdrücklich Gesetzte weiter den Standards und der
// Konfiguration (sekundengenau), und ein Wert, den es nicht mehr gibt (etwa
// der frühere Standard aus der Konfiguration oder ein gelöschter
// Stellvertreter), fällt still auf den Standard. Gelesen wird nur aus dem
// Speicher.
func Settings(c *core.Core) Resolved { return resolve(c.Cfg(), c.Store.State()) }

// Snapshot liefert Arten, globale Einstellungen und den geltenden Stand aus
// EINEM Stand von Konfiguration und Zustand – für eine Antwort, in der alle
// drei zusammenpassen müssen (notify_list: jeder Wert ist eine der gezeigten
// Möglichkeiten).
func Snapshot(c *core.Core) (types []TypeDef, global []OptionDef, set Resolved) {
	cfg, st := c.Cfg(), c.Store.State()
	return typesFor(cfg), globalSpecsFor(st), resolve(cfg, st)
}

// resolve: Settings für einen Stand der Konfiguration und einen des Zustands
// – rechnet nur (auch für einen Entwurf innerhalb von Store.Update).
func resolve(cfg *config.Config, st *model.State) Resolved {
	out := Resolved{Global: map[string]string{}, Types: map[string]KindSettings{}}
	for _, spec := range globalSpecsFor(st) {
		out.Global[spec.Key] = pick(spec, st.Notify.Global[spec.Key])
	}
	for _, def := range typesFor(cfg) {
		row := st.Notify.Types[def.Key]
		ks := KindSettings{Enabled: def.DefaultEnabled, Values: map[string]string{}}
		// Gespeichert ist "1" oder "0" (PHP: true/false); alles andere ist kein
		// angebotener Wert und gilt als nicht gesetzt.
		switch row["enabled"] {
		case "1":
			ks.Enabled = true
		case "0":
			ks.Enabled = false
		}
		for _, spec := range def.Options {
			ks.Values[spec.Key] = pick(spec, row[spec.Key])
		}
		out.Types[def.Key] = ks
	}
	return out
}

// pick: der gespeicherte Wert, wenn er einer der angebotenen ist (ein
// fehlender Eintrag ist ""), sonst der Standard (PHP: notify_pick).
func pick(spec OptionDef, stored string) string {
	for _, c := range spec.Choices {
		if c.Value == stored {
			return stored
		}
	}
	return spec.Default
}

// lookupKind sucht eine Art in einer Tabelle von typesFor.
func lookupKind(types []TypeDef, kind string) (TypeDef, bool) {
	for _, t := range types {
		if t.Key == kind {
			return t, true
		}
	}
	return TypeDef{}, false
}

// Enabled: Ist die Art eingeschaltet? Eine unbekannte Art (oder
// outside_watcher ohne eingerichteten Außen-Wächter) ist es nie (PHP:
// admin_notify_enabled).
func Enabled(c *core.Core, kind string) bool { return Settings(c).Types[kind].Enabled }

// ImportantFlags: Sind die sicherheitsrelevanten Arten an? Für den
// Status-Abruf des Dashboards (alle 5 s je Tab) – liest nur, was gespeichert
// ist, statt die ganze Tabelle aufzubauen. Alle drei sind ab Werk an: Nur ein
// ausdrücklich gespeichertes "aus" zählt (PHP: important_notify_flags;
// outside_watcher wertet der Aufrufer nur aus, wenn der Außen-Wächter
// eingerichtet ist).
func ImportantFlags(c *core.Core) (boxOffline, nasWeb, outsideWatcher bool) {
	types := c.Store.State().Notify.Types
	on := func(kind string) bool { return types[kind]["enabled"] != "0" }
	return on(KindBoxOffline), on(KindNasWeb), on(KindOutsideWatcher)
}

// InQuietHours: Liegt now in der Ruhezeit? Gerechnet in vollen Stunden der
// Zeitzone von now – der Aufrufer gibt now in der Zeitzone der Konfiguration
// (so entscheidet auch an den Tagen der Zeitumstellung die Ortszeit). "von 22
// bis 7" läuft über Mitternacht; gleiche Anfangs- und Endzeit = keine
// Ruhezeit (PHP: notify_in_quiet_hours).
func InQuietHours(global map[string]string, now time.Time) bool {
	if on := global["quiet_enabled"]; on == "" || on == "0" {
		return false
	}
	from, _ := strconv.Atoi(global["quiet_from"]) // Auswahl-Schlüssel 0–23
	to, _ := strconv.Atoi(global["quiet_to"])
	if from == to {
		return false
	}
	hour := now.Hour()
	if from < to {
		return hour >= from && hour < to
	}
	return hour >= from || hour < to
}

// ErrUnknownSetting: Art oder Einstellung gibt es nicht (der Text ist die
// Antwort des Dashboards).
var ErrUnknownSetting = errors.New("Unbekannte Einstellung.")

// InvalidValueError: Der Wert ist keiner der angebotenen; Label ist die
// Beschriftung der Einstellung (der Text ist die Antwort des Dashboards).
type InvalidValueError struct{ Label string }

func (e *InvalidValueError) Error() string {
	return "Ungültiger Wert für „" + e.Label + "“ – nichts geändert."
}

// enabledSpec: ein/aus einer Art, wie notify_set sie sieht (Standard ohne
// Bedeutung – ob eine Art ab Werk an ist, sagt TypeDef.DefaultEnabled).
var enabledSpec = OptionDef{Key: "enabled", Label: "Meldung",
	Choices: []Choice{{"1", "eingeschaltet"}, {"0", "abgeschaltet"}}, Default: "0"}

// scopeGlobal: die "Art" der Einstellungen für alle Meldungen.
const scopeGlobal = "global"

// CheckSetting prüft eine Einstellung, bevor sie gespeichert wird (PHP:
// notify_set; dazu die Übernahme der PHP-Daten): kind ist eine Art aus Types
// (für diese Konfiguration) oder "global", key eine ihrer Einstellungen
// ("enabled" = Art ein/aus), value einer der angebotenen Werte, genau so
// geschrieben. Sonst ErrUnknownSetting bzw. *InvalidValueError – nie still
// der Standard.
//
// Der Stellvertreter (global/extra_id) hängt an der Empfängerliste, die hier
// nicht vorliegt: CheckSetting nimmt "0" und jede id in Normalform (ohne
// führende Null) an; ob es sie gibt, prüft Apply am Zustand. Ein
// gespeicherter Stellvertreter, den es nicht mehr gibt, gilt ohnehin als
// "niemand" (Settings).
func CheckSetting(cfg *config.Config, kind, key, value string) error {
	spec, _, err := specFor(cfg, nil, kind, key)
	if err != nil {
		return err
	}
	if kind == scopeGlobal && key == "extra_id" {
		if value == "0" || isID(value) {
			return nil
		}
		return &InvalidValueError{Label: spec.Label}
	}
	if !offered(spec, value) {
		return &InvalidValueError{Label: spec.Label}
	}
	return nil
}

// specFor sucht die Einstellung kind/key; scope ist die Beschriftung für
// Antwort und Verlauf ("Alle Meldungen" oder die der Art). st nil = ohne
// Empfängerliste (extra_id bietet dann nur "niemand").
func specFor(cfg *config.Config, st *model.State, kind, key string) (spec OptionDef, scope string, err error) {
	if kind == scopeGlobal {
		if st == nil {
			st = &model.State{}
		}
		for _, s := range globalSpecsFor(st) {
			if s.Key == key {
				return s, "Alle Meldungen", nil
			}
		}
		return OptionDef{}, "", ErrUnknownSetting
	}
	def, ok := lookupKind(typesFor(cfg), kind)
	if !ok {
		return OptionDef{}, "", ErrUnknownSetting
	}
	if key == "enabled" {
		return enabledSpec, def.Label, nil
	}
	for _, s := range def.Options {
		if s.Key == key {
			return s, def.Label, nil
		}
	}
	return OptionDef{}, "", ErrUnknownSetting
}

func offered(spec OptionDef, value string) bool {
	for _, c := range spec.Choices {
		if c.Value == value {
			return true
		}
	}
	return false
}

// isID: eine id in Normalform – Ziffern ohne führende Null, als int lesbar.
func isID(s string) bool {
	if s == "" || s[0] == '0' {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	_, err := strconv.Atoi(s)
	return err == nil
}

// Change beschreibt eine Änderung durch Apply – für Antwort und Verlauf:
// Scope ist die Beschriftung der Art (bzw. "Alle Meldungen"), Option die der
// Einstellung, ValueText die des neuen Werts.
type Change struct {
	Changed                  bool
	Scope, Option, ValueText string
}

// Apply setzt im Entwurf d die Einstellung kind/key auf value (PHP:
// notify_set innerhalb der Sperre) und rechnet dabei nur – für den Rückruf
// von Store.Update. Gilt der Wert schon (gespeichert oder als Standard),
// ändert Apply nichts (Changed false): Gespeichert wird nur, was der Admin
// ausdrücklich anders wählt; alles andere folgt weiter den Standards. Prüft
// wie CheckSetting, den Stellvertreter zusätzlich gegen die Empfängerliste
// in d.
func Apply(cfg *config.Config, d *model.State, kind, key, value string) (Change, error) {
	spec, scope, err := specFor(cfg, d, kind, key)
	if err != nil {
		return Change{}, err
	}
	if !offered(spec, value) {
		return Change{}, &InvalidValueError{Label: spec.Label}
	}
	ch := Change{Scope: scope, Option: spec.Label}
	for _, c := range spec.Choices {
		if c.Value == value {
			ch.ValueText = c.Text
		}
	}
	cur := resolve(cfg, d)
	var old string
	switch {
	case kind == scopeGlobal:
		old = cur.Global[key]
	case key == "enabled":
		old = "0"
		if cur.Types[kind].Enabled {
			old = "1"
		}
	default:
		old = cur.Types[kind].Values[key]
	}
	if old == value {
		return ch, nil
	}
	if kind == scopeGlobal {
		if d.Notify.Global == nil {
			d.Notify.Global = map[string]string{}
		}
		d.Notify.Global[key] = value
	} else {
		if d.Notify.Types == nil {
			d.Notify.Types = map[string]map[string]string{}
		}
		row := d.Notify.Types[kind]
		if row == nil {
			row = map[string]string{}
			d.Notify.Types[kind] = row
		}
		row[key] = value
	}
	ch.Changed = true
	return ch, nil
}
