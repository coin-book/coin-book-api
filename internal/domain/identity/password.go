package identity

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/rotisserie/eris"
	"golang.org/x/crypto/argon2"
)

const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
	saltLen      = 16
)

// hashing bounds concurrent argon2 work: each call pins 64 MiB, so unlimited
// parallel sign-in attempts would otherwise exhaust memory.
var hashing = make(chan struct{}, maxConcurrentHashes)

const maxConcurrentHashes = 4

func hashPassword(password string) (string, error) {
	hashing <- struct{}{}
	defer func() { <-hashing }()
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", eris.Wrap(err, "generate salt")
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyPassword(encoded, password string) bool {
	hashing <- struct{}{}
	defer func() { <-hashing }()
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var memory uint32
	var iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, threads, uint32(len(want))) //nolint:gosec // G115: length of a 32-byte key
	return subtle.ConstantTimeCompare(got, want) == 1
}
