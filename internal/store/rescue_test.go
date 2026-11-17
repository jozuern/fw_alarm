package store_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"fwalarm/internal/model"
	"fwalarm/internal/store"
	"fwalarm/internal/store/storetest"
)

// emergencyWith: ein Notbetrieb (state.json kaputt, keine Vorfassung) mit
// dieser Not-Kopie; rescue nil = keine.
func emergencyWith(t *testing.T, rescue []byte) (*store.Store, *storetest.FaultFS) {
	t.Helper()
	f := storetest.New()
	f.Put(at("state.json"), garbage)
	if rescue != nil {
		f.Put(at("recipients.rescue.json"), rescue)
	}
	s, info := open(t, f)
	if info.Mode != store.StartEmergency {
		t.Fatalf("Start: %s", mode(info.Mode))
	}
	return s, f
}

// Die Not-Kopie wird im Notbetrieb EINMAL nachsichtig gelesen und dann aus
// dem Speicher geliefert, wie sie gelesen wurde – ohne Auslegung (die Regel
// ist model.Rescue.Targets). Ein Alarm liest nie eine Datei.
func TestRescueReadIsLenient(t *testing.T) {
	items := []string{`"kein Objekt"`, `17`, `null`,
		`{"id": 1, "key": "kurz", "label": "zu kurz"}`,
		`{"id": 2, "key": " testkey-r-02 ", "label": "Zwei", "muted": "1", "muted_at": 5, "mute_volume": 99}`,
		`{"id": 3, "key": "testkey-r-02", "label": "doppelt"}`,
		`{"id": "4", "label": "ohne Key"}`}
	for i := 5; i <= 16; i++ {
		items = append(items, fmt.Sprintf(`{"id": %d, "key": "testkey-r-%02d", "label": "N%d", "unbekannt": [1]}`, i, i, i))
	}
	text := `{"schema": 7, "written_at": "gestern", "demo_key_id": 0, "recipients": [` + strings.Join(items, ",") + `], "mehr": 1}`
	s, f := emergencyWith(t, []byte(text))

	got, ok := s.Rescue()
	want := model.DecodeRescueLenient([]byte(text))
	if !ok || !reflect.DeepEqual(got, want) || len(got.Recipients) != model.MaxRecipients {
		t.Fatalf("ok=%v %+v", ok, got)
	}
	if got.Recipients[0] != (model.RescueRecipient{ID: 2, Key: "testkey-r-02", Label: "Zwei", Muted: true, MutedAt: 5, MuteVolume: 10}) {
		t.Errorf("erster: %+v", got.Recipients[0])
	}
	targets, demo, ok := got.Targets(t0, 0)
	if !ok || demo || len(targets) != model.MaxRecipients || targets[1].ID != 5 {
		t.Errorf("Empfänger: ok=%v demo=%v %+v", ok, demo, targets)
	}

	// Aus dem Speicher: eine geänderte Datei ändert nichts, eine geänderte
	// Antwort auch nicht; gelesen wurde einmal.
	f.Put(at("recipients.rescue.json"), []byte(`{"recipients": []}`))
	got.Recipients[0].Key = "verändert vom Aufrufer"
	again, ok := s.Rescue()
	if !ok || !reflect.DeepEqual(again, want) {
		t.Errorf("zweiter Aufruf: ok=%v %+v", ok, again)
	}
	reads := 0
	for _, c := range f.Log() {
		if c.Op == "ReadFile" && contains(c.Names, at("recipients.rescue.json")) {
			reads++
		}
	}
	if reads != 1 {
		t.Errorf("Not-Kopie %d-mal gelesen", reads)
	}
}

// Die Kopie kommt samt Demo-Verweis zurück; mit model.Rescue.Targets geht
// der Alarm dann nur an einen gültigen Demo-Empfänger.
func TestRescueDemoOnly(t *testing.T) {
	copyWith := func(demo int) []byte {
		return doc(t, model.Rescue{Schema: 1, DemoKeyID: demo, Recipients: []model.RescueRecipient{
			{ID: 3, Key: "testkey-anna-01", Label: "Anna"},
			{ID: 7, Key: "testkey-bert-02", Label: "Bert", Muted: true, MutedAt: t0.Unix() - 60, MuteVolume: 2},
		}})
	}
	s, _ := emergencyWith(t, copyWith(7))
	r, ok := s.Rescue()
	if !ok || r.DemoKeyID != 7 {
		t.Fatalf("ok=%v %+v", ok, r)
	}
	got, demo, ok := r.Targets(t0, 43200)
	if !ok || !demo || len(got) != 1 || got[0] != (model.Target{ID: 7, Key: "testkey-bert-02", Label: "Bert", Muted: true, MuteVolume: 2}) {
		t.Errorf("Demo-Empfänger 7: ok=%v demo=%v %+v", ok, demo, got)
	}
	s, _ = emergencyWith(t, copyWith(99)) // Verweis ins Leere: lieber alle
	r, _ = s.Rescue()
	if got, demo, ok := r.Targets(t0, 43200); !ok || demo || len(got) != 2 {
		t.Errorf("Demo-Verweis ins Leere: ok=%v demo=%v %+v", ok, demo, got)
	}
}

// Ohne gültigen Key in der Kopie: Targets meldet ok=false (dann gilt die
// Not-Liste der Konfiguration, Aufgabe 20). Ohne lesbare Kopie, oder
// außerhalb des Notbetriebs, meldet schon Store.Rescue ok=false.
func TestRescueWithoutValidKey(t *testing.T) {
	for name, content := range map[string][]byte{
		"nur ungültige Keys": []byte(`{"schema": 1, "recipients": [{"id": 1, "key": "kurz"}, {"id": 2, "key": "mit leer zeichen"}]}`),
		"leere Liste":        []byte(`{"schema": 1, "recipients": []}`),
		"kein JSON":          garbage,
	} {
		s, _ := emergencyWith(t, content)
		r, read := s.Rescue()
		if !read {
			t.Errorf("%s: gelesene Kopie nicht geliefert", name)
		}
		if got, _, ok := r.Targets(t0, 0); ok || len(got) != 0 {
			t.Errorf("%s: ok=%v %+v", name, ok, got)
		}
	}

	s, _ := emergencyWith(t, nil)
	if r, ok := s.Rescue(); ok || len(r.Recipients) != 0 {
		t.Errorf("keine Kopie: ok=%v %+v", ok, r)
	}
	f := storetest.New()
	f.Put(at("state.json"), garbage)
	f.PutDir(at("recipients.rescue.json"))
	s, _ = open(t, f)
	if r, ok := s.Rescue(); ok || len(r.Recipients) != 0 {
		t.Errorf("Ordner statt Kopie: ok=%v %+v", ok, r)
	}

	// Außerhalb des Notbetriebs wird die Kopie nicht gelesen.
	g := storetest.New()
	plant(t, g, "state.json", sample(12))
	g.Put(at("recipients.rescue.json"), doc(t, model.Rescue{Schema: 1, Recipients: []model.RescueRecipient{
		{ID: 3, Key: "testkey-anna-01"}}}))
	s, _ = open(t, g)
	if r, ok := s.Rescue(); ok || len(r.Recipients) != 0 {
		t.Errorf("normaler Start: ok=%v %+v", ok, r)
	}
	for _, c := range g.Log() {
		if c.Op == "ReadFile" && contains(c.Names, at("recipients.rescue.json")) {
			t.Errorf("Not-Kopie im normalen Betrieb gelesen: %v", c)
		}
	}
}
