package model

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// Fester Zeitpunkt der Tests: 17.11.2026 10:00 UTC.
var t0 = time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

// Erfundene Keys und Tokens – nie echte (Plan: Global Constraints 1).
const (
	keyAnna   = "testkey-anna-01"
	keyBert   = "testkey-bert-02"
	keyCarl   = "testkey-carl-03"
	tokenAnna = "tokAnna0001"
	tokenBert = "tokBert0002"
	tokenCarl = "tokCarl0003"
)

// sampleState ist ein gültiger Zustand mit drei Empfängern (Bert stumm seit
// einer Stunde, Carl dauerhaft stumm), laufendem Befehl und einem Eintrag
// in der Outbox.
func sampleState() *State {
	s := NewState()
	s.KeysVersion = 12
	s.NextID = 14
	s.Recipients = []Recipient{
		{ID: 3, Key: keyAnna, Label: "Anna", AddedAt: t0.Unix() - 86400, MuteToken: tokenAnna},
		{ID: 7, Key: keyBert, Label: "Bert", Phone: "0171 1234", AddedAt: t0.Unix() - 86400,
			Muted: true, MutedAt: t0.Unix() - 3600, MuteVolume: 3, MuteToken: tokenBert},
		{ID: 13, Key: keyCarl, Label: "Carl", AddedAt: t0.Unix() - 86400,
			Muted: true, MutedAt: t0.Unix() - 30*86400, MutePermanent: true, MuteToken: tokenCarl},
	}
	s.Settings.MuteMaxSeconds = 14400
	s.Notify.Global["quiet_from"] = "22"
	s.Notify.Types["box_offline"] = map[string]string{"enabled": "1", "level": "critical"}
	s.Commands = Commands{NextID: 8, Current: &Command{ID: 7, Type: "TEST", State: CommandPending,
		CreatedAt: t0.Unix() - 10, CreatedBy: "admin"}, RecentTypes: []RecentType{{ID: 6, Type: "ALARM"}, {ID: 7, Type: "TEST"}}}
	s.Log(NewEntry(EvKeyAdded, t0, map[string]any{"by": "admin", "label": "Anna", "key": "test..."}))
	return s
}

// sampleRuntime: Box gesehen, ein Board, Außen-Wächter, Merker, Bremsen.
func sampleRuntime() *Runtime {
	r := NewRuntime()
	r.LifebeatAt = t0.Unix()
	r.Box = &BoxStatus{SeenAt: t0.Unix() - 5, ViaBridge: "1", RemoteAddr: "192.0.2.10",
		Fields: Fields{{"device_id", "fw-box-1"}, {"keys_version", "12"}, {"keys_count", "3"}}}
	r.AlarmsSeen["fw-box-1"] = AlarmSeen{LastAlarm: "nie", AlarmSeq: "0", At: t0.Unix() - 100}
	r.Devices["fw-box-1"] = Device{SeenAt: t0.Unix() - 5, FwBuild: "b1"}
	r.Watcher = &OutsideWatcher{SeenAt: t0.Unix() - 60}
	r.Watch.LastRunAt = t0.Unix() - 600
	r.Watch.RetryAfter["box_offline"] = t0.Unix() + 900
	r.Watch.Pending = []PendingNotice{{ID: "rewind", Title: "T", Body: "B", Since: t0.Unix()}}
	r.Throttle.Login["192.0.2.10"] = ThrottleEntry{Count: 2, FirstAt: t0.Unix() - 30}
	r.Log(NewEntry(EvKeyTest, t0, map[string]any{"by": "admin", "label": "Anna", "ok": true}))
	return r
}

func sampleSessions() *Sessions {
	s := NewSessions()
	s.Sessions["aa11"] = Session{User: "admin", Role: "admin", CSRF: "c1", AuthStamp: "st", CreatedAt: t0.Unix() - 60, LastSeen: t0.Unix()}
	return s
}

func mustJSON(t testing.TB, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ---- Reflexion: alles füllen, alles ändern, alle Verweise sammeln --------

var numberType = reflect.TypeFor[json.Number]()

// fill setzt jedes Feld auf einen Wert ungleich null: Listen mit zwei
// Elementen, Tabellen mit zwei Schlüsseln, jeden Zeiger auf ein Objekt und
// jedes "any" auf verschachtelte Tabellen und Listen. Ein neu
// hinzugekommenes Feld ist so automatisch gefüllt.
func fill(v reflect.Value, n *int) {
	*n++
	switch v.Kind() {
	case reflect.Struct:
		for i := range v.NumField() {
			fill(v.Field(i), n)
		}
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 2, 2))
		for i := range 2 {
			fill(v.Index(i), n)
		}
	case reflect.Array:
		for i := range v.Len() {
			fill(v.Index(i), n)
		}
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		for i := range 2 {
			k := reflect.New(v.Type().Key()).Elem()
			k.SetString("k" + strconv.Itoa(i))
			e := reflect.New(v.Type().Elem()).Elem()
			fill(e, n)
			v.SetMapIndex(k, e)
		}
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem(), n)
	case reflect.Interface:
		v.Set(reflect.ValueOf(map[string]any{
			"list": []any{"x", map[string]any{"n": json.Number(strconv.Itoa(*n))}},
			"map":  map[string]any{"b": true, "s": "t"},
		}))
	case reflect.String:
		v.SetString("s" + strconv.Itoa(*n))
	case reflect.Int, reflect.Int64:
		v.SetInt(int64(*n))
	case reflect.Bool:
		v.SetBool(true)
	default:
		panic("fill: Art nicht vorgesehen: " + v.Kind().String())
	}
}

// mutate ändert jeden erreichbaren Wert und fügt jeder Tabelle einen
// Schlüssel hinzu.
func mutate(v reflect.Value) {
	switch v.Kind() {
	case reflect.Struct:
		for i := range v.NumField() {
			mutate(v.Field(i))
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			mutate(v.Index(i))
		}
	case reflect.Map:
		if v.IsNil() {
			return
		}
		for _, k := range v.MapKeys() {
			e := reflect.New(v.Type().Elem()).Elem()
			e.Set(v.MapIndex(k))
			mutate(e)
			v.SetMapIndex(k, e)
		}
		k := reflect.New(v.Type().Key()).Elem()
		k.SetString("neu")
		v.SetMapIndex(k, reflect.Zero(v.Type().Elem()))
	case reflect.Pointer:
		if !v.IsNil() {
			mutate(v.Elem())
		}
	case reflect.Interface:
		if v.IsNil() {
			return
		}
		inner := v.Elem()
		if inner.Type() == numberType {
			v.Set(reflect.ValueOf(json.Number("424242")))
			return
		}
		c := reflect.New(inner.Type()).Elem()
		c.Set(inner)
		mutate(c)
		v.Set(c)
	case reflect.String:
		v.SetString(v.String() + "-geändert")
	case reflect.Int, reflect.Int64:
		v.SetInt(v.Int() + 1)
	case reflect.Float64:
		v.SetFloat(v.Float() + 1)
	case reflect.Bool:
		v.SetBool(!v.Bool())
	}
}

// refs sammelt die Adressen aller Listen-Elemente, Tabellen und Ziele von
// Zeigern, die von v aus erreichbar sind (Adresse → Pfad).
func refs(v reflect.Value, path string, out map[uintptr]string) {
	switch v.Kind() {
	case reflect.Struct:
		for i := range v.NumField() {
			refs(v.Field(i), path+"."+v.Type().Field(i).Name, out)
		}
	case reflect.Slice:
		for i := range v.Len() {
			p := fmt.Sprintf("%s[%d]", path, i)
			out[v.Index(i).Addr().Pointer()] = p
			refs(v.Index(i), p, out)
		}
	case reflect.Array:
		for i := range v.Len() {
			refs(v.Index(i), fmt.Sprintf("%s[%d]", path, i), out)
		}
	case reflect.Map:
		if v.IsNil() {
			return
		}
		out[v.Pointer()] = path
		for _, k := range v.MapKeys() {
			refs(v.MapIndex(k), fmt.Sprintf("%s[%v]", path, k), out)
		}
	case reflect.Pointer:
		if !v.IsNil() {
			out[v.Pointer()] = path
			refs(v.Elem(), path, out)
		}
	case reflect.Interface:
		if !v.IsNil() {
			refs(v.Elem(), path, out)
		}
	}
}
