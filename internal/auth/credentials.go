package auth

import (
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalidInput = errors.New("invalid username or password")
	ErrCredentials  = errors.New("invalid credentials")
	ErrAdminExists  = errors.New("an administrator already exists")
	ErrSession      = errors.New("invalid or expired session")
	ErrCSRF         = errors.New("invalid CSRF token")
	ErrBusy         = errors.New("authentication is busy")
)

var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{2,63}$`)

type Credentials struct {
	username string
	password string
}

func ParseCredentials(username, password string) (Credentials, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if !usernamePattern.MatchString(username) || len(password) == 0 || len(password) > 128 || !utf8.ValidString(password) {
		return Credentials{}, ErrInvalidInput
	}
	return Credentials{username: username, password: password}, nil
}

type Principal struct {
	UserID          string
	Username        string
	IsPlatformAdmin bool
}
