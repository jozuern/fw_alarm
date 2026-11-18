package logview

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"

	"fwalarm/internal/core"
	"fwalarm/internal/model"
)

// maxScanBytes: So viele Bytes an Verlaufszeilen sieht ein Aufruf von Read
// höchstens an (PHP: read_command_log_filtered, 4 MiB). Gibt es weniger
// Treffer als verlangt – beim Filter alarm_core der Normalfall –, endet die
// Suche dort statt am Anfang der älteren Datei.
const maxScanBytes = 4 << 20

// Read liefert die Einträge für die Oberfläche (PHP: log_view): neueste
// zuerst, höchstens limit, nur die zum Filter (all | alarm | alarm_core |
// keys | system – jeder andere Wert passt auf nichts) und nur, was die Rolle
// sehen darf, je als Ansicht von View. Was die Rolle nicht sehen darf und was
// niemand sehen darf (unbekannte Ereignisse), fällt VOR dem Abzählen weg –
// sonst bekäme sie weniger als limit, und "Ältere anzeigen" verschwände.
//
// Gelesen wird aus dem Speicher (Store.HistoryScan): erst, was noch nicht
// angehängt ist, dann die Zeilen beider Dateien rückwärts – Zeilen, die
// sicher nicht geliefert würden (mayMatch), ohne sie zu dekodieren, und
// höchstens maxScanBytes davon.
func Read(c *core.Core, limit int, filter string, isAdmin bool) []map[string]any {
	keep := func(e model.Entry) bool {
		if !InFilter(e, filter) {
			return false
		}
		_, shown := View(e, isAdmin)
		return shown
	}
	may := func(line []byte) bool { return mayMatch(line, filter, isAdmin) }
	entries := c.Store.HistoryScan(limit, maxScanBytes, may, keep)
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		v, _ := View(e, isAdmin) // keep hat genau diesen Eintrag angenommen
		out = append(out, v)
	}
	return out
}

// InFilter: Gehört der Eintrag zum Filter? (PHP: log_entry_in_filter) all =
// alles, alarm_core = nur die Versand-Ereignisse, sonst die Kategorie
// (category). Ein unbekannter Filter passt auf nichts – log_view macht ihn
// vorher zu all.
func InFilter(e model.Entry, filter string) bool {
	switch filter {
	case "all":
		return true
	case "alarm_core":
		return Events[eventName(e)].Core
	}
	return category(e) == filter
}

// category: die Kategorie eines Eintrags für den Filter – alarm, keys oder
// system (PHP: log_event_category). Ein Befehls-Ereignis ist alarm, wenn es um
// einen ALARM-Befehl geht (Typ aus dem Unterobjekt command, sonst aus type);
// unbekannte Ereignisse zählen als system (gezeigt werden sie nie).
func category(e model.Entry) string {
	ev, ok := Events[eventName(e)]
	if !ok {
		return catSystem
	}
	if ev.Cat != catCommand {
		return ev.Cat
	}
	var typ any
	switch cmd := e["command"].(type) {
	case map[string]any:
		typ = cmd["type"]
	case []any:
		// In PHP ein Array ohne Schlüssel type.
	default:
		typ = e["type"]
	}
	if toString(typ) == "ALARM" {
		return catAlarm
	}
	return catSystem
}

// eventName: das Ereignis eines Eintrags; "" wenn es fehlt oder kein Text
// ist (PHP (string) machte daraus nie den Namen eines Ereignisses der
// Tabelle).
func eventName(e model.Entry) string {
	name, _ := e["event"].(string)
	return name
}

// View ist ein Eintrag, wie ihn der Browser bekommt (PHP: log_event_view): at
// (log_at) und event, dann nur die Felder der Tabelle in festem Typ – int als
// int64, bool, str als string. false: gar nicht ausliefern (unbekanntes
// Ereignis, oder für diese Rolle nicht sichtbar). Die Ansicht teilt nichts mit
// dem Eintrag; ihre Schlüssel in PHPs Reihenfolge nennt Keys.
func View(e model.Entry, isAdmin bool) (map[string]any, bool) {
	name := eventName(e)
	ev, ok := Events[name]
	if !ok || ev.Admin && !isAdmin {
		return nil, false
	}
	v := make(map[string]any, len(ev.Fields)+2)
	v["at"] = toInt(e["log_at"])
	v["event"] = name
	for _, f := range ev.Fields {
		val := resolve(e, f.Path)
		if val == nil {
			if f.Optional {
				continue
			}
			val = f.Default
		}
		switch f.Type {
		case "int":
			v[f.Name] = toInt(val)
		case "bool":
			v[f.Name] = model.BoolOf(val)
		default:
			v[f.Name] = toString(val)
		}
	}
	return v, true
}

// Keys nennt die Schlüssel einer Ansicht von View in der Reihenfolge, in der
// PHP sie schreibt: at, event, dann die Felder in der Reihenfolge der Tabelle
// (ein Feld, das fehlt, fehlt auch hier).
func Keys(v map[string]any) []string {
	name, _ := v["event"].(string)
	keys := make([]string, 0, len(v))
	keys = append(keys, "at", "event")
	for _, f := range Events[name].Fields {
		if _, ok := v[f.Name]; ok {
			keys = append(keys, f.Name)
		}
	}
	return keys
}

// resolve folgt dem Pfad ("command.id") wie PHP log_event_view: Teil für Teil
// in Objekte hinein (ein Schlüssel, der selbst einen Punkt enthält, zählt
// dort nie); nil, wenn es ihn nicht gibt oder er null ist.
func resolve(e model.Entry, path string) any {
	var cur any = map[string]any(e)
	for part := range strings.SplitSeq(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[part]
	}
	return cur
}

// toInt: PHPs (int) – ein Objekt oder eine Liste ist 1, wenn es etwas
// enthält, sonst 0 (wie (int) auf ein Array); alles andere wie model.IntOf.
// Bewusst anders als PHP: Eine Kommazahl jenseits von int64 wird auf die
// Grenze gekappt (PHP rechnet sie modulo 2^64 um) – kein Schreiber erzeugt so
// etwas.
func toInt(v any) int64 {
	switch x := v.(type) {
	case map[string]any:
		return boolInt(len(x) > 0)
	case []any:
		return boolInt(len(x) > 0)
	}
	return model.IntOf(v)
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// toString: Text wie PHP log_event_view – einfache Werte wie (string), eine
// Zahl so, wie PHP sie nach json_decode als Text schreibt (numberString),
// Objekte und Listen "".
func toString(v any) string {
	switch x := v.(type) {
	case json.Number:
		return numberString(string(x))
	case float64:
		return floatString(x)
	}
	return model.StrOf(v)
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
		} else {
			b.WriteByte('+')
		}
		b.WriteString(strconv.Itoa(abs(decpt - 1)))
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

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// eventKey und alarmType: wie json_encode und encoding/json ein Ereignis und
// einen ALARM-Befehl schreiben.
var (
	eventKey  = []byte(`"event":"`)
	alarmType = []byte(`"type":"ALARM"`)
)

// mayMatch: Kann die rohe Zeile ein Eintrag sein, den Read liefert – im
// Filter und für die Rolle sichtbar? (PHP: log_line_may_match, dazu Rolle und
// unbekannte Ereignisse.) Eine billige Vorprüfung, damit nicht jede Zeile
// dekodiert wird; maßgeblich bleiben InFilter und View. Sie lässt jeden
// Eintrag durch, den View zeigt und InFilter annimmt, und verlässt sich wie
// PHP darauf, wie json_encode und encoding/json schreiben: das Ereignis als
// "event":"<name>" ohne Leerraum, Anführungszeichen in Texten maskiert – ein
// Text kann "event":"key_added" also nicht vortäuschen. Ein Unterobjekt mit
// eigenem event könnte es; darum zählt jedes Vorkommen (ein Treffer zu viel
// kostet nur das Dekodieren). Eine Zeile ganz ohne dieses Muster (von Hand
// geschrieben?) wird lieber dekodiert als übergangen.
func mayMatch(line []byte, filter string, isAdmin bool) bool {
	seen := false
	for rest := line; ; {
		i := bytes.Index(rest, eventKey)
		if i < 0 {
			break
		}
		rest = rest[i+len(eventKey):]
		name := rest
		if j := bytes.IndexByte(rest, '"'); j >= 0 {
			name = rest[:j]
		}
		seen = true
		ev, known := Events[string(name)]
		if !known || ev.Admin && !isAdmin {
			continue // so nie gezeigt
		}
		switch filter {
		case "all":
			return true
		case "alarm_core":
			if ev.Core {
				return true
			}
		case catKeys:
			if ev.Cat == catKeys {
				return true
			}
		case catAlarm:
			// Ein Befehl gehört zu alarm, wenn er ein ALARM-Befehl ist.
			if ev.Cat == catAlarm || ev.Cat == catCommand && bytes.Contains(line, alarmType) {
				return true
			}
		case catSystem:
			if ev.Cat == catSystem || ev.Cat == catCommand {
				return true
			}
		}
	}
	return !seen
}
