package dashapi

import (
	"errors"
	"net/http"

	"fwalarm/internal/config"
	"fwalarm/internal/model"
	"fwalarm/internal/notify"
	"fwalarm/internal/textde"
	"fwalarm/internal/web"
)

// Die Meldungen an den Admin einstellen (PHP: dashboard_api.php, Zweige
// notify_list, notify_set, notify_test): welche Meldungen der Server an den
// Status-Key schickt und wie. Alle drei nur für den Admin – auch das Lesen
// (Tabelle in actions.go); die Lese-Rolle bekommt 403 "readonly" und erfährt
// nichts über diese Einstellungen. Jede Einstellung ist eine Auswahl aus
// festen Möglichkeiten; die Oberfläche baut ihre Auswahlfelder vollständig
// aus der Antwort von notify_list.

func init() {
	Register("notify_list", Action{Handle: handleNotifyList})
	Register("notify_set", Action{Handle: handleNotifySet})
	Register("notify_test", Action{Handle: handleNotifyTest})
}

// optionList: Einstellungen samt aktuellem Wert als Liste für die
// Oberfläche; Werte als Text (ein <select> kennt nur Text).
func optionList(specs []notify.OptionDef, values map[string]string) []web.Object {
	out := make([]web.Object, 0, len(specs))
	for _, s := range specs {
		choices := make([]web.Object, 0, len(s.Choices))
		for _, c := range s.Choices {
			choices = append(choices, web.Object{{Key: "value", Value: c.Value}, {Key: "text", Value: c.Text}})
		}
		value, ok := values[s.Key]
		if !ok {
			value = s.Default
		}
		out = append(out, web.Object{{Key: "key", Value: s.Key}, {Key: "label", Value: s.Label},
			{Key: "value", Value: value}, {Key: "choices", Value: choices}})
	}
	return out
}

// handleNotifyList (dashboard_api.php 947–983): configured = ein Status-Key
// ist eingetragen (ohne ihn sendet der Server nichts – das Panel sagt das
// dazu); quiet_now = gerade Ruhezeit.
func handleNotifyList(x *Ctx) {
	cfg := x.Core.Cfg()
	types, global, set := notify.Snapshot(x.Core)
	kinds := make([]web.Object, 0, len(types))
	for _, t := range types {
		ks := set.Types[t.Key]
		kinds = append(kinds, web.Object{
			{Key: "key", Value: t.Key},
			{Key: "label", Value: t.Label},
			{Key: "hint", Value: t.Hint},
			{Key: "important", Value: t.Important},
			{Key: "enabled", Value: ks.Enabled},
			{Key: "options", Value: optionList(t.Options, ks.Values)},
		})
	}
	web.JSON(x.W, http.StatusOK, web.Object{
		{Key: "ok", Value: true},
		{Key: "configured", Value: config.TrimPHP(cfg.BarkKeyStatus) != ""},
		{Key: "quiet_now", Value: notify.InQuietHours(set.Global, x.Core.Clock.Now().In(cfg.Location()))},
		{Key: "global", Value: optionList(global, set.Global)},
		{Key: "types", Value: kinds},
	})
}

// errUnchanged: notify_set ohne Änderung – nichts wird gesichert.
var errUnchanged = errors.New("unverändert")

// handleNotifySet (dashboard_api.php 990–1066): EINE Einstellung ändern –
// type = Art der Meldung oder "global", key = die Einstellung ("enabled" =
// Art ein/aus), value = einer der angebotenen Werte. Ein unbekannter Wert
// wird ABGELEHNT (nie still der Standard). Gespeichert wird nur, was anders
// ist als der geltende Wert – alles andere folgt weiter den Standards. Die
// Änderung und ihr Verlaufseintrag notify_changed (nur für den Admin
// sichtbar) gehen in EINER Änderung der Ablage; scheitert das Sichern,
// gilt keins von beiden.
func handleNotifySet(x *Ctx) {
	kind, key, value := x.Form.Get("type"), x.Form.Get("key"), x.Form.Get("value")
	cfg := x.Core.Cfg()
	if err := notify.CheckSetting(cfg, kind, key, value); err != nil {
		web.JSON(x.W, http.StatusOK, web.Object{{Key: "ok", Value: false}, {Key: "message", Value: err.Error()}})
		return
	}
	now := x.Core.Clock.Now()
	ctx, cancel := web.Budget(web.BudgetUI)
	defer cancel()
	var ch notify.Change
	err := x.Core.Store.Update(ctx, func(d *model.State) error {
		var err error
		if ch, err = notify.Apply(cfg, d, kind, key, value); err != nil {
			return err
		}
		if !ch.Changed {
			return errUnchanged
		}
		d.Log(model.NewEntry(model.EvNotifyChanged, now, map[string]any{
			"by": x.Sess.User, "type": kind, "key": key, "label": ch.Scope, "option": ch.Option, "value": ch.ValueText,
		}))
		return nil
	})
	var invalid *notify.InvalidValueError
	switch {
	case errors.Is(err, errUnchanged):
		web.JSON(x.W, http.StatusOK, web.Object{{Key: "ok", Value: true}, {Key: "changed", Value: false},
			{Key: "message", Value: "Ist bereits so eingestellt."}})
		return
	case errors.Is(err, notify.ErrUnknownSetting), errors.As(err, &invalid):
		// Erst am Zustand erkennbar: ein Stellvertreter, den es nicht gibt.
		web.JSON(x.W, http.StatusOK, web.Object{{Key: "ok", Value: false}, {Key: "message", Value: err.Error()}})
		return
	case err != nil:
		web.StorageError(x.W, err, true)
		return
	}
	message := "Gespeichert – " + ch.Scope + ", " + ch.Option + ": " + ch.ValueText + "."
	if kind != "global" && key == "enabled" {
		if value == "1" {
			message = "Eingeschaltet: „" + ch.Scope + "“ wird dir gemeldet."
		} else {
			message = "Abgeschaltet: „" + ch.Scope + "“ wird dir nicht mehr gemeldet."
		}
	}
	web.JSON(x.W, http.StatusOK, web.Object{{Key: "ok", Value: true}, {Key: "changed", Value: true},
		{Key: "message", Value: message}})
}

// handleNotifyTest (dashboard_api.php 1135–1157): eine Probe-Meldung dieser
// Art, genau so, wie sie nach den aktuellen Einstellungen ankäme
// (Dringlichkeit, Ruhezeit, Ton, Stellvertreter) – auch wenn die Art gerade
// abgeschaltet ist. Die Antwort nennt die Dringlichkeit, mit der WIRKLICH
// gesendet wurde (Used), keine hier nachgerechnete.
func handleNotifyTest(x *Ctx) {
	kind := x.Form.Get("type")
	label := ""
	for _, t := range notify.Types(x.Core) {
		if t.Key == kind {
			label = t.Label
		}
	}
	if label == "" {
		web.JSON(x.W, http.StatusOK, web.Object{{Key: "ok", Value: false}, {Key: "message", Value: "Unbekannte Art von Meldung."}})
		return
	}
	stamp := textde.Stamp(x.Core.Clock.Now(), x.Core.Cfg().Location())
	result, used := notify.Send(x.R.Context(), x.Core, kind, "PROBE: "+label,
		stamp+"Probe-Meldung aus dem Dashboard. So kommt diese Art von Meldung bei dir an.", true)
	switch result {
	case notify.Sent:
		message := "Probe gesendet – Dringlichkeit: " + notify.LevelText(used.Level)
		if used.QuietCapped {
			message += " (gerade Ruhezeit, deshalb leise)"
		}
		web.JSON(x.W, http.StatusOK, web.Object{{Key: "ok", Value: true}, {Key: "message", Value: message + "."}})
	case notify.Off:
		web.JSON(x.W, http.StatusOK, web.Object{{Key: "ok", Value: false},
			{Key: "message", Value: "Kein Status-Key (bark_key_status) in config.toml eingetragen – es kann nichts gesendet werden."}})
	default:
		web.JSON(x.W, http.StatusOK, web.Object{{Key: "ok", Value: false},
			{Key: "message", Value: "Probe FEHLGESCHLAGEN – Bark hat sie nicht angenommen (Status-Key und Verbindung prüfen)."}})
	}
}
