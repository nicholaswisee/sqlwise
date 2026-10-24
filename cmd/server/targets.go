package main

import (
	"context"
	"errors"
	"fmt"

	"sqlwise/internal/database"
	"sqlwise/internal/migration"
	"sqlwise/internal/schemadiff"
)

type migrationTargets struct {
	resolver database.Resolver
}

func migrationError(err error) error {
	if errors.Is(err, database.ErrUnsupportedDialect) {
		return &migration.Error{Kind: migration.ErrUnsupportedDialect, Message: database.Message(err)}
	}
	message := database.Message(err)
	if message == "" {
		message = "The target database is unavailable."
	}
	return &migration.Error{Kind: migration.ErrTargetUnavailable, Message: message}
}

func (m migrationTargets) Dialect(ctx context.Context, databaseID string) (migration.Dialect, error) {
	d, err := m.resolver.DBMS(ctx, databaseID)
	if err != nil {
		return "", migrationError(err)
	}
	return migration.Dialect(d), nil
}

func (m migrationTargets) Open(ctx context.Context, databaseID string) (migration.Target, error) {
	h, err := m.resolver.Open(ctx, databaseID)
	if err != nil {
		return nil, migrationError(err)
	}
	exec, err := database.As[database.Executor](h)
	if err != nil {
		h.Close()
		return nil, migrationError(err)
	}
	begin, err := database.As[database.TxBeginner](h)
	if err != nil {
		h.Close()
		return nil, migrationError(err)
	}
	return migrationTarget{handle: h, exec: exec, begin: begin}, nil
}

func (m migrationTargets) Snapshot(ctx context.Context, databaseID, schema string) (schemadiff.Snapshot, error) {
	h, err := m.resolver.Open(ctx, databaseID)
	if err != nil {
		return schemadiff.Snapshot{}, migrationError(err)
	}
	defer h.Close()
	inspector, err := database.As[database.SchemaInspector](h)
	if err != nil {
		return schemadiff.Snapshot{}, migrationError(err)
	}
	s, err := inspector.InspectSchema(ctx, schema)
	if err != nil {
		return schemadiff.Snapshot{}, migrationError(err)
	}
	return diffSnapshot(s.WithoutInternal()), nil
}

func diffSnapshot(s database.SchemaSnapshot) schemadiff.Snapshot {
	out := schemadiff.Snapshot{Dialect: string(s.DBMS), Schema: s.Schema}
	for _, t := range s.Tables {
		table := schemadiff.Table{Name: t.Name, PrimaryKey: t.PrimaryKey}
		for _, c := range t.Columns {
			table.Columns = append(table.Columns, schemadiff.Column{Name: c.Name, Type: c.Type, Nullable: c.Nullable, Default: c.Default})
		}
		for _, i := range t.Indexes {
			table.Indexes = append(table.Indexes, schemadiff.Definition{Name: i.Name, Definition: i.Definition})
		}
		for _, c := range t.Constraints {
			table.Constraints = append(table.Constraints, schemadiff.Definition{Name: c.Name, Definition: c.Definition})
		}
		out.Tables = append(out.Tables, table)
	}
	return out
}

type migrationTarget struct {
	handle database.Handle
	exec   database.Executor
	begin  database.TxBeginner
}

func (t migrationTarget) Exec(ctx context.Context, sql string, args ...any) (int64, error) {
	return t.exec.Exec(ctx, sql, args...)
}

func (t migrationTarget) QueryRow(ctx context.Context, sql string, args ...any) migration.Row {
	return t.exec.QueryRow(ctx, sql, args...)
}

func (t migrationTarget) Begin(ctx context.Context) (migration.TargetTx, error) {
	tx, err := t.begin.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", migration.ErrTargetUnavailable, err)
	}
	return migrationTx{tx}, nil
}

func (t migrationTarget) Close() error { return t.handle.Close() }

type migrationTx struct {
	database.Tx
}

func (t migrationTx) Commit(ctx context.Context) error {
	err := t.Tx.Commit(ctx)
	if errors.Is(err, database.ErrOutcomeUnknown) {
		return fmt.Errorf("%w: %w", migration.ErrCommitUnknown, err)
	}
	return err
}
