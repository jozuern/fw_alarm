package store

import (
	"cmp"
	"slices"
	"time"

	"fwalarm/internal/model"
)

// Die Leseseite des Verlaufs: History und LogAndWait. Beide lesen nur den
// Speicher – nie eine Datei.

// History liefert Verlaufseinträge, neueste zuerst – aus dem Speicher, nie
// aus einer Datei: zuerst, was noch nicht angehängt ist (übergeben, oder
// noch in der outbox eines geltenden Schnappschusses), nach log_at, dann die
// Zeilen von history.jsonl und history.1.jsonl rückwärts. Kaputte Zeilen
// werden übersprungen; keep (nil = alle) wählt aus, limit begrenzt. Jeder
// Eintrag ist eine eigene Kopie.
func (s *Store) History(limit int, keep func(model.Entry) bool) []model.Entry {
	out := make([]model.Entry, 0, min(max(limit, 0), 100))
	if limit <= 0 {
		return out
	}
	pend, old, cur := s.historyView()
	slices.SortStableFunc(pend, func(a, b model.Entry) int { return cmp.Compare(a.Int("log_at"), b.Int("log_at")) })
	add := func(e model.Entry) bool {
		if keep == nil || keep(e) {
			out = append(out, e)
		}
		return len(out) >= limit
	}
	for i := len(pend) - 1; i >= 0; i-- {
		if e, ok := copyEntry(pend[i]); ok && add(e) {
			return out
		}
	}
	for _, lines := range [][][]byte{cur, old} {
		for i := len(lines) - 1; i >= 0; i-- {
			if e, ok := parseLine(lines[i]); ok && add(e) {
				return out
			}
		}
	}
	return out
}

// historyView nimmt unter der Sperre des Verlaufs, was History braucht: die
// Einträge, die noch nicht angehängt sind (Warteschlange, dann die outboxen
// der geltenden Schnappschüsse ohne Bekanntes), und die Zeilen. Gleichzeitig
// gelesen, damit kein Eintrag fehlt oder doppelt erscheint, während der
// Schreiber anhängt.
func (s *Store) historyView() (pend []model.Entry, old, cur [][]byte) {
	h := &s.hist
	h.mu.Lock()
	defer h.mu.Unlock()
	st, rt := s.state.Load(), s.runtime.Load()
	pend = make([]model.Entry, 0, len(h.queue))
	seen := make(map[string]bool, len(h.queue))
	for _, q := range h.queue {
		pend = append(pend, q.e)
		seen[q.uid] = true
	}
	for _, box := range [][]model.Entry{st.Outbox, rt.Outbox} {
		for _, e := range box {
			uid := e.Str("uid")
			if _, known := h.known[uid]; known || seen[uid] {
				continue // in der Warteschlange oder schon angehängt
			}
			seen[uid] = true
			pend = append(pend, e)
		}
	}
	return pend, h.old, h.cur
}

// LogAndWait legt den Eintrag wie Log in die outbox der flüchtigen Daten
// und wartet, bis er in history.jsonl steht (true) oder maxWait vergangen
// ist (false – er kommt dann später an). Für "Alarm gestartet": darauf wird
// höchstens 300 ms gewartet, eine tote Platte hält den Alarm nicht auf.
func (s *Store) LogAndWait(e model.Entry, maxWait time.Duration) bool {
	c, ok := s.entryCopy(e)
	if !ok {
		return false
	}
	uid := c.Str("uid")
	ch := s.hist.addWaiter(uid)
	s.UpdateRuntime(func(d *model.Runtime) { d.Log(c) })
	select {
	case <-ch:
		return true
	case <-s.clk.After(maxWait):
	}
	s.hist.dropWaiter(uid, ch)
	select { // gerade noch angehängt?
	case <-ch:
		return true
	default:
		return false
	}
}

// addWaiter meldet einen Wartenden für uid an; der Kanal schließt, sobald
// der Eintrag angehängt ist.
func (h *history) addWaiter(uid string) chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan struct{})
	h.waiters[uid] = append(h.waiters[uid], ch)
	return ch
}

// dropWaiter nimmt einen Wartenden wieder heraus.
func (h *history) dropWaiter(uid string, ch chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.waiters[uid] = slices.DeleteFunc(h.waiters[uid], func(c chan struct{}) bool { return c == ch })
	if len(h.waiters[uid]) == 0 {
		delete(h.waiters, uid)
	}
}
