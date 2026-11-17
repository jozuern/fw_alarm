package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // Zeitzonen unabhängig vom Rechner, auf dem der Test läuft

	"fwalarm/internal/config"
)

const examplePath = "../../server/go/config.example.toml"

func checkConfig(t *testing.T, path string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = RunCheckConfig(path, time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestCheckConfigExample(t *testing.T) {
	code, out, errOut := checkConfig(t, examplePath)
	if code != 0 {
		t.Fatalf("Rückgabe %d, Ausgabe:\n%s%s", code, out, errOut)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("erwartet zwei Hinweise und die Schlusszeile:\n%s", out)
	}
	for _, l := range lines[:2] {
		if !strings.HasPrefix(l, "Hinweis: ") {
			t.Errorf("Zeile %q beginnt nicht mit „Hinweis: “", l)
		}
	}
	if lines[2] != "Konfiguration in Ordnung." {
		t.Errorf("Schlusszeile %q", lines[2])
	}
	if errOut != "" {
		t.Errorf("Fehlerausgabe: %q", errOut)
	}
}

func TestCheckConfigBroken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	secret := "$2y$10$geheimgeheimgeheimgeheimgeheimgeheimgeheimgeheimgeheim"
	data := "dashboard_password_hash = \"" + secret + "\"\nfarbe = \"rot\"\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := checkConfig(t, path)
	if code != 1 {
		t.Fatalf("Rückgabe %d, erwartet 1", code)
	}
	if out != "" {
		t.Errorf("Ausgabe bei Fehler: %q", out)
	}
	if !strings.HasPrefix(errOut, "Fehler: ") || !strings.Contains(errOut, "farbe") {
		t.Errorf("Fehlerausgabe %q", errOut)
	}
	if strings.Contains(errOut+out, "geheim") {
		t.Errorf("Ausgabe enthält das Geheimnis: %q", errOut)
	}

	code, _, errOut = checkConfig(t, filepath.Join(dir, "fehlt.toml"))
	if code != 1 || !strings.HasPrefix(errOut, "Fehler: ") {
		t.Errorf("fehlende Datei: Rückgabe %d, %q", code, errOut)
	}
}

func TestCheckConfigNamesNoSecret(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	// Lauter Hinweise, jeder mit einem Wert, der nie ausgegeben werden darf.
	data := strings.Join([]string{
		`machine_token_sha256 = "token-geheim-1"`,
		`watcher_token_sha256 = "token-geheim-2"`,
		`dashboard_password_hash = "passwort-geheim-3"`,
		`bark_key_status = "key geheim 4"`,
		`bark_keys_alarm = ["key geheim 5"]`,
		`send_guard_keys = ["guardkeygeheim6"]`,
		`send_guard_until = "2026-11-20 11:00"`,
		``,
	}, "\n")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := checkConfig(t, path)
	if code != 0 {
		t.Fatalf("Rückgabe %d: %s%s", code, out, errOut)
	}
	if strings.Count(out, "Hinweis: ") < 5 {
		t.Errorf("zu wenige Hinweise:\n%s", out)
	}
	if strings.Contains(out+errOut, "geheim") {
		t.Errorf("Ausgabe enthält einen Wert:\n%s", out)
	}
}

func hashPassword(t *testing.T, input string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = RunHashPassword(strings.NewReader(input), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestHashPasswordCommand(t *testing.T) {
	code, out, errOut := hashPassword(t, "Pass wört!\nPass wört!\n")
	if code != 0 {
		t.Fatalf("Rückgabe %d: %s", code, errOut)
	}
	hash := strings.TrimSuffix(out, "\n")
	if strings.Contains(hash, "\n") || !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("Ausgabe %q", out)
	}
	if !config.VerifyPassword(hash, "Pass wört!") {
		t.Error("ausgegebener Hash passt nicht zum Passwort")
	}
	if strings.Contains(out+errOut, "Pass wört!") {
		t.Error("Passwort steht in der Ausgabe")
	}

	// Windows-Zeilenenden zählen nicht zum Passwort.
	code, out, _ = hashPassword(t, "abc\r\nabc\r\n")
	if code != 0 || !config.VerifyPassword(strings.TrimSpace(out), "abc") {
		t.Errorf("CRLF: Rückgabe %d, %q", code, out)
	}

	for name, input := range map[string]string{
		"verschieden":    "eins\nzwei\n",
		"leer":           "\n\n",
		"nur eine Zeile": "eins\n",
		"gar nichts":     "",
	} {
		code, out, errOut := hashPassword(t, input)
		if code != 1 || out != "" || !strings.HasPrefix(errOut, "Fehler: ") {
			t.Errorf("%s: Rückgabe %d, Ausgabe %q, Fehler %q", name, code, out, errOut)
		}
	}
}
