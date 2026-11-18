package model

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"time"
)

// Der Status-Push der Box (PHP: api/status.php): Was er an den
// flüchtigen Daten ändert und was aus ihm folgt – ein Relaisalarm im
// Verlauf, ein Neustart der Box, ein fremdes Board. Hier wird nur
// gerechnet; der Handler (internal/box) wendet das Ergebnis an.

const (
	// boardIDBytes: so viele Bytes der device_id zählen als Kennzeichen
	// eines Boards (PHP: substr($status['device_id'], 0, 80)) – ebenso viele
	// von fw_build in der Liste der Boards.
	boardIDBytes = 80
	// maxBoards: so viele Boards und so viele Alarm-Stände werden gemerkt,
	// die zuletzt gesehenen (PHP: array_slice(…, 0, 10)).
	maxBoards = 10
	// staleWindow: Ein Push, der höchstens so viele Sekunden VOR dem
	// gespeicherten eintraf, kann veraltet sein (PHP: $lag < 60).
	staleWindow = 60
	// Fenster für einen Demo-Alarm über die Box (PHP: last_alarm_source):
	// quittiert als ausgeführt – bis 300 s nach der Auslieferung; ohne
	// Quittung – bis 60 s danach. Setzt COOLDOWN_MS ≥ 300 s voraus.
	remoteAckedWindow     = 300
	remoteDeliveredWindow = 60
)

// RestartInfo ist ein erkannter Neustart der Box.
type RestartInfo struct {
	Reason string // reset_reason aus dem Push ("" = nicht gemeldet)
	// Abnormal: Absturz, Watchdog oder Spannungseinbruch – gehört dauerhaft
	// in den Verlauf.
	Abnormal bool
	Entry    *Entry // der Eintrag box_restart, nur bei Abnormal; sonst nil
}

// PushVerdict ist das Urteil über einen Status-Push.
type PushVerdict struct {
	Stale      bool         // veralteter Push: nur bestätigen
	RelayAlarm *Entry       // relay_alarm mit source relay|remote, sonst nil
	Restart    *RestartInfo // Neustart erkannt (für box_restart), sonst nil
	Foreign    bool         // anderes Board als box_device_id
}

// abnormalResets: Gründe eines Neustarts, die auf ein Problem deuten (PHP:
// api/status.php, in_array(…, true) – genau diese Schreibweise).
var abnormalResets = []string{"panic", "task_wdt", "int_wdt", "wdt", "brownout"}

// EvaluatePush wertet einen Status-Push aus (PHP: api/status.php). prev ist
// der Stand vor dem Push (nil wie ein leerer), cmds der Befehlsstand (für
// die Quelle eines Alarms), f die schon gekürzten Felder des Pushs ohne das
// Token, viaBridge: kam über die Brücke (Kopfzeile X-FW-Bridge), remote: die
// Adresse des Absenders, boxDeviceID: box_device_id aus der Konfiguration.
// prev wird nur gelesen. next wendet die Änderung auf einen Entwurf an; v
// sagt, was daraus folgt – die Verlaufseinträge legt der Aufrufer in
// dieselbe Änderung (d.Log). Die Regeln, in dieser Reihenfolge:
//
//  1. box_device_id gesetzt und ein ANDERES Board (beide ohne Leerraum
//     außen): angenommen, aber nur in der Liste der Boards gemerkt – es
//     ändert weder den Status der Box noch ihren Alarm-Stand, und seine
//     Alarme gehören nicht in den Verlauf (Foreign).
//  2. Veralteter Push derselben Box (früher eingetroffen als der
//     gespeicherte, weniger als 60 s Versatz, kleinere Laufzeit): nur
//     bestätigen, nichts ändern (Stale) – sonst spränge der Status zurück,
//     ein alter Alarm käme erneut in den Verlauf und ein Neustart würde
//     gemeldet.
//  3. Sonst ist der Push der neue Status der Box; das Board wird gemerkt
//     (höchstens 10, die zuletzt gesehenen).
//  4. Neustart derselben Box: die Laufzeit sank, oder der errechnete
//     Startzeitpunkt sprang um mehr als 10 s + 0,1 % des Abstands nach
//     hinten – beides nur, wenn die Laufzeit nicht länger ist als der
//     Abstand zum vorigen Push (+10 s): Eine seit Wochen laufende Box ist
//     nach einer Korrektur der Server-Uhr nicht neu gestartet.
//  5. Relaisalarm: last_alarm oder alarm_seq anders als zuletzt für DIESES
//     Board gemerkt (je device_id, höchstens 10). Ohne gemerkten Stand
//     zählt der vorige Push, wenn er von derselben Box kam; sonst kein
//     Vergleich (ein alter Alarm würde sonst nachträglich verbucht). Ein
//     Wechsel zu "nie" (oder leer) ist ein Neustart, kein Alarm – gemerkt
//     wird er trotzdem. Quelle: LastAlarmSource.
func EvaluatePush(prev *Runtime, cmds Commands, f Fields, viaBridge bool, remote string, now time.Time, boxDeviceID string) (next func(d *Runtime), v PushVerdict) {
	if prev == nil {
		prev = &Runtime{}
	}
	at := now.Unix()
	deviceID := f.Get("device_id")
	board := ClipBytes(deviceID, boardIDBytes)
	seen := Device{SeenAt: at, FwBuild: ClipBytes(f.Get("fw_build"), boardIDBytes)}

	// 1. Welche Box zählt (status.php 17-27; common.php 980-989).
	if expected := phpTrim(boxDeviceID); expected != "" && expected != phpTrim(board) {
		v.Foreign = true
		return func(d *Runtime) { rememberBoard(d, board, seen) }, v
	}

	old := prev.Box
	same := old != nil && old.Fields.Get("device_id") == deviceID // status.php 77
	newUp, hasUp := f.find("uptime_s")
	var oldUp string
	compare := false // status.php 87, 113: isset(uptime_s) in beiden
	if same && hasUp {
		oldUp, compare = old.Fields.find("uptime_s")
	}

	// 2. Veralteter Push (status.php 79-92).
	if compare {
		lag := old.SeenAt - at
		if lag > 0 && lag < staleWindow && phpInt(newUp) < phpInt(oldUp) {
			v.Stale = true
			return func(*Runtime) {}, v
		}
	}

	// 3. Der neue Status (status.php 8-14, 94-97).
	bridge := "0"
	if viaBridge {
		bridge = "1"
	}
	box := BoxStatus{SeenAt: at, ViaBridge: bridge, RemoteAddr: remote, Fields: slices.Clone(f)}

	// 4. Neustart (status.php 98-126).
	if compare {
		if r := restartOf(old, oldUp, newUp, at, f.Get("reset_reason")); r != nil {
			if r.Abnormal {
				e := NewEntry(EvBoxRestart, now, map[string]any{"device_id": deviceID, "reason": r.Reason})
				r.Entry = &e
			}
			v.Restart = r
		}
	}

	// 5. Relaisalarm (status.php 127-164).
	newAlarm, seq := f.Get("last_alarm"), f.Get("alarm_seq") // alarm_seq nur neuere Firmware, sonst ""
	known, ok := prev.AlarmsSeen[board]
	if !ok && old != nil && old.Fields.Get("device_id") == board {
		// Übergang von der alten Logik: Vergleich mit dem vorigen Push.
		if last, has := old.Fields.find("last_alarm"); has {
			known, ok = AlarmSeen{LastAlarm: last, AlarmSeq: old.Fields.Get("alarm_seq")}, true
		}
	}
	// Text ODER Zähler: der Zähler erkennt zwei textgleiche Alarme (ohne
	// NTP-Zeit), der Text einen Alarm nach einem Neustart, bei dem der
	// Zähler zufällig wieder denselben Wert hat.
	changed := ok && (newAlarm != known.LastAlarm || seq != known.AlarmSeq)
	remember := !ok || changed
	if changed && newAlarm != "" && newAlarm != "nie" {
		e := NewEntry(EvRelayAlarm, now, map[string]any{
			"device_id": deviceID, "info": newAlarm, "source": LastAlarmSource(cmds, f.Get("alarm_source"), now),
		})
		v.RelayAlarm = &e
	}
	mark := AlarmSeen{LastAlarm: newAlarm, AlarmSeq: seq, At: at}

	return func(d *Runtime) {
		b := box
		b.Fields = slices.Clone(box.Fields)
		d.Box = &b
		rememberBoard(d, board, seen)
		if remember {
			if d.AlarmsSeen == nil {
				d.AlarmsSeen = map[string]AlarmSeen{}
			}
			d.AlarmsSeen[board] = mark
			keepNewest(d.AlarmsSeen, board, func(s AlarmSeen) int64 { return s.At })
		}
	}, v
}

// restartOf: Hat dieselbe Box seit ihrem vorigen Push neu gestartet
// (status.php 113-126)? nil = nein. Der Startzeitpunkt hängt an der Uhr des
// Servers; gerechnet wird er in Kommazahlen wie PHP, wenn die Ganzzahlen
// überliefen.
func restartOf(old *BoxStatus, oldUp, newUp string, at int64, reason string) *RestartInfo {
	prevUptime, newUptime := phpInt(oldUp), phpInt(newUp)
	gap := max(0, at-old.SeenAt)
	bootShift := (float64(at) - float64(newUptime)) - (float64(old.SeenAt) - float64(prevUptime))
	couldHaveBooted := newUptime <= gap+10
	if !couldHaveBooted || (newUptime >= prevUptime && bootShift <= 10+0.001*float64(gap)) {
		return nil
	}
	return &RestartInfo{Reason: reason, Abnormal: slices.Contains(abnormalResets, reason)}
}

// rememberBoard merkt sich den Push eines Boards (PHP:
// remember_device_push) – höchstens 10, die zuletzt gesehenen.
func rememberBoard(d *Runtime, board string, seen Device) {
	if d.Devices == nil {
		d.Devices = map[string]Device{}
	}
	d.Devices[board] = seen
	keepNewest(d.Devices, board, func(x Device) int64 { return x.SeenAt })
}

// keepNewest lässt die maxBoards Einträge mit der jüngsten Zeit stehen. Bei
// gleicher Zeit bleibt current (das gerade pushende Board), danach das
// kleinere Kennzeichen (PHP nimmt dort die Reihenfolge der Datei).
func keepNewest[V any](m map[string]V, current string, at func(V) int64) {
	if len(m) <= maxBoards {
		return
	}
	for _, id := range slices.SortedFunc(maps.Keys(m), newestFirst(m, current, at))[maxBoards:] {
		delete(m, id)
	}
}

// newestFirst ordnet Kennzeichen nach ihrer Zeit, die jüngste zuerst; bei
// gleicher Zeit current vorn, dann aufsteigend nach Kennzeichen.
func newestFirst[V any](m map[string]V, current string, at func(V) int64) func(a, b string) int {
	return func(a, b string) int {
		if c := cmp.Compare(at(m[b]), at(m[a])); c != 0 {
			return c
		}
		switch {
		case a == current:
			return -1
		case b == current:
			return 1
		}
		return strings.Compare(a, b)
	}
}

// LastAlarmSource: Kam der Alarm, den die Box gerade meldet, vom Demo-Alarm
// des Dashboards ("remote") oder vom Relais ("relay")? (PHP:
// last_alarm_source.) Meldet die Firmware die Quelle selbst (sent, das Feld
// alarm_source der neueren Firmware), gilt sie. Sonst "remote" nur, wenn der
// laufende Befehl ein ALARM ist und die Box ihn als ausgeführt quittiert hat,
// weniger als 300 s nach der Auslieferung – oder er weniger als 60 s vor now
// ausgeliefert und noch nicht quittiert ist (die Box pusht ihren Status VOR
// der Quittung). Im Zweifel "relay": Ein echter Alarm darf nie als Demo im
// Verlauf stehen.
func LastAlarmSource(cmds Commands, sent string, now time.Time) string {
	if sent == "relay" || sent == "remote" {
		return sent
	}
	cur := cmds.Current
	if cur == nil || cur.Type != "ALARM" {
		return "relay"
	}
	since := now.Unix() - cur.DeliveredAt
	switch cur.State {
	case CommandAcked:
		// Bei "skipped_busy" o.ä. lief kein Demo-Alarm – ein Alarm kurz
		// danach ist echt.
		if cur.AckResult == "executed" && since < remoteAckedWindow {
			return "remote"
		}
	case CommandDelivered:
		if since < remoteDeliveredWindow {
			return "remote"
		}
	}
	return "relay"
}
