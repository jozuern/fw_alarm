package model

import (
	"cmp"
	"slices"
	"time"
)

// Befehle an die Box (PHP: api/poll.php, api/ack.php, die Zweige enqueue und
// cancel in dashboard_api.php, expire_stale_pending und
// remember_command_type in lib/common.php): die reinen Regeln ihres
// Lebenslaufs, alle auf dem Entwurf EINER Änderung der Ablage – keine Uhr
// außer now, keine Platte, kein Netz.
//
//	angelegt (pending) ──Poll──▶ ausgeliefert (delivered) ──Quittung──▶ quittiert (acked)
//	    │  └──zu alt──▶ verfallen (expired)
//	    └──Admin oder Sicherung des Demo-Alarms──▶ abgebrochen (cancelled)
//
// Das eine Versprechen, nach dem sich alles richtet: Ein Befehl wird
// HÖCHSTENS EINMAL ausgeliefert. Ein Befehl, den die Box zweimal ausführt,
// ist ein zweiter Alarm auf den Telefonen der Kollegen. Deshalb entscheidet
// das Sichern: Die Antwort des Polls trägt einen Befehl nur, wenn die
// Änderung, die ihn "ausgeliefert" nennt, gesichert ist (internal/box) –
// und ausgeliefert wird nur, was wartet.

// Arten von Befehlen (PHP: enqueue nimmt nur diese beiden).
const (
	CommandTest  = "TEST"  // Test über die Box: Status-Push an den Admin
	CommandAlarm = "ALARM" // Demo-Alarm über die Box – nur im Demo-Modus
)

// Sätze, wörtlich aus dashboard_api.php und api/poll.php.
const (
	// MsgInvalidCommand: ein anderer Typ als TEST/ALARM (enqueue).
	MsgInvalidCommand = "Ungültiger Befehl."
	msgCommandWaiting = "Es wartet bereits ein Befehl (abbrechen oder warten)."
	msgAwaitingAck    = "Der letzte Befehl wurde an die Box ausgeliefert, ihre Bestätigung steht noch aus."
	// DemoGuardBy und DemoGuardReason: Wer und warum ein Demo-Alarm beim
	// Ausliefern abgebrochen wird, wenn er nicht mehr sicher ist (poll.php
	// 88-92) – die Oberfläche zeigt den Grund ("… abgebrochen – <Grund>").
	DemoGuardBy     = "Sicherung"
	DemoGuardReason = "Demo-Stand seit dem Anlegen geändert - nicht ausgeliefert."
)

// maxBoxCommandID: größte Befehlsnummer der Box, die zählt (2^31 − 1). Die
// Firmware liest Nummern als long (32 Bit); was darüber liegt, ist Unsinn und
// höbe die Nummern des Servers über das, was die Box noch lesen kann.
const maxBoxCommandID = 1<<31 - 1

// AckKind sagt, was eine Quittung der Box bewirkt hat (PHP api/ack.php).
type AckKind int

const (
	// AckUnknown: verspätete Quittung für eine Nummer, die der Server nicht
	// (mehr) kennt – Eintrag late_ack ohne Typ, sonst nichts.
	AckUnknown AckKind = iota
	// AckFirst: Die Quittung gilt – ein ausgelieferter Befehl ist quittiert
	// (oder ein quittierter mit anderem Ergebnis neu); Eintrag acked.
	AckFirst
	// AckRepeat: dieselbe Quittung noch einmal (die erste Antwort kam für
	// die Box zu spät) – nur bestätigen, nichts ändern, kein Eintrag.
	AckRepeat
	// AckLate: verspätete Quittung für einen Befehl, dessen Typ der Server
	// noch kennt (laufend, aber nicht ausgeliefert; oder abgelöst und in
	// recent_types) – Eintrag late_ack mit dem Typ, der Befehl bleibt, wie er
	// ist (ein wartender wird danach ganz normal ausgeliefert).
	AckLate
)

// CommandMember ist ein Feld eines Befehls, wie PHP es in seinem Array führt
// (Name, Wert).
type CommandMember struct {
	Key   string
	Value any
}

// Members: die Felder des Befehls, wie PHP sein Array führt – gespeichert
// sind immer alle (das Format ist fest), gezeigt werden nur die, die sein Weg
// gesetzt hat, in der Reihenfolge, in der PHP sie setzt: id, type, state,
// created_at, created_by; demo_changed_at (nur ALARM); delivered_at,
// delivered_to (wenn ausgeliefert); acked_at, ack_result, ack_message,
// acked_by (wenn quittiert – auch leere Texte); cancelled_at, cancelled_by
// (wenn abgebrochen); cancel_reason (nur mit Grund); expired_at (wenn
// verfallen). So stehen die Befehle in den Antworten von enqueue, cancel und
// status und im Verlauf – ein Abbruch ohne Grund trägt dort kein
// cancel_reason, und log_view zeigt dann keine message (wie PHP).
func (c Command) Members() []CommandMember {
	m := []CommandMember{{"id", c.ID}, {"type", c.Type}, {"state", c.State}, {"created_at", c.CreatedAt}, {"created_by", c.CreatedBy}}
	if c.Type == CommandAlarm {
		m = append(m, CommandMember{"demo_changed_at", c.DemoChangedAt})
	}
	if c.DeliveredAt != 0 {
		m = append(m, CommandMember{"delivered_at", c.DeliveredAt}, CommandMember{"delivered_to", c.DeliveredTo})
	}
	if c.AckedAt != 0 {
		m = append(m, CommandMember{"acked_at", c.AckedAt}, CommandMember{"ack_result", c.AckResult},
			CommandMember{"ack_message", c.AckMessage}, CommandMember{"acked_by", c.AckedBy})
	}
	if c.CancelledAt != 0 {
		m = append(m, CommandMember{"cancelled_at", c.CancelledAt}, CommandMember{"cancelled_by", c.CancelledBy})
	}
	if c.CancelReason != "" {
		m = append(m, CommandMember{"cancel_reason", c.CancelReason})
	}
	if c.ExpiredAt != 0 {
		m = append(m, CommandMember{"expired_at", c.ExpiredAt})
	}
	return m
}

// commandEntry: der Verlaufseintrag eines Schritts (PHP:
// append_command_log(['event' => …, 'command' => $command])) – der Befehl
// danach mit seinen Members.
func commandEntry(event string, c Command, now time.Time) Entry {
	fields := map[string]any{}
	for _, m := range c.Members() {
		fields[m.Key] = m.Value
	}
	return NewEntry(event, now, map[string]any{"command": fields})
}

// PendingPast: Wartet der Befehl länger als ttl Sekunden (PHP
// expire_stale_pending: created_at > 0 und jetzt − created_at > ttl)? Ein
// Befehl an eine tote Box darf nicht Stunden später noch ausgeführt werden.
func (c Command) PendingPast(now time.Time, ttl int) bool {
	return c.State == CommandPending && c.CreatedAt > 0 && now.Unix()-c.CreatedAt > int64(ttl)
}

// Enqueue legt einen Befehl an (PHP: dashboard_api.php 237-282, unter dem
// Lock): typ genau TEST oder ALARM (sonst MsgInvalidCommand – großschreiben
// muss der Aufrufer), kein anderer wartet, und ein ausgelieferter ist
// entweder quittiert oder länger als lockSeconds ausgeliefert (sonst führte
// die Box womöglich gleich zwei aus, wenn nur ihre Quittung verloren ging).
// Die Nummer ist commands.next_id (mindestens 1), danach steigt next_id; ein
// ALARM hält den Demo-Stand fest (demo_changed_at), gegen den der Poll ihn
// beim Ausliefern prüft. Typ in recent_types, Eintrag created. "" = angelegt,
// sonst der Satz der Ablehnung (dann ist nichts geändert). Die Sperre des
// Demo-Alarms (DemoBlockReason) und das Verfallen eines alten Befehls
// (ExpirePending) prüft der Aufrufer vorher im selben Entwurf.
func (s *State) Enqueue(typ, by string, now time.Time, lockSeconds int) (Command, string) {
	if typ != CommandTest && typ != CommandAlarm {
		return Command{}, MsgInvalidCommand
	}
	if cur := s.Commands.Current; cur != nil {
		switch cur.State {
		case CommandPending:
			return Command{}, msgCommandWaiting
		case CommandDelivered:
			if cur.DeliveredAt > 0 && now.Unix()-cur.DeliveredAt < int64(lockSeconds) {
				return Command{}, msgAwaitingAck
			}
		}
	}
	id := max(1, s.Commands.NextID)
	cmd := Command{ID: id, Type: typ, State: CommandPending, CreatedAt: now.Unix(), CreatedBy: by}
	if typ == CommandAlarm {
		cmd.DemoChangedAt = s.Demo.ChangedAt
	}
	s.Commands.NextID = id + 1
	s.setCurrent(cmd)
	s.rememberType(id, typ)
	s.Log(commandEntry(EvCreated, cmd, now))
	return cmd, ""
}

// ExpirePending lässt einen zu lange wartenden Befehl verfallen (PHP
// expire_stale_pending): Zustand expired, expired_at, Eintrag expired. true =
// gerade verfallen.
func (s *State) ExpirePending(now time.Time, ttl int) bool {
	cur := s.Commands.Current
	if cur == nil || !cur.PendingPast(now, ttl) {
		return false
	}
	cur.State, cur.ExpiredAt = CommandExpired, now.Unix()
	s.Log(commandEntry(EvExpired, *cur, now))
	return true
}

// NoteBoxCommandID nimmt die Nummer, die die Box in diesem Lauf zuletzt
// gesehen hat (last_command_id des Polls; PHP api/poll.php 61-77): Neue
// Nummern müssen darüber liegen – sonst verwürfe die Box einen neuen Befehl
// still als Duplikat (etwa nachdem die Befehle des Servers verloren gingen
// und next_id wieder klein ist). next_id steigt über last; ein wartender
// Befehl, dessen Nummer die Box schon kennt, bekommt eine neue (in
// recent_types gemerkt; kein Verlaufseintrag, wie PHP). last ≤ 0 oder über
// 2^31 − 1 (unplausibel – die Firmware kann so große Nummern nicht lesen)
// zählt nicht. true = etwas geändert.
func (s *State) NoteBoxCommandID(last int) bool {
	if last <= 0 || last > maxBoxCommandID {
		return false
	}
	changed := false
	if last >= s.Commands.NextID {
		s.Commands.NextID = last + 1
		changed = true
	}
	if cur := s.Commands.Current; cur != nil && cur.State == CommandPending && cur.ID <= last {
		cur.ID = max(1, s.Commands.NextID)
		s.Commands.NextID = cur.ID + 1
		s.rememberType(cur.ID, cur.Type)
		changed = true
	}
	return changed
}

// Deliver: der wartende Befehl ist an die Box to ausgeliefert (PHP
// api/poll.php 100-110) – Zustand delivered, delivered_at, delivered_to,
// Eintrag delivered. Liefert eine Kopie des Befehls; nil, wenn nichts wartet
// (ein ausgelieferter wird NIE ein zweites Mal ausgeliefert). Ob er hinaus
// darf, entscheidet erst das Sichern dieser Änderung (internal/box).
func (s *State) Deliver(to string, now time.Time) *Command {
	cur := s.Commands.Current
	if cur == nil || cur.State != CommandPending {
		return nil
	}
	cur.State, cur.DeliveredAt, cur.DeliveredTo = CommandDelivered, now.Unix(), to
	s.Log(commandEntry(EvDelivered, *cur, now))
	out := *cur
	return &out
}

// Ack nimmt die Quittung der Box by für den Befehl id an (PHP api/ack.php
// 18-62; result und message hat der Aufrufer schon gekürzt):
//
//   - Laufender Befehl id, quittiert mit demselben Ergebnis: AckRepeat – die
//     Antwort kam für die Box zu spät; weder acked_at noch ein zweites acked.
//   - Laufender Befehl id, ausgeliefert oder quittiert: AckFirst – quittiert
//     mit acked_at, ack_result, ack_message, acked_by; Eintrag acked. Nur ein
//     AUSGELIEFERTER Befehl wird so quittiert: Ein wartender, abgebrochener
//     oder verfallener mit zufällig gleicher Nummer darf nie als "ausgeführt"
//     erscheinen.
//   - Sonst: Eintrag late_ack (id, type, result, message, device_id) – der
//     Typ vom laufenden Befehl mit dieser Nummer oder aus recent_types (die
//     verspätete Quittung eines Demo-Alarms gehört in den Alarm-Verlauf);
//     AckLate, wenn er bekannt ist, sonst AckUnknown. Der Befehl bleibt, wie
//     er ist.
func (s *State) Ack(id int, by, result, message string, now time.Time) AckKind {
	cur := s.Commands.Current
	if cur != nil && cur.ID == id {
		switch {
		case cur.State == CommandAcked && cur.AckResult == result:
			return AckRepeat
		case cur.State == CommandDelivered || cur.State == CommandAcked:
			cur.State, cur.AckedAt, cur.AckResult, cur.AckMessage, cur.AckedBy = CommandAcked, now.Unix(), result, message, by
			s.Log(commandEntry(EvAcked, *cur, now))
			return AckFirst
		}
	}
	typ, kind := "", AckUnknown
	switch {
	case cur != nil && cur.ID == id:
		typ, kind = cur.Type, AckLate
	default:
		if t, ok := s.typeOf(id); ok {
			typ, kind = t, AckLate
		}
	}
	s.Log(NewEntry(EvLateAck, now, map[string]any{"id": id, "type": typ, "result": result, "message": message, "device_id": by}))
	return kind
}

// Cancel bricht den wartenden Befehl ab (PHP: dashboard_api.php 289-305 mit
// by = Benutzer, ohne Grund; api/poll.php 88-97 mit DemoGuardBy und
// DemoGuardReason): Zustand cancelled, cancelled_at, cancelled_by, mit Grund
// cancel_reason; Eintrag cancelled (ohne Grund auch dort kein
// cancel_reason). Nur ein wartender – ein ausgelieferter ist nicht mehr
// abzubrechen (die Box hat ihn vielleicht schon ausgeführt). false = keiner
// wartet, nichts geändert.
func (s *State) Cancel(by, reason string, now time.Time) (Command, bool) {
	cur := s.Commands.Current
	if cur == nil || cur.State != CommandPending {
		return Command{}, false
	}
	cur.State, cur.CancelledAt, cur.CancelledBy, cur.CancelReason = CommandCancelled, now.Unix(), by, reason
	s.Log(commandEntry(EvCancelled, *cur, now))
	return *cur, true
}

// DemoAlarmStillSafe: Darf der Demo-Alarm cmd JETZT noch ausgeliefert werden
// (PHP: demo_alarm_still_safe in api/poll.php)? Beim Anlegen war geprüft, dass
// die Box die Demo-Liste hat (DemoBlockReason). Hat sich der Demo-Stand seither
// geändert (aus-, um- oder aus- und wieder eingeschaltet: changed_at ist
// anders) oder meldet die Box (box: ihr letzter Status, nil = nie gemeldet)
// nicht mehr genau einen Empfänger, könnte er an die volle Liste gehen – dann
// nicht.
func (s *State) DemoAlarmStillSafe(cmd Command, box *BoxStatus) bool {
	if !s.DemoEnabled() || cmd.DemoChangedAt != s.Demo.ChangedAt || box == nil {
		return false
	}
	return phpInt(box.Fields.Get("keys_count")) == 1
}

// PollCommand ist der Teil eines Polls der eingestellten Box, der die Befehle
// betrifft (PHP api/poll.php 54-113), auf dem Entwurf: die Nummer der Box
// (NoteBoxCommandID), ein zu alter wartender Befehl verfällt (ExpirePending,
// ttl = pending_command_ttl_seconds), ein Demo-Alarm, der nicht mehr sicher
// ist (DemoAlarmStillSafe gegen box), wird von der Sicherung abgebrochen –
// sonst wird der wartende an to ausgeliefert (Deliver). deliver: der
// ausgelieferte Befehl (eine Kopie) oder nil; changed: der Entwurf hat sich
// geändert. Ohne changed ist nichts zu sichern – ein Poll ohne Neues (der
// Normalfall alle paar Sekunden) schreibt nichts.
func (s *State) PollCommand(last int, to string, box *BoxStatus, now time.Time, ttl int) (deliver *Command, changed bool) {
	changed = s.NoteBoxCommandID(last)
	if s.ExpirePending(now, ttl) {
		changed = true
	}
	cur := s.Commands.Current
	if cur == nil || cur.State != CommandPending {
		return nil, changed
	}
	if cur.Type == CommandAlarm && !s.DemoAlarmStillSafe(*cur, box) {
		s.Cancel(DemoGuardBy, DemoGuardReason, now)
		return nil, true
	}
	return s.Deliver(to, now), true
}

// setCurrent setzt den laufenden Befehl (eine eigene Kopie im Entwurf).
func (s *State) setCurrent(c Command) { s.Commands.Current = &c }

// rememberType merkt den Typ des Befehls id (PHP remember_command_type):
// eine vorhandene Nummer bekommt ihren neuen Typ an ihrer Stelle, eine neue
// kommt nach Nummer geordnet dazu; es bleiben die MaxRecentTypes mit den
// höchsten Nummern (Nummern steigen – das sind die zuletzt vergebenen, wie
// PHPs array_slice(…, -20)).
func (s *State) rememberType(id int, typ string) {
	rt := s.Commands.RecentTypes
	i, found := slices.BinarySearchFunc(rt, id, func(r RecentType, id int) int { return cmp.Compare(r.ID, id) })
	if found {
		rt[i].Type = typ
	} else {
		rt = slices.Insert(rt, i, RecentType{ID: id, Type: typ})
	}
	if over := len(rt) - MaxRecentTypes; over > 0 {
		rt = slices.Delete(rt, 0, over)
	}
	s.Commands.RecentTypes = rt
}

// typeOf: der gemerkte Typ des Befehls id (PHP: recent_types[id]).
func (s *State) typeOf(id int) (string, bool) {
	rt := s.Commands.RecentTypes
	i, found := slices.BinarySearchFunc(rt, id, func(r RecentType, id int) int { return cmp.Compare(r.ID, id) })
	if !found {
		return "", false
	}
	return rt[i].Type, true
}
