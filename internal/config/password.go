package config

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

// Neue Hashes: Argon2id mit 19 MiB, 2 Durchläufen, 1 Faden – der Mindestwert
// der OWASP-Empfehlung, passend zur 1-GB-VM.
const (
	argonMemoryKiB = 19 * 1024
	argonPasses    = 2
	argonThreads   = 1
	argonSaltLen   = 16
	argonKeyLen    = 32
)

// Obergrenzen für vorhandene Hashes (Spec §6.8): Höchstens zwei Prüfungen
// laufen zugleich, und der Dienst hat 256 MiB Speicher (MemoryMax) – ein
// teurerer Hash sprengte mit zwei Prüfungen die Speichergrenze oder hielte
// die Plätze sekundenlang besetzt (bcrypt 13 dauert auf der VM rund eine
// halbe Sekunde, jede Stufe mehr das Doppelte). Solche Hashes gelten als
// unbrauchbar; check-config nennt sie "zu aufwendig". Der Argon2id-Hash von
// PHP (64 MiB, 4 Durchläufe) liegt genau auf der Grenze.
const (
	maxArgonMemoryKiB = 64 * 1024
	maxArgonPasses    = 4
	maxArgonThreads   = 4
	maxBcryptCost     = 13
)

// hashTooExpensiveText nennt die Obergrenzen (für die Hinweise).
const hashTooExpensiveText = "Hash zu aufwendig (Argon2id höchstens 64 MiB, 4 Durchläufe und 4 Fäden, bcrypt höchstens Kosten 13)"

var b64 = base64.RawStdEncoding // Form der PHC-Zeichenketten (ohne "=")

// HashPassword erzeugt einen neuen Argon2id-Hash in der üblichen Form
// $argon2id$v=19$m=19456,t=2,p=1$<Salz>$<Hash>.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("kein Zufall für das Salz: %w", err)
	}
	key := argon2.IDKey([]byte(pw), salt, argonPasses, argonMemoryKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonPasses, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword prüft pw gegen einen Argon2id-Hash (mit den Werten, die er
// trägt, innerhalb der Obergrenzen) oder einen bcrypt-Hash ($2y$, $2a$, $2b$
// – so erzeugt PHP sie). Alles andere ist false.
func VerifyPassword(hash, pw string) bool {
	if a, ok := parseArgon(hash); ok {
		got := argon2.IDKey([]byte(pw), a.salt, a.passes, a.memoryKiB, a.threads, uint32(len(a.key)))
		return subtle.ConstantTimeCompare(got, a.key) == 1
	}
	if bcryptUsable(hash) {
		return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
	}
	return false
}

// PasswordHashUsable: Würde VerifyPassword diesen Hash überhaupt prüfen
// (Argon2id oder bcrypt, innerhalb der Obergrenzen)? Für check-config und
// die Anmeldung (ein unbrauchbarer Hash wird nie geprüft, web/verify.go).
func PasswordHashUsable(hash string) bool { return judgeHash(hash) == hashUsable }

// hashVerdict: brauchbar, keine erkennbare Form (Klartext, anderes
// Verfahren, kaputt) oder in der Form richtig, aber über den Obergrenzen.
type hashVerdict int

const (
	hashUsable hashVerdict = iota
	hashMalformed
	hashTooExpensive
)

func judgeHash(hash string) hashVerdict {
	if a, ok := parseArgonForm(hash); ok {
		if !argonWithinBounds(a) {
			return hashTooExpensive
		}
		return hashUsable
	}
	if !bcryptPattern.MatchString(hash) {
		return hashMalformed
	}
	cost, err := bcrypt.Cost([]byte(hash))
	switch {
	case err != nil || cost < bcrypt.MinCost: // auch Kosten über 31: kein bcrypt
		return hashMalformed
	case cost > maxBcryptCost:
		return hashTooExpensive
	}
	return hashUsable
}

var bcryptPattern = regexp.MustCompile(`^\$2[aby]\$[0-9]{2}\$[./A-Za-z0-9]{53}$`)

func bcryptUsable(hash string) bool {
	return bcryptPattern.MatchString(hash) && judgeHash(hash) == hashUsable
}

type argonHash struct {
	memoryKiB uint32
	passes    uint32
	threads   uint8
	salt, key []byte
}

// Zahlen ohne Vorzeichen und ohne führende Nullen.
var argonParams = regexp.MustCompile(`^m=([1-9][0-9]{0,9}),t=([1-9][0-9]{0,9}),p=([1-9][0-9]{0,2})$`)

// parseArgon: ein Argon2id-Hash in gültiger Form und innerhalb der
// Obergrenzen.
func parseArgon(hash string) (argonHash, bool) {
	a, ok := parseArgonForm(hash)
	return a, ok && argonWithinBounds(a)
}

func argonWithinBounds(a argonHash) bool {
	return a.memoryKiB <= maxArgonMemoryKiB && a.passes <= maxArgonPasses && uint32(a.threads) <= maxArgonThreads
}

// parseArgonForm zerlegt einen Argon2id-Hash ohne die Obergrenzen zu prüfen.
func parseArgonForm(hash string) (argonHash, bool) {
	var a argonHash
	parts := strings.Split(hash, "$")
	// "", "argon2id", "v=19", "m=…,t=…,p=…", Salz, Hash
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return a, false
	}
	m := argonParams.FindStringSubmatch(parts[3])
	if m == nil {
		return a, false
	}
	memory, errM := strconv.ParseUint(m[1], 10, 32)
	passes, errT := strconv.ParseUint(m[2], 10, 32)
	threads, errP := strconv.ParseUint(m[3], 10, 8)
	if errM != nil || errT != nil || errP != nil ||
		memory < 8*threads { // Argon2 verlangt mindestens 8 KiB je Faden
		return a, false
	}
	salt, errS := b64.DecodeString(parts[4])
	key, errK := b64.DecodeString(parts[5])
	if errS != nil || errK != nil || len(salt) < 8 || len(key) < 16 || len(key) > 128 {
		return a, false
	}
	a.memoryKiB, a.passes, a.threads = uint32(memory), uint32(passes), uint8(threads)
	a.salt, a.key = salt, key
	return a, true
}
