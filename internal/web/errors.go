package web

import (
	"errors"
	"net/http"

	"fwalarm/internal/store"
)

// Antworten, wenn eine Änderung nicht gesichert wurde (Plan, "Texts this plan
// fixes"; PHP: LOCK_FAILED_MESSAGE in with_lock(), die Meldung bei
// unlesbarer Liste). Nur feste Sätze: Pfad, Ursache und Wortlaut des Fehlers
// stehen nie darin – die Antwort erreicht jeden Aufrufer, das Journal hat
// die Einzelheiten schon von der Ablage.
const (
	sentenceStorage   = "Fehler: Datenordner nicht beschreibbar (Rechte des Web-Benutzers auf dem Server prüfen)."
	sentenceLate      = "Fehler: Speichern dauert zu lange – bitte den angezeigten Stand prüfen."
	sentenceEmergency = "Fehler: Notbetrieb – die Empfängerliste ist nicht lesbar, Änderungen sind gerade nicht möglich. Ausweg: Sicherung zurückspielen (server/README.md)."
)

// StorageError beantwortet eine Anfrage, deren Änderung nicht gesichert
// wurde – jsonAPI: für dashboard_api.php als JSON, sonst als Text.
//
//   - store.ErrStorage: Status 500, code "storage", "Datenordner nicht
//     beschreibbar …" (wie PHP).
//   - store.ErrLate, store.ErrDropped: Status 500, code "storage",
//     "Speichern dauert zu lange …" – die Änderung kann bei ErrLate noch
//     gelten (Spec §7).
//   - store.ErrEmergency: wie PHP bei unlesbarer Empfängerliste – Status
//     200, ok=false mit dem Satz zum Notbetrieb, ohne code.
//   - alles andere (auch store.ErrInvalid, ein Fehler im Programm): nicht
//     gesichert – wie ErrStorage.
//
// Als Text immer Status 500 mit dem Satz (Klartext kennt kein ok=false).
func StorageError(w http.ResponseWriter, err error, jsonAPI bool) {
	status, code, sentence := http.StatusInternalServerError, "storage", sentenceStorage
	switch {
	case errors.Is(err, store.ErrEmergency):
		status, code, sentence = http.StatusOK, "", sentenceEmergency
	case errors.Is(err, store.ErrLate), errors.Is(err, store.ErrDropped):
		sentence = sentenceLate
	}
	if !jsonAPI {
		Text(w, http.StatusInternalServerError, sentence)
		return
	}
	if code == "" {
		JSON(w, status, Object{{"ok", false}, {"message", sentence}})
		return
	}
	JSON(w, status, Object{{"ok", false}, {"code", code}, {"message", sentence}})
}
