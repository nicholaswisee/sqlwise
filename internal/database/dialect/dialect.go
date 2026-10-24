package dialect

import (
	"strconv"
	"strings"
)

type Dialect interface {
	Name() string
	Quote(ident string) string
	Qualify(schema, name string) string
	Placeholder(n int) string
}

var (
	Postgres Dialect = postgres{}
	MySQL    Dialect = mysql{}
)

type postgres struct{}

func (postgres) Name() string { return "postgresql" }

func (postgres) Quote(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

func (d postgres) Qualify(schema, name string) string { return d.Quote(schema) + "." + d.Quote(name) }

func (postgres) Placeholder(n int) string { return "$" + strconv.Itoa(n) }

type mysql struct{}

func (mysql) Name() string { return "mysql" }

func (mysql) Quote(ident string) string {
	return "`" + strings.ReplaceAll(ident, "`", "``") + "`"
}

func (d mysql) Qualify(schema, name string) string { return d.Quote(schema) + "." + d.Quote(name) }

func (mysql) Placeholder(int) string { return "?" }
