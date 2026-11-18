package web

import (
	"fmt"
	"time"

	"fwalarm/internal/core"
	"fwalarm/internal/model"
)

// Bremse pro Absender (PHP: throttle_hit, throttle_clear): Nach zu vielen
// Versuchen in einem Zeitfenster ist der Absender bis zu dessen Ende
// gesperrt. Genutzt für die Anmeldung (jeder Versuch zählt, Erfolg setzt
// zurück) und den Kurzbefehl-Link (nur unbekannte Tokens zählen). Die
// Zähler sind flüchtig (Runtime.Throttle): Ein Versuch gilt sofort im
// Speicher und wartet nie auf die Platte.

// maxThrottleSources: Höchstzahl gezählter Absender je Bremse (PHP:
// THROTTLE_MAX_SOURCES). Wer viele Adressen hat (ein IPv6-/48 sind 65 536
// /64-Netze), bläht die Zähler nicht beliebig auf. Ist die Bremse voll,
// wird ein NEUER Absender weder gezählt noch gesperrt – bewusst kein
// Sammel-Zähler, den ein Angreifer füllen und so den Admin aussperren
// könnte (ausgerechnet, wenn er "Alarm an alle" braucht).
const maxThrottleSources = 500

// Hit ist das Ergebnis eines Versuchs: Blocked = der Absender ist gesperrt
// (der Versuch wurde nicht gezählt); Count = seine Versuche im Fenster (0:
// nicht gezählt, weil die Bremse voll ist).
type Hit struct {
	Blocked bool
	Count   int
}

// ThrottleHit zählt einen Versuch des Absenders who (ThrottleKey) in der
// Bremse bucket ("login" oder "mute"; jede andere ist ein Fehler im
// Programm). Prüfen und Zählen sind ein Schritt – N gleichzeitige Versuche
// bekommen nicht N Plätze. Abgelaufene Fenster (älter als window, gemessen
// ab dem ersten Versuch) werden dabei aufgeräumt. Gesperrt bzw. nicht
// gezählt ändert nichts (es gibt dann auch nichts zu sichern).
func ThrottleHit(c *core.Core, bucket, who string, max int, window time.Duration) Hit {
	pick := throttleBucket(bucket)
	now := c.Clock.Now().Unix()
	win := int64(window / time.Second)
	// Vorab am Schnappschuss: gesperrt oder voll ändert nichts – kein
	// Kopieren, kein Sichern (unter einem Angriff schriebe der Server sonst
	// bei jedem Aufruf runtime.json).
	if _, h, final := throttleDecide(*pick(c.Store.Runtime()), who, max, win, now); final {
		return h
	}
	var res Hit
	c.Store.UpdateRuntime(func(d *model.Runtime) {
		m := pick(d)
		if *m == nil {
			*m = map[string]model.ThrottleEntry{}
		}
		for k, e := range *m {
			if now-e.FirstAt > win {
				delete(*m, k)
			}
		}
		// Zwischen Vorab-Prüfung und hier kann ein anderer Versuch gezählt
		// haben: noch einmal entscheiden, jetzt unter dem Schreib-Halter.
		e, h, final := throttleDecide(*m, who, max, win, now)
		if final {
			res = h
			return
		}
		e.Count++
		(*m)[who] = e
		res = Hit{Count: e.Count}
	})
	return res
}

// throttleDecide entscheidet wie PHPs throttle_hit, ohne etwas zu ändern:
// e ist der laufende Eintrag von who (ein abgelaufener zählt wie keiner,
// ein neuer beginnt jetzt). final: Das Ergebnis h steht ohne Zählen fest –
// who ist neu und die Bremse voll (nicht gezählt, nicht gesperrt), oder who
// hat schon max Versuche (gesperrt).
func throttleDecide(m map[string]model.ThrottleEntry, who string, max int, win, now int64) (e model.ThrottleEntry, h Hit, final bool) {
	e, ok := m[who]
	if !ok || now-e.FirstAt > win {
		live := 0
		for _, x := range m {
			if now-x.FirstAt <= win {
				live++
			}
		}
		if live >= maxThrottleSources {
			return e, Hit{}, true
		}
		e = model.ThrottleEntry{FirstAt: now}
	}
	if e.Count >= max {
		return e, Hit{Blocked: true, Count: e.Count}, true
	}
	return e, Hit{}, false
}

// ThrottleClear nimmt den Absender who aus der Bremse bucket (PHP:
// throttle_clear, nach einer erfolgreichen Anmeldung). Hat er keinen
// Eintrag, ändert sich nichts.
func ThrottleClear(c *core.Core, bucket, who string) {
	pick := throttleBucket(bucket)
	if _, ok := (*pick(c.Store.Runtime()))[who]; !ok {
		return
	}
	c.Store.UpdateRuntime(func(d *model.Runtime) { delete(*pick(d), who) })
}

// throttleBucket liefert den Zugriff auf die Zähler einer Bremse. Es gibt
// genau "login" und "mute" (Runtime.Throttle); ein anderer Name ist ein
// Fehler im Programm und fällt beim ersten Aufruf auf.
func throttleBucket(bucket string) func(*model.Runtime) *map[string]model.ThrottleEntry {
	switch bucket {
	case "login":
		return func(r *model.Runtime) *map[string]model.ThrottleEntry { return &r.Throttle.Login }
	case "mute":
		return func(r *model.Runtime) *map[string]model.ThrottleEntry { return &r.Throttle.Mute }
	}
	panic(fmt.Sprintf("web: unbekannte Bremse %q (es gibt login und mute)", bucket))
}
