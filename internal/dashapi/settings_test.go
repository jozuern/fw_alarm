package dashapi

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fwalarm/internal/config"
	"fwalarm/internal/model"
)

// settings_list/settings_set: feste Stufen plus der Wert der Konfiguration
// (als "(Standard)"), Zahlen als Zahlen; nur angebotene Werte; gespeichert
// wird der gewählte; Verlauf setting_changed in derselben Änderung.
func TestSettings(t *testing.T) {
	b := newBench(t)
	b.setCfg(func(c *config.Config) { c.MuteMaxSeconds = 43200 })
	admin := b.admin()
	list := func() string {
		t.Helper()
		rec := b.serve(req("GET", "action=settings_list", nil, admin.cookie))
		if rec.Code != 200 {
			t.Fatalf("%d", rec.Code)
		}
		return rec.Body.String()
	}
	steps := `{"value":7200,"text":"2 Std"},{"value":14400,"text":"4 Std"},{"value":21600,"text":"6 Std"},` +
		`{"value":28800,"text":"8 Std"},{"value":43200,"text":"12 Std (Standard)"},{"value":57600,"text":"16 Std"},` +
		`{"value":86400,"text":"24 Std"}`
	if got := list(); got != `{"ok":true,"settings":{"mute_max_seconds":{"value":43200,"default":43200,"choices":[`+steps+`]}}}` {
		t.Errorf("Standard: %s", got)
	}

	for _, v := range []string{"999", "0", "3600", "43201", "-7200", "7200.0", "abc", "", "07200"} {
		admin.want(`{"ok":false,"message":"Ungültiger Wert – nichts geändert."}`, "action", "settings_set", "key", "mute_max_seconds", "value", v)
	}
	admin.want(`{"ok":false,"message":"Unbekannte Einstellung."}`, "action", "settings_set", "key", "foo", "value", "7200")
	admin.want(`{"ok":true,"changed":true,"message":"Gespeichert – Automatisch wieder laut nach: 4 Std."}`,
		"action", "settings_set", "key", "mute_max_seconds", "value", " 14400 ")
	admin.want(`{"ok":true,"changed":false,"message":"Ist bereits so eingestellt."}`, "action", "settings_set", "key", "mute_max_seconds", "value", "14400")
	if st := b.c.Store.State(); st.Settings.MuteMaxSeconds != 14400 || st.KeysVersion != 0 {
		t.Errorf("gespeichert: %+v, Version %d", st.Settings, st.KeysVersion)
	}
	if e := b.events(model.EvSettingChanged); len(e) != 1 ||
		fields(e[0]) != "event=setting_changed by=admin label=Automatisch wieder laut nach value=4 Std" {
		t.Errorf("Verlauf: %v", e)
	}
	if got := list(); got != `{"ok":true,"settings":{"mute_max_seconds":{"value":14400,"default":43200,"choices":[`+steps+`]}}}` {
		t.Errorf("nach dem Setzen: %s", got)
	}

	// Ein eigener Wert der Konfiguration steht unverändert in der Auswahl; 0
	// heißt "nie".
	b.setCfg(func(c *config.Config) { c.MuteMaxSeconds = 5400 })
	b.update(func(d *model.State) { d.Settings.MuteMaxSeconds = 0 })
	if got := list(); got != phpText(`{"ok":true,"settings":{"mute_max_seconds":{"value":5400,"default":5400,"choices":[{"value":5400,"text":"90 Min (Standard)"},`+
		`{"value":7200,"text":"2 Std"},{"value":14400,"text":"4 Std"},{"value":21600,"text":"6 Std"},{"value":28800,"text":"8 Std"},`+
		`{"value":43200,"text":"12 Std"},{"value":57600,"text":"16 Std"},{"value":86400,"text":"24 Std"}]}}}`) {
		t.Errorf("90 Min: %s", got)
	}
	b.setCfg(func(c *config.Config) { c.MuteMaxSeconds = 0 })
	admin.want(`{"ok":true,"changed":true,"message":"Gespeichert – Automatisch wieder laut nach: 2 Std."}`,
		"action", "settings_set", "key", "mute_max_seconds", "value", "7200")
	admin.want(`{"ok":true,"changed":true,"message":"Gespeichert – Automatisch wieder laut nach: nie."}`,
		"action", "settings_set", "key", "mute_max_seconds", "value", "0")
	if e := b.events(model.EvSettingChanged); len(e) != 3 || e[0].Str("value") != "nie" {
		t.Errorf("Verlauf: %v", e)
	}
}

// Die Sätze, wenn eine Änderung nicht gesichert wird (Plan "Texts this plan
// fixes"): Sichern scheitert → der bekannte Satz; Frist abgelaufen → "dauert
// zu lange" (beide 500, code storage); Notbetrieb → der Satz zum Notbetrieb
// (TestKeysListEmergency). Nie ein Pfad oder eine Ursache in der Antwort.
func TestStorageSentences(t *testing.T) {
	t.Parallel() // wartet einmal die 5 s der Oberfläche ab
	b := newBench(t)
	b.seed(3, anna)
	admin := b.admin()
	tmp := filepath.Join(benchDir, "state.json.tmp")

	b.fs.Fail(tmp, errors.New("Zugriff verweigert auf /daten/geheim"))
	for _, kv := range [][]string{
		{"action", "keys_add", "label", "Bert", "key", "bertkey00002"},
		{"action", "keys_rename", "id", "1", "label", "Anna B."},
		{"action", "settings_set", "key", "mute_max_seconds", "value", "7200"},
	} {
		code, body := admin.do(kv...)
		if code != 500 || body != phpText(`{"ok":false,"code":"storage","message":"Fehler: Datenordner nicht beschreibbar (Rechte des Web-Benutzers auf dem Server prüfen)."}`) {
			t.Errorf("%v: %d %s", kv, code, body)
		}
	}
	b.fs.Fail(tmp, nil)
	if st := b.c.Store.State(); len(st.Recipients) != 1 || st.Recipients[0].Label != "Anna" || st.Settings.MuteMaxSeconds != 0 {
		t.Fatalf("trotz Fehler geändert: %+v", st)
	}

	// Das Sichern hängt: nach der Frist der Oberfläche "dauert zu lange" – und
	// wird es danach doch fertig, gilt die Änderung samt Verlauf.
	release := b.fs.Hang(tmp)
	start := time.Now()
	code, body := admin.do("action", "keys_rename", "id", "1", "label", "Anna B.")
	if d := time.Since(start); d < 4500*time.Millisecond || d > 8*time.Second {
		t.Errorf("Antwort nach %v, erwartet nach etwa 5 s", d)
	}
	if code != 500 || body != phpText(`{"ok":false,"code":"storage","message":"Fehler: Speichern dauert zu lange – bitte den angezeigten Stand prüfen."}`) {
		t.Errorf("Frist: %d %s", code, body)
	}
	release()
	deadline := time.Now().Add(5 * time.Second)
	for b.c.Store.State().Recipients[0].Label != "Anna B." && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if st := b.c.Store.State(); st.Recipients[0].Label != "Anna B." || len(b.events(model.EvKeyRenamed)) != 1 {
		t.Errorf("nach der Frist fertig, gilt aber nicht: %+v", st.Recipients[0])
	}

	// Die Frist gilt je Anfrage: keys_mute schaltet erst Überfälliges zurück
	// und ändert dann – beides in DERSELBEN Frist. Hängt das Sichern, kommt
	// die Antwort nach etwa 5 s, nicht nach 10.
	b.setCfg(func(c *config.Config) { c.MuteMaxSeconds = 43200 })
	b.update(func(d *model.State) { d.Recipients[0].Muted, d.Recipients[0].MutedAt = true, 1 })
	release = b.fs.Hang(tmp)
	start = time.Now()
	code, body = admin.do("action", "keys_mute", "id", "1", "muted", "0")
	if d := time.Since(start); d < 4500*time.Millisecond || d > 7*time.Second {
		t.Errorf("keys_mute: Antwort nach %v, erwartet nach etwa 5 s", d)
	}
	if code != 500 || !strings.Contains(body, `"code":"storage"`) || !strings.Contains(body, "dauert zu lange") {
		t.Errorf("keys_mute: %d %s", code, body)
	}
	release()
}
