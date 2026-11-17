package model

import "encoding/json"

// Tiefe Kopien (Spec §6.2): Jede Änderung arbeitet auf einer Kopie, die mit
// dem veröffentlichten Schnappschuss keine Liste, keine Tabelle und kein
// Zeigerziel teilt. Fehlende Listen und Tabellen (nil) legt die Kopie leer
// an – so schreibt ein Dokument nie null, wo der strenge Leser eine Liste
// oder Tabelle verlangt. Ein neues Feld mit Liste, Tabelle oder Zeiger
// braucht hier seine Zeile; TestCloneSharesNothing fällt sonst um.

// Clone liefert eine tiefe Kopie des Zustands.
func (s *State) Clone() *State {
	c := *s
	c.Recipients = copySlice(s.Recipients)
	c.Notify.Global = copyMap(s.Notify.Global)
	c.Notify.Types = make(map[string]map[string]string, len(s.Notify.Types))
	for kind, values := range s.Notify.Types {
		c.Notify.Types[kind] = copyMap(values)
	}
	if s.Commands.Current != nil {
		cmd := *s.Commands.Current
		c.Commands.Current = &cmd
	}
	c.Commands.RecentTypes = copySlice(s.Commands.RecentTypes)
	c.Outbox = copyEntries(s.Outbox)
	return &c
}

// Clone liefert eine tiefe Kopie der Laufzeitdaten.
func (r *Runtime) Clone() *Runtime {
	c := *r
	if r.Box != nil {
		box := *r.Box
		box.Fields = copySlice(r.Box.Fields)
		c.Box = &box
	}
	c.AlarmsSeen = copyMap(r.AlarmsSeen)
	c.Devices = copyMap(r.Devices)
	if r.Watcher != nil {
		w := *r.Watcher
		c.Watcher = &w
	}
	c.Watch.RetryAfter = copyMap(r.Watch.RetryAfter)
	c.Watch.Pending = copySlice(r.Watch.Pending)
	c.Throttle.Login = copyMap(r.Throttle.Login)
	c.Throttle.Mute = copyMap(r.Throttle.Mute)
	c.Outbox = copyEntries(r.Outbox)
	return &c
}

// Clone liefert eine tiefe Kopie der Sitzungen.
func (s *Sessions) Clone() *Sessions {
	c := *s
	c.Sessions = copyMap(s.Sessions)
	return &c
}

// copySlice kopiert eine Liste aus Werten ohne eigene Verweise; nie nil.
func copySlice[T any](s []T) []T {
	return append(make([]T, 0, len(s)), s...)
}

// copyMap kopiert eine Tabelle aus Werten ohne eigene Verweise; nie nil.
func copyMap[K comparable, V any](m map[K]V) map[K]V {
	c := make(map[K]V, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

func copyEntries(box []Entry) []Entry {
	c := make([]Entry, len(box))
	for i, e := range box {
		c[i] = copyEntry(e)
	}
	return c
}

func copyEntry(e Entry) Entry {
	c := make(Entry, len(e))
	for k, v := range e {
		c[k] = copyAny(v)
	}
	return c
}

// copyAny kopiert einen JSON-Wert tief. Was keine JSON-Form hat (an
// NewEntry vorbei hineingeraten), wird in seine JSON-Form gebracht – auch
// das teilt nichts.
func copyAny(v any) any {
	switch x := v.(type) {
	case nil, string, bool, json.Number, float64, int, int64:
		return x
	case map[string]any:
		c := make(map[string]any, len(x))
		for k, item := range x {
			c[k] = copyAny(item)
		}
		return c
	case []any:
		c := make([]any, len(x))
		for i, item := range x {
			c[i] = copyAny(item)
		}
		return c
	}
	return jsonValue(v)
}
