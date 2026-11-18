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

// Obergrenzen für vorhandene Hashes: Ein Hash mit absurden Werten (von Hand
// erzeugt oder vertippt) hielte sonst jede Anmeldung minutenlang auf oder
// sprengte den Speicher des Dienstes. Solche Hashes gelten als unbrauchbar.
const (
	maxArgonMemoryKiB = 256 * 1024
	maxArgonPasses    = 10
	maxArgonThreads   = 16
	maxBcryptCost     = 15
)

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
// (Argon2id innerhalb der Obergrenzen oder bcrypt)? Für check-config und die
// Anmeldung (ein unbrauchbarer Hash wird nie geprüft, web/verify.go).
func PasswordHashUsable(hash string) bool {
	_, ok := parseArgon(hash)
	return ok || bcryptUsable(hash)
}

var bcryptPattern = regexp.MustCompile(`^\$2[aby]\$[0-9]{2}\$[./A-Za-z0-9]{53}$`)

func bcryptUsable(hash string) bool {
	if !bcryptPattern.MatchString(hash) {
		return false
	}
	cost, err := bcrypt.Cost([]byte(hash))
	return err == nil && cost >= bcrypt.MinCost && cost <= maxBcryptCost
}

type argonHash struct {
	memoryKiB uint32
	passes    uint32
	threads   uint8
	salt, key []byte
}

// Zahlen ohne Vorzeichen und ohne führende Nullen.
var argonParams = regexp.MustCompile(`^m=([1-9][0-9]{0,9}),t=([1-9][0-9]{0,9}),p=([1-9][0-9]{0,2})$`)

func parseArgon(hash string) (argonHash, bool) {
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
		memory > maxArgonMemoryKiB || passes > maxArgonPasses || threads > maxArgonThreads ||
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
