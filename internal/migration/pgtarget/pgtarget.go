// Package pgtarget implements migration.Targets and schemadiff.Source over
// plain pgx pools for tests. Its Commit hook models lost acknowledgements and
// Opens proves denied requests never reach a target. The server uses the
// internal/database adapter instead.
package pgtarget

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"sqlwise/internal/migration"
	"sqlwise/internal/schemadiff"
)

// CommitHook replaces a real commit; it receives the real commit and rollback
// so tests can model lost acknowledgements or lost requests.
type CommitHook func(ctx context.Context, commit, rollback func(context.Context) error) error

// Registry holds one caller-owned pool per registered target ID.
type Registry struct {
	mu    sync.Mutex
	pools map[string]*pgxpool.Pool
	// Commit, when set, wraps every target commit (tests only).
	Commit CommitHook
	opens  int
}

func NewRegistry() *Registry { return &Registry{pools: make(map[string]*pgxpool.Pool)} }

// Add maps a registration ID to a pool the caller keeps owning.
func (r *Registry) Add(databaseID string, pool *pgxpool.Pool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pools[databaseID] = pool
}

func (r *Registry) Remove(databaseID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.pools, databaseID)
}

// Opens counts Open calls, proving denied requests never reach a target.
func (r *Registry) Opens() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.opens
}

func (r *Registry) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pools = make(map[string]*pgxpool.Pool)
}

func (r *Registry) pool(databaseID string) (*pgxpool.Pool, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pool, ok := r.pools[databaseID]
	return pool, ok
}

// Dialect reports PostgreSQL even for unconfigured targets, so drafts can be
// written before a target pool is added.
func (r *Registry) Dialect(context.Context, string) (migration.Dialect, error) {
	return migration.PostgreSQL, nil
}

func (r *Registry) Open(_ context.Context, databaseID string) (migration.Target, error) {
	r.mu.Lock()
	r.opens++
	pool, ok := r.pools[databaseID]
	r.mu.Unlock()
	if !ok {
		return nil, migration.ErrTargetUnavailable
	}
	return &target{pool: pool, owner: r}, nil
}

// Snapshot implements schemadiff.Source from the PostgreSQL catalog.
func (r *Registry) Snapshot(ctx context.Context, databaseID, schema string) (schemadiff.Snapshot, error) {
	pool, ok := r.pool(databaseID)
	if !ok {
		return schemadiff.Snapshot{}, migration.ErrTargetUnavailable
	}
	return ReadSnapshot(ctx, pool, schema)
}

type target struct {
	pool  *pgxpool.Pool
	owner *Registry
}

func (t *target) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	tag, err := t.pool.Exec(ctx, sql, args...)
	return tag.RowsAffected(), err
}

func (t *target) QueryRow(ctx context.Context, sql string, args ...any) migration.Row {
	return t.pool.QueryRow(ctx, sql, args...)
}

func (t *target) Begin(ctx context.Context) (migration.TargetTx, error) {
	tx, err := t.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &targetTx{tx: tx, owner: t.owner}, nil
}

func (t *target) Close() error { return nil }

type targetTx struct {
	tx    pgx.Tx
	owner *Registry
}

func (t *targetTx) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	tag, err := t.tx.Exec(ctx, sql, args...)
	return tag.RowsAffected(), err
}

func (t *targetTx) Rollback(ctx context.Context) error { return t.tx.Rollback(ctx) }

func (t *targetTx) Commit(ctx context.Context) error {
	commit := func(ctx context.Context) error {
		err := t.tx.Commit(ctx)
		var pgErr *pgconn.PgError
		if err == nil || errors.As(err, &pgErr) {
			return err
		}
		// Anything but a server reply leaves the commit outcome unconfirmed.
		return fmt.Errorf("%w: %v", migration.ErrCommitUnknown, err)
	}
	if hook := t.owner.Commit; hook != nil {
		return hook(ctx, commit, t.tx.Rollback)
	}
	return commit(ctx)
}
