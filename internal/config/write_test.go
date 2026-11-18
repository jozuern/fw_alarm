package config

import (
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
)

// Jeder Schlüssel mit einem Wert, der nicht der Standard ist.
func everyKeySet() []byte {
	return tomlFile(map[string]string{
		"machine_token_sha256":             `"` + boxHash + `"`,
		"dashboard_user":                   `"chef"`,
		"dashboard_password_hash":          `"` + otherBcrypt + `"`,
		"dashboard_readonly_user":          `"leser"`,
		"dashboard_readonly_password_hash": `"` + testBcrypt + `"`,
		"login_max_failures":               "5",
		"login_lockout_window_seconds":     "600",
		"trusted_proxies":                  `["127.0.0.1", "::1"]`,
		"proxy_client_ip_header":           `"CF-Connecting-IP"`,
		"offline_after_seconds":            "240",
		"box_device_id":                    `"boss925-01"`,
		"bark_key_status":                  `"statuskey0001"`,
		"watchdog_offline_after_seconds":   "900",
		"demo_reminder_after_seconds":      "3600",
		"watcher_token_sha256":             `"` + watcherHash + `"`,
		"mute_max_seconds":                 "0",
		"public_base_url":                  `"https://alarm.example.org/"`,
		"delivered_command_lock_seconds":   "60",
		"pending_command_ttl_seconds":      "200",
		"bark_host":                        `"http://127.0.0.1:9"`,
		"bark_keys_alarm":                  `["startkey0001", "startkey0002"]`,
		"bark_alarm_title":                 `"Probe"`,
		"bark_alarm_body":                  `"Text"`,
		"bark_alarm_sound":                 `"bell"`,
		"bark_alarm_volume":                "8",
		"bark_alarm_call":                  "false",
		"bark_max_tries":                   "5",
		"bark_false_alarm_sound":           `"chime"`,
		"bark_false_alarm_volume":          "4",
		"timezone":                         `"America/New_York"`,
		"state_dir":                        `"/srv/fw-alarm/state"`,
		"listen":                           `"unix:/run/fw-alarm/http.sock"`,
		"control_socket":                   `"/run/fw-alarm-trial/control.sock"`,
		"check_interval_seconds":           "300",
		"send_guard_keys":                  `["statuskey0001"]`,
		"send_guard_until":                 `"2026-11-20 05:30"`, // New York: 11:30 in Berlin
	})
}

func roundTrip(t *testing.T, data []byte) {
	t.Helper()
	c := mustParse(t, data)
	written := Write(c)
	again, err := ParseAt(written, loadMoment(t))
	if err != nil {
		t.Fatalf("Write ergibt keine gültige Datei: %v\n%s", err, written)
	}
	sameValues(t, again, c)
	if string(Write(again)) != string(written) {
		t.Fatal("Write ist nicht stabil: zweimal geschrieben, verschieden")
	}
}

func TestWriteRoundTrip(t *testing.T) {
	t.Run("Standardwerte", func(t *testing.T) { roundTrip(t, minimalFile) })
	t.Run("jeder Schlüssel gesetzt", func(t *testing.T) { roundTrip(t, everyKeySet()) })
	t.Run("Vorlage", func(t *testing.T) {
		data, err := os.ReadFile(examplePath)
		if err != nil {
			t.Fatal(err)
		}
		roundTrip(t, data)
	})

	// Write nennt jeden Schlüssel genau einmal und jeden mit Erklärung.
	written := string(Write(mustParse(t, minimalFile)))
	var raw map[string]any
	if _, err := toml.Decode(written, &raw); err != nil {
		t.Fatal(err)
	}
	named := make([]string, 0, len(raw))
	for k := range raw {
		named = append(named, k)
	}
	sort.Strings(named)
	want := keyNames()
	sort.Strings(want)
	if !slices.Equal(named, want) {
		t.Errorf("Write nennt %q,\nerwartet %q", named, want)
	}
	lines := strings.Split(written, "\n")
	for i, line := range lines {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if i == 0 || !strings.HasPrefix(lines[i-1], "#") && !isKeyLine(lines[i-1]) {
			t.Errorf("Zeile %d ohne Erklärung davor: %q", i+1, line)
		}
	}
	if !strings.Contains(written, "HIER STEHEN ALLE WERTE") {
		t.Error("Write ohne Banner")
	}
}

func isKeyLine(line string) bool {
	name, _, ok := strings.Cut(line, " = ")
	return ok && slices.Contains(keyNames(), name)
}

var oddStrings = []string{
	`Anführungszeichen " mittendrin`,
	`Rückstrich \ und \n als Text`,
	"Umlaute äöüÄÖÜß und Emoji 🚒",
	"Zeilen\numbruch",
	"Windows\r\nZeile",
	"# kein Kommentar",
	"a = b",
	"Tabulator\tdazwischen",
	`'''drei einfache'''`,
	`"""drei doppelte"""`,
	"Steuerzeichen \x01 \x1f \x7f",
	"NUL \x00 mittendrin",
	"",
	" Leerzeichen außen ",
	"[tabelle]",
	"  Zeilentrenner",
}

func TestWriteOddStrings(t *testing.T) {
	base := mustParse(t, tomlFile(nil))
	for _, s := range oddStrings {
		c := *base
		c.BarkAlarmTitle = s
		c.BarkAlarmBody = "Text: " + s
		c.DashboardUser = s
		c.BarkKeysAlarm = []string{s, "startkey0001"}
		written := Write(&c)
		again, err := ParseAt(written, loadMoment(t))
		if err != nil {
			t.Fatalf("%q: Write ergibt keine gültige Datei: %v\n%s", s, err, written)
		}
		if again.BarkAlarmTitle != s || again.BarkAlarmBody != "Text: "+s || again.DashboardUser != s {
			t.Errorf("%q kam als Titel %q, Text %q, Benutzer %q zurück", s, again.BarkAlarmTitle, again.BarkAlarmBody, again.DashboardUser)
		}
		if !slices.Equal(again.BarkKeysAlarm, c.BarkKeysAlarm) {
			t.Errorf("%q: Liste kam als %q zurück", s, again.BarkKeysAlarm)
		}
	}
}

func FuzzWriteParse(f *testing.F) {
	for _, s := range oddStrings {
		f.Add(s, s+"x", int64(len(s)), true)
	}
	f.Fuzz(func(t *testing.T, title, body string, n int64, flag bool) {
		if !utf8.ValidString(title) || !utf8.ValidString(body) {
			t.Skip("TOML kennt nur gültiges UTF-8")
		}
		c := *mustParse(t, tomlFile(nil))
		c.BarkAlarmTitle = title
		c.BarkAlarmBody = body
		c.PublicBaseURL = body + title
		c.DashboardReadonlyUser = title
		c.BarkKeysAlarm = []string{title, body}
		c.TrustedProxies = []string{body}
		c.OfflineAfterSeconds = int(n)
		c.BarkAlarmVolume = int(n % 11)
		c.BarkFalseAlarmVolume = int(-n)
		c.BarkAlarmCall = flag
		again, err := ParseAt(Write(&c), loadMoment(t))
		if err != nil {
			t.Fatalf("Write ergibt keine gültige Datei: %v\n%s", err, Write(&c))
		}
		sameValues(t, again, &c)
	})
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(nil))
	f.Add(tomlFile(nil))
	f.Add(everyKeySet())
	if data, err := os.ReadFile(examplePath); err == nil {
		f.Add(data)
	}
	f.Add([]byte("bark_alarm_title = \"offen\n"))
	f.Add([]byte("[extra]\nx = 1\n"))
	f.Add([]byte("bark_keys_alarm = [\"a\", 1]\n"))
	f.Add([]byte("send_guard_keys = [\"adminkey0001\"]\nsend_guard_until = \"2026-11-20 10:30\"\n"))
	f.Add([]byte("\xef\xbb\xbftimezone = \"UTC\"\n"))
	f.Add([]byte("bark_alarm_body = '''\nmehr\nzeilig'''\n"))
	f.Add([]byte("bark_alarm_title = \"\\u00e4\\x41\\e\"\n"))
	f.Add([]byte("bark_host = \"https://a@b/c?d#e\"\n"))
	f.Add([]byte("bark_host = \"https://api.day.app/pfad\"\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		c, err := ParseAt(data, loadMoment(t))
		if err != nil {
			if c != nil {
				t.Fatal("Fehler und Konfiguration zugleich")
			}
			return
		}
		// Was angenommen wird, übersteht Schreiben und erneutes Lesen.
		again, err := ParseAt(Write(c), loadMoment(t))
		if err != nil {
			t.Fatalf("angenommene Datei lässt sich nicht zurückschreiben: %v", err)
		}
		sameValues(t, again, c)
	})
}

// Die Probe-Sperre steht als Erstes in der Datei: Auch ein abgeschnittener
// Anfang einer Datei mit aktiver Sperre hält sie noch.
func TestWriteSendGuardFirst(t *testing.T) {
	written := string(Write(mustParse(t, tomlFile(map[string]string{
		"send_guard_keys": `["statuskey0001"]`, "send_guard_until": `"2026-11-20 11:30"`}))))
	var keys []string
	for line := range strings.SplitSeq(written, "\n") {
		if isKeyLine(line) {
			name, _, _ := strings.Cut(line, " = ")
			keys = append(keys, name)
		}
	}
	// Erst die Keys, dann die Zeitzone, dann das Ende: Ein Anfang, der die
	// Sperre ganz enthält, enthält auch ihre Zeitzone; einer mit Keys ohne
	// Ende ist fehlerhaft (nur einer der beiden Werte gesetzt).
	if len(keys) < 4 || keys[0] != "send_guard_keys" || keys[1] != "timezone" || keys[2] != "send_guard_until" {
		t.Fatalf("Reihenfolge der Schlüssel: %q", keys)
	}
	end := strings.Index(written, "send_guard_until = ")
	end += strings.IndexByte(written[end:], '\n') + 1
	prefix := mustParse(t, []byte(written[:end]))
	if _, _, active := prefix.SendGuard(loadMoment(t)); !active {
		t.Error("der Anfang bis zur Sperre hält sie nicht")
	}
}

// Jeder Anfang der geschriebenen Datei, abgeschnitten an einem Zeilenende,
// ist entweder fehlerhaft oder hält die Sperre – mit demselben Ende, auch
// in einer anderen Zeitzone als der voreingestellten.
func TestWritePrefixesKeepTheLock(t *testing.T) {
	full := Write(mustParse(t, tomlFile(map[string]string{
		"timezone":         `"America/New_York"`,
		"send_guard_keys":  `["statuskey0001"]`,
		"send_guard_until": `"2026-11-20 05:30"`, // New York: 11:30 in Berlin
	})))
	now := loadMoment(t)
	_, wantUntil, active := mustParse(t, full).SendGuard(now)
	if !active {
		t.Fatal("die Sperre der ganzen Datei gilt nicht")
	}
	cuts := 0
	for i, b := range full {
		if b != '\n' {
			continue
		}
		cuts++
		c, err := ParseAt(full[:i+1], now)
		if err != nil {
			continue
		}
		if _, until, ok := c.SendGuard(now); !ok || !until.Equal(wantUntil) {
			t.Fatalf("Anfang bis Zeile %d: Sperre aktiv=%v bis %v, erwartet bis %v\n%s",
				cuts, ok, until, wantUntil, full[:i+1])
		}
	}
	if cuts < 50 {
		t.Fatalf("nur %d Zeilen", cuts)
	}
}
