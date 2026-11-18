package control

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"runtime"
	"sync"
	"time"
)

// maxSocketPath: Unix-Sockets fassen etwa 104–108 Bytes Pfad (je nach
// System); darüber lehnt das Programm lieber mit einer klaren Meldung ab.
const maxSocketPath = 100

// Socket ist ein Unix-Socket, der seine Datei nur dann löscht, wenn unter
// dem Pfad noch genau die Datei liegt, die beim Lauschen entstand. Ein
// Nachfolger, der den Pfad nach einem schnellen Neustart schon übernommen
// hat, verliert so nie seinen Socket – gelöscht wird nie bloß nach dem Pfad.
type Socket struct {
	*net.UnixListener
	path string
	own  fs.FileInfo // die eigene Datei, festgehalten gleich nach dem Lauschen

	once     sync.Once
	closeErr error
}

// Path ist der Pfad der Socket-Datei.
func (s *Socket) Path() string { return s.path }

// Close löscht die Socket-Datei, wenn sie noch die eigene ist, und schließt
// dann den Socket (in dieser Reihenfolge wie net selbst: Wer danach
// verbinden will, findet keine Datei mehr). Mehrfach rufbar.
func (s *Socket) Close() error {
	s.once.Do(func() {
		var errs []error
		if now, err := os.Lstat(s.path); err == nil && os.SameFile(s.own, now) {
			if err := os.Remove(s.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, fmt.Errorf("Socket-Datei %s nicht gelöscht: %w", s.path, err))
			}
		}
		errs = append(errs, s.UnixListener.Close())
		s.closeErr = errors.Join(errs...)
	})
	return s.closeErr
}

// ListenUnix lauscht auf einem Unix-Socket unter path (Rechte 0600 außer
// unter Windows). Eine liegen gebliebene Socket-Datei (nach einem harten
// Abbruch), an der niemand mehr lauscht, wird vorher gelöscht; ein Socket,
// an dem noch jemand lauscht oder dessen Probe anders scheitert, und alles,
// was kein Socket ist, bleiben unangetastet – dann ist das der Fehler. Auch
// für listen = "unix:<Pfad>".
//
// Zwischen dem Anlegen und dem Setzen der Rechte vergeht ein Augenblick, in
// dem der Socket die Rechte der umask hat; der Ordner (/run/fw-alarm) ist
// dafür nur für den Dienst zugänglich.
func ListenUnix(path string) (*Socket, error) {
	if path == "" {
		return nil, errors.New("kein Pfad für den Unix-Socket angegeben")
	}
	if len(path) > maxSocketPath {
		return nil, fmt.Errorf("Pfad des Unix-Sockets zu lang (%d Bytes, höchstens %d): %s", len(path), maxSocketPath, path)
	}
	if err := removeStale(path); err != nil {
		return nil, err
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("Unix-Socket %s: %w", path, err)
	}
	// Gelöscht wird nur über Socket.Close, nach dem Vergleich der Datei.
	ln.SetUnlinkOnClose(false)
	own, err := os.Lstat(path)
	if err != nil {
		// Ohne die eigene Datei zu kennen, wird nichts gelöscht; ein Rest wird
		// beim nächsten Start als verwaist erkannt.
		return nil, errors.Join(fmt.Errorf("Unix-Socket %s nicht prüfbar: %w", path, err), ln.Close())
	}
	// Unter Windows holt Go die Dateinummer erst beim ersten Vergleich – über
	// den Pfad; ein Vergleich gleich hier hält die der eigenen Datei fest.
	_ = os.SameFile(own, own) // nur für die Wirkung, das Ergebnis ist immer true
	s := &Socket{UnixListener: ln, path: path, own: own}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o600); err != nil {
			// Close löscht die Datei nur, wenn sie noch die eigene ist.
			return nil, errors.Join(fmt.Errorf("Rechte des Unix-Sockets %s: %w", path, err), s.Close())
		}
	}
	return s, nil
}

// removeStale löscht eine verwaiste Socket-Datei unter path.
func removeStale(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("Unix-Socket %s nicht prüfbar: %w", path, err)
	}
	if info.Mode().Type() != fs.ModeSocket {
		return fmt.Errorf("unter %s liegt kein Socket, sondern etwas anderes – nicht gelöscht", path)
	}
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err == nil {
		_ = conn.Close() // nur die Probe, ob dort jemand lauscht
		return fmt.Errorf("Unix-Socket %s ist in Benutzung (läuft dort schon ein Server?)", path)
	}
	if !staleSocket(err) {
		return fmt.Errorf("Unix-Socket %s: Probe gescheitert (%v) – nicht gelöscht (fehlen Rechte, oder hängt dort ein Server?)", path, err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("verwaister Unix-Socket %s nicht löschbar: %w", path, err)
	}
	return nil
}

// staleSocket: Sagt der Fehler eines Verbindungsversuchs, dass niemand
// lauscht (verweigert) oder die Datei inzwischen fort ist? Nur dann ist ein
// Socket verwaist; fehlende Rechte, keine Antwort und alles andere nicht.
func staleSocket(err error) bool {
	return errors.Is(err, refusedErrno) || errors.Is(err, fs.ErrNotExist)
}
