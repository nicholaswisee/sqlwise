package pgtarget

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sqlwise/internal/schemadiff"
)

// ReadSnapshot reads tables, columns, keys, constraints and indexes of one
// schema from the PostgreSQL catalog. The reserved receipt schema is always
// reported empty.
func ReadSnapshot(ctx context.Context, pool *pgxpool.Pool, schema string) (schemadiff.Snapshot, error) {
	snapshot := schemadiff.Snapshot{Dialect: schemadiff.PostgreSQL, Schema: schema}
	if strings.EqualFold(schema, schemadiff.InternalSchema) {
		return snapshot, nil
	}
	rows, err := pool.Query(ctx, `SELECT c.relname, a.attname, format_type(a.atttypid, a.atttypmod), NOT a.attnotnull, pg_get_expr(d.adbin, d.adrelid)
		FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
		LEFT JOIN pg_attrdef d ON d.adrelid=c.oid AND d.adnum=a.attnum
		WHERE n.nspname=$1 AND c.relkind IN ('r','p') ORDER BY c.relname, a.attnum`, schema)
	if err != nil {
		return snapshot, err
	}
	index := make(map[string]int)
	for rows.Next() {
		var table string
		var column schemadiff.Column
		if err := rows.Scan(&table, &column.Name, &column.Type, &column.Nullable, &column.Default); err != nil {
			rows.Close()
			return snapshot, err
		}
		i, ok := index[table]
		if !ok {
			i = len(snapshot.Tables)
			index[table] = i
			snapshot.Tables = append(snapshot.Tables, schemadiff.Table{Name: table})
		}
		snapshot.Tables[i].Columns = append(snapshot.Tables[i].Columns, column)
	}
	if err := rows.Err(); err != nil {
		return snapshot, err
	}
	rows, err = pool.Query(ctx, `SELECT c.relname, x.conname, x.contype, pg_get_constraintdef(x.oid),
		ARRAY(SELECT a.attname FROM unnest(x.conkey) WITH ORDINALITY k(attnum, n) JOIN pg_attribute a ON a.attrelid=x.conrelid AND a.attnum=k.attnum ORDER BY k.n)
		FROM pg_constraint x JOIN pg_class c ON c.oid=x.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname=$1 AND x.contype IN ('p','u','f','c') ORDER BY c.relname, x.conname`, schema)
	if err != nil {
		return snapshot, err
	}
	err = forEach(rows, func(row pgx.CollectableRow) error {
		var table, name, kind, definition string
		var columns []string
		if err := row.Scan(&table, &name, &kind, &definition, &columns); err != nil {
			return err
		}
		if i, ok := index[table]; ok {
			if kind == "p" {
				snapshot.Tables[i].PrimaryKey = columns
			} else {
				snapshot.Tables[i].Constraints = append(snapshot.Tables[i].Constraints, schemadiff.Definition{Name: name, Definition: definition})
			}
		}
		return nil
	})
	if err != nil {
		return snapshot, err
	}
	rows, err = pool.Query(ctx, `SELECT i.tablename, i.indexname, i.indexdef FROM pg_indexes i
		WHERE i.schemaname=$1 AND NOT EXISTS (SELECT 1 FROM pg_constraint x JOIN pg_class ic ON ic.oid=x.conindid
			JOIN pg_namespace n ON n.oid=ic.relnamespace WHERE ic.relname=i.indexname AND n.nspname=i.schemaname)
		ORDER BY i.tablename, i.indexname`, schema)
	if err != nil {
		return snapshot, err
	}
	err = forEach(rows, func(row pgx.CollectableRow) error {
		var table, name, definition string
		if err := row.Scan(&table, &name, &definition); err != nil {
			return err
		}
		if i, ok := index[table]; ok {
			snapshot.Tables[i].Indexes = append(snapshot.Tables[i].Indexes, schemadiff.Definition{Name: name, Definition: definition})
		}
		return nil
	})
	return snapshot, err
}

func forEach(rows pgx.Rows, fn func(pgx.CollectableRow) error) error {
	_, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (struct{}, error) { return struct{}{}, fn(row) })
	return err
}
