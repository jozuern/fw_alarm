package web

import (
	"context"
	"slices"
	"sync"
	"time"

	"fwalarm/internal/clock"
	"fwalarm/internal/core"
)

// Plätze für Passwortprüfungen (Spec §6.8). Eine Prüfung kostet Rechenzeit
// und Speicher (Argon2id bis 64 MiB), deshalb:
//   - höchstens zwei zugleich, höchstens eine je Absender (ThrottleKey) – in
//     beiden Reihen zusammen;
//   - wer warten muss, wartet höchstens 5 s, danach LoginBusy (503), nicht
//     gezählt (der Versuch wurde nie geprüft);
//   - zwei Reihen: Versuche mit gültigem Geräte-Ausweis (device.go) warten
//     in der Vorzugsreihe, die zuerst bedient wird – der Reihe nach je
//     Ausweis, je Ausweis höchstens ein Platz darin (wartend oder in
//     Prüfung), höchstens 16 Wartende; alle anderen in der allgemeinen Reihe
//     – der Reihe nach je Absender, höchstens 64 Wartende, höchstens 2 je
//     Absender. Wer in der Vorzugsreihe keinen Platz findet, wartet in der
//     allgemeinen; wer dort keinen findet, bekommt sofort LoginBusy.
//
// Der Grund für die Vorzugsreihe (Durchsicht von Aufgabe 12): In einer
// einzigen, je Absender gerechten Reihe stünde der Admin hinter JEDEM
// Absender, der gerade einen Versuch warten lässt – rund hundert Adressen
// schöben jede Anmeldung über die 5 s.

const (
	verifySlots         = 2               // Prüfungen zugleich
	verifyMaxWait       = 5 * time.Second // längstes Warten auf einen Platz
	maxGeneralWaiting   = 64              // Wartende in der allgemeinen Reihe (mehr ließe sich in 5 s nicht prüfen)
	maxWaitingPerSender = 2               // davon je Absender
	maxPriorityWaiting  = 16              // Wartende in der Vorzugsreihe
)

// slotQueue verteilt die Plätze. Zustände eines Versuchs: wartend in der
// Vorzugsreihe (prio) oder in der allgemeinen (waiting/turn), prüfend (sein
// Absender in busy; mit Ausweis dessen id in seated), fertig. Nach jeder
// Änderung gilt: Ist ein Platz frei, wartet niemand, der ihn nehmen dürfte
// (dispatch hat ihn vergeben).
type slotQueue struct {
	mu   sync.Mutex
	free int             // freie Plätze
	busy map[string]bool // Absender mit laufender Prüfung (beide Reihen)

	// Vorzugsreihe: Wartende in Ankunftsfolge, je Ausweis-id höchstens einer;
	// seated: ids mit Platz darin – wartend oder in Prüfung.
	prio   []*slotWaiter
	seated map[string]bool

	// Allgemeine Reihe: Wartende je Absender in Ankunftsfolge; turn sind die
	// Absender mit Wartenden, vorn, wer am längsten nicht dran war; general
	// zählt alle Wartenden darin.
	waiting map[string][]*slotWaiter
	turn    []string
	general int
}

type slotWaiter struct {
	who   string        // Absender
	pass  string        // Ausweis-id in der Vorzugsreihe, "" = allgemeine Reihe
	ready chan struct{} // geschlossen = Platz zugeteilt
}

// slotsByCore: eine Verteilung je Core (im Programm gibt es genau einen; in
// Tests hat jeder Prüfstand seine eigene).
var slotsByCore sync.Map // *core.Core → *slotQueue

func slotsFor(c *core.Core) *slotQueue {
	if q, ok := slotsByCore.Load(c); ok {
		return q.(*slotQueue)
	}
	q, _ := slotsByCore.LoadOrStore(c, &slotQueue{free: verifySlots, busy: map[string]bool{},
		seated: map[string]bool{}, waiting: map[string][]*slotWaiter{}})
	return q.(*slotQueue)
}

// acquire wartet auf einen Platz für den Absender who; pass ist die id
// eines gültigen Ausweises ("" = keiner). Höchstens 5 s auf der Uhr clk und
// nur, solange der Aufrufer noch da ist (ctx). ok: zugeteilt; release gibt
// den Platz zurück (mehrfach rufbar). Sonst – Frist abgelaufen, Aufrufer
// fort oder die allgemeine Reihe voll (dann sofort, ohne Zeitgeber) – steht
// der Versuch in keiner Reihe mehr.
func (q *slotQueue) acquire(ctx context.Context, clk clock.Clock, who, pass string) (release func(), ok bool) {
	q.mu.Lock()
	if pass != "" && q.seated[pass] {
		pass = "" // dieser Ausweis hat seinen Platz schon: allgemeine Reihe
	}
	if q.free > 0 && !q.busy[who] {
		// Frei und who prüft gerade nicht: Dann wartet auch niemand, der dran
		// wäre (dispatch hätte ihm den Platz schon gegeben) – kein Zeitgeber.
		q.take(who, pass)
		q.mu.Unlock()
		return q.releaser(who, pass), true
	}
	if pass != "" && len(q.prio) >= maxPriorityWaiting {
		pass = "" // Vorzugsreihe voll: allgemeine Reihe
	}
	w := &slotWaiter{who: who, pass: pass, ready: make(chan struct{})}
	if pass != "" {
		q.seated[pass] = true
		q.prio = append(q.prio, w)
	} else {
		if q.general >= maxGeneralWaiting || len(q.waiting[who]) >= maxWaitingPerSender {
			q.mu.Unlock()
			return nil, false
		}
		if len(q.waiting[who]) == 0 {
			q.turn = append(q.turn, who)
		}
		q.waiting[who] = append(q.waiting[who], w)
		q.general++
	}
	q.mu.Unlock()

	select {
	case <-w.ready:
		return q.releaser(who, pass), true
	case <-clk.After(verifyMaxWait):
	case <-ctx.Done():
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	select {
	case <-w.ready:
		// Genau beim Ablauf zugeteilt: Der Platz gehört schon diesem Versuch.
		return q.releaser(who, pass), true
	default:
	}
	q.leave(w)
	return nil, false
}

// take belegt einen Platz für who; mit Ausweis hält dessen id ihren Platz
// in der Vorzugsreihe, bis die Prüfung endet (unter mu).
func (q *slotQueue) take(who, pass string) {
	q.free--
	q.busy[who] = true
	if pass != "" {
		q.seated[pass] = true
	}
}

// releaser: die Rückgabe des Platzes von who, höchstens einmal wirksam.
func (q *slotQueue) releaser(who, pass string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			q.mu.Lock()
			defer q.mu.Unlock()
			q.free++
			delete(q.busy, who)
			if pass != "" {
				delete(q.seated, pass)
			}
			q.dispatch()
		})
	}
}

// dispatch gibt freie Plätze an Wartende (unter mu): zuerst die
// Vorzugsreihe (der älteste, dessen Absender gerade nicht prüft), dann die
// allgemeine reihum je Absender – der erste in turn, der gerade nicht prüft,
// bekommt seinen ältesten Wartenden und rückt ans Ende, wenn er noch weitere
// hat.
func (q *slotQueue) dispatch() {
	for q.free > 0 {
		if i := slices.IndexFunc(q.prio, func(w *slotWaiter) bool { return !q.busy[w.who] }); i >= 0 {
			w := q.prio[i]
			q.prio = slices.Delete(q.prio, i, i+1)
			q.take(w.who, w.pass)
			close(w.ready)
			continue
		}
		i := slices.IndexFunc(q.turn, func(who string) bool { return !q.busy[who] })
		if i < 0 {
			return
		}
		who := q.turn[i]
		q.turn = slices.Delete(q.turn, i, i+1)
		ws := q.waiting[who]
		w := ws[0]
		if len(ws) > 1 {
			q.waiting[who] = ws[1:]
			q.turn = append(q.turn, who)
		} else {
			delete(q.waiting, who)
		}
		q.general--
		q.take(who, "")
		close(w.ready)
	}
}

// leave nimmt den Wartenden w aus seiner Reihe (unter mu). Kosten: höchstens
// die Länge einer Reihe (16 bzw. 2 je Absender plus 64 in turn).
func (q *slotQueue) leave(w *slotWaiter) {
	if w.pass != "" {
		q.prio = slices.DeleteFunc(q.prio, func(x *slotWaiter) bool { return x == w })
		delete(q.seated, w.pass)
		return
	}
	q.general--
	ws := slices.DeleteFunc(q.waiting[w.who], func(x *slotWaiter) bool { return x == w })
	if len(ws) > 0 {
		q.waiting[w.who] = ws
		return
	}
	delete(q.waiting, w.who)
	q.turn = slices.DeleteFunc(q.turn, func(x string) bool { return x == w.who })
}
