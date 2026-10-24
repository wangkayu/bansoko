package application

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestPasswordEnvelopeRoundTripsAcceptedLengths(t *testing.T) {
	for _, password := range []string{
		"twelve chars",
		strings.Repeat("a", 72),
		strings.Repeat("long passphrase ", 20), // 320 bytes: past bcrypt's own limit
		strings.Repeat("密", 24),                // 72 bytes of multibyte text
	} {
		if !validPassword(password) {
			t.Fatalf("password of %d runes must be accepted", len([]rune(password)))
		}
		hash, err := hashPassword(password)
		if err != nil {
			t.Fatalf("hash password: %v", err)
		}
		if !strings.HasPrefix(hash, passwordHashPrefix) {
			t.Fatalf("hash %q does not carry the envelope prefix", hash)
		}
		if !verifyPassword(hash, password) {
			t.Fatalf("password of %d bytes did not verify", len(password))
		}
		if verifyPassword(hash, password+"x") {
			t.Fatalf("password of %d bytes verified with a wrong value", len(password))
		}
	}
}

func TestLegacyBcryptHashStillVerifies(t *testing.T) {
	legacy, err := bcrypt.GenerateFromPassword([]byte("legacy password 12"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyPassword(string(legacy), "legacy password 12") {
		t.Fatal("an unprefixed bcrypt hash must keep authenticating")
	}
	if verifyPassword(string(legacy), "wrong password 12") {
		t.Fatal("a wrong password must not verify")
	}
}

func TestPasswordAndUsernameLimitsCountRunes(t *testing.T) {
	if validPassword(strings.Repeat("a", passwordMinRunes-1)) {
		t.Fatal("11 characters must be rejected")
	}
	if !validPassword(strings.Repeat("a", passwordMinRunes)) {
		t.Fatal("12 characters must be accepted")
	}
	if validPassword(strings.Repeat("a", passwordMaxRunes+1)) {
		t.Fatal("1025 characters must be rejected")
	}
	if !validUsername(strings.Repeat("名", usernameMaxRunes)) {
		t.Fatal("128 multibyte characters must be an accepted username")
	}
	if validUsername(strings.Repeat("a", usernameMaxRunes+1)) {
		t.Fatal("129 characters must be rejected")
	}
}

func TestUnknownSchemeFailsClosed(t *testing.T) {
	if verifyPassword("$bansoko$v2$not-a-hash", "whatever password") {
		t.Fatal("an unknown hash envelope must never verify")
	}
	if verifyPassword("", "whatever password") {
		t.Fatal("an empty stored hash must never verify")
	}
}
