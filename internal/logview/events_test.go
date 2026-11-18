package logview

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"fwalarm/internal/model"
)

// Die Tabelle in sich: jedes Ereignis aus model.AllEvents hat genau eine
// Zeile und umgekehrt; Kategorien, Typen und Felder sind wohlgeformt; die
// Regeln, an denen etwas hängt, stehen ausdrücklich da.
func TestEventTableComplete(t *testing.T) {
	for _, name := range model.AllEvents {
		if _, ok := Events[name]; !ok {
			t.Errorf("%s: keine Zeile in der Tabelle", name)
		}
	}
	if len(Events) != len(model.AllEvents) {
		t.Errorf("%d Zeilen, %d Ereignisse in model.AllEvents", len(Events), len(model.AllEvents))
	}
	for name, ev := range Events {
		if !slices.Contains(model.AllEvents, name) {
			t.Errorf("%s: steht nicht in model.AllEvents", name)
		}
		if !slices.Contains([]string{"alarm", "keys", "system", "command"}, ev.Cat) {
			t.Errorf("%s: Kategorie %q", name, ev.Cat)
		}
		// Ein Versand-Ereignis steht auch unter "Alarme" ("Letzter Alarm"
		// zeigt nichts, was der Filter Alarme verschwiege).
		if ev.Core && ev.Cat != "alarm" {
			t.Errorf("%s: Versand-Ereignis außerhalb der Kategorie alarm", name)
		}
		seen := map[string]bool{"at": true, "event": true}
		for _, f := range ev.Fields {
			if seen[f.Name] || f.Name == "" {
				t.Errorf("%s: Feld %q doppelt, leer oder at/event", name, f.Name)
			}
			seen[f.Name] = true
			if !slices.Contains([]string{"str", "int", "bool"}, f.Type) {
				t.Errorf("%s.%s: Typ %q", name, f.Name, f.Type)
			}
			if f.Path == "" || strings.HasPrefix(f.Path, ".") || strings.HasSuffix(f.Path, ".") || strings.Contains(f.Path, "..") {
				t.Errorf("%s.%s: Pfad %q", name, f.Name, f.Path)
			}
			if f.Optional && f.Default != nil {
				t.Errorf("%s.%s: optional mit Standard – der Standard gälte nie", name, f.Name)
			}
		}
	}

	// demo_repaired schreibt der Go-Server nicht mehr, alte Einträge zeigt er
	// weiter (Spec §7).
	if ev, ok := Events[model.EvDemoRepaired]; !ok || ev.Cat != "system" || len(ev.Fields) != 0 {
		t.Errorf("demo_repaired: %+v %v", ev, ok)
	}
	// Ein Relaisalarm ohne source stammt von vor der Unterscheidung: relay.
	if f := fieldOf(t, model.EvRelayAlarm, "source"); f.Default != "relay" || f.Optional {
		t.Errorf("relay_alarm.source: %+v", f)
	}
	// Nur für den Admin – genau diese.
	var admin []string
	for name, ev := range Events {
		if ev.Admin {
			admin = append(admin, name)
		}
	}
	slices.Sort(admin)
	if want := []string{model.EvNotifyChanged, model.EvSettingChanged, model.EvStateRewound}; !slices.Equal(admin, want) {
		t.Errorf("nur für den Admin: %v, erwartet %v", admin, want)
	}
	// Die Versand-Ereignisse – genau diese.
	var core []string
	for name, ev := range Events {
		if ev.Core {
			core = append(core, name)
		}
	}
	slices.Sort(core)
	if want := []string{model.EvFalseAlarm, model.EvFalseAlarmStarted, model.EvNasAlarm, model.EvNasAlarmStarted,
		model.EvRelayAlarm, model.EvSendInterrupted}; !slices.Equal(core, want) {
		t.Errorf("Versand-Ereignisse: %v, erwartet %v", core, want)
	}
	if ev := Events[model.EvStateRewound]; ev.Cat != "system" {
		t.Errorf("state_rewound: Kategorie %q", ev.Cat)
	}
	// "demo" fehlt bei Alt-Einträgen – dann erkennt die Oberfläche den
	// Modus am Text; ein Standard wäre eine Behauptung.
	for _, name := range []string{model.EvNasAlarmStarted, model.EvFalseAlarmStarted, model.EvNasAlarm, model.EvFalseAlarm, model.EvSendInterrupted} {
		if f := fieldOf(t, name, "demo"); !f.Optional || f.Type != "bool" {
			t.Errorf("%s.demo: %+v", name, f)
		}
	}
}

// Was nie hinausgeht, steht in keiner Zeile: die Einzelergebnisse eines
// Versands (mit Klartext-Keys), Tokens, Telefonnummern, Passwörter. Keys
// (Feld key/old_key) stehen im Verlauf nur maskiert.
func TestNoSecretFields(t *testing.T) {
	banned := []string{"results", "mute_token", "token", "phone", "password", "csrf", "remote_addr"}
	for name, ev := range Events {
		for _, f := range ev.Fields {
			for _, part := range append(strings.Split(f.Path, "."), f.Name) {
				if slices.Contains(banned, part) {
					t.Errorf("%s.%s: %q geht nie hinaus", name, f.Name, f.Path)
				}
			}
		}
	}
}

func fieldOf(t *testing.T, event, name string) Field {
	t.Helper()
	for _, f := range Events[event].Fields {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("%s: kein Feld %q", event, name)
	return Field{}
}

// Die Tabelle ist die von PHP: log_event_types() aus nas_dashboard/lib/
// common.php wird gelesen und ausgewertet (ein kleiner Auswerter für genau
// die Sprachmittel, die die Funktion benutzt), dann wird jedes Ereignis
// verglichen – Kategorie, core, admin und jedes Feld mit Name, Typ, Pfad und
// Standard in seiner Reihenfolge. Die einzigen erlaubten Unterschiede sind
// die beiden neuen Ereignisse des Go-Servers. Dieser Test geht mit der
// PHP-Fassung (Aufgabe 37).
func TestEventTableMatchesPHP(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "nas_dashboard", "lib", "common.php"))
	if os.IsNotExist(err) {
		t.Skip("PHP-Fassung nicht mehr da")
	}
	if err != nil {
		t.Fatal(err)
	}
	src := strings.ReplaceAll(string(raw), "\r\n", "\n")
	body, ok := section(src, "function log_event_types(): array\n{\n", "\n}\n")
	if !ok {
		t.Fatal("log_event_types() nicht gefunden")
	}
	// Die Anweisungen nach dem Zwischenspeicher (static $types …) bis vor
	// "return $types;".
	_, rest, ok := strings.Cut(body, "if ($types !== null) {")
	if ok {
		_, rest, ok = strings.Cut(rest, "\n    }\n")
	}
	if !ok {
		t.Fatal("Zwischenspeicher in log_event_types() nicht gefunden")
	}
	end := strings.LastIndex(rest, "return $types;")
	if end < 0 {
		t.Fatal("return $types; nicht gefunden")
	}
	env, err := runPHP(rest[:end])
	if err != nil {
		t.Fatal(err)
	}
	types, ok := env["types"].(*phpArray)
	if !ok {
		t.Fatalf("$types ist %T", env["types"])
	}

	php := map[string]Event{}
	for i, k := range types.keys {
		name, ok := k.(string)
		if !ok {
			t.Fatalf("Schlüssel %v in $types", k)
		}
		ev, err := eventFromPHP(types.vals[i])
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		php[name] = ev
	}
	if len(php) < 30 {
		t.Fatalf("nur %d Ereignisse aus PHP gelesen", len(php))
	}
	for name, want := range php {
		got, ok := Events[name]
		if !ok {
			t.Errorf("%s: steht in PHP, nicht in Go", name)
			continue
		}
		if got.Cat != want.Cat || got.Core != want.Core || got.Admin != want.Admin {
			t.Errorf("%s: Go cat=%s core=%v admin=%v, PHP cat=%s core=%v admin=%v",
				name, got.Cat, got.Core, got.Admin, want.Cat, want.Core, want.Admin)
		}
		if g, w := fmt.Sprintf("%+v", got.Fields), fmt.Sprintf("%+v", want.Fields); g != w {
			t.Errorf("%s: Felder\nGo:  %s\nPHP: %s", name, g, w)
		}
	}
	for name := range Events {
		if _, ok := php[name]; !ok && name != model.EvStateRewound && name != model.EvSendInterrupted {
			t.Errorf("%s: steht in Go, nicht in PHP", name)
		}
	}
}

// eventFromPHP: eine Zeile von log_event_types() als Event.
func eventFromPHP(v any) (Event, error) {
	def, ok := v.(*phpArray)
	if !ok {
		return Event{}, fmt.Errorf("keine Liste: %T", v)
	}
	var ev Event
	for i, k := range def.keys {
		switch k {
		case "cat":
			ev.Cat, _ = def.vals[i].(string)
		case "core":
			ev.Core, _ = def.vals[i].(bool)
		case "admin":
			ev.Admin, _ = def.vals[i].(bool)
		case "fields":
			fields, ok := def.vals[i].(*phpArray)
			if !ok {
				return Event{}, fmt.Errorf("fields ist %T", def.vals[i])
			}
			ev.Fields = []Field{}
			for j, fk := range fields.keys {
				spec, ok := fields.vals[j].(*phpArray)
				if !ok || len(spec.vals) < 2 || len(spec.vals) > 3 {
					return Event{}, fmt.Errorf("Feld %v: %v", fk, fields.vals[j])
				}
				name, _ := fk.(string)
				typ, _ := spec.vals[0].(string)
				path, _ := spec.vals[1].(string)
				f := Field{Name: name, Type: strings.TrimPrefix(typ, "?"), Path: path, Optional: strings.HasPrefix(typ, "?")}
				if len(spec.vals) == 3 {
					f.Default = spec.vals[2]
				}
				ev.Fields = append(ev.Fields, f)
			}
		default:
			return Event{}, fmt.Errorf("unbekannter Schlüssel %v", k)
		}
	}
	if len(ev.Fields) == 0 {
		ev.Fields = nil // wie die Zeile in Go: ohne Felder
	}
	return ev, nil
}

// section: der Text zwischen open und dem ersten close danach; false, wenn
// eines fehlt.
func section(src, open, close string) (string, bool) {
	_, rest, ok := strings.Cut(src, open)
	if !ok {
		return "", false
	}
	body, _, ok := strings.Cut(rest, close)
	return body, ok
}

// ---- Ein kleiner Auswerter für log_event_types() ---------------------------
//
// Er kennt genau, was die Funktion benutzt: Zuweisungen "$name = Ausdruck;",
// Texte in einfachen Anführungszeichen, true/false/null, Listen [ … ] mit
// und ohne Schlüssel, ".", array_merge(…) und Pfeilfunktionen "static
// fn(Typ $a, Typ $b = …): Typ => Ausdruck" samt ihrem Aufruf $name(…).
// Alles andere ist ein Fehler – dann muss der Test mitwachsen.

// phpArray ist ein PHP-Array: Schlüssel (string oder int) in ihrer
// Reihenfolge.
type phpArray struct {
	keys []any
	vals []any
}

func (a *phpArray) set(k, v any) {
	for i, have := range a.keys {
		if have == k {
			a.vals[i] = v
			return
		}
	}
	a.keys = append(a.keys, k)
	a.vals = append(a.vals, v)
}

func (a *phpArray) nextIndex() int {
	n := 0
	for _, k := range a.keys {
		if i, ok := k.(int); ok && i >= n {
			n = i + 1
		}
	}
	return n
}

type phpFn struct {
	params []string
	defs   []*phpNode // Standard je Parameter, nil = keiner
	body   *phpNode
}

type phpNode struct {
	kind  string // str, bool, null, arr, var, call, cat, merge, fn
	s     string
	b     bool
	keys  []*phpNode // arr: Schlüssel je Eintrag (nil = ohne)
	vals  []*phpNode // arr: Werte; call/merge: Argumente
	left  *phpNode   // cat
	right *phpNode   // cat
	fn    *phpFn
}

type phpParser struct {
	toks []string
	pos  int
}

func tokenizePHP(src string) ([]string, error) {
	var toks []string
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == ' ' || c == '\n' || c == '\t' || c == '\r':
			i++
		case strings.HasPrefix(src[i:], "//"):
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '\'':
			var b strings.Builder
			i++
			for ; i < len(src) && src[i] != '\''; i++ {
				if src[i] == '\\' && i+1 < len(src) && (src[i+1] == '\'' || src[i+1] == '\\') {
					i++
				}
				b.WriteByte(src[i])
			}
			if i >= len(src) {
				return nil, fmt.Errorf("Text ohne Ende")
			}
			i++
			toks = append(toks, "'"+b.String())
		case strings.HasPrefix(src[i:], "=>"):
			toks = append(toks, "=>")
			i += 2
		case strings.ContainsRune("[](),;.:=", rune(c)):
			toks = append(toks, string(c))
			i++
		case c == '$' || c == '_' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z':
			j := i + 1
			for j < len(src) && (src[j] == '_' || 'a' <= src[j] && src[j] <= 'z' || 'A' <= src[j] && src[j] <= 'Z' || '0' <= src[j] && src[j] <= '9') {
				j++
			}
			toks = append(toks, src[i:j])
			i = j
		default:
			return nil, fmt.Errorf("unbekanntes Zeichen %q", c)
		}
	}
	return toks, nil
}

func (p *phpParser) peek() string {
	if p.pos < len(p.toks) {
		return p.toks[p.pos]
	}
	return ""
}

func (p *phpParser) next() string {
	t := p.peek()
	p.pos++
	return t
}

func (p *phpParser) expect(tok string) error {
	if got := p.next(); got != tok {
		return fmt.Errorf("%q erwartet, %q gefunden", tok, got)
	}
	return nil
}

func (p *phpParser) expr() (*phpNode, error) {
	left, err := p.term()
	if err != nil {
		return nil, err
	}
	for p.peek() == "." {
		p.next()
		right, err := p.term()
		if err != nil {
			return nil, err
		}
		left = &phpNode{kind: "cat", left: left, right: right}
	}
	return left, nil
}

func (p *phpParser) term() (*phpNode, error) {
	tok := p.next()
	switch {
	case strings.HasPrefix(tok, "'"):
		return &phpNode{kind: "str", s: tok[1:]}, nil
	case tok == "true" || tok == "false":
		return &phpNode{kind: "bool", b: tok == "true"}, nil
	case tok == "null":
		return &phpNode{kind: "null"}, nil
	case tok == "[":
		return p.array()
	case tok == "static":
		return p.arrowFn()
	case tok == "array_merge":
		args, err := p.args()
		return &phpNode{kind: "merge", vals: args}, err
	case strings.HasPrefix(tok, "$"):
		if p.peek() == "(" {
			args, err := p.args()
			return &phpNode{kind: "call", s: tok[1:], vals: args}, err
		}
		return &phpNode{kind: "var", s: tok[1:]}, nil
	}
	return nil, fmt.Errorf("unerwartet: %q", tok)
}

func (p *phpParser) array() (*phpNode, error) {
	n := &phpNode{kind: "arr"}
	for p.peek() != "]" {
		v, err := p.expr()
		if err != nil {
			return nil, err
		}
		var k *phpNode
		if p.peek() == "=>" {
			p.next()
			k = v
			if v, err = p.expr(); err != nil {
				return nil, err
			}
		}
		n.keys = append(n.keys, k)
		n.vals = append(n.vals, v)
		if p.peek() != "," {
			break
		}
		p.next()
	}
	return n, p.expect("]")
}

func (p *phpParser) args() ([]*phpNode, error) {
	if err := p.expect("("); err != nil {
		return nil, err
	}
	var out []*phpNode
	for p.peek() != ")" {
		v, err := p.expr()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		if p.peek() != "," {
			break
		}
		p.next()
	}
	return out, p.expect(")")
}

// arrowFn: nach "static": fn(Typ $a, Typ $b = Ausdruck): Typ => Ausdruck.
func (p *phpParser) arrowFn() (*phpNode, error) {
	if err := p.expect("fn"); err != nil {
		return nil, err
	}
	if err := p.expect("("); err != nil {
		return nil, err
	}
	fn := &phpFn{}
	for p.peek() != ")" {
		if !strings.HasPrefix(p.peek(), "$") {
			p.next() // Typ
		}
		name := p.next()
		if !strings.HasPrefix(name, "$") {
			return nil, fmt.Errorf("Parameter erwartet, %q gefunden", name)
		}
		var def *phpNode
		if p.peek() == "=" {
			p.next()
			var err error
			if def, err = p.expr(); err != nil {
				return nil, err
			}
		}
		fn.params = append(fn.params, name[1:])
		fn.defs = append(fn.defs, def)
		if p.peek() != "," {
			break
		}
		p.next()
	}
	if err := p.expect(")"); err != nil {
		return nil, err
	}
	if p.peek() == ":" {
		p.next()
		p.next() // Rückgabetyp
	}
	if err := p.expect("=>"); err != nil {
		return nil, err
	}
	body, err := p.expr()
	fn.body = body
	return &phpNode{kind: "fn", fn: fn}, err
}

func evalPHP(n *phpNode, env map[string]any) (any, error) {
	switch n.kind {
	case "str":
		return n.s, nil
	case "bool":
		return n.b, nil
	case "null":
		return nil, nil
	case "fn":
		return n.fn, nil
	case "var":
		v, ok := env[n.s]
		if !ok {
			return nil, fmt.Errorf("$%s unbekannt", n.s)
		}
		return v, nil
	case "cat":
		l, err := evalPHP(n.left, env)
		if err != nil {
			return nil, err
		}
		r, err := evalPHP(n.right, env)
		if err != nil {
			return nil, err
		}
		ls, ok1 := l.(string)
		rs, ok2 := r.(string)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf(". mit %T und %T", l, r)
		}
		return ls + rs, nil
	case "arr":
		a := &phpArray{}
		for i, kn := range n.keys {
			v, err := evalPHP(n.vals[i], env)
			if err != nil {
				return nil, err
			}
			if kn == nil {
				a.set(a.nextIndex(), v)
				continue
			}
			k, err := evalPHP(kn, env)
			if err != nil {
				return nil, err
			}
			if _, ok := k.(string); !ok {
				return nil, fmt.Errorf("Schlüssel %T", k)
			}
			a.set(k, v)
		}
		return a, nil
	case "merge":
		out := &phpArray{}
		for _, an := range n.vals {
			v, err := evalPHP(an, env)
			if err != nil {
				return nil, err
			}
			a, ok := v.(*phpArray)
			if !ok {
				return nil, fmt.Errorf("array_merge mit %T", v)
			}
			for i, k := range a.keys {
				if _, isInt := k.(int); isInt {
					out.set(out.nextIndex(), a.vals[i])
				} else {
					out.set(k, a.vals[i])
				}
			}
		}
		return out, nil
	case "call":
		fv, ok := env[n.s].(*phpFn)
		if !ok {
			return nil, fmt.Errorf("$%s ist keine Funktion", n.s)
		}
		local := map[string]any{}
		for k, v := range env {
			local[k] = v
		}
		for i, name := range fv.params {
			switch {
			case i < len(n.vals):
				v, err := evalPHP(n.vals[i], env)
				if err != nil {
					return nil, err
				}
				local[name] = v
			case fv.defs[i] != nil:
				v, err := evalPHP(fv.defs[i], env)
				if err != nil {
					return nil, err
				}
				local[name] = v
			default:
				return nil, fmt.Errorf("$%s: Parameter $%s fehlt", n.s, name)
			}
		}
		return evalPHP(fv.body, local)
	}
	return nil, fmt.Errorf("Knoten %q", n.kind)
}

// runPHP führt Zuweisungen "$name = Ausdruck;" aus und liefert die Variablen.
func runPHP(src string) (map[string]any, error) {
	toks, err := tokenizePHP(src)
	if err != nil {
		return nil, err
	}
	p := &phpParser{toks: toks}
	env := map[string]any{}
	for p.peek() != "" {
		name := p.next()
		if !strings.HasPrefix(name, "$") {
			return nil, fmt.Errorf("Zuweisung erwartet, %q gefunden", name)
		}
		if err := p.expect("="); err != nil {
			return nil, err
		}
		n, err := p.expr()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if err := p.expect(";"); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		v, err := evalPHP(n, env)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		env[name[1:]] = v
	}
	return env, nil
}

// Der Auswerter selbst: ein falsches Programm ist ein Fehler, kein stilles
// Ergebnis – sonst verglich der Test nichts.
func TestPHPEvaluator(t *testing.T) {
	env, err := runPHP(`
		$f = static fn(string $a, array $more = []): array => ['x' => 'p.' . $a, 'l' => array_merge(['eins', 'k' => 'a'], $more)];
		$v = $f('q', ['k' => 'b', 'zwei']); // Kommentar
		$w = $f('r');
		$s = 'it\'s';`)
	if err != nil {
		t.Fatal(err)
	}
	v := env["v"].(*phpArray)
	if v.vals[0] != "p.q" {
		t.Errorf("x: %v", v.vals[0])
	}
	l := v.vals[1].(*phpArray)
	if fmt.Sprint(l.keys) != "[0 k 1]" || fmt.Sprint(l.vals) != "[eins b zwei]" {
		t.Errorf("array_merge: %v %v", l.keys, l.vals)
	}
	if w := env["w"].(*phpArray).vals[1].(*phpArray); len(w.keys) != 2 {
		t.Errorf("Standard eines Parameters: %v", w.keys)
	}
	if env["s"] != "it's" {
		t.Errorf("Text: %q", env["s"])
	}
	for _, bad := range []string{`$x = $y;`, `$x = 'a' . [];`, `$x = 5;`, `$x = ['a' => 'b'`, `x = 'a';`, `$f = static fn($a): array => $a; $x = $f();`} {
		if _, err := runPHP(bad); err == nil {
			t.Errorf("%q: kein Fehler", bad)
		}
	}
}
