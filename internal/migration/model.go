package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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

// Operation is what an execution does with a migration: up applies it the
// first time, rollback runs its SQL down, forward re-applies its SQL up.
type Operation string

const (
	OpUp       Operation = "up"
	OpRollback Operation = "rollback"
	OpForward  Operation = "forward"
)

// RequestStatus is the review state of an action request.
type RequestStatus string

const (
	RequestDraft         RequestStatus = "draft"
	RequestPendingReview RequestStatus = "pending_review"
	RequestApproved      RequestStatus = "approved"
	RequestRejected      RequestStatus = "rejected"
	RequestQueued        RequestStatus = "queued"
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
	ID         string
	DatabaseID string
	Title      string
	SQL        string
	// SQLDown is empty when the migration is irreversible or not yet defined.
	SQLDown string
	// IrreversibleReason is set exactly when the migration is irreversible.
	IrreversibleReason string
	Dialect            Dialect
	Revision           int64
	CreatedBy          string
	CreatedByName      string
	UpdatedBy          string
	UpdatedByName      string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	AppliedVersion     int64
	LastStatus         Status
}

func (d Draft) Applied() bool { return d.AppliedVersion > 0 }

// Executed reports a definition that ran at least once, failed runs included;
// it is frozen from then on.
func (d Draft) Executed() bool { return d.LastStatus != "" }

func (d Draft) Irreversible() bool { return d.IrreversibleReason != "" }

// Complete reports a definition that may be reviewed: SQL down or an
// irreversible reason.
func (d Draft) Complete() bool { return d.SQLDown != "" || d.Irreversible() }

// DefinitionHash binds reviews to the exact up, down and reversibility.
func (d Draft) DefinitionHash() string {
	return HashSQL(fmt.Sprintf("up:%d:%s\ndown:%d:%s\nirreversible:%d:%s", len(d.SQL), d.SQL, len(d.SQLDown), d.SQLDown, len(d.IrreversibleReason), d.IrreversibleReason))
}

// SQLHash is the hex SHA-256 of the exact SQL text shown and executed.
func (d Draft) SQLHash() string { return HashSQL(d.SQL) }

type Attempt struct {
	ID             string
	Seq            int64
	DatabaseID     string
	MigrationID    string
	MigrationTitle string
	Operation      Operation
	RequestID      string
	// CancelsExecutionID is the successful execution a rollback cancels.
	CancelsExecutionID string
	DraftRevision      int64
	CandidateVersion   int64
	AppliedVersion     int64
	SQL                string
	SQLHash            string
	Dialect            Dialect
	StatementCount     int
	ExecutorID         string
	ExecutorName       string
	Status             Status
	Error              string
	FailedStatement    int
	StartedAt          time.Time
	FinishedAt         *time.Time
	ReconciledAt       *time.Time
}

type DraftInput struct {
	Title string
	SQL   string
	// SQLDown is optional while drafting; review needs it or Irreversible.
	SQLDown            string
	Irreversible       bool
	IrreversibleReason string
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
	Head           Head
	Drafts         []Draft
	Attempts       []Attempt
	// Committed holds successful executions only, oldest first.
	Committed []Attempt
}

type Detail struct {
	Database       Database
	CurrentVersion int64
	Head           Head
	Draft          Draft
	Attempts       []Attempt
	Requests       []Request
	// Eligible lists the operations a new request may ask for now.
	Eligible []Operation
	Blocked  string
	// BlockedBy is the attempt an editor may mark as reconciled, if any.
	BlockedBy *Attempt
}

// Reconcilable reports attempts that stay blocked until an editor confirms
// the target state: partially applied or unknown MySQL executions.
func (a Attempt) Reconcilable() bool {
	return a.Dialect == MySQL && a.ReconciledAt == nil && (a.Status == StatusPartiallyApplied || a.Status == StatusUnknown)
}

// Request asks to run one operation of one definition revision at one
// history position. It needs every authorized group's approval to be queued.
type Request struct {
	ID                 string
	DatabaseID         string
	MigrationID        string
	MigrationTitle     string
	Operation          Operation
	DefinitionRevision int64
	DefinitionHash     string
	ExpectedVersion    int64
	CancelsExecutionID string
	RequestedBy        string
	RequestedByName    string
	Status             RequestStatus
	ReviewRound        int
	PolicyHash         string
	ExecutionID        string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	// Filled on reads: why the request can no longer be approved or queued,
	// the groups that must approve, and the execution once it exists.
	Stale     string
	Approvals []GroupApproval
	Execution *Attempt
}

// GroupApproval is one authorized group's decision in the current round.
type GroupApproval struct {
	GroupID      string
	GroupName    string
	Permission   string
	LeaderID     string
	LeaderName   string
	LeaderActive bool
	Decision     string
	DecidedBy    string
	DecidedAt    *time.Time
	// CanDecide is true when the caller leads this group and may decide now.
	CanDecide bool
}

type RequestInput struct {
	Operation          Operation
	ExpectedRevision   int64
	ExpectedDefinition string
	ExpectedVersion    int64
}

// Execution identifies a consumed request for the job that will run it.
type Execution struct {
	ID          string
	RequestID   string
	DatabaseID  string
	MigrationID string
	Operation   Operation
	ActorID     string
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
	in.SQLDown = strings.ReplaceAll(in.SQLDown, "\r\n", "\n")
	if strings.TrimSpace(in.SQLDown) == "" {
		in.SQLDown = ""
	}
	if !utf8.ValidString(in.SQLDown) || strings.ContainsRune(in.SQLDown, 0) || len(in.SQLDown) > MaxSQLBytes {
		return in, fail(ErrValidation, "SQL down must be valid UTF-8 text of at most 256 KiB.")
	}
	in.IrreversibleReason = strings.TrimSpace(in.IrreversibleReason)
	if !in.Irreversible {
		in.IrreversibleReason = ""
		return in, nil
	}
	if in.SQLDown != "" {
		return in, fail(ErrValidation, "An irreversible migration cannot have SQL down.")
	}
	if !utf8.ValidString(in.IrreversibleReason) || strings.ContainsRune(in.IrreversibleReason, 0) || utf8.RuneCountInString(in.IrreversibleReason) < 1 || utf8.RuneCountInString(in.IrreversibleReason) > 500 {
		return in, fail(ErrValidation, "Explain why the migration is irreversible in 1 to 500 characters.")
	}
	return in, nil
}

func validID(id string) bool {
	if id == "" || len(id) > 128 || !utf8.ValidString(id) {
		return false
	}
	return !strings.ContainsFunc(id, func(r rune) bool { return r < 0x20 || r == 0x7f })
}
