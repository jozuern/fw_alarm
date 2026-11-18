package web

import (
	"context"
	"crypto/subtle"
	"slices"
	"sync"
	"time"

	"fwalarm/internal/clock"
	"fwalarm/internal/config"
	"fwalarm/internal/core"
)

// Begrenzte Passwortprüfungen (Spec §6.8). Eine Prüfung kostet Rechenzeit
// und Speicher (Argon2id: 19 MiB), deshalb:
//   - höchstens zwei zugleich, höchstens eine je Absender (ThrottleKey);
//   - wer warten muss, wartet der Reihe nach je Absender – ein Absender mit
//     vielen Versuchen drängt die anderen nicht zurück –, höchstens 5 s;
//     danach LoginBusy (503), und der Versuch zählt nicht (er wurde nie
//     geprüft);
//   - die Pause nach einem Fehlversuch liegt außerhalb der Plätze (login.go).

// verifyPassword ist die Passwortprüfung – im Programm immer
// config.VerifyPassword. Nur die Tests dieses Pakets setzen eine eigene ein
// (von außen nicht erreichbar), um Prüfungen zu zählen und zu takten.
var verifyPassword = config.VerifyPassword

const (
	verifySlots   = 2               // Prüfungen zugleich
	verifyMaxWait = 5 * time.Second // längstes Warten auf einen Platz
)

// passwordTarget sagt, gegen welchen Hash ein Versuch geprüft wird und
// welche Rolle ein Treffer ergibt. Jeder Versuch kostet genau EINE Prüfung
// (index.php:64-84): Ein Name ist ein Konto, wenn er nicht leer ist und
// genau dem Admin bzw. dem Lese-Benutzer gleicht (zuerst dem Admin; der
// Lese-Benutzer nur mit Hash, index.php:73). Mit brauchbarem Hash wird gegen
// ihn geprüft. Sonst – unbekannter Name oder unbrauchbarer Hash – gegen
// decoyHash, und ein Treffer meldet niemanden an (role ""): Die Antwortzeit
// verrät so nicht, ob es den Namen gibt.
func passwordTarget(cfg *config.Config, user string) (hash, role string) {
	switch {
	case cfg.DashboardUser != "" && sameText(cfg.DashboardUser, user):
		if config.PasswordHashUsable(cfg.DashboardPasswordHash) {
			return cfg.DashboardPasswordHash, "admin"
		}
	case cfg.DashboardReadonlyUser != "" && cfg.DashboardReadonlyPasswordHash != "" &&
		sameText(cfg.DashboardReadonlyUser, user):
		if config.PasswordHashUsable(cfg.DashboardReadonlyPasswordHash) {
			return cfg.DashboardReadonlyPasswordHash, "readonly"
		}
	}
	return decoyHash(cfg), ""
}

// decoyHash ist der Hash für Versuche ohne Konto: der ECHTE Hash des Admins
// (index.php:78-83 – ein fester Wegwerf-Hash mit anderem Aufwand, etwa ein
// Argon2id gegen ein übernommenes bcrypt, verriete über die Antwortzeit, ob
// es den Namen gibt). Ist er unbrauchbar, der des Lese-Benutzers; ist auch
// der es, ein eigener Argon2id-Hash mit den Werten von fw-alarm
// hash-password (dann meldet sich ohnehin niemand an – der Versuch kostet
// trotzdem eine Prüfung wie jeder andere).
func decoyHash(cfg *config.Config) string {
	switch {
	case config.PasswordHashUsable(cfg.DashboardPasswordHash):
		return cfg.DashboardPasswordHash
	case cfg.DashboardReadonlyUser != "" && config.PasswordHashUsable(cfg.DashboardReadonlyPasswordHash):
		return cfg.DashboardReadonlyPasswordHash
	}
	return spareHash()
}

// spareHash: ein Argon2id-Hash über ein zufälliges Passwort, einmal erzeugt
// (beim ersten Bedarf, nicht beim Start).
var spareHash = sync.OnceValue(func() string {
	h, err := config.HashPassword(string(randomBytes(16)))
	if err != nil {
		// Kann nicht eintreten (crypto/rand scheitert nie); dann prüft der
		// Versuch eben gegen nichts.
		return ""
	}
	return h
})

// sameText vergleicht in konstanter Zeit (PHP: hash_equals).
func sameText(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// slotQueue verteilt die Plätze für Passwortprüfungen.
type slotQueue struct {
	mu      sync.Mutex
	free    int                      // freie Plätze
	busy    map[string]bool          // Absender mit laufender Prüfung
	waiting map[string][]*slotWaiter // Wartende je Absender, in Ankunftsfolge
	turn    []string                 // Absender mit Wartenden; vorn, wer am längsten nicht dran war
}

type slotWaiter struct {
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
		waiting: map[string][]*slotWaiter{}})
	return q.(*slotQueue)
}

// acquire wartet auf einen Platz für den Absender who – höchstens 5 s auf
// der Uhr clk, und nur, solange der Aufrufer noch da ist (ctx). ok:
// zugeteilt; release gibt ihn zurück (mehrfach rufbar). Sonst ist der
// Absender aus der Schlange genommen.
func (q *slotQueue) acquire(ctx context.Context, clk clock.Clock, who string) (release func(), ok bool) {
	q.mu.Lock()
	if q.free > 0 && !q.busy[who] {
		// Frei und who prüft gerade nicht: Dann wartet auch niemand, der dran
		// wäre (dispatch hätte ihm den Platz schon gegeben) – kein Zeitgeber.
		q.take(who)
		q.mu.Unlock()
		return q.releaser(who), true
	}
	w := &slotWaiter{ready: make(chan struct{})}
	if len(q.waiting[who]) == 0 {
		q.turn = append(q.turn, who)
	}
	q.waiting[who] = append(q.waiting[who], w)
	q.mu.Unlock()

	select {
	case <-w.ready:
		return q.releaser(who), true
	case <-clk.After(verifyMaxWait):
	case <-ctx.Done():
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	select {
	case <-w.ready:
		// Genau beim Ablauf zugeteilt: Der Platz gehört schon diesem Versuch.
		return q.releaser(who), true
	default:
	}
	q.leave(who, w)
	return nil, false
}

// take belegt einen Platz für who (unter mu).
func (q *slotQueue) take(who string) {
	q.free--
	q.busy[who] = true
}

// releaser: die Rückgabe des Platzes von who, höchstens einmal wirksam.
func (q *slotQueue) releaser(who string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			q.mu.Lock()
			defer q.mu.Unlock()
			q.free++
			delete(q.busy, who)
			q.dispatch()
		})
	}
}

// dispatch gibt freie Plätze an Wartende (unter mu): reihum je Absender –
// der erste in turn, der gerade nicht prüft, bekommt seinen ältesten
// Wartenden und rückt ans Ende, wenn er noch weitere hat.
func (q *slotQueue) dispatch() {
	for q.free > 0 {
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
		q.take(who)
		close(w.ready)
	}
}

// leave nimmt den Wartenden w von who aus der Schlange (unter mu).
func (q *slotQueue) leave(who string, w *slotWaiter) {
	ws := slices.DeleteFunc(q.waiting[who], func(x *slotWaiter) bool { return x == w })
	if len(ws) > 0 {
		q.waiting[who] = ws
		return
	}
	delete(q.waiting, who)
	q.turn = slices.DeleteFunc(q.turn, func(x string) bool { return x == who })
}
