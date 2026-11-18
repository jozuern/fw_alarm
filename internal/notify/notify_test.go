package notify

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"fwalarm/internal/config"
	"fwalarm/internal/model"
)

// Ruhezeit in vollen Stunden der Ortszeit; über Mitternacht; gleiche Stunden
// = nie. An den Tagen der Zeitumstellung entscheidet die Ortsstunde, nicht
// die UTC-Stunde.
func TestQuietHoursAcrossMidnightAndDST(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	quiet := func(from, to string) map[string]string {
		return map[string]string{"quiet_enabled": "1", "quiet_from": from, "quiet_to": to}
	}
	local := func(day, hm string) time.Time {
		tt, err := time.ParseInLocation("2006-01-02 15:04", day+" "+hm, berlin)
		if err != nil {
			t.Fatal(err)
		}
		return tt
	}
	utc := func(s string) time.Time {
		tt, err := time.Parse("2006-01-02 15:04", s)
		if err != nil {
			t.Fatal(err)
		}
		return tt.In(berlin)
	}
	cases := []struct {
		name   string
		global map[string]string
		now    time.Time
		want   bool
	}{
		{"22→7 um 23:30", quiet("22", "7"), local("2026-11-17", "23:30"), true},
		{"22→7 um 06:59", quiet("22", "7"), local("2026-11-18", "06:59"), true},
		{"22→7 um 07:00", quiet("22", "7"), local("2026-11-18", "07:00"), false},
		{"22→7 um 21:59", quiet("22", "7"), local("2026-11-17", "21:59"), false},
		{"22→7 um 22:00", quiet("22", "7"), local("2026-11-17", "22:00"), true},
		{"22→7 um 00:00", quiet("22", "7"), local("2026-11-18", "00:00"), true},
		{"1→5 um 03:00", quiet("1", "5"), local("2026-11-18", "03:00"), true},
		{"1→5 um 05:00", quiet("1", "5"), local("2026-11-18", "05:00"), false},
		{"1→5 um 00:59", quiet("1", "5"), local("2026-11-18", "00:59"), false},
		{"gleiche Stunden: nie", quiet("7", "7"), local("2026-11-18", "07:30"), false},
		{"gleiche Stunden 0: nie", quiet("0", "0"), local("2026-11-18", "00:30"), false},
		{"ausgeschaltet", map[string]string{"quiet_enabled": "0", "quiet_from": "22", "quiet_to": "7"}, local("2026-11-17", "23:30"), false},
		{"ohne Angabe", map[string]string{}, local("2026-11-17", "23:30"), false},
		// 29.03.2026: um 01:00 UTC springt Berlin von 02:00 auf 03:00.
		{"Frühjahr: 05:30 UTC = 07:30 MESZ", quiet("22", "7"), utc("2026-03-29 05:30"), false},
		{"Frühjahr: 04:30 UTC = 06:30 MESZ", quiet("22", "7"), utc("2026-03-29 04:30"), true},
		{"Frühjahr: 20:30 UTC = 22:30 MESZ", quiet("22", "7"), utc("2026-03-29 20:30"), true},
		{"Frühjahr: 01:30 UTC = 03:30 MESZ (2 Uhr gibt es nicht)", quiet("2", "3"), utc("2026-03-29 01:30"), false},
		// 25.10.2026: um 01:00 UTC springt Berlin von 03:00 zurück auf 02:00.
		{"Herbst: 06:30 UTC = 07:30 MEZ", quiet("22", "7"), utc("2026-10-25 06:30"), false},
		{"Herbst: 05:30 UTC = 06:30 MEZ", quiet("22", "7"), utc("2026-10-25 05:30"), true},
		{"Herbst: 00:30 UTC = 02:30 MESZ", quiet("2", "3"), utc("2026-10-25 00:30"), true},
		{"Herbst: 01:30 UTC = 02:30 MEZ (die Stunde zweimal)", quiet("2", "3"), utc("2026-10-25 01:30"), true},
		{"Herbst: 21:30 UTC = 22:30 MEZ", quiet("22", "7"), utc("2026-10-25 21:30"), true},
	}
	for _, tc := range cases {
		if got := InQuietHours(tc.global, tc.now); got != tc.want {
			t.Errorf("%s (%s): %v, erwartet %v", tc.name, tc.now.Format(time.RFC3339), got, tc.want)
		}
	}
}

// Was Send liefert – davon hängt ab, ob der Prüf-Lauf einen Ausfall als
// gemeldet abhakt. "off" nur für eine abgeschaltete Art oder einen leeren
// Status-Key; alles, was nicht bei Bark angenommen wurde, ist "failed".
func TestSendResults(t *testing.T) {
	b := newBench(t)
	b.recipients()

	type want struct {
		result  string
		keys    []string
		primary bool // PrimaryFailed
	}
	check := func(name, kind string, force bool, w want) {
		t.Helper()
		got, used := b.send(kind, force)
		reqs := b.take()
		if got != w.result || used.PrimaryFailed != w.primary {
			t.Errorf("%s: %q %+v, erwartet %q PrimaryFailed=%v", name, got, used, w.result, w.primary)
		}
		// Je Key: die Zahl der Anfragen (2 = wiederholt).
		if gotKeys := keysOf(reqs); !slices.Equal(sortedCopy(gotKeys), sortedCopy(w.keys)) {
			t.Errorf("%s: Anfragen an %v, erwartet %v", name, gotKeys, w.keys)
		}
	}

	check("ab Werk an", KindBoxOffline, false, want{Sent, []string{statusKey}, false})
	check("neue Art ab Werk aus", KindLoginSuccess, false, want{Off, nil, false})
	check("neue Art ab Werk aus, Probe", KindLoginSuccess, true, want{Sent, []string{statusKey}, false})
	b.stored(KindBoxOffline, map[string]string{"enabled": "0"})
	check("abgeschaltet", KindBoxOffline, false, want{Off, nil, false})
	check("abgeschaltet, Probe", KindBoxOffline, true, want{Sent, []string{statusKey}, false})
	b.stored(KindBoxOffline, map[string]string{"enabled": "1"})

	check("unbekannte Art", "gibtesnicht", false, want{Failed, nil, false})
	check("unbekannte Art, Probe", "gibtesnicht", true, want{Failed, nil, false})
	check("Außen-Wächter nicht eingerichtet: unbekannt", KindOutsideWatcher, false, want{Failed, nil, false})

	b.setCfg(func(c *config.Config) { c.BarkKeyStatus = "" })
	check("kein Status-Key", KindBoxOffline, false, want{Off, nil, false})
	check("kein Status-Key, Probe", KindBoxOffline, true, want{Off, nil, false})
	check("kein Status-Key, unbekannte Art", "gibtesnicht", false, want{Off, nil, false})
	b.setCfg(func(c *config.Config) { c.BarkKeyStatus = "x!geheim" })
	check("unbrauchbarer Status-Key", KindBoxOffline, false, want{Failed, nil, false})
	b.setCfg(func(c *config.Config) { c.BarkKeyStatus = "https://bark.example/" + statusKey })
	check("ganze Adresse statt Key", KindBoxOffline, true, want{Failed, nil, false})
	b.setCfg(func(c *config.Config) { c.BarkKeyStatus = statusKey })

	// Bark lehnt ab oder antwortet nicht.
	b.setAnswer(func(string) int { return 400 })
	check("Bark 400: nicht wiederholt", KindBoxOffline, false, want{Failed, []string{statusKey}, false})
	b.setAnswer(func(string) int { return 500 })
	check("Bark 500: wiederholt", KindBoxOffline, false, want{Failed, []string{statusKey, statusKey}, false})
	b.setAnswer(func(string) int { return 0 })
	check("Bark antwortet nicht", KindBoxOffline, false, want{Failed, []string{statusKey, statusKey}, false})
	b.setAnswer(nil)

	// Stellvertreter: best effort.
	b.stored("global", map[string]string{"extra_id": "3"})
	check("Status-Key und Stellvertreter", KindBoxOffline, false, want{Sent, []string{statusKey, annaKey}, false})
	b.setAnswer(func(key string) int {
		if key == statusKey {
			return 404
		}
		return 200
	})
	check("nur der Stellvertreter nimmt an", KindBoxOffline, false, want{Sent, []string{statusKey, annaKey}, true})
	b.setAnswer(func(key string) int {
		if key == annaKey {
			return 500
		}
		return 200
	})
	check("nur der Status-Key nimmt an", KindBoxOffline, false, want{Sent, []string{statusKey, annaKey, annaKey}, false})
	b.setAnswer(func(string) int { return 500 })
	check("beide scheitern", KindBoxOffline, false, want{Failed, []string{statusKey, statusKey, annaKey, annaKey}, false})
	b.setAnswer(nil)
	b.setCfg(func(c *config.Config) { c.BarkKeyStatus = annaKey })
	check("Stellvertreter = Status-Key: nicht doppelt", KindBoxOffline, false, want{Sent, []string{annaKey}, false})
	b.setCfg(func(c *config.Config) { c.BarkKeyStatus = statusKey })
	b.update(func(d *model.State) { d.Recipients = d.Recipients[1:] }) // Anna gelöscht
	check("Stellvertreter gelöscht", KindBoxOffline, false, want{Sent, []string{statusKey}, false})
	b.recipients()

	// Die Probe-Sperre hält den Status-Key zurück: nichts ging hinaus.
	guardUntil := time.Now().Add(time.Hour).In(b.cfg.Load().Location()).Format("2006-01-02 15:04")
	b.setCfg(func(c *config.Config) { c.SendGuardKeys, c.SendGuardUntil = []string{"adminkey99"}, guardUntil })
	check("Probe-Sperre", KindBoxOffline, false, want{Failed, nil, false})
	b.setCfg(func(c *config.Config) { c.SendGuardKeys = []string{annaKey} })
	check("Probe-Sperre lässt nur den Stellvertreter durch", KindBoxOffline, false, want{Sent, []string{annaKey}, true})
	b.setCfg(func(c *config.Config) { c.SendGuardKeys, c.SendGuardUntil = nil, "" })

	// Ein Kontext, der schon zu Ende ist: nichts angenommen.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, _ := Send(ctx, b.c, KindBoxOffline, "T", "B", false); got != Failed {
		t.Errorf("abgelaufener Kontext: %q", got)
	}
	b.take()
}

func sortedCopy(s []string) []string {
	c := slices.Clone(s)
	slices.Sort(c)
	return c
}

// Die Felder: ASCII-Texte, Dringlichkeit, Ton und Lautstärke nach den
// Einstellungen; in der Ruhezeit (Ortszeit) leise, außer die Art ist
// ausgenommen.
func TestSendFields(t *testing.T) {
	b := newBench(t)
	b.recipients()
	send := func(kind string) (string, Used, barkReq) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		got, used := Send(ctx, b.c, kind, "Prüfung für Jürgen", "Größe – „ok“…\tÆ", false)
		reqs := b.take()
		if len(reqs) != 1 {
			t.Fatalf("%d Anfragen", len(reqs))
		}
		return got, used, reqs[0]
	}
	field := func(r barkReq, name string) string { return r.Fields.Get(name) }
	has := func(r barkReq, name string) bool { _, ok := r.Fields[name]; return ok }

	got, used, r := send(KindBoxOffline)
	if got != Sent || used != (Used{Level: "passive"}) {
		t.Errorf("Standard: %q %+v", got, used)
	}
	if field(r, "title") != "Pruefung fuer Juergen" || field(r, "body") != `Groesse - "ok"... Ae` {
		t.Errorf("Texte nicht als ASCII: %q %q", field(r, "title"), field(r, "body"))
	}
	if field(r, "level") != "passive" || has(r, "sound") || has(r, "volume") || has(r, "call") || len(r.Fields) != 3 {
		t.Errorf("leise: %v", r.Fields)
	}

	b.stored("global", map[string]string{"sound": "bell", "critical_volume": "8"})
	_, _, r = send(KindBoxOffline)
	if has(r, "sound") {
		t.Errorf("leise Meldung mit Ton: %v", r.Fields)
	}
	b.stored(KindBoxOffline, map[string]string{"level": "active"})
	_, used, r = send(KindBoxOffline)
	if used.Level != "active" || field(r, "level") != "active" || field(r, "sound") != "bell" || has(r, "volume") {
		t.Errorf("normal: %+v %v", used, r.Fields)
	}
	b.stored(KindBoxOffline, map[string]string{"level": "critical"})
	_, _, r = send(KindBoxOffline)
	if field(r, "level") != "critical" || field(r, "sound") != "bell" || field(r, "volume") != "8" {
		t.Errorf("kritisch: %v", r.Fields)
	}

	// Ruhezeit 11–12 Uhr Berlin (t0 ist 11:00 dort, 10:00 UTC).
	b.stored("global", map[string]string{"quiet_enabled": "1", "quiet_from": "11", "quiet_to": "12", "sound": ""})
	_, used, r = send(KindBoxOffline)
	if used != (Used{Level: "passive", QuietCapped: true}) || field(r, "level") != "passive" || has(r, "volume") {
		t.Errorf("in der Ruhezeit: %+v %v", used, r.Fields)
	}
	b.stored(KindBoxOffline, map[string]string{"ignore_quiet": "1"})
	_, used, r = send(KindBoxOffline)
	if used != (Used{Level: "critical"}) || field(r, "level") != "critical" {
		t.Errorf("ausgenommen: %+v %v", used, r.Fields)
	}
	b.stored(KindBoxOffline, map[string]string{"ignore_quiet": "0", "level": "passive"})
	if _, used, _ = send(KindBoxOffline); used.QuietCapped {
		t.Error("eine leise Meldung gilt nicht als gekappt")
	}
	// Die UTC-Stunde (10) zählt nicht.
	b.stored("global", map[string]string{"quiet_from": "10", "quiet_to": "11"})
	b.stored(KindBoxOffline, map[string]string{"level": "active"})
	if _, used, _ = send(KindBoxOffline); used.QuietCapped {
		t.Error("Ruhezeit nach UTC statt Ortszeit")
	}
}

// Die Info an einen betroffenen Empfänger: leise, ASCII, ein unbrauchbarer
// Key wird nicht versucht.
func TestSendTo(t *testing.T) {
	b := newBench(t)
	ctx := context.Background()
	if !SendTo(ctx, b.c, annaKey, "Wieder laut", "Du bist wieder laut – für alle Alarme.") {
		t.Fatal("nicht angenommen")
	}
	reqs := b.take()
	if len(reqs) != 1 || reqs[0].Key != annaKey || reqs[0].Fields.Get("level") != "passive" ||
		reqs[0].Fields.Get("body") != "Du bist wieder laut - fuer alle Alarme." || len(reqs[0].Fields) != 3 {
		t.Errorf("%+v", reqs)
	}
	if SendTo(ctx, b.c, "x!", "T", "B") || len(b.take()) != 0 {
		t.Error("unbrauchbarer Key versucht")
	}
	b.setAnswer(func(string) int { return 500 })
	if SendTo(ctx, b.c, annaKey, "T", "B") || len(b.take()) != 2 {
		t.Error("Fehlschlag nicht gemeldet oder nicht wiederholt")
	}
}

// Standards folgen der Konfiguration sekundengenau; ein gespeicherter Wert,
// den es nicht (mehr) gibt, fällt auf den Standard.
func TestSettingsDefaults(t *testing.T) {
	b := newBench(t)
	option := func(kind, key string) OptionDef {
		t.Helper()
		def, ok := lookupKind(Types(b.c), kind)
		if !ok {
			t.Fatalf("keine Art %s", kind)
		}
		for _, o := range def.Options {
			if o.Key == key {
				return o
			}
		}
		t.Fatalf("%s ohne %s", kind, key)
		return OptionDef{}
	}
	values := func(o OptionDef) []string {
		var out []string
		for _, c := range o.Choices {
			out = append(out, c.Value)
		}
		return out
	}
	text := func(o OptionDef, value string) string {
		for _, c := range o.Choices {
			if c.Value == value {
				return c.Text
			}
		}
		return "(fehlt)"
	}
	value := func(kind, key string) string { return Settings(b.c).Types[kind].Values[key] }

	// Ab Werk: 600 s (der größere von offline_after_seconds und
	// watchdog_offline_after_seconds), 4 Std für die Demo-Erinnerung.
	o := option(KindBoxOffline, "offline_after_s")
	if o.Default != "600" || value(KindBoxOffline, "offline_after_s") != "600" ||
		!slices.Equal(values(o), []string{"300", "600", "900", "1800", "3600", "7200"}) {
		t.Errorf("ab Werk: %+v", o)
	}
	if v := value("demo_reminder", "after_s"); v != "14400" || len(option("demo_reminder", "after_s").Choices) != 6 {
		t.Errorf("Demo ab Werk: %q", v)
	}

	cases := []struct {
		name               string
		offline, watchdog  int
		demo               int
		wantOffline        string
		wantOfflineText    string
		wantOfflineChoices int
		wantDemo           string
		wantDemoText       string
	}{
		{"777 s", 180, 777, 5000, "777", "777 s (aus config.toml)", 7, "5000", "nach 5000 s (aus config.toml)"},
		{"offline_after_seconds größer", 1000, 600, 5400, "1000", "1000 s (aus config.toml)", 7, "5400", "nach 90 Min (aus config.toml)"},
		{"volle Stunden", 180, 10800, 7200, "10800", "3 Std (aus config.toml)", 7, "7200", "nach 2 Std"},
		{"angebotener Wert", 180, 900, 0, "900", "15 Min", 6, "0", "sofort (aus config.toml)"},
		{"null und negativ", 0, -5, -1, "1", "1 s (aus config.toml)", 7, "0", "sofort (aus config.toml)"},
		{"riesig", 180, math.MaxInt, 600, strconv.Itoa(math.MaxInt), strconv.Itoa(math.MaxInt) + " s (aus config.toml)", 7,
			"600", "nach 10 Min (aus config.toml)"},
	}
	for _, tc := range cases {
		b.setCfg(func(c *config.Config) {
			c.OfflineAfterSeconds, c.WatchdogOfflineAfterSeconds, c.DemoReminderAfterSeconds = tc.offline, tc.watchdog, tc.demo
		})
		o := option(KindBoxOffline, "offline_after_s")
		if value(KindBoxOffline, "offline_after_s") != tc.wantOffline || o.Default != tc.wantOffline ||
			text(o, tc.wantOffline) != tc.wantOfflineText || len(o.Choices) != tc.wantOfflineChoices {
			t.Errorf("%s: offline %q %q, %d Möglichkeiten", tc.name, value(KindBoxOffline, "offline_after_s"),
				text(o, tc.wantOffline), len(o.Choices))
		}
		if !slices.IsSortedFunc(o.Choices, func(a, b Choice) int { return cmp.Compare(atoi(a.Value), atoi(b.Value)) }) {
			t.Errorf("%s: nicht nach Sekunden geordnet: %v", tc.name, values(o))
		}
		d := option("demo_reminder", "after_s")
		if value("demo_reminder", "after_s") != tc.wantDemo || text(d, tc.wantDemo) != tc.wantDemoText {
			t.Errorf("%s: Demo %q %q", tc.name, value("demo_reminder", "after_s"), text(d, tc.wantDemo))
		}
	}

	// Ausdrücklich gespeichert gilt; der Rest folgt weiter der Konfiguration.
	b.setCfg(func(c *config.Config) { c.OfflineAfterSeconds, c.WatchdogOfflineAfterSeconds = 180, 777 })
	b.stored(KindBoxOffline, map[string]string{"offline_after_s": "777", "repeat_hours": "3"})
	if value(KindBoxOffline, "offline_after_s") != "777" || value(KindBoxOffline, "repeat_hours") != "3" {
		t.Errorf("gespeichert: %v", Settings(b.c).Types[KindBoxOffline])
	}
	// Ändert sich die Konfiguration, gibt es 777 nicht mehr: der neue Standard.
	b.setCfg(func(c *config.Config) { c.WatchdogOfflineAfterSeconds = 800 })
	if value(KindBoxOffline, "offline_after_s") != "800" || value(KindBoxOffline, "repeat_hours") != "3" {
		t.Errorf("nach Änderung der Konfiguration: %v", Settings(b.c).Types[KindBoxOffline])
	}

	// Unbekannte gespeicherte Werte: der Standard.
	b.stored(KindBoxOffline, map[string]string{"level": "laut", "enabled": "ja", "farbe": "rot"})
	b.stored(KindLoginSuccess, map[string]string{"enabled": "true", "who": "alle"})
	b.stored("global", map[string]string{"quiet_from": "25", "critical_volume": "08", "extra_id": "9", "farbe": "1"})
	b.stored("gibtesnicht", map[string]string{"enabled": "1"})
	s := Settings(b.c)
	if v := s.Types[KindBoxOffline]; v.Values["level"] != "passive" || !v.Enabled || v.Values["farbe"] != "" {
		t.Errorf("box_offline: %+v", v)
	}
	if v := s.Types[KindLoginSuccess]; v.Enabled || v.Values["who"] != "all" {
		t.Errorf("login_success: %+v", v)
	}
	if s.Global["quiet_from"] != "22" || s.Global["critical_volume"] != "5" || s.Global["extra_id"] != "0" {
		t.Errorf("global: %v", s.Global)
	}
	if _, ok := s.Global["farbe"]; ok {
		t.Error("unbekannte globale Einstellung übernommen")
	}
	if _, ok := s.Types["gibtesnicht"]; ok || Enabled(b.c, "gibtesnicht") {
		t.Error("unbekannte Art übernommen")
	}
	// Vollständig: jede Art, jede Option, jede globale Einstellung.
	for _, def := range Types(b.c) {
		got := s.Types[def.Key]
		if len(got.Values) != len(def.Options) {
			t.Errorf("%s: %d Werte, %d Optionen", def.Key, len(got.Values), len(def.Options))
		}
	}
	if len(s.Global) != len(GlobalSpecs(b.c)) {
		t.Errorf("global: %v", s.Global)
	}

	// Der Außen-Wächter: nur eingerichtet eine Art (und dann ab Werk an).
	if Enabled(b.c, KindOutsideWatcher) {
		t.Error("outside_watcher ohne Außen-Wächter")
	}
	b.setCfg(func(c *config.Config) { c.WatcherTokenSHA256 = strings.Repeat("a", 64) })
	if !Enabled(b.c, KindOutsideWatcher) || Settings(b.c).Types[KindOutsideWatcher].Values["silent_after_s"] != "3600" {
		t.Error("outside_watcher mit Außen-Wächter")
	}
}

func atoi(s string) int {
	var n int
	_, _ = fmt.Sscan(s, &n) // im Test: nur Zahlen
	return n
}

// Nur ein ausdrücklich gespeichertes "aus" schaltet eine wichtige Art ab.
func TestImportantFlags(t *testing.T) {
	b := newBench(t)
	if a, w, o := ImportantFlags(b.c); !a || !w || !o {
		t.Errorf("ab Werk: %v %v %v", a, w, o)
	}
	b.stored(KindNasWeb, map[string]string{"enabled": "0", "level": "critical"})
	b.stored(KindOutsideWatcher, map[string]string{"enabled": "0"})
	b.stored(KindBoxOffline, map[string]string{"enabled": "1"})
	if a, w, o := ImportantFlags(b.c); !a || w || o {
		t.Errorf("gespeichert: %v %v %v", a, w, o)
	}
}

// CheckSetting: nur angebotene Werte, genau so geschrieben; die Texte sind
// die Antworten von notify_set.
func TestCheckSetting(t *testing.T) {
	cfg := &config.Config{OfflineAfterSeconds: 180, WatchdogOfflineAfterSeconds: 777, DemoReminderAfterSeconds: 14400}
	unknown := []struct{ kind, key string }{
		{"gibtesnicht", "enabled"}, {"box_offline", "farbe"}, {"global", "farbe"}, {"", "enabled"},
		{"global", "enabled"}, {"box_offline", ""}, {"outside_watcher", "enabled"}, {"Box_Offline", "level"},
		{"login_lockout", "repeat_hours"},
	}
	for _, u := range unknown {
		if err := CheckSetting(cfg, u.kind, u.key, "1"); !errors.Is(err, ErrUnknownSetting) || err.Error() != "Unbekannte Einstellung." {
			t.Errorf("%s/%s: %v", u.kind, u.key, err)
		}
	}
	invalid := []struct{ kind, key, value, label string }{
		{"box_offline", "level", "laut", "Dringlichkeit"},
		{"box_offline", "level", "Passive", "Dringlichkeit"},
		{"box_offline", "enabled", "2", "Meldung"},
		{"box_offline", "enabled", "", "Meldung"},
		{"box_offline", "enabled", "true", "Meldung"},
		{"box_offline", "offline_after_s", "301", "Als offline melden nach"},
		{"box_offline", "offline_after_s", "600 ", "Als offline melden nach"},
		{"box_offline", "offline_after_s", "0600", "Als offline melden nach"},
		{"global", "critical_volume", "11", "Lautstärke kritischer Meldungen"},
		{"global", "quiet_from", "24", "Ruhezeit von"},
		{"global", "sound", "trompete", "Ton"},
		{"global", "extra_id", "03", "Zusätzlich senden an"},
		{"global", "extra_id", "-1", "Zusätzlich senden an"},
		{"global", "extra_id", "abc", "Zusätzlich senden an"},
		{"global", "extra_id", "", "Zusätzlich senden an"},
		{"global", "extra_id", "99999999999999999999", "Zusätzlich senden an"},
		{"login_success", "who", "jeder", "Melden bei"},
	}
	for _, c := range invalid {
		err := CheckSetting(cfg, c.kind, c.key, c.value)
		var iv *InvalidValueError
		if !errors.As(err, &iv) || iv.Label != c.label ||
			err.Error() != "Ungültiger Wert für „"+c.label+"“ – nichts geändert." {
			t.Errorf("%s/%s=%q: %v", c.kind, c.key, c.value, err)
		}
	}
	valid := []struct{ kind, key, value string }{
		{"box_offline", "level", "timeSensitive"}, {"box_offline", "enabled", "0"}, {"box_offline", "enabled", "1"},
		{"box_offline", "offline_after_s", "777"}, {"box_offline", "offline_after_s", "300"},
		{"global", "critical_volume", "8"}, {"global", "critical_volume", "0"}, {"global", "sound", ""},
		{"global", "quiet_from", "0"}, {"global", "extra_id", "0"}, {"global", "extra_id", "3"},
		{"login_success", "who", "readonly"}, {"weekly_report", "hour", "23"}, {"login_lockout", "ignore_quiet", "1"},
		{"permanent_mute_reminder", "repeat_days", "30"}, {"demo_reminder", "after_s", "14400"},
	}
	for _, c := range valid {
		if err := CheckSetting(cfg, c.kind, c.key, c.value); err != nil {
			t.Errorf("%s/%s=%q: %v", c.kind, c.key, c.value, err)
		}
	}
	// Der Außen-Wächter ist nur eingerichtet eine Art.
	cfg.WatcherTokenSHA256 = strings.Repeat("b", 64)
	if err := CheckSetting(cfg, "outside_watcher", "enabled", "0"); err != nil {
		t.Errorf("outside_watcher mit Außen-Wächter: %v", err)
	}
}

// Apply: Nur was anders ist als der geltende Wert, wird gespeichert; der
// Stellvertreter muss in der Liste stehen.
func TestApply(t *testing.T) {
	cfg := &config.Config{OfflineAfterSeconds: 180, WatchdogOfflineAfterSeconds: 600, DemoReminderAfterSeconds: 14400}
	d := model.NewState()
	d.NextID = 6
	d.Recipients = []model.Recipient{{ID: 3, Key: annaKey, Label: "Anna Müller", MuteToken: "annatoken03"}}

	ch, err := Apply(cfg, d, "box_offline", "offline_after_s", "600")
	if err != nil || ch.Changed || len(d.Notify.Types) != 0 {
		t.Errorf("Standard noch einmal gewählt: %+v %v %v", ch, err, d.Notify)
	}
	ch, err = Apply(cfg, d, "box_offline", "enabled", "1")
	if err != nil || ch.Changed || len(d.Notify.Types) != 0 {
		t.Errorf("ab Werk an, noch einmal an: %+v %v", ch, err)
	}
	ch, err = Apply(cfg, d, "login_success", "enabled", "1")
	want := Change{Changed: true, Scope: "Anmeldung am Dashboard", Option: "Meldung", ValueText: "eingeschaltet"}
	if err != nil || ch != want || d.Notify.Types["login_success"]["enabled"] != "1" {
		t.Errorf("einschalten: %+v %v %v", ch, err, d.Notify)
	}
	ch, err = Apply(cfg, d, "box_offline", "level", "timeSensitive")
	want = Change{Changed: true, Scope: "Box offline / wieder online", Option: "Dringlichkeit", ValueText: "dringend (durchbricht Fokus)"}
	if err != nil || ch != want || d.Notify.Types["box_offline"]["level"] != "timeSensitive" || len(d.Notify.Types["box_offline"]) != 1 {
		t.Errorf("Dringlichkeit: %+v %v %v", ch, err, d.Notify)
	}
	ch, err = Apply(cfg, d, "global", "critical_volume", "8")
	want = Change{Changed: true, Scope: "Alle Meldungen", Option: "Lautstärke kritischer Meldungen", ValueText: "8"}
	if err != nil || ch != want || d.Notify.Global["critical_volume"] != "8" {
		t.Errorf("global: %+v %v %v", ch, err, d.Notify)
	}
	ch, err = Apply(cfg, d, "global", "extra_id", "3")
	if err != nil || !ch.Changed || ch.ValueText != "Anna Müller" || d.Notify.Global["extra_id"] != "3" {
		t.Errorf("Stellvertreter: %+v %v", ch, err)
	}
	var iv *InvalidValueError
	if _, err = Apply(cfg, d, "global", "extra_id", "5"); !errors.As(err, &iv) || iv.Label != "Zusätzlich senden an" {
		t.Errorf("Stellvertreter, den es nicht gibt: %v", err)
	}
	if _, err = Apply(cfg, d, "box_offline", "farbe", "1"); !errors.Is(err, ErrUnknownSetting) {
		t.Errorf("unbekannt: %v", err)
	}
	if _, err = Apply(cfg, d, "box_offline", "level", "laut"); !errors.As(err, &iv) {
		t.Errorf("ungültig: %v", err)
	}
	// Abschalten einer ab Werk eingeschalteten Art wird gespeichert.
	if ch, err = Apply(cfg, d, "nas_web", "enabled", "0"); err != nil || !ch.Changed || ch.ValueText != "abgeschaltet" ||
		d.Notify.Types["nas_web"]["enabled"] != "0" {
		t.Errorf("abschalten: %+v %v", ch, err)
	}
}

// Der Hinweis der Art nas_web nennt, was im Go-Server wirklich ausfällt
// (Spec §7) – nicht mehr "Login und Status der Box scheitern dann".
func TestNasWebHint(t *testing.T) {
	b := newBench(t)
	def, ok := lookupKind(Types(b.c), KindNasWeb)
	want := "Wenn das Dashboard nicht mehr in seinen Datenordner schreiben kann " +
		"(Änderungen werden dann nicht gespeichert, Befehle an die Box nicht ausgeliefert)."
	if !ok || def.Hint != want {
		t.Errorf("Hinweis: %q", def.Hint)
	}
	if strings.Contains(def.Hint, "Login") {
		t.Error("der Hinweis nennt noch die Anmeldung")
	}
}

// Die zwei Meldungen bei der Anmeldung: Text, Art, wer gemeldet wird.
func TestLoginNotices(t *testing.T) {
	b := newBench(t)
	stamp := "[17.11.2026 11:00] "

	LoginNotice(b.c, "admin", "admin", "198.51.100.7")
	if reqs := b.take(); len(reqs) != 0 {
		t.Errorf("ab Werk aus, trotzdem gesendet: %v", reqs)
	}
	b.stored(KindLoginSuccess, map[string]string{"enabled": "1"})
	LoginNotice(b.c, "admin", "admin", "198.51.100.7")
	LoginNotice(b.c, "readonly", "Jürgen", "2001:db8::1")
	reqs := b.take()
	if len(reqs) != 2 {
		t.Fatalf("%d Meldungen", len(reqs))
	}
	for i, want := range []string{
		stamp + "Admin 'admin' hat sich angemeldet (von 198.51.100.7).",
		stamp + "Lese-Benutzer 'Juergen' hat sich angemeldet (von 2001:db8::1).",
	} {
		r := reqs[i]
		if r.Key != statusKey || r.Fields.Get("title") != "Dashboard: Anmeldung" || r.Fields.Get("body") != want ||
			r.Fields.Get("level") != "passive" {
			t.Errorf("Meldung %d: %v", i, r.Fields)
		}
	}
	// Der Name höchstens 40 Bytes (PHP substr); ein angeschnittenes Zeichen wird "?".
	LoginNotice(b.c, "admin", strings.Repeat("x", 39)+"ü", "198.51.100.7")
	if reqs := b.take(); len(reqs) != 1 || !strings.Contains(reqs[0].Fields.Get("body"), "'"+strings.Repeat("x", 39)+"?'") {
		t.Errorf("gekürzt: %v", reqs)
	}
	b.stored(KindLoginSuccess, map[string]string{"who": "admin"})
	LoginNotice(b.c, "readonly", "leser", "198.51.100.7")
	LoginNotice(b.c, "admin", "admin", "198.51.100.7")
	if reqs := b.take(); len(reqs) != 1 || !strings.Contains(reqs[0].Fields.Get("body"), "Admin 'admin'") {
		t.Errorf("nur Admin: %v", reqs)
	}
	b.stored(KindLoginSuccess, map[string]string{"who": "readonly"})
	LoginNotice(b.c, "admin", "admin", "198.51.100.7")
	LoginNotice(b.c, "readonly", "leser", "198.51.100.7")
	if reqs := b.take(); len(reqs) != 1 || !strings.Contains(reqs[0].Fields.Get("body"), "Lese-Benutzer 'leser'") {
		t.Errorf("nur Lese-Benutzer: %v", reqs)
	}

	LockoutNotice(b.c, "198.51.100.66", 900)
	reqs = b.take()
	if len(reqs) != 1 || reqs[0].Fields.Get("title") != "Dashboard: Login gesperrt" ||
		reqs[0].Fields.Get("body") != stamp+"Zu viele fehlgeschlagene Login-Versuche von 198.51.100.66 - IP fuer 15 min gesperrt." ||
		reqs[0].Fields.Get("level") != "passive" {
		t.Errorf("Sperre: %v", reqs)
	}
	LockoutNotice(b.c, "203.0.113.9", 119) // ganze Minuten, abgerundet
	if reqs := b.take(); len(reqs) != 1 || !strings.HasSuffix(reqs[0].Fields.Get("body"), "IP fuer 1 min gesperrt.") {
		t.Errorf("Minuten: %v", reqs)
	}
	b.stored(KindLoginLockout, map[string]string{"enabled": "0"})
	LockoutNotice(b.c, "203.0.113.9", 900)
	if reqs := b.take(); len(reqs) != 0 {
		t.Errorf("abgeschaltet, trotzdem gesendet: %v", reqs)
	}
}
