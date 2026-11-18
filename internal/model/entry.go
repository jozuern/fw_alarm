package model

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Entry ist eine Zeile des Verlaufs: ein JSON-Objekt mit den Feldern, die
// PHP schreibt (event, log_at, …), plus uid. Bewusst lose – alten Zeilen
// fehlen Felder; gelesen wird über die nachsichtigen Str/Int/Bool.
type Entry map[string]any

// MaxOutbox: so viele Einträge fasst die Outbox eines Dokuments.
const MaxOutbox = 500

// Ereignisse des Verlaufs: eines je Eintrag von PHP log_event_types()
// (lib/common.php), der Wert ist der PHP-Name – auch wo "nas" historisch
// ist. Dazu die zwei neuen des Go-Servers (Spec §7).
const (
	EvCreated           = "created"
	EvDelivered         = "delivered"
	EvAcked             = "acked"
	EvExpired           = "expired"
	EvCancelled         = "cancelled"
	EvLateAck           = "late_ack"
	EvDemoEnabled       = "demo_enabled"
	EvDemoDisabled      = "demo_disabled"
	EvDemoRepaired      = "demo_repaired" // nur noch für alte Einträge (Spec §7)
	EvKeyAdded          = "key_added"
	EvKeyRemoved        = "key_removed"
	EvKeyRenamed        = "key_renamed"
	EvKeyReplaced       = "key_replaced"
	EvKeyPhoneChanged   = "key_phone_changed"
	EvKeyTokenRenewed   = "key_token_renewed"
	EvKeyMuted          = "key_muted"
	EvKeyUnmuted        = "key_unmuted"
	EvKeyMuteVolume     = "key_mute_volume"
	EvKeyMutePermanent  = "key_mute_permanent"
	EvKeyTest           = "key_test"
	EvNotifyChanged     = "notify_changed"
	EvSettingChanged    = "setting_changed"
	EvBoxRestart        = "box_restart"
	EvNasAlarmStarted   = "nas_alarm_started"
	EvFalseAlarmStarted = "false_alarm_started"
	EvNasAlarm          = "nas_alarm"
	EvFalseAlarm        = "false_alarm"
	EvRelayAlarm        = "relay_alarm"
	EvBoxOffline        = "box_offline"
	EvBoxRecovered      = "box_recovered"
	EvNasWebRecovered   = "nas_web_recovered"
	EvWatcherSilent     = "watcher_silent"
	EvWatcherRecovered  = "watcher_recovered"
	EvNasWebFailed      = "nas_web_failed"

	EvStateRewound    = "state_rewound"
	EvSendInterrupted = "send_interrupted"
)

// AllEvents nennt jedes Ereignis genau einmal, in der Reihenfolge von PHP
// log_event_types(), danach die neuen.
var AllEvents = []string{
	EvCreated, EvDelivered, EvAcked, EvExpired, EvCancelled, EvLateAck,
	EvDemoEnabled, EvDemoDisabled, EvDemoRepaired,
	EvKeyAdded, EvKeyRemoved, EvKeyRenamed, EvKeyReplaced, EvKeyPhoneChanged, EvKeyTokenRenewed,
	EvKeyMuted, EvKeyUnmuted, EvKeyMuteVolume, EvKeyMutePermanent, EvKeyTest,
	EvNotifyChanged, EvSettingChanged, EvBoxRestart,
	EvNasAlarmStarted, EvFalseAlarmStarted, EvNasAlarm, EvFalseAlarm, EvRelayAlarm,
	EvBoxOffline, EvBoxRecovered, EvNasWebRecovered, EvWatcherSilent, EvWatcherRecovered, EvNasWebFailed,
	EvStateRewound, EvSendInterrupted,
}

// NewEntry baut einen Verlaufseintrag – der einzige Weg dazu. Er setzt event,
// log_at (Unix-Sekunden) und uid (16 Zufallsbytes, hex); gleichnamige
// Felder aus fields werden dabei überschrieben. Jeder Wert wird in seine
// JSON-Form gebracht (Objekte als map[string]any, Listen als []any, Zahlen
// als json.Number, ungültiges UTF-8 als U+FFFD): Ein Eintrag im Speicher
// sieht genauso aus wie einer, der von der Platte kommt, und teilt nichts
// mit dem Aufrufer.
func NewEntry(event string, at time.Time, fields map[string]any) Entry {
	e := make(Entry, len(fields)+3)
	for k, v := range fields {
		e[k] = jsonValue(v)
	}
	e["event"] = event
	e["log_at"] = json.Number(strconv.FormatInt(at.Unix(), 10))
	e["uid"] = NewUID()
	return e
}

// NewUID: eine neue uid für einen Verlaufseintrag – 16 Zufallsbytes als hex.
// NewEntry setzt sie selbst; die Ablage braucht sie nur für einen Eintrag,
// der ohne uid hereinkommt (ein Programmierfehler, der nichts verlieren
// soll).
func NewUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read scheitert nie (Go ≥ 1.24 bricht sonst das Programm ab)
	return hex.EncodeToString(b[:])
}

// jsonValue bringt einen Wert in die Form, die json mit UseNumber liest.
// Was sich nicht als JSON schreiben lässt (NaN, Kanal …), wird als Text
// festgehalten – ein Verlaufseintrag geht nie verloren.
func jsonValue(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		b, _ = json.Marshal(fmt.Sprint(v)) // ein Text lässt sich immer schreiben
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out any
	_ = dec.Decode(&out) // was json.Marshal schreibt, liest json wieder
	return out
}

// lookup sucht ein Feld; "command.id" führt in ein Unterobjekt (wie PHP
// log_event_view). Ein Schlüssel, der selbst einen Punkt enthält, gewinnt.
func (e Entry) lookup(k string) (any, bool) {
	if v, ok := e[k]; ok {
		return v, true
	}
	if !strings.Contains(k, ".") {
		return nil, false
	}
	var cur any = map[string]any(e)
	for part := range strings.SplitSeq(k, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[part]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// Str liest ein Feld als Text wie PHPs (string): Zahlen so, wie PHP sie nach
// json_decode schreibt (StrOf), true als "1", false/null/fehlend als "",
// Objekte und Listen als "".
func (e Entry) Str(k string) string {
	v, _ := e.lookup(k)
	return StrOf(v)
}

// StrOf: ein Wert, wie ihn Str liest (nil = fehlt) – die EINE Fassung von
// PHPs (string) im Programm. Eine JSON-Zahl, wie PHP sie nach json_decode als
// Text schreibt (numberString), eine Kommazahl wie PHPs (string) auf einen
// float (floatString).
func StrOf(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return numberString(string(x))
	case bool:
		if x {
			return "1"
		}
		return ""
	case float64:
		return floatString(x)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	}
	return ""
}

// numberString: eine JSON-Zahl als Text wie PHP: Eine ganze Zahl, die in
// int64 passt, ist nach json_decode ein int und bleibt ihre Ziffernfolge ("-0"
// wird "0"); alles andere ist ein float (floatString).
func numberString(s string) string {
	if !strings.ContainsAny(s, ".eE") {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return strconv.FormatInt(n, 10)
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return s // kann bei einer gültigen JSON-Zahl nicht vorkommen
	}
	return floatString(f) // zu groß: ±Inf wie PHP
}

// floatString schreibt eine Kommazahl wie PHPs (string) (precision = 14,
// zend_gcvt): höchstens 14 gültige Stellen ohne Nullen am Ende; liegt der
// Dezimalpunkt mehr als 14 Stellen links oder mehr als 4 Stellen rechts der
// ersten Ziffer, als "1.5E+20" bzw. "1.0E-5".
func floatString(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NAN"
	case math.IsInf(f, 1):
		return "INF"
	case math.IsInf(f, -1):
		return "-INF"
	}
	var b strings.Builder
	if math.Signbit(f) {
		b.WriteByte('-')
		f = -f
	}
	mant, exp, _ := strings.Cut(strconv.FormatFloat(f, 'e', 13, 64), "e")
	digits := strings.TrimRight(strings.Replace(mant, ".", "", 1), "0")
	if digits == "" {
		digits = "0"
	}
	x, _ := strconv.Atoi(exp) // FormatFloat schreibt immer einen Exponenten
	decpt := x + 1            // Stellen vor dem Komma (zend_dtoa: decpt)
	if f == 0 {
		decpt = 1
	}
	switch {
	case decpt < -3 || decpt > 14:
		b.WriteByte(digits[0])
		b.WriteByte('.')
		if len(digits) > 1 {
			b.WriteString(digits[1:])
		} else {
			b.WriteByte('0')
		}
		b.WriteByte('E')
		if decpt-1 < 0 {
			b.WriteByte('-')
			b.WriteString(strconv.Itoa(1 - decpt))
		} else {
			b.WriteByte('+')
			b.WriteString(strconv.Itoa(decpt - 1))
		}
	case decpt <= 0:
		b.WriteString("0.")
		b.WriteString(strings.Repeat("0", -decpt))
		b.WriteString(digits)
	case len(digits) <= decpt:
		b.WriteString(digits)
		b.WriteString(strings.Repeat("0", decpt-len(digits)))
	default:
		b.WriteString(digits[:decpt])
		b.WriteByte('.')
		b.WriteString(digits[decpt:])
	}
	return b.String()
}

// Int liest ein Feld als Zahl wie PHPs (int): Kommazahlen abgeschnitten,
// Text nach seinen führenden Ziffern ("12abc" = 12, "1e3" = 1000), true = 1,
// ein Objekt oder eine Liste 1, wenn es etwas enthält, alles andere 0.
func (e Entry) Int(k string) int64 {
	v, _ := e.lookup(k)
	return IntOf(v)
}

// IntOf: ein Wert, wie ihn Int liest (nil = fehlt) – die EINE Fassung von
// PHPs (int) im Programm, auch für einen Text aus einer Anfrage. Bewusst
// anders als PHP: Eine Kommazahl jenseits von int64 wird auf die Grenze
// gekappt (PHP rechnet sie modulo 2^64 um) – kein Schreiber erzeugt so etwas.
func IntOf(v any) int64 {
	switch x := v.(type) {
	case json.Number:
		return phpInt(string(x))
	case string:
		return phpInt(x)
	case float64:
		return floatToInt(x)
	case int:
		return int64(x)
	case int64:
		return x
	case bool:
		if x {
			return 1
		}
	case map[string]any:
		if len(x) > 0 {
			return 1
		}
	case []any:
		if len(x) > 0 {
			return 1
		}
	}
	return 0
}

// Bool liest ein Feld wie PHPs !empty(): falsch sind fehlend, null, false,
// 0, "", "0" und leere Objekte/Listen – alles andere ist wahr.
func (e Entry) Bool(k string) bool {
	v, _ := e.lookup(k)
	return BoolOf(v)
}

// BoolOf: ein Wert, wie ihn Bool liest (nil = fehlt).
func BoolOf(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != "" && x != "0"
	case json.Number:
		f, err := strconv.ParseFloat(string(x), 64)
		return err != nil || f != 0
	case float64:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	case map[string]any:
		return len(x) > 0
	case []any:
		return len(x) > 0
	}
	return true
}

// leadingNumber: führende Zahl eines Texts nach PHP-Art (Leerraum davor
// erlaubt, danach beliebiger Rest).
var leadingNumber = regexp.MustCompile(`^[ \t\n\r\v\f]*([+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?)`)

// phpInt wandelt einen Text wie PHPs (int) in eine Zahl.
func phpInt(s string) int64 {
	m := leadingNumber.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
		return n
	}
	f, err := strconv.ParseFloat(m[1], 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0
	}
	return floatToInt(f)
}

// floatToInt schneidet ab und begrenzt auf den Bereich von int64 (NaN = 0).
func floatToInt(f float64) int64 {
	switch {
	case math.IsNaN(f):
		return 0
	case f >= math.MaxInt64:
		return math.MaxInt64
	case f <= math.MinInt64:
		return math.MinInt64
	}
	return int64(f)
}

// appendOutbox hängt an und lässt über MaxOutbox die ältesten fallen.
func appendOutbox(box []Entry, e Entry) []Entry {
	box = append(box, e)
	if over := len(box) - MaxOutbox; over > 0 {
		box = slices.Delete(box, 0, over)
	}
	return box
}
