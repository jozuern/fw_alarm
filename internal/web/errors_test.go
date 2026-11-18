package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"fwalarm/internal/store"
)

// Jeder Fehlerwert der Ablage: der feste Satz, Status, JSON- und Textform –
// und nie ein Pfad oder sonst etwas aus dem Fehler selbst.
func TestStorageError(t *testing.T) {
	const (
		storageSentence   = "Fehler: Datenordner nicht beschreibbar (Rechte des Web-Benutzers auf dem Server prüfen)."
		lateSentence      = "Fehler: Speichern dauert zu lange – bitte den angezeigten Stand prüfen."
		emergencySentence = "Fehler: Notbetrieb – die Empfängerliste ist nicht lesbar, Änderungen sind gerade nicht möglich. Ausweg: Sicherung zurückspielen (server/README.md)."
	)
	// Wie der Fehler aus der Ablage kommen kann: verpackt, mit Pfad.
	wrap := func(err error) error {
		return fmt.Errorf("sichern: open /var/lib/fw-alarm/state.json.tmp: permission denied: %w", err)
	}
	cases := []struct {
		name     string
		err      error
		status   int // JSON-Form
		code     string
		sentence string
	}{
		{"ErrStorage", store.ErrStorage, 500, "storage", storageSentence},
		{"ErrLate", store.ErrLate, 500, "storage", lateSentence},
		{"ErrDropped", store.ErrDropped, 500, "storage", lateSentence},
		// Wie PHP bei unlesbarer Liste: ok=false mit dem Satz, ohne code.
		{"ErrEmergency", store.ErrEmergency, 200, "", emergencySentence},
		// Was es sonst sein mag (ErrInvalid: ein Fehler im Programm): nicht
		// gesichert – der bekannte Satz.
		{"ErrInvalid", store.ErrInvalid, 500, "storage", storageSentence},
		{"unbekannt", errors.New("irgendwas in /var/lib/fw-alarm"), 500, "storage", storageSentence},
	}
	for _, tc := range cases {
		for _, err := range []error{tc.err, wrap(tc.err)} {
			rec := httptest.NewRecorder()
			StorageError(rec, err, true)
			if rec.Code != tc.status || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
				t.Errorf("%s (JSON): %d %q", tc.name, rec.Code, rec.Header().Get("Content-Type"))
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("%s: %v %q", tc.name, err, rec.Body.String())
			}
			want := map[string]any{"ok": false, "message": tc.sentence}
			order := `{"ok":false,"message":`
			if tc.code != "" {
				want["code"] = tc.code
				order = `{"ok":false,"code":"` + tc.code + `","message":`
			}
			if fmt.Sprint(body) != fmt.Sprint(want) || !strings.HasPrefix(rec.Body.String(), order) {
				t.Errorf("%s (JSON): %s", tc.name, rec.Body.String())
			}

			rec = httptest.NewRecorder()
			StorageError(rec, err, false)
			if rec.Code != 500 || rec.Body.String() != tc.sentence || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
				t.Errorf("%s (Text): %d %q %q", tc.name, rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
			}
		}
		for _, form := range []bool{true, false} {
			rec := httptest.NewRecorder()
			StorageError(rec, wrap(tc.err), form)
			if b := rec.Body.String(); strings.Contains(b, "/var/lib") || strings.Contains(b, "state.json") ||
				strings.Contains(b, "permission") {
				t.Errorf("%s: Einzelheiten des Fehlers in der Antwort: %q", tc.name, b)
			}
		}
	}
}
