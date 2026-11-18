package dashapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"fwalarm/internal/config"
	"fwalarm/internal/model"
	"fwalarm/internal/mutes"
)

// EINE Tabelle: was die Listen-Version erhöht – genau das, was die Box kennt
// (CLAUDE.md: Empfängerliste, Arbeitsmodus, Dauerhaft stumm, Demo-Modus). Jede
// Erhöhung geht über die Version hinaus, die die Box zuletzt gemeldet hat (PHP
// bump_keys_version) – jede Zeile läuft ohne Box und mit einer Box, die mehr
// meldet, als der Server hat. Dazu: Was abgelehnt wird oder ohne Wechsel
// bleibt, ändert nichts – keine Version, kein Feld, kein Verlaufseintrag.
func TestWhichActionsBump(t *testing.T) {
	type row struct {
		name  string
		setup func(d *model.State) // vor dem Aufruf (Anna id 1, Bert id 2, Version 10)
		kv    []string             // POST an dashboard_api.php …
		call  func(b *bench) string
		bump  bool
		// nothing: abgelehnt oder ohne Wechsel – nichts gespeichert, kein
		// Verlaufseintrag; die Antwort sagt ok=false oder changed=false.
		nothing bool
		// annaMuted: der Stumm-Zustand von Anna danach, wo die Zeile ihn festlegt.
		annaMuted *bool
	}
	yes, no := true, false
	muteAnna := func(d *model.State) { d.Recipients[0].Muted, d.Recipients[0].MutedAt = true, t0.Unix() }
	overdueAnna := func(d *model.State) { d.Recipients[0].Muted, d.Recipients[0].MutedAt = true, 1 }
	permAnna := func(d *model.State) { muteAnna(d); d.Recipients[0].MutePermanent = true }
	demoAnna := func(d *model.State) { d.Demo = model.Demo{Enabled: true, KeyID: 1, Key: anna.Key, Label: "Anna"} }
	onlyAnna := func(d *model.State) { d.Recipients = d.Recipients[:1] }
	tenRecipients := func(d *model.State) {
		for id := 3; id <= 10; id++ {
			d.Recipients = append(d.Recipients, model.Recipient{ID: id, Key: fmt.Sprintf("fullkey%05d", id),
				Label: fmt.Sprintf("E%d", id), MuteToken: fmt.Sprintf("fulltoken%03d", id)})
		}
		d.NextID = 11
	}
	expire := func(b *bench) string {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := mutes.Expire(ctx, b.c); err != nil {
			b.t.Fatalf("Expire: %v", err)
		}
		return ""
	}
	keysList := func(b *bench) string {
		raw, _ := b.admin().list()
		return raw
	}
	rows := []row{
		// Was die Box kennt: neue Version.
		{name: "keys_add", kv: []string{"action", "keys_add", "label", "Cara", "key", "carakey00003"}, bump: true},
		{name: "keys_delete", kv: []string{"action", "keys_delete", "id", "2"}, bump: true},
		{name: "keys_replace_key", kv: []string{"action", "keys_replace_key", "id", "1", "key", "neuekey00001"}, bump: true},
		{name: "keys_mute stumm", kv: []string{"action", "keys_mute", "id", "1", "muted", "1"}, bump: true, annaMuted: &yes},
		{name: "keys_mute laut", setup: muteAnna, kv: []string{"action", "keys_mute", "id", "1", "muted", "0"}, bump: true, annaMuted: &no},
		{name: "keys_mute laut beendet dauerhaft", setup: permAnna, kv: []string{"action", "keys_mute", "id", "1", "muted", "0"}, bump: true, annaMuted: &no},
		{name: "keys_set_mute_volume", kv: []string{"action", "keys_set_mute_volume", "id", "1", "volume", "3"}, bump: true},
		{name: "demo_set an", kv: []string{"action", "demo_set", "enabled", "1", "id", "1"}, bump: true},
		{name: "demo_set aus", setup: demoAnna, kv: []string{"action", "demo_set", "enabled", "0"}, bump: true},
		{name: "demo_set aus, obwohl aus (Reparaturweg)", kv: []string{"action", "demo_set", "enabled", "0"}, bump: true},
		{name: "mutes.Expire mit Überfälligem", setup: overdueAnna, call: expire, bump: true, annaMuted: &no},
		{name: "keys_list mit Überfälligem", setup: overdueAnna, call: keysList, bump: true, annaMuted: &no},
		// "Dauerhaft" ist reine Server-Logik: neue Version nur, wenn sich
		// dabei stumm/laut ändert – je Ausgangslage festgelegt.
		{name: "dauerhaft: laut → dauerhaft (wird stumm)", kv: []string{"action", "keys_set_mute_permanent", "id", "1", "permanent", "1"}, bump: true, annaMuted: &yes},
		{name: "dauerhaft: stumm → dauerhaft (bleibt stumm)", setup: muteAnna, kv: []string{"action", "keys_set_mute_permanent", "id", "1", "permanent", "1"}, bump: false, annaMuted: &yes},
		{name: "dauerhaft: beenden (bleibt stumm, wie PHP)", setup: permAnna, kv: []string{"action", "keys_set_mute_permanent", "id", "1", "permanent", "0"}, bump: false, annaMuted: &yes},
		// Was die Box nicht kennt: keine neue Version.
		{name: "keys_rename", kv: []string{"action", "keys_rename", "id", "1", "label", "Anna B."}},
		{name: "keys_rename des Demo-Empfängers", setup: demoAnna, kv: []string{"action", "keys_rename", "id", "1", "label", "Anna B."}},
		{name: "keys_set_phone", kv: []string{"action", "keys_set_phone", "id", "1", "phone", "0171 1"}},
		{name: "keys_new_mute_token", kv: []string{"action", "keys_new_mute_token", "id", "1"}},
		{name: "settings_set", kv: []string{"action", "settings_set", "key", "mute_max_seconds", "value", "7200"}},
		{name: "notify_set", kv: []string{"action", "notify_set", "type", "login_success", "key", "enabled", "value", "1"}},
		// Ohne Wechsel: nichts.
		{name: "mutes.Expire ohne Überfälliges", setup: muteAnna, call: expire, nothing: true, annaMuted: &yes},
		{name: "keys_list ohne Überfälliges", setup: muteAnna, call: keysList, nothing: true, annaMuted: &yes},
		{name: "keys_mute ohne Wechsel", kv: []string{"action", "keys_mute", "id", "1", "muted", "0"}, nothing: true},
		{name: "keys_set_mute_volume ohne Wechsel", kv: []string{"action", "keys_set_mute_volume", "id", "1", "volume", "0"}, nothing: true},
		{name: "keys_replace_key ohne Wechsel", kv: []string{"action", "keys_replace_key", "id", "1", "key", anna.Key}, nothing: true},
		{name: "keys_set_mute_permanent ohne Wechsel", kv: []string{"action", "keys_set_mute_permanent", "id", "1", "permanent", "0"}, nothing: true},
		{name: "keys_rename ohne Wechsel", kv: []string{"action", "keys_rename", "id", "1", "label", "Anna"}, nothing: true},
		// Abgelehnt, weil es an der gespeicherten Liste hängt: nichts.
		{name: "abgelehnt: letzter Empfänger", setup: onlyAnna, kv: []string{"action", "keys_delete", "id", "1"}, nothing: true},
		{name: "abgelehnt: aktiven Demo-Empfänger löschen", setup: demoAnna, kv: []string{"action", "keys_delete", "id", "1"}, nothing: true},
		{name: "abgelehnt: Key des aktiven Demo-Empfängers ersetzen", setup: demoAnna, kv: []string{"action", "keys_replace_key", "id", "1", "key", "neuekey00001"}, nothing: true},
		{name: "abgelehnt: höchstens 10", setup: tenRecipients, kv: []string{"action", "keys_add", "label", "Elf", "key", "elfkey000011"}, nothing: true},
		{name: "abgelehnt: doppelter Key beim Anlegen", kv: []string{"action", "keys_add", "label", "Doppelt", "key", bert.Key}, nothing: true},
		{name: "abgelehnt: doppelter Key beim Ersetzen", kv: []string{"action", "keys_replace_key", "id", "1", "key", bert.Key}, nothing: true},
		{name: "abgelehnt: Demo-Empfänger nicht in der Liste", kv: []string{"action", "demo_set", "enabled", "1", "id", "99"}, nothing: true},
		{name: "abgelehnt: unbekannte id", kv: []string{"action", "keys_delete", "id", "9"}, nothing: true},
	}
	// fingerprint: alles, was eine Änderung der Liste, des Demo-Modus oder der
	// Einstellungen berühren könnte.
	fingerprint := func(st *model.State) string {
		b, _ := json.Marshal([]any{st.KeysVersion, st.NextID, st.Recipients, st.Demo, st.Settings, st.Notify})
		return string(b)
	}
	for _, r := range rows {
		for _, boxV := range []int{0, 40} { // 0 = keine Box gesehen; 40 = Box meldet mehr als der Server hat
			t.Run(fmt.Sprintf("%s/Box %d", r.name, boxV), func(t *testing.T) {
				b := newBench(t)
				b.setCfg(func(c *config.Config) { c.MuteMaxSeconds = 43200 })
				b.seed(10, anna, bert)
				if r.setup != nil {
					b.update(r.setup)
				}
				if boxV > 0 {
					b.setBox(&model.BoxStatus{SeenAt: t0.Unix(), Fields: model.Fields{{Name: "keys_version", Value: strconv.Itoa(boxV)}}})
				}
				before := fingerprint(b.c.Store.State())
				entries := len(b.c.Store.History(1000, nil))
				var body string
				if r.call != nil {
					body = r.call(b)
				} else {
					var code int
					code, body = b.admin().do(r.kv...)
					switch {
					case code != 200:
						t.Fatalf("%d %s", code, body)
					case r.nothing && !strings.Contains(body, `"ok":false`) && !strings.Contains(body, `"changed":false`):
						t.Fatalf("weder abgelehnt noch ohne Wechsel: %s", body)
					case !r.nothing && !strings.Contains(body, `"ok":true`):
						t.Fatalf("abgelehnt: %s", body)
					}
				}
				st := b.c.Store.State()
				want := 10
				if r.bump {
					want = max(10, boxV) + 1
				}
				if st.KeysVersion != want {
					t.Errorf("Version %d, erwartet %d (%s)", st.KeysVersion, want, body)
				}
				if strings.Contains(body, `"version":`) && !strings.Contains(body, `"version":`+strconv.Itoa(want)) {
					t.Errorf("Antwort nennt eine andere Version: %s", body)
				}
				if r.annaMuted != nil && st.Recipients[0].Muted != *r.annaMuted {
					t.Errorf("Anna stumm: %v, erwartet %v", st.Recipients[0].Muted, *r.annaMuted)
				}
				if r.nothing {
					if after := fingerprint(st); after != before {
						t.Errorf("geändert:\n%s\nvorher\n%s", after, before)
					}
					if n := len(b.c.Store.History(1000, nil)); n != entries {
						t.Errorf("%d neue Verlaufseinträge", n-entries)
					}
				}
			})
		}
	}
}
