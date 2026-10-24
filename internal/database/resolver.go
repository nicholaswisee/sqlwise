package database

import (
	"context"
	"net"
	"net/url"
	"strconv"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Pools struct {
	store *Store
	mu    sync.Mutex
	pools map[string]*pgxpool.Pool
}

func NewPools(store *Store) *Pools {
	return &Pools{store: store, pools: make(map[string]*pgxpool.Pool)}
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
	if c.DBMS != PostgreSQL {
		return nil, fail(ErrUnsupportedDialect, c.DBMS.Label()+" connections are not supported yet.")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	pool, ok := p.pools[databaseID]
	if !ok {
		config, err := postgresConfig(c)
		if err != nil {
			return nil, err
		}
		if pool, err = pgxpool.NewWithConfig(ctx, config); err != nil {
			return nil, Unavailable()
		}
		p.pools[databaseID] = pool
	}
	return &postgresHandle{pool: pool}, nil
}

func (p *Pools) Invalidate(databaseID string) {
	p.mu.Lock()
	pool, ok := p.pools[databaseID]
	delete(p.pools, databaseID)
	p.mu.Unlock()
	if ok {
		go pool.Close()
	}
}

func (p *Pools) Close() {
	p.mu.Lock()
	pools := p.pools
	p.pools = make(map[string]*pgxpool.Pool)
	p.mu.Unlock()
	for _, pool := range pools {
		pool.Close()
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
