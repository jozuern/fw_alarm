package dashapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"fwalarm/internal/config"
	"fwalarm/internal/model"
	"fwalarm/internal/store/storetest"
)

// Erfundene Empfänger (Keys und Tokens im Format, das die Prüfung annimmt).
var (
	anna = model.Recipient{ID: 1, Key: "annakey00001", Label: "Anna", MuteToken: "annatoken001"}
	bert = model.Recipient{ID: 2, Key: "bertkey00002", Label: "Bert", MuteToken: "berttoken002"}
	cara = model.Recipient{ID: 3, Key: "carakey00003", Label: "Cara", MuteToken: "caratoken003"}
)

// person: eine angemeldete Sitzung des Prüfstands.
type person struct {
	b            *bench
	cookie, csrf string
}

func (b *bench) admin() person {
	b.t.Helper()
	ck, s := b.login("admin", "admin")
	return person{b, ck, s.CSRF}
}

func (b *bench) reader() person {
	b.t.Helper()
	ck, s := b.login("leser", "readonly")
	return person{b, ck, s.CSRF}
}

// do: ein POST an dashboard_api.php (Felder als Paare Name, Wert); Status und
// Rumpf.
func (p person) do(kv ...string) (int, string) {
	p.b.t.Helper()
	form := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		form.Set(kv[i], kv[i+1])
	}
	rec := p.b.serve(req("POST", "", form, p.cookie, [2]string{"X-CSRF-Token", p.csrf}))
	return rec.Code, rec.Body.String()
}

// want: Status 200 und genau dieser Rumpf (lesbar geschrieben, PHPs \uXXXX
// über phpText).
func (p person) want(body string, kv ...string) {
	p.b.t.Helper()
	code, got := p.do(kv...)
	if code != 200 || got != phpText(body) {
		p.b.t.Errorf("%v: %d %s\nerwartet %s", kv, code, got, phpText(body))
	}
}

// list: die Antwort von keys_list (Rumpf roh und gelesen).
func (p person) list() (string, map[string]any) {
	p.b.t.Helper()
	rec := p.b.serve(req("GET", "action=keys_list", nil, p.cookie))
	var v map[string]any
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &v) != nil {
		p.b.t.Fatalf("keys_list: %d %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String(), v
}

// seed legt die Liste unter Version v an.
func (b *bench) seed(v int, rs ...model.Recipient) {
	b.t.Helper()
	b.update(func(d *model.State) {
		maxID := 0
		for _, r := range rs {
			maxID = max(maxID, r.ID)
		}
		d.NextID = maxID + 1
		d.KeysVersion = v
		d.Recipients = rs
	})
}

// events: die Verlaufseinträge dieses Ereignisses, neueste zuerst.
func (b *bench) events(event string) []model.Entry {
	return b.c.Store.History(100, func(e model.Entry) bool { return e.Str("event") == event })
}

// fields: die Felder eines Eintrags ohne log_at und uid – zum Vergleichen.
func fields(e model.Entry) string {
	out := []string{}
	for _, k := range []string{"event", "by", "label", "key", "old_label", "old_key", "source", "permanent", "volume", "version", "value", "phone", "mute_token"} {
		if v, ok := e[k]; ok {
			out = append(out, fmt.Sprintf("%s=%v", k, v))
		}
	}
	return strings.Join(out, " ")
}

// keys_list: Felder je Empfänger in PHPs Reihenfolge; muted wirksam,
// mute_overdue, mute_until (nicht bei "dauerhaft"); Telefonnummer und Token
// nur für den Admin. Zuerst läuft Expire – scheitert es, bleibt die Liste
// sichtbar, die Zeile zeigt "überfällig".
func TestKeysListAnswer(t *testing.T) {
	b := newBench(t)
	b.setCfg(func(c *config.Config) { c.MuteMaxSeconds = 43200 })
	now := t0.Unix()
	a, bb, c := anna, bert, cara
	a.Muted, a.MutedAt, a.MuteVolume, a.Phone = true, now-100, 3, "+49 171 1234567"
	bb.Muted, bb.MutedAt, bb.MutePermanent = true, 5, true
	c.Muted, c.MutedAt = true, now-50000 // überfällig
	b.seed(4, a, bb, c)
	b.fs.Fail(filepath.Join(benchDir, "state.json.tmp"), errors.New("Platte voll"))
	admin, reader := b.admin(), b.reader()

	raw, _ := admin.list()
	want := `{"ok":true,"version":4,"keys":[` +
		fmt.Sprintf(`{"id":1,"label":"Anna","key_masked":"anna...","muted":true,"mute_overdue":false,"muted_at":%d,"mute_volume":3,"mute_permanent":false,"phone":"+49 171 1234567","mute_until":%d,"mute_token":"annatoken001"},`, now-100, now-100+43200) +
		`{"id":2,"label":"Bert","key_masked":"bert...","muted":true,"mute_overdue":false,"muted_at":5,"mute_volume":0,"mute_permanent":true,"phone":"","mute_token":"berttoken002"},` +
		fmt.Sprintf(`{"id":3,"label":"Cara","key_masked":"cara...","muted":false,"mute_overdue":true,"muted_at":%d,"mute_volume":0,"mute_permanent":false,"phone":"","mute_token":"caratoken003"}],`, now-50000) +
		`"demo":{"enabled":false,"label":"","key_id":0,"key_masked":"","changed_at":0},"mute_max_seconds":43200}`
	if raw != want {
		t.Errorf("Admin:\n%s\nerwartet\n%s", raw, want)
	}
	// Die Lese-Rolle: dieselbe Liste ohne Telefonnummer und Token – sie
	// stehen nirgends in der rohen Antwort.
	raw, _ = reader.list()
	for _, secret := range []string{"1234567", "annatoken001", "berttoken002", "caratoken003", `"phone"`, `"mute_token"`, "annakey00001"} {
		if strings.Contains(raw, secret) {
			t.Errorf("Lese-Rolle sieht %q: %s", secret, raw)
		}
	}
	if !strings.Contains(raw, `"mute_permanent":false,"mute_until":`) {
		t.Errorf("Lese-Rolle ohne mute_until: %s", raw)
	}

	// Gelingt das Sichern, ist Cara laut (neue Version) – und die Einstellung
	// im Dashboard gilt vor der Konfiguration.
	b.fs.Fail(filepath.Join(benchDir, "state.json.tmp"), nil)
	b.update(func(d *model.State) { d.Settings.MuteMaxSeconds = 7200 })
	_, v := admin.list()
	keys := v["keys"].([]any)
	if v["version"] != float64(5) || v["mute_max_seconds"] != float64(7200) {
		t.Errorf("Version/Höchstdauer: %v %v", v["version"], v["mute_max_seconds"])
	}
	if k := keys[2].(map[string]any); k["muted"] != false || k["mute_overdue"] != false || k["muted_at"] != float64(0) {
		t.Errorf("Cara: %v", k)
	}
	if k := keys[0].(map[string]any); k["mute_until"] != float64(now-100+7200) {
		t.Errorf("Anna mute_until: %v", k["mute_until"])
	}
	// Ohne Höchstdauer (Konfiguration 0, nichts eingestellt) kein mute_until.
	b.update(func(d *model.State) { d.Settings.MuteMaxSeconds = 0 })
	b.setCfg(func(c *config.Config) { c.MuteMaxSeconds = 0 })
	if raw, _ := admin.list(); strings.Contains(raw, "mute_until") || !strings.Contains(raw, `"mute_max_seconds":0}`) {
		t.Errorf("ohne Höchstdauer: %s", raw)
	}
}

// Notbetrieb: keys_list zeigt keine "leere" Liste, sondern code=keys_corrupt
// mit dem festen Satz; jede Änderung antwortet mit dem Satz zum Notbetrieb.
func TestKeysListEmergency(t *testing.T) {
	f := storetest.New()
	f.Put(filepath.Join(benchDir, "state.json"), []byte("{kaputt"))
	b := newBenchOn(t, f)
	if !b.c.Store.Emergency() {
		t.Fatal("kein Notbetrieb")
	}
	admin := b.admin()
	rec := b.serve(req("GET", "action=keys_list", nil, admin.cookie))
	want := phpText(`{"ok":false,"code":"keys_corrupt","message":"Empfängerliste nicht lesbar (Notbetrieb). Ein Alarm geht ` +
		`jetzt an die Not-Kopie des Servers bzw. die Not-Liste aus config.toml. Ausweg: Sicherung zurückspielen (server/README.md)."}`)
	if rec.Code != 200 || rec.Body.String() != want {
		t.Errorf("%d %s\nerwartet %s", rec.Code, rec.Body.String(), want)
	}
	emergency := `{"ok":false,"message":"Fehler: Notbetrieb – die Empfängerliste ist nicht lesbar, Änderungen sind gerade nicht möglich. Ausweg: Sicherung zurückspielen (server/README.md)."}`
	for _, kv := range [][]string{
		{"action", "keys_add", "label", "Anna", "key", "annakey00001"},
		{"action", "keys_delete", "id", "1"},
		{"action", "keys_mute", "id", "1", "muted", "1"},
		{"action", "demo_set", "enabled", "0"},
		{"action", "settings_set", "key", "mute_max_seconds", "value", "7200"},
	} {
		admin.want(emergency, kv...)
	}
	// Die Status-Teile zeigen die leere Liste (Version 0, LIVE).
	if got := b.statusOf(admin.cookie); !strings.Contains(got, `"demo":{"enabled":false,"label":"","key_id":0,"key_masked":"","changed_at":0},"nas_keys_version":0,"demo_sync_ok":false`) {
		t.Errorf("status: %s", got)
	}
}

// Anlegen und Löschen: Antworten und Verlauf wie PHP, neue Version, ids über
// die Hochwassermarke; was von der Liste abhängt, wird im Entwurf geprüft.
func TestKeysAddDelete(t *testing.T) {
	b := newBench(t)
	admin := b.admin()
	admin.want(`{"ok":true,"message":"Empfänger hinzugefügt. Die Box übernimmt die Liste nach wenigen Sekunden.","version":1}`,
		"action", "keys_add", "label", "  Anna  ", "key", " annakey00001 ")
	admin.want(`{"ok":true,"message":"Empfänger hinzugefügt. Die Box übernimmt die Liste nach wenigen Sekunden.","version":2}`,
		"action", "keys_add", "label", "Bert", "key", "bertkey00002", "phone", " 0171 555 ")
	st := b.c.Store.State()
	if len(st.Recipients) != 2 || st.Recipients[0] != (model.Recipient{ID: 1, Key: "annakey00001", Label: "Anna",
		AddedAt: t0.Unix(), MuteToken: st.Recipients[0].MuteToken}) || st.Recipients[1].Phone != "0171 555" ||
		!model.ValidMuteToken(st.Recipients[0].MuteToken) || st.Recipients[0].MuteToken == st.Recipients[1].MuteToken {
		t.Errorf("Liste: %+v", st.Recipients)
	}
	if got := fields(b.events(model.EvKeyAdded)[0]); got != "event=key_added by=admin label=Bert key=bert... version=2" {
		t.Errorf("Verlauf: %s", got)
	}

	for _, tc := range []struct {
		body string
		kv   []string
	}{
		{`{"ok":false,"message":"Name fehlt, ist länger als 40 Zeichen oder enthält ungültige Zeichen."}`, []string{"label", " ", "key", "carakey00003"}},
		{`{"ok":false,"message":"Name fehlt, ist länger als 40 Zeichen oder enthält ungültige Zeichen."}`, []string{"label", strings.Repeat("ä", 41), "key", "carakey00003"}},
		{`{"ok":false,"message":"Name fehlt, ist länger als 40 Zeichen oder enthält ungültige Zeichen."}`, []string{"label", "A\tB", "key", "carakey00003"}},
		{`{"ok":false,"message":"Ungültiger Bark-Key (erlaubt: 5-64 Zeichen, A-Z a-z 0-9 _ -)."}`, []string{"label", "Cara", "key", "abc"}},
		{`{"ok":false,"message":"Ungültige Telefonnummer (erlaubt: 3-30 Zeichen: Ziffern, + - / ( ) und Leerzeichen)."}`, []string{"label", "Cara", "key", "carakey00003", "phone", "abc"}},
		{`{"ok":false,"message":"Dieser Key ist bereits eingetragen."}`, []string{"label", "Doppelt", "key", "annakey00001"}},
	} {
		admin.want(tc.body, append([]string{"action", "keys_add"}, tc.kv...)...)
	}
	if st := b.c.Store.State(); len(st.Recipients) != 2 || st.KeysVersion != 2 {
		t.Fatalf("abgelehnt, aber geändert: %+v", st)
	}

	// Höchstens 10 Empfänger.
	for i := 3; i <= 10; i++ {
		if code, body := admin.do("action", "keys_add", "label", fmt.Sprintf("E%d", i), "key", fmt.Sprintf("validkey%05d", i)); !strings.Contains(body, `"ok":true`) {
			t.Fatalf("E%d: %d %s", i, code, body)
		}
	}
	admin.want(`{"ok":false,"message":"Maximal 10 Empfänger möglich."}`, "action", "keys_add", "label", "Elf", "key", "validkey00011")

	// Löschen: unbekannt, gelöscht (neue Version, key_removed), gelöschte ids
	// kommen nie wieder.
	admin.want(`{"ok":false,"message":"Empfänger nicht gefunden."}`, "action", "keys_delete", "id", "99")
	admin.want(`{"ok":true,"message":"Empfänger gelöscht. Die Box übernimmt die Liste nach wenigen Sekunden.","version":11}`,
		"action", "keys_delete", "id", "10")
	if got := fields(b.events(model.EvKeyRemoved)[0]); got != "event=key_removed by=admin label=E10 key=vali... version=11" {
		t.Errorf("Verlauf: %s", got)
	}
	admin.want(`{"ok":true,"message":"Empfänger hinzugefügt. Die Box übernimmt die Liste nach wenigen Sekunden.","version":12}`,
		"action", "keys_add", "label", "Neu", "key", "neukey000011")
	if st := b.c.Store.State(); st.Recipients[9].ID != 11 {
		t.Errorf("neue id %d, erwartet 11", st.Recipients[9].ID)
	}

	// Der letzte Empfänger bleibt; der Demo-Empfänger erst nach dem Demo-Modus.
	b.seed(20, anna, bert)
	b.update(func(d *model.State) { d.Demo = model.Demo{Enabled: true, KeyID: 2, Key: bert.Key, Label: "Bert"} })
	admin.want(`{"ok":false,"message":"Dieser Empfänger ist gerade der Demo-Empfänger. Zuerst den Demo-Modus beenden."}`,
		"action", "keys_delete", "id", "2")
	b.update(func(d *model.State) { d.Demo = model.Demo{} })
	admin.want(`{"ok":true,"message":"Empfänger gelöscht. Die Box übernimmt die Liste nach wenigen Sekunden.","version":21}`,
		"action", "keys_delete", "id", "2")
	admin.want(`{"ok":false,"message":"Der letzte Empfänger kann nicht gelöscht werden."}`, "action", "keys_delete", "id", "1")
}

// Umbenennen, Telefonnummer, neuer Kurzbefehl-Token: keine neue Version
// (die Box kennt nichts davon); Verlauf ohne Telefonnummer und ohne Token;
// die Antwort trägt keinen Token.
func TestKeysRenamePhoneToken(t *testing.T) {
	b := newBench(t)
	b.seed(7, anna, bert)
	admin := b.admin()

	admin.want(`{"ok":true,"changed":true,"message":"Name geändert.","version":7}`, "action", "keys_rename", "id", "1", "label", " Anna B. ")
	admin.want(`{"ok":true,"changed":false,"message":"Der Name ist bereits so eingetragen."}`, "action", "keys_rename", "id", "1", "label", "Anna B.")
	admin.want(`{"ok":false,"message":"Name fehlt, ist länger als 40 Zeichen oder enthält ungültige Zeichen."}`, "action", "keys_rename", "id", "1", "label", "")
	admin.want(`{"ok":false,"message":"Empfänger nicht gefunden."}`, "action", "keys_rename", "id", "9", "label", "X")
	if got := fields(b.events(model.EvKeyRenamed)[0]); got != "event=key_renamed by=admin label=Anna B. key=anna... old_label=Anna" {
		t.Errorf("Verlauf: %s", got)
	}

	admin.want(`{"ok":true,"changed":true,"message":"Telefonnummer gespeichert.","version":7}`, "action", "keys_set_phone", "id", "1", "phone", "+49 171 1234567")
	admin.want(`{"ok":true,"changed":false,"message":"Telefonnummer ist bereits so eingetragen."}`, "action", "keys_set_phone", "id", "1", "phone", " +49 171 1234567 ")
	admin.want(`{"ok":false,"message":"Ungültige Telefonnummer (erlaubt: 3-30 Zeichen: Ziffern, + - / ( ) und Leerzeichen)."}`, "action", "keys_set_phone", "id", "1", "phone", "12")
	admin.want(`{"ok":true,"changed":true,"message":"Telefonnummer gespeichert.","version":7}`, "action", "keys_set_phone", "id", "2", "phone", "0171 1")
	admin.want(`{"ok":true,"changed":true,"message":"Telefonnummer entfernt.","version":7}`, "action", "keys_set_phone", "id", "2", "phone", "")
	if got := fields(b.events(model.EvKeyPhoneChanged)[0]); got != "event=key_phone_changed by=admin label=Bert key=bert..." {
		t.Errorf("Verlauf: %s", got)
	}

	old := b.c.Store.State().Recipients[0].MuteToken
	code, body := admin.do("action", "keys_new_mute_token", "id", "1")
	want := phpText(`{"ok":true,"changed":true,"message":"Neue Kurzbefehl-Links erzeugt – die bisherigen Links funktionieren ab ` +
		`sofort nicht mehr. Die neuen Links kopieren und im Kurzbefehl des Empfängers eintragen.","version":7}`)
	if code != 200 || body != want {
		t.Errorf("%d %s", code, body)
	}
	tok := b.c.Store.State().Recipients[0].MuteToken
	if tok == old || !model.ValidMuteToken(tok) || strings.Contains(body, tok) {
		t.Errorf("Token %q (vorher %q)", tok, old)
	}
	admin.want(`{"ok":false,"message":"Empfänger nicht gefunden."}`, "action", "keys_new_mute_token", "id", "9")
	if got := fields(b.events(model.EvKeyTokenRenewed)[0]); got != "event=key_token_renewed by=admin label=Anna B. key=anna..." {
		t.Errorf("Verlauf: %s", got)
	}
	if v := b.c.Store.State().KeysVersion; v != 7 {
		t.Errorf("Version %d, erwartet 7", v)
	}
	// Nirgends im Verlauf: Telefonnummer, Token, ganzer Key.
	all, _ := json.Marshal(b.c.Store.History(100, nil))
	for _, secret := range []string{"1234567", "0171 1", old, tok, "annakey00001", "bertkey00002"} {
		if strings.Contains(string(all), secret) {
			t.Errorf("%q im Verlauf", secret)
		}
	}
}

// Key ersetzen: neue Version, Name/Token/Telefon/Stumm bleiben; doppelt,
// ungültig, unbekannt abgelehnt; derselbe Key "ist bereits".
func TestKeysReplaceKey(t *testing.T) {
	b := newBench(t)
	a := anna
	a.Phone, a.Muted, a.MutedAt = "0171 1", true, t0.Unix()
	b.seed(3, a, bert)
	admin := b.admin()
	admin.want(`{"ok":true,"changed":true,"message":"Key ersetzt. Die Box übernimmt die Liste nach wenigen Sekunden.","version":4}`,
		"action", "keys_replace_key", "id", "1", "key", " neuekey00001 ")
	r := b.c.Store.State().Recipients[0]
	if r.Key != "neuekey00001" || r.Label != "Anna" || r.MuteToken != anna.MuteToken || r.Phone != "0171 1" || !r.Muted {
		t.Errorf("%+v", r)
	}
	if got := fields(b.events(model.EvKeyReplaced)[0]); got != "event=key_replaced by=admin label=Anna key=neue... old_key=anna... version=4" {
		t.Errorf("Verlauf: %s", got)
	}
	admin.want(`{"ok":false,"message":"Dieser Key ist bereits eingetragen."}`, "action", "keys_replace_key", "id", "1", "key", "bertkey00002")
	admin.want(`{"ok":false,"message":"Ungültiger Bark-Key (erlaubt: 5-64 Zeichen, A-Z a-z 0-9 _ -)."}`, "action", "keys_replace_key", "id", "1", "key", "x")
	admin.want(`{"ok":false,"message":"Empfänger nicht gefunden."}`, "action", "keys_replace_key", "id", "99", "key", "neuekey00009")
	admin.want(`{"ok":true,"changed":false,"message":"Dieser Key ist bereits eingetragen."}`, "action", "keys_replace_key", "id", "1", "key", "neuekey00001")
	if v := b.c.Store.State().KeysVersion; v != 4 {
		t.Errorf("Version %d, erwartet 4", v)
	}
}

// Der Key des aktiven Demo-Empfängers ist nicht ersetzbar (der Demo-Modus hält
// ihn) – nichts geändert.
func TestReplaceKeyOfDemoRecipientRejected(t *testing.T) {
	b := newBench(t)
	b.seed(3, anna, bert)
	admin := b.admin()
	admin.want(`{"ok":true,"message":"DEMO-MODUS aktiviert: Alarme gehen nur noch an \"Anna\". WICHTIG: Die Box übernimmt das erst nach einigen Sekunden – Banner oben beobachten!","demo":{"enabled":true,"label":"Anna","key_id":1,"key_masked":"anna...","changed_at":`+fmt.Sprint(t0.Unix())+`}}`,
		"action", "demo_set", "enabled", "1", "id", "1")
	admin.want(`{"ok":false,"message":"Dieser Empfänger ist gerade der Demo-Empfänger. Zuerst den Demo-Modus beenden."}`,
		"action", "keys_replace_key", "id", "1", "key", "neuekey00001")
	if st := b.c.Store.State(); st.KeysVersion != 4 || st.Recipients[0].Key != anna.Key || st.Demo.Key != anna.Key ||
		len(b.events(model.EvKeyReplaced)) != 0 {
		t.Errorf("abgelehnt, aber geändert: %+v", st)
	}
	// Ein anderer Empfänger geht.
	admin.want(`{"ok":true,"changed":true,"message":"Key ersetzt. Die Box übernimmt die Liste nach wenigen Sekunden.","version":5}`,
		"action", "keys_replace_key", "id", "2", "key", "neuekey00002")
}

// Umbenennen des aktiven Demo-Empfängers zieht die Anzeige des Demo-Modus
// mit – in derselben Änderung, ohne neue Version.
func TestRenameDemoRecipientUpdatesLabel(t *testing.T) {
	b := newBench(t)
	b.seed(3, anna, bert)
	admin := b.admin()
	if _, body := admin.do("action", "demo_set", "enabled", "1", "id", "1"); !strings.Contains(body, `"ok":true`) {
		t.Fatal(body)
	}
	admin.want(`{"ok":true,"changed":true,"message":"Name geändert.","version":4}`, "action", "keys_rename", "id", "1", "label", "Anna C.")
	st := b.c.Store.State()
	if st.Demo.Label != "Anna C." || st.Recipients[0].Label != "Anna C." || st.KeysVersion != 4 || !st.Demo.Enabled {
		t.Errorf("Demo %+v, Liste %+v", st.Demo, st.Recipients[0])
	}
	if got := b.statusOf(admin.cookie); !strings.Contains(got, `"demo":{"enabled":true,"label":"Anna C.","key_id":1,"key_masked":"anna...",`) {
		t.Errorf("status: %s", got)
	}
	if raw, _ := admin.list(); !strings.Contains(raw, `"demo":{"enabled":true,"label":"Anna C.",`) {
		t.Errorf("keys_list: %s", raw)
	}
	// Ein anderer Empfänger ändert den Demo-Namen nicht.
	admin.want(`{"ok":true,"changed":true,"message":"Name geändert.","version":4}`, "action", "keys_rename", "id", "2", "label", "Bert B.")
	if st := b.c.Store.State(); st.Demo.Label != "Anna C." {
		t.Errorf("Demo-Name %q", st.Demo.Label)
	}
}
