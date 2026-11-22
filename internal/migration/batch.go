package migration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"sqlwise/internal/access"
)

// MaxBatchSize bounds one batch transaction; it matches the target runtime.
const MaxBatchSize = 100_000

// BatchSpec is the supported reversible large-data template:
// UPDATE schema.table SET column = value, walked by a unique integer key.
type BatchSpec struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
	Key    string `json:"key"`
	Column string `json:"column"`
	Value  string `json:"value"`
}

// BatchResult is durable progress read from the target batch ledger.
type BatchResult struct {
	Batches     int64
	Rows        int64
	TotalRows   int64
	Compensated int64
}

// BatchRunner runs batch templates on the execution gate the worker holds;
// the jobs runtime implements it. Forward resumes an execution's ledger.
type BatchRunner interface {
	Forward(ctx context.Context, executionID string, spec BatchSpec, size int) (BatchResult, error)
	Compensate(ctx context.Context, executionID string) (BatchResult, error)
}

var batchIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]{0,62}$`)

func (b BatchSpec) validate(size int) error {
	for _, name := range []string{b.Schema, b.Table, b.Key, b.Column} {
		if !batchIdentifier.MatchString(name) {
			return fail(ErrValidation, "Batch schema, table, key and column must be plain identifiers.")
		}
	}
	if strings.EqualFold(b.Schema, ReceiptSchema) || strings.EqualFold(b.Schema, "pg_catalog") || strings.EqualFold(b.Schema, "information_schema") {
		return fail(ErrValidation, "System and sqlwise schemas cannot be batch updated.")
	}
	if b.Key == b.Column {
		return fail(ErrValidation, "The updated column cannot be the batch key.")
	}
	if !utf8.ValidString(b.Value) || strings.ContainsRune(b.Value, 0) || len(b.Value) > 4096 {
		return fail(ErrValidation, "The batch value must be valid text of at most 4 KiB.")
	}
	if size < 1 || size > MaxBatchSize {
		return fail(ErrValidation, fmt.Sprintf("Batch size must be between 1 and %d.", MaxBatchSize))
	}
	return nil
}

// render is the exact statement reviewers approve; execution binds the value as a parameter.
func (b BatchSpec) render() string {
	return "UPDATE " + pgx.Identifier{b.Schema, b.Table}.Sanitize() + " SET " + pgx.Identifier{b.Column}.Sanitize() +
		" = '" + strings.ReplaceAll(b.Value, "'", "''") + "';"
}

// recoverable checks that r may resume or compensate its interrupted execution.
func (s *Service) recoverable(ctx context.Context, d Draft, r Request) error {
	if d.Batch == nil {
		return fail(ErrConflict, "Only batch migrations can be resumed or compensated.")
	}
	if !validID(r.RecoversExecutionID) {
		return fail(ErrValidation, "Choose the interrupted execution to recover.")
	}
	a, found, err := s.store.attemptByID(ctx, r.RecoversExecutionID)
	if err != nil {
		return err
	}
	if !found || a.MigrationID != d.ID || a.DatabaseID != r.DatabaseID {
		return fail(ErrNotFound, "The interrupted execution was not found for this migration.")
	}
	if !a.Batch || (a.Status != StatusPartiallyApplied && a.Status != StatusPartiallyReverted) {
		return fail(ErrConflict, "Only a partially applied batch execution can be resumed or compensated.")
	}
	// A resume would skip reverted batches yet record the migration as complete.
	if a.Status == StatusPartiallyReverted && r.Operation == OpResume {
		return fail(ErrConflict, "This execution was partly compensated, so it can only be compensated further, not resumed.")
	}
	return nil
}

// runBatch runs a batch-mode up or forward. Committed batches survive a
// failure, so the execution becomes partially applied, never failed.
func (s *Service) runBatch(ctx context.Context, batches BatchRunner, a Attempt, d Draft) (Attempt, error) {
	if batches == nil {
		a.Status, a.Error = StatusFailed, "This executor cannot run batch migrations. Nothing was executed."
		return a, ErrUnsupportedDialect
	}
	res, err := batches.Forward(ctx, a.ID, *d.Batch, d.BatchSize)
	if err == nil {
		a.Status = StatusSucceeded
		return a, nil
	}
	message := batchMessage(err)
	if res.Batches > 0 || errors.Is(err, ErrOutcomeUnknown) {
		a.Status = StatusPartiallyApplied
		a.Error = fmt.Sprintf("%s %d batches (%d rows) stay committed; an approved resume or compensation must settle this execution.", message, res.Batches, res.Rows)
		return a, ErrRecoveryRequired
	}
	a.Status, a.Error = StatusFailed, message+" No batch was committed."
	if errors.Is(err, ErrValidation) || errors.Is(err, ErrNotFound) {
		return a, ErrValidation
	}
	return a, ErrExecutionFailed
}

// recover resumes or compensates a partial batch execution under a fresh
// approval. Both are idempotent against the target ledger, so a redelivery
// continues where the last delivery stopped.
func (s *Service) recover(ctx context.Context, batches BatchRunner, r Request) (Attempt, error) {
	if batches == nil {
		return Attempt{}, fail(ErrUnsupportedDialect, "This executor cannot recover batch migrations. Nothing was executed.")
	}
	a, found, err := s.store.attemptByID(ctx, r.RecoversExecutionID)
	if err != nil {
		return Attempt{}, err
	}
	if !found {
		return Attempt{}, fail(ErrNotFound, "The interrupted execution was not found.")
	}
	if a.RecoveredBy == r.ID {
		return a, outcome(a)
	}
	var d Draft
	err = pgx.BeginFunc(ctx, s.store.pool, func(tx pgx.Tx) error {
		groups, err := s.approved(ctx, tx, r)
		if err != nil {
			return err
		}
		if actorPermission(groups, r.RequestedBy) < access.Edit {
			return fail(access.ErrPermissionDenied, "The requester no longer has edit access to this database. Nothing was executed.")
		}
		d, err = s.fresh(ctx, tx, r)
		return err
	})
	if err != nil {
		slog.WarnContext(ctx, "migration recovery refused", "event", "migration.recovery.refused", "database", r.DatabaseID,
			"migration_id", r.MigrationID, "request_id", r.ID, "execution_id", a.ID, "operation", string(r.Operation), "actor", r.RequestedBy)
		return Attempt{}, err
	}
	slog.InfoContext(ctx, "migration recovery started", "event", "migration.recovery.start", "database", r.DatabaseID,
		"migration_id", r.MigrationID, "request_id", r.ID, "execution_id", a.ID, "operation", string(r.Operation), "actor", r.RequestedBy)
	var res BatchResult
	if r.Operation == OpResume {
		res, err = batches.Forward(ctx, a.ID, *d.Batch, d.BatchSize)
	} else {
		res, err = batches.Compensate(ctx, a.ID)
	}
	settled := ""
	switch {
	case err == nil && r.Operation == OpResume:
		a.Status, a.Error, settled = StatusSucceeded, "", r.ID
	case err == nil:
		a.Status, settled = StatusReverted, r.ID
		a.Error = fmt.Sprintf("Compensated: %d batches (%d rows) were restored in reverse order.", res.Compensated, res.Rows)
	case r.Operation == OpResume:
		a.Error = fmt.Sprintf("Resume stopped: %s %d batches (%d of %d rows) are committed.", batchMessage(err), res.Batches, res.Rows, res.TotalRows)
	default:
		if res.Compensated > 0 {
			a.Status = StatusPartiallyReverted
		}
		a.Error = fmt.Sprintf("Compensation stopped: %s %d of %d batches are reverted.", batchMessage(err), res.Compensated, res.Batches)
	}
	if ok, serr := s.store.settleBatch(context.WithoutCancel(ctx), a, settled, s.now().UTC()); serr != nil || !ok {
		// The ledger stays authoritative; redelivery settles it again.
		return a, fail(ErrTargetUnavailable, "The recovery finished on the target but could not be recorded yet.")
	}
	slog.InfoContext(ctx, "migration recovery finished", "event", "migration.recovery.result", "database", r.DatabaseID,
		"migration_id", r.MigrationID, "request_id", r.ID, "execution_id", a.ID, "operation", string(r.Operation), "status", string(a.Status))
	switch {
	case err == nil:
		return a, nil
	case errors.Is(err, ErrTargetUnavailable):
		return a, fail(ErrTargetUnavailable, a.Error)
	}
	return a, fail(ErrRecoveryRequired, a.Error)
}

func batchMessage(err error) string {
	if m := Message(err); m != "" {
		return m
	}
	return "The batch run stopped."
}
