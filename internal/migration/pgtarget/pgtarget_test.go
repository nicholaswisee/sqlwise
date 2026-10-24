package pgtarget

import (
	"context"
	"errors"
	"testing"

	"sqlwise/internal/migration"
)

func TestUnknownTargetIsUnavailable(t *testing.T) {
	r := NewRegistry()
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
}
