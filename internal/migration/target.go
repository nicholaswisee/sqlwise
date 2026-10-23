package migration

import (
	"context"
	"errors"
)

// Targets is the slice of Jason's database adapter that migrations consume.
// Implementations resolve registered targets only; callers authorize first.
// TODO(jason): implement over database.Resolver and the transaction executor;
// pgtarget is the temporary stand-in.
type Targets interface {
	// Dialect reports the DBMS of a registered target without opening it.
	Dialect(ctx context.Context, databaseID string) (Dialect, error)
	// Open returns a handle for a registered target or wraps ErrTargetUnavailable.
	Open(ctx context.Context, databaseID string) (Target, error)
}

// Target is an opened registered target. Exec and QueryRow run in autocommit
// mode; Begin pins one connection for a single transaction.
type Target interface {
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
	QueryRow(ctx context.Context, sql string, args ...any) Row
	Begin(ctx context.Context) (TargetTx, error)
	Close() error
}

type Row interface {
	Scan(dest ...any) error
}

// TargetTx must wrap ErrCommitUnknown from Commit when the outcome is uncertain;
// any other Commit error means the target confirmed the rollback.
type TargetTx interface {
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

const (
	receiptTable = ReceiptSchema + ".migration_receipts"
	// The first statement of every migration transaction claims the receipt
	// row; a committed fence row makes that claim fail forever.
	insertReceipt = `INSERT INTO ` + receiptTable + ` (attempt_id, database_id, version, sql_sha256, outcome) VALUES ($1, $2, $3, $4, 'applied')`
	fenceReceipt  = `INSERT INTO ` + receiptTable + ` (attempt_id, database_id, version, sql_sha256, outcome) VALUES ($1, $2, $3, $4, 'fenced') ON CONFLICT (attempt_id) DO NOTHING`
	readReceipt   = `SELECT outcome, sql_sha256, version FROM ` + receiptTable + ` WHERE attempt_id = $1`
)

var receiptDDL = []string{
	`CREATE SCHEMA IF NOT EXISTS ` + ReceiptSchema,
	`CREATE TABLE IF NOT EXISTS ` + receiptTable + ` (
		attempt_id text PRIMARY KEY,
		database_id text NOT NULL,
		version bigint NOT NULL CHECK (version >= 1),
		sql_sha256 text NOT NULL,
		outcome text NOT NULL CHECK (outcome IN ('applied', 'fenced')),
		recorded_at timestamptz NOT NULL DEFAULT now()
	)`,
}

// ProvisionReceipts creates the reserved receipt table when it is missing. The
// target credential needs CREATE on the database only for the first run.
func ProvisionReceipts(ctx context.Context, target Target) error {
	var exists bool
	if err := target.QueryRow(ctx, `SELECT to_regclass('`+receiptTable+`') IS NOT NULL`).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	for _, ddl := range receiptDDL {
		if _, err := target.Exec(ctx, ddl); err != nil {
			return err
		}
	}
	return nil
}

type receiptOutcome int

const (
	receiptUndetermined receiptOutcome = iota
	receiptApplied
	receiptAbsent
)

// resolveReceipt decides an unfinished attempt. Inserting a committed fence
// waits for any in-flight transaction holding the claim, so "absent" is final:
// the original transaction can no longer commit.
func resolveReceipt(ctx context.Context, target Target, a Attempt) (receiptOutcome, error) {
	if err := ProvisionReceipts(ctx, target); err != nil {
		return receiptUndetermined, err
	}
	inserted, err := target.Exec(ctx, fenceReceipt, a.ID, a.DatabaseID, a.CandidateVersion, a.SQLHash)
	if err != nil {
		return receiptUndetermined, err
	}
	if inserted == 1 {
		return receiptAbsent, nil
	}
	var outcome, hash string
	var version int64
	if err := target.QueryRow(ctx, readReceipt, a.ID).Scan(&outcome, &hash, &version); err != nil {
		return receiptUndetermined, err
	}
	switch {
	case outcome == "fenced":
		return receiptAbsent, nil
	case outcome == "applied" && hash == a.SQLHash && version == a.CandidateVersion:
		return receiptApplied, nil
	}
	return receiptUndetermined, errors.New("receipt does not match attempt")
}
