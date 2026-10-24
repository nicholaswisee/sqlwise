package database

import (
	"encoding/base64"
	"strings"
	"time"
)

const InternalSchema = "sqlwise_internal"

type SchemaSnapshot struct {
	DBMS       DBMS
	Database   string
	Schema     string
	CapturedAt time.Time
	Tables     []Table
}

type Table struct {
	Schema      string
	Name        string
	Columns     []Column
	PrimaryKey  []string
	Constraints []Constraint
	Indexes     []Index
}

func (t Table) Ref() TableRef { return TableRef{Schema: t.Schema, Name: t.Name} }

type Column struct {
	Name     string
	Type     string
	Nullable bool
	Default  *string
}

type ConstraintKind string

const (
	Unique     ConstraintKind = "unique"
	ForeignKey ConstraintKind = "foreign key"
	Check      ConstraintKind = "check"
)

type Constraint struct {
	Name       string
	Kind       ConstraintKind
	Columns    []string
	Definition string
}

type Index struct {
	Name       string
	Unique     bool
	Definition string
}

func (s SchemaSnapshot) WithoutInternal() SchemaSnapshot {
	if strings.EqualFold(s.Schema, InternalSchema) {
		s.Tables = nil
	}
	return s
}

type TableRef struct {
	Schema string
	Name   string
}

func (r TableRef) ID() string {
	return base64.RawURLEncoding.EncodeToString([]byte(r.Schema + "\x00" + r.Name))
}

func ParseTableID(id string) (TableRef, error) {
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return TableRef{}, fail(ErrNotFound, "Table not found.")
	}
	schema, name, ok := strings.Cut(string(raw), "\x00")
	if !ok {
		return TableRef{}, fail(ErrNotFound, "Table not found.")
	}
	return TableRef{Schema: schema, Name: name}, nil
}
