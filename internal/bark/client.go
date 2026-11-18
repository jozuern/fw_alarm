// Package bark ist die EINE Stelle, durch die jede Nachricht des Servers
// hinausgeht – Alarme, Entwarnung, Test-Pushs, Meldungen an den Admin.
//
// Hier sitzt auch die Probe-Sperre: Solange sie gilt, geht nichts an einen
// Key, der nicht auf ihrer Liste steht. Weil sie in PostMany durchgesetzt
// wird, kann kein Fehler weiter oben an ihr vorbei senden.
package bark

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"fwalarm/internal/clock"
	"fwalarm/internal/config"
)

// Job ist eine Nachricht an einen Key; Fields gehen als Formular hinaus.
type Job struct {
	Key    string
	Fields map[string]string
}

// Result ist der Ausgang eines Jobs (PHP: ok/http/err). HTTP 0 = keine Antwort.
type Result struct {
	OK   bool
	HTTP int
	Err  string
}

const (
	connectTimeout = 5 * time.Second
	requestTimeout = 10 * time.Second
	roundPause     = 500 * time.Millisecond
	defaultHost    = "https://api.day.app"

	// guardErr ist das Ergebnis eines Jobs, den die Probe-Sperre nicht hinauslässt.
	guardErr = "Probe-Sperre: nicht gesendet"
)

// Client sendet an Bark. Er hält keinen Zustand außer seiner Verbindung.
type Client struct {
	cfg  func() *config.Config
	clk  clock.Clock
	http *http.Client
	send func(ctx context.Context, host string, j Job) Result // ein Versuch; Tests tauschen es aus
}

// New baut den Client. cfg wird bei jedem Versand neu gefragt (Host und
// Probe-Sperre können sich zur Laufzeit ändern); clk liefert die Zeit für die
// Sperre und die Pause zwischen den Runden.
func New(cfg func() *config.Config, clk clock.Clock) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{Timeout: connectTimeout}).DialContext
	tr.TLSHandshakeTimeout = connectTimeout
	c := &Client{
		cfg: cfg,
		clk: clk,
		http: &http.Client{
			Transport: tr,
			Timeout:   requestTimeout,
			// Bewusst keine Weiterleitungen: es zählt nur der Code der ersten Antwort.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
	c.send = c.post
	return c
}

// Guard sagt, ob die Probe-Sperre jetzt gilt und bis wann.
func (c *Client) Guard() (until time.Time, active bool) {
	_, until, active = c.cfg().SendGuard(c.clk.Now())
	return until, active
}

// PostMany sendet alle Jobs gleichzeitig, in Runden (PHP: bark_post_many):
// jede Runde nur die noch offenen, höchstens tries Runden (mindestens eine),
// 500 ms Pause dazwischen. Nur HTTP 200 gilt als angenommen; ein 4xx wird nie
// wiederholt (Key falsch), alles andere schon. Ein toter oder langsamer Key
// hält die anderen nicht auf. Ergebnisse in der Reihenfolge der Jobs.
//
// Probe-Sperre: Sie wird einmal am Anfang gelesen. Gilt sie, bekommt jeder
// Job mit einem Key außerhalb ihrer Liste sofort das feste Ergebnis, ohne
// jede Netzwerk-Aktivität und ohne an einer Runde teilzunehmen.
//
// Bricht ctx ab, brechen offene Anfragen ab und ihre Ergebnisse sagen es.
func (c *Client) PostMany(ctx context.Context, jobs []Job, tries int) []Result {
	results := make([]Result, len(jobs))
	if len(jobs) == 0 {
		return results
	}
	cfg := c.cfg()
	open := make([]int, 0, len(jobs))
	if guardKeys, _, active := cfg.SendGuard(c.clk.Now()); active {
		allowed := make(map[string]bool, len(guardKeys))
		for _, k := range guardKeys {
			allowed[k] = true
		}
		for i, j := range jobs {
			if allowed[j.Key] {
				open = append(open, i)
			} else {
				results[i] = Result{OK: false, HTTP: 0, Err: guardErr}
			}
		}
	} else {
		for i := range jobs {
			open = append(open, i)
		}
	}
	host := strings.TrimRight(cfg.BarkHost, "/")
	if host == "" {
		host = defaultHost
	}

	for attempt := 1; attempt <= max(1, tries) && len(open) > 0; attempt++ {
		if attempt > 1 {
			select {
			case <-c.clk.After(roundPause):
			case <-ctx.Done():
			}
		}
		if ctx.Err() != nil {
			if attempt == 1 { // sonst bleibt das Ergebnis des letzten echten Versuchs stehen
				for _, i := range open {
					results[i] = Result{Err: "abgebrochen"}
				}
			}
			return results
		}
		var wg sync.WaitGroup
		for _, i := range open {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i] = c.runJob(ctx, host, jobs[i]) // jeder Index gehört genau einer Goroutine
			}()
		}
		wg.Wait()
		next := open[:0]
		for _, i := range open {
			r := results[i]
			if !r.OK && !(r.HTTP >= 400 && r.HTTP < 500) {
				next = append(next, i)
			}
		}
		open = next
	}
	return results
}

// runJob führt einen Versuch aus; eine Panik wird zum Fehlschlag mit
// neutralem Text (weder der Wert noch der Key stehen dort oder im Log).
func (c *Client) runJob(ctx context.Context, host string, j Job) (res Result) {
	defer func() {
		if recover() != nil {
			res = Result{OK: false, HTTP: 0, Err: "interner Fehler beim Senden"}
		}
	}()
	return c.send(ctx, host, j)
}

// post ist ein Versuch: POST <host>/<key>, Formular, Antwort ungelesen.
func (c *Client) post(ctx context.Context, host string, j Job) Result {
	form := url.Values{}
	for k, v := range j.Fields {
		form.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		host+"/"+url.PathEscape(j.Key), strings.NewReader(form.Encode()))
	if err != nil {
		return Result{Err: "Anfrage nicht baubar"}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return Result{Err: describe(ctx, err, j.Key)}
	}
	// Nie lesen: ein gestörter Server könnte mit einer riesigen Antwort den
	// Speicher füllen. Schließen genügt; es zählt nur der Code.
	_ = resp.Body.Close()
	return Result{OK: resp.StatusCode == http.StatusOK, HTTP: resp.StatusCode}
}

// describe macht aus einem Transportfehler einen Text ohne die Adresse (sie
// enthält den Key) und ohne den Key selbst.
func describe(ctx context.Context, err error, key string) string {
	if ctx.Err() != nil {
		return "abgebrochen"
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return "Zeitüberschreitung"
		}
		err = ue.Err
	}
	text := err.Error()
	if key != "" {
		text = strings.ReplaceAll(text, key, MaskKey(key))
		text = strings.ReplaceAll(text, url.PathEscape(key), MaskKey(key))
	}
	return text
}
