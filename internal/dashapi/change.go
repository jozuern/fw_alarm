package dashapi

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"time"

	"fwalarm/internal/model"
	"fwalarm/internal/mutes"
	"fwalarm/internal/web"
)

// Gemeinsames der Aktionen, die die Empfängerliste, den Demo-Modus oder die
// Einstellungen ändern (PHP: mutate_alarm_keys und die Antworten der Zweige
// in dashboard_api.php). Jede Aktion ist EINE Änderung der Ablage: Was von
// der gespeicherten Liste abhängt (doppelter Key, letzter Empfänger, der
// aktive Demo-Empfänger, höchstens 10), wird IM Entwurf geprüft – zwei
// gleichzeitige Anfragen können so nie beide durchkommen. Der Verlaufseintrag
// reist in derselben Änderung (outbox): Gilt die Änderung, steht er im
// Verlauf, sonst keins von beiden.

// answered ist eine Antwort ohne Sichern (PHP: ein Ergebnis von
// mutate_alarm_keys ohne 'write') – eine Ablehnung oder "ist bereits so".
// Sie bricht die Änderung ab; nichts wird gesichert.
type answered struct{ obj web.Object }

func (a *answered) Error() string { return "beantwortet ohne Änderung" }

// refuse: abgelehnt, nichts geändert – {"ok":false,"message":…} mit Status
// 200 wie PHP.
func refuse(message string) error {
	return &answered{web.Object{{Key: "ok", Value: false}, {Key: "message", Value: message}}}
}

// same: schon so – {"ok":true,"changed":false,"message":…}, nichts gesichert.
func same(message string) error {
	return &answered{web.Object{{Key: "ok", Value: true}, {Key: "changed", Value: false}, {Key: "message", Value: message}}}
}

// reply beantwortet die Anfrage mit {"ok":false,"message":…} (Prüfungen VOR
// der Änderung – Format eines Felds, wie PHP vor dem Lock).
func reply(x *Ctx, message string) {
	web.JSON(x.W, http.StatusOK, web.Object{{Key: "ok", Value: false}, {Key: "message", Value: message}})
}

// changed: die Antwort nach einer gesicherten Änderung, in PHPs Reihenfolge
// ok, changed, message, version (version = die Listen-Version danach – neu
// oder unverändert).
func changed(message string, version int) web.Object {
	return web.Object{{Key: "ok", Value: true}, {Key: "changed", Value: true}, {Key: "message", Value: message},
		{Key: "version", Value: version}}
}

// apply führt EINE Änderung aus – ein Store.Update mit der Frist der
// Oberfläche – und beantwortet die Anfrage: fn rechnet auf dem Entwurf (nur
// mit übergebenen Werten: kein Netz, keine Datei, keine Uhr) und liefert die
// Antwort für den Fall, dass die Änderung gilt. Liefert fn refuse/same, wird
// das ohne Sichern beantwortet; scheitert das Sichern (oder läuft die Frist
// ab, oder der Server ist im Notbetrieb), antwortet web.StorageError.
func apply(x *Ctx, fn func(d *model.State) (web.Object, error)) {
	ctx, cancel := web.Budget(web.BudgetUI)
	defer cancel()
	applyWithin(ctx, x, fn)
}

// expireThenApply: wie apply, vorher aber überfällige Stummschaltungen
// zurück auf laut (PHP: expire_stale_mutes vor mutate_alarm_keys) – für
// Aktionen, die den Stumm-Zustand ansehen. Beides in DERSELBEN Frist der
// Anfrage (Spec §6.2). Ein Fehler von Expire ändert die Antwort nicht: Er
// zeigt sich an der Änderung der Aktion selbst (dieselbe Ablage, dieselbe
// Frist); Expire schreibt ihn ins Journal.
func expireThenApply(x *Ctx, fn func(d *model.State) (web.Object, error)) {
	ctx, cancel := web.Budget(web.BudgetUI)
	defer cancel()
	_ = mutes.Expire(ctx, x.Core) // siehe oben
	applyWithin(ctx, x, fn)
}

// applyWithin: apply mit der Frist ctx des Aufrufers.
func applyWithin(ctx context.Context, x *Ctx, fn func(d *model.State) (web.Object, error)) {
	var ok web.Object
	err := x.Core.Store.Update(ctx, func(d *model.State) error {
		var err error
		ok, err = fn(d)
		return err
	})
	var a *answered
	switch {
	case errors.As(err, &a):
		web.JSON(x.W, http.StatusOK, a.obj)
	case err != nil:
		web.StorageError(x.W, err, true)
	default:
		web.JSON(x.W, http.StatusOK, ok)
	}
}

// formID: die id aus dem Formular wie PHP (int)($_POST['id'] ?? 0).
func formID(x *Ctx) int { return int(model.IntOf(x.Form.Get("id"))) }

// boxVersion: die Listen-Version, die die Box zuletzt gemeldet hat – jede
// Erhöhung geht über sie hinaus (PHP bump_keys_version: Nach dem Verlust
// des Datenordners liefe die Version sonst auf eine Nummer, die die Box mit
// anderem Inhalt kennt).
func boxVersion(x *Ctx) int { return model.BoxKeysVersion(x.Core.Store.Runtime().Box) }

// keyEvent: ein Verlaufseintrag zu EINEM Empfänger – Name und Key immer
// gleich, der Key nur maskiert (PHP: log_key_event). extra kommt dazu.
func keyEvent(event string, r model.Recipient, by string, now time.Time, extra map[string]any) model.Entry {
	fields := map[string]any{"by": by, "label": r.Label, "key": model.MaskKey(r.Key)}
	maps.Copy(fields, extra)
	return model.NewEntry(event, now, fields)
}

// Sätze, die mehrere Aktionen geben (wörtlich aus dashboard_api.php und
// lib/common.php).
const (
	msgNotFound     = "Empfänger nicht gefunden."
	msgBadLabel     = "Name fehlt, ist länger als 40 Zeichen oder enthält ungültige Zeichen."
	msgBadKey       = "Ungültiger Bark-Key (erlaubt: 5-64 Zeichen, A-Z a-z 0-9 _ -)."
	msgBadPhone     = "Ungültige Telefonnummer (erlaubt: 3-30 Zeichen: Ziffern, + - / ( ) und Leerzeichen)."
	msgKeyTaken     = "Dieser Key ist bereits eingetragen."
	msgDemoKeyFixed = "Dieser Empfänger ist gerade der Demo-Empfänger. Zuerst den Demo-Modus beenden."
	msgBoxTakesList = "Die Box übernimmt die Liste nach wenigen Sekunden."
	msgBoxTakesIt   = "Die Box übernimmt das nach wenigen Sekunden."
)

// isDemoRecipient: Ist id gerade der Demo-Empfänger? (PHP:
// !empty($demo['enabled']) && (int)$demo['key_id'] === $id)
func isDemoRecipient(d *model.State, id int) bool { return d.Demo.Enabled && d.Demo.KeyID == id }

// uniqueToken: ein neuer Kurzbefehl-Token, den kein Empfänger in d schon hat
// (bei 72 Bit Zufall praktisch immer der erste – aber ein doppelter liefe
// sonst in Validate).
func uniqueToken(d *model.State) string {
	for {
		tok := model.NewMuteToken()
		taken := false
		for _, r := range d.Recipients {
			if r.MuteToken == tok {
				taken = true
			}
		}
		if !taken {
			return tok
		}
	}
}
