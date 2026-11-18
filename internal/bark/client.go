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
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"runtime/debug"
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

	// guardErr ist das Ergebnis eines Jobs, den die Probe-Sperre nicht hinauslässt.
	guardErr = "Probe-Sperre: nicht gesendet"

	errNoConfig  = "Konfiguration fehlt"
	errNoHost    = "Bark-Host fehlt"
	errBadHost   = "Bark-Host ungültig"
	errBadField  = "Feldname nicht erlaubt"
	errPanicked  = "interner Fehler beim Senden"
	errCancelled = "abgebrochen"
	errTimeout   = "Zeitüberschreitung"
	errNotBuilt  = "Anfrage nicht baubar"
)

// Client sendet an Bark. Er hält keinen Zustand außer seiner Verbindung.
type Client struct {
	cfg  func() *config.Config
	clk  clock.Clock
	log  *slog.Logger
	http *http.Client
}

// New baut den Client. cfg wird bei jedem Versand neu gefragt (Host und
// Probe-Sperre können sich zur Laufzeit ändern); clk liefert die Zeit für die
// Sperre und die Pause zwischen den Runden; log nimmt die Zeilen über
// gescheiterte Versuche (nil = slog.Default()). Keys stehen dort nur maskiert.
func New(cfg func() *config.Config, clk clock.Clock, log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{Timeout: connectTimeout}).DialContext
	tr.TLSHandshakeTimeout = connectTimeout
	return &Client{
		cfg: cfg,
		clk: clk,
		log: log,
		http: &http.Client{
			Transport: tr,
			Timeout:   requestTimeout,
			// Bewusst keine Weiterleitungen: es zählt nur der Code der ersten Antwort.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Guard sagt, ob die Probe-Sperre jetzt gilt und bis wann.
func (c *Client) Guard() (until time.Time, active bool) {
	cfg := c.cfg()
	if cfg == nil {
		return time.Time{}, false
	}
	_, until, active = cfg.SendGuard(c.clk.Now())
	return until, active
}

// failAll liefert für alle Jobs dasselbe Fehlergebnis (ohne eine Anfrage).
func failAll(n int, text string) []Result {
	rs := make([]Result, n)
	for i := range rs {
		rs[i] = Result{Err: text}
	}
	return rs
}

// forbiddenField sagt, ob ein Feldname ein Weg wäre, den Pfad-Key zu umgehen:
// Bark nimmt Empfänger auch über device_key/device_keys im Formular an.
func forbiddenField(fields map[string]string) bool {
	for name := range fields {
		if strings.EqualFold(name, "device_key") || strings.EqualFold(name, "device_keys") {
			return true
		}
	}
	return false
}

// PostMany sendet alle Jobs gleichzeitig, in Runden (PHP: bark_post_many):
// jede Runde nur die noch offenen, höchstens tries Runden (mindestens eine),
// 500 ms Pause dazwischen. Nur HTTP 200 gilt als angenommen; ein 4xx wird nie
// wiederholt (Key falsch), alles andere schon. Ein toter oder langsamer Key
// hält die anderen nicht auf. Ergebnisse in der Reihenfolge der Jobs.
//
// Probe-Sperre: Sie wird genau einmal am Anfang gelesen. Gilt sie, bekommt
// jeder Job mit einem Key außerhalb ihrer Liste sofort das feste Ergebnis,
// ohne jede Netzwerk-Aktivität und ohne an einer Runde teilzunehmen. Eine
// Sperre, die während des Aufrufs endet, hält ihre Jobs trotzdem bis zum Ende
// zurück; eine, die währenddessen beginnt, gilt erst für den nächsten Aufruf.
//
// Nie gesendet wird außerdem: ohne Konfiguration, ohne brauchbaren Host (leer
// oder ohne http:// bzw. https://) – es gibt keinen stillen Ersatzserver – und
// ein Job mit dem Feldnamen device_key/device_keys (Umweg um den Pfad-Key),
// mit oder ohne Sperre. Bei einem unbrauchbaren Host behalten von der Sperre
// zurückgehaltene Jobs ihr Sperr-Ergebnis.
//
// Bricht ctx ab, brechen offene Anfragen ab und ihre Ergebnisse sagen es.
func (c *Client) PostMany(ctx context.Context, jobs []Job, tries int) []Result {
	cfg := c.cfg()
	if cfg == nil {
		return failAll(len(jobs), errNoConfig)
	}
	results := make([]Result, len(jobs))
	open := make([]int, 0, len(jobs))
	guardKeys, _, locked := cfg.SendGuard(c.clk.Now())
	allowed := make(map[string]bool, len(guardKeys))
	for _, k := range guardKeys {
		allowed[k] = true
	}
	held := 0
	for i, j := range jobs {
		switch {
		case locked && !allowed[j.Key]:
			results[i] = Result{Err: guardErr}
			held++
		case forbiddenField(j.Fields):
			results[i] = Result{Err: errBadField}
			c.log.Warn("Bark-Versand verweigert", "key", MaskKey(j.Key), "grund", errBadField)
		default:
			open = append(open, i)
		}
	}
	if held > 0 {
		c.log.Warn("Probe-Sperre hält Bark-Nachrichten zurück", "anzahl", held)
	}

	host := strings.TrimRight(strings.TrimSpace(cfg.BarkHost), "/")
	hostErr := ""
	switch lower := strings.ToLower(host); {
	case host == "":
		hostErr = errNoHost
	case !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://"):
		hostErr = errBadHost
	}
	if hostErr != "" {
		for _, i := range open {
			results[i] = Result{Err: hostErr}
		}
		if len(open) > 0 {
			c.log.Error("Bark-Versand ohne brauchbaren Host", "grund", hostErr, "anzahl", len(open))
		}
		return results
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
					results[i] = Result{Err: errCancelled}
				}
			}
			return results
		}
		var wg sync.WaitGroup
		for _, i := range open {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Jeder Index gehört genau einer Goroutine.
				results[i] = guarded(c.log, func() Result { return c.post(ctx, host, jobs[i]) })
			}()
		}
		wg.Wait()
		next := open[:0]
		for _, i := range open {
			r := results[i]
			if !r.OK {
				c.log.Warn("Bark-Versand gescheitert", "key", MaskKey(jobs[i].Key),
					"runde", attempt, "http", r.HTTP, "fehler", r.Err)
			}
			if !r.OK && !(r.HTTP >= 400 && r.HTTP < 500) {
				next = append(next, i)
			}
		}
		open = next
	}
	return results
}

// guarded führt fn aus; eine Panik wird zum Fehlschlag mit neutralem Text.
// Ins Journal kommt – wie bei bg.Go – nur der Text eines Laufzeitfehlers der
// Sprache, sonst die Art des Werts (ein selbst ausgelöster Wert könnte einen
// Key tragen), dazu der Stapel. Weder Wert noch Key stehen im Ergebnis.
func guarded(log *slog.Logger, fn func() Result) (res Result) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		kind := fmt.Sprintf("%T", r)
		if rerr, ok := r.(runtime.Error); ok {
			kind = rerr.Error()
		}
		log.Error("Laufzeitfehler beim Bark-Versand abgefangen", "art", kind, "stapel", string(debug.Stack()))
		res = Result{Err: errPanicked}
	}()
	return fn()
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
		// Der Fehlertext enthielte die Adresse und damit den Key – er wird bewusst verworfen.
		return Result{Err: errNotBuilt}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return Result{Err: describe(ctx, err, j.Key)}
	}
	// Nie lesen: ein gestörter Server könnte mit einer riesigen oder endlosen
	// Antwort Speicher und Zeit binden. Schließen genügt; es zählt nur der Code.
	_ = resp.Body.Close()
	return Result{OK: resp.StatusCode == http.StatusOK, HTTP: resp.StatusCode}
}

// describe macht aus einem Transportfehler einen Text ohne die Adresse (sie
// enthält den Key) und ohne den Key selbst.
func describe(ctx context.Context, err error, key string) string {
	if ctx.Err() != nil {
		return errCancelled
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return errTimeout
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
