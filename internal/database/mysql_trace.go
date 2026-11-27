package database

import (
	"context"
	"database/sql"
	"errors"

	"github.com/go-sql-driver/mysql"
	"sqlwise/internal/telemetry"
)

// tracedDB logs and traces every MySQL statement the way the pgx tracer does
// for PostgreSQL; database/sql has no tracer hook of its own.
type tracedDB struct{ *sql.DB }

func (d tracedDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return tracedExec(ctx, query, args, func(ctx context.Context) (sql.Result, error) { return d.DB.ExecContext(ctx, query, args...) })
}

func (d tracedDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	ctx, trace := telemetry.StartSQL(ctx, "mysql", query, args)
	rows, err := d.DB.QueryContext(ctx, query, args...)
	state, message := mysqlState(err)
	trace.End(-1, err, state, message)
	return rows, err
}

func (d tracedDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	ctx, trace := telemetry.StartSQL(ctx, "mysql", query, args)
	row := d.DB.QueryRowContext(ctx, query, args...)
	err := row.Err()
	state, message := mysqlState(err)
	trace.End(-1, err, state, message)
	return row
}

func tracedConnExec(ctx context.Context, conn *sql.Conn, query string, args []any) (sql.Result, error) {
	return tracedExec(ctx, query, args, func(ctx context.Context) (sql.Result, error) { return conn.ExecContext(ctx, query, args...) })
}

func tracedExec(ctx context.Context, query string, args []any, exec func(context.Context) (sql.Result, error)) (sql.Result, error) {
	ctx, trace := telemetry.StartSQL(ctx, "mysql", query, args)
	result, err := exec(ctx)
	rows := int64(-1)
	if err == nil {
		if n, countErr := result.RowsAffected(); countErr == nil {
			rows = n
		}
	}
	state, message := mysqlState(err)
	trace.End(rows, err, state, message)
	return result, err
}

// mysqlState returns the server's SQLSTATE and message; driver and network
// errors return neither, so connection details never reach the logs.
func mysqlState(err error) (string, string) {
	var server *mysql.MySQLError
	if errors.As(err, &server) {
		return string(server.SQLState[:]), server.Message
	}
	return "", ""
}
