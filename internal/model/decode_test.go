package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Gültige Dokumente als Text – die Zeilen von TestStrictDecode verderben
// sie an je einer Stelle.
const (
	stateText = `{
  "schema": 1,
  "keys_version": 12,
  "next_id": 14,
  "recipients": [
    { "id": 3, "key": "testkey-anna-01", "label": "Anna", "phone": "", "added_at": 0,
      "muted": false, "muted_at": 0, "mute_volume": 0, "mute_permanent": false, "mute_token": "tokAnna0001" }
  ],
  "demo": { "enabled": false, "key_id": 0, "key": "", "label": "", "changed_at": 0, "by": "" },
  "settings": { "mute_max_seconds": 0 },
  "notify": { "global": {}, "types": { "box_offline": { "enabled": "1" } } },
  "commands": { "next_id": 8, "current": null, "recent_types": [ { "id": 7, "type": "TEST" } ] },
  "outbox": [ { "event": "key_added", "log_at": 1763373600, "uid": "ab", "extra": null, "list": [1, "x"] } ]
}`
	runtimeText = `{
  "schema": 1,
  "lifebeat_at": 0,
  "silence_from": 0,
  "box": { "seen_at": 5, "via_bridge": "0", "remote_addr": "192.0.2.1", "fields": [["device_id","fw-box-1"],["keys_version","12"]] },
  "alarms_seen": { "fw-box-1": { "last_alarm": "nie", "alarm_seq": "0", "at": 4 } },
  "devices": {},
  "watcher": null,
  "watch": { "last_run_at": 0, "retry_after": {}, "pending": [] },
  "throttle": { "login": {}, "mute": {} },
  "outbox": []
}`
	sessionsText = `{
  "schema": 1,
  "sessions": { "abc": { "user": "admin", "role": "admin", "csrf": "c", "auth_stamp": "s", "created_at": 1, "last_seen": 2 } }
}`
)

// Jede Zeile verdirbt das Dokument an einer Stelle; der Fehler muss das
// Feld nennen.
func TestStrictDecode(t *testing.T) {
	type row struct {
		name, from, to, field string
	}
	docs := []struct {
		name   string
		text   string
		decode func([]byte) error
		rows   []row
	}{
		{"State", stateText, func(b []byte) error { _, err := DecodeState(b); return err }, []row{
			{"unbekanntes Feld", `"label": "Anna",`, `"label": "Anna", "colour": "red",`, "recipients[0].colour"},
			{"unbekanntes Feld oben", `"next_id": 14,`, `"next_id": 14, "version": 3,`, "version"},
			{"falscher Typ", `"id": 3,`, `"id": "3",`, "recipients[0].id"},
			{"Zahl mit Komma statt ganzer Zahl", `"keys_version": 12,`, `"keys_version": 12.5,`, "keys_version"},
			{"null statt Liste", `"recent_types": [ { "id": 7, "type": "TEST" } ]`, `"recent_types": null`, "commands.recent_types"},
			{"null statt Objekt", `"settings": { "mute_max_seconds": 0 },`, `"settings": null,`, "settings"},
			{"null statt Tabelle", `"global": {},`, `"global": null,`, "notify.global"},
			{"null statt Text", `"label": "Anna",`, `"label": null,`, "recipients[0].label"},
			{"null statt Zahl", `"keys_version": 12,`, `"keys_version": null,`, "keys_version"},
			{"null statt Wahrheitswert", `"muted": false,`, `"muted": null,`, "recipients[0].muted"},
			{"andere Schreibweise", `"label": "Anna",`, `"LABEL": "Anna",`, "recipients[0].LABEL"},
			{"doppelter Schlüssel", `"label": "Anna",`, `"label": "Anna", "label": "Bob",`, "recipients[0].label"},
			{"doppelter Schlüssel im Verlaufseintrag", `"uid": "ab",`, `"uid": "ab", "uid": "cd",`, "outbox[0][uid]"},
			{"doppelter Schlüssel in einer Tabelle", `{ "enabled": "1" }`, `{ "enabled": "1", "enabled": "0" }`, "notify.types[box_offline][enabled]"},
			{"Daten nach dem Dokument", "\n}", "\n} {}", "nach dem Dokument"},
			{"Müll nach dem Dokument", "\n}", "\n}x", "nach dem Dokument"},
			{"anderes Schema", `"schema": 1,`, `"schema": 2,`, `"schema"`},
			{"Schema fehlt", `"schema": 1,`, ``, `"schema"`},
		}},
		{"Runtime", runtimeText, func(b []byte) error { _, err := DecodeRuntime(b); return err }, []row{
			{"unbekanntes Feld", `"seen_at": 5,`, `"seen_at": 5, "x": 1,`, "box.x"},
			{"falscher Typ", `"seen_at": 5,`, `"seen_at": "5",`, "box.seen_at"},
			{"null statt Liste", `"outbox": []`, `"outbox": null`, "outbox"},
			{"null statt Tabelle", `"devices": {},`, `"devices": null,`, "devices"},
			{"null statt Objekt", `"throttle": { "login": {}, "mute": {} },`, `"throttle": null,`, "throttle"},
			{"null statt Text", `"remote_addr": "192.0.2.1",`, `"remote_addr": null,`, "box.remote_addr"},
			{"null in einem Paar", `["device_id","fw-box-1"]`, `["device_id",null]`, "box.fields[0][1]"},
			{"Paar mit einem Element", `["device_id","fw-box-1"]`, `["device_id"]`, "box.fields[0]"},
			{"Paar mit drei Elementen", `["device_id","fw-box-1"]`, `["device_id","a","b"]`, "box.fields[0]"},
			{"null statt Paaren", `"fields": [["device_id","fw-box-1"],["keys_version","12"]]`, `"fields": null`, "box.fields"},
			{"andere Schreibweise", `"seen_at": 5,`, `"SEEN_AT": 5,`, "box.SEEN_AT"},
			{"doppelter Schlüssel", `"seen_at": 5,`, `"seen_at": 5, "seen_at": 6,`, "box.seen_at"},
			{"Daten nach dem Dokument", "\n}", "\n}\n[]", "nach dem Dokument"},
			{"anderes Schema", `"schema": 1,`, `"schema": 0,`, `"schema"`},
		}},
		{"Sessions", sessionsText, func(b []byte) error { _, err := DecodeSessions(b); return err }, []row{
			{"unbekanntes Feld", `"role": "admin",`, `"role": "admin", "ip": "x",`, "sessions[abc].ip"},
			{"falscher Typ", `"created_at": 1,`, `"created_at": true,`, "sessions[abc].created_at"},
			{"null statt Tabelle", `"sessions": {`, `"sessions": null, "x": {`, "sessions"},
			{"null statt Text", `"user": "admin",`, `"user": null,`, "sessions[abc].user"},
			{"andere Schreibweise", `"user": "admin",`, `"User": "admin",`, "sessions[abc].User"},
			{"doppelte Sitzung", `"sessions": {`, `"sessions": { "abc": {}, `, "sessions[abc]"},
			{"Daten nach dem Dokument", "\n}", "\n}\n\"x\"", "nach dem Dokument"},
			{"anderes Schema", `"schema": 1,`, `"schema": 3,`, `"schema"`},
		}},
	}
	for _, doc := range docs {
		t.Run(doc.name+"/gültig", func(t *testing.T) {
			if err := doc.decode([]byte(doc.text)); err != nil {
				t.Fatalf("gültiges Dokument abgelehnt: %v", err)
			}
		})
		for _, r := range doc.rows {
			t.Run(doc.name+"/"+r.name, func(t *testing.T) {
				if !strings.Contains(doc.text, r.from) {
					t.Fatalf("Testfehler: %q steht nicht im Dokument", r.from)
				}
				bad := strings.Replace(doc.text, r.from, r.to, 1)
				err := doc.decode([]byte(bad))
				if err == nil {
					t.Fatalf("angenommen:\n%s", bad)
				}
				if !strings.Contains(err.Error(), r.field) {
					t.Errorf("Fehler nennt %q nicht: %v", r.field, err)
				}
			})
		}
	}
}

// Ein anderes Schema ist ein SchemaError mit der gefundenen Nummer – auch
// wenn das Dokument sonst Neues enthält (ein späteres Programm schrieb es).
func TestSchemaErrorFirst(t *testing.T) {
	newer := strings.Replace(stateText, `"schema": 1,`, `"schema": 2, "neues_feld": [],`, 1)
	_, err := DecodeState([]byte(newer))
	var se *SchemaError
	if !errors.As(err, &se) || se.Found != 2 {
		t.Fatalf("erwartet SchemaError{2}, bekommen %v", err)
	}
	_, err = DecodeState([]byte(strings.Replace(stateText, `"schema": 1,`, ``, 1)))
	if !errors.As(err, &se) || se.Found != 0 {
		t.Fatalf("ohne schema: erwartet SchemaError{0}, bekommen %v", err)
	}
}

// Ein leeres oder abgeschnittenes Dokument ist ein Fehler, kein leerer
// Zustand.
func TestTruncatedIsError(t *testing.T) {
	for _, text := range []string{"", "   ", "{", stateText[:len(stateText)/2], "null", "[]", `"x"`} {
		if _, err := DecodeState([]byte(text)); err == nil {
			t.Errorf("angenommen: %q", text)
		}
	}
}

// Eine von Hand verdorbene Liste ist nicht lesbar (Spec §7): DecodeState
// prüft den Inhalt wie vor dem Sichern.
func TestDecodeValidates(t *testing.T) {
	bad := strings.Replace(stateText, `"key": "testkey-anna-01"`, `"key": "kurz"`, 1)
	if _, err := DecodeState([]byte(bad)); err == nil {
		t.Fatal("ungültiger Key angenommen")
	}
}

// Die Reihenfolge der gepushten Felder übersteht Schreiben und Lesen.
func TestFieldsKeepOrder(t *testing.T) {
	r := NewRuntime()
	r.Box = &BoxStatus{SeenAt: 1}
	for i := 39; i >= 0; i-- { // absichtlich nicht alphabetisch
		r.Box.Fields = append(r.Box.Fields, Field{Name: fmt.Sprintf("f%02d", i), Value: fmt.Sprintf("v%d", i)})
	}
	b := mustJSON(t, r)
	if !bytes.Contains(b, []byte(`"fields":[["f39","v39"],["f38","v38"],`)) {
		t.Fatalf("keine Liste von Paaren: %s", b)
	}
	back, err := DecodeRuntime(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Box.Fields) != 40 {
		t.Fatalf("%d Felder statt 40", len(back.Box.Fields))
	}
	for i, f := range back.Box.Fields {
		if want := fmt.Sprintf("f%02d", 39-i); f.Name != want {
			t.Fatalf("Feld %d heißt %q statt %q", i, f.Name, want)
		}
	}
	if back.Box.Fields.Get("f07") != "v7" || back.Box.Fields.Get("fehlt") != "" {
		t.Error("Get liefert falsche Werte")
	}
}

// Lesen → Schreiben → Lesen ist stabil: Was gelesen wurde, schreibt sich
// wieder genauso und liest sich wieder.
func FuzzDecodeState(f *testing.F) {
	f.Add([]byte(stateText))
	f.Add(mustJSON(f, sampleState()))
	f.Add([]byte(strings.Replace(stateText, `"current": null`,
		`"current": {"id": 7, "type": "ALARM", "state": "delivered", "created_at": 1, "created_by": "a", "demo_changed_at": 2,
		 "delivered_at": 3, "delivered_to": "b", "acked_at": 0, "acked_by": "", "ack_result": "", "ack_message": "",
		 "cancelled_at": 0, "cancelled_by": "", "cancel_reason": "", "expired_at": 0}`, 1)))
	f.Add([]byte(strings.Replace(stateText, `"uid": "ab"`, `"uid": "ä😀", "n": 1e400`, 1)))
	f.Add([]byte(strings.Replace(stateText, `"uid": "ab"`, "\"uid\": \"\xff\"", 1)))
	f.Add([]byte(`{"schema":1}`))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		s, err := DecodeState(data)
		if err != nil {
			return
		}
		b1, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("gelesen, aber nicht zu schreiben: %v", err)
		}
		s2, err := DecodeState(b1)
		if err != nil {
			t.Fatalf("Geschriebenes nicht wieder zu lesen: %v\n%s", err, b1)
		}
		b2, err := json.Marshal(s2)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(b1, b2) {
			t.Fatalf("nicht stabil:\n%s\n%s", b1, b2)
		}
	})
}
