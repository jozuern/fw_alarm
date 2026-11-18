package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"fwalarm/internal/config"
	"fwalarm/internal/core"
)

// Geräte-Ausweis (Spec §6.8 "bekannte Geräte zuerst", §10): Nach einer
// gelungenen Anmeldung bekommt das Gerät ein zweites, langlebiges Cookie –
// kein Zugang, nur ein vom Server unterschriebener Vermerk "an diesem Gerät
// hat sich schon einmal jemand mit diesem Konto richtig angemeldet".
// Anmeldeversuche mit gültigem Ausweis warten in der Vorzugsreihe
// (slots.go); sonst ändert er nichts: Bremse, Zählung, Prüfung, Pause und
// Antwort sind dieselben.
//
// Form: v1.<r>.<id>.<mac> – <r> "a" (Admin) oder "r" (Lese-Benutzer), <id>
// 16 Zufallsbytes, <mac> HMAC-SHA256 mit config.DeviceKey(Rolle) über
// "v1.<r>.<id>"; <id> und <mac> URL-sicheres Base64 ohne "=". Der Schlüssel
// hängt am Passwort-Hash und am Namen des Kontos: Passwort oder Name ändern
// entwertet jeden alten Ausweis. Abmelden, EndAll und eine fehlerhafte
// Konfiguration lassen ihn stehen.

const (
	deviceCookie  = "boss925_device"
	deviceMaxAge  = 400 * 24 * 3600 // 400 Tage – länger nehmen Browser ein Cookie nicht an
	deviceIDBytes = 16
	deviceVersion = "v1"
)

// deviceText: Base64 der Ausweis-Teile, streng (keine überzähligen Bits).
var deviceText = base64.RawURLEncoding.Strict()

// devicePassLen ist die Länge jedes gültigen Ausweises (71 Zeichen).
var devicePassLen = len(deviceVersion+".a.") + deviceText.EncodedLen(deviceIDBytes) + 1 + deviceText.EncodedLen(sha256.Size)

// devicePass ist ein gültiger Ausweis.
type devicePass struct {
	id   string // <id> wie im Cookie – Schlüssel der Vorzugsreihe
	role string // "admin" oder "readonly"
}

// devicePassOf liefert den Ausweis der Anfrage, wenn er für ein Konto der
// Fassung cfg gilt. Kostet höchstens zwei HMAC (Schlüssel und Prüfsumme),
// nie eine Passwortprüfung. Bei mehreren Cookies dieses Namens zählt das
// erste.
func devicePassOf(r *http.Request, cfg *config.Config) (devicePass, bool) {
	ck, err := r.Cookie(deviceCookie)
	if err != nil {
		return devicePass{}, false
	}
	return parseDevicePass(ck.Value, cfg)
}

// parseDevicePass prüft einen Ausweis streng: genau vier Teile in genau der
// Form, die mintDevicePass schreibt (jede andere Schreibweise derselben Bytes
// ist keiner), Prüfsumme in konstanter Zeit verglichen. Alles andere ist kein
// Ausweis – nie ein Fehler.
func parseDevicePass(v string, cfg *config.Config) (devicePass, bool) {
	if len(v) != devicePassLen {
		return devicePass{}, false
	}
	parts := strings.Split(v, ".")
	if len(parts) != 4 || parts[0] != deviceVersion {
		return devicePass{}, false
	}
	var role string
	switch parts[1] {
	case "a":
		role = "admin"
	case "r":
		role = "readonly"
	default:
		return devicePass{}, false
	}
	id, err := deviceText.DecodeString(parts[2])
	if err != nil || len(id) != deviceIDBytes || deviceText.EncodeToString(id) != parts[2] {
		return devicePass{}, false
	}
	mac, err := deviceText.DecodeString(parts[3])
	if err != nil || len(mac) != sha256.Size || deviceText.EncodeToString(mac) != parts[3] {
		return devicePass{}, false
	}
	key := cfg.DeviceKey(role)
	if key == nil {
		return devicePass{}, false
	}
	if !hmac.Equal(mac, deviceMAC(key, parts[0]+"."+parts[1]+"."+parts[2])) {
		return devicePass{}, false
	}
	return devicePass{id: parts[2], role: role}, true
}

// devicePassFor schreibt den Ausweis mit der id für role ("admin" oder
// "readonly") in der Fassung cfg; "" = das Konto hat keinen Schlüssel.
func devicePassFor(cfg *config.Config, role string, id []byte) string {
	key := cfg.DeviceKey(role)
	if key == nil {
		return ""
	}
	r := "r"
	if role == "admin" {
		r = "a"
	}
	head := deviceVersion + "." + r + "." + deviceText.EncodeToString(id)
	return head + "." + deviceText.EncodeToString(deviceMAC(key, head))
}

func deviceMAC(key []byte, head string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(head)) // Schreiben in einen Hash scheitert nie
	return m.Sum(nil)
}

// setDeviceCookie gibt dem Gerät einen neuen Ausweis für role – nur in der
// Antwort auf eine gelungene Anmeldung (Login). 400 Tage, Eigenschaften wie
// das Sitzungs-Cookie.
func setDeviceCookie(c *core.Core, w http.ResponseWriter, r *http.Request, cfg *config.Config, role string) {
	v := devicePassFor(cfg, role, randomBytes(deviceIDBytes))
	if v == "" {
		return // kann nach einer gelungenen Prüfung nicht sein: das Konto hat einen brauchbaren Hash
	}
	putCookie(c, w, r, &http.Cookie{Name: deviceCookie, Value: v, MaxAge: deviceMaxAge,
		Expires: c.Clock.Now().Add(deviceMaxAge * time.Second)})
}
