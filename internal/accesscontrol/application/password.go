package application

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

const (
	passwordMinRunes = 12
	passwordMaxRunes = 1024
	usernameMaxRunes = 128

	// passwordHashPrefix marks the SHA-256 + bcrypt envelope. bcrypt alone
	// rejects passwords over 72 bytes, while the contract accepts up to 1024
	// characters; the digest keeps every accepted password in range. Legacy
	// unprefixed bcrypt hashes still verify.
	passwordHashPrefix = "$bansoko$v1$"
)

func validPassword(password string) bool {
	n := utf8.RuneCountInString(password)
	return n >= passwordMinRunes && n <= passwordMaxRunes
}

func validUsername(username string) bool {
	n := utf8.RuneCountInString(username)
	return n >= 1 && n <= usernameMaxRunes
}

func hashPassword(password string) (string, error) {
	digest := sha256.Sum256([]byte(password))
	encoded := base64.StdEncoding.EncodeToString(digest[:])
	hash, err := bcrypt.GenerateFromPassword([]byte(encoded), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return passwordHashPrefix + string(hash), nil
}

func verifyPassword(stored, password string) bool {
	if strings.HasPrefix(stored, passwordHashPrefix) {
		digest := sha256.Sum256([]byte(password))
		encoded := base64.StdEncoding.EncodeToString(digest[:])
		return bcrypt.CompareHashAndPassword([]byte(strings.TrimPrefix(stored, passwordHashPrefix)), []byte(encoded)) == nil
	}
	return bcrypt.CompareHashAndPassword([]byte(stored), []byte(password)) == nil
}

// dummyPasswordHash equalizes the unknown-username login path with a real
// account: both run one full bcrypt comparison before answering the same
// error. It is built once per process.
var dummyPasswordHash = func() string {
	hash, err := hashPassword("bansoko login timing equalizer")
	if err != nil {
		panic(err)
	}
	return hash
}()
