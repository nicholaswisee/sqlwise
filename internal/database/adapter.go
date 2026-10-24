package database

import (
	"context"
)

type Resolver interface {
	DBMS(ctx context.Context, databaseID string) (DBMS, error)
	Open(ctx context.Context, databaseID string) (Handle, error)
}

type Handle interface {
	DBMS() DBMS
	Capabilities() Capabilities
	Ping(ctx context.Context) error
	Close() error
}

type Row interface {
	Scan(dest ...any) error
}

type SchemaInspector interface {
	Schemas(ctx context.Context) ([]string, error)
	InspectSchema(ctx context.Context, schema string) (SchemaSnapshot, error)
}

type TableReader interface {
	ReadTable(ctx context.Context, req TableRequest) (TablePage, error)
}

type Executor interface {
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
	QueryRow(ctx context.Context, sql string, args ...any) Row
}

type TxBeginner interface {
	Begin(ctx context.Context) (Tx, error)
}

type Tx interface {
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

type Explainer interface {
	Explain(ctx context.Context, sql string, analyze bool) ([]byte, error)
}

type Pinner interface {
	Pin(ctx context.Context) (Conn, error)
}

type Conn interface {
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
	Close() error
}

func As[T any](h Handle) (T, error) {
	t, ok := h.(T)
	if !ok {
		return t, fail(ErrUnsupportedDialect, "This operation is not supported for "+h.DBMS().Label()+".")
	}
	return t, nil
}
