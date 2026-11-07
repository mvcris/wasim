package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// PBKDF2 is in the standard library, unlike bcrypt and argon2, so hashing
// needs no new module.
const (
	hashScheme = "pbkdf2-sha256"
	saltBytes  = 16
	keyBytes   = 32
)

// hashIterations follows OWASP's recommendation for PBKDF2-HMAC-SHA256. It is
// a variable only so this package's tests can make hashing cheap; every hash
// records its own count, so checking does not depend on it.
var hashIterations = 600_000

var errMalformedHash = errors.New("malformed password hash")

// hashPassword returns a self-describing hash: scheme, iterations, salt, key.
// Storing the iteration count lets it grow later without breaking old hashes.
func hashPassword(password string) (string, error) {
	salt := make([]byte, saltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, hashIterations, keyBytes)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	encoding := base64.RawStdEncoding
	return strings.Join([]string{
		hashScheme,
		strconv.Itoa(hashIterations),
		encoding.EncodeToString(salt),
		encoding.EncodeToString(key),
	}, "$"), nil
}

// checkPassword reports whether password matches a hash from hashPassword.
func checkPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != hashScheme {
		return false, errMalformedHash
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 1 {
		return false, errMalformedHash
	}
	encoding := base64.RawStdEncoding
	salt, err := encoding.DecodeString(parts[2])
	if err != nil {
		return false, errMalformedHash
	}
	want, err := encoding.DecodeString(parts[3])
	if err != nil || len(want) == 0 {
		return false, errMalformedHash
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
	if err != nil {
		return false, fmt.Errorf("hash password: %w", err)
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
