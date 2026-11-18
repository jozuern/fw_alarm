package web

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"fwalarm/internal/config"
)

// PHP: require_machine_auth. Das Token zählt nur aus dem Rumpf eines POST
// (nie aus der Query oder einer Kopfzeile – die landeten in Protokollen);
// verglichen wird sein SHA-256 in konstanter Zeit mit dem Hash der
// Konfiguration, der in Groß- oder Kleinbuchstaben und mit Leerraum außen
// stehen darf. Ein leerer Hash passt auf nichts.
func TestMachineAuth(t *testing.T) {
	const token = "maschinen-token-der-box-0001"
	sum := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(sum[:])
	formOf := func(r *http.Request) *Form {
		t.Helper()
		return parse(t, r)
	}
	auth := func(configured string, r *http.Request) bool {
		t.Helper()
		return MachineAuth(cfgCore(&config.Config{MachineTokenSHA256: configured}), formOf(r))
	}

	if !auth(hash, postForm("/api/poll.php", formType, "nonce=1&token="+token)) {
		t.Error("richtiges Token im Rumpf abgelehnt")
	}
	mpType, mpBody := multipartBody(t, "token", token)
	if !auth(hash, postForm("/api/poll.php", mpType, mpBody)) {
		t.Error("richtiges Token im multipart-Rumpf abgelehnt")
	}
	for name, configured := range map[string]string{
		"Großbuchstaben":            strings.ToUpper(hash),
		"Leerraum außen":            " \t" + hash + "\r\n",
		"Großbuchstaben, Leerraum ": "  " + strings.ToUpper(hash) + "\n",
	} {
		if !auth(configured, postForm("/api/poll.php", formType, "token="+token)) {
			t.Errorf("Hash in der Konfiguration mit %s abgelehnt", name)
		}
	}

	// Nur aus dem Rumpf: Query, Kopfzeilen und Cookies zählen nicht.
	r := postForm("/api/poll.php?token="+token, formType, "nonce=1")
	r.Header.Set("Token", token)
	r.Header.Set("X-Token", token)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Cookie", "token="+token)
	if auth(hash, r) {
		t.Error("Token außerhalb des Rumpfs angenommen")
	}
	// Ein GET hat keine Felder – auch mit Token im Rumpf.
	g := httptest.NewRequest(http.MethodGet, "/api/poll.php?token="+token, strings.NewReader("token="+token))
	g.Header.Set("Content-Type", formType)
	if auth(hash, g) {
		t.Error("Token aus einem GET angenommen")
	}

	for name, c := range map[string]struct{ configured, body string }{
		"falsches Token":                       {hash, "token=" + token + "x"},
		"Token fehlt":                          {hash, "nonce=1"},
		"leeres Token":                         {hash, "token="},
		"Token ist der Hash selbst":            {hash, "token=" + hash},
		"Token mit Leerraum":                   {hash, "token=+" + token},
		"Token als Liste":                      {hash, "token[]=" + token},
		"leerer Hash, leeres Token":            {"", "token="},
		"leerer Hash, Token, dessen Hash …":    {"", "token=" + token},
		"Hash nur aus Leerraum, leeres Token":  {"  ", "token="},
		"leerer Hash gegen SHA-256 von nichts": {"", "token=%00"},
		"anderer Hash":                         {strings.Repeat("0", 64), "token=" + token},
		"verkürzter Hash":                      {hash[:63], "token=" + token},
	} {
		if auth(c.configured, postForm("/api/poll.php", formType, c.body)) {
			t.Errorf("%s: angenommen", name)
		}
	}
	// Der Hash des leeren Tokens ist ein gültiger SHA-256 – ein leeres Token
	// passt trotzdem nie.
	empty := sha256.Sum256(nil)
	if auth(hex.EncodeToString(empty[:]), postForm("/api/poll.php", formType, "token=")) {
		t.Error("leeres Token gegen den Hash von nichts angenommen")
	}
	if MachineAuth(cfgCore(&config.Config{MachineTokenSHA256: hash}), nil) {
		t.Error("ohne Formular angenommen")
	}
}

// Verglichen wird in konstanter Zeit: MachineAuth vergleicht den Hash nur
// über subtle.ConstantTimeCompare, nie mit == oder != (außer gegen eine
// Konstante wie "" oder nil).
func TestMachineAuthConstantTime(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "machine.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fn *ast.FuncDecl
	for _, d := range file.Decls {
		if f, ok := d.(*ast.FuncDecl); ok && f.Name.Name == "MachineAuth" {
			fn = f
		}
	}
	if fn == nil {
		t.Fatal("MachineAuth nicht in machine.go")
	}
	constantTime := false
	ast.Inspect(fn, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "ConstantTimeCompare" {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "subtle" {
					constantTime = true
				}
			}
		case *ast.BinaryExpr:
			if x.Op == token.EQL || x.Op == token.NEQ {
				if !isConstant(x.X) && !isConstant(x.Y) {
					t.Errorf("%s: Vergleich mit %s ohne Konstante", fset.Position(x.Pos()), x.Op)
				}
			}
		}
		return true
	})
	if !constantTime {
		t.Error("MachineAuth vergleicht nicht über subtle.ConstantTimeCompare")
	}
}

// isConstant: ein Literal ("", 1) oder nil.
func isConstant(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.BasicLit:
		return true
	case *ast.Ident:
		return x.Name == "nil"
	}
	return false
}
