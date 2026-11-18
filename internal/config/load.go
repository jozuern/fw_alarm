package config

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
)

// Größer ist keine Konfiguration; so liest der Prüfer nie eine riesige Datei
// in den Speicher.
const maxFileSize = 1 << 20

// Die Probe-Sperre darf höchstens so weit in die Zukunft reichen: Eine
// vertippte Zeit (nächstes Jahr) blockierte sonst still einen echten Alarm
// von Hand.
const maxGuardAhead = 3 * time.Hour

// Load liest und prüft die Datei; Fehler = die Datei ist fehlerhaft.
func Load(path string) (*Config, error) { return LoadAt(path, time.Now()) }

// LoadAt wie Load, mit dem Zeitpunkt des Ladens (gegen ihn wird die
// Probe-Sperre geprüft).
func LoadAt(path string, now time.Time) (*Config, error) {
	data, err := readFile(path)
	if err != nil {
		return nil, err
	}
	return ParseAt(data, now)
}

// readFile liest eine Konfigurationsdatei, höchstens maxFileSize Bytes.
func readFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("Datei nicht lesbar: %w", err)
	}
	defer f.Close() // nur gelesen: ein Fehler beim Schließen ändert nichts
	data, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("Datei nicht lesbar: %w", err)
	}
	if len(data) > maxFileSize {
		return nil, fmt.Errorf("Datei nicht lesbar: größer als %d Bytes", maxFileSize)
	}
	return data, nil
}

// Parse prüft den Inhalt einer Datei; Fehler = fehlerhaft.
func Parse(data []byte) (*Config, error) { return ParseAt(data, time.Now()) }

// ParseAt wie Parse, mit dem Zeitpunkt des Ladens. Fehlertexte nennen
// Zeilen und Schlüssel, nie einen Wert (er kann ein Geheimnis sein).
func ParseAt(data []byte, now time.Time) (*Config, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("keine gültige UTF-8-Datei")
	}
	var raw map[string]any
	md, err := toml.Decode(string(data), &raw)
	if err != nil {
		var perr toml.ParseError
		if errors.As(err, &perr) {
			msg := fmt.Sprintf("Syntaxfehler in Zeile %d", perr.Position.Line)
			if perr.LastKey != "" {
				msg += fmt.Sprintf(" (bei „%s“)", perr.LastKey)
			}
			return nil, errors.New(msg)
		}
		return nil, errors.New("Syntaxfehler")
	}

	if len(md.Keys()) == 0 {
		// Leer, nur Leerraum oder nur Kommentare: ein Versehen (abgeschnitten,
		// geleert). Mit allen Standards gälte keine Probe-Sperre, und gesendet
		// würde an den echten Bark-Server.
		return nil, errors.New("die Datei setzt keinen einzigen Wert")
	}

	c := defaults()
	fields := fieldsByName()
	seen := map[string]bool{}
	// In der Reihenfolge der Datei, damit der erste Fehler der oberste ist.
	// Gezählt wird der oberste Name: "[extra]" und "a.b = 1" sind unbekannte
	// Schlüssel "extra" bzw. "a"; "bark_alarm_title.x = 1" macht aus einem
	// bekannten Schlüssel eine Tabelle – falscher Typ.
	for _, key := range md.Keys() {
		name := key[0]
		if seen[name] {
			continue
		}
		seen[name] = true
		f, ok := fields[name]
		if !ok {
			return nil, fmt.Errorf("unbekannter Schlüssel „%s“", name)
		}
		if err := assign(reflect.ValueOf(c).Elem().FieldByIndex(f.Index), raw[name]); err != nil {
			return nil, fmt.Errorf("falscher Typ bei „%s“: erwartet %s", name, err)
		}
	}
	if len(seen) != len(raw) {
		// Kann nur geschehen, wenn die Bibliothek einen Schlüssel nicht in
		// Keys() aufführt – dann lieber fehlerhaft als still übergangen.
		return nil, errors.New("Syntaxfehler: Schlüssel nicht eindeutig lesbar")
	}

	// Vereinheitlichen wie der PHP-Code bei jedem Gebrauch.
	c.MachineTokenSHA256 = strings.ToLower(phpTrim(c.MachineTokenSHA256))
	c.WatcherTokenSHA256 = strings.ToLower(phpTrim(c.WatcherTokenSHA256))
	c.BarkKeyStatus = phpTrim(c.BarkKeyStatus)
	c.LoginMaxFailures = max(1, c.LoginMaxFailures)
	c.LoginLockoutWindowSeconds = max(60, c.LoginLockoutWindowSeconds)
	c.BarkMaxTries = max(1, c.BarkMaxTries)
	if !seen["bark_false_alarm_volume"] {
		c.BarkFalseAlarmVolume = c.BarkAlarmVolume
	}

	loc, err := loadLocation(c.Timezone)
	if err != nil {
		return nil, err
	}
	c.loc = loc
	c.loadedAt = now

	if err := checkSendGuard(c, now); err != nil {
		return nil, err
	}
	if err := checkBarkHost(c.BarkHost); err != nil {
		return nil, err
	}
	return c, nil
}

// barkHostTrimmed bereinigt bark_host wie der Bark-Client vor dem Senden:
// Leerraum außen und Schrägstriche am Ende weg. Gesendet wird an
// <Ergebnis>/<Key>.
func barkHostTrimmed(host string) string {
	return strings.TrimRight(strings.TrimSpace(host), "/")
}

// checkBarkHost: Eine Abfrage oder ein Fragment in bark_host schöbe den Key
// aus dem Pfad heraus (<host>?x/<key>), Zugangsdaten gehören nicht in die
// Datei. Beides macht die Datei fehlerhaft – der Text nennt den Wert nicht
// (er kann ein Key oder ein Passwort sein).
func checkBarkHost(host string) error {
	h := barkHostTrimmed(host)
	bad := errors.New("bark_host darf weder Abfrage noch Fragment noch Zugangsdaten enthalten")
	if strings.ContainsAny(h, "?#") {
		return bad
	}
	// Zugangsdaten stehen vor dem Rechner: zwischen "//" und dem nächsten
	// "/". Von Hand gesucht, damit auch eine sonst unlesbare Adresse
	// (Leerzeichen darin) erkannt wird.
	rest, found := "", false
	if i := strings.Index(h, "://"); i >= 0 {
		rest, found = h[i+3:], true
	} else if strings.HasPrefix(h, "//") {
		rest, found = h[2:], true
	}
	if found {
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			rest = rest[:j]
		}
		if strings.Contains(rest, "@") {
			return bad
		}
	}
	return nil
}

// barkHostWarning nennt, was an einem ladbaren bark_host nicht stimmt
// ("" = nichts). Ein Pfad ist erlaubt (ein eigener Bark-Server unter einem
// Unterpfad), aber ungewöhnlich: Beim Bark-Server von api.day.app wäre sein
// erster Teil das Gerät, an das dann jede Nachricht ginge.
func barkHostWarning(host string) string {
	if phpTrim(host) == "" {
		return "bark_host ist leer – der Server kann nichts senden"
	}
	h := barkHostTrimmed(host)
	lower := strings.ToLower(h)
	noHTTP := "bark_host ist keine http(s)-Adresse – der Server kann nichts senden"
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return noHTTP
	}
	u, err := url.Parse(h)
	if err != nil || u.Host == "" {
		return noHTTP
	}
	if u.Path != "" || u.RawPath != "" {
		return "bark_host hat einen Pfad – alle Meldungen gingen an diese eine Adresse; nur für einen eigenen Bark-Server unter einem Unterpfad"
	}
	return ""
}

// loadLocation lädt eine Zeitzone mit Namen. "" und "Local" kennt PHP
// nicht – Go läse daraus still UTC bzw. die Zeitzone des Rechners.
func loadLocation(name string) (*time.Location, error) {
	if name == "" || name == "Local" {
		return nil, fmt.Errorf("unbekannte Zeitzone „%s“ in timezone", name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unbekannte Zeitzone „%s“ in timezone", name)
	}
	return loc, nil
}

func checkSendGuard(c *Config, now time.Time) error {
	hasKeys, hasUntil := len(c.SendGuardKeys) > 0, c.SendGuardUntil != ""
	if hasKeys != hasUntil {
		return errors.New("Probe-Sperre: send_guard_keys und send_guard_until nur zusammen setzen (oder beide leer)")
	}
	if !hasKeys {
		return nil
	}
	for i, key := range c.SendGuardKeys {
		if !validBarkKey(key) {
			return fmt.Errorf("Probe-Sperre: Eintrag %d in send_guard_keys ist kein gültiger Bark-Key", i+1)
		}
	}
	until, err := parseGuardUntil(c.SendGuardUntil, c.loc)
	if err != nil {
		return errors.New("Probe-Sperre: send_guard_until ist keine Zeit der Form JJJJ-MM-TT HH:MM")
	}
	if latest := now.Add(maxGuardAhead); until.After(latest) {
		return fmt.Errorf("Probe-Sperre: send_guard_until liegt mehr als 3 Stunden in der Zukunft (höchstens %s)",
			latest.In(c.loc).Format(guardLayout))
	}
	return nil
}

func fieldsByName() map[string]reflect.StructField {
	t := reflect.TypeFor[Config]()
	m := make(map[string]reflect.StructField, t.NumField())
	for i := range t.NumField() {
		f := t.Field(i)
		if name := f.Tag.Get("toml"); name != "" {
			m[name] = f
		}
	}
	return m
}

// assign setzt dst auf den gelesenen Wert v, wenn der Typ passt; sonst
// beschreibt der Fehler den erwarteten Typ.
func assign(dst reflect.Value, v any) error {
	switch dst.Kind() {
	case reflect.String:
		s, ok := v.(string)
		if !ok {
			return errors.New("Text in Anführungszeichen")
		}
		dst.SetString(s)
	case reflect.Int:
		n, ok := v.(int64)
		if !ok {
			return errors.New("eine ganze Zahl")
		}
		if int64(int(n)) != n {
			return errors.New("eine kleinere ganze Zahl")
		}
		dst.SetInt(n)
	case reflect.Bool:
		b, ok := v.(bool)
		if !ok {
			return errors.New("true oder false")
		}
		dst.SetBool(b)
	case reflect.Slice:
		items, ok := v.([]any)
		if !ok {
			return errors.New("eine Liste von Texten, z.B. [\"a\", \"b\"]")
		}
		list := make([]string, 0, len(items))
		for _, item := range items {
			s, ok := item.(string)
			if !ok {
				return errors.New("eine Liste von Texten, z.B. [\"a\", \"b\"]")
			}
			list = append(list, s)
		}
		dst.Set(reflect.ValueOf(list))
	default:
		// Nur die vier Arten oben kommen in Config vor; ein neues Feld anderer
		// Art fällt im ersten Test auf.
		return fmt.Errorf("einen Wert der Art %s", dst.Kind())
	}
	return nil
}
