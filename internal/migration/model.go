package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

type Dialect string

const (
	PostgreSQL Dialect = "postgresql"
	MySQL      Dialect = "mysql"
)

// Status is the attempt state; running moves to exactly one outcome, and only
// unknown may later be resolved by receipt reconciliation.
type Status string

const (
	StatusRunning          Status = "running"
	StatusSucceeded        Status = "succeeded"
	StatusFailed           Status = "failed"
	StatusPartiallyApplied Status = "partially_applied"
	StatusUnknown          Status = "unknown"
)

// Domain error kinds. Error.Message is safe to show; handlers map kinds to HTTP.
var (
	ErrNotFound           = errors.New("not found")
	ErrValidation         = errors.New("validation failed")
	ErrConflict           = errors.New("conflict")
	ErrBusy               = errors.New("another migration is running")
	ErrUnsupportedDialect = errors.New("unsupported dialect")
	ErrTargetUnavailable  = errors.New("target unavailable")
	ErrExecutionFailed    = errors.New("migration failed")
	ErrTimeout            = errors.New("migration timed out")
	ErrOutcomeUnknown     = errors.New("migration outcome unknown")
	ErrRecoveryRequired   = errors.New("recovery required")
	// ErrCommitUnknown must be wrapped by TargetTx.Commit when the commit
	// request may have reached the target but no reply confirmed it.
	ErrCommitUnknown = errors.New("commit outcome unknown")
)

type Error struct {
	Kind    error
	Message string
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Kind }

func fail(kind error, message string) error { return &Error{Kind: kind, Message: message} }

// Message returns the user-safe message of a domain error.
func Message(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Message
	}
	return ""
}

type Database struct {
	ID      string
	Name    string
	CanEdit bool
}

type Draft struct {
	ID             string
	DatabaseID     string
	Title          string
	SQL            string
	Dialect        Dialect
	Revision       int64
	CreatedBy      string
	CreatedByName  string
	UpdatedBy      string
	UpdatedByName  string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	AppliedVersion int64
	LastStatus     Status
}

func (d Draft) Applied() bool { return d.AppliedVersion > 0 }

// SQLHash is the hex SHA-256 of the exact SQL text shown and executed.
func (d Draft) SQLHash() string { return HashSQL(d.SQL) }

type Attempt struct {
	ID               string
	Seq              int64
	DatabaseID       string
	MigrationID      string
	MigrationTitle   string
	DraftRevision    int64
	CandidateVersion int64
	AppliedVersion   int64
	SQL              string
	SQLHash          string
	Dialect          Dialect
	StatementCount   int
	ExecutorID       string
	ExecutorName     string
	Status           Status
	Error            string
	FailedStatement  int
	StartedAt        time.Time
	FinishedAt       *time.Time
	ReconciledAt     *time.Time
}

type DraftInput struct {
	Title string
	SQL   string
}

type UpdateInput struct {
	DraftInput
	ExpectedRevision int64
}

type ExecuteInput struct {
	ExpectedRevision int64
	ExpectedVersion  int64
	ExpectedSQLHash  string
}

type History struct {
	Database       Database
	CurrentVersion int64
	Drafts         []Draft
	Attempts       []Attempt
}

type Detail struct {
	Database       Database
	CurrentVersion int64
	Draft          Draft
	Attempts       []Attempt
	Blocked        string
	// BlockedBy is the attempt an editor may mark as reconciled, if any.
	BlockedBy *Attempt
}

// Reconcilable reports attempts that stay blocked until an editor confirms
// the target state: partially applied or unknown MySQL executions.
func (a Attempt) Reconcilable() bool {
	return a.Dialect == MySQL && a.ReconciledAt == nil && (a.Status == StatusPartiallyApplied || a.Status == StatusUnknown)
}

func HashSQL(sql string) string {
	sum := sha256.Sum256([]byte(sql))
	return hex.EncodeToString(sum[:])
}

func (in DraftInput) normalize() (DraftInput, error) {
	in.Title = strings.TrimSpace(in.Title)
	if !utf8.ValidString(in.Title) || strings.ContainsRune(in.Title, 0) || utf8.RuneCountInString(in.Title) < 1 || utf8.RuneCountInString(in.Title) > 200 {
		return in, fail(ErrValidation, "Title must be 1 to 200 characters.")
	}
	// Browsers submit CRLF; storing LF keeps the hash stable across edits.
	in.SQL = strings.ReplaceAll(in.SQL, "\r\n", "\n")
	if strings.TrimSpace(in.SQL) == "" {
		return in, fail(ErrValidation, "SQL is required.")
	}
	if !utf8.ValidString(in.SQL) || strings.ContainsRune(in.SQL, 0) {
		return in, fail(ErrValidation, "SQL must be valid UTF-8 text.")
	}
	if len(in.SQL) > MaxSQLBytes {
		return in, fail(ErrValidation, "SQL must be at most 256 KiB.")
	}
	return in, nil
}

func validID(id string) bool {
	if id == "" || len(id) > 128 || !utf8.ValidString(id) {
		return false
	}
	return !strings.ContainsFunc(id, func(r rune) bool { return r < 0x20 || r == 0x7f })
}
