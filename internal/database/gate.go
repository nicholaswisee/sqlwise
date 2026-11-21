package database

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrBusy reports that another worker holds the execution gate of a target.
var ErrBusy = errors.New("execution gate busy")

// gateKey is the session advisory lock that serializes sqlwise work on one target database.
const gateKey int64 = 0x73716c77697365

const gatePoll = 100 * time.Millisecond

// openGates counts live gate sessions for the pool metrics.
var openGates atomic.Int64

// Gate is the cross-process execution gate of one target database: a
// dedicated session, never taken from a pool, that holds a session-level
// advisory lock. Every target transaction and checkpoint of the gated action
// runs on this session. It never reconnects: once the session is lost the
// lock is gone and every later call fails until a new gate is acquired.
type Gate struct {
	mu         sync.Mutex
	conn       *pgx.Conn
	databaseID string
	waited     time.Duration
	closed     bool
}

// AcquireGate connects a dedicated session and waits up to wait for the
// gate. It fails with ErrBusy when another session keeps holding it.
func AcquireGate(ctx context.Context, config *pgx.ConnConfig, databaseID string, wait time.Duration) (*Gate, error) {
	started := time.Now()
	connectCtx, cancel := context.WithTimeout(ctx, ConnectTimeout)
	conn, err := pgx.ConnectConfig(connectCtx, config)
	cancel()
	if err != nil {
		return nil, targetError(ctx, err)
	}
	deadline := started.Add(wait)
	for {
		var held bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, gateKey).Scan(&held); err != nil {
			_ = conn.Close(context.WithoutCancel(ctx))
			return nil, targetError(ctx, err)
		}
		if held {
			openGates.Add(1)
			return &Gate{conn: conn, databaseID: databaseID, waited: time.Since(started)}, nil
		}
		if time.Now().After(deadline) {
			_ = conn.Close(context.WithoutCancel(ctx))
			return nil, fail(ErrBusy, "Another job is running on this database.")
		}
		select {
		case <-ctx.Done():
			_ = conn.Close(context.WithoutCancel(ctx))
			return nil, targetError(ctx, ctx.Err())
		case <-time.After(gatePoll):
		}
	}
}

// Gate opens the execution gate of a registered PostgreSQL target. MySQL has
// no queued M2 execution, so it is refused rather than run synchronously.
func (p *Pools) Gate(ctx context.Context, databaseID string, wait time.Duration) (*Gate, error) {
	c, err := p.active(ctx, databaseID)
	if err != nil {
		return nil, err
	}
	if c.DBMS != PostgreSQL {
		return nil, fail(ErrUnsupportedDialect, "Queued execution supports PostgreSQL targets only.")
	}
	config, err := postgresConfig(c)
	if err != nil {
		return nil, err
	}
	if p.tracer != nil {
		config.ConnConfig.Tracer = p.tracer
	}
	return AcquireGate(ctx, config.ConnConfig, databaseID, wait)
}

// Waited is how long acquisition waited for the lock.
func (g *Gate) Waited() time.Duration { return g.waited }

func (g *Gate) DatabaseID() string { return g.databaseID }

// PID is the target backend holding the lock, for logs and operators.
func (g *Gate) PID() uint32 { return g.conn.PgConn().PID() }

func (g *Gate) DBMS() DBMS { return PostgreSQL }

func (g *Gate) Capabilities() Capabilities { return postgresCapabilities }

// Ping proves the session, and with it the lock, is still alive.
func (g *Gate) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, PingTimeout)
	defer cancel()
	return targetError(ctx, g.Held(ctx))
}

// Held fails unless this session still owns the advisory lock.
func (g *Gate) Held(ctx context.Context) error {
	var held bool
	err := g.conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype='advisory' AND pid=pg_backend_pid()
		AND granted AND objid=($1::bigint & 4294967295)::oid AND classid=($1::bigint >> 32)::oid)`, gateKey).Scan(&held)
	if err != nil {
		return err
	}
	if !held {
		return fail(ErrConnectionUnavailable, "The execution gate was lost.")
	}
	return nil
}

// Lost reports whether the gate session is closed; its work must stop.
func (g *Gate) Lost() bool { return g.conn.IsClosed() }

// Close releases the lock by ending the session; closing also drops it if unlock fails.
func (g *Gate) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil
	}
	g.closed = true
	openGates.Add(-1)
	ctx, cancel := context.WithTimeout(context.Background(), PingTimeout)
	defer cancel()
	if !g.conn.IsClosed() {
		_, _ = g.conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, gateKey)
	}
	return g.conn.Close(ctx)
}

func (g *Gate) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	tag, err := g.conn.Exec(ctx, sql, args...)
	return tag.RowsAffected(), err
}

func (g *Gate) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return g.conn.QueryRow(ctx, sql, args...)
}

// Begin starts a transaction on the gate session itself.
func (g *Gate) Begin(ctx context.Context) (Tx, error) {
	tx, err := g.conn.Begin(ctx)
	if err != nil {
		return nil, targetError(ctx, err)
	}
	return &gateTx{tx: tx}, nil
}

type gateTx struct {
	tx pgx.Tx
}

func (t *gateTx) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	tag, err := t.tx.Exec(ctx, sql, args...)
	return tag.RowsAffected(), err
}

func (t *gateTx) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return t.tx.QueryRow(ctx, sql, args...)
}

func (t *gateTx) Rollback(ctx context.Context) error { return t.tx.Rollback(ctx) }

func (t *gateTx) Commit(ctx context.Context) error {
	err := t.tx.Commit(ctx)
	var pgErr *pgconn.PgError
	if err == nil || errors.As(err, &pgErr) || errors.Is(err, pgx.ErrTxCommitRollback) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrOutcomeUnknown, err)
}
