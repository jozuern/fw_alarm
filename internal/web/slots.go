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
//     allen Reihen zusammen;
//   - wer warten muss, wartet höchstens 5 s, danach LoginBusy (503), nicht
//     gezählt (der Versuch wurde nie geprüft);
//   - drei Reihen, bedient in fester Folge: Versuche mit gültigem
//     Geräte-Ausweis (device.go) des Admin-Kontos, dann die mit einem des
//     Lese-Kontos, dann alle anderen. In den beiden Ausweis-Reihen geht es
//     der Reihe nach je Ausweis, je Ausweis höchstens ein Platz (wartend oder
//     in Prüfung), höchstens 16 Wartende je Reihe; in der allgemeinen der
//     Reihe nach je Absender, höchstens 64 Wartende, höchstens 2 je Absender.
//     Wer in seiner Ausweis-Reihe keinen Platz findet, wartet in der
//     allgemeinen; wer dort keinen findet, bekommt sofort LoginBusy.
//   - Admin vor Lese-Konto: Wer nur das Lese-Passwort kennt, kann sich
//     beliebig viele Ausweise holen – er darf damit den Admin nicht
//     verdrängen (Durchsicht von Aufgabe 12). Die Rolle eines Ausweises ist
//     echt: Sein Schlüssel hängt am Passwort-Hash seines Kontos.
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
	maxPriorityWaiting  = 16              // Wartende je Ausweis-Reihe
)

// Ausweis-Reihen in der Folge, in der sie bedient werden.
const (
	classAdmin  = iota // Ausweise des Admin-Kontos
	classReader        // Ausweise des Lese-Kontos
	passClasses
)

// passClass: die Reihe eines Ausweises.
func passClass(p devicePass) int {
	if p.role == "admin" {
		return classAdmin
	}
	return classReader
}

// seatKey: der Platz eines Ausweises in seiner Reihe – Rolle und id.
func seatKey(p devicePass) string { return p.role + ":" + p.id }

// slotQueue verteilt die Plätze. Zustände eines Versuchs: wartend in einer
// Ausweis-Reihe (prio) oder in der allgemeinen (waiting/turn), prüfend (sein
// Absender in busy; mit Ausweis dessen Platz in seated), fertig. Nach jeder
// Änderung gilt: Ist ein Platz frei, wartet niemand, der ihn nehmen dürfte
// (dispatch hat ihn vergeben).
type slotQueue struct {
	mu   sync.Mutex
	free int             // freie Plätze
	busy map[string]bool // Absender mit laufender Prüfung (alle Reihen)

	// Ausweis-Reihen (classAdmin, classReader): Wartende in Ankunftsfolge, je
	// Ausweis höchstens einer; seated: Ausweise (seatKey) mit Platz darin –
	// wartend oder in Prüfung.
	prio   [passClasses][]*slotWaiter
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
	seat  string        // Platz in einer Ausweis-Reihe (seatKey), "" = allgemeine Reihe
	class int           // seine Ausweis-Reihe (nur mit seat)
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

// acquire wartet auf einen Platz für den Absender who; passes sind die
// gültigen Ausweise der Anfrage (devicePassesOf, leer = keiner). Höchstens
// 5 s auf der Uhr clk und nur, solange der Aufrufer noch da ist (ctx). ok:
// zugeteilt; release gibt den Platz zurück (mehrfach rufbar). Sonst – Frist
// abgelaufen, Aufrufer fort oder die allgemeine Reihe voll (dann sofort,
// ohne Zeitgeber) – steht der Versuch in keiner Reihe mehr.
//
// Welche Reihe: der erste Ausweis der besten Klasse (Admin vor Lese-Konto),
// dessen Platz noch frei ist (sonst hielte ein Ausweis zwei Plätze); ist
// seine Reihe voll (16), die allgemeine.
func (q *slotQueue) acquire(ctx context.Context, clk clock.Clock, who string, passes []devicePass) (release func(), ok bool) {
	q.mu.Lock()
	seat, class := q.pickSeat(passes)
	if q.free > 0 && !q.busy[who] {
		// Frei und who prüft gerade nicht: Dann wartet auch niemand, der dran
		// wäre (dispatch hätte ihm den Platz schon gegeben) – kein Zeitgeber.
		q.take(who, seat)
		q.mu.Unlock()
		return q.releaser(who, seat), true
	}
	if seat != "" && len(q.prio[class]) >= maxPriorityWaiting {
		seat = "" // Ausweis-Reihe voll: allgemeine Reihe
	}
	w := &slotWaiter{who: who, seat: seat, class: class, ready: make(chan struct{})}
	if seat != "" {
		q.seated[seat] = true
		q.prio[class] = append(q.prio[class], w)
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
		return q.releaser(who, seat), true
	case <-clk.After(verifyMaxWait):
	case <-ctx.Done():
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	select {
	case <-w.ready:
		// Genau beim Ablauf zugeteilt: Der Platz gehört schon diesem Versuch.
		return q.releaser(who, seat), true
	default:
	}
	q.leave(w)
	return nil, false
}

// pickSeat: der Platz (seatKey) und die Reihe für die Ausweise passes – der
// erste der besten Klasse, dessen Platz frei ist; "" = keiner (unter mu).
func (q *slotQueue) pickSeat(passes []devicePass) (seat string, class int) {
	for class := range passClasses {
		for _, p := range passes {
			if passClass(p) == class && !q.seated[seatKey(p)] {
				return seatKey(p), class
			}
		}
	}
	return "", 0
}

// take belegt einen Platz für who; mit Ausweis hält dieser seinen Platz in
// seiner Reihe, bis die Prüfung endet (unter mu).
func (q *slotQueue) take(who, seat string) {
	q.free--
	q.busy[who] = true
	if seat != "" {
		q.seated[seat] = true
	}
}

// releaser: die Rückgabe des Platzes von who, höchstens einmal wirksam.
func (q *slotQueue) releaser(who, seat string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			q.mu.Lock()
			defer q.mu.Unlock()
			q.free++
			delete(q.busy, who)
			if seat != "" {
				delete(q.seated, seat)
			}
			q.dispatch()
		})
	}
}

// dispatch gibt freie Plätze an Wartende (unter mu): zuerst die Ausweis-
// Reihen in ihrer Folge (je Reihe der älteste, dessen Absender gerade nicht
// prüft), dann die allgemeine reihum je Absender – der erste in turn, der
// gerade nicht prüft, bekommt seinen ältesten Wartenden und rückt ans Ende,
// wenn er noch weitere hat.
func (q *slotQueue) dispatch() {
	for q.free > 0 {
		if q.dispatchPass() {
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

// dispatchPass gibt einen freien Platz an den ersten Wartenden der ersten
// Ausweis-Reihe, dessen Absender gerade nicht prüft; false = keiner (unter
// mu).
func (q *slotQueue) dispatchPass() bool {
	for class := range passClasses {
		i := slices.IndexFunc(q.prio[class], func(w *slotWaiter) bool { return !q.busy[w.who] })
		if i < 0 {
			continue
		}
		w := q.prio[class][i]
		q.prio[class] = slices.Delete(q.prio[class], i, i+1)
		q.take(w.who, w.seat)
		close(w.ready)
		return true
	}
	return false
}

// leave nimmt den Wartenden w aus seiner Reihe (unter mu). Kosten: höchstens
// die Länge einer Reihe (16 bzw. 2 je Absender plus 64 in turn).
func (q *slotQueue) leave(w *slotWaiter) {
	if w.seat != "" {
		q.prio[w.class] = slices.DeleteFunc(q.prio[w.class], func(x *slotWaiter) bool { return x == w })
		delete(q.seated, w.seat)
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
