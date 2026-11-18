package web

import (
	"bytes"
	"embed"
	"html/template"
	"net/http"
	"sync"

	"fwalarm/internal/core"
)

// Die Seiten (PHP: index.php, classic.php, lib/shell.php): die Anmeldeseite
// und die App-Hülle der Oberfläche, beide über html/template aus
// eingebetteten Vorlagen (templates/) – Byte für Byte wie PHP sie schreibt
// (TestPagesEqualPHP). Alles kommt aus dem Speicher: Vorlagen, Versionen der
// Dateien, Konfiguration, Zustand.

//go:embed templates/*.html
var templateFiles embed.FS

// pageTemplates: login.html, app.html und der gemeinsame Kopf "head"
// (head.html). Ein Fehler darin fällt beim Start auf (und in jedem Test).
var pageTemplates = template.Must(template.ParseFS(templateFiles, "templates/*.html"))

// Meldungen der Anmeldeseite (PHP: index.php; LoginBusy: Spec §6.8).
const (
	textLoginFailed    = "Login fehlgeschlagen."
	textLoginBlocked   = "Zu viele Fehlversuche von dieser Adresse – bitte später erneut versuchen."
	textLoginForbidden = "Anmeldung nur direkt über diese Seite möglich."
	textLoginBusy      = "Gerade zu viele Anmeldungen gleichzeitig – bitte in ein paar Sekunden noch einmal versuchen."
)

// pageType: wie PHP ohne eigenen Content-Type (default_charset UTF-8).
const pageType = "text/html; charset=UTF-8"

// appScripts: die Skripte der Oberfläche. Die Reihenfolge ist Pflicht: Jede
// Datei hängt ihren Teil an window.FW, spätere nutzen die früheren (PHP:
// app_script_paths).
var appScripts = []string{
	"assets/vendor/htm-preact-3.1.1.umd.js",
	"assets/app/logic.js",
	"assets/app/api.js",
	"assets/app/store.js",
	"assets/app/ui.js",
	"assets/app/view-overview.js",
	"assets/app/view-recipients.js",
	"assets/app/view-log.js",
	"assets/app/view-settings.js",
	"assets/app/main.js",
}

// headAssets: die Adressen im gemeinsamen Kopf, je mit ?v=.
type headAssets struct{ Favicon, TouchIcon, ThemeBoot, CSS string }

type loginData struct {
	Head        headAssets
	Error, User string
}

type appData struct {
	Head                         headAssets
	CSRF, Role, User, PublicBase string
	MuteMax                      int
	Scripts                      []string
}

// pageAssets: die Adressen mit Version, einmal ausgerechnet (die Dateien
// liegen im Programm und ändern sich nicht).
var pageAssets = sync.OnceValues(func() (headAssets, []string) {
	head := headAssets{
		Favicon:   assetURL("assets/favicon.svg"),
		TouchIcon: assetURL("assets/apple-touch-icon.png"),
		ThemeBoot: assetURL("assets/app/theme-boot.js"),
		CSS:       assetURL("assets/app.css"),
	}
	scripts := make([]string, len(appScripts))
	for i, p := range appScripts {
		scripts[i] = assetURL(p)
	}
	return head, scripts
})

// assetURL: die Adresse mit Versions-Parameter (PHP: asset_url – dort die
// Dateizeit, hier AssetVersion; eine fehlende Datei hat wie dort 0).
func assetURL(p string) string {
	v := AssetVersion(p)
	if v == "" {
		v = "0"
	}
	return p + "?v=" + v
}

// IndexHandler ist index.php (auch unter "/"):
//   - POST action=logout: abmelden, wenn die Sitzung gilt und das Feld csrf
//     passt (ein fremdes Formular meldet niemanden ab), dann in jedem Fall
//     302 auf index.php;
//   - POST action=login: Login; gelungen → 302 auf index.php (die Cookies
//     setzt Login), sonst die Anmeldeseite mit Meldung und stehen
//     gebliebenem Namen: Status 200 (falsch), 429 (gesperrt), 403 (fremde
//     Seite), 503 (kein Platz für die Prüfung);
//   - sonst und danach: Gilt die Sitzung der Anfrage, die App-Hülle (auch
//     nach einem Fehlversuch, mit dessen Status – PHP index.php:148), sonst
//     die Anmeldeseite.
//
// Ohne gültige Sitzung entsteht nie eine, und es geht kein Cookie hinaus.
func IndexHandler(c *core.Core) Handler {
	return func(w http.ResponseWriter, r *http.Request) {
		f, ok := ParseForm(w, r)
		if !ok {
			return
		}
		post := r.Method == http.MethodPost
		if post && f.Get("action") == "logout" {
			if s, ok := Resume(c, w, r); ok && s.CSRFMatches(f.Get("csrf")) {
				End(c, w, r)
			}
			redirectToIndex(w)
			return
		}
		status, errText, shown := http.StatusOK, "", ""
		if post && f.Get("action") == "login" {
			var res LoginResult
			res, shown = Login(c, w, r, f)
			switch res {
			case LoginOK:
				redirectToIndex(w)
				return
			case LoginBlocked:
				status, errText = http.StatusTooManyRequests, textLoginBlocked
			case LoginForbidden:
				status, errText = http.StatusForbidden, textLoginForbidden
			case LoginBusy:
				status, errText = http.StatusServiceUnavailable, textLoginBusy
			default:
				errText = textLoginFailed
			}
		}
		if s, ok := Resume(c, w, r); ok {
			renderApp(c, w, status, s)
			return
		}
		renderLogin(c, w, status, errText, shown)
	}
}

// ClassicHandler ist classic.php: Die alte Oberfläche ist fort, alte
// Lesezeichen kommen per 302 (nicht 301 – den merkte sich der Browser) auf
// index.php – jede Methode, ohne Sitzung und ohne Cookie.
func ClassicHandler() Handler {
	return func(w http.ResponseWriter, r *http.Request) { redirectToIndex(w) }
}

// redirectToIndex: 302 auf index.php ohne Rumpf (PHP: header('Location:
// index.php') und exit).
func redirectToIndex(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Location", "index.php")
	h.Set("Content-Type", pageType)
	w.WriteHeader(http.StatusFound)
}

// renderLogin schreibt die Anmeldeseite (PHP: render_login_page); errText
// "" = keine Meldung, user = stehen gebliebener Benutzername.
func renderLogin(c *core.Core, w http.ResponseWriter, status int, errText, user string) {
	head, _ := pageAssets()
	writePage(c, w, status, "login.html", loginData{Head: head, Error: errText, User: user})
}

// renderApp schreibt die App-Hülle für die Sitzung s (PHP: render_app_page):
// Startwerte als meta-Tags – CSRF-Token, Rolle, Benutzer, public_base_url und
// die wirksame Dauer bis "automatisch wieder laut" (Einstellung im Dashboard,
// sonst die Konfiguration).
func renderApp(c *core.Core, w http.ResponseWriter, status int, s *Session) {
	cfg := c.Cfg()
	head, scripts := pageAssets()
	writePage(c, w, status, "app.html", appData{
		Head:       head,
		CSRF:       s.CSRF,
		Role:       sessionRole(s.Role),
		User:       s.User,
		PublicBase: cfg.PublicBaseURL,
		MuteMax:    c.Store.State().MuteMax(cfg.MuteMaxSeconds),
		Scripts:    scripts,
	})
}

// writePage füllt die Vorlage name in einen Puffer und schreibt sie erst
// dann – scheitert die Vorlage (ein Fehler im Programm), geht eine 500 ohne
// Rumpf hinaus statt einer halben Seite.
func writePage(c *core.Core, w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := pageTemplates.ExecuteTemplate(&buf, name, data); err != nil {
		logger(c).Error("Seite nicht gebaut", "vorlage", name, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", pageType)
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes()) // scheitert nur, wenn der Aufrufer schon fort ist
}
