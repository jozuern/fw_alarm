package web

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"time"

	"fwalarm/internal/config"
	"fwalarm/internal/core"
	"fwalarm/internal/model"
)

// Sitzungen des Dashboards (Spec §8; PHP: start_dashboard_session,
// resume_dashboard_session, is_dashboard_authed, dashboard_auth_stamp,
// refresh_dashboard_cookie, end_dashboard_session, ensure_csrf).
//
// Eine Sitzung gibt es nur für Angemeldete: Sie entsteht bei einer
// erfolgreichen Anmeldung (Begin) und wird fortgesetzt, wenn der Browser ihr
// Cookie schickt (Resume). Die ID sind 32 Zufallsbytes; abgelegt wird nur
// ihr SHA-256 – wer sessions.json liest, kann sich damit nicht anmelden.
// Die Sitzungen liegen in der Ablage (Store.UpdateSessions): Eine Änderung
// gilt sofort im Speicher und wartet nie auf die Platte – Anmelden geht auch,
// wenn nichts gesichert werden kann (Spec §15.11), und eine Sitzung übersteht
// einen Neustart.

const (
	// sessionCookie heißt das Cookie (PHP: DASHBOARD_SESSION_NAME).
	sessionCookie = "boss925_dashboard"
	// sessionMaxIdle: Nach so vielen Sekunden ohne Anfrage ist die Sitzung
	// fort (PHP: SESSION_MAX_IDLE_SECONDS, 12 h). Ebenso lange gilt das
	// Cookie: iOS wirft Cookies ohne Ablaufdatum weg, sobald die
	// Home-Bildschirm-App geschlossen wird.
	sessionMaxIdle = 43200
	// lastSeenEvery: last_seen wird höchstens einmal pro Minute geschrieben
	// (Spec §8) – der Status-Abruf schriebe sonst sessions.json alle 5 s
	// neu. Eine Sitzung endet dadurch höchstens eine Minute früher
	// als 12 h nach ihrer letzten Anfrage, nie später.
	lastSeenEvery  = 60
	sessionIDBytes = 32
	// csrfBytes: wie PHP bin2hex(random_bytes(24)).
	csrfBytes = 24
)

// sessionIDText ist die Form der ID im Cookie: URL-sicheres Base64 ohne
// "=" (43 Zeichen A–Z, a–z, 0–9, "-", "_") – passt ohne Maskierung in ein
// Cookie.
var sessionIDText = base64.RawURLEncoding

// Session ist die angemeldete Person einer Anfrage.
type Session struct {
	User string // Benutzername, wie in der Konfiguration
	Role string // "admin" oder "readonly"
	CSRF string // Token für ändernde Aktionen
}

// CSRFMatches sagt, ob sent das CSRF-Token dieser Sitzung ist – in konstanter
// Zeit (PHP: hash_equals in require_csrf). Ein leeres Token auf einer der
// beiden Seiten passt nie.
func (s *Session) CSRFMatches(sent string) bool {
	if s == nil || s.CSRF == "" || sent == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(s.CSRF), []byte(sent)) == 1
}

// Resume setzt die Sitzung der Anfrage fort (PHP: resume_dashboard_session
// mit is_dashboard_authed und refresh_dashboard_cookie): true = angemeldet;
// dann ist last_seen erneuert (höchstens einmal pro Minute geschrieben) und
// das Cookie für weitere 12 h gesetzt.
//
// Bringt die Anfrage mehrere Sitzungs-Cookies, zählt unter den ersten vier
// das erste, das eine lebende Sitzung nennt (cookies.go). Kein Cookie, eine
// ID ohne Sitzung (erfunden, abgemeldet, beendet) → false, es entsteht
// nichts. Eine Sitzung, die nicht mehr gilt – 12 h ohne Anfrage,
// Benutzername bzw. Passwort-Hash ihres Kontos haben sich geändert, oder ihr
// fehlt Benutzer, CSRF-Token oder Stempel (eine von Hand bearbeitete Datei) –,
// wird gelöscht. In keinem dieser Fälle sendet Resume ein Cookie, auch kein
// Lösch-Cookie: Trifft eine noch laufende Anfrage mit einer ALTEN ID erst
// nach einer Neuanmeldung ein, überschriebe es das gerade vergebene neue.
func Resume(c *core.Core, w http.ResponseWriter, r *http.Request) (*Session, bool) {
	refs := sessionRefsOf(r)
	if len(refs) == 0 {
		return nil, false
	}
	cfg := c.Cfg()
	now := c.Clock.Now().Unix()
	ref, s, ok, dead := liveSession(c.Store.Sessions(), refs, cfg, now)
	if len(dead) > 0 {
		c.Store.UpdateSessions(func(d *model.Sessions) {
			for _, key := range dead {
				if cur, there := d.Sessions[key]; there && !sessionLive(cur, cfg, now) {
					delete(d.Sessions, key)
				}
			}
		})
	}
	if !ok {
		return nil, false
	}
	key, value := ref.key, ref.value
	// Ein last_seen in der Zukunft (die Uhr sprang zurück) zählt als jetzt
	// und wird auf jetzt gesetzt – sonst lebte die Sitzung um den Sprung
	// länger als 12 h nach dieser Anfrage.
	if s.LastSeen > now || now-s.LastSeen >= lastSeenEvery {
		c.Store.UpdateSessions(func(d *model.Sessions) {
			// Nur, wenn sie noch so dasteht: abgemeldet oder beendet (EndAll)
			// bleibt sie fort; eine andere Anfrage hat sie vielleicht schon
			// erneuert.
			if cur, ok := d.Sessions[key]; ok && cur == s {
				cur.LastSeen = now
				d.Sessions[key] = cur
			}
		})
	}
	setSessionCookie(c, w, r, value)
	return &Session{User: s.User, Role: sessionRole(s.Role), CSRF: s.CSRF}, true
}

// Begin legt nach einer erfolgreichen Anmeldung eine neue Sitzung an (PHP:
// start_dashboard_session, session_regenerate_id(true), ensure_csrf): neue
// ID, neues CSRF-Token, gebunden an die Zugangsdaten des Kontos in der
// geltenden Konfiguration; das Cookie gilt 12 h. Die Sitzungen, deren Cookies
// die Anfrage mitbringt (die ersten vier), enden dabei – eine mitgeschickte
// alte ID wird nie übernommen. Jede Rolle außer "admin" wird die Lese-Rolle.
func Begin(c *core.Core, w http.ResponseWriter, r *http.Request, user, role string) *Session {
	return begin(c, w, r, c.Cfg(), user, role)
}

// begin bindet die Sitzung an die Zugangsdaten aus cfg – Login übergibt die
// Fassung, gegen die es das Passwort geprüft hat: Wird mitten in der Prüfung
// das Passwort geändert, gilt die neue Sitzung damit nicht.
func begin(c *core.Core, w http.ResponseWriter, r *http.Request, cfg *config.Config, user, role string) *Session {
	role = sessionRole(role)
	value := sessionIDText.EncodeToString(randomBytes(sessionIDBytes))
	csrf := hex.EncodeToString(randomBytes(csrfBytes))
	now := c.Clock.Now().Unix()
	s := model.Session{User: user, Role: role, CSRF: csrf, AuthStamp: cfg.AuthStamp(role), CreatedAt: now, LastSeen: now}
	old := sessionRefsOf(r)
	key := sessionKey(value)
	c.Store.UpdateSessions(func(d *model.Sessions) {
		for _, ref := range old {
			delete(d.Sessions, ref.key)
		}
		d.Sessions[key] = s
	})
	setSessionCookie(c, w, r, value)
	return &Session{User: user, Role: role, CSRF: csrf}
}

// End meldet ab (PHP: end_dashboard_session). Vorher hat der Aufrufer
// geprüft, dass die Anfrage eine gültige Sitzung hat (Resume → true) und
// das mitgeschickte Token passt (Session.CSRFMatches) – sonst ruft er End
// nicht (ein fremdes Formular soll niemanden abmelden). End löscht die
// Sitzung, die Resume fortsetzt (die erste lebende unter den ersten vier
// Cookies); nur wenn es sie dabei tatsächlich entfernt hat, geht das
// Lösch-Cookie hinaus – statt der Erneuerung, die Resume vorher gesetzt hat.
// Gab es sie nicht (mehr), setzt End nichts (PHP schickt ohne lebende
// Sitzung nie ein Lösch-Cookie). Den Geräte-Ausweis lässt End stehen.
func End(c *core.Core, w http.ResponseWriter, r *http.Request) {
	ref, _, ok, _ := liveSession(c.Store.Sessions(), sessionRefsOf(r), c.Cfg(), c.Clock.Now().Unix())
	if !ok {
		return
	}
	key := ref.key
	removed := false
	c.Store.UpdateSessions(func(d *model.Sessions) {
		if _, there := d.Sessions[key]; there {
			delete(d.Sessions, key)
			removed = true
		}
	})
	if removed {
		putCookie(c, w, r, &http.Cookie{Name: sessionCookie, Value: "", MaxAge: -1, Expires: time.Unix(1, 0)})
	}
}

// EndAll beendet alle Sitzungen – die Konfiguration ist fehlerhaft (Spec
// §6.8): Auch ein verlorenes, angemeldetes Handy ist dann draußen, wenn die
// Passwort-Änderung einen Tippfehler hatte. Anmelden geht danach mit den
// Passwörtern der letzten gültigen Fassung.
func EndAll(c *core.Core) {
	n := len(c.Store.Sessions().Sessions)
	if n == 0 {
		return
	}
	c.Store.UpdateSessions(func(d *model.Sessions) { clear(d.Sessions) })
	logger(c).Warn("Konfiguration fehlerhaft – alle Sitzungen beendet", "anzahl", n)
}

// SweepSessions räumt auf (der Prüf-Lauf ruft das): Sitzungen, die nicht
// mehr gelten – 12 h ohne Anfrage oder Zugangsdaten ihres Kontos geändert –,
// sind fort; ein last_seen in der Zukunft (die Uhr sprang zurück) wird jetzt,
// die Sitzung lebt so höchstens 12 h ab diesem Lauf. Ist nichts zu tun, wird
// nichts geschrieben.
func SweepSessions(c *core.Core) {
	cfg := c.Cfg()
	now := c.Clock.Now().Unix()
	needed := false
	for _, s := range c.Store.Sessions().Sessions {
		if s.LastSeen > now || !sessionLive(s, cfg, now) {
			needed = true
			break
		}
	}
	if !needed {
		return
	}
	c.Store.UpdateSessions(func(d *model.Sessions) {
		for key, s := range d.Sessions {
			switch {
			case !sessionLive(s, cfg, now):
				delete(d.Sessions, key)
			case s.LastSeen > now:
				s.LastSeen = now
				d.Sessions[key] = s
			}
		}
	})
}

// sessionLive: Gilt die Sitzung jetzt noch? Nicht länger als 12 h ohne
// Anfrage (ein last_seen in der Zukunft zählt als jetzt; genau 12 h gelten
// noch, wie PHP), und Benutzername und Passwort-Hash ihres Kontos sind
// dieselben wie bei der Anmeldung (PHP: dashboard_auth_stamp – wer das
// Passwort ändert, meldet ALLE Sitzungen dieses Kontos ab; ein entferntes
// Konto ebenso). Ohne Benutzer, CSRF-Token oder Stempel gilt sie nie (so
// legt Begin keine an – nur eine von Hand bearbeitete Datei). Die Rolle wird
// wie bei Resume gelesen.
func sessionLive(s model.Session, cfg *config.Config, now int64) bool {
	if s.User == "" || s.CSRF == "" || s.AuthStamp == "" {
		return false
	}
	if now-min(s.LastSeen, now) > sessionMaxIdle {
		return false
	}
	role := sessionRole(s.Role)
	name := cfg.DashboardReadonlyUser
	if role == "admin" {
		name = cfg.DashboardUser
	}
	stamp := cfg.AuthStamp(role)
	return stamp != "" && s.User == name &&
		subtle.ConstantTimeCompare([]byte(stamp), []byte(s.AuthStamp)) == 1
}

// sessionRole: "admin" bleibt, jede andere Rolle ist die Lese-Rolle – die
// sichere Richtung (PHP: dashboard_role).
func sessionRole(role string) string {
	if role == "admin" {
		return "admin"
	}
	return "readonly"
}

// sessionRef ist eine ID aus einem Cookie der Anfrage und ihr Schlüssel in
// sessions.json (SHA-256 hex der ID).
type sessionRef struct{ key, value string }

// sessionRefsOf: die IDs unter den ersten vier Sitzungs-Cookies der Anfrage,
// die die Form einer von Begin vergebenen ID haben (andere nennen keine
// Sitzung), in ihrer Reihenfolge.
func sessionRefsOf(r *http.Request) []sessionRef {
	var refs []sessionRef
	for _, v := range firstCookies(r, sessionCookie) {
		if validSessionID(v) {
			refs = append(refs, sessionRef{key: sessionKey(v), value: v})
		}
	}
	return refs
}

// liveSession: unter refs die erste, die im Schnappschuss eine lebende
// Sitzung nennt (ok), und die Schlüssel der toten Sitzungen, die davor
// gefunden wurden (Resume entfernt sie).
func liveSession(snap *model.Sessions, refs []sessionRef, cfg *config.Config, now int64) (ref sessionRef, s model.Session, ok bool, dead []string) {
	for _, ref := range refs {
		s, found := snap.Sessions[ref.key]
		switch {
		case !found:
		case sessionLive(s, cfg, now):
			return ref, s, true, dead
		default:
			dead = append(dead, ref.key)
		}
	}
	return sessionRef{}, model.Session{}, false, dead
}

func validSessionID(v string) bool {
	if len(v) != sessionIDText.EncodedLen(sessionIDBytes) {
		return false
	}
	for i := 0; i < len(v); i++ {
		switch b := v[i]; {
		case 'A' <= b && b <= 'Z', 'a' <= b && b <= 'z', '0' <= b && b <= '9', b == '-', b == '_':
		default:
			return false
		}
	}
	return true
}

func sessionKey(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// setSessionCookie setzt das Cookie der Sitzung für weitere 12 h.
func setSessionCookie(c *core.Core, w http.ResponseWriter, r *http.Request, value string) {
	putCookie(c, w, r, &http.Cookie{Name: sessionCookie, Value: value, MaxAge: sessionMaxIdle,
		Expires: c.Clock.Now().Add(sessionMaxIdle * time.Second)})
}
