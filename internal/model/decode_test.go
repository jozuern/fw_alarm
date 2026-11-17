package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Gültige Dokumente als Text – vollständig, jedes Feld vorhanden. Die
// Zeilen von TestStrictDecode verderben sie an je einer Stelle.
const (
	recipientsBlock = `"recipients": [
    { "id": 3, "key": "testkey-anna-01", "label": "Anna", "phone": "", "added_at": 0,
      "muted": false, "muted_at": 0, "mute_volume": 0, "mute_permanent": false, "mute_token": "tokAnna0001" }
  ],`
	demoLine    = `"demo": { "enabled": false, "key_id": 0, "key": "", "label": "", "changed_at": 0, "by": "" },`
	commandText = `{ "id": 7, "type": "TEST", "state": "pending", "created_at": 1, "created_by": "admin", "demo_changed_at": 0,
    "delivered_at": 0, "delivered_to": "", "acked_at": 0, "acked_by": "", "ack_result": "", "ack_message": "",
    "cancelled_at": 0, "cancelled_by": "", "cancel_reason": "", "expired_at": 0 }`
	stateText = `{
  "schema": 1,
  "keys_version": 12,
  "next_id": 14,
  ` + recipientsBlock + `
  ` + demoLine + `
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
  "watch": { "last_run_at": 0, "offline_notified": false, "offline_since": 0, "offline_seen_at": 0, "offline_reminded_at": 0,
    "offline_push_pending": false, "web_fail_count": 0, "web_fail_since": 0, "web_fail_notified_at": 0, "web_push_pending": false,
    "watcher_watch_since": 0, "watcher_silent_seen_at": 0, "watcher_notified_at": 0, "watcher_push_pending": false,
    "demo_reminded_at": 0, "demo_reminded_for": 0, "mute_reminded_at": 0, "permanent_mute_reminded_at": 0,
    "health_bad_runs": 0, "health_notified_at": 0, "weekly_report_at": 0, "last_send_ok_at": 0, "last_send_failed_at": 0,
    "last_run_line": "", "retry_after": {}, "emergency_notified_at": 0, "config_broken_notified_at": 0, "pending": [] },
  "throttle": { "login": {}, "mute": {} },
  "outbox": []
}`
	sessionObject = `{ "user": "admin", "role": "admin", "csrf": "c", "auth_stamp": "s", "created_at": 1, "last_seen": 2 }`
	sessionsText  = `{
  "schema": 1,
  "sessions": { "abc": ` + sessionObject + ` }
}`
)

// Jede Zeile verdirbt das Dokument an einer Stelle; der Fehler muss das
// Feld nennen (field ist ein Teil der Meldung, bei den neueren Zeilen die
// ganze Meldung bis auf den Zusatz bei kaputtem JSON).
func TestStrictDecode(t *testing.T) {
	type row struct {
		name, from, to, field string
	}
	dupCommand := strings.Replace(commandText, `"state": "pending",`, `"state": "pending", "state": "acked",`, 1)
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
			{"ganze Zahl über dem Bereich", `"keys_version": 12,`, `"keys_version": 9223372036854775808,`,
				`Feld "keys_version": keine ganze Zahl im erlaubten Bereich`},
			{"null statt Liste", `"recent_types": [ { "id": 7, "type": "TEST" } ]`, `"recent_types": null`, "commands.recent_types"},
			{"null statt Objekt", `"settings": { "mute_max_seconds": 0 },`, `"settings": null,`, "settings"},
			{"null statt Tabelle", `"global": {},`, `"global": null,`, "notify.global"},
			{"null statt Text", `"label": "Anna",`, `"label": null,`, "recipients[0].label"},
			{"null statt Zahl", `"keys_version": 12,`, `"keys_version": null,`, "keys_version"},
			{"null statt Wahrheitswert", `"muted": false,`, `"muted": null,`, "recipients[0].muted"},
			{"null als Empfänger", `"recipients": [`, `"recipients": [ null,`, `Feld "recipients[0]": null statt Objekt`},
			{"null als Verlaufseintrag", `"outbox": [`, `"outbox": [ null,`, `Feld "outbox[0]": null statt Tabelle`},
			{"null als Befehls-Typ", `"recent_types": [`, `"recent_types": [ null,`, `Feld "commands.recent_types[0]": null statt Objekt`},
			{"null als Wert in notify.types", `{ "box_offline": { "enabled": "1" } }`, `{ "box_offline": null }`,
				`Feld "notify.types[box_offline]": null statt Tabelle`},
			{"andere Schreibweise", `"label": "Anna",`, `"LABEL": "Anna",`, "recipients[0].LABEL"},
			{"doppelter Schlüssel", `"label": "Anna",`, `"label": "Anna", "label": "Bob",`, "recipients[0].label"},
			{"doppelter Schlüssel im Verlaufseintrag", `"uid": "ab",`, `"uid": "ab", "uid": "cd",`, "outbox[0][uid]"},
			{"doppelter Schlüssel in einer Tabelle", `{ "enabled": "1" }`, `{ "enabled": "1", "enabled": "0" }`, "notify.types[box_offline][enabled]"},
			{"doppelter Schlüssel im laufenden Befehl", `"current": null`, `"current": ` + dupCommand, `Feld "commands.current.state": doppelt`},
			{"doppeltes schema", `"schema": 1,`, `"schema": 1, "schema": 1,`, `Feld "schema": doppelt`},
			{"doppeltes schema, verschieden", `"schema": 1,`, `"schema": 2, "schema": 1,`, `Feld "schema": doppelt`},
			{"Komma vor der schließenden Klammer", `"by": "" }`, `"by": "", }`, `Feld "demo": kein gültiges JSON`},
			{"Komma am Ende einer Liste", `{ "id": 7, "type": "TEST" } ]`, `{ "id": 7, "type": "TEST" }, ]`,
				`Feld "commands.recent_types[1]": kein gültiges JSON`},
			{"Daten nach dem Dokument", "\n}", "\n} {}", "nach dem Dokument"},
			{"Müll nach dem Dokument", "\n}", "\n}x", "nach dem Dokument"},
			{"anderes Schema", `"schema": 1,`, `"schema": 2,`, `Feld "schema": 2 statt 1`},
			{"schema fehlt", `"schema": 1,`, ``, `Feld "schema": fehlt`},
			{"Liste der Empfänger fehlt", recipientsBlock + "\n  ", ``, `Feld "recipients": fehlt`},
			{"Demo fehlt", demoLine + "\n  ", ``, `Feld "demo": fehlt`},
			{"Feld der Demo fehlt", `, "by": "" }`, ` }`, `Feld "demo.by": fehlt`},
			{"Tabelle in notify fehlt", `"global": {}, `, ``, `Feld "notify.global": fehlt`},
			{"Feld eines Empfängers fehlt", `"phone": "", `, ``, `Feld "recipients[0].phone": fehlt`},
			{"Feld eines Befehls-Typs fehlt", `{ "id": 7, "type": "TEST" }`, `{ "id": 7 }`, `Feld "commands.recent_types[0].type": fehlt`},
			{"leerer Befehl fehlt statt null", `"current": null, `, ``, `Feld "commands.current": fehlt`},
			{"Feld des laufenden Befehls fehlt", `"current": null`,
				`"current": ` + strings.Replace(commandText, `, "expired_at": 0`, ``, 1), `Feld "commands.current.expired_at": fehlt`},
		}},
		{"Runtime", runtimeText, func(b []byte) error { _, err := DecodeRuntime(b); return err }, []row{
			{"unbekanntes Feld", `"seen_at": 5,`, `"seen_at": 5, "x": 1,`, "box.x"},
			{"falscher Typ", `"seen_at": 5,`, `"seen_at": "5",`, "box.seen_at"},
			{"null statt Liste", `"outbox": []`, `"outbox": null`, "outbox"},
			{"null statt Tabelle", `"devices": {},`, `"devices": null,`, "devices"},
			{"null statt Objekt", `"throttle": { "login": {}, "mute": {} },`, `"throttle": null,`, "throttle"},
			{"null statt Text", `"remote_addr": "192.0.2.1",`, `"remote_addr": null,`, "box.remote_addr"},
			{"null als Wert einer Tabelle", `{ "fw-box-1": { "last_alarm": "nie", "alarm_seq": "0", "at": 4 } }`, `{ "fw-box-1": null }`,
				`Feld "alarms_seen[fw-box-1]": null statt Objekt`},
			{"null als Verlaufseintrag", `"outbox": []`, `"outbox": [null]`, `Feld "outbox[0]": null statt Tabelle`},
			{"null als vorgemerkte Meldung", `"pending": []`, `"pending": [null]`, `Feld "watch.pending[0]": null statt Objekt`},
			{"null in einem Paar", `["device_id","fw-box-1"]`, `["device_id",null]`, "box.fields[0][1]"},
			{"Paar mit einem Element", `["device_id","fw-box-1"]`, `["device_id"]`, "box.fields[0]"},
			{"Paar mit drei Elementen", `["device_id","fw-box-1"]`, `["device_id","a","b"]`, "box.fields[0]"},
			{"null statt Paaren", `"fields": [["device_id","fw-box-1"],["keys_version","12"]]`, `"fields": null`, "box.fields"},
			{"andere Schreibweise", `"seen_at": 5,`, `"SEEN_AT": 5,`, "box.SEEN_AT"},
			{"doppelter Schlüssel", `"seen_at": 5,`, `"seen_at": 5, "seen_at": 6,`, "box.seen_at"},
			{"Daten nach dem Dokument", "\n}", "\n}\n[]", "nach dem Dokument"},
			{"anderes Schema", `"schema": 1,`, `"schema": 0,`, `Feld "schema": 0 statt 1`},
			{"schema fehlt", `"schema": 1,`, ``, `Feld "schema": fehlt`},
			{"Feld oben fehlt", "\"lifebeat_at\": 0,\n  ", ``, `Feld "lifebeat_at": fehlt`},
			{"leerer Außen-Wächter fehlt statt null", "\"watcher\": null,\n  ", ``, `Feld "watcher": fehlt`},
			{"Feld des Status fehlt", `"via_bridge": "0", `, ``, `Feld "box.via_bridge": fehlt`},
			{"Merker fehlt", `"weekly_report_at": 0, `, ``, `Feld "watch.weekly_report_at": fehlt`},
			{"Bremse fehlt", `"login": {}, "mute": {} }`, `"login": {} }`, `Feld "throttle.mute": fehlt`},
			{"Feld in einer Tabelle fehlt", `, "at": 4`, ``, `Feld "alarms_seen[fw-box-1].at": fehlt`},
			{"Feld eines Listenelements fehlt", `"pending": []`, `"pending": [ { "id": "a", "title": "t", "body": "b" } ]`,
				`Feld "watch.pending[0].since": fehlt`},
		}},
		{"Sessions", sessionsText, func(b []byte) error { _, err := DecodeSessions(b); return err }, []row{
			{"unbekanntes Feld", `"role": "admin",`, `"role": "admin", "ip": "x",`, "sessions[abc].ip"},
			{"falscher Typ", `"created_at": 1,`, `"created_at": true,`, "sessions[abc].created_at"},
			{"null statt Tabelle", `"sessions": {`, `"sessions": null, "x": {`, "sessions"},
			{"null statt Text", `"user": "admin",`, `"user": null,`, "sessions[abc].user"},
			{"null als Sitzung", `"abc": ` + sessionObject, `"abc": null`, `Feld "sessions[abc]": null statt Objekt`},
			{"andere Schreibweise", `"user": "admin",`, `"User": "admin",`, "sessions[abc].User"},
			{"doppelte Sitzung", `"sessions": { "abc": `, `"sessions": { "abc": ` + sessionObject + `, "abc": `, `Feld "sessions[abc]": doppelt`},
			{"Daten nach dem Dokument", "\n}", "\n}\n\"x\"", "nach dem Dokument"},
			{"anderes Schema", `"schema": 1,`, `"schema": 3,`, `Feld "schema": 3 statt 1`},
			{"schema fehlt", "\"schema\": 1,\n  ", ``, `Feld "schema": fehlt`},
			{"Tabelle der Sitzungen fehlt", ",\n  \"sessions\": { \"abc\": " + sessionObject + " }", ``, `Feld "sessions": fehlt`},
			{"Feld einer Sitzung fehlt", `"csrf": "c", `, ``, `Feld "sessions[abc].csrf": fehlt`},
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
	t.Run("State/gültig mit laufendem Befehl", func(t *testing.T) {
		if _, err := DecodeState([]byte(strings.Replace(stateText, `"current": null`, `"current": `+commandText, 1))); err != nil {
			t.Fatal(err)
		}
	})
}

// Ein anderes Schema ist ein SchemaError mit der gefundenen Nummer – auch
// wenn das Dokument sonst Neues enthält (ein späteres Programm schrieb es).
// Fehlt schema, ist das ein fehlendes Feld wie jedes andere.
func TestSchemaErrorFirst(t *testing.T) {
	newer := strings.Replace(stateText, `"schema": 1,`, `"schema": 2, "neues_feld": [],`, 1)
	_, err := DecodeState([]byte(newer))
	var se *SchemaError
	if !errors.As(err, &se) || se.Found != 2 {
		t.Fatalf("erwartet SchemaError{2}, bekommen %v", err)
	}
	_, err = DecodeState([]byte(strings.Replace(stateText, `"schema": 1,`, ``, 1)))
	var de *DecodeError
	if errors.As(err, &se) || !errors.As(err, &de) || de.Field != "schema" || de.Problem != "fehlt" {
		t.Fatalf("ohne schema: erwartet DecodeError{schema, fehlt}, bekommen %#v", err)
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
