package dashapi

import (
	"net/http"
	"slices"

	"fwalarm/internal/logview"
	"fwalarm/internal/model"
	"fwalarm/internal/web"
)

// Die Aktion log_view (PHP: dashboard_api.php, Zweig log_view): der Verlauf
// für das Dashboard – nur lesend, deshalb auch für die Lese-Rolle. Welche
// Ereignisse und Felder hinausgehen, steht allein in der Tabelle von logview;
// gelesen wird aus dem Speicher, nichts wird geändert.

func init() { Register("log_view", Action{Handle: handleLogView}) }

// handleLogView: limit und filter aus der Adresse (wie $_GET, auch bei einem
// POST). limit wie PHPs (int), ohne Angabe 25, begrenzt auf 1…100 ("Ältere
// anzeigen" lädt mehr nach, aber nie den ganzen Verlauf). server_time für
// relative Zeitangaben ("vor 5 min") – so spielt eine falsch gehende Uhr des
// Browsers keine Rolle.
func handleLogView(x *Ctx) {
	limit := int64(25)
	if v, ok := web.Query(x.R, "limit"); ok {
		limit = model.IntOf(v)
	}
	limit = max(1, min(100, limit))
	// Jeder Wert außerhalb von logview.Filters zählt als all.
	filter, _ := web.Query(x.R, "filter")
	if !slices.Contains(logview.Filters, filter) {
		filter = "all"
	}
	views := logview.Read(x.Core, int(limit), filter, x.Sess.Role == "admin")
	entries := make([]web.Object, 0, len(views))
	for _, v := range views {
		keys := logview.Keys(v)
		obj := make(web.Object, 0, len(keys))
		for _, k := range keys {
			obj = append(obj, web.Member{Key: k, Value: v[k]})
		}
		entries = append(entries, obj)
	}
	web.JSON(x.W, http.StatusOK, web.Object{
		{Key: "ok", Value: true},
		{Key: "entries", Value: entries},
		{Key: "server_time", Value: x.Core.Clock.Now().Unix()},
	})
}
