// Package config liest und schreibt die Konfiguration des Servers
// (/etc/fw-alarm/config.toml).
//
// Es gelten dieselben Schlüsselnamen wie in der config.php der PHP-Fassung
// (ohne data_dir und health_check_url), dazu state_dir, listen,
// control_socket, check_interval_seconds und die Probe-Sperre
// send_guard_keys/send_guard_until. Fehlt ein Schlüssel, gilt der Wert, den
// der PHP-Code dafür nimmt (das "?? …" dort) – das Verhalten soll sich beim
// Umschalten nicht ändern.
//
// "Fehlerhaft" ist eine Datei nur, wenn sie sich nicht lesen lässt, einen
// Syntaxfehler, einen unbekannten Schlüssel, einen Wert vom falschen Typ,
// eine unbekannte Zeitzone, eine unzulässige Probe-Sperre oder einen
// bark_host mit Abfrage, Fragment oder Zugangsdaten hat, keinen einzigen
// Wert setzt oder auf einem Unix-Socket lauscht, ohne eine brauchbare
// proxy_client_ip_header zu nennen (Parse liefert dann einen Fehler). Alles
// andere sind Hinweise (Warnings): Der Server arbeitet mit der Datei.
//
// Im laufenden Server hält ein Provider die geltende Fassung im Speicher,
// prüft die Datei im Hintergrund und legt nach jeder gültig geladenen
// Fassung eine Kopie in den Datenordner (provider.go).
package config

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/textproto"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"
)

// Config hält einen geladenen Stand. Er wird nach dem Laden nicht mehr
// verändert und darf von beliebig vielen Goroutinen gelesen werden.
// Die Reihenfolge der Felder ist die Reihenfolge in der geschriebenen Datei.
type Config struct {
	// Probe-Sperre: Solange sie gilt, sendet der Server an keinen anderen Key.
	// Sie steht als Erstes in der Datei, zwischen ihren beiden Werten die
	// Zeitzone ihres Endes – so hält ein abgeschnittener Anfang der Datei sie
	// entweder ganz (samt Zeitzone) oder ist fehlerhaft.
	SendGuardKeys  []string `toml:"send_guard_keys"`
	Timezone       string   `toml:"timezone"`
	SendGuardUntil string   `toml:"send_guard_until"` // Ortszeit "JJJJ-MM-TT HH:MM" in Timezone

	// Zugang der Box: SHA-256 des Maschinen-Tokens (vereinheitlicht: ohne
	// Leerraum außen, Kleinbuchstaben).
	MachineTokenSHA256 string `toml:"machine_token_sha256"`

	// Anmeldung am Dashboard.
	DashboardUser                 string `toml:"dashboard_user"`
	DashboardPasswordHash         string `toml:"dashboard_password_hash"`
	DashboardReadonlyUser         string `toml:"dashboard_readonly_user"`
	DashboardReadonlyPasswordHash string `toml:"dashboard_readonly_password_hash"`
	LoginMaxFailures              int    `toml:"login_max_failures"`           // nie unter 1
	LoginLockoutWindowSeconds     int    `toml:"login_lockout_window_seconds"` // nie unter 60

	// Hinter einem Proxy / Tunnel.
	TrustedProxies      []string `toml:"trusted_proxies"`
	ProxyClientIPHeader string   `toml:"proxy_client_ip_header"`

	// Box.
	OfflineAfterSeconds int    `toml:"offline_after_seconds"`
	BoxDeviceID         string `toml:"box_device_id"`

	// Meldungen an den Admin und Wächter.
	BarkKeyStatus               string `toml:"bark_key_status"` // ohne Leerraum außen
	WatchdogOfflineAfterSeconds int    `toml:"watchdog_offline_after_seconds"`
	DemoReminderAfterSeconds    int    `toml:"demo_reminder_after_seconds"`
	WatcherTokenSHA256          string `toml:"watcher_token_sha256"` // vereinheitlicht wie MachineTokenSHA256

	// Arbeitsmodus, Adressen, Befehle.
	MuteMaxSeconds              int    `toml:"mute_max_seconds"`
	PublicBaseURL               string `toml:"public_base_url"`
	DeliveredCommandLockSeconds int    `toml:"delivered_command_lock_seconds"`
	PendingCommandTTLSeconds    int    `toml:"pending_command_ttl_seconds"`

	// Alarm direkt vom Server an Bark.
	BarkHost        string   `toml:"bark_host"`
	BarkKeysAlarm   []string `toml:"bark_keys_alarm"` // wie eingetragen; bereinigt wird beim Gebrauch
	BarkAlarmTitle  string   `toml:"bark_alarm_title"`
	BarkAlarmBody   string   `toml:"bark_alarm_body"`
	BarkAlarmSound  string   `toml:"bark_alarm_sound"`
	BarkAlarmVolume int      `toml:"bark_alarm_volume"`
	BarkAlarmCall   bool     `toml:"bark_alarm_call"`
	BarkMaxTries    int      `toml:"bark_max_tries"` // nie unter 1

	// Entwarnung.
	BarkFalseAlarmSound  string `toml:"bark_false_alarm_sound"`
	BarkFalseAlarmVolume int    `toml:"bark_false_alarm_volume"` // ohne Eintrag: BarkAlarmVolume

	// Neu in der Go-Fassung.
	StateDir             string `toml:"state_dir"`
	Listen               string `toml:"listen"`         // host:port oder unix:<Pfad>
	ControlSocket        string `toml:"control_socket"` // Unix-Socket des Steuer-Zugangs
	CheckIntervalSeconds int    `toml:"check_interval_seconds"`

	loc      *time.Location // geladene Zeitzone (nil bei einem von Hand gebauten Stand)
	loadedAt time.Time      // Zeitpunkt des Ladens – an ihm misst Warnings die Probe-Sperre
}

// Standardwerte, die auch ohne lesbare Datei gebraucht werden: Die Befehle
// health, stop … suchen den Steuer-Zugang, der Start die Kopie der letzten
// gültigen Fassung.
const (
	DefaultStateDir      = "/var/lib/fw-alarm"
	DefaultControlSocket = "/run/fw-alarm/control.sock"
)

// Fehlt ein Schlüssel, gilt dieser Wert (die Fundstelle im PHP-Code steht
// dabei; "neu" = gibt es nur in der Go-Fassung).
func defaults() *Config {
	return &Config{
		SendGuardKeys:                 []string{},                                  // neu
		Timezone:                      "Europe/Berlin",                             // Vorgabe des Plans (PHP: die des Servers)
		SendGuardUntil:                "",                                          // neu
		MachineTokenSHA256:            "",                                          // common.php:609
		DashboardUser:                 "",                                          // common.php:835 (index.php:69 ohne ??)
		DashboardPasswordHash:         "",                                          // common.php:836
		DashboardReadonlyUser:         "",                                          // index.php:66
		DashboardReadonlyPasswordHash: "",                                          // index.php:67
		LoginMaxFailures:              8,                                           // common.php:662
		LoginLockoutWindowSeconds:     900,                                         // common.php:663
		TrustedProxies:                []string{},                                  // common.php:100
		ProxyClientIPHeader:           "",                                          // common.php:634
		OfflineAfterSeconds:           180,                                         // dashboard_api.php:129, common.php:1739
		BoxDeviceID:                   "",                                          // common.php:982
		BarkKeyStatus:                 "",                                          // common.php:2783
		WatchdogOfflineAfterSeconds:   600,                                         // common.php:2485
		DemoReminderAfterSeconds:      14400,                                       // common.php:2487
		WatcherTokenSHA256:            "",                                          // common.php:2472
		MuteMaxSeconds:                43200,                                       // common.php:1985
		PublicBaseURL:                 "",                                          // shell.php:141
		DeliveredCommandLockSeconds:   120,                                         // dashboard_api.php:249
		PendingCommandTTLSeconds:      300,                                         // common.php:1103
		BarkHost:                      "https://api.day.app",                       // common.php:2308
		BarkKeysAlarm:                 []string{},                                  // common.php:1944
		BarkAlarmTitle:                "FEUERWEHR-ALARM",                           // common.php:2944
		BarkAlarmBody:                 "Manuelle Alarmierung ueber das Dashboard!", // common.php:3132
		BarkAlarmSound:                "alarm_fw",                                  // common.php:2927, 2946
		BarkAlarmVolume:               10,                                          // common.php:2950
		BarkAlarmCall:                 true,                                        // common.php:2954
		BarkMaxTries:                  3,                                           // common.php:3008
		BarkFalseAlarmSound:           "glass",                                     // common.php:2976
		BarkFalseAlarmVolume:          10,                                          // common.php:2972 (= bark_alarm_volume)
		StateDir:                      DefaultStateDir,                             // neu
		Listen:                        "127.0.0.1:8080",                            // neu
		ControlSocket:                 DefaultControlSocket,                        // neu
		CheckIntervalSeconds:          900,                                         // neu
	}
}

// keyNames liefert alle Schlüssel in der Reihenfolge der Felder.
func keyNames() []string {
	t := reflect.TypeFor[Config]()
	names := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		if name := t.Field(i).Tag.Get("toml"); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// Gültiger Bark-Key (PHP: valid_bark_key).
var barkKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{5,64}$`)

func validBarkKey(key string) bool { return barkKeyPattern.MatchString(key) }

var sha256HexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// phpTrim entfernt, was PHPs trim() entfernt (nicht mehr – z.B. kein
// geschütztes Leerzeichen).
func phpTrim(s string) string { return strings.Trim(s, " \t\n\r\x00\x0b") }

// clientIPHeaderPattern: erlaubte Zeichen im Namen der Kopfzeile aus
// proxy_client_ip_header – PHP prüft nach strtoupper und "-" → "_" auf
// ^[A-Z0-9_]+$ (common.php, login_client_ip).
var clientIPHeaderPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ClientIPHeader liefert den Namen der Kopfzeile aus
// proxy_client_ip_header, wie net/http ihn führt: "_" wie "-", Groß/klein
// und Leerraum außen egal (PHP liest $_SERVER['HTTP_…']). false: leer oder
// unbrauchbar – dann zählt der Absender selbst, und alle Besucher über den
// Proxy sind EIN Absender.
func (c *Config) ClientIPHeader() (string, bool) {
	name := phpTrim(c.ProxyClientIPHeader)
	if !clientIPHeaderPattern.MatchString(name) {
		return "", false
	}
	return textproto.CanonicalMIMEHeaderKey(strings.ReplaceAll(name, "_", "-")), true
}

// Location liefert die Zeitzone der Konfiguration (für Zeitstempel in
// Meldungen und die Probe-Sperre).
func (c *Config) Location() *time.Location {
	if c.loc != nil {
		return c.loc
	}
	if loc, err := loadLocation(c.Timezone); err == nil {
		return loc
	}
	// Ein von Hand gebauter Stand mit unbrauchbarer Zeitzone: UTC statt Absturz.
	return time.UTC
}

// AuthStamp ist der Fingerabdruck der Zugangsdaten einer Rolle: HMAC-SHA256
// über "Rolle\nBenutzer" mit dem Passwort-Hash als Schlüssel (PHP:
// dashboard_auth_stamp). Eine Sitzung gilt nur, solange er gleich bleibt –
// wer das Passwort ändert, meldet alle Sitzungen dieses Kontos ab.
// "" = dieses Konto gibt es (so) nicht. Jede Rolle außer "admin" gilt als
// Lese-Rolle (die sichere Richtung, wie in PHP).
func (c *Config) AuthStamp(role string) string {
	user, hash := c.DashboardReadonlyUser, c.DashboardReadonlyPasswordHash
	if role == "admin" {
		user, hash = c.DashboardUser, c.DashboardPasswordHash
	}
	if user == "" || hash == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(hash))
	mac.Write([]byte(role + "\n" + user)) // Schreiben in einen Hash scheitert nie
	return hex.EncodeToString(mac.Sum(nil))
}

// SendGuard sagt, ob die Probe-Sperre zum Zeitpunkt now gilt, und für welche
// Keys. Sie gilt, solange send_guard_keys nicht leer ist und send_guard_until
// nach now liegt – sie endet also von selbst.
func (c *Config) SendGuard(now time.Time) (keys []string, until time.Time, active bool) {
	if len(c.SendGuardKeys) == 0 || c.SendGuardUntil == "" {
		return nil, time.Time{}, false
	}
	until, err := parseGuardUntil(c.SendGuardUntil, c.Location())
	if err != nil {
		// Beim Laden geprüft; nur ein von Hand gebauter Stand kommt hierher.
		return nil, time.Time{}, false
	}
	if !until.After(now) {
		return nil, until, false
	}
	return append([]string(nil), c.SendGuardKeys...), until, true
}

const guardLayout = "2006-01-02 15:04"

func parseGuardUntil(s string, loc *time.Location) (time.Time, error) {
	return time.ParseInLocation(guardLayout, s, loc)
}

// Warnings nennt, was an der Datei nicht stimmt, ohne sie fehlerhaft zu
// machen – ohne einen Wert, nur Schlüssel (kein Hash, kein Token, kein Key).
// Die Probe-Sperre wird am Zeitpunkt des Ladens gemessen. Leer = nichts zu
// melden.
func (c *Config) Warnings() []string {
	at := c.loadedAt
	if at.IsZero() {
		at = time.Now()
	}
	var w []string
	if !sha256HexPattern.MatchString(c.MachineTokenSHA256) {
		w = append(w, "machine_token_sha256 fehlt oder ist kein SHA-256-Hash (64 Hex-Zeichen) – die Box kann sich so nicht anmelden")
	}
	if c.WatcherTokenSHA256 != "" {
		if !sha256HexPattern.MatchString(c.WatcherTokenSHA256) {
			w = append(w, "watcher_token_sha256 ist kein SHA-256-Hash (64 Hex-Zeichen) – der Außen-Wächter kann sich so nicht melden")
		} else if c.WatcherTokenSHA256 == c.MachineTokenSHA256 {
			w = append(w, "watcher_token_sha256 ist derselbe Hash wie machine_token_sha256 – api/alive.php bleibt aus (der Außen-Wächter darf nie das Token der Box kennen)")
		}
	}
	if !passwordHashUsable(c.DashboardPasswordHash) {
		w = append(w, "dashboard_password_hash ist weder ein bcrypt- noch ein Argon2id-Hash – so kann sich der Admin nicht anmelden (neuer Hash: fw-alarm hash-password)")
	}
	if (c.DashboardReadonlyUser != "" || c.DashboardReadonlyPasswordHash != "") &&
		!passwordHashUsable(c.DashboardReadonlyPasswordHash) {
		w = append(w, "dashboard_readonly_password_hash ist weder ein bcrypt- noch ein Argon2id-Hash – so kann sich der Lese-Benutzer nicht anmelden")
	}
	if len(c.TrustedProxies) > 0 {
		if _, ok := c.ClientIPHeader(); !ok {
			reason := "leer"
			if phpTrim(c.ProxyClientIPHeader) != "" {
				reason = "unbrauchbar (nur Buchstaben, Ziffern, - und _)"
			}
			w = append(w, "trusted_proxies ist gesetzt, proxy_client_ip_header aber "+reason+
				": alle Besucher über den Proxy zählen als ein Absender")
		}
	}
	if !path.IsAbs(c.StateDir) && !filepath.IsAbs(c.StateDir) {
		w = append(w, "state_dir ist kein absoluter Pfad")
	}
	if c.CheckIntervalSeconds <= 0 {
		w = append(w, fmt.Sprintf("check_interval_seconds = %d – keine selbsttätigen Prüf-Läufe: der Server meldet weder eine stumme Box noch andere Störungen",
			c.CheckIntervalSeconds))
	}
	for i, key := range c.BarkKeysAlarm {
		if !validBarkKey(phpTrim(key)) {
			w = append(w, fmt.Sprintf("bark_keys_alarm: Eintrag %d ist kein gültiger Bark-Key und wird nie alarmiert", i+1))
		}
	}
	if hint := barkHostWarning(c.BarkHost); hint != "" {
		w = append(w, hint)
	}
	if c.BarkKeyStatus != "" && !validBarkKey(c.BarkKeyStatus) {
		w = append(w, "bark_key_status ist kein gültiger Bark-Key – Meldungen an den Admin scheitern")
	}
	if keys, until, active := c.SendGuard(at); active {
		w = append(w, fmt.Sprintf("Probe-Sperre aktiv bis %s – der Server sendet nur an %d Key(s)",
			until.In(c.Location()).Format("15:04"), len(keys)))
	}
	return w
}
