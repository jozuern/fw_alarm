// Package dashapi ist dashboard_api.php: die EINE Tabelle mit den
// Zugangsregeln aller Dashboard-Aktionen, die Prüfung davor (Handler) und die
// Aktionen selbst – status hier, die übrigen bringen spätere Aufgaben mit
// (Register). Teile der Antwort von status, die anderen Paketen gehören
// (Befehl, Demo-Modus, Wächter …), kommen über StatusPart.
package dashapi

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"fwalarm/internal/core"
	"fwalarm/internal/web"
)

// Action ist eine Aktion mit ihren Regeln: Post = nur per POST (alles, was
// etwas ändert oder auslöst), CSRF = CSRF-Token nötig (Kopfzeile
// X-CSRF-Token oder Feld csrf), Admin = nur die Admin-Rolle. Handle läuft
// erst, wenn alle drei erfüllt sind – es prüft sie nicht noch einmal.
type Action struct {
	Post, CSRF, Admin bool
	Handle            func(x *Ctx)
}

// Ctx ist, was eine Aktion bekommt: die Anfrage mit ihren Feldern (Form: der
// Rumpf eines POST, sonst leer) und die angemeldete Person.
type Ctx struct {
	Core *core.Core
	W    http.ResponseWriter
	R    *http.Request
	Form *web.Form
	Sess *web.Session
}

// rule ist eine Zeile der Tabelle.
type rule struct {
	name              string
	post, csrf, admin bool
}

// table: alle Aktionen mit ihren Regeln – an EINER Stelle, in der
// Reihenfolge von DASHBOARD_ACTIONS in dashboard_api.php (TestActionTable
// liest sie dort nach). Eine Aktion, die hier fehlt, ist gesperrt: eine neue
// Aktion = eine neue Zeile.
var table = []rule{
	// Lesen (auch per GET)
	{"status", false, false, false},
	{"keys_list", false, false, false},
	{"log_view", false, false, false},
	// lesend, aber nur für den Admin: Welche Meldungen er bekommt, geht die
	// Lese-Rolle nichts an
	{"notify_list", false, false, true},
	{"settings_list", false, false, true},
	// Ändern/Auslösen
	{"enqueue", true, true, true},
	{"cancel", true, true, true},
	{"demo_set", true, true, true},
	{"keys_add", true, true, true},
	{"keys_delete", true, true, true},
	{"keys_rename", true, true, true},
	{"keys_replace_key", true, true, true},
	{"keys_set_phone", true, true, true},
	{"keys_new_mute_token", true, true, true},
	{"keys_mute", true, true, true},
	{"keys_set_mute_permanent", true, true, true},
	{"keys_set_mute_volume", true, true, true},
	{"keys_test", true, true, true},
	{"notify_set", true, true, true},
	{"settings_set", true, true, true},
	{"notify_test", true, true, true},
	{"alarm", true, true, true},
	{"false_alarm", true, true, true},
}

// registry hält zu jeder Zeile der Tabelle ihre Regeln und – sobald eine
// Aufgabe sie gebaut hat – ihren Handler.
type registry struct {
	mu   sync.RWMutex
	rows map[string]*Action
}

func newRegistry() *registry {
	g := &registry{rows: make(map[string]*Action, len(table))}
	for _, r := range table {
		g.rows[r.name] = &Action{Post: r.post, CSRF: r.csrf, Admin: r.admin}
	}
	return g
}

// actions: das Register des Programms.
var actions = newRegistry()

// Register gibt der Aktion name ihren Handler (a.Handle). Die Regeln stehen in
// der Tabelle oben: Gibt a sie mit an, müssen sie genau so lauten – keine
// Aktion bekommt hier andere. Eine Aktion außerhalb der Tabelle, ein
// fehlender Handler oder ein zweiter für dieselbe Aktion sind Fehler im
// Aufbau des Programms (Abbruch beim Start). Aufzurufen, bevor der Server
// Anfragen annimmt (init oder app).
func Register(name string, a Action) { actions.register(name, a) }

func (g *registry) register(name string, a Action) {
	g.mu.Lock()
	defer g.mu.Unlock()
	row := g.rows[name]
	switch {
	case row == nil:
		panic(fmt.Sprintf("dashapi: Aktion %q steht nicht in der Tabelle", name))
	case a.Handle == nil:
		panic(fmt.Sprintf("dashapi: Aktion %q ohne Handler", name))
	case (a.Post || a.CSRF || a.Admin) && (a.Post != row.Post || a.CSRF != row.CSRF || a.Admin != row.Admin):
		panic(fmt.Sprintf("dashapi: Aktion %q mit anderen Regeln als in der Tabelle (post=%v csrf=%v admin=%v)",
			name, row.Post, row.CSRF, row.Admin))
	case row.Handle != nil:
		panic(fmt.Sprintf("dashapi: Aktion %q zweimal registriert", name))
	}
	row.Handle = a.Handle
}

// lookup liefert eine Abschrift der Aktion name; false: nicht in der Tabelle.
func (g *registry) lookup(name string) (Action, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	row := g.rows[name]
	if row == nil {
		return Action{}, false
	}
	return *row, true
}

// Antworten der Prüfung – Status und Text wie dashboard_api.php und
// require_csrf()/require_dashboard_admin() in common.php.
var (
	answerNotAuthed = web.Object{{Key: "ok", Value: false}}
	answerPostOnly  = web.Object{{Key: "ok", Value: false}, {Key: "message", Value: "Diese Aktion ist nur per POST erlaubt."}}
	answerUnknown   = web.Object{{Key: "ok", Value: false}, {Key: "message", Value: "Unbekannte Aktion."}}
	answerCSRF      = web.Object{{Key: "ok", Value: false}, {Key: "code", Value: "csrf"},
		{Key: "message", Value: "Sicherheits-Token veraltet – Seite neu laden. Aktion wurde NICHT ausgeführt."}}
	answerReadonly = web.Object{{Key: "ok", Value: false}, {Key: "code", Value: "readonly"},
		{Key: "message", Value: "Nur Lesezugriff: Dieser Benutzer darf nichts auslösen oder ändern."}}
	answerNotBuilt = web.Object{{Key: "ok", Value: false}, {Key: "message", Value: "Noch nicht gebaut."}}
)

// Handler ist dashboard_api.php. Vor jeder Aktion, in dieser Reihenfolge
// (die früheste verletzte Regel antwortet, die Aktion läuft dann nicht):
//
//  1. Rumpf lesen (zu groß: 413, wie Nginx vor PHP).
//  2. Anmeldung: keine gültige Sitzung → 401 {"ok":false}, kein Cookie.
//  3. Aktion: per POST das Feld action, sonst der Parameter action der
//     Adresse (fehlt er: status). Unbekannt oder nur per POST, aber kein
//     POST → 405.
//  4. Unbekannt (per POST) → 400.
//  5. CSRF: Token aus der Kopfzeile X-CSRF-Token – ist sie da, zählt nur sie,
//     auch leer –, sonst aus dem Feld csrf; passt es nicht → 403 "csrf".
//  6. Rolle: Admin-Aktion, aber Lese-Rolle → 403 "readonly".
//  7. Noch nicht gebaut (Handle fehlt) → 501.
func Handler(c *core.Core) web.Handler {
	return func(w http.ResponseWriter, r *http.Request) {
		f, ok := web.ParseForm(w, r)
		if !ok {
			return
		}
		s, ok := web.Resume(c, w, r)
		if !ok {
			web.JSON(w, http.StatusUnauthorized, answerNotAuthed)
			return
		}
		post := r.Method == http.MethodPost
		a, known := actions.lookup(actionName(r, f, post))
		switch {
		case (!known || a.Post) && !post:
			web.JSON(w, http.StatusMethodNotAllowed, answerPostOnly)
		case !known:
			web.JSON(w, http.StatusBadRequest, answerUnknown)
		case a.CSRF && !s.CSRFMatches(sentCSRF(r, f)):
			web.JSON(w, http.StatusForbidden, answerCSRF)
		case a.Admin && s.Role != "admin":
			web.JSON(w, http.StatusForbidden, answerReadonly)
		case a.Handle == nil:
			web.JSON(w, http.StatusNotImplemented, answerNotBuilt)
		default:
			a.Handle(&Ctx{Core: c, W: w, R: r, Form: f, Sess: s})
		}
	}
}

// actionName: per POST das Feld action ("" wenn es fehlt), sonst der
// Parameter action der Adresse, "status" wenn es ihn nicht gibt (PHP:
// $_POST['action'] ?? ” bzw. $_GET['action'] ?? 'status').
func actionName(r *http.Request, f *web.Form, post bool) string {
	if post {
		return f.Get("action")
	}
	if v, ok := web.Query(r, "action"); ok {
		return v
	}
	return "status"
}

// sentCSRF: das mitgeschickte Token (PHP: $_SERVER['HTTP_X_CSRF_TOKEN'] ??
// $_POST['csrf'] ?? ”). Ist die Kopfzeile da, zählt sie – auch leer, dann
// passt nichts. Mehrere Zeilen kommen wie bei Nginx mit ", " verbunden an
// (und passen damit nie).
func sentCSRF(r *http.Request, f *web.Form) string {
	if vals := r.Header.Values("X-CSRF-Token"); len(vals) > 0 {
		return strings.Join(vals, ", ")
	}
	return f.Get("csrf")
}
