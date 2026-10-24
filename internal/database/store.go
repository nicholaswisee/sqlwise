package database

import (
	"context"
	_ "embed"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

type Store struct {
	pool   *pgxpool.Pool
	cipher *Cipher
}

func NewStore(pool *pgxpool.Pool, cipher *Cipher) *Store {
	return &Store{pool: pool, cipher: cipher}
}

func (s *Store) ApplySchema(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(311007)"); err != nil {
			return fmt.Errorf("lock connection schema: %w", err)
		}
		if _, err := tx.Exec(ctx, schema); err != nil {
			return fmt.Errorf("apply connection schema: %w", err)
		}
		return nil
	})
}

func (s *Store) Insert(ctx context.Context, tx pgx.Tx, databaseID string, c Connection) error {
	if err := c.Validate(); err != nil {
		return err
	}
	sealed, err := s.cipher.Seal(c.Password)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO database_connections(database_id,dbms,host,port,database_name,username,tls_mode,password_sealed,analyze_sandbox)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, databaseID, string(c.DBMS), c.Host, c.Port, c.Database, c.Username, string(c.TLS), sealed, c.Sandbox)
	return err
}

func (s *Store) Update(ctx context.Context, tx pgx.Tx, databaseID string, c Connection) error {
	if err := c.Validate(); err != nil {
		return err
	}
	var dbms, host, database string
	var port int
	var sealed []byte
	err := tx.QueryRow(ctx, `SELECT dbms,host,port,database_name,password_sealed FROM database_connections WHERE database_id=$1 FOR UPDATE`, databaseID).Scan(&dbms, &host, &port, &database, &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(ErrNotFound, "Connection not found.")
	}
	if err != nil {
		return err
	}
	if DBMS(dbms) != c.DBMS || host != c.Host || port != c.Port || database != c.Database {
		var history bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM migration_attempts WHERE database_id=$1)`, databaseID).Scan(&history); err != nil {
			return err
		}
		if history {
			return fail(ErrConflict, "This connection has migration history. Register a new connection for a different database.")
		}
	}
	if c.Password != "" {
		if sealed, err = s.cipher.Seal(c.Password); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE database_connections SET dbms=$2,host=$3,port=$4,database_name=$5,username=$6,tls_mode=$7,password_sealed=$8,analyze_sandbox=$9,updated_at=now()
		WHERE database_id=$1`, databaseID, string(c.DBMS), c.Host, c.Port, c.Database, c.Username, string(c.TLS), sealed, c.Sandbox)
	return err
}

const connectionColumns = `r.id,r.name,r.active,c.dbms,c.host,c.port,c.database_name,c.username,c.tls_mode,c.analyze_sandbox,c.created_at,c.updated_at`

func scanConnection(row pgx.Row, extra ...any) (Connection, error) {
	var c Connection
	var dbms, tls string
	err := row.Scan(append([]any{&c.DatabaseID, &c.Name, &c.Active, &dbms, &c.Host, &c.Port, &c.Database, &c.Username, &tls, &c.Sandbox, &c.CreatedAt, &c.UpdatedAt}, extra...)...)
	c.DBMS, c.TLS = DBMS(dbms), TLSMode(tls)
	return c, err
}

func (s *Store) Get(ctx context.Context, databaseID string) (Connection, error) {
	var sealed []byte
	c, err := scanConnection(s.pool.QueryRow(ctx, `SELECT `+connectionColumns+`,c.password_sealed
		FROM database_connections c JOIN database_registrations r ON r.id=c.database_id WHERE c.database_id=$1`, databaseID), &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{}, fail(ErrNotFound, "Connection not found.")
	}
	if err != nil {
		return Connection{}, err
	}
	if c.Password, err = s.cipher.Open(sealed); err != nil {
		return Connection{}, err
	}
	return c, nil
}

func (s *Store) List(ctx context.Context) ([]Connection, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+connectionColumns+`
		FROM database_connections c JOIN database_registrations r ON r.id=c.database_id ORDER BY r.name,r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var connections []Connection
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		connections = append(connections, c)
	}
	return connections, rows.Err()
}
