package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
)

// Strenges Lesen der eigenen Dokumente (Spec §6.3). encoding/json allein
// genügt nicht: Es nimmt Schlüssel in jeder Schreibweise, null für Listen
// und Texte und bei doppelten Schlüsseln den letzten. Deshalb läuft vor dem
// eigentlichen Einlesen ein Durchgang über die Zeichen des Dokuments, der
// den Go-Typ danebenhält. Die erlaubten Feldnamen kommen per Reflexion aus
// den json-Angaben der Typen – es gibt keine zweite Liste, die abweichen
// könnte. Eingebettete Felder gibt es in den Dokumenten nicht.

// DecodeError nennt das Feld, an dem ein Dokument scheitert.
type DecodeError struct {
	Field   string // Pfad wie "recipients[0].label"; leer = das Dokument als Ganzes
	Problem string
}

func (e *DecodeError) Error() string {
	if e.Field == "" {
		return e.Problem
	}
	return "Feld \"" + e.Field + "\": " + e.Problem
}

// SchemaError: Das Dokument trägt eine andere Schema-Nummer als das
// Programm (fehlt sie, ist Found 0). Geprüft vor allem anderen – ein
// Dokument eines neueren Programms darf Felder haben, die dieses nicht kennt.
type SchemaError struct{ Found int }

func (e *SchemaError) Error() string {
	return fmt.Sprintf("Feld \"schema\": %d statt %d", e.Found, Schema)
}

// DecodeState liest state.json streng und prüft den Inhalt wie vor dem
// Sichern (Validate): Eine von Hand verdorbene Liste ist "nicht lesbar"
// (Spec §7).
func DecodeState(data []byte) (*State, error) {
	var s State
	if err := decodeStrict(data, &s); err != nil {
		return nil, err
	}
	if s.Schema != Schema {
		return nil, &SchemaError{Found: s.Schema}
	}
	c := s.Clone()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("Inhalt ungültig: %w", err)
	}
	return c, nil
}

// DecodeRuntime liest runtime.json streng.
func DecodeRuntime(data []byte) (*Runtime, error) {
	var r Runtime
	if err := decodeStrict(data, &r); err != nil {
		return nil, err
	}
	if r.Schema != Schema {
		return nil, &SchemaError{Found: r.Schema}
	}
	return r.Clone(), nil
}

// DecodeSessions liest sessions.json streng.
func DecodeSessions(data []byte) (*Sessions, error) {
	var s Sessions
	if err := decodeStrict(data, &s); err != nil {
		return nil, err
	}
	if s.Schema != Schema {
		return nil, &SchemaError{Found: s.Schema}
	}
	return s.Clone(), nil
}

// decodeStrict prüft data gegen den Typ, auf den v zeigt, und liest es
// dann ein.
func decodeStrict(data []byte, v any) error {
	if found, ok := peekSchema(data); ok && found != Schema {
		return &SchemaError{Found: found}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	w := walker{dec: dec}
	if err := w.value(reflect.TypeOf(v).Elem(), ""); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return &DecodeError{Problem: "Daten nach dem Dokument"}
	}
	full := json.NewDecoder(bytes.NewReader(data))
	full.UseNumber()
	full.DisallowUnknownFields()
	if err := full.Decode(v); err != nil {
		// Nach der Prüfung oben nicht zu erwarten – zur Sicherheit trotzdem.
		return &DecodeError{Problem: "nicht lesbar (" + err.Error() + ")"}
	}
	return nil
}

// peekSchema liest nur die oberste Ebene und liefert die Zahl unter
// "schema" (genau so geschrieben), wenn es sie gibt.
func peekSchema(data []byte) (int, bool) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return 0, false
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return 0, false
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return 0, false
		}
		if tok == "schema" {
			n, err := strconv.Atoi(string(raw))
			return n, err == nil
		}
	}
	return 0, false
}

// walker geht die Zeichen eines Dokuments durch und hält den Go-Typ daneben.
type walker struct{ dec *json.Decoder }

// shapes: Typen mit eigener JSON-Gestalt (UnmarshalJSON) und die Gestalt,
// nach der sie geprüft werden.
var shapes = map[reflect.Type]reflect.Type{
	reflect.TypeFor[Fields](): pairsShape,
}

var unmarshalerType = reflect.TypeFor[json.Unmarshaler]()

func (w *walker) next(path string) (json.Token, error) {
	tok, err := w.dec.Token()
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, &DecodeError{Field: path, Problem: "Dokument endet vorzeitig"}
	}
	if err != nil {
		return nil, &DecodeError{Field: path, Problem: "kein gültiges JSON (" + err.Error() + ")"}
	}
	return tok, nil
}

// value liest den nächsten Wert und prüft ihn gegen t.
func (w *walker) value(t reflect.Type, path string) error {
	tok, err := w.next(path)
	if err != nil {
		return err
	}
	if shape, ok := shapes[t]; ok {
		t = shape
	} else if t.Implements(unmarshalerType) || reflect.PointerTo(t).Implements(unmarshalerType) {
		return &DecodeError{Field: path, Problem: "Typ " + t.String() + " ohne Gestalt für das strenge Lesen"}
	}
	if tok == nil {
		if t.Kind() == reflect.Pointer || t.Kind() == reflect.Interface {
			return nil
		}
		return &DecodeError{Field: path, Problem: "null statt " + kindName(t)}
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Interface:
		return w.loose(tok, path)
	case reflect.Struct:
		if tok != json.Delim('{') {
			return wrongType(tok, t, path)
		}
		return w.object(t, path)
	case reflect.Map:
		if tok != json.Delim('{') {
			return wrongType(tok, t, path)
		}
		return w.mapping(t, path)
	case reflect.Slice, reflect.Array:
		if tok != json.Delim('[') {
			return wrongType(tok, t, path)
		}
		return w.list(t, path)
	case reflect.String:
		if _, ok := tok.(string); !ok {
			return wrongType(tok, t, path)
		}
	case reflect.Bool:
		if _, ok := tok.(bool); !ok {
			return wrongType(tok, t, path)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, ok := tok.(json.Number)
		if !ok {
			return wrongType(tok, t, path)
		}
		if _, err := strconv.ParseInt(string(n), 10, t.Bits()); err != nil {
			return &DecodeError{Field: path, Problem: "keine ganze Zahl im erlaubten Bereich"}
		}
	default:
		return &DecodeError{Field: path, Problem: "Typ " + t.String() + " ist im Dokument nicht vorgesehen"}
	}
	return nil
}

// object prüft die Felder eines Objekts gegen die Struktur t; die
// öffnende Klammer ist schon gelesen.
func (w *walker) object(t reflect.Type, path string) error {
	fields := jsonFields(t)
	seen := map[string]bool{}
	for w.dec.More() {
		tok, err := w.next(path)
		if err != nil {
			return err
		}
		key := tok.(string) // Schlüssel eines Objekts sind immer Texte
		fieldPath := joinField(path, key)
		if seen[key] {
			return &DecodeError{Field: fieldPath, Problem: "doppelt"}
		}
		seen[key] = true
		ft, ok := fields[key]
		if !ok {
			for name := range fields {
				if strings.EqualFold(name, key) {
					return &DecodeError{Field: fieldPath, Problem: "andere Schreibweise als \"" + name + "\""}
				}
			}
			return &DecodeError{Field: fieldPath, Problem: "unbekannt"}
		}
		if err := w.value(ft, fieldPath); err != nil {
			return err
		}
	}
	_, err := w.next(path) // schließende Klammer
	return err
}

// mapping prüft eine Tabelle: Schlüssel beliebig, aber nicht doppelt.
func (w *walker) mapping(t reflect.Type, path string) error {
	seen := map[string]bool{}
	for w.dec.More() {
		tok, err := w.next(path)
		if err != nil {
			return err
		}
		key := tok.(string)
		keyPath := path + "[" + key + "]"
		if seen[key] {
			return &DecodeError{Field: keyPath, Problem: "doppelt"}
		}
		seen[key] = true
		if err := w.value(t.Elem(), keyPath); err != nil {
			return err
		}
	}
	_, err := w.next(path)
	return err
}

// list prüft die Elemente einer Liste; ein Feld fester Länge (Array) muss
// genau so viele haben.
func (w *walker) list(t reflect.Type, path string) error {
	n := 0
	for w.dec.More() {
		if t.Kind() == reflect.Array && n == t.Len() {
			return &DecodeError{Field: path, Problem: fmt.Sprintf("mehr als %d Elemente", t.Len())}
		}
		if err := w.value(t.Elem(), fmt.Sprintf("%s[%d]", path, n)); err != nil {
			return err
		}
		n++
	}
	if t.Kind() == reflect.Array && n != t.Len() {
		return &DecodeError{Field: path, Problem: fmt.Sprintf("%d statt %d Elemente", n, t.Len())}
	}
	_, err := w.next(path)
	return err
}

// loose prüft einen freien Wert (Verlaufseintrag): jede Gestalt, auch
// null – nur doppelte Schlüssel nicht.
func (w *walker) loose(tok json.Token, path string) error {
	switch tok {
	case json.Delim('{'):
		seen := map[string]bool{}
		for w.dec.More() {
			k, err := w.next(path)
			if err != nil {
				return err
			}
			key := k.(string)
			keyPath := path + "[" + key + "]"
			if seen[key] {
				return &DecodeError{Field: keyPath, Problem: "doppelt"}
			}
			seen[key] = true
			v, err := w.next(keyPath)
			if err != nil {
				return err
			}
			if err := w.loose(v, keyPath); err != nil {
				return err
			}
		}
		_, err := w.next(path)
		return err
	case json.Delim('['):
		for n := 0; w.dec.More(); n++ {
			itemPath := fmt.Sprintf("%s[%d]", path, n)
			v, err := w.next(itemPath)
			if err != nil {
				return err
			}
			if err := w.loose(v, itemPath); err != nil {
				return err
			}
		}
		_, err := w.next(path)
		return err
	}
	return nil
}

// jsonFields: JSON-Name → Typ jedes Felds der Struktur t.
func jsonFields(t reflect.Type) map[string]reflect.Type {
	out := make(map[string]reflect.Type, t.NumField())
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = f.Type
	}
	return out
}

func joinField(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

func wrongType(tok json.Token, t reflect.Type, path string) error {
	return &DecodeError{Field: path, Problem: tokenName(tok) + " statt " + kindName(t)}
}

// kindName benennt, was an einer Stelle stehen muss.
func kindName(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Struct:
		return "Objekt"
	case reflect.Map:
		return "Tabelle"
	case reflect.Slice, reflect.Array:
		return "Liste"
	case reflect.String:
		return "Text"
	case reflect.Bool:
		return "Wahrheitswert"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return "Zahl"
	}
	return t.String()
}

// tokenName benennt, was tatsächlich dasteht.
func tokenName(tok json.Token) string {
	switch tok.(type) {
	case json.Delim:
		if tok == json.Delim('{') {
			return "Objekt"
		}
		return "Liste"
	case string:
		return "Text"
	case bool:
		return "Wahrheitswert"
	case json.Number:
		return "Zahl"
	}
	return "null"
}
