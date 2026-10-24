package database

import (
	"strings"
	"time"
)

type TLSMode string

const (
	TLSDisable    TLSMode = "disable"
	TLSRequire    TLSMode = "require"
	TLSVerifyFull TLSMode = "verify-full"
)

type Connection struct {
	DatabaseID string
	Name       string
	Active     bool
	DBMS       DBMS
	Host       string
	Port       int
	Database   string
	Username   string
	Password   string
	TLS        TLSMode
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (c Connection) Validate() error {
	if _, err := ParseDBMS(string(c.DBMS)); err != nil {
		return err
	}
	switch {
	case strings.TrimSpace(c.Host) == "":
		return fail(ErrValidation, "Host is required.")
	case c.Port < 1 || c.Port > 65535:
		return fail(ErrValidation, "Port must be from 1 to 65535.")
	case c.Database == "":
		return fail(ErrValidation, "Database name is required.")
	case c.Username == "":
		return fail(ErrValidation, "Username is required.")
	}
	switch c.TLS {
	case TLSDisable, TLSRequire, TLSVerifyFull:
		return nil
	}
	return fail(ErrValidation, "TLS mode must be disable, require or verify-full.")
}
