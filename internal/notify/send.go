package notify

import (
	"context"
	"strconv"

	"fwalarm/internal/bark"
	"fwalarm/internal/config"
	"fwalarm/internal/core"
	"fwalarm/internal/model"
)

// Ergebnisse von Send.
const (
	Sent   = "sent"   // Bark hat die Meldung angenommen (Status-Key oder Stellvertreter)
	Failed = "failed" // nicht angenommen – oder nie versucht, weil etwas nicht stimmt
	Off    = "off"    // bewusst nichts gesendet: Art abgeschaltet oder kein Status-Key
)

// sendTries: Versuche je Meldung (PHP: bark_post_many(…, 2)).
const sendTries = 2

// Used sagt, wie eine Meldung gesendet wurde – so muss niemand die Regeln
// nachrechnen (PHP: $used von admin_notify_send). Level: die tatsächliche
// Dringlichkeit; QuietCapped: die Ruhezeit hat sie auf leise gekappt;
// PrimaryFailed: nur der Stellvertreter hat angenommen, der Status-Key nicht.
// Leer, wenn gar nicht gesendet wurde.
type Used struct {
	Level                      string
	QuietCapped, PrimaryFailed bool
}

// Send sendet EINE Meldung der Art kind an den Status-Key, nach den
// Einstellungen des Admins (PHP: admin_notify_send) – jede Meldung des
// Servers an den Admin geht hier hindurch. In dieser Reihenfolge:
//
//  1. Status-Key (bark_key_status) und Einstellungen aus EINEM Stand von
//     Konfiguration und Zustand (nur Speicher, keine Sperre – gesendet wird
//     nie unter einer Sperre der Ablage).
//  2. Unbekannte Art (ein Tippfehler im Programm): "failed", bei leerem
//     Status-Key "off" – nie still "off" bei eingetragenem Key, sonst hakte
//     der Prüf-Lauf den Fall als erledigt ab.
//  3. Kein Status-Key, oder die Art ist abgeschaltet und force nicht gesetzt:
//     "off" – das einzige "off".
//  4. Ein eingetragener, aber unbrauchbarer Key: "failed" (ein Fehler, kein
//     "abgeschaltet").
//  5. Dringlichkeit der Art; in der Ruhezeit (Zeitzone der Konfiguration)
//     leise, außer die Art ist davon ausgenommen (ignore_quiet).
//  6. Felder: title und body als ASCII (bark.ASCII), level; ab "normal" der
//     eingestellte Ton, bei "kritisch" die eingestellte Lautstärke.
//  7. An den Status-Key und – best effort – an den Stellvertreter
//     (extra_id), wenn es ihn in der Liste gibt, sein Key brauchbar und nicht
//     der Status-Key ist; beide zugleich, höchstens 2 Versuche
//     (bark.Client.PostMany, mit seiner Probe-Sperre und seinen Zeitlimits).
//  8. "sent", wenn der Status-Key angenommen hat; hat nur der Stellvertreter
//     angenommen, ebenfalls "sent", mit Used.PrimaryFailed (sonst bekäme er
//     dieselbe Meldung bei jedem Lauf erneut); sonst "failed" – auch wenn die
//     Probe-Sperre den Versand zurückhielt, Bark mit 4xx/5xx oder gar nicht
//     antwortete oder ctx vorher endete.
//
// force: Probe-Meldung aus dem Dashboard – sendet auch eine abgeschaltete
// Art. ctx begrenzt, wie lange gesendet wird; länger als die Zeitlimits des
// Bark-Clients dauert Send nie.
func Send(ctx context.Context, c *core.Core, kind, title, body string, force bool) (result string, used Used) {
	cfg := c.Cfg()
	st := c.Store.State()
	key := config.TrimPHP(cfg.BarkKeyStatus)
	set := resolve(cfg, st)
	row, known := set.Types[kind]
	if !known {
		c.Log.Error("Meldung an den Admin: unbekannte Art", "art", kind)
		if key == "" {
			return Off, Used{}
		}
		return Failed, Used{}
	}
	if key == "" || (!force && !row.Enabled) {
		return Off, Used{}
	}
	if !model.ValidKey(key) {
		return Failed, Used{}
	}

	level := row.Values["level"]
	if row.Values["ignore_quiet"] != "1" && level != "passive" &&
		InQuietHours(set.Global, c.Clock.Now().In(cfg.Location())) {
		level = "passive"
		used.QuietCapped = true
	}
	used.Level = level
	fields := map[string]string{"title": bark.ASCII(title), "body": bark.ASCII(body), "level": level}
	if sound := set.Global["sound"]; level != "passive" && sound != "" {
		fields["sound"] = sound
	}
	if level == "critical" {
		fields["volume"] = set.Global["critical_volume"]
	}

	jobs := []bark.Job{{Key: key, Fields: fields}}
	if extra := deputyKey(st, set.Global["extra_id"]); extra != "" && extra != key {
		jobs = append(jobs, bark.Job{Key: extra, Fields: fields})
	}
	res := c.Bark.PostMany(ctx, jobs, sendTries)
	switch {
	case res[0].OK:
		return Sent, used
	case len(res) > 1 && res[1].OK:
		used.PrimaryFailed = true
		return Sent, used
	}
	return Failed, used
}

// deputyKey: der Key des Stellvertreters mit der id extraID, wenn es ihn
// gibt und er brauchbar ist; sonst "".
func deputyKey(st *model.State, extraID string) string {
	id, err := strconv.Atoi(extraID)
	if err != nil || id <= 0 {
		return ""
	}
	for _, r := range st.Recipients {
		if r.ID == id {
			if k := config.TrimPHP(r.Key); model.ValidKey(k) {
				return k
			}
			return ""
		}
	}
	return ""
}

// SendTo schickt eine leise Meldung (level=passive) an EINEN Key (PHP:
// bark_send_status) – nur noch für die Info an einen BETROFFENEN Empfänger
// (die automatische Laut-Rückschaltung). Texte als ASCII, höchstens 2
// Versuche; ein unbrauchbarer Key wird gar nicht erst versucht. true = Bark
// hat angenommen.
func SendTo(ctx context.Context, c *core.Core, key, title, body string) bool {
	if !model.ValidKey(key) {
		return false
	}
	fields := map[string]string{"title": bark.ASCII(title), "body": bark.ASCII(body), "level": "passive"}
	return c.Bark.PostMany(ctx, []bark.Job{{Key: key, Fields: fields}}, sendTries)[0].OK
}
