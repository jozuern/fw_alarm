package config

import (
	"strings"
	"testing"
)

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
