package model

import "testing"

// Die Listen-Version, die die Box zuletzt gemeldet hat, zählt bei jeder
// Erhöhung mit (PHP bump_keys_version) – aber nur eine plausible.
func TestBoxKeysVersion(t *testing.T) {
	with := func(v string) *BoxStatus {
		return &BoxStatus{SeenAt: 1, Fields: Fields{{Name: "device_id", Value: "fw-box-1"}, {Name: "keys_version", Value: v}}}
	}
	cases := []struct {
		name string
		box  *BoxStatus
		want int
	}{
		{"nie gemeldet", nil, 0},
		{"ohne Feld", &BoxStatus{SeenAt: 1, Fields: Fields{{Name: "device_id", Value: "x"}}}, 0},
		{"leer", with(""), 0},
		{"Zahl", with("41"), 41},
		{"wie PHP (int): führende Ziffern", with("12abc"), 12},
		{"wie PHP (int): Leerraum davor", with(" 7"), 7},
		{"Kommazahl abgeschnitten", with("9.9"), 9},
		{"negativ zählt nicht", with("-5"), 0},
		{"größte plausible", with("2147483647"), 2147483647},
		{"ab 2^31 zählt nicht", with("2147483648"), 0},
		{"riesig zählt nicht", with("99999999999999999999"), 0},
		{"Unsinn", with("abc"), 0},
	}
	for _, tc := range cases {
		if got := BoxKeysVersion(tc.box); got != tc.want {
			t.Errorf("%s: %d, erwartet %d", tc.name, got, tc.want)
		}
	}
	// Mit einer riesigen Zahl der Box läuft die Version nie über.
	s := NewState()
	s.KeysVersion = 5
	if v := s.Bump(BoxKeysVersion(with("9223372036854775807"))); v != 6 {
		t.Errorf("Bump mit unplausibler Box-Version: %d", v)
	}
}
