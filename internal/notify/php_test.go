package notify

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"fwalarm/internal/config"
	"fwalarm/internal/model"
)

// Die Tabellen sind die von PHP: Arten, Optionen, Möglichkeiten, Standards,
// Texte. Festgehalten in testdata/tables.golden.json – so, wie PHP sie
// ausgibt (admin_notify_types und admin_notify_global_specs rechnen ihr
// Ergebnis aus der Konfiguration und der Empfängerliste, deshalb wurde PHP
// ausgeführt statt die Quelle gelesen), nach genau den zwei Änderungen, die
// bewusst anders sind: der Hinweis von nas_web (Spec §7) und "config.php" →
// "config.toml" in den Beschriftungen.
//
// Go wird IMMER mit der Datei verglichen – auch auf GitHub (ohne php) und
// nachdem die PHP-Fassung fort ist (Aufgabe 37). Solange php im PATH und die
// PHP-Fassung da ist, wird zusätzlich die Datei mit PHP selbst verglichen, so
// kann sie nicht unbemerkt von PHP abweichen. Neu schreiben (aus PHP):
// FW_WRITE_GOLDEN=1 go test ./internal/notify -run TestTablesMatchPHP
func TestTablesMatchPHP(t *testing.T) {
	// Zuerst PHP: Beim Neuschreiben gilt danach schon die neue Datei.
	t.Run("PHP", func(t *testing.T) {
		common, err := filepath.Abs(filepath.Join("..", "..", "nas_dashboard", "lib", "common.php"))
		if err != nil {
			t.Fatal(err)
		}
		write := os.Getenv("FW_WRITE_GOLDEN") == "1"
		php, err := exec.LookPath("php")
		_, statErr := os.Stat(common)
		switch {
		case write && (err != nil || statErr != nil):
			t.Fatal("FW_WRITE_GOLDEN=1 braucht php im PATH und die PHP-Fassung")
		case os.IsNotExist(statErr):
			t.Skip("PHP-Fassung nicht mehr da – Go wird weiter mit der Datei verglichen")
		case err != nil:
			t.Skip("php nicht im PATH – Go wird weiter mit der Datei verglichen")
		}
		fromPHP := map[string]tables{}
		for _, tc := range tableCases {
			fromPHP[tc.id] = phpAsGo(t, phpTables(t, php, common, tc.php, goldenRecipients))
		}
		if write {
			b, _ := json.MarshalIndent(fromPHP, "", " ") // nur Text, Wahrheitswerte und Listen – geht immer
			if err := os.WriteFile(goldenPath, append(b, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		golden := readGolden(t)
		for _, tc := range tableCases {
			if diff := tablesDiff(golden[tc.id], fromPHP[tc.id]); diff != "" {
				t.Errorf("%s: %s weicht von PHP ab (neu schreiben mit FW_WRITE_GOLDEN=1, wenn PHP recht hat):\n%s",
					tc.name, goldenPath, diff)
			}
		}
	})
	golden := readGolden(t)
	for _, tc := range tableCases {
		t.Run(tc.name, func(t *testing.T) {
			want, ok := golden[tc.id]
			if !ok {
				t.Fatalf("%s fehlt in %s", tc.id, goldenPath)
			}
			st := model.NewState()
			st.Recipients = goldenRecipients
			if diff := tablesDiff(want, goTables(typesFor(tc.cfg), globalSpecsFor(st))); diff != "" {
				t.Errorf("Go weicht von PHP ab:\n%s", diff)
			}
		})
	}
}

// goldenPath: die Tabellen von PHP, je Fall (tableCases.id).
var goldenPath = filepath.Join("testdata", "tables.golden.json")

// goldenRecipients: die Empfängerliste aller Fälle (Stellvertreter-Wahl).
var goldenRecipients = []model.Recipient{
	{ID: 3, Key: annaKey, Label: "Anna Müller", MuteToken: "annatoken03"},
	{ID: 7, Key: jensKey, Label: "Jens", MuteToken: "jenstoken07"},
}

// tableCases: drei Konfigurationen – für PHP (config.php) und Go dieselben
// Werte.
var tableCases = []struct {
	id, name string
	php      map[string]any
	cfg      *config.Config
}{
	{"standards", "Standards", map[string]any{},
		&config.Config{OfflineAfterSeconds: 180, WatchdogOfflineAfterSeconds: 600, DemoReminderAfterSeconds: 14400}},
	{"eigene-werte-aussen-waechter", "eigene Werte, Außen-Wächter", map[string]any{
		"offline_after_seconds": 240, "watchdog_offline_after_seconds": 777, "demo_reminder_after_seconds": 0,
		"watcher_token_sha256": strings.Repeat("c", 64)},
		&config.Config{OfflineAfterSeconds: 240, WatchdogOfflineAfterSeconds: 777, DemoReminderAfterSeconds: 0,
			WatcherTokenSHA256: strings.Repeat("c", 64)}},
	{"groessere-box-schwelle-demo-in-minuten", "größere Box-Schwelle, Demo in Minuten", map[string]any{
		"offline_after_seconds": 5400, "watchdog_offline_after_seconds": 600, "demo_reminder_after_seconds": 5400},
		&config.Config{OfflineAfterSeconds: 5400, WatchdogOfflineAfterSeconds: 600, DemoReminderAfterSeconds: 5400}},
}

// readGolden liest testdata/tables.golden.json.
func readGolden(t *testing.T) map[string]tables {
	t.Helper()
	b, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("%v (schreiben mit FW_WRITE_GOLDEN=1, php im PATH)", err)
	}
	var out map[string]tables
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("%s: %v", goldenPath, err)
	}
	return out
}

// phpAsGo bringt, was bewusst anders ist, auf die Go-Form: den Hinweis von
// nas_web (der alte Klammersatz MUSS bei PHP noch stehen – sonst hat sich
// PHP geändert) und "config.php" in den Beschriftungen.
func phpAsGo(t *testing.T, want tables) tables {
	t.Helper()
	oldHint := regexp.MustCompile(`\(Login und Status der Box scheitern dann\)\.$`)
	for i := range want.Types {
		if want.Types[i].Key == KindNasWeb {
			if !oldHint.MatchString(want.Types[i].Hint) {
				t.Fatalf("PHP: Hinweis von nas_web geändert: %q", want.Types[i].Hint)
			}
			want.Types[i].Hint = oldHint.ReplaceAllString(want.Types[i].Hint,
				"(Änderungen werden dann nicht gespeichert, Befehle an die Box nicht ausgeliefert).")
		}
		for j := range want.Types[i].Options {
			for k := range want.Types[i].Options[j].Choices {
				c := &want.Types[i].Options[j].Choices[k]
				c[1] = strings.ReplaceAll(c[1], "config.php", "config.toml")
			}
		}
	}
	return want
}

// tablesDiff: "" bei Gleichheit, sonst die Zeilen um die erste Abweichung.
func tablesDiff(want, got tables) string {
	wb, _ := json.MarshalIndent(want, "", " ") // nur Text, Wahrheitswerte und Listen – geht immer
	gb, _ := json.MarshalIndent(got, "", " ")
	if string(wb) == string(gb) {
		return ""
	}
	return firstDifference(string(wb), string(gb))
}

// tables ist die gemeinsame Form beider Seiten: Möglichkeiten als Paare
// [Wert, Text] in ihrer Reihenfolge.
type tables struct {
	Types  []typeRow   `json:"types"`
	Global []optionRow `json:"global"`
}

type typeRow struct {
	Key            string      `json:"key"`
	Label          string      `json:"label"`
	Hint           string      `json:"hint"`
	Important      bool        `json:"important"`
	DefaultEnabled bool        `json:"default_enabled"`
	Options        []optionRow `json:"options"`
}

type optionRow struct {
	Key     string      `json:"key"`
	Label   string      `json:"label"`
	Default string      `json:"default"`
	Choices [][2]string `json:"choices"`
}

func goTables(types []TypeDef, global []OptionDef) tables {
	opts := func(defs []OptionDef) []optionRow {
		out := []optionRow{}
		for _, d := range defs {
			row := optionRow{Key: d.Key, Label: d.Label, Default: d.Default, Choices: [][2]string{}}
			for _, c := range d.Choices {
				row.Choices = append(row.Choices, [2]string{c.Value, c.Text})
			}
			out = append(out, row)
		}
		return out
	}
	var out tables
	for _, d := range types {
		out.Types = append(out.Types, typeRow{Key: d.Key, Label: d.Label, Hint: d.Hint, Important: d.Important,
			DefaultEnabled: d.DefaultEnabled, Options: opts(d.Options)})
	}
	out.Global = opts(global)
	return out
}

// phpScript gibt die Tabellen von PHP in der gemeinsamen Form aus. Argumente:
// common.php, die Konfiguration (JSON-Datei), die Empfängerliste (JSON-Datei).
const phpScript = `<?php
require $argv[1];
$config = json_decode(file_get_contents($argv[2]), true);
$keys = json_decode(file_get_contents($argv[3]), true);
if (!write_json_file(data_path($config, 'alarm_keys.json'), ['next_id' => 99, 'version' => 1, 'keys' => $keys])) {
    fwrite(STDERR, "alarm_keys.json nicht geschrieben\n");
    exit(1);
}
$opts = static function (array $specs): array {
    $out = [];
    foreach ($specs as $key => $spec) {
        $choices = [];
        foreach ($spec['choices'] as $value => $text) {
            $choices[] = [(string)$value, (string)$text];
        }
        $out[] = ['key' => (string)$key, 'label' => $spec['label'], 'default' => (string)$spec['default'], 'choices' => $choices];
    }
    return $out;
};
$types = [];
foreach (admin_notify_types($config) as $key => $def) {
    $types[] = ['key' => (string)$key, 'label' => $def['label'], 'hint' => $def['hint'], 'important' => $def['important'],
        'default_enabled' => $def['default_enabled'], 'options' => $opts($def['options'])];
}
echo json_encode(['types' => $types, 'global' => $opts(admin_notify_global_specs($config))]);
`

// phpTables führt phpScript mit der Konfiguration cfg (dazu ein eigener,
// leerer Datenordner) und der Empfängerliste aus.
func phpTables(t *testing.T, php, common string, cfg map[string]any, recipients []model.Recipient) tables {
	t.Helper()
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	if err := os.Mkdir(data, 0o700); err != nil {
		t.Fatal(err)
	}
	// data_dir muss mit "/" beginnen: unter Windows die laufwerks-relative
	// Form; PHP läuft dafür im Ordner dir (dasselbe Laufwerk).
	phpData := filepath.ToSlash(data)
	if vol := filepath.VolumeName(data); vol != "" {
		phpData = strings.TrimPrefix(phpData, filepath.ToSlash(vol))
	}
	full := map[string]any{"data_dir": phpData}
	for k, v := range cfg {
		full[k] = v
	}
	keys := []map[string]any{}
	for _, r := range recipients {
		keys = append(keys, map[string]any{"id": r.ID, "key": r.Key, "label": r.Label, "muted": false,
			"mute_token": r.MuteToken})
	}
	write := func(name string, v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	script := filepath.Join(dir, "tables.php")
	if err := os.WriteFile(script, []byte(phpScript), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(php, "-n", script, common, write("config.json", full), write("keys.json", keys))
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("php: %v\n%s\n%s", err, out, stderr)
	}
	var tb tables
	if err := json.Unmarshal(out, &tb); err != nil {
		t.Fatalf("Ausgabe von PHP: %v\n%s", err, out)
	}
	return tb
}

// firstDifference zeigt die Zeilen um die erste Abweichung.
func firstDifference(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			from := max(0, i-6)
			return "PHP: …\n" + strings.Join(w[from:min(len(w), i+3)], "\n") + "\nGo: …\n" + strings.Join(g[from:min(len(g), i+3)], "\n")
		}
	}
	return ""
}
