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
	key := argon2.IDKey([]byte(password), salt, passwordPasses, passwordMemory, passwordThreads, 32)
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
	values := [3]uint64{}
	for i, prefix := range []string{"m=", "t=", "p="} {
		if !strings.HasPrefix(params[i], prefix) {
			return false, ErrPasswordHash
		}
		value, err := strconv.ParseUint(strings.TrimPrefix(params[i], prefix), 10, 32)
		if err != nil {
			return false, ErrPasswordHash
		}
		values[i] = value
	}
	if values[0] < 19*1024 || values[0] > passwordMemory || values[1] < 1 || values[1] > passwordPasses || values[2] < 1 || values[2] > passwordThreads {
		return false, ErrPasswordHash
	}
	salt, saltErr := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	key, keyErr := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if saltErr != nil || keyErr != nil || len(salt) != 16 || len(key) != 32 {
		return false, ErrPasswordHash
	}
	actual := argon2.IDKey([]byte(password), salt, uint32(values[1]), uint32(values[0]), uint8(values[2]), uint32(len(key)))
	return subtle.ConstantTimeCompare(key, actual) == 1, nil
}
