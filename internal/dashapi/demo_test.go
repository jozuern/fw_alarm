package dashapi

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"fwalarm/internal/model"
)

// Demo-Modus und Listen-Version ändern sich in EINER Änderung: Scheitert das
// Sichern, hat sich keins von beiden geändert; gelingt es, sieht jeder
// Schnappschuss beide zugleich – nie den neuen Modus unter der alten Version
// oder umgekehrt (PHP: Schreibreihenfolge, Zurückrollen, code=inconsistent –
// entfällt, Spec §7).
func TestDemoSetIsOneChange(t *testing.T) {
	b := newBench(t)
	b.seed(10, anna, bert)
	admin := b.admin()
	tmp := filepath.Join(benchDir, "state.json.tmp")

	b.fs.Fail(tmp, errors.New("Platte voll"))
	code, body := admin.do("action", "demo_set", "enabled", "1", "id", "2")
	if code != 500 || body != phpText(`{"ok":false,"code":"storage","message":"Fehler: Datenordner nicht beschreibbar (Rechte des Web-Benutzers auf dem Server prüfen)."}`) {
		t.Errorf("%d %s", code, body)
	}
	if st := b.c.Store.State(); st.Demo.Enabled || st.KeysVersion != 10 || len(b.events(model.EvDemoEnabled)) != 0 {
		t.Fatalf("Sichern gescheitert, aber geändert: Demo %+v, Version %d", st.Demo, st.KeysVersion)
	}
	b.fs.Fail(tmp, nil)

	// Während des Wechsels (an, aus, an …) liest ein Leser ständig mit: Jeder
	// Schnappschuss hat bei gerader Version LIVE, bei ungerader Demo.
	var stop atomic.Bool
	var wg sync.WaitGroup
	var bad atomic.Value
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			st := b.c.Store.State()
			if st.Demo.Enabled != (st.KeysVersion%2 == 1) {
				bad.Store(fmt.Sprintf("Demo %v unter Version %d", st.Demo.Enabled, st.KeysVersion))
			}
		}
	}()
	for i := range 20 {
		enable := i%2 == 0
		kv := []string{"action", "demo_set", "enabled", "0"}
		if enable {
			kv = []string{"action", "demo_set", "enabled", "1", "id", "2"}
		}
		if code, body := admin.do(kv...); code != 200 || !strings.Contains(body, `"ok":true`) {
			t.Fatalf("%d %s", code, body)
		}
	}
	stop.Store(true)
	wg.Wait()
	if v := bad.Load(); v != nil {
		t.Error(v)
	}
	if st := b.c.Store.State(); st.Demo.Enabled || st.KeysVersion != 30 {
		t.Errorf("Ende: Demo %+v, Version %d", st.Demo, st.KeysVersion)
	}

	// Einschalten: genau der Empfänger aus der Liste, mit Key, Name, Zeit,
	// wer; Verlauf demo_enabled mit der neuen Version.
	b.clk.Advance(60e9)
	admin.want(`{"ok":true,"message":"DEMO-MODUS aktiviert: Alarme gehen nur noch an \"Bert\". WICHTIG: Die Box übernimmt das erst nach einigen Sekunden – Banner oben beobachten!","demo":{"enabled":true,"label":"Bert","key_id":2,"key_masked":"bert...","changed_at":`+fmt.Sprint(t0.Unix()+60)+`}}`,
		"action", "demo_set", "enabled", "1", "id", "2")
	if st := b.c.Store.State(); st.Demo != (model.Demo{Enabled: true, KeyID: 2, Key: bert.Key, Label: "Bert", ChangedAt: t0.Unix() + 60, By: "admin"}) || st.KeysVersion != 31 {
		t.Errorf("Demo %+v, Version %d", st.Demo, st.KeysVersion)
	}
	if got := fields(b.events(model.EvDemoEnabled)[0]); got != "event=demo_enabled by=admin label=Bert version=31" {
		t.Errorf("Verlauf: %s", got)
	}
	// Nicht aus der Liste: abgelehnt, nichts geändert.
	admin.want(`{"ok":false,"message":"Demo-Empfänger nicht gefunden. Zuerst im Panel \"Alarm-Empfänger\" anlegen, dann Demo-Modus einschalten."}`,
		"action", "demo_set", "enabled", "1", "id", "99")
	if st := b.c.Store.State(); st.Demo.KeyID != 2 || st.KeysVersion != 31 {
		t.Errorf("abgelehnt, aber geändert: %+v %d", st.Demo, st.KeysVersion)
	}
}

// Ausschalten ist idempotent und erhöht IMMER die Version (der Reparaturweg,
// falls die Box auf einer alten Liste sitzt); jedes Mal ein Eintrag
// demo_disabled.
func TestDemoOffIsIdempotentAndBumps(t *testing.T) {
	b := newBench(t)
	b.seed(10, anna, bert)
	admin := b.admin()
	off := `{"ok":true,"message":"Der Demo-Modus war bereits aus – die volle Liste wird der Box erneut angeboten. Die Box übernimmt die volle Liste nach wenigen Sekunden.","demo":{"enabled":false,"label":"","key_id":0,"key_masked":"","changed_at":` + fmt.Sprint(t0.Unix()) + `}}`
	admin.want(off, "action", "demo_set", "enabled", "0")
	admin.want(off, "action", "demo_set", "enabled", "0", "id", "1")
	if v := b.c.Store.State().KeysVersion; v != 12 {
		t.Errorf("Version %d, erwartet 12", v)
	}
	if e := b.events(model.EvDemoDisabled); len(e) != 2 || fields(e[0]) != "event=demo_disabled by=admin label= version=12" {
		t.Errorf("Verlauf: %v", e)
	}
	// Aus einem echten Demo-Modus: der andere Satz, Name/Key/id fort.
	admin.want(`{"ok":true,"message":"DEMO-MODUS aktiviert: Alarme gehen nur noch an \"Anna\". WICHTIG: Die Box übernimmt das erst nach einigen Sekunden – Banner oben beobachten!","demo":{"enabled":true,"label":"Anna","key_id":1,"key_masked":"anna...","changed_at":`+fmt.Sprint(t0.Unix())+`}}`,
		"action", "demo_set", "enabled", "1", "id", "1")
	admin.want(`{"ok":true,"message":"LIVE-Modus aktiv: Alarme gehen wieder an ALLE Empfänger. Die Box übernimmt die volle Liste nach wenigen Sekunden.","demo":{"enabled":false,"label":"","key_id":0,"key_masked":"","changed_at":`+fmt.Sprint(t0.Unix())+`}}`,
		"action", "demo_set", "enabled", "0")
	if st := b.c.Store.State(); st.Demo != (model.Demo{ChangedAt: t0.Unix(), By: "admin"}) || st.KeysVersion != 14 {
		t.Errorf("Demo %+v, Version %d", st.Demo, st.KeysVersion)
	}
}

// Ältere Firmware sperrt nach einem Demo-Alarm 300 s lang jeden Relaisalarm –
// wer kurz danach auf LIVE schaltet, wird gewarnt (PHP:
// demo_alarm_cooldown_warning). Firmware mit alarm_seq tut das nicht.
func TestDemoCooldownWarning(t *testing.T) {
	b := newBench(t)
	b.seed(10, anna, bert)
	admin := b.admin()
	alarm := func(state, result string, deliveredAgo int64) {
		b.update(func(d *model.State) {
			d.Commands.NextID = 8
			d.Commands.Current = &model.Command{ID: 7, Type: "ALARM", State: state, AckResult: result, DeliveredAt: t0.Unix() - deliveredAgo}
		})
	}
	oldBox := &model.BoxStatus{SeenAt: t0.Unix(), Fields: model.Fields{{Name: "device_id", Value: "fw-box-1"}}}
	warn := " ACHTUNG: Die Box hat gerade einen DEMO-ALARM ausgeführt und erkennt bis ca. 11:04 Uhr keinen neuen " +
		"Relaisalarm (Sperrzeit nach jedem Alarm). Im Ernstfall bis dahin den REAL ALARM benutzen."
	base := "Der Demo-Modus war bereits aus – die volle Liste wird der Box erneut angeboten. Die Box übernimmt die volle Liste nach wenigen Sekunden."
	for _, tc := range []struct {
		name          string
		box           *model.BoxStatus
		state, result string
		ago           int64
		warn          bool
	}{
		{"ausgeliefert vor 60 s, alte Firmware", oldBox, model.CommandDelivered, "", 60, true},
		{"ausgeführt quittiert", oldBox, model.CommandAcked, "executed", 60, true},
		{"nie gemeldet: wie alte Firmware", nil, model.CommandDelivered, "", 60, true},
		{"abgelehnt quittiert", oldBox, model.CommandAcked, "skipped_busy", 60, false},
		{"Sperrzeit vorbei", oldBox, model.CommandDelivered, "", 300, false},
		{"neue Firmware (alarm_seq)", &model.BoxStatus{SeenAt: t0.Unix(), Fields: model.Fields{{Name: "alarm_seq", Value: ""}}}, model.CommandDelivered, "", 60, false},
		{"wartet noch", oldBox, model.CommandPending, "", 60, false},
	} {
		b.setBox(tc.box)
		alarm(tc.state, tc.result, tc.ago)
		_, body := admin.do("action", "demo_set", "enabled", "0")
		want := base
		if tc.warn {
			want += warn
		}
		if !strings.Contains(body, `"message":"`+phpText(want)+`"`) {
			t.Errorf("%s: %s", tc.name, body)
		}
	}
	// Ein TEST-Befehl warnt nie.
	b.setBox(oldBox)
	b.update(func(d *model.State) { d.Commands.Current.Type = "TEST"; d.Commands.Current.DeliveredAt = t0.Unix() })
	if _, body := admin.do("action", "demo_set", "enabled", "0"); strings.Contains(body, "ACHTUNG") {
		t.Errorf("TEST: %s", body)
	}
}

// Die Teile der Antwort von status: Demo-Modus, Listen-Version des Servers
// und ob die Box die Demo-Liste übernommen hat – nach DERSELBEN Regel wie die
// Sperre des Demo-Alarms (Version, genau ein Empfänger, frisch).
func TestDemoStatusParts(t *testing.T) {
	b := newBench(t)
	b.seed(10, anna, bert)
	reader := b.reader()
	part := func(s, key string) string {
		i := strings.Index(s, `"`+key+`":`)
		if i < 0 {
			t.Fatalf("%s fehlt: %s", key, s)
		}
		rest := s[i+len(key)+3:]
		end := strings.Index(rest, `,"device_hint"`)
		if key == "demo" {
			end = strings.Index(rest, `},`) + 1
		} else if key == "nas_keys_version" {
			end = strings.Index(rest, `,`)
		}
		return rest[:end]
	}
	s := b.statusOf(reader.cookie)
	if part(s, "demo") != `{"enabled":false,"label":"","key_id":0,"key_masked":"","changed_at":0}` ||
		part(s, "nas_keys_version") != "10" || part(s, "demo_sync_ok") != "false" {
		t.Errorf("LIVE: %s", s)
	}
	b.update(func(d *model.State) {
		d.Demo = model.Demo{Enabled: true, KeyID: 2, Key: bert.Key, Label: "Bert", ChangedAt: 77, By: "admin"}
		d.KeysVersion = 11
	})
	box := func(version, count string, ago int64) {
		b.setBox(&model.BoxStatus{SeenAt: t0.Unix() - ago, Fields: model.Fields{{Name: "keys_version", Value: version}, {Name: "keys_count", Value: count}}})
	}
	for _, tc := range []struct {
		name           string
		version, count string
		ago            int64
		ok             bool
	}{
		{"übernommen", "11", "1", 10, true},
		{"alte Version", "10", "1", 10, false},
		{"volle Liste", "11", "2", 10, false},
		{"zu alt", "11", "1", 181, false},
		{"gerade noch frisch", "11", "1", 180, true},
	} {
		box(tc.version, tc.count, tc.ago)
		s := b.statusOf(reader.cookie)
		if part(s, "demo") != `{"enabled":true,"label":"Bert","key_id":2,"key_masked":"bert...","changed_at":77}` ||
			part(s, "nas_keys_version") != "11" || part(s, "demo_sync_ok") != fmt.Sprint(tc.ok) {
			t.Errorf("%s: %s", tc.name, s)
		}
	}
}
