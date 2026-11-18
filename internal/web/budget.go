package web

import (
	"context"
	"fmt"
	"time"
)

// BudgetKind ist die Art einer Anfrage, nach der sich die Frist für ihre
// festen Änderungen richtet (Spec §6.2). Die Frist gilt je Anfrage, nicht je
// Änderung, und schließt das Warten in der Schlange ein: Alle festen
// Änderungen einer Anfrage laufen mit demselben Kontext aus Budget.
type BudgetKind int

const (
	// BudgetBox: Aufrufe der Box (Poll, Quittung, api/keys.php) – 1,5 s; die
	// Box gibt nach 4 s auf, die Brücke nach 3 s.
	BudgetBox BudgetKind = iota + 1
	// BudgetLink: der Kurzbefehl-Link – 2 s; alte Links laufen über die
	// Brücke, die nach 3,5 s aufgibt.
	BudgetLink
	// BudgetUI: die Oberfläche (dashboard_api.php) – 5 s. Dieselbe Frist hat
	// eine feste Änderung neben einer Anfrage (nach der Antwort, im
	// Prüf-Lauf): Keine wartet ohne Grenze.
	BudgetUI
)

// Budget liefert den Kontext mit der Frist dieser Art. Er hängt bewusst
// NICHT am Kontext der Anfrage: Bricht der Aufrufer die Verbindung ab, läuft
// eine begonnene Änderung trotzdem zu Ende (ein abgebrochener Poll darf
// "ausgeliefert" nicht mitten im Sichern verwerfen). Der Aufrufer ruft
// cancel, sobald er fertig ist. Eine unbekannte Art ist ein Fehler im
// Programm.
func Budget(k BudgetKind) (context.Context, context.CancelFunc) {
	var d time.Duration
	switch k {
	case BudgetBox:
		d = 1500 * time.Millisecond
	case BudgetLink:
		d = 2 * time.Second
	case BudgetUI:
		d = 5 * time.Second
	default:
		panic(fmt.Sprintf("web: unbekannte Art einer Frist (%d)", int(k)))
	}
	return context.WithTimeout(context.Background(), d)
}
