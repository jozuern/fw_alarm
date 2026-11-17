// Package webassets bettet die Oberfläche des Dashboards in das Programm
// fw-alarm ein. Die Datei liegt im Ordner der Oberfläche, weil go:embed nur
// Dateien darunter erreicht; server/deploy.sh spielt sie nicht ein.
package webassets

import "embed"

// FS enthält assets/ und manifest.json.
//
//go:embed assets manifest.json
var FS embed.FS
