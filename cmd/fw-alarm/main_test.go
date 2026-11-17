package main

import (
	"bytes"
	"strings"
	"testing"
)

func call(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = run(args, strings.NewReader(""), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestRun(t *testing.T) {
	if code, out, _ := call("version"); code != 0 || out != "dev\n" {
		t.Errorf("version: %d %q", code, out)
	}
	if code, _, errOut := call(); code != 2 || !strings.Contains(errOut, "Aufruf:") {
		t.Errorf("ohne Befehl: %d %q", code, errOut)
	}
	if code, _, errOut := call("gibtsnicht"); code != 2 || !strings.Contains(errOut, "Unbekannter Befehl: gibtsnicht") {
		t.Errorf("unbekannter Befehl: %d %q", code, errOut)
	}
	if code, _, _ := call("version", "überzählig"); code != 2 {
		t.Errorf("überzählige Angabe: %d", code)
	}
	if code, out, _ := call("check-config", "--config", "../../server/go/config.example.toml"); code != 0 ||
		!strings.HasSuffix(out, "Konfiguration in Ordnung.\n") {
		t.Errorf("check-config mit --config: %d %q", code, out)
	}
	if code, _, errOut := call("check-config", "-config", "gibt/es/nicht.toml"); code != 1 || !strings.HasPrefix(errOut, "Fehler: ") {
		t.Errorf("check-config ohne Datei: %d %q", code, errOut)
	}
	// hash-password nimmt --config an (wie jeder Befehl), braucht es aber nicht.
	if code, _, errOut := call("hash-password", "--config", "egal.toml"); code != 1 || !strings.Contains(errOut, "\nFehler: ") {
		t.Errorf("hash-password ohne Eingabe: %d %q", code, errOut)
	}
}
