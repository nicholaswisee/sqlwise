package database

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"sqlwise/internal/database/dialect"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var postgresCapabilities = Capabilities{ConnectionTest: true, SchemaInspection: true, DataPreview: true, MigrationExecution: true, AtomicMigrations: true, SchemaDiff: true}

const userSchemas = `n.nspname NOT IN ('pg_catalog','information_schema','pg_toast','` + InternalSchema + `')
	AND n.nspname NOT LIKE 'pg\_temp\_%' AND n.nspname NOT LIKE 'pg\_toast\_temp\_%'`

type postgresHandle struct {
	pool *pgxpool.Pool
}

func (h *postgresHandle) DBMS() DBMS { return PostgreSQL }

func (h *postgresHandle) Capabilities() Capabilities { return postgresCapabilities }

func (h *postgresHandle) Close() error { return nil }

func (h *postgresHandle) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, PingTimeout)
	defer cancel()
	return targetError(ctx, h.pool.Ping(ctx))
}

func (h *postgresHandle) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	tag, err := h.pool.Exec(ctx, sql, args...)
	return tag.RowsAffected(), err
}

func (h *postgresHandle) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return h.pool.QueryRow(ctx, sql, args...)
}

func (h *postgresHandle) Begin(ctx context.Context) (Tx, error) {
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return nil, targetError(ctx, err)
	}
	return &postgresTx{tx: tx}, nil
}

type postgresTx struct {
	tx pgx.Tx
}

func (t *postgresTx) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	tag, err := t.tx.Exec(ctx, sql, args...)
	return tag.RowsAffected(), err
}

func (t *postgresTx) Rollback(ctx context.Context) error { return t.tx.Rollback(ctx) }

func (t *postgresTx) Commit(ctx context.Context) error {
	err := t.tx.Commit(ctx)
	var pgErr *pgconn.PgError
	if err == nil || errors.As(err, &pgErr) || errors.Is(err, pgx.ErrTxCommitRollback) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrOutcomeUnknown, err)
}

func targetError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var domain *Error
	if errors.As(err, &domain) {
		return err
	}
	if err := FromContext(ctx); err != nil {
		return err
	}
	if pgconn.Timeout(err) {
		return fail(ErrTimeout, "The database did not respond in time.")
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "28P01", "28000":
			return fail(ErrConnectionUnavailable, "The database rejected the username or password.")
		case "3D000":
			return fail(ErrConnectionUnavailable, "The database does not exist on this server.")
		}
		return fail(ErrConnectionUnavailable, "The database rejected the request (SQLSTATE "+pgErr.Code+").")
	}
	return Unavailable()
}

func (h *postgresHandle) Schemas(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()
	rows, err := h.pool.Query(ctx, `SELECT n.nspname FROM pg_namespace n WHERE `+userSchemas+` ORDER BY n.nspname`)
	if err != nil {
		return nil, targetError(ctx, err)
	}
	schemas, err := pgx.CollectRows(rows, pgx.RowTo[string])
	return schemas, targetError(ctx, err)
}

func (h *postgresHandle) InspectSchema(ctx context.Context, schema string) (SchemaSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()
	snapshot := SchemaSnapshot{DBMS: PostgreSQL, Schema: schema, CapturedAt: time.Now().UTC()}
	err := pgx.BeginTxFunc(ctx, h.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT current_database()`).Scan(&snapshot.Database); err != nil {
			return err
		}
		if strings.EqualFold(schema, InternalSchema) {
			return nil
		}
		return inspectPostgres(ctx, tx, &snapshot)
	})
	if err != nil {
		return SchemaSnapshot{}, targetError(ctx, err)
	}
	return snapshot, nil
}

func inspectPostgres(ctx context.Context, tx pgx.Tx, snapshot *SchemaSnapshot) error {
	rows, err := tx.Query(ctx, `SELECT c.relname, a.attname, format_type(a.atttypid, a.atttypmod), NOT a.attnotnull, pg_get_expr(d.adbin, d.adrelid)
		FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
		LEFT JOIN pg_attrdef d ON d.adrelid=c.oid AND d.adnum=a.attnum
		WHERE n.nspname=$1 AND c.relkind IN ('r','p') ORDER BY c.relname, a.attnum`, snapshot.Schema)
	if err != nil {
		return err
	}
	index := make(map[string]int)
	for rows.Next() {
		var table string
		var column Column
		if err := rows.Scan(&table, &column.Name, &column.Type, &column.Nullable, &column.Default); err != nil {
			rows.Close()
			return err
		}
		i, ok := index[table]
		if !ok {
			i = len(snapshot.Tables)
			index[table] = i
			snapshot.Tables = append(snapshot.Tables, Table{Schema: snapshot.Schema, Name: table})
		}
		snapshot.Tables[i].Columns = append(snapshot.Tables[i].Columns, column)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = tx.Query(ctx, `SELECT c.relname, x.conname, x.contype, pg_get_constraintdef(x.oid),
		ARRAY(SELECT a.attname FROM unnest(x.conkey) WITH ORDINALITY k(attnum, n) JOIN pg_attribute a ON a.attrelid=x.conrelid AND a.attnum=k.attnum ORDER BY k.n)
		FROM pg_constraint x JOIN pg_class c ON c.oid=x.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname=$1 AND x.contype IN ('p','u','f','c') ORDER BY c.relname, x.conname`, snapshot.Schema)
	if err != nil {
		return err
	}
	kinds := map[string]ConstraintKind{"u": Unique, "f": ForeignKey, "c": Check}
	for rows.Next() {
		var table, kind string
		var constraint Constraint
		if err := rows.Scan(&table, &constraint.Name, &kind, &constraint.Definition, &constraint.Columns); err != nil {
			rows.Close()
			return err
		}
		i, ok := index[table]
		switch {
		case !ok:
		case kind == "p":
			snapshot.Tables[i].PrimaryKey = constraint.Columns
		default:
			constraint.Kind = kinds[kind]
			snapshot.Tables[i].Constraints = append(snapshot.Tables[i].Constraints, constraint)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = tx.Query(ctx, `SELECT t.relname, i.relname, x.indisunique, pg_get_indexdef(x.indexrelid)
		FROM pg_index x JOIN pg_class i ON i.oid=x.indexrelid JOIN pg_class t ON t.oid=x.indrelid JOIN pg_namespace n ON n.oid=t.relnamespace
		WHERE n.nspname=$1 AND t.relkind IN ('r','p')
			AND NOT EXISTS (SELECT 1 FROM pg_constraint c WHERE c.conindid=x.indexrelid AND c.conrelid=x.indrelid AND c.contype IN ('p','u'))
		ORDER BY t.relname, i.relname`, snapshot.Schema)
	if err != nil {
		return err
	}
	for rows.Next() {
		var table string
		var idx Index
		if err := rows.Scan(&table, &idx.Name, &idx.Unique, &idx.Definition); err != nil {
			rows.Close()
			return err
		}
		if i, ok := index[table]; ok {
			snapshot.Tables[i].Indexes = append(snapshot.Tables[i].Indexes, idx)
		}
	}
	return rows.Err()
}

type pageColumn struct {
	ResultColumn
	binary bool
}

func (h *postgresHandle) ReadTable(ctx context.Context, req TableRequest) (TablePage, error) {
	ctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()
	page, err := h.readTable(ctx, req)
	if err != nil {
		return TablePage{}, targetError(ctx, err)
	}
	return page, nil
}

func (h *postgresHandle) readTable(ctx context.Context, req TableRequest) (TablePage, error) {
	page := TablePage{Table: req.Table, Offset: req.Offset, Limit: req.Limit}
	var oid uint32
	err := h.pool.QueryRow(ctx, `SELECT c.oid FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname=$1 AND c.relname=$2 AND c.relkind IN ('r','p') AND `+userSchemas, req.Table.Schema, req.Table.Name).Scan(&oid)
	if errors.Is(err, pgx.ErrNoRows) {
		return page, fail(ErrNotFound, "Table not found.")
	}
	if err != nil {
		return page, err
	}
	rows, err := h.pool.Query(ctx, `SELECT a.attname, format_type(a.atttypid, a.atttypmod), a.atttypid='bytea'::regtype
		FROM pg_attribute a WHERE a.attrelid=$1 AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, oid)
	if err != nil {
		return page, err
	}
	columns, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (pageColumn, error) {
		var c pageColumn
		return c, row.Scan(&c.Name, &c.Type, &c.binary)
	})
	if err != nil {
		return page, err
	}
	rows, err = h.pool.Query(ctx, `SELECT a.attname FROM pg_index x
		JOIN unnest(x.indkey) WITH ORDINALITY k(attnum, n) ON true
		JOIN pg_attribute a ON a.attrelid=x.indrelid AND a.attnum=k.attnum
		WHERE x.indrelid=$1 AND x.indisprimary ORDER BY k.n`, oid)
	if err != nil {
		return page, err
	}
	key, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return page, err
	}
	pg := dialect.Postgres
	exprs := make([]string, len(columns))
	for i, c := range columns {
		page.Columns = append(page.Columns, c.ResultColumn)
		if c.binary {
			exprs[i] = "substring(" + pg.Quote(c.Name) + " from 1 for " + strconv.Itoa(MaxCellBytes/2) + ")"
		} else {
			exprs[i] = "left(" + pg.Quote(c.Name) + "::text, " + strconv.Itoa(MaxCellBytes+1) + ")"
		}
	}
	query := "SELECT " + strings.Join(exprs, ", ") + " FROM " + pg.Qualify(req.Table.Schema, req.Table.Name)
	if len(key) > 0 {
		quoted := make([]string, len(key))
		for i, k := range key {
			quoted[i] = pg.Quote(k)
		}
		query += " ORDER BY " + strings.Join(quoted, ", ")
		page.Ordered = true
	}
	rows, err = h.pool.Query(ctx, query+" LIMIT $1 OFFSET $2", req.Limit+1, req.Offset)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	size := 0
	for rows.Next() {
		if len(page.Rows) == req.Limit {
			page.HasNext = true
			break
		}
		values := make([]any, len(columns))
		for i, c := range columns {
			if c.binary {
				values[i] = new([]byte)
			} else {
				values[i] = new(*string)
			}
		}
		if err := rows.Scan(values...); err != nil {
			return page, err
		}
		row := make([]Cell, len(columns))
		rowSize := 0
		for i, v := range values {
			switch v := v.(type) {
			case *[]byte:
				if *v == nil {
					row[i] = NullCell()
				} else {
					row[i] = BinaryCell(*v, MaxCellBytes)
				}
			case **string:
				if *v == nil {
					row[i] = NullCell()
				} else {
					row[i] = TextCell(**v, MaxCellBytes)
				}
			}
			rowSize += len(row[i].Text)
		}
		if size+rowSize > MaxPageBytes && len(page.Rows) > 0 {
			page.HasNext, page.Truncated = true, true
			break
		}
		size += rowSize
		page.Rows = append(page.Rows, row)
	}
	return page, rows.Err()
}
