package access

import (
	"errors"
	"testing"
)

func TestPermissionStringAndParse(t *testing.T) {
	for _, tc := range []struct {
		value Permission
		text  string
	}{
		{None, "none"},
		{View, "view"},
		{Edit, "edit"},
	} {
		if got := tc.value.String(); got != tc.text {
			t.Errorf("%d.String() = %q, want %q", tc.value, got, tc.text)
		}
		got, err := ParsePermission(tc.text)
		if err != nil || got != tc.value {
			t.Errorf("ParsePermission(%q) = %d, %v; want %d", tc.text, got, err, tc.value)
		}
	}
	if !(None < View && View < Edit) {
		t.Fatal("permission ordering must be None < View < Edit")
	}
	if got := Permission(255).String(); got != "invalid" {
		t.Errorf("unknown permission String() = %q, want invalid", got)
	}
	for _, text := range []string{"", "None", " view", "edit ", "unknown"} {
		if got, err := ParsePermission(text); !errors.Is(err, ErrInvalidPermission) {
			t.Errorf("ParsePermission(%q) = %d, %v; want ErrInvalidPermission", text, got, err)
		}
	}
}
