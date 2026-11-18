package notify

import (
	"context"
	"strconv"
	"time"

	"fwalarm/internal/core"
	"fwalarm/internal/textde"
)

// Die zwei Meldungen bei der Anmeldung (PHP: index.php, Zweig action=login).
// app.Serve setzt sie als web.OnLogin und web.OnLockout; web.Login ruft sie
// über Core.Bg, nachdem die Antwort feststeht – die Weiterleitung wartet nie
// auf Bark.

// noticeLimit: So lange darf eine dieser Meldungen höchstens dauern. Der
// Bark-Client braucht für zwei Versuche schlimmstenfalls gut 20 s; beim
// Beenden wartet der Server bis zu 40 s auf Arbeit nebenher (Spec §6.4).
const noticeLimit = 30 * time.Second

// userBytes: so viel vom Benutzernamen steht in der Meldung (PHP:
// substr($user, 0, 40) – Bytes; ein angeschnittenes Zeichen macht bark.ASCII
// zu "?").
const userBytes = 40

// LoginNotice meldet eine gelungene Anmeldung (Art login_success, ab Werk
// aus), wenn "Melden bei" diese Rolle umfasst: Admin oder Lese-Benutzer, der
// Name und die Adresse des Besuchers (ip). Die Signatur ist die von
// web.OnLogin.
func LoginNotice(c *core.Core, role, user, ip string) {
	kind := Settings(c).Types[KindLoginSuccess]
	if !kind.Enabled {
		return
	}
	if who := kind.Values["who"]; who != "all" && who != role {
		return
	}
	who := "Lese-Benutzer"
	if role == "admin" {
		who = "Admin"
	}
	if len(user) > userBytes {
		user = user[:userBytes]
	}
	ctx, cancel := context.WithTimeout(context.Background(), noticeLimit)
	defer cancel()
	cfg := c.Cfg()
	// Das Ergebnis hat keine Folge: Wie in PHP wird eine Meldung zur
	// Anmeldung nicht wiederholt (das Journal hat den Fehlschlag von bark).
	Send(ctx, c, KindLoginSuccess, "Dashboard: Anmeldung",
		textde.Stamp(c.Clock.Now(), cfg.Location())+who+" '"+user+"' hat sich angemeldet (von "+ip+").", false)
}

// LockoutNotice meldet, dass ein Absender (ip) nach zu vielen Fehlversuchen
// gesperrt wurde – einmal je Sperre, für windowSeconds Sekunden (Art
// login_lockout). Die Signatur ist die von web.OnLockout.
func LockoutNotice(c *core.Core, ip string, windowSeconds int) {
	ctx, cancel := context.WithTimeout(context.Background(), noticeLimit)
	defer cancel()
	cfg := c.Cfg()
	// Ergebnis ohne Folge, wie oben.
	Send(ctx, c, KindLoginLockout, "Dashboard: Login gesperrt",
		textde.Stamp(c.Clock.Now(), cfg.Location())+"Zu viele fehlgeschlagene Login-Versuche von "+ip+
			" - IP fuer "+strconv.Itoa(windowSeconds/60)+" min gesperrt.", false)
}
