package dashapi

import (
	"net/http"
	"strconv"

	"fwalarm/internal/config"
	"fwalarm/internal/model"
	"fwalarm/internal/textde"
	"fwalarm/internal/web"
)

// Allgemeine Einstellungen (PHP: dashboard_api.php, Zweige settings_list und
// settings_set): bisher nur, nach wie vielen Sekunden Stumme automatisch
// wieder laut werden (mute_max_seconds). Nur für den Admin (Tabelle in
// actions.go). Angeboten werden feste Stufen plus der Wert der Konfiguration
// (unverändert, als "(Standard)" markiert); ein anderer Wert wird abgelehnt.

func init() {
	Register("settings_list", Action{Handle: handleSettingsList})
	Register("settings_set", Action{Handle: handleSettingsSet})
}

// muteMaxLabel: die Beschriftung im Dashboard und im Verlauf.
const muteMaxLabel = "Automatisch wieder laut nach"

// muteMaxText: "nie" für 0, sonst die Dauer sekundengenau.
func muteMaxText(seconds int) string {
	if seconds == 0 {
		return "nie"
	}
	return textde.DurationLabel(seconds)
}

// handleSettingsList (dashboard_api.php 1072–1087): Wert, Standard und
// Auswahl – Zahlen als Zahlen.
func handleSettingsList(x *Ctx) {
	cfgSeconds := x.Core.Cfg().MuteMaxSeconds
	def := max(0, cfgSeconds)
	choices := []web.Object{}
	for _, s := range model.MuteMaxChoices(cfgSeconds) {
		text := muteMaxText(s)
		if s == def {
			text += " (Standard)"
		}
		choices = append(choices, web.Object{{Key: "value", Value: s}, {Key: "text", Value: text}})
	}
	web.JSON(x.W, http.StatusOK, web.Object{
		{Key: "ok", Value: true},
		{Key: "settings", Value: web.Object{{Key: "mute_max_seconds", Value: web.Object{
			{Key: "value", Value: x.Core.Store.State().MuteMax(cfgSeconds)},
			{Key: "default", Value: def},
			{Key: "choices", Value: choices},
		}}}},
	})
}

// handleSettingsSet (dashboard_api.php 1089–1130): key = mute_max_seconds,
// value = genau eine der angebotenen Stufen (als Text, Leerraum außen
// erlaubt). Gespeichert wird der gewählte Wert; ist er schon gespeichert,
// nichts. Verlauf setting_changed (nur für den Admin sichtbar) in derselben
// Änderung. Die Listen-Version bleibt: Was dadurch überfällig wird, schaltet
// das nächste Expire zurück (dann mit neuer Version).
func handleSettingsSet(x *Ctx) {
	if x.Form.Get("key") != "mute_max_seconds" {
		reply(x, "Unbekannte Einstellung.")
		return
	}
	raw := config.TrimPHP(x.Form.Get("value"))
	picked, found := 0, false
	for _, s := range model.MuteMaxChoices(x.Core.Cfg().MuteMaxSeconds) {
		if strconv.Itoa(s) == raw {
			picked, found = s, true
			break
		}
	}
	if !found {
		reply(x, "Ungültiger Wert – nichts geändert.")
		return
	}
	now, by := x.Core.Clock.Now(), x.Sess.User
	apply(x, func(d *model.State) (web.Object, error) {
		if d.Settings.MuteMaxSeconds == picked {
			return nil, same("Ist bereits so eingestellt.")
		}
		d.Settings.MuteMaxSeconds = picked
		text := muteMaxText(picked)
		d.Log(model.NewEntry(model.EvSettingChanged, now, map[string]any{"by": by, "label": muteMaxLabel, "value": text}))
		return web.Object{{Key: "ok", Value: true}, {Key: "changed", Value: true},
			{Key: "message", Value: "Gespeichert – " + muteMaxLabel + ": " + text + "."}}, nil
	})
}
