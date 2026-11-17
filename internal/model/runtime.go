package model

import (
	"bytes"
	"encoding/json"
	"reflect"
)

// Runtime sind die flüchtigen Daten (runtime.json): Sie gelten sofort im
// Speicher und werden nebenher gesichert.
type Runtime struct {
	Schema int `json:"schema"`
	// LifebeatAt: letztes Lebenszeichen des Servers selbst.
	LifebeatAt int64 `json:"lifebeat_at"`
	// SilenceFrom: Start nach einer Auszeit; die Stille von Box und
	// Außen-Wächter zählt frühestens ab hier.
	SilenceFrom int64      `json:"silence_from"`
	Box         *BoxStatus `json:"box"` // nil = die Box hat sich nie gemeldet
	// AlarmsSeen und Devices je device_id (PHP: alarm_seen.json,
	// devices_seen.json).
	AlarmsSeen map[string]AlarmSeen `json:"alarms_seen"`
	Devices    map[string]Device    `json:"devices"`
	Watcher    *OutsideWatcher      `json:"watcher"` // nil = nie gesehen
	Watch      Watch                `json:"watch"`
	Throttle   Throttle             `json:"throttle"`
	Outbox     []Entry              `json:"outbox"`
}

// BoxStatus ist der letzte Status-Push der Box (PHP: latest_status.json).
// Fields sind die gepushten Felder in der Reihenfolge des Pushs.
type BoxStatus struct {
	SeenAt     int64  `json:"seen_at"`
	ViaBridge  string `json:"via_bridge"` // "1" = kam über die Brücke auf dem NAS
	RemoteAddr string `json:"remote_addr"`
	Fields     Fields `json:"fields"`
}

// Fields sind geordnete Paare aus Name und Wert; als JSON eine Liste von
// Paaren: [["device_id","fw-box-1"], …].
type Fields []Field

// Field ist ein gepushtes Feld.
type Field struct{ Name, Value string }

// Get liefert den Wert des ersten Felds dieses Namens, "" wenn es fehlt.
func (f Fields) Get(name string) string {
	v, _ := f.find(name)
	return v
}

// find liefert den Wert und ob es das Feld gibt (PHP unterscheidet
// "fehlt" von "leer", z.B. keys_version ?? -1).
func (f Fields) find(name string) (string, bool) {
	for _, x := range f {
		if x.Name == name {
			return x.Value, true
		}
	}
	return "", false
}

// pairsShape ist die JSON-Gestalt von Fields; der strenge Leser prüft
// danach (decode.go).
var pairsShape = reflect.TypeFor[[][2]string]()

// MarshalJSON schreibt die Liste der Paare (nil als []).
func (f Fields) MarshalJSON() ([]byte, error) {
	pairs := make([][2]string, len(f))
	for i, x := range f {
		pairs[i] = [2]string{x.Name, x.Value}
	}
	return json.Marshal(pairs)
}

// UnmarshalJSON liest die Liste der Paare streng: jedes Paar genau zwei
// Texte, kein null.
func (f *Fields) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	w := walker{dec: dec}
	if err := w.value(pairsShape, "fields"); err != nil {
		return err
	}
	var pairs [][2]string
	if err := json.Unmarshal(b, &pairs); err != nil {
		return err
	}
	out := make(Fields, len(pairs))
	for i, p := range pairs {
		out[i] = Field{Name: p[0], Value: p[1]}
	}
	*f = out
	return nil
}

// AlarmSeen ist der zuletzt verbuchte Alarm-Stand einer Box (PHP:
// alarm_seen.json).
type AlarmSeen struct {
	LastAlarm string `json:"last_alarm"`
	AlarmSeq  string `json:"alarm_seq"`
	At        int64  `json:"at"`
}

// Device ist ein Board, das sich mit dem Maschinen-Token gemeldet hat (PHP:
// devices_seen.json).
type Device struct {
	SeenAt  int64  `json:"seen_at"`
	FwBuild string `json:"fw_build"`
}

// OutsideWatcher ist das letzte Lebenszeichen des Außen-Wächters (PHP:
// watcher_seen.json).
type OutsideWatcher struct {
	SeenAt       int64 `json:"seen_at"`
	BarkFailedAt int64 `json:"bark_failed_at"`
}

// Watch sind die Merker des Prüf-Laufs unter den Namen aus PHPs
// watchdog_state.json, dazu die neuen ab LastRunLine.
type Watch struct {
	LastRunAt               int64 `json:"last_run_at"`
	OfflineNotified         bool  `json:"offline_notified"`
	OfflineSince            int64 `json:"offline_since"`
	OfflineSeenAt           int64 `json:"offline_seen_at"`
	OfflineRemindedAt       int64 `json:"offline_reminded_at"`
	OfflinePushPending      bool  `json:"offline_push_pending"`
	WebFailCount            int   `json:"web_fail_count"`
	WebFailSince            int64 `json:"web_fail_since"`
	WebFailNotifiedAt       int64 `json:"web_fail_notified_at"`
	WebPushPending          bool  `json:"web_push_pending"`
	WatcherWatchSince       int64 `json:"watcher_watch_since"`
	WatcherSilentSeenAt     int64 `json:"watcher_silent_seen_at"`
	WatcherNotifiedAt       int64 `json:"watcher_notified_at"`
	WatcherPushPending      bool  `json:"watcher_push_pending"`
	DemoRemindedAt          int64 `json:"demo_reminded_at"`
	DemoRemindedFor         int64 `json:"demo_reminded_for"`
	MuteRemindedAt          int64 `json:"mute_reminded_at"`
	PermanentMuteRemindedAt int64 `json:"permanent_mute_reminded_at"`
	HealthBadRuns           int   `json:"health_bad_runs"`
	HealthNotifiedAt        int64 `json:"health_notified_at"`
	WeeklyReportAt          int64 `json:"weekly_report_at"`
	LastSendOkAt            int64 `json:"last_send_ok_at"`
	LastSendFailedAt        int64 `json:"last_send_failed_at"`

	LastRunLine string `json:"last_run_line"`
	// RetryAfter: Art der Meldung → frühester nächster Versuch.
	RetryAfter             map[string]int64 `json:"retry_after"`
	EmergencyNotifiedAt    int64            `json:"emergency_notified_at"`
	ConfigBrokenNotifiedAt int64            `json:"config_broken_notified_at"`
	// Pending: einmalige Meldungen, die noch zu senden sind.
	Pending []PendingNotice `json:"pending"`
}

// PendingNotice ist eine vorgemerkte einmalige Meldung an den Admin.
type PendingNotice struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body"`
	Since int64  `json:"since"`
}

// Throttle sind die Bremsen-Zähler je Absender (PHP: login_throttle.json,
// mute_throttle.json).
type Throttle struct {
	Login map[string]ThrottleEntry `json:"login"`
	Mute  map[string]ThrottleEntry `json:"mute"`
}

// ThrottleEntry zählt die Versuche eines Absenders seit FirstAt.
type ThrottleEntry struct {
	Count   int   `json:"count"`
	FirstAt int64 `json:"first_at"`
}

// NewRuntime liefert leere Laufzeitdaten.
func NewRuntime() *Runtime { return (&Runtime{Schema: Schema}).Clone() }

// Log hängt einen Verlaufseintrag an die Outbox; über MaxOutbox fallen die
// ältesten weg.
func (r *Runtime) Log(e Entry) { r.Outbox = appendOutbox(r.Outbox, e) }

// MarshalJSON schreibt über eine Kopie, nie null für eine Liste oder
// Tabelle (wie State.MarshalJSON).
func (r Runtime) MarshalJSON() ([]byte, error) {
	type plain Runtime
	return json.Marshal((*plain)(r.Clone()))
}
