package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"sqlwise/internal/database/dialect"
)

// Batch directions; compensation is driven by the migration domain.
const (
	Forward    = "forward"
	Compensate = "compensate"
)

// MaxBatchSize bounds one batch transaction.
const MaxBatchSize = 100_000

const maxBatchValue = 4096

var (
	batchExecutions  = InternalSchema + ".batch_executions"
	batchCheckpoints = InternalSchema + ".batch_checkpoints"
	// BatchRows holds before/after images per row for safe compensation.
	BatchRows = InternalSchema + ".batch_rows"
)

var batchDDL = []string{
	`CREATE SCHEMA IF NOT EXISTS ` + InternalSchema,
	`CREATE TABLE IF NOT EXISTS ` + batchExecutions + ` (
		execution_id text PRIMARY KEY,
		database_id text NOT NULL,
		spec_sha256 text NOT NULL,
		spec jsonb NOT NULL,
		batch_size integer NOT NULL CHECK (batch_size BETWEEN 1 AND 100000),
		column_type text NOT NULL,
		upper_bound bigint,
		total_rows bigint NOT NULL CHECK (total_rows >= 0),
		completed_at timestamptz,
		created_at timestamptz NOT NULL DEFAULT now()
	)`,
	`CREATE TABLE IF NOT EXISTS ` + batchCheckpoints + ` (
		execution_id text NOT NULL REFERENCES ` + batchExecutions + `(execution_id),
		direction text NOT NULL CHECK (direction IN ('forward', 'compensate')),
		batch_no integer NOT NULL CHECK (batch_no >= 1),
		first_key bigint NOT NULL,
		last_key bigint NOT NULL,
		row_count bigint NOT NULL CHECK (row_count >= 0),
		committed_at timestamptz NOT NULL DEFAULT now(),
		PRIMARY KEY (execution_id, direction, batch_no)
	)`,
	`CREATE TABLE IF NOT EXISTS ` + BatchRows + ` (
		execution_id text NOT NULL REFERENCES ` + batchExecutions + `(execution_id),
		batch_no integer NOT NULL CHECK (batch_no >= 1),
		row_key bigint NOT NULL,
		before_value text,
		after_value text,
		PRIMARY KEY (execution_id, row_key)
	)`,
	`CREATE INDEX IF NOT EXISTS batch_rows_batch_idx ON ` + BatchRows + ` (execution_id, batch_no)`,
}

// BatchSetupSQL grants a least-privilege migration role the batch ledger.
func BatchSetupSQL(migrationRole string) string {
	role := dialect.Postgres.Quote(migrationRole)
	out := strings.Join(batchDDL, ";\n") + ";\n"
	for _, table := range []string{batchExecutions, batchCheckpoints, BatchRows} {
		out += `REVOKE ALL ON ` + table + ` FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE ON ` + table + ` TO ` + role + ";\n"
	}
	return out
}

// BatchUpdate is the supported reversible large-data template:
// UPDATE schema.table SET column = value, walked by a unique integer key.
type BatchUpdate struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
	Key    string `json:"key"`
	Column string `json:"column"`
	// Value is text cast to the column type on the target, never spliced into SQL.
	Value string `json:"value"`
}

// BatchPlan is a prepared execution with its captured scope.
type BatchPlan struct {
	ExecutionID string
	Spec        BatchUpdate
	BatchSize   int
	// UpperBound is the highest key when the scope was captured; nil for an empty table.
	UpperBound *int64
	TotalRows  int64
	columnType string
}

// BatchProgress is read from target checkpoints, never from process memory.
type BatchProgress struct {
	Batches     int64
	Rows        int64
	TotalRows   int64
	Compensated int64
	Complete    bool
}

type Checkpoint struct {
	Direction   string
	Batch       int
	FirstKey    int64
	LastKey     int64
	Rows        int64
	CommittedAt time.Time
}

// BatchTx is the part of a gate transaction a batch callback may use.
type BatchTx interface {
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
	QueryRow(ctx context.Context, sql string, args ...any) Row
}

// BatchHooks observe batch commits; BeforeCommit injects faults in tests.
type BatchHooks struct {
	Committed    func(BatchProgress)
	BeforeCommit func(batch int) error
}

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]{0,62}$`)

func (s BatchUpdate) validate() error {
	for _, name := range []string{s.Schema, s.Table, s.Key, s.Column} {
		if !identifier.MatchString(name) {
			return fail(ErrValidation, "Schema, table, key and column must be plain identifiers.")
		}
	}
	switch strings.ToLower(s.Schema) {
	case InternalSchema, "pg_catalog", "information_schema":
		return fail(ErrValidation, "System and sqlwise schemas cannot be batch updated.")
	}
	if s.Key == s.Column {
		return fail(ErrValidation, "The updated column cannot be the batch key.")
	}
	if len(s.Value) > maxBatchValue {
		return fail(ErrValidation, "The new value is too long.")
	}
	return nil
}

func (s BatchUpdate) hash(size int) (string, []byte) {
	body, _ := json.Marshal(struct {
		BatchUpdate
		Size int `json:"batch_size"`
	}{s, size})
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), body
}

func (s BatchUpdate) table() string { return dialect.Postgres.Qualify(s.Schema, s.Table) }

// ProvisionBatches creates the target batch ledger when it is missing.
func (g *Gate) ProvisionBatches(ctx context.Context) error {
	var exists bool
	if err := g.conn.QueryRow(ctx, `SELECT to_regclass('`+BatchRows+`') IS NOT NULL`).Scan(&exists); err != nil {
		return targetError(ctx, err)
	}
	if exists {
		return nil
	}
	for _, ddl := range batchDDL {
		if _, err := g.conn.Exec(ctx, ddl); err != nil {
			return targetError(ctx, err)
		}
	}
	return nil
}

// PrepareBatch validates the template against the catalog and captures its
// scope once. Preparing the same execution again returns the captured plan,
// so resume never widens the scope; a different spec is a conflict.
func (g *Gate) PrepareBatch(ctx context.Context, executionID string, spec BatchUpdate, size int) (BatchPlan, error) {
	if executionID == "" || len(executionID) > 64 {
		return BatchPlan{}, fail(ErrValidation, "An execution ID is required.")
	}
	if size < 1 || size > MaxBatchSize {
		return BatchPlan{}, fail(ErrValidation, fmt.Sprintf("Batch size must be between 1 and %d.", MaxBatchSize))
	}
	if err := spec.validate(); err != nil {
		return BatchPlan{}, err
	}
	if err := g.ProvisionBatches(ctx); err != nil {
		return BatchPlan{}, err
	}
	hash, body := spec.hash(size)
	plan := BatchPlan{ExecutionID: executionID, Spec: spec, BatchSize: size}
	var stored string
	err := g.conn.QueryRow(ctx, `SELECT spec_sha256, column_type, upper_bound, total_rows FROM `+batchExecutions+` WHERE execution_id=$1`, executionID).
		Scan(&stored, &plan.columnType, &plan.UpperBound, &plan.TotalRows)
	switch {
	case err == nil && stored != hash:
		return BatchPlan{}, fail(ErrConflict, "This execution was prepared with a different batch template.")
	case err == nil:
		return plan, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return BatchPlan{}, targetError(ctx, err)
	}
	if plan.columnType, err = g.batchColumn(ctx, spec); err != nil {
		return BatchPlan{}, err
	}
	if _, err := g.conn.Exec(ctx, `SELECT $1::text::`+plan.columnType, spec.Value); err != nil {
		return BatchPlan{}, fail(ErrValidation, "The new value is not valid for column "+spec.Column+".")
	}
	tx, err := g.conn.Begin(ctx)
	if err != nil {
		return BatchPlan{}, targetError(ctx, err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	key := dialect.Postgres.Quote(spec.Key)
	if err := tx.QueryRow(ctx, `SELECT max(`+key+`)::bigint, count(*) FROM `+spec.table()).Scan(&plan.UpperBound, &plan.TotalRows); err != nil {
		return BatchPlan{}, targetError(ctx, err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO `+batchExecutions+` (execution_id, database_id, spec_sha256, spec, batch_size, column_type, upper_bound, total_rows)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, executionID, g.databaseID, hash, body, size, plan.columnType, plan.UpperBound, plan.TotalRows); err != nil {
		return BatchPlan{}, targetError(ctx, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return BatchPlan{}, targetError(ctx, err)
	}
	return plan, nil
}

// batchColumn returns the column type once the key is a unique, non-null integer.
func (g *Gate) batchColumn(ctx context.Context, spec BatchUpdate) (string, error) {
	var keyType, columnType string
	var unique bool
	err := g.conn.QueryRow(ctx, `SELECT k.atttypid::regtype::text, format_type(c.atttypid, c.atttypmod),
			k.attnotnull AND EXISTS (SELECT 1 FROM pg_index x WHERE x.indrelid=t.oid AND x.indisunique AND x.indnatts=1
				AND x.indkey[0]=k.attnum AND x.indpred IS NULL)
		FROM pg_class t JOIN pg_namespace n ON n.oid=t.relnamespace
		JOIN pg_attribute k ON k.attrelid=t.oid AND k.attname=$3 AND k.attnum>0 AND NOT k.attisdropped
		JOIN pg_attribute c ON c.attrelid=t.oid AND c.attname=$4 AND c.attnum>0 AND NOT c.attisdropped
		WHERE n.nspname=$1 AND t.relname=$2 AND t.relkind IN ('r','p')`, spec.Schema, spec.Table, spec.Key, spec.Column).
		Scan(&keyType, &columnType, &unique)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fail(ErrNotFound, "The table, key or column does not exist.")
	}
	if err != nil {
		return "", targetError(ctx, err)
	}
	switch keyType {
	case "smallint", "integer", "bigint":
	default:
		return "", fail(ErrValidation, "The batch key must be an integer column.")
	}
	if !unique {
		return "", fail(ErrValidation, "The batch key must be NOT NULL with a single-column unique index.")
	}
	return columnType, nil
}

// CommitBatch runs work and records its checkpoint in one target transaction
// on the gate session. The checkpoint key (execution, direction, batch) is
// unique, so a redelivered batch can never commit twice. work returns a
// zero-row checkpoint when there is nothing left; nothing is recorded then.
// Commit errors wrapping ErrOutcomeUnknown leave the batch for reconciliation
// from the ledger.
func (g *Gate) CommitBatch(ctx context.Context, executionID, direction string,
	work func(ctx context.Context, tx BatchTx) (Checkpoint, error), beforeCommit func(batch int) error) (Checkpoint, error) {
	if direction != Forward && direction != Compensate {
		return Checkpoint{}, fail(ErrValidation, "Unknown batch direction.")
	}
	if g.Lost() {
		return Checkpoint{}, fail(ErrConnectionUnavailable, "The execution gate was lost.")
	}
	tx, err := g.Begin(ctx)
	if err != nil {
		return Checkpoint{}, err
	}
	rollback := func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }
	cp, err := work(ctx, tx.(*gateTx))
	if err != nil {
		rollback()
		return Checkpoint{}, batchError(ctx, err)
	}
	cp.Direction = direction
	if cp.Rows > 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO `+batchCheckpoints+` (execution_id, direction, batch_no, first_key, last_key, row_count)
			VALUES ($1, $2, $3, $4, $5, $6)`, executionID, direction, cp.Batch, cp.FirstKey, cp.LastKey, cp.Rows); err != nil {
			rollback()
			return Checkpoint{}, batchError(ctx, err)
		}
	}
	if beforeCommit != nil {
		if err := beforeCommit(cp.Batch); err != nil {
			rollback()
			return Checkpoint{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		if errors.Is(err, ErrOutcomeUnknown) {
			return cp, fail(ErrOutcomeUnknown, fmt.Sprintf("The connection failed while committing batch %d; the ledger decides whether it committed.", cp.Batch))
		}
		return Checkpoint{}, batchError(ctx, err)
	}
	cp.CommittedAt = time.Now().UTC()
	return cp, nil
}

func batchError(ctx context.Context, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fail(ErrConflict, "This batch was already committed.")
	}
	return targetError(ctx, err)
}

// RunBatches commits the remaining forward batches of a prepared plan. It
// resumes after the last committed checkpoint and stops at the first error,
// keeping every earlier batch committed.
func (g *Gate) RunBatches(ctx context.Context, plan BatchPlan, hooks BatchHooks) (BatchProgress, error) {
	if plan.columnType == "" {
		return BatchProgress{}, fail(ErrValidation, "The batch plan was not prepared.")
	}
	spec := plan.Spec
	key, column := dialect.Postgres.Quote(spec.Key), dialect.Postgres.Quote(spec.Column)
	// Identifiers come from the validated spec and the type from the catalog.
	update := `WITH batch AS (
			SELECT ` + key + `::bigint AS k, ` + column + `::text AS before FROM ` + spec.table() + `
			WHERE ($1::bigint IS NULL OR ` + key + ` > $1) AND ` + key + ` <= $2 ORDER BY ` + key + ` LIMIT $3 FOR UPDATE
		), changed AS (
			UPDATE ` + spec.table() + ` t SET ` + column + ` = $4::text::` + plan.columnType + ` FROM batch
			WHERE t.` + key + ` = batch.k RETURNING batch.k, batch.before, t.` + column + `::text AS after
		), saved AS (
			INSERT INTO ` + BatchRows + ` (execution_id, batch_no, row_key, before_value, after_value)
			SELECT $5, $6, k, before, after FROM changed RETURNING row_key
		)
		SELECT count(*), COALESCE(min(row_key), 0), COALESCE(max(row_key), 0) FROM saved`
	for {
		if plan.UpperBound == nil {
			break
		}
		cp, err := g.CommitBatch(ctx, plan.ExecutionID, Forward, func(ctx context.Context, tx BatchTx) (Checkpoint, error) {
			var cp Checkpoint
			var cursor *int64
			if err := tx.QueryRow(ctx, `SELECT COALESCE(max(batch_no), 0) + 1, max(last_key) FROM `+batchCheckpoints+`
				WHERE execution_id=$1 AND direction='forward'`, plan.ExecutionID).Scan(&cp.Batch, &cursor); err != nil {
				return cp, err
			}
			err := tx.QueryRow(ctx, update, cursor, *plan.UpperBound, plan.BatchSize, spec.Value, plan.ExecutionID, cp.Batch).
				Scan(&cp.Rows, &cp.FirstKey, &cp.LastKey)
			return cp, err
		}, hooks.BeforeCommit)
		if err != nil {
			return g.progressAfter(ctx, plan.ExecutionID, err)
		}
		if cp.Rows == 0 {
			break
		}
		if hooks.Committed != nil {
			if p, err := g.BatchProgress(ctx, plan.ExecutionID); err == nil {
				hooks.Committed(p)
			}
		}
	}
	if _, err := g.conn.Exec(ctx, `UPDATE `+batchExecutions+` SET completed_at=COALESCE(completed_at, now()) WHERE execution_id=$1`, plan.ExecutionID); err != nil {
		return g.progressAfter(ctx, plan.ExecutionID, targetError(ctx, err))
	}
	return g.BatchProgress(ctx, plan.ExecutionID)
}

// progressAfter reports durable progress with the error that stopped the run.
func (g *Gate) progressAfter(ctx context.Context, executionID string, cause error) (BatchProgress, error) {
	if g.Lost() {
		return BatchProgress{}, cause
	}
	p, err := g.BatchProgress(context.WithoutCancel(ctx), executionID)
	if err != nil {
		return BatchProgress{}, cause
	}
	return p, cause
}

// BatchProgress reads committed batches and rows from the target ledger.
func (g *Gate) BatchProgress(ctx context.Context, executionID string) (BatchProgress, error) {
	var p BatchProgress
	err := g.conn.QueryRow(ctx, `SELECT e.total_rows, e.completed_at IS NOT NULL,
			count(c.batch_no) FILTER (WHERE c.direction='forward'),
			COALESCE(sum(c.row_count) FILTER (WHERE c.direction='forward'), 0),
			count(c.batch_no) FILTER (WHERE c.direction='compensate')
		FROM `+batchExecutions+` e LEFT JOIN `+batchCheckpoints+` c ON c.execution_id=e.execution_id
		WHERE e.execution_id=$1 GROUP BY e.execution_id`, executionID).Scan(&p.TotalRows, &p.Complete, &p.Batches, &p.Rows, &p.Compensated)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, fail(ErrNotFound, "No batch execution was prepared for this execution.")
	}
	if err != nil {
		return p, targetError(ctx, err)
	}
	return p, nil
}

// Checkpoints lists committed batches of one direction, newest first, which
// is the order compensation walks them.
func (g *Gate) Checkpoints(ctx context.Context, executionID, direction string) ([]Checkpoint, error) {
	rows, err := g.conn.Query(ctx, `SELECT direction, batch_no, first_key, last_key, row_count, committed_at FROM `+batchCheckpoints+`
		WHERE execution_id=$1 AND direction=$2 ORDER BY batch_no DESC`, executionID, direction)
	if err != nil {
		return nil, targetError(ctx, err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Checkpoint, error) {
		var c Checkpoint
		return c, row.Scan(&c.Direction, &c.Batch, &c.FirstKey, &c.LastKey, &c.Rows, &c.CommittedAt)
	})
	return out, targetError(ctx, err)
}
