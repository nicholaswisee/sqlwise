package database_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"sqlwise/internal/database"
)

func mysqlTarget(t *testing.T) (*sql.DB, database.Connection) {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_URL")
	if dsn == "" {
		t.Skip("set TEST_MYSQL_URL to run MySQL integration tests")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid TEST_MYSQL_URL")
	}
	admin, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	name := "sqlwise_my_" + hex.EncodeToString(suffix[:])
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+name+"`"); err != nil {
		t.Fatalf("create MySQL test database: %v", err)
	}
	t.Cleanup(func() {
		cleanup, err := sql.Open("mysql", dsn)
		if err != nil {
			return
		}
		defer cleanup.Close()
		if _, err := cleanup.Exec("DROP DATABASE IF EXISTS `" + name + "`"); err != nil {
			t.Errorf("drop MySQL test database: %v", err)
		}
	})
	target := cfg.Clone()
	target.DBName = name
	db, err := sql.Open("mysql", target.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	host, port, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := strconv.Atoi(port)
	return db, database.Connection{DBMS: database.MySQL, Host: host, Port: p, Database: name, Username: cfg.User, Password: cfg.Passwd, TLS: database.TLSDisable}
}

type mysqlFixture struct {
	*storeFixture
	target *sql.DB
	conn   database.Connection
	id     string
	pools  *database.Pools
}

func newMySQLFixture(t *testing.T) *mysqlFixture {
	t.Helper()
	target, conn := mysqlTarget(t)
	f := newStoreFixture(t)
	registration := f.register(t, "MySQL target", conn)
	pools := database.NewPools(f.store)
	t.Cleanup(pools.Close)
	return &mysqlFixture{storeFixture: f, target: target, conn: conn, id: registration.ID, pools: pools}
}

func (f *mysqlFixture) exec(t *testing.T, statements ...string) {
	t.Helper()
	for _, s := range statements {
		if _, err := f.target.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func (f *mysqlFixture) open(t *testing.T) database.Handle {
	t.Helper()
	h, err := f.pools.Open(t.Context(), f.id)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestMySQLResolverPingsAndSanitizesFailures(t *testing.T) {
	f := newMySQLFixture(t)
	if d, err := f.pools.DBMS(t.Context(), f.id); err != nil || d != database.MySQL {
		t.Fatalf("DBMS = %q, %v", d, err)
	}
	h := f.open(t)
	if err := h.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	caps := h.Capabilities()
	if h.DBMS() != database.MySQL || !caps.ConnectionTest || !caps.SchemaInspection || !caps.DataPreview || !caps.MigrationExecution || caps.AtomicMigrations || caps.SchemaDiff || caps.Explain {
		t.Fatalf("capabilities = %+v", caps)
	}
	if _, err := database.As[database.TxBeginner](h); !errors.Is(err, database.ErrUnsupportedDialect) {
		t.Fatal("MySQL offers an atomic transaction executor")
	}
	for name, change := range map[string]func(*database.Connection){
		"wrong password":   func(c *database.Connection) { c.Password = "wrong mysql password" },
		"missing database": func(c *database.Connection) { c.Database = "sqlwise_missing_db" },
		"closed port":      func(c *database.Connection) { c.Host, c.Port = "127.0.0.1", 1 },
	} {
		c := f.conn
		change(&c)
		registration := f.register(t, name, c)
		h, err := f.pools.Open(t.Context(), registration.ID)
		if err != nil {
			t.Fatal(err)
		}
		err = h.Ping(t.Context())
		if !errors.Is(err, database.ErrConnectionUnavailable) || database.Message(err) == "" || strings.Contains(err.Error(), c.Password) {
			t.Fatalf("%s: %v", name, err)
		}
		if name == "wrong password" && !strings.Contains(database.Message(err), "username or password") || name == "missing database" && !strings.Contains(database.Message(err), "does not exist") {
			t.Fatalf("%s message = %q", name, database.Message(err))
		}
	}
}

func TestMySQLInspectsSchema(t *testing.T) {
	f := newMySQLFixture(t)
	f.exec(t,
		"CREATE TABLE customers (id INT PRIMARY KEY, email VARCHAR(255) NOT NULL UNIQUE, name VARCHAR(80) DEFAULT 'anon', country CHAR(2) NULL)",
		"CREATE TABLE orders (id BIGINT AUTO_INCREMENT PRIMARY KEY, customer_id INT NOT NULL, amount DECIMAL(10,2) NOT NULL, placed DATETIME NOT NULL, "+
			"CONSTRAINT orders_customer_fk FOREIGN KEY (customer_id) REFERENCES customers (id), CONSTRAINT orders_amount_check CHECK (amount >= 0), INDEX orders_placed_idx (placed))",
		"CREATE TABLE `Odd ``Name` (`select` INT)",
	)
	inspector := must[database.SchemaInspector](t, f.open(t))
	schemas, err := inspector.Schemas(t.Context())
	if err != nil || len(schemas) != 1 || schemas[0] != f.conn.Database {
		t.Fatalf("Schemas = %v, %v", schemas, err)
	}
	if other, err := inspector.InspectSchema(t.Context(), "mysql"); err != nil || len(other.Tables) != 0 {
		t.Fatalf("other database inspected: %+v, %v", other, err)
	}
	snapshot, err := inspector.InspectSchema(t.Context(), f.conn.Database)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.DBMS != database.MySQL || len(snapshot.Tables) != 3 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	byName := map[string]database.Table{}
	for _, table := range snapshot.Tables {
		byName[table.Name] = table
	}
	customers, orders := byName["customers"], byName["orders"]
	if strings.Join(customers.PrimaryKey, ",") != "id" || len(customers.Constraints) != 1 || customers.Constraints[0].Kind != database.Unique || customers.Constraints[0].Definition != "UNIQUE (`email`)" {
		t.Fatalf("customers keys = %+v", customers)
	}
	if c := customers.Columns[1]; c.Type != "varchar(255)" || c.Nullable {
		t.Fatalf("email column = %+v", c)
	}
	if d := customers.Columns[2].Default; d == nil || *d != "anon" {
		t.Fatalf("name default = %v", d)
	}
	kinds := map[database.ConstraintKind]string{}
	for _, c := range orders.Constraints {
		kinds[c.Kind] = c.Definition
	}
	if kinds[database.ForeignKey] != "FOREIGN KEY (`customer_id`) REFERENCES `customers` (`id`)" || !strings.Contains(kinds[database.Check], "amount") {
		t.Fatalf("orders constraints = %+v", orders.Constraints)
	}
	if len(orders.Indexes) != 1 || orders.Indexes[0].Name != "orders_placed_idx" || orders.Indexes[0].Unique {
		t.Fatalf("orders indexes = %+v", orders.Indexes)
	}
	if odd, ok := byName["Odd `Name"]; !ok || odd.Columns[0].Name != "select" {
		t.Fatalf("odd identifiers = %+v", byName)
	}
}

func TestMySQLReadsBoundedPages(t *testing.T) {
	f := newMySQLFixture(t)
	f.exec(t,
		"CREATE TABLE items (id INT PRIMARY KEY, label TEXT NULL, data BLOB NULL)",
		"INSERT INTO items (id, label) WITH RECURSIVE seq (n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < 120) SELECT 121 - n, CONCAT('item ', 121 - n) FROM seq",
		"UPDATE items SET label = NULL WHERE id = 1",
		"UPDATE items SET label = 'héllo 世界', data = X'DEADBEEF' WHERE id = 2",
		"UPDATE items SET label = REPEAT('x', 10000), data = REPEAT(X'AB', 5000) WHERE id = 3",
		"CREATE TABLE keyless (v VARCHAR(10))",
		"INSERT INTO keyless VALUES ('a'), ('b')",
	)
	reader := must[database.TableReader](t, f.open(t))
	read := func(name string, offset, limit int) database.TablePage {
		t.Helper()
		page, err := reader.ReadTable(t.Context(), database.TableRequest{Table: database.TableRef{Schema: f.conn.Database, Name: name}, Offset: offset, Limit: limit})
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	first := read("items", 0, 50)
	if len(first.Rows) != 50 || !first.HasNext || !first.Ordered || first.Rows[0][0].Text != "1" || first.Rows[49][0].Text != "50" {
		t.Fatalf("first page rows=%d next=%v ordered=%v", len(first.Rows), first.HasNext, first.Ordered)
	}
	if !first.Rows[0][1].Null || first.Rows[1][1].Text != "héllo 世界" || !first.Rows[1][2].Binary || first.Rows[1][2].Text != `\xdeadbeef` {
		t.Fatalf("cells = %+v %+v", first.Rows[0], first.Rows[1])
	}
	if label, data := first.Rows[2][1], first.Rows[2][2]; !label.Truncated || len(label.Text) != database.MaxCellBytes || !data.Truncated {
		t.Fatalf("large values label=%d/%v data=%v", len(label.Text), label.Truncated, data.Truncated)
	}
	if last := read("items", 100, 50); len(last.Rows) != 20 || last.HasNext || last.Rows[19][0].Text != "120" {
		t.Fatalf("last page rows=%d", len(last.Rows))
	}
	if keyless := read("keyless", 0, 50); keyless.Ordered || len(keyless.Rows) != 2 {
		t.Fatalf("keyless = %+v", keyless)
	}
	for _, ref := range []database.TableRef{{Schema: f.conn.Database, Name: "missing"}, {Schema: "mysql", Name: "user"}, {Schema: "information_schema", Name: "TABLES"}, {Schema: "sqlwise_demo", Name: "customers"}} {
		if _, err := reader.ReadTable(t.Context(), database.TableRequest{Table: ref, Limit: 50}); !errors.Is(err, database.ErrNotFound) {
			t.Fatalf("%+v = %v", ref, err)
		}
	}
}

func TestMySQLPinnedStatementsCommitIndividually(t *testing.T) {
	f := newMySQLFixture(t)
	pinner := must[database.Pinner](t, f.open(t))
	conn, err := pinner.Pin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Exec(t.Context(), "CREATE TABLE first_step (id INT PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), "CREATE TABLE first_step (id INT)"); err == nil {
		t.Fatal("duplicate table accepted")
	}
	var exists int
	if err := f.target.QueryRow("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'first_step'").Scan(&exists); err != nil || exists != 1 {
		t.Fatalf("first statement rolled back: %d %v", exists, err)
	}
	if n, err := must[database.Executor](t, f.open(t)).Exec(t.Context(), "INSERT INTO first_step VALUES (?), (?)", 1, 2); err != nil || n != 2 {
		t.Fatalf("autocommit Exec = %d, %v", n, err)
	}
}
