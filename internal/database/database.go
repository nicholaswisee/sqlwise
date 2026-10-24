package database

import (
	"sqlwise/internal/database/dialect"
	"time"
)

type DBMS string

const (
	PostgreSQL DBMS = "postgresql"
	MySQL      DBMS = "mysql"
)

func ParseDBMS(s string) (DBMS, error) {
	switch DBMS(s) {
	case PostgreSQL, MySQL:
		return DBMS(s), nil
	}
	return "", fail(ErrUnsupportedDialect, "Database type must be PostgreSQL or MySQL.")
}

func (d DBMS) String() string { return string(d) }

func (d DBMS) Label() string {
	switch d {
	case PostgreSQL:
		return "PostgreSQL"
	case MySQL:
		return "MySQL"
	}
	return "Unknown"
}

func (d DBMS) Dialect() (dialect.Dialect, error) {
	switch d {
	case PostgreSQL:
		return dialect.Postgres, nil
	case MySQL:
		return dialect.MySQL, nil
	}
	return nil, fail(ErrUnsupportedDialect, "Database type must be PostgreSQL or MySQL.")
}

type Capabilities struct {
	ConnectionTest     bool
	SchemaInspection   bool
	DataPreview        bool
	MigrationExecution bool
	AtomicMigrations   bool
	SchemaDiff         bool
	Explain            bool
	ExplainAnalyze     bool
}

type Capability struct {
	Name      string
	Supported bool
}

func (c Capabilities) List() []Capability {
	return []Capability{
		{"Connection test", c.ConnectionTest},
		{"Schema inspection", c.SchemaInspection},
		{"Data preview", c.DataPreview},
		{"Migration execution", c.MigrationExecution},
		{"Atomic migrations", c.AtomicMigrations},
		{"Schema diff", c.SchemaDiff},
		{"Explain", c.Explain},
		{"Explain analyze", c.ExplainAnalyze},
	}
}

const (
	ConnectTimeout = 5 * time.Second
	PingTimeout    = 5 * time.Second
	QueryTimeout   = 5 * time.Second
)
