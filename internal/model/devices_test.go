package model

import (
	"fmt"
	"testing"
)

// DeviceHint: die Hinweise von PHP device_hint() (common.php 1041-1071)
// wörtlich, config.php heißt hier config.toml. Die Boards erscheinen nach
// ihrem letzten Push geordnet, das jüngste zuerst; ein Board schweigt nach
// max(180, offline_after_seconds) Sekunden.
func TestDeviceHintTexts(t *testing.T) {
	boards := func(ages map[string]int64) *Runtime {
		r := NewRuntime()
		for id, age := range ages {
			r.Devices[id] = Device{SeenAt: t0.Unix() - age, FwBuild: "x"}
		}
		return r
	}
	const several = "Mehrere Boards melden sich mit demselben Maschinen-Token (%s). Anzeige und Offline-Wächter sehen nur den jeweils letzten Push – fällt eins aus, merkt das niemand. In config.toml box_device_id auf die echte Box setzen."
	const silent = "ACHTUNG: %s meldet sich seit %s nicht mehr, nur noch %s (gleiches Maschinen-Token). Ist das die echte Box, ist sie AUSGEFALLEN – Anzeige und Offline-Wächter sehen nur das andere Board und melden nichts. In config.toml box_device_id auf die echte Box setzen."
	const foreign = "Ein fremdes Board meldet sich mit dem Maschinen-Token der Box (%s, zuletzt vor %s) – seine Pushs werden ignoriert."
	const never = "Die eingestellte Box „%s“ (box_device_id) hat sich noch nie gemeldet, wohl aber %s – stimmt box_device_id in config.toml mit REMOTE_DEVICE_ID in config.h der Box überein?"
	cases := []struct {
		name         string
		rt           *Runtime
		boxID        string
		offlineAfter int
		want         string
	}{
		{"nie ein Push", NewRuntime(), "", 180, ""},
		{"ohne Laufzeitdaten", nil, "", 180, ""},
		{"ein Board", boards(map[string]int64{"fw-box-1": 5}), "", 180, ""},
		{"zwei Boards, beide aktiv", boards(map[string]int64{"fw-box-1": 5, "bastel-1": 2}), "", 180,
			fmt.Sprintf(several, "„bastel-1“, „fw-box-1“")},
		{"drei Boards, Reihenfolge nach letztem Push", boards(map[string]int64{"c": 30, "a": 10, "b": 20}), "", 180,
			fmt.Sprintf(several, "„a“, „b“, „c“")},
		{"eins schweigt seit 15 min", boards(map[string]int64{"fw-box-1": 900, "bastel-1": 2}), "", 180,
			fmt.Sprintf(silent, "„fw-box-1“", "15 Min", "„bastel-1“")},
		{"Schwelle: offline_after_seconds, wenn größer als 180", boards(map[string]int64{"fw-box-1": 400, "bastel-1": 2}), "", 600,
			fmt.Sprintf(several, "„bastel-1“, „fw-box-1“")},
		{"Schwelle: mindestens 180 s", boards(map[string]int64{"fw-box-1": 181, "bastel-1": 2}), "", 60,
			fmt.Sprintf(silent, "„fw-box-1“", "3 Min", "„bastel-1“")},
		{"Schwelle: 0 heißt 180 s", boards(map[string]int64{"fw-box-1": 181, "bastel-1": 2}), "", 0,
			fmt.Sprintf(silent, "„fw-box-1“", "3 Min", "„bastel-1“")},
		{"genau an der Schwelle schweigt es noch nicht", boards(map[string]int64{"fw-box-1": 180, "bastel-1": 2}), "", 180,
			fmt.Sprintf(several, "„bastel-1“, „fw-box-1“")},
		{"alle schweigen: kein ACHTUNG", boards(map[string]int64{"fw-box-1": 900, "bastel-1": 800}), "", 180,
			fmt.Sprintf(several, "„bastel-1“, „fw-box-1“")},
		{"älter als 7 Tage zählt nicht", boards(map[string]int64{"fw-box-1": 5, "bastel-1": 7*86400 + 1}), "", 180, ""},
		{"genau 7 Tage zählt noch", boards(map[string]int64{"fw-box-1": 5, "bastel-1": 7 * 86400}), "", 180,
			fmt.Sprintf(silent, "„bastel-1“", "7 Tagen", "„fw-box-1“")},
		{"box_device_id: fremdes Board vor 2 Std", boards(map[string]int64{"fw-box-1": 5, "bastel-1": 7200}), "fw-box-1", 180,
			fmt.Sprintf(foreign, "„bastel-1“", "2 Std")},
		{"box_device_id: zwei fremde, zuletzt vor dem jüngsten", boards(map[string]int64{"fw-box-1": 5, "x": 600, "y": 300}), "fw-box-1", 180,
			fmt.Sprintf(foreign, "„y“, „x“", "5 Min")},
		{"box_device_id: eingestellte Box nie gesehen", boards(map[string]int64{"bastel-1": 60}), "fw-box-1", 180,
			fmt.Sprintf(never, "fw-box-1", "„bastel-1“")},
		{"box_device_id mit Leerraum", boards(map[string]int64{"bastel-1": 60}), " fw-box-1 ", 180,
			fmt.Sprintf(never, "fw-box-1", "„bastel-1“")},
		{"box_device_id: fremdes Board älter als 24 Std", boards(map[string]int64{"fw-box-1": 5, "bastel-1": 86401}), "fw-box-1", 180, ""},
		{"box_device_id: fremdes Board genau 24 Std", boards(map[string]int64{"fw-box-1": 5, "bastel-1": 86400}), "fw-box-1", 180,
			fmt.Sprintf(foreign, "„bastel-1“", "24 Std")},
		{"box_device_id: nur die eingestellte Box", boards(map[string]int64{"fw-box-1": 5}), "fw-box-1", 180, ""},
		{"box_device_id: die eingestellte Box ist alt, kein Fremder", boards(map[string]int64{"fw-box-1": 30 * 86400}), "fw-box-1", 180, ""},
		{"box_device_id: nichts gesehen", NewRuntime(), "fw-box-1", 180, ""},
	}
	for _, tc := range cases {
		if got := DeviceHint(tc.rt, tc.boxID, t0, tc.offlineAfter); got != tc.want {
			t.Errorf("%s:\n %q\nerwartet\n %q", tc.name, got, tc.want)
		}
	}
}

// Die Dauer im Hinweis (PHP: $duration in device_hint): Minuten gerundet,
// mindestens 1; ab 120 Min Stunden, ab 2 880 Min Tage.
func TestHintDuration(t *testing.T) {
	for seconds, want := range map[int64]string{
		-50: "1 Min", 0: "1 Min", 29: "1 Min", 89: "1 Min", 90: "2 Min", 900: "15 Min",
		119*60 + 29: "119 Min", 119*60 + 30: "2 Std", 150 * 60: "3 Std", 2879 * 60: "48 Std",
		2880 * 60: "2 Tagen", 7 * 86400: "7 Tagen", 3*86400 + 12*3600: "4 Tagen",
	} {
		if got := hintDuration(seconds); got != want {
			t.Errorf("%d s: %q, erwartet %q", seconds, got, want)
		}
	}
}
