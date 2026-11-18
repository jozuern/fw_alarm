package web

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"fwalarm/internal/config"
	"fwalarm/internal/core"
)

// Die Anmeldung (PHP: index.php, Zweig action=login; Spec §6.8, §8, §10).
// Login entscheidet nur – die Seite (Weiterleitung, Fehlerseite mit
// Statuscode und Text) baut der Aufrufer.

// LoginResult ist der Ausgang eines Anmeldeversuchs.
type LoginResult int

const (
	// LoginOK: angemeldet, die Sitzung ist angelegt und ihr Cookie gesetzt
	// (PHP: 302 auf index.php).
	LoginOK LoginResult = iota + 1
	// LoginFailed: Benutzer oder Passwort falsch – nach 1 s Pause (PHP: 200,
	// "Login fehlgeschlagen.").
	LoginFailed
	// LoginBlocked: zu viele Fehlversuche von diesem Absender, nichts geprüft
	// (PHP: 429, "Zu viele Fehlversuche von dieser Adresse – bitte später
	// erneut versuchen.").
	LoginBlocked
	// LoginForbidden: Das Formular kam von einer fremden Seite (Sec-Fetch-Site),
	// nichts gezählt (PHP: 403, "Anmeldung nur direkt über diese Seite
	// möglich.").
	LoginForbidden
	// LoginBusy: Fünf Sekunden kein Platz für die Passwortprüfung (oder der
	// Aufrufer ist gegangen), nichts gezählt (Spec §6.8: 503, "Gerade zu viele
	// Anmeldungen gleichzeitig – bitte in ein paar Sekunden noch einmal
	// versuchen.").
	LoginBusy
)

func (r LoginResult) String() string {
	switch r {
	case LoginOK:
		return "LoginOK"
	case LoginFailed:
		return "LoginFailed"
	case LoginBlocked:
		return "LoginBlocked"
	case LoginForbidden:
		return "LoginForbidden"
	case LoginBusy:
		return "LoginBusy"
	}
	return fmt.Sprintf("LoginResult(%d)", int(r))
}

// Meldungen nach einer Anmeldung – gesetzt beim Start (Aufgabe 15), nil =
// keine. Sie laufen nebenher (Core.Bg), nachdem die Antwort feststeht, und
// halten sie nie auf.
var (
	// OnLogin: erfolgreich angemeldet (PHP: Meldungsart login_success); ip
	// ist die Adresse des Besuchers (ClientIP), nicht der Schlüssel der Bremse.
	OnLogin func(c *core.Core, role, user, ip string)
	// OnLockout: Dieser Fehlversuch hat die Sperre ausgelöst – einmal je
	// Sperre (PHP: Meldungsart login_lockout); windowSeconds ist das
	// geltende Fenster.
	OnLockout func(c *core.Core, ip string, windowSeconds int)
)

const (
	// failPause: einfache Bremse nach jedem Fehlversuch (PHP: sleep(1)).
	failPause = time.Second
	// shownUserBytes: so viel vom eingegebenen Namen bleibt im Formular
	// stehen (PHP: substr($user, 0, 80)).
	shownUserBytes = 80
)

// Login prüft einen Anmeldeversuch (Felder user und password aus f). Der
// zweite Wert ist der eingegebene Name, gekürzt, für das Formular der
// Fehlerseite. In dieser Reihenfolge:
//
//  1. Sec-Fetch-Site: Kam das Formular von einer fremden Seite → Forbidden,
//     bevor irgendetwas gezählt wird (sonst ließe jede Webseite den Browser
//     des Admins Fehlversuche absenden und sperrte ihn aus).
//  2. Ist der Absender gesperrt → Blocked: keine Prüfung, kein Platz.
//  3. Platz für die Prüfung (slots.go): mit gültigem Geräte-Ausweis
//     (device.go) in der Vorzugsreihe, sonst in der allgemeinen; höchstens
//     5 s, und ist die allgemeine Reihe voll, sofort → sonst Busy, nicht
//     gezählt.
//  4. Der Versuch wird gezählt (ThrottleHit), VOR der Prüfung: gleichzeitige
//     Versuche bekommen nicht mehr Versuche als erlaubt. Wurde der Absender
//     inzwischen gesperrt → Blocked.
//  5. Genau EINE Prüfung (passwordTarget), dann ist der Platz wieder frei.
//  6. Erfolg: Zähler des Absenders zurück, neue Sitzung (Begin), und – wenn
//     die Anfrage keinen gültigen Ausweis für diese Rolle trug – ein neuer
//     Geräte-Ausweis → OK. Fehlschlag: hat er die Sperre ausgelöst,
//     OnLockout; dann 1 s Pause (außerhalb der Plätze) → Failed.
//
// Die Reihe ändert nur die Wartezeit, nie Antwort, Zählung oder Pause.
//
// Alles läuft im Speicher: Anmelden geht im Notbetrieb und wenn nichts
// gesichert werden kann. Es gelten die Passwörter der geltenden Fassung –
// ist config.toml fehlerhaft, die der letzten gültigen.
func Login(c *core.Core, w http.ResponseWriter, r *http.Request, f *Form) (LoginResult, string) {
	if f == nil {
		f = &Form{}
	}
	user, pass := f.Get("user"), f.Get("password")
	shown := ClipBytes(user, shownUserBytes)
	if fromForeignSite(r) {
		return LoginForbidden, shown
	}
	cfg := c.Cfg() // eine Fassung für Grenzen, Prüfung und Sitzung
	limit := max(1, cfg.LoginMaxFailures)
	windowSeconds := cfg.LoginLockoutWindow() // 60 s bis ein Jahr: als Dauer nie übergelaufen
	window := time.Duration(windowSeconds) * time.Second
	ip := ClientIP(c, r)
	who := ThrottleKey(ip)
	if throttleBlocked(c, "login", who, limit, window) {
		return LoginBlocked, shown
	}
	device, known := devicePassOf(r, cfg)
	lane := "" // allgemeine Reihe
	if known {
		lane = device.id
	}
	release, ok := slotsFor(c).acquire(r.Context(), c.Clock, who, lane)
	if !ok {
		return LoginBusy, shown
	}
	hit, good, role := checkInSlot(c, release, cfg, who, user, pass, limit, window)
	if hit.Blocked {
		return LoginBlocked, shown
	}
	if good {
		ThrottleClear(c, "login", who)
		begin(c, w, r, cfg, user, role)
		if !known || device.role != role {
			setDeviceCookie(c, w, r, cfg, role) // ein vorhandener gültiger bleibt (keine neuen ids)
		}
		if fn := OnLogin; fn != nil {
			c.Bg.Go("Meldung: Anmeldung", func() { fn(c, role, user, ip) })
		}
		return LoginOK, shown
	}
	if fn := OnLockout; fn != nil && hit.Count == limit {
		c.Bg.Go("Meldung: Anmeldung gesperrt", func() { fn(c, ip, windowSeconds) })
	}
	c.Clock.Sleep(failPause)
	return LoginFailed, shown
}

// checkInSlot zählt den Versuch und prüft das Passwort, solange der Platz
// belegt ist; release läuft in jedem Fall (auch nach einem Laufzeitfehler
// in der Prüfung – sonst wäre der Platz für immer fort). good: Passwort
// richtig für ein Konto mit Rolle role.
func checkInSlot(c *core.Core, release func(), cfg *config.Config, who, user, pass string,
	limit int, window time.Duration) (hit Hit, good bool, role string) {
	defer release()
	hit = ThrottleHit(c, "login", who, limit, window)
	if hit.Blocked {
		return hit, false, ""
	}
	hash, target := passwordTarget(cfg, user)
	ok := verifyPassword(hash, pass) // genau eine Prüfung je Versuch
	return hit, ok && target != "", target
}

// fromForeignSite: Sec-Fetch-Site sagt, dass das Formular nicht von dieser
// Seite kam (index.php:47-48). Erlaubt sind nur "same-origin" (das eigene
// Formular), "none" (vom Browser selbst, z.B. Neu-Laden) und eine fehlende
// oder leere Kopfzeile (alte Browser, http://). Auch "same-site" ist fremd:
// eine ANDERE Seite unter derselben Domain. Bewusst kein Vergleich von
// Origin und Host – hinter einem Proxy, der Host umschreibt, sperrte der
// jede Anmeldung. Mehrere Zeilen zählen wie bei Nginx → PHP mit ", "
// verbunden (und sind damit fremd).
func fromForeignSite(r *http.Request) bool {
	site := strings.ToLower(config.TrimPHP(strings.Join(r.Header.Values("Sec-Fetch-Site"), ", ")))
	return site != "" && site != "same-origin" && site != "none"
}
