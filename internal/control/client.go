package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"time"
)

// maxAnswer begrenzt, was der Client von einer Antwort liest.
const maxAnswer = 1 << 20

// Call schickt command (check, health, flush, reload, stop) über den Socket
// socketPath und wartet höchstens timeout auf die Antwort. output ist, was
// der Befehl ausgibt, ok sein Ergebnis. err: unbekannter Befehl, Server
// nicht erreichbar, keine Antwort in der Zeit oder eine unverständliche.
func Call(socketPath, command string, timeout time.Duration) (output string, ok bool, err error) {
	if !slices.Contains(commands, command) {
		return "", false, fmt.Errorf("unbekannter Befehl „%s“", command)
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
		DisableKeepAlives: true,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: timeout}
	resp, err := client.Post("http://fw-alarm/"+command, "text/plain", nil)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			if ue.Timeout() {
				return "", false, fmt.Errorf("Server antwortet nicht innerhalb von %v (%s)", timeout, socketPath)
			}
			err = ue.Err
		}
		return "", false, fmt.Errorf("Server nicht erreichbar über %s: %w", socketPath, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return "", false, fmt.Errorf("Antwort des Servers unvollständig: %w", err)
	}
	var a answer
	if err := json.Unmarshal(body, &a); err != nil {
		return "", false, fmt.Errorf("unverständliche Antwort des Servers (Status %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("Server lehnt ab (Status %d): %s", resp.StatusCode, a.Output)
	}
	return a.Output, a.OK, nil
}

// WaitGone wartet höchstens timeout, bis unter socketPath niemand mehr
// lauscht (nach stop: der Server hat seine Ablage geschlossen und den
// Socket gelöscht). true = niemand mehr da.
func WaitGone(socketPath string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("unix", socketPath, 500*time.Millisecond)
		if err != nil {
			return true
		}
		_ = conn.Close() // nur die Probe, ob dort noch jemand lauscht
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}
