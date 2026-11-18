package logview

import (
	"bytes"
	"slices"
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

// Filters: die Filter außer all – Alarme, nur die Versand-Ereignisse (für
// "Letzter Alarm"), Empfänger, System (PHP: die Liste im Zweig log_view von
// dashboard_api.php). Die EINE Liste: log_view macht jeden anderen Wert zu
// all, InFilter lässt ihn auf nichts passen.
var Filters = []string{"alarm", "alarm_core", "keys", "system"}

// InFilter: Gehört der Eintrag zum Filter? (PHP: log_entry_in_filter) all =
// alles, alarm_core = nur die Versand-Ereignisse, sonst die Kategorie
// (category). Ein Filter, der nicht in Filters steht, passt auf nichts –
// log_view macht ihn vorher zu all.
func InFilter(e model.Entry, filter string) bool {
	switch {
	case filter == "all":
		return true
	case !slices.Contains(Filters, filter):
		return false
	case filter == "alarm_core":
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
	if model.StrOf(typ) == "ALARM" {
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
// int64, bool, str als string, umgewandelt wie PHP (model.IntOf, BoolOf,
// StrOf). false: gar nicht ausliefern (unbekanntes
// Ereignis, oder für diese Rolle nicht sichtbar). Die Ansicht teilt nichts mit
// dem Eintrag; ihre Schlüssel in PHPs Reihenfolge nennt Keys.
func View(e model.Entry, isAdmin bool) (map[string]any, bool) {
	name := eventName(e)
	ev, ok := Events[name]
	if !ok || ev.Admin && !isAdmin {
		return nil, false
	}
	v := make(map[string]any, len(ev.Fields)+2)
	v["at"] = model.IntOf(e["log_at"])
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
			v[f.Name] = model.IntOf(val)
		case "bool":
			v[f.Name] = model.BoolOf(val)
		default:
			v[f.Name] = model.StrOf(val)
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
