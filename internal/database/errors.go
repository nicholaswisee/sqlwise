package database

import (
	"context"
	"errors"
)

var (
	ErrNotFound              = errors.New("not found")
	ErrValidation            = errors.New("validation failed")
	ErrConflict              = errors.New("conflict")
	ErrUnsupportedDialect    = errors.New("unsupported dialect")
	ErrUnsupportedStatement  = errors.New("unsupported statement")
	ErrConnectionUnavailable = errors.New("connection unavailable")
	ErrTimeout               = errors.New("operation timed out")
	ErrOutcomeUnknown        = errors.New("outcome unknown")
)

type Error struct {
	Kind    error
	Message string
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Kind }

func fail(kind error, message string) error { return &Error{Kind: kind, Message: message} }

func Message(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Message
	}
	return ""
}

func Unavailable() error {
	return fail(ErrConnectionUnavailable, "The database connection is unavailable.")
}

func FromContext(ctx context.Context) error {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fail(ErrTimeout, "The database did not respond in time.")
	case ctx.Err() != nil:
		return Unavailable()
	}
	return nil
}
