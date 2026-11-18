package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
)

// SelfTest ist das Ergebnis des Speicher-Selbsttests (Spec §7). Step:
// "ok", "write", "read", "delete" (selftest.tmp schreiben, zurücklesen,
// löschen) oder "save" (das letzte Sichern eines Dokuments, der Not-Kopie
// oder des Verlaufs scheiterte).
type SelfTest struct {
	OK   bool
	Step string
}

// SaveHealth: Gelang das letzte Sichern je Datei? (true = gelungen oder
// noch keins gescheitert; History auch false, wenn der Verlauf beim Start
// nicht lesbar war.) Volatile ist runtime.json.
type SaveHealth struct{ Durable, Volatile, Sessions, History, Rescue bool }

// SaveHealth liefert das Ergebnis des letzten Sicherns je Datei – aus dem
// Speicher, wartet auf nichts.
func (s *Store) SaveHealth() SaveHealth {
	return SaveHealth{
		Durable:  s.saved.durable.Load(),
		Volatile: s.saved.volatile.Load(),
		Sessions: s.saved.sessions.Load(),
		History:  s.saved.history.Load(),
		Rescue:   s.saved.rescue.Load(),
	}
}

// SelfTest prüft, ob der Datenordner beschreibbar ist: selftest.tmp
// schreiben, zurücklesen und vergleichen, löschen – der erste Schritt, der
// scheitert, ist das Ergebnis. Gelingen alle, zählt das letzte Sichern
// (SaveHealth): scheiterte eines, "save". Läuft im Prüf-Lauf, nie in einer
// Anfrage (es fasst die Platte an); zwei zugleich warten aufeinander.
func (s *Store) SelfTest() SelfTest {
	s.selfMu.Lock()
	defer s.selfMu.Unlock()
	name := s.path(fileSelfTest)
	var nonce [8]byte
	_, _ = rand.Read(nonce[:]) // crypto/rand.Read scheitert nie (Go ≥ 1.24 bricht sonst das Programm ab)
	data := []byte("fw-alarm Selbsttest " + hex.EncodeToString(nonce[:]) + "\n")
	if err := s.fsys.WriteFileSync(name, data); err != nil {
		s.log.Warn("Selbsttest: Schreiben scheitert", "err", err)
		s.dropTmp(name)
		return SelfTest{Step: "write"}
	}
	back, err := s.fsys.ReadFile(name)
	if err == nil && !bytes.Equal(back, data) {
		err = errMismatch
	}
	if err != nil {
		s.log.Warn("Selbsttest: Zurücklesen scheitert", "err", err)
		s.dropTmp(name)
		return SelfTest{Step: "read"}
	}
	if err := s.fsys.Remove(name); err != nil {
		s.log.Warn("Selbsttest: Löschen scheitert", "err", err)
		return SelfTest{Step: "delete"}
	}
	if h := s.SaveHealth(); !h.Durable || !h.Volatile || !h.Sessions || !h.History || !h.Rescue {
		return SelfTest{Step: "save"}
	}
	return SelfTest{OK: true, Step: "ok"}
}

// Resave sichert noch einmal, was zuletzt nicht gesichert werden konnte –
// den unveränderten Zustand, die flüchtigen Daten, die Sitzungen, die
// Not-Kopie, den Verlauf (der dafür history.jsonl neu liest) –, damit ein
// einzelnes gescheitertes Sichern nicht stundenlang wie eine kaputte Platte
// aussieht. Der Prüf-Lauf ruft es, solange SelfTest "save" meldet. Was
// wieder scheitert, steht im Journal und weiter in SaveHealth.
func (s *Store) Resave(ctx context.Context) {
	h := s.SaveHealth()
	if !h.Durable && !s.emergency {
		if _, err := s.change(ctx, nil, modeForce); err != nil {
			s.log.Warn("feste Daten nicht nachgesichert", "err", err)
		}
	}
	var again []*writer
	if !h.Volatile {
		s.markRuntimeDirty()
		again = append(again, s.rtW)
	}
	if !h.Sessions {
		s.markSessionsDirty()
		again = append(again, s.ssW)
	}
	if !h.History {
		s.hist.setResync()
		again = append(again, s.histW)
	}
	if !h.Rescue && !s.emergency {
		s.rescueVer.Add(1)
		again = append(again, s.rescueW)
	}
	for _, w := range again {
		if err := s.flushWriter(ctx, w); err != nil {
			s.log.Warn("nicht nachgesichert", "datei", w.name, "err", err)
		}
	}
}
