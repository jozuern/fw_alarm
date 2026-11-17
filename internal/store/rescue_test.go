package store_test

import (
	"fmt"
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

// Die Not-Kopie wird im Notbetrieb EINMAL nachsichtig gelesen: die
// gültigen, einmaligen Keys, höchstens 10 – lieber die gültigen als keiner.
// Danach kommt sie aus dem Speicher; ein Alarm liest nie eine Datei.
func TestRescueReadIsLenient(t *testing.T) {
	items := []string{`"kein Objekt"`, `17`, `null`,
		`{"id": 1, "key": "kurz", "label": "zu kurz"}`,
		`{"id": 2, "key": " testkey-r-02 ", "label": "Zwei", "muted": "1", "mute_volume": 99}`,
		`{"id": 3, "key": "testkey-r-02", "label": "doppelt"}`,
		`{"id": "4", "label": "ohne Key"}`}
	for i := 5; i <= 16; i++ {
		items = append(items, fmt.Sprintf(`{"id": %d, "key": "testkey-r-%02d", "label": "N%d", "unbekannt": [1]}`, i, i, i))
	}
	text := `{"schema": 7, "written_at": "gestern", "recipients": [` + strings.Join(items, ",") + `], "mehr": 1}`
	s, f := emergencyWith(t, []byte(text))

	got, ok := s.Rescue()
	if !ok || len(got) != model.MaxRecipients {
		t.Fatalf("ok=%v, %d Empfänger: %+v", ok, len(got), got)
	}
	if got[0] != (model.Target{ID: 2, Key: "testkey-r-02", Label: "Zwei", Muted: true, MuteVolume: 10}) {
		t.Errorf("erster: %+v", got[0])
	}
	for i, tg := range got[1:] {
		if tg.ID != i+5 || tg.Key != fmt.Sprintf("testkey-r-%02d", i+5) {
			t.Errorf("Eintrag %d: %+v", i+1, tg)
		}
	}

	// Aus dem Speicher: eine geänderte Datei ändert nichts, gelesen wurde einmal.
	f.Put(at("recipients.rescue.json"), []byte(`{"recipients": []}`))
	got[0].Key = "verändert vom Aufrufer"
	again, ok := s.Rescue()
	if !ok || len(again) != model.MaxRecipients || again[0].Key != "testkey-r-02" {
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

// Steht ein gültiger Demo-Empfänger in der Kopie, geht der Alarm nur an ihn.
func TestRescueDemoOnly(t *testing.T) {
	copyWith := func(demo int) []byte {
		return doc(t, model.Rescue{Schema: 1, DemoKeyID: demo, Recipients: []model.RescueRecipient{
			{ID: 3, Key: "testkey-anna-01", Label: "Anna"},
			{ID: 7, Key: "testkey-bert-02", Label: "Bert", Muted: true, MuteVolume: 2},
		}})
	}
	s, _ := emergencyWith(t, copyWith(7))
	got, ok := s.Rescue()
	if !ok || len(got) != 1 || got[0] != (model.Target{ID: 7, Key: "testkey-bert-02", Label: "Bert", Muted: true, MuteVolume: 2}) {
		t.Errorf("Demo-Empfänger 7: ok=%v %+v", ok, got)
	}
	s, _ = emergencyWith(t, copyWith(99)) // Verweis ins Leere: lieber alle
	if got, ok := s.Rescue(); !ok || len(got) != 2 {
		t.Errorf("Demo-Verweis ins Leere: ok=%v %+v", ok, got)
	}
}

// Ohne brauchbare Not-Kopie: ok=false (dann gilt die Not-Liste der
// Konfiguration, Aufgabe 20).
func TestRescueWithoutValidKey(t *testing.T) {
	for name, content := range map[string][]byte{
		"nur ungültige Keys": []byte(`{"schema": 1, "recipients": [{"id": 1, "key": "kurz"}, {"id": 2, "key": "mit leer zeichen"}]}`),
		"leere Liste":        []byte(`{"schema": 1, "recipients": []}`),
		"kein JSON":          garbage,
		"keine Kopie":        nil,
	} {
		s, _ := emergencyWith(t, content)
		if got, ok := s.Rescue(); ok || len(got) != 0 {
			t.Errorf("%s: ok=%v %+v", name, ok, got)
		}
	}

	// Außerhalb des Notbetriebs wird die Kopie nicht gelesen.
	f := storetest.New()
	plant(t, f, "state.json", sample(12))
	f.Put(at("recipients.rescue.json"), doc(t, model.Rescue{Schema: 1, Recipients: []model.RescueRecipient{
		{ID: 3, Key: "testkey-anna-01"}}}))
	s, _ := open(t, f)
	if got, ok := s.Rescue(); ok || len(got) != 0 {
		t.Errorf("normaler Start: ok=%v %+v", ok, got)
	}
	for _, c := range f.Log() {
		if c.Op == "ReadFile" && contains(c.Names, at("recipients.rescue.json")) {
			t.Errorf("Not-Kopie im normalen Betrieb gelesen: %v", c)
		}
	}
}
