// Package security holds pure credential rules: password hashing and checks.
// It has no dependencies, storage or command lifecycle.
package security

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

const passwordIterations = 600000

// ValidPassword bounds password length before any costly derivation.
func ValidPassword(p string) bool { return len(p) >= 8 && len(p) <= 128 }

// DerivePassword returns the self-describing PBKDF2-SHA256 hash for salt.
func DerivePassword(password string, salt []byte) string {
	key, _ := pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
	return "pbkdf2-sha256$600000$" + hex.EncodeToString(salt) + "$" + hex.EncodeToString(key)
}

// PasswordMatches compares in constant time and rejects unknown hash formats.
func PasswordMatches(password, hash string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" || parts[1] != "600000" {
		return false
	}
	salt, e := hex.DecodeString(parts[2])
	if e != nil || len(salt) != 16 {
		return false
	}
	actual := DerivePassword(password, salt)
	return subtle.ConstantTimeCompare([]byte(actual), []byte(hash)) == 1
}
