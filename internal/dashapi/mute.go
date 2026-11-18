package dashapi

import (
	"strconv"
	"strings"

	"fwalarm/internal/model"
	"fwalarm/internal/textde"
	"fwalarm/internal/web"
)

// Arbeitsmodus vom Dashboard aus (PHP: dashboard_api.php, Zweige keys_mute,
// keys_set_mute_permanent, keys_set_mute_volume): stumm = der Critical Alert
// kommt mit der Stumm-Lautstärke und ohne call=1 – sichtbar bleibt er. Was
// die Box kennt (muted, mutevol), erhöht die Listen-Version; "dauerhaft" ist
// reine Server-Logik (das Sicherheitsnetz läuft hier) und erhöht sie nur,
// wenn sich dabei stumm/laut ändert. Kein Push an den Betroffenen (Wunsch des
// Nutzers) – sichtbar bleibt der Wechsel an der Liste und im Verlauf.

func init() {
	Register("keys_mute", Action{Handle: handleKeysMute})
	Register("keys_set_mute_permanent", Action{Handle: handleKeysSetMutePermanent})
	Register("keys_set_mute_volume", Action{Handle: handleKeysSetMuteVolume})
}

// handleKeysMute (dashboard_api.php 771–816): muted=1 stumm, sonst laut.
// Zuerst überfällige Stummschaltungen zurück auf laut (wie PHP; ein Fehler
// dort zeigt sich an der eigenen Änderung). Laut beendet immer auch
// "dauerhaft" (model.ApplyMute) – dann zusätzlich key_mute_permanent im
// Verlauf.
func handleKeysMute(x *Ctx) {
	id := formID(x)
	want := x.Form.Get("muted") == "1"
	now, by, bv := x.Core.Clock.Now(), x.Sess.User, boxVersion(x)
	expireThenApply(x, func(d *model.State) (web.Object, error) {
		r := d.Find(id)
		if r == nil {
			return nil, refuse(msgNotFound)
		}
		if r.Muted == want {
			if want {
				return nil, same("Empfänger ist bereits stumm.")
			}
			return nil, same("Empfänger ist bereits laut.")
		}
		permanentEnded := model.ApplyMute(r, want, now)
		version := d.Bump(bv)
		extra := map[string]any{"source": "dashboard", "version": version}
		event := model.EvKeyUnmuted
		if want {
			event = model.EvKeyMuted
		}
		d.Log(keyEvent(event, *r, by, now, extra))
		if permanentEnded {
			d.Log(keyEvent(model.EvKeyMutePermanent, *r, by, now,
				map[string]any{"source": "dashboard", "version": version, "permanent": false}))
		}
		message := "Empfänger wieder laut geschaltet. " + msgBoxTakesIt
		switch {
		case want:
			message = "Empfänger stummgeschaltet (Arbeitsmodus): Alarme kommen dort nur mit der eingestellten " +
				"Stumm-Lautstärke an. " + msgBoxTakesIt
		case permanentEnded:
			message = "Empfänger wieder laut geschaltet – die dauerhafte Stummschaltung ist damit beendet. " + msgBoxTakesIt
		}
		return changed(message, version), nil
	})
}

// handleKeysSetMutePermanent (dashboard_api.php 831–898): permanent=1 schaltet
// für EINEN Empfänger das Sicherheitsnetz ab – und ihn zugleich stumm (kein
// "dauerhaft, aber laut"); eine schon laufende Stummzeit bleibt. Beenden lässt
// ihn stumm, die Höchstdauer beginnt neu (sonst schaltete ein alter muted_at
// ihn sofort laut). Neue Version NUR, wenn sich stumm/laut ändert.
func handleKeysSetMutePermanent(x *Ctx) {
	id := formID(x)
	want := x.Form.Get("permanent") == "1"
	cfgMax := x.Core.Cfg().MuteMaxSeconds
	now, by, bv := x.Core.Clock.Now(), x.Sess.User, boxVersion(x)
	expireThenApply(x, func(d *model.State) (web.Object, error) {
		r := d.Find(id)
		if r == nil {
			return nil, refuse(msgNotFound)
		}
		if r.MutePermanent == want {
			if want {
				return nil, same("Empfänger ist bereits dauerhaft stumm.")
			}
			return nil, same("Empfänger ist bereits zeitlich begrenzt stumm bzw. laut.")
		}
		wasMuted := r.Muted
		r.MutePermanent = want
		switch {
		case want:
			r.Muted = true
			if !wasMuted {
				r.MutedAt = now.Unix()
			}
		case wasMuted:
			r.MutedAt = now.Unix()
		}
		version := d.KeysVersion
		if wasMuted != r.Muted { // nur, was die Box kennt
			version = d.Bump(bv)
		}
		d.Log(keyEvent(model.EvKeyMutePermanent, *r, by, now,
			map[string]any{"source": "dashboard", "permanent": want, "version": version}))
		maxMute := d.MuteMax(cfgMax)
		hours := textde.DurationLabel(maxMute) // sekundengenau, nicht auf Stunden gerundet
		var message string
		switch {
		case want && maxMute > 0:
			message = "Empfänger ist jetzt DAUERHAFT stumm: Die automatische Rückschaltung nach " + hours +
				" greift hier nicht mehr."
		case want:
			message = "Empfänger ist jetzt DAUERHAFT stumm."
		case maxMute > 0:
			message = "Dauerhafte Stummschaltung beendet. Der Empfänger bleibt vorerst stumm, wird aber wie üblich nach " +
				hours + " automatisch wieder laut geschaltet."
		default:
			// Ohne Sicherheitsnetz gibt es keine Rückschaltung – das darf die
			// Meldung nicht behaupten.
			message = "Dauerhafte Stummschaltung beendet. Der Empfänger bleibt vorerst stumm, bleibt aber stumm, bis " +
				"jemand sie beendet (die automatische Rückschaltung ist in config.toml abgeschaltet)."
		}
		if want {
			message += " Alarme kommen weiterhin sichtbar an (Stumm-Lautstärke), aber ohne vollen Ton – bis du das hier wieder beendest."
		}
		return changed(message, version), nil
	})
}

// handleKeysSetMuteVolume (dashboard_api.php 904–939): Stumm-Lautstärke 0–10
// (0 = lautlos). Streng geprüft: Ein leerer oder kaputter Wert wird NICHT
// still 0. Neue Version; Verlauf key_mute_volume.
func handleKeysSetMuteVolume(x *Ctx) {
	id := formID(x)
	volume, ok := filterInt(x.Form.Get("volume"), 0, 10)
	if !ok { // auch ein fehlendes Feld ("" – PHP: filter_var(null) = false)
		reply(x, "Ungültige Lautstärke (erlaubt: 0-10).")
		return
	}
	now, by, bv := x.Core.Clock.Now(), x.Sess.User, boxVersion(x)
	apply(x, func(d *model.State) (web.Object, error) {
		r := d.Find(id)
		if r == nil {
			return nil, refuse(msgNotFound)
		}
		if r.MuteVolume == volume {
			return nil, same("Stumm-Lautstärke ist bereits so eingestellt.")
		}
		r.MuteVolume = volume
		version := d.Bump(bv)
		d.Log(keyEvent(model.EvKeyMuteVolume, *r, by, now, map[string]any{"volume": volume, "version": version}))
		message := "Gespeichert: Stumm = leise mit Lautstärke " + strconv.Itoa(volume) + "."
		if volume == 0 {
			message = "Gespeichert: Stumm = komplett lautlos."
		}
		return changed(message+" "+msgBoxTakesIt, version), nil
	})
}

// filterInt prüft eine ganze Zahl wie PHPs filter_var(…, FILTER_VALIDATE_INT)
// mit min_range/max_range: Leerraum außen erlaubt, ein Vorzeichen, keine
// führende Null (außer "0", "+0", "-0"), sonst nur Ziffern, im Bereich.
func filterInt(s string, lo, hi int) (int, bool) {
	s = strings.Trim(s, " \t\r\v\n")
	if s == "" {
		return 0, false
	}
	neg := false
	switch s[0] {
	case '-':
		neg = true
		s = s[1:]
	case '+':
		s = s[1:]
	}
	if s == "0" {
		return 0, lo <= 0 && 0 <= hi
	}
	if s == "" || s[0] < '1' || s[0] > '9' {
		return 0, false
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false // zu groß
	}
	if neg {
		n = -n
	}
	return n, lo <= n && n <= hi
}
