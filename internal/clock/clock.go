// Package clock liefert die Uhr des Programms: die echte und – für Tests –
// eine gestellte, die nur weiterläuft, wenn der Test sie weiterstellt.
//
// Zeitgeber (After, Sleep) messen vergangene Zeit, keine Uhrzeiten: Ein
// Sprung der Wanduhr (NTP, Umstellung von Hand) verschiebt sie nicht.
package clock

import (
	"sort"
	"sync"
	"time"
)

// Clock ist alles, was das Programm von einer Uhr braucht.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
	Sleep(d time.Duration)
}

type realClock struct{}

// Real liefert die Uhr des Rechners.
func Real() Clock { return realClock{} }

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (realClock) Sleep(d time.Duration)                  { time.Sleep(d) }

// Fake ist eine gestellte Uhr – nur für Tests. Sie steht, bis Advance sie
// weiterstellt; Set verstellt nur die Wanduhr.
type Fake struct {
	mu      sync.Mutex
	now     time.Time     // was Now() liefert
	elapsed time.Duration // vergangene Zeit seit NewFake – nur Advance zählt sie hoch
	timers  []fakeTimer
}

type fakeTimer struct {
	due time.Duration // fällig, sobald elapsed diesen Wert erreicht
	ch  chan time.Time
}

// NewFake liefert eine gestellte Uhr, die bei start steht.
func NewFake(start time.Time) *Fake { return &Fake{now: start} }

// Now liefert die gestellte Wanduhr.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// After liefert einen Kanal, auf dem nach d vergangener Zeit (über Advance)
// die dann gültige Wanduhr ankommt. d ≤ 0 löst sofort aus.
func (f *Fake) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan time.Time, 1) // gepuffert: Auslösen blockiert nie
	if d <= 0 {
		ch <- f.now
		return ch
	}
	f.timers = append(f.timers, fakeTimer{due: f.elapsed + d, ch: ch})
	return ch
}

// Sleep kehrt zurück, sobald Advance d weitergestellt hat.
func (f *Fake) Sleep(d time.Duration) {
	if d <= 0 {
		return
	}
	<-f.After(d)
}

// Advance stellt Wanduhr und vergangene Zeit um d weiter und löst alle
// Zeitgeber aus, die dabei fällig werden – in der Reihenfolge ihrer
// Fälligkeit, jeden mit der Wanduhr seines Zeitpunkts.
func (f *Fake) Advance(d time.Duration) {
	if d < 0 {
		d = 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	target := f.elapsed + d
	sort.SliceStable(f.timers, func(i, j int) bool { return f.timers[i].due < f.timers[j].due })
	keep := f.timers[:0]
	for _, tm := range f.timers {
		if tm.due <= target {
			f.now = f.now.Add(tm.due - f.elapsed)
			f.elapsed = tm.due
			tm.ch <- f.now
			continue
		}
		keep = append(keep, tm)
	}
	f.timers = keep
	f.now = f.now.Add(target - f.elapsed)
	f.elapsed = target
}

// Set verstellt die Wanduhr (Sprung durch NTP oder von Hand). Zeitgeber
// bleiben, wie sie sind: Sie zählen vergangene Zeit, nicht Uhrzeiten.
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t
}

// Pending nennt die Zahl der Zeitgeber, die noch nicht ausgelöst haben –
// damit ein Test warten kann, bis eine Goroutine schläft, bevor er die Uhr
// weiterstellt.
func (f *Fake) Pending() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.timers)
}
