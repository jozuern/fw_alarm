package model

import (
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Der Hinweis auf mehrere bzw. fremde Boards mit dem Maschinen-Token der Box
// (PHP: device_hint in lib/common.php) – aus der Liste der Boards, die der
// Status-Push führt (EvaluatePush, rememberBoard).

// hintBoard: ein Board mit dem Alter seines letzten Pushs (für den Hinweis).
type hintBoard struct {
	id  string
	age int64
}

// DeviceHint ist der Hinweis für das Dashboard, wenn sich mehrere bzw.
// fremde Boards mit dem Maschinen-Token melden ("" = alles in Ordnung; PHP:
// device_hint, die Texte wörtlich; "config.php" heißt hier "config.toml").
// Ein Board schweigt nach max(180, offlineAfter) Sekunden (offlineAfter:
// offline_after_seconds der Konfiguration). Die Kennzeichen stammen von den
// Boards selbst – der Text ist reiner Text (das Dashboard setzt ihn nie als
// HTML). Die Boards erscheinen nach ihrem letzten Push, das jüngste zuerst.
//
//   - box_device_id gesetzt: Hat sich die eingestellte Box nie gemeldet,
//     wohl aber andere in den letzten 24 h – die Frage, ob die Kennzeichen
//     übereinstimmen. Sonst: andere Boards in den letzten 24 h – ihre Pushs
//     werden ignoriert. Sonst nichts.
//   - Ohne box_device_id: Haben sich in den letzten 7 Tagen mehrere Boards
//     gemeldet, bleibt der Hinweis stehen – auch wenn eins davon inzwischen
//     schweigt. Genau das ist der gefährliche Fall (die echte Box ist
//     ausgefallen, nur das Bastel-Board pusht weiter, Anzeige und Wächter
//     sehen "online"): Schweigt eins, während ein anderes weiterpusht, sagt
//     er es mit ACHTUNG.
func DeviceHint(rt *Runtime, boxDeviceID string, now time.Time, offlineAfter int) string {
	expected := phpTrim(boxDeviceID)
	var devices map[string]Device
	if rt != nil {
		devices = rt.Devices
	}
	expectedSeen := false
	var foreign, recent []hintBoard // fremde der letzten 24 h, alle der letzten 7 Tage
	for _, id := range slices.SortedFunc(maps.Keys(devices), newestFirst(devices, "", func(x Device) int64 { return x.SeenAt })) {
		b := hintBoard{id: id, age: now.Unix() - devices[id].SeenAt}
		if expected != "" && expected == id {
			expectedSeen = true
		} else if b.age <= 86400 {
			foreign = append(foreign, b)
		}
		if b.age <= 7*86400 {
			recent = append(recent, b)
		}
	}
	if expected != "" {
		switch {
		case !expectedSeen && len(foreign) > 0:
			return "Die eingestellte Box „" + expected + "“ (box_device_id) hat sich noch nie gemeldet, " +
				"wohl aber " + hintNames(foreign) + " – stimmt box_device_id in config.toml " +
				"mit REMOTE_DEVICE_ID in config.h der Box überein?"
		case len(foreign) > 0:
			return "Ein fremdes Board meldet sich mit dem Maschinen-Token der Box (" + hintNames(foreign) +
				", zuletzt vor " + hintDuration(minAge(foreign)) + ") – seine Pushs werden ignoriert."
		}
		return ""
	}
	if len(recent) < 2 {
		return ""
	}
	silentAfter := int64(max(180, offlineAfter))
	var silent, pushing []hintBoard
	for _, b := range recent {
		if b.age > silentAfter {
			silent = append(silent, b)
		} else {
			pushing = append(pushing, b)
		}
	}
	if len(silent) > 0 && len(pushing) > 0 {
		return "ACHTUNG: " + hintNames(silent) + " meldet sich seit " + hintDuration(minAge(silent)) +
			" nicht mehr, nur noch " + hintNames(pushing) + " (gleiches Maschinen-Token). " +
			"Ist das die echte Box, ist sie AUSGEFALLEN – Anzeige und Offline-Wächter sehen nur das " +
			"andere Board und melden nichts. In config.toml box_device_id auf die echte Box setzen."
	}
	return "Mehrere Boards melden sich mit demselben Maschinen-Token (" + hintNames(recent) +
		"). Anzeige und Offline-Wächter sehen nur den jeweils letzten Push – fällt eins aus, merkt das " +
		"niemand. In config.toml box_device_id auf die echte Box setzen."
}

// hintNames: „a“, „b“ (PHP: $names in device_hint).
func hintNames(list []hintBoard) string {
	ids := make([]string, len(list))
	for i, b := range list {
		ids[i] = b.id
	}
	return "„" + strings.Join(ids, "“, „") + "“"
}

// minAge: das kleinste Alter (die Liste ist nie leer).
func minAge(list []hintBoard) int64 {
	m := list[0].age
	for _, b := range list[1:] {
		m = min(m, b.age)
	}
	return m
}

// hintDuration: eine Dauer für den Hinweis (PHP: $duration in device_hint):
// Minuten gerundet, mindestens 1; ab 120 Min in Stunden, ab 2 880 Min in
// Tagen. Gerundet wird wie PHPs round (halbe weg von der Null).
func hintDuration(seconds int64) string {
	minutes := max(1, int64(math.Round(float64(seconds)/60)))
	switch {
	case minutes < 120:
		return strconv.FormatInt(minutes, 10) + " Min"
	case minutes < 2880:
		return strconv.FormatInt(int64(math.Round(float64(minutes)/60)), 10) + " Std"
	}
	return strconv.FormatInt(int64(math.Round(float64(minutes)/1440)), 10) + " Tagen"
}
