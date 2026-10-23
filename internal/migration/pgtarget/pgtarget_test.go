package pgtarget

import (
	"context"
	"errors"
	"testing"

	"sqlwise/internal/migration"
)

func TestParse(t *testing.T) {
	got, err := Parse(" db-1 = postgres://a@h/x?sslmode=disable ; ;db-2=postgres://b@h/y=z ")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["db-1"] != "postgres://a@h/x?sslmode=disable" || got["db-2"] != "postgres://b@h/y=z" {
		t.Fatalf("Parse = %v", got)
	}
	for _, spec := range []string{"db-1", "=postgres://h/x", "db-1=", "a=postgres://h/x;a=postgres://h/y"} {
		if _, err := Parse(spec); err == nil {
			t.Errorf("Parse(%q) accepted", spec)
		}
	}
}

func TestUnknownTargetIsUnavailable(t *testing.T) {
	r, err := Connect("")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	if d, err := r.Dialect(ctx, "missing"); d != migration.PostgreSQL || err != nil {
		t.Errorf("Dialect = %q, %v", d, err)
	}
	if _, err := r.Open(ctx, "missing"); !errors.Is(err, migration.ErrTargetUnavailable) {
		t.Errorf("Open = %v", err)
	}
	if _, err := r.Snapshot(ctx, "missing", "public"); !errors.Is(err, migration.ErrTargetUnavailable) {
		t.Errorf("Snapshot = %v", err)
	}
	if _, err := Connect("db=::not a url::"); err == nil {
		t.Error("Connect accepted an invalid URL")
	}
}
