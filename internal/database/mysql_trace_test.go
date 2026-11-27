package database

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func TestMySQLStatementsAreLoggedLikePostgres(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	ok := func(context.Context) (sql.Result, error) { return driver.RowsAffected(2), nil }
	if _, err := tracedExec(context.Background(), "UPDATE t SET v = 'value-canary'", []any{"arg-canary"}, ok); err != nil {
		t.Fatal(err)
	}
	failed := &mysql.MySQLError{Number: 1146, SQLState: [5]byte{'4', '2', 'S', '0', '2'}, Message: "Table 'shop.missing' doesn't exist"}
	if _, err := tracedExec(context.Background(), "SELECT * FROM missing", nil, func(context.Context) (sql.Result, error) { return nil, failed }); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	logs := output.String()
	for _, expected := range []string{`"event":"sql.start"`, `"event":"sql.result"`, `"rows":2`, `"sqlstate":"42S02"`, `"level":"WARN"`, `[REDACTED string]`} {
		if !strings.Contains(logs, expected) {
			t.Errorf("logs miss %s:\n%s", expected, logs)
		}
	}
	for _, secret := range []string{"value-canary", "arg-canary", "shop.missing"} {
		if strings.Contains(logs, secret) {
			t.Errorf("logs contain %q", secret)
		}
	}
}
