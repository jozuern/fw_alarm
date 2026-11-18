package store

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"sync"
	"time"

	"fwalarm/internal/model"
)

// Der Verlauf reist mit der Änderung, zu der er gehört (Spec §6.2): Ein
// Eintrag steht zuerst in der outbox seines Dokuments (state.json bzw.
// runtime.json). Erst wenn dieses Dokument gesichert ist, bekommt der
// Verlauf ihn übergeben (handover) und hängt ihn an history.jsonl an; danach
// fällt er beim nächsten Sichern seines Dokuments aus der outbox. Nach einem
// Neustart wird angehängt, was in einer gesicherten outbox steht und unter
// den Zeilen im Speicher fehlt. So gibt es weder "gesichert, aber nicht im
// Verlauf" noch eine doppelte Zeile.
//
// Je uid merkt sich der Verlauf (known), ob der Eintrag übergeben und noch
// nicht angehängt ist (er steht dann in queue) oder schon angehängt. Ein
// angehängter Eintrag wird vergessen, sobald ein Sichern seines Dokuments
// ohne ihn gelungen ist – vorher kann er mit jedem weiteren Sichern noch
// einmal übergeben werden und wird dann übergangen.

// historyRotateAt: Ab dieser Größe wandert history.jsonl vor dem nächsten
// Anhängen nach history.1.jsonl.
const historyRotateAt = 5 << 20

// maxQueue: so viele übergebene, noch nicht angehängte Einträge hält der
// Verlauf – je Dokument eine volle outbox. Darüber fallen die ältesten weg
// (mit einer Zeile im Journal), wie in der outbox selbst.
const maxQueue = 2 * model.MaxOutbox

// docKind: aus welcher outbox ein Eintrag stammt.
type docKind uint8

const (
	kindState docKind = iota + 1
	kindRuntime
)

// queued ist ein übergebener, noch nicht angehängter Eintrag.
type queued struct {
	e    model.Entry // aus einem veröffentlichten Schnappschuss – nur lesen
	uid  string
	kind docKind
}

// uidMark: was der Verlauf über eine uid weiß.
type uidMark struct {
	kind docKind
	done bool // angehängt (oder verworfen) – fällt beim nächsten Sichern aus der outbox
}

// history ist der Verlauf im Speicher. mu schützt alles darin und wird nie
// während eines Zugriffs auf die Platte gehalten.
type history struct {
	mu sync.Mutex
	// Die Zeilen von history.1.jsonl und history.jsonl (ohne Zeilenende),
	// einmal beim Start gelesen und vom Schreiber fortgeschrieben. Eine
	// Zeile wird nie verändert; an cur wird nur angehängt (Leser halten sich
	// eine Kopie des Slice-Kopfs und lesen nie über dessen Länge hinaus),
	// beim Rotieren wird cur zu old und cur neu angelegt.
	old, cur [][]byte
	curSize  int64 // Bytes in history.jsonl, soweit bekannt
	needNL   bool  // history.jsonl endet nicht mit einem Zeilenende
	resync   bool  // Inhalt von history.jsonl unbekannt: vor dem nächsten Anhängen neu lesen
	rotateAt int64

	queue   []queued
	known   map[string]uidMark
	waiters map[string][]chan struct{} // LogAndWait: geschlossen, sobald angehängt
}

// openHistory liest history.jsonl und history.1.jsonl einmal (Spec §6.2:
// danach liest niemand mehr eine Datei). Fehlt eine, ist sie leer; ist eine
// nicht lesbar (etwa ein Ordner an ihrer Stelle), ist sie im Speicher leer
// und SaveHealth().History false – history.jsonl wird dann vor dem
// nächsten Anhängen neu gelesen.
func (s *Store) openHistory() {
	h := &s.hist
	h.rotateAt = historyRotateAt
	h.known = map[string]uidMark{}
	h.waiters = map[string][]chan struct{}{}
	cur, errCur := s.readHistory(fileHistory)
	old, errOld := s.readHistory(fileHistory1)
	h.cur, h.curSize, h.needNL = splitLines(cur), int64(len(cur)), openEnd(cur)
	h.old = splitLines(old)
	if errCur != nil {
		h.resync = true
		s.log.Error("history.jsonl nicht lesbar – Verlauf im Speicher leer", "err", errCur)
	}
	if errOld != nil {
		s.log.Error("history.1.jsonl nicht lesbar – ältere Einträge fehlen im Speicher", "err", errOld)
	}
	s.saved.history.Store(errCur == nil && errOld == nil)
}

// readHistory liest eine Verlaufsdatei; fehlt sie, ist sie leer.
func (s *Store) readHistory(name string) ([]byte, error) {
	b, err := s.fsys.ReadFile(s.path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

// splitLines zerlegt eine Verlaufsdatei in Zeilen ohne Zeilenende; leere
// fallen weg. Jede Zeile hat ihre Länge als Kapazität – niemand hängt je an
// sie an.
func splitLines(data []byte) [][]byte {
	var out [][]byte
	for len(data) > 0 {
		line := data
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			line, data = data[:i], data[i+1:]
		} else {
			data = nil
		}
		if len(line) > 0 {
			out = append(out, line[:len(line):len(line)])
		}
	}
	return out
}

// openEnd: Endet die Datei mitten in einer Zeile?
func openEnd(data []byte) bool { return len(data) > 0 && data[len(data)-1] != '\n' }

// parseLine liest eine Verlaufszeile nachsichtig: genau ein JSON-Objekt
// (Zahlen als json.Number, wie NewEntry), sonst übersprungen – nie ein
// Fehler.
func parseLine(b []byte) (model.Entry, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false // Daten nach dem Objekt
	}
	return model.Entry(m), true
}

// copyEntry: eine eigene Kopie eines Eintrags (über seine JSON-Form – genau
// das, was auch auf der Platte stünde); false, wenn er sich nicht schreiben
// lässt.
func copyEntry(e model.Entry) (model.Entry, bool) {
	b, err := json.Marshal(e)
	if err != nil {
		return nil, false
	}
	return parseLine(b)
}

// startCheck hängt beim Start an, was in einer gesicherten outbox steht und
// unter den Zeilen im Speicher fehlt; was schon dasteht, ist angehängt und
// fällt beim nächsten Sichern aus der outbox. Gesichert ist die outbox der
// flüchtigen Daten (von der Platte gelesen) und die des Zustands, wenn er so
// auf der Platte liegt (ein gescheitertes Sichern beim Start: erst mit dem
// nächsten gelungenen). Läuft vor den Schreibern und stößt keinen an.
func (s *Store) startCheck() {
	var cands []queued
	if s.saved.durable.Load() {
		for _, e := range s.state.Load().Outbox {
			cands = append(cands, queued{e: e, uid: e.Str("uid"), kind: kindState})
		}
	}
	for _, e := range s.runtime.Load().Outbox {
		cands = append(cands, queued{e: e, uid: e.Str("uid"), kind: kindRuntime})
	}
	if len(cands) == 0 {
		return
	}
	h := &s.hist
	want := make(map[string]bool, len(cands))
	for _, c := range cands {
		want[c.uid] = true
	}
	found := uidsIn(h.old, want)
	for uid := range uidsIn(h.cur, want) {
		found[uid] = true
	}
	h.mu.Lock()
	missing := 0
	for _, c := range cands {
		if _, dup := h.known[c.uid]; dup {
			continue
		}
		if found[c.uid] {
			h.known[c.uid] = uidMark{kind: c.kind, done: true}
			continue
		}
		h.known[c.uid] = uidMark{kind: c.kind}
		h.queue = append(h.queue, c)
		missing++
	}
	h.mu.Unlock()
	s.log.Info("outbox beim Start", "eintraege", len(cands), "fehlten_im_verlauf", missing)
	if _, err := s.historyRun(false); err != nil {
		s.log.Error("Verlauf beim Start nicht ergänzt – wird nachgeholt", "err", err)
	}
	// Auch was schon dastand (gefunden, nicht angehängt), fällt aus der
	// outbox der flüchtigen Daten – historyRun nimmt nur, was es anhängt.
	s.pruneRuntime(false)
}

// uidsIn: welche der gesuchten uids unter den Zeilen stehen (als Feld "uid"
// einer lesbaren Zeile).
func uidsIn(lines [][]byte, want map[string]bool) map[string]bool {
	found := map[string]bool{}
	for _, line := range lines {
		if e, ok := parseLine(line); ok {
			if uid := e.Str("uid"); want[uid] {
				found[uid] = true
			}
		}
	}
	return found
}

// handover: Ein Dokument dieser Art ist mit dieser outbox gesichert. Was
// darin noch unbekannt ist, geht in die Warteschlange; was angehängt ist und
// nicht mehr darin steht, ist mit diesem Sichern aus der outbox gefallen
// und wird vergessen.
func (s *Store) handover(kind docKind, outbox []model.Entry) {
	h := &s.hist
	h.mu.Lock()
	present := make(map[string]bool, len(outbox))
	added := 0
	for _, e := range outbox {
		uid := e.Str("uid")
		present[uid] = true
		if _, ok := h.known[uid]; ok {
			continue
		}
		h.known[uid] = uidMark{kind: kind}
		h.queue = append(h.queue, queued{e: e, uid: uid, kind: kind})
		added++
	}
	for uid, m := range h.known {
		if m.kind == kind && m.done && !present[uid] {
			delete(h.known, uid)
		}
	}
	dropped := 0
	if over := len(h.queue) - maxQueue; over > 0 {
		for _, q := range h.queue[:over] {
			h.known[q.uid] = uidMark{kind: q.kind, done: true} // verworfen: fällt aus der outbox
		}
		h.queue = slices.Delete(h.queue, 0, over)
		dropped = over
	}
	h.mu.Unlock()
	if dropped > 0 {
		s.log.Error("Verlauf nicht schreibbar und Warteschlange voll – die ältesten Einträge fallen weg", "anzahl", dropped)
	}
	if added > 0 {
		s.histW.trigger()
	}
}

// appendedUIDs: die uids dieser Art, die angehängt (oder verworfen) sind.
func (s *Store) appendedUIDs(kind docKind) map[string]bool {
	h := &s.hist
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]bool{}
	for uid, m := range h.known {
		if m.kind == kind && m.done {
			out[uid] = true
		}
	}
	return out
}

// markDoneLocked: Diese Einträge stehen in history.jsonl (oder sind
// verworfen) – aus der Warteschlange nehmen, Wartende wecken. h.mu gehalten.
func (h *history) markDoneLocked(items []queued) {
	if len(items) == 0 {
		return
	}
	gone := make(map[string]bool, len(items))
	for _, q := range items {
		gone[q.uid] = true
		h.known[q.uid] = uidMark{kind: q.kind, done: true}
		for _, ch := range h.waiters[q.uid] {
			close(ch)
		}
		delete(h.waiters, q.uid)
	}
	h.queue = slices.DeleteFunc(h.queue, func(q queued) bool { return gone[q.uid] })
}

// historyRun ist ein Durchgang des Verlauf-Schreibers: alles Übergebene in
// einem Schub anhängen – öffnen, anhängen, sichern, schließen (FS.AppendSync).
// Ist der Inhalt der Datei unbekannt (nicht lesbar beim Start, ein
// gescheitertes Anhängen), wird sie vorher neu gelesen: Was dort schon
// steht, wird nicht noch einmal angehängt. Über der Grenze wandert sie
// zuerst nach history.1.jsonl; scheitert das, wird trotzdem angehängt. Endet
// sie nicht mit einem Zeilenende, kommt zuerst eines. kick: danach die
// outbox nachsichern lassen (beim Start nicht).
func (s *Store) historyRun(kick bool) (bool, error) {
	h := &s.hist
	h.mu.Lock()
	batch := slices.Clone(h.queue)
	resync := h.resync
	h.mu.Unlock()
	if len(batch) == 0 && !resync {
		return false, nil
	}
	did := false
	var done []queued
	if resync {
		found, err := s.historyResync(batch)
		if err != nil {
			s.saved.history.Store(false)
			return false, fmt.Errorf("%s lesen: %w", fileHistory, err)
		}
		did = true
		var rest []queued
		for _, q := range batch {
			if found[q.uid] {
				done = append(done, q)
			} else {
				rest = append(rest, q)
			}
		}
		h.mu.Lock()
		h.markDoneLocked(done)
		h.mu.Unlock()
		batch = rest
	}
	var err error
	if len(batch) > 0 {
		var written []queued
		written, err = s.historyAppend(batch)
		done = append(done, written...)
		did = did || len(written) > 0
	}
	s.saved.history.Store(err == nil)
	s.afterAppend(done, kick)
	return did, err
}

// historyResync liest history.jsonl neu und nimmt sie als Wahrheit; liefert,
// welche uids des Schubs schon darin stehen.
func (s *Store) historyResync(batch []queued) (map[string]bool, error) {
	data, err := s.readHistory(fileHistory)
	if err != nil {
		return nil, err
	}
	lines := splitLines(data)
	want := make(map[string]bool, len(batch))
	for _, q := range batch {
		want[q.uid] = true
	}
	found := uidsIn(lines, want)
	h := &s.hist
	h.mu.Lock()
	h.cur, h.curSize, h.needNL, h.resync = lines, int64(len(data)), openEnd(data), false
	h.mu.Unlock()
	return found, nil
}

// historyAppend hängt den Schub an (vorher ggf. rotieren). Liefert, was
// danach als angehängt gilt (dazu Einträge, die sich nicht schreiben
// lassen – verworfen, mit einer Zeile im Journal), und den Fehler.
func (s *Store) historyAppend(batch []queued) ([]queued, error) {
	h := &s.hist
	h.mu.Lock()
	rotate := h.curSize > h.rotateAt
	h.mu.Unlock()
	var rotErr error
	if rotate {
		rotErr = s.rotateHistory()
	}

	h.mu.Lock()
	nl := h.needNL
	h.mu.Unlock()
	var buf bytes.Buffer
	if nl {
		buf.WriteByte('\n')
	}
	lines := make([][]byte, 0, len(batch))
	items := make([]queued, 0, len(batch))
	var bad []queued
	for _, q := range batch {
		b, err := json.Marshal(q.e)
		if err != nil {
			s.log.Error("Verlaufseintrag nicht schreibbar – verworfen", "event", q.e.Str("event"), "err", err)
			bad = append(bad, q)
			continue
		}
		lines = append(lines, b)
		items = append(items, q)
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if len(items) == 0 {
		h.mu.Lock()
		h.markDoneLocked(bad)
		h.mu.Unlock()
		return bad, rotErr
	}
	if err := s.fsys.AppendSync(s.path(fileHistory), buf.Bytes()); err != nil {
		h.mu.Lock()
		h.resync = true // was davon in der Datei steht, ist unbekannt
		h.markDoneLocked(bad)
		h.mu.Unlock()
		s.log.Error("Verlauf nicht angehängt – wird nachgeholt", "eintraege", len(items), "err", err)
		return bad, errors.Join(rotErr, fmt.Errorf("%s anhängen: %w", fileHistory, err))
	}
	h.mu.Lock()
	h.cur = append(h.cur, lines...)
	h.curSize += int64(buf.Len())
	h.needNL = false
	h.markDoneLocked(items)
	h.markDoneLocked(bad)
	h.mu.Unlock()
	return append(items, bad...), rotErr
}

// rotateHistory: history.jsonl → history.1.jsonl (die alte fällt weg, im
// Speicher wie auf der Platte).
func (s *Store) rotateHistory() error {
	if err := s.fsys.Rename(s.path(fileHistory), s.path(fileHistory1)); err != nil {
		s.log.Error("history.jsonl nicht nach history.1.jsonl verschoben – es wird weiter angehängt", "err", err)
		return fmt.Errorf("%s umbenennen: %w", fileHistory, err)
	}
	h := &s.hist
	h.mu.Lock()
	h.old, h.cur, h.curSize, h.needNL = h.cur, nil, 0, false
	h.mu.Unlock()
	if err := s.fsys.SyncDir(s.dir); err != nil {
		return fmt.Errorf("Datenordner sichern: %w", err)
	}
	return nil
}

// afterAppend: Angehängte Einträge fallen aus ihrer outbox – die der
// flüchtigen Daten sofort im Speicher, die des Zustands mit dem Nachsichern.
func (s *Store) afterAppend(done []queued, kick bool) {
	var rt, st bool
	for _, q := range done {
		rt = rt || q.kind == kindRuntime
		st = st || q.kind == kindState
	}
	if rt {
		s.pruneRuntime(kick)
	}
	if st && kick {
		select {
		case s.pruneKick <- struct{}{}:
		default: // es wartet schon eines
		}
	}
}

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
	h := &s.hist
	h.mu.Lock()
	st, rt := s.state.Load(), s.runtime.Load()
	pend := make([]model.Entry, 0, len(h.queue))
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
	old, cur := h.old, h.cur
	h.mu.Unlock()

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

// LogAndWait legt den Eintrag wie Log in die outbox der flüchtigen Daten
// und wartet, bis er in history.jsonl steht (true) oder maxWait vergangen
// ist (false – er kommt dann später an). Für "Alarm gestartet": darauf wird
// höchstens 300 ms gewartet, eine tote Platte hält den Alarm nicht auf.
// Ohne uid lässt sich nicht warten: false.
func (s *Store) LogAndWait(e model.Entry, maxWait time.Duration) bool {
	uid := e.Str("uid")
	if uid == "" {
		s.logEntry(e)
		return false
	}
	h := &s.hist
	ch := make(chan struct{})
	h.mu.Lock()
	h.waiters[uid] = append(h.waiters[uid], ch)
	h.mu.Unlock()
	if _, ok := s.logEntry(e); !ok {
		h.dropWaiter(uid, ch)
		return false
	}
	select {
	case <-ch:
		return true
	case <-s.clk.After(maxWait):
	}
	h.dropWaiter(uid, ch)
	select { // gerade noch angehängt?
	case <-ch:
		return true
	default:
		return false
	}
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
