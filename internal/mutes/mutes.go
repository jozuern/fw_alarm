// Package mutes ist das Sicherheitsnetz des Arbeitsmodus: Stummschaltungen,
// die länger als die Höchstdauer gelten (und nicht "dauerhaft" sind), gehen
// zurück auf laut – mit neuer Listen-Version, damit die Box sie übernimmt,
// und mit den Meldungen danach (PHP: expire_stale_mutes in lib/common.php).
//
// Die Leseseite rechnet überfällige Stummschaltungen schon vorher als laut
// (model.MuteIsStale, eine Definition) – für den Alarm des Servers. Die BOX
// holt ihre Liste aber nur bei neuer Version, und die entsteht erst hier.
// Deshalb läuft Expire vor den Antworten von Poll, api/keys.php,
// Kurzbefehl-Link und keys_list, vor keys_mute und keys_set_mute_permanent,
// nach einem Versand von Alarm oder Entwarnung und im Prüf-Lauf (Spec §6.5).
package mutes

import (
	"context"
	"errors"
	"strings"
	"time"

	"fwalarm/internal/core"
	"fwalarm/internal/model"
	"fwalarm/internal/notify"
	"fwalarm/internal/textde"
)

// sendLimit: so lange darf je eine Meldung danach dauern – die zwei Versuche
// des Bark-Clients brauchen höchstens etwa 21 s.
const sendLimit = 30 * time.Second

// errNothing: Im Entwurf ist (inzwischen) nichts mehr überfällig – nichts
// wird gesichert.
var errNothing = errors.New("nichts überfällig")

// Expire schaltet überfällige Stummschaltungen zurück auf laut.
//
//  1. Zeigt der Schnappschuss keine überfällige Stummschaltung, geschieht
//     nichts (kein Sichern – Expire läuft bei jedem Poll). Im Notbetrieb ist
//     der Schnappschuss leer, also ebenso.
//  2. Sonst EINE Änderung (Store.Update mit ctx, der Frist des Aufrufers):
//     model.ExpireStaleMutes auf dem Entwurf mit der Höchstdauer des Entwurfs
//     und der zuletzt von der Box gemeldeten Version; je Betroffenem der
//     Verlaufseintrag key_unmuted in derselben Änderung.
//  3. Erst wenn sie gesichert ist, nebenher (Core.Bg, nie vor der Antwort und
//     nie unter einer Sperre): die leise Info "Alarmton wieder LAUT" an alle
//     Betroffenen zugleich, danach die Meldung mute_expired an den Admin (nur,
//     wenn er die Art eingeschaltet hat).
//
// Der Fehler von Update kommt zurück (der Aufrufer antwortet trotzdem, wie
// sonst auch – ein Fehler hier ändert seine Antwort nicht); dann geht keine
// Meldung hinaus. Bei store.ErrLate kann die Rückschaltung nach der Frist
// noch gelten – dann steht sie im Verlauf, die Info an die Betroffenen
// entfällt.
func Expire(ctx context.Context, c *core.Core) error {
	cfg := c.Cfg()
	now := c.Clock.Now()
	st := c.Store.State()
	if !anyStale(st, now, st.MuteMax(cfg.MuteMaxSeconds)) {
		return nil
	}
	boxVersion := model.BoxKeysVersion(c.Store.Runtime().Box)
	var expired []model.Recipient
	var muteMax int
	err := c.Store.Update(ctx, func(d *model.State) error {
		muteMax = d.MuteMax(cfg.MuteMaxSeconds)
		if expired = d.ExpireStaleMutes(now, muteMax, boxVersion); len(expired) == 0 {
			return errNothing // von einer anderen Anfrage schon erledigt
		}
		return nil
	})
	switch {
	case errors.Is(err, errNothing):
		return nil
	case err != nil:
		// Der Server rechnet die Betroffenen trotzdem schon als laut, die Box
		// nicht: Sie holt die Liste nur bei neuer Version.
		c.Log.Warn("überfällige Stummschaltung nicht gesichert – die Box alarmiert die Betroffenen bis dahin weiter stumm",
			"err", err)
		return err
	}
	tell(c, expired, muteMax, now)
	return nil
}

// anyStale: Zeigt st eine überfällige Stummschaltung?
func anyStale(st *model.State, now time.Time, muteMax int) bool {
	for _, r := range st.Recipients {
		if model.MuteIsStale(r, now, muteMax) {
			return true
		}
	}
	return false
}

// tell schickt nebenher die Meldungen nach einer Rückschaltung (Texte wie
// PHP, als ASCII über notify): erst die leise Info an alle Betroffenen in
// EINEM Versand (nacheinander hielte das bei gestörtem Bark minutenlang auf),
// dann – nur wenn eingeschaltet – mute_expired an den Admin mit ihren Namen.
// Die Zeit im Text ist die der Rückschaltung.
func tell(c *core.Core, expired []model.Recipient, muteMax int, now time.Time) {
	stamp := textde.Stamp(now, c.Cfg().Location())
	keys := make([]string, len(expired))
	labels := make([]string, len(expired))
	for i, r := range expired {
		keys[i], labels[i] = r.Key, r.Label
	}
	info := stamp + "Stummschaltung automatisch beendet (war laenger als " + textde.DurationLabel(muteMax) +
		" aktiv). Alarme kommen wieder als Critical Alert mit Ton."
	admin := stamp + "Automatisch wieder laut geschaltet (Zeitlimit erreicht): " + strings.Join(labels, ", ") + "."
	c.Bg.Go("Meldungen nach dem Zurückschalten auf laut", func() {
		ctx, cancel := context.WithTimeout(context.Background(), sendLimit)
		notify.SendToAll(ctx, c, keys, "Alarmton wieder LAUT", info) // best effort wie PHP: das Ergebnis ändert nichts
		cancel()
		ctx, cancel = context.WithTimeout(context.Background(), sendLimit)
		defer cancel()
		notify.Send(ctx, c, "mute_expired", "Stummschaltung abgelaufen", admin, false) // ebenso (abgeschaltet: off)
	})
}
