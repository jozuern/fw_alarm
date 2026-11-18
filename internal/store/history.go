package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"slices"
	"sync"

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
// nicht angehängt ist (er steht dann in queue) oder schon angehängt. Jedes
// gelungene Sichern eines Dokuments gleicht ab: Was darin neu ist, kommt in
// die Warteschlange; was nicht mehr darin steht, wird vergessen – angehängt
// (die outbox hat es abgegeben) wie nicht angehängt (es fiel aus der vollen
// outbox, das stand dort schon im Journal). Die Warteschlange hält so nie
// mehr, als die beiden outboxen fassen.
//
// hist.mu wird nur in den kleinen Methoden unten genommen, jeweils mit
// defer: Ein Laufzeitfehler darin gibt die Sperre frei, und nichts, was
// danach aufräumt (onPanic), wartet auf eine Sperre, die der Fehler hielt.

// historyRotateAt: Ab dieser Größe wandert history.jsonl vor dem nächsten
// Anhängen nach history.1.jsonl.
const historyRotateAt = 5 << 20

// historyMemLimit: so viele Bytes an Verlaufszeilen hält der Speicher
// höchstens (normal sind es mit der Rotation bis etwa 10 MB). Klemmt die
// Rotation und wächst history.jsonl weiter, fallen darüber die ältesten
// Zeilen aus der Ansicht im Speicher – auf der Platte bleiben sie. Variable
// nur, damit ein Test sie klein stellen kann.
var historyMemLimit int64 = 12 << 20

// docKind: aus welcher outbox ein Eintrag stammt.
type docKind uint8

const (
	kindState docKind = iota + 1
	kindRuntime
)

// queued ist ein übergebener, noch nicht angehängter Eintrag.
type queued struct {
	e     model.Entry // aus einem veröffentlichten Schnappschuss – nur lesen
	uid   string
	kind  docKind
	start bool // beim Start aus einer gesicherten outbox übernommen
}

// uidMark: was der Verlauf über eine uid weiß.
type uidMark struct {
	kind docKind
	done bool // angehängt (oder verworfen) – fällt beim nächsten Sichern aus der outbox
}

// history ist der Verlauf im Speicher; mu schützt alles darin und wird nie
// während eines Zugriffs auf die Platte gehalten.
type history struct {
	mu sync.Mutex
	// Die Zeilen von history.1.jsonl und history.jsonl (ohne Zeilenende),
	// einmal beim Start gelesen und vom Schreiber fortgeschrieben. Eine
	// Zeile wird nie verändert; an cur wird nur angehängt (Leser halten sich
	// eine Kopie des Slice-Kopfs und lesen nie über dessen Länge hinaus).
	// Rotieren macht cur zu old, Kürzen und Neulesen legen neue Slices an.
	old, cur           [][]byte
	oldBytes, curBytes int64 // Bytes der Zeilen samt Zeilenende
	curSize            int64 // Bytes in history.jsonl, soweit bekannt
	needNL             bool  // history.jsonl endet nicht mit einem Zeilenende
	resync             bool  // Inhalt von history.jsonl unbekannt: vor dem nächsten Anhängen neu lesen
	rotateAt           int64
	memLimit           int64
	trimLogged         bool // das Kürzen steht schon im Journal (bis zur nächsten Rotation)

	queue   []queued
	known   map[string]uidMark
	waiters map[string][]chan struct{} // LogAndWait: geschlossen, sobald angehängt
}

// ---- Laden -------------------------------------------------------------

// openHistory liest history.jsonl und history.1.jsonl einmal (Spec §6.2:
// danach liest niemand mehr eine Datei). Fehlt eine, ist sie leer; ist eine
// nicht lesbar (etwa ein Ordner an ihrer Stelle), ist sie im Speicher leer
// und SaveHealth().History false – history.jsonl wird dann vor dem
// nächsten Anhängen neu gelesen.
func (s *Store) openHistory() {
	cur, errCur := s.readHistory(fileHistory)
	old, errOld := s.readHistory(fileHistory1)
	if errCur != nil {
		s.log.Error("history.jsonl nicht lesbar – Verlauf im Speicher leer", "err", errCur)
	}
	if errOld != nil {
		s.log.Error("history.1.jsonl nicht lesbar – ältere Einträge fehlen im Speicher", "err", errOld)
	}
	if s.hist.load(cur, old, errCur != nil) {
		s.logTrimmed()
	}
	s.saved.history.Store(errCur == nil && errOld == nil)
}

// load setzt den Verlauf im Speicher beim Start; true = gekürzt.
func (h *history) load(cur, old []byte, resync bool) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rotateAt, h.memLimit = historyRotateAt, historyMemLimit
	h.known = map[string]uidMark{}
	h.waiters = map[string][]chan struct{}{}
	h.cur, h.curBytes = splitLines(cur)
	h.old, h.oldBytes = splitLines(old)
	h.curSize, h.needNL, h.resync = int64(len(cur)), openEnd(cur), resync
	return h.trimLocked()
}

// readHistory liest eine Verlaufsdatei; fehlt sie, ist sie leer.
func (s *Store) readHistory(name string) ([]byte, error) {
	b, err := s.fsys.ReadFile(s.path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

// splitLines zerlegt eine Verlaufsdatei in Zeilen ohne Zeilenende (leere
// fallen weg) und zählt ihre Bytes samt Zeilenende. Jede Zeile hat ihre
// Länge als Kapazität – niemand hängt je an sie an.
func splitLines(data []byte) ([][]byte, int64) {
	var out [][]byte
	var n int64
	for len(data) > 0 {
		line := data
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			line, data = data[:i], data[i+1:]
		} else {
			data = nil
		}
		if len(line) > 0 {
			out = append(out, line[:len(line):len(line)])
			n += int64(len(line)) + 1
		}
	}
	return out, n
}

// openEnd: Endet die Datei mitten in einer Zeile?
func openEnd(data []byte) bool { return len(data) > 0 && data[len(data)-1] != '\n' }

// trimLocked hält die Ansicht im Speicher unter memLimit: Darüber fallen die
// ältesten Zeilen weg (erst aus old, dann aus cur), bis sie wieder
// höchstens neun Zehntel davon hält – so wird nicht bei jedem Anhängen
// gekürzt. Die übrigen Zeilen werden umkopiert, damit der Speicher der
// weggefallenen (oft eine ganze eingelesene Datei) frei wird. true = zum
// ersten Mal seit der letzten Rotation gekürzt (eine Zeile ins Journal).
func (h *history) trimLocked() bool {
	if h.memLimit <= 0 || h.oldBytes+h.curBytes <= h.memLimit {
		return false
	}
	target := h.memLimit / 10 * 9
	drop := func(lines [][]byte, size *int64) [][]byte {
		k := 0
		for k < len(lines) && h.oldBytes+h.curBytes > target {
			*size -= int64(len(lines[k])) + 1
			k++
		}
		if k == 0 {
			return lines
		}
		return compact(lines[k:])
	}
	h.old = drop(h.old, &h.oldBytes)
	h.cur = drop(h.cur, &h.curBytes)
	first := !h.trimLogged
	h.trimLogged = true
	return first
}

// compact kopiert Zeilen in einen eigenen, zusammenhängenden Speicher.
func compact(lines [][]byte) [][]byte {
	n := 0
	for _, l := range lines {
		n += len(l)
	}
	buf := make([]byte, 0, n)
	out := make([][]byte, len(lines))
	for i, l := range lines {
		start := len(buf)
		buf = append(buf, l...)
		out[i] = buf[start:len(buf):len(buf)]
	}
	return out
}

func (s *Store) logTrimmed() {
	s.log.Warn("Verlauf im Speicher gekürzt – ältere Zeilen stehen nur noch in den Dateien (klemmt die Rotation?)")
}

// ---- Zeilen lesen ----------------------------------------------------------

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

// uidsIn: welche der gesuchten uids unter den Zeilen stehen (als Feld "uid"
// einer lesbaren Zeile). Eine Zeile ohne uid ist nie ein Treffer – alte
// Zeilen der PHP-Fassung haben keine.
func uidsIn(lines [][]byte, want map[string]bool) map[string]bool {
	found := map[string]bool{}
	for _, line := range lines {
		if e, ok := parseLine(line); ok {
			if uid := e.Str("uid"); uid != "" && want[uid] {
				found[uid] = true
			}
		}
	}
	return found
}

// ---- Einträge ohne uid -------------------------------------------------

// hasUID: Trägt der Eintrag eine uid (einen nicht leeren Text)?
func hasUID(e model.Entry) bool {
	uid, ok := e["uid"].(string)
	return ok && uid != ""
}

// ensureUIDs gibt jedem Eintrag der outbox ohne uid eine neue – als Kopie
// an seiner Stelle, mit einer Zeile im Journal. Ein Eintrag ohne uid ist
// ein Programmierfehler (NewEntry setzt sie), soll aber nicht verloren
// gehen; Validate nähme ihn so nicht an. Liefert, wie viele.
func (s *Store) ensureUIDs(file string, box []model.Entry) int {
	n := 0
	for i, e := range box {
		if hasUID(e) {
			continue
		}
		c := maps.Clone(e)
		if c == nil {
			c = model.Entry{}
		}
		c["uid"] = model.NewUID()
		box[i] = c
		n++
		s.log.Error("Verlaufs-Eintrag ohne uid – bekommt eine neue", "datei", file, "event", e.Str("event"))
	}
	return n
}

// ---- Übergabe und Start ------------------------------------------------

// startCheck übernimmt beim Start, was in einer gesicherten outbox steht
// und unter den Zeilen im Speicher fehlt, in die Warteschlange – angehängt
// wird es erst vom Schreiber (Open wartet nie auf die Platte des Verlaufs).
// Was schon dasteht, ist angehängt und fällt beim nächsten Sichern aus der
// outbox. Gesichert ist die outbox der flüchtigen Daten (von der Platte
// gelesen) und die des Zustands, wenn er so auf der Platte liegt (ein
// gescheitertes Sichern beim Start: erst mit dem nächsten gelungenen).
// Läuft vor den Schreibern.
func (s *Store) startCheck() {
	s.fixRuntimeUIDs() // runtime.json kann Einträge ohne uid tragen
	var cands []queued
	if s.saved.durable.Load() {
		for _, e := range s.state.Load().Outbox {
			cands = append(cands, queued{e: e, uid: e.Str("uid"), kind: kindState, start: true})
		}
	}
	for _, e := range s.runtime.Load().Outbox {
		cands = append(cands, queued{e: e, uid: e.Str("uid"), kind: kindRuntime, start: true})
	}
	if len(cands) == 0 {
		return
	}
	want := make(map[string]bool, len(cands))
	for _, c := range cands {
		want[c.uid] = true
	}
	old, cur := s.hist.lines()
	found := uidsIn(old, want)
	maps.Copy(found, uidsIn(cur, want))
	missing := s.hist.adopt(cands, found)
	s.log.Info("outbox beim Start", "eintraege", len(cands), "fehlten_im_verlauf", missing)
	// Was schon dastand, fällt aus der outbox der flüchtigen Daten – im
	// Speicher; gesichert wird mit dem nächsten Durchgang.
	s.pruneRuntime(false)
}

// lines: die Zeilen im Speicher (Slice-Köpfe; die Zeilen selbst ändern
// sich nie).
func (h *history) lines() (old, cur [][]byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.old, h.cur
}

// adopt übernimmt die Kandidaten des Starts: gefunden = angehängt, sonst in
// die Warteschlange. Liefert, wie viele fehlten.
func (h *history) adopt(cands []queued, found map[string]bool) int {
	h.mu.Lock()
	defer h.mu.Unlock()
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
	return missing
}

// pending: Wartet etwas auf das Anhängen?
func (h *history) pending() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.queue) > 0
}

// handover: Ein Dokument dieser Art ist mit dieser outbox gesichert. Was
// darin noch unbekannt ist, geht in die Warteschlange; was nicht mehr darin
// steht, wird vergessen – angehängt (die outbox hat es abgegeben) oder
// nicht (es fiel aus der vollen outbox, das steht dort schon im Journal;
// dann fällt es auch aus der Warteschlange und kann keinen Eintrag des
// anderen Dokuments verdrängen).
func (s *Store) handover(kind docKind, outbox []model.Entry) {
	if s.hist.handover(kind, outbox) > 0 {
		s.histW.trigger()
	}
}

func (h *history) handover(kind docKind, outbox []model.Entry) (added int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	present := make(map[string]bool, len(outbox))
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
		if m.kind == kind && !present[uid] {
			delete(h.known, uid)
		}
	}
	h.queue = slices.DeleteFunc(h.queue, func(q queued) bool { return q.kind == kind && !present[q.uid] })
	return added
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

// ---- Der Schreiber -----------------------------------------------------

// historyRun ist ein Durchgang des Verlauf-Schreibers: alles Übergebene in
// einem Schub anhängen – öffnen, anhängen, sichern, schließen (FS.AppendSync).
// Ist der Inhalt der Datei unbekannt (nicht lesbar beim Start, ein
// gescheitertes Anhängen), wird sie vorher neu gelesen: Was dort schon
// steht, wird nicht noch einmal angehängt. Über der Grenze wandert sie
// zuerst nach history.1.jsonl; scheitert das, wird trotzdem angehängt. Endet
// sie nicht mit einem Zeilenende, kommt zuerst eines. Danach fallen die
// angehängten Einträge aus ihrer outbox (afterAppend).
func (s *Store) historyRun() (bool, error) {
	batch, resync := s.hist.batch()
	if len(batch) == 0 && !resync {
		return false, nil
	}
	did := false
	var done []queued
	if resync {
		rest, err := s.historyResync(batch)
		if err != nil {
			s.saved.history.Store(false)
			return false, fmt.Errorf("%s lesen: %w", fileHistory, err)
		}
		did = true
		done = without(batch, rest)
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
	s.afterAppend(done)
	return did, err
}

// batch: alles Übergebene und ob die Datei neu zu lesen ist.
func (h *history) batch() ([]queued, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.queue), h.resync
}

// without: die Einträge von all, die nicht in rest stehen.
func without(all, rest []queued) []queued {
	keep := make(map[string]bool, len(rest))
	for _, q := range rest {
		keep[q.uid] = true
	}
	var out []queued
	for _, q := range all {
		if !keep[q.uid] {
			out = append(out, q)
		}
	}
	return out
}

// historyResync liest history.jsonl neu und nimmt sie als Wahrheit: Im
// selben Schritt (unter derselben Sperre) gelten die Einträge des Schubs,
// die schon darin stehen, als angehängt – ein Leser sieht nie die neuen
// Zeilen und dieselben Einträge noch in der Warteschlange. Liefert die
// übrigen.
func (s *Store) historyResync(batch []queued) ([]queued, error) {
	data, err := s.readHistory(fileHistory)
	if err != nil {
		return nil, err
	}
	lines, size := splitLines(data)
	want := make(map[string]bool, len(batch))
	for _, q := range batch {
		want[q.uid] = true
	}
	found := uidsIn(lines, want)
	var done, rest []queued
	for _, q := range batch {
		if found[q.uid] {
			done = append(done, q)
		} else {
			rest = append(rest, q)
		}
	}
	if s.hist.install(lines, size, int64(len(data)), openEnd(data), done) {
		s.logTrimmed()
	}
	return rest, nil
}

// install: die neu gelesene history.jsonl gilt, und was davon schon im
// Schub stand, ist angehängt – in einem Schritt. true = gekürzt.
func (h *history) install(lines [][]byte, lineBytes, size int64, nl bool, done []queued) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cur, h.curBytes, h.curSize, h.needNL, h.resync = lines, lineBytes, size, nl, false
	h.markDoneLocked(done)
	return h.trimLocked()
}

// historyAppend hängt den Schub an (vorher ggf. rotieren). Liefert, was
// danach als angehängt gilt (dazu Einträge, die sich nicht schreiben
// lassen – verworfen, mit einer Zeile im Journal), und den Fehler.
func (s *Store) historyAppend(batch []queued) ([]queued, error) {
	var rotErr error
	if s.hist.rotateDue() {
		rotErr = s.rotateHistory()
	}
	var buf bytes.Buffer
	if s.hist.openEnded() {
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
		s.hist.failed(bad, false)
		return bad, rotErr
	}
	if err := s.fsys.AppendSync(s.path(fileHistory), buf.Bytes()); err != nil {
		s.hist.failed(bad, true) // was davon in der Datei steht, ist unbekannt
		s.log.Error("Verlauf nicht angehängt – wird nachgeholt", "eintraege", len(items), "err", err)
		return bad, errors.Join(rotErr, fmt.Errorf("%s anhängen: %w", fileHistory, err))
	}
	if s.hist.appended(lines, int64(buf.Len()), append(items, bad...)) {
		s.logTrimmed()
	}
	return append(items, bad...), rotErr
}

// rotateDue: Ist history.jsonl über der Grenze?
func (h *history) rotateDue() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.curSize > h.rotateAt
}

// openEnded: Endet history.jsonl mitten in einer Zeile?
func (h *history) openEnded() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.needNL
}

// failed: Das Anhängen scheiterte (resync: der Inhalt der Datei ist danach
// unbekannt); verworfene Einträge gelten trotzdem als erledigt.
func (h *history) failed(bad []queued, resync bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if resync {
		h.resync = true
	}
	h.markDoneLocked(bad)
}

// setResync: vor dem nächsten Anhängen history.jsonl neu lesen.
func (h *history) setResync() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.resync = true
}

// appended: Die Zeilen stehen in history.jsonl. true = gekürzt.
func (h *history) appended(lines [][]byte, written int64, items []queued) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cur = append(h.cur, lines...)
	for _, l := range lines {
		h.curBytes += int64(len(l)) + 1
	}
	h.curSize += written
	h.needNL = false
	h.markDoneLocked(items)
	return h.trimLocked()
}

// rotateHistory: history.jsonl → history.1.jsonl (die alte fällt weg, im
// Speicher wie auf der Platte).
func (s *Store) rotateHistory() error {
	if err := s.fsys.Rename(s.path(fileHistory), s.path(fileHistory1)); err != nil {
		s.log.Error("history.jsonl nicht nach history.1.jsonl verschoben – es wird weiter angehängt", "err", err)
		return fmt.Errorf("%s umbenennen: %w", fileHistory, err)
	}
	s.hist.rotated()
	if err := s.fsys.SyncDir(s.dir); err != nil {
		return fmt.Errorf("Datenordner sichern: %w", err)
	}
	return nil
}

func (h *history) rotated() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.old, h.oldBytes = h.cur, h.curBytes
	h.cur, h.curBytes, h.curSize, h.needNL = nil, 0, 0, false
	h.trimLogged = false
}

// afterAppend: Angehängte Einträge fallen aus ihrer outbox – die der
// flüchtigen Daten sofort im Speicher, die des Zustands mit dem
// Nachsichern. Für Einträge, die der Start übernahm, stößt es kein Sichern
// an (gleich nach dem Start schreibt nur, was der Start braucht); sie fallen
// mit dem nächsten Sichern heraus.
func (s *Store) afterAppend(done []queued) {
	var rt, rtKick, stKick bool
	for _, q := range done {
		switch q.kind {
		case kindRuntime:
			rt = true
			rtKick = rtKick || !q.start
		case kindState:
			stKick = stKick || !q.start
		}
	}
	if rt {
		s.pruneRuntime(rtKick)
	}
	if stKick {
		select {
		case s.pruneKick <- struct{}{}:
		default: // es wartet schon eines
		}
	}
}
