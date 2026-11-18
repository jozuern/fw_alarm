package web

import (
	"net"
	"net/http"
	"net/netip"
	"strings"

	"fwalarm/internal/config"
	"fwalarm/internal/core"
)

// Hinter dem Tunnel (Spec §10, PHP: der Abschnitt "HINTER EINEM
// REVERSE-PROXY / TUNNEL" in common.php): Der Server sieht als Absender
// jeder Anfrage cloudflared und kein HTTPS. Kopfzeilen, die der Proxy
// setzt (X-Forwarded-Proto, die Kopfzeile mit der Adresse des Besuchers),
// zählen deshalb – aber NUR, wenn die Anfrage wirklich vom Tunnel kommt
// (FromTunnel). Sonst suchte sich jeder Besucher seine Adresse aus: die
// Bremse umgehen oder gezielt die Adresse des Admins sperren.

// FromTunnel sagt, ob die Anfrage vom Tunnel kommt: über den Unix-Socket
// des Servers (nur cloudflared darf ihn öffnen; Spec §7, §10) oder von
// einer Adresse aus trusted_proxies (PHP: request_from_trusted_proxy).
func FromTunnel(c *core.Core, r *http.Request) bool {
	return ViaUnixSocket(r) || fromTrustedProxy(c.Cfg(), r)
}

// fromTrustedProxy: Der Absender ist einer der vertrauten Proxys –
// verglichen als Adresse (IPv4-in-IPv6 wie die IPv4, Leerraum außen egal);
// ein unbrauchbarer Eintrag passt auf nichts.
func fromTrustedProxy(cfg *config.Config, r *http.Request) bool {
	remote, ok := packedIP(remoteHost(r))
	if !ok {
		return false
	}
	for _, p := range cfg.TrustedProxies {
		if a, ok := packedIP(p); ok && a == remote {
			return true
		}
	}
	return false
}

// IsHTTPS sagt, ob die Anfrage beim Besucher per HTTPS ankam (PHP:
// request_is_https): eine direkte TLS-Verbindung, oder X-Forwarded-Proto
// "https" vom Tunnel. Mehrere Zeilen werden wie von Nginx an PHP mit ", "
// zusammengefasst – sie sind dann kein "https".
func IsHTTPS(c *core.Core, r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !FromTunnel(c, r) {
		return false
	}
	proto := strings.Join(r.Header.Values("X-Forwarded-Proto"), ", ")
	return strings.ToLower(config.TrimPHP(proto)) == "https"
}

// ClientIP liefert die Adresse des Besuchers (PHP: login_client_ip): den
// Absender – außer die Anfrage kommt vom Tunnel, dann den LETZTEN Eintrag
// der Kopfzeile proxy_client_ip_header (den hat der Proxy selbst angehängt,
// alles davor kann der Besucher frei erfinden; mehrere Zeilen zählen wie
// eine). Ist der Eintrag keine gültige Adresse, gilt der Absender. Der
// Eintrag wird ohne Leerraum außen, sonst wie gesendet geliefert –
// vereinheitlicht wird erst für die Bremse (ThrottleKey). Welcher Name der
// Kopfzeile brauchbar ist, sagt config.Config.ClientIPHeader (dieselbe
// Regel prüft die Konfiguration für einen Unix-Socket).
func ClientIP(c *core.Core, r *http.Request) string {
	remote := remoteHost(r)
	if !FromTunnel(c, r) {
		return remote
	}
	name, ok := c.Cfg().ClientIPHeader()
	if !ok {
		return remote
	}
	parts := strings.Split(strings.Join(r.Header.Values(name), ","), ",")
	candidate := config.TrimPHP(parts[len(parts)-1])
	if _, ok := packedIP(candidate); ok {
		return candidate
	}
	return remote
}

// remoteHost ist der Absender ohne Port (PHP: REMOTE_ADDR). Über den
// Unix-Socket gibt es keine Absender-Adresse: "unknown" (PHPs Ersatz, wenn
// REMOTE_ADDR fehlt).
func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || host == "" {
		return "unknown"
	}
	return host
}

// packedIP liefert die Adresse in vergleichbarer Form (PHP: packed_ip):
// Leerraum außen zählt nicht, IPv4-in-IPv6 (::ffff:a.b.c.d) wird die IPv4.
// false: keine gültige Adresse – auch eine mit Zone (fe80::1%eth0) nicht,
// die nimmt PHPs inet_pton nicht an.
func packedIP(s string) (netip.Addr, bool) {
	a, err := netip.ParseAddr(config.TrimPHP(s))
	if err != nil || a.Zone() != "" {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}
