// Package schemadiff compares a live schema snapshot with a desired snapshot
// and proposes reviewable PostgreSQL migration SQL for a safe subset.
package schemadiff

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	PostgreSQL = "postgresql"
	MySQL      = "mysql"
	// InternalSchema holds execution receipts and never appears in user diffs.
	InternalSchema = "sqlwise_internal"
	MaxTables      = 500
	MaxColumns     = 1600
	MaxInputBytes  = 512 << 10
)

// Snapshot is one schema of one database. It mirrors the fields of the
// database adapter's SchemaSnapshot that schema diff compares.
type Snapshot struct {
	Dialect string  `json:"dialect"`
	Schema  string  `json:"schema"`
	Tables  []Table `json:"tables"`
}

type Table struct {
	Name        string       `json:"name"`
	Columns     []Column     `json:"columns"`
	PrimaryKey  []string     `json:"primaryKey,omitempty"`
	Indexes     []Definition `json:"indexes,omitempty"`
	Constraints []Definition `json:"constraints,omitempty"`
}

type Column struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Nullable bool    `json:"nullable"`
	Default  *string `json:"default,omitempty"`
}

// Definition is a named index or non-primary-key constraint; Definition holds
// the DBMS definition text (for example pg_get_indexdef output).
type Definition struct {
	Name       string `json:"name"`
	Definition string `json:"definition"`
}

// ValidationError explains why a snapshot was rejected.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

func invalid(format string, args ...any) error {
	return &ValidationError{Message: fmt.Sprintf(format, args...)}
}

type columnInput struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Nullable *bool   `json:"nullable"`
	Default  *string `json:"default"`
}

type tableInput struct {
	Name        string        `json:"name"`
	Columns     []columnInput `json:"columns"`
	PrimaryKey  []string      `json:"primaryKey"`
	Indexes     []Definition  `json:"indexes"`
	Constraints []Definition  `json:"constraints"`
}

type snapshotInput struct {
	Dialect string       `json:"dialect"`
	Schema  string       `json:"schema"`
	Tables  []tableInput `json:"tables"`
}

// ParseDesired decodes and validates a desired snapshot submitted by a user.
// Unknown fields are rejected and nullability must be explicit.
func ParseDesired(raw string) (Snapshot, error) {
	if len(raw) > MaxInputBytes {
		return Snapshot{}, invalid("Desired snapshot must be at most %d KiB.", MaxInputBytes>>10)
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var in snapshotInput
	if err := decoder.Decode(&in); err != nil {
		return Snapshot{}, invalid("Desired snapshot is not valid JSON for this format: %s.", jsonProblem(err))
	}
	if decoder.More() {
		return Snapshot{}, invalid("Desired snapshot must contain a single JSON object.")
	}
	s := Snapshot{Dialect: in.Dialect, Schema: in.Schema}
	for _, t := range in.Tables {
		table := Table{Name: t.Name, PrimaryKey: t.PrimaryKey, Indexes: t.Indexes, Constraints: t.Constraints}
		for _, c := range t.Columns {
			if c.Nullable == nil {
				return Snapshot{}, invalid("Column %q in table %q must state \"nullable\": true or false.", c.Name, t.Name)
			}
			table.Columns = append(table.Columns, Column{Name: c.Name, Type: c.Type, Nullable: *c.Nullable, Default: c.Default})
		}
		s.Tables = append(s.Tables, table)
	}
	if err := s.Validate(); err != nil {
		return Snapshot{}, err
	}
	return s, nil
}

func jsonProblem(err error) string {
	message := err.Error()
	message = strings.TrimPrefix(message, "json: ")
	if len(message) > 160 {
		message = message[:160]
	}
	return message
}

// Validate checks dialect, scope, identifiers, types and structural rules.
func (s Snapshot) Validate() error {
	switch s.Dialect {
	case PostgreSQL:
	case MySQL:
		return &UnsupportedDialectError{Dialect: MySQL}
	default:
		return invalid("Dialect must be %q.", PostgreSQL)
	}
	if err := validIdentifier("schema", s.Schema); err != nil {
		return err
	}
	switch strings.ToLower(s.Schema) {
	case InternalSchema, "pg_catalog", "information_schema", "pg_toast":
		return invalid("Schema %q is reserved and cannot be diffed.", s.Schema)
	}
	if len(s.Tables) > MaxTables {
		return invalid("Snapshots may contain at most %d tables.", MaxTables)
	}
	tables := make(map[string]bool)
	for _, t := range s.Tables {
		if err := validIdentifier("table", t.Name); err != nil {
			return err
		}
		if tables[t.Name] {
			return invalid("Table %q appears more than once.", t.Name)
		}
		tables[t.Name] = true
		if len(t.Columns) == 0 || len(t.Columns) > MaxColumns {
			return invalid("Table %q must have 1 to %d columns.", t.Name, MaxColumns)
		}
		columns := make(map[string]bool)
		for _, c := range t.Columns {
			if err := validIdentifier("column", c.Name); err != nil {
				return err
			}
			if columns[c.Name] {
				return invalid("Column %q appears more than once in table %q.", c.Name, t.Name)
			}
			columns[c.Name] = true
			if _, ok := parseType(c.Type); !ok {
				return invalid("Column %q in table %q has an unrecognized type %q.", c.Name, t.Name, c.Type)
			}
			if c.Default != nil && (!utf8.ValidString(*c.Default) || strings.ContainsRune(*c.Default, 0) || len(*c.Default) > 1000) {
				return invalid("Default of column %q in table %q must be at most 1000 characters of text.", c.Name, t.Name)
			}
		}
		seen := make(map[string]bool)
		for _, name := range t.PrimaryKey {
			if !columns[name] || seen[name] {
				return invalid("Primary key of table %q must list distinct existing columns.", t.Name)
			}
			seen[name] = true
		}
		for _, group := range [][]Definition{t.Indexes, t.Constraints} {
			names := make(map[string]bool)
			for _, d := range group {
				if err := validIdentifier("index or constraint", d.Name); err != nil {
					return err
				}
				if names[d.Name] || !utf8.ValidString(d.Definition) || len(d.Definition) > 4000 {
					return invalid("Index or constraint %q in table %q is duplicated or invalid.", d.Name, t.Name)
				}
				names[d.Name] = true
			}
		}
	}
	return nil
}

// UnsupportedDialectError marks dialects without a diff implementation.
type UnsupportedDialectError struct{ Dialect string }

func (e *UnsupportedDialectError) Error() string {
	return fmt.Sprintf("Schema diff for %s is not supported yet.", e.Dialect)
}

// validIdentifier rejects names PostgreSQL would truncate or cannot store.
// Any other characters are allowed because generated SQL always quotes.
func validIdentifier(kind, name string) error {
	if name == "" || len(name) > 63 || !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		return invalid("Each %s name must be 1 to 63 bytes of UTF-8 text.", kind)
	}
	return nil
}

// QuoteIdent quotes a PostgreSQL identifier, doubling embedded quotes.
func QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

type pgType struct {
	canonical string
	base      string
	supported bool
}

var typeAliases = map[string]string{
	"int": "integer", "int4": "integer", "integer": "integer",
	"int2": "smallint", "smallint": "smallint",
	"int8": "bigint", "bigint": "bigint",
	"bool": "boolean", "boolean": "boolean",
	"text": "text", "uuid": "uuid", "jsonb": "jsonb", "json": "json", "bytea": "bytea", "date": "date",
	"float4": "real", "real": "real", "float8": "double precision", "double precision": "double precision",
	"timestamptz": "timestamp with time zone", "timestamp with time zone": "timestamp with time zone",
	"timestamp": "timestamp without time zone", "timestamp without time zone": "timestamp without time zone",
	"varchar": "character varying", "character varying": "character varying",
	"char": "character", "character": "character", "bpchar": "character",
	"numeric": "numeric", "decimal": "numeric",
}

var (
	typeShape  = regexp.MustCompile(`^([a-z_][a-z0-9_ ]*?)\s*(?:\(\s*(\d+)\s*(?:,\s*(\d+)\s*)?\))?$`)
	safeType   = regexp.MustCompile(`^([a-z_][a-z0-9_]*\.)?[a-z_][a-z0-9_ ]*(\(\s*\d+\s*(,\s*\d+\s*)?\))?( with(out)? time zone)?(\[\])*$`)
	spaceRun   = regexp.MustCompile(`\s+`)
	numberForm = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)
)

// parseType normalizes PostgreSQL type spellings to format_type output. Types
// outside the generator subset are accepted for comparison when they are a
// plain type name, but are never used to generate SQL.
func parseType(raw string) (pgType, bool) {
	text := strings.ToLower(strings.TrimSpace(spaceRun.ReplaceAllString(raw, " ")))
	if text == "" || len(text) > 128 {
		return pgType{}, false
	}
	if m := typeShape.FindStringSubmatch(text); m != nil {
		if base, ok := typeAliases[strings.TrimSpace(m[1])]; ok {
			args := m[2:]
			switch {
			case base == "character varying" || base == "character":
				if args[1] != "" {
					return pgType{}, false
				}
				if args[0] == "" {
					if base == "character" {
						args[0] = "1"
					} else {
						return pgType{canonical: base, base: base, supported: true}, true
					}
				}
				n, err := strconv.Atoi(args[0])
				if err != nil || n < 1 || n > 10485760 {
					return pgType{}, false
				}
				return pgType{canonical: fmt.Sprintf("%s(%d)", base, n), base: base, supported: true}, true
			case base == "numeric":
				if args[0] == "" {
					return pgType{canonical: base, base: base, supported: true}, true
				}
				p, _ := strconv.Atoi(args[0])
				s := 0
				if args[1] != "" {
					s, _ = strconv.Atoi(args[1])
				}
				if p < 1 || p > 1000 || s > p {
					return pgType{}, false
				}
				return pgType{canonical: fmt.Sprintf("numeric(%d,%d)", p, s), base: base, supported: true}, true
			case args[0] == "":
				return pgType{canonical: base, base: base, supported: true}, true
			}
		}
	}
	if safeType.MatchString(text) {
		return pgType{canonical: text, base: text}, true
	}
	return pgType{}, false
}

var defaultFunctions = map[string]string{
	"now()": "now()", "current_timestamp": "CURRENT_TIMESTAMP", "current_date": "CURRENT_DATE",
	"gen_random_uuid()": "gen_random_uuid()",
}

type pgDefault struct {
	canonical string
	supported bool
}

// parseDefault canonicalizes the default subset the generator can emit:
// NULL, numbers, booleans, plain string literals (optionally cast to the
// column type) and a few clock/UUID functions valid for the column type.
// Other expressions compare as trimmed text and are never generated.
func parseDefault(raw string, column pgType) pgDefault {
	text := strings.TrimSpace(raw)
	lower := strings.ToLower(text)
	numeric := slices.Contains([]string{"smallint", "integer", "bigint", "numeric", "real", "double precision"}, column.base)
	timestamp := strings.HasPrefix(column.base, "timestamp")
	switch {
	case lower == "null":
		return pgDefault{canonical: "NULL", supported: true}
	case numberForm.MatchString(text):
		return pgDefault{canonical: text, supported: numeric && column.supported}
	case lower == "true" || lower == "false":
		return pgDefault{canonical: lower, supported: column.base == "boolean"}
	}
	if fn, ok := defaultFunctions[lower]; ok {
		supported := (fn == "gen_random_uuid()" && column.base == "uuid") ||
			(fn == "CURRENT_DATE" && (column.base == "date" || timestamp)) ||
			((fn == "now()" || fn == "CURRENT_TIMESTAMP") && timestamp)
		return pgDefault{canonical: fn, supported: supported}
	}
	if literal, cast, ok := stringLiteral(text); ok {
		if cast == "" {
			return pgDefault{canonical: literal, supported: column.supported}
		}
		if t, ok := parseType(cast); ok && (t.canonical == column.canonical || t.base == column.base) {
			return pgDefault{canonical: literal, supported: column.supported && t.supported}
		}
	}
	return pgDefault{canonical: text}
}

// stringLiteral matches 'text' or 'text'::type with only doubled quotes and no
// backslashes, which is safe regardless of standard_conforming_strings.
func stringLiteral(text string) (literal, cast string, ok bool) {
	if !strings.HasPrefix(text, "'") {
		return "", "", false
	}
	for i := 1; i < len(text); i++ {
		switch text[i] {
		case '\\':
			return "", "", false
		case '\'':
			if i+1 < len(text) && text[i+1] == '\'' {
				i++
				continue
			}
			literal, rest := text[:i+1], strings.TrimSpace(text[i+1:])
			if rest == "" {
				return literal, "", true
			}
			if strings.HasPrefix(rest, "::") {
				return literal, strings.TrimSpace(rest[2:]), true
			}
			return "", "", false
		}
	}
	return "", "", false
}

// normalize returns comparison-ready tables sorted by name, with canonical
// types and defaults; column order is preserved.
func (s Snapshot) normalize() []Table {
	tables := make([]Table, 0, len(s.Tables))
	for _, t := range s.Tables {
		n := Table{Name: t.Name, PrimaryKey: slices.Clone(t.PrimaryKey), Indexes: sortedDefinitions(t.Indexes), Constraints: sortedDefinitions(t.Constraints)}
		for _, c := range t.Columns {
			typ, _ := parseType(c.Type)
			column := Column{Name: c.Name, Type: typ.canonical, Nullable: c.Nullable}
			if c.Default != nil {
				if d := parseDefault(*c.Default, typ); d.canonical != "NULL" {
					column.Default = &d.canonical
				}
			}
			n.Columns = append(n.Columns, column)
		}
		tables = append(tables, n)
	}
	slices.SortFunc(tables, func(a, b Table) int { return strings.Compare(a.Name, b.Name) })
	return tables
}

func sortedDefinitions(in []Definition) []Definition {
	out := make([]Definition, len(in))
	for i, d := range in {
		out[i] = Definition{Name: d.Name, Definition: strings.TrimSpace(spaceRun.ReplaceAllString(d.Definition, " "))}
	}
	slices.SortFunc(out, func(a, b Definition) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Fingerprint identifies the normalized schema content (not capture time).
func (s Snapshot) Fingerprint() string {
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(struct {
		Dialect string  `json:"dialect"`
		Schema  string  `json:"schema"`
		Tables  []Table `json:"tables"`
	}{s.Dialect, s.Schema, s.normalize()})
	sum := sha256.Sum256(body.Bytes())
	return hex.EncodeToString(sum[:])
}

// WithoutInternal drops the reserved receipt schema from a live snapshot.
func (s Snapshot) WithoutInternal() Snapshot {
	if strings.EqualFold(s.Schema, InternalSchema) {
		s.Tables = nil
	}
	return s
}
