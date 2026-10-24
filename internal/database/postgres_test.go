package database_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"sqlwise/internal/database"
	"sqlwise/internal/migration/migrationtest"

	"github.com/jackc/pgx/v5/pgxpool"
)

type targetFixture struct {
	*storeFixture
	target *pgxpool.Pool
	conn   database.Connection
	id     string
	pools  *database.Pools
}

func newTargetFixture(t *testing.T) *targetFixture {
	t.Helper()
	f := newStoreFixture(t)
	target := migrationtest.TargetDatabase(t)
	cc := target.Config().ConnConfig
	conn := database.Connection{DBMS: database.PostgreSQL, Host: cc.Host, Port: int(cc.Port), Database: cc.Database, Username: cc.User, Password: cc.Password, TLS: database.TLSDisable}
	registration := f.register(t, "Target", conn)
	pools := database.NewPools(f.store)
	t.Cleanup(pools.Close)
	return &targetFixture{storeFixture: f, target: target, conn: conn, id: registration.ID, pools: pools}
}

func (f *targetFixture) exec(t *testing.T, sql string) {
	t.Helper()
	if _, err := f.target.Exec(t.Context(), sql); err != nil {
		t.Fatal(err)
	}
}

func (f *targetFixture) open(t *testing.T) database.Handle {
	t.Helper()
	h, err := f.pools.Open(t.Context(), f.id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

func must[T any](t *testing.T, h database.Handle) T {
	t.Helper()
	v, err := database.As[T](h)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestResolverOpensRegisteredPostgres(t *testing.T) {
	f := newTargetFixture(t)
	if d, err := f.pools.DBMS(t.Context(), f.id); err != nil || d != database.PostgreSQL {
		t.Fatalf("DBMS = %q, %v", d, err)
	}
	h := f.open(t)
	if err := h.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	caps := h.Capabilities()
	if h.DBMS() != database.PostgreSQL || !caps.ConnectionTest || !caps.SchemaInspection || !caps.DataPreview || caps.MigrationExecution || caps.Explain {
		t.Fatalf("handle %s capabilities %+v", h.DBMS(), caps)
	}
	for _, id := range []string{"missing", ""} {
		if _, err := f.pools.Open(t.Context(), id); !errors.Is(err, database.ErrNotFound) {
			t.Fatalf("Open(%q) = %v", id, err)
		}
	}
	mysql := f.conn
	mysql.DBMS = database.MySQL
	other := f.register(t, "MySQL target", mysql)
	if d, err := f.pools.DBMS(t.Context(), other.ID); err != nil || d != database.MySQL {
		t.Fatalf("MySQL DBMS = %q, %v", d, err)
	}
	if _, err := f.pools.Open(t.Context(), other.ID); !errors.Is(err, database.ErrUnsupportedDialect) {
		t.Fatalf("MySQL Open = %v", err)
	}
	if err := f.Access.SetDatabaseActive(f.admin, f.id, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pools.Open(t.Context(), f.id); !errors.Is(err, database.ErrNotFound) {
		t.Fatalf("disabled Open = %v", err)
	}
}

func TestResolverInvalidateUsesNewCredentials(t *testing.T) {
	f := newTargetFixture(t)
	if err := f.open(t).Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	wrong := f.conn
	wrong.Password = "wrong password for target"
	if err := f.update(t, f.id, wrong); err != nil {
		t.Fatal(err)
	}
	if err := f.open(t).Ping(t.Context()); err != nil {
		t.Fatalf("cached pool must be reused until invalidated: %v", err)
	}
	f.pools.Invalidate(f.id)
	err := f.open(t).Ping(t.Context())
	if !errors.Is(err, database.ErrConnectionUnavailable) || !strings.Contains(database.Message(err), "username or password") {
		t.Fatalf("wrong password = %v", err)
	}
	if strings.Contains(err.Error(), wrong.Password) || strings.Contains(err.Error(), f.conn.Password) {
		t.Fatal("error leaked a password")
	}
	if err := f.update(t, f.id, f.conn); err != nil {
		t.Fatal(err)
	}
	f.pools.Invalidate(f.id)
	if err := f.open(t).Ping(t.Context()); err != nil {
		t.Fatalf("restored credentials: %v", err)
	}
}

func TestPingFailuresAreSanitized(t *testing.T) {
	f := newTargetFixture(t)
	for name, mutate := range map[string]func(*database.Connection){
		"missing database": func(c *database.Connection) { c.Database = "sqlwise_missing_database" },
		"closed port":      func(c *database.Connection) { c.Host, c.Port = "127.0.0.1", 1 },
	} {
		c := f.conn
		mutate(&c)
		registration := f.register(t, name, c)
		h, err := f.pools.Open(t.Context(), registration.ID)
		if err != nil {
			t.Fatal(err)
		}
		err = h.Ping(t.Context())
		if !errors.Is(err, database.ErrConnectionUnavailable) || database.Message(err) == "" || strings.Contains(err.Error(), c.Password) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	expired, cancel := context.WithTimeout(t.Context(), time.Nanosecond)
	defer cancel()
	<-expired.Done()
	if err := f.open(t).Ping(expired); !errors.Is(err, database.ErrTimeout) {
		t.Fatalf("expired ping = %v", err)
	}
}

func TestTransactionsCommitRollbackAndUnknown(t *testing.T) {
	f := newTargetFixture(t)
	f.exec(t, `CREATE TABLE items (id int PRIMARY KEY); CREATE TABLE deferred (x int UNIQUE DEFERRABLE INITIALLY DEFERRED)`)
	h := f.open(t)
	exec := must[database.Executor](t, h)
	begin := must[database.TxBeginner](t, h)
	count := func(table string) int {
		var n int
		if err := exec.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n, err := exec.Exec(t.Context(), `INSERT INTO items VALUES ($1), ($2)`, 1, 2); err != nil || n != 2 {
		t.Fatalf("autocommit Exec = %d, %v", n, err)
	}

	tx, err := begin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `INSERT INTO items VALUES (3)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}

	tx, _ = begin.Begin(t.Context())
	if _, err := tx.Exec(t.Context(), `INSERT INTO items VALUES (4)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}

	tx, _ = begin.Begin(t.Context())
	tx.Exec(t.Context(), `INSERT INTO items VALUES (5)`)
	if _, err := tx.Exec(t.Context(), `INSERT INTO items VALUES (1)`); err == nil {
		t.Fatal("duplicate key accepted")
	}
	if err := tx.Commit(t.Context()); err == nil || errors.Is(err, database.ErrOutcomeUnknown) {
		t.Fatalf("commit after failed statement = %v", err)
	}

	tx, _ = begin.Begin(t.Context())
	tx.Exec(t.Context(), `INSERT INTO deferred VALUES (1), (1)`)
	if err := tx.Commit(t.Context()); err == nil || errors.Is(err, database.ErrOutcomeUnknown) {
		t.Fatalf("deferred violation commit = %v", err)
	}

	tx, _ = begin.Begin(t.Context())
	tx.Exec(t.Context(), `INSERT INTO items VALUES (6)`)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := tx.Commit(canceled); !errors.Is(err, database.ErrOutcomeUnknown) {
		t.Fatalf("commit without reply = %v", err)
	}
	tx.Rollback(t.Context())

	if items, deferred := count("items"), count("deferred"); items != 3 || deferred != 0 {
		t.Fatalf("items=%d deferred=%d", items, deferred)
	}
}

func TestInspectSchemaMatchesCatalog(t *testing.T) {
	f := newTargetFixture(t)
	f.exec(t, `CREATE SCHEMA sales;
		CREATE TABLE sales.customers (id int PRIMARY KEY, email varchar(255) NOT NULL UNIQUE, name text DEFAULT 'anon');
		CREATE TABLE sales.orders (
			id bigint GENERATED ALWAYS AS IDENTITY,
			customer_id int REFERENCES sales.customers(id),
			amount numeric(10,2) NOT NULL CHECK (amount >= 0),
			placed timestamptz DEFAULT now(),
			PRIMARY KEY (id));
		CREATE INDEX orders_placed_idx ON sales.orders (placed);
		CREATE UNIQUE INDEX orders_customer_placed_key ON sales.orders (customer_id, placed);
		CREATE TABLE public.customers (id int);
		CREATE TABLE public."Odd ""Name""" ("select" int);
		CREATE SCHEMA sqlwise_internal;
		CREATE TABLE sqlwise_internal.migration_receipts (attempt_id text PRIMARY KEY)`)
	h := f.open(t)
	inspector := must[database.SchemaInspector](t, h)
	schemas, err := inspector.Schemas(t.Context())
	if err != nil || strings.Join(schemas, ",") != "public,sales" {
		t.Fatalf("Schemas = %v, %v", schemas, err)
	}
	snapshot, err := inspector.InspectSchema(t.Context(), "sales")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.DBMS != database.PostgreSQL || snapshot.Database != f.conn.Database || snapshot.Schema != "sales" || snapshot.CapturedAt.IsZero() || len(snapshot.Tables) != 2 {
		t.Fatalf("snapshot header = %+v", snapshot)
	}
	customers, orders := snapshot.Tables[0], snapshot.Tables[1]
	if customers.Ref() != (database.TableRef{Schema: "sales", Name: "customers"}) || orders.Name != "orders" {
		t.Fatalf("tables = %s, %s", customers.Name, orders.Name)
	}
	if got := fmt.Sprint(customers.Columns[1].Name, customers.Columns[1].Type, customers.Columns[1].Nullable); got != "emailcharacter varying(255)false" {
		t.Fatalf("email column = %s", got)
	}
	if d := customers.Columns[2].Default; d == nil || *d != "'anon'::text" || customers.Columns[0].Default != nil {
		t.Fatalf("defaults = %v, %v", d, customers.Columns[0].Default)
	}
	if strings.Join(customers.PrimaryKey, ",") != "id" || len(customers.Constraints) != 1 || customers.Constraints[0].Kind != database.Unique || strings.Join(customers.Constraints[0].Columns, ",") != "email" || len(customers.Indexes) != 0 {
		t.Fatalf("customers keys = %+v", customers)
	}
	kinds := map[database.ConstraintKind]database.Constraint{}
	for _, c := range orders.Constraints {
		kinds[c.Kind] = c
	}
	if len(orders.Constraints) != 2 || !strings.Contains(kinds[database.ForeignKey].Definition, "REFERENCES sales.customers(id)") || !strings.Contains(kinds[database.Check].Definition, "amount >= ") {
		t.Fatalf("orders constraints = %+v", orders.Constraints)
	}
	if len(orders.Indexes) != 2 || orders.Indexes[0].Name != "orders_customer_placed_key" || !orders.Indexes[0].Unique || orders.Indexes[1].Unique || !strings.HasPrefix(orders.Indexes[1].Definition, "CREATE INDEX orders_placed_idx") {
		t.Fatalf("orders indexes = %+v", orders.Indexes)
	}
	if orders.Columns[3].Type != "timestamp with time zone" || orders.Columns[3].Default == nil || *orders.Columns[3].Default != "now()" {
		t.Fatalf("placed column = %+v", orders.Columns[3])
	}
	public, err := inspector.InspectSchema(t.Context(), "public")
	if err != nil || len(public.Tables) != 2 || public.Tables[0].Name != `Odd "Name"` || public.Tables[0].Columns[0].Name != "select" {
		t.Fatalf("public = %+v, %v", public, err)
	}
	internal, err := inspector.InspectSchema(t.Context(), database.InternalSchema)
	if err != nil || len(internal.Tables) != 0 {
		t.Fatalf("internal = %+v, %v", internal, err)
	}
}

func TestReadTablePagesInSQL(t *testing.T) {
	f := newTargetFixture(t)
	f.exec(t, `CREATE SCHEMA sales;
		CREATE TABLE public.items (id int PRIMARY KEY, label text, data bytea);
		INSERT INTO public.items SELECT g, 'item ' || g, NULL FROM generate_series(120, 1, -1) g;
		UPDATE public.items SET label = NULL WHERE id = 1;
		UPDATE public.items SET label = 'héllo 世界', data = '\xdeadbeef' WHERE id = 2;
		UPDATE public.items SET label = repeat('x', 10000), data = decode(repeat('ab', 5000), 'hex') WHERE id = 3;
		CREATE TABLE sales.items (id int PRIMARY KEY, label text);
		INSERT INTO sales.items VALUES (1, 'sales');
		CREATE TABLE public.keyless (v text);
		INSERT INTO public.keyless VALUES ('a'), ('b');
		CREATE TABLE public.empty (id int);
		CREATE TABLE public."we""ird" ("sel ect" int);
		INSERT INTO public."we""ird" VALUES (7);
		CREATE TABLE public.wide (a text, b text, c text, d text, e text);
		INSERT INTO public.wide SELECT repeat('w', 4096), repeat('w', 4096), repeat('w', 4096), repeat('w', 4096), repeat('w', 4096) FROM generate_series(1, 200);
		CREATE SCHEMA sqlwise_internal;
		CREATE TABLE sqlwise_internal.migration_receipts (attempt_id text)`)
	reader := must[database.TableReader](t, f.open(t))
	read := func(schema, name string, offset, limit int) database.TablePage {
		t.Helper()
		page, err := reader.ReadTable(t.Context(), database.TableRequest{Table: database.TableRef{Schema: schema, Name: name}, Offset: offset, Limit: limit})
		if err != nil {
			t.Fatalf("%s.%s: %v", schema, name, err)
		}
		return page
	}
	first := read("public", "items", 0, database.DefaultPageSize)
	if len(first.Rows) != 50 || !first.HasNext || !first.Ordered || first.Truncated || len(first.Columns) != 3 || first.Columns[2].Type != "bytea" {
		t.Fatalf("first page rows=%d next=%v ordered=%v columns=%+v", len(first.Rows), first.HasNext, first.Ordered, first.Columns)
	}
	if first.Rows[0][0].Text != "1" || first.Rows[49][0].Text != "50" {
		t.Fatalf("not ordered by key: %s..%s", first.Rows[0][0].Text, first.Rows[49][0].Text)
	}
	if c := first.Rows[0][1]; !c.Null {
		t.Fatalf("NULL label = %+v", c)
	}
	if c := first.Rows[1][1]; c.Text != "héllo 世界" || c.Null {
		t.Fatalf("unicode label = %+v", c)
	}
	if c := first.Rows[1][2]; !c.Binary || c.Text != `\xdeadbeef` {
		t.Fatalf("bytea = %+v", c)
	}
	if label, data := first.Rows[2][1], first.Rows[2][2]; !label.Truncated || len(label.Text) != database.MaxCellBytes || !data.Truncated || len(data.Text) > database.MaxCellBytes {
		t.Fatalf("large values label=%d/%v data=%d/%v", len(label.Text), label.Truncated, len(data.Text), data.Truncated)
	}
	last := read("public", "items", 100, 50)
	if len(last.Rows) != 20 || last.HasNext || last.Rows[19][0].Text != "120" {
		t.Fatalf("last page rows=%d next=%v", len(last.Rows), last.HasNext)
	}
	if beyond := read("public", "items", 500, 50); len(beyond.Rows) != 0 || beyond.HasNext {
		t.Fatalf("beyond end = %+v", beyond)
	}
	if sales := read("sales", "items", 0, 50); len(sales.Rows) != 1 || sales.Rows[0][1].Text != "sales" {
		t.Fatalf("sales.items = %+v", sales.Rows)
	}
	if keyless := read("public", "keyless", 0, 50); keyless.Ordered || len(keyless.Rows) != 2 {
		t.Fatalf("keyless = %+v", keyless)
	}
	if empty := read("public", "empty", 0, 50); len(empty.Rows) != 0 || empty.HasNext || len(empty.Columns) != 1 {
		t.Fatalf("empty = %+v", empty)
	}
	if odd := read("public", `we"ird`, 0, 50); odd.Columns[0].Name != "sel ect" || odd.Rows[0][0].Text != "7" {
		t.Fatalf("odd identifiers = %+v", odd)
	}
	wide := read("public", "wide", 0, database.MaxPageSize)
	size := 0
	for _, row := range wide.Rows {
		for _, c := range row {
			size += len(c.Text)
		}
	}
	if !wide.Truncated || !wide.HasNext || len(wide.Rows) == 0 || len(wide.Rows) >= 200 || size > database.MaxPageBytes {
		t.Fatalf("wide page rows=%d truncated=%v size=%d", len(wide.Rows), wide.Truncated, size)
	}
	for _, ref := range []database.TableRef{{Schema: "public", Name: "missing"}, {Schema: database.InternalSchema, Name: "migration_receipts"}, {Schema: "pg_catalog", Name: "pg_class"}} {
		_, err := reader.ReadTable(t.Context(), database.TableRequest{Table: ref, Limit: 50})
		if !errors.Is(err, database.ErrNotFound) {
			t.Fatalf("%+v = %v", ref, err)
		}
	}
}
