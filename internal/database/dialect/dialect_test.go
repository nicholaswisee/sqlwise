package dialect

import "testing"

type quoteCase struct{ in, want string }

func TestPostgresQuotingAndPlaceholders(t *testing.T) {
	for _, tt := range []quoteCase{
		{"users", `"users"`},
		{"Mixed Case", `"Mixed Case"`},
		{`we"ird`, `"we""ird"`},
		{`"; DROP TABLE x; --`, `"""; DROP TABLE x; --"`},
		{"表", `"表"`},
		{"select", `"select"`},
	} {
		if got := Postgres.Quote(tt.in); got != tt.want {
			t.Fatalf("Quote(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if got := Postgres.Qualify("sales", `o"rders`); got != `"sales"."o""rders"` {
		t.Fatalf("Qualify = %q", got)
	}
	if Postgres.Placeholder(1) != "$1" || Postgres.Placeholder(12) != "$12" || Postgres.Name() != "postgresql" {
		t.Fatal("postgres placeholder or name")
	}
}

func TestMySQLQuotingAndPlaceholders(t *testing.T) {
	for _, tt := range []quoteCase{
		{"users", "`users`"},
		{"we`ird", "`we``ird`"},
		{"`; DROP TABLE x; --", "```; DROP TABLE x; --`"},
		{`dou"ble`, "`dou\"ble`"},
		{"表", "`表`"},
	} {
		if got := MySQL.Quote(tt.in); got != tt.want {
			t.Fatalf("Quote(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if got := MySQL.Qualify("shop", "orders"); got != "`shop`.`orders`" {
		t.Fatalf("Qualify = %q", got)
	}
	if MySQL.Placeholder(3) != "?" || MySQL.Name() != "mysql" {
		t.Fatal("mysql placeholder or name")
	}
}
