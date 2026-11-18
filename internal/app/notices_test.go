package app

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"fwalarm/internal/config"
	"fwalarm/internal/model"
)

// barkCall ist eine Anfrage an die Bark-Attrappe.
type barkCall struct{ key, title, body string }

// Serve hängt die Meldungen bei der Anmeldung ein, bevor es Anfragen
// annimmt, und sie laufen nebenher NACH der Antwort (Core.Bg): Die Antwort
// auf die Anmeldung kommt, während die Bark-Attrappe die Meldung noch
// festhält – sie wartet nie auf Bark. Beide Arten: Anmeldung (eingeschaltet)
// und Sperre nach Fehlversuchen.
func TestLoginNoticesAfterTheAnswer(t *testing.T) {
	arrived := make(chan barkCall, 8)
	release := make(chan struct{})
	barkSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("Bark-Attrappe: %v", err)
		}
		arrived <- barkCall{key: strings.TrimPrefix(r.URL.Path, "/"), title: r.PostForm.Get("title"), body: r.PostForm.Get("body")}
		select { // festhalten, bis der Test die Antworten gesehen hat
		case <-release:
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(barkSrv.Close) // zuletzt: wartet auf die festgehaltenen Anfragen

	hash, err := bcrypt.GenerateFromPassword([]byte("richtig-1"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	b := newBench(t)
	b.writeConfig(func(c *config.Config) {
		c.DashboardUser, c.DashboardPasswordHash = "admin", string(hash)
		c.BarkHost, c.BarkKeyStatus = barkSrv.URL, "statuskey01"
		c.LoginMaxFailures = 1 // der erste Fehlversuch sperrt
	})
	// "Anmeldung am Dashboard" ist ab Werk aus: eingeschaltet im Zustand.
	st := model.NewState()
	st.Notify.Types["login_success"] = map[string]string{"enabled": "1"}
	doc, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(b.state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.state, "state.json"), doc, 0o600); err != nil {
		t.Fatal(err)
	}
	b.start()
	t.Cleanup(func() { close(release) }) // zuerst: die Attrappe gibt frei

	client := b.httpClient()
	client.Timeout = 10 * time.Second // käme die Antwort erst nach Bark, käme sie nie
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	login := func(password, ip string) int {
		t.Helper()
		r, err := http.NewRequest(http.MethodPost, "http://fw-alarm/index.php",
			strings.NewReader(url.Values{"action": {"login"}, "user": {"admin"}, "password": {password}}.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("CF-Connecting-IP", ip)
		resp, err := client.Do(r)
		if err != nil {
			t.Fatalf("Anmeldung wartet auf Bark: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body) // nur der Status zählt
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	next := func(what string) barkCall {
		t.Helper()
		select {
		case c := <-arrived:
			return c
		case <-time.After(10 * time.Second):
			t.Fatalf("keine Meldung %q bei Bark", what)
			return barkCall{}
		}
	}

	if status := login("richtig-1", "198.51.100.7"); status != http.StatusFound {
		t.Fatalf("Anmeldung: %d", status)
	}
	c := next("Anmeldung")
	if c.key != "statuskey01" || c.title != "Dashboard: Anmeldung" ||
		!strings.HasSuffix(c.body, "Admin 'admin' hat sich angemeldet (von 198.51.100.7).") {
		t.Errorf("Anmeldung: %+v", c)
	}

	if status := login("falsch", "198.51.100.9"); status != http.StatusOK {
		t.Fatalf("Fehlversuch: %d", status)
	}
	c = next("Login gesperrt")
	if c.key != "statuskey01" || c.title != "Dashboard: Login gesperrt" ||
		!strings.HasSuffix(c.body, "Zu viele fehlgeschlagene Login-Versuche von 198.51.100.9 - IP fuer 15 min gesperrt.") {
		t.Errorf("Sperre: %+v", c)
	}
	select {
	case extra := <-arrived:
		t.Errorf("weitere Meldung: %+v", extra)
	default:
	}
}
