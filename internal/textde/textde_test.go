package textde

import (
	"testing"
	"time"
	_ "time/tzdata" // Zeitzonen unabhängig vom Rechner, auf dem der Test läuft
)

func berlin(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestStamp(t *testing.T) {
	loc := berlin(t)
	for _, tc := range []struct{ in, want string }{
		{"2026-07-01T10:00:00Z", "[01.07.2026 12:00] "}, // Sommerzeit
		{"2026-12-01T10:00:00Z", "[01.12.2026 11:00] "}, // Winterzeit
		{"2026-11-16T09:05:59Z", "[16.11.2026 10:05] "}, // Sekunden fallen weg
	} {
		in, err := time.Parse(time.RFC3339, tc.in)
		if err != nil {
			t.Fatal(err)
		}
		if got := Stamp(in, loc); got != tc.want {
			t.Errorf("Stamp(%s) = %q, erwartet %q", tc.in, got, tc.want)
		}
	}
	// Ohne Zeitzone: UTC statt Absturz.
	in := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	if got := Stamp(in, nil); got != "[01.07.2026 10:00] " {
		t.Errorf("Stamp ohne Zeitzone = %q", got)
	}
}

func TestOutageDuration(t *testing.T) {
	// PHP: outage_duration_text() – ab 120 min in Stunden, kaufmännisch gerundet.
	for _, tc := range []struct {
		in   int
		want string
	}{
		{0, "0 min"},
		{1, "1 min"},
		{119, "119 min"},
		{120, "2 Std"},
		{149, "2 Std"},
		{150, "3 Std"},
		{1439, "24 Std"},
	} {
		if got := OutageDuration(tc.in); got != tc.want {
			t.Errorf("OutageDuration(%d) = %q, erwartet %q", tc.in, got, tc.want)
		}
	}
}

func TestDurationLabel(t *testing.T) {
	// PHP: notify_duration_label() – volle Stunden, sonst volle Minuten, sonst Sekunden.
	for _, tc := range []struct {
		in   int
		want string
	}{
		{3600, "1 Std"},
		{43200, "12 Std"},
		{600, "10 Min"},
		{5400, "90 Min"},
		{90, "90 s"},
		{1, "1 s"},
		{0, "0 s"},
		{-60, "-60 s"},
	} {
		if got := DurationLabel(tc.in); got != tc.want {
			t.Errorf("DurationLabel(%d) = %q, erwartet %q", tc.in, got, tc.want)
		}
	}
}
