package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters. Values chosen for a good balance of security and
// responsiveness on typical phone-era devices / small servers.
const (
	argonTime    uint32 = 1
	argonMemory  uint32 = 64 * 1024
	argonThreads uint8  = 4
	argonKeyLen  uint32 = 32
	argonSaltLen        = 16
)

var (
	errHashFormat = errors.New("invalid password hash format")
	errHashParams = errors.New("invalid password hash parameters")
)

// HashPassword derives an Argon2id hash for the given plaintext password.
// The returned string is self-describing: $argon2id$v=19$m=...,t=...,p=...$salt$hash
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		enc.EncodeToString(salt), enc.EncodeToString(hash)), nil
}

// VerifyPassword checks a plaintext password against an encoded Argon2id hash.
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	// parts: ["", "argon2id", "v=19", "m=65536,t=1,p=4", "salt", "hash"]
	if len(parts) != 6 {
		return false, errHashFormat
	}
	if parts[1] != "argon2id" {
		return false, errHashFormat
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, errHashFormat
	}
	if version != argon2.Version {
		return false, errHashFormat
	}

	var memory uint32
	var timeCost uint32
	var threads uint8
	for _, param := range strings.Split(parts[3], ",") {
		var err error
		switch {
		case strings.HasPrefix(param, "m="):
			memory, err = parseUint32(param[2:])
		case strings.HasPrefix(param, "t="):
			timeCost, err = parseUint32(param[2:])
		case strings.HasPrefix(param, "p="):
			var v uint64
			v, err = strconv.ParseUint(param[2:], 10, 8)
			threads = uint8(v)
		default:
			err = errHashParams
		}
		if err != nil {
			return false, errHashParams
		}
	}

	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[4])
	if err != nil {
		return false, errHashFormat
	}
	expected, err := enc.DecodeString(parts[5])
	if err != nil {
		return false, errHashFormat
	}

	computed := argon2.IDKey([]byte(password), salt, timeCost, memory, threads, uint32(len(expected)))
	if subtle.ConstantTimeCompare(computed, expected) != 1 {
		return false, nil
	}
	return true, nil
}

func parseUint32(s string) (uint32, error) {
	v, err := strconv.ParseUint(s, 10, 32)
	return uint32(v), err
}
