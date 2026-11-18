package dashapi

import (
	"net/http"

	"fwalarm/internal/config"
	"fwalarm/internal/model"
	"fwalarm/internal/mutes"
	"fwalarm/internal/web"
)

// Die Empfängerliste im Dashboard pflegen (PHP: dashboard_api.php, Zweige
// keys_list bis keys_new_mute_token). Die Box übernimmt Änderungen von
// selbst: Ihr Poll meldet die Listen-Version, weicht sie ab, holt sie die
// Liste über api/keys.php. Deshalb erhöht genau das die Version, was die Box
// kennt (Key, Stumm-Zustand, Stumm-Lautstärke, Demo-Modus, Anlegen und
// Löschen) – Name, Telefonnummer und Kurzbefehl-Token nicht.

func init() {
	Register("keys_list", Action{Handle: handleKeysList})
	Register("keys_add", Action{Handle: handleKeysAdd})
	Register("keys_delete", Action{Handle: handleKeysDelete})
	Register("keys_rename", Action{Handle: handleKeysRename})
	Register("keys_replace_key", Action{Handle: handleKeysReplaceKey})
	Register("keys_set_phone", Action{Handle: handleKeysSetPhone})
	Register("keys_new_mute_token", Action{Handle: handleKeysNewMuteToken})
}

// keysCorruptMessage: keys_list im Notbetrieb (Plan "Texts this plan fixes";
// die Oberfläche kennt code=keys_corrupt). Die Liste ist dann nicht "leer":
// Die Box alarmiert weiter mit ihrer gespeicherten Liste, der Alarm des
// Servers mit der Not-Kopie bzw. der Not-Liste.
const keysCorruptMessage = "Empfängerliste nicht lesbar (Notbetrieb). Ein Alarm geht jetzt an die Not-Kopie des " +
	"Servers bzw. die Not-Liste aus config.toml. Ausweg: Sicherung zurückspielen (server/README.md)."

// handleKeysList (dashboard_api.php 315–377): die Liste für die Oberfläche.
// Zuerst überfällige Stummschaltungen zurück auf laut (so greift das
// Sicherheitsnetz auch ohne Prüf-Lauf) – ein Fehler dabei ändert nichts an
// der Antwort, die Liste bleibt sichtbar. Je Empfänger: muted wirksam
// (überfällig = laut), mute_overdue = überfällig, aber nicht gesichert (die
// BOX alarmiert dann noch stumm), mute_until = wann das Sicherheitsnetz
// zurückschaltet (nicht bei "dauerhaft"). Telefonnummer und Kurzbefehl-Token
// nur für den Admin. (PHPs ensure_mute_tokens entfällt: Jeder gespeicherte
// Empfänger hat einen gültigen Token – sonst ließe sich der Zustand nicht
// sichern.)
func handleKeysList(x *Ctx) {
	ctx, cancel := web.Budget(web.BudgetUI)
	_ = mutes.Expire(ctx, x.Core) // ein Fehler ändert nichts an der Antwort (Expire schreibt ihn ins Journal)
	cancel()
	if x.Core.Store.Emergency() {
		web.JSON(x.W, http.StatusOK, web.Object{{Key: "ok", Value: false}, {Key: "code", Value: "keys_corrupt"},
			{Key: "message", Value: keysCorruptMessage}})
		return
	}
	st := x.Core.Store.State()
	admin := x.Sess.Role == "admin"
	now := x.Core.Clock.Now()
	maxMute := st.MuteMax(x.Core.Cfg().MuteMaxSeconds)
	entries := make([]web.Object, 0, len(st.Recipients))
	for _, r := range st.Recipients {
		overdue := model.MuteIsStale(r, now, maxMute)
		muted := r.Muted && !overdue
		item := web.Object{
			{Key: "id", Value: r.ID},
			{Key: "label", Value: r.Label},
			{Key: "key_masked", Value: model.MaskKey(r.Key)},
			{Key: "muted", Value: muted},
			{Key: "mute_overdue", Value: overdue},
			{Key: "muted_at", Value: r.MutedAt},
			{Key: "mute_volume", Value: r.MuteVolume},
			{Key: "mute_permanent", Value: r.MutePermanent},
		}
		if admin {
			item = append(item, web.Member{Key: "phone", Value: r.Phone})
		}
		if muted && !r.MutePermanent && r.MutedAt > 0 && maxMute > 0 {
			item = append(item, web.Member{Key: "mute_until", Value: r.MutedAt + int64(maxMute)})
		}
		if admin {
			item = append(item, web.Member{Key: "mute_token", Value: r.MuteToken})
		}
		entries = append(entries, item)
	}
	web.JSON(x.W, http.StatusOK, web.Object{
		{Key: "ok", Value: true},
		{Key: "version", Value: st.KeysVersion},
		{Key: "keys", Value: entries},
		{Key: "demo", Value: demoInfo(st)},
		{Key: "mute_max_seconds", Value: maxMute},
	})
}

// handleKeysAdd (dashboard_api.php 527–579): Name (1–40 Zeichen, keine
// Steuerzeichen), Key (5–64 aus A-Z a-z 0-9 _ -) und optional Telefonnummer
// – eine ungültige lässt das Anlegen ganz scheitern. Höchstens 10
// Empfänger, kein Key doppelt (im Entwurf geprüft). Die id kommt über die
// Hochwassermarke (nie eine schon vergebene), dazu ein eigener
// Kurzbefehl-Token. Neue Version; Verlauf key_added.
func handleKeysAdd(x *Ctx) {
	label := config.TrimPHP(x.Form.Get("label"))
	key := config.TrimPHP(x.Form.Get("key"))
	if !model.ValidLabel(label) {
		reply(x, msgBadLabel)
		return
	}
	if !model.ValidKey(key) {
		reply(x, msgBadKey)
		return
	}
	phone, ok := model.NormalizePhone(x.Form.Get("phone"))
	if !ok {
		reply(x, msgBadPhone)
		return
	}
	now, by, bv := x.Core.Clock.Now(), x.Sess.User, boxVersion(x)
	apply(x, func(d *model.State) (web.Object, error) {
		if len(d.Recipients) >= model.MaxRecipients {
			return nil, refuse("Maximal 10 Empfänger möglich.")
		}
		for _, r := range d.Recipients {
			if r.Key == key {
				return nil, refuse(msgKeyTaken)
			}
		}
		r := model.Recipient{ID: d.AllocateID(), Key: key, Label: label, Phone: phone, AddedAt: now.Unix(),
			MuteToken: uniqueToken(d)}
		d.Recipients = append(d.Recipients, r)
		version := d.Bump(bv)
		d.Log(keyEvent(model.EvKeyAdded, r, by, now, map[string]any{"version": version}))
		return web.Object{{Key: "ok", Value: true},
			{Key: "message", Value: "Empfänger hinzugefügt. " + msgBoxTakesList}, {Key: "version", Value: version}}, nil
	})
}

// handleKeysDelete (dashboard_api.php 581–619): Der aktive Demo-Empfänger ist
// gerade der EINZIGE Alarmweg – erst nach dem Demo-Modus löschbar. Der letzte
// Empfänger bleibt (die Alarmierung steht nie ohne da). Neue Version;
// Verlauf key_removed; der Klartext-Key geht nie an den Browser.
func handleKeysDelete(x *Ctx) {
	id := formID(x)
	now, by, bv := x.Core.Clock.Now(), x.Sess.User, boxVersion(x)
	apply(x, func(d *model.State) (web.Object, error) {
		if isDemoRecipient(d, id) {
			return nil, refuse(msgDemoKeyFixed)
		}
		at := -1
		for i, r := range d.Recipients {
			if r.ID == id {
				at = i
				break
			}
		}
		if at < 0 {
			return nil, refuse(msgNotFound)
		}
		if len(d.Recipients) == 1 {
			return nil, refuse("Der letzte Empfänger kann nicht gelöscht werden.")
		}
		removed := d.Recipients[at]
		d.Recipients = append(d.Recipients[:at], d.Recipients[at+1:]...)
		version := d.Bump(bv)
		d.Log(keyEvent(model.EvKeyRemoved, removed, by, now, map[string]any{"version": version}))
		return web.Object{{Key: "ok", Value: true},
			{Key: "message", Value: "Empfänger gelöscht. " + msgBoxTakesList}, {Key: "version", Value: version}}, nil
	})
}

// handleKeysRename (dashboard_api.php 626–659): Der Name steht nur im
// Dashboard – KEINE neue Version. Ist es der aktive Demo-Empfänger, zieht die
// Anzeige des Demo-Modus mit (in derselben Änderung). Verlauf key_renamed mit
// dem alten Namen.
func handleKeysRename(x *Ctx) {
	id := formID(x)
	label := config.TrimPHP(x.Form.Get("label"))
	if !model.ValidLabel(label) {
		reply(x, msgBadLabel)
		return
	}
	now, by := x.Core.Clock.Now(), x.Sess.User
	apply(x, func(d *model.State) (web.Object, error) {
		r := d.Find(id)
		if r == nil {
			return nil, refuse(msgNotFound)
		}
		old := r.Label
		if old == label {
			return nil, same("Der Name ist bereits so eingetragen.")
		}
		if isDemoRecipient(d, id) {
			d.Demo.Label = label
		}
		r.Label = label
		d.Log(keyEvent(model.EvKeyRenamed, *r, by, now, map[string]any{"old_label": old}))
		return changed("Name geändert.", d.KeysVersion), nil
	})
}

// handleKeysReplaceKey (dashboard_api.php 665–702): neuer Bark-Key für
// denselben Empfänger (neues iPhone) – Name, Stumm-Zustand, Token und
// Telefonnummer bleiben. Für den aktiven Demo-Empfänger abgelehnt (der
// Demo-Modus hält seinen Key). Neue Version; Verlauf key_replaced mit beiden
// Keys maskiert.
func handleKeysReplaceKey(x *Ctx) {
	id := formID(x)
	key := config.TrimPHP(x.Form.Get("key"))
	if !model.ValidKey(key) {
		reply(x, msgBadKey)
		return
	}
	now, by, bv := x.Core.Clock.Now(), x.Sess.User, boxVersion(x)
	apply(x, func(d *model.State) (web.Object, error) {
		if isDemoRecipient(d, id) {
			return nil, refuse(msgDemoKeyFixed)
		}
		r := d.Find(id)
		if r == nil {
			return nil, refuse(msgNotFound)
		}
		for _, o := range d.Recipients {
			if o.ID != id && o.Key == key {
				return nil, refuse(msgKeyTaken)
			}
		}
		old := r.Key
		if old == key {
			return nil, same(msgKeyTaken)
		}
		r.Key = key
		version := d.Bump(bv)
		d.Log(keyEvent(model.EvKeyReplaced, *r, by, now, map[string]any{"old_key": model.MaskKey(old), "version": version}))
		return changed("Key ersetzt. "+msgBoxTakesList, version), nil
	})
}

// handleKeysSetPhone (dashboard_api.php 706–735): Telefonnummer ("" =
// löschen) – reine Anzeige für den Admin, weder Box noch Bark noch Verlauf
// bekommen sie, also keine neue Version. Verlauf key_phone_changed OHNE
// Nummer.
func handleKeysSetPhone(x *Ctx) {
	id := formID(x)
	phone, ok := model.NormalizePhone(x.Form.Get("phone"))
	if !ok {
		reply(x, msgBadPhone)
		return
	}
	now, by := x.Core.Clock.Now(), x.Sess.User
	apply(x, func(d *model.State) (web.Object, error) {
		r := d.Find(id)
		if r == nil {
			return nil, refuse(msgNotFound)
		}
		if r.Phone == phone {
			return nil, same("Telefonnummer ist bereits so eingetragen.")
		}
		r.Phone = phone
		d.Log(keyEvent(model.EvKeyPhoneChanged, *r, by, now, nil))
		message := "Telefonnummer gespeichert."
		if phone == "" {
			message = "Telefonnummer entfernt."
		}
		return changed(message, d.KeysVersion), nil
	})
}

// handleKeysNewMuteToken (dashboard_api.php 746–765): neuer Kurzbefehl-Token
// – die bisherigen Links gelten sofort nicht mehr (der Ausweg, wenn einer in
// falsche Hände geraten ist). Der Stumm-Zustand bleibt; keine neue Version
// (die Box kennt keine Tokens). Der Token steht weder in der Antwort noch im
// Verlauf (key_token_renewed) – die Oberfläche holt ihn mit keys_list.
func handleKeysNewMuteToken(x *Ctx) {
	id := formID(x)
	now, by := x.Core.Clock.Now(), x.Sess.User
	apply(x, func(d *model.State) (web.Object, error) {
		r := d.Find(id)
		if r == nil {
			return nil, refuse(msgNotFound)
		}
		r.MuteToken = uniqueToken(d)
		d.Log(keyEvent(model.EvKeyTokenRenewed, *r, by, now, nil))
		return changed("Neue Kurzbefehl-Links erzeugt – die bisherigen Links funktionieren ab sofort nicht mehr. "+
			"Die neuen Links kopieren und im Kurzbefehl des Empfängers eintragen.", d.KeysVersion), nil
	})
}
