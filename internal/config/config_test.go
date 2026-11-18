package config

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // Zeitzonen unabhängig vom Rechner, auf dem der Test läuft

	"github.com/BurntSushi/toml"
)

// Zugänge wie im Prüfstand (tests/backend/harness.mjs).
const (
	testBcrypt  = "$2y$04$r60I68m5mVq98EDvnpGXGeS1SZtBd2wWlr0dy0MuX73YPDZ27VzZy" // "test"
	otherBcrypt = "$2y$04$arQ57ZxGZu0qqkyhgNiBc.Ri6mtxzvUCvCwP8PvCH/TfBrFX9abKO" // "anderes"
	examplePath = "../../server/go/config.example.toml"
)

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

var (
	boxHash     = sha("box-token")
	watcherHash = sha("watcher-token")
)

func berlin(t testing.TB) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// Fester Zeitpunkt des Ladens: Freitag, 20.11.2026 10:00 Uhr in Berlin.
func loadMoment(t testing.TB) time.Time {
	return time.Date(2026, 11, 20, 10, 0, 0, 0, berlin(t))
}

// absent entfernt einen Schlüssel der Grunddatei.
const absent = "\x00absent"

// minimalFile setzt genau einen Wert, und zwar auf seinen Standard: eine
// Datei ohne jeden Wert ist fehlerhaft, diese hier ergibt alle Standards.
var minimalFile = []byte("send_guard_keys = []\n")

// tomlFile baut eine Datei aus der Grunddatei (gültig, ohne Hinweise) und
// den Abweichungen; die Werte stehen als TOML-Text darin.
func tomlFile(set map[string]string) []byte {
	vals := map[string]string{
		"machine_token_sha256":    `"` + boxHash + `"`,
		"dashboard_user":          `"admin"`,
		"dashboard_password_hash": `"` + testBcrypt + `"`,
	}
	for k, v := range set {
		if v == absent {
			delete(vals, k)
		} else {
			vals[k] = v
		}
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + " = " + vals[k] + "\n")
	}
	return []byte(b.String())
}

func mustParse(t testing.TB, data []byte) *Config {
	t.Helper()
	c, err := ParseAt(data, loadMoment(t))
	if err != nil {
		t.Fatalf("ParseAt: %v\nDatei:\n%s", err, data)
	}
	return c
}

func TestEmptyBarkHostWarns(t *testing.T) {
	c := mustParse(t, tomlFile(map[string]string{"bark_host": `""`})) // die Datei lädt trotzdem
	w := c.Warnings()
	if len(w) != 1 || w[0] != "bark_host ist leer – der Server kann nichts senden" {
		t.Errorf("Hinweise: %q", w)
	}
}

// Mit einem Pfad im Host nähme der Bark-Server dessen ersten Teil als Gerät:
// Jede Nachricht ginge an dieses eine Gerät, auch unter der Probe-Sperre.
// Abfrage, Fragment und Zugangsdaten machen die Datei deshalb fehlerhaft,
// ein Pfad, ein fehlendes http(s):// oder nur "/" sind Hinweise.
func TestBarkHostRules(t *testing.T) {
	const (
		broken = "bark_host darf weder Abfrage noch Fragment noch Zugangsdaten enthalten"
		path   = "bark_host hat einen Pfad – alle Meldungen gingen an diese eine Adresse; nur für einen eigenen Bark-Server unter einem Unterpfad"
		scheme = "bark_host ist keine http(s)-Adresse – der Server kann nichts senden"
	)
	cases := []struct {
		host string
		err  string // "" = lädt
		warn string // "" = kein Hinweis
	}{
		{"https://api.day.app", "", ""},
		{"https://api.day.app/", "", ""},
		{"HTTPS://Api.Day.App//", "", ""},
		{" http://127.0.0.1:9 ", "", ""},
		{"https://api.day.app?geheimkey123", broken, ""},
		{"https://api.day.app/?x=1", broken, ""},
		{"https://api.day.app?", broken, ""},
		{"https://api.day.app#geheimkey123", broken, ""},
		{"https://api.day.app/#", broken, ""},
		{"https://benutzer:geheimpass@api.day.app", broken, ""},
		{"https://geheimkey123@api.day.app/", broken, ""},
		{"https://ben utzer:geheim pass@api.day.app", broken, ""},
		{"//geheimkey123@api.day.app", broken, ""},
		{"https://api.day.app/geheimkey123", "", path},
		{"https://api.day.app/a/b/", "", path},
		{"http://127.0.0.1:9/bark", "", path},
		{"https://api.day.app/pfad@mit-at", "", path},
		{"/", "", scheme},
		{"///", "", scheme},
		{"api.day.app", "", scheme},
		{"ftp://api.day.app", "", scheme},
		{"https://", "", scheme},
		{"https:///pfad", "", scheme},
		{"https://ho st", "", scheme},
	}
	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			c, err := ParseAt(tomlFile(map[string]string{"bark_host": quote(tc.host)}), loadMoment(t))
			if tc.err != "" {
				if err == nil || err.Error() != tc.err {
					t.Fatalf("Fehler %v, erwartet %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Datei abgelehnt: %v", err)
			}
			if c.BarkHost != tc.host {
				t.Errorf("bark_host = %q, erwartet %q (der Wert gilt)", c.BarkHost, tc.host)
			}
			w := c.Warnings()
			switch {
			case tc.warn == "" && len(w) != 0:
				t.Errorf("Hinweise %q, erwartet keine", w)
			case tc.warn != "" && (len(w) != 1 || w[0] != tc.warn):
				t.Errorf("Hinweise %q, erwartet %q", w, tc.warn)
			}
		})
	}
}

func TestGoodFileHasNoWarnings(t *testing.T) {
	c := mustParse(t, tomlFile(nil))
	if w := c.Warnings(); len(w) != 0 {
		t.Fatalf("Hinweise bei einer guten Datei: %q", w)
	}
}

// Fehlt ein Schlüssel, gilt der Wert, den der PHP-CODE dafür nimmt (das
// "?? …" an der genannten Stelle), nicht der Wert der alten Vorlage.
func TestDefaultsMatchPHPCode(t *testing.T) {
	c, err := ParseAt(minimalFile, loadMoment(t))
	if err != nil {
		t.Fatal(err)
	}
	ints := []struct {
		name string
		got  int
		want int
	}{
		{"offline_after_seconds", c.OfflineAfterSeconds, 180},                  // dashboard_api.php:129, common.php:1739
		{"watchdog_offline_after_seconds", c.WatchdogOfflineAfterSeconds, 600}, // common.php:2485
		{"demo_reminder_after_seconds", c.DemoReminderAfterSeconds, 14400},     // common.php:2487
		{"mute_max_seconds", c.MuteMaxSeconds, 43200},                          // common.php:1985
		{"delivered_command_lock_seconds", c.DeliveredCommandLockSeconds, 120}, // dashboard_api.php:249
		{"pending_command_ttl_seconds", c.PendingCommandTTLSeconds, 300},       // common.php:1103
		{"login_max_failures", c.LoginMaxFailures, 8},                          // common.php:662
		{"login_lockout_window_seconds", c.LoginLockoutWindowSeconds, 900},     // common.php:663
		{"bark_alarm_volume", c.BarkAlarmVolume, 10},                           // common.php:2950
		{"bark_max_tries", c.BarkMaxTries, 3},                                  // common.php:3008
		{"bark_false_alarm_volume", c.BarkFalseAlarmVolume, 10},                // common.php:2972
		{"check_interval_seconds", c.CheckIntervalSeconds, 900},                // neu (Prüf-Lauf alle 15 min wie der Timer)
	}
	for _, tc := range ints {
		if tc.got != tc.want {
			t.Errorf("%s = %d, erwartet %d", tc.name, tc.got, tc.want)
		}
	}
	strs := []struct {
		name string
		got  string
		want string
	}{
		{"bark_host", c.BarkHost, "https://api.day.app"},                                  // common.php:2308
		{"bark_alarm_title", c.BarkAlarmTitle, "FEUERWEHR-ALARM"},                         // common.php:2944
		{"bark_alarm_body", c.BarkAlarmBody, "Manuelle Alarmierung ueber das Dashboard!"}, // common.php:3132
		{"bark_alarm_sound", c.BarkAlarmSound, "alarm_fw"},                                // common.php:2927, 2946
		{"bark_false_alarm_sound", c.BarkFalseAlarmSound, "glass"},                        // common.php:2976
		{"timezone", c.Timezone, "Europe/Berlin"},                                         // Vorgabe des Plans
		{"machine_token_sha256", c.MachineTokenSHA256, ""},                                // common.php:609
		{"watcher_token_sha256", c.WatcherTokenSHA256, ""},                                // common.php:2472
		{"dashboard_user", c.DashboardUser, ""},                                           // common.php:835
		{"dashboard_password_hash", c.DashboardPasswordHash, ""},                          // common.php:836
		{"dashboard_readonly_user", c.DashboardReadonlyUser, ""},                          // index.php:66
		{"dashboard_readonly_password_hash", c.DashboardReadonlyPasswordHash, ""},         // index.php:67
		{"proxy_client_ip_header", c.ProxyClientIPHeader, ""},                             // common.php:634
		{"box_device_id", c.BoxDeviceID, ""},                                              // common.php:982
		{"bark_key_status", c.BarkKeyStatus, ""},                                          // common.php:2783
		{"public_base_url", c.PublicBaseURL, ""},                                          // shell.php:141
		{"state_dir", c.StateDir, "/var/lib/fw-alarm"},                                    // neu
		{"listen", c.Listen, "127.0.0.1:8080"},                                            // neu
		{"control_socket", c.ControlSocket, "/run/fw-alarm/control.sock"},                 // neu
		{"send_guard_until", c.SendGuardUntil, ""},                                        // neu
	}
	for _, tc := range strs {
		if tc.got != tc.want {
			t.Errorf("%s = %q, erwartet %q", tc.name, tc.got, tc.want)
		}
	}
	if !c.BarkAlarmCall { // common.php:2954
		t.Error("bark_alarm_call muss ohne Eintrag true sein")
	}
	for name, list := range map[string][]string{
		"trusted_proxies": c.TrustedProxies, // common.php:100
		"bark_keys_alarm": c.BarkKeysAlarm,  // common.php:1944
		"send_guard_keys": c.SendGuardKeys,  // neu
	} {
		if len(list) != 0 {
			t.Errorf("%s = %q, erwartet leer", name, list)
		}
	}
	if c.Location().String() != "Europe/Berlin" {
		t.Errorf("Location() = %s", c.Location())
	}

	// Untergrenzen wie im PHP-Code (max(1, …) bzw. max(60, …)).
	clamp := mustParse(t, tomlFile(map[string]string{
		"login_max_failures":           "0",
		"login_lockout_window_seconds": "30",
		"bark_max_tries":               "-2",
	}))
	if clamp.LoginMaxFailures != 1 || clamp.LoginLockoutWindowSeconds != 60 || clamp.BarkMaxTries != 1 {
		t.Errorf("Untergrenzen: login_max_failures=%d (1), login_lockout_window_seconds=%d (60), bark_max_tries=%d (1)",
			clamp.LoginMaxFailures, clamp.LoginLockoutWindowSeconds, clamp.BarkMaxTries)
	}

	// Ohne eigene Entwarnungs-Lautstärke gilt die des Alarms (common.php:2972).
	vol := mustParse(t, tomlFile(map[string]string{"bark_alarm_volume": "7"}))
	if vol.BarkFalseAlarmVolume != 7 {
		t.Errorf("bark_false_alarm_volume = %d, erwartet 7 (wie bark_alarm_volume)", vol.BarkFalseAlarmVolume)
	}
	own := mustParse(t, tomlFile(map[string]string{"bark_alarm_volume": "7", "bark_false_alarm_volume": "2"}))
	if own.BarkFalseAlarmVolume != 2 {
		t.Errorf("bark_false_alarm_volume = %d, erwartet 2", own.BarkFalseAlarmVolume)
	}
	if call := mustParse(t, tomlFile(map[string]string{"bark_alarm_call": "false"})); call.BarkAlarmCall {
		t.Error("bark_alarm_call = false wurde nicht übernommen")
	}
}

// Ein vorhandener, aber leerer Wert bleibt leer (PHP: "??" greift nur bei
// fehlendem Schlüssel).
func TestPresentButEmptyStaysEmpty(t *testing.T) {
	empty := []string{
		"bark_host", "bark_alarm_title", "bark_alarm_body", "bark_alarm_sound",
		"bark_false_alarm_sound", "dashboard_user", "public_base_url", "box_device_id",
		"proxy_client_ip_header", "listen", "control_socket", "state_dir",
	}
	set := map[string]string{
		"bark_keys_alarm": "[]",
		"trusted_proxies": "[]",
	}
	for _, k := range empty {
		set[k] = `""`
	}
	c := mustParse(t, tomlFile(set))
	got := map[string]string{
		"bark_host": c.BarkHost, "bark_alarm_title": c.BarkAlarmTitle, "bark_alarm_body": c.BarkAlarmBody,
		"bark_alarm_sound": c.BarkAlarmSound, "bark_false_alarm_sound": c.BarkFalseAlarmSound,
		"dashboard_user": c.DashboardUser, "public_base_url": c.PublicBaseURL, "box_device_id": c.BoxDeviceID,
		"proxy_client_ip_header": c.ProxyClientIPHeader, "listen": c.Listen,
		"control_socket": c.ControlSocket, "state_dir": c.StateDir,
	}
	for _, k := range empty {
		if got[k] != "" {
			t.Errorf("%s = %q, erwartet leer", k, got[k])
		}
	}
	if len(c.BarkKeysAlarm) != 0 || len(c.TrustedProxies) != 0 {
		t.Errorf("Listen nicht leer: %q %q", c.BarkKeysAlarm, c.TrustedProxies)
	}
}

func TestExampleFileLoads(t *testing.T) {
	c, err := LoadAt(examplePath, loadMoment(t))
	if err != nil {
		t.Fatalf("Vorlage lädt nicht: %v", err)
	}

	// Die Vorlage nennt jeden Schlüssel – und keinen, den es nicht gibt.
	var raw map[string]any
	data, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := toml.Decode(string(data), &raw); err != nil {
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
		t.Errorf("Vorlage nennt %q,\nerwartet %q", named, want)
	}
	if !strings.Contains(string(data), ">>>>>>>>>>  HIER STEHEN ALLE WERTE, DIE DU EDITIEREN MUSST/KANNST  <<<<<<<<<") {
		t.Error("Vorlage ohne das Banner „>>> HIER STEHEN ALLE WERTE … <<<“")
	}

	// Ihre Platzhalter kommen als Hinweise heraus.
	warnings := c.Warnings()
	if len(warnings) != 2 {
		t.Fatalf("Hinweise der Vorlage: %q, erwartet zwei (Token-Hash und Passwort-Hash)", warnings)
	}
	if !strings.Contains(warnings[0]+warnings[1], "machine_token_sha256") ||
		!strings.Contains(warnings[0]+warnings[1], "dashboard_password_hash") {
		t.Errorf("Hinweise nennen die Platzhalter nicht: %q", warnings)
	}
	for _, w := range warnings {
		if strings.Contains(strings.ToLower(w), "paste_") {
			t.Errorf("Hinweis zitiert den Wert: %q", w)
		}
	}
	if c.BarkAlarmTitle != "FEUERWEHR-ALARM (manuell)" {
		t.Errorf("bark_alarm_title der Vorlage = %q (wie config.example.php)", c.BarkAlarmTitle)
	}
}

// Die Vorlage ist genau das, was Write aus ihren eigenen Werten macht – so
// können Erklärungen in Write und in der Vorlage nie auseinanderlaufen.
func TestExampleIsWriteOutput(t *testing.T) {
	data, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatal(err)
	}
	c := mustParse(t, data)
	if got := Write(c); string(got) != string(data) {
		if os.Getenv("FW_WRITE_EXAMPLE") == "1" {
			if err := os.WriteFile(filepath.FromSlash(examplePath), got, 0o644); err != nil {
				t.Fatal(err)
			}
			t.Log("Vorlage neu geschrieben")
			return
		}
		t.Fatalf("Vorlage weicht von Write ab (neu schreiben: FW_WRITE_EXAMPLE=1 go test ./internal/config -run TestExampleIsWriteOutput)")
	}
}

func TestBroken(t *testing.T) {
	cases := []struct {
		name    string
		data    []byte
		mention string // muss im Fehlertext stehen
	}{
		{"unbekannter Schlüssel", tomlFile(map[string]string{"colour": `"rot"`}), "colour"},
		{"unbekannte Tabelle", append(tomlFile(nil), []byte("[extra]\nx = 1\n")...), "extra"},
		{"Schlüssel in anderer Schreibweise", tomlFile(map[string]string{"Timezone": `"Europe/Berlin"`}), "Timezone"},
		{"data_dir gibt es nicht mehr", tomlFile(map[string]string{"data_dir": `"/srv/fw-alarm/data"`}), "data_dir"},
		{"health_check_url gibt es nicht mehr", tomlFile(map[string]string{"health_check_url": `"http://127.0.0.1/"`}), "health_check_url"},
		{"Text statt Zahl", tomlFile(map[string]string{"login_max_failures": `"8"`}), "login_max_failures"},
		{"Kommazahl statt Zahl", tomlFile(map[string]string{"offline_after_seconds": "180.0"}), "offline_after_seconds"},
		{"Text statt Wahrheitswert", tomlFile(map[string]string{"bark_alarm_call": `"ja"`}), "bark_alarm_call"},
		{"Text statt Liste", tomlFile(map[string]string{"bark_keys_alarm": `"startkey0001"`}), "bark_keys_alarm"},
		{"Liste mit Zahlen", tomlFile(map[string]string{"bark_keys_alarm": "[1, 2]"}), "bark_keys_alarm"},
		{"Liste mit Wahrheitswert", tomlFile(map[string]string{"trusted_proxies": `["127.0.0.1", true]`}), "trusted_proxies"},
		{"Zahl statt Text", tomlFile(map[string]string{"machine_token_sha256": "1234567"}), "machine_token_sha256"},
		{"Tabelle statt Text", tomlFile(map[string]string{"bark_alarm_title": "{ a = 1 }"}), "bark_alarm_title"},
		{"Zeitangabe statt Text", tomlFile(map[string]string{"send_guard_until": "2026-11-20 10:30:00"}), "send_guard_until"},
		{"Zahl statt Zeitzone", tomlFile(map[string]string{"timezone": "1"}), "timezone"},
		{"Syntaxfehler", tomlFile(map[string]string{"bark_alarm_title": `"offen`}), "Zeile"},
		{"Syntaxfehler mit Geheimnis", tomlFile(map[string]string{"dashboard_password_hash": "$2y$10$geheimgeheim"}), "Zeile"},
		{"doppelter Schlüssel", append(tomlFile(nil), []byte("dashboard_user = \"zwei\"\n")...), "Zeile"},
		{"unbekannte Zeitzone", tomlFile(map[string]string{"timezone": `"Mars/Olympus_Mons"`}), "Mars/Olympus_Mons"},
		{"leere Zeitzone", tomlFile(map[string]string{"timezone": `""`}), "Zeitzone"},
		{"Zeitzone Local", tomlFile(map[string]string{"timezone": `"Local"`}), "Zeitzone"},
		{"kein UTF-8", append(tomlFile(nil), []byte("bark_alarm_body = \"\xff\xfe\"\n")...), "UTF-8"},
		// Eine Datei ohne jeden Wert kann nur ein Versehen sein (abgeschnitten,
		// geleert): alle Standards hießen keine Probe-Sperre und der echte
		// Bark-Server.
		{"leere Datei", []byte{}, "die Datei setzt keinen einzigen Wert"},
		{"nur Leerraum", []byte(" \n\t\r\n\n"), "die Datei setzt keinen einzigen Wert"},
		{"nur Kommentare", []byte("# Konfiguration\n#\n# send_guard_keys = [\"x\"]\n"), "die Datei setzt keinen einzigen Wert"},
		{"nur das Banner von Write", []byte(writeHeader), "die Datei setzt keinen einzigen Wert"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := ParseAt(tc.data, loadMoment(t))
			if err == nil {
				t.Fatalf("keine Fehlermeldung, Konfiguration angenommen: %+v", c)
			}
			if c != nil {
				t.Error("bei einem Fehler darf keine Konfiguration herauskommen")
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.mention) {
				t.Errorf("Fehler %q nennt %q nicht", msg, tc.mention)
			}
			for _, secret := range []string{"geheim", "1234567", boxHash, testBcrypt} {
				if strings.Contains(msg, secret) {
					t.Errorf("Fehler %q enthält einen Wert (%q)", msg, secret)
				}
			}
		})
	}

	if _, err := LoadAt(filepath.Join(t.TempDir(), "fehlt.toml"), loadMoment(t)); err == nil {
		t.Error("eine fehlende Datei muss ein Fehler sein")
	}
}

func TestWarningsStillLoad(t *testing.T) {
	cases := []struct {
		name    string
		set     map[string]string
		applies func(c *Config) bool
		values  []string // dürfen im Hinweis nicht stehen
	}{
		{
			name:    "Token-Hash zu kurz",
			set:     map[string]string{"machine_token_sha256": `"abc123geheim"`},
			applies: func(c *Config) bool { return c.MachineTokenSHA256 == "abc123geheim" },
			values:  []string{"abc123geheim"},
		},
		{
			name:    "Token-Hash fehlt",
			set:     map[string]string{"machine_token_sha256": absent},
			applies: func(c *Config) bool { return c.MachineTokenSHA256 == "" },
		},
		{
			name:    "Wächter-Hash kein Hex",
			set:     map[string]string{"watcher_token_sha256": `"` + strings.Repeat("z", 64) + `"`},
			applies: func(c *Config) bool { return c.WatcherTokenSHA256 == strings.Repeat("z", 64) },
			values:  []string{strings.Repeat("z", 64)},
		},
		{
			name:    "Passwort-Hash im Klartext",
			set:     map[string]string{"dashboard_password_hash": `"klartext-geheim"`},
			applies: func(c *Config) bool { return c.DashboardPasswordHash == "klartext-geheim" },
			values:  []string{"klartext-geheim"},
		},
		{
			name:    "Passwort-Hash nach anderem Verfahren",
			set:     map[string]string{"dashboard_password_hash": `"$1$abcdefgh$geheimgeheimgeheimgeh"`},
			applies: func(c *Config) bool { return strings.HasPrefix(c.DashboardPasswordHash, "$1$") },
			values:  []string{"geheimgeheim"},
		},
		{
			name: "Lese-Benutzer ohne Hash",
			set:  map[string]string{"dashboard_readonly_user": `"leser"`, "dashboard_readonly_password_hash": `""`},
			applies: func(c *Config) bool {
				return c.DashboardReadonlyUser == "leser" && c.DashboardReadonlyPasswordHash == ""
			},
		},
		{
			name: "Lese-Benutzer mit unbrauchbarem Hash",
			set:  map[string]string{"dashboard_readonly_user": `"leser"`, "dashboard_readonly_password_hash": `"md5-geheim"`},
			applies: func(c *Config) bool {
				return c.DashboardReadonlyPasswordHash == "md5-geheim"
			},
			values: []string{"md5-geheim"},
		},
		{
			name:    "Wächter-Token gleich Box-Token",
			set:     map[string]string{"watcher_token_sha256": `"` + boxHash + `"`},
			applies: func(c *Config) bool { return c.WatcherTokenSHA256 == boxHash },
		},
		{
			name:    "relativer Datenordner",
			set:     map[string]string{"state_dir": `"relativ/daten"`},
			applies: func(c *Config) bool { return c.StateDir == "relativ/daten" },
		},
		{
			name:    "keine selbsttätigen Prüf-Läufe",
			set:     map[string]string{"check_interval_seconds": "0"},
			applies: func(c *Config) bool { return c.CheckIntervalSeconds == 0 },
		},
		{
			name:    "ungültiger Eintrag der Not-Liste",
			set:     map[string]string{"bark_keys_alarm": `["startkey0001", "kaputt geheim"]`},
			applies: func(c *Config) bool { return slices.Equal(c.BarkKeysAlarm, []string{"startkey0001", "kaputt geheim"}) },
			values:  []string{"kaputt geheim", "startkey0001"},
		},
		{
			name:    "unbrauchbarer Status-Key",
			set:     map[string]string{"bark_key_status": `"x!geheim"`},
			applies: func(c *Config) bool { return c.BarkKeyStatus == "x!geheim" },
			values:  []string{"x!geheim"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := mustParse(t, tomlFile(tc.set))
			if !tc.applies(c) {
				t.Errorf("Wert gilt nicht: %+v", c)
			}
			warnings := c.Warnings()
			if len(warnings) != 1 {
				t.Fatalf("Hinweise %q, erwartet genau einen", warnings)
			}
			for _, v := range append(tc.values, boxHash, watcherHash, testBcrypt) {
				if strings.Contains(warnings[0], v) {
					t.Errorf("Hinweis %q enthält einen Wert (%q)", warnings[0], v)
				}
			}
		})
	}
}

func TestSendGuardConfig(t *testing.T) {
	now := loadMoment(t) // 20.11.2026 10:00
	guard := func(keys, until string) map[string]string {
		return map[string]string{"send_guard_keys": keys, "send_guard_until": until}
	}

	// Nicht gesetzt: keine Sperre.
	c := mustParse(t, tomlFile(nil))
	if keys, _, active := c.SendGuard(now); active || len(keys) != 0 {
		t.Fatalf("ohne Eintrag aktiv: %q", keys)
	}
	c = mustParse(t, tomlFile(guard("[]", `""`)))
	if _, _, active := c.SendGuard(now); active {
		t.Fatal("leere Einträge: aktiv")
	}

	// Beide gesetzt, 30 min voraus: aktiv mit genau diesen Keys.
	c = mustParse(t, tomlFile(guard(`["adminkey0001"]`, `"2026-11-20 10:30"`)))
	keys, until, active := c.SendGuard(now)
	if !active || !slices.Equal(keys, []string{"adminkey0001"}) {
		t.Fatalf("Sperre nicht aktiv: %v %q", active, keys)
	}
	if want := time.Date(2026, 11, 20, 10, 30, 0, 0, berlin(t)); !until.Equal(want) {
		t.Errorf("bis %v, erwartet %v", until, want)
	}
	warnings := c.Warnings()
	if len(warnings) != 1 || warnings[0] != "Probe-Sperre aktiv bis 10:30 – der Server sendet nur an 1 Key(s)" {
		t.Errorf("Hinweis %q", warnings)
	}
	for _, w := range warnings {
		if strings.Contains(w, "adminkey0001") {
			t.Errorf("Hinweis nennt den Key: %q", w)
		}
	}
	// Sie endet von selbst.
	if _, _, active := c.SendGuard(now.Add(30 * time.Minute)); active {
		t.Error("Sperre um 10:30 noch aktiv")
	}
	if _, _, active := c.SendGuard(now.Add(29 * time.Minute)); !active {
		t.Error("Sperre um 10:29 nicht aktiv")
	}
	c = mustParse(t, tomlFile(guard(`["adminkey0001", "adminkey0002"]`, `"2026-11-20 13:00"`)))
	if w := c.Warnings(); len(w) != 1 || !strings.Contains(w[0], "bis 13:00") || !strings.Contains(w[0], "2 Key(s)") {
		t.Errorf("genau 3 h voraus: Hinweis %q", w)
	}

	// Abgelaufen: keine Sperre, kein Fehler, kein Hinweis.
	c = mustParse(t, tomlFile(guard(`["adminkey0001"]`, `"2026-11-20 09:59"`)))
	if _, _, active := c.SendGuard(now); active {
		t.Error("abgelaufene Sperre aktiv")
	}
	if w := c.Warnings(); len(w) != 0 {
		t.Errorf("abgelaufene Sperre: Hinweise %q", w)
	}

	broken := map[string]map[string]string{
		"nur Keys":            guard(`["adminkey0001"]`, absent),
		"nur Keys, Zeit leer": guard(`["adminkey0001"]`, `""`),
		"nur Zeit":            guard(absent, `"2026-11-20 10:30"`),
		"nur Zeit, Keys leer": guard("[]", `"2026-11-20 10:30"`),
		"Zeit unlesbar":       guard(`["adminkey0001"]`, `"20.11.2026 10:30"`),
		"Zeit mit T":          guard(`["adminkey0001"]`, `"2026-11-20T10:30"`),
		"Zeit mit Sekunden":   guard(`["adminkey0001"]`, `"2026-11-20 10:30:00"`),
		"ungültiger Key":      guard(`["adminkey0001", "kein key!"]`, `"2026-11-20 10:30"`),
		"3 h 1 min voraus":    guard(`["adminkey0001"]`, `"2026-11-20 13:01"`),
		"nächstes Jahr":       guard(`["adminkey0001"]`, `"2027-11-20 10:30"`),
	}
	for name, set := range broken {
		t.Run(name, func(t *testing.T) {
			c, err := ParseAt(tomlFile(set), now)
			if err == nil {
				t.Fatalf("angenommen: %+v", c)
			}
			if strings.Contains(err.Error(), "adminkey0001") || strings.Contains(err.Error(), "kein key!") {
				t.Errorf("Fehler nennt einen Key: %v", err)
			}
		})
	}

	// Die Zeit gilt in der Zeitzone der Konfiguration, nicht in der Berliner.
	// Geladen um 10:00 in Berlin = 04:00 in New York.
	newYork := func(until string) map[string]string {
		set := guard(`["adminkey0001"]`, until)
		set["timezone"] = `"America/New_York"`
		return set
	}
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("New York: 2 h voraus gilt", func(t *testing.T) {
		c := mustParse(t, tomlFile(newYork(`"2026-11-20 06:00"`)))
		keys, until, active := c.SendGuard(now)
		if !active || !slices.Equal(keys, []string{"adminkey0001"}) {
			t.Fatalf("Sperre bis 06:00 New York nicht aktiv (bis %v)", until)
		}
		if want := time.Date(2026, 11, 20, 6, 0, 0, 0, ny); !until.Equal(want) {
			t.Errorf("bis %v, erwartet %v", until, want)
		}
		if _, _, active := c.SendGuard(now.Add(2 * time.Hour)); active {
			t.Error("Sperre um 06:00 New York noch aktiv")
		}
		if w := c.Warnings(); len(w) != 1 || !strings.Contains(w[0], "bis 06:00") {
			t.Errorf("Hinweis %q", w)
		}
	})
	t.Run("New York: 6,5 h voraus ist fehlerhaft", func(t *testing.T) {
		if c, err := ParseAt(tomlFile(newYork(`"2026-11-20 10:30"`)), now); err == nil {
			t.Fatalf("angenommen: %+v", c)
		}
	})
}

func TestHashNormalised(t *testing.T) {
	upper := "  " + strings.ToUpper(boxHash) + "\t"
	c := mustParse(t, tomlFile(map[string]string{
		"machine_token_sha256": `"` + upper + `"`,
		"watcher_token_sha256": `" ` + strings.ToUpper(watcherHash) + ` "`,
	}))
	if c.MachineTokenSHA256 != boxHash || c.WatcherTokenSHA256 != watcherHash {
		t.Errorf("nicht vereinheitlicht: %q %q", c.MachineTokenSHA256, c.WatcherTokenSHA256)
	}
	if w := c.Warnings(); len(w) != 0 {
		t.Errorf("Hinweise %q", w)
	}
	// Gleichheit zählt nach dem Vereinheitlichen.
	c = mustParse(t, tomlFile(map[string]string{"watcher_token_sha256": `"` + strings.ToUpper(boxHash) + `"`}))
	if w := c.Warnings(); len(w) != 1 || !strings.Contains(w[0], "watcher_token_sha256") {
		t.Errorf("gleiche Hashes in anderer Schreibweise: Hinweise %q", w)
	}
}

func TestAuthStamp(t *testing.T) {
	c := mustParse(t, tomlFile(map[string]string{
		"dashboard_readonly_user":          `"leser"`,
		"dashboard_readonly_password_hash": `"` + otherBcrypt + `"`,
	}))
	// Werte von PHP dashboard_auth_stamp() (hash_hmac sha256 über "Rolle\nBenutzer").
	if got := c.AuthStamp("admin"); got != "45345dd55730663ba02e61d02283d31765072f16e088a35afddd9a5020eff529" {
		t.Errorf("AuthStamp(admin) = %s", got)
	}
	if got := c.AuthStamp("readonly"); got != "eaf35dcd572306bdef8bf31c44781e7d04c9db5d327b7e19d74e23d15ef038b8" {
		t.Errorf("AuthStamp(readonly) = %s", got)
	}
	// Anderes Passwort = anderer Stempel; ohne Lese-Konto kein Stempel.
	changed := mustParse(t, tomlFile(map[string]string{"dashboard_password_hash": `"` + otherBcrypt + `"`}))
	if changed.AuthStamp("admin") == c.AuthStamp("admin") {
		t.Error("geänderter Passwort-Hash ergibt denselben Stempel")
	}
	if got := changed.AuthStamp("readonly"); got != "" {
		t.Errorf("AuthStamp ohne Lese-Konto = %q", got)
	}
	if got := mustParse(t, tomlFile(map[string]string{"dashboard_password_hash": `""`})).AuthStamp("admin"); got != "" {
		t.Errorf("AuthStamp ohne Hash = %q", got)
	}
}

// exported liefert nur die Werte der Datei (ohne den Zeitpunkt des Ladens
// und die geladene Zeitzone); nil-Listen zählen wie leere.
func exported(c *Config) Config {
	v := *c
	v.loc = nil
	v.loadedAt = time.Time{}
	rv := reflect.ValueOf(&v).Elem()
	for i := range rv.NumField() {
		f := rv.Field(i)
		if f.CanSet() && f.Kind() == reflect.Slice && f.IsNil() {
			f.Set(reflect.MakeSlice(f.Type(), 0, 0))
		}
	}
	return v
}

func sameValues(t testing.TB, got, want *Config) {
	t.Helper()
	g, w := exported(got), exported(want)
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("Werte weichen ab:\n got %+v\nwant %+v", g, w)
	}
}
