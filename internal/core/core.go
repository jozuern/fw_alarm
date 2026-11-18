// Package core bündelt, was jede Anfrage und jede Arbeit nebenher braucht:
// die geltende Konfiguration, die Ablage, den Bark-Client, die Uhr, die
// Gruppe der Hintergrundarbeit, das Journal und den Stand des Programms.
// Gebaut wird der Core einmal beim Start (internal/app); danach ändert sich
// keines seiner Felder.
package core

import (
	"log/slog"

	"fwalarm/internal/bark"
	"fwalarm/internal/bg"
	"fwalarm/internal/clock"
	"fwalarm/internal/config"
	"fwalarm/internal/store"
)

// Core ist das Bündel für alle Handler.
type Core struct {
	Cfg     func() *config.Config // aktueller Stand aus dem Speicher
	CfgOK   func() bool           // false: config.toml fehlerhaft, die letzte gute gilt
	Store   *store.Store
	Bark    *bark.Client
	Clock   clock.Clock
	Bg      *bg.Group
	Log     *slog.Logger
	Version string
}
