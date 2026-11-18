package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// KeepCopies legt von nun an nach jeder Fassung von config.toml, die in
// Kraft gesetzt wird (nie aus einer erst einmal gesehenen Lesung), eine
// Kopie in dir – und gleich jetzt eine der geltenden, wenn
// sie aus config.toml stammt (eine aus der Kopie wird nie zurückkopiert).
// Der Fehler sagt, ob diese erste Kopie scheiterte; spätere Fehler stehen
// nur im Journal.
func (p *Provider) KeepCopies(dir string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.copyDir = dir
	if p.fromCopy {
		return nil
	}
	return p.writeCopy()
}

// writeCopy schreibt die geltende Fassung als Kopie: erst in eine
// Zwischendatei (0600, auf die Platte gesichert), dann in einem Schritt an
// ihren Platz – eine halbe Kopie gibt es so nie (unter mu).
func (p *Provider) writeCopy() error {
	if p.copyDir == "" || p.fromCopy {
		return nil
	}
	target := filepath.Join(p.copyDir, LastGoodName)
	tmp := target + ".tmp"
	if err := writeSynced(tmp, p.curBytes); err != nil {
		_ = os.Remove(tmp) // Aufräumen nach einem Fehler; scheitert es, bleibt nur die Zwischendatei
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp) // wie oben
		return err
	}
	return syncDir(p.copyDir)
}

// syncDir sichert den Ordner nach dem Umbenennen auf die Platte (wie die
// Ablage); unter Windows gibt es dafür nichts zu tun.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// writeSynced legt name neu an (Rechte 0600), schreibt data und sichert es.
func writeSynced(name string, data []byte) error {
	if err := os.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	return errors.Join(werr, serr, cerr)
}
