package model

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Die Felder eines Status-Pushs wie im Prüfstand (tests/backend/harness.mjs,
// BOX_FIELDS): der Stand der Firmware auf main.
var boxFields = Fields{
	{"device_id", "fw-box-1"}, {"fw_build", "test"}, {"core", "2.0.17"}, {"uptime_s", "360000"},
	{"wifi", "up"}, {"rssi", "-61"}, {"ip", "192.168.1.50"}, {"relay", "open"}, {"cooldown", "0"},
	{"waiting_for_release", "0"}, {"alarm_pending", "0"}, {"isr_latched", "0"}, {"key_progress", "0/0"},
	{"last_alarm", "nie"}, {"last_heartbeat", "nie"}, {"heartbeat_ok", "0"}, {"stuck_warned", "0"},
	{"ack_pending", "0"}, {"keys_count", "1"}, {"keys_version", "0"}, {"keys_muted", "0"},
	{"reset_reason", "poweron"}, {"free_heap", "180000"}, {"max_alloc_heap", "90000"}, {"alarm_seq", "0"},
	{"alarm_source", ""}, {"alarm_gaveup", "0"},
}

// Die Box im Betrieb läuft noch mit einem älteren Stand: Diese Felder kennt
// sie nicht (harness.mjs: OLD_FIRMWARE_MISSING).
var oldFirmwareMissing = []string{"core", "alarm_seq", "alarm_source", "max_alloc_heap", "alarm_gaveup"}

type firmware string

const (
	fwNew firmware = "neue Firmware"
	fwOld firmware = "alte Firmware"
)

var (
	both    = []firmware{fwNew, fwOld}
	newOnly = []firmware{fwNew}
)

// pushOf: die Felder eines Pushs dieser Firmware; over setzt Felder (Name,
// Wert, …), neue Namen hinten an. Die alte Firmware sendet die Felder aus
// oldFirmwareMissing nie – auch nicht, wenn over sie nennt.
func pushOf(fw firmware, over ...string) Fields {
	set := map[string]string{}
	var extra []string
	for i := 0; i+1 < len(over); i += 2 {
		if !slices.ContainsFunc(boxFields, func(f Field) bool { return f.Name == over[i] }) && !slices.Contains(extra, over[i]) {
			extra = append(extra, over[i])
		}
		set[over[i]] = over[i+1]
	}
	var out Fields
	for _, f := range boxFields {
		if v, ok := set[f.Name]; ok {
			f.Value = v
		}
		out = append(out, f)
	}
	for _, name := range extra {
		out = append(out, Field{name, set[name]})
	}
	if fw == fwOld {
		out = slices.DeleteFunc(out, func(f Field) bool { return slices.Contains(oldFirmwareMissing, f.Name) })
	}
	return out
}

// seqFor: der Zähler, wie ihn diese Firmware gemeldet hätte (die alte nie).
func seqFor(fw firmware, seq string) string {
	if fw == fwOld {
		return ""
	}
	return seq
}

// Bausteine für den Stand vor dem Push.
func storedBox(r *Runtime, fw firmware, ago int64, over ...string) {
	r.Box = &BoxStatus{SeenAt: t0.Unix() - ago, ViaBridge: "0", RemoteAddr: "192.0.2.1", Fields: pushOf(fw, over...)}
}

func seenAlarm(r *Runtime, device, last, seq string) {
	r.AlarmsSeen[device] = AlarmSeen{LastAlarm: last, AlarmSeq: seq, At: t0.Unix() - 500}
}

// command: der laufende Befehl (typ, Zustand, Ergebnis der Quittung,
// ausgeliefert vor ago Sekunden).
func command(typ, state, result string, ago int64) Commands {
	c := &Command{ID: 1, Type: typ, State: state, CreatedAt: t0.Unix() - ago - 5, CreatedBy: "admin", AckResult: result}
	if state != CommandPending {
		c.DeliveredAt, c.DeliveredTo = t0.Unix()-ago, "fw-box-1"
	}
	if state == CommandAcked {
		c.AckedAt, c.AckedBy = t0.Unix()-ago+2, "fw-box-1"
	}
	return Commands{NextID: 2, Current: c}
}

// Was mit dem Alarm-Stand der Box (alarms_seen[device]) geschieht.
type seenEffect int

const (
	seenKept seenEffect = iota // unverändert
	seenPush                   // last_alarm und alarm_seq dieses Pushs, zur Zeit des Pushs
)

// pushRow ist eine Regel des Status-Pushs (api/status.php) – eine Zeile je
// Satz aus CLAUDE.md, mit der Zeile der PHP-Fassung, die sie nachbaut.
type pushRow struct {
	rule   string
	fws    []firmware
	prev   func(r *Runtime, fw firmware) // Stand vor dem Push; nil = die Box hat sich nie gemeldet
	cmds   Commands
	push   []string // Felder über dem Grundstand (Name, Wert, …)
	bridge bool
	boxID  string

	stale, foreign bool
	alarm, source  string       // erwarteter relay_alarm (info, source); "" = keiner
	restart        *RestartInfo // erwarteter Neustart (Reason, Abnormal); nil = keiner
	seen           seenEffect
	check          func(t *testing.T, before, after *Runtime, f Fields, v PushVerdict)
}

const remoteAddr = "192.0.2.7"

func pushRows() []pushRow {
	rows := []pushRow{
		// --- Relaisalarm im Verlauf ------------------------------------
		{rule: "erster Push einer Box: kein Vergleich möglich – ein alter Alarm wird nicht nachträglich verbucht, der Stand wird gemerkt (status.php 139-147)",
			fws: both, push: []string{"last_alarm", "16.11.2026 10:00:00", "alarm_seq", "3"}, seen: seenPush},
		{rule: "erster Push dieser Box, während eine andere bekannt ist: ebenso kein Alarm (status.php 132-133: Übergang nur bei derselben device_id)",
			fws: both, prev: func(r *Runtime, fw firmware) {
				storedBox(r, fw, 30, "device_id", "bastel-1", "last_alarm", "B1")
				seenAlarm(r, "bastel-1", "B1", seqFor(fw, "1"))
			}, push: []string{"last_alarm", "A1", "alarm_seq", "1"}, seen: seenPush},
		{rule: "ein neuer Wert von last_alarm ist EIN relay_alarm, Quelle relay (status.php 144-145, 154, 157-163)",
			fws: both, prev: func(r *Runtime, fw firmware) { seenAlarm(r, "fw-box-1", "nie", seqFor(fw, "0")) },
			push: []string{"last_alarm", "16.11.2026 10:05:00", "alarm_seq", "1"}, alarm: "16.11.2026 10:05:00", source: "relay", seen: seenPush},
		{rule: "derselbe Wert erneut: kein Eintrag, Stand bleibt (status.php 144-146)",
			fws: both, prev: func(r *Runtime, fw firmware) { seenAlarm(r, "fw-box-1", "A", seqFor(fw, "1")) },
			push: []string{"last_alarm", "A", "alarm_seq", "1"}, seen: seenKept},
		{rule: "gleicher Text, neuer Zähler alarm_seq: ein weiterer Alarm (status.php 141-145; nur die neue Firmware zählt)",
			fws: newOnly, prev: func(r *Runtime, fw firmware) { seenAlarm(r, "fw-box-1", "ohne Uhrzeit", "1") },
			push: []string{"last_alarm", "ohne Uhrzeit", "alarm_seq", "2"}, alarm: "ohne Uhrzeit", source: "relay", seen: seenPush},
		{rule: "der Wechsel zu \"nie\" ist ein Neustart der Box, kein Alarm – gemerkt wird er trotzdem (status.php 152-154)",
			fws: both, prev: func(r *Runtime, fw firmware) { seenAlarm(r, "fw-box-1", "A", seqFor(fw, "1")) },
			push: []string{"last_alarm", "nie", "alarm_seq", "0"}, seen: seenPush},
		{rule: "ein leeres last_alarm ist kein Alarm (status.php 154: $new !== '')",
			fws: both, prev: func(r *Runtime, fw firmware) { seenAlarm(r, "fw-box-1", "A", seqFor(fw, "1")) },
			push: []string{"last_alarm", "", "alarm_seq", "1"}, seen: seenPush},
		{rule: "nach \"nie\" zählt derselbe Alarm-Text wieder als neuer Alarm (status.php 144-145)",
			fws: both, prev: func(r *Runtime, fw firmware) { seenAlarm(r, "fw-box-1", "nie", seqFor(fw, "0")) },
			push: []string{"last_alarm", "A", "alarm_seq", "1"}, alarm: "A", source: "relay", seen: seenPush},
		{rule: "der Stand wird pro Box gemerkt: ein zweites Board dazwischen ändert nichts (status.php 129-131, alarm_seen je device_id)",
			fws: both, prev: func(r *Runtime, fw firmware) {
				storedBox(r, fw, 30, "device_id", "bastel-1", "last_alarm", "nie")
				seenAlarm(r, "fw-box-1", "A1", seqFor(fw, "1"))
				seenAlarm(r, "bastel-1", "nie", seqFor(fw, "0"))
			}, push: []string{"last_alarm", "A1", "alarm_seq", "1"}, seen: seenKept},
		{rule: "ohne gemerkten Stand zählt der vorige Push DERSELBEN Box (Übergang, status.php 132-137)",
			fws: both, prev: func(r *Runtime, fw firmware) { storedBox(r, fw, 30, "last_alarm", "A", "alarm_seq", "1") },
			push: []string{"last_alarm", "B", "alarm_seq", "2"}, alarm: "B", source: "relay", seen: seenPush},
		{rule: "Übergang: derselbe Stand wie im vorigen Push ist kein Alarm (status.php 132-137, 144-145)",
			fws: both, prev: func(r *Runtime, fw firmware) { storedBox(r, fw, 30, "last_alarm", "A", "alarm_seq", "1") },
			push: []string{"last_alarm", "A", "alarm_seq", "1"}, seen: seenKept},

		// --- Quelle: Demo-Alarm über die Box oder Relais? -------------------
		{rule: "meldet die Firmware die Quelle selbst (alarm_source remote), gilt sie (status.php 35-37)",
			fws: newOnly, prev: func(r *Runtime, fw firmware) { seenAlarm(r, "fw-box-1", "nie", "0") },
			push: []string{"last_alarm", "A", "alarm_seq", "1", "alarm_source", "remote"}, alarm: "A", source: "remote", seen: seenPush},
		{rule: "alarm_source relay gilt, auch wenn gerade ein ALARM-Befehl ausgeliefert ist (status.php 35-37)",
			fws: newOnly, prev: func(r *Runtime, fw firmware) { seenAlarm(r, "fw-box-1", "nie", "0") },
			cmds: command("ALARM", CommandDelivered, "", 10),
			push: []string{"last_alarm", "A", "alarm_seq", "1", "alarm_source", "relay"}, alarm: "A", source: "relay", seen: seenPush},
		{rule: "ein unbekannter Wert in alarm_source zählt nicht – dann die Heuristik (status.php 36)",
			fws: newOnly, prev: func(r *Runtime, fw firmware) { seenAlarm(r, "fw-box-1", "nie", "0") },
			cmds: command("ALARM", CommandDelivered, "", 10),
			push: []string{"last_alarm", "A", "alarm_seq", "1", "alarm_source", "demo"}, alarm: "A", source: "remote", seen: seenPush},
		{rule: "ALARM-Befehl als executed quittiert, Auslieferung vor 299 s: remote (status.php 47-49)",
			fws: both, prev: func(r *Runtime, fw firmware) { seenAlarm(r, "fw-box-1", "nie", seqFor(fw, "0")) },
			cmds: command("ALARM", CommandAcked, "executed", 299),
			push: []string{"last_alarm", "A", "alarm_seq", "1"}, alarm: "A", source: "remote", seen: seenPush},
		{rule: "als executed quittiert, Auslieferung vor 300 s: relay (status.php 48: < 300)",
			fws: both, prev: func(r *Runtime, fw firmware) { seenAlarm(r, "fw-box-1", "nie", seqFor(fw, "0")) },
			cmds: command("ALARM", CommandAcked, "executed", 300),
			push: []string{"last_alarm", "A", "alarm_seq", "1"}, alarm: "A", source: "relay", seen: seenPush},
		{rule: "ausgeliefert vor 59 s, Quittung steht aus (die Box pusht VOR der ACK): remote (status.php 51-52)",
			fws: both, prev: func(r *Runtime, fw firmware) { seenAlarm(r, "fw-box-1", "nie", seqFor(fw, "0")) },
			cmds: command("ALARM", CommandDelivered, "", 59),
			push: []string{"last_alarm", "A", "alarm_seq", "1"}, alarm: "A", source: "remote", seen: seenPush},
		{rule: "ausgeliefert vor 60 s, keine Quittung: relay (status.php 52: < 60)",
			fws: both, prev: func(r *Runtime, fw firmware) { seenAlarm(r, "fw-box-1", "nie", seqFor(fw, "0")) },
			cmds: command("ALARM", CommandDelivered, "", 60),
			push: []string{"last_alarm", "A", "alarm_seq", "1"}, alarm: "A", source: "relay", seen: seenPush},
		{rule: "als skipped_busy quittiert: lief kein Demo-Alarm, ein Alarm kurz danach ist echt (status.php 45-49)",
			fws: both, prev: func(r *Runtime, fw firmware) { seenAlarm(r, "fw-box-1", "nie", seqFor(fw, "0")) },
			cmds: command("ALARM", CommandAcked, "skipped_busy", 5),
			push: []string{"last_alarm", "A", "alarm_seq", "1"}, alarm: "A", source: "relay", seen: seenPush},
		{rule: "ein TEST-Befehl macht aus einem Alarm nie einen Demo-Alarm (status.php 40-41)",
			fws: both, prev: func(r *Runtime, fw firmware) { seenAlarm(r, "fw-box-1", "nie", seqFor(fw, "0")) },
			cmds: command("TEST", CommandDelivered, "", 5),
			push: []string{"last_alarm", "A", "alarm_seq", "1"}, alarm: "A", source: "relay", seen: seenPush},
		{rule: "ein noch nicht abgeholter ALARM-Befehl: relay (status.php 52)",
			fws: both, prev: func(r *Runtime, fw firmware) { seenAlarm(r, "fw-box-1", "nie", seqFor(fw, "0")) },
			cmds: command("ALARM", CommandPending, "", 5),
			push: []string{"last_alarm", "A", "alarm_seq", "1"}, alarm: "A", source: "relay", seen: seenPush},

		// --- Veralteter Push -------------------------------------------
		{rule: "veralteter Push (früher eingetroffen als der gespeicherte, kleinere Laufzeit, < 60 s Versatz): nur bestätigen – kein Status-Rücksprung, kein Alarm, kein Neustart (status.php 87-92)",
			fws: both, prev: func(r *Runtime, fw firmware) {
				storedBox(r, fw, -30, "uptime_s", "5000")
				seenAlarm(r, "fw-box-1", "nie", seqFor(fw, "0"))
			}, push: []string{"uptime_s", "4000", "rssi", "-99", "last_alarm", "ALT", "alarm_seq", "9", "reset_reason", "panic"},
			stale: true, seen: seenKept},
		{rule: "60 s Versatz ist nicht mehr veraltet: verarbeitet (status.php 89: $lag < 60)",
			fws: both, prev: func(r *Runtime, fw firmware) { storedBox(r, fw, -60, "uptime_s", "5000") },
			push: []string{"uptime_s", "4000"}, seen: seenKept},
		{rule: "früher eingetroffen, aber längere Laufzeit: nicht veraltet (status.php 89)",
			fws: both, prev: func(r *Runtime, fw firmware) { storedBox(r, fw, -30, "uptime_s", "5000") },
			push: []string{"uptime_s", "6000"}, seen: seenKept},
		{rule: "veraltet nur gegenüber derselben Box (status.php 77, 87)",
			fws: both, prev: func(r *Runtime, fw firmware) { storedBox(r, fw, -30, "device_id", "bastel-1", "uptime_s", "5000") },
			push: []string{"uptime_s", "4000"}, seen: seenPush},
		{rule: "ohne uptime_s im gespeicherten Push kein Vergleich (status.php 87: isset)",
			fws: both, prev: func(r *Runtime, fw firmware) {
				storedBox(r, fw, -30)
				r.Box.Fields = slices.DeleteFunc(r.Box.Fields, func(f Field) bool { return f.Name == "uptime_s" })
			}, push: []string{"uptime_s", "4000"}, seen: seenKept},

		// --- Neustart der Box ------------------------------------------
		{rule: "kleinere Laufzeit, reset_reason panic: Neustart nach Absturz, gehört in den Verlauf (status.php 113-125, 171-178)",
			fws: both, prev: func(r *Runtime, fw firmware) { storedBox(r, fw, 100, "uptime_s", "5000") },
			push: []string{"uptime_s", "20", "reset_reason", "panic"}, restart: &RestartInfo{Reason: "panic", Abnormal: true}, seen: seenKept},
		{rule: "Absturz-Schleife, bei der die Laufzeit nicht sinkt: erkannt am Startzeitpunkt (status.php 101-105, 117-119)",
			fws: both, prev: func(r *Runtime, fw firmware) { storedBox(r, fw, 100, "uptime_s", "12") },
			push: []string{"uptime_s", "13", "reset_reason", "task_wdt"}, restart: &RestartInfo{Reason: "task_wdt", Abnormal: true}, seen: seenKept},
		{rule: "seit Wochen laufende Box nach Uhr-Korrektur des Servers: Laufzeit länger als der Abstand – kein Neustart (status.php 106-110, 118)",
			fws: both, prev: func(r *Runtime, fw firmware) { storedBox(r, fw, 100, "uptime_s", "5000") },
			push: []string{"uptime_s", "5050", "reset_reason", "panic"}, seen: seenKept},
		{rule: "Laufzeit gesunken, aber länger als Abstand + 10 s: kann nicht nach dem vorigen Push gestartet sein (status.php 111-112, 118)",
			fws: both, prev: func(r *Runtime, fw firmware) { storedBox(r, fw, 100, "uptime_s", "5000") },
			push: []string{"uptime_s", "111", "reset_reason", "panic"}, seen: seenKept},
		{rule: "Laufzeit gesunken, genau Abstand + 10 s: Neustart (status.php 118: <=)",
			fws: both, prev: func(r *Runtime, fw firmware) { storedBox(r, fw, 100, "uptime_s", "5000") },
			push: []string{"uptime_s", "110", "reset_reason", "panic"}, restart: &RestartInfo{Reason: "panic", Abnormal: true}, seen: seenKept},
		{rule: "Startzeitpunkt genau 10 s + 0,1 % des Abstands später (1 000 s Abstand: 11 s): noch kein Neustart (status.php 104-105, 119)",
			fws: both, prev: func(r *Runtime, fw firmware) { storedBox(r, fw, 1000, "uptime_s", "20") },
			push: []string{"uptime_s", "1009", "reset_reason", "panic"}, seen: seenKept},
		{rule: "gleiche Laufzeit wie zuvor, Startzeitpunkt nur 5 s später: kein Neustart (status.php 119: < und > 10 s)",
			fws: both, prev: func(r *Runtime, fw firmware) { storedBox(r, fw, 5, "uptime_s", "5") },
			push: []string{"uptime_s", "5", "reset_reason", "panic"}, seen: seenKept},
		{rule: "Startzeitpunkt 12 s später bei 1 000 s Abstand: Neustart (status.php 119)",
			fws: both, prev: func(r *Runtime, fw firmware) { storedBox(r, fw, 1000, "uptime_s", "20") },
			push: []string{"uptime_s", "1008", "reset_reason", "panic"}, restart: &RestartInfo{Reason: "panic", Abnormal: true}, seen: seenKept},
		{rule: "ein anderes Board ist kein Neustart der Box (status.php 77, 113)",
			fws: both, prev: func(r *Runtime, fw firmware) { storedBox(r, fw, 100, "uptime_s", "5000") },
			push: []string{"device_id", "bastel-1", "uptime_s", "20", "reset_reason", "panic"}, seen: seenPush},

		// --- Welche Box zählt (box_device_id) ------------------------------
		{rule: "box_device_id gesetzt, ein anderes Board pusht: angenommen, aber nur als Board gemerkt – weder Status noch Alarm-Stand noch Verlauf (status.php 17-27)",
			fws: both, boxID: "fw-box-1", prev: func(r *Runtime, fw firmware) {
				storedBox(r, fw, 100, "uptime_s", "5000")
				seenAlarm(r, "fw-box-1", "nie", seqFor(fw, "0"))
			}, push: []string{"device_id", "bastel-1", "last_alarm", "FREMD", "alarm_seq", "1", "uptime_s", "20", "reset_reason", "panic"},
			foreign: true, seen: seenKept},
		{rule: "box_device_id gesetzt, die eingestellte Box pusht: alles wie sonst (status.php 22, common.php 985-989)",
			fws: both, boxID: "fw-box-1", prev: func(r *Runtime, fw firmware) { seenAlarm(r, "fw-box-1", "nie", seqFor(fw, "0")) },
			push: []string{"last_alarm", "A", "alarm_seq", "1"}, alarm: "A", source: "relay", seen: seenPush},
		{rule: "box_device_id und gepushte device_id zählen ohne Leerraum außen (common.php 982, 988)",
			fws: both, boxID: " fw-box-1\t", prev: func(r *Runtime, fw firmware) { seenAlarm(r, " fw-box-1", "nie", seqFor(fw, "0")) },
			push: []string{"device_id", " fw-box-1", "last_alarm", "A", "alarm_seq", "1"}, alarm: "A", source: "relay", seen: seenPush},

		// --- Was gespeichert wird --------------------------------------
		{rule: "über die Brücke gekommen: via_bridge 1, die Absender-Adresse dabei (status.php 9-14)",
			fws: both, bridge: true, seen: seenPush},
		{rule: "device_id und fw_build der Boards auf 80 Bytes, nie mitten im Zeichen; im Push und im Verlauf die ganze device_id (status.php 15, 76, 160; common.php 998-1000)",
			fws: both, prev: func(r *Runtime, fw firmware) { seenAlarm(r, "x"+strings.Repeat("ä", 39), "nie", seqFor(fw, "0")) },
			push: []string{"device_id", "x" + strings.Repeat("ä", 50), "fw_build", strings.Repeat("b", 90), "last_alarm", "A", "alarm_seq", "1"},
			seen: seenPush, alarm: "A", source: "relay",
			check: func(t *testing.T, _, after *Runtime, f Fields, v PushVerdict) {
				id := "x" + strings.Repeat("ä", 39) // 79 Bytes – das 40. ä passte nicht mehr ganz
				if d, ok := after.Devices[id]; !ok || d.FwBuild != strings.Repeat("b", 80) {
					t.Errorf("Boards: %v", slices.Collect(maps.Keys(after.Devices)))
				}
				if _, ok := after.AlarmsSeen[id]; !ok {
					t.Errorf("Alarm-Stände: %v", slices.Collect(maps.Keys(after.AlarmsSeen)))
				}
				if after.Box.Fields.Get("device_id") != f.Get("device_id") || (*v.RelayAlarm)["device_id"] != f.Get("device_id") {
					t.Errorf("device_id im Push %q, im Verlauf %v", after.Box.Fields.Get("device_id"), (*v.RelayAlarm)["device_id"])
				}
			}},
		{rule: "höchstens 10 Boards, die zuletzt gesehenen (common.php 1002-1003)",
			fws: both, prev: func(r *Runtime, fw firmware) {
				for i := range 10 {
					r.Devices[fmt.Sprintf("b%02d", i)] = Device{SeenAt: t0.Unix() - 100 - int64(i), FwBuild: "x"}
				}
			}, seen: seenPush,
			check: func(t *testing.T, _, after *Runtime, _ Fields, _ PushVerdict) {
				if _, ok := after.Devices["b09"]; ok || len(after.Devices) != 10 {
					t.Errorf("Boards: %v", slices.Sorted(maps.Keys(after.Devices)))
				}
				if _, ok := after.Devices["fw-box-1"]; !ok {
					t.Error("das pushende Board fehlt")
				}
			}},
		{rule: "höchstens 10 gemerkte Alarm-Stände, die zuletzt geänderten (status.php 148-150)",
			fws: both, prev: func(r *Runtime, fw firmware) {
				for i := range 10 {
					r.AlarmsSeen[fmt.Sprintf("b%02d", i)] = AlarmSeen{LastAlarm: "nie", At: t0.Unix() - 100 - int64(i)}
				}
			}, seen: seenPush,
			check: func(t *testing.T, _, after *Runtime, _ Fields, _ PushVerdict) {
				if _, ok := after.AlarmsSeen["b09"]; ok || len(after.AlarmsSeen) != 10 {
					t.Errorf("Alarm-Stände: %v", slices.Sorted(maps.Keys(after.AlarmsSeen)))
				}
			}},
		{rule: "Gleichstand bei der Obergrenze: das gerade pushende Board bleibt (PHP: Reihenfolge der Datei – hier fest)",
			fws: both, prev: func(r *Runtime, fw firmware) {
				for i := range 10 {
					r.Devices[fmt.Sprintf("b%02d", i)] = Device{SeenAt: t0.Unix()}
					r.AlarmsSeen[fmt.Sprintf("b%02d", i)] = AlarmSeen{LastAlarm: "nie", At: t0.Unix()}
				}
			}, seen: seenPush,
			check: func(t *testing.T, _, after *Runtime, _ Fields, _ PushVerdict) {
				_, dev := after.Devices["fw-box-1"]
				_, seen := after.AlarmsSeen["fw-box-1"]
				if !dev || !seen || len(after.Devices) != 10 || len(after.AlarmsSeen) != 10 {
					t.Errorf("Boards %v, Alarm-Stände %v", slices.Sorted(maps.Keys(after.Devices)), slices.Sorted(maps.Keys(after.AlarmsSeen)))
				}
				if _, ok := after.Devices["b09"]; ok {
					t.Error("bei Gleichstand fällt das größte Kennzeichen")
				}
			}},
	}
	// Welche Gründe eines Neustarts in den Verlauf gehören (status.php 123).
	for reason, abnormal := range map[string]bool{"task_wdt": true, "int_wdt": true, "wdt": true, "brownout": true,
		"poweron": false, "software": false, "external": false, "": false, "PANIC": false} {
		rows = append(rows, pushRow{
			rule: fmt.Sprintf("Neustart mit reset_reason %q: im Verlauf %v (status.php 120-124)", reason, abnormal),
			fws:  both, prev: func(r *Runtime, fw firmware) { storedBox(r, fw, 100, "uptime_s", "5000") },
			push: []string{"uptime_s", "20", "reset_reason", reason}, restart: &RestartInfo{Reason: reason, Abnormal: abnormal}, seen: seenKept,
		})
	}
	return rows
}

// TestEvaluatePush prüft jede Regel des Status-Pushs für die alte und die
// neue Firmware, wo die Firmware zählt. Für jede Zeile außerdem: Die
// Auswertung ändert den vorigen Stand nicht (sie rechnet nur), die Änderung
// schreibt nichts in den Verlauf (das tut der Aufrufer mit dem Urteil), und
// derselbe Push noch einmal auf dem Ergebnis meldet nichts mehr – so können
// zwei gleichzeitige Pushs denselben Alarm nie zweimal melden.
func TestEvaluatePush(t *testing.T) {
	for _, row := range pushRows() {
		for _, fw := range row.fws {
			t.Run(string(fw)+"/"+row.rule, func(t *testing.T) {
				prev := NewRuntime()
				if row.prev != nil {
					row.prev(prev, fw)
				}
				prev.Log(NewEntry(EvKeyTest, t0, map[string]any{"label": "vorher"}))
				before := prev.Clone()
				f := pushOf(fw, row.push...)
				next, v := EvaluatePush(prev, row.cmds, f, row.bridge, remoteAddr, t0, row.boxID)
				if !reflect.DeepEqual(prev, before) {
					t.Fatal("EvaluatePush hat den vorigen Stand geändert")
				}
				after := prev.Clone()
				next(after)
				checkVerdict(t, row, before, after, f, v)
				if row.check != nil {
					row.check(t, before, after, f, v)
				}
				if !reflect.DeepEqual(after.Outbox, before.Outbox) {
					t.Error("die Änderung schreibt in den Verlauf")
				}
				if len(after.Devices) > 10 || len(after.AlarmsSeen) > 10 {
					t.Errorf("%d Boards, %d Alarm-Stände", len(after.Devices), len(after.AlarmsSeen))
				}
				if _, again := EvaluatePush(after, row.cmds, f, row.bridge, remoteAddr, t0, row.boxID); again.RelayAlarm != nil || again.Restart != nil {
					t.Errorf("derselbe Push noch einmal: %+v", again)
				}
			})
		}
	}
}

func checkVerdict(t *testing.T, row pushRow, before, after *Runtime, f Fields, v PushVerdict) {
	t.Helper()
	if v.Stale != row.stale || v.Foreign != row.foreign {
		t.Errorf("veraltet %v, fremd %v – erwartet %v, %v", v.Stale, v.Foreign, row.stale, row.foreign)
	}
	device := ClipBytes(f.Get("device_id"), 80)
	switch {
	case row.stale:
		if !reflect.DeepEqual(after, before) {
			t.Error("ein veralteter Push hat etwas geändert")
		}
	case row.foreign:
		if !reflect.DeepEqual(after.Box, before.Box) || !reflect.DeepEqual(after.AlarmsSeen, before.AlarmsSeen) {
			t.Error("ein fremdes Board hat Status oder Alarm-Stand geändert")
		}
	default:
		bridge := "0"
		if row.bridge {
			bridge = "1"
		}
		want := &BoxStatus{SeenAt: t0.Unix(), ViaBridge: bridge, RemoteAddr: remoteAddr, Fields: f}
		if !reflect.DeepEqual(after.Box, want) {
			t.Errorf("gespeichert %+v\nerwartet %+v", after.Box, want)
		}
	}
	if !row.stale {
		if d, ok := after.Devices[device]; !ok || d.SeenAt != t0.Unix() || d.FwBuild != ClipBytes(f.Get("fw_build"), 80) {
			t.Errorf("Board %q: %+v (gemerkt: %v)", device, d, ok)
		}
	}
	switch row.seen {
	case seenKept:
		if !reflect.DeepEqual(after.AlarmsSeen, before.AlarmsSeen) {
			t.Errorf("Alarm-Stand geändert: %+v", after.AlarmsSeen[device])
		}
	case seenPush:
		want := AlarmSeen{LastAlarm: f.Get("last_alarm"), AlarmSeq: f.Get("alarm_seq"), At: t0.Unix()}
		if got := after.AlarmsSeen[device]; got != want {
			t.Errorf("Alarm-Stand %+v, erwartet %+v", got, want)
		}
	}
	if row.alarm == "" {
		if v.RelayAlarm != nil {
			t.Errorf("unerwarteter Relaisalarm %v", *v.RelayAlarm)
		}
	} else {
		e := v.RelayAlarm
		switch {
		case e == nil:
			t.Errorf("kein Relaisalarm, erwartet %q", row.alarm)
		case e.Str("event") != EvRelayAlarm || e.Str("info") != row.alarm || e.Str("source") != row.source ||
			e.Str("device_id") != f.Get("device_id") || e.Int("log_at") != t0.Unix() || e.Str("uid") == "":
			t.Errorf("Relaisalarm %v, erwartet info %q, source %q", *e, row.alarm, row.source)
		}
	}
	if row.restart == nil {
		if v.Restart != nil {
			t.Errorf("unerwarteter Neustart %+v", *v.Restart)
		}
		return
	}
	r := v.Restart
	if r == nil || r.Reason != row.restart.Reason || r.Abnormal != row.restart.Abnormal {
		t.Errorf("Neustart %+v, erwartet %+v", r, *row.restart)
		return
	}
	switch e := r.Entry; {
	case !r.Abnormal && e != nil:
		t.Errorf("ein normaler Neustart gehört nicht in den Verlauf: %v", *e)
	case r.Abnormal && (e == nil || e.Str("event") != EvBoxRestart || e.Str("reason") != r.Reason ||
		e.Str("device_id") != f.Get("device_id") || e.Int("log_at") != t0.Unix() || e.Str("uid") == ""):
		t.Errorf("Eintrag box_restart: %v", e)
	}
}

// FuzzEvaluatePush: beliebige Werte der Box (Laufzeit, Alarm-Stand, Grund,
// Quelle, Kennzeichen) gegen einen gespeicherten Push derselben Box. Nie ein
// Absturz; der vorige Stand bleibt unberührt; höchstens 10 Boards und
// Alarm-Stände; ein veralteter Push ändert nichts; ein Relaisalarm nur mit
// geändertem Stand und nie für "nie" oder leer; Quelle nur relay|remote;
// derselbe Push noch einmal meldet nichts mehr.
func FuzzEvaluatePush(f *testing.F) {
	f.Add("fw-box-1", "5000", "20", int64(100), "nie", "A", "0", "1", "panic", "", "")
	f.Add("fw-box-1", "5000", "4000", int64(-30), "nie", "ALT", "0", "9", "panic", "", "")
	f.Add("x", "12", "13", int64(100), "A", "A", "1", "2", "task_wdt", "remote", "x")
	f.Add("", "", "", int64(0), "", "", "", "", "", "relay", "y")
	f.Add("\xff", "9223372036854775807", "-9223372036854775808", int64(1<<62), "nie", "1e999", "a", "b", "", "Remote", " \xff ")
	f.Fuzz(func(t *testing.T, device, oldUp, newUp string, ago int64, oldAlarm, newAlarm, oldSeq, newSeq, reason, source, boxID string) {
		prev := NewRuntime()
		prev.Box = &BoxStatus{SeenAt: t0.Unix() - ago, ViaBridge: "0",
			Fields: Fields{{"device_id", device}, {"uptime_s", oldUp}, {"last_alarm", oldAlarm}, {"alarm_seq", oldSeq}}}
		for i := range 10 { // an der Obergrenze
			prev.Devices[fmt.Sprint(i)] = Device{SeenAt: t0.Unix() - int64(i)}
		}
		before := prev.Clone()
		push := Fields{{"device_id", device}, {"uptime_s", newUp}, {"last_alarm", newAlarm}, {"alarm_seq", newSeq},
			{"reset_reason", reason}, {"alarm_source", source}}
		cmds := command("ALARM", CommandDelivered, "", 10)
		next, v := EvaluatePush(prev, cmds, push, true, "", t0, boxID)
		if !reflect.DeepEqual(prev, before) {
			t.Fatal("voriger Stand geändert")
		}
		after := prev.Clone()
		next(after)
		if len(after.Devices) > 10 || len(after.AlarmsSeen) > 10 {
			t.Fatalf("%d Boards, %d Alarm-Stände", len(after.Devices), len(after.AlarmsSeen))
		}
		if v.Stale && !reflect.DeepEqual(after, before) {
			t.Fatal("veralteter Push hat etwas geändert")
		}
		if e := v.RelayAlarm; e != nil {
			if v.Stale || v.Foreign || newAlarm == "" || newAlarm == "nie" || (newAlarm == oldAlarm && newSeq == oldSeq) {
				t.Fatalf("Relaisalarm %v", *e)
			}
			if s := e.Str("source"); s != "relay" && s != "remote" {
				t.Fatalf("Quelle %q", s)
			}
		}
		if r := v.Restart; r != nil && (r.Abnormal != (r.Entry != nil) || v.Stale || v.Foreign) {
			t.Fatalf("Neustart %+v", *r)
		}
		if _, again := EvaluatePush(after, cmds, push, true, "", t0, boxID); again.RelayAlarm != nil || again.Restart != nil {
			t.Fatalf("noch einmal: %+v", again)
		}
	})
}

// Eine Auswertung auf einem leeren Runtime{} (Tabellen nil) scheitert nicht.
func TestEvaluatePushOnZeroRuntime(t *testing.T) {
	for _, prev := range []*Runtime{nil, {}} {
		next, v := EvaluatePush(prev, Commands{}, pushOf(fwNew), false, "", t0, "")
		d := &Runtime{}
		next(d)
		if d.Box == nil || len(d.Devices) != 1 || len(d.AlarmsSeen) != 1 || v.Stale || v.RelayAlarm != nil {
			t.Errorf("%+v %+v", d, v)
		}
	}
}

// LastAlarmSource: die Quelle eines Alarms (PHP: last_alarm_source) – die
// Fenster sind halboffen wie in PHP (< 300 s, < 60 s).
func TestLastAlarmSource(t *testing.T) {
	cases := []struct {
		name string
		cmds Commands
		sent string
		want string
	}{
		{"kein Befehl", Commands{}, "", "relay"},
		{"Firmware sagt remote", Commands{}, "remote", "remote"},
		{"Firmware sagt relay", command("ALARM", CommandDelivered, "", 1), "relay", "relay"},
		{"unbekannter Wert", command("ALARM", CommandDelivered, "", 1), "Remote", "remote"},
		{"ausgeliefert vor 0 s", command("ALARM", CommandDelivered, "", 0), "", "remote"},
		{"ausgeliefert vor 59 s", command("ALARM", CommandDelivered, "", 59), "", "remote"},
		{"ausgeliefert vor 60 s", command("ALARM", CommandDelivered, "", 60), "", "relay"},
		{"executed vor 299 s", command("ALARM", CommandAcked, "executed", 299), "", "remote"},
		{"executed vor 300 s", command("ALARM", CommandAcked, "executed", 300), "", "relay"},
		{"skipped_busy", command("ALARM", CommandAcked, "skipped_busy", 1), "", "relay"},
		{"TEST", command("TEST", CommandDelivered, "", 1), "", "relay"},
		{"wartend", command("ALARM", CommandPending, "", 1), "", "relay"},
		{"abgebrochen", command("ALARM", CommandCancelled, "", 1), "", "relay"},
		{"verfallen", command("ALARM", CommandExpired, "", 1), "", "relay"},
	}
	for _, tc := range cases {
		if got := LastAlarmSource(tc.cmds, tc.sent, t0); got != tc.want {
			t.Errorf("%s: %q, erwartet %q", tc.name, got, tc.want)
		}
	}
}

// DeviceHint: die Hinweise von PHP device_hint() (common.php 1041-1071)
// wörtlich, config.php heißt hier config.toml. Die Boards erscheinen nach
// ihrem letzten Push geordnet, das jüngste zuerst.
func TestDeviceHintTexts(t *testing.T) {
	boards := func(ages map[string]int64) *Runtime {
		r := NewRuntime()
		for id, age := range ages {
			r.Devices[id] = Device{SeenAt: t0.Unix() - age, FwBuild: "x"}
		}
		return r
	}
	const several = "Mehrere Boards melden sich mit demselben Maschinen-Token (%s). Anzeige und Offline-Wächter sehen nur den jeweils letzten Push – fällt eins aus, merkt das niemand. In config.toml box_device_id auf die echte Box setzen."
	const silent = "ACHTUNG: %s meldet sich seit %s nicht mehr, nur noch %s (gleiches Maschinen-Token). Ist das die echte Box, ist sie AUSGEFALLEN – Anzeige und Offline-Wächter sehen nur das andere Board und melden nichts. In config.toml box_device_id auf die echte Box setzen."
	const foreign = "Ein fremdes Board meldet sich mit dem Maschinen-Token der Box (%s, zuletzt vor %s) – seine Pushs werden ignoriert."
	const never = "Die eingestellte Box „%s“ (box_device_id) hat sich noch nie gemeldet, wohl aber %s – stimmt box_device_id in config.toml mit REMOTE_DEVICE_ID in config.h der Box überein?"
	cases := []struct {
		name         string
		rt           *Runtime
		boxID        string
		offlineAfter int
		want         string
	}{
		{"nie ein Push", NewRuntime(), "", 180, ""},
		{"ein Board", boards(map[string]int64{"fw-box-1": 5}), "", 180, ""},
		{"zwei Boards, beide aktiv", boards(map[string]int64{"fw-box-1": 5, "bastel-1": 2}), "", 180,
			fmt.Sprintf(several, "„bastel-1“, „fw-box-1“")},
		{"drei Boards, Reihenfolge nach letztem Push", boards(map[string]int64{"c": 30, "a": 10, "b": 20}), "", 180,
			fmt.Sprintf(several, "„a“, „b“, „c“")},
		{"eins schweigt seit 15 min", boards(map[string]int64{"fw-box-1": 900, "bastel-1": 2}), "", 180,
			fmt.Sprintf(silent, "„fw-box-1“", "15 Min", "„bastel-1“")},
		{"Schwelle: offline_after_seconds, wenn größer als 180", boards(map[string]int64{"fw-box-1": 400, "bastel-1": 2}), "", 600,
			fmt.Sprintf(several, "„bastel-1“, „fw-box-1“")},
		{"Schwelle: mindestens 180 s", boards(map[string]int64{"fw-box-1": 181, "bastel-1": 2}), "", 60,
			fmt.Sprintf(silent, "„fw-box-1“", "3 Min", "„bastel-1“")},
		{"genau an der Schwelle schweigt es noch nicht", boards(map[string]int64{"fw-box-1": 180, "bastel-1": 2}), "", 180,
			fmt.Sprintf(several, "„bastel-1“, „fw-box-1“")},
		{"alle schweigen: kein ACHTUNG", boards(map[string]int64{"fw-box-1": 900, "bastel-1": 800}), "", 180,
			fmt.Sprintf(several, "„bastel-1“, „fw-box-1“")},
		{"älter als 7 Tage zählt nicht", boards(map[string]int64{"fw-box-1": 5, "bastel-1": 7*86400 + 1}), "", 180, ""},
		{"genau 7 Tage zählt noch", boards(map[string]int64{"fw-box-1": 5, "bastel-1": 7 * 86400}), "", 180,
			fmt.Sprintf(silent, "„bastel-1“", "7 Tagen", "„fw-box-1“")},
		{"box_device_id: fremdes Board vor 2 Std", boards(map[string]int64{"fw-box-1": 5, "bastel-1": 7200}), "fw-box-1", 180,
			fmt.Sprintf(foreign, "„bastel-1“", "2 Std")},
		{"box_device_id: zwei fremde, zuletzt vor dem jüngsten", boards(map[string]int64{"fw-box-1": 5, "x": 600, "y": 300}), "fw-box-1", 180,
			fmt.Sprintf(foreign, "„y“, „x“", "5 Min")},
		{"box_device_id: eingestellte Box nie gesehen", boards(map[string]int64{"bastel-1": 60}), "fw-box-1", 180,
			fmt.Sprintf(never, "fw-box-1", "„bastel-1“")},
		{"box_device_id mit Leerraum", boards(map[string]int64{"bastel-1": 60}), " fw-box-1 ", 180,
			fmt.Sprintf(never, "fw-box-1", "„bastel-1“")},
		{"box_device_id: fremdes Board älter als 24 Std", boards(map[string]int64{"fw-box-1": 5, "bastel-1": 86401}), "fw-box-1", 180, ""},
		{"box_device_id: fremdes Board genau 24 Std", boards(map[string]int64{"fw-box-1": 5, "bastel-1": 86400}), "fw-box-1", 180,
			fmt.Sprintf(foreign, "„bastel-1“", "24 Std")},
		{"box_device_id: nur die eingestellte Box", boards(map[string]int64{"fw-box-1": 5}), "fw-box-1", 180, ""},
		{"box_device_id: die eingestellte Box ist alt, kein Fremder", boards(map[string]int64{"fw-box-1": 30 * 86400}), "fw-box-1", 180, ""},
		{"box_device_id: nichts gesehen", NewRuntime(), "fw-box-1", 180, ""},
	}
	for _, tc := range cases {
		if got := DeviceHintAfter(tc.rt, tc.boxID, t0, tc.offlineAfter); got != tc.want {
			t.Errorf("%s:\n %q\nerwartet\n %q", tc.name, got, tc.want)
		}
	}
	// DeviceHint ist dieselbe Regel mit der Schwelle von 180 s.
	r := boards(map[string]int64{"fw-box-1": 181, "bastel-1": 2})
	if got, want := DeviceHint(r, "", t0), DeviceHintAfter(r, "", t0, 180); got != want || !strings.HasPrefix(got, "ACHTUNG") {
		t.Errorf("DeviceHint %q", got)
	}
}

// Die Dauer im Hinweis (PHP: $duration in device_hint): Minuten gerundet,
// mindestens 1; ab 120 Min Stunden, ab 2 880 Min Tage.
func TestHintDuration(t *testing.T) {
	for seconds, want := range map[int64]string{
		-50: "1 Min", 0: "1 Min", 29: "1 Min", 89: "1 Min", 90: "2 Min", 900: "15 Min",
		119*60 + 29: "119 Min", 119*60 + 30: "2 Std", 150 * 60: "3 Std", 2879 * 60: "48 Std",
		2880 * 60: "2 Tagen", 7 * 86400: "7 Tagen", 3*86400 + 12*3600: "4 Tagen",
	} {
		if got := hintDuration(seconds); got != want {
			t.Errorf("%d s: %q, erwartet %q", seconds, got, want)
		}
	}
}
