package core

import (
	"sync"
	"time"
)

// Warnungen, die eine kaputte oder hängende Platte bei jeder Anfrage
// hervorriefe (der Poll der Box alle paar Sekunden, die Quittung, das
// Zurückschalten überfälliger Stummschaltungen, das Verfallen eines Befehls
// im Dashboard): höchstens eine Zeile je Art und Minute; die nächste nennt,
// wie viele dazwischen unterdrückt wurden.

// rarelyEvery: so oft steht eine Art höchstens im Journal.
const rarelyEvery = time.Minute

// rarely lässt eine Zeile höchstens alle rarelyEvery durch und zählt, was
// dazwischen unterdrückt wurde.
type rarely struct {
	mu         sync.Mutex
	logged     bool
	last       time.Time
	suppressed int
}

// allow: true = jetzt ins Journal, mit der Zahl der seit der letzten Zeile
// unterdrückten. Springt die Uhr zurück, gilt das als abgelaufen (sonst
// schwiege die Warnung, bis die Uhr den alten Stand wieder erreicht).
func (l *rarely) allow(now time.Time) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.logged && now.Sub(l.last) < rarelyEvery && !now.Before(l.last) {
		l.suppressed++
		return false, 0
	}
	n := l.suppressed
	l.logged, l.last, l.suppressed = true, now, 0
	return true, n
}

// rarelyKey: je Core und Art eine Bremse (im Programm gibt es einen Core; die
// Tests bauen viele, die sich nicht gegenseitig bremsen sollen).
type rarelyKey struct {
	c    *Core
	kind string
}

var (
	rarelyMu sync.Mutex
	rarelyBy = map[rarelyKey]*rarely{}
)

// WarnRarely schreibt msg als Warnung ins Journal – höchstens eine Zeile je
// kind und Minute auf c.Clock, gemessen beim Aufruf. Jede Zeile trägt
// zusätzlich "unterdrueckt": wie viele Zeilen dieser Art seit der vorigen
// weggefallen sind. args wie bei slog.
func (c *Core) WarnRarely(kind, msg string, args ...any) {
	rarelyMu.Lock()
	l := rarelyBy[rarelyKey{c, kind}]
	if l == nil {
		l = &rarely{}
		rarelyBy[rarelyKey{c, kind}] = l
	}
	rarelyMu.Unlock()
	if ok, n := l.allow(c.Clock.Now()); ok {
		c.Log.Warn(msg, append(args, "unterdrueckt", n)...)
	}
}
