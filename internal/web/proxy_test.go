package web

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fwalarm/internal/config"
	"fwalarm/internal/core"
)

// cfgCore: ein Core, dessen geltende Konfiguration cfg ist.
func cfgCore(cfg *config.Config) *core.Core {
	return &core.Core{Cfg: func() *config.Config { return cfg }, Log: slog.New(slog.DiscardHandler), Version: "test"}
}

// from baut eine Anfrage von remote (host:port) mit diesen Kopfzeilen
// ("Name: Wert"; derselbe Name mehrfach = mehrere Zeilen).
func from(remote string, header ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	for _, h := range header {
		name, value, _ := strings.Cut(h, ":")
		r.Header.Add(name, strings.TrimSpace(value))
	}
	return r
}

// overUnix: dieselbe Anfrage, als käme sie über den Unix-Socket des Servers.
func overUnix(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), viaUnixKey{}, true))
}

// Jeder Fall nennt die Regel, die er festhält (PHP: login_client_ip,
// request_from_trusted_proxy, packed_ip).
func TestClientIP(t *testing.T) {
	xff := "X-Forwarded-For"
	cases := []struct {
		rule    string
		proxies []string
		header  string // proxy_client_ip_header
		r       *http.Request
		want    string
	}{
		{"ohne vertraute Proxys zählt allein der Absender – seine Kopfzeilen nicht",
			nil, xff, from("203.0.113.5:4711", "X-Forwarded-For: 198.51.100.1"), "203.0.113.5"},
		{"ein nicht vertrauter Absender sucht sich seine Adresse nicht aus (auch nicht über CF-Connecting-IP)",
			nil, "CF-Connecting-IP", from("203.0.113.5:4711", "CF-Connecting-IP: 198.51.100.1"), "203.0.113.5"},
		{"vertrauter Proxy: bei X-Forwarded-For zählt der LETZTE Eintrag (den hängt der Proxy selbst an)",
			[]string{"127.0.0.1"}, xff, from("127.0.0.1:5000", "X-Forwarded-For: 198.51.100.1, 203.0.113.9"), "203.0.113.9"},
		{"vertrauter Proxy: über mehrere Kopfzeilen hinweg zählt der letzte Eintrag",
			[]string{"127.0.0.1"}, xff, from("127.0.0.1:5000", "X-Forwarded-For: 198.51.100.1", "X-Forwarded-For: 203.0.113.9"), "203.0.113.9"},
		{"vertrauter Proxy: der Eintrag wird ohne Leerraum außen übernommen, sonst wie gesendet",
			[]string{"127.0.0.1"}, xff, from("127.0.0.1:5000", "X-Forwarded-For: 198.51.100.1,   2001:DB8::7  "), "2001:DB8::7"},
		{"vertrauter Proxy: ein ungültiger Eintrag zählt nicht – dann der Absender",
			[]string{"127.0.0.1"}, xff, from("127.0.0.1:5000", "X-Forwarded-For: 198.51.100.1, nicht-ip"), "127.0.0.1"},
		{"vertrauter Proxy: ein leerer letzter Eintrag zählt nicht – dann der Absender",
			[]string{"127.0.0.1"}, xff, from("127.0.0.1:5000", "X-Forwarded-For: 198.51.100.1,"), "127.0.0.1"},
		{"vertrauter Proxy: IPv4 mit führender Null ist ungültig – dann der Absender",
			[]string{"127.0.0.1"}, xff, from("127.0.0.1:5000", "X-Forwarded-For: 198.51.100.01"), "127.0.0.1"},
		{"vertrauter Proxy: eine Adresse mit Zone ist ungültig – dann der Absender",
			[]string{"127.0.0.1"}, xff, from("127.0.0.1:5000", "X-Forwarded-For: fe80::1%eth0"), "127.0.0.1"},
		{"vertrauter Proxy ohne die Kopfzeile: der Absender",
			[]string{"127.0.0.1"}, xff, from("127.0.0.1:5000"), "127.0.0.1"},
		{"trusted_proxies gesetzt, der Absender ist aber keiner davon: Kopfzeilen zählen nicht",
			[]string{"10.0.0.1", "::1"}, xff, from("127.0.0.1:5000", "X-Forwarded-For: 198.51.100.1"), "127.0.0.1"},
		{"ohne proxy_client_ip_header zählt auch beim vertrauten Proxy der Absender",
			[]string{"127.0.0.1"}, "", from("127.0.0.1:5000", "X-Forwarded-For: 198.51.100.1"), "127.0.0.1"},
		{"ein unbrauchbarer Name der Kopfzeile zählt nicht (nur Buchstaben, Ziffern, - und _)",
			[]string{"127.0.0.1"}, "X Forwarded For", from("127.0.0.1:5000", "X-Forwarded-For: 198.51.100.1"), "127.0.0.1"},
		{"der Name der Kopfzeile: _ wie -, Groß/klein egal, Leerraum außen egal (wie PHPs HTTP_…)",
			[]string{"127.0.0.1"}, " cf_connecting_ip ", from("127.0.0.1:5000", "CF-Connecting-IP: 198.51.100.1"), "198.51.100.1"},
		{"eine Kopfzeile mit _ im Namen ist nicht die mit - (Nginx verwarf sie, net/http hält sie getrennt)",
			[]string{"127.0.0.1"}, xff, from("127.0.0.1:5000", "X_Forwarded_For: 198.51.100.1"), "127.0.0.1"},
		{"vertraute Proxys: IPv4-in-IPv6 zählt wie die IPv4 (Eintrag gemappt)",
			[]string{"::ffff:127.0.0.1"}, xff, from("127.0.0.1:5000", "X-Forwarded-For: 198.51.100.1"), "198.51.100.1"},
		{"vertraute Proxys: IPv4-in-IPv6 zählt wie die IPv4 (Absender gemappt)",
			[]string{"127.0.0.1"}, xff, from("[::ffff:127.0.0.1]:5000", "X-Forwarded-For: 198.51.100.1"), "198.51.100.1"},
		{"vertraute Proxys: Leerraum um den Eintrag zählt nicht, IPv6 in anderer Schreibweise ist dieselbe",
			[]string{" 0:0:0:0:0:0:0:1 "}, xff, from("[::1]:5000", "X-Forwarded-For: 198.51.100.1"), "198.51.100.1"},
		{"vertraute Proxys: ein unbrauchbarer Eintrag passt auf nichts",
			[]string{"localhost", ""}, xff, from("127.0.0.1:5000", "X-Forwarded-For: 198.51.100.1"), "127.0.0.1"},
		{"der Absender ohne Port-Angabe (kein host:port): unknown",
			[]string{"127.0.0.1"}, xff, from("kaputt", "X-Forwarded-For: 198.51.100.1"), "unknown"},
		{"der Absender als IPv6: ohne Klammern und Port",
			nil, xff, from("[2001:db8::5]:5000"), "2001:db8::5"},
		{"über den Unix-Socket des Servers zählen die Kopfzeilen auch ohne trusted_proxies",
			nil, xff, overUnix(from("@", "X-Forwarded-For: 198.51.100.1, 203.0.113.9")), "203.0.113.9"},
		{"über den Unix-Socket ohne brauchbare Kopfzeile: unknown (es gibt keine Absender-Adresse)",
			nil, xff, overUnix(from("@")), "unknown"},
	}
	for _, c := range cases {
		cfg := &config.Config{TrustedProxies: c.proxies, ProxyClientIPHeader: c.header}
		if got := ClientIP(cfgCore(cfg), c.r); got != c.want {
			t.Errorf("%s: %q, erwartet %q", c.rule, got, c.want)
		}
	}
}

// FromTunnel und IsHTTPS folgen denselben Regeln (PHP:
// request_from_trusted_proxy, request_is_https).
func TestIsHTTPS(t *testing.T) {
	trusted := &config.Config{TrustedProxies: []string{"127.0.0.1"}}
	none := &config.Config{}
	tlsReq := from("203.0.113.5:4711")
	tlsReq.TLS = &tls.ConnectionState{}
	cases := []struct {
		rule        string
		cfg         *config.Config
		r           *http.Request
		tunnel, tls bool
	}{
		{"ein nicht vertrauter Absender: X-Forwarded-Proto zählt nicht",
			none, from("127.0.0.1:5000", "X-Forwarded-Proto: https"), false, false},
		{"ein vertrauter Proxy mit X-Forwarded-Proto: https",
			trusted, from("127.0.0.1:5000", "X-Forwarded-Proto: https"), true, true},
		{"X-Forwarded-Proto: Groß/klein und Leerraum egal",
			trusted, from("127.0.0.1:5000", "X-Forwarded-Proto:  HTTPS "), true, true},
		{"X-Forwarded-Proto: http",
			trusted, from("127.0.0.1:5000", "X-Forwarded-Proto: http"), true, false},
		{"X-Forwarded-Proto zweimal: zusammengefasst ist es kein https (wie Nginx an PHP)",
			trusted, from("127.0.0.1:5000", "X-Forwarded-Proto: https", "X-Forwarded-Proto: https"), true, false},
		{"ein vertrauter Proxy ohne X-Forwarded-Proto",
			trusted, from("127.0.0.1:5000"), true, false},
		{"trusted_proxies gesetzt, Absender ist keiner davon",
			trusted, from("127.0.0.2:5000", "X-Forwarded-Proto: https"), false, false},
		{"über den Unix-Socket zählt X-Forwarded-Proto",
			none, overUnix(from("@", "X-Forwarded-Proto: https")), true, true},
		{"über den Unix-Socket ohne X-Forwarded-Proto",
			none, overUnix(from("@")), true, false},
		{"eine direkte TLS-Verbindung ist HTTPS, auch ohne Proxy",
			none, tlsReq, false, true},
	}
	for _, c := range cases {
		core := cfgCore(c.cfg)
		if got := FromTunnel(core, c.r); got != c.tunnel {
			t.Errorf("%s: FromTunnel %v", c.rule, got)
		}
		if got := IsHTTPS(core, c.r); got != c.tls {
			t.Errorf("%s: IsHTTPS %v", c.rule, got)
		}
	}
}

// Verbindungen über den Unix-Socket des Servers gelten als vom Tunnel
// (Spec §7, §10) – über TCP zählt allein trusted_proxies.
func TestUnixListenerCountsAsTunnel(t *testing.T) {
	cfg := &config.Config{ProxyClientIPHeader: "X-Forwarded-For"} // keine vertrauten Proxys
	c := cfgCore(cfg)
	rt := NewRouter(c)
	rt.Handle("/wer", func(w http.ResponseWriter, r *http.Request) {
		Text(w, 200, fmt.Sprintf("tunnel=%v ip=%s https=%v", FromTunnel(c, r), ClientIP(c, r), IsHTTPS(c, r)))
	})
	get := func(client *http.Client, base string) string {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, base+"/wer", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Forwarded-For", "198.51.100.1, 203.0.113.9")
		req.Header.Set("X-Forwarded-Proto", "https")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}

	addr := serveTCP(t, rt)
	if got := get(http.DefaultClient, "http://"+addr); got != "tunnel=false ip=127.0.0.1 https=false" {
		t.Errorf("über TCP: %s", got)
	}

	dir, err := os.MkdirTemp("", "fwweb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "h.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	serveOn(t, rt, ln, true)
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}
	defer client.CloseIdleConnections()
	if got := get(client, "http://fw-alarm"); got != "tunnel=true ip=203.0.113.9 https=true" {
		t.Errorf("über den Unix-Socket: %s", got)
	}
}
