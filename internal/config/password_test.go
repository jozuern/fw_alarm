package config

import (
	"fmt"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

// cheapArgon baut einen Argon2id-Hash mit kleinen Werten (64 KiB, 1 Durchlauf),
// damit der Fuzz-Test ihn tausendfach prüfen kann.
func cheapArgon(pw string) string {
	salt := []byte("salzsalzsalzsalz")
	key := argon2.IDKey([]byte(pw), salt, 1, 64, 1, 32)
	return fmt.Sprintf("$argon2id$v=19$m=64,t=1,p=1$%s$%s", b64.EncodeToString(salt), b64.EncodeToString(key))
}

// Der Hash kommt aus config.toml – Eingabe von außen. Was auch drinsteht:
// kein Absturz, und VerifyPassword nimmt nie einen Hash an, den
// passwordHashUsable verwirft (der Hinweis von check-config muss stimmen).
func FuzzPasswordHash(f *testing.F) {
	argon := cheapArgon("x")
	for _, seed := range []string{
		testBcrypt, otherBcrypt,
		"$2y$04$" + strings.Repeat("A", 53),
		"$2y$31$r60I68m5mVq98EDvnpGXGeS1SZtBd2wWlr0dy0MuX73YPDZ27VzZy",
		argon,
		strings.Replace(argon, "m=64", "m=1048576", 1),
		strings.Replace(argon, "t=1", "t=0", 1),
		strings.Replace(argon, "p=1", "p=255", 1),
		strings.Replace(argon, "v=19", "v=16", 1),
		argon + "$",
		argon[:len(argon)-5],
		"$argon2id$v=19$m=19456,t=2,p=1$WERJZnJaZnBFLlRGQm05NA$0gEMEc9M8Wl65RDmj94mLwQZuYQg/ceEjRdfNJCtCuo",
		"", "$", "$$$$$", "$argon2id$", "$2y$", "PASTE_PASSWORD_HASH_HERE",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, hash string) {
		usable := passwordHashUsable(hash)
		if a, ok := parseArgon(hash); ok {
			if a.memoryKiB > maxArgonMemoryKiB || a.passes > maxArgonPasses || a.threads > maxArgonThreads ||
				a.passes < 1 || a.threads < 1 {
				t.Fatalf("Grenzen nicht eingehalten: %+v", a)
			}
			// Teuer, aber erlaubt: nur Zerlegen prüfen, nicht rechnen (der
			// Fuzz-Test soll Tausende Eingaben je Sekunde schaffen).
			if a.memoryKiB > 1024 || a.passes > 2 {
				return
			}
		}
		if bcryptUsable(hash) {
			cost, err := bcrypt.Cost([]byte(hash))
			if err != nil || cost > maxBcryptCost {
				t.Fatalf("bcrypt-Aufwand %d (%v) gilt als brauchbar", cost, err)
			}
			if cost > 5 {
				return
			}
		}
		if VerifyPassword(hash, "x") && !usable {
			t.Fatalf("VerifyPassword nimmt den als unbrauchbar gemeldeten Hash %q an", hash)
		}
	})
}

func TestVerifyPassword(t *testing.T) {
	// bcrypt aus dem Prüfstand (PHP password_hash, $2y$).
	if !VerifyPassword(testBcrypt, "test") {
		t.Error("bcrypt-Hash von „test“ nicht erkannt")
	}
	if VerifyPassword(testBcrypt, "Test") || VerifyPassword(testBcrypt, "") {
		t.Error("bcrypt: falsches Passwort angenommen")
	}
	for _, prefix := range []string{"$2a$", "$2b$"} {
		if !VerifyPassword(prefix+testBcrypt[4:], "test") {
			t.Errorf("bcrypt mit %s nicht erkannt", prefix)
		}
	}

	// Frischer Argon2id-Hash: 19 MiB, 2 Durchläufe, 1 Faden.
	hash, err := HashPassword("geheim")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Errorf("Hash-Form %q", hash)
	}
	if !VerifyPassword(hash, "geheim") {
		t.Error("frischer Argon2id-Hash nicht erkannt")
	}
	if VerifyPassword(hash, "Geheim") || VerifyPassword(hash, "geheim ") {
		t.Error("Argon2id: falsches Passwort angenommen")
	}
	if again, _ := HashPassword("geheim"); again == hash {
		t.Error("zwei Hashes desselben Passworts gleich (Salz fehlt)")
	}

	// Argon2id von PHP (password_hash mit PASSWORD_ARGON2ID: 64 MiB, t=4).
	const phpArgon = "$argon2id$v=19$m=65536,t=4,p=1$WERJZnJaZnBFLlRGQm05NA$0gEMEc9M8Wl65RDmj94mLwQZuYQg/ceEjRdfNJCtCuo"
	if !VerifyPassword(phpArgon, "test") {
		t.Error("Argon2id-Hash von PHP nicht erkannt")
	}
	if VerifyPassword(phpArgon, "falsch") {
		t.Error("PHP-Argon2id: falsches Passwort angenommen")
	}

	// Unbrauchbares wird abgelehnt – ohne die Rechnung zu beginnen.
	for _, bad := range []string{
		"",
		"test",
		"PASTE_PASSWORD_HASH_HERE",
		"$1$abcdefgh$abcdefghijklmnopqrstuv", // md5crypt
		"$2x$04$r60I68m5mVq98EDvnpGXGeS1SZtBd2wWlr0dy0MuX73YPDZ27VzZy", // unbekannte bcrypt-Variante
		testBcrypt + "x",
		testBcrypt[:59],
		"$2y$31$r60I68m5mVq98EDvnpGXGeS1SZtBd2wWlr0dy0MuX73YPDZ27VzZy", // Aufwand 2^31
		"$argon2i$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g",
		"$argon2id$v=16$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g",
		"$argon2id$v=19$m=1048576,t=2,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g", // 1 GiB
		"$argon2id$v=19$m=19456,t=1000,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g",
		"$argon2id$v=19$m=19456,t=2,p=0$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g",
		"$argon2id$v=19$m=19456,t=2,p=1$$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g",
		"$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0$",
		"$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0$!!!",
		"$argon2id$v=19$m=19456,t=2$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g",
		"$argon2id$v=19$m=+19456,t=2,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g",
	} {
		if VerifyPassword(bad, "test") {
			t.Errorf("unbrauchbarer Hash %q angenommen", bad)
		}
		if passwordHashUsable(bad) {
			t.Errorf("unbrauchbarer Hash %q gilt als brauchbar", bad)
		}
	}
	for _, good := range []string{testBcrypt, otherBcrypt, hash, phpArgon} {
		if !passwordHashUsable(good) {
			t.Errorf("brauchbarer Hash %q gilt als unbrauchbar", good)
		}
	}
}
