package database

import (
	"context"
	"net"
	"net/url"
	"strconv"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

type targetPool interface {
	handle() Handle
	close()
}

type postgresPool struct {
	pool    *pgxpool.Pool
	sandbox bool
}

func (p postgresPool) handle() Handle { return &postgresHandle{pool: p.pool, sandbox: p.sandbox} }

func (p postgresPool) close() { p.pool.Close() }

type Pools struct {
	store *Store
	mu    sync.Mutex
	pools map[string]targetPool
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
		if pool, err = openPool(ctx, c); err != nil {
			return nil, err
		}
		p.pools[databaseID] = pool
	}
	return pool.handle(), nil
}

func openPool(ctx context.Context, c Connection) (targetPool, error) {
	switch c.DBMS {
	case PostgreSQL:
		config, err := postgresConfig(c)
		if err != nil {
			return nil, err
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
