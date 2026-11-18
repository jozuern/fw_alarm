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
	"sync"
	"time"

	"fwalarm/internal/core"
	"fwalarm/internal/model"
	"fwalarm/internal/notify"
	"fwalarm/internal/store"
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
//  2. Sonst EINE Änderung (Store.UpdateThen mit ctx, der Frist des
//     Aufrufers): model.ExpireStaleMutes auf dem Entwurf mit der Höchstdauer
//     des Entwurfs und der zuletzt von der Box gemeldeten Version; je
//     Betroffenem der Verlaufseintrag key_unmuted in derselben Änderung.
//  3. Sobald sie gilt – rechtzeitig oder erst nach der Frist (ErrLate) –,
//     genau einmal und nebenher (Core.Bg, nie vor der Antwort und nie unter
//     einer Sperre): die leise Info "Alarmton wieder LAUT" an alle
//     Betroffenen zugleich, danach die Meldung mute_expired an den Admin
//     (nur, wenn er die Art eingeschaltet hat). Das sagt die Ablage
//     (UpdateThen ruft tell nach dem Umsetzen des Zeigers) – nie, wenn das
//     Sichern scheitert.
//
// Der Fehler von UpdateThen kommt zurück (der Aufrufer antwortet trotzdem,
// wie sonst auch – ein Fehler hier ändert seine Antwort nicht). Ins Journal
// geht er höchstens einmal pro Minute (Expire läuft bei jedem Poll; eine
// kaputte Platte schriebe sonst eine Zeile je Poll), die nächste Zeile nennt,
// wie viele dazwischen unterdrückt wurden.
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
	// expired und muteMax schreibt fn vor dem Sichern; after liest sie erst
	// danach, auf der Goroutine, die die Ablage dafür startete.
	err := c.Store.UpdateThen(ctx, func(d *model.State) error {
		muteMax = d.MuteMax(cfg.MuteMaxSeconds)
		if expired = d.ExpireStaleMutes(now, muteMax, boxVersion); len(expired) == 0 {
			return errNothing // von einer anderen Anfrage schon erledigt
		}
		return nil
	}, func() { tell(c, expired, muteMax, now) })
	switch {
	case errors.Is(err, errNothing):
		return nil
	case err != nil:
		// Der Server rechnet die Betroffenen trotzdem schon als laut, die Box
		// nicht: Sie holt die Liste nur bei neuer Version. Die Bremse misst
		// mit der Uhr JETZT, nicht mit now vom Beginn: Zwei Aufrufe, die in
		// umgekehrter Reihenfolge fertig werden, sähen sonst wie eine
		// zurückgesprungene Uhr aus.
		if ok, suppressed := warnings(c).allow(c.Clock.Now()); ok {
			c.Log.Warn(warningText(err), "err", err, "unterdrueckt", suppressed)
		}
		return err
	}
	return nil
}

// warningText: die Zeile im Journal, wenn die Rückschaltung nicht galt. Nach
// ErrLate kann das Sichern noch landen – dann gilt sie doch.
func warningText(err error) string {
	if errors.Is(err, store.ErrLate) {
		return "überfällige Stummschaltung nicht rechtzeitig gesichert – landet das Sichern noch, gilt sie; bis dahin alarmiert die Box die Betroffenen weiter stumm"
	}
	return "überfällige Stummschaltung nicht gesichert – die Box alarmiert die Betroffenen bis dahin weiter stumm"
}

// warnEvery: so oft steht die Warnung höchstens im Journal.
const warnEvery = time.Minute

// throttle lässt eine Zeile höchstens alle warnEvery durch und zählt, was
// dazwischen unterdrückt wurde.
type throttle struct {
	mu         sync.Mutex
	logged     bool
	last       time.Time
	suppressed int
}

// allow: true = jetzt ins Journal, mit der Zahl der seit der letzten Zeile
// unterdrückten. Springt die Uhr zurück, gilt das als abgelaufen (sonst
// schwiege die Warnung, bis die Uhr den alten Stand wieder erreicht).
func (l *throttle) allow(now time.Time) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.logged && now.Sub(l.last) < warnEvery && !now.Before(l.last) {
		l.suppressed++
		return false, 0
	}
	n := l.suppressed
	l.logged, l.last, l.suppressed = true, now, 0
	return true, n
}

// throttles: je Core eine Bremse (im Programm gibt es einen Core; die Tests
// bauen viele, die sich nicht gegenseitig bremsen sollen).
var (
	throttlesMu sync.Mutex
	throttles   = map[*core.Core]*throttle{}
)

func warnings(c *core.Core) *throttle {
	throttlesMu.Lock()
	defer throttlesMu.Unlock()
	l := throttles[c]
	if l == nil {
		l = &throttle{}
		throttles[c] = l
	}
	return l
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
