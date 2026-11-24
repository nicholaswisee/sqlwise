package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	passwordMemory  = 64 * 1024
	passwordPasses  = 3
	passwordThreads = 4
	passwordKeySize = 32
)

var ErrPasswordHash = errors.New("invalid stored password hash")

func HashPassword(password string) (string, error) {
	if utf8.RuneCountInString(password) < 12 || len(password) > 128 || !utf8.ValidString(password) {
		return "", ErrInvalidInput
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, passwordPasses, passwordMemory, passwordThreads, passwordKeySize)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version,
		passwordMemory, passwordPasses, passwordThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func VerifyPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" || len(password) > 128 {
		return false, ErrPasswordHash
	}
	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return false, ErrPasswordHash
	}
	memoryText, hasMemory := strings.CutPrefix(params[0], "m=")
	passesText, hasPasses := strings.CutPrefix(params[1], "t=")
	threadsText, hasThreads := strings.CutPrefix(params[2], "p=")
	if !hasMemory || !hasPasses || !hasThreads {
		return false, ErrPasswordHash
	}
	memory, memoryErr := strconv.ParseUint(memoryText, 10, 32)
	passes, passesErr := strconv.ParseUint(passesText, 10, 32)
	threads, threadsErr := strconv.ParseUint(threadsText, 10, 8)
	if memoryErr != nil || passesErr != nil || threadsErr != nil ||
		memory < 19*1024 || memory > passwordMemory || passes < 1 || passes > passwordPasses ||
		threads < 1 || threads > passwordThreads {
		return false, ErrPasswordHash
	}
	salt, saltErr := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	key, keyErr := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if saltErr != nil || keyErr != nil || len(salt) != 16 || len(key) != passwordKeySize {
		return false, ErrPasswordHash
	}
	actual := argon2.IDKey([]byte(password), salt, uint32(passes), uint32(memory), uint8(threads), passwordKeySize)
	return subtle.ConstantTimeCompare(key, actual) == 1, nil
}
