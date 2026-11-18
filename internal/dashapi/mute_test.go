package dashapi

import (
	"strconv"
	"strings"
	"testing"

	"fwalarm/internal/config"
	"fwalarm/internal/model"
)

// keys_mute: Antworten und Verlauf wie PHP; laut beendet auch "dauerhaft"
// (dann zwei Einträge); kein Push an irgendwen.
func TestKeysMute(t *testing.T) {
	b := newBench(t)
	b.setCfg(func(c *config.Config) { c.MuteMaxSeconds = 43200 })
	b.seed(5, anna, bert)
	admin := b.admin()
	admin.want(`{"ok":true,"changed":true,"message":"Empfänger stummgeschaltet (Arbeitsmodus): Alarme kommen dort nur mit der eingestellten Stumm-Lautstärke an. Die Box übernimmt das nach wenigen Sekunden.","version":6}`,
		"action", "keys_mute", "id", "1", "muted", "1")
	if r := b.c.Store.State().Recipients[0]; !r.Muted || r.MutedAt != t0.Unix() {
		t.Errorf("%+v", r)
	}
	admin.want(`{"ok":true,"changed":false,"message":"Empfänger ist bereits stumm."}`, "action", "keys_mute", "id", "1", "muted", "1")
	admin.want(`{"ok":true,"changed":true,"message":"Empfänger wieder laut geschaltet. Die Box übernimmt das nach wenigen Sekunden.","version":7}`,
		"action", "keys_mute", "id", "1", "muted", "0")
	admin.want(`{"ok":true,"changed":false,"message":"Empfänger ist bereits laut."}`, "action", "keys_mute", "id", "1", "muted", "true")
	admin.want(`{"ok":false,"message":"Empfänger nicht gefunden."}`, "action", "keys_mute", "id", "9", "muted", "1")
	muted, unmuted := b.events(model.EvKeyMuted), b.events(model.EvKeyUnmuted)
	if len(muted) != 1 || fields(muted[0]) != "event=key_muted by=admin label=Anna key=anna... source=dashboard version=6" ||
		len(unmuted) != 1 || fields(unmuted[0]) != "event=key_unmuted by=admin label=Anna key=anna... source=dashboard version=7" {
		t.Errorf("Verlauf: %v / %v", muted, unmuted)
	}

	// Laut beendet "dauerhaft".
	b.update(func(d *model.State) {
		d.Recipients[1].Muted, d.Recipients[1].MutedAt, d.Recipients[1].MutePermanent = true, 5, true
	})
	admin.want(`{"ok":true,"changed":true,"message":"Empfänger wieder laut geschaltet – die dauerhafte Stummschaltung ist damit beendet. Die Box übernimmt das nach wenigen Sekunden.","version":8}`,
		"action", "keys_mute", "id", "2", "muted", "0")
	if r := b.c.Store.State().Recipients[1]; r.Muted || r.MutePermanent || r.MutedAt != 0 {
		t.Errorf("%+v", r)
	}
	if perm := b.events(model.EvKeyMutePermanent); len(perm) != 1 ||
		fields(perm[0]) != "event=key_mute_permanent by=admin label=Bert key=bert... source=dashboard permanent=false version=8" {
		t.Errorf("Verlauf: %v", perm)
	}

	// Zuerst laufen überfällige Stummschaltungen ab: Anna ist seit 13 Std
	// stumm – "stumm" schaltet sie neu stumm (statt "bereits stumm").
	b.update(func(d *model.State) { d.Recipients[0].Muted, d.Recipients[0].MutedAt = true, t0.Unix()-46800 })
	admin.want(`{"ok":true,"changed":true,"message":"Empfänger stummgeschaltet (Arbeitsmodus): Alarme kommen dort nur mit der eingestellten Stumm-Lautstärke an. Die Box übernimmt das nach wenigen Sekunden.","version":10}`,
		"action", "keys_mute", "id", "1", "muted", "1")
	if auto := b.events(model.EvKeyUnmuted); auto[0].Str("by") != "automatisch" || auto[0].Int("version") != 9 {
		t.Errorf("Rückschaltung vorher: %v", auto)
	}
	b.c.Bg.Wait(15e9)
	for _, r := range b.barkTake() {
		if r.Key != anna.Key { // nur die Info "wieder laut" der Rückschaltung
			t.Errorf("Push an %s: %v", r.Key, r.Fields)
		}
	}
}

// "Dauerhaft stumm" erhöht die Version nur, wenn sich dabei stumm/laut
// ändert – das Flag selbst kennt die Box nicht.
func TestPermanentBumpsOnlyWithMuteChange(t *testing.T) {
	b := newBench(t)
	b.setCfg(func(c *config.Config) { c.MuteMaxSeconds = 43200 })
	b.seed(5, anna)
	admin := b.admin()
	keep := " Alarme kommen weiterhin sichtbar an (Stumm-Lautstärke), aber ohne vollen Ton – bis du das hier wieder beendest."

	// laut → dauerhaft (und damit stumm): neue Version.
	admin.want(`{"ok":true,"changed":true,"message":"Empfänger ist jetzt DAUERHAFT stumm: Die automatische Rückschaltung nach 12 Std greift hier nicht mehr.`+keep+`","version":6}`,
		"action", "keys_set_mute_permanent", "id", "1", "permanent", "1")
	if r := b.c.Store.State().Recipients[0]; !r.Muted || !r.MutePermanent || r.MutedAt != t0.Unix() {
		t.Errorf("%+v", r)
	}
	admin.want(`{"ok":true,"changed":false,"message":"Empfänger ist bereits dauerhaft stumm."}`, "action", "keys_set_mute_permanent", "id", "1", "permanent", "1")
	// Beenden: bleibt stumm, die Höchstdauer beginnt neu – keine neue Version.
	b.clk.Advance(3600e9)
	admin.want(`{"ok":true,"changed":true,"message":"Dauerhafte Stummschaltung beendet. Der Empfänger bleibt vorerst stumm, wird aber wie üblich nach 12 Std automatisch wieder laut geschaltet.","version":6}`,
		"action", "keys_set_mute_permanent", "id", "1", "permanent", "0")
	if r := b.c.Store.State().Recipients[0]; !r.Muted || r.MutePermanent || r.MutedAt != t0.Unix()+3600 {
		t.Errorf("%+v", r)
	}
	admin.want(`{"ok":true,"changed":false,"message":"Empfänger ist bereits zeitlich begrenzt stumm bzw. laut."}`, "action", "keys_set_mute_permanent", "id", "1", "permanent", "0")
	// schon stumm → dauerhaft: keine neue Version, die Stummzeit bleibt.
	admin.want(`{"ok":true,"changed":true,"message":"Empfänger ist jetzt DAUERHAFT stumm: Die automatische Rückschaltung nach 12 Std greift hier nicht mehr.`+keep+`","version":6}`,
		"action", "keys_set_mute_permanent", "id", "1", "permanent", "1")
	if r := b.c.Store.State().Recipients[0]; r.MutedAt != t0.Unix()+3600 {
		t.Errorf("Stummzeit nicht behalten: %+v", r)
	}
	perm := b.events(model.EvKeyMutePermanent)
	if len(perm) != 3 || fields(perm[2]) != "event=key_mute_permanent by=admin label=Anna key=anna... source=dashboard permanent=true version=6" ||
		fields(perm[1]) != "event=key_mute_permanent by=admin label=Anna key=anna... source=dashboard permanent=false version=6" {
		t.Errorf("Verlauf: %v", perm)
	}
	admin.want(`{"ok":false,"message":"Empfänger nicht gefunden."}`, "action", "keys_set_mute_permanent", "id", "9", "permanent", "1")

	// Ohne Sicherheitsnetz (Konfiguration 0) behauptet die Meldung keine
	// Rückschaltung; die Stufe aus dem Dashboard zählt sekundengenau.
	b.setCfg(func(c *config.Config) { c.MuteMaxSeconds = 0 })
	admin.want(`{"ok":true,"changed":true,"message":"Dauerhafte Stummschaltung beendet. Der Empfänger bleibt vorerst stumm, bleibt aber stumm, bis jemand sie beendet (die automatische Rückschaltung ist in config.toml abgeschaltet).","version":6}`,
		"action", "keys_set_mute_permanent", "id", "1", "permanent", "0")
	admin.want(`{"ok":true,"changed":true,"message":"Empfänger ist jetzt DAUERHAFT stumm.`+keep+`","version":6}`,
		"action", "keys_set_mute_permanent", "id", "1", "permanent", "1")
	b.setCfg(func(c *config.Config) { c.MuteMaxSeconds = 5400 })
	admin.want(`{"ok":true,"changed":true,"message":"Dauerhafte Stummschaltung beendet. Der Empfänger bleibt vorerst stumm, wird aber wie üblich nach 90 Min automatisch wieder laut geschaltet.","version":6}`,
		"action", "keys_set_mute_permanent", "id", "1", "permanent", "0")
}

// Stumm-Lautstärke: streng geprüft wie PHPs filter_var (ein kaputter Wert
// wird nie still 0); neue Version; Verlauf key_mute_volume.
func TestKeysSetMuteVolume(t *testing.T) {
	b := newBench(t)
	b.seed(5, anna)
	admin := b.admin()
	for _, bad := range []string{"", " ", "11", "-1", "05", "00", "4.0", "4a", "abc", "0x1", "1e1", "99999999999999999999"} {
		admin.want(`{"ok":false,"message":"Ungültige Lautstärke (erlaubt: 0-10)."}`, "action", "keys_set_mute_volume", "id", "1", "volume", bad)
	}
	admin.want(`{"ok":false,"message":"Ungültige Lautstärke (erlaubt: 0-10)."}`, "action", "keys_set_mute_volume", "id", "1")
	admin.want(`{"ok":true,"changed":true,"message":"Gespeichert: Stumm = leise mit Lautstärke 4. Die Box übernimmt das nach wenigen Sekunden.","version":6}`,
		"action", "keys_set_mute_volume", "id", "1", "volume", " +4\n")
	admin.want(`{"ok":true,"changed":false,"message":"Stumm-Lautstärke ist bereits so eingestellt."}`, "action", "keys_set_mute_volume", "id", "1", "volume", "4")
	admin.want(`{"ok":true,"changed":true,"message":"Gespeichert: Stumm = komplett lautlos. Die Box übernimmt das nach wenigen Sekunden.","version":7}`,
		"action", "keys_set_mute_volume", "id", "1", "volume", "-0")
	admin.want(`{"ok":false,"message":"Empfänger nicht gefunden."}`, "action", "keys_set_mute_volume", "id", "9", "volume", "3")
	if v := b.events(model.EvKeyMuteVolume); len(v) != 2 || fields(v[1]) != "event=key_mute_volume by=admin label=Anna key=anna... volume=4 version=6" {
		t.Errorf("Verlauf: %v", v)
	}
}

// filterInt ist PHPs filter_var(…, FILTER_VALIDATE_INT) mit Bereich.
func TestFilterInt(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
		ok   bool
	}{
		{"0", 0, true}, {"+0", 0, true}, {"-0", 0, true}, {"10", 10, true}, {" 7 ", 7, true}, {"\t\n3\r\v", 3, true},
		{"+10", 10, true}, {"11", 0, false}, {"-1", 0, false}, {"", 0, false}, {" ", 0, false}, {"01", 0, false},
		{"00", 0, false}, {"-00", 0, false}, {"+", 0, false}, {"-", 0, false}, {"1 0", 0, false}, {"1.0", 0, false},
		{"\x001", 0, false}, {"1\x00", 0, false}, {"٣", 0, false}, {"9223372036854775808", 0, false},
	} {
		got, ok := filterInt(tc.in, 0, 10)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("%q: %d %v, erwartet %d %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// FuzzFilterInt: Was filterInt annimmt, ist eine Zahl im Bereich, deren
// kanonische Form (ohne "+" und "-" vor 0, ohne Leerraum) genau die Eingabe
// ohne Leerraum außen ist.
func FuzzFilterInt(f *testing.F) {
	for _, s := range []string{"0", "-0", "+7", " 10 ", "11", "05", "4.0", "", "\v3\n", "-3", "99999999999999999999"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n, ok := filterInt(s, 0, 10)
		if !ok {
			return
		}
		if n < 0 || n > 10 {
			t.Fatalf("%q → %d außerhalb", s, n)
		}
		core := strings.Trim(s, " \t\r\v\n")
		core = strings.TrimPrefix(strings.TrimPrefix(core, "+"), "-")
		if core != strconv.Itoa(n) {
			t.Fatalf("%q → %d, Kern %q", s, n, core)
		}
	})
}
