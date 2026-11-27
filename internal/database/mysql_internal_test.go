package database

import (
	"errors"
	"testing"
)

func TestMySQLResolveUniqueMetadataName(t *testing.T) {
	if name, err := mysqlUniqueMetadataName([]string{"Orders"}); err != nil || name != "Orders" {
		t.Fatalf("unique metadata name = %q, %v", name, err)
	}
	if _, err := mysqlUniqueMetadataName(nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty metadata matches = %v, want ErrNotFound", err)
	}
	if _, err := mysqlUniqueMetadataName([]string{"Orders", "orders"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("ambiguous metadata matches = %v, want ErrConflict", err)
	}
}
