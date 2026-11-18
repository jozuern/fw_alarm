// Package logview ist der Verlauf, wie das Dashboard ihn sieht (PHP:
// log_event_types() und die Funktionen daneben in lib/common.php, die Aktion
// log_view in dashboard_api.php): EINE Tabelle aller Ereignisse – Kategorie
// für den Filter, Versand-Ereignis, nur für den Admin, und welche Felder in
// welchem Typ den Server verlassen. Was nicht in der Tabelle steht, geht nie
// hinaus – kein unbekanntes Ereignis und kein Feld, das sonst noch im Verlauf
// steht (die Einzelergebnisse eines Alarms mit Klartext-Keys, Telefonnummern,
// Tokens). Die Lese-Rolle bekommt weniger als der Admin.
//
// Ein NEUES Ereignis braucht eine Konstante in model (AllEvents), eine Zeile
// hier und einen Text in nas_dashboard/assets/app/logic.js (describeLogEntry)
// – sonst taucht es im Verlauf nicht auf.
package logview

import "fwalarm/internal/model"

// Field ist ein Feld, das der Browser bekommt: Name in der Antwort, Typ (str |
// int | bool), Pfad im Verlaufseintrag ("command.id" = Unterfeld). Fehlt der
// Wert oder ist er null: Optional → das Feld fehlt auch in der Antwort, sonst
// gilt Default (nil = "" / 0 / false).
type Field struct {
	Name, Type, Path string
	Optional         bool
	Default          any
}

// Event ist eine Zeile der Tabelle. Cat: Filter "Alarme / Empfänger / System"
// – alarm | keys | system | command (= alarm, wenn es um einen ALARM-Befehl
// geht, also einen Demo-Alarm über die Box, sonst system). Core:
// Versand-Ereignis (Filter alarm_core für "Letzter Alarm", ohne die Befehle
// der Demo-Alarme). Admin: nur für die Admin-Rolle – die Lese-Rolle bekommt
// es nie, und es fällt VOR dem Abzählen weg. Fields: in der Reihenfolge der
// Antwort.
type Event struct {
	Cat         string
	Core, Admin bool
	Fields      []Field
}

// Kategorien.
const (
	catAlarm   = "alarm"
	catKeys    = "keys"
	catSystem  = "system"
	catCommand = "command"
)

func str(name, path string) Field  { return Field{Name: name, Type: "str", Path: path} }
func num(name, path string) Field  { return Field{Name: name, Type: "int", Path: path} }
func flag(name, path string) Field { return Field{Name: name, Type: "bool", Path: path} }

// optional: nur, wenn der Eintrag das Feld trägt.
func optional(f Field) Field {
	f.Optional = true
	return f
}

// Felder, die viele Ereignisse haben. Keys stehen im Verlauf nur maskiert
// (PHP log_key_event, mask_bark_key) – key/old_key sind nie ein ganzer Key.
var (
	fieldBy    = str("by", "by")
	fieldLabel = str("label", "label")
	fieldKey   = str("key", "key")
)

// command: ein Befehls-Ereignis – die Felder stehen im Unterobjekt command;
// by kommt je nach Ereignis aus created_by oder cancelled_by.
func command(byField string, more ...Field) Event {
	return Event{Cat: catCommand, Fields: append([]Field{
		num("id", "command.id"),
		str("type", "command.type"),
		str("by", "command."+byField),
	}, more...)}
}

// minutes: Ausfall bzw. Rückkehr mit Dauer in Minuten.
func minutes() Event { return Event{Cat: catSystem, Fields: []Field{num("minutes", "minutes")}} }

// started: "Alarm/Entwarnung gestartet" – steht vor dem ersten Versuch auf
// der Platte. resend > 0: Nachsendung an so viele (zuvor nicht erreichte)
// Empfänger; demo fehlt bei Alt-Einträgen (dann erkennt die Oberfläche den
// Modus wie früher am Text).
func started() Event {
	return Event{Cat: catAlarm, Core: true, Fields: []Field{
		fieldBy, num("resend", "resend"), optional(flag("demo", "demo")),
	}}
}

// sent: das Ergebnis eines Versands – ohne die Einzelergebnisse (results mit
// Klartext-Keys), die nur im Verlauf stehen.
func sent() Event {
	return Event{Cat: catAlarm, Core: true, Fields: []Field{
		fieldBy, flag("ok", "ok"), str("message", "message"), num("resend", "resend"), optional(flag("demo", "demo")),
	}}
}

// Events ist die Tabelle (PHP: log_event_types), dazu die beiden Ereignisse
// des Go-Servers (Spec §7): state_rewound (System, nur Admin) und
// send_interrupted (Alarm, Versand-Ereignis). TestEventTableMatchesPHP
// vergleicht sie mit der Funktion in PHP.
var Events = map[string]Event{
	model.EvCreated:   command("created_by"),
	model.EvDelivered: command("created_by"),
	model.EvAcked: command("created_by",
		str("result", "command.ack_result"),
		str("message", "command.ack_message")),
	model.EvExpired: command("created_by"),
	// "message" nur, wenn der Abbruch einen Grund hat.
	model.EvCancelled: command("cancelled_by", optional(str("message", "command.cancel_reason"))),
	// Verspätete Quittung: Den Typ schreibt die Quittung oben mit (soweit noch
	// bekannt) – die eines Demo-Alarms gehört zu dessen Lebenslauf.
	model.EvLateAck: {Cat: catCommand, Fields: []Field{num("id", "id"), str("result", "result"), str("type", "type")}},

	model.EvDemoEnabled:  {Cat: catSystem, Fields: []Field{fieldBy, fieldLabel}},
	model.EvDemoDisabled: {Cat: catSystem, Fields: []Field{fieldBy, fieldLabel}},
	// Schreibt der Go-Server nicht mehr (Spec §7); alte Einträge bleiben
	// lesbar.
	model.EvDemoRepaired: {Cat: catSystem},

	model.EvKeyAdded:   {Cat: catKeys, Fields: []Field{fieldBy, fieldLabel, fieldKey}},
	model.EvKeyRemoved: {Cat: catKeys, Fields: []Field{fieldBy, fieldLabel, fieldKey}},
	model.EvKeyRenamed: {Cat: catKeys, Fields: []Field{fieldBy, fieldLabel, str("old_label", "old_label"), fieldKey}},
	// Beide Keys nur maskiert.
	model.EvKeyReplaced: {Cat: catKeys, Fields: []Field{fieldBy, fieldLabel, fieldKey, str("old_key", "old_key")}},
	// Die Telefonnummer selbst steht nie im Verlauf.
	model.EvKeyPhoneChanged: {Cat: catKeys, Fields: []Field{fieldBy, fieldLabel}},
	// Neue Kurzbefehl-Links – der Token selbst steht nie im Verlauf.
	model.EvKeyTokenRenewed:  {Cat: catKeys, Fields: []Field{fieldBy, fieldLabel}},
	model.EvKeyMuted:         {Cat: catKeys, Fields: []Field{fieldBy, fieldLabel, fieldKey, str("source", "source")}},
	model.EvKeyUnmuted:       {Cat: catKeys, Fields: []Field{fieldBy, fieldLabel, fieldKey, str("source", "source")}},
	model.EvKeyMuteVolume:    {Cat: catKeys, Fields: []Field{fieldBy, fieldLabel, num("volume", "volume")}},
	model.EvKeyMutePermanent: {Cat: catKeys, Fields: []Field{fieldBy, fieldLabel, flag("permanent", "permanent"), str("source", "source")}},
	model.EvKeyTest:          {Cat: catKeys, Fields: []Field{fieldBy, fieldLabel, flag("ok", "ok")}},

	// Welche Meldungen der Admin sich schicken lässt, geht die Lese-Rolle
	// nichts an (notify_list ist ihr ebenfalls verwehrt). key ist hier die
	// Einstellung ("enabled" = Art ein/aus), kein Bark-Key.
	model.EvNotifyChanged: {Cat: catSystem, Admin: true, Fields: []Field{
		fieldBy, fieldLabel, fieldKey, str("option", "option"), str("value", "value"),
	}},
	// Dashboard-Einstellung ("Automatisch wieder laut nach") – nur Admin.
	model.EvSettingChanged: {Cat: catSystem, Admin: true, Fields: []Field{fieldBy, fieldLabel, str("value", "value")}},
	model.EvBoxRestart:     {Cat: catSystem, Fields: []Field{str("reason", "reason")}},

	model.EvNasAlarmStarted:   started(),
	model.EvFalseAlarmStarted: started(),
	model.EvNasAlarm:          sent(),
	model.EvFalseAlarm:        sent(),
	// Ohne source war es ein Relaisalarm (Einträge von vor der
	// Unterscheidung); remote = Demo-Alarm über die Box.
	model.EvRelayAlarm: {Cat: catAlarm, Core: true, Fields: []Field{
		str("info", "info"), {Name: "source", Type: "str", Path: "source", Default: "relay"},
	}},

	model.EvBoxOffline:       minutes(),
	model.EvBoxRecovered:     minutes(),
	model.EvNasWebRecovered:  minutes(),
	model.EvWatcherSilent:    minutes(),
	model.EvWatcherRecovered: minutes(),
	model.EvNasWebFailed:     {Cat: catSystem, Fields: []Field{str("detail", "detail")}},

	// Neu im Go-Server. Der Stand sprang beim Start zurück (Spec §6.3) –
	// woraus (prev | restore | import), geschrieben von model.State.Rewind.
	model.EvStateRewound: {Cat: catSystem, Admin: true, Fields: []Field{fieldBy, str("source", "source")}},
	// Ein Versand lief beim Neustart, Ergebnis unbekannt (Spec §6.7): welcher
	// (nas_alarm | false_alarm), wann er begann, im Demo-Modus.
	model.EvSendInterrupted: {Cat: catAlarm, Core: true, Fields: []Field{
		str("kind", "kind"), num("started_at", "started_at"), optional(flag("demo", "demo")),
	}},
}
