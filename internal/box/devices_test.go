package box

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fwalarm/internal/config"
	"fwalarm/internal/dashapi"
	"fwalarm/internal/model"
	"fwalarm/internal/web"
)

// statusAnswer: die Antwort der Aktion status für eine Rolle (admin oder
// readonly – dann als Lese-Benutzer "leser").
func (b *bench) statusAnswer(role string) map[string]any {
	b.t.Helper()
	user := "admin"
	if role != "admin" {
		user = "leser"
		b.configure(func(c *config.Config) { c.DashboardReadonlyUser, c.DashboardReadonlyPasswordHash = user, "hash-leser" })
	}
	rec := httptest.NewRecorder()
	web.Begin(b.c, rec, httptest.NewRequest(http.MethodPost, "/index.php", nil), user, role)
	var cookie string
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "boss925_dashboard" {
			cookie = ck.Value
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/dashboard_api.php?action=status", nil)
	r.Header.Set("Cookie", "boss925_dashboard="+cookie)
	w := httptest.NewRecorder()
	web.SecurityHeaders(b.c, dashapi.Handler(b.c))(w, r)
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
		b.t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}
	return out
}

// ageBoard: der letzte Push dieses Boards liegt s Sekunden zurück.
func (b *bench) ageBoard(id string, s int64) {
	b.c.Store.UpdateRuntime(func(d *model.Runtime) {
		dev := d.Devices[id]
		dev.SeenAt = b.c.Clock.Now().Unix() - s
		d.Devices[id] = dev
	})
}

// Der Teil device_hint der Antwort von status – für Admin und Lese-Rolle
// gleich (PHP: device_hint($config) für alle) – mit box_device_id (ohne
// Leerraum außen) und offline_after_seconds aus der Konfiguration.
func TestDeviceHintPart(t *testing.T) {
	b := newBench(t)
	if got := b.statusAnswer("admin")["device_hint"]; got != "" {
		t.Errorf("nie ein Push: %q", got)
	}
	b.push(fwNew)
	b.push(fwOld, "device_id", "bastel-1")
	for _, role := range []string{"admin", "readonly"} {
		if got, _ := b.statusAnswer(role)["device_hint"].(string); !strings.HasPrefix(got, "Mehrere Boards melden sich mit demselben Maschinen-Token („bastel-1“, „fw-box-1“)") ||
			!strings.Contains(got, "In config.toml box_device_id") {
			t.Errorf("%s: %q", role, got)
		}
	}
	// Schweigt die Box seit 400 s: bei offline_after_seconds 180 ACHTUNG, bei
	// 600 noch nicht.
	b.ageBoard("fw-box-1", 400)
	if got, _ := b.statusAnswer("admin")["device_hint"].(string); !strings.HasPrefix(got, "ACHTUNG: „fw-box-1“ meldet sich seit 7 Min nicht mehr") {
		t.Errorf("schweigt: %q", got)
	}
	b.configure(func(c *config.Config) { c.OfflineAfterSeconds = 600 })
	if got, _ := b.statusAnswer("admin")["device_hint"].(string); !strings.HasPrefix(got, "Mehrere Boards") {
		t.Errorf("Schwelle 600 s: %q", got)
	}
	b.configure(func(c *config.Config) { c.BoxDeviceID = "\tfw-box-1 " })
	want := "Ein fremdes Board meldet sich mit dem Maschinen-Token der Box („bastel-1“, zuletzt vor 1 Min) – seine Pushs werden ignoriert."
	if got := b.statusAnswer("admin")["device_hint"]; got != want {
		t.Errorf("box_device_id: %q", got)
	}
	// Die Uhr zählt: nach 25 Stunden ist das fremde Board vergessen.
	b.advance(25 * time.Hour)
	if got := b.statusAnswer("admin")["device_hint"]; got != "" {
		t.Errorf("nach 25 Std: %q", got)
	}
}
