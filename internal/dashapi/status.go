package dashapi

import (
	"fmt"
	"net/http"
	"sync"

	"fwalarm/internal/config"
	"fwalarm/internal/model"
	"fwalarm/internal/web"
)

// Die Aktion status (PHP: dashboard_api.php, Zweig status): der Stand für
// das Dashboard, alle 5 s je offenem Tab. Gebaut aus den Schnappschüssen im
// Speicher – sie liest keine Datei und ändert nichts (die Sitzung erneuert
// Resume schon vorher).

func init() { Register("status", Action{Handle: handleStatus}) }

// statusPartNames: die Teile der Antwort, die andere Aufgaben liefern
// (StatusPart): command (Befehle, Aufgabe 18), demo, nas_keys_version,
// demo_sync_ok (Empfänger und Demo-Modus, Aufgabe 16), device_hint (Boards,
// Aufgabe 17), watchdog (Prüf-Lauf, Aufgabe 21).
var statusPartNames = []string{"command", "demo", "nas_keys_version", "demo_sync_ok", "device_hint", "watchdog"}

// emptyParts: was PHP für diese Teile auf einem frischen Datenordner
// liefert – es gilt, bis die Aufgabe, der der Teil gehört, ihn liefert.
var emptyParts = map[string]func(x *Ctx) any{
	// command_state_default(): kein laufender Befehl.
	"command": func(*Ctx) any { return nil },
	// demo_mode_info() ohne demo_mode.json.
	"demo": func(*Ctx) any {
		return web.Object{{Key: "enabled", Value: false}, {Key: "label", Value: ""}, {Key: "key_id", Value: 0}, {Key: "key_masked", Value: ""}, {Key: "changed_at", Value: 0}}
	},
	// load_alarm_keys() ohne Liste: Version 0.
	"nas_keys_version": func(*Ctx) any { return 0 },
	// Demo-Modus aus.
	"demo_sync_ok": func(*Ctx) any { return false },
	// device_hint() ohne devices_seen.json.
	"device_hint": func(*Ctx) any { return "" },
	// Ohne watchdog_state.json: nie gelaufen, nie gesendet. configured hängt
	// allein an der Konfiguration; was nur der Admin sieht (offline_alert,
	// web_alert) und der Außen-Wächter kommen mit Aufgabe 21.
	"watchdog": func(x *Ctx) any {
		return web.Object{
			{Key: "configured", Value: config.TrimPHP(x.Core.Cfg().BarkKeyStatus) != ""},
			{Key: "last_run", Value: 0},
			{Key: "last_send_ok_at", Value: 0},
			{Key: "last_send_failed_at", Value: 0},
		}
	},
}

// partSet: die Teile der Antwort – je Name die geltende Funktion (anfangs
// die aus emptyParts) und ob eine Aufgabe ihn schon geliefert hat.
type partSet struct {
	mu    sync.RWMutex
	fns   map[string]func(x *Ctx) any
	taken map[string]bool
}

func newPartSet() *partSet {
	p := &partSet{fns: map[string]func(x *Ctx) any{}, taken: map[string]bool{}}
	for _, name := range statusPartNames {
		p.fns[name] = emptyParts[name]
	}
	return p
}

// parts: die Teile des Programms.
var parts = newPartSet()

// StatusPart liefert den Teil name der Antwort von status: fn bekommt die
// Anfrage samt Sitzung (die Rolle kann den Teil ändern) und gibt den Wert
// zurück, wie er im JSON stehen soll (web.Object für Objekte in fester
// Reihenfolge). Nur die Teile aus statusPartNames, jeder einmal – alles
// andere ist ein Fehler im Aufbau des Programms (Abbruch beim Start). Vor dem
// ersten Aufruf gilt PHPs Wert für einen frischen Datenordner.
func StatusPart(name string, fn func(x *Ctx) any) { parts.set(name, fn) }

func (p *partSet) set(name string, fn func(x *Ctx) any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch _, known := p.fns[name]; {
	case !known:
		panic(fmt.Sprintf("dashapi: %q ist kein Teil der Antwort von status, den eine Aufgabe liefert (%v)", name, statusPartNames))
	case fn == nil:
		panic(fmt.Sprintf("dashapi: Teil %q ohne Funktion", name))
	case p.taken[name]:
		panic(fmt.Sprintf("dashapi: Teil %q zweimal geliefert", name))
	}
	p.fns[name] = fn
	p.taken[name] = true
}

func (p *partSet) value(name string, x *Ctx) any {
	p.mu.RLock()
	fn := p.fns[name]
	p.mu.RUnlock()
	return fn(x)
}

// handleStatus: die Felder in PHPs Reihenfolge (TestStatusKeySet).
func handleStatus(x *Ctx) {
	cfg := x.Core.Cfg()
	now := x.Core.Clock.Now()
	box := x.Core.Store.Runtime().Box
	status, seenAge, offline := boxView(box, cfg.OfflineAfterSeconds, now.Unix())
	web.JSON(x.W, http.StatusOK, web.Object{
		{Key: "ok", Value: true},
		{Key: "status", Value: status},
		{Key: "seen_age_seconds", Value: seenAge},
		{Key: "offline", Value: offline},
		{Key: "command", Value: parts.value("command", x)},
		// Demo-Modus + Listen-Version: Damit zeigt das Dashboard, ob die Box
		// die aktuelle Liste schon übernommen hat.
		{Key: "demo", Value: parts.value("demo", x)},
		{Key: "nas_keys_version", Value: parts.value("nas_keys_version", x)},
		{Key: "demo_sync_ok", Value: parts.value("demo_sync_ok", x)},
		{Key: "device_hint", Value: parts.value("device_hint", x)},
		// Zeitzone für die Zeitangaben im Browser.
		{Key: "timezone", Value: cfg.Location().String()},
		// Das aktuelle Token: Ein alter Tab übernimmt es nach einer
		// Neuanmeldung in einem anderen – sonst scheiterte sein erster Alarm.
		{Key: "csrf", Value: x.Sess.CSRF},
		{Key: "watchdog", Value: parts.value("watchdog", x)},
		{Key: "server_time", Value: now.Unix()},
	})
}

// boxView: der letzte Push für die Anzeige, sein Alter und ob die Box als
// offline gilt (PHP: latest_status.json im Zweig status).
//
// status: die gepushten Felder in ihrer Reihenfolge, dahinter seen_at und
// via_bridge – hieß ein Feld der Box schon so, steht der Wert des Servers an
// dessen Stelle (wie PHP sie ins Array setzte); die Adresse der Wache
// (remote_addr) nie. Hat sich die Box nie gemeldet: [] (PHPs leeres Array).
// seenAge: Sekunden seit dem Push, nil ohne Ankunftszeit. offline: kein
// Push oder älter als offline_after_seconds; läuft laut Push gerade eine
// Alarm-Zustellung (alarm_pending=1), mindestens 600 s – die Box blockiert
// dann zwischen ihren Bark-Versuchen länger als sonst.
func boxView(box *model.BoxStatus, offlineAfter int, now int64) (status, seenAge any, offline bool) {
	if box == nil {
		return []any{}, nil, true
	}
	var obj web.Object
	at := map[string]int{}
	put := func(key string, v any) {
		if i, ok := at[key]; ok {
			obj[i].Value = v
			return
		}
		at[key] = len(obj)
		obj = append(obj, web.Member{Key: key, Value: v})
	}
	for _, f := range box.Fields {
		put(f.Name, f.Value)
	}
	put("seen_at", box.SeenAt)
	put("remote_addr", nil) // Stelle wie in PHP, gleich wieder fort
	put("via_bridge", box.ViaBridge)
	pending := false
	out := obj[:0:0]
	for _, m := range obj {
		switch m.Key {
		case "remote_addr":
			continue
		case "alarm_pending":
			pending = m.Value == "1"
		}
		out = append(out, m)
	}
	if box.SeenAt <= 0 {
		return out, nil, true
	}
	age := now - box.SeenAt
	if pending {
		offlineAfter = max(offlineAfter, 600)
	}
	return out, age, age > int64(offlineAfter)
}
