package database

import (
	"context"
	"net"
	"net/url"
	"strconv"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type targetPool interface {
	handle() Handle
	close()
}

// PoolStat aggregates the target pools of one process for metrics.
type PoolStat struct {
	Pools, Active, Idle int64
	AcquireWait         float64
	Gates               int64
}

type postgresPool struct {
	pool    *pgxpool.Pool
	sandbox bool
}

func (p postgresPool) handle() Handle { return &postgresHandle{pool: p.pool, sandbox: p.sandbox} }

func (p postgresPool) close() { p.pool.Close() }

func (p postgresPool) stat() (active, idle int64, wait float64) {
	s := p.pool.Stat()
	return int64(s.AcquiredConns()), int64(s.IdleConns()), s.AcquireDuration().Seconds()
}

type Pools struct {
	store  *Store
	tracer pgx.QueryTracer
	mu     sync.Mutex
	pools  map[string]targetPool
}

// WithTracer logs and traces SQL of target pools and gate sessions opened later.
func (p *Pools) WithTracer(tracer pgx.QueryTracer) *Pools {
	p.tracer = tracer
	return p
}

// Stat sums target pool usage; per-target values stay out of metric labels.
func (p *Pools) Stat() PoolStat {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := PoolStat{Pools: int64(len(p.pools)), Gates: openGates.Load()}
	for _, pool := range p.pools {
		if pg, ok := pool.(postgresPool); ok {
			active, idle, wait := pg.stat()
			out.Active, out.Idle, out.AcquireWait = out.Active+active, out.Idle+idle, out.AcquireWait+wait
		}
	}
	return out
}

func NewPools(store *Store) *Pools {
	return &Pools{store: store, pools: make(map[string]targetPool)}
}

func (p *Pools) active(ctx context.Context, databaseID string) (Connection, error) {
	c, err := p.store.Get(ctx, databaseID)
	if err != nil {
		return Connection{}, err
	}
	if !c.Active {
		return Connection{}, fail(ErrNotFound, "Connection not found.")
	}
	return c, nil
}

func (p *Pools) DBMS(ctx context.Context, databaseID string) (DBMS, error) {
	c, err := p.active(ctx, databaseID)
	return c.DBMS, err
}

func (p *Pools) Open(ctx context.Context, databaseID string) (Handle, error) {
	c, err := p.active(ctx, databaseID)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	pool, ok := p.pools[databaseID]
	if !ok {
		if pool, err = openPool(ctx, c, p.tracer); err != nil {
			return nil, err
		}
		p.pools[databaseID] = pool
	}
	return pool.handle(), nil
}

func openPool(ctx context.Context, c Connection, tracer pgx.QueryTracer) (targetPool, error) {
	switch c.DBMS {
	case PostgreSQL:
		config, err := postgresConfig(c)
		if err != nil {
			return nil, err
		}
		if tracer != nil {
			config.ConnConfig.Tracer = tracer
		}
		pool, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			return nil, Unavailable()
		}
		return postgresPool{pool: pool, sandbox: c.Sandbox}, nil
	case MySQL:
		db, err := mysqlDB(c)
		if err != nil {
			return nil, err
		}
		return mysqlPool{db: db, database: c.Database}, nil
	}
	return nil, fail(ErrUnsupportedDialect, "Database type must be PostgreSQL or MySQL.")
}

func (p *Pools) Invalidate(databaseID string) {
	p.mu.Lock()
	pool, ok := p.pools[databaseID]
	delete(p.pools, databaseID)
	p.mu.Unlock()
	if ok {
		go pool.close()
	}
}

func (p *Pools) Close() {
	p.mu.Lock()
	pools := p.pools
	p.pools = make(map[string]targetPool)
	p.mu.Unlock()
	for _, pool := range pools {
		pool.close()
	}
}

func postgresConfig(c Connection) (*pgxpool.Config, error) {
	dsn := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(c.Username, c.Password),
		Host:     net.JoinHostPort(c.Host, strconv.Itoa(c.Port)),
		Path:     "/" + c.Database,
		RawQuery: url.Values{"sslmode": {string(c.TLS)}, "connect_timeout": {strconv.Itoa(int(ConnectTimeout.Seconds()))}}.Encode(),
	}
	config, err := pgxpool.ParseConfig(dsn.String())
	if err != nil {
		return nil, fail(ErrValidation, "Connection settings are invalid.")
	}
	config.MaxConns = 4
	return config, nil
}
