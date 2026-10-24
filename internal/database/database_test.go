package database

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"sqlwise/internal/database/dialect"
)

func TestParseDBMSSelectsDialectAndRejectsUnknown(t *testing.T) {
	for name, want := range map[string]dialect.Dialect{"postgresql": dialect.Postgres, "mysql": dialect.MySQL} {
		d, err := ParseDBMS(name)
		if err != nil || d.String() != name {
			t.Fatalf("ParseDBMS(%q) = %q, %v", name, d, err)
		}
		if got, err := d.Dialect(); err != nil || got != want {
			t.Fatalf("%s dialect = %v, %v", name, got, err)
		}
	}
	for _, name := range []string{"", "postgres", "PostgreSQL", "sqlite", "mysql "} {
		if _, err := ParseDBMS(name); !errors.Is(err, ErrUnsupportedDialect) || Message(err) == "" {
			t.Fatalf("ParseDBMS(%q) error = %v", name, err)
		}
	}
	if _, err := DBMS("oracle").Dialect(); !errors.Is(err, ErrUnsupportedDialect) {
		t.Fatalf("unknown dialect error = %v", err)
	}
	if PostgreSQL.Label() != "PostgreSQL" || MySQL.Label() != "MySQL" {
		t.Fatal("DBMS labels")
	}
}

func TestCapabilitiesDefaultToUnsupported(t *testing.T) {
	list := Capabilities{}.List()
	if len(list) != 8 {
		t.Fatalf("capabilities = %d, want 8", len(list))
	}
	for _, c := range list {
		if c.Supported {
			t.Fatalf("zero capability %q is supported", c.Name)
		}
	}
	all := Capabilities{true, true, true, true, true, true, true, true}.List()
	for _, c := range all {
		if !c.Supported {
			t.Fatalf("capability %q is not listed as supported", c.Name)
		}
	}
}

func TestErrorsMapToKindsWithSafeMessages(t *testing.T) {
	err := Unavailable()
	if !errors.Is(err, ErrConnectionUnavailable) || Message(err) != err.Error() {
		t.Fatalf("Unavailable() = %v", err)
	}
	if Message(errors.New("dial tcp user:secret@host")) != "" {
		t.Fatal("non-domain error exposed a message")
	}
	if FromContext(context.Background()) != nil {
		t.Fatal("live context mapped to an error")
	}
	expired, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	<-expired.Done()
	if err := FromContext(expired); !errors.Is(err, ErrTimeout) || Message(err) == "" {
		t.Fatalf("deadline = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := FromContext(canceled); !errors.Is(err, ErrConnectionUnavailable) {
		t.Fatalf("cancel = %v", err)
	}
}

type inspectOnly struct{}

func (inspectOnly) DBMS() DBMS { return MySQL }

func (inspectOnly) Capabilities() Capabilities { return Capabilities{SchemaInspection: true} }

func (inspectOnly) Ping(context.Context) error { return nil }

func (inspectOnly) Close() error { return nil }

func (inspectOnly) Schemas(context.Context) ([]string, error) { return nil, nil }

func (inspectOnly) InspectSchema(context.Context, string) (SchemaSnapshot, error) {
	return SchemaSnapshot{}, nil
}

func TestAsSelectsOptionalOperations(t *testing.T) {
	var h Handle = inspectOnly{}
	if _, err := As[SchemaInspector](h); err != nil {
		t.Fatalf("inspector: %v", err)
	}
	for _, err := range []error{asErr[TxBeginner](h), asErr[Pinner](h), asErr[TableReader](h), asErr[Executor](h)} {
		if !errors.Is(err, ErrUnsupportedDialect) || !strings.Contains(Message(err), "MySQL") {
			t.Fatalf("unsupported operation error = %v", err)
		}
	}
}

func asErr[T any](h Handle) error {
	_, err := As[T](h)
	return err
}

func TestWithoutInternalDropsOnlyReceiptSchema(t *testing.T) {
	internal := SchemaSnapshot{Schema: "SQLWISE_INTERNAL", Tables: []Table{{Name: "migration_receipts"}}}
	if got := internal.WithoutInternal(); got.Tables != nil {
		t.Fatalf("internal tables = %+v", got.Tables)
	}
	public := SchemaSnapshot{Schema: "public", Tables: []Table{{Schema: "public", Name: "t"}}}
	if got := public.WithoutInternal(); len(got.Tables) != 1 || got.Tables[0].Ref() != (TableRef{"public", "t"}) {
		t.Fatalf("public snapshot = %+v", got)
	}
}

func TestTableIDIsSchemaQualified(t *testing.T) {
	for _, ref := range []TableRef{{"public", "users"}, {"sales", "users"}, {`we"ird`, "tab.le/../x"}, {"ünï", "表"}} {
		got, err := ParseTableID(ref.ID())
		if err != nil || got != ref {
			t.Fatalf("round trip %+v = %+v, %v", ref, got, err)
		}
		if strings.ContainsAny(ref.ID(), "/+=") {
			t.Fatalf("ID %q is not URL safe", ref.ID())
		}
	}
	if (TableRef{"public", "users"}).ID() == (TableRef{"sales", "users"}).ID() {
		t.Fatal("same table name in different schemas shares an ID")
	}
	for _, id := range []string{"!!", "cHVibGlj"} {
		if _, err := ParseTableID(id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("ParseTableID(%q) error = %v", id, err)
		}
	}
}

func TestParsePageBounds(t *testing.T) {
	tests := []struct {
		offset, limit string
		wantOffset    int
		wantLimit     int
		ok            bool
	}{
		{"", "", 0, DefaultPageSize, true},
		{"0", "1", 0, 1, true},
		{"150", "200", 150, 200, true},
		{"-1", "", 0, 0, false},
		{"", "0", 0, 0, false},
		{"", "201", 0, 0, false},
		{"1.5", "", 0, 0, false},
		{"", "abc", 0, 0, false},
		{"99999999999999999999", "", 0, 0, false},
	}
	for _, tt := range tests {
		o, l, err := ParsePage(tt.offset, tt.limit)
		if tt.ok != (err == nil) || (tt.ok && (o != tt.wantOffset || l != tt.wantLimit)) {
			t.Fatalf("ParsePage(%q, %q) = %d, %d, %v", tt.offset, tt.limit, o, l, err)
		}
		if !tt.ok && !errors.Is(err, ErrValidation) {
			t.Fatalf("ParsePage(%q, %q) error kind = %v", tt.offset, tt.limit, err)
		}
	}
	if DefaultPageSize != 50 || MaxPageSize != 200 {
		t.Fatal("page limits differ from the contract")
	}
}

func TestCellsShowNullAndTruncation(t *testing.T) {
	if c := TextCell("abc", 3); c.Truncated || c.Text != "abc" {
		t.Fatalf("short cell = %+v", c)
	}
	if c := TextCell("aé€😀", 5); !c.Truncated || c.Text != "aé" {
		t.Fatalf("utf8 cut = %+v", c)
	}
	if c := TextCell("", 10); c.Null || c.Text != "" {
		t.Fatalf("empty text cell = %+v", c)
	}
	if c := NullCell(); !c.Null {
		t.Fatal("null cell is not null")
	}
	if c := BinaryCell([]byte{0xde, 0xad, 0xbe, 0xef}, 10); !c.Binary || c.Truncated || c.Text != `\xdeadbeef` {
		t.Fatalf("binary cell = %+v", c)
	}
	if c := BinaryCell([]byte{0xde, 0xad, 0xbe, 0xef}, 8); !c.Truncated || c.Text != `\xdeadbe` {
		t.Fatalf("truncated binary cell = %+v", c)
	}
}

func TestPackageDoesNotImportConsumers(t *testing.T) {
	forbidden := []string{"sqlwise/internal/auth", "sqlwise/internal/access", "sqlwise/internal/migration", "sqlwise/internal/schemadiff", "sqlwise/internal/queryplan", "sqlwise/internal/handlers"}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			imported, _ := strconv.Unquote(spec.Path.Value)
			for _, f := range forbidden {
				if imported == f || strings.HasPrefix(imported, f+"/") {
					t.Errorf("%s imports %s", path, imported)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
