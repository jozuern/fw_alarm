// Package storetest liefert eine Dateischicht für Tests der Ablage und der
// Pakete darüber: FaultFS hält alles im Speicher und baut gezielt Fehler,
// Hänger und verkürzte Schreibvorgänge ein.
package storetest

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"fwalarm/internal/store"
)

var _ store.FS = (*FaultFS)(nil)

// ErrInjected ist der Fehler eines Aufrufs, den FailCall scheitern lässt.
var ErrInjected = errors.New("storetest: eingebauter Fehler")

// FaultFS ist eine Dateischicht im Speicher – deterministisch und für
// gleichzeitige Aufrufe sicher. Wo die Regeln der Ablage davon abhängen,
// verhält sie sich wie das echte System:
//   - Link scheitert, wenn die Quelle fehlt oder das Ziel schon da ist;
//     beide Namen zeigen danach auf DIESELBE Datei (SameFile), und
//     WriteFileSync auf einen der Namen ändert beide (Kürzen, wie O_TRUNC).
//   - Rename ersetzt das Ziel in einem Schritt (eine Datei, nie einen Ordner).
//   - Remove einer fehlenden Datei ist kein Fehler; ein Ordner mit Inhalt
//     lässt sich nicht entfernen.
//   - Ein Ordner kann unter einem Dateinamen liegen; Schreiben dorthin und
//     Lesen von dort scheitern.
//   - In einem fehlenden Ordner lässt sich nichts anlegen (Schreiben,
//     Umbenennen, Verweisen, Sperren: "fehlt"), und ReadDir eines fehlenden
//     Ordners meldet "fehlt". Put und PutDir legen fehlende Ordner mit an.
//
// Fehler je Datei: Fail, Hang, ShortWrite, Mangle; je Datei und Methode:
// FailOp (vorher), FailAfter (nach dem Schreiben); für alles: FailCall,
// CrashAt, ReadOnly, NoLinks; AfterCrash liefert, was ein neuer Prozess
// danach auf der Platte fände. Heal nimmt alle Fehler zurück (Hänger nicht –
// die gibt ihre release-Funktion frei).
type FaultFS struct {
	mu    sync.Mutex
	nodes map[string]*node // bereinigter Pfad → Datei oder Ordner
	locks map[string]bool  // gehaltene Sperren

	calls      int
	log        []Call
	failAt     int
	crashAt    int
	fails      map[string]error
	opFails    map[opKey]error
	afterFails map[opKey]error
	hangs      map[string]chan struct{}
	short      map[string]int
	mangle     map[string]func([]byte) []byte
	readOnly   error
	noLinks    bool
}

// node ist ein Eintrag im Ordnerbaum. Dateien teilen sich über Link einen
// inode – wie Hardlinks.
type node struct {
	dir bool
	ino *inode
}

type inode struct {
	data    []byte
	modTime time.Time
}

// Call ist ein Aufruf der Dateischicht, wie ihn Log festhält.
type Call struct {
	N     int      // laufende Nummer, ab 1
	Op    string   // Name der Methode, z.B. "WriteFileSync"
	Names []string // bereinigte Pfade
	Err   error    // was der Aufruf lieferte (nil = gelungen)
}

// Writes sagt, ob der Aufruf etwas auf der Platte ändern würde.
func (c Call) Writes() bool {
	switch c.Op {
	case "WriteFileSync", "AppendSync", "Rename", "Link", "Remove", "MkdirAll", "Lock":
		return true
	}
	return false
}

// New liefert eine leere Dateischicht.
func New() *FaultFS {
	return &FaultFS{
		nodes:      map[string]*node{},
		locks:      map[string]bool{},
		fails:      map[string]error{},
		opFails:    map[opKey]error{},
		afterFails: map[opKey]error{},
		hangs:      map[string]chan struct{}{},
		short:      map[string]int{},
		mangle:     map[string]func([]byte) []byte{},
	}
}

func clean(name string) string { return filepath.Clean(name) }

// ---- Fehler einbauen ------------------------------------------------------

// Fail lässt jeden Aufruf scheitern, der name berührt (bei Rename und Link
// auch als Ziel); err nil nimmt das zurück.
func (f *FaultFS) Fail(name string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.fails, clean(name))
		return
	}
	f.fails[clean(name)] = err
}

// opKey: eine Methode der Dateischicht auf einem Namen.
type opKey struct{ name, op string }

// FailOp lässt nur die Aufrufe der Methode op (z.B. "Rename") scheitern,
// die name berühren (bei Rename und Link auch als Ziel); alle anderen
// Aufrufe auf name laufen normal. err nil nimmt das zurück.
func (f *FaultFS) FailOp(name, op string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := opKey{clean(name), op}
	if err == nil {
		delete(f.opFails, k)
		return
	}
	f.opFails[k] = err
}

// FailAfter: Aufrufe von WriteFileSync oder AppendSync (op) auf name wirken
// und melden danach err – wie ein fsync, der scheitert, nachdem die Daten
// schon in der Datei stehen. err nil nimmt das zurück.
func (f *FaultFS) FailAfter(name, op string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := opKey{clean(name), op}
	if err == nil {
		delete(f.afterFails, k)
		return
	}
	f.afterFails[k] = err
}

// Hang lässt jeden Aufruf, der name berührt, warten, bis release gerufen
// wird; danach läuft er normal weiter. release ist mehrfach rufbar.
func (f *FaultFS) Hang(name string) (release func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan struct{})
	f.hangs[clean(name)] = ch
	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			if f.hangs[clean(name)] == ch {
				delete(f.hangs, clean(name))
			}
			f.mu.Unlock()
			close(ch)
		})
	}
}

// ShortWrite: WriteFileSync und AppendSync auf name legen nur die ersten n
// Bytes ab und melden trotzdem Erfolg – eine Platte, die das Ende
// verliert; nur das Zurücklesen fällt darauf nicht herein. n < 0 nimmt das
// zurück.
func (f *FaultFS) ShortWrite(name string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n < 0 {
		delete(f.short, clean(name))
		return
	}
	f.short[clean(name)] = n
}

// Mangle: WriteFileSync und AppendSync auf name legen fn(Daten) ab und
// melden Erfolg – ein stiller Schreibfehler. fn nil nimmt das zurück.
func (f *FaultFS) Mangle(name string, fn func([]byte) []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if fn == nil {
		delete(f.mangle, clean(name))
		return
	}
	f.mangle[clean(name)] = fn
}

// FailCall lässt den n-ten Aufruf insgesamt (gezählt ab New, siehe Calls)
// mit ErrInjected scheitern; 0 nimmt das zurück.
func (f *FaultFS) FailCall(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failAt = n
}

// CrashAt: Ab dem n-ten Aufruf insgesamt scheitert jeder Aufruf mit
// ErrInjected – der Prozess ist "abgestürzt": Nichts erreicht mehr die
// Platte, auch kein Aufräumen danach. 0 nimmt das zurück.
func (f *FaultFS) CrashAt(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.crashAt = n
}

// AfterCrash liefert eine neue FaultFS mit denselben Dateien und Ordnern
// (Hardlinks bleiben untereinander verbunden) – ohne Fehler, Hänger,
// Sperren und Log: was ein neuer Prozess nach einem Absturz vorfände.
func (f *FaultFS) AfterCrash() *FaultFS {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := New()
	copies := map[*inode]*inode{}
	for name, nd := range f.nodes {
		if nd.dir {
			g.nodes[name] = &node{dir: true}
			continue
		}
		c, ok := copies[nd.ino]
		if !ok {
			c = &inode{data: bytes.Clone(nd.ino.data), modTime: nd.ino.modTime}
			copies[nd.ino] = c
		}
		g.nodes[name] = &node{ino: c}
	}
	return g
}

// ReadOnly: Jeder Aufruf, der etwas ändern würde (Writes, auch Lock),
// scheitert mit err – ein Ordner ohne Schreibrecht. nil nimmt das zurück.
func (f *FaultFS) ReadOnly(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readOnly = err
}

// NoLinks: Link scheitert immer – ein Dateisystem ohne Hardlinks.
func (f *FaultFS) NoLinks(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.noLinks = on
}

// Heal nimmt Fail, FailOp, FailAfter, ShortWrite, Mangle, FailCall,
// CrashAt, ReadOnly und NoLinks zurück.
func (f *FaultFS) Heal() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fails = map[string]error{}
	f.opFails = map[opKey]error{}
	f.afterFails = map[opKey]error{}
	f.short = map[string]int{}
	f.mangle = map[string]func([]byte) []byte{}
	f.failAt = 0
	f.crashAt = 0
	f.readOnly = nil
	f.noLinks = false
}

// Calls nennt die Zahl der Aufrufe bisher.
func (f *FaultFS) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// Log liefert eine Kopie aller Aufrufe bisher.
func (f *FaultFS) Log() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Call, len(f.log))
	for i, c := range f.log {
		c.Names = append([]string(nil), c.Names...)
		out[i] = c
	}
	return out
}

// ---- Dateien anlegen und ansehen (zählen nicht als Aufruf) ---------------

// Put legt eine Datei an oder ersetzt ihren Inhalt (eine verbundene
// Datei ändert sich mit); fehlende Ordner darüber entstehen mit – eine
// Datei liegt immer in einem Ordner.
func (f *FaultFS) Put(name string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name = clean(name)
	f.mkParents(name)
	if n, ok := f.nodes[name]; ok && !n.dir {
		n.ino.data = bytes.Clone(data)
		return
	}
	f.nodes[name] = &node{ino: &inode{data: bytes.Clone(data)}}
}

// PutDir legt einen (leeren) Ordner an, samt fehlender Ordner darüber.
func (f *FaultFS) PutDir(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mkParents(clean(name))
	f.nodes[clean(name)] = &node{dir: true}
}

// Get liefert den Inhalt einer Datei; false, wenn dort keine Datei liegt.
func (f *FaultFS) Get(name string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.nodes[clean(name)]
	if !ok || n.dir {
		return nil, false
	}
	return bytes.Clone(n.ino.data), true
}

// IsDir sagt, ob unter name ein Ordner liegt.
func (f *FaultFS) IsDir(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.nodes[clean(name)]
	return ok && n.dir
}

// Names nennt alle Einträge, sortiert.
func (f *FaultFS) Names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.nodes))
	for name := range f.nodes {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ---- store.FS -------------------------------------------------------------

// begin zählt einen Aufruf, hält ihn fest, wartet ggf. auf einen Hänger und
// liefert den eingebauten Fehler (nil = normal ausführen). writes: der
// Aufruf würde etwas ändern (für ReadOnly).
func (f *FaultFS) begin(op string, writes bool, names ...string) (int, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.log = append(f.log, Call{N: n, Op: op, Names: names})
	var err error
	if f.failAt == n || f.crashAt > 0 && n >= f.crashAt {
		err = ErrInjected
	}
	var wait []chan struct{}
	for _, name := range names {
		if e, ok := f.fails[name]; ok && err == nil {
			err = e
		}
		if e, ok := f.opFails[opKey{name, op}]; ok && err == nil {
			err = e
		}
		if ch, ok := f.hangs[name]; ok {
			wait = append(wait, ch)
		}
	}
	if err == nil && writes && f.readOnly != nil {
		err = f.readOnly
	}
	f.mu.Unlock()
	for _, ch := range wait {
		<-ch
	}
	return n, err
}

// end hält das Ergebnis eines Aufrufs im Log fest.
func (f *FaultFS) end(n int, err error) error {
	f.mu.Lock()
	f.log[n-1].Err = err
	f.mu.Unlock()
	return err
}

func pathErr(op, name string, err error) error { return &fs.PathError{Op: op, Path: name, Err: err} }

var (
	errIsDir    = errors.New("ist ein Ordner")
	errNotDir   = errors.New("ist kein Ordner")
	errNotEmpty = errors.New("Ordner nicht leer")
	errNoLinks  = errors.New("Dateisystem ohne Hardlinks")
)

func (f *FaultFS) ReadFile(name string) ([]byte, error) {
	name = clean(name)
	n, err := f.begin("ReadFile", false, name)
	if err != nil {
		return nil, f.end(n, pathErr("read", name, err))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	nd, ok := f.nodes[name]
	switch {
	case !ok:
		err = pathErr("open", name, fs.ErrNotExist)
	case nd.dir:
		err = pathErr("read", name, errIsDir)
	}
	f.log[n-1].Err = err
	if err != nil {
		return nil, err
	}
	return bytes.Clone(nd.ino.data), nil
}

func (f *FaultFS) WriteFileSync(name string, data []byte) error {
	return f.write("WriteFileSync", name, data, false)
}

func (f *FaultFS) AppendSync(name string, data []byte) error {
	return f.write("AppendSync", name, data, true)
}

func (f *FaultFS) write(op, name string, data []byte, appendTo bool) error {
	name = clean(name)
	n, err := f.begin(op, true, name)
	if err != nil {
		return f.end(n, pathErr("write", name, err))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	data = bytes.Clone(data)
	if fn, ok := f.mangle[name]; ok {
		data = fn(data)
	}
	if cut, ok := f.short[name]; ok && cut < len(data) {
		data = data[:cut]
	}
	nd, ok := f.nodes[name]
	switch {
	case ok && nd.dir:
		err = pathErr("open", name, errIsDir)
	case f.parentErr(name) != nil:
		err = pathErr("open", name, f.parentErr(name))
	case !ok:
		f.nodes[name] = &node{ino: &inode{data: data, modTime: f.stamp()}}
	case appendTo:
		nd.ino.data = append(nd.ino.data, data...)
		nd.ino.modTime = f.stamp()
	default:
		nd.ino.data = data // wie O_TRUNC: trifft auch jeden Hardlink
		nd.ino.modTime = f.stamp()
	}
	if e, ok := f.afterFails[opKey{name, op}]; ok && err == nil {
		err = pathErr("sync", name, e) // geschrieben ist es schon
	}
	f.log[n-1].Err = err
	return err
}

// stamp liefert eine Änderungszeit, die mit jedem Aufruf wächst.
func (f *FaultFS) stamp() time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(f.calls) * time.Second)
}

// parentErr: Lässt sich unter name etwas anlegen? errNotDir, wenn darüber
// eine Datei liegt; fs.ErrNotExist, wenn der Ordner darüber fehlt (wie
// ENOENT). Die Wurzel gibt es immer.
func (f *FaultFS) parentErr(name string) error {
	if f.parentIsFile(name) {
		return errNotDir
	}
	p := filepath.Dir(name)
	if p == name || filepath.Dir(p) == p || p == "." {
		return nil
	}
	if _, ok := f.nodes[p]; !ok {
		return fs.ErrNotExist
	}
	return nil
}

// mkParents legt die fehlenden Ordner über name an (nur für Put/PutDir).
func (f *FaultFS) mkParents(name string) {
	for p := filepath.Dir(name); p != name; name, p = p, filepath.Dir(p) {
		if _, ok := f.nodes[p]; !ok {
			f.nodes[p] = &node{dir: true}
		}
	}
}

// parentIsFile: Liegt über name eine Datei statt eines Ordners?
func (f *FaultFS) parentIsFile(name string) bool {
	for p := filepath.Dir(name); p != name; name, p = p, filepath.Dir(p) {
		if nd, ok := f.nodes[p]; ok && !nd.dir {
			return true
		}
	}
	return false
}

// children: die Einträge unter dem Ordner dir (alle Ebenen).
func (f *FaultFS) children(dir string) []string {
	prefix := dir + string(filepath.Separator)
	var out []string
	for name := range f.nodes {
		if strings.HasPrefix(name, prefix) {
			out = append(out, name)
		}
	}
	return out
}

func (f *FaultFS) Rename(from, to string) error {
	from, to = clean(from), clean(to)
	n, err := f.begin("Rename", true, from, to)
	if err != nil {
		return f.end(n, &fs.PathError{Op: "rename", Path: from + " " + to, Err: err})
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	src, okSrc := f.nodes[from]
	dst, okDst := f.nodes[to]
	switch {
	case !okSrc:
		err = fs.ErrNotExist
	case from == to:
	case okDst && dst.dir:
		err = errIsDir // eine Datei oder einen Ordner nie über einen Ordner
	case okDst && src.dir:
		err = errNotDir
	case f.parentErr(to) != nil:
		err = f.parentErr(to)
	case src.dir:
		for _, child := range f.children(from) {
			f.nodes[to+strings.TrimPrefix(child, from)] = f.nodes[child]
			delete(f.nodes, child)
		}
		f.nodes[to] = src
		delete(f.nodes, from)
	default:
		f.nodes[to] = src // ersetzt das Ziel in einem Schritt
		delete(f.nodes, from)
	}
	if err != nil {
		err = &fs.PathError{Op: "rename", Path: from + " " + to, Err: err}
	}
	f.log[n-1].Err = err
	return err
}

func (f *FaultFS) Link(from, to string) error {
	from, to = clean(from), clean(to)
	n, err := f.begin("Link", true, from, to)
	if err != nil {
		return f.end(n, &fs.PathError{Op: "link", Path: from + " " + to, Err: err})
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	src, okSrc := f.nodes[from]
	_, okDst := f.nodes[to]
	switch {
	case f.noLinks:
		err = errNoLinks
	case !okSrc:
		err = fs.ErrNotExist
	case src.dir:
		err = errIsDir
	case okDst:
		err = fs.ErrExist
	case f.parentErr(to) != nil:
		err = f.parentErr(to)
	default:
		f.nodes[to] = &node{ino: src.ino} // derselbe inode
	}
	if err != nil {
		err = &fs.PathError{Op: "link", Path: from + " " + to, Err: err}
	}
	f.log[n-1].Err = err
	return err
}

func (f *FaultFS) Remove(name string) error {
	name = clean(name)
	n, err := f.begin("Remove", true, name)
	if err != nil {
		return f.end(n, pathErr("remove", name, err))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	nd, ok := f.nodes[name]
	switch {
	case !ok: // fehlt: kein Fehler (Vertrag von store.FS)
	case nd.dir && len(f.children(name)) > 0:
		err = pathErr("remove", name, errNotEmpty)
	default:
		delete(f.nodes, name)
	}
	f.log[n-1].Err = err
	return err
}

func (f *FaultFS) SyncDir(dir string) error {
	dir = clean(dir)
	n, err := f.begin("SyncDir", false, dir)
	if err != nil {
		return f.end(n, pathErr("sync", dir, err))
	}
	return nil
}

func (f *FaultFS) Stat(name string) (fs.FileInfo, error) {
	name = clean(name)
	n, err := f.begin("Stat", false, name)
	if err != nil {
		return nil, f.end(n, pathErr("stat", name, err))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	nd, ok := f.nodes[name]
	if !ok {
		err = pathErr("stat", name, fs.ErrNotExist)
		f.log[n-1].Err = err
		return nil, err
	}
	if nd.dir {
		return fileInfo{name: filepath.Base(name), mode: fs.ModeDir | 0o700}, nil
	}
	return fileInfo{name: filepath.Base(name), size: int64(len(nd.ino.data)), mode: 0o600,
		mod: nd.ino.modTime, ino: nd.ino}, nil
}

// SameFile: beide Namen zeigen auf dieselbe Datei. Ein eingebauter Fehler
// liefert false (unbekannt).
func (f *FaultFS) SameFile(a, b string) bool {
	a, b = clean(a), clean(b)
	n, err := f.begin("SameFile", false, a, b)
	if err != nil {
		_ = f.end(n, err) // nur fürs Log: SameFile kennt keinen Fehler, "unbekannt" heißt false
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	na, okA := f.nodes[a]
	nb, okB := f.nodes[b]
	return okA && okB && !na.dir && !nb.dir && na.ino == nb.ino
}

func (f *FaultFS) MkdirAll(dir string) error {
	dir = clean(dir)
	n, err := f.begin("MkdirAll", true, dir)
	if err != nil {
		return f.end(n, pathErr("mkdir", dir, err))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for p := dir; ; p = filepath.Dir(p) {
		if nd, ok := f.nodes[p]; ok {
			if !nd.dir {
				err = pathErr("mkdir", p, errNotDir)
				f.log[n-1].Err = err
				return err
			}
		} else {
			f.nodes[p] = &node{dir: true}
		}
		if filepath.Dir(p) == p {
			return nil
		}
	}
}

func (f *FaultFS) ReadDir(dir string) ([]string, error) {
	dir = clean(dir)
	n, err := f.begin("ReadDir", false, dir)
	if err != nil {
		return nil, f.end(n, pathErr("readdir", dir, err))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	nd, ok := f.nodes[dir]
	switch {
	case ok && !nd.dir:
		err = pathErr("readdir", dir, errNotDir)
	case !ok && filepath.Dir(dir) != dir:
		err = pathErr("open", dir, fs.ErrNotExist) // wie das echte System
	}
	if err != nil {
		f.log[n-1].Err = err
		return nil, err
	}
	var out []string
	for name := range f.nodes {
		if name != dir && filepath.Dir(name) == dir {
			out = append(out, filepath.Base(name))
		}
	}
	sort.Strings(out)
	return out, nil
}

// Lock legt name bei Bedarf an und sperrt es; hält schon jemand die
// Sperre (auch dieselbe FaultFS über ein früheres Lock), store.ErrLocked.
func (f *FaultFS) Lock(name string) (func() error, error) {
	name = clean(name)
	n, err := f.begin("Lock", true, name)
	if err != nil {
		return nil, f.end(n, pathErr("lock", name, err))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	nd, ok := f.nodes[name]
	switch {
	case ok && nd.dir:
		err = pathErr("open", name, errIsDir)
	case !ok && f.parentErr(name) != nil:
		err = pathErr("open", name, f.parentErr(name)) // im fehlenden Ordner nicht anlegbar
	case f.locks[name]:
		err = store.ErrLocked
	}
	if err != nil {
		f.log[n-1].Err = err
		return nil, err
	}
	if !ok {
		f.nodes[name] = &node{ino: &inode{modTime: f.stamp()}}
	}
	f.locks[name] = true
	var once sync.Once
	return func() error {
		once.Do(func() {
			f.mu.Lock()
			delete(f.locks, name)
			f.mu.Unlock()
		})
		return nil
	}, nil
}

// fileInfo ist das fs.FileInfo der FaultFS.
type fileInfo struct {
	name string
	size int64
	mode fs.FileMode
	mod  time.Time
	ino  *inode
}

func (i fileInfo) Name() string       { return i.name }
func (i fileInfo) Size() int64        { return i.size }
func (i fileInfo) Mode() fs.FileMode  { return i.mode }
func (i fileInfo) ModTime() time.Time { return i.mod }
func (i fileInfo) IsDir() bool        { return i.mode.IsDir() }
func (i fileInfo) Sys() any           { return i.ino }

// String hilft beim Lesen von Testausgaben.
func (c Call) String() string {
	s := fmt.Sprintf("%d %s %s", c.N, c.Op, strings.Join(c.Names, " → "))
	if c.Err != nil {
		s += " (" + c.Err.Error() + ")"
	}
	return s
}
