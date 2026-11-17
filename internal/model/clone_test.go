package model

import (
	"bytes"
	"reflect"
	"testing"
)

// cloneCase: ein Dokument-Typ mit seiner Kopier-Funktion.
type cloneCase struct {
	name  string
	new   func() any        // Zeiger auf ein leeres Dokument
	clone func(doc any) any // Zeiger auf die Kopie
}

var cloneCases = []cloneCase{
	{"State", func() any { return &State{} }, func(d any) any { return d.(*State).Clone() }},
	{"Runtime", func() any { return &Runtime{} }, func(d any) any { return d.(*Runtime).Clone() }},
	{"Sessions", func() any { return &Sessions{} }, func(d any) any { return d.(*Sessions).Clone() }},
}

// Keine Liste, keine Tabelle und kein Zeigerziel ist von Original UND
// Kopie aus erreichbar. Das Original wird per Reflexion vollständig gefüllt
// – ein neues Feld ohne eigene Kopie in Clone fällt hier auf.
func TestCloneSharesNothing(t *testing.T) {
	for _, tc := range cloneCases {
		t.Run(tc.name, func(t *testing.T) {
			orig := tc.new()
			n := 0
			fill(reflect.ValueOf(orig).Elem(), &n)
			cp := tc.clone(orig)
			if !reflect.DeepEqual(orig, cp) {
				t.Fatalf("Kopie weicht vom Original ab:\n%s\n%s", mustJSON(t, orig), mustJSON(t, cp))
			}
			a := map[uintptr]string{}
			b := map[uintptr]string{}
			refs(reflect.ValueOf(orig).Elem(), tc.name, a)
			refs(reflect.ValueOf(cp).Elem(), tc.name, b)
			if len(a) == 0 {
				t.Fatal("nichts gesammelt – fill greift nicht")
			}
			for addr, p := range b {
				if q, shared := a[addr]; shared {
					t.Errorf("geteilt: %s (Original %s)", p, q)
				}
			}
		})
	}
}

// Jeden Teil eines Entwurfs ändern: Das Original schreibt danach dieselben
// Bytes wie vorher.
func TestDraftDoesNotTouchSnapshot(t *testing.T) {
	for _, tc := range cloneCases {
		t.Run(tc.name, func(t *testing.T) {
			orig := tc.new()
			n := 0
			fill(reflect.ValueOf(orig).Elem(), &n)
			before := mustJSON(t, orig)
			draft := tc.clone(orig)
			mutate(reflect.ValueOf(draft).Elem())
			if bytes.Equal(mustJSON(t, draft), before) {
				t.Fatal("mutate hat nichts geändert")
			}
			if after := mustJSON(t, orig); !bytes.Equal(after, before) {
				t.Errorf("Original verändert:\nvorher  %s\nnachher %s", before, after)
			}
		})
	}
}

// Ein Dokument mit fehlenden Listen und Tabellen schreibt [] bzw. {} –
// nie null (der strenge Leser lehnte es ab) – und liest sich zurück.
func TestMarshalNeverNull(t *testing.T) {
	st := &State{Schema: Schema, Commands: Commands{NextID: 1}}
	b := mustJSON(t, st)
	if bytes.Contains(b, []byte("null")) && !bytes.Contains(b, []byte(`"current":null`)) {
		t.Errorf("null geschrieben: %s", b)
	}
	if _, err := DecodeState(b); err != nil {
		t.Errorf("nicht zurückzulesen: %v\n%s", err, b)
	}
	rt := &Runtime{Schema: Schema, Box: &BoxStatus{}}
	b = mustJSON(t, rt)
	if _, err := DecodeRuntime(b); err != nil {
		t.Errorf("Laufzeitdaten nicht zurückzulesen: %v\n%s", err, b)
	}
	b = mustJSON(t, &Sessions{Schema: Schema})
	if _, err := DecodeSessions(b); err != nil {
		t.Errorf("Sitzungen nicht zurückzulesen: %v\n%s", err, b)
	}
	b = mustJSON(t, Rescue{Schema: Schema})
	if !bytes.Contains(b, []byte(`"recipients":[]`)) {
		t.Errorf("Not-Kopie: %s", b)
	}
}
