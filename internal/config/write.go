package config

import (
	"bytes"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// Kopf jeder geschriebenen Datei (auch der Vorlage server/go/config.example.toml).
const writeHeader = `# Konfiguration des Alarm-Servers fw-alarm.
# Auf dem Server: /etc/fw-alarm/config.toml (root:fwalarm, Rechte 0640).
# Enthält Geheimnisse (Hashes, Bark-Keys) – nie öffentlich teilen, nie ins Git.
# Vorlage im Repo: server/go/config.example.toml. Prüfen: sudo fw-alarm check-config
#
# ===========================================================================
# >>>>>>>>>>  HIER STEHEN ALLE WERTE, DIE DU EDITIEREN MUSST/KANNST  <<<<<<<<<
# ===========================================================================
#
# Fehlt ein Schlüssel, gilt sein Standardwert. Ein unbekannter Schlüssel, ein
# Wert vom falschen Typ, eine unbekannte Zeitzone, eine unzulässige
# Probe-Sperre, ein bark_host mit Abfrage, Fragment oder Zugangsdaten oder
# eine Datei ohne einen einzigen Wert machen die Datei fehlerhaft: Dann gilt
# die letzte gültige Fassung weiter, und alle Sitzungen enden. Eine geänderte
# Datei gilt erst, wenn sie zwei Sekunden lang unverändert bleibt (sofort:
# sudo fw-alarm reload). Texte stehen in "…"; ein " oder \ darin wird als \"
# bzw. \\ geschrieben.
`

// Ein Abschnitt der Datei: Erklärung (ohne "# ") und die Schlüssel darunter.
type block struct {
	comment string
	keys    []string
}

// Aufbau der Datei – jeder Schlüssel genau einmal, in der Reihenfolge der
// Felder von Config. Die Erklärungen stammen aus config.example.php.
var layout = []block{
	{`==== Probe-Sperre (nur beim Umschalten; sonst beide leer) =================
Solange send_guard_keys nicht leer ist und send_guard_until in der Zukunft
liegt, sendet der Server an KEINEN anderen Key – weder Alarm noch
Entwarnung, Test-Push oder Meldungen, gleich was Empfängerliste und
Demo-Modus ergeben. Die Box alarmiert unabhängig davon immer selbst.
send_guard_until ist Ortszeit in timezone, Form "JJJJ-MM-TT HH:MM"; danach
endet die Sperre von selbst. Damit eine vertippte Sperre nie einen echten
Alarm von Hand blockiert, ist die Datei fehlerhaft (wird nicht angenommen),
wenn die Zeit mehr als 3 Stunden voraus liegt, nur einer der beiden Werte
gesetzt ist oder ein Eintrag kein gültiger Bark-Key ist.
Die Sperre steht als Erstes in der Datei, zwischen ihren beiden Werten die
Zeitzone: Eine Datei, die beim Speichern abgeschnitten wurde, hält die
Sperre so entweder ganz (samt Zeitzone) oder ist fehlerhaft.`,
		[]string{"send_guard_keys"}},
	{`==== Zeitzone =============================================================
Für Zeitstempel in Meldungen, Ruhezeiten und die Probe-Sperre. Name aus der
Zeitzonen-Datenbank, z.B. "Europe/Berlin"; ein unbekannter Name macht die
Datei fehlerhaft.`,
		[]string{"timezone"}},
	{`Ende der Probe-Sperre (Ortszeit in timezone, Form "JJJJ-MM-TT HH:MM");
leer, wenn send_guard_keys leer ist.`,
		[]string{"send_guard_until"}},
	{`==== Zugang der Box =======================================================
Lange zufällige Zeichenfolge, die in boss925_alarm_bark/config.h als
REMOTE_MACHINE_TOKEN eingetragen wird. Hier steht NUR der SHA-256-Hash
davon (64 Hex-Zeichen; Großbuchstaben und Leerzeichen außen sind egal).
Token UND Hash in einem Schritt erzeugen (das Geheimnis steht so nie in der
Shell-History) – Zeile 1 = Token für config.h, Zeile 2 = Hash:
  t=$(openssl rand -hex 32); echo "$t"; printf '%s' "$t" | sha256sum | cut -d' ' -f1`,
		[]string{"machine_token_sha256"}},
	{`==== Anmeldung am Dashboard ===============================================
Admin-Zugang. Passwort-Hash erzeugen (das Passwort wird zweimal verdeckt
eingetippt, nie als Argument – sonst landet es in der History):
  sudo fw-alarm hash-password
Vorhandene bcrypt-Hashes aus config.php ($2y$…) gelten weiter.`,
		[]string{"dashboard_user", "dashboard_password_hash"}},
	{`Optionaler zweiter Benutzer mit reinem LESE-Zugriff: Er sieht Status und
Empfängerliste, kann aber weder TEST noch Alarm auslösen und die Liste
nicht ändern. Beide Werte leer lassen ("") = Lese-Zugang aus. Hash erzeugen
wie oben.`,
		[]string{"dashboard_readonly_user", "dashboard_readonly_password_hash"}},
	{`Login-Bremse: Nach so vielen Fehlversuchen wird der Absender für das
Zeitfenster (Sekunden) gesperrt – zusätzlich zur Pause von 1 s nach jedem
Fehlversuch. Untergrenzen: 1 Versuch, 60 Sekunden.`,
		[]string{"login_max_failures", "login_lockout_window_seconds"}},
	{`==== Nur hinter einem Reverse-Proxy / Tunnel (sonst leer lassen) ==========
Läuft der Server hinter einem Proxy auf demselben Rechner (Cloudflare
Tunnel), kommen aus seiner Sicht ALLE Aufrufe von dessen Adresse, und HTTPS
endet schon am Proxy. Ohne diese beiden Einträge zählt die Login-Sperre alle
Besucher als EINE Adresse (jeder sperrt mit ein paar Fehlversuchen den
Admin aus), und das Sitzungs-Cookie geht ohne „Secure“ raus.
trusted_proxies: Adressen, von denen der Proxy den Server anspricht – nur
von dort (und über einen Unix-Socket, siehe listen) werden
X-Forwarded-Proto und die unten genannte Kopfzeile angenommen, z.B.
["127.0.0.1", "::1"]. NIE eintragen, wenn der Server auch
direkt aus dem Internet erreichbar ist (dann könnte sich jeder eine Adresse
aussuchen).
proxy_client_ip_header: Kopfzeile, in die DIESER Proxy die echte Adresse
des Besuchers schreibt: "CF-Connecting-IP" (Cloudflare Tunnel), "X-Real-IP"
oder "X-Forwarded-For" (dort zählt der letzte Eintrag).`,
		[]string{"trusted_proxies", "proxy_client_ip_header"}},
	{`==== Box ==================================================================
Nach so vielen Sekunden ohne Status-Push gilt die Box als offline.`,
		[]string{"offline_after_seconds"}},
	{`Optional: Kennung der ECHTEN Box – derselbe Wert wie REMOTE_DEVICE_ID in
config.h (z.B. "boss925-01"). Gesetzt zählen für Online-Anzeige,
Offline-Wächter und Demo-Sicherung nur die Status-Pushs dieser Box, und nur
sie bekommt Befehle (TEST/DEMO-ALARM). Pushs anderer Boards mit demselben
Maschinen-Token (Bastel-/Ersatz-ESP32) werden angenommen, aber nur als
Hinweis „fremdes Board meldet sich“ im Dashboard geführt – ohne den Eintrag
würde ein weiter pushendes Bastel-Board den Ausfall der echten Box
verdecken. Leer ("") = jeder Push mit dem Token zählt; melden sich dann
mehrere Boards, warnt das Dashboard.`,
		[]string{"box_device_id"}},
	{`==== Meldungen an den Admin + Offline-Wächter (optional) ==================
Bark-Key des Owners für ALLE Meldungen des Servers an den Admin: Der
Prüf-Lauf meldet, wenn die Box zu lange keinen Status gepusht hat (und wenn
sie wieder da ist), wenn der Server nicht mehr speichern kann („Dashboard
DEFEKT“), wenn der Außen-Wächter verstummt, und erinnert an Demo-Modus und
Stummschaltungen; dazu kommen die Meldung bei einer Login-Sperre und die
optionalen Arten (Anmeldung, Neustart der Box, Wochenbericht ...). WELCHE
Meldungen an diesen Key gehen und wie dringend (ein/aus pro Art,
Dringlichkeit, Ruhezeit, Ton, Stellvertreter, Wiederholungen), stellst du im
Dashboard ein: Einstellungen → „Meldungen an mich“ (Standard: leise). Die
Werte hier darunter sind nur die Standards, solange dort nichts anderes
gewählt ist.
Leer ("") = ALLE Admin-Meldungen des Servers aus (das meldet dann auch der
Außen-Wächter). Sinnvoll: derselbe Key wie BARK_KEY_STATUS in config.h der
Box und BARK_KEY in der Konfiguration des Außen-Wächters.`,
		[]string{"bark_key_status"}},
	{`Wächter-Schwelle in Sekunden (großzügiger als offline_after_seconds, damit
nicht jeder kurze WLAN-Schluckauf eine Meldung auslöst).`,
		[]string{"watchdog_offline_after_seconds"}},
	{`Bleibt der Demo-Modus länger als diese Zeit (Sekunden) aktiv, erinnert der
Wächter leise per Bark daran (danach max. 1x pro Tag) – echte Alarme
erreichen im Demo-Modus nur den Test-Empfänger!`,
		[]string{"demo_reminder_after_seconds"}},
	{`Außen-Wächter (optional, siehe cloud_watcher/README.md): Ein ANDERER
Rechner (das NAS zu Hause) ruft api/alive.php alle 5 min von AUSSEN auf und
meldet per Bark, wenn der Server nicht erreichbar ist – das kann der Wächter
hier nicht, er fällt ja mit aus. Umgekehrt meldet dieser Wächter, wenn der
Außen-Wächter verstummt. Hier steht NUR der SHA-256-Hash des Wächter-Tokens
(eigenes Token, NICHT das Maschinen-Token der Box); der Klartext kommt in die
Konfiguration des Außen-Wächters. Erzeugen wie oben bei
machine_token_sha256.
Leer = aus (api/alive.php antwortet dann 404). Derselbe Hash wie
machine_token_sha256 wird ebenfalls mit 404 abgelehnt – der Außen-Wächter
darf nie das Token der Box kennen.`,
		[]string{"watcher_token_sha256"}},
	{`==== Arbeitsmodus (Stummschaltung per Kurzbefehl-Link) ===================
Jeder Empfänger kann seinen Eintrag über einen persönlichen Link stumm
schalten (z.B. iPhone-Kurzbefehl beim Betreten/Verlassen der Arbeit):
  .../api/mute.php?t=<GEHEIM-TOKEN>&state=on|off
Den fertigen Link kopiert der Admin im Dashboard im Bereich „Empfänger“
(Person antippen). Die alte Form ?key=<EIGENER_BARK_KEY> bleibt für
Bestands-Kurzbefehle gültig. Stumm = der Alarm kommt weiterhin als Critical
Alert an, aber mit der im Dashboard eingestellten Stumm-Lautstärke
(Standard 0 = lautlos). Sicherheitsnetz: Nach so vielen Sekunden wird
automatisch wieder auf laut geschaltet (0 = nie automatisch) – falls die
„Arbeit verlassen“-Automation mal nicht auslöst. Der Wächter erinnert
zusätzlich max. 1x pro Tag, solange Empfänger stumm sind. Im Dashboard
einstellbar; dieser Wert gilt, solange dort nichts gewählt ist.`,
		[]string{"mute_max_seconds"}},
	{`Öffentliche Adresse des Dashboards (mit abschließendem /), z.B.
"https://alarm.example.org/". Daraus baut das Dashboard die kopierbaren
Kurzbefehl-Links. Leer = die Adresse, unter der das Dashboard gerade
geöffnet ist – wer es über eine LAN-Adresse öffnet, kopiert dann aber Links,
die unterwegs nicht funktionieren.`,
		[]string{"public_base_url"}},
	{`==== Befehle an die Box ===================================================
Ausgelieferte Befehle werden nicht erneut ausgeliefert. Nach dieser Zeit
(Sekunden) darf das Dashboard einen neuen Befehl anlegen, falls die
Quittung der Box verloren ging.`,
		[]string{"delivered_command_lock_seconds"}},
	{`Ein Befehl, den die Box nicht innerhalb dieser Zeit (Sekunden) abholt,
verfällt („expired“) – sonst würde er Stunden später noch ausgeführt und das
Dashboard bliebe bis dahin blockiert.`,
		[]string{"pending_command_ttl_seconds"}},
	{`==== Alarm direkt vom Server an Bark ======================================
„Alarm an alle“ sendet OHNE Umweg über die Box – er muss ja gerade dann
funktionieren, wenn die Kette über die Box klemmt. Alle Empfänger bekommen
ihn gleichzeitig, je bis zu bark_max_tries Versuche (Untergrenze 1).
bark_host: nur Schema und Rechner, z.B. "https://api.day.app" – ohne Pfad
(sonst ginge jede Nachricht an das Gerät, das der Pfad nennt), ohne Abfrage
und ohne Zugangsdaten.
Die Empfängerliste wird IM DASHBOARD gepflegt (Bereich „Empfänger“);
bark_keys_alarm hier ist nur die Not-Liste für den Fall, dass der Server
keine Empfänger hat, z.B. ["KEY_PERSON_1", "KEY_PERSON_2"].
Lautstärke 0-10; bark_alarm_call = true lässt den Ton sich wiederholen
(call=1, gilt nicht für Stumme).
ACHTUNG: Titel und Text nur ASCII (ae/ue/oe), Bark zeigt keine Umlaute.`,
		[]string{"bark_host", "bark_keys_alarm", "bark_alarm_title", "bark_alarm_body",
			"bark_alarm_sound", "bark_alarm_volume", "bark_alarm_call", "bark_max_tries"}},
	{`==== ENTWARNUNG (Fehlalarm) ===============================================
Geht an dieselben Empfänger wie der Alarm und ist ebenfalls ein CRITICAL
ALERT – wer schon zum Einsatz aufgebrochen ist, muss die Entwarnung genauso
sicher mitbekommen (durch Stummschalter und Fokus hindurch). Unterschied zum
Alarm: ein kurzer, normaler Ton statt des Alarmtons und keine
Ton-Wiederholung (kein call=1).
Ton: einer der in Bark eingebauten Kurztöne, z.B. "glass" (klassisches
kurzes „Pling“), "bell", "chime", "telegraph" oder "paymentsuccess".`,
		[]string{"bark_false_alarm_sound"}},
	{`Lautstärke 0-10. Ohne Eintrag gilt bark_alarm_volume.`,
		[]string{"bark_false_alarm_volume"}},
	{`==== Server (Änderungen gelten erst nach einem Neustart des Dienstes) =====
Datenordner (Empfängerliste, Verlauf, Sitzungen) – absoluter Pfad, außerhalb
jedes Webroots, gehört dem Benutzer fwalarm. Hier liegen die Bark-Keys im
KLARTEXT.`,
		[]string{"state_dir"}},
	{`Adresse, auf der der Server lauscht: "host:port" (Standard
"127.0.0.1:8080": nur auf diesem Rechner, der Cloudflare Tunnel spricht ihn
dort an) oder "unix:<Pfad>" für einen Unix-Socket. Was über den
Unix-Socket kommt, gilt als vom Tunnel: X-Forwarded-Proto und
proxy_client_ip_header zählen dann auch ohne trusted_proxies – den Socket
darf außer dem Server nur der Benutzer von cloudflared öffnen.`,
		[]string{"listen"}},
	{`Steuer-Zugang für die Befehle fw-alarm health, check, flush, reload und
stop (Unix-Socket, nur auf diesem Rechner).`,
		[]string{"control_socket"}},
	{`Abstand der vollen Prüf-Läufe (Offline-Wächter, Erinnerungen, Selbsttest)
in Sekunden; der erste läuft 60 s nach dem Start. 0 = keine selbsttätigen
Läufe – dann meldet der Server weder eine stumme Box noch andere Störungen
(nur für Tests).`,
		[]string{"check_interval_seconds"}},
}

// Write schreibt c als TOML mit Erklärungen in fester Reihenfolge.
// ParseAt(Write(c)) ergibt dieselben Werte – für jeden Stand, dessen Texte
// gültiges UTF-8 sind (TOML kennt nichts anderes; alles, was Parse liefert,
// ist es).
func Write(c *Config) []byte {
	var b bytes.Buffer
	b.WriteString(writeHeader)
	fields := fieldsByName()
	v := reflect.ValueOf(c).Elem()
	for _, blk := range layout {
		b.WriteString("\n")
		for _, line := range strings.Split(blk.comment, "\n") {
			if line == "" {
				b.WriteString("#\n")
				continue
			}
			b.WriteString("# " + line + "\n")
		}
		for _, key := range blk.keys {
			f, ok := fields[key]
			if !ok {
				// Ein Tippfehler in layout – fällt in jedem Test von Write auf.
				panic(fmt.Sprintf("config: unbekannter Schlüssel %q im Aufbau der Datei", key))
			}
			b.WriteString(key + " = " + formatValue(v.FieldByIndex(f.Index)) + "\n")
		}
	}
	return b.Bytes()
}

func formatValue(v reflect.Value) string {
	switch v.Kind() {
	case reflect.String:
		return quote(v.String())
	case reflect.Int:
		return strconv.FormatInt(v.Int(), 10)
	case reflect.Bool:
		return strconv.FormatBool(v.Bool())
	case reflect.Slice:
		items := make([]string, v.Len())
		for i := range items {
			items[i] = quote(v.Index(i).String())
		}
		return "[" + strings.Join(items, ", ") + "]"
	default:
		// Nur die vier Arten oben kommen in Config vor.
		panic(fmt.Sprintf("config: Feld der Art %s lässt sich nicht schreiben", v.Kind()))
	}
}

// quote schreibt s als TOML-Text in "…": Anführungszeichen, Rückstrich und
// Steuerzeichen maskiert, alles andere wörtlich (auch Umlaute).
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
