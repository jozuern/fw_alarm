package dashapi

import (
	"time"

	"fwalarm/internal/model"
	"fwalarm/internal/web"
)

// Demo-Modus (PHP: dashboard_api.php, Zweig demo_set, und demo_mode_info in
// lib/common.php): Beide Alarmwege gehen nur an EINEN Empfänger aus der
// Liste – ohne Änderung der Firmware, über die Listen-Version: Jeder Wechsel
// erhöht sie, die Box holt die passende Liste beim nächsten Poll. Demo-Zustand
// und Version ändern sich in EINER Änderung der Ablage – den halb
// geschriebenen Wechsel (und PHPs code=inconsistent, das Zurückrollen, die
// Reparatur) gibt es nicht mehr (Spec §7).

func init() {
	Register("demo_set", Action{Handle: handleDemoSet})
	// Für das Banner: Demo-Modus, Listen-Version des Servers (die Oberfläche
	// vergleicht sie mit der, die die Box meldet) und ob die Box die
	// Demo-Liste nachweislich übernommen hat.
	StatusPart("demo", func(x *Ctx) any { return demoInfo(x.Core.Store.State()) })
	StatusPart("nas_keys_version", func(x *Ctx) any { return listVersion(x) })
	StatusPart("demo_sync_ok", demoSyncOK)
}

// demoInfo: der Demo-Modus für die Oberfläche, der Key nur maskiert (PHP:
// demo_mode_info). enabled sagt, wohin Alarme WIRKLICH gehen: nur bei
// gesetztem Schalter UND brauchbarem Key – sonst alarmiert der Server die
// volle Liste, und Oberfläche und Server müssen sich über den Modus einig
// sein (expect_demo).
func demoInfo(st *model.State) web.Object {
	on := st.DemoEnabled()
	label, id, masked := "", 0, ""
	if on {
		label, id, masked = st.Demo.Label, st.Demo.KeyID, model.MaskKey(st.Demo.Key)
	}
	return web.Object{
		{Key: "enabled", Value: on},
		{Key: "label", Value: label},
		{Key: "key_id", Value: id},
		{Key: "key_masked", Value: masked},
		{Key: "changed_at", Value: st.Demo.ChangedAt},
	}
}

// listVersion: die Listen-Version des Servers; 0 im Notbetrieb (PHP:
// load_alarm_keys liefert dann die leere Liste).
func listVersion(x *Ctx) int {
	if x.Core.Store.Emergency() {
		return 0
	}
	return x.Core.Store.State().KeysVersion
}

// demoSyncOK: "Box hat die Demo-Liste übernommen" nach DERSELBEN Regel wie die
// Sperre des Demo-Alarms über die Box (Version, genau ein Empfänger, frisch;
// model.DemoBlockReason) – das Banner darf nicht "gefahrlos testen" sagen, wo
// der Server sperrt.
func demoSyncOK(x *Ctx) any {
	st := x.Core.Store.State()
	return st.DemoEnabled() &&
		st.DemoBlockReason(x.Core.Store.Runtime().Box, x.Core.Clock.Now(), x.Core.Cfg().OfflineAfterSeconds) == ""
}

// handleDemoSet (dashboard_api.php 449–525): enabled=1 mit id = Demo-Empfänger
// (muss aus der Liste stammen – so gibt es beim Zurückschalten garantiert
// eine nicht leere Liste), sonst LIVE. Ausschalten ist idempotent und erhöht
// IMMER die Version (der Reparaturweg, falls die Box auf einer alten Liste
// sitzt). Verlauf demo_enabled/demo_disabled mit der neuen Version; die
// Antwort trägt den neuen Demo-Zustand.
func handleDemoSet(x *Ctx) {
	enable := x.Form.Get("enabled") == "1"
	id := formID(x)
	now, by, bv := x.Core.Clock.Now(), x.Sess.User, boxVersion(x)
	box := x.Core.Store.Runtime().Box
	loc := x.Core.Cfg().Location()
	apply(x, func(d *model.State) (web.Object, error) {
		var message string
		if enable {
			r := d.Find(id)
			if r == nil {
				return nil, refuse("Demo-Empfänger nicht gefunden. Zuerst im Panel \"Alarm-Empfänger\" anlegen, dann Demo-Modus einschalten.")
			}
			d.Demo = model.Demo{Enabled: true, KeyID: r.ID, Key: r.Key, Label: r.Label, ChangedAt: now.Unix(), By: by}
			message = "DEMO-MODUS aktiviert: Alarme gehen nur noch an \"" + r.Label + "\". " +
				"WICHTIG: Die Box übernimmt das erst nach einigen Sekunden – Banner oben beobachten!"
		} else {
			if d.Demo.Enabled {
				message = "LIVE-Modus aktiv: Alarme gehen wieder an ALLE Empfänger. "
			} else {
				message = "Der Demo-Modus war bereits aus – die volle Liste wird der Box erneut angeboten. "
			}
			message += "Die Box übernimmt die volle Liste nach wenigen Sekunden." +
				demoCooldownWarning(box, d.Commands.Current, now, loc)
			d.Demo = model.Demo{ChangedAt: now.Unix(), By: by}
		}
		version := d.Bump(bv)
		event := model.EvDemoDisabled
		if enable {
			event = model.EvDemoEnabled
		}
		d.Log(model.NewEntry(event, now, map[string]any{"by": by, "label": d.Demo.Label, "version": version}))
		return web.Object{{Key: "ok", Value: true}, {Key: "message", Value: message}, {Key: "demo", Value: demoInfo(d)}}, nil
	})
}

// demoCooldownWarning (PHP: demo_alarm_cooldown_warning): ÄLTERE Firmware
// behandelt einen Demo-Alarm wie einen Relaisalarm – danach gilt auch ihre
// Sperrzeit (Standard 300 s), ein ECHTER Relaisalarm darin wird verworfen.
// Wer kurz danach auf LIVE schaltet, muss das wissen. Firmware ab 11/2026
// (erkennbar am immer gesendeten alarm_seq) setzt keine Sperrzeit mehr.
func demoCooldownWarning(box *model.BoxStatus, cur *model.Command, now time.Time, loc *time.Location) string {
	if box != nil {
		for _, f := range box.Fields {
			if f.Name == "alarm_seq" {
				return ""
			}
		}
	}
	if cur == nil || cur.Type != "ALARM" {
		return ""
	}
	executed := cur.State == model.CommandDelivered ||
		(cur.State == model.CommandAcked && cur.AckResult == "executed")
	until := cur.DeliveredAt + 300
	if !executed || until <= now.Unix() {
		return ""
	}
	return " ACHTUNG: Die Box hat gerade einen DEMO-ALARM ausgeführt und erkennt bis ca. " +
		time.Unix(until, 0).In(loc).Format("15:04") + " Uhr keinen neuen Relaisalarm (Sperrzeit nach jedem Alarm). " +
		"Im Ernstfall bis dahin den REAL ALARM benutzen."
}
