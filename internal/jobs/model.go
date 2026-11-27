// Package jobs is the durable PostgreSQL-backed queue and the Go worker
// runtime that executes approved requests outside HTTP requests.
package jobs

import (
	"errors"
	"time"

	"sqlwise/internal/telemetry"
)

// Status is the transport lifecycle of a job.
type Status string

const (
	Queued    Status = "queued"
	Running   Status = "running"
	Succeeded Status = "succeeded"
	Failed    Status = "failed"
	// Blocked holds a job whose target effect is partial or unknown.
	Blocked Status = "blocked"
)

// Effect is what the job did to the target, kept apart from delivery status.
type Effect string

const (
	EffectNone      Effect = "none"
	EffectCommitted Effect = "committed"
	EffectPartial   Effect = "partial"
	EffectUnknown   Effect = "unknown"
)

// Recovery operations go through the reviewed request workflow.
const (
	OpResume     = "resume"
	OpCompensate = "compensate"
)

type Job struct {
	ID                  string
	DatabaseID          string
	MigrationID         string
	RequestID           string
	ExecutionID         string
	RecoversExecutionID string
	Operation           string
	ActorID             string
	Status              Status
	Effect              Effect
	Attempts            int
	MaxAttempts         int
	NextRunAt           time.Time
	CommittedBatches    int64
	CommittedRows       int64
	TotalRows           *int64
	Error               string
	Trace               telemetry.TraceContext
	ResolvedBy          string
	// BlockedBy is the unresolved job holding this database, when waiting.
	BlockedBy     string
	CreatedAt     time.Time
	StartedAt     *time.Time
	FinishedAt    *time.Time
	UpdatedAt     time.Time
	recoveryState string
}

// Terminal reports whether the worker will not deliver the job again.
func (j Job) Terminal() bool {
	return j.Status == Succeeded || j.Status == Failed || (j.Status == Blocked && j.Effect == EffectPartial)
}

// RecoveryTarget is the execution a resume or compensation must reference.
func (j Job) RecoveryTarget() string {
	if j.RecoversExecutionID != "" {
		return j.RecoversExecutionID
	}
	return j.ExecutionID
}

// Recovery lists the operations a reviewer may request for this job.
func (j Job) Recovery() []string {
	if j.Status == Blocked && j.Effect == EffectPartial && j.ResolvedBy == "" {
		switch j.recoveryState {
		case "partially_reverted":
			return []string{OpCompensate}
		case "partially_applied", "":
		default:
			return []string{}
		}
		// A compensation job counts reverted batches; once any is reverted only compensation may continue.
		if j.Operation == OpCompensate && j.CommittedBatches > 0 {
			return []string{OpCompensate}
		}
		return []string{OpResume, OpCompensate}
	}
	return []string{}
}

// Outcome is a runner's classified result for one delivery.
type Outcome struct {
	Status  Status
	Effect  Effect
	Message string
	// Retryable marks a confirmed transient failure before any target write.
	Retryable bool
	// Deferred returns the job to the queue unchanged: nothing ran, no attempt
	// is spent, and it no longer blocks its database.
	Deferred bool
}

func Success() Outcome { return Outcome{Status: Succeeded, Effect: EffectCommitted} }

func Failure(message string) Outcome {
	return Outcome{Status: Failed, Effect: EffectNone, Message: message}
}

func Transient(message string) Outcome {
	return Outcome{Status: Failed, Effect: EffectNone, Message: message, Retryable: true}
}

func Unknown(message string) Outcome {
	return Outcome{Status: Blocked, Effect: EffectUnknown, Message: message}
}

func Deferred(message string) Outcome {
	return Outcome{Status: Queued, Effect: EffectNone, Message: message, Deferred: true}
}

func Partial(message string) Outcome {
	return Outcome{Status: Blocked, Effect: EffectPartial, Message: message}
}

var (
	ErrNotFound   = errors.New("not found")
	ErrConflict   = errors.New("conflict")
	ErrValidation = errors.New("validation failed")
)

type Error struct {
	Kind    error
	Message string
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Kind }

func fail(kind error, message string) error { return &Error{Kind: kind, Message: message} }

// Message returns the safe user-facing text of a jobs error.
func Message(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Message
	}
	return ""
}
