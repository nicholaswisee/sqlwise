// Package pgtarget is a temporary PostgreSQL stand-in for Jason's database
// adapter so migrations and schema diff can run end to end before
// internal/database ready. It implements migration.Targets and
// schemadiff.Source over plain pgx pools.
//
// TODO(jason): replace with database.Resolver.Open, the transaction executor
// and database.SchemaSnapshot, then delete this package. Contract required by
// James: migration.Targets/Target/TargetTx (Commit wraps ErrCommitUnknown when
// the outcome is uncertain) and schemadiff.Source (never returns
// sqlwise_internal).
package pgtarget

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"sqlwise/internal/migration"
	"sqlwise/internal/schemadiff"
)

// EnvVar maps registration IDs to target URLs: "id=postgres://...;id2=...".
// TODO(jason): connection details come from encrypted registrations, not env.
const EnvVar = "SQLWISE_DEV_TARGETS"

// CommitHook replaces a real commit; it receives the real commit and rollback
// so tests can model lost acknowledgements or lost requests.
type CommitHook func(ctx context.Context, commit, rollback func(context.Context) error) error

// Registry holds one pool per registered target ID.
type Registry struct {
	mu    sync.Mutex
	pools map[string]*pgxpool.Pool
	owned bool
	// Commit, when set, wraps every target commit (tests only).
	Commit CommitHook
	opens  int
}

func NewRegistry() *Registry { return &Registry{pools: make(map[string]*pgxpool.Pool)} }

// Parse reads an EnvVar value into registration ID → URL.
func Parse(spec string) (map[string]string, error) {
	urls := make(map[string]string)
	for _, entry := range strings.Split(spec, ";") {
		if entry = strings.TrimSpace(entry); entry == "" {
			continue
		}
		id, url, ok := strings.Cut(entry, "=")
		id, url = strings.TrimSpace(id), strings.TrimSpace(url)
		if !ok || id == "" || url == "" {
			return nil, fmt.Errorf("%s: entries must look like id=postgres://...", EnvVar)
		}
		if _, dup := urls[id]; dup {
			return nil, fmt.Errorf("%s: duplicate target %q", EnvVar, id)
		}
		urls[id] = url
	}
	return urls, nil
}

// Connect creates lazy pools for every entry of spec; the registry owns and
// closes them. Connection errors surface on first use as ErrTargetUnavailable.
func Connect(spec string) (*Registry, error) {
	urls, err := Parse(spec)
	if err != nil {
		return nil, err
	}
	r := NewRegistry()
	r.owned = true
	for id, url := range urls {
		config, err := pgxpool.ParseConfig(url)
		if err != nil {
			r.Close()
			return nil, fmt.Errorf("%s: invalid URL for target %q", EnvVar, id)
		}
		config.MaxConns = 4
		pool, err := pgxpool.NewWithConfig(context.Background(), config)
		if err != nil {
			r.Close()
			return nil, fmt.Errorf("%s: target %q: %w", EnvVar, id, err)
		}
		r.pools[id] = pool
	}
	return r, nil
}

// Len reports how many targets are configured.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pools)
}

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
	if r.owned {
		for _, pool := range r.pools {
			pool.Close()
		}
	}
	r.pools = make(map[string]*pgxpool.Pool)
}

func (r *Registry) pool(databaseID string) (*pgxpool.Pool, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pool, ok := r.pools[databaseID]
	return pool, ok
}

// Dialect reports PostgreSQL even for unconfigured targets, so drafts can be
// written before a target URL is set.
// TODO(jason): read the DBMS from the registration (MySQL for bonus).
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
// TODO(jason): map database.SchemaSnapshot to schemadiff.Snapshot instead.
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
