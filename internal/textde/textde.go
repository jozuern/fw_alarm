// Package textde formt Zeiten und Dauern für deutsche Texte – genau wie die
// gleichnamigen Stellen der PHP-Fassung, damit Meldungen zeichengleich
// bleiben.
package textde

import (
	"strconv"
	"time"
)

// Stamp liefert den Zeitstempel vor den Meldungen des Wächters,
// z.B. "[16.11.2026 10:05] " (PHP: date('[d.m.Y H:i] ')). loc nil = UTC.
func Stamp(t time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("[02.01.2006 15:04] ")
}

// DurationLabel beschreibt eine Dauer in Sekunden: volle Stunden als
// "N Std", volle Minuten als "N Min", sonst "N s"
// (PHP: notify_duration_label).
func DurationLabel(seconds int) string {
	switch {
	case seconds > 0 && seconds%3600 == 0:
		return strconv.Itoa(seconds/3600) + " Std"
	case seconds > 0 && seconds%60 == 0:
		return strconv.Itoa(seconds/60) + " Min"
	default:
		return strconv.Itoa(seconds) + " s"
	}
}

// OutageDuration beschreibt die Dauer eines Ausfalls für Bark-Texte (ASCII):
// bis 119 Minuten "N min", ab 120 in Stunden, kaufmännisch gerundet
// (PHP: outage_duration_text).
func OutageDuration(minutes int) string {
	if minutes >= 120 {
		return strconv.Itoa((minutes+30)/60) + " Std"
	}
	return strconv.Itoa(minutes) + " min"
}
